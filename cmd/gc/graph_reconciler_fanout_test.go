package main

import (
	"bytes"
	"testing"

	"github.com/gastownhall/gascity/internal/beads"
	sessionpkg "github.com/gastownhall/gascity/internal/session"
)

// TestReachableStoresIncludeGraphStore pins the critical win3 gap: the
// session-liveness fan-out must include the embedded graph store on a
// routed city, so a worker whose only assigned work is a claimed gcg step
// never reads as idle (which drained and closed live workers).
func TestReachableStoresIncludeGraphStore(t *testing.T) {
	work := beads.NewMemStore()
	cityPath := t.TempDir()
	writeGraphMigratedMarker(t, cityPath)
	cfg := sqliteGraphConfig()

	graph, err := graphClassStoreFor(cityPath)
	if err != nil {
		t.Fatal(err)
	}
	step, err := graph.Create(beads.Bead{Title: "claimed step", Type: "task"})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok, err := graph.Claim(step.ID, "gc-city-worker-1"); err != nil || !ok {
		t.Fatalf("claim: %v %v", ok, err)
	}

	info := sessionpkg.Info{ID: "gcs-test-1", SessionNameMetadata: "gc-city-worker-1"}
	stores, err := reachableStoresForSessionInfo(cityPath, cfg, work, nil, info)
	if err != nil {
		t.Fatalf("reachableStoresForSessionInfo: %v", err)
	}
	found := false
	for _, s := range stores {
		if s == beads.Store(graph) {
			found = true
		}
	}
	if !found {
		t.Fatalf("graph store missing from reachable fan-out (%d stores)", len(stores))
	}

	has, err := sessionHasOpenAssignedWorkForReachableStore(cityPath, cfg, work, nil, info)
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if !has {
		t.Fatal("graph-assigned step invisible to the liveness probe — the destructive-drain bug")
	}

	// Unrouted city: byte-identical fan-out, no error.
	plain, err := reachableStoresForSessionInfo(t.TempDir(), nil, work, nil, info)
	if err != nil || len(plain) != 1 || plain[0] != beads.Store(work) {
		t.Fatalf("unrouted fan-out = (%d stores, %v)", len(plain), err)
	}
}

// TestCollectAssignedWorkIncludesGraphLeg pins the reconciler snapshot: an
// in_progress graph step surfaces with its store aligned to the graph
// handle, so orphan release and stamp writers operate on the owning store.
func TestCollectAssignedWorkIncludesGraphLeg(t *testing.T) {
	work := beads.NewMemStore()
	cityPath := t.TempDir()
	writeGraphMigratedMarker(t, cityPath)
	graph, err := graphClassStoreFor(cityPath)
	if err != nil {
		t.Fatal(err)
	}
	step, err := graph.Create(beads.Bead{Title: "orphan step", Type: "task"})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok, err := graph.Claim(step.ID, "gc-city-dead-worker"); err != nil || !ok {
		t.Fatalf("claim: %v %v", ok, err)
	}

	workBeads, workStores, _, _, partial := collectAssignedWorkBeadsWithStores(nil, work, graph, nil, nil, nil)
	if partial {
		t.Fatal("partial snapshot")
	}
	found := false
	for i, b := range workBeads {
		if b.ID == step.ID {
			found = true
			if workStores[i] != beads.Store(graph) {
				t.Fatalf("graph bead aligned to %T, want the graph store", workStores[i])
			}
		}
	}
	if !found {
		t.Fatalf("graph in_progress step missing from assigned-work snapshot: %v", workBeads)
	}
}

// TestRetirementUnclaimReleasesGraphStep pins the release side: retiring a
// session unclaims its graph-resident step through the graph leg of the
// fan-out.
func TestRetirementUnclaimReleasesGraphStep(t *testing.T) {
	work := beads.NewMemStore()
	cityPath := t.TempDir()
	writeGraphMigratedMarker(t, cityPath)
	graph, err := graphClassStoreFor(cityPath)
	if err != nil {
		t.Fatal(err)
	}
	step, err := graph.Create(beads.Bead{Title: "held step", Type: "task"})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok, err := graph.Claim(step.ID, "gc-city-worker-9"); err != nil || !ok {
		t.Fatalf("claim: %v %v", ok, err)
	}

	sessionBead := beads.Bead{
		ID:       "gcs-sess-9",
		Type:     "session",
		Metadata: map[string]string{"session_name": "gc-city-worker-9"},
	}
	var stderr bytes.Buffer
	unclaimWorkAssignedToRetiredSessionBead(work, nil, graph, sessionBead, "", &stderr)

	got, err := graph.Get(step.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Assignee == "gc-city-worker-9" {
		t.Fatalf("retired session's graph step still assigned: %+v (stderr=%s)", got, stderr.String())
	}
}
