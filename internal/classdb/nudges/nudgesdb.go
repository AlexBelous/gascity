// Package nudgesdb is the embedded-SQLite nudges-class store
// (engdocs/design/infra-class-sqlite-stores.md, Nudges section): ONE nudges
// table over the shared internal/classdb/core substrate replaces both tiers
// of the legacy queue — the flock'd state.json buckets AND the shadow beads.
// The row IS the record: enqueue is one INSERT, claim is a single
// transaction over the queue-key set, terminal transitions are row updates,
// and the design's queue_state vocabulary (pending | in_flight | dead |
// terminal) subsumes the bucket model plus the shadow's terminal stamps.
//
// It implements the internal/nudgequeue queueBackend structurally; wire it
// via nudgequeue.NewQueueWithBackend. Shadow-bead parameters on the backend
// surface are ignored here by design. Semantics preserved, not "fixed"
// (design ratification): a superseded in-flight delivery may still complete
// once (its ack finds the row already terminal and no-ops), and dead-letter
// stamping is never rolled back — in the merged model it is simply atomic
// with the dead transition.
package nudgesdb

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/classdb/core"
	"github.com/gastownhall/gascity/internal/nudgequeue"
)

// queue_state values (the design's CHECK enum).
const (
	statePending  = "pending"
	stateInFlight = "in_flight"
	stateDead     = "dead"
	stateTerminal = "terminal"
)

// migrations is the version-gated nudges schema, per the design's Nudges
// section. bead_id is a transition column absent from the design sketch: it
// preserves Item.BeadID for rows imported from the legacy queue so the Item
// projection stays lossless during cutover; fresh rows leave it empty.
func migrations() []core.Migration {
	return []core.Migration{{
		Version: 1,
		DDL: []string{
			`CREATE TABLE IF NOT EXISTS nudges (
				id                 TEXT PRIMARY KEY,
				bead_id            TEXT NOT NULL DEFAULT '',
				agent              TEXT NOT NULL,
				session_id         TEXT NOT NULL DEFAULT '',
				continuation_epoch TEXT NOT NULL DEFAULT '',
				source             TEXT NOT NULL,
				message            TEXT NOT NULL,
				ref_kind           TEXT NOT NULL DEFAULT '',
				ref_id             TEXT NOT NULL DEFAULT '',
				created_at         INTEGER NOT NULL,
				deliver_after      INTEGER NOT NULL,
				expires_at         INTEGER NOT NULL,
				attempts           INTEGER NOT NULL DEFAULT 0,
				last_attempt_at    INTEGER NOT NULL DEFAULT 0,
				last_error         TEXT NOT NULL DEFAULT '',
				claimed_at         INTEGER NOT NULL DEFAULT 0,
				lease_until        INTEGER NOT NULL DEFAULT 0,
				dead_at            INTEGER NOT NULL DEFAULT 0,
				queue_state        TEXT NOT NULL CHECK(queue_state IN ('pending','in_flight','dead','terminal')),
				terminal_state     TEXT NOT NULL DEFAULT '',
				terminal_reason    TEXT NOT NULL DEFAULT '',
				commit_boundary    TEXT NOT NULL DEFAULT '',
				terminal_at        INTEGER NOT NULL DEFAULT 0
			)`,
			`CREATE INDEX IF NOT EXISTS idx_nudges_claim ON nudges(queue_state, agent, deliver_after)`,
			`CREATE INDEX IF NOT EXISTS idx_nudges_lease ON nudges(queue_state, lease_until)`,
			`CREATE INDEX IF NOT EXISTS idx_nudges_ref ON nudges(agent, source, ref_kind, ref_id) WHERE queue_state IN ('pending','in_flight')`,
			`CREATE INDEX IF NOT EXISTS idx_nudges_retention ON nudges(queue_state, terminal_at)`,
			`CREATE INDEX IF NOT EXISTS idx_nudges_expiry ON nudges(queue_state, expires_at)`,
		},
	}}
}

// Store is the embedded-SQLite nudges-class queue backend.
type Store struct {
	db *core.DB
}

// Open opens (creating and migrating if needed) the nudges store file at
// path. Long-lived callers use the default read pool; short-lived one-shots
// pass core.WithSingleConn.
func Open(path string, opts ...core.Option) (*Store, error) {
	db, err := core.Open(path, migrations(), opts...)
	if err != nil {
		return nil, fmt.Errorf("opening nudges store: %w", err)
	}
	return &Store{db: db}, nil
}

// Close closes the underlying database handles. Idempotent.
func (s *Store) Close() error { return s.db.Close() }

// Path returns the database file path.
func (s *Store) Path() string { return s.db.Path() }

func nanos(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixNano()
}

func fromNanos(n int64) time.Time {
	if n == 0 {
		return time.Time{}
	}
	return time.Unix(0, n).UTC()
}

const itemColumns = `id, bead_id, agent, session_id, continuation_epoch, source, message,
	ref_kind, ref_id, created_at, deliver_after, expires_at, attempts,
	last_attempt_at, last_error, claimed_at, lease_until, dead_at`

func scanItem(scan func(dest ...any) error) (nudgequeue.Item, error) {
	var (
		item                                        nudgequeue.Item
		refKind, refID                              string
		created, deliverAfter, expires, lastAttempt int64
		claimed, lease, dead                        int64
	)
	if err := scan(&item.ID, &item.BeadID, &item.Agent, &item.SessionID, &item.ContinuationEpoch,
		&item.Source, &item.Message, &refKind, &refID, &created, &deliverAfter, &expires,
		&item.Attempts, &lastAttempt, &item.LastError, &claimed, &lease, &dead); err != nil {
		return nudgequeue.Item{}, err
	}
	if refKind != "" || refID != "" {
		item.Reference = &nudgequeue.Reference{Kind: refKind, ID: refID}
	}
	item.CreatedAt = fromNanos(created)
	item.DeliverAfter = fromNanos(deliverAfter)
	item.ExpiresAt = fromNanos(expires)
	item.LastAttemptAt = fromNanos(lastAttempt)
	item.ClaimedAt = fromNanos(claimed)
	item.LeaseUntil = fromNanos(lease)
	item.DeadAt = fromNanos(dead)
	return item, nil
}

func insertItem(tx *sql.Tx, item nudgequeue.Item, queueState string) error {
	refKind, refID := "", ""
	if item.Reference != nil {
		refKind, refID = item.Reference.Kind, item.Reference.ID
	}
	_, err := tx.Exec(
		`INSERT OR IGNORE INTO nudges (id, bead_id, agent, session_id, continuation_epoch, source, message,
			ref_kind, ref_id, created_at, deliver_after, expires_at, attempts, last_attempt_at,
			last_error, claimed_at, lease_until, dead_at, queue_state)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		item.ID, item.BeadID, item.Agent, item.SessionID, item.ContinuationEpoch, item.Source, item.Message,
		refKind, refID, nanos(item.CreatedAt), nanos(item.DeliverAfter), nanos(item.ExpiresAt),
		item.Attempts, nanos(item.LastAttemptAt), item.LastError, nanos(item.ClaimedAt),
		nanos(item.LeaseUntil), nanos(item.DeadAt), queueState,
	)
	return err
}

// maintain runs the maintenance transitions as set-based statements inside
// tx: expired pending/in-flight items dead-letter (terminal fields stamped
// atomically — the merged-model form of the file backend's best-effort
// shadow write), lease-expired claims return to pending for immediate
// redelivery, and dead rows past DeadRetention age into terminal (the row is
// its own durable terminal record, so no shadow confirmation is needed; the
// retention sweeper deletes terminal rows later).
func maintain(tx *sql.Tx, now time.Time) error {
	nowN := nanos(now)
	if _, err := tx.Exec(
		`UPDATE nudges SET queue_state = ?, dead_at = ?,
			last_error = CASE WHEN last_error = '' THEN 'expired' ELSE last_error END,
			terminal_state = 'expired', terminal_reason = CASE WHEN last_error = '' THEN 'expired' ELSE last_error END,
			terminal_at = ?, claimed_at = 0, lease_until = 0
		 WHERE queue_state IN (?, ?) AND expires_at > 0 AND expires_at <= ?`,
		stateDead, nowN, nowN, statePending, stateInFlight, nowN,
	); err != nil {
		return fmt.Errorf("expiring nudges: %w", err)
	}
	if _, err := tx.Exec(
		`UPDATE nudges SET queue_state = ?, claimed_at = 0, lease_until = 0, deliver_after = ?
		 WHERE queue_state = ? AND lease_until <= ?`,
		statePending, nowN, stateInFlight, nowN,
	); err != nil {
		return fmt.Errorf("recovering expired nudge leases: %w", err)
	}
	if _, err := tx.Exec(
		`UPDATE nudges SET queue_state = ?, terminal_at = CASE WHEN terminal_at = 0 THEN dead_at ELSE terminal_at END,
			terminal_state = CASE WHEN terminal_state = '' THEN
				CASE TRIM(last_error) WHEN 'expired' THEN 'expired' WHEN 'superseded' THEN 'superseded' ELSE 'failed' END
			ELSE terminal_state END,
			terminal_reason = CASE WHEN terminal_reason = '' THEN
				CASE WHEN TRIM(last_error) = '' THEN 'failed' ELSE last_error END
			ELSE terminal_reason END
		 WHERE queue_state = ? AND dead_at > 0 AND dead_at < ?`,
		stateTerminal, stateDead, nanos(now.Add(-nudgequeue.DeadRetention)),
	); err != nil {
		return fmt.Errorf("retiring dead nudges: %w", err)
	}
	return nil
}

// Enqueue inserts item as pending, superseding queued items for the same
// (agent, source, reference) in the same transaction. The shadow store is
// ignored: the row is the record, so there is nothing to save or roll back.
func (s *Store) Enqueue(item nudgequeue.Item, _ beads.NudgesStore) error {
	now := time.Now()
	return s.db.Write(context.Background(), func(tx *sql.Tx) error {
		if err := maintain(tx, now); err != nil {
			return err
		}
		var exists int
		if err := tx.QueryRow(`SELECT COUNT(*) FROM nudges WHERE id = ?`, item.ID).Scan(&exists); err != nil {
			return err
		}
		if exists > 0 {
			return nil
		}
		if item.Reference != nil && item.Reference.ID != "" {
			nowN := nanos(now)
			if _, err := tx.Exec(
				`UPDATE nudges SET queue_state = ?, dead_at = ?, last_error = 'superseded',
					terminal_state = 'superseded', terminal_reason = 'superseded', terminal_at = ?,
					claimed_at = 0, lease_until = 0
				 WHERE queue_state IN (?, ?) AND agent = ? AND source = ? AND ref_kind = ? AND ref_id = ?`,
				stateDead, nowN, nowN, statePending, stateInFlight,
				item.Agent, item.Source, item.Reference.Kind, item.Reference.ID,
			); err != nil {
				return fmt.Errorf("superseding nudges: %w", err)
			}
		}
		return insertItem(tx, item, statePending)
	})
}

// EnqueueDeferred inserts item as pending with no maintenance and no
// supersession — the deferred-submit shape.
func (s *Store) EnqueueDeferred(item nudgequeue.Item) error {
	return s.db.Write(context.Background(), func(tx *sql.Tx) error {
		return insertItem(tx, item, statePending)
	})
}

// ClaimDue claims due pending items for target inside one immediate write
// transaction: maintenance first, then a deterministic select (the pending
// bucket order: deliver_after, created_at, id) and the in-flight transition.
// Serialization comes from the transaction itself, exactly as the flock
// serialized the file backend.
func (s *Store) ClaimDue(target nudgequeue.ClaimTarget, now time.Time) ([]nudgequeue.Item, error) {
	if len(target.QueueKeys) == 0 {
		return nil, nil
	}
	var claimed []nudgequeue.Item
	err := s.db.Write(context.Background(), func(tx *sql.Tx) error {
		claimed = nil
		if err := maintain(tx, now); err != nil {
			return err
		}
		keyArgs := make([]any, 0, len(target.QueueKeys)+4)
		keyArgs = append(keyArgs, statePending, nanos(now))
		placeholders := make([]string, 0, len(target.QueueKeys))
		for _, key := range target.QueueKeys {
			placeholders = append(placeholders, "?")
			keyArgs = append(keyArgs, key)
		}
		// The fence claim gate (ClaimTarget.Claimable) as SQL: a session-fenced
		// item requires the matching session id; an epoch-fenced unfenced-session
		// item requires the target to carry an epoch at all.
		keyArgs = append(keyArgs, target.SessionID, target.SessionID, boolInt(target.ContinuationEpoch != ""))
		rows, err := tx.Query(
			`SELECT `+itemColumns+` FROM nudges
			 WHERE queue_state = ? AND deliver_after <= ? AND agent IN (`+strings.Join(placeholders, ", ")+`)
			   AND ((session_id <> '' AND ? <> '' AND session_id = ?)
			     OR (session_id = '' AND (continuation_epoch = '' OR ? = 1)))
			 ORDER BY deliver_after, created_at, id`,
			keyArgs...,
		)
		if err != nil {
			return err
		}
		defer rows.Close() //nolint:errcheck
		for rows.Next() {
			item, err := scanItem(rows.Scan)
			if err != nil {
				return err
			}
			item.ClaimedAt = now.UTC()
			item.LeaseUntil = now.Add(nudgequeue.ClaimLeaseTTL).UTC()
			claimed = append(claimed, item)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		for _, item := range claimed {
			if _, err := tx.Exec(
				`UPDATE nudges SET queue_state = ?, claimed_at = ?, lease_until = ? WHERE id = ?`,
				stateInFlight, nanos(item.ClaimedAt), nanos(item.LeaseUntil), item.ID,
			); err != nil {
				return err
			}
		}
		return nil
	})
	return claimed, err
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// ListForAgent returns the pending/in-flight/dead items addressed exactly to
// agentName, after maintenance.
func (s *Store) ListForAgent(agentName string, now time.Time) (pending, inFlight, dead []nudgequeue.Item, err error) {
	return s.list(now, `agent = ?`, agentName)
}

// ListFor returns the pending/in-flight/dead items matching any of target's
// queue keys, after maintenance.
func (s *Store) ListFor(target nudgequeue.ClaimTarget, now time.Time) (pending, inFlight, dead []nudgequeue.Item, err error) {
	if len(target.QueueKeys) == 0 {
		return nil, nil, nil, nil
	}
	placeholders := make([]string, len(target.QueueKeys))
	args := make([]any, len(target.QueueKeys))
	for i, key := range target.QueueKeys {
		placeholders[i] = "?"
		args[i] = key
	}
	return s.list(now, `agent IN (`+strings.Join(placeholders, ", ")+`)`, args...)
}

func (s *Store) list(now time.Time, where string, args ...any) (pending, inFlight, dead []nudgequeue.Item, err error) {
	err = s.db.Write(context.Background(), func(tx *sql.Tx) error {
		pending, inFlight, dead = nil, nil, nil
		if err := maintain(tx, now); err != nil {
			return err
		}
		for _, bucket := range []struct {
			state string
			out   *[]nudgequeue.Item
			order string
		}{
			{statePending, &pending, `deliver_after, created_at, id`},
			{stateInFlight, &inFlight, `lease_until, claimed_at, id`},
			{stateDead, &dead, `dead_at, created_at, id`},
		} {
			rows, err := tx.Query(
				`SELECT `+itemColumns+` FROM nudges WHERE queue_state = '`+bucket.state+`' AND `+where+` ORDER BY `+bucket.order,
				args...,
			)
			if err != nil {
				return err
			}
			for rows.Next() {
				item, err := scanItem(rows.Scan)
				if err != nil {
					rows.Close() //nolint:errcheck,gosec // error path
					return err
				}
				*bucket.out = append(*bucket.out, item)
			}
			if err := rows.Err(); err != nil {
				rows.Close() //nolint:errcheck,gosec // error path
				return err
			}
			if err := rows.Close(); err != nil {
				return err
			}
		}
		return nil
	})
	return pending, inFlight, dead, err
}

// Snapshot projects the live rows back onto the bucket model (terminal rows
// are history and stay invisible, matching the file backend where acked
// items left the queue).
func (s *Store) Snapshot() (nudgequeue.State, error) {
	var state nudgequeue.State
	rows, err := s.db.Read().Query(
		`SELECT ` + itemColumns + `, queue_state FROM nudges WHERE queue_state IN ('pending','in_flight','dead')`,
	)
	if err != nil {
		return nudgequeue.State{}, fmt.Errorf("reading nudge queue: %w", err)
	}
	defer rows.Close() //nolint:errcheck
	for rows.Next() {
		var queueState string
		item, err := scanItem(func(dest ...any) error {
			return rows.Scan(append(dest, &queueState)...)
		})
		if err != nil {
			return nudgequeue.State{}, err
		}
		switch queueState {
		case statePending:
			state.Pending = append(state.Pending, item)
		case stateInFlight:
			state.InFlight = append(state.InFlight, item)
		case stateDead:
			state.Dead = append(state.Dead, item)
		}
	}
	if err := rows.Err(); err != nil {
		return nudgequeue.State{}, err
	}
	nudgequeue.SortState(&state)
	return state, nil
}

// Ack transitions delivered ids to terminal with the outcome vocabulary the
// shadow bead used to carry. Already-terminal and unknown ids no-op.
func (s *Store) Ack(ids []string, outcome, reason, commitBoundary string) error {
	if len(ids) == 0 {
		return nil
	}
	now := time.Now()
	return s.db.Write(context.Background(), func(tx *sql.Tx) error {
		if err := maintain(tx, now); err != nil {
			return err
		}
		args := make([]any, 0, len(ids)+6)
		args = append(args, stateTerminal, outcome, reason, commitBoundary, nanos(now))
		placeholders := make([]string, 0, len(ids))
		for _, id := range ids {
			placeholders = append(placeholders, "?")
			args = append(args, id)
		}
		args = append(args, statePending, stateInFlight)
		_, err := tx.Exec(
			`UPDATE nudges SET queue_state = ?, terminal_state = ?, terminal_reason = ?,
				commit_boundary = ?, terminal_at = ?, claimed_at = 0, lease_until = 0
			 WHERE id IN (`+strings.Join(placeholders, ", ")+`) AND queue_state IN (?, ?)`,
			args...,
		)
		return err
	})
}

// ReleaseClaims returns undelivered in-flight ids to pending.
func (s *Store) ReleaseClaims(ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	now := time.Now()
	return s.db.Write(context.Background(), func(tx *sql.Tx) error {
		if err := maintain(tx, now); err != nil {
			return err
		}
		args := make([]any, 0, len(ids)+2)
		args = append(args, statePending)
		placeholders := make([]string, 0, len(ids))
		for _, id := range ids {
			placeholders = append(placeholders, "?")
			args = append(args, id)
		}
		args = append(args, stateInFlight)
		_, err := tx.Exec(
			`UPDATE nudges SET queue_state = ?, claimed_at = 0, lease_until = 0
			 WHERE id IN (`+strings.Join(placeholders, ", ")+`) AND queue_state = ?`,
			args...,
		)
		return err
	})
}

// RecordFailure applies the retry policy (nudgequeue.FailedItem — the shared
// vocabulary) to ids. Dead-lettered rows get their terminal fields stamped
// in the same transaction: the file backend's outside-the-lock best-effort
// shadow write collapses into the atomic dead transition, which the design
// ratifies (the stamping must never roll the transition back — here it
// cannot). warn is unused: there is no secondary record to fail.
func (s *Store) RecordFailure(ids []string, _ beads.NudgesStore, cause error, now time.Time, _ io.Writer) ([]nudgequeue.Item, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	want := make(map[string]bool, len(ids))
	placeholders := make([]string, 0, len(ids))
	args := make([]any, 0, len(ids)+2)
	args = append(args, statePending, stateInFlight)
	for _, id := range ids {
		want[id] = true
		placeholders = append(placeholders, "?")
		args = append(args, id)
	}
	var deadLettered []nudgequeue.Item
	err := s.db.Write(context.Background(), func(tx *sql.Tx) error {
		deadLettered = nil
		if err := maintain(tx, now); err != nil {
			return err
		}
		rows, err := tx.Query(
			`SELECT `+itemColumns+` FROM nudges WHERE queue_state IN (?, ?) AND id IN (`+strings.Join(placeholders, ", ")+`)`,
			args...,
		)
		if err != nil {
			return err
		}
		var matched []nudgequeue.Item
		for rows.Next() {
			item, err := scanItem(rows.Scan)
			if err != nil {
				rows.Close() //nolint:errcheck,gosec // error path
				return err
			}
			matched = append(matched, item)
		}
		if err := rows.Err(); err != nil {
			rows.Close() //nolint:errcheck,gosec // error path
			return err
		}
		if err := rows.Close(); err != nil {
			return err
		}
		for _, item := range matched {
			updated, deadLetter := nudgequeue.FailedItem(item, cause, now)
			if deadLetter {
				deadLettered = append(deadLettered, updated)
				if _, err := tx.Exec(
					`UPDATE nudges SET queue_state = ?, attempts = ?, last_attempt_at = ?, last_error = ?,
						claimed_at = 0, lease_until = 0, dead_at = ?,
						terminal_state = 'failed', terminal_reason = ?, terminal_at = ?
					 WHERE id = ?`,
					stateDead, updated.Attempts, nanos(updated.LastAttemptAt), updated.LastError,
					nanos(updated.DeadAt), updated.LastError, nanos(updated.DeadAt), updated.ID,
				); err != nil {
					return err
				}
				continue
			}
			if _, err := tx.Exec(
				`UPDATE nudges SET queue_state = ?, attempts = ?, last_attempt_at = ?, last_error = ?,
					claimed_at = 0, lease_until = 0, deliver_after = ?
				 WHERE id = ?`,
				statePending, updated.Attempts, nanos(updated.LastAttemptAt), updated.LastError,
				nanos(updated.DeliverAfter), updated.ID,
			); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return deadLettered, nil
}

// Rollback dead-letters a queued item as failed with reason; when the item
// is not queued (already delivered or never landed), a terminal record row
// is written so the failure stays observable, mirroring the file backend's
// shadow-only fallback terminalization.
func (s *Store) Rollback(_ *nudgequeue.Store, item nudgequeue.Item, reason string) error {
	if item.ID == "" {
		return nil
	}
	now := time.Now()
	return s.db.Write(context.Background(), func(tx *sql.Tx) error {
		nowN := nanos(now)
		res, err := tx.Exec(
			`UPDATE nudges SET queue_state = ?, dead_at = ?, last_error = ?,
				terminal_state = 'failed', terminal_reason = ?, terminal_at = ?, claimed_at = 0, lease_until = 0
			 WHERE id = ? AND queue_state IN (?, ?)`,
			stateDead, nowN, reason, reason, nowN, item.ID, statePending, stateInFlight,
		)
		if err != nil {
			return err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return err
		}
		if n > 0 {
			return nil
		}
		fallback := item
		fallback.LastError = reason
		fallback.DeadAt = now.UTC()
		if err := insertItem(tx, fallback, stateTerminal); err != nil {
			return err
		}
		_, err = tx.Exec(
			`UPDATE nudges SET terminal_state = 'failed', terminal_reason = ?, terminal_at = ? WHERE id = ? AND terminal_state = ''`,
			reason, nowN, item.ID,
		)
		return err
	})
}

// WithdrawWaitNudges transitions still-queued wait nudges to terminal
// wait-canceled (the beads store is ignored — the row is the record).
func (s *Store) WithdrawWaitNudges(_ beads.Store, ids []string) error {
	unique := make([]string, 0, len(ids))
	seen := make(map[string]bool, len(ids))
	for _, id := range ids {
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		unique = append(unique, id)
	}
	if len(unique) == 0 {
		return nil
	}
	now := time.Now()
	return s.db.Write(context.Background(), func(tx *sql.Tx) error {
		args := make([]any, 0, len(unique)+2)
		args = append(args, stateTerminal, nanos(now))
		placeholders := make([]string, 0, len(unique))
		for _, id := range unique {
			placeholders = append(placeholders, "?")
			args = append(args, id)
		}
		args = append(args, statePending, stateInFlight)
		_, err := tx.Exec(
			`UPDATE nudges SET queue_state = ?, terminal_state = 'failed', terminal_reason = 'wait-canceled',
				commit_boundary = 'delivery-withdrawn', terminal_at = ?, claimed_at = 0, lease_until = 0
			 WHERE id IN (`+strings.Join(placeholders, ", ")+`) AND queue_state IN (?, ?)`,
			args...,
		)
		return err
	})
}

// TerminalRecord is the merged-model replacement for the shadow-bead
// Find/FindIncludingTerminal reads the wait paths consume: the queue-record
// view of one nudge id.
type TerminalRecord struct {
	// Item is the row's Item projection.
	Item nudgequeue.Item
	// QueueState is the row's queue_state.
	QueueState string
	// TerminalState / TerminalReason / CommitBoundary / TerminalAt are the
	// terminal stamps (zero values while the item is live).
	TerminalState  string
	TerminalReason string
	CommitBoundary string
	TerminalAt     time.Time
}

// FindRecord returns the LIVE (pending/in-flight/dead) record for id.
func (s *Store) FindRecord(id string) (TerminalRecord, bool, error) {
	return s.findRecord(id, false)
}

// FindRecordIncludingTerminal returns the record for id including terminal
// rows.
func (s *Store) FindRecordIncludingTerminal(id string) (TerminalRecord, bool, error) {
	return s.findRecord(id, true)
}

func (s *Store) findRecord(id string, includeTerminal bool) (TerminalRecord, bool, error) {
	if id == "" {
		return TerminalRecord{}, false, nil
	}
	where := `id = ? AND queue_state IN ('pending','in_flight','dead')`
	if includeTerminal {
		where = `id = ?`
	}
	row := s.db.Read().QueryRow(
		`SELECT `+itemColumns+`, queue_state, terminal_state, terminal_reason, commit_boundary, terminal_at
		 FROM nudges WHERE `+where, id,
	)
	var rec TerminalRecord
	var terminalAt int64
	item, err := scanItem(func(dest ...any) error {
		return row.Scan(append(dest, &rec.QueueState, &rec.TerminalState, &rec.TerminalReason, &rec.CommitBoundary, &terminalAt)...)
	})
	if errors.Is(err, sql.ErrNoRows) {
		return TerminalRecord{}, false, nil
	}
	if err != nil {
		return TerminalRecord{}, false, fmt.Errorf("finding nudge %q: %w", id, err)
	}
	rec.Item = item
	rec.TerminalAt = fromNanos(terminalAt)
	return rec, true, nil
}

// CountRetention reports how many rows SweepRetention would delete at now
// with ttl, without writing anything: terminal rows past the ttl, plus dead
// rows that maintenance would age into terminal (dead past DeadRetention)
// whose inherited terminal clock also lands past the ttl. It is the dry-run
// twin of SweepRetention.
func (s *Store) CountRetention(now time.Time, ttl time.Duration) (int, error) {
	cutoff := nanos(now.Add(-ttl))
	aged := nanos(now.Add(-nudgequeue.DeadRetention))
	var count int
	err := s.db.Read().QueryRow(
		`SELECT COUNT(*) FROM nudges
		 WHERE (queue_state = ? AND terminal_at > 0 AND terminal_at < ?)
		    OR (queue_state = ? AND dead_at > 0 AND dead_at < ?
		        AND (CASE WHEN terminal_at > 0 THEN terminal_at ELSE dead_at END) < ?)`,
		stateTerminal, cutoff, stateDead, aged, cutoff,
	).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("counting nudge retention candidates: %w", err)
	}
	return count, nil
}

// SweepRetention deletes terminal rows past ttl (the design's terminal-row
// retention; dead rows age into terminal via maintain first). Returns the
// number of rows deleted.
func (s *Store) SweepRetention(ctx context.Context, now time.Time, ttl time.Duration) (int, error) {
	deleted := 0
	err := s.db.Write(ctx, func(tx *sql.Tx) error {
		if err := maintain(tx, now); err != nil {
			return err
		}
		res, err := tx.Exec(
			`DELETE FROM nudges WHERE queue_state = ? AND terminal_at > 0 AND terminal_at < ?`,
			stateTerminal, nanos(now.Add(-ttl)),
		)
		if err != nil {
			return err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return err
		}
		deleted = int(n)
		return nil
	})
	return deleted, err
}
