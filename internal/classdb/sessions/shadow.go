package sessionsdb

// The sessions shadow-write gate (design "Migration & cutover" row 4:
// 24-48h shadow-write with zero-discrepancy diff before the flip). While
// [beads.classes.sessions] shadow=true and the backend stays bd, the
// resolver wraps the bd-backed session store in Shadow: bd stays
// authoritative for EVERY read and write, and each committed sessions-class
// write is replayed onto the embedded class store, best-effort — a shadow
// failure logs and never fails the primary op, because shadow is an
// observability stage, not a second authority. DiffAgainstPrimary is the
// zero-discrepancy oracle the soak watches (surfaced via gc doctor).
//
// Tee mechanics: Create tees by importing the primary's echo VERBATIM
// (same id, clocks, status), so the shadow's rows are keyed by the bd ids
// the flip migration will later preserve. Id-keyed ops replay the same
// mutation when the shadow holds the row; a miss (a row created before the
// shadow was seeded, or by a process without the knob) converges via an
// on-miss import of the row's post-op state. Only rows that
// coordclass.Classify routes to ClassSessions are ever teed — a work-class
// write straying through a session handle must not copy work beads into
// the class store.

import (
	"errors"
	"log"
	"os"
	"path/filepath"
	"sync"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/coordclass"
	"github.com/gastownhall/gascity/internal/session"
)

// StoreDir returns the per-class embedded-store directory for a city.
func StoreDir(cityPath string) string {
	return filepath.Join(cityPath, ".gc", "store")
}

// StorePath returns the sessions class store file for a city.
func StorePath(cityPath string) string {
	return filepath.Join(StoreDir(cityPath), "sessions.db")
}

// sharedHandles is the process-wide cache of open sessions class stores,
// one per database path (the messaging precedent: the controller keeps a
// persistent handle; CLI one-shots exit promptly and the G0 SIGKILL gate
// proves durability never depends on a clean close).
var sharedHandles struct {
	mu     sync.Mutex
	byPath map[string]*Store
}

// SharedStoreFor returns the process-shared handle for a city's sessions
// class store, opening (and migrating the schema) on first use.
func SharedStoreFor(cityPath string) (*Store, error) {
	path := StorePath(cityPath)
	sharedHandles.mu.Lock()
	defer sharedHandles.mu.Unlock()
	if st, ok := sharedHandles.byPath[path]; ok {
		return st, nil
	}
	if err := os.MkdirAll(StoreDir(cityPath), 0o755); err != nil {
		return nil, err
	}
	st, err := Open(path)
	if err != nil {
		return nil, err
	}
	if sharedHandles.byPath == nil {
		sharedHandles.byPath = make(map[string]*Store)
	}
	sharedHandles.byPath[path] = st
	return st, nil
}

// Shadow wraps a bd-backed sessions-class store, teeing committed
// sessions-class writes onto the embedded class store. Reads and errors are
// the primary's, byte-identical.
type Shadow struct {
	primary beads.Store
	class   *Store
}

// cachedLister is the optional read-model cache capability
// (CachingStore.CachedList) that the session ListAll CacheFirst tier and
// the API read model assert on the resolved store. The wrapper must forward
// it: hiding it would silently downgrade every warm dashboard read to a
// direct bd union for the duration of the shadow soak.
type cachedLister interface {
	CachedList(beads.ListQuery) ([]beads.Bead, bool)
}

// shadowCached is Shadow plus the forwarded CachedList capability, returned
// by NewShadow when the primary implements it.
type shadowCached struct {
	*Shadow
	lister cachedLister
}

// CachedList forwards the read-model cache peek to the primary store.
func (s shadowCached) CachedList(q beads.ListQuery) ([]beads.Bead, bool) {
	return s.lister.CachedList(q)
}

// NewShadow wraps primary with the sessions shadow tee onto class. The
// returned store forwards the primary's CachedList capability when present.
func NewShadow(primary beads.Store, class *Store) beads.Store {
	sh := &Shadow{primary: primary, class: class}
	if lister, ok := primary.(cachedLister); ok {
		return shadowCached{Shadow: sh, lister: lister}
	}
	return sh
}

// Interface guard.
var _ beads.Store = (*Shadow)(nil)

// ShadowPrimary exposes the wrapped authoritative store. cmd/gc's
// identity-keyed lookups (the messaging repair-city registry) and handle
// closers unwrap through it, so a shadow-wrapped handle keys and closes
// exactly like the bd store it fronts. The class-store handle is
// deliberately NOT closed through this path — it is the process-shared
// SharedStoreFor handle.
func (s *Shadow) ShadowPrimary() beads.Store { return s.primary }

func shadowLog(op, id string, err error) {
	log.Printf("sessions shadow: %s %s: %v (bd stays authoritative; the diff will surface divergence)", op, id, err)
}

// classHas reports whether the class store holds a row for id.
func (s *Shadow) classHas(id string) bool {
	_, err := s.class.Get(id)
	return err == nil
}

// teeMissImport converges a shadow miss: fetch the row's post-op state from
// the primary and import it verbatim when it is sessions-class.
func (s *Shadow) teeMissImport(op, id string) {
	b, err := s.primary.Get(id)
	if err != nil {
		shadowLog(op+" (miss import)", id, err)
		return
	}
	if coordclass.Classify(b) != coordclass.ClassSessions {
		return
	}
	if _, err := s.class.ImportBead(b); err != nil {
		shadowLog(op+" (miss import)", id, err)
	}
}

// teeReplay replays fn onto the class store when it holds id, converging
// via an on-miss import otherwise.
func (s *Shadow) teeReplay(op, id string, fn func() error) {
	if !s.classHas(id) {
		s.teeMissImport(op, id)
		return
	}
	if err := fn(); err != nil {
		shadowLog(op, id, err)
	}
}

// Create persists via the primary and tees the committed echo verbatim
// (same id/clocks/status) when it classifies as sessions-class.
func (s *Shadow) Create(b beads.Bead) (beads.Bead, error) {
	created, err := s.primary.Create(b)
	if err != nil {
		return created, err
	}
	if coordclass.Classify(created) == coordclass.ClassSessions {
		if _, err := s.class.ImportBead(created); err != nil {
			shadowLog("create", created.ID, err)
		}
	}
	return created, nil
}

// Get reads from the primary.
func (s *Shadow) Get(id string) (beads.Bead, error) { return s.primary.Get(id) }

// Update applies via the primary, then replays onto the shadow.
func (s *Shadow) Update(id string, opts beads.UpdateOpts) error {
	if err := s.primary.Update(id, opts); err != nil {
		return err
	}
	s.teeReplay("update", id, func() error { return s.class.Update(id, opts) })
	return nil
}

// Close closes via the primary, then replays onto the shadow.
func (s *Shadow) Close(id string) error {
	if err := s.primary.Close(id); err != nil {
		return err
	}
	s.teeReplay("close", id, func() error { return s.class.Close(id) })
	return nil
}

// Reopen reopens via the primary, then replays onto the shadow.
func (s *Shadow) Reopen(id string) error {
	if err := s.primary.Reopen(id); err != nil {
		return err
	}
	s.teeReplay("reopen", id, func() error { return s.class.Reopen(id) })
	return nil
}

// CloseAll closes via the primary, then replays per id onto the shadow
// (the class CloseAll skips ids it does not hold, so misses converge via
// the import path first).
func (s *Shadow) CloseAll(ids []string, metadata map[string]string) (int, error) {
	n, err := s.primary.CloseAll(ids, metadata)
	if err != nil {
		return n, err
	}
	for _, id := range ids {
		if !s.classHas(id) {
			s.teeMissImport("close-all", id)
		}
	}
	if _, err := s.class.CloseAll(ids, metadata); err != nil {
		shadowLog("close-all", "", err)
	}
	return n, nil
}

// SetMetadata writes via the primary, then replays onto the shadow.
func (s *Shadow) SetMetadata(id, key, value string) error {
	if err := s.primary.SetMetadata(id, key, value); err != nil {
		return err
	}
	s.teeReplay("set-metadata", id, func() error { return s.class.SetMetadata(id, key, value) })
	return nil
}

// SetMetadataBatch writes via the primary, then replays onto the shadow.
func (s *Shadow) SetMetadataBatch(id string, kvs map[string]string) error {
	if err := s.primary.SetMetadataBatch(id, kvs); err != nil {
		return err
	}
	s.teeReplay("set-metadata-batch", id, func() error { return s.class.SetMetadataBatch(id, kvs) })
	return nil
}

// Delete deletes via the primary, then removes the shadow row (a missing
// shadow row is fine — nothing to converge for a deleted bead).
func (s *Shadow) Delete(id string) error {
	if err := s.primary.Delete(id); err != nil {
		return err
	}
	if err := s.class.Delete(id); err != nil && !errors.Is(err, beads.ErrNotFound) {
		shadowLog("delete", id, err)
	}
	return nil
}

// List reads from the primary.
func (s *Shadow) List(q beads.ListQuery) ([]beads.Bead, error) { return s.primary.List(q) }

// ListOpen reads from the primary.
func (s *Shadow) ListOpen(status ...string) ([]beads.Bead, error) {
	return s.primary.ListOpen(status...)
}

// Ready reads from the primary.
func (s *Shadow) Ready(q ...beads.ReadyQuery) ([]beads.Bead, error) { return s.primary.Ready(q...) }

// Children reads from the primary.
func (s *Shadow) Children(parentID string, opts ...beads.QueryOpt) ([]beads.Bead, error) {
	return s.primary.Children(parentID, opts...)
}

// ListByLabel reads from the primary.
func (s *Shadow) ListByLabel(label string, limit int, opts ...beads.QueryOpt) ([]beads.Bead, error) {
	return s.primary.ListByLabel(label, limit, opts...)
}

// ListByAssignee reads from the primary.
func (s *Shadow) ListByAssignee(assignee, status string, limit int) ([]beads.Bead, error) {
	return s.primary.ListByAssignee(assignee, status, limit)
}

// ListByMetadata reads from the primary.
func (s *Shadow) ListByMetadata(filters map[string]string, limit int, opts ...beads.QueryOpt) ([]beads.Bead, error) {
	return s.primary.ListByMetadata(filters, limit, opts...)
}

// Tx delegates to the primary (session paths never call it; the tee does
// not attempt to mirror arbitrary transactions).
func (s *Shadow) Tx(commitMsg string, fn func(tx beads.Tx) error) error {
	return s.primary.Tx(commitMsg, fn)
}

// Ping delegates to the primary.
func (s *Shadow) Ping() error { return s.primary.Ping() }

// DepAdd delegates to the primary.
func (s *Shadow) DepAdd(issueID, dependsOnID, depType string) error {
	return s.primary.DepAdd(issueID, dependsOnID, depType)
}

// DepRemove delegates to the primary.
func (s *Shadow) DepRemove(issueID, dependsOnID string) error {
	return s.primary.DepRemove(issueID, dependsOnID)
}

// DepList delegates to the primary.
func (s *Shadow) DepList(id, direction string) ([]beads.Dep, error) {
	return s.primary.DepList(id, direction)
}

// ExportSessionClassBeads gathers every sessions-class bead from a bd
// store: the session Type+Label union legs plus the wait-typed legs,
// classify-filtered (a graph gate or a work chore never crosses). Shared
// by the shadow seed, the shadow diff, and cmd/gc's migration/residue
// sweep — one collector, one selection rule.
func ExportSessionClassBeads(primary beads.Store, includeClosed bool) (map[string]beads.Bead, error) {
	legs := []beads.ListQuery{
		{Type: session.BeadType, IncludeClosed: includeClosed},
		{Label: session.LabelSession, IncludeClosed: includeClosed},
		{Type: session.WaitBeadType, IncludeClosed: includeClosed},
		{Type: session.LegacyWaitBeadType, IncludeClosed: includeClosed},
	}
	rows := make(map[string]beads.Bead)
	for _, leg := range legs {
		found, err := primary.List(leg)
		if err != nil && !beads.IsPartialResult(err) {
			return nil, err
		}
		for _, b := range found {
			if _, dup := rows[b.ID]; dup {
				continue
			}
			if coordclass.Classify(b) != coordclass.ClassSessions {
				continue
			}
			rows[b.ID] = b
		}
	}
	return rows, nil
}

// SeedFromPrimary resets the class store and imports every current
// sessions-class bead (open and closed) from the primary verbatim. Run at
// controller boot when the shadow gate is on, so the diff starts from a
// converged baseline instead of reporting every pre-existing session as
// missing. Returns the number of rows imported.
func (s *Store) SeedFromPrimary(primary beads.Store) (int, error) {
	rows, err := ExportSessionClassBeads(primary, true)
	if err != nil {
		return 0, err
	}
	if err := s.DeleteAllRows(); err != nil {
		return 0, err
	}
	imported := 0
	for _, b := range rows {
		inserted, err := s.ImportBead(b)
		if err != nil {
			return imported, err
		}
		if inserted {
			imported++
		}
	}
	return imported, nil
}

// ShadowMismatch is one diverged row in a shadow diff.
type ShadowMismatch struct {
	ID     string
	Detail string
}

// ShadowDiff is the zero-discrepancy oracle's result: OPEN rows the shadow
// is missing, OPEN shadow rows the primary lacks, and rows present in both
// whose stored fields differ. Closed rows are out of scope — retention
// diverges between the backends by design.
type ShadowDiff struct {
	Compared   int
	Missing    []string
	Extra      []string
	Mismatched []ShadowMismatch
}

// Clean reports a zero-discrepancy diff.
func (d ShadowDiff) Clean() bool {
	return len(d.Missing) == 0 && len(d.Extra) == 0 && len(d.Mismatched) == 0
}

// DiffAgainstPrimary compares the shadow's OPEN rows against the primary's
// OPEN sessions-class beads: row sets by id, and per-row Type, Title,
// Status, Assignee, label set, and the full metadata map. Clocks are
// excluded (UpdatedAt advances independently). Callers on a live city
// should re-diff discrepant ids once to filter in-flight write races.
func (s *Store) DiffAgainstPrimary(primary beads.Store) (ShadowDiff, error) {
	want, err := ExportSessionClassBeads(primary, false)
	if err != nil {
		return ShadowDiff{}, err
	}
	got := make(map[string]beads.Bead)
	shadowRows, err := s.List(beads.ListQuery{AllowScan: true})
	if err != nil {
		return ShadowDiff{}, err
	}
	for _, b := range shadowRows {
		got[b.ID] = b
	}

	diff := ShadowDiff{Compared: len(want)}
	for id, wb := range want {
		gb, ok := got[id]
		if !ok {
			diff.Missing = append(diff.Missing, id)
			continue
		}
		if detail := describeBeadDelta(wb, gb); detail != "" {
			diff.Mismatched = append(diff.Mismatched, ShadowMismatch{ID: id, Detail: detail})
		}
	}
	for id := range got {
		if _, ok := want[id]; !ok {
			diff.Extra = append(diff.Extra, id)
		}
	}
	return diff, nil
}

// describeBeadDelta returns a human-readable description of the first
// field-level divergence between the primary and shadow forms of a row, or
// "" when they agree on every compared field.
func describeBeadDelta(want, got beads.Bead) string {
	switch {
	case want.Type != got.Type:
		return "type " + got.Type + " != " + want.Type
	case want.Title != got.Title:
		return "title diverged"
	case want.Status != got.Status:
		return "status " + got.Status + " != " + want.Status
	case want.Assignee != got.Assignee:
		return "assignee diverged"
	}
	if !sameLabelSet(want.Labels, got.Labels) {
		return "label set diverged"
	}
	if len(want.Metadata) != len(got.Metadata) {
		return "metadata key count diverged"
	}
	for k, v := range want.Metadata {
		gv, ok := got.Metadata[k]
		if !ok {
			return "metadata key " + k + " missing"
		}
		if gv != v {
			return "metadata key " + k + " diverged"
		}
	}
	return ""
}

func sameLabelSet(a, b []string) bool {
	set := make(map[string]int, len(a))
	for _, l := range a {
		set[l]++
	}
	for _, l := range b {
		if set[l] == 0 {
			return false
		}
		set[l]--
	}
	for _, n := range set {
		if n != 0 {
			return false
		}
	}
	return true
}
