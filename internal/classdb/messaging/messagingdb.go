// Package messagingdb is the embedded-SQLite messaging-class store
// (engdocs/design/infra-class-sqlite-stores.md, Messaging section): the
// design's messages table over the shared internal/classdb/core substrate
// replaces the ephemeral message wisp beads. The row IS the message: the
// read column subsumes the label/metadata reconciliation, status +
// close_reason carry the 6b0eb0d6b retention-swept-vs-user-removed
// distinction (closed-with-the-retention-reason stays addressable;
// user-removed is row-deleted), and the two handoff flags become columns.
//
// It implements internal/mail/beadmail's messagesBackend structurally; wire
// it via beadmail.NewWithBackend. Per the seam plan, this store lands DARK:
// the extmsg half of the messaging class gets its typed tables in this same
// database file before any routing flips — the class relocates atomically.
package messagingdb

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/classdb/core"
	"github.com/gastownhall/gascity/internal/mail"
	"github.com/gastownhall/gascity/internal/mail/beadmail"
)

// idPrefix is the reserved messaging-class id prefix. It must stay in
// lockstep with config.ReservedClassPrefix(config.BeadClassMessaging) —
// pinned by TestIDPrefixMatchesReservedClassPrefix so the constant never
// drifts from the config registry without importing internal/config here.
const idPrefix = "gcm"

// migrations is the version-gated messaging-class schema, per the design's
// Messaging section: Version 1 is the mail messages table, Version 2 the
// extmsg typed tables (extmsgdb.go) — the second half of the class, in the
// SAME file so the class relocates atomically. id_seq is the shared id mint
// counter, advanced inside the same transaction as each INSERT so minted ids
// are unique across concurrent processes.
func migrations() []core.Migration {
	return []core.Migration{{
		Version: 1,
		DDL: []string{
			`CREATE TABLE IF NOT EXISTS messages (
				id                   TEXT PRIMARY KEY,
				thread_id            TEXT NOT NULL,
				reply_to_id          TEXT NOT NULL DEFAULT '',
				from_addr            TEXT NOT NULL,
				to_addr              TEXT NOT NULL,
				from_session_id      TEXT NOT NULL DEFAULT '',
				from_display         TEXT NOT NULL DEFAULT '',
				to_session_id        TEXT NOT NULL DEFAULT '',
				to_display           TEXT NOT NULL DEFAULT '',
				subject              TEXT NOT NULL,
				body                 TEXT NOT NULL,
				created_at           INTEGER NOT NULL,
				read                 INTEGER NOT NULL DEFAULT 0,
				status               TEXT NOT NULL DEFAULT 'open' CHECK(status IN ('open','closed')),
				close_reason         TEXT NOT NULL DEFAULT '',
				auto_handoff         INTEGER NOT NULL DEFAULT 0,
				archive_after_inject INTEGER NOT NULL DEFAULT 0
			)`,
			`CREATE INDEX IF NOT EXISTS idx_msg_inbox ON messages(to_addr, status, read)`,
			`CREATE INDEX IF NOT EXISTS idx_msg_thread ON messages(thread_id, created_at, id)`,
			`CREATE INDEX IF NOT EXISTS idx_msg_sweep ON messages(read, status, created_at)`,
			`CREATE TABLE IF NOT EXISTS id_seq (k INTEGER PRIMARY KEY CHECK (k = 1), next INTEGER NOT NULL)`,
			`INSERT OR IGNORE INTO id_seq (k, next) VALUES (1, 0)`,
		},
	}, {
		Version: 2,
		DDL:     extmsgDDL(),
	}}
}

// Store is the embedded-SQLite messaging-class messages backend.
type Store struct {
	db *core.DB
}

// Open opens (creating and migrating if needed) the messaging store file at
// path. Long-lived callers use the default read pool; short-lived one-shots
// pass core.WithSingleConn.
func Open(path string, opts ...core.Option) (*Store, error) {
	db, err := core.Open(path, migrations(), opts...)
	if err != nil {
		return nil, fmt.Errorf("opening messaging store: %w", err)
	}
	return &Store{db: db}, nil
}

// Close closes the underlying database handles. Idempotent.
func (s *Store) Close() error { return s.db.Close() }

// Path returns the database file path.
func (s *Store) Path() string { return s.db.Path() }

// IDPrefix returns the reserved messaging-class id prefix ("gcm"). Minted
// ids are "gcm-<seq>"; legacy-prefixed ids (migrated message beads) remain
// valid row keys.
func (s *Store) IDPrefix() string { return idPrefix }

func nanos(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixNano()
}

const recordColumns = `id, thread_id, reply_to_id, from_addr, to_addr,
	from_session_id, from_display, to_session_id, to_display,
	subject, body, created_at, read, status, close_reason,
	auto_handoff, archive_after_inject`

func scanRecord(scan func(dest ...any) error) (beadmail.Record, error) {
	var (
		rec              beadmail.Record
		created          int64
		readInt          int
		status           string
		autoInt, archInt int
	)
	if err := scan(&rec.ID, &rec.ThreadID, &rec.ReplyToID, &rec.FromAddr, &rec.ToAddr,
		&rec.FromSessionID, &rec.FromDisplay, &rec.ToSessionID, &rec.ToDisplay,
		&rec.Subject, &rec.Body, &created, &readInt, &status, &rec.CloseReason,
		&autoInt, &archInt); err != nil {
		return beadmail.Record{}, err
	}
	rec.CreatedAt = time.Unix(0, created).UTC()
	rec.Read = readInt != 0
	rec.ReadLabel = rec.Read
	rec.Open = status == "open"
	rec.AutoHandoff = autoInt != 0
	rec.ArchiveAfterInject = archInt != 0
	return rec, nil
}

// Create mints the next gcm id and inserts one open unread row in a single
// transaction — the acked-send durability contract. The two known handoff
// flag labels map to their columns; other extra labels are a bd-backend
// concept and are dropped (the design schema carries no free-form labels).
func (s *Store) Create(msg beadmail.NewMessage) (beadmail.Record, error) {
	autoHandoff, archiveAfterInject := 0, 0
	for _, label := range msg.ExtraLabels {
		switch label {
		case mail.AutoHandoffLabel:
			autoHandoff = 1
		case mail.ArchiveAfterInjectLabel:
			archiveAfterInject = 1
		}
	}
	created := time.Now().UTC()
	var id string
	err := s.db.Write(context.Background(), func(tx *sql.Tx) error {
		var next int64
		if err := tx.QueryRow(`UPDATE id_seq SET next = next + 1 WHERE k = 1 RETURNING next`).Scan(&next); err != nil {
			return fmt.Errorf("minting id: %w", err)
		}
		id = fmt.Sprintf("%s-%d", idPrefix, next)
		_, err := tx.Exec(
			`INSERT INTO messages (id, thread_id, reply_to_id, from_addr, to_addr,
				from_session_id, from_display, to_session_id, to_display,
				subject, body, created_at, read, status, close_reason, auto_handoff, archive_after_inject)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 0, 'open', '', ?, ?)`,
			id, msg.ThreadID, msg.ReplyToID, msg.From, msg.To,
			msg.FromSessionID, msg.FromDisplay, msg.ToSessionID, msg.ToDisplay,
			msg.Subject, msg.Body, nanos(created), autoHandoff, archiveAfterInject,
		)
		return err
	})
	if err != nil {
		return beadmail.Record{}, fmt.Errorf("creating message: %w", err)
	}
	return beadmail.Record{
		ID:                 id,
		ThreadID:           msg.ThreadID,
		ReplyToID:          msg.ReplyToID,
		FromAddr:           msg.From,
		ToAddr:             msg.To,
		FromSessionID:      msg.FromSessionID,
		FromDisplay:        msg.FromDisplay,
		ToSessionID:        msg.ToSessionID,
		ToDisplay:          msg.ToDisplay,
		Subject:            msg.Subject,
		Body:               msg.Body,
		CreatedAt:          created,
		Open:               true,
		AutoHandoff:        autoHandoff != 0,
		ArchiveAfterInject: archiveAfterInject != 0,
	}, nil
}

// Get returns the record for id; a deleted row is simply not found (the
// user-removed shape). NotAMessageError never occurs here — foreign classes
// are invisible to this store.
func (s *Store) Get(id string) (beadmail.Record, bool, error) {
	if id == "" {
		return beadmail.Record{}, false, nil
	}
	row := s.db.Read().QueryRow(`SELECT `+recordColumns+` FROM messages WHERE id = ?`, id)
	rec, err := scanRecord(row.Scan)
	if errors.Is(err, sql.ErrNoRows) {
		return beadmail.Record{}, false, nil
	}
	if err != nil {
		return beadmail.Record{}, false, fmt.Errorf("getting message %q: %w", id, err)
	}
	return rec, true, nil
}

// SetRead flips the read column.
func (s *Store) SetRead(id string, read bool) error {
	readInt := 0
	if read {
		readInt = 1
	}
	return s.writeExpectingRow(
		fmt.Sprintf("marking message %q read=%v", id, read),
		`UPDATE messages SET read = ? WHERE id = ?`, readInt, id,
	)
}

// Delete removes the message row outright (the eager archive / the
// user-removed shape). A missing row reports beads.ErrNotFound, matching
// the bd backend so the Provider's already-archived mapping holds.
func (s *Store) Delete(id string) error {
	return s.writeExpectingRow(
		fmt.Sprintf("deleting message %q", id),
		`DELETE FROM messages WHERE id = ?`, id,
	)
}

// writeExpectingRow runs one row-targeted statement and maps zero affected
// rows to beads.ErrNotFound.
func (s *Store) writeExpectingRow(op, query string, args ...any) error {
	var affected int64
	err := s.db.Write(context.Background(), func(tx *sql.Tx) error {
		res, err := tx.Exec(query, args...)
		if err != nil {
			return err
		}
		affected, err = res.RowsAffected()
		return err
	})
	if err != nil {
		return fmt.Errorf("%s: %w", op, err)
	}
	if affected == 0 {
		return fmt.Errorf("%s: %w", op, beads.ErrNotFound)
	}
	return nil
}

// ListOpenForRecipients returns open messages addressed to any of routes
// (nil/empty routes = every open message), excluding read messages unless
// includeRead, oldest first with an id tie-break (the bd store's canonical
// created-ascending ordering).
func (s *Store) ListOpenForRecipients(routes []string, includeRead bool) ([]beadmail.Record, error) {
	where := `status = 'open'`
	args := make([]any, 0, len(routes))
	if len(routes) > 0 {
		placeholders := make([]string, len(routes))
		for i, route := range routes {
			placeholders[i] = "?"
			args = append(args, route)
		}
		where += ` AND to_addr IN (` + strings.Join(placeholders, ", ") + `)`
	}
	if !includeRead {
		where += ` AND read = 0`
	}
	return s.listRecords(`SELECT `+recordColumns+` FROM messages WHERE `+where+` ORDER BY created_at, id`, args...)
}

// ListThread returns the open messages carrying threadID, oldest first.
func (s *Store) ListThread(threadID string) ([]beadmail.Record, error) {
	return s.listRecords(
		`SELECT `+recordColumns+` FROM messages WHERE thread_id = ? AND status = 'open' ORDER BY created_at, id`,
		threadID,
	)
}

// CountOpenForRecipients returns total and unread counts over the open
// messages addressed to any of routes — one native aggregate (the design's
// "native counts", replacing the bd scan-and-tally).
func (s *Store) CountOpenForRecipients(routes []string) (total, unread int, err error) {
	where := `status = 'open'`
	args := make([]any, 0, len(routes))
	if len(routes) > 0 {
		placeholders := make([]string, len(routes))
		for i, route := range routes {
			placeholders[i] = "?"
			args = append(args, route)
		}
		where += ` AND to_addr IN (` + strings.Join(placeholders, ", ") + `)`
	}
	row := s.db.Read().QueryRow(
		`SELECT COUNT(*), COALESCE(SUM(CASE WHEN read = 0 THEN 1 ELSE 0 END), 0) FROM messages WHERE `+where,
		args...,
	)
	if err := row.Scan(&total, &unread); err != nil {
		return 0, 0, fmt.Errorf("counting messages: %w", err)
	}
	return total, unread, nil
}

// ListReadCreatedBefore returns open read messages created before the
// cutoff, oldest first (limit 0 = unbounded) — the retention candidate read.
func (s *Store) ListReadCreatedBefore(before time.Time, limit int) ([]beadmail.Record, error) {
	query := `SELECT ` + recordColumns + ` FROM messages
		WHERE read = 1 AND status = 'open' AND created_at < ? ORDER BY created_at, id`
	args := []any{nanos(before)}
	if limit > 0 {
		query += ` LIMIT ?`
		args = append(args, limit)
	}
	return s.listRecords(query, args...)
}

// CloseReadWithReason stamps reason and closes the message — the retention
// sweep's transition, reported with the sweep's established per-message
// error vocabulary. A close with beadmail.RetentionSweepCloseReason keeps
// the row addressable through the Provider's direct-ID gate until the purge
// deletes it.
func (s *Store) CloseReadWithReason(id, reason string) error {
	var affected int64
	err := s.db.Write(context.Background(), func(tx *sql.Tx) error {
		res, err := tx.Exec(`UPDATE messages SET status = 'closed', close_reason = ? WHERE id = ?`, reason, id)
		if err != nil {
			return err
		}
		affected, err = res.RowsAffected()
		return err
	})
	if err != nil {
		return fmt.Errorf("mail %s: close: %w", id, err)
	}
	if affected == 0 {
		return fmt.Errorf("mail %s: close: %w", id, beads.ErrNotFound)
	}
	return nil
}

// PurgeReadCreatedBefore deletes read messages (open or closed) created
// before the cutoff — the consumed-mail purge. Returns the number of rows
// deleted.
func (s *Store) PurgeReadCreatedBefore(cutoff time.Time) (int, error) {
	return s.deleteCounting(
		"purging read messages",
		`DELETE FROM messages WHERE read = 1 AND created_at < ?`, nanos(cutoff),
	)
}

// SweepUnreadBefore deletes UNREAD messages created before the cutoff — the
// design's net-new 30d unread TTL (today unread bd mail leaks forever).
// Dormant until the messaging class flips; the store's retention sweeper
// drives it then. Returns the number of rows deleted.
func (s *Store) SweepUnreadBefore(cutoff time.Time) (int, error) {
	return s.deleteCounting(
		"sweeping unread messages",
		`DELETE FROM messages WHERE read = 0 AND created_at < ?`, nanos(cutoff),
	)
}

func (s *Store) deleteCounting(op, query string, args ...any) (int, error) {
	deleted := 0
	err := s.db.Write(context.Background(), func(tx *sql.Tx) error {
		res, err := tx.Exec(query, args...)
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
	if err != nil {
		return 0, fmt.Errorf("%s: %w", op, err)
	}
	return deleted, nil
}

// ImportMessage inserts one migrated legacy message verbatim, preserving its
// id (legacy bd prefixes stay valid row keys), clocks, read state, status,
// close_reason, and flags. INSERT OR IGNORE keeps re-imports idempotent, so
// an interrupted migration simply resumes; an id that already exists is
// left untouched.
func (s *Store) ImportMessage(rec beadmail.Record) error {
	if strings.TrimSpace(rec.ID) == "" {
		return fmt.Errorf("importing message: empty id")
	}
	if rec.CreatedAt.IsZero() {
		return fmt.Errorf("importing message %q: zero CreatedAt", rec.ID)
	}
	status := "closed"
	if rec.Open {
		status = "open"
	}
	return s.db.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec(
			`INSERT OR IGNORE INTO messages (id, thread_id, reply_to_id, from_addr, to_addr,
				from_session_id, from_display, to_session_id, to_display,
				subject, body, created_at, read, status, close_reason, auto_handoff, archive_after_inject)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			rec.ID, rec.ThreadID, rec.ReplyToID, rec.FromAddr, rec.ToAddr,
			rec.FromSessionID, rec.FromDisplay, rec.ToSessionID, rec.ToDisplay,
			rec.Subject, rec.Body, nanos(rec.CreatedAt), boolInt(rec.Read), status, rec.CloseReason,
			boolInt(rec.AutoHandoff), boolInt(rec.ArchiveAfterInject),
		)
		return err
	})
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func (s *Store) listRecords(query string, args ...any) ([]beadmail.Record, error) {
	rows, err := s.db.Read().Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("listing messages: %w", err)
	}
	defer rows.Close() //nolint:errcheck
	var out []beadmail.Record
	for rows.Next() {
		rec, err := scanRecord(rows.Scan)
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
