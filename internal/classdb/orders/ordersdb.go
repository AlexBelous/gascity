// Package ordersdb is the embedded-SQLite orders-class store
// (engdocs/design/infra-class-sqlite-stores.md, Orders section): one
// order_run row per tracking record over the shared internal/classdb/core
// substrate, implementing the internal/orders trackingBackend surface with
// typed columns — no bead envelope, no labels-as-state.
//
// Semantics are derived from the beads backend it replaces
// (internal/orders/tracking_beads.go), minus the Dolt-lag workarounds the
// design retires (close-verify retries, close_reason length floors): commits
// are synchronous and local, so a close is durable when CloseRun returns.
// Invariants live in the schema and single-statement UPDATEs, never in
// in-process locks — the file is opened by multiple processes (controller
// persistent handle; CLI/hook one-shots via core.WithSingleConn).
package ordersdb

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/classdb/core"
	"github.com/gastownhall/gascity/internal/convergence"
	"github.com/gastownhall/gascity/internal/orders"
)

// idPrefix is the reserved orders-class id prefix. It must stay in lockstep
// with config.ReservedClassPrefix(config.BeadClassOrders) — pinned by
// TestIDPrefixMatchesReservedClassPrefix so the constant never drifts from
// the config registry without importing internal/config here.
const idPrefix = "gco"

// migrations is the version-gated order_run schema. The composite
// (scoped_name, created_at DESC, id DESC) index bakes in the canonical
// newest-first ordering tie-break (the nativeCreatedLimitPushdown contract);
// the partial open indexes serve the single-flight, stale-sweep, and
// orphan-sweep reads; idx_run_retention serves the closed-run retention scan.
// id_seq is the id mint counter, advanced inside the same transaction as each
// INSERT so minted ids are unique across concurrent processes.
func migrations() []core.Migration {
	return []core.Migration{{
		Version: 1,
		DDL: []string{
			`CREATE TABLE IF NOT EXISTS order_run (
				id           TEXT PRIMARY KEY,
				scoped_name  TEXT NOT NULL,
				created_at   INTEGER NOT NULL,
				updated_at   INTEGER NOT NULL,
				open         INTEGER NOT NULL,
				outcome      TEXT NOT NULL DEFAULT '',
				seq          INTEGER NOT NULL DEFAULT 0,
				close_reason TEXT NOT NULL DEFAULT '',
				sweep_by     TEXT NOT NULL DEFAULT ''
			)`,
			`CREATE INDEX IF NOT EXISTS idx_run_scoped_created ON order_run(scoped_name, created_at DESC, id DESC)`,
			`CREATE INDEX IF NOT EXISTS idx_run_open ON order_run(scoped_name) WHERE open=1`,
			`CREATE INDEX IF NOT EXISTS idx_run_stale ON order_run(created_at) WHERE open=1`,
			`CREATE INDEX IF NOT EXISTS idx_run_retention ON order_run(open, scoped_name, updated_at)`,
			`CREATE TABLE IF NOT EXISTS id_seq (k INTEGER PRIMARY KEY CHECK (k = 1), next INTEGER NOT NULL)`,
			`INSERT OR IGNORE INTO id_seq (k, next) VALUES (1, 0)`,
		},
	}}
}

// runColumns is the canonical order_run SELECT column list every read shares.
const runColumns = `id, scoped_name, created_at, updated_at, open, outcome, seq`

// newestFirst is the canonical ordering: created-DESC with id-DESC tie-break
// (the nativeCreatedLimitPushdown contract the beads backend honors).
const newestFirst = ` ORDER BY created_at DESC, id DESC`

// Store is the embedded-SQLite orders-class store. It structurally implements
// the internal/orders trackingBackend interface; wire it into the domain
// front door via orders.NewStoreWithTracking. It deliberately does NOT
// implement the beadsBacked dedupe assertion — its rows can never be the
// graph store's, so the mixed orders+graph reads always union the graph leg.
type Store struct {
	db *core.DB

	maintenanceOnce sync.Once
}

// Open opens (creating and migrating if needed) the orders store file at
// path. Long-lived callers (the controller) use the default read pool;
// short-lived CLI/hook processes pass core.WithSingleConn.
func Open(path string, opts ...core.Option) (*Store, error) {
	db, err := core.Open(path, migrations(), opts...)
	if err != nil {
		return nil, fmt.Errorf("opening orders store: %w", err)
	}
	return &Store{db: db}, nil
}

// Close closes the underlying database handles. Idempotent.
func (s *Store) Close() error { return s.db.Close() }

// Path returns the database file path.
func (s *Store) Path() string { return s.db.Path() }

// IDPrefix returns the reserved orders-class id prefix ("gco"). Minted ids
// are "gco-<seq>"; legacy-prefixed ids (migrated bd tracking beads) remain
// valid row keys.
func (s *Store) IDPrefix() string { return idPrefix }

// nowNanos returns the current wall-clock time truncated to the stored
// integer precision, so a value returned to a caller compares Equal to the
// same row read back later.
func nowNanos() (time.Time, int64) {
	nanos := time.Now().UnixNano()
	return time.Unix(0, nanos), nanos
}

// CreateRun creates an OPEN tracking row for scoped (the in-flight
// single-flight marker whose created_at advances the cooldown clock). The id
// mint and the row insert commit in one transaction: the at-most-one-extra-
// fire crash contract holds because the row is durable before CreateRun
// returns and the dispatch goroutine launches.
func (s *Store) CreateRun(scoped string, opts orders.RunOpts) (orders.OrderRun, error) {
	if strings.TrimSpace(scoped) == "" {
		return orders.OrderRun{}, fmt.Errorf("creating order run: empty scoped order name")
	}
	created, nanos := nowNanos()
	id, err := s.insertRun(scoped, nanos, true, opts.Outcome, 0, "")
	if err != nil {
		return orders.OrderRun{}, fmt.Errorf("creating order run for %q: %w", scoped, err)
	}
	return orders.OrderRun{
		ID:        id,
		Scoped:    scoped,
		Outcome:   opts.Outcome,
		CreatedAt: created,
		UpdatedAt: created,
		Open:      true,
	}, nil
}

// CreateRunClosed creates an already-CLOSED tracking row — the
// cooldown-advance-only path. Unlike the beads backend's four-write sequence,
// this is one atomic INSERT; the observable result (a closed run carrying the
// cursor, outcome, and close reason, whose CreatedAt advances the cooldown
// clock) is identical.
func (s *Store) CreateRunClosed(scoped string, outcome orders.RunOutcome, cursor *orders.EventCursor, closeReason string) (orders.OrderRun, error) {
	if strings.TrimSpace(scoped) == "" {
		return orders.OrderRun{}, fmt.Errorf("creating closed order run: empty scoped order name")
	}
	var seq uint64
	if cursor != nil {
		seq = uint64(*cursor)
	}
	created, nanos := nowNanos()
	id, err := s.insertRun(scoped, nanos, false, outcome, seq, closeReason)
	if err != nil {
		return orders.OrderRun{}, fmt.Errorf("creating closed order run for %q: %w", scoped, err)
	}
	run := orders.OrderRun{
		ID:        id,
		Scoped:    scoped,
		Outcome:   outcome,
		CreatedAt: created,
		UpdatedAt: created,
	}
	if cursor != nil {
		run.Cursor = *cursor
	}
	return run, nil
}

// insertRun mints the next id and inserts one row in a single transaction.
func (s *Store) insertRun(scoped string, nanos int64, open bool, outcome orders.RunOutcome, seq uint64, closeReason string) (string, error) {
	var id string
	err := s.db.Write(context.Background(), func(tx *sql.Tx) error {
		var next int64
		if err := tx.QueryRow(`UPDATE id_seq SET next = next + 1 WHERE k = 1 RETURNING next`).Scan(&next); err != nil {
			return fmt.Errorf("minting id: %w", err)
		}
		id = fmt.Sprintf("%s-%d", idPrefix, next)
		openInt := 0
		if open {
			openInt = 1
		}
		_, err := tx.Exec(
			`INSERT INTO order_run (id, scoped_name, created_at, updated_at, open, outcome, seq, close_reason) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			id, scoped, nanos, nanos, openInt, outcome.Token(), int64(seq), closeReason, //nolint:gosec // seq is an event-bus sequence, far below int64 range
		)
		return err
	})
	if err != nil {
		return "", err
	}
	return id, nil
}

// ImportRun inserts one migrated legacy tracking run, preserving its id
// (legacy bd prefixes stay valid row keys), clocks, open state, outcome, and
// cursor. INSERT OR IGNORE keeps re-imports idempotent, so an interrupted
// migration simply resumes; an id that already exists is left untouched.
func (s *Store) ImportRun(run orders.OrderRun) error {
	if strings.TrimSpace(run.ID) == "" {
		return fmt.Errorf("importing order run: empty id")
	}
	if strings.TrimSpace(run.Scoped) == "" {
		return fmt.Errorf("importing order run %q: empty scoped order name", run.ID)
	}
	if run.CreatedAt.IsZero() {
		return fmt.Errorf("importing order run %q: zero CreatedAt (the cooldown clock)", run.ID)
	}
	created := run.CreatedAt.UnixNano()
	updated := created
	if !run.UpdatedAt.IsZero() {
		updated = run.UpdatedAt.UnixNano()
	}
	openInt := 0
	if run.Open {
		openInt = 1
	}
	return s.db.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec(
			`INSERT OR IGNORE INTO order_run (id, scoped_name, created_at, updated_at, open, outcome, seq, close_reason) VALUES (?, ?, ?, ?, ?, ?, ?, '')`,
			run.ID, run.Scoped, created, updated, openInt, run.Outcome.Token(), int64(run.Cursor), //nolint:gosec // event-bus sequence, far below int64 range
		)
		if err != nil {
			return fmt.Errorf("importing order run %q: %w", run.ID, err)
		}
		return nil
	})
}

// mutateRun runs one UPDATE against a single run id and converts a zero
// rows-affected result into the store's not-found error, matching the beads
// backend's ErrNotFound behavior for mutations of missing runs.
func (s *Store) mutateRun(runID, stmt string, args ...any) error {
	return s.db.Write(context.Background(), func(tx *sql.Tx) error {
		res, err := tx.Exec(stmt, args...)
		if err != nil {
			return err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return err
		}
		if n == 0 {
			return fmt.Errorf("order run %q: %w", runID, beads.ErrNotFound)
		}
		return nil
	})
}

// SetOutcome stamps the terminal outcome on an existing run.
func (s *Store) SetOutcome(runID string, outcome orders.RunOutcome) error {
	_, nanos := nowNanos()
	if err := s.mutateRun(runID,
		`UPDATE order_run SET outcome = ?, updated_at = ? WHERE id = ?`,
		outcome.Token(), nanos, runID,
	); err != nil {
		return fmt.Errorf("setting order run outcome on %q: %w", runID, err)
	}
	return nil
}

// SetCursor persists the event-bus cursor high-water mark. MAX keeps the
// column monotonic, matching the beads backend where cursor labels accumulate
// and the decode takes the max — a late lower stamp can never regress the
// cursor into replaying consumed events.
func (s *Store) SetCursor(runID, _ string, cursor orders.EventCursor) error {
	_, nanos := nowNanos()
	if err := s.mutateRun(runID,
		`UPDATE order_run SET seq = MAX(seq, ?), updated_at = ? WHERE id = ?`,
		int64(cursor), nanos, runID, //nolint:gosec // event-bus sequence, far below int64 range
	); err != nil {
		return fmt.Errorf("setting order run cursor on %q: %w", runID, err)
	}
	return nil
}

// MarkFailed stamps the failure outcome plus (when cursor is non-nil) the
// event cursor in ONE atomic UPDATE, mirroring the beads backend's
// single-Update contract. The error is returned unwrapped-of-extra-context:
// the sole caller logs it under its own message.
func (s *Store) MarkFailed(runID, _ string, outcome orders.RunOutcome, cursor *orders.EventCursor) error {
	_, nanos := nowNanos()
	if cursor != nil {
		return s.mutateRun(runID,
			`UPDATE order_run SET outcome = ?, seq = MAX(seq, ?), updated_at = ? WHERE id = ?`,
			outcome.Token(), int64(*cursor), nanos, runID, //nolint:gosec // event-bus sequence, far below int64 range
		)
	}
	return s.mutateRun(runID,
		`UPDATE order_run SET outcome = ?, updated_at = ? WHERE id = ?`,
		outcome.Token(), nanos, runID,
	)
}

// CloseRun closes a run, stamping close_reason when non-empty. Closing an
// already-closed run is a no-op that may refresh the close reason, matching
// the beads backend (SetMetadata applies, Close no-ops). The commit is
// synchronous and local — there is deliberately no close-verify retry (a
// Dolt-lag workaround the design retires).
func (s *Store) CloseRun(runID, reason string) error {
	_, nanos := nowNanos()
	if err := s.mutateRun(runID,
		`UPDATE order_run SET open = 0, updated_at = ?, close_reason = CASE WHEN ? = '' THEN close_reason ELSE ? END WHERE id = ?`,
		nanos, reason, reason, runID,
	); err != nil {
		return fmt.Errorf("closing order run %q: %w", runID, err)
	}
	return nil
}

// CloseRuns closes a batch of runs with a shared close reason in one UPDATE,
// returning how many rows actually transitioned open→closed (already-closed
// and missing ids are skipped, matching the beads backend's CloseAll). No
// verify loop: the local commit is the durability boundary.
func (s *Store) CloseRuns(ctx context.Context, ids []string, reason string) (int, error) {
	ids = uniqueNonEmptyIDs(ids)
	if len(ids) == 0 {
		return 0, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	_, nanos := nowNanos()
	args := make([]any, 0, len(ids)+2)
	args = append(args, reason, nanos)
	placeholders := make([]string, 0, len(ids))
	for _, id := range ids {
		placeholders = append(placeholders, "?")
		args = append(args, id)
	}
	closed := 0
	err := s.db.Write(ctx, func(tx *sql.Tx) error {
		res, err := tx.Exec(
			`UPDATE order_run SET open = 0, close_reason = ?, updated_at = ? WHERE id IN (`+strings.Join(placeholders, ", ")+`) AND open = 1`,
			args...,
		)
		if err != nil {
			return err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return err
		}
		closed = int(n)
		return nil
	})
	if err != nil {
		return 0, fmt.Errorf("closing order runs %s: %w", strings.Join(ids, ", "), err)
	}
	return closed, nil
}

// CloseRunsSwept is CloseRuns with the stale-sweep audit vocabulary: the
// initiating sweeper lands in the sweep_by column alongside close_reason.
func (s *Store) CloseRunsSwept(ctx context.Context, ids []string, reason, sweptBy string) (int, error) {
	ids = uniqueNonEmptyIDs(ids)
	if len(ids) == 0 {
		return 0, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	_, nanos := nowNanos()
	args := make([]any, 0, len(ids)+3)
	args = append(args, reason, sweptBy, nanos)
	placeholders := make([]string, 0, len(ids))
	for _, id := range ids {
		placeholders = append(placeholders, "?")
		args = append(args, id)
	}
	closed := 0
	err := s.db.Write(ctx, func(tx *sql.Tx) error {
		res, err := tx.Exec(
			`UPDATE order_run SET open = 0, close_reason = ?, sweep_by = ?, updated_at = ? WHERE id IN (`+strings.Join(placeholders, ", ")+`) AND open = 1`,
			args...,
		)
		if err != nil {
			return err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return err
		}
		closed = int(n)
		return nil
	})
	if err != nil {
		return 0, fmt.Errorf("sweep-closing order runs %s: %w", strings.Join(ids, ", "), err)
	}
	return closed, nil
}

// DeleteRun permanently removes one run row — the retention path.
func (s *Store) DeleteRun(runID string) error {
	err := s.db.Write(context.Background(), func(tx *sql.Tx) error {
		res, err := tx.Exec(`DELETE FROM order_run WHERE id = ?`, runID)
		if err != nil {
			return err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return err
		}
		if n == 0 {
			return beads.ErrNotFound
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("deleting order run %q: %w", runID, err)
	}
	return nil
}

// Get reads one run by id.
func (s *Store) Get(handle string) (orders.OrderRun, error) {
	run, err := s.queryRun(`SELECT `+runColumns+` FROM order_run WHERE id = ?`, handle)
	if err != nil {
		return orders.OrderRun{}, fmt.Errorf("orders get %q: %w", handle, err)
	}
	return run, nil
}

// RunDetail reads one run by id together with its exec-gate output. The
// orders class stores no gate metadata (nothing stamps gate output onto
// tracking records — gate captures live on graph beads), so Gate is always
// empty here.
func (s *Store) RunDetail(handle string) (orders.RunDetail, error) {
	run, err := s.queryRun(`SELECT `+runColumns+` FROM order_run WHERE id = ?`, handle)
	if err != nil {
		return orders.RunDetail{}, fmt.Errorf("orders run detail %q: %w", handle, err)
	}
	return orders.RunDetail{Run: run, Gate: convergence.GateOutput{}}, nil
}

// RecentRuns lists the runs for scoped newest-first, including closed.
// limit <= 0 lists all, matching the beads backend's unbounded ListQuery.
func (s *Store) RecentRuns(scoped string, limit int) ([]orders.OrderRun, error) {
	return s.queryRuns(limit, `SELECT `+runColumns+` FROM order_run WHERE scoped_name = ?`+newestFirst, scoped)
}

// RecentRunsAll lists up to limit runs across every order newest-first,
// including closed — the dispatch cooldown history index fold.
func (s *Store) RecentRunsAll(limit int) ([]orders.OrderRun, error) {
	return s.queryRuns(limit, `SELECT `+runColumns+` FROM order_run`+newestFirst)
}

// OpenRuns lists the OPEN runs across every order newest-first — the
// single-flight open-tracking index fold.
func (s *Store) OpenRuns() ([]orders.OrderRun, error) {
	return s.queryRuns(0, `SELECT `+runColumns+` FROM order_run WHERE open = 1`+newestFirst)
}

// StaleOpenRuns lists OPEN runs created at or before cutoff — the typed read
// half of the stale-order-tracking sweep.
func (s *Store) StaleOpenRuns(cutoff time.Time) ([]orders.OrderRun, error) {
	return s.queryRuns(0, `SELECT `+runColumns+` FROM order_run WHERE open = 1 AND created_at <= ?`+newestFirst, cutoff.UnixNano())
}

// OrphanedOpenRuns lists every OPEN run EXCEPT pre-dispatch
// trigger-env-failure markers (which the open-work gate intentionally keeps
// open until the normal stale sweep) — the startup orphan sweep read.
func (s *Store) OrphanedOpenRuns() ([]orders.OrderRun, error) {
	return s.queryRuns(0, `SELECT `+runColumns+` FROM order_run WHERE open = 1 AND outcome <> ?`+newestFirst, orders.RunOutcomeTriggerEnvFailed.Token())
}

// ClosedRunsForRetention lists the CLOSED runs across every order
// newest-first — the read half of the closed-run retention prune.
func (s *Store) ClosedRunsForRetention() ([]orders.OrderRun, error) {
	return s.queryRuns(0, `SELECT `+runColumns+` FROM order_run WHERE open = 0`+newestFirst)
}

// ListTracking lists every OPEN tracking row newest-first — the orders feed
// scan. Only open rows surface, matching the beads backend's
// no-IncludeClosed feed query.
func (s *Store) ListTracking() ([]orders.OrderRun, error) {
	return s.queryRuns(0, `SELECT `+runColumns+` FROM order_run WHERE open = 1 AND scoped_name <> ''`+newestFirst)
}

// LatestOpenRun returns the newest OPEN run for scoped, if any. Closed runs
// deliberately never surface (the order feed's freshness contract).
func (s *Store) LatestOpenRun(scoped string) (orders.OrderRun, bool, error) {
	run, err := s.queryRun(`SELECT `+runColumns+` FROM order_run WHERE scoped_name = ? AND open = 1`+newestFirst+` LIMIT 1`, scoped)
	if errors.Is(err, beads.ErrNotFound) {
		return orders.OrderRun{}, false, nil
	}
	if err != nil {
		return orders.OrderRun{}, false, err
	}
	return run, true, nil
}

// LastRunTracking reports the most recent run time recorded in this store —
// the orders-leg half of the mixed LastRun read, collapsed to an indexed
// MAX(created_at) point query.
func (s *Store) LastRunTracking(name string) (time.Time, error) {
	var nanos sql.NullInt64
	if err := s.db.Read().QueryRow(`SELECT MAX(created_at) FROM order_run WHERE scoped_name = ?`, name).Scan(&nanos); err != nil {
		return time.Time{}, fmt.Errorf("orders last-run lookup for %q: %w", name, err)
	}
	if !nanos.Valid {
		return time.Time{}, nil
	}
	return time.Unix(0, nanos.Int64), nil
}

// CursorTracking reports the max event seq recorded in this store — the
// orders-leg half of the mixed Cursor read, collapsed to an indexed MAX(seq)
// point query. Read failures are logged and contribute zero, matching the
// dispatcher's original tolerance.
func (s *Store) CursorTracking(name string) orders.EventCursor {
	var seq sql.NullInt64
	if err := s.db.Read().QueryRow(`SELECT MAX(seq) FROM order_run WHERE scoped_name = ?`, name).Scan(&seq); err != nil {
		log.Printf("ordersdb: cursor lookup failed for %s: %v", name, err)
		return 0
	}
	if !seq.Valid || seq.Int64 < 0 {
		return 0
	}
	return orders.EventCursor(seq.Int64)
}

// HasOpenTracking reports whether an open tracking row exists for scoped —
// the orders-leg half of the mixed HasOpenWork read, collapsed to an indexed
// EXISTS point query. The wisp-walk predicate is ignored: this store's rows
// are only tracking records, never wisp/molecule roots (those stay in the
// graph store, which the front door unions).
func (s *Store) HasOpenTracking(scoped string, _ func(beads.Store, beads.Bead) (bool, error)) (bool, error) {
	var one int
	err := s.db.Read().QueryRow(`SELECT 1 FROM order_run WHERE scoped_name = ? AND open = 1 LIMIT 1`, scoped).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("listing order work beads: %w", err)
	}
	return true, nil
}

// queryRun reads exactly one run; a missing row maps to beads.ErrNotFound.
func (s *Store) queryRun(query string, args ...any) (orders.OrderRun, error) {
	row := s.db.Read().QueryRow(query, args...)
	run, err := scanRun(row.Scan)
	if errors.Is(err, sql.ErrNoRows) {
		return orders.OrderRun{}, beads.ErrNotFound
	}
	return run, err
}

// queryRuns reads a run list; limit <= 0 means unbounded.
func (s *Store) queryRuns(limit int, query string, args ...any) ([]orders.OrderRun, error) {
	if limit > 0 {
		query += fmt.Sprintf(" LIMIT %d", limit)
	}
	rows, err := s.db.Read().Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("listing order runs: %w", err)
	}
	defer rows.Close() //nolint:errcheck
	out := make([]orders.OrderRun, 0)
	for rows.Next() {
		run, err := scanRun(rows.Scan)
		if err != nil {
			return out, fmt.Errorf("decoding order run: %w", err)
		}
		out = append(out, run)
	}
	if err := rows.Err(); err != nil {
		return out, fmt.Errorf("listing order runs: %w", err)
	}
	return out, nil
}

// scanRun decodes one runColumns row into an orders.OrderRun. An unknown
// outcome token fails loudly rather than silently reclassifying the run.
func scanRun(scan func(dest ...any) error) (orders.OrderRun, error) {
	var (
		id, scoped, token string
		created, updated  int64
		openInt           int
		seq               int64
	)
	if err := scan(&id, &scoped, &created, &updated, &openInt, &token, &seq); err != nil {
		return orders.OrderRun{}, err
	}
	outcome, ok := orders.RunOutcomeFromToken(token)
	if !ok {
		return orders.OrderRun{}, fmt.Errorf("order run %s: unknown outcome token %q", id, token)
	}
	run := orders.OrderRun{
		ID:        id,
		Scoped:    scoped,
		Outcome:   outcome,
		CreatedAt: time.Unix(0, created),
		UpdatedAt: time.Unix(0, updated),
		Open:      openInt == 1,
	}
	if seq > 0 {
		run.Cursor = orders.EventCursor(seq)
	}
	return run, nil
}

// uniqueNonEmptyIDs trims, drops empties, and dedupes a close batch,
// mirroring the beads backend's batch hygiene.
func uniqueNonEmptyIDs(ids []string) []string {
	out := make([]string, 0, len(ids))
	seen := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out
}

// StartMaintenance starts the store file's periodic WAL checkpoint +
// slow-cadence VACUUM (the design's maintenance-loop extension: only Dolt
// had maintenance before). Idempotent per handle, same as
// StartRetentionSweeper; the loop stops when the store closes.
func (s *Store) StartMaintenance(checkpointInterval, vacuumInterval time.Duration, warn io.Writer) {
	s.maintenanceOnce.Do(func() {
		s.db.StartMaintenance(checkpointInterval, vacuumInterval, warn)
	})
}
