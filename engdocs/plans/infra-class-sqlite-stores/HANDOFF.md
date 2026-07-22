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

## P2 nudges — COMPLETE (all four slices)

- **Seam plan**: `P2-NUDGES-SEAM-PLAN.md` (this dir) — evidence-grade op
  inventory + slice specs. Read it before touching nudges.
- **Slice 1 DONE** (`refactor(nudgequeue)`): the queue ops are reified as
  `nudgequeue.Queue` over an unexported `queueBackend` — file backend =
  moved WithState-closure bodies (flock authority + lazy shadow beads +
  maintenance passes + retry policy constants, all exported vocabulary).
  `ClaimTarget` carries claim identity as plain values (SQL-translatable);
  `ClaimDueMatching` remains the file-only func-predicate compat surface.
  cmd/gc keeps wrappers under existing names (~90 test sites untouched);
  `session.Manager` deferred submit → `Queue.EnqueueDeferred`; read-only
  LoadState sites → `Queue.Snapshot`. Behavioral suite `queue_test.go` is
  the portable conformance base.

- **Slice 2 DONE** (`feat(classdb)` nudgesdb): the merged `nudges` table
  over classdb/core implements `queueBackend` — enqueue = one INSERT,
  claim = deterministic select-then-update in one immediate tx (fence gate
  as SQL), terminal transitions are row updates, supersession one UPDATE,
  maintenance set-based in the same txs (dead-letter stamping now atomic
  with the transition — ratified-better). `bead_id` transition column
  beyond the design sketch (lossless import round-trip).
  `FindRecord/FindRecordIncludingTerminal` replace the shadow reads for
  the wait paths; `SweepRetention` implements dead(1h)→terminal→TTL-swept.
  Both-backend conformance in classdb/nudges/conformance_test.go; crash
  gate (acked enqueue survives SIGKILL); census 532/164.

- **Slice 3 DONE** (`feat(nudges)` 5c0a5223c — routing): ratchet flipped
  (`sqliteCapableBeadClasses` += nudges) + config acceptance test.
  DELIBERATE DEVIATION from the orders pattern: the routing resolver lives
  in nudgesdb (`Routed` / `SharedStoreFor` / `QueueForCity`), not cmd/gc,
  because THREE packages produce queue traffic — cmd/gc (`cityNudgeQueue`
  delegates), internal/session (deferred submit, submit.go), internal/api
  (wait-nudge withdraw). `Routed` is marker-FIRST (unmigrated cities never
  load config) and self-loads via `config.LoadWithIncludes` when cfg is nil
  (safe: the cmd/gc loader extras never touch `[beads]`); a marked city
  whose config can't load or store can't open gets
  `nudgequeue.NewUnavailableQueue` — every op fails closed, no bd fallback.
  Wait-path shadow reads route through the cmd/gc `nudgeShadowReader` seam
  (file: `*nudgequeue.Store`; routed: `FindRecord*` +
  `TerminalRecord.Shadow()` projection — dead rows always carry terminal
  stamps, so the projection is total). Both package-level
  `WithdrawWaitNudges` call sites (cmd_wait.go + api/wait_nudges.go) route
  through `Queue.WithdrawQueuedWaitNudges` (file leg byte-identical).
  Nudge-mail sweep gained `...Routed` variants (unrouted names = bd test
  surface): routed nudge leg = `SweepRetention`/`CountRetention` over the
  24h terminal TTL, budget-exempt; `--nudge-ttl` governs only the bd shadow
  shape (review-flagged). Raw NudgesStore wraps now route through
  `resolveNudgesStore` (cmd_nudge managed-wake, cmd_sling, three cmd_wait
  roots); cmd_order sweep wraps deliberately left (routed leg bypasses the
  store; bd leg identity forever). Import primitives (`ImportItem` /
  `ImportTerminalShadow`) landed HERE (only public surface carrying a
  legacy terminal clock — the routed-sweep test needs one). Seam guard:
  `TestNudgeQueueSeamIsTheOnlyConstructionPoint`.

- **Slice 4 DONE** (`feat(nudges)` be1e5f9bf — migration + events +
  retention + reaper): `cmd/gc/nudge_class_migrate.go`
  (`ensureNudgesClassMigrated` on controller boot next to orders'): imports
  live buckets verbatim + ≤24h TERMINAL shadow history — the history import
  is CORRECTNESS, not observability: post-cutover wait finalization reads
  `FindRecordIncludingTerminal`, so a pre-cutover delivery must stay
  findable or its wait wedges. Copy-verify → atomic marker → straggler
  re-import; aborts before the marker on ANY store-open/import failure.
  `sweepLegacyNudgeResidue` (bg): clears file items the class store owns
  (`nudgequeue.SweepFileResidue` — stops old-binary pollers redelivering)
  and deletes bd shadows (closed ∪ class-owned ∪ open-past-10m-grace;
  fresh open UNKNOWN ids spared for the next boot's import-then-sweep).
  `nudge.queued/delivered/dead` typed events
  (`events.NudgeLifecyclePayload`, registered in events/payloads.go init;
  spec + genclient + dashboard TS client + dist regenerated,
  dashboard-check green) fired from cmd/gc queue wrappers for BOTH
  backends; maintenance-internal expiry dead-letters are NOT evented.
  Retention: `nudgesdb.Store.StartRetentionSweeper` (core.StartSweeper,
  sync.Once per shared handle) started at controller boot — the
  SDK-self-sufficient path (nudge-mail watchdog rides the order dispatch
  tick); overlapping triggers converge on idempotent SweepRetention.
  reaper.sh Step 4 raw-SQL expiry close → one city-level
  `gc order sweep-nudge-mail` step (both backends); embed-guard needle
  moved from "expires_at" to "sweep-nudge-mail"; Step 5's expires_at
  exclusion untouched; bundled-pack pin NOT bumped (precedent: prior
  reaper.sh edits don't).

- **Review-hardening DONE** (post-slice-4 fix commit, from a 4-dimension
  adversarial review of both commits): (1) later boots now actually run the
  documented import-then-sweep — `sweepLegacyNudgeResidue` merge-imports
  file items the class store doesn't own before clearing residue, so an
  enqueue racing the marker (or an old binary's post-marker append) is
  never stranded; (2) the pre-marker migration RESETS the class store's
  live rows and re-imports the file's current truth, so an interrupted
  attempt + retry never resurrects a delivered/dead item (its terminal
  record re-enters via the shadow import); (3) `nudgesdb.Routed` treats
  only ENOENT as "unmigrated" — any other marker stat error fails closed;
  (4) dead-bucket imports carry their terminal stamps immediately (no 1h
  aging wedge for wait finalization); (5) `ShadowHistorySince` keys on
  created OR terminal clock (old-created recently-expired shadows import);
  (6) nudge event emission opens the event log directly (no per-emission
  full config load — the #2099 hook-emission norm); (7) the self-loaded
  routing decision is cached by city.toml (mtime, size).

## P3 messaging — slices 1–4 DONE (mail half + extmsg plan + seam, DARK)

- **Seam plan**: `P3-MESSAGING-SEAM-PLAN.md` (this dir) — mail op
  inventory + THE structural decision: ClassMessaging covers mail AND all
  `gc:extmsg-*` records (extmsg services are built on
  `resolveMailMessagesStore`), so the mail store lands dark and the single
  `[beads.classes.messaging]` flip + `messaging.migrated` marker +
  migration wait until the extmsg typed tables exist in the SAME
  `messaging.db` — the class relocates atomically. Do NOT flip
  `sqliteCapableBeadClasses[BeadClassMessaging]` before then.
- **Slice 1 DONE** (`refactor(beadmail)` 81e87d3e5): unexported
  `messagesBackend` seam inside beadmail; `Record`/`NewMessage` carry the
  design's row shape + bd-compat fields (`ReadLabel` for the conditional
  mark-read write; Priority/CC decode-only; ExtraLabels passthrough). bd
  backend = moved codec verbatim (backend_bead.go). Provider keeps
  addressing (session store), title derivation, the 6b0eb0d6b gate
  (`isRemovedRecord`), per-op error vocabulary (`NotAMessageError`).
  Constructors keep all signatures; `NewWithBackend` admits the class
  store. DELIBERATE TIGHTENING (no test pinned the old shape):
  Read/MarkRead/MarkUnread/Reply on a NON-message bead now error like Get
  always did, instead of mutating the foreign bead.
- **Slice 2 DONE** (`feat(classdb)` 271eec45a): `internal/classdb/messaging`
  (package messagingdb) — messages table + 3 indexes over core; `gcm-<n>`
  mint (prefix-lockstep guard test); native counts; `ImportMessage`
  (verbatim, OR IGNORE); `SweepUnreadBefore` (the design's net-new 30d
  unread TTL, dormant); `Provider.SweepReadMessages`/`PurgeReadMessages`
  backend-routed retention forms (the routed sweep callers adopt these at
  the wiring slice). Conformance: the FULL mailtest.RunProviderTests over
  `beadmail.NewWithBackend(sqlite, mem-session-store)` + both-backend
  retention/removal contract; crash gate (acked Send survives SIGKILL);
  census 533/165.

- **Slice 3 DONE** (`docs(plans)` ead558f26): P3-EXTMSG-SEAM-PLAN.md —
  evidence-grade extmsg inventory (full package read + repo-wide consumer
  sweep). Key ratifications: the seven record kinds' exact bd encodings
  (incl. the participant dual-base-label quirk and transcript text living
  in bead Description); writer topology is CONTROLLER-RESIDENT (gc extmsg
  CLI is a pure API client), so the in-process conversation lock pool
  stays the serializer ABOVE the seam and the sqlite UNIQUE constraints
  are the cross-process backstop; extmsg tables share the gcm mint, the
  id_seq counter, and the SAME messagingdb.Store as the mail backend (one
  handle per db → one lock-pool key); binding_generation monotonicity
  rule (retention spares the max-generation ended row per conversation —
  orders precedent); Append's entry-then-state-bump non-atomicity is a bd
  crash window the sqlite tx closes (bd leg preserved verbatim). NEW
  WIRING HAZARD RECORDED: session-repair call sites (session_beads.go
  :1275/:1278/:1302 via their two wrappers, session_lifecycle_parallel.go
  :2519/:2523, api/session_resolution.go:186/:189) pass SESSION-class
  stores into the three messaging-record pkg funcs
  (Reassign*/CloseSessionBindings) — silent no-op post-flip (#1939
  shape); routed twins required at the wiring slice.

- **Slice 4 DONE** (`refactor(extmsg)` 67e6b0a20 — the fabricBackend
  seam): unexported interface + exported method names/transport structs
  (backend.go), bd backend = moved codec verbatim (backend_bead.go —
  doubled base labels, participant dual-base-label quirk, Append's
  two-write sequence, StoreSupportsAtomicTx handoff branches). Composite
  single-commit ops: `CreateBinding` (displaced close + membership
  sub-writes via the `FabricWriter` callback, #3735), `RefreshBinding`,
  `AppendTranscript`, delivery upsert. The expiry cascade is the shared
  `expireBindingFunc`; the owner algebra/hydration gates/routing stayed
  in services untouched. `NewServicesWithBackend(backend, sessionStore)`
  admits the class store (lock pool per backend handle;
  `sharedBindingLockPoolForBackend` collapses bd backends to store-keyed
  pools). ALL constructor/pkg-func signatures preserved; ONE mechanical
  test edit (flaky stub's writer param type). Full suite + api +
  cmd/gc extmsg tests + fast-suite pre-push all green. Documented
  micro-divergences: store-failure wrap texts unified at op level;
  GetBinding parses last_touched_at unconditionally.

## What remains (P3+ per the design work plan)

- **P3 messaging, remaining**: (a) feat(classdb) extmsg typed tables —
  messagingdb migration Version 2, seven tables + partial UNIQUE
  constraints + meta JSON columns per the plan's Schema section;
  implements fabricBackend structurally (exported names in
  extmsg/backend.go are the contract); Import* primitives; dormant
  retention; both-backend conformance through the public
  extmsg.Services surface (bd leg `NewServicesWithSessionStore(mem,
  mem)`, sqlite leg `NewServicesWithBackend(store, mem)`); crash gate +
  census bump; (b) ONE wiring slice —
  ratchet flip + config acceptance test + `messaging.migrated` marker +
  routing at the construction roots (`newCityMailProvider`,
  `openCityMailProvider`, cmd_handoff.go:338's direct
  `beadmail.NewWithStores`, the nudge-mail sweep mail leg →
  `Provider.SweepReadMessages`, wisp GC leg → `PurgeReadMessages`, extmsg
  services, AND routed twins of the three extmsg pkg funcs for the
  session-repair call sites + the ReapStale* pair — see the wiring
  inventory in P3-EXTMSG-SEAM-PLAN.md) + seam-guard test + fail-closed
  erroring backend; the API
  needs no provider seam (it consumes state.MailProvider, which the
  controller builds routed); (c) migration — import open mail + extmsg
  actives (drop >30d unread, >TTL read), marker, residue, reaper.sh
  `mail_wisps` count + `issue_type='message'` filters, doctor/hook-claim
  raw-consumer touch-ups, and the store retention sweeper
  (read close→purge + unread TTL) on the controller.
- **P4 sessions+waits**: store + shadow-write gate + reconciler/doctor
  lockstep + `gc session show/prune` + orphan-sweep.sh rewrite.
- Also outstanding from the design: splittest topology port before any
  class flips by default (GA); storehealth `StorePath`/`WalkSize` extension
  to `.gc/store/*.db`; maintenance-loop `wal_checkpoint(TRUNCATE)`/`VACUUM`;
  P5 bd-surface work (gc bd write guard, generalized read federation);
  `gc doctor` migration-state surface (orders AND nudges routing/marker
  state — both trivial to surface now); the design's every-prompt drain
  soak + chaos "acked-write survival" gate beyond the existing crash test.
- Review follow-up for P1 orders: `ordersSQLiteRoutingActive`
  (cmd/gc/order_class_store.go:50) has the marker-stat conflation the
  nudges review fixed — any stat error (EACCES/EIO, not just ENOENT) reads
  as "not routed" and silently falls back to bd on a migrated city. Port
  the ENOENT-only check (needs the bool signature to grow an error).

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
