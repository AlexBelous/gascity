package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/gastownhall/gascity/internal/beadmeta"
	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/config"
	lumenstore "github.com/gastownhall/gascity/internal/lumen"
	"github.com/gastownhall/gascity/internal/lumen/engine"
	"github.com/gastownhall/gascity/internal/lumen/ir"
)

func loadLumenRuntimeTestIR(t *testing.T, name string) *ir.IR {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "examples", "lumen", name))
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	doc, err := ir.Decode(raw)
	if err != nil {
		t.Fatalf("decode %s: %v", name, err)
	}
	return doc
}

func TestLumenDispatchCreatesIdempotentOrdinaryWorkBead(t *testing.T) {
	store := beads.NewMemStore()
	cfg := &config.City{Agents: []config.Agent{{Name: "workers"}}}
	dispatch := lumenDispatchWork(store, cfg)
	work := engine.WorkDispatch{
		StreamID:   "run-1",
		Activation: "review:0",
		NodeID:     "review",
		Route:      "workers",
		Prompt:     "Review the document.",
		Attempt:    0,
		Metadata:   map[string]string{"custom": "value"},
	}

	first, err := dispatch(context.Background(), work)
	if err != nil {
		t.Fatalf("first dispatch: %v", err)
	}
	second, err := dispatch(context.Background(), work)
	if err != nil {
		t.Fatalf("second dispatch: %v", err)
	}
	if second != first {
		t.Fatalf("second bead = %q, want idempotent reuse of %q", second, first)
	}
	bead, err := store.Get(first)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if bead.Type != "task" || bead.Description != work.Prompt {
		t.Fatalf("bead = %#v, want an ordinary task carrying the prompt", bead)
	}
	if bead.Metadata[beadmeta.LumenRunMetadataKey] != work.StreamID ||
		bead.Metadata[beadmeta.LumenActivationMetadataKey] != work.Activation ||
		bead.Metadata[beadmeta.RoutedToMetadataKey] != work.Route ||
		bead.Metadata["custom"] != "value" {
		t.Fatalf("bead metadata = %#v", bead.Metadata)
	}
}

func TestLumenRunsTickDispatchesObservesAndSnapshots(t *testing.T) {
	ctx := context.Background()
	cityPath := t.TempDir()
	store, err := lumenstore.Open(ctx, cityPath)
	if err != nil {
		t.Fatalf("lumen.Open: %v", err)
	}
	doc := loadLumenRuntimeTestIR(t, "hello-do.lumen.json")
	streamID, err := store.Enqueue(ctx, doc, nil, "hello-do.lumen", "workers", engine.DriverController)
	if err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close enqueue store: %v", err)
	}

	enabled := true
	workStore := beads.NewMemStore()
	var stderr bytes.Buffer
	runtime := &CityRuntime{
		cityPath:            cityPath,
		cfg:                 &config.City{Daemon: config.DaemonConfig{LumenBeta: &enabled}, Agents: []config.Agent{{Name: "workers"}}},
		standaloneCityStore: workStore,
		stderr:              &stderr,
		logPrefix:           "test",
	}
	t.Cleanup(runtime.closeLumenStore)

	runtime.lumenRunsTick(ctx)
	work, err := workStore.List(beads.ListQuery{AllowScan: true, IncludeClosed: true})
	if err != nil {
		t.Fatalf("list work: %v", err)
	}
	if len(work) != 1 {
		t.Fatalf("work beads = %#v, want one dispatched do", work)
	}
	if err := workStore.Update(work[0].ID, beads.UpdateOpts{Metadata: map[string]string{
		beadmeta.OutcomeMetadataKey:    "pass",
		beadmeta.OutputJSONMetadataKey: `{"reviewed":true}`,
	}}); err != nil {
		t.Fatalf("stamp outcome: %v", err)
	}
	if err := workStore.Close(work[0].ID); err != nil {
		t.Fatalf("close work: %v", err)
	}

	runtime.lumenRunsTick(ctx)
	view, err := engine.FoldRunView(ctx, runtime.lumen.store.Journal(), streamID)
	if err != nil {
		t.Fatalf("FoldRunView: %v", err)
	}
	if !view.Closed || !engine.IsSucceededOutcome(view.Outcome) {
		t.Fatalf("run view = %#v, want closed success", view)
	}
	if _, ok, err := runtime.lumen.store.Journal().LatestSnapshot(ctx, streamID); err != nil {
		t.Fatalf("LatestSnapshot: %v", err)
	} else if !ok {
		t.Fatal("production controller run wrote no durable snapshot")
	}
	if stderr.Len() != 0 {
		t.Fatalf("unexpected runtime errors: %s", stderr.String())
	}
}
