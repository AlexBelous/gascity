package api

import (
	"context"
	"errors"
	"testing"

	"github.com/gastownhall/gascity/internal/api/apierr"
	"github.com/gastownhall/gascity/internal/beads"
)

// claimTestStore is a MemStore that also satisfies beads.AtomicClaimer with a
// simple compare-and-swap, so the claim handler (admission, actor validation,
// store iteration, result translation) can be exercised without a real Dolt
// backend. Claim semantics mirror upstream ClaimIssue at the level the handler
// depends on: award an open unassigned bead, idempotent same-actor, otherwise a
// lost race.
type claimTestStore struct {
	*beads.MemStore
}

func (c claimTestStore) ClaimBead(_ context.Context, id, actor string) (beads.Bead, bool, error) {
	b, err := c.Get(id)
	if err != nil {
		return beads.Bead{}, false, err // wraps beads.ErrNotFound
	}
	if b.Status == "open" && (b.Assignee == "" || b.Assignee == actor) {
		status := "in_progress"
		if err := c.Update(id, beads.UpdateOpts{Status: &status, Assignee: &actor}); err != nil {
			return beads.Bead{}, false, err
		}
		got, _ := c.Get(id)
		return got, true, nil
	}
	if b.Assignee == actor && b.Status == "in_progress" {
		got, _ := c.Get(id)
		return got, true, nil
	}
	return beads.Bead{}, false, nil // another actor won, or not claimable
}

func newClaimServer(t *testing.T) (*Server, claimTestStore) {
	t.Helper()
	store := claimTestStore{MemStore: beads.NewMemStore()}
	state := newFakeState(t)
	state.cityBeadStore = store
	return New(state), store
}

func assertAPIErrCode(t *testing.T, err error, wantCode string) {
	t.Helper()
	var me *apierr.ErrorModel
	if !errors.As(err, &me) {
		t.Fatalf("err = %v (%T), want *apierr.ErrorModel", err, err)
	}
	if me.Code != wantCode {
		t.Fatalf("err code = %q, want %q (detail: %s)", me.Code, wantCode, me.Detail)
	}
}

func TestBeadClaimSuccess(t *testing.T) {
	s, store := newClaimServer(t)
	created, err := store.Create(beads.Bead{Title: "claim target"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	in := &BeadClaimInput{ID: created.ID}
	in.Body.Actor = "worker-1"

	out, err := s.humaHandleBeadClaim(context.Background(), in)
	if err != nil {
		t.Fatalf("humaHandleBeadClaim: %v", err)
	}
	if !out.Body.Claimed {
		t.Fatal("Claimed = false, want true")
	}
	if out.Body.Bead.Assignee != "worker-1" || out.Body.Bead.Status != "in_progress" {
		t.Fatalf("claimed bead = %+v, want worker-1 in_progress", out.Body.Bead)
	}
}

func TestBeadClaimConflictReturnsNotClaimed(t *testing.T) {
	s, store := newClaimServer(t)
	created, err := store.Create(beads.Bead{Title: "contended"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	first := &BeadClaimInput{ID: created.ID}
	first.Body.Actor = "worker-1"
	if _, err := s.humaHandleBeadClaim(context.Background(), first); err != nil {
		t.Fatalf("first claim: %v", err)
	}
	second := &BeadClaimInput{ID: created.ID}
	second.Body.Actor = "worker-2"
	out, err := s.humaHandleBeadClaim(context.Background(), second)
	if err != nil {
		t.Fatalf("conflicting claim returned err = %v, want nil (lost race is not an error)", err)
	}
	if out.Body.Claimed {
		t.Fatal("Claimed = true for the loser, want false")
	}
}

func TestBeadClaimEmptyActorRejected(t *testing.T) {
	s, store := newClaimServer(t)
	created, err := store.Create(beads.Bead{Title: "needs actor"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	in := &BeadClaimInput{ID: created.ID}
	in.Body.Actor = "   "
	_, err = s.humaHandleBeadClaim(context.Background(), in)
	assertAPIErrCode(t, err, "invalid-request")
}

func TestBeadClaimNotFound(t *testing.T) {
	s, _ := newClaimServer(t)
	in := &BeadClaimInput{ID: "gc-nope"}
	in.Body.Actor = "worker-1"
	_, err := s.humaHandleBeadClaim(context.Background(), in)
	assertAPIErrCode(t, err, "bead-not-found")
}

func TestBeadClaimAdmissionSaturationFailsFast(t *testing.T) {
	s, store := newClaimServer(t)
	created, err := store.Create(beads.Bead{Title: "admit me"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	// Capacity-1 gate with its single slot already held: the handler's
	// non-blocking acquire must fail, returning a retryable 503 rather than
	// touching the store.
	s.claimAdmitter = newClaimAdmitter(1)
	release, ok := s.claimAdmitter.tryAcquire()
	if !ok {
		t.Fatal("could not pre-acquire the sole admission slot")
	}
	defer release()

	in := &BeadClaimInput{ID: created.ID}
	in.Body.Actor = "worker-1"
	_, err = s.humaHandleBeadClaim(context.Background(), in)
	assertAPIErrCode(t, err, "service-unavailable")

	// The bead must remain unclaimed — admission rejection must not mutate.
	got, getErr := store.Get(created.ID)
	if getErr != nil {
		t.Fatalf("Get: %v", getErr)
	}
	if got.Status != "open" || got.Assignee != "" {
		t.Fatalf("bead = %+v, want still open/unassigned after admission rejection", got)
	}
}

func TestBeadClaimAdmissionReleasedAfterHandling(t *testing.T) {
	s, store := newClaimServer(t)
	created, err := store.Create(beads.Bead{Title: "slot release"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	s.claimAdmitter = newClaimAdmitter(1)
	in := &BeadClaimInput{ID: created.ID}
	in.Body.Actor = "worker-1"

	// Two sequential claims through a single-slot gate both succeed only if the
	// first released its slot; a leaked slot would saturate the second.
	if _, err := s.humaHandleBeadClaim(context.Background(), in); err != nil {
		t.Fatalf("first claim: %v", err)
	}
	if _, err := s.humaHandleBeadClaim(context.Background(), in); err != nil {
		t.Fatalf("second claim (slot leaked?): %v", err)
	}
}

func TestBeadClaimStoreWithoutAtomicClaimer(t *testing.T) {
	// A plain MemStore does not implement AtomicClaimer; the handler must surface
	// a wiring defect rather than silently report the bead unclaimed.
	mem := beads.NewMemStore()
	created, err := mem.Create(beads.Bead{Title: "no claimer"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	state := newFakeState(t)
	state.cityBeadStore = mem
	s := New(state)

	in := &BeadClaimInput{ID: created.ID}
	in.Body.Actor = "worker-1"
	_, err = s.humaHandleBeadClaim(context.Background(), in)
	assertAPIErrCode(t, err, "internal")
}
