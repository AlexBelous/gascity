package graphstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

const schemaVersion = 1

const schema = `
CREATE TABLE schema_version (version INTEGER NOT NULL CHECK(version = 1));
INSERT INTO schema_version(version) VALUES (1);

CREATE TABLE journal (
  stream       TEXT NOT NULL CHECK(length(stream) > 0),
  sequence     INTEGER NOT NULL CHECK(sequence > 0),
  typ          TEXT NOT NULL CHECK(length(typ) > 0),
  payload      BLOB NOT NULL,
  payload_hash BLOB NOT NULL CHECK(length(payload_hash) = 32),
  PRIMARY KEY (stream, sequence)
);

CREATE TABLE writer_lease (
  stream     TEXT PRIMARY KEY CHECK(length(stream) > 0),
  holder     TEXT NOT NULL CHECK(length(holder) > 0),
  epoch      INTEGER NOT NULL CHECK(epoch > 0),
  expires_at INTEGER NOT NULL
);

CREATE TRIGGER journal_no_update BEFORE UPDATE ON journal
BEGIN
  SELECT RAISE(ABORT, 'journal is append-only');
END;

CREATE TRIGGER journal_no_delete BEFORE DELETE ON journal
BEGIN
  SELECT RAISE(ABORT, 'journal is append-only');
END;
`

func migrate(ctx context.Context, db *sql.DB) error {
	var present int
	if err := db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'schema_version'`,
	).Scan(&present); err != nil {
		return fmt.Errorf("graphstore: check schema version: %w", err)
	}
	if present == 0 {
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("graphstore: begin schema creation: %w", err)
		}
		defer func() { _ = tx.Rollback() }()
		if _, err := tx.ExecContext(ctx, schema); err != nil {
			return fmt.Errorf("graphstore: create schema: %w", err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("graphstore: commit schema creation: %w", err)
		}
		return nil
	}
	var version int
	if err := db.QueryRowContext(ctx, `SELECT version FROM schema_version`).Scan(&version); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("graphstore: schema version is missing")
		}
		return fmt.Errorf("graphstore: read schema version: %w", err)
	}
	if version != schemaVersion {
		return fmt.Errorf("graphstore: unsupported schema version %d", version)
	}
	return nil
}
