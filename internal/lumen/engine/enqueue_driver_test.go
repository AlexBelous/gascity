package engine_test

import (
	"context"
	"testing"

	"github.com/gastownhall/gascity/internal/lumen/engine"
)

func TestEnqueueNormalizesRunDriver(t *testing.T) {
	ctx := context.Background()
	store := newStore(t)

	doc := decodeIR(t, bundleDoc("", execNodeExit("work", "true", []int{0}, nil), ""))
	streamID, err := engine.EnqueueRunWithDriver(ctx, store, doc, nil, "formula", "", "")
	if err != nil {
		t.Fatalf("EnqueueRunWithDriver: %v", err)
	}
	manifest, err := engine.ReadRunManifest(ctx, store, streamID)
	if err != nil {
		t.Fatalf("ReadRunManifest: %v", err)
	}
	if manifest.Driver != engine.DriverController {
		t.Fatalf("manifest driver = %q, want %q", manifest.Driver, engine.DriverController)
	}
}

func TestEnqueueRejectsUnknownRunDriverBeforeStarting(t *testing.T) {
	ctx := context.Background()
	store := newStore(t)

	doc := decodeIR(t, bundleDoc("", execNodeExit("work", "true", []int{0}, nil), ""))
	if _, err := engine.EnqueueRunWithDriver(ctx, store, doc, nil, "formula", "", "mystery"); err == nil {
		t.Fatal("EnqueueRunWithDriver accepted an unknown driver")
	}
	runs, err := engine.ListOpenRuns(ctx, store)
	if err != nil {
		t.Fatalf("ListOpenRuns: %v", err)
	}
	if len(runs) != 0 {
		t.Fatalf("open runs = %#v, want none", runs)
	}
}
