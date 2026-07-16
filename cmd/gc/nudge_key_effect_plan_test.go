package main

import (
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/nudgequeue"
	"github.com/gastownhall/gascity/internal/runtime"
)

func TestPlanNudgeEffectCandidateParksBehindEarlierOrderedHead(t *testing.T) {
	now := testNudgeEffectTime()
	earlier := immediateNudgeEffectCommand(now)
	requested := immediateNudgeEffectCommand(now)
	requested.ID = "command-immediate-2"
	requested.Order = nudgequeue.CommandOrder{Sequence: 2, Revision: 2}
	requested.TrustedIngress.PayloadDigest = nudgequeue.ComputeCommandPayloadDigest(requested)

	got := planNudgeEffectCandidate(nudgeEffectCandidateFacts{
		operationID:   requested.ID,
		expectedStore: requested.Store,
		projection: nudgequeue.CommandIndexStatus{
			Store:                  requested.Store,
			Revision:               2,
			CompletedAuditRevision: 2,
			Synced:                 true,
		},
		page: nudgequeue.CommandIndexPage{
			Store:                  requested.Store,
			Revision:               2,
			CompletedAuditRevision: 2,
			Entries:                []nudgequeue.CommandIndexEntry{{Command: &earlier}},
		},
		target:     currentNudgeEffectPlanTarget(),
		observedAt: now,
	})
	want := nudgeEffectCandidatePlan{
		disposition: nudgeEffectCandidatePark,
		reason:      nudgeEffectPlanReasonEarlierOrderedHead,
		commandID:   requested.ID,
	}
	if got != want {
		t.Fatalf("candidate plan = %#v, want %#v", got, want)
	}
}

func TestPlanNudgeEffectCandidateFailsClosedOnProjectionIntegrity(t *testing.T) {
	now := testNudgeEffectTime()
	command := immediateNudgeEffectCommand(now)
	foreignStore := nudgequeue.CommandStoreBinding{StoreUUID: "foreign-effect-store", RestoreEpoch: 1}

	tests := []struct {
		name       string
		projection nudgequeue.CommandIndexStatus
		page       nudgequeue.CommandIndexPage
		want       nudgeEffectCandidatePlan
	}{
		{
			name: "unsynced projection retries without selecting a command",
			projection: nudgequeue.CommandIndexStatus{
				Store:          command.Store,
				Revision:       1,
				Synced:         false,
				UnsyncedReason: "revision gap",
			},
			want: nudgeEffectCandidatePlan{
				disposition: nudgeEffectCandidateRetry,
				reason:      nudgeEffectPlanReasonProjectionUnsynced,
				commandID:   command.ID,
			},
		},
		{
			name: "status lineage mismatch rejects provider admission",
			projection: nudgequeue.CommandIndexStatus{
				Store:                  foreignStore,
				Revision:               1,
				CompletedAuditRevision: 1,
				Synced:                 true,
			},
			page: nudgeEffectPlanPage(command),
			want: nudgeEffectCandidatePlan{
				disposition: nudgeEffectCandidateReject,
				reason:      nudgeEffectPlanReasonProjectionLineageChanged,
				commandID:   command.ID,
			},
		},
		{
			name:       "page lineage mismatch rejects provider admission",
			projection: nudgeEffectPlanProjection(command),
			page: nudgequeue.CommandIndexPage{
				Store:                  foreignStore,
				Revision:               1,
				CompletedAuditRevision: 1,
				Entries:                []nudgequeue.CommandIndexEntry{{Command: &command}},
			},
			want: nudgeEffectCandidatePlan{
				disposition: nudgeEffectCandidateReject,
				reason:      nudgeEffectPlanReasonProjectionLineageChanged,
				commandID:   command.ID,
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := planNudgeEffectCandidate(nudgeEffectCandidateFacts{
				operationID:   command.ID,
				expectedStore: command.Store,
				projection:    test.projection,
				page:          test.page,
				target:        currentNudgeEffectPlanTarget(),
				observedAt:    now,
			})
			if got != test.want {
				t.Fatalf("candidate plan = %#v, want %#v", got, test.want)
			}
		})
	}
}

func TestPlanNudgeEffectCandidateRetriesAtExactDeadline(t *testing.T) {
	now := testNudgeEffectTime()
	command := immediateNudgeEffectCommand(now)
	retryAt := now.Add(45 * time.Second)
	command.Retry = &nudgequeue.CommandRetry{NextEligibleAt: &retryAt}

	got := planNudgeEffectCandidate(nudgeEffectCandidateFacts{
		operationID:   command.ID,
		expectedStore: command.Store,
		projection:    nudgeEffectPlanProjection(command),
		page:          nudgeEffectPlanPage(command),
		target:        currentNudgeEffectPlanTarget(),
		observedAt:    now,
	})
	want := nudgeEffectCandidatePlan{
		disposition: nudgeEffectCandidateRetry,
		reason:      nudgeEffectPlanReasonRetryDeadline,
		commandID:   command.ID,
		retryAt:     retryAt,
	}
	if got != want {
		t.Fatalf("candidate plan = %#v, want %#v", got, want)
	}
}

func TestPlanNudgeEffectCandidateRejectsStaleTargetIdentity(t *testing.T) {
	now := testNudgeEffectTime()
	command := immediateNudgeEffectCommand(now)

	tests := []struct {
		name   string
		target nudgeEffectTarget
	}{
		{
			name: "intent generation changed",
			target: func() nudgeEffectTarget {
				target := currentNudgeEffectPlanTarget()
				target.intentGeneration++
				return target
			}(),
		},
		{
			name: "launch identity changed",
			target: func() nudgeEffectTarget {
				target := currentNudgeEffectPlanTarget()
				target.launchIdentity = "launch-2"
				return target
			}(),
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := planNudgeEffectCandidate(nudgeEffectCandidateFacts{
				operationID:   command.ID,
				expectedStore: command.Store,
				projection:    nudgeEffectPlanProjection(command),
				page:          nudgeEffectPlanPage(command),
				target:        test.target,
				observedAt:    now,
			})
			want := nudgeEffectCandidatePlan{
				disposition: nudgeEffectCandidateReject,
				reason:      nudgeEffectPlanReasonTargetChanged,
				commandID:   command.ID,
			}
			if got != want {
				t.Fatalf("candidate plan = %#v, want %#v", got, want)
			}
		})
	}
}

func TestPlanNudgeEffectCandidateSelectsClaimForCurrentExactLaunch(t *testing.T) {
	now := testNudgeEffectTime()
	command := immediateNudgeEffectCommand(now)
	target := currentNudgeEffectPlanTarget()

	got := planNudgeEffectCandidate(nudgeEffectCandidateFacts{
		operationID:   command.ID,
		expectedStore: command.Store,
		projection:    nudgeEffectPlanProjection(command),
		page:          nudgeEffectPlanPage(command),
		target:        target,
		observedAt:    now,
	})
	want := nudgeEffectCandidatePlan{
		disposition:         nudgeEffectCandidateNeedClaim,
		reason:              nudgeEffectPlanReasonReadyToClaim,
		commandID:           command.ID,
		boundLaunchIdentity: target.launchIdentity,
	}
	if got != want {
		t.Fatalf("candidate plan = %#v, want %#v", got, want)
	}
}

func TestPlanNudgeEffectPreEntryExecutesAuthorizedExactLaunch(t *testing.T) {
	now := testNudgeEffectTime()
	command := immediateNudgeEffectCommand(now)
	target := currentNudgeEffectPlanTarget()
	candidate := nudgeEffectCandidatePlan{
		disposition:         nudgeEffectCandidateNeedClaim,
		reason:              nudgeEffectPlanReasonReadyToClaim,
		commandID:           command.ID,
		boundLaunchIdentity: target.launchIdentity,
	}

	request := nudgeEffectClaimRequest{
		commandID:           command.ID,
		claimID:             "claim-1",
		ownerID:             "owner-1",
		attemptID:           "attempt-1",
		boundLaunchIdentity: target.launchIdentity,
		claimedAt:           now,
		leaseUntil:          now.Add(time.Minute),
	}
	claimed := command
	claimed.State = nudgequeue.CommandStateInFlight
	claimed.Order.Revision++
	claimed.Claim = &nudgequeue.CommandClaim{
		ID:                         request.claimID,
		OwnerID:                    request.ownerID,
		OperationID:                request.commandID,
		AttemptID:                  request.attemptID,
		BoundLaunchIdentity:        request.boundLaunchIdentity,
		AuthorizationDecisionID:    "claim-decision-1",
		AuthorizationPolicyVersion: "policy-v2",
		ClaimedAt:                  request.claimedAt,
		LeaseUntil:                 request.leaseUntil,
	}
	claimed.Retry = &nudgequeue.CommandRetry{
		AttemptCount:               1,
		LastAttemptAt:              request.claimedAt,
		ClaimID:                    request.claimID,
		OperationID:                request.commandID,
		AttemptID:                  request.attemptID,
		BoundLaunchIdentity:        request.boundLaunchIdentity,
		AuthorizationDecisionID:    claimed.Claim.AuthorizationDecisionID,
		AuthorizationPolicyVersion: claimed.Claim.AuthorizationPolicyVersion,
	}

	got := planNudgeEffectPreEntry(nudgeEffectPreEntryFacts{
		candidate: candidate,
		request:   request,
		claimResult: nudgequeue.CommandClaimResult{
			Disposition: nudgequeue.CommandClaimAllowed,
			Command:     claimed,
		},
		finalTarget: target,
	})
	want := nudgeEffectPreEntryPlan{
		disposition:         nudgeEffectPreEntryExecute,
		action:              nudgeEffectPlanActionNudge,
		reason:              nudgeEffectPlanReasonAuthorizedExactLaunch,
		commandID:           command.ID,
		boundLaunchIdentity: target.launchIdentity,
		interactionPolicy:   runtime.NudgeInteractionRequireUnattachedNormal,
	}
	if got != want {
		t.Fatalf("pre-entry plan = %#v, want %#v", got, want)
	}
}

func nudgeEffectPlanProjection(command nudgequeue.Command) nudgequeue.CommandIndexStatus {
	return nudgequeue.CommandIndexStatus{
		Store:                  command.Store,
		Revision:               command.Order.Revision,
		CompletedAuditRevision: command.Order.Revision,
		Synced:                 true,
	}
}

func nudgeEffectPlanPage(command nudgequeue.Command) nudgequeue.CommandIndexPage {
	return nudgequeue.CommandIndexPage{
		Store:                  command.Store,
		Revision:               command.Order.Revision,
		CompletedAuditRevision: command.Order.Revision,
		Entries:                []nudgequeue.CommandIndexEntry{{Command: &command}},
	}
}

func currentNudgeEffectPlanTarget() nudgeEffectTarget {
	return nudgeEffectTarget{
		sessionID:        "session-1",
		sessionName:      "city--worker",
		intentGeneration: 7,
		launchIdentity:   "launch-1",
		provider:         "fake",
	}
}
