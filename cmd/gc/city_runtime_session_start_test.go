package main

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/config"
	"github.com/gastownhall/gascity/internal/events"
	"github.com/gastownhall/gascity/internal/rollout"
	"github.com/gastownhall/gascity/internal/runtime"
	"github.com/gastownhall/gascity/internal/session"
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
	env.cfg = &config.City{
		Workspace: config.Workspace{Name: "test-city"},
		Agents:    []config.Agent{{Name: "worker", StartCommand: "true"}},
	}
	bead := env.createSessionBead("worker", "worker")
	if err := env.store.SetMetadataBatch(bead.ID, session.RequestExplicitWakePatch(string(session.WakeCauseExplicit), env.clk.Now())); err != nil {
		t.Fatalf("request explicit wake: %v", err)
	}
	cs := coherentSessionStartControllerStateForTest(env.cfg, env.sp, env.store, rollout.Auto)
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

	cr.stopSessionStartController()
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

func assertSingleSessionStartOwner(
	t *testing.T,
	cr *CityRuntime,
	info session.Info,
	cfg *config.City,
) {
	t.Helper()
	_, _, keyedOwns := resolveExactSessionStartOwnership(info, cfg, time.Now().UTC())
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
		rolloutFlags:                rollout.ForTest(rollout.WithSessionStartReconciler(mode)),
		sessionStartGeneration:      1,
		sessionStartStoreGeneration: 1,
	}
}
