package beads

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/testutil"
	beadslib "github.com/steveyegge/beads"
)

type observedErrContext struct {
	context.Context
	once    sync.Once
	checked chan struct{}
}

type cancelOnErrCheckContext struct {
	context.Context
	cancel   context.CancelFunc
	cancelAt int64
	checks   atomic.Int64
}

func (c *cancelOnErrCheckContext) Err() error {
	if c.checks.Add(1) >= c.cancelAt {
		c.cancel()
	}
	return c.Context.Err()
}

type countingErrContext struct {
	context.Context
	checks atomic.Int64
}

func (c *countingErrContext) Err() error {
	c.checks.Add(1)
	return c.Context.Err()
}

func TestCachingStoreCountContextCancelsWhileWaitingForLock(t *testing.T) {
	store := NewCachingStoreForTest(NewMemStore(), nil)
	store.mu.Lock()
	locked := true
	defer func() {
		if locked {
			store.mu.Unlock()
		}
	}()

	base, cancel := context.WithCancel(context.Background())
	ctx := &observedErrContext{Context: base, checked: make(chan struct{})}
	done := make(chan error, 1)
	go func() {
		_, err := store.Count(ctx, ListQuery{Status: "open"})
		done <- err
	}()

	select {
	case <-ctx.checked:
	case <-time.After(testutil.GoroutineRaceTimeout):
		store.mu.Unlock()
		locked = false
		select {
		case <-done:
		case <-time.After(testutil.GoroutineRaceTimeout):
		}
		t.Fatal("Count did not check context before waiting for the cache lock")
	}
	cancel()

	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Count error = %v, want context.Canceled", err)
		}
	case <-time.After(testutil.GoroutineRaceTimeout):
		store.mu.Unlock()
		locked = false
		select {
		case <-done:
		case <-time.After(testutil.GoroutineRaceTimeout):
		}
		t.Fatal("Count waited for the cache lock after context cancellation")
	}
}

func TestSortBeadsReadyOrderContextStopsAfterCancellation(t *testing.T) {
	rows := make([]Bead, 128)
	for i := range rows {
		priority := len(rows) - i
		rows[i] = Bead{ID: fmt.Sprintf("gc-%03d", i), Priority: &priority}
	}
	base, cancel := context.WithCancel(context.Background())
	defer cancel()
	ctx := &cancelOnErrCheckContext{Context: base, cancel: cancel, cancelAt: 8}

	err := sortBeadsReadyOrderContext(ctx, rows)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("sortBeadsReadyOrderContext error = %v, want context.Canceled", err)
	}
	if checks := ctx.checks.Load(); checks < ctx.cancelAt {
		t.Fatalf("context checks = %d, want at least %d", checks, ctx.cancelAt)
	}
	select {
	case <-ctx.Done():
	default:
		t.Fatal("context returned cancellation without closing Done")
	}
}

func TestSortBeadsReadyOrderBackgroundUsesNonCancellableFastPath(t *testing.T) {
	rows := make([]Bead, 128)
	for i := range rows {
		priority := len(rows) - i
		rows[i] = Bead{ID: fmt.Sprintf("gc-%03d", i), Priority: &priority}
	}
	ctx := &countingErrContext{Context: context.Background()}

	if err := sortBeadsReadyOrderContext(ctx, rows); err != nil {
		t.Fatalf("sortBeadsReadyOrderContext: %v", err)
	}
	if checks := ctx.checks.Load(); checks != 0 {
		t.Fatalf("uncancellable context checks = %d, want 0", checks)
	}
	for i := 1; i < len(rows); i++ {
		if beadReadyLess(rows[i], rows[i-1]) {
			t.Fatalf("rows are not sorted at index %d: %+v before %+v", i, rows[i-1], rows[i])
		}
	}
}

func TestCachedReadyRowsBackgroundUsesCanonicalOrderWithoutErrChecks(t *testing.T) {
	priorityZero, priorityOne := 0, 1
	created := time.Date(2026, time.July, 15, 9, 0, 0, 0, time.UTC)
	openBeads := []Bead{
		{ID: "gc-c", Status: "open", Priority: &priorityOne, CreatedAt: created},
		{ID: "gc-b", Status: "open", Priority: &priorityZero, CreatedAt: created.Add(time.Minute)},
		{ID: "gc-a", Status: "open", Priority: &priorityZero, CreatedAt: created},
	}
	statusByID := map[string]string{"gc-a": "open", "gc-b": "open", "gc-c": "open"}
	ctx := &countingErrContext{Context: context.Background()}

	rows, err := cachedReadyRows(ctx, ReadyQuery{Limit: 2}, statusByID, openBeads, nil, true)
	if err != nil {
		t.Fatalf("cachedReadyRows: %v", err)
	}
	gotIDs := make([]string, len(rows))
	for i := range rows {
		gotIDs[i] = rows[i].ID
	}
	if len(gotIDs) != 2 || gotIDs[0] != "gc-a" || gotIDs[1] != "gc-b" {
		t.Fatalf("cachedReadyRows IDs = %v, want [gc-a gc-b]", gotIDs)
	}
	if checks := ctx.checks.Load(); checks != 0 {
		t.Fatalf("uncancellable context checks = %d, want 0", checks)
	}
}

func TestMemStoreReadyLockedSkipsChecksForUncancellableContext(t *testing.T) {
	store := NewMemStore()
	bead, err := store.Create(Bead{Title: "ready"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	ctx := &countingErrContext{Context: context.Background()}

	store.mu.Lock()
	rows, err := store.readyLocked(ctx, ReadyQuery{})
	store.mu.Unlock()
	if err != nil {
		t.Fatalf("readyLocked: %v", err)
	}
	if len(rows) != 1 || rows[0].ID != bead.ID {
		t.Fatalf("readyLocked rows = %+v, want %s", rows, bead.ID)
	}
	if checks := ctx.checks.Load(); checks != 0 {
		t.Fatalf("uncancellable context checks = %d, want 0", checks)
	}
}

func TestMemStoreReadyLockedStopsDuringCancellableScan(t *testing.T) {
	store := NewMemStore()
	for i := 0; i < 32; i++ {
		if _, err := store.Create(Bead{Title: fmt.Sprintf("ready-%02d", i)}); err != nil {
			t.Fatalf("Create bead %d: %v", i, err)
		}
	}
	base, cancel := context.WithCancel(context.Background())
	defer cancel()
	ctx := &cancelOnErrCheckContext{Context: base, cancel: cancel, cancelAt: 8}

	store.mu.Lock()
	rows, err := store.readyLocked(ctx, ReadyQuery{})
	store.mu.Unlock()
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("readyLocked error = %v, want context.Canceled (rows = %d)", err, len(rows))
	}
	if checks := ctx.checks.Load(); checks < ctx.cancelAt {
		t.Fatalf("context checks = %d, want at least %d", checks, ctx.cancelAt)
	}
	select {
	case <-ctx.Done():
	default:
		t.Fatal("context returned cancellation without closing Done")
	}
}

func (c *observedErrContext) Err() error {
	err := c.Context.Err()
	c.once.Do(func() { close(c.checked) })
	return err
}

func TestMemStoreReadyContextCancelsWhileWaitingForLock(t *testing.T) {
	store := NewMemStore()
	store.mu.Lock()
	locked := true
	defer func() {
		if locked {
			store.mu.Unlock()
		}
	}()

	base, cancel := context.WithCancel(context.Background())
	ctx := &observedErrContext{Context: base, checked: make(chan struct{})}
	done := make(chan error, 1)
	go func() {
		_, err := store.ReadyContext(ctx)
		done <- err
	}()
	select {
	case <-ctx.checked: // the first pre-lock context check observed an active context
	case <-time.After(testutil.GoroutineRaceTimeout):
		store.mu.Unlock()
		locked = false
		select {
		case <-done:
		case <-time.After(testutil.GoroutineRaceTimeout):
		}
		t.Fatal("ReadyContext did not check context before waiting for the lock")
	}
	cancel()

	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("ReadyContext error = %v, want context.Canceled", err)
		}
	case <-time.After(testutil.GoroutineRaceTimeout):
		store.mu.Unlock()
		locked = false
		select {
		case <-done:
		case <-time.After(testutil.GoroutineRaceTimeout):
		}
		t.Fatal("ReadyContext waited for the lock after context cancellation")
	}
}

func TestFileStoreReadyContextReportsUnsupported(t *testing.T) {
	store := &FileStore{MemStore: NewMemStore()}
	rows, err := store.ReadyContext(context.Background())
	if !errors.Is(err, ErrReadyContextUnsupported) {
		t.Fatalf("ReadyContext error = %v, want ErrReadyContextUnsupported", err)
	}
	if len(rows) != 0 {
		t.Fatalf("ReadyContext rows = %+v, want none for context-blind file refresh", rows)
	}
}

// countingReadyBacking counts backing Ready reads so the cache-only capability
// tests can prove ReadyCachedContext never touches the backing store, even on
// failure.
type countingReadyBacking struct {
	Store
	readyCalls atomic.Int64
}

func (s *countingReadyBacking) Ready(query ...ReadyQuery) ([]Bead, error) {
	s.readyCalls.Add(1)
	return s.Store.Ready(query...)
}

type partialReadyUnsafeBacking struct {
	*MemStore
}

func (s *partialReadyUnsafeBacking) partialReadyCacheUnsafe() {}

// TestReadyCachedContextPartialCacheMissingDepRowFailsClosed pins the
// per-candidate coverage proof: a partial (PrimeActive) cache serves a bounded
// cache-only read only while every candidate has a cached deps row. Once a
// candidate's deps row is missing, the read must fail ErrCacheUnavailable —
// never serve the candidate as dependency-free, never fall back to a backing
// Ready read.
func TestReadyCachedContextPartialCacheMissingDepRowFailsClosed(t *testing.T) {
	created := time.Date(2026, time.July, 27, 12, 0, 0, 0, time.UTC)
	seed := []Bead{
		{ID: "gc-a", Title: "a", Status: "open", Type: "task", Assignee: "alias-1", CreatedAt: created},
	}
	backing := &countingReadyBacking{Store: NewMemStoreFrom(len(seed), seed, nil)}
	cache := NewCachingStoreForTest(backing, nil)
	if err := cache.PrimeActive(); err != nil {
		t.Fatalf("PrimeActive: %v", err)
	}
	cache.mu.RLock()
	state, depsComplete := cache.state, cache.depsComplete
	cache.mu.RUnlock()
	if state != cachePartial {
		t.Fatalf("cache state = %v, want cachePartial after PrimeActive", state)
	}
	if depsComplete {
		t.Fatal("depsComplete = true after PrimeActive; fixture must exercise the per-row coverage path")
	}
	backing.readyCalls.Store(0)

	query := ReadyQuery{Assignee: "alias-1", Limit: 1}
	rows, err := cache.ReadyCachedContext(context.Background(), query)
	if err != nil || len(rows) != 1 || rows[0].ID != "gc-a" {
		t.Fatalf("ReadyCachedContext with full coverage = (%v, %v), want ([gc-a], nil)", rows, err)
	}

	// Remove the candidate's deps row: its dependency coverage is now unknown.
	cache.mu.Lock()
	delete(cache.deps, "gc-a")
	cache.mu.Unlock()

	rows, err = cache.ReadyCachedContext(context.Background(), query)
	if !errors.Is(err, ErrCacheUnavailable) {
		t.Fatalf("ReadyCachedContext with missing dep row: err = %v, want ErrCacheUnavailable", err)
	}
	if len(rows) != 0 {
		t.Fatalf("ReadyCachedContext with missing dep row returned rows %v, want none", rows)
	}
	if n := backing.readyCalls.Load(); n != 0 {
		t.Fatalf("backing Ready called %d times, want 0 (fail closed without backing I/O)", n)
	}
}

// TestReadyCachedContextPrimeActiveNativeBlockedDependency pins the managed
// NativeDoltStore invariant behind partial-cache readiness. Gas City's
// normalized "open" list means every upstream nonterminal status other than
// in_progress, so PrimeActive's indexed open + in_progress reads contain the
// complete nonclosed target-status set. A raw upstream blocked target must
// therefore remain present as normalized open and keep its dependent out of
// the cache-only ready result.
func TestReadyCachedContextPrimeActiveNativeBlockedDependency(t *testing.T) {
	created := time.Date(2026, time.July, 27, 12, 0, 0, 0, time.UTC)
	issues := []*beadslib.Issue{
		{
			ID:        "gc-candidate",
			Title:     "candidate",
			Status:    beadslib.StatusOpen,
			IssueType: beadslib.TypeTask,
			Priority:  2,
			CreatedAt: created,
			Assignee:  "alias-1",
			Dependencies: []*beadslib.Dependency{{
				IssueID:     "gc-candidate",
				DependsOnID: "gc-blocker",
				Type:        beadslib.DepBlocks,
			}},
		},
		{
			ID:        "gc-blocker",
			Title:     "blocked target",
			Status:    beadslib.StatusBlocked,
			IssueType: beadslib.TypeTask,
			Priority:  2,
			CreatedAt: created.Add(-time.Minute),
		},
	}
	storage := &nativeDoltStorageSpy{
		searchIssues: func(_ context.Context, _ string, filter beadslib.IssueFilter) ([]*beadslib.Issue, error) {
			return filterNativeIssuesForTest(issues, filter), nil
		},
	}
	cache := NewCachingStoreForTest(newNativeDoltStoreForTest(storage), nil)
	if err := cache.PrimeActive(); err != nil {
		t.Fatalf("PrimeActive: %v", err)
	}

	cache.mu.RLock()
	blocker, ok := cache.beads["gc-blocker"]
	cache.mu.RUnlock()
	if !ok || blocker.Status != "open" {
		t.Fatalf("normalized blocked target = (%+v, %v), want cached status open", blocker, ok)
	}

	rows, err := cache.ReadyCachedContext(context.Background(), ReadyQuery{Assignee: "alias-1", Limit: 1})
	if err != nil {
		t.Fatalf("ReadyCachedContext: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("ReadyCachedContext returned blocked dependent %+v, want none", rows)
	}
}

// TestReadyCachedContextPartialUnsafeBackingFailsClosed pins the guard for
// backings such as DoltLite whose partial status scan cannot prove every
// nonclosed dependency target. The same backing is eligible once a full prime
// establishes the globally complete projection.
func TestReadyCachedContextPartialUnsafeBackingFailsClosed(t *testing.T) {
	seed := []Bead{{
		ID: "gc-a", Title: "a", Status: "open", Type: "task", Assignee: "alias-1",
		CreatedAt: time.Date(2026, time.July, 27, 12, 0, 0, 0, time.UTC),
	}}
	cache := NewCachingStoreForTest(&partialReadyUnsafeBacking{
		MemStore: NewMemStoreFrom(len(seed), seed, nil),
	}, nil)
	query := ReadyQuery{Assignee: "alias-1", Limit: 1}

	if err := cache.PrimeActive(); err != nil {
		t.Fatalf("PrimeActive: %v", err)
	}
	if _, err := cache.ReadyCachedContext(context.Background(), query); !errors.Is(err, ErrCacheUnavailable) {
		t.Fatalf("ReadyCachedContext after partial prime: err = %v, want ErrCacheUnavailable", err)
	}

	if err := cache.Prime(context.Background()); err != nil {
		t.Fatalf("Prime: %v", err)
	}
	rows, err := cache.ReadyCachedContext(context.Background(), query)
	if err != nil || len(rows) != 1 || rows[0].ID != "gc-a" {
		t.Fatalf("ReadyCachedContext after full prime = (%v, %v), want ([gc-a], nil)", rows, err)
	}
}

// TestReadyCachedContextDeclinesBusyOrDirtyPartialCache pins the remaining
// strict-read guards on the cache-only capability: a write-locked (busy) cache
// and a dirty row both decline with ErrCacheUnavailable instead of waiting or
// serving a possibly-stale candidate.
func TestReadyCachedContextDeclinesBusyOrDirtyPartialCache(t *testing.T) {
	created := time.Date(2026, time.July, 27, 12, 0, 0, 0, time.UTC)
	seed := []Bead{
		{ID: "gc-a", Title: "a", Status: "open", Type: "task", Assignee: "alias-1", CreatedAt: created},
	}
	cache := NewCachingStoreForTest(NewMemStoreFrom(len(seed), seed, nil), nil)
	if err := cache.PrimeActive(); err != nil {
		t.Fatalf("PrimeActive: %v", err)
	}
	query := ReadyQuery{Assignee: "alias-1", Limit: 1}

	cache.mu.Lock()
	_, err := cache.ReadyCachedContext(context.Background(), query)
	cache.mu.Unlock()
	if !errors.Is(err, ErrCacheUnavailable) {
		t.Fatalf("ReadyCachedContext against a busy cache: err = %v, want ErrCacheUnavailable (TryRLock, no wait)", err)
	}

	cache.mu.Lock()
	cache.markDirtyLocked("gc-a")
	cache.mu.Unlock()
	if _, err := cache.ReadyCachedContext(context.Background(), query); !errors.Is(err, ErrCacheUnavailable) {
		t.Fatalf("ReadyCachedContext with a dirty row: err = %v, want ErrCacheUnavailable", err)
	}
}

// TestSortBeadsCreatedAscContextCancelsMidSort pins that the routed-pool
// oldest-first sort observes cancellation DURING the sort itself, not only at
// entry: the context cancels on a later Err observation, once merge work is
// already underway, and the sort must surface that instead of finishing an
// uninterruptible pass.
func TestSortBeadsCreatedAscContextCancelsMidSort(t *testing.T) {
	t.Parallel()
	base := time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)
	items := make([]Bead, 0, 64)
	for i := 0; i < 64; i++ {
		// Reverse creation order forces real merge work in every pass.
		items = append(items, Bead{ID: fmt.Sprintf("gc-%03d", i), CreatedAt: base.Add(-time.Duration(i) * time.Minute)})
	}
	parent, cancel := context.WithCancel(context.Background())
	defer cancel()
	ctx := &cancelOnErrCheckContext{Context: parent, cancel: cancel, cancelAt: 5}
	err := sortBeadsCreatedAscContext(ctx, items)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("sortBeadsCreatedAscContext = %v, want context.Canceled surfaced mid-sort", err)
	}
}

// TestSortBeadsCreatedAscContextMatchesQueryOrder pins the happy path: a
// cancellable but never-canceled context yields the exact (created_at, id)
// ascending order that sortBeadsForQuery(SortCreatedAsc) produces, so the
// cancellation-aware routed sort cuts the same deterministic oldest-first
// prefix as the uncancellable one.
func TestSortBeadsCreatedAscContextMatchesQueryOrder(t *testing.T) {
	t.Parallel()
	base := time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)
	var items []Bead
	for i := 0; i < 33; i++ {
		items = append(items, Bead{ID: fmt.Sprintf("gc-%03d", i), CreatedAt: base.Add(-time.Duration(i%7) * time.Minute)})
	}
	want := append([]Bead(nil), items...)
	sortBeadsForQuery(want, SortCreatedAsc)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := sortBeadsCreatedAscContext(ctx, items); err != nil {
		t.Fatalf("sortBeadsCreatedAscContext: %v", err)
	}
	for i := range want {
		if items[i].ID != want[i].ID {
			t.Fatalf("order[%d] = %s, want %s (must match SortCreatedAsc)", i, items[i].ID, want[i].ID)
		}
	}
}
