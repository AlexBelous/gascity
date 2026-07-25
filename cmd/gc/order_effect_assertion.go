package main

import (
	"os"
	"path/filepath"
	"strings"
	"time"
)

// orderEffectProbe fingerprints the durable observable an exec order is
// expected to advance on a successful run. snapshot returns ok=false when
// nothing is currently eligible for the probe to watch (e.g. a city with
// zero registered databases, or a sync probe where every database has
// opted out via .no-sync) — dispatchExec must skip the assertion entirely
// in that case rather than compare two "nothing eligible" snapshots, so a
// legitimately idle rig (including gascity's own local-only deployment)
// never false-positives.
type orderEffectProbe struct {
	label    string
	snapshot func(cityPath string) (fingerprint string, ok bool)
}

// orderEffectProbes is keyed by orders.Order.Name (the plain, unscoped
// name). Detection only: an order having a registered probe never changes
// what dispatchExec runs, only whether it additionally records
// events.OrderEffectAssertionFailed on the success path when the probe's
// before/after snapshots are identical.
var orderEffectProbes = map[string]orderEffectProbe{
	"mol-dog-backup":      {label: "dolt-backup-artifact-tree", snapshot: backupEffectSnapshot},
	"dolt-remotes-patrol": {label: "dolt-sync-eligible-db-tree", snapshot: doltSyncEffectSnapshot},
}

// doltSyncSystemDBNames mirrors the case-insensitive skip list in
// examples/bd/dolt/commands/sync/run.sh's per-database sync loop, so a
// Dolt-internal system database can never itself register as "eligible"
// for either probe.
var doltSyncSystemDBNames = map[string]bool{
	"information_schema": true,
	"mysql":              true,
	"dolt_cluster":       true,
	"performance_schema": true,
	"sys":                true,
	"__gc_probe":         true,
}

// doltUserDatabaseDirs returns the on-disk directories under a city's
// managed Dolt data root that look like real user databases: an entry
// containing a .dolt subdirectory, excluding reserved system database
// names. ok=false means the data root itself could not be resolved or
// read (e.g. no Dolt backend configured for this city at all), which
// callers must treat the same as "nothing eligible."
func doltUserDatabaseDirs(cityPath string) ([]string, bool) {
	layout, err := resolveManagedDoltRuntimeLayout(cityPath)
	if err != nil {
		return nil, false
	}
	entries, err := os.ReadDir(layout.DataDir)
	if err != nil {
		return nil, false
	}
	var dirs []string
	for _, entry := range entries {
		if !entry.IsDir() || doltSyncSystemDBNames[strings.ToLower(entry.Name())] {
			continue
		}
		dbDir := filepath.Join(layout.DataDir, entry.Name())
		if info, err := os.Stat(filepath.Join(dbDir, ".dolt")); err != nil || !info.IsDir() {
			continue
		}
		dirs = append(dirs, dbDir)
	}
	return dirs, true
}

// backupEffectSnapshot fingerprints the observable mol-dog-backup is
// expected to advance: the newest mtime under <city>/.dolt-backup, the
// same artifact tree internal/doctor's DoltBackupCheck treats as evidence
// a backup has run. Unlike sync, backup has no per-database opt-out
// marker (mol-dog-backup.sh never checks .no-sync), so the only
// eligibility gate is the zero-user-databases case — mirroring
// mol-dog-backup.sh's own "no databases found, skipping" exit-0 no-op, so
// a freshly-initialized city that hasn't onboarded a database yet never
// false-positives.
func backupEffectSnapshot(cityPath string) (string, bool) {
	dbDirs, ok := doltUserDatabaseDirs(cityPath)
	if !ok || len(dbDirs) == 0 {
		return "", false
	}
	mt, hasContent := newestMTime(filepath.Join(cityPath, ".dolt-backup"))
	if !hasContent {
		return "absent", true
	}
	return mt.UTC().Format(time.RFC3339Nano), true
}

// doltSyncEffectSnapshot fingerprints the observable dolt-remotes-patrol
// is expected to advance: the newest mtime across every eligible (i.e.
// not .no-sync'd) user database's .dolt dir under the city's managed
// Dolt data root. Returns ok=false when zero databases are eligible —
// including a rig where every database is intentionally .no-sync'd,
// which must never be treated as a stuck sync order (gascity's own
// deployment is exactly this case, by explicit 2026-05-22 operator
// directive).
func doltSyncEffectSnapshot(cityPath string) (string, bool) {
	dbDirs, ok := doltUserDatabaseDirs(cityPath)
	if !ok {
		return "", false
	}
	var newest time.Time
	found := false
	for _, dbDir := range dbDirs {
		if _, err := os.Stat(filepath.Join(dbDir, ".no-sync")); err == nil {
			continue
		}
		mt, hasContent := newestMTime(dbDir)
		if !hasContent {
			continue
		}
		found = true
		if mt.After(newest) {
			newest = mt
		}
	}
	if !found {
		return "", false
	}
	return newest.UTC().Format(time.RFC3339Nano), true
}

// newestMTime walks dir and returns the most recent modification time
// among it and everything beneath it. ok=false when dir does not exist or
// the walk finds nothing (a nonexistent root is reported to walkFn as a
// single stat error rather than being an error from Walk itself, so a
// missing directory and an existing-but-untouched one are both handled
// here without a separate os.Stat pre-check).
func newestMTime(dir string) (newest time.Time, ok bool) {
	_ = filepath.Walk(dir, func(_ string, info os.FileInfo, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if info.ModTime().After(newest) {
			newest = info.ModTime()
		}
		ok = true
		return nil
	})
	return newest, ok
}
