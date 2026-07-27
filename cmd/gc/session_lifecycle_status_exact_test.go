package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/beads/beadstest"
	"github.com/gastownhall/gascity/internal/config"
	"github.com/gastownhall/gascity/internal/runtime"
	"github.com/gastownhall/gascity/internal/session"
	"github.com/gastownhall/gascity/internal/worker"
)

func TestEvaluateExactSessionLifecycleStatus(t *testing.T) {
	now := time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)
	base := exactSessionLifecycleStatusInput{
		Admission:            sessionStartAdmission{SessionID: "gcs-exact", Version: 4},
		ControllerGeneration: 9,
		RequestedID:          "gcs-exact",
		Info: session.Info{
			ID:            "gcs-exact",
			State:         session.StateAwake,
			MetadataState: string(session.StateAwake),
			SessionKey:    "resume-key",
		},
		Observation:        worker.LiveObservation{},
		ObservedAt:         now,
		PrerequisitesReady: true,
	}

	tests := []struct {
		name             string
		input            exactSessionLifecycleStatusInput
		wantReason       exactSessionLifecycleStatusReason
		wantPlan         bool
		mutatePlan       bool
		rollbackDiverges bool
	}{
		{
			name:       "all three status contexts agree",
			input:      base,
			wantReason: exactSessionLifecycleStatusReasonCandidate,
			wantPlan:   true,
			mutatePlan: true,
		},
		{
			name: "alive and running disagree",
			input: func() exactSessionLifecycleStatusInput {
				in := base
				in.Observation = worker.LiveObservation{Running: true, Alive: false}
				return in
			}(),
			wantReason: exactSessionLifecycleStatusReasonContextAmbiguous,
		},
		{
			name: "rollback sensitive creating state parks",
			input: func() exactSessionLifecycleStatusInput {
				in := base
				in.Info = session.Info{
					ID:                     "gcs-exact",
					State:                  session.StateCreating,
					MetadataState:          string(session.StateCreating),
					SessionKey:             "resume-key",
					CreatedAt:              now.Add(-20 * time.Minute),
					PendingCreateClaim:     true,
					PendingCreateStartedAt: now.Add(-20 * time.Minute).Format(time.RFC3339),
					LastWokeAt:             now.Add(-20 * time.Minute).Format(time.RFC3339),
				}
				in.StartupTimeout = 5 * time.Minute
				return in
			}(),
			wantReason:       exactSessionLifecycleStatusReasonContextAmbiguous,
			rollbackDiverges: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.rollbackDiverges {
				withRollback := planSessionLifecycleStatus(sessionLifecycleShadowInput{
					Info:              tt.input.Info,
					RuntimeObserved:   true,
					RuntimeAlive:      tt.input.Observation.Running,
					ObservedAt:        tt.input.ObservedAt,
					StartupTimeout:    tt.input.StartupTimeout,
					RollbackAvailable: true,
				})
				withoutRollback := planSessionLifecycleStatus(sessionLifecycleShadowInput{
					Info:              tt.input.Info,
					RuntimeObserved:   true,
					RuntimeAlive:      tt.input.Observation.Running,
					ObservedAt:        tt.input.ObservedAt,
					StartupTimeout:    tt.input.StartupTimeout,
					RollbackAvailable: false,
				})
				if sameExactSessionLifecycleStatusPlan(withRollback, withoutRollback) {
					t.Fatalf("rollback fixture does not diverge: with=%#v without=%#v", withRollback, withoutRollback)
				}
			}
			got := evaluateExactSessionLifecycleStatus(tt.input)
			wantDisposition := exactSessionLifecycleStatusDispositionPark
			if tt.wantPlan {
				wantDisposition = exactSessionLifecycleStatusDispositionCandidate
			}
			if got.Disposition != wantDisposition || got.Reason != tt.wantReason || (got.Plan != nil) != tt.wantPlan {
				t.Fatalf("evaluation = %#v, want disposition=%q reason=%q plan=%t", got, wantDisposition, tt.wantReason, tt.wantPlan)
			}
			if got.Admission != tt.input.Admission || got.AdmissionVersion != tt.input.Admission.Version || got.ControllerGeneration != tt.input.ControllerGeneration || got.RequestedID != tt.input.RequestedID || got.LoadedID != tt.input.Info.ID || !got.ObservedAt.Equal(now) || got.ComparedToLegacy {
				t.Fatalf("evaluation identity = %#v, want detached exact context and no legacy comparison", got)
			}
			if tt.mutatePlan {
				got.Plan.Patch["state"] = "corrupt"
				again := evaluateExactSessionLifecycleStatus(tt.input)
				if reflect.DeepEqual(got.Plan, again.Plan) || again.Plan.Patch["state"] == "corrupt" {
					t.Fatalf("returned plan was not detached: first=%#v second=%#v", got.Plan, again.Plan)
				}
			}
		})
	}
}

func TestEvaluateExactSessionLifecycleStatusClosedAndUnavailable(t *testing.T) {
	base := exactSessionLifecycleStatusInput{
		Admission:            sessionStartAdmission{SessionID: "gcs-exact", Version: 4},
		ControllerGeneration: 9,
		RequestedID:          "gcs-exact",
		Info:                 session.Info{ID: "gcs-exact"},
	}
	closed := base
	closed.Info.Closed = true
	if got := evaluateExactSessionLifecycleStatus(closed); got.Disposition != exactSessionLifecycleStatusDispositionCandidate || got.Plan == nil || got.Plan.Outcome != sessionLifecycleStatusNoop || got.Plan.Reason != sessionLifecycleStatusReasonTerminal {
		t.Fatalf("closed result = %#v, want terminal noop candidate", got)
	}
	unavailable := base
	unavailable.UnavailableReason = exactSessionLifecycleStatusReasonPrerequisiteUnavailable
	if got := evaluateExactSessionLifecycleStatus(unavailable); got.Disposition != exactSessionLifecycleStatusDispositionPark || got.Reason != exactSessionLifecycleStatusReasonPrerequisiteUnavailable {
		t.Fatalf("unavailable result = %#v, want prerequisite-unavailable park", got)
	}
}

func TestReconcileExactSessionStartReportsWrappedIDCollision(t *testing.T) {
	env := newReconcilerTestEnv()
	env.cfg = &config.City{Agents: []config.Agent{{Name: "worker"}}}
	collision := fmt.Errorf("live read: %w", beads.ErrIDCollision)
	before := exactStatusStoreState(t, env.store)
	store := newExactStatusCountingStore(t, env.store)
	store.rewriteGet = func(_ int, _ string, _ beads.Bead, _ error) (beads.Bead, error) {
		return beads.Bead{}, collision
	}
	var reports []exactSessionLifecycleStatusResult
	params := exactSessionStartTestParams(t, env)
	params.Generation = 1
	params.Store = store
	params.StartOptions = append(params.StartOptions, withExactSessionLifecycleStatusObserver(func(result exactSessionLifecycleStatusResult) {
		reports = append(reports, result)
	}))
	owner, err := reconcileExactSessionStartWithOwner(context.Background(), sessionStartAdmission{
		SessionID: "gcs-collision", Source: sessionStartAdmissionSocket, Version: 8,
	}, params)
	if owner != exactSessionStartUnowned || !errors.Is(err, beads.ErrIDCollision) {
		t.Fatalf("owner/error = %d/%v, want unowned/wrapped ErrIDCollision", owner, err)
	}
	if len(reports) != 1 || reports[0].Reason != exactSessionLifecycleStatusReasonInvalidInput || !strings.Contains(reports[0].Error, collision.Error()) {
		t.Fatalf("collision reports = %#v, want one invalid_input with in-memory error", reports)
	}
	if store.lists != 0 {
		t.Fatalf("collision list calls = %d, want 0", store.lists)
	}
	requireExactStatusStoreUnchanged(t, before, store)
	if calls := env.sp.SnapshotCalls(); len(calls) != 0 {
		t.Fatalf("collision provider calls = %#v, want none", calls)
	}
}

func TestReconcileExactSessionStartReportsLoadedEarlyExitsOnce(t *testing.T) {
	tests := []struct {
		name       string
		configure  func(*reconcilerTestEnv)
		metadata   map[string]string
		observeErr error
		wantOwner  exactSessionStartOwner
		wantReason exactSessionLifecycleStatusReason
		wantErr    bool
		wantDetail string
	}{
		{name: "empty template", configure: func(env *reconcilerTestEnv) {
			env.cfg = &config.City{}
		}, metadata: map[string]string{"template": "", "wake_request": string(session.WakeCauseExplicit)}, wantOwner: exactSessionStartKeyedOwner, wantReason: exactSessionLifecycleStatusReasonPrerequisiteUnavailable, wantErr: true, wantDetail: "persisted template is empty"},
		{name: "missing agent uses legacy helper", configure: func(env *reconcilerTestEnv) {
			env.cfg = &config.City{}
		}, metadata: map[string]string{"wake_request": string(session.WakeCauseExplicit)}, wantOwner: exactSessionStartLegacyOwner, wantReason: exactSessionLifecycleStatusReasonPrerequisiteUnavailable},
		{name: "suspended", configure: func(env *reconcilerTestEnv) {
			env.cfg = &config.City{Agents: []config.Agent{{Name: "worker", StartCommand: "true", Suspended: true}}}
		}, metadata: map[string]string{"wake_request": string(session.WakeCauseExplicit)}, wantOwner: exactSessionStartKeyedOwner, wantReason: exactSessionLifecycleStatusReasonNotObserved},
		{name: "held", configure: func(env *reconcilerTestEnv) {
			env.cfg = &config.City{Agents: []config.Agent{{Name: "worker", StartCommand: "true"}}}
		}, metadata: map[string]string{"wake_request": string(session.WakeCauseExplicit), "held_until": "2030-01-01T00:00:00Z"}, wantOwner: exactSessionStartKeyedOwner, wantReason: exactSessionLifecycleStatusReasonNotObserved},
		{name: "quarantined", configure: func(env *reconcilerTestEnv) {
			env.cfg = &config.City{Agents: []config.Agent{{Name: "worker", StartCommand: "true"}}}
		}, metadata: map[string]string{"state": string(session.StateQuarantined), "wake_request": string(session.WakeCauseExplicit), "quarantined_until": "2030-01-01T00:00:00Z"}, wantOwner: exactSessionStartKeyedOwner, wantReason: exactSessionLifecycleStatusReasonNotObserved},
		{name: "template resolution error", configure: func(env *reconcilerTestEnv) {
			env.cfg = &config.City{Agents: []config.Agent{{Name: "worker", StartCommand: "true", Upstream: "undeclared"}}}
		}, metadata: map[string]string{"wake_request": string(session.WakeCauseExplicit)}, wantOwner: exactSessionStartKeyedOwner, wantReason: exactSessionLifecycleStatusReasonPrerequisiteUnavailable, wantErr: true, wantDetail: "undeclared"},
		{name: "observation error", configure: func(env *reconcilerTestEnv) {
			env.cfg = &config.City{Agents: []config.Agent{{Name: "worker", StartCommand: "true"}}}
		}, metadata: map[string]string{"wake_request": string(session.WakeCauseExplicit)}, observeErr: errors.New("observe unavailable"), wantOwner: exactSessionStartKeyedOwner, wantReason: exactSessionLifecycleStatusReasonObservationUnavailable, wantErr: true, wantDetail: "observe unavailable"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := newReconcilerTestEnv()
			tt.configure(env)
			bead := env.createSessionBead("worker", "worker")
			env.setSessionMetadata(&bead, tt.metadata)
			params := exactSessionStartTestParams(t, env)
			params.Generation = 1
			if tt.observeErr != nil {
				params.ObserveLoadedSession = func(context.Context, string, beads.Store, runtime.Provider, *config.City, session.Info, []string) (worker.LiveObservation, error) {
					return worker.LiveObservation{}, tt.observeErr
				}
			}
			var reports []exactSessionLifecycleStatusResult
			params.StartOptions = append(params.StartOptions, withExactSessionLifecycleStatusObserver(func(result exactSessionLifecycleStatusResult) {
				reports = append(reports, result)
			}))
			owner, err := reconcileExactSessionStartWithOwner(context.Background(), sessionStartAdmission{SessionID: bead.ID, Source: sessionStartAdmissionExplicitWake, Version: 2}, params)
			if owner != tt.wantOwner || (err != nil) != tt.wantErr {
				t.Fatalf("owner/error = %d/%v, want %d/error=%t", owner, err, tt.wantOwner, tt.wantErr)
			}
			if len(reports) != 1 || reports[0].Reason != tt.wantReason || !reports[0].ObservedAt.IsZero() {
				t.Fatalf("reports = %#v, want one %s with zero timestamp", reports, tt.wantReason)
			}
			if tt.wantDetail != "" && (!strings.Contains(reports[0].Error, tt.wantDetail) || !strings.Contains(err.Error(), tt.wantDetail)) {
				t.Fatalf("report/acting error = %q/%v, want detail %q", reports[0].Error, err, tt.wantDetail)
			}
		})
	}
}

func TestReconcileExactSessionStartReportsNonClosedUnownedLoadedExitOnce(t *testing.T) {
	env := newReconcilerTestEnv()
	env.cfg = &config.City{Agents: []config.Agent{{Name: "worker", StartCommand: "true"}}}
	bead := env.createSessionBead("worker", "worker")
	var reports []exactSessionLifecycleStatusResult
	params := exactSessionStartTestParams(t, env)
	params.Generation = 1
	params.StartOptions = append(params.StartOptions, withExactSessionLifecycleStatusObserver(func(result exactSessionLifecycleStatusResult) {
		reports = append(reports, result)
	}))

	owner, err := reconcileExactSessionStartWithOwner(context.Background(), sessionStartAdmission{
		SessionID: bead.ID, Source: sessionStartAdmissionSocket, Version: 1,
	}, params)
	if err != nil || owner != exactSessionStartUnowned {
		t.Fatalf("owner/error = %d/%v, want unowned/nil", owner, err)
	}
	if len(reports) != 1 || reports[0].Reason != exactSessionLifecycleStatusReasonCandidate || reports[0].LoadedID != bead.ID {
		t.Fatalf("reports = %#v, want one candidate report for %q", reports, bead.ID)
	}
}

func TestReconcileExactSessionStartReportsPostObservationExitOnce(t *testing.T) {
	tests := []struct {
		name        string
		metadata    map[string]string
		observation worker.LiveObservation
		rewriteGet  func(int, string, beads.Bead, error) (beads.Bead, error)
		wantOwner   exactSessionStartOwner
		wantGets    int
	}{
		{
			name:        "no-op",
			metadata:    map[string]string{"wake_request": string(session.WakeCauseExplicit), "state": string(session.StateAwake), "session_key": "resume"},
			observation: worker.LiveObservation{Running: true, Alive: true},
			wantOwner:   exactSessionStartKeyedOwner,
			wantGets:    1,
		},
		{
			name: "superseded before pre-wake commit",
			metadata: map[string]string{
				"state":                     string(session.StateCreating),
				"pending_create_claim":      "true",
				"pending_create_started_at": time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC).Format(time.RFC3339),
			},
			rewriteGet: func(gets int, _ string, bead beads.Bead, err error) (beads.Bead, error) {
				if err == nil && gets == 2 {
					bead.Status = "closed"
				}
				return bead, err
			},
			wantOwner: exactSessionStartUnowned,
			wantGets:  2,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := newReconcilerTestEnv()
			env.cfg = &config.City{Agents: []config.Agent{{Name: "worker", StartCommand: "true"}}}
			bead := env.createSessionBead("worker", "worker")
			env.setSessionMetadata(&bead, tt.metadata)
			before := exactStatusStoreState(t, env.store)
			store := newExactStatusCountingStore(t, env.store)
			store.rewriteGet = tt.rewriteGet
			var reports []exactSessionLifecycleStatusResult
			params := exactSessionStartTestParams(t, env)
			params.Generation = 1
			params.Store = store
			observations := 0
			params.ObserveLoadedSession = func(context.Context, string, beads.Store, runtime.Provider, *config.City, session.Info, []string) (worker.LiveObservation, error) {
				observations++
				return tt.observation, nil
			}
			params.StartOptions = append(params.StartOptions, withExactSessionLifecycleStatusObserver(func(result exactSessionLifecycleStatusResult) {
				reports = append(reports, result)
			}))

			owner, err := reconcileExactSessionStartWithOwner(context.Background(), sessionStartAdmission{
				SessionID: bead.ID, Source: sessionStartAdmissionExplicitWake, Version: 1,
			}, params)
			if err != nil || owner != tt.wantOwner {
				t.Fatalf("owner/error = %d/%v, want %d/nil", owner, err, tt.wantOwner)
			}
			if observations != 1 || store.gets != tt.wantGets || store.lists != 0 || env.sp.CountCalls("Start", "worker") != 0 {
				t.Fatalf("observations/get/list/start = %d/%d/%d/%d, want 1/%d/0/0", observations, store.gets, store.lists, env.sp.CountCalls("Start", "worker"), tt.wantGets)
			}
			requireExactStatusStoreUnchanged(t, before, store)
			if len(reports) != 1 || reports[0].RequestedID != bead.ID || reports[0].LoadedID != bead.ID || reports[0].ObservedAt.IsZero() || reports[0].Error != "" {
				t.Fatalf("reports = %#v, want one post-observation report for %q", reports, bead.ID)
			}
		})
	}
}

func TestReconcileExactSessionStartDeliversKeyedReportAfterEffects(t *testing.T) {
	env := newReconcilerTestEnv()
	env.cfg = &config.City{Agents: []config.Agent{{Name: "worker", StartCommand: "true"}}}
	bead := env.createSessionBead("worker", "worker")
	env.setSessionMetadata(&bead, map[string]string{
		"state":                     string(session.StateCreating),
		"pending_create_claim":      "true",
		"pending_create_started_at": env.clk.Now().UTC().Format(time.RFC3339),
	})
	params := exactSessionStartTestParams(t, env)
	params.Generation = 1
	params.StartOptions = append(params.StartOptions, withExactSessionLifecycleStatusObserver(func(exactSessionLifecycleStatusResult) {
		if got := env.sp.CountCalls("Start", "worker"); got != 1 {
			t.Fatalf("observer saw Start calls = %d, want acting effect complete", got)
		}
		if got := env.sessionInfo(bead.ID).MetadataState; got != string(session.StateActive) {
			t.Fatalf("observer saw persisted state = %q, want active", got)
		}
	}))
	if _, err := reconcileExactSessionStartWithOwner(context.Background(), sessionStartAdmission{SessionID: bead.ID, Source: sessionStartAdmissionPendingCreate, Version: 1}, params); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
}

func TestReconcileExactSessionStartObserverPanicPreservesActingResult(t *testing.T) {
	observationErr := errors.New("runtime observation failed")
	env := newReconcilerTestEnv()
	env.cfg = &config.City{Agents: []config.Agent{{Name: "worker", StartCommand: "true"}}}
	bead := env.createSessionBead("worker", "worker")
	env.setSessionMetadata(&bead, map[string]string{"wake_request": string(session.WakeCauseExplicit)})
	params := exactSessionStartTestParams(t, env)
	params.Generation = 1
	params.ObserveLoadedSession = func(context.Context, string, beads.Store, runtime.Provider, *config.City, session.Info, []string) (worker.LiveObservation, error) {
		return worker.LiveObservation{}, observationErr
	}
	var stderr bytes.Buffer
	params.Stderr = &stderr
	params.StartOptions = append(params.StartOptions, withExactSessionLifecycleStatusObserver(func(exactSessionLifecycleStatusResult) {
		panicExactSessionLifecycleStatusObserver(128)
	}))

	const admissionVersion = 11
	owner, err := reconcileExactSessionStartWithOwner(context.Background(), sessionStartAdmission{
		SessionID: bead.ID,
		Source:    sessionStartAdmissionExplicitWake,
		Version:   admissionVersion,
	}, params)
	if owner != exactSessionStartKeyedOwner || !errors.Is(err, observationErr) || !strings.Contains(err.Error(), "observing runtime") {
		t.Fatalf("owner/error = %d/%v, want keyed/contextual wrapped observation error", owner, err)
	}
	diagnostic := stderr.String()
	for _, detail := range []string{bead.ID, fmt.Sprintf("version %d", admissionVersion), "status observer exploded", "goroutine"} {
		if !strings.Contains(diagnostic, detail) {
			t.Fatalf("panic diagnostic %q does not contain %q", diagnostic, detail)
		}
	}
	if len(diagnostic) > exactSessionLifecycleStatusPanicDiagnosticLimit {
		t.Fatalf("panic diagnostic length = %d, want <= %d", len(diagnostic), exactSessionLifecycleStatusPanicDiagnosticLimit)
	}
}

func panicExactSessionLifecycleStatusObserver(depth int) {
	if depth == 0 {
		panic("status observer exploded")
	}
	panicExactSessionLifecycleStatusObserver(depth - 1)
}

func TestReconcileExactSessionStartStatusShadowPreservesOwnerAndEffects(t *testing.T) {
	tests := []struct {
		name             string
		setup            func(*reconcilerTestEnv) session.Info
		observe          bool
		wantOwner        exactSessionStartOwner
		wantStarts       int
		wantObservations int
		wantReports      int
		wantGets         int
		observerPanic    bool
	}{
		{
			name: "keyed start reuses observation and starts once",
			setup: func(env *reconcilerTestEnv) session.Info {
				bead := env.createSessionBead("worker", "worker")
				env.setSessionMetadata(&bead, map[string]string{
					"state":                     string(session.StateCreating),
					"pending_create_claim":      "true",
					"pending_create_started_at": env.clk.Now().UTC().Format(time.RFC3339),
				})
				return env.sessionInfo(bead.ID)
			},
			observe: true, wantOwner: exactSessionStartKeyedOwner, wantStarts: 1, wantObservations: 1, wantReports: 1, wantGets: -1,
		},
		{
			name: "legacy owner probes once without starting",
			setup: func(env *reconcilerTestEnv) session.Info {
				env.cfg.Agents[0].DependsOn = []string{"dependency"}
				bead := env.createSessionBead("worker", "worker")
				env.setSessionMetadata(&bead, map[string]string{"wake_request": string(session.WakeCauseExplicit)})
				return env.sessionInfo(bead.ID)
			},
			observe: true, wantOwner: exactSessionStartLegacyOwner, wantObservations: 1, wantReports: 1, wantGets: 1,
		},
		{
			name: "nil observer keeps legacy owner zero probe",
			setup: func(env *reconcilerTestEnv) session.Info {
				env.cfg.Agents[0].DependsOn = []string{"dependency"}
				bead := env.createSessionBead("worker", "worker")
				env.setSessionMetadata(&bead, map[string]string{"wake_request": string(session.WakeCauseExplicit)})
				return env.sessionInfo(bead.ID)
			},
			wantOwner: exactSessionStartLegacyOwner, wantGets: 1,
		},
		{
			name: "closed reports parked without probe",
			setup: func(env *reconcilerTestEnv) session.Info {
				bead := env.createSessionBead("worker", "worker")
				if err := env.store.Close(bead.ID); err != nil {
					t.Fatalf("close session: %v", err)
				}
				return env.sessionInfo(bead.ID)
			},
			observe: true, wantOwner: exactSessionStartUnowned, wantReports: 1, wantGets: 1,
		},
		{
			name: "observer panic preserves legacy owner",
			setup: func(env *reconcilerTestEnv) session.Info {
				env.cfg.Agents[0].DependsOn = []string{"dependency"}
				bead := env.createSessionBead("worker", "worker")
				env.setSessionMetadata(&bead, map[string]string{"wake_request": string(session.WakeCauseExplicit)})
				return env.sessionInfo(bead.ID)
			},
			observe: true, observerPanic: true, wantOwner: exactSessionStartLegacyOwner, wantObservations: 1, wantGets: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := newReconcilerTestEnv()
			env.cfg = &config.City{Workspace: config.Workspace{Name: "test-city"}, Agents: []config.Agent{{Name: "worker", StartCommand: "true"}}}
			info := tt.setup(env)
			store := newExactStatusCountingStore(t, env.store)
			var reports []exactSessionLifecycleStatusResult
			params := exactSessionStartTestParams(t, env)
			params.Generation = 7
			params.Store = store
			observationCalls := 0
			params.ObserveLoadedSession = func(ctx context.Context, cityPath string, store beads.Store, provider runtime.Provider, cfg *config.City, loaded session.Info, processNames []string) (worker.LiveObservation, error) {
				observationCalls++
				return workerObserveLoadedSessionWithRuntimeHintsWithConfig(ctx, cityPath, store, provider, cfg, loaded, processNames)
			}
			if tt.observe {
				params.StartOptions = append(params.StartOptions, withExactSessionLifecycleStatusObserver(func(result exactSessionLifecycleStatusResult) {
					if tt.observerPanic {
						panic("observer panic")
					}
					reports = append(reports, result)
				}))
			}
			owner, err := reconcileExactSessionStartWithOwner(context.Background(), sessionStartAdmission{
				SessionID: info.ID, Source: sessionStartAdmissionExplicitWake, Version: 3,
			}, params)
			if err != nil {
				t.Fatalf("reconcile exact session start: %v", err)
			}
			if owner != tt.wantOwner || env.sp.CountCalls("Start", "worker") != tt.wantStarts {
				t.Fatalf("owner/start calls = %d/%d, want %d/%d", owner, env.sp.CountCalls("Start", "worker"), tt.wantOwner, tt.wantStarts)
			}
			if observationCalls != tt.wantObservations {
				t.Fatalf("loaded runtime observations = %d, want %d", observationCalls, tt.wantObservations)
			}
			if tt.wantGets >= 0 && (store.gets != tt.wantGets || store.lists != 0) {
				t.Fatalf("exact read/list calls = %d/%d, want %d/0", store.gets, store.lists, tt.wantGets)
			}
			if len(reports) != tt.wantReports {
				t.Fatalf("status reports = %d, want %d", len(reports), tt.wantReports)
			}
			for _, report := range reports {
				if report.ComparedToLegacy {
					t.Fatal("exact shadow report claimed legacy comparison")
				}
			}
		})
	}
}

func TestExactSessionStatusShadowOneKeyCostDoesNotGrowWithFleet(t *testing.T) {
	for _, fleetSize := range []int{1, 1000, 10000} {
		t.Run(fmt.Sprintf("fleet-%d", fleetSize), func(t *testing.T) {
			env := newReconcilerTestEnv()
			env.cfg = &config.City{Agents: []config.Agent{{Name: "worker", StartCommand: "true", DependsOn: []string{"dependency"}}}}
			for i := 1; i < fleetSize; i++ {
				env.createSessionBead("worker", fmt.Sprintf("worker-%d", i))
			}
			bead := env.createSessionBead("worker", "worker")
			env.setSessionMetadata(&bead, map[string]string{"wake_request": string(session.WakeCauseExplicit)})
			before := exactStatusStoreState(t, env.store)
			store := newExactStatusCountingStore(t, env.store)
			params := exactSessionStartTestParams(t, env)
			params.Generation = 1
			params.Store = store
			params.StartOptions = append(params.StartOptions, withExactSessionLifecycleStatusObserver(func(exactSessionLifecycleStatusResult) {}))
			observationCalls := 0
			params.ObserveLoadedSession = func(ctx context.Context, cityPath string, store beads.Store, provider runtime.Provider, cfg *config.City, loaded session.Info, processNames []string) (worker.LiveObservation, error) {
				observationCalls++
				return workerObserveLoadedSessionWithRuntimeHintsWithConfig(ctx, cityPath, store, provider, cfg, loaded, processNames)
			}
			owner, err := reconcileExactSessionStartWithOwner(context.Background(), sessionStartAdmission{SessionID: bead.ID, Source: sessionStartAdmissionExplicitWake, Version: 1}, params)
			if err != nil || owner != exactSessionStartLegacyOwner {
				t.Fatalf("reconcile owner/error = %d/%v, want legacy/nil", owner, err)
			}
			if store.gets != 1 || store.lists != 0 || observationCalls != 1 {
				t.Fatalf("fleet %d exact costs get/list/observation = %d/%d/%d, want 1/0/1", fleetSize, store.gets, store.lists, observationCalls)
			}
			requireExactStatusStoreUnchanged(t, before, store)
			for _, call := range env.sp.SnapshotCalls() {
				switch call.Method {
				case "IsRunning", "ProcessAlive", "IsAttached", "GetLastActivity":
				default:
					t.Fatalf("fleet %d provider call = %#v, want read-only observation whitelist", fleetSize, call)
				}
			}
		})
	}
}

func TestExactSessionStatusObserverOnOffCosts(t *testing.T) {
	tests := []struct {
		name          string
		setup         func(*reconcilerTestEnv) session.Info
		wantOwner     exactSessionStartOwner
		wantExactGets bool
		wantOffObs    int
		wantOnObs     int
		terminal      bool
	}{
		{
			name: "keyed", setup: func(env *reconcilerTestEnv) session.Info {
				bead := env.createSessionBead("worker", "worker")
				env.setSessionMetadata(&bead, map[string]string{"state": string(session.StateCreating), "pending_create_claim": "true", "pending_create_started_at": env.clk.Now().UTC().Format(time.RFC3339)})
				return env.sessionInfo(bead.ID)
			}, wantOwner: exactSessionStartKeyedOwner, wantOffObs: 1, wantOnObs: 1,
		},
		{
			name: "legacy", setup: func(env *reconcilerTestEnv) session.Info {
				env.cfg.Agents[0].DependsOn = []string{"dependency"}
				bead := env.createSessionBead("worker", "worker")
				env.setSessionMetadata(&bead, map[string]string{"wake_request": string(session.WakeCauseExplicit)})
				return env.sessionInfo(bead.ID)
			}, wantOwner: exactSessionStartLegacyOwner, wantExactGets: true, wantOffObs: 0, wantOnObs: 1,
		},
		{
			name: "closed", setup: func(env *reconcilerTestEnv) session.Info {
				bead := env.createSessionBead("worker", "worker")
				if err := env.store.Close(bead.ID); err != nil {
					t.Fatalf("close: %v", err)
				}
				return env.sessionInfo(bead.ID)
			}, wantOwner: exactSessionStartUnowned, wantExactGets: true, wantOffObs: 0, wantOnObs: 0, terminal: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			run := func(enabled bool) (int, int, int, int, exactSessionStartOwner, []exactSessionLifecycleStatusResult) {
				env := newReconcilerTestEnv()
				env.cfg = &config.City{Workspace: config.Workspace{Name: "test-city"}, Agents: []config.Agent{{Name: "worker", StartCommand: "true"}}}
				info := tt.setup(env)
				store := newExactStatusCountingStore(t, env.store)
				params := exactSessionStartTestParams(t, env)
				params.Generation, params.Store = 1, store
				observations := 0
				params.ObserveLoadedSession = func(ctx context.Context, cityPath string, store beads.Store, provider runtime.Provider, cfg *config.City, loaded session.Info, processNames []string) (worker.LiveObservation, error) {
					observations++
					return workerObserveLoadedSessionWithRuntimeHintsWithConfig(ctx, cityPath, store, provider, cfg, loaded, processNames)
				}
				var reports []exactSessionLifecycleStatusResult
				if enabled {
					params.StartOptions = append(params.StartOptions, withExactSessionLifecycleStatusObserver(func(result exactSessionLifecycleStatusResult) { reports = append(reports, result) }))
				}
				owner, err := reconcileExactSessionStartWithOwner(context.Background(), sessionStartAdmission{SessionID: info.ID, Source: sessionStartAdmissionExplicitWake, Version: 1}, params)
				if err != nil {
					t.Fatalf("reconcile %s enabled=%t: %v", tt.name, enabled, err)
				}
				return store.gets, store.lists, observations, env.sp.CountCalls("Start", "worker"), owner, reports
			}
			offGets, offLists, offObs, offStarts, offOwner, _ := run(false)
			onGets, onLists, onObs, onStarts, onOwner, reports := run(true)
			equalStoreCost := offGets == onGets && offLists == onLists
			exactSmallCost := !tt.wantExactGets || (offGets == 1 && onGets == 1 && offLists == 0 && onLists == 0)
			if offOwner != tt.wantOwner || onOwner != tt.wantOwner || !equalStoreCost || !exactSmallCost || offStarts != onStarts || offObs != tt.wantOffObs || onObs != tt.wantOnObs || len(reports) != 1 {
				t.Fatalf("on/off costs owner=%d/%d get=%d/%d list=%d/%d obs=%d/%d starts=%d/%d reports=%d", offOwner, onOwner, offGets, onGets, offLists, onLists, offObs, onObs, offStarts, onStarts, len(reports))
			}
			if tt.terminal && (reports[0].Plan == nil || reports[0].Plan.Outcome != sessionLifecycleStatusNoop || reports[0].Plan.Reason != sessionLifecycleStatusReasonTerminal) {
				t.Fatalf("closed observer report = %#v, want terminal noop", reports[0])
			}
		})
	}
}

func TestExactSessionStatusLegacyObservationUsesInheritedProcessHints(t *testing.T) {
	env := newReconcilerTestEnv()
	env.cfg = &config.City{
		Workspace: config.Workspace{Name: "test-city", Provider: "test"},
		Providers: map[string]config.ProviderSpec{
			"test": {Command: "true", ProcessNames: []string{"inherited-agent"}},
		},
		Agents: []config.Agent{{Name: "worker", DependsOn: []string{"dependency"}}},
	}
	bead := env.createSessionBead("worker", "worker")
	env.setSessionMetadata(&bead, map[string]string{"wake_request": string(session.WakeCauseExplicit), "state": string(session.StateAwake), "session_key": "resume"})
	var gotHints []string
	var gotObservation worker.LiveObservation
	observe := func(_ context.Context, _ string, _ beads.Store, _ runtime.Provider, _ *config.City, _ session.Info, processNames []string) (worker.LiveObservation, error) {
		gotHints = append([]string(nil), processNames...)
		gotObservation = worker.LiveObservation{Running: true, Alive: len(processNames) > 0}
		return gotObservation, nil
	}
	var reports []exactSessionLifecycleStatusResult
	params := exactSessionStartTestParams(t, env)
	params.Generation = 1
	params.ObserveLoadedSession = observe
	params.StartOptions = append(params.StartOptions, withExactSessionLifecycleStatusObserver(func(result exactSessionLifecycleStatusResult) { reports = append(reports, result) }))
	owner, err := reconcileExactSessionStartWithOwner(context.Background(), sessionStartAdmission{SessionID: bead.ID, Source: sessionStartAdmissionExplicitWake, Version: 1}, params)
	if err != nil || owner != exactSessionStartLegacyOwner || !reflect.DeepEqual(gotHints, []string{"inherited-agent"}) || !gotObservation.Alive || len(reports) != 1 {
		t.Fatalf("owner/error/hints/observation/reports = %d/%v/%v/%#v/%#v, want legacy/nil/inherited/alive/one", owner, err, gotHints, gotObservation, reports)
	}
}

func TestExactSessionStatusLegacyObservationPreservesNilProcessHints(t *testing.T) {
	env := newReconcilerTestEnv()
	env.cfg = &config.City{
		Workspace: config.Workspace{Name: "test-city"},
		Agents:    []config.Agent{{Name: "worker", DependsOn: []string{"dependency"}}},
	}
	bead := env.createSessionBead("worker", "worker")
	env.setSessionMetadata(&bead, map[string]string{"wake_request": string(session.WakeCauseExplicit)})
	observationCalls := 0
	params := exactSessionStartTestParams(t, env)
	params.Generation = 1
	params.ObserveLoadedSession = func(_ context.Context, _ string, _ beads.Store, _ runtime.Provider, _ *config.City, _ session.Info, processNames []string) (worker.LiveObservation, error) {
		observationCalls++
		if processNames != nil {
			t.Fatalf("process hints = %#v, want nil", processNames)
		}
		return worker.LiveObservation{}, nil
	}
	var reports []exactSessionLifecycleStatusResult
	params.StartOptions = append(params.StartOptions, withExactSessionLifecycleStatusObserver(func(result exactSessionLifecycleStatusResult) {
		reports = append(reports, result)
	}))
	owner, err := reconcileExactSessionStartWithOwner(context.Background(), sessionStartAdmission{
		SessionID: bead.ID,
		Source:    sessionStartAdmissionExplicitWake,
		Version:   1,
	}, params)
	if err != nil || owner != exactSessionStartLegacyOwner || observationCalls != 1 || len(reports) != 1 {
		t.Fatalf("owner/error/observations/reports = %d/%v/%d/%#v, want legacy/nil/1/one", owner, err, observationCalls, reports)
	}
}

func TestReconcileExactSessionStartRejectsMismatchedAuthoritativeIDBeforeEffects(t *testing.T) {
	env := newReconcilerTestEnv()
	env.cfg = &config.City{Agents: []config.Agent{{Name: "worker", StartCommand: "true"}}}
	bead := env.createSessionBead("worker", "worker")
	env.setSessionMetadata(&bead, map[string]string{
		"state":                     string(session.StateCreating),
		"pending_create_claim":      "true",
		"pending_create_started_at": env.clk.Now().UTC().Format(time.RFC3339),
	})
	const returnedID = "gcs-returned-mismatch"
	before := exactStatusStoreState(t, env.store)
	store := newExactStatusCountingStore(t, env.store)
	store.rewriteGet = func(_ int, id string, got beads.Bead, err error) (beads.Bead, error) {
		if err == nil && id == bead.ID {
			got.ID = returnedID
		}
		return got, err
	}
	var reports []exactSessionLifecycleStatusResult
	params := exactSessionStartTestParams(t, env)
	params.Store = store
	params.Generation = 1
	params.StartOptions = append(params.StartOptions, withExactSessionLifecycleStatusObserver(func(result exactSessionLifecycleStatusResult) {
		reports = append(reports, result)
	}))
	owner, err := reconcileExactSessionStartWithOwner(context.Background(), sessionStartAdmission{SessionID: bead.ID, Source: sessionStartAdmissionPendingCreate, Version: 1}, params)
	wantErr := fmt.Sprintf("authoritative read returned %q", returnedID)
	if owner != exactSessionStartUnowned || err == nil || !strings.Contains(err.Error(), wantErr) {
		t.Fatalf("owner/error = %d/%v, want unowned/error containing %q", owner, err, wantErr)
	}
	if len(reports) != 1 || reports[0].Reason != exactSessionLifecycleStatusReasonInvalidInput || reports[0].RequestedID != bead.ID || reports[0].LoadedID != returnedID || reports[0].Error != wantErr {
		t.Fatalf("reports = %#v, want one invalid_input report with requested=%q loaded=%q error=%q", reports, bead.ID, returnedID, wantErr)
	}
	if calls := env.sp.SnapshotCalls(); len(calls) != 0 {
		t.Fatalf("mismatched read provider calls = %#v, want none", calls)
	}
	if store.lists != 0 {
		t.Fatalf("mismatched read list calls = %d, want 0", store.lists)
	}
	requireExactStatusStoreUnchanged(t, before, store)
}

// exactStatusCountingStore records ordinary writes over a concrete MemStore
// while preserving its Tx, conditional-write, and assignment-release behavior.
type exactStatusCountingStore struct {
	*beadstest.RecordingStore
	mem *beads.MemStore

	gets        int
	lists       int
	extraWrites int
	rewriteGet  func(int, string, beads.Bead, error) (beads.Bead, error)
}

func newExactStatusCountingStore(t *testing.T, store beads.Store) *exactStatusCountingStore {
	t.Helper()
	mem, ok := store.(*beads.MemStore)
	if !ok {
		t.Fatalf("exact status store = %T, want *beads.MemStore", store)
	}
	return &exactStatusCountingStore{
		RecordingStore: beadstest.NewRecordingStore(mem),
		mem:            mem,
	}
}

func (s *exactStatusCountingStore) Get(id string) (beads.Bead, error) {
	s.gets++
	bead, err := s.mem.Get(id)
	if s.rewriteGet != nil {
		return s.rewriteGet(s.gets, id, bead, err)
	}
	return bead, err
}

func (s *exactStatusCountingStore) List(query beads.ListQuery) ([]beads.Bead, error) {
	s.lists++
	return s.mem.List(query)
}

func (s *exactStatusCountingStore) Tx(commitMessage string, fn func(beads.Tx) error) error {
	s.extraWrites++
	return s.mem.Tx(commitMessage, fn)
}

func (s *exactStatusCountingStore) ReleaseIfCurrent(id, expectedAssignee string) (bool, error) {
	s.extraWrites++
	return s.mem.ReleaseIfCurrent(id, expectedAssignee)
}

func (s *exactStatusCountingStore) UpdateIfMatch(id string, revision int64, opts beads.UpdateOpts) error {
	s.extraWrites++
	return s.mem.UpdateIfMatch(id, revision, opts)
}

func (s *exactStatusCountingStore) CloseIfMatch(id string, revision int64) error {
	s.extraWrites++
	return s.mem.CloseIfMatch(id, revision)
}

func (s *exactStatusCountingStore) DeleteIfMatch(id string, revision int64) error {
	s.extraWrites++
	return s.mem.DeleteIfMatch(id, revision)
}

func (s *exactStatusCountingStore) CompareAndSetMetadataKey(id, key, expected, next string) (bool, error) {
	s.extraWrites++
	return s.mem.CompareAndSetMetadataKey(id, key, expected, next)
}

func (s *exactStatusCountingStore) Handles() beads.StoreHandles {
	handles := beads.HandlesFor(s.mem)
	handles.Live = exactStatusCountingLiveReader{store: s, LiveReader: handles.Live}
	handles.Writer = s
	return handles
}

type exactStatusCountingLiveReader struct {
	store *exactStatusCountingStore
	beads.LiveReader
}

func (r exactStatusCountingLiveReader) Get(id string) (beads.Bead, error) {
	r.store.gets++
	bead, err := r.LiveReader.Get(id)
	if r.store.rewriteGet != nil {
		return r.store.rewriteGet(r.store.gets, id, bead, err)
	}
	return bead, err
}

func (s *exactStatusCountingStore) mutationCalls() int {
	return len(s.Calls()) + s.extraWrites
}

func TestExactStatusStoreCensusDetectsHiddenMemStoreMutations(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*testing.T, *exactStatusCountingStore, beads.Bead, beads.Bead)
	}{
		{
			name: "local string",
			mutate: func(t *testing.T, store *exactStatusCountingStore, bead, _ beads.Bead) {
				t.Helper()
				if err := store.SetLocalString(bead.ID, "hidden", "changed"); err != nil {
					t.Fatalf("SetLocalString: %v", err)
				}
			},
		},
		{
			name: "dependency through writer handle",
			mutate: func(t *testing.T, store *exactStatusCountingStore, bead, dependency beads.Bead) {
				t.Helper()
				if err := beads.HandlesFor(store).Writer.DepAdd(bead.ID, dependency.ID, "blocks"); err != nil {
					t.Fatalf("DepAdd: %v", err)
				}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mem := beads.NewMemStore()
			bead, err := mem.Create(beads.Bead{Title: "target"})
			if err != nil {
				t.Fatalf("create target: %v", err)
			}
			dependency, err := mem.Create(beads.Bead{Title: "dependency"})
			if err != nil {
				t.Fatalf("create dependency: %v", err)
			}
			before := exactStatusStoreState(t, mem)
			store := newExactStatusCountingStore(t, mem)
			if _, ok := any(store).(beads.ConditionalWriter); !ok {
				t.Fatal("concrete wrapper hid MemStore's conditional writer")
			}
			if _, ok := any(store).(beads.ConditionalAssignmentReleaser); !ok {
				t.Fatal("concrete wrapper hid MemStore's conditional assignment releaser")
			}

			tt.mutate(t, store, bead, dependency)

			if exactStatusStoreUnchanged(t, before, store) {
				t.Fatal("mutation census missed a state-invisible MemStore write")
			}
		})
	}
}

func exactStatusStoreState(t *testing.T, store beads.Store) []beads.Bead {
	t.Helper()
	snapshot, err := store.List(beads.ListQuery{AllowScan: true, IncludeClosed: true, TierMode: beads.TierBoth})
	if err != nil {
		t.Fatalf("snapshotting exact status store: %v", err)
	}
	return snapshot
}

func exactStatusStoreUnchanged(t *testing.T, before []beads.Bead, store *exactStatusCountingStore) bool {
	t.Helper()
	return store.mutationCalls() == 0 && reflect.DeepEqual(exactStatusStoreState(t, store.mem), before)
}

func requireExactStatusStoreUnchanged(t *testing.T, before []beads.Bead, store *exactStatusCountingStore) {
	t.Helper()
	if calls := store.mutationCalls(); calls != 0 {
		t.Fatalf("exact status store mutation calls = %d, want 0", calls)
	}
	if after := exactStatusStoreState(t, store.mem); !reflect.DeepEqual(after, before) {
		t.Fatalf("exact status store changed: before=%#v after=%#v", before, after)
	}
}
