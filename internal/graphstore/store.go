package graphstore

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"net/url"
	"time"

	"github.com/gastownhall/gascity/internal/graphstore/canon"
	_ "modernc.org/sqlite" // Pure-Go SQLite driver; keeps graphstore CGO-free.
)

var (
	// ErrStreamExists reports a second attempt to create a stream that already
	// has its immutable genesis record.
	ErrStreamExists = errors.New("graphstore: stream already exists")

	// ErrStreamNotFound reports a lease request for a stream without genesis.
	ErrStreamNotFound = errors.New("graphstore: stream not found")

	// ErrLeaseHeld reports an attempt to acquire an unexpired writer lease held
	// by another holder.
	ErrLeaseHeld = errors.New("graphstore: writer lease is held")

	// ErrLeaseFenced reports an append attempted with an expired, superseded, or
	// otherwise non-current writer lease.
	ErrLeaseFenced = errors.New("graphstore: writer lease is fenced")

	// ErrCorruptJournal reports persisted data that fails the journal's dense
	// sequence or canonical payload-hash integrity checks.
	ErrCorruptJournal = errors.New("graphstore: corrupt journal")
)

// StreamAddress is an opaque address for one independently ordered journal.
type StreamAddress struct {
	value string
}

// NewStreamAddress validates and returns a stream address.
func NewStreamAddress(value string) (StreamAddress, error) {
	if value == "" {
		return StreamAddress{}, fmt.Errorf("graphstore: stream address is required")
	}
	return StreamAddress{value: value}, nil
}

// Options controls only the SQLite behavior required by this journal.
type Options struct {
	// Clock supplies lease time. A nil Clock uses the wall clock.
	Clock func() time.Time
}

// Store is the SQLite-WAL append-only journal for the Lumen engine.
type Store struct {
	db     *sql.DB
	readDB *sql.DB
	now    func() time.Time
}

// Open opens or creates a minimal SQLite-WAL journal at path.
func Open(ctx context.Context, path string, options Options) (*Store, error) {
	if path == "" {
		return nil, fmt.Errorf("graphstore: open: path is required")
	}
	now := options.Clock
	if now == nil {
		now = time.Now
	}
	db, err := sql.Open("sqlite", sqliteDSN(path, true))
	if err != nil {
		return nil, fmt.Errorf("graphstore: open %q: %w", path, err)
	}
	db.SetMaxOpenConns(1)
	readDB, err := sql.Open("sqlite", sqliteDSN(path, false))
	if err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("graphstore: open read handle %q: %w", path, err)
	}
	readDB.SetMaxOpenConns(4)
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		_ = readDB.Close()
		return nil, fmt.Errorf("graphstore: connect %q: %w", path, err)
	}
	if err := readDB.PingContext(ctx); err != nil {
		_ = db.Close()
		_ = readDB.Close()
		return nil, fmt.Errorf("graphstore: connect read handle %q: %w", path, err)
	}
	if err := migrate(ctx, db); err != nil {
		_ = db.Close()
		_ = readDB.Close()
		return nil, err
	}
	return &Store{db: db, readDB: readDB, now: now}, nil
}

func sqliteDSN(path string, writer bool) string {
	query := url.Values{}
	query.Add("_pragma", "journal_mode(WAL)")
	query.Add("_pragma", "foreign_keys(ON)")
	query.Add("_pragma", "synchronous(FULL)")
	if writer {
		query.Set("_txlock", "immediate")
	}
	return (&url.URL{Scheme: "file", Path: path, RawQuery: query.Encode()}).String()
}

// Close closes the journal database.
func (s *Store) Close() error {
	if err := errors.Join(s.db.Close(), s.readDB.Close()); err != nil {
		return fmt.Errorf("graphstore: close: %w", err)
	}
	return nil
}

// Create atomically creates stream with its sequence-one genesis record.
func (s *Store) Create(ctx context.Context, stream StreamAddress, genesis Record) (Cursor, error) {
	if err := requireStream(stream); err != nil {
		return Cursor{}, err
	}
	genesis, err := validateRecord(genesis)
	if err != nil {
		return Cursor{}, fmt.Errorf("graphstore: create: %w", err)
	}
	if genesis.Sequence() != 1 {
		return Cursor{}, fmt.Errorf("graphstore: create genesis sequence %d: %w", genesis.Sequence(), ErrIncompletePrefix)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Cursor{}, fmt.Errorf("graphstore: create: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	head, err := streamHead(ctx, tx, stream)
	if err != nil {
		return Cursor{}, err
	}
	if head != 0 {
		return Cursor{}, fmt.Errorf("graphstore: create %q: %w", stream.value, ErrStreamExists)
	}
	hash := canon.Hash(genesis.payload)
	result, err := tx.ExecContext(ctx,
		`INSERT INTO journal(stream, sequence, typ, payload, payload_hash)
		 VALUES (?, ?, ?, ?, ?) ON CONFLICT(stream, sequence) DO NOTHING`,
		stream.value, genesis.Sequence(), genesis.Type(), genesis.payload, hash[:])
	if err != nil {
		return Cursor{}, fmt.Errorf("graphstore: create %q: %w", stream.value, err)
	}
	inserted, err := result.RowsAffected()
	if err != nil {
		return Cursor{}, fmt.Errorf("graphstore: create %q: check insert: %w", stream.value, err)
	}
	if inserted != 1 {
		return Cursor{}, fmt.Errorf("graphstore: create %q: %w", stream.value, ErrStreamExists)
	}
	if err := tx.Commit(); err != nil {
		return Cursor{}, fmt.Errorf("graphstore: create %q: commit: %w", stream.value, err)
	}
	return CursorAt(1), nil
}

// Append atomically appends the dense records immediately after expected. It
// requires an exact, active writer lease for stream.
func (s *Store) Append(ctx context.Context, stream StreamAddress, expected Cursor, lease WriterLease, records []Record) (Cursor, error) {
	if err := requireStream(stream); err != nil {
		return Cursor{}, err
	}
	validated, err := validateAppendRecords(expected, records)
	if err != nil {
		return Cursor{}, fmt.Errorf("graphstore: append: %w", err)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Cursor{}, fmt.Errorf("graphstore: append: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	head, err := streamHead(ctx, tx, stream)
	if err != nil {
		return Cursor{}, err
	}
	if head != expected.Sequence() {
		return Cursor{}, fmt.Errorf("graphstore: append expected %d, found %d: %w", expected.Sequence(), head, ErrCursorMismatch)
	}
	if err := s.requireLease(ctx, tx, stream, lease); err != nil {
		return Cursor{}, err
	}
	for _, record := range validated {
		if err := insertRecord(ctx, tx, stream, record); err != nil {
			return Cursor{}, fmt.Errorf("graphstore: append sequence %d: %w", record.Sequence(), err)
		}
	}
	if err := tx.Commit(); err != nil {
		return Cursor{}, fmt.Errorf("graphstore: append: commit: %w", err)
	}
	return CursorAt(validated[len(validated)-1].Sequence()), nil
}

// Read returns the complete committed prefix after cursor from one SQLite read
// transaction. It rejects gaps and noncanonical or hash-mismatched payloads.
func (s *Store) Read(ctx context.Context, stream StreamAddress, cursor Cursor) (CompletePrefix, error) {
	if err := requireStream(stream); err != nil {
		return CompletePrefix{}, err
	}
	tx, err := s.readDB.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return CompletePrefix{}, fmt.Errorf("graphstore: read: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	head, err := streamHead(ctx, tx, stream)
	if err != nil {
		return CompletePrefix{}, err
	}
	if cursor.Sequence() > head {
		return CompletePrefix{}, fmt.Errorf("graphstore: read after %d, head %d: %w", cursor.Sequence(), head, ErrCursorMismatch)
	}
	rows, err := tx.QueryContext(ctx,
		`SELECT sequence, typ, payload, payload_hash FROM journal WHERE stream = ? AND sequence > ? ORDER BY sequence`,
		stream.value, cursor.Sequence())
	if err != nil {
		return CompletePrefix{}, fmt.Errorf("graphstore: read %q: %w", stream.value, err)
	}
	defer func() { _ = rows.Close() }()
	records := make([]Record, 0)
	for rows.Next() {
		var sequence uint64
		var typ string
		var payload, payloadHash []byte
		if err := rows.Scan(&sequence, &typ, &payload, &payloadHash); err != nil {
			return CompletePrefix{}, fmt.Errorf("graphstore: read %q: %w", stream.value, err)
		}
		record, err := NewRecord(sequence, typ, payload)
		if err != nil || !bytes.Equal(payload, record.Payload()) || len(payloadHash) != 32 || canon.Hash(payload) != bytesToHash(payloadHash) {
			return CompletePrefix{}, fmt.Errorf("graphstore: read sequence %d: %w", sequence, ErrCorruptJournal)
		}
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		return CompletePrefix{}, fmt.Errorf("graphstore: read %q: %w", stream.value, err)
	}
	prefix, err := NewCompletePrefix(cursor, CursorAt(head), records)
	if err != nil {
		return CompletePrefix{}, fmt.Errorf("graphstore: read %q: %w (%w)", stream.value, ErrCorruptJournal, err)
	}
	if err := tx.Commit(); err != nil {
		return CompletePrefix{}, fmt.Errorf("graphstore: read %q: commit: %w", stream.value, err)
	}
	return prefix, nil
}

func validateAppendRecords(expected Cursor, records []Record) ([]Record, error) {
	if len(records) == 0 {
		return nil, fmt.Errorf("no records")
	}
	validated := make([]Record, len(records))
	next := expected.Sequence()
	for index, record := range records {
		if next == math.MaxUint64 {
			return nil, fmt.Errorf("sequence overflow: %w", ErrIncompletePrefix)
		}
		next++
		normalized, err := validateRecord(record)
		if err != nil {
			return nil, fmt.Errorf("record %d: %w", index, err)
		}
		if normalized.Sequence() != next {
			return nil, fmt.Errorf("record %d sequence %d, want %d: %w", index, normalized.Sequence(), next, ErrIncompletePrefix)
		}
		validated[index] = normalized
	}
	return validated, nil
}

func requireStream(stream StreamAddress) error {
	if stream.value == "" {
		return fmt.Errorf("graphstore: stream address is required")
	}
	return nil
}

func validateRecord(record Record) (Record, error) {
	normalized, err := NewRecord(record.sequence, record.typ, record.payload)
	if err != nil {
		return Record{}, err
	}
	if !bytes.Equal(normalized.payload, record.payload) {
		return Record{}, fmt.Errorf("payload is not canonical")
	}
	return normalized, nil
}

func insertRecord(ctx context.Context, tx *sql.Tx, stream StreamAddress, record Record) error {
	hash := canon.Hash(record.payload)
	_, err := tx.ExecContext(ctx,
		`INSERT INTO journal(stream, sequence, typ, payload, payload_hash) VALUES (?, ?, ?, ?, ?)`,
		stream.value, record.Sequence(), record.Type(), record.payload, hash[:])
	return err
}

func streamHead(ctx context.Context, tx *sql.Tx, stream StreamAddress) (uint64, error) {
	var head, count uint64
	if err := tx.QueryRowContext(ctx,
		`SELECT COALESCE(MAX(sequence), 0), COUNT(*) FROM journal WHERE stream = ?`, stream.value,
	).Scan(&head, &count); err != nil {
		return 0, fmt.Errorf("graphstore: read head %q: %w", stream.value, err)
	}
	if count != head {
		return 0, fmt.Errorf("graphstore: stream %q: %w", stream.value, ErrCorruptJournal)
	}
	return head, nil
}

func bytesToHash(value []byte) [32]byte {
	var hash [32]byte
	copy(hash[:], value)
	return hash
}
