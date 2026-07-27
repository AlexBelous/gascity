// Package kernel folds the minimal private Lumen execution journal.
package kernel

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/gastownhall/gascity/internal/lumen/program"
)

// HostRunKey is host-private correlation for one execution journal.
type HostRunKey string

// PrivateSequence correlates one host-private command and its observation.
type PrivateSequence uint64

// InputBinding is one immutable scalar input supplied at genesis.
type InputBinding struct {
	name  string
	value program.Scalar
}

// NewInputBinding builds one scalar input binding.
func NewInputBinding(name string, value program.Scalar) InputBinding {
	return InputBinding{name: name, value: value}
}

// Record is one closed journal record accepted by Fold.
type Record interface{ recordMarker() }

// Genesis starts a journal, establishes its key, and supplies required inputs.
type Genesis struct {
	hostRunKey HostRunKey
	bindings   []InputBinding
}

// NewGenesis builds a genesis record.
func NewGenesis(hostRunKey HostRunKey, bindings []InputBinding) Genesis {
	return Genesis{hostRunKey: hostRunKey, bindings: append([]InputBinding(nil), bindings...)}
}

func (Genesis) recordMarker() {}

// CommandIssued commits a derived command before the host receives it.
type CommandIssued struct {
	hostRunKey HostRunKey
	sequence   PrivateSequence
	stepID     string
}

// NewCommandIssued builds a committed-command record.
func NewCommandIssued(hostRunKey HostRunKey, sequence PrivateSequence, stepID string) CommandIssued {
	return CommandIssued{hostRunKey: hostRunKey, sequence: sequence, stepID: stepID}
}

func (CommandIssued) recordMarker() {}

// Observation records one correlated host execution observation.
type Observation struct {
	hostRunKey HostRunKey
	sequence   PrivateSequence
	value      observationValue
}

// NewObservation builds one closed host execution observation.
func NewObservation(hostRunKey HostRunKey, sequence PrivateSequence, stdout, stderr string, termination ExecTermination) Observation {
	return Observation{hostRunKey: hostRunKey, sequence: sequence, value: execObservation{stdout: stdout, stderr: stderr, termination: termination}}
}

func (Observation) recordMarker() {}

// NewCanceledObservation builds one closed host cancellation observation.
func NewCanceledObservation(hostRunKey HostRunKey, sequence PrivateSequence, reason string) Observation {
	return Observation{hostRunKey: hostRunKey, sequence: sequence, value: canceledObservation{reason: reason}}
}

type observationValue interface{ observationValueMarker() }

type execObservation struct {
	stdout      string
	stderr      string
	termination ExecTermination
}

func (execObservation) observationValueMarker() {}

type canceledObservation struct{ reason string }

func (canceledObservation) observationValueMarker() {}

// Terminal commits one kernel-derived terminal outcome.
type Terminal struct {
	hostRunKey HostRunKey
	sequence   PrivateSequence
	outcome    Outcome
}

// NewTerminal builds a terminal record. Fold accepts it only when it matches
// the terminal append already derived from the observation.
func NewTerminal(hostRunKey HostRunKey, sequence PrivateSequence, outcome Outcome) Terminal {
	return Terminal{hostRunKey: hostRunKey, sequence: sequence, outcome: outcome}
}

// Outcome returns the terminal outcome.
func (r Terminal) Outcome() Outcome { return r.outcome }

func (Terminal) recordMarker() {}

// RenderedEnvironment is one host-ready environment change.
type RenderedEnvironment struct {
	key    string
	value  string
	remove bool
}

// Key returns the environment variable name.
func (e RenderedEnvironment) Key() string { return e.key }

// Value returns the rendered value when Remove is false.
func (e RenderedEnvironment) Value() string { return e.value }

// Remove reports whether this environment variable must be removed.
func (e RenderedEnvironment) Remove() bool { return e.remove }

// ExecCommand is the only host command this kernel can derive.
type ExecCommand struct {
	hostRunKey HostRunKey
	sequence   PrivateSequence
	stepID     string
	script     string
	cwd        string
	cwdSet     bool
	stdin      string
	stdinSet   bool
	env        []RenderedEnvironment
	pass       []int
}

// HostRunKey returns the command's host-private key.
func (c ExecCommand) HostRunKey() HostRunKey { return c.hostRunKey }

// PrivateSequence returns the command's host-private sequence.
func (c ExecCommand) PrivateSequence() PrivateSequence { return c.sequence }

// StepID returns the source exec step identifier.
func (c ExecCommand) StepID() string { return c.stepID }

// Script returns the rendered shell script.
func (c ExecCommand) Script() string { return c.script }

// CWD returns the rendered optional working directory.
func (c ExecCommand) CWD() (string, bool) { return c.cwd, c.cwdSet }

// Stdin returns the rendered optional stdin.
func (c ExecCommand) Stdin() (string, bool) { return c.stdin, c.stdinSet }

// Environment returns a copy of host-ready environment changes.
func (c ExecCommand) Environment() []RenderedEnvironment {
	return append([]RenderedEnvironment(nil), c.env...)
}

// ExecTermination is the closed host execution termination family.
type ExecTermination interface{ terminationMarker() }

// ExitTermination is a process exit status.
type ExitTermination int

func (ExitTermination) terminationMarker() {}

// SignalTermination is a host-reported process signal.
type SignalTermination string

func (SignalTermination) terminationMarker() {}

// SpawnErrorTermination is a host-reported spawn failure detail.
type SpawnErrorTermination string

func (SpawnErrorTermination) terminationMarker() {}

// ExecResult is the exact successful exec value.
type ExecResult struct {
	stdout      string
	stderr      string
	termination ExecTermination
}

// Stdout returns captured stdout.
func (r ExecResult) Stdout() string { return r.stdout }

// Stderr returns captured stderr.
func (r ExecResult) Stderr() string { return r.stderr }

// Termination returns the closed host termination.
func (r ExecResult) Termination() ExecTermination { return r.termination }

// ExecFailure is the exact failed exec value, with no parallel error field.
type ExecFailure struct {
	stdout      string
	stderr      string
	termination ExecTermination
}

// Stdout returns captured stdout.
func (r ExecFailure) Stdout() string { return r.stdout }

// Stderr returns captured stderr.
func (r ExecFailure) Stderr() string { return r.stderr }

// Termination returns the closed host termination.
func (r ExecFailure) Termination() ExecTermination { return r.termination }

// Degradation is optional authored impairment metadata on a successful outcome.
type Degradation struct{ reason string }

// Reason returns the authored impairment reason.
func (d Degradation) Reason() string { return d.reason }

// Outcome is the closed runtime terminal outcome family.
type Outcome interface{ outcomeMarker() }

// Succeeded carries the exact successful exec result and optional degradation.
type Succeeded struct {
	result      ExecResult
	degradation *Degradation
}

// Result returns the exact successful exec result.
func (o Succeeded) Result() ExecResult { return o.result }

// Degradation returns optional authored impairment metadata.
func (o Succeeded) Degradation() *Degradation {
	if o.degradation == nil {
		return nil
	}
	cloned := *o.degradation
	return &cloned
}

func (Succeeded) outcomeMarker() {}

// Failed carries an exact failed exec value and a closed reason string.
type Failed struct {
	reason string
	detail ExecFailure
}

// Reason returns the normalized failure reason.
func (o Failed) Reason() string { return o.reason }

// Detail returns the exact failed exec value.
func (o Failed) Detail() ExecFailure { return o.detail }

func (Failed) outcomeMarker() {}

// Skipped is a benign runtime terminal outcome.
type Skipped struct{ reason string }

// NewSkipped builds a skipped runtime outcome.
func NewSkipped(reason string) Skipped { return Skipped{reason: reason} }

// Reason returns why the work did not run.
func (o Skipped) Reason() string { return o.reason }

func (Skipped) outcomeMarker() {}

// Canceled is an explicitly interrupted runtime terminal outcome.
type Canceled struct{ reason string }

// NewCanceled builds a canceled runtime outcome.
func NewCanceled(reason string) Canceled { return Canceled{reason: reason} }

// Reason returns why the work was interrupted.
func (o Canceled) Reason() string { return o.reason }

func (Canceled) outcomeMarker() {}

// FoldError identifies invalid private journal history or unrenderable input.
type FoldError struct {
	Index   int
	Problem string
}

func (e *FoldError) Error() string {
	return fmt.Sprintf("invalid Lumen journal record %d: %s", e.Index, e.Problem)
}

// State is the immutable result of folding a Program and its private records.
type State struct {
	command  ExecCommand
	issued   bool
	pending  Terminal
	terminal Outcome
}

// Command returns the sole uncommitted host-ready command, if any.
func (s State) Command() (ExecCommand, bool) {
	if s.command.stepID == "" || s.issued || s.terminal != nil {
		return ExecCommand{}, false
	}
	return s.command, true
}

// PendingTerminal returns the terminal append derived from one observation.
func (s State) PendingTerminal() (Terminal, bool) {
	if s.pending.outcome == nil || s.terminal != nil {
		return Terminal{}, false
	}
	return s.pending, true
}

// Terminal reports whether a matching terminal record has committed.
func (s State) Terminal() bool { return s.terminal != nil }

// Outcome returns the committed terminal outcome, if any.
func (s State) Outcome() Outcome { return s.terminal }

// Fold deterministically validates and folds the private records for one Program.
func Fold(candidate program.Program, records []Record) (State, error) {
	validated, err := program.ValidateProgram(candidate)
	if err != nil {
		return State{}, failure(-1, err.Error())
	}
	if len(records) == 0 {
		return State{}, failure(-1, "genesis is required")
	}
	genesis, ok := records[0].(Genesis)
	if !ok || genesis.hostRunKey == "" {
		return State{}, failure(0, "first record must be genesis with a host run key")
	}
	command, degraded, err := deriveCommand(validated, genesis)
	if err != nil {
		return State{}, failure(0, err.Error())
	}
	state := State{command: command}
	for index, record := range records[1:] {
		if state.terminal != nil {
			return State{}, failure(index+1, "journal cannot reopen after terminal")
		}
		switch typed := record.(type) {
		case Genesis:
			return State{}, failure(index+1, "genesis may occur only once")
		case CommandIssued:
			if err := foldIssued(&state, typed); err != nil {
				return State{}, failure(index+1, err.Error())
			}
		case Observation:
			if err := foldObservation(&state, typed, degraded); err != nil {
				return State{}, failure(index+1, err.Error())
			}
		case Terminal:
			if err := foldTerminal(&state, typed); err != nil {
				return State{}, failure(index+1, err.Error())
			}
		default:
			return State{}, failure(index+1, "record is not admitted")
		}
	}
	return state, nil
}

func foldIssued(state *State, record CommandIssued) error {
	if state.command.stepID == "" {
		return fmt.Errorf("program has no exec command")
	}
	if state.issued {
		return fmt.Errorf("command is already committed")
	}
	if record.hostRunKey != state.command.hostRunKey || record.sequence != 1 {
		return fmt.Errorf("command correlation is not contiguous")
	}
	if record.stepID != state.command.stepID {
		return fmt.Errorf("command step does not match derived command")
	}
	state.issued = true
	return nil
}

func foldObservation(state *State, record Observation, degraded *Degradation) error {
	if !state.issued {
		return fmt.Errorf("observation precedes command commitment")
	}
	if state.pending.outcome != nil {
		return fmt.Errorf("duplicate observation")
	}
	if record.hostRunKey != state.command.hostRunKey || record.sequence != 1 {
		return fmt.Errorf("observation correlation is not contiguous")
	}
	switch observed := record.value.(type) {
	case execObservation:
		if err := validateTermination(observed.termination); err != nil {
			return err
		}
		state.pending = Terminal{hostRunKey: state.command.hostRunKey, sequence: 1, outcome: project(state.command, observed, degraded)}
	case canceledObservation:
		if observed.reason == "" {
			return fmt.Errorf("cancellation reason is required")
		}
		state.pending = Terminal{hostRunKey: state.command.hostRunKey, sequence: 1, outcome: NewCanceled(observed.reason)}
	default:
		return fmt.Errorf("observation value is not admitted")
	}
	return nil
}

func foldTerminal(state *State, record Terminal) error {
	if state.pending.outcome == nil {
		return fmt.Errorf("terminal record precedes derived observation outcome")
	}
	if record.hostRunKey != state.command.hostRunKey || record.sequence != 1 {
		return fmt.Errorf("terminal correlation is not contiguous")
	}
	if !sameOutcome(record.outcome, state.pending.outcome) {
		return fmt.Errorf("terminal outcome does not match derived outcome")
	}
	state.terminal = record.outcome
	return nil
}

func project(command ExecCommand, observation execObservation, degraded *Degradation) Outcome {
	if exit, ok := observation.termination.(ExitTermination); ok && contains(command.pass, int(exit)) {
		return Succeeded{result: ExecResult(observation), degradation: degraded}
	}
	reason := failureReason(observation.termination)
	return Failed{reason: reason, detail: ExecFailure(observation)}
}

func failureReason(termination ExecTermination) string {
	switch typed := termination.(type) {
	case ExitTermination:
		return "exit_" + strconv.Itoa(int(typed))
	case SignalTermination:
		return "signal"
	case SpawnErrorTermination:
		return "not_executable"
	default:
		return "termination"
	}
}

func deriveCommand(candidate program.Program, genesis Genesis) (ExecCommand, *Degradation, error) {
	leaf, err := admitLeaf(candidate.Formula().Steps())
	if err != nil {
		return ExecCommand{}, nil, err
	}
	bindings, err := bindingsFor(candidate.Formula().Input(), genesis.bindings)
	if err != nil {
		return ExecCommand{}, nil, err
	}
	script, err := renderText(leaf.exec.Script(), bindings)
	if err != nil {
		return ExecCommand{}, nil, fmt.Errorf("render exec script: %w", err)
	}
	cwd, cwdSet, err := renderOptional(leaf.exec.CWD(), bindings)
	if err != nil {
		return ExecCommand{}, nil, fmt.Errorf("render exec cwd: %w", err)
	}
	stdin, stdinSet, err := renderOptional(leaf.exec.Stdin(), bindings)
	if err != nil {
		return ExecCommand{}, nil, fmt.Errorf("render exec stdin: %w", err)
	}
	command := ExecCommand{
		hostRunKey: genesis.hostRunKey,
		sequence:   1,
		stepID:     leaf.exec.ID(),
		script:     script,
		cwd:        cwd,
		cwdSet:     cwdSet,
		stdin:      stdin,
		stdinSet:   stdinSet,
		pass:       leaf.exec.ExitMap().Pass(),
	}
	for _, environment := range leaf.exec.Environment() {
		value, null, err := renderExpr(environment.Value(), bindings)
		if err != nil {
			return ExecCommand{}, nil, fmt.Errorf("render exec env %q: %w", environment.Key(), err)
		}
		command.env = append(command.env, RenderedEnvironment{key: environment.Key(), value: value, remove: null})
	}
	return command, leaf.degradation, nil
}

func renderOptional(expr program.Expr, bindings map[string]program.Scalar) (string, bool, error) {
	if expr == nil {
		return "", false, nil
	}
	value, null, err := renderExpr(expr, bindings)
	if err != nil {
		return "", false, err
	}
	return value, !null, nil
}

func bindingsFor(input program.Input, supplied []InputBinding) (map[string]program.Scalar, error) {
	bindings := make(map[string]program.Scalar, len(supplied))
	fields := input.Fields()
	declared := make(map[string]struct{}, len(fields))
	for _, field := range fields {
		declared[field.Name()] = struct{}{}
	}
	for _, binding := range supplied {
		if binding.name == "" || binding.value == nil {
			return nil, fmt.Errorf("genesis input binding is malformed")
		}
		if _, exists := declared[binding.name]; !exists {
			return nil, fmt.Errorf("genesis input %q is not declared", binding.name)
		}
		if _, exists := bindings[binding.name]; exists {
			return nil, fmt.Errorf("duplicate genesis input %q", binding.name)
		}
		bindings[binding.name] = binding.value
	}
	for _, field := range fields {
		if _, exists := bindings[field.Name()]; field.Required() && !exists {
			return nil, fmt.Errorf("required input %q is unbound", field.Name())
		}
	}
	return bindings, nil
}

func renderText(text program.InterpolatedText, bindings map[string]program.Scalar) (string, error) {
	var rendered strings.Builder
	for _, part := range text.Parts() {
		switch typed := part.(type) {
		case program.Text:
			rendered.WriteString(typed.Value())
		case program.Interpolation:
			value, null, err := renderExpr(typed.Expr(), bindings)
			if err != nil {
				return "", err
			}
			if !null {
				rendered.WriteString(value)
			}
		default:
			return "", fmt.Errorf("unsupported text part")
		}
	}
	return rendered.String(), nil
}

func renderExpr(expr program.Expr, bindings map[string]program.Scalar) (string, bool, error) {
	switch typed := expr.(type) {
	case program.Literal:
		return renderScalar(typed.Value())
	case program.Reference:
		value, ok := bindings[typed.Name()]
		if !ok {
			return "", false, fmt.Errorf("input %q is unbound", typed.Name())
		}
		return renderScalar(value)
	default:
		return "", false, fmt.Errorf("unsupported expression")
	}
}

func renderScalar(value program.Scalar) (string, bool, error) {
	switch typed := value.(type) {
	case program.Null:
		return "", true, nil
	case program.String:
		return string(typed), false, nil
	case program.Boolean:
		return strconv.FormatBool(bool(typed)), false, nil
	case program.Number:
		return strconv.FormatFloat(float64(typed), 'f', -1, 64), false, nil
	default:
		return "", false, fmt.Errorf("unsupported scalar")
	}
}

type leafProgram struct {
	exec        program.Exec
	degradation *Degradation
}

func admitLeaf(steps []program.Step) (leafProgram, error) {
	if len(steps) < 1 || len(steps) > 2 {
		return leafProgram{}, fmt.Errorf("program must contain one exec block and at most one terminal")
	}
	block, ok := steps[0].(program.Block)
	if !ok {
		return leafProgram{}, fmt.Errorf("program must begin with the compiler's exec block")
	}
	if len(block.Dependencies()) != 0 {
		return leafProgram{}, fmt.Errorf("exec block must not depend on another step")
	}
	members := block.Members()
	if len(members) != 1 {
		return leafProgram{}, fmt.Errorf("exec block must contain exactly one exec")
	}
	exec, ok := members[0].(program.Exec)
	if !ok {
		return leafProgram{}, fmt.Errorf("exec block must contain exactly one exec")
	}
	if len(exec.Dependencies()) != 0 {
		return leafProgram{}, fmt.Errorf("exec must not depend on another step in the leaf slice")
	}
	leaf := leafProgram{exec: exec}
	if len(steps) == 1 {
		return leaf, nil
	}
	terminal, ok := steps[1].(program.Terminal)
	if !ok {
		return leafProgram{}, fmt.Errorf("exec block may be followed only by a terminal")
	}
	dependencies := terminal.Dependencies()
	if len(dependencies) != 1 || dependencies[0] != block.ID() {
		return leafProgram{}, fmt.Errorf("terminal must follow the exec block")
	}
	degraded, ok := terminal.Outcome().(program.Degraded)
	if !ok {
		return leafProgram{}, fmt.Errorf("only authored degradation is admitted by the exec leaf")
	}
	leaf.degradation = &Degradation{reason: degraded.Reason()}
	return leaf, nil
}

func contains(values []int, target int) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func validateTermination(termination ExecTermination) error {
	switch typed := termination.(type) {
	case ExitTermination:
		if typed < 0 {
			return fmt.Errorf("exit termination must be non-negative")
		}
	case SignalTermination:
		if typed == "" {
			return fmt.Errorf("signal termination is required")
		}
	case SpawnErrorTermination:
		if typed == "" {
			return fmt.Errorf("spawn error termination is required")
		}
	default:
		return fmt.Errorf("observation termination is required")
	}
	return nil
}

func sameOutcome(left, right Outcome) bool {
	switch typed := left.(type) {
	case Succeeded:
		other, ok := right.(Succeeded)
		return ok && sameResult(typed.result, other.result) && sameDegradation(typed.degradation, other.degradation)
	case Failed:
		other, ok := right.(Failed)
		return ok && typed.reason == other.reason && sameFailure(typed.detail, other.detail)
	case Skipped:
		other, ok := right.(Skipped)
		return ok && typed.reason == other.reason
	case Canceled:
		other, ok := right.(Canceled)
		return ok && typed.reason == other.reason
	default:
		return false
	}
}

func sameResult(left, right ExecResult) bool {
	return left.stdout == right.stdout && left.stderr == right.stderr && sameTermination(left.termination, right.termination)
}

func sameFailure(left, right ExecFailure) bool {
	return left.stdout == right.stdout && left.stderr == right.stderr && sameTermination(left.termination, right.termination)
}

func sameDegradation(left, right *Degradation) bool {
	if left == nil || right == nil {
		return left == right
	}
	return left.reason == right.reason
}

func sameTermination(left, right ExecTermination) bool {
	switch typed := left.(type) {
	case ExitTermination:
		other, ok := right.(ExitTermination)
		return ok && typed == other
	case SignalTermination:
		other, ok := right.(SignalTermination)
		return ok && typed == other
	case SpawnErrorTermination:
		other, ok := right.(SpawnErrorTermination)
		return ok && typed == other
	default:
		return false
	}
}
func failure(index int, problem string) error { return &FoldError{Index: index, Problem: problem} }
