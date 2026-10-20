package routing

import (
	"context"
	"errors"
	"testing"

	"github.com/gastownhall/gascity/internal/runtime"
)

func TestPinnedRuntimeSurvivesPolicyChange(t *testing.T) {
	local, remote := runtime.NewFake(), runtime.NewFake()
	old := runtime.RuntimeBinding{Selector: "ssh:worker@vm", Endpoint: "worker@vm", ResourceDomain: "vm", PolicyDigest: "v1"}
	current := old
	pin := old
	p := New(local, Callbacks{
		Bind: func(context.Context, string, string) (runtime.RuntimeBinding, error) {
			if current != pin {
				return pin, errors.New("policy changed")
			}
			return pin, nil
		},
		Lookup: func(string) (runtime.RuntimeBinding, error) { return pin, nil },
		Factory: func(b runtime.RuntimeBinding) (runtime.Provider, error) {
			if b != old {
				t.Fatal("wrong pin")
			}
			return remote, nil
		},
		Bindings: func() ([]runtime.RuntimeBinding, error) { return []runtime.RuntimeBinding{pin}, nil },
	})
	cfg := runtime.Config{Command: "model --one-shot", Env: map[string]string{"GC_SESSION_ID": "sid-a", "GC_TEMPLATE": "worker"}}
	if err := p.Start(context.Background(), "runtime-a", cfg); err != nil {
		t.Fatal(err)
	}
	current.Endpoint = "worker@other"
	if err := p.Start(context.Background(), "runtime-a", cfg); err == nil {
		t.Fatal("policy change allowed launch")
	}
	if _, err := p.Peek("runtime-a", 10); err != nil {
		t.Fatal(err)
	}
	if err := p.Stop("runtime-a"); err != nil {
		t.Fatal(err)
	}
	if local.CountCalls("Start", "runtime-a") != 0 || remote.CountCalls("Stop", "runtime-a") != 1 {
		t.Fatal("wrong backend")
	}
	if p.SupportsCommandScope("runtime-a") {
		t.Fatal("remote advertised local command scope")
	}
}

func TestLookupFailureDoesNotFallBackLocal(t *testing.T) {
	local := runtime.NewFake()
	p := New(local, Callbacks{Lookup: func(string) (runtime.RuntimeBinding, error) {
		return runtime.RuntimeBinding{}, errors.New("store unavailable")
	}})
	if err := p.Stop("runtime-a"); !errors.Is(err, runtime.ErrRuntimeUnavailable) {
		t.Fatalf("got %v", err)
	}
	if local.CountCalls("Stop", "runtime-a") != 0 {
		t.Fatal("local fallback")
	}
	if _, err := runtime.ObserveLivenessWithError(p, "runtime-a", nil); !errors.Is(err, runtime.ErrRuntimeUnavailable) {
		t.Fatalf("lost uncertainty: %v", err)
	}
}

func TestAllRequiredVerbsUsePinnedBackend(t *testing.T) {
	local, remote := runtime.NewFake(), runtime.NewFake()
	b := runtime.RuntimeBinding{Selector: "ssh:worker@vm", Endpoint: "worker@vm", ResourceDomain: "vm", PolicyDigest: "v1"}
	p := New(local, Callbacks{Lookup: func(string) (runtime.RuntimeBinding, error) { return b, nil }, Factory: func(runtime.RuntimeBinding) (runtime.Provider, error) { return remote, nil }})
	calls := []struct {
		name string
		run  func()
	}{
		{"Interrupt", func() { _ = p.Interrupt("n") }},
		{"IsRunning", func() { p.IsRunning("n") }},
		{"IsAttached", func() { p.IsAttached("n") }},
		{"Attach", func() { _ = p.Attach("n") }},
		{"ProcessAlive", func() { p.ProcessAlive("n", nil) }},
		{"Nudge", func() { _ = p.Nudge("n", nil) }},
		{"SetMeta", func() { _ = p.SetMeta("n", "k", "v") }},
		{"GetMeta", func() { _, _ = p.GetMeta("n", "k") }},
		{"RemoveMeta", func() { _ = p.RemoveMeta("n", "k") }},
		{"Peek", func() { _, _ = p.Peek("n", 1) }},
		{"GetLastActivity", func() { _, _ = p.GetLastActivity("n") }},
		{"ClearScrollback", func() { _ = p.ClearScrollback("n") }},
		{"CopyTo", func() { _ = p.CopyTo("n", "x", "y") }},
		{"SendKeys", func() { _ = p.SendKeys("n", "Enter") }},
		{"RunLive", func() { _ = p.RunLive("n", runtime.Config{}) }},
		{"Stop", func() { _ = p.Stop("n") }},
	}
	for _, c := range calls {
		t.Run(c.name, func(t *testing.T) {
			c.run()
			if remote.CountCalls(c.name, "n") != 1 || local.CountCalls(c.name, "n") != 0 {
				t.Fatalf("%s routed incorrectly", c.name)
			}
		})
	}
}

type localScopeProvider struct{ *runtime.Fake }

func (p *localScopeProvider) SupportsCommandScope(string) bool { return true }
func TestLocalLaunchConfigAndCapabilitiesUnchanged(t *testing.T) {
	local := &localScopeProvider{runtime.NewFake()}
	p := New(local, Callbacks{Bind: func(context.Context, string, string) (runtime.RuntimeBinding, error) {
		return runtime.RuntimeBinding{}, nil
	}, Lookup: func(string) (runtime.RuntimeBinding, error) { return runtime.RuntimeBinding{}, nil }})
	cfg := runtime.Config{Command: "native-provider --model same --one-shot 'prompt one argument'", Env: map[string]string{"GC_SESSION_ID": "sid-local", "GC_TEMPLATE": "worker"}}
	before := runtime.ConfigFingerprint(cfg)
	if err := p.Start(context.Background(), "local", cfg); err != nil {
		t.Fatal(err)
	}
	got := local.LastStartConfig("local")
	if got == nil || got.Command != cfg.Command || runtime.ConfigFingerprint(*got) != before {
		t.Fatal("local command/fingerprint changed")
	}
	if p.Capabilities() != local.Capabilities() || !p.SupportsCommandScope("local") {
		t.Fatal("local capabilities changed")
	}
}

func TestRemoteSIDCannotUseLocalProcessScanner(t *testing.T) {
	local := runtime.NewFake()
	local.ExtraRuntimes = []runtime.LiveRuntime{{SessionID: "remote-sid", PID: 123}, {SessionID: "local-sid", PID: 456}}
	b := runtime.RuntimeBinding{Selector: "ssh:worker@vm"}
	p := New(local, Callbacks{LookupID: func(id string) (runtime.RuntimeBinding, bool, error) {
		if id == "remote-sid" {
			return b, true, nil
		}
		return runtime.RuntimeBinding{}, false, nil
	}})
	rows, err := p.FindRuntimesBySessionID("")
	if err != nil || len(rows) != 1 || rows[0].SessionID != "local-sid" {
		t.Fatalf("wrong local census: %+v %v", rows, err)
	}
	if err = p.TerminateRuntime(runtime.LiveRuntime{SessionID: "remote-sid", PID: 123}); err == nil {
		t.Fatal("remote SID authorized local kill")
	}
}
