package scripts_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// TestGateLock runs the shell self-test for scripts/lib/gate-lock.sh, the
// pre-push gate concurrency control (ga-scxvr8). It exercises
// gc_gate_serialize_run directly (uncontended run, exit-code propagation,
// serialization of concurrent callers, the "waiting for N other gate
// run(s)" message, queue-don't-fail semantics, and timeout past a generous
// deadline), plus .githooks/pre-push wiring end to end against a real bare
// remote to prove a bare `git push` is serialized without the caller
// wrapping anything in flock itself. Hermetic: temp git repos and a fake bd
// only, no network/gh/model calls, and never touches the real shared
// /var/tmp/gc-gate-serialize.lock.
func TestGateLock(t *testing.T) {
	root := repoRoot(t)

	cmd := exec.Command(filepath.Join(root, "scripts", "test-gate-lock.sh"))
	cmd.Dir = root
	cmd.Env = []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + t.TempDir(),
		"TMPDIR=" + t.TempDir(),
	}

	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("test-gate-lock.sh failed: %v\n%s", err, out)
	}
}
