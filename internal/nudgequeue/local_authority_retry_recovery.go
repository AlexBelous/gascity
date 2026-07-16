package nudgequeue

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"time"
)

type localAuthorityRetryAuditPhase string

const (
	localAuthorityRetryAuditIdle         localAuthorityRetryAuditPhase = "idle"
	localAuthorityRetryAuditPreparations localAuthorityRetryAuditPhase = "preparations"
	localAuthorityRetryAuditReceipts     localAuthorityRetryAuditPhase = "receipts"
	localAuthorityRetryAuditDone         localAuthorityRetryAuditPhase = "done"
)

var (
	errLocalAuthorityRetryAuditMoved             = errors.New("local retry audit binding moved")
	errLocalAuthorityRetryAuditCheckpointInvalid = errors.New("local retry audit checkpoint is invalid")
)

type localAuthorityRetryRecoveryToken struct {
	generation               uint64
	receiptRevisionHighWater uint64
	identity                 [sha256.Size]byte
	preparations             uint64
	receipts                 uint64
}

type localAuthorityRetryAuditCursor struct {
	generation         uint64
	repositoryRevision uint64
	sequenceHighWater  uint64
	phase              localAuthorityRetryAuditPhase
	afterRevision      uint64
	afterCommandID     string
	afterAttemptID     string
	identity           [sha256.Size]byte
	preparationCount   uint64
	receiptCount       uint64
}

func (c localAuthorityRetryAuditCursor) token() localAuthorityRetryRecoveryToken {
	return localAuthorityRetryRecoveryToken{
		generation: c.generation, receiptRevisionHighWater: c.afterRevision, identity: c.identity,
		preparations: c.preparationCount, receipts: c.receiptCount,
	}
}

// repairCommandRetryTransitions resolves reconstructible retry crash residue
// before claim recovery and audits every immutable retry receipt. Pages and a
// rolling checkpoint are authority-owned and durable, so a bounded invocation
// resumes after restart instead of rescanning an unbounded prefix. Neither
// repair path uses lease age or wall time as authority.
func (a *LocalNudgeAuthority) repairCommandRetryTransitions(
	ctx context.Context,
	repository *CommandRepository,
	state CommandRepositoryState,
) (localAuthorityRetryRecoveryToken, bool, error) {
	if repository == nil || state.Store != a.store {
		return localAuthorityRetryRecoveryToken{}, false, fmt.Errorf("%w: retry recovery repository differs from authority", ErrLocalNudgeAuthorityConflict)
	}
	ctx, budget := withCommandAuthorityRecoveryBudget(ctx)
	cursor, err := a.ensureRetryAuditCursor(ctx, state)
	if err != nil {
		return localAuthorityRetryRecoveryToken{}, false, err
	}
	for {
		if cursor.phase == localAuthorityRetryAuditDone {
			return cursor.token(), true, nil
		}
		remaining := budget.remainingWork()
		if remaining == 0 {
			return localAuthorityRetryRecoveryToken{}, false, budget.takeWork("auditing local retry transitions")
		}
		limit := min(localAuthorityRecoveryPageSize, remaining)
		var next localAuthorityRetryAuditCursor
		var work int
		var changed, deferred bool
		switch cursor.phase {
		case localAuthorityRetryAuditPreparations:
			next, work, changed, deferred, err = a.auditRetryPreparationPage(ctx, repository, state, cursor, limit)
		case localAuthorityRetryAuditReceipts:
			next, work, err = a.auditRetryReceiptPage(ctx, cursor, limit)
		default:
			return localAuthorityRetryRecoveryToken{}, false, fmt.Errorf("%w: unsupported durable retry audit phase %q", ErrLocalNudgeAuthorityConflict, cursor.phase)
		}
		if budgetErr := budget.takeWorkUnits(work, "auditing local retry transitions"); budgetErr != nil {
			return localAuthorityRetryRecoveryToken{}, false, budgetErr
		}
		if err != nil {
			moved, movementErr := a.retryAuditBindingMoved(ctx, repository, cursor)
			if movementErr != nil {
				return localAuthorityRetryRecoveryToken{}, false, errors.Join(err, movementErr)
			}
			if moved {
				return localAuthorityRetryRecoveryToken{}, false, nil
			}
			if errors.Is(err, errLocalAuthorityRetryAuditMoved) {
				return localAuthorityRetryRecoveryToken{}, false, fmt.Errorf("%w: retry evidence moved without advancing its authority binding", ErrLocalNudgeAuthorityConflict)
			}
			return localAuthorityRetryRecoveryToken{}, false, err
		}
		if changed {
			cursor, err = a.ensureRetryAuditCursor(ctx, state)
			if err != nil {
				return localAuthorityRetryRecoveryToken{}, false, err
			}
			continue
		}
		if next != cursor {
			if err := a.persistRetryAuditCursor(ctx, cursor, next); err != nil {
				if errors.Is(err, errLocalAuthorityRetryAuditMoved) {
					return localAuthorityRetryRecoveryToken{}, false, nil
				}
				return localAuthorityRetryRecoveryToken{}, false, err
			}
			cursor = next
		}
		if deferred {
			return localAuthorityRetryRecoveryToken{}, false, nil
		}
	}
}

func (a *LocalNudgeAuthority) auditRetryPreparationPage(
	ctx context.Context,
	repository *CommandRepository,
	state CommandRepositoryState,
	cursor localAuthorityRetryAuditCursor,
	limit int,
) (localAuthorityRetryAuditCursor, int, bool, bool, error) {
	next := cursor
	keys, _, err := a.localAuthorityRetryPreparationPage(ctx, cursor.afterCommandID, cursor.afterAttemptID, limit)
	if err != nil {
		return cursor, 0, false, false, err
	}
	if len(keys) == 0 {
		next.phase = localAuthorityRetryAuditReceipts
		return next, 0, false, false, nil
	}
	key := keys[0]
	intent, generation, err := a.readRetryPreparationForRecovery(ctx, key.commandID, key.attemptID)
	if err != nil {
		return cursor, 0, false, false, err
	}
	if generation != cursor.generation {
		return cursor, 0, false, false, errLocalAuthorityRetryAuditMoved
	}
	owned, err := a.retryWriterOwnsIntent(ctx, intent)
	if err != nil {
		return cursor, 0, false, false, err
	}
	resolution, command, digest, err := readRetryRecoveryCommand(ctx, repository, state, intent.CommandID)
	if err != nil {
		return cursor, 1, false, false, err
	}
	if resolution.Revision != cursor.repositoryRevision || resolution.SequenceHighWater != cursor.sequenceHighWater {
		return cursor, 1, false, false, errLocalAuthorityRetryAuditMoved
	}
	if command.Store != intent.Store || command.ID != intent.CommandID || command.Order.Sequence != intent.Sequence ||
		trustedCityPartitionFromAuthority(command.TrustedIngress) != intent.Partition {
		return cursor, 1, false, false, fmt.Errorf("%w: prepared retry command %q has inconsistent repository identity", ErrLocalNudgeAuthorityConflict, intent.CommandID)
	}
	if owned {
		return cursor, 1, false, true, nil
	}
	switch {
	case digest == intent.AfterCommandDigest && command.State == CommandStatePending && command.Claim == nil &&
		command.Terminal == nil && command.Retry != nil && command.Order.Revision == intent.RepositoryRevision &&
		sameCommandRetry(*command.Retry, intent.Retry):
		changed, deferred, err := a.finalizeRecoveredCommandRetryTransition(ctx, intent, state, generation)
		return cursor, 1, changed, deferred, err
	case digest == intent.BeforeCommandDigest && command.State == CommandStateInFlight && command.Claim != nil &&
		command.Terminal == nil && command.Retry != nil && command.Order.Revision <= intent.RepositoryBeforeRevision &&
		commandClaimsEqual(*command.Claim, intent.Claim):
		changed, deferred, err := a.abortRecoveredCommandRetryTransition(ctx, intent, state, generation)
		return cursor, 1, changed, deferred, err
	default:
		return cursor, 1, false, false, fmt.Errorf("%w: prepared retry command %q is neither its exact before-state nor after-state", ErrLocalNudgeAuthorityConflict, intent.CommandID)
	}
}

func (a *LocalNudgeAuthority) auditRetryReceiptPage(ctx context.Context, cursor localAuthorityRetryAuditCursor, limit int) (localAuthorityRetryAuditCursor, int, error) {
	next := cursor
	keys, more, err := a.localAuthorityRetryReceiptPage(
		ctx,
		cursor.afterRevision,
		cursor.afterCommandID,
		cursor.afterAttemptID,
		cursor.repositoryRevision,
		limit,
	)
	if err != nil {
		return cursor, 0, err
	}
	if len(keys) == 0 {
		next.phase = localAuthorityRetryAuditDone
		next.afterRevision = cursor.repositoryRevision
		next.afterCommandID = ""
		next.afterAttemptID = ""
		return next, 0, nil
	}
	for index, key := range keys {
		receipt, generation, err := a.readRetryReceiptForRecovery(ctx, key.commandID, key.attemptID)
		if err != nil {
			return cursor, index, err
		}
		if generation != cursor.generation {
			return cursor, index, errLocalAuthorityRetryAuditMoved
		}
		if receipt.RepositoryRevision != key.revision {
			return cursor, index + 1, fmt.Errorf("%w: retry receipt %q/%q moved across its audit page", ErrLocalNudgeAuthorityConflict, key.commandID, key.attemptID)
		}
		if receipt.RepositoryRevision > cursor.repositoryRevision || receipt.EffectRepositoryRevision > cursor.repositoryRevision ||
			receipt.Sequence > cursor.sequenceHighWater || receipt.EffectSequenceHighWater > cursor.sequenceHighWater {
			return cursor, index + 1, fmt.Errorf("%w: retry receipt %q/%q exceeds audited repository authority", ErrLocalNudgeAuthorityConflict, key.commandID, key.attemptID)
		}
		next.identity = advanceLocalAuthorityRetryAuditIdentity(next.identity, "receipt", localAuthorityRetryRecordIdentity(receipt))
		next.receiptCount++
		next.afterRevision = key.revision
		next.afterCommandID = key.commandID
		next.afterAttemptID = key.attemptID
	}
	if !more {
		next.phase = localAuthorityRetryAuditDone
		next.afterRevision = cursor.repositoryRevision
		next.afterCommandID = ""
		next.afterAttemptID = ""
	}
	return next, len(keys), nil
}

type localAuthorityRetryPreparationKey struct {
	commandID string
	attemptID string
}

type localAuthorityRetryReceiptKey struct {
	revision  uint64
	commandID string
	attemptID string
}

const (
	localAuthorityRetryReceiptAuditSuffixQuery = `SELECT retry_revision, command_id, attempt_id FROM retry_receipts
		WHERE retry_revision > ? AND retry_revision <= ?
		ORDER BY retry_revision, command_id, attempt_id LIMIT ?`
	localAuthorityRetryReceiptAuditContinuationQuery = `SELECT retry_revision, command_id, attempt_id FROM retry_receipts
		WHERE (retry_revision, command_id, attempt_id) > (?, ?, ?) AND retry_revision <= ?
		ORDER BY retry_revision, command_id, attempt_id LIMIT ?`
)

func (a *LocalNudgeAuthority) localAuthorityRetryPreparationPage(ctx context.Context, afterCommandID, afterAttemptID string, limit int) ([]localAuthorityRetryPreparationKey, bool, error) {
	if limit <= 0 || limit > localAuthorityRecoveryPageSize {
		return nil, false, fmt.Errorf("%w: retry preparation page limit %d is outside 1..%d", ErrLocalNudgeAuthorityConflict, limit, localAuthorityRecoveryPageSize)
	}
	release, err := a.begin(ctx)
	if err != nil {
		return nil, false, err
	}
	defer release()
	rows, err := a.db.QueryContext(ctx, `SELECT command_id, attempt_id FROM retry_preparations
		WHERE command_id > ? OR (command_id = ? AND attempt_id > ?)
		ORDER BY command_id, attempt_id LIMIT ?`, afterCommandID, afterCommandID, afterAttemptID, limit+1)
	if err != nil {
		return nil, false, fmt.Errorf("reading retry preparation page: %w", err)
	}
	defer func() { _ = rows.Close() }()
	keys := make([]localAuthorityRetryPreparationKey, 0, limit+1)
	for rows.Next() {
		var key localAuthorityRetryPreparationKey
		if err := rows.Scan(&key.commandID, &key.attemptID); err != nil {
			return nil, false, err
		}
		keys = append(keys, key)
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	more := len(keys) > limit
	if more {
		keys = keys[:limit]
	}
	return keys, more, nil
}

func (a *LocalNudgeAuthority) localAuthorityRetryReceiptPage(
	ctx context.Context,
	afterRevision uint64,
	afterCommandID string,
	afterAttemptID string,
	throughRevision uint64,
	limit int,
) ([]localAuthorityRetryReceiptKey, bool, error) {
	if limit <= 0 || limit > localAuthorityRecoveryPageSize {
		return nil, false, fmt.Errorf("%w: retry receipt page limit %d is outside 1..%d", ErrLocalNudgeAuthorityConflict, limit, localAuthorityRecoveryPageSize)
	}
	if afterRevision > throughRevision {
		return nil, false, fmt.Errorf("%w: retry receipt cursor revision %d exceeds audit revision %d", ErrLocalNudgeAuthorityConflict, afterRevision, throughRevision)
	}
	if (afterCommandID == "") != (afterAttemptID == "") {
		return nil, false, fmt.Errorf("%w: partial retry receipt audit cursor", ErrLocalNudgeAuthorityConflict)
	}
	release, err := a.begin(ctx)
	if err != nil {
		return nil, false, err
	}
	defer release()
	var rows *sql.Rows
	if afterCommandID == "" {
		rows, err = a.db.QueryContext(ctx, localAuthorityRetryReceiptAuditSuffixQuery,
			encodeLocalAuthorityUint64(afterRevision), encodeLocalAuthorityUint64(throughRevision), limit+1)
	} else {
		rows, err = a.db.QueryContext(ctx, localAuthorityRetryReceiptAuditContinuationQuery,
			encodeLocalAuthorityUint64(afterRevision), afterCommandID, afterAttemptID,
			encodeLocalAuthorityUint64(throughRevision), limit+1)
	}
	if err != nil {
		return nil, false, fmt.Errorf("reading retry receipt page: %w", err)
	}
	defer func() { _ = rows.Close() }()
	keys := make([]localAuthorityRetryReceiptKey, 0, limit+1)
	for rows.Next() {
		var key localAuthorityRetryReceiptKey
		var revisionWire []byte
		if err := rows.Scan(&revisionWire, &key.commandID, &key.attemptID); err != nil {
			return nil, false, err
		}
		key.revision, err = decodeLocalAuthorityUint64(revisionWire)
		if err != nil {
			return nil, false, err
		}
		keys = append(keys, key)
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	more := len(keys) > limit
	if more {
		keys = keys[:limit]
	}
	return keys, more, nil
}

func (a *LocalNudgeAuthority) readRetryPreparationForRecovery(ctx context.Context, commandID, attemptID string) (CommandRetryTransitionIntent, uint64, error) {
	release, err := a.begin(ctx)
	if err != nil {
		return CommandRetryTransitionIntent{}, 0, err
	}
	defer release()
	tx, err := a.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return CommandRetryTransitionIntent{}, 0, fmt.Errorf("reading retry recovery preparation: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	generation, _, _, err := localAuthorityRetryMutationState(ctx, tx)
	if err != nil {
		return CommandRetryTransitionIntent{}, 0, err
	}
	intent, found, err := localAuthorityRetryPreparationByCommand(ctx, tx, a.store, commandID)
	if err != nil {
		return CommandRetryTransitionIntent{}, 0, err
	}
	if !found || intent.Claim.AttemptID != attemptID {
		return CommandRetryTransitionIntent{}, 0, errLocalAuthorityRetryAuditMoved
	}
	if err := tx.Commit(); err != nil {
		return CommandRetryTransitionIntent{}, 0, fmt.Errorf("reading retry recovery preparation: commit: %w", err)
	}
	return intent, generation, nil
}

func (a *LocalNudgeAuthority) readRetryReceiptForRecovery(ctx context.Context, commandID, attemptID string) (CommandRetryTransitionReceipt, uint64, error) {
	release, err := a.begin(ctx)
	if err != nil {
		return CommandRetryTransitionReceipt{}, 0, err
	}
	defer release()
	tx, err := a.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return CommandRetryTransitionReceipt{}, 0, fmt.Errorf("reading retry recovery receipt: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	generation, _, _, err := localAuthorityRetryMutationState(ctx, tx)
	if err != nil {
		return CommandRetryTransitionReceipt{}, 0, err
	}
	receipt, found, err := localAuthorityRetryReceiptByAttempt(ctx, tx, a.store, commandID, attemptID)
	if err != nil {
		return CommandRetryTransitionReceipt{}, 0, err
	}
	if !found {
		return CommandRetryTransitionReceipt{}, 0, errLocalAuthorityRetryAuditMoved
	}
	membership, found, err := localAuthorityMembershipByCommand(ctx, tx, commandID)
	if err != nil {
		return CommandRetryTransitionReceipt{}, 0, err
	}
	if !found || membership.sequence != receipt.Sequence || membership.partition != receipt.Partition {
		return CommandRetryTransitionReceipt{}, 0, fmt.Errorf("%w: retry receipt %q/%q differs from admission membership", ErrLocalNudgeAuthorityConflict, commandID, attemptID)
	}
	if err := tx.Commit(); err != nil {
		return CommandRetryTransitionReceipt{}, 0, fmt.Errorf("reading retry recovery receipt: commit: %w", err)
	}
	return receipt, generation, nil
}

func advanceLocalAuthorityRetryAuditIdentity(current [sha256.Size]byte, kind string, item [sha256.Size]byte) [sha256.Size]byte {
	var canonical bytes.Buffer
	_, _ = canonical.Write(current[:])
	writeLocalClaimRecoveryString(&canonical, kind)
	_, _ = canonical.Write(item[:])
	return sha256.Sum256(canonical.Bytes())
}

func localAuthorityRetryRecordIdentity(receipt CommandRetryTransitionReceipt) [sha256.Size]byte {
	var canonical bytes.Buffer
	writeLocalClaimRecoveryString(&canonical, receipt.Store.StoreUUID)
	writeLocalClaimRecoveryUint64(&canonical, receipt.Store.RestoreEpoch)
	writeLocalClaimRecoveryUint64(&canonical, receipt.RepositoryRevision)
	writeLocalClaimRecoveryString(&canonical, receipt.CommandID)
	writeLocalClaimRecoveryUint64(&canonical, receipt.Sequence)
	_, _ = canonical.Write(receipt.Partition.identity[:])
	_, _ = canonical.Write(receipt.AfterCommandDigest[:])
	writeLocalClaimRecoveryString(&canonical, receipt.Claim.ID)
	writeLocalClaimRecoveryString(&canonical, receipt.Claim.OwnerID)
	writeLocalClaimRecoveryString(&canonical, receipt.Claim.OperationID)
	writeLocalClaimRecoveryString(&canonical, receipt.Claim.AttemptID)
	writeLocalClaimRecoveryString(&canonical, receipt.Claim.BoundLaunchIdentity)
	writeLocalClaimRecoveryString(&canonical, receipt.Claim.AuthorizationDecisionID)
	writeLocalClaimRecoveryString(&canonical, receipt.Claim.AuthorizationPolicyVersion)
	writeLocalClaimRecoveryString(&canonical, receipt.Claim.ClaimedAt.Format(time.RFC3339Nano))
	writeLocalClaimRecoveryString(&canonical, receipt.Claim.LeaseUntil.Format(time.RFC3339Nano))
	writeLocalClaimRecoveryUint64(&canonical, uint64(receipt.Retry.AttemptCount))
	writeLocalClaimRecoveryString(&canonical, receipt.Retry.LastAttemptAt.Format(time.RFC3339Nano))
	if receipt.Retry.NextEligibleAt == nil {
		writeLocalClaimRecoveryString(&canonical, "")
	} else {
		writeLocalClaimRecoveryString(&canonical, receipt.Retry.NextEligibleAt.Format(time.RFC3339Nano))
	}
	writeLocalClaimRecoveryString(&canonical, string(receipt.Retry.ErrorClass))
	writeLocalClaimRecoveryString(&canonical, receipt.Retry.ErrorDetail)
	writeLocalClaimRecoveryString(&canonical, receipt.ObservedAt.Format(time.RFC3339Nano))
	writeLocalClaimRecoveryString(&canonical, string(receipt.ProviderStage))
	writeLocalClaimRecoveryString(&canonical, string(receipt.Completion))
	writeLocalClaimRecoveryUint64(&canonical, receipt.EffectRepositoryRevision)
	writeLocalClaimRecoveryUint64(&canonical, receipt.EffectSequenceHighWater)
	return sha256.Sum256(canonical.Bytes())
}

func (a *LocalNudgeAuthority) retryWriterOwnsIntent(ctx context.Context, intent CommandRetryTransitionIntent) (bool, error) {
	if err := validateCommandRetryTransitionIntent(intent); err != nil {
		return false, err
	}
	release, err := a.begin(ctx)
	if err != nil {
		return false, err
	}
	defer release()
	a.retryOwnershipMu.Lock()
	defer a.retryOwnershipMu.Unlock()
	owner, owned := a.retryOwners[intent.CommandID]
	if !owned {
		return false, nil
	}
	if owner.writers == 0 || !sameCommandRetryTransitionIntent(owner.intent, intent) {
		return false, fmt.Errorf("%w: retry writer ownership differs from recovery snapshot", ErrLocalNudgeAuthorityConflict)
	}
	return true, nil
}

func readRetryRecoveryCommand(ctx context.Context, repository *CommandRepository, state CommandRepositoryState, commandID string) (CommandIndexResolution, Command, [sha256.Size]byte, error) {
	resolution, err := repository.Get(ctx, commandID)
	if err != nil {
		return CommandIndexResolution{}, Command{}, [sha256.Size]byte{}, fmt.Errorf("auditing retry command %q: %w", commandID, err)
	}
	if resolution.Store != state.Store || resolution.Revision < state.Revision || resolution.SequenceHighWater < state.SequenceHighWater ||
		!resolution.Found || resolution.Entry.Command == nil {
		return CommandIndexResolution{}, Command{}, [sha256.Size]byte{}, fmt.Errorf("%w: retry command %q is unavailable, opaque, or from inconsistent repository authority", ErrLocalNudgeAuthorityConflict, commandID)
	}
	command := cloneCommandValue(*resolution.Entry.Command)
	wire, err := EncodeCommandV1(command)
	if err != nil {
		return CommandIndexResolution{}, Command{}, [sha256.Size]byte{}, fmt.Errorf("auditing retry command %q: %w", commandID, err)
	}
	return resolution, command, sha256.Sum256(wire), nil
}

func (a *LocalNudgeAuthority) finalizeRecoveredCommandRetryTransition(ctx context.Context, intent CommandRetryTransitionIntent, state CommandRepositoryState, expectedGeneration uint64) (bool, bool, error) {
	release, err := a.begin(ctx)
	if err != nil {
		return false, false, err
	}
	defer release()
	a.retryOwnershipMu.Lock()
	defer a.retryOwnershipMu.Unlock()
	if owner, owned := a.retryOwners[intent.CommandID]; owned {
		if owner.writers == 0 || !sameCommandRetryTransitionIntent(owner.intent, intent) {
			return false, false, fmt.Errorf("%w: retry writer ownership differs from recovery finalization", ErrLocalNudgeAuthorityConflict)
		}
		return false, true, nil
	}
	commit := CommandRetryTransitionCommit{
		Store: intent.Store, RepositoryRevision: intent.RepositoryRevision, CommandID: intent.CommandID,
		Sequence: intent.Sequence, Partition: intent.Partition, AfterCommandDigest: intent.AfterCommandDigest,
		AttemptID: intent.Claim.AttemptID, ObservedAt: intent.ObservedAt,
		ProviderStage: intent.ProviderStage, Completion: intent.Completion,
		EffectRepositoryRevision: state.Revision, EffectSequenceHighWater: state.SequenceHighWater,
	}
	disposition, err := a.finalizeCommandRetryTransitionLocked(ctx, commit, &expectedGeneration, &intent)
	if err != nil {
		return false, false, err
	}
	return disposition == CommandRetryReceiptFinalized, false, nil
}

func (a *LocalNudgeAuthority) abortRecoveredCommandRetryTransition(ctx context.Context, intent CommandRetryTransitionIntent, state CommandRepositoryState, expectedGeneration uint64) (bool, bool, error) {
	release, err := a.begin(ctx)
	if err != nil {
		return false, false, err
	}
	defer release()
	a.retryOwnershipMu.Lock()
	defer a.retryOwnershipMu.Unlock()
	if owner, owned := a.retryOwners[intent.CommandID]; owned {
		if owner.writers == 0 || !sameCommandRetryTransitionIntent(owner.intent, intent) {
			return false, false, fmt.Errorf("%w: retry writer ownership differs from recovery abort", ErrLocalNudgeAuthorityConflict)
		}
		return false, true, nil
	}
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return false, false, fmt.Errorf("aborting recovered local retry transition: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	generation, _, _, err := localAuthorityRetryMutationState(ctx, tx)
	if err != nil {
		return false, false, err
	}
	if generation != expectedGeneration {
		return false, false, errLocalAuthorityRetryAuditMoved
	}
	highestSequence, highestRevision, err := localAuthorityObservedRepositoryHighWaters(ctx, tx)
	if err != nil {
		return false, false, err
	}
	if state.Store != a.store || state.Revision < highestRevision || state.SequenceHighWater < highestSequence ||
		state.Revision < intent.RepositoryBeforeRevision || state.SequenceHighWater < intent.RepositorySequenceHighWater {
		return false, false, fmt.Errorf("%w: retry rollback repository state does not dominate authority evidence", ErrLocalNudgeAuthorityConflict)
	}
	membership, found, err := localAuthorityMembershipByCommand(ctx, tx, intent.CommandID)
	if err != nil {
		return false, false, err
	}
	if !found || membership.sequence != intent.Sequence || membership.partition != intent.Partition || membership.terminalRevision != nil {
		return false, false, fmt.Errorf("%w: retry rollback has no matching active admission", ErrLocalNudgeAuthorityConflict)
	}
	claimReceipt, found, err := localAuthorityClaimReceiptByCommand(ctx, tx, a.store, intent.CommandID)
	if err != nil {
		return false, false, err
	}
	if !found || !claimReceiptMatchesRetryIntent(claimReceipt, intent) {
		return false, false, fmt.Errorf("%w: retry rollback lacks the exact claim receipt", ErrLocalNudgeAuthorityConflict)
	}
	prepared, found, err := localAuthorityRetryPreparationByCommand(ctx, tx, a.store, intent.CommandID)
	if err != nil {
		return false, false, err
	}
	if !found {
		return false, false, errLocalAuthorityRetryAuditMoved
	}
	if !sameCommandRetryTransitionIntent(prepared, intent) {
		return false, false, fmt.Errorf("%w: retry rollback preparation was substituted", ErrLocalNudgeAuthorityConflict)
	}
	deleted, err := tx.ExecContext(ctx, `DELETE FROM retry_preparations WHERE command_id = ? AND attempt_id = ?`, intent.CommandID, intent.Claim.AttemptID)
	if err != nil {
		return false, false, fmt.Errorf("aborting recovered local retry transition: %w", err)
	}
	if affected, err := deleted.RowsAffected(); err != nil || affected != 1 {
		return false, false, fmt.Errorf("%w: recovered retry abort affected %d rows: %w", ErrLocalNudgeAuthorityConflict, affected, err)
	}
	if err := advanceLocalAuthorityRetryTransitionGeneration(ctx, tx, -1, 0); err != nil {
		return false, false, err
	}
	if err := tx.Commit(); err != nil {
		return false, false, fmt.Errorf("aborting recovered local retry transition: commit: %w", err)
	}
	return true, false, nil
}

func initialLocalAuthorityRetryAuditIdentity() [sha256.Size]byte {
	return sha256.Sum256([]byte("gascity.local-retry-audit.v1"))
}

func (a *LocalNudgeAuthority) ensureRetryAuditCursor(ctx context.Context, state CommandRepositoryState) (localAuthorityRetryAuditCursor, error) {
	if state.Store != a.store {
		return localAuthorityRetryAuditCursor{}, fmt.Errorf("%w: retry audit repository store differs from authority", ErrLocalNudgeAuthorityConflict)
	}
	release, err := a.begin(ctx)
	if err != nil {
		return localAuthorityRetryAuditCursor{}, err
	}
	defer release()
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return localAuthorityRetryAuditCursor{}, fmt.Errorf("binding local retry audit cursor: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	cursor, err := a.readLocalAuthorityRetryAuditCursor(ctx, tx)
	checkpointInvalid := errors.Is(err, errLocalAuthorityRetryAuditCheckpointInvalid)
	if err != nil && !checkpointInvalid {
		return localAuthorityRetryAuditCursor{}, err
	}
	generation, preparations, receipts, err := localAuthorityRetryMutationState(ctx, tx)
	if err != nil {
		return localAuthorityRetryAuditCursor{}, err
	}
	if !checkpointInvalid {
		if cursor.generation > generation {
			return localAuthorityRetryAuditCursor{}, fmt.Errorf("%w: retry audit generation %d exceeds mutation generation %d", ErrLocalNudgeAuthorityConflict, cursor.generation, generation)
		}
		if cursor.repositoryRevision > state.Revision || cursor.sequenceHighWater > state.SequenceHighWater {
			return localAuthorityRetryAuditCursor{}, fmt.Errorf("%w: retry audit repository binding rewound", ErrLocalNudgeAuthorityConflict)
		}
		if cursor.preparationCount > preparations || cursor.receiptCount > receipts {
			return localAuthorityRetryAuditCursor{}, fmt.Errorf("%w: retry audit counts exceed retained authority evidence", ErrLocalNudgeAuthorityConflict)
		}
		if cursor.generation == generation && cursor.phase != localAuthorityRetryAuditPreparations && cursor.preparationCount != preparations {
			return localAuthorityRetryAuditCursor{}, fmt.Errorf("%w: retry audit skipped preparation evidence without generation movement", ErrLocalNudgeAuthorityConflict)
		}
		if cursor.generation == generation && cursor.phase == localAuthorityRetryAuditDone && cursor.receiptCount != receipts {
			return localAuthorityRetryAuditCursor{}, fmt.Errorf("%w: completed retry audit differs from retained receipts without generation movement", ErrLocalNudgeAuthorityConflict)
		}
	}
	sameBinding := !checkpointInvalid && cursor.phase != localAuthorityRetryAuditIdle &&
		cursor.generation == generation && cursor.repositoryRevision == state.Revision &&
		cursor.sequenceHighWater == state.SequenceHighWater
	if !sameBinding {
		if checkpointInvalid || cursor.phase == localAuthorityRetryAuditIdle {
			cursor = localAuthorityRetryAuditCursor{identity: initialLocalAuthorityRetryAuditIdentity()}
		}
		// Retry receipts are immutable and every genuine new receipt owns a
		// repository revision strictly above the prior audited repository
		// high-water. Preserve that certified prefix across generation changes,
		// repository advances, and process restarts; only preparations and the
		// receipt suffix can require new work. Historical receipts cannot grant
		// another effect: claim recovery and VerifyCommandRetryClaim separately
		// re-read the latest receipt for every actionable pending retry.
		cursor.generation = generation
		cursor.repositoryRevision = state.Revision
		cursor.sequenceHighWater = state.SequenceHighWater
		cursor.phase = localAuthorityRetryAuditPreparations
		cursor.preparationCount = 0
		if err := a.updateLocalAuthorityRetryAuditCursor(ctx, tx, cursor); err != nil {
			return localAuthorityRetryAuditCursor{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return localAuthorityRetryAuditCursor{}, fmt.Errorf("binding local retry audit cursor: commit: %w", err)
	}
	return cursor, nil
}

func (a *LocalNudgeAuthority) persistRetryAuditCursor(ctx context.Context, expected, next localAuthorityRetryAuditCursor) error {
	release, err := a.begin(ctx)
	if err != nil {
		return err
	}
	defer release()
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("persisting local retry audit cursor: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	current, err := a.readLocalAuthorityRetryAuditCursor(ctx, tx)
	if errors.Is(err, errLocalAuthorityRetryAuditCheckpointInvalid) {
		generation, _, _, mutationErr := localAuthorityRetryMutationState(ctx, tx)
		if mutationErr != nil {
			return mutationErr
		}
		reset := localAuthorityRetryAuditCursor{
			generation: generation, repositoryRevision: expected.repositoryRevision, sequenceHighWater: expected.sequenceHighWater,
			phase: localAuthorityRetryAuditPreparations, identity: initialLocalAuthorityRetryAuditIdentity(),
		}
		if updateErr := a.updateLocalAuthorityRetryAuditCursor(ctx, tx, reset); updateErr != nil {
			return updateErr
		}
		if commitErr := tx.Commit(); commitErr != nil {
			return fmt.Errorf("resetting invalid local retry audit cursor: commit: %w", commitErr)
		}
		return errLocalAuthorityRetryAuditMoved
	}
	if err != nil {
		return err
	}
	generation, preparations, receipts, err := localAuthorityRetryMutationState(ctx, tx)
	if err != nil {
		return err
	}
	if current != expected || generation != expected.generation {
		return errLocalAuthorityRetryAuditMoved
	}
	if next.preparationCount > preparations || next.receiptCount > receipts {
		return fmt.Errorf("%w: retry audit progress exceeds retained authority evidence", ErrLocalNudgeAuthorityConflict)
	}
	if next.phase == localAuthorityRetryAuditDone && (next.preparationCount != preparations || next.receiptCount != receipts) {
		return fmt.Errorf("%w: audited retry evidence counts %d/%d differ from authority metadata %d/%d",
			ErrLocalNudgeAuthorityConflict, next.preparationCount, next.receiptCount, preparations, receipts)
	}
	if err := a.updateLocalAuthorityRetryAuditCursor(ctx, tx, next); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("persisting local retry audit cursor: commit: %w", err)
	}
	return nil
}

func (a *LocalNudgeAuthority) completedRetryAuditToken(ctx context.Context, state CommandRepositoryState) (localAuthorityRetryRecoveryToken, bool, error) {
	if state.Store != a.store {
		return localAuthorityRetryRecoveryToken{}, false, fmt.Errorf("%w: retry audit repository store differs from authority", ErrLocalNudgeAuthorityConflict)
	}
	release, err := a.begin(ctx)
	if err != nil {
		return localAuthorityRetryRecoveryToken{}, false, err
	}
	defer release()
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return localAuthorityRetryRecoveryToken{}, false, fmt.Errorf("reading completed local retry audit: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	cursor, err := a.readLocalAuthorityRetryAuditCursor(ctx, tx)
	if errors.Is(err, errLocalAuthorityRetryAuditCheckpointInvalid) {
		return localAuthorityRetryRecoveryToken{}, false, nil
	}
	if err != nil {
		return localAuthorityRetryRecoveryToken{}, false, err
	}
	generation, preparations, receipts, err := localAuthorityRetryMutationState(ctx, tx)
	if err != nil {
		return localAuthorityRetryRecoveryToken{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return localAuthorityRetryRecoveryToken{}, false, fmt.Errorf("reading completed local retry audit: commit: %w", err)
	}
	if cursor.phase != localAuthorityRetryAuditDone || cursor.repositoryRevision != state.Revision ||
		cursor.sequenceHighWater != state.SequenceHighWater || cursor.generation != generation {
		return localAuthorityRetryRecoveryToken{}, false, nil
	}
	if cursor.preparationCount != preparations || cursor.receiptCount != receipts {
		return localAuthorityRetryRecoveryToken{}, false, fmt.Errorf("%w: completed retry audit counts %d/%d differ from authority metadata %d/%d",
			ErrLocalNudgeAuthorityConflict, cursor.preparationCount, cursor.receiptCount, preparations, receipts)
	}
	return cursor.token(), true, nil
}

func (a *LocalNudgeAuthority) retryAuditBindingMoved(ctx context.Context, repository *CommandRepository, cursor localAuthorityRetryAuditCursor) (bool, error) {
	release, err := a.begin(ctx)
	if err != nil {
		return false, err
	}
	generation, _, _, err := localAuthorityRetryMutationState(ctx, a.db)
	release()
	if err != nil {
		return false, err
	}
	if generation != cursor.generation {
		return true, nil
	}
	state, err := repository.State(ctx)
	if err != nil {
		return false, nil
	}
	return state.Revision != cursor.repositoryRevision || state.SequenceHighWater != cursor.sequenceHighWater, nil
}

func (a *LocalNudgeAuthority) readLocalAuthorityRetryAuditCursor(ctx context.Context, queryer localAuthorityQueryer) (localAuthorityRetryAuditCursor, error) {
	var generationWire, revisionWire, sequenceWire, identityWire, preparationsWire, receiptsWire, checkpointDigest []byte
	var phase, afterRevisionWire, afterAttemptID string
	if err := queryer.QueryRowContext(ctx, `SELECT audit_generation, audit_repository_revision, audit_sequence_high_water,
		audit_phase, audit_after_command_id, audit_after_attempt_id, audit_identity,
		audit_preparation_count, audit_receipt_count, audit_checkpoint_digest
		FROM retry_meta WHERE singleton = 1`).Scan(
		&generationWire, &revisionWire, &sequenceWire, &phase, &afterRevisionWire, &afterAttemptID,
		&identityWire, &preparationsWire, &receiptsWire, &checkpointDigest,
	); err != nil {
		return localAuthorityRetryAuditCursor{}, fmt.Errorf("reading local retry audit cursor: %w", err)
	}
	cursor := localAuthorityRetryAuditCursor{phase: localAuthorityRetryAuditPhase(phase)}
	var err error
	if cursor.generation, err = decodeLocalAuthorityUint64(generationWire); err != nil {
		return localAuthorityRetryAuditCursor{}, err
	}
	if cursor.repositoryRevision, err = decodeLocalAuthorityUint64(revisionWire); err != nil {
		return localAuthorityRetryAuditCursor{}, err
	}
	if cursor.sequenceHighWater, err = decodeLocalAuthorityUint64(sequenceWire); err != nil {
		return localAuthorityRetryAuditCursor{}, err
	}
	if cursor.preparationCount, err = decodeLocalAuthorityUint64(preparationsWire); err != nil {
		return localAuthorityRetryAuditCursor{}, err
	}
	if cursor.receiptCount, err = decodeLocalAuthorityUint64(receiptsWire); err != nil {
		return localAuthorityRetryAuditCursor{}, err
	}
	if cursor.phase == localAuthorityRetryAuditIdle {
		if afterRevisionWire != "" || afterAttemptID != "" {
			return localAuthorityRetryAuditCursor{}, fmt.Errorf("%w: %w: nonempty idle local retry audit position", errLocalAuthorityRetryAuditCheckpointInvalid, ErrLocalNudgeAuthorityConflict)
		}
	} else if cursor.afterRevision, cursor.afterCommandID, cursor.afterAttemptID, err = decodeLocalAuthorityRetryAuditPosition(afterRevisionWire, afterAttemptID); err != nil {
		return localAuthorityRetryAuditCursor{}, fmt.Errorf("%w: %w", errLocalAuthorityRetryAuditCheckpointInvalid, err)
	}
	if len(identityWire) != sha256.Size {
		return localAuthorityRetryAuditCursor{}, fmt.Errorf("%w: %w: malformed local retry audit identity", errLocalAuthorityRetryAuditCheckpointInvalid, ErrLocalNudgeAuthorityConflict)
	}
	copy(cursor.identity[:], identityWire)
	if err := validateLocalAuthorityRetryAuditCursor(cursor); err != nil {
		return localAuthorityRetryAuditCursor{}, fmt.Errorf("%w: %w", errLocalAuthorityRetryAuditCheckpointInvalid, err)
	}
	if len(checkpointDigest) != sha256.Size {
		return localAuthorityRetryAuditCursor{}, fmt.Errorf("%w: %w: malformed local retry audit checkpoint digest", errLocalAuthorityRetryAuditCheckpointInvalid, ErrLocalNudgeAuthorityConflict)
	}
	wantDigest := localAuthorityRetryAuditCursorDigest(cursor, a.store, a.opts.AuthorityID)
	if !bytes.Equal(checkpointDigest, wantDigest[:]) {
		return localAuthorityRetryAuditCursor{}, fmt.Errorf("%w: %w: local retry audit checkpoint digest differs", errLocalAuthorityRetryAuditCheckpointInvalid, ErrLocalNudgeAuthorityConflict)
	}
	return cursor, nil
}

// readLocalAuthorityRetryAuditCursorV4 validates the checkpoint wire emitted
// by schema v4. V4 keyed receipt progress only by command and attempt; v5 adds
// the receipt revision to make the immutable prefix resumable. The migration
// validates this old evidence before transactionally replacing it with a
// conservative full-audit cursor in the v5 wire.
func (a *LocalNudgeAuthority) readLocalAuthorityRetryAuditCursorV4(ctx context.Context, queryer localAuthorityQueryer) (localAuthorityRetryAuditCursor, error) {
	var generationWire, revisionWire, sequenceWire, identityWire, preparationsWire, receiptsWire, checkpointDigest []byte
	var phase, afterCommandID, afterAttemptID string
	if err := queryer.QueryRowContext(ctx, `SELECT audit_generation, audit_repository_revision, audit_sequence_high_water,
		audit_phase, audit_after_command_id, audit_after_attempt_id, audit_identity,
		audit_preparation_count, audit_receipt_count, audit_checkpoint_digest
		FROM retry_meta WHERE singleton = 1`).Scan(
		&generationWire, &revisionWire, &sequenceWire, &phase, &afterCommandID, &afterAttemptID,
		&identityWire, &preparationsWire, &receiptsWire, &checkpointDigest,
	); err != nil {
		return localAuthorityRetryAuditCursor{}, fmt.Errorf("reading local v4 retry audit cursor: %w", err)
	}
	cursor := localAuthorityRetryAuditCursor{
		phase: localAuthorityRetryAuditPhase(phase), afterCommandID: afterCommandID, afterAttemptID: afterAttemptID,
	}
	var err error
	if cursor.generation, err = decodeLocalAuthorityUint64(generationWire); err != nil {
		return localAuthorityRetryAuditCursor{}, err
	}
	if cursor.repositoryRevision, err = decodeLocalAuthorityUint64(revisionWire); err != nil {
		return localAuthorityRetryAuditCursor{}, err
	}
	if cursor.sequenceHighWater, err = decodeLocalAuthorityUint64(sequenceWire); err != nil {
		return localAuthorityRetryAuditCursor{}, err
	}
	if cursor.preparationCount, err = decodeLocalAuthorityUint64(preparationsWire); err != nil {
		return localAuthorityRetryAuditCursor{}, err
	}
	if cursor.receiptCount, err = decodeLocalAuthorityUint64(receiptsWire); err != nil {
		return localAuthorityRetryAuditCursor{}, err
	}
	if len(identityWire) != sha256.Size {
		return localAuthorityRetryAuditCursor{}, fmt.Errorf("%w: malformed local v4 retry audit identity", ErrLocalNudgeAuthorityConflict)
	}
	copy(cursor.identity[:], identityWire)
	if err := validateLocalAuthorityRetryAuditCursorV4(cursor); err != nil {
		return localAuthorityRetryAuditCursor{}, err
	}
	if len(checkpointDigest) != sha256.Size {
		return localAuthorityRetryAuditCursor{}, fmt.Errorf("%w: malformed local v4 retry audit checkpoint digest", ErrLocalNudgeAuthorityConflict)
	}
	wantDigest := localAuthorityRetryAuditCursorDigestV4(cursor, a.store, a.opts.AuthorityID)
	if !bytes.Equal(checkpointDigest, wantDigest[:]) {
		return localAuthorityRetryAuditCursor{}, fmt.Errorf("%w: local v4 retry audit checkpoint digest differs", ErrLocalNudgeAuthorityConflict)
	}
	return cursor, nil
}

func (a *LocalNudgeAuthority) updateLocalAuthorityRetryAuditCursor(ctx context.Context, tx *sql.Tx, cursor localAuthorityRetryAuditCursor) error {
	if err := validateLocalAuthorityRetryAuditCursor(cursor); err != nil {
		return err
	}
	afterRevisionWire, afterAttemptID := encodeLocalAuthorityRetryAuditPosition(cursor)
	checkpointDigest := localAuthorityRetryAuditCursorDigest(cursor, a.store, a.opts.AuthorityID)
	result, err := tx.ExecContext(ctx, `UPDATE retry_meta SET
		audit_generation = ?, audit_repository_revision = ?, audit_sequence_high_water = ?, audit_phase = ?,
		audit_after_command_id = ?, audit_after_attempt_id = ?, audit_identity = ?,
		audit_preparation_count = ?, audit_receipt_count = ?, audit_checkpoint_digest = ?
		WHERE singleton = 1`, encodeLocalAuthorityUint64(cursor.generation), encodeLocalAuthorityUint64(cursor.repositoryRevision),
		encodeLocalAuthorityUint64(cursor.sequenceHighWater), string(cursor.phase), afterRevisionWire, afterAttemptID,
		cursor.identity[:], encodeLocalAuthorityUint64(cursor.preparationCount), encodeLocalAuthorityUint64(cursor.receiptCount), checkpointDigest[:])
	if err != nil {
		return fmt.Errorf("updating local retry audit cursor: %w", err)
	}
	if affected, err := result.RowsAffected(); err != nil || affected != 1 {
		return fmt.Errorf("%w: retry audit cursor update affected %d rows: %w", ErrLocalNudgeAuthorityConflict, affected, err)
	}
	return nil
}

func localAuthorityRetryAuditCursorDigest(cursor localAuthorityRetryAuditCursor, store CommandStoreBinding, authorityID string) [sha256.Size]byte {
	if cursor.phase == localAuthorityRetryAuditIdle {
		return localAuthorityRetryAuditCheckpointDigest(store, authorityID, cursor.identity)
	}
	var canonical bytes.Buffer
	writeLocalClaimRecoveryString(&canonical, "gascity.local-retry-audit-checkpoint.v3")
	writeLocalClaimRecoveryString(&canonical, store.StoreUUID)
	writeLocalClaimRecoveryUint64(&canonical, store.RestoreEpoch)
	writeLocalClaimRecoveryString(&canonical, authorityID)
	writeLocalClaimRecoveryUint64(&canonical, cursor.generation)
	writeLocalClaimRecoveryUint64(&canonical, cursor.repositoryRevision)
	writeLocalClaimRecoveryUint64(&canonical, cursor.sequenceHighWater)
	writeLocalClaimRecoveryString(&canonical, string(cursor.phase))
	writeLocalClaimRecoveryUint64(&canonical, cursor.afterRevision)
	writeLocalClaimRecoveryString(&canonical, cursor.afterCommandID)
	writeLocalClaimRecoveryString(&canonical, cursor.afterAttemptID)
	_, _ = canonical.Write(cursor.identity[:])
	writeLocalClaimRecoveryUint64(&canonical, cursor.preparationCount)
	writeLocalClaimRecoveryUint64(&canonical, cursor.receiptCount)
	return sha256.Sum256(canonical.Bytes())
}

func localAuthorityRetryAuditCursorDigestV4(cursor localAuthorityRetryAuditCursor, store CommandStoreBinding, authorityID string) [sha256.Size]byte {
	if cursor.phase == localAuthorityRetryAuditIdle {
		return localAuthorityRetryAuditCheckpointDigest(store, authorityID, cursor.identity)
	}
	var canonical bytes.Buffer
	writeLocalClaimRecoveryString(&canonical, "gascity.local-retry-audit-checkpoint.v2")
	writeLocalClaimRecoveryString(&canonical, store.StoreUUID)
	writeLocalClaimRecoveryUint64(&canonical, store.RestoreEpoch)
	writeLocalClaimRecoveryString(&canonical, authorityID)
	writeLocalClaimRecoveryUint64(&canonical, cursor.generation)
	writeLocalClaimRecoveryUint64(&canonical, cursor.repositoryRevision)
	writeLocalClaimRecoveryUint64(&canonical, cursor.sequenceHighWater)
	writeLocalClaimRecoveryString(&canonical, string(cursor.phase))
	writeLocalClaimRecoveryString(&canonical, cursor.afterCommandID)
	writeLocalClaimRecoveryString(&canonical, cursor.afterAttemptID)
	_, _ = canonical.Write(cursor.identity[:])
	writeLocalClaimRecoveryUint64(&canonical, cursor.preparationCount)
	writeLocalClaimRecoveryUint64(&canonical, cursor.receiptCount)
	return sha256.Sum256(canonical.Bytes())
}

func validateLocalAuthorityRetryAuditCursor(cursor localAuthorityRetryAuditCursor) error {
	if cursor.sequenceHighWater > cursor.repositoryRevision || cursor.afterRevision > cursor.repositoryRevision ||
		cursor.preparationCount > cursor.generation || cursor.receiptCount > cursor.generation {
		return fmt.Errorf("%w: inconsistent local retry audit checkpoint high-waters", ErrLocalNudgeAuthorityConflict)
	}
	if (cursor.afterCommandID == "") != (cursor.afterAttemptID == "") {
		return fmt.Errorf("%w: partial local retry audit composite cursor", ErrLocalNudgeAuthorityConflict)
	}
	if cursor.afterCommandID != "" {
		if err := validateCommandIdentity("retry audit command id", cursor.afterCommandID); err != nil {
			return fmt.Errorf("%w: %w", ErrLocalNudgeAuthorityConflict, err)
		}
		if err := validateCommandIdentity("retry audit attempt id", cursor.afterAttemptID); err != nil {
			return fmt.Errorf("%w: %w", ErrLocalNudgeAuthorityConflict, err)
		}
	}
	initialIdentity := initialLocalAuthorityRetryAuditIdentity()
	switch cursor.phase {
	case localAuthorityRetryAuditIdle:
		want := localAuthorityRetryAuditCursor{phase: localAuthorityRetryAuditIdle, identity: initialIdentity}
		if cursor != want {
			return fmt.Errorf("%w: noncanonical idle local retry audit checkpoint", ErrLocalNudgeAuthorityConflict)
		}
		return nil
	case localAuthorityRetryAuditPreparations:
		if cursor.preparationCount != 0 {
			return fmt.Errorf("%w: inconsistent preparation retry audit cursor", ErrLocalNudgeAuthorityConflict)
		}
	case localAuthorityRetryAuditReceipts:
		if cursor.preparationCount != 0 {
			return fmt.Errorf("%w: inconsistent receipt retry audit cursor", ErrLocalNudgeAuthorityConflict)
		}
	case localAuthorityRetryAuditDone:
		if cursor.afterRevision != cursor.repositoryRevision || cursor.afterCommandID != "" || cursor.preparationCount != 0 {
			return fmt.Errorf("%w: inconsistent completed retry audit cursor", ErrLocalNudgeAuthorityConflict)
		}
	default:
		return fmt.Errorf("%w: invalid local retry audit phase %q", ErrLocalNudgeAuthorityConflict, cursor.phase)
	}
	if cursor.identity == ([sha256.Size]byte{}) {
		return fmt.Errorf("%w: empty local retry audit rolling identity", ErrLocalNudgeAuthorityConflict)
	}
	return nil
}

func validateLocalAuthorityRetryAuditCursorV4(cursor localAuthorityRetryAuditCursor) error {
	if cursor.afterRevision != 0 || cursor.sequenceHighWater > cursor.repositoryRevision ||
		cursor.preparationCount > cursor.generation || cursor.receiptCount > cursor.generation {
		return fmt.Errorf("%w: inconsistent local v4 retry audit checkpoint high-waters", ErrLocalNudgeAuthorityConflict)
	}
	if (cursor.afterCommandID == "") != (cursor.afterAttemptID == "") {
		return fmt.Errorf("%w: partial local v4 retry audit composite cursor", ErrLocalNudgeAuthorityConflict)
	}
	if cursor.afterCommandID != "" {
		if err := validateCommandIdentity("v4 retry audit command id", cursor.afterCommandID); err != nil {
			return fmt.Errorf("%w: %w", ErrLocalNudgeAuthorityConflict, err)
		}
		if err := validateCommandIdentity("v4 retry audit attempt id", cursor.afterAttemptID); err != nil {
			return fmt.Errorf("%w: %w", ErrLocalNudgeAuthorityConflict, err)
		}
	}
	initialIdentity := initialLocalAuthorityRetryAuditIdentity()
	switch cursor.phase {
	case localAuthorityRetryAuditIdle:
		want := localAuthorityRetryAuditCursor{phase: localAuthorityRetryAuditIdle, identity: initialIdentity}
		if cursor != want {
			return fmt.Errorf("%w: noncanonical idle local v4 retry audit checkpoint", ErrLocalNudgeAuthorityConflict)
		}
		return nil
	case localAuthorityRetryAuditPreparations:
		if cursor.afterCommandID != "" || cursor.preparationCount != 0 || cursor.receiptCount != 0 || cursor.identity != initialIdentity {
			return fmt.Errorf("%w: inconsistent preparation local v4 retry audit cursor", ErrLocalNudgeAuthorityConflict)
		}
	case localAuthorityRetryAuditReceipts:
		if cursor.preparationCount != 0 || (cursor.afterCommandID == "") != (cursor.receiptCount == 0) {
			return fmt.Errorf("%w: inconsistent receipt local v4 retry audit cursor", ErrLocalNudgeAuthorityConflict)
		}
	case localAuthorityRetryAuditDone:
		if cursor.afterCommandID != "" || cursor.preparationCount != 0 {
			return fmt.Errorf("%w: inconsistent completed local v4 retry audit cursor", ErrLocalNudgeAuthorityConflict)
		}
	default:
		return fmt.Errorf("%w: invalid local v4 retry audit phase %q", ErrLocalNudgeAuthorityConflict, cursor.phase)
	}
	if cursor.identity == ([sha256.Size]byte{}) {
		return fmt.Errorf("%w: empty local v4 retry audit rolling identity", ErrLocalNudgeAuthorityConflict)
	}
	return nil
}

func encodeLocalAuthorityRetryAuditPosition(cursor localAuthorityRetryAuditCursor) (string, string) {
	if cursor.phase == localAuthorityRetryAuditIdle {
		return "", ""
	}
	return fmt.Sprintf("%016x:%s", cursor.afterRevision, cursor.afterCommandID), cursor.afterAttemptID
}

func decodeLocalAuthorityRetryAuditPosition(revisionAndCommand, attemptID string) (uint64, string, string, error) {
	if len(revisionAndCommand) < 17 || revisionAndCommand[16] != ':' {
		return 0, "", "", fmt.Errorf("%w: malformed local retry audit revision cursor", ErrLocalNudgeAuthorityConflict)
	}
	revision, err := strconv.ParseUint(revisionAndCommand[:16], 16, 64)
	if err != nil {
		return 0, "", "", fmt.Errorf("%w: malformed local retry audit revision cursor: %w", ErrLocalNudgeAuthorityConflict, err)
	}
	if fmt.Sprintf("%016x", revision) != revisionAndCommand[:16] {
		return 0, "", "", fmt.Errorf("%w: noncanonical local retry audit revision cursor", ErrLocalNudgeAuthorityConflict)
	}
	return revision, revisionAndCommand[17:], attemptID, nil
}

func (a *LocalNudgeAuthority) resetRetryAuditCursor(ctx context.Context, state CommandRepositoryState) error {
	if state.Store != a.store {
		return fmt.Errorf("%w: retry audit reset repository store differs from authority", ErrLocalNudgeAuthorityConflict)
	}
	release, err := a.begin(ctx)
	if err != nil {
		return err
	}
	defer release()
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("resetting local retry audit cursor: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	generation, _, _, err := localAuthorityRetryMutationState(ctx, tx)
	if err != nil {
		return err
	}
	reset := localAuthorityRetryAuditCursor{
		generation: generation, repositoryRevision: state.Revision, sequenceHighWater: state.SequenceHighWater,
		phase: localAuthorityRetryAuditPreparations, identity: initialLocalAuthorityRetryAuditIdentity(),
	}
	if err := a.updateLocalAuthorityRetryAuditCursor(ctx, tx, reset); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("resetting local retry audit cursor: commit: %w", err)
	}
	return nil
}

func localAuthorityRetryMutationState(ctx context.Context, queryer localAuthorityQueryer) (generation, preparations, receipts uint64, err error) {
	var generationWire, preparationsWire, receiptsWire []byte
	if err := queryer.QueryRowContext(ctx, `SELECT transition_generation, preparation_count, receipt_count
		FROM retry_meta WHERE singleton = 1`).Scan(&generationWire, &preparationsWire, &receiptsWire); err != nil {
		return 0, 0, 0, fmt.Errorf("reading local retry mutation state: %w", err)
	}
	if generation, err = decodeLocalAuthorityUint64(generationWire); err != nil {
		return 0, 0, 0, err
	}
	if preparations, err = decodeLocalAuthorityUint64(preparationsWire); err != nil {
		return 0, 0, 0, err
	}
	if receipts, err = decodeLocalAuthorityUint64(receiptsWire); err != nil {
		return 0, 0, 0, err
	}
	return generation, preparations, receipts, nil
}
