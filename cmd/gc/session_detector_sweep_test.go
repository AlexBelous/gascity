package main

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/clock"
	"github.com/gastownhall/gascity/internal/config"
	"github.com/gastownhall/gascity/internal/events"
	"github.com/gastownhall/gascity/internal/runtime"
	sessionpkg "github.com/gastownhall/gascity/internal/session"
)

// detectorSweepProviderReadMethods is the closed set of provider calls the
// sweep may make (DETECTOR.md §2 cost model): one ListRunning per sweep, the
// two-bit liveness probe over bead-awake rows, and GetLastActivity only for
// rows whose deadline or stall timer is configured. Everything else — Peek,
// GetMeta (the drain-ack read), IsAttached, Stop, Start, Nudge — is either
// handler-side by design or a mutation.
var detectorSweepProviderReadMethods = map[string]bool{
	"ListRunning":     true,
	"IsRunning":       true,
	"ProcessAlive":    true,
	"GetLastActivity": true,
}

// TestDetectorShadowVocabularyNeverAutoArms is the non-perturbation invariant:
// no detector-shadow (reason, outcome) pair may satisfy shouldAutoArmForTrace,
// on either its reason leg or its outcome leg. If one could, a shadow record
// would write arms.json and consume one of the four auto-arm slots.
func TestDetectorShadowVocabularyNeverAutoArms(t *testing.T) {
	for _, reason := range detectorShadowReasons {
		if !strings.HasPrefix(string(reason), "detector_") {
			t.Errorf("reason %q must carry the detector_ prefix", reason)
		}
		for _, outcome := range detectorShadowOutcomes {
			if shouldAutoArmForTrace(reason, outcome) {
				t.Errorf("shouldAutoArmForTrace(%q, %q) = true, want false", reason, outcome)
			}
		}
	}
	for _, banned := range []TraceOutcomeCode{TraceOutcomeFailed, TraceOutcomeProviderError, TraceOutcomeDeadlineExceeded} {
		for _, outcome := range detectorShadowOutcomes {
			if outcome == banned {
				t.Errorf("detector shadow outcome vocabulary must not contain %q (auto-arm outcome leg)", banned)
			}
		}
	}
}

// TestDetectorFamiliesStayShadowOnlyDuringWD pins the act frontier: exactly the
// families whose keyed handler AND legacy yield have landed may act, and every
// other family stays shadow-only. D-DEADLINE crossed at WD.2; D-ORPHAN's CLOSE
// arm crossed at WD.3 and its live-orphan DRAIN arm at WD.4; D-STALE-CREATE at
// WD.7; D-DUP at WD.13; D-STALL at WD.12. A family that flips an act constant without an arm in
// detectorAdmissionSourceFor — or with an arm but no landed handler — fails here
// before it can double-act beside a non-yielding legacy.
func TestDetectorFamiliesStayShadowOnlyDuringWD(t *testing.T) {
	// The outcomes each acting family's EFFECT arms carry, one entry per landed
	// handler+yield pair. A family absent from this table must not route under
	// any outcome, and an outcome absent from a listed family's set must not
	// route either. D-DUP raises a single arm whose condition carries
	// TraceOutcomeNoChange (the retire is the predicted EFFECT, not the
	// recorded outcome), so that is the outcome its effect arm routes under.
	acting := map[detectorFamily]map[TraceOutcomeCode]bool{
		detectorFamilyDeadline:    {TraceOutcomeStop: true},
		detectorFamilyOrphan:      {TraceOutcomeClosed: true, TraceOutcomeDrain: true},
		detectorFamilyStaleCreate: {TraceOutcomeRollback: true},
		detectorFamilyDup:         {TraceOutcomeNoChange: true},
		detectorFamilyStall:       {TraceOutcomeStop: true},
	}
	if !detectorAnyFamilyActs() {
		t.Fatal("detectorAnyFamilyActs() = false; D-DEADLINE acts from WD.2 onward")
	}
	for _, spec := range detectorFamilySpecs {
		effects, acts := acting[spec.Family]
		if spec.Acts != acts || detectorFamilyActs(spec.Family) != acts {
			t.Errorf("family %q acts=%v, want %v", spec.Family, spec.Acts, acts)
		}
		if !acts {
			for _, outcome := range detectorShadowOutcomes {
				if _, routed := detectorAdmissionSourceFor(detectorCondition{Family: spec.Family, Outcome: outcome}); routed {
					t.Errorf("shadow-only family %q routed outcome %q", spec.Family, outcome)
				}
			}
			continue
		}
		sources := make(map[sessionStartAdmissionSource]TraceOutcomeCode, len(effects))
		for effect := range effects {
			source, routed := detectorAdmissionSourceFor(detectorCondition{Family: spec.Family, Outcome: effect})
			if !routed {
				t.Errorf("family %q did not route its effect arm (outcome %q)", spec.Family, effect)
			}
			if source == "" {
				t.Errorf("family %q routes outcome %q with an empty admission source", spec.Family, effect)
			}
			if err := validateSessionStartAdmission("ga-detector", source); err != nil {
				t.Errorf("family %q admission source %q is not accepted by the controller: %v", spec.Family, source, err)
			}
			// One arm, one source: two effect arms sharing a source would make
			// each arm's legacy yield stand down for the other arm's rows.
			if prior, seen := sources[source]; seen {
				t.Errorf("family %q routes outcomes %q and %q under the same source %q", spec.Family, prior, effect, source)
			}
			sources[source] = effect
		}
		for _, outcome := range detectorShadowOutcomes {
			if effects[outcome] {
				continue
			}
			if _, routed := detectorAdmissionSourceFor(detectorCondition{Family: spec.Family, Outcome: outcome}); routed {
				t.Errorf("family %q routed non-effect outcome %q; only its effect arms may enqueue", spec.Family, outcome)
			}
		}
	}
	if _, routed := detectorAdmissionSourceFor(detectorCondition{Family: detectorFamilyStaleCreate, Outcome: TraceOutcomeNoChange}); routed {
		t.Error("D-STALE-CREATE routed its preserved arm; only its rollback arm may enqueue")
	}
	// D-DUP raises exactly one arm — one condition per loser row — so it routes
	// on the family alone. Pin that so a future second arm cannot ride in
	// unnoticed on the family gate.
	if _, routed := detectorAdmissionSourceFor(detectorCondition{Family: detectorFamilyDup, Outcome: TraceOutcomeNoChange}); !routed {
		t.Error("D-DUP must route its only arm; every condition it raises predicts the retire of its own key")
	}
	// The D2 stop-capability screen guards the token-bound unattended stop.
	// D-DUP's handler reuses the retire path's own IsRunning → kill → IsRunning
	// stop instead, so screening it would strand duplicates on providers where
	// legacy retires them today.
	for _, family := range []detectorFamily{detectorFamilyDeadline, detectorFamilyOrphan, detectorFamilySleep} {
		if !detectorFamilyRequiresStopCapability(family) {
			t.Errorf("family %q must be screened on the D2 stop capability", family)
		}
	}
	if detectorFamilyRequiresStopCapability(detectorFamilyDup) {
		t.Error("D-DUP must be exempt from the D2 stop-capability screen; its handler uses the retire path's own stop")
	}
	if !detectorFamilyDestructive(detectorFamilyDup) {
		t.Error("D-DUP stays destructive for the partial-store guard even though it is D2-exempt")
	}
	for _, family := range []detectorFamily{
		detectorFamilyDeadline, detectorFamilyOrphan, detectorFamilyStaleCreate,
		detectorFamilyDrift, detectorFamilySleep, detectorFamilyDrain,
		detectorFamilyStall, detectorFamilyDup, detectorFamilyStranded,
	} {
		if !detectorFamilyDestructive(family) {
			t.Errorf("family %q must be classified destructive (close/stop/drain/rollback/retire)", family)
		}
	}
	for _, family := range []detectorFamily{detectorFamilyWake, detectorFamilyZombie} {
		if detectorFamilyDestructive(family) {
			t.Errorf("family %q must not be classified destructive; it neither closes, stops, drains, rolls back, nor retires", family)
		}
	}
}

// TestDetectSessionConditionsEmitsShadowRecordsAtLegacySites is the focused
// GREEN: over a seeded snapshot the sweep classifies rows into families, emits
// detector-shadow records carrying the LEGACY site codes with
// effect_applied=false and effect_owner=detector-shadow, and stays inside its
// provider read budget with zero provider mutations.
func TestDetectSessionConditionsEmitsShadowRecordsAtLegacySites(t *testing.T) {
	cityPath := t.TempDir()
	now := time.Date(2026, 8, 8, 12, 0, 0, 0, time.UTC)
	sp := runtime.NewFake()

	cfg := &config.City{
		Workspace: config.Workspace{Name: "test-city"},
		Agents:    []config.Agent{{Name: "worker", MinActiveSessions: intPtr(0), MaxActiveSessions: intPtr(4)}},
	}
	rows := []sessionpkg.ReconcileSession{
		detectorRow("sess-orphan", "worker-orphan", "active", now),
		detectorRow("sess-unknown", "worker-unknown", "teleported", now),
	}

	in := detectorSweepInput{
		CityPath: cityPath,
		CityName: "test-city",
		Cfg:      cfg,
		Provider: sp,
		Rows:     rows,
		Desired:  map[string]TemplateParams{},
		Clock:    &clock.Fake{Time: now},
		Trigger:  "patrol",
	}
	result := detectSessionConditions(context.Background(), in)

	if result.UnknownStateSkipped != 1 {
		t.Fatalf("UnknownStateSkipped = %d, want 1", result.UnknownStateSkipped)
	}
	if result.RowsEvaluated != 1 {
		t.Fatalf("RowsEvaluated = %d, want 1 (the unknown-state row is excluded from every detector)", result.RowsEvaluated)
	}
	if !result.RunningSetKnown {
		t.Fatal("RunningSetKnown = false, want true with a healthy provider")
	}

	sites := map[TraceSiteCode]int{}
	for _, cond := range result.Conditions {
		sites[cond.Site]++
		if cond.SessionID == "sess-unknown" && cond.Site != TraceSiteReconcilerUnknownState {
			t.Fatalf("unknown-state row leaked into detector %q at site %q", cond.Family, cond.Site)
		}
	}
	if sites[TraceSiteReconcilerUnknownState] != 1 {
		t.Fatalf("unknown-state records = %d, want 1", sites[TraceSiteReconcilerUnknownState])
	}
	if sites[TraceSiteReconcilerCloseOrphan] != 1 {
		t.Fatalf("close-orphan records = %d, want 1 (undesired row absent from the running set)", sites[TraceSiteReconcilerCloseOrphan])
	}

	for _, call := range sp.SnapshotCalls() {
		if !detectorSweepProviderReadMethods[call.Method] {
			t.Fatalf("sweep made a provider call outside its read budget: %s(%q)", call.Method, call.Name)
		}
	}

	records := detectorShadowRecordsForCycle(t, cityPath, cfg, in, result, "worker")
	if len(records) == 0 {
		t.Fatal("recordDetectorShadow emitted no durable records")
	}
	sawLegacySite := false
	for _, rec := range records {
		if applied, ok := rec.Fields["effect_applied"].(bool); !ok || applied {
			t.Fatalf("record at %q has effect_applied=%v, want false", rec.SiteCode, rec.Fields["effect_applied"])
		}
		if owner, _ := rec.Fields["effect_owner"].(string); owner != detectorShadowEffectOwner {
			t.Fatalf("record at %q has effect_owner=%q, want %q", rec.SiteCode, owner, detectorShadowEffectOwner)
		}
		if !strings.HasPrefix(string(rec.ReasonCode), "detector_") {
			t.Fatalf("record at %q has non-detector reason %q", rec.SiteCode, rec.ReasonCode)
		}
		if rec.SiteCode == TraceSiteReconcilerCloseOrphan {
			sawLegacySite = true
		}
	}
	if !sawLegacySite {
		t.Fatalf("no detector-shadow record landed on the legacy close-orphan site; got %s", detectorSiteSummary(records))
	}
}

// TestDetectSessionConditionsSuppressesDestructiveFamiliesOnPartialStore is the
// global-guard negative: on a partial store view every destructive family
// raises zero conditions, and suppression happens BEFORE the condition exists
// so no record can carry a Closed outcome the handler would then decline.
func TestDetectSessionConditionsSuppressesDestructiveFamiliesOnPartialStore(t *testing.T) {
	cityPath := t.TempDir()
	now := time.Date(2026, 8, 8, 12, 0, 0, 0, time.UTC)
	sp := runtime.NewFake()
	cfg := &config.City{
		Workspace: config.Workspace{Name: "test-city"},
		Agents:    []config.Agent{{Name: "worker", MinActiveSessions: intPtr(0), MaxActiveSessions: intPtr(4)}},
	}
	rows := []sessionpkg.ReconcileSession{
		detectorRow("sess-orphan", "worker-orphan", "active", now),
		detectorRow("sess-pending", "worker-pending", "start_pending", now),
	}
	rows[1].Info.PendingCreateClaim = true

	in := detectorSweepInput{
		CityPath:          cityPath,
		CityName:          "test-city",
		Cfg:               cfg,
		Provider:          sp,
		Rows:              rows,
		Desired:           map[string]TemplateParams{},
		Clock:             &clock.Fake{Time: now},
		StoreQueryPartial: true,
		Trigger:           "patrol",
	}
	result := detectSessionConditions(context.Background(), in)

	if result.SuppressedByPartialStore == 0 {
		t.Fatal("SuppressedByPartialStore = 0, want the destructive candidates suppressed before the record")
	}
	for _, cond := range result.Conditions {
		if detectorFamilyDestructive(cond.Family) {
			t.Fatalf("destructive family %q emitted a condition on a partial store view (site %q, outcome %q)",
				cond.Family, cond.Site, cond.Outcome)
		}
		switch cond.Outcome {
		case TraceOutcomeClosed, TraceOutcomeStop, TraceOutcomeDrain, TraceOutcomeRollback:
			t.Fatalf("condition at %q predicts destructive outcome %q on a partial store view", cond.Site, cond.Outcome)
		}
	}

	for _, rec := range detectorShadowRecordsForCycle(t, cityPath, cfg, in, result, "worker") {
		switch rec.SiteCode {
		case TraceSiteReconcilerCloseOrphan, TraceSiteReconcilerCloseFailedCreate,
			TraceSiteReconcilerIdleTimeout, TraceSiteReconcilerMaxSessionAge,
			TraceSiteReconcilerPendingCreate, TraceSiteReconcilerDrainDecision,
			TraceSiteReconcilerDrainAck, TraceSiteSessionReconcileHealRetire,
			TraceSiteReconcilerConfigDrift:
			t.Fatalf("destructive-family record survived the partial-store guard at site %q (reason %q)", rec.SiteCode, rec.ReasonCode)
		}
	}
}

// TestDetectSessionConditionsDoesNotFlagPoolSiblingsAsDuplicates is D-DUP's
// negative. Pool rows that have no slot stamp yet all resolve to the template's
// qualified name through canonicalSessionIdentityWithConfigInfo, so grouping on
// identity alone would read every ordinary pool sibling as a duplicate of the
// others. D-DUP keys on NAMED identity, so two unstamped siblings raise nothing.
func TestDetectSessionConditionsDoesNotFlagPoolSiblingsAsDuplicates(t *testing.T) {
	now := time.Date(2026, 8, 8, 12, 0, 0, 0, time.UTC)
	cfg := &config.City{
		Workspace: config.Workspace{Name: "test-city"},
		Agents:    []config.Agent{{Name: "worker", MinActiveSessions: intPtr(0), MaxActiveSessions: intPtr(4)}},
	}
	result := detectSessionConditions(context.Background(), detectorSweepInput{
		CityPath: t.TempDir(),
		CityName: "test-city",
		Cfg:      cfg,
		Provider: runtime.NewFake(),
		Rows: []sessionpkg.ReconcileSession{
			detectorRow("sess-pool-a", "worker-pool-a", "active", now),
			detectorRow("sess-pool-b", "worker-pool-b", "active", now),
		},
		Desired: map[string]TemplateParams{},
		Clock:   &clock.Fake{Time: now},
		Trigger: "patrol",
	})
	for _, cond := range result.Conditions {
		if cond.Family == detectorFamilyDup {
			t.Fatalf("pool sibling %q flagged as a duplicate of identity %q", cond.SessionID, cond.Identity)
		}
	}
}

// TestDetectorSweepBoundsPerFamilyRecordVolume proves the per-family record
// budget holds and that a truncated family is summarized rather than silently
// dropped, so doubled cycle volume stays inside the 4000-records-per-cycle cap.
func TestDetectorSweepBoundsPerFamilyRecordVolume(t *testing.T) {
	now := time.Date(2026, 8, 8, 12, 0, 0, 0, time.UTC)
	cfg := &config.City{
		Workspace: config.Workspace{Name: "test-city"},
		Agents:    []config.Agent{{Name: "worker", MinActiveSessions: intPtr(0), MaxActiveSessions: intPtr(4)}},
	}
	rows := make([]sessionpkg.ReconcileSession, 0, detectorFamilyRecordBudget+25)
	for i := range detectorFamilyRecordBudget + 25 {
		id := "sess-" + strconv.Itoa(i)
		rows = append(rows, detectorRow(id, "worker-"+strconv.Itoa(i), "active", now))
	}
	result := detectSessionConditions(context.Background(), detectorSweepInput{
		CityPath: t.TempDir(),
		CityName: "test-city",
		Cfg:      cfg,
		Provider: runtime.NewFake(),
		Rows:     rows,
		Desired:  map[string]TemplateParams{},
		Clock:    &clock.Fake{Time: now},
		Trigger:  "patrol",
	})
	perFamily := map[detectorFamily]int{}
	for _, cond := range result.Conditions {
		perFamily[cond.Family]++
	}
	for family, count := range perFamily {
		if count > detectorFamilyRecordBudget {
			t.Fatalf("family %q emitted %d conditions, over the per-family budget of %d", family, count, detectorFamilyRecordBudget)
		}
	}
	if result.FamilyOverflow[detectorFamilyOrphan] != 25 {
		t.Fatalf("orphan overflow = %d, want 25 summarized past the budget", result.FamilyOverflow[detectorFamilyOrphan])
	}
	summaries := detectorOverflowSummaries(result.FamilyOverflow)
	if len(summaries) == 0 {
		t.Fatal("truncated family produced no summary record")
	}
	for _, summary := range summaries {
		if summary.Reason != detectorReasonFamilyBudgetExceeded {
			t.Fatalf("summary reason = %q, want %q", summary.Reason, detectorReasonFamilyBudgetExceeded)
		}
	}
}

// TestDetectSessionConditionsSweepIsInertOnTheStore proves the sweep writes
// nothing: after a sweep over a seeded store every bead is byte-identical.
func TestDetectSessionConditionsSweepIsInertOnTheStore(t *testing.T) {
	now := time.Date(2026, 8, 8, 12, 0, 0, 0, time.UTC)
	store := beads.NewMemStore()
	bead, err := store.Create(beads.Bead{
		Title:  "worker",
		Type:   sessionBeadType,
		Status: "open",
		Labels: []string{sessionBeadLabel, "agent:worker"},
		Metadata: map[string]string{
			"session_name":       "worker-inert",
			"template":           "worker",
			"agent_name":         "worker",
			"state":              "active",
			"generation":         "1",
			"continuation_epoch": "1",
		},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	snapshot := newSessionBeadSnapshot([]beads.Bead{bead})
	cfg := &config.City{
		Workspace: config.Workspace{Name: "test-city"},
		Agents:    []config.Agent{{Name: "worker", MinActiveSessions: intPtr(0), MaxActiveSessions: intPtr(4)}},
	}
	sp := runtime.NewFake()

	before := detectorStoreFingerprint(t, store)
	detectSessionConditions(context.Background(), detectorSweepInput{
		CityPath: t.TempDir(),
		CityName: "test-city",
		Cfg:      cfg,
		Provider: sp,
		Rows:     snapshot.OpenForReconcile(),
		Snapshot: snapshot,
		Desired:  map[string]TemplateParams{},
		Clock:    &clock.Fake{Time: now},
		Trigger:  "patrol",
	})
	if after := detectorStoreFingerprint(t, store); after != before {
		t.Fatalf("sweep mutated the store:\nbefore=%s\nafter=%s", before, after)
	}
	for _, call := range sp.SnapshotCalls() {
		if !detectorSweepProviderReadMethods[call.Method] {
			t.Fatalf("sweep made a provider call outside its read budget: %s(%q)", call.Method, call.Name)
		}
	}
}

// TestDetectorSweepNeverArmsTraceDetail is the second non-perturbation
// negative: running the sweep against an UNARMED tracer must not auto-arm any
// template, must not write arms.json, and must not consume an auto-arm slot.
func TestDetectorSweepNeverArmsTraceDetail(t *testing.T) {
	cityPath := t.TempDir()
	now := time.Date(2026, 8, 8, 12, 0, 0, 0, time.UTC)
	sp := runtime.NewFake()
	cfg := &config.City{
		Workspace: config.Workspace{Name: "test-city"},
		Agents:    []config.Agent{{Name: "worker", MinActiveSessions: intPtr(0), MaxActiveSessions: intPtr(4)}},
	}
	in := detectorSweepInput{
		CityPath: cityPath,
		CityName: "test-city",
		Cfg:      cfg,
		Provider: sp,
		Rows: []sessionpkg.ReconcileSession{
			detectorRow("sess-orphan", "worker-orphan", "active", now),
		},
		Desired: map[string]TemplateParams{},
		Clock:   &clock.Fake{Time: now},
		Trigger: "patrol",
	}

	tracer := newSessionReconcilerTracer(cityPath, "test-city", io.Discard)
	if !tracer.Enabled() {
		t.Skip("session reconciler tracer is disabled in this environment")
	}
	cycle := tracer.BeginCycle(TraceTickTriggerPatrol, "detector_sweep_unarmed", now, cfg)
	if cycle == nil {
		t.Fatal("BeginCycle returned nil")
	}
	runDetectorSweep(context.Background(), cycle, in)
	if err := cycle.End(TraceCompletionCompleted, nil); err != nil {
		t.Fatalf("cycle.End: %v", err)
	}
	if err := tracer.Close(); err != nil {
		t.Fatalf("tracer.Close: %v", err)
	}

	records, err := ReadTraceRecords(traceCityRuntimeDir(cityPath), TraceFilter{TraceID: cycle.traceID})
	if err != nil {
		t.Fatalf("ReadTraceRecords: %v", err)
	}
	for _, rec := range records {
		if rec.RecordType != TraceRecordTraceControl {
			continue
		}
		if rec.Fields["action"] == "start" {
			t.Fatalf("sweep armed trace detail: %#v", rec)
		}
	}
	if _, err := tracer.armStore.load(); err != nil {
		t.Fatalf("load arm store: %v", err)
	}
	armsPath := filepath.Join(traceCityRuntimeDir(cityPath), sessionReconcilerTraceArmsFile)
	if _, err := os.Stat(armsPath); err == nil {
		t.Fatalf("sweep wrote %s; detector-shadow records must never arm detail", armsPath)
	}
}

// TestDetectorSweepRunsAtAllThreeProductionCallSites pins the wiring: the
// patrol/boot tick, the control-dispatcher tick, and the `gc start` one-shot
// each run the sweep beside their legacy reconcile call.
func TestDetectorSweepRunsAtAllThreeProductionCallSites(t *testing.T) {
	root := repoRoot(t)
	for _, site := range []struct{ file, fn string }{
		{file: "cmd/gc/city_runtime.go", fn: "func (cr *CityRuntime) beadReconcileTick("},
		{file: "cmd/gc/city_runtime.go", fn: "func (cr *CityRuntime) controlDispatcherTick("},
		{file: "cmd/gc/cmd_start.go", fn: "func doStartStandalone("},
	} {
		data, err := os.ReadFile(filepath.Join(root, site.file))
		if err != nil {
			t.Fatalf("read %s: %v", site.file, err)
		}
		body := detectorFunctionBody(string(data), site.fn)
		if body == "" {
			t.Fatalf("%s: could not locate %q", site.file, site.fn)
		}
		if !strings.Contains(body, "runDetectorSweep(") {
			t.Fatalf("%s: %s does not run the detector sweep beside its legacy reconcile call", site.file, site.fn)
		}
	}
}

// TestCityRuntimeBeadReconcileTickRunsDetectorSweepBesideLegacy is the
// behavioral half of the wiring proof for the patrol entry point: a real tick
// emits detector-shadow records onto the cycle it was handed, beside legacy.
func TestCityRuntimeBeadReconcileTickRunsDetectorSweepBesideLegacy(t *testing.T) {
	cityPath := t.TempDir()
	now := time.Date(2026, 8, 8, 12, 0, 0, 0, time.UTC)
	store := beads.NewMemStore()
	bead, err := store.Create(beads.Bead{
		Title:  "worker",
		Type:   sessionBeadType,
		Status: "open",
		Labels: []string{sessionBeadLabel, "agent:worker"},
		Metadata: map[string]string{
			"session_name":       "worker-detector-1",
			"template":           "worker",
			"agent_name":         "worker",
			"state":              "active",
			"generation":         "1",
			"continuation_epoch": "1",
		},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	cfg := &config.City{
		Workspace: config.Workspace{Name: "detector-city"},
		Agents:    []config.Agent{{Name: "worker", MinActiveSessions: intPtr(0), MaxActiveSessions: intPtr(2)}},
	}
	tracer := newSessionReconcilerTracer(cityPath, "detector-city", io.Discard)
	if !tracer.Enabled() {
		t.Skip("session reconciler tracer is disabled in this environment")
	}
	cycle := tracer.BeginCycle(TraceTickTriggerPatrol, "controller_tick", now, cfg)
	if cycle == nil {
		t.Fatal("BeginCycle returned nil")
	}

	cr := &CityRuntime{
		cityPath:            cityPath,
		cityName:            "detector-city",
		cfg:                 cfg,
		sp:                  runtime.NewFake(),
		trace:               tracer,
		standaloneCityStore: store,
		sessionDrains:       newDrainTracker(),
		rec:                 events.Discard,
		stdout:              io.Discard,
		stderr:              io.Discard,
	}
	cr.beadReconcileTick(context.Background(), DesiredStateResult{State: map[string]TemplateParams{}},
		newSessionBeadSnapshot([]beads.Bead{bead}), cycle, true)
	if err := cycle.End(TraceCompletionCompleted, nil); err != nil {
		t.Fatalf("cycle.End: %v", err)
	}
	if err := tracer.Close(); err != nil {
		t.Fatalf("tracer.Close: %v", err)
	}

	records, err := ReadTraceRecords(traceCityRuntimeDir(cityPath), TraceFilter{TraceID: cycle.traceID})
	if err != nil {
		t.Fatalf("ReadTraceRecords: %v", err)
	}
	found := false
	for _, rec := range records {
		owner, _ := rec.Fields["effect_owner"].(string)
		if owner != detectorShadowEffectOwner {
			continue
		}
		found = true
		if applied, ok := rec.Fields["effect_applied"].(bool); !ok || applied {
			t.Fatalf("tick emitted a detector record with effect_applied=%v at %q", rec.Fields["effect_applied"], rec.SiteCode)
		}
	}
	if !found {
		t.Fatal("beadReconcileTick emitted no detector-shadow records; the sweep did not run beside legacy")
	}
}

// detectorShadowRecordsForCycle arms the template (unarmed detail records are
// stashed and discarded — DETECTOR.md §3 campaign-arming rule), records the
// sweep result, and returns the durable detector-shadow records.
func detectorShadowRecordsForCycle(t *testing.T, cityPath string, cfg *config.City, in detectorSweepInput, result detectorSweepResult, templates ...string) []SessionReconcilerTraceRecord {
	t.Helper()
	tracer := newSessionReconcilerTracer(cityPath, "test-city", io.Discard)
	if !tracer.Enabled() {
		t.Skip("session reconciler tracer is disabled in this environment")
	}
	armNow := time.Now().UTC()
	for _, template := range templates {
		if _, err := tracer.armStore.upsertArm(TraceArm{
			ScopeType:      TraceArmScopeTemplate,
			ScopeValue:     template,
			Source:         TraceArmSourceManual,
			Level:          TraceModeDetail,
			ArmedAt:        armNow,
			ExpiresAt:      armNow.Add(15 * time.Minute),
			LastExtendedAt: armNow,
			UpdatedAt:      armNow,
		}); err != nil {
			t.Fatalf("upsert arm for %q: %v", template, err)
		}
	}
	cycle := tracer.BeginCycle(TraceTickTriggerPatrol, "detector_sweep", armNow, cfg)
	if cycle == nil {
		t.Fatal("BeginCycle returned nil")
	}
	cycle.syncArms(armNow, cfg)
	recordDetectorShadow(cycle, in, result)
	if err := cycle.End(TraceCompletionCompleted, nil); err != nil {
		t.Fatalf("cycle.End: %v", err)
	}
	if err := tracer.Close(); err != nil {
		t.Fatalf("tracer.Close: %v", err)
	}
	records, err := ReadTraceRecords(traceCityRuntimeDir(cityPath), TraceFilter{TraceID: cycle.traceID})
	if err != nil {
		t.Fatalf("ReadTraceRecords: %v", err)
	}
	out := make([]SessionReconcilerTraceRecord, 0, len(records))
	for _, rec := range records {
		if owner, _ := rec.Fields["effect_owner"].(string); owner == detectorShadowEffectOwner {
			out = append(out, rec)
		}
	}
	return out
}

func detectorSiteSummary(records []SessionReconcilerTraceRecord) string {
	sites := make([]string, 0, len(records))
	for _, rec := range records {
		sites = append(sites, string(rec.SiteCode)+"/"+string(rec.ReasonCode))
	}
	return strings.Join(sites, ", ")
}

func detectorStoreFingerprint(t *testing.T, store beads.Store) string {
	t.Helper()
	rows, err := store.List(beads.ListQuery{IncludeClosed: true, AllowScan: true})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	return sessionpkg.SetFingerprint(rows)
}

// detectorRow builds one reconcile row on the single configured template the
// sweep fixtures declare.
func detectorRow(id, sessionName, state string, now time.Time) sessionpkg.ReconcileSession {
	const template = "worker"
	return sessionpkg.ReconcileSession{
		Info: sessionpkg.Info{
			ID:                  id,
			Template:            template,
			SessionName:         sessionName,
			SessionNameMetadata: sessionName,
			MetadataState:       state,
			State:               sessionpkg.State(state),
			CreatedAt:           now.Add(-time.Hour),
		},
	}
}

func detectorFunctionBody(src, header string) string {
	start := strings.Index(src, header)
	if start < 0 {
		return ""
	}
	rest := src[start:]
	end := strings.Index(rest, "\n}\n")
	if end < 0 {
		return rest
	}
	return rest[:end]
}
