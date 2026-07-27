package program_test

import (
	"bytes"
	"reflect"
	"testing"

	"github.com/gastownhall/gascity/internal/lumen/program"
)

func TestProgramSnapshotRoundTripsExactExecLeaf(t *testing.T) {
	candidate := snapshotProgram(t, "terminal-id", "terminal-name", "partial")

	encoded, err := program.EncodeSnapshot(candidate)
	if err != nil {
		t.Fatalf("EncodeSnapshot: %v", err)
	}
	decoded, err := program.DecodeSnapshot(encoded)
	if err != nil {
		t.Fatalf("DecodeSnapshot: %v", err)
	}
	if !reflect.DeepEqual(decoded.Formula(), candidate.Formula()) {
		t.Fatalf("decoded formula = %#v, want %#v", decoded.Formula(), candidate.Formula())
	}
}

func TestProgramSnapshotPreservesDistinctTerminalContent(t *testing.T) {
	first := snapshotProgram(t, "terminal-a", "first", "partial-a")
	second := snapshotProgram(t, "terminal-b", "second", "partial-b")

	firstEncoded, err := program.EncodeSnapshot(first)
	if err != nil {
		t.Fatal(err)
	}
	secondEncoded, err := program.EncodeSnapshot(second)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(firstEncoded, secondEncoded) {
		t.Fatal("distinct terminal content produced identical snapshots")
	}
	decoded, err := program.DecodeSnapshot(secondEncoded)
	if err != nil {
		t.Fatal(err)
	}
	terminal := decoded.Formula().Steps()[1].(program.Terminal)
	degraded := terminal.Outcome().(program.Degraded)
	if terminal.ID() != "terminal-b" || terminal.Name() != "second" ||
		!reflect.DeepEqual(terminal.Dependencies(), []string{"block"}) ||
		degraded.Reason() != "partial-b" {
		t.Fatalf("decoded terminal = %#v / %#v", terminal, degraded)
	}
}

func TestProgramSnapshotDecodeFailsClosed(t *testing.T) {
	encoded, err := program.EncodeSnapshot(snapshotProgram(t, "terminal", "result", "partial"))
	if err != nil {
		t.Fatal(err)
	}
	unknown := append(append([]byte(nil), encoded[:len(encoded)-1]...), []byte(`,"unknown":true}`)...)
	for name, raw := range map[string][]byte{
		"unknown field": unknown,
		"trailing data": append(append([]byte(nil), encoded...), []byte(`{}`)...),
		"missing shape": []byte(`{}`),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := program.DecodeSnapshot(raw); err == nil {
				t.Fatalf("DecodeSnapshot accepted %s", raw)
			}
		})
	}
}

func TestProgramSnapshotRejectsValidProgramOutsideExecLeafCohort(t *testing.T) {
	candidate, err := program.ValidateProgram(program.NewProgram(program.NewFormula(
		"main",
		program.NewInput("main.input", nil),
		[]program.Step{program.NewTerminal("done", "", nil, program.NewSucceeded(program.NewLiteral(program.String("done"))))},
	)))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := program.EncodeSnapshot(candidate); err == nil {
		t.Fatal("EncodeSnapshot accepted non-exec-leaf program")
	}
}

func snapshotProgram(t *testing.T, terminalID, terminalName, reason string) program.Program {
	t.Helper()
	exec := program.NewExec(
		"exec",
		nil,
		program.NewInterpolatedText([]program.TextPart{
			program.NewText("printf "),
			program.NewInterpolation(program.NewReference("message")),
		}),
		program.NewLiteral(program.String("work")),
		[]program.Environment{
			program.NewEnvironment("FLAG", program.NewLiteral(program.Boolean(true))),
		},
		program.NewLiteral(program.Null{}),
		program.NewExitMap([]int{0, 10}, []int{75}),
	)
	degradedValue := program.NewRecord([]program.RecordEntry{
		program.NewRecordEntry("items", program.NewArray([]program.Expr{
			program.NewLiteral(program.Number(1)),
			program.NewReference("message"),
		})),
	})
	candidate, err := program.ValidateProgram(program.NewProgram(program.NewFormula(
		"main",
		program.NewInput("main.input", []program.Field{
			program.NewField("message", program.NewAtomicType("string"), true),
			program.NewField("nested", program.NewArrayType(program.NewRecordType([]program.Field{
				program.NewField("enabled", program.NewAtomicType("bool"), true),
			})), true),
		}),
		[]program.Step{
			program.NewBlock("block", nil, []program.Step{exec}),
			program.NewTerminal(terminalID, terminalName, []string{"block"}, program.NewDegraded(degradedValue, reason)),
		},
	)))
	if err != nil {
		t.Fatal(err)
	}
	return candidate
}
