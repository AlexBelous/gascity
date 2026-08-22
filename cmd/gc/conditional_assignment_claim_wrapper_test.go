package main

import (
	"testing"

	"github.com/gastownhall/gascity/internal/beads"
)

type conditionalAssignmentClaimStore interface {
	ClaimIfCurrent(id, expectedAssignee, assignee string) (beads.Bead, bool, error)
}

type conditionalAssignmentClaimProbe struct {
	beads.Store
	calls [][3]string
}

func (s *conditionalAssignmentClaimProbe) ClaimIfCurrent(id, expectedAssignee, assignee string) (beads.Bead, bool, error) {
	s.calls = append(s.calls, [3]string{id, expectedAssignee, assignee})
	current, err := s.Get(id)
	if err != nil {
		return beads.Bead{}, false, err
	}
	if current.Status != "open" || current.Assignee != expectedAssignee {
		return beads.Bead{}, false, nil
	}
	status := "in_progress"
	if err := s.Update(id, beads.UpdateOpts{Status: &status, Assignee: &assignee}); err != nil {
		return beads.Bead{}, false, err
	}
	current, err = s.Get(id)
	return current, err == nil, err
}

type conditionalAssignmentClaimStrippedStore struct{ beads.Store }

func TestBeadPolicyStoreForwardsConditionalAssignmentClaim(t *testing.T) {
	backing := &conditionalAssignmentClaimProbe{Store: beads.NewMemStore()}
	wrapped := wrapStoreWithBeadPolicies(backing, nil)
	claimer, ok := wrapped.(conditionalAssignmentClaimStore)
	if !ok {
		t.Fatalf("bead policy wrapper %T dropped the conditional-assignment claim operation", wrapped)
	}
	created, err := wrapped.Create(beads.Bead{Title: "pool work", Status: "open", Assignee: "pool-worker"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	claimed, won, err := claimer.ClaimIfCurrent(created.ID, "pool-worker", "pool-worker-1")
	if err != nil || !won {
		t.Fatalf("ClaimIfCurrent through policy wrapper = (%+v, %v, %v), want a win", claimed, won, err)
	}
	if len(backing.calls) != 1 || backing.calls[0] != [3]string{created.ID, "pool-worker", "pool-worker-1"} {
		t.Fatalf("backing calls = %v, want the exact expected and concrete assignees", backing.calls)
	}
}

func TestBeadPolicyStoreRejectsUnsupportedConditionalAssignmentClaim(t *testing.T) {
	backing := conditionalAssignmentClaimStrippedStore{Store: beads.NewMemStore()}
	wrapped := wrapStoreWithBeadPolicies(backing, nil)
	claimer, ok := wrapped.(conditionalAssignmentClaimStore)
	if !ok {
		t.Fatalf("bead policy wrapper %T hides whether conditional assignment claim is unsupported", wrapped)
	}
	created, err := wrapped.Create(beads.Bead{Title: "pool work", Status: "open", Assignee: "pool-worker"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	claimed, won, err := claimer.ClaimIfCurrent(created.ID, "pool-worker", "pool-worker-1")
	if err == nil {
		t.Fatalf("unsupported ClaimIfCurrent = (%+v, %v, nil), want an explicit error", claimed, won)
	}
	if won {
		t.Fatalf("unsupported ClaimIfCurrent reported a win: %+v", claimed)
	}
	got, getErr := wrapped.Get(created.ID)
	if getErr != nil {
		t.Fatalf("Get after unsupported claim: %v", getErr)
	}
	if got.Status != "open" || got.Assignee != "pool-worker" {
		t.Fatalf("unsupported claim fell back to a non-atomic write: %+v", got)
	}
}

func TestEmittingClassStoreForwardsConditionalAssignmentClaim(t *testing.T) {
	cityPath := t.TempDir()
	backing := &conditionalAssignmentClaimProbe{Store: beads.NewMemStore()}
	wrapped := &emittingClassStore{Store: backing, cityPath: cityPath}
	claimer, ok := any(wrapped).(conditionalAssignmentClaimStore)
	if !ok {
		t.Fatalf("emitting class wrapper %T dropped the conditional-assignment claim operation", wrapped)
	}
	created, err := backing.Create(beads.Bead{Title: "pool graph work", Status: "open", Assignee: "pool-worker"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	claimed, won, err := claimer.ClaimIfCurrent(created.ID, "pool-worker", "pool-worker-1")
	if err != nil || !won {
		t.Fatalf("ClaimIfCurrent through emitting wrapper = (%+v, %v, %v), want a win", claimed, won, err)
	}
	events := beadEvents(readCityJournal(t, cityPath))
	if len(events) != 1 || events[0].Type != "bead.updated" || events[0].Subject != created.ID {
		t.Fatalf("conditional claim emitted %+v, want one bead.updated for %s", events, created.ID)
	}
}

func TestEmittingClassStoreRejectsUnsupportedConditionalAssignmentClaim(t *testing.T) {
	cityPath := t.TempDir()
	backing := conditionalAssignmentClaimStrippedStore{Store: beads.NewMemStore()}
	wrapped := &emittingClassStore{Store: backing, cityPath: cityPath}
	claimer, ok := any(wrapped).(conditionalAssignmentClaimStore)
	if !ok {
		t.Fatalf("emitting class wrapper %T hides whether conditional assignment claim is unsupported", wrapped)
	}
	created, err := backing.Create(beads.Bead{Title: "pool graph work", Status: "open", Assignee: "pool-worker"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	claimed, won, err := claimer.ClaimIfCurrent(created.ID, "pool-worker", "pool-worker-1")
	if err == nil {
		t.Fatalf("unsupported ClaimIfCurrent = (%+v, %v, nil), want an explicit error", claimed, won)
	}
	if won {
		t.Fatalf("unsupported ClaimIfCurrent reported a win: %+v", claimed)
	}
	if events := beadEvents(readCityJournal(t, cityPath)); len(events) != 0 {
		t.Fatalf("unsupported conditional claim emitted events: %+v", events)
	}
}
