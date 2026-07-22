package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

// TestCmdSessionShowJSONShape pins the `gc session show --json` wire shape
// the orphan-sweep.sh liveness probe consumes: issue_type/status plus the
// metadata identity fields, resolved through the routed session store.
func TestCmdSessionShowJSONShape(t *testing.T) {
	t.Setenv("GC_BEADS", "file")
	t.Setenv("GC_SESSION", "fake")
	t.Setenv("GC_DIR", t.TempDir())

	cityDir := t.TempDir()
	writePhase0InterfaceCity(t, cityDir, `[workspace]
name = "test-city"

[beads]
provider = "file"

[[agent]]
name = "worker"
start_command = "true"
max_active_sessions = 1

[[named_session]]
template = "worker"
mode = "on_demand"
`)
	t.Setenv("GC_CITY", cityDir)

	// Materialize the named session so a session bead exists.
	var pinOut, pinErr bytes.Buffer
	if code := cmdSessionPin([]string{"worker"}, &pinOut, &pinErr); code != 0 {
		t.Fatalf("cmdSessionPin = %d; stderr=%s", code, pinErr.String())
	}

	var stdout, stderr bytes.Buffer
	if code := cmdSessionShow([]string{"worker"}, true, &stdout, &stderr); code != 0 {
		t.Fatalf("cmdSessionShow = %d; stderr=%s", code, stderr.String())
	}
	var got sessionShowJSON
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("decoding show output %q: %v", stdout.String(), err)
	}
	if got.ID == "" || got.IssueType != "session" || got.Status != "open" {
		t.Fatalf("show shape: %+v", got)
	}
	if got.Metadata.Template != "worker" {
		t.Fatalf("show metadata: %+v", got.Metadata)
	}

	// A missing session errors (the probe's failure path).
	var missOut, missErr bytes.Buffer
	if code := cmdSessionShow([]string{"gc-does-not-exist"}, true, &missOut, &missErr); code == 0 {
		t.Fatal("show of a missing session must fail")
	}
	if !strings.Contains(missErr.String(), "gc session show:") {
		t.Fatalf("missing-session error not surfaced: %q", missErr.String())
	}
}
