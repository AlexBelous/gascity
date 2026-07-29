package main

import "testing"

// TestBdWriteMentionsGraphOnlyInValues pins the guard escape for write verbs
// whose graph-prefix mention sits in value position (the pr-review router
// stamps pr_review.workflow_root_id=gcg-… onto a work-store source bead).
func TestBdWriteMentionsGraphOnlyInValues(t *testing.T) {
	const prefix = "gcg"
	cases := []struct {
		name string
		args []string
		want bool
	}{
		{"update work target, gcg in metadata value", []string{"update", "mc-yttw", "--set-metadata", "pr_review.workflow_root_id=gcg-98636585"}, true},
		{"update graph target", []string{"update", "gcg-42", "--set-metadata", "k=v"}, false},
		{"create with gcg in description", []string{"create", "title", "--description", "tracks gcg-7"}, true},
		{"close work targets, gcg value", []string{"close", "mc-1", "--reason", "superseded by gcg-9"}, true},
		{"close graph target", []string{"close", "gcg-9"}, false},
		// Deliberate pin flip (was: refused): value-position list filters
		// follow the same principle as write values — the addressed store
		// answers for its own rows. Graph-targeted list forms (--parent
		// gcg-…, positional ids) stay refused below.
		{"list with gcg metadata value passes", []string{"list", "--metadata-field", "gc.root_bead_id=gcg-5"}, true},
		{"list naming a graph parent stays refused", []string{"list", "--parent", "gcg-9"}, false},
		{"empty args", nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := bdWriteMentionsGraphOnlyInValues(tc.args, prefix); got != tc.want {
				t.Fatalf("bdWriteMentionsGraphOnlyInValues(%v) = %v, want %v", tc.args, got, tc.want)
			}
		})
	}
}

// TestBdListWithGraphValueMentionPassesThrough pins the read-side escape: a
// bd list whose ONLY graph mention is a metadata-filter value (the
// pr-merge-queue leftover-member probe) must pass through to bd rather than
// being refused — the rig/work store legitimately answers for its own rows.
func TestBdListWithGraphValueMentionPassesThrough(t *testing.T) {
	args := []string{"list", "--all", "--metadata-field", "workflow_id=gcg-98635475", "--limit", "0", "--json"}
	if !bdWriteMentionsGraphOnlyInValues(args, "gcg") {
		t.Fatal("list with value-position graph mention must pass through")
	}
}
