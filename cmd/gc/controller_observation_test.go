package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/config"
	"github.com/gastownhall/gascity/internal/runtime"
	"github.com/gastownhall/gascity/internal/runtime/observation"
	"github.com/gastownhall/gascity/internal/runtime/procobserver"
)

func TestControllerObservationLateReadyAndSingleFlight(t *testing.T) {
	svc := newControllerObservationService(context.Background(), "/city")
	if got := svc.observe(context.Background()); got.ProcessComplete || len(got.UnknownReasons) == 0 {
		t.Fatal("notready became complete")
	}
	cs := &controllerState{cityPath: "/city", cfg: &config.City{}, sp: runtime.NewFake()}
	svc.install(cs)
	started := make(chan struct{})
	release := make(chan struct{})
	svc.loadPolicy = func() (procobserver.Policy, error) { return procobserver.Policy{}, nil }
	svc.checkCaller = func(procobserver.Policy) error { return nil }
	svc.collect = func(ctx context.Context, _ procobserver.Policy, _ runtime.Provider) controllerObservationReply {
		close(started)
		select {
		case <-release:
		case <-ctx.Done():
		}
		return svc.unknown("fixture partial")
	}
	done := make(chan controllerObservationReply, 1)
	go func() { done <- svc.observe(context.Background()) }()
	<-started
	if got := svc.observe(context.Background()); got.ProcessComplete || len(got.UnknownReasons) != 1 || got.UnknownReasons[0] != "observation busy" {
		t.Fatalf("burst queued %+v", got)
	}
	close(release)
	<-done
}

func TestControllerObservationPreservesTypedHelperFailures(t *testing.T) {
	svc := newControllerObservationService(context.Background(), "/city")
	calls := 0
	svc.readEvidence = func(context.Context, procobserver.Policy) (procobserver.Response, error) {
		calls++
		now := time.Now().UTC()
		return procobserver.Response{
			Schema: procobserver.Schema, KernelRelease: "6.8.0-fixture", KernelProofProfile: procobserver.KernelProofProfile, Census: controllerFixtureCensus(3, strings.Repeat("a", 64)), StartedAt: now, FinishedAt: now,
			EnumeratedCountBefore: 324, EnumeratedCountAfter: 322,
			EnumerationDigestBefore: strings.Repeat("a", 64), EnumerationDigestAfter: strings.Repeat("b", 64),
			Roots: []procobserver.Root{}, Errors: []procobserver.EvidenceError{{Reason: "process_unavailable", Operation: "environ", PID: 29, Errno: 3}},
			ErrorsTotal: 303, ErrorsTruncated: true,
		}, fmt.Errorf("observer process coverage incomplete")
	}
	r := svc.collect(context.Background(), procobserver.Policy{}, runtime.NewFake())
	if calls != 2 || r.ProcessComplete || len(r.ProcessDiagnostics) != 2 {
		t.Fatalf("partial evidence changed meaning or lost frames: calls=%d reply=%+v", calls, r)
	}
	for _, d := range r.ProcessDiagnostics {
		if d.ErrorsTotal != 303 || !d.ErrorsTruncated || d.Errors[0].PID != 29 || d.Errors[0].Errno != 3 || d.EnumeratedCountBefore != 324 || d.EnumeratedCountAfter != 322 {
			t.Fatalf("typed diagnostic lost: %+v", d)
		}
	}
	raw, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	var decoded controllerObservationReply
	if err = procobserver.DecodeStrict(raw, &decoded); err != nil || len(decoded.ProcessDiagnostics) != 2 {
		t.Fatalf("typed diagnostic did not survive strict wire: %s %v", raw, err)
	}
}

func TestControllerObservationDiagnosticsCannotMakePartialComplete(t *testing.T) {
	r := controllerObservationFixture("/city")
	r.ProcessDiagnostics = []controllerProcessDiagnostics{{
		StartedAt: r.ObservedAt, FinishedAt: r.FinishedAt,
		EnumeratedCountBefore: 1, EnumeratedCountAfter: 1, EnumerationDigestBefore: strings.Repeat("a", 64), EnumerationDigestAfter: strings.Repeat("a", 64),
		Errors: []procobserver.EvidenceError{}, Complete: false,
	}}
	if err := validateControllerObservationReply(r, "/city", r.SourceRevision, time.Now().UTC()); err == nil {
		t.Fatal("incomplete helper diagnostic accepted as complete")
	}
}

func TestControllerCompleteRequiresBothReconciledFrames(t *testing.T) {
	r := controllerObservationFixture("/city")
	r.ProcessDiagnostics = nil
	if validateControllerObservationReply(r, "/city", r.SourceRevision, time.Now().UTC()) == nil {
		t.Fatal("complete promoted without the two independently validated helper frames")
	}
}

// Each attempt changes both its provider incarnation and its helper roots.
// A successful retry must reread those sources, never retain the earlier rows.
type controllerRetryProvider struct {
	*runtime.Fake
	name        string
	trackingErr error
}

func (p *controllerRetryProvider) ListRunning(string) ([]string, error) {
	return []string{p.name}, nil
}

func (p *controllerRetryProvider) TrackProcessRoots(roots []runtime.LiveRuntime) ([]runtime.LiveRuntime, error) {
	out := append([]runtime.LiveRuntime{}, roots...)
	for i := range out {
		out[i].ProviderName = out[i].SessionID
		out[i].IsTracked = true
	}
	return out, p.trackingErr
}

func controllerRetryFixture(t *testing.T) (*controllerObservationService, *controllerState, *controllerRetryProvider) {
	t.Helper()
	p := &controllerRetryProvider{Fake: runtime.NewFake(), name: "earlier"}
	for _, name := range []string{"earlier", "selected"} {
		for key, value := range map[string]string{"GC_SESSION_ID": name, "GC_TEMPLATE": "worker", "GC_RUNTIME_EPOCH": "1", "GC_INSTANCE_TOKEN": "fixture-" + name} {
			if err := p.SetMeta(name, key, value); err != nil {
				t.Fatal(err)
			}
		}
	}
	svc := newControllerObservationService(context.Background(), "/city")
	cs := &controllerState{sp: p}
	svc.install(cs)
	svc.loadPolicy = func() (procobserver.Policy, error) {
		return procobserver.Policy{CallerBinding: procobserver.CallerBinding{PID: os.Getpid(), StartTicks: "456", BootID: "01234567-0123-0123-0123-0123456789ab", ControllerBinarySHA256: strings.Repeat("b", 64)}, HelperBinarySHA256: strings.Repeat("c", 64), PolicyDigest: strings.Repeat("d", 64)}, nil
	}
	svc.checkCaller = func(procobserver.Policy) error { return nil }
	return svc, cs, p
}

func controllerRetryFrame(name string, partial bool) (procobserver.Response, error) {
	now := time.Now().UTC()
	pid := 101
	if name == "selected" {
		pid = 202
	}
	r := procobserver.Response{
		Schema: procobserver.Schema, KernelRelease: "6.8.0-fixture", KernelProofProfile: procobserver.KernelProofProfile, Census: controllerFixtureCensus(3, strings.Repeat("a", 64)), StartedAt: now, FinishedAt: now, Complete: !partial,
		EnumeratedCountBefore: 3, EnumeratedCountAfter: 3, EnumerationDigestBefore: strings.Repeat("a", 64), EnumerationDigestAfter: strings.Repeat("a", 64),
		Roots: []procobserver.Root{{PID: pid, PPID: 1, PGID: pid, StartTicks: "12", City: "/city", SessionID: name, Template: "worker", Epoch: 1, InstanceTokenSHA256: observation.TokenDigest("fixture-" + name)}}, Errors: []procobserver.EvidenceError{},
	}
	if partial {
		r.Errors = []procobserver.EvidenceError{{Reason: "process_unavailable", Operation: "stat", PID: 404, Errno: 2}, {Reason: "coverage_changed", Operation: "enumerate"}}
		r.ErrorsTotal = len(r.Errors)
		r.EnumerationDigestAfter = strings.Repeat("e", 64)
		return r, fmt.Errorf("observer process coverage incomplete")
	}
	return r, nil
}

func TestControllerObservationRetriesWholeAttempt(t *testing.T) {
	for _, mode := range []string{"transient", "second frame only", "all incomplete", "transient environ ESRCH", "second environ ESRCH frame only", "all environ ESRCH incomplete"} {
		t.Run(mode, func(t *testing.T) {
			svc, _, p := controllerRetryFixture(t)
			reads, lists, policyLoads := 0, 0, 0
			load := svc.loadPolicy
			svc.loadPolicy = func() (procobserver.Policy, error) { policyLoads++; return load() }
			var selectedStart, earlierFinish time.Time
			var requestContext context.Context
			svc.readEvidence = func(ctx context.Context, _ procobserver.Policy) (procobserver.Response, error) {
				if requestContext == nil {
					requestContext = ctx
				} else if ctx != requestContext {
					t.Fatal("retry created a new request context")
				}
				reads++
				partial := strings.HasPrefix(mode, "all ") || reads <= 2
				if strings.HasPrefix(mode, "second ") && reads == 1 {
					partial = false
				}
				r, err := controllerRetryFrame(p.name, partial)
				if partial && strings.Contains(mode, "environ ESRCH") {
					r.Errors[0].Operation = "environ"
					r.Errors[0].Errno = 3
					r.Errors[0].PID = 400 + reads
				}
				if reads == 3 {
					selectedStart = r.StartedAt
				}
				if reads == 2 {
					earlierFinish = r.FinishedAt
					p.name = "selected"
				}
				return r, err
			}
			r := svc.observe(context.Background())
			for _, call := range p.SnapshotCalls() {
				if call.Method == "GetMeta" && call.Key == "GC_SESSION_ID" {
					lists++
				}
				if call.Method == "FindRuntimesBySessionID" {
					t.Fatal("retry reverted to unprivileged process scan")
				}
			}
			wantReads := 4
			if strings.HasPrefix(mode, "all ") {
				wantReads = 6
			}
			if reads != wantReads || lists != wantReads || policyLoads != 1 || len(r.ProcessDiagnostics) != 2 {
				t.Fatalf("whole attempts not bounded/fresh: reads=%d metadata=%d policy=%d diagnostics=%d", reads, lists, policyLoads, len(r.ProcessDiagnostics))
			}
			if !r.ProviderComplete || r.ProcessComplete != !strings.HasPrefix(mode, "all ") || len(r.Processes) != 1 || r.Processes[0].PID != 202 || len(r.Sessions) != 1 || r.Sessions[0].SessionID != "selected" {
				t.Fatalf("selected attempt mixed or promoted: %+v", r)
			}
			if mode == "all environ ESRCH incomplete" {
				for i, d := range r.ProcessDiagnostics {
					if d.Complete || d.Errors[0].Operation != "environ" || d.Errors[0].Errno != 3 || d.Errors[0].PID != 405+i {
						t.Fatalf("persistent ESRCH lost final-attempt evidence: %+v", d)
					}
				}
			}
			if !strings.HasPrefix(mode, "all ") {
				if r.ObservedAt.Before(earlierFinish) || r.ObservedAt.After(selectedStart) || r.ProcessDiagnostics[0].StartedAt.Before(r.ObservedAt) || !r.ProcessDiagnostics[0].Complete || !r.ProcessDiagnostics[1].Complete {
					t.Fatal("successful attempt retained earlier frames or wrong interval")
				}
				r.SourceRevision = strings.Repeat("a", 40)
				if err := validateControllerObservationReply(r, "/city", r.SourceRevision, time.Now().UTC()); err != nil {
					t.Fatalf("successful selected attempt rejected by wire validator: %v", err)
				}
			}
		})
	}
}

func TestControllerObservationRetryTerminalFailures(t *testing.T) {
	for _, mode := range []string{"pin", "future", "truncated", "permission", "environment ENOENT", "environment EACCES", "environment EPERM", "environment zero pid", "environment malformed", "mixed environment ESRCH and EPERM", "limit", "zero pid", "unidentified", "provider incarnation", "tracking", "empty diagnostics"} {
		t.Run(mode, func(t *testing.T) {
			svc, _, p := controllerRetryFixture(t)
			reads := 0
			svc.readEvidence = func(context.Context, procobserver.Policy) (procobserver.Response, error) {
				reads++
				r, err := controllerRetryFrame(p.name, true)
				switch mode {
				case "pin":
					return procobserver.Response{}, fmt.Errorf("observer response binding mismatch")
				case "future":
					r.FinishedAt = time.Now().UTC().Add(time.Hour)
				case "truncated":
					r.ErrorsTruncated = true
					r.ErrorsTotal++
				case "permission":
					r.Errors[0].Errno = 13
				case "environment ENOENT":
					r.Errors[0].Operation = "environ"
				case "environment EACCES", "environment EPERM", "environment zero pid", "environment malformed", "mixed environment ESRCH and EPERM":
					r.Errors[0].Operation = "environ"
					r.Errors[0].Errno = 3
					switch mode {
					case "environment EACCES":
						r.Errors[0].Errno = 13
					case "environment EPERM":
						r.Errors[0].Errno = 1
					case "environment zero pid":
						r.Errors[0].PID = 0
					case "environment malformed":
						r.Errors[0].Reason = "environment_malformed"
					case "mixed environment ESRCH and EPERM":
						r.Errors = append(r.Errors, procobserver.EvidenceError{Reason: "process_unavailable", Operation: "environ", PID: 405, Errno: 1})
						r.ErrorsTotal = len(r.Errors)
					}
				case "limit":
					r.Errors[0].Reason = "process_limit"
				case "zero pid":
					r.Errors[0].PID = 0
				case "unidentified":
					err = fmt.Errorf("unidentified failure")
				case "provider incarnation":
					if reads == 1 {
						if e := p.SetMeta(p.name, "GC_RUNTIME_EPOCH", "2"); e != nil {
							t.Fatal(e)
						}
					}
				case "tracking":
					p.trackingErr = fmt.Errorf("fixture tracking unavailable")
				case "empty diagnostics":
					r.Errors = []procobserver.EvidenceError{}
					r.ErrorsTotal = 0
				}
				return r, err
			}
			r := svc.observe(context.Background())
			if reads != 2 || r.ProcessComplete {
				t.Fatalf("terminal %s retried/promoted: reads=%d %+v", mode, reads, r)
			}
		})
	}
}

func TestControllerObservationRetryGenerationFence(t *testing.T) {
	svc, cs, p := controllerRetryFixture(t)
	reads := 0
	svc.readEvidence = func(context.Context, procobserver.Policy) (procobserver.Response, error) {
		reads++
		if reads == 2 {
			cs.mu.Lock()
			cs.observationGeneration++
			cs.sp = runtime.NewFake()
			cs.mu.Unlock()
		}
		return controllerRetryFrame(p.name, true)
	}
	if r := svc.observe(context.Background()); reads != 2 || r.ProviderComplete || r.ProcessComplete {
		t.Fatalf("generation changed but retry continued: reads=%d %+v", reads, r)
	}
}

func TestControllerObservationEnvironmentRetryFences(t *testing.T) {
	for _, mode := range []string{"generation", "deadline"} {
		t.Run(mode, func(t *testing.T) {
			svc, cs, p := controllerRetryFixture(t)
			deadline := time.Now().Add(20 * time.Second)
			ctx, cancel := context.WithDeadline(context.Background(), deadline)
			defer cancel()
			reads := 0
			svc.readEvidence = func(ctx context.Context, _ procobserver.Policy) (procobserver.Response, error) {
				reads++
				if got, ok := ctx.Deadline(); !ok || !got.Equal(deadline) {
					t.Fatal("environment retry extended the caller's outer deadline")
				}
				if mode == "generation" && reads == 2 {
					cs.mu.Lock()
					cs.observationGeneration++
					cs.sp = runtime.NewFake()
					cs.mu.Unlock()
				}
				r, err := controllerRetryFrame(p.name, true)
				r.Errors[0].Operation = "environ"
				r.Errors[0].Errno = 3
				return r, err
			}
			r := svc.observe(ctx)
			wantReads := 6
			if mode == "generation" {
				wantReads = 2
			}
			if reads != wantReads || r.ProcessComplete || r.ProviderComplete != (mode == "deadline") {
				t.Fatalf("environment retry crossed %s fence: reads=%d %+v", mode, reads, r)
			}
		})
	}
}

func TestControllerObservationRetryCancellationKeepsFlightUntilCollectionEnds(t *testing.T) {
	for _, mode := range []string{"request", "shutdown"} {
		t.Run(mode, func(t *testing.T) {
			svc, _, p := controllerRetryFixture(t)
			request, cancel := context.WithCancel(context.Background())
			defer cancel()
			if mode == "shutdown" {
				svc.ctx = request
				request = context.Background()
			}
			secondRead, release := make(chan struct{}), make(chan struct{})
			reads := 0
			svc.readEvidence = func(context.Context, procobserver.Policy) (procobserver.Response, error) {
				reads++
				if reads == 2 {
					close(secondRead)
					<-release // Model a provider/helper that has not returned yet.
				}
				return controllerRetryFrame(p.name, true)
			}
			done := make(chan controllerObservationReply, 1)
			go func() { done <- svc.observe(request) }()
			<-secondRead
			cancel()
			if r := <-done; r.ProviderComplete || r.ProcessComplete {
				t.Fatal("canceled attempt returned complete")
			}
			other, _, _ := controllerRetryFixture(t)
			busy := other.observe(context.Background())
			close(release)
			// This barrier can acquire the global slot only after the outstanding
			// read really returns; no sleeps or new test timing races are needed.
			controllerObservationFlight <- struct{}{}
			<-controllerObservationFlight
			if reads != 2 || len(busy.UnknownReasons) != 1 || busy.UnknownReasons[0] != "observation busy" {
				t.Fatalf("cancellation released flight early or retried: reads=%d busy=%+v", reads, busy)
			}
		})
	}
}

func TestControllerObservationRetryRetainsCallerDeadline(t *testing.T) {
	svc, _, p := controllerRetryFixture(t)
	deadline := time.Now().Add(20 * time.Second)
	ctx, cancel := context.WithDeadline(context.Background(), deadline)
	defer cancel()
	reads := 0
	svc.readEvidence = func(ctx context.Context, _ procobserver.Policy) (procobserver.Response, error) {
		reads++
		if got, ok := ctx.Deadline(); !ok || !got.Equal(deadline) {
			t.Fatal("attempt extended the caller's outer deadline")
		}
		return controllerRetryFrame(p.name, true)
	}
	if r := svc.observe(ctx); reads != 6 || r.ProcessComplete {
		t.Fatalf("retry/deadline contract changed: reads=%d %+v", reads, r)
	}
	// An already expired request cannot enter an attempt or reset its budget.
	expired, stop := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer stop()
	if r := svc.observe(expired); reads != 6 || r.ProcessComplete || r.ProviderComplete {
		t.Fatal("expired request performed another attempt")
	}
}

func TestControllerObservationProviderSwapAndCancellationDeny(t *testing.T) {
	for _, mode := range []string{"swap", "cancel", "shutdown"} {
		t.Run(mode, func(t *testing.T) {
			root, cancel := context.WithCancel(context.Background())
			defer cancel()
			svc := newControllerObservationService(root, "/city")
			cs := &controllerState{cityPath: "/city", cfg: &config.City{}, sp: runtime.NewFake()}
			svc.install(cs)
			svc.loadPolicy = func() (procobserver.Policy, error) { return procobserver.Policy{}, nil }
			svc.checkCaller = func(procobserver.Policy) error { return nil }
			ctx, requestCancel := context.WithCancel(context.Background())
			defer requestCancel()
			svc.collect = func(context.Context, procobserver.Policy, runtime.Provider) controllerObservationReply {
				switch mode {
				case "swap":
					cs.mu.Lock()
					cs.observationGeneration++
					cs.sp = runtime.NewFake()
					cs.mu.Unlock()
				case "cancel":
					requestCancel()
				case "shutdown":
					cancel()
				}
				r := svc.unknown("")
				r.ProviderComplete = true
				r.ProcessComplete = true
				r.UnknownReasons = []string{}
				return r
			}
			if got := svc.observe(ctx); got.ProviderComplete || got.ProcessComplete {
				t.Fatalf("changed/canceled source complete %+v", got)
			}
		})
	}
}

func TestControllerObservationRelayPreservesDaemonBytesAndDeniesSource(t *testing.T) {
	now := time.Now().UTC()
	reply := controllerObservationReply{Observation: observation.Observation{Schema: observation.Schema, CityPath: "/city", ObservedAt: now, ProcessObservedAt: now, FinishedAt: now, ProviderComplete: true, ProcessComplete: true, Sessions: []observation.Session{}, Processes: []observation.Process{}, UnknownReasons: []string{}}, SourceRevision: strings.Repeat("a", 40), ControllerBinding: "verified_local_process", ControllerPID: os.Getpid(), ControllerBinarySHA256: observation.TokenDigest("binary"), ControllerStartIdentity: "456", ControllerBootID: "01234567-0123-0123-0123-0123456789ab", HelperBinarySHA256: observation.TokenDigest("helper"), HelperPolicyDigest: observation.TokenDigest("policy")}
	reply.ProcessDiagnostics = controllerFixtureDiagnostics(now, now)
	if err := validateControllerObservationReply(reply, "/city", strings.Repeat("a", 40), now); err != nil {
		t.Fatal(err)
	}
	reply.SourceRevision = "wrong"
	if err := validateControllerObservationReply(reply, "/city", strings.Repeat("a", 40), now); err == nil {
		t.Fatal("wrong daemon source accepted")
	}
}

func controllerObservationFixture(city string) controllerObservationReply {
	now := time.Now().UTC().Add(-time.Second)
	r := controllerObservationReply{Observation: observation.Observation{Schema: observation.Schema, CityPath: city, ObservedAt: now, ProcessObservedAt: now, FinishedAt: now, ProviderComplete: true, ProcessComplete: true, ProviderType: "fixture", Sessions: []observation.Session{}, Processes: []observation.Process{}, UnknownReasons: []string{}}, SourceRevision: strings.Repeat("a", 40), ControllerBinding: "verified_local_process", ControllerPID: os.Getpid(), ControllerBinarySHA256: strings.Repeat("b", 64), ControllerStartIdentity: "456", ControllerBootID: "01234567-0123-0123-0123-0123456789ab", HelperBinarySHA256: strings.Repeat("c", 64), HelperPolicyDigest: strings.Repeat("d", 64)}
	r.ProcessDiagnostics = controllerFixtureDiagnostics(now, now)
	return r
}

func TestControllerObservationSocketRelay(t *testing.T) {
	for _, mode := range []string{"exact", "partial", "wrong source", "wrong city", "missing scalar", "null scalar", "duplicate scalar", "extra frame", "oversized", "unsupported"} {
		t.Run(mode, func(t *testing.T) {
			city := t.TempDir()
			if err := os.MkdirAll(filepath.Join(city, ".gc"), 0o700); err != nil {
				t.Fatal(err)
			}
			lis, err := net.Listen("unix", controllerSocketPath(city))
			if err != nil {
				t.Fatal(err)
			}
			defer lis.Close() //nolint:errcheck // isolated fixture cleanup
			r := controllerObservationFixture(city)
			switch mode {
			case "partial":
				r.ProcessComplete = false
				r.UnknownReasons = []string{"fixture permission"}
			case "wrong source":
				r.SourceRevision = strings.Repeat("f", 40)
			case "wrong city":
				r.CityPath = "/other"
			}
			raw, err := json.Marshal(r)
			if err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "missing scalar":
				raw = bytes.Replace(raw, []byte(`"provider_complete":true,`), nil, 1)
			case "null scalar":
				raw = bytes.Replace(raw, []byte(`"provider_complete":true`), []byte(`"provider_complete":null`), 1)
			case "duplicate scalar":
				raw = bytes.Replace(raw, []byte(`"provider_complete":true`), []byte(`"provider_complete":true,"provider_complete":true`), 1)
			case "extra frame":
				raw = append(raw, []byte("\n{}")...)
			case "oversized":
				raw = []byte(strings.Repeat(" ", controllerObservationLimit+1))
			case "unsupported":
				raw = []byte("unsupported\n")
			}
			raw = append(raw, '\n')
			done := make(chan error, 1)
			go func() {
				conn, e := lis.Accept()
				if e != nil {
					done <- e
					return
				}
				defer conn.Close() //nolint:errcheck // isolated fixture cleanup
				line, e := bufio.NewReader(conn).ReadString('\n')
				if e != nil || line != controllerObservationCommand+"\n" {
					done <- fmt.Errorf("wrong fixed request %q: %w", line, e)
					return
				}
				_, e = conn.Write(raw)
				if mode == "oversized" {
					e = nil
				}
				done <- e
			}()
			var out bytes.Buffer
			err = fixtureControllerRelay(context.Background(), city, strings.Repeat("a", 40), &out)
			if mode == "exact" {
				if err != nil || !bytes.Equal(out.Bytes(), raw) {
					t.Fatalf("provenance/bytes rewritten err=%v", err)
				}
			} else if err == nil {
				t.Fatal("invalid/incomplete became complete")
			}
			if mode == "partial" && !bytes.Equal(out.Bytes(), raw) {
				t.Fatal("partial daemon evidence rewritten")
			}
			if e := <-done; e != nil {
				t.Fatal(e)
			}
		})
	}
}

func TestControllerObservationTransportAbsentAndCancellation(t *testing.T) {
	var out bytes.Buffer
	if err := relayControllerObservation(context.Background(), t.TempDir(), strings.Repeat("a", 40), &out); err == nil {
		t.Fatal("socket-down allowed")
	}
	var r controllerObservationReply
	if err := procobserver.DecodeStrict(out.Bytes(), &r); err != nil || r.ProcessComplete || r.ProviderComplete {
		t.Fatalf("socket-down not typed UNKNOWN: %v", err)
	}
	city := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	svc := newControllerObservationService(ctx, city)
	defer cancel()
	svc.install(&controllerState{sp: runtime.NewFake()})
	svc.loadPolicy = func() (procobserver.Policy, error) { return procobserver.Policy{}, nil }
	svc.checkCaller = func(procobserver.Policy) error { return nil }
	started := make(chan struct{})
	canceled := make(chan struct{})
	svc.collect = func(ctx context.Context, _ procobserver.Policy, _ runtime.Provider) controllerObservationReply {
		close(started)
		<-ctx.Done()
		close(canceled)
		return svc.unknown("fixture canceled")
	}
	lis, err := startLegacyObservationTestSocket(city, controllerSocketOptions{observe: svc.observe})
	if err != nil {
		t.Fatal(err)
	}
	defer lis.Close() //nolint:errcheck // isolated fixture cleanup
	conn, err := net.Dial("unix", controllerSocketPath(city))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = io.WriteString(conn, controllerObservationCommand+"\n"); err != nil {
		t.Fatal(err)
	}
	<-started
	_ = conn.Close()
	select {
	case <-canceled:
	case <-time.After(time.Second):
		t.Fatal("disconnect did not cancel helper collection")
	}
}

func TestControllerObservationPolicyLatchesOnlyVerifiedBinding(t *testing.T) {
	svc := newControllerObservationService(context.Background(), "/city")
	svc.install(&controllerState{sp: runtime.NewFake()})
	calls := 0
	verified := false
	svc.loadPolicy = func() (procobserver.Policy, error) { calls++; return procobserver.Policy{}, nil }
	svc.checkCaller = func(procobserver.Policy) error {
		if !verified {
			return fmt.Errorf("stale PID")
		}
		return nil
	}
	svc.collect = func(context.Context, procobserver.Policy, runtime.Provider) controllerObservationReply {
		return svc.unknown("fixture partial")
	}
	if got := svc.observe(context.Background()); got.ProcessComplete || svc.policy != nil {
		t.Fatal("stale binding latched")
	}
	// Await actual slot release before the next independent tick.
	for len(controllerObservationFlight) > 0 {
		time.Sleep(time.Millisecond)
	}
	verified = true
	svc.observe(context.Background())
	for len(controllerObservationFlight) > 0 {
		time.Sleep(time.Millisecond)
	}
	svc.observe(context.Background())
	if calls != 2 {
		t.Fatalf("policy read per tick: %d", calls)
	}
}

func TestControllerObservationRelaySubprocess(t *testing.T) {
	city := os.Getenv("TEST_OBSERVER_FIXTURE_CITY")
	if city == "" {
		return
	}
	if !strings.Contains(city, "gc-observer-fixture-") {
		t.Fatal("subprocess target is not an isolated fixture")
	}
	if err := fixtureControllerRelay(context.Background(), city, strings.Repeat("a", 40), os.Stdout); err != nil {
		t.Fatal(err)
	}
}

func TestControllerObservationTwoCLIProcessesOnePersistentCaller(t *testing.T) {
	city, err := os.MkdirTemp("/tmp", "gc-observer-fixture-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(city) //nolint:errcheck // isolated fixture cleanup
	svc := newControllerObservationService(context.Background(), city)
	svc.install(&controllerState{sp: runtime.NewFake()})
	policyReads := 0
	collections := 0
	callerPIDs := []int{}
	svc.loadPolicy = func() (procobserver.Policy, error) { policyReads++; return procobserver.Policy{}, nil }
	svc.checkCaller = func(procobserver.Policy) error { return nil }
	svc.collect = func(context.Context, procobserver.Policy, runtime.Provider) controllerObservationReply {
		collections++
		callerPIDs = append(callerPIDs, os.Getpid())
		r := controllerObservationFixture(city)
		r.ControllerPID = os.Getpid()
		return r
	}
	lis, err := startLegacyObservationTestSocket(city, controllerSocketOptions{observe: svc.observe})
	if err != nil {
		t.Fatal(err)
	}
	defer lis.Close() //nolint:errcheck // isolated fixture cleanup
	cliPIDs := []int{}
	for i := 0; i < 2; i++ {
		cmd := exec.Command(os.Args[0], "-test.run=^TestControllerObservationRelaySubprocess$")
		cmd.Env = append(os.Environ(), "TEST_OBSERVER_FIXTURE_CITY="+city, "TEST_OBSERVER_FIXTURE_PID="+strconv.Itoa(os.Getpid()))
		output, err := cmd.Output()
		if err != nil {
			exit := &exec.ExitError{}
			if errors.As(err, &exit) {
				t.Fatalf("relay subprocess: %v stdout=%s stderr=%s", err, output, exit.Stderr)
			}
			t.Fatalf("relay subprocess: %v %s", err, output)
		}
		cliPIDs = append(cliPIDs, cmd.ProcessState.Pid())
		line := bytes.SplitN(output, []byte("\n"), 2)[0]
		var r controllerObservationReply
		if procobserver.DecodeStrict(line, &r) != nil || r.ControllerPID != os.Getpid() || !r.ProcessComplete {
			t.Fatalf("wrong persistent provenance %s", line)
		}
	}
	if cliPIDs[0] == cliPIDs[1] || cliPIDs[0] == os.Getpid() || cliPIDs[1] == os.Getpid() || collections != 2 || policyReads != 1 || len(callerPIDs) != 2 || callerPIDs[0] != os.Getpid() || callerPIDs[1] != os.Getpid() {
		t.Fatalf("caller lifecycle CLI=%v caller=%v policyloads=%d", cliPIDs, callerPIDs, policyReads)
	}
}

func TestControllerObservationTimeoutRetainsSingleFlight(t *testing.T) {
	svc := newControllerObservationService(context.Background(), "/city")
	svc.install(&controllerState{sp: runtime.NewFake()})
	svc.loadPolicy = func() (procobserver.Policy, error) { return procobserver.Policy{}, nil }
	svc.checkCaller = func(procobserver.Policy) error { return nil }
	started := make(chan struct{})
	release := make(chan struct{})
	svc.collect = func(context.Context, procobserver.Policy, runtime.Provider) controllerObservationReply {
		close(started)
		<-release
		return svc.unknown("fixture released")
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan controllerObservationReply, 1)
	go func() { done <- svc.observe(ctx) }()
	<-started
	cancel()
	if got := <-done; got.ProcessComplete || got.ProviderComplete {
		t.Fatal("canceled result complete")
	}
	if got := svc.observe(context.Background()); len(got.UnknownReasons) != 1 || got.UnknownReasons[0] != "observation busy" {
		t.Fatal("noncooperative read lost limiter")
	}
	close(release)
	deadline := time.Now().Add(time.Second)
	for len(controllerObservationFlight) > 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if len(controllerObservationFlight) > 0 {
		t.Fatal("limiter was not released")
	}
}

// These portable fixtures exercise bounded transport/callback lifecycle. The
// Linux kernel peer proof has separate platform-specific fixtures.
func fixtureControllerRelay(ctx context.Context, city, source string, out io.Writer) error {
	r := controllerObservationFixture(city)
	r.ControllerPID = os.Getpid()
	if parent := os.Getenv("TEST_OBSERVER_FIXTURE_PID"); parent != "" {
		pid, err := strconv.Atoi(parent)
		if err != nil || pid <= 1 {
			return fmt.Errorf("invalid fixture daemon PID")
		}
		r.ControllerPID = pid
	}
	return relayControllerObservationAuthenticated(ctx, city, source, out, func() (procobserver.Policy, error) {
		return procobserver.Policy{CallerBinding: procobserver.CallerBinding{PID: r.ControllerPID, StartTicks: r.ControllerStartIdentity, ControllerBinarySHA256: r.ControllerBinarySHA256, BootID: r.ControllerBootID}, HelperBinarySHA256: r.HelperBinarySHA256, PolicyDigest: r.HelperPolicyDigest}, nil
	}, func(net.Conn, procobserver.CallerBinding) error { return nil }, time.Now)
}

func TestControllerObservationPrefetchedExtraAndOversizedDomain(t *testing.T) {
	for _, mode := range []string{"prefetched extra", "oversized domain"} {
		t.Run(mode, func(t *testing.T) {
			city := t.TempDir()
			calls := 0
			callback := func(context.Context) controllerObservationReply {
				calls++
				r := controllerObservationFixture(city)
				r.Processes = []observation.Process{{RuntimeName: strings.Repeat("x", controllerObservationLimit)}}
				return r
			}
			lis, err := startLegacyObservationTestSocket(city, controllerSocketOptions{observe: callback})
			if err != nil {
				t.Fatal(err)
			}
			defer lis.Close() //nolint:errcheck // isolated fixture cleanup
			conn, err := net.Dial("unix", controllerSocketPath(city))
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close() //nolint:errcheck // isolated fixture cleanup
			_ = conn.SetDeadline(time.Now().Add(time.Second))
			request := controllerObservationCommand + "\n"
			if mode == "prefetched extra" {
				request += "EXTRA\n"
			}
			if _, err = io.WriteString(conn, request); err != nil {
				t.Fatal(err)
			}
			data, err := io.ReadAll(io.LimitReader(conn, controllerObservationLimit+1))
			if err != nil {
				t.Fatal(err)
			}
			var r controllerObservationReply
			if procobserver.DecodeStrict(data, &r) != nil || r.ProviderComplete || r.ProcessComplete || len(r.UnknownReasons) == 0 || len(data) > controllerObservationLimit {
				t.Fatalf("invalid bound/extra response %s", data)
			}
			if mode == "prefetched extra" && calls != 0 {
				t.Fatal("buffered extra frame invoked provider")
			}
		})
	}
}

func TestControllerObservationForgedJSONCannotReplacePeerProof(t *testing.T) {
	city := t.TempDir()
	if err := os.MkdirAll(filepath.Join(city, ".gc"), 0o700); err != nil {
		t.Fatal(err)
	}
	lis, err := net.Listen("unix", controllerSocketPath(city))
	if err != nil {
		t.Fatal(err)
	}
	defer lis.Close() //nolint:errcheck // isolated fixture cleanup
	attempted := make(chan bool, 1)
	go func() {
		conn, e := lis.Accept()
		if e != nil {
			attempted <- false
			return
		}
		defer conn.Close() //nolint:errcheck // isolated fixture cleanup
		_ = conn.SetDeadline(time.Now().Add(time.Second))
		var buf [1]byte
		n, _ := conn.Read(buf[:])
		attempted <- n > 0
	}()
	var out bytes.Buffer
	err = relayControllerObservationAuthenticated(context.Background(), city, strings.Repeat("a", 40), &out, func() (procobserver.Policy, error) { return procobserver.Policy{}, nil }, func(net.Conn, procobserver.CallerBinding) error { return fmt.Errorf("kernel PID mismatch") }, time.Now)
	if err == nil || <-attempted {
		t.Fatal("unverified listener received observation request")
	}
	var r controllerObservationReply
	if procobserver.DecodeStrict(out.Bytes(), &r) != nil || r.ProcessComplete || r.ControllerBinding == "verified_local_process" {
		t.Fatal("forged peer became source")
	}
}

// Couples exact serialized helper-codec/domain output to the actual bounded
// controller relay. The fixture peer is injected; Linux peer proof is separate.
func TestControllerObservationSerializedConsumerRelay(t *testing.T) {
	path := os.Getenv("TEST_OBSERVER_SERIALIZED_HELPER_LOG")
	if path == "" {
		t.Skip("dedicated producer/relay/consumer receipt supplies the exact producer log")
	}
	input, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, line := range strings.Split(string(input), "\n") {
		const marker = "NATIVE_HELPER_CONSUMER_FIXTURE="
		index := strings.Index(line, marker)
		if index < 0 {
			continue
		}
		var fixture struct {
			Name        string                  `json:"name"`
			Observation observation.Observation `json:"observation"`
		}
		if err = json.Unmarshal([]byte(line[index+len(marker):]), &fixture); err != nil {
			t.Fatal(err)
		}
		t.Run(fixture.Name, func(t *testing.T) {
			count++
			r := controllerObservationFixture("/city")
			r.Observation = fixture.Observation
			r.ControllerPID = os.Getpid()
			if !r.ProcessComplete || !r.ProviderComplete {
				r.ControllerBinding = "not_observed"
			}
			raw, err := json.Marshal(r)
			if err != nil {
				t.Fatal(err)
			}
			raw = append(raw, '\n')
			city := t.TempDir()
			if err = os.MkdirAll(filepath.Join(city, ".gc"), 0o700); err != nil {
				t.Fatal(err)
			}
			// The fixture socket location is independent of the source city whose
			// immutable identity is checked in the reply. No actual city is opened.
			lis, err := net.Listen("unix", controllerSocketPath(city))
			if err != nil {
				t.Fatal(err)
			}
			defer lis.Close() //nolint:errcheck
			done := make(chan error, 1)
			go func() {
				conn, e := lis.Accept()
				if e != nil {
					done <- e
					return
				}
				defer conn.Close() //nolint:errcheck
				_, e = bufio.NewReader(conn).ReadString('\n')
				if e == nil {
					_, e = conn.Write(raw)
				}
				done <- e
			}()
			// Dial the temporary socket while retaining the producer city in the
			// canonical relay validation through a temporary path-only seam.
			var out bytes.Buffer
			policy := procobserver.Policy{CallerBinding: procobserver.CallerBinding{PID: r.ControllerPID, StartTicks: r.ControllerStartIdentity, ControllerBinarySHA256: r.ControllerBinarySHA256, BootID: r.ControllerBootID}, HelperBinarySHA256: r.HelperBinarySHA256, PolicyDigest: r.HelperPolicyDigest}
			err = relayControllerObservationAt(context.Background(), "/city", controllerSocketPath(city), r.SourceRevision, &out, func() (procobserver.Policy, error) { return policy, nil }, func(net.Conn, procobserver.CallerBinding) error { return nil }, func() time.Time { return time.Date(2026, 10, 4, 8, 0, 0, 0, time.UTC) })
			if e := <-done; e != nil {
				t.Fatal(e)
			}
			complete := fixture.Name == "complete"
			validInterval := fixture.Name != "stale" && fixture.Name != "wrong-helper-pin"
			if (err == nil) != complete || (validInterval && !bytes.Equal(out.Bytes(), raw)) {
				t.Fatalf("relay changed producer bytes/provenance: err=%v complete=%v", err, complete)
			}
			encoded, e := json.Marshal(struct {
				Name        string          `json:"name"`
				Observation json.RawMessage `json:"observation"`
			}{fixture.Name, json.RawMessage(out.Bytes())})
			if e != nil {
				t.Fatal(e)
			}
			t.Logf("NATIVE_CONTROLLER_CONSUMER_FIXTURE=%s", encoded)
		})
	}
	if count != 9 {
		t.Fatalf("expected9 producer cases, got%d", count)
	}
}

func TestControllerObservationReaderPreservesLegacyEOFLine(t *testing.T) {
	for _, input := range []string{"ping", "ping\n", "ping\r\n"} {
		line, err := readControllerCommandLine(bufio.NewReader(strings.NewReader(input)))
		if err != nil || string(line) != "ping" {
			t.Fatalf("legacy request %q changed: %q %v", input, line, err)
		}
	}
}

func controllerFixtureCensus(count int, digest string) procobserver.Census {
	return procobserver.Census{Seal: procobserver.CensusSeal{ScanIndex: 4, EnumeratedCount: count, PIDDigest: digest, ClassifiedCount: count, ClassifiedDigest: digest}, Closings: []procobserver.CensusClosing{{ScanIndex: 2, EnumeratedCount: count, EnumerationDigest: digest, LiveCount: count, LiveDigest: digest}, {ScanIndex: 3, EnumeratedCount: count, EnumerationDigest: digest, LiveCount: count, LiveDigest: digest}}, ReconciledCount: count, ReconciledDigest: digest, Proofs: []procobserver.CensusProof{}}
}

func controllerFixtureDiagnostics(start, finish time.Time) []controllerProcessDiagnostics {
	d := controllerProcessDiagnostics{StartedAt: start, FinishedAt: finish, Complete: true, EnumeratedCountBefore: 3, EnumeratedCountAfter: 3, EnumerationDigestBefore: strings.Repeat("a", 64), EnumerationDigestAfter: strings.Repeat("a", 64), Errors: []procobserver.EvidenceError{}, KernelRelease: "6.8.0-fixture", KernelProofProfile: procobserver.KernelProofProfile, Census: controllerFixtureCensus(3, strings.Repeat("a", 64))}
	return []controllerProcessDiagnostics{d, d}
}

func TestSerializedControllerRejectsReviewerCensusCounterexamples(t *testing.T) {
	for _, mode := range []string{"baseline", "roots exceed sealed", "coverage scan999", "equal count digest change"} {
		t.Run(mode, func(t *testing.T) {
			r := controllerObservationFixture("/city")
			switch mode {
			case "roots exceed sealed":
				for i := range r.ProcessDiagnostics {
					r.ProcessDiagnostics[i].Census = controllerFixtureCensus(1, strings.Repeat("a", 64))
					r.ProcessDiagnostics[i].Census.Closings[1].EnumeratedCount = 3
				}
				r.Processes = []observation.Process{{PID: 2}, {PID: 3}}
			case "coverage scan999":
				r.ProcessDiagnostics[0].Errors = []procobserver.EvidenceError{{Reason: "coverage_changed", Operation: "enumerate", ScanIndex: 999, ResolvedBy: -1}}
				r.ProcessDiagnostics[0].ErrorsTotal = 1
			case "equal count digest change":
				r.ProcessDiagnostics[0].Census.Closings[0].LiveDigest = strings.Repeat("b", 64)
			}
			wire, err := json.Marshal(r)
			if err != nil {
				t.Fatal(err)
			}
			var decoded controllerObservationReply
			if directory := os.Getenv("GC_TEST_CENSUS_FIXTURE_DIR"); directory != "" {
				if err = os.MkdirAll(directory, 0o700); err != nil {
					t.Fatal(err)
				}
				if err = os.WriteFile(filepath.Join(directory, "controller-"+strings.ReplaceAll(mode, " ", "-")+".json"), wire, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if err = procobserver.DecodeStrict(wire, &decoded); err != nil {
				t.Fatal(err)
			}
			err = validateControllerObservationReply(decoded, "/city", r.SourceRevision, time.Now().UTC())
			if (err == nil) != (mode == "baseline") {
				t.Fatalf("%s accepted=%v error=%v", mode, err == nil, err)
			}
		})
	}
}
