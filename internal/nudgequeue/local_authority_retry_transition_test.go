package nudgequeue

import (
	"context"
	"database/sql"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestLocalNudgeAuthorityMigratesExactV3JournalInPlace(t *testing.T) {
	cityPath := t.TempDir()
	state := localAuthorityRepositoryState()
	createExactLocalNudgeAuthorityV3(t, cityPath, state, localAuthorityOptions())
	path := LocalNudgeAuthorityPath(cityPath)

	authority, err := OpenLocalNudgeAuthority(t.Context(), cityPath, state, localAuthorityOptions())
	if err != nil {
		t.Fatalf("OpenLocalNudgeAuthority(v3): %v", err)
	}
	defer func() { _ = authority.Close() }()
	if authority.path != path || filepath.Base(authority.path) != "local-authority-v1.sqlite" {
		t.Fatalf("migrated authority path = %q, want unchanged %q", authority.path, path)
	}

	var schema, retrySingleton int
	if err := authority.db.QueryRowContext(t.Context(), `SELECT schema_version FROM authority_meta WHERE singleton = 1`).Scan(&schema); err != nil {
		t.Fatalf("read migrated schema: %v", err)
	}
	if err := authority.db.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM retry_meta WHERE singleton = 1`).Scan(&retrySingleton); err != nil {
		t.Fatalf("read migrated retry metadata: %v", err)
	}
	if schema != localNudgeAuthoritySchema || retrySingleton != 1 {
		t.Fatalf("migrated schema = %d, retry singleton = %d; want %d/1", schema, retrySingleton, localNudgeAuthoritySchema)
	}
	if empty, err := validateLocalAuthoritySchemaManifest(t.Context(), authority.db, localNudgeAuthorityV3SchemaStatements); empty || !errors.Is(err, ErrLocalNudgeAuthorityConflict) {
		t.Fatalf("N-1 schema validation after v4 migration = empty:%t err:%v, want fail-closed conflict", empty, err)
	}

	if err := authority.Close(); err != nil {
		t.Fatalf("Close migrated authority: %v", err)
	}
	reopened, err := OpenLocalNudgeAuthority(t.Context(), cityPath, state, localAuthorityOptions())
	if err != nil {
		t.Fatalf("OpenLocalNudgeAuthority(v4): %v", err)
	}
	defer func() { _ = reopened.Close() }()
	var preparations, receipts int
	if err := reopened.db.QueryRowContext(t.Context(), `SELECT
		(SELECT COUNT(*) FROM retry_preparations),
		(SELECT COUNT(*) FROM retry_receipts)`).Scan(&preparations, &receipts); err != nil {
		t.Fatalf("read reopened retry evidence: %v", err)
	}
	if preparations != 0 || receipts != 0 {
		t.Fatalf("retry evidence after idempotent reopen = %d/%d, want 0/0", preparations, receipts)
	}
}

func TestLocalNudgeAuthorityMigratesPersistedPreIndexV4InPlace(t *testing.T) {
	fixture := newLocalAuthorityProviderAttemptFixture(t)
	if _, err := fixture.repository.RetryProviderAttempt(
		t.Context(), localAuthorityRetryRequest(fixture.command), fixture.partition, fixture.authority,
	); err != nil {
		t.Fatalf("RetryProviderAttempt before v4 migration: %v", err)
	}
	state, err := fixture.repository.State(t.Context())
	if err != nil {
		t.Fatalf("State before v4 migration: %v", err)
	}
	before, found, err := localAuthorityRetryReceiptByAttempt(
		t.Context(), fixture.authority.db, fixture.authority.store, fixture.command.ID, fixture.command.Claim.AttemptID,
	)
	if err != nil || !found {
		t.Fatalf("retry receipt before v4 migration = %#v, found=%t err=%v", before, found, err)
	}
	generation, preparationCount, receiptCount, err := localAuthorityRetryMutationState(t.Context(), fixture.authority.db)
	if err != nil || preparationCount != 0 || receiptCount != 1 {
		t.Fatalf("retry mutation state before v4 migration = %d/%d/%d err=%v, want generation/0/1", generation, preparationCount, receiptCount, err)
	}
	v4Cursor := localAuthorityRetryAuditCursor{
		generation: generation, repositoryRevision: state.Revision, sequenceHighWater: state.SequenceHighWater,
		phase: localAuthorityRetryAuditDone, identity: advanceLocalAuthorityRetryAuditIdentity(
			initialLocalAuthorityRetryAuditIdentity(), "receipt", localAuthorityRetryRecordIdentity(before),
		),
		receiptCount: receiptCount,
	}
	v4Digest := localAuthorityRetryAuditCursorDigestV4(v4Cursor, fixture.authority.store, fixture.authority.opts.AuthorityID)
	if err := fixture.authority.Close(); err != nil {
		t.Fatalf("Close before v4 migration: %v", err)
	}

	db := openLocalAuthorityFixtureDB(t, fixture.cityPath)
	for _, index := range []string{"retry_receipts_audit", "retry_receipts_claim"} {
		if _, err := db.ExecContext(t.Context(), `DROP INDEX `+index); err != nil {
			_ = db.Close()
			t.Fatalf("remove post-v4 retry index %s: %v", index, err)
		}
	}
	if _, err := db.ExecContext(t.Context(), `UPDATE authority_meta SET schema_version = ? WHERE singleton = 1`, localNudgeAuthorityPreviousSchema); err != nil {
		_ = db.Close()
		t.Fatalf("restore persisted v4 schema version: %v", err)
	}
	if _, err := db.ExecContext(t.Context(), `UPDATE retry_meta SET
		audit_generation = ?, audit_repository_revision = ?, audit_sequence_high_water = ?, audit_phase = ?,
		audit_after_command_id = ?, audit_after_attempt_id = ?, audit_identity = ?,
		audit_preparation_count = ?, audit_receipt_count = ?, audit_checkpoint_digest = ? WHERE singleton = 1`,
		encodeLocalAuthorityUint64(v4Cursor.generation), encodeLocalAuthorityUint64(v4Cursor.repositoryRevision),
		encodeLocalAuthorityUint64(v4Cursor.sequenceHighWater), string(v4Cursor.phase), v4Cursor.afterCommandID,
		v4Cursor.afterAttemptID, v4Cursor.identity[:], encodeLocalAuthorityUint64(v4Cursor.preparationCount),
		encodeLocalAuthorityUint64(v4Cursor.receiptCount), v4Digest[:]); err != nil {
		_ = db.Close()
		t.Fatalf("write persisted v4 retry cursor: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close persisted v4 fixture: %v", err)
	}

	reopened, err := OpenLocalNudgeAuthority(t.Context(), fixture.cityPath, state, localAuthorityOptions())
	if err != nil {
		t.Fatalf("OpenLocalNudgeAuthority(persisted pre-index v4): %v", err)
	}
	defer func() { _ = reopened.Close() }()
	fixture.authority = reopened
	var schema, auditIndexes, claimIndexes int
	if err := reopened.db.QueryRowContext(t.Context(), `SELECT schema_version FROM authority_meta WHERE singleton = 1`).Scan(&schema); err != nil {
		t.Fatalf("read migrated v4 schema version: %v", err)
	}
	if err := reopened.db.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND name = 'retry_receipts_audit'`).Scan(&auditIndexes); err != nil {
		t.Fatalf("read migrated retry audit index: %v", err)
	}
	if err := reopened.db.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND name = 'retry_receipts_claim'`).Scan(&claimIndexes); err != nil {
		t.Fatalf("read migrated retry claim index: %v", err)
	}
	if schema != localNudgeAuthoritySchema || auditIndexes != 1 || claimIndexes != 1 {
		t.Fatalf("migrated v4 schema/indexes = %d/%d/%d, want %d/1/1", schema, auditIndexes, claimIndexes, localNudgeAuthoritySchema)
	}
	after, found, err := localAuthorityRetryReceiptByAttempt(
		t.Context(), reopened.db, reopened.store, fixture.command.ID, fixture.command.Claim.AttemptID,
	)
	if err != nil || !found || !reflect.DeepEqual(after, before) {
		t.Fatalf("retry receipt after v4 migration = %#v, found=%t err=%v; want %#v", after, found, err, before)
	}
	reset, err := reopened.readLocalAuthorityRetryAuditCursor(t.Context(), reopened.db)
	if err != nil {
		t.Fatalf("read reset v5 retry cursor: %v", err)
	}
	wantReset := localAuthorityRetryAuditCursor{
		generation: generation, repositoryRevision: state.Revision, sequenceHighWater: state.SequenceHighWater,
		phase: localAuthorityRetryAuditPreparations, identity: initialLocalAuthorityRetryAuditIdentity(),
	}
	if reset != wantReset {
		t.Fatalf("migrated v5 retry cursor = %#v, want conservative reset %#v", reset, wantReset)
	}
	if err := reopened.RecoverCommandAuthority(t.Context(), fixture.repository); err != nil {
		t.Fatalf("RecoverCommandAuthority after v4 migration: %v", err)
	}
	certified, err := reopened.readLocalAuthorityRetryAuditCursor(t.Context(), reopened.db)
	if err != nil {
		t.Fatalf("read certified v5 retry cursor: %v", err)
	}
	if certified.phase != localAuthorityRetryAuditDone || certified.afterRevision != state.Revision ||
		certified.receiptCount != receiptCount || certified.identity != v4Cursor.identity {
		t.Fatalf("certified v5 retry cursor = %#v, want full re-audit through revision %d with one preserved receipt", certified, state.Revision)
	}
}

func TestLocalNudgeAuthorityRefusesTamperedV4BeforeMigration(t *testing.T) {
	cityPath := t.TempDir()
	state := localAuthorityRepositoryState()
	authority, err := OpenLocalNudgeAuthority(t.Context(), cityPath, state, localAuthorityOptions())
	if err != nil {
		t.Fatalf("OpenLocalNudgeAuthority: %v", err)
	}
	if err := authority.Close(); err != nil {
		t.Fatalf("Close before v4 tamper: %v", err)
	}
	db := openLocalAuthorityFixtureDB(t, cityPath)
	for _, index := range []string{"retry_receipts_audit", "retry_receipts_claim"} {
		if _, err := db.ExecContext(t.Context(), `DROP INDEX `+index); err != nil {
			_ = db.Close()
			t.Fatalf("remove post-v4 retry index %s: %v", index, err)
		}
	}
	if _, err := db.ExecContext(t.Context(), `UPDATE authority_meta SET schema_version = ? WHERE singleton = 1`, localNudgeAuthorityPreviousSchema); err != nil {
		_ = db.Close()
		t.Fatalf("restore persisted v4 schema version: %v", err)
	}
	if _, err := db.ExecContext(t.Context(), `UPDATE retry_meta SET audit_checkpoint_digest = zeroblob(32) WHERE singleton = 1`); err != nil {
		_ = db.Close()
		t.Fatalf("tamper persisted v4 retry cursor: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close tampered v4 fixture: %v", err)
	}

	reopened, err := OpenLocalNudgeAuthority(t.Context(), cityPath, state, localAuthorityOptions())
	if reopened != nil || !errors.Is(err, ErrLocalNudgeAuthorityConflict) {
		if reopened != nil {
			_ = reopened.Close()
		}
		t.Fatalf("OpenLocalNudgeAuthority(tampered v4) = %#v, err=%v; want conflict", reopened, err)
	}
	db = openLocalAuthorityFixtureDB(t, cityPath)
	defer func() { _ = db.Close() }()
	var schema, v5Indexes int
	if err := db.QueryRowContext(t.Context(), `SELECT schema_version FROM authority_meta WHERE singleton = 1`).Scan(&schema); err != nil {
		t.Fatalf("read rejected v4 schema: %v", err)
	}
	if err := db.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND name IN ('retry_receipts_audit', 'retry_receipts_claim')`).Scan(&v5Indexes); err != nil {
		t.Fatalf("inspect rejected v4 migration: %v", err)
	}
	if schema != localNudgeAuthorityPreviousSchema || v5Indexes != 0 {
		t.Fatalf("rejected v4 schema/indexes = %d/%d, want unchanged %d/0", schema, v5Indexes, localNudgeAuthorityPreviousSchema)
	}
}

func TestLocalNudgeAuthorityRefusesTamperedV3BeforeMigration(t *testing.T) {
	cityPath := t.TempDir()
	state := localAuthorityRepositoryState()
	createExactLocalNudgeAuthorityV3(t, cityPath, state, localAuthorityOptions())
	db := openLocalAuthorityFixtureDB(t, cityPath)
	if _, err := db.ExecContext(t.Context(), `CREATE TABLE injected_retry_authority (value TEXT)`); err != nil {
		t.Fatalf("tamper v3 authority: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close tampered v3 authority: %v", err)
	}

	authority, err := OpenLocalNudgeAuthority(t.Context(), cityPath, state, localAuthorityOptions())
	if authority != nil || !errors.Is(err, ErrLocalNudgeAuthorityConflict) {
		if authority != nil {
			_ = authority.Close()
		}
		t.Fatalf("OpenLocalNudgeAuthority(tampered v3) = %#v, err=%v; want conflict", authority, err)
	}
	db = openLocalAuthorityFixtureDB(t, cityPath)
	defer func() { _ = db.Close() }()
	var schema int
	if err := db.QueryRowContext(t.Context(), `SELECT schema_version FROM authority_meta WHERE singleton = 1`).Scan(&schema); err != nil {
		t.Fatalf("read rejected v3 schema: %v", err)
	}
	if schema != localNudgeAuthorityV3Schema {
		t.Fatalf("rejected v3 schema = %d, want unchanged %d", schema, localNudgeAuthorityV3Schema)
	}
	var retryMeta int
	if err := db.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'retry_meta'`).Scan(&retryMeta); err != nil {
		t.Fatalf("inspect rejected v3 migration: %v", err)
	}
	if retryMeta != 0 {
		t.Fatal("tampered v3 authority was partially migrated")
	}
}

func TestLocalNudgeAuthorityRetryClaimFreshnessQueriesStayIndexedAtLargeHistory(t *testing.T) {
	fixture := newLocalAuthorityProviderAttemptFixture(t)
	if _, err := fixture.repository.RetryProviderAttempt(
		t.Context(), localAuthorityRetryRequest(fixture.command), fixture.partition, fixture.authority,
	); err != nil {
		t.Fatalf("RetryProviderAttempt seed: %v", err)
	}

	const history = 100_000
	if _, err := fixture.authority.db.ExecContext(t.Context(), `WITH RECURSIVE history(n) AS (
		SELECT 1 UNION ALL SELECT n + 1 FROM history WHERE n < ?
	) INSERT INTO retry_receipts (
		command_id, attempt_id, decision_kind, sequence, partition_id, repository_before_revision,
		retry_revision, sequence_high_water, before_digest, after_digest, claim_id, owner_id,
		operation_id, bound_launch_identity, authorization_decision_id, authorization_policy_version,
		claimed_at, lease_until, retry_attempt_count, retry_last_attempt_at, retry_next_eligible_at,
		retry_error_class, retry_error_detail, observed_at, provider_stage, completion,
		effect_revision, effect_sequence_high_water
	) SELECT
		seed.command_id, printf('attempt-history-%06d', history.n), seed.decision_kind, seed.sequence,
		seed.partition_id, seed.repository_before_revision, seed.retry_revision, seed.sequence_high_water,
		seed.before_digest, seed.after_digest, printf('claim-history-%06d', history.n), seed.owner_id,
		seed.operation_id, seed.bound_launch_identity, seed.authorization_decision_id,
		seed.authorization_policy_version, seed.claimed_at, seed.lease_until, seed.retry_attempt_count,
		seed.retry_last_attempt_at, seed.retry_next_eligible_at, seed.retry_error_class,
		seed.retry_error_detail, seed.observed_at, seed.provider_stage, seed.completion,
		seed.effect_revision, seed.effect_sequence_high_water
	FROM retry_receipts AS seed CROSS JOIN history
	WHERE seed.command_id = ? AND seed.attempt_id = ?`, history, fixture.command.ID, fixture.command.Claim.AttemptID); err != nil {
		t.Fatalf("seed %d same-command retry receipts: %v", history, err)
	}

	for _, test := range []struct {
		name      string
		query     string
		index     string
		used      string
		available string
	}{
		{
			name: "claim id", query: localAuthorityRetryClaimIDExistsQuery, index: "retry_receipts_claim",
			used: "claim-history-100000", available: "claim-history-new",
		},
		{
			name: "attempt id", query: localAuthorityRetryAttemptIDExistsQuery, index: "sqlite_autoindex_retry_receipts_1",
			used: "attempt-history-100000", available: "attempt-history-new",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			rows, err := fixture.authority.db.QueryContext(t.Context(), `EXPLAIN QUERY PLAN `+test.query, fixture.command.ID, test.available)
			if err != nil {
				t.Fatalf("EXPLAIN QUERY PLAN: %v", err)
			}
			var details []string
			for rows.Next() {
				var id, parent, unused int
				var detail string
				if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
					_ = rows.Close()
					t.Fatalf("scan query plan: %v", err)
				}
				details = append(details, detail)
			}
			rowsErr := rows.Err()
			closeErr := rows.Close()
			if rowsErr != nil || closeErr != nil {
				t.Fatalf("read query plan: %v", errors.Join(rowsErr, closeErr))
			}
			plan := strings.Join(details, "\n")
			if !strings.Contains(plan, "COVERING INDEX "+test.index) || strings.Contains(plan, "SCAN retry_receipts") || strings.Contains(plan, "TEMP B-TREE") {
				t.Fatalf("query plan = %q, want covering index %q without full scan or temp sort", plan, test.index)
			}

			var exists int
			if err := fixture.authority.db.QueryRowContext(t.Context(), test.query, fixture.command.ID, test.used).Scan(&exists); err != nil || exists != 1 {
				t.Fatalf("used %s lookup = %d, err=%v; want 1", test.name, exists, err)
			}
			if err := fixture.authority.db.QueryRowContext(t.Context(), test.query, fixture.command.ID, test.available).Scan(&exists); err != nil || exists != 0 {
				t.Fatalf("available %s lookup = %d, err=%v; want 0", test.name, exists, err)
			}
		})
	}
}

func TestLocalNudgeAuthorityRetryOpenValidationHasNoLifetimeCountQuery(t *testing.T) {
	source, err := os.ReadFile("local_authority_retry_transition.go")
	if err != nil {
		t.Fatalf("read retry authority source: %v", err)
	}
	text := string(source)
	start := strings.Index(text, "func (a *LocalNudgeAuthority) validateRetryMetadata(")
	end := strings.Index(text, "type localAuthorityRetryTransitionRecord struct")
	if start < 0 || end <= start {
		t.Fatal("retry metadata validation source boundary moved")
	}
	validation := strings.ToLower(text[start:end])
	if strings.Contains(validation, "count(") || strings.Contains(validation, "count (*)") {
		t.Fatal("retry metadata validation contains a lifetime row-count query; startup must use durable counters and bounded absence checks")
	}
}

func TestLocalNudgeAuthorityRetryFinalizationConsumesExactClaimAtomically(t *testing.T) {
	fixture := newLocalAuthorityProviderAttemptFixture(t)
	request := localAuthorityRetryRequest(fixture.command)

	result, err := fixture.repository.RetryProviderAttempt(t.Context(), request, fixture.partition, fixture.authority)
	if err != nil || result.Disposition != CommandRetryRecorded || !result.HasRetryTransitionWitness() {
		t.Fatalf("RetryProviderAttempt = %#v, err=%v", result, err)
	}
	assertLocalAuthorityRetryEvidence(t, fixture.authority, 0, 1, 0)

	repeated, err := fixture.repository.RetryProviderAttempt(t.Context(), request, fixture.partition, fixture.authority)
	if err != nil || repeated.Disposition != CommandRetryAlreadyRecorded || !repeated.HasRetryTransitionWitness() {
		t.Fatalf("repeated RetryProviderAttempt = %#v, err=%v", repeated, err)
	}
	assertLocalAuthorityRetryEvidence(t, fixture.authority, 0, 1, 0)

	receipt, found, err := localAuthorityRetryReceiptByAttempt(t.Context(), fixture.authority.db, fixture.authority.store, fixture.command.ID, fixture.command.Claim.AttemptID)
	if err != nil || !found {
		t.Fatalf("localAuthorityRetryReceiptByAttempt = %#v, found=%t err=%v", receipt, found, err)
	}
	if !commandClaimsEqual(receipt.Claim, *fixture.command.Claim) || receipt.AfterCommandDigest == ([32]byte{}) || receipt.ProviderStage != ProviderStageNotEntered {
		t.Fatalf("retained retry receipt = %#v, want exact claim and definite non-entry", receipt)
	}
}

func TestLocalNudgeAuthorityRetryPreparationMustAdvanceCertifiedReceiptRevision(t *testing.T) {
	fixture := newLocalAuthorityProviderAttemptFixture(t)
	state, err := fixture.repository.State(t.Context())
	if err != nil {
		t.Fatalf("State: %v", err)
	}
	intent := localAuthorityRetryIntent(t, state, fixture.command, fixture.partition)
	generation, _, _, err := localAuthorityRetryMutationState(t.Context(), fixture.authority.db)
	if err != nil {
		t.Fatalf("localAuthorityRetryMutationState: %v", err)
	}
	certified := localAuthorityRetryAuditCursor{
		generation: generation, repositoryRevision: intent.RepositoryRevision,
		sequenceHighWater: intent.RepositorySequenceHighWater, phase: localAuthorityRetryAuditDone,
		afterRevision: intent.RepositoryRevision, identity: initialLocalAuthorityRetryAuditIdentity(),
	}
	tx, err := fixture.authority.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatalf("begin certified cursor fixture: %v", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := fixture.authority.updateLocalAuthorityRetryAuditCursor(t.Context(), tx, certified); err != nil {
		t.Fatalf("update certified cursor fixture: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit certified cursor fixture: %v", err)
	}

	if err := fixture.authority.PrepareCommandRetryTransition(t.Context(), intent); !errors.Is(err, ErrLocalNudgeAuthorityConflict) {
		t.Fatalf("PrepareCommandRetryTransition at certified revision error = %v, want conflict", err)
	}
	assertLocalAuthorityRetryEvidence(t, fixture.authority, 0, 0, 1)
}

func TestLocalNudgeAuthorityRetryFinalizationRejectsSubstitutedClaimPartition(t *testing.T) {
	fixture := newLocalAuthorityProviderAttemptFixture(t)
	state, err := fixture.repository.State(t.Context())
	if err != nil {
		t.Fatalf("State: %v", err)
	}
	intent := localAuthorityRetryIntent(t, state, fixture.command, fixture.partition)
	if err := fixture.authority.PrepareCommandRetryTransition(t.Context(), intent); err != nil {
		t.Fatalf("PrepareCommandRetryTransition: %v", err)
	}
	foreignPartition := intent.Partition.identity
	foreignPartition[0] ^= 0xff
	if _, err := fixture.authority.db.ExecContext(t.Context(), `UPDATE claim_receipts SET partition_id = ? WHERE command_id = ?`,
		foreignPartition[:], intent.CommandID); err != nil {
		t.Fatalf("substitute claim receipt partition: %v", err)
	}
	commit := CommandRetryTransitionCommit{
		Store: intent.Store, RepositoryRevision: intent.RepositoryRevision, CommandID: intent.CommandID,
		Sequence: intent.Sequence, Partition: intent.Partition, AfterCommandDigest: intent.AfterCommandDigest,
		AttemptID: intent.Claim.AttemptID, ObservedAt: intent.ObservedAt,
		ProviderStage: intent.ProviderStage, Completion: intent.Completion,
		EffectRepositoryRevision: intent.RepositoryRevision, EffectSequenceHighWater: intent.RepositorySequenceHighWater,
	}
	if _, err := fixture.authority.FinalizeCommandRetryTransition(t.Context(), commit); !errors.Is(err, ErrLocalNudgeAuthorityConflict) {
		t.Fatalf("FinalizeCommandRetryTransition with substituted claim partition error = %v, want conflict", err)
	}
	assertLocalAuthorityRetryEvidence(t, fixture.authority, 1, 0, 1)
}

func TestLocalNudgeAuthorityRetryFinalizationRecoversLostResponseWithoutClaimReconstruction(t *testing.T) {
	fixture := newLocalAuthorityProviderAttemptFixture(t)
	wantErr := errors.New("injected authority finalization outage")
	authority := &failOnceLocalRetryFinalizeAuthority{LocalNudgeAuthority: fixture.authority, err: wantErr}
	request := localAuthorityRetryRequest(fixture.command)

	if _, err := fixture.repository.RetryProviderAttempt(t.Context(), request, fixture.partition, authority); !errors.Is(err, wantErr) {
		t.Fatalf("RetryProviderAttempt first error = %v, want %v", err, wantErr)
	}
	assertLocalAuthorityRetryEvidence(t, fixture.authority, 1, 0, 1)

	recovered, err := fixture.repository.RetryProviderAttempt(t.Context(), request, fixture.partition, authority)
	if err != nil || recovered.Disposition != CommandRetryAlreadyRecorded || !recovered.HasRetryTransitionWitness() {
		t.Fatalf("RetryProviderAttempt recovery = %#v, err=%v", recovered, err)
	}
	assertLocalAuthorityRetryEvidence(t, fixture.authority, 0, 1, 0)
}

func TestLocalNudgeAuthorityStartupFinalizesInterruptedRetryAfterStoreCommit(t *testing.T) {
	fixture := newLocalAuthorityProviderAttemptFixture(t)
	wantErr := errors.New("injected pre-finalization process death")
	request := localAuthorityRetryRequest(fixture.command)
	interrupted := &failOnceLocalRetryFinalizeAuthority{LocalNudgeAuthority: fixture.authority, err: wantErr}

	if _, err := fixture.repository.RetryProviderAttempt(t.Context(), request, fixture.partition, interrupted); !errors.Is(err, wantErr) {
		t.Fatalf("RetryProviderAttempt before process death error = %v, want %v", err, wantErr)
	}
	assertLocalAuthorityRetryEvidence(t, fixture.authority, 1, 0, 1)
	state, err := fixture.repository.State(t.Context())
	if err != nil {
		t.Fatalf("State after committed retry: %v", err)
	}
	if err := fixture.authority.Close(); err != nil {
		t.Fatalf("Close interrupted authority: %v", err)
	}
	reopened, err := OpenLocalNudgeAuthority(t.Context(), fixture.cityPath, state, localAuthorityOptions())
	if err != nil {
		t.Fatalf("OpenLocalNudgeAuthority after process death: %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })

	if err := reopened.RecoverCommandAuthority(t.Context(), fixture.repository); err != nil {
		t.Fatalf("RecoverCommandAuthority after committed retry: %v", err)
	}
	assertLocalAuthorityRetryEvidence(t, reopened, 0, 1, 0)

	claimAt := request.NextEligibleAt
	claimed, err := fixture.repository.ClaimAuthorized(t.Context(), CommandClaimRequest{
		CommandID: fixture.command.ID, ClaimID: "claim-recovered-retry-2", OwnerID: "owner-recovered-retry-2",
		AttemptID: "attempt-recovered-retry-2", BoundLaunchIdentity: fixture.command.Binding.LaunchIdentity,
		Partition: fixture.partition, ClaimedAt: claimAt, LeaseUntil: claimAt.Add(time.Minute),
	}, reopened, reopened)
	if err != nil || claimed.Disposition != CommandClaimAllowed || claimed.Command.Retry == nil || claimed.Command.Retry.AttemptCount != 2 {
		t.Fatalf("ClaimAuthorized after recovered retry = %#v, err=%v", claimed, err)
	}
}

func TestLocalNudgeAuthorityStartupAbortsInterruptedRetryAfterStoreRollback(t *testing.T) {
	fixture := newLocalAuthorityProviderAttemptFixture(t)
	state, err := fixture.repository.State(t.Context())
	if err != nil {
		t.Fatalf("State before retry preparation: %v", err)
	}
	intent := localAuthorityRetryIntent(t, state, fixture.command, fixture.partition)
	if err := fixture.authority.PrepareCommandRetryTransition(t.Context(), intent); err != nil {
		t.Fatalf("PrepareCommandRetryTransition: %v", err)
	}
	if err := fixture.authority.ReleaseCommandRetryTransitionWriter(t.Context(), intent); err != nil {
		t.Fatalf("ReleaseCommandRetryTransitionWriter before process death: %v", err)
	}
	assertLocalAuthorityRetryEvidence(t, fixture.authority, 1, 0, 1)
	if err := fixture.authority.Close(); err != nil {
		t.Fatalf("Close interrupted authority: %v", err)
	}
	reopened, err := OpenLocalNudgeAuthority(t.Context(), fixture.cityPath, state, localAuthorityOptions())
	if err != nil {
		t.Fatalf("OpenLocalNudgeAuthority after rolled-back retry: %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })

	if err := reopened.RecoverCommandAuthority(t.Context(), fixture.repository); err != nil {
		t.Fatalf("RecoverCommandAuthority after rolled-back retry: %v", err)
	}
	assertLocalAuthorityRetryEvidence(t, reopened, 0, 0, 1)

	result, err := fixture.repository.RetryProviderAttempt(t.Context(), localAuthorityRetryRequest(fixture.command), fixture.partition, reopened)
	if err != nil || result.Disposition != CommandRetryRecorded || !result.HasRetryTransitionWitness() {
		t.Fatalf("RetryProviderAttempt after recovered rollback = %#v, err=%v", result, err)
	}
}

func TestLocalNudgeAuthorityTerminalAndRetryPreparationsAreExclusive(t *testing.T) {
	for _, terminalFirst := range []bool{true, false} {
		name := "retry-first"
		if terminalFirst {
			name = "terminal-first"
		}
		t.Run(name, func(t *testing.T) {
			fixture := newLocalAuthorityProviderAttemptFixture(t)
			state, err := fixture.repository.State(t.Context())
			if err != nil {
				t.Fatalf("State: %v", err)
			}
			retryIntent := localAuthorityRetryIntent(t, state, fixture.command, fixture.partition)
			terminalCommand, err := terminalizeExpiredCommand(cloneCommandValue(fixture.command), fixture.command.ExpiresAt)
			if err != nil {
				t.Fatalf("terminalizeExpiredCommand: %v", err)
			}
			terminalCommand.Order.Revision = state.Revision + 1
			terminalIntent, err := terminalIntentForTransition(state.Revision, fixture.command, terminalCommand, fixture.partition)
			if err != nil {
				t.Fatalf("terminalIntentForTransition: %v", err)
			}

			if terminalFirst {
				if err := fixture.authority.PrepareCommandPartitionTerminal(t.Context(), terminalIntent); err != nil {
					t.Fatalf("PrepareCommandPartitionTerminal: %v", err)
				}
				if err := fixture.authority.PrepareCommandRetryTransition(t.Context(), retryIntent); !errors.Is(err, ErrLocalNudgeAuthorityConflict) {
					t.Fatalf("PrepareCommandRetryTransition with terminal preparation error = %v, want conflict", err)
				}
				return
			}
			if err := fixture.authority.PrepareCommandRetryTransition(t.Context(), retryIntent); err != nil {
				t.Fatalf("PrepareCommandRetryTransition: %v", err)
			}
			if err := fixture.authority.PrepareCommandPartitionTerminal(t.Context(), terminalIntent); !errors.Is(err, ErrLocalNudgeAuthorityConflict) {
				t.Fatalf("PrepareCommandPartitionTerminal with retry preparation error = %v, want conflict", err)
			}
		})
	}
}

func TestLocalNudgeAuthorityRetryPreparationIsIdempotentAndRejectsConflict(t *testing.T) {
	fixture := newLocalAuthorityProviderAttemptFixture(t)
	state, err := fixture.repository.State(t.Context())
	if err != nil {
		t.Fatalf("State: %v", err)
	}
	intent := localAuthorityRetryIntent(t, state, fixture.command, fixture.partition)
	if err := fixture.authority.PrepareCommandRetryTransition(t.Context(), intent); err != nil {
		t.Fatalf("PrepareCommandRetryTransition(first): %v", err)
	}
	if err := fixture.authority.PrepareCommandRetryTransition(t.Context(), intent); err != nil {
		t.Fatalf("PrepareCommandRetryTransition(idempotent peer): %v", err)
	}
	conflict := intent
	conflict.Retry.ErrorDetail = "different sanitized definite-non-entry detail"
	if err := fixture.authority.PrepareCommandRetryTransition(t.Context(), conflict); !errors.Is(err, ErrLocalNudgeAuthorityConflict) {
		t.Fatalf("PrepareCommandRetryTransition(conflict) error = %v, want conflict", err)
	}
	assertLocalAuthorityRetryEvidence(t, fixture.authority, 1, 0, 1)

	if err := fixture.authority.AbortCommandRetryTransition(t.Context(), intent); err != nil {
		t.Fatalf("AbortCommandRetryTransition(first peer): %v", err)
	}
	assertLocalAuthorityRetryEvidence(t, fixture.authority, 1, 0, 1)
	if err := fixture.authority.ReleaseCommandRetryTransitionWriter(t.Context(), intent); err != nil {
		t.Fatalf("ReleaseCommandRetryTransitionWriter(first peer): %v", err)
	}
	if err := fixture.authority.AbortCommandRetryTransition(t.Context(), intent); err != nil {
		t.Fatalf("AbortCommandRetryTransition(last peer): %v", err)
	}
	if err := fixture.authority.ReleaseCommandRetryTransitionWriter(t.Context(), intent); err != nil {
		t.Fatalf("ReleaseCommandRetryTransitionWriter(last peer): %v", err)
	}
	assertLocalAuthorityRetryEvidence(t, fixture.authority, 0, 0, 1)
}

func TestLocalNudgeAuthorityRetryPreparationRejectsRepositoryStateBehindEffectFence(t *testing.T) {
	fixture := newLocalAuthorityProviderAttemptFixture(t)
	state, err := fixture.repository.State(t.Context())
	if err != nil {
		t.Fatalf("State: %v", err)
	}
	intent := localAuthorityRetryIntent(t, state, fixture.command, fixture.partition)

	advanced := state
	advanced.Revision++
	if err := fixture.authority.RecordCommandRepositoryEffectFence(t.Context(), advanced); err != nil {
		t.Fatalf("RecordCommandRepositoryEffectFence: %v", err)
	}
	if err := fixture.authority.PrepareCommandRetryTransition(t.Context(), intent); !errors.Is(err, ErrLocalNudgeAuthorityConflict) {
		t.Fatalf("PrepareCommandRetryTransition behind effect fence error = %v, want conflict", err)
	}
	assertLocalAuthorityRetryEvidence(t, fixture.authority, 0, 0, 1)
}

func TestLocalNudgeAuthorityImmutableRetryReceiptRejectsConflictingFinalize(t *testing.T) {
	fixture := newLocalAuthorityProviderAttemptFixture(t)
	request := localAuthorityRetryRequest(fixture.command)
	result, err := fixture.repository.RetryProviderAttempt(t.Context(), request, fixture.partition, fixture.authority)
	if err != nil {
		t.Fatalf("RetryProviderAttempt: %v", err)
	}
	state, err := fixture.repository.State(t.Context())
	if err != nil {
		t.Fatalf("State: %v", err)
	}
	commit, err := commandRetryTransitionCommitFor(state, result.Command, request, fixture.partition)
	if err != nil {
		t.Fatalf("commandRetryTransitionCommitFor: %v", err)
	}
	commit.AfterCommandDigest[0] ^= 0xff
	if _, err := fixture.authority.FinalizeCommandRetryTransition(t.Context(), commit); !errors.Is(err, ErrLocalNudgeAuthorityConflict) {
		t.Fatalf("FinalizeCommandRetryTransition(conflict) error = %v, want conflict", err)
	}
	assertLocalAuthorityRetryEvidence(t, fixture.authority, 0, 1, 0)
}

func TestLocalNudgeAuthorityLateRetryFinalizePreservesNewerAttemptWriter(t *testing.T) {
	fixture := newLocalAuthorityProviderAttemptFixture(t)
	firstRequest := localAuthorityRetryRequest(fixture.command)
	firstRetry, err := fixture.repository.RetryProviderAttempt(t.Context(), firstRequest, fixture.partition, fixture.authority)
	if err != nil || firstRetry.Disposition != CommandRetryRecorded {
		t.Fatalf("first RetryProviderAttempt = %#v, err=%v", firstRetry, err)
	}
	firstState, err := fixture.repository.State(t.Context())
	if err != nil {
		t.Fatalf("State after first retry: %v", err)
	}
	firstCommit, err := commandRetryTransitionCommitFor(firstState, firstRetry.Command, firstRequest, fixture.partition)
	if err != nil {
		t.Fatalf("commandRetryTransitionCommitFor(first): %v", err)
	}

	secondClaimAt := firstRequest.NextEligibleAt
	secondClaim, err := fixture.repository.ClaimAuthorized(t.Context(), CommandClaimRequest{
		CommandID: firstRetry.Command.ID, ClaimID: "claim-late-finalize-2", OwnerID: "owner-late-finalize-2",
		AttemptID: "attempt-late-finalize-2", BoundLaunchIdentity: firstRetry.Command.Binding.LaunchIdentity,
		Partition: fixture.partition, ClaimedAt: secondClaimAt, LeaseUntil: secondClaimAt.Add(time.Minute),
	}, fixture.authority, fixture.authority)
	if err != nil || secondClaim.Disposition != CommandClaimAllowed {
		t.Fatalf("second ClaimAuthorized = %#v, err=%v", secondClaim, err)
	}
	secondState, err := fixture.repository.State(t.Context())
	if err != nil {
		t.Fatalf("State after second claim: %v", err)
	}
	secondIntent := localAuthorityRetryIntent(t, secondState, secondClaim.Command, fixture.partition)
	if err := fixture.authority.PrepareCommandRetryTransition(t.Context(), secondIntent); err != nil {
		t.Fatalf("PrepareCommandRetryTransition(second): %v", err)
	}

	disposition, err := fixture.authority.FinalizeCommandRetryTransition(t.Context(), firstCommit)
	if err != nil || disposition != CommandRetryReceiptAlreadyFinalized {
		t.Fatalf("late first FinalizeCommandRetryTransition = %q, err=%v", disposition, err)
	}
	fixture.authority.retryOwnershipMu.Lock()
	owner, found := fixture.authority.retryOwners[secondIntent.CommandID]
	fixture.authority.retryOwnershipMu.Unlock()
	if !found || owner.writers != 1 || !sameCommandRetryTransitionIntent(owner.intent, secondIntent) {
		t.Fatalf("newer retry writer after late finalize = %#v, found=%t; want exact retained owner", owner, found)
	}
	if err := fixture.authority.AbortCommandRetryTransition(t.Context(), secondIntent); err != nil {
		t.Fatalf("AbortCommandRetryTransition(second): %v", err)
	}
	if err := fixture.authority.ReleaseCommandRetryTransitionWriter(t.Context(), secondIntent); err != nil {
		t.Fatalf("ReleaseCommandRetryTransitionWriter(second): %v", err)
	}
}

func TestClaimAuthorizedRetriesOnlyAfterExactLatestAuthorityReceipt(t *testing.T) {
	fixture := newLocalAuthorityProviderAttemptFixture(t)
	oldAttempt := cloneCommandValue(fixture.command)
	retryRequest := localAuthorityRetryRequest(oldAttempt)
	retried, err := fixture.repository.RetryProviderAttempt(t.Context(), retryRequest, fixture.partition, fixture.authority)
	if err != nil || retried.Disposition != CommandRetryRecorded {
		t.Fatalf("RetryProviderAttempt = %#v, err=%v", retried, err)
	}

	claimAt := retryRequest.NextEligibleAt
	claimRequest := CommandClaimRequest{
		CommandID: oldAttempt.ID, ClaimID: "claim-local-retry-2", OwnerID: "owner-local-retry-2",
		AttemptID: "attempt-local-retry-2", BoundLaunchIdentity: oldAttempt.Binding.LaunchIdentity,
		Partition: fixture.partition, ClaimedAt: claimAt, LeaseUntil: claimAt.Add(time.Minute),
	}
	claimed, err := fixture.repository.ClaimAuthorized(t.Context(), claimRequest, fixture.authority, fixture.authority)
	if err != nil || claimed.Disposition != CommandClaimAllowed {
		t.Fatalf("ClaimAuthorized(retry) = %#v, err=%v", claimed, err)
	}
	if claimed.Command.Retry == nil || claimed.Command.Retry.AttemptCount != 2 || claimed.Command.Retry.AttemptID != claimRequest.AttemptID ||
		claimed.Command.Retry.ClaimID != claimRequest.ClaimID || claimed.Command.Retry.NextEligibleAt != nil || claimed.Command.Retry.ErrorClass != CommandErrorClassNone {
		t.Fatalf("second attempt evidence = %#v, want fresh attempt 2", claimed.Command.Retry)
	}

	stale, err := fixture.repository.CompleteProviderAttempt(t.Context(), providerAttemptCompletion(oldAttempt, CommandActionResultInjectedUnconfirmed), fixture.partition, fixture.authority)
	if err != nil || stale.Disposition != CommandCompletionStale || stale.Command.Retry == nil || stale.Command.Retry.AttemptID != claimRequest.AttemptID {
		t.Fatalf("stale attempt-N completion = %#v, err=%v; want N+1 unchanged", stale, err)
	}
}

func TestClaimAuthorizedRejectsPendingRetryWhenReceiptDigestIsTampered(t *testing.T) {
	fixture := newLocalAuthorityProviderAttemptFixture(t)
	request := localAuthorityRetryRequest(fixture.command)
	if _, err := fixture.repository.RetryProviderAttempt(t.Context(), request, fixture.partition, fixture.authority); err != nil {
		t.Fatalf("RetryProviderAttempt: %v", err)
	}
	if _, err := fixture.authority.db.ExecContext(t.Context(), `UPDATE retry_receipts SET after_digest = zeroblob(32)
		WHERE command_id = ? AND attempt_id = ?`, fixture.command.ID, fixture.command.Claim.AttemptID); err != nil {
		t.Fatalf("tamper retry receipt digest: %v", err)
	}

	claimAt := request.NextEligibleAt
	claimRequest := CommandClaimRequest{
		CommandID: fixture.command.ID, ClaimID: "claim-after-tamper", OwnerID: "owner-after-tamper",
		AttemptID: "attempt-after-tamper", BoundLaunchIdentity: fixture.command.Binding.LaunchIdentity,
		Partition: fixture.partition, ClaimedAt: claimAt, LeaseUntil: claimAt.Add(time.Minute),
	}
	result, err := fixture.repository.ClaimAuthorized(t.Context(), claimRequest, fixture.authority, fixture.authority)
	if !errors.Is(err, ErrLocalNudgeAuthorityConflict) || result.Disposition != "" {
		t.Fatalf("ClaimAuthorized(tampered retry receipt) = %#v, err=%v; want conflict", result, err)
	}
	resolved, resolveErr := fixture.repository.Get(t.Context(), fixture.command.ID)
	if resolveErr != nil || resolved.Entry.Command == nil || resolved.Entry.Command.State != CommandStatePending || resolved.Entry.Command.Claim != nil {
		t.Fatalf("command after rejected retry claim = %#v, err=%v; want unchanged pending", resolved, resolveErr)
	}
}

func TestLocalNudgeAuthorityFinalizedPendingRetrySurvivesRestartAndReclaim(t *testing.T) {
	cityPath := t.TempDir()
	store := newRepositoryAtomicTestStore()
	repository := newVerifiedCommandRepository(t, store)
	state, err := repository.State(t.Context())
	if err != nil {
		t.Fatalf("State: %v", err)
	}
	authority, err := OpenLocalNudgeAuthority(t.Context(), cityPath, state, localAuthorityOptions())
	if err != nil {
		t.Fatalf("OpenLocalNudgeAuthority: %v", err)
	}
	now := time.Date(2026, 7, 16, 4, 0, 0, 0, time.UTC)
	ingress, err := newTrustedNudgeIngressWithClock(repository, authority, func() time.Time { return now })
	if err != nil {
		t.Fatalf("newTrustedNudgeIngressWithClock: %v", err)
	}
	admitted, err := ingress.Admit(WithAuthenticatedNudgeRequester(t.Context(), localAuthorityRequester()), validNudgeIngressRequest(now))
	if err != nil || admitted.Entry.Command == nil {
		t.Fatalf("Admit = %#v, err=%v", admitted, err)
	}
	firstClaimAt := admitted.Entry.Command.DeliverAfter.Add(time.Second)
	first, err := repository.ClaimAuthorized(t.Context(), CommandClaimRequest{
		CommandID: admitted.Entry.Command.ID, ClaimID: "claim-restart-retry-1", OwnerID: "owner-restart-retry-1",
		AttemptID: "attempt-restart-retry-1", BoundLaunchIdentity: "launch-123", Partition: admitted.Partition,
		ClaimedAt: firstClaimAt, LeaseUntil: firstClaimAt.Add(time.Minute),
	}, authority, authority)
	if err != nil || first.Disposition != CommandClaimAllowed {
		t.Fatalf("first ClaimAuthorized = %#v, err=%v", first, err)
	}
	retryRequest := localAuthorityRetryRequest(first.Command)
	if _, err := repository.RetryProviderAttempt(t.Context(), retryRequest, admitted.Partition, authority); err != nil {
		t.Fatalf("RetryProviderAttempt: %v", err)
	}
	state, err = repository.State(t.Context())
	if err != nil {
		t.Fatalf("State after retry: %v", err)
	}
	if err := authority.Close(); err != nil {
		t.Fatalf("Close before restart: %v", err)
	}
	authority, err = OpenLocalNudgeAuthority(t.Context(), cityPath, state, localAuthorityOptions())
	if err != nil {
		t.Fatalf("OpenLocalNudgeAuthority after retry: %v", err)
	}
	defer func() { _ = authority.Close() }()
	if err := authority.RecoverCommandAuthority(t.Context(), repository); err != nil {
		t.Fatalf("RecoverCommandAuthority after retry: %v", err)
	}

	secondClaimAt := retryRequest.NextEligibleAt
	second, err := repository.ClaimAuthorized(t.Context(), CommandClaimRequest{
		CommandID: first.Command.ID, ClaimID: "claim-restart-retry-2", OwnerID: "owner-restart-retry-2",
		AttemptID: "attempt-restart-retry-2", BoundLaunchIdentity: first.Command.Binding.LaunchIdentity,
		Partition: admitted.Partition, ClaimedAt: secondClaimAt, LeaseUntil: secondClaimAt.Add(time.Minute),
	}, authority, authority)
	if err != nil || second.Disposition != CommandClaimAllowed || second.Command.Retry == nil || second.Command.Retry.AttemptCount != 2 {
		t.Fatalf("second ClaimAuthorized after restart = %#v, err=%v", second, err)
	}
}

type failOnceLocalRetryFinalizeAuthority struct {
	*LocalNudgeAuthority
	err error
}

func (a *failOnceLocalRetryFinalizeAuthority) FinalizeCommandRetryTransition(ctx context.Context, commit CommandRetryTransitionCommit) (CommandRetryReceiptDisposition, error) {
	if a.err != nil {
		err := a.err
		a.err = nil
		return "", err
	}
	return a.LocalNudgeAuthority.FinalizeCommandRetryTransition(ctx, commit)
}

func localAuthorityRetryRequest(command Command) CommandRetryRequest {
	observedAt := command.Claim.ClaimedAt.Add(100 * time.Millisecond)
	return CommandRetryRequest{
		CommandID: command.ID, ClaimID: command.Claim.ID, OperationID: command.Claim.OperationID,
		AttemptID: command.Claim.AttemptID, ObservedAt: observedAt, NextEligibleAt: observedAt.Add(250 * time.Millisecond),
		ErrorClass: CommandErrorClassProviderBusy, Detail: "local provider proved native entry did not occur",
		ProviderStage: ProviderStageNotEntered, Completion: CompletionStateNotCompleted,
	}
}

func localAuthorityRetryIntent(t *testing.T, state CommandRepositoryState, command Command, partition TrustedCityPartition) CommandRetryTransitionIntent {
	t.Helper()
	request := localAuthorityRetryRequest(command)
	after := cloneCommandValue(command)
	after.State = CommandStatePending
	after.Order.Revision = state.Revision + 1
	after.Claim = nil
	after.Retry.NextEligibleAt = commandRetryTimePointer(request.NextEligibleAt)
	after.Retry.ErrorClass = request.ErrorClass
	after.Retry.ErrorDetail = request.Detail
	intent, err := commandRetryTransitionIntentFor(state, command, after, request.ObservedAt, partition)
	if err != nil {
		t.Fatalf("commandRetryTransitionIntentFor: %v", err)
	}
	return intent
}

func assertLocalAuthorityRetryEvidence(t *testing.T, authority *LocalNudgeAuthority, preparations, receipts, claimReceipts int) {
	t.Helper()
	var gotPreparations, gotReceipts, gotClaimReceipts int
	if err := authority.db.QueryRowContext(t.Context(), `SELECT
		(SELECT COUNT(*) FROM retry_preparations),
		(SELECT COUNT(*) FROM retry_receipts),
		(SELECT COUNT(*) FROM claim_receipts)`).Scan(&gotPreparations, &gotReceipts, &gotClaimReceipts); err != nil {
		t.Fatalf("read retry authority evidence: %v", err)
	}
	if gotPreparations != preparations || gotReceipts != receipts || gotClaimReceipts != claimReceipts {
		t.Fatalf("retry/claim evidence = %d/%d/%d, want %d/%d/%d", gotPreparations, gotReceipts, gotClaimReceipts, preparations, receipts, claimReceipts)
	}
}

func createExactLocalNudgeAuthorityV3(t *testing.T, cityPath string, state CommandRepositoryState, opts LocalNudgeAuthorityOptions) {
	t.Helper()
	path := LocalNudgeAuthorityPath(cityPath)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("create v3 authority parent: %v", err)
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatalf("create v3 authority: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("close v3 authority file: %v", err)
	}
	db := openLocalAuthorityFixtureDB(t, cityPath)
	defer func() { _ = db.Close() }()
	tx, err := db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatalf("begin v3 schema: %v", err)
	}
	defer func() { _ = tx.Rollback() }()
	for _, statement := range localNudgeAuthorityV3SchemaStatements {
		if _, err := tx.ExecContext(t.Context(), statement.sql); err != nil {
			t.Fatalf("create v3 schema object %s:%s: %v", statement.objectType, statement.name, err)
		}
	}
	initialClaimAudit := localAuthorityClaimAuditCursor{phase: localAuthorityClaimAuditIdle}
	digest := localAuthorityClaimAuditCursorDigest(initialClaimAudit, state.Store, opts.AuthorityID)
	if _, err := tx.ExecContext(t.Context(), `INSERT INTO authority_meta (
		singleton, schema_version, profile, store_uuid, restore_epoch, authority_id, issuer,
		tenant_scope, city_scope, credential_class, policy_version, principal_schema, dense_decision_high_water,
		highest_observed_sequence, highest_observed_revision, claim_transition_generation, claim_audit_checkpoint_digest
	) VALUES (1, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		localNudgeAuthorityV3Schema, opts.Profile, state.Store.StoreUUID, encodeLocalAuthorityUint64(state.Store.RestoreEpoch),
		opts.AuthorityID, opts.Issuer, opts.TenantScope, opts.CityScope, opts.CredentialClass, opts.PolicyVersion,
		NudgePrincipalSchemaVersion, encodeLocalAuthorityUint64(0), encodeLocalAuthorityUint64(state.SequenceHighWater),
		encodeLocalAuthorityUint64(state.Revision), encodeLocalAuthorityUint64(0), digest[:]); err != nil {
		t.Fatalf("insert v3 authority metadata: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit v3 authority: %v", err)
	}
}

func openLocalAuthorityFixtureDB(t *testing.T, cityPath string) *sql.DB {
	t.Helper()
	dsn := (&url.URL{Scheme: "file", Path: LocalNudgeAuthorityPath(cityPath)}).String()
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("open authority fixture: %v", err)
	}
	return db
}
