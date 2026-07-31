//go:build linux

package tmuxorphan

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

// spawnFakeTmuxServer starts a real, long-lived process whose argv contains
// a literal "-L <socketName>" pair (so pidutil.Cmdline, which always reads
// the real /proc/<pid>/cmdline, finds it) and whose environment sets
// TMUX_TMPDIR to tmuxTmpDir, or omits it entirely when tmuxTmpDir == "". The
// comm value seen by listServersUnder is controlled separately via the fake
// root fixture in each test, since isTmuxServerComm respects the injected
// root but pidutil.Cmdline/Environ always read the real /proc.
//
// The process is "sh -c 'kill -STOP $$' -L <socketName>": sh's own argv
// (what /proc/<pid>/cmdline reports) is the full exec.Cmd argument list,
// including the trailing "-L socketName" pair passed through as unused
// positional params to the -c script. The script stops itself in place with
// a signal -- no fork, no exec -- so the same PID keeps that exact argv for
// the test's full duration and there is no forked grandchild to leak. SIGKILL
// (what Cleanup below sends) terminates a stopped process immediately.
//
// An earlier version of this helper wrote "#!/bin/sh\nexec sleep 30\n" to a
// script file and ran it as "sh scriptPath -L socketName". exec replaces the
// process image -- and its argv/comm -- in place, so the spawned process's
// real cmdline became ["sleep", "30"], discarding "-L socketName" entirely;
// one test still observed a plausible-looking value only because its first
// poll happened to land before the script's exec line ran.
func spawnFakeTmuxServer(t *testing.T, socketName, tmuxTmpDir string) int {
	t.Helper()

	cmd := exec.Command("sh", "-c", "kill -STOP $$", "-L", socketName)
	env := []string{"PATH=" + os.Getenv("PATH")}
	if tmuxTmpDir != "" {
		env = append(env, "TMUX_TMPDIR="+tmuxTmpDir)
	}
	cmd.Env = env
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting fake server: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})
	return cmd.Process.Pid
}

// writeFakeComm marks pid as a tmux server under the injected fake root,
// mirroring the real /proc/<pid>/comm content isTmuxServerComm looks for.
func writeFakeComm(t *testing.T, root string, pid int) {
	t.Helper()
	dir := filepath.Join(root, strconv.Itoa(pid))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("MkdirAll(%s): %v", dir, err)
	}
	if err := os.WriteFile(filepath.Join(dir, "comm"), []byte(tmuxServerComm+"\n"), 0o644); err != nil {
		t.Fatalf("writing fake comm: %v", err)
	}
}

// waitForServer polls listServersUnder(root) until it reports pid, since a
// freshly spawned process's /proc entries can take a moment to become
// readable under load.
func waitForServer(t *testing.T, root string, pid int) ServerProcess {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		servers, err := listServersUnder(root)
		if err != nil {
			t.Fatalf("listServersUnder(%s): %v", root, err)
		}
		for _, s := range servers {
			if s.PID == pid {
				return s
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("listServersUnder(%s) never reported spawned pid %d", root, pid)
	return ServerProcess{}
}

// TestListServersResolvesSocketPathFromTargetEnv is the RED test for
// ga-18nugn round 2 / review Finding 1: the socket path implied by a tmux
// server's -L argument must be resolved from the TARGET server's own
// TMUX_TMPDIR, not the reaper's. This test process's own TMUX_TMPDIR is
// deliberately set to a different, wrong value; with the pre-fix code
// (os.Getenv("TMUX_TMPDIR") reading the caller's own env) the computed
// SocketPath would incorrectly be rooted at that wrong value instead of the
// spawned target's.
func TestListServersResolvesSocketPathFromTargetEnv(t *testing.T) {
	t.Setenv("TMUX_TMPDIR", "/should-not-be-used-by-target-lookup")

	const socketName = "gctest-target-env-socket"
	const targetTmpDir = "/custom/target/tmux/tmp"
	pid := spawnFakeTmuxServer(t, socketName, targetTmpDir)

	root := t.TempDir()
	writeFakeComm(t, root, pid)

	got := waitForServer(t, root, pid)
	want := filepath.Join(targetTmpDir, fmt.Sprintf("tmux-%d", os.Getuid()), socketName)
	if got.SocketPath != want {
		t.Fatalf("SocketPath = %q, want %q (resolved from the target process's own TMUX_TMPDIR, not the caller's)", got.SocketPath, want)
	}
}

// TestListServersFallsBackToDefaultTmpDirWhenTargetEnvUnset covers the
// companion case: when the target process has no TMUX_TMPDIR set at all
// (not merely a different value from the caller's), resolution must still
// fall back to /tmp -- proving the fix doesn't overcorrect into never using
// the default.
func TestListServersFallsBackToDefaultTmpDirWhenTargetEnvUnset(t *testing.T) {
	t.Setenv("TMUX_TMPDIR", "/also-should-not-be-used-by-target-lookup")

	const socketName = "gctest-default-tmpdir-socket"
	pid := spawnFakeTmuxServer(t, socketName, "")

	root := t.TempDir()
	writeFakeComm(t, root, pid)

	got := waitForServer(t, root, pid)
	want := filepath.Join("/tmp", fmt.Sprintf("tmux-%d", os.Getuid()), socketName)
	if got.SocketPath != want {
		t.Fatalf("SocketPath = %q, want %q (default /tmp fallback when target has no TMUX_TMPDIR)", got.SocketPath, want)
	}
}
