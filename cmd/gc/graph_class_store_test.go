package main

import (
	"context"
	"os"
	"testing"

	"github.com/gastownhall/gascity/internal/beadmeta"

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

// TestPolicyStoreCreateSideRoutesGraphClass proves the create-side dispatch:
// on a routed city, a molecule pour (ApplyGraphPlan classifies wholesale to
// ClassGraph) and a wisp-typed Create land in the embedded graph store,
// while ordinary work creates stay on the wrapped work store. Unrouted
// wraps stay byte-identical.
func TestPolicyStoreCreateSideRoutesGraphClass(t *testing.T) {
	cityPath := t.TempDir()
	writeGraphMigratedMarker(t, cityPath)
	// The work leg needs graph-apply capability for the wrap to build the
	// graph-store arm (as BdStore does in production); a second SQLiteStore
	// stands in.
	workOpened, err := beads.OpenSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	work := workOpened.(*beads.SQLiteStore)
	t.Cleanup(func() { _ = work.CloseStore() })
	wrapped := wrapStoreWithBeadPoliciesAt(work, sqliteGraphConfig(), cityPath)

	// Work create stays on the work store.
	workBead, err := wrapped.Create(beads.Bead{Title: "ordinary task", Type: "task"})
	if err != nil {
		t.Fatalf("work Create: %v", err)
	}
	if _, err := work.Get(workBead.ID); err != nil {
		t.Fatalf("work create missing from work store: %v", err)
	}

	// Wisp-typed create routes to the graph store.
	wisp, err := wrapped.Create(beads.Bead{Title: "patrol wisp", Type: "wisp", Labels: []string{"gc:wisp"}})
	if err != nil {
		t.Fatalf("wisp Create: %v", err)
	}
	if _, err := work.Get(wisp.ID); err == nil {
		t.Fatal("wisp create leaked into the work store")
	}
	graph, err := graphClassStoreFor(cityPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := graph.Get(wisp.ID); err != nil {
		t.Fatalf("wisp create missing from graph store: %v", err)
	}

	// A molecule pour routes wholesale through graphApplierFor.
	applier, ok := beads.GraphApplyFor(wrapped)
	if !ok {
		t.Fatal("policy-wrapped store lost GraphApplyStore")
	}
	res, err := applier.ApplyGraphPlan(context.Background(), &beads.GraphApplyPlan{
		Nodes: []beads.GraphApplyNode{
			{Key: "root", Title: "wisp: run", Type: "molecule", Metadata: map[string]string{beadmeta.KindMetadataKey: beadmeta.KindWisp}},
			{Key: "step", Title: "step 1", Type: "task", ParentKey: "root", Metadata: map[string]string{beadmeta.KindMetadataKey: beadmeta.KindWisp}},
		},
		Edges: []beads.GraphApplyEdge{{FromKey: "step", ToKey: "root", Type: "tracks"}},
	})
	if err != nil {
		t.Fatalf("ApplyGraphPlan: %v", err)
	}
	rootID := res.IDs["root"]
	if rootID == "" {
		t.Fatalf("pour minted no root id: %+v", res.IDs)
	}
	if _, err := graph.Get(rootID); err != nil {
		t.Fatalf("poured root missing from graph store: %v", err)
	}
	if _, err := work.Get(rootID); err == nil {
		t.Fatal("poured root leaked into the work store")
	}

	// Unrouted wrap (no cityPath): byte-identical — wisp stays on work.
	plainWork := beads.NewMemStore()
	plain := wrapStoreWithBeadPolicies(plainWork, sqliteGraphConfig())
	pw, err := plain.Create(beads.Bead{Title: "wisp", Type: "wisp", Labels: []string{"gc:wisp"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := plainWork.Get(pw.ID); err != nil {
		t.Fatalf("unrouted wisp create left the work store: %v", err)
	}
}
