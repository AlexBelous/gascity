package beads

import (
	"context"
	"errors"
	"sort"
	"time"

	"github.com/gastownhall/gascity/internal/beadmeta"
)

// ErrQueryRequiresScan reports that a query would require an explicit scan.
// Callers must opt into that behavior with ListQuery.AllowScan.
var ErrQueryRequiresScan = errors.New("bead query requires scan")

// SortOrder controls optional result ordering for List queries.
type SortOrder string

// List query sort orders.
const (
	// SortDefault leaves store-defined ordering unchanged.
	SortDefault     SortOrder = ""
	SortCreatedAsc  SortOrder = "created_asc"
	SortCreatedDesc SortOrder = "created_desc"
)

// TierMode selects which storage tier(s) a List query reads from.
// The zero value is TierIssues.
//
// TierIssues is the permanent logical tier and filters out Ephemeral rows when
// a store returns them to the caller. NoHistory rows remain visible to list
// filters in TierIssues because they are durable work without Dolt history.
// Raw bd ready defaults are narrower than the logical union surface. In bd
// 1.0.4, ready queries cannot expose no-history rows with the full ready
// filter semantics, so compatibility policy keeps claimable work history-backed
// in that mode. TierBoth is a logical union; implementations may satisfy it
// through a single backend query when the backing store exposes a supported
// union surface for the requested bead type.
type TierMode int

const (
	// TierIssues reads only the permanent (issues) tier. Default.
	TierIssues TierMode = iota
	// TierWisps reads only the wisp-backed tier, including ephemeral and
	// no-history rows.
	TierWisps
	// TierBoth unions the issues and wisps tiers, deduping by ID and
	// preserving the query's sort.
	TierBoth
)

// TierModeFromOpts returns the tier mode implied by a slice of QueryOpts.
// WithBothTiers takes precedence over WithEphemeral.
func TierModeFromOpts(opts []QueryOpt) TierMode {
	switch {
	case HasOpt(opts, WithBothTiers):
		return TierBoth
	case HasOpt(opts, WithEphemeral):
		return TierWisps
	default:
		return TierIssues
	}
}

// ListQuery describes a filtered bead lookup.
//
// Queries are conjunctive: every populated field must match. A zero-value query
// is rejected unless AllowScan is true.
type ListQuery struct {
	Status   string
	Type     string
	Label    string
	Assignee string
	// Assignees matches beads assigned to any listed assignee.
	// It is mutually exclusive with Assignee; call Validate to enforce that contract.
	Assignees []string
	ParentID  string
	// ParentIDs matches beads whose parent_id is any of the listed ids — a
	// batched form of ParentID for graph/subtree walks. Backends that do not
	// recognize it should ignore it (returning a superset); callers that need
	// exact results must filter the returned beads by parent in memory.
	ParentIDs     []string
	Metadata      map[string]string
	CreatedBefore time.Time
	// UpdatedBefore matches beads whose UpdatedAt is before this timestamp.
	// Legacy beads with zero UpdatedAt fall back to CreatedAt. Purge callers
	// using CachingStore must also set Live: true to avoid stale cached timestamps.
	UpdatedBefore time.Time
	Limit         int
	IncludeClosed bool
	AllowScan     bool
	// SkipLabels tells backing stores and cache reconciliation that the
	// caller does not need labels for change detection. Stores that cannot
	// omit labels may ignore it.
	SkipLabels bool
	// Live bypasses CachingStore and reads from the backing store. Other Store
	// implementations ignore it. Use it only for lifecycle gates that must
	// observe external mutations immediately.
	Live bool
	Sort SortOrder
	// AllowBackingCreatedLimit lets a backing store satisfy a bounded
	// SortCreatedDesc read with its own native row limit even though the backing
	// breaks created_at ties by id ASC while Gas City's canonical order
	// (sortBeadsForQuery) and cursor continuation (SeekBoundary.After) break them
	// by id DESC. A native desc limit can therefore keep the smaller-id tie
	// members at the boundary and drop the larger-id ties an exact or
	// cursor-paginated caller needs, so it is OFF by default: exact/paginated
	// reads fetch the full candidate set and let ApplyListQuery cut the exact
	// (created_at DESC, id DESC) prefix. Only a caller that folds the bounded rows
	// into a max over the created_at sort key ITSELF may set it true — every
	// dropped boundary tie shares the surviving rows' created_at, so the max is
	// unchanged (e.g. the order dispatcher's RecentRunsAll/LastRun, which reduce to
	// max(created_at)). A caller that reduces over a DIFFERENT column must NOT set
	// it: the order dispatcher's event cursor (Cursor/bdCursor) reduces to max(seq)
	// via MaxSeqFromLabels, and because seq is forward-only the max-seq run is the
	// newest largest-id row — exactly the tie member a bounded id-ASC read drops —
	// so a bounded backing read there would regress the cursor and replay events. It
	// has no effect on SortCreatedAsc (whose backing id ASC tie-break already
	// matches the canonical order, so bounded asc reads are exact) or on stores that
	// always resolve the limit Go-side.
	AllowBackingCreatedLimit bool
	// TierMode selects the storage tier(s) to read from. Zero value
	// (TierIssues) preserves the legacy single-tier behavior.
	TierMode TierMode
	// SeekAfter is an exclusive keyset boundary for cursor pagination: only
	// rows STRICTLY AFTER the boundary in the query's sort order match. It
	// requires an explicit Sort (Validate enforces this) because a seek
	// without a total order is meaningless. Every backend resolves the compound
	// (created_at, id) boundary Go-side via Matches to keep the tie-break
	// byte-identical to the in-memory sort — a SQL/CLI seek predicate is
	// expressible but risks collation/precision divergence. Because the filter
	// is Go-side, it must run BEFORE any native row limit — a limit applied
	// first silently drops page rows — so seeked reads fetch a superset and cut
	// the page in Go.
	SeekAfter *SeekBoundary
}

// SeekBoundary identifies the last row a pagination client has seen, in the
// (created_at, id) total order (#3208). The boundary row itself is excluded.
type SeekBoundary struct {
	CreatedAt time.Time
	ID        string
}

// Validate returns an error when the query contains contradictory selectors.
func (q ListQuery) Validate() error {
	if q.Assignee != "" && len(q.Assignees) > 0 {
		return errors.New("ListQuery: Assignee and Assignees are mutually exclusive")
	}
	if q.SeekAfter != nil && q.Sort != SortCreatedAsc && q.Sort != SortCreatedDesc {
		return errors.New("ListQuery: SeekAfter requires an explicit created_at sort order")
	}
	return nil
}

// RouteMatchMode selects how a ReadyQuery.Route target is matched against a
// bead's routing metadata for the hook fast path's routed-pool tier. It stays a
// plain int so ReadyQuery remains a comparable struct (CachingStore.Ready gates
// on `!= (ReadyQuery{})`), and so the cache-served read can filter routed pool
// demand BEFORE applying Limit — reproducing the legacy `bd ready
// --metadata-field ... --unassigned --exclude-type=epic --sort oldest --limit 20`
// tier without shipping the full ready set over the wire.
type RouteMatchMode int

const (
	// RouteNone disables routed-pool matching; Route is ignored.
	RouteNone RouteMatchMode = iota
	// RouteCanonical matches the canonical persisted routing key: a bead whose
	// gc.routed_to metadata equals Route.
	RouteCanonical
	// RouteMigration matches the retirement-window legacy routing shape: a
	// workflow-root bead (gc.kind=workflow) whose gc.run_target equals Route and
	// which does NOT yet carry a gc.routed_to stamp. It mirrors the workquery
	// migration probe (poolDemandMigrationFilterJQ), so a stale divergent
	// gc.run_target cannot outrank a canonical gc.routed_to match.
	RouteMigration
)

// ReadyQuery describes optional filters for ready-work lookup. A zero-value
// query preserves Ready's historical behavior: all open, unblocked actionable
// work. Every field is a comparable scalar so the struct stays usable with
// `==`/`!=` (CachingStore.Ready gates cache vs backing on `!= (ReadyQuery{})`).
type ReadyQuery struct {
	Assignee string
	Limit    int
	// TierMode selects the storage tier(s) to read from. Zero value
	// (TierIssues) preserves raw Ready's historical main-tier behavior.
	// Policy-aware callers should use the policy store wrapper, which expands
	// default Ready reads to TierBoth so no-history and ephemeral policy rows
	// remain reachable under bd 1.0.4.
	TierMode TierMode
	// Unassigned, when true, keeps only unassigned (assignee=="") ready beads.
	// The hook fast path sets it for the routed-pool tier, which claims only
	// unassigned work.
	Unassigned bool
	// ExcludeType drops ready beads of this bead type. The hook fast path sets
	// it to "epic" so a pool worker never claims an unassigned parent epic
	// (which has no executable spec), matching the legacy --exclude-type=epic.
	ExcludeType string
	// Route, when non-empty, keeps only routed-pool demand for this target and
	// (via RouteMode) selects canonical vs migration matching. A routed read is
	// sorted oldest-first (created_asc), not by ready priority, matching the
	// legacy routed tier's `--sort oldest`. The filter is applied before Limit.
	Route     string
	RouteMode RouteMatchMode
}

// isRouted reports whether this query selects the routed-pool tier.
func (q ReadyQuery) isRouted() bool {
	return q.Route != "" && q.RouteMode != RouteNone
}

// matchesExtra reports whether b passes the Unassigned, ExcludeType, and
// routed-pool filters this query adds on top of base readiness. Base readiness
// (open, unblocked, tier, defer_until) and Assignee are evaluated by the
// caller; this covers only the fields introduced for the hook fast path.
func (q ReadyQuery) matchesExtra(b Bead) bool {
	if q.Unassigned && b.Assignee != "" {
		return false
	}
	if q.ExcludeType != "" && b.Type == q.ExcludeType {
		return false
	}
	if !q.isRouted() {
		return true
	}
	switch q.RouteMode {
	case RouteCanonical:
		return b.Metadata[beadmeta.RoutedToMetadataKey] == q.Route
	case RouteMigration:
		return b.Metadata[beadmeta.RunTargetMetadataKey] == q.Route &&
			b.Metadata[beadmeta.KindMetadataKey] == beadmeta.KindWorkflow &&
			b.Metadata[beadmeta.RoutedToMetadataKey] == ""
	default:
		return false
	}
}

func readyQueryFromArgs(queries []ReadyQuery) ReadyQuery {
	if len(queries) == 0 {
		return ReadyQuery{}
	}
	return queries[0]
}

// HasFilter reports whether the query includes at least one indexed selector.
func (q ListQuery) HasFilter() bool {
	return q.Status != "" ||
		q.Type != "" ||
		q.Label != "" ||
		q.Assignee != "" ||
		len(q.Assignees) > 0 ||
		q.ParentID != "" ||
		len(q.Metadata) > 0 ||
		!q.CreatedBefore.IsZero() ||
		!q.UpdatedBefore.IsZero() ||
		q.SeekAfter != nil
}

// IncludesClosed reports whether the query may return closed beads.
func (q ListQuery) IncludesClosed() bool {
	return q.IncludeClosed || q.Status == "closed"
}

// matchesTier reports whether the bead is in the storage tier(s) the query
// selects. TierIssues (the zero value) excludes ephemeral wisps; TierWisps
// keeps only ephemeral or no-history rows; TierBoth applies no tier filter.
func (q ListQuery) matchesTier(b Bead) bool {
	switch q.TierMode {
	case TierWisps:
		return b.Ephemeral || b.NoHistory
	case TierBoth:
		return true
	default: // TierIssues
		return !b.Ephemeral
	}
}

// Matches reports whether the bead satisfies the query.
func (q ListQuery) Matches(b Bead) bool {
	if !q.matchesTier(b) {
		return false
	}
	if q.Status != "" {
		if b.Status != q.Status {
			return false
		}
	} else if !q.IncludeClosed && b.Status == "closed" {
		return false
	}
	if q.Type != "" && b.Type != q.Type {
		return false
	}
	if q.Label != "" && !beadHasLabel(b, q.Label) {
		return false
	}
	if q.Assignee != "" && b.Assignee != q.Assignee {
		return false
	}
	if len(q.Assignees) > 0 {
		matched := false
		for _, assignee := range q.Assignees {
			if b.Assignee == assignee {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}
	if q.ParentID != "" && b.ParentID != q.ParentID {
		return false
	}
	if len(q.Metadata) > 0 && !matchesMetadata(b, q.Metadata) {
		return false
	}
	if !q.CreatedBefore.IsZero() && !b.CreatedAt.Before(q.CreatedBefore) {
		return false
	}
	if !q.UpdatedBefore.IsZero() && !beadUpdatedReferenceTime(b).Before(q.UpdatedBefore) {
		return false
	}
	if q.SeekAfter != nil && !q.SeekAfter.After(b, q.Sort) {
		return false
	}
	return true
}

// After reports whether the bead sorts strictly after the boundary in the
// given order — i.e. it belongs on a page that resumes from the boundary.
// The comparison mirrors sortBeadsForQuery's (created_at, id) total order
// exactly, id tie-break included, so a page boundary can never skip or
// duplicate a row.
func (sb *SeekBoundary) After(b Bead, sort SortOrder) bool {
	switch sort {
	case SortCreatedAsc:
		if b.CreatedAt.After(sb.CreatedAt) {
			return true
		}
		return b.CreatedAt.Equal(sb.CreatedAt) && b.ID > sb.ID
	case SortCreatedDesc:
		if b.CreatedAt.Before(sb.CreatedAt) {
			return true
		}
		return b.CreatedAt.Equal(sb.CreatedAt) && b.ID < sb.ID
	default:
		// Validate rejects this shape; match nothing rather than guess.
		return false
	}
}

func beadUpdatedReferenceTime(b Bead) time.Time {
	if !b.UpdatedAt.IsZero() {
		return b.UpdatedAt
	}
	return b.CreatedAt
}

func beadHasLabel(b Bead, want string) bool {
	for _, label := range b.Labels {
		if label == want {
			return true
		}
	}
	return false
}

// ApplyListQuery filters, sorts, and limits an in-memory bead slice.
func ApplyListQuery(items []Bead, q ListQuery) []Bead {
	filtered := make([]Bead, 0, len(items))
	for _, b := range items {
		if q.Matches(b) {
			filtered = append(filtered, b)
		}
	}
	sortBeadsForQuery(filtered, q.Sort)
	if q.Limit > 0 && len(filtered) > q.Limit {
		filtered = filtered[:q.Limit]
	}
	return filtered
}

func applyListQuery(items []Bead, q ListQuery) []Bead {
	return ApplyListQuery(items, q)
}

// SortBeads sorts items into the canonical (created_at, id) total order for
// the given direction. SortDefault leaves the slice order unchanged. Callers
// that merge results across stores use this to impose one deterministic
// global order on the merged set (#3208).
func SortBeads(items []Bead, order SortOrder) {
	sortBeadsForQuery(items, order)
}

// SortBeadsReadyOrder sorts ready results into the canonical
// (priority, created_at, id) ascending order used by the SQL-backed ready
// readers, matching CachedReady's own ordering (#3208). Callers that assemble
// a ready-shaped result from a source other than CachedReady/Ready (e.g. a
// single batched bd ready fallback) use this to match that canonical order.
func SortBeadsReadyOrder(items []Bead) {
	sortBeadsReadyOrder(items)
}

// sortBeadsReadyOrder sorts ready results into the canonical
// (priority, created_at, id) ascending order used by the SQL-backed ready
// readers (a nil priority sorts as 2, matching their COALESCE(i.priority, 2)),
// so a bounded ready read cuts the same deterministic prefix regardless of
// which store path served it (#3208).
func sortBeadsReadyOrder(items []Bead) {
	sort.Slice(items, func(i, j int) bool {
		return beadReadyLess(items[i], items[j])
	})
}

// sortBeadsReadyOrderContext is the cancellation-aware form used by
// deadline-sensitive cache projections. A local merge sort keeps cancellation
// checks inside both comparison and copy work instead of abandoning an
// uninterruptible sort goroutine when ctx expires.
func sortBeadsReadyOrderContext(ctx context.Context, items []Bead) error {
	if ctx == nil || ctx.Done() == nil {
		sortBeadsReadyOrder(items)
		return nil
	}
	return sortBeadsLessContext(ctx, items, beadReadyLess)
}

// sortBeadsCreatedAscContext is the cancellation-aware (created_at, id)
// ascending sort used by the routed-pool cache projection, whose oldest-first
// order (matching the legacy `bd ready --sort oldest` probe) must stay
// interruptible for the same reason as the ready-order sort above.
func sortBeadsCreatedAscContext(ctx context.Context, items []Bead) error {
	if ctx == nil || ctx.Done() == nil {
		sortBeadsForQuery(items, SortCreatedAsc)
		return nil
	}
	return sortBeadsLessContext(ctx, items, beadCreatedAscLess)
}

// sortBeadsLessContext merge-sorts items by less with cancellation checks
// inside both comparison and copy work, so a deadline-sensitive caller never
// abandons an uninterruptible sort goroutine when ctx expires. Callers must
// pass a cancellable ctx (Done non-nil).
func sortBeadsLessContext(ctx context.Context, items []Bead, less func(a, b Bead) bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if len(items) < 2 {
		return nil
	}

	scratch := make([]Bead, len(items))
	var mergeSort func(int, int) error
	mergeSort = func(lo, hi int) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if hi-lo < 2 {
			return nil
		}
		mid := lo + (hi-lo)/2
		if err := mergeSort(lo, mid); err != nil {
			return err
		}
		if err := mergeSort(mid, hi); err != nil {
			return err
		}

		i, j := lo, mid
		for k := lo; k < hi; k++ {
			if err := ctx.Err(); err != nil {
				return err
			}
			switch {
			case i == mid:
				scratch[k] = items[j]
				j++
			case j == hi:
				scratch[k] = items[i]
				i++
			case less(items[j], items[i]):
				scratch[k] = items[j]
				j++
			default:
				scratch[k] = items[i]
				i++
			}
		}
		for k := lo; k < hi; k++ {
			if err := ctx.Err(); err != nil {
				return err
			}
			items[k] = scratch[k]
		}
		return nil
	}
	return mergeSort(0, len(items))
}

// beadCreatedAscLess is the (created_at, id) ascending total order shared by
// sortBeadsForQuery's SortCreatedAsc branch and the cancellation-aware routed
// sort, so both paths cut the same deterministic oldest-first prefix.
func beadCreatedAscLess(a, b Bead) bool {
	if !a.CreatedAt.Equal(b.CreatedAt) {
		return a.CreatedAt.Before(b.CreatedAt)
	}
	return a.ID < b.ID
}

func beadReadyLess(a, b Bead) bool {
	pa, pb := readySortPriority(a), readySortPriority(b)
	if pa != pb {
		return pa < pb
	}
	if !a.CreatedAt.Equal(b.CreatedAt) {
		return a.CreatedAt.Before(b.CreatedAt)
	}
	return a.ID < b.ID
}

func readySortPriority(b Bead) int {
	if b.Priority == nil {
		return 2
	}
	return *b.Priority
}

func sortBeadsForQuery(items []Bead, order SortOrder) {
	switch order {
	case SortCreatedAsc:
		sort.Slice(items, func(i, j int) bool {
			return beadCreatedAscLess(items[i], items[j])
		})
	case SortCreatedDesc:
		sort.Slice(items, func(i, j int) bool {
			if items[i].CreatedAt.Equal(items[j].CreatedAt) {
				return items[i].ID > items[j].ID
			}
			return items[i].CreatedAt.After(items[j].CreatedAt)
		})
	}
}
