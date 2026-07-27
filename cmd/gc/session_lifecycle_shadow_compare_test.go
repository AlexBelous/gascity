package main

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/config"
	"github.com/gastownhall/gascity/internal/session"
)

type failSessionObservationGetStore struct {
	beads.Store
	sessionID string
	err       error
}

func (s *failSessionObservationGetStore) Get(id string) (beads.Bead, error) {
	if id == s.sessionID {
		return beads.Bead{}, s.err
	}
	return s.Store.Get(id)
}

func TestCompareSessionLifecycleStatusClassifiesLegacyResult(t *testing.T) {
	now := time.Date(2026, 7, 26, 6, 0, 0, 0, time.UTC)
	heal := planSessionLifecycleStatus(sessionLifecycleShadowInput{
		Info: session.Info{
			ID:            "session-heal",
			State:         session.StateAsleep,
			MetadataState: string(session.StateAsleep),
		},
		RuntimeObserved: true,
		RuntimeAlive:    true,
		ObservedAt:      now,
	})
	if heal.Outcome != sessionLifecycleStatusHeal {
		t.Fatalf("heal fixture plan = %+v, want heal", heal)
	}
	parked := planSessionLifecycleStatus(sessionLifecycleShadowInput{
		Info:       session.Info{ID: "session-parked"},
		ObservedAt: now,
	})
	noop := planSessionLifecycleStatus(sessionLifecycleShadowInput{
		Info: session.Info{
			ID:            "session-noop",
			State:         session.StateAwake,
			MetadataState: string(session.StateAwake),
		},
		RuntimeObserved: true,
		RuntimeAlive:    true,
		ObservedAt:      now,
	})

	tests := []struct {
		name       string
		candidate  sessionLifecycleStatusPlan
		legacy     session.MetadataPatch
		legacyErr  error
		want       sessionLifecycleStatusComparisonOutcome
		wantReason sessionLifecycleStatusComparisonReason
	}{
		{
			name:       "matching heal",
			candidate:  heal,
			legacy:     cloneSessionLifecycleStatusPatch(heal.Patch),
			want:       sessionLifecycleStatusComparisonMatched,
			wantReason: sessionLifecycleStatusComparisonReasonEquivalent,
		},
		{
			name:       "matching noop",
			candidate:  noop,
			want:       sessionLifecycleStatusComparisonMatched,
			wantReason: sessionLifecycleStatusComparisonReasonEquivalent,
		},
		{
			name:       "different patch",
			candidate:  heal,
			legacy:     session.MetadataPatch{"state": string(session.StateAsleep)},
			want:       sessionLifecycleStatusComparisonMismatched,
			wantReason: sessionLifecycleStatusComparisonReasonPatchMismatch,
		},
		{
			name:       "parked candidate",
			candidate:  parked,
			legacy:     session.MetadataPatch{"state": string(session.StateAsleep)},
			want:       sessionLifecycleStatusComparisonIncomparable,
			wantReason: sessionLifecycleStatusComparisonReasonShadowParked,
		},
		{
			name:       "legacy write error",
			candidate:  heal,
			legacyErr:  errors.New("ambiguous store write"),
			want:       sessionLifecycleStatusComparisonIncomparable,
			wantReason: sessionLifecycleStatusComparisonReasonLegacyError,
		},
		{
			name: "invalid candidate",
			candidate: sessionLifecycleStatusPlan{
				SessionID: "session-invalid",
			},
			want:       sessionLifecycleStatusComparisonIncomparable,
			wantReason: sessionLifecycleStatusComparisonReasonCandidateInvalid,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := compareSessionLifecycleStatus(
				sessionLifecycleStatusHealSiteDesired,
				tt.candidate,
				tt.legacy,
				tt.legacyErr,
			)
			if got.Outcome != tt.want || got.Reason != tt.wantReason {
				t.Fatalf("comparison = %+v, want outcome=%v reason=%v", got, tt.want, tt.wantReason)
			}
			if tt.legacyErr != nil && got.LegacyError != tt.legacyErr.Error() {
				t.Fatalf("legacy error = %q, want %q", got.LegacyError, tt.legacyErr.Error())
			}
		})
	}
}

func TestCompareSessionLifecycleStatusDetachesPlansAndPatches(t *testing.T) {
	candidate := sessionLifecycleStatusPlan{
		SessionID: "session-detached",
		Outcome:   sessionLifecycleStatusHeal,
		Reason:    sessionLifecycleStatusReasonHeal,
		Patch:     session.MetadataPatch{"state": string(session.StateAwake)},
	}
	legacy := session.MetadataPatch{"state": string(session.StateAwake)}

	got := compareSessionLifecycleStatus(
		sessionLifecycleStatusHealSiteDesired,
		candidate,
		legacy,
		nil,
	)
	candidate.Patch["state"] = "candidate-corrupt"
	legacy["state"] = "legacy-corrupt"

	if got.Candidate.Patch["state"] != string(session.StateAwake) {
		t.Fatalf("detached candidate patch = %#v, want awake", got.Candidate.Patch)
	}
	if got.LegacyPatch["state"] != string(session.StateAwake) {
		t.Fatalf("detached legacy patch = %#v, want awake", got.LegacyPatch)
	}
}

func TestReconcileSessionBeadsReportsStatusComparisonAtBothHealSites(t *testing.T) {
	tests := []struct {
		name               string
		desired            bool
		storeQueryPartial  bool
		pendingCreateClaim bool
		wantSite           sessionLifecycleStatusHealSite
		wantPatch          session.MetadataPatch
	}{
		{
			name:               "orphan partial inventory preserves stale creating lease",
			desired:            false,
			storeQueryPartial:  true,
			pendingCreateClaim: true,
			wantSite:           sessionLifecycleStatusHealSiteOrphan,
			wantPatch:          session.MetadataPatch{"state": string(session.StateAsleep)},
		},
		{
			name:      "desired site observes prepass-converged noop",
			desired:   true,
			wantSite:  sessionLifecycleStatusHealSiteDesired,
			wantPatch: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := newReconcilerTestEnv()
			env.cfg.Agents = []config.Agent{{Name: "worker"}}
			env.addDesired("worker", "worker", false)
			if !tt.desired {
				delete(env.desiredState, "worker")
			}
			sessionBead := env.createSessionBead("worker", "worker")
			metadata := map[string]string{
				"state":        string(session.StateCreating),
				"last_woke_at": env.clk.Now().Add(-2 * time.Minute).Format(time.RFC3339),
			}
			if tt.pendingCreateClaim {
				metadata["pending_create_claim"] = "true"
				metadata["pending_create_started_at"] = env.clk.Now().Add(-2 * time.Minute).Format(time.RFC3339)
			}
			env.setSessionMetadata(&sessionBead, metadata)
			var comparisons []sessionLifecycleStatusComparison
			env.startOptions = append(env.startOptions,
				withSessionLifecycleStatusComparisonObserver(func(comparison sessionLifecycleStatusComparison) {
					comparisons = append(comparisons, comparison)
				}),
			)

			cfgNames := configuredSessionNames(env.cfg, "", env.store)
			reconcileSessionBeads(
				context.Background(), []beads.Bead{sessionBead}, env.desiredState, cfgNames, env.cfg, env.sp,
				env.store, nil, nil, nil, env.dt, map[string]int{"worker": 1}, tt.storeQueryPartial, nil, "",
				nil, env.clk, env.rec, 0, 0, &env.stdout, &env.stderr, env.startOptions...,
			)

			if len(comparisons) != 1 {
				t.Fatalf("comparison count = %d, want 1; comparisons=%+v stderr=%q", len(comparisons), comparisons, env.stderr.String())
			}
			got := comparisons[0]
			if got.Site != tt.wantSite {
				t.Fatalf("site = %q, want %q", got.Site, tt.wantSite)
			}
			if got.Outcome != sessionLifecycleStatusComparisonMatched {
				t.Fatalf("comparison = %+v, want matched", got)
			}
			if got.Candidate.SessionID != sessionBead.ID {
				t.Fatalf("candidate session = %q, want %q", got.Candidate.SessionID, sessionBead.ID)
			}
			if !reflect.DeepEqual(got.Candidate.Patch, tt.wantPatch) || !reflect.DeepEqual(got.LegacyPatch, tt.wantPatch) {
				t.Fatalf("comparison patches = candidate:%#v legacy:%#v, want exact %#v", got.Candidate.Patch, got.LegacyPatch, tt.wantPatch)
			}
		})
	}
}

func TestReconcileSessionBeadsParksUnknownOrphanRuntimeObservation(t *testing.T) {
	env := newReconcilerTestEnv()
	sessionBead := env.createSessionBead("worker", "worker")
	env.addDesired(sessionBead.ID, "worker", true)
	delete(env.desiredState, sessionBead.ID)
	if err := env.sp.SetMeta(sessionBead.ID, "GC_SESSION_ID", sessionBead.ID); err != nil {
		t.Fatalf("set runtime session id: %v", err)
	}
	observationErr := errors.New("runtime observation unavailable")
	env.store = &failSessionObservationGetStore{
		Store:     env.store,
		sessionID: sessionBead.ID,
		err:       observationErr,
	}
	var comparisons []sessionLifecycleStatusComparison
	env.startOptions = append(env.startOptions,
		withSessionLifecycleStatusComparisonObserver(func(comparison sessionLifecycleStatusComparison) {
			comparisons = append(comparisons, comparison)
		}),
	)

	env.reconcile([]beads.Bead{sessionBead})

	if len(comparisons) != 1 {
		t.Fatalf("comparison count = %d, want 1; comparisons=%+v stderr=%q", len(comparisons), comparisons, env.stderr.String())
	}
	got := comparisons[0]
	if got.Site != sessionLifecycleStatusHealSiteOrphan {
		t.Fatalf("site = %q, want orphan", got.Site)
	}
	if got.Candidate.Outcome != sessionLifecycleStatusPark ||
		got.Candidate.Reason != sessionLifecycleStatusReasonRuntimeUnknown {
		t.Fatalf("candidate = %+v, want parked on unknown runtime", got.Candidate)
	}
	if got.Outcome != sessionLifecycleStatusComparisonIncomparable ||
		got.Reason != sessionLifecycleStatusComparisonReasonShadowParked {
		t.Fatalf("comparison = %+v, want incomparable parked shadow", got)
	}
	if got.LegacyError != "" {
		t.Fatalf("legacy error = %q, want successful legacy comparison", got.LegacyError)
	}
}
