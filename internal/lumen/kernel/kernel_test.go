package kernel_test

import (
	"errors"
	"strconv"
	"testing"

	"github.com/gastownhall/gascity/internal/lumen/kernel"
	"github.com/gastownhall/gascity/internal/lumen/program"
)

func TestFoldRendersBlockExecAndNeverReissuesCommittedCommand(t *testing.T) {
	formula := blockExecProgram(t, nil)
	records := []kernel.Record{kernel.NewGenesis("host-private", []kernel.InputBinding{kernel.NewInputBinding("message", program.String("world"))})}
	state, err := kernel.Fold(formula, records)
	if err != nil {
		t.Fatalf("Fold genesis: %v", err)
	}
	command, ok := state.Command()
	if !ok {
		t.Fatal("Fold genesis derived no command")
	}
	if command.HostRunKey() != "host-private" || command.PrivateSequence() != 1 || command.StepID() != "exec_1" {
		t.Fatalf("command correlation = %#v, want host-private/1/exec_1", command)
	}
	cwd, hasCWD := command.CWD()
	stdin, hasStdin := command.Stdin()
	if command.Script() != "echo world" || !hasCWD || cwd != "/tmp" || !hasStdin || stdin != "world" {
		t.Fatalf("rendered command = %#v, want script/cwd/stdin", command)
	}
	environment := command.Environment()
	if len(environment) != 2 || environment[0].Key() != "GREETING" || environment[0].Value() != "world" || environment[0].Remove() || environment[1].Key() != "REMOVE" || !environment[1].Remove() {
		t.Fatalf("rendered environment = %#v", environment)
	}

	records = append(records, kernel.NewCommandIssued("host-private", 1, "exec_1"))
	state, err = kernel.Fold(formula, records)
	if err != nil {
		t.Fatalf("Fold command issued: %v", err)
	}
	if _, ok := state.Command(); ok {
		t.Fatal("Fold reissued a committed command")
	}
}

func TestFoldPreservesAbsentExecOptionals(t *testing.T) {
	exec := program.NewExec("exec_1", nil, program.NewInterpolatedText([]program.TextPart{program.NewText("echo ordinary")}), nil, nil, nil, program.NewExitMap([]int{0}, nil))
	formula, err := program.ValidateProgram(program.NewProgram(program.NewFormula("main", program.NewInput("main.input", nil), []program.Step{program.NewBlock("block_1", nil, []program.Step{exec})})))
	if err != nil {
		t.Fatalf("ValidateProgram: %v", err)
	}

	state, err := kernel.Fold(formula, []kernel.Record{kernel.NewGenesis("host-private", nil)})
	if err != nil {
		t.Fatalf("Fold genesis: %v", err)
	}
	command, ok := state.Command()
	if !ok {
		t.Fatal("Fold genesis derived no command")
	}
	if cwd, ok := command.CWD(); ok || cwd != "" {
		t.Fatalf("CWD() = (%q, %t), want absent", cwd, ok)
	}
	if stdin, ok := command.Stdin(); ok || stdin != "" {
		t.Fatalf("Stdin() = (%q, %t), want absent", stdin, ok)
	}
	if environment := command.Environment(); len(environment) != 0 {
		t.Fatalf("Environment() = %#v, want absent", environment)
	}
}

func TestFoldDistinguishesEmptyAndNullExecOptionals(t *testing.T) {
	tests := []struct {
		name      string
		cwd       program.Expr
		stdin     program.Expr
		wantCWD   bool
		wantStdin bool
	}{
		{name: "empty strings are present", cwd: program.NewLiteral(program.String("")), stdin: program.NewLiteral(program.String("")), wantCWD: true, wantStdin: true},
		{name: "null omits both", cwd: program.NewLiteral(program.Null{}), stdin: program.NewLiteral(program.Null{})},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			exec := program.NewExec("exec_1", nil, program.NewInterpolatedText([]program.TextPart{program.NewText("echo ordinary")}), test.cwd, nil, test.stdin, program.NewExitMap([]int{0}, nil))
			formula, err := program.ValidateProgram(program.NewProgram(program.NewFormula("main", program.NewInput("main.input", nil), []program.Step{program.NewBlock("block_1", nil, []program.Step{exec})})))
			if err != nil {
				t.Fatalf("ValidateProgram: %v", err)
			}
			state, err := kernel.Fold(formula, []kernel.Record{kernel.NewGenesis("host-private", nil)})
			if err != nil {
				t.Fatalf("Fold genesis: %v", err)
			}
			command, ok := state.Command()
			if !ok {
				t.Fatal("Fold genesis derived no command")
			}
			if cwd, ok := command.CWD(); cwd != "" || ok != test.wantCWD {
				t.Fatalf("CWD() = (%q, %t), want (empty, %t)", cwd, ok, test.wantCWD)
			}
			if stdin, ok := command.Stdin(); stdin != "" || ok != test.wantStdin {
				t.Fatalf("Stdin() = (%q, %t), want (empty, %t)", stdin, ok, test.wantStdin)
			}
		})
	}
}

func TestFoldRendersNullInterpolationAsEmpty(t *testing.T) {
	exec := program.NewExec("exec_1", nil, program.NewInterpolatedText([]program.TextPart{
		program.NewText("echo "),
		program.NewInterpolation(program.NewLiteral(program.Null{})),
	}), nil, nil, nil, program.NewExitMap([]int{0}, nil))
	formula, err := program.ValidateProgram(program.NewProgram(program.NewFormula(
		"main",
		program.NewInput("main.input", nil),
		[]program.Step{program.NewBlock("block_1", nil, []program.Step{exec})},
	)))
	if err != nil {
		t.Fatalf("ValidateProgram: %v", err)
	}

	state, err := kernel.Fold(formula, []kernel.Record{kernel.NewGenesis("host-private", nil)})
	if err != nil {
		t.Fatalf("Fold genesis: %v", err)
	}
	command, ok := state.Command()
	if !ok || command.Script() != "echo " {
		t.Fatalf("Command() = (%#v, %t), want script %q", command, ok, "echo ")
	}
}

func TestFoldProjectsPassAndEveryNonPassIntoExactRuntimeValues(t *testing.T) {
	tests := []struct {
		name      string
		exitCode  int
		pass      []int
		retryable []int
		wantFail  bool
	}{
		{name: "pass", exitCode: 0},
		{name: "retryable remains failed", exitCode: 75, retryable: []int{75}, wantFail: true},
		{name: "custom pass", exitCode: 10, pass: []int{10}},
		{name: "ordinary failure", exitCode: 1, wantFail: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			pass := test.pass
			if pass == nil {
				pass = []int{0}
			}
			formula := blockExecProgramWithPass(t, pass, test.retryable, nil)
			records := []kernel.Record{
				kernel.NewGenesis("host-private", []kernel.InputBinding{kernel.NewInputBinding("message", program.String("world"))}),
				kernel.NewCommandIssued("host-private", 1, "exec_1"),
				kernel.NewObservation("host-private", 1, "stdout", "stderr", kernel.ExitTermination(test.exitCode)),
			}
			state, err := kernel.Fold(formula, records)
			if err != nil {
				t.Fatalf("Fold: %v", err)
			}
			pending, ok := state.PendingTerminal()
			if !ok {
				t.Fatal("Fold did not derive a pending terminal append")
			}
			if test.wantFail {
				outcome, ok := pending.Outcome().(kernel.Failed)
				if !ok || outcome.Reason() != "exit_"+strconv.Itoa(test.exitCode) {
					t.Fatalf("pending outcome = %#v, want failed exit_%d", pending.Outcome(), test.exitCode)
				}
				failure := outcome.Detail()
				if failure.Stdout() != "stdout" || failure.Stderr() != "stderr" || failure.Termination() != kernel.ExitTermination(test.exitCode) {
					t.Fatalf("ExecFailure = %#v", failure)
				}
			} else {
				outcome, ok := pending.Outcome().(kernel.Succeeded)
				if !ok {
					t.Fatalf("pending outcome = %T, want kernel.Succeeded", pending.Outcome())
				}
				result := outcome.Result()
				if result.Stdout() != "stdout" || result.Stderr() != "stderr" || result.Termination() != kernel.ExitTermination(test.exitCode) {
					t.Fatalf("ExecResult = %#v", result)
				}
			}
			state, err = kernel.Fold(formula, append(records, pending))
			if err != nil || !state.Terminal() {
				t.Fatalf("Fold committed terminal = (%#v, %v)", state, err)
			}
			if _, ok := state.Command(); ok {
				t.Fatal("terminal refold reissued a command")
			}
		})
	}
}

func TestFoldRejectsUnscopedAuthoredDegradation(t *testing.T) {
	exec := program.NewExec("exec_1", nil, program.NewInterpolatedText([]program.TextPart{program.NewText("echo okay")}), nil, nil, nil, program.NewExitMap([]int{0}, nil))
	formula, err := program.ValidateProgram(program.NewProgram(program.NewFormula("main", program.NewInput("main.input", nil), []program.Step{
		program.NewBlock("block_1", nil, []program.Step{exec}),
		program.NewTerminal("unscoped", "", nil, program.NewDegraded(nil, "unrelated")),
	})))
	if err != nil {
		t.Fatalf("ValidateProgram: %v", err)
	}
	if _, err := kernel.Fold(formula, []kernel.Record{kernel.NewGenesis("host-private", nil)}); err == nil {
		t.Fatal("Fold accepted an unscoped authored degradation")
	}
}

func TestFoldRejectsProgramsWithoutExactlyOneExec(t *testing.T) {
	first := program.NewExec("first", nil, program.NewInterpolatedText([]program.TextPart{program.NewText("echo first")}), nil, nil, nil, program.NewExitMap([]int{0}, nil))
	second := program.NewExec("second", nil, program.NewInterpolatedText([]program.TextPart{program.NewText("echo second")}), nil, nil, nil, program.NewExitMap([]int{0}, nil))
	candidates := map[string]program.Program{
		"zero":     program.NewProgram(program.NewFormula("main", program.NewInput("main.input", nil), []program.Step{program.NewTerminal("done", "", nil, program.NewSucceeded(program.NewLiteral(program.String("unused"))))})),
		"multiple": program.NewProgram(program.NewFormula("main", program.NewInput("main.input", nil), []program.Step{program.NewBlock("block_1", nil, []program.Step{first, second})})),
	}
	for name, candidate := range candidates {
		t.Run(name, func(t *testing.T) {
			formula, err := program.ValidateProgram(candidate)
			if err != nil {
				t.Fatalf("ValidateProgram: %v", err)
			}
			_, err = kernel.Fold(formula, []kernel.Record{kernel.NewGenesis("host-private", nil)})
			if err == nil {
				t.Fatal("Fold accepted a program without exactly one exec step")
			}
			var foldError *kernel.FoldError
			if !errors.As(err, &foldError) {
				t.Fatalf("Fold error = %T, want *FoldError", err)
			}
		})
	}
}

func TestFoldRejectsExpressionsAndAuthoredOutcomesOutsideTheLeafSlice(t *testing.T) {
	structuralExec := program.NewExec("exec_1", nil, program.NewInterpolatedText([]program.TextPart{
		program.NewInterpolation(program.NewArray([]program.Expr{program.NewLiteral(program.String("later"))})),
	}), nil, nil, nil, program.NewExitMap([]int{0}, nil))
	ordinaryExec := program.NewExec("exec_1", nil, program.NewInterpolatedText([]program.TextPart{
		program.NewText("echo okay"),
	}), nil, nil, nil, program.NewExitMap([]int{0}, nil))
	tests := map[string][]program.Step{
		"structural interpolation": {
			program.NewBlock("block_1", nil, []program.Step{structuralExec}),
		},
		"authored success": {
			program.NewBlock("block_1", nil, []program.Step{ordinaryExec}),
			program.NewTerminal("done", "", []string{"block_1"}, program.NewSucceeded(program.NewLiteral(program.String("done")))),
		},
		"authored failure": {
			program.NewBlock("block_1", nil, []program.Step{ordinaryExec}),
			program.NewTerminal("done", "", []string{"block_1"}, program.NewFailed("failed")),
		},
		"authored skip": {
			program.NewBlock("block_1", nil, []program.Step{ordinaryExec}),
			program.NewTerminal("done", "", []string{"block_1"}, program.NewSkipped("skipped")),
		},
	}
	for name, steps := range tests {
		t.Run(name, func(t *testing.T) {
			formula, err := program.ValidateProgram(program.NewProgram(program.NewFormula("main", program.NewInput("main.input", nil), steps)))
			if err != nil {
				t.Fatalf("ValidateProgram: %v", err)
			}
			if _, err := kernel.Fold(formula, []kernel.Record{kernel.NewGenesis("host-private", nil)}); err == nil {
				t.Fatal("Fold accepted semantics outside the admitted exec leaf")
			}
		})
	}
}

func TestFoldProjectsAuthoredDegradationOntoSucceeded(t *testing.T) {
	formula := blockExecProgram(t, program.NewDegraded(nil, "partial success"))
	records := []kernel.Record{
		kernel.NewGenesis("host-private", []kernel.InputBinding{kernel.NewInputBinding("message", program.String("world"))}),
		kernel.NewCommandIssued("host-private", 1, "exec_1"),
		kernel.NewObservation("host-private", 1, "", "", kernel.ExitTermination(0)),
	}
	state, err := kernel.Fold(formula, records)
	if err != nil {
		t.Fatalf("Fold: %v", err)
	}
	pending, ok := state.PendingTerminal()
	if !ok {
		t.Fatal("Fold did not derive a pending terminal append")
	}
	succeeded, ok := pending.Outcome().(kernel.Succeeded)
	if !ok || succeeded.Degradation() == nil || succeeded.Degradation().Reason() != "partial success" {
		t.Fatalf("pending outcome = %#v, want succeeded with degradation", pending.Outcome())
	}
}

func TestFoldPreservesSignalAndSpawnFailureDetails(t *testing.T) {
	tests := []struct {
		name        string
		termination kernel.ExecTermination
		reason      string
	}{
		{name: "signal", termination: kernel.SignalTermination("TERM"), reason: "signal"},
		{name: "spawn error", termination: kernel.SpawnErrorTermination("executable not found"), reason: "not_executable"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			formula := blockExecProgram(t, nil)
			state, err := kernel.Fold(formula, []kernel.Record{
				kernel.NewGenesis("host-private", []kernel.InputBinding{kernel.NewInputBinding("message", program.String("world"))}),
				kernel.NewCommandIssued("host-private", 1, "exec_1"),
				kernel.NewObservation("host-private", 1, "stdout", "stderr", test.termination),
			})
			if err != nil {
				t.Fatalf("Fold: %v", err)
			}
			pending, ok := state.PendingTerminal()
			if !ok {
				t.Fatal("Fold did not derive a pending terminal append")
			}
			failed, ok := pending.Outcome().(kernel.Failed)
			if !ok || failed.Reason() != test.reason {
				t.Fatalf("pending outcome = %#v, want failed %q", pending.Outcome(), test.reason)
			}
			detail := failed.Detail()
			if detail.Stdout() != "stdout" || detail.Stderr() != "stderr" || detail.Termination() != test.termination {
				t.Fatalf("ExecFailure = %#v", detail)
			}
		})
	}
}

func TestRuntimeOutcomeAndTerminationFamiliesAreClosed(t *testing.T) {
	skipped := kernel.NewSkipped("guard")
	canceled := kernel.NewCanceled("operator")
	if skipped.Reason() != "guard" || canceled.Reason() != "operator" {
		t.Fatalf("terminal reasons = (%q, %q)", skipped.Reason(), canceled.Reason())
	}
	for _, outcome := range []kernel.Outcome{kernel.Succeeded{}, kernel.Failed{}, skipped, canceled} {
		if outcome == nil {
			t.Fatal("runtime outcome arm is nil")
		}
	}
	for _, termination := range []kernel.ExecTermination{kernel.ExitTermination(0), kernel.SignalTermination("TERM"), kernel.SpawnErrorTermination("not found")} {
		if termination == nil {
			t.Fatal("exec termination arm is nil")
		}
	}
}

func TestFoldRejectsCorruptPrivateHistory(t *testing.T) {
	formula := blockExecProgram(t, nil)
	genesis := kernel.NewGenesis("host-private", []kernel.InputBinding{kernel.NewInputBinding("message", program.String("world"))})
	issued := kernel.NewCommandIssued("host-private", 1, "exec_1")
	observed := kernel.NewObservation("host-private", 1, "", "", kernel.ExitTermination(0))
	state, err := kernel.Fold(formula, []kernel.Record{genesis, issued, observed})
	if err != nil {
		t.Fatalf("Fold setup: %v", err)
	}
	pending, _ := state.PendingTerminal()
	tests := map[string][]kernel.Record{
		"duplicate genesis":             {genesis, genesis},
		"command host mismatch":         {genesis, kernel.NewCommandIssued("other", 1, "exec_1")},
		"command sequence mismatch":     {genesis, kernel.NewCommandIssued("host-private", 2, "exec_1")},
		"command step mismatch":         {genesis, kernel.NewCommandIssued("host-private", 1, "other")},
		"duplicate command":             {genesis, issued, issued},
		"observation before issue":      {genesis, observed},
		"observation host mismatch":     {genesis, issued, kernel.NewObservation("other", 1, "", "", kernel.ExitTermination(0))},
		"observation sequence mismatch": {genesis, issued, kernel.NewObservation("host-private", 2, "", "", kernel.ExitTermination(0))},
		"duplicate observation":         {genesis, issued, observed, observed},
		"terminal host mismatch":        {genesis, issued, observed, kernel.NewTerminal("other", 1, pending.Outcome())},
		"terminal sequence mismatch":    {genesis, issued, observed, kernel.NewTerminal("host-private", 2, pending.Outcome())},
		"mismatched terminal":           {genesis, issued, observed, kernel.NewTerminal("host-private", 1, kernel.Failed{})},
		"reopen":                        {genesis, issued, observed, pending, observed},
	}
	for name, records := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := kernel.Fold(formula, records)
			if err == nil {
				t.Fatal("Fold accepted corrupt journal")
			}
			var foldError *kernel.FoldError
			if !errors.As(err, &foldError) {
				t.Fatalf("Fold error = %T, want *FoldError", err)
			}
		})
	}
}

func TestFoldRejectsInvalidObservationsAndGenesisBindings(t *testing.T) {
	formula := blockExecProgram(t, nil)
	genesis := kernel.NewGenesis("host-private", []kernel.InputBinding{kernel.NewInputBinding("message", program.String("world"))})
	issued := kernel.NewCommandIssued("host-private", 1, "exec_1")
	tests := map[string][]kernel.Record{
		"nil termination":          {genesis, issued, kernel.NewObservation("host-private", 1, "", "", nil)},
		"negative exit":            {genesis, issued, kernel.NewObservation("host-private", 1, "", "", kernel.ExitTermination(-1))},
		"empty signal":             {genesis, issued, kernel.NewObservation("host-private", 1, "", "", kernel.SignalTermination(""))},
		"empty spawn error":        {genesis, issued, kernel.NewObservation("host-private", 1, "", "", kernel.SpawnErrorTermination(""))},
		"missing required binding": {kernel.NewGenesis("host-private", nil)},
		"unknown binding": {kernel.NewGenesis("host-private", []kernel.InputBinding{
			kernel.NewInputBinding("message", program.String("world")),
			kernel.NewInputBinding("other", program.String("surplus")),
		})},
		"duplicate required binding": {kernel.NewGenesis("host-private", []kernel.InputBinding{
			kernel.NewInputBinding("message", program.String("first")),
			kernel.NewInputBinding("message", program.String("second")),
		})},
	}
	for name, records := range tests {
		t.Run(name, func(t *testing.T) {
			state, err := kernel.Fold(formula, records)
			if err == nil {
				t.Fatalf("Fold = (%#v, nil), want rejected journal", state)
			}
			var foldError *kernel.FoldError
			if !errors.As(err, &foldError) {
				t.Fatalf("Fold error = %T, want *FoldError", err)
			}
		})
	}
}

func blockExecProgram(t *testing.T, terminal program.Outcome) program.Program {
	t.Helper()
	return blockExecProgramWithPass(t, []int{0}, nil, terminal)
}

func blockExecProgramWithPass(t *testing.T, pass, retryable []int, terminal program.Outcome) program.Program {
	t.Helper()
	exec := program.NewExec("exec_1", nil, program.NewInterpolatedText([]program.TextPart{program.NewText("echo "), program.NewInterpolation(program.NewReference("message"))}), program.NewLiteral(program.String("/tmp")), []program.Environment{program.NewEnvironment("GREETING", program.NewReference("message")), program.NewEnvironment("REMOVE", program.NewLiteral(program.Null{}))}, program.NewReference("message"), program.NewExitMap(pass, retryable))
	steps := []program.Step{program.NewBlock("block_1", nil, []program.Step{exec})}
	if terminal != nil {
		steps = append(steps, program.NewTerminal("terminal_1", "", []string{"block_1"}, terminal))
	}
	validated, err := program.ValidateProgram(program.NewProgram(program.NewFormula("main", program.NewInput("main.input", []program.Field{program.NewField("message", program.NewAtomicType("string"), true)}), steps)))
	if err != nil {
		t.Fatalf("ValidateProgram: %v", err)
	}
	return validated
}
