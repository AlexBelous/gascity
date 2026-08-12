# WD Detector Design — the sweep that lets legacy die

Companion to `engdocs/plans/reconciler-distillation/DESIGN.md` (§2 end-state, §6 traps,
§7 D1–D4). Paths relative to `/data/projects/gascity/.claude/worktrees/rec`; evidence =
direct reads at HEAD + lens reports `/var/tmp/distill-analysis/*.md`. The god function
`reconcileSessionBeadsTracedWithNamedDemand` spans `cmd/gc/session_reconciler.go:
1447–4057` (2,611 LOC, 30 params); wrapper chain :1293/:1332/:1367/:1408. Production
callers: `city_runtime.go:2870` (`beadReconcileTick`), `city_runtime.go:3626`
(`controlDispatcherTick`), `cmd_start.go:1035` (one-shot `gc start`), plus the perf CLI
(`cmd_perf_reconciler.go:349`, D4-retained until WE).

Invariants (restated): observation fans out, action stays keyed and serial; detectors
READ ONLY, enqueuing exact session keys into the existing controllers; all effects sit
in per-key reconcile functions behind the existing admission machinery (leases,
instance-token fences, refuse-on-ambiguity); D2 hard capabilities
(`FreshLivenessObserver` + `UnattendedSessionStopper` — only tmux/auto/hybrid);
`safeTick` preserved (`city_runtime.go:1149-1163`, #663); zero new controller
frameworks, queues, schemas, or provider interfaces.

## 1. Site disposition table

The 28 sites = 19 decision sites + 9 phase sites fired inside the god function.
Detector families (defined in §3): D-DEADLINE, D-ORPHAN, D-STALE-CREATE, D-DRIFT,
D-SLEEP, D-DRAIN, D-WAKE, D-ZOMBIE, D-STALL, D-DUP, D-STRANDED.

| # | Site | Disposition | Legacy logic | Strongest existing test |
|---|------|-------------|--------------|-------------------------|
| 1 | IdleTimeout | PORT → session-start / D-DEADLINE | session_reconciler.go:3364-3481 | `TestReconcileSessionBeads_IdleTimeoutStopsAndStaysAsleep` session_reconciler_test.go:9489 (+ hold/quarantine negatives :9609/:9879) |
| 2 | MaxSessionAge | MERGE-INTO D-DEADLINE (retirement REJECTED, arg. R1) | :3280-3354; kill at :3326 | `TestReconcileSessionBeads_MaxSessionAgeKillsAgedSession` :9963 (asserts kill + `SessionMaxAgeKilled` event + `sleep_reason`); 7 real negatives :10008-10574 |
| 3 | Orphaned | PORT → session-start / D-ORPHAN | :2176-2260 (kept-open :2204, deferred-confirm :2231, drain :2247) | `TestReconcileSessionBeads_OrphanSessionDrained` :6239; `OrphanDrainLiveAssignedWorkStaysOpen` :6257 |
| 4 | CloseOrphan | MERGE-INTO D-ORPHAN close handler | :2253-2302 | `TestReconcileSessionBeads_OrphanNotRunningClosed` :6812; partial-store fail-closed `:1045` (gc-hz0nu guard) |
| 5 | CloseFailedCreate | PORT → session-start / D-ORPHAN | :1955-2004 | `TestReconcileSessionBeads_ClosesOrphanedFailedCreateAndFreesSlot` :11968 (close + slot refill — best test in corpus) |
| 6 | PendingCreate | PORT → session-start / D-STALE-CREATE (per-tick budget counter :1696 retired, arg. R6) | :1703-1723, :1848-1889, :2862-2871, heal-annotation :5344-5383 | `TestReconcileSessionBeads_RollsBackPendingCreateWhenLeaseExpiredAndNoRuntime` :7764 |
| 7 | PendingCreatePreserved | MERGE-INTO D-STALE-CREATE (negative arm) | :1960-1969, :2063-2075 | `PreservesNeverStartedPendingCreateBeforeLeaseExpires` :7709 |
| 8 | ConfigDrift | PORT → session-start / D-DRIFT | :2903-3201 (alive), :3220-3267 (asleep), relaunch helper :5948-6082 | `ConfigDriftInitiatesDrain` :5710; `LaunchOnlyDriftRelaunchesOrdinarySession` :8696; `AttachedSessionNeverRestartedOnConfigDrift` :9086 |
| 9 | LiveDrift | MERGE-INTO D-DRIFT (same hash-compare detector) | :3158-3197 | `LiveDriftReapplied` :8668; `LaunchAndLiveDriftRelaunchThenLiveNextTick` :8757 |
| 10 | DrainAck | PORT detection → sweep; handler = existing keyed drain-ack stop | orphan arm :2077-2185, desired arm :2408-2550; keyed handler already at session_start_reconcile.go:1366-1647 | `DrainAckMarksStopPendingAndStopsAsync` :936; keyed side `TestReconcileExactDrainAckRequiresAtomicCloseBeforeStop` session_start_reconcile_test.go:1823 |
| 11 | DrainCancel | MERGE-INTO D-DRAIN cancel arms | :2100-2123, :2437-2453 (+ session_wake.go:605/617/636) | `TestAdvanceSessionDrains_OrphanedDrainCanceledForAssignedWork` session_wake_test.go:1070; `AttachedSessionCancelsQueuedConfigDriftDrain` :9190 |
| 12 | DrainDecision | PORT → session-start / D-SLEEP | :3840-3895 | `PreservedRunningNamedSessionStillIdleDrains` :7016; drain begin semantics session_wake.go:203-233 |
| 13 | PreserveConfiguredNamed | PORT: preserve = detector-side non-enqueue; rate-limit hold → D-WAKE handler | :1902-1954, re-admission :2040-2062 | `PreservedConfiguredNamedRateLimitRunsBeforeHeal` :6927 |
| 14 | ProgressStallExempt | MERGE-INTO D-STALL, claim-less arm ONLY (retirement REJECTED, arg. R2) | :2585-2609; floorExempt suppresses only the claim-less arm (`exempt \|\| floorExempt` :2636); the claim-holder arm gates on `exempt` alone (:2648) and the claim lookup still runs for floor workers when claim_holder_stall_timeout > 0 (:2611) | `ProgressStallExemptsMinFloorIdleWorker` session_reconciler_progress_test.go:538 PAIRED with `RecyclesAboveFloorWorker` :569; claim-holder arm `ClaimHolderStallKeepsPoolClaimForFreshWorker` :431 |
| 15 | TerminalProviderError | PORT → session-start / D-ZOMBIE | :2324-2354; markProviderTerminalError session_reconcile.go:594-619 | `ZombieTerminalProviderErrorMarkedUnhealthy` :10760 |
| 16 | ReconcilerCircuitOpen | KEYED-OWNED ALREADY as gate (session_start_reconcile.go:819-821, :1848-1850); persist+LogOpenOnce move to handler | :3719-3745 | `TestReconciler_CircuitOpenBlocksSpawn` session_circuit_breaker_test.go:956 (+ `CircuitClosedAllowsSpawn` :995) |
| 17 | ProviderHealthGate | KEYED-OWNED ALREADY as gate (:1846-1858); episode accounting → sweep. NEW RED required — legacy respawn-gate `continue` at :3755-3766 has NO integration test | :3746-3769; snapshot load :1508 | unit-only `TestGate_NoRespawnWhileRed` provider_health_gate_test.go:120; nearest real: session_reconciler_progress_test.go:405 |
| 18 | UnknownState | MERGE-INTO sweep guard (skip + throttled diagnostic; no handler) | :1802-1814 | `TestEmitSessionUnknownStateDiagnostic_ThrottlesAndEscalates` :2871; known-state pins :11727/:11733 |
| 19 | WakeDecision | PORTED (WD.10a, named + dependency arms) → D-WAKE via the EXISTING lease admissions (`AdmitConfiguredNamedWake`/`AdmitStrictDefaultPoolWake`/`AdmitConfiguredDependency`), certified at the routing seam under `AdmitWake`; pool-fill arm and the quarantine/rate-limit/preserve accounting stay legacy's until WD.10b; `keyed_start_owner` seam arms (:1735-1745, :3766-3775) RETIRE at WE | positive arm :3777-3795; quarantine skip :3702-3705 (untraced) | `AlwaysNamedSessionWakesAfterLiveChurnSequence` :4873 (multi-tick churn→wake) |
| 20 | BuildDeps (phase) | RETIRE (arg. R3) | :1531-1538; buildDepsMap :911 | none behavioral — feeds TopoOrder only |
| 21 | TopoOrder (phase) | RETIRE (arg. R3) | :1565-1571; topoOrderRows session_reconcile.go:1142-1200 | none behavioral (cycles already degrade to input order) |
| 22 | HealRetire (phase) | PORT split: expired hold/quarantine heal → wake-handler admission clear; duplicate retire → D-DUP | :1545-1562; retire impl session_beads.go:609-678 | duplicate-retire session_beads_test.go:1849 (winner kept, loser archived, work re-pointed); trace-site coverage is wiring-only (session_reconciler_trace_test.go:637) |
| 23 | CircuitBreaker (phase, restore) | PORT → sweep hydration (in-memory restore + progress signatures + pruneIdle; persists deferred to handlers — sweep stays zero-write) | :1599-1659; breaker session_circuit_breaker.go:207-696 | `CircuitOpenStatePersistsAcrossControllerRestart` session_circuit_breaker_test.go:909; `CircuitTripsThroughRepeatedWakeAttempts` :1113 |
| 24 | ForwardPass (phase) | MERGE-INTO sweep — the sweep IS the forward pass reduced to read-only predicates | :1724-3494 | n/a (structure, not behavior) |
| 25 | AwakeSet (phase) | MERGE-INTO sweep (`ComputeAwakeSet` reused as pure library; idle-probe selection detector-side, launch handler-side) | :3520-3600; compute_awake_set.go:130 | `compute_awake_set_test.go` (~60 pure-decision tests); `TestMinActive_AsleepCityStopWakes` compute_awake_set_min_active_test.go:22 |
| 26 | WakeSleep (phase) | split: wake arm → D-WAKE; no-wake arm → D-SLEEP; dead-pool arm → D-STRANDED; fresh-cycle/bead-id stamps → wake handler | :3602-3997 (stranded repair :3897-3979) | stranded/slot tests :11086-:11372 cluster |
| 27 | StartExecution (phase) | KEYED-OWNED ALREADY — shared start wave `executePlannedStartsTraced` → `commitStartResultTraced` (session_lifecycle_parallel.go:2305/:2881) serves both paths; keyed fires lifecycle.start.prepare/execute/commit | :4015-4027 | A2 exactly-once cluster, e.g. `TestConfiguredNamedPinnedSessionStartsSameCanonicalSession` controller_session_start_socket_test.go:751 |
| 28 | DrainAdvance (phase) | PORT → D-DRAIN (due-advance detection in sweep; interrupt/complete/GC_DRAIN_ACK effects keyed) | :4033-4047; engine session_wake.go:529-726 | `TestAdvanceSessionDrains_ProcessExited` session_wake_test.go:853; `TestCancelSessionDrain_GenerationMismatch` :800 |

Retirement arguments:

- **R1 MaxSessionAge — retirement rejected.** The corpus proves real behavior: actual
  kill via `workerKillSessionTargetWithConfig`, `SessionMaxAgeKilled` event, and 7
  preservation negatives (hold, quarantine, busy, young, pending). Off by default
  (`MaxSessionAgeDuration()` → 0, config.go:3571; tracker nil unless configured,
  cmd_start.go:292-305). If retired: any city setting `max_session_age` silently loses
  bounded-lifetime rotation and its event consumers. Cost to keep: ~30 lines sharing
  D-DEADLINE's detector and stop handler with IdleTimeout — same shape, different
  deadline source and reason. Only its *tracing* was never tested
  (`TraceSiteReconcilerMaxSessionAge`: zero test references; the production emit sites
  :3318/:3324 are its only uses), so trace parity is best-effort.
- **R2 ProgressStallExempt — retirement rejected.** The exemption is proven bounded by
  its paired counter-case (:538/:569). Retiring only the exemption while porting
  stall-recycle would recycle min-floor idle workers — a regression against a passing
  test. Retiring the whole stall path would orphan claim-holder stall recovery
  (progress_test.go:405). Both are mechanical config-threshold checks
  (`ProgressStallTimeoutDuration()` 0-default) — no judgment in Go added.
- **R3 TopoOrder + BuildDeps — retire.** Both exist only to sequence a single-pass
  fleet loop (last-write-wins name resolution in `ComputeAwakeSet` needs deterministic
  order). The sweep PINS its iteration order — stable sort by session name, then bead
  ID — into `ComputeAwakeSet`, and the campaign join carries the awake-set winner
  identity so any order-sensitive divergence surfaces as a classified mismatch (§3b).
  Dependency-triggered wakes are already owned end-to-end by the wait-dependency
  index/producer (session_wait_dependency_index.go, A10/A11 tests). `topoOrderRows`
  already returns input order on any cycle, so no behavior can depend on strict
  topological order; no behavioral test asserts cross-template processing order.
- **R4 tickDebouncer — retire NOW (WD.0).** Dead by default: `TickDebounceDuration()`
  returns 0 for empty/unparseable/negative (config.go:2710-2719); only
  `examples/hyperscale/city.toml:44` sets it; at delay<=0 `arm()` degenerates to the
  pre-existing non-blocking send (city_runtime.go:1218-1224). All 6 tests
  (city_runtime_test.go:638-714) count channel fires on a bare struct — none observes
  a session. What breaks: one doc-example line. Poke coalescing survives via the
  cap-1 channels and workqueue key coalescing.
  **Correction (recorded at WD.0, commit 9f7b673db4).** "One doc-example line"
  undercounted the blast radius. Six journey fixtures also set the key, two of them
  load-bearing: the v59 lane-gate journey
  (cmd/gc/session_start_real_bd_tmux_integration_test.go) and the wait-dependency
  journeys that paired `patrol_interval="1h"` with a 10m debounce specifically to
  suppress event-driven fleet ticks. Those fixtures had bought their determinism from
  a quiet window, and deleting the debouncer did not create the races it EXPOSED —
  every poke now ticks immediately, so a fleet tick lands inside legs that previously
  ran alone. Two were adjudicated on this lane: **ga-797vy** (a repairable coexistence
  race around a production-correct suspend family — fixed by the F1/F2/F4 ownership
  fences) and **ga-ij8mh** (a wake family that was never production-reachable, whose
  journey leg is skipped until WD.10a re-anchors it). The retirement still stands; the
  doctrine it establishes is that fixtures may not buy determinism with quiet windows,
  and that determinism comes from ownership.
- **R5 Wrapper chain — retire at WE, with the perf CLI, in the same commit.** Two
  wrappers ARE production entry points today (`AtPathWithNamedDemand` ←
  city_runtime.go:3626 + cmd_start.go:1035; `AtPath` ← the D4-retained perf CLI
  cmd_perf_reconciler.go:349); the chain dies at WE because every caller dies — the
  two scoped callers switch to the sweep during WD (WD.1) and the perf CLI is deleted
  at cutover. Tests that then fail to compile are scaffolding (Trap 2).
- **R6 rollback budget (`maxRollbacksPerTick=5`, :1695-1696) — retire.** Keyed
  rollbacks run on the controller's bounded worker pool, off the tick's critical path,
  so a rollback burst can no longer stall the fleet pass the budget was protecting
  (the rate limiter additionally shapes retries). Expected-divergence in the campaign:
  legacy defers rollback #6+, keyed does not (§3b).
- Also deleted with zero behavior: `TraceSiteReconcilerIdleDrain` (dead constant,
  zero call sites repo-wide, session_reconciler_trace_types.go:96).

## 2. The detector sweep

**Where it lives.** No new loop, timer, goroutine, or queue. `beadReconcileTick`
(city_runtime.go:2682-2964) keeps its entire loading pipeline and replaces the single
god-function call at :2870 with `detectSessionConditions(...)` — during WD the sweep
runs *beside* the legacy call in the same tick (shadow, §3); at WE the legacy call is
deleted and the sweep stands alone. Detection predicates live in ONE new file,
`cmd/gc/session_detector_sweep.go` (~400–550 LOC at completion — the only new
non-handler file; city_runtime.go is already 4,232 LOC, so folding the predicates into
it fails file hygiene, while the tick *body* is reused, not duplicated). All THREE
callers are wired in WD.1: the two scoped callers (:3626, cmd_start.go:1035) run the
same function with their existing narrowed inputs. `gc start` has no controller, so at
WE its detections execute the handler functions inline, sequentially per session —
durable fences (revision, instance token) remain the guards, as today.

**Cadence.** Unchanged: patrol ticker at `Daemon.PatrolIntervalDuration()` default 30s
(config.go:2703-2705, ticker city_runtime.go:1015-1016), every existing poke/event
trigger through `runTick` (:987-1007) under `safeTick` (:1004 — #663 preserved), and
the boot reconcile (:935). Event-driven keyed admissions (socket, wait-dependency,
pool hints, nudge wake) keep their own channels — the sweep is the periodic
anti-entropy layer over them, the level-triggered backstop role the tick has today.

**Inputs (all already loaded by the tick — no new reads beyond the two declared
carve-outs below).**
- session bead snapshot rows `OpenForReconcile()` (loaded/reloaded :1470/:1571/:1593)
- desired state + configured names (:1574-1583, :2817)
- pool desired counts `ComputePoolDesiredStates` (pure; :2750-2765, normally
  precomputed in `loadDemandSnapshot` :3878-3884) and named/routed demand (:2771)
- assigned-work beads + store refs (:2715-2731), ready-wait set (:2820-2824)
- provider-health snapshot `loadProviderHealthSnapshot` — one file read per sweep
  (moves from god-function prologue :1508)
- routed-work view — **one bounded `ReadyLive` read per store per patrol**, LANDED
  at WD.10b (Q2 resolved yes-with-promotion). This is not a new read: it is
  `readyDemandSnapshotFingerprint`'s former per-patrol enumeration *promoted* from
  hash input to the sweep's DECLARED routed-work view (`readyRoutedWorkView`,
  cmd/gc/ready_routed_work_view.go), which both invalidates the demand snapshot and
  enqueues exact `(workID, poolTarget, sourceStore)` keys into pool-allocation
  admission. Event-carried routed work is already exact-key covered
  (`admitReadyRoutedWorkEvent` → `LiveReadyByID` → keyed admission,
  api_state.go:801-884), so the view's residual value is event-silent raw-bd writes
  only — exactly the re-detection WD.10b owes. Declared on the same footing as the
  provider-health file read above: bounded and per sweep, not zero. The WE ledger
  entry is DISCHARGED: `readyDemandSnapshotFingerprint`,
  `writeReadyDemandFingerprintBead`, `flooredReadyDemandSnapshotFingerprint`,
  `readyDemandFingerprintFloor`, the `runtimeDemandSnapshot.readyDemandFingerprint`
  field and `requestReadyRoutedWorkLegacyFallback` (with its
  `readyRoutedWorkPokePending` flag) are all deleted. Invalidation moved from a
  fingerprint carried on the snapshot to a change edge detected at the read and
  consumed exactly once by the next refresh; WD.16's cadence floor rides the
  promoted read as `readyRoutedWorkViewFloor`, its tests moved with it
- liveness, two tiers: ONE `sp.ListRunning` per sweep — names-only
  (`internal/runtime/runtime.go:203`), so it can prove absence but NOT zombie-ness —
  for dead-orphan candidacy (the tick already pays it in `reconcilePoolDeaths` :1357);
  plus the existing two-bit probe `observeRuntimeProviderLiveness` (running, alive)
  (session_lifecycle_parallel.go:2142-2148) over **bead-awake rows only** (O(awake)),
  which defines `alive` for D-ZOMBIE (running ∧ !alive), D-WAKE, and D-SLEEP — a
  zombie IS in the running set; names-only data alone would leave zombies permanently
  stuck (never woken, never marked unhealthy, never drained)
- clock, config, `storeQueryPartial` flag

**What it computes.** Read-only, zero-store-write per-row predicates over the snapshot
(each family §3) with bounded in-memory detector state (consecutive-defer counters,
probe cursor, named-suspend confirm counts, breaker/health accrual);
`ComputeAwakeSet` (existing pure library, compute_awake_set.go:130) over the pinned
stable order (R3); circuit hydration (in-memory restore from snapshot rows,
session_circuit_breaker.go:329-389 — no store Get); idle-probe target selection.
Provider reads are bounded, not zero: O(1) ListRunning + O(awake) two-bit liveness +
O(timer-configured alive) `sp.GetLastActivity` for D-DEADLINE (idle_tracker.go:101-117)
and D-STALL (:2565). The structural win is what does NOT run fleet-wide anymore: screen
peeks (:2316), attachment probes, pending-interaction probes, fresh-liveness *proofs*,
and legacy's per-session GC_DRAIN_ACK poll (session_reconciler.go:2078, :2409 — not
inherited; D-DRAIN's ack read is handler-side) all move handler-side, per enqueued
key, under D2 capabilities. Detection also
screens D2-incapable providers for stop/kill families (pure config predicate on the
resolved provider type): traced refusal, no enqueue — no 30s re-enqueue treadmill.

**Output** (act mode — §3's per-family shadow rule governs when enqueueing begins).
`(sessionID, condition, evidence)` enqueued into the existing controllers:
- session-start controller for all lifecycle conditions — via `Admit(id, source)`
  (session_start_controller.go:204) with new `sessionStartAdmissionSource` values per
  condition family (existing enum, new values; key = bare durable session ID,
  validation :642-672), or via the existing certified-lease Admit* entries for wake
  families (:255-277), certified at detection exactly as the socket path certifies
  today (city_runtime_session_start.go:652-680);
- nudge-key controller for due nudges (already exists: `admitDueExactNudges`
  city_runtime_nudge.go:143);
- wait-dependency producer (already exists; sweep only redrives census, as :994-996
  does today);
- pool-allocation admission for routed-work fill (already exists via
  `enqueueRoutedWorkPoolAllocation` :376).

**Cost model.** O(rows) predicate evaluation + O(fleet) ComputeAwakeSet (both already
paid by legacy) + the bounded provider reads above + the one declared per-store
`ReadyLive` routed-work read from WD.10b onward (which replaces, rather than adds to,
the fingerprint scan the tick pays today) + **zero store mutations, zero provider
mutations, zero domain writes**. The sweep's only writes are trace-subsystem
appends (WAL records; NOT arm mutations — the shadow reason vocabulary never triggers
`ensureAutoArm`, §3), and those are the campaign's evidence, not a side effect.

**Degradation when a queue is full.** Detector conditions are level-triggered and
derived purely from durable state: a dropped enqueue is re-detected on the next sweep,
so overflow loses no work, only latency. Session-start overflow already returns
`sessionStartAdmissionOverflow` and arms `auditPending` + authoritative reseed
(session_start_controller.go:324-329, city_runtime_session_start.go:247-259) — the
sweep traces the overflow and moves on. It must NOT block, retry-loop, or fall back to
acting itself. (The pool-allocation 256-slot channel silently drops hints. Before WD.10b it
recovered via `requestReadyRoutedWorkLegacyFallback`; that legacy crutch is DELETED
and recovery is census-owed re-detection — `recordRoutedWorkPoolAllocationOverflow`
traces the dropped key and returns, and the next patrol's declared routed-work view
re-raises the same durable condition. The same rule now governs an unhandled or
failed keyed allocation: no fallback poke, so a routed key never crosses back to the
legacy pool builder.)

**Global guards (fail-closed, fixing two legacy trace lies).** On
`storeQueryPartial`, the sweep suppresses every destructive family (close, stop, drain,
rollback, retire) — preserving the gc-hz0nu guard (test :1045) — and, because
suppression happens *before* any trace record, the legacy hazard of recording
`Outcome=Closed` and then not closing (:1987-1991, :2284-2288) disappears by
construction. Unknown-state rows are skipped with the existing throttled diagnostic
(:1802-1814) and excluded from every other detector.

## 3. Per-detector specs (PORT rows)

Shared handler shape (all families): the session-start controller's per-key reconcile
re-reads the authoritative row (revision + instance token captured), dispatches on the
*durable row state* — the detector's reason is a hint, never authority (the .102
pattern, session_start_reconcile.go:1280-1356) — runs the family's admission checks,
requires D2 capabilities for any stop/kill, applies exactly one fenced effect
(`UpdateIfMatch` / token-bound stop / atomic close), and fires the **same legacy
TraceSite constant** with `effect_owner:"keyed"`, `effect_applied:true`. Handlers live
in per-family files beside session_start_reconcile.go; the dispatch point is one
routing block added once (WD.2) — no new framework.

Shared shadow shape (D4, every family). **The shadow/act rule is per-family and
explicit, NOT the ownership latch**: a detector-fed family is **shadow-only — it
never enqueues — regardless of `sessionStartOwnershipState()`, until its own act
constant is flipped**. A constant flips only when BOTH that family's keyed
handler and its legacy yield have landed, which is the end of that family's
slice: D-DEADLINE flipped at WD.2 and D-DUP at WD.13; every remaining family
flips in the WE cutover commit. The latch cannot gate this: it is a
single start-scoped tri-state (city_runtime_session_start.go:23-29) that reads Keyed
in the very auto-mode campaign city D4 mandates (DESIGN.md:100-105), while legacy
yields only at the start-family seams (`keyed_start_owner` skips,
session_reconciler.go:1739/:3770) — there is NO legacy yield for idle stop, orphan
close, drift, stall, dup, or stranded — a latch-gated enqueue would double-act beside
a non-yielding legacy — which is why WD.2 landed
`withLegacyDeadlineStopExclusion` in the same commit that flipped D-DEADLINE. The
rule is one compile-time constant per family, not config. Detectors always run
(read-only); in shadow they record the same legacy TraceSite with
`effect_applied:false`, `effect_owner:"detector-shadow"`, and the predicted
(reason, outcome) where §3b grants decision-level parity — mirroring the five existing
shadow sites (city_runtime.go:575/:635, city_runtime_wait_dependency_index.go:410,
city_runtime_session_start.go:371, nudge_dispatcher.go:356; pattern:
session_lifecycle_status_heal.go:24-62). Handlers are proven per slice in
require-mode test-city journeys; in production a family first acts when its own
constant flips (D-DEADLINE: WD.2; the rest: WE). **State honestly
what the auto campaign therefore exercises**: detection
(and where granted, decision) parity for the new families, plus full act-level parity
for the already-keyed start families — D4 sign-off targets exactly that scope (§3b).

Recording path (non-perturbing). Detector-shadow records use a **shadow-distinct
reason vocabulary** (`detector_`-prefixed, e.g. `detector_config_drift`) deliberately
outside `shouldAutoArmForTrace` (session_reconciler_trace_collector.go:888-903), so a
shadow record can never trigger `ensureAutoArm` — no arms.json writes, no detail-scope
mutation, no consumption of the 4 auto-arm slots (:20). Invariant: detector-shadow
predicted outcomes must NEVER use `failed`, `provider_error`, or `deadline_exceeded` —
the auto-arm OUTCOME leg (:898-901) triggers on those regardless of reason. §3b
already excludes error/probe arms from prediction, so this holds today; the invariant
ensures a future arm addition cannot silently re-open auto-arm writes from the sweep.
Volume is budgeted: per family per cycle the sweep emits a bounded record count plus a
summary beyond it, so doubled volume stays inside the 4000-records/cycle cap (:18).
Armed campaign templates bypass the 400-cap pending stash entirely (`detailSource`
short-circuits at :735) — the actual reason FIFO eviction cannot corrupt the join: the
stash only ever holds unarmed templates. Operational rule: WD.15 excludes from the
parity readout any cycle whose rollup shows `record_budget_exceeded` drops (counted at
:286, surfaced in the cycle rollup :996-998). Site codes stay the legacy constants, so
`gc trace` remains continuous.

Campaign arming and durability (this bites from WD.2 onward, not just WD.15).
Unarmed detail records are NOT durable: the record path stashes them per-template
(cap 400, FIFO-evicting, :335-353) and returns before `addRecord`
(trace_collector.go:735-741); `End()` copies only `c.records` (:930-938), so
stashed-and-never-promoted records are discarded — and auto-arms cover only anomalous
reasons/outcomes, 4 slots, 10-minute expiry (:888-903, :853). **An unarmed campaign
produces zero evidence.** Therefore every campaign city (and every slice's D4 evidence
run) manually detail-arms EVERY template via `gc trace start --for <window>` (default
15m, arbitrary durations accepted — trace_cmd.go:276, :839; arms persist in the arm
store); WD.15's harness verifies arms at every sample boundary and re-arms on
expiry/restart before counting a joined cycle.

Parity = per-trace-cycle join on (session, site code): legacy record with effect vs
detector-shadow record without, classified matched / mismatched / incomparable via
the existing comparator vocabulary (session_lifecycle_shadow_compare.go:14-41),
against the §3b expected-classification table. Double-acting is impossible by
construction: detection is read-only and shadow families never enqueue before WE.

- **D-DEADLINE** (IdleTimeout + MaxSessionAge). Condition: `it.checkIdle` via
  `sp.GetLastActivity` (idle_tracker.go:101-117) or `maxAgeTr.shouldRestart` anchored
  on `CreationCompleteAt` (:3283), then the existing pure ladders
  `sessionpkg.DecideIdleTimeout` / `DecideMaxSessionAge` (blockers: user-hold,
  quarantine, pending interaction, awake assigned work; consecutive-defer backstop
  :3400-3410 kept as detector state). Handler: re-read row; re-verify deadline from
  durable timestamps; token-bound unattended stop + fresh-death confirm (reuse .102's
  `workerStopUnattendedSessionByIDWithConfig` + `confirmDrainAckRuntimeDeadCompletion`,
  session_start_reconcile.go:1320-1323); persist SleepPatch (`sleep_reason`
  idle-timeout|max-session-age) BEFORE key release so D-WAKE cannot same-tick respawn
  (replaces legacy's optimistic-fold asymmetry :3350/:3476 with admission version
  fencing :1030-1037); emit SessionIdleKilled/SessionMaxAgeKilled. RED:
  seeded v59 row past deadline → handler stops and persists sleep_reason; negative:
  user-hold row → zero provider calls (port :9609's assertion to the keyed path).
- **D-ORPHAN** (Orphaned + CloseOrphan + CloseFailedCreate). Condition: open row not in
  desired set; running-set membership splits live-orphan vs dead-orphan; failed_create
  with expired lease (`pendingCreateLeaseExpiredForRollbackInfo`). Suppressors at
  detection: open assigned work (kept-open :2193-2204), named suspend deferred-confirm
  tick count (:2216-2231, counter moves into the detector), pending-create still
  leased, storeQueryPartial. Handler: dead → proven-absence close
  (`closeSessionBeadIfReachableStoreUnassigned` behind fresh-liveness proof; liveness
  error → typed refusal, zero effect, matching :2277's fail-closed); live → begin
  keyed drain (D-DRAIN). Trace: Orphaned/CloseOrphan/CloseFailedCreate. RED: undesired
  dead row → closed with close_reason; negative: liveness-error → refusal with zero
  writes; journey covers :11968's close-and-refill end-to-end.
- **D-STALE-CREATE** (PendingCreate + PendingCreatePreserved). Condition: pending
  create/creating state, lease expired, no runtime in running-set. Handler: reuse the
  keyed start path's existing rollback machinery (it already rolls back its own failed
  starts — `TraceSiteLifecycleStartRollback`); still-leased → no-op with
  PendingCreatePreserved trace. Post-WE all creates are keyed-initiated, so this is
  pure crash recovery. RED: expired-lease row → rolled back + claim cleared (port
  :7764); negative: fresh lease untouched (:7709).
- **D-DRIFT** (ConfigDrift + LiveDrift). Condition: `StartedConfigHash !=
  CoreFingerprint(current)` or live-hash mismatch — pure hash compares over snapshot +
  config. Handler ladder (order preserved from legacy): version-mismatch → silent
  rebaseline write; launch-only drift → relaunch (:5948-6082 helper reused);
  named+detached → restart-in-place; attached/recently-attached/active/pending →
  persist deferral, cancel queued drift drain (A6 attached-user safety); else begin
  drift drain with `driftDrainTimeout`. Live-drift rebaseline/backfill and `RunLive`
  re-apply fold into the same handler. RED per arm; the attached-deferral negatives
  port :9086/:9190 verbatim; journey: edit agent config in a live v59 city →
  detached session relaunches, attached session untouched.
- **D-SLEEP** (DrainDecision + idle-probe selection). Condition: alive ∧ not in awake
  set (ComputeAwakeSet + sleep-policy/ConfigSuppressed pass :3550-3591); user-hold
  keep-alive escape kept (:3857-3860, #3994). Round-robin probe budget
  (`maxIdleSleepProbesPerTick`, :5544-5605) stays detector-side (fleet-shaped rate
  limit); probe *launch* (`WaitForIdle`) moves handler-side. Handler: verify no-wake
  against durable intent, `markIdleSleepPending`, begin drain (enqueue-only semantics
  of session_wake.go:203-233 preserved — the one-tick rescue window is a behavior,
  not an accident). RED: suppressed session drains; negative: user-hold heartbeat
  session never drains.
- **D-DRAIN** (DrainAck + DrainCancel + DrainAdvance). ARCHITECT DECISION (recorded):
  **ack discovery is HANDLER-side.** Sweep condition: tracker-state only — any session
  with drain intent recorded / tracker in draining state is enqueued; the sweep
  performs NO `GetMeta(GC_DRAIN_ACK)` read (the tracker cannot distinguish
  awaiting-ack from acked — all ack reads are provider `GetMeta`,
  session_wake.go:480/:499 — and it does not need to: the handler decides).
  The handler performs the ack read (`isDrainAcked` = `sp.GetMeta(name,
  "GC_DRAIN_ACK")`, cmd_runtime_drain.go:114-120) plus generation-stale,
  process-exited, and cancel-cause checks; re-enqueue-until-ack is finite because
  every drain is bounded by ack-or-timeout. Handler effects: the existing keyed
  drain-ack stop (session_start_reconcile.go:1366-1647, fence :961-995,
  atomic-close-before-stop A5) absorbs the stop leg; advance/complete/cancel effects
  (`completeDrain`, `verifiedInterrupt`, ack metadata writes, session_wake.go:529-742)
  move behind the exact key. Drain tracker stays in-memory (Q4). RED: acked drain →
  stop-pending + async stop exactly once (port :936); negatives: stale generation →
  cleared not stopped (:800), assigned work → canceled (:1070).
- **D-WAKE** (WakeDecision + PreserveConfiguredNamed + rate-limit/churn accounting).
  Condition: in awake set ∧ not alive; preserve = configured-named rows outside
  desired are simply never enqueued for close (detector-side, replacing :1902-1954's
  screen; re-admission :2040-2062 becomes ordinary named-wake demand). Enqueue via
  the existing certified wake leases (configuredNamedWake / strictDefaultPoolWake /
  configuredDependency; pool fill via pool-allocation). Handler: the existing
  `reconcileExactSessionStartWithOwner` admission chain (:1170-1980) unchanged —
  blockers (:1793-1797 quarantine/hold, closing legacy's untraced quarantine skip
  :3702-3705 with a traced refusal), circuit + provider-health gates (:1846-1858),
  exactly-once start (A2). Rate-limit screen peek (`checkRateLimitStability`,
  session_reconcile.go:505-533) and churn/wake-failure accounting (:642-678, :714-730;
  `recordWakeFailure` already shared via `commitStartFailure`
  session_lifecycle_parallel.go:2531) move into the handler's failure path. RED:
  pool-under-min → keyed fill (turning the request-count-only legacy tests :568 into
  a real started-session assertion, corpus's known weak spot); negative: quarantined
  named session stays asleep after churn (:5009).
  **WD.10a amendments (five, binding; recorded on ga-f7v2ft.116 from the ga-ij8mh
  adjudication).** (1) TARGET SHAPE: the configured-dependency arm's target is the
  CANONICAL SINGLETON shape — accept
  `isCanonicalPoolManagedSessionInfoForTemplate` (session_name_lookup.go:52-63) beside
  the origin-less legacy shape; the two wake families partition on **slot markers, not
  on `pool_managed`**, so slotized rows (pool_slot / trigger-bead / PoolSessionName
  naming) stay with strict-pool. (2) SECOND ENTRY GATE, beside Q1: the pre-lease
  ownership seam. The pool-managed→legacy arm of `classifyExactSessionStartOwnership`
  (session_start_reconcile.go:2550) holds the wake-consume race open (wake write →
  BeadUpdated → in_process admission → legacy yield → fallback poke → legacy
  PreWakePatch consumes `wake_request`); WD.10a must either carve certified wake-family
  targets out of that arm or make detection-side admission the sole keyed entry, and
  state which, with a RED for the losing interleave. (3) Q1 is unchanged, but record
  alongside it that `poolAllocationShadowDependencies` (pool_allocation_shadow.go:82-86)
  categorically excludes dependency-bearing agents from the strict-pool lattice — that
  exclusion is WHY this family must own the singleton shape; do not close the hole by
  making dependency-bearing agents pool-eligible. (4) SWEEP RULE:
  `sweepUndesiredPoolSessions` / `GCSweepSessionBeads` (city_runtime.go:3419-3424,
  pool_session_name.go:81-87) must not reap a canonical singleton row whose explicit
  wake is current — spec + RED. (5) ACCEPTANCE: the v59 journey's dependency leg
  re-lands against a SYNC-PRODUCED canonical singleton row (materialized by the
  production path, never fabricated), asserting keyed lease admission + exactly-once
  start + dependency-alive gating within an absolute budget under active legacy at
  debounce-0, and the existing unit tests re-anchor on the same shape.
- **D-ZOMBIE** (TerminalProviderError). Condition: `running ∧ !alive` from the sweep's
  two-bit `observeRuntimeProviderLiveness` probe over bead-awake rows (§2 inputs) —
  matching legacy's own zombie predicate (:2324). NOT "not in running-set": a zombie
  IS in the names-only running set, and the anchor test (:10760) fails the naive
  condition. No screen peek in the sweep. Handler: fresh-liveness proof, then
  scrollback peek + `ProviderTerminalErrorReason` classify + `markProviderTerminalError`
  + SessionCrashed event (I/O handler-side). RED: port :10760's four-field assertion.
- **D-STALL** (progress-stall recycle + ProgressStallExempt). Condition: activity gap >
  configured stall threshold (:2562-2569, `sp.GetLastActivity` :2565). The min-floor
  exemption (fleet-shaped `openPoolSessionCountForTemplate`, computed detector-side)
  suppresses enqueue **for the claim-less arm ONLY** — mirroring legacy, where
  floorExempt gates `sessionProgressStalled` (:2636) but the claim-holder arm gates on
  `exempt` alone (:2648). When `claim_holder_stall_timeout > 0`, floor workers ARE
  enqueued and the handler runs the per-session claim lookup (:2611's condition),
  recycling confirmed stalled claim-holders; the threshold<=0 fast path is preserved
  (no enqueue, no lookup). Handler: recycle via restart_requested/continuation-reset
  writes, converging with .103's keyed reset machinery; per-session re-checks only
  (A1). RED: port the :538/:569 pair keyed PLUS re-point
  `ClaimHolderStallKeepsPoolClaimForFreshWorker` (progress_test.go:431) keyed — the
  floor worker's claim must survive detection.
- **D-DUP** (HealRetire). Condition: >1 open continuity-eligible rows sharing a named
  identity; winner selection (generation → canonical name → created-at) computed
  detector-side; each loser key enqueued. Handler: stop-before-mutate, archive loser,
  re-point work/wait/nudge beads to winner (session_beads.go:609-678 logic behind the
  key). Expired hold/quarantine timer heal does NOT get a detector: the wake handler
  clears expired timers at admission (it re-reads the row anyway). RED: port
  session_beads_test.go:1849 + stop-failure negative :1976.
- **D-STRANDED** (WakeSleep dead-pool arm). Condition: dead pool slot with stranded
  marker/confirmation window elapsed. Handler: stranded diagnostic, repair
  (unassign/reopen work), close bead preserving sleep_reason as close reason, prune
  worktree if safe (:3897-3979). RED: stranded slot repaired + work reopened; negative:
  inside confirmation window → untouched.

Circuit/health (sites 16-17,23): the sweep hydrates the breaker singleton from snapshot
rows, observes progress signatures (in-memory), and accrues provider-health episodes.
**Reset-persistence ownership moves to the wake handler**: legacy persists
cooldown-expired auto-resets every tick (session_reconciler.go:1629-1641), while the
keyed gate ORs the RAW persisted string into `circuitOpen`
(session_start_reconcile.go:817/:1847) and refuses with zero writes — a zero-write
sweep would strand a durable "open" string the refusing handler never clears, losing
auto-recovery post-WE. Fix: the wake handler derives `circuitOpen` from the hydrated
model (which applies `maybeAutoReset`), not the raw string, and persists the reset via
`persistSessionCircuitBreakerMetadata` BEFORE evaluating the gate; trip accounting
stays at the shared start-failure write (session_lifecycle_parallel.go:3039). WD.11
RED: persisted-open breaker past cooldown + controller restart → keyed wake eventually
starts the session. Plus the missing respawn-gate integration RED (legacy's
:3755-3766 `continue` was never integration-tested).

### §2/§3 implementation deltas (recorded at WD.1)

Where the sweep as built diverges from §2/§3 as written. Reported, not improvised —
verbatim from ga-f7v2ft.107's closing notes and the WD.1 commits (fbcaaa34c9,
bb36c285f4, 60c0d9d2a0, f0c525dae9).

1. The unknown-state guard cannot call `emitSessionUnknownStateDiagnostic` in shadow:
   that helper SetMarkers and emits events, and the sweep is zero-write. The sweep
   skips the row and records a `detector_unknown_state_skipped` shadow at the legacy
   site; the throttled diagnostic stays legacy-owned this wave.
2. D-STALL has no legacy trace site for its recycle arm (legacy only traces
   ProgressStallExempt), so both stall arms record at
   `TraceSiteReconcilerProgressStallExempt` with different detector reasons.
3. `gc start` and `controlDispatcherTick` have no trace cycle, so the sweep runs there
   for its guards and cost profile but records nothing.
4. §2's "each family §3" per-row list has no precedence rule; legacy's forward pass
   early-continues after drain, orphan and stale-create. Without mirroring that, the
   sweep raised extra sleep/wake conditions on rows legacy had already routed to a
   close (fixed in bb36c285f4: detectDrain, detectOrphan and detectStaleCreate report
   whether they claimed the row, and the remaining families then run in legacy's order
   — drift, zombie, deadline, stall, wake/sleep, stranded).
5. D-DUP cannot key on canonical identity alone: unstamped pool rows all resolve to
   the template's qualified name, so it keys on named identity (`isNamedSessionInfo`)
   — negative test added.
6. Shadow records at legacy site codes collide with existing tests that count by
   (site, outcome). One collision found repo-wide
   (`TestSessionReconcilerTraceGH1654WorkRequestedStartCandidates`); re-pointed to
   filter on `effect_owner`, which exists precisely to separate the legacy, keyed and
   detector-shadow populations on a shared cycle.
7. §1's table has 28 sites and §3 names eleven families, but main's two new unbounded
   per-tick patrol scans — `readyDemandSnapshotFingerprint` (city_runtime.go:4058) and
   `reconcileExecutionCompletions` (api_state.go:585, patrol phase :1698) — contradict
   A1 and belong in the inventory. WD.1 carries them as **OBSERVED-ONLY** family
   members (`detectorFamilyReadyDemandScan`, `detectorFamilyExecutionCompletionScan`,
   `ObservedOnly: true`), so they are counted and traced but never enqueue;
   absorption-or-retirement is adjudicated at WD.10b (see the §2 routed-work input)
   and WE. **WD.10b ABSORBED the ready-demand scan**: the sweep now owns that read as
   its declared routed-work view, so `detectorFamilyReadyDemandScan` and
   `detectorActReadyDemandScan` are retired — there is no foreign scan left to
   observe. `detectorFamilyExecutionCompletionScan` stays observed-only for WE.

### §3 D-DEADLINE deltas (recorded at WD.2)

Where the first ACTING family as built diverges from §3 as written. Reported,
not improvised.

1. **The decision ladder runs handler-side, not detector-side.** §3's D-DEADLINE
   entry puts `DecideIdleTimeout`/`DecideMaxSessionAge` and their blockers in the
   *condition*; §2's cost model and §3b's own matrix put pending-interaction
   probes and store scans handler-side. §2/§3b win, because the two rungs below
   the blocker are provider I/O (`pendingInteractionKeepsAwakeInfo`) and a
   reachable-store scan (`sessionHasAwakeAssignedWorkForReachableStore`), and a
   zero-store-read sweep cannot pay either fleet-wide. The sweep evaluates the
   one rung it can compute purely — the durable `held_until`/`quarantined_until`
   blocker via `lifecycleTimerBlockerInfo` — records the deferral for the join
   under `detector_deadline_deferred`, and never enqueues a blocked row. The
   handler then runs the whole ladder per key, with real gathers and legacy's
   fail-closed error mapping. The consecutive-defer backstop (ga-nllza6) travels
   with the ladder for the same reason: legacy yields the key, so its own
   backstop would never see the defer.
2. **The D2 screen is an interface assertion in the routing seam, not a config
   predicate in detection.** §2 says "pure config predicate on the resolved
   provider type"; the capabilities are actually carried as
   `runtime.FreshLivenessObserver` + `runtime.UnattendedSessionStopper` on the
   provider value the handler itself asserts, so asserting the same pair is
   exact rather than a second, driftable spelling. Putting it in
   `routeDetectorConditions` rather than in `detectDeadline` also keeps the
   shadow record intact for the parity join: the condition is still detected and
   recorded, it just carries `admission_outcome=refused_provider_incapable` and
   no enqueue. Refused every sweep, so no treadmill forms.
3. **The sleep patch takes the CAS fence when the store has one and the front
   door when it does not.** Conditional writes are a per-store gated capability
   that is off by default (`beads.ResolveConditionalWriter` returns a nil writer
   on an unset/off mode), so requiring one would have made the whole family yield
   on most cities. `persistExactSessionDeadlineSleep` fences on the pre-stop
   reread's revision with one bounded retry where a writer exists, and otherwise
   writes the same patch through the same `sessionFrontDoor(...).ApplyPatch` the
   legacy arm uses. What is unconditional is the ORDERING the design actually
   asks for: the patch lands before the key is released. A resolution *error*
   (conditional writes required but unavailable) still fails closed.
4. **A routed record carries `effect_owner=keyed` with `effect_applied=false`.**
   §3 says the family fires the legacy site with `effect_owner:"keyed"`,
   `effect_applied:true` — that is the HANDLER's record, written when the effect
   lands. The sweep's own record for a routed condition flips only the owner (the
   key belongs to the keyed population from admission onward) and adds
   `admission`/`admission_outcome`; claiming `effect_applied:true` at enqueue
   time would re-open exactly the trace lie §2's partial-store ordering closes.
5. **Legacy's deadline arms needed a NEW exclusion, not an existing one.**
   `sessionStartLegacyExclusionPredicate` answers "does keyed own this row's
   START family", which is true for rows legacy must stay free to idle-kill;
   reusing it would have silently disabled legacy idle kills fleet-wide. WD.2
   adds `withLegacyDeadlineStopExclusion`, backed by
   `sessionStartController.ownsDeadlineStop(id)` — true from Admit until the
   handler succeeds or exhausts. Note the difference from the start seam: this
   is not a race to lose. Both writers read the SAME tracker on the same tick,
   so an acting D-DEADLINE beside a non-yielding legacy double-stops by
   construction, and legacy's kill targets the session NAME, which a replacement
   incarnation may already own. The arm retires at WE with the god function.
6. **Only the patrol/boot call site routes.** `controlDispatcherTick` and
   `gc start` pass no `Admit` hook, so their sweeps stay read-only — the patrol
   sweep is the anti-entropy layer §2 describes, and doubling the enqueue rate
   from a narrowed-input call site buys nothing a coalesced key does not already
   give.

### §3 D-ORPHAN close deltas (recorded at WD.3)

Where the family's CLOSE arms as built diverge from §3 as written. Reported,
not improvised. WD.3 flips `detectorActOrphan` for the close outcome ONLY —
`detectorAdmissionSourceFor` routes `TraceOutcomeClosed`, so the live-orphan
drain arm and the running-set-unavailable refusal keep recording shadow until
WD.4 lands their handler and yield.

1. **Undesiredness is the one input no per-key predicate can answer, so the
   tick publishes it.** Every other rung of this family is durable (state,
   pending-create lease, named spec, state_reason) or provable per key
   (fresh liveness, reachable-store assigned work). Undesiredness is not: it
   falls out of pool desired counts, named specs and demand, and recomputing
   `buildDesiredState` per key would turn an O(1) handler into an O(fleet)
   one. `beadReconcileTick` therefore publishes its desired-name view through
   `CityRuntime.publishDesiredSessionNames` immediately before the sweep, and
   the handler reads it back through `exactSessionStartParams.
   DesiredSessionNames` — the same threading WD.2 used for the idle and
   max-age trackers, and for the same reason: keyed and legacy must answer
   from one fleet input, never two. A nil accessor or an unpublished view
   fails the close closed. Only the patrol/boot call site publishes:
   `controlDispatcherTick` and `gc start` build NARROWED desired states, and a
   narrowed view would read as fleet-wide undesiredness (this is the publish
   half of WD.2's delta 6).
2. **The legacy yield is a SIBLING of `withLegacyDeadlineStopExclusion`, not a
   widening of it.** WD.2's ownership-semantics test asked whether the
   existing predicate answers the question the new arm needs;
   `ownsDeadlineStop` answers "is a D-DEADLINE stop in flight for this key",
   which is false for every orphan-close admission. Widening it to accept both
   sources would have made legacy's idle and max-age arms stand down for rows
   the keyed ORPHAN handler owns (and, symmetrically, legacy's close arms stand
   down for deadline-owned rows) — the same silent-disable trap WD.2 recorded
   when it declined to reuse `sessionStartLegacyExclusionPredicate`. WD.3 adds
   `withLegacyOrphanCloseExclusion` backed by
   `sessionStartController.ownsOrphanClose(id)`, installed in the same block
   and the same window as the deadline option. Both retire at WE.
3. **`.78.6`'s atomic terminal closure reaches this arm through the close
   helper's single transaction, not through `CloseWithMetadataIfMatch`.** §3
   names `closeSessionBeadIfReachableStoreUnassigned` as the reuse, and that
   helper routes to `closeBead`/`closeFailedCreateBead`, which commit the
   terminal metadata batch and the status close in ONE `store.Tx` — the
   ga-igcny0.1.1 property — AND run the extmsg cancel plus the
   orphaned-work release cascade a bare conditional closer skips. Lifting the
   A5 fenced-close block here would have had to replicate both cascades and
   the failed-create claim clears, so the fence WD.3 adds instead is the
   authoritative pre-close match on revision, instance token and name, plus a
   post-close terminal witness: `effect_applied:true` is recorded only when
   the durable row actually shows the close and its canonical close_reason.
4. **The detection-side D2 screen stays the shared stop-capable pair, though
   the close needs only half of it.** `routeDetectorConditions` screens every
   destructive family with `detectorProviderStopCapable`
   (`FreshLivenessObserver` + `UnattendedSessionStopper`); the close arm
   performs no stop and the handler asserts only `FreshLivenessObserver`. The
   screen is therefore stricter than the effect requires. Left shared on
   purpose: the D2 hard capabilities travel together on tmux/auto/hybrid, a
   per-family screen would be a second spelling of the same fact, and the
   over-strict direction is a traced refusal, never an unproven close.
5. **The kept-open suppressor is snapshot-side, and the live re-query is still
   the authority.** §3 lists "open assigned work (kept-open :2193-2204)" as a
   detection-side suppressor; legacy answers it with a live store query the
   sweep cannot pay fleet-wide. The sweep answers it from
   `sessionBeadHasAssignedWorkInfo` over the assigned-work beads the tick has
   already loaded — zero new reads — records
   `detector_orphan_assigned_work` and never enqueues. A row whose work is
   invisible to that snapshot is still refused inside
   `closeSessionBeadIfReachableStoreUnassigned`, which re-queries live and
   fails closed on error. Expected-divergence for §3b: the two views can
   disagree on a row whose assigned work is not wake-relevant, which shows as
   a detector kept-open against a legacy close.
6. **The seam case is ordered BEFORE D-DEADLINE.** Legacy's forward pass runs
   its not-desired block before the deadline arms and early-continues, and the
   sweep's own family precedence puts orphan ahead of deadline. An undesired
   awake row past its idle deadline is therefore the orphan family's; the
   handler-dispatch switch mirrors that order so the two paths cannot disagree
   about which family owns such a row.

### §3 D-ORPHAN drain deltas (recorded at WD.4)

Where the family's live-orphan DRAIN arm as built diverges from §3 as written.
Reported, not improvised. Q4 is resolved on ga-f7v2ft.110 in the KEEP direction,
so drain intent stays in the in-memory `drainTracker` with the one-tick
deferred-interrupt rescue window — today's semantics, and WD.6 inherits it.

1. **One act constant per EFFECT ARM, not per family.** §3's shared shadow
   shape says "one compile-time constant per family". D-ORPHAN has two
   effect arms with two handlers and two legacy yields, which landed a slice
   apart, so one constant would have crossed the drain arm at WD.3 — beside
   a legacy drain that did not yet yield. WD.4 splits `detectorActOrphan`
   into `detectorActOrphanClose` and `detectorActOrphanDrain`, and
   `detectorFamilySpecs` ORs them back into the family's single `Acts` bit,
   so the family-spec structure the rule is expressed in is unchanged. The
   act-frontier pin now maps a family to a SET of routing outcomes and
   additionally asserts that no two effect arms share an admission source.
2. **The two arms split by OUTCOME in `detectorAdmissionSourceFor` and by
   LIVENESS in the handler — and the handler's split happens inside the
   close arm's single observation.** Detection can separate them from the
   running set; the per-key handler cannot, because the fact that separates
   them is provider I/O. A second admission under the drain source would
   re-probe, and a second probe may disagree with the first, leaving the row
   owned by neither arm. So `reconcileExactSessionDetectorFamily` keeps ONE
   orphan case (its guard is the shared durable-row candidate) and
   `reconcileExactSessionOrphanClose` hands a live row straight to
   `reconcileExactSessionOrphanDrain` from inside its own liveness
   observation. The sources still differ because the two LEGACY yields
   differ.
3. **The named deferred-confirm counter is a SECOND counter, and it gates
   the ENQUEUE, not the handler.** §3 says the counter "moves into bounded
   in-memory detector state"; it moves as `detectorSuspendDeferralTracker`,
   a detector-owned map pruned at the end of every sweep so it counts
   CONSECUTIVE candidacy and stays bounded by the live fleet. Legacy keeps
   its own `drainTracker.suspendDeferrals` window, which is what §3b's
   "deferred-confirm off-by-one (duplicated counters)" already anticipates;
   sharing one counter would have coupled the windows and made the join
   unable to tell the paths apart. The handler does NOT re-derive it: like
   undesiredness it is fleet-tick state — a count of consecutive sweeps — so
   the detector simply withholds the key until the window elapses, and no
   key inside the window ever reaches a handler. Only the patrol/boot call
   site supplies a tracker (the publish half of WD.2's delta 6 again); a nil
   tracker fails CLOSED, deferring rather than draining.
4. **A6 becomes an explicit handler rung, and it is a STRENGTHENING of
   legacy.** Legacy's orphan drain has no attachment check, so it will
   interrupt a session a human is attached to on the tick after it begins.
   Attached-user safety is a KEEP invariant of the whole redesign
   (DESIGN.md §2), not a D-DRIFT-local policy, so the keyed arm refuses with
   zero effect while `IsAttached` or a pending interaction says a person is
   engaged. It deliberately stops at those two rungs:
   `namedSessionActiveUseReasonInfo`'s remaining rungs (`activity_unknown`,
   `recent_activity`) are config-drift policy and would defer orphan drains
   indefinitely on every provider that cannot report activity. The refusal is
   level-triggered, so the drain proceeds once the human detaches. **This is a
   new expected-divergence class for §3b's D-ORPHAN row** — keyed defers where
   legacy drains an attached row — and it must be triaged into that cell
   rather than counted as a mismatch.
5. **The legacy yield sits at the drain BEGIN, not at the top of the arm.**
   Legacy keeps raising and tracing its own kept-open and deferred-confirm
   records above the yield, so the join keeps both populations on the
   deferring ticks; only `beginSessionDrainInfo` stands down. Both writers
   share ONE in-memory tracker on one tick, which makes the yield mandatory
   rather than best-effort: an un-yielding legacy would not merely
   double-begin, it could win and stamp its own reason on the keyed arm's
   drain. `withLegacyOrphanDrainExclusion`/`ownsOrphanDrain` are siblings of
   the close pair for the reason WD.2 and WD.3 both recorded.
6. **A LIVE failed-create row is not a drain target.** The shared candidate
   returns `failed_create` for such a row, and legacy's failed-create arm is
   dead-only, so the drain arm accepts only the `orphaned` and `suspended`
   reasons and a live failed-create row keeps WD.3's kept-open refusal.
   Detection never routes one here anyway — `detectOrphan` classifies
   failed-create before the running-set split — so this is the handler
   agreeing with the detector rather than a second opinion.
7. **Exactly-once is enforced by family precedence, not by a handler
   latch.** Once intent is recorded, `detectDrain` claims the row before
   `detectOrphan` runs (WD.1's delta 4 ordering) and D-DRAIN does not act,
   so the orphan family never re-enqueues a key it just drained: no
   treadmill and no second intent, with no new state. A key carried back in
   by some other admission still lands on WD.3's active-drain refusal, which
   is correct — advancing a drain is D-DRAIN's (WD.6).

### §3 D-SLEEP deltas (recorded at WD.5)

Where the no-wake family as built diverges from §3 as written. Reported, not
improvised.

1. **The effect arm is NARROWER than legacy's, and the narrowing is the
   handler-answers-from-the-row rule, not a shortcut.** §3's condition is
   "alive ∧ not in the awake set". Legacy's own drain arm stamps one of
   five reasons for such a row (session_reconciler.go:3973-3985), and the
   last of them — plain `no-wake-reason` — means "ComputeAwakeSet found no
   reason to be awake", a FLEET verdict over pool counts, named and routed
   demand and the ready-wait set. No per-key predicate can re-derive it, and
   the seam's rule is that the detector's reason is a hint while the row is
   the authority. Unlike WD.3's undesiredness, publishing the fleet view
   would not help: the reason ladder is not one bit but a five-way choice
   whose branches carry different effects (the `idle` branch marks
   `sleep_intent` and waits on a probe; the others drain immediately), so a
   published verdict the handler cannot check would be exactly the trusted
   reason the seam forbids. WD.5 therefore acts on the two rungs the handler
   CAN re-derive per key — a durable `sleep_intent`, and the sleep-policy
   suppression pass — and records the fleet-only rows under
   `detector_no_wake_fleet_only` with a non-routing outcome, leaving them to
   legacy for this wave. They come back with D-WAKE (WD.10a/b), which is
   where the fleet demand rungs get their keyed home.
2. **The sleep-policy/ConfigSuppressed pass moves INTO detection, minus its
   two provider probes.** §3 names the pass (:3550-3591, now :3666-3704) as
   part of the condition, and it has to be: it is the only rung that fires
   for a workspace `session_sleep` window, which is the idle-sleep
   production path and the anchor test's own mechanism. The sweep therefore
   runs `resolveSessionSleepPolicyInfo` + `configWakeSuppressedInfo` +
   `wakeDemandOverridesSleepSuppression` per live row, which costs one
   capability read (unrecorded, in-memory on every provider) and — only for
   a row whose policy is actually enabled — one `GetLastActivity`, already a
   declared §2 input. The two rungs it does NOT run are
   `pendingInteractionReady` and the attachment probe: §2 moves both
   handler-side by name, §3b already marks D-SLEEP's "probe/pending arms
   unpredicted", and the handler pays both before it drains anything. The
   detector's suppression is therefore the WIDER view and the handler is the
   authority — the same direction of asymmetry WD.3 recorded for kept-open.
   The pass is evaluated for every live row rather than only for
   `ShouldWake` rows, because it is also the predicate that decides whether
   a row's no-wake verdict is re-derivable at all (delta 1).
3. **One act constant for the whole family, unlike D-ORPHAN.** WD.4 split
   `detectorActOrphan` because that family's two effect arms landed a slice
   apart. D-SLEEP's probe and drain are two rungs of ONE ladder on ONE key
   that land together here, so a second constant would gate nothing, and the
   act-frontier pin's "no two effect arms share an admission source" holds
   trivially: there is one arm and one source. The probe is not a second
   effect — it is the confirmation the idle drain waits on, and legacy runs
   both in the same pass for the same session.
4. **The probe budget stays detector-side as a SECOND cursor, and it gates
   the ENQUEUE.** §2 names "probe cursor" as bounded in-memory detector
   state and §3 keeps `maxIdleSleepProbesPerTick` detector-side, so
   `detectorIdleProbeCursor` grants slots round-robin over the sweep's
   pinned candidate order and only the winners are enqueued; the losers
   record `detector_idle_probe_budget` and wait a sweep. It is a second
   cursor rather than legacy's for the reason WD.4 gave the named suspend
   window its own counter: legacy keeps advancing its own position over its
   own candidate list on the same tick, and one shared position would
   interleave two round-robins and starve rows neither meant to skip. The
   ceiling itself is shared — both sides subtract the probes actually in
   flight (`drainTracker.activeIdleProbes`) — so the two schedules cannot
   between them exceed the fleet's per-tick probe rate. Only the patrol/boot
   call site supplies a cursor (WD.2 delta 6 again); a nil cursor grants
   nothing, which defers rather than drains.
5. **A6 becomes an explicit handler rung here too, and it is REQUIRED rather
   than a strengthening.** WD.4 added the attached/pending-interaction
   refusal to the orphan drain as a strengthening of legacy. For D-SLEEP it
   is not optional: the sweep hands `ComputeAwakeSet` an EMPTY
   `AttachedSessions`/`PendingSessions` map by design, so the detector's
   no-wake verdict is blind to exactly the two facts legacy consults before
   it suppresses a wake. The handler reuses WD.4's predicate verbatim —
   renamed `exactSessionActiveUseDeferralReason`, one rung with one spelling
   for both drain families — and it sits ABOVE the probe rung, so an
   attached session is not even probed.
6. **The seam guard has to carry the suppression check, because every rung
   above it is the shape of an ordinary running session.** D-DEADLINE's
   guard is narrow (a fired timer), D-ORPHAN's is narrow (an undesired row);
   "awake, unpinned, unheld" is the shape of every healthy session, so a
   guard that stopped at the durable rungs would claim every admission on
   every live key and divert it out of the ordinary start path. The guard
   therefore ends with the durable-intent test or the policy suppression
   test, and `configWakeSuppressedInfo` short-circuits on pure config
   (`policy.enabled()`) before any provider read — so on a city that
   configures no sleep the family costs nothing and claims nothing. A nil
   drain tracker or provider likewise fails the guard rather than the
   handler: without them there is no intent to record and no probe state to
   read, so the row is legacy's.
7. **The legacy yield sits below the #3994 keep-alive escape.**
   `withLegacySleepDrainExclusion`/`ownsSleepDrain` are siblings of the
   orphan-drain pair for the reason WD.2, WD.3 and WD.4 all recorded. The
   placement matters: legacy's escape CANCELS a drain a heartbeat hold has
   overtaken, and the keyed family never enqueues a held row, so a yield
   above the escape would disable a cancel nobody replaced. Below it, the
   yield covers everything that is actually shared and destructive — the
   idle-probe consumption (`shouldBeginIdleDrainInfo` clears the probe it
   reads, so an un-yielded legacy would retract the very confirmation the
   keyed handler is waiting on), the `idle-stop-pending` write, and the
   drain begin into the one shared in-memory tracker.
8. **A suspended-but-still-running row is out of scope, as WD.1 already left
   it.** Legacy's arm keys on runtime liveness, so it drains a
   `state=suspended` row whose runtime is still up. The sweep probes
   liveness for bead-awake rows only (`detectorBeadAwake`, WD.1), so it
   never sees such a row as alive, and the handler's guard uses the same
   predicate deliberately — detection and re-derivation answer from one
   predicate (the WD.13 delta 1 rule). Recorded rather than fixed here: the
   fix belongs with whatever slice widens the sweep's liveness set, and
   widening it inside this family would put the two sides back out of step.

### §3 D-DRAIN deltas (recorded at WD.6)

Where the family as built diverges from §3 as written. Reported, not improvised.
Line anchors are HEAD at the slice; §3's `session_start_reconcile.go:1366-1647`
for the keyed drain-ack stop had drifted to `:1180-1920` (fence at `:1204-1252`,
stop-pending block at `:1711-1916`), and §1 row 28's `session_reconciler.go:4033-4047`
for the DrainAdvance phase had drifted to `:4282-4290`.

1. **The handler-dispatch case goes SECOND, directly below D-DUP — not last.**
   §1 row 28 names the DrainAdvance phase, which runs at the very END of the
   tick (`advanceSessionDrainsWithSessionsTraced`, after the wake/sleep phase
   and after start execution), so "last" is the reading the phase anchor
   invites. It is the wrong one. Legacy's drain handling STRADDLES two
   positions, and the one that decides precedence is the earlier: the undesired
   block opens with `isDrainAcked` (`session_reconciler.go:2195`) before either
   orphan arm, and the desired path's acknowledgement block (`:2548`)
   `continue`s the row past progress-stall, drift, the deadline arms and the
   entire wake/sleep phase. The trailing advance scan is a SEPARATE loop over
   the tracker re-walking rows the forward pass already claimed, not a
   lower-precedence arm on the same row. Phase-0b duplicate retire is the only
   arm that genuinely precedes drain handling, which is what puts D-DUP above
   and everything else below. **The slot is also the only one that composes:**
   every landed family's handler already refuses a row with an active drain
   (`params.DrainTracker.get(info.ID) != nil` → `yieldOrPark` in D-ORPHAN close,
   D-DEADLINE, D-STALE-CREATE and D-STRANDED; a quiet no-change in D-SLEEP),
   and every one of those refusals was written to mean "advancing a drain is
   D-DRAIN's" (WD.4 delta 7 says so in as many words). Any lower slot would let
   those refusals swallow the key and starve the advance the moment this family
   began acting.
2. **ONE act constant for a family that straddles two legacy positions.** The
   per-EFFECT-ARM rule WD.4 introduced would suggest two constants, one per
   legacy site. It would gate halves of a single decision: the forward-pass
   block and the advance scan read the same `drainState` pointer, and the
   acknowledgement the scan WRITES is the one the block READS. `detectorActDrain`
   is one constant and `withLegacyDrainAdvanceExclusion` is one option installed
   at both sites for the same reason.
3. **`verifiedInterrupt` is dead on this path; the third effect is the
   acknowledgement WRITE.** §3 lists the handler's fenced effects as
   "`completeDrain`, `verifiedInterrupt`, or the ack metadata write". The drain
   library has not interrupted since the deferred-signal rework
   (`session_wake.go`: "The interrupt signal (Ctrl-C) is NOT sent immediately …
   no Ctrl-C keystroke injection into the pane"); its terminal arm is
   `verifiedStop`, and the deferred `setReconcilerDrainAckMetadata` write IS the
   signal. The keyed handler applies exactly one of: the stale-generation clear,
   `completeDrain`, one of the two cancel arms, the stop-pending transition, the
   deferred acknowledgement write, or the timeout `verifiedStop`.
   `verifiedInterrupt` survives only for non-drain callers and should be struck
   from §3's list at WE.
4. **The seam guard reads the in-memory TRACKER, and that is not a violation of
   the durable-row rule.** Every other family's guard answers from the durable
   row. This one cannot: Q4 (resolved on ga-f7v2ft.110, inherited here) keeps
   drain intent in `drainTracker`, so the row carries no durable trace of an
   in-flight drain until it reaches stop-pending. It is the shape D-DEADLINE
   already takes with the lifecycle timer trackers — one tracker, two readers,
   no second opinion — and the one rung that IS durable is the guard's top
   refusal (delta 5).
5. **The stop leg is a FALL-THROUGH to the existing keyed drain-ack stop, not a
   handover message.** §3 says the keyed drain-ack stop "absorbs the stop leg".
   Mechanically that is achieved by the guard REFUSING every
   `isDrainAckStopPendingInfo` row: `reconcileExactSessionDetectorFamily` runs
   above the `isDrainAckStopPendingInfo` block in
   `reconcileExactSessionStartWithOwner`, so once the handler has marked
   stop-pending (and retired the tracker entry, exactly as legacy's
   `clearDrainTrackerForStopPending` does) the next admission on that key falls
   straight through to the block that owns the atomic close and the async stop
   (A5). No second stop path, no new state, and the "exactly once" property is
   the guard, not a latch. **Consequence to carry to WE:** the transition uses
   `markDrainAckStopPending` (`sessionpkg.DrainAckStopPendingPatch`), which is
   RECONCILER-owned and carries no agent provenance, so a tracker-originated
   drain arrives at the stop-pending block without one. In auto mode that block
   yields the row to legacy, which is the correct coexistence answer; in require
   mode it parks — and that park is exactly what ruling 1b's deadline now bounds
   (delta 9). An AGENT acknowledgement (`gc runtime drain-ack`, the journey's
   shape) carries provenance and finishes keyed end to end.
6. **Two cancel arms are re-derived per key from the probe the FLEET reason was
   built from.** The fleet scan cancels on `wakeEvals` — `WakePending` and
   `eval.Reason == "assigned-work"` — which is fleet-tick state no per-key
   predicate can re-derive. Rather than trust a reason it cannot check, the
   handler re-pays the two observations those reasons are derived from:
   `pendingInteractionKeepsAwakeInfo` and
   `sessionHasAwakeAssignedWorkForReachableStore` (the same query D-SLEEP's
   handler re-pays, delta-for-delta). The plain `len(eval.Reasons) > 0` cancel
   has no per-key analogue and is deliberately NOT ported: those rows stay
   legacy's for the WD wave, and the detector still records them for the parity
   join. **Expected-divergence class for §3b's D-DRAIN row:** keyed declines a
   wake-reasons-reappeared cancel legacy applies.
7. **Non-liveness is a FRESH-liveness proof, and it fails closed the other way
   from legacy.** The fleet scan treats an unreadable running-probe as `running
   = false` and completes the drain — writing `asleep` onto a row whose agent
   may still be working. The keyed arm screens on the D2 pair and refuses with
   zero effect on `!liveness.Complete`. Strictly safer, level-triggered, and the
   same over-strict direction WD.3 delta 4 and WD.14 delta 6 both recorded.
   RED: `TestExactDrainAdvanceRefusesWhenLivenessIsIncomplete`.
8. **The legacy yield stands down BOTH halves and is source-GATED.**
   `ownsDrainAdvance` answers only on a `drain_advance` admission, unlike
   D-STALE-CREATE's and D-STRANDED's source-blind predicates. The reason is
   specific to this family: an agent may `gc runtime drain-ack` a row that
   carries no tracker intent at all, such a row is never routed under this
   source, and a source-blind yield would stand legacy's acknowledgement block
   down for it while no keyed handler claimed it. The narrow yield is what keeps
   the un-tracked agent acknowledgement legacy's for the whole WD wave.
9. **The retained drain-ack admission is bounded by the DRAIN's deadline, and
   the bound is this slice's (ruling 1b).** `session_start_controller.go`'s
   re-queue exempted `PoolDrainAck` / `PoolDrainAckUncertain` /
   `errSessionStartPoolDrainAckPending` from `maxRetries` with no other bound,
   while `ownsPoolDrainAckStop` excluded legacy from the row — so a permanently
   refusing authorization parked forever AND blocked the drain under any owner.
   The obligation now carries `DrainAckDeadline` (stamped on first retention,
   carried across coalescing so a re-admission storm cannot roll it forward,
   budget = `defaultDrainTimeout` because that IS the drain's ack-or-timeout
   contract). On expiry the controller deletes the admission, drops the retained
   lease, arms `auditPending` and reports
   `sessionStartReconcileDeadlineExceeded`; the runtime's observer emits one
   traced event at the DrainAck site and requests the legacy fallback in auto
   mode. Refusals are never classified — a throttled consecutive-refusal
   diagnostic (`drainAckRefusalDiagnosticInterval`) is the only escalation.
   This bound doubles as the bound for the ga-f7v2ft.131 old-agent fallback
   residue.
10. **The drain-finalize purity assertion is ROW-SCOPED (`:1779` ruling).** The
   journey leg asserted that no patrol/poke cycle START record appeared inside
   the finalize window — fleet silence, which a level-triggered controller
   cannot promise. It now asserts that no LEGACY-owned drain/stop/wake EFFECT
   record (site ∈ the drain/stop/wake set, outcome ∈ the applied set,
   `effect_owner` ≠ keyed/detector-shadow) names the drained row or its sibling.
   Same logic as ruling 3's sibling respec: isolation of effects, not silence of
   the fleet. **Both legs are un-skipped**
   (`routed_work_drain_finalize`, `routed_work_sibling_retirement`).
11. **JOURNEY RUN OWED.** The un-skipped legs were exercised at unit/process
   level only; the full `TestExactSessionStartNativeV59RealBDTmuxJourney` is
   HOST-blocked at slice time, and the block is upstream of every drain leg. Its
   `gc init` fixture bootstrap fails with bd v1.1.0 (the `deps.env` pin) —
   `legacy Dolt server workspace detected; explicit migration is required before
   this bd version can open or modify the workspace` — so the run never reaches
   `:1782`. Nothing on the init path is touched by this slice; the
   `live_socket_noop` sibling leg passes. Standing proof in the meantime:
   `TestExactAckedDrainReachesStopPendingOnceByKey` (stop-pending exactly once
   by key, then release), `TestReconcileExactDrainAckRequiresAtomicCloseBeforeStop`
   (auto + require, atomic close before stop),
   `TestExactDrainAdvanceCompletesWhenTheProcessExited` (finalize to drained),
   `TestRoutedWorkPoolAllocationCanonicalSingletonRetiresByExactDrainAck`
   (process-level retire by exact drain-ack) and
   `TestSiblingPoolIsolationMetadataDiff` (the respecced sibling comparison). The
   journey re-run on a host with a migrated bd workspace is owed before WE
   sign-off.

   **DISCHARGED, and it found a defect (council R1, rec/r7).** The host block is
   gone (rec/wd145's bd-workspace guard plus the pinned bd), the journey reaches
   `routed_work_drain_finalize`, and the leg FAILED — deterministically where
   reached, byte-identical signature across runs. The purity assertion at
   `:1993` was CORRECT and stays byte-identical; the defect was the keyed
   drain-ack fence, which derived its authorization from RUNTIME-resident state
   and so could not hold the drain of a legacy-created or already-stopped
   member — the member shape the whole fleet has at cutover. Fixed durable-first
   (see ruling 12). Ruling 11's "standing proof in the meantime" list is
   therefore superseded as *sufficient* evidence: those tests all passed while
   the fleet-wide member shape had no keyed acting evidence at all, which is
   precisely the hole the journey found.
12. **The drain fence is ack stamps + row binding, not allocation lineage
   (council R1).** Two gates were removed from the keyed drain-ack path, and one
   trace obligation was added.

   *Occupancy.* `newRoutedWorkPoolDrainAckLease` and
   `authorizeRoutedWorkPoolDrainAck` required
   `poolMembershipShadow.observeOccupiedMember` to certify the row. That is
   keyed ALLOCATION lineage, and a drain acknowledgement is always about a
   member that already exists — so the gate refused every legacy-created member
   outright. It is now recorded on the lease (`MembershipOccupied`,
   `MembershipRevision`) and enforced only as a monotonicity fence for a row the
   keyed allocator actually owns; a row it never owned has no lineage to regress
   and is fenced by its stamps and its binding instead. `MembershipRevision` is
   no longer required by `validateRoutedWorkPoolDrainAckLease`.

   *Runtime re-derivation.* The acknowledgement stamps are read from the runtime
   exactly ONCE, before the stop-pending transition. That transition already
   commits them onto the row under CAS (`AgentDrainAckStopPendingPatch`, consumed
   by `hasAgentProvenance`), so every re-authorization after it is satisfied by
   the durable row: `routedWorkPoolDrainAckLease.DurableAgentProvenance` skips
   the provider-meta half. Re-deriving an acknowledgement from the runtime after
   the commit asks a process that has just announced it is finished to keep
   answering for the drain that is about to stop it. **No fence is lost:** the
   keyed stop runs with `strictTokenFence`, which delegates the token check and
   the kill together to `workerStopUnattendedSessionByIDWithConfig` — the
   instance-token proof lives at the destructive boundary, atomically, with no
   TOCTOU gap for the dropped pre-stop read to have covered.

   *Unchanged.* Admission stays SOURCE-gated: an acknowledgement whose
   `GC_DRAIN_ACK` source is not the agent is still never admitted (delta 8's
   stranding argument stands). The auto-mode legacy fallback survives for
   genuinely unprovable acks — an older agent CLI, or a reconciler-authored
   marker — and only for those.

   *Traced.* Every handback now emits a `TraceSiteReconcilerDrainAck` decision
   with `effect_owner=keyed`, `effect_applied=false`, `handed_back=true`,
   outcome `rejected`, and a typed `drainAckRefusal` reason —
   `not_agent_stamped`, `member_not_occupied`, `runtime_gone`, `lease_invalid`,
   `unavailable`. A stderr-only yield is a trace lie by delta 8's own standard,
   and the controller log for the failing runs carried no drain-ack diagnostics
   at all for the drained row. The outcome is outside `legacyDrainEffectOutcomes`
   and the owner is keyed, so a handback can never be mistaken by the row-scoped
   purity scan for the effect it is refusing to apply. §3b classifies the
   `not_agent_stamped` fallback from this record rather than by inference.
13. **The drain-ack lifecycle gate asks the PROJECTION, and `lease_invalid`
   names its arm (ga-f7v2ft.147, frontier campaign).** Ruling 12 left the fence
   correct in substance and still unable to hold a real drain, because
   `isRoutedWorkPoolDrainAckLifecycleShape` compared the raw state metadata
   against one literal spelling — `active`. Nothing keeps a live member on that
   spelling: the status heal rewrites an active row to `awake` a tick after it
   reaches the runtime (`session_status_alias_heal.go:52`), and
   `projectBaseState` maps BOTH to `BaseStateActive`. From the heal onward the
   keyed family refused every one of its own members, which is why
   `keyed_drain_ack_owner` — the traced skip the .147 stand-down emits when the
   family owns a row — fired ZERO times in eight journey runs, and why legacy
   applied the stop-pending effect the `:1993` purity assertion caught. The gate
   now asks `ProjectLifecycle(...).BaseState == BaseStateActive`, which widens it
   by exactly the second spelling of a running row, keeps every dormant and
   terminal state out, and inherits the closed-status guard.

   *Why it took two campaigns.* `lease_invalid` covered a dozen independent
   preconditions, so a handback reading `refusal=lease_invalid` said only that
   ONE of them failed. Each arm now carries a sub-code with `lease_invalid` as
   its prefix (`lease_invalid/lifecycle_shape`, `/row_binding`,
   `/work_not_closed`, `/assigned_work`, `/policy_unsupported`, …) and the
   identity conjunction is split into one rung per fact. The instrumented run
   read `refusal=lease_invalid/lifecycle_shape` directly off the yield line.
   **Rule for this family: a refusal code whose cardinality is smaller than the
   set of arms that return it is a diagnostic dead end.**

   *Fixture lesson.* Every unit fixture stamped `state: active` by hand and no
   test ever ran the status heal over one, so the whole suite agreed with the
   defect. The `:1804`/`:1986` allowlist precedents are the same shape:
   stale fixture versus live writer.

### The legacy dead-runtime corpse pass (recorded at the .147/.156 frontier)

Not a detector family — `cleanupDeadRuntimeSessionCorpses` is a legacy
god-function pass — but it terminates rows the keyed families own, so its two
holes are recorded here.

1. **A corpse falsifies only a row that CLAIMS a live runtime.** The pass reaps
   a session whose panes are all dead and then closes the bead, so the alias is
   released for a successor (gastownhall/gascity#2437). A dormant row asserts no
   incarnation: `gc session kill` stops the runtime and syncs the bead to asleep
   precisely so a later wake can start a fresh one on the same durable session,
   and the corpse is that sleep's expected residue. The close is now scoped by
   `sessionBeadClaimsLiveRuntime` (the same lifecycle projection ruling 13 uses);
   the reap is unchanged, because freeing the name is what the wake needs.
2. **The corpse must be the row's own incarnation.** A restart rotates the
   instance token at the pre-wake commit and starts the new runtime after it, so
   a row mid-rebind reads `awake` — it claims a live runtime, and hole 1's guard
   passes it — while the name still carries the previous incarnation's corpse.
   The pass reaped it and closed the row out from under the start in flight,
   which came back to `ignoring stale async start result`. The pass now proves
   `GC_INSTANCE_TOKEN` against the row's token before touching anything, which
   is where the keyed stop already proves it (ruling 12, `strictTokenFence`). An
   unreadable token refuses; a row carrying no token keeps the old behavior.

Both holes present as the same journey symptom — the exact-wake leg failing at
`:1395` with `Closed:true MetadataState:dead-runtime` — and neither is the
`gc_swept` sweep shape ga-ij8mh owns.

### §3 D-STALE-CREATE deltas (recorded at WD.7)

Where the rollback family as built diverges from §3 as written. Reported, not
improvised.

1. **The legacy yield is a NEW narrow exclusion, and it is keyed on ANY
   in-flight admission rather than on the family's admission source.**
   `withLegacyStaleCreateRollbackExclusion` /
   `sessionStartController.ownsStaleCreateRollback` mirror WD.2's deadline
   bridge, for the mirror-image of WD.2's reason: reusing
   `sessionStartLegacyExclusionPredicate` would have left exactly these rows
   unyielded, because a stranded create is usually lifecycle-terminal
   (`staleCreatingStateTimeout`) or named/pool-managed, and
   `classifyExactSessionStartOwnership` hands both of those to legacy — while
   simultaneously disabling legacy rollback on rows keyed never admitted. The
   source is not consulted because the handler seam guards on the durable row
   (seam rule 1), so any admission that reaches it on an expired-lease row runs
   the keyed rollback; a source-gated yield would reproduce the ga-f7v2ft.125
   coalescing hole on legacy's side. The predicate's other half —
   "and the row really is a rollback candidate" — is re-derived at the legacy
   call site with `pendingCreateLeaseExpiredForRollbackInfo`, which keeps
   legacy's "live runtime belongs to another session" arm (:2390), an arm the
   keyed guard does not claim, running unchanged.
2. **The handler re-pays absence per key; the sweep's absence bit stays as
   WD.1 built it.** `detectStaleCreate` still raises its rollback arm on a
   cycle whose `ListRunning` failed, unlike D-ORPHAN. That shape is left alone
   so the WD.1 shadow population the campaign joins does not move; the handler
   re-observes with `workerSessionTargetRunningWithConfig` (legacy's own leg,
   :1841) and fails CLOSED on an unreadable provider, so the enqueue is a
   scheduling hint and the row is the authority.
3. **D-STALE-CREATE inherits the D2 provider screen from its destructive
   classification.** A rollback stops no runtime, but
   `routeDetectorConditions` screens the whole destructive class on
   `detectorProviderStopCapable`, and the family is `Destructive: true` for the
   storeQueryPartial guard (pinned by WD.1's frontier test). Consequence: on a
   D2-incapable city the family records `refused_provider_incapable` and legacy
   keeps the rollback for the WD wave. Reported rather than special-cased,
   because carving one family out of the shared screen is a change to a
   surface every sibling slice is extending.
4. **The reused effect is `rollbackPendingCreate`, not
   `rollbackPendingCreateClearingClaim`.** §3 asks for a rollback that clears
   the claim; both variants clear `pending_create_claim` in the store, inside
   `closeFailedCreateBeadInTx`'s single Tx. They differ only in the metadata
   batch they mirror back for legacy's tick snapshot fold, which a keyed
   handler has no use for — so the family reuses the exact call
   `commitStartFailure` makes for its own failed starts, and no second
   rollback implementation exists.

### §3 D-DRIFT convergence deltas (recorded at WD.8)

Where the family's CONVERGENCE arms as built diverge from §3 as written.
Reported, not improvised. WD.8 flips `detectorActDriftConverge`; the ladder's
deferral rungs stay shadow behind `detectorActDriftDefer` until WD.9.

1. **The split is CONVERGE vs DEFER, and it cannot be expressed by outcome.**
   WD.4 split D-ORPHAN into two act constants whose arms the DETECTOR can
   separate — running-set membership is a sweep input, so each arm got its own
   routing outcome and its own admission source. D-DRIFT's split is invisible to
   detection: the fact that decides it is attachment, which is provider I/O and
   is excluded from this family's parity level by §3b. So the two constants ride
   ONE detected condition, ONE outcome, ONE admission source
   (`config_drift`) and ONE legacy yield, and the fork happens inside the
   handler: `detectorActDriftConverge` gates the routing and every effect,
   `detectorActDriftDefer` gates nothing yet and is asserted false by the
   act-frontier pin. The frontier test therefore gains a per-constant assertion
   beside its per-outcome table, because an outcome table cannot see this seam.
2. **Site 9 merges in at DETECTION, not just in the handler.** §3's condition
   says "`StartedConfigHash != CoreFingerprint(current)` **or a live-hash
   mismatch**", but WD.1 built only the core compare, so a live-only drift was
   never enqueued and the handler could never have re-applied one. `detectDrift`
   now raises the LIVE arm — at the legacy `LiveDrift` site, under
   `detector_live_drift` — when the core compare holds and the live one does
   not. The arms are mutually exclusive by construction (the live helper answers
   "" for a core-drifted row), exactly as legacy reaches its live clause only
   through the else of the core compare, so no row raises two drift conditions.
   This MOVES the WD.1 shadow population: D-DRIFT's per-cycle count grows by the
   live-only drifts, which is a completion of §3's stated condition rather than
   a change to it, and the campaign readout must be joined against the arm's
   site rather than the family alone.
3. **A fingerprint carries its own colon, so the joined drift key is not a
   parseable pair.** WD.1's shadow record split `sessionConfigDriftKey` on the
   first `:` to fill `stored_hash`/`current_hash`, but a fingerprint is
   `"<version>:<digest>"`, so every shadow record since WD.1 has carried
   `stored_hash:"v5"` and the whole remainder as `current_hash`. WD.8 adds
   `sessionConfigDriftHashes`/`sessionLiveDriftHashes` — the unjoined form — and
   the sweep and handler both read those. The JOINED key is unchanged and still
   built the same way, because it is persisted as legacy's config-drift deferral
   key and a re-spelling would orphan every stamp already on disk.
4. **The yield sits at the CONVERGENCE effects, and that placement is what makes
   the two-constant split safe.** `withLegacyConfigDriftConvergeExclusion` is
   installed at four sites inside legacy's drift block — the version-artifact
   rebaseline, the named lane's relaunch/restart pair, the ordinary lane's
   relaunch/drain pair, and the live-half clause — never at the top of the block.
   Legacy keeps raising, STAMPING and tracing its deferral arms above it. That is
   not stylistic: WD.8's handler applies nothing on a deferral rung, so a
   top-of-block yield would suppress the attached-deferral stamp on every tick an
   admission was in flight, and `recentlyDeferredSessionAttachedConfigDrift` is
   the guard that keeps a single transient `IsAttached` false negative from
   destroying an attached conversation (A6, a KEEP invariant). It also makes a
   keyed refusal free: legacy's bookkeeping already ran on that tick.
5. **The ownership predicate is WD.7's shape, not WD.2's.**
   `ownsConfigDriftConverge` answers "is ANY admission in flight for this key",
   because the seam guards on the durable row and the controller coalesces
   admissions while keeping the earlier source — and this is the family where
   that bites hardest, since one config edit drifts the whole fleet onto keys
   that already carry ordinary start admissions. A source-gated yield would
   reproduce the ga-f7v2ft.125 hole on legacy's side. The predicate's other half
   is re-derived at the legacy call site: the row must still be the drift
   candidate the keyed handler converges, which keeps the yield off rows keyed
   never claimed.
6. **The handler pays one template resolution per candidate admission, and that
   is the family's declared cost.** Unlike D-DEADLINE, whose condition is
   re-derivable from durable timestamps, "has this row's config moved" is only
   answerable against the RESOLVED template — the same resolution the ordinary
   start path already performs per admission, including its idempotent hook
   install. The seam guard runs the cheap durable rungs first (open, revisioned,
   named, a stamped baseline, no create in flight) so a row that cannot be
   drifted never pays it, and the guard and the handler share one resolution
   rather than answering from two derivations that could skew. Declared on the
   same footing as D-DUP's bounded sibling list (WD.13 delta 5).
7. **The keyed family owns the ALIVE lane only; the asleep-named repair stays
   legacy's.** Legacy has two drift sites: the alive block and a separate
   asleep-named repair guarded by `driftRestartedInPlace`, a TICK-LOCAL flag no
   keyed handler can see. The handler therefore proves liveness per key
   (`workerSessionTargetRunningWithConfig`, failing CLOSED on an unreadable
   provider) and refuses with zero effect on a row whose runtime is not alive,
   and no yield is installed on the asleep site. Expected-divergence class for
   §3b's D-DRIFT row: on a cycle where the sweep enqueues an asleep-named
   drifted row, the keyed population shows a `no_change` refusal against a legacy
   `repair_in_place`. WD.9 or WE absorbs the arm. The reason an OFF-TICK restart
   is nonetheless safe beside that unyielded arm is durable, not lucky:
   `ConfigDriftResetPatch` stages the pending-create claim alongside the
   preserved `session_key` and baseline, so the staged row satisfies
   `pendingResumePreservingNamedRestartInfo` and the next fleet pass's asleep
   repair skips it and starts the session instead of rotating its key. Pinned by
   `TestExactConfigDriftRestartInPlaceKeepsResumeAcrossTheNextLegacyPass`,
   because the alternative — legacy's tick-local `driftRestartedInPlace` — is
   invisible to a keyed handler and would have lost the conversation.
8. **The deferral prediction is recorded by the HANDLER, shadow-flavoured, under
   a `detector_`-prefixed reason.** WD.8's handler is the first place in the
   design where a LANDED handler records a prediction for an arm a LATER slice
   owns, so the record carries `effect_owner=detector-shadow`,
   `effect_applied=false`, and `detector_config_drift_deferred` rather than
   legacy's `TraceReasonConfigDrift` — which is on `shouldAutoArmForTrace`'s
   reason leg and would have let a shadow-flavoured record write arms. The three
   deferral outcomes join `detectorShadowOutcomes` so the non-perturbation
   invariant test covers them too.
9. **D-DRIFT inherits the shared D2 screen although only one of its five rungs
   stops anything.** `routeDetectorConditions` screens the whole destructive
   class on `detectorProviderStopCapable`, and the family is `Destructive: true`
   for the storeQueryPartial guard. Its effects are a metadata write, a
   `Relaunch`, a `RunLive`, a kill-and-reset, and a drain BEGIN — none of which
   is the token-bound unattended stop the screen exists to guarantee (that stop
   is D-DRAIN's, at the far end of the drain). D-DUP took the
   `StopCapabilityExempt` carve-out for exactly this shape, and D-DRIFT
   deliberately does NOT: unlike duplicate retire, drift convergence is not
   stranded by the screen — legacy keeps converging those cities for the whole
   WD wave, so the over-strict direction costs a traced refusal rather than an
   unconverged fleet. Consequence to expect in §3b: on a D2-incapable city the
   family records `refused_provider_incapable` every sweep and never enqueues.
10. **The named-active deferral rungs are read READ-ONLY, and legacy supplies the
   stamp.** Legacy's `shouldDeferNamedSessionConfigDrift` writes as it reads: it
   persists `config_drift_deferred_at` to START the bounded window that retires
   the `activity_unknown` and `recent_activity` rungs. WD.8 may not apply that
   write — it is WD.9's effect — so
   `namedSessionConfigDriftDeferralStillBinding` reads the stamp back through the
   same key and treats an elapsed window as no longer a deferral. This composes
   only because delta 4 keeps legacy's deferral arms unyielded: legacy stamps on
   the same tick, and the keyed rung reads what legacy wrote. When WD.9 lands the
   write, the read-only mirror becomes the handler's own.
11. **A row carrying a durable `restart_requested` is the RESTART family's, at
   both the sweep and the seam** (corrected at ga-f7v2ft.138, after WD.8 shipped
   without it). Legacy's restart-requested block (`session_reconciler.go:2806`,
   reading the marker off the snapshot at `:2819`) runs ABOVE the drift block
   (`:3050`) and `continue`s the row past it once the kill lands (`:2906`); the
   single path that falls through has already applied `RestartRequestPatch`,
   which clears `started_config_hash`, so legacy's drift compare cannot see a
   drifted row carrying the marker by either route. Detecting or claiming one is
   therefore a detector-present/legacy-absent divergence, and it cost two real
   effects: the seam claimed a public `gc session reset` — whose keyed arm lives
   BELOW the family dispatch in `reconcileExactSessionStartWithOwner` — and
   silently rebaselined or drained it instead, with no drain-tracker gate, on
   rows ga-f7v2ft.103's legacy-drain park fence exists to leave alone; and the
   sweep's `config_drift` enqueue overwrote the source a pending reset was
   admitted under (`admit()` keeps the earlier source only for `anti_entropy`
   and `in_process`), which the source-gated reset arm then declined. Yielding
   costs no convergence: the restart re-stamps all four fingerprints. The
   predicate is the marker alone, so named and pool rows keyed does not own stay
   with legacy's block above, unyielded.

### §3 D-DRIFT deferral deltas (recorded at WD.9)

Where the family's A6 (DEFERRAL) arms as built diverge from §3 as written.
Reported, not improvised. WD.9 flips `detectorActDriftDefer`, which is the last
of the family's two constants; the ladder is now keyed end to end except the
asleep-named repair (WD.8 delta 7).

1. **The deferral yield is CONJUNCTIVE with the convergence yield, and that is
   the no-lapse guarantee.** The obvious reading of "install the bridge at the
   deferral effects" is a bridge that stands legacy's deferral arm down whenever
   keyed owns the key. That is unsafe, and not subtly: legacy's ladder falls
   THROUGH a skipped deferral arm into the convergence arms below it, so a
   deferral-only yield does not stop legacy from acting on an attached row — it
   promotes it, from a deferral into a relaunch or a drift drain. So
   `legacyConfigDriftDeferKeyed` requires BOTH predicates, making "legacy skipped
   the deferral arm" imply "legacy will skip every effect below it". The
   guarantee is structural rather than a wiring convention, and the pathological
   wiring (defer-yield true, converge-yield false) is pinned by
   `TestLegacyConfigDriftDeferralNeverYieldsWithoutTheConvergenceYield`. This is
   the answer to the question the two-slice handoff raises: there is no tick on
   which an attached session is defended by neither writer.
2. **The RUNG selects the effect; the reason does not.** §3 names the deferral
   arms by their reasons ("attached, recently-attached, active, pending"), but
   two different arms report the SAME reason: `namedSessionActiveUseReasonInfo`
   answers `pending_interaction` for a named row, and the ordinary lane answers
   `pending_interaction` for a pool row — and legacy applies a window stamp to
   the first and a drain cancel to the second. The deferral therefore carries a
   typed `Rung` beside legacy's verbatim `active_reason` payload, and the effect
   switch is on the rung. Five rungs, six shapes: `pinned` and the named
   active-use reasons share `named_active` because they share the stamp.
3. **Only three of the five rungs write anything, and that asymmetry is
   legacy's.** `attached` stamps the attached window and cancels a queued
   config-drift drain; `named_active` starts the bounded window (only for
   `pinned`, `activity_unknown`, `recent_activity` — the unconditional reasons
   bind forever and legacy writes nothing for them); `pending_interaction`
   cancels a pending-cancelable drain. `attached_recently` and
   `live_assigned_work` apply nothing at all: the first is already held by the
   stamp that created it, and re-stamping would extend the false-negative guard
   indefinitely rather than letting it expire. The keyed record still carries
   `effect_applied=true` on all five — the EFFECT of a deferral arm is the
   deferral, which is applied on every rung — with `deferral_stamped` and
   `drain_canceled` reported as their own fields, exactly as legacy reports
   `drain_canceled`.
4. **The pending rung traces under `TraceReasonPending`, not
   `TraceReasonConfigDrift`.** Legacy's ordinary-lane pending arm is the one
   deferral in the block that changes the trace REASON as well as the outcome.
   The keyed record carries the same pair, because the WD.15 join keys on
   (site, reason, outcome) and a keyed record under the drift reason would read
   as a legacy deferral that never fired.
5. **Two drain-cancel helpers, not one.** The attached rung cancels only a
   `config-drift` drain (`cancelSessionConfigDriftDrainInfo`); the pending rung
   cancels the whole pending-cancelable set (`cancelSessionDrainForPendingInfo`).
   Unifying them would widen the attached rung into cancelling drains — orphan,
   no-wake — that attachment is not a reason to cancel.
6. **The named window's "has it started" read is now ONE predicate shared by
   both writers, and it retires a WD.8 divergence.**
   `configDriftDeferralWindowStart` is asked by legacy's
   `boundedNamedSessionConfigDriftDeferral` before it stamps, by the keyed
   handler before it stamps, and by `namedSessionConfigDriftDeferralStillBinding`
   before it expires. Folding the three together fixed a real skew: WD.8's mirror
   parsed the stamp with `parseRFC3339Metadata`, which rejects a zero time, where
   legacy used a bare `time.Parse`, which accepts it — so a zero-valued stamp
   made the mirror re-defer forever while legacy expired the window immediately.
   Legacy's spelling won.
7. **The deferral CLEAR stays legacy's, unyielded.**
   `clearSessionConfigDriftDeferral` runs on the NOT-drifted path, which the
   keyed handler by construction never reaches — a row whose hashes match is not
   a candidate, so the family is never admitted for it. There is no arm to yield
   and no second implementation to add; the clear is a convergent, idempotent
   metadata write that two writers cannot disagree about.
8. **The shadow-deferral record is retired rather than kept.** WD.8 delta 8
   introduced `detectorReasonConfigDriftDeferred` so a landed handler could
   predict an arm a later slice owned without its record being able to auto-arm.
   That window is closed, so the reason constant and the shadow recorder are
   deleted rather than left as a branch no build can reach. The three deferral
   outcomes STAY in `detectorShadowOutcomes`: that list is also the act-frontier
   test's enumeration of outcomes a family must not enqueue under, and D-DRIFT's
   deferral outcomes are exactly the ones that must never open a second
   admission path.
9. **The seam guard and the handler entry lost "Converge" from their names.**
   One durable-row guard (`exactSessionConfigDriftCandidate`) admits the key and
   one entry (`reconcileExactSessionConfigDrift`) runs the ladder, because the
   fact that forks the halves is attachment and the detector may not pay it. The
   names now say so. The two act constants gate their own halves INSIDE the
   entry — the version-artifact and live lanes on converge, the deferral rungs on
   defer — rather than the whole handler on one of them.
10. **The admission gate is `converge || defer`, not `converge`.** WD.8 gated
    `detectorAdmissionSourceFor`'s D-DRIFT case on the convergence constant
    alone, which was correct only while the deferral half applied nothing. Both
    halves ride ONE source (`config_drift`) and one legacy-yield family, so
    either half having landed is reason enough to enqueue the key; the handler
    is the only thing that knows which arm the row takes. The family's `Acts`
    bit was already the OR, and the seam's dispatch case now matches it.

The legacy corpus's two A6 anchors have keyed twins as of this slice:
`AttachedSessionNeverRestartedOnConfigDrift` →
`TestExactConfigDriftAttachedRowDefersInsteadOfConverging`, and
`AttachedSessionCancelsQueuedConfigDriftDrain` →
`TestExactConfigDriftAttachedRowCancelsQueuedDriftDrainByKey`. The legacy
originals stay green and stay meaningful for the whole WD wave: they cover the
rows the keyed controller does not own.

### §3 D-WAKE deltas (recorded at WD.10a)

Where the named/dependency half of D-WAKE as built diverges from §3 as written.
Reported, not improvised. Q1 is discharged by the uniform predicate contract (§6
question 1); the five ga-ij8mh amendments above are implemented rather than
amended.

1. **Certification runs at the ROUTING seam, not in detection, and it is the one
   family that cannot ride the bare `Admit(id, source)` entry.** Every other
   acting family hands the key over bare because its handler re-derives the
   condition from the row. A wake cannot: a wake IS a start, and the keyed start
   path fences a start behind a certified lease
   (`AdmitConfiguredNamedWake` / `AdmitStrictDefaultPoolWake` /
   `AdmitConfiguredDependency`), so the key has to arrive already carrying one.
   The sweep therefore gains a SECOND admission entry, `AdmitWake`, and a traced
   refusal (`refused_uncertifiable`) for any call site that cannot mint a
   certificate. Cost: detection itself stays read-only and pays no new reads, and
   the seam pays ONE authoritative row read per ROUTED wake key — the same read
   the CLI socket pays for the same decision, bounded by the wake-target set
   (awake-set rows the liveness probe found dead), not by the fleet.
2. **The family splits on REASON, not on outcome, and takes TWO act constants.**
   D-ORPHAN and D-DRIFT split by outcome and by handler. D-WAKE's arms share one
   outcome (`start_candidate`) and one handler; what differs is which certified
   lease can own the row. `detectorWakeTargetReason` classifies the arm from the
   ROW SHAPE — the only split detection can see — and
   `detectorActWakeNamedDependency` (true at WD.10a) /
   `detectorActWakePoolFill` (false until WD.10b) gate the two. Routing on the
   family-wide gate alone would have carried pool-fill keys in unnoticed the
   moment either half landed.
3. **The legacy yield is the EXISTING start exclusion, and no new helper was
   added.** `sessionStartLegacyExclusionPredicate` already stands legacy down for
   a held `ownsConfiguredNamedWakeStart` / `ownsStrictDefaultPoolWakeStart` /
   `ownsConfiguredDependencyStart` lease, and that predicate drives
   `withLegacyStartExclusion`, which leaves the durable wake cause untouched and
   keeps the fleet loop out of `prepareStartCandidateForCity` — i.e. out of
   `PreWakePatch`. The gap ga-ij8mh found was entirely PRE-lease, so the fix is
   the seam in delta 4, not a fourth exclusion.
4. **The pre-lease ownership seam (amendment 2) is answered by carving certified
   wake targets out of the yield, NOT out of `classifyExactSessionStartOwnership`
   and NOT by making detection the sole keyed entry.** The classifier stays a pure
   projection over (row, config, now) — certification needs the store, the
   provider and the controller generation — and re-classifying pool-managed rows
   as keyed would route them past the socket handler's certification arm (which
   runs only when `owner != keyed`) into the ordinary keyed start path, which has
   no dependency gate. Detection-alone was rejected too: the race is won or lost
   on the BeadUpdated admission that the wake write itself fires, which is before
   any sweep. So `exactSessionStartParams.CertifyWakeFamilyStart` is invoked at
   the moment the handler would have returned `exactSessionStartLegacyOwner`,
   reusing the read the classification already paid for; a successful
   certification re-admits the same key under the lease and the handler reports
   keyed ownership with no effect, so no fallback poke fires and the durable wake
   cause survives. RED for the losing interleave:
   `TestKeyedWakeSeamClosesPreLeaseOwnershipWindow`.
5. **Capacity landed at the WITNESS, not in the eligibility predicate.** Q1's
   contract puts eligibility at `supported()` and capacity in a separate explicit
   check "exactly where the action can change the ACTIVE count". The identity
   predicate `strictDefaultPoolWakeIdentityMatches` is pure and fleet-blind, so
   the check lives in `strictDefaultPoolWakeStartWitnessCurrent`, where the
   certified membership view is reachable — the same placement the shipped
   bounded wait-dependency resume uses. Occupancy is self-excluding: re-waking an
   existing member adds no member.
6. **The two remaining pool-predicate spellings folded onto the contract
   (council F13) as a RE-SPELLING, and the attempt to fold them as a WIDENING was
   falsified by test.** `waitDependencyBoundedPoolTarget` and the pool arm of
   `waitDependencyConfiguredTemplateEligible` were the third and fourth spellings
   of "which pool identities may resume". They are NOT the anti-pattern the two
   Q1-indicted sites were: under `supported()` the reason is `EligibleAgentCap` if
   and only if the policy carries a cap, so their `reason ==` test was the
   contract's CAPACITY clause wearing a reason's clothes, not a scope narrowing.
   Rewriting them as supported()-minus-the-singleton — which reads correct in the
   abstract — widened the WITNESS REQUIREMENT to unlimited pools and broke the
   shipped resume (`TestSessionWaitDependencyReadyStartsExactSleepingSessionThroughKeyedController/strict-default-pool-member`,
   whose fixture models the witness as owed only for a cap above 1). An unlimited
   pool is ELIGIBLE to resume but owes no membership witness, because there is no
   cap for membership to witness against — clause 2's "trivially pass when
   unlimited". Both sites now spell eligibility and capacity separately with the
   same answers as before. **Correction to the Q1 disposition's premise:** the
   unlimited arm of the shipped resume was never blocked; it simply never took the
   witness route, so `sessionWaitDependencyPoolWitnessCurrent`'s unlimited arm was
   unreachable rather than the resume being unreachable. The :521 fix is still
   right (a site must not narrow by reason); its stated consequence was one seam
   too broad.
7. **The sweep rule (amendment 4) is a narrow non-candidacy applied at EVERY
   reaper, not just the one the amendment named.** The amendment cited
   `sweepUndesiredPoolSessions`/`GCSweepSessionBeads`, the reaper the pre-WD.3
   evidence caught. The re-landed journey leg proved that post-batch-3 the row is
   reaped first by the acting D-ORPHAN close family (it ended
   `Closed=true, MetadataState=orphaned`, not `gc_swept`), and by legacy's sync and
   forward-pass arms when no keyed family holds the key. The cause is the same at
   all four: NO configured single-session agent generates desired-state demand of
   its own — pool demand is driven by assigned work, and
   `poolAllocationShadowDependencies` excludes the dependency-bearing ones — so a
   row an operator just asked to wake is, to every undesiredness test in the fleet,
   an orphan. `wakeCurrentSingletonPreservesUndesiredRow` (plus its bead mirror for
   legacy's sync pass) is therefore ONE predicate answered at all four sites, which
   is also the WD.13 delta-1 rule: detection and re-derivation answer from one
   spelling. It preserves only — it never makes a row desired, so it creates no
   demand and starts nothing; it requires a CURRENT wake cause, so a consumed or
   stale one still reaps; and it does not cover slotized members, whose freeing is
   the pool lattice's business.
8. **What WD.10a does NOT take, and why it is not silent.** §3's D-WAKE entry also
   lists preserve-configured-named as a detection-side non-enqueue, the rate-limit
   screen peek moving into the handler's failure path, and the traced refusal
   replacing legacy's untraced quarantine skip (:3702-3705). Those three ride the
   PRESERVE/accounting half of the family, not the demand-fill half this slice
   owns, and each needs legacy's corresponding arm to stand down in the same
   commit or it double-acts. They travel with WD.10b's accounting arm; WD.5's
   fleet-only no-wake rows (`detector_no_wake_fleet_only`) likewise still record
   without routing, because the rung that would re-home them is pool demand, which
   is WD.10b's. **Update at WD.10b:** the traced quarantine refusal landed there
   (arm 3 needs no stand-down — it is a non-action on both sides); the other two
   and the churn/wake-failure accounting did not, for the reason recorded in the
   WD.10b deltas below.

### §3 D-WAKE deltas (recorded at WD.10b)

Where the pool half of D-WAKE as built diverges from §3 as written. Reported, not
improvised. Q2 is discharged by the promotion recorded in §2.

1. **The FILL arm has no session key, so it does not ride any admission source.**
   Every other acting arm in every family hands a bare or certified SESSION id to
   the session-start controller. Pool-under-min fill cannot: the member does not
   exist yet, so the thing being admitted is the routed WORK item. Its exact key
   is the `(workID, poolTarget, sourceStore)` triple, its sink is the existing
   pool-allocation admission (`enqueueRoutedWorkPoolAllocation`), and
   `routeDetectorPoolFill` dispatches it on the REASON before the session-keyed
   loop runs. `detectorAdmissionSourceFor` deliberately answers "not routable" for
   it, and the act-frontier test pins that: an arm with no session must never
   claim a session-start source. Its disposition is carried on the route outcome
   (`queued_pool_allocation` / `refused_overflow`) instead, and `routedToKeyed()`
   is what keeps the shadow record's `effect_owner` honest for both shapes.
2. **The FILL arm's legacy yield is an ALLOCATION seam, not a start seam.** The
   slotized pool member's re-wake shares the start-family yield its named and
   dependency siblings use (`ownsStrictDefaultPoolWakeStart` inside
   `sessionStartLegacyExclusionPredicate`), which is why both crossed under one
   act constant. The FILL arm races a different legacy engine entirely — the pool
   BUILDER in `build_desired_state.go`, not the start family — so it needed its
   own stand-down: `keyedRoutedWorkAllocations`, consulted at
   `revalidatePlannedPoolMemberDemand` on CURRENT state, full-supersede (no
   member, no demand), with a bounded lapse so a stranded reservation costs one
   patrol rather than fencing legacy forever. Before it, ga-f7v2ft.126's
   first-creator-wins made legacy the winner in practice: the durable claim the
   keyed side stamps exists only AFTER a member is created, and legacy plans from
   a per-tick snapshot and creates immediately. The reservation moves keyed
   ownership to the moment the exact key enters the lane, which is what the :469
   keyed-materialization proof needed on both arms.
3. **Open-member counting is the planner's, not the detector's own.** The arm
   counts every open pool-managed row, including one already in
   creating/start-pending and one asleep on a wait, because that is
   `poolSessionConsumesNewDemandInfo`'s definition of spent demand. Detection and
   re-derivation answering from one spelling is the WD.13 delta-1 rule; a second
   spelling here would have the sweep asking for a member the planner has already
   committed to. The arm also accrues its own fills against the desired count as
   it walks the view, so one sweep never overfills a pool short by one.
4. **The :583/:614 family (catalogued on ga-f7v2ft.117) is resolved by
   construction on the KEYED path and stays legacy's until WE.** The signature was
   a tracked pool member asleep on its wait while its routed trigger stayed open,
   `poolDesired` still reading 1, and legacy refilling. The keyed FILL arm cannot
   reproduce it: it counts the asleep member as open, so it raises no fill. The
   residual refill is legacy's FLOOR path, which carries no work item and is
   therefore gated by neither the durable claim nor the reservation — by design
   ("floor refills carry no work item and are never gated here"). That engine dies
   at WE; until then the journey's fixture premise remains the one the .117
   catalogue named, and no keyed-side change can close it.
5. **Delta-8 arm 3 is taken; arms 1 and 2 and the churn/wake-failure accounting
   are NOT, and this says where they go.** The traced quarantine/hold refusal
   (`detector_wake_blocked`) landed here: legacy drops a wake target inside a live
   quarantine window with no record at all, which is a trace lie the parity join
   cannot read past, and refusing at detection is a non-action on both sides
   (legacy already skips; the keyed admission chain blocks again at the handler),
   so it needs no stand-down of its own. The other two arms —
   preserve-configured-named as a detection-side non-enqueue, and the rate-limit
   screen peek moving into the handler's failure path — plus the churn and
   wake-failure accounting (`checkStability` / `checkChurn`,
   session_reconcile.go:478/:714, driven from the god function's forward pass) do
   NOT ride this slice. `recordWakeFailure` is already keyed-covered on the START
   failure path through `commitStartFailure`, but the post-mortem accounting for a
   row that DIED is not, and moving it keyed without a legacy stand-down at that
   forward-pass block would DOUBLE-COUNT `wake_attempts` and quarantine rows
   early. Per the ga-ij8mh round-6 Ruling 2 each of those arms owes its own
   stand-down at its own effect boundary plus a no-lapse RED, which is a slice of
   its own size. The §1 anchors `AlwaysNamedSessionWakesAfterLiveChurnSequence`
   (:4873) and `PreservedConfiguredNamedRateLimitRunsBeforeHeal` (:6927) therefore
   stay pointed at legacy until that accounting arm lands.

### §3 D-ZOMBIE + circuit/health deltas (recorded at WD.11)

Where the zombie family and the circuit/health hydration as built diverge from
§3 as written. Reported, not improvised. Line anchors are HEAD at the slice;
§3's `:2324-2354` for legacy's zombie arm had drifted to
`session_reconciler.go:2460-2502`, and its `:1599-1659` circuit-restore phase to
`:1690-1761`.

1. **The dispatch arm goes LAST and is an `if`, not a switch case — because
   legacy's zombie arm is the one forward-pass arm that CLAIMS NOTHING.** Every
   other family's legacy counterpart `continue`s the row, so mirroring legacy's
   textual order also mirrors its ownership. Legacy's zombie block marks and
   falls straight through: drift, deadline, stall and the wake/sleep phase all
   still evaluate the same row on the same tick. A switch case returns
   `handled` and therefore claims, and no position in legacy's order reproduces
   "claims nothing" — so the arm takes the position where a claim preempts
   least, below D-STRANDED. Placing it at legacy's textual position (above
   D-DRIFT and D-DEADLINE) would starve those families of the row on every
   sweep for an ownership legacy never asserts. The `if` form is WD.4 delta 2's
   rule applied to the family that meets it hardest: this condition is PURE
   provider I/O, so a bool-returning guard would force the handler to probe a
   second time, and a second probe may disagree with the first and leave the row
   owned by neither arm. The guard therefore hands its candidacy — session name,
   template and process names — straight to the handler, which makes the one
   observation that licenses the effect (delta 3).
2. **The family stays NON-destructive, so it takes neither the D2 screen nor the
   partial-store suppression.** WD.1 seated it that way and this slice keeps it.
   Its effect is a metadata mark, not a member of §2's close/stop/drain/rollback/
   retire set; legacy gates it on neither; and the mark needs no fleet view, so a
   partial store cannot make it wrong. Screening it on D2 would strand zombies
   forever on non-tmux providers (WD.13 delta 3's argument), and the runtime is
   already dead so there is nothing to stop.
3. **The guard consults a PUBLISHED fleet liveness view instead of probing, and
   that is a cost requirement rather than an optimization.** D-ZOMBIE has no
   cheap durable trigger rung — `running ∧ !alive` has no durable shadow — while
   "awake, unmarked, with a live token" is the shape of every healthy session.
   The first build probed inside the guard after the durable rungs, and that put
   one provider call on the ordinary start path for every admitted key, which
   `TestExactSessionStatusShadowOneKeyCostDoesNotGrowWithFleet` (zero provider
   calls for one ordinary keyed start) caught immediately. The fix is WD.3's
   `DesiredSessionNames` threading applied to a second fleet-shaped fact: the
   patrol sweep already probes bead-awake rows once per tick (O(awake), a
   declared §2 input), so it publishes that view through
   `CityRuntime.publishSessionLiveness` and the guard reads it back through
   `exactSessionStartParams.SessionLiveness`. Only the patrol/boot sweep
   publishes (WD.2 delta 6 again): the control dispatcher and `gc start` sweep
   NARROWED row sets, and publishing one would overwrite the fleet view with a
   partial one and mask a real zombie for a tick. **The view is a scheduling
   filter, never authority.** It is up to one patrol old, so the handler makes
   its OWN observation first and refuses with zero effect on a row that
   recovered — a replacement incarnation must never inherit the dead one's mark
   (`TestExactZombieHandlerRefusesARecoveredRow`). An unpublished or unprobed
   view declines the family with no provider call at all, which is fail-safe and
   level-triggered. The durable rungs are unchanged and still run first: fenced
   revision, open, named, live instance token, state ACTIVE or AWAKE, and not
   already terminal-marked. Creating/start_pending rows are excluded
   deliberately — legacy's arm runs on the desired fast path, a start in flight
   has no incarnation to declare dead, and pending-create rollback owns that
   shape.
4. **Legacy's exit-classification lane is a SIBLING writer of the same cluster
   and does NOT yield.** `checkRateLimitStability`
   (session_reconcile.go:505-533, called from the desired branch below the
   zombie block) marks the same terminal-error cluster for the same dead row
   from the same peek — and it also owns rate-limit quarantine, which nothing
   in this slice replaces, so fencing it would disable a recovery path with no
   keyed successor. The two writes are content-identical because both classify
   one peek, so the row converges either way; what is uniquely the zombie arm's
   is the `SessionCrashed` event, and that is what
   `withLegacyZombieMarkExclusion` protects. The keyed handler treats a lost
   CAS fence as CONVERGENCE rather than an error: it re-reads, and a row that
   now carries the mark ends the arm with zero effect claimed.
   **Consequence for §3b:** on a keyed-owned row the health cluster may carry a
   legacy write while the crash event carries only the keyed one; triage that
   as expected rather than as a double-act.
5. **The legacy yield gates the WHOLE block, unlike the stranded and stall
   seams.** Those keep legacy's observational records above the yield so the
   join sees both populations. This arm has no observational half: every step
   below `running && !alive` is an effect (a metadata batch, a bus event, a
   telemetry sample). The yield records a `keyed_zombie_mark_owner` skip at the
   legacy site so the join still sees legacy on the tick, and nothing else runs.
   `ownsZombieMark` is source-BLIND (WD.7/WD.14's `holdsAnyAdmission` shape),
   because a zombie row is awake and desired and is routinely already held by an
   ordinary wake, drift or deadline admission when the sweep finds it.
6. **Reset persistence became LEVEL-triggered on BOTH sides, and that was
   mandatory rather than tidy.** §3 says the wake handler derives `circuitOpen`
   from the hydrated model and persists the reset before the gate. Building only
   that half would have shipped a live regression: `restoreFromMetadata` is a
   consume-once EDGE (it no-ops for an identity that already has an entry) and
   the sweep runs BEFORE the god function on every tick, so a hydrating sweep
   silently disables legacy's `else if reset { persist }` on every city for the
   whole WD wave — stranding a durable "open" string that nothing clears and
   losing auto-recovery fleet-wide. `ObserveProgressSignature`'s boolean return
   is the same shape and the same trap. Both legacy gates therefore became
   convergence questions — `sessionCircuitBreakerResetOwed` ("does the row still
   say OPEN while the model says CLOSED?") and
   `sessionCircuitBreakerProgressPersistOwed` ("has the model's signature moved
   away from the row's?") — answered from the row feed the tick already holds,
   with no extra store read and no extra steady-state write. Both are
   edge-identical on the un-swept path. The signature comparison deliberately
   excludes `last_observed`, which advances in memory every tick and would turn
   a change-triggered write into a per-tick one per named identity.
   **Ownership, stated:** the legacy circuit arms are READ-SHARED with the
   sweep, not effect-competing, and they take NO fence — the write is
   idempotent, provider-free and convergent, so neither side depends on who
   hydrated first (the D-DUP expired-timer-heal shape, WD.13 delta 6). Pinned by
   `TestLegacyCircuitRestorePersistsResetAfterSweepHydration`, whose fixture
   quarantines the row so the restore arm is the only writer that could clear
   the string.
7. **The keyed gate's cold-model branch keeps the raw string, and that is the
   fail-closed direction rather than a fallback.** `exactSessionCircuitOpen`
   answers from the model where the model knows the identity and from the
   durable string where it does not. A controller that has just restarted and
   not yet swept has no grounds to believe a persisted OPEN breaker has cooled
   down, so it refuses; the next sweep's hydration converges it and the
   admission after that starts the session. That is what "EVENTUALLY starts" in
   the slice's RED means, and it is why the RED asserts the pre-hydration
   refusal as well as the post-hydration start. Trip accounting is untouched and
   stays at the shared start-failure write.
8. **The provider-health half splits the OTHER way from the breaker: the sweep
   owns the SNAPSHOT and never touches the GATE.** Hydrating the breaker is a
   pure in-memory restore, so the sweep can own it. `providerHealthGate`'s
   accrual is not analogous — `recordRedSkip` mints an episode, counts parked
   sessions and emits the ADR-0013 escalation alert (a bus event and a stdout
   line). That is an effect, effects are handler-side, and a sweep accruing
   beside legacy on the same tick would inflate the `sessions_parked` number
   operators read. So §3's "accrue provider-health episodes" is implemented as
   the snapshot half only: the tick loads `provider-health.json` ONCE, hands it
   to the sweep, and publishes it through
   `exactSessionStartParams.ProviderHealth` so the three keyed gates
   (`session_start_reconcile.go`'s two, `session_stall_reconcile.go`'s one)
   stop re-reading the file PER KEY. A nil accessor falls back to the per-call
   read, which is what a controller-free entry point needs.
9. **The named circuit-breaker clear now travels with the recycle, closing
   WD.12 delta 9 — and it sits BELOW the authority re-read, not at legacy's
   textual position.** Legacy clears between the kill and the handoff commit;
   the keyed body has a revision fence in between, and the clear is itself a
   store write, so above the fence its own revision bump would fail the very
   authority check that licenses the handoff. Below it the ordering legacy
   depends on still holds: the breaker is clear before the restart is committed.
   It lives in the shared `commitExactSessionResetHandoff` body and is a no-op
   for .103's own arm, whose ownership lattice excludes named rows. §3b's
   D-STALL row loses its "keyed skips the named circuit-breaker clear until
   WD.11" divergence.
10. **The respawn-gate RED landed as a PAIR.** §3 asks for the integration
    coverage legacy's `continue` never had; the fleet arm's test drives the real
    reconciler over a real on-disk `provider-health.json` with a green control,
    and a second test pins the KEYED half (the start plan refuses to prepare
    while the published snapshot is red) so the gate does not quietly vanish at
    the WE cutover. `TestGate_NoRespawnWhileRed`'s unit coverage of the episode
    bookkeeping stays: it is the gate's own state machine, which neither new
    test replaces.

### §3 D-STALL deltas (recorded at WD.12)

Where the progress-stall family as built diverges from §3 as written. Reported,
not improvised.

1. **The reset machinery is reused through a PARAMETERIZED pre-stop authority,
   not copied.** §3 says D-STALL converges on .103's keyed reset machinery, and
   `commitExactOrdinaryResetHandoff` is that machinery — but its pre-stop
   authority, `exactOrdinaryResetCurrent`, is the ordinary reset family's
   OWNERSHIP lattice: it excludes named, pool-managed and dependency-bearing
   rows, which is precisely the population a progress-stall recycle targets
   (legacy's two anchors are a pool floor worker and a configured named worker).
   Reusing it verbatim would have refused every row this family owns; copying the
   body would have been the second recycle implementation §3 forbids. So the body
   became `commitExactSessionResetHandoff(..., authority func(Info) bool)` and
   `commitExactOrdinaryResetHandoff` is now a one-line wrapper passing .103's own
   predicate — zero behavior change there. D-STALL passes
   `exactSessionProgressStallResetCurrent`, which proves the narrower thing this
   family actually needs between admission and stop and the same thing legacy's
   restart block re-reads: the row is open and still owes the reset the handler
   just persisted. The D2 pair, the token-bound stop, the death confirmation, the
   revision fence and the `RestartRequestPatch` commit are shared verbatim. (The
   commit body's check-then-write is flagged for a WF-fold fence in
   ga-f7v2ft.133 item 2 and is reused here unchanged.)
2. **The marker PAIR is written before the stop, which legacy does not do — so
   pinned configured named sessions are refused at the guard.** Legacy sets
   `restart_requested` alone and lets its restart block mint
   `continuation_reset_pending` inside `RestartRequestPatch`; the keyed handler
   must persist both up front, because the pair is what makes the row a reset
   .103's machinery recognizes. End state is identical, but the intermediate
   state is not: legacy's restart block treats `continuation_reset_pending=true`
   as an *explicit controller reset* and, on that basis alone, overrides the
   pinned-named kill protection at :2766. A crash between the two keyed writes
   would therefore hand legacy a licence to kill a session its own stall arm
   protects. The guard refuses `pinnedConfiguredNamedSessionKillProtected` rows
   outright, which is net-identical to legacy (set marker → decline kill → clear
   marker) with no window at all.
3. **The seam case sits between D-ORPHAN and D-DEADLINE, and its guard is the
   WHOLE ladder, not a trigger rung.** Legacy evaluates the stall arm at :2638,
   above the max-age (:3363) and idle (:3471) arms, and a firing stall
   `continue`s the row past them while a non-firing one falls through to them. A
   candidacy-only guard would claim every quiet row the ladder then declines and
   starve D-DEADLINE of it on every sweep — the treadmill WD.13 delta 1 names,
   pointed at a sibling family instead of at itself. `exactSessionProgressStallCandidate`
   therefore runs `decideExactSessionProgressStall` in full and the handler re-runs
   it against its own fresh re-read (A1). The rungs are ordered by COST rather
   than in legacy's textual order: liveness moves BELOW the activity-gap check,
   because legacy already holds an `alive` bit for every row from its fleet pass
   while a keyed guard would pay a provider probe on every admission to answer it
   first. The rungs are ANDed either way, so the answer is identical and only
   already-stalled rows pay. That liveness rung is also what makes the recycle
   exactly-once: the incarnation the handler just killed can never re-satisfy it.
4. **`floorExempt` IS re-derived handler-side, from one bounded row list.** §3
   puts the exemption detector-side because it is fleet-shaped, and that is where
   the ENQUEUE suppression stays — but the whole point of the bounded exemption
   is that a floor worker with `claim_holder_stall_timeout > 0` IS enqueued, and
   without the bit the handler cannot reproduce legacy's `exempt || floorExempt`
   gate on the claim-less arm (:2721). Threading a second fleet view (WD.3's
   `DesiredSessionNames` pattern) would add publish plumbing for one boolean;
   instead the handler pays D-DUP's price — one bounded `ListAllForReconcile` per
   candidate admission, after the cheap durable, activity and liveness rungs have
   already held — and answers from the SAME `openPoolSessionCountForTemplate` the
   sweep calls, so the two sides cannot drift. A store failure exempts (fails
   safe): an unreadable fleet must not recycle a floor worker.
5. **The enqueue-refuse loop for a claim-less floor worker is the ga-nllza6
   question, and the answer is that nothing travels.** WD.2 delta 1 established
   that a backstop legacy owns for a ladder must move with that ladder, since
   legacy yields the key and its own counter never sees the defer. D-STALL's arms
   have no counter: legacy's stall block is a pure per-tick threshold evaluation,
   and the assigned-work consecutive-defer backstop belongs to D-DEADLINE's idle
   ladder, which this family precedes rather than replaces. What the question
   does surface is the one repeating shape here — a floor worker with a positive
   claim-holder timeout and no claim is enqueued, looked up and refused every
   sweep. That is bounded and legacy-identical: it is exactly the lookup legacy
   pays for the same row on every tick at :2696, so the keyed loop adds no work
   legacy did not already do, and it is level-triggered rather than a treadmill
   against a condition that is not this family's.
6. **The legacy ResetStalled arm does NOT yield to keyed-owned rows.**
   `recordResetStallIfDue` (:203-268) is an observational alarm, not an effect:
   no store write, no provider call, self-deduping through the drain tracker,
   self-clearing when the reset lands. There is no destructive effect to
   serialize and no second writer to disagree with, because the keyed handler
   emits no alarm of its own — the same reasoning WD.13 delta 6 applied to the
   expired-timer heal, minus even the second writer. Yielding it would blind the
   fleet to exactly the recycles the new handler owns, which is the single
   failure this alarm exists to report. The pre-commit window needs no special
   handling: `resetPendingCommittedAtInfo` requires `reset_committed_at`, which
   the handler's marker pair does not carry, so the transient state cannot trip
   the alarm early. Pinned by
   `TestLegacyResetStalledArmKeepsWatchingKeyedRecycles`.
7. **Both arms record at `TraceSiteReconcilerProgressStallExempt`** — WD.1's
   delta 2 carried forward to the handler. The recycle arm has no legacy site of
   its own, so the handler's `effect_applied:true` record fires at the exempt
   site under `detector_progress_stall`, and the refusal records under
   `detector_progress_stall_exempt` when the floor exemption is what declined it.
   Legacy's own record keeps `TraceReasonMinFloorIdleWorker`/`TraceOutcomeExempt`,
   which the detector-shadow vocabulary deliberately does not reuse, so the
   WD.15 join separates the three populations by reason and `effect_owner` on a
   shared cycle. One act constant governs both arms: they share one condition,
   one handler and one legacy yield, and differ only in which threshold answered
   — unlike D-ORPHAN, whose two arms have two handlers and two yields.
8. **The provider-health gate reads the row's own provider, not a resolved
   template.** Legacy reads `tp.ResolvedProvider.Name` off the tick's already
   resolved `TemplateParams`; rebuilding that per admission inside a guard is not
   affordable. The durable row records the provider its live incarnation actually
   started under, which is the authority for a session that is running right now,
   with the fleet's own `firstNonEmpty(agent override, city default)` spelling as
   the fallback for a row written before the mirror existed. An unresolvable
   provider leaves the gate fail-open, exactly as legacy's
   `tp.ResolvedProvider != nil` guard does.
9. **The circuit-breaker clear does not travel with the recycle, inherited from
   .103.** Legacy's restart block calls `resetSessionCircuitBreakerState` for a
   named identity before it commits the handoff; .103's reset machinery does not,
   and this slice reuses that machinery unchanged. Breaker persistence at the
   keyed lane is WD.11's (§3, circuit/health), so a named row recycled here keeps
   whatever breaker state it had until WD.11 lands — recorded as an expected
   divergence for §3b's D-STALL row rather than fixed twice.
   **CLOSED at WD.11** (delta 9): the clear now travels with the shared
   `commitExactSessionResetHandoff` body, below its authority re-read.

### §3 D-DUP deltas (recorded at WD.13)

Where the D-DUP family as built diverges from §3 as written. Reported, not
improvised.

1. **The grouping predicate is legacy's whole predicate, not just "named".**
   WD.1's delta 5 recorded that D-DUP keys on named identity rather than
   canonical identity (unstamped pool rows all resolve to the template's
   qualified name). WD.13 tightens it the rest of the way to the retire body's
   own gate — open, `isNamedSessionInfo`, `NamedSessionInfoContinuityEligible`,
   and a `findNamedSessionSpec` hit on the stored identity — because detection
   and the handler's re-derivation must answer from the SAME predicate. A
   detector that grouped more loosely would enqueue a key the handler refuses on
   every patrol: a 30-second treadmill against a condition that is real but not
   this family's.
2. **The winner rule is `namedSessionWinsCanonicalRepairInfo`, reused.** §3 spells
   the rule out as "generation → canonical name → created-at", and WD.1 shipped a
   detector-local tiebreak that had already drifted from it (created-at, then ID
   — no generation rung, no canonical-name rung). `detectorDuplicateWinner` now
   calls the retire body's own comparator with the spec's canonical session name,
   so the detector cannot schedule the retire of a row the handler keeps. The
   ordering is asserted rung by rung, from both iteration orders, so it cannot
   drift again. §1's iteration-order pinning still seats the incumbent — the
   sweep's stable sort by session name then bead ID feeds both sides — but the
   rule is a total order over distinct IDs, so pinned order and rule agree by
   construction rather than by luck.
3. **D-DUP is exempt from the D2 stop-capability routing screen, and stays
   destructive for the partial-store guard.** The screen exists to guarantee the
   token-bound unattended stop D-DEADLINE's handler performs. D-DUP's handler
   performs the retire path's own stop-before-mutate
   (`stopRuntimeBeforeSessionBeadMutationInfo`: `IsRunning` → kill → `IsRunning`),
   a self-verifying stop every provider supports. Screening it on D2 would make
   the keyed family strictly less capable than the legacy phase it replaces and
   strand duplicate named rows forever on non-tmux providers. The family spec
   therefore carries `StopCapabilityExempt` while remaining `Destructive`, and
   `routeDetectorConditions` screens on the narrower predicate.
4. **The family routes on its act constant alone.** `detectDuplicateNamed` raises
   exactly one arm — one condition per LOSER row — so unlike D-DEADLINE there is
   no non-effect arm to exclude with an outcome gate. The shadow record keeps
   WD.1's `no_change` predicted outcome (the sweep applies nothing; the honest
   `applied`/`skipped` lives on the handler's record at the same site), so the
   parity join sees no vocabulary churn across the flip.
5. **The handler pays one bounded session list per candidate admission.** Unlike
   D-DEADLINE, whose condition is re-derivable from the row plus a per-session
   provider probe, "is this row a loser" is only answerable against its siblings.
   The guard runs the cheap durable rungs first, so the list is paid only by
   admissions for configured named sessions whose identity still has a spec —
   declared on the same footing as §2's provider-health file read. Every failure
   fails closed (not a loser): a store blip must never archive a row and re-point
   its work.
6. **The two Phase-0 arms take DIFFERENT ownership semantics, deliberately.** The
   duplicate retire takes a fence — `withLegacyDuplicateRetireExclusion`, backed
   by `sessionStartController.ownsDuplicateNamedRetire(id)`, threaded into the
   retire body as a per-loser skip so the identity's other losers still retire on
   the same pass. Like the deadline seam this is not a race to lose: both writers
   derive the same duplicate set from the same durable rows on the same tick, so
   an un-yielding legacy stops the loser's runtime twice and races two re-points
   at the same work beads. The expired-timer heal takes NO fence: it is a
   convergent, idempotent, provider-free clear of an already-elapsed timestamp,
   so two writers cannot disagree and there is no destructive effect to
   serialize. It SELF-yields — once the keyed admission heal has cleared the
   timer, legacy's Phase-0a fold finds nothing to clear and performs zero writes,
   which is what the RED asserts.
7. **The heal runs at the top of the admission path and re-reads.** §3 says the
   wake handler clears expired timers "at admission, since it already re-reads
   the row". It lands immediately after the closed check — before the suspend
   arm, before the detector-family seam, before the ordinary start path — so
   every arm below decides against a row whose lapsed timers are already gone. A
   current (future-dated) timer is untouched, so the suspend arm's own
   future-`held_until` condition is unaffected. Because the clear is a write, the
   handler re-reads and carries the POST-heal revision forward; a failed re-read
   yields a zero revision, which every downstream fence reads as "refuse" rather
   than fencing against a revision that no longer exists.

### §3 D-STRANDED deltas (recorded at WD.14)

Where the D-STRANDED family as built diverges from §3 as written. Reported, not
improvised. Line anchors are HEAD at the slice; §3's `:3897-3979` for the repair
helpers had drifted to `session_reconciler.go:4051-4073` (arm) plus
`session_beads.go:1240-1273` (`repairStrandedPoolWorkerBead`).

1. **The arm has NO legacy decision trace site, and the one it fires is the
   WakeSleep PHASE constant (minting ambiguity 3, resolved).** §1's row 26
   names `TraceSiteSessionReconcileWakeSleep` for the phase, and §3 never named
   a decision site for the dead-pool arm — because legacy has none.
   `emitSessionStrandedDiagnostic` records an EVENT (`events.SessionStranded`)
   on the event bus, not a trace record, and `repairStrandedPoolWorkerBead`
   traces nothing at all; the constant's only production use is the
   phase-timing `recordPhase` at session_reconciler.go:4120. WD.1 seated
   `detectStranded` on it and WD.14's handler stays there, so `gc trace` stays
   continuous. **Consequence for WD.15:** this family has no legacy
   record-with-effect to join against. Its §3b "detection" parity is
   candidacy agreement between the detector-shadow record and the phase
   record's own cycle plus the `session.stranded` event stream — not the
   record-to-record join every other family uses. State it in the harness
   rather than counting an unmatched cell.
2. **The confirmation window is DURABLE and SHARED, so §3b's "confirmation-window
   off-by-one (duplicated counters)" divergence does not exist.** The design
   row asserts the confirmation counter is bounded in-memory detector state. It
   is not: it is the `stranded_event_emitted_at` marker on the session bead,
   compared against `strandedRepairConfirmGrace`, and BOTH the detector and
   `repairStrandedPoolWorkerBead` read that one marker. There is no second
   counter to skew, unlike D-ORPHAN's genuinely duplicated suspend-deferral
   window. The expected-divergence cell should be narrowed at WD.15 to
   marker-STAMPING skew (delta 3), not counter skew.
3. **The stranded DIAGNOSTIC stays legacy-owned this wave: it is the family's
   entry CONDITION, not its effect.** §3 and the slice AC both list "emit the
   stranded diagnostic" as a handler step. It cannot be one.
   `detectStranded` keys on the marker, the marker is stamped only by
   `emitSessionStrandedDiagnostic`, and that helper early-returns while the
   marker is set — so a keyed handler calling it on a detected row is a
   guaranteed no-op, and a zero-write sweep cannot stamp it either. The keyed
   arm therefore INHERITS the marker, and the legacy yield fences only the
   destructive half of the arm. Same shape as WD.1's delta 1 for the
   unknown-state diagnostic. **WE-ledger debt:** the emit-once stamp, its
   `snapshot.ApplyOpenInfoPatch` carrier, and `clearStrandedEventMarker`'s
   alive-tick clear must move with the god function or this family loses the
   fact it keys on.
4. **The close reason is `stranded-repair`, not the preserved `sleep_reason`.**
   §3's entry and the slice AC say "close bead preserving sleep_reason as the
   close reason". That is the SIBLING arm's behavior — legacy's
   `poolFreeable && !hasAssignedWork` clean close, which has no detector and
   stays legacy's for this wave. The repair arm has always stamped
   `strandedRepairCloseReason` on purpose, so ops can tell a repaired strand
   from a natural idle recycle in the closed record. "Reuse the existing repair
   helpers unchanged — no new repair path" wins over the prose; changing the
   constant would have been a behavior change to a proven arm. The clean-close
   arm's porting is a WE-ledger item.
5. **Detection SPLITS the arm on the confirmation window, and only the confirmed
   arm routes.** WD.1's `detectStranded` raised one `TraceOutcomeClosed`
   condition for every marker-bearing not-alive row, regardless of the window or
   the pool rungs. Acting on that shape would enqueue a key the handler refuses
   on every patrol — the 30-second treadmill D-DUP's delta 1 names. WD.14 keeps
   the whole WD.1 population in the join (every such row still records) and
   gives the unconfirmed rows `detector_stranded_confirm_deferred` /
   `TraceOutcomeDeferredConfirm`; `detectorAdmissionSourceFor` routes
   `TraceOutcomeClosed` alone.
6. **Non-liveness is a HANDLER rung, and it is the load-bearing one.** Legacy
   gates the repair on its fleet-wide `!target.alive`. The sweep cannot mirror
   that: `detectorLiveness` probes bead-awake rows only, and a stranded slot is
   durably asleep, so its liveness bits are unprobed-and-false by construction.
   Detection therefore enqueues rows whose runtime may still be up, and the
   handler's per-key `ObserveFreshLiveness` is the only thing between a running
   worker and a cleared claim. Incomplete observation → typed refusal (legacy's
   fail-closed direction); live → a `kept_open` record with zero effect. RED:
   `TestExactStrandedRepairRefusesWhenTheRuntimeIsStillUp`.
7. **The assigned-work rung is a live per-key query in the GUARD, and it fails
   closed the OTHER way from legacy.** `sessionHasOpenAssignedWorkForReachableStore`
   runs after the cheap durable rungs, so only marker-bearing, pool-freeable,
   past-window rows pay it — declared on the same footing as D-DUP's bounded
   sibling list. It is a rung rather than an afterthought because a row with no
   assigned work belongs to the sibling clean-close arm (delta 4), not here. On
   a read ERROR legacy sets `hasAssignedWork = true` and enters the repair
   branch; the keyed guard refuses the family outright. Keyed is strictly safer
   and the condition is level-triggered. Detection deliberately does NOT mirror
   this rung from `AssignedWorkBeads` the way D-ORPHAN mirrors its kept-open
   suppressor: the polarity is inverted here (work is REQUIRED, not
   disqualifying), so an unpopulated snapshot input would silently disable the
   whole family rather than fail safe. No treadmill forms without it — a
   marker-bearing row that has since lost its work is closed by the sibling
   clean-close arm on that same tick, and that arm does not yield.
8. **The legacy yield is source-BLIND, and shares WD.7's predicate shape.**
   `ownsStrandedRepair` answers on ANY in-flight admission, like
   `ownsStaleCreateRollback` and unlike `ownsDeadlineStop`. The seam guards on
   the durable row, the controller coalesces admissions on a key while keeping
   the EARLIER source, and a stranded pool member is routinely already held by a
   pool-wake admission when the sweep finds it — which is exactly how the
   acked-member re-point residual (delta 9) arrives. A source-gated yield would
   let the keyed handler repair through the coalesced admission while legacy
   raced a second release at the same work beads: the ga-f7v2ft.125 hole on
   legacy's side. The two source-blind predicates now share one
   `holdsAnyAdmission` body so they cannot drift apart.
9. **The family keeps the shared D2 screen even though its effect stops
   nothing.** The runtime is already gone by definition here, so the handler
   asserts only `FreshLivenessObserver`; `routeDetectorConditions` still screens
   it on the full stop-capable pair because it is `Destructive` and not
   `StopCapabilityExempt`. Unlike D-DUP (WD.13 delta 3) this costs no capability:
   on a D2-incapable city the family simply records
   `refused_provider_incapable` and legacy keeps the repair for the WD wave,
   because legacy only yields while an admission is in flight and none is
   raised. WD.3's delta 4 reasoning applies unchanged — the over-strict
   direction is a traced refusal, never an unproven claim clear.
10. **The round-5 AC addendum's producer needs no new detection.** The
   acked-member re-point residual — new work legacy bound to a member that then
   completed its acknowledged drain and stopped, the ga-f7v2ft.131 window
   `poolTriggerRepointSuperseded` narrowed to ack → stop-pending but declared
   irreducible past that — lands as an ORDINARY stranded slot: pool-managed,
   drained, not alive, still holding the re-pointed work. The heal is the same
   single fenced effect. What the residual DOES pin is the release shape:
   `unclaimWorkAssignedToRetiredSessionInfo` returns the bead to `open` and
   unassigned AND stamps the retired member's fallback `run_target` when the
   bead is otherwise unrouted — which is the residual's shape, because work that
   arrived through a trigger binding carries no route of its own. Without that
   stamp the reopened bead leaves the routed-ready census and ga-f7v2ft.117's
   re-detection premise breaks: the strand would merely move from a dead member
   to an unroutable bead.

10. **The handler-dispatch case goes last, BELOW D-SLEEP (recorded at the batch-3
    integration).** WD.5 and WD.14 were authored on sibling branches and each
    claimed the "last" slot, because each was the only wake/sleep-phase family on
    its own branch. Legacy decides it: both arms live in ONE per-target loop in
    that phase, and the no-wake drain (`!shouldWake && alive`,
    session_reconciler.go:4088) runs before the stranded pool-slot repair
    (`!shouldWake && !alive && poolFreeable`, :4191). The order is not observable
    today — the two seam guards are disjoint on the durable row, since D-SLEEP
    requires `detectorBeadAwake` and D-STRANDED requires
    `isPoolSessionSlotFreeableInfo` (drained, or asleep with a terminal reason) —
    which is precisely why it is pinned rather than left to merge order: legacy
    separates the two on `alive`, provider I/O the seam guard may not pay, so the
    durable-state disjointness is a property of today's guards, not of the family
    boundary.

## 3b. Campaign judgment (WE sign-off bar)

Per-family parity level and expected classifications. "Detection" = the shadow record
predicts only (key, condition); "decision" = it also predicts (reason, outcome) from
snapshot-derivable predicates; provider-probe-dependent arms are never predicted
(the probes are handler-side by design). Families scoped to detection-level parity
carry **recorded owner sign-off that require-mode journey A/B satisfies D4** for their
effect arms — that sign-off is part of the WD.15 artifact, not implied.

| Family | Parity level | Must-match | Expected divergence (classified) |
|---|---|---|---|
| start families (already keyed) | act | existing shadow-worker + comparator evidence | per existing comparators |
| D-DEADLINE | decision | deadline firing + hold/quarantine/work blockers | legacy pending-interaction deferral (probe-only signal, unpredicted) |
| D-ORPHAN | decision | close/drain/kept-open arm choice | deferred-confirm off-by-one (duplicated counters); liveness-error arm incomparable; **keyed A6 attached/pending-interaction deferral against a legacy drain** (WD.4 delta 4) |
| D-STALE-CREATE | decision | rollback vs preserved | legacy defers rollback #6+ (R6 budget retired) |
| D-DRIFT | detection | hash-mismatch firing per session, per HALF (core arm at ConfigDrift, live arm at LiveDrift — WD.8 delta 2) | entire 5-arm ladder handler-side (attached probe is provider I/O — excluded, sign-off required); **keyed `no_change` refusal against a legacy `repair_in_place` on an asleep-named row** (WD.8 delta 7); the A6 deferral rungs are keyed and legacy yields them from WD.9, so on an owned key the deferral appears once under `effect_owner=keyed` with no legacy twin (WD.9 delta 1) |
| D-SLEEP | decision | awake-set membership (incl. winner identity, R3) | probe/pending arms unpredicted |
| D-DRAIN | detection | tracker-state candidacy (drain intent / draining) | ack-timing skew (handler-side ack read vs legacy's in-tick poll); advance arms journey-proven; **keyed declines the plain wake-reasons-reappeared cancel legacy applies** (WD.6 delta 6 — the fleet `len(eval.Reasons) > 0` verdict has no per-key analogue, so those rows stay legacy's for the WD wave and the detector records them without enqueueing); keyed refuses on incomplete liveness where legacy completes the drain (WD.6 delta 7) |
| D-WAKE | decision | wake-target set | legacy quarantine skip is UNTRACED (:3702-3705) → detector-present/legacy-absent, expected |
| D-ZOMBIE | detection | running ∧ !alive candidacy | classification arm handler-side; **legacy's exit-classification lane (`checkRateLimitStability`) writes the same terminal-error cluster for the same row and does NOT yield, so on a keyed-owned row the health cluster may carry a legacy write while the crash event carries only the keyed one** (WD.11 delta 4) |
| D-STALL | decision | claim-less stall + floor exemption | claim-check-error fail-safe arm incomparable; keyed refuses a pinned configured named row where legacy sets-then-clears the marker (WD.12 delta 2); the named circuit-breaker clear now travels with the recycle, so WD.12 delta 9's divergence is CLOSED (WD.11 delta 9) |
| D-DUP | decision | winner + loser set | none expected |
| D-STRANDED | detection | dead-slot candidacy (no legacy decision record exists — WD.14 delta 1) | marker-stamping skew only; the confirmation window itself is one DURABLE marker read by both paths, so the "duplicated counters" off-by-one does not arise (WD.14 delta 2) |
| (global) | — | — | storeQueryPartial cycles: legacy records Closed-without-closing (:1987-1991, :2284-2288); detector suppresses — expected, bounded to partial-view cycles |

**Legacy-at-0 residual** (WC council advisory): rows a pre-fix writer left at revision
0 are refused by the keyed `Revision==0` guards until the first unconditional write
self-heals them, so during the window such a row shows keyed refusal against legacy
effect — fail-closed, self-clearing, and triaged as its own class rather than a
detector mismatch. The two the campaign will actually meet are the drain-ack
terminal-close fence (`exactCloseFence.revision == 0`, session_reconciler.go:632,
which returns an empty finalize result rather than closing) and the pre-start
metadata fence (`loadedRevision == 0`, session_start_reconcile.go:1612, which skips
the conditional patch); triage against those two before opening a new class.

The residual is narrower than when this advisory was written (772c2c4d5f):
`session.Store.Close` no longer refuses a zero revision (d45315d498). Fence validity
moved out of the caller and into the store — a row revision on the native stores, bd's
in-transaction status compare-and-swap on a bd-backed one — because only `bd show`
projects bd's `row_lock` as `revision`, so a bead served from a `CachingStore` primed
by `bd list` legitimately carries 0 and the old caller-side refusal would have wedged
every session close on a bd-backed city. A store that does fence on the revision
rejects the useless token with a bounded-retry precondition and never closes unfenced.
Terminal close therefore contributes no legacy-at-0 refusals to the campaign at all.

**Window**: ≥7 consecutive days on ≥1 live auto-mode city, ≥10,000 joined trace
cycles, spanning ≥1 controller restart and ≥1 config reload (arms re-verified after
each, §3). **Bar**: every must-match cell ≥99.5% matched over the window; every
mismatch triaged into a table class above; **one unclassified mismatch = WE blocker**
(triage it, extend the table with evidence, or fix the detector — then re-run the
window for that family). **Join tool**: a `parity-join` subcommand on the D4-retained
perf CLI (~150-300 LOC, deleted with it at WE); join contract = shared trace-cycle
handle + normalized session name with bead-ID cross-check + records distinguished by
`effect_owner` (legacy / detector-shadow / keyed). **Artifacts**: per-family counts,
triaged mismatch log, sign-off records → `engdocs/plans/reconciler-distillation/
evidence/`; reviewed by the Fable council before WE per DESIGN.md §4 (wave gates).

### Evidence hygiene (council F5 — binding)

**No proof-bearing journey run on a saturated host.** A run is citable only if
the load average was under ~40 **at start AND at finish**. A red on a saturated
host cannot be distinguished from contention (this is exactly how ga-lnkbg's
`:1598` reset leg was first observed — one failure in four, on a box at load
73), and a green on a saturated host proves only that the timing happened to
work out. Record both load samples with every run; a run whose load was not
recorded is not evidence.

The start-only form of this rule is NOT sufficient, and the first run under it
proved so: rec/r7 run 1 began at load 37.06 and finished at 106.19, and its log
carries the signature of a host that fell over underneath it — `slow_storage_
degraded` traces, `[mysql] packets.go:58 unexpected EOF`, `source store %q is
unavailable`, `rigStores=0`, and a tmux server that was unreachable at adoption.
Legs failed there that no keyed change touches (`configured_dependency_wake` at
`:1049`). Sample both ends.

This is not a licence to widen latency budgets. `:1598` asserts a 30s absolute
bound by design (§4 absolute-bound rule) and stays there. The rule governs which
runs may be CITED, not what the assertions demand.

**Journey-runner log rotation.** Failure logs must be preserved, not overwritten.
The fourth run in the ga-lnkbg series lost its `:1598` log to a reused log slot,
which is why that bead carries a mechanism question it should already have been
able to answer from the artifact. A run that failed keeps its log.

### Coexistence-code census (council §4 — archive for the WE deletion ledger)

Of ~31,100 production insertions, **~16% dies at WE** (honest band 15-20%,
~4.8-5.4k LOC); **~84% is permanent keyed logic.** Buckets:

| Bucket | LOC | Note |
|---|---|---|
| perf/parity CLI | 2,581 | owner directive D4 keeps it until the WE campaign |
| shadow-parity pipeline | ~450 | |
| legacy-file yield/stand-down edits | 819 | dies with `session_reconciler.go` |
| 14 `withLegacy*` bridges + wiring | ~400 | |
| rollout/latch/fallback | ~400-600 | |
| sweep lattice | ~200-350 | |

**Trap — archive this with the number.** ~894 LOC of shadow-NAMED files are LIVE
keyed code: `session_lifecycle_shadow_plan`, `session_lifecycle_shadow_start_plan`,
`api_state_session_wait_shadow`, `pool_allocation_shadow`. Rename them at WF;
**never delete by filename.**

**Re-point supersede survives WE (council R2).** `build_desired_state.go` is on
§5's "survive intact" list and `DESIGN.md` §2 marks it *shared*, not
legacy-owned; `beadReconcileTick` and `controlDispatcherTick` both call
`buildDesiredStateWithSessionBeads` every tick, and the file carries no WE-stamp
comments. So `poolTriggerRepointSuperseded` and `bindPoolSessionTriggerBead`
execute post-WE, and the ga-vumr7 duplicate-claim race is NOT debt that the
legacy deletion collects for free. Priority raised above coexistence-residue
accordingly. (DETECTOR:1439's "that engine dies at WE" is about the legacy FLOOR
REFILL create arm, not the file; the re-point arm is a metadata write in the
surviving input producer.)

### Evidence hygiene — citability is artifact-gated, not load-gated (F5, ga-f7v2ft.154)

Council F5 originally barred proof-bearing journey runs on saturated hosts
(load-73 class), reds OR greens. The architect amendment of 2026-08-11
supersedes the threshold, on the owner signal that this host's load will not
materially drop: **the load<40 gate was only ever a proxy for
saturation-induced invalidity, and the direct standard replaces it.**

A journey run is **CITABLE** iff both hold:

1. its artifact shows **zero saturation signatures**, and
2. its legs complete within their **absolute budgets**.

Ambient load is **recorded as metadata on every run and never disqualifies by
itself.** A non-citable run is VOID: it neither breaks nor extends a streak.

The enumerated signature list: `newosproc` / "failed to create new OS thread";
fork/exec `EAGAIN` / "resource temporarily unavailable"; "Cannot fork";
`slow_storage_degraded`; mysql EOF / connection reset on the managed dolt;
unavailable source store / `rigStores=0`; tmux server unreachable at adoption.

**The list is extensible, and extension is mandatory, not optional: any new
infrastructure-flavored failure in a red run must be classified (signature vs
product) before the run counts either way.** Suites adapt to the
campaign-proven serial-shard pattern — one shard at a time; the parallel
fan-out is what dies, not the branch (ga-asley's serial discharge).

**Classification rulings so far.** Applying the clause above to the first
campaign runs retired two list entries from bare matching, because on a
rig-less journey fixture they are product-normal in *every* run and matching
them bare would make every run permanently VOID — i.e. would render the rule
unrunnable:

- **`rigStores=0` — PRODUCT, does not void.** `build_desired_state.go:716`
  prints `assignedWorkBeads: 0 beads (rigStores=N)` on the else branch of "did
  we find assigned work beads" — a per-tick census line, not an error path. The
  journey city registers no rigs, so `N=0` is structural. The real
  store-failure discriminator on that same path is `assignedWorkBeads: PARTIAL
  — store query failed, drain decisions suppressed` (gated on `storePartial`),
  and that is what a scanner must match instead.
- **`tmux server unreachable: no tmux server running` — PRODUCT, does not
  void.** Emitted at city startup (adoption barrier, dead cleanup, closed-bead
  reap, pool death check) before the first session exists, when no server
  legitimately exists yet. The signature means a server that *should* be
  reachable and is not, so the cold-start form is excluded and every other
  unreachable form still counts.
- **"source store `<ref>` is unavailable" — PRODUCT DEFECT on the current
  lane, does not void.** Proven deterministic, not saturation: the city store
  entry is labelled with the bare city name while the canonical workflow store
  ref is `city:<name>`, so the compare can never match. Reproduced at load 52
  as readily as at load 82. See the regression note in §6.

The general rule the three rulings share: **a signature must be something host
pressure can cause.** If a candidate string is emitted deterministically by the
fixture's own shape, or is a product defect reproducible at low load, it is not
a saturation signature — classifying it as one voids the evidence and hides the
bug, which is the exact failure mode this clause exists to prevent.

## 4. Slice plan

House template (.102, commit 6296db9b68): description = the parity gap; design =
"admit only X / reuse Y / add no controller, queue, schema, marker, or provider
interface"; acceptance = focused RED/GREEN + at least one negative + one real
schema-v59 managed-Dolt isolated-tmux journey where lifecycle is touched + gates
(`make test-fast-parallel`, `go vet`, pre-commit; journeys assert absolute latency
budgets, not "beats the 30s debounce" — .103/.105's held-debounce AC clauses likewise
become absolute budgets when executed). During WD every handler is exercised in
require-mode test cities; production stays off/auto per D4; each slice's evidence run
detail-arms its templates per §3 (unarmed runs record nothing). Parallelization: the
shared surface is the sweep file + the routing block + the admission-source enum;
WD.3-9/13-14 parallelize across worktrees after WD.2, at one-line rebase cost.

| Slice | One behavior | Reuses | Parallel? |
|-------|--------------|--------|-----------|
| WD.0 | tickDebouncer + dead IdleDrain constant deleted; no behavior change (R4). Rides in WD (not WB) because it edits the tick loop WD.1 lands in | — | INDEP |
| WD.1 | Sweep skeleton at ALL THREE call sites (patrol/boot, controlDispatcherTick, `gc start`): detection pass beside legacy, shadow traces only (shadow reason vocabulary, §3); storeQueryPartial + unknown-state global guards | tick loading pipeline, trace arms | INDEP |
| WD.2 | Idle/age deadline stop by exact key (D-DEADLINE); creates the handler dispatch seam | .102 stop machinery, Decide* ladders | SERIAL (seam creator, session_start_reconcile.go) |
| WD.3 | Undesired dead session / failed-create closed by exact key with proven absence (D-ORPHAN close) | atomic close + absence confirm (A5) | SERIAL after WD.2 (parallelizable per §4 note) |
| WD.4 | Live orphan drained by exact key incl. named deferred-confirm (D-ORPHAN drain); **resolves Q4 first** (WD.6 inherits via SERIAL) | drain library session_wake.go | SERIAL |
| WD.5 | No-wake session drained + idle probes (D-SLEEP) | ComputeAwakeSet, probe engine | SERIAL |
| WD.6 | Drain advance/ack/cancel keyed (D-DRAIN, handler-side ack read per the recorded §3 amendment; chair-gated on that amendment landing — satisfied by this revision, all other slices proceed per go-order). **LANDED** — handler `session_drain_reconcile.go`, yield `withLegacyDrainAdvanceExclusion` at both legacy positions, dispatch case SECOND (delta 1), drain-ack admissions bounded by the drain's own deadline (delta 9) | keyed drain-ack stop :1366-1647 (drifted to :1180-1920) | SERIAL |
| WD.7 | Stale pending-create rolled back by exact key (D-STALE-CREATE) | keyed start rollback | SERIAL |
| WD.8 | Detached drift converges: rebaseline / launch-relaunch / restart-in-place (D-DRIFT 1) | relaunch helper :5948 | SERIAL |
| WD.9 | Attached/active drift defers + cancels drains (D-DRIFT 2, A6) | deferral records | SERIAL |
| WD.10a | Named + dependency wake demand filled through certified wake leases (D-WAKE part 1) incl. preserve-named + rate-limit screen; **resolves Q1 first** | Admit* leases :255-277 | SERIAL |
| WD.10b | Pool-under-min fill (D-WAKE part 2) + pool-channel overflow recovery becomes census-owed re-detection + the allocation-ownership seam; **Q2 discharged**. Churn/wake-failure accounting and delta-8 arms 1-2 deferred with their own stand-downs owed (WD.10b deltas, item 5) | ComputePoolDesiredStates, pool-allocation admission | SERIAL, after WD.10a |
| WD.11 | Zombie marked unhealthy keyed (D-ZOMBIE) + circuit/health sweep hydration + NEW respawn-gate integration RED | breaker :329-389, phSnap | sweep part INDEP of seam; handler SERIAL |
| WD.12 | Progress-stall recycle with bounded min-floor exemption (D-STALL) | .103 reset machinery | SERIAL, after .103 |
| WD.13 | Duplicate named rows retired keyed + expired-timer heal at wake admission (D-DUP) | retire logic session_beads.go:609 | SERIAL |
| WD.14 | Stranded pool slot repaired + closed keyed (D-STRANDED) | repair helpers :3897-3979 | SERIAL |
| WD.15 | Campaign execution per §3b: arming harness (verify arms at every sample boundary), parity-join tool, window + match-bar evaluation, triaged mismatch log, sign-off artifacts; `gc perf reconciler-compare` A/B recorded in engdocs (WE precondition, D4) | shadow comparator, perf CLI, arm store | SEQUENCE-FINAL — completes only after WD.2-14 (tool + arming harness may start early) |

Interleave the existing behavior beads per DESIGN §4/§5: .103 (reset — before WD.12),
.105 (kill — before WD.10a, so the campaign window covers pinned wake), .78.6 (atomic
terminal closure — inside WD.3/WD.6's atomic-close reuse), ga-adnxji (archaeology from
3a96ef4e8). Legacy-corpus triage travels with each slice:
contract tests re-pointed keyed (§1 names the anchor test per site), wiring tests
marked to die at WE — not a separate wave.

## 5. What dies at WE (unlocked by this design)

Only after the WD.15 evidence campaign is archived (D4):

- **The god function**: session_reconciler.go:1447-4057 (2,611 LOC) + wrapper chain
  :1293-1446 (R5 — same WE commit as the perf CLI, its last caller) + the 9 phase-site
  constants + `keyed_start_owner` seam arms. The file dissolves; surviving helpers
  (atomic close, finalize, reset-stall alarm :203-268, diagnostics) move to their
  handler families.
- **Call-site scaffolding**: `beadReconcileTick`'s legacy branch; the rollout lattice —
  flag + registry entry + `SessionReconcilerMode()` (~200 LOC), all 85+ mode branch
  sites, `exactSessionStartOwner` tri-state + 24 legacy-owner returns, `*Entered`
  handback flags (47 refs), `drainAckStopPendingRollback` (:997-1046), `yieldOrPark`/
  `*Failure` closures, `requestLegacySessionStartFallback` chain, ownership latch
  arbitration — city_runtime_session_start.go collapses 854 → <200 LOC
  (DESIGN ledger −1,500 prod / −2,900 test).
- **Shadow/parity + perf machinery** (D4-retained until now): shadow worker/compare/
  plan files, nudge_shadow gate + internal/nudgeshadow, perf-compare CLI + schema
  (−7,900), the four shadow trace-site constants, `daemon.session_reconciler` gets a
  deprecation warning (repo precedent: the retired session_start_reconciler flag).
- **Shrink, not vanish**: session_reconcile.go (legacy-only glue dies; predicates and
  accounting move into handlers), session_wake.go (advance loop re-driven keyed;
  effect library survives), session_circuit_breaker.go (persistence call sites move;
  model survives), idle_nudge.go (backstops re-driven from the sweep). **Survive
  intact**: idle_tracker.go (117, detector input), pool_desired_state.go (pure
  computation, detector input), build_desired_state.go (5,260 — the sweep's input
  producer; its own reduction pass is out of scope per parent §2), ComputeAwakeSet,
  adoption barrier, trace WAL.
- **Tests**: ~22k legacy corpus resolved by the §1 triage — behavior contracts
  re-pointed during WD slices, wiring deleted by compilation (Trap 2); four "beats the
  30s debounce" assertions rewritten as absolute budgets.

## 6. Open questions

1. **Q1 (entry gate of WD.10a):** Is the predicate divergence at
   session_start_reconcile.go:299 (`reason != Eligible`, rejecting `EligibleAgentCap`,
   vs `supported()` at all 5 sibling sites) deliberate? If deliberate, D-WAKE
   preserves and documents it; if drift, agent-capped pools gain strict-default wakes
   at cutover — a behavior change needing its own test.
   **RESOLVED — deliberate-in-intent, incoherent-in-contract** (architect, 2026-08-09,
   recorded verbatim on ga-f7v2ft.116). The strict wake predicate and the bounded
   wait-dependency witness were INVERTED relative to each other — one accepted only
   unlimited pools, the other only bounded ones — because each slice encoded its own
   scope into the eligibility REASON. **The uniform predicate contract, binding at
   every pool-family site:** (1) eligibility is `supported()` — `{Eligible,
   EligibleAgentCap}`; encoding scope or capacity by excluding a reason is the
   anti-pattern, because it makes eligibility site-dependent and silently
   unsatisfiable. (2) Capacity is a separate explicit check exactly where the action
   can change the ACTIVE count, trivially passing when unlimited, with the acting
   session's own occupancy excluded (resuming or re-waking an existing member adds
   no member). (3) Identity-model exclusions — the canonical singleton, `max==1` —
   are expressed through the shape predicates or an explicit `maxActiveSessions == 1`
   guard naming the singleton identity, never by reason narrowing. Landed at WD.10a
   across all four spellings (see the D-WAKE deltas, items 5 and 6); the behavior
   change Q1 predicted is pinned by
   `TestStrictDefaultPoolWakeEligibilityIsSupported` and
   `TestStrictDefaultPoolWakeWitnessExcludesOwnOccupancy`. **One correction from
   implementation:** only two of the four sites were narrowing ELIGIBILITY. The
   other two fused eligibility with the capacity clause, so folding them is a
   re-spelling with identical answers — D-WAKE delta 6 records the falsification
   (a widening fold broke the shipped resume) and what it means for the `:521`
   disposition's stated consequence.
2. **Q2 (entry gate of WD.10b, blocks WE):** The pool-allocation hint channel drops on
   overflow and recovers via legacy fallback (pool_allocation_controller.go:393-398).
   Post-WE recovery = sweep re-detection of unallocated routed work, or conversion to
   the shared workqueue? (Re-detection is the no-new-machinery answer; confirm the
   sweep's routed-work view suffices.)
   **RESOLVED — yes, with promotion** (WC council, ga-f7v2ft.117): re-detection, and
   the sweep's routed-work view is made sufficient by promoting the per-store
   `ReadyLive` read to a declared sweep input (§2). WD.10b's entry gate is satisfied.
3. **Q3 (shapes the §3b matrix):** Do any live cities set `max_session_age`,
   `progress_stall_timeout`/`claim_holder_stall_timeout`, or
   `session_circuit_breaker`? Off-by-default families with no production user can take
   test-only parity (require-mode journeys) instead of campaign residency, shortening
   the side-by-side matrix.
4. **Q4 (entry gate of WD.4; WD.6 inherits):** Drain intent stays in-memory
   (drainTracker) with the one-tick deferred-interrupt rescue window — drains reset on
   controller restart, today's semantics. RECOMMEND keep (a durable-intent redesign
   adds metadata writes and fencing for a crash window nobody has reported); WD.4
   starts only on owner sign-off or a directive for the durable variant.
   **RESOLVED — keep** (architect sign-off under the owner's standing
   simplest-correct directive, 2026-08-09, recorded on ga-f7v2ft.110 and flagged
   for veto): in-memory IS the parity-faithful choice for the D4 campaign, since
   legacy's drain intent is also in-memory and a durable redesign mid-campaign
   would be a behavior change confounding the joins. WD.4's entry gate is
   satisfied and WD.6 inherits; a durable variant becomes a normal post-WE bead
   if a real crash-window incident ever surfaces.

5. **The sweep's routed-work view labels the city store with the BARE city
   name** (open regression, found by the first artifact-gated journey campaign,
   2026-08-11). `ready_routed_work_view.go:134` builds the city store entry as
   `{ref: cr.cityName, ...}` — e.g. `gctest-1d689824` — but the canonical
   workflow store ref for the city is `"city:" + cityName`
   (`workflowStoreRefForDir`, `cmd_sling.go:1298-1310`). The detector sweep
   forwards that ref raw into the allocation contribution
   (`detectorPoolAllocationEnqueueFunc`, `city_runtime_session_start.go:932`),
   and `controllerState.routedWorkStore`
   (`pool_allocation_controller.go:975-1001`) compares it against the canonical
   form **without canonicalizing**, so the city store never resolves:
   `"city:<name>" == "<name>"` is false, the (empty) rig loop falls through, and
   every city-store routed-work allocation reports *source store `<name>` is
   unavailable*. `canonicalizeLegacyWorkflowStoreRef` does not rescue it: that
   helper canonicalizes the bare literal `"city"` and bare RIG names, but a bare
   city NAME falls through all three branches unchanged.

   The same spelling divergence reaches the drain-ack fence. The
   trigger-binding arm in `authorizeRoutedWorkPoolDrainAck` compares
   `canonicalizeLegacyWorkflowStoreRef(..., info.TriggerBeadStoreRef)` against
   `lease.SourceStore` — the ROW side is canonicalized (`city:<name>`) but the
   LEASE side is compared raw (`<name>`), so the arm mismatches and the
   acknowledgement is refused `lease_invalid`.

   Introduced by `83049b52e8` (2026-08-10, "promote the ready-demand scan to the
   sweep's routed-work view"), i.e. in-lane and one day old — this is the
   bare-vs-`city:` class of the Round-4 frontier (the ga-2oboq canonicalizer)
   resurfacing on the sweep path. It is the dominant blocker of the v59 journey:
   five consecutive citable runs failed with zero infrastructure signatures, and
   it is what falsified ga-f7v2ft.147's `runtime_gone` hypothesis.
