package engine_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/gastownhall/gascity/internal/lumen/engine"
)

func inputDoc(fieldJSON string) string {
	return fmt.Sprintf(`{
      "contract": {"name": "lumen.ir", "version": "0.2.5", "producer": "test"},
      "name": "input-conformance",
      "input": {
        "name": "main.input",
        "fields": [%s],
        "origin": {"uri": "t", "line": 0, "col": 0}
      },
      "origin": {"uri": "t", "line": 0, "col": 0},
      "nodes": [%s]
    }`, fieldJSON, execNode("effect", `echo should-not-run`, nil))
}

func TestRunRejectsWrongTypedInputBeforeJournalStart(t *testing.T) {
	store := newStore(t)
	doc := decodeIR(t, inputDoc(`{
      "name": "items",
      "type": {"kind": "array", "element": {"kind": "atomic", "name": "string"}},
      "required": true,
      "body": false,
      "origin": {"uri": "t", "line": 0, "col": 0}
    }`))

	_, err := engine.Run(context.Background(), store, doc, map[string]any{
		"items": []any{"valid", float64(42)},
	})
	if !errors.Is(err, engine.ErrInvalidInput) {
		t.Fatalf("Run error = %v, want ErrInvalidInput", err)
	}

	var events int
	if err := store.ReadDB().QueryRowContext(
		context.Background(),
		`SELECT COUNT(*) FROM journal`,
	).Scan(&events); err != nil {
		t.Fatalf("count journal: %v", err)
	}
	if events != 0 {
		t.Fatalf("journal event count = %d, want 0", events)
	}
}

func TestRunDistinguishesExplicitNullDefaultFromMissingDefault(t *testing.T) {
	doc := decodeIR(t, inputDoc(`{
      "name": "note",
      "type": {
        "kind": "union",
        "of": [
          {"kind": "atomic", "name": "string"},
          {"kind": "atomic", "name": "null"}
        ]
      },
      "required": true,
      "default": null,
      "body": false,
      "origin": {"uri": "t", "line": 0, "col": 0}
    }`))

	if _, err := engine.Run(context.Background(), newStore(t), doc, nil); err != nil {
		t.Fatalf("Run with explicit null default: %v", err)
	}
}

func TestRunValidatesRecordAdditionalFieldsRecursively(t *testing.T) {
	doc := decodeIR(t, inputDoc(`{
      "name": "config",
      "type": {
        "kind": "record",
        "fields": [{
          "name": "enabled",
          "type": {"kind": "atomic", "name": "bool"},
          "required": true,
          "body": false,
          "origin": {"uri": "t", "line": 0, "col": 0}
        }],
        "additionalFields": {"kind": "atomic", "name": "number"}
      },
      "required": true,
      "body": false,
      "origin": {"uri": "t", "line": 0, "col": 0}
    }`))

	_, err := engine.Run(context.Background(), newStore(t), doc, map[string]any{
		"config": map[string]any{
			"enabled": true,
			"retries": "three",
		},
	})
	if !errors.Is(err, engine.ErrInvalidInput) {
		t.Fatalf("Run error = %v, want ErrInvalidInput", err)
	}
}
