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
		"printf '%s\\n' \"$GC_DISABLE_USAGE_METRICS\" \"$BD_DISABLE_METRICS\" \"$OTEL_SERVICE_NAME\" > \"$GC_TEST_PRODUCT_METRICS_CHILD_ENV_SPY.tmp\" && mv \"$GC_TEST_PRODUCT_METRICS_CHILD_ENV_SPY.tmp\" \"$GC_TEST_PRODUCT_METRICS_CHILD_ENV_SPY\"\n"
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

// waitForCompleteSnapshotRead is overridden in tests to simulate a writer
// still mid-write without depending on real filesystem timing.
var waitForCompleteSnapshotRead = os.ReadFile

// waitForCompleteSnapshot requires the full line count, not just existence,
// because a writer may create the file before it finishes writing (e.g.
// shell redirect truncation) -- a short read is not done.
func waitForCompleteSnapshot(path string, wantLines int, deadline time.Time) ([]byte, error) {
	for {
		data, err := waitForCompleteSnapshotRead(path)
		if err == nil {
			lines := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
			if len(lines) == wantLines {
				return data, nil
			}
		} else if !os.IsNotExist(err) {
			return nil, err
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("child environment snapshot did not contain %d complete lines within deadline", wantLines)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestWaitForCompleteSnapshotIgnoresPartialRead reproduces the truncation
// race from the bug report by injecting reads instead of racing real
// goroutines against filesystem timing.
func TestWaitForCompleteSnapshotIgnoresPartialRead(t *testing.T) {
	partial := []byte("only-one-line\n")
	full := []byte("1\nkeep-beads-setting\nkeep-otel-setting\n")

	var calls int
	previous := waitForCompleteSnapshotRead
	waitForCompleteSnapshotRead = func(string) ([]byte, error) {
		calls++
		if calls < 3 {
			return partial, nil
		}
		return full, nil
	}
	t.Cleanup(func() { waitForCompleteSnapshotRead = previous })

	deadline := time.Now().Add(testutil.ExecRaceTimeout)
	data, err := waitForCompleteSnapshot("child.env", 3, deadline)
	if err != nil {
		t.Fatalf("waitForCompleteSnapshot: %v", err)
	}
	if calls != 3 {
		t.Fatalf("waitForCompleteSnapshot read %d time(s), want 3 (must retry past partial reads)", calls)
	}

	got := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
	want := []string{"1", "keep-beads-setting", "keep-otel-setting"}
	if !slices.Equal(got, want) {
		t.Fatalf("waitForCompleteSnapshot returned %#v, want %#v (must not accept a partial read)", got, want)
	}
}
