// Package admission owns private Lumen run admission and controller advancement.
package admission

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"
	"time"

	"github.com/gastownhall/gascity/internal/graphstore"
	"github.com/gastownhall/gascity/internal/lumen/kernel"
	"github.com/gastownhall/gascity/internal/lumen/program"
	"golang.org/x/sync/singleflight"
)

const (
	writerLeaseDuration = time.Minute
	durableCommitLimit  = 5 * time.Second
)

// ErrIdempotencyConflict reports reuse of an identity for different work.
var ErrIdempotencyConflict = errors.New("lumen admission: idempotency identity conflicts with prior admission")

// HostRunKey is controller-private correlation for one execution journal.
type HostRunKey string

type valueKind string

const (
	stringValue valueKind = "string"
	boolValue   valueKind = "bool"
	numberValue valueKind = "number"
	nullValue   valueKind = "null"
)

// Value is one closed inline scalar captured at admission.
type Value struct {
	kind    valueKind
	text    string
	boolean bool
	number  float64
}

// NewStringValue builds an inline string value.
func NewStringValue(value string) Value { return Value{kind: stringValue, text: value} }

// NewBooleanValue builds an inline boolean value.
func NewBooleanValue(value bool) Value { return Value{kind: boolValue, boolean: value} }

// NewNumberValue builds an inline number value. Admission rejects non-finite values.
func NewNumberValue(value float64) Value { return Value{kind: numberValue, number: value} }

// NewNullValue builds an inline null value.
func NewNullValue() Value { return Value{kind: nullValue} }

// Input is one named accepted input captured by value.
type Input struct {
	name  string
	value Value
}

// NewInput builds one captured input.
func NewInput(name string, value Value) Input { return Input{name: name, value: value} }

// RunSubmission is the complete typed work captured by one admission.
type RunSubmission struct {
	Source            []byte
	Program           program.Program
	Inputs            []Input
	EffectEnvironment []string
	WorkingDirectory  string
	IdempotencyKey    string
}

// Outcome is the exact closed kernel completion family.
type Outcome = kernel.Outcome

// Result is the controller-private result of admission or advancement.
type Result struct {
	HostRunKey HostRunKey
	Outcome    Outcome
	Replayed   bool
}

type executor func(context.Context, kernel.ExecCommand, []string, string) (kernel.Observation, error)

// Service is the sole owner of run admission and journal advancement.
type Service struct {
	store           *graphstore.Store
	execute         executor
	mintHostRunKey  func() (HostRunKey, error)
	writerHolder    string
	writerHolderErr error
	advances        singleflight.Group
}

// New builds an admission service over the supplied Lumen graph journal.
func New(store *graphstore.Store, execute func(context.Context, kernel.ExecCommand, []string, string) (kernel.Observation, error)) *Service {
	holder, err := randomHolder()
	return &Service{
		store: store, execute: execute, mintHostRunKey: randomHostRunKey,
		writerHolder: holder, writerHolderErr: err,
	}
}

// Admit validates and atomically creates one immutable private run journal.
func (s *Service) Admit(ctx context.Context, submission RunSubmission) (Result, error) {
	if s == nil || s.store == nil {
		return Result{}, fmt.Errorf("lumen admission: store is required")
	}
	if s.execute == nil {
		return Result{}, fmt.Errorf("lumen admission: executor is required")
	}
	if s.writerHolderErr != nil {
		return Result{}, s.writerHolderErr
	}
	if len(submission.Source) == 0 {
		return Result{}, fmt.Errorf("lumen admission: source is required")
	}
	if !filepath.IsAbs(submission.WorkingDirectory) {
		return Result{}, fmt.Errorf("lumen admission: working directory must be absolute")
	}
	validated, err := program.ValidateProgram(submission.Program)
	if err != nil {
		return Result{}, fmt.Errorf("lumen admission: validate program: %w", err)
	}
	inputs, bindings, err := captureInputs(validated, submission.Inputs)
	if err != nil {
		return Result{}, err
	}
	programBytes, err := program.EncodeSnapshot(validated)
	if err != nil {
		return Result{}, fmt.Errorf("lumen admission: encode program: %w", err)
	}
	prepared := snapshot{
		Source:      append([]byte(nil), submission.Source...),
		Program:     programBytes,
		Inputs:      inputs,
		Environment: append([]string(nil), submission.EffectEnvironment...),
		Base:        submission.WorkingDirectory,
	}

	if submission.IdempotencyKey != "" {
		key := idempotentHostRunKey(submission.IdempotencyKey)
		return s.createOrReplay(ctx, key, prepared, validated, bindings, true)
	}
	for {
		key, err := s.mintHostRunKey()
		if err != nil {
			return Result{}, fmt.Errorf("lumen admission: mint private key: %w", err)
		}
		if key == "" {
			return Result{}, fmt.Errorf("lumen admission: minted empty private key")
		}
		result, err := s.createOrReplay(ctx, key, prepared, validated, bindings, false)
		if errors.Is(err, graphstore.ErrStreamExists) {
			continue
		}
		return result, err
	}
}

func (s *Service) createOrReplay(
	ctx context.Context,
	key HostRunKey,
	prepared snapshot,
	candidate program.Program,
	bindings []kernel.InputBinding,
	allowReplay bool,
) (Result, error) {
	prepared.Key = key
	genesisBytes, err := encodeGenesis(prepared)
	if err != nil {
		return Result{}, err
	}
	if _, err := kernel.Fold(candidate, []kernel.Record{
		kernel.NewGenesis(kernel.HostRunKey(key), bindings),
	}); err != nil {
		return Result{}, fmt.Errorf("lumen admission: validate executable program: %w", err)
	}
	stream, err := streamFor(key)
	if err != nil {
		return Result{}, err
	}
	record, err := graphstore.NewRecord(1, genesisRecordType, genesisBytes)
	if err != nil {
		return Result{}, fmt.Errorf("lumen admission: create genesis: %w", err)
	}
	if _, err := s.store.Create(ctx, stream, record); err == nil {
		return Result{HostRunKey: key}, nil
	} else if !errors.Is(err, graphstore.ErrStreamExists) {
		return Result{}, fmt.Errorf("lumen admission: create stream: %w", err)
	} else if !allowReplay {
		return Result{}, graphstore.ErrStreamExists
	}

	prefix, err := s.store.Read(ctx, stream, graphstore.CursorAt(0))
	if err != nil {
		return Result{}, fmt.Errorf("lumen admission: reread replay: %w", err)
	}
	records := prefix.Records()
	if len(records) == 0 || records[0].Type() != genesisRecordType {
		return Result{}, ErrIdempotencyConflict
	}
	if _, err := decodeGenesis(records[0].Payload(), key); err != nil {
		return Result{}, err
	}
	if !bytes.Equal(genesisBytes, records[0].Payload()) {
		return Result{}, ErrIdempotencyConflict
	}
	return Result{HostRunKey: key, Replayed: true}, nil
}

// Advance resumes a private run until it reaches its exact terminal outcome.
// After restart, only a command committed without an observation may execute
// again under the journal's at-least-once effect boundary.
func (s *Service) Advance(ctx context.Context, key HostRunKey) (Result, error) {
	if s == nil || s.store == nil {
		return Result{}, fmt.Errorf("lumen admission: store is required")
	}
	if s.execute == nil {
		return Result{}, fmt.Errorf("lumen admission: executor is required")
	}
	if key == "" {
		return Result{}, fmt.Errorf("lumen admission: host run key is required")
	}
	value, err, _ := s.advances.Do(string(key), func() (any, error) {
		return s.advance(ctx, key)
	})
	if err != nil {
		return Result{}, err
	}
	return value.(Result), nil
}

func (s *Service) advance(ctx context.Context, key HostRunKey) (Result, error) {
	stream, err := streamFor(key)
	if err != nil {
		return Result{}, err
	}
	for {
		prefix, err := s.store.Read(ctx, stream, graphstore.CursorAt(0))
		if err != nil {
			return Result{}, err
		}
		candidate, records, environment, base, err := decodeJournal(prefix.Records(), key)
		if err != nil {
			return Result{}, err
		}
		state, err := kernel.Fold(candidate, records)
		if err != nil {
			return Result{}, fmt.Errorf("lumen admission: fold: %w", err)
		}
		if state.Terminal() {
			return Result{HostRunKey: key, Outcome: state.Outcome()}, nil
		}

		if s.writerHolderErr != nil {
			return Result{}, s.writerHolderErr
		}
		lease, err := s.store.AcquireLease(ctx, stream, s.writerHolder, writerLeaseDuration)
		if err != nil {
			return Result{}, err
		}
		cursor := prefix.Through()
		if command, ok := state.Command(); ok {
			cursor, err = s.appendMarker(ctx, stream, cursor, lease, issuedRecordType)
			if isJournalRace(err) {
				continue
			}
			if err != nil {
				return Result{}, err
			}
			records = append(records, kernel.NewCommandIssued(
				command.HostRunKey(), command.PrivateSequence(), command.StepID(),
			))
			return s.executeAndCommit(ctx, stream, cursor, s.writerHolder, candidate, records, environment, base, command)
		}
		if command, ok := state.IssuedCommand(); ok {
			return s.executeAndCommit(ctx, stream, cursor, s.writerHolder, candidate, records, environment, base, command)
		}
		if _, ok := state.PendingTerminal(); ok {
			if _, err := s.appendMarker(ctx, stream, cursor, lease, terminalRecordType); isJournalRace(err) {
				continue
			} else if err != nil {
				return Result{}, err
			}
			continue
		}
		return Result{}, fmt.Errorf("lumen admission: run cannot advance")
	}
}

func (s *Service) executeAndCommit(
	ctx context.Context,
	stream graphstore.StreamAddress,
	cursor graphstore.Cursor,
	holder string,
	candidate program.Program,
	records []kernel.Record,
	environment []string,
	base string,
	command kernel.ExecCommand,
) (Result, error) {
	observation, err := s.execute(ctx, command, append([]string(nil), environment...), base)
	if err != nil {
		return Result{}, fmt.Errorf("lumen admission: execute: %w", err)
	}
	pending, err := validateObservation(candidate, records, observation)
	if err != nil {
		return Result{}, err
	}
	observationBytes, err := encodeObservation(observation)
	if err != nil {
		return Result{}, err
	}
	observationRecord, err := graphstore.NewRecord(cursor.Sequence()+1, observationRecordType, observationBytes)
	if err != nil {
		return Result{}, err
	}
	terminalRecord, err := graphstore.NewRecord(cursor.Sequence()+2, terminalRecordType, []byte("{}"))
	if err != nil {
		return Result{}, err
	}
	commitCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), durableCommitLimit)
	defer cancel()
	lease, err := s.store.AcquireLease(commitCtx, stream, holder, writerLeaseDuration)
	if err != nil {
		return Result{}, fmt.Errorf("lumen admission: renew writer lease: %w", err)
	}
	if _, err := s.store.Append(
		commitCtx, stream, cursor, lease, []graphstore.Record{observationRecord, terminalRecord},
	); err != nil {
		return Result{}, fmt.Errorf("lumen admission: commit observation: %w", err)
	}
	return Result{HostRunKey: HostRunKey(command.HostRunKey()), Outcome: pending.Outcome()}, nil
}

func validateObservation(candidate program.Program, records []kernel.Record, observation kernel.Observation) (kernel.Terminal, error) {
	withObservation := append(append([]kernel.Record(nil), records...), observation)
	state, err := kernel.Fold(candidate, withObservation)
	if err != nil {
		return kernel.Terminal{}, fmt.Errorf("lumen admission: reject host observation: %w", err)
	}
	terminal, ok := state.PendingTerminal()
	if !ok {
		return kernel.Terminal{}, fmt.Errorf("lumen admission: host observation derived no terminal")
	}
	return terminal, nil
}

func (s *Service) appendMarker(
	ctx context.Context,
	stream graphstore.StreamAddress,
	cursor graphstore.Cursor,
	lease graphstore.WriterLease,
	typ string,
) (graphstore.Cursor, error) {
	record, err := graphstore.NewRecord(cursor.Sequence()+1, typ, []byte("{}"))
	if err != nil {
		return graphstore.Cursor{}, err
	}
	next, err := s.store.Append(ctx, stream, cursor, lease, []graphstore.Record{record})
	if err != nil {
		return graphstore.Cursor{}, err
	}
	return next, nil
}

func isJournalRace(err error) bool {
	return errors.Is(err, graphstore.ErrCursorMismatch) || errors.Is(err, graphstore.ErrLeaseFenced)
}

func idempotentHostRunKey(identity string) HostRunKey {
	sum := sha256.Sum256([]byte(identity))
	return HostRunKey("i-" + hex.EncodeToString(sum[:]))
}

func randomHostRunKey() (HostRunKey, error) {
	value, err := randomToken()
	return HostRunKey("r-" + value), err
}

func randomHolder() (string, error) {
	value, err := randomToken()
	if err != nil {
		return "", fmt.Errorf("lumen admission: mint writer holder: %w", err)
	}
	return "controller-" + value, nil
}

func randomToken() (string, error) {
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return hex.EncodeToString(value), nil
}

func streamFor(key HostRunKey) (graphstore.StreamAddress, error) {
	return graphstore.NewStreamAddress("lumen/private/" + string(key))
}
