# Next-session prompt (paste this to continue)

Continue the **infra-class SQLite stores** track in the existing worktree
`/data/projects/gascity/.claude/worktrees/sqlit` (branch `worktree-sqlit`,
pushed as `origin/feat/infra-class-sqlite-stores`). Do NOT create a new
worktree; run everything from that directory.

**First, read (in this order):**
1. `engdocs/plans/infra-class-sqlite-stores/HANDOFF.md` — state, next steps,
   gotchas. START HERE.
2. `engdocs/design/infra-class-sqlite-stores.md` — the authoritative design
   (ratified decisions, per-class schemas, migration story).

**Then execute HANDOFF "Next: P1 orders", one slice at a time, TDD, one
commit per slice:**
1. **Backend extraction** (byte-identical): unexported domain-level
   `trackingBackend` interface in `internal/orders`; move the bead/label
   bodies into a `beadsTracking` impl; `Store` keeps the graph-leg
   composition (`NewStoreWithGraph`/`mixedLegStores` already exist —
   preserve their dedupe via an optional `underlying() beads.Store`
   assertion). Public surface unchanged; all existing orders tests must pass
   untouched.
2. **`internal/classdb/orders`**: `order_run` table per the design's Orders
   schema (created_at DESC, id DESC tie-break; partial open indexes) over
   `internal/classdb/core`; run the orders store test suites against BOTH
   backends; crash-durability via core's re-exec pattern (integration tag —
   see census gotcha).
3. **Wiring**: dispatch in `resolveOrderStore` on
   `cfg.Beads.ClassBackend(config.BeadClassOrders)` → `.gc/store/orders.db`
   (controller persistent handle; CLI `WithSingleConn`); flip
   `sqliteCapableBeadClasses["orders"] = true` + update
   `TestBeadsClassesSQLiteRejectedUntilImplemented`; construct
   `NewStoreWithGraph(sqliteBacked, graphStore)` so wisp-root evidence keeps
   unioning.
4. **Migration + migrated-marker** per the design's "Seamless upgrade"
   section; retention sweeper (7d delete_after_close, retain-last-10).

**Discipline / gotchas (all recorded in HANDOFF.md):**
- Quality gates per slice: package tests + affected neighbors; sharded
  targets only (never monolithic `go test ./cmd/gc`).
- This box's default `umask 002` fails `TestWriteRunMap*` everywhere incl.
  clean main — run pre-push/full suites and `git push` under
  `(umask 022 && …)` for CI parity.
- New subprocess-spawning tests: `//go:build integration` tag, else the
  resource-census untagged ratchet fires; ScopeAll bumps touch THREE
  lockstepped artifacts (resourcecensus/census.go, test/test-resources.toml,
  TESTING.md).
- Do not port Dolt-lag workarounds (close-verify retries, ≥20-char
  close_reason floors) into the sqlite backend.
- Push to `origin/feat/infra-class-sqlite-stores` before ending the session.
