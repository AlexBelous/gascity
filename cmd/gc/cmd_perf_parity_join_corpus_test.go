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

func parityJoinAbsenceCount(report parityJoinReport, site TraceSiteCode, disposition string) int {
	total := 0
	for _, entry := range report.OwnerAbsence {
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

	if report.Cycles.LegacyByElimination != 3 {
		t.Fatalf("legacy_by_elimination = %d, want 3 (drain/no-wake-reason, wake_decision/wake, drain_ack/acknowledged): %+v",
			report.Cycles.LegacyByElimination, report.Cycles)
	}

	// The one cycle in the corpus where legacy and the sweep both wrote for the
	// same session at the same site joins, and lands in a real section 3b class.
	sleep := parityJoinFamilyRow(t, report, parityJoinFamilySleep)
	if sleep.Joined != 1 {
		t.Fatalf("D-SLEEP joined = %d, want 1 (%+v)", sleep.Joined, sleep)
	}
	if sleep.Incomparable != 1 || sleep.Unclassified != 0 {
		t.Fatalf("D-SLEEP incomparable=%d unclassified=%d, want 1/0 (%+v)", sleep.Incomparable, sleep.Unclassified, sleep)
	}
	if got := parityJoinTriageCount(report, parityJoinFamilySleep, "fleet_only_no_wake_left_to_legacy"); got != 1 {
		t.Fatalf("fleet-only triage count = %d, want 1 (triage=%+v)", got, report.Triage)
	}

	// The legacy singletons show up as legacy-only, not as unowned records.
	if drain := parityJoinFamilyRow(t, report, parityJoinFamilyDrain); drain.LegacyOnly != 1 {
		t.Fatalf("D-DRAIN legacy_only = %d, want 1 (%+v)", drain.LegacyOnly, drain)
	}
	if got := parityJoinTriageCount(report, parityJoinFamilyDrain, "ack_timing_skew"); got != 1 {
		t.Fatalf("D-DRAIN ack_timing_skew = %d, want 1 (triage=%+v)", got, report.Triage)
	}
	if wake := parityJoinFamilyRow(t, report, parityJoinFamilyWake); wake.LegacyOnly != 1 {
		t.Fatalf("D-WAKE legacy_only = %d, want 1 (%+v)", wake.LegacyOnly, wake)
	}
}

// B1's guard. Absence classifies as legacy only inside the section 1 legacy
// vocabulary. A phase site's per-cycle marker, a coexistence-seam yield whose
// effect belongs to the keyed population, and a keyed-owned site are each
// counted and surfaced with the reason they were refused — never binned as
// legacy, which would have manufactured phantom legacy-only rows in exactly the
// families (D-DUP, D-STRANDED) whose only site is a phase site.
func TestParityJoinRefusesToAttributeAbsenceOutsideTheLegacyVocabulary(t *testing.T) {
	report := buildParityJoinReport(parityJoinCorpusRecords(t), parityJoinOptions{Bar: 0.995, Samples: 8})

	for _, family := range []string{parityJoinFamilyDup, parityJoinFamilyStranded, parityJoinFamilyStart} {
		row := parityJoinFamilyRow(t, report, family)
		if row.LegacyOnly != 0 || row.Joined != 0 {
			t.Fatalf("family %q took a phantom legacy row: legacy_only=%d joined=%d (%+v)", family, row.LegacyOnly, row.Joined, row)
		}
	}

	for _, want := range []struct {
		site        TraceSiteCode
		disposition string
	}{
		{TraceSiteSessionReconcileHealRetire, parityJoinAbsencePhaseMarker},
		{TraceSiteSessionReconcileWakeSleep, parityJoinAbsencePhaseMarker},
		{TraceSiteReconcilerWakeDecision, parityJoinAbsenceKeyedSeamYield},
		{TraceSiteLifecycleStartCommit, parityJoinAbsenceUnattributable},
	} {
		if got := parityJoinAbsenceCount(report, want.site, want.disposition); got != 1 {
			t.Fatalf("owner_absence[%s/%s] = %d, want 1 (absence=%+v)", want.site, want.disposition, got, report.OwnerAbsence)
		}
	}

	// Every refused record is still counted, so the readout never loses one.
	if report.Cycles.UnownedRecords != 4 {
		t.Fatalf("unowned_records = %d, want 4 (2 phase markers, 1 seam yield, 1 keyed-owned site): %+v",
			report.Cycles.UnownedRecords, report.Cycles)
	}

	var out strings.Builder
	if err := writeParityJoinReport(&out, report); err != nil {
		t.Fatalf("writeParityJoinReport: %v", err)
	}
	for _, want := range []string{"OWNER-ABSENT", parityJoinAbsencePhaseMarker, parityJoinAbsenceKeyedSeamYield} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("human readout hides the owner-absence taxonomy (missing %q):\n%s", want, out.String())
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
	if rollups != 7 {
		t.Fatalf("fixture carries %d cycle rollups, want 7", rollups)
	}

	report := buildParityJoinReport(records, parityJoinOptions{Bar: 0.995})

	if report.Cycles.Considered != 7 {
		t.Fatalf("considered = %d, want 7 (%+v)", report.Cycles.Considered, report.Cycles)
	}
	if report.Cycles.WithoutDetailArms != 1 {
		t.Fatalf("without_detail_arms = %d, want 1 — six fixture rollups carry fields.detailed_template_count>0 (%+v)",
			report.Cycles.WithoutDetailArms, report.Cycles)
	}
}

// B2, end to end through the real collector and the real store: the mis-arming
// alarm must go quiet for a cycle that actually ran armed. A synthesized rollup
// cannot prove this — only a record the collector wrote and the store read back
// carries the counter where production puts it, as the JSON number it becomes.
func TestParityJoinArmingAlarmIsQuietForACollectorWrittenArmedCycle(t *testing.T) {
	cityDir := t.TempDir()
	arms := newSessionReconcilerTraceArmStore(cityDir)
	now := time.Now().UTC()
	if _, err := arms.upsertArm(TraceArm{
		ScopeType:  TraceArmScopeTemplate,
		ScopeValue: "worker",
		Source:     TraceArmSourceManual,
		Level:      TraceModeDetail,
		ArmedAt:    now,
		ExpiresAt:  now.Add(30 * time.Minute),
		UpdatedAt:  now,
	}); err != nil {
		t.Fatalf("upsertArm: %v", err)
	}

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
