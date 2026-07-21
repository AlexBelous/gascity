// Package core is the shared embedded-SQLite substrate for the per-class
// coordination stores (engdocs/design/infra-class-sqlite-stores.md). Each
// relocated class (messaging, sessions, orders, nudges) builds its typed
// store over one core.DB: a pure-Go (modernc.org/sqlite, CGO_ENABLED=0)
// database file with WAL journaling, version-gated migrations, a serialized
// write path with busy retries, a read pool, and a retention-sweeper scaffold.
//
// The mechanics are ported from the deleted coordination store
// (git show ba607c16d^:internal/beads/sqlite_store.go), with one fix: PRAGMAs
// ride the modernc `_pragma=` DSN form so they apply to every pooled
// connection (the deleted store's mattn-style `?_busy_timeout=` param was
// silently ignored by modernc).
//
// Access model (ratified in the design): files are opened directly by
// multiple processes — the long-running controller holds a persistent
// handle (write conn + read pool) while short-lived CLI/hook processes use
// WithSingleConn. WAL + busy_timeout + application-level retry arbitrate
// cross-process contention; correctness invariants live in each class's
// schema (UNIQUE constraints, transactions), never in in-process locks.
package core

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite" // pure-Go SQLite driver, CGO_ENABLED=0 safe
)

const (
	busyTimeout = 5 * time.Second

	// busyRetryAttempts is the number of application-level retries after the
	// per-connection busy_timeout is exhausted. Each retry backs off by
	// busyRetryDelay before re-attempting, giving competing writers (possibly
	// in other processes) time to release the WAL write lock.
	busyRetryAttempts = 3
	busyRetryDelay    = 150 * time.Millisecond

	defaultReadPool = 8
)

// Migration is one version-gated schema step. DDL statements must be
// idempotent (CREATE TABLE IF NOT EXISTS / CREATE INDEX IF NOT EXISTS) so a
// crash between the DDL and the user_version bump re-runs harmlessly.
type Migration struct {
	// Version is the PRAGMA user_version the database reports after this
	// migration applies. Versions start at 1 and are strictly increasing.
	Version int
	// DDL statements applied in order inside one transaction.
	DDL []string
}

// Options configures Open.
type Options struct {
	readPool   int
	singleConn bool
}

// Option customizes Open.
type Option func(*Options)

// WithReadPool sets the read-pool size for a long-lived handle (default 8).
func WithReadPool(n int) Option {
	return func(o *Options) {
		if n > 0 {
			o.readPool = n
		}
	}
}

// WithSingleConn opens one connection serving both reads and writes — the
// shape for short-lived CLI/hook processes, which pay one connection setup
// and hold the file open only for the duration of the command.
func WithSingleConn() Option {
	return func(o *Options) { o.singleConn = true }
}

// DB is an open per-class store file.
type DB struct {
	write *sql.DB // MaxOpenConns=1: serializes this process's mutations
	read  *sql.DB // read pool; == write under WithSingleConn
	path  string

	mu       sync.Mutex // guards sweepers
	sweepers []chan struct{}

	closeOnce sync.Once
	closeErr  error
}

// dsn builds the modernc DSN with per-connection PRAGMAs. journal_mode=WAL is
// a persistent database property but is asserted on every connection anyway;
// _txlock=immediate makes write transactions take the WAL write lock at BEGIN,
// so lock contention surfaces as a retryable SQLITE_BUSY at the start of the
// transaction instead of a non-retryable upgrade failure mid-transaction.
func dsn(path string, forWrite bool) string {
	pragmas := []string{
		"busy_timeout(" + fmt.Sprint(busyTimeout.Milliseconds()) + ")",
		"journal_mode(WAL)",
		"synchronous(FULL)",
		"foreign_keys(1)",
	}
	q := url.Values{}
	for _, p := range pragmas {
		q.Add("_pragma", p)
	}
	if forWrite {
		q.Set("_txlock", "immediate")
	}
	return "file:" + path + "?" + q.Encode()
}

// Open opens (creating if absent) the store file at path and applies any
// unapplied migrations. It refuses a file whose schema version is NEWER than
// the highest known migration: an older binary must not operate on a schema
// it does not understand.
func Open(path string, migrations []Migration, opts ...Option) (*DB, error) {
	cfg := Options{readPool: defaultReadPool}
	for _, opt := range opts {
		opt(&cfg)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("opening class store %s: %w", path, err)
	}

	write, err := sql.Open("sqlite", dsn(path, true))
	if err != nil {
		return nil, fmt.Errorf("opening class store %s: %w", path, err)
	}
	write.SetMaxOpenConns(1)

	d := &DB{write: write, read: write, path: path}
	if err := d.migrate(context.Background(), migrations); err != nil {
		_ = write.Close()
		return nil, err
	}

	if !cfg.singleConn {
		read, err := sql.Open("sqlite", dsn(path, false))
		if err != nil {
			_ = write.Close()
			return nil, fmt.Errorf("opening class store read pool %s: %w", path, err)
		}
		read.SetMaxOpenConns(cfg.readPool)
		read.SetMaxIdleConns(cfg.readPool)
		read.SetConnMaxIdleTime(5 * time.Minute)
		d.read = read
	}
	return d, nil
}

// migrate applies unapplied migrations in ascending version order, each in
// its own transaction followed by the user_version bump.
func (d *DB) migrate(ctx context.Context, migrations []Migration) error {
	sorted := make([]Migration, len(migrations))
	copy(sorted, migrations)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Version < sorted[j].Version })

	var current int
	if err := d.write.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&current); err != nil {
		return fmt.Errorf("reading schema version of %s: %w", d.path, err)
	}
	highest := 0
	if len(sorted) > 0 {
		highest = sorted[len(sorted)-1].Version
	}
	if current > highest {
		return fmt.Errorf("class store %s has schema version %d, newer than this binary's %d; upgrade gc before opening it", d.path, current, highest)
	}
	for _, m := range sorted {
		if m.Version <= current {
			continue
		}
		err := retryOnBusy(func() error {
			tx, err := d.write.BeginTx(ctx, nil)
			if err != nil {
				return err
			}
			defer tx.Rollback() //nolint:errcheck
			for _, stmt := range m.DDL {
				if _, err := tx.Exec(stmt); err != nil {
					return fmt.Errorf("migration v%d: %w", m.Version, err)
				}
			}
			// PRAGMA cannot be parameterized; Version is a trusted literal
			// from compiled-in migrations, never user input.
			if _, err := tx.Exec(fmt.Sprintf(`PRAGMA user_version = %d`, m.Version)); err != nil {
				return fmt.Errorf("migration v%d: bumping user_version: %w", m.Version, err)
			}
			return tx.Commit()
		})
		if err != nil {
			return fmt.Errorf("migrating class store %s to v%d: %w", d.path, m.Version, err)
		}
	}
	return nil
}

// Write runs fn inside one immediate write transaction, retrying the whole
// transaction on SQLITE_BUSY. fn must be safe to re-run (it is re-invoked on
// retry) and must not retain the Tx after returning.
func (d *DB) Write(ctx context.Context, fn func(tx *sql.Tx) error) error {
	return retryOnBusy(func() error {
		tx, err := d.write.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer tx.Rollback() //nolint:errcheck
		if err := fn(tx); err != nil {
			return err
		}
		return tx.Commit()
	})
}

// Read returns the read handle (the shared single connection under
// WithSingleConn). Callers issue plain database/sql queries against it.
func (d *DB) Read() *sql.DB { return d.read }

// Path returns the database file path.
func (d *DB) Path() string { return d.path }

// Ping verifies the store is operational.
func (d *DB) Ping() error {
	var one int
	return d.read.QueryRow(`SELECT 1`).Scan(&one)
}

// IntegrityCheck runs PRAGMA integrity_check and reports whether the
// database is intact. Used by crash-recovery tests and health surfaces.
func (d *DB) IntegrityCheck(ctx context.Context) (bool, error) {
	var result string
	if err := d.read.QueryRowContext(ctx, `PRAGMA integrity_check`).Scan(&result); err != nil {
		return false, err
	}
	return result == "ok", nil
}

// StartSweeper runs sweep on the given cadence until the returned stop
// function is called or the DB is closed. Stop is idempotent. Each class
// store passes its retention DELETEs here.
func (d *DB) StartSweeper(interval time.Duration, sweep func(ctx context.Context)) (stop func()) {
	if interval <= 0 {
		return func() {}
	}
	done := make(chan struct{})
	d.mu.Lock()
	d.sweepers = append(d.sweepers, done)
	d.mu.Unlock()

	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				sweep(ctx)
			}
		}
	}()
	var once sync.Once
	return func() {
		once.Do(func() {
			close(done)
			cancel()
			wg.Wait()
		})
	}
}

// Close stops sweepers and closes the underlying handles. Idempotent.
func (d *DB) Close() error {
	d.closeOnce.Do(func() {
		d.mu.Lock()
		sweepers := d.sweepers
		d.sweepers = nil
		d.mu.Unlock()
		for _, done := range sweepers {
			select {
			case <-done:
			default:
				close(done)
			}
		}
		var errs []error
		if d.read != d.write {
			if err := d.read.Close(); err != nil {
				errs = append(errs, err)
			}
		}
		if err := d.write.Close(); err != nil {
			errs = append(errs, err)
		}
		d.closeErr = errors.Join(errs...)
	})
	return d.closeErr
}

// isBusy reports whether err is a SQLite write-contention error. The modernc
// driver surfaces these as "database is locked (5) (SQLITE_BUSY)" once the
// per-connection busy_timeout expires without acquiring the WAL write lock.
func isBusy(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "SQLITE_BUSY") || strings.Contains(msg, "database is locked")
}

// retryOnBusy retries fn up to busyRetryAttempts times when it returns a
// SQLITE_BUSY error, backing off between attempts. The busy_timeout PRAGMA
// already waits at the driver layer per call, so each application-level retry
// is an additional full busy_timeout window.
func retryOnBusy(fn func() error) error {
	err := fn()
	for attempt := 0; attempt < busyRetryAttempts && isBusy(err); attempt++ {
		time.Sleep(busyRetryDelay)
		err = fn()
	}
	return err
}
