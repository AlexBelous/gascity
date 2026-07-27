package admission

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"path/filepath"

	"github.com/gastownhall/gascity/internal/graphstore"
	"github.com/gastownhall/gascity/internal/graphstore/canon"
	"github.com/gastownhall/gascity/internal/lumen/kernel"
	"github.com/gastownhall/gascity/internal/lumen/program"
)

const (
	genesisRecordType     = "lumen.admission.genesis"
	issuedRecordType      = "lumen.kernel.issued"
	observationRecordType = "lumen.kernel.observation"
	terminalRecordType    = "lumen.kernel.terminal"
)

type snapshot struct {
	Key         HostRunKey      `json:"key"`
	Source      []byte          `json:"source"`
	Program     []byte          `json:"program"`
	Inputs      []inputSnapshot `json:"inputs"`
	Environment []string        `json:"environment"`
	Base        string          `json:"base"`
}

type inputSnapshot struct {
	Name  string        `json:"name"`
	Value valueSnapshot `json:"value"`
}

type valueSnapshot struct {
	Kind    valueKind `json:"kind"`
	String  *string   `json:"string"`
	Boolean *bool     `json:"boolean"`
	Number  *float64  `json:"number"`
}

type observationSnapshot struct {
	Kind        string               `json:"kind"`
	Stdout      *string              `json:"stdout"`
	Stderr      *string              `json:"stderr"`
	Termination *terminationSnapshot `json:"termination"`
	Reason      *string              `json:"reason"`
}

type terminationSnapshot struct {
	Kind  string  `json:"kind"`
	Exit  *int    `json:"exit"`
	Value *string `json:"value"`
}

func encodeGenesis(value snapshot) ([]byte, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("lumen admission: encode genesis: %w", err)
	}
	canonical, err := canon.Canonicalize(encoded)
	if err != nil {
		return nil, fmt.Errorf("lumen admission: encode genesis: %w", err)
	}
	return canonical, nil
}

func decodeGenesis(data []byte, expected HostRunKey) (snapshot, error) {
	var value snapshot
	if err := decodeCanonical(data, &value); err != nil {
		return snapshot{}, fmt.Errorf("lumen admission: decode genesis: %w", err)
	}
	if value.Key == "" || value.Key != expected {
		return snapshot{}, fmt.Errorf("lumen admission: genesis key %q does not match stream key %q", value.Key, expected)
	}
	if len(value.Source) == 0 || len(value.Program) == 0 || !filepath.IsAbs(value.Base) {
		return snapshot{}, fmt.Errorf("lumen admission: genesis is incomplete")
	}
	for _, input := range value.Inputs {
		if input.Name == "" {
			return snapshot{}, fmt.Errorf("lumen admission: genesis input name is required")
		}
		if _, err := decodeValue(input.Value); err != nil {
			return snapshot{}, err
		}
	}
	return value, nil
}

func captureInputs(candidate program.Program, supplied []Input) ([]inputSnapshot, []kernel.InputBinding, error) {
	provided := make(map[string]Value, len(supplied))
	for index, input := range supplied {
		if input.name == "" {
			return nil, nil, fmt.Errorf("lumen admission: input %d has no name", index)
		}
		if input.value.kind == "" {
			return nil, nil, fmt.Errorf("lumen admission: input %q has no value", input.name)
		}
		if _, exists := provided[input.name]; exists {
			return nil, nil, fmt.Errorf("lumen admission: duplicate input %q", input.name)
		}
		provided[input.name] = input.value
	}
	fields := candidate.Formula().Input().Fields()
	inputs := make([]inputSnapshot, 0, len(fields))
	bindings := make([]kernel.InputBinding, 0, len(fields))
	for _, field := range fields {
		if err := validateInputType(field.Type()); err != nil {
			return nil, nil, fmt.Errorf("lumen admission: input %q: %w", field.Name(), err)
		}
		value, ok := provided[field.Name()]
		if !ok {
			if field.Required() {
				return nil, nil, fmt.Errorf("lumen admission: required input %q is not supplied", field.Name())
			}
			inputs = append(inputs, inputSnapshot{Name: field.Name(), Value: valueSnapshot{Kind: nullValue}})
			bindings = append(bindings, kernel.NewInputBinding(field.Name(), program.Null{}))
			continue
		}
		scalar, err := validateValueType(value, field.Type())
		if err != nil {
			return nil, nil, fmt.Errorf("lumen admission: input %q: %w", field.Name(), err)
		}
		encoded, err := encodeValue(value)
		if err != nil {
			return nil, nil, err
		}
		inputs = append(inputs, inputSnapshot{Name: field.Name(), Value: encoded})
		bindings = append(bindings, kernel.NewInputBinding(field.Name(), scalar))
		delete(provided, field.Name())
	}
	for name := range provided {
		return nil, nil, fmt.Errorf("lumen admission: input %q is not declared", name)
	}
	return inputs, bindings, nil
}

func validateInputType(typ program.Type) error {
	atomic, ok := typ.(program.AtomicType)
	if !ok {
		return fmt.Errorf("non-atomic inputs are not supported")
	}
	switch atomic.Name() {
	case "string", "bool", "number", "null":
		return nil
	default:
		return fmt.Errorf("unsupported atomic type %q", atomic.Name())
	}
}

func validateValueType(value Value, typ program.Type) (program.Scalar, error) {
	if err := validateInputType(typ); err != nil {
		return nil, err
	}
	atomic, ok := typ.(program.AtomicType)
	if !ok {
		return nil, fmt.Errorf("non-atomic inputs are not supported")
	}
	switch atomic.Name() {
	case "string":
		if value.kind != stringValue {
			return nil, fmt.Errorf("must be a string")
		}
		return program.String(value.text), nil
	case "bool":
		if value.kind != boolValue {
			return nil, fmt.Errorf("must be a bool")
		}
		return program.Boolean(value.boolean), nil
	case "number":
		if value.kind != numberValue || math.IsNaN(value.number) || math.IsInf(value.number, 0) {
			return nil, fmt.Errorf("must be a finite number")
		}
		return program.Number(value.number), nil
	case "null":
		if value.kind != nullValue {
			return nil, fmt.Errorf("must be null")
		}
		return program.Null{}, nil
	default:
		return nil, fmt.Errorf("unsupported atomic type %q", atomic.Name())
	}
}

func encodeValue(value Value) (valueSnapshot, error) {
	switch value.kind {
	case stringValue:
		text := value.text
		return valueSnapshot{Kind: stringValue, String: &text}, nil
	case boolValue:
		boolean := value.boolean
		return valueSnapshot{Kind: boolValue, Boolean: &boolean}, nil
	case numberValue:
		if math.IsNaN(value.number) || math.IsInf(value.number, 0) {
			return valueSnapshot{}, fmt.Errorf("lumen admission: number must be finite")
		}
		number := value.number
		return valueSnapshot{Kind: numberValue, Number: &number}, nil
	case nullValue:
		return valueSnapshot{Kind: nullValue}, nil
	default:
		return valueSnapshot{}, fmt.Errorf("lumen admission: unsupported value")
	}
}

func decodeValue(value valueSnapshot) (Value, error) {
	switch value.Kind {
	case stringValue:
		if value.String == nil || value.Boolean != nil || value.Number != nil {
			return Value{}, fmt.Errorf("lumen admission: invalid string value")
		}
		return NewStringValue(*value.String), nil
	case boolValue:
		if value.String != nil || value.Boolean == nil || value.Number != nil {
			return Value{}, fmt.Errorf("lumen admission: invalid bool value")
		}
		return NewBooleanValue(*value.Boolean), nil
	case numberValue:
		if value.String != nil || value.Boolean != nil || value.Number == nil ||
			math.IsNaN(*value.Number) || math.IsInf(*value.Number, 0) {
			return Value{}, fmt.Errorf("lumen admission: invalid number value")
		}
		return NewNumberValue(*value.Number), nil
	case nullValue:
		if value.String != nil || value.Boolean != nil || value.Number != nil {
			return Value{}, fmt.Errorf("lumen admission: invalid null value")
		}
		return NewNullValue(), nil
	default:
		return Value{}, fmt.Errorf("lumen admission: unknown value kind %q", value.Kind)
	}
}

func decodeJournal(
	stored []graphstore.Record,
	expected HostRunKey,
) (program.Program, []kernel.Record, []string, string, error) {
	if len(stored) == 0 || stored[0].Type() != genesisRecordType {
		return program.Program{}, nil, nil, "", fmt.Errorf("lumen admission: missing genesis")
	}
	genesis, err := decodeGenesis(stored[0].Payload(), expected)
	if err != nil {
		return program.Program{}, nil, nil, "", err
	}
	candidate, err := program.DecodeSnapshot(genesis.Program)
	if err != nil {
		return program.Program{}, nil, nil, "", err
	}
	fields := candidate.Formula().Input().Fields()
	if len(genesis.Inputs) != len(fields) {
		return program.Program{}, nil, nil, "", fmt.Errorf(
			"lumen admission: genesis has %d inputs, want %d", len(genesis.Inputs), len(fields),
		)
	}
	bindings := make([]kernel.InputBinding, len(genesis.Inputs))
	for index, input := range genesis.Inputs {
		field := fields[index]
		if input.Name != field.Name() {
			return program.Program{}, nil, nil, "", fmt.Errorf(
				"lumen admission: genesis input %d is %q, want %q", index, input.Name, field.Name(),
			)
		}
		value, err := decodeValue(input.Value)
		if err != nil {
			return program.Program{}, nil, nil, "", err
		}
		if err := validateInputType(field.Type()); err != nil {
			return program.Program{}, nil, nil, "", fmt.Errorf(
				"lumen admission: input %q: %w", input.Name, err,
			)
		}
		var scalar program.Scalar
		if value.kind == nullValue && !field.Required() {
			scalar = program.Null{}
		} else {
			scalar, err = validateValueType(value, field.Type())
			if err != nil {
				return program.Program{}, nil, nil, "", err
			}
		}
		bindings[index] = kernel.NewInputBinding(input.Name, scalar)
	}
	records := []kernel.Record{kernel.NewGenesis(kernel.HostRunKey(expected), bindings)}
	for _, record := range stored[1:] {
		state, err := kernel.Fold(candidate, records)
		if err != nil {
			return program.Program{}, nil, nil, "", fmt.Errorf("lumen admission: refold journal: %w", err)
		}
		switch record.Type() {
		case issuedRecordType:
			if err := decodeEmptyMarker(record.Payload()); err != nil {
				return program.Program{}, nil, nil, "", err
			}
			command, ok := state.Command()
			if !ok {
				return program.Program{}, nil, nil, "", fmt.Errorf("lumen admission: issued marker has no command")
			}
			records = append(records, kernel.NewCommandIssued(
				command.HostRunKey(), command.PrivateSequence(), command.StepID(),
			))
		case observationRecordType:
			command, ok := state.IssuedCommand()
			if !ok {
				return program.Program{}, nil, nil, "", fmt.Errorf("lumen admission: observation has no issued command")
			}
			observation, err := decodeObservation(record.Payload(), command)
			if err != nil {
				return program.Program{}, nil, nil, "", err
			}
			if _, err := validateObservation(candidate, records, observation); err != nil {
				return program.Program{}, nil, nil, "", err
			}
			records = append(records, observation)
		case terminalRecordType:
			if err := decodeEmptyMarker(record.Payload()); err != nil {
				return program.Program{}, nil, nil, "", err
			}
			terminal, ok := state.PendingTerminal()
			if !ok {
				return program.Program{}, nil, nil, "", fmt.Errorf("lumen admission: terminal marker has no pending terminal")
			}
			records = append(records, terminal)
		default:
			return program.Program{}, nil, nil, "", fmt.Errorf("lumen admission: unknown journal record %q", record.Type())
		}
	}
	return candidate, records, append([]string(nil), genesis.Environment...), genesis.Base, nil
}

func encodeObservation(observation kernel.Observation) ([]byte, error) {
	var value observationSnapshot
	if reason, ok := observation.Cancellation(); ok {
		if reason == "" {
			return nil, fmt.Errorf("lumen admission: cancellation reason is required")
		}
		value.Kind = "canceled"
		value.Reason = &reason
	} else {
		stdout, stderr, termination, ok := observation.Execution()
		if !ok {
			return nil, fmt.Errorf("lumen admission: observation value is required")
		}
		encodedTermination, err := encodeTermination(termination)
		if err != nil {
			return nil, err
		}
		value.Kind = "execution"
		value.Stdout = &stdout
		value.Stderr = &stderr
		value.Termination = &encodedTermination
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("lumen admission: encode observation: %w", err)
	}
	canonical, err := canon.Canonicalize(encoded)
	if err != nil {
		return nil, fmt.Errorf("lumen admission: encode observation: %w", err)
	}
	return canonical, nil
}

func decodeObservation(data []byte, command kernel.ExecCommand) (kernel.Observation, error) {
	var value observationSnapshot
	if err := decodeCanonical(data, &value); err != nil {
		return kernel.Observation{}, fmt.Errorf("lumen admission: decode observation: %w", err)
	}
	switch value.Kind {
	case "canceled":
		if value.Reason == nil || *value.Reason == "" ||
			value.Stdout != nil || value.Stderr != nil || value.Termination != nil {
			return kernel.Observation{}, fmt.Errorf("lumen admission: invalid canceled observation")
		}
		return kernel.NewCanceledObservation(command.HostRunKey(), command.PrivateSequence(), *value.Reason), nil
	case "execution":
		if value.Reason != nil || value.Stdout == nil || value.Stderr == nil || value.Termination == nil {
			return kernel.Observation{}, fmt.Errorf("lumen admission: invalid execution observation")
		}
		termination, err := decodeTermination(*value.Termination)
		if err != nil {
			return kernel.Observation{}, err
		}
		return kernel.NewObservation(
			command.HostRunKey(), command.PrivateSequence(),
			*value.Stdout, *value.Stderr, termination,
		), nil
	default:
		return kernel.Observation{}, fmt.Errorf("lumen admission: unknown observation kind %q", value.Kind)
	}
}

func encodeTermination(termination kernel.ExecTermination) (terminationSnapshot, error) {
	switch typed := termination.(type) {
	case kernel.ExitTermination:
		exit := int(typed)
		return terminationSnapshot{Kind: "exit", Exit: &exit}, nil
	case kernel.SignalTermination:
		value := string(typed)
		return terminationSnapshot{Kind: "signal", Value: &value}, nil
	case kernel.SpawnErrorTermination:
		value := string(typed)
		return terminationSnapshot{Kind: "spawn", Value: &value}, nil
	default:
		return terminationSnapshot{}, fmt.Errorf("lumen admission: unknown execution termination")
	}
}

func decodeTermination(value terminationSnapshot) (kernel.ExecTermination, error) {
	switch value.Kind {
	case "exit":
		if value.Exit == nil || *value.Exit < 0 || value.Value != nil {
			return nil, fmt.Errorf("lumen admission: invalid exit termination")
		}
		return kernel.ExitTermination(*value.Exit), nil
	case "signal":
		if value.Exit != nil || value.Value == nil || *value.Value == "" {
			return nil, fmt.Errorf("lumen admission: invalid signal termination")
		}
		return kernel.SignalTermination(*value.Value), nil
	case "spawn":
		if value.Exit != nil || value.Value == nil || *value.Value == "" {
			return nil, fmt.Errorf("lumen admission: invalid spawn termination")
		}
		return kernel.SpawnErrorTermination(*value.Value), nil
	default:
		return nil, fmt.Errorf("lumen admission: unknown termination kind %q", value.Kind)
	}
}

func decodeEmptyMarker(data []byte) error {
	if !bytes.Equal(data, []byte("{}")) {
		return fmt.Errorf("lumen admission: marker must be empty")
	}
	return nil
}

func decodeCanonical(data []byte, destination any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	var trailing struct{}
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return fmt.Errorf("trailing data")
		}
		return fmt.Errorf("trailing data: %w", err)
	}
	reencoded, err := json.Marshal(destination)
	if err != nil {
		return err
	}
	canonical, err := canon.Canonicalize(reencoded)
	if err != nil {
		return err
	}
	if !bytes.Equal(canonical, data) {
		return fmt.Errorf("value is not canonical")
	}
	return nil
}
