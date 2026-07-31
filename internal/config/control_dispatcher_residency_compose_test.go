package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/BurntSushi/toml"
	"github.com/gastownhall/gascity/internal/fsys"
)

// These are production-shape regressions: they compose the SHIPPED core pack via
// LoadWithIncludes (the real controller/gc-start load path), so they exercise the
// actual agent.toml (resident=true) plus ClearDisabledControlLaneResidency +
// ApplyResidentAgentFloor wired into compose. A direct helper test with a
// hand-built agent cannot catch a regression where the shipped agent.toml
// reintroduces an unconditional min_active_sessions=1 — which then survives a
// max=0 disable override and fails ValidateAgents — or drops the resident opt-in.

// composeCoreCity writes a city.toml that includes the shipped core pack plus the
// given extra TOML, then composes it with LoadWithIncludes.
func composeCoreCity(t *testing.T, extraToml string) *City {
	t.Helper()
	corePack, err := filepath.Abs(filepath.Join("..", "bootstrap", "packs", "core"))
	if err != nil {
		t.Fatalf("abs core pack: %v", err)
	}
	cityDir := t.TempDir()
	body := "[workspace]\nname = \"test\"\nincludes = [\"" + corePack + "\"]\n" + extraToml
	if err := os.WriteFile(filepath.Join(cityDir, "city.toml"), []byte(body), 0o644); err != nil {
		t.Fatalf("write city.toml: %v", err)
	}
	cfg, _, err := LoadWithIncludes(fsys.OSFS{}, filepath.Join(cityDir, "city.toml"))
	if err != nil {
		t.Fatalf("LoadWithIncludes: %v", err)
	}
	return cfg
}

func composedDispatcher(t *testing.T, cfg *City) *Agent {
	t.Helper()
	for i := range cfg.Agents {
		if cfg.Agents[i].Name == ControlDispatcherAgentName {
			return &cfg.Agents[i]
		}
	}
	t.Fatal("composed city has no control-dispatcher agent")
	return nil
}

// TestControlDispatcherResidency_Composition_ActiveGetsFloor: composing the
// shipped core pack (formula_v2 default-on) yields a dispatcher floored to
// min_active_sessions=1 via the compose-wired ApplyResidentAgentFloor.
func TestControlDispatcherResidency_Composition_ActiveGetsFloor(t *testing.T) {
	cfg := composeCoreCity(t, "")
	d := composedDispatcher(t, cfg)
	if d.MinActiveSessions == nil || *d.MinActiveSessions != 1 {
		t.Fatalf("composed dispatcher MinActiveSessions=%v, want 1 (residency floor via compose, gc-0ychy)", d.MinActiveSessions)
	}
	if err := ValidateAgents(cfg.Agents); err != nil {
		t.Fatalf("ValidateAgents on composed city: %v", err)
	}
}

// TestControlDispatcherResidency_Composition_DisableOverrideValidates is the P1
// production-shape regression: a max_active_sessions=0 disable override on the
// composed dispatcher must NOT be floored (the disable wins) and must leave the
// city valid — never synthesizing min>max, which would fail a controller reload.
func TestControlDispatcherResidency_Composition_DisableOverrideValidates(t *testing.T) {
	cfg := composeCoreCity(t, "\n[[patches.agent]]\nname = \"control-dispatcher\"\nmax_active_sessions = 0\n")
	d := composedDispatcher(t, cfg)
	if d.MinActiveSessions != nil {
		t.Fatalf("disabled (max=0) dispatcher MinActiveSessions=%v, want nil — the disable override must win (gc-0ychy P1)", *d.MinActiveSessions)
	}
	if err := ValidateAgents(cfg.Agents); err != nil {
		t.Fatalf("ValidateAgents after max=0 disable override: %v — the residency floor must never produce min>max (gc-0ychy P1)", err)
	}
}

// TestControlDispatcherResidency_Composition_FormulaV2DisabledNoFloor is the P2
// production-shape regression: with daemon.formula_v2=false the composed
// dispatcher is not floored, so the v2 control lane is not kept resident.
func TestControlDispatcherResidency_Composition_FormulaV2DisabledNoFloor(t *testing.T) {
	cfg := composeCoreCity(t, "\n[daemon]\nformula_v2 = false\n")
	d := composedDispatcher(t, cfg)
	if d.MinActiveSessions != nil {
		t.Fatalf("formula_v2=false dispatcher MinActiveSessions=%v, want nil — no resident dispatcher without a v2 lane (gc-0ychy P2)", *d.MinActiveSessions)
	}
}

// TestControlDispatcherAgentTomlHasNoUnconditionalMin is the shipped-config
// guard: the core dispatcher agent.toml must NOT declare min_active_sessions.
// Residency is supplied conditionally by ApplyResidentAgentFloor; a static min in
// the toml is unconditional and would re-break the max=0 disable override
// (gc-0ychy P1). The toml must instead declare resident=true — the role-neutral
// opt-in the floor keys on.
func TestControlDispatcherAgentTomlHasNoUnconditionalMin(t *testing.T) {
	path := filepath.Join("..", "bootstrap", "packs", "core", "agents", "control-dispatcher", "agent.toml")
	var parsed struct {
		MinActiveSessions *int  `toml:"min_active_sessions"`
		MaxActiveSessions *int  `toml:"max_active_sessions"`
		Resident          *bool `toml:"resident"`
	}
	if _, err := toml.DecodeFile(path, &parsed); err != nil {
		t.Fatalf("decode %s: %v", path, err)
	}
	if parsed.MinActiveSessions != nil {
		t.Fatalf("control-dispatcher agent.toml declares min_active_sessions=%d; it must be unset so the residency floor is applied conditionally (gc-0ychy P1)", *parsed.MinActiveSessions)
	}
	if parsed.MaxActiveSessions == nil || *parsed.MaxActiveSessions != 1 {
		t.Fatalf("control-dispatcher agent.toml max_active_sessions=%v, want 1 (canonical singleton)", parsed.MaxActiveSessions)
	}
	if parsed.Resident == nil || !*parsed.Resident {
		t.Fatalf("control-dispatcher agent.toml resident=%v, want true — the residency floor keys on this role-neutral opt-in (gc-0ychy P2)", parsed.Resident)
	}
}
