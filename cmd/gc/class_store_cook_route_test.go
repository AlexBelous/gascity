package main

import (
	"context"
	"testing"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/molecule"
)

// TestCookOnClassRoutedPoursByRecipeClass pins the decision the convenience
// wrapper cannot make: molecule.Cook picks its store before anything has
// compiled the formula, so a caller with one store open pours graph-class
// workflows into the work ledger. Both arms matter and they fail differently.
// A vapor formula left on the work store is invisible to every graph reader —
// the repair bead is created, routed and reported dispatched with steps nobody
// can find. A poured v1 formula pushed into the graph binding hides its steps
// from `gc hook`, which only reads the work ledger.
//
// The parent bead deliberately lives in NEITHER store. Instantiate stamps
// ParentID onto the beads it creates and never reads the parent back, and the
// production shape is exactly that cross-store link: a work-class repair bead in
// a rig ledger owning a city-keyed graph workflow.
func TestCookOnClassRoutedPoursByRecipeClass(t *testing.T) {
	dir := convergenceResidenceFormulaDir(t)

	for _, tt := range []struct {
		name    string
		formula string
		// wantGraph is whether the molecule must land in the graph store.
		wantGraph bool
	}{
		{"vapor formula is graph class", convergenceVaporFormula, true},
		{"poured v1 formula is work class", convergencePouredFormula, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			// Disjoint ID spaces on purpose. Two fresh MemStores both mint gc-1,
			// so counting rows in the store that should be empty is the only
			// oracle that cannot be satisfied by a collision.
			work := beads.NewMemStore()
			graph := beads.NewMemStoreFrom(1000, nil, nil)

			res, err := cookOnClassRouted(context.Background(), work, graph, tt.formula, []string{dir}, molecule.Options{
				ParentID: "gc-parent-lives-in-a-third-store",
			})
			if err != nil {
				t.Fatalf("cookOnClassRouted: %v", err)
			}
			if res == nil || res.Created == 0 {
				t.Fatalf("cook created no beads; the residence assertions below would be vacuous")
			}

			gotWork, gotGraph := countBeads(t, work), countBeads(t, graph)
			wantStore, otherStore := "graph", "work"
			wantCount, otherCount := gotGraph, gotWork
			if !tt.wantGraph {
				wantStore, otherStore = otherStore, wantStore
				wantCount, otherCount = otherCount, wantCount
			}
			if wantCount != res.Created {
				t.Errorf("the %s store holds %d of the %d beads the cook created; the molecule did not land in its class store", wantStore, wantCount, res.Created)
			}
			if otherCount != 0 {
				t.Errorf("the %s store holds %d beads; the cook leaked across the class boundary", otherStore, otherCount)
			}
		})
	}
}

// TestCookOnClassRoutedRequiresAParent keeps the attach-only contract that
// molecule.CookOn enforced. Dropping it would turn a missing ParentID into a
// detached molecule that no autoclose path can ever reap.
func TestCookOnClassRoutedRequiresAParent(t *testing.T) {
	dir := convergenceResidenceFormulaDir(t)
	_, err := cookOnClassRouted(context.Background(), beads.NewMemStore(), beads.NewMemStore(), convergencePouredFormula, []string{dir}, molecule.Options{})
	if err == nil {
		t.Fatal("cooking with no ParentID succeeded; the attach-only contract is gone")
	}
}
