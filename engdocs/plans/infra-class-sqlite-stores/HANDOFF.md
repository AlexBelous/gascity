# Infra-class SQLite stores — implementation handoff

**Design (authoritative):** `engdocs/design/infra-class-sqlite-stores.md`
**Branch:** `feat/infra-class-sqlite-stores` (worktree `worktree-sqlit`), based on `origin/main` @ `5131e3b57`.

## Done (all committed, hooks green, affected packages tested)

- **Design doc** incl. ratified decisions (multi-process WAL access; one file
  per class under `.gc/store/`; nudges two-tier merge; full bd-surface story;
  seamless auto-migration on first boot with migrated-marker routing).
- **P0.1** `[beads.classes.<name>]` config knob, fail-closed
  (`sqliteCapableBeadClasses` ratchet — flip a class's entry when its store
  lands, and update `TestBeadsClassesSQLiteRejectedUntilImplemented`).
- **P0.2** `ValidateBeadsClassPrefixes` — reserved-prefix shadowing fatal only
  once a class backend is non-bd; wired into Parse + the 3 no-re-Parse edit
  paths.
- **P0.3** `openNudgeBeadStore` nil-cfg fix +
  `TestClassResolversNeverCalledWithNilConfig` guard (cmd/gc-wide).
- **P0.4** extmsg two-store seam: `NewServicesWithSessionStore(msg, sess)`;
  reapers take both class handles; api_state services construct through the
  resolvers; distinct-MemStore tests pin read/write class separation.
- **G0 PASSED** — `internal/classdb/core`: the shared substrate (modernc
  `_pragma` DSN — note the deleted store's `?_busy_timeout` was silently
  ignored, don't copy it; `_txlock=immediate`; version-gated migrations that
  refuse newer-schema files; Write w/ busy retry; read pool / WithSingleConn;
  sweeper; IntegrityCheck). `multiprocess_test.go` proves 5-process
  concurrent WAL writes lose nothing and SIGKILL loses no acked commit.

Known-red locally: `TestWriteRunMap*/TestPruneRunMap*` fail under this box's
default `umask 002` — proven identical on clean origin/main; CI (umask 022)
is unaffected. Not ours.

## Next: P1 orders (the in-flight step)

`internal/orders.Store` already has the right shape: it confines the bead
codec and already unions mixed orders+graph reads via
`NewStoreWithGraph`/`mixedLegStores()` (store.go:197-235). Plan:

1. **Backend extraction (byte-identical refactor).** Introduce an unexported
   domain-level `trackingBackend` interface = the ORDERS-LEG ops only
   (CreateRun, CreateRunClosed, SetOutcome, SetCursor, MarkFailed, CloseRun,
   CloseRuns, DeleteRun, Get, RunDetail, RecentRuns(All), OpenRuns,
   StaleOpenRuns, OrphanedOpenRuns, ClosedRunsForRetention, ListTracking,
   LatestOpenRun, plus orders-leg halves LastRunTracking / CursorTracking /
   HasOpenTracking). Move the existing bead/label bodies into a
   `beadsTracking` impl; `Store` keeps the graph-leg composition (fold max /
   union with the graph store when distinct). Graph-leg dedupe: optional
   in-package assertion `underlying() beads.Store` on the backend —
   beadsTracking returns its store (dedupe as today); the sqlite backend
   returns nil (graph leg always unions). Public surface unchanged; all
   orders tests must pass untouched.
2. **`internal/classdb/orders`** — `order_run` table + indexes exactly per
   the design's Orders section (incl. created_at DESC, id DESC tie-break in
   `idx_run_scoped_created`; partial open indexes). Implements
   `trackingBackend` over `core.DB`. Conformance: run the orders store_test
   suites against BOTH backends (table-driven backend factory); add
   crash-durability (kill mid-CreateRun → orphan-open row survives; startup
   sweep contract) via core's re-exec pattern.
3. **Wiring**: `resolveOrderStore` dispatch on `cfg.Beads.ClassBackend
   (orders)` → open `.gc/store/orders.db` (controller: persistent; CLI:
   WithSingleConn). Flip `sqliteCapableBeadClasses["orders"]=true`. The
   dispatch must construct `NewStoreWithGraph(sqliteBacked, graphStore)` so
   the wisp-root leg keeps unioning.
4. **Migration + migrated-marker**: seed from bd (closed runs ≤7d + open
   markers; seq from tracking∪wisp-root legs); marker file decides routing
   (design "seamless upgrade" section); `gc doctor` surfaces state.
   Retention sweeper: 7d delete_after_close, retain-last-10 per scoped_name.
5. Then P2 nudges (merged queue) → P3 messaging → P4 sessions per the design
   work plan; splittest topology port from `feat/split-store-conformance`
   before the first class flips by default.

## Gotchas carried forward

- `make test` monolithic cmd/gc run exceeds its timeout on this box — use
  the sharded targets (TESTING.md).
- Adding a config field: BeadsConfig has no patch/override sync (city-level
  only), but `knownTOMLKeys` in undecoded.go must list any new struct type
  (BeadClassConfig is already there).
- New test package: run `go run scripts/add-testenv-import.go`.
- Close-verify retries and ≥20-char close_reason floors are Dolt-lag
  workarounds — do NOT port them into the sqlite backend; retire at call
  sites in the wiring slice.
