package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/beads/beadstest"
	"github.com/gastownhall/gascity/internal/config"
	"github.com/gastownhall/gascity/internal/events"
	"github.com/gastownhall/gascity/internal/rollout"
	"github.com/gastownhall/gascity/internal/runtime"
	sessionauto "github.com/gastownhall/gascity/internal/runtime/auto"
	"github.com/gastownhall/gascity/internal/session"
	"github.com/gastownhall/gascity/internal/testutil"
	"github.com/gastownhall/gascity/internal/worker"
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

func TestReconcileExactSessionStartPoolDrainAckTransitionFailureHonorsRolloutMode(t *testing.T) {
	tests := []struct {
		name            string
		authorized      bool
		authorizeErr    error
		withWriter      bool
		writerSetupErr  error
		writerCommitErr error
		wantDiagnostic  string
		wantWriterCalls int
	}{
		{name: "authorization denied", withWriter: true, wantDiagnostic: "authorization no longer holds"},
		{name: "authorization read failed", authorizeErr: errors.New("trigger read unavailable"), withWriter: true, wantDiagnostic: "trigger read unavailable"},
		{name: "conditional writer unavailable", authorized: true, wantDiagnostic: "conditional writer is unavailable"},
		{name: "conditional writer setup failed", authorized: true, withWriter: true, writerSetupErr: errors.New("conditional writer setup failed"), wantDiagnostic: "conditional writer setup failed"},
		{name: "revision CAS lost", authorized: true, withWriter: true, writerCommitErr: errors.New("revision conflict"), wantDiagnostic: "revision conflict", wantWriterCalls: 1},
	}

	for _, mode := range []rollout.Mode{rollout.Auto, rollout.Require} {
		t.Run(string(mode), func(t *testing.T) {
			for _, test := range tests {
				t.Run(test.name, func(t *testing.T) {
					env := newReconcilerTestEnv()
					env.cfg = &config.City{
						Workspace: config.Workspace{Name: "test-city"},
						Agents:    []config.Agent{{Name: "worker", StartCommand: "true"}},
					}
					bead := env.createSessionBead("worker", "worker")
					env.setSessionMetadata(&bead, map[string]string{
						"state":          string(session.StateActive),
						"instance_token": "drain-token",
					})
					before, err := env.store.Get(bead.ID)
					if err != nil {
						t.Fatalf("read pre-reconcile row: %v", err)
					}

					writer := &recordingExactStatusWriter{store: env.store, err: test.writerCommitErr}
					params := exactSessionStartTestParams(t, env)
					params.RolloutMode = mode
					if test.withWriter {
						params.StatusWriter = writer
					}
					params.StatusWriterError = test.writerSetupErr
					params.AuthorizePoolDrainAck = func(session.Info, routedWorkPoolDrainAckLease) (bool, error) {
						return test.authorized, test.authorizeErr
					}
					owner, reconcileErr := reconcileExactSessionStartWithOwner(t.Context(), sessionStartAdmission{
						SessionID: bead.ID,
						Source:    sessionStartAdmissionSocket,
						PoolDrainAck: &routedWorkPoolDrainAckLease{
							SessionID:     bead.ID,
							InstanceToken: "drain-token",
						},
					}, params)
					switch mode {
					case rollout.Auto:
						if owner != exactSessionStartLegacyOwner || !errors.Is(reconcileErr, errSessionStartLegacyFallbackRequired) {
							t.Fatalf("reconcile result = (owner=%v, err=%v), want visible legacy fallback", owner, reconcileErr)
						}
					case rollout.Require:
						if owner != exactSessionStartKeyedOwner || reconcileErr == nil || errors.Is(reconcileErr, errSessionStartLegacyFallbackRequired) {
							t.Fatalf("reconcile result = (owner=%v, err=%v), want visible required refusal", owner, reconcileErr)
						}
					}
					if !strings.Contains(reconcileErr.Error(), test.wantDiagnostic) {
						t.Fatalf("transition diagnostic = %q, want %q", reconcileErr, test.wantDiagnostic)
					}
					if got := len(writer.expected); got != test.wantWriterCalls {
						t.Fatalf("conditional writer calls = %d, want %d", got, test.wantWriterCalls)
					}
					after, err := env.store.Get(bead.ID)
					if err != nil {
						t.Fatalf("read post-reconcile row: %v", err)
					}
					if !reflect.DeepEqual(after, before) {
						t.Fatalf("failed transition mutated row:\nbefore=%+v\nafter=%+v", before, after)
					}
					if got := env.sp.CountCalls("Stop", "worker"); got != 0 {
						t.Fatalf("provider Stop calls = %d, want 0", got)
					}
				})
			}
		})
	}
}

func TestReconcileExactSessionStartPoolDrainAckUsesNegativeRevisionToken(t *testing.T) {
	env := newReconcilerTestEnv()
	env.cfg = &config.City{
		Workspace: config.Workspace{Name: "test-city"},
		Agents:    []config.Agent{{Name: "worker", StartCommand: "true"}},
	}
	bead := env.createSessionBead("worker", "worker")
	env.setSessionMetadata(&bead, map[string]string{
		"state":          string(session.StateActive),
		"instance_token": "drain-token",
	})

	writer := &recordingExactStatusWriter{
		store: env.store,
		err:   &beads.PreconditionFailedError{ID: bead.ID, Expected: -17, Current: -16},
	}
	params := exactSessionStartTestParams(t, env)
	params.RolloutMode = rollout.Auto
	params.Store = negativeRevisionSessionStore{Store: env.store, revision: -17}
	params.StatusWriter = writer
	params.AuthorizePoolDrainAck = func(session.Info, routedWorkPoolDrainAckLease) (bool, error) {
		return true, nil
	}

	owner, reconcileErr := reconcileExactSessionStartWithOwner(t.Context(), sessionStartAdmission{
		SessionID: bead.ID,
		Source:    sessionStartAdmissionSocket,
		PoolDrainAck: &routedWorkPoolDrainAckLease{
			SessionID:     bead.ID,
			InstanceToken: "drain-token",
		},
	}, params)
	if owner != exactSessionStartLegacyOwner || !errors.Is(reconcileErr, errSessionStartLegacyFallbackRequired) {
		t.Fatalf("owner/error = %v/%v, want visible legacy fallback after CAS conflict", owner, reconcileErr)
	}
	if len(writer.expected) != 1 || writer.expected[0] != -17 {
		t.Fatalf("UpdateIfMatch revisions = %v, want [-17]", writer.expected)
	}
	if got := env.sp.CountCalls("Stop", "worker"); got != 0 {
		t.Fatalf("provider Stop calls = %d, want 0", got)
	}
}

func TestDrainAckStopPendingFenceAcceptsNegativeRevisionToken(t *testing.T) {
	drainAt := time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC).Format(time.RFC3339)
	response := session.PersistedResponse{
		Status: "open",
		Metadata: map[string]string{
			"state":                     string(session.StateDraining),
			"state_reason":              session.DrainAckStopPendingReason,
			"drain_at":                  drainAt,
			"pending_create_claim":      "",
			"pending_create_started_at": "",
		},
		Revision: -17,
	}
	info := session.Info{
		ID:            "session-1",
		MetadataState: string(session.StateDraining),
		StateReason:   session.DrainAckStopPendingReason,
		InstanceToken: "drain-token",
	}

	fence := newDrainAckStopPendingFence(response)
	if !fence.matches(info, response, info.ID, info.InstanceToken) {
		t.Fatal("negative nonzero revision did not form a canonical stop-pending fence")
	}
}

func TestReconcileExactSessionStartPoolDrainAckPostCASAuthorizationHonorsRolloutMode(t *testing.T) {
	for _, mode := range []rollout.Mode{rollout.Auto, rollout.Require} {
		t.Run(string(mode), func(t *testing.T) {
			env := newReconcilerTestEnv()
			env.cfg = &config.City{Agents: []config.Agent{{Name: "worker", StartCommand: "true"}}}
			bead := env.createSessionBead("worker", "worker")
			env.setSessionMetadata(&bead, map[string]string{
				"state":                     string(session.StateActive),
				"state_reason":              "before-drain",
				"drain_at":                  "before-drain-at",
				"pending_create_claim":      "true",
				"pending_create_started_at": "before-create-at",
				"instance_token":            "drain-token",
			})
			before, err := env.store.Get(bead.ID)
			if err != nil {
				t.Fatalf("read pre-transition row: %v", err)
			}
			writer, ok := env.store.(beads.ConditionalWriter)
			if !ok {
				t.Fatal("test store does not implement conditional writer")
			}
			params := exactSessionStartTestParams(t, env)
			params.RolloutMode = mode
			params.StatusWriter = writer
			var authorizations int
			params.AuthorizePoolDrainAck = func(session.Info, routedWorkPoolDrainAckLease) (bool, error) {
				authorizations++
				return authorizations == 1, nil
			}

			owner, reconcileErr := reconcileExactSessionStartWithOwner(t.Context(), sessionStartAdmission{
				SessionID: bead.ID,
				Source:    sessionStartAdmissionSocket,
				PoolDrainAck: &routedWorkPoolDrainAckLease{
					SessionID:     bead.ID,
					InstanceToken: "drain-token",
				},
			}, params)
			if authorizations != 2 {
				t.Fatalf("authorization calls = %d, want initial and post-CAS checks", authorizations)
			}
			after, err := env.store.Get(bead.ID)
			if err != nil {
				t.Fatalf("read post-transition row: %v", err)
			}
			switch mode {
			case rollout.Auto:
				if owner != exactSessionStartLegacyOwner || !errors.Is(reconcileErr, errSessionStartLegacyFallbackRequired) {
					t.Fatalf("Auto owner/error = %v/%v, want legacy fenced fallback", owner, reconcileErr)
				}
				for _, key := range drainAckStopPendingRollbackKeys {
					if after.Metadata[key] != before.Metadata[key] {
						t.Fatalf("Auto rollback metadata[%q] = %q, want original %q", key, after.Metadata[key], before.Metadata[key])
					}
				}
			case rollout.Require:
				if owner != exactSessionStartKeyedOwner || reconcileErr == nil || errors.Is(reconcileErr, errSessionStartLegacyFallbackRequired) {
					t.Fatalf("Require owner/error = %v/%v, want parked refusal", owner, reconcileErr)
				}
				if after.Metadata["state"] != string(session.StateDraining) || after.Metadata["state_reason"] != session.DrainAckStopPendingReason {
					t.Fatalf("Require row = %#v, want drain-ack stop-pending", after.Metadata)
				}
			}
			if got := env.sp.CountCalls("Stop", "worker"); got != 0 {
				t.Fatalf("provider Stop calls = %d, want 0", got)
			}
		})
	}
}

func TestReconcileExactSessionStartPoolDrainAckAsyncAuthorizationChangeRollsBackBeforeStop(t *testing.T) {
	env := newReconcilerTestEnv()
	env.cfg = &config.City{Agents: []config.Agent{{Name: "worker", StartCommand: "true"}}}
	bead := env.createSessionBead("worker", "worker")
	env.setSessionMetadata(&bead, map[string]string{
		"state":                     string(session.StateActive),
		"state_reason":              "before-drain",
		"drain_at":                  "before-drain-at",
		"pending_create_claim":      "true",
		"pending_create_started_at": "before-create-at",
		"instance_token":            "drain-token",
	})
	before, err := env.store.Get(bead.ID)
	if err != nil {
		t.Fatalf("read pre-transition row: %v", err)
	}
	provider := &freshLivenessProvider{Fake: env.sp, fresh: runtime.Liveness{Running: true, Alive: true, Complete: true}}
	writer, ok := env.store.(beads.ConditionalWriter)
	if !ok {
		t.Fatal("test store does not implement conditional writer")
	}
	preStopAuthorization := make(chan struct{})
	releasePreStopAuthorization := make(chan struct{})
	completion := make(chan drainAckAsyncStopCompletion, 1)
	params := exactSessionStartTestParams(t, env)
	params.Provider = provider
	params.RolloutMode = rollout.Auto
	params.StatusWriter = writer
	params.AsyncStopTracker = &asyncStartTracker{}
	params.AsyncStopCompletion = func(result drainAckAsyncStopCompletion) { completion <- result }
	var authorizations atomic.Int32
	params.AuthorizePoolDrainAck = func(session.Info, routedWorkPoolDrainAckLease) (bool, error) {
		switch authorizations.Add(1) {
		case 1, 2:
			return true, nil
		case 3:
			close(preStopAuthorization)
			<-releasePreStopAuthorization
			return false, nil
		default:
			return false, errors.New("unexpected further authorization")
		}
	}

	owner, reconcileErr := reconcileExactSessionStartWithOwner(t.Context(), sessionStartAdmission{
		SessionID: bead.ID,
		Source:    sessionStartAdmissionSocket,
		PoolDrainAck: &routedWorkPoolDrainAckLease{
			SessionID:     bead.ID,
			InstanceToken: "drain-token",
		},
	}, params)
	if owner != exactSessionStartKeyedOwner || !errors.Is(reconcileErr, errSessionStartPoolDrainAckPending) {
		t.Fatalf("initial owner/error = %v/%v, want keyed pending", owner, reconcileErr)
	}
	select {
	case <-preStopAuthorization:
	case <-time.After(testutil.GoroutineRaceTimeout):
		t.Fatal("async stop did not reach effect-boundary authorization")
	}
	close(releasePreStopAuthorization)
	select {
	case result := <-completion:
		if result != drainAckAsyncStopYielded {
			t.Fatalf("async completion = %v, want yielded", result)
		}
	case <-time.After(testutil.GoroutineRaceTimeout):
		t.Fatal("async stop did not complete after authorization denial")
	}
	after, err := env.store.Get(bead.ID)
	if err != nil {
		t.Fatalf("read rolled-back row: %v", err)
	}
	for _, key := range drainAckStopPendingRollbackKeys {
		if after.Metadata[key] != before.Metadata[key] {
			t.Fatalf("rollback metadata[%q] = %q, want %q", key, after.Metadata[key], before.Metadata[key])
		}
	}
	if got := provider.CountCalls("Stop", "worker"); got != 0 {
		t.Fatalf("provider Stop calls = %d, want 0", got)
	}
}

func TestReconcileExactSessionStartPoolDrainAckAutoRollbackConflictParks(t *testing.T) {
	env := newReconcilerTestEnv()
	env.cfg = &config.City{Agents: []config.Agent{{Name: "worker", StartCommand: "true"}}}
	bead := env.createSessionBead("worker", "worker")
	env.setSessionMetadata(&bead, map[string]string{"state": string(session.StateActive), "instance_token": "drain-token"})
	writer, ok := env.store.(beads.ConditionalWriter)
	if !ok {
		t.Fatal("test store does not implement conditional writer")
	}
	params := exactSessionStartTestParams(t, env)
	params.RolloutMode = rollout.Auto
	params.StatusWriter = &failSecondConditionalWriter{ConditionalWriter: writer}
	var authorizations int
	params.AuthorizePoolDrainAck = func(session.Info, routedWorkPoolDrainAckLease) (bool, error) {
		authorizations++
		return authorizations == 1, nil
	}

	owner, reconcileErr := reconcileExactSessionStartWithOwner(t.Context(), sessionStartAdmission{
		SessionID: bead.ID,
		Source:    sessionStartAdmissionSocket,
		PoolDrainAck: &routedWorkPoolDrainAckLease{
			SessionID: bead.ID, InstanceToken: "drain-token",
		},
	}, params)
	if owner != exactSessionStartKeyedOwner || reconcileErr == nil || errors.Is(reconcileErr, errSessionStartLegacyFallbackRequired) {
		t.Fatalf("owner/error = %v/%v, want keyed rollback-conflict park", owner, reconcileErr)
	}
	if !isDrainAckStopPendingInfo(env.sessionInfo(bead.ID)) {
		t.Fatal("rollback conflict cleared drain-ack stop-pending marker")
	}
	if got := env.sp.CountCalls("Stop", "worker"); got != 0 {
		t.Fatalf("provider Stop calls = %d, want 0", got)
	}
}

func TestReconcileExactSessionStartPoolDrainAckAmbiguousCASCommitRetainsKeyedOwnership(t *testing.T) {
	env := newReconcilerTestEnv()
	env.cfg = &config.City{Agents: []config.Agent{{Name: "worker", StartCommand: "true"}}}
	bead := env.createSessionBead("worker", "worker")
	env.setSessionMetadata(&bead, map[string]string{"state": string(session.StateActive), "instance_token": "drain-token"})
	writer, ok := env.store.(beads.ConditionalWriter)
	if !ok {
		t.Fatal("test store does not implement conditional writer")
	}
	params := exactSessionStartTestParams(t, env)
	params.RolloutMode = rollout.Auto
	params.Provider = &freshLivenessProvider{Fake: env.sp, fresh: runtime.Liveness{Running: true, Alive: true, Complete: true}}
	params.StatusWriter = &ambiguousCommitConditionalWriter{ConditionalWriter: writer}
	params.AuthorizePoolDrainAck = func(session.Info, routedWorkPoolDrainAckLease) (bool, error) { return true, nil }

	owner, reconcileErr := reconcileExactSessionStartWithOwner(t.Context(), sessionStartAdmission{
		SessionID: bead.ID,
		Source:    sessionStartAdmissionSocket,
		PoolDrainAck: &routedWorkPoolDrainAckLease{
			SessionID: bead.ID, InstanceToken: "drain-token",
		},
	}, params)
	if owner != exactSessionStartKeyedOwner || !errors.Is(reconcileErr, errSessionStartPoolDrainAckPending) {
		t.Fatalf("owner/error = %v/%v, want keyed self-win retained for async stop", owner, reconcileErr)
	}
	if !isDrainAckStopPendingInfo(env.sessionInfo(bead.ID)) {
		t.Fatal("ambiguous committed CAS did not retain drain-ack stop-pending marker")
	}
}

type failSecondConditionalWriter struct {
	beads.ConditionalWriter
	calls atomic.Int32
}

type ambiguousCommitConditionalWriter struct {
	beads.ConditionalWriter
}

func (w *ambiguousCommitConditionalWriter) UpdateIfMatch(id string, revision int64, opts beads.UpdateOpts) error {
	if err := w.ConditionalWriter.UpdateIfMatch(id, revision, opts); err != nil {
		return err
	}
	return errors.New("conditional write committed but acknowledgement was lost")
}

func (w *failSecondConditionalWriter) UpdateIfMatch(id string, revision int64, opts beads.UpdateOpts) error {
	if w.calls.Add(1) == 2 {
		return errors.New("rollback revision conflict")
	}
	return w.ConditionalWriter.UpdateIfMatch(id, revision, opts)
}

func TestReconcileExactSessionStartRecordsSocketCommitAfterDurableStart(t *testing.T) {
	env := newReconcilerTestEnv()
	env.cfg = &config.City{
		Workspace: config.Workspace{Name: "test-city"},
		Agents:    []config.Agent{{Name: "worker", StartCommand: "true"}},
	}
	bead := env.createSessionBead("worker", "worker")
	if err := env.store.SetMetadataBatch(bead.ID, session.RequestExplicitWakePatch(string(session.WakeCauseExplicit), env.clk.Now())); err != nil {
		t.Fatalf("request explicit wake: %v", err)
	}
	params := exactSessionStartTestParams(t, env)
	trace := newSessionReconcilerTraceManager(params.CityPath, params.CityName, io.Discard)
	t.Cleanup(func() { _ = trace.Close() })
	if _, err := newSessionReconcilerTraceArmStore(params.CityPath).upsertArm(TraceArm{
		ScopeType:  TraceArmScopeTemplate,
		ScopeValue: "worker",
		Source:     TraceArmSourceManual,
		Level:      TraceModeDetail,
		ExpiresAt:  time.Now().Add(time.Minute),
	}); err != nil {
		t.Fatalf("arm detail trace: %v", err)
	}
	params.Trace = trace
	admission := sessionStartAdmission{SessionID: bead.ID, Source: sessionStartAdmissionSocket, Version: 7}
	if err := reconcileExactSessionStart(context.Background(), admission, params); err != nil {
		t.Fatalf("reconcile exact socket start: %v", err)
	}
	woken := env.sessionInfo(bead.ID)
	records, err := ReadTraceRecords(traceCityRuntimeDir(params.CityPath), TraceFilter{
		RecordType:  TraceRecordOperation,
		SiteCode:    TraceSiteLifecycleStartCommit,
		SessionName: "worker",
	})
	if err != nil {
		t.Fatalf("read socket commit trace: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("socket commit traces = %#v, want exactly one", records)
	}
	record := records[0]
	if record.Template != "worker" || record.SessionBeadID != bead.ID ||
		record.Fields["admission"] != string(sessionStartAdmissionSocket) ||
		record.Fields["admission_version"] != float64(admission.Version) ||
		record.Fields["generation"] != float64(params.Generation) ||
		record.Fields["session_id"] != bead.ID ||
		record.Fields["instance_token"] != woken.InstanceToken ||
		record.Fields["effect_applied"] != true {
		t.Fatalf("socket commit trace = %#v, want durable socket commit identity", record)
	}
	for _, field := range []string{"duration_ns", "start_call_ns", "zombie_recycle_ns", "state_sync_recovery_ns", "post_start_observe_ns", "commit_refresh_ns"} {
		if _, ok := record.Fields[field]; !ok {
			t.Fatalf("socket commit trace missing %q: %#v", field, record)
		}
	}
	if traceFieldInt(record.Fields["duration_ns"]) <= 0 || traceFieldInt(record.Fields["start_call_ns"]) <= 0 {
		t.Fatalf("socket commit timing = duration_ns:%v start_call_ns:%v, want positive exact timings", record.Fields["duration_ns"], record.Fields["start_call_ns"])
	}
}

func TestReconcileExactSessionStartStatusHealUsesNegativeRevisionToken(t *testing.T) {
	for _, tt := range []struct {
		name      string
		writeErr  error
		wantError bool
	}{
		{name: "applies heal"},
		{
			name:      "stale writer does not overwrite",
			writeErr:  &beads.PreconditionFailedError{ID: "placeholder", Expected: -17, Current: -16},
			wantError: true,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			env := newReconcilerTestEnv()
			env.cfg = &config.City{
				Workspace: config.Workspace{Name: "test-city"},
				Agents:    []config.Agent{{Name: "worker", StartCommand: "true"}},
			}
			bead := env.createSessionBead("worker", "worker")
			if err := env.store.SetMetadata(bead.ID, "wake_request", string(session.WakeCauseExplicit)); err != nil {
				t.Fatalf("configure exact status heal: %v", err)
			}
			provider := &exactStartCachedLivenessProvider{Fake: env.sp}
			if err := provider.Start(context.Background(), "worker", runtime.Config{Command: "true"}); err != nil {
				t.Fatalf("seed live runtime: %v", err)
			}
			before, err := env.store.Get(bead.ID)
			if err != nil {
				t.Fatalf("read session before exact status heal: %v", err)
			}
			conditionalStore, ok := env.store.(beads.ConditionalWriter)
			if !ok {
				t.Fatalf("session store %T does not support conditional writes", env.store)
			}
			writer := &recordingExactStatusWriter{
				ConditionalWriter: conditionalStore,
				store:             env.store,
				err:               tt.writeErr,
			}
			params := exactSessionStartTestParams(t, env)
			params.Generation = 1
			params.Provider = provider
			params.Store = negativeRevisionSessionStore{Store: env.store, revision: -17}
			params.StatusWriter = writer
			var reports []exactSessionLifecycleStatusResult
			params.StartOptions = append(params.StartOptions, withExactSessionLifecycleStatusObserver(func(result exactSessionLifecycleStatusResult) {
				reports = append(reports, result)
			}))
			owner, reconcileErr := reconcileExactSessionStartWithOwner(context.Background(), sessionStartAdmission{
				SessionID: bead.ID,
				Source:    sessionStartAdmissionInProcess,
				Version:   1,
			}, params)
			if owner != exactSessionStartKeyedOwner {
				t.Fatalf("owner = %v, want keyed", owner)
			}
			if tt.wantError != (reconcileErr != nil) {
				t.Fatalf("reconcile error = %v, want error=%t", reconcileErr, tt.wantError)
			}
			if len(writer.expected) != 1 || writer.expected[0] != -17 {
				t.Fatalf("UpdateIfMatch revisions = %v, want [-17]; status reports=%#v invalidations=%v", writer.expected, reports, provider.invalidations)
			}
			after, err := env.store.Get(bead.ID)
			if err != nil {
				t.Fatalf("read session after exact status heal: %v", err)
			}
			if tt.wantError {
				if !reflect.DeepEqual(after, before) {
					t.Fatalf("stale status writer overwrote session:\nbefore=%#v\nafter=%#v", before, after)
				}
				return
			}
			if after.Metadata["state"] != string(session.StateAwake) {
				t.Fatalf("healed state = %q, want awake", after.Metadata["state"])
			}
		})
	}
}

type negativeRevisionSessionStore struct {
	beads.Store
	revision int64
}

func (s negativeRevisionSessionStore) Get(id string) (beads.Bead, error) {
	bead, err := s.Store.Get(id)
	if err == nil {
		bead.Revision = s.revision
	}
	return bead, err
}

type recordingExactStatusWriter struct {
	beads.ConditionalWriter
	store    beads.Store
	err      error
	expected []int64
}

func (w *recordingExactStatusWriter) UpdateIfMatch(id string, revision int64, opts beads.UpdateOpts) error {
	w.expected = append(w.expected, revision)
	if w.err != nil {
		return w.err
	}
	return w.store.Update(id, opts)
}

func TestResolveExactSessionStartTemplateEmptyPromptDoesNotInvokeGit(t *testing.T) {
	env := newReconcilerTestEnv()
	env.cfg = &config.City{
		Workspace: config.Workspace{Name: "test-city"},
		Agents:    []config.Agent{{Name: "worker", StartCommand: "true"}},
	}
	bead := env.createSessionBead("worker", "worker")
	params := exactSessionStartTestParams(t, env)
	params.CityPath = t.TempDir()
	env.cfg.Agents[0].WorkDir = params.CityPath
	marker := filepath.Join(t.TempDir(), "git-invoked")
	fakeGitDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(fakeGitDir, "git"), []byte("#!/bin/sh\nprintf 'git\\n' >> \"$GC_TEST_GIT_MARKER\"\n"), 0o755); err != nil {
		t.Fatalf("write fake git: %v", err)
	}
	t.Setenv("GC_TEST_GIT_MARKER", marker)
	t.Setenv("PATH", fakeGitDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	gitPath, err := exec.LookPath("git")
	if err != nil {
		t.Fatalf("find fake git: %v", err)
	}
	if want := filepath.Join(fakeGitDir, "git"); gitPath != want {
		t.Fatalf("git path = %q, want fake %q", gitPath, want)
	}

	tp, err := resolveExactSessionStartTemplate(params, env.sessionInfo(bead.ID), &env.cfg.Agents[0], env.clk, io.Discard)
	if err != nil {
		t.Fatalf("resolve exact session start template: %v", err)
	}
	if tp.Command != "true" || tp.SessionName != "worker" {
		t.Fatalf("template = %+v, want worker true command", tp)
	}
	if tp.WorkDir != params.CityPath {
		t.Fatalf("template workdir = %q, want existing city path %q", tp.WorkDir, params.CityPath)
	}
	if calls, err := os.ReadFile(marker); err == nil {
		t.Fatalf("empty-prompt exact template resolution invoked git:\n%s", calls)
	} else if !os.IsNotExist(err) {
		t.Fatalf("read fake git marker: %v", err)
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

	// One authoritative admission read, one pre-wake freshness read, one final
	// live fence immediately before provider.Start, one post-start freshness
	// read, and the authoritative refresh after each of the pre-wake and final
	// commit writes are the only backing reads needed. Runtime observation must
	// reuse the admission's loaded session record.
	const wantGets = 6
	if got := counting.count(); got != wantGets {
		t.Fatalf("cold-cache exact start issued %d backing store.Get calls, want %d", got, wantGets)
	}
}

func TestGetAuthoritativeSessionStartRecordReturnsSameLiveReadRevision(t *testing.T) {
	env := newReconcilerTestEnv()
	bead := env.createSessionBead("worker", "worker")
	store := newExactStatusCountingStore(t, env.store)
	store.rewriteGet = func(gets int, id string, got beads.Bead, err error) (beads.Bead, error) {
		if err == nil && id == bead.ID {
			if gets == 1 {
				got.Revision = 101
			} else {
				got.Revision = 202
			}
		}
		return got, err
	}

	info, revision, err := getAuthoritativeSessionStartRecord(store, bead.ID)
	if err != nil {
		t.Fatalf("get authoritative record: %v", err)
	}
	if info.ID != bead.ID || revision != 101 || store.gets != 1 {
		t.Fatalf("record = (%q, %d), live gets = %d; want (%q, 101), one", info.ID, revision, store.gets, bead.ID)
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

func TestPlanExactSessionWaitDependencyStartShadowReadsAndObservesOnlyReadySession(t *testing.T) {
	env := newReconcilerTestEnv()
	env.cfg = &config.City{
		Workspace: config.Workspace{Name: "test-city"},
		Agents: []config.Agent{{
			Name:         "worker",
			StartCommand: "true",
		}},
	}
	ready := env.createSessionBead("worker", "worker")
	env.setSessionMetadata(&ready, map[string]string{
		"state":                     string(session.StateCreating),
		"pending_create_claim":      "true",
		"pending_create_started_at": env.clk.Now().UTC().Format(time.RFC3339),
	})
	unrelated := env.createSessionBead("unrelated", "worker")

	store := &getCountingStore{Store: env.store}
	params := exactSessionStartTestParams(t, env)
	params.Store = store
	var observed []string
	params.ObserveLoadedSession = func(_ context.Context, _ string, _ beads.Store, _ runtime.Provider, _ *config.City, info session.Info, _ []string) (worker.LiveObservation, error) {
		observed = append(observed, info.ID)
		return worker.LiveObservation{}, nil
	}

	plan, err := planExactSessionWaitDependencyStartShadow(t.Context(), ready.ID, params)
	if err != nil {
		t.Fatalf("planExactSessionWaitDependencyStartShadow: %v", err)
	}
	if plan.Outcome != sessionLifecycleStartSelectionPrepare || plan.Reason != sessionLifecycleStartSelectionReasonReady {
		t.Fatalf("plan = %+v, want ready prepare", plan)
	}
	if got := observed; len(got) != 1 || got[0] != ready.ID {
		t.Fatalf("observed sessions = %v, want [%q]", got, ready.ID)
	}
	if got := store.count(); got != 1 {
		t.Fatalf("authoritative Get calls = %d, want 1", got)
	}
	if got := env.sp.CountCalls("Start", "worker"); got != 0 {
		t.Fatalf("provider Start calls = %d, want 0", got)
	}
	if got := env.sessionInfo(unrelated.ID); got.ID != unrelated.ID {
		t.Fatalf("unrelated session %q was not retained", unrelated.ID)
	}
}

func TestPlanExactSessionWaitDependencyStartShadowStopsBeforeReadWhenCanceled(t *testing.T) {
	env := newReconcilerTestEnv()
	env.cfg = &config.City{Agents: []config.Agent{{Name: "worker", StartCommand: "true"}}}
	bead := env.createSessionBead("worker", "worker")
	store := &getCountingStore{Store: env.store}
	params := exactSessionStartTestParams(t, env)
	params.Store = store
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	plan, err := planExactSessionWaitDependencyStartShadow(ctx, bead.ID, params)
	if err != nil {
		t.Fatalf("planExactSessionWaitDependencyStartShadow: %v", err)
	}
	if plan.Outcome != sessionLifecycleStartSelectionPark || plan.Reason != sessionLifecycleStartSelectionReasonRuntimeUnknown {
		t.Fatalf("plan = %+v, want runtime-unknown park", plan)
	}
	if got := store.count(); got != 0 {
		t.Fatalf("authoritative Get calls after cancellation = %d, want 0", got)
	}
}

func TestPlanExactSessionWaitDependencyStartShadowParksSuspendedTargetWithoutObservation(t *testing.T) {
	env := newReconcilerTestEnv()
	env.cfg = &config.City{Agents: []config.Agent{{Name: "worker", StartCommand: "true", Suspended: true}}}
	bead := env.createSessionBead("worker", "worker")
	env.setSessionMetadata(&bead, map[string]string{
		"state":                     string(session.StateCreating),
		"pending_create_claim":      "true",
		"pending_create_started_at": env.clk.Now().UTC().Format(time.RFC3339),
	})
	params := exactSessionStartTestParams(t, env)
	observed := false
	params.ObserveLoadedSession = func(context.Context, string, beads.Store, runtime.Provider, *config.City, session.Info, []string) (worker.LiveObservation, error) {
		observed = true
		return worker.LiveObservation{}, nil
	}

	plan, err := planExactSessionWaitDependencyStartShadow(t.Context(), bead.ID, params)
	if err != nil {
		t.Fatalf("planExactSessionWaitDependencyStartShadow: %v", err)
	}
	if plan.Outcome != sessionLifecycleStartSelectionPark || plan.Reason != sessionLifecycleStartSelectionReasonRuntimeUnknown {
		t.Fatalf("plan = %+v, want runtime-unknown park", plan)
	}
	if observed {
		t.Fatal("suspended target was observed")
	}
}

func TestPlanExactSessionWaitDependencyStartShadowDoesNotInstallHooksOrRouteACP(t *testing.T) {
	env := newReconcilerTestEnv()
	cityPath := t.TempDir()
	env.cfg = &config.City{
		Workspace: config.Workspace{InstallAgentHooks: []string{"claude"}},
		Agents: []config.Agent{{
			Name: "worker", StartCommand: "true", Session: config.SessionTransportACP,
		}},
	}
	bead := env.createSessionBead("worker", "worker")
	env.setSessionMetadata(&bead, map[string]string{
		"state": string(session.StateCreating), "pending_create_claim": "true",
		"pending_create_started_at": env.clk.Now().UTC().Format(time.RFC3339),
	})
	defaultProvider, acpProvider := runtime.NewFake(), runtime.NewFake()
	provider := sessionauto.New(defaultProvider, acpProvider)
	before, err := os.ReadDir(cityPath)
	if err != nil {
		t.Fatal(err)
	}
	params := exactSessionStartTestParams(t, env)
	params.CityPath, params.Provider = cityPath, provider
	recording := beadstest.NewRecordingStore(env.store)
	recording.Reset()
	params.Store = recording
	recorder := events.NewFake()
	params.Recorder = recorder

	if _, err := planExactSessionWaitDependencyStartShadow(t.Context(), bead.ID, params); err != nil {
		t.Fatalf("planExactSessionWaitDependencyStartShadow: %v", err)
	}
	after, err := os.ReadDir(cityPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != len(before) {
		t.Fatalf("shadow planner created files: before=%v after=%v", before, after)
	}
	if got := defaultProvider.CountCalls("IsRunning", "worker"); got == 0 {
		t.Fatal("shadow liveness did not observe the default backend first")
	}
	if got := defaultProvider.CountCalls("Start", "worker"); got != 0 {
		t.Fatalf("default Start calls = %d, want 0", got)
	}
	if got := acpProvider.CountCalls("Start", "worker"); got != 0 {
		t.Fatalf("ACP Start calls = %d, want 0", got)
	}
	if calls := recording.Calls(); len(calls) != 0 {
		t.Fatalf("store effects = %#v, want none", calls)
	}
	if got := len(recorder.Events); got != 0 {
		t.Fatalf("emitted events = %d, want 0", got)
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
