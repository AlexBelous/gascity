//go:build integration

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
	"time"

	"github.com/gastownhall/gascity/internal/runtime"
	"github.com/gastownhall/gascity/internal/runtime/observation"
	"github.com/gastownhall/gascity/internal/runtime/procobserver"
	"github.com/gastownhall/gascity/internal/testutil"
)

func TestControllerObservationSocketRelay(t *testing.T) {
	for _, mode := range []string{"exact", "partial", "wrong source", "wrong city", "missing scalar", "null scalar", "duplicate scalar", "extra frame", "oversized", "unsupported"} {
		t.Run(mode, func(t *testing.T) {
			city := t.TempDir()
			if err := os.MkdirAll(filepath.Dir(controllerSocketPath(city)), 0o700); err != nil {
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

func TestControllerObservationForgedJSONCannotReplacePeerProof(t *testing.T) {
	city := t.TempDir()
	if err := os.MkdirAll(filepath.Dir(controllerSocketPath(city)), 0o700); err != nil {
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
			// Diagnostic fixture intervals follow the immutable producer timestamps.
			r.ProcessDiagnostics = controllerFixtureDiagnostics(r.ObservedAt, r.FinishedAt)
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
			if err = os.MkdirAll(filepath.Dir(controllerSocketPath(city)), 0o700); err != nil {
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

func TestControllerObservationLegacySharedDispatcher(t *testing.T) {
	city := shortSocketTempDir(t, "gc-observer-legacy-")
	var mu sync.Mutex
	var callback func(context.Context) controllerObservationReply
	observe := func(ctx context.Context) controllerObservationReply {
		mu.Lock()
		current := callback
		mu.Unlock()
		if current == nil {
			return controllerObservationFixture(city)
		}
		return current(ctx)
	}
	setObserver := func(current func(context.Context) controllerObservationReply) {
		mu.Lock()
		callback = current
		mu.Unlock()
	}
	lis, err := startLegacyObservationTestSocket(city, controllerSocketOptions{observe: observe})
	if err != nil {
		t.Fatal(err)
	}
	defer lis.Close() //nolint:errcheck // owned legacy fixture
	assertControllerObservationSharedDispatcher(t, city, setObserver)
}

// A single legacy fixture listener owns all protocol/process-lifetime proofs.
func startLegacyObservationTestSocket(city string, options ...controllerSocketOptions) (net.Listener, error) {
	if err := os.MkdirAll(filepath.Dir(controllerSocketPath(city)), 0o700); err != nil {
		return nil, err
	}
	lis, err := net.Listen("unix", controllerSocketPath(city))
	if err != nil {
		return nil, err
	}
	go func() {
		for {
			c, err := lis.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close() //nolint:errcheck // owned fixture
				_ = c.SetDeadline(time.Now().Add(testutil.GoroutineRaceTimeout))
				reader := bufio.NewReader(c)
				line, err := readControllerCommandLine(reader)
				if err == nil && string(line) == controllerObservationCommand {
					handleControllerObservation(c, reader, city, options)
				}
			}()
		}
	}()
	return lis, nil
}

func TestControllerV3RelayClosingCancellation(t *testing.T) {
	for _, mode := range []string{"before policy", "after decode", "after closing peer"} {
		t.Run(mode, func(t *testing.T) {
			p, frames, now := controllerV3Fixture()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var out bytes.Buffer
			loads := 0
			load := func() (procobserver.ReleasePolicyV3, error) { loads++; return p, nil }
			if mode == "before policy" {
				cancel()
				err := relayControllerObservationV3At(ctx, "/city", "/fixture-not-open", p.HelperSourceRevision, &out, load,
					func(net.Conn, procobserver.CallerBinding) error { t.Fatal("canceled relay verified peer"); return nil },
					writeControllerObservationRequestV3, func() time.Time { return now })
				if err == nil || loads != 0 {
					t.Fatalf("already canceled relay loaded policy or succeeded: loads=%d err=%v", loads, err)
				}
				return
			}
			calls := 0
			reply := collectControllerObservationV3(context.Background(), "/city", p, controllerV3Provider(t),
				func(context.Context, procobserver.ReleasePolicyV3) (procobserver.ResponseV3, error) {
					f := frames[calls]
					calls++
					return f, nil
				}, func() time.Time { return now })
			data, err := json.Marshal(reply)
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(shortSocketTempDir(t, "gc-v3-"), "s")
			lis, err := net.Listen("unix", path)
			if err != nil {
				t.Fatal(err)
			}
			defer lis.Close() //nolint:errcheck // owned fixture
			done := make(chan error, 1)
			go func() {
				c, e := lis.Accept()
				if e != nil {
					done <- e
					return
				}
				defer c.Close() //nolint:errcheck // owned fixture
				_ = c.SetDeadline(time.Now().Add(testutil.GoroutineRaceTimeout))
				if _, e = bufio.NewReader(c).ReadString('\n'); e == nil {
					_, e = c.Write(data)
				}
				done <- e
			}()
			checks := 0
			verify := func(net.Conn, procobserver.CallerBinding) error {
				checks++
				if mode == "after closing peer" && checks == 2 {
					cancel()
				}
				return nil
			}
			relayNow := func() time.Time {
				if mode == "after decode" {
					cancel()
				}
				return now
			}
			err = relayControllerObservationV3At(ctx, "/city", path, p.HelperSourceRevision, &out, load, verify, writeControllerObservationRequestV3, relayNow)
			if err == nil {
				t.Fatal("cancellation published a late COMPLETE")
			}
			var got controllerObservationReplyV3
			if procobserver.DecodeStrict(out.Bytes(), &got) != nil || (got.ProviderComplete && got.ProcessComplete) || got.CertificateDisposition != "provisional" {
				t.Fatal("cancellation was not typed UNKNOWN")
			}
			if e := waitControllerV3Service(t, done); e != nil {
				t.Fatal(e)
			}
		})
	}
}

func TestControllerV3RelayPreservesBytesAndPeerFence(t *testing.T) {
	for _, mode := range []string{"exact", "bad JSON", "extra JSON", "peer before", "peer after", "request failure", "partial"} {
		t.Run(mode, func(t *testing.T) {
			p, frames, now := controllerV3Fixture()
			calls := 0
			r := collectControllerObservationV3(context.Background(), "/city", p, controllerV3Provider(t), func(context.Context, procobserver.ReleasePolicyV3) (procobserver.ResponseV3, error) {
				f := frames[calls]
				calls++
				return f, nil
			}, func() time.Time { return now })
			if mode == "partial" {
				r.ProviderComplete = false
				r.CertificateDisposition = "provisional"
				r.UnknownReasons = []string{"fixture provider unavailable"}
				r.ControllerBinding = "not_observed"
			}
			data, _ := json.MarshalIndent(r, "", " ")
			data = append(data, '\n')
			if mode == "bad JSON" {
				data = []byte("{}\n")
			}
			if mode == "extra JSON" {
				data = append(data, []byte("{}\n")...)
			}
			path := filepath.Join(shortSocketTempDir(t, "gc-v3-"), "s")
			lis, e := net.Listen("unix", path)
			if e != nil {
				t.Fatal(e)
			}
			defer lis.Close() //nolint:errcheck // isolated relay fixture
			done := make(chan error, 1)
			go func() {
				c, e := lis.Accept()
				if e != nil {
					done <- e
					return
				}
				defer c.Close() //nolint:errcheck // isolated relay fixture
				_ = c.SetDeadline(time.Now().Add(testutil.GoroutineRaceTimeout))
				_, e = bufio.NewReader(c).ReadString('\n')
				if e != nil {
					done <- nil
					return
				}
				_, e = c.Write(data)
				done <- e
			}()
			checks := 0
			verify := func(net.Conn, procobserver.CallerBinding) error {
				checks++
				if mode == "peer before" || mode == "peer after" && checks == 2 {
					return fmt.Errorf("fixture mismatch")
				}
				return nil
			}
			request := func(c net.Conn) error {
				if mode == "request failure" {
					return fmt.Errorf("fixture request failed")
				}
				_, err := io.WriteString(c, "test-only request\n")
				return err
			}
			ctx, cancel := context.WithTimeout(context.Background(), testutil.GoroutineRaceTimeout)
			defer cancel()
			var out bytes.Buffer
			err := relayControllerObservationV3At(ctx, "/city", path, p.HelperSourceRevision, &out, func() (procobserver.ReleasePolicyV3, error) { return p, nil }, verify, request, func() time.Time { return now })
			if (err == nil) != (mode == "exact") {
				t.Fatalf("mode %s: %v", mode, err)
			}
			if mode == "exact" && (!bytes.Equal(out.Bytes(), data) || checks != 2) {
				t.Fatal("relay refreshed, trimmed or failed to fence evidence")
			}
			if mode == "partial" && (!bytes.Equal(out.Bytes(), data) || checks != 2) {
				t.Fatal("partial raw frames altered")
			}
			if mode != "exact" && mode != "partial" {
				var unknown controllerObservationReplyV3
				if procobserver.DecodeStrict(out.Bytes(), &unknown) != nil || unknown.ProcessComplete || unknown.ProviderComplete || unknown.CertificateDisposition != "provisional" {
					t.Fatal("invalid relay reply promoted")
				}
			}
			select {
			case e := <-done:
				if e != nil {
					t.Fatal(e)
				}
			case <-ctx.Done():
				t.Fatal("fixture relay server did not return")
			}
		})
	}
}

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
