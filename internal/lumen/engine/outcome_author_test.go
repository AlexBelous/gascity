package engine_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/gastownhall/gascity/internal/graphstore"
	"github.com/gastownhall/gascity/internal/lumen/engine"
)

func outcomeAuthorNode(outcome, value string) string {
	const id = "author"
	valueJSON, _ := json.Marshal(value)
	return `{
      "kind": "settle", "id": "` + id + `", "name": "` + id + `", "after": [],
      "origin": {"uri": "t", "line": 1, "col": 0},
      "outcome": "` + outcome + `", "value": {"kind": "literal", "value": ` + string(valueJSON) + `},
      "publicOutcome": true
    }`
}

func groupingBlockNode(id string, members ...string) string {
	return `{
      "kind": "block", "id": "` + id + `", "after": [],
      "origin": {"uri": "t", "line": 1, "col": 0},
      "members": [` + strings.Join(members, ",") + `]
    }`
}

func hasNodeActivation(t *testing.T, events []graphstore.StoredEvent, nodeID string) bool {
	t.Helper()
	for _, event := range events {
		if event.Type != engine.EventNodeActivated {
			continue
		}
		var payload struct {
			NodeID string `json:"node_id"`
		}
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			t.Fatalf("decode node.activated: %v", err)
		}
		if payload.NodeID == nodeID {
			return true
		}
	}
	return false
}

func TestOutcomeAuthorStopsLaterBlockMembersBeforeAdmission(t *testing.T) {
	for _, outcome := range []string{engine.OutcomeSucceeded, engine.OutcomeFailed} {
		t.Run(outcome, func(t *testing.T) {
			doc := decodeIR(t, blockDoc("author-"+outcome,
				outcomeAuthorNode(outcome, "terminal"),
				execNode("unreachable", `echo must-not-run`, []string{"author"}),
			))

			result, err := engine.Run(context.Background(), newStore(t), doc, nil)
			if err != nil {
				t.Fatalf("run: %v", err)
			}
			if result.Outcome != outcome {
				t.Fatalf("run outcome = %q, want %q", result.Outcome, outcome)
			}
			if hasNodeActivation(t, result.Events, "unreachable") {
				t.Fatal("later block member was admitted after an outcome author")
			}
			if _, ok := result.NodeOutputs["unreachable"]; ok {
				t.Fatal("later block member produced output after an outcome author")
			}
		})
	}
}

func TestOutcomeAuthorStopsOnlyItsNearestBlockScope(t *testing.T) {
	doc := decodeIR(t, blockDoc("nearest-scope",
		groupingBlockNode("inner",
			outcomeAuthorNode(engine.OutcomeSucceeded, "inner-result"),
			execNode("inner-unreachable", `echo must-not-run`, []string{"author"}),
		),
		execNode("outer-after", `echo outer-ran`, nil),
	))

	result, err := engine.Run(context.Background(), newStore(t), doc, nil)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if hasNodeActivation(t, result.Events, "inner-unreachable") {
		t.Fatal("later member in authored block was admitted")
	}
	if got := result.NodeOutputs["outer-after"]; got != "outer-ran" {
		t.Fatalf("outer sibling output = %q, want %q", got, "outer-ran")
	}
}

func TestNonPublicSettleRemainsAnOrdinarySequentialStep(t *testing.T) {
	doc := decodeIR(t, blockDoc("ordinary-settle",
		settleNode("value", engine.OutcomeSucceeded),
		execNode("after", `echo ran`, []string{"value"}),
	))

	result, err := engine.Run(context.Background(), newStore(t), doc, nil)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if got := result.NodeOutputs["after"]; got != "ran" {
		t.Fatalf("later step output = %q, want %q", got, "ran")
	}
}

func TestCleanupFinalizerRunsBeforeGuardedOutcomeTransferEscapes(t *testing.T) {
	doc := decodeIR(t, blockDoc("cleanup-transfer",
		cleanupNode(nil,
			outcomeAuthorNode(engine.OutcomeSucceeded, "terminal"),
			execNode("finalizer", `echo cleaned`, nil),
		),
		execNode("unreachable", `echo must-not-run`, []string{"clean"}),
	))

	result, err := engine.Run(context.Background(), newStore(t), doc, nil)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if got := result.NodeOutputs["finalizer"]; got != "cleaned" {
		t.Fatalf("finalizer output = %q, want %q", got, "cleaned")
	}
	if hasNodeActivation(t, result.Events, "unreachable") {
		t.Fatal("later sibling was admitted after cleanup propagated its guarded outcome author")
	}
}

func TestAdvanceOutcomeAuthorDoesNotDispatchLaterAgent(t *testing.T) {
	store := newStore(t)
	fake := newFakeWorkStore()
	doc := decodeIR(t, blockDoc("advance-author",
		outcomeAuthorNode(engine.OutcomeSucceeded, "terminal"),
		doNode("unreachable", "must not dispatch", []string{"author"}),
	))

	result, err := engine.Advance(
		context.Background(),
		store,
		doc,
		"gcg-run-outcome-author",
		nil,
		fake.opts(),
	)
	if err != nil {
		t.Fatalf("advance: %v", err)
	}
	if !result.Sealed || result.Run.Outcome != engine.OutcomeSucceeded {
		t.Fatalf("advance = %+v, want sealed succeeded run", result)
	}
	if got := fake.dispatchCount(); got != 0 {
		t.Fatalf("dispatched work count = %d, want 0", got)
	}
	if hasNodeActivation(t, result.Run.Events, "unreachable") {
		t.Fatal("later agent node was admitted after an outcome author")
	}
}
