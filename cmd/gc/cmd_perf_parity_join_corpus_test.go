package main

import (
	"bufio"
	"encoding/json"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/config"
)

// These tests are the WD.15 day-0 lesson written down. The suite next door
// builds records by setting struct fields the collector never sets — a shape
// production cannot produce — which is how a blind reader (the typed
// DetailedTemplateCount) and a blind classifier (owner-absent records dropped
// on the floor) both shipped green. Everything here is production-shaped:
// either byte-copied from the live campaign corpus at
// /data/cities/reconciler-campaign, or written by the real collector and read
// back through the real store, so the JSON round-trip is part of the test.

const parityJoinCorpusFixture = "testdata/wd15_campaign_corpus.jsonl"

// parityJoinCorpusRecords decodes the byte-copied campaign corpus fixture.
func parityJoinCorpusRecords(t *testing.T) []SessionReconcilerTraceRecord {
	t.Helper()
	f, err := os.Open(parityJoinCorpusFixture)
	if err != nil {
		t.Fatalf("opening campaign corpus fixture: %v", err)
	}
	defer f.Close() //nolint:errcheck
	var out []SessionReconcilerTraceRecord
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 1<<20), 1<<20)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var rec SessionReconcilerTraceRecord
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("decoding campaign corpus line: %v", err)
		}
		out = append(out, rec)
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("reading campaign corpus fixture: %v", err)
	}
	return out
}

// parityJoinArmTemplate installs the detail arm the collector requires before it
// promotes a record. Without it the record path stashes and returns
// (trace_collector.go:335-353) and the whole cycle reads empty — the campaign's
// original arming gap, reproduced in miniature.
func parityJoinArmTemplate(t *testing.T, cityDir, template string) {
	t.Helper()
	now := time.Now().UTC()
	arms := newSessionReconcilerTraceArmStore(cityDir)
	if _, err := arms.upsertArm(TraceArm{
		ScopeType:  TraceArmScopeTemplate,
		ScopeValue: template,
		Source:     TraceArmSourceManual,
		Level:      TraceModeDetail,
		ArmedAt:    now,
		ExpiresAt:  now.Add(30 * time.Minute),
		UpdatedAt:  now,
	}); err != nil {
		t.Fatalf("upsertArm: %v", err)
	}
}

func parityJoinDispositionCount(report parityJoinReport, site TraceSiteCode, disposition string) int {
	total := 0
	for _, entry := range report.Dispositions {
		if entry.Site == site && entry.Disposition == disposition {
			total += entry.Count
		}
	}
	return total
}

// B1. Nothing in the tree stamps effect_owner="legacy": every keyed handler
// stamps keyed, the sweep stamps detector-shadow, and the god function stamps
// nothing. The tool therefore classifies owner-ABSENT records at legacy trace
// sites as legacy by elimination, and the live corpus's legacy population
// appears where day-0 saw only unowned_records.
func TestParityJoinAttributesUnstampedLegacyRecordsOnTheLiveCorpus(t *testing.T) {
	report := buildParityJoinReport(parityJoinCorpusRecords(t), parityJoinOptions{Bar: 0.995, Samples: 8})

	if report.Cycles.LegacyByElimination != 9 {
		t.Fatalf("legacy_by_elimination = %d, want 9 (2x drain/no-wake-reason, 3x wake_decision/wake, drain_ack/acknowledged, drain_ack/orphaned, drain.timeout/orphaned, rollback_pending_create/recovery — %+v)",
			report.Cycles.LegacyByElimination, report.Cycles)
	}

	// The cycle where legacy and the sweep both wrote for the same session at
	// the same site joins, and lands in a real section 3b class.
	sleep := parityJoinFamilyRow(t, report, parityJoinFamilySleep)
	if sleep.Joined != 1 {
		t.Fatalf("D-SLEEP joined = %d, want 1 (%+v)", sleep.Joined, sleep)
	}
	if sleep.Unclassified != 0 {
		t.Fatalf("D-SLEEP unclassified = %d, want 0 (%+v)", sleep.Unclassified, sleep)
	}
	if got := parityJoinTriageCount(report, parityJoinFamilySleep, "fleet_only_no_wake_left_to_legacy"); got != 2 {
		t.Fatalf("fleet-only triage count = %d, want 2 (the joined pair and the legacy-only singleton) (triage=%+v)", got, report.Triage)
	}

	// Legacy's own acknowledgement singleton stays a singleton and triages into
	// the ack-timing-skew class, not into unowned_records.
	if got := parityJoinTriageCount(report, parityJoinFamilyDrain, "ack_timing_skew"); got != 1 {
		t.Fatalf("D-DRAIN ack_timing_skew = %d, want 1 (triage=%+v)", got, report.Triage)
	}
}

// The owner ruling's yield-join: legacy's traced stand-down beside the keyed
// actor's record for the same row in the same tick. Both of these pairs are
// byte-copied from the live campaign corpus, where the D-DEADLINE seam alone
// produces thousands of them per hour — the evidence auto mode actually makes.
func TestParityJoinPairsLegacyYieldsAgainstTheActorOnTheLiveCorpus(t *testing.T) {
	report := buildParityJoinReport(parityJoinCorpusRecords(t), parityJoinOptions{Bar: 0.995, Samples: 8})

	deadline := parityJoinFamilyRow(t, report, parityJoinFamilyDeadline)
	if deadline.YieldJoined != 1 || deadline.Matched != 1 {
		t.Fatalf("D-DEADLINE yield_joined=%d matched=%d, want 1/1 — keyed_deadline_owner beside detector_idle_timeout (%+v)",
			deadline.YieldJoined, deadline.Matched, deadline)
	}
	orphan := parityJoinFamilyRow(t, report, parityJoinFamilyOrphan)
	if orphan.YieldJoined != 1 || orphan.Matched != 1 {
		t.Fatalf("D-ORPHAN yield_joined=%d matched=%d, want 1/1 — keyed_orphan_drain_owner beside detector_orphan_live (%+v)",
			orphan.YieldJoined, orphan.Matched, orphan)
	}
	if report.JoinedYields != 2 {
		t.Fatalf("joined_yields = %d, want 2 (%+v)", report.JoinedYields, report.Families)
	}
	if report.JoinedActs == 0 {
		t.Fatal("joined_acts = 0: the act-vs-act join must survive beside the yield-join")
	}

	// The yield vocabulary is reported, not just consumed.
	var deadlineYield *parityJoinYieldEntry
	for i := range report.Yields {
		if report.Yields[i].Reason == "keyed_deadline_owner" {
			deadlineYield = &report.Yields[i]
		}
	}
	if deadlineYield == nil {
		t.Fatalf("yields log has no keyed_deadline_owner entry: %+v", report.Yields)
	}
	if deadlineYield.Arm != parityJoinYieldCandidacy || deadlineYield.Joined != 1 {
		t.Fatalf("keyed_deadline_owner entry = %+v, want candidacy arm with joined=1", *deadlineYield)
	}
}

// A stand-down that only asserts ownership is not evidence. The wake seam fires
// at the top of the row scan before any condition is evaluated, and its two arms
// are indistinguishable in the record, so an unpaired one is counted and
// surfaced rather than scored as a divergence — the same discipline that keeps
// phase markers out of D-DUP and D-STRANDED.
func TestParityJoinRefusesToScoreUnpairedOwnershipYields(t *testing.T) {
	report := buildParityJoinReport(parityJoinCorpusRecords(t), parityJoinOptions{Bar: 0.995, Samples: 8})

	if report.Cycles.UnpairedOwnershipYields != 1 {
		t.Fatalf("unpaired_ownership_yields = %d, want 1 (%+v)", report.Cycles.UnpairedOwnershipYields, report.Cycles)
	}
	wake := parityJoinFamilyRow(t, report, parityJoinFamilyWake)
	if wake.YieldOnly != 0 || wake.Mismatched != 0 {
		t.Fatalf("D-WAKE yield_only=%d mismatched=%d, want 0/0 — an ownership yield is not a divergence (%+v)",
			wake.YieldOnly, wake.Mismatched, wake)
	}
	var entry *parityJoinYieldEntry
	for i := range report.Yields {
		if report.Yields[i].Reason == "keyed_start_owner" {
			entry = &report.Yields[i]
		}
	}
	if entry == nil || entry.Arm != parityJoinYieldOwnership || entry.Unpaired != 1 {
		t.Fatalf("keyed_start_owner yields entry = %+v, want ownership arm with unpaired=1 (%+v)", entry, report.Yields)
	}
}

// The sweep stamps effect_owner=keyed for a condition it ROUTED, so before the
// ruling every routed family's record fell into the keyed column and joined
// nothing. Widening the actor side to the sweep's own records turns three of
// ga-f7v2ft.158's legacy-only shapes into act-pairs.
func TestParityJoinJoinsRoutedSweepRecordsAgainstLegacyActs(t *testing.T) {
	report := buildParityJoinReport(parityJoinCorpusRecords(t), parityJoinOptions{Bar: 0.995, Samples: 8})

	wake := parityJoinFamilyRow(t, report, parityJoinFamilyWake)
	if wake.Joined != 2 || wake.Matched != 1 {
		t.Fatalf("D-WAKE joined=%d matched=%d, want 2/1 — legacy wake/start_candidate beside the routed detector_wake_target, plus the admission-refused pair (%+v)",
			wake.Joined, wake.Matched, wake)
	}
	drain := parityJoinFamilyRow(t, report, parityJoinFamilyDrain)
	if drain.Joined != 1 {
		t.Fatalf("D-DRAIN joined = %d, want 1 — legacy orphaned/stop_pending beside the routed detector_drain_in_flight (%+v)",
			drain.Joined, drain)
	}
}

// Decision-level parity compares the OUTCOME. The two writers' reason
// vocabularies are disjoint by construction, so the old reason-equality clause
// could only ever match a seeded pair — on the live corpus it turned every
// decision-level act-pair into an unclassified WE blocker.
func TestParityJoinDecisionLevelMatchesOnOutcomeNotReason(t *testing.T) {
	report := buildParityJoinReport(parityJoinCorpusRecords(t), parityJoinOptions{Bar: 0.995, Samples: 8})

	wake := parityJoinFamilyRow(t, report, parityJoinFamilyWake)
	if wake.Unclassified != 0 {
		t.Fatalf("D-WAKE unclassified = %d, want 0: legacy (wake, start_candidate) and the sweep's (detector_wake_target, start_candidate) decide the same thing (%+v)",
			wake.Unclassified, wake)
	}
	for _, sample := range report.Unclassified {
		if sample.LegacyReason != "" && sample.DetectorReason != "" {
			t.Fatalf("a joined pair was left unclassified on a reason difference alone: %+v", sample)
		}
	}
}

// The cross-family split ga-f7v2ft.158 filed as two separate shapes. The sweep
// claims each row for ONE family; legacy runs an arm per pass. A start-pending
// row with a live create lease is claimed by D-STALE-CREATE's preserve arm while
// legacy's wake pass drives the start already in flight — same row, same tick,
// two families, two sites. The rule fires only because the twin is in the cycle.
func TestParityJoinTriagesThePendingCreateFamilySplitAgainstItsTwin(t *testing.T) {
	report := buildParityJoinReport(parityJoinCorpusRecords(t), parityJoinOptions{Bar: 0.995, Samples: 8})

	for _, family := range []string{parityJoinFamilyWake, parityJoinFamilyStaleCreate} {
		if got := parityJoinTriageCount(report, family, parityJoinClassPendingCreateFamilySplit); got != 1 {
			t.Fatalf("%s %s = %d, want 1 (triage=%+v)", family, parityJoinClassPendingCreateFamilySplit, got, report.Triage)
		}
		if row := parityJoinFamilyRow(t, report, family); row.Unclassified != 0 {
			t.Fatalf("%s unclassified = %d, want 0 (%+v)", family, row.Unclassified, row)
		}
	}
}

// The rest of ga-f7v2ft.158's survivors: the advance engine's own arms, which
// the sweep detects one site away, and legacy's live-runtime recovery deferral,
// which the sweep excludes from the family by construction.
func TestParityJoinTriagesTheRemainingLegacyOnlySingletons(t *testing.T) {
	report := buildParityJoinReport(parityJoinCorpusRecords(t), parityJoinOptions{Bar: 0.995, Samples: 8})

	// Two arms of one class: legacy's drain.timeout completion beside the
	// sweep's drain_ack detection, and that detection itself, which has no
	// legacy per-session twin because legacy's advance pass is a phase site.
	if got := parityJoinTriageCount(report, parityJoinFamilyDrain, "advance_arms_journey_proven"); got != 2 {
		t.Fatalf("advance_arms_journey_proven = %d, want 2 (triage=%+v)", got, report.Triage)
	}
	if got := parityJoinTriageCount(report, parityJoinFamilyStaleCreate, "live_runtime_recovery_excluded_from_sweep"); got != 1 {
		t.Fatalf("live_runtime_recovery_excluded_from_sweep = %d, want 1 (triage=%+v)", got, report.Triage)
	}
	if report.WEBlocker {
		t.Fatalf("we_blocker = true: the campaign corpus fixture must triage clean, unclassified=%+v", report.Unclassified)
	}
}

// B1's guard. Absence classifies as legacy only inside the section 1 legacy
// vocabulary. A phase site's per-cycle marker and a keyed-owned site are each
// counted and surfaced with the reason they were refused — never binned as
// legacy, which would have manufactured phantom legacy-only rows in exactly the
// families (D-DUP, D-STRANDED) whose only site is a phase site.
func TestParityJoinRefusesToAttributeAbsenceOutsideTheLegacyVocabulary(t *testing.T) {
	report := buildParityJoinReport(parityJoinCorpusRecords(t), parityJoinOptions{Bar: 0.995, Samples: 8})

	for _, family := range []string{parityJoinFamilyDup, parityJoinFamilyStranded, parityJoinFamilyStart} {
		row := parityJoinFamilyRow(t, report, family)
		if row.LegacyOnly != 0 || row.Joined != 0 || row.YieldJoined != 0 {
			t.Fatalf("family %q took a phantom legacy row: legacy_only=%d joined=%d yield_joined=%d (%+v)",
				family, row.LegacyOnly, row.Joined, row.YieldJoined, row)
		}
	}

	for _, want := range []struct {
		site        TraceSiteCode
		disposition string
	}{
		{TraceSiteSessionReconcileHealRetire, parityJoinDispositionPhaseMarker},
		{TraceSiteSessionReconcileWakeSleep, parityJoinDispositionPhaseMarker},
		{TraceSiteLifecycleStartCommit, parityJoinDispositionUnattributable},
	} {
		if got := parityJoinDispositionCount(report, want.site, want.disposition); got != 1 {
			t.Fatalf("owner_absence[%s/%s] = %d, want 1 (absence=%+v)", want.site, want.disposition, got, report.Dispositions)
		}
	}

	// Every refused record is still counted, so the readout never loses one.
	if report.Cycles.UnownedRecords != 4 {
		t.Fatalf("unowned_records = %d, want 4 (2 phase markers, 1 keyed-owned site, 1 sessionless pool-fill): %+v",
			report.Cycles.UnownedRecords, report.Cycles)
	}

	var out strings.Builder
	if err := writeParityJoinReport(&out, report); err != nil {
		t.Fatalf("writeParityJoinReport: %v", err)
	}
	for _, want := range []string{"OWNER-ABSENT", parityJoinDispositionPhaseMarker, "YIELDS", "keyed_deadline_owner"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("human readout hides the owner-absence or yield taxonomy (missing %q):\n%s", want, out.String())
		}
	}
}

// B2. The collector writes every cycle-rollup counter into rec.Fields and never
// sets the typed rec.DetailedTemplateCount, so a reader of the typed field sees
// zero on every real rollup and the campaign's own "did this window run armed?"
// alarm reads all-unarmed. Six of the seven fixture cycles carry a detail arm.
func TestParityJoinReadsRollupCountersWhereTheCollectorWritesThem(t *testing.T) {
	records := parityJoinCorpusRecords(t)
	rollups := 0
	for _, rec := range records {
		if rec.RecordType != TraceRecordCycleResult {
			continue
		}
		rollups++
		if rec.DetailedTemplateCount != 0 {
			t.Fatalf("fixture rollup carries a typed detailed_template_count=%d; production never sets it, so this fixture is no longer production-shaped",
				rec.DetailedTemplateCount)
		}
	}
	if rollups != 15 {
		t.Fatalf("fixture carries %d cycle rollups, want 15", rollups)
	}

	report := buildParityJoinReport(records, parityJoinOptions{Bar: 0.995})

	if report.Cycles.Considered != 15 {
		t.Fatalf("considered = %d, want 15 (%+v)", report.Cycles.Considered, report.Cycles)
	}
	if report.Cycles.WithoutDetailArms != 1 {
		t.Fatalf("without_detail_arms = %d, want 1 — fourteen fixture rollups carry fields.detailed_template_count>0 (%+v)",
			report.Cycles.WithoutDetailArms, report.Cycles)
	}
}

// B2, end to end through the real collector and the real store: the mis-arming
// alarm must go quiet for a cycle that actually ran armed. A synthesized rollup
// cannot prove this — only a record the collector wrote and the store read back
// carries the counter where production puts it, as the JSON number it becomes.
func TestParityJoinArmingAlarmIsQuietForACollectorWrittenArmedCycle(t *testing.T) {
	cityDir := t.TempDir()
	parityJoinArmTemplate(t, cityDir, "worker")
	now := time.Now().UTC()

	tracer := newSessionReconcilerTracer(cityDir, "wd15-arming", io.Discard)
	if !tracer.Enabled() {
		t.Fatal("tracer should be enabled")
	}
	cycle := tracer.BeginCycle(TraceTickTriggerPatrol, "", now, &config.City{})
	if cycle == nil {
		t.Fatal("BeginCycle returned nil")
	}
	cycle.RecordDecision(TraceSiteReconcilerDrainDecision, TraceReasonNoWakeReason, TraceOutcomeDrain,
		"worker", "worker-rc-z1e", map[string]any{"session_id": "gcs-1"})
	cycle.RecordDecision(TraceSiteReconcilerDrainDecision, detectorReasonNoWakeFleetOnly, TraceOutcomeSkipped,
		"worker", "worker-rc-z1e", map[string]any{
			"session_id":     "gcs-1",
			"effect_owner":   detectorShadowEffectOwner,
			"effect_applied": false,
		})
	if err := cycle.End(TraceCompletionCompleted, map[string]any{}); err != nil {
		t.Fatalf("End: %v", err)
	}
	if err := tracer.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	records, err := ReadTraceRecords(traceCityRuntimeDir(cityDir), TraceFilter{})
	if err != nil {
		t.Fatalf("ReadTraceRecords: %v", err)
	}
	report := buildParityJoinReport(records, parityJoinOptions{Bar: 0.995})

	if report.Cycles.Considered != 1 {
		t.Fatalf("considered = %d, want 1 (%+v)", report.Cycles.Considered, report.Cycles)
	}
	if report.Cycles.WithoutDetailArms != 0 {
		t.Fatalf("without_detail_arms = %d, want 0 for a cycle the collector recorded under a live detail arm (%+v)",
			report.Cycles.WithoutDetailArms, report.Cycles)
	}
	if row := parityJoinFamilyRow(t, report, parityJoinFamilySleep); row.Joined != 1 {
		t.Fatalf("D-SLEEP joined = %d, want 1 through the real collector and store (%+v)", row.Joined, row)
	}
	if report.NoEvidence {
		t.Fatal("no_evidence = true for a corpus with a joined pair")
	}
}

// Two shapes ga-f7v2ft.158 filed as D-WAKE candidacy gaps are neither. The
// sweep's pool-under-min FILL condition is a wake for a session that does not
// exist yet, so it carries no session identity and is not a row in a per-session
// join at all; and a wake target its own admission refused as uncertifiable is a
// row the sweep deliberately left to legacy, whose negative wake arms are
// untraced. Both are refused or classified, never scored as divergences.
func TestParityJoinRefusesSessionlessConditionsAndAdmissionRefusedWakes(t *testing.T) {
	report := buildParityJoinReport(parityJoinCorpusRecords(t), parityJoinOptions{Bar: 0.995, Samples: 8})

	if got := parityJoinDispositionCount(report, TraceSiteReconcilerWakeDecision, parityJoinDispositionNoSessionKey); got != 1 {
		t.Fatalf("no_session_key refusals at wake_decision = %d, want 1 for the pool-fill condition (%+v)", got, report.Dispositions)
	}
	// Both sides: the detector-only refusal, and the joined pair where legacy
	// declined too because the start was already in flight.
	if got := parityJoinTriageCount(report, parityJoinFamilyWake, "wake_admission_refused_row_stays_legacy"); got != 2 {
		t.Fatalf("wake_admission_refused_row_stays_legacy = %d, want 2 (triage=%+v)", got, report.Triage)
	}
	if row := parityJoinFamilyRow(t, report, parityJoinFamilyWake); row.Unclassified != 0 {
		t.Fatalf("D-WAKE unclassified = %d, want 0 (%+v)", row.Unclassified, row)
	}
}

// The yield-join's agreement condition has two halves: both writers identified
// the row, AND the family legacy stood down FOR is the family that acted. A
// D-DEADLINE stand-down beside a D-ORPHAN act is two writers looking at one row
// and disagreeing about what it is — a divergence to classify, not a match. The
// campaign corpus has no such pair, so this one is written by the real collector
// and read back through the real store.
func TestParityJoinFlagsAYieldStandingDownForADifferentFamilyThanTheActor(t *testing.T) {
	cityDir := t.TempDir()
	parityJoinArmTemplate(t, cityDir, "worker")
	tracer := newSessionReconcilerTracer(cityDir, "wd15-family-mismatch", io.Discard)
	cycle := tracer.BeginCycle(TraceTickTriggerPatrol, "", time.Now().UTC(), &config.City{})
	if cycle == nil {
		t.Fatal("BeginCycle returned nil")
	}
	// Legacy's deadline seam stands down for the keyed deadline owner...
	cycle.RecordDecision(TraceSiteReconcilerIdleTimeout, "keyed_deadline_owner", TraceOutcomeSkipped,
		"worker", "worker-rc-mix", map[string]any{
			"session_id":     "rc-mix",
			"effect_owner":   detectorKeyedEffectOwner,
			"effect_applied": false,
		})
	// ...but the sweep claimed the same row for D-ORPHAN at the same site.
	cycle.RecordDecision(TraceSiteReconcilerIdleTimeout, detectorReasonOrphanLive, TraceOutcomeDrain,
		"worker", "worker-rc-mix", map[string]any{
			"session_id":      "rc-mix",
			"detector_family": string(detectorFamilyOrphan),
			"detector_acts":   true,
			"effect_owner":    detectorKeyedEffectOwner,
			"effect_applied":  false,
		})
	if err := cycle.End(TraceCompletionCompleted, map[string]any{}); err != nil {
		t.Fatalf("End: %v", err)
	}
	if err := tracer.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	records, err := ReadTraceRecords(traceCityRuntimeDir(cityDir), TraceFilter{})
	if err != nil {
		t.Fatalf("ReadTraceRecords: %v", err)
	}
	report := buildParityJoinReport(records, parityJoinOptions{Bar: 0.995, Samples: 4})

	row := parityJoinFamilyRow(t, report, parityJoinFamilyDeadline)
	if row.YieldJoined != 1 || row.Mismatched != 1 || row.Matched != 0 {
		t.Fatalf("D-DEADLINE yield_joined=%d mismatched=%d matched=%d, want 1/1/0 (%+v)",
			row.YieldJoined, row.Mismatched, row.Matched, row)
	}
	if got := parityJoinTriageCount(report, parityJoinFamilyDeadline, parityJoinClassYieldFamilyMismatch); got != 1 {
		t.Fatalf("%s = %d, want 1 (triage=%+v)", parityJoinClassYieldFamilyMismatch, got, report.Triage)
	}
}

// A candidacy-bearing stand-down with nothing beside it is a real divergence:
// legacy reached the family's arm, judged the row actionable, stepped aside —
// and nothing acted that tick. It must surface as an unclassified WE blocker
// with its evidence, never be absorbed by the ownership-arm refusal.
func TestParityJoinReportsACandidacyYieldWithNoActorAsAWEBlocker(t *testing.T) {
	cityDir := t.TempDir()
	parityJoinArmTemplate(t, cityDir, "worker")
	tracer := newSessionReconcilerTracer(cityDir, "wd15-orphan-yield", io.Discard)
	cycle := tracer.BeginCycle(TraceTickTriggerPatrol, "", time.Now().UTC(), &config.City{})
	if cycle == nil {
		t.Fatal("BeginCycle returned nil")
	}
	cycle.RecordDecision(TraceSiteReconcilerOrphaned, "keyed_orphan_drain_owner", TraceOutcomeSkipped,
		"worker", "worker-rc-lone", map[string]any{
			"session_id":     "rc-lone",
			"effect_owner":   detectorKeyedEffectOwner,
			"effect_applied": false,
		})
	if err := cycle.End(TraceCompletionCompleted, map[string]any{}); err != nil {
		t.Fatalf("End: %v", err)
	}
	if err := tracer.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	records, err := ReadTraceRecords(traceCityRuntimeDir(cityDir), TraceFilter{})
	if err != nil {
		t.Fatalf("ReadTraceRecords: %v", err)
	}
	report := buildParityJoinReport(records, parityJoinOptions{Bar: 0.995, Samples: 4})

	row := parityJoinFamilyRow(t, report, parityJoinFamilyOrphan)
	if row.YieldOnly != 1 || row.Unclassified != 1 {
		t.Fatalf("D-ORPHAN yield_only=%d unclassified=%d, want 1/1 (%+v)", row.YieldOnly, row.Unclassified, row)
	}
	if !report.WEBlocker {
		t.Fatal("we_blocker = false for a candidacy stand-down nothing acted on")
	}
	if report.Cycles.UnpairedOwnershipYields != 0 {
		t.Fatalf("unpaired_ownership_yields = %d, want 0: a candidacy arm must not be refused (%+v)",
			report.Cycles.UnpairedOwnershipYields, report.Cycles)
	}
	if len(report.Unclassified) != 1 || report.Unclassified[0].LegacyReason != "keyed_orphan_drain_owner" {
		t.Fatalf("unclassified sample does not carry the stand-down's evidence: %+v", report.Unclassified)
	}
}

// B3. no_evidence was owner-presence-based (owned == 0), so a corpus carrying
// keyed and detector-shadow records but no legacy ones — precisely the corpus
// this campaign produced before B1 — reported no_evidence=false beside joined=0
// in every family. The flag is the campaign's guard against reading an unarmed
// or unjoinable window as a parity result, so it must be join-based.
func TestParityJoinNoEvidenceIsJoinBased(t *testing.T) {
	cityDir := t.TempDir()
	tracer := newSessionReconcilerTracer(cityDir, "wd15-no-evidence", io.Discard)
	cycle := tracer.BeginCycle(TraceTickTriggerPatrol, "", time.Now().UTC(), &config.City{})
	if cycle == nil {
		t.Fatal("BeginCycle returned nil")
	}
	cycle.RecordDecision(TraceSiteReconcilerIdleTimeout, TraceReasonIdleTimeout, TraceOutcomeStop,
		"worker", "worker-rc-gv8", map[string]any{"effect_owner": detectorKeyedEffectOwner, "effect_applied": false})
	cycle.RecordDecision(TraceSiteReconcilerPendingCreatePreserved, TraceReasonPreserve, TraceOutcomeNoChange,
		"worker", "worker-rc-7iv", map[string]any{"effect_owner": detectorShadowEffectOwner, "effect_applied": false})
	if err := cycle.End(TraceCompletionCompleted, map[string]any{}); err != nil {
		t.Fatalf("End: %v", err)
	}
	if err := tracer.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	records, err := ReadTraceRecords(traceCityRuntimeDir(cityDir), TraceFilter{})
	if err != nil {
		t.Fatalf("ReadTraceRecords: %v", err)
	}
	report := buildParityJoinReport(records, parityJoinOptions{Bar: 0.995})

	total := 0
	for _, row := range report.Families {
		total += row.Joined
	}
	if total != 0 {
		t.Fatalf("fixture joined %d rows; it must join none for this test to mean anything", total)
	}
	if !report.NoEvidence {
		t.Fatal("no_evidence = false for a corpus with owned records but zero joined rows")
	}
	if report.BarMet {
		t.Fatal("bar_met = true with no joined evidence")
	}
}
