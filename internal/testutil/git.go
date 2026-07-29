package testutil

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

// InitGitRepo creates a test-owned repository with one commit and returns its
// path and initial branch.
func InitGitRepo(t testing.TB) (string, string) {
	t.Helper()
	dir := t.TempDir()
	RunGit(t, dir, "init")
	RunGit(t, dir, "config", "user.email", "test@test.com")
	RunGit(t, dir, "config", "user.name", "Test")
	RunGit(t, dir, "commit", "--allow-empty", "-m", "init")
	return dir, RunGit(t, dir, "rev-parse", "--abbrev-ref", "HEAD")
}

// InitGitRepoWithoutCWD creates a repository using git -C so callers do not
// consume the process-working-directory resource.
func InitGitRepoWithoutCWD(t testing.TB) (string, string) {
	t.Helper()
	dir := t.TempDir()
	env := gitTestEnv()
	RunCommandAtProcessCWD(t, env, "git", "init", dir)
	RunCommandAtProcessCWD(t, env, "git", "-C", dir, "config", "user.email", "test@test.com")
	RunCommandAtProcessCWD(t, env, "git", "-C", dir, "config", "user.name", "Test")
	RunCommandAtProcessCWD(t, env, "git", "-C", dir, "commit", "--allow-empty", "-m", "init")
	return dir, RunCommandAtProcessCWD(t, env, "git", "-C", dir, "rev-parse", "--abbrev-ref", "HEAD")
}

// RunGit runs git with repository-locating environment variables removed so
// ambient Git state cannot redirect a test command.
func RunGit(t testing.TB, dir string, args ...string) string {
	t.Helper()
	return RunCommand(t, dir, gitTestEnv(), "git", args...)
}

func gitTestEnv() []string {
	env := make([]string, 0, len(os.Environ()))
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		switch key {
		case "GIT_DIR", "GIT_WORK_TREE", "GIT_INDEX_FILE", "GIT_OBJECT_DIRECTORY", "GIT_COMMON_DIR":
			continue
		}
		env = append(env, entry)
	}
	return env
}

// RunCommand runs a test-owned subprocess and returns trimmed combined output.
func RunCommand(t testing.TB, dir string, env []string, name string, args ...string) string {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s %s: %s: %v", name, strings.Join(args, " "), out, err)
	}
	return strings.TrimSpace(string(out))
}

// RunCommandAtProcessCWD runs a test-owned subprocess without changing its
// working directory.
func RunCommandAtProcessCWD(t testing.TB, env []string, name string, args ...string) string {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s %s: %s: %v", name, strings.Join(args, " "), out, err)
	}
	return strings.TrimSpace(string(out))
}

// LookPath resolves an executable for a test without exposing os/exec at each
// call site.
func LookPath(t testing.TB, name string) string {
	t.Helper()
	path, err := exec.LookPath(name)
	if err != nil {
		t.Fatalf("LookPath %s: %v", name, err)
	}
	return path
}
