package main

import (
	"testing"

	"github.com/gastownhall/gascity/internal/beadmeta"
	"github.com/gastownhall/gascity/internal/beads"
)

// TestResolveActiveWispStepSpansTheClassBoundary is the split-store shape of
// the attached (v1) formula: the source work bead is in the WORK ledger (city
// or rig), and the molecule root plus its step children are in the GRAPH
// binding. Every existing test in this file passes the same store twice, which
// is the single-store city — so none of them can tell whether the resolution
// respects the class boundary or merely happens to find everything in one
// place.
//
// Two ways to get this wrong, and this pins both. Resolve the whole thing on
// the work store and the molecule_id bridge dead-ends: the root is not there,
// the bridge returns nil, and the injection silently falls through to the
// legacy description path and shows the agent its SOURCE bead instead of its
// current step. Resolve the whole thing on the graph store and the bridge never
// starts: the assignee-owned source bead is not there either, so it returns
// nothing at all.
func TestResolveActiveWispStepSpansTheClassBoundary(t *testing.T) {
	// Disjoint ID spaces on purpose. Two fresh MemStores both mint gc-1, so a
	// cross-store Get would collide and the wrong-store read would silently
	// succeed — the test would pass with the routing broken. Seeding the graph
	// store's counter past the work store's is what makes a wrong-store read miss.
	work := beads.NewMemStore()
	graph := beads.NewMemStoreFrom(100, nil, nil)

	// Graph class: the root (unrouted, unassigned — attached formulas leave it
	// that way) and its in-progress step child.
	root := mustCreateInProgress(t, graph, beads.Bead{
		Title: "Formula: mol-attached-work",
		Type:  "molecule",
	})
	step := mustCreateInProgress(t, graph, beads.Bead{
		Title:       "Step 1: attached implement",
		Description: "Write the attached implementation",
		Type:        "step",
		Assignee:    "alice",
		ParentID:    root.ID,
	})

	// Work class: the only agent-assigned in-progress bead, carrying the bridge
	// stamp. It has a description, so a resolution that loses the bridge returns
	// THIS instead of the step — a wrong answer that looks like a right one.
	source := mustCreateInProgress(t, work, beads.Bead{
		Title:       "Source work bead",
		Description: "Do the attached work",
		Type:        "task",
		Assignee:    "alice",
	})
	if err := work.SetMetadata(source.ID, beadmeta.MoleculeIDMetadataKey, root.ID); err != nil {
		t.Fatalf("SetMetadata(molecule_id): %v", err)
	}

	b, err := resolveActiveWispStep(work, graph, []string{"alice"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if b == nil {
		t.Fatal("resolution returned nil across the class boundary; the molecule_id bridge did not cross from the work store to the graph store")
	}
	if b.ID == source.ID {
		t.Fatalf("resolution returned the source work bead %q; the bridge was lost and the legacy description fallback answered instead of the current step %q", source.ID, step.ID)
	}
	if b.ID != step.ID {
		t.Errorf("got bead ID %q (type %q), want the bridged step %q", b.ID, b.Type, step.ID)
	}
}

// TestResolveActiveWispStepKeepsTheLegacyFallbackOnWork pins the other side of
// the split: with no molecule anywhere, the legacy in-progress-with-description
// answer must still come from the WORK store. Routing this file wholesale to
// the graph class — the obvious one-line "fix" — would return nil here.
func TestResolveActiveWispStepKeepsTheLegacyFallbackOnWork(t *testing.T) {
	// Disjoint ID spaces on purpose. Two fresh MemStores both mint gc-1, so a
	// cross-store Get would collide and the wrong-store read would silently
	// succeed — the test would pass with the routing broken. Seeding the graph
	// store's counter past the work store's is what makes a wrong-store read miss.
	work := beads.NewMemStore()
	graph := beads.NewMemStoreFrom(100, nil, nil)

	legacy := mustCreateInProgress(t, work, beads.Bead{
		Title:       "Plain work bead",
		Description: "No formula here",
		Type:        "task",
		Assignee:    "alice",
	})

	b, err := resolveActiveWispStep(work, graph, []string{"alice"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if b == nil {
		t.Fatal("the legacy fallback returned nil; it must read the work store, which is where an agent's plain in-progress bead lives")
	}
	if b.ID != legacy.ID {
		t.Errorf("got bead ID %q, want the work-store bead %q", b.ID, legacy.ID)
	}
}
