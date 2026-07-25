package beads

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

// ErrExportUnsupported reports that a store cannot produce raw bead snapshots
// (ExportBeadSnapshots). The class-store SQLite backends return it because the
// topology migrations never source work beads from a class store.
var ErrExportUnsupported = errors.New("bead snapshot export unsupported")

// ErrImportUnsupported reports that a store cannot apply raw bead snapshots
// (ImportBeadSnapshots).
var ErrImportUnsupported = errors.New("bead snapshot import unsupported")

// ErrFetchUnsupported reports that a store cannot read raw bead snapshots by id
// (GetBeadSnapshots).
var ErrFetchUnsupported = errors.New("bead snapshot fetch unsupported")

// ErrBdCapabilityMissing reports that the pinned bd binary lacks the
// workspace-prefix-mint capability the guarded-upsert options ConflictSkip and
// AllowStaleIDs require. Detected via the fork-proof capability probe
// (bd version --json capabilities), never a version-number comparison. A plain
// guarded-upsert import works on any bd; only the option-carrying import path
// returns this.
var ErrBdCapabilityMissing = errors.New("bd lacks workspace-prefix-mint capability")

// ErrConflictSkipWithAllowStale reports that an import combined ConflictSkip with
// AllowStaleIDs. The two are mutually exclusive — one never overwrites an
// existing row, the other forces an overwrite — mirroring bd import's own
// mutual-exclusion check. Both legs reject the combination with this error.
var ErrConflictSkipWithAllowStale = errors.New("conflict-skip and allow-stale are mutually exclusive: one never overwrites an existing row, the other forces an overwrite")

// ErrExportOwnerExcludeConfigured reports that the source scope has a
// bd `export.exclude_owners` (or legacy `export.exclude_owner`) config that
// `bd export` honors silently, which would drop owner-keyed beads from the
// migration stream and make copy-verify blind to them. The operator must clear
// the config for the migration or use the native leg.
var ErrExportOwnerExcludeConfigured = errors.New("bd export.exclude_owners config is set; clear it for the migration or use the native export leg")

// snapshotWorkspacePrefixMintCapability is the fork-proof capability key the
// prerequisite bd slice advertises in `bd version --json`. It ships the
// `--conflict-skip` flag and per-id `--allow-stale` behavior together, so a
// single probe gates both guarded-upsert options.
const snapshotWorkspacePrefixMintCapability = "workspace-prefix-mint"

// Snapshot is a raw-fidelity carrier for one exported bead — the bd-export
// JSONL object held losslessly plus a decoded envelope of the fields Gas City
// must read to route and verify the row.
//
// The raw payload is stored as json.RawMessage on purpose. This is NOT a wire
// type (it never crosses an HTTP/SSE boundary and never appears in the OpenAPI
// spec); it is a migration-fidelity carrier. The typed-wire invariant exists so
// domain state cannot smuggle across the API as untyped JSON, but a migration
// must reproduce a bead byte-for-byte across backends. Decoding through the gc
// Bead struct would silently fabricate close clocks and drop comment history, so
// the raw bytes are the source of truth and the envelope is a read-only
// projection of them.
//
// FIDELITY SCOPE (honest bound): the raw carrier and the BdStore leg preserve
// every column verbatim, including any a future bd adds (JSONL passthrough). The
// NativeDoltStore write leg decodes into the pinned beadslib.Issue, so a column
// unknown to that library version is dropped on the NATIVE write — the bd leg is
// the full-fidelity path when lib-vs-CLI column skew exists.
type Snapshot struct {
	raw json.RawMessage
	env snapshotEnvelope
}

// snapshotEnvelope decodes the subset of the bd-export record Gas City reads:
// identity, status, classification inputs, clocks, dependency edges (with
// provenance), and comments. Every other exported column is preserved only in
// the raw bytes.
type snapshotEnvelope struct {
	ID         string            `json:"id"`
	Status     string            `json:"status"`
	IssueType  string            `json:"issue_type"`
	IsTemplate bool              `json:"is_template"`
	Labels     []string          `json:"labels"`
	Metadata   StringMap         `json:"metadata"`
	CreatedAt  time.Time         `json:"created_at"`
	UpdatedAt  time.Time         `json:"updated_at"`
	ClosedAt   *time.Time        `json:"closed_at"`
	Deps       []snapshotDep     `json:"dependencies"`
	Comments   []snapshotComment `json:"comments"`
}

// snapshotDep decodes one dependency edge with its provenance columns so the
// native write can preserve created_at/created_by/metadata like the bd leg does.
type snapshotDep struct {
	IssueID     string    `json:"issue_id"`
	DependsOnID string    `json:"depends_on_id"`
	Type        string    `json:"type"`
	CreatedAt   time.Time `json:"created_at"`
	CreatedBy   string    `json:"created_by"`
	Metadata    string    `json:"metadata"`
}

// snapshotComment decodes one comment for the tie-arm merge.
type snapshotComment struct {
	Author    string    `json:"author"`
	Text      string    `json:"text"`
	CreatedAt time.Time `json:"created_at"`
}

// depRecord is the guarded-upsert dep phase's unit: a resolved edge plus its
// provenance, so both backends apply an edge identically to bd import.
type depRecord struct {
	IssueID     string
	DependsOnID string
	Type        string
	CreatedAt   time.Time
	CreatedBy   string
	Metadata    string
}

// snapshotRecordType is the discriminator bd stamps on every issue line of a
// `bd export` stream (memory lines carry "memory"). We accept "issue" and the
// empty string (a bare issue object, e.g. `bd show --json`) and skip everything
// else.
const snapshotRecordType = "issue"

// tombstoneStatus is the pseudo-status pre-v0.50 bd exported for deleted rows.
// bd import skips them; snapshot decoding does the same so a re-import never
// resurrects a tombstone as a real bead.
const tombstoneStatus = "tombstone"

// DecodeSnapshot parses one bd-export JSONL line into a Snapshot. It returns
// keep=false (with a nil error) for lines that are not importable work beads —
// blank lines, memory records ("_type":"memory"), the beads-jsonl schema header,
// tombstone rows, and template molecules ("is_template":true) — mirroring bd
// import's skip set (plus templates, which a `bd export --all` stream carries
// but the migration never targets) so the plain and --all export paths yield the
// same effective set.
func DecodeSnapshot(line []byte) (snap Snapshot, keep bool, err error) {
	trimmed := trimJSONLLine(line)
	if len(trimmed) == 0 {
		return Snapshot{}, false, nil
	}
	var peek struct {
		Type       string `json:"_type"`
		Schema     string `json:"_schema"`
		Status     string `json:"status"`
		IsTemplate bool   `json:"is_template"`
	}
	if err := json.Unmarshal(trimmed, &peek); err != nil {
		return Snapshot{}, false, fmt.Errorf("decoding bead snapshot: %w", err)
	}
	if peek.Schema != "" && peek.Type == "" {
		return Snapshot{}, false, nil // beads-jsonl provenance header
	}
	if peek.Type != "" && peek.Type != snapshotRecordType {
		return Snapshot{}, false, nil // memory or other non-issue record
	}
	if peek.Status == tombstoneStatus || peek.IsTemplate {
		return Snapshot{}, false, nil
	}
	var env snapshotEnvelope
	if err := json.Unmarshal(trimmed, &env); err != nil {
		return Snapshot{}, false, fmt.Errorf("decoding bead snapshot: %w", err)
	}
	if strings.TrimSpace(env.ID) == "" {
		return Snapshot{}, false, fmt.Errorf("decoding bead snapshot: record has no id")
	}
	return Snapshot{raw: append(json.RawMessage(nil), trimmed...), env: env}, true, nil
}

// DecodeSnapshots parses a full bd-export JSONL stream, skipping the records
// DecodeSnapshot skips.
func DecodeSnapshots(stream []byte) ([]Snapshot, error) {
	var out []Snapshot
	for i, line := range splitJSONLLines(stream) {
		snap, keep, err := DecodeSnapshot(line)
		if err != nil {
			return nil, fmt.Errorf("bead snapshot line %d: %w", i+1, err)
		}
		if keep {
			out = append(out, snap)
		}
	}
	return out, nil
}

// newSnapshotFromRaw builds a Snapshot from already-validated raw bytes,
// re-decoding the envelope. Used by StampMetadata/StampLabel, which rewrite the
// raw JSON.
func newSnapshotFromRaw(raw []byte) (Snapshot, error) {
	var env snapshotEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return Snapshot{}, fmt.Errorf("re-decoding stamped snapshot: %w", err)
	}
	return Snapshot{raw: append(json.RawMessage(nil), raw...), env: env}, nil
}

// RawJSON returns the exact bd-export object bytes for this bead. The returned
// slice is a copy; callers may not mutate the snapshot through it.
func (s Snapshot) RawJSON() []byte {
	return append([]byte(nil), s.raw...)
}

// MarshalJSON returns the raw bd-export object verbatim, so a re-marshaled
// snapshot round-trips byte-for-byte with the source export.
func (s Snapshot) MarshalJSON() ([]byte, error) {
	if len(s.raw) == 0 {
		return []byte("null"), nil
	}
	return append([]byte(nil), s.raw...), nil
}

// ID returns the bead's id.
func (s Snapshot) ID() string { return s.env.ID }

// Status returns the bead's raw bd status (open, in_progress, closed, ...).
func (s Snapshot) Status() string { return s.env.Status }

// IssueType returns the bead's issue_type.
func (s Snapshot) IssueType() string { return s.env.IssueType }

// Labels returns a copy of the bead's labels.
func (s Snapshot) Labels() []string { return append([]string(nil), s.env.Labels...) }

// Metadata returns a copy of the bead's metadata map.
func (s Snapshot) Metadata() map[string]string {
	if s.env.Metadata == nil {
		return nil
	}
	out := make(map[string]string, len(s.env.Metadata))
	for k, v := range s.env.Metadata {
		out[k] = v
	}
	return out
}

// CreatedAt returns the bead's created_at clock.
func (s Snapshot) CreatedAt() time.Time { return s.env.CreatedAt }

// UpdatedAt returns the bead's updated_at clock — the guard the upsert compares.
func (s Snapshot) UpdatedAt() time.Time { return s.env.UpdatedAt }

// ClosedAt returns the bead's closed_at clock (nil for open beads).
func (s Snapshot) ClosedAt() *time.Time {
	if s.env.ClosedAt == nil {
		return nil
	}
	c := *s.env.ClosedAt
	return &c
}

// Deps returns the bead's dependency edges as gc Deps (issue/target/type), the
// shape DiffSnapshotLinks compares.
func (s Snapshot) Deps() []Dep {
	recs := s.depRecords()
	if len(recs) == 0 {
		return nil
	}
	out := make([]Dep, 0, len(recs))
	for _, r := range recs {
		out = append(out, Dep{IssueID: r.IssueID, DependsOnID: r.DependsOnID, Type: r.Type})
	}
	return out
}

// depRecords returns the bead's dependency edges with provenance, defaulting the
// source id to this bead and the type to "blocks", and dropping edges with no
// target.
func (s Snapshot) depRecords() []depRecord {
	if len(s.env.Deps) == 0 {
		return nil
	}
	out := make([]depRecord, 0, len(s.env.Deps))
	for _, d := range s.env.Deps {
		issueID := strings.TrimSpace(d.IssueID)
		if issueID == "" {
			issueID = s.env.ID
		}
		dependsOn := strings.TrimSpace(d.DependsOnID)
		if dependsOn == "" {
			continue
		}
		depType := strings.TrimSpace(d.Type)
		if depType == "" {
			depType = "blocks"
		}
		out = append(out, depRecord{
			IssueID:     issueID,
			DependsOnID: dependsOn,
			Type:        depType,
			CreatedAt:   d.CreatedAt,
			CreatedBy:   d.CreatedBy,
			Metadata:    d.Metadata,
		})
	}
	return out
}

// commentRecords returns the bead's comments for the tie-arm aux merge.
func (s Snapshot) commentRecords() []snapshotComment {
	return append([]snapshotComment(nil), s.env.Comments...)
}

// Bead returns a beads.Bead carrying the fields coordclass.Classify reads
// (Type, Labels, Metadata) plus identity, status, clocks, and deps, so a
// migration can filter ClassWork rows and copy-verify without decoding the raw
// bytes twice. It is a projection for routing and verification only — never a
// carrier for a write, which must go through ImportBeadSnapshots so no field is
// silently fabricated.
func (s Snapshot) Bead() Bead {
	return Bead{
		ID:           s.env.ID,
		Status:       mapBdStatus(s.env.Status),
		Type:         s.env.IssueType,
		CreatedAt:    s.env.CreatedAt,
		UpdatedAt:    s.env.UpdatedAt,
		Labels:       s.Labels(),
		Metadata:     s.env.Metadata,
		Dependencies: s.Deps(),
	}
}

// StampMetadata returns a copy of the snapshot with metadata[key] set to value
// in the raw JSON, leaving every other field intact. Migrations use it to stamp
// gc.topology_source before import (the remote collision discriminator).
//
// The rewrite re-marshals the object, so JSON key order is normalized, but no
// value other than the injected metadata key changes — order-insensitive-lossless,
// which is all the caller needs because the import re-parses the object anyway.
func (s Snapshot) StampMetadata(key, value string) (Snapshot, error) {
	if strings.TrimSpace(key) == "" {
		return Snapshot{}, fmt.Errorf("stamping snapshot metadata: empty key")
	}
	obj, err := s.rawObject()
	if err != nil {
		return Snapshot{}, fmt.Errorf("stamping snapshot metadata: %w", err)
	}
	meta := map[string]json.RawMessage{}
	if raw, ok := obj["metadata"]; ok && len(raw) > 0 {
		if err := json.Unmarshal(raw, &meta); err != nil {
			return Snapshot{}, fmt.Errorf("stamping snapshot metadata: decoding existing metadata: %w", err)
		}
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return Snapshot{}, fmt.Errorf("stamping snapshot metadata: %w", err)
	}
	meta[key] = encoded
	metaRaw, err := json.Marshal(meta)
	if err != nil {
		return Snapshot{}, fmt.Errorf("stamping snapshot metadata: %w", err)
	}
	obj["metadata"] = metaRaw
	return s.reassemble(obj, "stamping snapshot metadata")
}

// StampLabel returns a copy of the snapshot with label appended to the labels
// array in the raw JSON (deduplicated), leaving every other field intact.
// Migrations use it to stamp the gc.topology_migrating quarantine label on every
// copied row atomically with the row (both legs persist a snapshot's labels on
// write), so an aborted one-shot never exposes unquarantined mid-copy beads.
func (s Snapshot) StampLabel(label string) (Snapshot, error) {
	if strings.TrimSpace(label) == "" {
		return Snapshot{}, fmt.Errorf("stamping snapshot label: empty label")
	}
	obj, err := s.rawObject()
	if err != nil {
		return Snapshot{}, fmt.Errorf("stamping snapshot label: %w", err)
	}
	var labels []string
	if raw, ok := obj["labels"]; ok && len(raw) > 0 {
		if err := json.Unmarshal(raw, &labels); err != nil {
			return Snapshot{}, fmt.Errorf("stamping snapshot label: decoding existing labels: %w", err)
		}
	}
	for _, l := range labels {
		if l == label {
			return s, nil // already present; no change
		}
	}
	labels = append(labels, label)
	labelsRaw, err := json.Marshal(labels)
	if err != nil {
		return Snapshot{}, fmt.Errorf("stamping snapshot label: %w", err)
	}
	obj["labels"] = labelsRaw
	return s.reassemble(obj, "stamping snapshot label")
}

func (s Snapshot) rawObject() (map[string]json.RawMessage, error) {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(s.raw, &obj); err != nil {
		return nil, err
	}
	return obj, nil
}

func (s Snapshot) reassemble(obj map[string]json.RawMessage, op string) (Snapshot, error) {
	newRaw, err := json.Marshal(obj)
	if err != nil {
		return Snapshot{}, fmt.Errorf("%s: %w", op, err)
	}
	return newSnapshotFromRaw(newRaw)
}

// DepPair identifies one dependency edge (issueID depends on dependsOnID). It is
// the id-pair vocabulary ImportReport.SkippedDependencies uses.
type DepPair struct {
	IssueID     string
	DependsOnID string
}

// ExportOptions configures ExportBeadSnapshots.
type ExportOptions struct {
	// IncludeEphemeral crosses the ephemeral (wisp) tier as well as the durable
	// tier — the unify snapshot step's TierBoth demand, so in-flight wisp
	// molecules are not stranded in the old rig database. When false (the
	// default), ephemeral rows are excluded; a work bead minted with ephemeral
	// storage out of band therefore requires this option to cross.
	IncludeEphemeral bool
}

// ImportOptions configures the guarded upsert of ImportBeadSnapshots.
type ImportOptions struct {
	// ConflictSkip makes the import insert-if-new: a snapshot whose id already
	// exists is left entirely untouched — no scalar rewrite and no
	// label/comment/dependency side-table merge. It is the remote migration's
	// first-copy guard, so a probe that raced a concurrent writer can never
	// overwrite an existing row. Mutually exclusive with AllowStaleIDs.
	ConflictSkip bool
	// AllowStaleIDs lists ids whose stale guard is overridden: for exactly these
	// ids an incoming row is written even when it TIES the stored row (the
	// bounded same-second tie re-import). The override is CONDITIONAL — an id
	// whose destination has advanced STRICTLY NEWER than the incoming snapshot is
	// NOT overwritten (it is reported StaleSkipped for the next residue pass), so
	// a destination close/claim landing after a tie report is never clobbered.
	//
	// PRECONDITION (caller contract): the flagged ids' source must be
	// authoritative for the duration of the call — i.e. the caller re-derives the
	// forced set from the report of the SAME import (never a prior one) and, in
	// post-re-point residue passes against a live destination, only re-imports
	// ids no concurrent writer owns. There is an epsilon race between the bd
	// leg's per-id destination probe and the forced import; it is acceptable ONLY
	// under this source-is-authoritative precondition. Mutually exclusive with
	// ConflictSkip.
	AllowStaleIDs []string
}

// validate rejects the ConflictSkip + AllowStaleIDs combination on both legs,
// mirroring bd import's mutual-exclusion check.
func (o ImportOptions) validate() error {
	if o.ConflictSkip && len(o.allowStaleSet()) > 0 {
		return ErrConflictSkipWithAllowStale
	}
	return nil
}

func (o ImportOptions) allowStaleSet() map[string]bool {
	if len(o.AllowStaleIDs) == 0 {
		return nil
	}
	set := make(map[string]bool, len(o.AllowStaleIDs))
	for _, id := range o.AllowStaleIDs {
		if id = strings.TrimSpace(id); id != "" {
			set[id] = true
		}
	}
	return set
}

// ImportReport summarizes what a guarded-upsert import did, using bd import's
// report vocabulary. Every arm lists the affected ids so a residue pass can diff
// the flagged rows.
//
// DRAIN-SIGNAL CAVEAT: on the BdStore leg, Inserted may include existing
// equal-clock rows whose content is identical (bd reports only content-differing
// rows as tie/updated, so a content-identical re-import lands in `ids` and is
// classified Inserted). Inserted therefore MUST NOT be used as a
// progress/drain signal — a residue re-import of an already-drained source would
// loop forever. Drain state is computed by per-id updated_at comparison via
// GetBeadSnapshots, never by counting Inserted.
type ImportReport struct {
	// Inserted lists ids that were absent and newly created. See the
	// drain-signal caveat above for the BdStore leg's classification limits.
	Inserted []string
	// Updated lists existing ids the import rewrote (incoming strictly newer, or
	// a forced tie override via AllowStaleIDs).
	Updated []string
	// KeptLocal lists ids whose incoming updated_at tied the stored row's
	// (second granularity); the local scalar columns are kept and their aux data
	// (labels, comments, deps) still merges. bd calls these tie_kept_local_ids.
	KeptLocal []string
	// StaleSkipped lists ids skipped because the incoming row is strictly older
	// than the stored row (including a forced id whose destination advanced past
	// the source); neither scalars nor aux data are touched.
	StaleSkipped []string
	// ConflictSkipped lists ids left untouched under ConflictSkip because they
	// already existed. Populated on both legs (the capable bd emits
	// conflict_skipped_ids, which the ConflictSkip path already requires).
	ConflictSkipped []string
	// SkippedDependencies lists dep edges intentionally not applied — a dangling
	// non-external target, a cycle, or a cross-type block — never a silently
	// dropped edge. Mirrors bd import's OnSkippedDependency tolerance.
	SkippedDependencies []DepPair
}

func (r *ImportReport) addSkippedDependency(issueID, dependsOnID string) {
	r.SkippedDependencies = append(r.SkippedDependencies, DepPair{IssueID: issueID, DependsOnID: dependsOnID})
}

// importBackend is the write surface runGuardedUpsert drives. It is implemented
// by NativeDoltStore's transaction-scoped adapter and by the in-memory test
// double, so the guarded-upsert decision logic is shared and independently
// testable without a live backend. Every method operates against a single
// logical unit of work owned by the backend.
type importBackend interface {
	// updatedAtOf returns the stored updated_at (UTC) for id and whether the row
	// exists.
	updatedAtOf(id string) (updatedAt time.Time, exists bool, err error)
	// writeIssue inserts-or-replaces the full issue row (scalars plus labels and
	// comments) from the snapshot in a single write, so a closed bead is written
	// closed directly and never transits through an open state.
	writeIssue(snap Snapshot) error
	// mergeLabels additively adds labels to an existing row (used for tie-kept
	// rows, whose scalars are not rewritten but whose aux data still merges).
	mergeLabels(id string, labels []string) error
	// mergeComments additively adds a tie-kept row's comments (deduplicated by
	// content), mirroring bd import's comment merge for ties.
	mergeComments(snap Snapshot) error
	// addDep records a dependency edge with bd import's tolerance. It returns a
	// non-empty skippedReason (and nil err) when the edge is intentionally not
	// applied — a dangling non-external target, a cycle, or a cross-type block —
	// and reserves err for genuine I/O failures. External-target edges are
	// written without an existence check.
	addDep(dep depRecord) (skippedReason string, err error)
}

// runGuardedUpsert applies the snapshot set to backend with the guarded-upsert
// semantics bd import implements: absent -> insert; present with incoming
// updated_at strictly newer (or a conditional AllowStaleIDs tie override) ->
// replace; second-granularity tie -> keep local and report, merging aux;
// strictly older -> skip and report. Dependency edges are applied only after
// every issue write, and non-applied edges (dangling/cycle/cross-type) are
// reported rather than aborting the batch. It is the resume mechanism:
// re-running converges every row to the newest content.
func runGuardedUpsert(_ context.Context, snaps []Snapshot, opts ImportOptions, backend importBackend) (ImportReport, error) {
	if err := opts.validate(); err != nil {
		return ImportReport{}, err
	}
	var report ImportReport
	allowStale := opts.allowStaleSet()
	// auxEligible records ids that received a scalar write (insert/replace) or a
	// tie-keep: exactly the rows whose dependency edges bd applies.
	auxEligible := make(map[string]bool, len(snaps))

	for _, snap := range snaps {
		id := snap.ID()
		storedAt, exists, err := backend.updatedAtOf(id)
		if err != nil {
			return ImportReport{}, fmt.Errorf("import %s: reading stored row: %w", id, err)
		}
		if !exists {
			if err := backend.writeIssue(snap); err != nil {
				return ImportReport{}, fmt.Errorf("import %s: inserting: %w", id, err)
			}
			report.Inserted = append(report.Inserted, id)
			auxEligible[id] = true
			continue
		}
		if opts.ConflictSkip {
			report.ConflictSkipped = append(report.ConflictSkipped, id)
			continue
		}
		incoming := snap.UpdatedAt().UTC().Truncate(time.Second)
		stored := storedAt.UTC().Truncate(time.Second)
		switch {
		case allowStale[id]:
			// Conditional override: overwrite a tie or an older-destination row,
			// but never clobber a destination that advanced strictly past the
			// source — report it StaleSkipped for the next residue pass.
			if incoming.Before(stored) {
				report.StaleSkipped = append(report.StaleSkipped, id)
				break
			}
			if err := backend.writeIssue(snap); err != nil {
				return ImportReport{}, fmt.Errorf("import %s: forced replace: %w", id, err)
			}
			report.Updated = append(report.Updated, id)
			auxEligible[id] = true
		case incoming.Before(stored):
			report.StaleSkipped = append(report.StaleSkipped, id)
		case incoming.Equal(stored):
			report.KeptLocal = append(report.KeptLocal, id)
			if err := backend.mergeLabels(id, snap.Labels()); err != nil {
				return ImportReport{}, fmt.Errorf("import %s: merging tie labels: %w", id, err)
			}
			if err := backend.mergeComments(snap); err != nil {
				return ImportReport{}, fmt.Errorf("import %s: merging tie comments: %w", id, err)
			}
			auxEligible[id] = true
		default: // incoming.After(stored)
			if err := backend.writeIssue(snap); err != nil {
				return ImportReport{}, fmt.Errorf("import %s: replacing: %w", id, err)
			}
			report.Updated = append(report.Updated, id)
			auxEligible[id] = true
		}
	}

	// Dependency edges apply after every issue write so an intra-batch edge sees
	// its freshly written target. Edges on stale-skipped / conflict-skipped rows
	// never enter this phase.
	for _, snap := range snaps {
		if !auxEligible[snap.ID()] {
			continue
		}
		for _, dep := range snap.depRecords() {
			reason, err := backend.addDep(dep)
			if err != nil {
				return ImportReport{}, fmt.Errorf("import %s: adding dep %s: %w", dep.IssueID, dep.DependsOnID, err)
			}
			if reason != "" {
				report.addSkippedDependency(dep.IssueID, dep.DependsOnID)
			}
		}
	}
	return report, nil
}

// LinkDiff reports the dependency edges and labels present in a source snapshot
// but missing from the destination bead. DiffSnapshotLinks produces it for the
// residue pass, which drives the missing edges/labels onto stale-skipped and
// tie-kept rows (a row counts as drained only when its edges do).
type LinkDiff struct {
	// MissingDeps are dependency edges in the source not yet on the destination.
	MissingDeps []Dep
	// MissingLabels are labels on the source not yet on the destination.
	MissingLabels []string
}

// Empty reports whether the destination already carries every source edge and
// label — i.e. the row is fully drained.
func (d LinkDiff) Empty() bool {
	return len(d.MissingDeps) == 0 && len(d.MissingLabels) == 0
}

// DiffSnapshotLinks returns the dependency edges and labels present in source
// but missing from dest. It is a pure function over the source snapshot and a
// fetched destination bead; the residue pass fetches dest via Get and applies
// whatever this reports. Edges are compared by (issue, target) — the natural
// identity bd's dependency unique key enforces — so a type-only difference is
// not reported as missing. Comment drift is NOT reported here: the spec's
// residue pass mandates only dep and label repair.
func DiffSnapshotLinks(source Snapshot, dest Bead) LinkDiff {
	var diff LinkDiff

	haveEdge := make(map[[2]string]bool, len(dest.Dependencies))
	for _, d := range dest.Dependencies {
		issueID := strings.TrimSpace(d.IssueID)
		if issueID == "" {
			issueID = dest.ID
		}
		haveEdge[[2]string{issueID, strings.TrimSpace(d.DependsOnID)}] = true
	}
	for _, d := range source.Deps() {
		if !haveEdge[[2]string{d.IssueID, d.DependsOnID}] {
			diff.MissingDeps = append(diff.MissingDeps, d)
		}
	}

	haveLabel := make(map[string]bool, len(dest.Labels))
	for _, l := range dest.Labels {
		haveLabel[l] = true
	}
	seen := make(map[string]bool)
	for _, l := range source.Labels() {
		if !haveLabel[l] && !seen[l] {
			seen[l] = true
			diff.MissingLabels = append(diff.MissingLabels, l)
		}
	}
	sort.Strings(diff.MissingLabels)
	return diff
}

// SnapshotExporter is the optional store capability that produces raw bead
// snapshots for a topology migration. BdStore and NativeDoltStore implement it;
// the class SQLite stores return ErrExportUnsupported.
type SnapshotExporter interface {
	ExportBeadSnapshots(ctx context.Context, opts ExportOptions) ([]Snapshot, error)
}

// SnapshotImporter is the optional store capability that applies raw bead
// snapshots with the guarded upsert.
type SnapshotImporter interface {
	ImportBeadSnapshots(ctx context.Context, snaps []Snapshot, opts ImportOptions) (ImportReport, error)
}

// SnapshotFetcher is the optional store capability that reads raw bead snapshots
// by id — copy-verify's and the remote stamp-check pre-probe's read path,
// bounded per-id so it never becomes a full-store (org-DB) scan.
type SnapshotFetcher interface {
	GetBeadSnapshots(ctx context.Context, ids []string) ([]Snapshot, error)
}

// ExportBeadSnapshotsFrom exports snapshots from store when it implements
// SnapshotExporter, else returns ErrExportUnsupported.
func ExportBeadSnapshotsFrom(ctx context.Context, store Store, opts ExportOptions) ([]Snapshot, error) {
	exporter, ok := store.(SnapshotExporter)
	if !ok {
		return nil, fmt.Errorf("exporting snapshots: %w", ErrExportUnsupported)
	}
	return exporter.ExportBeadSnapshots(ctx, opts)
}

// ImportBeadSnapshotsTo imports snapshots into store when it implements
// SnapshotImporter, else returns ErrImportUnsupported.
func ImportBeadSnapshotsTo(ctx context.Context, store Store, snaps []Snapshot, opts ImportOptions) (ImportReport, error) {
	importer, ok := store.(SnapshotImporter)
	if !ok {
		return ImportReport{}, fmt.Errorf("importing snapshots: %w", ErrImportUnsupported)
	}
	return importer.ImportBeadSnapshots(ctx, snaps, opts)
}

// GetBeadSnapshotsFrom reads snapshots by id from store when it implements
// SnapshotFetcher, else returns ErrFetchUnsupported.
func GetBeadSnapshotsFrom(ctx context.Context, store Store, ids []string) ([]Snapshot, error) {
	fetcher, ok := store.(SnapshotFetcher)
	if !ok {
		return nil, fmt.Errorf("fetching snapshots: %w", ErrFetchUnsupported)
	}
	return fetcher.GetBeadSnapshots(ctx, ids)
}

// marshalSnapshotsJSONL renders snapshots back to a bd-export JSONL stream (one
// raw object per line) for the bd import stdin path.
func marshalSnapshotsJSONL(snaps []Snapshot) []byte {
	var b strings.Builder
	for _, snap := range snaps {
		b.Write(snap.raw)
		b.WriteByte('\n')
	}
	return []byte(b.String())
}

// splitJSONLLines splits a JSONL stream into lines, dropping a trailing empty
// segment. It tolerates both "\n" and "\r\n" terminators.
func splitJSONLLines(stream []byte) [][]byte {
	text := string(stream)
	rawLines := strings.Split(text, "\n")
	out := make([][]byte, 0, len(rawLines))
	for _, line := range rawLines {
		out = append(out, []byte(line))
	}
	if n := len(out); n > 0 && len(trimJSONLLine(out[n-1])) == 0 {
		out = out[:n-1]
	}
	return out
}

// trimJSONLLine trims whitespace and a trailing carriage return from a JSONL line.
func trimJSONLLine(line []byte) []byte {
	return []byte(strings.TrimSpace(string(line)))
}
