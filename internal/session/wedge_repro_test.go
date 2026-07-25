package session

import (
	"testing"
	"time"
)

// TestCreatingWedgesWhenRuntimeUnobserved demonstrates ga-pofwv9: a session bead
// stuck in state=creating is only healed when the runtime was OBSERVED. When the
// runtime probe returns Observed=false — the case for a session that never
// acquired a session_key, so there is nothing to probe — projectRuntimeProjection
// bails at the !input.Runtime.Observed guard and returns the stored state
// unchanged, never reaching the BaseStateCreating staleness branch that would
// heal it. The bead then sits in "creating" forever while still counting against
// pool occupancy.
//
// Runtime.Observed is the ONLY field that differs between the two subtests.
func TestCreatingWedgesWhenRuntimeUnobserved(t *testing.T) {
	now := time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)
	longAgo := now.Add(-12 * 24 * time.Hour) // 12 days, matching gm-4l0p2

	newInput := func(observed bool) LifecycleInput {
		return LifecycleInput{
			Status:                 "open",
			StoredState:            string(StateCreating),
			PendingCreateStartedAt: longAgo.Format(time.RFC3339),
			CreatedAt:              longAgo,
			StaleCreatingAfter:     time.Minute,
			Now:                    now,
			Runtime:                RuntimeFacts{Observed: observed, Alive: false},
		}
	}

	t.Run("observed dead runtime heals to asleep", func(t *testing.T) {
		got := ProjectLifecycle(newInput(true))
		t.Logf("observed=true  -> RuntimeProjection=%q ReconciledState=%q CountsAgainstCap=%v",
			got.RuntimeProjection, got.ReconciledState, got.CountsAgainstCap)
		if got.ReconciledState != StateAsleep {
			t.Errorf("ReconciledState = %q, want %q", got.ReconciledState, StateAsleep)
		}
		if got.RuntimeProjection != RuntimeProjectionStaleCreating {
			t.Errorf("RuntimeProjection = %q, want %q", got.RuntimeProjection, RuntimeProjectionStaleCreating)
		}
	})

	t.Run("unobserved runtime stays creating forever", func(t *testing.T) {
		got := ProjectLifecycle(newInput(false))
		t.Logf("observed=false -> RuntimeProjection=%q ReconciledState=%q CountsAgainstCap=%v",
			got.RuntimeProjection, got.ReconciledState, got.CountsAgainstCap)

		// This is the assertion that SHOULD hold once ga-pofwv9 is fixed: a
		// 12-day-old creating bead must not still be projected as creating,
		// and must not keep occupying a pool slot.
		if got.ReconciledState == StateCreating {
			t.Errorf("BUG ga-pofwv9: 12-day-old creating bead still projects ReconciledState=%q "+
				"(RuntimeProjection=%q, CountsAgainstCap=%v) — staleness never evaluated because "+
				"Runtime.Observed=false short-circuits projectRuntimeProjection",
				got.ReconciledState, got.RuntimeProjection, got.CountsAgainstCap)
		}
	})
}

// TestCreatingStaleDetectionIgnoresPendingCreateClaim pins the ALTERNATIVE
// hypothesis: that pending_create_claim gating is what wedges the bead. If the
// claim were the discriminator, flipping it alone would change the projection.
// It does not — both claim values wedge identically when the runtime is
// unobserved — which disproves the claim-gating theory for THIS path.
func TestCreatingStaleDetectionIgnoresPendingCreateClaim(t *testing.T) {
	now := time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)
	longAgo := now.Add(-12 * 24 * time.Hour)

	newInput := func(claim bool) LifecycleInput {
		return LifecycleInput{
			Status:                 "open",
			StoredState:            string(StateCreating),
			PendingCreateClaim:     claim,
			PendingCreateStartedAt: longAgo.Format(time.RFC3339),
			CreatedAt:              longAgo,
			StaleCreatingAfter:     time.Minute,
			Now:                    now,
			Runtime:                RuntimeFacts{Observed: false, Alive: false},
		}
	}

	withClaim := ProjectLifecycle(newInput(true))
	withoutClaim := ProjectLifecycle(newInput(false))

	t.Logf("claim=true  -> RuntimeProjection=%q ReconciledState=%q", withClaim.RuntimeProjection, withClaim.ReconciledState)
	t.Logf("claim=false -> RuntimeProjection=%q ReconciledState=%q", withoutClaim.RuntimeProjection, withoutClaim.ReconciledState)

	if withClaim.ReconciledState != withoutClaim.ReconciledState {
		t.Errorf("pending_create_claim changed the projection (%q vs %q) — the claim-gating "+
			"hypothesis is NOT disproven and needs its own investigation",
			withClaim.ReconciledState, withoutClaim.ReconciledState)
	}
}

// TestStaleCreatingAfterUnsetNeverGoesStale pins the second latent gap:
// creatingStateIsStale returns false whenever StaleCreatingAfter <= 0, and
// StaleCreatingAfter is assigned in exactly one place in the whole tree
// (cmd/gc/session_reconcile.go:889). Any caller that builds a LifecycleInput
// without setting it can never mark a creating bead stale, no matter its age.
func TestStaleCreatingAfterUnsetNeverGoesStale(t *testing.T) {
	now := time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)
	longAgo := now.Add(-12 * 24 * time.Hour)

	got := ProjectLifecycle(LifecycleInput{
		Status:                 "open",
		StoredState:            string(StateCreating),
		PendingCreateStartedAt: longAgo.Format(time.RFC3339),
		CreatedAt:              longAgo,
		// StaleCreatingAfter deliberately unset (zero).
		Now:     now,
		Runtime: RuntimeFacts{Observed: true, Alive: false},
	})

	t.Logf("StaleCreatingAfter=0, observed=true -> RuntimeProjection=%q ReconciledState=%q CountsAgainstCap=%v",
		got.RuntimeProjection, got.ReconciledState, got.CountsAgainstCap)

	if got.ReconciledState == StateCreating {
		t.Errorf("BUG ga-pofwv9 (second gap): with StaleCreatingAfter unset, a 12-day-old creating "+
			"bead projects ReconciledState=%q even though the runtime was observed dead",
			got.ReconciledState)
	}
}
