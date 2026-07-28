package beads

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// CachedReader is the cache-only eventual-consistency read handle for active
// beads. Get may return ErrNotFound for closed-but-existing beads because the
// cache does not retain complete closed history; use Live.Get for closed or
// historical lookups. List reads across both bead tiers regardless of the
// caller's TierMode; use the underlying Store directly for intentionally
// tier-scoped list queries.
type CachedReader interface {
	Get(id string) (Bead, error)
	List(query ListQuery) ([]Bead, error)
	Ready(query ...ReadyQuery) ([]Bead, error)
	DepList(id, direction string) ([]Dep, error)
}

// LiveReader is the authoritative read handle for beads. List reads across both
// bead tiers regardless of the caller's TierMode; use the underlying Store
// directly for intentionally tier-scoped list queries.
type LiveReader interface {
	Get(id string) (Bead, error)
	List(query ListQuery) ([]Bead, error)
	Ready(query ...ReadyQuery) ([]Bead, error)
	DepList(id, direction string) ([]Dep, error)
}

// Writer is the mutation handle for beads.
type Writer interface {
	Create(b Bead) (Bead, error)
	Update(id string, opts UpdateOpts) error
	Close(id string) error
	Reopen(id string) error
	CloseAll(ids []string, metadata map[string]string) (int, error)
	SetMetadata(id, key, value string) error
	SetMetadataBatch(id string, kvs map[string]string) error
	Delete(id string) error
	DepAdd(issueID, dependsOnID, depType string) error
	DepRemove(issueID, dependsOnID string) error
}

// StoreHandles groups explicit bead read and write capabilities.
type StoreHandles struct {
	Cached CachedReader
	Live   LiveReader
	Writer Writer
}

// HandlesFor returns explicit cached/live reader and writer handles for a
// store. Stores with a native handle implementation keep their stronger
// guarantees; plain stores use logical wrappers that hide tier selection from
// callers.
func HandlesFor(store Store) StoreHandles {
	if provider, ok := store.(interface {
		Handles() StoreHandles
	}); ok {
		return provider.Handles()
	}
	return StoreHandles{
		Cached: logicalCachedStoreReader{store: store},
		Live:   logicalLiveStoreReader{store: store},
		Writer: store,
	}
}

// Handles returns explicit cached/live reader and writer handles that share
// this store's cache coordinator.
func (c *CachingStore) Handles() StoreHandles {
	return StoreHandles{
		Cached: cachedStoreReader{store: c},
		Live:   liveStoreReader{store: c},
		Writer: c,
	}
}

type logicalCachedStoreReader struct {
	store Store
}

func (r logicalCachedStoreReader) Get(id string) (Bead, error) {
	return r.store.Get(id)
}

func (r logicalCachedStoreReader) List(query ListQuery) ([]Bead, error) {
	query.Live = false
	query.TierMode = TierBoth
	return r.store.List(query)
}

func (r logicalCachedStoreReader) Ready(query ...ReadyQuery) ([]Bead, error) {
	return r.store.Ready(query...)
}

func (r logicalCachedStoreReader) DepList(id, direction string) ([]Dep, error) {
	return r.store.DepList(id, direction)
}

type logicalLiveStoreReader struct {
	store Store
}

func (r logicalLiveStoreReader) Get(id string) (Bead, error) {
	return r.store.Get(id)
}

func (r logicalLiveStoreReader) List(query ListQuery) ([]Bead, error) {
	query.Live = true
	query.TierMode = TierBoth
	return r.store.List(query)
}

func (r logicalLiveStoreReader) Ready(query ...ReadyQuery) ([]Bead, error) {
	return r.store.Ready(query...)
}

func (r logicalLiveStoreReader) DepList(id, direction string) ([]Dep, error) {
	return r.store.DepList(id, direction)
}

type cachedStoreReader struct {
	store *CachingStore
}

func (r cachedStoreReader) Get(id string) (Bead, error) {
	if err := r.store.ensureFullPrime(context.Background()); err != nil {
		return Bead{}, err
	}
	return r.store.cachedGetOnly(id)
}

func (r cachedStoreReader) List(query ListQuery) ([]Bead, error) {
	rows, err := r.store.cachedListOnly(logicalCachedListQuery(query))
	if err == nil || !errors.Is(err, ErrCacheUnavailable) {
		return rows, err
	}
	if err := r.store.ensureFullPrime(context.Background()); err != nil {
		return nil, err
	}
	return r.store.cachedListOnly(logicalCachedListQuery(query))
}

func (r cachedStoreReader) Ready(query ...ReadyQuery) ([]Bead, error) {
	if err := r.store.ensureFullPrime(context.Background()); err != nil {
		return nil, err
	}
	return r.store.cachedReadyOnly(readyQueryFromArgs(query))
}

func (r cachedStoreReader) DepList(id, direction string) ([]Dep, error) {
	if err := r.store.ensureFullPrime(context.Background()); err != nil {
		return nil, err
	}
	return r.store.cachedDepListOnly(id, direction)
}

type liveStoreReader struct {
	store *CachingStore
}

func (r liveStoreReader) Get(id string) (Bead, error) {
	return r.store.backing.Get(id)
}

func (r liveStoreReader) List(query ListQuery) ([]Bead, error) {
	query.Live = true
	query.TierMode = TierBoth
	return r.store.backing.List(query)
}

func (r liveStoreReader) Ready(query ...ReadyQuery) ([]Bead, error) {
	return r.store.backing.Ready(query...)
}

func (r liveStoreReader) DepList(id, direction string) ([]Dep, error) {
	return r.store.backing.DepList(id, direction)
}

func logicalCachedListQuery(query ListQuery) ListQuery {
	query.Live = false
	query.TierMode = TierBoth
	return query
}

func (c *CachingStore) cachedGetOnly(id string) (Bead, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if _, deleted := c.deletedSeq[id]; deleted {
		return Bead{}, ErrNotFound
	}
	if _, dirty := c.dirty[id]; dirty {
		return Bead{}, fmt.Errorf("getting bead %q from cache: %w", id, ErrCacheUnavailable)
	}
	b, ok := c.beads[id]
	if !ok {
		return Bead{}, ErrNotFound
	}
	return cloneBead(b), nil
}

func (c *CachingStore) cachedListOnly(query ListQuery) ([]Bead, error) {
	if !query.HasFilter() && !query.AllowScan {
		return nil, fmt.Errorf("listing beads from cache: %w", ErrQueryRequiresScan)
	}
	if query.IncludesClosed() {
		return nil, fmt.Errorf("listing closed beads from cache: %w", ErrCacheUnavailable)
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	if (c.state != cacheLive && c.state != cachePartial) || c.primePartialErr != nil || len(c.dirty) > 0 {
		return nil, fmt.Errorf("listing beads from cache: %w", ErrCacheUnavailable)
	}
	rows := make([]Bead, 0, len(c.beads))
	for _, b := range c.beads {
		if !query.Matches(b) {
			continue
		}
		rows = append(rows, cloneBead(b))
	}
	sortBeadsForQuery(rows, query.Sort)
	if query.Limit > 0 && len(rows) > query.Limit {
		rows = rows[:query.Limit]
	}
	return rows, nil
}

func (c *CachingStore) cachedReadyOnly(query ReadyQuery) ([]Bead, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.cachedReadyLocked(query)
}

func (c *CachingStore) cachedReadyCompleteOnly(ctx context.Context, query ReadyQuery) ([]Bead, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	// A cache-only deadline-sensitive read must never wait behind a writer
	// after its caller has returned. A busy cache is a partial observation,
	// not a reason to abandon a goroutine on RLock.
	if !c.mu.TryRLock() {
		return nil, fmt.Errorf("reading complete ready projection from busy cache: %w", ErrCacheUnavailable)
	}
	if c.state != cacheLive || !c.depsComplete || c.primePartialErr != nil || len(c.dirty) > 0 {
		c.mu.RUnlock()
		return nil, fmt.Errorf("reading complete ready projection from cache: %w", ErrCacheUnavailable)
	}

	statusByID := make(map[string]string, len(c.beads))
	openBeads := make([]Bead, 0, len(c.beads))
	now := time.Now().UTC()
	for _, b := range c.beads {
		if err := ctx.Err(); err != nil {
			c.mu.RUnlock()
			return nil, err
		}
		statusByID[b.ID] = b.Status
		if !IsReadyCandidateForTier(b, now, query.TierMode) {
			continue
		}
		if query.Assignee != "" && b.Assignee != query.Assignee {
			continue
		}
		if !query.matchesExtra(b) {
			continue
		}
		openBeads = append(openBeads, cloneBead(b))
	}
	depsByID := make(map[string][]Dep, len(openBeads))
	for _, b := range openBeads {
		if err := ctx.Err(); err != nil {
			c.mu.RUnlock()
			return nil, err
		}
		depsByID[b.ID] = cloneDeps(c.deps[b.ID])
	}
	c.mu.RUnlock()

	// The maps above are a consistent snapshot, so sorting and dependency
	// evaluation need not hold the cache lock or delay writers.
	return cachedReadyRows(ctx, query, statusByID, openBeads, depsByID, true)
}

// cachedReadyPrimedOnly is the cache-only ready snapshot behind
// ReadyCachedContext. It accepts the partial (PrimeActive) state as well as
// live, but keeps every other strict-read guard from cachedReadyCompleteOnly:
// no backing I/O ever, TryRLock (a cache-only deadline-sensitive read must not
// wait behind a writer), a clean prime (primePartialErr nil), no dirty rows,
// ctx observed throughout, and a consistent copied snapshot released before
// the CPU-bound sort. PrimeActive's normalized open + in_progress snapshot
// contains every nonclosed dependency target, while dependency-edge coverage
// is proven per candidate: only deps rows actually present in the cache enter
// the snapshot, and the cache's real depsComplete flag is passed through, so a
// candidate with no cached deps row fails ErrCacheUnavailable in
// cachedReadyRows instead of being served as dependency-free.
func (c *CachingStore) cachedReadyPrimedOnly(ctx context.Context, query ReadyQuery) ([]Bead, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !c.mu.TryRLock() {
		return nil, fmt.Errorf("reading primed ready projection from busy cache: %w", ErrCacheUnavailable)
	}
	if (c.state != cacheLive && c.state != cachePartial) || c.primePartialErr != nil || len(c.dirty) > 0 {
		c.mu.RUnlock()
		return nil, fmt.Errorf("reading primed ready projection from cache: %w", ErrCacheUnavailable)
	}
	if c.state == cachePartial {
		if _, unsafe := c.backing.(partialReadyCacheUnsafe); unsafe {
			c.mu.RUnlock()
			return nil, fmt.Errorf("reading partial ready projection from unsafe backing: %w", ErrCacheUnavailable)
		}
	}

	depsComplete := c.depsComplete
	statusByID := make(map[string]string, len(c.beads))
	openBeads := make([]Bead, 0, len(c.beads))
	now := time.Now().UTC()
	for _, b := range c.beads {
		if err := ctx.Err(); err != nil {
			c.mu.RUnlock()
			return nil, err
		}
		statusByID[b.ID] = b.Status
		if !IsReadyCandidateForTier(b, now, query.TierMode) {
			continue
		}
		if query.Assignee != "" && b.Assignee != query.Assignee {
			continue
		}
		if !query.matchesExtra(b) {
			continue
		}
		openBeads = append(openBeads, cloneBead(b))
	}
	depsByID := make(map[string][]Dep, len(openBeads))
	for _, b := range openBeads {
		if err := ctx.Err(); err != nil {
			c.mu.RUnlock()
			return nil, err
		}
		// Preserve per-row presence: only a deps row the cache actually holds
		// enters the snapshot, so a candidate with unknown coverage fails
		// closed below rather than being treated as dependency-free.
		if deps, ok := c.deps[b.ID]; ok {
			depsByID[b.ID] = cloneDeps(deps)
		}
	}
	c.mu.RUnlock()

	// The maps above are a consistent snapshot, so sorting and dependency
	// evaluation need not hold the cache lock or delay writers.
	return cachedReadyRows(ctx, query, statusByID, openBeads, depsByID, depsComplete)
}

func (c *CachingStore) cachedReadyLocked(query ReadyQuery) ([]Bead, error) {
	if (c.state != cacheLive && c.state != cachePartial) || c.primePartialErr != nil || len(c.dirty) > 0 {
		return nil, fmt.Errorf("reading ready beads from cache: %w", ErrCacheUnavailable)
	}

	statusByID := make(map[string]string, len(c.beads))
	openBeads := make([]Bead, 0, len(c.beads))
	now := time.Now().UTC()
	for _, b := range c.beads {
		statusByID[b.ID] = b.Status
		if !IsReadyCandidateForTier(b, now, query.TierMode) {
			continue
		}
		if query.Assignee != "" && b.Assignee != query.Assignee {
			continue
		}
		if !query.matchesExtra(b) {
			continue
		}
		openBeads = append(openBeads, cloneBead(b))
	}
	return cachedReadyRows(context.Background(), query, statusByID, openBeads, c.deps, c.depsComplete)
}

func cachedReadyRows(
	ctx context.Context,
	query ReadyQuery,
	statusByID map[string]string,
	openBeads []Bead,
	depsByID map[string][]Dep,
	depsComplete bool,
) ([]Bead, error) {
	cancellable := ctx != nil && ctx.Done() != nil
	// Sort candidates before the limit-bounded loop below: the cache source is
	// a map, so without this a Limit cuts an arbitrary subset. The context-aware
	// path remains interruptible throughout this CPU work.
	switch {
	case query.isRouted():
		// The routed-pool tier is ordered oldest-first (created_asc), matching the
		// legacy `bd ready --sort oldest --limit 20` demand probe, NOT ready
		// priority order — so a filter-before-limit read cuts the oldest routed
		// candidates a worker would have claimed, regardless of priority. The
		// cancellation-aware form keeps the deadline-sensitive strict cache path
		// interruptible through this CPU work too.
		if err := sortBeadsCreatedAscContext(ctx, openBeads); err != nil {
			return nil, err
		}
	case !cancellable:
		sortBeadsReadyOrder(openBeads)
	default:
		if err := sortBeadsReadyOrderContext(ctx, openBeads); err != nil {
			return nil, err
		}
	}

	result := make([]Bead, 0, len(openBeads))
	for _, b := range openBeads {
		if cancellable {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
		}
		deps, ok := depsByID[b.ID]
		switch {
		case ok:
		case depsComplete:
			deps = nil
		default:
			return nil, fmt.Errorf("reading ready deps from cache: %w", ErrCacheUnavailable)
		}
		if !cachedBeadReady(b, statusByID, deps) {
			continue
		}
		result = append(result, cloneBead(b))
		if query.Limit > 0 && len(result) >= query.Limit {
			break
		}
	}
	return result, nil
}

func (c *CachingStore) cachedDepListOnly(id, direction string) ([]Dep, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if (c.state != cacheLive && c.state != cachePartial) || c.primePartialErr != nil || len(c.dirty) > 0 {
		return nil, fmt.Errorf("listing deps from cache: %w", ErrCacheUnavailable)
	}
	if direction == "" || direction == "down" {
		deps, ok := c.deps[id]
		if ok || c.depsComplete {
			return cloneDeps(deps), nil
		}
		return nil, fmt.Errorf("listing deps from cache: %w", ErrCacheUnavailable)
	}
	if direction != "up" {
		return nil, fmt.Errorf("listing deps from cache: unsupported direction %q", direction)
	}
	if !c.depsComplete {
		return nil, fmt.Errorf("listing reverse deps from cache: %w", ErrCacheUnavailable)
	}
	var deps []Dep
	for _, beadDeps := range c.deps {
		for _, dep := range beadDeps {
			if dep.DependsOnID == id {
				deps = append(deps, dep)
			}
		}
	}
	return cloneDeps(deps), nil
}
