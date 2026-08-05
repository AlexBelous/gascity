package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestRunExternalOutputRetriesOnWorkspaceGateBusy proves the cmd/gc test
// helper rides out bd's workspace-gate contention (workspacegate.ErrBusy,
// literal string "workspace gate busy") instead of hard-failing on the
// first transient collision with a maintenance operation (dolt
// migrate/restore holding the gate EXCLUSIVE while a routine bd call
// attempts a SHARED acquire).
//
// The fake "bd" script fails twice with bd's gate-busy wrapped message,
// then succeeds on the third invocation -- mirroring
// TestRunWithGateBusyRetrySucceedsAfterTransientBusy in internal/beads, but
// exercised through the real subprocess path (cmd.CombinedOutput()) used by
// runExternal/runExternalOutput, where the busy-marker substring lives in
// the captured output bytes rather than in err.Error().
func TestRunExternalOutputRetriesOnWorkspaceGateBusy(t *testing.T) {
	dir := t.TempDir()
	scriptPath := filepath.Join(dir, "bd")
	counterPath := filepath.Join(dir, "gatebusy.count")
	script := `#!/bin/sh
count=0
if [ -f gatebusy.count ]; then count=$(cat gatebusy.count); fi
count=$((count + 1))
echo "$count" > gatebusy.count
if [ "$count" -lt 3 ]; then
  echo 'a maintenance operation is running on this workspace; retry when it completes: workspace gate busy' >&2
  exit 1
fi
echo ok
`
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake bd script: %v", err)
	}

	out := runExternalOutput(t, dir, scriptPath, "show", "ga-1")

	if !strings.Contains(string(out), "ok") {
		t.Fatalf("runExternalOutput result = %q, want the eventual success output", out)
	}
	gotCount, err := os.ReadFile(counterPath)
	if err != nil {
		t.Fatalf("read fake bd invocation counter: %v", err)
	}
	if strings.TrimSpace(string(gotCount)) != "3" {
		t.Fatalf("fake bd invocation count = %s, want 3 (2 gate-busy failures + 1 success)", gotCount)
	}
}
