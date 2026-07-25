package engine_test

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"testing"

	"github.com/gastownhall/gascity/internal/graphstore"
	"github.com/gastownhall/gascity/internal/graphstore/canon"
	"github.com/gastownhall/gascity/internal/graphstore/fold"
	"github.com/gastownhall/gascity/internal/lumen/engine"
)

func TestFreshRunsStampCurrentSemanticDialect(t *testing.T) {
	doc := decodeIR(t, blockDoc("dialect",
		execNode("work", `echo done`, nil),
	))

	tests := []struct {
		name  string
		start func(context.Context, *graphstore.Store) (string, error)
	}{
		{
			name: "run",
			start: func(ctx context.Context, store *graphstore.Store) (string, error) {
				result, err := engine.Run(ctx, store, doc, nil)
				return result.StreamID, err
			},
		},
		{
			name: "advance",
			start: func(ctx context.Context, store *graphstore.Store) (string, error) {
				const streamID = "gcg-run-current-dialect"
				_, err := engine.Advance(ctx, store, doc, streamID, nil, engine.Options{})
				return streamID, err
			},
		},
		{
			name: "enqueue",
			start: func(ctx context.Context, store *graphstore.Store) (string, error) {
				return engine.EnqueueRun(ctx, store, doc, nil, "packs/test@v1", "workers")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			store := newStore(t)
			streamID, err := tt.start(ctx, store)
			if err != nil {
				t.Fatalf("start run: %v", err)
			}

			manifest, err := engine.ReadRunManifest(ctx, store, streamID)
			if err != nil {
				t.Fatalf("read manifest: %v", err)
			}
			if manifest.SemanticDialect != engine.SemanticDialectCurrent {
				t.Fatalf("manifest semantic dialect = %q, want %q", manifest.SemanticDialect, engine.SemanticDialectCurrent)
			}

			events, err := store.ReadStream(ctx, streamID, 1, 1)
			if err != nil {
				t.Fatalf("read run.started: %v", err)
			}
			var payload struct {
				Dialect string `json:"dialect"`
			}
			if err := json.Unmarshal(events[0].Payload, &payload); err != nil {
				t.Fatalf("decode run.started: %v", err)
			}
			if payload.Dialect != engine.SemanticDialectCurrent {
				t.Fatalf("run.started dialect = %q, want %q", payload.Dialect, engine.SemanticDialectCurrent)
			}
		})
	}
}

func TestPreDialectJournalFoldsAsLegacy(t *testing.T) {
	payload, err := canonJSON(t, map[string]any{
		"root_id":    "gcg-run-legacy-dialect",
		"name":       "legacy",
		"created_at": "2026-01-01T00:00:00Z",
	})
	if err != nil {
		t.Fatal(err)
	}
	state, _, err := fold.Fold(engine.Reducer(), nil, []fold.Event{{
		StreamID:          "gcg-run-legacy-dialect",
		Seq:               1,
		Engine:            engine.Engine,
		Type:              engine.EventRunStarted,
		IRContractVersion: "0.2.5",
		Payload:           payload,
	}})
	if err != nil {
		t.Fatalf("fold pre-dialect journal: %v", err)
	}
	blob, err := state.MarshalSnapshot()
	if err != nil {
		t.Fatalf("marshal folded state: %v", err)
	}
	var snapshot struct {
		SemanticDialect string `json:"dialect"`
	}
	if err := json.Unmarshal(blob, &snapshot); err != nil {
		t.Fatalf("decode folded state: %v", err)
	}
	if snapshot.SemanticDialect != engine.SemanticDialectLegacy {
		t.Fatalf("pre-dialect journal folded as %q, want %q", snapshot.SemanticDialect, engine.SemanticDialectLegacy)
	}
}

func TestResumeMigratesV4SnapshotAsLegacy(t *testing.T) {
	ctx := context.Background()
	store := newStore(t)
	doc := decodeIR(t, blockDoc("legacy-snapshot",
		execNode("work", `echo resumed`, nil),
	))
	const streamID = "gcg-run-v4-snapshot"

	seedHistoricalSnapshot(t, store, streamID, 4, 4)

	result, err := engine.Resume(ctx, store, doc, streamID, nil, engine.Options{})
	if err != nil {
		t.Fatalf("resume v4 snapshot: %v", err)
	}
	if result.Outcome != engine.OutcomePass {
		t.Fatalf("legacy resumed outcome = %q, want %q", result.Outcome, engine.OutcomePass)
	}
	if result.NodeOutputs["work"] != "resumed" {
		t.Fatalf("legacy resumed output = %q, want resumed", result.NodeOutputs["work"])
	}
	manifest, err := engine.ReadRunManifest(ctx, store, streamID)
	if err != nil {
		t.Fatalf("read legacy manifest: %v", err)
	}
	if manifest.SemanticDialect != engine.SemanticDialectLegacy {
		t.Fatalf("legacy manifest semantic dialect = %q, want %q", manifest.SemanticDialect, engine.SemanticDialectLegacy)
	}
}

func TestResumeRejectsSnapshotOlderThanV4(t *testing.T) {
	ctx := context.Background()
	store := newStore(t)
	doc := decodeIR(t, blockDoc("stale-snapshot",
		execNode("work", `echo must-not-run`, nil),
	))
	const streamID = "gcg-run-v3-snapshot"

	seedHistoricalSnapshot(t, store, streamID, 3, 3)

	_, err := engine.Resume(ctx, store, doc, streamID, nil, engine.Options{})
	if !errors.Is(err, fold.ErrReducerVersionSkew) {
		t.Fatalf("resume v3 snapshot = %v, want ErrReducerVersionSkew", err)
	}
}

func seedHistoricalSnapshot(t *testing.T, store *graphstore.Store, streamID string, reducerVersion, formatVersion int) {
	t.Helper()
	ctx := context.Background()
	engine.RegisterVocabulary(store)
	run := newManualRun(t, store, streamID)
	run.append(engine.EventRunStarted, streamID+":run:started", map[string]any{
		"root_id":    streamID,
		"name":       "historical",
		"created_at": "2026-01-01T00:00:00Z",
	})

	// A historical snapshot presupposes that its covered projection was already
	// committed. Rebuild it from the one-event journal before installing the
	// fixture snapshot so resumed node edges retain their root.
	if err := store.RebuildTierA(ctx, engine.Reducer(), streamID); err != nil {
		t.Fatalf("project historical prefix: %v", err)
	}

	state, err := canonJSON(t, map[string]any{
		"root_id":    streamID,
		"name":       "historical",
		"created_at": "2026-01-01T00:00:00Z",
		"closed":     false,
	})
	if err != nil {
		t.Fatalf("canonicalize historical state: %v", err)
	}
	stateHash := canon.Hash(state)
	anchorPayload, err := canonJSON(t, map[string]any{
		"covered_seq": 1,
		"state_hash":  hex.EncodeToString(stateHash[:]),
	})
	if err != nil {
		t.Fatalf("canonicalize anchor: %v", err)
	}
	_, err = store.WriteSnapshot(ctx, engine.Engine, run.lease.Epoch, fold.Snapshot{
		StreamID:              streamID,
		CoveredSeq:            1,
		Engine:                engine.Engine,
		ReducerVersion:        reducerVersion,
		SnapshotFormatVersion: formatVersion,
		StateHash:             stateHash,
		State:                 state,
	}, graphstore.JournalEvent{
		Type:              engine.EventSnapshotAnchored,
		IRContractVersion: "0.2.5",
		IdemToken:         streamID + ":snap:1",
		Payload:           anchorPayload,
	})
	if err != nil {
		t.Fatalf("write historical snapshot: %v", err)
	}
	run.close()
}
