package program

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
)

const snapshotVersion = 1

type programSnapshot struct {
	Version  int               `json:"version"`
	Name     string            `json:"name"`
	Input    inputSnapshot     `json:"input"`
	Block    blockSnapshot     `json:"block"`
	Terminal *terminalSnapshot `json:"terminal"`
}

type inputSnapshot struct {
	Name   string          `json:"name"`
	Fields []fieldSnapshot `json:"fields"`
}

type fieldSnapshot struct {
	Name     string       `json:"name"`
	Type     typeSnapshot `json:"type"`
	Required bool         `json:"required"`
}

type typeSnapshot struct {
	Kind    string           `json:"kind"`
	Name    *string          `json:"name"`
	Fields  *[]fieldSnapshot `json:"fields"`
	Element *typeSnapshot    `json:"element"`
}

type blockSnapshot struct {
	ID      string       `json:"id"`
	After   []string     `json:"after"`
	Execute execSnapshot `json:"execute"`
}

type execSnapshot struct {
	ID          string                `json:"id"`
	After       []string              `json:"after"`
	Script      textSnapshot          `json:"script"`
	CWD         *exprSnapshot         `json:"cwd"`
	Environment []environmentSnapshot `json:"environment"`
	Stdin       *exprSnapshot         `json:"stdin"`
	Pass        []int                 `json:"pass"`
	Retryable   []int                 `json:"retryable"`
}

type environmentSnapshot struct {
	Key   string       `json:"key"`
	Value exprSnapshot `json:"value"`
}

type terminalSnapshot struct {
	ID     string        `json:"id"`
	Name   string        `json:"name"`
	After  []string      `json:"after"`
	Value  *exprSnapshot `json:"value"`
	Reason string        `json:"reason"`
}

type exprSnapshot struct {
	Kind      string                 `json:"kind"`
	Literal   *scalarSnapshot        `json:"literal"`
	Reference *string                `json:"reference"`
	Record    *[]recordEntrySnapshot `json:"record"`
	Array     *[]exprSnapshot        `json:"array"`
	Text      *textSnapshot          `json:"text"`
}

type scalarSnapshot struct {
	Kind    string   `json:"kind"`
	String  *string  `json:"string"`
	Boolean *bool    `json:"boolean"`
	Number  *float64 `json:"number"`
}

type recordEntrySnapshot struct {
	Key   string       `json:"key"`
	Value exprSnapshot `json:"value"`
}

type textSnapshot struct {
	Parts []textPartSnapshot `json:"parts"`
}

type textPartSnapshot struct {
	Kind string        `json:"kind"`
	Text *string       `json:"text"`
	Expr *exprSnapshot `json:"expr"`
}

// EncodeSnapshot returns a strict, self-contained snapshot of one validated
// exec-leaf Program.
func EncodeSnapshot(candidate Program) ([]byte, error) {
	validated, err := ValidateProgram(candidate)
	if err != nil {
		return nil, fmt.Errorf("encode Lumen program snapshot: %w", err)
	}
	formula := validated.formula
	if len(formula.steps) < 1 || len(formula.steps) > 2 {
		return nil, fmt.Errorf("encode Lumen program snapshot: program is outside the exec-leaf cohort")
	}
	block, ok := formula.steps[0].(Block)
	if !ok || len(block.after) != 0 || len(block.members) != 1 {
		return nil, fmt.Errorf("encode Lumen program snapshot: program is outside the exec-leaf cohort")
	}
	execute, ok := block.members[0].(Exec)
	if !ok || len(execute.after) != 0 {
		return nil, fmt.Errorf("encode Lumen program snapshot: program is outside the exec-leaf cohort")
	}

	input, err := encodeInputSnapshot(formula.input)
	if err != nil {
		return nil, err
	}
	execDTO, err := encodeExecSnapshot(execute)
	if err != nil {
		return nil, err
	}
	dto := programSnapshot{
		Version: snapshotVersion,
		Name:    formula.name,
		Input:   input,
		Block:   blockSnapshot{ID: block.id, After: cloneStrings(block.after), Execute: execDTO},
	}
	if len(formula.steps) == 2 {
		terminal, ok := formula.steps[1].(Terminal)
		if !ok || len(terminal.after) != 1 || terminal.after[0] != block.id {
			return nil, fmt.Errorf("encode Lumen program snapshot: program is outside the exec-leaf cohort")
		}
		degraded, ok := terminal.outcome.(Degraded)
		if !ok {
			return nil, fmt.Errorf("encode Lumen program snapshot: program is outside the exec-leaf cohort")
		}
		var value *exprSnapshot
		if degraded.value != nil {
			encoded, err := encodeExprSnapshot(degraded.value)
			if err != nil {
				return nil, err
			}
			value = &encoded
		}
		dto.Terminal = &terminalSnapshot{
			ID: terminal.id, Name: terminal.name, After: cloneStrings(terminal.after),
			Value: value, Reason: degraded.reason,
		}
	}
	encoded, err := json.Marshal(dto)
	if err != nil {
		return nil, fmt.Errorf("encode Lumen program snapshot: %w", err)
	}
	return encoded, nil
}

// DecodeSnapshot strictly decodes and validates one exec-leaf Program snapshot.
func DecodeSnapshot(data []byte) (Program, error) {
	var dto programSnapshot
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&dto); err != nil {
		return Program{}, fmt.Errorf("decode Lumen program snapshot: %w", err)
	}
	if err := requireSnapshotEOF(decoder); err != nil {
		return Program{}, err
	}
	if dto.Version != snapshotVersion {
		return Program{}, fmt.Errorf("decode Lumen program snapshot: unsupported version %d", dto.Version)
	}
	input, err := decodeInputSnapshot(dto.Input)
	if err != nil {
		return Program{}, err
	}
	execute, err := decodeExecSnapshot(dto.Block.Execute)
	if err != nil {
		return Program{}, err
	}
	steps := []Step{NewBlock(dto.Block.ID, dto.Block.After, []Step{execute})}
	if dto.Terminal != nil {
		var value Expr
		if dto.Terminal.Value != nil {
			value, err = decodeExprSnapshot(*dto.Terminal.Value)
			if err != nil {
				return Program{}, err
			}
		}
		steps = append(steps, NewTerminal(
			dto.Terminal.ID,
			dto.Terminal.Name,
			dto.Terminal.After,
			NewDegraded(value, dto.Terminal.Reason),
		))
	}
	decoded, err := ValidateProgram(NewProgram(NewFormula(dto.Name, input, steps)))
	if err != nil {
		return Program{}, fmt.Errorf("decode Lumen program snapshot: %w", err)
	}
	reencoded, err := EncodeSnapshot(decoded)
	if err != nil {
		return Program{}, fmt.Errorf("decode Lumen program snapshot: %w", err)
	}
	if !bytes.Equal(reencoded, data) {
		return Program{}, fmt.Errorf("decode Lumen program snapshot: snapshot is not canonical")
	}
	return decoded, nil
}

func requireSnapshotEOF(decoder *json.Decoder) error {
	var trailing struct{}
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return fmt.Errorf("decode Lumen program snapshot: trailing data")
		}
		return fmt.Errorf("decode Lumen program snapshot: trailing data: %w", err)
	}
	return nil
}

func encodeInputSnapshot(input Input) (inputSnapshot, error) {
	fields := make([]fieldSnapshot, len(input.fields))
	for index, field := range input.fields {
		typ, err := encodeTypeSnapshot(field.typ)
		if err != nil {
			return inputSnapshot{}, err
		}
		fields[index] = fieldSnapshot{Name: field.name, Type: typ, Required: field.required}
	}
	return inputSnapshot{Name: input.name, Fields: fields}, nil
}

func decodeInputSnapshot(input inputSnapshot) (Input, error) {
	fields := make([]Field, len(input.Fields))
	for index, field := range input.Fields {
		typ, err := decodeTypeSnapshot(field.Type)
		if err != nil {
			return Input{}, err
		}
		fields[index] = NewField(field.Name, typ, field.Required)
	}
	return NewInput(input.Name, fields), nil
}

func encodeTypeSnapshot(typ Type) (typeSnapshot, error) {
	switch typed := typ.(type) {
	case AtomicType:
		name := typed.name
		return typeSnapshot{Kind: "atomic", Name: &name}, nil
	case RecordType:
		fields := make([]fieldSnapshot, len(typed.fields))
		for index, field := range typed.fields {
			fieldType, err := encodeTypeSnapshot(field.typ)
			if err != nil {
				return typeSnapshot{}, err
			}
			fields[index] = fieldSnapshot{Name: field.name, Type: fieldType, Required: field.required}
		}
		return typeSnapshot{Kind: "record", Fields: &fields}, nil
	case ArrayType:
		element, err := encodeTypeSnapshot(typed.element)
		if err != nil {
			return typeSnapshot{}, err
		}
		return typeSnapshot{Kind: "array", Element: &element}, nil
	default:
		return typeSnapshot{}, fmt.Errorf("encode Lumen program snapshot: unsupported type")
	}
}

func decodeTypeSnapshot(typ typeSnapshot) (Type, error) {
	switch typ.Kind {
	case "atomic":
		if typ.Name == nil || typ.Fields != nil || typ.Element != nil {
			return nil, fmt.Errorf("decode Lumen program snapshot: invalid atomic type")
		}
		return NewAtomicType(*typ.Name), nil
	case "record":
		if typ.Name != nil || typ.Fields == nil || typ.Element != nil {
			return nil, fmt.Errorf("decode Lumen program snapshot: invalid record type")
		}
		fields := make([]Field, len(*typ.Fields))
		for index, field := range *typ.Fields {
			fieldType, err := decodeTypeSnapshot(field.Type)
			if err != nil {
				return nil, err
			}
			fields[index] = NewField(field.Name, fieldType, field.Required)
		}
		return NewRecordType(fields), nil
	case "array":
		if typ.Name != nil || typ.Fields != nil || typ.Element == nil {
			return nil, fmt.Errorf("decode Lumen program snapshot: invalid array type")
		}
		element, err := decodeTypeSnapshot(*typ.Element)
		if err != nil {
			return nil, err
		}
		return NewArrayType(element), nil
	default:
		return nil, fmt.Errorf("decode Lumen program snapshot: unknown type kind %q", typ.Kind)
	}
}

func encodeExecSnapshot(execute Exec) (execSnapshot, error) {
	script, err := encodeTextSnapshot(execute.script)
	if err != nil {
		return execSnapshot{}, err
	}
	cwd, err := encodeOptionalExprSnapshot(execute.cwd)
	if err != nil {
		return execSnapshot{}, err
	}
	stdin, err := encodeOptionalExprSnapshot(execute.stdin)
	if err != nil {
		return execSnapshot{}, err
	}
	environment := make([]environmentSnapshot, len(execute.env))
	for index, item := range execute.env {
		value, err := encodeExprSnapshot(item.value)
		if err != nil {
			return execSnapshot{}, err
		}
		environment[index] = environmentSnapshot{Key: item.key, Value: value}
	}
	return execSnapshot{
		ID: execute.id, After: cloneStrings(execute.after), Script: script, CWD: cwd,
		Environment: environment, Stdin: stdin,
		Pass: cloneInts(execute.exitMap.pass), Retryable: cloneInts(execute.exitMap.retryable),
	}, nil
}

func decodeExecSnapshot(execute execSnapshot) (Exec, error) {
	script, err := decodeTextSnapshot(execute.Script)
	if err != nil {
		return Exec{}, err
	}
	cwd, err := decodeOptionalExprSnapshot(execute.CWD)
	if err != nil {
		return Exec{}, err
	}
	stdin, err := decodeOptionalExprSnapshot(execute.Stdin)
	if err != nil {
		return Exec{}, err
	}
	environment := make([]Environment, len(execute.Environment))
	for index, item := range execute.Environment {
		value, err := decodeExprSnapshot(item.Value)
		if err != nil {
			return Exec{}, err
		}
		environment[index] = NewEnvironment(item.Key, value)
	}
	return NewExec(
		execute.ID, execute.After, script, cwd, environment, stdin,
		NewExitMap(execute.Pass, execute.Retryable),
	), nil
}

func encodeOptionalExprSnapshot(expr Expr) (*exprSnapshot, error) {
	if expr == nil {
		return nil, nil
	}
	encoded, err := encodeExprSnapshot(expr)
	if err != nil {
		return nil, err
	}
	return &encoded, nil
}

func decodeOptionalExprSnapshot(expr *exprSnapshot) (Expr, error) {
	if expr == nil {
		return nil, nil
	}
	return decodeExprSnapshot(*expr)
}

func encodeExprSnapshot(expr Expr) (exprSnapshot, error) {
	switch typed := expr.(type) {
	case Literal:
		scalar, err := encodeScalarSnapshot(typed.value)
		return exprSnapshot{Kind: "literal", Literal: &scalar}, err
	case Reference:
		name := typed.name
		return exprSnapshot{Kind: "reference", Reference: &name}, nil
	case Record:
		entries := make([]recordEntrySnapshot, len(typed.entries))
		for index, entry := range typed.entries {
			value, err := encodeExprSnapshot(entry.value)
			if err != nil {
				return exprSnapshot{}, err
			}
			entries[index] = recordEntrySnapshot{Key: entry.key, Value: value}
		}
		return exprSnapshot{Kind: "record", Record: &entries}, nil
	case Array:
		elements := make([]exprSnapshot, len(typed.elements))
		for index, element := range typed.elements {
			encoded, err := encodeExprSnapshot(element)
			if err != nil {
				return exprSnapshot{}, err
			}
			elements[index] = encoded
		}
		return exprSnapshot{Kind: "array", Array: &elements}, nil
	case InterpolatedText:
		text, err := encodeTextSnapshot(typed)
		return exprSnapshot{Kind: "text", Text: &text}, err
	default:
		return exprSnapshot{}, fmt.Errorf("encode Lumen program snapshot: unsupported expression")
	}
}

func decodeExprSnapshot(expr exprSnapshot) (Expr, error) {
	switch expr.Kind {
	case "literal":
		if expr.Literal == nil || expr.Reference != nil || expr.Record != nil || expr.Array != nil || expr.Text != nil {
			return nil, fmt.Errorf("decode Lumen program snapshot: invalid literal expression")
		}
		value, err := decodeScalarSnapshot(*expr.Literal)
		if err != nil {
			return nil, err
		}
		return NewLiteral(value), nil
	case "reference":
		if expr.Literal != nil || expr.Reference == nil || expr.Record != nil || expr.Array != nil || expr.Text != nil {
			return nil, fmt.Errorf("decode Lumen program snapshot: invalid reference expression")
		}
		return NewReference(*expr.Reference), nil
	case "record":
		if expr.Literal != nil || expr.Reference != nil || expr.Record == nil || expr.Array != nil || expr.Text != nil {
			return nil, fmt.Errorf("decode Lumen program snapshot: invalid record expression")
		}
		entries := make([]RecordEntry, len(*expr.Record))
		for index, entry := range *expr.Record {
			value, err := decodeExprSnapshot(entry.Value)
			if err != nil {
				return nil, err
			}
			entries[index] = NewRecordEntry(entry.Key, value)
		}
		return NewRecord(entries), nil
	case "array":
		if expr.Literal != nil || expr.Reference != nil || expr.Record != nil || expr.Array == nil || expr.Text != nil {
			return nil, fmt.Errorf("decode Lumen program snapshot: invalid array expression")
		}
		elements := make([]Expr, len(*expr.Array))
		for index, element := range *expr.Array {
			decoded, err := decodeExprSnapshot(element)
			if err != nil {
				return nil, err
			}
			elements[index] = decoded
		}
		return NewArray(elements), nil
	case "text":
		if expr.Literal != nil || expr.Reference != nil || expr.Record != nil || expr.Array != nil || expr.Text == nil {
			return nil, fmt.Errorf("decode Lumen program snapshot: invalid text expression")
		}
		return decodeTextSnapshot(*expr.Text)
	default:
		return nil, fmt.Errorf("decode Lumen program snapshot: unknown expression kind %q", expr.Kind)
	}
}

func encodeScalarSnapshot(value Scalar) (scalarSnapshot, error) {
	switch typed := value.(type) {
	case Null:
		return scalarSnapshot{Kind: "null"}, nil
	case String:
		value := string(typed)
		return scalarSnapshot{Kind: "string", String: &value}, nil
	case Boolean:
		value := bool(typed)
		return scalarSnapshot{Kind: "bool", Boolean: &value}, nil
	case Number:
		value := float64(typed)
		return scalarSnapshot{Kind: "number", Number: &value}, nil
	default:
		return scalarSnapshot{}, fmt.Errorf("encode Lumen program snapshot: unsupported scalar")
	}
}

func decodeScalarSnapshot(value scalarSnapshot) (Scalar, error) {
	switch value.Kind {
	case "null":
		if value.String != nil || value.Boolean != nil || value.Number != nil {
			return nil, fmt.Errorf("decode Lumen program snapshot: invalid null scalar")
		}
		return Null{}, nil
	case "string":
		if value.String == nil || value.Boolean != nil || value.Number != nil {
			return nil, fmt.Errorf("decode Lumen program snapshot: invalid string scalar")
		}
		return String(*value.String), nil
	case "bool":
		if value.String != nil || value.Boolean == nil || value.Number != nil {
			return nil, fmt.Errorf("decode Lumen program snapshot: invalid bool scalar")
		}
		return Boolean(*value.Boolean), nil
	case "number":
		if value.String != nil || value.Boolean != nil || value.Number == nil {
			return nil, fmt.Errorf("decode Lumen program snapshot: invalid number scalar")
		}
		return Number(*value.Number), nil
	default:
		return nil, fmt.Errorf("decode Lumen program snapshot: unknown scalar kind %q", value.Kind)
	}
}

func encodeTextSnapshot(text InterpolatedText) (textSnapshot, error) {
	parts := make([]textPartSnapshot, len(text.parts))
	for index, part := range text.parts {
		switch typed := part.(type) {
		case Text:
			value := typed.value
			parts[index] = textPartSnapshot{Kind: "text", Text: &value}
		case Interpolation:
			expr, err := encodeExprSnapshot(typed.expr)
			if err != nil {
				return textSnapshot{}, err
			}
			parts[index] = textPartSnapshot{Kind: "interpolation", Expr: &expr}
		default:
			return textSnapshot{}, fmt.Errorf("encode Lumen program snapshot: unsupported text part")
		}
	}
	return textSnapshot{Parts: parts}, nil
}

func decodeTextSnapshot(text textSnapshot) (InterpolatedText, error) {
	parts := make([]TextPart, len(text.Parts))
	for index, part := range text.Parts {
		switch part.Kind {
		case "text":
			if part.Text == nil || part.Expr != nil {
				return InterpolatedText{}, fmt.Errorf("decode Lumen program snapshot: invalid text part")
			}
			parts[index] = NewText(*part.Text)
		case "interpolation":
			if part.Text != nil || part.Expr == nil {
				return InterpolatedText{}, fmt.Errorf("decode Lumen program snapshot: invalid interpolation part")
			}
			expr, err := decodeExprSnapshot(*part.Expr)
			if err != nil {
				return InterpolatedText{}, err
			}
			parts[index] = NewInterpolation(expr)
		default:
			return InterpolatedText{}, fmt.Errorf("decode Lumen program snapshot: unknown text part kind %q", part.Kind)
		}
	}
	return NewInterpolatedText(parts), nil
}
