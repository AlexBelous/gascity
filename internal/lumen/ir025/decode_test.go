package ir025_test

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gastownhall/gascity/internal/lumen/compiler"
	"github.com/gastownhall/gascity/internal/lumen/ir025"
	"github.com/gastownhall/gascity/internal/lumen/program"
)

func TestDecodeProjectsObservedFirstCohort(t *testing.T) {
	decoded, err := ir025.Decode([]byte(observedFirstCohort))
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	formula := decoded.Formula()
	if formula.Name() != "main" {
		t.Fatalf("formula name = %q, want main", formula.Name())
	}
	fields := formula.Input().Fields()
	if len(fields) != 1 || fields[0].Name() != "message" || !fields[0].Required() {
		t.Fatalf("input fields = %#v, want required message", fields)
	}
	steps := formula.Steps()
	if len(steps) != 5 {
		t.Fatalf("steps = %d, want 5", len(steps))
	}
	block, ok := steps[0].(program.Block)
	if !ok {
		t.Fatalf("steps[0] = %T, want program.Block", steps[0])
	}
	exec, ok := block.Members()[0].(program.Exec)
	if !ok {
		t.Fatalf("block member = %T, want program.Exec", block.Members()[0])
	}
	if exec.ID() != "exec_1" || len(exec.Environment()) != 1 || len(exec.Script().Parts()) != 2 {
		t.Fatalf("exec = %#v, want observed exec projection", exec)
	}
	for index, want := range []string{"succeeded", "degraded", "failed", "skipped"} {
		terminal, ok := steps[index+1].(program.Terminal)
		if !ok {
			t.Fatalf("steps[%d] = %T, want terminal", index+1, steps[index+1])
		}
		switch outcome := terminal.Outcome().(type) {
		case program.Succeeded:
			if want != "succeeded" {
				t.Fatalf("steps[%d] outcome = %T, want %s", index+1, outcome, want)
			}
			if _, ok := outcome.Value().(program.Record); !ok {
				t.Fatalf("succeeded value = %T, want program.Record", outcome.Value())
			}
		case program.Degraded:
			if want != "degraded" || outcome.Reason() != "degraded" {
				t.Fatalf("degraded outcome = %#v", outcome)
			}
		case program.Failed:
			if want != "failed" || outcome.Reason() != "failed" {
				t.Fatalf("failed outcome = %#v", outcome)
			}
		case program.Skipped:
			if want != "skipped" || outcome.Reason() != "skipped" {
				t.Fatalf("skipped outcome = %#v", outcome)
			}
		default:
			t.Fatalf("steps[%d] outcome = %T, want %s", index+1, outcome, want)
		}
	}
}

func TestDecodeProjectsPinnedArtifactOutput(t *testing.T) {
	usePinnedNode(t)
	compiled, err := compiler.CompileForExecution(t.Context(), "main.lumen", []byte("formula main(message: string) {\n  exec: echo {{ message }}\n}\n"), "main")
	if err != nil {
		t.Fatalf("CompileForExecution: %v", err)
	}
	if _, err := ir025.Decode(compiled); err != nil {
		t.Fatalf("Decode pinned artifact output: %v", err)
	}
}

func TestDecodeProjectsPinnedArtifactTerminalOutcomes(t *testing.T) {
	usePinnedNode(t)
	tests := []struct {
		name   string
		source string
		value  string
	}{
		{name: "succeeded", source: "formula main() {\n  succeed \"ok\"\n}\n", value: "ok"},
		{name: "degraded", source: "formula main() {\n  degrade \"why\"\n}\n", value: "why"},
		{name: "failed", source: "formula main() {\n  fail \"why\"\n}\n", value: "why"},
		{name: "skipped", source: "formula main() {\n  skip \"why\"\n}\n", value: "why"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			compiled, err := compiler.CompileForExecution(t.Context(), "main.lumen", []byte(test.source), "main")
			if err != nil {
				t.Fatalf("CompileForExecution: %v", err)
			}
			decoded, err := ir025.Decode(compiled)
			if err != nil {
				t.Fatalf("Decode: %v", err)
			}
			terminal, ok := decoded.Formula().Steps()[0].(program.Terminal)
			if !ok {
				t.Fatalf("step = %T, want program.Terminal", decoded.Formula().Steps()[0])
			}
			switch outcome := terminal.Outcome().(type) {
			case program.Succeeded:
				literal, ok := outcome.Value().(program.Literal)
				if !ok || literal.Value() != program.String(test.value) {
					t.Fatalf("succeeded value = %#v, want %q", outcome.Value(), test.value)
				}
			case program.Degraded:
				if test.name != "degraded" || outcome.Reason() != test.value {
					t.Fatalf("degraded outcome = %#v, want reason %q", outcome, test.value)
				}
			case program.Failed:
				if test.name != "failed" || outcome.Reason() != test.value {
					t.Fatalf("failed outcome = %#v, want reason %q", outcome, test.value)
				}
			case program.Skipped:
				if test.name != "skipped" || outcome.Reason() != test.value {
					t.Fatalf("skipped outcome = %#v, want reason %q", outcome, test.value)
				}
			default:
				t.Fatalf("outcome = %T, want %s", outcome, test.name)
			}
		})
	}
}

func TestDecodeFailsClosed(t *testing.T) {
	malformedFormulaOrigin := strings.TrimSuffix(observedFormula, `,"origin":{"uri":"main.lumen","line":0,"col":0}}`)
	if malformedFormulaOrigin == observedFormula {
		t.Fatal("observed formula fixture lost its top-level origin suffix")
	}
	malformedFormulaOrigin += `,"origin":null}`

	tests := map[string]string{
		"unknown top-level field":  replace(observedFirstCohort, `"diagnostics":[]`, `"diagnostics":[],"extra":true`),
		"unknown node kind":        replace(observedFirstCohort, `"kind":"block"`, `"kind":"mystery"`),
		"retired scatter":          replace(observedFirstCohort, `"kind":"block"`, `"kind":"scatter"`),
		"deferred extern":          replace(observedFirstCohort, `"kind":"block"`, `"kind":"extern"`),
		"unknown exec field":       replace(observedFirstCohort, `"retryable":[]}`, `"retryable":[],"extra":true}`),
		"unproved reference":       replace(observedFirstCohort, `"name":"message"`, `"name":"unproved"`),
		"malformed required input": replace(observedFirstCohort, `"required":true`, `"required":"true"`),
		"malformed formula origin": observedCompilerResult(malformedFormulaOrigin),
	}
	for name, payload := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := ir025.Decode([]byte(payload))
			if err == nil {
				t.Fatal("Decode accepted unsupported compiler evidence")
			}
			var decodeError *ir025.DecodeError
			if !errors.As(err, &decodeError) {
				t.Fatalf("Decode error = %T, want *DecodeError", err)
			}
		})
	}
}

func usePinnedNode(t *testing.T) {
	t.Helper()
	const nodeBin = "/home/ubuntu/.nvm/versions/node/v22.23.1/bin"
	if _, err := os.Stat(filepath.Join(nodeBin, "node")); err != nil {
		t.Skipf("pinned Node is unavailable: %v", err)
	}
	t.Setenv("PATH", nodeBin+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func replace(value, old, replacement string) string {
	index := strings.Index(value, old)
	if index < 0 {
		panic("fixture substring not found: " + old)
	}
	return value[:index] + replacement + value[index+len(old):]
}

// observedFormula is a strict JSON fixture shaped from the pinned 0.2.5
// artifact. The live compiler tests above are its provenance proof.
const observedFormula = `{"contract":{"name":"lumen.ir","version":"0.2.5","producer":"donbox/formula-language"},"name":"main","input":{"name":"main.input","fields":[{"name":"message","type":{"kind":"atomic","name":"string","origin":{"uri":"main.lumen","line":0,"col":0}},"required":true,"body":false,"origin":{"uri":"main.lumen","line":0,"col":0}}],"origin":{"uri":"main.lumen","line":0,"col":0}},"nodes":[{"kind":"block","id":"block_1","after":[],"origin":{"uri":"main.lumen","line":1,"col":0},"members":[{"kind":"exec","id":"exec_1","after":[],"origin":{"uri":"main.lumen","line":2,"col":0},"interpreter":{"kind":"shell","program":{"kind":"exec"},"origin":{"uri":"main.lumen","line":2,"col":0}},"body":{"raw":"echo {{ message }}","template":{"parts":[{"kind":"text","value":"echo "},{"kind":"interp","expr":{"kind":"ref","name":"message","origin":{"uri":"main.lumen","line":2,"col":0}},"origin":{"uri":"main.lumen","line":2,"col":0}}]},"source":{"kind":"inline"},"templated":true,"language":"bash","syntax":"bare","origin":{"uri":"main.lumen","line":2,"col":0}},"exitMap":{"pass":[0],"retryable":[]},"env":[{"key":"GREETING","value":{"kind":"literal","value":"hello"}}]}]},{"kind":"settle","id":"done","after":["block_1"],"origin":{"uri":"main.lumen","line":3,"col":0},"outcome":"succeeded","value":{"kind":"object","entries":[{"key":"values","value":{"kind":"array","elements":[{"kind":"literal","value":1},{"kind":"ref","name":"message","origin":{"uri":"main.lumen","line":3,"col":0}}]}}]},"publicOutcome":true},{"kind":"settle","id":"degraded","after":["done"],"origin":{"uri":"main.lumen","line":4,"col":0},"outcome":"degraded","reason":"degraded","publicOutcome":true},{"kind":"settle","id":"failed","after":["degraded"],"origin":{"uri":"main.lumen","line":5,"col":0},"outcome":"failed","reason":"failed","publicOutcome":true},{"kind":"settle","id":"skipped","after":["failed"],"origin":{"uri":"main.lumen","line":6,"col":0},"outcome":"skipped","reason":"skipped","publicOutcome":true}],"origin":{"uri":"main.lumen","line":0,"col":0}}`

var observedFirstCohort = observedCompilerResult(observedFormula)

func observedCompilerResult(formula string) string {
	return fmt.Sprintf(`{"formula":%s,"formulas":[%s],"selfStepFormulas":[],"modules":[],"exports":[],"agents":[],"sessions":[],"stepDeclarations":[],"typeAliases":[],"diagnostics":[]}`, formula, formula)
}
