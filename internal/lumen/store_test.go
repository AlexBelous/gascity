package lumen

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/gastownhall/gascity/internal/lumen/engine"
	"github.com/gastownhall/gascity/internal/lumen/ir"
)

func loadTestIR(t *testing.T) *ir.IR {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "examples", "lumen", "hello.lumen.json"))
	if err != nil {
		t.Fatalf("read test IR: %v", err)
	}
	doc, err := ir.Decode(raw)
	if err != nil {
		t.Fatalf("decode test IR: %v", err)
	}
	return doc
}

func TestOpenOwnsDedicatedLumenState(t *testing.T) {
	cityPath := t.TempDir()

	store, err := Open(context.Background(), cityPath)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	if _, err := os.Stat(filepath.Join(cityPath, ".gc", "lumen", "journal.sqlite")); err != nil {
		t.Fatalf("stat Lumen journal: %v", err)
	}
	if _, err := os.Stat(filepath.Join(cityPath, ".gc", "store", "graph", "beads.sqlite")); !os.IsNotExist(err) {
		t.Fatalf("graph bead store exists or stat failed with %v; Lumen must not share it", err)
	}
}

func TestEnqueuePersistsInputsForManifestLoad(t *testing.T) {
	ctx := context.Background()
	cityPath := t.TempDir()
	store, err := Open(ctx, cityPath)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	doc := loadTestIR(t)
	input := map[string]any{"document": "design.md"}
	streamID, err := store.Enqueue(ctx, doc, input, "hello.lumen", "", engine.DriverController)
	if err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	manifest, err := engine.ReadRunManifest(ctx, store.Journal(), streamID)
	if err != nil {
		t.Fatalf("ReadRunManifest: %v", err)
	}
	loadedDoc, loadedInput, err := store.Load(manifest)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loadedDoc.Name != doc.Name {
		t.Fatalf("loaded formula name = %q, want %q", loadedDoc.Name, doc.Name)
	}
	if got := loadedInput["document"]; got != "design.md" {
		t.Fatalf("loaded input document = %#v, want design.md", got)
	}
}

func TestEnqueueDoesNotStartRunWhenCASWriteFails(t *testing.T) {
	ctx := context.Background()
	cityPath := t.TempDir()
	store, err := Open(ctx, cityPath)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	casPath := filepath.Join(cityPath, ".gc", "lumen", "cas")
	if err := os.WriteFile(casPath, []byte("blocks directory creation"), 0o600); err != nil {
		t.Fatalf("write CAS blocker: %v", err)
	}
	if _, err := store.Enqueue(ctx, loadTestIR(t), nil, "hello.lumen", "", engine.DriverController); err == nil {
		t.Fatal("Enqueue succeeded with an unwritable CAS")
	}
	runs, err := engine.ListOpenRuns(ctx, store.Journal())
	if err != nil {
		t.Fatalf("ListOpenRuns: %v", err)
	}
	if len(runs) != 0 {
		t.Fatalf("open runs = %#v, want none after CAS failure", runs)
	}
}
