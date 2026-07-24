# Graph-read gap analysis — ADDENDUM: heuristic sweep (2026-07-24)

Second pass: the 39 verified gaps were used as heuristics to sweep for
additional graph-blind codepaths (5 pattern finders, adversarial verify).
25 NEW confirmed gaps (4 refuted). Same severity scale.

### N00 [CRITICAL] Graph-class boot migration and residue sweep cover ONLY the city bd store — rig-store graph beads (in-flight rig-scope molecules, steps, control beads, synthetic convoys) are stranded invisible the moment the marker flips

- pattern: byid-mutations
- evidence:
  openGraphClassMigrationStore is hardwired to the city store:
  cmd/gc/graph_class_migrate.go:36-38 (openStoreAtForCity(cityPath,
  cityPath)); ensureGraphClassMigrated (:44-80) imports from that one store
  via importGraphSnapshot (:104-117, store.List over the single handle) and
  then writes the atomic marker; sweepLegacyGraphResidue (:187-234) opens the
  same single city store. The sole call site is cmd/gc/city_runtime.go:291-292
  — no per-rig loop. Yet pre-flip, rig-scope graph beads live in RIG bd
  stores: openControlStoreAtForCity's pre-flip rig arm controlBdStoreForRig
  (cmd/gc/cmd_convoy_dispatch.go:457-469), rig-store control dispatch
  (dispatchScopedControlBeads, :510), and the codebase's own examples of rig-
  scope workflows (internal/dispatch/runtime.go:820-823 'city-scope Adopt PR
  request that spawned a rig-scope mol-adopt-pr-v2 workflow'). Post-marker,
  EVERY graph read for every scope early-returns the embedded store
  (cmd/gc/cmd_convoy_dispatch.go:436-446 'regardless of the requesting
  scope'), so rig-resident roots/steps/finalizers are unreachable by all
  routed readers, and the residue sweep never deletes (or merge-imports) them
  either.
- scenario:
  City with rigs running in-flight rig-scope graph.v2 workflows (e.g. mol-
  adopt-pr-v2: root + steps + workflow-finalize poured into the rig's bd store
  via the pre-flip createTarget identity path) upgrades to
  [beads.classes.graph] backend=sqlite. Boot migration
  (ensureGraphClassMigrated) imports ONLY city-store graph beads, copy-
  verifies, writes .gc/store/graph.migrated, and routing flips. From that
  instant every routed reader — control dispatch, control-ready discovery,
  hook claim/ready, findBeadAcrossStores (whose rig loop itself opens the
  routed store) — reads only the embedded city-level graph store, so the rig-
  resident molecule is never served, its steps never claimed, its finalize
  never runs: permanent silent wedge. Unlike G35's city-store rogue-writer
  window, no later boot self-heals, because importGraphSnapshot and
  sweepLegacyGraphResidue re-open only the city store on every boot. The stale
  open copies also persist in the rig bd store indefinitely (never merge-
  imported, never deleted), surfacing as zombie ready/open work in rig-scoped
  bd list/ready and pack-custom bd queries.
- fix sketch:
  Make the boot migration and residue sweep iterate all graph-bead-bearing
  stores, not just the city store. Concretely: change
  openGraphClassMigrationStore (cmd/gc/graph_class_migrate.go) from a single-
  store opener into an enumerator returning the city store plus one handle per
  configured rig (loadCityConfig + resolveRigPaths(cfg.Rigs), opening each via
  the pre-flip bd arm — the controlBdStoreForRig/openStoreAtForCity path, NOT
  openControlStoreAtForCity, which would route to the embedded store post-
  marker). ensureGraphClassMigrated then runs importGraphSnapshot (reset once,
  then merge-import per store, within-store dep edges only, copy-verify)
  across every handle before writing the marker, and the straggler pass and
  sweepLegacyGraphResidue loop the same enumeration so post-marker rig-store
  pours from old binaries merge-import and the bd copies (city and rig) are
  deleted under the same open-grace rules. Keep per-store failure fail-closed
  for the marker write (any store's import failure aborts the flip) but per-
  store best-effort for the sweep. Add a test: rig bd store with an open rig-
  scope molecule (root + step + finalize + within-graph deps) migrates into
  the embedded store at flip and its rig-store residue is swept on the next
  boot.

### N01 [HIGH] Convergence engine is graph-blind: roots are created in the routed graph store but every read/write/index scan runs against the work store — gc converge hard-fails and all migrated loops silently stop

- pattern: byid-mutations
- evidence:
  coordclass classifies type=convergence as ClassGraph
  (internal/coordclass/classify.go:133-134, rationale :39-42,64-65). On a
  routed city the create side therefore lands in the embedded graph store:
  convergenceStoreAdapter.CreateConvergenceBead
  (cmd/gc/convergence_store.go:322-332) calls a.store.Create, a.store is
  cr.cityBeadStore()/rigBeadStores() (cmd/gc/convergence_tick.go:72-89,
  :129-142), which is the beadPolicyStore-wrapped store (cmd/gc/main.go:1437
  wrapStoreWithBeadPoliciesAt), whose Create dispatches by class through
  createTarget (cmd/gc/bead_policy_store.go:101-104) and routes ClassGraph to
  routedGraphStoreFor (cmd/gc/class_store.go:205-214) — minting a gcg root in
  .gc/store/graph/beads.sqlite. But EVERY other adapter operation goes to the
  embedded work store (the policy wrapper does not intercept reads/metadata
  writes — cmd/gc/bead_policy_store.go:50-56 comment, List at :106-109):
  populateIndex List(Type convergence) cmd/gc/convergence_store.go:40-59;
  GetBead/GetMetadata :72-94; SetMetadata :96; CloseBead :121; DeleteBead
  :138; Children :148; ActivateWisp Get/Update/Children :213-251;
  FindByIdempotencyKey :253-290; CountActiveConvergenceLoops :292-320. Call
  path for the hard failure: gc converge -> convergenceReqCh ->
  Handler.CreateHandler (internal/convergence/create.go:89
  CreateConvergenceBead succeeds in the graph store, then :107
  h.Store.SetMetadata(beadID, FieldState, StateCreating) hits the WORK store
  -> beads.ErrNotFound -> CreateResult error; the :101 rollback CloseBead also
  NotFounds). No convergence mention exists anywhere in the inventory (grep
  'convergence' over GRAPH-READ-GAP-ANALYSIS.md matches only unrelated wording
  at line 2012).
- scenario:
  On a graph-routed city (backend=sqlite + .gc/store/graph.migrated): (1)
  every `gc converge <formula>` request fails with "setting creating state:
  not found" — CreateConvergenceBead routes the type=convergence root through
  beadPolicyStore.Create → createTarget(ClassGraph) → routedGraphStoreFor into
  .gc/store/graph/beads.sqlite, but the very next call
  (internal/convergence/create.go:107 SetMetadata FieldState=creating)
  delegates through the un-routed embedded CachingStore(work store) and
  NotFounds; the rollback closeBead (SetMetadata+CloseBead) also targets the
  work store, leaving an open, unclosable gcg convergence root orphaned in the
  graph store on every attempt. (2) Convergence loops active at migration time
  are imported into the graph store by importGraphSnapshot (all open
  ClassGraph beads) and their bd-side copies deleted by
  sweepLegacyGraphResidue, so populateIndex's List(Type:"convergence") on the
  work store returns zero rows — the active index goes empty and every in-
  flight convergence loop silently freezes with no error.
- fix sketch:
  Give the convergence adapter a graph-class store arm mirroring the existing
  routed seams: in newConvergenceScope (cmd/gc/convergence_tick.go:129-142),
  resolve routedGraphStoreFor(cr.cityPath, cr.cfg) (per-rig: the rig scope
  root) and, when routed, construct convergenceStoreAdapter over the graph
  store for all root/wisp operations (Get/SetMetadata/Close/Delete/Children/Li
  st/FindByIdempotencyKey/CountActiveConvergenceLoops), falling back to
  scope.store when unrouted — creation already routes correctly via the policy
  wrapper, so pointing the adapter's reads/mutations at the same routed store
  restores read-your-writes. PourWisp's molecule.Cook should keep receiving
  the policy-wrapped store (its ApplyGraphPlan already class-routes).
  Alternatively (smaller blast radius, same shape as G37's cliGraphStore):
  thread a resolveGraphStore-backed store into the adapter only when the
  migrated marker exists. Add a routed-city integration test: create loop →
  SetMetadata succeeds → populateIndex finds it after simulated restart +
  residue sweep.

### N02 [HIGH] Durable session-wait dependency resolution has no graph arm: waits on gcg workflow roots cannot be created, and the controller wake pass DESTRUCTIVELY fails existing ones (FailWait + wait-hold cleared) on first tick

- pattern: byid-mutations
- evidence:
  Dep-bead reads fan over work/scope stores only. CLI create:
  cmd/gc/cmd_wait.go:275-277 wires loadWaitDependencyBead; :293-298 refuses
  the wait when the dep Get fails; loadWaitDependencyBead (:950-987) iterates
  convoyStoreCandidates(cfg, cityPath, depID) (cmd/gc/cmd_convoy.go:427-473 —
  rig-prefix dir + cityPath + rig dirs, no reserved-prefix/graph arm) and
  opens each via openStoreAtForCity — work stores. Controller wake path:
  city_runtime.go:2394 -> prepareWaitWakeStateForTick (:1682-1687) passes
  newWaitDependencyStoreSet(store, rigStores) (cmd/gc/cmd_wait.go:70-86 —
  city+rig work stores only) into prepareWaitWakeStateWithSnapshot;
  depsWaitReadyDetailedFrom (:910-948) Get()s each DepID; on ErrNotFound with
  mode 'all' it returns the error, and the wake pass at :1100-1110 then calls
  sessFront.FailWait(wait.ID,...) and clearSessionWaitHoldIfIdle — permanently
  failing the wait and releasing the sleeping session's wait_hold. Post-flip,
  workflow roots/steps are gcg-resident in the embedded graph store only
  (routed openControlStoreAtForCity, cmd/gc/cmd_convoy_dispatch.go:436-446),
  so any dep id naming a workflow root resolves ErrNotFound in every probed
  store. The comment at cmd_wait.go:1098-1099 ('Dependency beads are WORK
  class') documents the now-broken assumption.
- scenario:
  Graph-routed city. (a) An agent runs `gc session wait --on-beads <gcg
  workflow root or migrated foreign-id root> --sleep`: loadWaitDependencyBead
  probes only city+rig work stores, Get fails, and the wait is refused at
  creation ("dependency gcg-N: not found") — fail-loud, the wait-on-workflow-
  root pattern is simply unavailable. (b) Destructive arm: any durable wait
  whose dep_ids name a bead that is graph-resident post-flip (e.g. a wait on a
  workflow root created before the flip, or by an older binary) is dep-checked
  on the next controller tick against work stores only; ErrNotFound in mode
  "all" triggers sessFront.FailWait + clearSessionWaitHoldIfIdle — the wait is
  permanently terminal, the session's wait_hold is released, and the session
  never receives the completion signal even though the workflow is progressing
  normally in the graph store. Work-bead waits (the documented design usage
  per P4-SESSIONS-SEAM-PLAN.md:28) are unaffected, which is what bounds this
  below critical.
- fix sketch:
  Add a graph leg to both dep-read paths. (1) Controller tick: in
  prepareWaitWakeStateForTick (cmd/gc/city_runtime.go:1687), append the routed
  graph store (routedGraphStoreFor(cityPath, cfg), nil-safe on unrouted
  cities) to newWaitDependencyStoreSet — storeref.Resolve already iterates the
  set, so a graph tail slots in directly; alternatively give
  waitDependencyStoreSet an explicit reserved-prefix fast path via
  resolveGraphStore for gcg- ids plus a probe tail for migrated foreign-id
  roots (mirroring the findBeadAcrossStores gcg arm shape). (2) CLI create: in
  loadWaitDependencyBead (cmd/gc/cmd_wait.go:949), before/after the
  convoyStoreCandidates loop, probe the graph-class store (reserved-prefix
  route for gcg-, plus unconditional graph probe for foreign ids). (3)
  Hardening: make the wake pass distinguish "dep missing from an incomplete
  store set" from "dep genuinely gone" — at minimum only FailWait after the
  graph leg was actually consulted (fail-loud skip when the graph store fails
  to open), consistent with the fail-loud convention in G12/G14. Update the P4
  plan gotcha note. Tests: routed-city wait create on a gcg root succeeds;
  pre-existing wait on a migrated foreign-id root survives a tick and wakes on
  root close; unrouted city unchanged.

### N03 [HIGH] Convergence engine is split-brained: convergence beads deliberately classify ClassGraph (created into the graph store) but every engine/CLI read and mutation runs against the work store

- pattern: scan-unions
- evidence:
  internal/coordclass/classify.go:133-134 (`case beadType == typeConvergence:
  return ClassGraph`) with the comment at classify.go:38-42 calling this 'a
  deliberate decision — they are the convergence engine's execution state'.
  Create path: cmd/gc/convergence_store.go:322-331 CreateConvergenceBead calls
  a.store.Create({Type:"convergence"}); the adapter store is the controller's
  policy-wrapped city/rig store (cmd/gc/convergence_tick.go:72-88
  buildConvergenceScopes uses cr.cityBeadStore()/rigBeadStores();
  cityBeadStore is wrapStoreWithBeadPoliciesAt-wrapped via cmd/gc/main.go:1437
  and cmd/gc/api_state.go:272-281), so beadPolicyStore.Create
  (cmd/gc/bead_policy_store.go:101-103) routes through
  createTarget(coordclass.Classify(b)) which on a routed city returns
  routedGraphStoreFor (cmd/gc/class_store.go:205-215) — the bead is minted
  gcg-* in .gc/store/graph/beads.sqlite. Read/mutate path: the adapter's
  GetBead/GetMetadata/SetMetadata/Children/List all delegate to the same
  wrapped store, and beadPolicyStore overrides NO by-id read or mutation
  (method inventory at cmd/gc/bead_policy_store.go:56-241 — no
  Get/Update/SetMetadata override, per the same structural fact G18 records
  for order_dispatch), so they promote to the work store:
  convergence_store.go:41 populateIndex List(Type convergence),
  :149/:260/:278/:304 child+concurrency scans, :241 Children(id,
  IncludeClosed); startup reconcile convergence_tick.go:563 List(Type
  convergence) on scope.store; CLI cmd/gc/cmd_converge.go:334/:359
  openStoreAtForCity then :155-165 Get + type check, :294 List, :650 Children.
- scenario:
  On a graph-routed city, `gc converge create` (via the controller's
  convergence handler) creates the convergence root as gcg-* in the embedded
  graph store (createTarget routes ClassGraph), but the immediately following
  SetMetadata(FieldState=creating) — unoverridden on beadPolicyStore —
  executes against the work store and fails NotFound. The create errors out
  and the rollback (SetMetadata terminated + CloseBead, errors discarded) also
  targets the work store, so every attempt strands an orphaned in_progress gcg
  convergence bead in the graph store that no sweep owns. No loop can ever
  start on a routed city. Additionally, the scan plane is graph-blind:
  convergenceStartupReconcile and populateIndex List(Type=convergence) run
  against the work store, so any convergence loops migrated into the graph
  store from a pre-flip city are silently never recovered and invisible to `gc
  converge status|list` and concurrency limits. Net: the convergence subsystem
  is dead end-to-end on a routed city — fail-fast on new creates, silently
  abandoning migrated loops.
- fix sketch:
  Two-sided fix mirroring the existing routed seams: (1) engine plane — in
  buildConvergenceScopes / newConvergenceScope (cmd/gc/convergence_tick.go),
  resolve the routed graph store via resolveGraphStore(cityPath, cfg) and hand
  the convergence adapter that store (all convergence beads are ClassGraph by
  design, so the adapter can bind wholly to the graph store on a routed city;
  Create/Get/SetMetadata/List/Children then agree on one store). Wisp pours
  from PourWisp already classify ClassGraph so they stay consistent. (2) CLI
  plane — openConvergeStore / the per-rig openStoreAtForCity call sites in
  cmd_converge.go grow the same routed arm used by cmd_bd_show_fed.go: by-id
  reads (status/test-gate) promote gcg-* ids to the graph store (or use
  findBeadAcrossStores), and `converge list` unions List(Type=convergence)
  across work + graph stores. Add a regression test on a marker+sqlite-config
  city asserting converge create → status → list round-trips and that startup
  reconcile sees a pre-existing graph-store convergence bead.

### N04 [HIGH] gc formula cook --attach (graph v2) idempotency, conflict, and failed-root-cleanup reads run against the work store while the pour routes to the graph store — duplicate live workflows per re-cook; attach dep edge minted cross-store

- pattern: scan-unions
- evidence:
  cmd/gc/cmd_formula.go:643 store = openStoreAtForCity(scope.storeRoot,
  cityPath) → policy-wrapped with cityPath (cmd/gc/main.go:1437). The pour at
  cmd_formula.go:712 molecule.Instantiate routes into the graph store via the
  create-side seam (beadPolicyGraphStore.graphApplierFor,
  cmd/gc/class_store.go:218-234). But every guard around it reads the SAME
  policy store whose List/ListByMetadata forward to the work store
  (bead_policy_store.go:106-227 expand tier only, no graph arm):
  closeFormulaCookFailedGraphV2Roots (call :680, impl :1000-1021,
  ListByMetadata Graphv2RootKey), existingFormulaCookGraphV2Root (call :683,
  impl :1023-1055, ListByMetadata Graphv2RootKey limit 2),
  formulaCookLiveInputConvoyGraphRoots (call :691, impl :971-998,
  ListByMetadata gc.input_convoy_id), sourceworkflow.ListLiveRoots (call
  :699). ensureFormulaCookAttachDep (calls :704/:725, impl :951-969) runs
  store.Deps/store.DepAdd(attach, gcg-root, "blocks") — beadPolicyStore has no
  DepAdd override, so the blocks edge lands in the work store where the gcg
  root does not exist. Also `gc formula` hash-verify at :1208-1216
  store.Get(beadID) fails NotFound for gcg roots.
- scenario:
  On a graph-routed city, an agent re-runs gc formula cook &lt;f&gt; --attach
  &lt;bead&gt; on a v2 formula (retry after transient failure, crash replay,
  order re-tick). existingFormulaCookGraphV2Root's
  ListByMetadata(Graphv2RootKey) runs against the work store and finds nothing
  (the live root is in the graph store); formulaCookLiveInputConvoyGraphRoots
  and sourceworkflow.ListLiveRoots are equally blind, so no conflict is
  raised; molecule.Instantiate then pours a SECOND live gcg workflow with the
  identical gc.graphv2_root_key into the graph store (ApplyGraphPlan does not
  dedupe on idempotency_key — it is stamped as metadata only), and both
  molecules' steps execute concurrently through the graph-aware ready seams.
  Additionally, closeFormulaCookFailedGraphV2Roots can never find a failed
  prior root (work-store ListByMetadata), so failed-instantiate residue
  accumulates in the graph store forever, and gc formula verify store.Get(gcg-
  root) fails NotFound. NOT part of the failure (refuted): the attach 'blocks'
  edge — ensureFormulaCookAttachDep detects the gcg- prefix and stamps
  gc.attached_workflow_root metadata on the work-store attach bead, which
  filterAttachBlockedByGraphRoot enforces, so the attach cannot-close contract
  holds.
- fix sketch:
  Mirror the G22 sling-lane treatment in cmd/gc/cmd_formula.go's graph v2 cook
  arm: resolve the routed graph store once (routedGraphStoreFor(cityPath,
  cfg)) inside the WithLock body and run the four guards against it (or a two-
  leg federated reader when unrouted falls back to the work store):
  existingFormulaCookGraphV2Root and closeFormulaCookFailedGraphV2Roots query
  Graphv2RootKey in the graph store; formulaCookLiveInputConvoyGraphRoots
  queries gc.input_convoy_id there; sourceworkflow.ListLiveRoots gains a
  graph-store leg (or accept a second store param like the sling copy). Fail
  loud if the graph store is unavailable rather than silently falling back to
  the work store. Separately, route the gc formula verify hash-check Get for
  reserved gcg- ids through the existing findBeadAcrossStores gcg arm. Add a
  graph-routed-city test: cook --attach twice, assert the second returns the
  existing root (no duplicate pour) and that a MoleculeFailed root is swept.

### N05 [HIGH] gc convoy status cannot resolve graph-resident synthetic input convoys — every core-pack molecule formula gates its first step on it

- pattern: exec-bd-shells
- evidence:
  Consumers: internal/bootstrap/packs/core/formulas/mol-polecat-
  base.toml:26-27 and :73-77 (`CONVOY_STATUS=$(gc convoy status {{convoy_id}}
  --json)` then `exit 1` when WORK_BEAD_ID cannot be derived), plus the same
  read in mol-do-work.toml:36, mol-scoped-work.toml:54/:93/:199, mol-prompt-
  synth.toml:65/:135, mol-polecat-report.toml:52/:146. On a graph-routed city
  the sling input convoy is a synthetic type=convoy bead classified ClassGraph
  (internal/coordclass/classify.go:61-63 'ClassGraph for synthetic convoys
  (gc.synthetic...) so graph.v2 input convoys... travel with the graph') and
  minted gcg-* in the embedded store (create-side routing acknowledged in the
  inventory's own aside: 'beadPolicyStore: Create routes synthetic convoys to
  the graph store via createTarget'). Call path 1 (API):
  cmd/gc/cmd_convoy.go:842-882 routeConvoyStatus -> c.GetConvoy ->
  internal/api/huma_handlers_convoys.go:137-162 humaHandleConvoyGet — the only
  graph divert is isGraphConvoyID (internal/api/handler_convoys.go:8-24),
  which requires isGraphConvoyBead == isWorkflowRoot
  (internal/api/handler_convoy_dispatch.go:268-270); a synthetic convoy is NOT
  a workflow root, so the handler falls to s.state.BeadStores() (work stores
  only) and 404s. Call path 2 (fallback, also taken when the controller is
  down): doConvoyStatusFallback (cmd_convoy.go:938-944) ->
  openConvoyStoreByIDAt (cmd_convoy.go:714-730) ->
  resolveConvoyStore/resolveOwningStoreDir (cmd_convoy.go:514-560) probing
  only convoyStoreCandidates (cmd_convoy.go:427-483: rig dirs + cityPath)
  opened via openStoreAtForCity (cmd/gc/main.go:1334) — no routedGraphStoreFor
  arm anywhere, so Get(gcg-*) is ErrNotFound in every candidate and the
  command exits 1.
- scenario:
  Graph-routed city ([beads.classes.graph] backend=sqlite +
  .gc/store/graph.migrated) slings any core-pack molecule formula (mol-
  polecat-base, mol-do-work, mol-scoped-work, mol-prompt-synth, mol-polecat-
  report) onto a NON-convoy target — the default single-bead dispatch mode.
  NormalizeInputConvoy mints a synthetic gc.synthetic=true input convoy that
  beadPolicyStore.Create routes to the embedded graph store as gcg-*. The
  worker's load-context step runs `gc convoy status <gcg-id> --json`: the API
  route 404s (isGraphConvoyID only diverts gc.kind=workflow roots; the handler
  then scans work stores only) and the local fallback's candidate set (rig
  dirs + cityPath via openStoreAtForCity) has no graph arm, so both exit 1.
  CONVOY_STATUS is empty, WORK_BEAD_ID derivation fails, and the step exits 1
  — every bead-targeted run of every convoy-driven core-pack formula fails at
  its first step (and mol-polecat-report at completion). Runs slung on a pre-
  existing user convoy are unaffected (that convoy is ClassWork/gc- and
  resolves normally). Note: because TrackItem's DepAdd is also unrouted
  (tracks edge lands in the work store), routing the by-id Get alone still
  yields 0 children and the "exactly one tracked member" guard still fails —
  the two defects compound.
- fix sketch:
  Two coordinated arms. (1) CLI fallback: in cmd/gc/cmd_convoy.go, give the
  by-id resolution a reserved-class fast path — when
  reservedClassForBeadID(convoyID) is the graph class on a routed city,
  resolve via routedGraphStoreFor(cityPath, cfg) (fail-closed, no work-store
  fallback) before/instead of the rig+city candidate scan in
  openConvoyStoreByIDAt/resolveOwningStoreDir; alternatively append a graph-
  store candidate to openConvoyStores so ambiguity detection still applies.
  (2) API: in internal/api/huma_handlers_convoys.go humaHandleConvoyGet (and
  ConvoyCheck), after the isGraphConvoyID workflow divert, add a
  GraphBeadStore().Store arm for plain type=convoy beads (when graph store !=
  city store): Get the bead there and serve the same convoyGetResponse shape.
  (3) Members: because the tracks edge may reside in the work store (unrouted
  DepAdd), thread a memberStoreComplement-style probe set (G31's helper,
  internal/convoy.Members already variadic) into the children listing on both
  the API arm and the CLI's listConvoyChildren for graph-store convoys — [city
  + rig work stores] complement; conversely route TrackItem/DepAdd for graph-
  class convoy edges so new edges co-reside (write-lane fix, cross-ref the
  inventory's landmine #7 note). Test: two-store city, sling a formula onto a
  single work bead, assert `gc convoy status <gcg-convoy> --json` (API up and
  down) returns the convoy with exactly one child so the mol-* WORK_BEAD_ID
  derivation succeeds.

### N06 [HIGH] Graph-plane CAS fencing (control epochs, drain reservations, attach fences) silently degrades to unfenced writes on the routed graph store — conditional_writes=require is silently voided

- pattern: store-identity-caps
- evidence:
  The routed graph store is a raw *beads.SQLiteStore:
  cmd/gc/graph_class_store.go:92 opens it with only WithSQLiteStoreIDPrefix,
  and openControlStoreAtForCity returns it unwrapped on a routed city
  (cmd/gc/cmd_convoy_dispatch.go:436-446), flowing into dispatch.Process via
  runControlDispatcherWithStoreAndConfig
  (cmd/gc/cmd_convoy_dispatch.go:157-166, 173-189). SQLiteStore implements NO
  ConditionalWriter method (grep over internal/beads/sqlite_store*.go: no
  UpdateIfMatch/CloseIfMatch/DeleteIfMatch/CompareAndSetMetadataKey) and does
  NOT embed condWritesStamp (internal/beads/sqlite_store.go:99-111; the stamp
  is embedded only by MemStore internal/beads/memstore.go:17, NativeDoltStore
  native_dolt_store.go:291, BdStore bdstore.go:319).
  beads.ResolveConditionalWriter therefore hits ModeUnset and returns (nil,
  nil, nil) with no diagnostic and no degrade event
  (internal/beads/conditional_writes_resolve.go:264-271 — the mode gate short-
  circuits BEFORE the capability probe/refuseOrDegrade machinery). Every
  writer==nil fallback then runs unfenced: syncControlEpochToAttempt does
  plain store.SetMetadata on the control bead
  (internal/dispatch/control.go:336-343); claimDrainReservation does plain
  SetMetadata of the exclusive-drain owner for graph-class members whose
  owning store is the graph store (internal/dispatch/drain.go:1264-1272,
  reserveDrainMember at 1233-1255); releaseDrainReservation takes the read-
  check-clear path (drain.go:1360-1379); advanceAttachEpochIfNeeded and
  advanceAttachEpochFence do unconditional SetMetadata
  (internal/molecule/molecule.go:559-579, 634-641); claimAttachCandidate falls
  back to the 'legacy racy read-check-set' (molecule.go:394-408). On an
  UNROUTED city the same paths run against the factory-stamped
  CachingStore(BdStore/NativeDoltStore) with real value-CAS, so the flip
  silently removes fencing from the entire graph/control plane.
- scenario:
  City sets beads.conditional_writes=require (fail-closed: never fall back to
  unconditional writes) and flips [beads.classes.graph] backend=sqlite with
  the migrated marker. All control-plane fencing on graph-resident beads
  silently reverts to the pre-CAS racy paths: two racing control-dispatcher
  invocations both pass claimDrainReservation's read-check-set on a graph-
  resident exclusive-drain member (both observe empty owner, both SetMetadata
  their own control.ID) — double exclusive-drain dispatch; concurrent attach
  processors both pass claimAttachCandidate's legacy read-check-set and the
  epoch fence meant to neutralize the loser is itself an unconditional
  SetMetadata, so duplicate sub-DAGs for one idempotency key both run;
  control-epoch sync (syncControlEpochToAttempt) writes unfenced. No require
  refusal, no beads.conditional_writes.degraded event (even auto mode's loud-
  degrade is suppressed — the ModeUnset gate short-circuits before the probe),
  no preflight ERROR (preflight only probes city/rig stores, and an unstamped
  store resolves cleanly). Minor narrowing vs original: drain members that are
  work beads still resolve to the stamped work store and stay fenced; the
  unfenced drain leg covers graph-resident members via
  drainMemberOwningStore's ambient fallback. Control-epoch and attach fences
  are unconditionally unfenced on a routed city.
- fix sketch:
  Two complementary pieces. (1) Stamp the routed graph store: thread the
  resolved conditional-writes flags into the graph open path
  (graphClassStoreFor/routedGraphStoreFor already receive cfg via
  routedGraphStoreFor's callers; add a
  beads.WithSQLiteStoreConditionalWrites(mode, onDegraded) option or route the
  open through the same factory stamping openControlBdStoreThroughFactory
  uses), so require fails closed with the typed ConditionalWritesRequiredError
  and auto fires the once-latched degrade event with the already-reserved
  'sqlite-graph' store kind (store_rollout.go:105). Also add the routed graph
  store to preflightConditionalWrites so require surfaces at boot. (2)
  Implement ConditionalWriter on SQLiteStore —
  UpdateIfMatch/CloseIfMatch/DeleteIfMatch as single-statement UPDATE ...
  WHERE id=? AND revision=? and CompareAndSetMetadataKey as a transactional
  read-modify-write (or json_set with a WHERE on the current value), which is
  trivial real value-CAS in SQLite — then embed condWritesStamp so
  ResolveConditionalWriter resolves capable and every existing
  dispatch/molecule seam gets true fencing with no caller changes. (2) alone
  restores the guarantee; (1) alone restores the contract's failure signal;
  ship (2) with the stamp for full parity with the unrouted
  CachingStore(BdStore/NativeDoltStore) path.

### N07 [HIGH] Graph store inherits the 4-hour terminal-retention sweeper — closed steps/item-roots of still-running workflows are purged, breaking drain idempotency and closed-child counting for runs longer than 4h

- pattern: store-identity-caps
- evidence:
  graphClassStoreFor opens the store with only WithSQLiteStoreIDPrefix
  (cmd/gc/graph_class_store.go:92); WithSQLiteStoreRetention is never called
  anywhere in production code (grep cmd/ + internal/: only the definition at
  internal/beads/sqlite_store.go:60-66), so the graph store runs the default
  sweeper: sqliteDefaultRetentionPeriod = 4h, sweep every 30s
  (internal/beads/sqlite_store.go:24-25, 117-118, startRetentionSweeper
  1081-1105). purgeTerminal deletes ALL tier='main' beads with status
  closed/cancelled/expired older than 4h (sqlite_store.go:1107-1140) — closed
  steps, closed item roots, closed synthetic convoys, closed molecule roots.
  Graph-plane consumers that read CLOSED graph beads as coordination state:
  ensureDrainItemRoot's idempotency lookup ListByMetadata(ItemRootKey,
  IncludeClosed) (internal/dispatch/drain.go:1015-1030) — a purged closed item
  root makes the level-triggered drain re-create and re-dispatch the item;
  listByWorkflowRoot / listByWorkflowRootAndScope with IncludeClosed:true
  (internal/dispatch/runtime.go:534-541, 1433-1437) feed scope snapshots and
  closed-child counting for tally/finalize on the still-open root;
  sourceWorkflowChildSources IncludeClosed (runtime.go:1077-1082); wisp-GC
  closure collection ListByMetadata(RootBeadID, IncludeClosed)
  (internal/molecule/cleanup.go:38). On the bd/Dolt store closed graph beads
  persisted indefinitely, so none of these consumers guard against closed
  state vanishing mid-run. The graph ADR (git show
  09830032e:engdocs/design/graph-store-backend-selection.md:103) lists the
  sweeper as a store feature but never reconciles the 4h default against these
  closed-read contracts, and no retention/purge entry exists in the gap
  inventory.
- scenario:
  On a graph-routed city the controller keeps the graph SQLite store open for
  days; every 30s the default sweeper purges tier='main' beads closed >4h ago.
  (1) Drain wedge: a drain over a large convoy whose first item root closes at
  hour 1 while the last items finish after hour 5 — the closed item root is
  purged; when completeDrain runs, store.Get(row.ItemRootID) returns not-found
  and the drain control bead errors on every dispatch tick, permanently unable
  to close; the parent workflow stalls forever. (Silent re-creation via
  ensureDrainItemRoot only occurs in the narrow window before the manifest row
  records ItemRootID — e.g., manifest loss/crash during expansion — not in the
  normal path, because the persisted manifest short-circuits the idempotency
  lookup.) (2) Silent miscount: a fan-out/scope on a still-open root whose
  children close over a >4h window — early closed children are purged before
  tally/finalize/scope-check evaluates; listByWorkflowRootAndScope
  (IncludeClosed:true) returns fewer beads than were created, so closed-child
  counts shrink and finalize/scope-check stalls or misjudges outcomes,
  silently, since a purged row is indistinguishable from never-created.
- fix sketch:
  In graphClassStoreFor (cmd/gc/graph_class_store.go), open the graph class
  store with retention explicitly configured instead of inheriting the 4h
  default: either disable the sweeper (beads.WithSQLiteStoreRetention(0, 0))
  to match the bd/Dolt indefinite-persistence contract, or set a long config-
  driven period (days) — and, if bounded retention is truly wanted, make
  purgeTerminal graph-safe by only purging terminal beads whose workflow root
  (gc.root_bead_id) is itself terminal or absent. Add a regression test that
  closes a step/item root under a still-open root, runs purgeTerminal past the
  cutoff, and asserts the closed bead survives (or that
  completeDrain/listByWorkflowRootAndScope still see it). Record the decision
  in the graph ADR's feature table, which currently marks the sweeper 'done'
  without analyzing the closed-read consumers.

### N08 [HIGH] Migrated legacy-id graph beads are unclaimable and immutable through the prefix-gated worker seams: hook-claim and the doBd mutation arm route only gcg-* ids, while the ready union happily surfaces the imported beads

- pattern: lifecycle-migration-edges
- evidence:
  The boot migration preserves foreign ids (graph_class_migrate.go:123
  class.CreateWithForeignID(b)) — pre-flip graph beads were minted by the bd
  store with the work prefix (pre-flip createTarget is identity:
  cmd/gc/class_store.go:205-213 routedGraphStoreFor not routed), so the graph
  store post-flip holds non-gcg ids for every in-flight workflow. Discovery
  surfaces them: cmd/gc/graph_hook_ready.go:68
  mergeGraphReadyIntoWorkQueryOutput unions st.Ready(TierBoth) with NO prefix
  filter. But every worker mutation seam is prefix-gated:
  cmd/gc/graph_hook_claim.go:24-26 graphHookClaimStore returns ok=false unless
  strings.HasPrefix(beadID, "gcg-"), so Claim/AssignContinuation/stamp fall
  through to hookClaimWithBdStore (graph_hook_claim.go:44) — a bd subprocess
  against the WORK store; and cmd/gc/cmd_bd_graph_sqlite.go:46-49 gates the
  doBd close/update arm on the gcg prefix, so `gc bd close/update <legacy-id>`
  also goes to bd. Meanwhile sweepLegacyGraphResidue
  (graph_class_migrate.go:218-231) deletes class-owned open bd copies
  immediately after boot (class.Get succeeds -> no grace), so the bd target of
  those fallthrough mutations no longer exists. The asymmetry is proven in-
  tree: the SHOW path grew a legacy-id probe (cmd/gc/cmd_bd_show_fed.go:19-21
  and :68 'Legacy-id probe over the routed classes'), and G26 documents the
  sling-side foreign-id variant — the claim/mutation arms got no such probe.
- scenario:
  City flips with in-flight workflows. Open molecule roots/steps/wisps import
  into the embedded graph store keeping their bd ids (e.g. ga-1234,
  <hq>-wisp-<hash>); the boot residue sweep deletes the class-owned open bd
  copies immediately (no grace when class.Get hits). Worker discovery
  (graphFederatedWorkQueryRunner) surfaces ga-1234 as ready, but
  graphHookClaimStore says not-mine (no gcg- prefix), so
  Claim/AssignContinuation/StampWorkMeta fall through to the bd-CLI store
  against the work store and fail NotFound; the candidate is skipped with
  claims_errored, the graph row stays ready, and graph_scale_demand keeps
  waking pool workers onto it — a permanent claim-fail loop for every step in
  flight at the flip. `gc bd close/update ga-1234` likewise bypasses the gcg-
  gated in-process arm and fails against bd, so the beads are immutable
  through every CLI seam (only the read-only show path has a legacy-id probe).
  Narrow boot-window variant: before the sweep goroutine finishes, a claim
  lands on the doomed bd residue copy, the sweep deletes it, and the still-
  open graph copy re-dispatches — double execution with lost stamps.
- fix sketch:
  Mirror cmd_bd_show_fed.go's legacy-id probe discipline on the mutation
  seams, keyed on store membership instead of id prefix. (a) In
  graphHookClaimStore (cmd/gc/graph_hook_claim.go): when the id lacks the gcg-
  prefix but routedGraphStoreFor reports routed, probe st.Get(beadID) —
  ErrNotFound falls through to the bd default (byte-identical for genuine work
  ids), a hit routes in-process, and a store failure fails loud (never falls
  to bd, which would misread a migrated bead as absent). Apply the same
  membership probe for ListContinuation's rootID. (b) In
  maybeRouteBdGraphSqliteMutation (cmd/gc/cmd_bd_graph_sqlite.go): when
  sawGraphID is false on a routed city, probe the positional ids against the
  graph store and take ownership when all ids are store-owned; relax
  requireAllGraphIDs to accept store-owned legacy ids (keep the mixed-set
  refusal). Do NOT rewrite ids to gcg-* at import —
  molecule_id/attached_workflow_root metadata on work-store beads reference
  the original ids. Tests: routed city with a CreateWithForeignID-imported
  step (bd copy swept) → hook --claim claims it in-process and gc bd close
  closes it; unrouted city and unknown legacy id keep byte-identical bd
  passthrough. Same membership-probe treatment for the future G23 release arm.

### N09 [HIGH] Rig-scope bd stores are excluded from the boot migration and residue sweep, while post-flip control dispatch collapses every scope to the city graph store — in-flight rig-scoped workflows are stranded with their control beads never served again

- pattern: lifecycle-migration-edges
- evidence:
  Migration and sweep open ONLY the city store:
  cmd/gc/graph_class_migrate.go:36-38 openGraphClassMigrationStore(cityPath) =
  openStoreAtForCity(cityPath, cityPath), and sweepLegacyGraphResidue:200 uses
  the same opener — no rig iteration anywhere in the file. But pre-flip, graph
  beads are poured into the SCOPE store: createTarget is identity when routing
  is inactive (cmd/gc/class_store.go:205-213), and rig-scoped orders/slings
  open the rig store (cmd/gc/order_dispatch.go:453-455
  openStoreAtForCity(target.ScopeRoot); manual path cmd/gc/cmd_bd.go:120), so
  rig-scoped molecule roots, steps, and control beads live in the rig's own bd
  store. Post-flip, openControlStoreAtForCity
  (cmd/gc/cmd_convoy_dispatch.go:436-446) early-returns the ONE city graph
  store for EVERY storePath before the scope resolution — including the per-
  rig control serve loops at cmd_convoy_dispatch.go:367 and :510 — so the
  dispatcher never opens a rig bd store again. runtime.go:820-825's own
  comment establishes rig-scope workflows as the normal adopt-pr shape ('city-
  scope Adopt PR request that spawned a rig-scope mol-adopt-pr-v2 workflow').
  Side effect of the same scope-blind collapse: findBeadAcrossStores' rig loop
  (cmd_convoy_dispatch.go:509-514) now probes the graph store N times instead
  of the rig stores, so manual `gc convoy dispatch <rig-work-bead>` (caller at
  :132) can no longer resolve ANY rig-store bead.
- scenario:
  A city whose rigs have their own bd stores runs rig-scoped workflows (e.g.
  mol-adopt-pr-v2 spawned from a city-scope source, per runtime.go's own
  comment) and flips [beads.classes.graph] backend=sqlite mid-flight. Boot
  migration imports open graph beads ONLY from the city bd store
  (openGraphClassMigrationStore = openStoreAtForCity(cityPath, cityPath)); the
  marker flips anyway. From the next tick every control-dispatch read for
  every scope — per-rig serve loop (cmd_convoy_dispatch.go:367), control-ready
  cache (dispatch_control_ready.go:339), rig: storeRef resolver — goes through
  openControlStoreAtForCity, which early-returns the one city graph store, so
  the rig stores' in-flight molecule roots, steps, and
  retry/check/scope/finalize control beads are never read again: no retry
  fires, no finalize closes the root or the city-scope source chain, and every
  in-flight rig-scoped workflow wedges permanently and silently. Unlike the
  G35 city-store case there is no restart self-heal: sweepLegacyGraphResidue
  also opens only the city store, so the rig copies are never merge-imported
  nor swept and sit as live-looking open work indefinitely. Manual recovery is
  also degraded: findBeadAcrossStores' rig loop (:510) now opens the graph
  store N times instead of the rig stores, so `gc convoy dispatch <id>` cannot
  resolve any rig-store bead. Blast radius is bounded to workflows in flight
  at flip time (post-flip creates route correctly via createTarget), and the
  stranded beads remain intact and recoverable in principle, which is why this
  is high rather than critical.
- fix sketch:
  Extend the migration and sweep to every bd scope store, mirroring the read-
  side rig fan-outs: in cmd/gc/graph_class_migrate.go, have
  ensureGraphClassMigrated and sweepLegacyGraphResidue enumerate the city
  store plus each configured rig store (loadCityConfig + resolveRigPaths, open
  via openStoreAtForCity(rig.Path, cityPath), skipping rigs that share the
  city store / tolerating open failures as skips before the marker write —
  fail the flip, not silently narrow it). importGraphSnapshot already takes a
  single store and within-graph dep edges are per-molecule, so per-store
  invocation composes; keep openGraphClassMigrationStore as the test seam but
  make it return the store list. Independently, fix findBeadAcrossStores' rig
  loop (cmd_convoy_dispatch.go:510) to open the raw rig work store
  (openStoreAtForCity) instead of openControlStoreAtForCity so non-graph rig-
  store beads stay manually resolvable on a routed city (dovetails with G35's
  fix sketch (1)). Add a migration test: rig store holding an open rig-
  prefixed molecule root + control bead pre-flip, assert both are imported
  into the class store and swept from the rig store, and that the per-rig
  serve loop processes the control post-flip.

### N10 [HIGH] 4h terminal retention on the graph store deletes closed item roots out from under long-running drains — the completion pass Gets each closed root by id and wedges the drain forever; drains straddling the flip wedge immediately because closed roots never migrated

- pattern: lifecycle-migration-edges
- evidence:
  The graph class store opens with default retention
  (cmd/gc/graph_class_store.go:91 OpenSQLiteStore with only
  WithSQLiteStoreIDPrefix): internal/beads/sqlite_store.go:24
  sqliteDefaultRetentionPeriod = 4h, :117-118 defaults applied, :1107-1141
  purgeTerminal deletes ANY closed row older than 4h with no subtree-liveness
  or referencing-manifest check, and Delete (:995-1014) also removes its dep
  edges. The drain completion pass (control dispatcher store = the graph store
  post-flip, cmd_convoy_dispatch.go:442-445) does `root, err :=
  store.Get(row.ItemRootID)` for EVERY manifest row on every tick
  (internal/dispatch/drain.go:422-426 and shared-drain :497) and returns the
  error un-special-cased — only status!=closed maps to ErrControlPending;
  ErrNotFound errors the whole control. Item roots are graph-class molecule
  roots minted in the graph store. Pre-flip variant: importGraphSnapshot skips
  closed beads (graph_class_migrate.go:112) and the sweep deletes closed bd
  copies unconditionally (:222-227), so a drain whose manifest references an
  already-closed item root loses that root the moment routing flips — the
  migration header's premise 'nothing replays closed molecule topology'
  (graph_class_migrate.go:12-14) is false for drains.
- scenario:
  A drain fans out N item workflows on a graph-routed city; the first item
  root closes at hour 0 while others run long (normal maintainer-city
  profile). At hour ~4 the graph store's retention sweeper (purgeTerminal, 4h
  default, no reference check) deletes the closed root and its dep edges. On
  the next dispatch tick that reaches the drain's completion (or shared-
  advance) pass, store.Get(row.ItemRootID) returns ErrNotFound; the error is
  neither ErrControlPending nor transient, so cmd_convoy_dispatch.go
  quarantines the drain immediately — closed with outcome=fail and gc:control-
  quarantined — discarding every completed item's outcome
  (row.OutcomeBead/OutcomeKind are read from the now-purged roots and were
  never persisted incrementally on the fan-out path). Migration-edge variant:
  any drain in flight at the flip with >=1 already-closed item root hits the
  same NotFound→quarantine on its FIRST post-flip tick, because
  importGraphSnapshot skips closed beads and routed reads never fall back to
  bd.
- fix sketch:
  Three complementary layers: (a) open the graph class store with retention
  tuned for graph lifetimes — graphClassStoreFor passes
  beads.WithSQLiteStoreRetention(period, sweep) with a much longer period, or
  teach purgeTerminal a liveness guard (skip closed beads that are dep targets
  of open beads, or whose parent/owning control bead is open — the deps table
  is already in the same DB, so a NOT EXISTS join against open referrers is
  cheap); (b) make the drain completion pass purge-tolerant: persist row
  outcomes into the manifest incrementally as each root is observed closed
  (the shared path already persists on wait; the fan-out path should too) and
  skip the Get for rows whose outcome is already recorded, so a later purge of
  that root is harmless; optionally map ErrNotFound on an unrecorded row to a
  recorded failed row instead of a control error; (c) migration edge:
  importGraphSnapshot should also import closed graph beads still referenced
  by an open drain manifest / open control bead's dep edges (or, more simply,
  closed beads whose updated_at is within the retention window), and the
  residue sweep should spare closed bd copies referenced by open drain
  manifests until (b) lands.

### N11 [HIGH] Workflow-finalize outcome vote silently flips to PASS when failed closed steps vanish: the migration neither imports closed steps nor their dep edges, and post-flip the 4h retention purges failed closed steps plus the very edges that carry their fail votes

- pattern: lifecycle-migration-edges
- evidence:
  Finalize computes the workflow outcome from its blocking deps:
  internal/dispatch/runtime.go:1567-1588 resolveBlockedOutcome iterates
  store.DepList(finalizer,"down") and only a blocker Get with
  beadOutcomeFailed flips the vote to fail; the backstop scan
  workflowRootHasTerminalAbortScopeFailure (:1591-1605) lists by root
  metadata. Both legs go blind two ways. (a) Migration edge:
  importGraphSnapshot's graphIDs contains only OPEN graph beads
  (graph_class_migrate.go:110-117) and the edge loop drops any dep whose
  target is not in graphIDs (:136-138 `if !graphIDs[d.DependsOnID] { continue
  }`), so a finalizer's edges to already-closed steps — including a closed
  outcome=fail on_fail=continue step — are not re-added, and the closed step
  itself is never imported (its bd copy is then swept, :222-227). (b)
  Retention edge: purgeTerminal (sqlite_store.go:1107-1141) deletes closed
  steps after 4h and Delete (:1009) removes deps WHERE depends_on_id=id,
  erasing the finalizer's blocking edge to the failed step. Either way DepList
  returns no trace of the failed step, resolveBlockedOutcome returns
  OutcomePass, and processWorkflowFinalize (runtime.go:806-871) stamps
  gc.outcome=pass on the root AND walks closeSourceBeadChain (:826-852),
  closing the upstream source beads that a fail outcome would deliberately
  have left open as the human audit handle.
- scenario:
  Graph-routed city. Variant A (retention): a fan-shaped formulas-v2 workflow
  has parallel branches; a sink step on one branch hard-fails and closes
  gc.outcome=fail at hour 0 (retry.go stamps fail on the step bead only).
  Sibling branches run past hour 4 (CI wait, human-approval hold). The graph
  store's default retention sweeper (4h/30s, enabled because
  graphClassStoreFor passes no retention option) purges the closed failed
  step; Delete also removes the finalizer's blocks edge to it. When the last
  branch closes at hour 5, resolveBlockedOutcome iterates the surviving edges
  (all pass) and the abort_scope backstop lists a root whose failed member no
  longer exists — outcome=PASS. Variant A2 (abort_scope): an
  on_fail=abort_scope terminal failure anywhere in the graph is purged the
  same way, blinding the workflowRootHasTerminalAbortScopeFailure backstop.
  Variant B (migration, no 4h window): a workflow in flight at flip whose
  failed sink/abort_scope step closed pre-flip — importGraphSnapshot imports
  neither the closed step nor the finalizer's edge to it, and the residue
  sweep deletes the bd copy; the first post-flip finalize evaluation resolves
  PASS. In all variants processWorkflowFinalize stamps gc.outcome=pass on the
  root and closeSourceBeadChain closes the upstream source beads (e.g. the
  city-scope Adopt PR request) that a fail outcome deliberately leaves open as
  the human audit handle — the failure vanishes from every queue a human or
  retry order watches. Correction vs. the original scenario: a mid-chain
  on_fail=continue failure with downstream dependents never voted at finalize
  even pre-flip (finalize depends only on sinks), so the linear step-2
  narrative is wrong; the sink and abort_scope variants above are the real
  carriers of the lost vote.
- fix sketch:
  Two independent guards, either sufficient alone for the retention leg; both
  needed for full coverage. (1) Retention: make purgeTerminal workflow-aware
  for the graph class — skip closed beads whose gc.root_bead_id resolves to a
  still-open root (one indexed metadata lookup per candidate), or open the
  graph class store with a retention policy that only purges beads whose root
  is terminal. Cheapest structural form: in graphClassStoreFor, pass a purge
  predicate/option (e.g. WithSQLiteStorePurgeGuard) that checks root openness
  before Delete. (2) Migration: in importGraphSnapshot, also import closed
  graph beads that carry a fail vote for an open workflow — i.e. closed beads
  whose gc.root_bead_id is in the open import set and which either
  beadOutcomeFailed() or have on_fail=abort_scope — plus the dep edges
  targeting them (relax the graphIDs filter to include these imported-closed
  ids). Alternative smaller fix that covers both legs at once: at step-failure
  time (retry.go hard/soft-fail terminal arms and abortScope), stamp an
  aggregating fail marker on the ROOT bead (e.g. gc.failed_step_ids append or
  gc.workflow_fail=true), and have resolveFinalizeOutcome consult that root
  metadata as a third vote source — the root is open until finalize and is
  never purged or migration-skipped, making the outcome evidence lifecycle-
  proof. Add a regression test: closed failed sink step + purge (or migration
  round-trip) then finalize must still resolve fail.

### N12 [MEDIUM] workflow-finalize source-bead chain close is graph-blind in both directions: ref-less (migrated/legacy) roots stop the walk so parent sources are never closed, and the live-root singleton scan reads only work stores so a source can be closed while sibling graph workflows are still live

- pattern: byid-mutations
- evidence:
  processWorkflowFinalize (internal/dispatch/runtime.go:806-868) runs with
  store = the routed graph store post-flip (openControlStoreAtForCity early-
  return, cmd/gc/cmd_convoy_dispatch.go:442-446). Direction A:
  walkSourceBeadChain (runtime.go:886-993) reads gc.source_bead_id (:905) and
  gc.source_store_ref (:910); when the ref is EMPTY it keeps nextStore =
  currentStore (:911-912) — the GRAPH store — so loadAndClose's
  nextStore.Get(work-bead-id) (:942) returns ErrNotFound and the walk stops as
  a traced no-op 'deleted_parent' (:944-947): the work-store source bead is
  never closed. Refs are elided exactly when root and source were same-store
  at sling time (internal/sourceworkflow/sourceworkflow.go:144 'roots without
  SourceStoreRefMetadataKey are treated as belonging to the same store';
  internal/sling/sling_core.go:664-668 stamps the ref only when deps.StoreRef
  is non-empty, and cmd/gc/cmd_sling.go:1290-1312 workflowStoreRefForDir
  returns "" for unregistered store dirs) — an invariant the boot migration
  silently breaks by relocating the root (graph_class_migrate.go:123) without
  stamping a ref. Direction B: the mutate path's singleton guard
  listLiveSourceWorkflowRoots (:951, :995-1048) scans
  opts.SourceWorkflowStores, wired for workflow-finalize at
  cmd/gc/cmd_convoy_dispatch.go:218 via makeSourceWorkflowStoresLister
  (:382-420) -> openSourceWorkflowStoresWith — city + rig WORK stores only, no
  graph arm — so live gcg roots (which are the ONLY place workflow roots exist
  post-flip) are invisible to the scan, as are child sources found via
  sourceWorkflowChildSources (:1032,:1072-1095).
- scenario:
  Graph-routed city (graph.migrated marker + backend=sqlite), workflow-
  finalize dispatched against the routed graph store. Direction A (narrow
  population): a live v2 workflow root WITHOUT gc.source_store_ref — created
  before the Apr 18 2026 ref-stamping commit 6b5bde042 and still in flight at
  migration, or slung from a store dir not registered in city.toml
  (workflowStoreRefForDir returns "") — finalizes with outcome=pass;
  walkSourceBeadChain keeps the graph store for the ref-less hop, Get(work-
  store source id) is a clean ErrNotFound, the walk stops as 'deleted_parent',
  and the human-visible source bead stays open forever as a phantom
  queue/witness item (it does NOT block future slings — the singleton scan
  keys on live roots, which were closed). Roots stamped
  'city:<name>'/'rig:<name>' (all registered-scope slings since Apr 2026)
  close their sources correctly. Direction B: a source with two live graph
  workflows (--force replacement whose old root survived the graph-blind
  closeReplacedGraphV2Root per G22, a G22-minted duplicate root, or a drain
  child chain) finalizes one; listLiveSourceWorkflowRoots scans only city+rig
  work stores where zero roots live post-flip, so the sibling gcg root is
  invisible and the source is closed mid-flight — it vanishes from queues and
  witness/rewake logic keyed on the open source breaks.
- fix sketch:
  Two patches. (1) Direction B — add a routed graph-store arm to the shared
  lister: in openSourceWorkflowStoresWithProvider
  (cmd/gc/cmd_convoy_dispatch.go:1905) or in makeSourceWorkflowStoresLister,
  append a convoyStoreView for routedGraphStoreFor(cityPath, cfg) when routing
  is active (empty StoreRef, tolerate open failure as a skip like rig stores).
  Doing it in the shared helper simultaneously satisfies G22's fix option (3)
  for the sling singleton scan, so land it there once. (2) Direction A — in
  walkSourceBeadChain (internal/dispatch/runtime.go), when the ref is empty
  and currentStore is the graph-class store, resolve the fallback to the city
  work store before declaring deleted_parent: either thread a
  DefaultSourceStore/MemberStores probe (mirroring the G25/ebeba2a55 pattern)
  into ProcessOptions from cmd_convoy_dispatch.go, or — simpler and one-time —
  have the boot migration (graph_class_migrate.go importGraphSnapshot) stamp
  gc.source_store_ref='city:<name>' on relocated workflow roots that carry
  gc.source_bead_id but no ref, restoring the same-store invariant the
  relocation broke. Tests: a graph-routed t.TempDir city where (a) a ref-less
  migrated root's finalize closes the work-store source, and (b) finalize with
  a second live gcg root on the same source leaves the source open
  (live_child_workflow trace).

### N13 [MEDIUM] gc formula cook --attach (graph.v2) duplicate/conflict guards read the work store while Instantiate writes the graph store — re-cook mints a duplicate live gcg root and the attach dep edge lands cross-store

- pattern: byid-mutations
- evidence:
  cmd/gc/cmd_formula.go:643 opens store = openStoreAtForCity(scope.storeRoot,
  cityPath), which is policy-wrapped with cityPath
  (cmd/gc/scoped_store.go:121), so molecule.Instantiate at :711 routes its
  graph-apply to the embedded graph store
  (beadPolicyGraphStore.graphApplierFor, cmd/gc/class_store.go:225-234). But
  every idempotency/conflict guard in the same closure reads `store` directly
  — i.e. the work store, since the policy wrapper does not route reads:
  closeFormulaCookFailedGraphV2Roots (:680, ListByMetadata gc.graphv2_root_key
  at :1008), existingFormulaCookGraphV2Root (:683, :1031),
  formulaCookLiveInputConvoyGraphRoots (:691, ListByMetadata
  gc.input_convoy_id at :976), sourceworkflow.ListLiveRoots (:699). All find
  nothing post-flip because the gcg roots exist only in the graph store.
  Additionally ensureFormulaCookAttachDep (:689/:723 -> :940,
  store.DepAdd(attachBeadID, rootID, "blocks") at :965) adds a work-store dep
  edge whose DependsOnID is a graph-store resident.
- scenario:
  On a graph-routed city, `gc formula cook <v2-formula> --attach <convoy-id>`
  is re-run for the same input convoy (agent retry after a transient error,
  script re-invocation, or operator re-run). The graphv2 root key is stable
  for a convoy target, so pre-flip the second cook reused the live root (or
  refused with sourceworkflow.ConflictError for a conflicting live workflow).
  Post-flip, all four guards in the cook closure —
  existingFormulaCookGraphV2Root, closeFormulaCookFailedGraphV2Roots,
  formulaCookLiveInputConvoyGraphRoots, and sourceworkflow.ListLiveRoots —
  read through the policy wrapper into the doltlite WORK store, find nothing
  (the gcg root lives only in .gc/store/graph/beads.sqlite), and
  molecule.Instantiate pours a SECOND live gcg root with the identical
  gc.graphv2_root_key into the graph store (no store-side idempotency exists).
  The graph hook-ready/claim federation then dispatches both molecules' steps
  concurrently against the same source, and the failed-root cleanup path also
  never closes crashed prior roots. CORRECTION vs the original finding: the
  attach linkage is NOT a dangling cross-store DepAdd —
  ensureFormulaCookAttachDep already stamps AttachedWorkflowRootMetadataKey
  metadata for gcg-prefixed roots (cmd_formula.go:950-955); only the
  duplicate/conflict/cleanup guard reads are broken. Also note the plain work-
  bead attach variant mints a fresh input convoy per cook, so its same-key
  idempotency was inert even pre-flip; the regression there is limited to the
  cross-workflow conflict guard.
- fix sketch:
  Mirror G22's routed-store approach at the cook call site
  (cmd/gc/cmd_formula.go, v2 --attach closure): resolve guardStore once — if
  gs, routed, err := routedGraphStoreFor(cityPath, cfg); err == nil && routed
  { guardStore = gs } else { guardStore = store } — and pass guardStore
  (instead of store) to closeFormulaCookFailedGraphV2Roots,
  existingFormulaCookGraphV2Root, and formulaCookLiveInputConvoyGraphRoots
  (their queries index gc.graphv2_root_key / gc.input_convoy_id, which only
  ever live on graph-class roots). For sourceworkflow.ListLiveRoots, query
  both the work store and the routed graph store and merge (sling-minted and
  cook-minted roots may live in either pre/post flip). Note
  CloseWorkflowSubtree inside closeFormulaCookFailedGraphV2Roots must also run
  against the graph store so the failed subtree closes where it lives. Keep
  ensureFormulaCookAttachDep unchanged (its gcg metadata-linkage arm is
  already correct). Test: graph-routed t.TempDir city (config +
  .gc/store/graph.migrated marker); cook a v2 formula --attach onto a convoy
  twice — second invocation must return the existing gcg root (idempotent,
  Created=0) — and a conflicting live workflow on the same convoy must yield
  sourceworkflow.ConflictError.

### N14 [MEDIUM] gc prime / gc nudge wisp-step prompt injection cannot see graph-resident molecules and steps — agents on routed cities silently lose their current-step reminder

- pattern: scan-unions
- evidence:
  cmd/gc/wisp_step_inject.go:22-60 wispStepInjectionContent →
  openWispStepStore opens only GC_RIG_ROOT's store via openStoreAtForCity or
  openCityStoreAt (no graph arm anywhere in the file); resolveActiveMolecule
  :147-178 List(Status:in_progress, Type:molecule|wisp, Assignees) and
  resolveMoleculeRootViaBridge :190-210 then
  resolveInProgressStepChild/resolveEntryStepChild (:250,:272 List ParentID)
  all on that work store. Callers: cmd/gc/cmd_prime.go:211,:231,:342,:369,:384
  and cmd/gc/cmd_nudge.go:463. Molecule/wisp roots and their type=step
  children classify ClassGraph
  (internal/coordclass/classify.go:117-118,129-134) and live only in the graph
  store on a routed city. Everything is best-effort — errors and empty results
  return "" (:16-18 'callers must never fail hard on an empty return').
- scenario:
  On a graph-routed city, a pool/graph worker mid-formula is restarted (gc
  prime --hook session-start) or nudged (gc nudge drain --inject).
  wispStepInjectionContent opens only the work store (rig store if
  GC_RIG_ROOT, else city store); the in-progress wisp/molecule root and its
  type=step children are gcg beads resident only in the graph store, so
  resolveActiveMolecule and both step-child resolvers return empty, and the v1
  molecule_id bridge's Get(gcg-root) fails. For v2 graph workers (claimed gcg
  step is their only in-progress assignment) the function silently returns ""
  — the agent resumes with no current-step reminder. For v1 attached formulas
  the legacy fallback still injects the ga- source bead's description, so
  context degrades from step-level to bead-level rather than vanishing. No
  error is surfaced anywhere by design.
- fix sketch:
  Add a graph arm to openWispStepStore (or wrap its result): resolve the work
  store as today, then via routedGraphStoreFor(cityPath, cfg) (cfg loadable
  from the effective city path, mirroring cmd_bd_show_fed.go's marker-gated
  pattern) union or prefer the graph store for the molecule/step resolution.
  Cheapest correct shape: run resolveActiveWispStep against the graph store
  first on routed cities (roots/steps live there), falling back to the work
  store for the v1 bridge source bead and legacy description path; the
  bridge's Get(rootID) should route gcg- ids to the graph store
  (reservedClassForBeadID pattern from the show-fed seam). Keep the best-
  effort contract — nil graph store on any error, never fail hard.

### N15 [MEDIUM] API ActiveBead lookup for agents and sessions (findActiveBeadForAssignees*) fans out over BeadStores() only — dashboard shows workers executing graph steps as idle

- pattern: scan-unions
- evidence:
  internal/api/handler_agents.go:300-352
  findActiveBeadForAssigneesWithFreshness iterates s.state.BeadStores() (work
  stores per api_state.go buildStores) with ListQuery{Assignee,
  Status:in_progress}; no graph leg, and the cached arm (cachedListStore
  CachedList, :335-343) reads the work-store CachingStore. Consumers:
  internal/api/huma_handlers_agents.go:125 (agents list resp.ActiveBead) and
  :247 (agent detail, live variant); internal/api/handler_sessions.go:616-618
  (session detail ActiveBead). In-progress graph-class steps are claimed with
  the session assignee via the routed hook claim ops
  (cmd/gc/graph_hook_claim.go) and live only in the graph store.
- scenario:
  On a graph-routed city, pool workers executing claimed gcg- steps (assignee
  stamped only in .gc/store/graph/beads.sqlite via graph_hook_claim.go) return
  ActiveBead="" from GET /v0/agents, agent detail, and session detail, and
  computeAgentState downgrades them from "working"/"waiting" to "idle". During
  an incident with the graph plane fully loaded, the dashboard shows an
  apparently idle fleet — inverted triage signal — and per-store List errors
  are silently swallowed so nothing flags the miss.
- fix sketch:
  In findActiveBeadForAssigneesWithFreshness (internal/api/handler_agents.go),
  after the BeadStores() loop misses, add a graph leg guarded the same way as
  handler_convoy_dispatch.go:577: graphStore :=
  s.state.GraphBeadStore().Store; if graphStore != nil && graphStore !=
  s.state.CityBeadStore(), run the same ListQuery{Assignee,
  Status:"in_progress", Limit:1, Sort:CreatedDesc} per unique assignee via
  direct List (the dedicated graph store is not behind the API CachingStore,
  so skip the cachedListStore arm; honor the rig filter by only adding the leg
  when rig=="" or rig==cityName, matching where graph work executes).
  computeAgentState then corrects itself. Cover with a test seeding an
  in_progress gcg bead in a distinct graph store and asserting ActiveBead and
  State="working" for the assignee.

### N16 [MEDIUM] Non-claim `gc hook` work-query runner is never graph-federated — bare `gc hook` (the documented check-for-work command for several providers) reports no work for graph-resident beads

- pattern: exec-bd-shells
- evidence:
  cmd/gc/cmd_hook.go:419-423: the non-claim runner is
  `firstStoreWithWork(command, stores, stores[0], shellWorkQueryWithEnv)` —
  the RAW shell runner — passed to doHook at cmd_hook.go:459. Only the --claim
  branch (cmd_hook.go:456-457) installs
  graphFederatedWorkQueryRunner(cityPath, cfg)
  (cmd/gc/graph_hook_ready.go:30-49). doHook (cmd_hook.go:792, contract at
  :34: 'Without --inject: prints normalized ready-only output, exits 0 if work
  exists, 1 if empty') therefore runs the bd-shell work query against work
  stores only. Consumers whose behavior gates on this exit code/output:
  internal/bootstrap/packs/core/overlay/per-provider/kiro/AGENTS.md:25 and :33
  ('When you finish your current task or have no active work, run `gc
  hook`...', '`gc hook` — check for and claim available work') and per-
  provider/copilot/.github/copilot-instructions.md:21-33 ('run `gc hook` to
  check for available routed work'); also `gc hook <agent>` and `gc hook show`
  (skills/gc-work/SKILL.md:64). (Verified `gc hook --inject` is NOT affected —
  it skips the query entirely, cmd_hook.go:239-241.)
- scenario:
  Graph-routed city with a kiro or copilot provider worker (or any
  operator/agent following the documented bare `gc hook` check): the worker
  finishes a bead and runs `gc hook` per its instructions; its ready (or
  assigned) gcg step sits in the embedded graph store, the bd shell returns
  [], doHook exits 1 with 'no ready work', and the agent idles/ends its turn
  while dispatchable graph work for its own identity is pending. Recovery
  relies on pool-demand respawn churn instead of the in-session continue path;
  for assigned work it compounds with G13's adoption gap.
- fix sketch:
  In cmd/gc/cmd_hook.go:419-423, replace the raw shell runner in the non-claim
  path with the already-built federated one: `out, _, err :=
  firstStoreWithWork(command, stores, stores[0],
  graphFederatedWorkQueryRunner(cityPath, cfg))`.
  graphFederatedWorkQueryRunner already returns a hookStoreRunner with the
  same (command, dir, env) signature, is a no-op on non-routed cities
  (routed==false passes shell output through), fail-loud on graph read errors,
  and dedups by id, so it is drop-in compatible with firstStoreWithWork. Test:
  on a marker-flipped routed city with an empty work store and a ready gcg
  step routed to the agent, bare `gc hook` must print the graph-store
  candidate and exit 0 (today it exits 1 'no ready work').

### N17 [MEDIUM] Default on_death / on_boot recovery hooks are graph-blind bd shells — dead pool workers' in_progress gcg steps are never released, and boot-time reopen of unassigned in_progress routed graph beads is a silent no-op

- pattern: exec-bd-shells
- evidence:
  internal/config/workquery.go:537-564 buildOnDeath: discovery is `bd list
  --assignee=<qualified> --status=in_progress --json` plus the ephemeral `bd
  query` probe, release is `bd update "$id" --assignee "" --status open` — all
  exec'd bd, which cannot open the embedded graph store;
  internal/config/workquery.go:579-596 buildOnBoot: `bd list --metadata-field
  gc.routed_to=$template --status=in_progress --no-assignee` + reopen via `bd
  update`. Execution sites: cmd/gc/city_runtime.go:995-1008
  reconcilePoolDeaths runs poolDeathInfo.Command via shellRunHook on every
  controller tick when a pool session vanishes (handlers built at
  cmd/gc/cmd_start.go:142-168 computePoolDeathHandlers from
  EffectiveOnDeathForBeads at :158); cmd/gc/pool.go:398-427 runPoolOnBoot runs
  EffectiveOnBootForBeads at controller startup (controller.go:1431,
  cmd_start.go:888, cmd_supervisor.go:2134). Claimed gcg steps genuinely
  become assignee=<session identity>, status=in_progress in the graph store
  via graphRoutedHookClaimOps (cmd/gc/graph_hook_claim.go:38, wired
  cmd_hook.go:457).
- scenario:
  Graph-routed city: a pool worker claims a gcg step via
  graphRoutedHookClaimOps → SQLiteStore.Claim (status=in_progress,
  assignee=session identity, in the embedded graph store only), then its
  session dies. reconcilePoolDeaths fires the default on_death hook, but the
  hook is an exec'd bd shell: `bd list --assignee=<qualified>
  --status=in_progress` runs against the WORK store, finds nothing, exits 0 —
  the fast-path release silently does nothing and no gc-recovery diagnostic
  fires. Today nothing else recovers the step (controller orphan release G10
  and worker self-re-adoption G13 are open), so the molecule wedges; even
  after G07/G10 land, the designed fast-path release stays permanently dead
  and recovery always waits for the slower controller tier. Separately,
  on_boot's target shape — no-assignee in_progress routed beads (producible by
  a manual unassign, a user hook override, or non-default tooling; NOT by the
  default on_death, whose release is a single atomic bd update of both fields)
  — is covered by no controller mechanism at all (assigned-work collection is
  assignee-keyed; unassigned-routed collection lists status=open only), so a
  graph bead in that state is never reopened by anything.
- fix sketch:
  Mirror the graph_hook_claim.go pattern for the recovery hooks instead of
  trying to make exec'd bd graph-aware. Option A (in-process arm): after
  running the bd-shell hook, have reconcilePoolDeaths and runPoolOnBoot
  additionally call a Go-side graph arm when routedGraphStoreFor(cityPath,cfg)
  is routed — on_death: st.List({Status:"in_progress", Assignee:<qualified
  instance>}) then for each bead st.Update(id, {Assignee:ptr(""),
  Status:ptr("open")}) (backfilling gc.run_target metadata when both route
  keys are empty, matching buildOnDeath's semantics); on_boot:
  st.List({Status:"in_progress"}) filtered to empty assignee and
  gc.routed_to==template (or empty routed_to + gc.run_target==template +
  gc.kind=workflow), then st.Update(id, {Status:ptr("open")}). Gate on the
  default-hook case only (user overrides pass through verbatim, same as the
  RecoveryHookMarker contract). Fail loud to stderr on graph-store open errors
  rather than silently skipping. Option B (heavier): route the generated hooks
  through `gc` subcommands that already federate. Tests: routed city, gcg step
  in_progress assigned to a pool instance whose session is absent → on_death
  arm reopens it in the graph store; gcg step in_progress with no assignee and
  gc.routed_to=<pool> → on_boot arm reopens it.

### N18 [MEDIUM] Graph-class wisps have NO retention path: wisp-compact.sh TTL compaction and reaper.sh's closed-wisp purge are both graph-blind, so .gc/store/graph/beads.sqlite grows unboundedly and stuck-wisp promotion is lost

- pattern: exec-bd-shells
- evidence:
  internal/bootstrap/packs/core/assets/scripts/wisp-compact.sh:27 — discovery
  is `gc bd list --json --all -n 0`, which is not graph-federated (only plain
  `gc bd show <id>` is, per cmd/gc/cmd_bd_show_fed.go; no list federation
  exists in cmd_bd.go), then :83-90 promote/delete decisions run off that
  list. internal/bootstrap/packs/core/assets/scripts/reaper.sh purges closed
  wisps with raw Dolt SQL (`DELETE FROM \`$DB\`.wisps ...`, ~line 941-951)
  enumerated from `SHOW DATABASES` (:157) — the embedded graph SQLite store is
  structurally unreachable from dolt_sql. Wisps and all molecule roots/steps
  are ClassGraph (internal/coordclass/classify.go:49-77), so on a routed city
  100% of the wisp population relocates out of both sweepers' view. The only
  graph-side sweeps that exist are the order dispatcher's own (order-tracking
  retention + stale ORDER wisp subtrees,
  cmd/gc/order_dispatch.go:2288-2293/:2279 — and G18 already records those as
  graph-residual); no generic graph-store retention sweeper exists (grep
  StartRetentionSweeper: only sessions [session_class_migrate.go:274-285] and
  nudges [nudge_class_migrate.go] classes).
- scenario:
  Graph-routed city running the shipped core pack's wisp-compact and reaper
  exec orders: every heartbeat/ping/patrol/molecule wisp and closed step
  accumulates forever in .gc/store/graph/beads.sqlite — wisp-compact's `gc bd
  list` returns only work-store beads (typically none ephemeral), the reaper's
  SQL never touches the sqlite file, and both report success. Store bloat
  degrades every graph read on the hot dispatch path over weeks, and the
  secondary loss is behavioral: wisp-compact's 'non-closed past TTL → promote'
  stuck-detection (wisp-compact.sh:80-86) never fires for graph wisps, so
  wedged graph wisps also lose their escalation channel.
- fix sketch:
  Mirror the existing class-store retention pattern: add a graph-class
  retention sweeper started from city_runtime.go alongside
  startSessionsRetentionSweeper/startNudgesRetentionSweeper, but graph-aware —
  (a) closed wisp-tier rows past per-wisp_type TTL are deleted (reusing wisp-
  compact's TTL table, ideally hoisted into Go constants or config), (b) non-
  closed wisps past TTL are promoted to permanent with a comment (preserving
  the stuck-detection escalation), (c) closed non-wisp graph beads follow a
  longer configurable retention like order-tracking's policy. Alternatively
  (smaller, script-preserving): add a graph list arm to cmd_bd.go (a
  maybeRouteBdListLocal that unions routed graph-store rows with `.ephemeral`
  faithfully rendered into `gc bd list --json --all` output) so wisp-
  compact.sh discovers graph wisps and its existing promote/delete mutations
  route through the already-shipped cmd_bd_graph_sqlite.go arm; the reaper's
  SQL purge lane can then be left Dolt-only since the graph store is covered
  by the compact path. Add a routed-city test: mint an aged closed heartbeat
  wisp and an aged open wisp in the graph store, run the sweeper (or script
  harness), assert delete + promote respectively.

### N19 [LOW] Convoy view lane blind to graph-resident synthetic convoys: GET /v0/convoys list and non-workflow ConvoyGet arm iterate work stores only; CLI convoyStoreCandidates has no graph candidate despite its own contract comment claiming one

- pattern: scan-unions
- evidence:
  internal/api/huma_handlers_convoys.go:83-100 humaHandleConvoyList iterates
  s.state.BeadStores() with List(Type:"convoy") — no graph leg; :146/:484
  divert only isGraphConvoyID hits, and handler_convoys.go:8-24
  isGraphConvoyID probes the graph store but returns true only for
  isGraphConvoyBead (gc.kind=workflow) — a synthetic type=convoy bead found in
  the graph store returns false and falls to the BeadStores Get loop → 404.
  CLI: cmd/gc/cmd_convoy.go:427-483 convoyStoreCandidates builds city+rig dirs
  only, while the resolveOwningStoreDir doc comment (:520-527) explicitly
  claims 'The candidate set is the convoy class-store ordering (the graph
  store the convoy bead lives in, plus the per-rig work stores...)' — the
  graph candidate was never implemented; collectOpenConvoys :657-664 (gc
  convoy list / doConvoyStatusFallback) scans the same set. Synthetic convoys
  (gc.synthetic on type=convoy — graph.v2 input convoys, drain-unit convoys)
  classify ClassGraph (internal/coordclass/classify.go:131) and are created
  into the graph store via beadPolicyStore.createTarget.
- scenario:
  On a graph-routed city with an active drain, drain-unit convoys
  (gc.synthetic_kind=drain-unit-convoy) and the workflow input convoy live
  only in the graph SQLite store. GET /v0/convoys and gc convoy list omit
  them; GET /v0/convoy/{id} finds the bead via isGraphConvoyID's graph-first
  probe, classifies it non-workflow, falls to the work-store loop, and returns
  ConvoyNotFound; gc convoy status (API and controller-down fallback via
  resolveOwningStoreDir) likewise reports not-found. An operator debugging
  drain progress through the convoy surface sees a real convoy id 404 that
  listed pre-flip — a silent result-set discontinuity that can mislead
  recovery decisions, though nothing wedges or is destroyed.
- fix sketch:
  Three small patches mirroring existing seam patterns. (1) API list: in
  humaHandleConvoyList, after stores := s.state.BeadStores(), inject the graph
  store as a synthetic leg when g := s.state.GraphBeadStore().Store; g != nil
  && g != s.state.CityBeadStore() (same "infra:<city>" injection pattern as
  G14's fix for humaHandleBeadList), so graph-resident type=convoy beads join
  the keyset-sorted concatenation. (2) API by-id: in humaHandleConvoyGet, when
  isGraphConvoyID is false, add a graph-store arm before/alongside the
  BeadStores loop — Get the id from the routed graph store; if found and
  Type=="convoy", serve it with convoycore.Members(graphStore, id, true,
  s.memberStoreComplement(graphStore)...) so work-store members resolve (pairs
  with G31's memberStoreComplement fix). (3) CLI: in
  convoyStoreCandidatesWithProvider (or openConvoyStores), append the routed
  graph store as a candidate when graph routing is active — a convoyStoreView
  over routedGraphStoreFor(cityPath, cfg) since the graph store is not a
  directory-addressed scope — making resolveOwningStoreDir honor its
  documented contract and collectOpenConvoys see synthetic convoys; tolerate
  open failure as a skip like rig stores. Tests: a routed t.TempDir city with
  a synthetic type=convoy bead in the graph store must appear in GET
  /v0/convoys and gc convoy list, and resolve via GET /v0/convoy/{id} and gc
  convoy status.

### N20 [LOW] Doctor scans beyond backlog-depth are also graph-blind (corrects G38's 'only affected probe' claim): routed_to namespace check, hold-label checks, and work-option metadata check skip graph-resident beads

- pattern: scan-unions
- evidence:
  G38's local evidence asserts 'Other local doctor checks do not read
  molecule/wisp beads... so backlog depth is the only affected probe found.'
  Counter-examples: cmd/gc/doctor_routed_to_checks.go:80-106
  v2RoutedToNamespaceCheck.scanScope opens city+rig stores (c.newStore) and
  Lists Metadata{gc.routed_to:route} — order-poured wisp roots and workflow
  roots/steps carry gc.routed_to and are graph-resident (order_dispatch.go
  stamps routed_to on gcg wisp roots; workflow beads classify ClassGraph);
  cmd/gc/doctor_hold_label_routed_to.go:75-105 AllowScan sweep over the same
  scopes matching hold:<value> labels;
  cmd/gc/doctor_hold_label_conventions.go:83 ListByLabel(hold labels);
  cmd/gc/doctor_work_option_metadata.go:92 List(Type:"task") — embedded work-
  typed graph-plan steps are Type task with gc.root_bead_id and classify
  ClassGraph (classify.go:84,113-118), so they live in the graph store. None
  of these files reference routedGraphStoreFor.
- scenario:
  On a graph-routed city ([beads.classes.graph] backend=sqlite +
  graph.migrated marker), gc doctor's v2-routed-to-namespace, hold-label-
  routed-to, hold-label-conventions (city and rig scopes), and work-option-
  metadata-migration checks scan only work-class stores via openStoreForCity,
  whose policy wrapper federates creates but not List/ListByLabel reads.
  Order-poured wisp roots stamped with gc.routed_to (order_dispatch.go:1559),
  graph steps carrying hold:<value> labels or retired hold labels, and work-
  typed graph steps with legacy gc.model/gc.reasoning metadata all live only
  in .gc/store/graph/beads.sqlite and pass doctor clean — including doctor
  --fix backfill/migration paths that therefore never repair graph-resident
  beads. Advisory-only impact (missed diagnosis), but G38's explicit "backlog
  depth is the only affected probe" claim would steer the P5 doctor port to
  fix only backlog-depth and run_target_backfill, leaving these four sites
  graph-blind.
- fix sketch:
  Same pattern as G38's fix sketch, generalized: cmd_doctor.go:310-321 already
  has cfg and cityPath in scope, so resolve routedGraphStoreFor(cityPath, cfg)
  once and thread it into the four checks (or wrap storeFactory's city-scope
  result in a small read-union that appends graph-store List/ListByLabel
  results for the city scope only — the graph class is city-rooted, so rig
  scopes need no arm). In each check's scan/collect, append graph-store
  results to the city-scope items (ID prefixes are disjoint, simple append is
  safe) before classification; for the two --fix-capable checks (hold-label-
  routed-to backfill, work-option-metadata migration) keep the originating
  store on the target struct so SetMetadataBatch/Update hit the owning store.
  On routing-resolve error, degrade to the checks' existing "skipped scope"
  StatusWarning path rather than silently reporting work-store-only. Also
  correct G38's "only affected probe" sentence in GRAPH-READ-GAP-ANALYSIS.md
  so the P5 port scopes doctor correctly.

### N21 [LOW] gc beads show/list controller-down fallback is graph-blind: openAllConvoyStores has no graph candidate, so gcg ids resolve only while the controller is up

- pattern: scan-unions
- evidence:
  cmd/gc/cmd_beads.go doBeadsShowFallback (:~258-278) resolves via
  openAllConvoyStoresAt → cmd/gc/cmd_convoy.go:427-483 convoyStoreCandidates
  (city+rig dirs only, no graph store), then per-store Get → 'bead %s not
  found'; collectBeadsAcrossStores :286-300 (gc beads list fallback) Lists the
  same store set. The API-routed halves are covered (GetBead → beadStoresForID
  gcg arm; list endpoint gap is G14/G30), but routeRead falls back to the
  direct multi-store read exactly when the controller is down.
- scenario:
  Controller down (or GC_NO_API set) on a graph-routed city: `gc beads show
  gcg-X` takes the routeRead fallback into doBeadsShowFallback, probes only
  work-class stores, and prints "bead gcg-X not found" for a bead that exists
  in .gc/store/graph/beads.sqlite; `gc beads list` (doBeadsListFallback →
  collectBeadsAcrossStores) silently omits the entire graph plane. Meanwhile
  `gc bd show gcg-X` (cmd_bd_show_fed.go) succeeds, giving inconsistent
  absence signals during exactly the incident-triage windows where operators
  use these commands, with the false not-found inviting destructive
  conclusions (root-loss lesson).
- fix sketch:
  In doBeadsShowFallback, add a reserved-prefix arm before (or after) the
  work-store scan: when coordclass/IsReservedGraphID(beadID) or
  unconditionally as an extra candidate, open the graph store via
  routedGraphStoreFor(cityPath, cfg) and Get there — or reuse the existing
  findBeadAcrossStores gcg arm from cmd_convoy_dispatch.go. For
  collectBeadsAcrossStores, have openAllConvoyStoresAt append a graph-store
  convoyStoreView (path .gc/store/graph) when the city is graph-routed (marker
  + [beads.classes.graph] backend=sqlite), so both show and list fallbacks
  federate the graph plane; fail-loud if the marker exists but the store
  cannot open, mirroring the G14 fail-loud convention.

### N22 [LOW] gc wait dependency reads probe only work-store candidates — waits on gcg dependencies report not-found / never observe closure

- pattern: exec-bd-shells
- evidence:
  cmd/gc/cmd_wait.go:951-987 loadWaitDependencyBead: resolves each depID over
  convoyStoreCandidates(cfg, cityPath, depID) (cmd/gc/cmd_wait.go:962;
  candidates = rig dirs + cityPath, cmd_convoy.go:427-483) opened via
  openStoreAtForCity (no graph arm, cmd/gc/main.go:1334) and the cityStore
  fast path; a gcg dep misses every candidate and returns beads.ErrNotFound,
  so the caller's closed-dependency gate (cmd_wait.go:925-948: dep.Status ==
  "closed" counting, missingErr propagation) either errors or never satisfies.
- scenario:
  On a graph-routed city, an agent runs `gc session wait <session> --deps
  gcg-<workflow-root-or-step>` (e.g., to sleep until a sibling subtree's root
  closes). doSessionWait's upfront dep validation (cmd_wait.go:293-297) calls
  loadWaitDependencyBead, which probes only work-store candidates (rig dirs +
  city) and returns beads.ErrNotFound for the graph-resident bead, so
  registration fails immediately with "dependency gcg-…: not found" even
  though the bead exists (possibly already closed) in
  .gc/store/graph/beads.sqlite — waits on graph-class beads are simply
  impossible. Additionally, any wait registered before the graph flip whose
  dep migrated to the graph store is spuriously failed on the next controller
  tick: prepareWaitWakeStateWithSnapshot's dep check (cmd_wait.go:1100-1110)
  hits ErrNotFound via the equally graph-blind newWaitDependencyStoreSet and
  calls FailWait + clearSessionWaitHoldIfIdle, so the session's wait
  terminates in failure instead of being satisfied when the dep closes. Both
  arms are loud wrong-answers, not silent spins.
- fix sketch:
  Add a graph-class arm to both wait dependency readers. (1) In
  loadWaitDependencyBead (cmd/gc/cmd_wait.go:950): when
  coordclass.Classify(depID) is ClassGraph (reserved gcg- prefix) on a graph-
  routed city, resolve via routedGraphStoreFor(cityPath,
  cfg)/resolveGraphStore first (cfg is already loaded in-function), falling
  through to the existing work-store candidate loop for partial-migration
  safety. (2) In the controller tick, thread the runtime's already-open graph
  store into newWaitDependencyStoreSet (or append it in
  prepareWaitWakeStateForTick, city_runtime.go:1682-1687) so
  storeref.Resolve's PrefixOwner routes gcg ids to it. Test: register a wait
  on a closed graph-store bead on a routed city and assert registration
  succeeds + wake pass marks it ready instead of FailWait.

### N23 [LOW] spawn-storm-detect.sh reset-loop discovery lists only work stores — crash-loop escalation can never fire for graph-resident beads

- pattern: exec-bd-shells
- evidence:
  internal/bootstrap/packs/core/assets/scripts/spawn-storm-detect.sh:36 —
  `OPEN_BEADS=$(gc bd list --status=open --assignee="" --json --limit=0)`
  feeds the reset-count ledger (:47-71) that triggers the SPAWN_STORM mayor
  mail at threshold; `gc bd list` has no graph federation (only plain show is
  federated via cmd_bd_show_fed.go). The inventory's covered-section cites
  this script ONLY for its `gc bd show` probes at :57 and :79 (covered by
  maybeRouteBdShowLocal) — the :36 list discovery is a different read.
- scenario:
  Graph-routed city ([beads.classes.graph] backend=sqlite +
  .gc/store/graph.migrated): a gcg step bead is repeatedly reset to
  open+unassigned with metadata.recovered/rejection_reason stamped — today
  only via prompt-driven recovery (`gc bd update gcg-X --status open
  --assignee "" --set-metadata ...`, which routes to the graph store via
  cmd_bd_graph_sqlite.go), and more broadly once the G07/G10/G20 orphan-
  release graph fixes land. spawn-storm-detect.sh's discovery at :36 (`gc bd
  list --status=open --assignee=""`) execs bd against the work stores only, so
  the reset never enters the spawn-storm-counts.json ledger, the threshold is
  never crossed, and the SPAWN_STORM escalation mail to mayor/ never fires —
  the crash loop burns worker sessions with no escalation. Pre-flip behavior
  (gcg beads in the work store WERE listed and counted) regresses silently on
  the flip.
- fix sketch:
  File as a sibling of G23's orphan-sweep list-blindness note and fix both
  script discoveries together. Two options: (a) script-level — extend spawn-
  storm-detect.sh step 1 to union a graph-store leg into OPEN_BEADS (e.g. a
  small `gc graph list --status=open --assignee="" --json`-shaped read served
  by routedGraphStoreFor, or reuse whatever federated discovery command the
  G23 fix gives orphan-sweep.sh), fail-loud on routing error so a graph read
  failure doesn't silently shrink the ledger; or (b) gc-level — add a doBd
  list-federation seam mirroring graphFederatedWorkQueryRunner: when bdArgs
  match `list --status=<s> --assignee="" --json` on a routed city, union
  st.List rows from the embedded graph store into bd's JSON-array output (same
  fail-loud discipline as graph_hook_ready.go). Option (b) also heals orphan-
  sweep.sh:58/75 and any future script using the same shape, at the cost of a
  wider intercept surface; option (a) is the minimal slice.

### N24 [LOW] Wisp-GC batch delete comment assumes the sqlite graph store implements BatchDeleter — it does not; per-bead fallback runs silently (perf-only today, and the false assumption invites regressions)

- pattern: store-identity-caps
- evidence:
  deleteWorkflowBeadsBatch's doc comment claims 'On the sqlite/Dolt graph
  store this collapses an O(subprocess-per-edge) closure teardown into a
  single batched delete' (cmd/gc/cmd_convoy_dispatch.go:1459-1467), but
  DeleteBatch is implemented only by BdStore (internal/beads/bdstore.go:2217)
  and CachingStore forwarding (internal/beads/caching_store_writes.go:956) —
  never by SQLiteStore (grep over internal/beads/sqlite_store*.go). On a
  routed city the wisp-GC closure teardown against the graph store silently
  takes the per-bead path: deleteWorkflowBead does DepList down+up, DepRemove
  per edge, then Delete per bead (cmd_convoy_dispatch.go:1489+), i.e. O(edges)
  individual sqlite transactions per purge instead of chunked batches.
- scenario:
  On a graph-routed city, a large wisp-GC closure purge (or convoy-dispatch
  workflow teardown) against the embedded SQLiteStore silently takes the per-
  bead path: one fsync'd sqlite transaction per dep edge removed plus one per
  bead deleted (synchronous=FULL), instead of the chunked batch the doc
  comments at cmd/gc/cmd_convoy_dispatch.go:1463-1465 and
  cmd/gc/wisp_gc.go:604-605 claim the sqlite graph store already provides. A
  multi-thousand-edge closure costs seconds of controller-tick latency
  (bounded per sweep by wispGCClosurePurgeBatchCap), never errors, and
  correctness is preserved (SQLiteStore.Delete cascades its own dep rows). The
  main hazard is the two load-bearing false comments: future callers may size
  batches or skip caps assuming the batched capability exists on the routed
  store.
- fix sketch:
  Implement DeleteBatch on *SQLiteStore in internal/beads/sqlite_store.go
  satisfying the BatchDeleter contract: single retryOnBusy transaction per
  chunk doing `DELETE FROM beads WHERE id IN (...)` plus `DELETE FROM deps
  WHERE issue_id IN (...) OR depends_on_id IN (...)` (labels/metadata already
  go via ON DELETE CASCADE; deps has no FK so it needs the explicit sweep,
  matching what Delete does per-bead today), chunked to keep the IN-list
  bounded. Add it to the sqlite conformance test alongside the existing
  BatchDeleter coverage. Alternatively (minimal): correct the two comments
  (cmd_convoy_dispatch.go:1459-1467, wisp_gc.go:603-608) to say only the
  bd/Dolt store batches and sqlite takes the per-bead path.
