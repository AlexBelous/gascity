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
	"github.com/gastownhall/gascity/internal/lumen/ir"
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

func TestCurrentDialectUsesSucceededOutcomeVocabulary(t *testing.T) {
	doc := decodeIR(t, blockDoc("current-outcomes",
		execNode("work", `echo done`, nil),
	))

	result, err := engine.Run(context.Background(), newStore(t), doc, nil)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if result.Outcome != engine.OutcomeSucceeded {
		t.Fatalf("run outcome = %q, want %q", result.Outcome, engine.OutcomeSucceeded)
	}
	if got := settledIDs(t, result.Events); len(got) != 1 || got[0] != [2]string{"work", engine.OutcomeSucceeded} {
		t.Fatalf("settled outcomes = %v, want [[work %s]]", got, engine.OutcomeSucceeded)
	}
	if got := closedOutcome(t, result.Events); got != engine.OutcomeSucceeded {
		t.Fatalf("run.closed outcome = %q, want %q", got, engine.OutcomeSucceeded)
	}
}

func TestCurrentRepeatConditionObservesSucceeded(t *testing.T) {
	body := execNodeExit("draft", `echo done`, []int{0}, nil)
	cond := `{"kind":"operator","op":"==","operands":[` +
		`{"kind":"ref","name":"draft","field":"outcome"},` +
		`{"kind":"literal","value":"succeeded"}]}`
	doc := decodeIR(t, blockDoc("current-repeat-outcome", repeatNode(body, cond)))

	result, err := engine.Run(context.Background(), newStore(t), doc, nil)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if result.Outcome != engine.OutcomeSucceeded {
		t.Fatalf("run outcome = %q, want %q", result.Outcome, engine.OutcomeSucceeded)
	}
	if got := countAttemptMinted(result.Events); got != 1 {
		t.Fatalf("attempt.minted count = %d, want 1 (succeeded condition exits immediately)", got)
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

	seedHistoricalSnapshot(t, store, streamID, 4, 4, "")

	result, err := engine.Resume(ctx, store, doc, streamID, nil, engine.Options{})
	if err != nil {
		t.Fatalf("resume v4 snapshot: %v", err)
	}
	if result.Outcome != "pass" {
		t.Fatalf("legacy resumed outcome = %q, want pass", result.Outcome)
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

func TestResumeMigratesV5SnapshotRetainingDialect(t *testing.T) {
	ctx := context.Background()
	store := newStore(t)
	doc := decodeIR(t, blockDoc("current-snapshot",
		execNode("work", `echo resumed`, nil),
	))
	const streamID = "gcg-run-v5-snapshot"

	seedHistoricalSnapshot(t, store, streamID, 5, 5, engine.SemanticDialectCurrent)

	result, err := engine.Resume(ctx, store, doc, streamID, nil, engine.Options{})
	if err != nil {
		t.Fatalf("resume v5 snapshot: %v", err)
	}
	if result.Outcome != engine.OutcomeSucceeded {
		t.Fatalf("current resumed outcome = %q, want succeeded", result.Outcome)
	}
	manifest, err := engine.ReadRunManifest(ctx, store, streamID)
	if err != nil {
		t.Fatalf("read current manifest: %v", err)
	}
	if manifest.SemanticDialect != engine.SemanticDialectCurrent {
		t.Fatalf("v5 semantic dialect = %q, want %q", manifest.SemanticDialect, engine.SemanticDialectCurrent)
	}
}

func TestResumeV6SnapshotReconstructsPublicOutcomeFromIR(t *testing.T) {
	ctx := context.Background()
	store := newStore(t)
	doc := decodeIR(t, bundleDoc(
		"",
		execNode("old-failure", `printf legacy; exit 7`, nil)+","+
			runNodeJSON("review", nil, "reviewer", "", "")+","+
			`{
		  "kind": "settle",
		  "id": "author",
		  "name": "author",
		  "after": ["review"],
		  "origin": {"uri": "t", "line": 1, "col": 0},
		  "outcome": "succeeded",
		  "value": {"kind": "ref", "name": "review"},
		  "publicOutcome": true
		}`,
		subDoc("reviewer", "", `{
		  "kind": "settle",
		  "id": "draft",
		  "name": "draft",
		  "after": [],
		  "origin": {"uri": "t", "line": 1, "col": 0},
		  "outcome": "succeeded",
		  "value": {
		    "kind": "object",
		    "entries": [{
		      "key": "status",
		      "value": {"kind": "literal", "value": "approved"}
		    }]
		  }
		}`),
	))
	const streamID = "gcg-run-v6-public-outcome"
	seedV6PublicOutcomeSnapshot(t, store, streamID, doc)

	resumed, err := engine.Resume(ctx, store, doc, streamID, nil, engine.Options{})
	if err != nil {
		t.Fatalf("resume v6 public outcome snapshot: %v", err)
	}
	requireResultJSON(t, resumed.Result,
		`{"value":{"kind":"succeeded","result":{"status":"approved"}},"outcome":"succeeded","error":null}`)
}

func TestResumeV6SnapshotReconstructsTransparentWrapperResultFromIR(t *testing.T) {
	ctx := context.Background()
	store := newStore(t)
	doc := decodeIR(t, blockDoc("recover-v6-result",
		recoverNode(nil, `{
		  "kind": "settle",
		  "id": "guarded",
		  "name": "guarded",
		  "after": [],
		  "origin": {"uri": "t", "line": 1, "col": 0},
		  "outcome": "succeeded",
		  "value": {
		    "kind": "object",
		    "entries": [{
		      "key": "status",
		      "value": {"kind": "literal", "value": "approved"}
		    }]
		  }
		}`, settleNode("fallback", engine.OutcomeSucceeded), "error"),
	))
	const streamID = "gcg-run-v6-wrapper-result"
	seedV6TransparentWrapperSnapshot(t, store, streamID, doc)

	resumed, err := engine.Resume(ctx, store, doc, streamID, nil, engine.Options{})
	if err != nil {
		t.Fatalf("resume v6 transparent wrapper snapshot: %v", err)
	}
	requireResultJSON(t, resumed.Result,
		`{"value":{"status":{"value":"approved","outcome":"succeeded","error":null}},"outcome":"succeeded","error":null}`)
}

func TestResumeV6PartialSnapshotReconstructsSettledTransparentWrappersForPendingReference(t *testing.T) {
	tests := []struct {
		name      string
		wrapperID string
		wrapper   string
		nodes     []v6SettledNode
		want      string
	}{
		{
			name:      "guard",
			wrapperID: "guarded",
			wrapper: `{
			  "kind":"guard","id":"guarded","name":"guarded","after":[],
			  "origin":{"uri":"t","line":1,"col":0},
			  "cond":{"kind":"literal","value":true},
			  "then":` + execNode("guarded.then", `printf guard`, nil) + `
			}`,
			nodes: []v6SettledNode{
				{
					Activation: "guarded.then:0",
					NodeID:     "guarded.then",
					Kind:       string(ir.NodeExec),
					Parent:     "guarded:0",
					Outcome:    engine.OutcomeSucceeded,
					Output:     "guard",
				},
				{
					Activation: "guarded:0",
					NodeID:     "guarded",
					Kind:       string(ir.NodeGuard),
					Outcome:    engine.OutcomeSucceeded,
					Output:     "guard",
				},
			},
			want: `{"value":"guard","outcome":"succeeded","error":null}`,
		},
		{
			name:      "recover",
			wrapperID: "rec",
			wrapper: recoverNode(
				nil,
				settleNodeReason("attempt", engine.OutcomeFailed, "blocked"),
				`{
				  "kind":"settle","id":"fallback","name":"fallback","after":[],
				  "origin":{"uri":"t","line":1,"col":0},
				  "outcome":"succeeded",
				  "value":{"kind":"literal","value":true}
				}`,
				"error",
			),
			nodes: []v6SettledNode{
				{
					Activation: "attempt:0",
					NodeID:     "attempt",
					Kind:       string(ir.NodeSettle),
					Parent:     "rec:0",
					Outcome:    engine.OutcomeFailed,
					Detail:     "blocked",
				},
				{
					Activation: "fallback:0",
					NodeID:     "fallback",
					Kind:       string(ir.NodeSettle),
					Parent:     "rec:0",
					Outcome:    engine.OutcomeSucceeded,
					Output:     "true",
				},
				{
					Activation: "rec:0",
					NodeID:     "rec",
					Kind:       string(ir.NodeRecover),
					Outcome:    engine.OutcomeSucceeded,
					Output:     "true",
				},
			},
			want: `{"value":true,"outcome":"succeeded","error":null}`,
		},
		{
			name:      "cleanup",
			wrapperID: "clean",
			wrapper: cleanupNode(
				nil,
				`{
				  "kind":"settle","id":"guarded","name":"guarded","after":[],
				  "origin":{"uri":"t","line":1,"col":0},
				  "outcome":"succeeded",
				  "value":{"kind":"literal","value":7}
				}`,
				execNode("finalizer", `printf done`, nil),
			),
			nodes: []v6SettledNode{
				{
					Activation: "guarded:0",
					NodeID:     "guarded",
					Kind:       string(ir.NodeSettle),
					Parent:     "clean:0",
					Outcome:    engine.OutcomeSucceeded,
					Output:     "7",
				},
				{
					Activation: "finalizer:0",
					NodeID:     "finalizer",
					Kind:       string(ir.NodeExec),
					Parent:     "clean:0",
					Outcome:    engine.OutcomeSucceeded,
					Output:     "done",
				},
				{
					Activation: "clean:0",
					NodeID:     "clean",
					Kind:       string(ir.NodeCleanup),
					Outcome:    engine.OutcomeSucceeded,
					Output:     "7",
				},
			},
			want: `{"value":7,"outcome":"succeeded","error":null}`,
		},
		{
			name:      "timeout",
			wrapperID: "check",
			wrapper:   tnkTimeoutExec(nil, "5m", `printf timed`),
			nodes: []v6SettledNode{
				{
					Activation: "v:0",
					NodeID:     "v",
					Kind:       string(ir.NodeExec),
					Parent:     "check:0",
					Outcome:    engine.OutcomeSucceeded,
					Output:     "timed",
				},
				{
					Activation: "check:0",
					NodeID:     "check",
					Kind:       string(ir.NodeTimeout),
					Outcome:    engine.OutcomeSucceeded,
					Output:     "timed",
				},
			},
			want: `{"value":"timed","outcome":"succeeded","error":null}`,
		},
		{
			name:      "dispatch",
			wrapperID: "d",
			wrapper:   dispatchExec([2]string{"chosen", `printf dispatched`}),
			nodes: []v6SettledNode{
				{
					Activation: "d_arm0:0",
					NodeID:     "d_arm0",
					Kind:       string(ir.NodeExec),
					Parent:     "d:0",
					Outcome:    engine.OutcomeSucceeded,
					Output:     "dispatched",
				},
				{
					Activation: "d:0",
					NodeID:     "d",
					Kind:       string(ir.NodeDispatch),
					Outcome:    engine.OutcomeSucceeded,
					Output:     "dispatched",
				},
			},
			want: `{"value":"dispatched","outcome":"succeeded","error":null}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			store := newStore(t)
			doc := decodeIR(t, blockDoc(
				"partial-v6-"+tt.name,
				tt.wrapper,
				`{
				  "kind":"settle","id":"done","name":"done","after":["`+tt.wrapperID+`"],
				  "origin":{"uri":"t","line":2,"col":0},
				  "outcome":"succeeded",
				  "value":{"kind":"ref","name":"`+tt.wrapperID+`"}
				}`,
			))
			streamID := "gcg-run-v6-partial-" + tt.name
			seedV6SettledPrefixSnapshot(t, store, streamID, doc, tt.nodes)

			resumed, err := engine.Resume(ctx, store, doc, streamID, nil, engine.Options{})
			if err != nil {
				t.Fatalf("resume partial v6 %s snapshot: %v", tt.name, err)
			}
			requireResultJSON(t, resumed.Result, tt.want)
		})
	}
}

func TestResumeV6PartialSnapshotReconstructsSettledAuthoredValueForPendingWork(t *testing.T) {
	tests := []struct {
		name       string
		downstream string
		want       string
	}{
		{
			name: "referenced",
			downstream: `{
			  "kind": "settle",
			  "id": "done",
			  "name": "done",
			  "after": ["seed"],
			  "origin": {"uri": "t", "line": 2, "col": 0},
			  "outcome": "succeeded",
			  "value": {"kind": "ref", "name": "seed"}
			}`,
			want: `{"value":true,"outcome":"succeeded","error":null}`,
		},
		{
			name: "unreferenced",
			downstream: `{
			  "kind": "settle",
			  "id": "done",
			  "name": "done",
			  "after": ["seed"],
			  "origin": {"uri": "t", "line": 2, "col": 0},
			  "outcome": "succeeded",
			  "value": {"kind": "literal", "value": "continued"}
			}`,
			want: `{"value":"continued","outcome":"succeeded","error":null}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			store := newStore(t)
			doc := decodeIR(t, blockDoc("partial-v6-author",
				`{
				  "kind": "settle",
				  "id": "seed",
				  "name": "seed",
				  "after": [],
				  "origin": {"uri": "t", "line": 1, "col": 0},
				  "outcome": "succeeded",
				  "value": {"kind": "literal", "value": true}
				}`,
				tt.downstream,
			))
			streamID := "gcg-run-v6-partial-author-" + tt.name
			seedV6SettledPrefixSnapshot(t, store, streamID, doc, []v6SettledNode{
				{
					Activation: "seed:0",
					NodeID:     "seed",
					Kind:       string(ir.NodeSettle),
					Outcome:    engine.OutcomeSucceeded,
					Output:     "true",
				},
			})

			resumed, err := engine.Resume(ctx, store, doc, streamID, nil, engine.Options{})
			if err != nil {
				t.Fatalf("resume partial v6 snapshot: %v", err)
			}
			requireResultJSON(t, resumed.Result, tt.want)
		})
	}
}

func TestResumeV6PartialSnapshotReconstructsSettledRunForPendingReference(t *testing.T) {
	ctx := context.Background()
	store := newStore(t)
	doc := decodeIR(t, bundleDoc(
		"",
		runNodeJSON("review", nil, "reviewer", "", "")+`,{
		  "kind": "settle",
		  "id": "done",
		  "name": "done",
		  "after": ["review"],
		  "origin": {"uri": "t", "line": 2, "col": 0},
		  "outcome": "succeeded",
		  "value": {"kind": "ref", "name": "review"}
		}`,
		subDoc("reviewer", "", `{
		  "kind": "settle",
		  "id": "draft",
		  "name": "draft",
		  "after": [],
		  "origin": {"uri": "t", "line": 1, "col": 0},
		  "outcome": "succeeded",
		  "value": {"kind": "literal", "value": true}
		}`),
	))
	const streamID = "gcg-run-v6-partial-run"
	seedV6SettledPrefixSnapshot(t, store, streamID, doc, []v6SettledNode{
		{
			Activation: "review/draft:0",
			NodeID:     "review/draft",
			Kind:       string(ir.NodeSettle),
			Parent:     "review:0",
			Outcome:    engine.OutcomeSucceeded,
			Output:     "true",
		},
		{
			Activation: "review:0",
			NodeID:     "review",
			Kind:       string(ir.NodeRun),
			Outcome:    engine.OutcomeSucceeded,
			Output:     "true",
		},
	})

	resumed, err := engine.Resume(ctx, store, doc, streamID, nil, engine.Options{})
	if err != nil {
		t.Fatalf("resume partial v6 run snapshot: %v", err)
	}
	requireResultJSON(t, resumed.Result,
		`{"value":true,"outcome":"succeeded","error":null}`)
}

func TestResumeV6PartialSnapshotReconstructsSettledScatterForPendingReference(t *testing.T) {
	ctx := context.Background()
	store := newStore(t)
	doc := decodeIR(t, blockDoc(
		"partial-v6-scatter",
		scatterNode(
			"values",
			nil,
			"continue",
			`{
			  "kind": "lit", "id": "constant-id", "name": "constant", "after": [],
			  "origin": {"uri": "t", "line": 1, "col": 0},
			  "value": 7
			}`,
		),
		`{
		  "kind": "settle", "id": "done", "name": "done", "after": ["values"],
		  "origin": {"uri": "t", "line": 2, "col": 0},
		  "outcome": "succeeded",
		  "value": {
		    "kind": "member",
		    "base": {"kind": "ref", "name": "values"},
		    "name": "constant"
		  }
		}`,
	))
	const streamID = "gcg-run-v6-partial-scatter"
	seedV6SettledPrefixSnapshot(t, store, streamID, doc, []v6SettledNode{
		{
			Activation: "values:0",
			NodeID:     "values",
			Kind:       string(ir.NodeScatter),
			Outcome:    engine.OutcomeSucceeded,
			Output:     `{"constant":{"value":7,"outcome":"succeeded","error":null}}`,
		},
	})

	resumed, err := engine.Resume(ctx, store, doc, streamID, nil, engine.Options{})
	if err != nil {
		t.Fatalf("resume partial v6 scatter snapshot: %v", err)
	}
	requireResultJSON(t, resumed.Result,
		`{"value":7,"outcome":"succeeded","error":null}`)
}

func TestResumeV6PartialSnapshotReconstructsSettledGatherForPendingReference(t *testing.T) {
	ctx := context.Background()
	store := newStore(t)
	doc := decodeIR(t, blockDoc(
		"partial-v6-gather",
		scatterNode(
			"values",
			nil,
			"continue",
			`{
			  "kind": "lit", "id": "member", "name": "member", "after": [],
			  "origin": {"uri": "t", "line": 1, "col": 0},
			  "value": "input"
			}`,
		),
		gatherNode(
			"combined",
			"values",
			[]string{"values"},
			`{
			  "kind": "lit", "id": "answer", "name": "answer", "after": [],
			  "origin": {"uri": "t", "line": 2, "col": 0},
			  "value": 42
			}`,
		),
		`{
		  "kind": "settle", "id": "done", "name": "done", "after": ["combined"],
		  "origin": {"uri": "t", "line": 3, "col": 0},
		  "outcome": "succeeded",
		  "value": {"kind": "ref", "name": "combined"}
		}`,
	))
	const streamID = "gcg-run-v6-partial-gather"
	seedV6SettledPrefixSnapshot(t, store, streamID, doc, []v6SettledNode{
		{
			Activation: "values:0",
			NodeID:     "values",
			Kind:       string(ir.NodeScatter),
			Outcome:    engine.OutcomeSucceeded,
			Output:     `{"member":{"value":"input","outcome":"succeeded","error":null}}`,
		},
		{
			Activation: "combined:0",
			NodeID:     "combined",
			Kind:       string(ir.NodeGather),
			Outcome:    engine.OutcomeSucceeded,
			Output:     "42",
		},
	})

	resumed, err := engine.Resume(ctx, store, doc, streamID, nil, engine.Options{})
	if err != nil {
		t.Fatalf("resume partial v6 gather snapshot: %v", err)
	}
	requireResultJSON(t, resumed.Result,
		`{"value":42,"outcome":"succeeded","error":null}`)
}

func TestResumeV6PartialSnapshotReconstructsSettledForEachForPendingReference(t *testing.T) {
	ctx := context.Background()
	store := newStore(t)
	doc := decodeIR(t, bundleDoc(
		arrField("items"),
		forEachNode(
			nil,
			"item",
			"continue",
			refOver("items"),
			execNode("member", `printf '%s' '{{ item }}'`, nil),
		)+`,{
		  "kind": "settle", "id": "done", "name": "done", "after": ["fan"],
		  "origin": {"uri": "t", "line": 2, "col": 0},
		  "outcome": "succeeded",
		  "value": {
		    "kind": "member",
		    "base": {"kind": "ref", "name": "fan"},
		    "name": "1"
		  }
		}`,
		"",
	))
	const streamID = "gcg-run-v6-partial-for-each"
	seedV6SettledPrefixSnapshot(t, store, streamID, doc, []v6SettledNode{
		{
			Activation: "fan/0:0",
			NodeID:     "fan/0",
			Kind:       string(ir.NodeExec),
			Parent:     "fan:0",
			Outcome:    engine.OutcomeSucceeded,
			Output:     "alpha",
		},
		{
			Activation: "fan/1:0",
			NodeID:     "fan/1",
			Kind:       string(ir.NodeExec),
			Parent:     "fan:0",
			Outcome:    engine.OutcomeSucceeded,
			Output:     "beta",
		},
		{
			Activation: "fan:0",
			NodeID:     "fan",
			Kind:       string(ir.NodeScatter),
			Outcome:    engine.OutcomeSucceeded,
			Output: `{"0":{"value":"alpha","outcome":"succeeded","error":null},` +
				`"1":{"value":"beta","outcome":"succeeded","error":null}}`,
		},
	})

	resumed, err := engine.Resume(
		ctx,
		store,
		doc,
		streamID,
		map[string]any{"items": []any{"alpha", "beta"}},
		engine.Options{},
	)
	if err != nil {
		t.Fatalf("resume partial v6 for-each snapshot: %v", err)
	}
	requireResultJSON(t, resumed.Result,
		`{"value":"beta","outcome":"succeeded","error":null}`)
}

func TestResumeV6PartialSnapshotReconstructsRunBodiedForEachMemberBeforeHarvest(t *testing.T) {
	ctx := context.Background()
	store := newStore(t)
	doc := decodeIR(t, bundleDoc(
		arrField("items"),
		forEachNode(
			nil,
			"item",
			"continue",
			refOver("items"),
			runNodeJSON("lane", nil, "reviewer", "reviewer", "item"),
		),
		subDoc("reviewer", strField("reviewer"), `{
		  "kind": "settle",
		  "id": "draft",
		  "name": "draft",
		  "after": [],
		  "origin": {"uri": "t", "line": 1, "col": 0},
		  "outcome": "succeeded",
		  "value": {"kind": "ref", "name": "reviewer"}
		}`),
	))
	const streamID = "gcg-run-v6-partial-for-each-run"
	seedV6SettledPrefixSnapshot(t, store, streamID, doc, []v6SettledNode{
		{
			Activation: "fan/0/draft:0",
			NodeID:     "fan/0/draft",
			Kind:       string(ir.NodeSettle),
			Parent:     "fan/0:0",
			Outcome:    engine.OutcomeSucceeded,
			Output:     "alpha",
		},
		{
			Activation: "fan/0:0",
			NodeID:     "fan/0",
			Kind:       string(ir.NodeRun),
			Parent:     "fan:0",
			Outcome:    engine.OutcomeSucceeded,
			Output:     "alpha",
		},
	})

	resumed, err := engine.Resume(
		ctx,
		store,
		doc,
		streamID,
		map[string]any{"items": []any{"alpha"}},
		engine.Options{},
	)
	if err != nil {
		t.Fatalf("resume partial v6 run-bodied for-each snapshot: %v", err)
	}
	requireResultJSON(t, resumed.Result, `{
	  "value": {
	    "0": {"value":"alpha","outcome":"succeeded","error":null}
	  },
	  "outcome":"succeeded",
	  "error":null
	}`)
}

func TestResumeV6PartialSnapshotReconstructsSettledRunBodyAttemptForPendingLoop(t *testing.T) {
	ctx := context.Background()
	store := newStore(t)
	doc := decodeIR(t, bundleDoc(
		"",
		repeatRunLoop(nil,
			runNodeJSON("stage", nil, "reviewer", "", ""),
			runCondPassOrIter()),
		subDoc("reviewer", "", `{
		  "kind": "settle",
		  "id": "draft",
		  "name": "draft",
		  "after": [],
		  "origin": {"uri": "t", "line": 1, "col": 0},
		  "outcome": "succeeded",
		  "value": {"kind": "literal", "value": true}
		}`),
	))
	const streamID = "gcg-run-v6-partial-run-loop"
	seedV6SettledPrefixSnapshot(t, store, streamID, doc, []v6SettledNode{
		{
			Activation: "stage/0/draft:0",
			NodeID:     "stage/0/draft",
			Kind:       string(ir.NodeSettle),
			Parent:     "stage:0",
			Outcome:    engine.OutcomeSucceeded,
			Output:     "true",
		},
		{
			Activation: "stage:0",
			NodeID:     "stage",
			Kind:       string(ir.NodeRun),
			Parent:     "loop:0",
			Outcome:    engine.OutcomeSucceeded,
			Output:     "true",
		},
	})

	resumed, err := engine.Resume(ctx, store, doc, streamID, nil, engine.Options{})
	if err != nil {
		t.Fatalf("resume partial v6 run-body loop snapshot: %v", err)
	}
	requireResultJSON(t, resumed.Result,
		`{"value":true,"outcome":"succeeded","error":null}`)
}

func TestResumeV6PartialSnapshotReconstructsSettledLoopFromHighestAttempt(t *testing.T) {
	ctx := context.Background()
	store := newStore(t)
	doc := decodeIR(t, blockDoc(
		"partial-v6-settled-loop",
		`{
		  "kind":"repeat","id":"loop","name":"loop","after":[],
		  "origin":{"uri":"t","line":1,"col":0},
		  "body":`+execNode("review", `printf latest`, nil)+`,
		  "cond":{
		    "kind":"operator","op":"==","operands":[
		      {"kind":"ref","name":"review","field":"outcome"},
		      {"kind":"literal","value":"succeeded"}
		    ]
		  },
		  "iterationName":"iteration"
		}`,
		`{
		  "kind":"settle","id":"done","name":"done","after":["loop"],
		  "origin":{"uri":"t","line":2,"col":0},
		  "outcome":"succeeded",
		  "value":{"kind":"ref","name":"loop"}
		}`,
	))
	const streamID = "gcg-run-v6-partial-settled-loop"
	seedV6SettledPrefixSnapshot(t, store, streamID, doc, []v6SettledNode{
		{
			Activation: "review:2",
			NodeID:     "review",
			Kind:       string(ir.NodeExec),
			Parent:     "loop:0",
			Outcome:    engine.OutcomeFailed,
			Detail:     "exit_7",
		},
		{
			Activation: "review:10",
			NodeID:     "review",
			Kind:       string(ir.NodeExec),
			Parent:     "loop:0",
			Outcome:    engine.OutcomeSucceeded,
			Output:     "latest",
		},
		{
			Activation: "loop:0",
			NodeID:     "loop",
			Kind:       string(ir.NodeRepeat),
			Outcome:    engine.OutcomeSucceeded,
			Output:     "latest",
		},
	})

	resumed, err := engine.Resume(ctx, store, doc, streamID, nil, engine.Options{})
	if err != nil {
		t.Fatalf("resume partial v6 settled loop snapshot: %v", err)
	}
	requireResultJSON(t, resumed.Result,
		`{"value":"latest","outcome":"succeeded","error":null}`)
}

func TestResumeRejectsSnapshotOlderThanV4(t *testing.T) {
	ctx := context.Background()
	store := newStore(t)
	doc := decodeIR(t, blockDoc("stale-snapshot",
		execNode("work", `echo must-not-run`, nil),
	))
	const streamID = "gcg-run-v3-snapshot"

	seedHistoricalSnapshot(t, store, streamID, 3, 3, "")

	_, err := engine.Resume(ctx, store, doc, streamID, nil, engine.Options{})
	if !errors.Is(err, fold.ErrReducerVersionSkew) {
		t.Fatalf("resume v3 snapshot = %v, want ErrReducerVersionSkew", err)
	}
}

func seedHistoricalSnapshot(
	t *testing.T,
	store *graphstore.Store,
	streamID string,
	reducerVersion, formatVersion int,
	dialect string,
) {
	t.Helper()
	ctx := context.Background()
	engine.RegisterVocabulary(store)
	run := newManualRun(t, store, streamID)
	started := map[string]any{
		"root_id":    streamID,
		"name":       "historical",
		"created_at": "2026-01-01T00:00:00Z",
	}
	if dialect != "" {
		started["dialect"] = dialect
	}
	run.append(engine.EventRunStarted, streamID+":run:started", started)

	// A historical snapshot presupposes that its covered projection was already
	// committed. Rebuild it from the one-event journal before installing the
	// fixture snapshot so resumed node edges retain their root.
	if err := store.RebuildTierA(ctx, engine.Reducer(), streamID); err != nil {
		t.Fatalf("project historical prefix: %v", err)
	}

	stateFields := map[string]any{
		"root_id":    streamID,
		"name":       "historical",
		"created_at": "2026-01-01T00:00:00Z",
		"closed":     false,
	}
	if dialect != "" {
		stateFields["dialect"] = dialect
	}
	writeHistoricalSnapshot(t, store, run, reducerVersion, formatVersion, stateFields)
	run.close()
}

func writeHistoricalSnapshot(
	t *testing.T,
	store *graphstore.Store,
	run *manualRun,
	reducerVersion, formatVersion int,
	stateFields map[string]any,
) {
	t.Helper()
	state, err := canonJSON(t, stateFields)
	if err != nil {
		t.Fatalf("canonicalize historical state: %v", err)
	}
	stateHash := canon.Hash(state)
	anchorPayload, err := canonJSON(t, map[string]any{
		"covered_seq": run.head,
		"state_hash":  hex.EncodeToString(stateHash[:]),
	})
	if err != nil {
		t.Fatalf("canonicalize anchor: %v", err)
	}
	_, err = store.WriteSnapshot(context.Background(), engine.Engine, run.lease.Epoch, fold.Snapshot{
		StreamID:              run.stream,
		CoveredSeq:            run.head,
		Engine:                engine.Engine,
		ReducerVersion:        reducerVersion,
		SnapshotFormatVersion: formatVersion,
		StateHash:             stateHash,
		State:                 state,
	}, graphstore.JournalEvent{
		Type:              engine.EventSnapshotAnchored,
		IRContractVersion: "0.2.5",
		IdemToken:         run.stream + ":snap:historical",
		Payload:           anchorPayload,
	})
	if err != nil {
		t.Fatalf("write historical snapshot: %v", err)
	}
}

func seedV6PublicOutcomeSnapshot(
	t *testing.T,
	store *graphstore.Store,
	streamID string,
	doc *ir.IR,
) {
	t.Helper()
	ctx := context.Background()
	irHash := historicalIRHash(t, doc)

	engine.RegisterVocabulary(store)
	run := newManualRun(t, store, streamID)
	run.append(engine.EventRunStarted, streamID+":run:started", map[string]any{
		"root_id":    streamID,
		"name":       "public-v6-snapshot",
		"created_at": "2026-01-01T00:00:00Z",
		"dialect":    engine.SemanticDialectCurrent,
		"ir_hash":    irHash,
	})
	run.append(engine.EventNodeActivated, streamID+":old-failure:0:act", map[string]any{
		"node_id":    "old-failure",
		"activation": "old-failure:0",
		"kind":       "exec",
	})
	run.append(engine.EventOutcomeSettled, streamID+":old-failure:0:settled", map[string]any{
		"activation": "old-failure:0",
		"outcome":    engine.OutcomeFailed,
		"output":     "legacy",
		"detail":     "exit_7",
	})
	run.append(engine.EventNodeActivated, streamID+":review/draft:0:act", map[string]any{
		"node_id":    "review/draft",
		"activation": "review/draft:0",
		"parent":     "review:0",
		"kind":       "settle",
	})
	run.append(engine.EventOutcomeSettled, streamID+":review/draft:0:settled", map[string]any{
		"activation": "review/draft:0",
		"outcome":    engine.OutcomeSucceeded,
		"output":     `{"status":{"value":"approved","outcome":"succeeded","error":null}}`,
	})

	if err := store.RebuildTierA(ctx, engine.Reducer(), streamID); err != nil {
		t.Fatalf("project v6 public outcome prefix: %v", err)
	}
	writeHistoricalSnapshot(t, store, run, 6, 6, map[string]any{
		"root_id":    streamID,
		"name":       "public-v6-snapshot",
		"created_at": "2026-01-01T00:00:00Z",
		"dialect":    engine.SemanticDialectCurrent,
		"ir_hash":    irHash,
		"closed":     false,
		"outcome":    engine.OutcomeSucceeded,
		"nodes": map[string]any{
			"old-failure:0": map[string]any{
				"node_id": "old-failure",
				"kind":    "exec",
				"settled": true,
				"outcome": engine.OutcomeFailed,
				"output":  "legacy",
				"detail":  "exit_7",
			},
			"review/draft:0": map[string]any{
				"node_id": "review/draft",
				"kind":    "settle",
				"parent":  "review:0",
				"settled": true,
				"outcome": engine.OutcomeSucceeded,
				"output":  `{"status":{"value":"approved","outcome":"succeeded","error":null}}`,
			},
		},
	})
	run.close()
}

func seedV6TransparentWrapperSnapshot(
	t *testing.T,
	store *graphstore.Store,
	streamID string,
	doc *ir.IR,
) {
	t.Helper()
	ctx := context.Background()
	irHash := historicalIRHash(t, doc)
	const output = `{"status":{"value":"approved","outcome":"succeeded","error":null}}`

	engine.RegisterVocabulary(store)
	run := newManualRun(t, store, streamID)
	run.append(engine.EventRunStarted, streamID+":run:started", map[string]any{
		"root_id":    streamID,
		"name":       "recover-v6-result",
		"created_at": "2026-01-01T00:00:00Z",
		"dialect":    engine.SemanticDialectCurrent,
		"ir_hash":    irHash,
	})
	run.append(engine.EventNodeActivated, streamID+":rec:0:act", map[string]any{
		"node_id":    "rec",
		"activation": "rec:0",
		"kind":       "recover",
	})
	run.append(engine.EventNodeActivated, streamID+":guarded:0:act", map[string]any{
		"node_id":    "guarded",
		"activation": "guarded:0",
		"parent":     "rec:0",
		"kind":       "settle",
	})
	run.append(engine.EventOutcomeSettled, streamID+":guarded:0:settled", map[string]any{
		"activation": "guarded:0",
		"outcome":    engine.OutcomeSucceeded,
		"output":     output,
	})

	if err := store.RebuildTierA(ctx, engine.Reducer(), streamID); err != nil {
		t.Fatalf("project v6 wrapper prefix: %v", err)
	}
	writeHistoricalSnapshot(t, store, run, 6, 6, map[string]any{
		"root_id":    streamID,
		"name":       "recover-v6-result",
		"created_at": "2026-01-01T00:00:00Z",
		"dialect":    engine.SemanticDialectCurrent,
		"ir_hash":    irHash,
		"closed":     false,
		"outcome":    engine.OutcomeSucceeded,
		"nodes": map[string]any{
			"rec:0": map[string]any{
				"node_id": "rec",
				"kind":    "recover",
				"settled": false,
			},
			"guarded:0": map[string]any{
				"node_id": "guarded",
				"kind":    "settle",
				"parent":  "rec:0",
				"settled": true,
				"outcome": engine.OutcomeSucceeded,
				"output":  output,
			},
		},
	})
	run.close()
}

func historicalIRHash(t *testing.T, doc *ir.IR) string {
	t.Helper()
	rawIR, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("marshal historical IR: %v", err)
	}
	canonicalIR, err := canon.Canonicalize(rawIR)
	if err != nil {
		t.Fatalf("canonicalize historical IR: %v", err)
	}
	digest := canon.Hash(canonicalIR)
	return hex.EncodeToString(digest[:])
}

type v6SettledNode struct {
	Activation string
	NodeID     string
	Kind       string
	Parent     string
	Outcome    string
	Output     string
	Detail     string
}

func seedV6SettledPrefixSnapshot(
	t *testing.T,
	store *graphstore.Store,
	streamID string,
	doc *ir.IR,
	nodes []v6SettledNode,
) {
	t.Helper()
	ctx := context.Background()
	irHash := historicalIRHash(t, doc)

	engine.RegisterVocabulary(store)
	run := newManualRun(t, store, streamID)
	run.append(engine.EventRunStarted, streamID+":run:started", map[string]any{
		"root_id":    streamID,
		"name":       doc.Name,
		"created_at": "2026-01-01T00:00:00Z",
		"dialect":    engine.SemanticDialectCurrent,
		"ir_hash":    irHash,
	})

	stateNodes := make(map[string]any, len(nodes))
	for _, node := range nodes {
		run.append(engine.EventNodeActivated, streamID+":"+node.Activation+":act", map[string]any{
			"node_id":    node.NodeID,
			"activation": node.Activation,
			"parent":     node.Parent,
			"kind":       node.Kind,
		})
		run.append(engine.EventOutcomeSettled, streamID+":"+node.Activation+":settled", map[string]any{
			"activation": node.Activation,
			"outcome":    node.Outcome,
			"output":     node.Output,
			"detail":     node.Detail,
		})
		stateNodes[node.Activation] = map[string]any{
			"node_id": node.NodeID,
			"kind":    node.Kind,
			"parent":  node.Parent,
			"settled": true,
			"outcome": node.Outcome,
			"output":  node.Output,
			"detail":  node.Detail,
		}
	}

	if err := store.RebuildTierA(ctx, engine.Reducer(), streamID); err != nil {
		t.Fatalf("project v6 settled prefix: %v", err)
	}
	writeHistoricalSnapshot(t, store, run, 6, 6, map[string]any{
		"root_id":    streamID,
		"name":       doc.Name,
		"created_at": "2026-01-01T00:00:00Z",
		"dialect":    engine.SemanticDialectCurrent,
		"ir_hash":    irHash,
		"closed":     false,
		"outcome":    engine.OutcomeSucceeded,
		"nodes":      stateNodes,
	})
	run.close()
}
