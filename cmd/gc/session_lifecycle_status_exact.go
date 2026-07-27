package main

import (
	"fmt"
	"io"
	"runtime/debug"
	"strings"
	"time"

	"github.com/gastownhall/gascity/internal/session"
	"github.com/gastownhall/gascity/internal/worker"
)

// exactSessionLifecycleStatusReason describes why an exact-key status shadow
// result is usable or deliberately parked. It never claims legacy parity.
type exactSessionLifecycleStatusReason string

const (
	exactSessionLifecycleStatusReasonCandidate               exactSessionLifecycleStatusReason = "candidate"
	exactSessionLifecycleStatusReasonInvalidInput            exactSessionLifecycleStatusReason = "invalid_input"
	exactSessionLifecycleStatusReasonContextAmbiguous        exactSessionLifecycleStatusReason = "context_ambiguous"
	exactSessionLifecycleStatusReasonPrerequisiteUnavailable exactSessionLifecycleStatusReason = "prerequisite_unavailable"
	exactSessionLifecycleStatusReasonNotObserved             exactSessionLifecycleStatusReason = "not_observed"
	exactSessionLifecycleStatusReasonObservationUnavailable  exactSessionLifecycleStatusReason = "observation_unavailable"
)

type exactSessionLifecycleStatusDisposition string

const (
	exactSessionLifecycleStatusDispositionCandidate exactSessionLifecycleStatusDisposition = "candidate"
	exactSessionLifecycleStatusDispositionPark      exactSessionLifecycleStatusDisposition = "park"
)

// exactSessionLifecycleStatusResult is an effect-free report from one
// authoritative exact-key read and at most one runtime observation.
type exactSessionLifecycleStatusResult struct {
	Admission            sessionStartAdmission
	AdmissionVersion     uint64
	ControllerGeneration uint64
	RequestedID          string
	LoadedID             string
	ObservedAt           time.Time
	Disposition          exactSessionLifecycleStatusDisposition
	Reason               exactSessionLifecycleStatusReason
	Plan                 *sessionLifecycleStatusPlan
	Error                string
	ComparedToLegacy     bool
}

// exactSessionLifecycleStatusObserver receives a detached shadow-only result.
// Its return value is intentionally absent: it cannot affect start ownership.
type exactSessionLifecycleStatusObserver func(exactSessionLifecycleStatusResult)

// exactSessionLifecycleStatusInput is the immutable evidence used by the
// pure exact-key status evaluator.
type exactSessionLifecycleStatusInput struct {
	Admission            sessionStartAdmission
	ControllerGeneration uint64
	RequestedID          string
	Info                 session.Info
	Observation          worker.LiveObservation
	ObservedAt           time.Time
	StartupTimeout       time.Duration
	PrerequisitesReady   bool
	UnavailableReason    exactSessionLifecycleStatusReason
	Error                string
}

// evaluateExactSessionLifecycleStatus accepts a status candidate only when
// the desired and both legacy orphan contexts derive the identical plan from
// the same durable row and runtime observation. It owns no store or provider.
func evaluateExactSessionLifecycleStatus(input exactSessionLifecycleStatusInput) exactSessionLifecycleStatusResult {
	result := exactSessionLifecycleStatusResult{
		Admission:            input.Admission,
		AdmissionVersion:     input.Admission.Version,
		ControllerGeneration: input.ControllerGeneration,
		RequestedID:          input.RequestedID,
		LoadedID:             input.Info.ID,
		ObservedAt:           input.ObservedAt,
		Disposition:          exactSessionLifecycleStatusDispositionPark,
		Reason:               exactSessionLifecycleStatusReasonInvalidInput,
		Error:                input.Error,
		ComparedToLegacy:     false,
	}
	if input.RequestedID == "" {
		result.RequestedID = input.Admission.SessionID
	}
	if input.ControllerGeneration == 0 || input.Admission.Version == 0 ||
		input.Admission.SessionID != input.RequestedID || input.Info.ID == "" ||
		input.RequestedID != input.Info.ID || strings.TrimSpace(input.Info.ID) != input.Info.ID {
		return result
	}
	if input.Info.Closed {
		if !input.ObservedAt.IsZero() || input.UnavailableReason != "" || input.Error != "" {
			return result
		}
		plan := planSessionLifecycleStatus(sessionLifecycleShadowInput{Info: input.Info})
		result.Disposition = exactSessionLifecycleStatusDispositionCandidate
		result.Reason = exactSessionLifecycleStatusReasonCandidate
		result.Plan = ptrExactSessionLifecycleStatusPlan(plan)
		return result
	}
	if input.UnavailableReason != "" {
		if !input.ObservedAt.IsZero() {
			return result
		}
		result.Reason = input.UnavailableReason
		return result
	}
	if !input.PrerequisitesReady || input.ObservedAt.IsZero() || input.Error != "" {
		return result
	}

	desired := planSessionLifecycleStatus(sessionLifecycleShadowInput{
		Info:              input.Info,
		RuntimeObserved:   true,
		RuntimeAlive:      input.Observation.Alive,
		ObservedAt:        input.ObservedAt,
		StartupTimeout:    input.StartupTimeout,
		RollbackAvailable: true,
	})
	orphanComplete := planSessionLifecycleStatus(sessionLifecycleShadowInput{
		Info:              input.Info,
		RuntimeObserved:   true,
		RuntimeAlive:      input.Observation.Running,
		ObservedAt:        input.ObservedAt,
		StartupTimeout:    input.StartupTimeout,
		RollbackAvailable: true,
	})
	orphanPartial := planSessionLifecycleStatus(sessionLifecycleShadowInput{
		Info:              input.Info,
		RuntimeObserved:   true,
		RuntimeAlive:      input.Observation.Running,
		ObservedAt:        input.ObservedAt,
		StartupTimeout:    input.StartupTimeout,
		RollbackAvailable: false,
	})
	if desired.Outcome == sessionLifecycleStatusPark ||
		!sameExactSessionLifecycleStatusPlan(desired, orphanComplete) ||
		!sameExactSessionLifecycleStatusPlan(desired, orphanPartial) {
		result.Reason = exactSessionLifecycleStatusReasonContextAmbiguous
		return result
	}
	result.Reason = exactSessionLifecycleStatusReasonCandidate
	result.Disposition = exactSessionLifecycleStatusDispositionCandidate
	plan := cloneSessionLifecycleStatusPlan(desired)
	result.Plan = &plan
	return result
}

func ptrExactSessionLifecycleStatusPlan(plan sessionLifecycleStatusPlan) *sessionLifecycleStatusPlan {
	cloned := cloneSessionLifecycleStatusPlan(plan)
	return &cloned
}

func sameExactSessionLifecycleStatusPlan(left, right sessionLifecycleStatusPlan) bool {
	return left.Outcome == right.Outcome && left.Reason == right.Reason &&
		sameSessionLifecycleStatusPatch(left.Patch, right.Patch)
}

const exactSessionLifecycleStatusPanicDiagnosticLimit = 4096

func reportExactSessionLifecycleStatus(stderr io.Writer, observer exactSessionLifecycleStatusObserver, result exactSessionLifecycleStatusResult) {
	if observer == nil {
		return
	}
	if stderr == nil {
		stderr = io.Discard
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			prefix := fmt.Sprintf(
				"exact session lifecycle status observer panicked for %s version %d: %v\n",
				result.Admission.SessionID,
				result.Admission.Version,
				recovered,
			)
			stack := debug.Stack()
			remaining := exactSessionLifecycleStatusPanicDiagnosticLimit - len(prefix)
			if remaining < 0 {
				prefix = prefix[:exactSessionLifecycleStatusPanicDiagnosticLimit]
				remaining = 0
			}
			if len(stack) > remaining {
				stack = stack[:remaining]
			}
			fmt.Fprint(stderr, prefix, string(stack)) //nolint:errcheck // observer diagnostics must not affect reconciliation
		}
	}()
	observer(result)
}
