package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/danielgtaylor/huma/v2"
	"github.com/gastownhall/gascity/internal/beadmeta"
	"github.com/gastownhall/gascity/internal/beads"
)

func TestRunDetailUsesAuthoritativeGraph(t *testing.T) {
	root := runRootBead("run-topology", "branching-formula", "open")
	root.Metadata["gc.formula_hash"] = "sha256:abc123"
	root.Metadata["gc.formula_source"] = "packs/example/formulas/branching-formula.toml"
	root.Metadata["gc.root_store_ref"] = "city:test-city"

	prepare := runDetailBead("prepare-physical", root.ID, "prepare", "Prepare", "closed", nil)
	prepare.Metadata["gc.outcome"] = "pass"

	reviewAttempt1 := runDetailBead("review-attempt-1", root.ID, "review", "Review", "closed", map[string]string{
		"gc.attempt":    "1",
		"gc.iteration":  "3",
		"gc.kind":       "retry",
		"gc.session_id": "gc-session-a1",
	})
	reviewAttempt1.Metadata["gc.outcome"] = "fail"
	reviewAttempt2 := runDetailBead("review-attempt-2", root.ID, "review", "Review", "in_progress", map[string]string{
		"gc.attempt":      "2",
		"gc.iteration":    "3",
		"gc.session_id":   "gc-session-a2",
		"gc.session_name": "review-worker",
	})
	publish := runDetailBead("publish-physical", root.ID, "publish", "Publish", "blocked", nil)
	notify := runDetailBead("notify-physical", root.ID, "notify", "Notify", "blocked", nil)
	dynamic := runDetailBead("dynamic-physical", root.ID, "dynamic-child", "Dynamic child", "open", map[string]string{
		"gc.dynamic_fragment": "true",
	})
	finalize := runDetailBead("finalize-physical", root.ID, "finalize", "Finalize", "open", map[string]string{
		"gc.kind": "workflow-finalize",
	})

	graph := []beads.Bead{
		root,
		prepare,
		reviewAttempt1,
		reviewAttempt2,
		publish,
		notify,
		dynamic,
		finalize,
	}
	deps := []beads.Dep{
		{IssueID: reviewAttempt1.ID, DependsOnID: prepare.ID, Type: "blocks"},
		{IssueID: reviewAttempt2.ID, DependsOnID: prepare.ID, Type: "blocks"},
		{IssueID: publish.ID, DependsOnID: reviewAttempt2.ID, Type: "blocks"},
		{IssueID: notify.ID, DependsOnID: reviewAttempt2.ID, Type: "blocks"},
		{IssueID: dynamic.ID, DependsOnID: reviewAttempt2.ID, Type: "blocks"},
		{IssueID: notify.ID, DependsOnID: prepare.ID, Type: "custom-gate"},
	}

	fs := newFakeState(t)
	store := beads.NewMemStoreFrom(len(graph), graph, deps)
	fs.cityBeadStore = store
	fs.stores["myrig"] = store

	// The warm event projection deliberately knows only the root. Detail must
	// still read every node and edge from the authoritative bead graph.
	writeRunEventLog(t, fs.cityPath, beadCreatedEvent(1, root))

	s := &Server{state: fs}
	out, err := s.humaHandleRunDetail(context.Background(), &RunDetailInput{
		CityScope: CityScope{CityName: "test-city"},
		RunID:     root.ID,
	})
	if err != nil {
		t.Fatalf("humaHandleRunDetail: %v", err)
	}

	got := out.Body
	if got.RunID != root.ID || got.RootBeadID != root.ID {
		t.Fatalf("run/root identity = %q/%q, want %q/%q", got.RunID, got.RootBeadID, root.ID, root.ID)
	}
	if got.City != "test-city" || got.Scope.Kind != "city" || got.Scope.Ref != "test-city" {
		t.Fatalf("scope = city %q %+v, want test-city city/test-city", got.City, got.Scope)
	}
	if got.Formula.Name != "branching-formula" ||
		got.Formula.Hash != "sha256:abc123" ||
		got.Formula.Source != "packs/example/formulas/branching-formula.toml" {
		t.Fatalf("formula identity = %+v", got.Formula)
	}
	if got.Source.Kind != "gascity_bead_graph" ||
		!got.Source.Available ||
		got.Source.Partial ||
		got.Source.Truncated {
		t.Fatalf("source state = %+v, want available complete graph", got.Source)
	}

	nodes := make(map[string]RunDetailNode, len(got.Nodes))
	for _, node := range got.Nodes {
		nodes[node.SemanticID] = node
	}
	if len(nodes) < 6 {
		t.Fatalf("nodes = %d, want authoritative graph nodes absent from the event fold: %+v", len(nodes), got.Nodes)
	}
	review, ok := nodes["review"]
	if !ok {
		t.Fatalf("review semantic node missing: %+v", got.Nodes)
	}
	if len(review.Executions) != 2 {
		t.Fatalf("review executions = %d, want two physical attempts: %+v", len(review.Executions), review.Executions)
	}
	if review.ConstructKind != RunDetailConstructRetry || review.ExecutionKind != "retry" {
		t.Fatalf("review kind = %q/%q, want retry/retry", review.ConstructKind, review.ExecutionKind)
	}
	latest := executionByID(t, review.Executions, reviewAttempt2.ID)
	if latest.PhysicalID != reviewAttempt2.ID || latest.BeadID != reviewAttempt2.ID {
		t.Fatalf("physical identity = %+v, want %q", latest, reviewAttempt2.ID)
	}
	if latest.Attempt == nil || *latest.Attempt != 2 || latest.Iteration == nil || *latest.Iteration != 3 {
		t.Fatalf("attempt/iteration = %v/%v, want 2/3", latest.Attempt, latest.Iteration)
	}
	if latest.Session.Availability != RunDetailSessionAttached ||
		latest.Session.ID != "gc-session-a2" ||
		latest.Session.Name != "review-worker" {
		t.Fatalf("session = %+v, want attached gc-session-a2/review-worker", latest.Session)
	}
	if node, ok := nodes["dynamic-child"]; !ok || !node.Dynamic {
		t.Fatalf("dynamic child missing or not marked dynamic: %+v", node)
	}
	rootNode, ok := nodes[root.ID]
	if !ok || len(rootNode.ControlBadges) != 1 ||
		rootNode.ControlBadges[0].Kind != RunDetailConstructFinalize {
		t.Fatalf("root control badges = %+v, want typed finalize badge", rootNode.ControlBadges)
	}

	assertRunDetailEdge(t, got.Edges, "review", "publish", RunDetailEdgeBlocks, "blocks")
	assertRunDetailEdge(t, got.Edges, "review", "notify", RunDetailEdgeBlocks, "blocks")
	assertRunDetailEdge(t, got.Edges, "review", "dynamic-child", RunDetailEdgeBlocks, "blocks")
	assertRunDetailEdge(t, got.Edges, "prepare", "notify", RunDetailEdgeUnknown, "custom-gate")
}

func TestRunDetailReportsInstantiationFenceAsPartial(t *testing.T) {
	root := runRootBead("run-instantiating", "branching-formula", "open")
	root.Metadata[beadmeta.RootStoreRefMetadataKey] = "city:test-city"
	step := runDetailBead("step-instantiating", root.ID, "step", "Step", "open", map[string]string{
		beadmeta.InstantiatingMetadataKey: "true",
	})

	fs := newFakeState(t)
	store := beads.NewMemStoreFrom(2, []beads.Bead{root, step}, nil)
	fs.cityBeadStore = store
	fs.stores["myrig"] = store

	out, err := (&Server{state: fs}).humaHandleRunDetail(context.Background(), &RunDetailInput{
		CityScope: CityScope{CityName: "test-city"},
		RunID:     root.ID,
	})
	if err != nil {
		t.Fatalf("humaHandleRunDetail: %v", err)
	}
	if !out.Body.Source.Partial {
		t.Fatalf("source = %+v, want partial while a graph bead is instantiating", out.Body.Source)
	}
	if out.Body.Source.Truncated {
		t.Fatalf("source = %+v, want uncapped graph read to remain untruncated", out.Body.Source)
	}
	if len(out.Body.Source.Reasons) != 1 || out.Body.Source.Reasons[0] != "graph_instantiating" {
		t.Fatalf("partial reasons = %q, want [graph_instantiating]", out.Body.Source.Reasons)
	}
}

func TestRunDetailWireRoute(t *testing.T) {
	root := runRootBead("run-wire-detail", "wire-formula", "open")
	root.Metadata["gc.root_store_ref"] = "city:test-city"
	step := runDetailBead("wire-step", root.ID, "wire-step", "Wire step", "open", nil)
	store := beads.NewMemStoreFrom(2, []beads.Bead{root, step}, nil)

	fs := newFakeState(t)
	fs.cityBeadStore = store
	fs.stores["myrig"] = store
	sm := newTestSupervisorMux(t, map[string]*fakeState{"test-city": fs})

	rec := httptest.NewRecorder()
	sm.ServeHTTP(rec, httptest.NewRequest(
		http.MethodGet,
		"/v0/city/test-city/runs/"+root.ID+"/detail",
		nil,
	))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET run detail = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var got RunDetail
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode run detail: %v; body=%s", err, rec.Body.String())
	}
	if got.RunID != root.ID || len(got.Nodes) != 2 {
		t.Fatalf("wire detail = run %q, %d nodes; want %q, 2 nodes", got.RunID, len(got.Nodes), root.ID)
	}
	if strings.Contains(rec.Body.String(), `"streamable"`) {
		t.Fatalf("wire detail claims live session availability without reading the session plane: %s", rec.Body.String())
	}

	for _, tc := range []struct {
		name     string
		runID    string
		wantCode int
		wantType string
	}{
		{name: "missing", runID: "missing-run", wantCode: http.StatusNotFound, wantType: "run-not-found"},
		{name: "non graph v2", runID: "legacy-run", wantCode: http.StatusUnprocessableEntity, wantType: "run-detail-unavailable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.runID == "legacy-run" {
				legacy := runRootBead(tc.runID, "legacy-formula", "open")
				delete(legacy.Metadata, "gc.formula_contract")
				legacyStore := beads.NewMemStoreFrom(1, []beads.Bead{legacy}, nil)
				fs.cityBeadStore = legacyStore
				fs.stores["myrig"] = legacyStore
			}
			rec := httptest.NewRecorder()
			sm.ServeHTTP(rec, httptest.NewRequest(
				http.MethodGet,
				"/v0/city/test-city/runs/"+tc.runID+"/detail",
				nil,
			))
			if rec.Code != tc.wantCode {
				t.Fatalf("status = %d, want %d; body=%s", rec.Code, tc.wantCode, rec.Body.String())
			}
			var problem struct {
				Code string `json:"code"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &problem); err != nil {
				t.Fatalf("decode problem: %v; body=%s", err, rec.Body.String())
			}
			if problem.Code != tc.wantType {
				t.Fatalf("problem code = %q, want %q; body=%s", problem.Code, tc.wantType, rec.Body.String())
			}
		})
	}
}

func TestRunDetailGraphReadFailureIsSanitized503(t *testing.T) {
	root := runRootBead("run-read-failure", "broken-formula", "open")
	backing := beads.NewMemStoreFrom(1, []beads.Bead{root}, nil)
	store := runDetailListFailStore{
		Store: backing,
		err:   errors.New("read /private/city/beads: permission denied"),
	}
	fs := newFakeState(t)
	fs.cityBeadStore = store
	fs.stores["myrig"] = store

	_, err := (&Server{state: fs}).humaHandleRunDetail(context.Background(), &RunDetailInput{
		CityScope: CityScope{CityName: "test-city"},
		RunID:     root.ID,
	})
	if err == nil {
		t.Fatal("humaHandleRunDetail error = nil, want 503")
	}
	var statusErr huma.StatusError
	if !errors.As(err, &statusErr) || statusErr.GetStatus() != http.StatusServiceUnavailable {
		t.Fatalf("error = %T %v, want Huma 503", err, err)
	}
	if strings.Contains(err.Error(), "/private/") || strings.Contains(err.Error(), "permission denied") {
		t.Fatalf("public error leaked store cause: %q", err)
	}
}

func TestRunDetailMalformedProjectionIsSanitized500(t *testing.T) {
	root := runRootBead("run-invalid-snapshot", "broken-formula", "open")
	root.Metadata[beadmeta.ScopeKindMetadataKey] = "rig"
	root.Metadata[beadmeta.ScopeRefMetadataKey] = "demo"
	delete(root.Metadata, beadmeta.RootStoreRefMetadataKey)

	fs := newFakeState(t)
	store := beads.NewMemStoreFrom(1, []beads.Bead{root}, nil)
	fs.cityBeadStore = store
	fs.stores["myrig"] = store

	_, err := (&Server{state: fs}).humaHandleRunDetail(context.Background(), &RunDetailInput{
		CityScope: CityScope{CityName: "test-city"},
		RunID:     root.ID,
	})
	if err == nil {
		t.Fatal("humaHandleRunDetail error = nil, want 500")
	}
	var statusErr huma.StatusError
	if !errors.As(err, &statusErr) || statusErr.GetStatus() != http.StatusInternalServerError {
		t.Fatalf("error = %T %v, want Huma 500", err, err)
	}
	if strings.Contains(err.Error(), "snapshot identity") || strings.Contains(err.Error(), "invalid") {
		t.Fatalf("public error leaked projection cause: %q", err)
	}
}

func TestRunDetailDoesNotPromoteTitleFallbackToFormulaIdentity(t *testing.T) {
	root := runRootBead("run-no-formula-identity", "placeholder", "open")
	delete(root.Metadata, beadmeta.FormulaMetadataKey)
	root.Metadata[beadmeta.RunTargetMetadataKey] = "worker"
	root.Metadata[beadmeta.RootStoreRefMetadataKey] = "city:test-city"

	fs := newFakeState(t)
	store := beads.NewMemStoreFrom(1, []beads.Bead{root}, nil)
	fs.cityBeadStore = store
	fs.stores["myrig"] = store

	out, err := (&Server{state: fs}).humaHandleRunDetail(context.Background(), &RunDetailInput{
		CityScope: CityScope{CityName: "test-city"},
		RunID:     root.ID,
	})
	if err != nil {
		t.Fatalf("humaHandleRunDetail: %v", err)
	}
	if out.Body.Formula.Name != "" {
		t.Fatalf("formula name = %q, want empty without recorded formula metadata", out.Body.Formula.Name)
	}
	if out.Body.Title != root.Title {
		t.Fatalf("run title = %q, want %q", out.Body.Title, root.Title)
	}
}

func TestHydrateRunDetailDependenciesReplacesEmbeddedDependencyFields(t *testing.T) {
	graphBeads := []beads.Bead{
		{ID: "prepare"},
		{
			ID:           "review",
			Needs:        []string{"prepare"},
			Dependencies: []beads.Dep{{IssueID: "review", DependsOnID: "prepare", Type: "dependency"}},
		},
	}

	got := hydrateRunDetailDependencies(graphBeads, []workflowDepResponse{{
		From: "prepare",
		To:   "review",
		Kind: "blocks",
	}})

	if len(got[1].Needs) != 0 {
		t.Fatalf("hydrated legacy needs = %v, want authoritative dependencies only", got[1].Needs)
	}
	if len(got[1].Dependencies) != 1 || got[1].Dependencies[0].Type != "blocks" {
		t.Fatalf("hydrated dependencies = %+v, want one authoritative blocks edge", got[1].Dependencies)
	}
}

type runDetailListFailStore struct {
	beads.Store
	err error
}

func (s runDetailListFailStore) List(beads.ListQuery) ([]beads.Bead, error) {
	return nil, s.err
}

func runDetailBead(id, rootID, stepID, title, status string, extra map[string]string) beads.Bead {
	metadata := map[string]string{
		"gc.kind":         "step",
		"gc.root_bead_id": rootID,
		"gc.step_id":      stepID,
		"gc.step_ref":     "branching-formula." + stepID,
	}
	for key, value := range extra {
		metadata[key] = value
	}
	return beads.Bead{
		ID:       id,
		Title:    title,
		Status:   status,
		Type:     "task",
		ParentID: rootID,
		Metadata: metadata,
	}
}

func executionByID(t *testing.T, executions []RunDetailExecution, id string) RunDetailExecution {
	t.Helper()
	for _, execution := range executions {
		if execution.PhysicalID == id {
			return execution
		}
	}
	t.Fatalf("execution %q missing from %+v", id, executions)
	return RunDetailExecution{}
}

func assertRunDetailEdge(t *testing.T, edges []RunDetailEdge, from, to string, kind RunDetailEdgeKind, sourceKind string) {
	t.Helper()
	for _, edge := range edges {
		if edge.From != from || edge.To != to {
			continue
		}
		if edge.Kind != kind || edge.SourceKind != sourceKind {
			t.Fatalf("edge %s -> %s = %s/%q, want %s/%q", from, to, edge.Kind, edge.SourceKind, kind, sourceKind)
		}
		return
	}
	t.Fatalf("edge %s -> %s (%s/%q) missing from %+v", from, to, kind, sourceKind, edges)
}
