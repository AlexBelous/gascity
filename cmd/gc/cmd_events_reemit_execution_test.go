package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"testing"

	"github.com/gastownhall/gascity/internal/beadmeta"
	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/config"
	"github.com/gastownhall/gascity/internal/events"
)

func TestEventsReemitExecutionDryRunAndApply(t *testing.T) {
	city := writeOddballMinimalCity(t, "reemit-city")
	store, runID := reemitGraphStore(t)
	restore := setExecutionReemitTestSeams(t, store)
	defer restore()

	var opened int
	openExecutionReemitRecorder = func(string, config.EventsConfig, io.Writer) (executionReemitRecorder, error) {
		opened++
		return nil, errors.New("dry-run must not open recorder")
	}
	var stdout, stderr bytes.Buffer
	if code := run([]string{"--city", city, "events", "reemit-execution", "--run", runID}, &stdout, &stderr); code != 0 {
		t.Fatalf("dry-run code=%d stderr=%s", code, stderr.String())
	}
	var summary executionReemitSummary
	if err := json.Unmarshal(stdout.Bytes(), &summary); err != nil {
		t.Fatal(err)
	}
	if summary.City != city || summary.Run != runID || summary.Apply || summary.EventCount != 2 || summary.WorkCount != 1 || summary.StepCount != 1 || opened != 0 {
		t.Fatalf("dry-run summary=%+v opened=%d", summary, opened)
	}

	recorder := &reemitRecorder{}
	openExecutionReemitRecorder = func(string, config.EventsConfig, io.Writer) (executionReemitRecorder, error) { return recorder, nil }
	stdout.Reset()
	if code := run([]string{"--city", city, "events", "reemit-execution", "--run", runID, "--apply"}, &stdout, &stderr); code != 0 {
		t.Fatalf("apply code=%d stderr=%s", code, stderr.String())
	}
	if len(recorder.events) != 2 || recorder.events[0].Type != events.ExecutionWorkAssociated || recorder.events[1].Type != events.ExecutionStepDefined || recorder.closes != 1 {
		t.Fatalf("apply events=%#v closes=%d", recorder.events, recorder.closes)
	}
	if code := run([]string{"--city", city, "events", "reemit-execution", "--run", runID, "--apply"}, io.Discard, &stderr); code != 0 {
		t.Fatalf("second apply code=%d stderr=%s", code, stderr.String())
	}
	if len(recorder.events) != 4 || recorder.closes != 2 {
		t.Fatalf("repeated apply events=%d closes=%d", len(recorder.events), recorder.closes)
	}
}

func TestEventsReemitExecutionRejectsMissingOrRemoteScopeAndCloseError(t *testing.T) {
	city := writeOddballMinimalCity(t, "reemit-city")
	store, runID := reemitGraphStore(t)
	restore := setExecutionReemitTestSeams(t, store)
	defer restore()
	for _, args := range [][]string{
		{"events", "reemit-execution", "--run", runID},
		{"--city", city, "--rig", "x", "events", "reemit-execution", "--run", runID},
		{"--city", city, "--context", "remote", "events", "reemit-execution", "--run", runID},
	} {
		if code := run(args, io.Discard, io.Discard); code == 0 {
			t.Fatalf("args %v unexpectedly succeeded", args)
		}
	}
	badClose := &reemitRecorder{closeErr: errors.New("close failed")}
	openExecutionReemitRecorder = func(string, config.EventsConfig, io.Writer) (executionReemitRecorder, error) { return badClose, nil }
	if code := run([]string{"--city", city, "events", "reemit-execution", "--run", runID, "--apply"}, io.Discard, io.Discard); code == 0 || badClose.closes != 1 || len(badClose.events) != 2 {
		t.Fatalf("close failure code=%d closes=%d events=%d", code, badClose.closes, len(badClose.events))
	}
}

func TestEventsReemitExecutionUsesRoutedRootAndKeepsProjectionFailuresWriteFree(t *testing.T) {
	city := writeOddballMinimalCity(t, "reemit-city")
	work, _ := reemitGraphStore(t)
	graph, graphRun := reemitGraphStore(t)
	restore := setExecutionReemitTestSeams(t, work)
	defer restore()
	executionReemitGraphStore = func(string, *config.City) (beads.Store, bool, error) { return graph, true, nil }
	recorder := &reemitRecorder{}
	openExecutionReemitRecorder = func(string, config.EventsConfig, io.Writer) (executionReemitRecorder, error) { return recorder, nil }
	if code := run([]string{"--city", city, "events", "reemit-execution", "--run", graphRun, "--apply"}, io.Discard, io.Discard); code != 0 {
		t.Fatalf("routed root code=%d", code)
	}
	if len(recorder.events) != 2 {
		t.Fatalf("routed root events=%d, want 2", len(recorder.events))
	}

	bad := beads.NewMemStore()
	badRoot, _ := bad.Create(beads.Bead{})
	executionReemitOpenStore = func(string, string) (beads.Store, error) { return bad, nil }
	executionReemitGraphStore = func(string, *config.City) (beads.Store, bool, error) { return nil, false, nil }
	recorder.events = nil
	recorder.closes = 0
	if code := run([]string{"--city", city, "events", "reemit-execution", "--run", badRoot.ID, "--apply"}, io.Discard, io.Discard); code == 0 {
		t.Fatal("invalid graph root unexpectedly succeeded")
	}
	if len(recorder.events) != 0 || recorder.closes != 0 {
		t.Fatalf("projection failure wrote events=%d closes=%d", len(recorder.events), recorder.closes)
	}
}

func TestResolveExecutionReemitStoresFailsWhenAnyMemberStoreCannotOpen(t *testing.T) {
	city := t.TempDir()
	previous := executionReemitOpenStore
	t.Cleanup(func() { executionReemitOpenStore = previous })
	executionReemitOpenStore = func(string, string) (beads.Store, error) { return nil, errors.New("unavailable") }
	if _, _, err := resolveExecutionReemitStores(city, &config.City{}, "gcg-root"); err == nil {
		t.Fatal("member-store open failure was accepted")
	}
}

func reemitGraphStore(t *testing.T) (*beads.MemStore, string) {
	t.Helper()
	store := beads.NewMemStore()
	convoy, _ := store.Create(beads.Bead{Type: "convoy"})
	work, _ := store.Create(beads.Bead{Type: "task"})
	if err := store.DepAdd(convoy.ID, work.ID, "tracks"); err != nil {
		t.Fatal(err)
	}
	root, err := store.Create(beads.Bead{Metadata: map[string]string{beadmeta.KindMetadataKey: beadmeta.KindWorkflow, beadmeta.FormulaContractMetadataKey: beadmeta.FormulaContractGraphV2, beadmeta.InputConvoyIDMetadataKey: convoy.ID}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Create(beads.Bead{Metadata: map[string]string{beadmeta.RootBeadIDMetadataKey: root.ID, beadmeta.StepIDMetadataKey: "step"}}); err != nil {
		t.Fatal(err)
	}
	return store, root.ID
}

func setExecutionReemitTestSeams(t *testing.T, store beads.Store) func() {
	t.Helper()
	oldOpen, oldGraph, oldRecorder := executionReemitOpenStore, executionReemitGraphStore, openExecutionReemitRecorder
	executionReemitOpenStore = func(string, string) (beads.Store, error) { return store, nil }
	executionReemitGraphStore = func(string, *config.City) (beads.Store, bool, error) { return nil, false, nil }
	return func() {
		executionReemitOpenStore, executionReemitGraphStore, openExecutionReemitRecorder = oldOpen, oldGraph, oldRecorder
	}
}

type reemitRecorder struct {
	events   []events.Event
	closes   int
	closeErr error
}

func (r *reemitRecorder) Record(event events.Event) { r.events = append(r.events, event) }
func (r *reemitRecorder) Close() error              { r.closes++; return r.closeErr }
