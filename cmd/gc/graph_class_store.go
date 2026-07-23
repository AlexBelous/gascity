package main

// Graph-class backend routing (engdocs/design/graph-store-backend-selection.md
// + engdocs/design/infra-class-sqlite-stores.md "Relationship to the graph
// class"): when [beads.classes.graph] selects the sqlite backend AND the
// city's graph migration has completed (the migrated marker exists), graph
// reads and writes route to the embedded SQLiteStore at
// .gc/store/graph/beads.sqlite, minting the reserved gcg prefix. Until both
// hold, routing is the byte-identical work-store shape.
//
// DARK UNTIL THE WIRING COMPLETES: config validation still rejects
// backend="sqlite" for graph (sqliteCapableBeadClasses has no graph entry),
// so this routing cannot activate on any real city. The ratchet flips only
// in the final wiring slice, after the create-side dispatch
// (beadPolicyStore.createTarget / graphApplierFor), the doBd in-process
// mutation arm, and the ready/claim federation all route — flipping earlier
// would split the class across two backends. Same discipline as the four
// landed classes; graph just has more roots.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/gastownhall/gascity/internal/beads"
	nudgesdb "github.com/gastownhall/gascity/internal/classdb/nudges"
	sessionsdb "github.com/gastownhall/gascity/internal/classdb/sessions"
	"github.com/gastownhall/gascity/internal/config"
)

// graphClassStoreDir returns the graph store's directory. The store file is
// OpenSQLiteStore's fixed beads.sqlite inside it (the proven opener takes a
// directory), so the graph class nests one level under the flat
// .gc/store/<class>.db convention; storehealth's TotalSize walk covers it
// either way.
func graphClassStoreDir(cityPath string) string {
	return filepath.Join(nudgesdb.StoreDir(cityPath), "graph")
}

// graphClassStorePath returns the graph class store file.
func graphClassStorePath(cityPath string) string {
	return filepath.Join(graphClassStoreDir(cityPath), "beads.sqlite")
}

// graphMigratedMarkerPath returns the marker file whose presence commits the
// city to sqlite-backed graph routing.
func graphMigratedMarkerPath(cityPath string) string {
	return filepath.Join(nudgesdb.StoreDir(cityPath), "graph.migrated")
}

// graphSQLiteRoutingActive reports whether graph-class operations route to
// the embedded store: the config selects the sqlite backend AND the migrated
// marker exists. ENOENT-only marker discipline, same as the other classes:
// any other stat failure is an error, never a silent bd fallback.
func graphSQLiteRoutingActive(cityPath string, cfg *config.City) (bool, error) {
	if cityPath == "" || cfg == nil || cfg.Beads.ClassBackend(config.BeadClassGraph) != config.BeadsClassBackendSQLite {
		return false, nil
	}
	if _, err := os.Stat(graphMigratedMarkerPath(cityPath)); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, fmt.Errorf("checking graph migrated marker: %w", err)
	}
	return true, nil
}

// graphClassHandles is the process-wide cache of open graph class stores,
// one per store directory. Handles live for the process, matching the other
// class stores' shared-handle model.
var graphClassHandles struct {
	mu    sync.Mutex
	byDir map[string]*beads.SQLiteStore
}

// graphClassStoreFor returns the process-shared handle for a city's graph
// class store, opening it (and applying schema) on first use. The store
// mints the reserved gcg prefix — the namespace the by-id federation and
// the API graph arms route on.
func graphClassStoreFor(cityPath string) (*beads.SQLiteStore, error) {
	dir := graphClassStoreDir(cityPath)
	graphClassHandles.mu.Lock()
	defer graphClassHandles.mu.Unlock()
	if st, ok := graphClassHandles.byDir[dir]; ok {
		return st, nil
	}
	prefix, _ := config.ReservedClassPrefix(config.BeadClassGraph)
	st, err := beads.OpenSQLiteStore(dir, beads.WithSQLiteStoreIDPrefix(prefix))
	if err != nil {
		return nil, fmt.Errorf("opening graph class store: %w", err)
	}
	sq, ok := st.(*beads.SQLiteStore)
	if !ok {
		return nil, fmt.Errorf("opening graph class store: unexpected store type %T", st)
	}
	if graphClassHandles.byDir == nil {
		graphClassHandles.byDir = make(map[string]*beads.SQLiteStore)
	}
	graphClassHandles.byDir[dir] = sq
	return sq, nil
}

// routedGraphStoreFor resolves a city's graph-class routing and, when routed,
// the process-shared store handle. Fail-closed: a marked city whose routing
// cannot be resolved or whose store cannot open returns the error — callers
// must NOT fall back to the work store, which a routed reader never sees.
func routedGraphStoreFor(cityPath string, cfg *config.City) (*beads.SQLiteStore, bool, error) {
	active, err := graphSQLiteRoutingActive(cityPath, cfg)
	if err != nil || !active {
		return nil, false, err
	}
	st, err := graphClassStoreFor(cityPath)
	if err != nil {
		return nil, false, err
	}
	return st, true, nil
}

// resolveGraphStoreRouted is the routing arm resolveGraphStore delegates to:
// the embedded graph store on a routed city, the work store otherwise, and a
// fail-closed erroring store when a marked city's routing/store cannot
// resolve (reusing the generic unavailable-store shape; the error carries
// the graph branding).
func resolveGraphStoreRouted(workStore beads.Store, cfg *config.City, cityPath string) beads.Store {
	class, routed, err := routedGraphStoreFor(cityPath, cfg)
	if err != nil {
		return sessionsdb.NewUnavailableStore(fmt.Errorf("graph-class store unavailable: %w", err))
	}
	if routed {
		return class
	}
	return workStore
}

// unavailableGraphApplier is the fail-closed graph-apply arm: a marked city
// whose graph routing or store cannot resolve must fail the pour rather
// than apply it to the work store, which routed readers never see.
type unavailableGraphApplier struct{ err error }

// ApplyGraphPlan fails with the routing error.
func (u unavailableGraphApplier) ApplyGraphPlan(context.Context, *beads.GraphApplyPlan) (*beads.GraphApplyResult, error) {
	return nil, u.err
}
