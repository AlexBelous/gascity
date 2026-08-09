package main

import (
	"context"
	"sort"
	"strings"
	"time"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/clock"
	"github.com/gastownhall/gascity/internal/config"
	"github.com/gastownhall/gascity/internal/fsys"
	"github.com/gastownhall/gascity/internal/runtime"
	sessionpkg "github.com/gastownhall/gascity/internal/session"
)

// The detector sweep is the read-only observation half of the reconciler
// distillation (engdocs/plans/reconciler-distillation/DETECTOR.md §2). It runs
// beside the legacy god function at all three production entry points and
// classifies every session row into condition families. It never mutates the
// store, the provider, or any domain state, and during the WD wave it never
// enqueues: every family's act constant below is false until the WE cutover
// commit flips it.

// detectorFamily names one condition family from DETECTOR.md §3. The value is
// the stable key used in trace payloads and by the WD.15 parity join.
type detectorFamily string

const (
	detectorFamilyDeadline    detectorFamily = "d-deadline"
	detectorFamilyOrphan      detectorFamily = "d-orphan"
	detectorFamilyStaleCreate detectorFamily = "d-stale-create"
	detectorFamilyDrift       detectorFamily = "d-drift"
	detectorFamilySleep       detectorFamily = "d-sleep"
	detectorFamilyDrain       detectorFamily = "d-drain"
	detectorFamilyWake        detectorFamily = "d-wake"
	detectorFamilyZombie      detectorFamily = "d-zombie"
	detectorFamilyStall       detectorFamily = "d-stall"
	detectorFamilyDup         detectorFamily = "d-dup"
	detectorFamilyStranded    detectorFamily = "d-stranded"
	// detectorFamilyUnknownState is the global unknown-state guard, not a
	// condition family: it never predicts an effect, so it is never destructive
	// and never suppressed by the partial-store guard.
	detectorFamilyUnknownState detectorFamily = "d-unknown-state"
	// The two families below are OBSERVED-ONLY patrol members added to main
	// after the WD design was written (WC merge 71d1dad702). They are
	// unbounded per-tick patrol scans that contradict A1; the sweep inventories
	// them so the campaign readout sees them, but does not run, absorb, or
	// duplicate them. Absorption-or-retirement is adjudicated at WD.10b and WE.
	detectorFamilyReadyDemandScan         detectorFamily = "d-ready-demand-scan"
	detectorFamilyExecutionCompletionScan detectorFamily = "d-execution-completion-scan"
)

// Per-family act constants. THE SHADOW/ACT RULE IS PER-FAMILY AND EXPLICIT, NOT
// the session-start ownership latch (DETECTOR.md §3, shared shadow shape): the
// latch reads Keyed in the very auto-mode campaign city the evidence run needs,
// while legacy yields only at the start-family seams — there is no legacy yield
// for idle stop, orphan close, drift, stall, dup, or stranded, so a latch-gated
// enqueue would double-act beside a non-yielding legacy.
//
// A constant flips only when that family's keyed handler AND its legacy yield
// have both landed — an acting family beside a non-yielding legacy double-acts
// by construction. D-DEADLINE crossed at WD.2 (handler:
// session_deadline_reconcile.go; yield: withLegacyDeadlineStopExclusion);
// D-STALE-CREATE at WD.7 (handler: session_stale_create_reconcile.go; yield:
// withLegacyStaleCreateRollbackExclusion). The rest flip in the WE cutover
// commit, one family at a time, once the WD.15 parity window has cleared their
// must-match bar. They are compile-time constants on purpose: this is not a
// config surface.
const (
	detectorActDeadline                = true
	detectorActOrphan                  = false
	detectorActStaleCreate             = true
	detectorActDrift                   = false
	detectorActSleep                   = false
	detectorActDrain                   = false
	detectorActWake                    = false
	detectorActZombie                  = false
	detectorActStall                   = false
	detectorActDup                     = false
	detectorActStranded                = false
	detectorActReadyDemandScan         = false
	detectorActExecutionCompletionScan = false
)

// detectorShadowEffectOwner is the effect_owner every sweep record carries. It
// is distinct from "legacy" and "keyed" so the WD.15 parity join can separate
// the three record populations on a shared trace cycle.
const detectorShadowEffectOwner = "detector-shadow"

// detectorKeyedEffectOwner is the effect_owner a routed condition carries. The
// key belongs to the keyed population from the moment it is admitted, so the
// WD.15 join must not count it against the shadow arm.
const detectorKeyedEffectOwner = "keyed"

// detectorFamilyRecordBudget bounds how many per-session records one family may
// emit in a single trace cycle; beyond it the sweep emits one summary record
// naming the suppressed count. Eleven acting families at this bound cost at most
// 11*100 + 13 records per cycle, so doubling the cycle's volume beside legacy
// stays inside sessionReconcilerTraceMaxRecordsPerCycle (4000).
const detectorFamilyRecordBudget = 100

// Detector-shadow reason vocabulary. Every code is `detector_`-prefixed and sits
// deliberately outside shouldAutoArmForTrace's reason leg
// (session_reconciler_trace_collector.go), so a shadow record can never trigger
// ensureAutoArm: no arms.json write, no detail-scope mutation, and none of the
// four auto-arm slots consumed. TestDetectorShadowVocabularyNeverAutoArms pins
// this against both the reason and the outcome leg.
const (
	detectorReasonIdleTimeout             TraceReasonCode = "detector_idle_timeout"
	detectorReasonMaxSessionAge           TraceReasonCode = "detector_max_session_age"
	detectorReasonDeadlineDeferred        TraceReasonCode = "detector_deadline_deferred"
	detectorReasonOrphanDead              TraceReasonCode = "detector_orphan_dead"
	detectorReasonOrphanLive              TraceReasonCode = "detector_orphan_live"
	detectorReasonFailedCreate            TraceReasonCode = "detector_failed_create"
	detectorReasonStalePendingCreate      TraceReasonCode = "detector_stale_pending_create"
	detectorReasonPendingCreatePreserved  TraceReasonCode = "detector_pending_create_preserved"
	detectorReasonConfigDrift             TraceReasonCode = "detector_config_drift"
	detectorReasonNoWake                  TraceReasonCode = "detector_no_wake"
	detectorReasonDrainInFlight           TraceReasonCode = "detector_drain_in_flight"
	detectorReasonWakeTarget              TraceReasonCode = "detector_wake_target"
	detectorReasonZombie                  TraceReasonCode = "detector_zombie"
	detectorReasonProgressStall           TraceReasonCode = "detector_progress_stall"
	detectorReasonProgressStallExempt     TraceReasonCode = "detector_progress_stall_exempt"
	detectorReasonDuplicateNamed          TraceReasonCode = "detector_duplicate_named"
	detectorReasonStrandedPoolSlot        TraceReasonCode = "detector_stranded_pool_slot"
	detectorReasonUnknownStateSkipped     TraceReasonCode = "detector_unknown_state_skipped"
	detectorReasonStoreQueryPartial       TraceReasonCode = "detector_store_query_partial"
	detectorReasonFamilyBudgetExceeded    TraceReasonCode = "detector_family_budget_exceeded"
	detectorReasonObservedPatrolScan      TraceReasonCode = "detector_observed_patrol_scan"
	detectorReasonSweepComplete           TraceReasonCode = "detector_sweep_complete"
	detectorReasonRunningSetUnavailable   TraceReasonCode = "detector_running_set_unavailable"
	detectorReasonProviderLivenessUnknown TraceReasonCode = "detector_provider_liveness_unknown"
)

// detectorShadowReasons is the closed reason vocabulary of the sweep.
var detectorShadowReasons = []TraceReasonCode{
	detectorReasonIdleTimeout,
	detectorReasonMaxSessionAge,
	detectorReasonDeadlineDeferred,
	detectorReasonOrphanDead,
	detectorReasonOrphanLive,
	detectorReasonFailedCreate,
	detectorReasonStalePendingCreate,
	detectorReasonPendingCreatePreserved,
	detectorReasonConfigDrift,
	detectorReasonNoWake,
	detectorReasonDrainInFlight,
	detectorReasonWakeTarget,
	detectorReasonZombie,
	detectorReasonProgressStall,
	detectorReasonProgressStallExempt,
	detectorReasonDuplicateNamed,
	detectorReasonStrandedPoolSlot,
	detectorReasonUnknownStateSkipped,
	detectorReasonStoreQueryPartial,
	detectorReasonFamilyBudgetExceeded,
	detectorReasonObservedPatrolScan,
	detectorReasonSweepComplete,
	detectorReasonRunningSetUnavailable,
	detectorReasonProviderLivenessUnknown,
}

// detectorShadowOutcomes is the closed predicted-outcome vocabulary. It may
// never contain TraceOutcomeFailed, TraceOutcomeProviderError, or
// TraceOutcomeDeadlineExceeded: shouldAutoArmForTrace's OUTCOME leg fires on
// those regardless of reason, which would let a shadow record write arms.
var detectorShadowOutcomes = []TraceOutcomeCode{
	TraceOutcomeStop,
	TraceOutcomeClosed,
	TraceOutcomeDrain,
	TraceOutcomeRollback,
	TraceOutcomeStartCandidate,
	TraceOutcomeSkipped,
	TraceOutcomeNoChange,
}

// detectorFamilySpec records the fixed properties of one family: whether its
// keyed effect is destructive (close, stop, drain, rollback, retire — the set
// the storeQueryPartial guard fails closed on), whether it is observed-only,
// and whether it may enqueue yet.
type detectorFamilySpec struct {
	Family       detectorFamily
	Destructive  bool
	ObservedOnly bool
	Acts         bool
}

var detectorFamilySpecs = []detectorFamilySpec{
	{Family: detectorFamilyDeadline, Destructive: true, Acts: detectorActDeadline},
	{Family: detectorFamilyOrphan, Destructive: true, Acts: detectorActOrphan},
	{Family: detectorFamilyStaleCreate, Destructive: true, Acts: detectorActStaleCreate},
	{Family: detectorFamilyDrift, Destructive: true, Acts: detectorActDrift},
	{Family: detectorFamilySleep, Destructive: true, Acts: detectorActSleep},
	{Family: detectorFamilyDrain, Destructive: true, Acts: detectorActDrain},
	{Family: detectorFamilyStall, Destructive: true, Acts: detectorActStall},
	{Family: detectorFamilyDup, Destructive: true, Acts: detectorActDup},
	{Family: detectorFamilyStranded, Destructive: true, Acts: detectorActStranded},
	{Family: detectorFamilyUnknownState, Acts: false},
	{Family: detectorFamilyWake, Acts: detectorActWake},
	{Family: detectorFamilyZombie, Acts: detectorActZombie},
	{Family: detectorFamilyReadyDemandScan, ObservedOnly: true, Acts: detectorActReadyDemandScan},
	{Family: detectorFamilyExecutionCompletionScan, ObservedOnly: true, Acts: detectorActExecutionCompletionScan},
}

func detectorFamilySpecFor(family detectorFamily) detectorFamilySpec {
	for _, spec := range detectorFamilySpecs {
		if spec.Family == family {
			return spec
		}
	}
	return detectorFamilySpec{Family: family}
}

// detectorFamilyDestructive reports whether the family's keyed effect belongs
// to the close/stop/drain/rollback/retire set suppressed on a partial store view.
func detectorFamilyDestructive(family detectorFamily) bool {
	return detectorFamilySpecFor(family).Destructive
}

// detectorFamilyActs reports whether the family may enqueue exact keys yet.
// Every family answers false for the whole WD wave.
func detectorFamilyActs(family detectorFamily) bool {
	return detectorFamilySpecFor(family).Acts
}

// detectorAnyFamilyActs reports whether any family has been flipped to act
// mode. It is the single predicate the sweep consults before it would enqueue.
func detectorAnyFamilyActs() bool {
	for _, spec := range detectorFamilySpecs {
		if spec.Acts {
			return true
		}
	}
	return false
}

// detectorCondition is one detected, un-acted condition. SessionID is the bare
// durable bead ID — the exact key a handler would be admitted under. Identity is
// the canonical session identity resolved through
// canonicalSessionIdentityWithConfigInfo, so detector keying matches
// build_desired_state (a named bead resolves to its stored
// configured_named_identity, not the template's qualified name).
type detectorCondition struct {
	Family      detectorFamily
	SessionID   string
	SessionName string
	Template    string
	Identity    string
	Site        TraceSiteCode
	Reason      TraceReasonCode
	Outcome     TraceOutcomeCode
	Fields      map[string]any

	// AdmissionSource and AdmissionOutcome are filled by routeDetectorConditions
	// for the arms of an ACTING family. They stay zero for every shadow-only
	// family, which is what keeps a shadow record's effect_owner honest.
	AdmissionSource  sessionStartAdmissionSource
	AdmissionOutcome detectorRouteOutcome
}

// detectorRouteOutcome records what the routing seam did with one condition.
type detectorRouteOutcome string

const (
	// detectorAdmissionRefusedProviderIncapable is the traced refusal for a
	// destructive family under a provider that cannot prove fresh liveness or
	// an unattended stop (the D2 capabilities). It is a refusal, not a retry:
	// the condition is re-detected every sweep and refused every sweep, so no
	// re-enqueue treadmill forms.
	detectorAdmissionRefusedProviderIncapable detectorRouteOutcome = "refused_provider_incapable"
	// detectorAdmissionRefusedError is a rejected Admit call. Detector
	// conditions are level-triggered, so the next sweep re-detects.
	detectorAdmissionRefusedError detectorRouteOutcome = "refused_error"
)

// detectorSweepResult is the sweep's whole output. During the WD wave it is
// recorded as shadow trace and otherwise discarded; nothing in it is enqueued.
type detectorSweepResult struct {
	Conditions []detectorCondition
	// SuppressedByPartialStore counts conditions that a destructive family
	// would have raised had the store view been complete. The suppression
	// happens before the condition is constructed, so these never become
	// records.
	SuppressedByPartialStore int
	// UnknownStateSkipped counts rows excluded from every detector because
	// their persisted state is unrecognized.
	UnknownStateSkipped int
	// RowsEvaluated counts rows that reached the family predicates.
	RowsEvaluated int
	// RunningSetKnown is false when the single ListRunning probe failed; every
	// absence-dependent family fails closed for the cycle.
	RunningSetKnown bool
	// FamilyOverflow counts, per family, the conditions dropped past the
	// per-family record budget. recordDetectorShadow turns each entry into one
	// summary record so a truncated family is visible rather than silent.
	FamilyOverflow map[detectorFamily]int
	Duration       time.Duration
}

// detectorSweepInput is assembled from values the tick has already loaded. The
// sweep performs no store reads of its own: the row feed, desired state,
// demand, ready-wait set, and provider-health snapshot all arrive from the
// caller's existing pipeline (DETECTOR.md §2 inputs, "zero new reads").
type detectorSweepInput struct {
	CityPath    string
	CityName    string
	Cfg         *config.City
	Provider    runtime.Provider
	Rows        []sessionpkg.ReconcileSession
	Snapshot    *sessionBeadSnapshot
	Desired     map[string]TemplateParams
	CfgNames    map[string]bool
	PoolDesired map[string]int

	NamedDemand        map[string]bool
	NamedRoutedDemand  map[string]bool
	WorkSet            map[string]bool
	ReadyWaitSet       map[string]bool
	AssignedWorkBeads  []beads.Bead
	ReadyAssignedFlags []bool

	ProviderHealth *providerHealthSnapshot
	Drains         *drainTracker
	Idle           idleTracker
	MaxAge         maxSessionAgeTracker
	Clock          clock.Clock
	StartupTimeout time.Duration

	// StoreQueryPartial marks a degraded view of the work/session stores. Every
	// destructive family fails closed for the cycle.
	StoreQueryPartial bool
	// DeferSessionCloses mirrors the boot tick's withDeferSessionClosesOnBoot:
	// legacy skips per-session orphan/failed-create closes on the boot pass, so
	// the sweep withholds the same candidacy and the cycle stays comparable.
	DeferSessionCloses bool
	// Trigger names the call site ("patrol", "boot", "control-dispatcher",
	// "start") and is carried on every record so the parity join can scope a
	// cycle to its entry point.
	Trigger string
	// Admit hands one exact durable session ID to the existing session-start
	// controller. It is nil wherever no keyed controller owns session start —
	// `gc start`, the control dispatcher, and any legacy-owned city — so those
	// entry points stay read-only no matter which family has flipped to act.
	Admit func(sessionID string, source sessionStartAdmissionSource) (sessionStartAdmissionOutcome, error)
}

// detectSessionConditions classifies every session row in the snapshot into
// condition families. It is READ ONLY: it issues one ListRunning, the existing
// two-bit liveness probe over bead-awake rows, and GetLastActivity only for rows
// whose deadline or stall timer is configured. It performs no store mutation, no
// provider mutation, no domain write, and no enqueue.
func detectSessionConditions(ctx context.Context, in detectorSweepInput) detectorSweepResult {
	started := time.Now()
	result := detectorSweepResult{}
	if in.Cfg == nil {
		result.Duration = time.Since(started)
		return result
	}
	clk := in.Clock
	if clk == nil {
		clk = clock.Real{}
	}
	now := clk.Now()

	rows := detectorOrderedRows(in.Rows)

	// One names-only ListRunning per sweep. It proves ABSENCE but not
	// liveness: a zombie is in this set. When it fails, every family that
	// depends on proven absence fails closed for the cycle.
	runningSet, runningKnown := detectorRunningSet(in.Provider)
	result.RunningSetKnown = runningKnown

	emit := newDetectorConditionSink(in.StoreQueryPartial)

	// Global guard 1: unknown-state rows are excluded from every detector.
	// The throttled diagnostic itself stays legacy-owned this wave — it stamps
	// markers and emits events, and the sweep is zero-write.
	known := make([]sessionpkg.ReconcileSession, 0, len(rows))
	for _, row := range rows {
		if ctx != nil && ctx.Err() != nil {
			result.Duration = time.Since(started)
			return result
		}
		if row.Info.Closed {
			continue
		}
		if !isKnownStateInfo(row.Info) {
			result.UnknownStateSkipped++
			emit.add(detectorCondition{
				Family:      detectorFamilyUnknownState,
				SessionID:   row.Info.ID,
				SessionName: strings.TrimSpace(row.Info.SessionNameMetadata),
				Template:    row.Info.Template,
				Site:        TraceSiteReconcilerUnknownState,
				Reason:      detectorReasonUnknownStateSkipped,
				Outcome:     TraceOutcomeSkipped,
				Fields:      map[string]any{"state": row.Info.MetadataState},
			}, false)
			continue
		}
		known = append(known, row)
	}
	result.RowsEvaluated = len(known)

	// Two-bit liveness over bead-awake rows only (O(awake)). `alive` is what
	// D-ZOMBIE, D-WAKE, and D-SLEEP key on; names-only membership alone would
	// leave zombies permanently stuck.
	liveness := detectorLiveness(in.Provider, in.Cfg, known)

	infoByID := make(map[string]sessionpkg.Info, len(known))
	for _, row := range known {
		infoByID[row.Info.ID] = row.Info
	}

	awake := detectorAwakeSet(in, known, liveness, now)

	namedIdentityRows := make(map[string][]sessionpkg.Info)

	for _, row := range known {
		if ctx != nil && ctx.Err() != nil {
			break
		}
		info := row.Info
		name := strings.TrimSpace(info.SessionNameMetadata)
		if name == "" {
			continue
		}
		template := normalizedSessionTemplateInfo(info, in.Cfg)
		if template == "" {
			template = info.Template
		}
		cfgAgent := findAgentByTemplate(in.Cfg, template)
		_, identity := canonicalSessionIdentityWithConfigInfo(in.Cfg, cfgAgent, info)
		// D-DUP keys on NAMED identity only. Pool rows that have not yet been
		// stamped with a slot all resolve to the template's qualified name, so
		// grouping every row by identity would read ordinary pool siblings as
		// duplicates of one another.
		if identity != "" && isNamedSessionInfo(info) {
			namedIdentityRows[identity] = append(namedIdentityRows[identity], info)
		}
		base := detectorCondition{
			SessionID:   info.ID,
			SessionName: name,
			Template:    template,
			Identity:    identity,
		}
		tp, desired := in.Desired[name]
		live := liveness[info.ID]

		// Family precedence mirrors legacy's forward-pass early-continues: a row
		// that raises drain, orphan, or stale-create candidacy is claimed by that
		// family and evaluated no further this cycle. Without it the sweep would
		// raise a sleep or wake condition on a row legacy had already routed to a
		// close, producing detector-present/legacy-absent mismatches that are an
		// artifact of shape rather than a real divergence.
		if detectDrain(in, emit, base, info) {
			continue
		}
		if detectOrphan(in, emit, base, info, desired, runningSet, runningKnown) {
			continue
		}
		if detectStaleCreate(in, emit, base, info, runningSet, runningKnown, clk) {
			continue
		}
		if desired {
			detectDrift(in, emit, base, info, tp)
		}
		detectZombie(emit, base, live)
		detectDeadline(in, emit, base, info, template, live, now)
		detectStall(in, emit, base, infoByID, tp, live, now)
		detectWakeOrSleep(emit, base, name, awake, live)
		detectStranded(emit, base, info, desired, live, now)
	}

	detectDuplicateNamed(emit, namedIdentityRows, in.Cfg)

	result.Conditions = emit.conditions
	result.SuppressedByPartialStore = emit.suppressed
	result.FamilyOverflow = emit.overflow
	result.Duration = time.Since(started)
	return result
}

// detectorOrderedRows pins the sweep's iteration order — stable sort by session
// name, then bead ID — so ComputeAwakeSet's last-write-wins name resolution is
// deterministic without TopoOrder or BuildDeps (DETECTOR.md R3).
func detectorOrderedRows(rows []sessionpkg.ReconcileSession) []sessionpkg.ReconcileSession {
	ordered := make([]sessionpkg.ReconcileSession, len(rows))
	copy(ordered, rows)
	sort.SliceStable(ordered, func(i, j int) bool {
		a := strings.TrimSpace(ordered[i].Info.SessionNameMetadata)
		b := strings.TrimSpace(ordered[j].Info.SessionNameMetadata)
		if a != b {
			return a < b
		}
		return ordered[i].Info.ID < ordered[j].Info.ID
	})
	return ordered
}

func detectorRunningSet(sp runtime.Provider) (map[string]bool, bool) {
	if sp == nil {
		return nil, false
	}
	names, err := sp.ListRunning("")
	if err != nil {
		return nil, false
	}
	set := make(map[string]bool, len(names))
	for _, n := range names {
		set[strings.TrimSpace(n)] = true
	}
	return set, true
}

// detectorLivenessBits is the two-bit provider observation for one row.
type detectorLivenessBits struct {
	Probed  bool
	Running bool
	Alive   bool
}

// detectorLiveness runs the existing two-bit observeRuntimeProviderLiveness
// probe over bead-awake rows only. Asleep rows are never probed: their absence
// is already the durable answer, and probing them would make the sweep
// O(fleet) in provider calls.
func detectorLiveness(sp runtime.Provider, cfg *config.City, rows []sessionpkg.ReconcileSession) map[string]detectorLivenessBits {
	out := make(map[string]detectorLivenessBits, len(rows))
	if sp == nil {
		return out
	}
	for _, row := range rows {
		info := row.Info
		name := strings.TrimSpace(info.SessionNameMetadata)
		if name == "" || !detectorBeadAwake(info) {
			continue
		}
		running, alive := observeRuntimeProviderLiveness(sp, name, detectorProcessNames(cfg, info))
		out[info.ID] = detectorLivenessBits{Probed: true, Running: running, Alive: alive}
	}
	return out
}

// detectorBeadAwake reports whether the durable row claims to be awake. It is
// the probe-selection predicate, not a liveness verdict.
func detectorBeadAwake(info sessionpkg.Info) bool {
	switch sessionpkg.State(strings.TrimSpace(info.MetadataState)) {
	case sessionpkg.StateActive, sessionpkg.StateAwake, sessionpkg.StateCreating, sessionpkg.StateStartPending:
		return true
	default:
		return false
	}
}

func detectorProcessNames(cfg *config.City, info sessionpkg.Info) []string {
	template := normalizedSessionTemplateInfo(info, cfg)
	if template == "" {
		template = info.Template
	}
	if agent := findAgentByTemplate(cfg, template); agent != nil {
		return agent.ProcessNames
	}
	return nil
}

// detectorAwakeSet reuses ComputeAwakeSet as a pure library over the pinned
// order. Attachment and pending-interaction probes are deliberately absent:
// they are provider I/O and move handler-side, so their arms are unpredicted
// (DETECTOR.md §3b, D-SLEEP "probe/pending arms unpredicted").
func detectorAwakeSet(in detectorSweepInput, rows []sessionpkg.ReconcileSession, liveness map[string]detectorLivenessBits, now time.Time) map[string]AwakeDecision {
	input := AwakeInput{
		ScaleCheckCounts:         in.PoolDesired,
		NamedSessionDemand:       cloneBoolMap(in.NamedDemand),
		NamedSessionRoutedDemand: cloneBoolMap(in.NamedRoutedDemand),
		WorkSet:                  in.WorkSet,
		ReadyWaitSet:             in.ReadyWaitSet,
		RunningSessions:          make(map[string]bool),
		AttachedSessions:         map[string]bool{},
		PendingSessions:          map[string]bool{},
		ChatIdleTimeout:          in.Cfg.ChatSessions.IdleTimeoutDuration(),
		ManualGracePeriod:        in.Cfg.ChatSessions.GracePeriodDuration(),
		Now:                      now,
	}
	infos := make([]sessionpkg.Info, 0, len(rows))
	for _, row := range rows {
		infos = append(infos, row.Info)
		if liveness[row.Info.ID].Alive {
			if name := strings.TrimSpace(row.Info.SessionNameMetadata); name != "" {
				input.RunningSessions[name] = true
			}
		}
	}
	detectorFillAwakeConfig(&input, in.Cfg, in.CityPath)
	detectorFillAwakeWork(&input, in.AssignedWorkBeads, in.ReadyAssignedFlags)
	detectorFillAwakeSessions(&input, in.Cfg, infos, now)
	return ComputeAwakeSet(input)
}

func detectorFillAwakeConfig(input *AwakeInput, cfg *config.City, cityPath string) {
	suspState, _ := loadSuspensionState(fsys.OSFS{}, cityPath)
	for i := range cfg.Agents {
		a := &cfg.Agents[i]
		agent := AwakeAgent{
			QualifiedName:     a.QualifiedName(),
			Suspended:         isAgentEffectivelySuspendedWith(cfg, cityPath, a, suspState),
			SleepAfterIdle:    parseSleepDuration(a.SleepAfterIdle),
			MinActiveSessions: a.EffectiveMinActiveSessions(),
		}
		if len(a.DependsOn) > 0 {
			agent.DependsOn = a.DependsOn
		}
		input.Agents = append(input.Agents, agent)
	}
	cityName := config.EffectiveCityName(cfg, "")
	for i := range cfg.NamedSessions {
		ns := &cfg.NamedSessions[i]
		identity := ns.QualifiedName()
		input.NamedSessions = append(input.NamedSessions, AwakeNamedSession{
			Identity:    identity,
			Template:    ns.TemplateQualifiedName(),
			Mode:        ns.Mode,
			RuntimeName: config.NamedSessionRuntimeName(cityName, cfg.Workspace, identity),
		})
	}
}

func detectorFillAwakeWork(input *AwakeInput, work []beads.Bead, readyFlags []bool) {
	for i := range work {
		wb := work[i]
		assignee := strings.TrimSpace(wb.Assignee)
		if assignee == "" || (wb.Status != "open" && wb.Status != "in_progress") {
			continue
		}
		ready := i < len(readyFlags) && readyFlags[i]
		blocked := wb.Status == "in_progress" && wb.IsBlocked != nil && *wb.IsBlocked
		input.WorkBeads = append(input.WorkBeads, AwakeWorkBead{
			ID: wb.ID, Assignee: assignee, Status: wb.Status, Ready: ready, Blocked: blocked,
		})
	}
}

func detectorFillAwakeSessions(input *AwakeInput, cfg *config.City, infos []sessionpkg.Info, now time.Time) {
	for i := range infos {
		info := infos[i]
		if info.Closed {
			continue
		}
		name := strings.TrimSpace(info.SessionNameMetadata)
		if name == "" {
			continue
		}
		lcInput := sessionpkg.LifecycleInputFromInfo(info)
		lcInput.Now = now
		lifecycle := sessionpkg.ProjectLifecycle(lcInput)
		bead := AwakeSessionBead{
			ID:                     info.ID,
			SessionName:            name,
			Template:               normalizeAgentTemplateIdentity(cfg, info.Template),
			State:                  string(lifecycle.CompatState),
			SleepReason:            info.SleepReason,
			ManualSession:          isManualSessionInfo(info),
			PendingCreate:          lifecycle.HasWakeCause(sessionpkg.WakeCausePendingCreate),
			ExplicitWake:           lifecycle.HasWakeCause(sessionpkg.WakeCauseExplicit),
			DependencyOnly:         info.DependencyOnly,
			NamedIdentity:          lifecycle.NamedIdentity,
			ConfiguredNamedSession: isNamedSessionInfo(info),
			Pinned:                 lifecycle.HasWakeCause(sessionpkg.WakeCausePinned),
			Drained:                lifecycle.BaseState == sessionpkg.BaseStateDrained,
			WaitHold:               info.WaitHold == "true",
			RestartRequested:       strings.TrimSpace(info.RestartRequested) == "true",
			ContinuationResetPending: strings.TrimSpace(info.ContinuationResetPending) == "true" &&
				strings.TrimSpace(info.ResetCommittedAt) != "",
			CurrentlyProcessingBeadID: strings.TrimSpace(info.CurrentlyProcessingBeadID),
			HeldUntil:                 lifecycle.HeldUntil,
			QuarantinedUntil:          lifecycle.QuarantinedUntil,
			CreatedAt:                 info.CreatedAt,
		}
		if t, err := time.Parse(time.RFC3339, info.DetachedAt); err == nil && !t.IsZero() {
			bead.IdleSince = t
		}
		input.SessionBeads = append(input.SessionBeads, bead)
	}
}

// detectorConditionSink collects conditions and applies the storeQueryPartial
// guard BEFORE a condition exists. That ordering is the point: legacy records
// Outcome=Closed and then declines to close on a partial view; suppressing
// ahead of the record makes that trace lie impossible by construction.
type detectorConditionSink struct {
	partial    bool
	conditions []detectorCondition
	suppressed int
	perFamily  map[detectorFamily]int
	overflow   map[detectorFamily]int
}

func newDetectorConditionSink(partial bool) *detectorConditionSink {
	return &detectorConditionSink{
		partial:   partial,
		perFamily: make(map[detectorFamily]int),
		overflow:  make(map[detectorFamily]int),
	}
}

// add records one condition. destructive names the family's effect class for
// the partial-store guard; the caller passes it explicitly so a family whose
// arms differ (D-ORPHAN's kept-open arm vs its close arm) can classify per arm.
func (s *detectorConditionSink) add(cond detectorCondition, destructive bool) {
	if destructive && s.partial {
		s.suppressed++
		return
	}
	if s.perFamily[cond.Family] >= detectorFamilyRecordBudget {
		s.overflow[cond.Family]++
		return
	}
	s.perFamily[cond.Family]++
	s.conditions = append(s.conditions, cond)
}

func detectorOverflowSummaries(overflow map[detectorFamily]int) []detectorCondition {
	families := make([]detectorFamily, 0, len(overflow))
	for family := range overflow {
		families = append(families, family)
	}
	sort.Slice(families, func(i, j int) bool { return families[i] < families[j] })
	out := make([]detectorCondition, 0, len(families))
	for _, family := range families {
		out = append(out, detectorCondition{
			Family:  family,
			Site:    TraceSiteControllerTickPhase,
			Reason:  detectorReasonFamilyBudgetExceeded,
			Outcome: TraceOutcomeSkipped,
			Fields: map[string]any{
				"detector_family":  string(family),
				"budget":           detectorFamilyRecordBudget,
				"suppressed_count": overflow[family],
			},
		})
	}
	return out
}

func detectDeadline(in detectorSweepInput, emit *detectorConditionSink, base detectorCondition, info sessionpkg.Info, template string, live detectorLivenessBits, now time.Time) {
	if !live.Alive {
		return
	}
	// The durable blocker rung of DecideIdleTimeout / DecideMaxSessionAge is the
	// only rung the sweep can evaluate: it reads held_until and quarantined_until
	// off the row it already has. The pending-interaction and assigned-work rungs
	// are a provider probe and a reachable-store scan, so they stay handler-side
	// with the rest of the ladder (§3b: "legacy pending-interaction deferral —
	// probe-only signal, unpredicted"). A blocked row records its deferral for
	// the parity join and is never enqueued, so the handler is never entered.
	blocker := lifecycleTimerBlockerInfo(info, now)
	deadline := func(site TraceSiteCode, reason TraceReasonCode, fields map[string]any) {
		cond := base
		cond.Family = detectorFamilyDeadline
		cond.Site = site
		if blocker != "" {
			cond.Reason = detectorReasonDeadlineDeferred
			cond.Outcome = TraceOutcomeNoChange
			cond.Fields = map[string]any{"predicted_effect": "none", "blocker": blocker, "deadline": string(reason)}
			emit.add(cond, false)
			return
		}
		cond.Reason = reason
		cond.Outcome = TraceOutcomeStop
		cond.Fields = fields
		emit.add(cond, true)
	}
	if in.Idle != nil && in.Idle.checkIdle(base.SessionName, template, in.Provider, now) {
		deadline(TraceSiteReconcilerIdleTimeout, detectorReasonIdleTimeout, map[string]any{
			"predicted_effect": "stop",
			"sleep_reason":     string(sessionpkg.SleepReasonIdleTimeout),
		})
	}
	if in.MaxAge == nil {
		return
	}
	completeAt, err := time.Parse(time.RFC3339, strings.TrimSpace(info.CreationCompleteAt))
	if err != nil || completeAt.IsZero() {
		return
	}
	if in.MaxAge.shouldRestart(base.SessionName, template, completeAt, now) {
		deadline(TraceSiteReconcilerMaxSessionAge, detectorReasonMaxSessionAge, map[string]any{
			"predicted_effect": "stop",
			"sleep_reason":     string(sessionpkg.SleepReasonMaxSessionAge),
			"age_seconds":      int64(now.Sub(completeAt).Seconds()),
		})
	}
}

// detectOrphan reports whether the row was claimed by the orphan family.
func detectOrphan(in detectorSweepInput, emit *detectorConditionSink, base detectorCondition, info sessionpkg.Info, desired bool, runningSet map[string]bool, runningKnown bool) bool {
	if desired || in.DeferSessionCloses {
		return false
	}
	if strings.TrimSpace(info.MetadataState) == string(sessionpkg.StateFailedCreate) {
		cond := base
		cond.Family = detectorFamilyOrphan
		cond.Site = TraceSiteReconcilerCloseFailedCreate
		cond.Reason = detectorReasonFailedCreate
		cond.Outcome = TraceOutcomeClosed
		cond.Fields = map[string]any{"predicted_effect": "close"}
		emit.add(cond, true)
		return true
	}
	// Proven absence, not assumed absence: without a running set the close arm
	// fails closed for the cycle, matching legacy's liveness-error refusal.
	if !runningKnown {
		cond := base
		cond.Family = detectorFamilyOrphan
		cond.Site = TraceSiteReconcilerOrphaned
		cond.Reason = detectorReasonRunningSetUnavailable
		cond.Outcome = TraceOutcomeSkipped
		cond.Fields = map[string]any{"predicted_effect": "none"}
		emit.add(cond, false)
		return true
	}
	cond := base
	cond.Family = detectorFamilyOrphan
	if runningSet[base.SessionName] {
		cond.Site = TraceSiteReconcilerOrphaned
		cond.Reason = detectorReasonOrphanLive
		cond.Outcome = TraceOutcomeDrain
		cond.Fields = map[string]any{"predicted_effect": "drain"}
	} else {
		cond.Site = TraceSiteReconcilerCloseOrphan
		cond.Reason = detectorReasonOrphanDead
		cond.Outcome = TraceOutcomeClosed
		cond.Fields = map[string]any{"predicted_effect": "close"}
	}
	emit.add(cond, true)
	return true
}

// detectStaleCreate reports whether the row was claimed by the stale-create
// family.
func detectStaleCreate(in detectorSweepInput, emit *detectorConditionSink, base detectorCondition, info sessionpkg.Info, runningSet map[string]bool, runningKnown bool, clk clock.Clock) bool {
	if !info.PendingCreateClaim {
		return false
	}
	if runningKnown && runningSet[base.SessionName] {
		return false
	}
	cond := base
	cond.Family = detectorFamilyStaleCreate
	if pendingCreateLeaseExpiredForRollbackInfo(info, clk, in.StartupTimeout) {
		cond.Site = TraceSiteReconcilerPendingCreate
		cond.Reason = detectorReasonStalePendingCreate
		cond.Outcome = TraceOutcomeRollback
		cond.Fields = map[string]any{"predicted_effect": "rollback"}
		emit.add(cond, true)
		return true
	}
	cond.Site = TraceSiteReconcilerPendingCreatePreserved
	cond.Reason = detectorReasonPendingCreatePreserved
	cond.Outcome = TraceOutcomeNoChange
	cond.Fields = map[string]any{"predicted_effect": "none"}
	emit.add(cond, false)
	return true
}

func detectDrift(in detectorSweepInput, emit *detectorConditionSink, base detectorCondition, info sessionpkg.Info, tp TemplateParams) {
	key := sessionConfigDriftKey(info, in.Cfg, tp)
	if key == "" {
		return
	}
	stored, current, _ := strings.Cut(key, ":")
	cond := base
	cond.Family = detectorFamilyDrift
	cond.Site = TraceSiteReconcilerConfigDrift
	cond.Reason = detectorReasonConfigDrift
	cond.Outcome = TraceOutcomeDrain
	cond.Fields = map[string]any{
		"predicted_effect": "converge",
		"stored_hash":      stored,
		"current_hash":     current,
	}
	emit.add(cond, true)
}

// detectDrain reports whether the row was claimed by the drain family.
func detectDrain(in detectorSweepInput, emit *detectorConditionSink, base detectorCondition, info sessionpkg.Info) bool {
	if in.Drains == nil {
		return false
	}
	state := in.Drains.get(info.ID)
	if state == nil {
		return false
	}
	// Tracker state only. The sweep performs NO GetMeta(GC_DRAIN_ACK) read: ack
	// discovery is handler-side (DETECTOR.md §3, D-DRAIN architect decision).
	cond := base
	cond.Family = detectorFamilyDrain
	cond.Site = TraceSiteReconcilerDrainAck
	cond.Reason = detectorReasonDrainInFlight
	cond.Outcome = TraceOutcomeDrain
	cond.Fields = map[string]any{
		"predicted_effect": "advance",
		"drain_reason":     state.reason,
	}
	emit.add(cond, true)
	return true
}

func detectWakeOrSleep(emit *detectorConditionSink, base detectorCondition, name string, awake map[string]AwakeDecision, live detectorLivenessBits) {
	decision, ok := awake[name]
	if !ok {
		return
	}
	switch {
	case decision.ShouldWake && !live.Alive:
		cond := base
		cond.Family = detectorFamilyWake
		cond.Site = TraceSiteReconcilerWakeDecision
		cond.Reason = detectorReasonWakeTarget
		cond.Outcome = TraceOutcomeStartCandidate
		cond.Fields = map[string]any{"predicted_effect": "start", "wake_reason": decision.Reason}
		emit.add(cond, false)
	case !decision.ShouldWake && live.Alive:
		cond := base
		cond.Family = detectorFamilySleep
		cond.Site = TraceSiteReconcilerDrainDecision
		cond.Reason = detectorReasonNoWake
		cond.Outcome = TraceOutcomeDrain
		cond.Fields = map[string]any{"predicted_effect": "drain", "no_wake_reason": decision.Reason}
		emit.add(cond, true)
	}
}

func detectZombie(emit *detectorConditionSink, base detectorCondition, live detectorLivenessBits) {
	if !live.Probed || !live.Running || live.Alive {
		return
	}
	cond := base
	cond.Family = detectorFamilyZombie
	cond.Site = TraceSiteReconcilerTerminalProviderError
	cond.Reason = detectorReasonZombie
	cond.Outcome = TraceOutcomeNoChange
	cond.Fields = map[string]any{"predicted_effect": "mark_unhealthy"}
	emit.add(cond, false)
}

func detectStall(in detectorSweepInput, emit *detectorConditionSink, base detectorCondition, infoByID map[string]sessionpkg.Info, tp TemplateParams, live detectorLivenessBits, now time.Time) {
	claimless := in.Cfg.Session.ProgressStallTimeoutDuration()
	claimHolder := in.Cfg.Session.ClaimHolderStallTimeoutDuration()
	gate := minPositiveDuration(claimless, claimHolder)
	if gate <= 0 || !live.Alive || !sessionActivityReportable(in.Provider, base.SessionName) {
		return
	}
	lastActivity, err := in.Provider.GetLastActivity(base.SessionName)
	if err != nil || lastActivity.IsZero() || now.Sub(lastActivity) <= gate {
		return
	}
	// The min-floor exemption suppresses the CLAIM-LESS arm only. When
	// claim_holder_stall_timeout is set, a floor worker is still a candidate:
	// the handler runs the per-session claim lookup (DETECTOR.md §3, D-STALL).
	if cfgAgent := findAgentByTemplate(in.Cfg, tp.TemplateName); cfgAgent != nil {
		minFloor := cfgAgent.EffectiveMinActiveSessions()
		if minFloor > 0 && isMinFloorIdleWorker(minFloor, openPoolSessionCountForTemplate(infoByID, in.Cfg, tp.TemplateName)) && claimHolder <= 0 {
			cond := base
			cond.Family = detectorFamilyStall
			cond.Site = TraceSiteReconcilerProgressStallExempt
			cond.Reason = detectorReasonProgressStallExempt
			cond.Outcome = TraceOutcomeNoChange
			cond.Fields = map[string]any{"predicted_effect": "none", "pool_min": minFloor}
			emit.add(cond, false)
			return
		}
	}
	cond := base
	cond.Family = detectorFamilyStall
	cond.Site = TraceSiteReconcilerProgressStallExempt
	cond.Reason = detectorReasonProgressStall
	cond.Outcome = TraceOutcomeStop
	cond.Fields = map[string]any{
		"predicted_effect":       "recycle",
		"idle_gap_seconds":       int64(now.Sub(lastActivity).Seconds()),
		"gate_threshold_seconds": int64(gate.Seconds()),
	}
	emit.add(cond, true)
}

func detectStranded(emit *detectorConditionSink, base detectorCondition, info sessionpkg.Info, desired bool, live detectorLivenessBits, now time.Time) {
	marker := strings.TrimSpace(info.StrandedEventEmittedAt)
	if marker == "" || live.Alive {
		return
	}
	emittedAt, err := time.Parse(time.RFC3339, marker)
	if err != nil {
		return
	}
	cond := base
	cond.Family = detectorFamilyStranded
	cond.Site = TraceSiteSessionReconcileWakeSleep
	cond.Reason = detectorReasonStrandedPoolSlot
	cond.Outcome = TraceOutcomeClosed
	cond.Fields = map[string]any{
		"predicted_effect":        "repair_and_close",
		"stranded_for_seconds":    int64(now.Sub(emittedAt).Seconds()),
		"desired":                 desired,
		"confirmation_window_sec": int64(strandedRepairConfirmGrace.Seconds()),
	}
	emit.add(cond, true)
}

// detectDuplicateNamed raises one condition per LOSER row when more than one
// open row shares a canonical named identity. Winner selection stays legacy's:
// highest generation, then canonical name, then oldest created-at.
func detectDuplicateNamed(emit *detectorConditionSink, byIdentity map[string][]sessionpkg.Info, cfg *config.City) {
	identities := make([]string, 0, len(byIdentity))
	for identity, rows := range byIdentity {
		if len(rows) > 1 {
			identities = append(identities, identity)
		}
	}
	sort.Strings(identities)
	for _, identity := range identities {
		rows := byIdentity[identity]
		winner := detectorDuplicateWinner(rows)
		for _, info := range rows {
			if info.ID == winner.ID {
				continue
			}
			emit.add(detectorCondition{
				Family:      detectorFamilyDup,
				SessionID:   info.ID,
				SessionName: strings.TrimSpace(info.SessionNameMetadata),
				Template:    normalizedSessionTemplateInfo(info, cfg),
				Identity:    identity,
				Site:        TraceSiteSessionReconcileHealRetire,
				Reason:      detectorReasonDuplicateNamed,
				Outcome:     TraceOutcomeNoChange,
				Fields: map[string]any{
					"predicted_effect": "retire",
					"winner_id":        winner.ID,
					"duplicate_count":  len(rows),
				},
			}, true)
		}
	}
}

func detectorDuplicateWinner(rows []sessionpkg.Info) sessionpkg.Info {
	winner := rows[0]
	for _, candidate := range rows[1:] {
		if detectorDuplicateBeats(candidate, winner) {
			winner = candidate
		}
	}
	return winner
}

func detectorDuplicateBeats(candidate, incumbent sessionpkg.Info) bool {
	if candidate.CreatedAt.After(incumbent.CreatedAt) {
		return true
	}
	if candidate.CreatedAt.Before(incumbent.CreatedAt) {
		return false
	}
	return candidate.ID > incumbent.ID
}

// detectorAdmissionSourceFor is THE detector half of the handler-dispatch seam.
// It answers one question for one detected condition: may this arm hand its
// exact key to the session-start controller, and under which admission source?
//
// Each later WD slice adds EXACTLY ONE case. The case names the family, its act
// constant, and the arm(s) that predict a real effect — a family's non-effect
// arms (deferrals, preserved rows, traced refusals) record for the parity join
// and never enqueue. No new controller, queue, or framework: the value returned
// here is fed straight into the existing Admit(id, source) entry.
func detectorAdmissionSourceFor(cond detectorCondition) (sessionStartAdmissionSource, bool) {
	switch cond.Family { //nolint:gocritic // the seam is a table, not a branch: WD.3-14 each add one case
	case detectorFamilyDeadline:
		return sessionStartAdmissionDeadline, detectorActDeadline && cond.Outcome == TraceOutcomeStop
	case detectorFamilyStaleCreate:
		return sessionStartAdmissionStaleCreate, detectorActStaleCreate && cond.Outcome == TraceOutcomeRollback
	}
	return "", false
}

// detectorProviderStopCapable is the detection-side D2 screen: a family whose
// handler must stop or kill a runtime is never enqueued under a provider that
// cannot prove fresh liveness and an unattended, token-bound stop. Screening
// here rather than in the handler is what keeps a D2-incapable city off the
// 30-second re-enqueue treadmill (DETECTOR.md §2).
func detectorProviderStopCapable(sp runtime.Provider) bool {
	if sp == nil {
		return false
	}
	if _, ok := sp.(runtime.FreshLivenessObserver); !ok {
		return false
	}
	_, ok := sp.(runtime.UnattendedSessionStopper)
	return ok
}

// routeDetectorConditions hands every acting family's effect arms to the
// existing session-start controller by exact key. It is the only place the
// sweep is allowed to leave read-only mode, and it still writes nothing itself:
// the handler behind the key owns every effect.
func routeDetectorConditions(in detectorSweepInput, result *detectorSweepResult) {
	if in.Admit == nil || result == nil || !detectorAnyFamilyActs() {
		return
	}
	for i := range result.Conditions {
		cond := &result.Conditions[i]
		source, routable := detectorAdmissionSourceFor(*cond)
		if !routable || cond.SessionID == "" {
			continue
		}
		if detectorFamilyDestructive(cond.Family) && !detectorProviderStopCapable(in.Provider) {
			cond.AdmissionOutcome = detectorAdmissionRefusedProviderIncapable
			continue
		}
		cond.AdmissionSource = source
		outcome, err := in.Admit(cond.SessionID, source)
		if err != nil {
			cond.AdmissionOutcome = detectorAdmissionRefusedError
			continue
		}
		cond.AdmissionOutcome = detectorRouteOutcome(outcome)
	}
}

// recordDetectorShadow writes the sweep's conditions to the trace cycle as
// detector-shadow records: the LEGACY site codes, effect_applied=false,
// effect_owner=detector-shadow, and a detector_-prefixed reason. A condition
// that WAS routed carries effect_owner=keyed instead, because the keyed
// population now owns it — but still effect_applied=false, because the sweep
// enqueued a key and applied nothing. The effect_applied=true record at the
// same legacy site is the handler's, written when the effect actually lands.
// The sweep writes nothing else — no store, no provider. A nil cycle (the `gc
// start` one-shot has no tracer) makes every call a no-op.
func recordDetectorShadow(cycle *sessionReconcilerTraceCycle, in detectorSweepInput, result detectorSweepResult) {
	if cycle == nil {
		return
	}
	conditions := result.Conditions
	if len(result.FamilyOverflow) > 0 {
		conditions = append(append([]detectorCondition(nil), conditions...), detectorOverflowSummaries(result.FamilyOverflow)...)
	}
	for _, cond := range conditions {
		owner := detectorShadowEffectOwner
		if cond.AdmissionSource != "" {
			owner = detectorKeyedEffectOwner
		}
		fields := traceRecordPayload{
			"effect_applied":   false,
			"effect_owner":     owner,
			"detector_family":  string(cond.Family),
			"detector_acts":    detectorFamilyActs(cond.Family),
			"session_id":       cond.SessionID,
			"session_identity": cond.Identity,
			"sweep_trigger":    in.Trigger,
		}
		if cond.AdmissionOutcome != "" {
			fields["admission_outcome"] = string(cond.AdmissionOutcome)
		}
		if cond.AdmissionSource != "" {
			fields["admission"] = string(cond.AdmissionSource)
		}
		for k, v := range cond.Fields {
			fields[k] = v
		}
		cycle.RecordDecision(cond.Site, cond.Reason, cond.Outcome, cond.Template, cond.SessionName, fields)
	}
	for _, spec := range detectorFamilySpecs {
		if !spec.ObservedOnly {
			continue
		}
		cycle.RecordControllerOperation(TraceSiteControllerTickPhase, detectorReasonObservedPatrolScan, TraceOutcomeNoChange,
			"detector_sweep.observed_patrol_scan", 0, map[string]any{
				"effect_applied":  false,
				"effect_owner":    detectorShadowEffectOwner,
				"detector_family": string(spec.Family),
				"observed_only":   true,
				"sweep_trigger":   in.Trigger,
			})
	}
	cycle.RecordControllerOperation(TraceSiteControllerTickPhase, detectorReasonSweepComplete, TraceOutcomeNoChange,
		"detector_sweep.complete", result.Duration, map[string]any{
			"effect_applied":              false,
			"effect_owner":                detectorShadowEffectOwner,
			"sweep_trigger":               in.Trigger,
			"rows_evaluated":              result.RowsEvaluated,
			"conditions":                  len(result.Conditions),
			"unknown_state_skipped":       result.UnknownStateSkipped,
			"suppressed_by_partial_store": result.SuppressedByPartialStore,
			"running_set_known":           result.RunningSetKnown,
			"store_query_partial":         in.StoreQueryPartial,
			"any_family_acts":             detectorAnyFamilyActs(),
			"duration_ms":                 result.Duration.Milliseconds(),
		})
}

// detectorSweepTriggerFor names the patrol-family entry point the sweep ran on.
func detectorSweepTriggerFor(bootReconcile bool) string {
	if bootReconcile {
		return "boot"
	}
	return "patrol"
}

// runDetectorSweep is the single production entry point: detect (read-only),
// route the acting families' exact keys into the existing session-start
// controller, then record. Shadow-only families and every entry point without
// an Admit hook come out of this exactly as they went in: read-only. Callers
// deliberately discard the result — the trace records ARE the output. Tests
// that need the conditions call detectSessionConditions directly.
func runDetectorSweep(ctx context.Context, cycle *sessionReconcilerTraceCycle, in detectorSweepInput) {
	result := detectSessionConditions(ctx, in)
	routeDetectorConditions(in, &result)
	recordDetectorShadow(cycle, in, result)
}
