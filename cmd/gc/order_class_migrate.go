package main

// Seamless bd→sqlite orders migration (design "Seamless upgrade" +
// "Migration & cutover" row 1). On controller boot with
// [beads.classes.orders] backend="sqlite" and no migrated marker, the legacy
// order-tracking beads are imported into the class store, the marker is
// written (flipping routing for every process from that instant), and the
// bd-side tracking beads are cleared. The whole flow is idempotent and
// recomputed from live state — no status file beyond the marker itself — so
// an interrupted first boot simply resumes: imports are INSERT OR IGNORE,
// the marker write is atomic, and the residue sweep converges across boots.

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/gastownhall/gascity/internal/beads"
	ordersdb "github.com/gastownhall/gascity/internal/classdb/orders"
	"github.com/gastownhall/gascity/internal/config"
	"github.com/gastownhall/gascity/internal/orders"
)

// openOrderClassMigrationStore is the store-open seam for the migration and
// residue sweep (overridden by tests, mirroring newCityRuntimeOpenSweepStore).
var openOrderClassMigrationStore func(scopeRoot, cityPath string) (beads.Store, error) = openStoreAtForCity

// legacyOrderResidueOpenGrace protects in-flight single-flight markers written
// by a not-yet-upgraded process during the mixed-version window: the residue
// sweep leaves OPEN bd tracking beads younger than this alone (they either
// close normally on that process or go stale and age past the grace), so
// clearing residue can never double-fire a live dispatch.
const legacyOrderResidueOpenGrace = defaultOrderTrackingSweepStaleAfter

// orderClassMigrationResult summarizes one import pass.
type orderClassMigrationResult struct {
	imported int
	skipped  int
}

// ensureOrdersClassMigrated runs the orders-class migration when the config
// selects the sqlite backend and the migrated marker is absent: import →
// verify → marker → straggler re-import. Returns whether routing is (now)
// committed to the class store. Any failure aborts BEFORE the marker is
// written, so the city stays wholly on bd and the next boot retries —
// a partial import must never flip routing (rig scopes that failed to open
// would lose their cooldown/cursor history to the residue sweep).
func ensureOrdersClassMigrated(cityPath string, cfg *config.City, stderr io.Writer) bool {
	if cfg == nil || cfg.Beads.ClassBackend(config.BeadClassOrders) != config.BeadsClassBackendSQLite {
		return false
	}
	if _, err := os.Stat(ordersMigratedMarkerPath(cityPath)); err == nil {
		return true
	}
	class, err := ordersClassStoreFor(cityPath)
	if err != nil {
		fmt.Fprintf(stderr, "gc start: orders class migration: %v\n", err) //nolint:errcheck // best-effort stderr
		return false
	}
	stores, closeStores, err := openOrderClassMigrationStores(cityPath, cfg)
	if err != nil {
		// A scope that cannot be opened must abort the migration outright:
		// importing the visible scopes and flipping the marker would hand the
		// unopened scope's tracking beads to the residue sweep un-imported.
		closeStores()
		fmt.Fprintf(stderr, "gc start: orders class migration: %v\n", err) //nolint:errcheck // best-effort stderr
		return false
	}
	defer closeStores()

	policy := orderTrackingRetentionPolicyForConfig(cfg)
	result, err := migrateOrdersTrackingIntoClassStore(class, stores, policy)
	if err != nil {
		fmt.Fprintf(stderr, "gc start: orders class migration: %v\n", err) //nolint:errcheck // best-effort stderr
		return false
	}
	if err := writeOrdersMigratedMarkerFile(cityPath); err != nil {
		fmt.Fprintf(stderr, "gc start: orders class migration: %v\n", err) //nolint:errcheck // best-effort stderr
		return false
	}
	// Straggler pass: a run created between the import and the marker write
	// landed in bd; re-importing after the marker closes most of that window
	// (INSERT OR IGNORE keeps it idempotent). Best-effort — anything it still
	// misses is bounded by the at-most-one-extra-fire crash contract.
	if straggler, err := migrateOrdersTrackingIntoClassStore(class, stores, policy); err == nil {
		result.imported += straggler.imported
	}
	fmt.Fprintf(stderr, "gc start: orders class migrated to %s (%d runs imported, %d aged out)\n", ordersClassStorePath(cityPath), result.imported, result.skipped) //nolint:errcheck // best-effort stderr
	return true
}

// openOrderClassMigrationStores opens every orders scope store (city + each
// bound rig). Any open failure is fatal to the caller.
func openOrderClassMigrationStores(cityPath string, cfg *config.City) ([]beads.Store, func(), error) {
	targets := orderTrackingSweepTargetsForConfig(cityPath, cfg)
	var stores []beads.Store
	closeAll := func() {
		for _, store := range stores {
			closeBeadStoreHandle(store) //nolint:errcheck // best-effort close
		}
	}
	seen := make(map[string]struct{}, len(targets))
	for _, target := range targets {
		key := orderStoreTargetKey(target.target)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		store, err := openOrderClassMigrationStore(target.target.ScopeRoot, cityPath)
		if err != nil {
			return stores, closeAll, fmt.Errorf("opening %s order store: %w", target.label, err)
		}
		stores = append(stores, store)
	}
	return stores, closeAll, nil
}

// migrateOrdersTrackingIntoClassStore imports the bd-side order-tracking runs
// from the given scope stores into the class store. Selection mirrors the
// retention policy so nothing an operator sees regresses: per scoped name,
// every OPEN run, every closed run within the delete-after-close TTL, and the
// newest retain-floor closed runs regardless of age. The newest run always
// imports, and because the event cursor is forward-only it carries the max
// seq — so the cooldown clock and cursor high-water survive the cutover.
// Imported ids are read back from the class store (copy-verify); runs whose
// scoped name cannot be resolved are left to the residue sweep.
func migrateOrdersTrackingIntoClassStore(class *ordersdb.Store, stores []beads.Store, policy orderTrackingRetentionPolicy) (orderClassMigrationResult, error) {
	if policy.retainLast < minClosedOrderTrackingRetained {
		policy.retainLast = minClosedOrderTrackingRetained
	}
	byScoped := make(map[string][]orders.OrderRun)
	seenIDs := make(map[string]struct{})
	for i, store := range stores {
		runs, err := orderFrontForStore(store).RecentRunsAll(0)
		if err != nil {
			return orderClassMigrationResult{}, fmt.Errorf("listing order-tracking beads in %s: %w", orderTrackingSweepStoreLabel(store, i), err)
		}
		for _, run := range runs {
			if strings.TrimSpace(run.Scoped) == "" || run.CreatedAt.IsZero() {
				continue
			}
			if _, ok := seenIDs[run.ID]; ok {
				continue
			}
			seenIDs[run.ID] = struct{}{}
			byScoped[run.Scoped] = append(byScoped[run.Scoped], run)
		}
	}

	cutoff := time.Now().Add(-policy.deleteAfterClose)
	result := orderClassMigrationResult{}
	var imported []orders.OrderRun
	for _, runs := range byScoped {
		closedKept := 0
		// RecentRunsAll returns newest-first, so walking in order keeps the
		// newest retain-floor closed runs.
		for _, run := range runs {
			keep := run.Open ||
				closedKept < policy.retainLast ||
				!orderTrackingClosedReferenceTime(run).Before(cutoff)
			if !run.Open {
				if keep {
					closedKept++
				}
			}
			if !keep {
				result.skipped++
				continue
			}
			if err := class.ImportRun(run); err != nil {
				return result, err
			}
			imported = append(imported, run)
		}
	}
	// Copy-verify: every selected run must read back before the caller may
	// flip the marker.
	for _, run := range imported {
		if _, err := class.Get(run.ID); err != nil {
			return result, fmt.Errorf("verifying imported order run %q: %w", run.ID, err)
		}
	}
	result.imported = len(imported)
	return result, nil
}

// writeOrdersMigratedMarkerFile atomically writes the migrated marker that
// commits the city to class-store routing.
func writeOrdersMigratedMarkerFile(cityPath string) error {
	dir := ordersClassStoreDir(cityPath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("writing orders migrated marker: %w", err)
	}
	tmp, err := os.CreateTemp(dir, "orders.migrated.tmp*")
	if err != nil {
		return fmt.Errorf("writing orders migrated marker: %w", err)
	}
	name := tmp.Name()
	if _, err := fmt.Fprintf(tmp, "orders class migrated %s\n", time.Now().UTC().Format(time.RFC3339)); err != nil {
		_ = tmp.Close()
		_ = os.Remove(name)
		return fmt.Errorf("writing orders migrated marker: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(name)
		return fmt.Errorf("writing orders migrated marker: %w", err)
	}
	if err := os.Rename(name, ordersMigratedMarkerPath(cityPath)); err != nil {
		_ = os.Remove(name)
		return fmt.Errorf("writing orders migrated marker: %w", err)
	}
	return nil
}

// sweepLegacyOrderTrackingResidue deletes bd-side order-tracking beads on a
// MIGRATED city: the imported copies (now stale duplicates) and anything a
// pre-marker writer left behind. OPEN beads younger than the grace window are
// spared so a not-yet-upgraded process's in-flight single-flight marker is
// never yanked mid-dispatch; they age into a later boot's sweep. Deleting
// converges across boots (the redundancy principle), so a kill mid-sweep
// costs nothing. Returns the number of beads deleted.
func sweepLegacyOrderTrackingResidue(cityPath string, cfg *config.City, stderr io.Writer) int {
	if !ordersSQLiteRoutingActive(cityPath, cfg) {
		return 0
	}
	stores, closeStores, err := openOrderClassMigrationStores(cityPath, cfg)
	defer closeStores()
	if err != nil {
		fmt.Fprintf(stderr, "gc: orders legacy residue sweep: %v\n", err) //nolint:errcheck // best-effort stderr
		// Sweep whatever opened; the failed scope converges on a later boot.
	}
	now := time.Now()
	deleted := 0
	var errs []error
	for i, store := range stores {
		beadsList, err := store.List(beads.ListQuery{
			Label:         labelOrderTracking,
			IncludeClosed: true,
			Sort:          beads.SortCreatedDesc,
			TierMode:      beads.TierBoth,
		})
		if err != nil {
			errs = append(errs, fmt.Errorf("listing %s: %w", orderTrackingSweepStoreLabel(store, i), err))
			continue
		}
		ids := make([]string, 0, len(beadsList))
		for _, b := range beadsList {
			if b.Status != "closed" && now.Sub(b.CreatedAt) < legacyOrderResidueOpenGrace {
				continue
			}
			ids = append(ids, b.ID)
		}
		n, err := deleteLegacyOrderTrackingBeads(store, ids)
		deleted += n
		if err != nil {
			errs = append(errs, fmt.Errorf("deleting residue in %s: %w", orderTrackingSweepStoreLabel(store, i), err))
		}
	}
	if err := errors.Join(errs...); err != nil {
		fmt.Fprintf(stderr, "gc: orders legacy residue sweep (deleted %d): %v\n", deleted, err) //nolint:errcheck // best-effort stderr
	} else if deleted > 0 {
		fmt.Fprintf(stderr, "gc: orders legacy residue sweep: deleted %d migrated bd tracking beads\n", deleted) //nolint:errcheck // best-effort stderr
	}
	return deleted
}

// deleteLegacyOrderTrackingBeads removes the given beads, batching when the
// store supports it (tracking beads are dep-free, so a plain delete is safe).
func deleteLegacyOrderTrackingBeads(store beads.Store, ids []string) (int, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	if batcher, ok := store.(beads.BatchDeleter); ok {
		err := batcher.DeleteBatch(ids)
		if err == nil {
			return len(ids), nil
		}
		var partial *beads.BatchDeleteError
		if errors.As(err, &partial) {
			return len(partial.Committed), err
		}
		if !errors.Is(err, beads.ErrBatchDeleteUnsupported) {
			return 0, err
		}
	}
	deleted := 0
	var errs []error
	for _, id := range ids {
		if err := store.Delete(id); err != nil {
			if errors.Is(err, beads.ErrNotFound) {
				continue
			}
			errs = append(errs, err)
			continue
		}
		deleted++
	}
	return deleted, errors.Join(errs...)
}
