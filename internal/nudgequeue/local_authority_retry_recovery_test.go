package nudgequeue

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"testing"

	"github.com/gastownhall/gascity/internal/beads"
)

func TestLocalNudgeAuthorityRetryRecoveryRejectsContradictoryEvidenceWithoutChangingAuthority(t *testing.T) {
	tests := []struct {
		name   string
		tamper func(*testing.T, *localAuthorityProviderAttemptFixture, CommandRepositoryState)
	}{
		{
			name: "after digest mismatch",
			tamper: func(t *testing.T, fixture *localAuthorityProviderAttemptFixture, _ CommandRepositoryState) {
				t.Helper()
				if _, err := fixture.authority.db.ExecContext(t.Context(), `UPDATE retry_preparations SET after_digest = zeroblob(32) WHERE command_id = ?`, fixture.command.ID); err != nil {
					t.Fatalf("tamper retry after digest: %v", err)
				}
			},
		},
		{
			name: "partition mismatch",
			tamper: func(t *testing.T, fixture *localAuthorityProviderAttemptFixture, _ CommandRepositoryState) {
				t.Helper()
				partition := fixture.partition.identity
				partition[0] ^= 0xff
				if _, err := fixture.authority.db.ExecContext(t.Context(), `UPDATE retry_preparations SET partition_id = ? WHERE command_id = ?`, partition[:], fixture.command.ID); err != nil {
					t.Fatalf("tamper retry partition: %v", err)
				}
			},
		},
		{
			name: "attempt mismatch",
			tamper: func(t *testing.T, fixture *localAuthorityProviderAttemptFixture, _ CommandRepositoryState) {
				t.Helper()
				if _, err := fixture.authority.db.ExecContext(t.Context(), `UPDATE retry_preparations SET attempt_id = 'attempt-substituted' WHERE command_id = ?`, fixture.command.ID); err != nil {
					t.Fatalf("tamper retry attempt: %v", err)
				}
			},
		},
		{
			name: "effect fence ahead of repository",
			tamper: func(t *testing.T, fixture *localAuthorityProviderAttemptFixture, state CommandRepositoryState) {
				t.Helper()
				if _, err := fixture.authority.db.ExecContext(t.Context(), `UPDATE authority_meta SET highest_observed_revision = ? WHERE singleton = 1`, encodeLocalAuthorityUint64(state.Revision+1)); err != nil {
					t.Fatalf("advance authority effect fence: %v", err)
				}
			},
		},
		{
			name: "preparation and receipt duality",
			tamper: func(t *testing.T, fixture *localAuthorityProviderAttemptFixture, state CommandRepositoryState) {
				t.Helper()
				intent, found, err := localAuthorityRetryPreparationByCommand(t.Context(), fixture.authority.db, fixture.authority.store, fixture.command.ID)
				if err != nil || !found {
					t.Fatalf("read retry preparation before duality tamper = %#v, found=%t err=%v", intent, found, err)
				}
				commit := CommandRetryTransitionCommit{
					Store: intent.Store, RepositoryRevision: intent.RepositoryRevision, CommandID: intent.CommandID,
					Sequence: intent.Sequence, Partition: intent.Partition, AfterCommandDigest: intent.AfterCommandDigest,
					AttemptID: intent.Claim.AttemptID, ObservedAt: intent.ObservedAt,
					ProviderStage: intent.ProviderStage, Completion: intent.Completion,
					EffectRepositoryRevision: state.Revision, EffectSequenceHighWater: state.SequenceHighWater,
				}
				receipt, err := commandRetryTransitionReceiptFor(intent, commit)
				if err != nil {
					t.Fatalf("commandRetryTransitionReceiptFor duality tamper: %v", err)
				}
				tx, err := fixture.authority.db.BeginTx(t.Context(), nil)
				if err != nil {
					t.Fatalf("begin duality tamper: %v", err)
				}
				defer func() { _ = tx.Rollback() }()
				if err := insertLocalAuthorityRetryReceipt(t.Context(), tx, intent, receipt); err != nil {
					t.Fatalf("insert dual retry receipt: %v", err)
				}
				if err := advanceLocalAuthorityRetryTransitionGeneration(t.Context(), tx, 0, 1); err != nil {
					t.Fatalf("advance retry metadata for duality: %v", err)
				}
				if err := tx.Commit(); err != nil {
					t.Fatalf("commit duality tamper: %v", err)
				}
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := interruptedLocalRetryAfterStoreCommit(t)
			state, err := fixture.repository.State(t.Context())
			if err != nil {
				t.Fatalf("State: %v", err)
			}
			test.tamper(t, fixture, state)
			before := snapshotLocalRetryRecoveryEvidence(t, fixture.authority.db)

			if err := fixture.authority.RecoverCommandAuthority(t.Context(), fixture.repository); !errors.Is(err, ErrLocalNudgeAuthorityConflict) {
				t.Fatalf("RecoverCommandAuthority with contradictory retry evidence error = %v, want conflict", err)
			}
			after := snapshotLocalRetryRecoveryEvidence(t, fixture.authority.db)
			if !reflect.DeepEqual(after, before) {
				t.Fatalf("contradictory retry recovery changed authority evidence\nbefore: %q\n after: %q", before, after)
			}
		})
	}
}

func TestLocalNudgeAuthorityRetryRecoveryDefersLiveWriterAndGenerationMovement(t *testing.T) {
	t.Run("live writer", func(t *testing.T) {
		fixture := newLocalAuthorityProviderAttemptFixture(t)
		state, err := fixture.repository.State(t.Context())
		if err != nil {
			t.Fatalf("State: %v", err)
		}
		intent := localAuthorityRetryIntent(t, state, fixture.command, fixture.partition)
		if err := fixture.authority.PrepareCommandRetryTransition(t.Context(), intent); err != nil {
			t.Fatalf("PrepareCommandRetryTransition: %v", err)
		}

		if _, stable, err := fixture.authority.repairCommandRetryTransitions(t.Context(), fixture.repository, state); err != nil || stable {
			t.Fatalf("repairCommandRetryTransitions with live writer = stable:%t err:%v, want deferred", stable, err)
		}
		assertLocalAuthorityRetryEvidence(t, fixture.authority, 1, 0, 1)
		fixture.authority.retryOwnershipMu.Lock()
		owner, owned := fixture.authority.retryOwners[intent.CommandID]
		fixture.authority.retryOwnershipMu.Unlock()
		if !owned || owner.writers != 1 || !sameCommandRetryTransitionIntent(owner.intent, intent) {
			t.Fatalf("retry owner after deferred recovery = %#v, owned=%t", owner, owned)
		}
		if err := fixture.authority.ReleaseCommandRetryTransitionWriter(t.Context(), intent); err != nil {
			t.Fatalf("ReleaseCommandRetryTransitionWriter: %v", err)
		}
		if _, stable, err := fixture.authority.repairCommandRetryTransitions(t.Context(), fixture.repository, state); err != nil || !stable {
			t.Fatalf("repairCommandRetryTransitions after writer release = stable:%t err:%v", stable, err)
		}
		assertLocalAuthorityRetryEvidence(t, fixture.authority, 0, 0, 1)
	})

	t.Run("generation changes across repository read", func(t *testing.T) {
		fixture := newLocalAuthorityProviderAttemptFixture(t)
		state, err := fixture.repository.State(t.Context())
		if err != nil {
			t.Fatalf("State: %v", err)
		}
		intent := localAuthorityRetryIntent(t, state, fixture.command, fixture.partition)
		if err := fixture.authority.PrepareCommandRetryTransition(t.Context(), intent); err != nil {
			t.Fatalf("PrepareCommandRetryTransition: %v", err)
		}
		if err := fixture.authority.ReleaseCommandRetryTransitionWriter(t.Context(), intent); err != nil {
			t.Fatalf("ReleaseCommandRetryTransitionWriter: %v", err)
		}
		hookedStore := &retryAuditReadHookStore{repositoryAtomicTestStore: fixture.store}
		hookedRepository := *fixture.repository
		hookedRepository.reader.store = hookedStore
		hookedRepository.reader.snapshots = hookedStore
		hookedRepository.preparer = hookedStore
		hookedStore.setAfterGet(func() {
			if err := fixture.authority.AbortCommandRetryTransition(t.Context(), intent); err != nil {
				t.Fatalf("concurrent AbortCommandRetryTransition: %v", err)
			}
		})

		if _, stable, err := fixture.authority.repairCommandRetryTransitions(t.Context(), &hookedRepository, state); err != nil || stable {
			t.Fatalf("repairCommandRetryTransitions across generation movement = stable:%t err:%v, want retry", stable, err)
		}
		assertLocalAuthorityRetryEvidence(t, fixture.authority, 0, 0, 1)
		if _, stable, err := fixture.authority.repairCommandRetryTransitions(t.Context(), &hookedRepository, state); err != nil || !stable {
			t.Fatalf("repairCommandRetryTransitions after generation movement = stable:%t err:%v", stable, err)
		}
	})

	t.Run("preparation is substituted across repository read", func(t *testing.T) {
		fixture := interruptedLocalRetryAfterStoreCommit(t)
		state, err := fixture.repository.State(t.Context())
		if err != nil {
			t.Fatalf("State: %v", err)
		}
		hookedStore := &retryAuditReadHookStore{repositoryAtomicTestStore: fixture.store}
		hookedRepository := *fixture.repository
		hookedRepository.reader.store = hookedStore
		hookedRepository.reader.snapshots = hookedStore
		hookedRepository.preparer = hookedStore
		hookedStore.setAfterGet(func() {
			if _, err := fixture.authority.db.ExecContext(t.Context(), `UPDATE retry_preparations
				SET retry_error_detail = 'substituted after repository read' WHERE command_id = ?`, fixture.command.ID); err != nil {
				t.Fatalf("substitute retry preparation across read: %v", err)
			}
		})

		if _, stable, err := fixture.authority.repairCommandRetryTransitions(t.Context(), &hookedRepository, state); !errors.Is(err, ErrLocalNudgeAuthorityConflict) || stable {
			t.Fatalf("repairCommandRetryTransitions across preparation substitution = stable:%t err:%v, want conflict", stable, err)
		}
		assertLocalAuthorityRetryEvidence(t, fixture.authority, 1, 0, 1)
		var detail string
		if err := fixture.authority.db.QueryRowContext(t.Context(), `SELECT retry_error_detail FROM retry_preparations WHERE command_id = ?`, fixture.command.ID).Scan(&detail); err != nil {
			t.Fatalf("read substituted retry preparation: %v", err)
		}
		if detail != "substituted after repository read" {
			t.Fatalf("retry preparation detail after rejected recovery = %q", detail)
		}
	})
}

func TestLocalNudgeAuthorityRetryAuditCursorResumesReceiptPageAfterRestart(t *testing.T) {
	fixture := newLocalAuthorityProviderAttemptFixture(t)
	beforeState, err := fixture.repository.State(t.Context())
	if err != nil {
		t.Fatalf("State before retry: %v", err)
	}
	baseIntent := localAuthorityRetryIntent(t, beforeState, fixture.command, fixture.partition)
	request := localAuthorityRetryRequest(fixture.command)
	if _, err := fixture.repository.RetryProviderAttempt(t.Context(), request, fixture.partition, fixture.authority); err != nil {
		t.Fatalf("RetryProviderAttempt: %v", err)
	}
	state, err := fixture.repository.State(t.Context())
	if err != nil {
		t.Fatalf("State after retry: %v", err)
	}
	insertLocalRetryReceiptPageForTest(t, fixture.authority, baseIntent, state, localAuthorityRecoveryPageSize)
	if err := fixture.authority.Close(); err != nil {
		t.Fatalf("Close before paged audit: %v", err)
	}
	reopened, err := OpenLocalNudgeAuthority(t.Context(), fixture.cityPath, state, localAuthorityOptions())
	if err != nil {
		t.Fatalf("OpenLocalNudgeAuthority before paged audit: %v", err)
	}

	if _, stable, err := reopened.repairCommandRetryTransitions(t.Context(), fixture.repository, state); !errors.Is(err, ErrCommandAuthorityRecoveryYield) || stable {
		t.Fatalf("first retry audit page = stable:%t err:%v, want durable yield", stable, err)
	}
	cursor, err := reopened.readLocalAuthorityRetryAuditCursor(t.Context(), reopened.db)
	if err != nil {
		t.Fatalf("read retry audit cursor after first page: %v", err)
	}
	if cursor.phase != localAuthorityRetryAuditReceipts || cursor.receiptCount != localAuthorityRecoveryPageSize || cursor.afterCommandID == "" || cursor.afterAttemptID == "" {
		t.Fatalf("retry audit cursor after first page = %#v", cursor)
	}
	if err := reopened.Close(); err != nil {
		t.Fatalf("Close after first retry audit page: %v", err)
	}
	reopened, err = OpenLocalNudgeAuthority(t.Context(), fixture.cityPath, state, localAuthorityOptions())
	if err != nil {
		t.Fatalf("OpenLocalNudgeAuthority after paged audit restart: %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })

	token, stable, err := reopened.repairCommandRetryTransitions(t.Context(), fixture.repository, state)
	if err != nil || !stable {
		t.Fatalf("resumed retry audit = stable:%t err:%v", stable, err)
	}
	if token.receipts != localAuthorityRecoveryPageSize+1 || token.preparations != 0 {
		t.Fatalf("completed retry audit token = %#v", token)
	}
}

func interruptedLocalRetryAfterStoreCommit(t *testing.T) *localAuthorityProviderAttemptFixture {
	t.Helper()
	fixture := newLocalAuthorityProviderAttemptFixture(t)
	wantErr := errors.New("injected retry finalization interruption")
	interrupted := &failOnceLocalRetryFinalizeAuthority{LocalNudgeAuthority: fixture.authority, err: wantErr}
	if _, err := fixture.repository.RetryProviderAttempt(t.Context(), localAuthorityRetryRequest(fixture.command), fixture.partition, interrupted); !errors.Is(err, wantErr) {
		t.Fatalf("RetryProviderAttempt interruption error = %v, want %v", err, wantErr)
	}
	return fixture
}

func snapshotLocalRetryRecoveryEvidence(t *testing.T, db *sql.DB) []string {
	t.Helper()
	queries := []string{
		`SELECT hex(transition_generation), hex(preparation_count), hex(receipt_count) FROM retry_meta WHERE singleton = 1`,
		`SELECT hex(highest_observed_sequence), hex(highest_observed_revision), hex(claim_transition_generation), hex(claim_preparation_count), hex(claim_receipt_count) FROM authority_meta WHERE singleton = 1`,
		`SELECT * FROM retry_preparations ORDER BY command_id, attempt_id`,
		`SELECT * FROM retry_receipts ORDER BY command_id, attempt_id`,
		`SELECT * FROM claim_receipts ORDER BY command_id, attempt_id`,
	}
	result := make([]string, 0)
	for _, query := range queries {
		rows, err := db.QueryContext(t.Context(), query)
		if err != nil {
			t.Fatalf("snapshot retry recovery evidence %q: %v", query, err)
		}
		columns, err := rows.Columns()
		if err != nil {
			_ = rows.Close()
			t.Fatalf("read snapshot columns: %v", err)
		}
		for rows.Next() {
			values := make([]any, len(columns))
			destinations := make([]any, len(columns))
			for i := range values {
				destinations[i] = &values[i]
			}
			if err := rows.Scan(destinations...); err != nil {
				_ = rows.Close()
				t.Fatalf("scan retry recovery snapshot: %v", err)
			}
			var row bytes.Buffer
			for i, value := range values {
				_, _ = fmt.Fprintf(&row, "%s=", columns[i])
				switch typed := value.(type) {
				case []byte:
					_, _ = fmt.Fprintf(&row, "bytes:%x;", typed)
				default:
					_, _ = fmt.Fprintf(&row, "%T:%v;", value, value)
				}
			}
			result = append(result, row.String())
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			t.Fatalf("iterate retry recovery snapshot: %v", err)
		}
		if err := rows.Close(); err != nil {
			t.Fatalf("close retry recovery snapshot: %v", err)
		}
	}
	return result
}

func insertLocalRetryReceiptPageForTest(t *testing.T, authority *LocalNudgeAuthority, base CommandRetryTransitionIntent, state CommandRepositoryState, count int) {
	t.Helper()
	tx, err := authority.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatalf("begin retry receipt page fixture: %v", err)
	}
	defer func() { _ = tx.Rollback() }()
	for i := 0; i < count; i++ {
		intent := base
		intent.Claim.ID = fmt.Sprintf("claim-page-%04d", i)
		intent.Claim.AttemptID = fmt.Sprintf("attempt-page-%04d", i)
		intent.Retry.ClaimID = intent.Claim.ID
		intent.Retry.AttemptID = intent.Claim.AttemptID
		intent.Retry.AttemptCount = uint32(i + 2)
		commit := CommandRetryTransitionCommit{
			Store: intent.Store, RepositoryRevision: intent.RepositoryRevision, CommandID: intent.CommandID,
			Sequence: intent.Sequence, Partition: intent.Partition, AfterCommandDigest: intent.AfterCommandDigest,
			AttemptID: intent.Claim.AttemptID, ObservedAt: intent.ObservedAt,
			ProviderStage: intent.ProviderStage, Completion: intent.Completion,
			EffectRepositoryRevision: state.Revision, EffectSequenceHighWater: state.SequenceHighWater,
		}
		receipt, err := commandRetryTransitionReceiptFor(intent, commit)
		if err != nil {
			t.Fatalf("commandRetryTransitionReceiptFor page row %d: %v", i, err)
		}
		if err := insertLocalAuthorityRetryReceipt(t.Context(), tx, intent, receipt); err != nil {
			t.Fatalf("insert retry receipt page row %d: %v", i, err)
		}
		if err := advanceLocalAuthorityRetryTransitionGeneration(t.Context(), tx, 0, 1); err != nil {
			t.Fatalf("advance retry receipt page row %d: %v", i, err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit retry receipt page fixture: %v", err)
	}
}

type retryAuditReadHookStore struct {
	*repositoryAtomicTestStore

	mu       sync.Mutex
	afterGet func()
}

func (s *retryAuditReadHookStore) setAfterGet(hook func()) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.afterGet = hook
}

func (s *retryAuditReadHookStore) takeAfterGet() func() {
	s.mu.Lock()
	defer s.mu.Unlock()
	hook := s.afterGet
	s.afterGet = nil
	return hook
}

func (s *retryAuditReadHookStore) AtomicReadSnapshot(ctx context.Context, fn func(beads.AtomicReadSnapshotTx) error) error {
	return s.repositoryAtomicTestStore.AtomicReadSnapshot(ctx, func(tx beads.AtomicReadSnapshotTx) error {
		return fn(retryAuditReadHookTx{AtomicReadSnapshotTx: tx, afterGet: s.takeAfterGet})
	})
}

func (s *retryAuditReadHookStore) AtomicReadWrite(ctx context.Context, commitMessage string, fn func(beads.AtomicReadWriteTx) error) error {
	return s.repositoryAtomicTestStore.AtomicReadWrite(ctx, commitMessage, func(tx beads.AtomicReadWriteTx) error {
		return fn(retryAuditReadWriteHookTx{AtomicReadWriteTx: tx, afterGet: s.takeAfterGet})
	})
}

type retryAuditReadHookTx struct {
	beads.AtomicReadSnapshotTx
	afterGet func() func()
}

func (tx retryAuditReadHookTx) GetIssue(id string) (beads.Bead, error) {
	row, err := tx.AtomicReadSnapshotTx.GetIssue(id)
	if err == nil {
		if hook := tx.afterGet(); hook != nil {
			hook()
		}
	}
	return row, err
}

type retryAuditReadWriteHookTx struct {
	beads.AtomicReadWriteTx
	afterGet func() func()
}

func (tx retryAuditReadWriteHookTx) GetIssue(id string) (beads.Bead, error) {
	row, err := tx.AtomicReadWriteTx.GetIssue(id)
	if err == nil {
		if hook := tx.afterGet(); hook != nil {
			hook()
		}
	}
	return row, err
}
