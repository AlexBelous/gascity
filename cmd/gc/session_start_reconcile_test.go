package main

import (
	"context"
	"fmt"
	"io"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/config"
	"github.com/gastownhall/gascity/internal/events"
	"github.com/gastownhall/gascity/internal/runtime"
	"github.com/gastownhall/gascity/internal/session"
)

func TestReconcileExactSessionStartStartsPendingCreateAndCommitsActive(t *testing.T) {
	env := newReconcilerTestEnv()
	env.cfg = &config.City{
		Workspace: config.Workspace{Name: "test-city"},
		Agents: []config.Agent{{
			Name:         "worker",
			StartCommand: "true",
		}},
	}
	bead := env.createSessionBead("worker", "worker")
	env.setSessionMetadata(&bead, map[string]string{
		"state":                     string(session.StateCreating),
		"pending_create_claim":      "true",
		"pending_create_started_at": env.clk.Now().UTC().Format(time.RFC3339),
	})

	err := reconcileExactSessionStart(context.Background(), sessionStartAdmission{
		SessionID: bead.ID,
		Source:    sessionStartAdmissionPendingCreate,
	}, exactSessionStartParams{
		CityPath: t.TempDir(),
		CityName: "test-city",
		Config:   env.cfg,
		Provider: env.sp,
		Store:    env.store,
		Clock:    env.clk,
		Recorder: events.Discard,
		Stdout:   io.Discard,
		Stderr:   io.Discard,
		StartOptions: []startExecutionOption{
			withStartStabilityWaiter(immediateStartStabilityWaiter),
			withSessionStaleKeyDetectionWaiter(immediateSessionStaleKeyDetectionWaiter),
		},
	})
	if err != nil {
		t.Fatalf("reconcileExactSessionStart: %v", err)
	}
	if !env.sp.IsRunning("worker") {
		t.Fatal("pending-create runtime was not started")
	}
	got := env.sessionInfo(bead.ID)
	if got.MetadataState != string(session.StateActive) {
		t.Fatalf("persisted state = %q, want active", got.MetadataState)
	}
	if got.PendingCreateClaim {
		t.Fatal("pending_create_claim remained set after successful start")
	}
}

func TestReconcileExactSessionStartColdCacheBackingGetBudget(t *testing.T) {
	env := newReconcilerTestEnv()
	env.cfg = &config.City{
		Workspace: config.Workspace{Name: "test-city"},
		Agents:    []config.Agent{{Name: "worker", StartCommand: "true"}},
	}
	counting := &getCountingStore{Store: env.store}
	cache := beads.NewCachingStore(counting, nil)
	if err := cache.PrimeActive(); err != nil {
		t.Fatalf("prime empty controller cache: %v", err)
	}

	// Create through the backing store after priming, reproducing a session
	// written by another process immediately before its exact socket hint.
	bead := env.createSessionBead("worker", "worker")
	env.setSessionMetadata(&bead, map[string]string{
		"state":                     string(session.StateCreating),
		"pending_create_claim":      "true",
		"pending_create_started_at": env.clk.Now().UTC().Format(time.RFC3339),
	})
	counting.reset()

	params := exactSessionStartTestParams(t, env)
	params.Store = cache
	if err := reconcileExactSessionStart(context.Background(), sessionStartAdmission{
		SessionID: bead.ID,
		Source:    sessionStartAdmissionPendingCreate,
	}, params); err != nil {
		t.Fatalf("reconcileExactSessionStart: %v", err)
	}

	// One authoritative admission read, one pre-wake freshness read, and the
	// authoritative refresh after each of the pre-wake and final commit writes
	// are the only backing reads needed. Runtime observation must reuse the
	// admission's loaded session record.
	const wantGets = 4
	if got := counting.count(); got != wantGets {
		t.Fatalf("cold-cache exact start issued %d backing store.Get calls, want %d", got, wantGets)
	}
}

func TestReconcileExactSessionStartRefreshesCachedRowBeforeOwnership(t *testing.T) {
	env := newReconcilerTestEnv()
	env.cfg = &config.City{
		Workspace: config.Workspace{Name: "test-city"},
		Agents:    []config.Agent{{Name: "worker", StartCommand: "true"}},
	}
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

	// Simulate an external writer changing the already-cached row into a pool
	// member immediately before sending its exact socket hint. Pool ownership
	// requires a fleet capacity census, so a stale cache hit must not start it.
	if err := backing.SetMetadata(bead.ID, poolManagedMetadataKey, "true"); err != nil {
		t.Fatalf("mark backing row pool-managed: %v", err)
	}
	params := exactSessionStartTestParams(t, env)
	params.Store = cache
	if err := reconcileExactSessionStart(context.Background(), sessionStartAdmission{
		SessionID: bead.ID,
		Source:    sessionStartAdmissionSocket,
	}, params); err != nil {
		t.Fatalf("reconcileExactSessionStart: %v", err)
	}
	if got := env.sp.CountCalls("Start", "worker"); got != 0 {
		t.Fatalf("provider Start calls = %d, want 0 after authoritative pool reclassification", got)
	}
}

func TestReconcileExactSessionStartRechecksOwnershipAtPreWake(t *testing.T) {
	env := newReconcilerTestEnv()
	env.cfg = &config.City{
		Workspace: config.Workspace{Name: "test-city"},
		Agents:    []config.Agent{{Name: "worker", StartCommand: "true"}},
	}
	bead := env.createSessionBead("worker", "worker")
	env.setSessionMetadata(&bead, map[string]string{"session_origin": "manual"})
	if err := env.store.SetMetadataBatch(bead.ID, session.RequestExplicitWakePatch(string(session.WakeCauseExplicit), env.clk.Now())); err != nil {
		t.Fatalf("request explicit wake: %v", err)
	}
	before := env.sessionInfo(bead.ID)

	store := &mutateAfterFirstSessionGetStore{
		Store:  env.store,
		target: bead.ID,
		afterFirst: func() {
			if err := env.store.SetMetadata(bead.ID, poolManagedMetadataKey, "true"); err != nil {
				t.Fatalf("mark backing row pool-managed: %v", err)
			}
		},
	}
	params := exactSessionStartTestParams(t, env)
	params.Store = store
	owner, err := reconcileExactSessionStartWithOwner(context.Background(), sessionStartAdmission{
		SessionID: bead.ID,
		Source:    sessionStartAdmissionSocket,
	}, params)
	if err != nil {
		t.Fatalf("reconcileExactSessionStartWithOwner: %v", err)
	}
	if owner != exactSessionStartLegacyOwner {
		t.Fatalf("owner = %v, want legacy after pre-wake pool reclassification", owner)
	}
	if got := env.sp.CountCalls("Start", "worker"); got != 0 {
		t.Fatalf("provider Start calls = %d, want 0 after pre-wake pool reclassification", got)
	}
	got := env.sessionInfo(bead.ID)
	if got.Generation != before.Generation || got.InstanceToken != before.InstanceToken {
		t.Fatalf(
			"pre-wake mutation landed after ownership changed: generation/token (%q, %q) -> (%q, %q)",
			before.Generation,
			before.InstanceToken,
			got.Generation,
			got.InstanceToken,
		)
	}
}

func TestReconcileExactSessionStartRechecksBlockersAtPreWake(t *testing.T) {
	env := newReconcilerTestEnv()
	env.cfg = &config.City{
		Workspace: config.Workspace{Name: "test-city"},
		Agents:    []config.Agent{{Name: "worker", StartCommand: "true"}},
	}
	bead := env.createSessionBead("worker", "worker")
	env.setSessionMetadata(&bead, map[string]string{"session_origin": "manual"})
	if err := env.store.SetMetadataBatch(bead.ID, session.RequestExplicitWakePatch(string(session.WakeCauseExplicit), env.clk.Now())); err != nil {
		t.Fatalf("request explicit wake: %v", err)
	}
	before := env.sessionInfo(bead.ID)

	store := &mutateAfterFirstSessionGetStore{
		Store:  env.store,
		target: bead.ID,
		afterFirst: func() {
			heldUntil := env.clk.Now().Add(time.Hour).UTC().Format(time.RFC3339)
			if err := env.store.SetMetadata(bead.ID, "held_until", heldUntil); err != nil {
				t.Fatalf("hold session: %v", err)
			}
		},
	}
	params := exactSessionStartTestParams(t, env)
	params.Store = store
	owner, err := reconcileExactSessionStartWithOwner(context.Background(), sessionStartAdmission{
		SessionID: bead.ID,
		Source:    sessionStartAdmissionSocket,
	}, params)
	if err != nil {
		t.Fatalf("reconcileExactSessionStartWithOwner: %v", err)
	}
	if owner != exactSessionStartKeyedOwner {
		t.Fatalf("owner = %v, want keyed no-op after pre-wake hold", owner)
	}
	if got := env.sp.CountCalls("Start", "worker"); got != 0 {
		t.Fatalf("provider Start calls = %d, want 0 after pre-wake hold", got)
	}
	got := env.sessionInfo(bead.ID)
	if got.Generation != before.Generation || got.InstanceToken != before.InstanceToken {
		t.Fatalf(
			"pre-wake mutation landed after hold: generation/token (%q, %q) -> (%q, %q)",
			before.Generation,
			before.InstanceToken,
			got.Generation,
			got.InstanceToken,
		)
	}
}

func TestReconcileExactSessionStartDoesNotDuplicateLiveRuntime(t *testing.T) {
	env := newReconcilerTestEnv()
	env.cfg = &config.City{
		Workspace: config.Workspace{Name: "test-city"},
		Agents:    []config.Agent{{Name: "worker", StartCommand: "true"}},
	}
	bead := env.createSessionBead("worker", "worker")
	if err := env.store.SetMetadataBatch(bead.ID, session.RequestExplicitWakePatch(string(session.WakeCauseExplicit), env.clk.Now())); err != nil {
		t.Fatalf("request explicit wake: %v", err)
	}
	if err := env.sp.Start(context.Background(), "worker", runtime.Config{Command: "true"}); err != nil {
		t.Fatalf("seed live runtime: %v", err)
	}

	if err := reconcileExactSessionStart(context.Background(), sessionStartAdmission{
		SessionID: bead.ID,
		Source:    sessionStartAdmissionExplicitWake,
	}, exactSessionStartTestParams(t, env)); err != nil {
		t.Fatalf("reconcileExactSessionStart: %v", err)
	}
	if got := env.sp.CountCalls("Start", "worker"); got != 1 {
		t.Fatalf("provider Start calls = %d, want only the fixture start", got)
	}
}

func TestReconcileExactSessionStartDiscardsRuntimeWhenSessionClosesDuringStart(t *testing.T) {
	env := newReconcilerTestEnv()
	env.cfg = &config.City{
		Workspace: config.Workspace{Name: "test-city"},
		Agents:    []config.Agent{{Name: "worker", StartCommand: "true"}},
	}
	bead := env.createSessionBead("worker", "worker")
	env.setSessionMetadata(&bead, map[string]string{
		"state":                     string(session.StateCreating),
		"pending_create_claim":      "true",
		"pending_create_started_at": env.clk.Now().UTC().Format(time.RFC3339),
	})
	provider := newGatedStartProvider()
	params := exactSessionStartTestParams(t, env)
	params.Provider = provider

	done := make(chan error, 1)
	go func() {
		done <- reconcileExactSessionStart(context.Background(), sessionStartAdmission{
			SessionID: bead.ID,
			Source:    sessionStartAdmissionPendingCreate,
		}, params)
	}()
	t.Cleanup(func() { provider.release("worker") })

	select {
	case name := <-provider.startSignals:
		if name != "worker" {
			t.Fatalf("provider started %q, want worker", name)
		}
	case <-time.After(time.Second):
		t.Fatal("provider Start was not entered")
	}
	if err := env.store.Close(bead.ID); err != nil {
		t.Fatalf("close session during start: %v", err)
	}
	if err := provider.SetMeta("worker", "GC_SESSION_ID", bead.ID); err != nil {
		t.Fatalf("expose runtime session identity: %v", err)
	}
	if err := provider.SetMeta("worker", "GC_INSTANCE_TOKEN", "test-token"); err != nil {
		t.Fatalf("expose runtime instance identity: %v", err)
	}
	provider.release("worker")

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("stale start should converge as a no-op: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("exact start did not return")
	}
	got := env.sessionInfo(bead.ID)
	if !got.Closed {
		t.Fatal("session was reopened by stale start commit")
	}
	if got.MetadataState == string(session.StateActive) {
		t.Fatal("stale start overwrote the closed session state with active")
	}
	if provider.IsRunning("worker") {
		t.Fatal("runtime from stale start remained live")
	}
	if got := provider.CountCalls("Stop", "worker"); got != 1 {
		t.Fatalf("provider Stop calls = %d, want 1 stale-runtime cleanup", got)
	}
}

func TestReconcileExactSessionStartParksUnsafeCandidatesWithoutMutation(t *testing.T) {
	tests := []struct {
		name     string
		metadata func(time.Time) map[string]string
	}{
		{
			name: "quarantined",
			metadata: func(now time.Time) map[string]string {
				return map[string]string{
					"state":             string(session.StateQuarantined),
					"wake_request":      string(session.WakeCauseExplicit),
					"wake_requested_at": now.Format(time.RFC3339),
					"quarantined_until": now.Add(time.Hour).Format(time.RFC3339),
				}
			},
		},
		{
			name: "failed create",
			metadata: func(now time.Time) map[string]string {
				return map[string]string{
					"state":                     string(session.StateFailedCreate),
					"pending_create_claim":      "true",
					"pending_create_started_at": now.Format(time.RFC3339),
				}
			},
		},
		{
			name: "start already in flight",
			metadata: func(now time.Time) map[string]string {
				return map[string]string{
					"state":                     string(session.StateCreating),
					"pending_create_claim":      "true",
					"pending_create_started_at": now.Format(time.RFC3339),
					"last_woke_at":              now.Format(time.RFC3339),
				}
			},
		},
		{
			name: "circuit open",
			metadata: func(now time.Time) map[string]string {
				return map[string]string{
					"state":                 string(session.StateAsleep),
					"wake_request":          string(session.WakeCauseExplicit),
					"wake_requested_at":     now.Format(time.RFC3339),
					"session_circuit_state": session.SessionCircuitStateOpen,
				}
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			env := newReconcilerTestEnv()
			env.cfg = &config.City{
				Workspace: config.Workspace{Name: "test-city"},
				Agents:    []config.Agent{{Name: "worker", StartCommand: "true"}},
			}
			bead := env.createSessionBead("worker", "worker")
			metadata := test.metadata(env.clk.Now().UTC())
			env.setSessionMetadata(&bead, metadata)

			if err := reconcileExactSessionStart(context.Background(), sessionStartAdmission{
				SessionID: bead.ID,
				Source:    sessionStartAdmissionAntiEntropy,
			}, exactSessionStartTestParams(t, env)); err != nil {
				t.Fatalf("reconcileExactSessionStart: %v", err)
			}
			if got := env.sp.CountCalls("Start", "worker"); got != 0 {
				t.Fatalf("provider Start calls = %d, want 0", got)
			}
			stored, err := env.store.Get(bead.ID)
			if err != nil {
				t.Fatalf("get parked session: %v", err)
			}
			for key, want := range metadata {
				if got := stored.Metadata[key]; got != want {
					t.Fatalf("metadata %s = %q, want unchanged %q", key, got, want)
				}
			}
		})
	}
}

func TestReconcileExactSessionStartLeavesDependencyTemplatesToLegacy(t *testing.T) {
	env := newReconcilerTestEnv()
	env.cfg = &config.City{
		Workspace: config.Workspace{Name: "test-city"},
		Agents: []config.Agent{
			{Name: "database", StartCommand: "true"},
			{Name: "worker", StartCommand: "true", DependsOn: []string{"database"}},
		},
	}
	bead := env.createSessionBead("worker", "worker")
	if err := env.store.SetMetadataBatch(bead.ID, session.RequestExplicitWakePatch(string(session.WakeCauseExplicit), env.clk.Now())); err != nil {
		t.Fatalf("request explicit wake: %v", err)
	}

	if err := reconcileExactSessionStart(context.Background(), sessionStartAdmission{
		SessionID: bead.ID,
		Source:    sessionStartAdmissionExplicitWake,
	}, exactSessionStartTestParams(t, env)); err != nil {
		t.Fatalf("reconcileExactSessionStart: %v", err)
	}
	if got := env.sp.CountCalls("Start", "worker"); got != 0 {
		t.Fatalf("provider Start calls = %d, want 0", got)
	}
	if got := env.sessionInfo(bead.ID).WakeRequest; got != string(session.WakeCauseExplicit) {
		t.Fatalf("wake request = %q, want legacy-owned request unchanged", got)
	}
}

func TestReconcileExactSessionStartTemplateResolutionFailurePreservesWake(t *testing.T) {
	env := newReconcilerTestEnv()
	env.cfg = &config.City{
		Workspace: config.Workspace{Name: "test-city"},
		Agents: []config.Agent{{
			Name:         "worker",
			StartCommand: "true",
			Upstream:     "undeclared",
		}},
	}
	bead := env.createSessionBead("worker", "worker")
	if err := env.store.SetMetadataBatch(bead.ID, session.RequestExplicitWakePatch(string(session.WakeCauseExplicit), env.clk.Now())); err != nil {
		t.Fatalf("request explicit wake: %v", err)
	}

	err := reconcileExactSessionStart(context.Background(), sessionStartAdmission{
		SessionID: bead.ID,
		Source:    sessionStartAdmissionExplicitWake,
	}, exactSessionStartTestParams(t, env))
	if err == nil {
		t.Fatal("expected undeclared upstream resolution to fail")
	}
	if got := env.sp.CountCalls("Start", "worker"); got != 0 {
		t.Fatalf("provider Start calls = %d, want 0", got)
	}
	got := env.sessionInfo(bead.ID)
	if got.WakeRequest != string(session.WakeCauseExplicit) {
		t.Fatalf("wake request = %q, want preserved explicit wake", got.WakeRequest)
	}
	if got.LastWokeAt != "" {
		t.Fatalf("last_woke_at = %q, want no pre-wake mutation", got.LastWokeAt)
	}
}

func TestReconcileExactSessionStartParksUnhealthyProviderWithoutMutation(t *testing.T) {
	cityPath := t.TempDir()
	writeHealthCache(t, cityPath, "provider-red", "unhealthy", nowSecs())
	env := newReconcilerTestEnv()
	env.cfg = &config.City{
		Workspace: config.Workspace{Name: "test-city"},
		Providers: map[string]config.ProviderSpec{
			"provider-red": {Command: "true"},
		},
		Agents: []config.Agent{{
			Name:     "worker",
			Provider: "provider-red",
		}},
	}
	bead := env.createSessionBead("worker", "worker")
	if err := env.store.SetMetadataBatch(bead.ID, session.RequestExplicitWakePatch(string(session.WakeCauseExplicit), env.clk.Now())); err != nil {
		t.Fatalf("request explicit wake: %v", err)
	}
	params := exactSessionStartTestParams(t, env)
	params.CityPath = cityPath

	if err := reconcileExactSessionStart(context.Background(), sessionStartAdmission{
		SessionID: bead.ID,
		Source:    sessionStartAdmissionExplicitWake,
	}, params); err != nil {
		t.Fatalf("reconcileExactSessionStart: %v", err)
	}
	if got := env.sp.CountCalls("Start", "worker"); got != 0 {
		t.Fatalf("provider Start calls = %d, want 0", got)
	}
	got := env.sessionInfo(bead.ID)
	if got.WakeRequest != string(session.WakeCauseExplicit) {
		t.Fatalf("wake request = %q, want parked explicit wake", got.WakeRequest)
	}
	if got.LastWokeAt != "" {
		t.Fatalf("last_woke_at = %q, want no pre-wake mutation", got.LastWokeAt)
	}
}

func TestReconcileExactSessionStartDoesNotEnumerateSessions(t *testing.T) {
	env := newReconcilerTestEnv()
	env.cfg = &config.City{
		Workspace: config.Workspace{Name: "test-city"},
		Agents:    []config.Agent{{Name: "worker", StartCommand: "true"}},
	}
	bead := env.createSessionBead("worker", "worker")
	env.setSessionMetadata(&bead, map[string]string{
		"state":                     string(session.StateCreating),
		"pending_create_claim":      "true",
		"pending_create_started_at": env.clk.Now().UTC().Format(time.RFC3339),
	})
	rejecting := &sessionListRejectingStore{Store: env.store}
	params := exactSessionStartTestParams(t, env)
	params.Store = rejecting

	if err := reconcileExactSessionStart(context.Background(), sessionStartAdmission{
		SessionID: bead.ID,
		Source:    sessionStartAdmissionPendingCreate,
	}, params); err != nil {
		t.Fatalf("reconcileExactSessionStart: %v", err)
	}
	if got := rejecting.sessionListCalls.Load(); got != 0 {
		t.Fatalf("session-enumerating store.List calls = %d, want 0", got)
	}
}

type sessionListRejectingStore struct {
	beads.Store
	sessionListCalls atomic.Int32
}

func (s *sessionListRejectingStore) List(query beads.ListQuery) ([]beads.Bead, error) {
	if query.Label == sessionBeadLabel {
		s.sessionListCalls.Add(1)
		return nil, fmt.Errorf("session enumeration is forbidden on the exact-key path")
	}
	return s.Store.List(query)
}

func TestReconcileExactSessionStartStartsExplicitWakeAndClearsRequest(t *testing.T) {
	env := newReconcilerTestEnv()
	env.cfg = &config.City{
		Workspace: config.Workspace{Name: "test-city"},
		Agents: []config.Agent{{
			Name:         "worker",
			StartCommand: "true",
		}},
	}
	bead := env.createSessionBead("worker", "worker")
	if err := env.store.SetMetadataBatch(bead.ID, session.RequestExplicitWakePatch(string(session.WakeCauseExplicit), env.clk.Now())); err != nil {
		t.Fatalf("request explicit wake: %v", err)
	}

	err := reconcileExactSessionStart(context.Background(), sessionStartAdmission{
		SessionID: bead.ID,
		Source:    sessionStartAdmissionExplicitWake,
	}, exactSessionStartTestParams(t, env))
	if err != nil {
		t.Fatalf("reconcileExactSessionStart: %v", err)
	}
	if !env.sp.IsRunning("worker") {
		t.Fatal("explicit-wake runtime was not started")
	}
	got := env.sessionInfo(bead.ID)
	if got.MetadataState != string(session.StateActive) {
		t.Fatalf("persisted state = %q, want active", got.MetadataState)
	}
	if got.WakeRequest != "" {
		t.Fatalf("durable wake request remained after successful start: %q", got.WakeRequest)
	}
	stored, err := env.store.Get(bead.ID)
	if err != nil {
		t.Fatalf("get started session bead: %v", err)
	}
	if got := stored.Metadata["wake_requested_at"]; got != "" {
		t.Fatalf("wake_requested_at remained after successful start: %q", got)
	}
}

func TestReconcileExactSessionStartIgnoresNonActionableKeys(t *testing.T) {
	tests := []struct {
		name  string
		setup func(*testing.T, *reconcilerTestEnv) string
	}{
		{
			name: "missing",
			setup: func(*testing.T, *reconcilerTestEnv) string {
				return "gcs-missing"
			},
		},
		{
			name: "non-session",
			setup: func(t *testing.T, env *reconcilerTestEnv) string {
				b, err := env.store.Create(beads.Bead{Title: "ordinary work", Type: "task"})
				if err != nil {
					t.Fatalf("create non-session bead: %v", err)
				}
				return b.ID
			},
		},
		{
			name: "closed",
			setup: func(t *testing.T, env *reconcilerTestEnv) string {
				b := env.createSessionBead("worker", "worker")
				if err := env.store.Close(b.ID); err != nil {
					t.Fatalf("close session bead: %v", err)
				}
				return b.ID
			},
		},
		{
			name: "no durable start cause",
			setup: func(_ *testing.T, env *reconcilerTestEnv) string {
				return env.createSessionBead("worker", "worker").ID
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			env := newReconcilerTestEnv()
			env.cfg = &config.City{
				Workspace: config.Workspace{Name: "test-city"},
				Agents:    []config.Agent{{Name: "worker", StartCommand: "true"}},
			}
			id := test.setup(t, env)

			err := reconcileExactSessionStart(context.Background(), sessionStartAdmission{
				SessionID: id,
				Source:    sessionStartAdmissionAntiEntropy,
			}, exactSessionStartTestParams(t, env))
			if err != nil {
				t.Fatalf("reconcileExactSessionStart: %v", err)
			}
			if got := env.sp.CountCalls("Start", "worker"); got != 0 {
				t.Fatalf("provider Start calls = %d, want 0", got)
			}
		})
	}
}

type mutateAfterFirstSessionGetStore struct {
	beads.Store
	target     string
	gets       atomic.Int32
	afterFirst func()
}

func (s *mutateAfterFirstSessionGetStore) Get(id string) (beads.Bead, error) {
	b, err := s.Store.Get(id)
	if err == nil && id == s.target && s.gets.Add(1) == 1 && s.afterFirst != nil {
		s.afterFirst()
	}
	return b, err
}

func exactSessionStartTestParams(t *testing.T, env *reconcilerTestEnv) exactSessionStartParams {
	t.Helper()
	return exactSessionStartParams{
		CityPath: t.TempDir(),
		CityName: "test-city",
		Config:   env.cfg,
		Provider: env.sp,
		Store:    env.store,
		Clock:    env.clk,
		Recorder: events.Discard,
		Stdout:   io.Discard,
		Stderr:   io.Discard,
		StartOptions: []startExecutionOption{
			withStartStabilityWaiter(immediateStartStabilityWaiter),
			withSessionStaleKeyDetectionWaiter(immediateSessionStaleKeyDetectionWaiter),
		},
	}
}
