package main

// Seamless bd→sqlite migration for the SESSIONS class (design "Migration &
// cutover" row 4; the P1–P3 bulletproof-upgrade pattern, verbatim where it
// applies): on the first controller boot with [beads.classes.sessions]
// backend="sqlite", import the bd store's session truth — a FULL import of
// open session beads and waits with ids preserved (open rows are the
// projection the reconciler re-derives everything from; losing one is the
// root-loss shape), plus recently-closed rows within the retention TTL so
// `gc session list --state closed` and closed-wait retries keep working —
// then copy-verify, flip the atomic marker, and straggler-import anything
// that raced the flip. The migration RESETS the class store first so an
// interrupted attempt + retry re-syncs to the bd truth instead of
// resurrecting rows the still-bd city already mutated (the P2/P3
// no-resurrection lesson), and aborts BEFORE the marker on ANY failure.
// The residue sweep clears the bd-side copies in the background with the
// documented import-then-sweep, converging across boots.

import (
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/gastownhall/gascity/internal/beads"
	sessionsdb "github.com/gastownhall/gascity/internal/classdb/sessions"
	"github.com/gastownhall/gascity/internal/config"
)

const (
	// sessionsClosedRetentionTTL is the closed-row retention window: the
	// migration imports closed sessions/waits younger than this (so recent
	// closed-state reads and closed-wait retries survive the cutover), and
	// the class store's sweeper purges closed rows older than it — the
	// design's net-new 7d closed-session purge.
	sessionsClosedRetentionTTL = 7 * 24 * time.Hour
	// sessionsRetentionSweepInterval is the cadence of the routed class
	// store's retention loop.
	sessionsRetentionSweepInterval = 15 * time.Minute
	// legacySessionsResidueOpenGrace protects a not-yet-upgraded process's
	// fresh bd session write from being deleted before a boot's
	// import-then-sweep has converged it (mixed-version window).
	legacySessionsResidueOpenGrace = 10 * time.Minute
)

// openSessionsClassMigrationStore is the bd-store open seam for the
// migration and residue sweep (overridden by tests, mirroring
// openMessagingClassMigrationStore).
var openSessionsClassMigrationStore func(cityPath string) (beads.Store, error) = func(cityPath string) (beads.Store, error) {
	return openStoreAtForCity(cityPath, cityPath)
}

// ensureSessionsClassMigrated performs the seamless sessions-class cutover
// on controller boot. Returns true when the city is (now) migrated; false
// leaves the city on bd with the reason on stderr — the marker is never
// written unless the import and copy-verify completed.
func ensureSessionsClassMigrated(cityPath string, cfg *config.City, stderr io.Writer) bool {
	if cfg == nil || cfg.Beads.ClassBackend(config.BeadClassSessions) != config.BeadsClassBackendSQLite {
		return false
	}
	if _, err := os.Stat(sessionsdb.MigratedMarkerPath(cityPath)); err == nil {
		return true
	}
	class, err := sessionsdb.SharedStoreFor(cityPath)
	if err != nil {
		fmt.Fprintf(stderr, "gc start: sessions class migration: %v\n", err) //nolint:errcheck // best-effort stderr
		return false
	}
	store, err := openSessionsClassMigrationStore(cityPath)
	if err != nil {
		fmt.Fprintf(stderr, "gc start: sessions class migration: %v\n", err) //nolint:errcheck // best-effort stderr
		return false
	}
	defer closeBeadStoreHandle(store) //nolint:errcheck // best-effort close

	result, err := migrateSessionsIntoClassStore(class, store, time.Now())
	if err != nil {
		fmt.Fprintf(stderr, "gc start: sessions class migration: %v\n", err) //nolint:errcheck // best-effort stderr
		return false
	}
	if err := writeSessionsMigratedMarkerFile(cityPath); err != nil {
		fmt.Fprintf(stderr, "gc start: sessions class migration: %v\n", err) //nolint:errcheck // best-effort stderr
		return false
	}
	// Straggler pass: a session created or a wait registered between the
	// import and the marker flip merge-imports here (INSERT OR IGNORE keeps
	// it idempotent). Best-effort — anything still missed is imported by a
	// later boot's residue sweep before that sweep clears it.
	if stragglers, err := importSessionsSnapshot(class, store, time.Now(), false); err == nil {
		result.imported += stragglers.imported
	}
	fmt.Fprintf(stderr, "gc start: sessions class migrated to %s (%d rows imported; %d aged-out closed rows dropped)\n", //nolint:errcheck // best-effort stderr
		sessionsdb.StorePath(cityPath), result.imported, result.dropped)
	return true
}

type sessionsClassMigrationResult struct {
	imported int
	dropped  int
}

// migrateSessionsIntoClassStore imports the bd store's current sessions
// truth. It first RESETS the class store: an interrupted earlier attempt
// left committed rows behind while the still-bd city closed sessions,
// finalized waits, or re-registered them — re-syncing to the bd store's
// current truth keeps the retry from resurrecting a closed session or a
// finalized wait as open. This runs strictly before the marker, so the bd
// store is still the authority being copied. Every imported id is read
// back from the class store (copy-verify) before the caller may flip the
// marker. (A shadow-soak city's teed rows are reset along with everything
// else — the bd truth is re-imported wholesale.)
func migrateSessionsIntoClassStore(class *sessionsdb.Store, store beads.Store, now time.Time) (sessionsClassMigrationResult, error) {
	if err := class.DeleteAllRows(); err != nil {
		return sessionsClassMigrationResult{}, err
	}
	return importSessionsSnapshot(class, store, now, true)
}

// importSessionsSnapshot imports one read of the bd store's sessions-class
// beads (open rows always; closed rows within the retention TTL — ids,
// clocks, labels, and the full metadata map preserved verbatim). INSERT OR
// IGNORE keeps re-imports idempotent. verify re-reads every imported id
// from the class store — the copy-verify gate the pre-marker migration
// requires.
func importSessionsSnapshot(class *sessionsdb.Store, store beads.Store, now time.Time, verify bool) (sessionsClassMigrationResult, error) {
	result := sessionsClassMigrationResult{}
	rows, err := sessionsdb.ExportSessionClassBeads(store, true)
	if err != nil {
		return result, fmt.Errorf("reading legacy session beads: %w", err)
	}
	cutoff := now.Add(-sessionsClosedRetentionTTL)
	var ids []string
	for _, b := range rows {
		if sessionsImportDropsBead(b, cutoff) {
			result.dropped++
			continue
		}
		if _, err := class.ImportBead(b); err != nil {
			return result, fmt.Errorf("importing session-class bead %q: %w", b.ID, err)
		}
		result.imported++
		ids = append(ids, b.ID)
	}
	if verify {
		for _, id := range ids {
			if _, err := class.Get(id); err != nil {
				return result, fmt.Errorf("verifying imported session-class bead %q: %w", id, err)
			}
		}
	}
	return result, nil
}

// sessionsImportDropsBead is the age drop at import: closed rows whose
// last write precedes the retention cutoff stay behind on the bd side (the
// class store's sweeper would purge them immediately anyway). Open rows
// always import — they are the reconciler's restart projection.
func sessionsImportDropsBead(b beads.Bead, cutoff time.Time) bool {
	if b.Status != "closed" {
		return false
	}
	ref := b.UpdatedAt
	if ref.IsZero() {
		ref = b.CreatedAt
	}
	return ref.Before(cutoff)
}

func writeSessionsMigratedMarkerFile(cityPath string) error {
	dir := sessionsdb.StoreDir(cityPath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("writing sessions migrated marker: %w", err)
	}
	tmp, err := os.CreateTemp(dir, "sessions.migrated.tmp*")
	if err != nil {
		return fmt.Errorf("writing sessions migrated marker: %w", err)
	}
	name := tmp.Name()
	if _, err := fmt.Fprintf(tmp, "sessions class migrated %s\n", time.Now().UTC().Format(time.RFC3339)); err != nil {
		_ = tmp.Close()
		_ = os.Remove(name)
		return fmt.Errorf("writing sessions migrated marker: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(name)
		return fmt.Errorf("writing sessions migrated marker: %w", err)
	}
	if err := os.Rename(name, sessionsdb.MigratedMarkerPath(cityPath)); err != nil {
		_ = os.Remove(name)
		return fmt.Errorf("writing sessions migrated marker: %w", err)
	}
	return nil
}

// sweepLegacySessionResidue converges the bd store's sessions-class residue
// on a MIGRATED city with the documented import-then-sweep: it first
// merge-imports any bd session or wait bead the class store does not yet
// own (a write that raced the marker flip, or a mixed-version old
// binary's — without this, such rows would be stranded in bd forever,
// since routed readers never look there), then deletes bd copies the class
// store owns, closed bd rows, and open bd rows past the grace window.
// Deleting converges across boots, so a kill mid-sweep costs nothing.
func sweepLegacySessionResidue(cityPath string, cfg *config.City, stderr io.Writer) {
	routed, err := sessionsdb.Routed(cityPath, cfg)
	if err != nil {
		fmt.Fprintf(stderr, "gc: sessions legacy residue sweep: %v\n", err) //nolint:errcheck // best-effort stderr
		return
	}
	if !routed {
		return
	}
	class, err := sessionsdb.SharedStoreFor(cityPath)
	if err != nil {
		fmt.Fprintf(stderr, "gc: sessions legacy residue sweep: %v\n", err) //nolint:errcheck // best-effort stderr
		return
	}
	store, err := openSessionsClassMigrationStore(cityPath)
	if err != nil {
		fmt.Fprintf(stderr, "gc: sessions legacy residue sweep: %v\n", err) //nolint:errcheck // best-effort stderr
		return
	}
	defer closeBeadStoreHandle(store) //nolint:errcheck // best-effort close

	if stragglers, err := importSessionsSnapshot(class, store, time.Now(), false); err != nil {
		// Import failure must skip the sweep below: sweeping without the
		// import could strand an unimported row — retry next boot.
		fmt.Fprintf(stderr, "gc: sessions legacy residue sweep: importing stragglers: %v\n", err) //nolint:errcheck // best-effort stderr
		return
	} else if stragglers.imported > 0 {
		fmt.Fprintf(stderr, "gc: sessions legacy residue sweep: merged %d rows into the class store\n", stragglers.imported) //nolint:errcheck // best-effort stderr
	}

	residue, err := sessionsdb.ExportSessionClassBeads(store, true)
	if err != nil {
		fmt.Fprintf(stderr, "gc: sessions legacy residue sweep: %v\n", err) //nolint:errcheck // best-effort stderr
		return
	}
	now := time.Now()
	var ids []string
	for _, b := range residue {
		if keep, err := spareSessionResidueBead(class, b, now); err != nil || keep {
			continue
		}
		ids = append(ids, b.ID)
	}
	deleted, err := deleteLegacyOrderTrackingBeads(store, ids)
	if err != nil {
		fmt.Fprintf(stderr, "gc: sessions legacy residue sweep (deleted %d): %v\n", deleted, err) //nolint:errcheck // best-effort stderr
	} else if deleted > 0 {
		fmt.Fprintf(stderr, "gc: sessions legacy residue sweep: cleared %d migrated bd beads\n", deleted) //nolint:errcheck // best-effort stderr
	}
}

// spareSessionResidueBead reports whether a bd residue bead must survive
// this sweep: only an OPEN bead inside the mixed-version grace window that
// the class store does not own yet — the next boot's import-then-sweep
// converges it. Everything else (closed, class-owned, or aged open) is
// deletable residue.
func spareSessionResidueBead(class *sessionsdb.Store, b beads.Bead, now time.Time) (bool, error) {
	if b.Status == "closed" || now.Sub(b.CreatedAt) >= legacySessionsResidueOpenGrace {
		return false, nil
	}
	if _, err := class.Get(b.ID); err == nil {
		return false, nil // class-owned: the routed store is authoritative
	} else if !errors.Is(err, beads.ErrNotFound) {
		// Ownership unprovable: spare the row and surface the error — a
		// failing class read must never license deleting a fresh open bead.
		return true, err
	}
	return true, nil
}

// startSessionsRetentionSweeper starts the routed class store's retention
// loop on the controller (idempotent per process-shared handle): the
// design's net-new closed-session/wait purge TTL — the path that replaces
// reaper.sh's raw Dolt session SQL and runs with only the controller alive
// (SDK self-sufficiency).
func startSessionsRetentionSweeper(cityPath string, stderr io.Writer) {
	class, err := sessionsdb.SharedStoreFor(cityPath)
	if err != nil {
		fmt.Fprintf(stderr, "gc start: sessions retention sweeper: %v\n", err) //nolint:errcheck // best-effort stderr
		return
	}
	class.StartRetentionSweeper(sessionsRetentionSweepInterval, sessionsClosedRetentionTTL, stderr)
}
