package graphstore

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite" // pure-Go SQLite driver
)

const sqliteBusyTimeoutMillis = 5000

func openSQLite(ctx context.Context, path string, migrations []migration) (*sql.DB, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("create journal directory: %w", err)
	}

	params := url.Values{}
	for _, pragma := range []string{
		fmt.Sprintf("busy_timeout(%d)", sqliteBusyTimeoutMillis),
		"journal_mode(WAL)",
		"synchronous(FULL)",
		"foreign_keys(1)",
	} {
		params.Add("_pragma", pragma)
	}
	params.Set("_txlock", "immediate")

	db, err := sql.Open("sqlite", "file:"+path+"?"+params.Encode())
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	db.SetMaxOpenConns(1)
	if err := migrate(ctx, db, path, migrations); err != nil {
		_ = db.Close()
		return nil, err
	}
	return db, nil
}

func migrate(ctx context.Context, db *sql.DB, path string, migrations []migration) error {
	var current int
	if err := db.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&current); err != nil {
		return fmt.Errorf("read schema version of %q: %w", path, err)
	}
	highest := 0
	if len(migrations) > 0 {
		highest = migrations[len(migrations)-1].Version
	}
	if current > highest {
		return fmt.Errorf(
			"journal %q has schema version %d, newer than this binary's %d",
			path, current, highest,
		)
	}

	for _, migration := range migrations {
		if migration.Version <= current {
			continue
		}
		err := writeTransaction(ctx, db, func(tx *sql.Tx) error {
			for _, statement := range migration.DDL {
				if _, err := tx.ExecContext(ctx, statement); err != nil {
					return err
				}
			}
			_, err := tx.ExecContext(
				ctx,
				fmt.Sprintf(`PRAGMA user_version = %d`, migration.Version),
			)
			return err
		})
		if err != nil {
			return fmt.Errorf(
				"migrate journal %q to version %d: %w",
				path, migration.Version, err,
			)
		}
		current = migration.Version
	}
	return nil
}

func writeTransaction(ctx context.Context, db *sql.DB, fn func(*sql.Tx) error) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit()
}
