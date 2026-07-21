package main

// Seamless bd→sqlite nudges migration (design "Seamless upgrade" +
// "Migration & cutover" row 2: drain-or-import the live queue; shadow
// history ≤24h). On controller boot with [beads.classes.nudges]
// backend="sqlite" and no migrated marker, the live file-queue items and
// the recent terminal shadow history are imported into the class store, the
// marker is written (flipping routing for every process from that instant),
// and the legacy residue — imported file items plus bd shadow beads — is
// cleared in the background. The whole flow is idempotent and recomputed
// from live state (imports are INSERT OR IGNORE, the marker write is
// atomic, the residue sweep converges across boots), so an interrupted
// first boot simply resumes.

import (
	"fmt"
	"io"
	"os"
	"time"

	"github.com/gastownhall/gascity/internal/beads"
	nudgesdb "github.com/gastownhall/gascity/internal/classdb/nudges"
	"github.com/gastownhall/gascity/internal/config"
	"github.com/gastownhall/gascity/internal/nudgequeue"
)

// openNudgeClassMigrationStore is the shadow-store open seam for the
// migration and residue sweep (overridden by tests, mirroring
// openOrderClassMigrationStore).
var openNudgeClassMigrationStore func(cityPath string) (beads.Store, error) = func(cityPath string) (beads.Store, error) {
	return openStoreAtForCity(cityPath, cityPath)
}

// legacyNudgeResidueOpenGrace protects a not-yet-upgraded process's
// in-flight shadow writes during the mixed-version window: the residue
// sweep leaves OPEN shadow beads younger than this alone unless the class
// store already owns their id. It matches the nudge-mail sweep's stale
// threshold — past it, an old binary's item has either delivered or
// dead-lettered (2s poll cadence, 5 attempts × 15s backoff).
const legacyNudgeResidueOpenGrace = nudgeMailSweepDefaultNudgeTTL

// nudgeRetentionSweepInterval is the cadence of the routed class store's
// terminal-row retention sweep on the controller.
const nudgeRetentionSweepInterval = 15 * time.Minute

// nudgeClassMigrationResult summarizes one import pass.
type nudgeClassMigrationResult struct {
	liveImported     int
	terminalImported int
	skipped          int
}

// ensureNudgesClassMigrated runs the nudges-class migration when the config
// selects the sqlite backend and the migrated marker is absent: import
// (live queue + ≤24h terminal shadow history) → copy-verify → marker →
// straggler re-import. Returns whether routing is (now) committed to the
// class store. Any failure aborts BEFORE the marker is written, so the city
// stays wholly on the file backend and the next boot retries — a partial
// import must never flip routing (queued deliveries would vanish from the
// routed readers).
func ensureNudgesClassMigrated(cityPath string, cfg *config.City, stderr io.Writer) bool {
	if cfg == nil || cfg.Beads.ClassBackend(config.BeadClassNudges) != config.BeadsClassBackendSQLite {
		return false
	}
	if _, err := os.Stat(nudgesdb.MigratedMarkerPath(cityPath)); err == nil {
		return true
	}
	class, err := nudgesdb.SharedStoreFor(cityPath)
	if err != nil {
		fmt.Fprintf(stderr, "gc start: nudges class migration: %v\n", err) //nolint:errcheck // best-effort stderr
		return false
	}
	store, err := openNudgeClassMigrationStore(cityPath)
	if err != nil {
		// The shadow store holds the ≤24h terminal history the wait paths
		// still need after the cutover; importing without it would wedge
		// in-flight waits, so an unopenable store aborts outright.
		fmt.Fprintf(stderr, "gc start: nudges class migration: %v\n", err) //nolint:errcheck // best-effort stderr
		return false
	}
	defer closeBeadStoreHandle(store) //nolint:errcheck // best-effort close
	front := nudgequeue.NewStore(beads.NudgesStore{Store: resolveNudgesStore(store, cfg, cityPath, nil)})

	result, err := migrateNudgeQueueIntoClassStore(class, cityPath, front, time.Now())
	if err != nil {
		fmt.Fprintf(stderr, "gc start: nudges class migration: %v\n", err) //nolint:errcheck // best-effort stderr
		return false
	}
	if err := writeNudgesMigratedMarkerFile(cityPath); err != nil {
		fmt.Fprintf(stderr, "gc start: nudges class migration: %v\n", err) //nolint:errcheck // best-effort stderr
		return false
	}
	// Straggler pass: an item enqueued to the file queue between the import
	// and the marker write re-imports here (INSERT OR IGNORE keeps it
	// idempotent). Best-effort — anything still missed is imported by a
	// later boot before that boot's residue sweep clears it.
	if straggler, err := importLiveNudgeQueue(class, cityPath); err == nil {
		result.liveImported += len(straggler)
	}
	fmt.Fprintf(stderr, "gc start: nudges class migrated to %s (%d live items, %d terminal records imported, %d aged out)\n", nudgesdb.StorePath(cityPath), result.liveImported, result.terminalImported, result.skipped) //nolint:errcheck // best-effort stderr
	return true
}

// importNudgeQueueState imports one loaded file-queue snapshot's three
// buckets verbatim, returning the imported ids.
func importNudgeQueueState(class *nudgesdb.Store, state nudgequeue.State) ([]string, error) {
	var imported []string
	for _, bucket := range []struct {
		items      []nudgequeue.Item
		queueState string
	}{
		{state.Pending, "pending"},
		{state.InFlight, "in_flight"},
		{state.Dead, "dead"},
	} {
		for _, item := range bucket.items {
			if item.ID == "" {
				continue
			}
			if err := class.ImportItem(item, bucket.queueState); err != nil {
				return imported, err
			}
			imported = append(imported, item.ID)
		}
	}
	return imported, nil
}

// importLiveNudgeQueue merge-imports the file queue's current buckets
// (INSERT OR IGNORE — existing class rows, live or terminal, are never
// touched). It is the straggler / later-boot import: any item another
// process appended to state.json after the marker flip converges into the
// class store here, before the residue sweep clears the file copy.
func importLiveNudgeQueue(class *nudgesdb.Store, cityPath string) ([]string, error) {
	state, err := nudgequeue.LoadState(cityPath)
	if err != nil {
		return nil, fmt.Errorf("reading legacy nudge queue: %w", err)
	}
	return importNudgeQueueState(class, state)
}

// migrateNudgeQueueIntoClassStore imports the live file-queue buckets plus
// the terminal shadow history whose created OR terminal clock falls within
// the queue TTL (24h — older records are past their deliver-by deadline and
// carry no wait-finalization value). It first RESETS the class store's live
// rows: an interrupted earlier attempt left committed rows behind while the
// still-file-backed city delivered, acked, released, or dead-lettered past
// them — re-syncing to the file's current truth keeps the retry from
// resurrecting an already-delivered item (its terminal record re-enters via
// the shadow import) or keeping a stale bucket. This runs strictly before
// the marker, so the file queue is still the authority being copied. Every
// imported id is read back from the class store (copy-verify) before the
// caller may flip the marker.
func migrateNudgeQueueIntoClassStore(class *nudgesdb.Store, cityPath string, front *nudgequeue.Store, now time.Time) (nudgeClassMigrationResult, error) {
	result := nudgeClassMigrationResult{}
	state, err := nudgequeue.LoadState(cityPath)
	if err != nil {
		return result, fmt.Errorf("reading legacy nudge queue: %w", err)
	}
	if _, err := class.ResetLive(); err != nil {
		return result, err
	}
	importedIDs, err := importNudgeQueueState(class, state)
	if err != nil {
		return result, err
	}
	result.liveImported = len(importedIDs)

	shadows, err := front.ShadowHistorySince(now.Add(-nudgequeue.DefaultTTL))
	if err != nil {
		return result, fmt.Errorf("listing legacy nudge shadows: %w", err)
	}
	for _, shadow := range shadows {
		if shadow.ID == "" || !nudgequeue.IsTerminalState(shadow.State) {
			result.skipped++
			continue
		}
		if err := class.ImportTerminalShadow(shadow, shadow.CreatedAt, shadow.TerminalAt); err != nil {
			return result, err
		}
		result.terminalImported++
		importedIDs = append(importedIDs, shadow.ID)
	}
	for _, id := range importedIDs {
		if _, ok, err := class.FindRecordIncludingTerminal(id); err != nil || !ok {
			return result, fmt.Errorf("verifying imported nudge %q: found=%v err=%w", id, ok, err)
		}
	}
	return result, nil
}

// writeNudgesMigratedMarkerFile atomically writes the migrated marker that
// commits the city to class-store routing.
func writeNudgesMigratedMarkerFile(cityPath string) error {
	dir := nudgesdb.StoreDir(cityPath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("writing nudges migrated marker: %w", err)
	}
	tmp, err := os.CreateTemp(dir, "nudges.migrated.tmp*")
	if err != nil {
		return fmt.Errorf("writing nudges migrated marker: %w", err)
	}
	name := tmp.Name()
	if _, err := fmt.Fprintf(tmp, "nudges class migrated %s\n", time.Now().UTC().Format(time.RFC3339)); err != nil {
		_ = tmp.Close()
		_ = os.Remove(name)
		return fmt.Errorf("writing nudges migrated marker: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(name)
		return fmt.Errorf("writing nudges migrated marker: %w", err)
	}
	if err := os.Rename(name, nudgesdb.MigratedMarkerPath(cityPath)); err != nil {
		_ = os.Remove(name)
		return fmt.Errorf("writing nudges migrated marker: %w", err)
	}
	return nil
}

// sweepLegacyNudgeResidue converges the legacy queue's residue on a
// MIGRATED city with the documented import-then-sweep: it first
// merge-imports any file-queue item the class store does not yet own
// (an enqueue that raced the marker flip, or a mixed-version old binary's
// append — without this, such items would be stranded in state.json
// forever, since routed readers never look there), then clears file items
// the class store owns (so a not-yet-upgraded poller stops redelivering
// them) and bd shadow beads (closed ones, any whose id the class store
// owns, and open ones past the grace window). Deleting converges across
// boots, so a kill mid-sweep costs nothing. Returns the number of file
// items plus beads removed.
func sweepLegacyNudgeResidue(cityPath string, cfg *config.City, stderr io.Writer) int {
	routed, err := nudgesdb.Routed(cityPath, cfg)
	if err != nil {
		fmt.Fprintf(stderr, "gc: nudges legacy residue sweep: %v\n", err) //nolint:errcheck // best-effort stderr
		return 0
	}
	if !routed {
		return 0
	}
	class, err := nudgesdb.SharedStoreFor(cityPath)
	if err != nil {
		fmt.Fprintf(stderr, "gc: nudges legacy residue sweep: %v\n", err) //nolint:errcheck // best-effort stderr
		return 0
	}
	if stragglers, err := importLiveNudgeQueue(class, cityPath); err != nil {
		// Import failure must skip the file sweep below: sweeping without the
		// import could only remove already-imported items, but converging is
		// cheaper than reasoning about partial imports — retry next boot.
		fmt.Fprintf(stderr, "gc: nudges legacy residue sweep: importing stragglers: %v\n", err) //nolint:errcheck // best-effort stderr
		return 0
	} else if len(stragglers) > 0 {
		fmt.Fprintf(stderr, "gc: nudges legacy residue sweep: merged %d file items into the class store\n", len(stragglers)) //nolint:errcheck // best-effort stderr
	}
	inClass := func(id string) (bool, error) {
		_, ok, err := class.FindRecordIncludingTerminal(id)
		return ok, err
	}
	removed, err := nudgequeue.SweepFileResidue(cityPath, inClass)
	if err != nil {
		fmt.Fprintf(stderr, "gc: nudges legacy residue sweep: %v\n", err) //nolint:errcheck // best-effort stderr
	}

	store, err := openNudgeClassMigrationStore(cityPath)
	if err != nil {
		fmt.Fprintf(stderr, "gc: nudges legacy residue sweep: %v\n", err) //nolint:errcheck // best-effort stderr
		return removed
	}
	defer closeBeadStoreHandle(store) //nolint:errcheck // best-effort close
	front := nudgequeue.NewStore(beads.NudgesStore{Store: resolveNudgesStore(store, cfg, cityPath, nil)})
	shadows, err := front.ShadowHistorySince(time.Time{})
	if err != nil {
		fmt.Fprintf(stderr, "gc: nudges legacy residue sweep: %v\n", err) //nolint:errcheck // best-effort stderr
		return removed
	}
	now := time.Now()
	var ids []string
	for _, shadow := range shadows {
		if shadow.BeadID == "" {
			continue
		}
		keepable := shadow.Open && now.Sub(shadow.CreatedAt) < legacyNudgeResidueOpenGrace
		if keepable {
			owned, err := inClass(shadow.ID)
			if err != nil || !owned {
				continue
			}
		}
		ids = append(ids, shadow.BeadID)
	}
	deleted, err := deleteLegacyOrderTrackingBeads(store, ids)
	removed += deleted
	if err != nil {
		fmt.Fprintf(stderr, "gc: nudges legacy residue sweep (deleted %d): %v\n", deleted, err) //nolint:errcheck // best-effort stderr
	} else if removed > 0 {
		fmt.Fprintf(stderr, "gc: nudges legacy residue sweep: cleared %d migrated file items / shadow beads\n", removed) //nolint:errcheck // best-effort stderr
	}
	return removed
}

// startNudgesRetentionSweeper starts the routed class store's terminal-row
// retention loop on the controller (idempotent per process-shared handle).
// Unlike orders, nudges had no pre-existing retention path to route, so the
// store's own sweeper is the path that runs with only the controller alive
// (SDK self-sufficiency — the nudge-mail sweep watchdog rides the order
// dispatch tick). The routed nudge-mail sweep leg converges on the same
// SweepRetention policy; overlapping triggers are idempotent.
func startNudgesRetentionSweeper(cityPath string, stderr io.Writer) {
	class, err := nudgesdb.SharedStoreFor(cityPath)
	if err != nil {
		fmt.Fprintf(stderr, "gc start: nudges retention sweeper: %v\n", err) //nolint:errcheck // best-effort stderr
		return
	}
	class.StartRetentionSweeper(nudgeRetentionSweepInterval, nudgeTerminalRetentionTTL, stderr)
}
