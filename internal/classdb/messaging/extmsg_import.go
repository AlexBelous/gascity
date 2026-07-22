package messagingdb

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/gastownhall/gascity/internal/extmsg"
)

// Import primitives for the messaging-class migration: each inserts one
// migrated legacy record verbatim, preserving its id (legacy bd prefixes
// stay valid row keys), clocks, generations, and lifecycle state. INSERT OR
// IGNORE keeps re-imports idempotent, so an interrupted migration simply
// resumes; an id that already exists is left untouched.

func importGuard(kind, id string) error {
	if strings.TrimSpace(id) == "" {
		return fmt.Errorf("importing %s: empty id", kind)
	}
	return nil
}

// ImportBinding imports one binding record. Ended bindings are importable
// too — the migration carries, per conversation, at least the max-generation
// row so generation minting and delivery gating stay monotonic across the
// cutover; lastTouchedAt supplies the touched clock (and the ended clock for
// ended rows).
func (s *Store) ImportBinding(rec extmsg.SessionBindingRecord, lastTouchedAt time.Time) error {
	if err := importGuard("binding", rec.ID); err != nil {
		return err
	}
	meta, err := encodeMeta(rec.Metadata)
	if err != nil {
		return err
	}
	status := "active"
	var endedAt any
	if rec.Status == extmsg.BindingEnded {
		status = "ended"
		endedAt = nanos(lastTouchedAt)
	}
	return s.db.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT OR IGNORE INTO extmsg_bindings
			(id, schema_version, scope_id, provider, account_id, conversation_id, parent_conversation_id, kind,
			 session_id, session_name, agent_name, generation, bound_at, expires_at, last_touched_at,
			 created_by_kind, created_by_id, status, ended_at, meta)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, '', '', ?, ?, ?)`,
			append(append([]any{rec.ID, rec.SchemaVersion}, refArgs(rec.Conversation)...),
				rec.SessionID, rec.SessionName, rec.AgentName, rec.BindingGeneration,
				nanos(rec.BoundAt), nullableNanos(rec.ExpiresAt), nanos(lastTouchedAt),
				status, endedAt, meta)...)
		return err
	})
}

// ImportDeliveryContext imports one open delivery context.
func (s *Store) ImportDeliveryContext(rec extmsg.DeliveryContextRecord) error {
	if err := importGuard("delivery context", rec.ID); err != nil {
		return err
	}
	meta, err := encodeMeta(rec.Metadata)
	if err != nil {
		return err
	}
	return s.db.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT OR IGNORE INTO extmsg_delivery_contexts
			(id, schema_version, scope_id, provider, account_id, conversation_id, parent_conversation_id, kind,
			 session_id, generation, last_published_at, last_message_id, source_session_id, status, meta)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'open', ?)`,
			append(append([]any{rec.ID, rec.SchemaVersion}, refArgs(rec.Conversation)...),
				rec.SessionID, rec.BindingGeneration, nanos(rec.LastPublishedAt),
				rec.LastMessageID, rec.SourceSessionID, meta)...)
		return err
	})
}

// ImportGroup imports one open group.
func (s *Store) ImportGroup(rec extmsg.ConversationGroupRecord) error {
	if err := importGuard("group", rec.ID); err != nil {
		return err
	}
	meta, err := encodeMeta(rec.Metadata)
	if err != nil {
		return err
	}
	return s.db.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT OR IGNORE INTO extmsg_groups
			(id, schema_version, scope_id, provider, account_id, conversation_id, parent_conversation_id, kind,
			 mode, default_handle, last_addressed_handle, fanout_enabled, fanout_allow_untargeted,
			 fanout_max_peer_triggered_publishes, fanout_max_total_peer_deliveries, status, meta)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'open', ?)`,
			append(append([]any{rec.ID, rec.SchemaVersion}, refArgs(rec.RootConversation)...),
				string(rec.Mode), rec.DefaultHandle, rec.LastAddressedHandle,
				boolInt(rec.FanoutPolicy.Enabled), boolInt(rec.FanoutPolicy.AllowUntargetedPublication),
				rec.FanoutPolicy.MaxPeerTriggeredPublishes, rec.FanoutPolicy.MaxTotalPeerDeliveries, meta)...)
		return err
	})
}

// ImportParticipant imports one open participant, including its
// pending-cleanup set (an in-flight handover resumes on the new backend).
func (s *Store) ImportParticipant(rec extmsg.ParticipantRecord) error {
	if err := importGuard("participant", rec.ID); err != nil {
		return err
	}
	meta, err := encodeMeta(rec.Metadata)
	if err != nil {
		return err
	}
	return s.db.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT OR IGNORE INTO extmsg_participants
			(id, schema_version, group_id, handle, session_id, session_name, public, pending_cleanup, status, meta)
			VALUES (?, 1, ?, ?, ?, ?, ?, ?, 'open', ?)`,
			rec.ID, rec.GroupID, rec.Handle, rec.SessionID, rec.SessionName,
			boolInt(rec.Public), encodePending(rec.PendingCleanup), meta)
		return err
	})
}

// ImportMembership imports one open membership.
func (s *Store) ImportMembership(rec extmsg.ConversationMembershipRecord) error {
	if err := importGuard("membership", rec.ID); err != nil {
		return err
	}
	meta, err := encodeMeta(rec.Metadata)
	if err != nil {
		return err
	}
	return s.db.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT OR IGNORE INTO extmsg_memberships
			(id, schema_version, scope_id, provider, account_id, conversation_id, parent_conversation_id, kind,
			 session_id, joined_at, joined_sequence, last_read_sequence, backfill_policy, manual_backfill_policy, owner_kinds, meta)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			append(append([]any{rec.ID, rec.SchemaVersion}, refArgs(rec.Conversation)...),
				rec.SessionID, nanos(rec.JoinedAt), rec.JoinedSequence, rec.LastReadSequence,
				string(rec.BackfillPolicy), string(rec.ManualBackfill), encodeOwners(rec.Owners), meta)...)
		return err
	})
}

// ImportTranscriptState imports one transcript-state record.
func (s *Store) ImportTranscriptState(rec extmsg.ConversationTranscriptStateRecord) error {
	if err := importGuard("transcript state", rec.ID); err != nil {
		return err
	}
	meta, err := encodeMeta(rec.Metadata)
	if err != nil {
		return err
	}
	return s.db.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT OR IGNORE INTO extmsg_transcript_state
			(id, schema_version, scope_id, provider, account_id, conversation_id, parent_conversation_id, kind,
			 next_sequence, earliest_available_sequence, hydration_status, oldest_hydrated_message_id, max_retained_entries, meta)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			append(append([]any{rec.ID, rec.SchemaVersion}, refArgs(rec.Conversation)...),
				rec.NextSequence, rec.EarliestAvailableSequence, string(rec.HydrationStatus),
				rec.OldestHydratedMessageID, rec.MaxRetainedEntries, meta)...)
		return err
	})
}

// ImportTranscriptEntry imports one transcript entry, re-serializing the
// actor/attachment payloads at the edge.
func (s *Store) ImportTranscriptEntry(rec extmsg.ConversationTranscriptRecord) error {
	if err := importGuard("transcript entry", rec.ID); err != nil {
		return err
	}
	meta, err := encodeMeta(rec.Metadata)
	if err != nil {
		return err
	}
	actorJSON := ""
	if rec.Actor != (extmsg.ExternalActor{}) {
		data, err := json.Marshal(rec.Actor)
		if err != nil {
			return fmt.Errorf("importing transcript entry %q: encoding actor: %w", rec.ID, err)
		}
		actorJSON = string(data)
	}
	attachmentsJSON := ""
	if len(rec.Attachments) > 0 {
		data, err := json.Marshal(rec.Attachments)
		if err != nil {
			return fmt.Errorf("importing transcript entry %q: encoding attachments: %w", rec.ID, err)
		}
		attachmentsJSON = string(data)
	}
	return s.db.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT OR IGNORE INTO extmsg_transcript_entries
			(id, schema_version, scope_id, provider, account_id, conversation_id, parent_conversation_id, kind,
			 sequence, msg_kind, provenance, provider_message_id, explicit_target, reply_to_message_id,
			 source_session_id, created_at, text, actor_json, attachments_json, meta)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			append(append([]any{rec.ID, rec.SchemaVersion}, refArgs(rec.Conversation)...),
				rec.Sequence, string(rec.Kind), string(rec.Provenance), rec.ProviderMessageID,
				rec.ExplicitTarget, rec.ReplyToMessageID, rec.SourceSessionID,
				nanos(rec.CreatedAt), rec.Text, actorJSON, attachmentsJSON, meta)...)
		return err
	})
}

// SweepExtmsgRetention deletes terminal extmsg rows older than cutoff: ended
// bindings, closed delivery contexts, closed participants, and closed
// memberships. Per conversation, the binding row carrying the highest
// (generation, id) always survives — deleting it could re-mint a colliding
// binding_generation that a stale delivery context falsely matches, so the
// sweep preserves the generation ceiling (the orders "newest run carries the
// max seq" precedent). Dormant until the messaging class flips; the store's
// retention sweeper drives it then. Returns the number of rows deleted.
func (s *Store) SweepExtmsgRetention(cutoff time.Time) (int, error) {
	deleted := 0
	err := s.db.Write(context.Background(), func(tx *sql.Tx) error {
		statements := []struct {
			query string
			args  []any
		}{
			{
				`DELETE FROM extmsg_bindings AS b
				WHERE b.status = 'ended' AND b.ended_at IS NOT NULL AND b.ended_at < ?
				AND EXISTS (SELECT 1 FROM extmsg_bindings x
					WHERE x.scope_id = b.scope_id AND x.provider = b.provider AND x.account_id = b.account_id
					AND x.conversation_id = b.conversation_id AND x.parent_conversation_id = b.parent_conversation_id
					AND x.kind = b.kind
					AND (x.generation > b.generation OR (x.generation = b.generation AND x.id > b.id)))`,
				[]any{nanos(cutoff)},
			},
			{`DELETE FROM extmsg_delivery_contexts WHERE status = 'closed' AND closed_at IS NOT NULL AND closed_at < ?`, []any{nanos(cutoff)}},
			{`DELETE FROM extmsg_participants WHERE status = 'closed' AND closed_at IS NOT NULL AND closed_at < ?`, []any{nanos(cutoff)}},
			{`DELETE FROM extmsg_memberships WHERE status = 'closed' AND closed_at IS NOT NULL AND closed_at < ?`, []any{nanos(cutoff)}},
		}
		for _, stmt := range statements {
			res, err := tx.Exec(stmt.query, stmt.args...)
			if err != nil {
				return err
			}
			n, err := res.RowsAffected()
			if err != nil {
				return err
			}
			deleted += int(n)
		}
		return nil
	})
	if err != nil {
		return 0, fmt.Errorf("sweeping extmsg retention: %w", err)
	}
	return deleted, nil
}

// PruneTranscripts enforces per-conversation transcript retention: each
// conversation keeps its newest entries — a state row's max_retained_entries
// when > 0, else defaultMaxRetained (<= 0 disables pruning entirely) — and
// earliest_available_sequence advances past what was pruned, which the list
// and backfill reads already clamp to. This makes the design's
// max_retained_entries knob real (it was written as 0 and never enforced on
// the bd backend). Dormant until the flip. Returns the number of entries
// deleted.
func (s *Store) PruneTranscripts(defaultMaxRetained int) (int, error) {
	states, err := s.allTranscriptStates()
	if err != nil {
		return 0, err
	}
	deleted := 0
	for _, state := range states {
		keep := defaultMaxRetained
		if state.MaxRetainedEntries > 0 {
			keep = state.MaxRetainedEntries
		}
		if keep <= 0 {
			continue
		}
		// Entries with sequence <= cutoffSeq fall outside the retained
		// window [head-keep+1, head].
		cutoffSeq := state.NextSequence - 1 - int64(keep)
		if cutoffSeq < state.EarliestAvailableSequence {
			continue
		}
		err := s.db.Write(context.Background(), func(tx *sql.Tx) error {
			res, err := tx.Exec(
				`DELETE FROM extmsg_transcript_entries WHERE `+whereConv+` AND sequence <= ?`,
				append(refArgs(state.Conversation), cutoffSeq)...)
			if err != nil {
				return err
			}
			n, err := res.RowsAffected()
			if err != nil {
				return err
			}
			deleted += int(n)
			_, err = tx.Exec(
				`UPDATE extmsg_transcript_state SET earliest_available_sequence = ? WHERE id = ? AND earliest_available_sequence < ?`,
				cutoffSeq+1, state.ID, cutoffSeq+1)
			return err
		})
		if err != nil {
			return deleted, fmt.Errorf("pruning transcripts for %s: %w", state.ID, err)
		}
	}
	return deleted, nil
}

func (s *Store) allTranscriptStates() ([]extmsg.ConversationTranscriptStateRecord, error) {
	rows, err := s.db.Read().Query(`SELECT ` + stateCols + ` FROM extmsg_transcript_state ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("listing transcript states: %w", err)
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
