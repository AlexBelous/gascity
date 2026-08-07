package main

import (
	"strings"
	"time"

	"github.com/gastownhall/gascity/internal/clock"
	"github.com/gastownhall/gascity/internal/session"
)

// sessionLifecycleStartShadowInput is the complete effect-free input the
// start-selection planner needs. RuntimeObserved distinguishes a known-dead
// session from a failed or skipped runtime probe.
type sessionLifecycleStartShadowInput struct {
	Info                 session.Info
	WakeDecisionObserved bool
	ShouldWake           bool
	ConfigSuppressed     bool
	RuntimeObserved      bool
	RuntimeAlive         bool
	ObservedAt           time.Time
	StartupTimeout       time.Duration
	CircuitOpen          bool
	ProviderUnavailable  bool
}

type sessionLifecycleStartSelectionOutcome uint8

const (
	sessionLifecycleStartSelectionUnknown sessionLifecycleStartSelectionOutcome = iota
	sessionLifecycleStartSelectionNoop
	sessionLifecycleStartSelectionPrepare
	sessionLifecycleStartSelectionPark
)

type sessionLifecycleStartSelectionReason string

const (
	sessionLifecycleStartSelectionReasonUnknown             sessionLifecycleStartSelectionReason = ""
	sessionLifecycleStartSelectionReasonInvalidInput        sessionLifecycleStartSelectionReason = "invalid_input"
	sessionLifecycleStartSelectionReasonTerminal            sessionLifecycleStartSelectionReason = "terminal"
	sessionLifecycleStartSelectionReasonWakeUnknown         sessionLifecycleStartSelectionReason = "wake_unknown"
	sessionLifecycleStartSelectionReasonRuntimeUnknown      sessionLifecycleStartSelectionReason = "runtime_unknown"
	sessionLifecycleStartSelectionReasonObservationUnknown  sessionLifecycleStartSelectionReason = "observation_unknown"
	sessionLifecycleStartSelectionReasonConfigSuppressed    sessionLifecycleStartSelectionReason = "config_suppressed"
	sessionLifecycleStartSelectionReasonNotNeeded           sessionLifecycleStartSelectionReason = "not_needed"
	sessionLifecycleStartSelectionReasonAlreadyRunning      sessionLifecycleStartSelectionReason = "already_running"
	sessionLifecycleStartSelectionReasonFailedCreate        sessionLifecycleStartSelectionReason = "failed_create"
	sessionLifecycleStartSelectionReasonQuarantined         sessionLifecycleStartSelectionReason = "quarantined"
	sessionLifecycleStartSelectionReasonStartInFlight       sessionLifecycleStartSelectionReason = "start_in_flight"
	sessionLifecycleStartSelectionReasonCircuitOpen         sessionLifecycleStartSelectionReason = "circuit_open"
	sessionLifecycleStartSelectionReasonProviderUnavailable sessionLifecycleStartSelectionReason = "provider_unavailable"
	sessionLifecycleStartSelectionReasonReady               sessionLifecycleStartSelectionReason = "ready"
)

type sessionLifecycleStartSelectionPlan struct {
	SessionID string
	Outcome   sessionLifecycleStartSelectionOutcome
	Reason    sessionLifecycleStartSelectionReason
}

func planSessionLifecycleStartSelection(input sessionLifecycleStartShadowInput) sessionLifecycleStartSelectionPlan {
	plan := sessionLifecycleStartSelectionPlan{SessionID: input.Info.ID}
	switch {
	case input.Info.ID == "" || strings.TrimSpace(input.Info.ID) != input.Info.ID:
		plan.Outcome = sessionLifecycleStartSelectionPark
		plan.Reason = sessionLifecycleStartSelectionReasonInvalidInput
	case input.Info.Closed:
		plan.Outcome = sessionLifecycleStartSelectionNoop
		plan.Reason = sessionLifecycleStartSelectionReasonTerminal
	case !input.WakeDecisionObserved:
		plan.Outcome = sessionLifecycleStartSelectionPark
		plan.Reason = sessionLifecycleStartSelectionReasonWakeUnknown
	case !input.RuntimeObserved:
		plan.Outcome = sessionLifecycleStartSelectionPark
		plan.Reason = sessionLifecycleStartSelectionReasonRuntimeUnknown
	case input.ObservedAt.IsZero():
		plan.Outcome = sessionLifecycleStartSelectionPark
		plan.Reason = sessionLifecycleStartSelectionReasonObservationUnknown
	case input.ConfigSuppressed:
		plan.Outcome = sessionLifecycleStartSelectionNoop
		plan.Reason = sessionLifecycleStartSelectionReasonConfigSuppressed
	case !input.ShouldWake:
		plan.Outcome = sessionLifecycleStartSelectionNoop
		plan.Reason = sessionLifecycleStartSelectionReasonNotNeeded
	case input.RuntimeAlive:
		plan.Outcome = sessionLifecycleStartSelectionNoop
		plan.Reason = sessionLifecycleStartSelectionReasonAlreadyRunning
	case isFailedCreateSessionInfo(input.Info):
		plan.Outcome = sessionLifecycleStartSelectionPark
		plan.Reason = sessionLifecycleStartSelectionReasonFailedCreate
	case sessionIsQuarantinedInfo(input.Info, &clock.Fake{Time: input.ObservedAt}):
		plan.Outcome = sessionLifecycleStartSelectionPark
		plan.Reason = sessionLifecycleStartSelectionReasonQuarantined
	case pendingCreateStartInFlightInfo(input.Info, &clock.Fake{Time: input.ObservedAt}, input.StartupTimeout):
		plan.Outcome = sessionLifecycleStartSelectionPark
		plan.Reason = sessionLifecycleStartSelectionReasonStartInFlight
	case input.CircuitOpen:
		plan.Outcome = sessionLifecycleStartSelectionPark
		plan.Reason = sessionLifecycleStartSelectionReasonCircuitOpen
	case input.ProviderUnavailable:
		plan.Outcome = sessionLifecycleStartSelectionPark
		plan.Reason = sessionLifecycleStartSelectionReasonProviderUnavailable
	default:
		plan.Outcome = sessionLifecycleStartSelectionPrepare
		plan.Reason = sessionLifecycleStartSelectionReasonReady
	}
	return plan
}
