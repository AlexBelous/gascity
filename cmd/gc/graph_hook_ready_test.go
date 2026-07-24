package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/gastownhall/gascity/internal/beads"
)

// TestMergeGraphReadyIntoWorkQueryOutput pins the union semantics: shell
// JSON rows keep precedence, graph ready rows append deduped, count-form
// output passes through untouched.
func TestMergeGraphReadyIntoWorkQueryOutput(t *testing.T) {
	cityPath := t.TempDir()
	st, err := graphClassStoreFor(cityPath)
	if err != nil {
		t.Fatal(err)
	}
	g, err := st.Create(beads.Bead{Title: "graph step", Type: "task"})
	if err != nil {
		t.Fatal(err)
	}

	// Union with a work row.
	merged, err := mergeGraphReadyIntoWorkQueryOutput(`[{"id":"gc-w1","title":"work","status":"open","issue_type":"task"}]`, st)
	if err != nil {
		t.Fatal(err)
	}
	var rows []beads.Bead
	if err := json.Unmarshal([]byte(merged), &rows); err != nil {
		t.Fatalf("merged output not JSON: %v (%s)", err, merged)
	}
	ids := map[string]bool{}
	for _, b := range rows {
		ids[b.ID] = true
	}
	if !ids["gc-w1"] || !ids[g.ID] {
		t.Fatalf("merged rows %v missing union", ids)
	}

	// Empty shell output still surfaces graph work.
	merged, err = mergeGraphReadyIntoWorkQueryOutput("", st)
	if err != nil || !strings.Contains(merged, g.ID) {
		t.Fatalf("empty-shell merge = (%q, %v)", merged, err)
	}

	// Count form passes through untouched.
	merged, err = mergeGraphReadyIntoWorkQueryOutput("3", st)
	if err != nil || merged != "3" {
		t.Fatalf("count form = (%q, %v), want untouched", merged, err)
	}
}

// TestMergeGraphAssignedInProgress pins the crash-recovery tier: a worker's
// own assigned in_progress graph step (wisp tier) joins the candidate list
// so a respawned worker re-adopts it; other workers' steps do not.
func TestMergeGraphAssignedInProgress(t *testing.T) {
	cityPath := t.TempDir()
	st, err := graphClassStoreFor(cityPath)
	if err != nil {
		t.Fatal(err)
	}
	mine, err := st.Create(beads.Bead{Title: "my step", Type: "task", Ephemeral: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok, err := st.Claim(mine.ID, "gc-city-w1"); err != nil || !ok {
		t.Fatalf("claim: %v %v", ok, err)
	}
	theirs, err := st.Create(beads.Bead{Title: "their step", Type: "task"})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok, err := st.Claim(theirs.ID, "gc-city-w2"); err != nil || !ok {
		t.Fatalf("claim: %v %v", ok, err)
	}

	env := []string{"GC_SESSION_NAME=gc-city-w1", "PATH=/usr/bin"}
	merged, err := mergeGraphAssignedInProgressIntoWorkQueryOutput("", st, env)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(merged, mine.ID) {
		t.Fatalf("own in_progress step missing from merge: %s", merged)
	}
	if strings.Contains(merged, theirs.ID) {
		t.Fatalf("another worker's step leaked into the merge: %s", merged)
	}
	// Count-form output passes through untouched.
	if out, err := mergeGraphAssignedInProgressIntoWorkQueryOutput("2", st, env); err != nil || out != "2" {
		t.Fatalf("count form = (%q, %v)", out, err)
	}
}
