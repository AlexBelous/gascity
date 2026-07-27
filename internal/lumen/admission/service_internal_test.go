package admission

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/graphstore"
	"github.com/gastownhall/gascity/internal/lumen/kernel"
	"github.com/gastownhall/gascity/internal/lumen/program"
)

func TestFreshAdmissionRetriesPrivateKeyCollision(t *testing.T) {
	store := openInternalStore(t)
	service := New(store, successfulExecutor)
	keys := []HostRunKey{"collision", "collision", "unique"}
	service.mintHostRunKey = func() (HostRunKey, error) {
		key := keys[0]
		keys = keys[1:]
		return key, nil
	}
	first, err := service.Admit(context.Background(), internalSubmission())
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.Admit(context.Background(), internalSubmission())
	if err != nil {
		t.Fatal(err)
	}
	if first.HostRunKey != "collision" || second.HostRunKey != "unique" || second.Replayed {
		t.Fatalf("admissions = %#v, %#v", first, second)
	}
}

func TestAdmissionWriterHolderFailureLeavesIdempotencyUnclaimed(t *testing.T) {
	service := New(openInternalStore(t), successfulExecutor)
	holderErr := errors.New("writer holder unavailable")
	service.writerHolderErr = holderErr
	submission := internalSubmission()
	submission.IdempotencyKey = "retry-after-holder-recovery"

	if _, err := service.Admit(context.Background(), submission); !errors.Is(err, holderErr) {
		t.Fatalf("Admit error = %v, want writer holder failure", err)
	}
	service.writerHolderErr = nil
	admitted, err := service.Admit(context.Background(), submission)
	if err != nil {
		t.Fatalf("Admit after writer holder recovery: %v", err)
	}
	if admitted.Replayed {
		t.Fatalf("Admit after writer holder recovery replayed a partial admission: %#v", admitted)
	}
}

func TestAdmissionAdvancesWithOmittedOptionalAtomicInput(t *testing.T) {
	execute := program.NewExec(
		"exec", nil,
		program.NewInterpolatedText([]program.TextPart{program.NewText("true")}),
		nil, nil, nil, program.NewExitMap([]int{0}, nil),
	)
	submission := RunSubmission{
		Source: []byte("true"),
		Program: program.NewProgram(program.NewFormula(
			"run",
			program.NewInput("run.input", []program.Field{
				program.NewField("message", program.NewAtomicType("string"), false),
			}),
			[]program.Step{program.NewBlock("block", nil, []program.Step{execute})},
		)),
		WorkingDirectory: "/",
	}
	service := New(openInternalStore(t), successfulExecutor)

	admitted, err := service.Admit(context.Background(), submission)
	if err != nil {
		t.Fatalf("Admit: %v", err)
	}
	completed, err := service.Advance(context.Background(), admitted.HostRunKey)
	if err != nil {
		t.Fatalf("Advance: %v", err)
	}
	succeeded, ok := completed.Outcome.(kernel.Succeeded)
	if !ok {
		t.Fatalf("outcome = %T, want kernel.Succeeded", completed.Outcome)
	}
	result := succeeded.Result()
	exit, ok := result.Termination().(kernel.ExitTermination)
	if result.Stdout() != "ok" || result.Stderr() != "" || !ok || exit != 0 || succeeded.Degradation() != nil {
		t.Fatalf("outcome = %#v, want exact successful execution", completed.Outcome)
	}
}

func TestGenesisAndRecordDecodersFailClosed(t *testing.T) {
	candidate := internalSubmission()
	encodedProgram, err := program.EncodeSnapshot(candidate.Program)
	if err != nil {
		t.Fatal(err)
	}
	inputs, _, err := captureInputs(candidate.Program, candidate.Inputs)
	if err != nil {
		t.Fatal(err)
	}
	snap := snapshot{
		Key: "expected", Source: candidate.Source, Program: encodedProgram, Inputs: inputs,
		Environment: candidate.EffectEnvironment, Base: candidate.WorkingDirectory,
	}
	raw, err := encodeGenesis(snap)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := decodeGenesis(raw, "other"); err == nil {
		t.Fatal("decodeGenesis accepted a key that does not match its stream")
	}
	unknown := append(append([]byte(nil), raw[:len(raw)-1]...), []byte(`,"unknown":true}`)...)
	if _, err := decodeGenesis(unknown, "expected"); err == nil {
		t.Fatal("decodeGenesis accepted an unknown field")
	}
	if err := decodeEmptyMarker([]byte(`{"unknown":true}`)); err == nil {
		t.Fatal("decodeEmptyMarker accepted a non-empty marker")
	}

	command := commandForTest(t, candidate.Program, "expected")
	observation := kernel.NewObservation(command.HostRunKey(), command.PrivateSequence(), "", "", kernel.ExitTermination(0))
	encodedObservation, err := encodeObservation(observation)
	if err != nil {
		t.Fatal(err)
	}
	unknown = append(append([]byte(nil), encodedObservation[:len(encodedObservation)-1]...), []byte(`,"unknown":true}`)...)
	if _, err := decodeObservation(unknown, command); err == nil {
		t.Fatal("decodeObservation accepted an unknown field")
	}
	if _, err := decodeObservation([]byte(`{"kind":"canceled","reason":"stop","stdout":""}`), command); err == nil {
		t.Fatal("decodeObservation accepted an impossible union")
	}

	extra := snap
	extra.Inputs = append(extra.Inputs, inputSnapshot{Name: "extra", Value: valueSnapshot{Kind: nullValue}})
	extraRaw, err := encodeGenesis(extra)
	if err != nil {
		t.Fatal(err)
	}
	extraRecord, err := graphstore.NewRecord(1, genesisRecordType, extraRaw)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, _, err := decodeJournal([]graphstore.Record{extraRecord}, "expected"); err == nil {
		t.Fatal("decodeJournal accepted extra genesis input")
	}
}

func TestInvalidHostObservationNeverPoisonsJournal(t *testing.T) {
	ctx := context.Background()
	store := openInternalStore(t)
	bad := New(store, func(_ context.Context, command kernel.ExecCommand, _ []string, _ string) (kernel.Observation, error) {
		return kernel.NewObservation("wrong-key", command.PrivateSequence(), "", "", kernel.ExitTermination(0)), nil
	})
	admitted, err := bad.Admit(ctx, internalSubmission())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := bad.Advance(ctx, admitted.HostRunKey); err == nil {
		t.Fatal("Advance accepted a mismatched host observation")
	}
	bad.execute = successfulExecutor
	completed, err := bad.Advance(ctx, admitted.HostRunKey)
	if err != nil {
		t.Fatalf("Advance after rejected observation: %v", err)
	}
	if _, ok := completed.Outcome.(kernel.Succeeded); !ok {
		t.Fatalf("outcome = %#v", completed.Outcome)
	}
}

func TestObservationOnlyPrefixRecoversWithoutReexecution(t *testing.T) {
	ctx := context.Background()
	store := openInternalStore(t)
	service := New(store, successfulExecutor)
	admitted, err := service.Admit(ctx, internalSubmission())
	if err != nil {
		t.Fatal(err)
	}
	stream, err := streamFor(admitted.HostRunKey)
	if err != nil {
		t.Fatal(err)
	}
	prefix, err := store.Read(ctx, stream, graphstore.CursorAt(0))
	if err != nil {
		t.Fatal(err)
	}
	candidate, records, _, _, err := decodeJournal(prefix.Records(), admitted.HostRunKey)
	if err != nil {
		t.Fatal(err)
	}
	state, err := kernel.Fold(candidate, records)
	if err != nil {
		t.Fatal(err)
	}
	command, ok := state.Command()
	if !ok {
		t.Fatal("genesis derived no command")
	}
	observation := kernel.NewObservation(command.HostRunKey(), command.PrivateSequence(), "recovered", "", kernel.ExitTermination(0))
	observationBytes, err := encodeObservation(observation)
	if err != nil {
		t.Fatal(err)
	}
	issued, err := graphstore.NewRecord(2, issuedRecordType, []byte("{}"))
	if err != nil {
		t.Fatal(err)
	}
	observed, err := graphstore.NewRecord(3, observationRecordType, observationBytes)
	if err != nil {
		t.Fatal(err)
	}
	lease, err := store.AcquireLease(ctx, stream, service.writerHolder, writerLeaseDuration)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Append(ctx, stream, prefix.Through(), lease, []graphstore.Record{issued, observed}); err != nil {
		t.Fatal(err)
	}
	service.execute = func(_ context.Context, _ kernel.ExecCommand, _ []string, _ string) (kernel.Observation, error) {
		return kernel.Observation{}, errors.New("observation-only prefix reexecuted")
	}
	completed, err := service.Advance(ctx, admitted.HostRunKey)
	if err != nil {
		t.Fatal(err)
	}
	succeeded, ok := completed.Outcome.(kernel.Succeeded)
	if !ok || succeeded.Result().Stdout() != "recovered" {
		t.Fatalf("outcome = %#v", completed.Outcome)
	}
}

func TestLongEffectRenewsSameWriterBeforeCommit(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 7, 27, 0, 0, 0, 0, time.UTC)
	store := openInternalStoreWithClock(t, func() time.Time { return now })
	calls := 0
	service := New(store, func(_ context.Context, command kernel.ExecCommand, _ []string, _ string) (kernel.Observation, error) {
		calls++
		now = now.Add(2 * writerLeaseDuration)
		return kernel.NewObservation(command.HostRunKey(), command.PrivateSequence(), "long", "", kernel.ExitTermination(0)), nil
	})
	admitted, err := service.Admit(ctx, internalSubmission())
	if err != nil {
		t.Fatal(err)
	}
	completed, err := service.Advance(ctx, admitted.HostRunKey)
	if err != nil {
		t.Fatal(err)
	}
	succeeded, ok := completed.Outcome.(kernel.Succeeded)
	if !ok || succeeded.Result().Stdout() != "long" || calls != 1 {
		t.Fatalf("outcome/calls = %#v / %d", completed.Outcome, calls)
	}
}

func TestCommitRaceReturnsForLaterAdvanceWithoutReexecution(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 7, 27, 0, 0, 0, 0, time.UTC)
	store := openInternalStoreWithClock(t, func() time.Time { return now })
	calls := 0
	service := New(store, func(_ context.Context, command kernel.ExecCommand, _ []string, _ string) (kernel.Observation, error) {
		calls++
		observation := kernel.NewObservation(command.HostRunKey(), command.PrivateSequence(), "once", "", kernel.ExitTermination(0))
		stream, err := streamFor(HostRunKey(command.HostRunKey()))
		if err != nil {
			return kernel.Observation{}, err
		}
		prefix, err := store.Read(ctx, stream, graphstore.CursorAt(0))
		if err != nil {
			return kernel.Observation{}, fmt.Errorf("read issued journal: %w", err)
		}
		encoded, err := encodeObservation(observation)
		if err != nil {
			return kernel.Observation{}, err
		}
		record, err := graphstore.NewRecord(prefix.Through().Sequence()+1, observationRecordType, encoded)
		if err != nil {
			return kernel.Observation{}, err
		}
		now = now.Add(2 * writerLeaseDuration)
		lease, err := store.AcquireLease(ctx, stream, "competing-writer", writerLeaseDuration)
		if err != nil {
			return kernel.Observation{}, fmt.Errorf("acquire competing lease: %w", err)
		}
		if _, err := store.Append(ctx, stream, prefix.Through(), lease, []graphstore.Record{record}); err != nil {
			return kernel.Observation{}, fmt.Errorf("append competing observation: %w", err)
		}
		now = now.Add(2 * writerLeaseDuration)
		return observation, nil
	})
	admitted, err := service.Admit(ctx, internalSubmission())
	if err != nil {
		t.Fatal(err)
	}

	if _, err := service.Advance(ctx, admitted.HostRunKey); !errors.Is(err, graphstore.ErrCursorMismatch) {
		t.Fatalf("Advance error = %v, want ErrCursorMismatch", err)
	}
	if calls != 1 {
		t.Fatalf("effect calls = %d, want 1", calls)
	}
	completed, err := service.Advance(ctx, admitted.HostRunKey)
	if err != nil {
		t.Fatalf("later Advance: %v", err)
	}
	succeeded, ok := completed.Outcome.(kernel.Succeeded)
	if !ok || succeeded.Result().Stdout() != "once" || calls != 1 {
		t.Fatalf("later outcome/calls = %#v / %d", completed.Outcome, calls)
	}
}

func openInternalStore(t *testing.T) *graphstore.Store {
	t.Helper()
	return openInternalStoreWithClock(t, nil)
}

func openInternalStoreWithClock(t *testing.T, clock func() time.Time) *graphstore.Store {
	t.Helper()
	store, err := graphstore.Open(
		context.Background(), filepath.Join(t.TempDir(), "journal.sqlite"),
		graphstore.Options{Clock: clock},
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func internalSubmission() RunSubmission {
	execute := program.NewExec("exec", nil, program.NewInterpolatedText([]program.TextPart{program.NewText("true")}), nil, nil, nil, program.NewExitMap([]int{0}, nil))
	return RunSubmission{
		Source: []byte("true"),
		Program: program.NewProgram(program.NewFormula(
			"run", program.NewInput("run.input", nil),
			[]program.Step{program.NewBlock("block", nil, []program.Step{execute})},
		)),
		WorkingDirectory: "/",
	}
}

func successfulExecutor(_ context.Context, command kernel.ExecCommand, _ []string, _ string) (kernel.Observation, error) {
	if command.StepID() == "" {
		return kernel.Observation{}, errors.New("missing command")
	}
	return kernel.NewObservation(command.HostRunKey(), command.PrivateSequence(), "ok", "", kernel.ExitTermination(0)), nil
}

func commandForTest(t *testing.T, candidate program.Program, key HostRunKey) kernel.ExecCommand {
	t.Helper()
	state, err := kernel.Fold(candidate, []kernel.Record{kernel.NewGenesis(kernel.HostRunKey(key), nil)})
	if err != nil {
		t.Fatal(err)
	}
	command, ok := state.Command()
	if !ok {
		t.Fatal("genesis derived no command")
	}
	return command
}
