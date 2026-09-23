package beads

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

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
