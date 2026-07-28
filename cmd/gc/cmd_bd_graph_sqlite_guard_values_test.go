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
		{"list mentioning gcg stays refused", []string{"list", "--metadata-field", "gc.root_bead_id=gcg-5"}, false},
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
