package engine

import "testing"

func TestLowerCompletionProjectionClassifiesExistingEdge(t *testing.T) {
	cond := `{"kind":"operator","op":"==","operands":[` +
		`{"kind":"ref","name":"bad","field":"outcome"},` +
		`{"kind":"literal","value":"failed"}]}`
	doc := decodeBundle(t, plainDoc(
		execNode("bad", nil, "exit 1")+","+
			guardNode("g", []string{"bad"}, cond, execNode("gthen", nil, "echo t")),
	))

	units, err := buildUnits(doc, true, true)
	if err != nil {
		t.Fatalf("buildUnits: %v", err)
	}
	guard := unitByNode(units, "g")
	if guard == nil {
		t.Fatal("guard unit missing")
	}
	if !containsStr(guard.afterDeps, "bad:0") {
		t.Fatalf("guard afterDeps = %v, want bad:0", guard.afterDeps)
	}
	if !containsStr(guard.completionDeps, "bad:0") {
		t.Fatalf("guard completionDeps = %v, want bad:0", guard.completionDeps)
	}
	if containsStr(guard.skipDeps, "bad:0") {
		t.Fatalf("guard skipDeps = %v, do not want completion-only bad:0", guard.skipDeps)
	}

	driver := condScopeDriver(map[string]*nodeState{
		"bad:0": {
			NodeID:  "bad",
			Settled: true,
			Outcome: OutcomeFailed,
		},
	}, nil, nil)
	scope, err := driver.condScope("", map[string]string{"bad": ""}, map[string]string{"bad": ""})
	if err != nil {
		t.Fatalf("condScope: %v", err)
	}
	truthy, err := evalCondTruthy(guard.guard.cond, scope)
	if err != nil {
		t.Fatalf("eval cond: %v", err)
	}
	if !truthy {
		t.Fatal("bad.outcome == failed evaluated false")
	}
}
