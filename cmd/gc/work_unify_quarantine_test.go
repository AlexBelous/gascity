package main

import (
	"encoding/json"
	"testing"

	"github.com/gastownhall/gascity/internal/beads"
)

func TestBeadIsTopologyQuarantined(t *testing.T) {
	quarantined := beads.Bead{ID: "gc-1", Labels: []string{"x", workTopologyMigratingLabel}}
	if !beadIsTopologyQuarantined(quarantined) {
		t.Fatalf("expected a %s-labeled bead to be quarantined", workTopologyMigratingLabel)
	}
	clean := beads.Bead{ID: "gc-2", Labels: []string{"x"}}
	if beadIsTopologyQuarantined(clean) {
		t.Fatalf("expected an unlabeled bead to flow")
	}
	if beadIsTopologyQuarantined(beads.Bead{ID: "gc-3"}) {
		t.Fatalf("expected a label-less bead to flow")
	}
}

func TestFilterTopologyQuarantinedDropsOnlyLabeled(t *testing.T) {
	rows := []beads.Bead{
		{ID: "a"},
		{ID: "b", Labels: []string{workTopologyMigratingLabel}},
		{ID: "c", Labels: []string{"other"}},
	}
	got := filterTopologyQuarantined(rows)
	if len(got) != 2 || got[0].ID != "a" || got[1].ID != "c" {
		t.Fatalf("expected a,c to survive quarantine filter, got %+v", got)
	}
}

// TestHookCandidateClaimableRejectsQuarantined pins that a quarantined bead is
// never freshly claimable while an otherwise-identical unlabeled bead is
// (deliverable G, claim-path arm).
func TestHookCandidateClaimableRejectsQuarantined(t *testing.T) {
	routes := []string{"rig-a"}
	labeled := beads.Bead{ID: "gc-1", Labels: []string{workTopologyMigratingLabel}, Metadata: map[string]string{"gc.routed_to": "rig-a"}}
	if hookCandidateClaimable(labeled, routes) {
		t.Fatalf("quarantined bead must not be claimable")
	}
	clean := beads.Bead{ID: "gc-2", Metadata: map[string]string{"gc.routed_to": "rig-a"}}
	if !hookCandidateClaimable(clean, routes) {
		t.Fatalf("unlabeled routed bead should be claimable")
	}
}

// TestFilterTopologyQuarantinedWorkQueryOutput pins the unconditional shell-output
// filter (F17/F21): a JSON array drops quarantined rows; a count form and
// non-array output pass through verbatim.
func TestFilterTopologyQuarantinedWorkQueryOutput(t *testing.T) {
	in := `[{"id":"a","status":"open"},{"id":"b","status":"open","labels":["` + workTopologyMigratingLabel + `"]}]`
	out := filterTopologyQuarantinedWorkQueryOutput(in)
	var rows []beads.Bead
	if err := json.Unmarshal([]byte(out), &rows); err != nil {
		t.Fatalf("filtered output not JSON: %v (%s)", err, out)
	}
	if len(rows) != 1 || rows[0].ID != "a" {
		t.Fatalf("expected only unlabeled 'a' to survive, got %+v", rows)
	}
	// Count form and no-quarantine array pass through byte-for-byte.
	if got := filterTopologyQuarantinedWorkQueryOutput("3"); got != "3" {
		t.Fatalf("count form should pass through, got %q", got)
	}
	clean := `[{"id":"x","status":"open"}]`
	if got := filterTopologyQuarantinedWorkQueryOutput(clean); got != clean {
		t.Fatalf("clean array should pass through verbatim, got %q", got)
	}
}

// TestMergeGraphReadyFiltersQuarantined pins the ready-federation arm: a
// quarantined work row present in the shell work-query output is dropped from
// the merged ready candidate list while an unlabeled peer survives.
func TestMergeGraphReadyFiltersQuarantined(t *testing.T) {
	st, err := graphClassStoreFor(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	shell := []beads.Bead{
		{ID: "w-open", Status: "open"},
		{ID: "w-migrating", Status: "open", Labels: []string{workTopologyMigratingLabel}},
	}
	shellJSON, err := json.Marshal(shell)
	if err != nil {
		t.Fatal(err)
	}
	out, err := mergeGraphReadyIntoWorkQueryOutput(string(shellJSON), st)
	if err != nil {
		t.Fatalf("merge: %v", err)
	}
	var got []beads.Bead
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("unmarshal merged output %q: %v", out, err)
	}
	ids := map[string]bool{}
	for _, b := range got {
		ids[b.ID] = true
	}
	if !ids["w-open"] {
		t.Fatalf("expected unlabeled row to survive, got %+v", got)
	}
	if ids["w-migrating"] {
		t.Fatalf("expected quarantined row to be filtered, got %+v", got)
	}
}
