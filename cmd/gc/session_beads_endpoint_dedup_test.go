package main

import (
	"path/filepath"
	"testing"

	"github.com/gastownhall/gascity/internal/beads"
)

// bdStoreAtDir builds a bd-CLI-backed store rooted at dir with a runner that
// never executes (the endpoint dedup only reads the backing Dir()). It lets the
// work-store dedup resolve a scope root without shelling out to bd.
func bdStoreAtDir(t *testing.T, dir string) beads.Store {
	t.Helper()
	return beads.NewBdStore(dir, func(string, string, ...string) ([]byte, error) {
		t.Fatalf("bd runner must not be invoked by the endpoint dedup")
		return nil, nil
	})
}

// scopeRootStore is a provider-agnostic fake standing in for a NativeDoltStore:
// it exposes ScopeRoot() (the accessor the sweep/coordClass dedup reads) without
// a bd-CLI backing, so bdStoreBacking returns false for it — exactly like a real
// native store. It proves the endpoint dedup collapses native/remote legs too.
type scopeRootStore struct {
	*beads.MemStore
	root string
}

func (s *scopeRootStore) ScopeRoot() string { return s.root }

func nativeLikeStore(root string) beads.Store {
	return &scopeRootStore{MemStore: beads.NewMemStore(), root: root}
}

// TestWorkAssignmentStoresEndpointDedupNative pins the red-team fix: native
// (remote-target) work stores — which return ok=false from bdStoreBacking — are
// still collapsed by the endpoint dedup via their ScopeRoot() accessor, so the
// retirement sweeps act exactly once per bead on a remote multi-rig city.
func TestWorkAssignmentStoresEndpointDedupNative(t *testing.T) {
	t.Run("aliased native rig legs collapse to the city store", func(t *testing.T) {
		city := t.TempDir()
		rigA := filepath.Join(city, "fe")
		rigB := filepath.Join(city, "be")
		// Canonical (remote) city + inherited rigs, all one org endpoint.
		writeScopeFiles(t, city, cityCanonicalState("db.example", "3306"), "org")
		writeScopeFiles(t, rigA, inheritedCanonicalRigState("db.example", "3306"), "org")
		writeScopeFiles(t, rigB, inheritedCanonicalRigState("db.example", "3306"), "org")
		writeRemoteMarker(t, city, "db.example", "3306", "org")

		got := workAssignmentStores(nativeLikeStore(city), map[string]beads.Store{
			"fe": nativeLikeStore(rigA),
			"be": nativeLikeStore(rigB),
		})
		if len(got) != 1 {
			t.Fatalf("aliased native rig legs must collapse to the city store; got %d", len(got))
		}
	})

	t.Run("coordClass collapses aliased native rigs", func(t *testing.T) {
		city := t.TempDir()
		rigA := filepath.Join(city, "fe")
		rigB := filepath.Join(city, "be")
		writeScopeFiles(t, city, cityCanonicalState("db.example", "3306"), "org")
		writeScopeFiles(t, rigA, inheritedCanonicalRigState("db.example", "3306"), "org")
		writeScopeFiles(t, rigB, inheritedCanonicalRigState("db.example", "3306"), "org")
		writeRemoteMarker(t, city, "db.example", "3306", "org")

		cfg := workCfg("unified", "remote", rigA, rigB)
		cfg.Rigs[0].Name = "fe"
		cfg.Rigs[1].Name = "be"
		got := coordClassStoreCandidates(cfg, nativeLikeStore(city), map[string]beads.Store{
			"fe": nativeLikeStore(rigA),
			"be": nativeLikeStore(rigB),
		}, nil, "city")
		if len(got) != 1 || got[0].ref != "city" {
			t.Fatalf("aliased native rig candidates must collapse to the city candidate; got %d", len(got))
		}
	})
}

// TestWorkAssignmentStoresEndpointDedup pins the residual-C sweep fix: on a
// topology-active city the city store and its aliased rig stores collapse to a
// single work store so a release/reassign/unclaim acts EXACTLY ONCE per bead
// (the round-1 duplicate-release-wipes-a-fresh-claim hazard); a non-bd graph
// extra is never endpoint-identical, so it survives. Marker-less cities keep
// every leg (DARK), and non-bd backings are never collapsed.
func TestWorkAssignmentStoresEndpointDedup(t *testing.T) {
	t.Run("aliased rig stores collapse to the city store", func(t *testing.T) {
		city := t.TempDir()
		rigA := filepath.Join(city, "fe")
		rigB := filepath.Join(city, "be")
		writeScopeFiles(t, city, managedCityState(), "acme")
		writeScopeFiles(t, rigA, inheritedRigState(), "acme")
		writeScopeFiles(t, rigB, inheritedRigState(), "acme")
		writeUnifiedMarker(t, city)

		cityStore := bdStoreAtDir(t, city)
		rigStores := map[string]beads.Store{
			"fe": bdStoreAtDir(t, rigA),
			"be": bdStoreAtDir(t, rigB),
		}
		graph := beads.NewMemStore() // distinct local store, never collapsed

		got := workAssignmentStores(cityStore, rigStores, graph)
		if len(got) != 2 {
			t.Fatalf("aliased legs must collapse to [city, graph]; got %d stores", len(got))
		}
		if got[0] != cityStore {
			t.Fatal("the city store must be the surviving work endpoint")
		}
		if got[1] != beads.Store(graph) {
			t.Fatal("the distinct graph store must survive the collapse")
		}
	})

	t.Run("scoped distinct databases keep every leg (DARK)", func(t *testing.T) {
		city := t.TempDir()
		rigA := filepath.Join(city, "fe")
		rigB := filepath.Join(city, "be")
		writeScopeFiles(t, city, managedCityState(), "hq")
		writeScopeFiles(t, rigA, managedCityState(), "fe")
		writeScopeFiles(t, rigB, managedCityState(), "be")
		// No unified marker: DARK.

		got := workAssignmentStores(bdStoreAtDir(t, city), map[string]beads.Store{
			"fe": bdStoreAtDir(t, rigA),
			"be": bdStoreAtDir(t, rigB),
		})
		if len(got) != 3 {
			t.Fatalf("distinct databases must each keep a leg; got %d", len(got))
		}
	})

	t.Run("non-bd city store is never deduped", func(t *testing.T) {
		// A mem-backed city store cannot resolve a scope root, so the dedup is a
		// no-op even were a marker present — every existing mem-store caller keeps
		// its behavior.
		got := workAssignmentStores(beads.NewMemStore(), map[string]beads.Store{
			"fe": beads.NewMemStore(),
		})
		if len(got) != 2 {
			t.Fatalf("mem stores must be left untouched; got %d", len(got))
		}
	})
}

// TestCoordClassStoreCandidatesEndpointDedup pins the reconciler-fan-out arm of
// residual-C: aliased rig candidates collapse to the city candidate on a
// topology-active city, and marker-less cities keep every candidate (DARK).
func TestCoordClassStoreCandidatesEndpointDedup(t *testing.T) {
	t.Run("aliased rigs collapse to the city candidate", func(t *testing.T) {
		city := t.TempDir()
		rigA := filepath.Join(city, "fe")
		rigB := filepath.Join(city, "be")
		writeScopeFiles(t, city, managedCityState(), "acme")
		writeScopeFiles(t, rigA, inheritedRigState(), "acme")
		writeScopeFiles(t, rigB, inheritedRigState(), "acme")
		writeUnifiedMarker(t, city)

		cfg := workCfg("unified", "managed", rigA, rigB)
		cfg.Rigs[0].Name = "fe"
		cfg.Rigs[1].Name = "be"
		rigStores := map[string]beads.Store{
			"fe": bdStoreAtDir(t, rigA),
			"be": bdStoreAtDir(t, rigB),
		}
		got := coordClassStoreCandidates(cfg, bdStoreAtDir(t, city), rigStores, nil, "city")
		if len(got) != 1 || got[0].ref != "city" {
			t.Fatalf("aliased rig candidates must collapse to the city candidate; got %d", len(got))
		}
	})

	t.Run("scoped distinct databases keep every candidate (DARK)", func(t *testing.T) {
		city := t.TempDir()
		rigA := filepath.Join(city, "fe")
		rigB := filepath.Join(city, "be")
		writeScopeFiles(t, city, managedCityState(), "hq")
		writeScopeFiles(t, rigA, managedCityState(), "fe")
		writeScopeFiles(t, rigB, managedCityState(), "be")

		cfg := workCfg("scoped", "managed", rigA, rigB)
		cfg.Rigs[0].Name = "fe"
		cfg.Rigs[1].Name = "be"
		rigStores := map[string]beads.Store{
			"fe": bdStoreAtDir(t, rigA),
			"be": bdStoreAtDir(t, rigB),
		}
		got := coordClassStoreCandidates(cfg, bdStoreAtDir(t, city), rigStores, nil, "city")
		if len(got) != 3 {
			t.Fatalf("distinct databases must each keep a candidate; got %d", len(got))
		}
	})
}
