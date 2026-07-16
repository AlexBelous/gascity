package main

import (
	"time"

	"github.com/gastownhall/gascity/internal/nudgequeue"
	"github.com/gastownhall/gascity/internal/runtime"
)

type nudgeEffectCandidateDisposition uint8

const (
	nudgeEffectCandidateUnknown nudgeEffectCandidateDisposition = iota
	nudgeEffectCandidateNoop
	nudgeEffectCandidatePark
	nudgeEffectCandidateRetry
	nudgeEffectCandidateReject
	nudgeEffectCandidateNeedTarget
	nudgeEffectCandidateNeedClaim
)

type nudgeEffectPlanReason uint8

const (
	nudgeEffectPlanReasonUnknown nudgeEffectPlanReason = iota
	nudgeEffectPlanReasonProjectionUnsynced
	nudgeEffectPlanReasonProjectionLineageChanged
	nudgeEffectPlanReasonEarlierOrderedHead
	nudgeEffectPlanReasonRetryDeadline
	nudgeEffectPlanReasonTargetChanged
	nudgeEffectPlanReasonReadyToClaim
	nudgeEffectPlanReasonAuthorizedExactLaunch
	nudgeEffectPlanReasonProjectionRevisionChanged
	nudgeEffectPlanReasonNoPendingCommand
	nudgeEffectPlanReasonCommandNotPending
	nudgeEffectPlanReasonWaitIdle
	nudgeEffectPlanReasonExpired
	nudgeEffectPlanReasonClaimBusy
	nudgeEffectPlanReasonAuthorizationUnknown
	nudgeEffectPlanReasonAuthorizationDenied
	nudgeEffectPlanReasonClaimInvalid
	nudgeEffectPlanReasonTargetObservationRequired
	nudgeEffectPlanReasonFinalTargetObservationRequired
)

type nudgeEffectCandidateFacts struct {
	operationID    string
	expectedStore  nudgequeue.CommandStoreBinding
	projection     nudgequeue.CommandIndexStatus
	page           nudgequeue.CommandIndexPage
	target         nudgeEffectTarget
	targetObserved bool
	observedAt     time.Time
}

type nudgeEffectCandidatePlan struct {
	disposition         nudgeEffectCandidateDisposition
	reason              nudgeEffectPlanReason
	commandID           string
	boundLaunchIdentity string
	retryAt             time.Time
}

type nudgeEffectPreEntryFacts struct {
	candidate           nudgeEffectCandidatePlan
	request             nudgeEffectClaimRequest
	claimResult         nudgequeue.CommandClaimResult
	finalTarget         nudgeEffectTarget
	finalTargetObserved bool
}

type nudgeEffectPreEntryDisposition uint8

const (
	nudgeEffectPreEntryUnknown nudgeEffectPreEntryDisposition = iota
	nudgeEffectPreEntryPark
	nudgeEffectPreEntryRetry
	nudgeEffectPreEntryReject
	nudgeEffectPreEntryTerminalizeSuperseded
	nudgeEffectPreEntryNeedFinalTarget
	nudgeEffectPreEntryExecute
)

type nudgeEffectPlanAction uint8

const (
	nudgeEffectPlanActionUnknown nudgeEffectPlanAction = iota
	nudgeEffectPlanActionNone
	nudgeEffectPlanActionNudge
)

type nudgeEffectPreEntryPlan struct {
	disposition         nudgeEffectPreEntryDisposition
	action              nudgeEffectPlanAction
	reason              nudgeEffectPlanReason
	commandID           string
	boundLaunchIdentity string
	interactionPolicy   runtime.NudgeInteractionPolicy
}

func planNudgeEffectCandidate(facts nudgeEffectCandidateFacts) nudgeEffectCandidatePlan {
	plan := nudgeEffectCandidatePlan{commandID: facts.operationID}
	if !facts.projection.Synced {
		plan.disposition = nudgeEffectCandidateRetry
		plan.reason = nudgeEffectPlanReasonProjectionUnsynced
		return plan
	}
	if facts.projection.Store != facts.expectedStore || facts.page.Store != facts.expectedStore {
		plan.disposition = nudgeEffectCandidateReject
		plan.reason = nudgeEffectPlanReasonProjectionLineageChanged
		return plan
	}
	if facts.projection.CompletedAuditRevision > facts.projection.Revision ||
		facts.page.CompletedAuditRevision > facts.page.Revision ||
		facts.page.Revision != facts.projection.Revision ||
		facts.page.CompletedAuditRevision != facts.projection.CompletedAuditRevision {
		plan.disposition = nudgeEffectCandidateRetry
		plan.reason = nudgeEffectPlanReasonProjectionRevisionChanged
		return plan
	}
	if len(facts.page.Entries) == 0 {
		plan.disposition = nudgeEffectCandidateNoop
		plan.reason = nudgeEffectPlanReasonNoPendingCommand
		return plan
	}

	head := facts.page.Entries[0]
	if head.Command == nil || head.Command.ID != facts.operationID {
		plan.disposition = nudgeEffectCandidatePark
		plan.reason = nudgeEffectPlanReasonEarlierOrderedHead
		return plan
	}
	command := *head.Command
	if command.State != nudgequeue.CommandStatePending {
		plan.disposition = nudgeEffectCandidateNoop
		plan.reason = nudgeEffectPlanReasonCommandNotPending
		return plan
	}
	if command.Mode == nudgequeue.DeliveryModeWaitIdle {
		plan.disposition = nudgeEffectCandidatePark
		plan.reason = nudgeEffectPlanReasonWaitIdle
		return plan
	}
	if command.Retry != nil && command.Retry.NextEligibleAt != nil && facts.observedAt.Before(*command.Retry.NextEligibleAt) {
		plan.disposition = nudgeEffectCandidateRetry
		plan.reason = nudgeEffectPlanReasonRetryDeadline
		plan.retryAt = command.Retry.NextEligibleAt.UTC()
		return plan
	}
	if facts.observedAt.IsZero() || !facts.observedAt.Before(command.ExpiresAt) {
		plan.disposition = nudgeEffectCandidateNoop
		plan.reason = nudgeEffectPlanReasonExpired
		return plan
	}
	if !facts.targetObserved && facts.target == (nudgeEffectTarget{}) {
		plan.disposition = nudgeEffectCandidateNeedTarget
		plan.reason = nudgeEffectPlanReasonTargetObservationRequired
		return plan
	}
	launch, err := selectNudgeEffectLaunch(command, facts.target)
	if err != nil {
		plan.disposition = nudgeEffectCandidateReject
		plan.reason = nudgeEffectPlanReasonTargetChanged
		return plan
	}
	plan.disposition = nudgeEffectCandidateNeedClaim
	plan.reason = nudgeEffectPlanReasonReadyToClaim
	plan.boundLaunchIdentity = launch
	return plan
}

func planNudgeEffectPreEntry(facts nudgeEffectPreEntryFacts) nudgeEffectPreEntryPlan {
	plan := nudgeEffectPreEntryPlan{
		action:              nudgeEffectPlanActionNone,
		commandID:           facts.candidate.commandID,
		boundLaunchIdentity: facts.candidate.boundLaunchIdentity,
	}
	if facts.candidate.disposition != nudgeEffectCandidateNeedClaim ||
		facts.candidate.reason != nudgeEffectPlanReasonReadyToClaim ||
		facts.request.commandID != facts.candidate.commandID ||
		facts.request.boundLaunchIdentity != facts.candidate.boundLaunchIdentity {
		plan.disposition = nudgeEffectPreEntryReject
		plan.reason = nudgeEffectPlanReasonClaimInvalid
		return plan
	}

	switch facts.claimResult.Disposition {
	case nudgequeue.CommandClaimBusy:
		plan.disposition = nudgeEffectPreEntryPark
		plan.reason = nudgeEffectPlanReasonClaimBusy
		return plan
	case nudgequeue.CommandClaimAuthorizationUnknown:
		plan.disposition = nudgeEffectPreEntryRetry
		plan.reason = nudgeEffectPlanReasonAuthorizationUnknown
		return plan
	case nudgequeue.CommandClaimDenied:
		plan.disposition = nudgeEffectPreEntryReject
		plan.reason = nudgeEffectPlanReasonAuthorizationDenied
		return plan
	case nudgequeue.CommandClaimAllowed:
		// Continue below.
	default:
		plan.disposition = nudgeEffectPreEntryReject
		plan.reason = nudgeEffectPlanReasonClaimInvalid
		return plan
	}
	if err := validateNudgeEffectClaim(facts.claimResult.Command, facts.request); err != nil {
		plan.disposition = nudgeEffectPreEntryReject
		plan.reason = nudgeEffectPlanReasonClaimInvalid
		return plan
	}
	if !facts.finalTargetObserved && facts.finalTarget == (nudgeEffectTarget{}) {
		plan.disposition = nudgeEffectPreEntryNeedFinalTarget
		plan.reason = nudgeEffectPlanReasonFinalTargetObservationRequired
		return plan
	}
	finalLaunch, err := selectNudgeEffectLaunch(facts.claimResult.Command, facts.finalTarget)
	if err != nil || finalLaunch != facts.request.boundLaunchIdentity {
		plan.disposition = nudgeEffectPreEntryTerminalizeSuperseded
		plan.reason = nudgeEffectPlanReasonTargetChanged
		return plan
	}

	plan.disposition = nudgeEffectPreEntryExecute
	plan.action = nudgeEffectPlanActionNudge
	plan.reason = nudgeEffectPlanReasonAuthorizedExactLaunch
	plan.boundLaunchIdentity = finalLaunch
	plan.interactionPolicy = runtime.NudgeInteractionRequireUnattachedNormal
	return plan
}
