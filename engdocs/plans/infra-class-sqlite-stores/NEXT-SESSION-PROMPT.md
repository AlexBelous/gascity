# Next-session prompt (paste this to continue)

Continue the **infra-class SQLite stores** track in the existing worktree
`/data/projects/gascity/.claude/worktrees/sqlit` (branch `worktree-sqlit`,
pushed as `origin/feat/infra-class-sqlite-stores`). Do NOT create a new
worktree; run everything from that directory.

**First, read (in this order):**
1. `engdocs/plans/infra-class-sqlite-stores/HANDOFF.md` — state, next
   steps, gotchas. START HERE. P1 orders + P2 nudges + P3 messaging are
   COMPLETE; **P4 sessions+waits slices 1–3 are DONE** (seam plan doc,
   sessionsdb store + conformance + crash gate, shadow-write gate with
   boot seed + `gc doctor` sessions-shadow diff).
2. `engdocs/plans/infra-class-sqlite-stores/P4-SESSIONS-SEAM-PLAN.md` —
   the evidence inventory + ratified seam decision + slice-4 spec. The
   five routing bypass gaps listed there are the wiring checklist.
3. `engdocs/design/infra-class-sqlite-stores.md` — Sessions + waits
   section (the design spec) and "Migration & cutover".

**Then execute P4 slice 4 — the wiring flip + migration (may split into
flip and migration commits, P1–P3 precedent):**

1. **Ratchet flip + routing**: `sqliteCapableBeadClasses` += sessions +
   config acceptance test; sessions routing (marker-FIRST
   `.gc/store/sessions.migrated`, ENOENT-only stat, config cache,
   fail-CLOSED at every root) plugged into `resolveSessionStore` next to
   the shadow arm (shadow and backend=sqlite are mutually exclusive by
   config validation). Close the five bypass gaps from the seam plan
   (doctor_session_model raw open, cmd_mail mailbox lookups :930/:951/
   :1203, api `session.NewStore(raw)` audit, messaging-seam session legs
   — those already call resolveSessionStore, verify — and the
   agent-output manager stores).
2. **`gc session show [--json]`** (NEW command): must expose exactly the
   fields orphan-sweep.sh's jq probe reads (issue_type/status/
   metadata.state/metadata.closed, id/session_name/alias/agent_name);
   then rewrite orphan-sweep.sh's `gc bd show "$session_id"` onto it +
   embed-guard needles.
3. **Retention**: closed-session purge TTL (default 7d) via
   core.StartSweeper started at controller boot (P2/P3 precedent);
   closed waits swept with their sessions; `gc session prune` extension;
   reaper.sh's raw-SQL session-delete leg replaced.
4. **Migration**: `ensureSessionsClassMigrated` on controller boot next
   to the P1–P3 ensure* calls: reset→FULL import of open session beads +
   waits (ids preserved, `sessionsdb.ImportBead`)→copy-verify→atomic
   marker→straggler import-then-sweep; abort-before-marker on ANY
   failure; residue sweep (closed ∪ owned ∪ open-past-10m-grace).
   Upgrade-flow tests are LAW: fresh-flip idempotence, bd-truth import,
   NO-RESURRECTION retry, straggler import-then-clear,
   abort-before-marker, closed-TTL drop matrix. Doctor lockstep:
   doctor_session_model routed + migration-state surface.

**Operational protocol (do not skip):** the shadow soak runs BEFORE any
city flips — knob `[beads.classes.sessions] shadow = true`, watch
`gc doctor` check `sessions-shadow` for zero discrepancy (24–48h on mc),
THEN flip backend="sqlite".

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
and retry; pinned 2.9.0 built with the repo toolchain); umask 022 for
suites+push; new subprocess tests need integration tag + THREE census
artifacts (535/166 now) with files `git add`ed first; never time.Sleep
in tests; memstore does NOT honor explicit Create ids but bd and
sessionsdb do (cross-backend conformance nuance); the identity-keyed
messaging repair registry unwraps `ShadowPrimary()` — any new session
store wrapper must keep that unwrap working. Push to
`origin/feat/infra-class-sqlite-stores` before ending the session.
