package ir

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"
	"sort"
	"sync"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

const embeddedSchemaURL = "lumen-ir-0.2.5.schema.json"

//go:embed lumen-ir-0.2.5.schema.json
var embeddedSchema []byte

type compiledSchemas struct {
	document *jsonschema.Schema
	runNode  *jsonschema.Schema
}

var (
	schemasOnce sync.Once
	schemas     compiledSchemas
	schemasErr  error
)

func loadSchemas() (compiledSchemas, error) {
	schemasOnce.Do(func() {
		doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(embeddedSchema))
		if err != nil {
			schemasErr = fmt.Errorf("parse embedded schema: %w", err)
			return
		}
		compiler := jsonschema.NewCompiler()
		compiler.DefaultDraft(jsonschema.Draft7)
		if err := compiler.AddResource(embeddedSchemaURL, doc); err != nil {
			schemasErr = fmt.Errorf("register embedded schema: %w", err)
			return
		}
		schemas.document, err = compiler.Compile(embeddedSchemaURL)
		if err != nil {
			schemasErr = fmt.Errorf("compile embedded schema: %w", err)
			return
		}
		schemas.runNode, err = compiler.Compile(embeddedSchemaURL + "#/definitions/runNode")
		if err != nil {
			schemasErr = fmt.Errorf("compile embedded runNode schema: %w", err)
		}
	})
	return schemas, schemasErr
}

// validateDocumentSchema validates the main document and every Gas City
// sub-formula bundle entry before Decode populates the typed IR.
func validateDocumentSchema(data []byte) error {
	value, err := jsonschema.UnmarshalJSON(bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("decoding lumen.ir JSON: %w", err)
	}
	compiled, err := loadSchemas()
	if err != nil {
		return fmt.Errorf("lumen.ir: %w", err)
	}
	return validateSchemaTree(compiled.document, value, "document")
}

func validateSchemaTree(schema *jsonschema.Schema, value any, label string) error {
	if err := schema.Validate(value); err != nil {
		return fmt.Errorf("lumen.ir: %s violates the pinned 0.2.5 schema: %w", label, err)
	}
	object, ok := value.(map[string]any)
	if !ok {
		return nil // The root schema already requires an object.
	}
	rawFormulas, exists := object["formulas"]
	if !exists {
		return nil
	}
	formulas, ok := rawFormulas.(map[string]any)
	if !ok {
		return fmt.Errorf("lumen.ir: %s.formulas must be an object", label)
	}
	names := make([]string, 0, len(formulas))
	for name := range formulas {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		childLabel := fmt.Sprintf(`%s.formulas[%q]`, label, name)
		if err := validateSchemaTree(schema, formulas[name], childLabel); err != nil {
			return err
		}
	}
	return nil
}

func validateRunNodeSchema(node map[string]json.RawMessage) error {
	data, err := json.Marshal(node)
	if err != nil {
		return fmt.Errorf("encode run node: %w", err)
	}
	value, err := jsonschema.UnmarshalJSON(bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("decode run node: %w", err)
	}
	compiled, err := loadSchemas()
	if err != nil {
		return err
	}
	return compiled.runNode.Validate(value)
}
