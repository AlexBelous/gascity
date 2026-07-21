# Next-session prompt (paste this to continue)

Continue the **infra-class SQLite stores** track in the existing worktree
`/data/projects/gascity/.claude/worktrees/sqlit` (branch `worktree-sqlit`,
pushed as `origin/feat/infra-class-sqlite-stores`). Do NOT create a new
worktree; run everything from that directory.

**First, read (in this order):**
1. `engdocs/plans/infra-class-sqlite-stores/HANDOFF.md` — state, next steps,
   gotchas. START HERE. P1 orders is COMPLETE (backend seam, ordersdb store,
   routing behind the migrated marker, seamless migration + residue sweep).
2. `engdocs/design/infra-class-sqlite-stores.md` — the authoritative design;
   the Nudges section is the spec for this session.

**Then execute P2 nudges, one slice at a time, TDD, one commit per slice,
following the P1 pattern** (it is the template — study
`internal/classdb/orders`, `cmd/gc/order_class_store.go`, and
`cmd/gc/order_class_migrate.go` before writing anything):

1. **Domain backend seam** for the nudge queue (the merged two-tier model):
   inventory the flock-guarded `state.json` bucket ops and the shadow-bead
   ops; define an unexported backend interface at the nudgequeue domain
   edge; existing impl = today's two-tier machinery, byte-identical, all
   existing tests pass untouched.
2. **`internal/classdb/nudges`** (package `nudgesdb`): the design's `nudges`
   table + indexes over `internal/classdb/core`; claim = single
   UPDATE…RETURNING against a SET of queue keys (alias history, session id,
   qualified name); supersession semantics preserved, not "fixed"
   (superseded in-flight may still deliver once; dead-letter stamping never
   rolls back); both-backend conformance suite through the public queue
   surface; crash gate (acked-enqueue survival) via core's re-exec pattern
   (integration tag + three-artifact census bump).
3. **Wiring** behind `[beads.classes.nudges]` + `.gc/store/nudges.migrated`
   (mirror orders exactly: routing resolver, fail-closed roots, seam-guard
   test, ratchet flip + config acceptance test); fold in the
   `session.Manager` deferred-submit direct write
   (`internal/session/submit.go:544`); wake socket and session/epoch fences
   unchanged.
4. **Migration** (drain-or-import live queue + ≤24h shadow history) + marker
   + residue cleanup; **`nudge.*` typed events**
   (`events.RegisterPayload`!); reaper.sh nudge-leg rewrite per the design's
   bd-surface story; terminal-row TTL retention via `core.StartSweeper` —
   nudges has NO existing retention path (unlike orders, where the routed
   watchdog already owned it), so the store's own sweeper is correct here.

**Discipline / gotchas (all recorded in HANDOFF.md):**
- Sharded test targets only (never monolithic `go test ./cmd/gc`); give
  `git commit` a long timeout (the pre-commit hook runs `go vet ./...`).
- This box's default `umask 002` fails `TestWriteRunMap*` everywhere incl.
  clean main — run full suites and `git push` under `(umask 022 && …)`.
- New subprocess tests: `//go:build integration` + the THREE lockstepped
  census artifacts; `git add` new test files before running the census.
- Never `time.Sleep` in tests (fixed_sleep ratchet is hard); consecutive
  `time.Now()` stamps are distinct.
- Never wrap a beads.Store that flows into capability-asserting paths —
  thread routing explicitly (dispatcher field / params / State provider).
- Push to `origin/feat/infra-class-sqlite-stores` before ending the session.
