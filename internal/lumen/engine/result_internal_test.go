package engine

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/gastownhall/gascity/internal/graphstore/fold"
	"github.com/gastownhall/gascity/internal/lumen/ir"
)

func TestRuntimeStepResultUsesPublicOutcomeVocabulary(t *testing.T) {
	tests := []struct {
		name    string
		outcome string
		value   any
		want    LumenStepResult
	}{
		{
			name:    "legacy success",
			outcome: outcomeLegacyPass,
			value:   "done",
			want:    LumenStepResult{Value: "done", Outcome: OutcomeSucceeded},
		},
		{
			name:    "cancellation is failed class",
			outcome: OutcomeCanceled,
			value:   "discarded",
			want: LumenStepResult{
				Value:   nil,
				Outcome: OutcomeFailed,
				Error:   &LumenResultError{Reason: OutcomeCanceled, Canceled: true},
			},
		},
		{
			name:    "pending cannot escape the runtime",
			outcome: OutcomePending,
			value:   "still running",
			want: LumenStepResult{
				Value:   nil,
				Outcome: OutcomeFailed,
				Error:   &LumenResultError{Reason: OutcomePending},
			},
		},
		{
			name:    "dependency skip has no value or error",
			outcome: OutcomeSkipped,
			value:   "discarded",
			want:    LumenStepResult{Value: nil, Outcome: OutcomeSkipped},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := runtimeStepResult(tt.outcome, tt.value, "", false, nil)
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("runtimeStepResult() = %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestAuthoredSettleResultPreservesSettleSemantics(t *testing.T) {
	got := authoredSettleResult(OutcomeSkipped, "discarded", "disabled")
	want := LumenStepResult{
		Value:   nil,
		Outcome: OutcomeSkipped,
		Error: &LumenResultError{
			Reason:  "disabled",
			Message: "disabled",
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("authoredSettleResult() = %#v, want %#v", got, want)
	}
}

func TestBlockResultAccumulatorFollowsSequentialSemantics(t *testing.T) {
	success := func(value any) LumenStepResult {
		return LumenStepResult{Value: value, Outcome: OutcomeSucceeded}
	}
	skipped := LumenStepResult{Value: nil, Outcome: OutcomeSkipped}
	failed := LumenStepResult{
		Value:   nil,
		Outcome: OutcomeFailed,
		Error:   &LumenResultError{Reason: "boom"},
	}

	tests := []struct {
		name     string
		children []LumenStepResult
		want     LumenStepResult
	}{
		{
			name:     "last success wins",
			children: []LumenStepResult{success("first"), success("last")},
			want:     success("last"),
		},
		{
			name:     "trailing skip is benign",
			children: []LumenStepResult{success("kept"), skipped},
			want:     success("kept"),
		},
		{
			name:     "all skipped",
			children: []LumenStepResult{skipped, skipped},
			want:     skipped,
		},
		{
			name:     "later success recovers failed block",
			children: []LumenStepResult{failed, success("recovered")},
			want:     success("recovered"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			acc := newBlockResult(OutcomeSucceeded)
			for _, child := range tt.children {
				acc.add(child)
			}
			if !reflect.DeepEqual(acc.result, tt.want) {
				t.Fatalf("block result = %#v, want %#v", acc.result, tt.want)
			}
		})
	}
}

func TestEvaluateLumenExprPreservesTypedValues(t *testing.T) {
	context := make(resultContext, 2)
	context.set("count", LumenStepResult{
		Value:   float64(2),
		Outcome: OutcomeSucceeded,
	})
	context.set("record", LumenStepResult{
		Value: map[string]LumenStepResult{
			"status": {Value: "ready", Outcome: OutcomeSucceeded},
		},
		Outcome: OutcomeSucceeded,
	})

	raw := json.RawMessage(`{
		"kind":"object",
		"entries":[
			{"key":"items","value":{"kind":"array","elements":[
				{"kind":"ref","name":"count"},
				{"kind":"member","base":{"kind":"ref","name":"record"},"name":"status"}
			]}}
		]
	}`)
	got, err := evaluateLumenExpr(raw, context)
	if err != nil {
		t.Fatalf("evaluateLumenExpr: %v", err)
	}
	want := map[string]LumenStepResult{
		"items": {
			Value:   []any{float64(2), "ready"},
			Outcome: OutcomeSucceeded,
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("evaluateLumenExpr() = %#v, want %#v", got, want)
	}

	missing, err := evaluateLumenExpr(
		json.RawMessage(`{"kind":"member","base":{"kind":"ref","name":"record"},"name":"missing"}`),
		context,
	)
	if err != nil {
		t.Fatalf("missing member: %v", err)
	}
	if missing != nil {
		t.Fatalf("missing member = %#v, want nil", missing)
	}

	_, err = evaluateLumenExpr(json.RawMessage(`{"kind":"future"}`), context)
	if !errors.Is(err, ErrUnsupportedNode) {
		t.Fatalf("unsupported kind error = %v, want ErrUnsupportedNode", err)
	}
}

func TestIsPublicOutcomeValidatesTheDiscriminatedShape(t *testing.T) {
	if !isPublicOutcome(map[string]any{
		"kind":   OutcomeFailed,
		"reason": "blocked",
	}) {
		t.Fatal("valid failed public outcome was rejected")
	}
	if isPublicOutcome(map[string]any{
		"kind":    OutcomeFailed,
		"payload": "ordinary record",
	}) {
		t.Fatal("ordinary record with kind=failed was accepted as a public outcome")
	}
	if isPublicOutcome(map[string]any{
		"kind":   OutcomeFailed,
		"reason": "blocked",
		"result": "forbidden",
	}) {
		t.Fatal("failed public outcome with a result field was accepted")
	}
}

func TestResultContextSelectsBestAttemptBeforeRecovery(t *testing.T) {
	latest := LumenStepResult{Value: "attempt ten", Outcome: OutcomeSucceeded}
	d := condScopeDriver(
		map[string]*nodeState{
			"review:2": {
				NodeID:  "review",
				Kind:    string(ir.NodeExec),
				Settled: true,
				Outcome: OutcomeFailed,
				Detail:  "exit_7",
			},
			"review:10": {
				NodeID:  "review",
				Kind:    string(ir.NodeSettle),
				Settled: true,
				Outcome: OutcomeSucceeded,
				Result:  &latest,
			},
		},
		nil,
		map[string]*runSpec{},
	)

	context, err := d.resultContextFor("", map[string]string{"review": "legacy"})
	if err != nil {
		t.Fatalf("resultContextFor: %v", err)
	}
	got, ok := context.get("review")
	if !ok || !reflect.DeepEqual(got, latest) {
		t.Fatalf("context review = %#v, %v; want latest attempt %#v", got, ok, latest)
	}
	if context.isUnavailable("review") {
		t.Fatal("latest exact attempt remained marked unavailable")
	}
}

func TestResultContextDefersUnavailableErrorUntilRead(t *testing.T) {
	d := condScopeDriver(
		map[string]*nodeState{
			"old-failure:0": {
				NodeID:  "old-failure",
				Kind:    string(ir.NodeExec),
				Settled: true,
				Outcome: OutcomeFailed,
				Detail:  "exit_7",
			},
		},
		nil,
		map[string]*runSpec{},
	)

	context, err := d.resultContextFor("", map[string]string{"old-failure": "legacy"})
	if err != nil {
		t.Fatalf("resultContextFor rejected an unread unavailable result: %v", err)
	}
	if _, err := evaluateLumenExpr(
		json.RawMessage(`{"kind":"literal","value":"independent"}`),
		context,
	); err != nil {
		t.Fatalf("evaluate independent literal: %v", err)
	}
	outcome, err := evaluateLumenExpr(
		json.RawMessage(`{"kind":"ref","name":"old-failure","field":"outcome"}`),
		context,
	)
	if err != nil {
		t.Fatalf("evaluate known outcome from unavailable result: %v", err)
	}
	if outcome != OutcomeFailed {
		t.Fatalf("unavailable result outcome = %#v, want %q", outcome, OutcomeFailed)
	}
	_, err = evaluateLumenExpr(
		json.RawMessage(`{"kind":"ref","name":"old-failure"}`),
		context,
	)
	if !errors.Is(err, errResultUnavailable) {
		t.Fatalf("evaluate unavailable ref = %v, want errResultUnavailable", err)
	}
}

func TestPromptStringReturnsEncodingErrors(t *testing.T) {
	_, err := promptString(make(chan int))
	if err == nil {
		t.Fatal("promptString accepted a value that JSON cannot encode")
	}
}

func TestLegacyOutcomeSettlementDoesNotFabricateAuthoredResult(t *testing.T) {
	state := &lumenState{
		RootID:          "gcg-run-legacy-author",
		SemanticDialect: SemanticDialectCurrent,
		Nodes: map[string]*nodeState{
			"author:0": {
				NodeID: "author",
				Kind:   string(ir.NodeSettle),
			},
		},
	}
	payload, err := json.Marshal(outcomeSettledPayload{
		Activation: "author:0",
		Outcome:    OutcomeSucceeded,
		Output:     "approved",
	})
	if err != nil {
		t.Fatalf("marshal settlement: %v", err)
	}

	next, _, err := applyOutcomeSettled(state.clone(), fold.Event{
		StreamID: "gcg-run-legacy-author",
		Seq:      2,
		Payload:  payload,
	})
	if err != nil {
		t.Fatalf("applyOutcomeSettled: %v", err)
	}
	if got := next.(*lumenState).Nodes["author:0"].Result; got != nil {
		t.Fatalf("legacy authored result = %#v, want nil until the IR reconstructs it", got)
	}
}

func TestAuthoredNodeResultReconstructsLegacyPublicOutcomeFromIR(t *testing.T) {
	var node ir.Node
	if err := json.Unmarshal([]byte(`{
		"kind": "settle",
		"id": "author",
		"name": "author",
		"after": [],
		"outcome": "succeeded",
		"value": {"kind": "literal", "value": "approved"},
		"publicOutcome": true
	}`), &node); err != nil {
		t.Fatalf("decode authored node: %v", err)
	}
	state := &lumenState{
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

	got, ok, err := authoredNodeResult(node, "", state, resultContext{}, nil)
	if err != nil {
		t.Fatalf("authoredNodeResult: %v", err)
	}
	if !ok {
		t.Fatal("authoredNodeResult did not find the settled author")
	}
	want := LumenStepResult{
		Value: LumenPublicOutcome{
			Kind:   OutcomeSucceeded,
			Result: "approved",
		},
		Outcome: OutcomeSucceeded,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("authoredNodeResult() = %#v, want %#v", got, want)
	}
}

func TestAuthoredNodeResultPreservesLegacyDependencySkip(t *testing.T) {
	var node ir.Node
	if err := json.Unmarshal([]byte(`{
		"kind": "settle",
		"id": "author",
		"name": "author",
		"after": ["blocked"],
		"outcome": "succeeded",
		"value": {"kind": "literal", "value": "never evaluated"},
		"publicOutcome": true
	}`), &node); err != nil {
		t.Fatalf("decode authored node: %v", err)
	}
	state := &lumenState{
		Nodes: map[string]*nodeState{
			"author:0": {
				NodeID:  "author",
				Kind:    string(ir.NodeSettle),
				Settled: true,
				Outcome: OutcomeSkipped,
				Detail:  dependencySkippedDetail,
			},
		},
	}

	got, ok, err := authoredNodeResult(node, "", state, resultContext{}, nil)
	if err != nil {
		t.Fatalf("authoredNodeResult: %v", err)
	}
	if !ok {
		t.Fatal("authoredNodeResult did not find the skipped author")
	}
	want := LumenStepResult{Value: nil, Outcome: OutcomeSkipped}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("authoredNodeResult() = %#v, want dependency skip %#v", got, want)
	}
}

func TestAuthoredSkipPreservesLegacyDependencySkip(t *testing.T) {
	var node ir.Node
	if err := json.Unmarshal([]byte(`{
		"kind": "settle",
		"id": "author",
		"name": "author",
		"after": ["blocked"],
		"outcome": "skipped",
		"reason": "authored reason",
		"publicOutcome": true
	}`), &node); err != nil {
		t.Fatalf("decode authored node: %v", err)
	}
	state := &lumenState{
		Nodes: map[string]*nodeState{
			"author:0": {
				NodeID:  "author",
				Kind:    string(ir.NodeSettle),
				Settled: true,
				Outcome: OutcomeSkipped,
				Detail:  dependencySkippedDetail,
			},
		},
	}

	got, ok, err := authoredNodeResult(node, "", state, resultContext{}, nil)
	if err != nil {
		t.Fatalf("authoredNodeResult: %v", err)
	}
	if !ok {
		t.Fatal("authoredNodeResult did not find the skipped author")
	}
	want := LumenStepResult{Value: nil, Outcome: OutcomeSkipped}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("authoredNodeResult() = %#v, want dependency skip %#v", got, want)
	}
}

func TestBlockResultContinuesAfterDependencySkippedPublicOutcome(t *testing.T) {
	var nodes []ir.Node
	if err := json.Unmarshal([]byte(`[
		{
			"kind": "settle",
			"id": "skipped-author",
			"name": "skipped-author",
			"after": ["blocked"],
			"outcome": "succeeded",
			"value": {"kind": "literal", "value": "never evaluated"},
			"publicOutcome": true
		},
		{
			"kind": "settle",
			"id": "fallback",
			"name": "fallback",
			"after": [],
			"outcome": "succeeded",
			"value": {"kind": "literal", "value": "continued"}
		}
	]`), &nodes); err != nil {
		t.Fatalf("decode block nodes: %v", err)
	}
	fallback := authoredSettleResult(OutcomeSucceeded, "continued", "")
	state := &lumenState{
		Nodes: map[string]*nodeState{
			"skipped-author:0": {
				NodeID:  "skipped-author",
				Kind:    string(ir.NodeSettle),
				Settled: true,
				Outcome: OutcomeSkipped,
				Detail:  dependencySkippedDetail,
			},
			"fallback:0": {
				NodeID:  "fallback",
				Kind:    string(ir.NodeSettle),
				Settled: true,
				Outcome: OutcomeSucceeded,
				Output:  "continued",
				Result:  &fallback,
			},
		},
	}

	got, err := blockResult(nodes, "", state, resultContext{}, nil)
	if err != nil {
		t.Fatalf("blockResult: %v", err)
	}
	if !reflect.DeepEqual(got, fallback) {
		t.Fatalf("blockResult() = %#v, want later sibling %#v", got, fallback)
	}
}
