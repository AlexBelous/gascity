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
