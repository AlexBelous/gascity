# Graph-read gap analysis — win3 deploy lineage vs feat/infra-class-sqlite-stores

Date: 2026-07-24. Method: 7 parallel finder agents swept the cross-store
graph READ surfaces of `integ/cli-class-store-event-emission` @2c74f8747
(INTEG) and `deploy/sqlite-b36-probe-attribution` (B36) against this
branch's seams; every claimed gap was then adversarially verified by an
independent refuter (39 confirmed of 43 claimed; 4 refuted; 22 mechanisms
confirmed covered). Scope: reads and read-driven decisions that gate
destructive actions, on a graph-routed local city.

Severity: critical = destructive action from a false-absence read;
high = graph work invisible/wedged; medium = degraded views/UX;
low = edge/cosmetic.

## Confirmed gaps (deduplicated cross-dimension; citations in each entry)

### G00 [CRITICAL] Reconciler session-liveness gates (close/drain/recycle) consult the graph store before destructive action

- dimension: mol-workflow-liveness
- deploy evidence:
  B36 deploy/sqlite-b36-probe-
  attribution:cmd/gc/session_reconciler.go:2731-2900 —
  sessionHasOpenAssignedWorkForReachableStore takes a graph-only branch via
  beads.GraphOnlyListFor when GraphIDPrefix()!="" (line ~2748:
  graphOnlyHasAssignedWork over ListGraphOnly{Assignees}), and
  sessionHasAwakeAssignedWorkForReachableStore routes through
  GraphOnlyReadyFor/graphOnlyHasAwakeAssignedWork (~2778-2900) with an
  explicit fail-safe comment: skipping the graph List 'would make a worker
  mid-step look idle and drain it mid-step'. Capabilities defined in B36-only
  internal/beads/graph_only_list.go and ready_graph_only.go, forwarded through
  the policy wrapper at cmd/gc/bead_policy_store.go:106-149.
- local evidence:
  Local cmd/gc/session_reconciler.go:3855-3905:
  sessionHasOpenAssignedWorkForReachableStore /
  sessionHasAwakeAssignedWorkForReachableStore iterate
  reachableStoresForSessionInfo -> workAssignmentStores
  (cmd/gc/session_beads.go:946-966) = city + rig WORK stores only; no graph
  arm. Grep of all routedGraphStoreFor/graphSQLiteRoutingActive call sites
  (class_store.go, cmd_convoy_dispatch.go, cmd_bd_show_fed.go,
  cmd_bd_graph_sqlite.go, dispatch_control_ready.go, graph_scale_demand.go,
  graph_hook_ready.go, graph_hook_claim.go) shows zero
  reconciler/session_beads consumers; grep 'gcg|GraphIDPrefix|graphResident'
  in session_reconciler.go and assigned_work_scope.go returns nothing.
  Meanwhile local workers DO claim gcg beads with assignee in the graph store
  (cmd/gc/graph_hook_claim.go:40-45 st.Claim). Destructive consumer: drain-ack
  finalize at session_reconciler.go:541-560 closes the session bead
  ('drained') when hasAssignedWork=false; progress-stall recycle at :2434 and
  drain/wake gates at :1933/:2268 read the same blind probes.
- failure scenario:
  Graph-routed local city (backend=sqlite + .gc/store/graph.migrated): a pool
  worker claims a gcg molecule step via graphRoutedHookClaimOps (in_progress,
  assignee=session identity, stored only in .gc/store/graph/beads.sqlite).
  When the session looks idle between tool calls, the reconciler's awake gate
  (sessionHasAwakeAssignedWorkForReachableStore, graph-blind) finds no
  assigned work and initiates drain; on ack, drain-ack finalize
  (sessionHasOpenAssignedWorkForReachableStore, also graph-blind) closes the
  session bead as "drained". releaseWorkFromClosedSessionBead and
  releaseOrphanedPoolAssignments list only city+rig work stores, so the
  in_progress gcg step remains assigned to the closed session forever — the
  molecule wedges with no automatic recovery. The same blindness also prevents
  waking a sleeping worker that holds ready gcg work, and lets progress-stall
  recycle destroy a session mid-step.
- fix sketch:
  In cmd/gc/session_reconciler.go, add a graph arm to both probes: when
  routedGraphStoreFor(cityPath, cfg) reports routed, additionally query the
  embedded graph store — st.List(ListQuery{Assignees: identifiers, TierMode:
  TierBoth}) filtered to open/in_progress for the open-work probe, and
  in_progress plus st.Ready(ReadyQuery{Assignee: id}) intersection for the
  awake probe — OR-ing the result into the existing work-store fan-out. Unlike
  B36 (which replaces the work probe because its workers execute graph nodes
  exclusively), the local ready-union runner feeds workers BOTH classes, so
  the graph check must be an additive union, not a substitute. Fail closed: on
  graph-store open/List error, treat as hasAssignedWork=true (matching the
  existing assignedErr handling at :541). Companion fix (separate finding):
  thread the routed graph store into releaseWorkFromClosedSessionBead /
  releaseOrphanedPoolAssignments so any already-stranded gcg steps are
  released.

### G01 [CRITICAL] Graph-aware session work guards + reconciler assigned-work capture (close/recycle/drain gates and orphan release see graph-resident steps)

- dimension: claimable-crashrecovery
- deploy evidence:
  B36 (deploy/sqlite-b36-probe-attribution) session_reconciler.go:2723-2793:
  graph-only close/recycle scope —
  graphOnlyHasAssignedWork/graphOnlyHasAwakeAssignedWork consult the graph
  backend before approving session close, fail-CLOSED when the graph store is
  degraded ('the graph-only branch there would approve closing a session that
  still has [graph work]'); assigned_work_scope.go:68
  remapGraphResidentAssignedWorkStoreRefs + build_desired_state.go:674-682
  (capture reads gcg beads from the city graph store, commit 186ee6a93 'retag
  graph-resident assigned work to its routed rig');
  pool_session_name.go:85-200 gates gcg orphan release on the graph leg's own
  partiality. INTEG (2c74f8747) achieves the same by co-residence: graph steps
  live in the sessions/infra store (claimable_store.go:14-16), which is the
  leading 'city' store buildDesiredState scans (city_runtime.go:3233-3244;
  build_desired_state.go:525-531 'the sessions/infra store, where routed graph
  wisps ALWAYS live'), so collectAssignedWorkBeadsWithStores' in_progress/open
  passes (build_desired_state.go:1131-1160) and every sessionHas*AssignedWork
  guard see graph steps.
- local evidence:
  Local capture fan-out is sessions store + rig work stores only:
  cmd/gc/build_desired_state.go:617,1106 → coordClassStoreCandidates
  (cmd/gc/session_beads.go:922-937, no graph arm); grep -i graph over
  cmd/gc/session_reconciler.go, session_beads.go, session_work_guard.go
  returns ZERO hits; grep
  graphOnlyHasAssignedWork|GraphOnlyReadyFor|remapGraphResident across cmd/gc
  + internal/beads returns 0. sessionHasOpenAssignedWorkForConfig
  (session_reconciler.go:3836, 4476-4487) fans out
  workAssignmentStores/rigStores only, and gates
  closeSessionBeadIfRuntimeStoppedAndUnassigned (session_beads.go:2843-2865).
  releaseWorkFromClosedSessionBead (session_beads.go:3047) and
  unclaim/reassign scans (session_beads.go:948, 999-1160) likewise never touch
  the graph store. Only the READY demand probes were federated
  (graph_scale_demand.go).
- failure scenario:
  Graph-routed local city (config [beads.classes.graph] backend="sqlite" —
  accepted by validation since sqliteCapableBeadClasses lists graph, marker
  auto-stamped at boot by ensureGraphClassMigrated): worker-3 claims gcg step
  X via graphRoutedHookClaimOps (in_progress, assignee=worker-3, stored ONLY
  in .gc/store/graph/beads.sqlite after the residue sweep removed work-store
  copies), with no other assigned work. (a) Reconciler tick:
  sessionHasOpenAssignedWorkForReachableStore /
  sessionHasAwakeAssignedWorkForReachableStore (session_reconciler.go:541,
  587, 1933, 2268) scan only city+rig work stores → report no work → drain
  proceeds / close gate approves, and collectAssignedWorkBeadsWithStores
  contributes no resume-tier demand for the session, so pool scale-down drains
  the worker mid-step (its only demand contribution — the ready probe —
  vanished when the step was claimed). (b) Crash leg (unconditional): if
  worker-3's session dies for any reason, releaseWorkFromClosedSessionBead
  sweeps only the work store and releaseOrphanedPoolAssignments only sees the
  graph-blind AssignedWorkBeads, so gcg step X stays in_progress with a dead
  assignee forever; graphRoutedHookClaimOps refuses re-claim of an in_progress
  bead, dependents stay blocked, and the workflow stalls permanently and
  silently. New open steps still respawn workers (appendGraphScaleTargets),
  producing a spawn/drain treadmill around the stalled molecule rather than
  recovery.
- fix sketch:
  Minimal local fix, mirroring the existing appendGraphScaleTargets seam
  pattern: (1) Capture — in collectAssignedWorkBeadsWithStores
  (cmd/gc/build_desired_state.go:1090), append one extra classStoreCandidate
  {store: routedGraphStoreFor(cityPath,cfg), ref: "graph"} when
  graphSQLiteRoutingActive, so AssignedWorkBeads/Stores/StoreRefs include
  in_progress/open-assigned gcg beads; treat a graph open failure as
  storePartial (fail-closed, suppressing drain/orphan decisions that tick,
  matching B36's degraded-mode discipline). Ensure openSessionOwnsWork/store-
  ref matching accepts the "graph" ref for city-scoped sessions (B36's
  remapGraphResidentAssignedWorkStoreRefs is the reference). (2) Guards — in
  reachableStoresForSession/Info (session_reconciler.go:3911+) and
  workAssignmentStores callers, append the routed graph store to the fan-out
  when routing is active (the simple union is the fail-safe minimal form; the
  fuller B36 graph-only execution-scope branch via GraphOnlyListFor can be
  ported later if worker execution becomes graph-only). (3) Release — in
  releaseWorkFromClosedSessionBead (session_beads.go:3047) and the
  unclaim/reassign sweeps, also sweep the routed graph store for the session's
  assignee identities, using the in-process SQLiteStore (Update/reopen) rather
  than bd, per the graph_hook_claim.go pattern. Add a reconciler-level test:
  routed city, claimed gcg step, dead session → step is reopened and
  drain/close gates report assigned work while the session lives.

### G02 [CRITICAL] Session has-assigned-work liveness guards fan out over work-class stores only (no graph arm) — destructive session close/recycle on false absence

- dimension: sling-convoy-adoptpr
- deploy evidence:
  B36 b233fec1b 'complete the Router as the controller store (B1b+B2+B3)': on
  deploy the city store IS the coord Router whose List/queries include the
  ClassGraph leg, so every assigned-work probe sees gcg beads (confirmed by
  B36 cmd/gc/assigned_work_scope.go:12-16 'these beads physically live in the
  city Router's ClassGraph leg'). On top of that B36 adds
  openSessionReachableStoreRef/cross-store ownership wiring
  (assigned_work_scope.go:120-141 @deploy/sqlite-b36-probe-attribution) so the
  release path doesn't strand a live holder's routed work (#3453).
- local evidence:
  cmd/gc/session_beads.go:947-966 workAssignmentStores = city + rig work
  stores only; cmd/gc/session_reconciler.go:3909-3950
  reachableStoresForSession/reachableStoresForSessionInfo ->
  workAssignmentStores, no graph arm; cmd/gc/session_work_guard.go:58-84
  closeSessionInfoIfUnassigned gates gc_swept close on that probe;
  cmd/gc/session_beads.go:996-1010 unclaimWorkAssignedToRetiredSessionBead
  iterates the same stores. grep 'routedGraphStoreFor' across cmd/gc shows
  only 8 seams (hook ready/claim, scale demand, control dispatch, class_store
  create arms, control-ready fallback) — none in the session-guard/unclaim
  fan-outs.
- failure scenario:
  Graph-routed local city (backend=sqlite + .gc/store/graph.migrated): a pool
  worker claims gcg-wisp-abc; graphRoutedHookClaimOps writes the assignee only
  into the embedded graph store — no work store ever sees it. (a) Live-session
  destruction: the drain-initiation gate (~session_reconciler.go:754) and
  drain-ack finalize (541-547) probe
  sessionHasOpenAssignedWorkForReachableStore -> reachableStoresForSessionInfo
  -> workAssignmentStores (city+rigs only), find nothing, and drain+close the
  worker as "drained" between turns while it still holds an open gcg step; the
  progress-stall recycle gate (2434,
  sessionHasInProgressAssignedWorkForConfig) likewise sees holdsClaim=false
  and can recycle a worker mid-step on an in-progress gcg bead. (b) Permanent
  strand: GCSweepSessionBeads/closeSessionInfoIfUnassigned closes the session
  bead ("gc_swept"), and unclaimWorkAssignedToRetiredSessionBead iterates the
  same work-store fan-out, so the gcg claim is never released — the step stays
  assigned to a dead session, invisible to Tier-1 work queries, and the
  workflow wedges permanently. Precision note vs the original finding: the
  orphan-close arm at 2118 only fires when the runtime is already confirmed
  dead (fail-closed on liveness error), so that specific arm strands rather
  than kills; the mid-step destruction vectors are drain-init/ack and
  progress-stall recycle. Deploy is immune because the city store IS the
  coordrouter Router whose federated ListByAssignee/List include the
  ClassGraph leg (b233fec1b) plus the graph-resident storeRef retag
  (186ee6a93).
- fix sketch:
  Add a graph arm to the shared assigned-work fan-outs instead of patching
  each gate. (1) Introduce a helper, e.g. graphAssignmentStoreFor(cityPath,
  cfg) wrapping routedGraphStoreFor, and thread (cityPath, cfg) into the probe
  family: append the routed graph store in
  reachableStoresForSession/reachableStoresForSessionInfo (all return branches
  — graph-class work is claimable by any session via the hook claim arm, so
  include it even for rig-bound sessions), and in
  sessionHasAssignedWorkInStoresForStatuses (covers
  ForConfig/ForConfigInfo/InProgress and thus close gates, drain gates, and
  progress-stall suppression). (2) Add the graph store to
  unclaimWorkAssignedToRetiredSessionBead's store loop so retirement releases
  gcg claims (ReleaseWorkBead against the graph store; reuse the storeIndex
  dedup key). (3) Same append for the awake variant
  sessionHasAwakeAssignedWorkForReachableStore (ready-filter runs per-store,
  so no extra logic). (4) pool_session_name.go GCSweep path passes nil cfg —
  plumb cityPath/cfg through so closeSessionInfoIfUnassigned's probe can
  resolve the graph store. Tests: seed an assigned open/in_progress gcg bead
  in the routed graph store only, assert
  sessionHasOpenAssignedWorkForConfigInfo/ForReachableStore return true,
  GCSweep does not close, and retirement unclaim clears the graph-store
  assignee.

### G03 [CRITICAL] Graph-resident assigned-work reads for the reconciler frame (drain protection, orphan release, storeRef remap)

- dimension: dispatch-doctor-status
- deploy evidence:
  B36 (deploy/sqlite-b36-probe-attribution = 783407a975): every store open is
  wrapped policy(Router(work+graph)) — cmd/gc/api_state.go:246
  routedPolicyStore; internal/coordrouter/router_federation.go:57-206
  federates Get/List/ListOpen/Children/ListByLabel/ListByAssignee/ListByMetada
  ta/Ready/DepList across the graph backend, so collectAssignedWorkBeads'
  List(in_progress)/List(open) surfaces gcg beads. On top of that B36 adds
  cmd/gc/assigned_work_scope.go:30/68 graphResidentAssignedWorkStoreRef +
  remapGraphResidentAssignedWorkStoreRefs (called at
  cmd/gc/build_desired_state.go:682) retagging graph-resident beads to their
  owning rig so rig-scoped gates reach them, and
  cmd/gc/pool_session_name.go:85-175 graph-aware orphan release (graphPrefix
  physical-owner remap, per-leg partiality so a flaky rig Dolt leg does not
  suppress release of gcg orphans). B36's lazyGraphStore doc (api_state.go
  ~345-355) states the exact hazard: an empty graph read 'would look like no
  assigned work and recycle live sessions'.
- local evidence:
  cmd/gc/build_desired_state.go:1090-1167 collectAssignedWorkBeadsWithStores
  iterates coordClassStoreCandidates (cmd/gc/session_beads.go:922 = city store
  + rig stores ONLY; no graph-store candidate). The only graph arm in
  build_desired_state.go is appendGraphScaleTargets (lines 676/728,
  cmd/gc/graph_scale_demand.go) which probes READY demand only, never
  in_progress/assigned beads. grep of
  build_desired_state.go/assigned_work_scope.go/pool_session_name.go for
  routedGraphStoreFor/remapGraphResident/graphPrefix returns nothing; local
  assigned_work_scope.go (functions listed at lines 12-275) has no graph-
  resident remap; cmd/gc/pool_session_name.go:118-200
  releaseOrphanedPoolAssignments only reaches beads the (graph-blind)
  collection produced.
- failure scenario:
  On a graph-routed city ([beads.classes.graph] backend=sqlite +
  .gc/store/graph.migrated), a pool worker claims a gcg step:
  graphRoutedHookClaimOps writes assignee+in_progress directly into
  .gc/store/graph/beads.sqlite. On the next controller tick,
  collectAssignedWorkBeadsWithStores lists in_progress/open only across
  coordClassStoreCandidates (city + rig stores), so the gcg bead is invisible
  and the read is a clean zero (not partial — drain suppression never
  triggers). The session drops out of the resume tier and poolDesired; the
  reconciler's orphaned-drain branch re-checks assigned work via the equally
  graph-blind sessionHasOpenAssignedWorkForConfigInfo, finds none, and begins
  a drain that Ctrl-Cs the running agent mid-step; the drain-ack cancel check
  (sessionHasAwakeAssignedWorkForReachableStore, city+rig fan-out only) cannot
  rescue it, and the session is stopped. The now-orphaned in_progress gcg bead
  is invisible to releaseOrphanedPoolAssignments (it only iterates the graph-
  blind collection), is not Ready (so appendGraphScaleTargets generates no
  demand), and formula retry never fires (the step is not failed) — so it
  stays assigned to the dead session forever and the workflow wedges
  permanently. The same permanent wedge occurs for ANY session death (crash,
  host reboot) while holding a gcg step, independent of the drain path.
- fix sketch:
  Port the B36 reconciler-frame graph coverage onto the local seam
  architecture (no router needed; the graph store is an in-process
  beads.Store): (1) In collectAssignedWorkBeadsWithStores, append a graph
  classStoreCandidate when routedGraphStoreFor(cityPath, cfg) is routed — ref
  "" so it gates with the city leg — and, critically, treat a routing/read
  error as partial=true so an unreadable graph store suppresses drain instead
  of reading as no-work (B36's lazyGraphStore hazard). cityPath/cfg must be
  threaded into the collection (or the candidate built at the
  buildDesiredStateWithSessionBeads call site at build_desired_state.go:617).
  (2) Port B36's assigned_work_scope.go graphResidentAssignedWorkStoreRef +
  remapGraphResidentAssignedWorkStoreRefs (called after collection, as at B36
  build_desired_state.go:682) so rig-scoped pool-demand/wake filters reach
  graph-resident beads. (3) Port the graphPrefix arm of B36
  pool_session_name.go into releaseOrphanedPoolAssignments: gate gcg- beads on
  the city/graph leg's partiality key "" and validate/release against the
  owning graph store (the index-aligned assignedWorkStores already carry it
  once collection includes the graph candidate). (4) Add the routed graph
  store to workAssignmentStores/reachableStoresForSessionInfo so the pre-drain
  check, drain-ack cancel check, closeSessionBeadIfReachableStoreUnassigned,
  and unclaimWorkAssignedToRetiredSessionBead see graph-resident assignments.
  Guard with a test that claims a gcg step in the graph store and asserts (a)
  the session survives the reconcile tick and (b) after the session dies the
  bead is reopened by orphan release.

### G04 [CRITICAL] Reconciler drain/close/awake assigned-work probes reach the graph store

- dimension: wrapper-capability-reads
- deploy evidence:
  INTEG 2c74f8747:cmd/gc/session_reconciler.go:3906-3970 —
  reachableStoresForSession/-Info append the infra (graph-carrying) store for
  rig-bound sessions ('Without this leg the *ForReachableStore drain/awake
  probes cannot see a claimed wisp and the drain just moves post-claim —
  spawn/drain treadmill, post-claim half'); on a split city the reconciler's
  primary store IS the sessions/infra store so the cross-store fan-out always
  includes graph. B36 deploy/sqlite-b36-probe-
  attribution:cmd/gc/session_reconciler.go:2722-2905 —
  sessionHasOpenAssignedWorkForReachableStore /
  sessionHasAwakeAssignedWorkForReachableStore take the graph-only path via
  beads.GraphOnlyListFor/GraphOnlyReadyFor (internal/beads/graph_only_list.go,
  ready_graph_only.go) with an explicit livelock rationale and a fail-safe
  TierBoth in-progress fallback.
- local evidence:
  cmd/gc/session_reconciler.go:3901-3950 reachableStoresForSession/-Info fan
  out only via workAssignmentStores (cmd/gc/session_beads.go:946-966 = city
  work store + rig stores); grep 'graph|Graph' over session_reconciler.go,
  session_beads.go, work_assignment.go returns zero hits. Destructive
  consumers: session_reconciler.go:587 (drain-ack close gate), :3631 (pool-
  slot freeable gate), :1933/:2268 (awake gates), :2434 (progress-stall
  recycle), :3378 vicinity (worktree prune path). Local workers DO claim gcg
  beads with the session identity in the graph store only
  (cmd/gc/graph_hook_claim.go:40-45 st.Claim).
- failure scenario:
  On a graph-routed local city ([beads.classes.graph] backend=sqlite +
  .gc/store/graph.migrated), a pool or rig-bound worker claims gcg step X:
  graphRoutedHookClaimOps.Claim sets assignee=session-identity in the embedded
  graph store ONLY. Any reconciler decision then probes
  sessionHasOpenAssignedWorkForReachableStore /
  sessionHasAwakeAssignedWorkForReachableStore, whose store set is
  workAssignmentStores(primary, rigStores) or [rigStore] — never the graph
  store — and gets a definite (false, nil), not an error (so no fail-safe
  fires). Consequences: (a) drain-ack finalize closes the session bead
  "drained" while X is in_progress; (b) the pool-slot freeable gate frees the
  slot mid-step; (c) awake gates let the session sleep / cancel no drain
  despite in-progress X; (d) progress-stall recycle destroys the session (and
  worktree-prune vicinity). Post-destruction there is NO self-heal:
  collectAssignedWorkBeadsWithStores iterates coordClassStoreCandidates (also
  graph-blind), so in-progress gcg work generates zero wake demand and the
  orphan-release pass never sees X — the assignee stays a dead session forever
  and the molecule stalls permanently. Sub-case: on a city that also routes
  sessions, the primary probe store is the sessions class store (whose List
  returns empty for work-assignee queries without error), so even the primary-
  store leg contributes nothing.
- fix sketch:
  Minimal local fix (INTEG-style append, adapted since the local graph store
  is a full beads.Store with real List/Ready): in
  cmd/gc/session_reconciler.go, extend reachableStoresForSession AND
  reachableStoresForSessionInfo to append the routed graph store — `if st,
  routed, err := routedGraphStoreFor(cityPath, cfg); err != nil { return nil,
  err } else if routed { stores = append(stores, st) }` — in BOTH arms (the
  workAssignmentStores cross-store arm and the rig-bound arm), fail-closed on
  routing error (callers already treat probe errors as has-work, the safe
  direction). That covers the drain-ack close gate, awake gates, pool-slot
  free, progress-stall recycle, and firstOpenAssignedWorkBeadForReachableStore
  in one seam. Follow-ups in the same slice: (1) append the same routed-graph
  leg to workAssignmentStores callers used by retirement/unclaim
  (unclaimWorkAssignedToRetiredSessionBead and the ForConfig cleanup probes at
  session_reconciler.go:3836/:3851) so release-of-record can unassign gcg
  beads; (2) add a graph candidate to coordClassStoreCandidates (or a graph-
  specific in-progress pass in collectAssignedWorkBeadsWithStores) so claimed
  gcg steps generate wake demand and orphan release. B36's graph-only
  execution-scope probes (livelock-avoidance: don't keep a graph-only worker
  alive for Dolt work it can't run) are the hardened end-state but not
  required for the destructive-loss fix; the append is a strict keep-alive
  superset and safe first.

### G05 [HIGH] bd mol current/progress federation (split-store molecule topology read)

- dimension: bd-cli-reads
- deploy evidence:
  INTEG 2c74f8747:cmd/gc/cmd_bd_mol_current.go:69 maybeRouteBdMolViaAPI
  (invoked at 2c74f8747:cmd/gc/cmd_bd.go:254) federates `gc bd mol
  current|progress <id> [--json]` through the controller's bead-graph
  endpoint, rendering bd's exact JSON shapes
  (molProgressJSON/molProgressSummaryJSON) so `.steps` is a populated array
  instead of the null a single-store passthrough returns; header comment: step
  beads are graph-class in the infra store, invisible to work-scoped bd,
  'blocks every reader (workflow progress, finalize approval)' (port of
  X2/305bed90d). B36 783407a97:cmd/gc/cmd_bd_shim.go:259-281 routes mol
  current|progress via client.GetBeadGraph and REFUSES unroutable mol forms
  under split phase rather than letting them silently miss SQLite-resident
  molecule topology.
- local evidence:
  No mol routing anywhere in local cmd/gc: `grep -rn 'maybeRouteBdMol|mol
  current' cmd/gc/*.go` (non-test) matches nothing; local doBd
  (cmd/gc/cmd_bd.go:196-300) has only maybeRouteBdGraphSqliteMutation
  (close/update only, cmd/gc/cmd_bd_graph_sqlite.go:38), bdInfraWriteRefusal,
  and maybeRouteBdShowLocal (show-only). resolveBdScopeTarget
  (cmd/gc/cmd_bd.go:610-702) has NO reserved-prefix arm, so 'mol current
  gcg-X' scope-resolves via GC_RIG/cwd/city to the WORK store and execs bd
  there. Exposure is live: the shipped pack prompt
  internal/bootstrap/packs/core/assets/prompts/pool-worker.md:51,62,74-75,85
  instructs workers to run `gc bd mol current <molecule-id>` in their main
  loop (graph-worker.md forbids it, but pool-worker is also shipped).
- failure scenario:
  On a graph-routed local city (backend=sqlite + .gc/store/graph.migrated),
  sling materializes a molecule whose root/steps mint gcg in the embedded
  graph store and stamps molecule_id=gcg-<root> on the work-store source bead
  hooked to a pool worker. The worker, following shipped pool-worker.md ('gc
  bd mol current <molecule-id>' in its main loop), runs the command; doBd has
  no mol interceptor and no reserved-prefix scope arm, so it execs bd against
  the work (Dolt) store, which contains neither the gcg root nor any step — bd
  reports no molecule / steps:null. The worker concludes there is no molecule
  and either executes the bead free-form (bypassing the step contract) or
  drains, and human/LLM 'gc bd mol current|progress' observability reads are
  equally blind. The controller's in-process finalize/dispatch readers are NOT
  affected locally (they route through resolveGraphStore), so the wedge is
  worker-side and observational, not controller-side.
- fix sketch:
  Add a mol read interceptor in doBd before resolveBdScopeTarget, mirroring
  the existing maybeRouteBdShowLocal pattern: recognize 'mol current|progress
  <id> [--json]' (port bdMolRoutable's conservative arg parse from INTEG
  2c74f8747:cmd/gc/cmd_bd_mol_current.go — refuse-to-route on other
  subcommands, omitted id, or view flags), and when graphSQLiteRoutingActive
  answer IN-PROCESS from resolveGraphStore/routedGraphStoreFor: load the root,
  walk children (reuse the collectBeadGraph traversal shape from
  internal/api/handler_beads.go:380), and render molProgressJSON /
  molProgressSummaryJSON exactly as INTEG defines them so '.steps' is a
  populated array. The in-process arm is preferable to INTEG's API route
  locally: it works controller-down (matching the existing show-fed and
  mutation-arm precedents) and needs no client plumbing. Fall through byte-
  identically for unrouted cities and non-routable mol forms; optionally
  refuse (B36-style) non-routable mol forms when routing is active so graph
  topology is never silently missed. Also consider fixing the stale 'DARK
  UNTIL THE WIRING COMPLETES' header in cmd/gc/graph_class_store.go, and
  either shipping the 'gc mol' surface the guard message references or
  rewording the refusal.

### G06 [HIGH] Reserved-prefix scope routing for ALL bd subcommands (show with extra flags, multi-id show, dep list, list --parent <id>)

- dimension: bd-cli-reads
- deploy evidence:
  INTEG 2c74f8747:cmd/gc/cmd_bd.go:691-698: resolveBdScopeTarget routes ANY bd
  subcommand carrying a reserved-prefix positional
  (config.IsReservedClassBeadID) to bdInfraScopeTarget — the store that owns
  the id — explicitly so reads and writes agree with
  claimableStore.storeForID; comment documents the failure it fixes: the id
  'falls through to the city WORK store, where the bead does not exist'. B36
  783407a97:cmd/gc/cmd_bd_shim.go:75-84,424-441: verb 'show' is routed
  unconditionally (any extra flags) to client.GetBead with an explicit
  404→empty-array (genuine absence) mapping.
- local evidence:
  Local federates ONLY plain `show <id> [--json]`:
  cmd/gc/cmd_bd_show_fed.go:95-118 bdShowRoutable returns ok=false for any
  other flag (e.g. --verbose), any second positional id, or any other verb —
  and the miss is a silent fall-through (no refusal). resolveBdScopeTarget
  (cmd/gc/cmd_bd.go:610-702) has no reserved-prefix arm, so `gc bd show gcg-a
  gcg-b`, `gc bd show gcg-X --verbose`, `gc bd dep list gcg-X`, and `gc bd
  list --parent gcg-X` all scope-resolve to the work store and exec bd there →
  'no issue found'/empty. The write side is guarded (cmd/gc/cmd_bd_guard.go
  bdInfraWriteRefusal) and known destructive gates use the covered plain form
  (orphan-sweep.sh:181 re-reads via `gc bd show <id> --json`, which IS
  federated), so exposure is read-side false absence, not direct destruction.
- failure scenario:
  On a graph-routed local city (backend=sqlite + .gc/store/graph.migrated
  marker), any `gc bd` read on a gcg id that is not exactly `show <id>
  [--json]` — e.g. `gc bd show gcg-a1 gcg-a2 --json`, `gc bd show gcg-X
  --verbose`, `gc bd dep list gcg-X`, `gc bd list --parent gcg-X` — silently
  falls through bdShowRoutable (ok=false, no refusal), is scope-resolved by
  resolveBdScopeTarget to the work/rig Dolt store (no reserved-prefix arm;
  bdBeadExists probes miss), and execs bd there, printing 'no issue found' or
  an empty list. An agent or operator treats live graph beads (molecule steps,
  deps) as absent and re-creates, abandons, or restarts work — the same false-
  absence class as the recorded root-loss incident. Writes are guarded and
  orphan-sweep uses the covered plain-show form, so exposure is decision-
  corrupting false absence, not direct destruction.
- fix sketch:
  Minimal fail-closed slice, mirroring the B36 shim's bdRefuse closed-
  allowlist property: in doBd, after maybeRouteBdShowLocal declines, scan
  bdArgs positionals (plus known id-carrying flag values like --parent) for
  reserved-prefix ids via reservedClassForBeadID; if any hit a ROUTED class
  (reuse classShowRouted), refuse loudly naming the covered replacement (`gc
  bd show <id> --json` / class-specific gc command) instead of exec'ing bd
  against the work store — converts silent false absence into a loud error
  with zero new read plumbing. Optional follow-ups: widen bdShowRoutable to
  accept multiple positional ids and benign flags (loop renderClassShow per
  id), and add a dep-list federation arm over the graph SQLiteStore's
  dependency reads. Alternatively port the INTEG reserved-prefix arm shape
  into local resolveBdScopeTarget as a refusal (it cannot be a routing arm
  locally, since bd cannot read the embedded sqlite class stores).

### G07 [HIGH] Assigned-work snapshot + orphan-release + wake-demand see graph-resident step beads

- dimension: mol-workflow-liveness
- deploy evidence:
  B36: collection reaches graph beads because the city store is the per-class
  Router (internal/beads/graph_only_list.go doc), plus
  cmd/gc/assigned_work_scope.go:31-90 remapGraphResidentAssignedWorkStoreRefs
  retags gcg beads' logical storeRef so every storeRef-scoped gate
  (assignedWorkIndexReachableFromAgent, filterAssignedWorkBeadsForSessionWake,
  openSessionOwnsWork, namedWorkReady, pool demand) stops missing them, and
  cmd/gc/pool_session_name.go:171-176 hoists GraphIDPrefix inside
  releaseOrphanedPoolAssignments so graph-resident beads gate on the graph
  leg's health and keep their physical ownerStore.
- local evidence:
  Local cmd/gc/build_desired_state.go:1096-1110
  collectAssignedWorkBeadsWithStores iterates coordClassStoreCandidates
  (cmd/gc/session_beads.go:922-936) = city work store + rig work stores; no
  graph-store source. The comment at build_desired_state.go:1145-1150
  explicitly says this pass exists so 'graph.v2 step beads orphaned by a
  session drain' reach releaseOrphanedPoolAssignments — but on a graph-routed
  local city those beads live only in the embedded graph sqlite and are never
  collected. releaseOrphanedPoolAssignmentsWhenSnapshotsComplete
  (cmd/gc/pool_session_name.go:97-111, called from city_runtime.go:2293 and
  cmd_start.go:946) consumes only result.AssignedWorkBeads, so it can never
  see or release them. Local assigned_work_scope.go has no graph remap (no
  gcg/GraphIDPrefix references). Note: READY unassigned graph demand IS
  covered separately (see covered finding), but assigned/in_progress/orphaned
  graph work is not.
- failure scenario:
  On a local city with [beads.classes.graph] backend="sqlite" and
  .gc/store/graph.migrated present, gcg step beads live only in the embedded
  graph SQLiteStore. A pool worker claims a gcg step (in_progress, assignee =
  its session identity) and then dies (crash/kill), or a session drain strands
  an open-assigned gcg step. Each controller tick,
  collectAssignedWorkBeadsWithStores fans out only over the leading city store
  (the sessions-class store) and rig work stores — the routed graph store is
  never a candidate — so the gcg bead never enters result.AssignedWorkBeads.
  releaseOrphanedPoolAssignmentsWhenSnapshotsComplete (city_runtime.go:2293,
  cmd_start.go:946) therefore never sees it and never reopens it;
  filterAssignedWorkBeadsForPoolDemand / session-wake never counts it; the
  session-retirement unclaim scans (workAssignmentStores) are also work-store-
  only. The step stays assigned to the dead session indefinitely, its
  dependents never become ready, and the workflow root silently stalls with no
  self-heal — the exact issue-#2793 failure the pass exists to prevent,
  reintroduced for the graph class.
- fix sketch:
  Minimal: thread cityPath (already a param of
  buildDesiredStateWithSessionBeads) into collectAssignedWorkBeadsWithStores
  and append one extra classStoreCandidate when routed: st, routed, err :=
  routedGraphStoreFor(cityPath, cfg); if routed, add {store: st, ref: ""}; if
  err != nil, log and set partial (fail-visible — partial already suppresses
  orphan release for the tick, which is the safe direction). Index alignment
  then automatically records the graph SQLiteStore in resultStores, so
  releaseOrphanedPoolAssignments' reopen write and stampRunSessionIdentity
  target the graph store with no further change. Apply the same candidate to
  collectOpenUnassignedRoutedWork if its route-canonicalization should cover
  graph beads. If rig-routed graph steps must pass the storeRef-scoped gates
  (openSessionOwnsWork etc.), additionally port B36's
  graphResidentAssignedWorkStoreRef/remapGraphResidentAssignedWorkStoreRefs
  (assigned_work_scope.go) to retag the logical ref while keeping the physical
  graph store; for a single-scope city the ref "" candidate alone suffices.
  Add a test: routed city, gcg step in_progress assigned to a closed/dead
  session bead → one tick reopens it (status open, assignee cleared) in the
  graph store.

### G08 [HIGH] Assigned-in-progress crash-recovery tier federated over the graph store (worker self-re-adoption after crash)

- dimension: claimable-crashrecovery
- deploy evidence:
  INTEG 2c74f8747 (commits 63235fe0a, 2ed2fc961):
  cmd/gc/split_city_work_query.go:57-64 rewrites the default work_query token
  'bd list --status in_progress --assignee=' → 'gc ready --status in_progress
  --assignee=' on a split city; cmd/gc/cmd_ready.go readyBeadsForOpts
  (status=in_progress branch) serves it from claimableStore.List, which fans
  out work+infra fail-loud — claimable_store.go:123-135: 'It backs the
  in_progress crash-recovery tier, where a graph step assigned to a worker
  that died lives in the infra store.' Wired at cmd_hook.go:336 (worker claim
  path) and work_query_probe.go:177 (controller wake probe).
- local evidence:
  Local cmd/gc/cmd_hook.go:333 uses a.EffectiveWorkQueryForBeads unmodified
  (no rewrite; no cmd_ready.go/split_city_work_query.go exist locally). The
  graph federation seam, graph_hook_ready.go:57-93
  mergeGraphReadyIntoWorkQueryOutput, unions only st.Ready(TierBoth) — and
  SQLiteStore.Ready hard-filters b.status='open'
  (internal/beads/sqlite_store.go:878-883), so in_progress steps are
  structurally excluded; grep 'in_progress' over
  graph_hook_ready.go/graph_hook_claim.go/graph_class_store.go = 0 hits.
  hookClaimExistingOrAssigned (cmd_hook_claim.go:305-312) can only adopt
  candidates the work query returned, and graphRoutedHookClaimOps'
  continuation reads fire only after a claim candidate exists.
- failure scenario:
  On a graph-routed local city, a pool worker claims a routed gcg step via
  graphRoutedHookClaimOps.Claim, which sets status=in_progress and
  assignee=worker identity in the embedded graph store
  (.gc/store/graph/beads.sqlite). The worker crashes mid-step. On respawn (or
  on the controller's per-session wake probe), the discovery shell runs:
  tier-1 'bd list --status in_progress --assignee=$id' reads only the work
  store (empty); graphFederatedWorkQueryRunner then unions only
  st.Ready(TierBoth), which SQL-filters b.status='open', so the assigned
  in_progress gcg step is excluded. hookClaimExistingOrAssigned therefore
  never sees it, and its existing_assignment self-re-adoption tier cannot
  fire. Independently, the controller's assigned-work collection
  (coordClassStoreCandidates = city + rigs, no graph arm) never captures the
  bead, so releaseOrphanedPoolAssignments never reopens it either. The step
  sits in_progress with a dead assignee forever; the workflow's downstream
  gates never unblock — permanent stranding requiring manual intervention.
- fix sketch:
  Extend the existing runner seam in cmd/gc/graph_hook_ready.go rather than
  porting the split-branch gc-ready command. In graphFederatedWorkQueryRunner
  (which already receives the query env slice), extract the identity
  candidates (GC_SESSION_ID / GC_SESSION_NAME / GC_ALIAS) from env, and when
  the city is graph-routed, run st.List(beads.ListQuery{Status: "in_progress",
  TierMode: beads.TierBoth}), filter rows whose Assignee matches an identity
  candidate, and union them into the merged JSON array alongside the Ready
  rows (fail-loud on store error, same contract as the Ready union).
  hookClaimExistingOrAssigned's first pass scans all candidates for an
  in_progress bead with own identity, so mere presence in the array restores
  self-re-adoption; ordering is irrelevant. Optionally wire the same wrapped
  runner (or the same union) into the controller wake-probe path in
  work_query_probe.go so the probe with injected GC_SESSION_NAME also sees the
  step. The companion orphan-release gap (finding 1: coordClassStoreCandidates
  lacking a graph arm) must be fixed separately for unassigned/dead-identity
  recovery.

### G09 [HIGH] Orphan-release last-resort session-liveness probe routed to the sessions-class store

- dimension: claimable-crashrecovery
- deploy evidence:
  INTEG 2c74f8747 commit 180ad7dd8 ('cure the split-city spawn/drain
  treadmill... + 3 reachability siblings'): cmd/gc/pool_session_name.go:97-149
  adds sessionStoreOpt and routes liveOpenSessionAssignmentExists to it — doc
  comment: 'on a split city session beads live in the INFRA store, not the
  work store, so probing the work store misses live holders and wrongfully
  releases their claims'; call site city_runtime.go beadReconcileTick passes
  sessStore.Store ('the last-resort session-liveness probe routes to the
  sessions store').
- local evidence:
  Local cmd/gc/pool_session_name.go:97-122 has no sessionStoreOpt parameter;
  line 183 calls liveOpenSessionAssignmentExists(store, assignee) where store
  = cr.cityBeadStore() (city_runtime.go:2264, call at 2293). Local sessions
  class IS relocatable to a dedicated sqlite store (class_store.go:293-301
  resolveSessionStore → sessionsdb.RoutedStoreFor), so on a sessions-routed
  city the probe reads the wrong store; List on the work store returns empty
  (no error) → probe returns false → release proceeds
  (pool_session_name.go:533-560 only fail-safes on List ERROR).
- failure scenario:
  Sessions-routed local city (backend=sqlite + .gc/store/sessions migration
  marker, residue sweep past its 10-minute grace): a worker session bead is
  created (spawn/adoption/sling) after beadReconcileTick's session snapshot is
  loaded from the ROUTED sessions store, and its claim lands before/within the
  tick's assigned-work collection — or a snapshotted live holder's reachable
  store-ref set fails to cover the work's store-ref (the "" / routed-leg drop
  the deploy commit documents). openSessionOwnsWork misses it;
  assigneePreservesNamedSessionRoute does not apply to pool-ephemeral
  assignees; the last-resort probe
  liveOpenSessionAssignmentExists(cr.cityBeadStore(), assignee) lists a work
  store that post-residue-sweep contains zero session beads, returns false,
  and releaseOrphanedPoolAssignments clears the live worker's assignee and
  resets its in_progress bead to open. The next demand pass spawns/claims a
  second worker onto the same bead: duplicate execution and clobbered claim
  (verifyReleasedPoolAssignment only logs the race).
- fix sketch:
  Port 180ad7dd8 onto the local session.Info-based signatures: (1) add
  `sessionStoreOpt ...beads.Store` to
  releaseOrphanedPoolAssignmentsWhenSnapshotsComplete and
  releaseOrphanedPoolAssignments in cmd/gc/pool_session_name.go, default
  `sessionStore := store`, override from sessionStoreOpt[0] when non-nil, and
  change only line 183 to liveOpenSessionAssignmentExists(sessionStore,
  assignee); (2) pass sessStore.Store at cmd/gc/city_runtime.go:2293
  (sessStore already in scope) and cliSessionStore(oneShotStore, cfg,
  cityPath) — already computed as sessStore — at cmd/gc/cmd_start.go:946; (3)
  update the now-stale residual comments in cmd_start.go:924-927 and
  frontdoor_di_guard_test.go:281-284; (4) adapt the site-4 red-first test from
  180ad7dd8's split_store_treadmill_test.go to a sessions-routed sqlite city:
  session bead present only in the sessions-class store, assigned in_progress
  pool work in the work store, assert the release sweep does NOT clear the
  assignee.

### G10 [HIGH] Controller assigned-work collection has no graph-store candidate — orphan release / wake demand never see assigned gcg work

- dimension: sling-convoy-adoptpr
- deploy evidence:
  Deploy sees graph beads in the city collection pass via the Router
  ClassGraph leg (B36 b233fec1b), then fixes the residual storeRef mistag: B36
  186ee6a93 'retag graph-resident assigned work to its routed rig (Group B)' =
  remapGraphResidentAssignedWorkStoreRefs (deploy/sqlite-b36-probe-
  attribution:cmd/gc/assigned_work_scope.go:68-92, called from
  build_desired_state.go:682), B36 4fa57334a Group A ListGraphOnlyHandle
  passthrough (bead_policy_store.go:131-141), B36 08cdd75f3 physical
  ownerStore remap for release. Same file exists on INTEG
  (2c74f8747:cmd/gc/assigned_work_scope.go).
- local evidence:
  cmd/gc/build_desired_state.go:1090-1107 collectAssignedWorkBeadsWithStores
  iterates coordClassStoreCandidates(cfg, cityStore, rigStores, ...)
  (cmd/gc/session_beads.go:922-936 = city + rigs only, no graph candidate);
  local cityStore is NOT a router — graph beads live in a separate SQLiteStore
  (cmd/gc/graph_class_store.go:83-104). grep remapGraphResident/graphResident
  in local cmd/gc: zero hits; local cmd/gc/assigned_work_scope.go lacks the
  whole block (diff vs B36 shows +90 lines absent).
- failure scenario:
  On a graph-routed local city (config [beads.classes.graph] backend="sqlite"
  + .gc/store/graph.migrated marker — now permitted, since
  sqliteCapableBeadClasses includes graph), a worker claims a gcg step via
  graphRoutedHookClaimOps, which sets assignee+in_progress directly in the
  embedded graph store. The worker session then dies. Every controller
  recovery path is graph-blind: (1) collectAssignedWorkBeadsWithStores lists
  in_progress/open only from city+rig stores, so the gcg bead never enters
  DesiredStateResult.AssignedWorkBeads and releaseOrphanedPoolAssignments
  never iterates it; (2) the retired/closed-session sweeps
  unclaimWorkAssignedToRetiredSessionBead and
  reassignWorkAssignedToRetiredSessionBead iterate workAssignmentStores
  (city+rigs only) and never unassign it; (3) appendGraphScaleTargets counts
  only Ready (open, unassigned) graph work, so the stuck in_progress bead
  contributes zero pool demand and its blocked dependents never become ready.
  The step stays assigned to the dead session forever; the workflow wedges
  silently with no partial/error surfaced — the issue-#2793 class reopened for
  the embedded graph store.
- fix sketch:
  Local fix must differ from B36's retag-only shape because gcg beads are
  physically absent from the snapshot, not mis-tagged. (1) In
  collectAssignedWorkBeadsWithStores (and collectOpenUnassignedRoutedWork if
  applicable), append a graph candidate to the store fan-out when
  routedGraphStoreFor(cityPath, cfg) is routed — classStoreCandidate{store:
  graphStore, ref: ""} — with a routing error surfaced as partial (fail-
  visible), matching appendGraphScaleTargets discipline. Because the
  beads/stores/storeRefs slices are index-aligned, release writes then
  automatically target the correct physical graph store. (2) Port B36's
  graphResidentAssignedWorkStoreRef/remapGraphResidentAssignedWorkStoreRefs
  (assigned_work_scope.go) to retag graph-collected beads from "" to their
  routed rig ref (gc.routed_to agent → assignedWorkStoreRefForAgent;
  gc.session_id direct-bind → ""; gc.root_store_ref "rig:NAME" fallback), so
  the storeRef-scoped gates (assignedWorkIndexReachableFromAgent,
  filterAssignedWorkBeadsForSessionWake/PoolDemand, openSessionOwnsWork)
  resolve correctly; the local port keys on routedGraphStoreFor + the gcg-
  prefix instead of B36's GraphOnlyListFor router capability. (3) Add the
  routed graph store to workAssignmentStores (or a graph arm in the
  unclaim/reassign sweeps in session_beads.go) so retired-session recovery
  also covers gcg work. Test: routed-city fixture with an in_progress gcg step
  assigned to a dead session; assert it appears in AssignedWorkBeads with the
  graph store at its index and that releaseOrphanedPoolAssignments reopens it.

### G11 [HIGH] Orphan-release last-resort liveness probe reads the WORK store, not the sessions-class store (INTEG sessionStoreOpt fix) — ACTIVE today, not gated on graph flip

- dimension: sling-convoy-adoptpr
- deploy evidence:
  2c74f8747:cmd/gc/pool_session_name.go:118-149:
  releaseOrphanedPoolAssignments gains sessionStoreOpt — 'on a split city
  session beads live in the INFRA store, not the work store, so probing the
  work store misses live holders and wrongfully releases their claims';
  liveOpenSessionAssignmentExists(sessionStore, assignee) at line ~203. Wired
  at 2c74f8747:cmd/gc/city_runtime.go:2210 (passes sessStore.Store) and
  cmd_start.go:957 (passes sessStore).
- local evidence:
  cmd/gc/pool_session_name.go:118-127: signature has no sessionStoreOpt; line
  181 calls liveOpenSessionAssignmentExists(store, assignee) with store =
  cr.cityBeadStore() (work store, cmd/gc/city_runtime.go:2263+2293).
  liveOpenSessionAssignmentExists (pool_session_name.go:533-556) does
  store.List(Label: gc:session) on that work store. Local sessions-class
  routing is LIVE on migrated cities (cmd/gc/class_store.go:293-301
  resolveSessionStore -> sessionsdb.RoutedStoreFor), so the probe reads a
  store that no longer contains session beads.
- failure scenario:
  On a sessions-migrated local city (today's shape, no graph flip): the
  reconcile tick loads the session-bead snapshot from the sessions-class
  store, then collects assigned work beads. A fresh pool session spawns and
  claims work bead X in that window (session bead written to .gc sessions
  sqlite; X.Assignee set in the work store). openSessionOwnsWork misses
  (holder absent from the older session snapshot),
  assigneePreservesNamedSessionRoute misses, and the last-resort probe
  liveOpenSessionAssignmentExists(store, assignee) lists Label:gc:session
  beads in the WORK store — which is empty of session beads post-migration
  residue sweep — and returns false without error.
  liveWorkAssignmentStillReleasable re-reads X, sees status/assignee
  unchanged, and the release proceeds: X is reopened and its assignee cleared
  while its holder is alive and working, so the next tick mints a second
  worker on X (duplicate/racing execution, possible claim-theft churn).
- fix sketch:
  Port the INTEG change verbatim into cmd/gc/pool_session_name.go: add
  `sessionStoreOpt ...beads.Store` to
  releaseOrphanedPoolAssignmentsWhenSnapshotsComplete and
  releaseOrphanedPoolAssignments, default `sessionStore := store` and override
  when sessionStoreOpt[0] != nil, and pass sessionStore to
  liveOpenSessionAssignmentExists. Wire the two call sites:
  city_runtime.go:2293 passes cr.sessionsBeadStore().Store (already computed
  as sessStore a few lines above in beadReconcileTick), and cmd_start.go:946
  passes sessStore (already computed via cliSessionStore). Update the
  cmd_start.go:924-929 comment that documents the deferred follow-up. Add a
  test: sessions-routed city, session bead only in the class store, assigned
  work in the work store, assert the claim is NOT released.

### G12 [HIGH] Drain reads input-convoy membership from the control (graph) store with no MemberStores tail and no owning-store/fail-loud guard — drain silently completes with 0 members

- dimension: sling-convoy-adoptpr
- deploy evidence:
  2c74f8747:internal/dispatch/drain.go:219-243: convoyStore =
  drainMemberOwningStore(store, parentConvoyID, opts) ('the input convoy + its
  tracks edges live in the WORK store, while the drain control runs in the
  GRAPH store'), Members(convoyStore, ..., opts.MemberStores...), plus
  explicit fail-loud guard: '0-row manifest would silently mark the drain pass
  and prematurely unblock the DAG'. MemberStores wired at
  2c74f8747:cmd/gc/cmd_convoy_dispatch.go:261-293 via crossStoreMemberStores
  (def line 433-442), and drain.go:1035 TrackItem(..., opts.MemberStores...).
- local evidence:
  internal/dispatch/drain.go:218 Members(store, parentConvoyID, false,
  opts.MemberStores...) — store is the control store, which on a graph-routed
  city is the graph store (cmd/gc/cmd_convoy_dispatch.go:436-447
  openControlStoreAtForCity routed arm); opts.MemberStores is never populated
  anywhere in local cmd/gc (grep MemberStores in cmd/gc: zero hits — only
  internal/dispatch threads it); no drainMemberOwningStore call for the convoy
  id, no empty-members guard (drain.go:195-243); drain.go:1011 TrackItem
  without MemberStores. The seam plumbing (drainMemberOwningStore,
  drainMemberProbeSet, drain.go:289-346) exists but collapses to the single
  store because MemberStores is always empty.
- failure scenario:
  On a graph-routed local city (config [beads.classes.graph] backend=sqlite +
  .gc/store/graph.migrated marker — enabled since BeadClassGraph is ratcheted
  into sqliteCapableBeadClasses): a user slings a v2 formula --on a work-store
  convoy (user convoys are work-class; only synthetic convoys route to the
  graph store). The workflow root (gcg, graph store) records
  gc.input_convoy_id = the work-store convoy id. When the drain control step
  dispatches, runControlDispatcherWithStoreAndConfig opens the graph store via
  openControlStoreAtForCity and loadOrBuildDrainManifest calls
  convoycore.Members(graphStore, workConvoyID) with an always-empty
  opts.MemberStores. The graph SQLiteStore's List(ParentID) and DepList both
  return empty rows (no error) for the absent convoy id, so Members returns 0
  members; rejectUnresolvedDrainMembers passes (nothing to reject), a 0-row
  manifest is persisted, and the len(manifest.Rows)==0 branch calls
  completeDrain — the drain is marked passed, the DAG unblocks, and downstream
  steps run against fully undrained work with no error, warning, or quarantine
  anywhere. Variant: sling --on a single work bead creates a synthetic (graph-
  store) input convoy whose tracks edge is DepAdd'd through the un-intercepted
  work store, so that path also fails to drain (dangling or missing edge),
  though it may surface differently.
- fix sketch:
  Port the deploy seam (2c74f8747) in two pieces. (1)
  cmd/gc/cmd_convoy_dispatch.go: add crossStoreMemberStores(cityPath, cfg)
  (work-class store tail: city work store + rig work stores on a routed city;
  empty on single-store cities) and set opts.MemberStores in the "drain" case
  (and "retry-eval"/"retry"/"ralph" for the required-artifact cross-store
  reads), mirroring the deploy branch's per-kind switch. (2)
  internal/dispatch/drain.go loadOrBuildDrainManifest: resolve the convoy's
  owning store via the already-present drainMemberOwningStore(store,
  parentConvoyID, opts), call convoycore.Members(convoyStore, parentConvoyID,
  false, opts.MemberStores...), and add the fail-loud guard — on
  len(members)==0, convoyStore.Get(parentConvoyID) must succeed or the
  expansion returns an error ("input convoy unresolvable from drain stores")
  instead of building a 0-row manifest. Also pass the member tail at the drain
  TrackItem call (~line 1011/1035) so unit-convoy tracks resolve work-store
  members. dispatch_control_ready.go's in-process fallback flows through the
  same runControlDispatcher opts construction, so the wiring covers it. Add a
  two-store test: drain control in a graph store, input convoy + members in a
  work store — asserting both the successful cross-store expansion with
  MemberStores wired and the loud error (not silent completion) when the tail
  is absent.

### G13 [HIGH] Worker crash-recovery tier (assigned in_progress list) is graph-blind — graph union merges Ready only, no split-city work-query rewrite

- dimension: sling-convoy-adoptpr
- deploy evidence:
  2c74f8747:cmd/gc/split_city_work_query.go:43-62 rewrites the default
  work_query's 'bd list --status in_progress --assignee=' crash-recovery tier
  to 'gc ready --status in_progress --assignee=' ;
  2c74f8747:cmd/gc/cmd_ready.go:119-131 readyBeadsForOpts(status=in_progress)
  -> claimableStore.List federating work+infra ('crash recovery of a graph
  step whose worker died', claimable_store.go List doc).
- local evidence:
  internal/config/workquery.go:170-199 still shells 'bd list --status
  in_progress --assignee=' against the work store; the local federation
  wrapper cmd/gc/graph_hook_ready.go:70 unions ONLY st.Ready(...) into the
  output (mergeGraphReadyIntoWorkQueryOutput) — in_progress gcg beads are
  never merged; no split_city_work_query.go and no gc ready command exist
  locally (ls cmd/gc/cmd_ready.go: not found).
- failure scenario:
  Graph-routed local city (backend=sqlite + graph.migrated marker): a named
  agent or stable-name pool instance claims a gcg step via the hook ready
  federation (graphRoutedHookClaimOps routes the claim into the embedded
  store; bead becomes in_progress, assignee = the session identity). The
  session crashes mid-step and the reconciler respawns it under the SAME
  identity. On restart, the work query's crash-recovery tier ('bd list
  --status in_progress --assignee=$id', internal/config/workquery.go
  standardAssignedInProgressWorkQueryScript) reads only the bd work store and
  returns []; graphFederatedWorkQueryRunner merges only st.Ready rows
  (graph_hook_ready.go mergeGraphReadyIntoWorkQueryOutput), so the in_progress
  gcg bead never appears in the candidate list and hookClaimExistingOrAssigned
  cannot adopt it. No other path recovers it:
  releaseOrphanedPoolAssignments/collectAssignedWorkBeadsWithStores fan out
  only over city+rig work stores (and would skip anyway since the same-
  identity session is live), session-retirement unclaim and orphan-sweep.sh
  are equally graph-blind, and dispatch.ProcessControl skips non-open beads
  with a trace line only. The step stays in_progress forever, its dependents
  (finalize/root) never gate open, and the workflow wedges until manual
  intervention.
- fix sketch:
  Minimal local fix in cmd/gc/graph_hook_ready.go: extend the federation union
  to include the graph store's assigned in-progress rows, mirroring integ
  readyBeadsForOpts(status=in_progress). In graphFederatedWorkQueryRunner,
  extract the session identities from the runner env
  (GC_SESSION_ID/GC_SESSION_NAME/GC_ALIAS — env is already passed in) and,
  alongside st.Ready(...), fetch st.List(beads.ListQuery{Status:
  "in_progress", Assignee: id, TierMode: beads.TierBoth, Live: true}) for each
  identity; union those rows (dedup by id, shell rows win) into the merged
  JSON before the ready rows so hookClaimExistingOrAssigned's
  in_progress+identity pass fires first. Over-inclusion stays safe per the
  file's existing contract (identity/claimability filters downstream are
  authoritative), and an unconditional un-scoped in_progress union would also
  work but bloats candidate lists with other workers' live steps. Keep the
  fail-loud error shape on List failure, same as the Ready arm. Test: cmd_hook
  test spawning a routed city, claim a gcg bead as identity X, run the claim
  path again as X with an empty work-store — expect existing_assignment
  adoption of the gcg id.

### G14 [HIGH] GET /v0/beads LIST federates the graph store as an explicit infra leg (fail-loud)

- dimension: api-read-federation
- deploy evidence:
  INTEG 2c74f8747:internal/api/huma_handlers_beads.go:71-86 —
  humaHandleBeadList injects the graph store into the list fan-out as a
  synthetic 'infra:<city>' leg ('graph-class beads (gcg- molecule roots,
  steps, control beads) live in the infra store, which BeadStores() does not
  include. Inject it as a federation leg or the whole DAG is invisible with an
  authoritative 200'); :172-178 a hard List failure on that leg returns 503
  ('graph plane unreadable') instead of a work-only Partial 200. Landed in the
  8803e0dbf embedded-store lineage.
- local evidence:
  internal/api/huma_handlers_beads.go:69 humaHandleBeadList iterates only
  stores := s.state.BeadStores() (loop :109-180);
  cmd/gc/api_state.go:1263-1277 BeadStores() returns city+rig stores only,
  never the graph store; grep GraphBeadStore in huma_handlers_beads.go returns
  nothing. By-id gcg arm exists (handler_beads.go:177-195) but no list-level
  arm.
- failure scenario:
  On a graph-routed local city (the branch's shipped state:
  .gc/store/graph.migrated marker + [beads.classes.graph] backend=sqlite), all
  gcg beads — molecule roots, steps, control beads, wisps, synthetic
  drain/input convoy members — live only in the dedicated graph SQLiteStore,
  which controllerState.BeadStores() (cmd/gc/api_state.go:1264) never returns.
  humaHandleBeadList (internal/api/huma_handlers_beads.go:69) fans out only
  over BeadStores(), so GET /v0/beads with any filter (type=molecule,
  assignee=<worker>, status, label, all=true) returns 200 with Partial:false
  and zero graph-plane beads. Dashboard bead lists and attention views, and
  any agent using the HTTP list to conclude "no graph work exists", see an
  empty graph plane while a formula is live. By-id (beadStoresForID graph
  arm), graph-walk-by-root, and convoy endpoints DO federate the graph store,
  so only list-level discovery is blind — but list is the discovery surface.
  The local ready handler (humaHandleBeadReady, :364) has the same blindness
  (separate dimension).
- fix sketch:
  Port the INTEG 2c74f8747 hunk into local internal/api/huma_handlers_beads.go
  humaHandleBeadList: immediately after `stores := s.state.BeadStores()`, if
  `graph := s.state.GraphBeadStore().Store; graph != nil && graph !=
  s.state.CityBeadStore()`, copy the map and add
  `merged["infra:"+s.state.CityName()] = graph` (':' cannot appear in a rig
  name, so no collision and explicit ?rig= never selects it), remembering the
  synthetic key in `infraLeg`. Because injection happens before
  rigNames/boundedCounts are computed, the bounded-total path picks it up
  automatically. In the per-store error arm, when `rigName == infraLeg` and
  the failure is a hard List error (not a partial-with-rows), return
  huma.Error503ServiceUnavailable("infra store list read failed (graph plane
  unreadable): "+err) instead of continuing to a work-only Partial 200. Add a
  test mirroring INTEG's: fakeState with distinct graphBeadStore, assert gcg
  beads appear in the list and that a failing graph store yields 503. Apply
  the same treatment to humaHandleBeadReady as a follow-up (tracked as its own
  finding).

### G15 [HIGH] GET /v0/beads/ready federates the graph store's ready set (fail-loud)

- dimension: api-read-federation
- deploy evidence:
  INTEG 2c74f8747:internal/api/huma_handlers_beads.go:432-453 — after city+rig
  federation, an explicit graph-store leg runs
  beads.HandlesFor(graph).Live.Ready() and fails LOUD with 503 on error ('a
  work-only 200 would hide the entire graph plane... mirrors
  claimableStore.Ready's fail-loud contract'). B36 equivalent:
  deploy/sqlite-b36-probe-attribution
  internal/api/huma_handlers_beads.go:337-351 routes readiness through the
  Router's ReadyGraphOnly (commit 1b3663849 + 2b56b20a2).
- local evidence:
  internal/api/huma_handlers_beads.go:357-426 humaHandleBeadReady federates
  federate("city", CityBeadStore()) and per-rig stores only (:399-407); no
  GraphBeadStore leg. Route registered at
  internal/api/supervisor_city_routes.go:201. The local WORKER claim path is
  in-process (cmd/gc/graph_hook_ready.go / graph_hook_claim.go), so workers
  are not wedged — but the HTTP surface is graph-blind.
- failure scenario:
  On a local city with [beads.classes.graph] backend="sqlite" and the
  .gc/store/graph.migrated marker present (routing active — the config ratchet
  BeadClassGraph:true accepts it), all molecule roots/steps live only in
  .gc/store/graph/beads.sqlite. GET /v0/city/{name}/beads/ready federates only
  the city bd store and rig stores, so with 40 dep-satisfied gcg steps ready
  it returns an authoritative 200 with items=[], Partial=false. Any remote-gc
  control-plane read, dashboard/generated-SDK BeadsReady consumer, or stall-
  incident diagnostic concludes the city has no ready work while the execution
  DAG is fully loaded; workers keep executing via the in-process hook path, so
  the HTTP surface silently contradicts actual city state. Additionally, a
  graph-store read failure is invisible (no leg, so no 503), unlike both
  deploy lineages. Same gap exists in handler_status.go:874 status ready
  federation (adjacent follow-up).
- fix sketch:
  Port the INTEG leg verbatim: in humaHandleBeadReady after the rig loop, add
  `if graph := s.state.GraphBeadStore().Store; graph != nil && graph !=
  s.state.CityBeadStore() { pa.attempt(); ready, err :=
  beads.HandlesFor(graph).Live.Ready(); if err != nil { return nil,
  huma.Error503ServiceUnavailable("infra store ready read failed (graph plane
  unreadable): " + err.Error()) }; pa.success(); dedupe via seen and append
  }`. No route change needed — supervisor_city_routes.go:201 already declares
  StatusServiceUnavailable. Add an internal/api test with a relocated graph
  store (pattern of class_store_test.go/state_class_accessors_test.go)
  asserting (a) gcg ready beads appear in the response on a split state, (b)
  graph-store Ready error yields 503 not a work-only 200, (c) single-store
  state is byte-identical. Consider the same leg for the handler_status.go
  ready federation as a follow-up.

### G16 [HIGH] Graph-store mutations emit bead.* events (controller CachingStore wrap + one-shot CLI emission)

- dimension: api-read-federation
- deploy evidence:
  INTEG 2c74f8747:cmd/gc/api_state.go:216-224 — the embedded infra/graph store
  gets 'the same CachingStore + event-feed treatment as the city store':
  cs.cityInfraStore = wrapWithCachingStore(ctx, openedInfra.Store, ep, true),
  whose onChange records bead.created/updated/closed to the bus; plus commit
  59ebf549f 'emit bead.* events from one-shot CLI class-store mutations' —
  production incident: '277/290 claimed gcg beads with no terminal event; run
  detail rendered every step Running forever'.
- local evidence:
  cmd/gc/class_store.go:303-316 resolveGraphStore returns the raw SQLiteStore
  and explicitly ignores the recorder ('Graph is event-silent by design');
  cmd/gc/api_state.go:1460-1468 GraphBeadStore doc: 'the graph store stays
  event-silent, matching the prior Router graph leg';
  cmd/gc/cmd_bd_graph_sqlite.go:22-24: 'Graph is bead.*-event-silent by design
  on this branch, so no emission wrap'; cmd/gc/class_store.go:205-234
  createTarget/graphApplierFor route creates to the raw routedGraphStoreFor
  handle (no CachingStore/onChange wrap). internal/runproj/fold.go:29-34 folds
  ONLY bead.created/updated/closed/deleted.
- failure scenario:
  On a graph-routed local city (graph.migrated marker + [beads.classes.graph]
  backend=sqlite, live since the b483e45c9 ratchet flip), slinging a formula
  pours the molecule (root + steps, gcg ids) into the embedded SQLite graph
  store with zero bead.* events: controller in-process creates bypass the
  CachingStore onChange (policy-store createTarget/graphApplierFor route
  around it), one-shot gc bd close/update on gcg ids use the deliberately
  emission-free cmd_bd_graph_sqlite.go arm, and hook claim/dispatch paths
  never Record. Since /v0 runs list/detail and the run SSE stream fold only
  bead.created/updated/closed/deleted from events.jsonl (runproj), the run
  never appears at all — empty runs list, no detail, no SSE frames (not a
  stuck-Running skeleton; nothing leaks, and autoclose's payload-less
  BeadClosed is a runproj decode miss). Orchestration itself still completes
  via the dispatcher's 5s-capped idle re-poll, so operators see a city doing
  work with a permanently empty run view — the misdiagnosis-then-destructive-
  restart incident class documented on the mc deploy (277/290 event-less gcg
  beads). Secondary cost: bead-event-triggered dispatcher wakes are lost,
  adding up to ~5s latency per graph hop.
- fix sketch:
  Add a thin emission wrapper for the routed graph store and use it at the
  three write seams. (1) Create an emitGraphStore (implements beads.Store +
  GraphApplyStore) that delegates to the routed SQLiteStore and Records
  bead.created/updated/closed with the full post-mutation bead snapshot as
  payload (same shape CachingStore.notifyChange emits, so runproj decodes it).
  (2) Controller path: plumb the events.Recorder the policy store already
  receives contextually into beadPolicyStore/beadPolicyGraphStore so
  createTarget(ClassGraph)/graphApplierFor return the wrapped store instead of
  the raw routedGraphStoreFor handle; alternatively have resolveGraphStore
  stop ignoring rec and wrap there. (3) One-shot CLI: port INTEG 59ebf549f
  into doBdGraphSqliteClose/doBdGraphSqliteUpdate — best-effort open the city
  events provider and Record bead.closed/bead.updated with the fetched post-
  mutation bead. (4) Cover the hook claim mutations in graph_hook_claim.go the
  same way. Defense-in-depth: cherry-pick the #4566 runproj terminal clamp
  from main. Test: sling on a routed test city, assert events.jsonl gains
  bead.created for root+steps and bead.closed on step close, and that
  runproj.Fold surfaces the run.

### G17 [HIGH] BFF run detail reads the authoritative run graph from GET /v0/beads/graph/{runID}, not just the event fold

- dimension: api-read-federation
- deploy evidence:
  B36 commit db9d6302c (deploy/sqlite-b36-probe-attribution
  internal/api/dashboardbff/runtailer.go) — detail() does one loopback read of
  /v0/city/{name}/beads/graph/{runID}, merges via mergeGraphBeads (graph beads
  win on id collision), and marks the detail partial with 'graph_fetch_failed'
  on read failure: 'a graph.v2 run keeps its step beads in the SQLite graph
  store and only a handful ever reach the event log; projecting over the event
  fold alone shows ~2 of ~67 steps.'
- local evidence:
  internal/api/dashboardbff/runtailer.go:606 detail() builds only 'off the
  warm bead snapshot' (event fold) + a loopback sessions read; grep
  fetchRunGraph|mergeGraphBeads|graph_fetch_failed|beads/graph across
  internal/api/dashboardbff and internal/runproj returns nothing (rc=1).
  Local's by-id graph endpoint itself works for gcg roots
  (handler_beads.go:177-195 + collectBeadGraph same-store walk), so the
  fallback read WOULD be servable if wired.
- failure scenario:
  On a graph-routed local city (graph class = embedded SQLite at
  .gc/store/graph/beads.sqlite), a graph.v2 run's step beads live only in the
  graph store and — by explicit design on branch worktree-sqlit
  (cmd/gc/cmd_bd_graph_sqlite.go: "Graph is bead.*-event-silent... no emission
  wrap") — emit no bead.* events into .gc/events.jsonl. The dashboard BFF run-
  detail surface (wired locally via cmd/gc/supervisor_dashboard.go) builds its
  DTO solely from the events.jsonl fold plus sessions/formula loopback reads
  (internal/api/dashboardbff/runtailer.go detail(), ~line 591), so the
  rendered run DAG is empty or near-empty for every graph.v2 run, and marks
  nothing partial — the operator sees a run with no steps and no error
  indicator, even though GET /v0/beads/graph/{runID} (served by
  handler_beads.go's gcg class-prefix arm + collectBeadGraph) returns the
  complete graph. Unlike deploy, this is not just resilience against missed
  events: with emission silent by design, no future emission fix restores the
  view — the read-side fetch is the only path.
- fix sketch:
  Port B36 db9d6302c onto worktree-sqlit (clean cherry-pick candidate; ~4
  files): (1) internal/api/dashboardbff/runtailer.go — add
  runTailerManager.fetchRunGraph doing one loopback GET
  {base}/v0/city/{name}/beads/graph/{rootID} over the existing self-read
  transport (same pattern as the sessions/formula caches), call it from
  detail(), and union via mergeGraphBeads with graph beads winning on id
  collision; on fetch failure append "graph_fetch_failed" to the partial
  reasons so the detail never reports complete on a truncated graph. (2)
  internal/runproj/detail.go + detail_types.go — thread the extra partial-
  reason/fetch-failure through BuildRunDetailFromSnapshot as the B36 commit
  does. (3) Port rundetailtailer_test.go additions. No server-side work
  needed: the local by-id graph endpoint already serves gcg roots via
  handler_beads.go beadStoresForID's class-prefix arm ([graph, city]) and the
  same-store collectBeadGraph walk. Optionally add a small TTL/single-flight
  cache keyed by (city, runID) mirroring sessionsCache to keep per-request
  loopback cost bounded, if the B36 version does not already.

### G18 [HIGH] Order dispatcher wisp-root label write + order-run evidence reads + stale-wisp sweep route to the graph store

- dimension: dispatch-doctor-status
- deploy evidence:
  B36 783407a975: internal/coordrouter/router_mutation.go:21-48 backendForID +
  Router.Update route by-id mutations (Update/Close/SetMetadata) of gcg beads
  to the graph backend, and router_federation.go:116-123 federates ListByLabel
  — and since every dispatcher store open goes through routedPolicyStore
  (api_state.go:246), the order dispatcher's post-instantiate
  `store.Update(rootID, ...)` label stamp, the order-run:<scoped> single-
  flight ListByLabel, and the stale-wisp subtree reads all reach the graph
  store automatically on B36.
- local evidence:
  cmd/gc/order_dispatch.go:453-455 storeFn =
  openStoreAtForCity(target.ScopeRoot) — molecule.Instantiate at :1537 DOES
  route the pour into the graph store via the create-side policy dispatch
  (cmd/gc/class_store.go:205-233 createTarget/graphApplierFor), but the
  follow-up store.Update(rootID, update) at order_dispatch.go:1561 is NOT
  routed (beadPolicyStore has no Update override — method list at
  bead_policy_store.go:56-267 — so the promoted Update hits the Dolt scope
  store and fails NotFound for gcg roots). hasOpenWorkStrict
  (order_dispatch.go:1649-1657) and the orders front graph leg use the raw
  scope store as the graph leg (cmd/gc/order_class_store.go:149
  NewStoreWithTracking(class, beads.GraphStore{Store: scope}) — scope, not
  routedGraphStoreFor), and the stale-wisp subtree sweep 'stays graph residual
  on the raw scope store' (order_dispatch.go:2272-2285,
  sweepStaleOrderWispSubtreesMode at :2279/:2503). No graph-routed order-
  dispatch test exists (grep sqlite/routed/migrated in order_dispatch_test.go
  = 0).
- failure scenario:
  On a graph-routed local city (backend=sqlite + .gc/store/graph.migrated), an
  interval or event order fires and dispatchWisp runs in the controller:
  molecule.Instantiate routes the pour through the create-side policy seam
  (createTarget/graphApplierFor) into the SQLite graph store, minting the wisp
  root as gcg-N. The follow-up store.Update(rootID, {Labels: order-
  run:<scoped>[, order:/seq:], Metadata: routed_to}) at
  cmd/gc/order_dispatch.go:1562 executes on the policy-wrapped SCOPE store —
  beadPolicyStore has no Update override, so it promotes to the embedded
  Dolt/file store, which has no gcg-N — and fails NotFound. The dispatcher
  logs "failed to label wisp", emits OrderFailed, and markTrackingFailure
  records the run as failed, while a live, unlabeled wisp graph exists (and is
  pool-visible via the graph hook-ready union, so it can be executed). On the
  next fire, hasOpenWorkStrict's order-run:<scoped> read (orders front whose
  graph leg is the raw scope store, order_class_store.go:149) cannot see the
  graph-store root — which is unlabeled anyway — so hasOpenWork=false and a
  duplicate wisp is poured every interval (tr-kds01 accumulation, now plus
  duplicate execution). sweepStaleOrderWispSubtreesMode lists only the scope
  store, so the orphaned graph wisp subtrees are never swept and accumulate
  unboundedly in .gc/store/graph/beads.sqlite.
- fix sketch:
  Three small routed arms plus a test, mirroring the existing seam style
  rather than porting the whole coordrouter: (1) Label stamp — add a by-id
  graph mutation arm: either an Update (and SetMetadata/SetMetadataBatch)
  override on beadPolicyStore that, when cityPath is set and the id carries
  the graph prefix (or Get on the routed graph store succeeds), routes through
  routedGraphStoreFor — the mutation twin of createTarget; or, more narrowly,
  in dispatchWisp resolve the store owning cookResult.RootID
  (routedGraphStoreFor when routing is active) before the store.Update at
  order_dispatch.go:1562. (2) Single-flight gate — make the orders front's
  graph leg routing-aware: in orderClassRoutingFor/bdOrderClassRouting
  (cmd/gc/order_class_store.go), when graphSQLiteRoutingActive, build the
  graph leg as a union of the scope store and routedGraphStoreFor
  (ListByLabel/Children/descendant walks), so HasOpenWork sees gcg wisp roots.
  (3) Sweep — run sweepStaleOrderWispSubtreesMode against the routed graph
  store in addition to the scope store on routed cities
  (order_dispatch.go:~2279/2503). Add an order-dispatch test on a graph-routed
  temp city (config + marker) asserting: wisp root lands in the graph store
  WITH order-run/order/seq labels, second tick does not pour a duplicate while
  the subtree is open, and the stale sweep closes an aged graph wisp subtree.

### G19 [HIGH] Control-dispatcher pack-custom work queries rewritten to the in-process/graph-federated reader (gc ready)

- dimension: dispatch-doctor-status
- deploy evidence:
  INTEG 2c74f8747: cmd/gc/dispatch_runtime.go:828-856 —
  controlDispatcherGraphReadNeedsGCNative(cityPath) plus
  rewriteControlReadyQueryGCNative rewrite EVERY `bd ready` / `bd --readonly
  --sandbox ready` in a control-dispatcher work query to `"${GC_BIN:-gc}"
  ready` (applied at dispatch_runtime.go:339-343 for both the built-in query
  and pack-configured overrides): 'Packs may configure their own bd-based
  work_query... on a sqlite-infra city every such query must still read
  through gc, because bd cannot open the embedded store.' B36 ships the
  federated `gc ready` command (cmd/gc/cmd_ready.go).
- local evidence:
  Local covers ONLY the built-in query shape:
  cmd/gc/dispatch_runtime.go:876-883 nextWorkflowServeBeads →
  tryControlReadyFromCacheOrFallback (dispatch_control_ready.go:363-397,
  graph-routed in-process arm at :384, cache primed over the graph-routed
  openControlStoreAtForCity, cmd_convoy_dispatch.go:436-447). But when
  parseControlReadyQuery does not recognize the query (pack-custom
  work_query), handled=false and the raw query is shelled via
  shellWorkQueryWithEnv (dispatch_runtime.go:883) with no graph union and no
  rewrite — grep for rewriteControlReadyQueryGCNative / GraphReadNeedsGCNative
  / a `gc ready` command in local cmd/gc = 0 hits. (The gc-hook worker path IS
  federated via graphFederatedWorkQueryRunner, cmd/gc/cmd_hook.go:457 — but
  that wrapper is not applied to the controller's serve-loop shell fallback.)
- failure scenario:
  A local city flips [beads.classes.graph] backend=sqlite (now permitted:
  BeadClassGraph is in sqliteCapableBeadClasses) and the graph.migrated marker
  exists, and its pack sets a custom bd-based work_query on the control-
  dispatcher agent (as mc's workflows pack does per the INTEG rationale).
  runWorkflowServe uses the pack query verbatim (dispatch_runtime.go:321-323);
  nextWorkflowServeBeads fails to recognize it (no BD_EXPORT_AUTO=false
  GC_CONTROL_TARGET= prefix) and shells it raw. bd opens the city/rig work
  store — where graph-class control beads no longer live — and returns an
  empty ready set every tick, with exit 0. Ready fan-
  out/check/tally/drain/finalize control beads are never discovered or
  claimed; every graph workflow stalls at its first control step, silently,
  fleet-wide on that city. Cities using the built-in query (WorkQuery=="") are
  unaffected (in-process graph arm at dispatch_control_ready.go:384 covers
  them).
- fix sketch:
  Cheapest option using existing local seams: in nextWorkflowServeBeads'
  shell-fallback arm (dispatch_runtime.go:~883), when the agent is a control
  dispatcher and graphSQLiteRoutingActive, wrap the shell exec with the
  already-built graphFederatedWorkQueryRunner(cityPath, cfg) union (cityPath
  recoverable via cityForStoreDir(dir), same as dispatch_control_ready.go:369)
  so the embedded graph store's ready set is unioned into pack-custom query
  results. Fuller port matching deploy: add a federated `gc ready` command
  (port B36 cmd/gc/cmd_ready.go onto resolveGraphStore) plus
  controlDispatcherGraphReadNeedsGCNative(cityPath){return
  graphSQLiteRoutingActive(...)} and rewriteControlReadyQueryGCNative, applied
  at dispatch_runtime.go after line 323 for control-dispatcher agents on
  routed cities — idempotent on the built-in query, and keeps the query
  semantics (assignee/route filters) authored by the pack instead of a blind
  union. Either way add a test: pack-custom bd work_query on a marker-flipped
  city must surface graph-store control beads from the serve loop.

### G20 [HIGH] Assigned-work snapshot + orphan-release collect a graph-store leg

- dimension: wrapper-capability-reads
- deploy evidence:
  INTEG 2c74f8747:cmd/gc/assigned_work_scope.go:84-144,302-313 — split-city
  infra leg layered on the assigned-work snapshot with storeRef retagging so
  pool-demand/wake filters and the release loop attribute graph beads to the
  '' (infra) leg; build_desired_state.go:519-545 unconditional cross-store
  probe so routed infra demand doesn't cause the spawn/drain treadmill. B36
  cmd/gc/assigned_work_scope.go:68-90 remapGraphResidentAssignedWorkStoreRefs
  (GraphIDPrefix-gated) + cmd/gc/pool_session_name.go:170-205
  releaseOrphanedPoolAssignments hoists graphPrefix so graph-resident beads
  gate on the city/graph leg's health and releases write to the graph owner
  store.
- local evidence:
  cmd/gc/build_desired_state.go:1102 collectAssignedWorkBeadsWithStores
  iterates coordClassStoreCandidates (session_beads.go:922-936 = city + rig
  stores only); grep 'graph' in build_desired_state.go hits only
  appendGraphScaleTargets (:676,:728 — ready/unassigned demand, not assigned
  work) and comments. cmd/gc/assigned_work_scope.go has zero graph/infra
  handling (grep empty). unclaimWorkAssignedToRetiredSessionBead
  (session_beads.go:995+) also iterates workAssignmentStores only.
- failure scenario:
  On a graph-routed local city ([beads.classes.graph] backend=sqlite +
  .gc/store/graph.migrated), a worker claims gcg step X via
  graphRoutedHookClaimOps (st.Claim sets in_progress + session assignee in the
  embedded graph store), then the session dies uncleanly.
  collectAssignedWorkBeadsWithStores lists only city+rig work stores, so X
  never enters AssignedWorkBeads: releaseOrphanedPoolAssignments never
  evaluates it, unclaimWorkAssignedToRetiredSessionBead's workAssignmentStores
  sweep never reaches the graph store, appendGraphScaleTargets' Ready probe
  reads X as not-ready (in_progress), and other sessions' work_query cannot
  claim it (tier-1 assignee mismatch). X stays in_progress assigned to the
  dead session forever; pool demand stays 0 and the workflow is permanently
  wedged (issue #2793 class re-opened for graph-routed cities). Secondary
  effect: live sessions holding only graph work also contribute nothing to
  wake/resume demand.
- fix sketch:
  Minimal: add a graph leg to the assigned-work fan-out. In
  collectAssignedWorkBeadsWithStores (and its call sites, which have
  cityPath), when routedGraphStoreFor(cityPath, cfg) is routed, append
  classStoreCandidate{store: graphStore, ref: ""} so the in_progress and open-
  assigned passes list gcg beads; the existing index-aligned
  assignedWorkStores slice then makes releaseOrphanedPoolAssignments write the
  reopen to the graph owner store automatically. Wrong-release of live holders
  is already guarded by the liveOpenSessionAssignmentExists last-resort probe
  (deploy comments: correct but slow), so storeRef "" is safe day one. Same
  leg must be added to workAssignmentStores (session_beads.go) so the
  retirement unclaim/reassign sweeps reach the graph store. Follow-up
  (perf/correct gating parity with B36): port
  graphResidentAssignedWorkStoreRef + remapGraphResidentAssignedWorkStoreRefs
  so gcg beads retag to their routed owner's rig ref and
  openSessionOwnsWork/pool-demand filters match without falling to the per-
  bead live probe.

### G21 [HIGH] GET /v0/beads/ready federates the graph store (fail-loud)

- dimension: wrapper-capability-reads
- deploy evidence:
  INTEG 2c74f8747:internal/api/huma_handlers_beads.go:432-450 — explicit graph
  arm after the city/rig federation: 'graph-class ready work (gcg- steps,
  control beads, molecule roots) lives in the infra store, which BeadStores()
  does not include — federate it or the whole execution DAG is invisible
  behind an authoritative-looking 200', with an infra-leg error returned as
  503 (authoritative failure, not Partial). B36
  internal/api/huma_handlers_beads.go:340-350 serves the graph leg via
  beads.GraphOnlyReadyFor on the Router.
- local evidence:
  internal/api/huma_handlers_beads.go:358-410 humaHandleBeadReady federates
  only s.state.CityBeadStore() + BeadStores() rigs; no GraphBeadStore
  reference anywhere in the function (grep GraphBeadStore in internal/api hits
  only handler_beads.go:186, handler_convoys.go:9,
  handler_convoy_dispatch.go:577/646). Note: a naive port must pair with the
  wrapper fix — Live.Ready() on the local RAW SQLiteStore defaults to
  TierIssues and would drop ephemeral wisps
  (internal/beads/sqlite_store.go:892-898 tier='main' filter), where integ's
  policy wrap expands the tier.
- failure scenario:
  On a graph-routed local city (config [beads.classes.graph] backend="sqlite"
  + .gc/store/graph.migrated marker — now activatable since
  sqliteCapableBeadClasses includes graph), all gcg- beads (task-typed
  wisp/workflow roots, steps, control beads) live only in the embedded graph
  SQLiteStore; the boot migration's residue sweep removes any city-store
  copies. GET /v0/beads/ready (humaHandleBeadReady) federates only
  CityBeadStore() and BeadStores() rigs, so it returns HTTP 200 with work-
  store rows only and zero Partial signal. Every HTTP consumer — dashboard
  ready views, remote gc, HTTP bd ready — sees an empty execution DAG and can
  conclude no graph work exists, inviting destructive operator action
  (precedent: graph-blind bd show caused a destructive restart). In-process
  orchestration (hook ready/claim, control dispatch) is unaffected, which is
  exactly why the HTTP blindness goes unnoticed.
- fix sketch:
  In internal/api/huma_handlers_beads.go humaHandleBeadReady, after the rig
  loop, port the INTEG arm: if graph := s.state.GraphBeadStore().Store; graph
  != nil && graph != s.state.CityBeadStore() { pa.attempt(); read graph ready;
  on error return huma.Error503ServiceUnavailable("infra store ready read
  failed (graph plane unreadable): "+err.Error()); pa.success(); merge rows
  through the existing seen-map dedupe }. Do NOT use
  beads.HandlesFor(graph).Live.Ready() naively — the raw SQLiteStore defaults
  to tier='main' and drops NoHistory wisp rows; instead mirror
  cmd/gc/graph_hook_ready.go:68 by type-asserting to a ReadyQuery-capable
  store (*beads.SQLiteStore or an interface) and calling
  Ready(beads.ReadyQuery{TierMode: beads.TierBoth}), falling back to
  Live.Ready() for non-SQLite graph stores. Add a routed-city test asserting a
  gcg task-typed root appears in the /v0/beads/ready response and that a
  broken graph store yields 503, not a work-only 200. Follow-on (same defect
  class): the ready federation in internal/api/handler_status.go needs the
  same graph leg.

### G22 [HIGH] Sling graph READ seam: GraphStore threading + sourceWorkflowStores graph arm

- dimension: wrapper-capability-reads
- deploy evidence:
  INTEG 2c74f8747:internal/api/handler_sling.go:124-136 sets
  SlingDeps.GraphStore = s.slingSplitGraphStore() (:329-345) explicitly
  because 'the reconciler/graph reads (which resolve through GraphBeadStore())
  never find' a molecule written to the wrong store, mirroring the CLI's
  cmd/gc/cmd_sling.go slingSplitGraphStore; and :382-412 sourceWorkflowStores
  appends the graph store — 'otherwise a duplicate workflow on a relocated
  graph goes undetected'.
- local evidence:
  Local CREATE side is covered by a different mechanism —
  beadPolicyStore.createTarget/graphApplierFor route ClassGraph
  creates/ApplyGraphPlan to the routed store (cmd/gc/class_store.go:205-234) —
  but the READ side is not: grep 'GraphStore:' over cmd/gc + internal (non-
  test) finds zero assignments; internal/api/handler_sling.go:118-147 builds
  SlingDeps without GraphStore, cmd/gc/cmd_sling.go populateSlingDepsCallbacks
  (:684-690) sets no GraphStore, so SlingDeps.graphStore()
  (internal/sling/sling.go:157-161) collapses to the WORK store;
  internal/api/handler_sling.go:353-371 sourceWorkflowStores has no graph arm.
- failure scenario:
  On a graph-routed city (.gc/store/graph.migrated marker +
  [beads.classes.graph] backend=sqlite), `gc sling <formula> --on <bead>`
  (graph v2, via CLI or API) instantiates the molecule through the
  beadPolicyGraphStore wrapper: ApplyGraphPlan classifies the workflow plan as
  ClassGraph and writes the gcg root+steps (stamped with gc.graphv2_root_key)
  into the embedded graph SQLite store. Any later sling with the same root key
  (retry after transient failure, order re-tick, operator re-run) evaluates
  closeFailedGraphV2Roots and existingGraphV2Root via
  deps.graphStore()==deps.Store, whose ListByMetadata forwards to the doltlite
  WORK store — finds nothing — and mints a second live gcg root with the
  identical root key; the graph-aware ready/dispatch seams then run both
  molecules' steps concurrently. With --force,
  snapshotGraphV2ReplacementRoot/closeReplacedGraphV2Root similarly miss the
  old gcg root in the work store, so the replace launches a new root while
  leaving the old one live (and the failed-instantiate rollback restores
  nothing). The source-workflow singleton scans (CLI convoyStoreCandidates and
  API sourceWorkflowStores) never open the graph store, so cross-store
  conflict detection cannot catch either duplicate.
- fix sketch:
  Mirror the INTEG seam onto the routed-store mechanism, three small patches.
  (1) cmd/gc/cmd_sling.go: before sling.New, set deps.GraphStore to the routed
  graph store when routing is active — if store, routed, err :=
  routedGraphStoreFor(cityPath, cfg); err==nil && routed { deps.GraphStore =
  store } (nil otherwise, preserving single-store collapse onto deps.Store).
  This makes existingGraphV2Root/closeFailed/closeReplaced reads AND
  molecule.Instantiate writes hit the same graph store. (2)
  internal/api/handler_sling.go: same assignment in the SlingDeps literal
  using the server-side equivalent (the api_state
  resolveGraphStore/routedGraphStoreFor path); also append the routed graph
  store to sourceWorkflowStores() with empty/graph StoreRef, matching INTEG's
  dedup-by-store-index behavior. (3) cmd/gc CLI singleton scan: in
  openSourceWorkflowStoresWithProvider (or the cmd_sling.go:503 call site),
  append a convoyStoreView for the routed graph store when
  graphSQLiteRoutingActive, tolerating open failure as a skip like rig stores.
  Tests: a graph-routed t.TempDir city where a second identical sling must
  return the existing gcg root (idempotent), and a --force re-sling must close
  the old gcg root.

### G23 [MEDIUM] CLI conditional release of reserved-prefix claims (release-if-current routed to the owning store)

- dimension: bd-cli-reads
- deploy evidence:
  INTEG 2c74f8747:cmd/gc/cmd_bd.go:548-560: doBdReleaseIfCurrent runs against
  the infra scope target (reserved-prefix arm resolves target first),
  performing the assignee re-read + conditional reset against the store that
  actually owns the bead, with infra-scope event augmentation ('a reserved-
  prefix release routes to the split city's infra scope'). So on integ, `gc bd
  release-if-current gcg-X <assignee>` verifies against and releases in the
  real graph store.
- local evidence:
  Local fails CLOSED instead of routing: cmd/gc/cmd_bd_guard.go:53-72
  bdInfraWriteRefusal explicitly refuses `release-if-current` on any reserved-
  prefix id (ids = bdArgs[1:2]) before doBdReleaseIfCurrent
  (cmd/gc/cmd_bd.go:235-239 ordering). That is safe (no wrong-store
  conditional release — the root-loss-safe direction) but means there is NO
  CLI path to release an orphaned gcg claim. Interlocking blindness: orphan-
  sweep.sh discovers candidates via `gc bd list --status=in_progress --json`
  (orphan-sweep.sh:58,75), which is not graph-federated locally, so graph-
  resident in-progress beads never enter the sweep at all — dead-worker gcg
  claims are invisible to the script AND unreleasable through gc bd. (Note:
  `gc bd list` is equally graph-blind on both deploy lineages — B36's shim
  passes 'list' through — so the list blindness itself is not a deploy-side
  fix; the routed release IS.) Recovery depends entirely on the in-process
  reconciler seams (graph_hook_claim.go exposes claim ops only — no release
  op: cmd/gc/graph_hook_claim.go:23,38).
- failure scenario:
  On a graph-routed local city, a pool worker dies holding an in-progress gcg
  step it claimed via the routed hook claim ops. No automated layer recovers
  it: orphan-sweep.sh never lists it (gc bd list is graph-blind) and its
  release verb (gc bd release-if-current) is refused by the write guard for
  gcg ids; the controller's releaseOrphanedPoolAssignments never sees it
  because the assigned-work fan-out (coordClassStoreCandidates) covers only
  city+rig stores, and session-retirement unclaim is equally graph-blind. The
  step stays in_progress/assigned and the molecule wedges until an operator
  notices and manually runs `gc bd update gcg-X --status open --assignee ""`
  (which does route to the graph store, but is an unconditional write with no
  re-claim race protection, and nothing points the operator at it — the
  refusal message says "use gc mol").
- fix sketch:
  Minimal fix for this finding: add a release-if-current case to the routed
  graph arm, before the write guard. In maybeRouteBdGraphSqliteMutation
  (cmd/gc/cmd_bd_graph_sqlite.go) accept bdArgs[0]=="release-if-current",
  parse via the existing parseBdReleaseIfCurrentArgs, require the id be a gcg
  id (requireAllGraphIDs), resolve routedGraphStoreFor, and call the store's
  ReleaseIfCurrent (beads.SQLiteStore already implements
  ConditionalAssignmentReleaser, internal/beads/sqlite_store.go:669), printing
  "released"/"skipped" byte-identically to doBdReleaseIfCurrent. Unrouted
  cities keep falling through to the guard refusal. Adjacent (larger) gap
  worth its own finding/fix: append a graph-store candidate to the
  controller's assigned-work collection (an appendGraphScaleTargets-style arm
  feeding collectAssignedWorkBeadsWithStores) so
  releaseOrphanedPoolAssignments can see and auto-release graph-resident
  orphaned steps, and make orphan-sweep.sh's in_progress discovery graph-
  aware.

### G24 [MEDIUM] gc bd mol current|progress federation for graph-class molecule roots/steps

- dimension: mol-workflow-liveness
- deploy evidence:
  INTEG 2c74f8747:cmd/gc/cmd_bd.go:254 intercepts before passthrough ('mol
  current|progress <id> on a split city cannot be answered by the single-store
  bd exec... returns steps: null and blocks every reader (workflow progress,
  finalize approval)'); 2c74f8747:cmd/gc/cmd_bd_mol_current.go:69
  maybeRouteBdMolViaAPI parses routable forms (current|progress + explicit id
  + at most --json, bdMolRoutable at :96-123), fetches client.GetBeadGraph
  (internal/api/client.go:1219) and renders bd's exact JSON/text shapes
  (renderBdMolCurrent/renderBdMolProgress); falls through byte-identically for
  other mol subcommands, omitted id, view flags, single-store cities, or
  controller-down. B36 has no mol federation (no cmd_bd_mol_current.go in its
  tree).
- local evidence:
  Local cmd/gc/cmd_bd.go has no mol handling (routings at :226-246 are graph-
  mutation arm, infra write guard, show-fed only; grep maybeRouteBdMol|'mol'
  across cmd/gc non-test = zero hits). `gc bd mol current gcg-<root>` falls
  through to the bd passthrough, which cannot see the embedded graph store ->
  'bead not found'. The server-side surface a port would need exists and is
  gcg-federated (internal/api/huma_handlers_beads.go:429-478
  humaHandleBeadGraph via beadStoresForID's gcg arm at
  handler_beads.go:186-192), but no local client method (no GetBeadGraph in
  internal/api/client.go) and no CLI interception. Mitigation limiting
  severity: local formula-v2 cities prime graph-worker.md, which explicitly
  forbids `gc bd mol current` (cmd/gc/cmd_prime.go:352-362;
  internal/bootstrap/packs/core/assets/prompts/graph-worker.md:10), and v1
  molecules (ga- ids) stay bd-visible; pool-worker.md (which instructs mol
  current at lines 51-85) is only primed when formula_v2 is disabled.
- failure scenario:
  On a graph-routed local city (config [beads.classes.graph] backend="sqlite"
  + .gc/store/graph.migrated marker — now flippable since BeadClassGraph is in
  sqliteCapableBeadClasses), any caller of the mol read surface — an operator,
  a diagnostic script, a custom pack prompt, or (aggravated case) the builtin
  pool-worker.md loop on a routed city with formula_v2 disabled, since v1
  molecule.Instantiate steps also carry gc.root_bead_id and classify to
  ClassGraph — runs `gc bd mol current gcg-<root>` or `gc bd mol progress
  gcg-<root> [--json]`. doBd has no mol interception, so the command hits the
  bd 1.1.0 passthrough, which resolves only the work store and exits 1 with
  'bead not found' (or renders steps:null for a work-resident root with graph-
  resident steps). Molecule observability through the mol surface is dead for
  graph-class roots; the builtin v2 path is mitigated because graph-worker.md
  forbids mol current.
- fix sketch:
  Port INTEG 2c74f8747's cmd/gc/cmd_bd_mol_current.go with two adaptations.
  (a) Gate: replace cityHasInfraStore(cityPath) with the local graph routing
  predicate — cheapest is the show-fed pattern:
  reservedClassForBeadID(id)==graph OR classStoreFileExists(cityPath,
  BeadClassGraph) (marker/file check, no cfg needed, so the intercept can sit
  at the same pre-config spot in doBd). (b) Source: instead of (or as fallback
  before) the API client, answer in-process from
  routedGraphStoreFor/graphClassStoreFor — the local branch already has
  embedded-store access (the show-fed's renderClassShow proves the pattern),
  which removes INTEG's controller-up requirement; keep bdMolRoutable's strict
  form gate (current|progress + explicit id + at most --json) and the byte-
  identical fall-through for everything else. If API parity with INTEG is
  preferred, also port GetBeadGraph into internal/api/client.go (INTEG
  client.go:1219) and use in-process render as the controller-down fallback.
  Reuse INTEG's molProgressJSON/molStepJSON render code verbatim to keep bd's
  JSON shapes.

### G25 [MEDIUM] retry-eval required-artifact SOURCE read not federated (P3 landmine #14) — passing attempts misclassified, worker sessions recycled

- dimension: sling-convoy-adoptpr
- deploy evidence:
  2c74f8747:internal/dispatch/retry.go:476-511: resolves gc.source_store_ref
  via opts.ResolveStoreRef (fail loud if resolver missing), then
  storeref.Resolve over [sourceStore]+opts.MemberStores — comment: 'Reading
  the source through the ambient graph store gets a clean ErrNotFound and
  misclassifies a genuinely-passing attempt as
  missing_required_artifact_context, burning retries.' Lineage: ebeba2a55
  'resolve retry required-artifact source across stores (P3 landmine #14)'.
- local evidence:
  Local HAS the #4374 root-first mitigation
  (internal/dispatch/retry.go:445-463: prefer root.Metadata[work_dir]); but
  the fallback at retry.go:471 is a bare store.Get(sourceID) on the ambient
  store — no source_store_ref resolution, no MemberStores probe (grep
  MemberStores internal/dispatch/retry.go: zero hits).
- failure scenario:
  Graph-routed local city (worktree-sqlit: [beads.classes.graph]
  backend=sqlite + .gc/store/graph.migrated): a retry-gated step declaring
  gc.required_artifact(s) completes with outcome=pass, but neither the attempt
  bead nor the workflow root carries a work_dir stamp (flow without the rebase
  gate). retry-eval on the gcg control runs against the embedded graph store
  only (openControlStoreAtForCity early-returns the routed graph store, no
  union). resolveRequiredArtifactWorktree (internal/dispatch/retry.go:445-483)
  falls through to store.Get(gc.source_bead_id) — a work-store bead — gets a
  clean ErrNotFound, returns missing_required_artifact_context, and the
  passing attempt is classified transient-fail. For pooled subjects,
  opts.RecycleSession (wired at cmd/gc/cmd_convoy_dispatch.go:236-256) kills
  the worker session and a new attempt is minted; the miss is deterministic,
  so every retry fails identically until attempts exhaust and the logical bead
  hard-fails, discarding the completed work. Mitigated (not eliminated) by the
  local #4374 root-first work_dir preference, which covers flows where the
  rebase gate stamps work_dir on the root.
- fix sketch:
  Port ebeba2a55 mechanically — all ingredients already exist locally. In
  internal/dispatch/retry.go: (1) change resolveRequiredArtifactWorktree
  signature to accept ProcessOptions (thread from
  resolveRequiredArtifactPath/validateRequiredArtifacts, which already receive
  opts at the classifyRetryAttemptWithPostconditions level); (2) when the
  sourceID came from gc.source_bead_id and
  root.Metadata[beadmeta.SourceStoreRefMetadataKey] is non-empty, resolve via
  opts.ResolveStoreRef (already wired for retry-eval at
  cmd/gc/cmd_convoy_dispatch.go:213), failing loud if the resolver is nil; (3)
  replace the bare store.Get(sourceID) with storeref.Resolve(sourceID,
  append([]beads.Store{sourceStore}, opts.MemberStores...))
  (internal/storeref/storeref.go:74 exists). Optionally populate
  opts.MemberStores with the city work store for graph-routed control dispatch
  in runControlDispatcherWithStoreAndConfig so the ref-less legacy-root case
  also resolves; the deploy-ref code and its test are directly copyable.

### G26 [MEDIUM] gc sling by-id has no reserved-class (gcg) source-store arm — graph beads unresolvable by sling

- dimension: sling-convoy-adoptpr
- deploy evidence:
  2c74f8747:cmd/gc/cmd_sling.go:722-739 slingSourceStoreRootForCandidate: 'A
  reserved coordination-class id namespace (gcg-..., including gcg-wisp-...)
  resolves to the infra scope root on a split city, so gc sling <gcg-...>
  opens the store that actually holds the infra/graph bead' (gated on
  cityHasInfraStore, config.ReservedClassBeadIDPrefix). Mirrored on the
  claim/read sides by claimable_store.go:59-66 storeForID.
- local evidence:
  cmd/gc/cmd_sling.go:661-678 slingSourceStoreRootForCandidate goes straight
  to sling.BeadPrefixForCity — 'gcg' matches neither HQ nor rig prefixes,
  returns ok=false, so probeExistingSlingSourceBead (line 642) returns
  unchecked; grep ReservedClassBeadIDPrefix in local cmd/gc: only
  graph_hook_ready.go:101 (attach filter), nothing in sling.
- failure scenario:
  On a graph-routed local city (backend=sqlite + .gc/store/graph.migrated),
  'gc sling <agent> gcg-<seq>' fails hard: probeExistingSlingSourceBead never
  considers the graph class store (slingSourceStoreRootForCandidate has no
  reserved-prefix arm), openSlingStore opens the work store, and DoSling's
  validateExistingBead returns MissingBeadError ('bead "gcg-123" not found in
  store …') — the graph bead is unroutable by sling even though it exists.
  Worse variant: graph beads migrated with their original multi-dash bd ids
  (CreateWithForeignID keeps foreign ids, e.g. <hq>-wisp-<hash>) fail the
  bead-id heuristic, are treated as inline text after the work-store probe
  misses (residue-swept), and applySlingInlineBead mints a junk task bead
  titled with the id and dispatches the agent onto it. Either way the operator
  re-dispatch/recovery flow for workflow roots is broken on a routed city.
- fix sketch:
  Mirror the deploy arm but keyed on class routing instead of infra scope. (a)
  In cmd/gc/cmd_sling.go slingSourceStoreRootForCandidate, before
  BeadPrefixForCity: if graphSQLiteRoutingActive(cityPath, cfg) and beadID
  starts with config.ReservedClassPrefix(config.BeadClassGraph)+"-", return
  (graphClassStoreDir(cityPath), prefix, true). (b) Teach the two store-open
  sites (probeExistingSlingSourceBead and openSlingStore, both currently
  hardwired to openAuthoritativeStoreAtForCity) to detect that dir and open
  via graphClassStoreFor(cityPath) (or resolveGraphStore) instead —
  SQLiteStore implements beads.Store, so the downstream SetMetadata/Get/hook
  path works unchanged. (c) Guard slingStoreEnvWithError and storeRef labeling
  so the graph store dir doesn't fall into the bd rig-env branch
  (samePath(storeDir,cityPath) is false there). Also cover the migrated
  foreign-id shape if desired by probing the graph store when the work-store
  probe comes back checked-but-missing. Test: routed-city sling of a minted
  gcg id routes the real bead; unrouted city keeps byte-identical behavior.

### G27 [MEDIUM] gc bd mol current/progress not federated — workers lose molecule situational awareness

- dimension: sling-convoy-adoptpr
- deploy evidence:
  5a09b4cdf 'federate gc bd mol current/progress across a split city's
  stores': 2c74f8747:cmd/gc/cmd_bd_mol_current.go (269 lines) answers mol
  current/progress from the controller's federated bead graph, matching bd's
  JSON shapes; wired from cmd_bd.go. Commit message: single-store passthrough
  returns null .steps because step beads live in the infra store.
- local evidence:
  ls cmd/gc/cmd_bd_mol_current.go: not found; local cmd/gc/cmd_bd.go routes
  only graph-sqlite mutations (line 227) and plain show federation (line 243,
  cmd_bd_show_fed.go restricts to 'show <id> [--json]'); mol subcommands pass
  through to bd against the work store.
- failure scenario:
  Graph-routed local city (backend=sqlite + .gc/store/graph.migrated): a sling
  attaches a wisp/workflow formula to a work bead; classify.go routes the
  molecule root+steps into the embedded graph store as gcg-* while sling
  stamps molecule_id=gcg-wisp-... on the work-store source bead. A pool worker
  claims the work bead, sees molecule_id in 'gc bd show', and per pool-
  worker.md runs 'gc bd mol current gcg-wisp-...' (and 'mol progress'). doBd
  has no mol seam, so the command passes through to bd against the work store,
  which has no gcg rows — bd exits with a molecule-not-found error (locally
  both root AND steps are graph-resident, so it is a hard miss, not mc's
  null-.steps). The worker cannot enumerate steps, concludes the molecule is
  absent, and falls back to executing the parent description wholesale —
  exactly the skip-steps failure the molecule protocol forbids. Step closes
  still work (mutation arm), but step discovery is broken for every attached
  molecule on a routed city.
- fix sketch:
  Add cmd/gc/cmd_bd_mol_fed.go with maybeRouteBdMolLocal(cityPath, cfg,
  bdArgs, stdout, stderr), wired into doBd immediately after
  maybeRouteBdShowLocal (cmd_bd.go:245). Port bdMolRoutable plus the
  molStepJSON/molProgressJSON/molProgressSummaryJSON shapes and renderers from
  2c74f8747:cmd/gc/cmd_bd_mol_current.go, but replace the API-client
  federation with the in-process store the local architecture already uses:
  gate on graphSQLiteRoutingActive + a gcg-prefixed id, open the store via
  routedGraphStoreFor (same pattern as cmd_bd_show_fed.go), load the root and
  its child step beads with parent/dep edges from the embedded SQLiteStore,
  and render current/progress in bd's JSON/text shapes. Fall through byte-
  identically for non-gcg ids, other mol subcommands, extra flags
  (--for/--limit/--range), or unrouted cities. Table test alongside: routed
  city with a gcg molecule → mol current/progress returns populated steps;
  non-gcg id → passthrough untouched.

### G28 [MEDIUM] /status work counts (open/in_progress/ready) include the graph plane

- dimension: api-read-federation
- deploy evidence:
  B36 covers by architecture: statusWorkCounts iterates BeadStores() whose
  city entry is the graph_store=sqlite Router, so graph beads are counted
  (8803e0dbf/2b56b20a2 Router lineage). Note INTEG shares the local hole —
  2c74f8747:internal/api/handler_status.go has no GraphBeadStore arm and integ
  BeadStores() (api_state.go:1344-1358) excludes the infra store.
- local evidence:
  internal/api/handler_status.go:581-645 statusWorkCounts queries
  CityBeadStore + BeadStores() rigs only; no graph leg. Ready derivation
  (0d1c7606e, in local history via #4285 merge 044a49b7d) is canonical but
  store-scoped the same way.
- failure scenario:
  Routed local city with N open/in_progress/ready gcg graph beads (workflow
  roots, steps, control beads): full GET /v0/status — and gc status, which
  renders that body — computes open/in_progress/ready from the city store and
  rig stores only, omitting the entire graph plane. During active formula
  execution, where most in-flight work is graph-resident step beads, the city
  reads as (near-)idle in dashboards and busy-checks. Lite-mode polls
  (?lite=1) omit work counts and are unaffected; no destructive action gates
  on these counts.
- fix sketch:
  In internal/api/handler_status.go statusWorkCounts, after appending the city
  query, add a graph leg guarded like handler_convoys.go: if g :=
  s.state.GraphBeadStore().Store; g != nil && g != cityStore { queries =
  append(queries, workQuery{label: "graph", store: g, includeStored: true,
  includeReady: true}) }. The existing seenReady id-dedup already handles
  ready-union overlap, and the stored leg's Counter-or-List path works against
  the embedded SQLiteStore. Add a test with a fake state whose GraphBeadStore
  differs from CityBeadStore asserting graph open/in_progress/ready beads
  appear in the counts (and that the default backend,
  GraphBeadStore==CityBeadStore, stays byte-identical).

### G29 [MEDIUM] gc graph routes reserved gcg- ids to the graph-class store (with cross-class member probes)

- dimension: dispatch-doctor-status
- deploy evidence:
  INTEG 2c74f8747: cmd/gc/cmd_graph.go:98-121 openRigAwareStore — 'Reserved
  graph-class id on a split city: the DAG... lives in the infra store, which
  no rig/HQ prefix or route reaches — without this arm `gc graph gcg-…` hits
  the city store and returns NotFound' — routes
  config.IsReservedClassBeadID(args[0]) to cachedCityInfraStore and returns
  graphWorkMemberStores/graphInfraMemberStores so convoy members render
  instead of 'unknown' placeholders.
- local evidence:
  Local cmd/gc/cmd_graph.go:93-124 openRigAwareStore has no reserved-id arm:
  it resolves only via slingDirForBead (rig prefixes) then
  openStoreAtForCity(cityPath) — grep of cmd_graph.go for
  routedGraphStoreFor/IsReservedClassBeadID = 0 hits. (By-id federation exists
  locally for plain `gc bd show` via cmd/gc/cmd_bd_show_fed.go and for
  dispatch via findBeadAcrossStores cmd/gc/cmd_convoy_dispatch.go:480-493, but
  gc graph does not use either.)
- failure scenario:
  On a graph-routed local city ([beads.classes.graph] backend="sqlite" +
  .gc/store/graph.migrated marker — activatable today since BeadClassGraph is
  in sqliteCapableBeadClasses), an operator runs `gc graph gcg-…` to inspect a
  stalled workflow's DAG/readiness. openRigAwareStore resolves the city work
  store (gcg matches no rig prefix; the beadPolicyStore wrapper has no read
  routing), resolveGraphInput's store.Get fails NotFound, and the command
  exits 1 reporting the root as absent — a false-absence result on the primary
  workflow-inspection surface consulted before destructive recovery.
  Additionally, even for a work-store convoy, members relocated to the graph
  store would render as failures since local doGraph has no cross-class
  member-store probes.
- fix sketch:
  In cmd/gc/cmd_graph.go openRigAwareStore, before the slingDirForBead arm: if
  len(args)>0 && config.IsReservedClassBeadID(args[0]), load cfg and call
  routedGraphStoreFor(cityPath, cfg) (cmd/gc/graph_class_store.go); if routed,
  return that store (fail-closed on error, per the routing contract — no work-
  store fallback). Minimal slice ends there. For full INTEG parity, also
  thread a memberStores []beads.Store parameter through
  doGraph/resolveGraphInput (as INTEG 2c74f8747 does with
  graphWorkMemberStores/graphInfraMemberStores) so convoy member expansion
  probes the other class's store: work stores when the primary is the graph
  store, and the routed graph store when the primary is a work store.

### G30 [MEDIUM] GET /v0/beads (list) injects the graph/infra leg

- dimension: wrapper-capability-reads
- deploy evidence:
  INTEG 2c74f8747:internal/api/huma_handlers_beads.go:72-84 —
  humaHandleBeadList merges an 'infra:<city>' leg ('graph-class beads ... live
  in the infra store, which BeadStores() does not include. Inject it') and
  :172-177 treats an infra-leg read error as authoritative 503 ('graph plane
  unreadable') rather than Partial. B36 gets this for free via Router
  federation of the city store.
- local evidence:
  internal/api/huma_handlers_beads.go:18-120 humaHandleBeadList iterates only
  s.state.BeadStores() rigNames (plus city); no GraphBeadStore reference in
  the file (local grep of GraphBeadStore in internal/api).
  beadListBoundedTotal (:323-338) likewise counts only rig stores.
- failure scenario:
  On a graph-routed city (backend=sqlite + .gc/store/graph.migrated), GET
  /v0/beads — including ?assignee=<worker> and all=true totals — reads only
  BeadStores() (city + rigs) and returns an authoritative 200 that omits every
  gcg molecule root, step, wisp, and control bead. A dashboard or operator
  listing a worker's beads sees none of its claimed graph steps and may
  conclude the session holds no work; list totals/counts exclude the whole
  graph plane. By-id GET, convoy views, and CLI-direct reads still work, so
  the gap is silent and inconsistent across surfaces.
- fix sketch:
  Port the INTEG infra-leg block into local
  internal/api/huma_handlers_beads.go: after `stores := s.state.BeadStores()`,
  if `graph := s.state.GraphBeadStore().Store; graph != nil && graph !=
  s.state.CityBeadStore()`, copy the map and add it under `infraLeg :=
  "infra:" + s.state.CityName()` (colon can't collide with a rig name;
  explicit ?rig= never selects it). In the List error loop, on a hard failure
  where `rigName == infraLeg`, return huma.Error503ServiceUnavailable("infra
  store list read failed (graph plane unreadable): ...") instead of recording
  Partial. beadListBoundedTotal needs no change — it iterates the merged
  stores/rigNames; if the graph SQLiteStore lacks exact Count for a query,
  boundedMode falls back to the full scan, which stays correct. Add a test
  mirroring INTEG's (fakeState with relocated graph store; assert gcg beads
  appear in the list and that a failing graph leg yields 503).

### G31 [MEDIUM] Cross-class member resolution for /v0/beads/graph (memberStoreComplement)

- dimension: wrapper-capability-reads
- deploy evidence:
  INTEG 2c74f8747:internal/api/handler_beads.go:393-422 —
  memberStoreComplement probes the complement class stores when resolving
  convoy/graph members ('a drain-unit convoy in the infra/graph store tracks
  work-store members. Without probing the complement the view renders those
  members as synthetic "unknown" placeholders'), threaded into
  collectBeadGraph(store, root, memberStores...).
- local evidence:
  internal/api/handler_beads.go:380 — local collectBeadGraph(store beads.Bead
  root) has no memberStores variadic and no complement helper (grep
  memberStoreComplement empty); humaHandleBeadGraph
  (internal/api/huma_handlers_beads.go:429-456) resolves foundStore via the
  gcg-aware beadStoresForID but then walks members in that single store only.
- failure scenario:
  On a graph-routed local city (graph class = embedded SQLite store minting
  gcg, GraphBeadStore != CityBeadStore), GET
  /v0/city/{city}/beads/graph/{rootID} for a gcg-prefixed graph-resident
  convoy (e.g. a drain parent convoy whose tracks deps point at work-store
  beads) pins foundStore to the graph store via the beadStoresForID class-
  prefix arm, then convoycore.Members probes only that store: every work-store
  member Get misses and is replaced by a synthetic unresolvedTrackedItem
  (Status "unknown", Type "task", Title==ID). The graph response — consumed by
  the dashboard run/graph view via getV0CityByCityNameBeadsGraphByRootId —
  renders real members as unknown placeholders and omits their true status,
  children, and deps. The reverse direction (work-store convoy tracking gcg
  members) fails identically, as do the two convoy list/show handlers
  (huma_handlers_convoys.go:176, :423) that share the single-store Members
  call.
- fix sketch:
  Port the INTEG hunk essentially verbatim — no library change needed since
  internal/convoy.Members already accepts the memberStores variadic locally.
  In internal/api/handler_beads.go: add the (s *Server)
  memberStoreComplement(foundStore beads.Store) []beads.Store helper (nil when
  GraphBeadStore().Store is nil or == CityBeadStore(); [city + sortedRigNames
  work stores] when foundStore is the graph store; [graph] otherwise), and
  change collectBeadGraph's signature to (store beads.Store, root beads.Bead,
  memberStores ...beads.Store), passing memberStores... to convoycore.Members
  at line 418. In internal/api/huma_handlers_beads.go:456: call
  collectBeadGraph(foundStore, root, s.memberStoreComplement(foundStore)...).
  Also apply s.memberStoreComplement(store)... to the two convoycore.Members
  calls in internal/api/huma_handlers_convoys.go (lines 176 and 423), matching
  INTEG. Port the INTEG test
  internal/api/collect_bead_graph_cross_store_test.go (asserts nil complement
  on single-store cities and cross-store member materialization on routed
  ones).

### G32 [LOW] Substring/truncated-id resolution on federated show (bd fuzzy resolver parity)

- dimension: bd-cli-reads
- deploy evidence:
  INTEG keeps reserved-prefix reads on bd's own resolver against the owning
  (Dolt-infra) store — 2c74f8747:cmd/gc/cmd_bd.go:691-698 routes the scope,
  then execs real bd, whose substring resolver matches a truncated id
  (behavior documented as intentional for reads in the exact-ID-guard comment,
  2c74f8747:cmd/gc/cmd_bd.go:~320: 'gc bd show (read passthrough) does NOT
  have this guard and still substring-resolves. That is intentional').
- local evidence:
  Local federated show is exact-match only: cmd/gc/cmd_bd_show_fed.go:53-66
  routes any 'gcg-'-prefixed id (a truncated gcg id still matches
  reservedClassForBeadID) to renderClassShow → classStoreShowBead →
  SQLiteStore.Get, which is `SELECT ... WHERE id=?` exact
  (internal/beads/sqlite_store.go:573-577) → printBdShowNotFound renders bd's
  'no issue found' (cmd_bd_show_fed.go:239,248) for an id bd itself would have
  substring-resolved. Because the reserved arm handles the id BEFORE any fall-
  through, there is no second chance.
- failure scenario:
  On a graph-routed local city, `gc bd show gcg-abc [--json]` where gcg-abc is
  a truncated form of existing bead gcg-abc12: reservedClassForBeadID matches
  the gcg- prefix, the reserved arm in maybeRouteBdShowLocal serves it
  exclusively (handled=true even on miss), SQLiteStore.Get exact-matches
  nothing, and the command prints bd's genuine-absence shape (`Error fetching
  gcg-abc: no issue found matching "gcg-abc"`, exit 1). On a Dolt-infra deploy
  city the same truncated id would have been substring-resolved by bd's own
  reader. An operator/agent pasting a shortened id from a log concludes the
  step bead was deleted. Read-only; no destructive path consumes the false
  absence. (Note: on embedded-sqlite-infra deploy cities the truncated read
  was already broken pre-flip, so the regression is only vs. bd-owned Dolt
  stores.)
- fix sketch:
  In cmd_bd_show_fed.go's reserved arm (and reusable by the legacy-probe arm),
  on ErrNotFound from the exact Get, run a class-store candidate scan before
  printing not-found: add a small resolver — e.g.
  SQLiteStore.ResolveIDInfix(partial) doing SELECT id FROM beads WHERE id LIKE
  '%'||?||'%' (bd resolves substrings, not just prefixes; reuse the existing
  LIKE-scan pattern at sqlite_store.go:244) — then: exactly one candidate →
  Get(full) and render; multiple → print bd-shaped ambiguity error listing
  candidates (exit 1); zero → existing printBdShowNotFound. Wire equivalent
  single-candidate fallbacks (or skip with a comment) for the other class
  stores' show arms in classStoreShowBead so the reserved-prefix classes stay
  behaviorally uniform. Guarded to reads only — the write guard and exact-ID
  mutation discipline (gcy-g4o) must not inherit this resolver.

### G33 [LOW] bd v1.1.x ephemeral in-progress probe repair (ga-oevup7): non-open statuses query by status alone

- dimension: claimable-crashrecovery
- deploy evidence:
  INTEG 2c74f8747 commit 9a2782c41 'fix(workquery): repair the assigned-in-
  progress self-adoption tier for wisps': internal/config/workquery.go
  bdQueryEphemeralStatusShell — for status != open, emits plain `bd query
  --json 'status=<s>'` because the compound `ephemeral=true AND
  status=in_progress` silently returns zero rows on bd v1.1.x, blinding the
  assigned-in-progress self-adoption tier to molecule step wisps (7 of 21
  review runs stranded 2026-07-19).
- local evidence:
  Local internal/config/workquery.go bdQueryEphemeralStatusShell (lines
  ~66-69) unconditionally emits the compound `ephemeral=true AND status=<s>`
  predicate — the pre-fix shape; no status branch exists. The rest of
  workquery.go is otherwise line-identical to integ's.
- failure scenario:
  Local city on bd 1.1.x whose work-store molecule/review-run step wisps land
  in the no_history wisps tier (ephemeral=0 in the wisps table — via
  [beads.policies.wisp] storage="no_history", or steps inheriting NoHistory
  from a no_history wisp root through storageFromPersistedWispRoot): worker
  crashes mid-step; on respawn, tier-1 'bd list --status in_progress
  --assignee' returns nothing (bd list sees no wisps-table beads at all —
  empirically verified) and the ephemeral probe's compound 'ephemeral=true AND
  status=in_progress' excludes ephemeral=0 wisps rows, so the assigned-in-
  progress self-adoption tier returns empty and the session reports 'No routed
  work' while its claimed step sits in_progress. Default-config cities (bd105
  compat → ephemeral-tier wisps) are NOT affected: the compound returns
  ephemeral=1 wisps on bd v1.1.0 (empirically verified against the
  v1.1.0-lineage query engine, identical at deployed ref 8c958d2).
- fix sketch:
  Cherry-pick INTEG 9a2782c41 (applies cleanly; local workquery.go is
  otherwise line-identical): in internal/config/workquery.go
  bdQueryEphemeralStatusShell, return `bd query --json 'status=<s>' --limit=0`
  when status != "open", keeping the bounded compound for the open-status
  pool-demand probes; regenerate the four workquery goldens
  (legacy_AssignedInProgress_bd104/bd105, legacy_OnBoot_bd104/bd105) and port
  the config_test.go regression test pinning the predicate shape.

### G34 [LOW] Orphan reclaim gated on ONE global snapshot-partial flag instead of per-store-ref (B36 Group F)

- dimension: sling-convoy-adoptpr
- deploy evidence:
  B36 a0579772f 'gate orphan reclaim per-store-ref, not one global partial
  (Group F)': deploy/sqlite-b36-probe-
  attribution:cmd/gc/pool_session_name.go:109
  releaseOrphanedPoolAssignmentsWithPartialScopes(...,
  result.AssignedWorkPartialStoreRefs) — a partial read of one store suspends
  release only for that store's beads.
- local evidence:
  cmd/gc/pool_session_name.go:97-115
  releaseOrphanedPoolAssignmentsWhenSnapshotsComplete: 'if
  result.snapshotQueryPartial() { return nil }' — global gate; no
  AssignedWorkPartialStoreRefs field or per-scope variant in local
  build_desired_state.go (grep PartialStoreRefs: zero hits).
- failure scenario:
  On a graph-routed local city (embedded SQLiteStore minting gcg, plus one or
  more attached rig stores), any rig store whose
  List(in_progress)/List(open)/Ready() query errors during a controller
  reconcile tick sets the single DesiredStateResult.StoreQueryPartial bool.
  releaseOrphanedPoolAssignmentsWhenSnapshotsComplete
  (cmd/gc/pool_session_name.go:108) then returns nil, skipping orphan release
  for ALL stores — including gcg-resident pool work whose own graph-sqlite leg
  returned a complete snapshot. While the flaky store keeps erroring (e.g.
  chronic bd/dolt EOF flap), orphaned pool assignments in healthy stores stay
  assigned, pool demand stays suppressed, and affected molecules stall;
  release resumes only on the first fully-complete tick.
- fix sketch:
  Port B36 a0579772f (deploy/sqlite-b36-probe-
  attribution:cmd/gc/pool_session_name.go + build_desired_state.go): (1) add
  AssignedWorkPartialStoreRefs map[string]bool to DesiredStateResult and
  populate it in collectAssignedWorkBeadsWithStores by recording source.ref
  for each store goroutine that returned errs ("" = city/graph store); (2) in
  releaseOrphanedPoolAssignmentsWhenSnapshotsComplete keep SessionQueryPartial
  as a global bail, fall back to the global skip when StoreQueryPartial is set
  with an empty attribution map, otherwise call a new
  releaseOrphanedPoolAssignmentsWithPartialScopes; (3) in that variant, skip
  only beads whose collected scope (index-aligned assignedWorkStoreRefs[i]) is
  in the partial set, with the graph-prefix override
  (beads.GraphOnlyListFor(store).GraphIDPrefix()) so gcg- beads gate on the ""
  key rather than a retagged rig ref, and bail globally if partialStoreRefs is
  non-empty but storeRefs are not index-aligned. Adapt to the local
  session.Info signature (local functions take []session.Info, not
  []beads.Bead). Add a test mirroring
  TestCityRuntimeBeadReconcileTick_StoreQueryPartialDoesNotReleaseAssignedWork
  that asserts a rig-scoped partial still releases city/graph-scoped orphans.

### G35 [LOW] Control-dispatcher serve loop cross-store fallback for misplaced control beads

- dimension: sling-convoy-adoptpr
- deploy evidence:
  ce4e41b2d (2c74f8747 lineage) 'control-dispatcher serve loop federates to
  the bead's store': cmd_convoy_dispatch.go adds an ErrNotFound ->
  findBeadAcrossStores fallback in runControlDispatcherInStore so a graph bead
  physically in the work store (pre/without migration) is processed in place
  instead of error-looping.
- local evidence:
  Local runControlDispatcherInStore (cmd/gc/cmd_convoy_dispatch.go:157-165)
  has no fallback on Get failure; BUT local covers the main class differently:
  openControlStoreAtForCity routed arm (line 436-447) sends all control
  dispatch to the graph store, findBeadAcrossStores has a gcg-first arm (line
  479-493), and boot-time migration + residue sweep
  (cmd/gc/graph_class_migrate.go, city_runtime.go:291-292
  sweepLegacyGraphResidue) relocates work-store gcg residue.
- failure scenario:
  Graph-routed local city (marker present): an out-of-lineage writer — an old
  gc binary still on PATH or a long-lived pre-flip process, or a bd write with
  an explicit gcg- id — creates an open gcg control bead in the bd work store
  after the boot residue sweep has run. Routed control-ready discovery reads
  only the embedded graph store, so the serve loop never sees the bead; there
  is no error loop, it is simply invisible. Manual recovery also fails: `gc
  convoy control <id>` resolves gcg ids via the graph-store-only arm of
  findBeadAcrossStores and returns not-found. The control bead (and any
  molecule gated on it) wedges until the next controller restart, when boot-
  time sweepLegacyGraphResidue merge-imports it into the graph store and
  normal dispatch resumes. Bounded, self-healing at restart, rogue-writer
  precondition — severity low.
- fix sketch:
  Two small local changes, either of which closes most of the window: (1) in
  cmd/gc/cmd_convoy_dispatch.go findBeadAcrossStores, make the gcg arm fall
  through to the city/rig scan on beads.ErrNotFound instead of returning the
  error — this restores manual `gc convoy control <id>` recovery and makes a
  ported ce4e41b2d-style serve-loop fallback effective rather than a no-op;
  (2) run sweepLegacyGraphResidue periodically instead of boot-only —
  piggyback the existing controller cadence (e.g. the wisp-GC shouldRun gate
  or the per-file store maintenance loop in city_runtime.go) so a post-boot
  pour converges within minutes without a restart. (2) is the convergence fix;
  (1) is the operator escape hatch. Porting the deploy dispatcher fallback
  alone, without (1), does nothing locally.

### G36 [LOW] type=molecule list augment surfacing graph.v2 run roots (gc.kind=workflow)

- dimension: api-read-federation
- deploy evidence:
  B36 aa76d6d98 + 4cca46820 (deploy huma_handlers_beads.go:121-138, 186-195):
  GET /v0/beads?type=molecule adds a second per-store Metadata
  gc.kind=workflow query with global cross-store dedupe, because graph.v2
  roots are issue_type=task, not molecule — the dashboard runs view read.
- local evidence:
  No augment locally: internal/api/huma_handlers_beads.go:109-180 runs only
  the primary typed query (grep KindMetadataKey in huma_handlers_beads.go:
  none). Mitigating difference: the local dashboard runs view is projection-
  based (runproj/orders_feed via workflowStores + events fold), not a
  type=molecule bead read — so the b36 consumer doesn't exist here. The
  residual gap is the raw API query shape only, and it is subsumed by the
  missing list-level graph federation (finding 1): on a routed city
  type=molecule misses graph roots on both axes.
- failure scenario:
  A raw API client (script, external tool, or a future dashboard change)
  calling GET /v0/beads?type=molecule to inventory runs gets an incomplete
  set: graph.v2 run roots are issue_type=task tagged gc.kind=workflow, so on a
  NON-routed local city they are silently omitted by the type filter, and on a
  graph-routed city they are omitted entirely because the graph SQLite store
  is not in BeadStores(). The local dashboard itself is unaffected — its runs
  view folds runproj over .gc/events.jsonl, not this endpoint.
- fix sketch:
  Port the B36 augment into local internal/api/huma_handlers_beads.go
  listBeads: when input.Type=="molecule", append a second ListQuery per store
  with Type cleared and Metadata: map[string]string{beadmeta.KindMetadataKey:
  "workflow"} (local beads.ListQuery already has the Metadata field,
  query.go:81). Keep it best-effort additive — only the primary query (qi==0)
  drives partialAggregator and boundedCounts — and dedupe augment rows
  globally by "\x00workflow\x00"+bead.ID so multi-store federation doesn't
  multiply the same root. For the routed-city store-miss axis, fold the graph
  store into the list federation set (the finding-1 fix): either add the
  routed GraphBeadStore to the stores iterated for the augment query, or
  address it wholesale when list-level graph federation lands.

### G37 [LOW] bd on_close hook autoclose CLI arms route root/attachment reads through the graph-class store (cliGraphStore)

- dimension: dispatch-doctor-status
- deploy evidence:
  INTEG 2c74f8747: cmd/gc/molecule_autoclose.go:92/100 pass
  cliGraphStore(store, cfg, cityPath) into doMoleculeAutocloseWith, and
  cmd/gc/wisp_autoclose.go:65/73 pass it into doWispAutocloseWith — 'the just-
  closed bead is read from its owning store, but its molecule/graph-workflow
  root lives in the graph-class store... exactly the controller precedent at
  api_state.go runBeadCloseAutoclose'. cliGraphStore (integ
  cmd/gc/cli_class_store.go:39-46) resolves the routed class store and adds
  CLI event emission.
- local evidence:
  Local doMoleculeAutoclose (cmd/gc/molecule_autoclose.go:79-88) and
  doWispAutoclose (cmd/gc/wisp_autoclose.go:54-63) call
  doMoleculeAutocloseWith/doWispAutocloseWith WITHOUT the graphStoreOpt
  trailing argument, so graphStore collapses to the owning (Dolt) store;
  cliGraphStore does not exist locally (grep 'cliGraphStore' in cmd/gc non-
  test = 0 hits). The *cores* already accept the seam
  (molecule_autoclose.go:123, wisp_autoclose.go:83) and the CONTROLLER arm is
  covered: cmd/gc/api_state.go:613-627 runBeadCloseAutoclose passes
  graphStore.Store from the routed GraphBeadStore(). Only the bd-hook CLI arm
  is graph-blind.
- failure scenario:
  On a graph-routed local city that still carries a non-gc-stamped
  .beads/hooks/on_close hook (legacy/user-authored — a supported configuration
  per the nativeHooksGate preflight in internal/beads/factory.go) or where an
  operator manually runs the hidden `gc wisp autoclose`/`gc molecule
  autoclose` commands, the hook-time autoclose reads the closed work bead from
  its owning Dolt store and then looks up its gc.attached_workflow_root /
  reverse-scans workflow roots in that SAME store; the gcg roots live in the
  graph SQLite store, so the arm silently no-ops. The close is still handled
  by the controller's graph-aware runBeadCloseAutoclose within one reconcile
  cycle (~30-60s) via the cache-eviction bead.closed event, so in steady state
  the only effect is lost synchronous redundancy and added latency. An
  attached wisp root actually leaks only in the narrower window where the
  close happens while the controller is down or restarting: the rebooted
  CachingStore primes with the bead already closed, never emits the
  bead.closed eviction event, and that close's autoclose never runs — the open
  stepless root can then be treated as orphaned and re-dispatched.
- fix sketch:
  Pass the routed graph store as the trailing graphStoreOpt at the four local
  CLI call sites, mirroring the controller arm: in doMoleculeAutoclose
  (cmd/gc/molecule_autoclose.go) hoist the loadCityConfig call that
  autocloseStoreRef already performs (load cfg once, add an
  autocloseStoreRefWithConfig variant as INTEG did), and in doWispAutoclose
  (cmd/gc/wisp_autoclose.go) load cfg best-effort with io.Discard; then call
  doMoleculeAutocloseWith(store, ref, rec, beadID, stdout,
  resolveGraphStore(store, cfg, cityPath, rec)) and doWispAutocloseWith(store,
  beadID, stdout, resolveGraphStore(store, cfg, cityPath, nil)) —
  resolveGraphStore (cmd/gc/class_store.go:312 → resolveGraphStoreRouted,
  graph_class_store.go:127) already exists locally and is identity on unrouted
  cities and fail-closed on marked cities, so unrouted behavior stays byte-
  identical. Porting INTEG's fuller cliGraphStore + classStoreWithCLIEmission
  (CLI-side bead.* event emission, commit 59ebf549f) is a separate follow-up;
  the read/close routing alone closes this finding's gap. Also fix the stale
  "DARK UNTIL THE WIRING COMPLETES" header comment in
  cmd/gc/graph_class_store.go, which contradicts BeadClassGraph:true in
  internal/config/config.go.

### G38 [LOW] Doctor/status bead counters remain work-store-only (backlog depth)

- dimension: dispatch-doctor-status
- deploy evidence:
  B36 783407a975: no dedicated backlog-depth doctor check exists there, but
  any doctor/status read opening a store gets the graph-federated Router
  automatically (api_state.go:246 routedPolicyStore; router_federation.go
  Ready/ListOpen federation) — deploy's mechanism makes such probes graph-
  inclusive by construction; B36's /status StoreHealth reads run over the
  Router-wrapped controller store (783407a97 tip commit).
- local evidence:
  cmd/gc/doctor_backlog_depth.go:100-131 opens only the city store
  (c.newStore(c.cityPath)) and calls ListOpen/Ready on it — on a routed city
  ready graph-class steps and control beads are excluded from the claimable
  count; no graph arm (grep graph in doctor_backlog_depth.go = comment-free).
  Other local doctor checks do not read molecule/wisp beads (grep across
  doctor_*.go), so backlog depth is the only affected probe found.
- failure scenario:
  On a local city with [beads.classes.graph] backend="sqlite" and the
  graph.migrated marker present, all molecule roots/steps/wisps live in
  .gc/store/graph/beads.sqlite (gcg prefix). `gc doctor` backlog-depth opens
  only the work store (c.newStore(c.cityPath) → openStoreAtForCity, whose
  beadPolicyStore.Ready/ListOpen do not federate), so its "N claimable (of M
  open ...)" line counts zero graph-class work even when dozens of graph steps
  are Ready — while workers' own claim path (graph_hook_ready.go union) does
  see them. An operator triaging a stall reads "0 claimable" and wrongly rules
  out backlog; on flip day the metric's shape silently discontinues. Advisory-
  only: no automation gates on the check. Same blindness affects
  doctor_run_target_backfill.go's workflow-root listing on routed cities.
- fix sketch:
  Minimal local fix in cmd/gc: pass cfg into newBacklogDepthCheck
  (cmd_doctor.go:321 already has cfg in scope) and, in backlogDepthCheck.Run
  after opening the city store, call routedGraphStoreFor(c.cityPath, cfg)
  (cmd/gc/graph_class_store.go:110); when routed, append the graph store's
  ListOpen("open") and Ready() results to the work-store sets (dedup by ID —
  prefixes are disjoint so a simple append is safe) before classifyBacklog,
  and note the graph-store contribution in the message (e.g. "city store: N
  claimable (X graph)"). On routing-resolve error, degrade to StatusWarning
  "backlog depth partial: graph store unavailable" rather than silently
  reporting work-store-only. Optionally apply the same routedGraphStoreFor arm
  to runTargetRoutedToBackfillCheck.collect's city scope.

## Refuted claims (checked, NOT gaps)

- (sling-convoy-adoptpr) Convoy member resolution in API/CLI graph+convoy views passes no cross-store member tail (landmine #15 Half B)
  The code-level claim is accurate but the failure scenario is unreachable on
  the local branch, and reachability was the finding's load-bearing premise
  for medium severity. CONFIRMED (angle 1): Deploy mechanism exists exactly as
  cited — commit 5fb0888cd ("resolve cross-store convoy members in
  graph/convoy views (P2/P3 landmine #15 Half B)") is on the B36/integ
  lineage; 2c74f8747 shows Members(store, root.ID, true, memberStores...) in
  internal/api/handler_beads.go (~:465), Server.memberStoreComplement at both
  huma_handlers_convoys.go sites, and the cmd_graph.go member-store threading.
  CONFIRMED (angle 2, partially): Local view call sites pass no tail — /data/p
  rojects/gascity/.claude/worktrees/sqlit/internal/api/handler_beads.go:418,
  internal/api/huma_handlers_convoys.go:176 and :423, cmd/gc/cmd_graph.go:276
  all call convoycore.Members(store, id, ...) bare; no memberStoreComplement
  exists in internal/api. No other seam (show federation, mutation arm, hook
  fed, storeref, policy store) substitutes for view-lane member resolution.
  Notably, the LIBRARY half is already ported locally:
  internal/convoy/membership.go:96 Members is variadic with the probe-set
  contract, storeref.Resolve exists, and internal/dispatch has
  ProcessOptions.MemberStores + drainMemberProbeSet. REFUTED (angle 3): No
  production code on the local branch ever creates the state the failure
  requires — a convoy whose tracks edges are co-resident in one class store
  while the member beads live in the other: (a) grep shows
  ProcessOptions.MemberStores is populated NOWHERE in production (only
  drain_test.go); cmd_convoy_dispatch.go:189 builds opts with
  CityPath/StorePath only. (b) The boot migration deliberately drops cross-
  boundary dep edges (cmd/gc/graph_class_migrate.go importGraphSnapshot: `if
  !graphIDs[d.DependsOnID] { continue }` — "Cross-boundary relationships stay
  metadata linkage per the split design"), so migrated convoys land in the
  graph store WITHOUT tracks edges to work members. The resulting defect is
  silently EMPTY membership (edges absent), not the claimed 'unknown'
  placeholders — a migration-design/write-lane issue, not the view-lane tail.
  (c) Post-flip, convoycore.TrackItem validates the target via
  storeref.Resolve over the same store handle it DepAdds through, so dispatch
  (graph-store-ambient via openControlStoreAtForCity) and `gc convoy track`
  fail before minting a cross-store edge; fresh drain expansion hard-fails
  with unresolved_member (rejectUnresolvedDrainMembers) before any unit convoy
  exists; the persisted-manifest branch routes unknown members to the
  DrainMemberUnresolved metadata path, never TrackItem. (d) The graphv2 input-
  convoy path (beadPolicyStore: Create routes synthetic convoys to the graph
  store via createTarget, but DepAdd is NOT overridden and falls through to
  the work store) would misplace the tracks edge in the WORK store — again
  invisible-empty from the graph-store view, not placeholdered. That is
  deploy's landmine #7 (dispatch/write-lane MemberStores threading), which is
  ALSO unported locally and sits causally ahead of Half B. Angle 4: Because
  the placeholder scenario has no live producer, "dashboard and operator views
  show wrong membership/progress" today is not honest at medium. This mirrors
  the deploy commit's own treatment of its Half A as a "latent seam with no
  live producer — left as documented". The gap is a real, must-port last-mile
  slice, but it is inert until the write lane (#7) lands, and porting Half B
  alone would fix nothing observable.

- (api-read-federation) Cross-store convoy member resolution (memberStoreComplement) in convoy/graph read endpoints
  Deploy mechanism confirmed at INTEG (memberStoreComplement in
  handler_beads.go:400, threaded into collectBeadGraph, ConvoyGet,
  ConvoyCheck), and the local code-level claim is accurate (convoycore.Members
  supports a memberStores probe tail but no API caller passes it; local
  collectBeadGraph doesn't even accept member stores; dispatch
  ProcessOptions.MemberStores never set in cmd/gc). However the claimed
  failure scenario is unreachable on the local branch at every link: (1) sling
  auto-convoys run only for non-formula slings and track the slung WORK bead
  in the same work store — no production path creates a work-store convoy
  tracking a gcg root ('every sling convoy is cross-store' is false;
  root↔convoy linkage is metadata gc.input_convoy_id, and gc convoy create /
  POST /v0/convoys pre-validate items in the work store and fail on gcg ids);
  (2) the cross-store convoys that do exist (synthetic graph-class
  input/drain-unit convoys living in the graph store) never reach the
  complement-blind Members call — ConvoyGet/ConvoyCheck iterate only
  BeadStores() (work stores, api_state.go:1264) and isGraphConvoyID only
  diverts gc.kind=workflow roots, so graph-store convoys 404 before member
  resolution, which the complement would not fix; (3) even on GET
  /v0/beads/graph, whose beadStoresForID gcg arm can find a graph-store
  convoy, there are no cross-boundary tracks edges to resolve:
  graph_class_migrate.go imports only within-graph deps (cross-boundary edges
  deliberately dropped — 'stay metadata linkage per the split design', pinned
  by TestGraphTopologyCrossStoreAttachLinkage) and post-flip TrackItem via the
  policy store lands DepAdd on the embedded work store, so Members returns
  empty (member loss), not unknown placeholders. Complement probing resolves
  existing edges; it cannot recover edges that were never co-resident. Ported
  alone, the read-side complement changes no observable behavior on this
  branch today. The real defect this uncovers is a different, write/migration-
  side gap (cross-boundary tracks-edge residence), which deserves its own
  finding — e.g. a migrated in-flight drain sees 0 members and completeDrain
  fires on an empty manifest.

- (wrapper-capability-reads) Policy wrapper stack (tier expansion TierIssues->TierBoth) on the graph-class store
  The structural observation is accurate but the failure scenario does not
  survive a full trace. Verified facts: (1) Deploy mechanism exists as cited —
  INTEG 2c74f8747 cmd/gc/bead_policy_store.go wraps
  List/Ready/ReadyContext/Count in expandPolicyReadTier. (2) Locally the
  routed graph store IS raw — resolveGraphStoreRouted
  (cmd/gc/graph_class_store.go:127) returns the *beads.SQLiteStore unwrapped,
  and beadPolicyStore only routes CREATES to it (createTarget/graphApplierFor
  in cmd/gc/class_store.go:205-234); its read expansion never applies to
  graph-class reads. (3) SQLiteStore filters tier='main' on default TierMode
  (internal/beads/sqlite_store.go:796-802, :892-898). All confirmed. BUT the
  failure scenario is unreachable because of which beads can ever be
  tier='wisp' in the graph store, versus which beads the unshielded consumer
  queries. tier='wisp' requires Ephemeral=true (sqlite_store.go:508-510),
  which only the wisp POLICY produces: kind=wisp roots
  (internal/formula/compile.go:355 stamps KindWisp only on root-only/vapor
  recipes, never on graph.v2 roots, which get KindWorkflow at :352) and
  children of a persisted-Ephemeral wisp root (policyForCreate first branch).
  The workflow policy can NEVER be ephemeral — compatibleBeadPolicyStorage
  (bead_policy_store.go:386-401) restricts it to no_history/history, and
  NoHistory rows are deliberately stored in SQLite's MAIN tier
  (CreateWithStorage in sqlite_store_storage.go; ApplyGraphPlanWithStorage;
  comment at sqlite_store.go:798 'NoHistory rows live in SQLite's main tier').
  The workflow-snapshot fallback only ever touches workflow-policy beads: the
  :134 logical-id sweep filters Metadata{gc.kind=workflow, gc.workflow_id=X} —
  a wisp root has gc.kind=wisp and can never match that filter ON ANY BACKEND
  (the deploy-side TierBoth expansion returns nothing extra for this query
  either); the :178 member List filters root_bead_id=<workflow root>, and
  every bead stamped with root_bead_id under a workflow root takes the
  workflow policy (isWorkflowPolicyMetadata treats non-empty root_bead_id as
  workflow; the wisp-inheritance branch fires only when the ROOT is a wisp,
  which isWorkflowRoot at :262 already excluded) → NoHistory → tier='main' →
  fully visible to the default-tier List. No code stamps gc:wisp labels onto
  workflow-rooted beads (grep: only classifier sites), attached root-only
  wisps have their kind=wisp REMOVED (privatizeAttachedRootOnlyWisp,
  internal/sling/sling.go:1575) so they land main-tier, and dispatch-created
  fan-out/retry/finalizer beads carry no wisp markers. So the raw store
  returns byte-identical results to a policy-wrapped store for every query the
  cited consumer issues. All actual wisp-tier readers pass TierBoth explicitly
  (graph_hook_ready.go:68, graph_hook_claim.go:52, wisp_gc.go selectors +
  collectExpiredBeadClosure, molecule cleanup WithBothTiers) and
  dispatch_control_ready.go:388-391 correctly maps includeEphemeral→TierBoth,
  mirroring bd's --include-ephemeral semantics. What remains is a latent-
  hazard note, not a current defect: any FUTURE default-tier List/Ready added
  against GraphBeadStore() that targets wisp-classified beads would silently
  diverge from single-store semantics, because the tier-expansion shield lives
  only on the work-store wrapper.

- (wrapper-capability-reads) Controller CachingStore + bead.* event store routing for the graph class
  Deploy mechanism confirmed at INTEG 2c74f8747 (CachingStore-wrapped
  cityInfraStore + InfraScopePrefix arm in beadEventConfiguredStoreLocked),
  and the local absence is real (no gcg arm at api_state.go:651-690, raw
  SQLiteStore from resolveGraphStore). But the failure scenario is unreachable
  on the local branch: (1) the triggering event source — win3's CLI gcg bead.*
  emission arm — is INTEG-only; no non-test local cmd/gc bd/graph path emits
  bead events (graph is event-silent by design, cmd_bd_graph_sqlite.go imports
  no events). (2) The single local gcg-subject emitter, announceClosedMolecule
  (BeadClosed, no payload), never reaches the store-probe loop because
  applyBeadEventToStores early-returns on empty payload
  (api_state.go:562-564). (3) Even for a hypothetical payload-carrying gcg
  event, 'wasted reads per event' is factually wrong: CachingStore.ApplyEvent
  does one JSON decode then rejects via the ownsBeadID prefix check
  (caching_store_events.go:28-30) — zero store reads, zero cache pollution,
  since every production cache carries the bd IDPrefix. (4) The 'no cached
  tier' cost model imports INTEG's economics: there the infra scope is
  bd/dolt-subprocess-backed ('reconciler reads regress to a bd-subprocess per
  read'); locally the graph store is an in-process embedded SQLite DB and
  HandlesFor degrades to direct reads (caching_store_handles.go:58-68), so a
  cache buys negligible latency while adding staleness risk. Net: no degraded
  behavior exists on the local branch today; the mechanism solves
  preconditions (subprocess-backed store, CLI event emission) the local
  architecture does not have.

## Confirmed covered (local mechanism differs from deploy but holds)

- (bd-cli-reads) bd ready / bd query ephemeral discovery of graph beads (thin-client routing)
  Local covers the paths that matter via a DIFFERENT mechanism:
  cmd/gc/graph_hook_ready.go:25-97 wraps the hook work-query runner with a
  fail-loud graph-store union (graphFederatedWorkQueryRunner +
  mergeGraphReadyIntoWorkQueryOutput with Ready(TierBoth) so wisp-tier rows
  are included, plus filterAttachBlockedByGraphRoot), and pool-demand probes
  are served by cmd/gc/graph_scale_demand.go. Shipped pro

- (bd-cli-reads) Plain by-id show federation with 404-vs-error discipline (incl. migrated legacy ids)
  cmd/gc/cmd_bd_show_fed.go:49-91 maybeRouteBdShowLocal serves plain `show
  <id> [--json]` from the per-class embedded stores with the root-loss 404-vs-
  error discipline spelled out (absent store file/row = genuine absence; store
  FAILURE surfaces as a distinct error, never absence — file header lines
  12-19), and additionally probes routed class stores for MIGRATED legacy ids
  (gc-*/mc-*) before falling

- (mol-workflow-liveness) Pool ready-demand probe over the graph store (worker/pool wake for ready graph work)
  Local covers this via a different mechanism: cmd/gc/graph_scale_demand.go
  appendGraphScaleTargets adds a graph-store probe target per pool template on
  routed cities (fail-visible per-template partial on routing error), wired
  into the demand pass at cmd/gc/build_desired_state.go:676 and :728. gc ready
  CLI does not exist locally (B36-only file), so no local counterpart is
  needed for it.

- (mol-workflow-liveness) Dispatcher root-scoped scope-check/finalize List over graph-resident molecules
  Covered by construction locally: control beads for gcg roots are dispatched
  against the routed graph store itself — cmd/gc/cmd_convoy_dispatch.go:442
  (openControlStoreAtForCity graph arm via routedGraphStoreFor) and :482, with
  the in-process fallback at cmd/gc/dispatch_control_ready.go:384;
  dispatch.ProcessControl (cmd_convoy_dispatch.go:257) then runs
  processScopeCheck's root-scoped Lists (intern

- (mol-workflow-liveness) Pack liveness-probe read shapes (`gc bd show <id> --json`) federated for graph/class ids; reaper workflow-root store-ref close gate
  Local maybeRouteBdShowLocal (cmd/gc/cmd_bd_show_fed.go:45-110, wired at
  cmd_bd.go:243) federates exactly `show <id> [--json]` — bdShowRoutable
  accepts the id + --json in any order, which is precisely the shape local
  probes use (internal/bootstrap/packs/core/assets/scripts/orphan-
  sweep.sh:181, spawn-storm-detect.sh:57,79); reserved-prefix ids that miss
  the class store are reported as genuine absenc

- (claimable-crashrecovery) Worker ready/routed-pool discovery federation over the graph store (incl. cross-store attach-block filter)
  Local covers this via a DIFFERENT mechanism:
  cmd/gc/graph_hook_ready.go:30-49 graphFederatedWorkQueryRunner wraps the
  hook's shell runner and unions st.Ready(TierBoth) rows into the JSON-array
  output fail-loud (installed at cmd_hook.go:457), and
  filterAttachBlockedByGraphRoot (graph_hook_ready.go:96-118) enforces the
  gc.attached_workflow_root block, mirroring integ's
  filterCrossStoreAttachBlocked.

- (claimable-crashrecovery) Reconciler pool-demand (count-form) federation over the graph store
  Local covers via a DIFFERENT mechanism: cmd/gc/graph_scale_demand.go:26-49
  appendGraphScaleTargets adds one graph-store Ready probe per distinct
  template, applied to both defaultScaleTargets and defaultNamedScaleTargets
  (build_desired_state.go:676, 728); a routing failure surfaces as a per-
  template partial rather than zero demand.

- (claimable-crashrecovery) Claim-time mutation routing for gcg ids (claim / stamp / continuation-list / continuation-assign)
  Local covers in-process: cmd/gc/graph_hook_claim.go:38-70
  graphRoutedHookClaimOps routes the same four seams directly to the embedded
  graph SQLiteStore for gcg- ids (installed at cmd_hook.go:457);
  ListContinuation is the routed READ (st.List ParentID/Label). Local's graph
  arm lacks integ's RecordSessionPointers override, but that is a session-
  class write (write-path-only, out of scope per mission)

- (sling-convoy-adoptpr) COVERED: hook claim-time mutations (claim/continuation/stamp) route gcg ids to the embedded graph store
  cmd/gc/graph_hook_claim.go:23-70 graphRoutedHookClaimOps: in-process
  st.Claim/List/Update against routedGraphStoreFor for gcg- ids, wired at
  cmd/gc/cmd_hook.go:457; explicitly the 'split-branch split_city_claim.go
  pattern, adapted in-process'.

- (sling-convoy-adoptpr) COVERED: worker ready-discovery federation + cross-store attach-block filter
  cmd/gc/graph_hook_ready.go:30-114: graphFederatedWorkQueryRunner unions
  st.Ready into the shell work-query output (fail-loud on graph read failure)
  and filterAttachBlockedByGraphRoot enforces the cross-store attach block
  (fail-loud on dangling root). Wired at cmd_hook.go:457. NOTE: covers the
  READY tier only — the in_progress crash-recovery tier is the separate
  missing finding above.

- (sling-convoy-adoptpr) COVERED: pool-demand probes federate to the graph store; control dispatch reads route to the graph store
  cmd/gc/graph_scale_demand.go:25-49 appendGraphScaleTargets (per-template
  graph demand target, fail-visible per-template partial), wired at
  build_desired_state.go:676+728; cmd/gc/dispatch_control_ready.go:384-397 in-
  process graph Ready fallback; cmd/gc/cmd_convoy_dispatch.go:436-447
  openControlStoreAtForCity routed arm and :481-493 findBeadAcrossStores gcg
  arm. Caveat: demand from ready UNASSIGNED

- (api-read-federation) By-id bead reads federate the graph store (gcg class-prefix arm, fail-loud errors)
  internal/api/handler_beads.go:166-207 beadStoresForID has the gcg arm
  (graph-first [graph, city]); all by-id handlers loop it
  (huma_handlers_beads.go:440,490,510,592,616,640,702,754); non-NotFound store
  errors return 500, never a silent skip to the next store
  (huma_handlers_beads.go:492-497) — the Group-D 'degraded leg, not false-
  complete' principle holds by a fail-loud mechanism.

- (api-read-federation) Workflow/convoy snapshot scan federates the graph store (workflowStores graph-first + SQL fast-path skip)
  internal/api/handler_convoy_dispatch.go:563-585 workflowStores() leads with
  a graph-first entry (ref 'graph:<city>') when
  GraphBeadStore()!=CityBeadStore, correctly resolved via
  api_state.go:1469-1472 resolveGraphStore(cs.cityBeadStore, cs.cfg,
  cs.cityPath,...) with the right cfg/cityPath; :646-661 workflowStoreByRef
  round-trips the graph ref; internal/api/convoy_sql.go:602-609
  workflowStorePath s

- (api-read-federation) HTTP atomic claim/release + ephemeral-wisp endpoints for the pure-HTTP bd shim
  Different architecture, same outcome: local claims/release and wisp/work-
  query discovery run IN-PROCESS in the gc binary — cmd/gc/graph_hook_claim.go
  + graph_hook_ready.go (hook claim ops, work-query runner union incl.
  gc.attached_workflow_root), cmd/gc/cmd_bd_graph_sqlite.go (in-process gcg
  close/update), cmd/gc/dispatch_control_ready.go (in-process control-ready
  scan). No local worker path consu

- (api-read-federation) StoreHealth /status recompute-thrash guard (cache TTL + coalescing)
  internal/api/store_health.go:17 storeHealthCacheTTL = 3 * time.Minute, plus
  singleflight coalescing of concurrent refreshes (store_health.go:20-31, from
  #4313) — strictly stronger than the b36 fix.

- (api-read-federation) Batched graph child fan-out + assignee session-name normalization on bead reads
  internal/api/handler_beads.go:444-480 collectBeadGraph walks BFS levels with
  one batched store.List(ParentIDs=...) per level (comment explicitly cites
  replacing the per-bead N+1); handler_beads.go:50-137
  beadListAssigneeTerms/normalizeRawBeadAssignee expand all session identity
  forms via session.AssigneeIdentities.

- (dispatch-doctor-status) Control-dispatcher built-in ready scan reads the graph store in-process
  cmd/gc/dispatch_control_ready.go:381-397 — routedGraphStoreFor in-process
  arm (fail-loud on a marked city), plus the short-TTL cache
  (dispatch_control_ready.go:331-353) primes a CachingStore over
  openControlStoreAtForCity, which itself routes to the graph store on a
  routed city (cmd/gc/cmd_convoy_dispatch.go:436-447). Different mechanism
  than integ (routed class store vs memoized infra store) but

- (dispatch-doctor-status) Wisp-GC sweep and pool ready-demand probes read the graph store
  Wisp GC: cmd/gc/city_runtime.go:1328 runs the sweep off cr.graphBeadStore(),
  which routes via resolveGraphStore/resolveGraphStoreRouted
  (cmd/gc/class_store.go:103-104, graph_class_store.go:127-138, fail-closed on
  a marked city). Ready demand: cmd/gc/graph_scale_demand.go:26-49
  appendGraphScaleTargets adds a graph-store probe per pool template, wired at
  build_desired_state.go:676/728, with routing

- (wrapper-capability-reads) Control-dispatcher readiness reads on the graph store
  Different mechanism, same coverage: cmd/gc/cmd_convoy_dispatch.go:436-446
  openControlStoreAtForCity returns the routed graph store for ALL control-
  plane scopes (fail-closed), so the whole internal/dispatch plane runs
  directly against the graph store; cmd/gc/dispatch_control_ready.go:330-353
  controlReadyCacheFor primes a CachingStore over that store (PrimeActive uses
  TierBoth, internal/beads/cachin

- (wrapper-capability-reads) Worker ready-discovery + claim-time graph reads
  Different mechanism: cmd/gc/graph_hook_ready.go:30-90 wraps the shell work-
  query runner to UNION st.Ready(TierBoth) rows (fail-loud on routing error)
  plus the gc.attached_workflow_root cross-store block filter (:97-114);
  cmd/gc/graph_hook_claim.go routes claim/continuation/stamp for gcg ids in-
  process with TierBoth continuation lists (:46-56). Residual note: local
  ships no `gc ready` composite com

- (wrapper-capability-reads) Pool-demand probes count graph-resident routed work
  Different mechanism: cmd/gc/graph_scale_demand.go appendGraphScaleTargets
  adds one unconditional graph-store demand target per template (wired at
  build_desired_state.go:676 and :728 for named sessions), sharing one store
  group; the demand reads go through readyForControllerDemandQuery with
  explicit TierBoth (build_desired_state.go:1803-1808) and HandlesFor's
  logical handles work correctly on the r

- (wrapper-capability-reads) By-id federation and autoclose/GC graph-root reads
  internal/api/handler_beads.go:166-196 beadStoresForID has the graph-first
  gcg arm; cmd/gc/cmd_convoy_dispatch.go:479-492 findBeadAcrossStores graph
  arm; cmd/gc/api_state.go:613-660 runBeadCloseAutoclose passes graphBeadStore
  into doWispAutocloseWith/doMoleculeAutocloseWith; the destructive subtree-
  terminal read is tier-safe on the raw store
  (internal/molecule/cleanup.go:38-59 uses IncludeClosed +
