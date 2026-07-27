package program_test

import (
	"errors"
	"math"
	"reflect"
	"testing"

	"github.com/gastownhall/gascity/internal/lumen/program"
)

func TestValidateProgramRejectsInvalidSemanticProgram(t *testing.T) {
	candidate := program.NewProgram(program.NewFormula("", program.NewInput("input", nil), nil))
	if _, err := program.ValidateProgram(candidate); err == nil {
		t.Fatal("ValidateProgram accepted a formula without a name")
	}
}

func TestValidateProgramRejectsUnknownDependency(t *testing.T) {
	exec := program.NewExec("exec_1", []string{"missing"}, program.NewInterpolatedText([]program.TextPart{program.NewText("echo hello")}), nil, nil, nil, program.NewExitMap([]int{0}, nil))
	candidate := program.NewProgram(program.NewFormula("main", program.NewInput("main.input", nil), []program.Step{exec}))
	if _, err := program.ValidateProgram(candidate); err == nil {
		t.Fatal("ValidateProgram accepted an unknown dependency")
	}
}

func TestValidateProgramRejectsDependencyCycle(t *testing.T) {
	first := program.NewExec("first", []string{"second"}, program.NewInterpolatedText([]program.TextPart{program.NewText("echo first")}), nil, nil, nil, program.NewExitMap([]int{0}, nil))
	second := program.NewExec("second", []string{"first"}, program.NewInterpolatedText([]program.TextPart{program.NewText("echo second")}), nil, nil, nil, program.NewExitMap([]int{0}, nil))
	candidate := program.NewProgram(program.NewFormula("main", program.NewInput("main.input", nil), []program.Step{first, second}))
	if _, err := program.ValidateProgram(candidate); err == nil {
		t.Fatal("ValidateProgram accepted a dependency cycle")
	}
}

func TestValidateProgramRejectsUnknownReference(t *testing.T) {
	exec := program.NewExec("exec_1", nil, program.NewInterpolatedText([]program.TextPart{program.NewInterpolation(program.NewReference("missing"))}), nil, nil, nil, program.NewExitMap([]int{0}, nil))
	candidate := program.NewProgram(program.NewFormula("main", program.NewInput("main.input", nil), []program.Step{exec}))
	if _, err := program.ValidateProgram(candidate); err == nil {
		t.Fatal("ValidateProgram accepted an unproved reference")
	}
}

func TestValidateProgramRejectsTypedNilStepWithoutPanicking(t *testing.T) {
	var nilExec *program.Exec
	candidate := program.NewProgram(program.NewFormula("main", program.NewInput("main.input", nil), []program.Step{nilExec}))
	if _, err := program.ValidateProgram(candidate); err == nil {
		t.Fatal("ValidateProgram accepted a typed-nil step")
	}
}

func TestValidateProgramRejectsNonFiniteNumber(t *testing.T) {
	exec := program.NewExec("exec_1", nil, program.NewInterpolatedText([]program.TextPart{program.NewInterpolation(program.NewLiteral(program.Number(math.NaN())))}), nil, nil, nil, program.NewExitMap([]int{0}, nil))
	candidate := program.NewProgram(program.NewFormula("main", program.NewInput("main.input", nil), []program.Step{exec}))
	if _, err := program.ValidateProgram(candidate); err == nil {
		t.Fatal("ValidateProgram accepted a non-finite number")
	}
}

func TestTerminalOutcomesAreClosed(t *testing.T) {
	for _, outcome := range []program.Outcome{
		program.NewSucceeded(program.NewLiteral(program.String("ok"))),
		program.NewDegraded(nil, "degraded"),
		program.NewFailed("failed"),
		program.NewSkipped("skipped"),
	} {
		if _, err := program.ValidateProgram(program.NewProgram(program.NewFormula("main", program.NewInput("main.input", nil), []program.Step{program.NewTerminal("done", "", nil, outcome)}))); err != nil {
			t.Fatalf("ValidateProgram rejected %T: %v", outcome, err)
		}
	}
}

func TestValidateProgramPreservesNormalizedExitMap(t *testing.T) {
	exec := program.NewExec("exec_1", nil, program.NewInterpolatedText([]program.TextPart{program.NewText("echo hello")}), nil, nil, nil, program.NewExitMap([]int{0, 10}, []int{75}))
	decoded, err := program.ValidateProgram(program.NewProgram(program.NewFormula("main", program.NewInput("main.input", nil), []program.Step{exec})))
	if err != nil {
		t.Fatalf("ValidateProgram: %v", err)
	}
	got := decoded.Formula().Steps()[0].(program.Exec).ExitMap()
	if !reflect.DeepEqual(got.Pass(), []int{0, 10}) || !reflect.DeepEqual(got.Retryable(), []int{75}) {
		t.Fatalf("exit map = %#v, want pass [0 10], retryable [75]", got)
	}
}

func TestValidationErrorIsTyped(t *testing.T) {
	_, err := program.ValidateProgram(program.NewProgram(program.NewFormula("", program.NewInput("input", nil), nil)))
	var validationError *program.ValidationError
	if !errors.As(err, &validationError) {
		t.Fatalf("ValidateProgram error = %T, want *ValidationError", err)
	}
}
