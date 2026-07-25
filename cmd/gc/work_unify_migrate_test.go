package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/beadmeta"
	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/beads/contract"
	messagingdb "github.com/gastownhall/gascity/internal/classdb/messaging"
	nudgesdb "github.com/gastownhall/gascity/internal/classdb/nudges"
	sessionsdb "github.com/gastownhall/gascity/internal/classdb/sessions"
	"github.com/gastownhall/gascity/internal/config"
	"github.com/gastownhall/gascity/internal/fsys"
)

// writeAllClassMigratedMarkers creates the five class migrated-marker files so a
// unified-city unify passes gate B.1.
func writeAllClassMigratedMarkers(t *testing.T, city string) {
	t.Helper()
	paths := []string{
		ordersMigratedMarkerPath(city),
		nudgesdb.MigratedMarkerPath(city),
		messagingdb.MigratedMarkerPath(city),
		sessionsdb.MigratedMarkerPath(city),
		graphMigratedMarkerPath(city),
	}
	for _, p := range paths {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("migrated\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// ── in-memory snapshot-capable fake store ────────────────────────────────────

type fakeWorkRec struct {
	id        string
	status    string
	issueType string
	labels    []string
	metadata  map[string]string
	createdAt time.Time
	updatedAt time.Time
	closedAt  *time.Time
	deps      []beads.Dep
}

// fakeWorkStore is a minimal in-memory beads.Store that also implements the
// snapshot Export/Import/Fetch capabilities, so the unify orchestration can be
// exercised without a real bd/Dolt backend. Only the store methods the migration
// calls are implemented; the rest are inherited from a nil beads.Store and must
// not be reached in a passing test.
type fakeWorkStore struct {
	beads.Store
	mu   sync.Mutex
	recs map[string]*fakeWorkRec
}

func newFakeWorkStore() *fakeWorkStore { return &fakeWorkStore{recs: map[string]*fakeWorkRec{}} }

func (f *fakeWorkStore) seed(rec *fakeWorkRec) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if rec.metadata == nil {
		rec.metadata = map[string]string{}
	}
	f.recs[rec.id] = rec
}

func (f *fakeWorkStore) Get(id string) (beads.Bead, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	rec, ok := f.recs[id]
	if !ok {
		return beads.Bead{}, beads.ErrNotFound
	}
	return recToBead(rec), nil
}

func (f *fakeWorkStore) List(q beads.ListQuery) ([]beads.Bead, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	ids := make([]string, 0, len(f.recs))
	for id := range f.recs {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	out := make([]beads.Bead, 0, len(ids))
	for _, id := range ids {
		rec := f.recs[id]
		if !q.IncludeClosed && rec.status == "closed" {
			continue
		}
		if q.Label != "" && !recHasLabel(rec, q.Label) {
			continue
		}
		out = append(out, recToBead(rec))
	}
	return out, nil
}

func recHasLabel(rec *fakeWorkRec, label string) bool {
	for _, l := range rec.labels {
		if l == label {
			return true
		}
	}
	return false
}

func (f *fakeWorkStore) Update(id string, opts beads.UpdateOpts) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	rec, ok := f.recs[id]
	if !ok {
		return beads.ErrNotFound
	}
	if len(opts.RemoveLabels) > 0 {
		remove := map[string]bool{}
		for _, l := range opts.RemoveLabels {
			remove[l] = true
		}
		var kept []string
		for _, l := range rec.labels {
			if !remove[l] {
				kept = append(kept, l)
			}
		}
		rec.labels = kept
	}
	for _, l := range opts.Labels {
		if !recHasLabel(rec, l) {
			rec.labels = append(rec.labels, l)
		}
	}
	return nil
}

func (f *fakeWorkStore) DepAdd(issueID, dependsOnID, depType string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	rec, ok := f.recs[issueID]
	if !ok {
		return beads.ErrNotFound
	}
	for _, d := range rec.deps {
		if d.DependsOnID == dependsOnID {
			return nil
		}
	}
	rec.deps = append(rec.deps, beads.Dep{IssueID: issueID, DependsOnID: dependsOnID, Type: depType})
	return nil
}

func (f *fakeWorkStore) DepList(id, _ string) ([]beads.Dep, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	rec, ok := f.recs[id]
	if !ok {
		return nil, beads.ErrNotFound
	}
	return append([]beads.Dep(nil), rec.deps...), nil
}

func (f *fakeWorkStore) SetMetadata(id, key, value string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	rec, ok := f.recs[id]
	if !ok {
		return beads.ErrNotFound
	}
	if rec.metadata == nil {
		rec.metadata = map[string]string{}
	}
	rec.metadata[key] = value
	return nil
}

func (f *fakeWorkStore) ExportBeadSnapshots(_ context.Context, _ beads.ExportOptions) ([]beads.Snapshot, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	ids := make([]string, 0, len(f.recs))
	for id := range f.recs {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	out := make([]beads.Snapshot, 0, len(ids))
	for _, id := range ids {
		snap, err := recToSnapshot(f.recs[id])
		if err != nil {
			return nil, err
		}
		out = append(out, snap)
	}
	return out, nil
}

func (f *fakeWorkStore) GetBeadSnapshots(_ context.Context, ids []string) ([]beads.Snapshot, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []beads.Snapshot
	for _, id := range ids {
		if rec, ok := f.recs[id]; ok {
			snap, err := recToSnapshot(rec)
			if err != nil {
				return nil, err
			}
			out = append(out, snap)
		}
	}
	return out, nil
}

func (f *fakeWorkStore) ImportBeadSnapshots(_ context.Context, snaps []beads.Snapshot, opts beads.ImportOptions) (beads.ImportReport, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	allowStale := map[string]bool{}
	for _, id := range opts.AllowStaleIDs {
		allowStale[id] = true
	}
	var report beads.ImportReport
	writes := map[string]bool{}
	for _, snap := range snaps {
		id := snap.ID()
		incoming := snapToRec(snap)
		existing, present := f.recs[id]
		switch {
		case !present:
			f.recs[id] = incoming
			report.Inserted = append(report.Inserted, id)
			writes[id] = true
		case opts.ConflictSkip:
			report.ConflictSkipped = append(report.ConflictSkipped, id)
		default:
			in := incoming.updatedAt.UTC().Truncate(time.Second)
			cur := existing.updatedAt.UTC().Truncate(time.Second)
			switch {
			case allowStale[id]:
				if in.Before(cur) {
					report.StaleSkipped = append(report.StaleSkipped, id)
				} else {
					f.recs[id] = incoming
					report.Updated = append(report.Updated, id)
					writes[id] = true
				}
			case in.After(cur):
				f.recs[id] = incoming
				report.Updated = append(report.Updated, id)
				writes[id] = true
			case in.Equal(cur):
				report.KeptLocal = append(report.KeptLocal, id)
			default:
				report.StaleSkipped = append(report.StaleSkipped, id)
			}
		}
	}
	// Dangling dep edges on written rows are reported, never applied.
	for _, snap := range snaps {
		if !writes[snap.ID()] {
			continue
		}
		for _, d := range snap.Deps() {
			if _, ok := f.recs[d.DependsOnID]; !ok {
				report.SkippedDependencies = append(report.SkippedDependencies, beads.DepPair{IssueID: d.IssueID, DependsOnID: d.DependsOnID})
			}
		}
	}
	return report, nil
}

func recToBead(rec *fakeWorkRec) beads.Bead {
	meta := map[string]string{}
	for k, v := range rec.metadata {
		meta[k] = v
	}
	return beads.Bead{
		ID:           rec.id,
		Status:       rec.status,
		Type:         rec.issueType,
		Labels:       append([]string(nil), rec.labels...),
		Metadata:     meta,
		CreatedAt:    rec.createdAt,
		UpdatedAt:    rec.updatedAt,
		Dependencies: append([]beads.Dep(nil), rec.deps...),
	}
}

func recToSnapshot(rec *fakeWorkRec) (beads.Snapshot, error) {
	obj := map[string]any{
		"_type":      "issue",
		"id":         rec.id,
		"status":     rec.status,
		"issue_type": rec.issueType,
		"labels":     rec.labels,
		"metadata":   rec.metadata,
		"created_at": rec.createdAt.Format(time.RFC3339Nano),
		"updated_at": rec.updatedAt.Format(time.RFC3339Nano),
	}
	if rec.closedAt != nil {
		obj["closed_at"] = rec.closedAt.Format(time.RFC3339Nano)
	}
	if len(rec.deps) > 0 {
		deps := make([]map[string]string, 0, len(rec.deps))
		for _, d := range rec.deps {
			deps = append(deps, map[string]string{"issue_id": d.IssueID, "depends_on_id": d.DependsOnID, "type": d.Type})
		}
		obj["dependencies"] = deps
	}
	line, err := json.Marshal(obj)
	if err != nil {
		return beads.Snapshot{}, err
	}
	snap, keep, err := beads.DecodeSnapshot(line)
	if err != nil {
		return beads.Snapshot{}, err
	}
	if !keep {
		return beads.Snapshot{}, fmt.Errorf("snapshot for %s unexpectedly skipped", rec.id)
	}
	return snap, nil
}

func snapToRec(snap beads.Snapshot) *fakeWorkRec {
	b := snap.Bead()
	rec := &fakeWorkRec{
		id:        snap.ID(),
		status:    snap.Status(),
		issueType: snap.IssueType(),
		labels:    snap.Labels(),
		metadata:  snap.Metadata(),
		createdAt: snap.CreatedAt(),
		updatedAt: snap.UpdatedAt(),
		closedAt:  snap.ClosedAt(),
		deps:      b.Dependencies,
	}
	if rec.metadata == nil {
		rec.metadata = map[string]string{}
	}
	return rec
}

// ── pure-helper tests ────────────────────────────────────────────────────────

func TestGatePrefixDistinct(t *testing.T) {
	ok := &config.City{Workspace: config.Workspace{Prefix: "hq"}, Rigs: []config.Rig{{Name: "alpha", Prefix: "al"}, {Name: "beta", Prefix: "be"}}}
	if err := gatePrefixDistinct(ok); err != nil {
		t.Fatalf("distinct prefixes should pass: %v", err)
	}
	clash := &config.City{Workspace: config.Workspace{Prefix: "hq"}, Rigs: []config.Rig{{Name: "alpha", Prefix: "HQ"}}}
	if err := gatePrefixDistinct(clash); err == nil {
		t.Fatalf("case-insensitive prefix clash must be rejected")
	}
}

func TestSanitizeResidueDirIsSafe(t *testing.T) {
	got := sanitizeResidueDir(workResidueSource{Host: "127.0.0.1", Port: "3306", Database: "../evil/db"})
	if strings.ContainsAny(got, "/\\") {
		t.Fatalf("residue dir must not contain path separators: %q", got)
	}
	// Distinct identities produce distinct dirs.
	a := sanitizeResidueDir(workResidueSource{Database: "rigdb", Port: "1"})
	b := sanitizeResidueDir(workResidueSource{Database: "rigdb", Port: "2"})
	if a == b {
		t.Fatalf("distinct ports should yield distinct residue dirs")
	}
}

func TestWorkTopologyCityIdentityStampStable(t *testing.T) {
	a := workTopologyCityIdentityStamp("/data/city")
	b := workTopologyCityIdentityStamp("/data/city/")
	if a != b {
		t.Fatalf("stamp must be path-clean stable: %q vs %q", a, b)
	}
	if a == workTopologyCityIdentityStamp("/data/other") {
		t.Fatalf("distinct cities must get distinct stamps")
	}
	if !strings.HasPrefix(a, "gc-city:") {
		t.Fatalf("stamp should carry the gc-city: prefix, got %q", a)
	}
}

func TestFilterCreatedAtConflicts(t *testing.T) {
	created := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	dest := newFakeWorkStore()
	dest.seed(&fakeWorkRec{id: "al-1", status: "open", createdAt: created, updatedAt: created})
	// Same id in the source with a DIFFERENT created_at is a conflict.
	conflictSnap, _ := recToSnapshot(&fakeWorkRec{id: "al-1", status: "open", createdAt: created.Add(48 * time.Hour), updatedAt: created.Add(48 * time.Hour)})
	// A fresh id is kept.
	freshSnap, _ := recToSnapshot(&fakeWorkRec{id: "al-2", status: "open", createdAt: created, updatedAt: created})
	kept, conflicts, err := filterCreatedAtConflicts(context.Background(), dest, []beads.Snapshot{conflictSnap, freshSnap})
	if err != nil {
		t.Fatal(err)
	}
	if len(conflicts) != 1 || conflicts[0] != "al-1" {
		t.Fatalf("expected al-1 reported as a created_at conflict, got %v", conflicts)
	}
	if len(kept) != 1 || kept[0].ID() != "al-2" {
		t.Fatalf("expected only al-2 kept, got %v", kept)
	}
}

// ── ensureWorkUnified dark paths ─────────────────────────────────────────────

func TestEnsureWorkUnifiedDarkOnScopedCity(t *testing.T) {
	city := t.TempDir()
	cfg := &config.City{} // scoped default
	if err := ensureWorkUnified(city, cfg, &strings.Builder{}); err != nil {
		t.Fatalf("scoped city must be dark, got %v", err)
	}
}

// TestEnsureWorkUnifiedMarkerPresentClearsQuarantine pins F1/F3/F4: a
// marker-present boot does not re-run the migration, but it DOES convergently
// sweep any lingering gc.topology_migrating label off the city store (the
// post-marker crash window), then returns nil.
func TestEnsureWorkUnifiedMarkerPresentClearsQuarantine(t *testing.T) {
	city := t.TempDir()
	writeUnifiedMarker(t, city) // shared test helper writes the marker
	cityStore := newFakeWorkStore()
	cityStore.seed(&fakeWorkRec{id: "al-1", status: "open", labels: []string{workTopologyMigratingLabel}})

	origOpen := openWorkUnifyScopeStore
	t.Cleanup(func() { openWorkUnifyScopeStore = origOpen })
	openWorkUnifyScopeStore = func(string, string) (beads.Store, func(), error) { return cityStore, func() {}, nil }

	if err := ensureWorkUnified(city, unifiedCityConfig(), &strings.Builder{}); err != nil {
		t.Fatalf("marker-present unified city must no-op (post-sweep), got %v", err)
	}
	b1, _ := cityStore.Get("al-1")
	if beadIsTopologyQuarantined(b1) {
		t.Fatalf("marker-present boot must convergently clear the quarantine label, labels=%v", b1.Labels)
	}
}

// TestEnsureWorkUnifiedNativeProviderRefusedEarly pins F11: a non-bd work store
// is refused at the provider gate, before any copy or marker.
func TestEnsureWorkUnifiedNativeProviderRefusedEarly(t *testing.T) {
	city := t.TempDir()
	writeAllClassMigratedMarkers(t, city)
	cityStore := newFakeWorkStore()
	rigStore := newFakeWorkStore()
	withWorkUnifySeams(t, cityStore, rigStore)
	// Restore the REAL provider gate for this test so the fake (non-*BdStore) is
	// rejected.
	workUnifyRequireBdProvider = func(s beads.Store) error {
		if _, ok := s.(*beads.BdStore); ok {
			return nil
		}
		return errNativeProvider
	}
	err := ensureWorkUnified(city, unifiedCityConfig(), &strings.Builder{})
	if err == nil || !strings.Contains(err.Error(), "bd work-store provider") {
		t.Fatalf("expected native-provider refusal, got %v", err)
	}
	if _, ok, _ := readWorkTopologyMarker(workUnifiedMarkerPath(city)); ok {
		t.Fatalf("provider refusal must not write the marker")
	}
}

var errNativeProvider = fmt.Errorf("work unify blocked: scope=\"unified\" currently requires the bd work-store provider; keep scope=\"scoped\" or switch the work provider")

// TestEnsureWorkUnifiedCounterModeRefused pins F5/F10/F15: a counter-mode work
// database is refused with a message naming the missing counter-advance guard.
func TestEnsureWorkUnifiedCounterModeRefused(t *testing.T) {
	city := t.TempDir()
	writeAllClassMigratedMarkers(t, city)
	cityStore := newFakeWorkStore()
	rigStore := newFakeWorkStore()
	withWorkUnifySeams(t, cityStore, rigStore)
	workUnifyIsCounterModeWorkDB = func(beads.Store) (bool, error) { return true, nil }
	err := ensureWorkUnified(city, unifiedCityConfig(), &strings.Builder{})
	if err == nil || !strings.Contains(err.Error(), "counter-mode") {
		t.Fatalf("expected counter-mode refusal, got %v", err)
	}
}

// ── ensureWorkUnified happy path (fully seamed) ──────────────────────────────

func unifiedCityConfig() *config.City {
	return &config.City{
		Workspace: config.Workspace{Prefix: "hq"},
		Beads:     config.BeadsConfig{Work: config.BeadsWorkConfig{Scope: config.BeadsWorkScopeUnified}},
		Rigs:      []config.Rig{{Name: "alpha", Prefix: "al", Path: "rigs/alpha"}},
	}
}

// withWorkUnifySeams overrides the exec-backed seams with fakes for the duration
// of a test, restoring them after. The rig scope resolves to database "al"; the
// city to "hq", so the trigger fires.
func withWorkUnifySeams(t *testing.T, city *fakeWorkStore, rig *fakeWorkStore) {
	t.Helper()
	origOpen := openWorkUnifyScopeStore
	origProvider := workUnifyRequireBdProvider
	origProbe := workUnifyProbeCapability
	origCounter := workUnifyIsCounterModeWorkDB
	origAddPrefix := workUnifyConfigAddPrefixes
	origClassResidue := workUnifyImportRigClassResidue
	origRepoint := workUnifyRepointScopes
	origStraggler := openWorkUnifyStragglerStore
	origIdentity := workUnifyResolveIdentity
	t.Cleanup(func() {
		openWorkUnifyScopeStore = origOpen
		workUnifyRequireBdProvider = origProvider
		workUnifyProbeCapability = origProbe
		workUnifyIsCounterModeWorkDB = origCounter
		workUnifyConfigAddPrefixes = origAddPrefix
		workUnifyImportRigClassResidue = origClassResidue
		workUnifyRepointScopes = origRepoint
		openWorkUnifyStragglerStore = origStraggler
		workUnifyResolveIdentity = origIdentity
	})

	openWorkUnifyScopeStore = func(_, scopeRoot string) (beads.Store, func(), error) {
		if strings.Contains(scopeRoot, "alpha") {
			return rig, func() {}, nil
		}
		return city, func() {}, nil
	}
	workUnifyRequireBdProvider = func(beads.Store) error { return nil }
	workUnifyProbeCapability = func(beads.Store) (bool, error) { return true, nil }
	workUnifyIsCounterModeWorkDB = func(beads.Store) (bool, error) { return false, nil }
	workUnifyConfigAddPrefixes = func(beads.Store, string, []string) error { return nil }
	workUnifyImportRigClassResidue = func(string, *config.City, beads.Store, io.Writer) error { return nil }
	workUnifyRepointScopes = func(string, *config.City, io.Writer) error { return nil }
	openWorkUnifyStragglerStore = func(string, workResidueSource) (beads.Store, func(), error) {
		return rig, func() {}, nil
	}
	workUnifyResolveIdentity = func(_, scopeRoot string) (workUnifyScope, error) {
		if strings.Contains(scopeRoot, "alpha") {
			return workUnifyScope{root: scopeRoot, database: "al"}, nil
		}
		return workUnifyScope{root: scopeRoot, database: "hq"}, nil
	}
}

func TestEnsureWorkUnifiedHappyPath(t *testing.T) {
	// The city store starts empty; the rig store holds an open and a closed
	// work bead. After unify, both must appear in the city store, the marker
	// must record the rig's residue source, and copied rows must be quarantine-
	// cleared (label removed) after re-point.
	city := t.TempDir()
	writeAllClassMigratedMarkers(t, city)
	cityStore := newFakeWorkStore()
	rigStore := newFakeWorkStore()
	created := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	closed := created.Add(time.Hour)
	rigStore.seed(&fakeWorkRec{id: "al-1", status: "open", issueType: "task", createdAt: created, updatedAt: created})
	rigStore.seed(&fakeWorkRec{id: "al-2", status: "closed", issueType: "task", createdAt: created, updatedAt: closed, closedAt: &closed})

	withWorkUnifySeams(t, cityStore, rigStore)

	var out strings.Builder
	if err := ensureWorkUnified(city, unifiedCityConfig(), &out); err != nil {
		t.Fatalf("unify: %v\n%s", err, out.String())
	}

	// Both work beads copied.
	if _, err := cityStore.Get("al-1"); err != nil {
		t.Fatalf("al-1 not copied: %v", err)
	}
	b2, err := cityStore.Get("al-2")
	if err != nil {
		t.Fatalf("al-2 not copied: %v", err)
	}
	if b2.Status != "closed" {
		t.Fatalf("closed bead must cross as closed, got %q", b2.Status)
	}
	// Quarantine label cleared after re-point.
	b1, _ := cityStore.Get("al-1")
	if beadIsTopologyQuarantined(b1) {
		t.Fatalf("al-1 must be quarantine-cleared after unify, labels=%v", b1.Labels)
	}
	// Topology source stamped.
	if b1.Metadata[workTopologySourceMetadataKey] == "" {
		t.Fatalf("al-1 missing topology source stamp: %v", b1.Metadata)
	}

	// Marker written with the rig residue source (undrained).
	m, ok, err := readWorkTopologyMarker(workUnifiedMarkerPath(city))
	if err != nil || !ok {
		t.Fatalf("marker not written: ok=%v err=%v", ok, err)
	}
	if m.Kind != workMarkerKindUnified {
		t.Fatalf("unexpected marker kind %q", m.Kind)
	}
	if m.undrainedResidueCount() != 1 {
		t.Fatalf("expected 1 undrained residue source, got %d (%+v)", m.undrainedResidueCount(), m.ResidueSources)
	}
	if m.Counts.Imported != 2 {
		t.Fatalf("expected 2 imported recorded, got %d", m.Counts.Imported)
	}
}

// TestConvertSkippedGraphAttachEdges pins the landmine-#4 conversion: a skipped
// edge whose far endpoint is an OPEN graph-class bead becomes the
// gc.attached_workflow_root metadata linkage on the work bead; an edge to a
// non-class / closed endpoint is only logged.
func TestConvertSkippedGraphAttachEdges(t *testing.T) {
	city := t.TempDir()
	graph, err := graphClassStoreFor(city)
	if err != nil {
		t.Fatal(err)
	}
	root, err := graph.Create(beads.Bead{Title: "workflow root", Type: "task"})
	if err != nil {
		t.Fatal(err)
	}
	if !config.IsReservedClassBeadID(root.ID) {
		t.Fatalf("graph class store should mint a reserved-class id, got %q", root.ID)
	}

	cityStore := newFakeWorkStore()
	cityStore.seed(&fakeWorkRec{id: "al-1", status: "open"})
	cityStore.seed(&fakeWorkRec{id: "al-2", status: "open"})

	skipped := []beads.DepPair{
		{IssueID: "al-1", DependsOnID: root.ID}, // open graph root -> attach linkage
		{IssueID: "al-2", DependsOnID: "al-3"},  // non-class endpoint -> logged only
	}
	if err := convertSkippedGraphAttachEdges(city, cityStore, skipped, &strings.Builder{}); err != nil {
		t.Fatal(err)
	}
	b1, _ := cityStore.Get("al-1")
	if b1.Metadata[beadmeta.AttachedWorkflowRootMetadataKey] != root.ID {
		t.Fatalf("al-1 should carry attached_workflow_root=%s, got %v", root.ID, b1.Metadata)
	}
	b2, _ := cityStore.Get("al-2")
	if _, ok := b2.Metadata[beadmeta.AttachedWorkflowRootMetadataKey]; ok {
		t.Fatalf("al-2 must not gain an attach linkage for a non-class endpoint")
	}
}

// TestVerifyCopiedRowsRoutingProof pins F7: a verify read that returns an
// UN-stamped row (as a bd prefix-routing leak to the source database would) must
// fail — the destination row must carry this city's topology-source stamp.
func TestVerifyCopiedRowsRoutingProof(t *testing.T) {
	source := "gc-city:deadbeef"
	// The source-side (stamped) snapshot the copy would import.
	stamped, err := recToSnapshot(&fakeWorkRec{id: "al-1", status: "open", metadata: map[string]string{workTopologySourceMetadataKey: source}})
	if err != nil {
		t.Fatal(err)
	}
	// The destination store returns the row WITHOUT the stamp (routing leak).
	dest := newFakeWorkStore()
	dest.seed(&fakeWorkRec{id: "al-1", status: "open"})
	report := beads.ImportReport{Inserted: []string{"al-1"}}
	err = verifyCopiedRows(context.Background(), dest, []beads.Snapshot{stamped}, report, source, workUnifyScope{label: "alpha"})
	if err == nil || !strings.Contains(err.Error(), "source database") {
		t.Fatalf("expected routing-proof verify failure on an unstamped row, got %v", err)
	}
	// A correctly-stamped destination passes.
	dest.seed(&fakeWorkRec{id: "al-1", status: "open", metadata: map[string]string{workTopologySourceMetadataKey: source}})
	if err := verifyCopiedRows(context.Background(), dest, []beads.Snapshot{stamped}, report, source, workUnifyScope{label: "alpha"}); err != nil {
		t.Fatalf("stamped destination should pass verify: %v", err)
	}
}

// TestStragglerScopeConfigStateNeverManagedCity pins F2: the residue temp scope
// never writes a managed_city origin (the contract rejects it for non-city
// scopes). An external identity is written verbatim as Explicit; an empty
// identity with no managed port is an error, not managed_city.
func TestStragglerScopeConfigStateNeverManagedCity(t *testing.T) {
	ext, err := stragglerScopeConfigState(t.TempDir(), workResidueSource{Host: "db.example", Port: "3306", Database: "rigdb"})
	if err != nil {
		t.Fatal(err)
	}
	if ext.EndpointOrigin != contract.EndpointOriginExplicit || ext.DoltHost != "db.example" || ext.DoltPort != "3306" {
		t.Fatalf("external identity must be Explicit verbatim, got %+v", ext)
	}
	// Empty host/port with no managed port available → error, never managed_city.
	_, err = stragglerScopeConfigState(t.TempDir(), workResidueSource{Database: "rigdb"})
	if err == nil {
		t.Fatalf("empty identity with no managed port must error, not fall back to managed_city")
	}
}

// TestStragglerResidueScopeResolves pins F2 end-to-end at the contract layer: a
// .gc/store/work-residue/* scope written from a residue identity resolves via
// ResolveDoltConnectionTarget (a managed_city origin there would be REJECTED —
// this proves the Explicit endpoint the builder writes is accepted).
func TestStragglerResidueScopeResolves(t *testing.T) {
	city := t.TempDir()
	root := filepath.Join(city, ".gc", "store", "work-residue", "rigdb-x")
	state, err := stragglerScopeConfigState(city, workResidueSource{Host: "127.0.0.1", Port: "3306", Database: "rigdb"})
	if err != nil {
		t.Fatal(err)
	}
	if err := ensureCanonicalScopeConfigState(fsys.OSFS{}, root, state); err != nil {
		t.Fatal(err)
	}
	if err := ensureCanonicalScopeMetadataForInit(fsys.OSFS{}, root, "rigdb"); err != nil {
		t.Fatal(err)
	}
	target, err := contract.ResolveDoltConnectionTarget(fsys.OSFS{}, city, root)
	if err != nil {
		t.Fatalf("ResolveDoltConnectionTarget on residue scope: %v", err)
	}
	if target.Database != "rigdb" || target.Port != "3306" {
		t.Fatalf("residue scope resolved to %+v, want rigdb:3306", target)
	}
}

// TestReconcileFlaggedRowsAppliesMissingLinks pins F8/F13/F14: a kept-local row's
// missing dep and label are applied on the city store, while a dep whose endpoint
// is a reserved-class bead is skipped (represented by attach metadata).
func TestReconcileFlaggedRowsAppliesMissingLinks(t *testing.T) {
	src, err := recToSnapshot(&fakeWorkRec{
		id: "al-1", status: "open",
		labels: []string{"want-label"},
		deps: []beads.Dep{
			{IssueID: "al-1", DependsOnID: "al-2", Type: "blocks"},
			{IssueID: "al-1", DependsOnID: "gcg-root", Type: "blocks"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	city := newFakeWorkStore()
	city.seed(&fakeWorkRec{id: "al-1", status: "open"}) // no dep, no label
	city.seed(&fakeWorkRec{id: "al-2", status: "open"})
	// ConflictSkipped (not KeptLocal) so no scalar re-import runs — this isolates
	// the dep/label diff-apply path.
	report := beads.ImportReport{ConflictSkipped: []string{"al-1"}}
	if err := reconcileFlaggedRows(context.Background(), city, []beads.Snapshot{src}, report, &strings.Builder{}); err != nil {
		t.Fatal(err)
	}
	deps, _ := city.DepList("al-1", "down")
	var toAl2, toGcg bool
	for _, d := range deps {
		if d.DependsOnID == "al-2" {
			toAl2 = true
		}
		if d.DependsOnID == "gcg-root" {
			toGcg = true
		}
	}
	if !toAl2 {
		t.Fatalf("missing work dep al-1->al-2 should be applied, deps=%v", deps)
	}
	if toGcg {
		t.Fatalf("reserved-class dep al-1->gcg-root must be skipped (attach metadata), deps=%v", deps)
	}
	b1, _ := city.Get("al-1")
	if !recHasLabelBead(b1, "want-label") {
		t.Fatalf("missing label should be applied, labels=%v", b1.Labels)
	}
}

func recHasLabelBead(b beads.Bead, label string) bool {
	for _, l := range b.Labels {
		if l == label {
			return true
		}
	}
	return false
}

// TestLinkDiffDrainedExcludesReservedClassDeps pins F14: a missing dep whose
// endpoint is a reserved-class bead does not keep a source undrained forever.
func TestLinkDiffDrainedExcludesReservedClassDeps(t *testing.T) {
	if !linkDiffDrained(beads.LinkDiff{MissingDeps: []beads.Dep{{IssueID: "al-1", DependsOnID: "gcg-root"}}}) {
		t.Fatalf("a reserved-class missing dep must not block drain")
	}
	if linkDiffDrained(beads.LinkDiff{MissingDeps: []beads.Dep{{IssueID: "al-1", DependsOnID: "al-2"}}}) {
		t.Fatalf("a real work missing dep must block drain")
	}
	if linkDiffDrained(beads.LinkDiff{MissingLabels: []string{"x"}}) {
		t.Fatalf("a missing label must block drain")
	}
}

// TestResidueConvergenceReArmsOnAppend pins F16: appending a residue source to a
// running convergence loop triggers convergence WITHOUT a reboot — the appended
// source drains via the poke, not the slow ticker.
func TestResidueConvergenceReArmsOnAppend(t *testing.T) {
	city := t.TempDir()
	created := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	cityStore := newFakeWorkStore()
	oldStore := newFakeWorkStore()
	oldStore.seed(&fakeWorkRec{id: "al-1", status: "open", issueType: "task", createdAt: created, updatedAt: created})

	origOpen := openWorkUnifyScopeStore
	origStraggler := openWorkUnifyStragglerStore
	origTick := workUnifyResidueTickInterval
	origHook := workUnifyResidueConvergePassHook
	t.Cleanup(func() {
		openWorkUnifyScopeStore = origOpen
		openWorkUnifyStragglerStore = origStraggler
		workUnifyResidueTickInterval = origTick
		workUnifyResidueConvergePassHook = origHook
	})
	openWorkUnifyScopeStore = func(string, string) (beads.Store, func(), error) { return cityStore, func() {}, nil }
	openWorkUnifyStragglerStore = func(string, workResidueSource) (beads.Store, func(), error) {
		return oldStore, func() {}, nil
	}
	// A long tick so the drain we observe can only have come from the poke, and a
	// lifecycle signal per pass so the test waits on the loop, not on wall time.
	workUnifyResidueTickInterval = func() time.Duration { return time.Hour }
	passes := make(chan struct{}, 64)
	workUnifyResidueConvergePassHook = func() {
		select {
		case passes <- struct{}{}:
		default:
		}
	}

	// Marker exists with NO undrained sources yet.
	if err := writeWorkUnifiedMarker(city, nil, nil, 0); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go runWorkUnifyResidueConvergenceLoop(ctx, city, unifiedCityConfig(), &strings.Builder{})

	// The loop stores its poke channel before the first pass; the first pass
	// signal therefore implies the poke channel is registered.
	awaitPass(t, passes, "initial pass")

	// Append a NEW residue source on the running runtime; this kicks convergence.
	if err := appendWorkResidueSource(workUnifiedMarkerPath(city), workResidueSource{Scope: "alpha", Database: "al"}); err != nil {
		t.Fatal(err)
	}

	// The appended source drains without a reboot (via the poke, not the ticker).
	drained := func() bool {
		m, ok, _ := readWorkTopologyMarker(workUnifiedMarkerPath(city))
		return ok && m.undrainedResidueCount() == 0 && len(m.ResidueSources) == 1
	}
	for i := 0; i < 8 && !drained(); i++ {
		awaitPass(t, passes, "post-append convergence pass")
	}
	if !drained() {
		t.Fatalf("appended source did not drain via the poke")
	}
}

// awaitPass blocks on the loop's per-pass lifecycle signal (never a fixed sleep),
// failing if none arrives within the deadline.
func awaitPass(t *testing.T, passes <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-passes:
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for: %s", what)
	}
}

// TestResidueSourceDrained pins the drain check: a source is drained only when
// every WORK row is present-or-older in the unified DB with its links reflected.
func TestResidueSourceDrained(t *testing.T) {
	created := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	old := newFakeWorkStore()
	old.seed(&fakeWorkRec{id: "al-1", status: "open", createdAt: created, updatedAt: created})
	city := newFakeWorkStore()

	// Nothing imported yet: not drained.
	drained, err := residueSourceDrained(context.Background(), city, old)
	if err != nil {
		t.Fatal(err)
	}
	if drained {
		t.Fatalf("empty destination must not be drained")
	}

	// After the row is present with an equal clock: drained.
	city.seed(&fakeWorkRec{id: "al-1", status: "open", createdAt: created, updatedAt: created})
	drained, err = residueSourceDrained(context.Background(), city, old)
	if err != nil {
		t.Fatal(err)
	}
	if !drained {
		t.Fatalf("present-with-equal-clock source must be drained")
	}
}

// TestConvergeWorkUnifiedResidueMarksDrained pins deliverable F: the later-boot
// background pass re-imports each undrained source and, once drained, flips the
// marker's Drained flag (undrained count -> 0).
func TestConvergeWorkUnifiedResidueMarksDrained(t *testing.T) {
	city := t.TempDir()
	created := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	cityStore := newFakeWorkStore()
	oldStore := newFakeWorkStore()
	oldStore.seed(&fakeWorkRec{id: "al-1", status: "open", issueType: "task", createdAt: created, updatedAt: created})

	origOpen := openWorkUnifyScopeStore
	origStraggler := openWorkUnifyStragglerStore
	t.Cleanup(func() {
		openWorkUnifyScopeStore = origOpen
		openWorkUnifyStragglerStore = origStraggler
	})
	openWorkUnifyScopeStore = func(string, string) (beads.Store, func(), error) { return cityStore, func() {}, nil }
	openWorkUnifyStragglerStore = func(string, workResidueSource) (beads.Store, func(), error) {
		return oldStore, func() {}, nil
	}

	// A marker with one undrained residue source.
	if err := writeWorkUnifiedMarker(city, []workUnifyScope{{label: "alpha", database: "al"}}, nil, 0); err != nil {
		t.Fatal(err)
	}

	convergeWorkUnifiedResidue(city, unifiedCityConfig(), &strings.Builder{})

	m, ok, err := readWorkTopologyMarker(workUnifiedMarkerPath(city))
	if err != nil || !ok {
		t.Fatalf("marker read: ok=%v err=%v", ok, err)
	}
	if m.undrainedResidueCount() != 0 {
		t.Fatalf("expected source drained after convergence, undrained=%d", m.undrainedResidueCount())
	}
	if _, err := cityStore.Get("al-1"); err != nil {
		t.Fatalf("residue row must be imported into the unified store: %v", err)
	}
}

// TestEnsureWorkUnifiedCollisionAborts pins gate 6: a city-store row carrying a
// rig prefix that the rig store does not hold aborts before any copy or marker.
func TestEnsureWorkUnifiedCollisionAborts(t *testing.T) {
	city := t.TempDir()
	writeAllClassMigratedMarkers(t, city)
	cityStore := newFakeWorkStore()
	rigStore := newFakeWorkStore()
	// A city-store row with rig alpha's prefix that alpha does NOT hold.
	cityStore.seed(&fakeWorkRec{id: "al-99", status: "open", updatedAt: time.Now()})

	withWorkUnifySeams(t, cityStore, rigStore)

	err := ensureWorkUnified(city, unifiedCityConfig(), &strings.Builder{})
	if err == nil || !strings.Contains(err.Error(), "split-brain") {
		t.Fatalf("expected split-brain collision abort, got %v", err)
	}
	// No marker on abort.
	if _, ok, _ := readWorkTopologyMarker(workUnifiedMarkerPath(city)); ok {
		t.Fatalf("collision abort must not write the marker")
	}
}
