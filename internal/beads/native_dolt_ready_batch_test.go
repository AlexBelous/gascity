package beads

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"testing"

	beadslib "github.com/steveyegge/beads"
)

type readyOutcomeBatchStorage struct {
	beadslib.Storage
	edges                              map[string][]*beadslib.Dependency
	issues                             map[string]*beadslib.Issue
	singleCalls, edgeCalls, issueCalls int
	edgeErr, issueErr                  error
}

func (s *readyOutcomeBatchStorage) GetDependencyRecordsForIssues(_ context.Context, ids []string) (map[string][]*beadslib.Dependency, error) {
	s.edgeCalls++
	out := make(map[string][]*beadslib.Dependency)
	for _, id := range ids {
		out[id] = s.edges[id]
	}
	return out, s.edgeErr
}

func (s *readyOutcomeBatchStorage) GetIssuesByIDs(_ context.Context, ids []string) ([]*beadslib.Issue, error) {
	s.issueCalls++
	seen := map[string]bool{}
	var out []*beadslib.Issue
	for _, id := range ids {
		if seen[id] {
			return nil, fmt.Errorf("duplicate hydration ID %s", id)
		}
		seen[id] = true
		if issue := s.issues[id]; issue != nil {
			out = append(out, issue)
		}
	}
	return out, s.issueErr
}

func (s *readyOutcomeBatchStorage) GetDependenciesWithMetadata(_ context.Context, id string) ([]*beadslib.IssueWithDependencyMetadata, error) {
	s.singleCalls++
	var out []*beadslib.IssueWithDependencyMetadata
	for _, edge := range s.edges[id] {
		if issue := s.issues[edge.DependsOnID]; issue != nil {
			out = append(out, &beadslib.IssueWithDependencyMetadata{Issue: *issue, DependencyType: edge.Type})
		}
	}
	return out, nil
}

func TestNativeReadyOutcomeBatchCallCount(t *testing.T) {
	for _, n := range []int{0, 1, 1000, 2001} {
		t.Run(fmt.Sprint(n), func(t *testing.T) {
			s := &readyOutcomeBatchStorage{edges: map[string][]*beadslib.Dependency{}, issues: map[string]*beadslib.Issue{"shared": {ID: "shared", Status: beadslib.StatusClosed}}}
			candidates := make([]Bead, n)
			for i := range candidates {
				id := fmt.Sprintf("candidate-%d", i)
				candidates[i] = Bead{ID: id}
				s.edges[id] = []*beadslib.Dependency{{IssueID: id, DependsOnID: "shared", Type: beadslib.DependencyType("blocks")}}
			}
			got, err := (&NativeDoltStore{}).filterReadyByWorkOutcome(context.Background(), s, candidates)
			if err != nil || !reflect.DeepEqual(got, candidates) {
				t.Fatalf("result=%v err=%v", got, err)
			}
			want := 0
			if n > 0 {
				want = 1
			}
			if s.singleCalls != 0 || s.edgeCalls != want || s.issueCalls != want {
				t.Fatalf("N=%d single=%d batchEdges=%d batchTargets=%d; want 0/%d/%d", n, s.singleCalls, s.edgeCalls, s.issueCalls, want, want)
			}
		})
	}
}

func TestNativeReadyOutcomeBatchParity(t *testing.T) {
	s := &readyOutcomeBatchStorage{edges: map[string][]*beadslib.Dependency{}, issues: map[string]*beadslib.Issue{}}
	cases := []struct{ id, status, kind, meta string }{
		{"pinned", "pinned", "blocks", `{"gc.work_outcome":"blocked"}`},
		{"spawner", "open", "waits-for", `{"gc.work_outcome":"blocked"}`},
		{"blocked", "closed", "blocks", `{"gc.work_outcome":"blocked"}`},
		{"blocked-wait", "closed", "waits-for", `{"gc.work_outcome":"blocked"}`},
		{"done", "closed", "blocks", `{"gc.work_outcome":"shipped"}`},
		{"related", "closed", "related", `{"gc.work_outcome":"blocked"}`},
		{"malformed-unrelated", "closed", "related", `{`},
		{"missing", "closed", "blocks", ``},
	}
	var candidates []Bead
	for _, c := range cases {
		candidates = append(candidates, Bead{ID: c.id})
		s.edges[c.id] = []*beadslib.Dependency{{IssueID: c.id, DependsOnID: "target-" + c.id, Type: beadslib.DependencyType(c.kind)}}
		if c.id != "missing" {
			s.issues["target-"+c.id] = &beadslib.Issue{ID: "target-" + c.id, Status: beadslib.Status(c.status), Metadata: json.RawMessage(c.meta)}
		}
	}
	legacy := &nativeDoltStorageSpy{getDependenciesWithMetadata: s.GetDependenciesWithMetadata}
	store := &NativeDoltStore{}
	want, err := store.filterReadyByWorkOutcome(context.Background(), legacy, candidates)
	if err != nil {
		t.Fatal(err)
	}
	got, err := store.filterReadyByWorkOutcome(context.Background(), s, candidates)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("batch=%v legacy=%v", got, want)
	}
	if len(got) != 6 {
		t.Fatalf("survivors=%v", got)
	}
}

func TestNativeReadyOutcomeBatchFailures(t *testing.T) {
	sentinel := errors.New("backend read failed")
	for _, stage := range []string{"edges", "targets", "metadata"} {
		t.Run(stage, func(t *testing.T) {
			s := &readyOutcomeBatchStorage{edges: map[string][]*beadslib.Dependency{"candidate": {{IssueID: "candidate", DependsOnID: "target", Type: beadslib.DependencyType("blocks")}}}, issues: map[string]*beadslib.Issue{"target": {ID: "target", Status: beadslib.StatusClosed, Metadata: json.RawMessage(`{`)}}}
			if stage == "edges" {
				s.edgeErr = sentinel
			}
			if stage == "targets" {
				s.issueErr = sentinel
			}
			got, err := (&NativeDoltStore{}).filterReadyByWorkOutcome(context.Background(), s, []Bead{{ID: "candidate"}})
			if err == nil || got != nil {
				t.Fatalf("got=%v err=%v; want no partial result and error", got, err)
			}
			if stage != "metadata" && !errors.Is(err, sentinel) {
				t.Fatalf("lost backend error: %v", err)
			}
		})
	}
}
