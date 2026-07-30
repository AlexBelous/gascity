package session

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/execenv"
	"github.com/gastownhall/gascity/internal/testutil"
)

func TestProductMetricsDirectChildEnvSessionSubmitPoller(t *testing.T) {
	dir := t.TempDir()
	snapshot := filepath.Join(dir, "child.env")
	spy := filepath.Join(dir, "gc-child-spy")
	script := "#!/bin/sh\n" +
		"printf '%s\\n' \"$GC_DISABLE_USAGE_METRICS\" \"$BD_DISABLE_METRICS\" \"$OTEL_SERVICE_NAME\" > \"$GC_TEST_PRODUCT_METRICS_CHILD_ENV_SPY\"\n"
	if err := os.WriteFile(spy, []byte(script), 0o700); err != nil {
		t.Fatalf("write child spy: %v", err)
	}
	t.Setenv("GC_TEST_PRODUCT_METRICS_CHILD_ENV_SPY", snapshot)
	t.Setenv(execenv.UsageMetricsDisableEnv, "0")
	t.Setenv("BD_DISABLE_METRICS", "keep-beads-setting")
	t.Setenv("OTEL_SERVICE_NAME", "keep-otel-setting")

	previous := sessionSubmitPollerExecutable
	sessionSubmitPollerExecutable = func() (string, error) { return spy, nil }
	t.Cleanup(func() { sessionSubmitPollerExecutable = previous })

	if err := ensureSessionSubmitPoller(dir, "worker", "session-worker"); err != nil {
		t.Fatalf("ensureSessionSubmitPoller: %v", err)
	}
	deadline := time.Now().Add(testutil.ExecRaceTimeout)
	data, err := waitForCompleteSnapshot(snapshot, 3, deadline)
	if err != nil {
		t.Fatalf("%v", err)
	}

	got := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
	want := []string{execenv.UsageMetricsDisableValue, "keep-beads-setting", "keep-otel-setting"}
	if !slices.Equal(got, want) {
		t.Fatalf("session submit poller environment = %#v, want %#v", got, want)
	}
}

// waitForCompleteSnapshot polls path until it can read wantLines complete
// lines or deadline passes. A snapshot writer may create the file before
// its content is fully written (e.g. shell redirect truncation); a read
// landing in that window must not be mistaken for a finished write.
func waitForCompleteSnapshot(path string, _ int, deadline time.Time) ([]byte, error) {
	for {
		data, err := os.ReadFile(path)
		if err == nil {
			return data, nil
		}
		if !os.IsNotExist(err) {
			return nil, err
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("child environment snapshot was not written within deadline")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestWaitForCompleteSnapshotIgnoresPartialRead reproduces the race from
// the bug report deterministically: a reader can observe a snapshot file
// after it has been created but before its writer finished populating it.
// waitForCompleteSnapshot must keep polling past a short/partial read
// instead of returning it as if the write were complete.
func TestWaitForCompleteSnapshotIgnoresPartialRead(t *testing.T) {
	dir := t.TempDir()
	snapshot := filepath.Join(dir, "child.env")

	// Simulate the exact race window from the bug report: the file exists
	// with partial content before the writer finishes populating it.
	if err := os.WriteFile(snapshot, []byte("only-one-line\n"), 0o600); err != nil {
		t.Fatalf("write partial snapshot: %v", err)
	}

	full := []byte("1\nkeep-beads-setting\nkeep-otel-setting\n")
	errCh := make(chan error, 1)
	go func() {
		time.Sleep(30 * time.Millisecond)
		tmp := snapshot + ".tmp"
		if err := os.WriteFile(tmp, full, 0o600); err != nil {
			errCh <- err
			return
		}
		errCh <- os.Rename(tmp, snapshot)
	}()

	deadline := time.Now().Add(testutil.ExecRaceTimeout)
	data, err := waitForCompleteSnapshot(snapshot, 3, deadline)
	if err != nil {
		t.Fatalf("waitForCompleteSnapshot: %v", err)
	}
	if err := <-errCh; err != nil {
		t.Fatalf("complete snapshot in background: %v", err)
	}

	got := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
	want := []string{"1", "keep-beads-setting", "keep-otel-setting"}
	if !slices.Equal(got, want) {
		t.Fatalf("waitForCompleteSnapshot returned %#v, want %#v (must not accept a partial read)", got, want)
	}
}
