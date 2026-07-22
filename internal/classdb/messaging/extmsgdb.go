package messagingdb

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/extmsg"
)

// This file is the extmsg half of the messaging-class store: the seven typed
// tables (migration Version 2) that replace the gc:extmsg-* label-KV beads,
// implementing internal/extmsg's fabricBackend seam structurally
// (extmsg/backend.go is the contract). The pre-seam decode-time invariant
// checks are schema here: partial UNIQUE indexes reject the states the bd
// backend could only detect after corruption (multiple active bindings, dup
// open groups/participants/memberships/states, duplicate provider message
// ids). The sha256 locator labels die — lookups are column indexes.
//
// The bd model's "retained retired-session lookup label" during a
// participant handover maps to the pending_cleanup column: a participant is
// discoverable by a session id when it either targets it (session_id) or
// still owes it cleanup (pending_cleanup), which is exactly the
// retry-discoverability contract ReassignSessionParticipants needs.
// DropParticipantSessionLabel is therefore a no-op here — clearing
// pending_cleanup (done by the membership-migration writeback) IS the drop.

// extmsgDDL is migration Version 2: the extmsg typed tables. Conversation
// identity is the shared six-column tuple; timestamps are UnixNano INTEGERs
// (0 = zero time, NULL expiry = no expiry); meta is the user-metadata
// passthrough map as JSON.
func extmsgDDL() []string {
	const convCols = `
		scope_id               TEXT NOT NULL,
		provider               TEXT NOT NULL,
		account_id             TEXT NOT NULL,
		conversation_id        TEXT NOT NULL,
		parent_conversation_id TEXT NOT NULL DEFAULT '',
		kind                   TEXT NOT NULL,`
	const convTuple = `scope_id, provider, account_id, conversation_id, parent_conversation_id, kind`
	return []string{
		`CREATE TABLE IF NOT EXISTS extmsg_bindings (
			id             TEXT PRIMARY KEY,
			schema_version INTEGER NOT NULL DEFAULT 1,` + convCols + `
			session_id      TEXT NOT NULL DEFAULT '',
			session_name    TEXT NOT NULL DEFAULT '',
			agent_name      TEXT NOT NULL DEFAULT '',
			generation      INTEGER NOT NULL,
			bound_at        INTEGER NOT NULL,
			expires_at      INTEGER,
			last_touched_at INTEGER NOT NULL DEFAULT 0,
			created_by_kind TEXT NOT NULL DEFAULT '',
			created_by_id   TEXT NOT NULL DEFAULT '',
			status          TEXT NOT NULL DEFAULT 'active' CHECK(status IN ('active','ended')),
			ended_at        INTEGER,
			meta            TEXT NOT NULL DEFAULT '{}'
		)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_extmsg_binding_one_active
			ON extmsg_bindings(` + convTuple + `) WHERE status = 'active'`,
		`CREATE INDEX IF NOT EXISTS idx_extmsg_binding_conv ON extmsg_bindings(` + convTuple + `)`,
		`CREATE INDEX IF NOT EXISTS idx_extmsg_binding_session ON extmsg_bindings(session_id) WHERE status = 'active'`,
		`CREATE INDEX IF NOT EXISTS idx_extmsg_binding_agent ON extmsg_bindings(agent_name) WHERE status = 'active'`,

		`CREATE TABLE IF NOT EXISTS extmsg_delivery_contexts (
			id             TEXT PRIMARY KEY,
			schema_version INTEGER NOT NULL DEFAULT 1,` + convCols + `
			session_id        TEXT NOT NULL,
			generation        INTEGER NOT NULL,
			last_published_at INTEGER NOT NULL DEFAULT 0,
			last_message_id   TEXT NOT NULL DEFAULT '',
			source_session_id TEXT NOT NULL DEFAULT '',
			status            TEXT NOT NULL DEFAULT 'open' CHECK(status IN ('open','closed')),
			closed_at         INTEGER,
			meta              TEXT NOT NULL DEFAULT '{}'
		)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_extmsg_delivery_one_open
			ON extmsg_delivery_contexts(` + convTuple + `, session_id) WHERE status = 'open'`,
		`CREATE INDEX IF NOT EXISTS idx_extmsg_delivery_session ON extmsg_delivery_contexts(session_id) WHERE status = 'open'`,

		`CREATE TABLE IF NOT EXISTS extmsg_groups (
			id             TEXT PRIMARY KEY,
			schema_version INTEGER NOT NULL DEFAULT 1,` + convCols + `
			mode                    TEXT NOT NULL,
			default_handle          TEXT NOT NULL DEFAULT '',
			last_addressed_handle   TEXT NOT NULL DEFAULT '',
			fanout_enabled          INTEGER NOT NULL DEFAULT 0,
			fanout_allow_untargeted INTEGER NOT NULL DEFAULT 0,
			fanout_max_peer_triggered_publishes INTEGER NOT NULL DEFAULT 0,
			fanout_max_total_peer_deliveries    INTEGER NOT NULL DEFAULT 0,
			status    TEXT NOT NULL DEFAULT 'open' CHECK(status IN ('open','closed')),
			closed_at INTEGER,
			meta      TEXT NOT NULL DEFAULT '{}'
		)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_extmsg_group_one_open
			ON extmsg_groups(` + convTuple + `) WHERE status = 'open'`,

		`CREATE TABLE IF NOT EXISTS extmsg_participants (
			id             TEXT PRIMARY KEY,
			schema_version INTEGER NOT NULL DEFAULT 1,
			group_id        TEXT NOT NULL,
			handle          TEXT NOT NULL,
			session_id      TEXT NOT NULL DEFAULT '',
			session_name    TEXT NOT NULL DEFAULT '',
			public          INTEGER NOT NULL DEFAULT 0,
			pending_cleanup TEXT NOT NULL DEFAULT '',
			status          TEXT NOT NULL DEFAULT 'open' CHECK(status IN ('open','closed')),
			closed_at       INTEGER,
			meta            TEXT NOT NULL DEFAULT '{}'
		)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_extmsg_participant_one_open
			ON extmsg_participants(group_id, handle) WHERE status = 'open'`,
		`CREATE INDEX IF NOT EXISTS idx_extmsg_participant_group ON extmsg_participants(group_id)`,
		`CREATE INDEX IF NOT EXISTS idx_extmsg_participant_session ON extmsg_participants(session_id) WHERE status = 'open'`,

		`CREATE TABLE IF NOT EXISTS extmsg_transcript_state (
			id             TEXT PRIMARY KEY,
			schema_version INTEGER NOT NULL DEFAULT 1,` + convCols + `
			next_sequence               INTEGER NOT NULL,
			earliest_available_sequence INTEGER NOT NULL,
			hydration_status            TEXT NOT NULL,
			oldest_hydrated_message_id  TEXT NOT NULL DEFAULT '',
			max_retained_entries        INTEGER NOT NULL DEFAULT 0,
			meta                        TEXT NOT NULL DEFAULT '{}'
		)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_extmsg_state_one
			ON extmsg_transcript_state(` + convTuple + `)`,

		`CREATE TABLE IF NOT EXISTS extmsg_transcript_entries (
			id             TEXT PRIMARY KEY,
			schema_version INTEGER NOT NULL DEFAULT 1,` + convCols + `
			sequence            INTEGER NOT NULL,
			msg_kind            TEXT NOT NULL,
			provenance          TEXT NOT NULL,
			provider_message_id TEXT NOT NULL DEFAULT '',
			explicit_target     TEXT NOT NULL DEFAULT '',
			reply_to_message_id TEXT NOT NULL DEFAULT '',
			source_session_id   TEXT NOT NULL DEFAULT '',
			created_at          INTEGER NOT NULL,
			text                TEXT NOT NULL DEFAULT '',
			actor_json          TEXT NOT NULL DEFAULT '',
			attachments_json    TEXT NOT NULL DEFAULT '',
			meta                TEXT NOT NULL DEFAULT '{}'
		)`,
		`CREATE INDEX IF NOT EXISTS idx_extmsg_entry_seq
			ON extmsg_transcript_entries(` + convTuple + `, sequence)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_extmsg_entry_provider_msg
			ON extmsg_transcript_entries(` + convTuple + `, provider_message_id) WHERE provider_message_id != ''`,

		`CREATE TABLE IF NOT EXISTS extmsg_memberships (
			id             TEXT PRIMARY KEY,
			schema_version INTEGER NOT NULL DEFAULT 1,` + convCols + `
			session_id             TEXT NOT NULL,
			joined_at              INTEGER NOT NULL,
			joined_sequence        INTEGER NOT NULL,
			last_read_sequence     INTEGER NOT NULL DEFAULT 0,
			backfill_policy        TEXT NOT NULL,
			manual_backfill_policy TEXT NOT NULL DEFAULT '',
			owner_kinds            TEXT NOT NULL DEFAULT '',
			status                 TEXT NOT NULL DEFAULT 'open' CHECK(status IN ('open','closed')),
			closed_at              INTEGER,
			meta                   TEXT NOT NULL DEFAULT '{}'
		)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_extmsg_membership_one_open
			ON extmsg_memberships(` + convTuple + `, session_id) WHERE status = 'open'`,
		`CREATE INDEX IF NOT EXISTS idx_extmsg_membership_conv ON extmsg_memberships(` + convTuple + `) WHERE status = 'open'`,
		`CREATE INDEX IF NOT EXISTS idx_extmsg_membership_session ON extmsg_memberships(session_id) WHERE status = 'open'`,
	}
}

const whereConv = `scope_id = ? AND provider = ? AND account_id = ? AND conversation_id = ? AND parent_conversation_id = ? AND kind = ?`

func refArgs(ref extmsg.ConversationRef) []any {
	return []any{ref.ScopeID, ref.Provider, ref.AccountID, ref.ConversationID, ref.ParentConversationID, string(ref.Kind)}
}

func scanRef(scope, provider, account, conv, parent, kind string) extmsg.ConversationRef {
	return extmsg.ConversationRef{
		ScopeID:              scope,
		Provider:             provider,
		AccountID:            account,
		ConversationID:       conv,
		ParentConversationID: parent,
		Kind:                 extmsg.ConversationKind(kind),
	}
}

// encodeMeta serializes the user-metadata passthrough, trimming and dropping
// blank keys (the bd codec's copyMetadata semantics).
func encodeMeta(meta map[string]string) (string, error) {
	clean := map[string]string{}
	for k, v := range meta {
		k = strings.TrimSpace(k)
		if k == "" {
			continue
		}
		clean[k] = v
	}
	data, err := json.Marshal(clean)
	if err != nil {
		return "", fmt.Errorf("encoding metadata: %w", err)
	}
	return string(data), nil
}

// decodeMeta parses the meta column; empty decodes to a non-nil empty map,
// matching the bd decoder.
func decodeMeta(raw string) (map[string]string, error) {
	if strings.TrimSpace(raw) == "" {
		return map[string]string{}, nil
	}
	out := map[string]string{}
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil, fmt.Errorf("decoding metadata: %w", err)
	}
	return out, nil
}

// mergeMetaLocked merges patch into the row's meta column inside tx — the
// bd SetMetadataBatch semantics: mentioned keys are set (blank keys
// dropped), unmentioned keys persist.
func mergeMetaLocked(tx *sql.Tx, table, id string, patch map[string]string) error {
	if len(patch) == 0 {
		return nil
	}
	var raw string
	if err := tx.QueryRow(`SELECT meta FROM `+table+` WHERE id = ?`, id).Scan(&raw); err != nil {
		return err
	}
	current, err := decodeMeta(raw)
	if err != nil {
		return err
	}
	for k, v := range patch {
		k = strings.TrimSpace(k)
		if k == "" {
			continue
		}
		current[k] = v
	}
	encoded, err := encodeMeta(current)
	if err != nil {
		return err
	}
	_, err = tx.Exec(`UPDATE `+table+` SET meta = ? WHERE id = ?`, encoded, id)
	return err
}

func encodeOwners(owners []extmsg.MembershipOwner) string {
	parts := make([]string, 0, len(owners))
	for _, owner := range owners {
		if strings.TrimSpace(string(owner)) == "" {
			continue
		}
		parts = append(parts, string(owner))
	}
	return strings.Join(parts, ",")
}

func decodeOwners(raw string) []extmsg.MembershipOwner {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	out := make([]extmsg.MembershipOwner, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		out = append(out, extmsg.MembershipOwner(part))
	}
	return out
}

func encodePending(sessionIDs []string) string {
	parts := make([]string, 0, len(sessionIDs))
	for _, id := range sessionIDs {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		parts = append(parts, id)
	}
	return strings.Join(parts, ",")
}

func decodePending(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		out = append(out, part)
	}
	return out
}

func nullableNanos(t *time.Time) any {
	if t == nil || t.IsZero() {
		return nil
	}
	return t.UTC().UnixNano()
}

func timeFromNullable(n sql.NullInt64) *time.Time {
	if !n.Valid {
		return nil
	}
	t := time.Unix(0, n.Int64).UTC()
	return &t
}

func timeFromNanos(n int64) time.Time {
	if n == 0 {
		return time.Time{}
	}
	return time.Unix(0, n).UTC()
}

// mintID allocates the next messaging-class id inside tx (shared id_seq —
// one mint per class, the gcm prefix).
func mintID(tx *sql.Tx) (string, error) {
	var next int64
	if err := tx.QueryRow(`UPDATE id_seq SET next = next + 1 WHERE k = 1 RETURNING next`).Scan(&next); err != nil {
		return "", fmt.Errorf("minting id: %w", err)
	}
	return fmt.Sprintf("%s-%d", idPrefix, next), nil
}

func isUniqueViolation(err error, table string) bool {
	return err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed") && strings.Contains(err.Error(), table)
}

// --- capabilities / writer ---

// AtomicTx reports that this store's composite writes commit atomically.
func (s *Store) AtomicTx() bool { return true }

// Writer returns a standalone typed writer whose writes each commit in their
// own transaction.
func (s *Store) Writer() extmsg.FabricWriter { return standaloneFabricWriter{s: s} }

// standaloneFabricWriter commits each sub-write in its own transaction.
type standaloneFabricWriter struct{ s *Store }

// CreateMembership creates a membership row in its own transaction.
func (w standaloneFabricWriter) CreateMembership(create extmsg.MembershipCreate) (extmsg.ConversationMembershipRecord, error) {
	var out extmsg.ConversationMembershipRecord
	err := w.s.db.Write(context.Background(), func(tx *sql.Tx) error {
		var err error
		out, err = txFabricWriter{tx: tx}.CreateMembership(create)
		return err
	})
	return out, err
}

// PatchMembership patches a membership row in its own transaction.
func (w standaloneFabricWriter) PatchMembership(id string, patch extmsg.MembershipPatch) error {
	return w.s.db.Write(context.Background(), func(tx *sql.Tx) error {
		return txFabricWriter{tx: tx}.PatchMembership(id, patch)
	})
}

// CreateTranscriptState creates the first-touch state row in its own
// transaction.
func (w standaloneFabricWriter) CreateTranscriptState(ref extmsg.ConversationRef) (extmsg.ConversationTranscriptStateRecord, error) {
	var out extmsg.ConversationTranscriptStateRecord
	err := w.s.db.Write(context.Background(), func(tx *sql.Tx) error {
		var err error
		out, err = txFabricWriter{tx: tx}.CreateTranscriptState(ref)
		return err
	})
	return out, err
}

// txFabricWriter runs the sub-writes inside an enclosing transaction (a
// binding commit).
type txFabricWriter struct{ tx *sql.Tx }

// CreateMembership inserts a membership row with the service-computed
// owner/policy outcome.
func (w txFabricWriter) CreateMembership(create extmsg.MembershipCreate) (extmsg.ConversationMembershipRecord, error) {
	id, err := mintID(w.tx)
	if err != nil {
		return extmsg.ConversationMembershipRecord{}, err
	}
	meta, err := encodeMeta(create.Meta)
	if err != nil {
		return extmsg.ConversationMembershipRecord{}, err
	}
	args := append(refArgs(create.Ref),
		create.SessionID, nanos(create.JoinedAt), create.JoinedSequence,
		string(create.Backfill), create.ManualBackfill, encodeOwners(create.Owners), meta)
	if _, err := w.tx.Exec(`INSERT INTO extmsg_memberships
		(id, schema_version, scope_id, provider, account_id, conversation_id, parent_conversation_id, kind,
		 session_id, joined_at, joined_sequence, last_read_sequence, backfill_policy, manual_backfill_policy, owner_kinds, meta)
		VALUES (?, 1, ?, ?, ?, ?, ?, ?, ?, ?, ?, 0, ?, ?, ?, ?)`,
		append([]any{id}, args...)...); err != nil {
		return extmsg.ConversationMembershipRecord{}, fmt.Errorf("create membership: %w", err)
	}
	metaMap, err := decodeMeta(meta)
	if err != nil {
		return extmsg.ConversationMembershipRecord{}, err
	}
	return extmsg.ConversationMembershipRecord{
		ID:               id,
		SchemaVersion:    1,
		Conversation:     create.Ref,
		SessionID:        create.SessionID,
		JoinedAt:         create.JoinedAt.UTC(),
		JoinedSequence:   create.JoinedSequence,
		LastReadSequence: 0,
		BackfillPolicy:   create.Backfill,
		ManualBackfill:   extmsg.MembershipBackfillPolicy(create.ManualBackfill),
		Owners:           append([]extmsg.MembershipOwner(nil), create.Owners...),
		Metadata:         metaMap,
	}, nil
}

// PatchMembership applies a tri-state field patch plus the user-metadata
// merge.
func (w txFabricWriter) PatchMembership(id string, patch extmsg.MembershipPatch) error {
	sets := make([]string, 0, 3)
	args := make([]any, 0, 4)
	if patch.SetOwners {
		sets = append(sets, "owner_kinds = ?")
		args = append(args, encodeOwners(patch.Owners))
	}
	if patch.SetManual {
		sets = append(sets, "manual_backfill_policy = ?")
		args = append(args, patch.Manual)
	}
	if patch.SetBackfill {
		sets = append(sets, "backfill_policy = ?")
		args = append(args, string(patch.Backfill))
	}
	if len(sets) > 0 {
		args = append(args, id)
		res, err := w.tx.Exec(`UPDATE extmsg_memberships SET `+strings.Join(sets, ", ")+` WHERE id = ?`, args...)
		if err != nil {
			return err
		}
		affected, err := res.RowsAffected()
		if err != nil {
			return err
		}
		if affected == 0 {
			return fmt.Errorf("patching membership %q: %w", id, beads.ErrNotFound)
		}
	}
	return mergeMetaLocked(w.tx, "extmsg_memberships", id, patch.Meta)
}

// CreateTranscriptState inserts the conversation's first-touch state row.
func (w txFabricWriter) CreateTranscriptState(ref extmsg.ConversationRef) (extmsg.ConversationTranscriptStateRecord, error) {
	id, err := mintID(w.tx)
	if err != nil {
		return extmsg.ConversationTranscriptStateRecord{}, err
	}
	if _, err := w.tx.Exec(`INSERT INTO extmsg_transcript_state
		(id, schema_version, scope_id, provider, account_id, conversation_id, parent_conversation_id, kind,
		 next_sequence, earliest_available_sequence, hydration_status, max_retained_entries)
		VALUES (?, 1, ?, ?, ?, ?, ?, ?, 1, 1, ?, 0)`,
		append(append([]any{id}, refArgs(ref)...), string(extmsg.HydrationLiveOnly))...); err != nil {
		return extmsg.ConversationTranscriptStateRecord{}, fmt.Errorf("create transcript state: %w", err)
	}
	return extmsg.ConversationTranscriptStateRecord{
		ID:                        id,
		SchemaVersion:             1,
		Conversation:              ref,
		NextSequence:              1,
		EarliestAvailableSequence: 1,
		HydrationStatus:           extmsg.HydrationLiveOnly,
		Metadata:                  map[string]string{},
	}, nil
}

// --- bindings ---

const bindingCols = `id, schema_version, scope_id, provider, account_id, conversation_id, parent_conversation_id, kind,
	session_id, session_name, agent_name, generation, bound_at, expires_at, last_touched_at, status, meta`

func scanBinding(scan func(dest ...any) error) (extmsg.SessionBindingRecord, time.Time, error) {
	var (
		rec                            extmsg.SessionBindingRecord
		scope, provider, account, conv string
		parent, kind, status, meta     string
		boundAt, lastTouched           int64
		expires                        sql.NullInt64
	)
	if err := scan(&rec.ID, &rec.SchemaVersion, &scope, &provider, &account, &conv, &parent, &kind,
		&rec.SessionID, &rec.SessionName, &rec.AgentName, &rec.BindingGeneration,
		&boundAt, &expires, &lastTouched, &status, &meta); err != nil {
		return extmsg.SessionBindingRecord{}, time.Time{}, err
	}
	rec.Conversation = scanRef(scope, provider, account, conv, parent, kind)
	rec.BoundAt = timeFromNanos(boundAt)
	rec.ExpiresAt = timeFromNullable(expires)
	rec.Status = extmsg.BindingActive
	if status == "ended" {
		rec.Status = extmsg.BindingEnded
	}
	metaMap, err := decodeMeta(meta)
	if err != nil {
		return extmsg.SessionBindingRecord{}, time.Time{}, err
	}
	rec.Metadata = metaMap
	return rec, timeFromNanos(lastTouched), nil
}

func (s *Store) listBindings(query string, args ...any) ([]extmsg.SessionBindingRecord, error) {
	rows, err := s.db.Read().Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("listing bindings: %w", err)
	}
	defer rows.Close() //nolint:errcheck
	var out []extmsg.SessionBindingRecord
	for rows.Next() {
		rec, _, err := scanBinding(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// BindingHistory returns every binding row for ref, including ended ones.
func (s *Store) BindingHistory(ref extmsg.ConversationRef) ([]extmsg.SessionBindingRecord, error) {
	return s.listBindings(`SELECT `+bindingCols+` FROM extmsg_bindings WHERE `+whereConv+` ORDER BY id`, refArgs(ref)...)
}

// ActiveBindings returns every active binding row (the reaper scan).
func (s *Store) ActiveBindings() ([]extmsg.SessionBindingRecord, error) {
	return s.listBindings(`SELECT ` + bindingCols + ` FROM extmsg_bindings WHERE status = 'active' ORDER BY id`)
}

// ActiveBindingsBySession returns the active bindings targeting sessionID.
func (s *Store) ActiveBindingsBySession(sessionID string) ([]extmsg.SessionBindingRecord, error) {
	return s.listBindings(`SELECT `+bindingCols+` FROM extmsg_bindings WHERE status = 'active' AND session_id = ? ORDER BY id`, sessionID)
}

// ActiveBindingsByAgent returns the active bindings targeting agentName.
func (s *Store) ActiveBindingsByAgent(agentName string) ([]extmsg.SessionBindingRecord, error) {
	return s.listBindings(`SELECT `+bindingCols+` FROM extmsg_bindings WHERE status = 'active' AND agent_name = ? ORDER BY id`, agentName)
}

// GetBinding fetches one binding row plus its last-touched clock.
func (s *Store) GetBinding(id string) (extmsg.SessionBindingRecord, time.Time, error) {
	row := s.db.Read().QueryRow(`SELECT `+bindingCols+` FROM extmsg_bindings WHERE id = ?`, id)
	rec, lastTouched, err := scanBinding(row.Scan)
	if errors.Is(err, sql.ErrNoRows) {
		return extmsg.SessionBindingRecord{}, time.Time{}, fmt.Errorf("get binding %s: %w", id, beads.ErrNotFound)
	}
	if err != nil {
		return extmsg.SessionBindingRecord{}, time.Time{}, fmt.Errorf("get binding %s: %w", id, err)
	}
	return rec, lastTouched, nil
}

// GetOpenBinding fetches one binding row for the repair paths; ok is false
// when id is not an active binding row.
func (s *Store) GetOpenBinding(id string) (extmsg.SessionBindingRecord, bool, error) {
	rec, _, err := s.GetBinding(id)
	if errors.Is(err, beads.ErrNotFound) {
		return extmsg.SessionBindingRecord{}, false, nil
	}
	if err != nil {
		return extmsg.SessionBindingRecord{}, false, err
	}
	if rec.Status != extmsg.BindingActive {
		return extmsg.SessionBindingRecord{}, false, nil
	}
	return rec, true, nil
}

// CreateBinding creates a binding in one transaction, optionally ending a
// displaced binding first and running the membership sub-writes through the
// same transaction.
func (s *Store) CreateBinding(create extmsg.BindingCreate, displaceID string, membership func(extmsg.FabricWriter) error) (extmsg.SessionBindingRecord, error) {
	meta, err := encodeMeta(create.Meta)
	if err != nil {
		return extmsg.SessionBindingRecord{}, err
	}
	var id string
	err = s.db.Write(context.Background(), func(tx *sql.Tx) error {
		if displaceID != "" {
			res, err := tx.Exec(`UPDATE extmsg_bindings SET status = 'ended', ended_at = ? WHERE id = ? AND status = 'active'`,
				nanos(time.Now().UTC()), displaceID)
			if err != nil {
				return fmt.Errorf("close displaced binding %s: %w", displaceID, err)
			}
			affected, err := res.RowsAffected()
			if err != nil {
				return err
			}
			if affected == 0 {
				return fmt.Errorf("close displaced binding %s: %w", displaceID, beads.ErrNotFound)
			}
		}
		id, err = mintID(tx)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(`INSERT INTO extmsg_bindings
			(id, schema_version, scope_id, provider, account_id, conversation_id, parent_conversation_id, kind,
			 session_id, session_name, agent_name, generation, bound_at, expires_at, last_touched_at,
			 created_by_kind, created_by_id, status, meta)
			VALUES (?, 1, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'active', ?)`,
			append(append([]any{id}, refArgs(create.Ref)...),
				create.SessionID, create.SessionName, create.AgentName, create.Generation,
				nanos(create.BoundAt), nullableNanos(create.ExpiresAt), nanos(create.BoundAt),
				string(create.CreatedByKind), create.CreatedByID, meta)...); err != nil {
			if isUniqueViolation(err, "extmsg_bindings") {
				return fmt.Errorf("%w: conversation already has an active binding", extmsg.ErrBindingConflict)
			}
			return fmt.Errorf("create external binding: %w", err)
		}
		if membership != nil {
			return membership(txFabricWriter{tx: tx})
		}
		return nil
	})
	if err != nil {
		return extmsg.SessionBindingRecord{}, err
	}
	metaMap, err := decodeMeta(meta)
	if err != nil {
		return extmsg.SessionBindingRecord{}, err
	}
	return extmsg.SessionBindingRecord{
		ID:                id,
		SchemaVersion:     1,
		Conversation:      create.Ref,
		SessionID:         create.SessionID,
		SessionName:       create.SessionName,
		AgentName:         create.AgentName,
		Status:            extmsg.BindingActive,
		BoundAt:           create.BoundAt.UTC(),
		ExpiresAt:         create.ExpiresAt,
		BindingGeneration: create.Generation,
		Metadata:          metaMap,
	}, nil
}

// RefreshBinding re-stamps an active binding and re-runs its membership
// sub-writes in one transaction (the same-target rebind).
func (s *Store) RefreshBinding(_ extmsg.ConversationRef, id string, refresh extmsg.BindingRefresh, membership func(extmsg.FabricWriter) error) error {
	return s.db.Write(context.Background(), func(tx *sql.Tx) error {
		if refresh.SessionNameBackfill != "" {
			if _, err := tx.Exec(`UPDATE extmsg_bindings SET session_name = ? WHERE id = ?`,
				refresh.SessionNameBackfill, id); err != nil {
				return fmt.Errorf("backfill session name on binding %s: %w", id, err)
			}
		}
		res, err := tx.Exec(`UPDATE extmsg_bindings SET expires_at = ?, last_touched_at = ? WHERE id = ?`,
			nullableNanos(refresh.ExpiresAt), nanos(refresh.TouchedAt), id)
		if err != nil {
			return err
		}
		affected, err := res.RowsAffected()
		if err != nil {
			return err
		}
		if affected == 0 {
			return fmt.Errorf("refresh binding %s: %w", id, beads.ErrNotFound)
		}
		if err := mergeMetaLocked(tx, "extmsg_bindings", id, refresh.Meta); err != nil {
			return err
		}
		if membership != nil {
			return membership(txFabricWriter{tx: tx})
		}
		return nil
	})
}

// TouchBinding stamps the binding's last-touched clock.
func (s *Store) TouchBinding(id string, at time.Time) error {
	return s.writeExpectingRow(
		fmt.Sprintf("touching binding %q", id),
		`UPDATE extmsg_bindings SET last_touched_at = ? WHERE id = ?`, nanos(at), id,
	)
}

// CloseBinding ends an active binding row.
func (s *Store) CloseBinding(id string) error {
	return s.writeExpectingRow(
		fmt.Sprintf("closing binding %q", id),
		`UPDATE extmsg_bindings SET status = 'ended', ended_at = ? WHERE id = ? AND status = 'active'`,
		nanos(time.Now().UTC()), id,
	)
}

// ReassignBindingSession re-points a binding at a respawned session's bead
// id (canonical session repair).
func (s *Store) ReassignBindingSession(id string, oldSessionID, newSessionID string, touchedAt time.Time) error {
	err := s.writeExpectingRow(
		fmt.Sprintf("reassigning binding %q", id),
		`UPDATE extmsg_bindings SET session_id = ?, last_touched_at = ? WHERE id = ?`,
		newSessionID, nanos(touchedAt), id,
	)
	if err != nil {
		return fmt.Errorf("reassign binding %s from session %s to %s: %w", id, oldSessionID, newSessionID, err)
	}
	return nil
}

// --- delivery contexts ---

const deliveryCols = `id, schema_version, scope_id, provider, account_id, conversation_id, parent_conversation_id, kind,
	session_id, generation, last_published_at, last_message_id, source_session_id, meta`

func scanDelivery(scan func(dest ...any) error) (extmsg.DeliveryContextRecord, error) {
	var (
		rec                            extmsg.DeliveryContextRecord
		scope, provider, account, conv string
		parent, kind, meta             string
		published                      int64
	)
	if err := scan(&rec.ID, &rec.SchemaVersion, &scope, &provider, &account, &conv, &parent, &kind,
		&rec.SessionID, &rec.BindingGeneration, &published, &rec.LastMessageID, &rec.SourceSessionID, &meta); err != nil {
		return extmsg.DeliveryContextRecord{}, err
	}
	rec.Conversation = scanRef(scope, provider, account, conv, parent, kind)
	rec.LastPublishedAt = timeFromNanos(published)
	metaMap, err := decodeMeta(meta)
	if err != nil {
		return extmsg.DeliveryContextRecord{}, err
	}
	rec.Metadata = metaMap
	return rec, nil
}

// OpenDeliveryContexts returns the open delivery contexts for the
// (conversation, session) route.
func (s *Store) OpenDeliveryContexts(ref extmsg.ConversationRef, sessionID string) ([]extmsg.DeliveryContextRecord, error) {
	rows, err := s.db.Read().Query(
		`SELECT `+deliveryCols+` FROM extmsg_delivery_contexts WHERE `+whereConv+` AND session_id = ? AND status = 'open' ORDER BY id`,
		append(refArgs(ref), sessionID)...)
	if err != nil {
		return nil, fmt.Errorf("listing delivery contexts: %w", err)
	}
	defer rows.Close() //nolint:errcheck
	var out []extmsg.DeliveryContextRecord
	for rows.Next() {
		rec, err := scanDelivery(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// CreateDeliveryContext creates a delivery-context row.
func (s *Store) CreateDeliveryContext(f extmsg.DeliveryFields) error {
	meta, err := encodeMeta(f.Meta)
	if err != nil {
		return err
	}
	return s.db.Write(context.Background(), func(tx *sql.Tx) error {
		id, err := mintID(tx)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(`INSERT INTO extmsg_delivery_contexts
			(id, schema_version, scope_id, provider, account_id, conversation_id, parent_conversation_id, kind,
			 session_id, generation, last_published_at, last_message_id, source_session_id, status, meta)
			VALUES (?, 1, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'open', ?)`,
			append(append([]any{id}, refArgs(f.Ref)...),
				f.SessionID, f.BindingGeneration, nanos(f.LastPublishedAt),
				strings.TrimSpace(f.LastMessageID), strings.TrimSpace(f.SourceSessionID), meta)...); err != nil {
			return fmt.Errorf("create delivery context: %w", err)
		}
		return nil
	})
}

// UpdateDeliveryContext refreshes an existing delivery-context row.
func (s *Store) UpdateDeliveryContext(id string, f extmsg.DeliveryFields) error {
	return s.db.Write(context.Background(), func(tx *sql.Tx) error {
		res, err := tx.Exec(`UPDATE extmsg_delivery_contexts
			SET generation = ?, last_published_at = ?, last_message_id = ?, source_session_id = ?
			WHERE id = ?`,
			f.BindingGeneration, nanos(f.LastPublishedAt),
			strings.TrimSpace(f.LastMessageID), strings.TrimSpace(f.SourceSessionID), id)
		if err != nil {
			return fmt.Errorf("update delivery metadata: %w", err)
		}
		affected, err := res.RowsAffected()
		if err != nil {
			return err
		}
		if affected == 0 {
			return fmt.Errorf("update delivery metadata: %w", beads.ErrNotFound)
		}
		return mergeMetaLocked(tx, "extmsg_delivery_contexts", id, f.Meta)
	})
}

// CloseDeliveryContext closes a delivery-context row.
func (s *Store) CloseDeliveryContext(id string) error {
	return s.writeExpectingRow(
		fmt.Sprintf("closing delivery context %q", id),
		`UPDATE extmsg_delivery_contexts SET status = 'closed', closed_at = ? WHERE id = ? AND status = 'open'`,
		nanos(time.Now().UTC()), id,
	)
}

// --- groups ---

const groupCols = `id, schema_version, scope_id, provider, account_id, conversation_id, parent_conversation_id, kind,
	mode, default_handle, last_addressed_handle, fanout_enabled, fanout_allow_untargeted,
	fanout_max_peer_triggered_publishes, fanout_max_total_peer_deliveries, status, meta`

func scanGroup(scan func(dest ...any) error) (extmsg.ConversationGroupRecord, bool, error) {
	var (
		rec                            extmsg.ConversationGroupRecord
		scope, provider, account, conv string
		parent, kind, mode             string
		status, meta                   string
		enabled, untargeted            int
	)
	if err := scan(&rec.ID, &rec.SchemaVersion, &scope, &provider, &account, &conv, &parent, &kind,
		&mode, &rec.DefaultHandle, &rec.LastAddressedHandle, &enabled, &untargeted,
		&rec.FanoutPolicy.MaxPeerTriggeredPublishes, &rec.FanoutPolicy.MaxTotalPeerDeliveries,
		&status, &meta); err != nil {
		return extmsg.ConversationGroupRecord{}, false, err
	}
	rec.RootConversation = scanRef(scope, provider, account, conv, parent, kind)
	rec.Mode = extmsg.GroupMode(mode)
	rec.FanoutPolicy.Enabled = enabled != 0
	rec.FanoutPolicy.AllowUntargetedPublication = untargeted != 0
	metaMap, err := decodeMeta(meta)
	if err != nil {
		return extmsg.ConversationGroupRecord{}, false, err
	}
	rec.Metadata = metaMap
	return rec, status == "open", nil
}

// OpenGroupsByRoot returns the open groups rooted at ref.
func (s *Store) OpenGroupsByRoot(ref extmsg.ConversationRef) ([]extmsg.ConversationGroupRecord, error) {
	rows, err := s.db.Read().Query(
		`SELECT `+groupCols+` FROM extmsg_groups WHERE `+whereConv+` AND status = 'open' ORDER BY id`,
		refArgs(ref)...)
	if err != nil {
		return nil, fmt.Errorf("listing groups: %w", err)
	}
	defer rows.Close() //nolint:errcheck
	var out []extmsg.ConversationGroupRecord
	for rows.Next() {
		rec, _, err := scanGroup(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// GetGroup fetches one group row; ok is false when id is not an open group.
func (s *Store) GetGroup(id string) (extmsg.ConversationGroupRecord, bool, error) {
	row := s.db.Read().QueryRow(`SELECT `+groupCols+` FROM extmsg_groups WHERE id = ?`, id)
	rec, open, err := scanGroup(row.Scan)
	if errors.Is(err, sql.ErrNoRows) {
		return extmsg.ConversationGroupRecord{}, false, fmt.Errorf("get group %s: %w", id, beads.ErrNotFound)
	}
	if err != nil {
		return extmsg.ConversationGroupRecord{}, false, fmt.Errorf("get group %s: %w", id, err)
	}
	if !open {
		return extmsg.ConversationGroupRecord{}, false, nil
	}
	return rec, true, nil
}

// RefetchGroup re-reads a group row after an update.
func (s *Store) RefetchGroup(id string) (extmsg.ConversationGroupRecord, error) {
	row := s.db.Read().QueryRow(`SELECT `+groupCols+` FROM extmsg_groups WHERE id = ?`, id)
	rec, _, err := scanGroup(row.Scan)
	if err != nil {
		return extmsg.ConversationGroupRecord{}, fmt.Errorf("get group %s: %w", id, err)
	}
	return rec, nil
}

// CreateGroup creates a group row.
func (s *Store) CreateGroup(f extmsg.GroupFields) (extmsg.ConversationGroupRecord, error) {
	meta, err := encodeMeta(f.Meta)
	if err != nil {
		return extmsg.ConversationGroupRecord{}, err
	}
	var id string
	err = s.db.Write(context.Background(), func(tx *sql.Tx) error {
		id, err = mintID(tx)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(`INSERT INTO extmsg_groups
			(id, schema_version, scope_id, provider, account_id, conversation_id, parent_conversation_id, kind,
			 mode, default_handle, last_addressed_handle, fanout_enabled, fanout_allow_untargeted,
			 fanout_max_peer_triggered_publishes, fanout_max_total_peer_deliveries, status, meta)
			VALUES (?, 1, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'open', ?)`,
			append(append([]any{id}, refArgs(f.Ref)...),
				string(f.Mode), f.DefaultHandle, f.LastAddressedHandle,
				boolInt(f.Fanout.Enabled), boolInt(f.Fanout.AllowUntargetedPublication),
				f.Fanout.MaxPeerTriggeredPublishes, f.Fanout.MaxTotalPeerDeliveries, meta)...); err != nil {
			return fmt.Errorf("create group: %w", err)
		}
		return nil
	})
	if err != nil {
		return extmsg.ConversationGroupRecord{}, err
	}
	return s.RefetchGroup(id)
}

// UpdateGroup refreshes an existing group row. An empty LastAddressedHandle
// leaves the stored cursor untouched (the bd delete-from-fields semantics).
func (s *Store) UpdateGroup(id string, f extmsg.GroupFields) error {
	return s.db.Write(context.Background(), func(tx *sql.Tx) error {
		query := `UPDATE extmsg_groups SET mode = ?, default_handle = ?, fanout_enabled = ?,
			fanout_allow_untargeted = ?, fanout_max_peer_triggered_publishes = ?, fanout_max_total_peer_deliveries = ?`
		args := []any{
			string(f.Mode), f.DefaultHandle,
			boolInt(f.Fanout.Enabled), boolInt(f.Fanout.AllowUntargetedPublication),
			f.Fanout.MaxPeerTriggeredPublishes, f.Fanout.MaxTotalPeerDeliveries,
		}
		if f.LastAddressedHandle != "" {
			query += `, last_addressed_handle = ?`
			args = append(args, f.LastAddressedHandle)
		}
		query += ` WHERE id = ?`
		args = append(args, id)
		res, err := tx.Exec(query, args...)
		if err != nil {
			return fmt.Errorf("update group metadata: %w", err)
		}
		affected, err := res.RowsAffected()
		if err != nil {
			return err
		}
		if affected == 0 {
			return fmt.Errorf("update group metadata: %w", beads.ErrNotFound)
		}
		return mergeMetaLocked(tx, "extmsg_groups", id, f.Meta)
	})
}

// SetGroupCursor sets the group's last-addressed cursor.
func (s *Store) SetGroupCursor(id string, handle string) error {
	return s.writeExpectingRow(
		fmt.Sprintf("setting group %q cursor", id),
		`UPDATE extmsg_groups SET last_addressed_handle = ? WHERE id = ?`, handle, id,
	)
}

// --- participants ---

const participantCols = `id, group_id, handle, session_id, session_name, public, pending_cleanup, status, meta`

func scanParticipant(scan func(dest ...any) error) (extmsg.ParticipantRecord, error) {
	var (
		rec                   extmsg.ParticipantRecord
		public                int
		pending, status, meta string
	)
	if err := scan(&rec.ID, &rec.GroupID, &rec.Handle, &rec.SessionID, &rec.SessionName,
		&public, &pending, &status, &meta); err != nil {
		return extmsg.ParticipantRecord{}, err
	}
	rec.Public = public != 0
	rec.PendingCleanup = decodePending(pending)
	rec.Closed = status == "closed"
	metaMap, err := decodeMeta(meta)
	if err != nil {
		return extmsg.ParticipantRecord{}, err
	}
	rec.Metadata = metaMap
	return rec, nil
}

func (s *Store) listParticipants(query string, args ...any) ([]extmsg.ParticipantRecord, error) {
	rows, err := s.db.Read().Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("listing participants: %w", err)
	}
	defer rows.Close() //nolint:errcheck
	var out []extmsg.ParticipantRecord
	for rows.Next() {
		rec, err := scanParticipant(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// ParticipantsByGroup returns the group's participant rows, optionally
// including closed ones.
func (s *Store) ParticipantsByGroup(groupID string, includeClosed bool) ([]extmsg.ParticipantRecord, error) {
	query := `SELECT ` + participantCols + ` FROM extmsg_participants WHERE group_id = ?`
	if !includeClosed {
		query += ` AND status = 'open'`
	}
	return s.listParticipants(query+` ORDER BY id`, groupID)
}

// ParticipantsBySession returns the open participants discoverable by
// sessionID: those targeting it, plus those still owing it cleanup — the
// retained retired-session lookup handle of the bd model.
func (s *Store) ParticipantsBySession(sessionID string) ([]extmsg.ParticipantRecord, error) {
	return s.listParticipants(`SELECT `+participantCols+` FROM extmsg_participants
		WHERE status = 'open' AND (session_id = ?1 OR (',' || pending_cleanup || ',') LIKE ('%,' || ?1 || ',%'))
		ORDER BY id`, sessionID)
}

// OpenParticipants returns every open participant row (the reaper scan).
func (s *Store) OpenParticipants() ([]extmsg.ParticipantRecord, error) {
	return s.listParticipants(`SELECT ` + participantCols + ` FROM extmsg_participants WHERE status = 'open' ORDER BY id`)
}

// GetParticipant fetches one participant row for the repair paths; ok is
// false when id is not an open participant row.
func (s *Store) GetParticipant(id string) (extmsg.ParticipantRecord, bool, error) {
	row := s.db.Read().QueryRow(`SELECT `+participantCols+` FROM extmsg_participants WHERE id = ?`, id)
	rec, err := scanParticipant(row.Scan)
	if errors.Is(err, sql.ErrNoRows) {
		return extmsg.ParticipantRecord{}, false, nil
	}
	if err != nil {
		return extmsg.ParticipantRecord{}, false, fmt.Errorf("get participant %s: %w", id, err)
	}
	if rec.Closed {
		return extmsg.ParticipantRecord{}, false, nil
	}
	return rec, true, nil
}

// RefetchParticipant re-reads a participant row after an update.
func (s *Store) RefetchParticipant(id string) (extmsg.ConversationGroupParticipant, error) {
	row := s.db.Read().QueryRow(`SELECT `+participantCols+` FROM extmsg_participants WHERE id = ?`, id)
	rec, err := scanParticipant(row.Scan)
	if err != nil {
		return extmsg.ConversationGroupParticipant{}, fmt.Errorf("get participant %s: %w", id, err)
	}
	return rec.ConversationGroupParticipant, nil
}

// CreateParticipant creates a participant row.
func (s *Store) CreateParticipant(f extmsg.ParticipantFields) (extmsg.ConversationGroupParticipant, error) {
	meta, err := encodeMeta(f.Meta)
	if err != nil {
		return extmsg.ConversationGroupParticipant{}, err
	}
	var id string
	err = s.db.Write(context.Background(), func(tx *sql.Tx) error {
		id, err = mintID(tx)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(`INSERT INTO extmsg_participants
			(id, schema_version, group_id, handle, session_id, session_name, public, pending_cleanup, status, meta)
			VALUES (?, 1, ?, ?, ?, ?, ?, '', 'open', ?)`,
			id, f.GroupID, f.Handle, f.SessionID, f.SessionName, boolInt(f.Public), meta); err != nil {
			return fmt.Errorf("create group participant: %w", err)
		}
		return nil
	})
	if err != nil {
		return extmsg.ConversationGroupParticipant{}, err
	}
	return s.RefetchParticipant(id)
}

// RetargetParticipant moves an existing participant to a new session target
// and persists the pending-cleanup set (the upsert path — the old session
// lookup handle is dropped immediately, matching the bd label swap).
func (s *Store) RetargetParticipant(id string, f extmsg.ParticipantFields, _ string, _ string, pendingCleanup []string) error {
	return s.db.Write(context.Background(), func(tx *sql.Tx) error {
		res, err := tx.Exec(`UPDATE extmsg_participants
			SET handle = ?, session_id = ?, session_name = ?, public = ?, pending_cleanup = ?
			WHERE id = ?`,
			f.Handle, f.SessionID, f.SessionName, boolInt(f.Public), encodePending(pendingCleanup), id)
		if err != nil {
			return fmt.Errorf("update group participant: %w", err)
		}
		affected, err := res.RowsAffected()
		if err != nil {
			return err
		}
		if affected == 0 {
			return fmt.Errorf("update group participant: %w", beads.ErrNotFound)
		}
		return mergeMetaLocked(tx, "extmsg_participants", id, f.Meta)
	})
}

// ReassignParticipantSession points the participant at the replacement
// session and persists the pending-cleanup set. The retired session stays
// discoverable through pending_cleanup (see ParticipantsBySession) until the
// membership-migration writeback clears it.
func (s *Store) ReassignParticipantSession(id string, oldSessionID, newSessionID string, pendingCleanup []string) error {
	err := s.writeExpectingRow(
		fmt.Sprintf("reassigning participant %q", id),
		`UPDATE extmsg_participants SET session_id = ?, pending_cleanup = ? WHERE id = ?`,
		newSessionID, encodePending(pendingCleanup), id,
	)
	if err != nil {
		return fmt.Errorf("reassign participant %s from session %s to %s: %w", id, oldSessionID, newSessionID, err)
	}
	return nil
}

// DropParticipantSessionLabel completes a handover. On this backend the
// retired-session lookup handle IS the pending_cleanup entry, which the
// membership-migration writeback already cleared, so there is nothing left
// to drop.
func (s *Store) DropParticipantSessionLabel(string, string, string) error { return nil }

// CloseParticipant closes a participant row.
func (s *Store) CloseParticipant(id string) error {
	return s.writeExpectingRow(
		fmt.Sprintf("closing participant %q", id),
		`UPDATE extmsg_participants SET status = 'closed', closed_at = ? WHERE id = ? AND status = 'open'`,
		nanos(time.Now().UTC()), id,
	)
}

// SetParticipantPendingCleanup persists the participant's pending-cleanup
// session set.
func (s *Store) SetParticipantPendingCleanup(id string, sessionIDs []string) error {
	return s.writeExpectingRow(
		fmt.Sprintf("setting participant %q pending cleanup", id),
		`UPDATE extmsg_participants SET pending_cleanup = ? WHERE id = ?`, encodePending(sessionIDs), id,
	)
}

// --- transcript state ---

const stateCols = `id, schema_version, scope_id, provider, account_id, conversation_id, parent_conversation_id, kind,
	next_sequence, earliest_available_sequence, hydration_status, oldest_hydrated_message_id, max_retained_entries, meta`

func scanState(scan func(dest ...any) error) (extmsg.ConversationTranscriptStateRecord, error) {
	var (
		rec                            extmsg.ConversationTranscriptStateRecord
		scope, provider, account, conv string
		parent, kind, hydration, meta  string
	)
	if err := scan(&rec.ID, &rec.SchemaVersion, &scope, &provider, &account, &conv, &parent, &kind,
		&rec.NextSequence, &rec.EarliestAvailableSequence, &hydration,
		&rec.OldestHydratedMessageID, &rec.MaxRetainedEntries, &meta); err != nil {
		return extmsg.ConversationTranscriptStateRecord{}, err
	}
	rec.Conversation = scanRef(scope, provider, account, conv, parent, kind)
	rec.HydrationStatus = extmsg.HydrationStatus(hydration)
	metaMap, err := decodeMeta(meta)
	if err != nil {
		return extmsg.ConversationTranscriptStateRecord{}, err
	}
	rec.Metadata = metaMap
	return rec, nil
}

// OpenTranscriptStates returns the transcript-state rows for ref (at most
// one — schema-enforced).
func (s *Store) OpenTranscriptStates(ref extmsg.ConversationRef) ([]extmsg.ConversationTranscriptStateRecord, error) {
	rows, err := s.db.Read().Query(
		`SELECT `+stateCols+` FROM extmsg_transcript_state WHERE `+whereConv+` ORDER BY id`,
		refArgs(ref)...)
	if err != nil {
		return nil, fmt.Errorf("listing transcript state: %w", err)
	}
	defer rows.Close() //nolint:errcheck
	var out []extmsg.ConversationTranscriptStateRecord
	for rows.Next() {
		rec, err := scanState(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// RefetchTranscriptState re-reads a state row after an update.
func (s *Store) RefetchTranscriptState(id string) (extmsg.ConversationTranscriptStateRecord, error) {
	row := s.db.Read().QueryRow(`SELECT `+stateCols+` FROM extmsg_transcript_state WHERE id = ?`, id)
	rec, err := scanState(row.Scan)
	if err != nil {
		return extmsg.ConversationTranscriptStateRecord{}, fmt.Errorf("get state %s: %w", id, err)
	}
	return rec, nil
}

// PatchTranscriptState applies a tri-state field patch plus the
// user-metadata merge.
func (s *Store) PatchTranscriptState(id string, patch extmsg.StatePatch) error {
	return s.db.Write(context.Background(), func(tx *sql.Tx) error {
		sets := make([]string, 0, 3)
		args := make([]any, 0, 4)
		if patch.NextSequence != nil {
			sets = append(sets, "next_sequence = ?")
			args = append(args, *patch.NextSequence)
		}
		if patch.EarliestFloorOne {
			sets = append(sets, "earliest_available_sequence = 1")
		}
		if patch.Hydration != nil {
			sets = append(sets, "hydration_status = ?")
			args = append(args, string(*patch.Hydration))
		}
		if len(sets) > 0 {
			args = append(args, id)
			res, err := tx.Exec(`UPDATE extmsg_transcript_state SET `+strings.Join(sets, ", ")+` WHERE id = ?`, args...)
			if err != nil {
				return err
			}
			affected, err := res.RowsAffected()
			if err != nil {
				return err
			}
			if affected == 0 {
				return fmt.Errorf("patching transcript state %q: %w", id, beads.ErrNotFound)
			}
		}
		return mergeMetaLocked(tx, "extmsg_transcript_state", id, patch.Meta)
	})
}

// --- transcript entries ---

const entryCols = `id, schema_version, scope_id, provider, account_id, conversation_id, parent_conversation_id, kind,
	sequence, msg_kind, provenance, provider_message_id, explicit_target, reply_to_message_id,
	source_session_id, created_at, text, actor_json, attachments_json, meta`

func scanEntry(scan func(dest ...any) error) (extmsg.ConversationTranscriptRecord, error) {
	var (
		rec                               extmsg.ConversationTranscriptRecord
		scope, provider, account, conv    string
		parent, kind, msgKind, provenance string
		actorJSON, attachmentsJSON, meta  string
		created                           int64
	)
	if err := scan(&rec.ID, &rec.SchemaVersion, &scope, &provider, &account, &conv, &parent, &kind,
		&rec.Sequence, &msgKind, &provenance, &rec.ProviderMessageID, &rec.ExplicitTarget,
		&rec.ReplyToMessageID, &rec.SourceSessionID, &created, &rec.Text,
		&actorJSON, &attachmentsJSON, &meta); err != nil {
		return extmsg.ConversationTranscriptRecord{}, err
	}
	rec.Conversation = scanRef(scope, provider, account, conv, parent, kind)
	rec.Kind = extmsg.TranscriptMessageKind(msgKind)
	rec.Provenance = extmsg.TranscriptProvenance(provenance)
	rec.CreatedAt = timeFromNanos(created)
	if strings.TrimSpace(actorJSON) != "" {
		if err := json.Unmarshal([]byte(actorJSON), &rec.Actor); err != nil {
			return extmsg.ConversationTranscriptRecord{}, fmt.Errorf("decode actor_json: %w", err)
		}
	}
	if strings.TrimSpace(attachmentsJSON) != "" {
		if err := json.Unmarshal([]byte(attachmentsJSON), &rec.Attachments); err != nil {
			return extmsg.ConversationTranscriptRecord{}, fmt.Errorf("decode attachments_json: %w", err)
		}
	}
	metaMap, err := decodeMeta(meta)
	if err != nil {
		return extmsg.ConversationTranscriptRecord{}, err
	}
	rec.Metadata = metaMap
	return rec, nil
}

func (s *Store) listEntries(query string, args ...any) ([]extmsg.ConversationTranscriptRecord, error) {
	rows, err := s.db.Read().Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("listing transcript entries: %w", err)
	}
	defer rows.Close() //nolint:errcheck
	var out []extmsg.ConversationTranscriptRecord
	for rows.Next() {
		rec, err := scanEntry(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// OpenTranscriptsByProviderMessage returns the entries carrying the provider
// message id (at most one — schema-enforced).
func (s *Store) OpenTranscriptsByProviderMessage(ref extmsg.ConversationRef, providerMessageID string) ([]extmsg.ConversationTranscriptRecord, error) {
	return s.listEntries(
		`SELECT `+entryCols+` FROM extmsg_transcript_entries WHERE `+whereConv+` AND provider_message_id = ? ORDER BY id`,
		append(refArgs(ref), providerMessageID)...)
}

// AppendTranscript persists one entry and advances the sequence allocator in
// ONE transaction — closing the bd backend's create-then-bump crash window.
func (s *Store) AppendTranscript(entry extmsg.TranscriptEntryCreate, stateID string, nextSequence int64, setEarliestFloor bool) (extmsg.ConversationTranscriptRecord, error) {
	meta, err := encodeMeta(entry.Meta)
	if err != nil {
		return extmsg.ConversationTranscriptRecord{}, err
	}
	var id string
	err = s.db.Write(context.Background(), func(tx *sql.Tx) error {
		id, err = mintID(tx)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(`INSERT INTO extmsg_transcript_entries
			(id, schema_version, scope_id, provider, account_id, conversation_id, parent_conversation_id, kind,
			 sequence, msg_kind, provenance, provider_message_id, explicit_target, reply_to_message_id,
			 source_session_id, created_at, text, actor_json, attachments_json, meta)
			VALUES (?, 1, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			append(append([]any{id}, refArgs(entry.Ref)...),
				entry.Sequence, string(entry.Kind), string(entry.Provenance), entry.ProviderMessageID,
				entry.ExplicitTarget, entry.ReplyToMessageID, entry.SourceSessionID,
				nanos(entry.CreatedAt), entry.Text, entry.ActorJSON, entry.AttachmentsJSON, meta)...); err != nil {
			return fmt.Errorf("create transcript entry: %w", err)
		}
		updates := `next_sequence = ?`
		args := []any{nextSequence}
		if setEarliestFloor {
			updates += `, earliest_available_sequence = 1`
		}
		args = append(args, stateID)
		res, err := tx.Exec(`UPDATE extmsg_transcript_state SET `+updates+` WHERE id = ?`, args...)
		if err != nil {
			return fmt.Errorf("update transcript state: %w", err)
		}
		affected, err := res.RowsAffected()
		if err != nil {
			return err
		}
		if affected == 0 {
			return fmt.Errorf("update transcript state: %w", beads.ErrNotFound)
		}
		return nil
	})
	if err != nil {
		return extmsg.ConversationTranscriptRecord{}, err
	}
	row := s.db.Read().QueryRow(`SELECT `+entryCols+` FROM extmsg_transcript_entries WHERE id = ?`, id)
	rec, err := scanEntry(row.Scan)
	if err != nil {
		return extmsg.ConversationTranscriptRecord{}, fmt.Errorf("get transcript entry %s: %w", id, err)
	}
	return rec, nil
}

// ListTranscript returns entries with sequence above after, clamped to
// [startSeq, endSeq], ordered by sequence (id tiebreak), up to limit — the
// direct range scan that replaces the bd bucket walk.
func (s *Store) ListTranscript(ref extmsg.ConversationRef, after, startSeq, endSeq int64, limit int, descending bool) ([]extmsg.ConversationTranscriptRecord, error) {
	order := `sequence, id`
	if descending {
		order = `sequence DESC, id DESC`
	}
	return s.listEntries(
		`SELECT `+entryCols+` FROM extmsg_transcript_entries
		 WHERE `+whereConv+` AND sequence > ? AND sequence >= ? AND sequence <= ?
		 ORDER BY `+order+` LIMIT ?`,
		append(refArgs(ref), after, startSeq, endSeq, limit)...)
}

// --- memberships ---

const membershipCols = `id, schema_version, scope_id, provider, account_id, conversation_id, parent_conversation_id, kind,
	session_id, joined_at, joined_sequence, last_read_sequence, backfill_policy, manual_backfill_policy, owner_kinds, meta`

func scanMembership(scan func(dest ...any) error) (extmsg.ConversationMembershipRecord, error) {
	var (
		rec                            extmsg.ConversationMembershipRecord
		scope, provider, account, conv string
		parent, kind                   string
		backfill, manual, owners, meta string
		joined                         int64
	)
	if err := scan(&rec.ID, &rec.SchemaVersion, &scope, &provider, &account, &conv, &parent, &kind,
		&rec.SessionID, &joined, &rec.JoinedSequence, &rec.LastReadSequence,
		&backfill, &manual, &owners, &meta); err != nil {
		return extmsg.ConversationMembershipRecord{}, err
	}
	rec.Conversation = scanRef(scope, provider, account, conv, parent, kind)
	rec.JoinedAt = timeFromNanos(joined)
	rec.BackfillPolicy = extmsg.MembershipBackfillPolicy(backfill)
	rec.ManualBackfill = extmsg.MembershipBackfillPolicy(manual)
	rec.Owners = decodeOwners(owners)
	metaMap, err := decodeMeta(meta)
	if err != nil {
		return extmsg.ConversationMembershipRecord{}, err
	}
	rec.Metadata = metaMap
	return rec, nil
}

func (s *Store) listMemberships(query string, args ...any) ([]extmsg.ConversationMembershipRecord, error) {
	rows, err := s.db.Read().Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("listing memberships: %w", err)
	}
	defer rows.Close() //nolint:errcheck
	var out []extmsg.ConversationMembershipRecord
	for rows.Next() {
		rec, err := scanMembership(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// OpenMembershipsExact returns the open memberships for the exact
// (conversation, session) pair (at most one — schema-enforced).
func (s *Store) OpenMembershipsExact(ref extmsg.ConversationRef, sessionID string) ([]extmsg.ConversationMembershipRecord, error) {
	return s.listMemberships(
		`SELECT `+membershipCols+` FROM extmsg_memberships WHERE `+whereConv+` AND session_id = ? AND status = 'open' ORDER BY id`,
		append(refArgs(ref), sessionID)...)
}

// OpenMembershipsByConversation returns the conversation's open memberships.
func (s *Store) OpenMembershipsByConversation(ref extmsg.ConversationRef) ([]extmsg.ConversationMembershipRecord, error) {
	return s.listMemberships(
		`SELECT `+membershipCols+` FROM extmsg_memberships WHERE `+whereConv+` AND status = 'open' ORDER BY id`,
		refArgs(ref)...)
}

// OpenMembershipsBySession returns the session's open memberships.
func (s *Store) OpenMembershipsBySession(sessionID string) ([]extmsg.ConversationMembershipRecord, error) {
	return s.listMemberships(
		`SELECT `+membershipCols+` FROM extmsg_memberships WHERE session_id = ? AND status = 'open' ORDER BY id`,
		sessionID)
}

// RefetchMembership re-reads a membership row after an update.
func (s *Store) RefetchMembership(id string) (extmsg.ConversationMembershipRecord, error) {
	row := s.db.Read().QueryRow(`SELECT `+membershipCols+` FROM extmsg_memberships WHERE id = ?`, id)
	rec, err := scanMembership(row.Scan)
	if err != nil {
		return extmsg.ConversationMembershipRecord{}, fmt.Errorf("get membership %s: %w", id, err)
	}
	return rec, nil
}

// CloseMembership stamps the closed clock and closes the membership row.
func (s *Store) CloseMembership(id string, closedAt time.Time) error {
	return s.writeExpectingRow(
		fmt.Sprintf("closing membership %q", id),
		`UPDATE extmsg_memberships SET status = 'closed', closed_at = ? WHERE id = ? AND status = 'open'`,
		nanos(closedAt), id,
	)
}

// SetMembershipLastRead advances the membership's read cursor.
func (s *Store) SetMembershipLastRead(id string, sequence int64) error {
	return s.writeExpectingRow(
		fmt.Sprintf("acking membership %q", id),
		`UPDATE extmsg_memberships SET last_read_sequence = ? WHERE id = ?`, sequence, id,
	)
}
