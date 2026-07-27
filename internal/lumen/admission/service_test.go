//go:build !windows

package admission_test

import (
	"context"
	"errors"
	"math"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/graphstore"
	"github.com/gastownhall/gascity/internal/lumen/admission"
	"github.com/gastownhall/gascity/internal/lumen/exechost"
	"github.com/gastownhall/gascity/internal/lumen/kernel"
	"github.com/gastownhall/gascity/internal/lumen/program"
	"github.com/gastownhall/gascity/internal/testutil"
)

func TestAdmissionCapturesInputsAndCompletesExactOutcome(t *testing.T) {
	ctx := context.Background()
	store := openStore(t)
	service := newService(store)
	submission := testSubmission("echo ")
	result, err := service.Admit(ctx, submission)
	if err != nil {
		t.Fatalf("Admit: %v", err)
	}
	submission.Inputs[0] = admission.NewInput("message", admission.NewStringValue("changed"))
	completed, err := service.Advance(ctx, result.HostRunKey)
	if err != nil {
		t.Fatalf("Advance: %v", err)
	}
	succeeded, ok := completed.Outcome.(kernel.Succeeded)
	if !ok {
		t.Fatalf("outcome = %T, want kernel.Succeeded", completed.Outcome)
	}
	if got := succeeded.Result().Stdout(); got != "hello\n" {
		t.Fatalf("stdout = %q, want hello", got)
	}
}

func TestAdmissionFreshIdenticalSubmissionsMintDistinctPrivateKeys(t *testing.T) {
	ctx := context.Background()
	service := newService(openStore(t))
	first, err := service.Admit(ctx, testSubmission("true"))
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.Admit(ctx, testSubmission("true"))
	if err != nil {
		t.Fatal(err)
	}
	if first.HostRunKey == second.HostRunKey {
		t.Fatalf("fresh keys = %q and %q, want distinct", first.HostRunKey, second.HostRunKey)
	}
}

func TestAdmissionIdempotencyReplaysOnlyTheSameSubmission(t *testing.T) {
	ctx := context.Background()
	service := newService(openStore(t))
	submission := testSubmission("true")
	submission.IdempotencyKey = "retry-1"
	first, err := service.Admit(ctx, submission)
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.Admit(ctx, submission)
	if err != nil {
		t.Fatal(err)
	}
	if first.HostRunKey != second.HostRunKey || !second.Replayed {
		t.Fatalf("replay = %#v, want original key %q", second, first.HostRunKey)
	}
	submission = testSubmission("false")
	submission.IdempotencyKey = "retry-1"
	if _, err := service.Admit(ctx, submission); !errors.Is(err, admission.ErrIdempotencyConflict) {
		t.Fatalf("conflicting replay error = %v", err)
	}
}

func TestAdmissionIdempotencyConflictsOnDistinctTerminalIdentity(t *testing.T) {
	ctx := context.Background()
	service := newService(openStore(t))
	base := testSubmission("true")
	first := base
	first.IdempotencyKey = "terminal-identity"
	first.Program = withDegradedTerminal(first.Program, "terminal-a", "first", "partial")
	if _, err := service.Admit(ctx, first); err != nil {
		t.Fatal(err)
	}
	second := first
	second.Program = withDegradedTerminal(base.Program, "terminal-b", "second", "partial")
	if _, err := service.Admit(ctx, second); !errors.Is(err, admission.ErrIdempotencyConflict) {
		t.Fatalf("distinct terminal identity error = %v, want idempotency conflict", err)
	}
}

func TestAdmissionRetainsSubmittedSourceByValue(t *testing.T) {
	ctx := context.Background()
	service := newService(openStore(t))
	submission := testSubmission("true")
	submission.IdempotencyKey = "source-snapshot"
	if _, err := service.Admit(ctx, submission); err != nil {
		t.Fatal(err)
	}
	submission.Source[0] = 'X'
	if _, err := service.Admit(ctx, submission); !errors.Is(err, admission.ErrIdempotencyConflict) {
		t.Fatalf("mutated source replay error = %v, want idempotency conflict", err)
	}
}

func TestAdmissionRejectsIdempotencyReplayWithDifferentEffectEnvironment(t *testing.T) {
	ctx := context.Background()
	service := newService(openStore(t))
	submission := testSubmission("true")
	submission.IdempotencyKey = "environment-conflict"
	submission.EffectEnvironment = []string{"MODE=first"}
	if _, err := service.Admit(ctx, submission); err != nil {
		t.Fatal(err)
	}
	submission.EffectEnvironment = []string{"MODE=second"}
	if _, err := service.Admit(ctx, submission); !errors.Is(err, admission.ErrIdempotencyConflict) {
		t.Fatalf("environment conflict error = %v, want idempotency conflict", err)
	}
}

func TestAdmissionRejectsInvalidProgramWithoutCreatingAStream(t *testing.T) {
	ctx := context.Background()
	store := openStore(t)
	service := newService(store)
	invalid := admission.RunSubmission{
		Source: []byte("invalid"), Program: program.NewProgram(program.NewFormula("", program.NewInput("input", nil), nil)),
		WorkingDirectory: "/", IdempotencyKey: "invalid-program",
	}
	if _, err := service.Admit(ctx, invalid); err == nil {
		t.Fatal("Admit accepted invalid program")
	}
	corrected := testSubmission("true")
	corrected.IdempotencyKey = invalid.IdempotencyKey
	if _, err := service.Admit(ctx, corrected); err != nil {
		t.Fatalf("corrected admission after invalid program: %v", err)
	}
}

func TestAdmissionRejectsEmptySourceAndRelativeWorkingDirectory(t *testing.T) {
	ctx := context.Background()
	service := newService(openStore(t))
	empty := testSubmission("true")
	empty.Source = []byte{}
	if _, err := service.Admit(ctx, empty); err == nil {
		t.Fatal("Admit accepted empty source")
	}
	relative := testSubmission("true")
	relative.WorkingDirectory = "."
	if _, err := service.Admit(ctx, relative); err == nil {
		t.Fatal("Admit accepted relative working directory")
	}
}

func TestAdmissionRejectsTypedInputBeforeItClaimsIdempotency(t *testing.T) {
	ctx := context.Background()
	service := newService(openStore(t))
	exec := program.NewExec("exec", nil, program.NewInterpolatedText([]program.TextPart{program.NewText("true")}), nil, nil, nil, program.NewExitMap([]int{0}, nil))
	bad := admission.RunSubmission{Source: []byte("true"), Program: program.NewProgram(program.NewFormula("run", program.NewInput("run.input", []program.Field{program.NewField("count", program.NewAtomicType("number"), true)}), []program.Step{program.NewBlock("block", nil, []program.Step{exec})})), Inputs: []admission.Input{admission.NewInput("count", admission.NewStringValue("one"))}, WorkingDirectory: "/", IdempotencyKey: "typed"}
	if _, err := service.Admit(ctx, bad); err == nil {
		t.Fatal("Admit accepted a string for number input")
	}
	bad.Inputs = []admission.Input{admission.NewInput("count", admission.NewNumberValue(1))}
	if _, err := service.Admit(ctx, bad); err != nil {
		t.Fatalf("valid retry after rejected admission: %v", err)
	}
}

func TestAdmissionUsesExactAtomicNamesAndRejectsNonFiniteNumbersBeforeClaim(t *testing.T) {
	ctx := context.Background()
	service := newService(openStore(t))
	exec := program.NewExec("exec", nil, program.NewInterpolatedText([]program.TextPart{program.NewText("true")}), nil, nil, nil, program.NewExitMap([]int{0}, nil))
	boolean := admission.RunSubmission{Source: []byte("true"), Program: program.NewProgram(program.NewFormula("run", program.NewInput("run.input", []program.Field{program.NewField("flag", program.NewAtomicType("boolean"), true)}), []program.Step{program.NewBlock("block", nil, []program.Step{exec})})), Inputs: []admission.Input{admission.NewInput("flag", admission.NewBooleanValue(true))}, WorkingDirectory: "/"}
	if _, err := service.Admit(ctx, boolean); err == nil {
		t.Fatal("Admit accepted invented boolean atomic type")
	}
	number := admission.RunSubmission{Source: []byte("true"), Program: program.NewProgram(program.NewFormula("run", program.NewInput("run.input", []program.Field{program.NewField("count", program.NewAtomicType("number"), true)}), []program.Step{program.NewBlock("block", nil, []program.Step{exec})})), Inputs: []admission.Input{admission.NewInput("count", admission.NewNumberValue(math.NaN()))}, WorkingDirectory: "/", IdempotencyKey: "finite"}
	if _, err := service.Admit(ctx, number); err == nil {
		t.Fatal("Admit accepted non-finite number")
	}
	number.Inputs = []admission.Input{admission.NewInput("count", admission.NewNumberValue(1))}
	if _, err := service.Admit(ctx, number); err != nil {
		t.Fatalf("finite retry after rejected admission: %v", err)
	}
	null := admission.RunSubmission{Source: []byte("true"), Program: program.NewProgram(program.NewFormula("run", program.NewInput("run.input", []program.Field{program.NewField("nothing", program.NewAtomicType("null"), true)}), []program.Step{program.NewBlock("block", nil, []program.Step{exec})})), Inputs: []admission.Input{admission.NewInput("nothing", admission.NewNullValue())}, WorkingDirectory: "/"}
	if _, err := service.Admit(ctx, null); err != nil {
		t.Fatalf("null input: %v", err)
	}
}

func TestAdmissionRejectsUnsupportedOmittedOptionalInputBeforeClaim(t *testing.T) {
	ctx := context.Background()
	exec := program.NewExec(
		"exec", nil,
		program.NewInterpolatedText([]program.TextPart{program.NewText("true")}),
		nil, nil, nil, program.NewExitMap([]int{0}, nil),
	)
	for name, typ := range map[string]program.Type{
		"unknown atomic": program.NewAtomicType("boolean"),
		"record": program.NewRecordType([]program.Field{
			program.NewField("value", program.NewAtomicType("string"), true),
		}),
	} {
		t.Run(name, func(t *testing.T) {
			service := newService(openStore(t))
			bad := admission.RunSubmission{
				Source: []byte("true"),
				Program: program.NewProgram(program.NewFormula(
					"run",
					program.NewInput("run.input", []program.Field{
						program.NewField("unsupported", typ, false),
					}),
					[]program.Step{program.NewBlock("block", nil, []program.Step{exec})},
				)),
				WorkingDirectory: "/",
				IdempotencyKey:   "unsupported-optional-" + name,
			}
			if _, err := service.Admit(ctx, bad); err == nil {
				t.Fatal("Admit accepted an unsupported omitted optional input")
			}

			corrected := testSubmission("true")
			corrected.IdempotencyKey = bad.IdempotencyKey
			if _, err := service.Admit(ctx, corrected); err != nil {
				t.Fatalf("valid retry after rejected admission: %v", err)
			}
		})
	}
}

func TestAdmissionCanonicalizesBindingsInFormulaOrder(t *testing.T) {
	ctx := context.Background()
	service := newService(openStore(t))
	exec := program.NewExec("exec", nil, program.NewInterpolatedText([]program.TextPart{program.NewText("true")}), nil, nil, nil, program.NewExitMap([]int{0}, nil))
	program := program.NewProgram(program.NewFormula("run", program.NewInput("run.input", []program.Field{program.NewField("first", program.NewAtomicType("string"), true), program.NewField("second", program.NewAtomicType("string"), true)}), []program.Step{program.NewBlock("block", nil, []program.Step{exec})}))
	first := admission.RunSubmission{Source: []byte("true"), Program: program, Inputs: []admission.Input{admission.NewInput("first", admission.NewStringValue("one")), admission.NewInput("second", admission.NewStringValue("two"))}, WorkingDirectory: "/", IdempotencyKey: "ordered"}
	result, err := service.Admit(ctx, first)
	if err != nil {
		t.Fatal(err)
	}
	first.Inputs[0], first.Inputs[1] = first.Inputs[1], first.Inputs[0]
	replay, err := service.Admit(ctx, first)
	if err != nil || !replay.Replayed || replay.HostRunKey != result.HostRunKey {
		t.Fatalf("reordered replay = %#v, %v", replay, err)
	}
}

func TestAdmissionCapturesEffectEnvironmentByValue(t *testing.T) {
	ctx := context.Background()
	service := newService(openStore(t))
	submission := testSubmission("printf \"$ADMISSION_ENV\"")
	submission.EffectEnvironment = []string{"ADMISSION_ENV=before"}
	result, err := service.Admit(ctx, submission)
	if err != nil {
		t.Fatal(err)
	}
	submission.EffectEnvironment[0] = "ADMISSION_ENV=after"
	completed, err := service.Advance(ctx, result.HostRunKey)
	if err != nil {
		t.Fatal(err)
	}
	if got := completed.Outcome.(kernel.Succeeded).Result().Stdout(); got != "before" {
		t.Fatalf("captured environment output = %q, want before", got)
	}
}

func TestAdmissionCapturesHomeUsedForWorkingDirectory(t *testing.T) {
	ctx := context.Background()
	service := newService(openStore(t))
	home := t.TempDir()
	other := t.TempDir()
	exec := program.NewExec("exec", nil, program.NewInterpolatedText([]program.TextPart{program.NewText("pwd")}), program.NewLiteral(program.String("$HOME")), nil, nil, program.NewExitMap([]int{0}, nil))
	submission := admission.RunSubmission{Source: []byte("pwd"), Program: program.NewProgram(program.NewFormula("run", program.NewInput("run.input", nil), []program.Step{program.NewBlock("block", nil, []program.Step{exec})})), EffectEnvironment: []string{"HOME=" + home}, WorkingDirectory: "/"}
	result, err := service.Admit(ctx, submission)
	if err != nil {
		t.Fatal(err)
	}
	submission.EffectEnvironment[0] = "HOME=" + other
	completed, err := service.Advance(ctx, result.HostRunKey)
	if err != nil {
		t.Fatal(err)
	}
	if got := completed.Outcome.(kernel.Succeeded).Result().Stdout(); got != home+"\n" {
		t.Fatalf("working directory = %q, want %q", got, home+"\n")
	}
}

func TestAdmissionCapturesBaseWorkingDirectory(t *testing.T) {
	ctx := context.Background()
	service := newService(openStore(t))
	base := t.TempDir()
	submission := testSubmission("pwd")
	submission.WorkingDirectory = base
	result, err := service.Admit(ctx, submission)
	if err != nil {
		t.Fatal(err)
	}
	submission.WorkingDirectory = t.TempDir()
	completed, err := service.Advance(ctx, result.HostRunKey)
	if err != nil {
		t.Fatal(err)
	}
	if got := completed.Outcome.(kernel.Succeeded).Result().Stdout(); got != base+"\n" {
		t.Fatalf("working directory = %q, want %q", got, base+"\n")
	}
}

func TestAdmissionReopensAndAdvancesAfterRestart(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "journal.sqlite")
	store, err := graphstore.Open(ctx, path, graphstore.Options{})
	if err != nil {
		t.Fatal(err)
	}
	service := newService(store)
	result, err := service.Admit(ctx, testSubmission("printf reopened"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := graphstore.Open(ctx, path, graphstore.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	completed, err := newService(reopened).Advance(ctx, result.HostRunKey)
	if err != nil {
		t.Fatalf("Advance after reopen: %v", err)
	}
	if _, ok := completed.Outcome.(kernel.Succeeded); !ok {
		t.Fatalf("outcome = %T", completed.Outcome)
	}
}

func TestAdmissionReopensExactFailedOutcome(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "journal.sqlite")
	store, err := graphstore.Open(ctx, path, graphstore.Options{})
	if err != nil {
		t.Fatal(err)
	}
	result, err := newService(store).Admit(ctx, testSubmission("echo failure >&2; exit 7"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := graphstore.Open(ctx, path, graphstore.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	completed, err := newService(reopened).Advance(ctx, result.HostRunKey)
	if err != nil {
		t.Fatal(err)
	}
	failed, ok := completed.Outcome.(kernel.Failed)
	if !ok || failed.Reason() != "exit_7" || failed.Detail().Stderr() != "failure\n" {
		t.Fatalf("failed outcome = %#v", completed.Outcome)
	}
	if err := reopened.Close(); err != nil {
		t.Fatal(err)
	}
	finalStore, err := graphstore.Open(ctx, path, graphstore.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = finalStore.Close() })
	completed, err = newService(finalStore).Advance(ctx, result.HostRunKey)
	if err != nil {
		t.Fatal(err)
	}
	failed, ok = completed.Outcome.(kernel.Failed)
	if !ok || failed.Reason() != "exit_7" || failed.Detail().Stderr() != "failure\n" {
		t.Fatalf("reopened failed outcome = %#v", completed.Outcome)
	}
}

func TestAdmissionConcurrentIdempotencyHasOneAdmission(t *testing.T) {
	ctx := context.Background()
	service := newService(openStore(t))
	start := make(chan struct{})
	results := make(chan admission.Result, 2)
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			sub := testSubmission("true")
			sub.IdempotencyKey = "same"
			got, err := service.Admit(ctx, sub)
			results <- got
			errs <- err
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	close(errs)
	var key admission.HostRunKey
	var fresh, replay int
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent Admit: %v", err)
		}
	}
	for result := range results {
		if key != "" && key != result.HostRunKey {
			t.Fatalf("keys = %q, %q", key, result.HostRunKey)
		}
		key = result.HostRunKey
		if result.Replayed {
			replay++
		} else {
			fresh++
		}
	}
	if fresh != 1 || replay != 1 {
		t.Fatalf("admission classifications = fresh %d replay %d, want one each", fresh, replay)
	}
}

func TestConcurrentAdvanceExecutesOneEffectAndReturnsExactResults(t *testing.T) {
	ctx := context.Background()
	store := openStore(t)
	var calls atomic.Int32
	effectStarted := make(chan struct{})
	releaseEffect := make(chan struct{})
	execute := func(_ context.Context, command kernel.ExecCommand, _ []string, _ string) (kernel.Observation, error) {
		if calls.Add(1) == 1 {
			close(effectStarted)
		}
		<-releaseEffect
		return kernel.NewObservation(command.HostRunKey(), command.PrivateSequence(), "once", "", kernel.ExitTermination(0)), nil
	}
	service := admission.New(store, execute)
	admitted, err := service.Admit(ctx, testSubmission("true"))
	if err != nil {
		t.Fatal(err)
	}
	type result struct {
		admission.Result
		err error
	}
	results := make(chan result, 2)
	go func() {
		completed, err := service.Advance(ctx, admitted.HostRunKey)
		results <- result{Result: completed, err: err}
	}()
	receiveBeforeTimeout(t, effectStarted, "first effect start")
	secondEntered := make(chan struct{})
	go func() {
		close(secondEntered)
		completed, err := service.Advance(ctx, admitted.HostRunKey)
		results <- result{Result: completed, err: err}
	}()
	receiveBeforeTimeout(t, secondEntered, "second Advance entry")
	close(releaseEffect)
	for range 2 {
		completed := receiveBeforeTimeout(t, results, "concurrent Advance result")
		if completed.err != nil {
			t.Fatalf("Advance: %v", completed.err)
		}
		succeeded, ok := completed.Outcome.(kernel.Succeeded)
		if !ok || succeeded.Result().Stdout() != "once" {
			t.Fatalf("outcome = %#v", completed.Outcome)
		}
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("effect calls = %d, want 1", got)
	}
}

func TestIndependentServiceIsFencedThenReadsExactCompletedResult(t *testing.T) {
	ctx := context.Background()
	store := openStore(t)
	effectStarted := make(chan struct{})
	releaseEffect := make(chan struct{})
	first := admission.New(store, func(_ context.Context, command kernel.ExecCommand, _ []string, _ string) (kernel.Observation, error) {
		close(effectStarted)
		<-releaseEffect
		return kernel.NewObservation(command.HostRunKey(), command.PrivateSequence(), "owner", "", kernel.ExitTermination(0)), nil
	})
	var secondCalls atomic.Int32
	second := admission.New(store, func(_ context.Context, command kernel.ExecCommand, _ []string, _ string) (kernel.Observation, error) {
		secondCalls.Add(1)
		return kernel.NewObservation(command.HostRunKey(), command.PrivateSequence(), "wrong", "", kernel.ExitTermination(0)), nil
	})
	admitted, err := first.Admit(ctx, testSubmission("true"))
	if err != nil {
		t.Fatal(err)
	}
	firstResult := make(chan error, 1)
	go func() {
		_, err := first.Advance(ctx, admitted.HostRunKey)
		firstResult <- err
	}()
	receiveBeforeTimeout(t, effectStarted, "owning effect start")
	if _, err := second.Advance(ctx, admitted.HostRunKey); !errors.Is(err, graphstore.ErrLeaseHeld) {
		t.Fatalf("competing Advance error = %v, want ErrLeaseHeld", err)
	}
	close(releaseEffect)
	if err := receiveBeforeTimeout(t, firstResult, "owning Advance result"); err != nil {
		t.Fatalf("owning Advance: %v", err)
	}
	completed, err := second.Advance(ctx, admitted.HostRunKey)
	if err != nil {
		t.Fatalf("retry Advance: %v", err)
	}
	succeeded, ok := completed.Outcome.(kernel.Succeeded)
	if !ok || succeeded.Result().Stdout() != "owner" {
		t.Fatalf("retry outcome = %#v", completed.Outcome)
	}
	if got := secondCalls.Load(); got != 0 {
		t.Fatalf("competing effect calls = %d, want 0", got)
	}
}

func TestCanceledObservationPersistsAfterCallerCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	store := openStore(t)
	var calls atomic.Int32
	execute := func(_ context.Context, command kernel.ExecCommand, _ []string, _ string) (kernel.Observation, error) {
		calls.Add(1)
		cancel()
		return kernel.NewCanceledObservation(command.HostRunKey(), command.PrivateSequence(), context.Canceled.Error()), nil
	}
	service := admission.New(store, execute)
	admitted, err := service.Admit(context.Background(), testSubmission("true"))
	if err != nil {
		t.Fatal(err)
	}
	completed, err := service.Advance(ctx, admitted.HostRunKey)
	if err != nil {
		t.Fatalf("Advance: %v", err)
	}
	canceled, ok := completed.Outcome.(kernel.Canceled)
	if !ok || canceled.Reason() != context.Canceled.Error() {
		t.Fatalf("outcome = %#v", completed.Outcome)
	}

	reopened := admission.New(store, func(_ context.Context, _ kernel.ExecCommand, _ []string, _ string) (kernel.Observation, error) {
		calls.Add(1)
		return kernel.Observation{}, errors.New("terminal run reexecuted")
	})
	completed, err = reopened.Advance(context.Background(), admitted.HostRunKey)
	if err != nil {
		t.Fatalf("reopen Advance: %v", err)
	}
	if _, ok := completed.Outcome.(kernel.Canceled); !ok {
		t.Fatalf("reopened outcome = %#v", completed.Outcome)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("effect calls after reopen = %d, want 1", got)
	}
}

func openStore(t *testing.T) *graphstore.Store {
	t.Helper()
	store, err := graphstore.Open(context.Background(), filepath.Join(t.TempDir(), "journal.sqlite"), graphstore.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func newService(store *graphstore.Store) *admission.Service {
	return admission.New(store, exechost.ExecuteCaptured)
}

func testSubmission(script string) admission.RunSubmission {
	parts := []program.TextPart{program.NewText(script)}
	if script == "echo " {
		parts = append(parts, program.NewInterpolation(program.NewReference("message")))
	}
	exec := program.NewExec("exec", nil, program.NewInterpolatedText(parts), nil, nil, nil, program.NewExitMap([]int{0}, nil))
	return admission.RunSubmission{Source: []byte(script), Program: program.NewProgram(program.NewFormula("run", program.NewInput("run.input", []program.Field{program.NewField("message", program.NewAtomicType("string"), true)}), []program.Step{program.NewBlock("block", nil, []program.Step{exec})})), Inputs: []admission.Input{admission.NewInput("message", admission.NewStringValue("hello"))}, WorkingDirectory: "/"}
}

func withDegradedTerminal(candidate program.Program, id, name, reason string) program.Program {
	formula := candidate.Formula()
	steps := formula.Steps()
	steps = append(steps, program.NewTerminal(id, name, []string{steps[0].ID()}, program.NewDegraded(nil, reason)))
	return program.NewProgram(program.NewFormula(formula.Name(), formula.Input(), steps))
}

func receiveBeforeTimeout[T any](t *testing.T, values <-chan T, description string) T {
	t.Helper()
	select {
	case value := <-values:
		return value
	case <-time.After(testutil.GoroutineRaceTimeout):
		t.Fatalf("timed out waiting for %s", description)
		var zero T
		return zero
	}
}
