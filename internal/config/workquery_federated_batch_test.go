package config

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func runFederatedAssignedReadyBatch(t *testing.T, env map[string]string) (string, string) {
	t.Helper()
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq not available; the work-query shell requires it")
	}
	tmp := t.TempDir()
	logPath := filepath.Join(tmp, "gc.log")
	gcPath := filepath.Join(tmp, "gc")
	gcScript := `#!/bin/sh
printf '%s\n' "$*" >> "$GC_LOG"
printf '%s' '[{"id":"alias-first","status":"open","assignee":"worker-alias","priority":0},{"id":"name-second","status":"open","assignee":"worker-name","priority":1},{"id":"session-third","status":"open","assignee":"sess-1","priority":2}]'
`
	if err := os.WriteFile(gcPath, []byte(gcScript), 0o755); err != nil {
		t.Fatal(err)
	}
	script := standardAssignedReadyWorkQueryScript(QueryTopology{FederatedReady: true}) + `printf "[]"`
	cmd := exec.Command("sh", "-c", script)
	cmd.Env = []string{"PATH=" + tmp + ":" + os.Getenv("PATH"), "GC_LOG=" + logPath}
	for k, v := range env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("run batched query: %v: %s", err, out)
	}
	log, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	return string(out), string(log)
}

func TestFederatedAssignedReadyBatchesAndPreservesIdentityPrecedence(t *testing.T) {
	out, log := runFederatedAssignedReadyBatch(t, map[string]string{
		"GC_SESSION_ID": "sess-1", "GC_SESSION_NAME": "worker-name", "GC_ALIAS": "worker-alias",
	})
	if lines := strings.FieldsFunc(strings.TrimSpace(log), func(r rune) bool { return r == '\n' }); len(lines) != 1 {
		t.Fatalf("gc ready calls = %d, want 1; log=%q", len(lines), log)
	}
	for _, want := range []string{"--assignee-any=sess-1", "--assignee-any=worker-name", "--assignee-any=worker-alias"} {
		if !strings.Contains(log, want) {
			t.Errorf("batched gc ready args missing %q: %q", want, log)
		}
	}
	var rows []map[string]any
	if err := json.Unmarshal([]byte(out), &rows); err != nil {
		t.Fatalf("output is not JSON: %v: %q", err, out)
	}
	if len(rows) != 1 || rows[0]["id"] != "session-third" {
		t.Fatalf("selected rows = %v, want session identity before globally higher-priority alias row", rows)
	}
}

func TestFederatedAssignedReadyOmitsEmptyOptionalIdentities(t *testing.T) {
	_, log := runFederatedAssignedReadyBatch(t, map[string]string{"GC_ALIAS": "worker-alias"})
	if strings.Count(log, "--assignee-any=") != 1 || !strings.Contains(log, "--assignee-any=worker-alias") {
		t.Fatalf("optional identity args = %q, want only the non-empty alias", log)
	}
}
