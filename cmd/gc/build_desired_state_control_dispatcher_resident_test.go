package main

import (
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/config"
)

// A deterministic control-dispatcher runs `gc convoy control --serve --follow`
// (config.IsDeterministicControlDispatcher): a RESIDENT serve loop that blocks
// on city events rather than exiting per bead. It ships max_active_sessions=1
// with no min floor.
//
// gc-0ychy: on a quiet tick a resident dispatcher has no queued work, so
// ComputePoolDesiredStates leaves poolDesired[template]=0. The reconciler's
// awake decision (ComputeAwakeSet, fed poolDesired as ScaleCheckCounts) then
// marks the live session ShouldWake=false and drains it with reason
// "no-wake-reason" (session_reconciler.go:3633) — independent of desiredState
// membership — and a replacement is respawned whose canonical alias collides
// with the still-draining prior generation.
//
// The fix is a role-neutral residency floor: the dispatcher declares
// resident=true in its agent.toml, and config.ApplyResidentAgentFloor floors any
// resident agent to min_active_sessions=1 so the existing pool min-fill keeps
// poolDesired>=1. The floor keys only on the resident flag (no role reference);
// it is GATED — skipped when the agent is disabled via max_active_sessions=0 or
// min is already set. The v2-control-lane gate lives in a separate SDK step,
// config.ClearDisabledControlLaneResidency, which clears the dispatcher's
// residency when daemon.formula_v2 is disabled. These tests pin the delivered
// chain: clear+floor -> ComputePoolDesiredStates -> ComputeAwakeSet. The floor's
// own gate behavior is covered in
// internal/config/control_dispatcher_residency_test.go.

const residentControlDispatcherTemplate = "core.control-dispatcher"

// residentControlDispatcherCfg builds a city with one deterministic
// control-dispatcher agent in the SHIPPED shape (max_active_sessions=1, no min
// floor, resident=true — the agent.toml opt-in). formulaV2 sets
// daemon.formula_v2 (nil = default-on). Residency is applied by
// applyControlDispatcherResidency (the clear-then-floor compose order), not here.
func residentControlDispatcherCfg(formulaV2 *bool) *config.City {
	maxActive := 1
	resident := true
	return &config.City{
		Workspace: config.Workspace{Name: "gc"},
		Daemon:    config.DaemonConfig{FormulaV2: formulaV2},
		Agents: []config.Agent{{
			Name:              config.ControlDispatcherAgentName,
			BindingName:       "core",
			StartCommand:      config.ControlDispatcherStartCommandFor("{{.Agent}}"),
			MaxActiveSessions: &maxActive,
			Resident:          &resident,
		}},
	}
}

// applyControlDispatcherResidency replicates the compose order the controller
// tick uses: clear the SDK control-lane residency when the v2 lane is disabled,
// then apply the role-neutral resident floor. Tests call this instead of the
// floor alone so the formula_v2=false path is exercised faithfully.
func applyControlDispatcherResidency(cfg *config.City) {
	config.ClearDisabledControlLaneResidency(cfg)
	config.ApplyResidentAgentFloor(cfg)
}

// createControlDispatcherSessionBead materializes a pool-managed singleton
// control-dispatcher session bead in the given lifecycle state, owning its
// canonical pool session name, so ComputePoolDesiredStates sees a live occupant.
func createControlDispatcherSessionBead(t *testing.T, store beads.Store, state string) {
	t.Helper()
	created, err := store.Create(beads.Bead{
		Title:  "control-dispatcher session",
		Type:   sessionBeadType,
		Status: "open",
		Labels: []string{sessionBeadLabel},
		Metadata: map[string]string{
			"template":                residentControlDispatcherTemplate,
			"agent_name":              residentControlDispatcherTemplate,
			"alias":                   residentControlDispatcherTemplate,
			"canonical_instance_name": residentControlDispatcherTemplate,
			"state":                   state,
			"pool_managed":            "true",
			"session_origin":          "ephemeral",
		},
	})
	if err != nil {
		t.Fatalf("create control-dispatcher session bead: %v", err)
	}
	if err := store.SetMetadata(created.ID, "session_name", PoolSessionName(residentControlDispatcherTemplate, created.ID)); err != nil {
		t.Fatalf("set session_name: %v", err)
	}
}

// poolDesiredAfterFloor applies the residency floor (the fix) then computes pool
// demand for a live dispatcher, exactly as the controller tick does:
// clear+floor -> ComputePoolDesiredStates.
func poolDesiredAfterFloor(t *testing.T, cfg *config.City, store beads.Store) int {
	t.Helper()
	applyControlDispatcherResidency(cfg)
	snap, err := loadSessionBeadSnapshot(store)
	if err != nil {
		t.Fatalf("load snapshot: %v", err)
	}
	states := ComputePoolDesiredStates(cfg, nil, snap.OpenInfos(), nil)
	return PoolDesiredCounts(states)[residentControlDispatcherTemplate]
}

// TestControlDispatcherResidencyFloorDrivesPoolDemand is the demand-path
// regression: after the config residency floor, a live dispatcher has
// poolDesired=1 when formula_v2 is on, and 0 when formula_v2 is off (no resident
// dispatcher without a v2 lane — gc-0ychy P2).
func TestControlDispatcherResidencyFloorDrivesPoolDemand(t *testing.T) {
	storeOn := beads.NewMemStore()
	createControlDispatcherSessionBead(t, storeOn, "active")
	if got := poolDesiredAfterFloor(t, residentControlDispatcherCfg(nil), storeOn); got != 1 {
		t.Fatalf("formula_v2 on: poolDesired=%d, want 1 (residency floor must keep the live dispatcher demanded, gc-0ychy)", got)
	}

	off := false
	storeOff := beads.NewMemStore()
	createControlDispatcherSessionBead(t, storeOff, "active")
	if got := poolDesiredAfterFloor(t, residentControlDispatcherCfg(&off), storeOff); got != 0 {
		t.Fatalf("formula_v2 off: poolDesired=%d, want 0 (no resident dispatcher when the v2 lane is disabled, gc-0ychy P2)", got)
	}
}

// poolDemandForDispatcherState applies the residency floor then computes pool
// demand for a single dispatcher session in the given lifecycle state, returning
// (poolDesired, "new"/mint request count, resume/wake request count).
func poolDemandForDispatcherState(t *testing.T, state string) (poolDesired, newReqs, resumeReqs int) {
	t.Helper()
	store := beads.NewMemStore()
	createControlDispatcherSessionBead(t, store, state)
	cfg := residentControlDispatcherCfg(nil)
	applyControlDispatcherResidency(cfg)
	snap, err := loadSessionBeadSnapshot(store)
	if err != nil {
		t.Fatalf("snap: %v", err)
	}
	states := ComputePoolDesiredStates(cfg, nil, snap.OpenInfos(), nil)
	for _, s := range states {
		if s.Template != residentControlDispatcherTemplate {
			continue
		}
		for _, r := range s.Requests {
			switch r.Tier {
			case "new":
				newReqs++
			case "resume", "wake-known-identity":
				resumeReqs++
			}
		}
	}
	return PoolDesiredCounts(states)[residentControlDispatcherTemplate], newReqs, resumeReqs
}

// TestControlDispatcherResidency_IdentityOwnershipLifecycle pins the unified
// identity-ownership model for a min-floored canonical-singleton dispatcher
// (gc-0ychy). An occupant OWNS the canonical identity until it is CLOSED or its
// create failed (identity released). While owned, the residency floor must never
// MINT a replacement (that mint is the colliding second generation):
//
//   - active/awake -> reuse/WAKE the same bead (a resume request, no mint).
//   - draining / drained-but-not-closed / a bare asleep with no retained reason ->
//     still owns the alias, cannot be reused -> no request (wait for close). Asleep
//     retention is reason-dependent and covered by the SleeperLifecycle table.
//   - failed-create -> identity released -> a fresh spawn (new request) is allowed.
func TestControlDispatcherResidency_IdentityOwnershipLifecycle(t *testing.T) {
	// Owned + reuse/preserve: retain/wake/keep-in-flight the SAME bead, never mint.
	// creating/start-pending is an in-flight create that must stay desired so cold
	// startup does not roll it back (gc-0ychy review-7 P1).
	for _, state := range []string{"active", "awake", "creating", "start-pending"} {
		pd, newReqs, resumeReqs := poolDemandForDispatcherState(t, state)
		if newReqs != 0 {
			t.Fatalf("state=%s: %d new(mint) requests, want 0 — an owning occupant must be reused/preserved in place, never replaced (gc-0ychy)", state, newReqs)
		}
		if pd != 1 || resumeReqs != 1 {
			t.Fatalf("state=%s: poolDesired=%d resume=%d, want 1/1 — the resident/in-flight occupant must be preserved as desired via a resume request (gc-0ychy)", state, pd, resumeReqs)
		}
	}
	// Owned but not reuse-able: wait — no replacement demand while the alias/
	// identity is still held (drained is NOT released until the bead is closed; a
	// bare asleep with no city-stop/no-wake reason is a deliberate/unknown park).
	for _, state := range []string{"draining", "drained", "asleep"} {
		pd, newReqs, resumeReqs := poolDemandForDispatcherState(t, state)
		if newReqs != 0 || pd != 0 || resumeReqs != 0 {
			t.Fatalf("state=%s: poolDesired=%d new=%d resume=%d, want 0/0/0 — a %s occupant still owns the identity; minting a replacement collides (gc-0ychy)", state, pd, newReqs, resumeReqs, state)
		}
	}
	// Identity released (failed-create): a clean fresh spawn is allowed.
	if pd, newReqs, _ := poolDemandForDispatcherState(t, "failed-create"); pd != 1 || newReqs != 1 {
		t.Fatalf("state=failed-create: poolDesired=%d new=%d, want 1/1 — a failed-create bead has released the identity, so a fresh spawn must be allowed (gc-0ychy)", pd, newReqs)
	}
}

// poolDemandForSleeper creates an ASLEEP dispatcher with the given sleep_reason,
// applies the residency floor, and returns (poolDesired, new/mint requests,
// resume/wake requests).
func poolDemandForSleeper(t *testing.T, sleepReason string) (poolDesired, newReqs, resumeReqs int) {
	t.Helper()
	store := beads.NewMemStore()
	created, err := store.Create(beads.Bead{
		Title:  "control-dispatcher session",
		Type:   sessionBeadType,
		Status: "open",
		Labels: []string{sessionBeadLabel},
		Metadata: map[string]string{
			"template":                residentControlDispatcherTemplate,
			"agent_name":              residentControlDispatcherTemplate,
			"alias":                   residentControlDispatcherTemplate,
			"canonical_instance_name": residentControlDispatcherTemplate,
			"state":                   "asleep",
			"sleep_reason":            sleepReason,
			"pool_managed":            "true",
			"session_origin":          "ephemeral",
		},
	})
	if err != nil {
		t.Fatalf("create sleeper: %v", err)
	}
	if err := store.SetMetadata(created.ID, "session_name", PoolSessionName(residentControlDispatcherTemplate, created.ID)); err != nil {
		t.Fatalf("set session_name: %v", err)
	}
	cfg := residentControlDispatcherCfg(nil)
	applyControlDispatcherResidency(cfg)
	snap, err := loadSessionBeadSnapshot(store)
	if err != nil {
		t.Fatalf("snap: %v", err)
	}
	states := ComputePoolDesiredStates(cfg, nil, snap.OpenInfos(), nil)
	for _, s := range states {
		if s.Template != residentControlDispatcherTemplate {
			continue
		}
		for _, r := range s.Requests {
			switch r.Tier {
			case "new":
				newReqs++
			case "resume", "wake-known-identity":
				resumeReqs++
			}
		}
	}
	return PoolDesiredCounts(states)[residentControlDispatcherTemplate], newReqs, resumeReqs
}

// TestControlDispatcherResidency_SleeperLifecycle pins residency reuse as an
// ALLOWLIST keyed on WHY the dispatcher slept (gc-0ychy). Only a city-stop sleep
// and a deterministic-control-dispatcher no-wake-reason completed drain are
// retained (reuse/WAKE the same bead). EVERY other sleep — deliberate parks
// (user-hold, wait-hold, rate-limit, quarantine, config-drift) and terminal
// sleeps (idle, idle-timeout, provider-terminal-error, max-session-age, ...) —
// must NOT be woken and must NOT mint a colliding replacement: the mint is held
// and the reconciler frees/cleans the slot or leaves the park in place. The table
// spans the SleepReason constants so a new "default reuse" can't creep back in.
func TestControlDispatcherResidency_SleeperLifecycle(t *testing.T) {
	retain := []string{"city-stop", "no-wake-reason"}
	hold := []string{
		"idle", "idle-timeout", "config-drift", "drained", "user-hold",
		"wait-hold", "rate_limit", "provider-terminal-error", "runtime-missing",
		"quarantine", "context-churn", "max-session-age", "assigned-work-exhausted",
		"failed-create", "some-future-unknown-reason",
	}
	for _, reason := range retain {
		pd, newReqs, resumeReqs := poolDemandForSleeper(t, reason)
		if pd != 1 || resumeReqs != 1 || newReqs != 0 {
			t.Fatalf("sleep_reason=%s: poolDesired=%d new=%d resume=%d, want 1/0/1 — a retained sleeper must be reused/woken in place (gc-0ychy)", reason, pd, newReqs, resumeReqs)
		}
	}
	for _, reason := range hold {
		pd, newReqs, resumeReqs := poolDemandForSleeper(t, reason)
		if pd != 0 || newReqs != 0 || resumeReqs != 0 {
			t.Fatalf("sleep_reason=%s: poolDesired=%d new=%d resume=%d, want 0/0/0 — a deliberate/terminal/unknown sleep must not be woken and must not mint a colliding replacement (gc-0ychy)", reason, pd, newReqs, resumeReqs)
		}
	}
}

// TestControlDispatcherResidency_NoWakeRetentionScopedToResident pins that the
// no-wake-reason retention is role-neutral: it is scoped to Agent.IsResident(),
// not to the control-dispatcher role. A NON-resident canonical-singleton pool
// agent with a min floor does NOT reuse a no-wake-reason sleeper (it is
// held/cleaned), while an arbitrary RESIDENT pack agent DOES — proving any pack
// resident opts into the same protection. city-stop retention (general min-floor
// semantics) still applies regardless of residency.
func TestControlDispatcherResidency_NoWakeRetentionScopedToResident(t *testing.T) {
	maxActive, minActive := 1, 1
	const workerTemplate = "repo.singleton" // canonical singleton (max=1), NOT a control-dispatcher
	demand := func(sleepReason string, resident bool) (int, int, int) {
		agent := config.Agent{
			Name:              "singleton",
			BindingName:       "repo",
			StartCommand:      "sh -c 'sleep 1'", // not a `convoy control --serve` dispatcher
			MaxActiveSessions: &maxActive,
			MinActiveSessions: &minActive,
		}
		if resident {
			r := true
			agent.Resident = &r
		}
		cfg := &config.City{
			Workspace: config.Workspace{Name: "gc"},
			Agents:    []config.Agent{agent},
		}
		if config.IsDeterministicControlDispatcher(&cfg.Agents[0]) {
			t.Fatal("fixture agent must NOT be a deterministic control-dispatcher")
		}
		store := beads.NewMemStore()
		created, err := store.Create(beads.Bead{
			Title: "singleton session", Type: sessionBeadType, Status: "open", Labels: []string{sessionBeadLabel},
			Metadata: map[string]string{
				"template": workerTemplate, "agent_name": workerTemplate, "alias": workerTemplate,
				"state": "asleep", "sleep_reason": sleepReason, "pool_managed": "true", "session_origin": "ephemeral",
			},
		})
		if err != nil {
			t.Fatalf("create: %v", err)
		}
		if err := store.SetMetadata(created.ID, "session_name", PoolSessionName(workerTemplate, created.ID)); err != nil {
			t.Fatalf("set session_name: %v", err)
		}
		snap, err := loadSessionBeadSnapshot(store)
		if err != nil {
			t.Fatalf("snap: %v", err)
		}
		states := ComputePoolDesiredStates(cfg, nil, snap.OpenInfos(), nil)
		var newReqs, resumeReqs int
		for _, s := range states {
			if s.Template != workerTemplate {
				continue
			}
			for _, r := range s.Requests {
				switch r.Tier {
				case "new":
					newReqs++
				case "resume", "wake-known-identity":
					resumeReqs++
				}
			}
		}
		return PoolDesiredCounts(states)[workerTemplate], newReqs, resumeReqs
	}
	// no-wake-reason is NOT retained for a NON-resident singleton (held).
	if pd, newReqs, resumeReqs := demand("no-wake-reason", false); pd != 0 || newReqs != 0 || resumeReqs != 0 {
		t.Fatalf("non-resident no-wake-reason: poolDesired=%d new=%d resume=%d, want 0/0/0 — no-wake retention is scoped to resident agents (gc-0ychy)", pd, newReqs, resumeReqs)
	}
	// no-wake-reason IS retained for a RESIDENT pack agent (role-neutral opt-in).
	if pd, newReqs, resumeReqs := demand("no-wake-reason", true); pd != 1 || newReqs != 0 || resumeReqs != 1 {
		t.Fatalf("resident pack agent no-wake-reason: poolDesired=%d new=%d resume=%d, want 1/0/1 — any resident agent opts into no-wake retention (gc-0ychy P2)", pd, newReqs, resumeReqs)
	}
	// city-stop retention is general and still applies (independent of residency).
	if pd, _, resumeReqs := demand("city-stop", false); pd != 1 || resumeReqs != 1 {
		t.Fatalf("non-resident city-stop: poolDesired=%d resume=%d, want 1/1 — city-stop retention is a general min-floor semantic", pd, resumeReqs)
	}
}

// awakeDecisionForLiveDispatcher runs the awake decision (ComputeAwakeSet — the
// function that gates the reconciler's no-wake-reason drain at
// session_reconciler.go:3633) for one live active resident dispatcher whose pool
// demand is `poolDesired`. buildAwakeInputFromReconciler feeds poolDesired in as
// ScaleCheckCounts, so this is the faithful production signal.
func awakeDecisionForLiveDispatcher(poolDesired int) AwakeDecision {
	const sn = "core__control-dispatcher-gc-1"
	return ComputeAwakeSet(AwakeInput{
		Agents:           []AwakeAgent{{QualifiedName: residentControlDispatcherTemplate, MinActiveSessions: 1}},
		SessionBeads:     []AwakeSessionBead{{ID: "s1", SessionName: sn, Template: residentControlDispatcherTemplate, State: "active"}},
		ScaleCheckCounts: map[string]int{residentControlDispatcherTemplate: poolDesired},
		RunningSessions:  map[string]bool{sn: true},
		Now:              time.Now().UTC(),
	})[sn]
}

// TestComputeAwakeSet_ResidentDispatcherWakesOnlyWithDemand reproduces the
// no-wake-reason condition that drains the resident serve loop (gc-0ychy): a live
// active singleton with zero pool demand gets ShouldWake=false (→ drained at
// session_reconciler.go:3633), and only pool demand (poolDesired>0) wakes it. The
// AwakeInput min pass alone (MinActiveSessions:1 above) does NOT wake a live idle
// singleton — which is why the fix flows through demand.
func TestComputeAwakeSet_ResidentDispatcherWakesOnlyWithDemand(t *testing.T) {
	if d := awakeDecisionForLiveDispatcher(0); d.ShouldWake {
		t.Fatalf("poolDesired=0: ShouldWake=true reason=%q, want false — a demandless live singleton must be classed no-wake-reason (gc-0ychy)", d.Reason)
	}
	if d := awakeDecisionForLiveDispatcher(1); !d.ShouldWake {
		t.Fatalf("poolDesired=1: ShouldWake=false, want true (scaled:demand must keep the live resident singleton awake)")
	} else if d.Reason != "scaled:demand" {
		t.Fatalf("poolDesired=1: wake reason=%q, want \"scaled:demand\"", d.Reason)
	}
}

// TestComputeAwakeSet_ResidentNoWakeSleeperWakesOnDemand is the P1 regression
// for the cfb627a2f review (gc-0ychy): canonicalSingletonResidency RETAINS an
// asleep resident singleton whose completed drain left sleep_reason=no-wake-
// reason (reuse the same bead, suppress a replacement mint), but the awake
// decision must ALSO wake that lifecycle state — otherwise the bead holds the
// canonical alias asleep indefinitely (a dead lane). A resident, min-floored,
// no-wake-reason sleeper with demand=1 must receive an explicit wake cause and
// ShouldWake=true. A NON-resident min-floored agent's no-wake-reason sleeper
// stays asleep (held), matching the pool retention allowlist.
func TestComputeAwakeSet_ResidentNoWakeSleeperWakesOnDemand(t *testing.T) {
	const sn = "core__control-dispatcher-gc-1"
	decide := func(resident bool) AwakeDecision {
		return ComputeAwakeSet(AwakeInput{
			Agents: []AwakeAgent{{
				QualifiedName:     residentControlDispatcherTemplate,
				MinActiveSessions: 1,
				Resident:          resident,
			}},
			SessionBeads: []AwakeSessionBead{{
				ID:          "s1",
				SessionName: sn,
				Template:    residentControlDispatcherTemplate,
				State:       "asleep",
				SleepReason: "no-wake-reason",
			}},
			ScaleCheckCounts: map[string]int{residentControlDispatcherTemplate: 1},
			Now:              time.Now().UTC(),
		})[sn]
	}
	if d := decide(true); !d.ShouldWake {
		t.Fatalf("resident no-wake sleeper + demand=1: ShouldWake=false, want true — a retained resident serve loop must be woken, not stranded holding the alias (gc-0ychy)")
	} else if d.Reason == "" {
		t.Fatalf("resident no-wake sleeper woken with empty reason, want an explicit wake cause (gc-0ychy)")
	}
	if d := decide(false); d.ShouldWake {
		t.Fatalf("non-resident no-wake sleeper: ShouldWake=true reason=%q, want false — no-wake retention is scoped to resident agents, matching the pool allowlist (gc-0ychy)", d.Reason)
	}
}

// createControlDispatcherSessionBeadWithTrigger materializes a pool-managed
// singleton control-dispatcher session bead in the given lifecycle state with the
// given trigger metadata (empty values are omitted), so the residency retention
// path can be exercised with an in-flight bead that already carries a work
// binding.
func createControlDispatcherSessionBeadWithTrigger(t *testing.T, store beads.Store, state, sleepReason, triggerBeadID, triggerStoreRef, brainParentSID string) {
	t.Helper()
	md := map[string]string{
		"template":                residentControlDispatcherTemplate,
		"agent_name":              residentControlDispatcherTemplate,
		"alias":                   residentControlDispatcherTemplate,
		"canonical_instance_name": residentControlDispatcherTemplate,
		"state":                   state,
		"pool_managed":            "true",
		"session_origin":          "ephemeral",
	}
	if sleepReason != "" {
		md["sleep_reason"] = sleepReason
	}
	if triggerBeadID != "" {
		md["gc.trigger_bead_id"] = triggerBeadID
	}
	if triggerStoreRef != "" {
		md["gc.trigger_bead_store_ref"] = triggerStoreRef
	}
	if brainParentSID != "" {
		md["gc.brain_parent_sid"] = brainParentSID
	}
	created, err := store.Create(beads.Bead{
		Title:    "control-dispatcher session",
		Type:     sessionBeadType,
		Status:   "open",
		Labels:   []string{sessionBeadLabel},
		Metadata: md,
	})
	if err != nil {
		t.Fatalf("create control-dispatcher session bead: %v", err)
	}
	if err := store.SetMetadata(created.ID, "session_name", PoolSessionName(residentControlDispatcherTemplate, created.ID)); err != nil {
		t.Fatalf("set session_name: %v", err)
	}
}

// residencyResumeRequestForState applies the residency floor and computes pool
// desired state at the production boundary (ComputePoolDesiredStatesWithDemandTraced)
// for a single dispatcher session in the given lifecycle state, alongside routed
// scale demand for the template. It returns the retained "resume" request so the
// test can assert it carries the full routing context.
func residencyResumeRequestForState(t *testing.T, state, sleepReason, triggerBeadID, triggerStoreRef, brainParentSID string, demand scaleCheckDemand) SessionRequest {
	t.Helper()
	store := beads.NewMemStore()
	createControlDispatcherSessionBeadWithTrigger(t, store, state, sleepReason, triggerBeadID, triggerStoreRef, brainParentSID)
	cfg := residentControlDispatcherCfg(nil)
	applyControlDispatcherResidency(cfg)
	snap, err := loadSessionBeadSnapshot(store)
	if err != nil {
		t.Fatalf("snap: %v", err)
	}
	states := ComputePoolDesiredStatesWithDemandTraced(
		cfg, nil, snap.OpenInfos(),
		map[string]int{residentControlDispatcherTemplate: demand.Count},
		map[string]scaleCheckDemand{residentControlDispatcherTemplate: demand},
		nil,
	)
	for _, s := range states {
		if s.Template != residentControlDispatcherTemplate {
			continue
		}
		for _, r := range s.Requests {
			if isResumeLikeTier(r.Tier) {
				return r
			}
		}
	}
	t.Fatalf("state=%s: no resume-like request for the retained resident singleton", state)
	return SessionRequest{}
}

// TestControlDispatcherResidency_RetainedResumePreservesRoutedContext is the P1
// regression for the 2bf5273d6 review (gc-0ychy): reusing a resident singleton in
// place consumes its single cap slot ahead of routed scale demand, so a context-
// free retention resume silently DROPS the routed work's WorkBeadID / pack /
// workspace / store / fork-parent. An idle active or asleep singleton that has no
// trigger of its own must adopt the routed scale demand's full context onto the
// retained resume; an in-flight (creating) bead that already carries trigger
// metadata must keep it (an in-flight bind must not clear it).
func TestControlDispatcherResidency_RetainedResumePreservesRoutedContext(t *testing.T) {
	routed := scaleCheckDemand{
		Count:       1,
		WorkBeadIDs: []string{"gc-work-1"},
		Titles:      map[string]string{"gc-work-1": "routed control work"},
		Packs:       map[string]string{"gc-work-1": "corepack"},
		Workspaces:  map[string]string{"gc-work-1": "ws-a"},
		StoreRefs:   map[string]string{"gc-work-1": "rig:gascity"},
		ParentSIDs:  map[string]string{"gc-work-1": "parent-sid-1"},
	}
	// Idle occupants with no trigger of their own adopt the routed demand context.
	for _, tc := range []struct{ state, sleepReason string }{
		{"active", ""},
		{"asleep", "no-wake-reason"},
	} {
		r := residencyResumeRequestForState(t, tc.state, tc.sleepReason, "", "", "", routed)
		if r.WorkBeadID != "gc-work-1" || r.WorkPack != "corepack" || r.WorkWorkspace != "ws-a" ||
			r.WorkStoreRef != "rig:gascity" || r.BrainParentSID != "parent-sid-1" {
			t.Fatalf("state=%s: retained resume dropped routed context: WorkBeadID=%q pack=%q workspace=%q store=%q parent=%q — the residency reuse must preserve routed scale demand, not consume the cap context-free (gc-0ychy)",
				tc.state, r.WorkBeadID, r.WorkPack, r.WorkWorkspace, r.WorkStoreRef, r.BrainParentSID)
		}
	}
	// An in-flight (creating) bead with its own trigger keeps it — the bind must
	// not clear the trigger metadata. pack/workspace enrich from the demand keyed
	// on the same work bead.
	r := residencyResumeRequestForState(t, "creating", "", "gc-work-1", "rig:gascity", "parent-sid-1", routed)
	if r.WorkBeadID != "gc-work-1" || r.WorkStoreRef != "rig:gascity" || r.BrainParentSID != "parent-sid-1" ||
		r.WorkPack != "corepack" || r.WorkWorkspace != "ws-a" {
		t.Fatalf("state=creating: retained resume lost in-flight trigger/context: WorkBeadID=%q pack=%q workspace=%q store=%q parent=%q — an in-flight bind must not clear the bead's trigger metadata (gc-0ychy)",
			r.WorkBeadID, r.WorkPack, r.WorkWorkspace, r.WorkStoreRef, r.BrainParentSID)
	}
}

// TestControlDispatcherResidencyEndToEnd ties the whole delivered chain: config
// floor -> ComputePoolDesiredStates -> ComputeAwakeSet ShouldWake. With
// formula_v2 on the live dispatcher stays awake; with formula_v2 off the floor is
// skipped, poolDesired stays 0, and it is classed no-wake-reason (drained).
func TestControlDispatcherResidencyEndToEnd(t *testing.T) {
	const sn = "core__control-dispatcher-gc-1"
	chainShouldWake := func(formulaV2 *bool) (bool, int) {
		store := beads.NewMemStore()
		createControlDispatcherSessionBead(t, store, "active")
		poolDesired := poolDesiredAfterFloor(t, residentControlDispatcherCfg(formulaV2), store)
		res := ComputeAwakeSet(AwakeInput{
			Agents:           []AwakeAgent{{QualifiedName: residentControlDispatcherTemplate, MinActiveSessions: 1}},
			SessionBeads:     []AwakeSessionBead{{ID: "s1", SessionName: sn, Template: residentControlDispatcherTemplate, State: "active"}},
			ScaleCheckCounts: map[string]int{residentControlDispatcherTemplate: poolDesired},
			RunningSessions:  map[string]bool{sn: true},
			Now:              time.Now().UTC(),
		})
		return res[sn].ShouldWake, poolDesired
	}

	if wake, pd := chainShouldWake(nil); !wake {
		t.Fatalf("formula_v2 on: end-to-end ShouldWake=false (poolDesired=%d), want true — the config floor must keep the resident serve loop awake", pd)
	}
	off := false
	if wake, pd := chainShouldWake(&off); wake {
		t.Fatalf("formula_v2 off: end-to-end ShouldWake=true (poolDesired=%d), want false — a disabled v2 lane must not keep a resident dispatcher (gc-0ychy P2)", pd)
	}
}

// TestOrdinaryPoolWorkerWithoutMinFloorHasNoDemand guards that residency is not a
// blanket "keep every live singleton": an ordinary pool worker (untouched by the
// dispatcher floor) with no work still has zero demand and drains when idle.
func TestOrdinaryPoolWorkerWithoutMinFloorHasNoDemand(t *testing.T) {
	maxActive := 1
	const workerTemplate = "repo.worker"
	cfg := &config.City{
		Workspace: config.Workspace{Name: "gc"},
		Agents: []config.Agent{{
			Name:              "worker",
			BindingName:       "repo",
			StartCommand:      "sh -c 'sleep 1'",
			MaxActiveSessions: &maxActive,
		}},
	}
	applyControlDispatcherResidency(cfg) // no-op for a non-resident agent
	store := beads.NewMemStore()
	created, err := store.Create(beads.Bead{
		Title:  "worker session",
		Type:   sessionBeadType,
		Status: "open",
		Labels: []string{sessionBeadLabel},
		Metadata: map[string]string{
			"template":       workerTemplate,
			"agent_name":     workerTemplate,
			"alias":          workerTemplate,
			"state":          "active",
			"pool_managed":   "true",
			"session_origin": "ephemeral",
		},
	})
	if err != nil {
		t.Fatalf("create worker session bead: %v", err)
	}
	if err := store.SetMetadata(created.ID, "session_name", PoolSessionName(workerTemplate, created.ID)); err != nil {
		t.Fatalf("set session_name: %v", err)
	}
	snap, err := loadSessionBeadSnapshot(store)
	if err != nil {
		t.Fatalf("snap: %v", err)
	}
	got := PoolDesiredCounts(ComputePoolDesiredStates(cfg, nil, snap.OpenInfos(), nil))[workerTemplate]
	if got != 0 {
		t.Fatalf("ordinary pool worker: poolDesired=%d, want 0 — the resident floor must not touch non-dispatcher agents (gc-0ychy)", got)
	}
}

// TestControlDispatcherMinFloorKeepsCanonicalIdentity guards that the delivered
// min floor does not flip the singleton to numbered "-1" identity: max=1 keeps
// UsesCanonicalSingletonPoolIdentity true and SupportsExpandedSessionIdentities
// false with a min floor.
func TestControlDispatcherMinFloorKeepsCanonicalIdentity(t *testing.T) {
	cfg := residentControlDispatcherCfg(nil)
	applyControlDispatcherResidency(cfg)
	a := cfg.Agents[0]
	if a.MinActiveSessions == nil || *a.MinActiveSessions != 1 {
		t.Fatalf("precondition: floor did not set min=1 (got %v)", a.MinActiveSessions)
	}
	if !a.UsesCanonicalSingletonPoolIdentity() {
		t.Fatalf("floored dispatcher lost canonical singleton identity (UsesCanonicalSingletonPoolIdentity=false)")
	}
	if a.SupportsExpandedSessionIdentities() {
		t.Fatalf("floored dispatcher gained numbered instance identities (SupportsExpandedSessionIdentities=true)")
	}
}
