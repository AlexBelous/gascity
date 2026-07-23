package main

// Seamless graph-class cutover (the P1–P4 boot-migration template, applied
// to the biggest class): on a controller boot where [beads.classes.graph]
// selects sqlite and the migrated marker is absent, the bd store's OPEN
// graph-class beads — molecule roots, steps, convoys' graph legs, wisps
// (both tiers) — import into the embedded graph store with their ids
// preserved (CreateWithForeignID) and their WITHIN-GRAPH dep edges re-added,
// then copy-verify, then the atomic marker flips routing, then a background
// residue sweep clears the bd copies (open beads younger than the grace are
// spared for the mixed-version window; unknown open beads merge-import
// before any sweep on later boots). Closed graph beads deliberately do NOT
// cross: the store's own retention purges terminal rows within hours, and
// nothing replays closed molecule topology.

import (
	"fmt"
	"io"
	"os"
	"time"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/config"
	"github.com/gastownhall/gascity/internal/coordclass"
)

const (
	// legacyGraphResidueOpenGrace protects a not-yet-upgraded process's
	// fresh bd graph write (an in-flight molecule pour) from deletion
	// before a boot's import-then-sweep has converged it.
	legacyGraphResidueOpenGrace = 10 * time.Minute
)

// openGraphClassMigrationStore is the bd-store open seam for the migration
// and residue sweep (overridden by tests, mirroring the other classes).
var openGraphClassMigrationStore func(cityPath string) (beads.Store, error) = func(cityPath string) (beads.Store, error) {
	return openStoreAtForCity(cityPath, cityPath)
}

// ensureGraphClassMigrated performs the seamless graph-class cutover on
// controller boot. Returns true when the city is (now) migrated; false
// leaves the city on bd with the reason on stderr — the marker is never
// written unless the import and copy-verify completed.
func ensureGraphClassMigrated(cityPath string, cfg *config.City, stderr io.Writer) bool {
	if cfg == nil || cfg.Beads.ClassBackend(config.BeadClassGraph) != config.BeadsClassBackendSQLite {
		return false
	}
	if _, err := os.Stat(graphMigratedMarkerPath(cityPath)); err == nil {
		return true
	}
	class, err := graphClassStoreFor(cityPath)
	if err != nil {
		fmt.Fprintf(stderr, "gc start: graph class migration: %v\n", err) //nolint:errcheck // best-effort stderr
		return false
	}
	store, err := openGraphClassMigrationStore(cityPath)
	if err != nil {
		fmt.Fprintf(stderr, "gc start: graph class migration: %v\n", err) //nolint:errcheck // best-effort stderr
		return false
	}
	defer closeBeadStoreHandle(store) //nolint:errcheck // best-effort close

	imported, err := migrateGraphIntoClassStore(class, store)
	if err != nil {
		fmt.Fprintf(stderr, "gc start: graph class migration: %v\n", err) //nolint:errcheck // best-effort stderr
		return false
	}
	if err := writeGraphMigratedMarkerFile(cityPath); err != nil {
		fmt.Fprintf(stderr, "gc start: graph class migration: %v\n", err) //nolint:errcheck // best-effort stderr
		return false
	}
	// Straggler pass: a pour racing the marker flip merge-imports here;
	// anything still missed converges on a later boot's residue sweep.
	if stragglers, err := importGraphSnapshot(class, store, false); err == nil {
		imported += stragglers
	}
	fmt.Fprintf(stderr, "gc start: graph class migrated to %s (%d open graph beads imported)\n", //nolint:errcheck // best-effort stderr
		graphClassStorePath(cityPath), imported)
	return true
}

// migrateGraphIntoClassStore imports the bd store's current open graph
// truth. It first RESETS the class store (an interrupted earlier attempt
// must never resurrect state the still-bd city has since progressed), then
// imports with copy-verify. Runs strictly before the marker, so the bd
// store is still the authority being copied.
func migrateGraphIntoClassStore(class *beads.SQLiteStore, store beads.Store) (int, error) {
	existing, err := class.List(beads.ListQuery{IncludeClosed: true, TierMode: beads.TierBoth, AllowScan: true})
	if err != nil {
		return 0, fmt.Errorf("listing class store for reset: %w", err)
	}
	for _, b := range existing {
		if err := class.Delete(b.ID); err != nil {
			return 0, fmt.Errorf("resetting class store row %s: %w", b.ID, err)
		}
	}
	return importGraphSnapshot(class, store, true)
}

// importGraphSnapshot copies the bd store's open graph-class beads (both
// tiers) and their within-graph dep edges into the class store. Already
// present ids are skipped (merge semantics for the straggler/residue
// passes). verify re-reads every imported id before returning.
func importGraphSnapshot(class *beads.SQLiteStore, store beads.Store, verify bool) (int, error) {
	rows, err := store.List(beads.ListQuery{TierMode: beads.TierBoth, AllowScan: true})
	if err != nil {
		return 0, fmt.Errorf("listing bd graph beads: %w", err)
	}
	graphRows := make([]beads.Bead, 0, len(rows))
	graphIDs := make(map[string]bool, len(rows))
	for _, b := range rows {
		if coordclass.Classify(b) != coordclass.ClassGraph || b.Status == "closed" {
			continue
		}
		graphRows = append(graphRows, b)
		graphIDs[b.ID] = true
	}
	imported := 0
	for _, b := range graphRows {
		if _, err := class.Get(b.ID); err == nil {
			continue // already present (straggler/residue merge)
		}
		if _, err := class.CreateWithForeignID(b); err != nil {
			return imported, fmt.Errorf("importing graph bead %s: %w", b.ID, err)
		}
		imported++
	}
	// Within-graph dep edges (the molecule topology). Cross-boundary
	// relationships stay metadata linkage per the split design.
	for _, b := range graphRows {
		deps, err := store.DepList(b.ID, "down")
		if err != nil {
			return imported, fmt.Errorf("listing deps of %s: %w", b.ID, err)
		}
		for _, d := range deps {
			if !graphIDs[d.DependsOnID] {
				continue
			}
			if err := class.DepAdd(b.ID, d.DependsOnID, d.Type); err != nil {
				return imported, fmt.Errorf("importing dep %s -> %s: %w", b.ID, d.DependsOnID, err)
			}
		}
	}
	if verify {
		for _, b := range graphRows {
			if _, err := class.Get(b.ID); err != nil {
				return imported, fmt.Errorf("copy-verify %s: %w", b.ID, err)
			}
		}
	}
	return imported, nil
}

// writeGraphMigratedMarkerFile writes the routing marker atomically
// (temp + rename), the same shape as the other classes.
func writeGraphMigratedMarkerFile(cityPath string) error {
	if err := os.MkdirAll(graphClassStoreDir(cityPath), 0o755); err != nil {
		return fmt.Errorf("writing graph migrated marker: %w", err)
	}
	tmp, err := os.CreateTemp(graphClassStoreDir(cityPath), "graph.migrated.tmp-*")
	if err != nil {
		return fmt.Errorf("writing graph migrated marker: %w", err)
	}
	name := tmp.Name()
	if _, err := fmt.Fprintf(tmp, "graph class migrated %s\n", time.Now().UTC().Format(time.RFC3339)); err != nil {
		_ = tmp.Close()
		_ = os.Remove(name)
		return fmt.Errorf("writing graph migrated marker: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(name)
		return fmt.Errorf("writing graph migrated marker: %w", err)
	}
	if err := os.Rename(name, graphMigratedMarkerPath(cityPath)); err != nil {
		_ = os.Remove(name)
		return fmt.Errorf("writing graph migrated marker: %w", err)
	}
	return nil
}

// sweepLegacyGraphResidue clears bd-side graph-class beads on a MIGRATED
// city: first merge-import any open bead the class store does not own (an
// enqueue racing the marker, or an old binary's post-marker pour), then
// delete the bd copies — closed ones and class-owned open ones
// immediately, unknown fresh open ones spared by the grace window for the
// next boot's import-then-sweep. Converges across boots.
func sweepLegacyGraphResidue(cityPath string, cfg *config.City, stderr io.Writer) int {
	active, err := graphSQLiteRoutingActive(cityPath, cfg)
	if err != nil || !active {
		if err != nil {
			fmt.Fprintf(stderr, "gc: graph legacy residue sweep: %v\n", err) //nolint:errcheck // best-effort stderr
		}
		return 0
	}
	class, err := graphClassStoreFor(cityPath)
	if err != nil {
		fmt.Fprintf(stderr, "gc: graph legacy residue sweep: %v\n", err) //nolint:errcheck // best-effort stderr
		return 0
	}
	store, err := openGraphClassMigrationStore(cityPath)
	if err != nil {
		fmt.Fprintf(stderr, "gc: graph legacy residue sweep: %v\n", err) //nolint:errcheck // best-effort stderr
		return 0
	}
	defer closeBeadStoreHandle(store) //nolint:errcheck // best-effort close

	if _, err := importGraphSnapshot(class, store, false); err != nil {
		fmt.Fprintf(stderr, "gc: graph legacy residue sweep: import: %v\n", err) //nolint:errcheck // best-effort stderr
		return 0                                                                 // never delete what we could not first converge
	}
	rows, err := store.List(beads.ListQuery{IncludeClosed: true, TierMode: beads.TierBoth, AllowScan: true})
	if err != nil {
		fmt.Fprintf(stderr, "gc: graph legacy residue sweep: %v\n", err) //nolint:errcheck // best-effort stderr
		return 0
	}
	now := time.Now()
	deleted := 0
	for _, b := range rows {
		if coordclass.Classify(b) != coordclass.ClassGraph {
			continue
		}
		if b.Status != "closed" {
			if _, err := class.Get(b.ID); err != nil && now.Sub(b.CreatedAt) < legacyGraphResidueOpenGrace {
				continue // fresh unknown open bead: spared for the next boot
			}
		}
		if err := store.Delete(b.ID); err != nil {
			fmt.Fprintf(stderr, "gc: graph legacy residue sweep: deleting %s: %v\n", b.ID, err) //nolint:errcheck // best-effort stderr
			continue
		}
		deleted++
	}
	return deleted
}
