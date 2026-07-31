//go:build integration

package integration

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/gastownhall/gascity/test/tmuxtest"
)

// TestSetupGasTownCityCleanupKillsTmuxSocketEvenWhenGCStopCannotResolveCity
// covers ga-026hrg: setupGasTownCity's t.Cleanup relies entirely on `gc
// stop`/`gc supervisor stop --wait` to tear down the city's tmux server.
// Both resolve the city by re-reading state from cityDir
// (resolveStopCityPath -> stopCityPathFromArg -> validateCityPath in
// cmd/gc/cmd_stop.go); when cityDir is unresolvable by the time cleanup
// runs, `gc stop` fails at path resolution and returns 1 before ever
// reaching teardownServerForStop's tmux TeardownServer() call. The tmux
// server started for the test city is then never torn down -- exactly the
// class of leak ga-026hrg reports in the field (647 orphaned tmux servers,
// only 3 sockets and 20 sessions reachable).
//
// This test simulates that unresolvable-city condition by removing cityDir
// out from under `gc stop` (registered as a t.Cleanup on the inner subtest
// AFTER setupGasTownCityNoGuard's own cleanup, so LIFO ordering runs the
// sabotage first) and asserts the tmux socket does not outlive the subtest.
// It is expected to fail until setupGasTownCity's cleanup gains a
// guaranteed `tmux -L <socket> kill-server` fallback that does not depend
// on `gc stop` resolving city state.
func TestSetupGasTownCityCleanupKillsTmuxSocketEvenWhenGCStopCannotResolveCity(t *testing.T) {
	if usingSubprocess() {
		t.Skip("tmux-specific orphan-cleanup fallback; not applicable under GC_SESSION=subprocess")
	}
	tmuxtest.RequireTmux(t)

	var socketName string
	ok := t.Run("inner", func(t *testing.T) {
		cityDir := setupGasTownCityNoGuard(t, []gasTownAgent{{Name: "mayor", StartCommand: "sleep 3600"}})
		socketName = filepath.Base(cityDir)

		if !tmuxSocketServerReachable(socketName) {
			t.Fatalf("precondition failed: tmux socket %q not reachable right after city setup", socketName)
		}

		// Registered after setupGasTownCityNoGuard, so t.Cleanup's LIFO
		// order runs this sabotage BEFORE setupGasTownCity's own "gc
		// stop"/"gc supervisor stop --wait" cleanup -- simulating the field
		// condition where cleanup-time city state is unresolvable.
		t.Cleanup(func() {
			if err := os.RemoveAll(cityDir); err != nil {
				t.Logf("sabotage: removing city dir before gc stop runs: %v", err)
			}
		})
	})
	if !ok {
		t.Fatalf("inner subtest failed unexpectedly; cannot verify the tmux cleanup fallback")
	}

	if socketName != "" {
		t.Cleanup(func() {
			// Safety net: this test intentionally defeats the normal `gc
			// stop` teardown path, so if the production fallback under test
			// is still missing (or a future regression reintroduces the
			// gap), forcibly reclaim the socket rather than leaking a real
			// tmux server process -- the exact failure mode ga-026hrg
			// exists to prevent.
			_ = exec.Command("tmux", "-L", socketName, "kill-server").Run()
		})
	}

	if tmuxSocketServerReachable(socketName) {
		t.Errorf("tmux socket %q still reachable after cleanup ran with an unresolvable city dir; setupGasTownCity's cleanup needs a guaranteed `tmux -L <socket> kill-server` fallback that does not depend on `gc stop` resolving city state (ga-026hrg)", socketName)
	}
}

// tmuxSocketServerReachable reports whether a tmux server is still
// listening on the named socket, mirroring the exec.Command("tmux", "-L",
// ..., "list-sessions", ...) idiom already used by
// waitForExpectedTmuxSessions in helpers_test.go: a zero exit from
// `list-sessions` means the server is up.
func tmuxSocketServerReachable(socketName string) bool {
	return exec.Command("tmux", "-L", socketName, "list-sessions").Run() == nil
}
