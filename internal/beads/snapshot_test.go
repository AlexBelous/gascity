package beads

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"
)

// memImportBackend is an in-memory importBackend for the pure-Go guarded-upsert
// tests. It stores each row's raw bytes, updated_at, label set, comment set, and
// dep set so a round-trip (import then re-read) proves both the decision logic
// and byte fidelity of the Snapshot carrier without a live Dolt backend.
type memImportBackend struct {
	rows   map[string]*memRow
	writes int // writeIssue call count (single-write-per-row assertions)
}

type memRow struct {
	raw       []byte
	updatedAt time.Time
	labels    map[string]bool
	comments  map[string]bool
	deps      map[string]bool
}

func newMemImportBackend() *memImportBackend {
	return &memImportBackend{rows: map[string]*memRow{}}
}

func (m *memImportBackend) updatedAtOf(id string) (time.Time, bool, error) {
	r, ok := m.rows[id]
	if !ok {
		return time.Time{}, false, nil
	}
	return r.updatedAt, true, nil
}

func (m *memImportBackend) writeIssue(snap Snapshot) error {
	m.writes++
	labels := map[string]bool{}
	for _, l := range snap.Labels() {
		labels[l] = true
	}
	comments := map[string]bool{}
	for _, c := range snap.commentRecords() {
		comments[commentKey(c.Author, c.Text, c.CreatedAt)] = true
	}
	m.rows[snap.ID()] = &memRow{
		raw:       snap.RawJSON(),
		updatedAt: snap.UpdatedAt(),
		labels:    labels,
		comments:  comments,
		deps:      map[string]bool{},
	}
	return nil
}

func (m *memImportBackend) mergeLabels(id string, labels []string) error {
	r, ok := m.rows[id]
	if !ok {
		return fmt.Errorf("mergeLabels: row %s absent", id)
	}
	for _, l := range labels {
		r.labels[l] = true
	}
	return nil
}

func (m *memImportBackend) mergeComments(snap Snapshot) error {
	r, ok := m.rows[snap.ID()]
	if !ok {
		return fmt.Errorf("mergeComments: row %s absent", snap.ID())
	}
	for _, c := range snap.commentRecords() {
		r.comments[commentKey(c.Author, c.Text, c.CreatedAt)] = true
	}
	return nil
}

func (m *memImportBackend) addDep(dep depRecord) (string, error) {
	r, ok := m.rows[dep.IssueID]
	if !ok {
		return "", fmt.Errorf("addDep: issue %s absent", dep.IssueID)
	}
	// External targets are written without an existence check, mirroring bd.
	if !strings.HasPrefix(dep.DependsOnID, "external:") {
		if _, present := m.rows[dep.DependsOnID]; !present {
			return "target not found", nil
		}
	}
	r.deps[dep.DependsOnID] = true
	return "", nil
}

// exportLine builds a compact bd-export-shaped JSONL object for a bead.
func exportLine(t *testing.T, fields map[string]any) []byte {
	t.Helper()
	if _, ok := fields["_type"]; !ok {
		fields["_type"] = "issue"
	}
	raw, err := json.Marshal(fields)
	if err != nil {
		t.Fatalf("marshaling export line: %v", err)
	}
	return raw
}

func decodeOne(t *testing.T, line []byte) Snapshot {
	t.Helper()
	snap, keep, err := DecodeSnapshot(line)
	if err != nil {
		t.Fatalf("DecodeSnapshot: %v", err)
	}
	if !keep {
		t.Fatalf("DecodeSnapshot dropped an importable line: %s", line)
	}
	return snap
}

func ts(s string) time.Time {
	tm, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return tm
}

func TestSnapshotDecodeAccessorsAndByteFidelity(t *testing.T) {
	line := exportLine(t, map[string]any{
		"id":           "gc-1",
		"title":        "work bead",
		"status":       "closed",
		"issue_type":   "task",
		"priority":     0,
		"labels":       []string{"area:core", "gc:reviewed"},
		"metadata":     map[string]any{"gc.routed_to": "gascity/builder"},
		"created_at":   "2026-01-01T00:00:00Z",
		"updated_at":   "2026-02-02T03:04:05Z",
		"closed_at":    "2026-02-02T03:04:06Z",
		"close_reason": "done",
		"dependencies": []map[string]any{
			{"issue_id": "gc-1", "depends_on_id": "gc-2", "type": "blocks"},
		},
	})
	snap := decodeOne(t, line)

	if snap.ID() != "gc-1" || snap.Status() != "closed" || snap.IssueType() != "task" {
		t.Fatalf("accessors = (%q,%q,%q)", snap.ID(), snap.Status(), snap.IssueType())
	}
	if !snap.UpdatedAt().Equal(ts("2026-02-02T03:04:05Z")) {
		t.Fatalf("UpdatedAt = %v", snap.UpdatedAt())
	}
	if snap.ClosedAt() == nil || !snap.ClosedAt().Equal(ts("2026-02-02T03:04:06Z")) {
		t.Fatalf("ClosedAt = %v", snap.ClosedAt())
	}
	if got := snap.Metadata()["gc.routed_to"]; got != "gascity/builder" {
		t.Fatalf("Metadata[gc.routed_to] = %q", got)
	}
	if got := snap.Labels(); !reflect.DeepEqual(got, []string{"area:core", "gc:reviewed"}) {
		t.Fatalf("Labels = %v", got)
	}
	deps := snap.Deps()
	if len(deps) != 1 || deps[0].DependsOnID != "gc-2" || deps[0].Type != "blocks" {
		t.Fatalf("Deps = %v", deps)
	}

	if string(snap.RawJSON()) != string(line) {
		t.Fatalf("RawJSON not byte-lossless:\n got %s\nwant %s", snap.RawJSON(), line)
	}
	marshaled, err := json.Marshal(snap)
	if err != nil {
		t.Fatalf("Marshal snapshot: %v", err)
	}
	if string(marshaled) != string(line) {
		t.Fatalf("MarshalJSON not byte-lossless:\n got %s\nwant %s", marshaled, line)
	}
}

func TestSnapshotBeadIsClassifyCompatible(t *testing.T) {
	line := exportLine(t, map[string]any{
		"id":         "gc-7",
		"title":      "a bead",
		"status":     "open",
		"issue_type": "bug",
		"labels":     []string{"x"},
		"metadata":   map[string]any{"k": "v"},
		"updated_at": "2026-03-03T00:00:00Z",
	})
	snap := decodeOne(t, line)
	b := snap.Bead()
	if b.ID != "gc-7" || b.Type != "bug" || b.Status != "open" {
		t.Fatalf("Bead projection = %+v", b)
	}
	if b.Metadata["k"] != "v" || len(b.Labels) != 1 {
		t.Fatalf("Bead classify inputs wrong: %+v", b)
	}
}

func TestDecodeSnapshotSkipsNonBeadRecords(t *testing.T) {
	cases := []struct {
		name string
		line string
	}{
		{"blank", "   "},
		{"memory", `{"_type":"memory","key":"k","value":"v"}`},
		{"schema_header", `{"_schema":"beads-jsonl/1","_dolt_branch":"main"}`},
		{"tombstone", `{"_type":"issue","id":"gc-9","status":"tombstone"}`},
		{"template", `{"_type":"issue","id":"gc-tpl","is_template":true}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, keep, err := DecodeSnapshot([]byte(tc.line))
			if err != nil {
				t.Fatalf("DecodeSnapshot(%s): %v", tc.name, err)
			}
			if keep {
				t.Fatalf("DecodeSnapshot(%s) kept a record it must skip", tc.name)
			}
		})
	}
}

func TestDecodeSnapshotsSkipsMixedStream(t *testing.T) {
	stream := `{"_schema":"beads-jsonl/1"}
{"_type":"issue","id":"gc-1","title":"a","updated_at":"2026-01-01T00:00:00Z"}
{"_type":"memory","key":"m","value":"x"}
{"_type":"issue","id":"gc-2","status":"tombstone"}
{"_type":"issue","id":"gc-tpl","is_template":true}

{"_type":"issue","id":"gc-3","title":"c","updated_at":"2026-01-01T00:00:00Z"}
`
	snaps, err := DecodeSnapshots([]byte(stream))
	if err != nil {
		t.Fatalf("DecodeSnapshots: %v", err)
	}
	if len(snaps) != 2 || snaps[0].ID() != "gc-1" || snaps[1].ID() != "gc-3" {
		t.Fatalf("DecodeSnapshots kept %d rows: %v", len(snaps), snapIDs(snaps))
	}
}

func snapIDs(snaps []Snapshot) []string {
	out := make([]string, len(snaps))
	for i, s := range snaps {
		out[i] = s.ID()
	}
	return out
}

func TestImportOptionsValidateRejectsConflictSkipWithAllowStale(t *testing.T) {
	opts := ImportOptions{ConflictSkip: true, AllowStaleIDs: []string{"gc-1"}}
	backend := newMemImportBackend()
	if _, err := runGuardedUpsert(context.Background(), []Snapshot{snapForID(t, "gc-1")}, opts, backend); !errors.Is(err, ErrConflictSkipWithAllowStale) {
		t.Fatalf("runGuardedUpsert = %v, want ErrConflictSkipWithAllowStale", err)
	}
	if err := opts.validate(); !errors.Is(err, ErrConflictSkipWithAllowStale) {
		t.Fatalf("validate = %v, want ErrConflictSkipWithAllowStale", err)
	}
}

func TestGuardedUpsertInsertsAbsentRowsByteLossless(t *testing.T) {
	lines := [][]byte{
		exportLine(t, map[string]any{"id": "gc-1", "title": "open bead", "status": "open", "updated_at": "2026-01-01T00:00:00Z"}),
		exportLine(t, map[string]any{"id": "gc-2", "title": "closed bead", "status": "closed", "updated_at": "2026-01-02T00:00:00Z", "closed_at": "2026-01-02T00:00:01Z", "close_reason": "done"}),
		exportLine(t, map[string]any{"id": "gc-3", "title": "labeled", "status": "open", "labels": []string{"a", "b"}, "updated_at": "2026-01-03T00:00:00Z"}),
		exportLine(t, map[string]any{"id": "gc-4", "title": "with meta", "status": "open", "metadata": map[string]any{"gc.k": "v"}, "updated_at": "2026-01-04T00:00:00Z"}),
	}
	var snaps []Snapshot
	for _, l := range lines {
		snaps = append(snaps, decodeOne(t, l))
	}
	backend := newMemImportBackend()
	report, err := runGuardedUpsert(context.Background(), snaps, ImportOptions{}, backend)
	if err != nil {
		t.Fatalf("runGuardedUpsert: %v", err)
	}
	if len(report.Inserted) != 4 || len(report.Updated) != 0 || len(report.KeptLocal) != 0 {
		t.Fatalf("report = %+v", report)
	}
	for i, l := range lines {
		id := snaps[i].ID()
		if got := string(backend.rows[id].raw); got != string(l) {
			t.Fatalf("row %s not byte-lossless through import:\n got %s\nwant %s", id, got, l)
		}
	}
}

func TestGuardedUpsertReplacesOnNewerAndConvergesToClosed(t *testing.T) {
	backend := newMemImportBackend()
	openSnap := decodeOne(t, exportLine(t, map[string]any{"id": "gc-1", "title": "t", "status": "open", "updated_at": "2026-01-01T00:00:00Z"}))
	if _, err := runGuardedUpsert(context.Background(), []Snapshot{openSnap}, ImportOptions{}, backend); err != nil {
		t.Fatalf("seed import: %v", err)
	}
	writesAfterSeed := backend.writes

	closedSnap := decodeOne(t, exportLine(t, map[string]any{"id": "gc-1", "title": "t", "status": "closed", "updated_at": "2026-02-01T00:00:00Z", "closed_at": "2026-02-01T00:00:01Z"}))
	report, err := runGuardedUpsert(context.Background(), []Snapshot{closedSnap}, ImportOptions{}, backend)
	if err != nil {
		t.Fatalf("re-import: %v", err)
	}
	if len(report.Updated) != 1 || report.Updated[0] != "gc-1" {
		t.Fatalf("expected gc-1 updated, got %+v", report)
	}
	if backend.writes-writesAfterSeed != 1 {
		t.Fatalf("expected exactly one write for the replace, got %d", backend.writes-writesAfterSeed)
	}
	if got := decodeOne(t, backend.rows["gc-1"].raw).Status(); got != "closed" {
		t.Fatalf("converged status = %q, want closed", got)
	}
}

func TestGuardedUpsertTieKeepsLocalAndAllowStaleOverrides(t *testing.T) {
	backend := newMemImportBackend()
	local := decodeOne(t, exportLine(t, map[string]any{"id": "gc-1", "title": "local", "status": "open", "labels": []string{"local"}, "updated_at": "2026-01-01T00:00:05Z"}))
	if _, err := runGuardedUpsert(context.Background(), []Snapshot{local}, ImportOptions{}, backend); err != nil {
		t.Fatalf("seed: %v", err)
	}

	tie := decodeOne(t, exportLine(t, map[string]any{"id": "gc-1", "title": "incoming", "status": "open", "labels": []string{"incoming"}, "updated_at": "2026-01-01T00:00:05.900Z"}))
	report, err := runGuardedUpsert(context.Background(), []Snapshot{tie}, ImportOptions{}, backend)
	if err != nil {
		t.Fatalf("tie import: %v", err)
	}
	if len(report.KeptLocal) != 1 || report.KeptLocal[0] != "gc-1" {
		t.Fatalf("expected gc-1 kept local, got %+v", report)
	}
	if got := decodeOne(t, backend.rows["gc-1"].raw).RawJSON(); string(got) != string(local.RawJSON()) {
		t.Fatalf("tie must keep local scalars byte-untouched")
	}
	if !backend.rows["gc-1"].labels["incoming"] || !backend.rows["gc-1"].labels["local"] {
		t.Fatalf("tie must merge incoming label additively: %v", backend.rows["gc-1"].labels)
	}

	// AllowStaleIDs forces the same tie row to overwrite local.
	report, err = runGuardedUpsert(context.Background(), []Snapshot{tie}, ImportOptions{AllowStaleIDs: []string{"gc-1"}}, backend)
	if err != nil {
		t.Fatalf("allow-stale import: %v", err)
	}
	if len(report.Updated) != 1 {
		t.Fatalf("allow-stale expected update, got %+v", report)
	}
	if got := decodeOne(t, backend.rows["gc-1"].raw).RawJSON(); string(got) != string(tie.RawJSON()) {
		t.Fatalf("allow-stale must overwrite local with source")
	}
}

func TestGuardedUpsertAllowStaleIsConditionalOnNewerDestination(t *testing.T) {
	backend := newMemImportBackend()
	// Destination is NEWER than the forced source snapshot; the conditional
	// override must NOT clobber it — reports StaleSkipped instead.
	newerLocal := decodeOne(t, exportLine(t, map[string]any{"id": "gc-1", "title": "newer local", "status": "closed", "updated_at": "2026-05-01T00:00:00Z"}))
	if _, err := runGuardedUpsert(context.Background(), []Snapshot{newerLocal}, ImportOptions{}, backend); err != nil {
		t.Fatalf("seed: %v", err)
	}
	olderSource := decodeOne(t, exportLine(t, map[string]any{"id": "gc-1", "title": "older source", "status": "open", "updated_at": "2026-01-01T00:00:00Z"}))
	report, err := runGuardedUpsert(context.Background(), []Snapshot{olderSource}, ImportOptions{AllowStaleIDs: []string{"gc-1"}}, backend)
	if err != nil {
		t.Fatalf("forced import: %v", err)
	}
	if len(report.StaleSkipped) != 1 || report.StaleSkipped[0] != "gc-1" {
		t.Fatalf("expected gc-1 stale-skipped (dest newer), got %+v", report)
	}
	if got := decodeOne(t, backend.rows["gc-1"].raw).Status(); got != "closed" {
		t.Fatalf("forced override must not clobber a strictly-newer destination: status=%q", got)
	}
}

func TestGuardedUpsertStaleOlderIsSkippedAndReported(t *testing.T) {
	backend := newMemImportBackend()
	newer := decodeOne(t, exportLine(t, map[string]any{"id": "gc-1", "title": "newer", "status": "open", "updated_at": "2026-05-01T00:00:00Z"}))
	if _, err := runGuardedUpsert(context.Background(), []Snapshot{newer}, ImportOptions{}, backend); err != nil {
		t.Fatalf("seed: %v", err)
	}
	older := decodeOne(t, exportLine(t, map[string]any{"id": "gc-1", "title": "older", "status": "open", "updated_at": "2026-01-01T00:00:00Z"}))
	report, err := runGuardedUpsert(context.Background(), []Snapshot{older}, ImportOptions{}, backend)
	if err != nil {
		t.Fatalf("stale import: %v", err)
	}
	if len(report.StaleSkipped) != 1 || report.StaleSkipped[0] != "gc-1" {
		t.Fatalf("expected gc-1 stale-skipped, got %+v", report)
	}
	if got := decodeOne(t, backend.rows["gc-1"].raw).RawJSON(); string(got) != string(newer.RawJSON()) {
		t.Fatalf("stale skip must keep local")
	}
}

func TestGuardedUpsertConflictSkipLeavesExistingUntouched(t *testing.T) {
	backend := newMemImportBackend()
	existing := decodeOne(t, exportLine(t, map[string]any{
		"id": "gc-1", "title": "existing", "status": "open", "labels": []string{"keep"},
		"updated_at":   "2026-01-01T00:00:00Z",
		"dependencies": []map[string]any{{"issue_id": "gc-1", "depends_on_id": "gc-9", "type": "blocks"}},
	}))
	target := decodeOne(t, exportLine(t, map[string]any{"id": "gc-9", "title": "target", "status": "open", "updated_at": "2026-01-01T00:00:00Z"}))
	if _, err := runGuardedUpsert(context.Background(), []Snapshot{target, existing}, ImportOptions{}, backend); err != nil {
		t.Fatalf("seed: %v", err)
	}
	before := *backend.rows["gc-1"]

	incoming := decodeOne(t, exportLine(t, map[string]any{
		"id": "gc-1", "title": "incoming", "status": "closed", "labels": []string{"new"},
		"updated_at":   "2026-09-09T00:00:00Z",
		"dependencies": []map[string]any{{"issue_id": "gc-1", "depends_on_id": "gc-8", "type": "blocks"}},
	}))
	report, err := runGuardedUpsert(context.Background(), []Snapshot{incoming}, ImportOptions{ConflictSkip: true}, backend)
	if err != nil {
		t.Fatalf("conflict-skip import: %v", err)
	}
	if len(report.ConflictSkipped) != 1 || report.ConflictSkipped[0] != "gc-1" {
		t.Fatalf("expected gc-1 conflict-skipped, got %+v", report)
	}
	after := backend.rows["gc-1"]
	if string(after.raw) != string(before.raw) {
		t.Fatalf("conflict-skip changed scalars")
	}
	if !reflect.DeepEqual(after.labels, before.labels) {
		t.Fatalf("conflict-skip merged labels: before %v after %v", before.labels, after.labels)
	}
	if !reflect.DeepEqual(after.deps, before.deps) {
		t.Fatalf("conflict-skip merged deps: before %v after %v", before.deps, after.deps)
	}
}

func TestGuardedUpsertDanglingDepReportedNotDropped(t *testing.T) {
	backend := newMemImportBackend()
	snap := decodeOne(t, exportLine(t, map[string]any{
		"id": "gc-1", "title": "has dangling dep", "status": "open", "updated_at": "2026-01-01T00:00:00Z",
		"dependencies": []map[string]any{
			{"issue_id": "gc-1", "depends_on_id": "gc-absent", "type": "blocks"},
		},
	}))
	report, err := runGuardedUpsert(context.Background(), []Snapshot{snap}, ImportOptions{}, backend)
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if len(report.SkippedDependencies) != 1 {
		t.Fatalf("expected 1 skipped dependency, got %+v", report.SkippedDependencies)
	}
	got := report.SkippedDependencies[0]
	if got.IssueID != "gc-1" || got.DependsOnID != "gc-absent" {
		t.Fatalf("skipped dep pair = %+v", got)
	}
	if len(backend.rows["gc-1"].deps) != 0 {
		t.Fatalf("dangling dep must not be applied")
	}
}

func TestGuardedUpsertExternalDepWrittenWithoutExistenceCheck(t *testing.T) {
	backend := newMemImportBackend()
	snap := decodeOne(t, exportLine(t, map[string]any{
		"id": "gc-1", "title": "external dep", "status": "open", "updated_at": "2026-01-01T00:00:00Z",
		"dependencies": []map[string]any{
			{"issue_id": "gc-1", "depends_on_id": "external:https://gh/pr/9", "type": "blocks"},
		},
	}))
	report, err := runGuardedUpsert(context.Background(), []Snapshot{snap}, ImportOptions{}, backend)
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if len(report.SkippedDependencies) != 0 {
		t.Fatalf("external dep must be written, not skipped: %+v", report.SkippedDependencies)
	}
	if !backend.rows["gc-1"].deps["external:https://gh/pr/9"] {
		t.Fatalf("external dep edge not applied")
	}
}

func TestGuardedUpsertAppliesDepAfterAllWrites(t *testing.T) {
	backend := newMemImportBackend()
	blocked := decodeOne(t, exportLine(t, map[string]any{
		"id": "gc-1", "title": "blocked", "status": "open", "updated_at": "2026-01-01T00:00:00Z",
		"dependencies": []map[string]any{{"issue_id": "gc-1", "depends_on_id": "gc-2", "type": "blocks"}},
	}))
	blocker := decodeOne(t, exportLine(t, map[string]any{"id": "gc-2", "title": "blocker", "status": "open", "updated_at": "2026-01-01T00:00:00Z"}))
	report, err := runGuardedUpsert(context.Background(), []Snapshot{blocked, blocker}, ImportOptions{}, backend)
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if len(report.SkippedDependencies) != 0 {
		t.Fatalf("intra-batch dep must not be skipped: %+v", report.SkippedDependencies)
	}
	if !backend.rows["gc-1"].deps["gc-2"] {
		t.Fatalf("intra-batch dep gc-1 -> gc-2 not applied")
	}
}

func TestGuardedUpsertTieMergesComments(t *testing.T) {
	backend := newMemImportBackend()
	local := decodeOne(t, exportLine(t, map[string]any{
		"id": "gc-1", "title": "t", "status": "open", "updated_at": "2026-01-01T00:00:05Z",
		"comments": []map[string]any{{"author": "a", "text": "first", "created_at": "2026-01-01T00:00:05Z"}},
	}))
	if _, err := runGuardedUpsert(context.Background(), []Snapshot{local}, ImportOptions{}, backend); err != nil {
		t.Fatalf("seed: %v", err)
	}
	tie := decodeOne(t, exportLine(t, map[string]any{
		"id": "gc-1", "title": "t2", "status": "open", "updated_at": "2026-01-01T00:00:05Z",
		"comments": []map[string]any{{"author": "b", "text": "second", "created_at": "2026-01-01T00:00:05Z"}},
	}))
	report, err := runGuardedUpsert(context.Background(), []Snapshot{tie}, ImportOptions{}, backend)
	if err != nil {
		t.Fatalf("tie import: %v", err)
	}
	if len(report.KeptLocal) != 1 {
		t.Fatalf("expected tie kept-local, got %+v", report)
	}
	comments := backend.rows["gc-1"].comments
	if !comments[commentKey("a", "first", ts("2026-01-01T00:00:05Z"))] ||
		!comments[commentKey("b", "second", ts("2026-01-01T00:00:05Z"))] {
		t.Fatalf("tie must merge both comments additively: %v", comments)
	}
}

func TestStampMetadataInjectsWithoutDisturbingOtherFields(t *testing.T) {
	line := exportLine(t, map[string]any{
		"id":         "gc-1",
		"title":      "stamp target",
		"status":     "open",
		"labels":     []string{"a"},
		"metadata":   map[string]any{"existing": "kept"},
		"updated_at": "2026-01-01T00:00:00Z",
	})
	snap := decodeOne(t, line)
	stamped, err := snap.StampMetadata("gc.topology_source", "city-alpha")
	if err != nil {
		t.Fatalf("StampMetadata: %v", err)
	}
	if got := stamped.Metadata()["gc.topology_source"]; got != "city-alpha" {
		t.Fatalf("stamped metadata missing: %v", stamped.Metadata())
	}
	if got := stamped.Metadata()["existing"]; got != "kept" {
		t.Fatalf("stamp clobbered existing metadata: %v", stamped.Metadata())
	}
	assertOnlyFieldChanged(t, snap.RawJSON(), stamped.RawJSON(), "metadata")
}

func TestStampLabelAppendsWithoutDisturbingOtherFields(t *testing.T) {
	line := exportLine(t, map[string]any{
		"id":         "gc-1",
		"title":      "stamp target",
		"status":     "open",
		"labels":     []string{"a"},
		"metadata":   map[string]any{"k": "v"},
		"updated_at": "2026-01-01T00:00:00Z",
	})
	snap := decodeOne(t, line)
	stamped, err := snap.StampLabel("gc.topology_migrating")
	if err != nil {
		t.Fatalf("StampLabel: %v", err)
	}
	labels := stamped.Labels()
	sort.Strings(labels)
	if !reflect.DeepEqual(labels, []string{"a", "gc.topology_migrating"}) {
		t.Fatalf("StampLabel labels = %v", labels)
	}
	assertOnlyFieldChanged(t, snap.RawJSON(), stamped.RawJSON(), "labels")

	// Idempotent: stamping an existing label is a no-op.
	again, err := stamped.StampLabel("gc.topology_migrating")
	if err != nil {
		t.Fatalf("StampLabel again: %v", err)
	}
	if len(again.Labels()) != 2 {
		t.Fatalf("duplicate label appended: %v", again.Labels())
	}
}

// assertOnlyFieldChanged asserts that before and after differ only in the named
// field (order-insensitive over the rest).
func assertOnlyFieldChanged(t *testing.T, before, after []byte, field string) {
	t.Helper()
	var b, a map[string]json.RawMessage
	if err := json.Unmarshal(before, &b); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(after, &a); err != nil {
		t.Fatal(err)
	}
	delete(b, field)
	delete(a, field)
	if !reflect.DeepEqual(b, a) {
		t.Fatalf("stamp disturbed fields other than %q:\nbefore %v\nafter  %v", field, b, a)
	}
}

func TestDiffSnapshotLinksReportsMissingEdgesAndLabels(t *testing.T) {
	source := decodeOne(t, exportLine(t, map[string]any{
		"id": "gc-1", "title": "src", "status": "open", "labels": []string{"a", "b", "c"},
		"updated_at": "2026-01-01T00:00:00Z",
		"dependencies": []map[string]any{
			{"issue_id": "gc-1", "depends_on_id": "gc-2", "type": "blocks"},
			{"issue_id": "gc-1", "depends_on_id": "gc-3", "type": "blocks"},
		},
	}))
	dest := Bead{
		ID:     "gc-1",
		Labels: []string{"a"},
		Dependencies: []Dep{
			{IssueID: "gc-1", DependsOnID: "gc-2", Type: "blocks"},
		},
	}
	diff := DiffSnapshotLinks(source, dest)
	if diff.Empty() {
		t.Fatalf("expected a non-empty diff")
	}
	if len(diff.MissingDeps) != 1 || diff.MissingDeps[0].DependsOnID != "gc-3" {
		t.Fatalf("MissingDeps = %+v", diff.MissingDeps)
	}
	want := []string{"b", "c"}
	sort.Strings(diff.MissingLabels)
	if !reflect.DeepEqual(diff.MissingLabels, want) {
		t.Fatalf("MissingLabels = %v, want %v", diff.MissingLabels, want)
	}

	full := Bead{
		ID:     "gc-1",
		Labels: []string{"a", "b", "c"},
		Dependencies: []Dep{
			{IssueID: "gc-1", DependsOnID: "gc-2", Type: "blocks"},
			{IssueID: "gc-1", DependsOnID: "gc-3", Type: "blocks"},
		},
	}
	if d := DiffSnapshotLinks(source, full); !d.Empty() {
		t.Fatalf("expected empty diff for drained row, got %+v", d)
	}
}
