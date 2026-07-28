package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/config"
	"github.com/gastownhall/gascity/internal/events"
	"github.com/gastownhall/gascity/internal/rollout"
	"github.com/gastownhall/gascity/internal/rollout/gate"
	"github.com/gastownhall/gascity/internal/runtime"
	"github.com/gastownhall/gascity/internal/session"
	"github.com/gastownhall/gascity/internal/testutil"
	"github.com/gastownhall/gascity/internal/worker"
)

func TestCityRuntimeSessionStartControllerOffKeepsLegacyOwnership(t *testing.T) {
	cr := newSessionStartCityRuntimeForTest(t, rollout.Off, true)

	if err := cr.ensureSessionStartController(context.Background(), newSessionBeadSnapshotFromInfos(nil)); err != nil {
		t.Fatalf("ensureSessionStartController: %v", err)
	}
	if cr.sessionStartOwnershipState() != sessionStartOwnershipLegacy {
		t.Fatalf("ownership = %v, want legacy", cr.sessionStartOwnershipState())
	}
	if option := cr.sessionStartLegacyExclusionOption(); option != nil {
		t.Fatal("off mode installed a legacy-start exclusion")
	}
}

func TestCityRuntimeSessionStartControllerAutoFallsBackLoudly(t *testing.T) {
	cr := newSessionStartCityRuntimeForTest(t, rollout.Auto, false)
	var stderr bytes.Buffer
	cr.stderr = &stderr

	if err := cr.ensureSessionStartController(context.Background(), newSessionBeadSnapshotFromInfos(nil)); err != nil {
		t.Fatalf("auto ensureSessionStartController: %v", err)
	}
	if cr.sessionStartOwnershipState() != sessionStartOwnershipLegacy {
		t.Fatalf("ownership = %v, want loud legacy fallback", cr.sessionStartOwnershipState())
	}
	if !strings.Contains(stderr.String(), "falling back to legacy") || !strings.Contains(stderr.String(), "session store") {
		t.Fatalf("auto fallback diagnostic = %q, want legacy fallback and store reason", stderr.String())
	}
}

func TestCityRuntimeSessionStartControllerAutoRefusesAmbiguousAdmissionOwner(t *testing.T) {
	cr := newSessionStartCityRuntimeForTest(t, rollout.Auto, true)
	existingAdmission := func(string) {}
	if err := cr.cs.installSessionStartEventAdmission(existingAdmission); err != nil {
		t.Fatalf("install existing admission: %v", err)
	}
	t.Cleanup(cr.cs.stopSessionStartEventAdmission)

	err := cr.ensureSessionStartController(context.Background(), newSessionBeadSnapshotFromInfos(nil))
	if err == nil || !strings.Contains(err.Error(), "callback is already installed") {
		t.Fatalf("ensureSessionStartController error = %v, want ambiguous-admission refusal", err)
	}
	if cr.sessionStartController != nil {
		t.Fatal("ambiguous admission owner left a second keyed controller installed")
	}
	cr.cs.mu.RLock()
	gotAdmission := cr.cs.sessionStartEventAdmission
	cr.cs.mu.RUnlock()
	if gotAdmission == nil {
		t.Fatal("ambiguous admission refusal removed the existing owner's callback")
	}
}

func TestCityRuntimeSessionStartControllerRequireFailsClosed(t *testing.T) {
	cr := newSessionStartCityRuntimeForTest(t, rollout.Require, false)

	err := cr.ensureSessionStartController(context.Background(), newSessionBeadSnapshotFromInfos(nil))
	if err == nil {
		t.Fatal("require mode started without a coherent session store")
	}
	if cr.sessionStartOwnershipState() != sessionStartOwnershipRequiredBlocked {
		t.Fatalf("ownership = %v, want required-blocked", cr.sessionStartOwnershipState())
	}
	option := cr.sessionStartLegacyExclusionOption()
	if option == nil {
		t.Fatal("require failure left legacy starts enabled")
	}
	opts := startExecutionOptions{}
	option(&opts)
	info := session.Info{
		ID:          "gcs-require1",
		Type:        session.BeadType,
		Template:    "worker",
		WakeRequest: string(session.WakeCauseExplicit),
	}
	if opts.legacyStartExcluded == nil || !opts.legacyStartExcluded(info) {
		t.Fatal("require failure did not fail closed for a keyed-owned start")
	}
}

func TestCityRuntimeSessionStartControllerStartsAndCommitsSeededWake(t *testing.T) {
	env := newReconcilerTestEnv()
	openConditionalStore := func() beads.Store {
		opened, err := beads.OpenStoreAtForCity(context.Background(), beads.StoreOpenOptions{
			Provider: "file", ConditionalWrites: gate.Auto,
			OpenFileStore: func() (beads.Store, error) { return beads.NewMemStore(), nil },
		})
		if err != nil {
			t.Fatalf("open conditional-write store: %v", err)
		}
		return opened.Store
	}
	env.store = openConditionalStore()
	env.cfg = &config.City{
		Workspace: config.Workspace{Name: "test-city"},
		Agents:    []config.Agent{{Name: "worker", StartCommand: "true"}},
	}
	bead := env.createSessionBead("worker", "worker")
	if err := env.store.SetMetadataBatch(bead.ID, session.RequestExplicitWakePatch(string(session.WakeCauseExplicit), env.clk.Now())); err != nil {
		t.Fatalf("request explicit wake: %v", err)
	}
	cs := coherentSessionStartControllerStateForTest(env.cfg, env.sp, env.store, rollout.Auto)
	exactStatusCh := make(chan exactSessionLifecycleStatusResult, 2)
	cr := &CityRuntime{
		cityPath: t.TempDir(),
		cityName: "test-city",
		cfg:      env.cfg,
		sp:       env.sp,
		cs:       cs,
		rec:      events.Discard,
		stdout:   io.Discard,
		stderr:   io.Discard,
		sessionStartOptions: []startExecutionOption{
			withStartStabilityWaiter(immediateStartStabilityWaiter),
			withSessionStaleKeyDetectionWaiter(immediateSessionStaleKeyDetectionWaiter),
			withExactSessionLifecycleStatusObserver(func(result exactSessionLifecycleStatusResult) {
				exactStatusCh <- result
			}),
		},
	}
	t.Cleanup(cr.stopSessionStartController)

	seed := newSessionBeadSnapshotFromInfos([]session.Info{env.sessionInfo(bead.ID)})
	if err := cr.ensureSessionStartController(context.Background(), seed); err != nil {
		t.Fatalf("ensureSessionStartController: %v", err)
	}
	awaitCond(t, func() bool {
		return env.sessionInfo(bead.ID).MetadataState == string(session.StateActive)
	}, "seeded exact wake to commit active")
	if got := env.sp.CountCalls("Start", "worker"); got != 1 {
		t.Fatalf("provider Start calls = %d, want 1", got)
	}
	if cr.sessionStartOwnershipState() != sessionStartOwnershipKeyed {
		t.Fatalf("ownership = %v, want keyed", cr.sessionStartOwnershipState())
	}
	if cr.sessionStartLegacyExclusionOption() == nil {
		t.Fatal("keyed controller did not install legacy exclusion")
	}
	var exactStatus exactSessionLifecycleStatusResult
	select {
	case exactStatus = <-exactStatusCh:
	case <-time.After(testutil.GoroutineRaceTimeout):
		t.Fatal("exact status observer did not receive the seeded reconciliation result")
	}
	if exactStatus.ControllerGeneration != 1 || exactStatus.AdmissionVersion == 0 || exactStatus.Context != exactSessionLifecycleStatusContextDesired {
		t.Fatalf("exact status composition result = %#v, want generation-1 result", exactStatus)
	}

	controller := cr.sessionStartController
	env.store = openConditionalStore()
	swapBead := env.createSessionBead("worker-swap", "worker")
	swapPatch := session.RequestExplicitWakePatch(string(session.WakeCauseExplicit), env.clk.Now())
	swapPatch["session_key"] = "resume"
	if err := env.store.SetMetadataBatch(swapBead.ID, swapPatch); err != nil {
		t.Fatalf("request replacement-store heal: %v", err)
	}
	if err := env.sp.Start(context.Background(), "worker-swap", runtime.Config{Command: "test-cmd"}); err != nil {
		t.Fatalf("seed replacement-store runtime: %v", err)
	}
	swapInitial, err := env.store.Get(swapBead.ID)
	if err != nil {
		t.Fatalf("read replacement-store session: %v", err)
	}
	releaseSwap := cs.beginSessionStartGenerationSwap()
	cs.mu.Lock()
	cs.advanceSessionStartGenerationLocked()
	cs.cityBeadStore = env.store
	cs.sessionStartStoreGeneration = cs.sessionStartGeneration
	cs.mu.Unlock()
	releaseSwap()
	if cr.sessionStartController != controller {
		t.Fatal("store-generation swap restarted the keyed controller")
	}
	if _, err := controller.Admit(swapBead.ID, sessionStartAdmissionInProcess); err != nil {
		t.Fatalf("admit replacement-store heal: %v", err)
	}
	var swapStatus exactSessionLifecycleStatusResult
	select {
	case swapStatus = <-exactStatusCh:
	case <-time.After(testutil.GoroutineRaceTimeout):
		t.Fatal("replacement-store heal did not reconcile")
	}
	swapFinal, err := env.store.Get(swapBead.ID)
	if err != nil {
		t.Fatalf("read healed replacement-store session: %v", err)
	}
	if swapStatus.LoadedRevision != swapInitial.Revision || swapFinal.Revision != swapInitial.Revision+1 ||
		swapFinal.Metadata["state"] != string(session.StateAwake) {
		t.Fatalf("replacement-store heal revision/state = %d/%q from %d, want one fenced awake heal", swapFinal.Revision, swapFinal.Metadata["state"], swapInitial.Revision)
	}

	env.addDesired("worker-guard", "worker", true)
	guardBead := env.createSessionBead("worker-guard", "worker")
	if err := env.store.SetMetadataBatch(guardBead.ID, swapPatch); err != nil {
		t.Fatalf("request legacy-guard wake: %v", err)
	}
	guardBefore, err := env.store.Get(guardBead.ID)
	if err != nil {
		t.Fatalf("read legacy-guard session: %v", err)
	}
	env.startOptions = append(env.startOptions, cr.sessionStartLegacyExclusionOption())
	if woken := env.reconcile([]beads.Bead{guardBefore}); woken != 0 {
		t.Fatalf("legacy guard wake attempts = %d, want 0", woken)
	}
	guardAfter, err := env.store.Get(guardBead.ID)
	if err != nil {
		t.Fatalf("read guarded session: %v", err)
	}
	if guardAfter.Metadata["state"] != string(session.StateAsleep) {
		t.Fatalf("legacy desired heal was not excluded: state = %q, want asleep", guardAfter.Metadata["state"])
	}

	t.Run("concurrent update fences exact heal before effects", func(t *testing.T) {
		fenceEnv := newReconcilerTestEnv()
		fenceEnv.cfg = &config.City{Agents: []config.Agent{{Name: "worker", StartCommand: "true"}}}
		fenceBead := fenceEnv.createSessionBead("worker", "worker")
		fenceEnv.setSessionMetadata(&fenceBead, map[string]string{
			"wake_request": string(session.WakeCauseExplicit),
			"state":        string(session.StateAwake),
			"session_key":  "resume",
		})
		initial, err := fenceEnv.store.Get(fenceBead.ID)
		if err != nil {
			t.Fatalf("read initial fenced session: %v", err)
		}
		counting := newExactStatusCountingStore(t, fenceEnv.store)
		params := exactSessionStartTestParams(t, fenceEnv)
		params.Generation, params.Store, params.StatusWriter = 1, counting, counting
		params.ObserveLoadedSession = func(context.Context, string, beads.Store, runtime.Provider, *config.City, session.Info, []string) (worker.LiveObservation, error) {
			if err := fenceEnv.store.Update(fenceBead.ID, beads.UpdateOpts{Metadata: map[string]string{"state": string(session.StateQuarantined)}}); err != nil {
				t.Fatalf("commit concurrent update: %v", err)
			}
			return worker.LiveObservation{}, nil
		}
		owner, err := reconcileExactSessionStartWithOwner(context.Background(), sessionStartAdmission{
			SessionID: fenceBead.ID, Source: sessionStartAdmissionExplicitWake, Version: 1,
		}, params)
		if owner != exactSessionStartKeyedOwner || !beads.IsPreconditionFailed(err) {
			t.Fatalf("owner/error = %d/%v, want keyed/wrapped precondition", owner, err)
		}
		if counting.gets != 1 || counting.lists != 0 || counting.extraWrites != 1 || len(counting.Calls()) != 0 {
			t.Fatalf("get/list/CAS/ordinary = %d/%d/%d/%d, want 1/0/1/0", counting.gets, counting.lists, counting.extraWrites, len(counting.Calls()))
		}
		final, err := fenceEnv.store.Get(fenceBead.ID)
		if err != nil {
			t.Fatalf("read final fenced session: %v", err)
		}
		if final.Revision != initial.Revision+1 || final.Metadata["state"] != string(session.StateQuarantined) ||
			len(fenceEnv.sp.SnapshotCalls()) != 0 {
			t.Fatalf("fenced final revision/state/provider = %d/%q/%#v, want concurrent row preserved and no effects", final.Revision, final.Metadata["state"], fenceEnv.sp.SnapshotCalls())
		}
	})

	cr.stopSessionStartController()
	select {
	case extra := <-exactStatusCh:
		t.Fatalf("exact status observer received an extra result: %#v", extra)
	default:
	}
	if cr.sessionStartOwnershipState() != sessionStartOwnershipLegacy {
		t.Fatalf("ownership after stop = %v, want legacy for auto mode", cr.sessionStartOwnershipState())
	}
	cs.mu.RLock()
	admission := cs.sessionStartEventAdmission
	cs.mu.RUnlock()
	if admission != nil {
		t.Fatal("session-event admission remained installed after child stop")
	}
}

func TestCityRuntimeSessionStartEventStartsWithoutFleetTick(t *testing.T) {
	env := newReconcilerTestEnv()
	env.cfg = &config.City{
		Workspace: config.Workspace{Name: "test-city"},
		Agents:    []config.Agent{{Name: "worker", StartCommand: "true"}},
	}
	cs := coherentSessionStartControllerStateForTest(env.cfg, env.sp, env.store, rollout.Auto)
	cityPath := t.TempDir()
	trace := newSessionReconcilerTraceManager(cityPath, "test-city", io.Discard)
	t.Cleanup(func() { _ = trace.Close() })
	status := make(chan exactSessionLifecycleStatusResult, 1)
	cr := &CityRuntime{
		cityPath: cityPath,
		cityName: "test-city",
		cfg:      env.cfg,
		sp:       env.sp,
		cs:       cs,
		trace:    trace,
		rec:      events.Discard,
		stdout:   io.Discard,
		stderr:   io.Discard,
		sessionStartOptions: []startExecutionOption{
			withStartStabilityWaiter(immediateStartStabilityWaiter),
			withSessionStaleKeyDetectionWaiter(immediateSessionStaleKeyDetectionWaiter),
			withExactSessionLifecycleStatusObserver(func(result exactSessionLifecycleStatusResult) {
				status <- result
			}),
		},
	}
	t.Cleanup(cr.stopSessionStartController)
	if err := cr.ensureSessionStartController(context.Background(), newSessionBeadSnapshotFromInfos(nil)); err != nil {
		t.Fatalf("ensureSessionStartController: %v", err)
	}

	bead := env.createSessionBead("worker", "worker")
	if err := env.store.SetMetadataBatch(bead.ID, session.RequestExplicitWakePatch(string(session.WakeCauseExplicit), env.clk.Now())); err != nil {
		t.Fatalf("request explicit wake: %v", err)
	}
	bead, err := env.store.Get(bead.ID)
	if err != nil {
		t.Fatalf("get event bead: %v", err)
	}
	cs.admitSessionStartEvent(beadEventForSessionStartTest(t, events.BeadUpdated, bead))

	awaitCond(t, func() bool {
		return env.sessionInfo(bead.ID).MetadataState == string(session.StateActive)
	}, "event-admitted exact wake to commit active")
	if got := env.sp.CountCalls("Start", "worker"); got != 1 {
		t.Fatalf("provider Start calls = %d, want 1 without a fleet tick", got)
	}
	var exactStatus exactSessionLifecycleStatusResult
	select {
	case exactStatus = <-status:
	case <-time.After(testutil.GoroutineRaceTimeout):
		t.Fatal("event-admitted exact start did not report status")
	}
	if exactStatus.Plan == nil || exactStatus.Plan.Outcome != sessionLifecycleStatusNoop ||
		exactStatus.Plan.Reason != sessionLifecycleStatusReasonConverged {
		t.Fatalf("missing-runtime exact status = %#v, want converged no-op before provider start", exactStatus)
	}
	if exactStatus.RuntimeLive {
		t.Fatalf("missing-runtime exact status = %#v, want runtime_live=false", exactStatus)
	}
	records, err := ReadTraceRecords(traceCityRuntimeDir(cityPath), TraceFilter{})
	if err != nil {
		t.Fatalf("read start trace: %v", err)
	}
	for _, record := range records {
		if record.RecordType == TraceRecordOperation && record.SiteCode == TraceSiteLifecycleStatusShadow {
			t.Fatalf("start event emitted false no-effect status witness: %#v", record)
		}
	}
}

func TestCityRuntimeSessionStartEventRecordsConvergedStatusShadowWithoutEffects(t *testing.T) {
	env := newReconcilerTestEnv()
	env.cfg = &config.City{
		Workspace: config.Workspace{Name: "test-city"},
		Agents:    []config.Agent{{Name: "worker", StartCommand: "true"}},
	}
	bead := env.createSessionBead("worker", "worker")
	if err := env.store.SetMetadataBatch(bead.ID, map[string]string{
		"state":        string(session.StateAwake),
		"wake_request": string(session.WakeCauseExplicit),
	}); err != nil {
		t.Fatalf("configure active wake: %v", err)
	}
	if err := env.sp.Start(context.Background(), "worker", runtime.Config{Command: "true"}); err != nil {
		t.Fatalf("seed live runtime: %v", err)
	}
	before := exactStatusStoreState(t, env.store)
	store := newExactStatusCountingStore(t, env.store)
	cs := coherentSessionStartControllerStateForTest(env.cfg, env.sp, store, rollout.Auto)
	status := make(chan exactSessionLifecycleStatusResult, 1)
	cityPath := t.TempDir()
	trace := newSessionReconcilerTraceManager(cityPath, "test-city", io.Discard)
	t.Cleanup(func() { _ = trace.Close() })
	cr := &CityRuntime{
		cityPath: cityPath,
		cityName: "test-city",
		cfg:      env.cfg,
		sp:       env.sp,
		cs:       cs,
		trace:    trace,
		rec:      events.Discard,
		stdout:   io.Discard,
		stderr:   io.Discard,
		sessionStartOptions: []startExecutionOption{
			withStartStabilityWaiter(immediateStartStabilityWaiter),
			withSessionStaleKeyDetectionWaiter(immediateSessionStaleKeyDetectionWaiter),
			withExactSessionLifecycleStatusObserver(func(result exactSessionLifecycleStatusResult) {
				status <- result
			}),
		},
	}
	t.Cleanup(cr.stopSessionStartController)
	if err := cr.ensureSessionStartController(context.Background(), newSessionBeadSnapshotFromInfos(nil)); err != nil {
		t.Fatalf("ensureSessionStartController: %v", err)
	}
	beforeCalls := len(env.sp.SnapshotCalls())
	eventBead, err := env.store.Get(bead.ID)
	if err != nil {
		t.Fatalf("read post-commit event bead: %v", err)
	}
	cs.admitSessionStartEvent(beadEventForSessionStartTest(t, events.BeadUpdated, eventBead))

	var got exactSessionLifecycleStatusResult
	select {
	case got = <-status:
	case <-time.After(testutil.GoroutineRaceTimeout):
		t.Fatal("event-admitted converged status did not report")
	}
	if got.Admission.Source != sessionStartAdmissionInProcess || got.AdmissionVersion == 0 || got.ControllerGeneration != 1 ||
		!got.RuntimeLive || got.Disposition != exactSessionLifecycleStatusDispositionCandidate || got.Plan == nil ||
		got.Plan.Outcome != sessionLifecycleStatusNoop || got.Plan.Reason != sessionLifecycleStatusReasonConverged {
		t.Fatalf("status result = %#v, want event-admitted converged no-op candidate", got)
	}
	if store.lists != 0 {
		t.Fatalf("store List calls = %d, want 0", store.lists)
	}
	requireExactStatusStoreUnchanged(t, before, store)
	readOnlyProviderCalls := map[string]bool{
		"GetLastActivity": true,
		"IsAttached":      true,
		"IsRunning":       true,
	}
	for _, call := range env.sp.SnapshotCalls()[beforeCalls:] {
		if !readOnlyProviderCalls[call.Method] {
			t.Fatalf("provider call after event = %#v, want only read-only observation", call)
		}
	}

	records, err := ReadTraceRecords(traceCityRuntimeDir(cityPath), TraceFilter{})
	if err != nil {
		t.Fatalf("read detached shadow trace: %v", err)
	}
	var witnesses []SessionReconcilerTraceRecord
	for _, record := range records {
		if record.RecordType == TraceRecordOperation && record.SiteCode == TraceSiteLifecycleStatusShadow {
			witnesses = append(witnesses, record)
		}
	}
	if len(witnesses) != 1 {
		t.Fatalf("status-shadow witnesses = %#v, want one", witnesses)
	}
	witness := witnesses[0]
	if witness.OutcomeCode != TraceOutcomeNoChange || witness.Fields["session_id"] != bead.ID ||
		witness.Fields["admission"] != string(sessionStartAdmissionInProcess) ||
		witness.Fields["admission_version"] != float64(got.AdmissionVersion) ||
		witness.Fields["generation"] != float64(got.ControllerGeneration) ||
		witness.Fields["status_outcome"] != "noop" ||
		witness.Fields["status_reason"] != string(sessionLifecycleStatusReasonConverged) ||
		witness.Fields["effect_applied"] != false {
		t.Fatalf("status-shadow witness = %#v, want converged detached event witness", witness)
	}
}

func TestRecordExactSessionLifecycleStatusShadowUsesAdmissionToObservationLatency(t *testing.T) {
	cityPath := t.TempDir()
	trace := newSessionReconcilerTraceManager(cityPath, "test-city", io.Discard)
	t.Cleanup(func() { _ = trace.Close() })
	cr := &CityRuntime{
		cityPath: cityPath,
		cityName: "test-city",
		trace:    trace,
		stderr:   io.Discard,
	}
	admittedAt := time.Now().UTC().Add(-time.Second)
	observedAt := admittedAt.Add(137 * time.Millisecond)
	result := exactSessionLifecycleStatusResult{
		Admission: sessionStartAdmission{
			SessionID:  "gcs-latency",
			Source:     sessionStartAdmissionInProcess,
			Version:    3,
			AdmittedAt: admittedAt,
		},
		AdmissionVersion:     3,
		ControllerGeneration: 7,
		RequestedID:          "gcs-latency",
		LoadedID:             "gcs-latency",
		Context:              exactSessionLifecycleStatusContextDesired,
		ObservedAt:           observedAt,
		RuntimeLive:          true,
		Disposition:          exactSessionLifecycleStatusDispositionCandidate,
		Reason:               exactSessionLifecycleStatusReasonCandidate,
		Plan: &sessionLifecycleStatusPlan{
			SessionID: "gcs-latency",
			Outcome:   sessionLifecycleStatusNoop,
			Reason:    sessionLifecycleStatusReasonConverged,
		},
	}
	cr.recordExactSessionLifecycleStatusShadow(&config.City{}, result)

	records, err := ReadTraceRecords(traceCityRuntimeDir(cityPath), TraceFilter{})
	if err != nil {
		t.Fatalf("read status latency trace: %v", err)
	}
	var cycleStart, witness *SessionReconcilerTraceRecord
	for i := range records {
		switch {
		case records[i].RecordType == TraceRecordCycleStart:
			cycleStart = &records[i]
		case records[i].RecordType == TraceRecordOperation && records[i].SiteCode == TraceSiteLifecycleStatusShadow:
			witness = &records[i]
		}
	}
	if cycleStart == nil || witness == nil {
		t.Fatalf("latency trace records = %#v, want cycle start and status witness", records)
	}
	if !cycleStart.Ts.Equal(admittedAt) {
		t.Fatalf("status-shadow cycle start = %s, want admission %s", cycleStart.Ts, admittedAt)
	}
	if witness.DurationMS != 137 {
		t.Fatalf("status-shadow duration_ms = %d, want admission-to-observation 137", witness.DurationMS)
	}

	for _, timing := range []struct {
		admittedAt time.Time
		observedAt time.Time
	}{
		{observedAt: observedAt},
		{admittedAt: admittedAt},
		{admittedAt: observedAt, observedAt: admittedAt},
	} {
		result.Admission.AdmittedAt = timing.admittedAt
		result.ObservedAt = timing.observedAt
		cr.recordExactSessionLifecycleStatusShadow(&config.City{}, result)
	}
	records, err = ReadTraceRecords(traceCityRuntimeDir(cityPath), TraceFilter{})
	if err != nil {
		t.Fatalf("read status trace after invalid timing: %v", err)
	}
	witnesses := 0
	for _, record := range records {
		if record.RecordType == TraceRecordOperation && record.SiteCode == TraceSiteLifecycleStatusShadow {
			witnesses++
		}
	}
	if witnesses != 1 {
		t.Fatalf("status-shadow witnesses after invalid timing = %d, want original valid witness only", witnesses)
	}
}

func TestCityRuntimeSessionStartEventOverflowRequestsLegacyFallback(t *testing.T) {
	env := newReconcilerTestEnv()
	env.cfg = &config.City{Agents: []config.Agent{{Name: "worker", StartCommand: "true"}}}
	cs := coherentSessionStartControllerStateForTest(env.cfg, env.sp, env.store, rollout.Auto)
	pokeCh := make(chan struct{}, 1)
	firstEntered := make(chan struct{})
	releaseFirst := make(chan struct{})
	firstID := ""
	original := newCitySessionStartController
	newCitySessionStartController = func(opts sessionStartControllerOptions) (*sessionStartController, error) {
		opts.MaxDistinct = 1
		opts.Reconcile = func(_ context.Context, admission sessionStartAdmission) error {
			if admission.SessionID == firstID {
				close(firstEntered)
				<-releaseFirst
			}
			return nil
		}
		return original(opts)
	}
	t.Cleanup(func() { newCitySessionStartController = original })
	cr := &CityRuntime{
		cfg:    env.cfg,
		sp:     env.sp,
		cs:     cs,
		pokeCh: pokeCh,
		stdout: io.Discard,
		stderr: io.Discard,
	}
	t.Cleanup(cr.stopSessionStartController)
	t.Cleanup(func() { close(releaseFirst) })
	if err := cr.ensureSessionStartController(context.Background(), newSessionBeadSnapshotFromInfos(nil)); err != nil {
		t.Fatalf("ensureSessionStartController: %v", err)
	}
	first := env.createSessionBead("gcs-event-overflow-first", "worker")
	second := env.createSessionBead("gcs-event-overflow-second", "worker")
	firstID = first.ID
	cs.admitSessionStartEvent(beadEventForSessionStartTest(t, events.BeadUpdated, first))
	awaitClose(t, firstEntered, "first event reconciliation")
	cs.admitSessionStartEvent(beadEventForSessionStartTest(t, events.BeadUpdated, second))

	select {
	case <-pokeCh:
	case <-time.After(testutil.GoroutineRaceTimeout):
		t.Fatal("overflowed session event did not request immediate legacy fallback")
	}
	if !cr.sessionStartController.TakeAuditRequest() {
		t.Fatal("overflowed session event did not preserve the authoritative audit request")
	}
}

func TestCityRuntimeSessionStartEventOverflowDoesNotBlockOnFullFallback(t *testing.T) {
	env := newReconcilerTestEnv()
	env.cfg = &config.City{Agents: []config.Agent{{Name: "worker", StartCommand: "true"}}}
	cs := coherentSessionStartControllerStateForTest(env.cfg, env.sp, env.store, rollout.Auto)
	pokeCh := make(chan struct{}, 1)
	pokeCh <- struct{}{}
	firstEntered := make(chan struct{})
	releaseFirst := make(chan struct{})
	firstID := ""
	original := newCitySessionStartController
	newCitySessionStartController = func(opts sessionStartControllerOptions) (*sessionStartController, error) {
		opts.MaxDistinct = 1
		opts.Reconcile = func(_ context.Context, admission sessionStartAdmission) error {
			if admission.SessionID == firstID {
				close(firstEntered)
				<-releaseFirst
			}
			return nil
		}
		return original(opts)
	}
	t.Cleanup(func() { newCitySessionStartController = original })
	cr := &CityRuntime{cfg: env.cfg, sp: env.sp, cs: cs, pokeCh: pokeCh, stdout: io.Discard, stderr: io.Discard}
	t.Cleanup(cr.stopSessionStartController)
	t.Cleanup(func() { close(releaseFirst) })
	if err := cr.ensureSessionStartController(context.Background(), newSessionBeadSnapshotFromInfos(nil)); err != nil {
		t.Fatalf("ensureSessionStartController: %v", err)
	}
	first := env.createSessionBead("gcs-event-full-first", "worker")
	second := env.createSessionBead("gcs-event-full-second", "worker")
	firstID = first.ID
	cs.admitSessionStartEvent(beadEventForSessionStartTest(t, events.BeadUpdated, first))
	awaitClose(t, firstEntered, "first event reconciliation")
	eventDone := make(chan struct{})
	go func() {
		cs.admitSessionStartEvent(beadEventForSessionStartTest(t, events.BeadUpdated, second))
		close(eventDone)
	}()
	awaitClose(t, eventDone, "overflow event with full fallback channel")
	if !cr.sessionStartController.TakeAuditRequest() {
		t.Fatal("full fallback channel cleared the authoritative audit request")
	}
}

func TestCityRuntimeSessionStartSeedOverflowDoesNotRequestLegacyFallback(t *testing.T) {
	cfg := &config.City{Agents: []config.Agent{{Name: "worker", StartCommand: "true"}}}
	cs := coherentSessionStartControllerStateForTest(cfg, runtime.NewFake(), beads.NewMemStore(), rollout.Auto)
	pokeCh := make(chan struct{}, 1)
	firstEntered := make(chan struct{})
	releaseFirst := make(chan struct{})
	original := newCitySessionStartController
	newCitySessionStartController = func(opts sessionStartControllerOptions) (*sessionStartController, error) {
		opts.MaxDistinct = 1
		opts.Reconcile = func(_ context.Context, admission sessionStartAdmission) error {
			if admission.SessionID == "gcs-seed-overflow-first" {
				close(firstEntered)
				<-releaseFirst
			}
			return nil
		}
		return original(opts)
	}
	t.Cleanup(func() { newCitySessionStartController = original })
	cr := &CityRuntime{cfg: cfg, sp: cs.sp, cs: cs, pokeCh: pokeCh, stdout: io.Discard, stderr: io.Discard}
	t.Cleanup(cr.stopSessionStartController)
	t.Cleanup(func() { close(releaseFirst) })
	seed := newSessionBeadSnapshotFromInfos([]session.Info{
		{ID: "gcs-seed-overflow-first", Type: session.BeadType, Template: "worker", WakeRequest: string(session.WakeCauseExplicit)},
		{ID: "gcs-seed-overflow-second", Type: session.BeadType, Template: "worker", WakeRequest: string(session.WakeCauseExplicit)},
	})
	if err := cr.ensureSessionStartController(context.Background(), seed); err != nil {
		t.Fatalf("ensureSessionStartController: %v", err)
	}
	awaitClose(t, firstEntered, "first seed reconciliation")
	select {
	case <-pokeCh:
		t.Fatal("seed-time overflow requested legacy fallback")
	default:
	}
}

func TestCityRuntimeSessionStartWorkerImmediatelyDelegatesLegacyOwnedKey(t *testing.T) {
	env := newReconcilerTestEnv()
	env.cfg = &config.City{Agents: []config.Agent{
		{Name: "database", StartCommand: "true"},
		{Name: "worker", StartCommand: "true", DependsOn: []string{"database"}},
	}}
	bead := env.createSessionBead("worker", "worker")
	if err := env.store.SetMetadataBatch(bead.ID, session.RequestExplicitWakePatch(string(session.WakeCauseExplicit), env.clk.Now())); err != nil {
		t.Fatalf("request explicit wake: %v", err)
	}
	pokeCh := make(chan struct{}, 1)
	cr := &CityRuntime{
		cityPath: t.TempDir(),
		cfg:      env.cfg,
		sp:       env.sp,
		cs:       coherentSessionStartControllerStateForTest(env.cfg, env.sp, env.store, rollout.Auto),
		pokeCh:   pokeCh,
		stdout:   io.Discard,
		stderr:   io.Discard,
	}
	t.Cleanup(cr.stopSessionStartController)
	if err := cr.ensureSessionStartController(context.Background(), newSessionBeadSnapshotFromInfos(nil)); err != nil {
		t.Fatalf("ensureSessionStartController: %v", err)
	}
	if _, err := cr.sessionStartController.Admit(bead.ID, sessionStartAdmissionInProcess); err != nil {
		t.Fatalf("admit legacy-owned key: %v", err)
	}

	select {
	case <-pokeCh:
	case <-time.After(testutil.GoroutineRaceTimeout):
		t.Fatal("legacy-owned exact key did not request an immediate fleet reconcile")
	}
}

func TestCityRuntimeSessionStartAntiEntropySeedsWithoutQueueAlarm(t *testing.T) {
	cfg := &config.City{Agents: []config.Agent{{Name: "worker", StartCommand: "true"}}}
	cs := coherentSessionStartControllerStateForTest(cfg, runtime.NewFake(), beads.NewMemStore(), rollout.Auto)
	release := make(chan struct{})
	controller := mustStartSessionStartController(t, sessionStartControllerOptions{
		Workers:     1,
		MaxDistinct: 4,
		MaxRetries:  1,
		Reconcile: func(context.Context, sessionStartAdmission) error {
			<-release
			return nil
		},
	})
	t.Cleanup(func() {
		close(release)
		controller.Stop()
	})
	cr := &CityRuntime{
		cfg:                    cfg,
		cs:                     cs,
		stdout:                 io.Discard,
		stderr:                 io.Discard,
		sessionStartController: controller,
		sessionStartOwnership:  sessionStartOwnershipKeyed,
	}
	snapshot := newSessionBeadSnapshotFromInfos([]session.Info{{
		ID:          "gcs-audit1",
		Type:        session.BeadType,
		Template:    "worker",
		WakeRequest: string(session.WakeCauseExplicit),
	}})

	cr.seedActiveSessionStartController(snapshot)

	if got := controller.Pending(); got != 1 {
		t.Fatalf("pending keys = %d, want 1 from periodic authoritative seed without a queue alarm", got)
	}
}

func TestCityRuntimeSessionStartConfigMutationKeepsOneOwner(t *testing.T) {
	stubSessionStartCityStoreOpen(t)
	tests := []struct {
		name       string
		oldDepends []string
		newDepends []string
	}{
		{name: "dependency added", newDepends: []string{"database"}},
		{name: "dependency removed", oldDepends: []string{"database"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cacheCtx, cancelCache := context.WithCancel(context.Background())
			t.Cleanup(cancelCache)
			oldCfg := &config.City{Agents: []config.Agent{{Name: "worker", StartCommand: "true", DependsOn: test.oldDepends}}}
			newCfg := &config.City{Agents: []config.Agent{{Name: "worker", StartCommand: "true", DependsOn: test.newDepends}}}
			cs := coherentSessionStartControllerStateForTest(oldCfg, runtime.NewFake(), beads.NewMemStore(), rollout.Auto)
			cs.cacheCtx = cacheCtx
			cr := &CityRuntime{
				cfg:                   oldCfg,
				cs:                    cs,
				sessionStartMode:      rollout.Auto,
				sessionStartOwnership: sessionStartOwnershipKeyed,
				stdout:                io.Discard,
				stderr:                io.Discard,
			}
			info := session.Info{
				ID:          "gcs-config-transition1",
				Type:        session.BeadType,
				Template:    "worker",
				WakeRequest: string(session.WakeCauseExplicit),
			}

			assertSingleSessionStartOwner(t, cr, info, oldCfg)
			cs.updateWithPendingConfigMutation(newCfg, cs.sp, "next-revision")

			if _, _, err := cs.acquireSessionStartSnapshot(); err == nil {
				t.Fatal("keyed owner remained available while runtime config application was pending")
			}
			if option := cr.sessionStartLegacyExclusionOption(); option != nil {
				t.Fatal("auto mode did not temporarily return pending config ownership to legacy")
			}

			cr.cfg = newCfg
			cs.clearConfigMutationPending()
			assertSingleSessionStartOwner(t, cr, info, newCfg)
		})
	}
}

func TestCityRuntimeSessionStartRequireBlocksBothOwnersDuringConfigMutation(t *testing.T) {
	cfg := &config.City{Agents: []config.Agent{{Name: "worker", StartCommand: "true"}}}
	cs := coherentSessionStartControllerStateForTest(cfg, runtime.NewFake(), beads.NewMemStore(), rollout.Require)
	cs.markConfigMutationPending("next-revision")
	cr := &CityRuntime{
		cfg:                   cfg,
		cs:                    cs,
		sessionStartMode:      rollout.Require,
		sessionStartOwnership: sessionStartOwnershipKeyed,
	}
	info := session.Info{
		ID:          "gcs-required-config1",
		Type:        session.BeadType,
		Template:    "worker",
		WakeRequest: string(session.WakeCauseExplicit),
	}

	if _, _, err := cs.acquireSessionStartSnapshot(); err == nil {
		t.Fatal("required keyed owner acquired config while runtime application was pending")
	}
	option := cr.sessionStartLegacyExclusionOption()
	if option == nil || !legacySessionStartExcluded(option, info) {
		t.Fatal("require mode allowed legacy to enter while keyed config was unavailable")
	}
}

func TestCityRuntimeProviderSwapDrainsKeyedStartBeforeListingOldProvider(t *testing.T) {
	oldProvider := runtime.NewFake()
	fixture := newSessionStartProviderSwapFixture(t, oldProvider, rollout.Auto)
	cr := fixture.cr

	entered := make(chan struct{})
	release := make(chan struct{})
	controller := mustStartSessionStartController(t, sessionStartControllerOptions{
		Workers:     1,
		MaxDistinct: 4,
		MaxRetries:  0,
		Reconcile: func(context.Context, sessionStartAdmission) error {
			close(entered)
			<-release
			return nil
		},
	})
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	cr.sessionStartController = controller
	cr.sessionStartOwnership = sessionStartOwnershipKeyed
	cr.sessionStartMode = rollout.Auto
	if _, err := controller.Admit("gcs-provider-swap1", sessionStartAdmissionExplicitWake); err != nil {
		t.Fatalf("admit pending keyed start: %v", err)
	}
	awaitClose(t, entered, "pending keyed start")

	writeCityRuntimeConfig(t, fixture.tomlPath, "fail")
	lastProviderName := "fake"
	var reply reloadControlReply
	reloadDone := make(chan struct{})
	go func() {
		reply = cr.reloadConfigTraced(context.Background(), &lastProviderName, fixture.cityPath, nil, reloadSourceManual)
		close(reloadDone)
	}()
	awaitCond(t, func() bool {
		controller.mu.Lock()
		stopped := controller.stopped
		controller.mu.Unlock()
		return stopped || oldProvider.CountCalls("ListRunning", "") > 0 || channelClosed(reloadDone)
	}, "provider reload to reach keyed drain or old-provider listing")

	controller.mu.Lock()
	stopped := controller.stopped
	controller.mu.Unlock()
	if !stopped {
		t.Fatal("provider reload listed or swapped the old provider before stopping the keyed child")
	}
	if got := oldProvider.CountCalls("ListRunning", ""); got != 0 {
		t.Fatalf("old-provider ListRunning calls before keyed drain = %d, want 0", got)
	}

	close(release)
	awaitClose(t, reloadDone, "provider reload after keyed drain")
	if reply.Outcome != reloadOutcomeApplied {
		t.Fatalf("reload outcome = %q, want applied: %+v", reply.Outcome, reply)
	}
	if got := oldProvider.CountCalls("ListRunning", ""); got != 1 {
		t.Fatalf("old-provider ListRunning calls = %d, want 1 after keyed drain", got)
	}
	if cr.sessionStartController == nil || cr.sessionStartController == controller {
		t.Fatal("provider reload did not restart a fresh keyed child")
	}
	if cr.sessionStartOwnershipState() != sessionStartOwnershipKeyed {
		t.Fatalf("ownership after provider reload = %v, want keyed", cr.sessionStartOwnershipState())
	}
}

func TestCityRuntimeProviderSwapListingFailureRestoresKeyedChild(t *testing.T) {
	oldProvider := &partialListPoolProvider{
		Fake:    runtime.NewFake(),
		listErr: errors.New("old provider unavailable"),
	}
	fixture := newSessionStartProviderSwapFixture(t, oldProvider, rollout.Auto)
	if err := fixture.cr.ensureSessionStartController(context.Background(), newSessionBeadSnapshotFromInfos(nil)); err != nil {
		t.Fatalf("start old keyed child: %v", err)
	}
	oldChild := fixture.cr.sessionStartController

	writeCityRuntimeConfig(t, fixture.tomlPath, "fail")
	lastProviderName := "fake"
	reply := fixture.cr.reloadConfigTraced(context.Background(), &lastProviderName, fixture.cityPath, nil, reloadSourceManual)

	if reply.Outcome != reloadOutcomeFailed {
		t.Fatalf("reload outcome = %q, want failed: %+v", reply.Outcome, reply)
	}
	if lastProviderName != "fake" || fixture.cr.sp != oldProvider {
		t.Fatal("aborted provider swap changed the active provider")
	}
	if fixture.cr.sessionStartController == nil || fixture.cr.sessionStartController == oldChild {
		t.Fatal("aborted provider swap did not restore a fresh keyed child")
	}
	if fixture.cr.sessionStartOwnershipState() != sessionStartOwnershipKeyed {
		t.Fatalf("ownership after aborted provider swap = %v, want keyed", fixture.cr.sessionStartOwnershipState())
	}
}

func TestCityRuntimeProviderSwapRestartFailureHonorsRolloutMode(t *testing.T) {
	tests := []struct {
		name          string
		mode          rollout.Mode
		wantOwnership sessionStartOwnership
		wantText      string
	}{
		{name: "auto degrades loudly", mode: rollout.Auto, wantOwnership: sessionStartOwnershipLegacy, wantText: "falling back to legacy"},
		{name: "require remains blocked", mode: rollout.Require, wantOwnership: sessionStartOwnershipRequiredBlocked, wantText: "keyed starts remain blocked"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			oldProvider := runtime.NewFake()
			fixture := newSessionStartProviderSwapFixture(t, oldProvider, test.mode)
			var stderr bytes.Buffer
			fixture.cr.stderr = &stderr
			if err := fixture.cr.ensureSessionStartController(context.Background(), newSessionBeadSnapshotFromInfos(nil)); err != nil {
				t.Fatalf("start old keyed child: %v", err)
			}
			previousFactory := newCitySessionStartController
			newCitySessionStartController = func(sessionStartControllerOptions) (*sessionStartController, error) {
				return nil, errors.New("injected child restart failure")
			}
			t.Cleanup(func() {
				newCitySessionStartController = previousFactory
			})

			writeCityRuntimeConfig(t, fixture.tomlPath, "fail")
			lastProviderName := "fake"
			reply := fixture.cr.reloadConfigTraced(context.Background(), &lastProviderName, fixture.cityPath, nil, reloadSourceManual)

			if reply.Outcome != reloadOutcomeApplied {
				t.Fatalf("reload outcome = %q, want applied after committed provider swap: %+v", reply.Outcome, reply)
			}
			if lastProviderName != "fail" {
				t.Fatalf("last provider = %q, want fail", lastProviderName)
			}
			if fixture.cr.sessionStartOwnershipState() != test.wantOwnership {
				t.Fatalf("ownership = %v, want %v", fixture.cr.sessionStartOwnershipState(), test.wantOwnership)
			}
			combinedDiagnostics := stderr.String() + strings.Join(reply.Warnings, "\n")
			if !strings.Contains(combinedDiagnostics, test.wantText) || !strings.Contains(combinedDiagnostics, "injected child restart failure") {
				t.Fatalf("diagnostics = %q, want %q and injected failure", combinedDiagnostics, test.wantText)
			}
		})
	}
}

func TestCityRuntimeRunCoordinatesSessionStartRolloutBeforeReadiness(t *testing.T) {
	tests := []struct {
		name           string
		mode           rollout.Mode
		factoryFails   bool
		wantStarted    bool
		wantBuild      bool
		wantOwnership  sessionStartOwnership
		wantChild      bool
		wantDiagnostic string
	}{
		{
			name:          "keyed child precedes legacy startup and readiness",
			mode:          rollout.Auto,
			wantStarted:   true,
			wantBuild:     true,
			wantOwnership: sessionStartOwnershipKeyed,
			wantChild:     true,
		},
		{
			name:           "auto degradation runs legacy startup",
			mode:           rollout.Auto,
			factoryFails:   true,
			wantStarted:    true,
			wantBuild:      true,
			wantOwnership:  sessionStartOwnershipLegacy,
			wantDiagnostic: "falling back to legacy",
		},
		{
			name:           "require failure prevents readiness",
			mode:           rollout.Require,
			factoryFails:   true,
			wantOwnership:  sessionStartOwnershipRequiredBlocked,
			wantDiagnostic: "session-start controller",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			stubManagedDoltStoreOpeners(t)
			cityPath := t.TempDir()
			tomlPath := filepath.Join(cityPath, "city.toml")
			writeCityRuntimeConfig(t, tomlPath, "fake")
			cfg, err := config.Load(osFS{}, tomlPath)
			if err != nil {
				t.Fatalf("load config: %v", err)
			}
			cfg.Daemon.SessionReconciler = string(test.mode)
			provider := runtime.NewFake()
			ctx, cancel := context.WithCancel(context.Background())
			t.Cleanup(cancel)
			var stderr bytes.Buffer
			var started atomic.Bool
			var buildCalled atomic.Bool
			var buildOwnership sessionStartOwnership
			var buildHadChild bool
			var cr *CityRuntime
			cr = newTestCityRuntime(t, CityRuntimeParams{
				CityPath: cityPath,
				CityName: "test-city",
				TomlPath: tomlPath,
				Cfg:      cfg,
				SP:       provider,
				BuildFn: func(*config.City, runtime.Provider, beads.Store) DesiredStateResult {
					buildCalled.Store(true)
					buildOwnership = cr.sessionStartOwnershipState()
					cr.sessionStartMu.Lock()
					buildHadChild = cr.sessionStartController != nil
					cr.sessionStartMu.Unlock()
					return DesiredStateResult{State: map[string]TemplateParams{}}
				},
				Dops: newDrainOps(provider),
				Rec:  events.Discard,
				OnStarted: func() {
					started.Store(true)
					cancel()
				},
				Stdout: io.Discard,
				Stderr: &stderr,
			})
			cs := newControllerState(ctx, cfg, provider, events.NewFake(), "test-city", cityPath)
			cs.cityBeadStore = beads.NewMemStore()
			cr.setControllerState(cs)

			if test.factoryFails {
				previousFactory := newCitySessionStartController
				newCitySessionStartController = func(sessionStartControllerOptions) (*sessionStartController, error) {
					if test.mode == rollout.Require {
						cancel()
					}
					return nil, errors.New("injected startup child failure")
				}
				t.Cleanup(func() {
					newCitySessionStartController = previousFactory
				})
			}

			cr.run(ctx)

			if got := started.Load(); got != test.wantStarted {
				t.Fatalf("OnStarted called = %t, want %t", got, test.wantStarted)
			}
			if got := buildCalled.Load(); got != test.wantBuild {
				t.Fatalf("legacy startup build called = %t, want %t", got, test.wantBuild)
			}
			if test.wantBuild {
				if buildOwnership != test.wantOwnership || buildHadChild != test.wantChild {
					t.Fatalf("legacy startup observed ownership=%v child=%t, want ownership=%v child=%t", buildOwnership, buildHadChild, test.wantOwnership, test.wantChild)
				}
			} else if got := cr.sessionStartOwnershipState(); got != test.wantOwnership {
				t.Fatalf("ownership after refused startup = %v, want %v", got, test.wantOwnership)
			}
			if test.wantDiagnostic != "" && !strings.Contains(stderr.String(), test.wantDiagnostic) {
				t.Fatalf("stderr = %q, want %q", stderr.String(), test.wantDiagnostic)
			}
		})
	}
}

func TestCityRuntimeShutdownDrainsSessionStartBeforeSessionTeardown(t *testing.T) {
	provider := runtime.NewFake()
	store := beads.NewMemStore()
	cs := coherentSessionStartControllerStateForTest(&config.City{}, provider, store, rollout.Auto)

	admissionEntered := make(chan struct{})
	releaseAdmission := make(chan struct{})
	if err := cs.installSessionStartEventAdmission(func(string) {
		close(admissionEntered)
		<-releaseAdmission
	}); err != nil {
		t.Fatalf("install event admission: %v", err)
	}
	t.Cleanup(cs.stopSessionStartEventAdmission)
	defer func() {
		select {
		case <-releaseAdmission:
		default:
			close(releaseAdmission)
		}
	}()
	eventDone := make(chan struct{})
	evt := beadEventForSessionStartTest(t, events.BeadUpdated, beads.Bead{
		ID:   "gcs-shutdown-admission1",
		Type: session.BeadType,
	})
	go func() {
		cs.admitSessionStartEvent(evt)
		close(eventDone)
	}()
	awaitClose(t, admissionEntered, "shutdown event admission")

	workerEntered := make(chan struct{})
	releaseWorker := make(chan struct{})
	controller := mustStartSessionStartController(t, sessionStartControllerOptions{
		Workers:     1,
		MaxDistinct: 4,
		MaxRetries:  0,
		Reconcile: func(context.Context, sessionStartAdmission) error {
			close(workerEntered)
			<-releaseWorker
			return nil
		},
	})
	t.Cleanup(controller.Stop)
	defer func() {
		select {
		case <-releaseWorker:
		default:
			close(releaseWorker)
		}
	}()
	if _, err := controller.Admit("gcs-shutdown-worker1", sessionStartAdmissionExplicitWake); err != nil {
		t.Fatalf("admit keyed shutdown work: %v", err)
	}
	awaitClose(t, workerEntered, "shutdown keyed worker")

	cr := &CityRuntime{
		cfg:                    &config.City{},
		sp:                     provider,
		cs:                     cs,
		rec:                    events.Discard,
		stdout:                 io.Discard,
		stderr:                 io.Discard,
		sessionStartController: controller,
		sessionStartOwnership:  sessionStartOwnershipKeyed,
		sessionStartMode:       rollout.Auto,
	}
	shutdownDone := make(chan struct{})
	go func() {
		cr.shutdown()
		close(shutdownDone)
	}()
	awaitCond(t, func() bool {
		cs.mu.RLock()
		stopping := cs.sessionStartEventAdmissionStopping
		cs.mu.RUnlock()
		return stopping
	}, "shutdown to begin event-admission drain")

	controller.mu.Lock()
	workerStopped := controller.stopped
	controller.mu.Unlock()
	if workerStopped || provider.CountCalls("ListRunning", "") != 0 {
		t.Fatal("shutdown advanced past event admission before its callback drained")
	}

	close(releaseAdmission)
	awaitClose(t, eventDone, "shutdown event callback")
	awaitCond(t, func() bool {
		controller.mu.Lock()
		defer controller.mu.Unlock()
		return controller.stopped
	}, "shutdown to begin keyed-worker join")
	if provider.CountCalls("ListRunning", "") != 0 {
		t.Fatal("session teardown began before the keyed worker joined")
	}

	close(releaseWorker)
	awaitClose(t, shutdownDone, "shutdown after keyed-worker join")
	if got := provider.CountCalls("ListRunning", ""); got != 1 {
		t.Fatalf("session teardown ListRunning calls = %d, want 1 after child join", got)
	}
}

type sessionStartProviderSwapFixture struct {
	cr       *CityRuntime
	cityPath string
	tomlPath string
}

func newSessionStartProviderSwapFixture(
	t *testing.T,
	oldProvider runtime.Provider,
	mode rollout.Mode,
) sessionStartProviderSwapFixture {
	t.Helper()
	stubManagedDoltStoreOpeners(t)
	cityPath := t.TempDir()
	tomlPath := filepath.Join(cityPath, "city.toml")
	writeCityRuntimeConfig(t, tomlPath, "fake")
	cfg, err := config.Load(osFS{}, tomlPath)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	cr := newTestCityRuntime(t, CityRuntimeParams{
		CityPath: cityPath,
		CityName: "test-city",
		TomlPath: tomlPath,
		Cfg:      cfg,
		SP:       oldProvider,
		BuildFn: func(*config.City, runtime.Provider, beads.Store) DesiredStateResult {
			return DesiredStateResult{State: map[string]TemplateParams{}}
		},
		Dops:   newDrainOps(oldProvider),
		Rec:    events.Discard,
		Stdout: io.Discard,
		Stderr: io.Discard,
	})
	cs := newControllerState(context.Background(), cfg, oldProvider, events.NewFake(), "test-city", cityPath)
	cs.rolloutFlags = rollout.ForTest(rollout.WithSessionReconciler(mode))
	cr.setControllerState(cs)
	cr.sessionDrains = newDrainTracker()
	return sessionStartProviderSwapFixture{cr: cr, cityPath: cityPath, tomlPath: tomlPath}
}

func channelClosed(ch <-chan struct{}) bool {
	select {
	case <-ch:
		return true
	default:
		return false
	}
}

func assertSingleSessionStartOwner(
	t *testing.T,
	cr *CityRuntime,
	info session.Info,
	cfg *config.City,
) {
	t.Helper()
	keyedOwns := resolveExactSessionStartOwnership(info, cfg, time.Now().UTC())
	option := cr.sessionStartLegacyExclusionOption()
	legacyOwns := option == nil || !legacySessionStartExcluded(option, info)
	if keyedOwns == legacyOwns {
		t.Fatalf("session start owner = keyed:%t legacy:%t, want exactly one", keyedOwns, legacyOwns)
	}
}

func legacySessionStartExcluded(option startExecutionOption, info session.Info) bool {
	opts := startExecutionOptions{}
	option(&opts)
	return opts.legacyStartExcluded != nil && opts.legacyStartExcluded(info)
}

func newSessionStartCityRuntimeForTest(t *testing.T, mode rollout.Mode, coherent bool) *CityRuntime {
	t.Helper()
	cfg := &config.City{
		Workspace: config.Workspace{Name: "test-city"},
		Agents:    []config.Agent{{Name: "worker", StartCommand: "true"}},
	}
	provider := runtime.NewFake()
	store := beads.NewMemStore()
	cs := coherentSessionStartControllerStateForTest(cfg, provider, store, mode)
	if !coherent {
		cs.sessionStartStoreGeneration = 0
	}
	return &CityRuntime{
		cityPath: "test-city",
		cityName: "test-city",
		cfg:      cfg,
		sp:       provider,
		cs:       cs,
		rec:      events.Discard,
		stdout:   io.Discard,
		stderr:   io.Discard,
	}
}

func coherentSessionStartControllerStateForTest(
	cfg *config.City,
	provider runtime.Provider,
	store beads.Store,
	mode rollout.Mode,
) *controllerState {
	return &controllerState{
		cfg:                         cfg,
		sp:                          provider,
		cityBeadStore:               store,
		cityName:                    "test-city",
		cityPath:                    "test-city",
		eventProv:                   events.NewFake(),
		rolloutFlags:                rollout.ForTest(rollout.WithSessionReconciler(mode)),
		sessionStartGeneration:      1,
		sessionStartStoreGeneration: 1,
	}
}
