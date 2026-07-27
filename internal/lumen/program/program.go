// Package program defines the immutable semantic program accepted by the
// Lumen execution engine.
package program

import (
	"fmt"
	"math"
	"reflect"
)

// Program is the semantic formula container. Engine-facing code receives only
// values returned by ValidateProgram, the sole legality check.
type Program struct{ formula Formula }

// Formula returns the program's formula declaration.
func (p Program) Formula() Formula { return p.formula.clone() }

// Formula is the typed formula declaration.
type Formula struct {
	name  string
	input Input
	steps []Step
}

// NewFormula builds an inert formula candidate. Call ValidateProgram before
// using the resulting program for execution.
func NewFormula(name string, input Input, steps []Step) Formula {
	return Formula{name: name, input: input.clone(), steps: cloneSteps(steps)}
}

// Name returns the formula name.
func (f Formula) Name() string { return f.name }

// Input returns the formula input declaration.
func (f Formula) Input() Input { return f.input.clone() }

// Steps returns a copy of the formula's top-level steps.
func (f Formula) Steps() []Step { return cloneSteps(f.steps) }

func (f Formula) clone() Formula { return NewFormula(f.name, f.input, f.steps) }

// Input is a typed formula input declaration.
type Input struct {
	name   string
	fields []Field
}

// NewInput builds a typed input declaration.
func NewInput(name string, fields []Field) Input {
	return Input{name: name, fields: cloneFields(fields)}
}

// Name returns the input declaration name.
func (i Input) Name() string { return i.name }

// Fields returns a copy of the input fields.
func (i Input) Fields() []Field { return cloneFields(i.fields) }

func (i Input) clone() Input { return NewInput(i.name, i.fields) }

// Field is one typed input field.
type Field struct {
	name     string
	typ      Type
	required bool
}

// NewField builds a typed input field.
func NewField(name string, typ Type, required bool) Field {
	return Field{name: name, typ: cloneType(typ), required: required}
}

// Name returns the field name.
func (f Field) Name() string { return f.name }

// Type returns the field type.
func (f Field) Type() Type { return cloneType(f.typ) }

// Required reports whether the caller must supply the field.
func (f Field) Required() bool { return f.required }

func (f Field) clone() Field { return NewField(f.name, f.typ, f.required) }

// Type is a closed input-type family.
type Type interface {
	typeMarker()
}

// AtomicType is an upstream atomic input type such as string or number.
type AtomicType struct{ name string }

// NewAtomicType builds an atomic input type.
func NewAtomicType(name string) AtomicType { return AtomicType{name: name} }

// Name returns the upstream atomic type name.
func (t AtomicType) Name() string { return t.name }

func (AtomicType) typeMarker() {}

// RecordType is a typed record input.
type RecordType struct{ fields []Field }

// NewRecordType builds a typed record input.
func NewRecordType(fields []Field) RecordType { return RecordType{fields: cloneFields(fields)} }

// Fields returns a copy of the record fields.
func (t RecordType) Fields() []Field { return cloneFields(t.fields) }

func (RecordType) typeMarker() {}

// ArrayType is a typed array input.
type ArrayType struct{ element Type }

// NewArrayType builds a typed array input.
func NewArrayType(element Type) ArrayType { return ArrayType{element: cloneType(element)} }

// Element returns the array element type.
func (t ArrayType) Element() Type { return cloneType(t.element) }

func (ArrayType) typeMarker() {}

// Step is a closed executable step family.
type Step interface {
	stepMarker()
	ID() string
	Dependencies() []string
}

// Block executes its members as one structured step.
type Block struct {
	id      string
	after   []string
	members []Step
}

// NewBlock builds a block step.
func NewBlock(id string, after []string, members []Step) Block {
	return Block{id: id, after: cloneStrings(after), members: cloneSteps(members)}
}

// ID returns the stable step identifier.
func (s Block) ID() string { return s.id }

// Dependencies returns a copy of predecessor step IDs.
func (s Block) Dependencies() []string { return cloneStrings(s.after) }

// Members returns a copy of the block members.
func (s Block) Members() []Step { return cloneSteps(s.members) }

func (Block) stepMarker() {}

// Exec executes an interpolated shell script.
type Exec struct {
	id      string
	after   []string
	script  InterpolatedText
	cwd     Expr
	env     []Environment
	stdin   Expr
	exitMap ExitMap
}

// NewExec builds an exec step with normalized exit-code semantics.
func NewExec(id string, after []string, script InterpolatedText, cwd Expr, env []Environment, stdin Expr, exitMap ExitMap) Exec {
	return Exec{id: id, after: cloneStrings(after), script: script.clone(), cwd: cloneExpr(cwd), env: cloneEnvironment(env), stdin: cloneExpr(stdin), exitMap: exitMap.clone()}
}

// ID returns the stable step identifier.
func (s Exec) ID() string { return s.id }

// Dependencies returns a copy of predecessor step IDs.
func (s Exec) Dependencies() []string { return cloneStrings(s.after) }

// Script returns the interpolated shell script.
func (s Exec) Script() InterpolatedText { return s.script.clone() }

// CWD returns the optional working-directory expression.
func (s Exec) CWD() Expr { return cloneExpr(s.cwd) }

// Environment returns a copy of environment assignments.
func (s Exec) Environment() []Environment { return cloneEnvironment(s.env) }

// Stdin returns the optional stdin expression.
func (s Exec) Stdin() Expr { return cloneExpr(s.stdin) }

// ExitMap returns the normalized exit-code semantics for the exec step.
func (s Exec) ExitMap() ExitMap { return s.exitMap.clone() }

func (Exec) stepMarker() {}

// ExitMap classifies shell exit codes without retaining compiler JSON.
type ExitMap struct {
	pass      []int
	retryable []int
}

// NewExitMap builds normalized shell exit-code semantics.
func NewExitMap(pass, retryable []int) ExitMap {
	return ExitMap{pass: cloneInts(pass), retryable: cloneInts(retryable)}
}

// Pass returns a copy of successful exit codes.
func (m ExitMap) Pass() []int { return cloneInts(m.pass) }

// Retryable returns a copy of retryable exit codes.
func (m ExitMap) Retryable() []int { return cloneInts(m.retryable) }

func (m ExitMap) clone() ExitMap { return NewExitMap(m.pass, m.retryable) }

// Terminal carries one of the four terminal outcomes.
type Terminal struct {
	id      string
	name    string
	after   []string
	outcome Outcome
}

// NewTerminal builds a terminal outcome carrier.
func NewTerminal(id, name string, after []string, outcome Outcome) Terminal {
	return Terminal{id: id, name: name, after: cloneStrings(after), outcome: cloneOutcome(outcome)}
}

// ID returns the stable step identifier.
func (s Terminal) ID() string { return s.id }

// Name returns the optional authored terminal binding name.
func (s Terminal) Name() string { return s.name }

// Dependencies returns a copy of predecessor step IDs.
func (s Terminal) Dependencies() []string { return cloneStrings(s.after) }

// Outcome returns the terminal outcome.
func (s Terminal) Outcome() Outcome { return cloneOutcome(s.outcome) }

func (Terminal) stepMarker() {}

// Outcome is the closed terminal outcome family.
type Outcome interface{ outcomeMarker() }

// Succeeded is a successful terminal outcome carrying its value.
type Succeeded struct{ value Expr }

// NewSucceeded builds a successful terminal outcome.
func NewSucceeded(value Expr) Succeeded { return Succeeded{value: cloneExpr(value)} }

// Value returns the successful terminal value.
func (o Succeeded) Value() Expr { return cloneExpr(o.value) }

func (Succeeded) outcomeMarker() {}

// Degraded is a degraded terminal outcome carrying its optional value and reason.
type Degraded struct {
	value  Expr
	reason string
}

// NewDegraded builds a degraded terminal outcome.
func NewDegraded(value Expr, reason string) Degraded {
	return Degraded{value: cloneExpr(value), reason: reason}
}

// Value returns the optional degraded value.
func (o Degraded) Value() Expr { return cloneExpr(o.value) }

// Reason returns the degradation reason.
func (o Degraded) Reason() string { return o.reason }

func (Degraded) outcomeMarker() {}

// Failed is a failed terminal outcome carrying its reason.
type Failed struct{ reason string }

// NewFailed builds a failed terminal outcome.
func NewFailed(reason string) Failed { return Failed{reason: reason} }

// Reason returns the failure reason.
func (o Failed) Reason() string { return o.reason }

func (Failed) outcomeMarker() {}

// Skipped is a skipped terminal outcome carrying its reason.
type Skipped struct{ reason string }

// NewSkipped builds a skipped terminal outcome.
func NewSkipped(reason string) Skipped { return Skipped{reason: reason} }

// Reason returns the skip reason.
func (o Skipped) Reason() string { return o.reason }

func (Skipped) outcomeMarker() {}

// Expr is a closed pure expression family.
type Expr interface{ exprMarker() }

// Literal is a scalar literal expression.
type Literal struct{ value Scalar }

// Scalar is a closed literal scalar family.
type Scalar interface{ scalarMarker() }

// Null is the null literal scalar.
type Null struct{}

func (Null) scalarMarker() {}

// String is the string literal scalar.
type String string

func (String) scalarMarker() {}

// Boolean is the boolean literal scalar.
type Boolean bool

func (Boolean) scalarMarker() {}

// Number is the finite number literal scalar.
type Number float64

func (Number) scalarMarker() {}

// NewLiteral builds a scalar literal expression.
func NewLiteral(value Scalar) Literal { return Literal{value: value} }

// Value returns the scalar literal value.
func (e Literal) Value() Scalar { return e.value }
func (Literal) exprMarker()     {}

// Reference is a proven input reference.
type Reference struct{ name string }

// NewReference builds a reference expression.
func NewReference(name string) Reference { return Reference{name: name} }

// Name returns the referenced input name.
func (e Reference) Name() string { return e.name }
func (Reference) exprMarker()    {}

// Record is a structural record expression.
type Record struct{ entries []RecordEntry }

// RecordEntry is one immutable record expression entry.
type RecordEntry struct {
	key   string
	value Expr
}

// NewRecord builds a record expression.
func NewRecord(entries []RecordEntry) Record { return Record{entries: cloneEntries(entries)} }

// NewRecordEntry builds an immutable record entry.
func NewRecordEntry(key string, value Expr) RecordEntry {
	return RecordEntry{key: key, value: cloneExpr(value)}
}

// Entries returns a copy of the record entries.
func (e Record) Entries() []RecordEntry { return cloneEntries(e.entries) }

// Key returns the entry key.
func (e RecordEntry) Key() string { return e.key }

// Value returns the entry expression.
func (e RecordEntry) Value() Expr { return cloneExpr(e.value) }

func (Record) exprMarker() {}

// Array is an ordered structural expression.
type Array struct{ elements []Expr }

// NewArray builds an array expression.
func NewArray(elements []Expr) Array { return Array{elements: cloneExprs(elements)} }

// Elements returns a copy of the array elements.
func (e Array) Elements() []Expr { return cloneExprs(e.elements) }
func (Array) exprMarker()        {}

// InterpolatedText is a text expression with text and expression parts.
type InterpolatedText struct{ parts []TextPart }

// NewInterpolatedText builds an interpolated text expression.
func NewInterpolatedText(parts []TextPart) InterpolatedText {
	return InterpolatedText{parts: cloneParts(parts)}
}

// Parts returns a copy of the interpolation parts.
func (e InterpolatedText) Parts() []TextPart       { return cloneParts(e.parts) }
func (e InterpolatedText) exprMarker()             {}
func (e InterpolatedText) clone() InterpolatedText { return NewInterpolatedText(e.parts) }

// TextPart is a closed interpolated-text part family.
type TextPart interface{ textPartMarker() }

// Text is literal text in an interpolation.
type Text struct{ value string }

// NewText builds a literal text part.
func NewText(value string) Text { return Text{value: value} }

// Value returns the literal text.
func (p Text) Value() string { return p.value }
func (Text) textPartMarker() {}

// Interpolation embeds a pure expression in text.
type Interpolation struct{ expr Expr }

// NewInterpolation builds an expression text part.
func NewInterpolation(expr Expr) Interpolation { return Interpolation{expr: cloneExpr(expr)} }

// Expr returns the interpolated expression.
func (p Interpolation) Expr() Expr    { return cloneExpr(p.expr) }
func (Interpolation) textPartMarker() {}

// Environment is one immutable exec environment assignment.
type Environment struct {
	key   string
	value Expr
}

// NewEnvironment builds an environment assignment.
func NewEnvironment(key string, value Expr) Environment {
	return Environment{key: key, value: cloneExpr(value)}
}

// Key returns the environment variable name.
func (e Environment) Key() string { return e.key }

// Value returns the assigned expression.
func (e Environment) Value() Expr { return cloneExpr(e.value) }

// NewProgram builds an inert program candidate. It must pass ValidateProgram
// before it is admitted to engine-facing code.
func NewProgram(formula Formula) Program { return Program{formula: formula.clone()} }

// ValidationError identifies an invalid semantic program before execution.
type ValidationError struct {
	Path    string
	Problem string
}

func (e *ValidationError) Error() string {
	return fmt.Sprintf("invalid Lumen program at %s: %s", e.Path, e.Problem)
}

// ValidateProgram is the sole legality ingress for engine-facing programs.
func ValidateProgram(candidate Program) (Program, error) {
	formula := candidate.formula
	if formula.name == "" {
		return Program{}, invalid("formula.name", "must not be empty")
	}
	if formula.input.name == "" {
		return Program{}, invalid("formula.input.name", "must not be empty")
	}
	known := make(map[string]struct{}, len(formula.input.fields))
	for index, field := range formula.input.fields {
		path := fmt.Sprintf("formula.input.fields[%d]", index)
		if field.name == "" {
			return Program{}, invalid(path+".name", "must not be empty")
		}
		if _, exists := known[field.name]; exists {
			return Program{}, invalid(path+".name", "is duplicated")
		}
		if !field.required {
			return Program{}, invalid(path+".required", "must be true in the admitted cohort")
		}
		if err := validateType(field.typ, path+".type"); err != nil {
			return Program{}, err
		}
		known[field.name] = struct{}{}
	}
	ids := make(map[string]struct{})
	if err := collectStepIDs(formula.steps, "formula.steps", ids); err != nil {
		return Program{}, err
	}
	if err := validateSteps(formula.steps, "formula.steps", ids, known); err != nil {
		return Program{}, err
	}
	if err := validateDependencyGraph(formula.steps); err != nil {
		return Program{}, err
	}
	return NewProgram(formula), nil
}

func invalid(path, problem string) error { return &ValidationError{Path: path, Problem: problem} }

func validateType(typ Type, path string) error {
	switch typed := typ.(type) {
	case AtomicType:
		if typed.name == "" {
			return invalid(path+".name", "must not be empty")
		}
	case RecordType:
		seen := map[string]struct{}{}
		for index, field := range typed.fields {
			fieldPath := fmt.Sprintf("%s.fields[%d]", path, index)
			if field.name == "" {
				return invalid(fieldPath+".name", "must not be empty")
			}
			if _, exists := seen[field.name]; exists {
				return invalid(fieldPath+".name", "is duplicated")
			}
			if err := validateType(field.typ, fieldPath+".type"); err != nil {
				return err
			}
			seen[field.name] = struct{}{}
		}
	case ArrayType:
		if typed.element == nil {
			return invalid(path+".element", "must not be nil")
		}
		return validateType(typed.element, path+".element")
	default:
		return invalid(path, "has an unsupported type")
	}
	return nil
}

func collectStepIDs(steps []Step, path string, ids map[string]struct{}) error {
	for index, step := range steps {
		stepPath := fmt.Sprintf("%s[%d]", path, index)
		if step == nil || isNilInterface(step) {
			return invalid(stepPath, "must not be nil")
		}
		if step.ID() == "" {
			return invalid(stepPath+".id", "must not be empty")
		}
		if _, exists := ids[step.ID()]; exists {
			return invalid(stepPath+".id", "is duplicated")
		}
		ids[step.ID()] = struct{}{}
		if block, ok := step.(Block); ok {
			if err := collectStepIDs(block.members, stepPath+".members", ids); err != nil {
				return err
			}
		}
	}
	return nil
}

func validateSteps(steps []Step, path string, ids map[string]struct{}, known map[string]struct{}) error {
	for index, step := range steps {
		stepPath := fmt.Sprintf("%s[%d]", path, index)
		for dependencyIndex, dependency := range step.Dependencies() {
			if _, exists := ids[dependency]; !exists {
				return invalid(fmt.Sprintf("%s.after[%d]", stepPath, dependencyIndex), "references an unknown step")
			}
		}
		switch typed := step.(type) {
		case Block:
			if err := validateSteps(typed.members, stepPath+".members", ids, known); err != nil {
				return err
			}
		case Exec:
			if err := validateInterpolated(typed.script, stepPath+".script", known); err != nil {
				return err
			}
			if err := validateExitMap(typed.exitMap, stepPath+".exitMap"); err != nil {
				return err
			}
			if err := validateExpr(typed.cwd, stepPath+".cwd", known); err != nil {
				return err
			}
			for envIndex, environment := range typed.env {
				if environment.key == "" {
					return invalid(fmt.Sprintf("%s.env[%d].key", stepPath, envIndex), "must not be empty")
				}
				if err := validateExpr(environment.value, fmt.Sprintf("%s.env[%d].value", stepPath, envIndex), known); err != nil {
					return err
				}
			}
			if err := validateExpr(typed.stdin, stepPath+".stdin", known); err != nil {
				return err
			}
		case Terminal:
			if err := validateOutcome(typed.outcome, stepPath+".outcome", known); err != nil {
				return err
			}
		default:
			return invalid(stepPath, "has an unsupported step")
		}
	}
	return nil
}

func validateOutcome(outcome Outcome, path string, known map[string]struct{}) error {
	switch typed := outcome.(type) {
	case Succeeded:
		if typed.value == nil {
			return invalid(path+".value", "must not be nil")
		}
		return validateExpr(typed.value, path+".value", known)
	case Degraded:
		if typed.reason == "" {
			return invalid(path+".reason", "must not be empty")
		}
		return validateExpr(typed.value, path+".value", known)
	case Failed:
		if typed.reason == "" {
			return invalid(path+".reason", "must not be empty")
		}
	case Skipped:
		if typed.reason == "" {
			return invalid(path+".reason", "must not be empty")
		}
	default:
		return invalid(path, "has an unsupported terminal outcome")
	}
	return nil
}

func validateDependencyGraph(steps []Step) error {
	byID := make(map[string]Step)
	indexSteps(steps, byID)
	state := make(map[string]uint8, len(byID))
	var visit func(string) error
	visit = func(id string) error {
		switch state[id] {
		case 1:
			return invalid("formula.steps", "contains a dependency cycle at "+id)
		case 2:
			return nil
		}
		state[id] = 1
		for _, dependency := range byID[id].Dependencies() {
			if err := visit(dependency); err != nil {
				return err
			}
		}
		state[id] = 2
		return nil
	}
	for id := range byID {
		if err := visit(id); err != nil {
			return err
		}
	}
	return nil
}

func indexSteps(steps []Step, byID map[string]Step) {
	for _, step := range steps {
		byID[step.ID()] = step
		if block, ok := step.(Block); ok {
			indexSteps(block.members, byID)
		}
	}
}

func validateExitMap(exitMap ExitMap, path string) error {
	if len(exitMap.pass) == 0 {
		return invalid(path+".pass", "must not be empty")
	}
	seen := map[int]struct{}{}
	for index, code := range append(cloneInts(exitMap.pass), exitMap.retryable...) {
		if code < 0 {
			return invalid(fmt.Sprintf("%s[%d]", path, index), "must not be negative")
		}
		if _, exists := seen[code]; exists {
			return invalid(path, "contains a duplicate exit code")
		}
		seen[code] = struct{}{}
	}
	return nil
}

func validateInterpolated(text InterpolatedText, path string, known map[string]struct{}) error {
	if len(text.parts) == 0 {
		return invalid(path+".parts", "must not be empty")
	}
	for index, part := range text.parts {
		switch typed := part.(type) {
		case Text:
		case Interpolation:
			if err := validateExpr(typed.expr, fmt.Sprintf("%s.parts[%d]", path, index), known); err != nil {
				return err
			}
		default:
			return invalid(fmt.Sprintf("%s.parts[%d]", path, index), "has an unsupported interpolation part")
		}
	}
	return nil
}

func validateExpr(expr Expr, path string, known map[string]struct{}) error {
	if expr == nil {
		return nil
	}
	switch typed := expr.(type) {
	case Literal:
		switch typed.value.(type) {
		case Null, String, Boolean, Number:
		default:
			return invalid(path, "has an unsupported literal")
		}
		if number, ok := typed.value.(Number); ok && (math.IsNaN(float64(number)) || math.IsInf(float64(number), 0)) {
			return invalid(path, "contains a non-finite number")
		}
	case Reference:
		if typed.name == "" {
			return invalid(path+".name", "must not be empty")
		}
		if _, exists := known[typed.name]; !exists {
			return invalid(path+".name", "is not proven by a required input")
		}
	case Record:
		seen := map[string]struct{}{}
		for index, entry := range typed.entries {
			entryPath := fmt.Sprintf("%s.entries[%d]", path, index)
			if entry.key == "" {
				return invalid(entryPath+".key", "must not be empty")
			}
			if _, exists := seen[entry.key]; exists {
				return invalid(entryPath+".key", "is duplicated")
			}
			if err := validateExpr(entry.value, entryPath+".value", known); err != nil {
				return err
			}
			seen[entry.key] = struct{}{}
		}
	case Array:
		for index, element := range typed.elements {
			if err := validateExpr(element, fmt.Sprintf("%s.elements[%d]", path, index), known); err != nil {
				return err
			}
		}
	case InterpolatedText:
		return validateInterpolated(typed, path, known)
	default:
		return invalid(path, "has an unsupported expression")
	}
	return nil
}

func cloneStrings(values []string) []string { return append([]string(nil), values...) }
func cloneInts(values []int) []int          { return append([]int(nil), values...) }
func cloneFields(fields []Field) []Field {
	copied := make([]Field, len(fields))
	for i := range fields {
		copied[i] = fields[i].clone()
	}
	return copied
}

func cloneSteps(steps []Step) []Step {
	copied := make([]Step, len(steps))
	for i := range steps {
		copied[i] = cloneStep(steps[i])
	}
	return copied
}

func cloneStep(step Step) Step {
	switch typed := step.(type) {
	case Block:
		return NewBlock(typed.id, typed.after, typed.members)
	case Exec:
		return NewExec(typed.id, typed.after, typed.script, typed.cwd, typed.env, typed.stdin, typed.exitMap)
	case Terminal:
		return NewTerminal(typed.id, typed.name, typed.after, typed.outcome)
	default:
		return step
	}
}

func cloneType(typ Type) Type {
	switch typed := typ.(type) {
	case AtomicType:
		return typed
	case RecordType:
		return NewRecordType(typed.fields)
	case ArrayType:
		return NewArrayType(typed.element)
	default:
		return typ
	}
}

func cloneExprs(expressions []Expr) []Expr {
	copied := make([]Expr, len(expressions))
	for i := range expressions {
		copied[i] = cloneExpr(expressions[i])
	}
	return copied
}

func cloneExpr(expr Expr) Expr {
	switch typed := expr.(type) {
	case Literal:
		return typed
	case Reference:
		return typed
	case Record:
		return NewRecord(typed.entries)
	case Array:
		return NewArray(typed.elements)
	case InterpolatedText:
		return typed.clone()
	default:
		return expr
	}
}

func cloneEntries(entries []RecordEntry) []RecordEntry {
	copied := make([]RecordEntry, len(entries))
	for i := range entries {
		copied[i] = NewRecordEntry(entries[i].key, entries[i].value)
	}
	return copied
}

func cloneParts(parts []TextPart) []TextPart {
	copied := make([]TextPart, len(parts))
	for i := range parts {
		switch typed := parts[i].(type) {
		case Text:
			copied[i] = typed
		case Interpolation:
			copied[i] = NewInterpolation(typed.expr)
		default:
			copied[i] = parts[i]
		}
	}
	return copied
}

func cloneEnvironment(values []Environment) []Environment {
	copied := make([]Environment, len(values))
	for i := range values {
		copied[i] = NewEnvironment(values[i].key, values[i].value)
	}
	return copied
}

func cloneOutcome(outcome Outcome) Outcome {
	switch typed := outcome.(type) {
	case Succeeded:
		return NewSucceeded(typed.value)
	case Degraded:
		return NewDegraded(typed.value, typed.reason)
	case Failed:
		return NewFailed(typed.reason)
	case Skipped:
		return NewSkipped(typed.reason)
	default:
		return outcome
	}
}

func isNilInterface(value any) bool {
	reflected := reflect.ValueOf(value)
	return reflected.Kind() == reflect.Ptr && reflected.IsNil()
}
