package main

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/config"
	"github.com/gastownhall/gascity/internal/events"
	"github.com/gastownhall/gascity/internal/rollout"
	"github.com/gastownhall/gascity/internal/runtime"
	"github.com/gastownhall/gascity/internal/session"
	"github.com/gastownhall/gascity/internal/testutil"
)

func TestPokeSessionStartControllerUsesExactKey(t *testing.T) {
	var commands []string
	fallbackCalled := false
	err := pokeSessionStartControllerWith(
		"/city",
		"ga-session-1",
		func(cityPath, command string) ([]byte, error) {
			if cityPath != "/city" {
				t.Fatalf("city path = %q, want /city", cityPath)
			}
			commands = append(commands, command)
			return []byte(sessionStartSocketReplyOK), nil
		},
		func(string) error {
			fallbackCalled = true
			return nil
		},
	)
	if err != nil {
		t.Fatalf("pokeSessionStartControllerWith: %v", err)
	}
	if fallbackCalled {
		t.Fatal("generic fallback called after exact admission succeeded")
	}
	want := []string{sessionStartCommandPrefix + "ga-session-1"}
	if len(commands) != len(want) || commands[0] != want[0] {
		t.Fatalf("commands = %v, want %v", commands, want)
	}
}

func TestPokeSessionStartControllerFallsBackWhenExactIngressIsUnavailable(t *testing.T) {
	fallbackCalls := 0
	err := pokeSessionStartControllerWith(
		"/city",
		"ga-session-1",
		func(string, string) ([]byte, error) {
			return []byte(sessionStartSocketReplyFallback), nil
		},
		func(cityPath string) error {
			if cityPath != "/city" {
				t.Fatalf("fallback city path = %q, want /city", cityPath)
			}
			fallbackCalls++
			return nil
		},
	)
	if err == nil || !strings.Contains(err.Error(), "exact session-start hint for \"ga-session-1\" in city \"/city\" was not admitted") || !strings.Contains(err.Error(), "controller returned \"fallback\"") || !strings.Contains(err.Error(), "generic fallback requested") {
		t.Fatalf("pokeSessionStartControllerWith = %v, want exact-ingress fallback diagnostic", err)
	}
	if fallbackCalls != 1 {
		t.Fatalf("fallback calls = %d, want 1", fallbackCalls)
	}
}

func TestPokeSessionStartControllerDoesNotFallBackWhenRequireRefusesClosed(t *testing.T) {
	fallbackCalls := 0
	err := pokeSessionStartControllerWith(
		"/city",
		"ga-session-1",
		func(string, string) ([]byte, error) {
			return []byte(sessionStartSocketReplyBlocked), nil
		},
		func(string) error {
			fallbackCalls++
			return nil
		},
	)
	if !errors.Is(err, errSessionStartControllerBlocked) {
		t.Fatalf("require refusal error = %v, want errors.Is(errSessionStartControllerBlocked)", err)
	}
	if fallbackCalls != 0 {
		t.Fatalf("fallback calls = %d, want 0", fallbackCalls)
	}
}

func TestHandleControllerConnRoutesValidatedSessionStartKey(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close() //nolint:errcheck

	admitted := make(chan string, 1)
	go handleControllerConn(
		server,
		"/city",
		controllerHostingStandalone,
		func() {},
		nil,
		&atomic.Bool{},
		nil,
		make(chan convergenceRequest),
		make(chan struct{}, 1),
		make(chan struct{}, 1),
		func(sessionID string) sessionStartSocketReply {
			admitted <- sessionID
			return sessionStartSocketReplyOK
		},
	)

	if _, err := fmt.Fprintln(client, sessionStartCommandPrefix+"ga-session-1"); err != nil {
		t.Fatalf("write command: %v", err)
	}
	response, err := bufio.NewReader(client).ReadString('\n')
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	if got := strings.TrimSpace(response); got != string(sessionStartSocketReplyOK) {
		t.Fatalf("response = %q, want %q", got, sessionStartSocketReplyOK)
	}
	select {
	case sessionID := <-admitted:
		if sessionID != "ga-session-1" {
			t.Fatalf("session ID = %q, want ga-session-1", sessionID)
		}
	case <-time.After(testutil.GoroutineRaceTimeout):
		t.Fatal("timed out waiting for exact session-start request")
	}
}

func TestHandleControllerConnRejectsInvalidSessionStartKey(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close() //nolint:errcheck

	admitCalled := false
	go handleControllerConn(
		server,
		"/city",
		controllerHostingStandalone,
		func() {},
		nil,
		&atomic.Bool{},
		nil,
		make(chan convergenceRequest),
		make(chan struct{}, 1),
		make(chan struct{}, 1),
		func(string) sessionStartSocketReply {
			admitCalled = true
			return sessionStartSocketReplyOK
		},
	)

	if _, err := fmt.Fprintln(client, sessionStartCommandPrefix+" bad key "); err != nil {
		t.Fatalf("write command: %v", err)
	}
	response, err := bufio.NewReader(client).ReadString('\n')
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	if got := strings.TrimSpace(response); got != string(sessionStartSocketReplyInvalid) {
		t.Fatalf("response = %q, want %q", got, sessionStartSocketReplyInvalid)
	}
	if admitCalled {
		t.Fatal("invalid key reached session-start admission")
	}
}

func TestCityRuntimeAdmitsSessionStartSocketKey(t *testing.T) {
	env := newReconcilerTestEnv()
	env.cfg = &config.City{Agents: []config.Agent{{Name: "worker", StartCommand: "true"}}}
	bead := env.createSessionBead("worker", "worker")
	if err := env.store.SetMetadataBatch(bead.ID, session.RequestExplicitWakePatch(string(session.WakeCauseExplicit), env.clk.Now())); err != nil {
		t.Fatalf("request explicit wake: %v", err)
	}
	reconciled := make(chan sessionStartAdmission, 1)
	controller, err := newSessionStartController(sessionStartControllerOptions{
		Workers:     1,
		MaxDistinct: 8,
		MaxRetries:  0,
		Reconcile: func(_ context.Context, admission sessionStartAdmission) error {
			reconciled <- admission
			return nil
		},
	})
	if err != nil {
		t.Fatalf("newSessionStartController: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	if err := controller.Start(ctx); err != nil {
		cancel()
		t.Fatalf("controller.Start: %v", err)
	}
	defer controller.Stop()

	cr := &CityRuntime{
		cfg:                    env.cfg,
		sp:                     env.sp,
		cs:                     coherentSessionStartControllerStateForTest(env.cfg, env.sp, env.store, rollout.Auto),
		sessionStartController: controller,
		sessionStartOwnership:  sessionStartOwnershipKeyed,
	}
	if reply := cr.admitSessionStartSocketKey(bead.ID); reply != sessionStartSocketReplyOK {
		t.Fatalf("reply = %q, want %q", reply, sessionStartSocketReplyOK)
	}
	select {
	case admission := <-reconciled:
		if admission.SessionID != bead.ID || admission.Source != sessionStartAdmissionSocket {
			t.Fatalf("admission = %+v, want exact socket key", admission)
		}
	case <-time.After(testutil.GoroutineRaceTimeout):
		t.Fatal("timed out waiting for keyed reconciliation")
	}
	cancel()
}

func TestCityRuntimeSessionStartSocketIngressFallsBackToLegacyOwner(t *testing.T) {
	var stderr bytes.Buffer
	cr := &CityRuntime{stderr: &stderr, sessionStartOwnership: sessionStartOwnershipLegacy}
	if got := cr.admitSessionStartSocketKey("ga-session-1"); got != sessionStartSocketReplyFallback {
		t.Fatalf("reply = %q, want %q", got, sessionStartSocketReplyFallback)
	}
	if !strings.Contains(stderr.String(), "exact session-start socket fallback for ga-session-1: controller unavailable or not keyed") {
		t.Fatalf("fallback diagnostic = %q, want unavailable-owner reason", stderr.String())
	}
}

func TestCityRuntimeSessionStartSocketIngressAdmitsLiveSingletonDependency(t *testing.T) {
	env := newReconcilerTestEnv()
	env.cfg = &config.City{Agents: []config.Agent{
		{Name: "database", StartCommand: "true", MaxActiveSessions: intPtr(1)},
		{Name: "worker", StartCommand: "true", DependsOn: []string{"database"}},
	}}
	dependency := env.createSessionBead("database", "database")
	env.markSessionActive(&dependency)
	env.addDesired("database", "database", true)
	bead := env.createSessionBead("worker", "worker")
	if err := env.store.SetMetadataBatch(bead.ID, session.RequestExplicitWakePatch(string(session.WakeCauseExplicit), env.clk.Now())); err != nil {
		t.Fatalf("request explicit wake: %v", err)
	}

	reconciled := make(chan sessionStartAdmission, 1)
	controller, err := newSessionStartController(sessionStartControllerOptions{
		Workers:     1,
		MaxDistinct: 8,
		MaxRetries:  0,
		Reconcile: func(_ context.Context, admission sessionStartAdmission) error {
			reconciled <- admission
			return nil
		},
	})
	if err != nil {
		t.Fatalf("newSessionStartController: %v", err)
	}
	if err := controller.Start(context.Background()); err != nil {
		t.Fatalf("controller.Start: %v", err)
	}
	t.Cleanup(controller.Stop)

	var stderr bytes.Buffer
	cr := &CityRuntime{
		cfg:                    env.cfg,
		sp:                     runtime.NewFake(),
		cs:                     coherentSessionStartControllerStateForTest(env.cfg, env.sp, env.store, rollout.Auto),
		sessionStartController: controller,
		sessionStartOwnership:  sessionStartOwnershipKeyed,
		stderr:                 &stderr,
	}
	if got := cr.admitSessionStartSocketKey(bead.ID); got != sessionStartSocketReplyOK {
		t.Fatalf("reply = %q, want %q for a live singleton dependency", got, sessionStartSocketReplyOK)
	}
	select {
	case admission := <-reconciled:
		if admission.ConfiguredDependency == nil {
			t.Fatal("configured-dependency admission did not retain its private witness")
		}
		if got, want := *admission.ConfiguredDependency, (configuredDependencyStartLease{
			SessionID:               bead.ID,
			TargetTemplate:          "worker",
			DependencyTemplate:      "database",
			DependencySessionID:     dependency.ID,
			DependencySessionName:   "database",
			DependencyInstanceToken: "test-token",
			ControllerGeneration:    1,
		}); got != want {
			t.Fatalf("configured-dependency witness = %+v, want %+v", got, want)
		}
	case <-time.After(testutil.GoroutineRaceTimeout):
		t.Fatal("timed out waiting for configured-dependency admission")
	}
}

func TestCityRuntimeSessionStartSocketIngressLeavesUnsupportedDependencyShapesToLegacy(t *testing.T) {
	tests := []struct {
		name       string
		dependsOn  []string
		dependency *config.Agent
		live       []string
		metadata   map[string]string
	}{
		{name: "missing", dependsOn: []string{"missing"}},
		{name: "cold", dependsOn: []string{"database"}, dependency: &config.Agent{Name: "database", StartCommand: "true", MaxActiveSessions: intPtr(1)}},
		{name: "multiple", dependsOn: []string{"database", "cache"}, dependency: &config.Agent{Name: "database", StartCommand: "true", MaxActiveSessions: intPtr(1)}, live: []string{"database", "cache"}},
		{name: "non-singleton", dependsOn: []string{"database"}, dependency: &config.Agent{Name: "database", StartCommand: "true"}, live: []string{"database"}},
		{name: "named", dependsOn: []string{"database"}, dependency: &config.Agent{Name: "database", StartCommand: "true", MaxActiveSessions: intPtr(1)}, live: []string{"database"}, metadata: map[string]string{session.NamedSessionMetadataKey: "true"}},
		{name: "pool", dependsOn: []string{"database"}, dependency: &config.Agent{Name: "database", StartCommand: "true", MaxActiveSessions: intPtr(1)}, live: []string{"database"}, metadata: map[string]string{poolManagedMetadataKey: "true"}},
		{name: "manual", dependsOn: []string{"database"}, dependency: &config.Agent{Name: "database", StartCommand: "true", MaxActiveSessions: intPtr(1)}, live: []string{"database"}, metadata: map[string]string{"manual_session": "true"}},
		{name: "dependency-only", dependsOn: []string{"database"}, dependency: &config.Agent{Name: "database", StartCommand: "true", MaxActiveSessions: intPtr(1)}, live: []string{"database"}, metadata: map[string]string{"dependency_only": "true"}},
		{name: "pending-create", dependsOn: []string{"database"}, dependency: &config.Agent{Name: "database", StartCommand: "true", MaxActiveSessions: intPtr(1)}, live: []string{"database"}, metadata: map[string]string{"pending_create_claim": "true", "state": string(session.StateCreating)}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			env := newReconcilerTestEnv()
			agents := []config.Agent{{Name: "worker", StartCommand: "true", DependsOn: test.dependsOn}}
			if test.dependency != nil {
				agents = append([]config.Agent{*test.dependency}, agents...)
			}
			if test.name == "multiple" {
				agents = append([]config.Agent{{Name: "cache", StartCommand: "true", MaxActiveSessions: intPtr(1)}}, agents...)
			}
			env.cfg = &config.City{Agents: agents}
			for _, dependency := range test.live {
				env.addDesired(dependency, dependency, true)
			}
			bead := env.createSessionBead("worker", "worker")
			if err := env.store.SetMetadataBatch(bead.ID, session.RequestExplicitWakePatch(string(session.WakeCauseExplicit), env.clk.Now())); err != nil {
				t.Fatalf("request explicit wake: %v", err)
			}
			env.setSessionMetadata(&bead, test.metadata)

			controller, err := newSessionStartController(sessionStartControllerOptions{
				Workers: 1, MaxDistinct: 8, MaxRetries: 0,
				Reconcile: func(context.Context, sessionStartAdmission) error { return nil },
			})
			if err != nil {
				t.Fatalf("newSessionStartController: %v", err)
			}
			if err := controller.Start(t.Context()); err != nil {
				t.Fatalf("controller.Start: %v", err)
			}
			t.Cleanup(controller.Stop)
			cr := &CityRuntime{
				cfg:                    env.cfg,
				sp:                     env.sp,
				cs:                     coherentSessionStartControllerStateForTest(env.cfg, env.sp, env.store, rollout.Auto),
				sessionStartController: controller,
				sessionStartOwnership:  sessionStartOwnershipKeyed,
				stderr:                 &bytes.Buffer{},
			}
			if got := cr.admitSessionStartSocketKey(bead.ID); got != sessionStartSocketReplyFallback {
				t.Fatalf("reply = %q, want legacy fallback", got)
			}
		})
	}
}

func TestReconcileConfiguredDependencyWakeStartsTargetOnceWithoutChangingDependency(t *testing.T) {
	for _, mode := range []rollout.Mode{rollout.Auto, rollout.Require} {
		t.Run(string(mode), func(t *testing.T) {
			env := newReconcilerTestEnv()
			env.cfg = &config.City{
				Workspace: config.Workspace{Name: "test-city"},
				Agents: []config.Agent{
					{Name: "database", StartCommand: "true", MaxActiveSessions: intPtr(1)},
					{Name: "worker", StartCommand: "true", DependsOn: []string{"database"}},
				},
			}
			dependency := env.createSessionBead("database", "database")
			env.markSessionActive(&dependency)
			env.addDesired("database", "database", true)
			dependencyBefore := env.sessionInfo(dependency.ID)
			target := env.createSessionBead("worker", "worker")
			if err := env.store.SetMetadataBatch(target.ID, session.RequestExplicitWakePatch(string(session.WakeCauseExplicit), env.clk.Now())); err != nil {
				t.Fatalf("request explicit wake: %v", err)
			}
			lease, certified := certifyConfiguredDependencyStartLease(
				env.sessionInfo(target.ID), env.cfg, env.sp, "test-city", env.store, 1, env.clk.Now().UTC(),
			)
			if !certified {
				t.Fatal("live canonical singleton dependency was not certified")
			}
			params := exactSessionStartTestParams(t, env)
			params.Generation = 1
			params.RolloutMode = mode
			params.ValidateConfiguredDependencyStart = func(info session.Info, retained configuredDependencyStartLease) bool {
				return configuredDependencyStartTargetMatches(info, env.cfg, retained) &&
					allDependenciesAliveForTemplateWithClock(retained.TargetTemplate, env.cfg, nil, env.sp, "test-city", env.store, env.clk)
			}
			params.EnterConfiguredDependencyStart = func(configuredDependencyStartLease) bool { return true }

			owner, err := reconcileExactSessionStartWithOwner(t.Context(), sessionStartAdmission{
				SessionID:            target.ID,
				Source:               sessionStartAdmissionSocket,
				ConfiguredDependency: &lease,
			}, params)
			if err != nil || owner != exactSessionStartKeyedOwner {
				t.Fatalf("reconcile result = (owner=%v, err=%v), want keyed success", owner, err)
			}
			if got := env.sp.CountCalls("Start", "worker"); got != 1 {
				t.Fatalf("target provider Start calls = %d, want 1", got)
			}
			started := env.sessionInfo(target.ID)
			if started.ID != target.ID || started.SessionName != "worker" || started.MetadataState != string(session.StateActive) || started.WakeRequest != "" {
				t.Fatalf("started target = %+v, want same keyed worker active with wake cleared", started)
			}
			dependencyAfter := env.sessionInfo(dependency.ID)
			if dependencyAfter.ID != dependencyBefore.ID || dependencyAfter.InstanceToken != dependencyBefore.InstanceToken {
				t.Fatalf("dependency identity changed: before=%+v after=%+v", dependencyBefore, dependencyAfter)
			}
			if !env.sp.IsRunning("database") || env.sp.CountCalls("Start", "database") != 1 || env.sp.CountCalls("Stop", "database") != 0 {
				t.Fatalf("dependency runtime changed: running=%t starts=%d stops=%d", env.sp.IsRunning("database"), env.sp.CountCalls("Start", "database"), env.sp.CountCalls("Stop", "database"))
			}
		})
	}
}

func TestReconcileConfiguredDependencyWakeRechecksWitnessBeforeEffects(t *testing.T) {
	for _, phase := range []struct {
		name         string
		validThrough int32
		preMutation  bool
	}{
		{name: "pre-wake", validThrough: 1, preMutation: true},
		{name: "provider-entry", validThrough: 2},
	} {
		for _, mode := range []rollout.Mode{rollout.Auto, rollout.Require} {
			t.Run(phase.name+"/"+string(mode), func(t *testing.T) {
				env := newReconcilerTestEnv()
				env.cfg = &config.City{
					Workspace: config.Workspace{Name: "test-city"},
					Agents: []config.Agent{
						{Name: "database", StartCommand: "true", MaxActiveSessions: intPtr(1)},
						{Name: "worker", StartCommand: "true", DependsOn: []string{"database"}},
					},
				}
				dependency := env.createSessionBead("database", "database")
				env.markSessionActive(&dependency)
				env.addDesired("database", "database", true)
				target := env.createSessionBead("worker", "worker")
				if err := env.store.SetMetadataBatch(target.ID, session.RequestExplicitWakePatch(string(session.WakeCauseExplicit), env.clk.Now())); err != nil {
					t.Fatalf("request explicit wake: %v", err)
				}
				before := env.sessionInfo(target.ID)
				lease := configuredDependencyStartLease{
					SessionID: target.ID, TargetTemplate: "worker", DependencyTemplate: "database",
					DependencySessionID: dependency.ID, DependencySessionName: "database", DependencyInstanceToken: "test-token",
					ControllerGeneration: 1,
				}
				params := exactSessionStartTestParams(t, env)
				params.Generation = 1
				params.RolloutMode = mode
				var validations atomic.Int32
				params.ValidateConfiguredDependencyStart = func(session.Info, configuredDependencyStartLease) bool {
					return validations.Add(1) <= phase.validThrough
				}
				params.EnterConfiguredDependencyStart = func(configuredDependencyStartLease) bool { return true }

				owner, err := reconcileExactSessionStartWithOwner(t.Context(), sessionStartAdmission{
					SessionID: target.ID, Source: sessionStartAdmissionSocket, ConfiguredDependency: &lease,
				}, params)
				if phase.preMutation && mode == rollout.Auto {
					if owner != exactSessionStartLegacyOwner || err != nil {
						t.Fatalf("Auto pre-wake result = (owner=%v, err=%v), want clean legacy yield", owner, err)
					}
				} else if owner != exactSessionStartKeyedOwner || err == nil {
					t.Fatalf("parked result = (owner=%v, err=%v), want keyed error", owner, err)
				}
				if got := env.sp.CountCalls("Start", "worker"); got != 0 {
					t.Fatalf("target provider Start calls = %d, want 0 after witness drift", got)
				}
				if phase.preMutation {
					after := env.sessionInfo(target.ID)
					if after.Generation != before.Generation || after.InstanceToken != before.InstanceToken || after.MetadataState != before.MetadataState || after.WakeRequest != before.WakeRequest || after.LastWokeAt != before.LastWokeAt {
						t.Fatalf("pre-wake drift mutated target: before=%+v after=%+v", before, after)
					}
				}
			})
		}
	}
}

func TestReconcileConfiguredDependencyWakeRejectsDependencyReplacementIdentity(t *testing.T) {
	for _, phase := range []string{"pre-wake", "provider-entry"} {
		for _, mode := range []rollout.Mode{rollout.Auto, rollout.Require} {
			t.Run(phase+"/"+string(mode), func(t *testing.T) {
				env := newReconcilerTestEnv()
				env.cfg = &config.City{
					Workspace: config.Workspace{Name: "test-city"},
					Agents: []config.Agent{
						{Name: "database", StartCommand: "true", MaxActiveSessions: intPtr(1)},
						{Name: "worker", StartCommand: "true", DependsOn: []string{"database"}},
					},
				}
				dependency := env.createSessionBead("database", "database")
				env.markSessionActive(&dependency)
				env.addDesired("database", "database", true)
				target := env.createSessionBead("worker", "worker")
				if err := env.store.SetMetadataBatch(target.ID, session.RequestExplicitWakePatch(string(session.WakeCauseExplicit), env.clk.Now())); err != nil {
					t.Fatalf("request explicit wake: %v", err)
				}
				lease, certified := certifyConfiguredDependencyStartLease(
					env.sessionInfo(target.ID), env.cfg, env.sp, "test-city", env.store, 1, env.clk.Now().UTC(),
				)
				if !certified {
					t.Fatal("live canonical dependency was not certified")
				}
				before := env.sessionInfo(target.ID)
				var replaced atomic.Bool
				replaceDependency := func() {
					if !replaced.CompareAndSwap(false, true) {
						return
					}
					if err := env.store.Close(dependency.ID); err != nil {
						t.Fatalf("close certified dependency: %v", err)
					}
					replacement := env.createSessionBead("database", "database")
					env.setSessionMetadata(&replacement, map[string]string{
						"state":          string(session.StateActive),
						"instance_token": "replacement-token",
					})
				}
				cr := &CityRuntime{cfg: env.cfg}
				snapshot := controllerSessionStartSnapshot{
					Generation: 1, CityName: "test-city", Config: env.cfg, Provider: env.sp, Store: env.store,
				}
				params := exactSessionStartTestParams(t, env)
				params.Generation = 1
				params.RolloutMode = mode
				params.ValidateConfiguredDependencyStart = func(info session.Info, retained configuredDependencyStartLease) bool {
					current := cr.configuredDependencyStartWitnessCurrent(snapshot, info, retained)
					if phase == "pre-wake" {
						replaceDependency()
					}
					return current
				}
				params.EnterConfiguredDependencyStart = func(configuredDependencyStartLease) bool { return true }
				if phase == "provider-entry" {
					params.StartOptions = append(params.StartOptions, withTaskWorkDirResolver(func(startCandidate, *config.City) string {
						replaceDependency()
						return ""
					}))
				}

				owner, err := reconcileExactSessionStartWithOwner(t.Context(), sessionStartAdmission{
					SessionID: target.ID, Source: sessionStartAdmissionSocket, ConfiguredDependency: &lease,
				}, params)
				if phase == "pre-wake" && mode == rollout.Auto {
					if owner != exactSessionStartLegacyOwner || err != nil {
						t.Fatalf("Auto pre-wake replacement result = (owner=%v, err=%v), want clean legacy yield", owner, err)
					}
				} else if owner != exactSessionStartKeyedOwner || err == nil {
					t.Fatalf("replacement result = (owner=%v, err=%v), want keyed park", owner, err)
				}
				if got := env.sp.CountCalls("Start", "worker"); got != 0 {
					t.Fatalf("target provider Start calls = %d, want 0 after dependency replacement", got)
				}
				if phase == "pre-wake" {
					after := env.sessionInfo(target.ID)
					if after.Generation != before.Generation || after.InstanceToken != before.InstanceToken || after.WakeRequest != before.WakeRequest {
						t.Fatalf("pre-wake replacement mutated target: before=%+v after=%+v", before, after)
					}
				}
			})
		}
	}
}

func TestConfiguredDependencyWakeRedriveStaysKeyedAfterPreWake(t *testing.T) {
	env := newReconcilerTestEnv()
	env.cfg = &config.City{
		Workspace: config.Workspace{Name: "test-city"},
		Agents: []config.Agent{
			{Name: "database", StartCommand: "true", MaxActiveSessions: intPtr(1)},
			{Name: "worker", StartCommand: "true", DependsOn: []string{"database"}},
		},
	}
	dependency := env.createSessionBead("database", "database")
	env.markSessionActive(&dependency)
	env.addDesired("database", "database", true)
	target := env.createSessionBead("worker", "worker")
	if err := env.store.SetMetadataBatch(target.ID, session.RequestExplicitWakePatch(string(session.WakeCauseExplicit), env.clk.Now())); err != nil {
		t.Fatalf("request explicit wake: %v", err)
	}
	dependencyStopped := make(chan struct{})
	var stopOnce atomic.Bool
	cs := coherentSessionStartControllerStateForTest(env.cfg, env.sp, env.store, rollout.Auto)
	cr := &CityRuntime{
		cityPath: "test-city", cityName: "test-city", cfg: env.cfg, sp: env.sp, cs: cs,
		rec: events.Discard, stdout: &bytes.Buffer{}, stderr: &bytes.Buffer{},
		sessionStartOptions: []startExecutionOption{
			withStartStabilityWaiter(immediateStartStabilityWaiter),
			withSessionStaleKeyDetectionWaiter(immediateSessionStaleKeyDetectionWaiter),
			withTaskWorkDirResolver(func(startCandidate, *config.City) string {
				if stopOnce.CompareAndSwap(false, true) {
					if err := env.sp.Stop("database"); err != nil {
						t.Errorf("stop dependency before provider entry: %v", err)
					}
					close(dependencyStopped)
				}
				return ""
			}),
		},
	}
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	if err := cr.ensureSessionStartController(ctx, newSessionBeadSnapshotFromInfos(nil)); err != nil {
		t.Fatalf("ensure session-start controller: %v", err)
	}
	t.Cleanup(cr.stopSessionStartController)
	if got := cr.admitSessionStartSocketKey(target.ID); got != sessionStartSocketReplyOK {
		t.Fatalf("socket reply = %q, want keyed admission", got)
	}
	awaitClose(t, dependencyStopped, "configured dependency stop after pre-wake")
	awaitCond(t, func() bool {
		controller := cr.sessionStartController
		controller.mu.Lock()
		defer controller.mu.Unlock()
		_, retained := controller.admissions[target.ID]
		_, inFlight := controller.inFlight[target.ID]
		return retained && !inFlight
	}, "parked configured-dependency admission")
	if got := env.sp.CountCalls("Start", "worker"); got != 0 {
		t.Fatalf("first-attempt target starts = %d, want 0 after provider-entry drift", got)
	}
	if err := env.sp.Start(t.Context(), "database", runtime.Config{Command: "test-cmd"}); err != nil {
		t.Fatalf("restore certified dependency runtime: %v", err)
	}
	if _, err := cr.sessionStartController.Admit(target.ID, sessionStartAdmissionInProcess); err != nil {
		t.Fatalf("redrive configured-dependency admission: %v", err)
	}
	awaitCond(t, func() bool {
		return env.sp.CountCalls("Start", "worker") == 1 || cr.sessionStartController.Pending() == 0
	}, "configured-dependency redrive completion")
	if got := env.sp.CountCalls("Start", "worker"); got != 1 {
		t.Fatalf("redrive target starts = %d, want 1 from retained keyed ownership", got)
	}
	if got := env.sessionInfo(target.ID); got.MetadataState != string(session.StateActive) || got.WakeRequest != "" {
		t.Fatalf("redriven target = %+v, want active with wake cleared", got)
	}
}

func TestCityRuntimeSessionStartSocketIngressClassifiesFromAuthoritativeRow(t *testing.T) {
	env := newReconcilerTestEnv()
	env.cfg = &config.City{Agents: []config.Agent{{Name: "worker", StartCommand: "true"}}}
	backing := env.store
	bead := env.createSessionBead("worker", "worker")
	env.setSessionMetadata(&bead, map[string]string{"session_origin": "manual"})
	if err := backing.SetMetadataBatch(bead.ID, session.RequestExplicitWakePatch(string(session.WakeCauseExplicit), env.clk.Now())); err != nil {
		t.Fatalf("request explicit wake: %v", err)
	}
	cache := beads.NewCachingStore(backing, nil)
	if err := cache.PrimeActive(); err != nil {
		t.Fatalf("prime controller cache: %v", err)
	}
	if err := backing.SetMetadata(bead.ID, poolManagedMetadataKey, "true"); err != nil {
		t.Fatalf("mark backing row pool-managed: %v", err)
	}

	controller, err := newSessionStartController(sessionStartControllerOptions{
		Workers:     1,
		MaxDistinct: 8,
		MaxRetries:  0,
		Reconcile: func(context.Context, sessionStartAdmission) error {
			return nil
		},
	})
	if err != nil {
		t.Fatalf("newSessionStartController: %v", err)
	}
	if err := controller.Start(context.Background()); err != nil {
		t.Fatalf("controller.Start: %v", err)
	}
	t.Cleanup(controller.Stop)

	cr := &CityRuntime{
		cfg:                    env.cfg,
		sp:                     env.sp,
		cs:                     coherentSessionStartControllerStateForTest(env.cfg, env.sp, cache, rollout.Auto),
		sessionStartController: controller,
		sessionStartOwnership:  sessionStartOwnershipKeyed,
	}
	if got := cr.admitSessionStartSocketKey(bead.ID); got != sessionStartSocketReplyFallback {
		t.Fatalf("reply = %q, want %q for live pool row hidden by stale cache", got, sessionStartSocketReplyFallback)
	}
}
