# Next-session prompt (paste this to continue)

Continue the **infra-class SQLite stores** track in the existing worktree
`/data/projects/gascity/.claude/worktrees/sqlit` (branch `worktree-sqlit`,
pushed as `origin/feat/infra-class-sqlite-stores`). Do NOT create a new
worktree; run everything from that directory.

**First, read (in this order):**
1. `engdocs/plans/infra-class-sqlite-stores/HANDOFF.md` — state, next
   steps, gotchas. START HERE. P1 orders + P2 nudges + **P3 messaging are
   COMPLETE** (seam → typed tables → atomic wiring flip → bulletproof
   boot migration, all tested; the class relocates as one unit behind
   `[beads.classes.messaging]` + the `messaging.migrated` marker).
2. `engdocs/design/infra-class-sqlite-stores.md` — the Sessions + waits
   section is the spec for P4 (the last and most durability-critical
   class: shadow-write gated, reconciler/doctor lockstep).

**Then execute P4 sessions+waits per the design, TDD, one commit per
slice** (mirror the P3 slice structure: seam plan doc → store +
conformance + crash gate → wiring → migration with upgrade-flow tests):

1. **Seam plan doc** (evidence-grade): full read of the session-bead
   persistence edge (`internal/session/lifecycle_projection.go`, the
   session front door, wait records), the reconciler's restart-projection
   contract, `doctor_session_model.go`'s raw union, and every consumer.
2. **sessionsdb store** in `internal/classdb/sessions`: the design's
   sessions + waits tables (hot lifecycle columns + meta JSON for the
   ~75 low-churn codec keys), conformance + crash gate
   (restart-projection survival), census bump.
3. **Shadow-write gate** (design): dual-write with comparison before the
   flip — sessions is the only class with this extra stage.
4. **Wiring + migration**: ratchet flip, marker, construction-root
   routing, `gc session show/prune`, orphan-sweep.sh rewrite, closed-
   session purge TTL, upgrade-flow tests (P2/P3 lessons are LAW:
   reset+re-sync retries, import-then-sweep stragglers, ENOENT-only,
   copy-verify before the atomic marker).

**Also outstanding (smaller, can interleave):** splittest topology port
before any class flips by default (GA); storehealth StorePath/WalkSize
extension to `.gc/store/*.db`; maintenance-loop wal_checkpoint/VACUUM;
`gc doctor` migration-state surface for orders+nudges+messaging
routing/markers; ordersSQLiteRoutingActive's ENOENT conflation
(cmd/gc/order_class_store.go:50); P5 bd-surface work (write guard, read
federation); the design's chaos/soak gates.

**Discipline / gotchas (all recorded in HANDOFF.md):** sharded test
targets only; long git-commit/push timeouts (pre-commit vets + lint,
pre-push runs the full fast suite; golangci-lint lock collisions — wait
and retry; the host lint binary must be the pinned 2.9.0 built with the
repo's Go toolchain — `GOTOOLCHAIN=go1.26.5 go install ...@v2.9.0` if a
concurrent session clobbers it); umask 022 for suites+push; new
subprocess tests need integration tag + THREE census artifacts (534/165
now) with files `git add`ed first; never time.Sleep in tests; seam
interfaces need EXPORTED method names/types for cross-package structural
implementation; check the in-package test doubles before changing any
internal interface. Push to `origin/feat/infra-class-sqlite-stores`
before ending the session.
