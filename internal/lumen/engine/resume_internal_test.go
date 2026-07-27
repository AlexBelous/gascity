package engine

import (
	"encoding/json"
	"testing"

	"github.com/gastownhall/gascity/internal/graphstore/canon"
	"github.com/gastownhall/gascity/internal/graphstore/fold"
	"github.com/gastownhall/gascity/internal/lumen/ir"
)

// TestReconstructOutputsPicksMaxNumericAttempt (T-K2) pins the S6 fix: a node
// re-attempted by a retry/repeat loop reuses ONE bare node id across activations
// b:0…b:N, and its authoritative output is the highest-numbered attempt's.
// activationKeys() is LEXICOGRAPHIC ("b:10" < "b:2"), so a plain last-write-wins
// walk seeds the WRONG attempt once a node has more than ten attempts (the loop
// cap is 32). reconstructOutputs must order by the numeric attempt suffix and let
// the max attempt win. This test FAILS on the pre-L5a lexicographic ordering.
func TestReconstructOutputsPicksMaxNumericAttempt(t *testing.T) {
	s := &lumenState{
		RootID: "gcg-run-x",
		Nodes: map[string]*nodeState{
			"b:2":  {NodeID: "b", Kind: "exec", Settled: true, Outcome: OutcomePass, Output: "two"},
			"b:10": {NodeID: "b", Kind: "exec", Settled: true, Outcome: OutcomePass, Output: "ten"},
		},
	}

	nodeOutputs, scope := reconstructOutputs(s)

	if got := scope["b"]; got != "ten" {
		t.Errorf("scope[b] = %q, want ten (attempt :10, the max numeric attempt — not :2 by lexical order)", got)
	}
	if got := nodeOutputs["b"]; got != "ten" {
		t.Errorf("nodeOutputs[b] = %q, want ten (the highest-numbered attempt)", got)
	}
}

func TestSnapshotMigrationV6RecoversLosslessRuntimeResult(t *testing.T) {
	legacy := &lumenState{
		RootID:          "gcg-run-v6-result",
		Name:            "legacy",
		CreatedAt:       "2026-01-01T00:00:00Z",
		SemanticDialect: SemanticDialectCurrent,
		Nodes: map[string]*nodeState{
			"ok:0": {
				NodeID:  "ok",
				Kind:    string(ir.NodeExec),
				Settled: true,
				Outcome: OutcomeSucceeded,
				Output:  "done",
			},
			"bad:0": {
				NodeID:    "bad",
				Kind:      string(ir.NodeDo),
				Settled:   true,
				Outcome:   OutcomeFailed,
				Output:    "discarded",
				Detail:    "agent unavailable",
				Retryable: true,
			},
		},
	}

	stateV7 := migrateV6State(t, legacy)
	ok := stateV7.Nodes["ok:0"].Result
	if ok == nil || ok.Outcome != OutcomeSucceeded || ok.Value != "done" || ok.Error != nil {
		t.Fatalf("migrated success result = %#v, want succeeded value done", ok)
	}
	bad := stateV7.Nodes["bad:0"].Result
	if bad == nil || bad.Outcome != OutcomeFailed || bad.Value != nil || bad.Error == nil {
		t.Fatalf("migrated do failure result = %#v, want failed structured error", bad)
	}
	if bad.Error.Reason != "agent unavailable" || bad.Error.Message != "agent unavailable" ||
		!bad.Error.Retryable {
		t.Fatalf("migrated do failure error = %#v, want preserved detail and retryability", bad.Error)
	}
}

func TestSnapshotMigrationV6DoesNotFabricateAuthoredResult(t *testing.T) {
	legacy := &lumenState{
		RootID:          "gcg-run-v6-author",
		Name:            "legacy",
		CreatedAt:       "2026-01-01T00:00:00Z",
		SemanticDialect: SemanticDialectCurrent,
		Nodes: map[string]*nodeState{
			"author:0": {
				NodeID:  "author",
				Kind:    string(ir.NodeSettle),
				Settled: true,
				Outcome: OutcomeSucceeded,
				Output:  "approved",
			},
		},
	}

	stateV7 := migrateV6State(t, legacy)
	if got := stateV7.Nodes["author:0"].Result; got != nil {
		t.Fatalf("migrated authored result = %#v, want nil until the IR reconstructs it", got)
	}
}

func TestSnapshotMigrationV6LeavesInsufficientResultUnset(t *testing.T) {
	legacy := &lumenState{
		RootID:          "gcg-run-v6-resultless",
		Name:            "legacy",
		CreatedAt:       "2026-01-01T00:00:00Z",
		SemanticDialect: SemanticDialectCurrent,
		Nodes: map[string]*nodeState{
			"unknown:0": {
				NodeID:  "unknown",
				Settled: true,
			},
		},
	}

	stateV7 := migrateV6State(t, legacy)
	if got := stateV7.Nodes["unknown:0"].Result; got != nil {
		t.Fatalf("migrated result-less node = %#v, want nil", got)
	}
}

func migrateV6State(t *testing.T, legacy *lumenState) *lumenState {
	t.Helper()
	state, err := legacy.MarshalSnapshot()
	if err != nil {
		t.Fatalf("marshal v6 state: %v", err)
	}
	migrated, err := snapshotForCurrentReducer(fold.Snapshot{
		StreamID:              legacy.RootID,
		CoveredSeq:            3,
		Engine:                Engine,
		ReducerVersion:        6,
		SnapshotFormatVersion: 6,
		StateHash:             canon.Hash(state),
		State:                 state,
	})
	if err != nil {
		t.Fatalf("migrate v6 snapshot: %v", err)
	}
	if migrated.ReducerVersion != 7 || migrated.SnapshotFormatVersion != 7 {
		t.Fatalf("migrated versions = reducer %d / format %d, want 7 / 7",
			migrated.ReducerVersion, migrated.SnapshotFormatVersion)
	}
	if got := canon.Hash(migrated.State); got != migrated.StateHash {
		t.Fatal("migrated state hash does not match migrated state")
	}

	var stateV7 lumenState
	if err := json.Unmarshal(migrated.State, &stateV7); err != nil {
		t.Fatalf("decode migrated state: %v", err)
	}
	return &stateV7
}
