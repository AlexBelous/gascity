# Next-session prompt (paste this to continue)

Continue the **infra-class SQLite stores** track in the existing worktree
`/data/projects/gascity/.claude/worktrees/sqlit` (branch `worktree-sqlit`,
pushed as `origin/feat/infra-class-sqlite-stores`). Do NOT create a new
worktree; run everything from that directory.

**First, read (in this order):**
1. `engdocs/plans/infra-class-sqlite-stores/HANDOFF.md` — state, next
   steps, gotchas. START HERE. **P1 orders + P2 nudges + P3 messaging +
   P4 sessions+waits are ALL COMPLETE** (seam → store + conformance +
   crash gate → shadow-write gate → marker-gated fail-closed routing +
   bulletproof boot migration + gc session show + orphan-sweep rewrite +
   closed-row retention).
2. `engdocs/design/infra-class-sqlite-stores.md` — the P5 section ("The
   bd / raw-prompt story" + "Doctor / storehealth / maintenance") is the
   spec for what remains.

**Operational gate before any real city flips sessions:** run the shadow
soak on mc — `[beads.classes.sessions] shadow = true`, watch `gc doctor`
check `sessions-shadow` for zero discrepancy 24–48h — THEN swap the knob
to `backend = "sqlite"` (drop shadow; the combination is rejected).

**Then execute P5 — bd surface + cleanup (per the design work plan):**

1. **`gc bd` write guard**: `doBd` refuses create/update/close targeting
   infra classes (reserved-prefix id, or a create carrying an infra
   type/label) with a message naming the `gc` replacement.
2. **Generalized read federation**: port `cmd_bd_show_fed.go`
   (`124bca8c3`) and widen from BeadClassGraph to a loop over
   `config.ReservedClassPrefixes()`, backed by per-class by-id reads;
   preserve the 404-vs-error discipline. Note: MIGRATED legacy ids
   (gc-*/mc-*) don't match reserved prefixes — the probe-all fallback
   (storeref.Resolve) is what covers them.
3. **`gc doctor` migration-state surface**: routing/marker state for all
   four classes (ordersSQLiteRoutingActive's ENOENT conflation at
   cmd/gc/order_class_store.go:50 gets fixed as part of this — port the
   sessions/nudges ENOENT-only discipline; the bool signature grows an
   error).
4. **storehealth** StorePath/WalkSize extension to `.gc/store/*.db`;
   **maintenance loop** per-file wal_checkpoint(TRUNCATE) + periodic
   VACUUM.
5. **Retire dead code** (design list): close-verify retries,
   close_reason floors, `UpdateMetadataInfo` (sqlite batches are
   atomic), doctor_backlog_depth notification/control-plane buckets,
   doltlite order-run cache, nudge two-tier machinery residue. Also the
   mail storeless-provider raw leg (openMailTargetStore — the deferred
   "mail DI pass").
6. **splittest topology port** before any class backend defaults to
   sqlite (GA); the design's every-prompt drain soak + chaos gates;
   final census. Create-side guard: beadPolicyStore.createTarget is
   still identity — the write guard (item 1) is the designed cover.

**Discipline / gotchas (all recorded in HANDOFF.md):** sharded test
targets only; long git-commit/push timeouts (pre-commit vets + lint +
docsync, pre-push runs the full fast suite; golangci-lint lock
collisions — wait and retry; pinned 2.9.0 built with the repo
toolchain); umask 022 for suites+push; new subprocess tests need
integration tag + THREE census artifacts (535/166 now) with files `git
add`ed first; never time.Sleep in tests; memstore does NOT honor
explicit Create ids but bd and sessionsdb do; the identity-keyed
messaging repair registry unwraps `ShadowPrimary()`; the doctor
name-set golden (cmd/gc/testdata/doctor_check_names.golden) must gain
any new check's name. Push to `origin/feat/infra-class-sqlite-stores`
before ending the session.
