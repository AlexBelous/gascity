package main

import (
	"context"
	"testing"

	"github.com/gastownhall/gascity/internal/beads"
)

// TestGraphRoutedHookClaimOps pins the claim-time routing: on a routed city,
// claim/stamp/continuation mutations on gcg ids land in the embedded graph
// store in-process; a second claimant loses cleanly (CAS), and continuations
// under a gcg root list from the graph store.
func TestGraphRoutedHookClaimOps(t *testing.T) {
	cityPath := t.TempDir()
	writeGraphMigratedMarker(t, cityPath)
	cfg := sqliteGraphConfig()
	st, err := graphClassStoreFor(cityPath)
	if err != nil {
		t.Fatal(err)
	}
	step, err := st.Create(beads.Bead{Title: "step", Type: "task"})
	if err != nil {
		t.Fatal(err)
	}

	ops := graphRoutedHookClaimOps(cityPath, cfg)
	ctx := context.Background()

	claimed, ok, err := ops.Claim(ctx, t.TempDir(), nil, step.ID, "worker-1")
	if err != nil || !ok || claimed.Assignee != "worker-1" {
		t.Fatalf("claim = (%+v, %v, %v)", claimed, ok, err)
	}
	if _, ok, err := ops.Claim(ctx, t.TempDir(), nil, step.ID, "worker-2"); err != nil || ok {
		t.Fatalf("second claim = (%v, %v), want lost-not-error", ok, err)
	}

	if err := ops.StampWorkMeta(ctx, t.TempDir(), nil, step.ID, "worker-1", map[string]string{"gc.work_branch": "b1"}); err != nil {
		t.Fatalf("stamp: %v", err)
	}
	got, err := st.Get(step.ID)
	if err != nil || got.Metadata["gc.work_branch"] != "b1" {
		t.Fatalf("stamp did not land: %+v %v", got, err)
	}

	// Continuations under a gcg root list from the graph store.
	root, err := st.Create(beads.Bead{Title: "root", Type: "molecule"})
	if err != nil {
		t.Fatal(err)
	}
	child, err := st.Create(beads.Bead{Title: "cont", Type: "task", ParentID: root.ID, Labels: []string{"grp"}})
	if err != nil {
		t.Fatal(err)
	}
	conts, err := ops.ListContinuation(ctx, t.TempDir(), nil, root.ID, "grp")
	if err != nil || len(conts) != 1 || conts[0].ID != child.ID {
		t.Fatalf("continuations = (%+v, %v)", conts, err)
	}
	if err := ops.AssignContinuation(ctx, t.TempDir(), nil, child.ID, "worker-1"); err != nil {
		t.Fatalf("assign continuation: %v", err)
	}
	if got, _ := st.Get(child.ID); got.Assignee != "worker-1" {
		t.Fatalf("assign did not land: %+v", got)
	}
}
