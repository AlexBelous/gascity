package main

import (
	"maps"
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/clock"
	"github.com/gastownhall/gascity/internal/session"
)

func TestPlanSessionLifecycleStatusMatchesLegacyDerivation(t *testing.T) {
	now := time.Date(2026, 7, 26, 3, 0, 0, 0, time.UTC)
	tests := []struct {
		name       string
		input      sessionLifecycleShadowInput
		want       sessionLifecycleStatusOutcome
		wantReason sessionLifecycleStatusReason
	}{
		{
			name: "converged",
			input: sessionLifecycleShadowInput{
				Info:            session.Info{ID: "session-converged", State: session.StateAsleep, MetadataState: string(session.StateAsleep)},
				RuntimeObserved: true,
				ObservedAt:      now,
			},
			want:       sessionLifecycleStatusNoop,
			wantReason: sessionLifecycleStatusReasonConverged,
		},
		{
			name: "heal",
			input: sessionLifecycleShadowInput{
				Info: session.Info{
					ID:                "session-heal",
					State:             session.StateAwake,
					MetadataState:     string(session.StateAwake),
					SessionKey:        "resume-key",
					StartedConfigHash: "config-hash",
					CreatedAt:         now.Add(-time.Hour),
				},
				RuntimeObserved: true,
				RuntimeAlive:    false,
				ObservedAt:      now,
			},
			want:       sessionLifecycleStatusHeal,
			wantReason: sessionLifecycleStatusReasonHeal,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			plan := planSessionLifecycleStatus(tt.input)
			if plan.Outcome != tt.want || plan.Reason != tt.wantReason {
				t.Fatalf("plan = %+v, want outcome=%v reason=%v", plan, tt.want, tt.wantReason)
			}
			wantPatch := healStatePatchWithRollbackInfo(
				tt.input.Info,
				tt.input.RuntimeAlive,
				&clock.Fake{Time: tt.input.ObservedAt},
				tt.input.StartupTimeout,
				tt.input.RollbackAvailable,
			)
			if !maps.Equal(plan.Patch, wantPatch) {
				t.Fatalf("plan patch = %#v, legacy derivation = %#v", plan.Patch, wantPatch)
			}
			if tt.want == sessionLifecycleStatusHeal && len(plan.Patch) == 0 {
				t.Fatal("heal fixture produced an empty patch")
			}
		})
	}
}

func TestPlanSessionLifecycleStatusFailsClosedWithoutRuntimeFacts(t *testing.T) {
	now := time.Date(2026, 7, 26, 3, 0, 0, 0, time.UTC)
	tests := []struct {
		name       string
		input      sessionLifecycleShadowInput
		want       sessionLifecycleStatusOutcome
		wantReason sessionLifecycleStatusReason
	}{
		{
			name: "terminal wins before runtime",
			input: sessionLifecycleShadowInput{
				Info: session.Info{ID: "session-closed", Closed: true},
			},
			want:       sessionLifecycleStatusNoop,
			wantReason: sessionLifecycleStatusReasonTerminal,
		},
		{
			name: "runtime unobserved",
			input: sessionLifecycleShadowInput{
				Info:       session.Info{ID: "session-unobserved"},
				ObservedAt: now,
			},
			want:       sessionLifecycleStatusPark,
			wantReason: sessionLifecycleStatusReasonRuntimeUnknown,
		},
		{
			name: "observation time missing",
			input: sessionLifecycleShadowInput{
				Info:            session.Info{ID: "session-no-time"},
				RuntimeObserved: true,
			},
			want:       sessionLifecycleStatusPark,
			wantReason: sessionLifecycleStatusReasonObservationUnknown,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			plan := planSessionLifecycleStatus(tt.input)
			if plan.Outcome != tt.want || plan.Reason != tt.wantReason || plan.Patch != nil {
				t.Fatalf("plan = %+v, want outcome=%v reason=%v and no patch", plan, tt.want, tt.wantReason)
			}
		})
	}
}

func TestPlanSessionLifecycleStatusReturnsDetachedPatch(t *testing.T) {
	now := time.Date(2026, 7, 26, 3, 0, 0, 0, time.UTC)
	input := sessionLifecycleShadowInput{
		Info: session.Info{
			ID:            "session-heal",
			State:         session.StateAwake,
			MetadataState: string(session.StateAwake),
			SessionKey:    "resume-key",
		},
		RuntimeObserved: true,
		ObservedAt:      now,
	}

	first := planSessionLifecycleStatus(input)
	if first.Outcome != sessionLifecycleStatusHeal {
		t.Fatalf("first plan = %+v, want heal", first)
	}
	first.Patch["state"] = "corrupt"
	second := planSessionLifecycleStatus(input)
	if second.Patch["state"] == "corrupt" {
		t.Fatal("mutating a returned plan changed the next derivation")
	}
}
