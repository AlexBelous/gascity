package observation

import (
	"encoding/json"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/runtime/auto"

	"github.com/gastownhall/gascity/internal/runtime"
	"github.com/gastownhall/gascity/internal/runtime/proctable"
)

type observedProvider struct {
	*runtime.Fake
	names            []string
	roots            []runtime.LiveRuntime
	listErr, scanErr error
	capable          bool
}

func (p *observedProvider) ListRunning(string) ([]string, error) { return p.names, p.listErr }
func (p *observedProvider) FindRuntimesBySessionID(string) ([]runtime.LiveRuntime, error) {
	return p.roots, p.scanErr
}
func (p *observedProvider) CanScanProcessTable() bool { return p.capable }

func fixture(t *testing.T) (*observedProvider, []proctable.ObservedRoot) {
	t.Helper()
	p := &observedProvider{Fake: runtime.NewFake(), names: []string{"live-a", "live-b", "protected"}, capable: true}
	var roots []proctable.ObservedRoot
	for i, name := range p.names {
		sid := []string{"sid-a", "sid-b", "sid-p"}[i]
		template := "worker"
		if i == 2 {
			template = "observer"
		}
		for key, value := range map[string]string{"GC_SESSION_ID": sid, "GC_TEMPLATE": template, "GC_RUNTIME_EPOCH": "2", "GC_INSTANCE_TOKEN": "test-token-" + sid} {
			if err := p.SetMeta(name, key, value); err != nil {
				t.Fatal(err)
			}
		}
		live := runtime.LiveRuntime{SessionID: sid, City: "/city", Epoch: 2, PID: 100 + i, PPID: 1, ProviderName: name, IsTracked: true}
		p.roots = append(p.roots, live)
		roots = append(roots, proctable.ObservedRoot{Runtime: live, Template: template, StartIdentity: "start-" + sid, InstanceTokenSHA256: TokenDigest("test-token-" + sid)})
	}
	return p, roots
}

func TestObserveExactSIDCoverage(t *testing.T) {
	p, roots := fixture(t)
	now := time.Date(2026, 10, 4, 8, 0, 0, 0, time.UTC)
	got := Observe("/city", p, func() ([]proctable.ObservedRoot, error) { return roots, nil }, func() time.Time { return now })
	if !got.ProviderComplete || !got.ProcessComplete || len(got.Sessions) != 3 || len(got.Processes) != 3 || len(got.UnknownReasons) != 0 {
		t.Fatalf("incomplete: %+v", got)
	}
	if got.Sessions[0].SessionID == got.Sessions[1].SessionID {
		t.Fatal("collapsed two SIDs of one template")
	}
	for _, call := range p.SnapshotCalls() {
		if call.Method != "SetMeta" && call.Method != "GetMeta" {
			t.Fatalf("unexpected effect %s", call.Method)
		}
	}
}

func TestObserveUnknownDoesNotBecomeComplete(t *testing.T) {
	for _, name := range []string{"scannerless composite", "partial scanner", "partial strict scan", "missing process", "unknown city", "alias conflict", "missing epoch", "wrong run", "untracked process", "provider failure", "missing template", "PID reuse"} {
		t.Run(name, func(t *testing.T) {
			p, roots := fixture(t)
			var strictErr error
			switch name {
			case "scannerless composite":
				p.capable = false
			case "partial scanner":
				p.scanErr = errors.New("permission denied")
			case "partial strict scan":
				strictErr = errors.New("unreadable environment")
			case "missing process":
				roots = roots[1:]
				p.roots = p.roots[1:]
			case "unknown city":
				roots[0].Runtime.City = ""
			case "alias conflict":
				_ = p.SetMeta("live-b", "GC_SESSION_ID", "sid-a")
			case "missing epoch":
				_ = p.SetMeta("live-a", "GC_RUNTIME_EPOCH", "")
			case "wrong run":
				roots[0].InstanceTokenSHA256 = TokenDigest("old-run")
			case "untracked process":
				p.roots[0].IsTracked = false
			case "provider failure":
				p.listErr = errors.New("provider down")
			case "missing template":
				_ = p.SetMeta("live-a", "GC_TEMPLATE", "")
			case "PID reuse":
				p.roots[0].Epoch = 3
			}
			now := time.Date(2026, 10, 4, 8, 0, 0, 0, time.UTC)
			got := Observe("/city", p, func() ([]proctable.ObservedRoot, error) { return roots, strictErr }, func() time.Time { return now })
			if got.ProviderComplete && got.ProcessComplete {
				t.Fatalf("unknown accepted: %+v", got)
			}
			if len(got.UnknownReasons) == 0 {
				t.Fatal("missing explicit reason")
			}
		})
	}
}

func TestObserveChangedProcessIncarnation(t *testing.T) {
	p, roots := fixture(t)
	calls := 0
	now := time.Date(2026, 10, 4, 8, 0, 0, 0, time.UTC)
	got := Observe("/city", p, func() ([]proctable.ObservedRoot, error) {
		copied := append([]proctable.ObservedRoot{}, roots...)
		calls++
		if calls > 1 {
			copied[0].StartIdentity = "recycled-PID"
		}
		return copied, nil
	}, func() time.Time { return now })
	if got.ProcessComplete {
		t.Fatal("changed process identity accepted")
	}
}

type scannerlessProvider struct{ runtime.Provider }

func TestObserveActualScannerlessComposite(t *testing.T) {
	p := auto.New(&scannerlessProvider{Provider: runtime.NewFake()}, runtime.NewFake())
	now := time.Date(2026, 10, 4, 8, 0, 0, 0, time.UTC)
	got := Observe("/city", p, func() ([]proctable.ObservedRoot, error) { return []proctable.ObservedRoot{}, nil }, func() time.Time { return now })
	if got.ProcessComplete || len(got.UnknownReasons) == 0 {
		t.Fatalf("scannerless composite fabricated empty complete scan: %+v", got)
	}
}

// These serialized producer results are consumed unchanged by the pinned
// external consumer check. Policy names live only in that consumer's data.
func TestSerializedConsumerObservations(t *testing.T) {
	data, err := os.ReadFile("testdata/consumer-input.json")
	if err != nil {
		t.Fatal(err)
	}
	var input struct {
		Templates []string `json:"templates"`
	}
	if err := json.Unmarshal(data, &input); err != nil {
		t.Fatal(err)
	}
	for _, testCase := range []string{"complete", "strict-unknown", "scanner-partial", "missing-process", "alias-conflict", "wrong-run", "scannerless"} {
		t.Run(testCase, func(t *testing.T) {
			p, roots := fixture(t)
			for i, name := range p.names {
				_ = p.SetMeta(name, "GC_TEMPLATE", input.Templates[i])
				roots[i].Template = input.Templates[i]
			}
			var strictErr error
			switch testCase {
			case "strict-unknown":
				strictErr = errors.New("process environment permission denied")
			case "scanner-partial":
				p.scanErr = errors.New("partial scanner")
			case "missing-process":
				roots = roots[1:]
				p.roots = p.roots[1:]
			case "alias-conflict":
				_ = p.SetMeta("live-b", "GC_SESSION_ID", "sid-a")
			case "wrong-run":
				roots[0].InstanceTokenSHA256 = TokenDigest("old-incarnation")
			case "scannerless":
				p.capable = false
			}
			now := time.Date(2026, 10, 4, 8, 0, 0, 0, time.UTC)
			got := Observe("/city", p, func() ([]proctable.ObservedRoot, error) { return roots, strictErr }, func() time.Time { return now })
			if (got.ProviderComplete && got.ProcessComplete) != (testCase == "complete") {
				t.Fatalf("unexpected completeness: %+v", got)
			}
			encoded, err := json.Marshal(struct {
				Name        string      `json:"name"`
				Observation Observation `json:"observation"`
			}{testCase, got})
			if err != nil {
				t.Fatal(err)
			}
			t.Logf("NATIVE_CONSUMER_FIXTURE=%s", encoded)
		})
	}
}
