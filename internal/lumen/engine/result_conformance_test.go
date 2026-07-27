package engine_test

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/gastownhall/gascity/internal/lumen/engine"
	"github.com/gastownhall/gascity/internal/lumen/enginehost"
	"github.com/gastownhall/gascity/internal/lumen/ir"
)

func requireResultJSON(t *testing.T, result engine.LumenStepResult, want string) {
	t.Helper()
	gotJSON, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("marshal result: %v", err)
	}
	var gotValue any
	if err := json.Unmarshal(gotJSON, &gotValue); err != nil {
		t.Fatalf("decode result: %v", err)
	}
	var wantValue any
	if err := json.Unmarshal([]byte(want), &wantValue); err != nil {
		t.Fatalf("decode expected result: %v", err)
	}
	if !reflect.DeepEqual(gotValue, wantValue) {
		t.Fatalf("result = %s, want %s", gotJSON, want)
	}
}

func TestRunResultUsesLastNonSkippedFormulaValue(t *testing.T) {
	doc := decodeIR(t, blockDoc("last-result",
		execNode("first", `echo first`, nil),
		execNode("last", `echo last`, []string{"first"}),
	))

	result, err := engine.Run(context.Background(), newStore(t), doc, nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	requireResultJSON(t, result.Result,
		`{"value":"last","outcome":"succeeded","error":null}`)
}

func TestRunOutcomeMatchesSequentialFormulaResult(t *testing.T) {
	doc := decodeIR(t, blockDoc("failure-recovered-by-later-step",
		execNode("deploy", `exit 1`, nil),
		guardExecAfter(
			"page",
			nil,
			condOutcomeEq("deploy", engine.OutcomeFailed),
			"page.then",
			`echo paged`,
		),
	))

	result, err := engine.Run(context.Background(), newStore(t), doc, nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	requireResultJSON(t, result.Result,
		`{"value":"paged","outcome":"succeeded","error":null}`)
	if result.Outcome != result.Result.Outcome {
		t.Fatalf("legacy outcome = %q, structured outcome = %q", result.Outcome, result.Result.Outcome)
	}
	if got := closedOutcome(t, result.Events); got != result.Result.Outcome {
		t.Fatalf("run.closed outcome = %q, structured outcome = %q", got, result.Result.Outcome)
	}
}

func TestPublicOutcomeAuthorReturnsStructuredValue(t *testing.T) {
	doc := structuredOutcomeDoc(t)

	result, err := engine.Run(context.Background(), newStore(t), doc, nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	requireResultJSON(t, result.Result,
		`{"value":{"kind":"succeeded","result":{"status":"ok"}},"outcome":"succeeded","error":null}`)
}

func TestSettleResultPreservesTypedInputValues(t *testing.T) {
	doc := decodeIR(t, bundleDoc(
		arrField("items"),
		`{
		  "kind": "settle", "id": "result", "name": "result", "after": [],
		  "origin": {"uri": "t", "line": 1, "col": 0},
		  "outcome": "succeeded",
		  "value": {"kind": "ref", "name": "items"}
		}`,
		"",
	))

	result, err := engine.Run(
		context.Background(),
		newStore(t),
		doc,
		map[string]any{"items": []any{"alpha", "beta"}},
	)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	requireResultJSON(t, result.Result,
		`{"value":["alpha","beta"],"outcome":"succeeded","error":null}`)
}

func TestGuardPreservesItsChildResult(t *testing.T) {
	doc := decodeIR(t, blockDoc("guard-result", `{
	  "kind": "guard", "id": "guarded", "name": "guarded", "after": [],
	  "origin": {"uri": "t", "line": 1, "col": 0},
	  "cond": {"kind": "literal", "value": true},
	  "then": `+doNode("child", "Review it.", nil)+`
	}`))
	host := &enginehost.StubHost{Results: map[string]enginehost.DoResult{
		"child": {
			Outcome: enginehost.OutcomeDegraded,
			Output:  "partial review",
			Detail:  "one source unavailable",
		},
	}}

	result, err := engine.RunWithOptions(
		context.Background(),
		newStore(t),
		doc,
		nil,
		engine.Options{Host: host},
	)
	if err != nil {
		t.Fatalf("RunWithOptions: %v", err)
	}
	requireResultJSON(t, result.Result,
		`{"value":"partial review","outcome":"degraded","error":{"reason":"one source unavailable","message":"one source unavailable"}}`)
}

func TestRecoverPreservesTheSelectedChildResult(t *testing.T) {
	doc := decodeIR(t, blockDoc("recover-result", `{
	  "kind": "recover", "id": "recovery", "name": "recovery", "after": [],
	  "origin": {"uri": "t", "line": 1, "col": 0},
	  "guarded": `+doNode("guarded", "Try it.", nil)+`,
	  "body": `+settleNode("fallback", engine.OutcomeSucceeded)+`,
	  "errorBinding": "error"
	}`))
	host := &enginehost.StubHost{Results: map[string]enginehost.DoResult{
		"guarded": {
			Outcome: enginehost.OutcomeDegraded,
			Output:  "usable result",
			Detail:  "minor warning",
		},
	}}

	result, err := engine.RunWithOptions(
		context.Background(),
		newStore(t),
		doc,
		nil,
		engine.Options{Host: host},
	)
	if err != nil {
		t.Fatalf("RunWithOptions: %v", err)
	}
	requireResultJSON(t, result.Result,
		`{"value":"usable result","outcome":"degraded","error":{"reason":"minor warning","message":"minor warning"}}`)
}

func TestCleanupPreservesTheGuardedResult(t *testing.T) {
	doc := decodeIR(t, blockDoc("cleanup-result",
		cleanupNode(
			nil,
			doNode("guarded", "Use the available sources.", nil),
			settleNode("finalizer", engine.OutcomeFailed),
		),
	))
	host := &enginehost.StubHost{Results: map[string]enginehost.DoResult{
		"guarded": {
			Outcome: enginehost.OutcomeDegraded,
			Output:  "usable result",
			Detail:  "one source unavailable",
		},
	}}

	result, err := engine.RunWithOptions(
		context.Background(),
		newStore(t),
		doc,
		nil,
		engine.Options{Host: host},
	)
	if err != nil {
		t.Fatalf("RunWithOptions: %v", err)
	}
	requireResultJSON(t, result.Result,
		`{"value":"usable result","outcome":"degraded","error":{"reason":"one source unavailable","message":"one source unavailable"}}`)
}

func TestRepeatPreservesTheLastChildResult(t *testing.T) {
	doc := decodeIR(t, blockDoc("repeat-result",
		repeatNode(
			doNode("review", "Review it.", nil),
			`{"kind":"literal","value":true}`,
		),
	))
	host := &enginehost.StubHost{Results: map[string]enginehost.DoResult{
		"review": {
			Outcome: enginehost.OutcomeDegraded,
			Output:  "usable review",
			Detail:  "one source unavailable",
		},
	}}

	result, err := engine.RunWithOptions(
		context.Background(),
		newStore(t),
		doc,
		nil,
		engine.Options{Host: host},
	)
	if err != nil {
		t.Fatalf("RunWithOptions: %v", err)
	}
	requireResultJSON(t, result.Result,
		`{"value":"usable review","outcome":"degraded","error":{"reason":"one source unavailable","message":"one source unavailable"}}`)
}

func TestRetryPreservesTheLastChildResultAndAddsBudget(t *testing.T) {
	doc := decodeIR(t, blockDoc("retry-result",
		retryNode(
			`{"kind":"literal","value":3}`,
			doNode("review", "Review it.", nil),
		),
	))
	host := &enginehost.StubHost{Results: map[string]enginehost.DoResult{
		"review": {
			Outcome: enginehost.OutcomeFailed,
			Detail:  "review is blocked",
		},
	}}

	result, err := engine.RunWithOptions(
		context.Background(),
		newStore(t),
		doc,
		nil,
		engine.Options{Host: host},
	)
	if err != nil {
		t.Fatalf("RunWithOptions: %v", err)
	}
	requireResultJSON(t, result.Result,
		`{"value":null,"outcome":"failed","error":{"reason":"review is blocked","message":"review is blocked","retriesRemaining":2}}`)
}

func TestRetryExhaustionPreservesChildErrorAndAddsAttempts(t *testing.T) {
	doc := decodeIR(t, blockDoc("retry-exhausted-result",
		retryNode(
			`{"kind":"literal","value":3}`,
			execNodeExit(
				"review",
				`printf 'review is blocked' >&2; exit 7`,
				[]int{0},
				[]int{7},
			),
		),
	))

	result, err := engine.Run(context.Background(), newStore(t), doc, nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	requireResultJSON(t, result.Result,
		`{"value":null,"outcome":"failed","error":{"reason":"exit_7","message":"review is blocked","retryable":true,"attempts":3,"retriesRemaining":0}}`)
}

func TestRunUsesTheSequentialSubformulaResult(t *testing.T) {
	doc := decodeIR(t, bundleDoc(
		"",
		runNodeJSON("review", nil, "reviewer", "", ""),
		subDoc(
			"reviewer",
			"",
			settleNode("failed-attempt", engine.OutcomeFailed),
			execNode("recovered", `echo recovered`, nil),
		),
	))

	result, err := engine.Run(context.Background(), newStore(t), doc, nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	requireResultJSON(t, result.Result,
		`{"value":"recovered","outcome":"succeeded","error":null}`)
}

func TestRunPreservesTheSubformulaStructuredResult(t *testing.T) {
	doc := decodeIR(t, bundleDoc(
		"",
		runNodeJSON("review", nil, "reviewer", "", ""),
		subDoc("reviewer", "", `{
		  "kind": "settle", "id": "result", "name": "result", "after": [],
		  "origin": {"uri": "t", "line": 1, "col": 0},
		  "outcome": "succeeded",
		  "value": {
		    "kind": "object",
		    "entries": [{
		      "key": "status",
		      "value": {"kind": "literal", "value": "approved"}
		    }]
		  },
		  "publicOutcome": true
		}`),
	))

	result, err := engine.Run(context.Background(), newStore(t), doc, nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	requireResultJSON(t, result.Result,
		`{"value":{"kind":"succeeded","result":{"status":"approved"}},"outcome":"succeeded","error":null}`)
}

func TestFormulaResultIncludesATrailingLiteral(t *testing.T) {
	doc := decodeIR(t, blockDoc(
		"literal-result",
		execNode("first", `echo first`, nil),
		`{
		  "kind": "lit", "id": "answer", "name": "answer", "after": ["first"],
		  "origin": {"uri": "t", "line": 1, "col": 0},
		  "value": 64
		}`,
	))

	result, err := engine.Run(context.Background(), newStore(t), doc, nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	requireResultJSON(t, result.Result,
		`{"value":64,"outcome":"succeeded","error":null}`)
}

func TestRunIncludesSilentSubformulaMembers(t *testing.T) {
	doc := decodeIR(t, bundleDoc(
		"",
		runNodeJSON("release", nil, "labeler", "", ""),
		subDoc(
			"labeler",
			"",
			`{
			  "kind": "lit", "id": "amount", "name": "amount", "after": [],
			  "origin": {"uri": "t", "line": 1, "col": 0},
			  "value": 64
			}`,
			`{
			  "kind": "interp", "id": "label", "name": "label", "after": ["amount"],
			  "origin": {"uri": "t", "line": 2, "col": 0},
			  "parts": [
			    {"kind": "text", "value": "release "},
			    {"kind": "interp", "expr": {"kind": "ref", "name": "amount"}}
			  ]
			}`,
		),
	))

	result, err := engine.Run(context.Background(), newStore(t), doc, nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	requireResultJSON(t, result.Result,
		`{"value":"release 64","outcome":"succeeded","error":null}`)
}

func TestPublicOutcomeAuthorsMatchUpstreamShapes(t *testing.T) {
	tests := []struct {
		name string
		node string
		want string
	}{
		{
			name: "degrade",
			node: `{
			  "kind": "settle", "id": "author", "name": "author", "after": [],
			  "origin": {"uri": "t", "line": 1, "col": 0},
			  "outcome": "degraded",
			  "value": {"kind": "literal", "value": "partial"},
			  "reason": "one reviewer failed",
			  "publicOutcome": true
			}`,
			want: `{"value":{"kind":"degraded","result":"partial","reason":"one reviewer failed"},"outcome":"degraded","error":{"reason":"one reviewer failed","message":"one reviewer failed"}}`,
		},
		{
			name: "fail",
			node: `{
			  "kind": "settle", "id": "author", "name": "author", "after": [],
			  "origin": {"uri": "t", "line": 1, "col": 0},
			  "outcome": "failed",
			  "reason": "blocked",
			  "publicOutcome": true
			}`,
			want: `{"value":{"kind":"failed","reason":"blocked"},"outcome":"failed","error":{"reason":"blocked","message":"blocked"}}`,
		},
		{
			name: "skip",
			node: `{
			  "kind": "settle", "id": "author", "name": "author", "after": [],
			  "origin": {"uri": "t", "line": 1, "col": 0},
			  "outcome": "skipped",
			  "reason": "disabled",
			  "publicOutcome": true
			}`,
			want: `{"value":{"kind":"skipped","reason":"disabled"},"outcome":"skipped","error":{"reason":"disabled","message":"disabled"}}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			doc := decodeIR(t, blockDoc("outcome-"+tt.name, tt.node))
			result, err := engine.Run(context.Background(), newStore(t), doc, nil)
			if err != nil {
				t.Fatalf("Run: %v", err)
			}
			requireResultJSON(t, result.Result, tt.want)
		})
	}
}

func TestStructuredResultSurvivesSnapshotResume(t *testing.T) {
	ctx := context.Background()
	store := newStore(t)
	doc := structuredOutcomeDoc(t)

	result, err := engine.RunWithOptions(ctx, store, doc, nil, engine.Options{SnapshotEvery: 1})
	if err != nil {
		t.Fatalf("RunWithOptions: %v", err)
	}
	if snapshots := allSnapshots(t, store, result.StreamID); len(snapshots) == 0 {
		t.Fatal("RunWithOptions persisted no snapshot")
	}

	resumed, err := engine.Resume(ctx, store, doc, result.StreamID, nil, engine.Options{SnapshotEvery: 1})
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	requireResultJSON(t, resumed.Result,
		`{"value":{"kind":"succeeded","result":{"status":"ok"}},"outcome":"succeeded","error":null}`)
}

func TestTerminalStepperResultMatchesRunResult(t *testing.T) {
	ctx := context.Background()
	store := newStore(t)
	doc := structuredOutcomeDoc(t)
	streamID := enqueueV1(t, store, doc)

	stepped, err := engine.Step(ctx, store, doc, streamID, nil, engine.Options{})
	if err != nil {
		t.Fatalf("Step: %v", err)
	}
	if !stepped.Done || stepped.Result == nil {
		t.Fatalf("terminal StepResult = %+v, want Done with a structured result", stepped)
	}
	resumed, err := engine.Resume(ctx, store, doc, streamID, nil, engine.Options{})
	if err != nil {
		t.Fatalf("Resume closed run: %v", err)
	}

	stepJSON, err := json.Marshal(stepped.Result)
	if err != nil {
		t.Fatalf("marshal StepResult.Result: %v", err)
	}
	runJSON, err := json.Marshal(resumed.Result)
	if err != nil {
		t.Fatalf("marshal RunResult.Result: %v", err)
	}
	if string(stepJSON) != string(runJSON) {
		t.Fatalf("StepResult.Result = %s, RunResult.Result = %s", stepJSON, runJSON)
	}
}

func structuredOutcomeDoc(t *testing.T) *ir.IR {
	t.Helper()
	return decodeIR(t, blockDoc("structured-outcome", `{
      "kind": "settle", "id": "author", "name": "author", "after": [],
      "origin": {"uri": "t", "line": 1, "col": 0},
      "outcome": "succeeded",
      "value": {
        "kind": "object",
        "entries": [{
          "key": "status",
          "value": {"kind": "literal", "value": "ok"}
        }]
      },
      "publicOutcome": true
    }`))
}

func TestScatterHarvestsMemberStepResultsAsData(t *testing.T) {
	doc := goldenDoc(t, "scatter-members.ir.json")
	host := &enginehost.StubHost{Results: map[string]enginehost.DoResult{
		"gpt":    {Outcome: enginehost.OutcomePass, Output: "gpt review"},
		"claude": {Outcome: enginehost.OutcomePass, Output: "claude review"},
	}}

	result, err := engine.RunWithOptions(
		context.Background(),
		newStore(t),
		doc,
		nil,
		engine.Options{Host: host},
	)
	if err != nil {
		t.Fatalf("RunWithOptions: %v", err)
	}
	requireResultJSON(t, result.Result,
		`{"value":{"claude":{"value":"claude review","outcome":"succeeded","error":null},"gpt":{"value":"gpt review","outcome":"succeeded","error":null}},"outcome":"succeeded","error":null}`)
}

func TestScatterPreservesAuthoredMemberStructure(t *testing.T) {
	doc := decodeIR(t, blockDoc("scatter-structure", scatterNode(
		"reviews",
		nil,
		"continue",
		`{
		  "kind": "do", "id": "failed-id", "name": "failed", "after": [],
		  "origin": {"uri": "t", "line": 1, "col": 0},
		  "source": {"kind": "prompt"},
		  "interpreter": {"kind": "agent", "mode": {"kind": "do"}},
		  "body": {"kind": "template", "parts": [{"kind": "text", "value": "Review it."}]}
		}`,
		`{
		  "kind": "lit", "id": "constant-id", "name": "constant", "after": [],
		  "origin": {"uri": "t", "line": 2, "col": 0},
		  "value": 7
		}`,
		`{
		  "kind": "block", "id": "group-id", "name": "group", "after": [],
		  "origin": {"uri": "t", "line": 3, "col": 0},
		  "members": [
		    {
		      "kind": "settle", "id": "group-failed", "name": "group-failed", "after": [],
		      "origin": {"uri": "t", "line": 3, "col": 0},
		      "outcome": "failed"
		    },
		    {
		      "kind": "exec", "id": "group-recovered", "name": "group-recovered", "after": [],
		      "origin": {"uri": "t", "line": 3, "col": 0},
		      "interpreter": {"kind": "shell", "program": {"kind": "bash"}},
		      "body": {"kind": "template", "raw": "echo recovered"}
		    }
		  ]
		}`,
		`{
		  "kind": "guard", "id": "skipped-id", "name": "skipped", "after": [],
		  "origin": {"uri": "t", "line": 4, "col": 0},
		  "cond": {"kind": "literal", "value": false},
		  "then": {
		    "kind": "exec", "id": "never", "name": "never", "after": [],
		    "origin": {"uri": "t", "line": 4, "col": 0},
		    "interpreter": {"kind": "shell", "program": {"kind": "bash"}},
		    "body": {"kind": "template", "raw": "echo never"}
		  }
		}`,
	)))
	host := &enginehost.StubHost{Results: map[string]enginehost.DoResult{
		"failed-id": {
			Outcome: enginehost.OutcomeFailed,
			Detail:  "model unavailable",
		},
	}}

	result, err := engine.RunWithOptions(
		context.Background(),
		newStore(t),
		doc,
		nil,
		engine.Options{Host: host},
	)
	if err != nil {
		t.Fatalf("RunWithOptions: %v", err)
	}
	requireResultJSON(t, result.Result, `{
	  "value": {
	    "failed": {
	      "value": null,
	      "outcome": "failed",
	      "error": {"reason": "model unavailable", "message": "model unavailable"}
	    },
	    "constant": {"value": 7, "outcome": "succeeded", "error": null},
	    "group": {"value": "recovered", "outcome": "succeeded", "error": null},
	    "skipped": {"value": null, "outcome": "skipped", "error": null}
	  },
	  "outcome": "succeeded",
	  "error": null
	}`)
}

func TestScatterHarvestIsAvailableToDownstreamExpressions(t *testing.T) {
	doc := decodeIR(t, blockDoc(
		"scatter-downstream",
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
		  "kind": "settle", "id": "result", "name": "result", "after": ["values"],
		  "origin": {"uri": "t", "line": 2, "col": 0},
		  "outcome": "succeeded",
		  "value": {
		    "kind": "member",
		    "base": {"kind": "ref", "name": "values"},
		    "name": "constant"
		  }
		}`,
	))

	result, err := engine.Run(context.Background(), newStore(t), doc, nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	requireResultJSON(t, result.Result,
		`{"value":7,"outcome":"succeeded","error":null}`)
}

func TestScatterHarvestRemainsRenderableAfterResume(t *testing.T) {
	doc := decodeIR(t, blockDoc(
		"scatter-resume",
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
		execNode("render", `printf '%s' '{{ values }}'`, []string{"values"}),
	))

	result, _, _ := injectCrashThenResume(
		t,
		doc,
		nil,
		engine.CrashAfterSettle,
		"values:0",
		0,
	)
	if got := result.NodeOutputs["render"]; got !=
		`{"constant":{"value":7,"outcome":"succeeded","error":null}}` {
		t.Fatalf("rendered harvest = %q", got)
	}
}

func TestGatherAuthoredVerdictStopsTheCombine(t *testing.T) {
	doc := decodeIR(t, blockDoc(
		"gather-verdict",
		scatterNode(
			"reviews",
			nil,
			"continue",
			`{
			  "kind": "lit", "id": "review", "name": "review", "after": [],
			  "origin": {"uri": "t", "line": 1, "col": 0},
			  "value": "looks good"
			}`,
		),
		gatherNode(
			"verdict",
			"reviews",
			[]string{"reviews"},
			`{
			  "kind": "settle", "id": "degrade", "name": "degrade", "after": [],
			  "origin": {"uri": "t", "line": 2, "col": 0},
			  "outcome": "degraded",
			  "reason": "one reviewer failed, but the rest passed",
			  "publicOutcome": true
			}`,
			doNode("late", "This must not run.", nil),
		),
	))
	host := &enginehost.StubHost{}

	result, err := engine.RunWithOptions(
		context.Background(),
		newStore(t),
		doc,
		nil,
		engine.Options{Host: host},
	)
	if err != nil {
		t.Fatalf("RunWithOptions: %v", err)
	}
	if calls := host.Calls(); len(calls) != 0 {
		t.Fatalf("late combine host calls = %d, want 0", len(calls))
	}
	requireResultJSON(t, result.Result, `{
	  "value": {
	    "kind": "degraded",
	    "result": null,
	    "reason": "one reviewer failed, but the rest passed"
	  },
	  "outcome": "degraded",
	  "error": {
	    "reason": "one reviewer failed, but the rest passed",
	    "message": "one reviewer failed, but the rest passed"
	  }
	}`)
}

func TestGatherIncludesATrailingSilentResult(t *testing.T) {
	doc := decodeIR(t, blockDoc(
		"gather-silent",
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
	))

	result, err := engine.Run(context.Background(), newStore(t), doc, nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	requireResultJSON(t, result.Result,
		`{"value":42,"outcome":"succeeded","error":null}`)
}
