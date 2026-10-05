package main

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/gastownhall/gascity/internal/runtime"
	"github.com/gastownhall/gascity/internal/runtime/procobserver"
	"github.com/gastownhall/gascity/internal/testutil"
)

func controllerV3ServiceFixture(t *testing.T) (*controllerObservationServiceV3, *controllerState, procobserver.ReleasePolicyV3, []procobserver.ResponseV3) {
	t.Helper()
	p, frames, now := controllerV3Fixture()
	provider := controllerV3Provider(t)
	cs := &controllerState{cityPath: "/city", sp: provider}
	s := newControllerObservationServiceV3(context.Background(), "/city")
	s.sourceRevision = p.HelperSourceRevision
	s.currentSource = func() string { return p.HelperSourceRevision }
	s.loadPolicy = func() (procobserver.ReleasePolicyV3, error) { return p, nil }
	s.checkCaller = func(procobserver.Policy) error { return nil }
	s.now = func() time.Time { return now }
	reads := 0
	s.readEvidence = func(context.Context, procobserver.ReleasePolicyV3) (procobserver.ResponseV3, error) {
		f := frames[reads%2]
		reads++
		return f, nil
	}
	s.install(cs)
	return s, cs, p, frames
}

func waitControllerV3Service[T any](t *testing.T, ch <-chan T) T {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), testutil.GoroutineRaceTimeout)
	defer cancel()
	select {
	case v := <-ch:
		return v
	case <-ctx.Done():
		t.Fatal("controller V3 service fixture did not reach barrier")
	}
	var zero T
	return zero
}

func controllerV3ServiceIdle(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), testutil.GoroutineRaceTimeout)
	defer cancel()
	select {
	case controllerObservationFlight <- struct{}{}:
		<-controllerObservationFlight
	case <-ctx.Done():
		t.Fatal("controller observation flight still held after fixture release")
	}
}

func TestControllerV3ServiceSuccessAndProvisionalFrames(t *testing.T) {
	for _, mode := range []string{"complete", "partial", "replayed frame"} {
		t.Run(mode, func(t *testing.T) {
			s, _, p, frames := controllerV3ServiceFixture(t)
			if mode == "partial" {
				frames[1].Complete = false
			}
			if mode == "replayed frame" {
				original := s.collect
				s.collect = func(ctx context.Context, p procobserver.ReleasePolicyV3, sp runtime.Provider) controllerObservationReplyV3 {
					r := original(ctx, p, sp)
					r.ProcessDiagnostics[1].RequestNonce = r.ProcessDiagnostics[0].RequestNonce
					return r
				}
			}
			got := s.observe(context.Background())
			if (got.ProviderComplete && got.ProcessComplete) != (mode == "complete") {
				t.Fatalf("mode %s: %+v", mode, got)
			}
			if mode == "complete" || mode == "partial" {
				if len(got.ProcessDiagnostics) != 2 || !reflect.DeepEqual(got.ProcessDiagnostics, frames) {
					t.Fatal("service altered or dropped original helper frames")
				}
				if err := validateControllerObservationReplyV3(got, p, "/city", p.HelperSourceRevision, s.now()); err != nil {
					t.Fatal(err)
				}
			}
			if mode != "complete" && got.CertificateDisposition != "provisional" {
				t.Fatal("partial/replay promoted")
			}
		})
	}
}

func TestControllerV3ServicePolicyLatchesOnlyExactCaller(t *testing.T) {
	s, _, p, _ := controllerV3ServiceFixture(t)
	loads, checks := 0, 0
	s.loadPolicy = func() (procobserver.ReleasePolicyV3, error) { loads++; return p, nil }
	s.checkCaller = func(procobserver.Policy) error {
		checks++
		if checks == 1 {
			return fmt.Errorf("fixture stale caller")
		}
		return nil
	}
	first := s.observe(context.Background())
	if first.ProviderComplete || first.ProcessComplete || s.policy != nil {
		t.Fatal("failed exact caller latched a policy")
	}
	for i := 0; i < 2; i++ {
		got := s.observe(context.Background())
		if !got.ProviderComplete || !got.ProcessComplete {
			t.Fatal("verified caller failed")
		}
	}
	if loads != 2 || checks != 3 || s.policy == nil {
		t.Fatalf("unexpected latch or caller revalidation: loads=%d checks=%d", loads, checks)
	}
}

func TestControllerV3ServiceDeploymentFailuresDoNotRead(t *testing.T) {
	for _, mode := range []string{"not ready", "no provider", "loader unavailable", "v2 selector", "wrong helper source", "wrong controller source", "source unavailable", "source changed", "request canceled", "shutdown canceled"} {
		t.Run(mode, func(t *testing.T) {
			s, cs, p, _ := controllerV3ServiceFixture(t)
			reads := 0
			s.readEvidence = func(context.Context, procobserver.ReleasePolicyV3) (procobserver.ResponseV3, error) {
				reads++
				return procobserver.ResponseV3{}, fmt.Errorf("fixture should not read")
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			switch mode {
			case "not ready":
				s.install(nil)
			case "no provider":
				cs.sp = nil
			case "loader unavailable":
				s.loadPolicy = func() (procobserver.ReleasePolicyV3, error) {
					return procobserver.ReleasePolicyV3{}, fmt.Errorf("fixture unavailable")
				}
			case "v2 selector":
				p.EvidenceSchema = procobserver.Schema
			case "wrong helper source":
				p.HelperSourceRevision = strings.Repeat("0", 40)
			case "wrong controller source":
				p.CallerBinding.ControllerSourceRevision = strings.Repeat("0", 40)
			case "source unavailable":
				s.sourceRevision = "dev"
				s.currentSource = func() string { return "dev" }
			case "source changed":
				s.currentSource = func() string { return strings.Repeat("0", 40) }
			case "request canceled":
				cancel()
			case "shutdown canceled":
				s.ctx = ctx
				cancel()
				ctx = context.Background()
			}
			if mode == "v2 selector" || mode == "wrong helper source" || mode == "wrong controller source" {
				s.loadPolicy = func() (procobserver.ReleasePolicyV3, error) { return p, nil }
			}
			got := s.observe(ctx)
			if reads != 0 || got.ProviderComplete || got.ProcessComplete || got.CertificateDisposition != "provisional" || len(got.UnknownReasons) == 0 {
				t.Fatalf("failure read/promoted: mode=%s reads=%d %+v", mode, reads, got)
			}
		})
	}
}

func TestControllerV3ServiceGenerationAndSourceFences(t *testing.T) {
	for _, mode := range []string{"provider generation", "provider pointer", "state install ABA", "source revision", "source change during policy"} {
		t.Run(mode, func(t *testing.T) {
			s, cs, p, _ := controllerV3ServiceFixture(t)
			var source atomic.Value
			source.Store(p.HelperSourceRevision)
			s.currentSource = func() string { return source.Load().(string) }
			change := func() {
				switch mode {
				case "provider generation":
					cs.mu.Lock()
					cs.observationGeneration++
					cs.mu.Unlock()
				case "provider pointer":
					cs.mu.Lock()
					cs.sp = runtime.NewFake()
					cs.mu.Unlock()
				case "state install ABA":
					s.install(&controllerState{cityPath: "/city", sp: runtime.NewFake()})
					s.install(cs)
				case "source revision", "source change during policy":
					source.Store(strings.Repeat("0", 40))
				}
			}
			if mode == "source change during policy" {
				s.loadPolicy = func() (procobserver.ReleasePolicyV3, error) { change(); return p, nil }
			} else {
				original := s.collect
				s.collect = func(ctx context.Context, p procobserver.ReleasePolicyV3, sp runtime.Provider) controllerObservationReplyV3 {
					r := original(ctx, p, sp)
					change()
					return r
				}
			}
			got := s.observe(context.Background())
			if got.ProviderComplete || got.ProcessComplete || got.CertificateDisposition != "provisional" || got.ControllerBinding == "verified_local_process" || len(got.UnknownReasons) == 0 {
				t.Fatalf("changed source promoted: %+v", got)
			}
			if mode != "source change during policy" && len(got.ProcessDiagnostics) != 2 {
				t.Fatal("fence dropped already collected provisional frames")
			}
			if mode == "source change during policy" && s.policy != nil {
				t.Fatal("source change latched policy")
			}
		})
	}
}

func TestControllerV3ServiceSharesFlightWithV2(t *testing.T) {
	for _, mode := range []string{"V3 holds V2", "V2 holds V3"} {
		t.Run(mode, func(t *testing.T) {
			v3, _, _, _ := controllerV3ServiceFixture(t)
			v2 := newControllerObservationService(context.Background(), "/other-city")
			v2.install(&controllerState{cityPath: "/other-city", sp: runtime.NewFake()})
			v2.loadPolicy = func() (procobserver.Policy, error) { return procobserver.Policy{}, nil }
			v2.checkCaller = func(procobserver.Policy) error { return nil }
			entered, release := make(chan struct{}), make(chan struct{})
			var once sync.Once
			unlock := func() { once.Do(func() { close(release) }) }
			defer unlock()
			finished := make(chan struct{})
			if mode == "V3 holds V2" {
				original := v3.collect
				v3.collect = func(ctx context.Context, p procobserver.ReleasePolicyV3, sp runtime.Provider) controllerObservationReplyV3 {
					close(entered)
					<-release
					return original(ctx, p, sp)
				}
				go func() { _ = v3.observe(context.Background()); close(finished) }()
				waitControllerV3Service(t, entered)
				if r := v2.observe(context.Background()); len(r.UnknownReasons) != 1 || r.UnknownReasons[0] != "observation busy" {
					t.Fatal("V2 bypassed V3 flight")
				}
			} else {
				v2.collect = func(context.Context, procobserver.Policy, runtime.Provider) controllerObservationReply {
					close(entered)
					<-release
					return v2.unknown("fixture partial")
				}
				go func() { _ = v2.observe(context.Background()); close(finished) }()
				waitControllerV3Service(t, entered)
				if r := v3.observe(context.Background()); len(r.UnknownReasons) != 1 || r.UnknownReasons[0] != "observation busy" {
					t.Fatal("V3 bypassed V2 flight")
				}
			}
			unlock()
			waitControllerV3Service(t, finished)
			controllerV3ServiceIdle(t)
		})
	}
}

func TestControllerV3ServiceCanceledReadRetainsFlight(t *testing.T) {
	for _, mode := range []string{"request", "shutdown"} {
		t.Run(mode, func(t *testing.T) {
			s, _, _, _ := controllerV3ServiceFixture(t)
			request, cancel := context.WithCancel(context.Background())
			defer cancel()
			if mode == "shutdown" {
				s.ctx = request
				request = context.Background()
			}
			entered, release, returned := make(chan struct{}), make(chan struct{}), make(chan struct{})
			var once sync.Once
			unlock := func() { once.Do(func() { close(release) }) }
			defer unlock()
			original := s.collect
			s.collect = func(ctx context.Context, p procobserver.ReleasePolicyV3, sp runtime.Provider) controllerObservationReplyV3 {
				close(entered)
				<-release
				r := original(ctx, p, sp)
				close(returned)
				return r
			}
			done := make(chan controllerObservationReplyV3, 1)
			go func() { done <- s.observe(request) }()
			waitControllerV3Service(t, entered)
			cancel()
			r := waitControllerV3Service(t, done)
			if r.ProviderComplete || r.ProcessComplete || r.CertificateDisposition != "provisional" {
				t.Fatal("canceled attempt promoted")
			}
			other, _, _, _ := controllerV3ServiceFixture(t)
			if r := other.observe(context.Background()); len(r.UnknownReasons) != 1 || r.UnknownReasons[0] != "observation busy" {
				t.Fatal("cancellation released the global slot before actual read return")
			}
			unlock()
			waitControllerV3Service(t, returned)
			controllerV3ServiceIdle(t)
		})
	}
}

func TestControllerV3ServiceWholeBudgetAndCallerDeadline(t *testing.T) {
	for _, mode := range []string{"whole25s", "shorter caller"} {
		t.Run(mode, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				s, _, _, _ := controllerV3ServiceFixture(t)
				begin := time.Now()
				want := begin.Add(controllerObservationBudget)
				ctx := context.Background()
				if mode == "shorter caller" {
					want = begin.Add(20 * time.Second)
					var cancel context.CancelFunc
					ctx, cancel = context.WithDeadline(ctx, want)
					defer cancel()
				}
				original := s.collect
				s.collect = func(ctx context.Context, p procobserver.ReleasePolicyV3, sp runtime.Provider) controllerObservationReplyV3 {
					got, ok := ctx.Deadline()
					if !ok || !got.Equal(want) {
						t.Errorf("whole collector budget changed: got=%v want=%v", got, want)
					}
					return original(ctx, p, sp)
				}
				r := s.observe(ctx)
				if !r.ProviderComplete || !r.ProcessComplete {
					t.Fatalf("bounded attempt incomplete: %+v", r)
				}
			})
		})
	}
}

func TestControllerV3ServiceUnknownReadsSourceUnderMutex(t *testing.T) {
	s, _, p, _ := controllerV3ServiceFixture(t)
	initial := p.HelperSourceRevision
	alternate := strings.Repeat("0", 40)
	start, writerDone, readerDone := make(chan struct{}), make(chan struct{}), make(chan struct{})
	go func() {
		<-start
		for i := 0; i < 500; i++ {
			s.mu.Lock()
			if i%2 == 0 {
				s.sourceRevision = alternate
			} else {
				s.sourceRevision = initial
			}
			s.mu.Unlock()
		}
		close(writerDone)
	}()
	go func() {
		<-start
		for i := 0; i < 500; i++ {
			r := s.unknown("fixture source transition")
			if r.SourceRevision != initial && r.SourceRevision != alternate {
				t.Errorf("UNKNOWN source was not a coherent snapshot: %q", r.SourceRevision)
			}
		}
		close(readerDone)
	}()
	close(start)
	waitControllerV3Service(t, writerDone)
	waitControllerV3Service(t, readerDone)
}
