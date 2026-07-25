package main

// Window-3 / deploy-lineage integration (engdocs/design/infra-class-sqlite-stores.md
// "Window-3 / deploy-lineage integration"): the maintainer-city binary we are
// replacing ran the infra-store-split lineage, where ALL FIVE infra classes —
// graph, sessions, orders, messaging, nudges — live comingled in ONE embedded
// sqlite scope at .gc/infra/.beads/beads.sqlite (issue_prefix gcg). Our binary
// splits infra into five per-class stores under .gc/store. The five boot
// migrations already source the Dolt WORK stores; on a deploy-lineage city that
// source holds ~zero infra beads (they all sit in .gc/infra), so without this
// seam every gcg session/order/message/nudge/graph bead would be orphaned.
//
// This file adds the combined infra scope as an ADDITIONAL, strictly READ-ONLY
// import source. Each class migration filters the scope down to its own slice
// (via the same class extractor it runs over the work store) and imports the
// matching beads. The scope is NEVER mutated: rollback to the window-3 binary
// must find .gc/infra bit-intact (reversibility), and the routed readers read
// the freshly-populated class stores instead. The read-only guarantee is the
// SQLite file:...?mode=ro connection (WithSQLiteStoreReadOnly) — it cannot
// checkpoint the WAL on close, so the source's main db AND -wal stay
// byte-identical even when the migration is the sole connection over a stopped
// controller's crash-state WAL. (A hardened production rehearsal should still go
// further: `VACUUM INTO` a snapshot of the live .gc/infra and migrate the COPY,
// never the live file, so a bug in a NON-read-only code path can never reach the
// fleet brain's real store.)
//
// Prefix reclassification decision (the hardest call in this slice): the
// combined scope's beads are ALL gcg-prefixed, yet Classify routes a
// session/order/message/nudge bead to a NON-graph class store whose reserved
// prefix is gcs/gco/gcm/gcn. We KEEP the gcg id verbatim rather than rewrite it
// (option i, not ii). See infra_scope_migrate.go's package comment block below
// and the return-report rationale. The keep-id choice is safe because every
// subsystem reads its OWN class store directly (resolveSessionStore →
// sessions.db, the mail provider → messaging.db, …), a prefix-agnostic point
// read/list — so a gcg session bead sitting in sessions.db is found by the
// session subsystem regardless of prefix, and the cross-class references that
// point at it (gc.session_id, mail.*_session_id, extmsg bindings, waits) resolve
// through those same owning-store reads, never through by-prefix routing. This
// mirrors the window-3 migration's own North Star ("infra-class beads keep their
// ids — never re-minted; cross-boundary edges resolved by Go-side seams").
// Rewriting (option ii) would instead have to atomically rewrite that entire
// cross-store reference graph — including references living in the Dolt WORK
// store — which both violates the reversibility constraint (mutating the work
// store) and risks dangling gcg refs; it is strictly worse here.
//
// Two consequences the keep-id choice creates are handled next to it:
//   - By-id READ federation (cmd_bd_show_fed.go) treats gcg as graph's reserved
//     namespace, so `gc bd show <gcg-non-graph-id>` would miss. The federation is
//     widened to fall through to the other routed class stores on a graph miss.
//   - The graph store keeps minting gcg-<n> after the cutover; a fresh mint could
//     otherwise collide with an imported non-graph gcg-<n> from the shared
//     combined sequence. The graph migration lifts the graph store's id floor
//     above the GLOBAL max gcg suffix of the whole combined scope (durably, via a
//     small floor file the opener re-applies) so no post-cutover graph id ever
//     reuses an imported one.

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/gastownhall/gascity/internal/beads"
	nudgesdb "github.com/gastownhall/gascity/internal/classdb/nudges"
	"github.com/gastownhall/gascity/internal/config"
	"github.com/gastownhall/gascity/internal/coordclass"
)

// infraCombinedScopeDir is the window-3 combined infra scope's store directory
// (.gc/infra/.beads); OpenSQLiteStore appends beads.sqlite.
func infraCombinedScopeDir(cityPath string) string {
	return filepath.Join(cityPath, ".gc", "infra", ".beads")
}

// legacyCombinedScopeDir is the superseded pre-window-3 scope's store directory
// (.gc), holding a bare .gc/beads.sqlite.
func legacyCombinedScopeDir(cityPath string) string {
	return filepath.Join(cityPath, ".gc")
}

// infraScopeMigrationSource returns the store DIRECTORY of the window-3 combined
// infra scope to source class migrations from, and whether one is present.
// Preference order: the .gc/infra/.beads combined scope, else the legacy
// .gc/beads.sqlite location, else ("", false). Detection is the presence of the
// actual beads.sqlite data file (not a config marker), so a Dolt-backed infra
// scope — which has no beads.sqlite — reads as absent and the migration stays
// DARK. Purely a stat; opens nothing.
func infraScopeMigrationSource(cityPath string) (dir string, ok bool) {
	if cityPath == "" {
		return "", false
	}
	for _, cand := range []string{infraCombinedScopeDir(cityPath), legacyCombinedScopeDir(cityPath)} {
		if _, err := os.Stat(filepath.Join(cand, "beads.sqlite")); err == nil {
			return cand, true
		}
	}
	return "", false
}

// openInfraCombinedScopeSource opens the window-3 combined infra scope strictly
// READ-ONLY for use as an extra migration import source. The tri-state return is
// deliberate: (nil, noop, false, nil) when no scope is present (DARK — the
// migration proceeds work-store-only, byte-identical to a fresh city); a non-nil
// err when a scope IS present but cannot be opened, which each caller treats as
// fatal — flipping the routing marker while an infra source is unreadable would
// orphan its beads. Overridden by tests to inject a fake combined scope.
var openInfraCombinedScopeSource = func(cityPath string) (store beads.Store, closeFn func(), ok bool, err error) {
	dir, present := infraScopeMigrationSource(cityPath)
	if !present {
		return nil, func() {}, false, nil
	}
	// The gcg prefix only governs id MINTING, which a read-only source never
	// does; ids are read back verbatim. It is set for correctness of the scan
	// helpers only.
	prefix, _ := config.ReservedClassPrefix(config.BeadClassGraph)
	st, oerr := beads.OpenSQLiteStore(dir, beads.WithSQLiteStoreReadOnly(), beads.WithSQLiteStoreIDPrefix(prefix))
	if oerr != nil {
		return nil, func() {}, true, fmt.Errorf("opening infra combined scope %s: %w", dir, oerr)
	}
	return st, func() { closeBeadStoreHandle(st) }, true, nil //nolint:errcheck // best-effort close
}

// ensureInfraScopeClassifierClean is the boot preflight (G1) that makes the
// window-3 .gc/infra migration self-guarding. None of the five class migrations
// import a ClassWork bead — each imports only its OWN infra class — so if the
// combined scope holds any bead the CURRENT coordclass.Classify calls ClassWork
// (classifier or metadata drift versus the binary that populated .gc/infra), S7
// would import it NOWHERE. Because window-3's model is DISJOINT (a work bead is
// deleted from Dolt once its infra siblings move into .gc/infra), such a bead
// becomes a silent ORPHAN after the routing flip: present only in the read-only
// .gc/infra scope, invisible to the running fleet.
//
// coordclass.Classify is byte-identical between window-3's branch and ours, so
// this PROBABLY never fires — but nothing else checks it, and an orphan is only
// discoverable after the markers are already written and the fleet is blind to
// the bead. So this runs BEFORE the first class migration and is boot-blocking:
// when the combined scope is present it lists every bead and, for each one the
// current classifier calls ClassWork, confirms the SAME id is a safe duplicate
// already living in the Dolt work store (the routed fleet still sees it there).
// An id absent from the work store is an orphan; ANY orphan fails the boot
// before a single class marker is written, so the city stays whole and
// reversible on the window-3 binary. Zero ClassWork beads is the clean fast
// path — the work store is never consulted per bead. DARK on a city with no
// .gc/infra (source absent → no-op; workStore untouched).
//
// The .gc/infra scope is RETAINED read-only forever (window-3 reversibility), so
// the presence gate never short-circuits post-migration. To avoid re-Listing the
// whole scope (and re-opening the work store) on every boot for the life of the
// city, a clean census stamps a durable marker keyed on the scope's mtime+size;
// a re-boot over the byte-identical scope matches the marker and skips outright.
// The marker is an optimization only — a missing or mismatched marker always
// re-runs the full census.
func ensureInfraScopeClassifierClean(cityPath string, workStore beads.Store, stderr io.Writer) error {
	if infraScopePreflightClean(cityPath) {
		return nil
	}
	scope, closeScope, present, err := openInfraCombinedScopeSource(cityPath)
	defer closeScope()
	if err != nil {
		// Present but unopenable: the same condition each class migration treats
		// as fatal. Blocking the boot here (before any marker) is strictly safer
		// than letting a migration that cannot read .gc/infra flip its routing.
		return err
	}
	if !present {
		return nil
	}
	rows, err := scope.List(beads.ListQuery{IncludeClosed: true, TierMode: beads.TierBoth, AllowScan: true})
	if err != nil {
		return fmt.Errorf("infra-scope classifier preflight: scanning combined scope: %w", err)
	}
	var orphans []string
	safeDuplicates := 0
	for _, b := range rows {
		if coordclass.Classify(b) != coordclass.ClassWork {
			continue
		}
		// A ClassWork bead in .gc/infra is imported by no class migration. Unless
		// the work store already owns the same id (a safe duplicate the routed
		// fleet still reads), flipping routing would strand it. A hard work-store
		// read failure is surfaced — never flattened into "orphan" or "safe".
		if _, gerr := workStore.Get(b.ID); gerr == nil {
			safeDuplicates++
			continue
		} else if !errors.Is(gerr, beads.ErrNotFound) {
			return fmt.Errorf("infra-scope classifier preflight: checking work store for %q: %w", b.ID, gerr)
		}
		orphans = append(orphans, b.ID)
	}
	if len(orphans) > 0 {
		dir, _ := infraScopeMigrationSource(cityPath)
		return fmt.Errorf("infra-scope classifier preflight: %d work-class bead(s) in the combined infra scope %s would be orphaned by the class migration — the current classifier calls them ClassWork (no class store imports ClassWork) and they are absent from the Dolt work store: %s; refusing to write any class marker (reconcile the classifier drift or import these ids into the work store first)",
			len(orphans), dir, strings.Join(cappedInfraOrphanIDs(orphans), ", "))
	}
	if safeDuplicates > 0 {
		fmt.Fprintf(stderr, "gc start: infra-scope classifier preflight: %d work-class bead(s) in the combined scope verified as safe duplicates of the work store\n", safeDuplicates) //nolint:errcheck // best-effort stderr
	}
	// Census passed clean: stamp the marker so the next boot over this unchanged
	// scope skips the rescan and the work-store open. Best-effort — a stamp failure
	// only costs a rescan next boot, never correctness.
	if err := stampInfraScopePreflightClean(cityPath); err != nil {
		fmt.Fprintf(stderr, "gc start: infra-scope classifier preflight: could not stamp clean marker (will rescan next boot): %v\n", err) //nolint:errcheck // best-effort stderr
	}
	return nil
}

// infraScopePreflightMarkerPath is the durable "already censused clean" marker
// for the G1 preflight, in the shared class-store dir alongside the per-class
// migrated markers.
func infraScopePreflightMarkerPath(cityPath string) string {
	return filepath.Join(nudgesdb.StoreDir(cityPath), "infra-scope-preflight.clean")
}

// infraScopeStatSignature returns a compact mtime+size signature of the combined
// scope's beads.sqlite, mirroring routedConfigCache's stat-signature key. Because
// the .gc/infra scope is retained READ-ONLY after migration, an unchanged
// signature proves the exact bytes already passed the census.
func infraScopeStatSignature(scopeDir string) (string, error) {
	info, err := os.Stat(filepath.Join(scopeDir, "beads.sqlite"))
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%d-%d", info.ModTime().UnixNano(), info.Size()), nil
}

// infraScopePreflightClean reports whether the combined infra scope's current
// stat signature matches a previously-stamped preflight-clean marker — i.e. this
// exact scope already passed the census on an earlier boot, so the rescan and the
// work-store open can both be skipped. Conservative: a missing scope, stat
// failure, unreadable marker, or signature mismatch all return false, so the full
// census runs unless the marker POSITIVELY matches.
func infraScopePreflightClean(cityPath string) bool {
	dir, present := infraScopeMigrationSource(cityPath)
	if !present {
		return false
	}
	sig, err := infraScopeStatSignature(dir)
	if err != nil {
		return false
	}
	stamped, err := os.ReadFile(infraScopePreflightMarkerPath(cityPath))
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(stamped)) == sig
}

// stampInfraScopePreflightClean records the current scope signature atomically so
// a re-boot over the unchanged scope skips the census. Optimization only — never
// a correctness dependency.
func stampInfraScopePreflightClean(cityPath string) error {
	dir, present := infraScopeMigrationSource(cityPath)
	if !present {
		return nil
	}
	sig, err := infraScopeStatSignature(dir)
	if err != nil {
		return err
	}
	storeDir := nudgesdb.StoreDir(cityPath)
	if err := os.MkdirAll(storeDir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(storeDir, "infra-scope-preflight.clean.tmp-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	if _, err := fmt.Fprintln(tmp, sig); err != nil {
		_ = tmp.Close()
		_ = os.Remove(name)
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(name)
		return err
	}
	if err := os.Rename(name, infraScopePreflightMarkerPath(cityPath)); err != nil {
		_ = os.Remove(name)
		return err
	}
	return nil
}

// cappedInfraOrphanIDs bounds the id list in the preflight's error so a large
// classifier drift does not print thousands of ids; the count in the message
// stays exact.
func cappedInfraOrphanIDs(ids []string) []string {
	const limit = 10
	if len(ids) <= limit {
		return ids
	}
	out := append([]string(nil), ids[:limit]...)
	return append(out, fmt.Sprintf("… (+%d more)", len(ids)-limit))
}
