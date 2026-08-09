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
| 19 | WakeDecision | PORT → D-WAKE via EXISTING lease admissions (`AdmitConfiguredNamedWake`/`AdmitStrictDefaultPoolWake`/`AdmitConfiguredDependency`, session_start_controller.go:255-277); `keyed_start_owner` seam arms (:1735-1745, :3766-3775) RETIRE at WE | positive arm :3777-3795; quarantine skip :3702-3705 (untraced) | `AlwaysNamedSessionWakesAfterLiveChurnSequence` :4873 (multi-tick churn→wake) |
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
- routed-work view — **one bounded `ReadyLive` read per store per patrol**, arriving
  at WD.10b (Q2 resolved yes-with-promotion). This is not a new read: it is
  `readyDemandSnapshotFingerprint`'s existing per-patrol enumeration
  (city_runtime.go:4058) *promoted* from hash input to the sweep's DECLARED
  routed-work view, where it both invalidates the demand snapshot and enqueues exact
  `(workID, poolTarget, sourceStore)` keys into pool-allocation admission.
  Event-carried routed work is already exact-key covered (`admitReadyRoutedWorkEvent`
  → `LiveReadyByID` → keyed admission, api_state.go:801-884), so the scan's residual
  value is event-silent raw-bd writes only — exactly the re-detection WD.10b owes.
  Declared on the same footing as the provider-health file read above: bounded and
  per sweep, not zero. WD.10b then deletes `readyDemandSnapshotFingerprint` +
  `writeReadyDemandFingerprintBead` (~70 LOC, :4058-4125), the snapshot field, and
  `requestReadyRoutedWorkLegacyFallback` together (WE ledger entry); the interim
  floor lands separately as WD.16 before that slice
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
acting itself. (The pool-allocation 256-slot channel silently drops hints and today
recovers via `requestReadyRoutedWorkLegacyFallback` (pool_allocation_controller.go:
393-398, city_runtime.go:501-503) — that legacy crutch becomes census-owed
re-detection, owned by slice WD.10b with Q2 resolution as its entry gate.)

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
slice: D-DEADLINE flipped at WD.2; every remaining family flips in the WE
cutover commit. The latch cannot gate this: it is a
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
   and WE.

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
| D-ORPHAN | decision | close/drain/kept-open arm choice | deferred-confirm off-by-one (duplicated counters); liveness-error arm incomparable |
| D-STALE-CREATE | decision | rollback vs preserved | legacy defers rollback #6+ (R6 budget retired) |
| D-DRIFT | detection | hash-mismatch firing per session | entire 5-arm ladder handler-side (attached probe is provider I/O — excluded, sign-off required) |
| D-SLEEP | decision | awake-set membership (incl. winner identity, R3) | probe/pending arms unpredicted |
| D-DRAIN | detection | tracker-state candidacy (drain intent / draining) | ack-timing skew (handler-side ack read vs legacy's in-tick poll); advance arms journey-proven |
| D-WAKE | decision | wake-target set | legacy quarantine skip is UNTRACED (:3702-3705) → detector-present/legacy-absent, expected |
| D-ZOMBIE | detection | running ∧ !alive candidacy | classification arm handler-side |
| D-STALL | decision | claim-less stall + floor exemption | claim-check-error fail-safe arm incomparable |
| D-DUP | decision | winner + loser set | none expected |
| D-STRANDED | detection | dead-slot candidacy | confirmation-window off-by-one (duplicated counters) |
| (global) | — | — | storeQueryPartial cycles: legacy records Closed-without-closing (:1987-1991, :2284-2288); detector suppresses — expected, bounded to partial-view cycles |

**Legacy-at-0 residual** (WC council advisory): rows a pre-fix writer left at revision
0 are refused by the `Revision==0` guards until the first unconditional write
self-heals them, so during the window such a row shows keyed refusal against legacy
effect — fail-closed, self-clearing, and triaged as its own class rather than a
detector mismatch.

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
| WD.6 | Drain advance/ack/cancel keyed (D-DRAIN, handler-side ack read per the recorded §3 amendment; chair-gated on that amendment landing — satisfied by this revision, all other slices proceed per go-order) | keyed drain-ack stop :1366-1647 | SERIAL |
| WD.7 | Stale pending-create rolled back by exact key (D-STALE-CREATE) | keyed start rollback | SERIAL |
| WD.8 | Detached drift converges: rebaseline / launch-relaunch / restart-in-place (D-DRIFT 1) | relaunch helper :5948 | SERIAL |
| WD.9 | Attached/active drift defers + cancels drains (D-DRIFT 2, A6) | deferral records | SERIAL |
| WD.10a | Named + dependency wake demand filled through certified wake leases (D-WAKE part 1) incl. preserve-named + rate-limit screen; **resolves Q1 first** | Admit* leases :255-277 | SERIAL |
| WD.10b | Pool-under-min fill + churn/wake-failure accounting (D-WAKE part 2) + pool-channel overflow recovery becomes census-owed re-detection; **Q2 resolution is the entry gate** | ComputePoolDesiredStates, pool-allocation admission | SERIAL, after WD.10a |
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
