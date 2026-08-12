package main

// This file is the machine-readable transcription of the DETECTOR.md section 3b
// classification table. It exists only for the WD parity campaign and is deleted
// with the rest of the D4-retained perf CLI at WE (DETECTOR.md section 5).
//
// Divergence rules are deliberately conservative: a rule is present only where
// section 3b names the divergence AND the trace codes that express it exist
// today. Where section 3b names a divergence whose codes land with a later WD
// slice, no rule is written — the mismatch then surfaces as UNCLASSIFIED, which
// is exactly the section 3b workflow ("triage it, extend the table with
// evidence, or fix the detector"). Inventing a speculative predicate would
// silently bucket real mismatches, which is the one failure the bar forbids.

const (
	parityJoinFamilyStart       = "start"
	parityJoinFamilyDeadline    = "D-DEADLINE"
	parityJoinFamilyOrphan      = "D-ORPHAN"
	parityJoinFamilyStaleCreate = "D-STALE-CREATE"
	parityJoinFamilyDrift       = "D-DRIFT"
	parityJoinFamilySleep       = "D-SLEEP"
	parityJoinFamilyDrain       = "D-DRAIN"
	parityJoinFamilyWake        = "D-WAKE"
	parityJoinFamilyZombie      = "D-ZOMBIE"
	parityJoinFamilyStall       = "D-STALL"
	parityJoinFamilyDup         = "D-DUP"
	parityJoinFamilyStranded    = "D-STRANDED"
)

type parityJoinLevel string

const (
	parityJoinLevelDetection parityJoinLevel = "detection"
	parityJoinLevelDecision  parityJoinLevel = "decision"
	parityJoinLevelAct       parityJoinLevel = "act"
)

type parityJoinSide string

const (
	parityJoinSideBoth         parityJoinSide = "both"
	parityJoinSideLegacyOnly   parityJoinSide = "legacy_only"
	parityJoinSideDetectorOnly parityJoinSide = "detector_only"
)

const (
	parityJoinMatched      = "matched"
	parityJoinMismatched   = "mismatched"
	parityJoinIncomparable = "incomparable"
)

// Classes raised by the join itself rather than by the section 3b table.
const (
	parityJoinClassBeadIDCrossCheck = "bead_id_cross_check_failed"
	parityJoinClassUnclassified     = "UNCLASSIFIED"
)

// parityJoinDivergenceRule triages one expected divergence. Every non-empty
// predicate must hold for the rule to fire; the first matching rule wins.
type parityJoinDivergenceRule struct {
	Class            string
	Classification   string // defaults to parityJoinMismatched
	Side             parityJoinSide
	Sites            []TraceSiteCode
	LegacyReasons    []TraceReasonCode
	LegacyOutcomes   []TraceOutcomeCode
	DetectorReasons  []TraceReasonCode
	DetectorOutcomes []TraceOutcomeCode
	AnyReasons       []TraceReasonCode
	AnyOutcomes      []TraceOutcomeCode
}

type parityJoinFamilySpec struct {
	Family      string
	Level       parityJoinLevel
	Sites       []TraceSiteCode
	Divergences []parityJoinDivergenceRule
}

// parityJoinGlobalDivergences apply to every family (the section 3b "(global)"
// row): on a partial store view legacy records Closed without closing while the
// detector suppresses the whole destructive family.
var parityJoinGlobalDivergences = []parityJoinDivergenceRule{{
	Class:         "store_query_partial_legacy_only",
	Side:          parityJoinSideLegacyOnly,
	LegacyReasons: []TraceReasonCode{TraceReasonStoreQueryPartial, TraceReasonStorePartial},
}}

var parityJoinFamilySpecs = []parityJoinFamilySpec{
	{
		// "existing shadow-worker + comparator evidence | per existing comparators"
		Family: parityJoinFamilyStart,
		Level:  parityJoinLevelAct,
		Sites: []TraceSiteCode{
			TraceSiteLifecycleStartPrepare,
			TraceSiteLifecycleStartExecute,
			TraceSiteLifecycleStartCommit,
			TraceSiteLifecycleStartRun,
			TraceSiteLifecycleStartFailed,
			TraceSiteLifecycleStartRollback,
			TraceSiteLifecycleStartSelectionShadow,
		},
	},
	{
		// "legacy pending-interaction deferral (probe-only signal, unpredicted)"
		Family: parityJoinFamilyDeadline,
		Level:  parityJoinLevelDecision,
		Sites:  []TraceSiteCode{TraceSiteReconcilerIdleTimeout, TraceSiteReconcilerMaxSessionAge},
		Divergences: []parityJoinDivergenceRule{{
			Class:          "legacy_pending_interaction_deferral",
			LegacyOutcomes: []TraceOutcomeCode{TraceOutcomeDeferred, TraceOutcomeDeferredPending, TraceOutcomeDeferredBusy},
		}},
	},
	{
		// "deferred-confirm off-by-one (duplicated counters); liveness-error arm incomparable"
		Family: parityJoinFamilyOrphan,
		Level:  parityJoinLevelDecision,
		Sites: []TraceSiteCode{
			TraceSiteReconcilerOrphaned,
			TraceSiteReconcilerCloseOrphan,
			TraceSiteReconcilerCloseFailedCreate,
		},
		Divergences: []parityJoinDivergenceRule{
			{
				Class:          "liveness_error_arm",
				Classification: parityJoinIncomparable,
				AnyOutcomes:    []TraceOutcomeCode{TraceOutcomeSkippedLivenessError},
			},
			{
				Class:       "deferred_confirm_off_by_one",
				AnyOutcomes: []TraceOutcomeCode{TraceOutcomeDeferredConfirm},
			},
		},
	},
	{
		// "legacy defers rollback #6+ (R6 budget retired)"
		Family: parityJoinFamilyStaleCreate,
		Level:  parityJoinLevelDecision,
		Sites:  []TraceSiteCode{TraceSiteReconcilerPendingCreate, TraceSiteReconcilerPendingCreatePreserved},
		Divergences: []parityJoinDivergenceRule{{
			Class:       "legacy_defers_rollback_beyond_budget",
			AnyOutcomes: []TraceOutcomeCode{TraceOutcomeRollbackDeferred},
		}},
	},
	{
		// Detection level: the entire 5-arm ladder is handler-side, so reason and
		// outcome are not compared at all. No singleton rule — a drift record on
		// one side only is a real candidacy gap, not an expected divergence.
		Family: parityJoinFamilyDrift,
		Level:  parityJoinLevelDetection,
		Sites:  []TraceSiteCode{TraceSiteReconcilerConfigDrift, TraceSiteReconcilerLiveDrift},
	},
	{
		// "probe/pending arms unpredicted"
		Family: parityJoinFamilySleep,
		Level:  parityJoinLevelDecision,
		Sites:  []TraceSiteCode{TraceSiteReconcilerDrainDecision},
		Divergences: []parityJoinDivergenceRule{
			{
				Class:          "probe_arm_unpredicted",
				Classification: parityJoinIncomparable,
				AnyReasons:     []TraceReasonCode{TraceReasonPending},
			},
			{
				Class:          "pending_arm_unpredicted",
				Classification: parityJoinIncomparable,
				AnyOutcomes:    []TraceOutcomeCode{TraceOutcomeDeferredPending},
			},
			{
				// WD.5 delta 1: legacy's plain "no-wake-reason" rung is a fleet
				// verdict the keyed handler cannot re-derive per key, so the
				// detector records those rows and never enqueues them. Legacy
				// drains where the detector predicts nothing, by design, until
				// D-WAKE gives the fleet demand rungs a keyed home.
				Class:           "fleet_only_no_wake_left_to_legacy",
				Classification:  parityJoinIncomparable,
				DetectorReasons: []TraceReasonCode{detectorReasonNoWakeFleetOnly},
			},
			{
				// WD.5 delta 4: the per-sweep probe budget and the probe already in
				// flight are detector-side scheduling, not a decision about the row.
				Class:           "idle_probe_scheduling",
				Classification:  parityJoinIncomparable,
				DetectorReasons: []TraceReasonCode{detectorReasonIdleProbePending, detectorReasonIdleProbeBudget},
			},
			{
				// WD.5 delta 2: the #3994 keep-alive escape is a detection-side
				// non-enqueue where legacy cancels mid-pass and records nothing.
				Class:           "keep_alive_escape_detector_only",
				Classification:  parityJoinIncomparable,
				Side:            parityJoinSideDetectorOnly,
				DetectorReasons: []TraceReasonCode{detectorReasonSleepKeepAlive},
			},
		},
	},
	{
		// "ack-timing skew (handler-side ack read vs legacy's in-tick poll);
		// advance arms journey-proven". Both are singleton classes: the pair lands
		// in adjacent cycles, which a same-cycle-handle join reports as one
		// legacy-only and one detector-only record.
		Family: parityJoinFamilyDrain,
		Level:  parityJoinLevelDetection,
		Sites: []TraceSiteCode{
			TraceSiteReconcilerDrainAck,
			TraceSiteDrainCancel,
			TraceSiteDrainComplete,
			TraceSiteDrainStale,
			TraceSiteDrainTimeout,
			TraceSiteLifecycleDrainBegin,
			TraceSiteLifecycleDrainAdvance,
			TraceSiteSessionReconcileDrainAdvance,
		},
		Divergences: []parityJoinDivergenceRule{
			{
				Class: "ack_timing_skew",
				Sites: []TraceSiteCode{TraceSiteReconcilerDrainAck},
			},
			{
				Class:          "advance_arms_journey_proven",
				Classification: parityJoinIncomparable,
				Sites: []TraceSiteCode{
					TraceSiteDrainComplete,
					TraceSiteLifecycleDrainAdvance,
					TraceSiteSessionReconcileDrainAdvance,
				},
			},
		},
	},
	{
		// "legacy quarantine skip is UNTRACED (:3702-3705) -> detector-present/
		// legacy-absent, expected"
		Family: parityJoinFamilyWake,
		Level:  parityJoinLevelDecision,
		Sites:  []TraceSiteCode{TraceSiteReconcilerWakeDecision, TraceSiteReconcilerPreserveConfiguredNamed},
		Divergences: []parityJoinDivergenceRule{
			{
				Class:           "untraced_legacy_quarantine_skip",
				Side:            parityJoinSideDetectorOnly,
				DetectorReasons: []TraceReasonCode{TraceReasonQuarantine},
			},
			{
				Class:            "untraced_legacy_quarantine_skip",
				Side:             parityJoinSideDetectorOnly,
				DetectorOutcomes: []TraceOutcomeCode{TraceOutcomeDeferredQuarantine},
			},
		},
	},
	{
		// Detection level: the classification arm is handler-side and therefore
		// already excluded from the comparison. A candidacy gap stays a mismatch.
		Family: parityJoinFamilyZombie,
		Level:  parityJoinLevelDetection,
		Sites:  []TraceSiteCode{TraceSiteReconcilerTerminalProviderError},
	},
	{
		// "claim-check-error fail-safe arm incomparable" — the fail-safe arm's
		// codes land with WD.13, so no rule yet; it triages as UNCLASSIFIED until
		// the slice that emits it extends this entry with evidence.
		Family: parityJoinFamilyStall,
		Level:  parityJoinLevelDecision,
		Sites:  []TraceSiteCode{TraceSiteReconcilerResetStalled, TraceSiteReconcilerProgressStallExempt},
	},
	{
		// "none expected" — every divergence here is a WE blocker by design.
		Family: parityJoinFamilyDup,
		Level:  parityJoinLevelDecision,
		Sites:  []TraceSiteCode{TraceSiteSessionReconcileHealRetire},
	},
	{
		// "confirmation-window off-by-one (duplicated counters)" — the class
		// name is a misnomer WD.15 owns retiring (WD.14 delta 2): the window is
		// ONE durable marker (stranded_event_emitted_at) read by both paths, so
		// no counters can skew. What the rule actually triages is the detector's
		// in-window DEFER arm, which legacy records nothing for — this family
		// has no legacy decision record at all (WD.14 delta 1), so its detection
		// parity is candidacy agreement, not a record-to-record join.
		Family: parityJoinFamilyStranded,
		Level:  parityJoinLevelDetection,
		Sites:  []TraceSiteCode{TraceSiteSessionReconcileWakeSleep},
		Divergences: []parityJoinDivergenceRule{{
			Class:       "confirmation_window_off_by_one",
			AnyOutcomes: []TraceOutcomeCode{TraceOutcomeDeferredConfirm},
		}},
	},
}

// parityJoinSiteAttribution says what an effect_owner-ABSENT record at a site
// means. It is the machine-readable half of the section 1 site-disposition
// table: which of the 28 sites the god function itself writes a per-session
// decision at, and which it does not touch.
//
// The tool needs this because no line of production code stamps
// effect_owner="legacy". Every keyed handler stamps "keyed", the sweep stamps
// "detector-shadow" (or "keyed" for a routed condition), and legacy stamps
// nothing — so legacy is identified by ELIMINATION, not by a stamp it will
// never carry. The alternative, teaching the god function to stamp, is
// scaffolding built into code scheduled for deletion at WE.
type parityJoinSiteAttribution string

const (
	// parityJoinSiteLegacy is a section 1 DECISION site: the god function
	// writes a per-session decision record here, unstamped. Absence of
	// effect_owner classifies the record as legacy.
	parityJoinSiteLegacy parityJoinSiteAttribution = "legacy"
	// parityJoinSitePhase is a section 1 PHASE site. Legacy writes exactly one
	// cycle-level marker per tick here (reason=retained, outcome=complete, no
	// session identity); only the keyed and detector writers write per-session
	// rows, and they stamp. Binning the marker as legacy would manufacture one
	// phantom legacy-only row per cycle in D-DUP and D-STRANDED, whose only
	// sites are phase sites.
	parityJoinSitePhase parityJoinSiteAttribution = "phase"
	// parityJoinSiteNonLegacy is a site with no legacy per-session writer, or
	// one whose writer serves both engines. Absence cannot be attributed by
	// elimination, so the record is counted and surfaced rather than binned.
	parityJoinSiteNonLegacy parityJoinSiteAttribution = "non_legacy"
)

type parityJoinSiteDisposition struct {
	Attribution parityJoinSiteAttribution
	// Note is the section 1 row (or the writer) this transcribes, so a reader
	// of the readout can check the claim without reading Go.
	Note string
}

// parityJoinSiteDispositions covers every site the section 3b family table
// claims. TestParityJoinSiteDispositionsCoverEveryFamilySite enforces that.
var parityJoinSiteDispositions = map[TraceSiteCode]parityJoinSiteDisposition{
	// start — section 1 row 27 (StartExecution) is KEYED-OWNED ALREADY, and its
	// shared start wave serves both paths, so nothing here attributes. Section
	// 3b routes this family to the existing shadow-worker comparators anyway.
	TraceSiteLifecycleStartPrepare:         {parityJoinSiteNonLegacy, "s1#27 keyed-owned: the keyed start wave fires lifecycle.start.prepare"},
	TraceSiteLifecycleStartExecute:         {parityJoinSiteNonLegacy, "s1#27 keyed-owned: the keyed start wave fires lifecycle.start.execute"},
	TraceSiteLifecycleStartCommit:          {parityJoinSiteNonLegacy, "s1#27 keyed-owned: the keyed start wave fires lifecycle.start.commit"},
	TraceSiteLifecycleStartRun:             {parityJoinSiteNonLegacy, "s1#27 shared start wave (session_lifecycle_parallel.go) serves both paths"},
	TraceSiteLifecycleStartFailed:          {parityJoinSiteNonLegacy, "s1#27 shared start wave (session_lifecycle_parallel.go) serves both paths"},
	TraceSiteLifecycleStartRollback:        {parityJoinSiteNonLegacy, "s1#27 shared start wave (session_lifecycle_parallel.go) serves both paths"},
	TraceSiteLifecycleStartSelectionShadow: {parityJoinSiteNonLegacy, "start-selection shadow comparator, not a reconciler decision"},

	// D-DEADLINE
	TraceSiteReconcilerIdleTimeout:   {parityJoinSiteLegacy, "s1#1 IdleTimeout"},
	TraceSiteReconcilerMaxSessionAge: {parityJoinSiteLegacy, "s1#2 MaxSessionAge"},

	// D-ORPHAN
	TraceSiteReconcilerOrphaned:          {parityJoinSiteLegacy, "s1#3 Orphaned"},
	TraceSiteReconcilerCloseOrphan:       {parityJoinSiteLegacy, "s1#4 CloseOrphan"},
	TraceSiteReconcilerCloseFailedCreate: {parityJoinSiteLegacy, "s1#5 CloseFailedCreate"},

	// D-STALE-CREATE
	TraceSiteReconcilerPendingCreate:          {parityJoinSiteLegacy, "s1#6 PendingCreate"},
	TraceSiteReconcilerPendingCreatePreserved: {parityJoinSiteLegacy, "s1#7 PendingCreatePreserved"},

	// D-DRIFT
	TraceSiteReconcilerConfigDrift: {parityJoinSiteLegacy, "s1#8 ConfigDrift"},
	TraceSiteReconcilerLiveDrift:   {parityJoinSiteLegacy, "s1#9 LiveDrift"},

	// D-SLEEP
	TraceSiteReconcilerDrainDecision: {parityJoinSiteLegacy, "s1#12 DrainDecision"},

	// D-DRAIN. The legacy drain engine (session_wake.go) writes the four
	// reconciler.drain.* sites unstamped; the keyed drain handler
	// (session_drain_reconcile.go) writes the same sites with effect_owner=keyed.
	TraceSiteReconcilerDrainAck:           {parityJoinSiteLegacy, "s1#10 DrainAck"},
	TraceSiteDrainCancel:                  {parityJoinSiteLegacy, "s1#11 DrainCancel"},
	TraceSiteDrainStale:                   {parityJoinSiteLegacy, "legacy drain engine session_wake.go, unstamped"},
	TraceSiteDrainComplete:                {parityJoinSiteLegacy, "legacy drain engine session_wake.go, unstamped"},
	TraceSiteDrainTimeout:                 {parityJoinSiteLegacy, "legacy drain engine session_wake.go, unstamped"},
	TraceSiteLifecycleDrainBegin:          {parityJoinSiteNonLegacy, "no production writer"},
	TraceSiteLifecycleDrainAdvance:        {parityJoinSiteNonLegacy, "keyed drain advance (session_start_reconcile.go)"},
	TraceSiteSessionReconcileDrainAdvance: {parityJoinSitePhase, "s1#28 DrainAdvance (phase)"},

	// D-WAKE
	TraceSiteReconcilerWakeDecision:            {parityJoinSiteLegacy, "s1#19 WakeDecision"},
	TraceSiteReconcilerPreserveConfiguredNamed: {parityJoinSiteLegacy, "s1#13 PreserveConfiguredNamed"},

	// D-ZOMBIE
	TraceSiteReconcilerTerminalProviderError: {parityJoinSiteLegacy, "s1#15 TerminalProviderError"},

	// D-STALL
	TraceSiteReconcilerResetStalled:        {parityJoinSiteLegacy, "legacy stall reset (session_reconciler.go), unstamped"},
	TraceSiteReconcilerProgressStallExempt: {parityJoinSiteLegacy, "s1#14 ProgressStallExempt"},

	// D-DUP / D-STRANDED: phase sites only. Legacy has no per-session decision
	// record in either family (WD.13 / WD.14 delta 1), so their detection parity
	// is candidacy agreement, not a record-to-record join.
	TraceSiteSessionReconcileHealRetire: {parityJoinSitePhase, "s1#22 HealRetire (phase)"},
	TraceSiteSessionReconcileWakeSleep:  {parityJoinSitePhase, "s1#26 WakeSleep (phase)"},
}

// parityJoinKeyedSeamYieldReasons are legacy's coexistence-seam yields: arms
// where the god function stepped aside because the keyed controller holds the
// key, so the EFFECT belongs to the keyed population even though legacy wrote
// the record. Three of the four stamp effect_owner=keyed already
// (session_reconciler.go:1547, :2436, :3595); the wake arm (:1882, :4093) does
// not, and binning its yields as legacy would swamp D-WAKE with rows for
// decisions legacy explicitly declined to make.
var parityJoinKeyedSeamYieldReasons = map[TraceReasonCode]bool{
	"keyed_start_owner":        true,
	"keyed_deadline_owner":     true,
	"keyed_orphan_drain_owner": true,
	"keyed_stale_create_owner": true,
}

// parityJoinSiteFamily indexes every section 3b site to its family.
var parityJoinSiteFamily = func() map[TraceSiteCode]*parityJoinFamilySpec {
	index := make(map[TraceSiteCode]*parityJoinFamilySpec)
	for i := range parityJoinFamilySpecs {
		spec := &parityJoinFamilySpecs[i]
		for _, site := range spec.Sites {
			index[site] = spec
		}
	}
	return index
}()
