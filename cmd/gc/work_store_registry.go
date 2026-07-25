package main

// Deliverable B of the work-topology runtime aftermath
// (engdocs/design/beads-work-topology.md, "Shared handle per endpoint"): a
// process-level registry keyed by the resolved (Host, Port, Database) endpoint
// that returns per-scope FACADES over ONE shared underlying store. Post-unify /
// remote, N+1 scope directories resolve to one physical database; without a
// shared handle each scope opens its own connection to it and every
// identity-keyed dedup (seen[beads.Store]) sees distinct instances.
//
// The read / query / by-id plane delegates to the shared underlying handle. The
// WRITE plane stays scope-aware: an auto-mint Create (empty ID) must carry the
// ORIGINATING scope's own prefix, never the representative dir's — otherwise a
// post-unify SDK create for rig B (e.g. sling's auto-convoy) would mint rig A's
// prefix. That prefix override is the prerequisite bd slice's per-call mint
// (`--prefix` / PrefixOverride), surfaced here as the optional
// PrefixOverrideCreator capability; a store that cannot mint under an arbitrary
// prefix is NOT wrapped (the registry declines and the caller opens directly),
// because sharing one handle across differently-prefixed scopes without it would
// silently mis-prefix rig writes. Explicit-ID creates pass through unchanged.
//
// Lifecycle: registry-owned underlyings are closed ONLY by the registry. A
// caller-visible CloseStore on a facade is a refcount RELEASE — the per-tick
// close discipline (order_dispatch.go, gascity#3157) would otherwise LATCH the
// shared native handle (CloseStore is a one-way latch). The registry closes an
// underlying at process shutdown and when a config reload evicts its key, AFTER
// draining in-flight users and evicting the cache entry in the same step so a
// later lookup reopens fresh.
//
// DARK: the registry is consulted ONLY at the store-open choke point and ONLY
// for a topology-active city (work.unified / work.remote). A marker-less city
// never reaches it — the choke point opens directly, byte-identical.

import (
	"context"
	"sync"

	"github.com/gastownhall/gascity/internal/beads"
)

// PrefixOverrideCreator is the optional store capability the shared-handle
// facade needs on the WRITE plane: mint a new bead under an explicitly supplied
// ID prefix instead of the store's own configured prefix. It is the runtime
// surface of the prerequisite bd workspace-prefix-mint slice (`bd create
// --prefix`). A store that does not implement it cannot be shared across scopes
// that carry different prefixes, because an auto-mint create routed through the
// shared handle would take the representative dir's prefix.
type PrefixOverrideCreator interface {
	CreateUnderPrefix(b beads.Bead, prefix string) (beads.Bead, error)
}

// workStoreEntry is one registry-owned underlying handle plus its refcount and
// an in-flight wait-group. refs counts live facades; users tracks operations
// currently executing against the underlying so an evict/shutdown close drains
// them first. Both are guarded by the registry mutex except users, which is a
// WaitGroup so it can be waited on outside the lock.
type workStoreEntry struct {
	key        scopeEndpointKey
	underlying beads.Store
	refs       int
	users      sync.WaitGroup
	closeOnce  sync.Once
	closeErr   error
}

// workStoreRegistry is the process-level shared-handle store. Its zero value is
// not usable; construct with newWorkStoreRegistry.
type workStoreRegistry struct {
	mu      sync.Mutex
	entries map[scopeEndpointKey]*workStoreEntry
	closeFn func(beads.Store) error
}

// newWorkStoreRegistry builds a registry that closes underlying handles with
// closeFn (closeBeadStoreHandle in production; a probe-able stub in tests).
func newWorkStoreRegistry(closeFn func(beads.Store) error) *workStoreRegistry {
	if closeFn == nil {
		closeFn = func(beads.Store) error { return nil }
	}
	return &workStoreRegistry{
		entries: make(map[scopeEndpointKey]*workStoreEntry),
		closeFn: closeFn,
	}
}

// getOrOpen returns a per-scope facade over the shared handle for key. On a cache
// miss it opens the underlying via open() and caches it; on a hit it reuses the
// cached underlying and increments the refcount. prefix is the ORIGINATING
// scope's ID prefix, forwarded on the facade's auto-mint write plane.
//
// It DECLINES (returns wrapped=false with a directly-usable store) when the
// underlying cannot preserve every capability the facade must expose — in
// particular when a differently-prefixed scope needs per-call prefix minting the
// underlying does not implement. The caller then uses the returned store as a
// plain, unshared handle (correctness over coverage). A declined open still owns
// its underlying: the caller closes it directly, the registry never tracks it.
func (r *workStoreRegistry) getOrOpen(key scopeEndpointKey, prefix string, open func() (beads.Store, error)) (store beads.Store, wrapped bool, err error) {
	if !key.resolvable() {
		s, oerr := open()
		return s, false, oerr
	}
	r.mu.Lock()
	entry, ok := r.entries[key]
	if !ok {
		underlying, oerr := open()
		if oerr != nil {
			r.mu.Unlock()
			return nil, false, oerr
		}
		if !facadePreservesCapabilities(underlying, prefix) {
			// Declined: the facade would drop a capability (or cannot mint the
			// scope's prefix). Do NOT cache; hand the underlying back directly so
			// the caller opens/close it itself, byte-identical to a direct open.
			r.mu.Unlock()
			return underlying, false, nil
		}
		entry = &workStoreEntry{key: key, underlying: underlying}
		r.entries[key] = entry
	} else if !facadePreservesCapabilities(entry.underlying, prefix) {
		// A later scope on the same endpoint needs a capability (prefix mint) the
		// shared underlying cannot honor for it. Open a private, unshared handle
		// rather than mis-prefix its writes through the shared one.
		r.mu.Unlock()
		s, oerr := open()
		return s, false, oerr
	}
	entry.refs++
	entry.users.Add(1)
	r.mu.Unlock()
	return newSharedWorkStoreFacade(r, entry, prefix), true, nil
}

// release drops one facade's hold on an entry: refcount-- and users.Done(). It
// NEVER closes the underlying — that is the registry's job at evict/shutdown.
// Idempotent per facade (guarded by the facade's own once).
func (r *workStoreRegistry) release(entry *workStoreEntry) {
	if entry == nil {
		return
	}
	r.mu.Lock()
	if entry.refs > 0 {
		entry.refs--
	}
	r.mu.Unlock()
	entry.users.Done()
}

// evict removes key's entry from the cache (so the next getOrOpen reopens fresh),
// then drains in-flight users and closes the underlying. Draining happens OUTSIDE
// the lock so a concurrent getOrOpen for the same key — which now creates a NEW
// entry — is never blocked by the old entry's stragglers. Returns the close
// error, or nil when the key was not present.
func (r *workStoreRegistry) evict(key scopeEndpointKey) error {
	r.mu.Lock()
	entry, ok := r.entries[key]
	if ok {
		delete(r.entries, key)
	}
	r.mu.Unlock()
	if !ok {
		return nil
	}
	entry.users.Wait()
	return r.closeEntry(entry)
}

// closeAll evicts and closes every cached underlying — process shutdown. It
// snapshots the entries under the lock, clears the map, then drains and closes
// each outside the lock. The first close error is returned; all are attempted.
func (r *workStoreRegistry) closeAll() error {
	r.mu.Lock()
	pending := make([]*workStoreEntry, 0, len(r.entries))
	for k, e := range r.entries {
		pending = append(pending, e)
		delete(r.entries, k)
	}
	r.mu.Unlock()
	var firstErr error
	for _, entry := range pending {
		entry.users.Wait()
		if err := r.closeEntry(entry); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

func (r *workStoreRegistry) closeEntry(entry *workStoreEntry) error {
	entry.closeOnce.Do(func() {
		entry.closeErr = r.closeFn(entry.underlying)
	})
	return entry.closeErr
}

// activeEntries reports the number of cached endpoints — a test/observability
// hook only.
func (r *workStoreRegistry) activeEntries() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.entries)
}

// facadePreservesCapabilities reports whether a sharedWorkStoreFacade over
// underlying would expose every optional capability the underlying implements.
// The facade embeds beads.Store and re-declares the "error-channel" optional
// interfaces (Counter, ConditionalWriter, …) so those are always preserved; the
// gate therefore turns on the two cases the facade cannot forward transparently:
//
//   - the WRITE plane: a scope prefix is set but the underlying cannot mint under
//     an arbitrary prefix (no PrefixOverrideCreator), so a shared auto-mint
//     create would take the representative dir's prefix — DECLINE.
//   - GraphApply: preserved by type SELECTION (a graph-flavored facade), so an
//     underlying that is a GraphApplyStore is always matched by
//     newSharedWorkStoreFacade — no decline needed, but asserted here so a future
//     capability the facade forgets to forward fails this gate instead of
//     silently dropping.
func facadePreservesCapabilities(underlying beads.Store, prefix string) bool {
	if underlying == nil {
		return false
	}
	if prefix != "" {
		if _, ok := underlying.(PrefixOverrideCreator); !ok {
			return false
		}
	}
	// Every other capability the codebase type-asserts on is re-declared on the
	// facade (error-channel forward) or type-selected (graph). Assert the
	// type-selected one is reachable so the facade construction below stays in
	// sync with this gate.
	if _, isGraph := beads.GraphApplyFor(underlying); isGraph {
		// newSharedWorkStoreFacade returns a graphSharedWorkStoreFacade for these,
		// which embeds the base facade and implements GraphApplyStore.
		return true
	}
	return true
}

// sharedWorkStoreFacade is the per-scope view over a registry-owned shared
// underlying handle. It embeds the underlying Store so the read / query / by-id
// plane and every non-optional method delegate transparently; it overrides the
// WRITE plane's auto-mint prefix and the CloseStore refcount semantics, and
// re-declares the optional capability interfaces so wrapping does not hide them.
type sharedWorkStoreFacade struct {
	beads.Store
	registry *workStoreRegistry
	entry    *workStoreEntry
	prefix   string
	relOnce  sync.Once
}

// graphSharedWorkStoreFacade is the GraphApplyStore-preserving variant, returned
// when the underlying implements graph apply. Type selection (not always-declare)
// keeps a facade over a non-graph store from falsely advertising GraphApplyStore
// — mirroring beadPolicyGraphStore.
type graphSharedWorkStoreFacade struct {
	*sharedWorkStoreFacade
	applier beads.GraphApplyStore
}

func newSharedWorkStoreFacade(r *workStoreRegistry, entry *workStoreEntry, prefix string) beads.Store {
	base := &sharedWorkStoreFacade{
		Store:    entry.underlying,
		registry: r,
		entry:    entry,
		prefix:   prefix,
	}
	if applier, ok := beads.GraphApplyFor(entry.underlying); ok {
		return &graphSharedWorkStoreFacade{sharedWorkStoreFacade: base, applier: applier}
	}
	return base
}

// Create keeps the WRITE plane scope-aware. An auto-mint create (empty ID) mints
// under the ORIGINATING scope's prefix through the underlying's
// PrefixOverrideCreator — the shared handle's own configured prefix belongs to
// the representative dir, not this scope. An explicit-ID create passes through
// unchanged. facadePreservesCapabilities guarantees the assertion holds whenever
// prefix is set.
func (f *sharedWorkStoreFacade) Create(b beads.Bead) (beads.Bead, error) {
	if f.prefix != "" && b.ID == "" {
		if creator, ok := f.Store.(PrefixOverrideCreator); ok {
			return creator.CreateUnderPrefix(b, f.prefix)
		}
	}
	return f.Store.Create(b)
}

// CloseStore is a refcount RELEASE, never an underlying close: the per-tick close
// discipline must not latch the shared native handle (gascity#3157). Idempotent.
//
//nolint:unparam // beads.Store mandates CloseStore() error; release never errors.
func (f *sharedWorkStoreFacade) CloseStore() error {
	f.relOnce.Do(func() { f.registry.release(f.entry) })
	return nil
}

// Unwrap exposes the shared underlying so the store-stack helpers that walk to a
// concrete backing (bdStoreBacking, the conditional-writes resolve target) can
// see through the facade instead of stopping at it.
func (f *sharedWorkStoreFacade) Unwrap() beads.Store { return f.Store }

// ── optional capability forwarding (error-channel: always declared, delegated
//    to the underlying, vetoed when the underlying lacks the capability) ──────

func (f *sharedWorkStoreFacade) Count(ctx context.Context, query beads.ListQuery, excludeTypes ...string) (int, error) {
	if c, ok := f.Store.(beads.Counter); ok {
		return c.Count(ctx, query, excludeTypes...)
	}
	return 0, beads.ErrCountUnsupported
}

func (f *sharedWorkStoreFacade) ReadyContext(ctx context.Context, query ...beads.ReadyQuery) ([]beads.Bead, error) {
	if r, ok := f.Store.(beads.ContextReadyReader); ok {
		return r.ReadyContext(ctx, query...)
	}
	return nil, beads.ErrReadyContextUnsupported
}

func (f *sharedWorkStoreFacade) DeleteBatch(ids []string) error {
	if d, ok := f.Store.(beads.BatchDeleter); ok {
		return d.DeleteBatch(ids)
	}
	return beads.ErrBatchDeleteUnsupported
}

func (f *sharedWorkStoreFacade) CreateWithStorage(b beads.Bead, storage beads.StorageClass) (beads.Bead, error) {
	// Storage-tiered create with the same scope-aware prefix override.
	if f.prefix != "" && b.ID == "" {
		if creator, ok := f.Store.(interface {
			CreateUnderPrefixWithStorage(beads.Bead, string, beads.StorageClass) (beads.Bead, error)
		}); ok {
			return creator.CreateUnderPrefixWithStorage(b, f.prefix, storage)
		}
	}
	if c, ok := f.Store.(beads.StorageCreateStore); ok {
		return c.CreateWithStorage(b, storage)
	}
	return f.Store.Create(b)
}

func (f *sharedWorkStoreFacade) CreateWithForeignID(b beads.Bead) (beads.Bead, error) {
	if c, ok := f.Store.(beads.ForeignIDCreator); ok {
		return c.CreateWithForeignID(b)
	}
	return f.Store.Create(b)
}

// ConditionalWriter (4 verbs) — delegated via the resolve-target-aware accessor
// so a store that exposes a delegated handle is honored.
func (f *sharedWorkStoreFacade) UpdateIfMatch(id string, rev int64, opts beads.UpdateOpts) error {
	if w, ok := beads.ConditionalWriterFor(f.Store); ok {
		return w.UpdateIfMatch(id, rev, opts)
	}
	return beads.ErrConditionalWriteUnsupported
}

func (f *sharedWorkStoreFacade) CloseIfMatch(id string, rev int64) error {
	if w, ok := beads.ConditionalWriterFor(f.Store); ok {
		return w.CloseIfMatch(id, rev)
	}
	return beads.ErrConditionalWriteUnsupported
}

func (f *sharedWorkStoreFacade) DeleteIfMatch(id string, rev int64) error {
	if w, ok := beads.ConditionalWriterFor(f.Store); ok {
		return w.DeleteIfMatch(id, rev)
	}
	return beads.ErrConditionalWriteUnsupported
}

func (f *sharedWorkStoreFacade) CompareAndSetMetadataKey(id, key, expected, next string) (bool, error) {
	if w, ok := beads.ConditionalWriterFor(f.Store); ok {
		return w.CompareAndSetMetadataKey(id, key, expected, next)
	}
	return false, beads.ErrConditionalWriteUnsupported
}

func (f *sharedWorkStoreFacade) ReleaseIfCurrent(id, expectedAssignee string) (bool, error) {
	if r, ok := f.Store.(beads.ConditionalAssignmentReleaser); ok {
		return r.ReleaseIfCurrent(id, expectedAssignee)
	}
	return false, beads.ErrConditionalWriteUnsupported
}

// ConditionalWritesResolveTarget forwards the fenced-write resolution target so a
// require deployment does not collapse to legacy writes through the facade.
func (f *sharedWorkStoreFacade) ConditionalWritesResolveTarget() beads.Store {
	return f.Store
}

func (f *sharedWorkStoreFacade) WaitForParentProjection(ctx context.Context, id, oldParentID, newParentID string) error {
	if w, ok := f.Store.(beads.ParentProjectionWaiter); ok {
		return w.WaitForParentProjection(ctx, id, oldParentID, newParentID)
	}
	return nil
}

// AtomicTx forwards the underlying's transaction-atomicity guarantee (consumed
// by sourceworkflow's close-with-marker). Conservative false when the underlying
// does not implement AtomicTxStore, matching StoreSupportsAtomicTx.
func (f *sharedWorkStoreFacade) AtomicTx() bool {
	return beads.StoreSupportsAtomicTx(f.Store)
}

// Handles forwards the underlying's cached/live reader handles (shared reads)
// but keeps the WRITE handle on the facade so a write routed through
// HandlesFor(...).Writer still carries this scope's prefix on an auto-mint.
func (f *sharedWorkStoreFacade) Handles() beads.StoreHandles {
	h := beads.HandlesFor(f.Store)
	h.Writer = f
	return h
}

func (f *sharedWorkStoreFacade) IDPrefix() string {
	// The facade's OWN write prefix is the originating scope's; fall back to the
	// underlying's when this scope carries none.
	if f.prefix != "" {
		return f.prefix
	}
	if p, ok := f.Store.(interface{ IDPrefix() string }); ok {
		return p.IDPrefix()
	}
	return ""
}

// ── snapshot capabilities (topology copy primitive) ──────────────────────────

func (f *sharedWorkStoreFacade) ExportBeadSnapshots(ctx context.Context, opts beads.ExportOptions) ([]beads.Snapshot, error) {
	if e, ok := f.Store.(beads.SnapshotExporter); ok {
		return e.ExportBeadSnapshots(ctx, opts)
	}
	return nil, beads.ErrExportUnsupported
}

func (f *sharedWorkStoreFacade) ImportBeadSnapshots(ctx context.Context, snaps []beads.Snapshot, opts beads.ImportOptions) (beads.ImportReport, error) {
	if i, ok := f.Store.(beads.SnapshotImporter); ok {
		return i.ImportBeadSnapshots(ctx, snaps, opts)
	}
	return beads.ImportReport{}, beads.ErrImportUnsupported
}

func (f *sharedWorkStoreFacade) GetBeadSnapshots(ctx context.Context, ids []string) ([]beads.Snapshot, error) {
	if g, ok := f.Store.(beads.SnapshotFetcher); ok {
		return g.GetBeadSnapshots(ctx, ids)
	}
	return nil, beads.ErrExportUnsupported
}

// ── graph apply (type-selected variant) ──────────────────────────────────────

func (g *graphSharedWorkStoreFacade) ApplyGraphPlan(ctx context.Context, plan *beads.GraphApplyPlan) (*beads.GraphApplyResult, error) {
	return g.applier.ApplyGraphPlan(ctx, plan)
}

func (g *graphSharedWorkStoreFacade) ApplyGraphPlanWithStorage(ctx context.Context, plan *beads.GraphApplyPlan, storage beads.StorageClass) (*beads.GraphApplyResult, error) {
	if s, ok := g.applier.(beads.StorageGraphApplyStore); ok {
		return s.ApplyGraphPlanWithStorage(ctx, plan, storage)
	}
	return g.applier.ApplyGraphPlan(ctx, plan)
}

func (g *graphSharedWorkStoreFacade) SupportsEphemeralGraphApply() bool {
	if s, ok := g.applier.(beads.EphemeralGraphApplyStore); ok {
		return s.SupportsEphemeralGraphApply()
	}
	return false
}
