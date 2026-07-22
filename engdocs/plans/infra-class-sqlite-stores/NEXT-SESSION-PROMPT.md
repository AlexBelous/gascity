# Next-session prompt (paste this to continue)

Continue the **infra-class SQLite stores** track in the existing worktree
`/data/projects/gascity/.claude/worktrees/sqlit` (branch `worktree-sqlit`,
pushed as `origin/feat/infra-class-sqlite-stores`). Do NOT create a new
worktree; run everything from that directory.

**First, read (in this order):**
1. `engdocs/plans/infra-class-sqlite-stores/HANDOFF.md` — state, next
   steps, gotchas. START HERE. P1 orders + P2 nudges are COMPLETE; P3
   messaging slices 1–4 (mail seam + messagingdb messages store + the
   extmsg seam plan + the extmsg fabricBackend seam refactor) are DONE
   and deliberately DARK — no routing until the extmsg tables exist.
2. `engdocs/plans/infra-class-sqlite-stores/P3-EXTMSG-SEAM-PLAN.md` —
   THE spec for the next two slices: record/op inventory, what stays
   above the seam, invariant→constraint mapping, schema deviations,
   concurrency stance (controller-resident writers; lock pool stays),
   conformance/crash-gate plan, wiring-hazard inventory. Trust it — it
   was derived from a full package read; spot-check cited lines rather
   than re-reading all of internal/extmsg.
3. `engdocs/plans/infra-class-sqlite-stores/P3-MESSAGING-SEAM-PLAN.md` —
   the mail half + the atomic-flip decision (ClassMessaging = mail + all
   gc:extmsg-* records; ONE knob, ONE marker, only after extmsg tables).

**Then execute the remaining P3 slices, TDD, one commit per slice:**

1. **feat(classdb): extmsg typed tables** in messagingdb (migration
   Version 2, same db/Store/mint/id_seq). Seven tables + partial UNIQUE
   constraints + meta JSON column per plan "Schema" section; implements
   fabricBackend structurally; Import* primitives (verbatim ids/clocks,
   OR IGNORE, incl. max-generation ended-binding import); dormant
   retention (transcript prune → earliest_available_sequence advance;
   30d TTL sweeps sparing max-generation ended binding). Both-backend
   conformance through the public extmsg.Services surface (nudges
   eachBackend pattern); crash gate (acked Bind + acked Append survive
   SIGKILL; integration tag; bump the THREE census artifacts, `git add`
   new files first). The backend contract is `internal/extmsg/backend.go`
   (fabricBackend + FabricWriter + transport structs) — implement it
   structurally; do NOT alter the interface without re-running the whole
   extmsg suite.
2. **Wiring** (one slice, whole class): per NEXT-slice inventories in
   BOTH P3 plan docs — ratchet flip + config acceptance test +
   `messaging.migrated` marker + route mail construction roots
   (newCityMailProvider, openCityMailProvider, cmd_handoff.go:338,
   sweep/wisp-GC legs) AND extmsg roots (api_state.go:214/:733, routed
   twins of ReassignSessionBindings / ReassignSessionParticipants /
   CloseSessionBindings / ReapStale* for the session-repair call sites
   listed in the extmsg plan — they currently pass SESSION-class
   stores and would silently no-op post-flip) + seam-guard test +
   fail-closed erroring backend. Resolver home: internal/api also
   produces record traffic → nudges precedent (resolver in the classdb
   package) likely; decide there.
3. **Migration + marker + residue + retention**: import open mail (drop
   >30d unread, >TTL read) + extmsg actives (active bindings +
   max-generation ended per conv, open delivery/groups/participants/
   memberships, transcript state + retained entries) with copy-verify
   before the atomic marker + straggler re-import (P2's lessons are
   LAW: later boots import-then-sweep; pre-marker reset+re-sync; only
   ENOENT = unmigrated); residue sweep (extmsg beads are durable task
   beads, plain-List deletion); controller retention sweeper (mail read
   close→purge + unread TTL + extmsg sweeps); reaper.sh mail_wisps
   count + issue_type='message' filters; doctor_backlog_depth +
   hook-claim raw-consumer notes.

**Discipline / gotchas (all recorded in HANDOFF.md):** sharded test
targets only; long git-commit/push timeouts (pre-commit vets, pre-push
runs the full fast suite; golangci-lint lock collisions — wait and retry);
umask 022 for suites+push; new subprocess tests need integration tag + the
THREE census artifacts (533/165 now) with files `git add`ed first; never
time.Sleep in tests; `TestPackCommandExitReturnsThroughRun` flakes under
parallel shards (rerun in isolation). OPEN follow-up:
ordersSQLiteRoutingActive's marker-stat ENOENT conflation.
Push to `origin/feat/infra-class-sqlite-stores` before ending the session.
