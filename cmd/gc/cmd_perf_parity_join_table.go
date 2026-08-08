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
		// "confirmation-window off-by-one (duplicated counters)"
		Family: parityJoinFamilyStranded,
		Level:  parityJoinLevelDetection,
		Sites:  []TraceSiteCode{TraceSiteSessionReconcileWakeSleep},
		Divergences: []parityJoinDivergenceRule{{
			Class:       "confirmation_window_off_by_one",
			AnyOutcomes: []TraceOutcomeCode{TraceOutcomeDeferredConfirm},
		}},
	},
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
