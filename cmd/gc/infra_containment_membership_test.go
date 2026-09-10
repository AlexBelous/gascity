package main

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/gastownhall/gascity/internal/beads"
)

// containmentMembershipStore rejects broad reads and models missing destination IDs.
type containmentMembershipStore struct {
	beads.Store
	calls  int
	seen   map[string]bool
	failAt int
}

func (s *containmentMembershipStore) List(q beads.ListQuery) ([]beads.Bead, error) {
	s.calls++
	if len(q.IDs) == 0 || len(q.IDs) > 256 || q.AllowScan {
		return nil, fmt.Errorf("unbounded destination read: ids=%d scan=%v", len(q.IDs), q.AllowScan)
	}
	if !q.IncludeClosed || q.TierMode != beads.TierBoth || q.Limit != 0 {
		return nil, errors.New("membership must include closed rows and both tiers without truncation")
	}
	if s.calls == s.failAt {
		return nil, errors.New("binding unavailable")
	}
	var result []beads.Bead
	for _, id := range q.IDs {
		if s.seen[id] {
			return nil, fmt.Errorf("duplicate destination lookup %s", id)
		}
		s.seen[id] = true
		if id != "missing" {
			result = append(result, beads.Bead{ID: id})
		}
	}
	return result, nil
}

// TestInfraContainmentMembershipBoundsDestinationReads guards cost as the unrelated graph grows.
func TestInfraContainmentMembershipBoundsDestinationReads(t *testing.T) {
	rows := []beads.Bead{{ID: "missing"}}
	for i := 0; i < 600; i++ {
		rows = append(rows, beads.Bead{ID: fmt.Sprintf("infra-%d", i)})
	}
	store := &containmentMembershipStore{seen: map[string]bool{}}
	have, err := infraDestinationMembership(store, rows)
	if err != nil {
		t.Fatal(err)
	}
	if len(have) != 600 || have["missing"] || len(store.seen) != 601 {
		t.Fatalf("membership=%d missing=%v inspected=%d", len(have), have["missing"], len(store.seen))
	}
	for _, r := range rows[1:] {
		if !have[r.ID] {
			t.Fatalf("lost membership %s", r.ID)
		}
	}
	if store.calls != 3 {
		t.Fatalf("round trips=%d, want 3 bounded batches", store.calls)
	}
}

// TestInfraContainmentMembershipEmptyDoesNotScan avoids turning an empty IN-list into a full scan.
func TestInfraContainmentMembershipEmptyDoesNotScan(t *testing.T) {
	store := &containmentMembershipStore{seen: map[string]bool{}}
	have, err := infraDestinationMembership(store, nil)
	if err != nil || len(have) != 0 || store.calls != 0 {
		t.Fatalf("have=%v err=%v reads=%d", have, err, store.calls)
	}
}

// TestInfraContainmentMembershipFailureRejectsPartialEvidence prevents a later batch failure blessing convergence.
func TestInfraContainmentMembershipFailureRejectsPartialEvidence(t *testing.T) {
	store := &containmentMembershipStore{seen: map[string]bool{}, failAt: 2}
	rows := make([]beads.Bead, 257)
	for i := range rows {
		rows[i].ID = fmt.Sprintf("infra-%d", i)
	}
	have, err := infraDestinationMembership(store, rows)
	if have != nil || err == nil || !strings.Contains(err.Error(), "listing binding: binding unavailable") {
		t.Fatalf("partial evidence=%v err=%v", have, err)
	}
}
