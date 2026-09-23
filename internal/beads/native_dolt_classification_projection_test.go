package beads

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/go-sql-driver/mysql"

	beadslib "github.com/steveyegge/beads"
)

// Containment must keep the class inputs while declining bodies and dependency hydration.
func TestNativeClassificationProjectionReadShape(t *testing.T) {
	calls := 0
	storage := &nativeDoltStorageSpy{searchIssues: func(_ context.Context, query string, f beadslib.IssueFilter) ([]*beadslib.Issue, error) {
		calls++
		if query != "" || !f.Lite || f.IncludeDependencies || f.SkipLabels || f.Ephemeral != nil || f.Status != nil || len(f.ExcludeStatus) != 0 || f.Limit != 0 {
			t.Fatalf("unsafe or hydrating classification filter: %+v", f)
		}
		return []*beadslib.Issue{
			{ID: "closed-session", IssueType: "session", Status: beadslib.StatusClosed, Labels: []string{"gc:session"}, Metadata: json.RawMessage(`{"gc.kind":"workflow","number":7}`), Description: "must not escape"},
			{ID: "ephemeral", IssueType: "task", Ephemeral: true, Labels: []string{"gc:nudge-queue"}},
			{ID: "no-history", IssueType: "task", NoHistory: true, Metadata: json.RawMessage(`{"gc.root_bead_id":"root"}`)},
		}, nil
	}}
	reader, ok := Store(newNativeDoltStoreForTest(storage)).(ClassificationReader)
	if !ok {
		t.Fatal("native store has no classification projection; containment still hydrates full rows")
	}
	want := []ClassificationRow{
		{ID: "closed-session", Type: "session", Labels: []string{"gc:session"}, Metadata: map[string]string{"gc.kind": "workflow", "number": "7"}},
		{ID: "ephemeral", Type: "task", Labels: []string{"gc:nudge-queue"}},
		{ID: "no-history", Type: "task", Metadata: map[string]string{"gc.root_bead_id": "root"}},
	}
	for range 2 {
		got, err := reader.ReadClassification()
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("projection=%#v err=%v want=%#v", got, err, want)
		}
	}
	if calls != 2 {
		t.Fatalf("reads=%d: classification must not cache a verdict", calls)
	}
}

// Neither a failed query nor a partially decoded census may authorize containment.
func TestNativeClassificationProjectionFailsClosed(t *testing.T) {
	readFailure := errors.New("classification source unavailable")
	for _, tc := range []struct {
		name string
		rows []*beadslib.Issue
		err  error
	}{
		{name: "query", err: readFailure},
		{name: "metadata", rows: []*beadslib.Issue{{ID: "valid", IssueType: "session"}, {ID: "bad", Metadata: json.RawMessage(`{"broken":`)}}},
		{name: "nil row", rows: []*beadslib.Issue{nil}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			storage := &nativeDoltStorageSpy{searchIssues: func(context.Context, string, beadslib.IssueFilter) ([]*beadslib.Issue, error) { return tc.rows, tc.err }}
			reader, ok := Store(newNativeDoltStoreForTest(storage)).(ClassificationReader)
			if !ok {
				t.Fatal("native store lacks classification projection")
			}
			got, err := reader.ReadClassification()
			if err == nil || got != nil {
				t.Fatalf("partial census accepted: %#v, %v", got, err)
			}
			if tc.err != nil && !errors.Is(err, tc.err) {
				t.Fatalf("lost query failure: %v", err)
			}
		})
	}
}

// This boundary proof uses an in-memory SQL engine, not a Dolt conformance
// substitute. The tagged isolated-Dolt owner covers the real provider.
type classificationSQLStorage struct {
	*nativeDoltStorageSpy
	db *sql.DB
}

func (s *classificationSQLStorage) QueryContext(ctx context.Context, q string, args ...any) (*sql.Rows, error) {
	return s.db.QueryContext(ctx, q, args...)
}

func TestNativeClassificationSQLCensus(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	exec := func(q string) {
		t.Helper()
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	exec(`CREATE TABLE issues (id TEXT PRIMARY KEY, issue_type TEXT, metadata TEXT)`)
	exec(`CREATE TABLE wisps (id TEXT PRIMARY KEY, issue_type TEXT, metadata TEXT)`)
	exec(`CREATE TABLE labels (issue_id TEXT, label TEXT)`)
	exec(`CREATE TABLE wisp_labels (issue_id TEXT, label TEXT)`)
	// The joined census must retain unlabelled records too.
	exec(`INSERT INTO issues VALUES ('closed','task','{"gc.kind":"workflow"}'), ('unlabelled','task','{}')`)
	exec(`INSERT INTO labels VALUES ('closed',''), ('closed','z'), ('closed','gc:session'), ('duplicate','durable-only'), ('orphan','ignored')`)
	storage := &classificationSQLStorage{nativeDoltStorageSpy: &nativeDoltStorageSpy{searchIssues: func(context.Context, string, beadslib.IssueFilter) ([]*beadslib.Issue, error) {
		return nil, errors.New("SQL-capable store unexpectedly used full search")
	}}, db: db}
	reader := newNativeDoltStoreForTest(storage)
	if got, err := reader.ReadClassification(); err != nil || len(got) != 2 || got[1].Labels != nil {
		t.Fatalf("unlabelled row lost from joined census: %#v %v", got, err)
	}
	exec(`INSERT INTO issues VALUES ('duplicate','task','{"broken":')`)
	if got, err := reader.ReadClassification(); err == nil || got != nil {
		t.Fatalf("malformed census accepted: %#v %v", got, err)
	}
	exec(`INSERT INTO wisps VALUES ('duplicate','session','{"gc.kind":"wisp"}')`)
	exec(`INSERT INTO wisp_labels VALUES ('duplicate','wisp-only')`)
	want := []ClassificationRow{{ID: "closed", Type: "task", Labels: []string{"", "gc:session", "z"}, Metadata: map[string]string{"gc.kind": "workflow"}}, {ID: "duplicate", Type: "session", Labels: []string{"wisp-only"}, Metadata: map[string]string{"gc.kind": "wisp"}}, {ID: "unlabelled", Type: "task"}}
	got, err := reader.ReadClassification()
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("tier parity got=%#v err=%v want=%#v", got, err, want)
	}
	exec(`UPDATE wisps SET metadata='{"gc.kind":"updated"}'`)
	got, err = reader.ReadClassification()
	if err != nil || got[1].Metadata["gc.kind"] != "updated" {
		t.Fatalf("stale census: %#v %v", got, err)
	}
	// Rename only this in-memory fixture table: required label failures cannot
	// silently return an incomplete classification census.
	exec(`ALTER TABLE wisp_labels RENAME TO unavailable_labels`)
	if got, err := reader.ReadClassification(); err == nil || got != nil {
		t.Fatalf("missing labels accepted: %#v %v", got, err)
	}
	exec(`ALTER TABLE unavailable_labels RENAME TO wisp_labels`)
	exec(`UPDATE wisps SET metadata='{"broken":'`)
	if got, err := reader.ReadClassification(); err == nil || got != nil || !errors.Is(err, errNativeIssueMetadataParse) {
		t.Fatalf("metadata failure lost: %#v %v", got, err)
	}
	exec(`UPDATE wisps SET metadata='{}'`)
	exec(`UPDATE wisp_labels SET label=NULL`)
	if got, err := reader.ReadClassification(); err == nil || got != nil {
		t.Fatalf("NULL label accepted: %#v %v", got, err)
	}
}

// Only schema absence delegates to the canonical guarded reader; it remains
// responsible for distinguishing optional tables from an incomplete census.
type classificationQueryFailure struct {
	*nativeDoltStorageSpy
	err error
}

func (s *classificationQueryFailure) QueryContext(context.Context, string, ...any) (*sql.Rows, error) {
	return nil, s.err
}

func TestNativeClassificationSQLFallback(t *testing.T) {
	for _, number := range []uint16{1146, 1105} {
		calls := 0
		upstream := errors.New("canonical reader refused incomplete schema")
		storage := &classificationQueryFailure{nativeDoltStorageSpy: &nativeDoltStorageSpy{searchIssues: func(context.Context, string, beadslib.IssueFilter) ([]*beadslib.Issue, error) {
			calls++
			return nil, upstream
		}}, err: &mysql.MySQLError{Number: number, Message: "query failure"}}
		got, err := newNativeDoltStoreForTest(storage).ReadClassification()
		if got != nil || err == nil {
			t.Fatalf("partial census accepted: %#v %v", got, err)
		}
		if number == 1146 {
			if calls != 1 || !errors.Is(err, upstream) {
				t.Fatalf("canonical failure lost: calls=%d err=%v", calls, err)
			}
		} else if calls != 0 || !errors.Is(err, storage.err) {
			t.Fatalf("non-schema error retried as legacy read: calls=%d err=%v", calls, err)
		}
	}
}
