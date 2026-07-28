package beads_test

import (
	"context"
	"errors"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/beadmeta"
	"github.com/gastownhall/gascity/internal/beads"
)

// These tests pin the P1-2 STRICT cache-only, FILTERED-before-limit routed-pool
// ready read that lets the hook fast path reproduce the generated default
// work_query's assigned-ready and routed-pool tiers over the controller cache
// without a per-worker SQL connection and without shipping the full ready set
// over the wire. The path under test is ContextReadyReader.ReadyContext — the
// only handle the API's bounded fast-path shape may use, because it answers
// purely from the complete active cache and fails closed (ErrCacheUnavailable)
// instead of priming or falling back to a live backing read. The cache must
// apply the assignee/unassigned/exclude-type/route filters BEFORE the limit so
// a routed candidate buried behind many non-matching ready beads is still
// found, and must order the routed tier oldest-first (not by ready priority).

const (
	routeTarget = "pool-x"
	otherTarget = "pool-y"
)

var readyFilterBase = time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)

// seededBead builds an open, unblocked bead with an explicit created-at offset
// so the oldest-first routed order is deterministic (MemStore.Create would
// overwrite CreatedAt, so the tests seed the backing directly).
func seededBead(id string, ageOffset time.Duration, meta map[string]string, mutate func(*beads.Bead)) beads.Bead {
	b := beads.Bead{
		ID:        id,
		Title:     id,
		Status:    "open",
		Type:      "task",
		CreatedAt: readyFilterBase.Add(ageOffset),
		Metadata:  beads.StringMap(meta),
	}
	if mutate != nil {
		mutate(&b)
	}
	return b
}

func primedCache(t *testing.T, backing beads.Store) *beads.CachingStore {
	t.Helper()
	cache := beads.NewCachingStoreForTest(backing, nil)
	// A FULL prime: ReadyContext's strict complete-cache contract requires the
	// live state with a complete dependency projection, which PrimeActive's
	// partial prime does not establish.
	if err := cache.Prime(context.Background()); err != nil {
		t.Fatalf("Prime: %v", err)
	}
	return cache
}

func cachedReady(t *testing.T, cache *beads.CachingStore, q beads.ReadyQuery) []beads.Bead {
	t.Helper()
	rows, err := cache.ReadyContext(context.Background(), q)
	if err != nil {
		t.Fatalf("ReadyContext(%+v): %v", q, err)
	}
	return rows
}

func idsOf(rows []beads.Bead) []string {
	out := make([]string, len(rows))
	for i, b := range rows {
		out[i] = b.ID
	}
	return out
}

// TestCachedReadyRoutedFilterBeforeLimit is the buried-routed-row guard: with a
// small Limit and many non-matching ready beads created BEFORE the one routed
// match, a naive limit-then-filter read would cut the match. Filtering before
// the limit must still surface it.
func TestCachedReadyRoutedFilterBeforeLimit(t *testing.T) {
	t.Parallel()
	var seed []beads.Bead
	// 30 unassigned, non-matching ready beads, all older than the one match.
	for i := 0; i < 30; i++ {
		seed = append(seed, seededBead(idFor(i), time.Duration(i)*time.Minute,
			map[string]string{beadmeta.RoutedToMetadataKey: otherTarget}, nil))
	}
	// The single routed match is the NEWEST bead, so any oldest-first read that
	// applied the limit before the route filter would never reach it.
	match := seededBead("gc-match", 999*time.Minute,
		map[string]string{beadmeta.RoutedToMetadataKey: routeTarget}, nil)
	seed = append(seed, match)

	cache := primedCache(t, beads.NewMemStoreFrom(len(seed), seed, nil))
	rows := cachedReady(t, cache, beads.ReadyQuery{
		Route: routeTarget, RouteMode: beads.RouteCanonical, Unassigned: true, ExcludeType: "epic", Limit: 20,
	})
	if got := idsOf(rows); len(got) != 1 || got[0] != "gc-match" {
		t.Fatalf("routed canonical read = %v, want [gc-match] (filter before limit)", got)
	}
}

// TestCachedReadyCanonicalMigrationDivergence pins canonical-vs-migration
// matching, including the divergence case: a bead carrying BOTH gc.run_target
// and a gc.routed_to must match canonical (by routed_to) and must NOT match a
// migration probe (whose contract requires gc.routed_to absent).
func TestCachedReadyCanonicalMigrationDivergence(t *testing.T) {
	t.Parallel()
	canonical := seededBead("gc-canon", 1*time.Minute, map[string]string{
		beadmeta.RoutedToMetadataKey: routeTarget,
	}, nil)
	migration := seededBead("gc-migr", 2*time.Minute, map[string]string{
		beadmeta.RunTargetMetadataKey: routeTarget,
		beadmeta.KindMetadataKey:      beadmeta.KindWorkflow,
	}, nil)
	// Divergent: run_target=target but already carries a (different) routed_to,
	// so the migration probe must skip it and canonical must match it only for
	// its own routed_to target, never for `routeTarget`.
	divergent := seededBead("gc-div", 3*time.Minute, map[string]string{
		beadmeta.RunTargetMetadataKey: routeTarget,
		beadmeta.KindMetadataKey:      beadmeta.KindWorkflow,
		beadmeta.RoutedToMetadataKey:  otherTarget,
	}, nil)
	seed := []beads.Bead{canonical, migration, divergent}
	cache := primedCache(t, beads.NewMemStoreFrom(len(seed), seed, nil))

	canon := idsOf(cachedReady(t, cache, beads.ReadyQuery{
		Route: routeTarget, RouteMode: beads.RouteCanonical, Unassigned: true, ExcludeType: "epic", Limit: 20,
	}))
	if len(canon) != 1 || canon[0] != "gc-canon" {
		t.Fatalf("canonical read = %v, want [gc-canon] only", canon)
	}
	migr := idsOf(cachedReady(t, cache, beads.ReadyQuery{
		Route: routeTarget, RouteMode: beads.RouteMigration, Unassigned: true, ExcludeType: "epic", Limit: 20,
	}))
	if len(migr) != 1 || migr[0] != "gc-migr" {
		t.Fatalf("migration read = %v, want [gc-migr] only (divergent gc-div excluded)", migr)
	}
}

// TestCachedReadyRoutedExcludesAssignedAndEpic pins the --unassigned and
// --exclude-type=epic filters: an assigned routed bead and an unassigned routed
// epic are both dropped from the routed-pool tier.
func TestCachedReadyRoutedExcludesAssignedAndEpic(t *testing.T) {
	t.Parallel()
	claimable := seededBead("gc-ok", 1*time.Minute, map[string]string{beadmeta.RoutedToMetadataKey: routeTarget}, nil)
	assigned := seededBead("gc-assigned", 2*time.Minute, map[string]string{beadmeta.RoutedToMetadataKey: routeTarget},
		func(b *beads.Bead) { b.Assignee = "someone" })
	epic := seededBead("gc-epic", 3*time.Minute, map[string]string{beadmeta.RoutedToMetadataKey: routeTarget},
		func(b *beads.Bead) { b.Type = "epic" })
	seed := []beads.Bead{claimable, assigned, epic}
	cache := primedCache(t, beads.NewMemStoreFrom(len(seed), seed, nil))

	got := idsOf(cachedReady(t, cache, beads.ReadyQuery{
		Route: routeTarget, RouteMode: beads.RouteCanonical, Unassigned: true, ExcludeType: "epic", Limit: 20,
	}))
	if len(got) != 1 || got[0] != "gc-ok" {
		t.Fatalf("routed read = %v, want [gc-ok] (assigned + epic dropped)", got)
	}
}

// TestCachedReadyRoutedOldestDespitePriority pins that the routed tier orders
// oldest-first (created_asc), NOT by ready priority: a higher-priority but
// newer match must sort after an older, lower-priority match.
func TestCachedReadyRoutedOldestDespitePriority(t *testing.T) {
	t.Parallel()
	hi := 0
	lo := 5
	older := seededBead("gc-older", 1*time.Minute, map[string]string{beadmeta.RoutedToMetadataKey: routeTarget},
		func(b *beads.Bead) { b.Priority = &lo }) // lower priority number = higher urgency in ready order
	newer := seededBead("gc-newer", 2*time.Minute, map[string]string{beadmeta.RoutedToMetadataKey: routeTarget},
		func(b *beads.Bead) { b.Priority = &hi })
	seed := []beads.Bead{newer, older}
	cache := primedCache(t, beads.NewMemStoreFrom(len(seed), seed, nil))

	got := idsOf(cachedReady(t, cache, beads.ReadyQuery{
		Route: routeTarget, RouteMode: beads.RouteCanonical, Unassigned: true, ExcludeType: "epic", Limit: 20,
	}))
	want := []string{"gc-older", "gc-newer"}
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("routed order = %v, want %v (oldest first, priority ignored)", got, want)
	}
}

// TestCachedReadyAssignedLimit1 pins the assigned-ready tier read: an assignee
// filter with Limit 1 returns exactly the one assigned-ready bead for that
// identity/alias, and no work assigned to another identity.
func TestCachedReadyAssignedLimit1(t *testing.T) {
	t.Parallel()
	mine1 := seededBead("gc-mine1", 1*time.Minute, nil, func(b *beads.Bead) { b.Assignee = "alias-1" })
	mine2 := seededBead("gc-mine2", 2*time.Minute, nil, func(b *beads.Bead) { b.Assignee = "alias-1" })
	theirs := seededBead("gc-theirs", 3*time.Minute, nil, func(b *beads.Bead) { b.Assignee = "alias-2" })
	seed := []beads.Bead{mine1, mine2, theirs}
	cache := primedCache(t, beads.NewMemStoreFrom(len(seed), seed, nil))

	got := idsOf(cachedReady(t, cache, beads.ReadyQuery{Assignee: "alias-1", Limit: 1}))
	if len(got) != 1 {
		t.Fatalf("assigned-ready read = %v, want exactly one bead", got)
	}
	if got[0] != "gc-mine1" && got[0] != "gc-mine2" {
		t.Fatalf("assigned-ready read = %v, want one of alias-1's beads", got)
	}
}

// readyCountingStore wraps a backing store and counts Ready calls so a test can
// prove the fast-path read is served purely from the controller cache.
type readyCountingStore struct {
	beads.Store
	readyCalls atomic.Int64
}

func (s *readyCountingStore) Ready(query ...beads.ReadyQuery) ([]beads.Bead, error) {
	s.readyCalls.Add(1)
	return s.Store.Ready(query...)
}

// TestCachedReadyServedFromCacheNoBackingRead pins that once the cache is warm,
// a routed/assigned ReadyContext read issues ZERO backing Ready reads — the core
// of the P1-2 cure: worker discovery must not open a per-hook SQL connection to
// the store.
func TestCachedReadyServedFromCacheNoBackingRead(t *testing.T) {
	t.Parallel()
	match := seededBead("gc-ok", 1*time.Minute, map[string]string{beadmeta.RoutedToMetadataKey: routeTarget}, nil)
	backing := &readyCountingStore{Store: beads.NewMemStoreFrom(1, []beads.Bead{match}, nil)}
	cache := primedCache(t, backing)
	// Ignore any priming-time reads; the cure is about the steady-state hot path.
	backing.readyCalls.Store(0)

	for i := 0; i < 5; i++ {
		_ = cachedReady(t, cache, beads.ReadyQuery{
			Route: routeTarget, RouteMode: beads.RouteCanonical, Unassigned: true, ExcludeType: "epic", Limit: 20,
		})
		_ = cachedReady(t, cache, beads.ReadyQuery{Assignee: "whoever", Limit: 1})
	}
	if n := backing.readyCalls.Load(); n != 0 {
		t.Fatalf("backing Ready called %d times, want 0 (fast-path read must be cache-served)", n)
	}
}

// TestCachedReadyRoutedTierMode pins ephemeral coverage: an ephemeral routed
// bead is invisible to the default (issues) tier and visible under TierBoth,
// matching the generated query's --include-ephemeral routed probe.
func TestCachedReadyRoutedTierMode(t *testing.T) {
	t.Parallel()
	wisp := seededBead("gc-wisp", 1*time.Minute, map[string]string{beadmeta.RoutedToMetadataKey: routeTarget},
		func(b *beads.Bead) { b.Ephemeral = true })
	seed := []beads.Bead{wisp}
	cache := primedCache(t, beads.NewMemStoreFrom(len(seed), seed, nil))

	issuesOnly := idsOf(cachedReady(t, cache, beads.ReadyQuery{
		Route: routeTarget, RouteMode: beads.RouteCanonical, Unassigned: true, ExcludeType: "epic", Limit: 20,
	}))
	if len(issuesOnly) != 0 {
		t.Fatalf("issues-tier routed read = %v, want empty (ephemeral hidden)", issuesOnly)
	}
	both := idsOf(cachedReady(t, cache, beads.ReadyQuery{
		Route: routeTarget, RouteMode: beads.RouteCanonical, Unassigned: true, ExcludeType: "epic",
		Limit: 20, TierMode: beads.TierBoth,
	}))
	if len(both) != 1 || both[0] != "gc-wisp" {
		t.Fatalf("both-tier routed read = %v, want [gc-wisp]", both)
	}
}

func idFor(i int) string {
	return "gc-n" + strconv.Itoa(i)
}

// TestReadyContextUnprimedFailsClosedNoBackingRead pins the fail-closed side of
// the strict contract: a cache that cannot answer (never primed) surfaces
// ErrCacheUnavailable and issues ZERO backing Ready reads — it must not prime
// behind the caller's back or silently switch to a live read.
func TestReadyContextUnprimedFailsClosedNoBackingRead(t *testing.T) {
	t.Parallel()
	match := seededBead("gc-ok", 1*time.Minute, map[string]string{beadmeta.RoutedToMetadataKey: routeTarget}, nil)
	backing := &readyCountingStore{Store: beads.NewMemStoreFrom(1, []beads.Bead{match}, nil)}
	cache := beads.NewCachingStoreForTest(backing, nil) // deliberately unprimed

	rows, err := cache.ReadyContext(context.Background(), beads.ReadyQuery{
		Route: routeTarget, RouteMode: beads.RouteCanonical, Unassigned: true, ExcludeType: "epic", Limit: 20,
	})
	if !errors.Is(err, beads.ErrCacheUnavailable) {
		t.Fatalf("ReadyContext on unprimed cache: err = %v, want ErrCacheUnavailable", err)
	}
	if len(rows) != 0 {
		t.Fatalf("ReadyContext on unprimed cache returned rows %v, want none", rows)
	}
	if n := backing.readyCalls.Load(); n != 0 {
		t.Fatalf("backing Ready called %d times, want 0 (fail closed, no live fallback)", n)
	}
}

// TestReadyCachedContextPrimeActiveOnlyServesBoundedReads pins the rollout
// unblock: a cache that has only completed PrimeActive (cachePartial, no full
// prime, depsComplete false globally but every primed active bead carrying its
// own deps row) must serve BOTH bounded fast-path shapes — assigned-ready and
// routed-pool — through ReadyCachedContext with ZERO backing Ready reads,
// while the stricter ReadyContext keeps failing closed until the full prime
// lands. This is the first-worker-hook-after-restart window on a paused rig.
func TestReadyCachedContextPrimeActiveOnlyServesBoundedReads(t *testing.T) {
	t.Parallel()
	assigned := seededBead("gc-mine", 1*time.Minute, nil, func(b *beads.Bead) { b.Assignee = "alias-1" })
	routed := seededBead("gc-routed", 2*time.Minute, map[string]string{beadmeta.RoutedToMetadataKey: routeTarget}, nil)
	backing := &readyCountingStore{Store: beads.NewMemStoreFrom(2, []beads.Bead{assigned, routed}, nil)}
	cache := beads.NewCachingStoreForTest(backing, nil)
	if err := cache.PrimeActive(); err != nil {
		t.Fatalf("PrimeActive: %v", err)
	}
	backing.readyCalls.Store(0)

	got, err := cache.ReadyCachedContext(context.Background(), beads.ReadyQuery{Assignee: "alias-1", Limit: 1})
	if err != nil {
		t.Fatalf("ReadyCachedContext(assigned) on PrimeActive-only cache: %v", err)
	}
	if ids := idsOf(got); len(ids) != 1 || ids[0] != "gc-mine" {
		t.Fatalf("assigned read = %v, want [gc-mine]", ids)
	}
	got, err = cache.ReadyCachedContext(context.Background(), beads.ReadyQuery{
		Route: routeTarget, RouteMode: beads.RouteCanonical, Unassigned: true, ExcludeType: "epic", Limit: 20,
	})
	if err != nil {
		t.Fatalf("ReadyCachedContext(routed) on PrimeActive-only cache: %v", err)
	}
	if ids := idsOf(got); len(ids) != 1 || ids[0] != "gc-routed" {
		t.Fatalf("routed read = %v, want [gc-routed]", ids)
	}
	if n := backing.readyCalls.Load(); n != 0 {
		t.Fatalf("backing Ready called %d times, want 0 (cache-only capability must never touch backing)", n)
	}

	// The stricter full-projection capability is unchanged: PrimeActive alone is
	// not a live, dependency-complete cache, so ReadyContext keeps failing closed.
	if _, err := cache.ReadyContext(context.Background(), beads.ReadyQuery{Assignee: "alias-1", Limit: 1}); !errors.Is(err, beads.ErrCacheUnavailable) {
		t.Fatalf("ReadyContext on PrimeActive-only cache: err = %v, want ErrCacheUnavailable", err)
	}
}

// TestReadyCachedContextUnprimedFailsClosedNoBackingRead pins the fail-closed
// side of the cache-only capability: an unprimed cache surfaces
// ErrCacheUnavailable with ZERO backing Ready reads — it never primes and never
// falls back to a live read.
func TestReadyCachedContextUnprimedFailsClosedNoBackingRead(t *testing.T) {
	t.Parallel()
	match := seededBead("gc-ok", 1*time.Minute, map[string]string{beadmeta.RoutedToMetadataKey: routeTarget}, nil)
	backing := &readyCountingStore{Store: beads.NewMemStoreFrom(1, []beads.Bead{match}, nil)}
	cache := beads.NewCachingStoreForTest(backing, nil) // deliberately unprimed

	rows, err := cache.ReadyCachedContext(context.Background(), beads.ReadyQuery{
		Route: routeTarget, RouteMode: beads.RouteCanonical, Unassigned: true, ExcludeType: "epic", Limit: 20,
	})
	if !errors.Is(err, beads.ErrCacheUnavailable) {
		t.Fatalf("ReadyCachedContext on unprimed cache: err = %v, want ErrCacheUnavailable", err)
	}
	if len(rows) != 0 {
		t.Fatalf("ReadyCachedContext on unprimed cache returned rows %v, want none", rows)
	}
	if n := backing.readyCalls.Load(); n != 0 {
		t.Fatalf("backing Ready called %d times, want 0 (fail closed, no prime, no live fallback)", n)
	}
}

// TestReadyCachedContextCancelled pins that the cache-only capability observes
// cancellation like ReadyContext does.
func TestReadyCachedContextCancelled(t *testing.T) {
	t.Parallel()
	var seed []beads.Bead
	for i := 0; i < 50; i++ {
		seed = append(seed, seededBead(idFor(i), time.Duration(i)*time.Minute,
			map[string]string{beadmeta.RoutedToMetadataKey: routeTarget}, nil))
	}
	cache := primedCache(t, beads.NewMemStoreFrom(len(seed), seed, nil))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	rows, err := cache.ReadyCachedContext(ctx, beads.ReadyQuery{
		Route: routeTarget, RouteMode: beads.RouteCanonical, Unassigned: true, ExcludeType: "epic", Limit: 20,
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("ReadyCachedContext with canceled ctx: err = %v, want context.Canceled", err)
	}
	if len(rows) != 0 {
		t.Fatalf("canceled cache-only read returned rows %v, want none", rows)
	}
}

// TestReadyContextRoutedCancelled pins that a routed strict read observes
// cancellation: a canceled context returns the context error, not rows.
func TestReadyContextRoutedCancelled(t *testing.T) {
	t.Parallel()
	var seed []beads.Bead
	for i := 0; i < 50; i++ {
		seed = append(seed, seededBead(idFor(i), time.Duration(i)*time.Minute,
			map[string]string{beadmeta.RoutedToMetadataKey: routeTarget}, nil))
	}
	cache := primedCache(t, beads.NewMemStoreFrom(len(seed), seed, nil))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	rows, err := cache.ReadyContext(ctx, beads.ReadyQuery{
		Route: routeTarget, RouteMode: beads.RouteCanonical, Unassigned: true, ExcludeType: "epic", Limit: 20,
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("ReadyContext with canceled ctx: err = %v, want context.Canceled", err)
	}
	if len(rows) != 0 {
		t.Fatalf("canceled routed read returned rows %v, want none", rows)
	}
}
