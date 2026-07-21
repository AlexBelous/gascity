package core

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func testMigrations() []Migration {
	return []Migration{{
		Version: 1,
		DDL: []string{
			`CREATE TABLE IF NOT EXISTS items (
				id TEXT PRIMARY KEY,
				val TEXT NOT NULL DEFAULT '',
				created_at INTEGER NOT NULL
			)`,
			`CREATE INDEX IF NOT EXISTS idx_items_created ON items(created_at)`,
		},
	}}
}

func openTestDB(t *testing.T, opts ...Option) *DB {
	t.Helper()
	db, err := Open(filepath.Join(t.TempDir(), "test.db"), testMigrations(), opts...)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestOpenMigrateReopen(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "class.db")
	db, err := Open(path, testMigrations())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := db.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO items (id, val, created_at) VALUES ('a', 'x', 1)`)
		return err
	}); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	// Reopen: migrations are version-gated and must be a no-op; data survives.
	db2, err := Open(path, testMigrations())
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer db2.Close() //nolint:errcheck
	var n int
	if err := db2.Read().QueryRow(`SELECT COUNT(*) FROM items`).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 1 {
		t.Fatalf("count = %d, want 1", n)
	}
	var version int
	if err := db2.Read().QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		t.Fatalf("user_version: %v", err)
	}
	if version != 1 {
		t.Fatalf("user_version = %d, want 1", version)
	}
}

func TestMigrationsApplyInOrderAndOnlyOnce(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "class.db")
	v1 := testMigrations()
	db, err := Open(path, v1)
	if err != nil {
		t.Fatalf("Open v1: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	// A second migration adds a column; reopening with [v1, v2] applies only v2.
	v2 := append(testMigrations(), Migration{
		Version: 2,
		DDL:     []string{`ALTER TABLE items ADD COLUMN extra TEXT NOT NULL DEFAULT ''`},
	})
	db2, err := Open(path, v2)
	if err != nil {
		t.Fatalf("Open v2: %v", err)
	}
	defer db2.Close() //nolint:errcheck
	if _, err := db2.Read().Query(`SELECT extra FROM items`); err != nil {
		t.Fatalf("v2 column missing: %v", err)
	}
	var version int
	if err := db2.Read().QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		t.Fatalf("user_version: %v", err)
	}
	if version != 2 {
		t.Fatalf("user_version = %d, want 2", version)
	}
}

func TestOpenRejectsDowngrade(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "class.db")
	v2 := append(testMigrations(), Migration{Version: 2, DDL: []string{`CREATE TABLE t2 (id TEXT)`}})
	db, err := Open(path, v2)
	if err != nil {
		t.Fatalf("Open v2: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	// An older binary (knowing only v1) must refuse a v2 file rather than
	// silently operating on a schema it does not understand.
	if _, err := Open(path, testMigrations()); err == nil {
		t.Fatal("Open accepted a database with a newer schema version")
	}
}

func TestWALModePersisted(t *testing.T) {
	db := openTestDB(t)
	var mode string
	if err := db.Read().QueryRow(`PRAGMA journal_mode`).Scan(&mode); err != nil {
		t.Fatalf("journal_mode: %v", err)
	}
	if mode != "wal" {
		t.Fatalf("journal_mode = %q, want wal", mode)
	}
}

func TestConcurrentWritersSerialize(t *testing.T) {
	db := openTestDB(t)
	const goroutines = 8
	const perG = 25
	var wg sync.WaitGroup
	errs := make(chan error, goroutines)
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < perG; i++ {
				id := fmt.Sprintf("g%d-i%d", g, i)
				if err := db.Write(context.Background(), func(tx *sql.Tx) error {
					_, err := tx.Exec(`INSERT INTO items (id, val, created_at) VALUES (?, ?, ?)`, id, "v", time.Now().UnixNano())
					return err
				}); err != nil {
					errs <- fmt.Errorf("write %s: %w", id, err)
					return
				}
			}
		}(g)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	var n int
	if err := db.Read().QueryRow(`SELECT COUNT(*) FROM items`).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != goroutines*perG {
		t.Fatalf("count = %d, want %d", n, goroutines*perG)
	}
}

func TestWriteRollsBackOnError(t *testing.T) {
	db := openTestDB(t)
	sentinel := errors.New("boom")
	err := db.Write(context.Background(), func(tx *sql.Tx) error {
		if _, err := tx.Exec(`INSERT INTO items (id, val, created_at) VALUES ('r', 'v', 1)`); err != nil {
			return err
		}
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("Write err = %v, want sentinel", err)
	}
	var n int
	if err := db.Read().QueryRow(`SELECT COUNT(*) FROM items WHERE id='r'`).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 0 {
		t.Fatal("failed Write left a committed row")
	}
}

func TestSweeperRunsAndStops(t *testing.T) {
	db := openTestDB(t)
	ran := make(chan struct{}, 8)
	stop := db.StartSweeper(5*time.Millisecond, func(_ context.Context) {
		select {
		case ran <- struct{}{}:
		default:
		}
	})
	select {
	case <-ran:
	case <-time.After(5 * time.Second):
		t.Fatal("sweeper never ran")
	}
	stop()
	// Stop must be idempotent and must not race Close.
	stop()
	if err := db.Close(); err != nil {
		t.Fatalf("Close after sweeper stop: %v", err)
	}
}

func TestSingleConnOption(t *testing.T) {
	db := openTestDB(t, WithSingleConn())
	if err := db.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO items (id, val, created_at) VALUES ('s', 'v', 1)`)
		return err
	}); err != nil {
		t.Fatalf("Write: %v", err)
	}
	var n int
	if err := db.Read().QueryRow(`SELECT COUNT(*) FROM items`).Scan(&n); err != nil {
		t.Fatalf("read on single-conn DB: %v", err)
	}
	if n != 1 {
		t.Fatalf("count = %d, want 1", n)
	}
}

func TestPingAndIntegrity(t *testing.T) {
	db := openTestDB(t)
	if err := db.Ping(); err != nil {
		t.Fatalf("Ping: %v", err)
	}
	ok, err := db.IntegrityCheck(context.Background())
	if err != nil {
		t.Fatalf("IntegrityCheck: %v", err)
	}
	if !ok {
		t.Fatal("IntegrityCheck reported corruption on a fresh database")
	}
}

func TestCloseIdempotent(t *testing.T) {
	db := openTestDB(t)
	if err := db.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
}
