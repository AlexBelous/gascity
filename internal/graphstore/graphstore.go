// Package graphstore provides the append-only journal used by the beta formula
// runtime.
package graphstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync"
)

// Options configures Open.
type Options struct {
	// CityID is the immutable city identity included in each stream's genesis
	// hash. An empty value adopts the identity already stored in the database.
	CityID string
}

// Store is a hash-chained event journal with optimistic concurrency and writer
// leases. The shared SQLite substrate serializes writes within this process and
// provides a WAL-backed read pool for concurrent readers.
type Store struct {
	db     *sql.DB
	cityID string

	mu    sync.RWMutex
	vocab map[vocabKey]struct{}

	rebuildAfterRead func()
}

type vocabKey struct {
	engine string
	typ    string
}

// Open opens or creates a journal at path.
func Open(ctx context.Context, path string, opts Options) (*Store, error) {
	if path == "" {
		return nil, fmt.Errorf("graphstore: open: empty path")
	}
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("graphstore: open: %w", err)
	}
	db, err := openSQLite(ctx, path, migrations)
	if err != nil {
		return nil, fmt.Errorf("graphstore: open %q: %w", path, err)
	}
	cityID, err := seedCityID(ctx, db, opts.CityID)
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	return &Store{
		db:     db,
		cityID: cityID,
		vocab:  make(map[vocabKey]struct{}),
	}, nil
}

func seedCityID(ctx context.Context, db *sql.DB, want string) (string, error) {
	var got string
	err := writeTransaction(ctx, db, func(tx *sql.Tx) error {
		if want != "" {
			if _, err := tx.ExecContext(ctx,
				`INSERT INTO graph_meta(key, value) VALUES('city_id', ?)
				 ON CONFLICT(key) DO NOTHING`,
				want,
			); err != nil {
				return fmt.Errorf("seeding city id: %w", err)
			}
		}
		err := tx.QueryRowContext(ctx,
			`SELECT value FROM graph_meta WHERE key = 'city_id'`,
		).Scan(&got)
		if errors.Is(err, sql.ErrNoRows) {
			got = ""
			return nil
		}
		if err != nil {
			return fmt.Errorf("reading city id: %w", err)
		}
		return nil
	})
	if err != nil {
		return "", fmt.Errorf("graphstore: %w", mapSQLiteBusy(err))
	}
	if want != "" && got != want {
		return "", fmt.Errorf(
			"graphstore: opening store for city %q but it belongs to city %q: %w",
			want, got, ErrCityMismatch,
		)
	}
	return got, nil
}

func (s *Store) write(ctx context.Context, fn func(*sql.Tx) error) error {
	return mapSQLiteBusy(writeTransaction(ctx, s.db, fn))
}

func mapSQLiteBusy(err error) error {
	if err == nil {
		return nil
	}
	message := err.Error()
	if strings.Contains(message, "SQLITE_BUSY") ||
		strings.Contains(message, "SQLITE_LOCKED") ||
		strings.Contains(message, "database is locked") ||
		strings.Contains(message, "database table is locked") {
		return fmt.Errorf("%w: %w", ErrBusy, err)
	}
	return err
}

// CityID returns the immutable city identity used by stream genesis hashes.
func (s *Store) CityID() string { return s.cityID }

// ReadDB returns the journal's read-only-by-convention query handle.
func (s *Store) ReadDB() *sql.DB { return s.db }

// Close closes the journal.
func (s *Store) Close() error { return s.db.Close() }

// RegisterEventType permits an engine/type pair at Append. Registration is
// additive and idempotent.
func (s *Store) RegisterEventType(engine, typ string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.vocab[vocabKey{engine: engine, typ: typ}] = struct{}{}
}

func (s *Store) isRegistered(engine, typ string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, ok := s.vocab[vocabKey{engine: engine, typ: typ}]
	return ok
}
