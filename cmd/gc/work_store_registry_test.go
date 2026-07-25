package main

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/gastownhall/gascity/internal/beads"
)

// capableWorkStore is a fake underlying handle that implements the full optional
// capability set the shared-handle facade must preserve. It embeds *MemStore for
// the base Store plane (and MemStore's own ConditionalWriter / ReadyContext /
// ReleaseIfCurrent) and adds the rest. Every added method is a stub — the pins
// assert the facade type-asserts POSITIVELY for each capability, plus that the
// prefix-aware write plane routes through CreateUnderPrefix.
type capableWorkStore struct {
	*beads.MemStore
	prefixCreates []string // prefixes seen by CreateUnderPrefix, in order
}

func newCapableWorkStore() *capableWorkStore {
	return &capableWorkStore{MemStore: beads.NewMemStore()}
}

func (s *capableWorkStore) CreateUnderPrefix(b beads.Bead, prefix string) (beads.Bead, error) {
	s.prefixCreates = append(s.prefixCreates, prefix)
	created, err := s.Create(b)
	if err != nil {
		return created, err
	}
	created.ID = prefix + "-1" // model the server minting the next id under prefix
	return created, nil
}

func (s *capableWorkStore) Count(context.Context, beads.ListQuery, ...string) (int, error) {
	return 0, nil
}

func (s *capableWorkStore) CreateWithForeignID(b beads.Bead) (beads.Bead, error) {
	return s.Create(b)
}
func (s *capableWorkStore) DeleteBatch([]string) error { return nil }
func (s *capableWorkStore) CreateWithStorage(b beads.Bead, _ beads.StorageClass) (beads.Bead, error) {
	return s.Create(b)
}

func (s *capableWorkStore) ExportBeadSnapshots(context.Context, beads.ExportOptions) ([]beads.Snapshot, error) {
	return nil, nil
}

func (s *capableWorkStore) ImportBeadSnapshots(context.Context, []beads.Snapshot, beads.ImportOptions) (beads.ImportReport, error) {
	return beads.ImportReport{}, nil
}

func (s *capableWorkStore) GetBeadSnapshots(context.Context, []string) ([]beads.Snapshot, error) {
	return nil, nil
}

func (s *capableWorkStore) ApplyGraphPlan(context.Context, *beads.GraphApplyPlan) (*beads.GraphApplyResult, error) {
	return &beads.GraphApplyResult{}, nil
}
func (s *capableWorkStore) AtomicTx() bool { return true }
func (s *capableWorkStore) Handles() beads.StoreHandles {
	// A native/caching-style underlying exposes its own handles; the facade must
	// preserve the reader handles but keep the write handle scope-aware.
	return beads.StoreHandles{Writer: s.MemStore}
}

// endpointKey is a small helper for a resolvable managed endpoint.
func testEndpointKey(db string) scopeEndpointKey {
	return scopeEndpointKey{database: db}
}

// TestWorkStoreFacadeCapabilityPreservation pins that a facade over a store
// implementing the full optional set still type-asserts positively for each —
// wrapping must never silently disable a capability on a registry-served city.
func TestWorkStoreFacadeCapabilityPreservation(t *testing.T) {
	reg := newWorkStoreRegistry(nil)
	underlying := newCapableWorkStore()
	facade, wrapped, err := reg.getOrOpen(testEndpointKey("acme"), "fe", func() (beads.Store, error) {
		return underlying, nil
	})
	if err != nil || !wrapped {
		t.Fatalf("expected a wrapped facade, got wrapped=%v err=%v", wrapped, err)
	}

	if _, ok := facade.(beads.Counter); !ok {
		t.Error("facade dropped beads.Counter")
	}
	if _, ok := facade.(beads.ForeignIDCreator); !ok {
		t.Error("facade dropped beads.ForeignIDCreator")
	}
	if _, ok := facade.(beads.ConditionalWriter); !ok {
		t.Error("facade dropped beads.ConditionalWriter")
	}
	if _, ok := facade.(beads.ConditionalAssignmentReleaser); !ok {
		t.Error("facade dropped beads.ConditionalAssignmentReleaser")
	}
	if _, ok := facade.(beads.BatchDeleter); !ok {
		t.Error("facade dropped beads.BatchDeleter")
	}
	if _, ok := facade.(beads.ContextReadyReader); !ok {
		t.Error("facade dropped beads.ContextReadyReader")
	}
	if _, ok := facade.(beads.SnapshotExporter); !ok {
		t.Error("facade dropped beads.SnapshotExporter")
	}
	if _, ok := facade.(beads.SnapshotImporter); !ok {
		t.Error("facade dropped beads.SnapshotImporter")
	}
	if _, ok := facade.(beads.SnapshotFetcher); !ok {
		t.Error("facade dropped beads.SnapshotFetcher")
	}
	if _, ok := facade.(beads.StorageCreateStore); !ok {
		t.Error("facade dropped beads.StorageCreateStore")
	}
	if _, ok := facade.(beads.GraphApplyStore); !ok {
		t.Error("facade dropped beads.GraphApplyStore (type-selected variant)")
	}
	if _, ok := facade.(beads.ConditionalWritesResolveTargeter); !ok {
		t.Error("facade dropped beads.ConditionalWritesResolveTargeter")
	}
	// The facade exposes the shared underlying to the store-stack walkers
	// (bdStoreBacking, the conditional-writes resolve target) so wiring it into
	// the choke point does not blind them.
	unwrapper, ok := facade.(interface{ Unwrap() beads.Store })
	if !ok || unwrapper.Unwrap() != beads.Store(underlying) {
		t.Error("facade must Unwrap to the shared underlying handle")
	}
	// AtomicTx + Handles: two optional capabilities the completeness gate guards.
	atx, ok := facade.(beads.AtomicTxStore)
	if !ok || !atx.AtomicTx() {
		t.Error("facade dropped beads.AtomicTxStore (or failed to forward AtomicTx)")
	}
	handler, ok := facade.(interface{ Handles() beads.StoreHandles })
	if !ok {
		t.Fatal("facade dropped the Handles() provider")
	}
	// The Handles() write handle must stay scope-aware: an auto-mint through it
	// carries this scope's prefix, not the underlying's own.
	underlying.prefixCreates = nil
	if _, err := handler.Handles().Writer.Create(beads.Bead{Title: "via-handles"}); err != nil {
		t.Fatalf("Handles().Writer.Create: %v", err)
	}
	if len(underlying.prefixCreates) != 1 || underlying.prefixCreates[0] != "fe" {
		t.Errorf("Handles().Writer auto-mint must carry the scope prefix, saw %v", underlying.prefixCreates)
	}
}

// TestWorkStoreFacadeScopeAwareWritePlane pins the write plane: an auto-mint
// create routes through the ORIGINATING scope's prefix, while an explicit-id
// create passes through unchanged.
func TestWorkStoreFacadeScopeAwareWritePlane(t *testing.T) {
	reg := newWorkStoreRegistry(nil)
	underlying := newCapableWorkStore()
	facade, _, err := reg.getOrOpen(testEndpointKey("acme"), "be", func() (beads.Store, error) {
		return underlying, nil
	})
	if err != nil {
		t.Fatalf("getOrOpen: %v", err)
	}

	minted, err := facade.Create(beads.Bead{Title: "auto"})
	if err != nil {
		t.Fatalf("auto-mint create: %v", err)
	}
	if len(underlying.prefixCreates) != 1 || underlying.prefixCreates[0] != "be" {
		t.Fatalf("auto-mint must carry the originating scope prefix, saw %v", underlying.prefixCreates)
	}
	if minted.ID != "be-1" {
		t.Fatalf("minted id = %q, want be-1", minted.ID)
	}

	if _, err := facade.Create(beads.Bead{ID: "fe-42", Title: "explicit"}); err != nil {
		t.Fatalf("explicit-id create: %v", err)
	}
	if len(underlying.prefixCreates) != 1 {
		t.Fatalf("explicit-id create must bypass the prefix override, saw %v", underlying.prefixCreates)
	}
}

// TestWorkStoreRegistryDeclinesWithoutPrefixMint pins correctness-over-coverage:
// a differently-prefixed scope over a store that cannot mint under an arbitrary
// prefix is NOT wrapped — it falls back to the direct (unshared) handle so rig
// writes are never mis-prefixed through a shared handle.
func TestWorkStoreRegistryDeclinesWithoutPrefixMint(t *testing.T) {
	reg := newWorkStoreRegistry(nil)
	plain := beads.NewMemStore() // no PrefixOverrideCreator
	got, wrapped, err := reg.getOrOpen(testEndpointKey("acme"), "fe", func() (beads.Store, error) {
		return plain, nil
	})
	if err != nil {
		t.Fatalf("getOrOpen: %v", err)
	}
	if wrapped {
		t.Fatal("a store without prefix minting must not be wrapped for a prefixed scope")
	}
	if got != beads.Store(plain) {
		t.Fatal("declined open must hand back the direct underlying")
	}
	if reg.activeEntries() != 0 {
		t.Fatal("a declined open must not be cached")
	}
}

// TestWorkStoreRegistryRefcountReleaseDoesNotClose pins the #3157 latch guard: a
// caller CloseStore on a facade is a refcount release, never an underlying close.
func TestWorkStoreRegistryRefcountReleaseDoesNotClose(t *testing.T) {
	var closes int32
	reg := newWorkStoreRegistry(func(beads.Store) error {
		atomic.AddInt32(&closes, 1)
		return nil
	})
	underlying := newCapableWorkStore()
	key := testEndpointKey("acme")

	// Two scopes share one endpoint: two facades, one underlying.
	f1, _, _ := reg.getOrOpen(key, "fe", func() (beads.Store, error) { return underlying, nil })
	f2, w2, _ := reg.getOrOpen(key, "be", func() (beads.Store, error) {
		t.Fatal("second get on the same endpoint must reuse the cached underlying")
		return nil, nil
	})
	if !w2 {
		t.Fatal("second scope must also be wrapped")
	}
	if reg.activeEntries() != 1 {
		t.Fatalf("one endpoint must cache one entry, got %d", reg.activeEntries())
	}

	closer1 := f1.(interface{ CloseStore() error })
	closer2 := f2.(interface{ CloseStore() error })
	if err := closer1.CloseStore(); err != nil {
		t.Fatalf("CloseStore f1: %v", err)
	}
	if err := closer2.CloseStore(); err != nil {
		t.Fatalf("CloseStore f2: %v", err)
	}
	// Idempotent: a second per-tick close must also not latch.
	_ = closer1.CloseStore()
	if got := atomic.LoadInt32(&closes); got != 0 {
		t.Fatalf("facade CloseStore latched the shared handle (%d closes)", got)
	}
}

// TestWorkStoreRegistryEvictClosesAndReopens pins the reload path: evicting a key
// closes the underlying and drops the cache entry so a later lookup reopens a
// fresh handle.
func TestWorkStoreRegistryEvictClosesAndReopens(t *testing.T) {
	var closes int32
	reg := newWorkStoreRegistry(func(beads.Store) error {
		atomic.AddInt32(&closes, 1)
		return nil
	})
	key := testEndpointKey("acme")

	var opens int32
	open := func() (beads.Store, error) {
		atomic.AddInt32(&opens, 1)
		return newCapableWorkStore(), nil
	}

	f, _, _ := reg.getOrOpen(key, "fe", open)
	_ = f.(interface{ CloseStore() error }).CloseStore() // drop the only ref

	if err := reg.evict(key); err != nil {
		t.Fatalf("evict: %v", err)
	}
	if atomic.LoadInt32(&closes) != 1 {
		t.Fatalf("evict must close the underlying exactly once, got %d", atomic.LoadInt32(&closes))
	}
	if reg.activeEntries() != 0 {
		t.Fatal("evict must drop the cache entry")
	}

	// A later lookup reopens fresh.
	_, _, _ = reg.getOrOpen(key, "fe", open)
	if atomic.LoadInt32(&opens) != 2 {
		t.Fatalf("post-evict lookup must reopen a fresh underlying, opens=%d", atomic.LoadInt32(&opens))
	}
}

// TestWorkStoreRegistryConcurrentGetRelease exercises the get/release/evict paths
// under -race to prove the lifecycle is race-free.
func TestWorkStoreRegistryConcurrentGetRelease(t *testing.T) {
	reg := newWorkStoreRegistry(func(beads.Store) error { return nil })
	keys := []scopeEndpointKey{testEndpointKey("a"), testEndpointKey("b"), testEndpointKey("c")}

	var wg sync.WaitGroup
	for i := 0; i < 64; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			key := keys[i%len(keys)]
			f, _, err := reg.getOrOpen(key, "fe", func() (beads.Store, error) {
				return newCapableWorkStore(), nil
			})
			if err != nil {
				return
			}
			_ = f.(interface{ CloseStore() error }).CloseStore()
			if i%8 == 0 {
				_ = reg.evict(key)
			}
		}(i)
	}
	wg.Wait()
	_ = reg.closeAll()
	if n := reg.activeEntries(); n != 0 {
		t.Fatalf("closeAll must leave the registry empty, got %d active entries", n)
	}
}

// TestWorkStoreRegistryPerScopePointerIdentity pins that endpoint-identical
// scopes share ONE underlying but receive DISTINCT facade instances — so the
// graph-store-vs-cityStore pointer checks (graph is never endpoint-identical, so
// never collapsed) keep distinct identities.
func TestWorkStoreRegistryPerScopePointerIdentity(t *testing.T) {
	reg := newWorkStoreRegistry(nil)
	underlying := newCapableWorkStore()
	key := testEndpointKey("acme")

	fe, _, _ := reg.getOrOpen(key, "fe", func() (beads.Store, error) { return underlying, nil })
	be, _, _ := reg.getOrOpen(key, "be", func() (beads.Store, error) { return underlying, nil })
	if fe == be {
		t.Fatal("per-scope facades over one endpoint must be distinct instances")
	}
	// A distinct endpoint (the local graph store's key would differ) never
	// collapses into the work endpoint.
	graph, _, _ := reg.getOrOpen(testEndpointKey("graph"), "", func() (beads.Store, error) {
		return newCapableWorkStore(), nil
	})
	if graph == fe || graph == be {
		t.Fatal("a distinct endpoint must never collapse into the work endpoint")
	}
}
