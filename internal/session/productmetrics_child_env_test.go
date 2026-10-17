package session

import (
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/execenv"
)

func TestProductMetricsDirectChildEnvSessionSubmitPoller(t *testing.T) {
	dir := t.TempDir()
	snapshot := filepath.Join(dir, "child.env")
	spy := filepath.Join(dir, "gc-child-spy")
	script := "#!/bin/sh\n" +
		"snapshot=\"$GC_TEST_PRODUCT_METRICS_CHILD_ENV_SPY\"\n" +
		"tmp=\"${snapshot}.tmp.$$\"\n" +
		"printf '%s\\n' \"$GC_DISABLE_USAGE_METRICS\" \"$BD_DISABLE_METRICS\" \"$OTEL_SERVICE_NAME\" \"$GC_CITY_PATH\" \"$GC_CITY\" \"$FOREIGN_API_KEY\" > \"$tmp\"\n" +
		"for key in GC_SESSION_ID GC_SESSION_NAME GC_ALIAS GC_AGENT GC_TEMPLATE GC_SESSION_ORIGIN GC_RUNTIME_EPOCH GC_CONTINUATION_EPOCH GC_INSTANCE_TOKEN BEADS_HOLDER_TOKEN BEADS_ACTOR CLAUDE_CODE_SESSION_ID; do\n" +
		"  eval \"present=\\${$key+x}\"; printf '%s=%s\\n' \"$key\" \"$present\" >> \"$tmp\"\n" +
		"done\n" +
		"printf 'arg=%s\\n' \"$@\" >> \"$tmp\"\n" +
		"mv -f \"$tmp\" \"$snapshot\"\n"
	if err := os.WriteFile(spy, []byte(script), 0o700); err != nil {
		t.Fatalf("write child spy: %v", err)
	}
	t.Setenv("GC_TEST_PRODUCT_METRICS_CHILD_ENV_SPY", snapshot)
	t.Setenv(execenv.UsageMetricsDisableEnv, "0")
	t.Setenv("BD_DISABLE_METRICS", "keep-beads-setting")
	t.Setenv("OTEL_SERVICE_NAME", "keep-otel-setting")
	t.Setenv("GC_CITY_PATH", dir)
	t.Setenv("GC_CITY", dir)
	t.Setenv("FOREIGN_API_KEY", "fixture-ordinary-credential")
	keys := []string{"GC_SESSION_ID", "GC_SESSION_NAME", "GC_ALIAS", "GC_AGENT", "GC_TEMPLATE", "GC_SESSION_ORIGIN", "GC_RUNTIME_EPOCH", "GC_CONTINUATION_EPOCH", "GC_INSTANCE_TOKEN", "BEADS_HOLDER_TOKEN", "BEADS_ACTOR", "CLAUDE_CODE_SESSION_ID"}
	for _, key := range keys {
		t.Setenv(key, "fixture-worker-identity")
	}

	previous := sessionSubmitPollerExecutable
	sessionSubmitPollerExecutable = func() (string, error) { return spy, nil }
	t.Cleanup(func() { sessionSubmitPollerExecutable = previous })

	if err := ensureSessionSubmitPoller(dir, "worker", "session-worker"); err != nil {
		t.Fatalf("ensureSessionSubmitPoller: %v", err)
	}
	deadline := time.Now().Add(execHangBudget)
	var data []byte
	for {
		var err error
		data, err = os.ReadFile(snapshot)
		if err == nil {
			break
		}
		if !os.IsNotExist(err) {
			t.Fatalf("read child environment snapshot: %v", err)
		}
		if time.Now().After(deadline) {
			t.Fatalf("child environment snapshot was not written within %s", execHangBudget)
		}
		time.Sleep(10 * time.Millisecond)
	}
	// The launcher releases its handle, so reap this test's finite spy after
	// its atomic output becomes visible; no detached test poller survives.
	pidRaw, err := os.ReadFile(sessionSubmitPollerPIDPath(dir, "session-worker", "worker"))
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(pidRaw)))
	if err != nil {
		t.Fatal(err)
	}
	child, err := os.FindProcess(pid)
	if err != nil {
		t.Fatal(err)
	}
	if state, err := child.Wait(); err != nil || !state.Success() {
		t.Fatalf("reap owned finite spy: %v, %v", state, err)
	}

	got := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
	want := []string{execenv.UsageMetricsDisableValue, "keep-beads-setting", "keep-otel-setting", dir, dir, "fixture-ordinary-credential"}
	for _, key := range keys {
		want = append(want, key+"=")
	}
	for _, arg := range []string{"nudge", "poll", "--city", dir, "--session", "session-worker", "worker"} {
		want = append(want, "arg="+arg)
	}
	if !slices.Equal(got, want) {
		t.Fatalf("session submit poller environment = %#v, want %#v", got, want)
	}
}
