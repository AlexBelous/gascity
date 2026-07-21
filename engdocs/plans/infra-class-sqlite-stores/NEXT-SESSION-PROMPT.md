# Next-session prompt (paste this to continue)

Continue the **infra-class SQLite stores** track in the existing worktree
`/data/projects/gascity/.claude/worktrees/sqlit` (branch `worktree-sqlit`,
pushed as `origin/feat/infra-class-sqlite-stores`). Do NOT create a new
worktree; run everything from that directory.

**First, read (in this order):**
1. `engdocs/plans/infra-class-sqlite-stores/HANDOFF.md` — state, next
   steps, gotchas. START HERE. P1 orders + P2 nudges are COMPLETE; P3
   messaging slices 1–2 (mail seam + messagingdb store) are DONE and
   deliberately DARK — no routing until the extmsg tables exist.
2. `engdocs/plans/infra-class-sqlite-stores/P3-MESSAGING-SEAM-PLAN.md` —
   the mail-half plan, including the atomic-flip decision (ClassMessaging
   = mail + all gc:extmsg-* records).
3. `engdocs/design/infra-class-sqlite-stores.md` — the extmsg table
   sketches in the Messaging section are the spec for the next slices.

**Then execute the remaining P3 slices, TDD, one commit per slice:**

1. **extmsg seam plan doc** (like the P2/P3 ones, evidence-grade): full
   read of internal/extmsg — the label-KV record shapes
   (gc:extmsg-binding/-delivery/-group/-group-participant/-transcript/
   -membership/-transcript-state), the in-process per-conversation mutex
   pool that is invalid under multi-process access (its invariants become
   UNIQUE constraints), the bind/rebind multi-record commit, the 64-entry
   transcript buckets (die — range scans over idx(conv-cols, sequence)),
   `next_sequence` allocation (becomes UPDATE…RETURNING), and every
   consumer (extmsg services in api_state.go, the reaper legs in
   city_runtime.go:1248/1255, gc extmsg CLI as pure API client).
2. **extmsg typed tables** in the SAME messaging.db (extend messagingdb's
   migrations — version-gated), implementing the extmsg persistence edge
   behind a seam; both-backend conformance through the extmsg services
   surface; transcript retention (max_retained_entries becomes real) +
   closed bindings/memberships TTL.
3. **Wiring** (one slice, whole class): flip
   `sqliteCapableBeadClasses[BeadClassMessaging]` + config acceptance
   test; `messaging.migrated` marker; route the construction roots —
   `newCityMailProvider`, `openCityMailProvider`, cmd_handoff.go:338,
   nudge-mail sweep mail leg → `Provider.SweepReadMessages`, wisp GC →
   `PurgeReadMessages`, extmsg services — fail-closed (erroring backend),
   seam-guard test. Decide the resolver home per the nudges precedent
   (shared resolver in messagingdb if >1 package constructs; today all
   construction is cmd/gc, so the orders cmd/gc-local pattern may
   suffice — internal/api consumes state.MailProvider).
4. **Migration + marker + residue + retention**: import open mail (drop
   >30d unread, >TTL read) + extmsg actives with copy-verify before the
   atomic marker + straggler pass (P2's lessons are LAW here: later boots
   must import-then-sweep stragglers; the pre-marker migration must reset
   and re-sync so an aborted attempt never resurrects consumed mail; only
   ENOENT means "unmigrated"); residue sweep; store retention sweeper on
   the controller (read close→purge + unread TTL); reaper.sh mail_wisps
   count + issue_type='message' filters; doctor_backlog_depth +
   hook-claim raw-consumer notes.

**Discipline / gotchas (all recorded in HANDOFF.md):** sharded test
targets only; long git-commit/push timeouts (pre-commit vets, pre-push
runs the full fast suite; golangci-lint lock collisions — wait and retry);
umask 022 for suites+push; new subprocess tests need integration tag + the
THREE census artifacts (533/165 now) with files `git add`ed first; never
time.Sleep in tests; new event types ripple through genspec + genclient +
dashboard client + dist; `TestPackCommandExitReturnsThroughRun` flakes
under parallel shards (tmux-socket noise — rerun in isolation). OPEN
follow-up: ordersSQLiteRoutingActive's marker-stat ENOENT conflation.
Push to `origin/feat/infra-class-sqlite-stores` before ending the session.
