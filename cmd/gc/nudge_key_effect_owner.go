package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/gastownhall/gascity/internal/nudgequeue"
	"github.com/gastownhall/gascity/internal/reconcilekey"
	"github.com/gastownhall/gascity/internal/runtime"
	"github.com/gastownhall/gascity/internal/worker"
)

var errNudgeEffectTargetChanged = errors.New("durable nudge target changed")

const (
	defaultNudgeEffectClaimLease        = 30 * time.Second
	defaultNudgeEffectCompletionTimeout = 10 * time.Second
	defaultNudgeEffectRetryDelay        = 100 * time.Millisecond
	nudgeTargetSupersededDetail         = "target generation or launch identity changed after claim"
	nudgeProviderAmbiguousDetail        = "provider may have accepted the nudge without a durable result"
	nudgeProviderRejectedDetail         = "provider definitively rejected the nudge"
	nudgeProviderNotEnteredDetail       = "provider proved that native nudge entry did not occur"
	nudgeProviderInvalidEvidenceDetail  = "provider returned incomplete or contradictory delivery evidence"
)

type nudgeEffectClaimRequest struct {
	commandID           string
	claimID             string
	ownerID             string
	attemptID           string
	boundLaunchIdentity string
	claimedAt           time.Time
	leaseUntil          time.Time
}

// nudgeCommandEffectSource is the city-bound command capability used by the
// first effect owner. Implementations inject their opaque trusted partition;
// callers cannot provide or substitute city authority in the claim request.
type nudgeCommandEffectSource interface {
	nudgeCommandSource
	ClaimAuthorized(context.Context, nudgeEffectClaimRequest, nudgequeue.NudgeClaimAuthorizer) (nudgequeue.CommandClaimResult, error)
	CompleteProviderAttempt(context.Context, nudgequeue.CommandCompletionRequest) (nudgequeue.CommandCompletionResult, error)
	RetryProviderAttempt(context.Context, nudgequeue.CommandRetryRequest) (nudgequeue.CommandRetryResult, error)
}

type nudgeEffectTargetReader interface {
	Read(context.Context, string) (nudgeEffectTarget, error)
}

type nudgeEffectHandleFactory interface {
	Handle(nudgeEffectTarget) (worker.Handle, error)
}

type nudgeEffectIDGenerator func(string) (string, error)

type nudgeKeyEffectOwnerConfig struct {
	reader            *nudgeKeyReadShadow
	source            nudgeCommandEffectSource
	authorizer        nudgequeue.NudgeClaimAuthorizer
	targets           nudgeEffectTargetReader
	handles           nudgeEffectHandleFactory
	ownerID           string
	now               func() time.Time
	newID             nudgeEffectIDGenerator
	claimLease        time.Duration
	completionTimeout time.Duration
	retryDelay        time.Duration
}

// nudgeKeyEffectOwner executes at most one exact pending command per callback.
// The workqueue still owns same-key serialization; durable Claim+Retry owns the
// cross-callback and cross-process may-enter fence.
type nudgeKeyEffectOwner struct {
	reader            *nudgeKeyReadShadow
	source            nudgeCommandEffectSource
	authorizer        nudgequeue.NudgeClaimAuthorizer
	targets           nudgeEffectTargetReader
	handles           nudgeEffectHandleFactory
	ownerID           string
	now               func() time.Time
	newID             nudgeEffectIDGenerator
	claimLease        time.Duration
	completionTimeout time.Duration
	retryDelay        time.Duration
}

func newNudgeKeyEffectOwner(config nudgeKeyEffectOwnerConfig) (*nudgeKeyEffectOwner, error) {
	if config.reader == nil || config.source == nil || config.authorizer == nil || config.targets == nil || config.handles == nil {
		return nil, errors.New("creating keyed nudge effect owner: dependencies are incomplete")
	}
	if config.reader.source != config.source {
		return nil, errors.New("creating keyed nudge effect owner: reader and effect source differ")
	}
	if strings.TrimSpace(config.ownerID) == "" || config.ownerID != strings.TrimSpace(config.ownerID) {
		return nil, errors.New("creating keyed nudge effect owner: owner id is not canonical")
	}
	if config.now == nil || config.newID == nil {
		return nil, errors.New("creating keyed nudge effect owner: clock and id generator are required")
	}
	if config.claimLease <= 0 {
		config.claimLease = defaultNudgeEffectClaimLease
	}
	if config.completionTimeout <= 0 {
		config.completionTimeout = defaultNudgeEffectCompletionTimeout
	}
	if config.retryDelay <= 0 {
		config.retryDelay = defaultNudgeEffectRetryDelay
	}
	return &nudgeKeyEffectOwner{
		reader:            config.reader,
		source:            config.source,
		authorizer:        config.authorizer,
		targets:           config.targets,
		handles:           config.handles,
		ownerID:           config.ownerID,
		now:               config.now,
		newID:             config.newID,
		claimLease:        config.claimLease,
		completionTimeout: config.completionTimeout,
		retryDelay:        config.retryDelay,
	}, nil
}

// reconcile executes at most the first ordered pending command. Once a claim
// commits, final validation/provider entry and marker-last persistence each get
// their own detached bounded context. Cancellation of the queue callback, or a
// provider consuming its entire deadline, must not erase the persistence budget
// for terminal evidence.
func (o *nudgeKeyEffectOwner) reconcile(ctx context.Context, key reconcilekey.Session, batch nudgeReconcileBatch) nudgeReconcileOutcome {
	if o == nil {
		return nudgeReconcileInvariant(errors.New("reconciling keyed nudge effect: owner is nil"))
	}
	if ctx == nil {
		return nudgeReconcileInvariant(errors.New("reconciling keyed nudge effect: context is nil"))
	}

	readOutcome := o.reader.reconcile(ctx, key, batch)
	switch readOutcome.disposition {
	case nudgeReconcileOutcomeForget, nudgeReconcileOutcomeContinue:
		// The index itself is already a complete immutable projection. Continue is
		// bounded read-walk scheduling, not a reason to delay its ordered head.
	case nudgeReconcileOutcomeAudit, nudgeReconcileOutcomeTransient, nudgeReconcileOutcomeInvariant:
		return readOutcome
	default:
		return nudgeReconcileInvariant(fmt.Errorf("reconciling keyed nudge effect: unknown read disposition %d", readOutcome.disposition))
	}
	if ctx.Err() != nil {
		return nudgeReconcileSuccess()
	}

	facts, outcome := o.candidateFacts(key, o.now().UTC())
	if outcome.disposition != nudgeReconcileOutcomeForget || outcome.err != nil {
		return outcome
	}
	var candidate nudgeEffectCandidatePlan
	for {
		candidate = planNudgeEffectCandidate(facts)
		if candidate.disposition != nudgeEffectCandidateNeedTarget {
			break
		}
		command := facts.page.Entries[0].Command
		preClaimTarget, err := o.targets.Read(ctx, command.Target.SessionID)
		if err != nil {
			if ctx.Err() != nil {
				return nudgeReconcileSuccess()
			}
			return nudgeReconcileTransient(fmt.Errorf("reading keyed nudge target before claim: %w", err))
		}
		facts.target = preClaimTarget
		facts.targetObserved = true
	}
	switch candidate.disposition {
	case nudgeEffectCandidateNoop, nudgeEffectCandidatePark:
		return nudgeReconcileSuccess()
	case nudgeEffectCandidateRetry:
		if candidate.reason == nudgeEffectPlanReasonRetryDeadline {
			return nudgeReconcileAtRetryDeadline(candidate.retryAt)
		}
		return nudgeReconcileAudit()
	case nudgeEffectCandidateReject:
		if candidate.reason == nudgeEffectPlanReasonTargetChanged {
			return nudgeReconcileSuccess()
		}
		return nudgeReconcileInvariant(fmt.Errorf("planning keyed nudge candidate %q: rejected reason %d", candidate.commandID, candidate.reason))
	case nudgeEffectCandidateNeedClaim:
		// Continue below.
	default:
		return nudgeReconcileInvariant(fmt.Errorf("planning keyed nudge candidate %q: unknown disposition %d", candidate.commandID, candidate.disposition))
	}
	command := *facts.page.Entries[0].Command

	claimedAt := o.now().UTC()
	if !claimedAt.Before(command.ExpiresAt) {
		return nudgeReconcileSuccess()
	}
	leaseUntil := claimedAt.Add(o.claimLease)
	if leaseUntil.After(command.ExpiresAt) {
		leaseUntil = command.ExpiresAt
	}
	if !leaseUntil.After(claimedAt) {
		return nudgeReconcileSuccess()
	}
	claimID, err := o.newID("claim")
	if err != nil {
		return nudgeReconcileInvariant(fmt.Errorf("allocating keyed nudge claim id: %w", err))
	}
	attemptID, err := o.newID("attempt")
	if err != nil {
		return nudgeReconcileInvariant(fmt.Errorf("allocating keyed nudge attempt id: %w", err))
	}
	claimRequest := nudgeEffectClaimRequest{
		commandID:           candidate.commandID,
		claimID:             claimID,
		ownerID:             o.ownerID,
		attemptID:           attemptID,
		boundLaunchIdentity: candidate.boundLaunchIdentity,
		claimedAt:           claimedAt,
		leaseUntil:          leaseUntil,
	}
	claimResult, err := o.source.ClaimAuthorized(ctx, claimRequest, o.authorizer)
	if err != nil {
		if ctx.Err() != nil {
			return nudgeReconcileSuccess()
		}
		if errors.Is(err, nudgequeue.ErrNudgeAuthorizationUnknown) && claimResult.Disposition == nudgequeue.CommandClaimAuthorizationUnknown {
			if _, _, refreshErr := o.reader.acceptCommandHint(ctx, command.ID); refreshErr != nil {
				return o.reader.sourceFailureOutcome(newNudgeCommandSourceFailure(o.source, fmt.Errorf("refreshing authorization-unknown keyed nudge command: %w", refreshErr)))
			}
			return nudgeReconcileTransient(fmt.Errorf("claiming keyed nudge command: %w", err))
		}
		return o.reader.sourceFailureOutcome(newNudgeCommandSourceFailure(o.source, fmt.Errorf("claiming keyed nudge command: %w", err)))
	}
	preEntryFacts := nudgeEffectPreEntryFacts{
		candidate:   candidate,
		request:     claimRequest,
		claimResult: claimResult,
	}
	providerCtx := ctx
	cancelProvider := func() {}
	providerContextOpened := false
	var preEntry nudgeEffectPreEntryPlan
	for {
		preEntry = planNudgeEffectPreEntry(preEntryFacts)
		if preEntry.disposition != nudgeEffectPreEntryNeedFinalTarget {
			break
		}
		providerCtx, cancelProvider = context.WithTimeout(context.WithoutCancel(ctx), o.completionTimeout)
		providerContextOpened = true
		defer cancelProvider()
		claimed, err := o.refreshClaimedCommand(providerCtx, command.ID, claimRequest)
		if err != nil {
			return o.reader.sourceFailureOutcome(newNudgeCommandSourceFailure(o.source, err))
		}
		finalTarget, err := o.targets.Read(providerCtx, claimed.Target.SessionID)
		if err != nil {
			return nudgeReconcileTransient(fmt.Errorf("reading keyed nudge target after claim: %w", err))
		}
		preEntryFacts.claimResult.Command = claimed
		preEntryFacts.finalTarget = finalTarget
		preEntryFacts.finalTargetObserved = true
	}
	switch preEntry.disposition {
	case nudgeEffectPreEntryPark, nudgeEffectPreEntryRetry:
		if _, _, refreshErr := o.reader.acceptCommandHint(ctx, command.ID); refreshErr != nil && ctx.Err() == nil {
			return o.reader.sourceFailureOutcome(newNudgeCommandSourceFailure(o.source, fmt.Errorf("refreshing unentered keyed nudge claim result: %w", refreshErr)))
		}
		return nudgeReconcileSuccess()
	case nudgeEffectPreEntryReject:
		if preEntry.reason == nudgeEffectPlanReasonAuthorizationDenied {
			if _, _, refreshErr := o.reader.acceptCommandHint(ctx, command.ID); refreshErr != nil && ctx.Err() == nil {
				return o.reader.sourceFailureOutcome(newNudgeCommandSourceFailure(o.source, fmt.Errorf("refreshing denied keyed nudge claim result: %w", refreshErr)))
			}
			return nudgeReconcileSuccess()
		}
		return nudgeReconcileInvariant(fmt.Errorf("planning keyed nudge pre-entry %q: rejected reason %d", preEntry.commandID, preEntry.reason))
	case nudgeEffectPreEntryTerminalizeSuperseded:
		cancelProvider()
		return o.completeAttemptDetached(ctx, key, preEntryFacts.claimResult.Command, nudgeProviderCompletion{
			terminal:      true,
			actionResult:  nudgequeue.CommandActionResultSuperseded,
			errorClass:    nudgequeue.CommandErrorClassSuperseded,
			detail:        nudgeTargetSupersededDetail,
			providerStage: nudgequeue.ProviderStageNotEntered,
			completion:    nudgequeue.CompletionStateNotCompleted,
		})
	case nudgeEffectPreEntryExecute:
		if !providerContextOpened || preEntry.action != nudgeEffectPlanActionNudge {
			return nudgeReconcileInvariant(errors.New("planning keyed nudge pre-entry: execute plan has no bounded provider context or nudge action"))
		}
		// Continue below.
	default:
		return nudgeReconcileInvariant(fmt.Errorf("planning keyed nudge pre-entry %q: unknown disposition %d", preEntry.commandID, preEntry.disposition))
	}

	claimed := preEntryFacts.claimResult.Command
	finalTarget := preEntryFacts.finalTarget
	handle, err := o.handles.Handle(finalTarget)
	if err != nil {
		return nudgeReconcileTransient(fmt.Errorf("constructing keyed nudge worker handle: %w", err))
	}
	result, effectErr := handle.Nudge(providerCtx, worker.NudgeRequest{
		Text:     claimed.Message,
		Delivery: worker.NudgeDeliveryImmediate,
		Source:   string(claimed.Source),
		Wake:     worker.NudgeWakeLiveOnly,
		Effect: &runtime.NudgeEffectContract{
			OperationID:            claimed.Claim.OperationID,
			ExpectedLaunchIdentity: preEntry.boundLaunchIdentity,
			InteractionPolicy:      preEntry.interactionPolicy,
		},
	})
	completion := mapNudgeProviderCompletion(result, effectErr)
	if !completion.terminal {
		cancelProvider()
		return o.retryAttemptDetached(ctx, key, claimed, completion)
	}
	cancelProvider()
	return o.completeAttemptDetached(ctx, key, claimed, completion)
}

func (o *nudgeKeyEffectOwner) candidateFacts(key reconcilekey.Session, observedAt time.Time) (nudgeEffectCandidateFacts, nudgeReconcileOutcome) {
	status := o.reader.index.Status()
	facts := nudgeEffectCandidateFacts{
		expectedStore: o.reader.store,
		projection:    status,
		observedAt:    observedAt,
	}
	if !status.Synced {
		return facts, nudgeReconcileSuccess()
	}
	page, err := o.reader.index.Page(key.SessionID(), 0, 1)
	if err != nil {
		if errors.Is(err, nudgequeue.ErrCommandIndexUnsynced) {
			facts.projection = o.reader.index.Status()
			facts.projection.Synced = false
			return facts, nudgeReconcileSuccess()
		}
		return nudgeEffectCandidateFacts{}, nudgeReconcileInvariant(fmt.Errorf("reading keyed nudge ordered head: %w", err))
	}
	facts.page = page
	if len(page.Entries) != 0 {
		facts.operationID = nudgeCommandEntryID(page.Entries[0])
	}
	return facts, nudgeReconcileSuccess()
}

func validateNudgeEffectClaim(command nudgequeue.Command, request nudgeEffectClaimRequest) error {
	if command.ID != request.commandID || command.State != nudgequeue.CommandStateInFlight || command.Claim == nil || command.Retry == nil ||
		command.Claim.ID != request.claimID || command.Claim.OwnerID != request.ownerID ||
		command.Claim.OperationID != request.commandID || command.Claim.AttemptID != request.attemptID ||
		command.Claim.BoundLaunchIdentity != request.boundLaunchIdentity ||
		!command.Claim.ClaimedAt.Equal(request.claimedAt) || !command.Claim.LeaseUntil.Equal(request.leaseUntil) ||
		command.Retry.ClaimID != request.claimID || command.Retry.OperationID != request.commandID ||
		command.Retry.AttemptID != request.attemptID || command.Retry.BoundLaunchIdentity != request.boundLaunchIdentity {
		return errors.New("claiming keyed nudge command: allowed result does not contain the exact durable claim")
	}
	return nil
}

func (o *nudgeKeyEffectOwner) refreshClaimedCommand(ctx context.Context, commandID string, request nudgeEffectClaimRequest) (nudgequeue.Command, error) {
	if _, found, err := o.reader.acceptCommandHint(ctx, commandID); err != nil {
		return nudgequeue.Command{}, fmt.Errorf("refreshing durable keyed nudge claim: %w", err)
	} else if !found {
		return nudgequeue.Command{}, errors.New("refreshing durable keyed nudge claim: command disappeared")
	}
	resolution, err := o.reader.index.Resolve(commandID)
	if err != nil {
		return nudgequeue.Command{}, fmt.Errorf("resolving refreshed keyed nudge claim: %w", err)
	}
	if !resolution.Found || resolution.Entry.Command == nil {
		return nudgequeue.Command{}, errors.New("resolving refreshed keyed nudge claim: exact known command is absent")
	}
	command := *resolution.Entry.Command
	if err := validateNudgeEffectClaim(command, request); err != nil {
		return nudgequeue.Command{}, err
	}
	return command, nil
}

func (o *nudgeKeyEffectOwner) completeAttempt(ctx context.Context, key reconcilekey.Session, command nudgequeue.Command, completion nudgeProviderCompletion) nudgeReconcileOutcome {
	return o.completeAttemptAt(ctx, key, command, completion, o.now().UTC())
}

func (o *nudgeKeyEffectOwner) completeAttemptAt(ctx context.Context, key reconcilekey.Session, command nudgequeue.Command, completion nudgeProviderCompletion, completedAt time.Time) nudgeReconcileOutcome {
	if command.Claim == nil || !completion.terminal {
		return nudgeReconcileInvariant(errors.New("completing keyed nudge attempt: terminal completion has no durable claim"))
	}
	request := nudgequeue.CommandCompletionRequest{
		CommandID:     command.ID,
		ClaimID:       command.Claim.ID,
		OperationID:   command.Claim.OperationID,
		AttemptID:     command.Claim.AttemptID,
		CompletedAt:   completedAt.UTC(),
		ActionResult:  completion.actionResult,
		ErrorClass:    completion.errorClass,
		Detail:        completion.detail,
		ProviderStage: completion.providerStage,
		Completion:    completion.completion,
	}
	result, err := o.source.CompleteProviderAttempt(ctx, request)
	if err != nil {
		// An exact reread can resolve response loss without another provider entry.
		if _, _, refreshErr := o.reader.acceptCommandHint(ctx, command.ID); refreshErr == nil {
			if resolved, resolveErr := o.reader.index.Resolve(command.ID); resolveErr == nil && resolved.Found && resolved.Entry.Command != nil && resolved.Entry.Command.Terminal != nil {
				return o.nextAfterCompletion(key)
			}
		}
		return o.reader.sourceFailureOutcome(newNudgeCommandSourceFailure(o.source, fmt.Errorf("persisting keyed nudge completion: %w", err)))
	}
	if result.Disposition != nudgequeue.CommandCompletionRecorded && result.Disposition != nudgequeue.CommandCompletionAlreadyRecorded {
		return nudgeReconcileInvariant(fmt.Errorf("persisting keyed nudge completion: disposition %q does not own the exact attempt", result.Disposition))
	}
	if _, found, err := o.reader.acceptCommandHint(ctx, command.ID); err != nil {
		return o.reader.sourceFailureOutcome(newNudgeCommandSourceFailure(o.source, fmt.Errorf("refreshing keyed nudge completion: %w", err)))
	} else if !found {
		return nudgeReconcileInvariant(errors.New("refreshing keyed nudge completion: command disappeared"))
	}
	return o.nextAfterCompletion(key)
}

func (o *nudgeKeyEffectOwner) retryAttempt(ctx context.Context, key reconcilekey.Session, command nudgequeue.Command, completion nudgeProviderCompletion) nudgeReconcileOutcome {
	if command.Claim == nil || command.Retry == nil || completion.terminal ||
		completion.providerStage != nudgequeue.ProviderStageNotEntered || completion.completion != nudgequeue.CompletionStateNotCompleted {
		return nudgeReconcileInvariant(errors.New("retrying keyed nudge attempt: exact definite-non-entry claim evidence is required"))
	}
	observedAt := o.now().UTC()
	if observedAt.Before(command.Claim.ClaimedAt) {
		return nudgeReconcileInvariant(errors.New("retrying keyed nudge attempt: clock regressed before claim"))
	}
	nextEligibleAt := observedAt.Add(o.retryDelay)
	if !nextEligibleAt.Before(command.ExpiresAt) {
		return o.completeAttemptAt(ctx, key, command, nudgeProviderCompletion{
			terminal:      true,
			actionResult:  nudgequeue.CommandActionResultExpired,
			errorClass:    nudgequeue.CommandErrorClassExpired,
			detail:        "command expired before a safe provider retry",
			providerStage: nudgequeue.ProviderStageNotEntered,
			completion:    nudgequeue.CompletionStateNotCompleted,
		}, command.ExpiresAt)
	}
	request := nudgequeue.CommandRetryRequest{
		CommandID:      command.ID,
		ClaimID:        command.Claim.ID,
		OperationID:    command.Claim.OperationID,
		AttemptID:      command.Claim.AttemptID,
		ObservedAt:     observedAt,
		NextEligibleAt: nextEligibleAt,
		ErrorClass:     nudgequeue.CommandErrorClassProviderBusy,
		Detail:         nudgeProviderNotEnteredDetail,
		ProviderStage:  completion.providerStage,
		Completion:     completion.completion,
	}
	result, err := o.source.RetryProviderAttempt(ctx, request)
	if err != nil {
		if _, _, refreshErr := o.reader.acceptCommandHint(ctx, command.ID); refreshErr == nil &&
			o.pendingRetryMatches(command.ID, request) {
			return nudgeReconcileAtRetryDeadline(request.NextEligibleAt)
		}
		return o.reader.sourceFailureOutcome(newNudgeCommandSourceFailure(o.source, fmt.Errorf("persisting keyed nudge retry: %w", err)))
	}
	if result.Disposition != nudgequeue.CommandRetryRecorded && result.Disposition != nudgequeue.CommandRetryAlreadyRecorded {
		return nudgeReconcileInvariant(fmt.Errorf("persisting keyed nudge retry: disposition %q does not own the exact attempt", result.Disposition))
	}
	if _, found, err := o.reader.acceptCommandHint(ctx, command.ID); err != nil {
		return o.reader.sourceFailureOutcome(newNudgeCommandSourceFailure(o.source, fmt.Errorf("refreshing keyed nudge retry: %w", err)))
	} else if !found {
		return nudgeReconcileInvariant(errors.New("refreshing keyed nudge retry: command disappeared"))
	}
	if !o.pendingRetryMatches(command.ID, request) {
		return nudgeReconcileInvariant(errors.New("refreshing keyed nudge retry: exact pending retry is absent"))
	}
	return nudgeReconcileAtRetryDeadline(request.NextEligibleAt)
}

func (o *nudgeKeyEffectOwner) pendingRetryMatches(commandID string, request nudgequeue.CommandRetryRequest) bool {
	resolved, err := o.reader.index.Resolve(commandID)
	if err != nil || !resolved.Found || resolved.Entry.Command == nil {
		return false
	}
	command := resolved.Entry.Command
	return command.State == nudgequeue.CommandStatePending && command.Claim == nil && command.Terminal == nil && command.Retry != nil &&
		command.Retry.ClaimID == request.ClaimID && command.Retry.OperationID == request.OperationID &&
		command.Retry.AttemptID == request.AttemptID && command.Retry.NextEligibleAt != nil &&
		command.Retry.NextEligibleAt.Equal(request.NextEligibleAt) && command.Retry.ErrorClass == request.ErrorClass &&
		command.Retry.ErrorDetail == request.Detail
}

func (o *nudgeKeyEffectOwner) retryAttemptDetached(parent context.Context, key reconcilekey.Session, command nudgequeue.Command, completion nudgeProviderCompletion) nudgeReconcileOutcome {
	if parent == nil {
		return nudgeReconcileInvariant(errors.New("retrying keyed nudge attempt: parent context is nil"))
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(parent), o.completionTimeout)
	defer cancel()
	return o.retryAttempt(ctx, key, command, completion)
}

// completeAttemptDetached gives terminal marker-last persistence a fresh
// bounded budget that is independent of both the queue callback and the
// provider phase. It never invokes the provider or creates a new claim.
func (o *nudgeKeyEffectOwner) completeAttemptDetached(parent context.Context, key reconcilekey.Session, command nudgequeue.Command, completion nudgeProviderCompletion) nudgeReconcileOutcome {
	if parent == nil {
		return nudgeReconcileInvariant(errors.New("completing keyed nudge attempt: parent context is nil"))
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(parent), o.completionTimeout)
	defer cancel()
	return o.completeAttempt(ctx, key, command, completion)
}

func (o *nudgeKeyEffectOwner) nextAfterCompletion(key reconcilekey.Session) nudgeReconcileOutcome {
	page, err := o.reader.index.Page(key.SessionID(), 0, 1)
	if err != nil {
		if errors.Is(err, nudgequeue.ErrCommandIndexUnsynced) {
			return nudgeReconcileAudit()
		}
		return nudgeReconcileInvariant(fmt.Errorf("checking keyed nudge continuation: %w", err))
	}
	if len(page.Entries) > 0 && page.Entries[0].Command != nil && page.Entries[0].Command.State == nudgequeue.CommandStatePending {
		return nudgeReconcileContinue()
	}
	return nudgeReconcileSuccess()
}

// nudgeProviderCompletion is the closed durable projection of one classified
// worker nudge result. A nonterminal value is proven pre-entry evidence that
// the owner must park until an explicit recovery policy exists.
type nudgeProviderCompletion struct {
	terminal      bool
	actionResult  nudgequeue.CommandActionResult
	errorClass    nudgequeue.CommandErrorClass
	detail        string
	providerStage nudgequeue.ProviderStage
	completion    nudgequeue.CompletionState
}

// nudgeEffectTarget is one exact persisted session/runtime identity reread. It
// carries only the fields needed to reject stale command generations before a
// provider can be entered.
type nudgeEffectTarget struct {
	sessionID            string
	sessionName          string
	intentGeneration     uint64
	continuationIdentity string
	launchIdentity       string
	provider             string
	transport            string
	closed               bool
}

func selectNudgeEffectLaunch(command nudgequeue.Command, target nudgeEffectTarget) (string, error) {
	if target.closed || target.sessionID == "" || target.sessionName == "" ||
		target.launchIdentity == "" || target.sessionID != command.Target.SessionID ||
		target.intentGeneration != command.Target.IntentGeneration {
		return "", errNudgeEffectTargetChanged
	}

	switch command.Target.Policy {
	case nudgequeue.TargetPolicyContinuation:
		if target.continuationIdentity == "" || target.continuationIdentity != command.Target.ContinuationIdentity {
			return "", errNudgeEffectTargetChanged
		}
	case nudgequeue.TargetPolicyExactLaunch:
		if target.launchIdentity != command.Target.LaunchIdentity {
			return "", errNudgeEffectTargetChanged
		}
	default:
		return "", errNudgeEffectTargetChanged
	}
	if command.Binding != nil && command.Binding.LaunchIdentity != target.launchIdentity {
		return "", errNudgeEffectTargetChanged
	}
	return target.launchIdentity, nil
}

func mapNudgeProviderCompletion(result worker.NudgeResult, effectErr error) nudgeProviderCompletion {
	if result.Effect == nil {
		return invalidNudgeProviderCompletion()
	}
	effect := *result.Effect
	transportAccepted := effect.Stage == runtime.NudgeEffectStageAccepted &&
		effect.Completion == runtime.NudgeEffectCompletionCompleted
	if effect.Validate(effectErr) != nil || result.Delivered != transportAccepted {
		return invalidNudgeProviderCompletion()
	}

	switch effect.Stage {
	case runtime.NudgeEffectStageNotEntered:
		return nudgeProviderCompletion{
			providerStage: nudgequeue.ProviderStageNotEntered,
			completion:    nudgequeue.CompletionStateNotCompleted,
		}
	case runtime.NudgeEffectStageRejected:
		return nudgeProviderCompletion{
			terminal:      true,
			actionResult:  nudgequeue.CommandActionResultRejected,
			errorClass:    nudgequeue.CommandErrorClassProviderRejected,
			detail:        nudgeProviderRejectedDetail,
			providerStage: nudgequeue.ProviderStageRejected,
			completion:    nudgequeue.CompletionStateNotCompleted,
		}
	case runtime.NudgeEffectStageMayHaveEntered:
		return nudgeProviderCompletion{
			terminal:      true,
			actionResult:  nudgequeue.CommandActionResultDeliveryUnknown,
			errorClass:    nudgequeue.CommandErrorClassProviderAmbiguous,
			detail:        nudgeProviderAmbiguousDetail,
			providerStage: nudgequeue.ProviderStageMayHaveEntered,
			completion:    nudgequeue.CompletionStateUnknown,
		}
	case runtime.NudgeEffectStageAccepted:
		actionResult := nudgequeue.CommandActionResultInjectedUnconfirmed
		if effect.ConsumptionConfirmed {
			actionResult = nudgequeue.CommandActionResultDelivered
		}
		return nudgeProviderCompletion{
			terminal:      true,
			actionResult:  actionResult,
			providerStage: nudgequeue.ProviderStageAccepted,
			completion:    nudgequeue.CompletionStateCompleted,
		}
	default:
		return invalidNudgeProviderCompletion()
	}
}

func invalidNudgeProviderCompletion() nudgeProviderCompletion {
	return nudgeProviderCompletion{
		terminal:      true,
		actionResult:  nudgequeue.CommandActionResultDeliveryUnknown,
		errorClass:    nudgequeue.CommandErrorClassProviderAmbiguous,
		detail:        nudgeProviderInvalidEvidenceDetail,
		providerStage: nudgequeue.ProviderStageMayHaveEntered,
		completion:    nudgequeue.CompletionStateUnknown,
	}
}
