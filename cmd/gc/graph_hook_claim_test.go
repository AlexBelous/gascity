package main

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/gastownhall/gascity/internal/beadmeta"
	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/events"
)

// TestGraphRoutedHookClaimOps pins the claim-time routing: on a routed city,
// claim/stamp/continuation mutations on gcg ids land in the embedded graph
// store in-process; a second claimant loses cleanly (CAS), and continuations
// under a gcg root list from the graph store.
func TestGraphRoutedHookClaimOps(t *testing.T) {
	cityPath := t.TempDir()
	writeGraphMigratedMarker(t, cityPath)
	cfg := sqliteGraphConfig()
	st, err := graphClassStoreFor(cityPath)
	if err != nil {
		t.Fatal(err)
	}
	step, err := st.Create(beads.Bead{Title: "step", Type: "task"})
	if err != nil {
		t.Fatal(err)
	}

	ops := graphRoutedHookClaimOps(cityPath, cfg)
	ctx := context.Background()

	claimed, ok, err := ops.Claim(ctx, t.TempDir(), nil, step.ID, "worker-1")
	if err != nil || !ok || claimed.Assignee != "worker-1" {
		t.Fatalf("claim = (%+v, %v, %v)", claimed, ok, err)
	}
	if _, ok, err := ops.Claim(ctx, t.TempDir(), nil, step.ID, "worker-2"); err != nil || ok {
		t.Fatalf("second claim = (%v, %v), want lost-not-error", ok, err)
	}

	if err := ops.StampWorkMeta(ctx, t.TempDir(), nil, step.ID, "worker-1", map[string]string{"gc.work_branch": "b1"}); err != nil {
		t.Fatalf("stamp: %v", err)
	}
	got, err := st.Get(step.ID)
	if err != nil || got.Metadata["gc.work_branch"] != "b1" {
		t.Fatalf("stamp did not land: %+v %v", got, err)
	}

	// Continuations under a gcg root list from the graph store.
	root, err := st.Create(beads.Bead{Title: "root", Type: "molecule"})
	if err != nil {
		t.Fatal(err)
	}
	child, err := st.Create(beads.Bead{Title: "cont", Type: "task", ParentID: root.ID, Labels: []string{"grp"}})
	if err != nil {
		t.Fatal(err)
	}
	conts, err := ops.ListContinuation(ctx, t.TempDir(), nil, root.ID, "grp")
	if err != nil || len(conts) != 1 || conts[0].ID != child.ID {
		t.Fatalf("continuations = (%+v, %v)", conts, err)
	}
	if err := ops.AssignContinuation(ctx, t.TempDir(), nil, child.ID, "worker-1"); err != nil {
		t.Fatalf("assign continuation: %v", err)
	}
	if got, _ := st.Get(child.ID); got.Assignee != "worker-1" {
		t.Fatalf("assign did not land: %+v", got)
	}
}

// TestGraphRoutedHookClaimPromotesAndEmitsFromGraphStore guards the complete
// preassigned hook path. The graph store owns the claim, identity readback,
// workflow-root validation, and lifecycle emission; no bd/work-store fallback
// may be used for a gcg step.
func TestGraphRoutedHookClaimPromotesAndEmitsFromGraphStore(t *testing.T) {
	cityPath := t.TempDir()
	writeGraphMigratedMarker(t, cityPath)
	cfg := sqliteGraphConfig()
	st, err := graphClassStoreFor(cityPath)
	if err != nil {
		t.Fatal(err)
	}
	root, err := st.Create(beads.Bead{Title: "report run", Type: "task", Metadata: map[string]string{
		beadmeta.KindMetadataKey:            beadmeta.KindWorkflow,
		beadmeta.FormulaContractMetadataKey: beadmeta.FormulaContractGraphV2,
	}})
	if err != nil {
		t.Fatal(err)
	}
	step, err := st.Create(beads.Bead{
		Title:    "load context",
		Type:     "task",
		Status:   "open",
		Assignee: "seth__seth",
		Metadata: map[string]string{
			beadmeta.RootBeadIDMetadataKey:             root.ID,
			beadmeta.StepIDMetadataKey:                 "report.load-context",
			beadmeta.NativeStepDependenciesMetadataKey: `["report.prepare"]`,
			beadmeta.RoutedToMetadataKey:               "seth.seth",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	candidates, err := json.Marshal([]beads.Bead{step})
	if err != nil {
		t.Fatal(err)
	}

	ops := graphRoutedHookClaimOps(cityPath, cfg)
	ops.Runner = func(string, string) (string, error) { return string(candidates), nil }
	ops.ResolveWorkBranch = func(string) string { return "" }
	ops.PublishRunMap = noopPublishRunMap
	opts := hookClaimOptions{
		Assignee:           "seth__seth",
		IdentityCandidates: []string{"seth__seth"},
		RouteTargets:       []string{"seth.seth"},
		Env: []string{
			"GC_SESSION_ID=mc-wisp-proof",
			"GC_SESSION_NAME=seth__seth",
		},
		JSON: true,
	}

	var stdout, stderr bytes.Buffer
	if code := doHookClaim("work-query", cityPath, opts, ops, &stdout, &stderr); code != 0 {
		t.Fatalf("doHookClaim = %d, want 0; stderr=%s", code, stderr.String())
	}
	got, err := st.Get(step.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "in_progress" || got.Metadata[beadmeta.SessionIDMetadataKey] != "mc-wisp-proof" {
		t.Fatalf("claimed step = %#v, want in_progress with exact gc.session_id", got)
	}

	started, err := events.ReadFiltered(
		filepath.Join(cityPath, ".gc", "events.jsonl"),
		events.Filter{Type: events.ExecutionStepStarted, Subject: step.ID},
	)
	if err != nil {
		t.Fatal(err)
	}
	wantDeps := &[]string{"report.prepare"}
	if len(started) != 1 {
		t.Fatalf("execution.step_started events = %#v, want one", started)
	}
	event := started[0]
	if event.RunID != root.ID || event.SessionID != "mc-wisp-proof" ||
		event.StepID != "report.load-context" || !reflect.DeepEqual(event.DependsOnStepIDs, wantDeps) {
		t.Fatalf("execution.step_started = %#v", event)
	}
}
