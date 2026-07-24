package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/beadmeta"

	"github.com/gastownhall/gascity/internal/beads"
	nudgesdb "github.com/gastownhall/gascity/internal/classdb/nudges"
	"github.com/gastownhall/gascity/internal/config"
	"github.com/gastownhall/gascity/internal/doctor"
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

// TestGraphSqliteBackendAcceptedByConfig pins the ratchet flip: the parsed
// config now accepts [beads.classes.graph] backend="sqlite" (it was a hard
// load failure while the wiring landed).
func TestGraphSqliteBackendAcceptedByConfig(t *testing.T) {
	cfg, err := config.Parse([]byte("[workspace]\nname = \"g\"\n\n[beads.classes.graph]\nbackend = \"sqlite\"\n"))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if cfg.Beads.ClassBackend(config.BeadClassGraph) != config.BeadsClassBackendSQLite {
		t.Fatal("graph backend did not resolve to sqlite")
	}
}

// TestGraphStoreRetentionDisabled pins gap N07/N10/N11: the graph store
// must NOT run the ported 4h terminal sweeper — closed steps of running
// workflows are read by finalize votes and drain re-counts. A closed bead
// must survive a sweep-interval-scale wait trivially (no sweeper started).
func TestGraphStoreRetentionDisabled(t *testing.T) {
	cityPath := t.TempDir()
	st, err := graphClassStoreFor(cityPath)
	if err != nil {
		t.Fatal(err)
	}
	b, err := st.Create(beads.Bead{Title: "failed step", Type: "task"})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Close(b.ID); err != nil {
		t.Fatal(err)
	}
	got, err := st.Get(b.ID)
	if err != nil || got.Status != "closed" {
		t.Fatalf("closed step unreadable: %+v %v", got, err)
	}
}

// TestWaitDependencyReadsReachGraphStore pins gap N02/N22: wait-dependency
// resolution (create-time loader and the tick's dep store set) sees
// graph-resident beads on a routed city.
func TestWaitDependencyReadsReachGraphStore(t *testing.T) {
	cityPath := t.TempDir()
	writeGraphMigratedMarker(t, cityPath)
	cfg := sqliteGraphConfig()
	st, err := graphClassStoreFor(cityPath)
	if err != nil {
		t.Fatal(err)
	}
	root, err := st.Create(beads.Bead{Title: "workflow root", Type: "molecule"})
	if err != nil {
		t.Fatal(err)
	}
	depStores, err := appendRoutedGraphStore(newWaitDependencyStoreSet(beads.NewMemStore(), nil), cityPath, cfg)
	if err != nil {
		t.Fatal(err)
	}
	got, err := waitDependencyStoreSet(depStores).Get(root.ID)
	if err != nil || got.ID != root.ID {
		t.Fatalf("dep store set missed the graph root: (%+v, %v)", got, err)
	}
}

// TestConvoyStoresIncludeRoutedGraphStore pins sweep gaps N05/N19/N21: the
// convoy/beads store fan-out prepends the routed graph store, by-id
// resolution pins it as the owner (an un-swept bd residue row is not an
// ambiguity), and list lanes dedupe.
func TestConvoyStoresIncludeRoutedGraphStore(t *testing.T) {
	cityPath := t.TempDir()
	writeGraphMigratedMarker(t, cityPath)
	cfg := sqliteGraphConfig()
	graph, err := graphClassStoreFor(cityPath)
	if err != nil {
		t.Fatal(err)
	}
	syn, err := graph.Create(beads.Bead{Title: "input convoy", Type: "convoy", Metadata: map[string]string{"gc.synthetic": "true"}})
	if err != nil {
		t.Fatal(err)
	}

	work := beads.NewMemStore()
	views, err := openConvoyStores(cfg, cityPath, syn.ID, func(string) (beads.Store, error) { return work, nil })
	if err != nil {
		t.Fatal(err)
	}
	if len(views) == 0 || !views[0].graph {
		t.Fatalf("graph view not prepended: %+v", views)
	}
	got, dir, err := resolveOwningStoreDir(syn.ID, cfg, cityPath, func(string) (beads.Store, error) { return work, nil })
	if err != nil || got == nil {
		t.Fatalf("resolveOwningStoreDir = (%v, %q, %v)", got, dir, err)
	}
	convoys, err := collectOpenConvoys(views)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, c := range convoys {
		if c.bead.ID == syn.ID {
			found = true
		}
	}
	if !found {
		t.Fatalf("synthetic graph convoy missing from the fan-out: %+v", convoys)
	}
}

// TestWispStepInjectionReadsGraphStore pins sweep gap N14: on a routed city
// the agent's active molecule step is resolved from the graph store, so a
// routed-pool agent gets step context in its prompt.
func TestWispStepInjectionReadsGraphStore(t *testing.T) {
	cityPath := t.TempDir()
	writeGraphMigratedMarker(t, cityPath)
	if err := os.WriteFile(filepath.Join(cityPath, "city.toml"),
		[]byte("[workspace]\nname = \"inject\"\n\n[beads.classes.graph]\nbackend = \"sqlite\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	graph, err := graphClassStoreFor(cityPath)
	if err != nil {
		t.Fatal(err)
	}
	root, err := graph.Create(beads.Bead{Title: "wisp: run", Type: "molecule", Assignee: "gc-city-w1", Status: "in_progress"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := graph.Create(beads.Bead{
		Title: "step one", Type: "step", ParentID: root.ID, Status: "in_progress",
		Description: "do the thing carefully",
	}); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GC_SESSION_NAME", "gc-city-w1")
	t.Setenv("GC_RIG_ROOT", "")
	got := wispStepInjectionContent(cityPath)
	if !strings.Contains(got, "do the thing carefully") {
		t.Fatalf("graph-resident step not injected: %q", got)
	}
}

// TestWispGCDefaultsOnRoutedCity pins sweep gap N18: a graph-routed city
// with unset [daemon] knobs gets working wisp retention (bd cities relied on
// reaper.sh / wisp-compact.sh, which cannot see the embedded store), while
// explicit config still wins and unrouted cities stay disabled-by-default.
func TestWispGCDefaultsOnRoutedCity(t *testing.T) {
	// Unrouted + unset knobs: GC stays disabled (newWispGC returns nil), the
	// pre-existing bd behavior where reaper.sh/wisp-compact.sh own retention.
	if plain := newWispGCForConfig(classMigrationConfig(t, ""), t.TempDir()); plain != nil {
		t.Fatalf("unrouted city gained retention defaults: %+v", plain)
	}

	cityPath := t.TempDir()
	writeGraphMigratedMarker(t, cityPath)
	routed := newWispGCForConfig(sqliteGraphConfig(), cityPath)
	m, ok := routed.(*memoryWispGC)
	if !ok || m.ttl != graphRoutedWispTTL || m.interval != graphRoutedWispGCInterval {
		t.Fatalf("routed city missing retention defaults: %+v", routed)
	}

	explicit := sqliteGraphConfig()
	explicit.Daemon.WispTTL = "48h"
	explicit.Daemon.WispGCInterval = "30m"
	got := newWispGCForConfig(explicit, cityPath)
	if m, ok := got.(*memoryWispGC); !ok || m.ttl != 48*time.Hour || m.interval != 30*time.Minute {
		t.Fatalf("explicit config did not win: %+v", got)
	}
}

// TestBacklogDepthIncludesGraphStore pins gaps G38/N20: the backlog census
// counts the relocated plane on a routed city, and fails loud rather than
// reporting a confident work-store-only number when the graph store cannot
// be read.
func TestBacklogDepthIncludesGraphStore(t *testing.T) {
	cityPath := t.TempDir()
	writeGraphMigratedMarker(t, cityPath)
	graph, err := graphClassStoreFor(cityPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := graph.Create(beads.Bead{Title: "graph step", Type: "task"}); err != nil {
		t.Fatal(err)
	}
	work := beads.NewMemStore()
	if _, err := work.Create(beads.Bead{Title: "work item", Type: "task"}); err != nil {
		t.Fatal(err)
	}

	check := newBacklogDepthCheck(sqliteGraphConfig(), cityPath, func(string) (beads.Store, error) { return work, nil })
	r := check.Run(&doctor.CheckContext{CityPath: cityPath})
	if r.Status != doctor.StatusOK {
		t.Fatalf("status = %v: %s", r.Status, r.Message)
	}
	if !strings.Contains(r.Message, "+ graph store: 1 open") {
		t.Fatalf("graph plane missing from the census: %s", r.Message)
	}

	// Unrouted city: byte-identical message, no suffix.
	plain := newBacklogDepthCheck(nil, t.TempDir(), func(string) (beads.Store, error) { return work, nil })
	if got := plain.Run(&doctor.CheckContext{}); strings.Contains(got.Message, "graph store") {
		t.Fatalf("unrouted city gained a graph suffix: %s", got.Message)
	}
}
