package main

import (
	"bytes"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/orders"
)

// legacyStoreFrom builds a MemStore holding the given beads verbatim.
func legacyStoreFrom(t *testing.T, seeds []beads.Bead) *beads.MemStore {
	t.Helper()
	return beads.NewMemStoreFrom(len(seeds)+1, seeds, nil)
}

func legacyTrackingBead(id, scoped, status string, createdAt time.Time, extraLabels ...string) beads.Bead { //nolint:unparam // scoped is a fixture knob; today's cases share one order name
	return beads.Bead{
		ID:        id,
		Title:     "order:" + scoped,
		Status:    status,
		CreatedAt: createdAt,
		Labels:    append([]string{"order-run:" + scoped, labelOrderTracking}, extraLabels...),
	}
}

// TestMigrateOrdersTrackingSelectionMirrorsRetention pins the import rule:
// every open run, closed runs within TTL, the newest retain-floor closed runs
// regardless of age; older history ages out; the newest run's seq (the
// forward-only cursor high-water) survives into the class store.
func TestMigrateOrdersTrackingSelectionMirrorsRetention(t *testing.T) {
	now := time.Now()
	seeds := []beads.Bead{
		legacyTrackingBead("gc-open", "digest", "open", now.Add(-time.Minute)),
		legacyTrackingBead("gc-recent", "digest", "closed", now.Add(-time.Hour), "wisp", "order:digest", "seq:9"),
	}
	// 12 old closed runs (well past the 7d TTL): the newest 10 must survive
	// via the retain floor, the 2 oldest age out.
	for i := 0; i < 12; i++ {
		seeds = append(seeds, legacyTrackingBead(
			fmt.Sprintf("gc-old-%02d", i), "digest", "closed",
			now.Add(-30*24*time.Hour-time.Duration(i)*time.Hour), "exec"))
	}
	// A wisp root carries order-run but NOT order-tracking: never imported,
	// never deleted (graph class).
	seeds = append(seeds, beads.Bead{
		ID: "gc-wisp", Title: "wisp: digest", Type: "molecule", Status: "open",
		CreatedAt: now, Labels: []string{"order-run:digest", "order:digest", "seq:12"},
	})
	// Unresolvable-name tracking bead: skipped by the import (residue sweep
	// handles it).
	seeds = append(seeds, beads.Bead{
		ID: "gc-foreign", Title: "unrelated", Status: "closed",
		CreatedAt: now, Labels: []string{labelOrderTracking},
	})
	store := legacyStoreFrom(t, seeds)

	cityPath := t.TempDir()
	class, err := ordersClassStoreFor(cityPath)
	if err != nil {
		t.Fatalf("ordersClassStoreFor: %v", err)
	}
	result, err := migrateOrdersTrackingIntoClassStore(class, []beads.Store{store}, orderTrackingRetentionPolicyForConfig(nil))
	if err != nil {
		t.Fatalf("migrate: %v", err)
	}
	// open + newest-10 closed (gc-recent counts toward the floor) = 11
	// imported; the 3 oldest age out.
	if result.imported != 11 || result.skipped != 3 {
		t.Fatalf("result = %+v, want 11 imported / 3 aged out", result)
	}

	front := orders.NewStoreWithTracking(class, beads.GraphStore{})
	if got, err := front.Get("gc-open"); err != nil || !got.Open {
		t.Fatalf("open marker gc-open = (%+v, %v), want imported open", got, err)
	}
	if got, err := front.Get("gc-recent"); err != nil || got.Open || got.Outcome != orders.RunOutcomeWisp || got.Cursor != 9 {
		t.Fatalf("gc-recent = (%+v, %v), want closed wisp run with cursor 9", got, err)
	}
	for _, id := range []string{"gc-old-09", "gc-old-10", "gc-old-11"} {
		if _, err := front.Get(id); err == nil {
			t.Fatalf("aged-out run %s was imported", id)
		}
	}
	if _, err := front.Get("gc-wisp"); err == nil {
		t.Fatal("wisp root imported — graph-class beads must stay put")
	}
	if got := front.Cursor("digest"); got != 9 {
		t.Fatalf("Cursor(digest) = %d, want 9 (the newest run's high-water survives)", got)
	}
	last, err := front.LastRun("digest")
	if err != nil || !last.Equal(seeds[0].CreatedAt) {
		t.Fatalf("LastRun = (%s, %v), want the open marker's CreatedAt (cooldown clock)", last, err)
	}

	// Idempotent: a resumed migration re-imports nothing new and changes
	// nothing.
	again, err := migrateOrdersTrackingIntoClassStore(class, []beads.Store{store}, orderTrackingRetentionPolicyForConfig(nil))
	if err != nil {
		t.Fatalf("re-migrate: %v", err)
	}
	if again.imported != 11 {
		t.Fatalf("re-migrate imported = %d, want 11 (OR IGNORE idempotence)", again.imported)
	}
	runs, err := front.RecentRuns("digest", 0)
	if err != nil || len(runs) != 11 {
		t.Fatalf("class store rows = (%d, %v), want 11 after re-migrate", len(runs), err)
	}
}

// TestEnsureOrdersClassMigratedWritesMarkerAndResidueSweepClearsBD is the
// end-to-end seamless-upgrade proof: first boot imports, writes the marker,
// flips routing; the residue sweep clears bd-side tracking beads but spares a
// fresh open marker (mixed-version grace) and never touches wisp roots.
func TestEnsureOrdersClassMigratedWritesMarkerAndResidueSweepClearsBD(t *testing.T) {
	now := time.Now()
	staleOpen := legacyTrackingBead("gc-stale-open", "digest", "open", now.Add(-time.Hour))
	freshOpen := legacyTrackingBead("gc-fresh-open", "digest", "open", now.Add(-time.Second))
	closedRun := legacyTrackingBead("gc-closed", "digest", "closed", now.Add(-2*time.Hour), "exec")
	wispRoot := beads.Bead{
		ID: "gc-wisp", Title: "wisp: digest", Type: "molecule", Status: "open",
		CreatedAt: now, Labels: []string{"order-run:digest"},
	}
	store := legacyStoreFrom(t, []beads.Bead{staleOpen, freshOpen, closedRun, wispRoot})

	prevOpen := openOrderClassMigrationStore
	openOrderClassMigrationStore = func(_, _ string) (beads.Store, error) { return store, nil }
	t.Cleanup(func() { openOrderClassMigrationStore = prevOpen })

	cityPath := t.TempDir()
	cfg := sqliteOrdersConfig(t)
	var log bytes.Buffer

	if !ensureOrdersClassMigrated(cityPath, cfg, &log) {
		t.Fatalf("ensureOrdersClassMigrated = false, want migrated; log: %s", log.String())
	}
	if _, err := os.Stat(ordersMigratedMarkerPath(cityPath)); err != nil {
		t.Fatalf("migrated marker missing: %v", err)
	}
	if !ordersSQLiteRoutingActive(cityPath, cfg) {
		t.Fatal("routing inactive after migration")
	}

	// Second call short-circuits on the marker.
	if !ensureOrdersClassMigrated(cityPath, cfg, &log) {
		t.Fatal("ensureOrdersClassMigrated(again) = false, want true (marker short-circuit)")
	}

	// Residue sweep: stale open + closed deleted; fresh open spared (grace);
	// wisp root untouched.
	deleted := sweepLegacyOrderTrackingResidue(cityPath, cfg, &log)
	if deleted != 2 {
		t.Fatalf("residue deleted = %d, want 2 (stale open + closed); log: %s", deleted, log.String())
	}
	if _, err := store.Get("gc-fresh-open"); err != nil {
		t.Fatalf("fresh open marker deleted — the mixed-version grace must spare it: %v", err)
	}
	if _, err := store.Get("gc-wisp"); err != nil {
		t.Fatalf("wisp root deleted — graph-class beads must stay put: %v", err)
	}
	if _, err := store.Get("gc-stale-open"); err == nil {
		t.Fatal("stale open bd tracking bead survived the residue sweep")
	}

	// The imported runs are readable through the routed front door.
	routing, err := orderClassRoutingFor(cityPath, cfg)
	if err != nil || !routing.routed {
		t.Fatalf("orderClassRoutingFor = (routed=%v, %v), want routed", routing.routed, err)
	}
	front := routing.front(store)
	for _, id := range []string{"gc-stale-open", "gc-fresh-open", "gc-closed"} {
		if _, err := front.Get(id); err != nil {
			t.Fatalf("imported run %s not readable through the routed front: %v", id, err)
		}
	}
}

// TestEnsureOrdersClassMigratedFreshCity proves a city with no legacy
// tracking beads flips straight to sqlite: zero imports, marker written.
func TestEnsureOrdersClassMigratedFreshCity(t *testing.T) {
	store := beads.NewMemStore()
	prevOpen := openOrderClassMigrationStore
	openOrderClassMigrationStore = func(_, _ string) (beads.Store, error) { return store, nil }
	t.Cleanup(func() { openOrderClassMigrationStore = prevOpen })

	cityPath := t.TempDir()
	var log bytes.Buffer
	if !ensureOrdersClassMigrated(cityPath, sqliteOrdersConfig(t), &log) {
		t.Fatalf("fresh-city migration failed; log: %s", log.String())
	}
	if !ordersSQLiteRoutingActive(cityPath, sqliteOrdersConfig(t)) {
		t.Fatal("fresh city not routed after first boot")
	}
}

// TestEnsureOrdersClassMigratedAbortsWhenScopeUnopenable proves a scope-store
// open failure aborts BEFORE the marker: a partial import must never flip
// routing (the unopened scope's history would be lost to the residue sweep).
func TestEnsureOrdersClassMigratedAbortsWhenScopeUnopenable(t *testing.T) {
	prevOpen := openOrderClassMigrationStore
	openOrderClassMigrationStore = func(_, _ string) (beads.Store, error) {
		return nil, fmt.Errorf("scope store unavailable")
	}
	t.Cleanup(func() { openOrderClassMigrationStore = prevOpen })

	cityPath := t.TempDir()
	var log bytes.Buffer
	if ensureOrdersClassMigrated(cityPath, sqliteOrdersConfig(t), &log) {
		t.Fatal("migration reported success with an unopenable scope store")
	}
	if _, err := os.Stat(ordersMigratedMarkerPath(cityPath)); err == nil {
		t.Fatal("marker written despite aborted migration")
	}
	if ordersSQLiteRoutingActive(cityPath, sqliteOrdersConfig(t)) {
		t.Fatal("routing active despite aborted migration")
	}
}

// TestEnsureOrdersClassMigratedNoopOnBD proves a bd-backed city never
// migrates.
func TestEnsureOrdersClassMigratedNoopOnBD(t *testing.T) {
	cityPath := t.TempDir()
	var log bytes.Buffer
	if ensureOrdersClassMigrated(cityPath, nil, &log) {
		t.Fatal("nil-config city migrated")
	}
	if _, err := os.Stat(ordersMigratedMarkerPath(cityPath)); err == nil {
		t.Fatal("marker written for a bd city")
	}
}
