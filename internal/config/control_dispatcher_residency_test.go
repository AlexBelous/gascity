package config

import "testing"

// dispatcherAgent builds a deterministic control-dispatcher agent in the shipped
// shape (providerless resident serve command, resident=true). max/min are
// optional overrides.
func dispatcherAgent(maxActive, minActive *int) Agent {
	resident := true
	return Agent{
		Name:              ControlDispatcherAgentName,
		BindingName:       "core",
		StartCommand:      ControlDispatcherStartCommandFor("{{.Agent}}"),
		MaxActiveSessions: maxActive,
		MinActiveSessions: minActive,
		Resident:          &resident,
	}
}

func cityWithDispatcher(a Agent, formulaV2 *bool) *City {
	return &City{
		Workspace: Workspace{Name: "gc"},
		Daemon:    DaemonConfig{FormulaV2: formulaV2},
		Agents:    []Agent{a},
	}
}

// residentPackAgent builds an ordinary pack agent (with a provider, NOT the
// control-dispatcher) that opts into residency via config. It proves the floor is
// role-neutral: any pack agent can declare resident=true (gc-0ychy review-7 P2).
func residentPackAgent(maxActive, minActive *int) Agent {
	resident := true
	return Agent{
		Name:              "watcher",
		BindingName:       "repo",
		Provider:          "claude",
		StartCommand:      "sh -c 'exec my-serve-loop'",
		MaxActiveSessions: maxActive,
		MinActiveSessions: minActive,
		Resident:          &resident,
	}
}

// TestApplyResidentAgentFloor_SetsFloorForResident: an enabled resident agent
// (max=1, min unset) is floored to min_active_sessions=1 so its serve loop keeps
// pool demand (gc-0ychy).
func TestApplyResidentAgentFloor_SetsFloorForResident(t *testing.T) {
	cfg := cityWithDispatcher(dispatcherAgent(ptrInt(1), nil), nil)
	ApplyResidentAgentFloor(cfg)
	got := cfg.Agents[0].MinActiveSessions
	if got == nil || *got != 1 {
		t.Fatalf("MinActiveSessions=%v, want 1 (resident floor, gc-0ychy)", got)
	}
	if err := ValidateAgents(cfg.Agents); err != nil {
		t.Fatalf("ValidateAgents after floor: %v", err)
	}
}

// TestApplyResidentAgentFloor_RoleNeutralPackAgent: the floor keys ONLY on the
// resident flag, so an arbitrary pack-defined resident agent (with a provider,
// not the control-dispatcher) is floored on equal footing — the zero-hardcoded-
// roles fix for review-7 P2. It also proves the floor no longer gates on
// formula_v2: a non-dispatcher resident is floored regardless of that flag.
func TestApplyResidentAgentFloor_RoleNeutralPackAgent(t *testing.T) {
	off := false
	cfg := &City{
		Workspace: Workspace{Name: "gc"},
		Daemon:    DaemonConfig{FormulaV2: &off}, // v2 off must not affect a non-dispatcher resident
		Agents:    []Agent{residentPackAgent(ptrInt(1), nil)},
	}
	ApplyResidentAgentFloor(cfg)
	got := cfg.Agents[0].MinActiveSessions
	if got == nil || *got != 1 {
		t.Fatalf("pack resident MinActiveSessions=%v, want 1 — the floor must be role-neutral (gc-0ychy P2)", got)
	}
	if err := ValidateAgents(cfg.Agents); err != nil {
		t.Fatalf("ValidateAgents after role-neutral floor: %v", err)
	}
}

// TestApplyResidentAgentFloor_SkipsWhenDisabled is the P1 regression: a
// max_active_sessions=0 disable override must win — the floor must not be applied
// (no residency) and must never synthesize min>max, which would fail
// ValidateAgents / a controller reload.
func TestApplyResidentAgentFloor_SkipsWhenDisabled(t *testing.T) {
	cfg := cityWithDispatcher(dispatcherAgent(ptrInt(0), nil), nil)
	ApplyResidentAgentFloor(cfg)
	if got := cfg.Agents[0].MinActiveSessions; got != nil {
		t.Fatalf("MinActiveSessions=%v, want nil — a disabled (max=0) resident must not be floored (gc-0ychy P1)", *got)
	}
	if err := ValidateAgents(cfg.Agents); err != nil {
		t.Fatalf("ValidateAgents after floor on a disabled resident: %v — the floor must never produce min>max", err)
	}
}

// TestApplyResidentAgentFloor_RespectsExplicitMin: an operator that set
// min_active_sessions explicitly (including 0) keeps their value.
func TestApplyResidentAgentFloor_RespectsExplicitMin(t *testing.T) {
	cfg := cityWithDispatcher(dispatcherAgent(ptrInt(1), ptrInt(0)), nil)
	ApplyResidentAgentFloor(cfg)
	if got := cfg.Agents[0].MinActiveSessions; got == nil || *got != 0 {
		t.Fatalf("MinActiveSessions=%v, want 0 — an explicit min override must win over the floor", got)
	}
}

// TestApplyResidentAgentFloor_IgnoresNonResident: the floor is scoped to resident
// agents; ordinary agents (resident unset) are untouched.
func TestApplyResidentAgentFloor_IgnoresNonResident(t *testing.T) {
	cfg := &City{
		Workspace: Workspace{Name: "gc"},
		Agents: []Agent{{
			Name:              "worker",
			BindingName:       "repo",
			Provider:          "claude",
			StartCommand:      "sh -c 'sleep 1'",
			MaxActiveSessions: ptrInt(1),
		}},
	}
	ApplyResidentAgentFloor(cfg)
	if got := cfg.Agents[0].MinActiveSessions; got != nil {
		t.Fatalf("non-resident agent MinActiveSessions=%v, want nil — the floor must not touch non-resident agents", *got)
	}
}

// TestClearDisabledControlLaneResidency_ClearsDispatcherWhenV2Off is the P2
// regression: with daemon.formula_v2=false the v2 control lane is off, so the
// dispatcher's residency is cleared and the subsequent floor leaves it unfloored
// (scales to zero as before).
func TestClearDisabledControlLaneResidency_ClearsDispatcherWhenV2Off(t *testing.T) {
	off := false
	cfg := cityWithDispatcher(dispatcherAgent(ptrInt(1), nil), &off)
	ClearDisabledControlLaneResidency(cfg)
	if cfg.Agents[0].IsResident() {
		t.Fatalf("dispatcher still resident with formula_v2=false — the control lane is disabled (gc-0ychy P2)")
	}
	ApplyResidentAgentFloor(cfg)
	if got := cfg.Agents[0].MinActiveSessions; got != nil {
		t.Fatalf("MinActiveSessions=%v, want nil — no resident dispatcher when daemon.formula_v2=false (gc-0ychy P2)", *got)
	}
}

// TestClearDisabledControlLaneResidency_KeepsDispatcherWhenV2On: with the v2 lane
// enabled (default) the dispatcher stays resident and is floored.
func TestClearDisabledControlLaneResidency_KeepsDispatcherWhenV2On(t *testing.T) {
	cfg := cityWithDispatcher(dispatcherAgent(ptrInt(1), nil), nil) // formula_v2 default-on
	ClearDisabledControlLaneResidency(cfg)
	if !cfg.Agents[0].IsResident() {
		t.Fatalf("dispatcher residency cleared with formula_v2 enabled — must stay resident")
	}
	ApplyResidentAgentFloor(cfg)
	if got := cfg.Agents[0].MinActiveSessions; got == nil || *got != 1 {
		t.Fatalf("MinActiveSessions=%v, want 1 — an enabled v2 dispatcher is floored", got)
	}
}

// TestClearDisabledControlLaneResidency_IgnoresNonDispatcherResident: the v2-lane
// gate is scoped to the SDK control-dispatcher; a pack-defined resident agent is
// NOT cleared even when formula_v2=false (residency is its own capability,
// independent of the control lane).
func TestClearDisabledControlLaneResidency_IgnoresNonDispatcherResident(t *testing.T) {
	off := false
	cfg := &City{
		Workspace: Workspace{Name: "gc"},
		Daemon:    DaemonConfig{FormulaV2: &off},
		Agents:    []Agent{residentPackAgent(ptrInt(1), nil)},
	}
	ClearDisabledControlLaneResidency(cfg)
	if !cfg.Agents[0].IsResident() {
		t.Fatalf("pack resident cleared by the control-lane gate — it must only touch the dispatcher")
	}
}
