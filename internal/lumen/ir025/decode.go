// Package ir025 owns the strict private JSON edge for compiler-emitted
// lumen.ir version 0.2.5 evidence.
package ir025

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"

	"github.com/gastownhall/gascity/internal/lumen/program"
)

const (
	contractNameIR025     = "lumen.ir"
	contractVersionIR025  = "0.2.5"
	contractProducerIR025 = "donbox/formula-language"
)

// DecodeError reports compiler evidence that cannot enter the semantic model.
type DecodeError struct {
	Path    string
	Problem string
}

func (e *DecodeError) Error() string {
	return fmt.Sprintf("invalid Lumen ir025 at %s: %s", e.Path, e.Problem)
}

// Decode projects one compiler result into the sole engine-facing Program.
func Decode(data []byte) (program.Program, error) {
	result, err := decodeResultIR025(data)
	if err != nil {
		return program.Program{}, err
	}
	if err := requireFirstCohortClosureIR025(result); err != nil {
		return program.Program{}, err
	}
	formula, err := decodeFormulaIR025(result.formula, "formula")
	if err != nil {
		return program.Program{}, err
	}
	decoded, err := program.ValidateProgram(program.NewProgram(formula))
	if err != nil {
		return program.Program{}, &DecodeError{Path: "formula", Problem: err.Error()}
	}
	return decoded, nil
}

type resultIR025 struct {
	formula          json.RawMessage
	formulas         []json.RawMessage
	selfStepFormulas []json.RawMessage
	modules          []json.RawMessage
	exports          []json.RawMessage
	agents           []json.RawMessage
	sessions         []json.RawMessage
	stepDeclarations []json.RawMessage
	typeAliases      []json.RawMessage
	diagnostics      []compilerDiagnosticIR025
}

type compilerDiagnosticIR025 struct {
	Severity string `json:"severity"`
}

func decodeResultIR025(data []byte) (resultIR025, error) {
	object, err := objectIR025(data, "compiler result", []string{"formula", "formulas", "selfStepFormulas", "modules", "exports", "agents", "sessions", "stepDeclarations", "typeAliases", "diagnostics"})
	if err != nil {
		return resultIR025{}, err
	}
	result := resultIR025{formula: object["formula"]}
	for key, destination := range map[string]*[]json.RawMessage{
		"formulas": &result.formulas, "selfStepFormulas": &result.selfStepFormulas,
		"modules": &result.modules, "exports": &result.exports, "agents": &result.agents,
		"sessions": &result.sessions, "stepDeclarations": &result.stepDeclarations, "typeAliases": &result.typeAliases,
	} {
		if err := decodeIR025(object[key], destination); err != nil {
			return resultIR025{}, failureIR025("compiler result."+key, "must be an array")
		}
	}
	var diagnostics []compilerDiagnosticIR025
	if err := decodeIR025(object["diagnostics"], &diagnostics); err != nil {
		return resultIR025{}, failureIR025("compiler result.diagnostics", "must be an array")
	}
	for index, diagnostic := range diagnostics {
		if diagnostic.Severity == "error" {
			return resultIR025{}, failureIR025(fmt.Sprintf("compiler result.diagnostics[%d]", index), "contains an error diagnostic")
		}
		result.diagnostics = append(result.diagnostics, diagnostic)
	}
	if bytes.Equal(bytes.TrimSpace(result.formula), []byte("null")) {
		return resultIR025{}, failureIR025("compiler result.formula", "must select a formula")
	}
	return result, nil
}

func requireFirstCohortClosureIR025(result resultIR025) error {
	for key, values := range map[string][]json.RawMessage{
		"selfStepFormulas": result.selfStepFormulas, "modules": result.modules, "exports": result.exports,
		"agents": result.agents, "sessions": result.sessions, "stepDeclarations": result.stepDeclarations, "typeAliases": result.typeAliases,
	} {
		if len(values) != 0 {
			return failureIR025("compiler result."+key, "is not admitted in the first cohort")
		}
	}
	if len(result.formulas) != 1 {
		return failureIR025("compiler result.formulas", "must contain exactly the selected formula")
	}
	if !sameJSONIR025(result.formula, result.formulas[0]) {
		return failureIR025("compiler result.formulas[0]", "must equal the selected formula")
	}
	return nil
}

func decodeFormulaIR025(data []byte, path string) (program.Formula, error) {
	object, err := objectIR025(data, path, []string{"contract", "name", "input", "nodes", "origin"})
	if err != nil {
		return program.Formula{}, err
	}
	if err := validateContractIR025(object["contract"], path+".contract"); err != nil {
		return program.Formula{}, err
	}
	name, err := stringIR025(object["name"], path+".name")
	if err != nil {
		return program.Formula{}, err
	}
	input, err := decodeInputIR025(object["input"], path+".input")
	if err != nil {
		return program.Formula{}, err
	}
	if err := validateOriginIR025(object["origin"], path+".origin"); err != nil {
		return program.Formula{}, err
	}
	var rawNodes []json.RawMessage
	if err := decodeIR025(object["nodes"], &rawNodes); err != nil {
		return program.Formula{}, failureIR025(path+".nodes", "must be an array")
	}
	steps := make([]program.Step, len(rawNodes))
	for index, rawNode := range rawNodes {
		step, err := decodeStepIR025(rawNode, fmt.Sprintf("%s.nodes[%d]", path, index))
		if err != nil {
			return program.Formula{}, err
		}
		steps[index] = step
	}
	return program.NewFormula(name, input, steps), nil
}

func validateContractIR025(data []byte, path string) error {
	object, err := objectIR025(data, path, []string{"name", "version", "producer"})
	if err != nil {
		return err
	}
	name, err := stringIR025(object["name"], path+".name")
	if err != nil {
		return err
	}
	version, err := stringIR025(object["version"], path+".version")
	if err != nil {
		return err
	}
	producer, err := stringIR025(object["producer"], path+".producer")
	if err != nil {
		return err
	}
	if name != contractNameIR025 || version != contractVersionIR025 || producer != contractProducerIR025 {
		return failureIR025(path, "does not match the pinned lumen.ir 0.2.5 contract")
	}
	return nil
}

func decodeInputIR025(data []byte, path string) (program.Input, error) {
	object, err := objectIR025(data, path, []string{"name", "fields", "origin"})
	if err != nil {
		return program.Input{}, err
	}
	name, err := stringIR025(object["name"], path+".name")
	if err != nil {
		return program.Input{}, err
	}
	if err := validateOriginIR025(object["origin"], path+".origin"); err != nil {
		return program.Input{}, err
	}
	var rawFields []json.RawMessage
	if err := decodeIR025(object["fields"], &rawFields); err != nil {
		return program.Input{}, failureIR025(path+".fields", "must be an array")
	}
	fields := make([]program.Field, len(rawFields))
	for index, rawField := range rawFields {
		field, err := decodeFieldIR025(rawField, fmt.Sprintf("%s.fields[%d]", path, index))
		if err != nil {
			return program.Input{}, err
		}
		fields[index] = field
	}
	return program.NewInput(name, fields), nil
}

func decodeFieldIR025(data []byte, path string) (program.Field, error) {
	object, err := objectIR025(data, path, []string{"name", "type", "required", "body", "origin"})
	if err != nil {
		return program.Field{}, err
	}
	name, err := stringIR025(object["name"], path+".name")
	if err != nil {
		return program.Field{}, err
	}
	var required bool
	if err := decodeIR025(object["required"], &required); err != nil {
		return program.Field{}, failureIR025(path+".required", "must be a boolean")
	}
	var body bool
	if err := decodeIR025(object["body"], &body); err != nil {
		return program.Field{}, failureIR025(path+".body", "must be a boolean")
	}
	if body {
		return program.Field{}, failureIR025(path+".body", "is not admitted in the first cohort")
	}
	if err := validateOriginIR025(object["origin"], path+".origin"); err != nil {
		return program.Field{}, err
	}
	typ, err := decodeTypeIR025(object["type"], path+".type")
	if err != nil {
		return program.Field{}, err
	}
	return program.NewField(name, typ, required), nil
}

func decodeTypeIR025(data []byte, path string) (program.Type, error) {
	object, err := arbitraryObjectIR025(data, path)
	if err != nil {
		return nil, err
	}
	kind, err := rawStringIR025(object, "kind", path+".kind")
	if err != nil {
		return nil, err
	}
	switch kind {
	case "atomic":
		if err := exactKeysIR025(object, path, []string{"kind", "name", "origin"}); err != nil {
			return nil, err
		}
		name, err := rawStringIR025(object, "name", path+".name")
		if err != nil {
			return nil, err
		}
		if err := validateOriginIR025(object["origin"], path+".origin"); err != nil {
			return nil, err
		}
		return program.NewAtomicType(name), nil
	case "record":
		if err := exactKeysIR025(object, path, []string{"kind", "fields", "origin"}); err != nil {
			return nil, err
		}
		if err := validateOriginIR025(object["origin"], path+".origin"); err != nil {
			return nil, err
		}
		var rawFields []json.RawMessage
		if err := decodeIR025(object["fields"], &rawFields); err != nil {
			return nil, failureIR025(path+".fields", "must be an array")
		}
		fields := make([]program.Field, len(rawFields))
		for index, rawField := range rawFields {
			field, err := decodeFieldIR025(rawField, fmt.Sprintf("%s.fields[%d]", path, index))
			if err != nil {
				return nil, err
			}
			fields[index] = field
		}
		return program.NewRecordType(fields), nil
	case "array":
		if err := exactKeysIR025(object, path, []string{"kind", "element", "origin"}); err != nil {
			return nil, err
		}
		if err := validateOriginIR025(object["origin"], path+".origin"); err != nil {
			return nil, err
		}
		element, err := decodeTypeIR025(object["element"], path+".element")
		if err != nil {
			return nil, err
		}
		return program.NewArrayType(element), nil
	default:
		return nil, failureIR025(path+".kind", "is not an admitted type kind")
	}
}

func decodeStepIR025(data []byte, path string) (program.Step, error) {
	object, err := arbitraryObjectIR025(data, path)
	if err != nil {
		return nil, err
	}
	kind, err := rawStringIR025(object, "kind", path+".kind")
	if err != nil {
		return nil, err
	}
	switch kind {
	case "block":
		if err := exactKeysIR025(object, path, []string{"kind", "id", "after", "origin", "members"}); err != nil {
			return nil, err
		}
		id, after, err := commonStepIR025(object, path)
		if err != nil {
			return nil, err
		}
		var rawMembers []json.RawMessage
		if err := decodeIR025(object["members"], &rawMembers); err != nil {
			return nil, failureIR025(path+".members", "must be an array")
		}
		members := make([]program.Step, len(rawMembers))
		for index, rawMember := range rawMembers {
			member, err := decodeStepIR025(rawMember, fmt.Sprintf("%s.members[%d]", path, index))
			if err != nil {
				return nil, err
			}
			members[index] = member
		}
		return program.NewBlock(id, after, members), nil
	case "exec":
		if err := knownKeysIR025(object, path, []string{"kind", "id", "after", "origin", "interpreter", "body", "exitMap"}, []string{"cwd", "env", "stdin"}); err != nil {
			return nil, err
		}
		id, after, err := commonStepIR025(object, path)
		if err != nil {
			return nil, err
		}
		if err := validateInterpreterIR025(object["interpreter"], path+".interpreter"); err != nil {
			return nil, err
		}
		script, err := decodeBodyIR025(object["body"], path+".body")
		if err != nil {
			return nil, err
		}
		cwd, err := decodeOptionalExprIR025(object["cwd"], path+".cwd")
		if err != nil {
			return nil, err
		}
		stdin, err := decodeOptionalExprIR025(object["stdin"], path+".stdin")
		if err != nil {
			return nil, err
		}
		env, err := decodeEnvironmentIR025(object["env"], path+".env")
		if err != nil {
			return nil, err
		}
		exitMap, err := decodeExitMapIR025(object["exitMap"], path+".exitMap")
		if err != nil {
			return nil, err
		}
		return program.NewExec(id, after, script, cwd, env, stdin, exitMap), nil
	case "settle":
		id, after, err := commonStepIR025(object, path)
		if err != nil {
			return nil, err
		}
		name, outcome, err := decodeOutcomeIR025(object, path)
		if err != nil {
			return nil, err
		}
		return program.NewTerminal(id, name, after, outcome), nil
	case "scatter":
		return nil, failureIR025(path+".kind", "retired scatter is not admitted")
	case "extern":
		return nil, failureIR025(path+".kind", "deferred extern is not admitted")
	default:
		return nil, failureIR025(path+".kind", "is not an admitted step kind")
	}
}

func commonStepIR025(object map[string]json.RawMessage, path string) (string, []string, error) {
	id, err := rawStringIR025(object, "id", path+".id")
	if err != nil {
		return "", nil, err
	}
	var after []string
	if err := decodeIR025(object["after"], &after); err != nil {
		return "", nil, failureIR025(path+".after", "must be an array of strings")
	}
	if err := validateOriginIR025(object["origin"], path+".origin"); err != nil {
		return "", nil, err
	}
	return id, after, nil
}

func validateInterpreterIR025(data []byte, path string) error {
	object, err := objectIR025(data, path, []string{"kind", "program", "origin"})
	if err != nil {
		return err
	}
	kind, err := stringIR025(object["kind"], path+".kind")
	if err != nil {
		return err
	}
	programObject, err := objectIR025(object["program"], path+".program", []string{"kind"})
	if err != nil {
		return err
	}
	programKind, err := stringIR025(programObject["kind"], path+".program.kind")
	if err != nil {
		return err
	}
	if kind != "shell" || programKind != "exec" {
		return failureIR025(path, "must be the admitted shell exec interpreter")
	}
	return validateOriginIR025(object["origin"], path+".origin")
}

func decodeBodyIR025(data []byte, path string) (program.InterpolatedText, error) {
	object, err := objectIR025(data, path, []string{"raw", "template", "source", "templated", "language", "syntax", "origin"})
	if err != nil {
		return program.InterpolatedText{}, err
	}
	if _, err := stringIR025(object["raw"], path+".raw"); err != nil {
		return program.InterpolatedText{}, err
	}
	var templated bool
	if err := decodeIR025(object["templated"], &templated); err != nil || !templated {
		return program.InterpolatedText{}, failureIR025(path+".templated", "must be true")
	}
	language, err := stringIR025(object["language"], path+".language")
	if err != nil || language != "bash" {
		return program.InterpolatedText{}, failureIR025(path+".language", "must be bash")
	}
	syntax, err := stringIR025(object["syntax"], path+".syntax")
	if err != nil {
		return program.InterpolatedText{}, err
	}
	if syntax != "bare" && syntax != "quoted" && syntax != "fenced" {
		return program.InterpolatedText{}, failureIR025(path+".syntax", "is not an admitted normalized syntax")
	}
	if err := validateOriginIR025(object["origin"], path+".origin"); err != nil {
		return program.InterpolatedText{}, err
	}
	source, err := objectIR025(object["source"], path+".source", []string{"kind"})
	if err != nil {
		return program.InterpolatedText{}, err
	}
	if kind, err := stringIR025(source["kind"], path+".source.kind"); err != nil || kind != "inline" {
		return program.InterpolatedText{}, failureIR025(path+".source.kind", "must be inline")
	}
	template, err := objectIR025(object["template"], path+".template", []string{"parts"})
	if err != nil {
		return program.InterpolatedText{}, err
	}
	var rawParts []json.RawMessage
	if err := decodeIR025(template["parts"], &rawParts); err != nil {
		return program.InterpolatedText{}, failureIR025(path+".template.parts", "must be an array")
	}
	parts := make([]program.TextPart, len(rawParts))
	for index, rawPart := range rawParts {
		part, err := decodeTextPartIR025(rawPart, fmt.Sprintf("%s.template.parts[%d]", path, index))
		if err != nil {
			return program.InterpolatedText{}, err
		}
		parts[index] = part
	}
	return program.NewInterpolatedText(parts), nil
}

func decodeTextPartIR025(data []byte, path string) (program.TextPart, error) {
	object, err := arbitraryObjectIR025(data, path)
	if err != nil {
		return nil, err
	}
	kind, err := rawStringIR025(object, "kind", path+".kind")
	if err != nil {
		return nil, err
	}
	switch kind {
	case "text":
		if err := exactKeysIR025(object, path, []string{"kind", "value"}); err != nil {
			return nil, err
		}
		value, err := rawStringIR025(object, "value", path+".value")
		if err != nil {
			return nil, err
		}
		return program.NewText(value), nil
	case "interp":
		if err := exactKeysIR025(object, path, []string{"kind", "expr", "origin"}); err != nil {
			return nil, err
		}
		if err := validateOriginIR025(object["origin"], path+".origin"); err != nil {
			return nil, err
		}
		expr, err := decodeExprIR025(object["expr"], path+".expr")
		if err != nil {
			return nil, err
		}
		return program.NewInterpolation(expr), nil
	default:
		return nil, failureIR025(path+".kind", "is not an admitted interpolation part")
	}
}

func decodeEnvironmentIR025(data []byte, path string) ([]program.Environment, error) {
	if data == nil {
		return nil, nil
	}
	var rawEntries []json.RawMessage
	if err := decodeIR025(data, &rawEntries); err != nil {
		return nil, failureIR025(path, "must be an array")
	}
	entries := make([]program.Environment, len(rawEntries))
	for index, rawEntry := range rawEntries {
		entryPath := fmt.Sprintf("%s[%d]", path, index)
		object, err := objectIR025(rawEntry, entryPath, []string{"key", "value"})
		if err != nil {
			return nil, err
		}
		key, err := stringIR025(object["key"], entryPath+".key")
		if err != nil {
			return nil, err
		}
		value, err := decodeExprIR025(object["value"], entryPath+".value")
		if err != nil {
			return nil, err
		}
		entries[index] = program.NewEnvironment(key, value)
	}
	return entries, nil
}

func decodeOptionalExprIR025(data []byte, path string) (program.Expr, error) {
	if data == nil {
		return nil, nil
	}
	return decodeExprIR025(data, path)
}

func decodeExprIR025(data []byte, path string) (program.Expr, error) {
	object, err := arbitraryObjectIR025(data, path)
	if err != nil {
		return nil, err
	}
	kind, err := rawStringIR025(object, "kind", path+".kind")
	if err != nil {
		return nil, err
	}
	switch kind {
	case "literal":
		if err := exactKeysIR025(object, path, []string{"kind", "value"}); err != nil {
			return nil, err
		}
		value, err := decodeScalarIR025(object["value"], path+".value")
		if err != nil {
			return nil, err
		}
		return program.NewLiteral(value), nil
	case "ref":
		if err := exactKeysIR025(object, path, []string{"kind", "name", "origin"}); err != nil {
			return nil, err
		}
		name, err := rawStringIR025(object, "name", path+".name")
		if err != nil {
			return nil, err
		}
		if err := validateOriginIR025(object["origin"], path+".origin"); err != nil {
			return nil, err
		}
		return program.NewReference(name), nil
	case "object":
		if err := exactKeysIR025(object, path, []string{"kind", "entries"}); err != nil {
			return nil, err
		}
		var rawEntries []json.RawMessage
		if err := decodeIR025(object["entries"], &rawEntries); err != nil {
			return nil, failureIR025(path+".entries", "must be an array")
		}
		entries := make([]program.RecordEntry, len(rawEntries))
		for index, rawEntry := range rawEntries {
			entryPath := fmt.Sprintf("%s.entries[%d]", path, index)
			entry, err := objectIR025(rawEntry, entryPath, []string{"key", "value"})
			if err != nil {
				return nil, err
			}
			key, err := stringIR025(entry["key"], entryPath+".key")
			if err != nil {
				return nil, err
			}
			value, err := decodeExprIR025(entry["value"], entryPath+".value")
			if err != nil {
				return nil, err
			}
			entries[index] = program.NewRecordEntry(key, value)
		}
		return program.NewRecord(entries), nil
	case "array":
		if err := exactKeysIR025(object, path, []string{"kind", "elements"}); err != nil {
			return nil, err
		}
		var rawElements []json.RawMessage
		if err := decodeIR025(object["elements"], &rawElements); err != nil {
			return nil, failureIR025(path+".elements", "must be an array")
		}
		elements := make([]program.Expr, len(rawElements))
		for index, rawElement := range rawElements {
			element, err := decodeExprIR025(rawElement, fmt.Sprintf("%s.elements[%d]", path, index))
			if err != nil {
				return nil, err
			}
			elements[index] = element
		}
		return program.NewArray(elements), nil
	default:
		return nil, failureIR025(path+".kind", "is not an admitted expression kind")
	}
}

func decodeScalarIR025(data []byte, path string) (program.Scalar, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, failureIR025(path, "must be a scalar")
	}
	if decoder.More() {
		return nil, failureIR025(path, "must contain one JSON value")
	}
	switch typed := value.(type) {
	case nil:
		return program.Null{}, nil
	case string:
		return program.String(typed), nil
	case bool:
		return program.Boolean(typed), nil
	case json.Number:
		number, err := typed.Float64()
		if err != nil {
			return nil, failureIR025(path, "contains an invalid number")
		}
		return program.Number(number), nil
	default:
		return nil, failureIR025(path, "must be null, string, boolean, or number")
	}
}

func decodeOutcomeIR025(object map[string]json.RawMessage, path string) (string, program.Outcome, error) {
	value, err := rawStringIR025(object, "outcome", path+".outcome")
	if err != nil {
		return "", nil, err
	}
	name := ""
	if rawName, ok := object["name"]; ok {
		name, err = stringIR025(rawName, path+".name")
		if err != nil {
			return "", nil, err
		}
	}
	publicOutcome, err := rawBoolIR025(object, "publicOutcome", path+".publicOutcome")
	if err != nil {
		return "", nil, err
	}
	if !publicOutcome {
		return "", nil, failureIR025(path+".publicOutcome", "must be true")
	}
	switch value {
	case "succeeded":
		if err := knownKeysIR025(object, path, []string{"kind", "id", "after", "origin", "outcome", "value", "publicOutcome"}, []string{"name"}); err != nil {
			return "", nil, err
		}
		terminalValue, err := decodeExprIR025(object["value"], path+".value")
		if err != nil {
			return "", nil, err
		}
		return name, program.NewSucceeded(terminalValue), nil
	case "degraded":
		if err := knownKeysIR025(object, path, []string{"kind", "id", "after", "origin", "outcome", "reason", "publicOutcome"}, []string{"name", "value"}); err != nil {
			return "", nil, err
		}
		reason, err := rawStringIR025(object, "reason", path+".reason")
		if err != nil {
			return "", nil, err
		}
		var terminalValue program.Expr
		if rawValue, ok := object["value"]; ok {
			terminalValue, err = decodeExprIR025(rawValue, path+".value")
			if err != nil {
				return "", nil, err
			}
		}
		return name, program.NewDegraded(terminalValue, reason), nil
	case "failed":
		if err := knownKeysIR025(object, path, []string{"kind", "id", "after", "origin", "outcome", "reason", "publicOutcome"}, []string{"name"}); err != nil {
			return "", nil, err
		}
		reason, err := rawStringIR025(object, "reason", path+".reason")
		if err != nil {
			return "", nil, err
		}
		return name, program.NewFailed(reason), nil
	case "skipped":
		if err := knownKeysIR025(object, path, []string{"kind", "id", "after", "origin", "outcome", "reason", "publicOutcome"}, []string{"name"}); err != nil {
			return "", nil, err
		}
		reason, err := rawStringIR025(object, "reason", path+".reason")
		if err != nil {
			return "", nil, err
		}
		return name, program.NewSkipped(reason), nil
	default:
		return "", nil, failureIR025(path+".outcome", "is not an admitted terminal outcome")
	}
}

func decodeExitMapIR025(data []byte, path string) (program.ExitMap, error) {
	object, err := objectIR025(data, path, []string{"pass", "retryable"})
	if err != nil {
		return program.ExitMap{}, err
	}
	var pass, retryable []int
	if err := decodeIR025(object["pass"], &pass); err != nil {
		return program.ExitMap{}, failureIR025(path+".pass", "must be an array of integers")
	}
	if err := decodeIR025(object["retryable"], &retryable); err != nil {
		return program.ExitMap{}, failureIR025(path+".retryable", "must be an array of integers")
	}
	return program.NewExitMap(pass, retryable), nil
}

func validateOriginIR025(data []byte, path string) error {
	object, err := objectIR025(data, path, []string{"uri", "line", "col"})
	if err != nil {
		return err
	}
	if _, err := stringIR025(object["uri"], path+".uri"); err != nil {
		return err
	}
	for _, key := range []string{"line", "col"} {
		var value int
		if err := decodeIR025(object[key], &value); err != nil {
			return failureIR025(path+"."+key, "must be an integer")
		}
		if value < 0 {
			return failureIR025(path+"."+key, "must not be negative")
		}
	}
	return nil
}

func objectIR025(data []byte, path string, keys []string) (map[string]json.RawMessage, error) {
	object, err := arbitraryObjectIR025(data, path)
	if err != nil {
		return nil, err
	}
	if err := exactKeysIR025(object, path, keys); err != nil {
		return nil, err
	}
	return object, nil
}

func arbitraryObjectIR025(data []byte, path string) (map[string]json.RawMessage, error) {
	var object map[string]json.RawMessage
	if err := decodeIR025(data, &object); err != nil || object == nil {
		return nil, failureIR025(path, "must be an object")
	}
	return object, nil
}

func exactKeysIR025(object map[string]json.RawMessage, path string, keys []string) error {
	if len(object) != len(keys) {
		return failureIR025(path, "has unknown or missing fields")
	}
	for _, key := range keys {
		if _, ok := object[key]; !ok {
			return failureIR025(path+"."+key, "is required")
		}
	}
	return nil
}

func knownKeysIR025(object map[string]json.RawMessage, path string, required, optional []string) error {
	allowed := make(map[string]struct{}, len(required)+len(optional))
	for _, key := range required {
		allowed[key] = struct{}{}
		if _, ok := object[key]; !ok {
			return failureIR025(path+"."+key, "is required")
		}
	}
	for _, key := range optional {
		allowed[key] = struct{}{}
	}
	for key := range object {
		if _, ok := allowed[key]; !ok {
			return failureIR025(path+"."+key, "is not admitted")
		}
	}
	return nil
}

func stringIR025(data []byte, path string) (string, error) {
	var value string
	if err := decodeIR025(data, &value); err != nil {
		return "", failureIR025(path, "must be a string")
	}
	return value, nil
}

func rawStringIR025(object map[string]json.RawMessage, key, path string) (string, error) {
	data, ok := object[key]
	if !ok {
		return "", failureIR025(path, "is required")
	}
	return stringIR025(data, path)
}

func rawBoolIR025(object map[string]json.RawMessage, key, path string) (bool, error) {
	data, ok := object[key]
	if !ok {
		return false, failureIR025(path, "is required")
	}
	var value bool
	if err := decodeIR025(data, &value); err != nil {
		return false, failureIR025(path, "must be a boolean")
	}
	return value, nil
}

func decodeIR025(data []byte, destination any) error {
	if bytes.Equal(bytes.TrimSpace(data), []byte("null")) {
		return fmt.Errorf("null JSON value")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return fmt.Errorf("trailing JSON")
	}
	return nil
}
func failureIR025(path, problem string) error { return &DecodeError{Path: path, Problem: problem} }

func sameJSONIR025(left, right []byte) bool {
	var compactLeft, compactRight bytes.Buffer
	return json.Compact(&compactLeft, left) == nil && json.Compact(&compactRight, right) == nil && bytes.Equal(compactLeft.Bytes(), compactRight.Bytes())
}
