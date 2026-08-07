package main

import (
	"strings"
	"time"

	"github.com/gastownhall/gascity/internal/clock"
	"github.com/gastownhall/gascity/internal/session"
)

// sessionLifecycleShadowInput is the complete effect-free input the status
// planner needs. RuntimeObserved distinguishes a known-dead session from a
// failed or skipped runtime probe.
type sessionLifecycleShadowInput struct {
	Info              session.Info
	RuntimeObserved   bool
	RuntimeAlive      bool
	ObservedAt        time.Time
	StartupTimeout    time.Duration
	RollbackAvailable bool
}

type sessionLifecycleStatusOutcome uint8

const (
	sessionLifecycleStatusUnknown sessionLifecycleStatusOutcome = iota
	sessionLifecycleStatusNoop
	sessionLifecycleStatusHeal
	sessionLifecycleStatusPark
)

type sessionLifecycleStatusReason string

const (
	sessionLifecycleStatusReasonUnknown            sessionLifecycleStatusReason = ""
	sessionLifecycleStatusReasonConverged          sessionLifecycleStatusReason = "converged"
	sessionLifecycleStatusReasonHeal               sessionLifecycleStatusReason = "heal"
	sessionLifecycleStatusReasonTerminal           sessionLifecycleStatusReason = "terminal"
	sessionLifecycleStatusReasonRuntimeUnknown     sessionLifecycleStatusReason = "runtime_unknown"
	sessionLifecycleStatusReasonObservationUnknown sessionLifecycleStatusReason = "observation_unknown"
	sessionLifecycleStatusReasonInvalidInput       sessionLifecycleStatusReason = "invalid_input"
)

// sessionLifecycleStatusPlan describes at most one legacy-compatible metadata
// heal. It contains no writer or runtime capability.
type sessionLifecycleStatusPlan struct {
	SessionID string
	Outcome   sessionLifecycleStatusOutcome
	Reason    sessionLifecycleStatusReason
	Patch     session.MetadataPatch
}

func planSessionLifecycleStatus(input sessionLifecycleShadowInput) sessionLifecycleStatusPlan {
	plan := sessionLifecycleStatusPlan{SessionID: input.Info.ID}
	if input.Info.ID == "" || strings.TrimSpace(input.Info.ID) != input.Info.ID {
		plan.Outcome = sessionLifecycleStatusPark
		plan.Reason = sessionLifecycleStatusReasonInvalidInput
		return plan
	}
	if input.Info.Closed {
		plan.Outcome = sessionLifecycleStatusNoop
		plan.Reason = sessionLifecycleStatusReasonTerminal
		return plan
	}
	if !input.RuntimeObserved {
		plan.Outcome = sessionLifecycleStatusPark
		plan.Reason = sessionLifecycleStatusReasonRuntimeUnknown
		return plan
	}
	if input.ObservedAt.IsZero() {
		plan.Outcome = sessionLifecycleStatusPark
		plan.Reason = sessionLifecycleStatusReasonObservationUnknown
		return plan
	}

	patch := healStatePatchWithRollbackInfo(
		input.Info,
		input.RuntimeAlive,
		&clock.Fake{Time: input.ObservedAt},
		input.StartupTimeout,
		input.RollbackAvailable,
	)
	if len(patch) == 0 {
		plan.Outcome = sessionLifecycleStatusNoop
		plan.Reason = sessionLifecycleStatusReasonConverged
		return plan
	}
	plan.Outcome = sessionLifecycleStatusHeal
	plan.Reason = sessionLifecycleStatusReasonHeal
	plan.Patch = cloneSessionLifecycleStatusPatch(patch)
	return plan
}

func cloneSessionLifecycleStatusPlan(plan sessionLifecycleStatusPlan) sessionLifecycleStatusPlan {
	cloned := plan
	cloned.Patch = cloneSessionLifecycleStatusPatch(plan.Patch)
	return cloned
}

func cloneSessionLifecycleStatusPatch(patch map[string]string) session.MetadataPatch {
	if len(patch) == 0 {
		return nil
	}
	cloned := make(session.MetadataPatch, len(patch))
	for key, value := range patch {
		cloned[key] = value
	}
	return cloned
}
