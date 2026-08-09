package executionevent

import (
	"compress/gzip"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/gastownhall/gascity/internal/beadmeta"
	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/events"
)

func TestLifecycleEventsPreserveNativeGraphIdentityAndTopology(t *testing.T) {
	root := beads.Bead{ID: "gcg-run", Metadata: map[string]string{
		beadmeta.KindMetadataKey: "workflow", beadmeta.FormulaContractMetadataKey: beadmeta.FormulaContractGraphV2,
	}}
	rootDeps := "[]"
	fanoutDeps := `["root"]`
	joinDeps := `["fan-a","fan-b"]`
	for _, tc := range []struct {
		name, ref, step, topology string
		wantDeps                  *[]string
	}{
		{"root", "gcg-root-attempt", "root", rootDeps, lifecycleStrings([]string{})},
		{"fanout", "gcg-fan-a-attempt", "fan-a", fanoutDeps, lifecycleStrings([]string{"root"})},
		{"join", "gcg-join-attempt", "join", joinDeps, lifecycleStrings([]string{"fan-a", "fan-b"})},
	} {
		t.Run(tc.name, func(t *testing.T) {
			step := beads.Bead{ID: tc.ref, Status: "in_progress", Metadata: map[string]string{
				beadmeta.RootBeadIDMetadataKey: root.ID, beadmeta.StepIDMetadataKey: tc.step,
				beadmeta.SessionIDMetadataKey: "gcs-session", beadmeta.NativeStepDependenciesMetadataKey: tc.topology,
			}}
			started, ok := LifecycleEvent(events.ExecutionStepStarted, root, step, "worker")
			if !ok {
				t.Fatal("LifecycleEvent(started) = false")
			}
			if started.Type != events.ExecutionStepStarted || started.Subject != tc.ref || started.RunID != root.ID || started.SessionID != "gcs-session" || started.StepID != tc.step || !reflect.DeepEqual(started.DependsOnStepIDs, tc.wantDeps) {
				t.Fatalf("started = %#v", started)
			}
			step.Status = "closed"
			completed, ok := LifecycleEvent(events.ExecutionStepCompleted, root, step, "close-hook")
			if !ok || completed.Type != events.ExecutionStepCompleted || !reflect.DeepEqual(completed.DependsOnStepIDs, tc.wantDeps) {
				t.Fatalf("completed = %#v, ok=%v", completed, ok)
			}
		})
	}
}

func TestEmitCompletedFromClosedNotificationUsesPhysicalSnapshot(t *testing.T) {
	graph := beads.NewMemStore()
	root := mustCreateProjectionRoot(t, graph, "")
	step := mustCreateProjectionStep(t, graph, "gcg-retry-attempt", root.ID, "build", `["prepare"]`)
	step.Status = "closed"
	step.Metadata[beadmeta.SessionIDMetadataKey] = "gcs-session"
	payload, err := json.Marshal(step)
	if err != nil {
		t.Fatal(err)
	}
	rec := events.NewFake()
	if !EmitCompletedFromClosedNotification(rec, graph, payload, "close-hook") {
		t.Fatal("close notification did not emit completed")
	}
	if len(rec.Events) != 1 {
		t.Fatalf("events = %#v", rec.Events)
	}
	got := rec.Events[0]
	if got.Type != events.ExecutionStepCompleted || got.Subject != step.ID || got.RunID != root.ID || got.SessionID != "gcs-session" || got.StepID != "build" || !reflect.DeepEqual(got.DependsOnStepIDs, lifecycleStrings([]string{"prepare"})) {
		t.Fatalf("completed = %#v", got)
	}
	legacy := step
	legacy.Metadata[beadmeta.RootBeadIDMetadataKey] = "unknown"
	payload, _ = json.Marshal(legacy)
	if EmitCompletedFromClosedNotification(rec, graph, payload, "close-hook") {
		t.Fatal("unresolved close notification emitted")
	}
}

func TestReconcileCompletedRepairsMissingFactAndRetainsConflictingHistory(t *testing.T) {
	graph := beads.NewMemStore()
	root := mustCreateProjectionRoot(t, graph, "")
	step := mustCreateProjectionStep(t, graph, "gcg-attempt", root.ID, "build", `["prepare"]`)
	closed := "closed"
	if err := graph.Update(step.ID, beads.UpdateOpts{Status: &closed, Metadata: map[string]string{beadmeta.SessionIDMetadataKey: "gcs-session"}}); err != nil {
		t.Fatal(err)
	}
	recorder := events.NewFake()
	// This looks like an already-emitted lifecycle fact by subject, but its
	// session is stale. It must not suppress the authoritative correction.
	recorder.Record(events.Event{
		Type: events.ExecutionStepCompleted, Subject: step.ID, RunID: root.ID,
		SessionID: "gcs-stale", StepID: "build", DependsOnStepIDs: lifecycleStrings([]string{"prepare"}),
	})

	if got := ReconcileCompleted(recorder, beads.GraphStore{Store: graph}, "execution-reconcile"); got != 1 {
		t.Fatalf("ReconcileCompleted = %d, want 1 correction", got)
	}
	if got := ReconcileCompleted(recorder, beads.GraphStore{Store: graph}, "execution-reconcile"); got != 0 {
		t.Fatalf("second ReconcileCompleted = %d, want exact-fact no-op", got)
	}
	completed, err := recorder.List(events.Filter{Type: events.ExecutionStepCompleted, Subject: step.ID})
	if err != nil {
		t.Fatal(err)
	}
	if len(completed) != 2 || completed[1].SessionID != "gcs-session" || completed[1].RunID != root.ID || completed[1].StepID != "build" {
		t.Fatalf("completed facts = %#v, want stale history plus authoritative correction", completed)
	}
}

func TestReconcileCompletedDoesNotDuplicateFactDuringFileRecorderRotation(t *testing.T) {
	graph := beads.NewMemStore()
	root := mustCreateProjectionRoot(t, graph, "")
	step := mustCreateProjectionStep(t, graph, "gcg-rotation-attempt", root.ID, "build", "[]")
	closed := "closed"
	if err := graph.Update(step.ID, beads.UpdateOpts{Status: &closed, Metadata: map[string]string{beadmeta.SessionIDMetadataKey: "gcs-session"}}); err != nil {
		t.Fatal(err)
	}
	step, err := graph.Get(step.ID)
	if err != nil {
		t.Fatal(err)
	}
	completed, ok := LifecycleEvent(events.ExecutionStepCompleted, root, step, "close-hook")
	if !ok {
		t.Fatal("LifecycleEvent(completed) = false")
	}

	path := filepath.Join(t.TempDir(), "events.jsonl")
	recorder, err := events.NewFileRecorder(path, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = recorder.Close() })
	recorder.Record(completed)
	rotation, err := recorder.ForceRotate()
	if err != nil {
		t.Fatal(err)
	}
	if !rotation.Rotated {
		t.Fatalf("ForceRotate = %#v, want archive", rotation)
	}
	recorder.WaitForRotations()
	if got := ReconcileCompleted(recorder, beads.GraphStore{Store: graph}, "execution-reconcile"); got != 0 {
		t.Fatalf("ReconcileCompleted after archived rotation = %d, want 0 duplicate facts", got)
	}

	// Re-create the state after the active file is renamed but before its
	// asynchronous gzip promotion. FileRecorder.List deliberately cannot see
	// this segment; ListInFlight must supply it to a durable reconciler.
	rotating := filepath.Join(filepath.Dir(path), "events.jsonl.rotating-20260805T000000Z-seq-1-1")
	expandArchiveToRotating(t, rotation.ArchivePath, rotating)

	if got := ReconcileCompleted(recorder, beads.GraphStore{Store: graph}, "execution-reconcile"); got != 0 {
		t.Fatalf("ReconcileCompleted during rotation = %d, want 0 duplicate facts", got)
	}
	all, err := recorder.ListInFlight(events.Filter{Type: events.ExecutionStepCompleted, Subject: step.ID})
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 1 {
		t.Fatalf("completion facts during rotation = %#v, want exactly archived fact", all)
	}
}

func expandArchiveToRotating(t *testing.T, archivePath, rotatingPath string) {
	t.Helper()
	archive, err := os.Open(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	reader, err := gzip.NewReader(archive)
	if err != nil {
		_ = archive.Close()
		t.Fatal(err)
	}
	data, err := io.ReadAll(reader)
	if closeErr := reader.Close(); err == nil {
		err = closeErr
	}
	if closeErr := archive.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(rotatingPath, data, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(archivePath); err != nil {
		t.Fatal(err)
	}
}

func TestReconcileCompletedStoresPreloadsExactFactsOnceAndFailsClosed(t *testing.T) {
	firstGraph := beads.NewMemStore()
	firstRoot := mustCreateProjectionRoot(t, firstGraph, "")
	nilTopologyStep := mustCreateProjectionStep(t, firstGraph, "gcg-nil-topology", firstRoot.ID, "build", "")
	closed := "closed"
	if err := firstGraph.Update(nilTopologyStep.ID, beads.UpdateOpts{Status: &closed, Metadata: map[string]string{beadmeta.SessionIDMetadataKey: "gcs-session"}}); err != nil {
		t.Fatal(err)
	}
	nilTopologyStep, err := firstGraph.Get(nilTopologyStep.ID)
	if err != nil {
		t.Fatal(err)
	}
	nilTopologyFact, ok := LifecycleEvent(events.ExecutionStepCompleted, firstRoot, nilTopologyStep, "prior-reconcile")
	if !ok || nilTopologyFact.DependsOnStepIDs != nil {
		t.Fatalf("nil topology fact = %#v, ok=%v", nilTopologyFact, ok)
	}

	secondGraph := beads.NewMemStore()
	secondRoot := mustCreateProjectionRoot(t, secondGraph, "")
	emptyTopologyStep := mustCreateProjectionStep(t, secondGraph, "gcg-empty-topology", secondRoot.ID, "test", "[]")
	if err := secondGraph.Update(emptyTopologyStep.ID, beads.UpdateOpts{Status: &closed, Metadata: map[string]string{beadmeta.SessionIDMetadataKey: "gcs-session"}}); err != nil {
		t.Fatal(err)
	}
	emptyTopologyStep, err = secondGraph.Get(emptyTopologyStep.ID)
	if err != nil {
		t.Fatal(err)
	}
	emptyTopologyFact, ok := LifecycleEvent(events.ExecutionStepCompleted, secondRoot, emptyTopologyStep, "prior-reconcile")
	if !ok || !reflect.DeepEqual(emptyTopologyFact.DependsOnStepIDs, lifecycleStrings([]string{})) {
		t.Fatalf("empty topology fact = %#v, ok=%v", emptyTopologyFact, ok)
	}

	backing := events.NewFake()
	backing.Record(nilTopologyFact) // Exact fact: must suppress this candidate.
	emptyTopologyFact.DependsOnStepIDs = nil
	backing.Record(emptyTopologyFact) // Same tuple except unknown, not known-empty, topology.
	provider := &countingEventProvider{Provider: backing}
	stores := []beads.GraphStore{{Store: firstGraph}, {Store: secondGraph}}
	if got := ReconcileCompletedStores(provider, stores, "execution-reconcile"); got != 1 {
		t.Fatalf("ReconcileCompletedStores = %d, want one topology correction", got)
	}
	if provider.listCalls != 1 {
		t.Fatalf("completed fact List calls = %d, want one across both stores", provider.listCalls)
	}
	if got := ReconcileCompletedStores(provider, stores, "execution-reconcile"); got != 0 {
		t.Fatalf("second ReconcileCompletedStores = %d, want exact-fact no-op", got)
	}
	if provider.listCalls != 2 {
		t.Fatalf("completed fact List calls after second pass = %d, want one per pass", provider.listCalls)
	}

	before, err := backing.List(events.Filter{Type: events.ExecutionStepCompleted})
	if err != nil {
		t.Fatal(err)
	}
	failing := &countingEventProvider{Provider: backing, listErr: errors.New("journal unavailable")}
	if got := ReconcileCompletedStores(failing, stores, "execution-reconcile"); got != 0 {
		t.Fatalf("ReconcileCompletedStores with List error = %d, want fail-closed zero", got)
	}
	if failing.listCalls != 1 {
		t.Fatalf("failed completed fact List calls = %d, want one", failing.listCalls)
	}
	after, err := backing.List(events.Filter{Type: events.ExecutionStepCompleted})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(after, before) {
		t.Fatalf("List error recorded events: before=%#v after=%#v", before, after)
	}
}

type countingEventProvider struct {
	events.Provider
	listCalls int
	listErr   error
}

func (p *countingEventProvider) List(filter events.Filter) ([]events.Event, error) {
	p.listCalls++
	if p.listErr != nil {
		return nil, p.listErr
	}
	return p.Provider.List(filter)
}

// countingGraphStore counts the per-root step walk a reconcile pass performs.
type countingGraphStore struct {
	beads.Store
	getCalls  int
	listCalls int
}

func (s *countingGraphStore) Get(id string) (beads.Bead, error) {
	s.getCalls++
	return s.Store.Get(id)
}

func (s *countingGraphStore) ListByMetadata(match map[string]string, limit int, opts ...beads.QueryOpt) ([]beads.Bead, error) {
	s.listCalls++
	return s.Store.ListByMetadata(match, limit, opts...)
}

// A steady-state patrol tick must not re-read the whole completion journal and
// re-walk every retained closed root. Once a closed root's steps are all closed
// and projected, the memo settles it: nothing else can produce a completion
// fact for it short of the root row changing.
func TestReconcileCompletedStoresMemoSkipsSettledClosedRoots(t *testing.T) {
	backingGraph := beads.NewMemStore()
	root := mustCreateProjectionRoot(t, backingGraph, "")
	step := mustCreateProjectionStep(t, backingGraph, "gcg-memo-attempt", root.ID, "build", "[]")
	closed := "closed"
	if err := backingGraph.Update(step.ID, beads.UpdateOpts{Status: &closed, Metadata: map[string]string{beadmeta.SessionIDMetadataKey: "gcs-session"}}); err != nil {
		t.Fatal(err)
	}
	if err := backingGraph.Close(root.ID); err != nil {
		t.Fatal(err)
	}

	graph := &countingGraphStore{Store: backingGraph}
	stores := []beads.GraphStore{{Store: graph}}
	provider := &countingEventProvider{Provider: events.NewFake()}
	memo := NewCompletionMemo()

	if got := ReconcileCompletedStoresMemo(provider, stores, "execution-reconcile", memo); got != 1 {
		t.Fatalf("first ReconcileCompletedStoresMemo = %d, want one repaired fact", got)
	}
	if provider.listCalls != 1 {
		t.Fatalf("journal reads on the first pass = %d, want 1", provider.listCalls)
	}
	if graph.getCalls == 0 {
		t.Fatal("first pass performed no step Gets; fixture does not exercise the walk")
	}
	listsAfterFirst := provider.listCalls
	getsAfterFirst := graph.getCalls
	rootListsAfterFirst := graph.listCalls

	if got := ReconcileCompletedStoresMemo(provider, stores, "execution-reconcile", memo); got != 0 {
		t.Fatalf("second ReconcileCompletedStoresMemo = %d, want exact-fact no-op", got)
	}
	if provider.listCalls != listsAfterFirst {
		t.Fatalf("journal reads after a no-change tick = %d, want %d (zero re-reads)", provider.listCalls, listsAfterFirst)
	}
	if graph.getCalls != getsAfterFirst {
		t.Fatalf("step Gets after a no-change tick = %d, want %d (zero per-root Gets)", graph.getCalls, getsAfterFirst)
	}
	if graph.listCalls != rootListsAfterFirst+1 {
		t.Fatalf("root list calls = %d, want %d; the root sweep itself stays", graph.listCalls, rootListsAfterFirst+1)
	}

	// Boot / crash restart: the memo is process-local, so a fresh one always
	// replays the full pass — including the journal read and the step walk.
	if got := ReconcileCompletedStoresMemo(provider, stores, "execution-reconcile", NewCompletionMemo()); got != 0 {
		t.Fatalf("boot pass with a fresh memo = %d, want exact-fact no-op", got)
	}
	if provider.listCalls != listsAfterFirst+1 {
		t.Fatalf("journal reads after a fresh-memo boot pass = %d, want %d", provider.listCalls, listsAfterFirst+1)
	}
	if graph.getCalls <= getsAfterFirst {
		t.Fatalf("step Gets after a fresh-memo boot pass = %d, want more than %d", graph.getCalls, getsAfterFirst)
	}

	// A nil memo keeps the pre-memo contract for the plain entry points.
	listsBeforeNil := provider.listCalls
	if got := ReconcileCompletedStores(provider, stores, "execution-reconcile"); got != 0 {
		t.Fatalf("ReconcileCompletedStores = %d, want exact-fact no-op", got)
	}
	if provider.listCalls != listsBeforeNil+1 {
		t.Fatalf("journal reads without a memo = %d, want %d", provider.listCalls, listsBeforeNil+1)
	}
}

// The crash window this scan exists for is an open root whose step closed
// event-silently. Such a root must never settle, however many ticks run.
func TestReconcileCompletedStoresMemoKeepsScanningUnsettledRoots(t *testing.T) {
	backingGraph := beads.NewMemStore()
	root := mustCreateProjectionRoot(t, backingGraph, "")
	first := mustCreateProjectionStep(t, backingGraph, "gcg-open-root-first", root.ID, "build", "[]")
	closed := "closed"
	if err := backingGraph.Update(first.ID, beads.UpdateOpts{Status: &closed, Metadata: map[string]string{beadmeta.SessionIDMetadataKey: "gcs-session"}}); err != nil {
		t.Fatal(err)
	}

	graph := &countingGraphStore{Store: backingGraph}
	stores := []beads.GraphStore{{Store: graph}}
	provider := &countingEventProvider{Provider: events.NewFake()}
	memo := NewCompletionMemo()

	if got := ReconcileCompletedStoresMemo(provider, stores, "execution-reconcile", memo); got != 1 {
		t.Fatalf("first pass = %d, want one repaired fact", got)
	}
	getsAfterFirst := graph.getCalls

	// The root is still open, so the pass keeps walking it.
	if got := ReconcileCompletedStoresMemo(provider, stores, "execution-reconcile", memo); got != 0 {
		t.Fatalf("second pass = %d, want exact-fact no-op", got)
	}
	if graph.getCalls <= getsAfterFirst {
		t.Fatalf("step Gets on an open root = %d, want more than %d", graph.getCalls, getsAfterFirst)
	}

	// A later event-silent close under the still-open root is repaired.
	second := mustCreateProjectionStep(t, backingGraph, "gcg-open-root-second", root.ID, "test", "[]")
	if err := backingGraph.Update(second.ID, beads.UpdateOpts{Status: &closed, Metadata: map[string]string{beadmeta.SessionIDMetadataKey: "gcs-session"}}); err != nil {
		t.Fatal(err)
	}
	if got := ReconcileCompletedStoresMemo(provider, stores, "execution-reconcile", memo); got != 1 {
		t.Fatalf("pass after a later event-silent close = %d, want one repaired fact", got)
	}
}

// A settled root that is touched again (its row changes) must be re-walked.
func TestReconcileCompletedStoresMemoRescansChangedSettledRoot(t *testing.T) {
	backingGraph := beads.NewMemStore()
	root := mustCreateProjectionRoot(t, backingGraph, "")
	step := mustCreateProjectionStep(t, backingGraph, "gcg-resettle-attempt", root.ID, "build", "[]")
	closed := "closed"
	if err := backingGraph.Update(step.ID, beads.UpdateOpts{Status: &closed, Metadata: map[string]string{beadmeta.SessionIDMetadataKey: "gcs-session"}}); err != nil {
		t.Fatal(err)
	}
	if err := backingGraph.Close(root.ID); err != nil {
		t.Fatal(err)
	}

	graph := &countingGraphStore{Store: backingGraph}
	stores := []beads.GraphStore{{Store: graph}}
	provider := &countingEventProvider{Provider: events.NewFake()}
	memo := NewCompletionMemo()
	if got := ReconcileCompletedStoresMemo(provider, stores, "execution-reconcile", memo); got != 1 {
		t.Fatalf("first pass = %d, want one repaired fact", got)
	}
	getsAfterFirst := graph.getCalls

	if err := backingGraph.Update(root.ID, beads.UpdateOpts{Metadata: map[string]string{"gc.touched": "1"}}); err != nil {
		t.Fatal(err)
	}
	if got := ReconcileCompletedStoresMemo(provider, stores, "execution-reconcile", memo); got != 0 {
		t.Fatalf("pass after root row change = %d, want exact-fact no-op", got)
	}
	if graph.getCalls <= getsAfterFirst {
		t.Fatalf("step Gets after root row change = %d, want more than %d (memo must not go blind)", graph.getCalls, getsAfterFirst)
	}
}

func TestCompletedFactKeyDistinguishesUnknownFromKnownEmptyTopology(t *testing.T) {
	base := events.Event{Subject: "gcg-attempt", RunID: "gcg-run", SessionID: "gcs-session", StepID: "build"}
	var nilSlice []string
	emptySlice := []string{}
	unknown := completedFactKeyFor(base)
	presentNil := completedFactKeyFor(events.Event{
		Subject: base.Subject, RunID: base.RunID, SessionID: base.SessionID, StepID: base.StepID, DependsOnStepIDs: &nilSlice,
	})
	presentEmpty := completedFactKeyFor(events.Event{
		Subject: base.Subject, RunID: base.RunID, SessionID: base.SessionID, StepID: base.StepID, DependsOnStepIDs: &emptySlice,
	})
	if presentNil != presentEmpty {
		t.Fatalf("present nil topology key = %#v, present empty topology key = %#v; want equality", presentNil, presentEmpty)
	}
	if unknown == presentEmpty {
		t.Fatalf("unknown topology key = %#v, known empty topology key = %#v; want distinction", unknown, presentEmpty)
	}
}

func TestLifecycleEventRetainsUnknownAndRejectsNonNativeOrInvalidFacts(t *testing.T) {
	root := beads.Bead{ID: "gcg-run", Metadata: map[string]string{beadmeta.KindMetadataKey: "workflow", beadmeta.FormulaContractMetadataKey: beadmeta.FormulaContractGraphV2}}
	base := beads.Bead{ID: "gcg-attempt", Status: "in_progress", Metadata: map[string]string{beadmeta.RootBeadIDMetadataKey: root.ID, beadmeta.StepIDMetadataKey: "build", beadmeta.SessionIDMetadataKey: "gcs-session"}}
	got, ok := LifecycleEvent(events.ExecutionStepStarted, root, base, "worker")
	if !ok || got.DependsOnStepIDs != nil {
		t.Fatalf("unknown topology = %#v, ok=%v", got, ok)
	}
	for _, mutate := range []func(*beads.Bead){
		func(b *beads.Bead) { b.Metadata[beadmeta.SessionIDMetadataKey] = "" },
		func(b *beads.Bead) { b.Metadata[beadmeta.StepIDMetadataKey] = " " },
		func(b *beads.Bead) { b.Metadata[beadmeta.RootBeadIDMetadataKey] = "external-root" },
	} {
		step := base
		step.Metadata = map[string]string{}
		for k, v := range base.Metadata {
			step.Metadata[k] = v
		}
		mutate(&step)
		if _, ok := LifecycleEvent(events.ExecutionStepStarted, root, step, "worker"); ok {
			t.Fatalf("invalid step emitted: %#v", step)
		}
	}
	invalidTopology := base
	invalidTopology.Metadata = map[string]string{}
	for k, v := range base.Metadata {
		invalidTopology.Metadata[k] = v
	}
	invalidTopology.Metadata[beadmeta.NativeStepDependenciesMetadataKey] = `["build"]`
	if event, ok := LifecycleEvent(events.ExecutionStepStarted, root, invalidTopology, "worker"); !ok || event.DependsOnStepIDs != nil {
		t.Fatalf("malformed topology must degrade to unknown, got %#v ok=%v", event, ok)
	}
	legacy := root
	legacy.Metadata = map[string]string{beadmeta.KindMetadataKey: "workflow"}
	if _, ok := LifecycleEvent(events.ExecutionStepStarted, legacy, base, "worker"); ok {
		t.Fatal("v1 root emitted lifecycle event")
	}
	control := base
	control.Metadata = map[string]string{}
	for k, v := range base.Metadata {
		control.Metadata[k] = v
	}
	control.Metadata[beadmeta.KindMetadataKey] = "check"
	if _, ok := LifecycleEvent(events.ExecutionStepCompleted, root, control, "close-hook"); ok {
		t.Fatal("control close emitted lifecycle event")
	}
}

func lifecycleStrings(v []string) *[]string { return &v }
