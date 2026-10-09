package beads

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestSQLiteActiveSessionHintSelectorContract(t *testing.T) {
	base := ListQuery{Type: "session", TierMode: TierBoth}
	tests := []struct {
		name     string
		change   func(*ListQuery)
		hasIndex bool
		hint     bool
	}{
		{"default-active", func(*ListQuery) {}, true, true},
		{"absent-index", func(*ListQuery) {}, false, false},
		{"other-type", func(q *ListQuery) { q.Type = "task" }, true, false},
		{"main-tier", func(q *ListQuery) { q.TierMode = TierIssues }, true, false},
		{"wisp-tier", func(q *ListQuery) { q.TierMode = TierWisps }, true, false},
		{"explicit-status", func(q *ListQuery) { q.Status = "open" }, true, false},
		{"include-closed", func(q *ListQuery) { q.IncludeClosed = true }, true, false},
		{"metadata", func(q *ListQuery) { q.Metadata = map[string]string{"session_name": "fixture"} }, true, false},
		{"ids", func(q *ListQuery) { q.IDs = []string{"gcg-fixture"} }, true, false},
		{"parent", func(q *ListQuery) { q.ParentID = "gcg-parent" }, true, false},
		{"parents", func(q *ListQuery) { q.ParentIDs = []string{"gcg-parent"} }, true, false},
		{"assignee", func(q *ListQuery) { q.Assignee = "fixture" }, true, false},
		{"assignees", func(q *ListQuery) { q.Assignees = []string{"fixture"} }, true, false},
		{"label", func(q *ListQuery) { q.Label = "fixture" }, true, false},
		{"created-before", func(q *ListQuery) { q.CreatedBefore = time.Unix(1, 0) }, true, false},
		{"updated-before", func(q *ListQuery) { q.UpdatedBefore = time.Unix(1, 0) }, true, false},
		{"seek", func(q *ListQuery) { q.SeekAfter = new(SeekBoundary); q.Sort = SortCreatedAsc }, true, false},
		{"limit", func(q *ListQuery) { q.Limit = 1 }, true, false},
		{"sort", func(q *ListQuery) { q.Sort = SortCreatedAsc }, true, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			q := base
			tc.change(&q)
			want, args := sqliteListSQL(q, "b.bead_json")
			got, gotArgs := sqliteActiveSessionListSQL(q, "b.bead_json", tc.hasIndex)
			if strings.Contains(got, "INDEXED BY idx_beads_active_type") != tc.hint {
				t.Fatalf("active index hint presence does not match contract: %q", got)
			}
			if tc.hint {
				want = strings.Replace(want, " FROM beads b", " FROM beads b INDEXED BY idx_beads_active_type", 1)
			}
			if got != want || !reflect.DeepEqual(gotArgs, args) {
				t.Fatalf("SQL/args changed outside contract: got %q %#v; want %q %#v", got, gotArgs, want, args)
			}
		})
	}
}

func TestSQLiteActiveSessionHintRealStore(t *testing.T) {
	for _, indexed := range []bool{false, true} {
		t.Run(map[bool]string{false: "baseline", true: "indexed"}[indexed], func(t *testing.T) {
			dir := t.TempDir()
			var mutate func([]string) []string
			if indexed {
				mutate = func(sql []string) []string { return append(sql, sqliteSchemaActiveTypeIndex) }
			}
			createSQLiteSchemaFixture(t, dir, true, false, mutate)
			opened, err := OpenSQLiteStore(dir, WithSQLiteStoreIDPrefix(sqliteGraphPrefix), WithSQLiteStoreRetention(0, 0))
			if err != nil {
				t.Fatal(err)
			}
			store := opened.(*SQLiteStore)
			t.Cleanup(func() {
				if err := store.CloseStore(); err != nil {
					t.Error(err)
				}
			})
			if store.hasActiveTypeIndex != indexed {
				t.Fatal("admitted layout flag mismatch")
			}
			for _, item := range []Bead{{ID: "gcg-live-fixture", Title: "fixture", Type: "session", Status: "open"}, {ID: "gcg-closed-fixture", Title: "fixture", Type: "session", Status: "closed"}} {
				if _, err := store.Create(item); err != nil {
					t.Fatal(err)
				}
			}
			expected, err := store.Get("gcg-live-fixture")
			if err != nil {
				t.Fatal(err)
			}
			got, err := store.List(ListQuery{Type: "session", TierMode: TierBoth})
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != 1 || !reflect.DeepEqual(got[0], expected) {
				t.Fatalf("actual full row differs: %#v / %#v", got, expected)
			}
		})
	}
}

func TestSQLiteActiveSessionHintRequiresReopenAfterDDL(t *testing.T) {
	dir := t.TempDir()
	createSQLiteSchemaFixture(t, dir, true, false, nil)
	opened, err := OpenSQLiteStore(dir, WithSQLiteStoreIDPrefix(sqliteGraphPrefix), WithSQLiteStoreRetention(0, 0))
	if err != nil {
		t.Fatal(err)
	}
	store := opened.(*SQLiteStore)
	t.Cleanup(func() { _ = store.CloseStore() })
	if store.hasActiveTypeIndex {
		t.Fatal("index unexpectedly present")
	}
	if _, err := store.Create(Bead{ID: "gcg-reopen-fixture", Title: "fixture", Type: "session", Status: "open"}); err != nil {
		t.Fatal(err)
	}
	before, err := store.List(ListQuery{Type: "session", TierMode: TierBoth})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(sqliteSchemaActiveTypeIndex); err != nil {
		t.Fatal(err)
	}
	if store.hasActiveTypeIndex {
		t.Fatal("cached flag changed without reopen")
	}
	after, err := store.List(ListQuery{Type: "session", TierMode: TierBoth})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatal("DDL changed existing handle's rows")
	}
	if err := store.CloseStore(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenSQLiteStore(dir, WithSQLiteStoreIDPrefix(sqliteGraphPrefix), WithSQLiteStoreReadOnly())
	if err != nil {
		t.Fatal(err)
	}
	next := reopened.(*SQLiteStore)
	t.Cleanup(func() { _ = next.CloseStore() })
	if !next.hasActiveTypeIndex {
		t.Fatal("reopen failed to observe admitted index")
	}
	final, err := next.List(ListQuery{Type: "session", TierMode: TierBoth})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, final) {
		t.Fatal("reopen changed full rows")
	}
}

func TestSQLiteActiveSessionHintDropIndexRequiresReopen(t *testing.T) {
	dir := t.TempDir()
	createSQLiteSchemaFixture(t, dir, true, false, func(sql []string) []string { return append(sql, sqliteSchemaActiveTypeIndex) })
	opened, err := OpenSQLiteStore(dir, WithSQLiteStoreIDPrefix(sqliteGraphPrefix), WithSQLiteStoreRetention(0, 0))
	if err != nil {
		t.Fatal(err)
	}
	store := opened.(*SQLiteStore)
	defer store.CloseStore()
	if _, err := store.Create(Bead{ID: "gcg-drop-fixture", Title: "fixture", Type: "session", Status: "open"}); err != nil {
		t.Fatal(err)
	}
	query := ListQuery{Type: "session", TierMode: TierBoth}
	before, err := store.List(query)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec("DROP INDEX idx_beads_active_type"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.List(query); err == nil || !strings.Contains(err.Error(), "no such index") {
		t.Fatalf("expected stale hinted handle failure; got %v", err)
	}
	if err := store.CloseStore(); err != nil {
		t.Fatal(err)
	}
	nextOpened, err := OpenSQLiteStore(dir, WithSQLiteStoreIDPrefix(sqliteGraphPrefix), WithSQLiteStoreReadOnly())
	if err != nil {
		t.Fatal(err)
	}
	next := nextOpened.(*SQLiteStore)
	defer next.CloseStore()
	if next.hasActiveTypeIndex {
		t.Fatal("dropped index still cached after reopen")
	}
	after, err := next.List(query)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatal("drop/reopen changed rows")
	}
}
