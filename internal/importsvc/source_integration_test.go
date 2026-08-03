//go:build integration

package importsvc

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gastownhall/gascity/internal/git"
)

// TestCanonicalizeLocalGitImportSource_AcceptsRelativeTargetDir pins the
// ga-iawy13.7 canonical-path-at-ingest migration:
// canonicalizeLocalGitImportSource must succeed when targetDir is a valid
// relative path. The pre-migration bare EvalSymlinks call preserves
// relative inputs unresolved (Go's EvalSymlinks does not force an absolute
// result), which then makes the following filepath.Rel(repoRoot,
// resolvedTarget) call fail outright because repoRoot (from `git rev-parse
// --show-toplevel`) is always absolute while resolvedTarget stays relative;
// pathutil.NormalizePathForCompare always resolves to an absolute path
// first, fixing that. Every real caller today (resolveImportAddPath)
// already produces an absolute targetDir, so this closes a latent
// robustness gap rather than an observed production bug.
func TestCanonicalizeLocalGitImportSource_AcceptsRelativeTargetDir(t *testing.T) {
	repoRoot := initGitRepoForTest(t)
	sub := filepath.Join(repoRoot, "sub")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	t.Chdir(repoRoot)

	canonical, ok, err := canonicalizeLocalGitImportSource("sub")
	if err != nil {
		t.Fatalf("canonicalizeLocalGitImportSource(%q) error = %v, want success for a valid relative path", "sub", err)
	}
	if !ok {
		t.Fatalf("canonicalizeLocalGitImportSource(%q) ok = false, want true", "sub")
	}
	const wantSuffix = "//sub"
	if !strings.HasSuffix(canonical, wantSuffix) {
		t.Errorf("canonicalizeLocalGitImportSource(%q) = %q, want suffix %q", "sub", canonical, wantSuffix)
	}
}

// initGitRepoForTest creates a minimal git repository in a temp directory.
func initGitRepoForTest(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	runGitForTest(t, dir, "init")
	runGitForTest(t, dir, "config", "user.email", "test@test.com")
	runGitForTest(t, dir, "config", "user.name", "Test")
	return dir
}

func runGitForTest(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = git.SanitizedEnv()
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %s: %v", strings.Join(args, " "), out, err)
	}
}
