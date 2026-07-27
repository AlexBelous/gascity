package rollout

import (
	"testing"

	"github.com/gastownhall/gascity/internal/config"
)

// TestHookClaimFastPathDefaultOff proves the gate is OFF by default across all
// three homes (Spec.Default, defaultFlags, config accessor): the controller
// fast path is opt-in, and an unwired/degraded Flags runs the legacy shell
// claim path.
func TestHookClaimFastPathDefaultOff(t *testing.T) {
	t.Parallel()

	byKey := map[string]Spec{}
	for _, s := range Specs() {
		byKey[s.Key] = s
	}
	spec, ok := byKey[keyBeadsHookClaimFastPath]
	if !ok {
		t.Fatalf("registry missing %q", keyBeadsHookClaimFastPath)
	}
	if spec.Default.Bool == nil || *spec.Default.Bool {
		t.Fatalf("Spec.Default.Bool = %v, want false (opt-in rollout)", spec.Default.Bool)
	}
	if defaultFlags().HookClaimFastPath() {
		t.Error("defaultFlags HookClaimFastPath = true, want false")
	}
	if (config.BeadsConfig{}).HookClaimFastPathEnabled() {
		t.Error("config accessor default = true, want false")
	}
}

// TestHookClaimFastPathResolvesFromConfig proves an explicit config opt-in
// resolves to true with config origin.
func TestHookClaimFastPathResolvesFromConfig(t *testing.T) {
	t.Parallel()

	enabled := true
	cfg := &config.City{}
	cfg.Beads.HookClaimFastPath = &enabled

	f, err := Resolve(cfg, ResolveOptions{LookupEnv: func(string) (string, bool) { return "", false }})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if !f.HookClaimFastPath() {
		t.Error("HookClaimFastPath() = false after config opt-in, want true")
	}
	if f.OriginOf(keyBeadsHookClaimFastPath) != OriginConfig {
		t.Errorf("origin = %q, want config", f.OriginOf(keyBeadsHookClaimFastPath))
	}
	if got := f.ValueOf(keyBeadsHookClaimFastPath); got != "true" {
		t.Errorf("ValueOf = %q, want true", got)
	}
}

// TestHookClaimFastPathForTestOverride proves the ForTest constructor toggles
// the gate for consumer tests without touching config or env.
func TestHookClaimFastPathForTestOverride(t *testing.T) {
	t.Parallel()
	if !ForTest(WithHookClaimFastPath(true)).HookClaimFastPath() {
		t.Error("WithHookClaimFastPath(true) did not enable the gate")
	}
	if ForTest().HookClaimFastPath() {
		t.Error("ForTest() default enabled the gate, want off")
	}
}
