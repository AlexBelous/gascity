package main

// Orders-class backend routing (engdocs/design/infra-class-sqlite-stores.md):
// when [beads.classes.orders] selects the sqlite backend AND the city's
// orders migration has completed (the migrated marker exists), every orders
// front door routes tracking operations to the embedded class store at
// .gc/store/orders.db, keeping the resolved scope store as the graph leg so
// wisp-root order-run evidence keeps unioning. Until both hold, routing is
// the byte-identical bd shape — the migrated marker, not the binary version,
// decides routing, so a mixed-version window never splits the class across
// two backends.

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/gastownhall/gascity/internal/beads"
	ordersdb "github.com/gastownhall/gascity/internal/classdb/orders"
	"github.com/gastownhall/gascity/internal/config"
	"github.com/gastownhall/gascity/internal/orders"
)

// ordersClassStoreDir returns the per-class embedded-store directory.
func ordersClassStoreDir(cityPath string) string {
	return filepath.Join(cityPath, ".gc", "store")
}

// ordersClassStorePath returns the orders class store file.
func ordersClassStorePath(cityPath string) string {
	return filepath.Join(ordersClassStoreDir(cityPath), "orders.db")
}

// ordersMigratedMarkerPath returns the marker file whose presence commits the
// city to sqlite-backed orders routing. The migration slice writes it after
// the bd import completes (immediately, on a city with no legacy tracking
// beads).
func ordersMigratedMarkerPath(cityPath string) string {
	return filepath.Join(ordersClassStoreDir(cityPath), "orders.migrated")
}

// ordersSQLiteRoutingActive reports whether orders tracking operations route
// to the sqlite class store: the config selects the sqlite backend AND the
// migrated marker exists. Only an ABSENT marker means "not migrated"; any
// other stat failure (EACCES/EIO) is an error, not a bd fallback — guessing
// "bd" there would land writes where a routed reader on a migrated city
// never looks (the nudges-review ENOENT-only lesson).
func ordersSQLiteRoutingActive(cityPath string, cfg *config.City) (bool, error) {
	if cfg == nil || cfg.Beads.ClassBackend(config.BeadClassOrders) != config.BeadsClassBackendSQLite {
		return false, nil
	}
	if _, err := os.Stat(ordersMigratedMarkerPath(cityPath)); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, fmt.Errorf("checking orders migrated marker: %w", err)
	}
	return true, nil
}

// orderFrontResolver maps a resolved scope store to its orders front door.
type orderFrontResolver func(scope beads.Store) *orders.Store

// orderClassRouting carries a city's orders-class routing decision: how to
// build a front door over a resolved scope store, and whether the class is
// relocated to the sqlite store (which switches the retention delete off the
// graph-aware bd path and retires the event-cursor bd override).
type orderClassRouting struct {
	front  orderFrontResolver
	routed bool
}

// bdOrderClassRouting is the bd-backed routing: the byte-identical two-leg
// wrap of each scope store. It is the default for every city whose orders
// class has not relocated, and the fixed shape the sweep wrappers hand to
// tests.
func bdOrderClassRouting() orderClassRouting {
	return orderClassRouting{front: orderFrontForStore}
}

// ordersClassHandles is the process-wide cache of open orders class stores,
// one per database path. Handles live for the process: the controller's
// persistent handle per the design, and for CLI one-shots the process exits
// promptly — the G0 SIGKILL gate proves WAL durability never depends on a
// clean close, so process exit without Close is safe. Connections open
// lazily, so a one-shot pays for the connections it uses, not the pool cap.
var ordersClassHandles struct {
	mu     sync.Mutex
	byPath map[string]*ordersdb.Store
}

// ordersClassStoreFor returns the process-shared handle for a city's orders
// class store, opening (and migrating) it on first use.
func ordersClassStoreFor(cityPath string) (*ordersdb.Store, error) {
	path := ordersClassStorePath(cityPath)
	ordersClassHandles.mu.Lock()
	defer ordersClassHandles.mu.Unlock()
	if st, ok := ordersClassHandles.byPath[path]; ok {
		return st, nil
	}
	st, err := ordersdb.Open(path)
	if err != nil {
		return nil, fmt.Errorf("opening orders class store: %w", err)
	}
	if ordersClassHandles.byPath == nil {
		ordersClassHandles.byPath = make(map[string]*ordersdb.Store)
	}
	ordersClassHandles.byPath[path] = st
	return st, nil
}

// OrderFrontDoor implements internal/api's optional orderFrontDoorProvider:
// the API layer's orders feed/check paths construct their front doors through
// the controller's class routing, so a migrated city's API reads come from
// the class store. An error means the routed class store is unavailable — the
// API fails the read rather than silently reading bd.
func (cs *controllerState) OrderFrontDoor(scope beads.Store) (*orders.Store, error) {
	cs.mu.RLock()
	cityPath, cfg := cs.cityPath, cs.cfg
	cs.mu.RUnlock()
	routing, err := orderClassRoutingFor(cityPath, cfg)
	if err != nil {
		return nil, err
	}
	return routing.front(scope), nil
}

// orderClassRoutingFor resolves a city's orders-class routing. Inactive
// routing costs nothing (no file opened). Active routing opens the class
// store; the error is returned rather than falling back to bd, because a
// silent bd fallback on a migrated city would split the class across two
// backends (writes landing where reads no longer look).
func orderClassRoutingFor(cityPath string, cfg *config.City) (orderClassRouting, error) {
	active, err := ordersSQLiteRoutingActive(cityPath, cfg)
	if err != nil {
		return orderClassRouting{}, err
	}
	if !active {
		return bdOrderClassRouting(), nil
	}
	class, err := ordersClassStoreFor(cityPath)
	if err != nil {
		return orderClassRouting{}, err
	}
	return orderClassRouting{
		front: func(scope beads.Store) *orders.Store {
			return orders.NewStoreWithTracking(class, beads.GraphStore{Store: scope})
		},
		routed: true,
	}, nil
}
