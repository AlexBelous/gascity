package beads

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	beadslib "github.com/steveyegge/beads"
)

// noClaimStorage is a beadslib.Storage whose method set does not include
// ClaimIssue (the base Storage interface omits it; only DoltStorage's embedded
// BulkIssueStore adds it). It exercises NativeDoltStore.ClaimBead's
// wiring-defect guard.
type noClaimStorage struct{ beadslib.Storage }

func TestNativeDoltStoreClaimBeadStorageWithoutClaimIssue(t *testing.T) {
	store := newNativeDoltStoreForTest(noClaimStorage{})
	_, ok, err := store.ClaimBead(context.Background(), "gc-1", "worker-1")
	if ok {
		t.Fatal("ClaimBead ok = true with a non-claimer storage, want false")
	}
	if err == nil || !strings.Contains(err.Error(), "does not expose ClaimIssue") {
		t.Fatalf("err = %v, want a does-not-expose-ClaimIssue wiring error", err)
	}
}

// These tests raise measured coverage on the slice-1 claim surfaces to the
// handoff bar, exercising the error classifiers' nil/positive branches and
// CachingStore.ClaimBead's backing-refresh-failure fallback.

func TestNativeClaimErrorClassifiers(t *testing.T) {
	cases := []struct {
		name         string
		err          error
		wantConflict bool
		wantNotClaim bool
	}{
		{"nil", nil, false, false},
		{"already claimed", errors.New("issue already claimed by worker-2"), true, false},
		{"not claimable", errors.New("issue not claimable: status closed"), false, true},
		{"unrelated", errors.New("connection reset"), false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isNativeClaimConflict(tc.err); got != tc.wantConflict {
				t.Fatalf("isNativeClaimConflict = %v, want %v", got, tc.wantConflict)
			}
			if got := isNativeNotClaimable(tc.err); got != tc.wantNotClaim {
				t.Fatalf("isNativeNotClaimable = %v, want %v", got, tc.wantNotClaim)
			}
		})
	}
}

// getFailClaimBacking is an AtomicClaimer whose claim succeeds but whose Get
// fails, forcing CachingStore.ClaimBead down its refresh-failure branch.
type getFailClaimBacking struct {
	*MemStore
	seed Bead
}

func (g *getFailClaimBacking) Get(string) (Bead, error) {
	return Bead{}, errors.New("backing get unavailable")
}

func (g *getFailClaimBacking) ClaimBead(_ context.Context, _, actor string) (Bead, bool, error) {
	b := g.seed
	b.Status = "in_progress"
	b.Assignee = actor
	return b, true, nil
}

func TestCachingStoreClaimBeadRefreshFailureStillReturnsBackingBead(t *testing.T) {
	seed := Bead{ID: "gc-77", Title: "refresh-fail", Status: "open", Type: "task"}
	backing := &getFailClaimBacking{MemStore: NewMemStore(), seed: seed}
	cache := NewCachingStoreForTest(backing, nil)

	bead, ok, err := cache.ClaimBead(context.Background(), "gc-77", "worker-1")
	if err != nil || !ok {
		t.Fatalf("ClaimBead ok=%v err=%v, want true nil", ok, err)
	}
	// The backing's canonical bead is returned even though the cache refresh read
	// failed — the claim is committed regardless of read lag.
	if bead.ID != "gc-77" || bead.Assignee != "worker-1" || bead.Status != "in_progress" {
		t.Fatalf("bead = %+v, want gc-77 worker-1 in_progress", bead)
	}
}

// lostRaceBacking reports every claim as a lost race (claimed=false, no error),
// so CachingStore.ClaimBead's pass-through-without-cache-mutation branch is
// exercised.
type lostRaceBacking struct {
	*MemStore
}

func (l *lostRaceBacking) ClaimBead(context.Context, string, string) (Bead, bool, error) {
	return Bead{}, false, nil
}

func TestCachingStoreClaimBeadLostRaceLeavesCacheUntouched(t *testing.T) {
	backing := &lostRaceBacking{MemStore: NewMemStore()}
	created, err := backing.Create(Bead{Title: "lost race", Type: "task"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	cache := NewCachingStoreForTest(backing, func(string, string, json.RawMessage) {
		t.Fatal("a lost race must not emit a bead.updated notification")
	})
	_, ok, err := cache.ClaimBead(context.Background(), created.ID, "worker-1")
	if err != nil {
		t.Fatalf("ClaimBead err = %v, want nil", err)
	}
	if ok {
		t.Fatal("ClaimBead ok = true, want false (lost race)")
	}
}

// emptyBeadClaimBacking claims successfully but returns a zero-valued bead, so
// CachingStore.ClaimBead must fall back to the cache-refreshed copy.
type emptyBeadClaimBacking struct {
	*MemStore
}

func (e *emptyBeadClaimBacking) ClaimBead(_ context.Context, id, actor string) (Bead, bool, error) {
	status := "in_progress"
	if err := e.Update(id, UpdateOpts{Status: &status, Assignee: &actor}); err != nil {
		return Bead{}, false, err
	}
	return Bead{}, true, nil // deliberately empty; cache-refreshed copy must win
}

// toggleGetBacking claims successfully but can be switched to fail Get, so a
// warm-cache claim whose post-claim refresh read fails exercises
// CachingStore.ClaimBead's cached-entry patch branch.
type toggleGetBacking struct {
	*MemStore
	failGet bool
}

func (b *toggleGetBacking) Get(id string) (Bead, error) {
	if b.failGet {
		return Bead{}, errors.New("backing get unavailable")
	}
	return b.MemStore.Get(id)
}

func (b *toggleGetBacking) ClaimBead(_ context.Context, id, actor string) (Bead, bool, error) {
	status := "in_progress"
	if err := b.Update(id, UpdateOpts{Status: &status, Assignee: &actor}); err != nil {
		return Bead{}, false, err
	}
	got, _ := b.MemStore.Get(id)
	return got, true, nil
}

func TestCachingStoreClaimBeadWarmCacheRefreshFailurePatchesCachedEntry(t *testing.T) {
	backing := &toggleGetBacking{MemStore: NewMemStore()}
	created, err := backing.Create(Bead{Title: "warm cache", Type: "task"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	cache := NewCachingStoreForTest(backing, nil)
	// A first successful claim warms the cache entry via the refresh path.
	if _, ok, err := cache.ClaimBead(context.Background(), created.ID, "worker-1"); err != nil || !ok {
		t.Fatalf("warming claim ok=%v err=%v, want true nil", ok, err)
	}
	// Now fail the post-claim refresh read: the idempotent same-actor re-claim
	// must patch the warm cached entry rather than lose the committed state.
	backing.failGet = true

	bead, ok, err := cache.ClaimBead(context.Background(), created.ID, "worker-1")
	if err != nil || !ok {
		t.Fatalf("ClaimBead ok=%v err=%v, want true nil", ok, err)
	}
	if bead.Assignee != "worker-1" || bead.Status != "in_progress" {
		t.Fatalf("bead = %+v, want worker-1 in_progress", bead)
	}
	// The cached entry must reflect the committed claim even though the refresh
	// read failed.
	backing.failGet = false
	got, err := cache.Get(created.ID)
	if err != nil {
		t.Fatalf("post-claim Get: %v", err)
	}
	if got.Assignee != "worker-1" || got.Status != "in_progress" {
		t.Fatalf("cached bead = %+v, want worker-1 in_progress", got)
	}
}

// claimOkReadFailStorage claims successfully but fails the canonical re-read, so
// NativeDoltStore.ClaimBead returns claimed=true with a surfaced reload error.
type claimOkReadFailStorage struct {
	beadslib.Storage
}

func (claimOkReadFailStorage) ClaimIssue(context.Context, string, string) error { return nil }

func (claimOkReadFailStorage) SearchIssues(context.Context, string, beadslib.IssueFilter) ([]*beadslib.Issue, error) {
	return nil, errors.New("search unavailable after claim")
}

func TestNativeDoltStoreClaimBeadReadFailureAfterClaim(t *testing.T) {
	store := newNativeDoltStoreForTest(claimOkReadFailStorage{})
	bead, ok, err := store.ClaimBead(context.Background(), "gc-9", "worker-1")
	if !ok {
		t.Fatal("ClaimBead ok = false, want true (claim committed despite read failure)")
	}
	if err == nil || !strings.Contains(err.Error(), "reloading claimed bead") {
		t.Fatalf("err = %v, want a reloading-claimed-bead error", err)
	}
	if bead.ID != "" {
		t.Fatalf("bead = %+v, want zero-valued when the reload failed", bead)
	}
}

func TestCachingStoreClaimBeadEmptyBackingBeadFallsBackToRefresh(t *testing.T) {
	mem := NewMemStore()
	created, err := mem.Create(Bead{Title: "empty-return", Type: "task"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	backing := &emptyBeadClaimBacking{MemStore: mem}
	cache := NewCachingStoreForTest(backing, nil)

	bead, ok, err := cache.ClaimBead(context.Background(), created.ID, "worker-1")
	if err != nil || !ok {
		t.Fatalf("ClaimBead ok=%v err=%v, want true nil", ok, err)
	}
	if bead.ID != created.ID || bead.Assignee != "worker-1" || bead.Status != "in_progress" {
		t.Fatalf("bead = %+v, want %s worker-1 in_progress (cache-refreshed)", bead, created.ID)
	}
}
