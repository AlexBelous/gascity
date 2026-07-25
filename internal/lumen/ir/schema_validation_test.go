package ir

import (
	"crypto/sha256"
	"fmt"
	"os"
	"strings"
	"testing"
)

const pinnedSchemaSHA256 = "044073d3b67025306b5d1eff2240429ca05a7c21eca9c2891289cf1dba6e41da"

func TestPinnedSchemaMatchesLatestUpstream(t *testing.T) {
	data, err := os.ReadFile("lumen-ir-0.2.5.schema.json")
	if err != nil {
		t.Fatalf("read pinned schema: %v", err)
	}
	if got := fmt.Sprintf("%x", sha256.Sum256(data)); got != pinnedSchemaSHA256 {
		t.Fatalf("schema SHA-256 = %s, want latest upstream %s", got, pinnedSchemaSHA256)
	}
}

func TestDecodeRejectsSchemaViolation(t *testing.T) {
	doc := validIRDocument(`{
	  "kind": "settle",
	  "id": "done",
	  "after": [],
	  "outcome": "succeeded"
	}`)

	_, err := Decode([]byte(doc))
	if err == nil {
		t.Fatal("Decode accepted a node without required origin")
	}
}

func TestDecodeRejectsDanglingAfterReference(t *testing.T) {
	doc := validIRDocument(`{
	  "kind": "settle",
	  "id": "done",
	  "after": ["missing"],
	  "origin": {"uri": "test", "line": 1, "col": 0},
	  "outcome": "succeeded"
	}`)

	_, err := Decode([]byte(doc))
	if err == nil || !strings.Contains(err.Error(), `unknown id "missing"`) {
		t.Fatalf("Decode dangling after error = %v, want unknown id", err)
	}
}

func TestDecodeRejectsDuplicateTopLevelNodeIDs(t *testing.T) {
	doc := validIRDocument(`{
	  "kind": "settle",
	  "id": "same",
	  "after": [],
	  "origin": {"uri": "test", "line": 1, "col": 0},
	  "outcome": "succeeded"
	}, {
	  "kind": "settle",
	  "id": "same",
	  "after": [],
	  "origin": {"uri": "test", "line": 2, "col": 0},
	  "outcome": "failed"
	}`)

	_, err := Decode([]byte(doc))
	if err == nil || !strings.Contains(err.Error(), `duplicate top-level node id "same"`) {
		t.Fatalf("Decode duplicate-id error = %v, want duplicate top-level node id", err)
	}
}

func TestDecodeRejectsUnknownNestedNodeKindBySignature(t *testing.T) {
	doc := validIRDocument(`{
	  "kind": "block",
	  "id": "group",
	  "after": [],
	  "origin": {"uri": "test", "line": 1, "col": 0},
	  "members": [{
	    "kind": "future-kind",
	    "after": [],
	    "origin": {"uri": "test", "line": 2, "col": 0}
	  }]
	}`)

	_, err := Decode([]byte(doc))
	if err == nil || !strings.Contains(err.Error(), `unknown node kind "future-kind"`) {
		t.Fatalf("Decode nested-kind error = %v, want unknown node kind", err)
	}
}

func TestDecodeRejectsUnknownSettleOutcome(t *testing.T) {
	doc := validIRDocument(`{
	  "kind": "settle",
	  "id": "done",
	  "after": [],
	  "origin": {"uri": "test", "line": 1, "col": 0},
	  "outcome": "victory"
	}`)

	_, err := Decode([]byte(doc))
	if err == nil || !strings.Contains(err.Error(), `unknown settle outcome "victory"`) {
		t.Fatalf("Decode settle-outcome error = %v, want unknown settle outcome", err)
	}
}

func TestDecodeRejectsMissingRequiredNodePayload(t *testing.T) {
	doc := validIRDocument(`{
	  "kind": "exec",
	  "id": "work",
	  "after": [],
	  "origin": {"uri": "test", "line": 1, "col": 0},
	  "interpreter": {"kind": "shell", "program": {"kind": "bash"}},
	  "exitMap": {"pass": [0]}
	}`)

	_, err := Decode([]byte(doc))
	if err == nil || !strings.Contains(err.Error(), `missing required field "body"`) {
		t.Fatalf("Decode required-payload error = %v, want missing body", err)
	}
}

func TestDecodeRejectsUnknownTypeKindAndCapability(t *testing.T) {
	tests := []struct {
		name string
		typ  string
	}{
		{
			name: "kind",
			typ:  `{"kind":"future-type"}`,
		},
		{
			name: "capability",
			typ:  `{"kind":"channel","payload":{"kind":"atomic","name":"string"},"stream":false,"capability":"future-capability"}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			doc := fmt.Sprintf(`{
			  "contract": {"name": "lumen.ir", "version": "0.2.5", "producer": "test"},
			  "name": "main",
			  "input": {
			    "name": "main.input",
			    "fields": [{
			      "name": "value",
			      "type": %s,
			      "required": true,
			      "body": false,
			      "origin": {"uri": "test", "line": 0, "col": 0}
			    }],
			    "origin": {"uri": "test", "line": 0, "col": 0}
			  },
			  "nodes": [],
			  "origin": {"uri": "test", "line": 0, "col": 0}
			}`, tt.typ)

			if _, err := Decode([]byte(doc)); err == nil {
				t.Fatalf("Decode accepted unknown type %s", tt.name)
			}
		})
	}
}

func validIRDocument(nodes string) string {
	return fmt.Sprintf(`{
	  "contract": {"name": "lumen.ir", "version": "0.2.5", "producer": "test"},
	  "name": "main",
	  "input": {
	    "name": "main.input",
	    "fields": [],
	    "origin": {"uri": "test", "line": 0, "col": 0}
	  },
	  "nodes": [%s],
	  "origin": {"uri": "test", "line": 0, "col": 0}
	}`, nodes)
}
