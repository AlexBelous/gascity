# Infra-class SQLite stores — implementation handoff

**Design (authoritative):** `engdocs/design/infra-class-sqlite-stores.md`
**Branch:** `feat/infra-class-sqlite-stores` (worktree `worktree-sqlit`), based on `origin/main` @ `5131e3b57`.

## P5 bd surface + cleanup — DONE (2026-07-22, five slices)

1. **`gc doctor` migration-state surface** (`feat(doctor)`):
   `infra-class-migration` check (cmd/gc/doctor_class_migration.go) reports
   backend/marker/routing (+sessions shadow flag) for all four classes in
   cutover order; advisory in every healthy shape, WARNING on
   marker-present+backend=bd (escape hatch active), blocking ERROR on an
   unstatable marker (routing fails closed). Registered after
   sessions-shadow; doctor_check_names.golden gained the name. The same
   slice ported the ENOENT-only discipline to
   `ordersSQLiteRoutingActive` — signature grew an error; the residue
   sweep SKIPS (never deletes) when routing state is unknowable.
2. **`gc bd` write guard** (`feat(bd)` — cmd/gc/cmd_bd_guard.go):
   `doBd` refuses mutations (update/close/reopen/delete +
   release-if-current) whose positional ids carry a reserved class prefix,
   and creates whose declared --type/--labels (or --wisp-type) classify
   off ClassWork via coordclass — message names the gc replacement.
   Static over args, fires before any store/subprocess work. Verified: no
   pack/script creates infra-typed beads via bd (design inventory holds).
3. **Generalized read federation** (`feat(bd)` — cmd/gc/cmd_bd_show_fed.go):
   `maybeRouteBdShowLocal` in doBd after config load. Reserved-prefix ids
   are served from their class store (absent file/row = bd's "no issue
   found"; store failure surfaces distinctly — 404-vs-error preserved);
   legacy ids probe the ROUTED classes in cutover order before the
   byte-identical bd passthrough (covers migrated gc-*/mc-* ids); gcg
   was fall-through until P6 landed the graph arm. Per-class renders:
   sessionsdb.Get verbatim, messagingdb.Get→mail-codec bead,
   ordersdb.Get→tracking-label bead, nudges
   FindRecordIncludingTerminal→shadow bead. NOTE: gcm ids minted by
   extmsg tables are NOT readable by messagingdb.Get (messages table
   only) — they render "no issue found"; acceptable (no consumer reads
   extmsg by id via bd), flagged here for honesty.
4. **Maintenance + storehealth** (`feat(classdb)`):
   core.DB.Checkpoint (wal_checkpoint(TRUNCATE); busy = skip, not error) /
   Vacuum / StartMaintenance (sweeper-scaffold loop, checkpoint every
   tick + slow VACUUM); per-store once-guarded StartMaintenance
   delegators; controller starts loops for ROUTED classes at boot
   (cmd/gc/class_store_maintenance.go, 15m/24h) next to the retention
   sweepers — unrouted cities pay nothing (no file even created).
   storehealth.ClassStoreDir + TotalSize (Dolt walk + .gc/store walk);
   gc status and /v0/status size consumers switched.
5. **Cleanup** (`refactor(cleanup)`): doltlite order-run cache DELETED
   (LastOrderRun/HasOpenOrderRun/loadOrderRuns + fields — zero callers in
   tree AND in all history, git log --all -G verified; mutator hook
   survives as resetReadCaches for the still-live session/ready caches).
   openMailTargetStore/tryOpenCityStore (the mail storeless-provider raw
   leg, P4's known residual) now routes through cliSessionStore with the
   no-refresh cfg loader (openCityMailProvider's exact shape).

**Design retirement list — verified LIVE, blocked on bd-leg removal**
(re-audited 2026-07-22; do NOT delete while unmigrated cities exist):
close-verify retries + close_reason constants (internal/orders
tracking_beads.go bd backend), UpdateMetadataInfo (production caller
build_desired_state.go:2892), doctor_backlog_depth
control-plane/notification predicates (report 0 on migrated cities;
retire when every city migrates), nudge two-tier file machinery (the
LIVE backend for unmigrated cities behind QueueForCity).

## P6 GRAPH class — COMPLETE (2026-07-23): all wiring + migration + ratchet flip landed

The five remaining slices landed after G1+G2:

- **G3 create-side dispatch**: beadPolicyStore carries cityPath
  (wrapStoreWithBeadPoliciesAt; six production wrap sites thread it; the
  cityPath-less form stays inert for tests). createTarget(ClassGraph) +
  graphApplierFor(ClassGraph) resolve the routed store — pours and wisp
  creates land in .gc/store/graph; fail-closed erroring target/applier
  on unresolvable marked cities.
- **G4 doBd mutation arm** (cmd_bd_graph_sqlite.go, ported from integ
  cmd_bd_infra_sqlite.go): close [--reason] + update --set-metadata/
  --status/--assignee on gcg ids apply in-process on routed cities; runs
  BEFORE the write guard in doBd; mixed id sets and unsupported flags
  error loudly; unrouted cities fall to the guard's refusal.
- **G5 hook claim + ready federation** (graph_hook_claim.go,
  graph_hook_ready.go): claim/continuation/stamp seams route gcg ids to
  the store's CAS Claim/Update in-process; the work-query runner unions
  the store's TierBoth ready rows into the shell's JSON candidate list
  (fail-loud; count forms pass through) — the split-branch gc-ready
  composite folded into the existing runner seam, no new subcommand.
- **G6 control dispatcher**: openControlStoreAtForCity returns the graph
  store on routed cities (control lane is graph-class wholesale);
  findBeadAcrossStores resolves gcg directly; the control-ready bd
  fallback runs in-process (tier-matched, convoy-excluded); the cache
  tier federates via the same open.
- **G7 migration + flip** (graph_class_migrate.go): boot template —
  reset -> open-bead import (both tiers, ids preserved, within-graph
  deps re-added) -> copy-verify -> atomic marker -> straggler
  merge-import -> residue import-then-sweep w/ 10m grace. Closed graph
  beads never cross. sqliteCapableBeadClasses[BeadClassGraph] flipped
  LAST; config accepts backend=sqlite; the dormant API arms are live.

**Pre-flip gates — ALL DONE (2026-07-23, follow-up session):**

1. **Pool-demand federation** (graph_scale_demand.go): every pool/named
   template gains a graph-store probe target in buildDesiredState's two
   demand passes (appendGraphScaleTargets); counted-bead dedup guards
   unions; routing failure = per-template partial, never silent zero.
   The controller demand pass is the authoritative counter; worker-side
   count shells remain bd-only, shadowed by it.
2. **ADR bench gates** (internal/beads/sqlite_store_bench_test.go; run
   `go test ./internal/beads/ -run '^$' -bench BenchmarkSQLiteStore
   -benchtime 2s`). Measured 2026-07-23, AMD EPYC 9654, 5k-row seed:
   PointGet 27.8µs (gate ≤1ms PASS), FilterList/label 4.45ms (≤10ms
   PASS), Write 640µs (≤5ms PASS), Claim 616µs (PASS).
   Ready(TierBoth, 3.3k open rows, NO limit) 48.4ms — a ~14.5µs/row
   bead_json decode slope, no N+1; the ≤10ms bar holds for ready sets
   ≤~600 rows, and production ready surfaces (post readyExcludeTypes,
   usually limit-bearing) are far smaller. Recorded as the slope to
   watch, not a failure.
3. **Topology invariants** (graph_topology_conformance_test.go): the
   split-branch 11-invariant suite re-expressed against this branch's
   seams — new pins for wisp-id claim routing, cross-store attach
   linkage, attach-block-at-ready, wake/ownership fast path, read-path
   tier consistency; the rest map to the per-slice tests named in the
   file header. LANDMINE #4 CLOSED as part of this:
   ensureFormulaCookAttachDep on a gcg root now stamps
   gc.attached_workflow_root metadata (beadmeta key added) instead of a
   cross-store dep bd would degrade to non-blocking, and the federated
   worker ready union withholds marked parents while the root is open
   (dangling marker fails LOUD).

Remaining before a production city flips graph: the operational mc soak
protocol only (sessions shadow soak + infra-class-migration doctor
watch), which needs the live city. INCIDENT NOTE 2026-07-23: the sqlit worktree directory +
worktree-sqlit branch ref were deleted EXTERNALLY mid-session; the tip
(b483e45c9) was recovered from dangling objects via git fsck and the
worktree recreated — if it happens again, fsck --lost-found first.

## P6 GRAPH class — G1+G2 (superseded by the section above)

Graph is the fifth and last infra class (after it + migration, bd/Dolt
holds only work beads). Authoritative design:
`git show 09830032e:engdocs/design/graph-store-backend-selection.md`
(the graph ADR) + the main design's "Relationship to the graph class"
section. Strategy ratified this session: PORT THE WIN3-PROVEN LINEAGE,
don't rebuild — `integ/cli-class-store-event-emission` @2c74f8747 and
`deploy/sqlite-b36-probe-attribution` carry the live deploy wiring
window 3 has been debugging on mc.

- **G1 DONE** (`feat(beads)` — the store): internal/beads/sqlite_store
  {,_storage,_claim,_graph_apply}.go + tests ported VERBATIM from integ
  @2c74f8747 (the recovered ga-aec8q store + graph extensions:
  ApplyGraphPlan/WithStorage, CreateWithStorage, CreateWithForeignID,
  CAS claim, main/wisp tier column, deps + Ready blocking subquery,
  retention sweeper). Two fixes vs the ported code: dep edges keyed one
  per (issue,depends_on) with type updatable (beadstest
  DepAddUpdatesType — deploy schema allowed contradictory duplicates),
  and per-conn PRAGMAs moved to the modernc `_pragma=` DSN form (the
  deploy `?_busy_timeout=` was silently ignored — the G0 finding; its
  read pool ran WITHOUT the busy timeout its comments assume).
  Conformance = this tree's beadstest suites (b36's shim rode the
  deploy-only coordtest pkg). ForeignIDCreator interface added
  (new file internal/beads/foreign_id_creator.go).
- **G2 DONE** (`feat(graph)` — routing, DARK): cmd/gc/
  graph_class_store.go — two-key activation (backend=sqlite +
  .gc/store/graph.migrated marker, ENOENT-only), store at
  .gc/store/graph/beads.sqlite (OpenSQLiteStore takes a dir) minting
  gcg via WithSQLiteStoreIDPrefix, process-shared handle, fail-closed
  resolveGraphStoreRouted wired into resolveGraphStore (returns the
  store ITSELF, never a wrapper — capability assertions survive).
  gc bd show federation serves gcg (reserved arm + legacy probe);
  doctor infra-class-migration reports all five classes.
  **sqliteCapableBeadClasses has NO graph entry yet — deliberately.**
  Routing cannot activate on any real city until the ratchet flips in
  the final wiring slice. Tests construct config.City directly.

**Remaining graph slices (in order; each has a proven source to port):**

1. **Create-side dispatch**: beadPolicyStore.createTarget(ClassGraph) +
   beadPolicyGraphStore.graphApplierFor(ClassGraph) route to the graph
   store on a routed city (bead_policy_store.go + class_store.go —
   both identity today, documented as THE seam). Molecule pours
   (ClassifyGraphPlan routes wholesale) then land in the graph store.
   Thread routing via construction (cityPath+cfg at policy-store build
   sites), NEVER by wrapping (the routed-wrapper hazard).
2. **doBd in-process mutation arm**: port cmd_bd_infra_sqlite.go from
   integ @2c74f8747 (maybeRouteBdInfraSqliteMutation — close +
   update --set-metadata/--status applied in-process because bd 1.1.0
   cannot read the store), adapted to gate on graphSQLiteRoutingActive
   instead of the infra-scope metadata.json probe. MUST reconcile with
   cmd_bd_guard.go: the arm intercepts gcg mutations BEFORE the guard
   (workers legitimately claim/update/close graph steps); the guard
   keeps refusing on UNROUTED cities. Also the read side: integ's
   execStoreTargetForBd routes reserved-prefix ids + runBdStoreBridge
   serves list/ready/dep-list in-process (bridge already in this tree).
3. **Ready/claim federation**: port claimableStore (composite work ∪
   graph Ready in canonical (priority,created_at,id) order, claim
   routed by reserved prefix) + gc ready + workquery.go re-route —
   split-branch commits 63235fe0a (#2a), 2ed2fc961 (#2b), #2c,
   c224a9792 (#13). Without this a flipped city's molecule steps are
   invisible to workers. Cross-store dep replacement (landmine #4,
   eae511422): gc.attached_workflow_root metadata linkage.
4. **Migration**: adapt cmd_migrate_infra_store.go /
   infra_store_migrate.go from feat/split-store-conformance (839
   lines; owner-gated stop-the-world, idempotent, no status file,
   copy-verify-delete, ids preserved via CreateWithForeignID) to
   graph-only + the marker write. Graph is deliberately NOT
   boot-auto-migrated (live molecules mid-flight — operator-gated,
   unlike the four ephemeral classes).
5. **Ratchet flip + events audit + API arms**: flip
   sqliteCapableBeadClasses[BeadClassGraph] LAST; the dormant API
   federation arms (handler_beads gcg arm, handler_convoys,
   handler_convoy_dispatch workflowStores) go live automatically;
   verify runBeadCloseAutoclose / bead.*-keyed watchers per the
   design's events note; splittest invariants NOW apply (the harness
   targets exactly this work/graph split — the P5 deferral unblocks).

## P5 residuals — deferred with evidence

- **splittest topology port**: the feat/split-store-conformance harness
  (internal/beads/splittest ~1.3k lines + cmd/gc
  split_topology_conformance_test.go 665 lines + split_topology_env_test
  fixture) is built for the work/INFRA-GRAPH split topology
  (config.InfraScopePrefix, ready/claim/residence-sweep federation
  invariants). Per-class stores are a different shape — sessions fails
  loud on Ready, nudges/orders/messaging aren't beads.Stores at all — so
  "adapt to per-class backends" is a design task (which of the 11
  invariants apply?), not a mechanical port. Its gate is GA-default
  flips; nothing defaults to sqlite on this branch, so not yet blocking.
- **Chaos gates beyond the crash tests** (every-prompt drain soak,
  at-most-one-extra-fire, acked-write survival multi-process) — same
  GA-gate bucket.
- **Final census** — re-measure per-class volumes on mc before freezing
  retention/index choices (operational, needs a live city).

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

- **Slice 5 DONE** (`feat(classdb)` 7e6882577 — extmsg typed tables):
  messagingdb migration Version 2 (same db, shared gcm mint/id_seq).
  Seven tables; partial UNIQUE indexes carry the invariants
  (active-binding violation → ErrBindingConflict); AppendTranscript is
  ONE tx (bd crash window closed; crash gate proves allocator+entry
  commit as a unit); participant retained-label ≡ pending_cleanup column
  (ParticipantsBySession matches session_id OR owed cleanup;
  DropParticipantSessionLabel = documented no-op); meta JSON column with
  bd MERGE semantics (mergeMetaLocked). Import* primitives (OR IGNORE
  resume; ended-binding import carries generation ceiling) +
  SweepExtmsgRetention (spares max-(generation,id) binding row per conv)
  + PruneTranscripts (earliest_available_sequence advances) — ALL
  DORMANT. Conformance through public extmsg.Services over both
  backends; repair-op sqlite semantics pinned at backend level in
  extmsgdb_test.go (service-level repair conformance lands with the
  wiring slice's routed twins). Census 534/165. NOTE: shared-host
  golangci-lint was clobbered mid-session (2.12.0/go1.25); restored
  pinned 2.9.0 built via `GOTOOLCHAIN=go1.26.5 go install` into
  ~/go/bin.

- **Slices 6–8 DONE — P3 messaging COMPLETE** (addffe047 + c4d94c16e +
  91bd32680): (6) dark plumbing — messagingdb/routing.go (marker-FIRST,
  ENOENT-only, config cache, RoutedStoreFor fail-closed; one shared
  handle per db = one extmsg lock pool per city), config ratchet +
  acceptance test, extmsg *WithBackend repair twins (internals refactored
  onto backend+locks; bd forms delegate byte-identically), beadmail
  NewCachedWithBackend + Provider.CountReadMessages. (7) the wiring
  flip — cmd/gc/messaging_class_store.go seam routes EVERY construction
  root: controller boot/reload mail+extmsg (reload routing failure swaps
  services to nil — fail closed), openCityMailProvider, gc handoff,
  nudge-mail sweep mail leg (mixed nudges-bd/messaging-routed budget
  handled), wisp GC arm (runWispGCRouted starves bd arm), extmsg
  reapers, session-repair cascades via a pointer-identity store→city
  registry (registered at controller boot/reload + CLI opener —
  closeBead is 8 chains deep, threading was worse), internal/api
  session-continuity repair; seam guard
  TestMessagingSeamIsTheOnlyConstructionPoint. (8) the migration —
  cmd/gc/messaging_class_migrate.go: reset→import(open mail minus 30d
  unread/TTL-read drops + extmsg actives + per-conv generation-ceiling
  ended binding)→copy-verify→atomic marker→straggler; residue sweep
  import-then-sweep (closed ∪ owned ∪ open-past-10m-grace);
  StartRetentionSweeper (unread 30d + extmsg 30d w/ ceiling spare +
  transcript prune 10k default). Upgrade-flow tests pin: fresh-flip
  idempotence, bd-truth import (gen ceiling → post-cutover gen 3;
  allocator continues), NO-RESURRECTION retry, straggler
  import-then-clear, abort-before-marker, age-drop matrix. reaper.sh
  needs NO change (mail refs are observability counts); doctor bucket
  retirement stays P5.

## P4 sessions+waits — COMPLETE (all four slices)

Slice 4 (2026-07-22, e634318bd + ad4c45c1f + ea2851e59) finished the class:

- **Routing flip** (e634318bd): ratchet `sqliteCapableBeadClasses` +=
  sessions (+acceptance test); `sessionsdb/routing.go` (marker-FIRST
  `.gc/store/sessions.migrated`, ENOENT-only stat, config self-load w/
  city.toml mtime+size cache, rollback = marker + bd knob);
  `resolveSessionStore` routes at THE one seam every root already uses —
  marked+configured city gets the process-shared class store; a marked
  city whose routing/store fails gets `NewUnavailableStore` (every op
  errors — fail CLOSED, never bd fallback). The five seam-plan bypass
  gaps closed identity-pre-flip: doctor_session_model session legs
  routed (work legs stay raw — two-store split in
  loadSessionModelDoctorBeads), cmd_mail identity/recipient/target
  funnels route via cliSessionStore (the storeless-provider raw leg
  through openMailTargetStore stays on the mail DI pass — KNOWN
  RESIDUAL), api handler_beads/handler_extmsg/handler_mail/
  handler_status/agent-output switched to the SessionsBeadStore
  accessor, and the retired-session repair splits work vs waits legs
  explicitly. Retention primitives: `SweepClosedBefore` +
  `StartRetentionSweeper` (7d TTL, 15m cadence).
- **Migration** (ad4c45c1f): `cmd/gc/session_class_migrate.go` at boot
  next to the P1–P3 ensure* calls — reset → FULL import (open rows
  ALWAYS — the restart projection; closed rows ≤7d so recent closed
  reads and closed-wait retries survive; work beads never cross via
  Classify) → copy-verify → atomic marker → straggler;
  abort-before-marker on ANY failure; `sweepLegacySessionResidue`
  (import-then-sweep, 10m open grace, ownership-unprovable rows spared);
  `startSessionsRetentionSweeper` (replaces reaper.sh's raw session SQL,
  which now no-ops harmlessly — reaper.sh itself needs NO change, same
  as P3). Upgrade tests: fresh-flip idempotence, bd-truth import w/
  age-drop matrix, NO-RESURRECTION retry, abort-before-marker,
  straggler import-then-clear + spare arm.
- **gc session show + orphan-sweep rewrite** (ea2851e59): NEW
  `gc session show <id-or-alias> [--json]` reads through the routed
  store (json shape = the probe's bead fields; missing session exits
  non-zero); orphan-sweep.sh's session liveness probe switched from
  `gc bd show` to it (work-bead recheck stays bd show); the
  maintenance_scripts_test stubs moved their session-probe arms to the
  session) case; guard list gained cmd_session_show.go.
- **Operational protocol**: run the shadow soak (shadow=true, watch
  `gc doctor` sessions-shadow) clean on mc BEFORE flipping
  backend="sqlite" anywhere. Shadow and backend=sqlite are mutually
  exclusive by config validation — drop the shadow knob when flipping.
- Known residuals for P5: mail storeless-provider raw leg; `gc doctor`
  migration-state surface (all four classes' routing/marker state);
  create-side `beadPolicyStore.createTarget` still identity (a stray
  `gc bd create` of a session-typed bead post-flip lands in bd — the P5
  write guard is the designed fix).

## P4 sessions+waits — slices 1–3 (seam plan + store + shadow gate)

- **Slice 1 DONE** (`docs(plans)` e77e493fb): P4-SESSIONS-SEAM-PLAN.md —
  evidence-grade inventory (full persistence-edge read + three repo-wide
  sweeps) + THE structural ratification: the sessions backend seam is the
  audited `beads.Store` subset ITSELF (codec already confined to
  internal/session; `SetFingerprint` hashes ALL metadata keys incl.
  non-codec ones, so the store must round-trip an open vocabulary;
  Manager/api/worker all thread beads.Store handles; `resolveSessionStore`
  + guard tests already exist). Records the ~18 hot keys with citations,
  the five routing bypass gaps (doctor_session_model raw open, cmd_mail
  mailbox lookups, api session.NewStore(raw) sites, messaging-seam
  session legs, agent-output managers), that `gc session show` does NOT
  exist (orphan-sweep.sh uses `gc bd show`), and that NO Go delete path
  exists for closed sessions (only reaper.sh raw SQL).
- **Slice 2 DONE** (`feat(classdb)` fc08566b1): internal/classdb/sessions
  (package sessionsdb) — two tables (sessions, waits; dispatch invariant:
  waits table holds exactly IsWaitBeadType rows, Update Type-crossing
  reclassifies) of bead-shaped rows; meta JSON column AUTHORITATIVE for
  the full metadata map (empty-string values kept PRESENT — fingerprint/
  empty-clear fidelity), hot columns are derived mirrors recomputed in
  the same tx via the single writeRow chokepoint; `gcs-<n>` mint in-tx,
  explicit ids honored (bd parity — memstore does NOT honor them, a
  known cross-backend delta); List = SQL narrowing + beads.ApplyListQuery
  (canonical semantics by construction); Ready/deps/Tx/tiers/Priority/
  ParentID fail LOUD (ErrUnsupported); ImportBead (verbatim OR IGNORE) +
  DeleteAllRows = the migration primitives. DEVIATION: session_circuit
  sidecar folded into meta (recorded). Conformance: one behavioral suite
  over memstore AND sessionsdb through the PUBLIC session.Store surface
  (union traps, fingerprint-over-all-metadata, wait lifecycle incl.
  retry-clone/reassign/cancel-collect, WakeSession, probe, close/reopen,
  Manager-shape creates). Crash gate (integration): restart-projection
  survival under SIGKILL. Census 535/166.
- **Slice 3 DONE** (`feat(sessions)` 51375b2a3 — shadow-write gate):
  `[beads.classes.sessions] shadow = true` (validated sessions-only,
  rejected with backend=sqlite; `BeadsConfig.ClassShadow`).
  `resolveSessionStore` wraps the resolved bd store in `sessionsdb.Shadow`
  (cmd/gc/session_class_store.go): bd authoritative for ALL reads/writes;
  Create tees the primary's ECHO verbatim via ImportBead (bd ids
  preserved — the ids the flip migration keeps); id-keyed ops replay when
  the shadow holds the row, else on-miss import of post-op state; only
  Classify==ClassSessions rows cross; tee failures log, never fail the
  primary (fail-OPEN — opposite of the slice-4 flip). Identity
  discipline: wrapper cached per (base, city) so resolves return ONE
  value; `storeIdentityKey` + `closeBeadStoreHandle` unwrap
  `ShadowPrimary()` (the messaging repair-city registry keeps working);
  CachedList forwarded when the primary has it (dashboard read-model tier
  survives the soak). Boot: `seedSessionsShadowAtBoot` (reset +
  re-import bd truth, city_runtime.go after the messaging block).
  `gc doctor` check `sessions-shadow`: DiffAgainstPrimary over OPEN rows,
  diffed TWICE with intersection to filter in-flight-write races;
  divergence warns "do not flip the backend" with per-row Details.

### P4 slice 4 — REMAINS (wiring flip + migration; the last slice)

Per the seam plan (P4-SESSIONS-SEAM-PLAN.md, read it first):
ratchet flip (`sqliteCapableBeadClasses` += sessions) + config acceptance
test; routing (marker-FIRST `.gc/store/sessions.migrated`, ENOENT-only,
config cache, fail-CLOSED at every root — plug into `resolveSessionStore`
next to the shadow arm; shadow and routing are mutually exclusive by
config validation); close the five bypass gaps; `gc session show [--json]`
(NEW — must expose the fields orphan-sweep.sh's jq probe reads:
issue_type/status/metadata.state/closed + id/session_name/alias/
agent_name); orphan-sweep.sh rewrite + embed-guard needles; closed-session
purge TTL (7d default) via core.StartSweeper + `gc session prune`
extension; reaper.sh session-SQL leg replacement;
`ensureSessionsClassMigrated` (reset→FULL import of open session beads +
open/recent waits with ids preserved→copy-verify→atomic marker→straggler
import-then-sweep; abort-before-marker on ANY failure); doctor lockstep
(doctor_session_model routed + migration-state surface); upgrade-flow
tests (fresh-flip idempotence, bd-truth import, no-resurrection retry,
straggler, abort-before-marker, closed-TTL drop matrix). The operational
protocol: mc runs the shadow soak (knob on, watch `gc doctor`
sessions-shadow) BEFORE any city flips backend=sqlite.

## What remains (P4+ per the design work plan)

- **P4 sessions+waits**: slices 1–3 DONE (above); slice 4 (flip +
  migration) remains — see the P4 section.
- (superseded — P3 wiring/migration notes below are DONE, kept for
  reference) (a) ONE wiring slice —
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
  controller builds routed); (b) migration — import open mail + extmsg
  actives (drop >30d unread, >TTL read), marker, residue, reaper.sh
  `mail_wisps` count + `issue_type='message'` filters, doctor/hook-claim
  raw-consumer touch-ups, and the store retention sweeper
  (read close→purge + unread TTL) on the controller.
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
