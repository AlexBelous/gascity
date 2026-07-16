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
)

type nudgeEffectCandidateFacts struct {
	operationID   string
	expectedStore nudgequeue.CommandStoreBinding
	projection    nudgequeue.CommandIndexStatus
	page          nudgequeue.CommandIndexPage
	target        nudgeEffectTarget
	observedAt    time.Time
}

type nudgeEffectCandidatePlan struct {
	disposition         nudgeEffectCandidateDisposition
	reason              nudgeEffectPlanReason
	commandID           string
	boundLaunchIdentity string
	retryAt             time.Time
}

type nudgeEffectPreEntryFacts struct {
	candidate   nudgeEffectCandidatePlan
	request     nudgeEffectClaimRequest
	claimResult nudgequeue.CommandClaimResult
	finalTarget nudgeEffectTarget
}

type nudgeEffectPreEntryDisposition uint8

const (
	nudgeEffectPreEntryUnknown nudgeEffectPreEntryDisposition = iota
	nudgeEffectPreEntryPark
	nudgeEffectPreEntryRetry
	nudgeEffectPreEntryReject
	nudgeEffectPreEntryTerminalizeSuperseded
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

func planNudgeEffectCandidate(nudgeEffectCandidateFacts) nudgeEffectCandidatePlan {
	return nudgeEffectCandidatePlan{}
}

func planNudgeEffectPreEntry(nudgeEffectPreEntryFacts) nudgeEffectPreEntryPlan {
	return nudgeEffectPreEntryPlan{}
}
