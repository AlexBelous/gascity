package beads

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// nativePGBeadColumns is the projected column set, shared by the issues and
// wisps SELECTs. It is the exact field set toBead consumes; every other column
// (notes, close_reason, counters, …) bd omits from the Bead object model too.
// metadata is cast to text so the row scans into []byte independent of the pgx
// jsonb codec, then decodes through the same StringMap path bd's wire uses.
const nativePGBeadColumns = `id, title, description, status, priority, issue_type, ` +
	`assignee, created_at, updated_at, defer_until, ephemeral, no_history, is_blocked, metadata::text`

// nativeBeadUnionSQL renders the both-tier bead SELECT with a tier_rank
// discriminator (issues=0 durable, wisps=1 ephemeral) appended as the final
// column, so scanNativeBeadRows can deduplicate a dual-residence id (present in
// both tables) deterministically, preferring the durable issues copy. clause is
// static SQL (never caller input); user-influenced values arrive only as bound
// args to pool.Query.
func nativeBeadUnionSQL(clause string) string {
	return `SELECT ` + nativePGBeadColumns + `, 0 AS tier_rank FROM beads.issues` + clause +
		` UNION ALL SELECT ` + nativePGBeadColumns + `, 1 AS tier_rank FROM beads.wisps` + clause
}

// Get returns a bead by exact id from the issues or wisps tier. On an exact
// hit the native row is returned. On a MISS the lookup DELEGATES to the embedded
// BdStore rather than returning a bare ErrNotFound, so bd's substring-collision
// guard (gcy-g4o) still runs: a truncated/mistyped id that fuzzy-resolves to a
// different bead is caught as ErrIDCollision by the write-mutation preflight
// instead of silently mutating the wrong bead. Any connection/scan error also
// falls back to bd.
func (s *NativePostgresReadStore) Get(id string) (Bead, error) {
	if !s.nativeUsable() {
		return s.BdStore.Get(id)
	}
	ctx, cancel := s.opContext()
	defer cancel()
	if err := s.ensureVerified(ctx); err != nil {
		s.recordNativeFailure("Get", err)
		return s.BdStore.Get(id)
	}
	bead, found, err := s.nativeGetCtx(ctx, id)
	if err != nil {
		s.recordNativeFailure("Get", err)
		return s.BdStore.Get(id)
	}
	s.breaker.recordSuccess()
	if !found {
		// Exact miss on a healthy pool: delegate to bd so its fuzzy-resolution
		// collision probe runs. The native path never emits ErrNotFound alone.
		return s.BdStore.Get(id)
	}
	return bead, nil
}

// nativeGetCtx returns (bead, found, err). found=false means the exact id was
// not present in either tier (not an error). A dual-residence id yields the
// durable issues-tier copy via scanNativeBeadRows' tier-preferring dedup.
func (s *NativePostgresReadStore) nativeGetCtx(ctx context.Context, id string) (Bead, bool, error) {
	query := nativeBeadUnionSQL(` WHERE id=$1`)
	rows, err := s.pool.Query(ctx, query, id)
	if err != nil {
		return Bead{}, false, fmt.Errorf("native get %q: %w", id, err)
	}
	beads, err := scanNativeBeadRows(rows)
	if err != nil {
		return Bead{}, false, fmt.Errorf("native get %q: %w", id, err)
	}
	if len(beads) == 0 {
		return Bead{}, false, nil
	}
	if err := s.hydrateBeads(ctx, beads); err != nil {
		return Bead{}, false, fmt.Errorf("native get %q: %w", id, err)
	}
	return beads[0], true, nil
}

// List serves the query from Postgres, mirroring BdStore.List: it fetches the
// candidate rows across both tiers and applies the identical Go-side
// applyListQuery, so tier, status, metadata, label, seek, sort, and limit
// semantics match bd byte-for-byte. Any error falls back to bd.
func (s *NativePostgresReadStore) List(query ListQuery) ([]Bead, error) {
	if !query.HasFilter() && !query.AllowScan {
		return nil, fmt.Errorf("native postgres list: %w", ErrQueryRequiresScan)
	}
	if !s.nativeUsable() {
		return s.BdStore.List(query)
	}
	ctx, cancel := s.opContext()
	defer cancel()
	if err := s.ensureVerified(ctx); err != nil {
		s.recordNativeFailure("List", err)
		return s.BdStore.List(query)
	}
	beads, err := s.nativeListCtx(ctx, query)
	if err != nil {
		s.recordNativeFailure("List", err)
		return s.BdStore.List(query)
	}
	s.breaker.recordSuccess()
	return beads, nil
}

func (s *NativePostgresReadStore) nativeListCtx(ctx context.Context, query ListQuery) ([]Bead, error) {
	statusWhere, args := nativeListStatusPredicate(query)
	beads, err := s.fetchBeads(ctx, statusWhere, args, nativeShouldIncludeTemplates(query))
	if err != nil {
		return nil, err
	}
	return applyListQuery(beads, query), nil
}

// nativeListStatusPredicate reproduces bd list's server-side status visibility,
// which runs on the RAW stored status before the shared applyListQuery sees the
// mapBdStatus-collapsed status. The user-influenced Status value is BOUND ($1),
// never string-concatenated, so it can never break out of the literal. The
// pinned-COLUMN exclusion mirrors bd list's filter.Pinned=&false, applied for
// the default and explicit-non-closed cases and dropped for --all / --status
// closed exactly as cmd/bd/list_filter.go does for the flag combinations gc
// passes:
//
//   - explicit non-closed Status: status=$1 AND pinned excluded.
//   - explicit Status=closed: gc adds --all → no pinned filter.
//   - IncludeClosed (bd --all): every row, no status/pinned filter.
//   - default: hide closed AND the pinned status AND the pinned column.
func nativeListStatusPredicate(query ListQuery) (string, []any) {
	if query.Status != "" {
		if strings.EqualFold(strings.TrimSpace(query.Status), "closed") {
			return "status = $1", []any{query.Status}
		}
		return "status = $1 AND (pinned = 0 OR pinned IS NULL)", []any{query.Status}
	}
	if query.IncludesClosed() {
		return "", nil
	}
	return "status NOT IN ('closed', 'pinned') AND (pinned = 0 OR pinned IS NULL)", nil
}

// nativeReadyWhere is bd ready's server-side WHERE for the v59 postgres schema,
// on the raw stored columns: status='open' (bd ready CLI pins status open, not
// the library {open,in_progress} default), the pinned COLUMN excluded, and
// is_blocked=0 read from bd's OWN projection. Reading is_blocked directly means
// the native path inherits bd's exact blocking semantics — waits-for spawner
// gates, blocked-parent cascade, pinned/dangling/external blocker rules — rather
// than re-deriving them. Templates are NOT filtered: bd ready returns
// is_template rows. The remaining tier/type/label/own-defer filters run Go-side
// via IsReadyCandidateForTier, exactly as BdStore.Ready applies them to bd's
// rows.
const nativeReadyWhere = "status = 'open' AND (pinned = 0 OR pinned IS NULL) AND is_blocked = 0"

// Ready returns open, unblocked, actionable work, reproducing bd ready's
// GetReadyWork semantics. Any error falls back to bd.
func (s *NativePostgresReadStore) Ready(query ...ReadyQuery) ([]Bead, error) {
	if !s.nativeUsable() {
		return s.BdStore.Ready(query...)
	}
	ctx, cancel := s.opContext()
	defer cancel()
	if err := s.ensureVerified(ctx); err != nil {
		s.recordNativeFailure("Ready", err)
		return s.BdStore.Ready(query...)
	}
	beads, err := s.nativeReadyCtx(ctx, readyQueryFromArgs(query))
	if err != nil {
		s.recordNativeFailure("Ready", err)
		return s.BdStore.Ready(query...)
	}
	s.breaker.recordSuccess()
	return beads, nil
}

func (s *NativePostgresReadStore) nativeReadyCtx(ctx context.Context, q ReadyQuery) ([]Bead, error) {
	now := time.Now().UTC()
	// Templates included (true): bd ready has no is_template filter.
	candidates, err := s.fetchBeads(ctx, nativeReadyWhere, nil, true)
	if err != nil {
		return nil, err
	}
	deferredChildren, err := s.fetchDeferredParentChildIDs(ctx, now)
	if err != nil {
		return nil, err
	}
	result := make([]Bead, 0, len(candidates))
	for _, b := range candidates {
		if !IsReadyCandidateForTier(b, now, q.TierMode) {
			continue
		}
		if q.Assignee != "" && b.Assignee != q.Assignee {
			continue
		}
		if deferredChildren[b.ID] {
			continue
		}
		result = append(result, b)
	}
	sortBeadsReadyOrder(result)
	if q.Limit > 0 && len(result) > q.Limit {
		result = result[:q.Limit]
	}
	return result, nil
}

// fetchDeferredParentChildIDs returns the ids of beads whose parent (via a
// parent-child edge) is future-deferred, mirroring bd's
// getChildrenOfDeferredParentsInTx: bd ready hides these even though the child
// itself is open and unblocked. The `now` bound is passed explicitly so the
// exclusion and IsReadyCandidateForTier share one clock. Returns an empty set
// when no parent is future-deferred (the common case).
func (s *NativePostgresReadStore) fetchDeferredParentChildIDs(ctx context.Context, now time.Time) (map[string]bool, error) {
	const query = `WITH deferred AS (
  SELECT id FROM beads.issues WHERE defer_until IS NOT NULL AND defer_until > $1
  UNION SELECT id FROM beads.wisps WHERE defer_until IS NOT NULL AND defer_until > $1
)
SELECT dep.issue_id FROM beads.dependencies dep
  JOIN deferred p ON p.id = COALESCE(dep.depends_on_issue_id, dep.depends_on_wisp_id)
  WHERE dep.type = 'parent-child'
UNION
SELECT dep.issue_id FROM beads.wisp_dependencies dep
  JOIN deferred p ON p.id = COALESCE(dep.depends_on_issue_id, dep.depends_on_wisp_id)
  WHERE dep.type = 'parent-child'`
	rows, err := s.pool.Query(ctx, query, now)
	if err != nil {
		return nil, fmt.Errorf("native deferred-children query: %w", err)
	}
	defer rows.Close()
	out := make(map[string]bool)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("native deferred-children scan: %w", err)
		}
		out[id] = true
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("native deferred-children rows: %w", err)
	}
	return out, nil
}

// Count reports how many beads List would return for query, minus excludeTypes,
// honoring the caller's context. It computes the exact count from the native
// List projection, so every shape List answers is answered here. Limited queries
// are ErrCountUnsupported (the Counter contract pins Count to List cardinality
// including the post-sort cap). BdStore is not a Counter, so a native failure
// reports ErrCountUnsupported and callers fall back to List (also native).
func (s *NativePostgresReadStore) Count(ctx context.Context, query ListQuery, excludeTypes ...string) (int, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := query.Validate(); err != nil {
		return 0, err
	}
	if !query.HasFilter() && !query.AllowScan {
		return 0, fmt.Errorf("counting beads: %w", ErrQueryRequiresScan)
	}
	if query.Limit > 0 {
		return 0, fmt.Errorf("counting beads: %w", ErrCountUnsupported)
	}
	if !s.nativeUsable() {
		return 0, fmt.Errorf("counting beads: %w", ErrCountUnsupported)
	}
	cctx, cancel := context.WithTimeout(ctx, nativePGOpTimeout)
	defer cancel()
	if err := s.ensureVerified(cctx); err != nil {
		s.recordNativeFailure("Count", err)
		return 0, fmt.Errorf("counting beads: %w", ErrCountUnsupported)
	}
	beads, err := s.nativeListCtx(cctx, query)
	if err != nil {
		if errors.Is(err, ErrQueryRequiresScan) {
			return 0, err
		}
		s.recordNativeFailure("Count", err)
		return 0, fmt.Errorf("counting beads: %w", ErrCountUnsupported)
	}
	s.breaker.recordSuccess()
	n := 0
	for _, b := range beads {
		if typeIsExcluded(b.Type, excludeTypes) {
			continue
		}
		n++
	}
	return n, nil
}

// DepList returns dependency edges for a bead. "down" (default) returns what the
// bead depends on; "up" returns what depends on it. Matches BdStore.DepList,
// including returning nil (not an error) when the bead has no edges. Any error
// falls back to bd.
func (s *NativePostgresReadStore) DepList(id, direction string) ([]Dep, error) {
	if !s.nativeUsable() {
		return s.BdStore.DepList(id, direction)
	}
	ctx, cancel := s.opContext()
	defer cancel()
	if err := s.ensureVerified(ctx); err != nil {
		s.recordNativeFailure("DepList", err)
		return s.BdStore.DepList(id, direction)
	}
	deps, err := s.nativeDepListCtx(ctx, id, direction)
	if err != nil {
		s.recordNativeFailure("DepList", err)
		return s.BdStore.DepList(id, direction)
	}
	s.breaker.recordSuccess()
	return deps, nil
}

func (s *NativePostgresReadStore) nativeDepListCtx(ctx context.Context, id, direction string) ([]Dep, error) {
	const dep = `SELECT issue_id, COALESCE(depends_on_issue_id, depends_on_wisp_id, depends_on_external, ''), type FROM beads.dependencies`
	const wispDep = `SELECT issue_id, COALESCE(depends_on_issue_id, depends_on_wisp_id, depends_on_external, ''), type FROM beads.wisp_dependencies`
	var query string
	if direction == "up" {
		where := ` WHERE depends_on_issue_id=$1 OR depends_on_wisp_id=$1`
		query = dep + where + ` UNION ALL ` + wispDep + where
	} else {
		where := ` WHERE issue_id=$1`
		query = dep + where + ` UNION ALL ` + wispDep + where
	}
	rows, err := s.pool.Query(ctx, query, id)
	if err != nil {
		return nil, fmt.Errorf("native dep list %q: %w", id, err)
	}
	defer rows.Close()
	var out []Dep
	for rows.Next() {
		var issueID, dependsOn, depType string
		if err := rows.Scan(&issueID, &dependsOn, &depType); err != nil {
			return nil, fmt.Errorf("native dep list %q: %w", id, err)
		}
		if strings.TrimSpace(depType) == "" {
			depType = "blocks"
		}
		if direction == "up" {
			out = append(out, Dep{IssueID: issueID, DependsOnID: id, Type: depType})
		} else {
			out = append(out, Dep{IssueID: id, DependsOnID: dependsOn, Type: depType})
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("native dep list %q: %w", id, err)
	}
	return out, nil
}

// Children returns beads whose parent is parentID, mirroring BdStore.Children so
// the shared native List path guarantees identical filtering.
func (s *NativePostgresReadStore) Children(parentID string, opts ...QueryOpt) ([]Bead, error) {
	return s.List(ListQuery{
		ParentID:      parentID,
		IncludeClosed: HasOpt(opts, IncludeClosed),
		Sort:          SortCreatedAsc,
	})
}

// ListByLabel returns beads carrying an exact label, mirroring BdStore.ListByLabel.
func (s *NativePostgresReadStore) ListByLabel(label string, limit int, opts ...QueryOpt) ([]Bead, error) {
	return s.List(ListQuery{
		Label:         label,
		Limit:         limit,
		IncludeClosed: HasOpt(opts, IncludeClosed),
		Sort:          SortCreatedDesc,
		TierMode:      TierModeFromOpts(opts),
	})
}

// ListByAssignee returns beads assigned to an agent with a status, mirroring
// BdStore.ListByAssignee.
func (s *NativePostgresReadStore) ListByAssignee(assignee, status string, limit int) ([]Bead, error) {
	return s.List(ListQuery{
		Assignee: assignee,
		Status:   status,
		Limit:    limit,
		Sort:     SortCreatedDesc,
	})
}

// ListByMetadata returns beads whose metadata contains all given pairs,
// mirroring BdStore.ListByMetadata.
func (s *NativePostgresReadStore) ListByMetadata(filters map[string]string, limit int, opts ...QueryOpt) ([]Bead, error) {
	return s.List(ListQuery{
		Metadata:      filters,
		Limit:         limit,
		IncludeClosed: HasOpt(opts, IncludeClosed),
		Sort:          SortCreatedDesc,
		TierMode:      TierModeFromOpts(opts),
	})
}

// Handles returns read handles that route Cached and Live reads through the
// native methods (both at TierBoth, matching the logical readers) and keep
// writes on the embedded BdStore.
func (s *NativePostgresReadStore) Handles() StoreHandles {
	return StoreHandles{
		Cached: nativePostgresReader{store: s, live: false},
		Live:   nativePostgresReader{store: s, live: true},
		Writer: s.BdStore,
	}
}

// listIncludesCompleteDependencies reports true: the native List hydrates each
// bead's Dependencies from beads.dependencies, so the caching store may treat
// the List snapshot's deps as complete (matching NativeDoltStore). This makes
// reconcile use fresh deps and propagate an out-of-band `bd dep remove` instead
// of resurrecting the removed edge.
func (s *NativePostgresReadStore) listIncludesCompleteDependencies() bool {
	return true
}

// reconcileDepsAreAuthoritative reports true: because the native List hydrates
// complete deps into each bead, an EMPTY fresh dep set during reconcile is an
// authoritative removal, not a missing-data artifact — so the caching store's
// dep-authority carve-out must treat this wrapper like *BdStore. (The embedded
// BdStore also returns true, so this is belt-and-suspenders documentation of
// the wrapper's own guarantee.)
func (s *NativePostgresReadStore) reconcileDepsAreAuthoritative() bool {
	return true
}

// nativePostgresReader adapts the native store to the Cached/Live reader
// contract: List reads across both tiers, matching logicalCached/LiveStoreReader.
type nativePostgresReader struct {
	store *NativePostgresReadStore
	live  bool
}

func (r nativePostgresReader) Get(id string) (Bead, error) { return r.store.Get(id) }

func (r nativePostgresReader) List(query ListQuery) ([]Bead, error) {
	query.Live = r.live
	query.TierMode = TierBoth
	return r.store.List(query)
}

func (r nativePostgresReader) Ready(query ...ReadyQuery) ([]Bead, error) {
	return r.store.Ready(query...)
}

func (r nativePostgresReader) DepList(id, direction string) ([]Dep, error) {
	return r.store.DepList(id, direction)
}

// fetchBeads reads the candidate rows across both tiers with the given status
// predicate (a static clause plus bound args for any user-influenced value) and
// template policy, then hydrates labels, dependencies, and derived parent.
func (s *NativePostgresReadStore) fetchBeads(ctx context.Context, statusWhere string, args []any, includeTemplates bool) ([]Bead, error) {
	where := make([]string, 0, 2)
	if statusWhere != "" {
		where = append(where, statusWhere)
	}
	if !includeTemplates {
		where = append(where, "COALESCE(is_template, 0) = 0")
	}
	clause := ""
	if len(where) > 0 {
		clause = " WHERE " + strings.Join(where, " AND ")
	}
	rows, err := s.pool.Query(ctx, nativeBeadUnionSQL(clause), args...)
	if err != nil {
		return nil, fmt.Errorf("native list query: %w", err)
	}
	beads, err := scanNativeBeadRows(rows)
	if err != nil {
		return nil, fmt.Errorf("native list scan: %w", err)
	}
	if err := s.hydrateBeads(ctx, beads); err != nil {
		return nil, err
	}
	return beads, nil
}

// hydrateBeads fills labels, dependencies, and the derived parent for the given
// beads in bounded batch queries.
func (s *NativePostgresReadStore) hydrateBeads(ctx context.Context, beads []Bead) error {
	if len(beads) == 0 {
		return nil
	}
	ids := make([]string, len(beads))
	for i := range beads {
		ids[i] = beads[i].ID
	}
	labels, err := s.fetchLabels(ctx, ids)
	if err != nil {
		return err
	}
	deps, err := s.fetchDeps(ctx, ids)
	if err != nil {
		return err
	}
	for i := range beads {
		beads[i].Labels = labels[beads[i].ID]
		d := deps[beads[i].ID]
		beads[i].Dependencies = d
		beads[i].ParentID = parentFromDeps(d)
	}
	return nil
}

func (s *NativePostgresReadStore) fetchLabels(ctx context.Context, ids []string) (map[string][]string, error) {
	query := `SELECT issue_id, label FROM beads.labels WHERE issue_id = ANY($1)` +
		` UNION ALL SELECT issue_id, label FROM beads.wisp_labels WHERE issue_id = ANY($1)` +
		` ORDER BY issue_id, label`
	rows, err := s.pool.Query(ctx, query, ids)
	if err != nil {
		return nil, fmt.Errorf("native label query: %w", err)
	}
	defer rows.Close()
	out := make(map[string][]string)
	for rows.Next() {
		var id, label string
		if err := rows.Scan(&id, &label); err != nil {
			return nil, fmt.Errorf("native label scan: %w", err)
		}
		out[id] = append(out[id], label)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("native label rows: %w", err)
	}
	return out, nil
}

func (s *NativePostgresReadStore) fetchDeps(ctx context.Context, ids []string) (map[string][]Dep, error) {
	query := `SELECT issue_id, COALESCE(depends_on_issue_id, depends_on_wisp_id, depends_on_external, ''), type FROM beads.dependencies WHERE issue_id = ANY($1)` +
		` UNION ALL SELECT issue_id, COALESCE(depends_on_issue_id, depends_on_wisp_id, depends_on_external, ''), type FROM beads.wisp_dependencies WHERE issue_id = ANY($1)` +
		` ORDER BY issue_id`
	rows, err := s.pool.Query(ctx, query, ids)
	if err != nil {
		return nil, fmt.Errorf("native dep query: %w", err)
	}
	defer rows.Close()
	out := make(map[string][]Dep)
	for rows.Next() {
		var issueID, dependsOn, depType string
		if err := rows.Scan(&issueID, &dependsOn, &depType); err != nil {
			return nil, fmt.Errorf("native dep scan: %w", err)
		}
		if strings.TrimSpace(depType) == "" {
			depType = "blocks"
		}
		out[issueID] = append(out[issueID], Dep{IssueID: issueID, DependsOnID: dependsOn, Type: depType})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("native dep rows: %w", err)
	}
	return out, nil
}

// nativeBeadScan holds one raw row before conversion to the Bead object model.
type nativeBeadScan struct {
	id          string
	title       string
	description string
	status      string
	priority    int
	issueType   string
	assignee    *string
	createdAt   time.Time
	updatedAt   time.Time
	deferUntil  *time.Time
	ephemeral   int16
	noHistory   int16
	isBlocked   int16
	metadata    []byte
}

// scanNativeBeadRows scans a bead result set (in nativePGBeadColumns order plus
// the trailing tier_rank) and converts each row to a Bead, deduplicating a
// dual-residence id by keeping the lower tier_rank (issues=0 durable) so List
// and Ready never emit two beads with the same id during a be-iabdi-class
// integrity incident. It closes rows before returning.
func scanNativeBeadRows(rows interface {
	Next() bool
	Scan(...any) error
	Err() error
	Close()
},
) ([]Bead, error) {
	defer rows.Close()
	var beads []Bead
	tiers := make(map[string]int16)
	index := make(map[string]int)
	for rows.Next() {
		var r nativeBeadScan
		var tier int16
		if err := rows.Scan(
			&r.id, &r.title, &r.description, &r.status, &r.priority, &r.issueType,
			&r.assignee, &r.createdAt, &r.updatedAt, &r.deferUntil,
			&r.ephemeral, &r.noHistory, &r.isBlocked, &r.metadata, &tier,
		); err != nil {
			return nil, err
		}
		if existing, ok := index[r.id]; ok {
			// Dual-residence: keep the durable (lower tier_rank) copy.
			if tier < tiers[r.id] {
				beads[existing] = r.toBead()
				tiers[r.id] = tier
			}
			continue
		}
		index[r.id] = len(beads)
		tiers[r.id] = tier
		beads = append(beads, r.toBead())
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return beads, nil
}

// toBead converts a raw Postgres row to a Bead, reproducing BdStore.toBead's
// derivations: status is collapsed via mapBdStatus, timestamps are truncated to
// second precision (UTC), from comes from metadata, and empty
// metadata/labels/deps stay nil to match bd's omitempty wire form. Labels,
// Dependencies, and ParentID are filled by hydrateBeads.
func (r nativeBeadScan) toBead() Bead {
	priority := r.priority
	bead := Bead{
		ID:          r.id,
		Title:       r.title,
		Status:      mapBdStatus(r.status),
		Type:        r.issueType,
		Priority:    &priority,
		CreatedAt:   r.createdAt.UTC().Truncate(time.Second),
		UpdatedAt:   r.updatedAt.UTC().Truncate(time.Second),
		Assignee:    derefString(r.assignee),
		Description: r.description,
		Ephemeral:   r.ephemeral != 0,
		NoHistory:   r.noHistory != 0,
	}
	if md := parseNativeMetadata(r.metadata); len(md) > 0 {
		bead.Metadata = md
		bead.From = md["from"]
	}
	if r.deferUntil != nil {
		defer0 := r.deferUntil.UTC()
		bead.DeferUntil = &defer0
	}
	// bd emits is_blocked only when true (omitempty); a false/0 column decodes to
	// nil, matching toBead's optionalBool.ptr().
	if r.isBlocked != 0 {
		blocked := true
		bead.IsBlocked = &blocked
	}
	return bead
}

// parseNativeMetadata decodes the inline metadata jsonb into a StringMap
// (coercing non-string values as bd's wire decode does). An empty or "{}"
// object decodes to nil so it matches bd's omitempty metadata.
func parseNativeMetadata(raw []byte) StringMap {
	if len(raw) == 0 {
		return nil
	}
	var md StringMap
	if err := json.Unmarshal(raw, &md); err != nil {
		return nil
	}
	if len(md) == 0 {
		return nil
	}
	return md
}

// parentFromDeps returns the parent id from a parent-child down-edge, matching
// BdStore.toBead's parent derivation.
func parentFromDeps(deps []Dep) string {
	for _, d := range deps {
		if d.Type == "parent-child" {
			return d.DependsOnID
		}
	}
	return ""
}

// nativeShouldIncludeTemplates mirrors bdListShouldIncludeTemplates: bd passes
// --include-templates only for wisp-tier reads and for both-tier reads that are
// not scoped to messages.
func nativeShouldIncludeTemplates(query ListQuery) bool {
	return query.TierMode == TierWisps || (query.TierMode == TierBoth && query.Type != "message")
}

func typeIsExcluded(t string, excludeTypes []string) bool {
	for _, e := range excludeTypes {
		if t == e {
			return true
		}
	}
	return false
}

func derefString(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
