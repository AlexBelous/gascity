// Package sessionsdb is the embedded-SQLite sessions-class store
// (engdocs/design/infra-class-sqlite-stores.md, Sessions + waits section;
// seam ratification in engdocs/plans/infra-class-sqlite-stores/
// P4-SESSIONS-SEAM-PLAN.md): session lifecycle rows and durable wait rows
// in one file over the shared internal/classdb/core substrate.
//
// Unlike the other class stores, the seam here is the `beads.Store`
// interface itself, narrowed to the audited sessions-class op surface —
// the session domain codec (info_codec, waits, circuit_state) is already
// confined to internal/session, `SetFingerprint` requires the FULL
// open-vocabulary metadata map to round-trip, and session.Manager / the
// worker factory / the API all thread beads.Store handles. So this store
// speaks bead-shaped rows: the `meta` JSON column is AUTHORITATIVE for the
// whole metadata map (arbitrary keys, empty-string values preserved
// verbatim — the observable empty-clear contract and the fingerprint both
// depend on presence fidelity), and the design's hot lifecycle columns are
// derived MIRRORS recomputed from meta inside the same transaction, so
// they can never drift. The design's session_circuit sidecar table is
// deliberately folded into meta (deviation, recorded in the seam plan):
// circuit keys are written through the same SetMetadataBatch vocabulary as
// every other key and read off the same row, so a sidecar would add a
// split/join for zero observable gain.
//
// Wrap-don't-widen: only the audited ops are implemented; graph/work
// concerns (Ready, deps, Tx, tier routing) fail LOUD with ErrUnsupported,
// never silently no-op. Query semantics are byte-identical to the
// in-memory reference by construction: List loads the class rows and
// applies beads.ApplyListQuery — the same filter/sort/limit code every
// other backend converges on.
package sessionsdb

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/classdb/core"
	"github.com/gastownhall/gascity/internal/session"
)

// idPrefix is the reserved sessions-class id prefix. It must stay in
// lockstep with config.ReservedClassPrefix(config.BeadClassSessions) —
// pinned by TestIDPrefixMatchesReservedClassPrefix.
const idPrefix = "gcs"

// ErrUnsupported reports a beads.Store operation outside the audited
// sessions-class surface. The store fails loud instead of silently
// returning empty results so a future caller reaching for graph/work
// behavior on a sessions handle is caught immediately.
var ErrUnsupported = errors.New("sessions class store: operation not supported")

// sessionHotKeys are the metadata keys mirrored into typed columns on the
// sessions table (the design's hot lifecycle columns: reconciler
// projection, create/wake fences, adoption-barrier filter). The mirrors
// are recomputed from the authoritative meta JSON inside every writing
// transaction; reads that need an indexed narrowing use the columns, and
// the full map is always decoded from meta.
var sessionHotKeys = []string{
	"state",
	"session_name",
	"configured_named_identity",
	"pool_slot",
	"generation",
	"instance_token",
	"pending_create_claim",
	"pending_create_started_at",
	"last_woke_at",
}

// waitHotKeys are the metadata keys mirrored on the waits table
// (session_id drives WaitsForSession; state drives the sweep reads).
var waitHotKeys = []string{"session_id", "state"}

func migrations() []core.Migration {
	return []core.Migration{{
		Version: 1,
		DDL: []string{
			`CREATE TABLE IF NOT EXISTS sessions (
				id          TEXT PRIMARY KEY,
				title       TEXT NOT NULL DEFAULT '',
				bead_type   TEXT NOT NULL DEFAULT '',
				status      TEXT NOT NULL DEFAULT 'open',
				assignee    TEXT NOT NULL DEFAULT '',
				description TEXT NOT NULL DEFAULT '',
				created_at  INTEGER NOT NULL,
				updated_at  INTEGER NOT NULL,
				labels      TEXT NOT NULL DEFAULT '[]',
				meta        TEXT NOT NULL DEFAULT '{}',
				state                     TEXT NOT NULL DEFAULT '',
				session_name              TEXT NOT NULL DEFAULT '',
				configured_named_identity TEXT NOT NULL DEFAULT '',
				pool_slot                 TEXT NOT NULL DEFAULT '',
				generation                TEXT NOT NULL DEFAULT '',
				instance_token            TEXT NOT NULL DEFAULT '',
				pending_create_claim      TEXT NOT NULL DEFAULT '',
				pending_create_started_at TEXT NOT NULL DEFAULT '',
				last_woke_at              TEXT NOT NULL DEFAULT ''
			)`,
			`CREATE INDEX IF NOT EXISTS idx_sessions_open ON sessions(status, state)`,
			`CREATE INDEX IF NOT EXISTS idx_sessions_name ON sessions(session_name) WHERE status='open'`,
			`CREATE INDEX IF NOT EXISTS idx_sessions_ident ON sessions(configured_named_identity) WHERE status='open'`,
			`CREATE INDEX IF NOT EXISTS idx_sessions_created ON sessions(created_at)`,
			`CREATE TABLE IF NOT EXISTS waits (
				id          TEXT PRIMARY KEY,
				title       TEXT NOT NULL DEFAULT '',
				bead_type   TEXT NOT NULL DEFAULT '',
				status      TEXT NOT NULL DEFAULT 'open',
				assignee    TEXT NOT NULL DEFAULT '',
				description TEXT NOT NULL DEFAULT '',
				created_at  INTEGER NOT NULL,
				updated_at  INTEGER NOT NULL,
				labels      TEXT NOT NULL DEFAULT '[]',
				meta        TEXT NOT NULL DEFAULT '{}',
				session_id  TEXT NOT NULL DEFAULT '',
				state       TEXT NOT NULL DEFAULT ''
			)`,
			`CREATE INDEX IF NOT EXISTS idx_waits_session ON waits(session_id, status)`,
			`CREATE INDEX IF NOT EXISTS idx_waits_open ON waits(status)`,
			`CREATE INDEX IF NOT EXISTS idx_waits_created ON waits(created_at)`,
			`CREATE TABLE IF NOT EXISTS id_seq (k INTEGER PRIMARY KEY CHECK (k = 1), next INTEGER NOT NULL)`,
			`INSERT OR IGNORE INTO id_seq (k, next) VALUES (1, 0)`,
		},
	}}
}

// rowColumns is the generic bead-row column list both tables share; the
// per-table mirror columns are write-only derivations and never read back
// (meta is authoritative).
const rowColumns = `id, title, bead_type, status, assignee, description, created_at, updated_at, labels, meta`

// Store is the embedded-SQLite sessions-class store. It implements the
// audited beads.Store subset for session and wait beads; resolveSessionStore
// hands it out in place of the work store on migrated cities.
type Store struct {
	db                   *core.DB
	retentionSweeperOnce sync.Once
}

// Interface guard: the class store must satisfy the full beads.Store
// surface (unsupported ops fail loud at call time, not compile time).
var _ beads.Store = (*Store)(nil)

// Open opens (creating and migrating if needed) the sessions store file at
// path. Long-lived callers (the controller) use the default read pool;
// short-lived CLI/hook processes pass core.WithSingleConn.
func Open(path string, opts ...core.Option) (*Store, error) {
	db, err := core.Open(path, migrations(), opts...)
	if err != nil {
		return nil, fmt.Errorf("opening sessions store: %w", err)
	}
	return &Store{db: db}, nil
}

// CloseStore closes the underlying database handles. Idempotent. (Named to
// stay clear of beads.Store.Close, which closes a bead.)
func (s *Store) CloseStore() error { return s.db.Close() }

// Path returns the database file path.
func (s *Store) Path() string { return s.db.Path() }

// IDPrefix returns the reserved sessions-class id prefix ("gcs"). Minted
// ids are "gcs-<seq>"; legacy-prefixed ids (imported bd beads) remain
// valid row keys.
func (s *Store) IDPrefix() string { return idPrefix }

// row is the stored generic bead row shared by both tables.
type row struct {
	id, title, beadType, status, assignee, description string
	createdAt, updatedAt                               int64
	labels, meta                                       string
}

func scanRow(scan func(dest ...any) error) (row, error) {
	var r row
	err := scan(&r.id, &r.title, &r.beadType, &r.status, &r.assignee,
		&r.description, &r.createdAt, &r.updatedAt, &r.labels, &r.meta)
	return r, err
}

func (r row) bead() (beads.Bead, error) {
	var labels []string
	if err := json.Unmarshal([]byte(r.labels), &labels); err != nil {
		return beads.Bead{}, fmt.Errorf("decoding labels for %s: %w", r.id, err)
	}
	var meta map[string]string
	if err := json.Unmarshal([]byte(r.meta), &meta); err != nil {
		return beads.Bead{}, fmt.Errorf("decoding metadata for %s: %w", r.id, err)
	}
	if len(labels) == 0 {
		labels = nil
	}
	if len(meta) == 0 {
		meta = nil
	}
	return beads.Bead{
		ID:          r.id,
		Title:       r.title,
		Type:        r.beadType,
		Status:      r.status,
		Assignee:    r.assignee,
		Description: r.description,
		CreatedAt:   time.Unix(0, r.createdAt),
		UpdatedAt:   time.Unix(0, r.updatedAt),
		Labels:      labels,
		Metadata:    meta,
	}, nil
}

func encodeLabels(labels []string) (string, error) {
	if len(labels) == 0 {
		return "[]", nil
	}
	buf, err := json.Marshal(labels)
	if err != nil {
		return "", fmt.Errorf("encoding labels: %w", err)
	}
	return string(buf), nil
}

func encodeMeta(meta map[string]string) (string, error) {
	if len(meta) == 0 {
		return "{}", nil
	}
	buf, err := json.Marshal(meta)
	if err != nil {
		return "", fmt.Errorf("encoding metadata: %w", err)
	}
	return string(buf), nil
}

// isWaitRow reports whether a bead belongs in the waits table. The dispatch
// invariant: the waits table holds exactly the wait-typed rows, so a
// Type-filtered List can narrow to one table safely.
func isWaitRow(beadType string) bool { return session.IsWaitBeadType(beadType) }

func tableFor(beadType string) string {
	if isWaitRow(beadType) {
		return "waits"
	}
	return "sessions"
}

// hotValues returns the mirror-column values for a table, derived from the
// authoritative metadata map.
func hotValues(table string, meta map[string]string) []any {
	keys := sessionHotKeys
	if table == "waits" {
		keys = waitHotKeys
	}
	out := make([]any, len(keys))
	for i, k := range keys {
		out[i] = meta[k]
	}
	return out
}

func hotAssignments(table string) string {
	keys := sessionHotKeys
	if table == "waits" {
		keys = waitHotKeys
	}
	assign := ""
	for _, k := range keys {
		assign += ", " + k + " = ?"
	}
	return assign
}

func hotColumnList(table string) (cols, placeholders string) {
	keys := sessionHotKeys
	if table == "waits" {
		keys = waitHotKeys
	}
	for _, k := range keys {
		cols += ", " + k
		placeholders += ", ?"
	}
	return cols, placeholders
}

// rejectUnsupportedFields fails a Create carrying graph/work/tier fields
// the sessions class never uses and this store does not persist.
func rejectUnsupportedFields(b beads.Bead) error {
	switch {
	case b.Priority != nil:
		return fmt.Errorf("%w: Create with Priority", ErrUnsupported)
	case b.ParentID != "":
		return fmt.Errorf("%w: Create with ParentID", ErrUnsupported)
	case len(b.Needs) > 0 || len(b.Dependencies) > 0:
		return fmt.Errorf("%w: Create with dependencies", ErrUnsupported)
	case b.Ephemeral || b.NoHistory:
		return fmt.Errorf("%w: Create with storage-tier routing", ErrUnsupported)
	case b.DeferUntil != nil:
		return fmt.Errorf("%w: Create with DeferUntil", ErrUnsupported)
	case b.From != "" || b.Ref != "":
		return fmt.Errorf("%w: Create with From/Ref", ErrUnsupported)
	}
	return nil
}

// Create persists a new session or wait bead. Interface contract semantics
// (memstore-aligned): Status is forced open, an empty Type defaults to
// "task", CreatedAt/UpdatedAt are stamped now. An explicit non-empty ID is
// honored (bd parity — the pool create site pre-allocates ids); otherwise
// an id is minted as "gcs-<seq>" in the same transaction as the insert.
// Imports that must preserve status/clocks verbatim use ImportBead.
func (s *Store) Create(b beads.Bead) (beads.Bead, error) {
	if err := rejectUnsupportedFields(b); err != nil {
		return beads.Bead{}, err
	}
	if b.Type == "" {
		b.Type = "task"
	}
	b.Status = "open"
	now := time.Now()
	b.CreatedAt = time.Unix(0, now.UnixNano())
	b.UpdatedAt = b.CreatedAt

	labelsJSON, err := encodeLabels(b.Labels)
	if err != nil {
		return beads.Bead{}, err
	}
	metaJSON, err := encodeMeta(b.Metadata)
	if err != nil {
		return beads.Bead{}, err
	}
	table := tableFor(b.Type)
	hotCols, hotPh := hotColumnList(table)
	err = s.db.Write(context.Background(), func(tx *sql.Tx) error {
		if b.ID == "" {
			var next int64
			if err := tx.QueryRow(`UPDATE id_seq SET next = next + 1 WHERE k = 1 RETURNING next`).Scan(&next); err != nil {
				return fmt.Errorf("minting session id: %w", err)
			}
			b.ID = fmt.Sprintf("%s-%d", idPrefix, next)
		}
		args := []any{
			b.ID, b.Title, b.Type, b.Status, b.Assignee, b.Description,
			b.CreatedAt.UnixNano(), b.UpdatedAt.UnixNano(), labelsJSON, metaJSON,
		}
		args = append(args, hotValues(table, b.Metadata)...)
		_, err := tx.Exec(`INSERT INTO `+table+` (`+rowColumns+hotCols+`) VALUES (?,?,?,?,?,?,?,?,?,?`+hotPh+`)`, args...)
		if err != nil {
			return fmt.Errorf("creating %s row %s: %w", table, b.ID, err)
		}
		return nil
	})
	if err != nil {
		return beads.Bead{}, err
	}
	return b, nil
}

// ImportBead inserts a bead VERBATIM — id, type (empty allowed), status,
// clocks, labels, metadata all preserved — for the bd→sqlite migration and
// the straggler re-import. INSERT OR IGNORE: re-running an interrupted
// import never duplicates or overwrites a row the class store already owns.
// Reports whether the row was inserted.
func (s *Store) ImportBead(b beads.Bead) (bool, error) {
	if b.ID == "" {
		return false, fmt.Errorf("importing session-class bead: empty id")
	}
	labelsJSON, err := encodeLabels(b.Labels)
	if err != nil {
		return false, err
	}
	metaJSON, err := encodeMeta(b.Metadata)
	if err != nil {
		return false, err
	}
	status := b.Status
	if status == "" {
		status = "open"
	}
	createdAt := b.CreatedAt
	if createdAt.IsZero() {
		createdAt = time.Now()
	}
	updatedAt := b.UpdatedAt
	if updatedAt.IsZero() {
		updatedAt = createdAt
	}
	table := tableFor(b.Type)
	hotCols, hotPh := hotColumnList(table)
	inserted := false
	err = s.db.Write(context.Background(), func(tx *sql.Tx) error {
		args := []any{
			b.ID, b.Title, b.Type, status, b.Assignee, b.Description,
			createdAt.UnixNano(), updatedAt.UnixNano(), labelsJSON, metaJSON,
		}
		args = append(args, hotValues(table, b.Metadata)...)
		res, err := tx.Exec(`INSERT OR IGNORE INTO `+table+` (`+rowColumns+hotCols+`) VALUES (?,?,?,?,?,?,?,?,?,?`+hotPh+`)`, args...)
		if err != nil {
			return fmt.Errorf("importing %s row %s: %w", table, b.ID, err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return err
		}
		inserted = n > 0
		return nil
	})
	return inserted, err
}

// DeleteAllRows clears both tables (id_seq untouched, so minted ids never
// recycle). It is the migration's reset step: an interrupted pre-marker
// import retries from the bd truth without resurrecting rows a previous
// attempt imported (the P2/P3 no-resurrection lesson).
func (s *Store) DeleteAllRows() error {
	return s.db.Write(context.Background(), func(tx *sql.Tx) error {
		if _, err := tx.Exec(`DELETE FROM sessions`); err != nil {
			return fmt.Errorf("resetting sessions rows: %w", err)
		}
		if _, err := tx.Exec(`DELETE FROM waits`); err != nil {
			return fmt.Errorf("resetting wait rows: %w", err)
		}
		return nil
	})
}

// getRowTx fetches a row by id from either table inside a transaction,
// reporting which table holds it.
func getRowTx(tx *sql.Tx, id string) (row, string, error) {
	for _, table := range []string{"sessions", "waits"} {
		r, err := scanRow(tx.QueryRow(`SELECT `+rowColumns+` FROM `+table+` WHERE id = ?`, id).Scan)
		if err == nil {
			return r, table, nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return row{}, "", err
		}
	}
	return row{}, "", fmt.Errorf("bead %q: %w", id, beads.ErrNotFound)
}

// Get retrieves a bead by ID from either table. Returns a wrapped
// beads.ErrNotFound when the id is absent.
func (s *Store) Get(id string) (beads.Bead, error) {
	for _, table := range []string{"sessions", "waits"} {
		r, err := scanRow(s.db.Read().QueryRow(`SELECT `+rowColumns+` FROM `+table+` WHERE id = ?`, id).Scan)
		if err == nil {
			return r.bead()
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return beads.Bead{}, err
		}
	}
	return beads.Bead{}, fmt.Errorf("bead %q: %w", id, beads.ErrNotFound)
}

// writeRow persists a mutated row back to its table, recomputing the hot
// mirror columns from the authoritative metadata map. The single write
// chokepoint every mutation funnels through, so mirrors cannot drift.
func writeRow(tx *sql.Tx, table string, b beads.Bead) error {
	labelsJSON, err := encodeLabels(b.Labels)
	if err != nil {
		return err
	}
	metaJSON, err := encodeMeta(b.Metadata)
	if err != nil {
		return err
	}
	args := []any{
		b.Title, b.Type, b.Status, b.Assignee, b.Description,
		b.UpdatedAt.UnixNano(), labelsJSON, metaJSON,
	}
	args = append(args, hotValues(table, b.Metadata)...)
	args = append(args, b.ID)
	_, err = tx.Exec(`UPDATE `+table+` SET title = ?, bead_type = ?, status = ?, assignee = ?, description = ?, updated_at = ?, labels = ?, meta = ?`+hotAssignments(table)+` WHERE id = ?`, args...)
	if err != nil {
		return fmt.Errorf("updating %s row %s: %w", table, b.ID, err)
	}
	return nil
}

// mutate loads the row for id, applies fn to its bead form, and writes it
// back in the same transaction. fn returning false skips the write (no-op).
func (s *Store) mutate(id string, fn func(b *beads.Bead) (bool, error)) error {
	return s.db.Write(context.Background(), func(tx *sql.Tx) error {
		r, table, err := getRowTx(tx, id)
		if err != nil {
			return err
		}
		b, err := r.bead()
		if err != nil {
			return err
		}
		write, err := fn(&b)
		if err != nil || !write {
			return err
		}
		b.UpdatedAt = time.Now()
		return writeRow(tx, table, b)
	})
}

// Update modifies fields of an existing bead, mirroring the in-memory
// reference semantics field for field (labels append verbatim, RemoveLabels
// filters, metadata merges). Priority and ParentID writes are outside the
// sessions-class surface and fail loud. A Type change may re-classify the
// row's table (the empty-type repair heals toward "session"; a wait never
// changes type), handled by delete+reinsert in the same transaction.
func (s *Store) Update(id string, opts beads.UpdateOpts) error {
	if opts.Priority != nil {
		return fmt.Errorf("%w: Update with Priority", ErrUnsupported)
	}
	if opts.ParentID != nil {
		return fmt.Errorf("%w: Update with ParentID", ErrUnsupported)
	}
	return s.db.Write(context.Background(), func(tx *sql.Tx) error {
		r, table, err := getRowTx(tx, id)
		if err != nil {
			return fmt.Errorf("updating bead %q: %w", id, beads.ErrNotFound)
		}
		b, err := r.bead()
		if err != nil {
			return err
		}
		if opts.Title != nil {
			b.Title = *opts.Title
		}
		if opts.Status != nil {
			b.Status = *opts.Status
		}
		if opts.Description != nil {
			b.Description = *opts.Description
		}
		if opts.Assignee != nil {
			b.Assignee = *opts.Assignee
		}
		if opts.Type != nil {
			b.Type = *opts.Type
		}
		if len(opts.Metadata) > 0 {
			if b.Metadata == nil {
				b.Metadata = make(map[string]string, len(opts.Metadata))
			}
			for k, v := range opts.Metadata {
				b.Metadata[k] = v
			}
		}
		if len(opts.Labels) > 0 {
			b.Labels = append(b.Labels, opts.Labels...)
		}
		if len(opts.RemoveLabels) > 0 {
			remove := make(map[string]bool, len(opts.RemoveLabels))
			for _, rl := range opts.RemoveLabels {
				remove[rl] = true
			}
			filtered := b.Labels[:0]
			for _, l := range b.Labels {
				if !remove[l] {
					filtered = append(filtered, l)
				}
			}
			b.Labels = filtered
		}
		b.UpdatedAt = time.Now()
		if newTable := tableFor(b.Type); newTable != table {
			if _, err := tx.Exec(`DELETE FROM `+table+` WHERE id = ?`, id); err != nil {
				return fmt.Errorf("reclassifying row %s: %w", id, err)
			}
			hotCols, hotPh := hotColumnList(newTable)
			labelsJSON, lerr := encodeLabels(b.Labels)
			if lerr != nil {
				return lerr
			}
			metaJSON, merr := encodeMeta(b.Metadata)
			if merr != nil {
				return merr
			}
			args := []any{
				b.ID, b.Title, b.Type, b.Status, b.Assignee, b.Description,
				b.CreatedAt.UnixNano(), b.UpdatedAt.UnixNano(), labelsJSON, metaJSON,
			}
			args = append(args, hotValues(newTable, b.Metadata)...)
			_, err := tx.Exec(`INSERT INTO `+newTable+` (`+rowColumns+hotCols+`) VALUES (?,?,?,?,?,?,?,?,?,?`+hotPh+`)`, args...)
			if err != nil {
				return fmt.Errorf("reclassifying row %s into %s: %w", id, newTable, err)
			}
			return nil
		}
		return writeRow(tx, table, b)
	})
}

// Close sets a bead's status to "closed". Closing an already-closed bead
// is a no-op; an absent id returns a wrapped beads.ErrNotFound.
func (s *Store) Close(id string) error {
	return s.mutate(id, func(b *beads.Bead) (bool, error) {
		if b.Status == "closed" {
			return false, nil
		}
		b.Status = "closed"
		return true, nil
	})
}

// Reopen sets a closed bead's status back to "open".
func (s *Store) Reopen(id string) error {
	return s.mutate(id, func(b *beads.Bead) (bool, error) {
		if b.Status == "open" {
			return false, nil
		}
		b.Status = "open"
		return true, nil
	})
}

// CloseAll closes multiple beads in one transaction, merging metadata onto
// each row actually closed. Already-closed and absent ids are skipped.
// Returns the number of beads closed.
func (s *Store) CloseAll(ids []string, metadata map[string]string) (int, error) {
	closed := 0
	err := s.db.Write(context.Background(), func(tx *sql.Tx) error {
		for _, id := range ids {
			r, table, err := getRowTx(tx, id)
			if err != nil {
				if errors.Is(err, beads.ErrNotFound) {
					continue
				}
				return err
			}
			b, err := r.bead()
			if err != nil {
				return err
			}
			if b.Status == "closed" {
				continue
			}
			b.Status = "closed"
			if len(metadata) > 0 {
				if b.Metadata == nil {
					b.Metadata = make(map[string]string, len(metadata))
				}
				for k, v := range metadata {
					b.Metadata[k] = v
				}
			}
			b.UpdatedAt = time.Now()
			if err := writeRow(tx, table, b); err != nil {
				return err
			}
			closed++
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return closed, nil
}

// SetMetadata sets one metadata key on a bead.
func (s *Store) SetMetadata(id, key, value string) error {
	return s.SetMetadataBatch(id, map[string]string{key: value})
}

// SetMetadataBatch merges the key-value pairs onto the bead's metadata in
// ONE transaction — the genuinely atomic batch the design promises (bd/exec
// decompose this per key). Empty-string values are stored verbatim, never
// deleted: presence fidelity is what SetFingerprint and the shadow diff
// hash.
func (s *Store) SetMetadataBatch(id string, kvs map[string]string) error {
	if len(kvs) == 0 {
		return nil
	}
	return s.mutate(id, func(b *beads.Bead) (bool, error) {
		if b.Metadata == nil {
			b.Metadata = make(map[string]string, len(kvs))
		}
		for k, v := range kvs {
			b.Metadata[k] = v
		}
		return true, nil
	})
}

// Delete permanently removes a bead row — the retention primitive that
// cannot exist on bd today (closed-session purge TTL, slice 4).
func (s *Store) Delete(id string) error {
	return s.db.Write(context.Background(), func(tx *sql.Tx) error {
		_, table, err := getRowTx(tx, id)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(`DELETE FROM `+table+` WHERE id = ?`, id); err != nil {
			return fmt.Errorf("deleting %s row %s: %w", table, id, err)
		}
		return nil
	})
}

// List returns beads matching the query with semantics byte-identical to
// the canonical in-memory reference: the class rows are loaded (narrowed to
// one table when the Type filter decides it, and status-narrowed in SQL)
// and beads.ApplyListQuery applies the shared filter/sort/limit/seek code.
func (s *Store) List(query beads.ListQuery) ([]beads.Bead, error) {
	if err := query.Validate(); err != nil {
		return nil, err
	}
	if !query.HasFilter() && !query.AllowScan {
		return nil, beads.ErrQueryRequiresScan
	}
	tables := []string{"sessions", "waits"}
	if query.Type != "" {
		if isWaitRow(query.Type) {
			tables = []string{"waits"}
		} else {
			tables = []string{"sessions"}
		}
	}
	where, args := "", []any(nil)
	switch {
	case query.Status != "":
		where = ` WHERE status = ?`
		args = []any{query.Status}
	case !query.IncludeClosed:
		where = ` WHERE status != 'closed'`
	}
	var out []beads.Bead
	for _, table := range tables {
		rows, err := s.db.Read().Query(`SELECT `+rowColumns+` FROM `+table+where+` ORDER BY created_at ASC, id ASC`, args...)
		if err != nil {
			return nil, fmt.Errorf("listing %s rows: %w", table, err)
		}
		for rows.Next() {
			r, err := scanRow(rows.Scan)
			if err != nil {
				_ = rows.Close()
				return nil, err
			}
			b, err := r.bead()
			if err != nil {
				_ = rows.Close()
				return nil, err
			}
			out = append(out, b)
		}
		if err := rows.Close(); err != nil {
			return nil, err
		}
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}
	return beads.ApplyListQuery(out, query), nil
}

// ListOpen returns non-closed beads, or beads with the given status.
func (s *Store) ListOpen(status ...string) ([]beads.Bead, error) {
	q := beads.ListQuery{AllowScan: true}
	if len(status) > 0 {
		q.Status = status[0]
	}
	return s.List(q)
}

// ListByLabel returns beads carrying the exact label.
func (s *Store) ListByLabel(label string, limit int, opts ...beads.QueryOpt) ([]beads.Bead, error) {
	return s.List(beads.ListQuery{
		Label:         label,
		Limit:         limit,
		IncludeClosed: beads.HasOpt(opts, beads.IncludeClosed),
		TierMode:      beads.TierModeFromOpts(opts),
	})
}

// ListByMetadata returns beads whose metadata contains all filter pairs.
func (s *Store) ListByMetadata(filters map[string]string, limit int, opts ...beads.QueryOpt) ([]beads.Bead, error) {
	return s.List(beads.ListQuery{
		Metadata:      filters,
		Limit:         limit,
		IncludeClosed: beads.HasOpt(opts, beads.IncludeClosed),
		TierMode:      beads.TierModeFromOpts(opts),
	})
}

// Ping verifies the store is operational.
func (s *Store) Ping() error { return s.db.Ping() }

// SweepClosedBefore deletes closed session and wait rows whose last write
// (updated_at) precedes cutoff — the design's net-new closed-session purge
// (default 7d TTL), a DELETE path that cannot exist on bd. Open rows are
// never touched. Returns the number of rows deleted.
func (s *Store) SweepClosedBefore(ctx context.Context, cutoff time.Time) (int, error) {
	deleted := 0
	err := s.db.Write(ctx, func(tx *sql.Tx) error {
		for _, table := range []string{"sessions", "waits"} {
			res, err := tx.Exec(`DELETE FROM `+table+` WHERE status = 'closed' AND updated_at < ?`, cutoff.UnixNano())
			if err != nil {
				return fmt.Errorf("sweeping closed %s rows: %w", table, err)
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
		return 0, err
	}
	return deleted, nil
}

// StartRetentionSweeper starts the store's own periodic closed-row
// retention sweep (the design's "stores own retention": the only
// pre-existing delete path was reaper.sh's raw Dolt SQL, which no-ops on a
// routed city). Idempotent per handle — the first call starts the loop,
// later calls no-op, so controller rebuilds over the process-shared handle
// never stack tickers. The loop stops when the store closes; sweep
// failures are reported to warn (nil discards them).
func (s *Store) StartRetentionSweeper(interval, ttl time.Duration, warn io.Writer) {
	s.retentionSweeperOnce.Do(func() {
		s.db.StartSweeper(interval, func(ctx context.Context) {
			if _, err := s.SweepClosedBefore(ctx, time.Now().Add(-ttl)); err != nil && warn != nil {
				fmt.Fprintf(warn, "sessions retention sweep: %v\n", err) //nolint:errcheck // best-effort warning
			}
		})
	})
}

// --- Outside the audited sessions-class surface: fail loud. ---

// Ready is a work/graph concern; sessions-class rows are never claimable.
func (s *Store) Ready(...beads.ReadyQuery) ([]beads.Bead, error) {
	return nil, fmt.Errorf("%w: Ready", ErrUnsupported)
}

// Children is a graph concern; sessions-class rows have no parents.
func (s *Store) Children(string, ...beads.QueryOpt) ([]beads.Bead, error) {
	return nil, fmt.Errorf("%w: Children", ErrUnsupported)
}

// ListByAssignee is a work concern; session ownership reads use ListAll.
func (s *Store) ListByAssignee(string, string, int) ([]beads.Bead, error) {
	return nil, fmt.Errorf("%w: ListByAssignee", ErrUnsupported)
}

// Tx is unused on sessions-class paths; batch writes use CloseAll and
// SetMetadataBatch, which are natively transactional here.
func (s *Store) Tx(string, func(tx beads.Tx) error) error {
	return fmt.Errorf("%w: Tx", ErrUnsupported)
}

// DepAdd is a graph concern; waits carry dep_ids as metadata by design.
func (s *Store) DepAdd(string, string, string) error {
	return fmt.Errorf("%w: DepAdd", ErrUnsupported)
}

// DepRemove is a graph concern.
func (s *Store) DepRemove(string, string) error {
	return fmt.Errorf("%w: DepRemove", ErrUnsupported)
}

// DepList is a graph concern; sessions-class rows never carry dep edges.
func (s *Store) DepList(string, string) ([]beads.Dep, error) {
	return nil, fmt.Errorf("%w: DepList", ErrUnsupported)
}
