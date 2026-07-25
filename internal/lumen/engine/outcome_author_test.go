package engine_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/gastownhall/gascity/internal/graphstore"
	"github.com/gastownhall/gascity/internal/lumen/engine"
)

func outcomeAuthorNode(outcome, value string, after ...string) string {
	const id = "author"
	if after == nil {
		after = []string{}
	}
	afterJSON, _ := json.Marshal(after)
	valueJSON, _ := json.Marshal(value)
	return `{
      "kind": "settle", "id": "` + id + `", "name": "` + id + `", "after": ` + string(afterJSON) + `,
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

func TestBlockedOutcomeAuthorDoesNotTransfer(t *testing.T) {
	doc := decodeIR(t, blockDoc("blocked-author",
		execNode("gate", `exit 1`, nil),
		outcomeAuthorNode(engine.OutcomeSucceeded, "must-not-transfer", "gate"),
		execNode("survivor", `echo survived`, []string{"author"}),
	))

	result, err := engine.Run(context.Background(), newStore(t), doc, nil)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	settled := settledOutcomeByID(t, result.Events)
	if got := settled["author"]; got != engine.OutcomeSkipped {
		t.Fatalf("blocked author outcome = %q, want skipped", got)
	}
	if got := result.NodeOutputs["survivor"]; got != "survived" {
		t.Fatalf("later sibling output = %q, want survived", got)
	}
}

func TestStepperConsumesOutcomeAuthorBeforeOfferingLaterDo(t *testing.T) {
	ctx := context.Background()
	store := newStore(t)
	doc := decodeIR(t, blockDoc("stepper-author",
		doNode("before", "Do the prerequisite.", nil),
		outcomeAuthorNode(engine.OutcomeSucceeded, "terminal", "before"),
		doNode("unreachable", "Must not be offered.", []string{"author"}),
	))
	streamID := enqueueV1(t, store, doc)

	first, err := engine.Step(ctx, store, doc, streamID, nil, engine.Options{})
	if err != nil {
		t.Fatalf("step: %v", err)
	}
	if first.Done || first.NodeID != "before" {
		t.Fatalf("step = %+v, want before", first)
	}
	final, err := engine.Settle(
		ctx, store, doc, streamID, nil,
		first.NodeID, engine.OutcomeSucceeded, "done", engine.Options{},
	)
	if err != nil {
		t.Fatalf("settle before: %v", err)
	}
	if !final.Done || final.Outcome != engine.OutcomeSucceeded {
		t.Fatalf("settle result = %+v, want done succeeded", final)
	}
	if hasNodeActivation(t, streamStored(t, store, streamID), "unreachable") {
		t.Fatal("stepper offered a later do after an outcome author")
	}
}

func TestReAdvancePreservesOutcomeTransferAfterCrash(t *testing.T) {
	ctx := context.Background()
	store := newStore(t)
	doc := decodeIR(t, blockDoc("advance-author-replay",
		outcomeAuthorNode(engine.OutcomeSucceeded, "terminal"),
		doNode("unreachable", "Must not dispatch.", []string{"author"}),
	))
	fake := newFakeWorkStore()
	const streamID = "gcg-run-outcome-author-replay"
	errCrash := errors.New("crash after outcome author")
	fired := false
	restore := engine.SetCrashHookForTest(func(boundary, _, activation string) error {
		if boundary == engine.CrashAfterSettle && activation == "author:0" && !fired {
			fired = true
			return errCrash
		}
		return nil
	})
	_, err := engine.Advance(ctx, store, doc, streamID, nil, fake.opts())
	restore()
	if !errors.Is(err, errCrash) {
		t.Fatalf("advance error = %v, want injected crash", err)
	}

	result, err := engine.Advance(ctx, store, doc, streamID, nil, fake.opts())
	if err != nil {
		t.Fatalf("re-advance: %v", err)
	}
	if !result.Sealed || result.Run.Outcome != engine.OutcomeSucceeded {
		t.Fatalf("re-advance = %+v, want sealed succeeded", result)
	}
	if got := fake.dispatchCount(); got != 0 {
		t.Fatalf("dispatched work count = %d, want 0", got)
	}
	if hasNodeActivation(t, result.Run.Events, "unreachable") {
		t.Fatal("re-advance admitted a later node after replaying the outcome author")
	}
}

func TestScatterConsumesDirectMemberOutcomeTransfer(t *testing.T) {
	doc := decodeIR(t, blockDoc("scatter-author",
		scatterNode("lanes", nil, "continue",
			outcomeAuthorNode(engine.OutcomeSucceeded, "terminal"),
			execNode("lane-after", `echo lane-ran`, []string{"author"}),
		),
		execNode("outer-after", `echo outer-ran`, []string{"lanes"}),
	))

	result, err := engine.Run(context.Background(), newStore(t), doc, nil)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	settled := settledOutcomeByID(t, result.Events)
	if got := settled["lanes"]; got != engine.OutcomeSucceeded {
		t.Fatalf("scatter outcome = %q, want succeeded", got)
	}
	if got := result.NodeOutputs["lane-after"]; got != "lane-ran" {
		t.Fatalf("later scatter member output = %q, want lane-ran", got)
	}
	if got := result.NodeOutputs["outer-after"]; got != "outer-ran" {
		t.Fatalf("outer sibling output = %q, want outer-ran", got)
	}
}

func TestNestedRunConsumesChildOutcomeTransfer(t *testing.T) {
	doc := decodeIR(t, bundleDoc(
		"",
		runNodeJSON("stage", nil, "child", "", "")+","+
			execNode("outer-after", `echo outer-ran`, []string{"stage"}),
		subDoc("child", "",
			outcomeAuthorNode(engine.OutcomeSucceeded, "terminal"),
			execNode("child-unreachable", `echo must-not-run`, []string{"author"}),
		),
	))

	result, err := engine.Run(context.Background(), newStore(t), doc, nil)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	settled := settledOutcomeByID(t, result.Events)
	if got := settled["stage"]; got != engine.OutcomeSucceeded {
		t.Fatalf("run outcome = %q, want succeeded", got)
	}
	if hasNodeActivation(t, result.Events, "stage/child-unreachable") {
		t.Fatal("nested run admitted a later child after an outcome author")
	}
	if got := result.NodeOutputs["outer-after"]; got != "outer-ran" {
		t.Fatalf("outer sibling output = %q, want outer-ran", got)
	}
}
