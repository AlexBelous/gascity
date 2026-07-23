package main

import (
	"os"
	"testing"

	"github.com/gastownhall/gascity/internal/beads"
	nudgesdb "github.com/gastownhall/gascity/internal/classdb/nudges"
	"github.com/gastownhall/gascity/internal/config"
)

// sqliteGraphConfig builds a config selecting the sqlite graph backend by
// direct construction: the config ratchet (sqliteCapableBeadClasses) still
// rejects it at parse time on purpose — routing stays dark until the wiring
// slices flip the ratchet — so tests reach the routing arm directly.
func sqliteGraphConfig() *config.City {
	return &config.City{Beads: config.BeadsConfig{Classes: map[string]config.BeadClassConfig{
		config.BeadClassGraph: {Backend: config.BeadsClassBackendSQLite},
	}}}
}

func writeGraphMigratedMarker(t *testing.T, cityPath string) {
	t.Helper()
	if err := os.MkdirAll(nudgesdb.StoreDir(cityPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(graphMigratedMarkerPath(cityPath), []byte("migrated\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestGraphSQLiteRoutingRequiresConfigAndMarker pins the two-key activation
// and the config-rollback escape hatch, matching the four landed classes.
func TestGraphSQLiteRoutingRequiresConfigAndMarker(t *testing.T) {
	cityPath := t.TempDir()
	if active, err := graphSQLiteRoutingActive(cityPath, nil); active || err != nil {
		t.Fatalf("nil config = (%v, %v), want inactive", active, err)
	}
	if active, err := graphSQLiteRoutingActive(cityPath, sqliteGraphConfig()); active || err != nil {
		t.Fatalf("no marker = (%v, %v), want inactive", active, err)
	}
	writeGraphMigratedMarker(t, cityPath)
	if active, err := graphSQLiteRoutingActive(cityPath, sqliteGraphConfig()); !active || err != nil {
		t.Fatalf("config + marker = (%v, %v), want active", active, err)
	}
	if active, err := graphSQLiteRoutingActive(cityPath, &config.City{}); active || err != nil {
		t.Fatalf("marker + bd backend = (%v, %v), want inactive (rollback wins)", active, err)
	}
}

// TestGraphRoutingNonENOENTMarkerStatFailsClosed pins the ENOENT-only
// discipline for the graph marker.
func TestGraphRoutingNonENOENTMarkerStatFailsClosed(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("permission-based stat failures do not apply to root")
	}
	cityPath := t.TempDir()
	writeGraphMigratedMarker(t, cityPath)
	if err := os.Chmod(nudgesdb.StoreDir(cityPath), 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(nudgesdb.StoreDir(cityPath), 0o755) })
	if _, err := graphSQLiteRoutingActive(cityPath, sqliteGraphConfig()); err == nil {
		t.Fatal("EACCES on the marker stat must fail closed, not read as unmigrated")
	}
}

// TestResolveGraphStoreRoutes proves the resolver arm: identity on an
// unrouted city, the embedded SQLiteStore (minting gcg, with graph-apply
// capability intact) on a routed city, and fail-closed on a marked city
// whose marker is unstatable.
func TestResolveGraphStoreRoutes(t *testing.T) {
	work := beads.NewMemStore()

	// Unrouted: byte-identical work store.
	if got := resolveGraphStore(work, nil, t.TempDir(), nil); got != work {
		t.Fatalf("unrouted resolveGraphStore returned %T, want the work store", got)
	}

	// Routed: the embedded store, minting the reserved gcg prefix and
	// carrying the graph-apply capability the molecule paths assert.
	cityPath := t.TempDir()
	writeGraphMigratedMarker(t, cityPath)
	routed := resolveGraphStore(work, sqliteGraphConfig(), cityPath, nil)
	if routed == work {
		t.Fatal("routed city still resolved the work store")
	}
	if _, ok := beads.GraphApplyFor(routed); !ok {
		t.Fatal("routed graph store lost the GraphApplyStore capability")
	}
	created, err := routed.Create(beads.Bead{Title: "wisp root", Type: "molecule"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if got, want := created.ID[:4], "gcg-"; got != want {
		t.Fatalf("routed create minted %q, want the reserved %q prefix", created.ID, want)
	}
	if _, err := work.Get(created.ID); err == nil {
		t.Fatal("routed graph create leaked into the work store")
	}

	// A second resolve shares the process handle.
	again := resolveGraphStore(beads.NewMemStore(), sqliteGraphConfig(), cityPath, nil)
	if _, err := again.Get(created.ID); err != nil {
		t.Fatalf("second resolver did not share the handle: %v", err)
	}
}
