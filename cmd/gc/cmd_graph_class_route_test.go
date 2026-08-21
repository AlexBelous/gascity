package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/gastownhall/gascity/internal/beads"
	convoycore "github.com/gastownhall/gascity/internal/convoy"
)

// TestGraphResolvesRelocatedIDs pins that `gc graph` on a migrated city can graph
// a bead that lives in the infrastructure binding.
//
// `gc storage migrate` preserves ids, so a graph-class bead on a migrated city
// answers only from the binding. Reading the whole request from the work store
// reported it not found — for the exact ids an operator reaches for `gc graph`
// with, since a molecule root and its steps are graph-class.
func TestGraphResolvesRelocatedIDs(t *testing.T) {
	work := beads.NewMemStore()
	binding := &beads.MemStore{IDPrefix: "gcg"}

	root, err := binding.Create(beads.Bead{Title: "Formula: ship-it", Type: "molecule"})
	if err != nil {
		t.Fatalf("Create molecule root in the binding: %v", err)
	}
	step, err := binding.Create(beads.Bead{Title: "Step 1: implement", Type: "step", ParentID: root.ID})
	if err != nil {
		t.Fatalf("Create step in the binding: %v", err)
	}
	if err := binding.DepAdd(step.ID, root.ID, "blocks"); err != nil {
		t.Fatalf("DepAdd: %v", err)
	}

	var stdout, stderr bytes.Buffer
	code := doGraph(graphStoresOver(work, binding), []string{root.ID, step.ID}, graphOpts{}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("doGraph = %d, want 0; stderr: %s", code, stderr.String())
	}
	out := stdout.String()
	if !strings.Contains(out, "Formula: ship-it") || !strings.Contains(out, "Step 1: implement") {
		t.Errorf("graph of relocated beads did not render them; got:\n%s", out)
	}
	if !graphRowShows(out, step.ID, root.ID) {
		t.Errorf("row for %s should show %s as its blocker; got:\n%s", step.ID, root.ID, out)
	}
}

// TestGraphExpandsConvoyWithRelocatedMembers pins the failure mode that looks
// like an answer.
//
// A convoy is always a work bead, but its members can be graph-class. Expanding
// it against the work store alone still SUCCEEDS: convoy membership is an edge
// inventory, so every unresolvable member comes back as a placeholder carrying
// its id and nothing else. The command then lists the right ids with the right
// count, reads each member's edges from a store that does not hold it, and
// prints a dependency graph with no dependencies in it — which is why the
// assertion here is on the EDGES rather than on the member list.
func TestGraphExpandsConvoyWithRelocatedMembers(t *testing.T) {
	work := beads.NewMemStore()
	binding := &beads.MemStore{IDPrefix: "gcg"}

	convoy, err := work.Create(beads.Bead{Title: "convoy: release", Type: "convoy"})
	if err != nil {
		t.Fatalf("Create convoy in work: %v", err)
	}
	first, err := binding.Create(beads.Bead{Title: "Step 1: implement", Type: "step"})
	if err != nil {
		t.Fatalf("Create first step: %v", err)
	}
	second, err := binding.Create(beads.Bead{Title: "Step 2: verify", Type: "step"})
	if err != nil {
		t.Fatalf("Create second step: %v", err)
	}
	for _, member := range []string{first.ID, second.ID} {
		if err := work.DepAdd(convoy.ID, member, convoycore.TrackingDepType); err != nil {
			t.Fatalf("DepAdd tracks %s: %v", member, err)
		}
	}
	// The members' own edge lives in the binding alongside them.
	if err := binding.DepAdd(second.ID, first.ID, "blocks"); err != nil {
		t.Fatalf("DepAdd blocks: %v", err)
	}

	var stdout, stderr bytes.Buffer
	code := doGraph(graphStoresOver(work, binding), []string{convoy.ID}, graphOpts{}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("doGraph = %d, want 0; stderr: %s", code, stderr.String())
	}
	out := stdout.String()
	if !strings.Contains(out, "Step 2: verify") {
		t.Errorf("convoy member rendered as an unresolved placeholder (no title); got:\n%s", out)
	}
	if !graphRowShows(out, second.ID, first.ID) {
		t.Errorf("row for %s should show %s as its blocker; the members' edges were read from the work store, which does not hold them, so the graph printed no edges at all:\n%s", second.ID, first.ID, out)
	}
}

// graphRowShows reports whether the table row for id names want in its
// BLOCKED BY column. It matches on the row rather than the whole output because
// every id appears somewhere once the beads render at all — the question is
// whether the EDGE did.
func graphRowShows(out, id, want string) bool {
	for _, line := range strings.Split(out, "\n") {
		if !strings.HasPrefix(strings.TrimSpace(line), id+" ") {
			continue
		}
		return strings.Contains(line, want)
	}
	return false
}
