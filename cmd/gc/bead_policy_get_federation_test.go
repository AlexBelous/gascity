package main

import (
	"testing"

	"github.com/gastownhall/gascity/internal/beads"
)

// TestBeadPolicyGetFederatesGraphReads is the follow-up to the ref-by-id cross-store
// tracks fix: beadPolicyStore.Create routes a ClassGraph bead to the routed graph
// store (createTarget), so Get must read it back from there too. Before the Get
// override, a graph-routed bead was write-only through the policy wrapper —
// TrackingConvoysForItem / hasLiveTrackingConvoy resolving a convoy by id through
// the store handle alone silently missed it.
func TestBeadPolicyGetFederatesGraphReads(t *testing.T) {
	cityPath := t.TempDir()
	writeGraphMigratedMarker(t, cityPath)

	work := beads.NewMemStore()
	store := wrapStoreWithBeadPoliciesAt(work, sqliteGraphConfig(), cityPath)

	// A graph-class bead (wisp label) routes to the graph store via the create chokepoint.
	gb, err := store.Create(beads.Bead{Title: "graph bead", Type: "task", Labels: []string{"gc:wisp"}})
	if err != nil {
		t.Fatalf("create graph bead: %v", err)
	}
	// It must physically live on the routed graph store, not the work store.
	graph, routed, err := routedGraphStoreFor(cityPath, sqliteGraphConfig())
	if err != nil || !routed {
		t.Fatalf("routedGraphStoreFor = (routed=%v, err=%v), want routed", routed, err)
	}
	if _, err := graph.Get(gb.ID); err != nil {
		t.Fatalf("graph bead %s not on the graph store: %v", gb.ID, err)
	}
	if _, err := work.Get(gb.ID); err == nil {
		t.Fatalf("graph bead %s must NOT be on the work store", gb.ID)
	}
	// The fix: reading it back through the policy store handle federates to graph.
	got, err := store.Get(gb.ID)
	if err != nil {
		t.Fatalf("policy-store Get(%s) must federate to the graph store, got: %v", gb.ID, err)
	}
	if got.ID != gb.ID {
		t.Fatalf("Get returned %s, want %s", got.ID, gb.ID)
	}
	// A work-class bead still resolves through the same handle (byte-identical path).
	wb, err := store.Create(beads.Bead{Title: "work bead", Type: "task"})
	if err != nil {
		t.Fatalf("create work bead: %v", err)
	}
	if _, err := store.Get(wb.ID); err != nil {
		t.Fatalf("policy-store Get(%s) work bead: %v", wb.ID, err)
	}
}

// TestBeadPolicyGetUnroutedBypass pins the byte-identical default: with no
// cityPath the Get override collapses to the embedded store read.
func TestBeadPolicyGetUnroutedBypass(t *testing.T) {
	work := beads.NewMemStore()
	store := wrapStoreWithBeadPolicies(work, sqliteGraphConfig())
	b, err := store.Create(beads.Bead{Title: "plain bead", Type: "task"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := store.Get(b.ID); err != nil {
		t.Fatalf("unrouted Get: %v", err)
	}
}
