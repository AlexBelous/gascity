package beads

import "testing"

// TestListQueryIDPrefixesMatch pins the remote read-plane prefix filter on the
// universal Matches/ApplyListQuery path every store routes through (BdStore.List
// and NativeDoltStore.List both fold results through ApplyListQuery). Empty
// IDPrefixes is the DARK default: no constraint.
func TestListQueryIDPrefixesMatch(t *testing.T) {
	items := []Bead{
		{ID: "gca-1", Title: "a1"},
		{ID: "gca-2", Title: "a2"},
		{ID: "gcb-9", Title: "b9"},
		{ID: "hq-3", Title: "hq3"},
		{ID: "GCA-4", Title: "upper"}, // case-insensitive match
	}

	t.Run("empty is dark", func(t *testing.T) {
		got := ApplyListQuery(items, ListQuery{AllowScan: true})
		if len(got) != len(items) {
			t.Fatalf("empty IDPrefixes must not filter: got %d want %d", len(got), len(items))
		}
	})

	t.Run("single prefix", func(t *testing.T) {
		got := ApplyListQuery(items, ListQuery{IDPrefixes: []string{"gca"}})
		gotIDs := map[string]bool{}
		for _, b := range got {
			gotIDs[b.ID] = true
		}
		for _, want := range []string{"gca-1", "gca-2", "GCA-4"} {
			if !gotIDs[want] {
				t.Errorf("expected %s in results", want)
			}
		}
		if gotIDs["gcb-9"] || gotIDs["hq-3"] {
			t.Errorf("foreign-prefix rows leaked: %v", gotIDs)
		}
	})

	t.Run("OR across the city prefix set", func(t *testing.T) {
		got := ApplyListQuery(items, ListQuery{IDPrefixes: []string{"gca", "hq"}})
		if len(got) != 4 { // gca-1, gca-2, GCA-4, hq-3
			t.Fatalf("prefix union got %d want 4: %v", len(got), got)
		}
	})

	t.Run("normalization tolerates dash and case", func(t *testing.T) {
		got := ApplyListQuery(items, ListQuery{IDPrefixes: []string{"GCB-"}})
		if len(got) != 1 || got[0].ID != "gcb-9" {
			t.Fatalf("normalized prefix mismatch: %v", got)
		}
	})

	t.Run("blank prefix never widens", func(t *testing.T) {
		// A misconfigured empty entry must match nothing, not everything.
		got := ApplyListQuery(items, ListQuery{IDPrefixes: []string{"  "}})
		if len(got) != 0 {
			t.Fatalf("blank prefix must match nothing, got %d", len(got))
		}
	})

	t.Run("LIKE metacharacter prefixes never over-match", func(t *testing.T) {
		// A prefix carrying a SQL LIKE metacharacter (_ % \) is not a valid bd
		// issue prefix; it must be DROPPED (match nothing) so the Go matcher and
		// the SQL `id LIKE` predicate agree by construction — never letting "%"
		// widen to everything or "_" match any single char.
		meta := []Bead{
			{ID: "gca-1"},
			{ID: "gcax-1"},
			{ID: "gc_-1"},
		}
		for _, bad := range []string{"%", "gc_", `gc\`, "g%a", "gc-a"} {
			got := ApplyListQuery(meta, ListQuery{IDPrefixes: []string{bad}})
			if len(got) != 0 {
				t.Fatalf("metacharacter prefix %q must match nothing, got %v", bad, got)
			}
		}
		// The clean sibling still matches exactly, proving the gate is not
		// over-broad.
		got := ApplyListQuery(meta, ListQuery{IDPrefixes: []string{"gca"}})
		if len(got) != 1 || got[0].ID != "gca-1" {
			t.Fatalf("clean prefix must still match literally, got %v", got)
		}
	})
}

func TestNormalizeIDFilterPrefix(t *testing.T) {
	cases := []struct {
		in   string
		want string
		ok   bool
	}{
		{"gca", "gca", true},
		{"GCA-", "gca", true}, // lowercased, separator stripped
		{"  hq ", "hq", true},
		{"", "", false},
		{"   ", "", false},
		{"gc_", "", false},  // underscore is a LIKE metacharacter
		{"gc%", "", false},  // percent is a LIKE metacharacter
		{`gc\`, "", false},  // backslash is a LIKE escape
		{"gc-a", "", false}, // internal separator is not a valid prefix
	}
	for _, c := range cases {
		got, ok := NormalizeIDFilterPrefix(c.in)
		if got != c.want || ok != c.ok {
			t.Errorf("NormalizeIDFilterPrefix(%q) = (%q, %v), want (%q, %v)", c.in, got, ok, c.want, c.ok)
		}
	}
}

// TestListQueryIDPrefixesHasFilter pins that a prefix-only query is a real
// filter (so stores don't reject it as a bare scan) — the remote count/list
// surfaces issue prefix-only queries.
func TestListQueryIDPrefixesHasFilter(t *testing.T) {
	if !(ListQuery{IDPrefixes: []string{"gca"}}).HasFilter() {
		t.Fatal("IDPrefixes must count as a filter")
	}
	if (ListQuery{}).HasFilter() {
		t.Fatal("empty query must not report a filter")
	}
}
