package main

// Class-store maintenance wiring (engdocs/design/infra-class-sqlite-stores.md,
// "Doctor / storehealth / maintenance"): the controller starts each routed
// class store's own WAL-checkpoint + periodic-VACUUM loop at boot, next to
// the retention sweepers. Only Dolt had a maintenance loop before; the
// .gc/store files would otherwise accumulate WAL for the controller's
// lifetime and never return space reclaimed by retention DELETEs.

import (
	"fmt"
	"io"
	"time"

	messagingdb "github.com/gastownhall/gascity/internal/classdb/messaging"
	nudgesdb "github.com/gastownhall/gascity/internal/classdb/nudges"
	sessionsdb "github.com/gastownhall/gascity/internal/classdb/sessions"
	"github.com/gastownhall/gascity/internal/config"
)

const (
	// classStoreCheckpointInterval is the WAL-checkpoint cadence. The class
	// stores see steady small writes (nudge queue ticks, session
	// transitions), so a modest cadence keeps the WAL bounded without
	// contending with the write path.
	classStoreCheckpointInterval = 15 * time.Minute
	// classStoreVacuumInterval is the slow VACUUM cadence that returns the
	// space retention sweeps free.
	classStoreVacuumInterval = 24 * time.Hour
)

// maintainableClassStore is the maintenance surface every class store
// exposes (once-guarded per process-shared handle).
type maintainableClassStore interface {
	StartMaintenance(checkpointInterval, vacuumInterval time.Duration, warn io.Writer)
}

// startClassStoreMaintenance starts the maintenance loop on every class
// store that is routed for this city. Idempotent per process-shared handle
// (each store's StartMaintenance is once-guarded), so controller reloads
// never stack loops. A routing failure for one class is reported and skips
// only that class — maintenance is best-effort plumbing, not a
// correctness gate.
func startClassStoreMaintenance(cityPath string, cfg *config.City, stderr io.Writer) {
	classes := []struct {
		name   string
		routed func() (bool, error)
		open   func() (maintainableClassStore, error)
	}{
		{
			config.BeadClassOrders,
			func() (bool, error) { return ordersSQLiteRoutingActive(cityPath, cfg) },
			func() (maintainableClassStore, error) { return ordersClassStoreFor(cityPath) },
		},
		{
			config.BeadClassNudges,
			func() (bool, error) { return nudgesdb.Routed(cityPath, cfg) },
			func() (maintainableClassStore, error) { return nudgesdb.SharedStoreFor(cityPath) },
		},
		{
			config.BeadClassMessaging,
			func() (bool, error) { return messagingdb.Routed(cityPath, cfg) },
			func() (maintainableClassStore, error) { return messagingClassStoreHandle(cityPath) },
		},
		{
			config.BeadClassSessions,
			func() (bool, error) { return sessionsdb.Routed(cityPath, cfg) },
			func() (maintainableClassStore, error) { return sessionsdb.SharedStoreFor(cityPath) },
		},
	}
	for _, cl := range classes {
		routed, err := cl.routed()
		if err != nil {
			fmt.Fprintf(stderr, "gc start: %s class maintenance: %v\n", cl.name, err) //nolint:errcheck // best-effort stderr
			continue
		}
		if !routed {
			continue
		}
		st, err := cl.open()
		if err != nil {
			fmt.Fprintf(stderr, "gc start: %s class maintenance: %v\n", cl.name, err) //nolint:errcheck // best-effort stderr
			continue
		}
		st.StartMaintenance(classStoreCheckpointInterval, classStoreVacuumInterval, stderr)
	}
}
