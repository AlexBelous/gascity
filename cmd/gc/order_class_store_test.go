package main

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/config"
	"github.com/gastownhall/gascity/internal/events"
	"github.com/gastownhall/gascity/internal/orders"
)

func sqliteOrdersConfig(t *testing.T) *config.City {
	t.Helper()
	cfg, err := config.Parse([]byte(`[workspace]
name = "routing-test"

[beads.classes.orders]
backend = "sqlite"
`))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	return cfg
}

func writeOrdersMigratedMarker(t *testing.T, cityPath string) {
	t.Helper()
	if err := os.MkdirAll(ordersClassStoreDir(cityPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ordersMigratedMarkerPath(cityPath), []byte("migrated\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestOrdersSQLiteRoutingRequiresConfigAndMarker pins the two-key activation:
// the config knob alone (pre-migration) and the marker alone (config rolled
// back) both keep routing on bd — the marker, not the binary, decides.
func TestOrdersSQLiteRoutingRequiresConfigAndMarker(t *testing.T) {
	cityPath := t.TempDir()
	if active, err := ordersSQLiteRoutingActive(cityPath, nil); active || err != nil {
		t.Fatalf("routing with nil config = (%v, %v), want inactive", active, err)
	}
	if active, err := ordersSQLiteRoutingActive(cityPath, sqliteOrdersConfig(t)); active || err != nil {
		t.Fatalf("routing without the migrated marker = (%v, %v), want inactive", active, err)
	}
	writeOrdersMigratedMarker(t, cityPath)
	if active, err := ordersSQLiteRoutingActive(cityPath, sqliteOrdersConfig(t)); !active || err != nil {
		t.Fatalf("routing with config + marker = (%v, %v), want active", active, err)
	}
	bdCfg, err := config.Parse([]byte("[workspace]\nname = \"bd\"\n"))
	if err != nil {
		t.Fatal(err)
	}
	if active, err := ordersSQLiteRoutingActive(cityPath, bdCfg); active || err != nil {
		t.Fatalf("routing with marker but bd backend = (%v, %v), want inactive (config rollback must win)", active, err)
	}
}

// TestOrdersRoutingNonENOENTMarkerStatFailsClosed pins the ENOENT-only
// discipline the nudges review established: only an ABSENT marker means
// "not migrated". Any other stat failure (EACCES/EIO) must surface as an
// error — silently reading it as "bd" would land writes where a routed
// reader on a migrated city never looks.
func TestOrdersRoutingNonENOENTMarkerStatFailsClosed(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("permission-based stat failures do not apply to root")
	}
	cityPath := t.TempDir()
	writeOrdersMigratedMarker(t, cityPath)
	if err := os.Chmod(ordersClassStoreDir(cityPath), 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(ordersClassStoreDir(cityPath), 0o755) })
	if _, err := ordersSQLiteRoutingActive(cityPath, sqliteOrdersConfig(t)); err == nil {
		t.Fatal("EACCES on the marker stat must fail closed, not read as unmigrated")
	}
	if _, err := orderClassRoutingFor(cityPath, sqliteOrdersConfig(t)); err == nil {
		t.Fatal("orderClassRoutingFor must propagate the marker stat failure")
	}
}

// TestOrderClassRoutingInactiveIsByteIdenticalBD proves inactive routing
// returns the bd front shape: tracking writes land in the scope store.
func TestOrderClassRoutingInactiveIsByteIdenticalBD(t *testing.T) {
	routing, err := orderClassRoutingFor(t.TempDir(), nil)
	if err != nil {
		t.Fatalf("orderClassRoutingFor: %v", err)
	}
	if routing.routed {
		t.Fatal("routing.routed = true without config+marker")
	}
	scope := beads.NewMemStore()
	run, err := routing.front(scope).CreateRun("digest", orders.RunOpts{})
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	if _, err := scope.Get(run.ID); err != nil {
		t.Fatalf("bd-routed create did not land in the scope store: %v", err)
	}
}

// TestOrderClassRoutingActiveRoutesTrackingToClassStore is the end-to-end
// routing proof: with config + marker, tracking writes land in
// .gc/store/orders.db (NOT the scope store), reads come back through the
// front door, and the scope store still contributes wisp-root order-run
// evidence as the graph leg.
func TestOrderClassRoutingActiveRoutesTrackingToClassStore(t *testing.T) {
	cityPath := t.TempDir()
	writeOrdersMigratedMarker(t, cityPath)
	routing, err := orderClassRoutingFor(cityPath, sqliteOrdersConfig(t))
	if err != nil {
		t.Fatalf("orderClassRoutingFor: %v", err)
	}
	if !routing.routed {
		t.Fatal("routing.routed = false with config + marker")
	}

	scope := beads.NewMemStore()
	front := routing.front(scope)
	run, err := front.CreateRun("digest", orders.RunOpts{})
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	if _, err := os.Stat(ordersClassStorePath(cityPath)); err != nil {
		t.Fatalf("class store file missing: %v", err)
	}
	if _, err := scope.Get(run.ID); err == nil {
		t.Fatal("routed tracking create leaked into the scope store")
	}
	got, err := front.Get(run.ID)
	if err != nil || !got.Open || got.Scoped != "digest" {
		t.Fatalf("front.Get = (%+v, %v), want the routed open run", got, err)
	}

	// A second resolver for the same city shares the process handle and sees
	// the same rows (the CLI-vs-controller multi-handle model).
	again, err := orderClassRoutingFor(cityPath, sqliteOrdersConfig(t))
	if err != nil {
		t.Fatalf("orderClassRoutingFor(again): %v", err)
	}
	if open, err := again.front(beads.NewMemStore()).OpenRuns(); err != nil || len(open) != 1 {
		t.Fatalf("second resolver OpenRuns = (%d, %v), want the routed run", len(open), err)
	}

	// Graph leg still unions: wisp-root evidence in the scope store counts.
	if _, err := scope.Create(beads.Bead{
		Title:  "wisp: other",
		Type:   "molecule",
		Labels: []string{"order-run:other", "order:other", "seq:5"},
	}); err != nil {
		t.Fatal(err)
	}
	last, err := front.LastRun("other")
	if err != nil {
		t.Fatalf("LastRun: %v", err)
	}
	if last.IsZero() {
		t.Fatal("LastRun(other) zero — the graph leg stopped unioning wisp-root evidence")
	}
	if got := front.Cursor("other"); got != 5 {
		t.Fatalf("Cursor(other) = %d, want 5 from the scope-store wisp root", got)
	}
}

// TestOrderFrontSeamIsTheOnlyConstructionPoint is the completeness ratchet
// for the routing seam: production cmd/gc code must never construct an
// orders front door directly — a direct orders.NewStore* call would bypass
// the [beads.classes.orders] backend dispatch and split tracking state
// across two backends on a migrated city.
func TestOrderFrontSeamIsTheOnlyConstructionPoint(t *testing.T) {
	allowed := map[string]bool{
		"order_store.go":       true, // orderFrontForStore, the bd shape
		"order_class_store.go": true, // the routed shape
	}
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || filepath.Ext(name) != ".go" || allowed[name] {
			continue
		}
		if len(name) > 8 && name[len(name)-8:] == "_test.go" {
			continue
		}
		data, err := os.ReadFile(name) //nolint:gosec // test reads its own package sources
		if err != nil {
			t.Fatal(err)
		}
		for _, forbidden := range []string{"orders.NewStore(", "orders.NewStoreWithGraph(", "orders.NewStoreWithTracking("} {
			if strings.Contains(string(data), forbidden) {
				t.Errorf("%s constructs an orders front door directly (%s...); route through orderFrontForStore / orderClassRoutingFor instead", name, forbidden)
			}
		}
	}
}

// TestDispatcherConstructionAdoptsClassRouting proves newMemoryOrderDispatcher
// resolves the city's orders-class routing at construction: on a migrated
// sqlite city its front doors write tracking records to the class store, not
// the scope store, and a routing failure latches fail-closed.
func TestDispatcherConstructionAdoptsClassRouting(t *testing.T) {
	cityPath := t.TempDir()
	writeOrdersMigratedMarker(t, cityPath)
	m := newMemoryOrderDispatcher(nil, cityPath, sqliteOrdersConfig(t), events.Discard, io.Discard)
	if m.orderRoutingErr != nil {
		t.Fatalf("orderRoutingErr = %v, want nil", m.orderRoutingErr)
	}
	if !m.orderRouting.routed {
		t.Fatal("dispatcher routing not routed on a migrated sqlite city")
	}
	scope := beads.NewMemStore()
	run, err := m.orderFront(scope).CreateRun("digest", orders.RunOpts{})
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	if _, err := scope.Get(run.ID); err == nil {
		t.Fatal("dispatcher-routed tracking create leaked into the scope store")
	}

	// bd city: byte-identical two-leg shape.
	bd := newMemoryOrderDispatcher(nil, t.TempDir(), nil, events.Discard, io.Discard)
	if bd.orderRouting.routed || bd.orderRoutingErr != nil {
		t.Fatalf("bd dispatcher routing = (%v, %v), want inactive", bd.orderRouting.routed, bd.orderRoutingErr)
	}
	bdScope := beads.NewMemStore()
	bdRun, err := bd.orderFront(bdScope).CreateRun("digest", orders.RunOpts{})
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	if _, err := bdScope.Get(bdRun.ID); err != nil {
		t.Fatalf("bd tracking create missing from the scope store: %v", err)
	}
}
