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
	"fmt"
	"os"
	"path/filepath"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/config"
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
