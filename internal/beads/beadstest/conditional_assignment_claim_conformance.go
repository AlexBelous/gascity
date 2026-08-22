package beadstest

import (
	"sync"
	"testing"

	"github.com/gastownhall/gascity/internal/beads"
)

// conditionalAssignmentClaimer is the store-boundary operation pool members
// need when ready work is assigned to their shared template. Keeping the
// expected and next assignees in one call is what prevents a read followed by
// an unconditional assignment from producing two owners.
type conditionalAssignmentClaimer interface {
	ClaimIfCurrent(id, expectedAssignee, assignee string) (beads.Bead, bool, error)
}

// RunConditionalAssignmentClaimConformance verifies that a store transfers an
// open assignment only from the exact expected owner and admits one winner
// under concurrent pool-member claims.
func RunConditionalAssignmentClaimConformance(t *testing.T, name string, open func(t *testing.T) beads.Store) {
	t.Helper()

	t.Run(name+"/exact_open_assignment", func(t *testing.T) {
		store := open(t)
		claimer := conditionalAssignmentClaimerFor(t, store)
		created := createConditionalAssignmentClaimBead(t, store, "open")

		claimed, ok, err := claimer.ClaimIfCurrent(created.ID, "pool-worker", "pool-worker-1")
		if err != nil {
			t.Fatalf("ClaimIfCurrent exact assignment: %v", err)
		}
		if !ok {
			t.Fatal("ClaimIfCurrent exact assignment lost without a competing claimant")
		}
		if claimed.Status != "in_progress" || claimed.Assignee != "pool-worker-1" {
			t.Fatalf("claimed bead = status %q assignee %q, want in_progress owned by pool-worker-1", claimed.Status, claimed.Assignee)
		}
		assertConditionalAssignmentClaimState(t, store, created.ID, "in_progress", "pool-worker-1")
	})

	t.Run(name+"/wrong_expected_assignee", func(t *testing.T) {
		store := open(t)
		claimer := conditionalAssignmentClaimerFor(t, store)
		created := createConditionalAssignmentClaimBead(t, store, "open")

		if claimed, ok, err := claimer.ClaimIfCurrent(created.ID, "other-pool", "pool-worker-1"); err != nil {
			t.Fatalf("ClaimIfCurrent wrong expected assignee: %v", err)
		} else if ok {
			t.Fatalf("ClaimIfCurrent wrong expected assignee reported success: %+v", claimed)
		}
		assertConditionalAssignmentClaimState(t, store, created.ID, "open", "pool-worker")
	})

	t.Run(name+"/in_progress_template_is_not_transferable", func(t *testing.T) {
		store := open(t)
		claimer := conditionalAssignmentClaimerFor(t, store)
		created := createConditionalAssignmentClaimBead(t, store, "in_progress")

		if claimed, ok, err := claimer.ClaimIfCurrent(created.ID, "pool-worker", "pool-worker-1"); err != nil {
			t.Fatalf("ClaimIfCurrent in-progress template assignment: %v", err)
		} else if ok {
			t.Fatalf("ClaimIfCurrent adopted in-progress template work: %+v", claimed)
		}
		assertConditionalAssignmentClaimState(t, store, created.ID, "in_progress", "pool-worker")
	})

	t.Run(name+"/two_pool_members_have_one_winner", func(t *testing.T) {
		store := open(t)
		claimer := conditionalAssignmentClaimerFor(t, store)
		created := createConditionalAssignmentClaimBead(t, store, "open")

		type result struct {
			assignee string
			claimed  beads.Bead
			ok       bool
			err      error
		}
		start := make(chan struct{})
		results := make(chan result, 2)
		var ready sync.WaitGroup
		ready.Add(2)
		for _, assignee := range []string{"pool-worker-1", "pool-worker-2"} {
			go func(assignee string) {
				ready.Done()
				<-start
				claimed, ok, err := claimer.ClaimIfCurrent(created.ID, "pool-worker", assignee)
				results <- result{assignee: assignee, claimed: claimed, ok: ok, err: err}
			}(assignee)
		}
		ready.Wait()
		close(start)

		winner := ""
		for range 2 {
			got := <-results
			if got.err != nil {
				t.Errorf("ClaimIfCurrent by %s: %v", got.assignee, got.err)
				continue
			}
			if !got.ok {
				continue
			}
			if winner != "" {
				t.Errorf("both %s and %s won the same template assignment", winner, got.assignee)
			}
			winner = got.assignee
			if got.claimed.Status != "in_progress" || got.claimed.Assignee != got.assignee {
				t.Errorf("winner %s received %+v, want its concrete in-progress ownership", got.assignee, got.claimed)
			}
		}
		if winner == "" {
			t.Fatal("neither pool member won the open template assignment")
		}
		assertConditionalAssignmentClaimState(t, store, created.ID, "in_progress", winner)
	})
}

func conditionalAssignmentClaimerFor(t *testing.T, store beads.Store) conditionalAssignmentClaimer {
	t.Helper()
	claimer, ok := store.(conditionalAssignmentClaimer)
	if !ok {
		t.Fatalf("%T does not implement the atomic conditional-assignment claim contract", store)
	}
	return claimer
}

func createConditionalAssignmentClaimBead(t *testing.T, store beads.Store, status string) beads.Bead {
	t.Helper()
	created, err := store.Create(beads.Bead{
		Title:    "shared pool work",
		Type:     "task",
		Status:   status,
		Assignee: "pool-worker",
	})
	if err != nil {
		t.Fatalf("Create conditional-assignment fixture: %v", err)
	}
	return created
}

func assertConditionalAssignmentClaimState(t *testing.T, store beads.Store, id, status, assignee string) {
	t.Helper()
	got, err := store.Get(id)
	if err != nil {
		t.Fatalf("Get(%q): %v", id, err)
	}
	if got.Status != status || got.Assignee != assignee {
		t.Fatalf("persisted bead = status %q assignee %q, want status %q assignee %q", got.Status, got.Assignee, status, assignee)
	}
}
