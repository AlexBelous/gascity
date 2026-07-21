# Infra-class SQLite stores — implementation handoff

**Design (authoritative):** `engdocs/design/infra-class-sqlite-stores.md`
**Branch:** `feat/infra-class-sqlite-stores` (worktree `worktree-sqlit`), based on `origin/main` @ `5131e3b57`.

## Done (all committed, hooks green, affected packages tested)

- **Design doc** incl. ratified decisions (multi-process WAL access; one file
  per class under `.gc/store/`; nudges two-tier merge; full bd-surface story;
  seamless auto-migration on first boot with migrated-marker routing).
- **P0.1** `[beads.classes.<name>]` config knob, fail-closed
  (`sqliteCapableBeadClasses` ratchet — orders is now flipped true).
- **P0.2** `ValidateBeadsClassPrefixes`; **P0.3** `openNudgeBeadStore` nil-cfg
  fix + `TestClassResolversNeverCalledWithNilConfig`; **P0.4** extmsg
  two-store seam.
- **G0 PASSED** — `internal/classdb/core`: shared substrate (modernc
  `_pragma` DSN, `_txlock=immediate`, version-gated migrations, busy-retry
  Write, read pool / WithSingleConn, sweeper, IntegrityCheck); 5-process WAL
  + SIGKILL durability proven (integration tier).
- **P1 orders — COMPLETE (this session).** Four slices:
  1. `refactor(orders)` b76e2dd10 — unexported `trackingBackend` seam in
     internal/orders; bead/label bodies moved verbatim into `beadsTracking`;
     mixed reads (LastRun/Cursor/HasOpenWork) fold backend halves
     (`LastRunTracking`/`CursorTracking`/`HasOpenTracking`) with a graph leg
     deduped via the optional `beadsBacked.underlying()` assertion. New
     `Store.DeleteRun`. Five test seed lines changed `st.store.Create` →
     `rec.Create` (same object); all assertions untouched.
  2. `feat(classdb)` 64c4f3a45 — `internal/classdb/orders` (package
     `ordersdb`): design's `order_run` schema over core, id mint
     (`gco-<n>`, same-tx counter), indexed MAX/EXISTS point reads, monotonic
     `seq = MAX(seq, new)`. Both-backend conformance suite through the
     public `*orders.Store` surface; crash-durability re-exec gate
     (integration tag; census 531/163 across the three artifacts).
     `orders.RunOutcome.Token()/RunOutcomeFromToken` +
     `orders.NewStoreWithTracking`.
  3. `refactor+feat(orders)` dd42d976d + 0ac8ee194 — every front-door
     construction in cmd/gc + internal/api routes through one seam
     (`orderFrontForStore` / `orderFrontResolver`); ratchet flipped;
     `cmd/gc/order_class_store.go` = `orderClassRoutingFor` (two-key
     activation: config backend=sqlite AND `.gc/store/orders.migrated`
     marker; process-wide handle cache; fail-CLOSED on open error at every
     root — dispatcher tick + webhook refuse to dispatch). Sweeps/retention
     got `...Routed` variants (unrouted names remain the bd test surface);
     retention delete branches (routed → `front.DeleteRun`; bd →
     `deleteWorkflowBead`); event-cursor bd label-scan override is bd-only.
     `Store.CloseRunsSwept` (sweep_by column on sqlite) absorbed cmd/gc's
     `closeAndVerifyOrderTrackingBeads` twin (deleted, drift guard retired).
     API routes via optional `orderFrontDoorProvider` on State
     (controllerState implements). Guard test
     `TestOrderFrontSeamIsTheOnlyConstructionPoint` forbids direct
     `orders.NewStore*` in cmd/gc outside the two seam files.
  4. `feat(orders)` (migration) — `cmd/gc/order_class_migrate.go`:
     `ensureOrdersClassMigrated` on controller boot (import → copy-verify →
     atomic marker → straggler re-import; abort before marker on ANY
     scope-store open failure), selection mirrors retention (open ∪
     closed≤TTL ∪ newest-10 closed; newest run carries the max seq so the
     cursor survives); `sweepLegacyOrderTrackingResidue` clears bd copies in
     the background, sparing open beads younger than the 10m grace
     (mixed-version double-fire protection), converging across boots.
     `ordersdb.ImportRun` preserves legacy ids/clocks. Fresh cities flip
     immediately.

Known-red locally: `TestWriteRunMap*/TestPruneRunMap*` fail under this box's
default `umask 002` — proven identical on clean origin/main; CI (umask 022)
is unaffected. Not ours. Run suites and `git push` under `(umask 022 && …)`.

## Design deviations (deliberate, review-worthy)

- **Retention sweeper**: the design says "enforced by the store's sweeper";
  instead the ONE existing retention path now routes (watchdog + CLI via
  `front.ClosedRunsForRetention` + `DeleteRun`) — a second store-internal
  sweeper would duplicate the same policy. `core.StartSweeper` remains for
  classes that need it (nudges/messaging TTLs have no existing path).
- **CLI conn mode**: CLI one-shots share the process-wide pooled handle
  (connections open lazily, so a one-shot pays only for what it uses);
  `core.WithSingleConn` is still what the crash child and any explicit
  short-lived opener uses. G0's SIGKILL gate is the justification for
  process-exit-without-Close.
- **`gc doctor` surface for migration state**: NOT yet done (design asks for
  it) — fold into P5's doctor/storehealth extension or a small follow-up
  (`ordersSQLiteRoutingActive` + marker path are trivial to surface).

## Next: P2 nudges (per the design work plan)

Merged queue (state.json + shadow beads → one `nudges` table over core),
`nudge.*` typed events, reaper.sh nudge-leg rewrite, deferred-submit
fold-in. Before the first class flips **by default** (GA), port the
splittest topology suite from `feat/split-store-conformance`. Also
outstanding from the design: storehealth `StorePath`/`WalkSize` extension to
`.gc/store/*.db`, maintenance-loop `wal_checkpoint(TRUNCATE)`/`VACUUM`, and
the P5 bd-surface work (gc bd write guard, generalized read federation).

## Gotchas carried forward

- `make test` monolithic cmd/gc run exceeds its timeout on this box — use
  the sharded targets (TESTING.md). Pre-commit hook's `go vet ./...` can
  exceed 2m — give `git commit` a long timeout.
- New subprocess-spawning tests need `//go:build integration` AND the
  three-artifact census bump (resourcecensus/census.go,
  test/test-resources.toml, TESTING.md). The census counts TRACKED files —
  `git add` new test files before running it. The fixed_sleep ratchet is
  hard: don't add `time.Sleep` to tests (consecutive `time.Now()` stamps are
  distinct; the id-DESC tie-break covers ties).
- Adding a config field: `knownTOMLKeys` in undecoded.go must list any new
  struct type (BeadClassConfig already there).
- New test package: run `go run scripts/add-testenv-import.go`.
- Close-verify retries and ≥20-char close_reason floors are Dolt-lag
  workarounds — NOT ported into the sqlite backend; the cmd/gc twin is gone.
- The routed wrapper hazard: never wrap a beads.Store that flows into
  capability-asserting paths (molecule.Instantiate, GraphApplyStore) —
  optional capabilities don't promote through wrappers. Routing therefore
  travels explicitly (dispatcher field / function params / State provider),
  never by wrapping scope stores.
- `orders.NewStoreWithTracking` takes the unexported interface: callers pass
  any structural implementation; keep it that way (the backend type never
  becomes public API).
