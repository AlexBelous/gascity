package main

// Graph-class backend routing (engdocs/design/graph-store-backend-selection.md
// + engdocs/design/infra-class-sqlite-stores.md "Relationship to the graph
// class"): when [beads.classes.graph] selects the sqlite backend AND the
// city's graph migration has completed (the migrated marker exists), graph
// reads and writes route to the embedded SQLiteStore at
// .gc/store/graph/beads.sqlite, minting the reserved gcg prefix. Until both
// hold, routing is the byte-identical work-store shape.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
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
	// Terminal retention is DISABLED on the graph class store: the ported
	// store's 4h purgeTerminal default would delete closed steps out from
	// under still-running workflows — drains re-count their manifests, and
	// workflow-finalize's outcome vote reads closed step results, so a
	// purged failed step silently flips the vote to PASS (gap N07/N10/N11).
	// Whole-tree cleanup belongs to workflow GC, which reasons about the
	// root's lifecycle, not row age.
	st, err := beads.OpenSQLiteStore(dir,
		beads.WithSQLiteStoreIDPrefix(prefix),
		beads.WithSQLiteStoreRetention(0, 0))
	if err != nil {
		return nil, fmt.Errorf("opening graph class store: %w", err)
	}
	sq, ok := st.(*beads.SQLiteStore)
	if !ok {
		return nil, fmt.Errorf("opening graph class store: unexpected store type %T", st)
	}
	// Re-apply the deploy-lineage id floor on every open. On a window-3 combined
	// scope import, non-graph gcg-<n> ids were reclassified into sibling stores
	// while the graph store keeps minting gcg-<n>; recoverSequence only sees this
	// store's own rows, so without this a post-cutover graph mint could reissue a
	// suffix an imported session/order/message/nudge bead already holds. The floor
	// file records the global max gcg suffix (see graph_class_migrate.go); the
	// bump is idempotent and a no-op once the store's own rows exceed it.
	floor, err := readGraphSeqFloor(cityPath)
	if err != nil {
		// Fail CLOSED: a present-but-unreadable floor sidecar must NOT open the
		// graph store with floor 0 — that silently re-arms the very re-mint
		// collision the floor exists to prevent (the graph store could reissue a
		// gcg-<n> a sibling class store already owns). Surface it; the migration /
		// caller aborts rather than corrupt ids. Do not cache the handle.
		_ = closeBeadStoreHandle(sq) //nolint:errcheck // best-effort close
		return nil, fmt.Errorf("opening graph class store: %w", err)
	}
	if floor > 0 {
		sq.AdvanceSequenceFloor(floor)
	}
	if graphClassHandles.byDir == nil {
		graphClassHandles.byDir = make(map[string]*beads.SQLiteStore)
	}
	graphClassHandles.byDir[dir] = sq
	return sq, nil
}

// graphSeqFloorPath is the durable id-floor sidecar the deploy-lineage graph
// migration writes and graphClassStoreFor re-applies on open.
func graphSeqFloorPath(cityPath string) string {
	return filepath.Join(graphClassStoreDir(cityPath), "graph.seqfloor")
}

// readGraphSeqFloor returns the persisted graph id floor. It returns (0, nil)
// ONLY when the sidecar is genuinely absent (a fresh our-branch city — DARK). A
// present-but-unreadable or corrupt/negative sidecar returns a non-nil error:
// the collision guard must never be silently disabled by a read/parse failure,
// which fail-open (returning 0) would do.
func readGraphSeqFloor(cityPath string) (int64, error) {
	raw, err := os.ReadFile(graphSeqFloorPath(cityPath))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return 0, nil
		}
		return 0, fmt.Errorf("reading graph seq floor %s: %w", graphSeqFloorPath(cityPath), err)
	}
	n, err := strconv.ParseInt(strings.TrimSpace(string(raw)), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("parsing graph seq floor %s: %w", graphSeqFloorPath(cityPath), err)
	}
	if n < 0 {
		return 0, fmt.Errorf("graph seq floor %s is negative (%d)", graphSeqFloorPath(cityPath), n)
	}
	return n, nil
}

// writeGraphSeqFloor atomically persists the graph id floor (temp + rename), the
// same durability shape as the migrated markers.
func writeGraphSeqFloor(cityPath string, floor int64) error {
	if floor <= 0 {
		return nil
	}
	dir := graphClassStoreDir(cityPath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("writing graph seq floor: %w", err)
	}
	tmp, err := os.CreateTemp(dir, "graph.seqfloor.tmp-*")
	if err != nil {
		return fmt.Errorf("writing graph seq floor: %w", err)
	}
	name := tmp.Name()
	if _, err := fmt.Fprintf(tmp, "%d\n", floor); err != nil {
		_ = tmp.Close()
		_ = os.Remove(name)
		return fmt.Errorf("writing graph seq floor: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(name)
		return fmt.Errorf("writing graph seq floor: %w", err)
	}
	if err := os.Rename(name, graphSeqFloorPath(cityPath)); err != nil {
		_ = os.Remove(name)
		return fmt.Errorf("writing graph seq floor: %w", err)
	}
	return nil
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
		// Inherit the controller's emission wrapper when it exists: EVERY
		// routed-store seam must emit bead.* on lifecycle writes, not just
		// the one resolve path. Convergence's adapter (SetMetadata/Close/
		// Delete on step beads) and the order-dispatch by-id update reach
		// the store through here; without the wrapper their writes were
		// silent and the runs views froze steps at creation status.
		return graphStoreMaybeWithEvents(class, cityPath)
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

// appendRoutedGraphStore appends the routed graph store to a work-store
// fan-out on a graph-routed city. Fail-closed: a marked city whose routing
// or store cannot resolve returns the error so liveness gates treat the
// probe as unanswerable (callers already fail safe to "has work" on error)
// rather than reading graph-assigned work as absent and destroying a live
// session.
func appendRoutedGraphStore(stores []beads.Store, cityPath string, cfg *config.City) ([]beads.Store, error) {
	st, routed, err := routedGraphStoreFor(cityPath, cfg)
	if err != nil {
		return nil, fmt.Errorf("graph-class store for assigned-work fan-out: %w", err)
	}
	if routed {
		return append(stores, graphStoreMaybeWithEvents(st, cityPath)), nil
	}
	return stores, nil
}

// routedGraphStoreOrWarn resolves the routed graph store best-effort for
// recovery/release scans: nil on an unrouted city, nil WITH a logged warning
// when a marked city's routing cannot resolve (the scan then skips the graph
// leg — the affected steps stay assigned, which is the pre-existing state,
// rather than blocking the whole recovery pass).
func routedGraphStoreOrWarn(cityPath string, cfg *config.City, stderr io.Writer) beads.Store {
	st, routed, err := routedGraphStoreFor(cityPath, cfg)
	if err != nil {
		if stderr != nil {
			fmt.Fprintf(stderr, "gc: graph-class store unavailable for assigned-work scan: %v\n", err) //nolint:errcheck // best-effort stderr
		}
		return nil
	}
	if !routed {
		return nil
	}
	return graphStoreMaybeWithEvents(st, cityPath)
}

// graphStoreForID returns the store that owns id for a by-id mutation on a
// graph-routed city: the embedded graph store when it holds the row (or the
// id carries the reserved prefix), else fallback. Routing errors fall back
// too — the caller's own write then fails loud on the missing row rather
// than silently landing in the wrong store.
func graphStoreForID(cityPath string, cfg *config.City, fallback beads.Store, id string) beads.Store {
	st, routed, err := routedGraphStoreFor(cityPath, cfg)
	if err != nil || !routed {
		return fallback
	}
	if prefix, ok := config.ReservedClassPrefix(config.BeadClassGraph); ok && strings.HasPrefix(id, prefix+"-") {
		return graphStoreMaybeWithEvents(st, cityPath)
	}
	if _, gerr := st.Get(id); gerr == nil {
		return graphStoreMaybeWithEvents(st, cityPath)
	}
	return fallback
}

// graphStoreForIDIfOwned returns the routed graph store when it owns id
// (reserved gcg- prefix, or a migrated legacy id resident in the store), else
// nil. It is the read-side twin of graphStoreForID for callers that need to
// know whether to route at all rather than pick between two stores.
func graphStoreForIDIfOwned(cityPath string, cfg *config.City, id string) beads.Store {
	st, routed, err := routedGraphStoreFor(cityPath, cfg)
	if err != nil || !routed {
		return nil
	}
	if prefix, ok := config.ReservedClassPrefix(config.BeadClassGraph); ok && strings.HasPrefix(id, prefix+"-") {
		return graphStoreMaybeWithEvents(st, cityPath)
	}
	if _, gerr := st.Get(id); gerr == nil {
		return graphStoreMaybeWithEvents(st, cityPath)
	}
	return nil
}
