package main

import (
	"io"
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/runtime"
)

// Production-boundary regressions for the residency GATES at
// buildDesiredStateWithSessionBeads (gc-0ychy stable-line integration review,
// finding 1): the composed config is the single source of dispatcher retention.
// A running deterministic dispatcher whose residency is OFF — because the v2
// control lane is disabled (ClearDisabledControlLaneResidency) or because the
// operator overrode resident=false — must NOT be floored back into demand by
// any role-specific overlay inside buildDesiredState itself.

func residentGateDesired(t *testing.T, formulaV2 *bool, resident bool) int {
	t.Helper()
	cfg := residentControlDispatcherCfg(formulaV2)
	cfg.Agents[0].Resident = &resident
	applyControlDispatcherResidency(cfg)

	cityStore := beads.NewMemStore()
	createControlDispatcherSessionBead(t, cityStore, "active")
	snap, err := loadSessionBeadSnapshot(cityStore)
	if err != nil {
		t.Fatalf("load snapshot: %v", err)
	}

	dsResult := buildDesiredStateWithSessionBeads(
		"gc", t.TempDir(), time.Now().UTC(), cfg, runtime.NewFake(),
		cityStore, nil, snap, nil, io.Discard,
	)
	// Retention is the POOL outcome: scale_check demand from the desired-state
	// pass plus the composed min floor, exactly as the controller consumes it.
	states := ComputePoolDesiredStates(cfg, nil, snap.OpenInfos(), dsResult.ScaleCheckCounts)
	return PoolDesiredCounts(states)[residentControlDispatcherTemplate]
}

func TestBuildDesiredState_FormulaV2OffDoesNotFloorRunningDispatcher(t *testing.T) {
	off := false
	if got := residentGateDesired(t, &off, true); got != 0 {
		t.Fatalf("formula_v2 off: poolDesired[%s] = %d, want 0 — a role-specific "+
			"floor inside buildDesiredState bypasses ClearDisabledControlLaneResidency "+
			"and retains a dispatcher whose control lane is disabled (gc-0ychy integration HIGH-1)",
			residentControlDispatcherTemplate, got)
	}
}

func TestBuildDesiredState_ResidentFalseOverrideDoesNotFloorRunningDispatcher(t *testing.T) {
	if got := residentGateDesired(t, nil, false); got != 0 {
		t.Fatalf("resident=false: poolDesired[%s] = %d, want 0 — an explicit "+
			"operator override must not be bypassed by a role-specific floor inside "+
			"buildDesiredState (gc-0ychy integration HIGH-1)",
			residentControlDispatcherTemplate, got)
	}
}

func TestBuildDesiredState_ComposedResidencyStillFloorsRunningDispatcher(t *testing.T) {
	if got := residentGateDesired(t, nil, true); got != 1 {
		t.Fatalf("resident=true, v2 on: poolDesired[%s] = %d, want 1 — the composed "+
			"residency floor is the retention path and must survive the overlay removal",
			residentControlDispatcherTemplate, got)
	}
}
