package beads

import (
	"context"
	"strings"
	"testing"
)

// TestBdStoreClaimBeadFailsClosedOnActorMismatch proves the AtomicClaimer
// actor-authority contract: when the bd invocation claims a bead for a
// different actor than the caller requested (a store whose configured
// BEADS_ACTOR diverges from the passed actor), ClaimBead reports a lost race
// rather than a successful wrong-owner claim. Without the guard this returned
// (bead-owned-by-A, true) for a request to claim as B — a wrong-owner success.
func TestBdStoreClaimBeadFailsClosedOnActorMismatch(t *testing.T) {
	// The runner simulates `bd update --claim`: it always commits the bead to
	// actor-A (the store's configured BEADS_ACTOR), regardless of the requested
	// actor, because the bd actor is fixed at store construction.
	runner := func(_, _ string, _ ...string) ([]byte, error) {
		return []byte(`{"id":"gc-7","status":"in_progress","assignee":"actor-A"}`), nil
	}
	s := NewBdStore("/city", runner)

	bead, ok, err := s.ClaimBead(context.Background(), "gc-7", "actor-B")
	if err != nil {
		t.Fatalf("ClaimBead err = %v, want nil (a mismatch is a lost race, not an error)", err)
	}
	if ok {
		t.Fatalf("ClaimBead ok = true for actor-B while bd committed to actor-A; want false (fail closed). bead=%+v", bead)
	}
	if strings.TrimSpace(bead.Assignee) != "" {
		t.Fatalf("ClaimBead returned a bead owned by %q on a mismatch; want the zero bead so no wrong owner leaks", bead.Assignee)
	}
}

// TestBdStoreClaimBeadSucceedsWhenActorMatches proves the common path: when the
// bd invocation claims for the same actor the caller requested, ClaimBead
// returns the claimed bead with ok=true.
func TestBdStoreClaimBeadSucceedsWhenActorMatches(t *testing.T) {
	runner := func(_, _ string, _ ...string) ([]byte, error) {
		return []byte(`{"id":"gc-7","status":"in_progress","assignee":"actor-A"}`), nil
	}
	s := NewBdStore("/city", runner)

	bead, ok, err := s.ClaimBead(context.Background(), "gc-7", "actor-A")
	if err != nil || !ok {
		t.Fatalf("ClaimBead ok=%v err=%v, want true nil for a matching actor", ok, err)
	}
	if bead.Assignee != "actor-A" || bead.ID != "gc-7" {
		t.Fatalf("ClaimBead bead = %+v, want gc-7 owned by actor-A", bead)
	}
}

// TestBdStoreClaimBeadEmptyActorSkipsGuard proves an empty actor (an
// identity-agnostic caller) keeps the historical forward-to-Claim behavior: the
// guard only fires when a concrete actor was requested.
func TestBdStoreClaimBeadEmptyActorSkipsGuard(t *testing.T) {
	runner := func(_, _ string, _ ...string) ([]byte, error) {
		return []byte(`{"id":"gc-7","status":"in_progress","assignee":"actor-A"}`), nil
	}
	s := NewBdStore("/city", runner)

	bead, ok, err := s.ClaimBead(context.Background(), "gc-7", "")
	if err != nil || !ok {
		t.Fatalf("ClaimBead ok=%v err=%v, want true nil for an empty requested actor", ok, err)
	}
	if bead.Assignee != "actor-A" {
		t.Fatalf("ClaimBead bead assignee = %q, want actor-A passed through", bead.Assignee)
	}
}
