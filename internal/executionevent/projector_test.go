package executionevent

import (
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/gastownhall/gascity/internal/beadmeta"
	"github.com/gastownhall/gascity/internal/beads"
	convoycore "github.com/gastownhall/gascity/internal/convoy"
	"github.com/gastownhall/gascity/internal/events"
)

func TestProjectCurrentUsesExactConvoyMembershipAndPreservesNativeTopology(t *testing.T) {
	graphStore := beads.NewMemStore()
	workStore := beads.NewMemStore()

	member, err := workStore.Create(beads.Bead{ID: "mc-work", Title: "work", Type: "task"})
	if err != nil {
		t.Fatal(err)
	}
	convoy, err := graphStore.Create(beads.Bead{ID: "gcg-input", Title: "input", Type: "convoy"})
	if err != nil {
		t.Fatal(err)
	}
	if err := convoycore.TrackItem(graphStore, convoy.ID, member.ID, workStore); err != nil {
		t.Fatalf("TrackItem: %v", err)
	}
	// Same-store membership is represented twice (tracks and metadata) but must
	// project once. Cross-store membership above has only the metadata form.
	sameStoreMember, err := graphStore.Create(beads.Bead{ID: "gcg-same-store", Title: "same", Type: "task"})
	if err != nil {
		t.Fatal(err)
	}
	if err := convoycore.TrackItem(graphStore, convoy.ID, sameStoreMember.ID); err != nil {
		t.Fatalf("TrackItem same store: %v", err)
	}
	// A parent-child edge is not a tracks membership and must not be projected.
	ignored, err := graphStore.Create(beads.Bead{ID: "mc-parent-child", Title: "ignore", Type: "task"})
	if err != nil {
		t.Fatal(err)
	}
	if err := graphStore.DepAdd(convoy.ID, ignored.ID, "parent-child"); err != nil {
		t.Fatal(err)
	}
	root, err := graphStore.Create(beads.Bead{
		ID: "gcg-root",
		Metadata: map[string]string{
			beadmeta.KindMetadataKey:                   beadmeta.KindWorkflow,
			beadmeta.FormulaContractMetadataKey:        beadmeta.FormulaContractGraphV2,
			beadmeta.InputConvoyIDMetadataKey:          convoy.ID,
			beadmeta.StepIDMetadataKey:                 "workflow-root",
			beadmeta.NativeStepDependenciesMetadataKey: "[]",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	// The root is also a graph-v2 bead and may carry step-shaped metadata. It
	// is never a materialized step event, even when gc.root_bead_id is exact.
	if err := graphStore.SetMetadata(root.ID, beadmeta.RootBeadIDMetadataKey, root.ID); err != nil {
		t.Fatal(err)
	}
	stepBeads := make(map[string]beads.Bead)
	for _, step := range []struct {
		id, stepID, topology string
	}{
		{id: "gcg-root-step", stepID: "root", topology: "[]"},
		{id: "gcg-dependent", stepID: "dependent", topology: `["root"]`},
		{id: "gcg-malformed", stepID: "malformed", topology: `not-json`},
		{id: "gcg-unknown", stepID: "unknown"},
	} {
		metadata := map[string]string{
			beadmeta.RootBeadIDMetadataKey: root.ID,
			beadmeta.StepIDMetadataKey:     step.stepID,
		}
		if step.topology != "" {
			metadata[beadmeta.NativeStepDependenciesMetadataKey] = step.topology
		}
		created, err := graphStore.Create(beads.Bead{ID: step.id, Metadata: metadata})
		if err != nil {
			t.Fatal(err)
		}
		stepBeads[step.stepID] = created
	}

	got, err := ProjectCurrent(graphStore, root.ID, workStore)
	if err != nil {
		t.Fatalf("ProjectCurrent: %v", err)
	}
	memberIDs := []string{sameStoreMember.ID, member.ID}
	sort.Strings(memberIDs)
	if want := []WorkAssociation{
		{WorkBeadID: memberIDs[0], ExecutionRunID: root.ID},
		{WorkBeadID: memberIDs[1], ExecutionRunID: root.ID},
	}; !reflect.DeepEqual(got.WorkAssociations, want) {
		t.Fatalf("work associations = %#v, want %#v", got.WorkAssociations, want)
	}
	wantSteps := []StepDefinition{
		{BeadID: stepBeads["dependent"].ID, ExecutionRunID: root.ID, StepID: "dependent", DependsOnStepIDs: ptr([]string{"root"})},
		{BeadID: stepBeads["malformed"].ID, ExecutionRunID: root.ID, StepID: "malformed"},
		{BeadID: stepBeads["root"].ID, ExecutionRunID: root.ID, StepID: "root", DependsOnStepIDs: ptr([]string{})},
		{BeadID: stepBeads["unknown"].ID, ExecutionRunID: root.ID, StepID: "unknown"},
	}
	sort.Slice(wantSteps, func(i, j int) bool { return wantSteps[i].BeadID < wantSteps[j].BeadID })
	if !reflect.DeepEqual(got.Steps, wantSteps) {
		t.Fatalf("steps = %#v, want %#v", got.Steps, wantSteps)
	}

	recorder := events.NewFake()
	if err := EmitCurrent(recorder, "graph-materializer", graphStore, root.ID, workStore); err != nil {
		t.Fatalf("EmitCurrent: %v", err)
	}
	if got, want := len(recorder.Events), 6; got != want {
		t.Fatalf("emitted events = %d, want %d: %#v", got, want, recorder.Events)
	}
	if event := recorder.Events[0]; event.Type != events.ExecutionWorkAssociated || event.Subject != memberIDs[0] || event.RunID != root.ID || len(event.Payload) != 0 {
		t.Fatalf("same-store work association event = %#v", event)
	}
	if event := recorder.Events[1]; event.Type != events.ExecutionWorkAssociated || event.Subject != memberIDs[1] || event.RunID != root.ID || len(event.Payload) != 0 {
		t.Fatalf("cross-store work association event = %#v", event)
	}
	for i, step := range wantSteps {
		event := recorder.Events[i+2]
		if event.Type != events.ExecutionStepDefined || event.Subject != step.BeadID || event.RunID != step.ExecutionRunID || event.StepID != step.StepID || !reflect.DeepEqual(event.DependsOnStepIDs, step.DependsOnStepIDs) || len(event.Payload) != 0 {
			t.Fatalf("step event %d = %#v, want %#v", i, event, step)
		}
	}
}

func TestProjectCurrentDropsConflictingTrackMetadata(t *testing.T) {
	store := beads.NewMemStore()
	convoy, err := store.Create(beads.Bead{Type: "convoy"})
	if err != nil {
		t.Fatal(err)
	}
	other, err := store.Create(beads.Bead{Type: "convoy"})
	if err != nil {
		t.Fatal(err)
	}
	member, err := store.Create(beads.Bead{Metadata: map[string]string{beadmeta.TrackingConvoyIDMetadataKey: other.ID}})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.DepAdd(convoy.ID, member.ID, convoycore.TrackingDepType); err != nil {
		t.Fatal(err)
	}
	root, err := store.Create(beads.Bead{Metadata: map[string]string{
		beadmeta.KindMetadataKey:            beadmeta.KindWorkflow,
		beadmeta.FormulaContractMetadataKey: beadmeta.FormulaContractGraphV2,
		beadmeta.InputConvoyIDMetadataKey:   convoy.ID,
	}})
	if err != nil {
		t.Fatal(err)
	}
	got, err := ProjectCurrent(store, root.ID)
	if err != nil {
		t.Fatalf("ProjectCurrent: %v", err)
	}
	if len(got.WorkAssociations) != 0 {
		t.Fatalf("conflicting tracks association = %#v, want UNKNOWN/no association", got.WorkAssociations)
	}
}

func TestProjectCurrentDropsMetadataMembershipWhenTracksNamesAnotherConvoy(t *testing.T) {
	store := beads.NewMemStore()
	selected, err := store.Create(beads.Bead{ID: "gcg-selected", Type: "convoy"})
	if err != nil {
		t.Fatal(err)
	}
	other, err := store.Create(beads.Bead{ID: "gcg-other", Type: "convoy"})
	if err != nil {
		t.Fatal(err)
	}
	member, err := store.Create(beads.Bead{ID: "mc-member", Metadata: map[string]string{
		beadmeta.TrackingConvoyIDMetadataKey: selected.ID,
	}})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.DepAdd(other.ID, member.ID, convoycore.TrackingDepType); err != nil {
		t.Fatal(err)
	}
	root, err := store.Create(beads.Bead{ID: "gcg-root", Metadata: map[string]string{
		beadmeta.KindMetadataKey:            beadmeta.KindWorkflow,
		beadmeta.FormulaContractMetadataKey: beadmeta.FormulaContractGraphV2,
		beadmeta.InputConvoyIDMetadataKey:   selected.ID,
	}})
	if err != nil {
		t.Fatal(err)
	}

	got, err := ProjectCurrent(store, root.ID)
	if err != nil {
		t.Fatalf("ProjectCurrent: %v", err)
	}
	if len(got.WorkAssociations) != 0 {
		t.Fatalf("conflicting metadata association = %#v, want UNKNOWN/no association", got.WorkAssociations)
	}
}

func TestProjectCurrentOmitsInvalidPhysicalReferences(t *testing.T) {
	store := beads.NewMemStore()
	convoy, err := store.Create(beads.Bead{ID: "gcg-input", Type: "convoy"})
	if err != nil {
		t.Fatal(err)
	}
	root, err := store.Create(beads.Bead{ID: "gcg-root", Metadata: map[string]string{
		beadmeta.KindMetadataKey:            beadmeta.KindWorkflow,
		beadmeta.FormulaContractMetadataKey: beadmeta.FormulaContractGraphV2,
		beadmeta.InputConvoyIDMetadataKey:   convoy.ID,
	}})
	if err != nil {
		t.Fatal(err)
	}
	projectorStore := projectionStore{
		Store: store,
		depList: func(id, direction string) ([]beads.Dep, error) {
			if id == convoy.ID && direction == "down" {
				return []beads.Dep{{IssueID: convoy.ID, DependsOnID: "MC invalid", Type: convoycore.TrackingDepType}}, nil
			}
			return store.DepList(id, direction)
		},
		listByMetadata: func(filters map[string]string, limit int, opts ...beads.QueryOpt) ([]beads.Bead, error) {
			switch {
			case filters[beadmeta.TrackingConvoyIDMetadataKey] == convoy.ID:
				return []beads.Bead{{ID: "MC metadata invalid"}}, nil
			case filters[beadmeta.RootBeadIDMetadataKey] == root.ID:
				return []beads.Bead{{ID: "GC invalid", Metadata: map[string]string{
					beadmeta.RootBeadIDMetadataKey: root.ID,
					beadmeta.StepIDMetadataKey:     "native-step",
				}}}, nil
			default:
				return store.ListByMetadata(filters, limit, opts...)
			}
		},
	}

	got, err := ProjectCurrent(projectorStore, root.ID)
	if err != nil {
		t.Fatalf("ProjectCurrent: %v", err)
	}
	if len(got.WorkAssociations) != 0 || len(got.Steps) != 0 {
		t.Fatalf("projection = %#v, want invalid physical references omitted", got)
	}
}

func TestProjectCurrentRejectsInvalidRootReference(t *testing.T) {
	store := beads.NewMemStore()
	if _, err := ProjectCurrent(store, "GC root"); err == nil || !strings.Contains(err.Error(), "invalid root reference") {
		t.Fatalf("ProjectCurrent invalid root error = %v, want invalid root reference", err)
	}
}

func TestProjectCurrentIncludesClosedPhysicalRetriesWithoutCollapsingSemanticStep(t *testing.T) {
	store := beads.NewMemStore()
	member, err := store.Create(beads.Bead{Type: "task"})
	if err != nil {
		t.Fatal(err)
	}
	convoy, err := store.Create(beads.Bead{Type: "convoy"})
	if err != nil {
		t.Fatal(err)
	}
	if err := convoycore.TrackItem(store, convoy.ID, member.ID); err != nil {
		t.Fatal(err)
	}
	closed := "closed"
	if err := store.Update(member.ID, beads.UpdateOpts{Status: &closed}); err != nil {
		t.Fatal(err)
	}
	root, err := store.Create(beads.Bead{Metadata: map[string]string{
		beadmeta.KindMetadataKey:            beadmeta.KindWorkflow,
		beadmeta.FormulaContractMetadataKey: beadmeta.FormulaContractGraphV2,
		beadmeta.InputConvoyIDMetadataKey:   convoy.ID,
	}})
	if err != nil {
		t.Fatal(err)
	}
	first, err := store.Create(beads.Bead{Metadata: map[string]string{
		beadmeta.RootBeadIDMetadataKey:             root.ID,
		beadmeta.StepIDMetadataKey:                 "write-report",
		beadmeta.NativeStepDependenciesMetadataKey: `["review"]`,
	}})
	if err != nil {
		t.Fatal(err)
	}
	retry, err := store.Create(beads.Bead{Metadata: map[string]string{
		beadmeta.RootBeadIDMetadataKey:             root.ID,
		beadmeta.StepIDMetadataKey:                 "write-report",
		beadmeta.NativeStepDependenciesMetadataKey: `["review"]`,
	}})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Update(retry.ID, beads.UpdateOpts{Status: &closed}); err != nil {
		t.Fatal(err)
	}

	got, err := ProjectCurrent(store, root.ID)
	if err != nil {
		t.Fatalf("ProjectCurrent: %v", err)
	}
	if want := []WorkAssociation{{WorkBeadID: member.ID, ExecutionRunID: root.ID}}; !reflect.DeepEqual(got.WorkAssociations, want) {
		t.Fatalf("closed work association = %#v, want %#v", got.WorkAssociations, want)
	}
	if len(got.Steps) != 2 {
		t.Fatalf("steps = %#v, want both physical attempts", got.Steps)
	}
	ids := []string{first.ID, retry.ID}
	sort.Strings(ids)
	for i, step := range got.Steps {
		if step.BeadID != ids[i] || step.StepID != "write-report" || !reflect.DeepEqual(step.DependsOnStepIDs, ptr([]string{"review"})) {
			t.Fatalf("retry step %d = %#v, want physical id %q with canonical topology", i, step, ids[i])
		}
	}
}

func TestProjectCurrentRejectsNonGraphRootOrMissingInputConvoy(t *testing.T) {
	store := beads.NewMemStore()
	plain, err := store.Create(beads.Bead{ID: "plain"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ProjectCurrent(store, plain.ID); err == nil {
		t.Fatal("ProjectCurrent accepted a non-graph root")
	}
	graph, err := store.Create(beads.Bead{ID: "graph", Metadata: map[string]string{
		beadmeta.KindMetadataKey:            beadmeta.KindWorkflow,
		beadmeta.FormulaContractMetadataKey: beadmeta.FormulaContractGraphV2,
	}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ProjectCurrent(store, graph.ID); err == nil {
		t.Fatal("ProjectCurrent accepted a graph root without gc.input_convoy_id")
	}
}

func TestEmitProjectionClonesTopologyAndPreservesProjection(t *testing.T) {
	dependencies := []string{"prepare"}
	projection := Projection{
		WorkAssociations: []WorkAssociation{{WorkBeadID: "mc-work", ExecutionRunID: "gcg-root"}},
		Steps: []StepDefinition{{
			BeadID:           "gcg-step",
			ExecutionRunID:   "gcg-root",
			StepID:           "implement",
			DependsOnStepIDs: &dependencies,
		}},
	}
	recorder := events.NewFake()
	if err := EmitProjection(recorder, "execution-reemit", projection); err != nil {
		t.Fatalf("EmitProjection: %v", err)
	}
	if got, want := len(recorder.Events), 2; got != want {
		t.Fatalf("event count = %d, want %d", got, want)
	}
	if got := recorder.Events[0]; got.Type != events.ExecutionWorkAssociated || got.Subject != "mc-work" || got.RunID != "gcg-root" {
		t.Fatalf("work event = %#v", got)
	}
	if got := recorder.Events[1]; got.Type != events.ExecutionStepDefined || got.Subject != "gcg-step" || got.StepID != "implement" || !reflect.DeepEqual(got.DependsOnStepIDs, &dependencies) {
		t.Fatalf("step event = %#v", got)
	}
	(*recorder.Events[1].DependsOnStepIDs)[0] = "mutated"
	if got := (*projection.Steps[0].DependsOnStepIDs)[0]; got != "prepare" {
		t.Fatalf("projection topology mutated to %q", got)
	}
}

func ptr(values []string) *[]string { return &values }

type projectionStore struct {
	beads.Store
	depList        func(id, direction string) ([]beads.Dep, error)
	listByMetadata func(filters map[string]string, limit int, opts ...beads.QueryOpt) ([]beads.Bead, error)
}

func (s projectionStore) DepList(id, direction string) ([]beads.Dep, error) {
	return s.depList(id, direction)
}

func (s projectionStore) ListByMetadata(filters map[string]string, limit int, opts ...beads.QueryOpt) ([]beads.Bead, error) {
	return s.listByMetadata(filters, limit, opts...)
}
