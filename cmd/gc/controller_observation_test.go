package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
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
			Schema: procobserver.Schema, StartedAt: now, FinishedAt: now,
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
		Schema: procobserver.Schema, StartedAt: now, FinishedAt: now, Complete: !partial,
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
	for _, mode := range []string{"transient", "second frame only", "all incomplete"} {
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
				partial := mode == "all incomplete" || reads <= 2
				if mode == "second frame only" && reads == 1 {
					partial = false
				}
				r, err := controllerRetryFrame(p.name, partial)
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
			if mode == "all incomplete" {
				wantReads = 6
			}
			if reads != wantReads || lists != wantReads || policyLoads != 1 || len(r.ProcessDiagnostics) != 2 {
				t.Fatalf("whole attempts not bounded/fresh: reads=%d metadata=%d policy=%d diagnostics=%d", reads, lists, policyLoads, len(r.ProcessDiagnostics))
			}
			if !r.ProviderComplete || r.ProcessComplete != (mode != "all incomplete") || len(r.Processes) != 1 || r.Processes[0].PID != 202 || len(r.Sessions) != 1 || r.Sessions[0].SessionID != "selected" {
				t.Fatalf("selected attempt mixed or promoted: %+v", r)
			}
			if mode != "all incomplete" {
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
	for _, mode := range []string{"pin", "future", "truncated", "permission", "environment", "limit", "zero pid", "unidentified", "provider incarnation", "tracking", "empty diagnostics"} {
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
				case "environment":
					r.Errors[0].Operation = "environ"
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
			releaseRead := sync.OnceFunc(func() { close(release) })
			t.Cleanup(func() {
				cancel()
				releaseRead()
				// Also drain the flight after an earlier assertion/readiness failure.
				select {
				case controllerObservationFlight <- struct{}{}:
					<-controllerObservationFlight
				case <-time.After(hangBudget):
					t.Error("canceled collection retained its flight beyond the hang budget")
				}
			})
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
			awaitClose(t, secondRead, "second helper read")
			cancel()
			select {
			case r := <-done:
				if r.ProviderComplete || r.ProcessComplete {
					t.Fatal("canceled attempt returned complete")
				}
			case <-time.After(hangBudget):
				t.Fatal("canceled observation did not return within the hang budget")
			}
			other, _, _ := controllerRetryFixture(t)
			busy := other.observe(context.Background())
			releaseRead()
			// This barrier can acquire the global slot only after the outstanding
			// read really returns; its deadline detects a hung proof, not speed.
			select {
			case controllerObservationFlight <- struct{}{}:
				<-controllerObservationFlight
			case <-time.After(hangBudget):
				t.Fatal("released collection did not return its flight within the hang budget")
			}
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
	return controllerObservationReply{Observation: observation.Observation{Schema: observation.Schema, CityPath: city, ObservedAt: now, ProcessObservedAt: now, FinishedAt: now, ProviderComplete: true, ProcessComplete: true, ProviderType: "fixture", Sessions: []observation.Session{}, Processes: []observation.Process{}, UnknownReasons: []string{}}, SourceRevision: strings.Repeat("a", 40), ControllerBinding: "verified_local_process", ControllerPID: os.Getpid(), ControllerBinarySHA256: strings.Repeat("b", 64), ControllerStartIdentity: "456", ControllerBootID: "01234567-0123-0123-0123-0123456789ab", HelperBinarySHA256: strings.Repeat("c", 64), HelperPolicyDigest: strings.Repeat("d", 64)}
}

func TestControllerObservationPolicyLatchesOnlyVerifiedBinding(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
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
		synctest.Wait()
		if len(controllerObservationFlight) != 0 {
			t.Fatal("flight not released at quiescence")
		}
		verified = true
		svc.observe(context.Background())
		synctest.Wait()
		if len(controllerObservationFlight) != 0 {
			t.Fatal("flight not released at quiescence")
		}
		svc.observe(context.Background())
		if calls != 2 {
			t.Fatalf("policy read per tick: %d", calls)
		}
	})
}

func TestControllerObservationTimeoutRetainsSingleFlight(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
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
		synctest.Wait()
		if len(controllerObservationFlight) > 0 {
			t.Fatal("limiter was not released")
		}
	})
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

func TestControllerObservationReaderPreservesLegacyEOFLine(t *testing.T) {
	for _, input := range []string{"ping", "ping\n", "ping\r\n"} {
		line, err := readControllerCommandLine(bufio.NewReader(strings.NewReader(input)))
		if err != nil || string(line) != "ping" {
			t.Fatalf("legacy request %q changed: %q %v", input, line, err)
		}
	}
}

func TestControllerObservationTransportAbsent(t *testing.T) {
	var out bytes.Buffer
	if err := relayControllerObservation(context.Background(), t.TempDir(), strings.Repeat("a", 40), &out); err == nil {
		t.Fatal("socket-down allowed")
	}
	var r controllerObservationReply
	if err := procobserver.DecodeStrict(out.Bytes(), &r); err != nil || r.ProcessComplete || r.ProviderComplete {
		t.Fatalf("socket-down not typed UNKNOWN: %v", err)
	}
}

// The existing long-path dispatcher owns one actual listener for these protocol
// and process-lifetime proofs. No extra server is created by this helper.
func assertControllerObservationSharedDispatcher(t *testing.T, city string, setObserver func(func(context.Context) controllerObservationReply)) {
	t.Helper()
	resolvedCity, err := filepath.EvalSymlinks(city)
	if err != nil {
		t.Fatal(err)
	}
	city = resolvedCity
	svc := newControllerObservationService(context.Background(), city)
	svc.install(&controllerState{sp: runtime.NewFake()})
	var mu sync.Mutex
	policyReads, collections := 0, 0
	callerPIDs := []int{}
	svc.loadPolicy = func() (procobserver.Policy, error) {
		mu.Lock()
		defer mu.Unlock()
		policyReads++
		return procobserver.Policy{}, nil
	}
	svc.checkCaller = func(procobserver.Policy) error { return nil }
	svc.collect = func(context.Context, procobserver.Policy, runtime.Provider) controllerObservationReply {
		mu.Lock()
		collections++
		callerPIDs = append(callerPIDs, os.Getpid())
		mu.Unlock()
		return controllerObservationFixture(city)
	}
	setObserver(svc.observe)
	cliPIDs := []int{}
	for i := 0; i < 2; i++ {
		result := runPackCommandProcessWithEnv(t, city, "observer-relay", []string{"TEST_OBSERVER_FIXTURE_PID=" + strconv.Itoa(os.Getpid())}, "observer-fixture")
		if result.exitCode != 0 {
			t.Fatalf("relay subprocess: code=%d stdout=%s stderr=%s", result.exitCode, result.stdout, result.stderr)
		}
		cliPIDs = append(cliPIDs, result.pid)
		line := bytes.SplitN([]byte(result.stdout), []byte("\n"), 2)[0]
		var r controllerObservationReply
		if procobserver.DecodeStrict(line, &r) != nil || r.ControllerPID != os.Getpid() || !r.ProcessComplete {
			t.Fatalf("wrong persistent provenance %s", line)
		}
	}
	mu.Lock()
	if cliPIDs[0] == cliPIDs[1] || cliPIDs[0] == os.Getpid() || cliPIDs[1] == os.Getpid() || collections != 2 || policyReads != 1 || len(callerPIDs) != 2 || callerPIDs[0] != os.Getpid() || callerPIDs[1] != os.Getpid() {
		t.Errorf("caller lifecycle CLI=%v caller=%v policyloads=%d", cliPIDs, callerPIDs, policyReads)
	}
	mu.Unlock()

	for _, mode := range []string{"prefetched extra", "oversized domain"} {
		t.Run(mode, func(t *testing.T) {
			var calls atomic.Int32
			setObserver(func(context.Context) controllerObservationReply {
				calls.Add(1)
				r := controllerObservationFixture(city)
				r.Processes = []observation.Process{{RuntimeName: strings.Repeat("x", controllerObservationLimit)}}
				return r
			})
			conn, err := net.Dial("unix", controllerSocketPath(city))
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close() //nolint:errcheck // owned fixture
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
			want := int32(1)
			if mode == "prefetched extra" {
				want = 0
			}
			if calls.Load() != want {
				t.Fatalf("callback count=%d want%d", calls.Load(), want)
			}
		})
	}
	cancelSvc := newControllerObservationService(context.Background(), city)
	cancelSvc.install(&controllerState{sp: runtime.NewFake()})
	cancelSvc.loadPolicy = func() (procobserver.Policy, error) { return procobserver.Policy{}, nil }
	cancelSvc.checkCaller = func(procobserver.Policy) error { return nil }
	started, canceled := make(chan struct{}), make(chan struct{})
	cancelSvc.collect = func(ctx context.Context, _ procobserver.Policy, _ runtime.Provider) controllerObservationReply {
		close(started)
		<-ctx.Done()
		close(canceled)
		return cancelSvc.unknown("fixture canceled")
	}
	setObserver(cancelSvc.observe)
	conn, err := net.Dial("unix", controllerSocketPath(city))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close() //nolint:errcheck // owned fixture
	if _, err = io.WriteString(conn, controllerObservationCommand+"\n"); err != nil {
		t.Fatal(err)
	}
	awaitClose(t, started, "observer collection to start")
	_ = conn.Close()
	awaitClose(t, canceled, "disconnect to cancel observer collection")
	setObserver(svc.observe)
	awaitCond(t, func() bool {
		var reply bytes.Buffer
		return fixtureControllerRelay(context.Background(), city, strings.Repeat("a", 40), &reply) == nil
	}, "a complete observer reply after canceled flight release")
	setObserver(nil)
}
