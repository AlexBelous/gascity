package main

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/spf13/cobra"
)

func readyAssigneeAnyTestOpts(t *testing.T, args ...string) readyOpts {
	t.Helper()
	var opts readyOpts
	var ephemeral, jsonOut bool
	cmd := &cobra.Command{}
	registerReadyFlags(cmd, &opts, &ephemeral, &jsonOut)
	if err := cmd.ParseFlags(args); err != nil {
		t.Fatal(err)
	}
	return opts
}

// A worker can own active work under its durable ID, runtime name or alias.
// Their union must exclude other workers before any dependency reads occur.
func TestReadyAssigneeAnyScopesBeforeDependencyReads(t *testing.T) {
	store := &readyAssigneeAnyDepStore{Store: beads.NewMemStore()}
	first := mustCreateReadyBead(t, store, beads.Bead{Title: "first", Type: "task", Status: "in_progress", Assignee: "session-17", Priority: readyPriority(2)})
	second := mustCreateReadyBead(t, store, beads.Bead{Title: "second", Type: "task", Status: "in_progress", Assignee: "worker-a", Priority: readyPriority(1)})
	foreign := mustCreateReadyBead(t, store, beads.Bead{Title: "unrelated", Type: "task", Status: "in_progress", Assignee: "worker-b", Priority: readyPriority(0)})
	status := readyStatusInProgress
	for id, identity := range map[string]string{first.ID: "session-17", second.ID: "worker-a", foreign.ID: "worker-b"} {
		if err := store.Update(id, beads.UpdateOpts{Status: &status, Assignee: &identity}); err != nil {
			t.Fatal(err)
		}
	}
	store.forbidden = foreign.ID
	opts := readyAssigneeAnyTestOpts(t, "--status=in_progress", "--assignee-any=session-17", "--assignee-any=worker-a", "--assignee-any=session-17")
	rows, err := readyBeadsForOpts([]readyLeg{readyTestLeg("city", store)}, opts)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := readyWireIDs(rows), []string{second.ID, first.ID}; !reflect.DeepEqual(got, want) {
		t.Fatalf("rows = %v, want %v", got, want)
	}
	if !reflect.DeepEqual(store.reads, []string{second.ID, first.ID}) {
		t.Fatalf("dependency reads = %v", store.reads)
	}
}

type readyAssigneeAnyDepStore struct {
	beads.Store
	forbidden string
	reads     []string
}

func (s *readyAssigneeAnyDepStore) DepList(id, direction string) ([]beads.Dep, error) {
	s.reads = append(s.reads, id)
	if id == s.forbidden {
		return nil, errors.New("unrelated worker dependency source must not be read")
	}
	return s.Store.DepList(id, direction)
}

func TestReadyAssigneeAnyInvalidQueryDoesNotReadStores(t *testing.T) {
	for _, args := range [][]string{
		{"--assignee-any="},
		{"--assignee-any=   "},
		{"--assignee-any=session-17", "--assignee-any="},
		{"--assignee-any=session-17", "--assignee=worker-a"},
		{"--assignee-any=session-17", "--unassigned"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			opts := readyAssigneeAnyTestOpts(t, args...)
			_, err := readyBeadsForOpts([]readyLeg{readyTestLeg("unavailable", readyFailingStore{err: errors.New("store was read")})}, opts)
			if err == nil || !strings.Contains(err.Error(), "--assignee-any") {
				t.Fatalf("error = %v, want invalid identity selector before store read", err)
			}
		})
	}
}

func TestReadyAssigneeAnyLiteralIdentitiesAndOtherFilters(t *testing.T) {
	items := []beads.Bead{
		{ID: "a", Assignee: "runtime,name", Type: "task"},
		{ID: "b", Assignee: "runtime", Type: "task"},
		{ID: "c", Assignee: "alias", Type: "task", Labels: []string{"hold:external"}},
		{ID: "d", Assignee: " alias ", Type: "task"},
		{ID: "e", Assignee: "", Type: "task"},
	}
	opts := readyAssigneeAnyTestOpts(t, "--assignee-any=runtime,name", "--assignee-any= alias ", "--exclude-label=hold:external")
	got := filterReadyBeads(items, opts, nil)
	if len(got) != 2 || got[0].ID != "a" || got[1].ID != "d" {
		t.Fatalf("filtered rows = %+v", got)
	}
}
