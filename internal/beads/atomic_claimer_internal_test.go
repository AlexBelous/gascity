package beads

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
)

// ClaimIssue gives the in-memory native storage fixture the same
// compare-and-swap claim semantics upstream beads' DoltStore.ClaimIssue
// provides, so NativeDoltStore.ClaimBead's translation logic can be unit-tested
// without a real Dolt server. It mirrors issueops.ClaimIssueInTx: claim only an
// open bead that is unassigned or already this actor's; a same-actor in_progress
// re-claim is an idempotent success; a foreign assignee is "already claimed"; any
// other non-open state is "not claimable". Error phrasings match the upstream
// sentinels so isNativeClaimConflict / isNativeNotClaimable classify them.
func (s *nativeDoltMemStorage) ClaimIssue(_ context.Context, id, actor string) error {
	bead, err := s.store.Get(id)
	if err != nil {
		return err // wraps ErrNotFound with a "... not found" message
	}
	if bead.Status == "open" && (bead.Assignee == "" || bead.Assignee == actor) {
		status := "in_progress"
		return s.store.Update(id, UpdateOpts{Status: &status, Assignee: &actor})
	}
	if bead.Assignee == actor && bead.Status == "in_progress" {
		return nil // idempotent same-actor re-claim
	}
	if bead.Assignee != "" && bead.Assignee != actor {
		return fmt.Errorf("issue already claimed by %s", bead.Assignee)
	}
	return fmt.Errorf("issue not claimable: status %s", bead.Status)
}

func seedOpenBead(t *testing.T, store Store, title string) Bead {
	t.Helper()
	created, err := store.Create(Bead{Title: title, Type: "task"})
	if err != nil {
		t.Fatalf("seeding bead: %v", err)
	}
	if created.Status != "open" {
		t.Fatalf("seeded bead status = %q, want open", created.Status)
	}
	return created
}

func TestNativeDoltStoreClaimBeadClaimsOpenBead(t *testing.T) {
	store := newNativeDoltStoreForTest(newNativeDoltMemStorage())
	seed := seedOpenBead(t, store, "claim me")

	claimed, ok, err := store.ClaimBead(context.Background(), seed.ID, "worker-1")
	if err != nil {
		t.Fatalf("ClaimBead err = %v, want nil", err)
	}
	if !ok {
		t.Fatal("ClaimBead ok = false, want true")
	}
	if claimed.ID != seed.ID || claimed.Status != "in_progress" || claimed.Assignee != "worker-1" {
		t.Fatalf("claimed = %+v, want %s in_progress assigned to worker-1", claimed, seed.ID)
	}
}

func TestNativeDoltStoreClaimBeadIdempotentSameActor(t *testing.T) {
	store := newNativeDoltStoreForTest(newNativeDoltMemStorage())
	seed := seedOpenBead(t, store, "claim me twice")

	if _, ok, err := store.ClaimBead(context.Background(), seed.ID, "worker-1"); err != nil || !ok {
		t.Fatalf("first ClaimBead ok=%v err=%v, want true nil", ok, err)
	}
	claimed, ok, err := store.ClaimBead(context.Background(), seed.ID, "worker-1")
	if err != nil {
		t.Fatalf("re-ClaimBead err = %v, want nil", err)
	}
	if !ok {
		t.Fatal("re-ClaimBead ok = false, want true (idempotent same-actor)")
	}
	if claimed.Assignee != "worker-1" || claimed.Status != "in_progress" {
		t.Fatalf("re-claimed = %+v, want still worker-1 in_progress", claimed)
	}
}

func TestNativeDoltStoreClaimBeadConflictReturnsFalse(t *testing.T) {
	store := newNativeDoltStoreForTest(newNativeDoltMemStorage())
	seed := seedOpenBead(t, store, "contended")

	if _, ok, err := store.ClaimBead(context.Background(), seed.ID, "worker-1"); err != nil || !ok {
		t.Fatalf("worker-1 ClaimBead ok=%v err=%v, want true nil", ok, err)
	}
	claimed, ok, err := store.ClaimBead(context.Background(), seed.ID, "worker-2")
	if err != nil {
		t.Fatalf("conflicting ClaimBead err = %v, want nil (lost race is not an error)", err)
	}
	if ok {
		t.Fatalf("conflicting ClaimBead ok = true, want false; claimed=%+v", claimed)
	}
	if claimed.ID != "" {
		t.Fatalf("conflicting ClaimBead returned bead %+v, want empty on lost race", claimed)
	}
}

func TestNativeDoltStoreClaimBeadNotFoundIsError(t *testing.T) {
	store := newNativeDoltStoreForTest(newNativeDoltMemStorage())
	_, ok, err := store.ClaimBead(context.Background(), "gc-does-not-exist", "worker-1")
	if ok {
		t.Fatal("ClaimBead ok = true for missing bead, want false")
	}
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("ClaimBead err = %v, want wrapped ErrNotFound", err)
	}
}

func TestBdStoreClaimBeadForwardsToClaim(t *testing.T) {
	var gotArgs []string
	runner := func(_, name string, args ...string) ([]byte, error) {
		if name != "bd" {
			t.Fatalf("name = %q, want bd", name)
		}
		gotArgs = append([]string(nil), args...)
		return []byte(`[{"id":"bd-7","title":"t","status":"in_progress","assignee":"worker-9","issue_type":"task","created_at":"2025-01-15T10:30:00Z"}]`), nil
	}
	store := NewBdStore("/city", runner)
	claimed, ok, err := store.ClaimBead(context.Background(), "bd-7", "worker-9")
	if err != nil || !ok {
		t.Fatalf("ClaimBead ok=%v err=%v, want true nil", ok, err)
	}
	if claimed.ID != "bd-7" || claimed.Assignee != "worker-9" {
		t.Fatalf("claimed = %+v, want bd-7 assigned worker-9", claimed)
	}
	if len(gotArgs) == 0 || gotArgs[0] != "update" {
		t.Fatalf("bd args = %v, want an update --claim invocation", gotArgs)
	}
}

func TestCachingStoreClaimBeadForwardsAndRefreshes(t *testing.T) {
	backing := newNativeDoltStoreForTest(newNativeDoltMemStorage())
	seed := seedOpenBead(t, backing, "cached claim")

	var events []string
	cache := NewCachingStoreForTest(backing, func(eventType, beadID string, _ json.RawMessage) {
		events = append(events, eventType+":"+beadID)
	})

	claimed, ok, err := cache.ClaimBead(context.Background(), seed.ID, "worker-1")
	if err != nil || !ok {
		t.Fatalf("ClaimBead ok=%v err=%v, want true nil", ok, err)
	}
	if claimed.Assignee != "worker-1" || claimed.Status != "in_progress" {
		t.Fatalf("claimed = %+v, want worker-1 in_progress", claimed)
	}
	got, err := cache.Get(seed.ID)
	if err != nil {
		t.Fatalf("Get after claim: %v", err)
	}
	if got.Assignee != "worker-1" || got.Status != "in_progress" {
		t.Fatalf("cached bead = %+v, want worker-1 in_progress", got)
	}
	if len(events) == 0 {
		t.Fatal("expected a bead.updated notification after claim, got none")
	}
}

func TestCachingStoreClaimBeadUnsupportedBacking(t *testing.T) {
	// MemStore does not implement AtomicClaimer, so the cache must refuse rather
	// than silently report a lost race.
	cache := NewCachingStoreForTest(NewMemStore(), nil)
	_, ok, err := cache.ClaimBead(context.Background(), "gc-1", "worker-1")
	if ok {
		t.Fatal("ClaimBead ok = true with unsupported backing, want false")
	}
	if !errors.Is(err, ErrAtomicClaimUnsupported) {
		t.Fatalf("err = %v, want ErrAtomicClaimUnsupported", err)
	}
}
