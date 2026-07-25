package beads

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	beadslib "github.com/steveyegge/beads"
)

var (
	_ SnapshotExporter = (*NativeDoltStore)(nil)
	_ SnapshotImporter = (*NativeDoltStore)(nil)
	_ SnapshotFetcher  = (*NativeDoltStore)(nil)
)

// nativeSnapshotBulkReader is the subset of the concrete Dolt store's bulk
// relational methods the export/fetch legs need. It is declared locally
// (structural typing) because gascity cannot import beads' internal storage
// package; the method signatures use only publicly aliased beads types, so a
// type assertion on the storage handle reaches them — the same technique
// rawDBGetter uses for DB() access. These are the exact bulk loads `bd export`
// performs.
type nativeSnapshotBulkReader interface {
	GetLabelsForIssues(ctx context.Context, ids []string) (map[string][]string, error)
	GetDependencyRecordsForIssues(ctx context.Context, ids []string) (map[string][]*beadslib.Dependency, error)
	GetCommentsForIssues(ctx context.Context, ids []string) (map[string][]*beadslib.Comment, error)
	GetCommentCounts(ctx context.Context, ids []string) (map[string]int, error)
	GetDependencyCounts(ctx context.Context, ids []string) (map[string]*beadslib.DependencyCounts, error)
}

// nativeExportRecord mirrors bd export's per-line record: a "_type":"issue"
// discriminator wrapping the issue with its relational counts, so the native leg
// emits objects field-identical to `bd export` and the two legs interchange.
type nativeExportRecord struct {
	RecordType string `json:"_type"`
	*beadslib.IssueWithCounts
}

// ExportBeadSnapshots sources raw bead snapshots via direct reads over the full
// column set, producing JSONL objects field-identical to `bd export` so the
// bd-CLI and native legs are interchangeable. It reads all statuses (closed rows
// included), excluding templates always; ephemeral (wisp) rows are excluded
// unless opts.IncludeEphemeral is set (the unify TierBoth demand).
//
// Deviation from bd export: this leg does not pre-exclude infra bead TYPES
// (agents/roles/messages). The consuming migration filters the decoded stream to
// coordclass.ClassWork, which removes them regardless, so the effective work set
// is identical; excluding them here would require an internal bd type list this
// module cannot reach.
func (s *NativeDoltStore) ExportBeadSnapshots(ctx context.Context, opts ExportOptions) ([]Snapshot, error) {
	storage, release, err := s.acquireStorage()
	if err != nil {
		return nil, err
	}
	defer release()
	if ctx == nil {
		ctx = context.Background()
	}
	cctx, cancel := nativeDoltOperationContext(ctx)
	defer cancel()

	bulk, ok := storage.(nativeSnapshotBulkReader)
	if !ok {
		return nil, fmt.Errorf("native export: %w: storage does not expose bulk relational reads", ErrExportUnsupported)
	}

	notTemplate := false
	filter := beadslib.IssueFilter{Limit: 0, IsTemplate: &notTemplate}
	if !opts.IncludeEphemeral {
		notEphemeral := false
		filter.Ephemeral = &notEphemeral
	}
	issues, err := storage.SearchIssues(cctx, "", filter)
	if err != nil {
		return nil, fmt.Errorf("native export: searching issues: %w", err)
	}
	snaps, err := nativeSnapshotsFromIssues(cctx, bulk, issues)
	if err != nil {
		return nil, fmt.Errorf("native export: %w", err)
	}
	return snaps, nil
}

// GetBeadSnapshots reads raw snapshots for the given ids through the same bulk
// relational surface the export leg uses, bounded to the id set so it never
// becomes a full-store scan. Ids not present are simply absent from the result.
func (s *NativeDoltStore) GetBeadSnapshots(ctx context.Context, ids []string) ([]Snapshot, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	storage, release, err := s.acquireStorage()
	if err != nil {
		return nil, err
	}
	defer release()
	if ctx == nil {
		ctx = context.Background()
	}
	cctx, cancel := nativeDoltOperationContext(ctx)
	defer cancel()

	bulk, ok := storage.(nativeSnapshotBulkReader)
	if !ok {
		return nil, fmt.Errorf("native fetch: %w: storage does not expose bulk relational reads", ErrFetchUnsupported)
	}
	issues, err := storage.GetIssuesByIDs(cctx, ids)
	if err != nil {
		return nil, fmt.Errorf("native fetch: reading issues: %w", err)
	}
	snaps, err := nativeSnapshotsFromIssues(cctx, bulk, issues)
	if err != nil {
		return nil, fmt.Errorf("native fetch: %w", err)
	}
	return snaps, nil
}

// nativeSnapshotsFromIssues attaches each issue's relational data and marshals it
// into a bd-export-shaped record, decoded into a Snapshot (skipping the records
// DecodeSnapshot skips, e.g. templates).
func nativeSnapshotsFromIssues(ctx context.Context, bulk nativeSnapshotBulkReader, issues []*beadslib.Issue) ([]Snapshot, error) {
	if len(issues) == 0 {
		return nil, nil
	}
	ids := make([]string, 0, len(issues))
	for _, issue := range issues {
		if issue != nil {
			ids = append(ids, issue.ID)
		}
	}
	labels, err := bulk.GetLabelsForIssues(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("loading labels: %w", err)
	}
	deps, err := bulk.GetDependencyRecordsForIssues(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("loading dependencies: %w", err)
	}
	comments, err := bulk.GetCommentsForIssues(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("loading comments: %w", err)
	}
	commentCounts, err := bulk.GetCommentCounts(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("loading comment counts: %w", err)
	}
	depCounts, err := bulk.GetDependencyCounts(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("loading dependency counts: %w", err)
	}

	snaps := make([]Snapshot, 0, len(issues))
	for _, issue := range issues {
		if issue == nil {
			continue
		}
		issue.Labels = labels[issue.ID]
		issue.Dependencies = deps[issue.ID]
		issue.Comments = comments[issue.ID]
		sanitizeNativeExportTimes(issue)

		counts := depCounts[issue.ID]
		if counts == nil {
			counts = &beadslib.DependencyCounts{}
		}
		record := nativeExportRecord{
			RecordType: snapshotRecordType,
			IssueWithCounts: &beadslib.IssueWithCounts{
				Issue:           issue,
				DependencyCount: counts.DependencyCount,
				DependentCount:  counts.DependentCount,
				CommentCount:    commentCounts[issue.ID],
			},
		}
		raw, err := json.Marshal(record)
		if err != nil {
			return nil, fmt.Errorf("marshaling %s: %w", issue.ID, err)
		}
		snap, keep, err := DecodeSnapshot(raw)
		if err != nil {
			return nil, fmt.Errorf("decoding %s: %w", issue.ID, err)
		}
		if keep {
			snaps = append(snaps, snap)
		}
	}
	return snaps, nil
}

// sanitizeNativeExportTimes replaces Go zero-value created/updated clocks with
// the Unix epoch, mirroring bd export: a NULL datetime scanned as time.Time{}
// (year 0001) cannot be JSON-marshaled.
func sanitizeNativeExportTimes(issue *beadslib.Issue) {
	epoch := time.Unix(0, 0).UTC()
	if issue.CreatedAt.IsZero() {
		issue.CreatedAt = epoch
	}
	if issue.UpdatedAt.IsZero() {
		issue.UpdatedAt = epoch
	}
}

// ImportBeadSnapshots applies snapshots with the guarded upsert directly in one
// native Dolt transaction, mirroring bd import's semantics: absent -> insert;
// incoming updated_at strictly newer (or a conditional AllowStaleIDs tie
// override) -> replace; second-granularity tie -> keep local and merge aux;
// strictly older -> skip. Dependency edges apply after every issue write, with
// bd import's tolerance (dangling/cycle/cross-type edges are reported, not
// fatal, and never roll back the batch).
//
// Hook silence: the write path is the native storage layer, which fires no bd
// shell hooks and emits no gc events — nothing wires event emission here.
//
// Unlike the BdStore leg, ConflictSkip and AllowStaleIDs need no capability
// probe: this leg implements both directly. Cross-prefix imports rely on the
// store's configured allowed_prefixes (the consuming migration sets them),
// exactly as bd's own prefix validation does.
func (s *NativeDoltStore) ImportBeadSnapshots(ctx context.Context, snaps []Snapshot, opts ImportOptions) (ImportReport, error) {
	if err := opts.validate(); err != nil {
		return ImportReport{}, err
	}
	if len(snaps) == 0 {
		return ImportReport{}, nil
	}
	storage, release, err := s.acquireStorage()
	if err != nil {
		return ImportReport{}, err
	}
	defer release()
	if ctx == nil {
		ctx = context.Background()
	}
	cctx, cancel := context.WithTimeout(ctx, nativeImportDeadline(len(snaps)))
	defer cancel()

	var report ImportReport
	commitMsg := fmt.Sprintf("gc: import %d bead snapshots", len(snaps))
	if err := storage.RunInTransaction(cctx, commitMsg, func(tx beadslib.Transaction) error {
		backend := &nativeImportBackend{ctx: cctx, tx: tx, actor: s.actor}
		r, err := runGuardedUpsert(cctx, snaps, opts, backend)
		if err != nil {
			return err
		}
		report = r
		return nil
	}); err != nil {
		return ImportReport{}, fmt.Errorf("native import: %w", err)
	}
	return report, nil
}

// nativeImportDeadline scales the transaction budget with batch size, on top of
// the flat per-command floor, so a large migration batch completes instead of
// dying at the flat deadline mid-write. It is shared with the bd-leg import
// timeout.
func nativeImportDeadline(rows int) time.Duration {
	const perRow = 50 * time.Millisecond
	return bdCommandTimeout + time.Duration(rows)*perRow
}

// nativeImportBackend adapts a native Dolt transaction to importBackend, so the
// shared guarded-upsert decision logic drives the writes. The writes reuse bd's
// own transaction primitives (CreateIssue's ODKU insert, AddLabel, AddDependency,
// ImportIssueComment) rather than hand-rolled SQL, so the full column fidelity,
// content-hash, and derived-column recompute bd performs are preserved.
type nativeImportBackend struct {
	ctx   context.Context
	tx    beadslib.Transaction
	actor string
}

func (b *nativeImportBackend) updatedAtOf(id string) (time.Time, bool, error) {
	issue, err := b.tx.GetIssue(b.ctx, id)
	if err != nil {
		if nativeUpstreamNotFound(err) {
			return time.Time{}, false, nil
		}
		return time.Time{}, false, err
	}
	if issue == nil {
		return time.Time{}, false, nil
	}
	return issue.UpdatedAt, true, nil
}

func (b *nativeImportBackend) writeIssue(snap Snapshot) error {
	issue, err := snapshotToNativeIssue(snap)
	if err != nil {
		return err
	}
	// CreateIssue upserts by id in a single write (ODKU), so a closed bead is
	// written closed and never transits an open state. It persists the issue's
	// labels and comments; dependency edges are applied separately in the
	// guarded-upsert dep phase, so they are cleared here to avoid ambiguity.
	issue.Dependencies = nil
	return b.tx.CreateIssue(b.ctx, issue, b.actor)
}

func (b *nativeImportBackend) mergeLabels(id string, labels []string) error {
	for _, label := range labels {
		if err := b.tx.AddLabel(b.ctx, id, label, b.actor); err != nil {
			return err
		}
	}
	return nil
}

// mergeComments adds a tie-kept row's comments that the destination does not
// already carry (matched by author+text+second-granularity created_at),
// mirroring bd import's PersistComments dedup so ties merge comments on the
// native leg too.
func (b *nativeImportBackend) mergeComments(snap Snapshot) error {
	incoming := snap.commentRecords()
	if len(incoming) == 0 {
		return nil
	}
	have := make(map[string]bool)
	existing, err := b.tx.GetIssueComments(b.ctx, snap.ID())
	if err != nil {
		// The nocgo embedded transaction shim implements neither comment read nor
		// import; a real (cgo) Dolt transaction implements both. When the read is
		// unavailable, skip the tie comment merge — a bounded gap the spec's
		// allow-stale tie re-import (which full-row-replaces comments) repairs.
		if isNativeNotImplemented(err) {
			return nil
		}
		return err
	}
	for _, c := range existing {
		if c != nil {
			have[commentKey(c.Author, c.Text, c.CreatedAt)] = true
		}
	}
	for _, c := range incoming {
		if have[commentKey(c.Author, c.Text, c.CreatedAt)] {
			continue
		}
		createdAt := c.CreatedAt
		if createdAt.IsZero() {
			createdAt = time.Now().UTC()
		}
		if _, err := b.tx.ImportIssueComment(b.ctx, snap.ID(), c.Author, c.Text, createdAt); err != nil {
			if isNativeNotImplemented(err) {
				return nil
			}
			return err
		}
	}
	return nil
}

// isNativeNotImplemented reports whether err is the embedded-transaction shim's
// "not implemented" response for a capability a real cgo Dolt transaction
// provides. The tie comment merge degrades gracefully on such backends.
func isNativeNotImplemented(err error) bool {
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "not implemented")
}

func commentKey(author, text string, createdAt time.Time) string {
	return author + "\x1f" + text + "\x1f" + createdAt.UTC().Truncate(time.Second).Format(time.RFC3339)
}

// addDep records a dependency edge with bd import's tolerance. It skips (reports)
// a dangling non-external target, a cycle, or a cross-type block instead of
// aborting; external-target edges are written without an existence check
// (tx.AddDependency bypasses the existence and cross-type checks for
// "external:"-prefixed targets and applies only the cycle check). Provenance
// (created_at/created_by/metadata) is preserved.
func (b *nativeImportBackend) addDep(dep depRecord) (string, error) {
	existing, err := b.tx.GetDependencyRecords(b.ctx, dep.IssueID)
	if err != nil {
		return "", err
	}
	for _, e := range existing {
		if e != nil && e.DependsOnID == dep.DependsOnID {
			return "", nil // already present (resume idempotency)
		}
	}
	d := &beadslib.Dependency{
		IssueID:     dep.IssueID,
		DependsOnID: dep.DependsOnID,
		Type:        beadslib.DependencyType(dep.Type),
		CreatedBy:   dep.CreatedBy,
		Metadata:    dep.Metadata,
	}
	if !dep.CreatedAt.IsZero() {
		d.CreatedAt = dep.CreatedAt
	}
	if err := b.tx.AddDependency(b.ctx, d, b.actor); err != nil {
		if reason, ok := nativeDepSkipReason(err); ok {
			return reason, nil
		}
		return "", err
	}
	return "", nil
}

// nativeDepSkipReason classifies an AddDependency error as one of bd import's
// tolerated skip conditions (dangling target, cycle, cross-type block), so the
// dep phase reports it rather than aborting the whole migration. Any other error
// is a genuine failure and is returned as ok=false.
func nativeDepSkipReason(err error) (string, bool) {
	if err == nil {
		return "", false
	}
	msg := strings.ToLower(err.Error())
	switch {
	case nativeUpstreamNotFound(err) || strings.Contains(msg, "not found"):
		return "target not found", true
	case strings.Contains(msg, "cycle"):
		return "dependency cycle", true
	case strings.Contains(msg, "can only block"):
		return "cross-type block", true
	default:
		return "", false
	}
}

// snapshotToNativeIssue decodes a snapshot's raw bd-export bytes into a
// beadslib.Issue, preserving every field verbatim (clocks, status, close reason,
// labels, comments). The extra export-only keys (_type, dependency_count, ...)
// are ignored by the decode.
func snapshotToNativeIssue(snap Snapshot) (*beadslib.Issue, error) {
	var issue beadslib.Issue
	if err := json.Unmarshal(snap.RawJSON(), &issue); err != nil {
		return nil, fmt.Errorf("decoding snapshot %s into native issue: %w", snap.ID(), err)
	}
	return &issue, nil
}
