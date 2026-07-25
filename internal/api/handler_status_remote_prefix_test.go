package api

import (
	"context"
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/beads"
)

// remotePrefixState wraps the fake state with the remote read-plane capability.
type remotePrefixState struct {
	*fakeState
	prefixes []string
}

func (s remotePrefixState) WorkReadPrefixes() ([]string, bool) {
	return s.prefixes, len(s.prefixes) > 0
}

// storeWithIDs builds a MemStore holding beads with EXPLICIT ids (bypassing
// MemStore's gc-N minting) so a test can seed distinct id prefixes — the work
// plane (gca-) vs the local graph plane's reserved prefix (gcg-).
func storeWithIDs(rows ...beads.Bead) beads.Store {
	now := time.Now()
	for i := range rows {
		if rows[i].Type == "" {
			rows[i].Type = "task"
		}
		if rows[i].Status == "" {
			rows[i].Status = "open"
		}
		if rows[i].CreatedAt.IsZero() {
			rows[i].CreatedAt = now
		}
	}
	return beads.NewMemStoreFrom(len(rows), rows, nil)
}

// orgDBWork is one handle onto the shared org work DB: one open + one
// in_progress work bead under the city's own prefix (gca-).
func orgDBWork() beads.Store {
	return storeWithIDs(
		beads.Bead{ID: "gca-1", Status: "open"},
		beads.Bead{ID: "gca-2", Status: "in_progress"},
	)
}

// TestStatusWorkCountsRemoteCollapsesAliasedRigLegs is the deliverable D/red-team
// pin: on a remote-target city every rig work scope aliases the shared org DB,
// so gc status must count each bead exactly once (via one prefix-scoped city
// leg), not once per aliased rig leg.
func TestStatusWorkCountsRemoteCollapsesAliasedRigLegs(t *testing.T) {
	state := remotePrefixState{fakeState: newFakeState(t), prefixes: []string{"gca"}}
	// City work store IS the org DB; two rig entries are DISTINCT handles onto
	// the same shared rows (the post-remote aliasing the aftermath addresses).
	state.cityBeadStore = orgDBWork()
	state.stores = map[string]beads.Store{
		"alpha": orgDBWork(),
		"beta":  orgDBWork(),
	}

	h := newTestCityHandlerReadOnly(t, state)
	resp := getStatusFrom(t, h, state.fakeState)

	if resp.Work.Open != 1 || resp.Work.InProgress != 1 || resp.Work.Ready != 1 {
		t.Fatalf("remote status counts = %+v, want open=1 in_progress=1 ready=1 (aliased rig legs collapsed, not summed)", resp.Work)
	}
	if resp.Partial {
		t.Fatalf("unexpected partial: %v", resp.PartialErrors)
	}
}

// TestStatusWorkCountsRemoteKeepsLocalGraphLegUnscoped is the blocker regression
// pin (findings 1/2/3): on a remote city the LOCAL graph store is a SEPARATE
// sqlite store, NOT the shared org DB, so its reserved-prefix (gcg-) graph and
// control beads must be counted — prefix scoping must be per-LEG (org work leg
// only), never per-state. It also confirms the aliased-rig collapse still holds.
func TestStatusWorkCountsRemoteKeepsLocalGraphLegUnscoped(t *testing.T) {
	state := remotePrefixState{fakeState: newFakeState(t), prefixes: []string{"gca"}}
	state.cityBeadStore = orgDBWork()
	state.stores = map[string]beads.Store{
		"alpha": orgDBWork(), // aliased rig leg — must collapse
		"beta":  orgDBWork(),
	}
	// Local graph store: an open molecule-root-ish bead + an in_progress step,
	// both under the reserved graph prefix gcg- (foreign to the city work prefix).
	state.graphBeadStore = storeWithIDs(
		beads.Bead{ID: "gcg-1", Status: "open"},        // open + ready
		beads.Bead{ID: "gcg-2", Status: "in_progress"}, // in_progress
	)

	h := newTestCityHandlerReadOnly(t, state)
	resp := getStatusFrom(t, h, state.fakeState)

	// Work leg contributes gca-1/gca-2 once (collapse); graph leg contributes
	// gcg-1/gcg-2 UNSCOPED. If the graph leg were prefix-scoped (the G28 bug),
	// the gcg- beads would be filtered to zero and every count would be 1.
	if resp.Work.Open != 2 || resp.Work.InProgress != 2 || resp.Work.Ready != 2 {
		t.Fatalf("remote status counts = %+v, want open=2 in_progress=2 ready=2 "+
			"(gca- work leg once + gcg- graph leg counted UNSCOPED)", resp.Work)
	}
	if resp.Partial {
		t.Fatalf("unexpected partial: %v", resp.PartialErrors)
	}
}

// prefixScopedStore is a shared org DB: List honors the query (IDPrefixes
// included) and Count would return an org-wide (wrong) total. countCalls records
// whether the Counter fast path was taken.
type prefixScopedStore struct {
	beads.Store
	rows       []beads.Bead
	countCalls *int
}

func (s *prefixScopedStore) List(q beads.ListQuery) ([]beads.Bead, error) {
	return beads.ApplyListQuery(s.rows, q), nil
}

func (s *prefixScopedStore) Count(_ context.Context, _ beads.ListQuery, _ ...string) (int, error) {
	*s.countCalls++
	return 9999, nil
}

// TestStatusStoredWorkCountsScopedLeg pins deliverable D's per-leg contract: a
// scoped leg bypasses the Counter fast path and constrains to the given
// prefixes; an unscoped leg (the DARK/graph case) keeps the Counter fast path.
func TestStatusStoredWorkCountsScopedLeg(t *testing.T) {
	rows := []beads.Bead{
		{ID: "gca-1", Type: "task", Status: "open"},
		{ID: "gca-2", Type: "task", Status: "in_progress"},
		{ID: "gcb-9", Type: "task", Status: "open"}, // foreign city — must not count
	}

	t.Run("scoped leg bypasses Counter and scopes to the given prefixes", func(t *testing.T) {
		countCalls := 0
		store := &prefixScopedStore{rows: rows, countCalls: &countCalls}
		wc, err := statusStoredWorkCounts(context.Background(), newFakeState(t), store, true, []string{"gca"})
		if err != nil {
			t.Fatalf("statusStoredWorkCounts: %v", err)
		}
		if countCalls != 0 {
			t.Fatalf("Counter fast path must be bypassed on a scoped leg, got %d Count calls", countCalls)
		}
		if wc.Open != 1 || wc.InProgress != 1 {
			t.Fatalf("scoped counts = %+v, want open=1 in_progress=1 (gcb-9 excluded)", wc)
		}
	})

	t.Run("unscoped leg uses the Counter fast path (DARK / graph leg)", func(t *testing.T) {
		countCalls := 0
		store := &prefixScopedStore{rows: rows, countCalls: &countCalls}
		wc, err := statusStoredWorkCounts(context.Background(), newFakeState(t), store, false, nil)
		if err != nil {
			t.Fatalf("statusStoredWorkCounts: %v", err)
		}
		if countCalls != 2 { // one Count per stored bucket (open, in_progress)
			t.Fatalf("unscoped leg must use the Counter fast path, got %d Count calls", countCalls)
		}
		if wc.Open != 9999 || wc.InProgress != 9999 {
			t.Fatalf("unscoped counts must come from the Counter, got %+v", wc)
		}
	})
}
