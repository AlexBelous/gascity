package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func worktreeTestRepo(t *testing.T) (string, string) {
	t.Helper()
	dir := t.TempDir()
	run := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		for _, e := range os.Environ() {
			k, _, _ := strings.Cut(e, "=")
			switch k {
			case "GIT_DIR", "GIT_WORK_TREE", "GIT_INDEX_FILE", "GIT_OBJECT_DIRECTORY", "GIT_COMMON_DIR":
				continue
			}
			cmd.Env = append(cmd.Env, e)
		}
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %s: %s: %v", strings.Join(args, " "), out, err)
		}
		return strings.TrimSpace(string(out))
	}
	run("init")
	run("config", "user.email", "test@test.com")
	run("config", "user.name", "Test")
	run("commit", "--allow-empty", "-m", "init")
	return dir, run("rev-parse", "--abbrev-ref", "HEAD")
}

func TestCmdWorktreeEnsureCreatesAndVerifies(t *testing.T) {
	repo, base := worktreeTestRepo(t)
	wt := filepath.Join(t.TempDir(), "wt")
	var stdout, stderr bytes.Buffer

	code := runWorktreeEnsure(worktreeCmdOpts{
		Repo: repo, Path: wt, Branch: "feat", Base: base, JSON: true,
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("ensure exit = %d, stderr: %s", code, stderr.String())
	}
	var rep struct {
		Path          string `json:"path"`
		Branch        string `json:"branch"`
		Created       bool   `json:"created"`
		BranchCreated bool   `json:"branch_created"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &rep); err != nil {
		t.Fatalf("unmarshal ensure output %q: %v", stdout.String(), err)
	}
	if !rep.Created || !rep.BranchCreated || rep.Branch != "feat" {
		t.Errorf("report = %+v, want created new-branch feat", rep)
	}

	// verify must pass on the ensured worktree.
	stdout.Reset()
	stderr.Reset()
	code = runWorktreeVerify(worktreeCmdOpts{Repo: repo, Path: wt, Branch: "feat", JSON: true}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("verify exit = %d, stderr: %s", code, stderr.String())
	}
}

func TestCmdWorktreeVerifyFailsOnMissing(t *testing.T) {
	repo, _ := worktreeTestRepo(t)
	var stdout, stderr bytes.Buffer
	code := runWorktreeVerify(worktreeCmdOpts{
		Repo: repo, Path: filepath.Join(t.TempDir(), "nope"), Branch: "feat",
	}, &stdout, &stderr)
	if code == 0 {
		t.Fatal("verify on missing worktree returned 0, want nonzero")
	}
	if !strings.Contains(stderr.String(), "does not exist") {
		t.Errorf("stderr %q does not explain the missing path", stderr.String())
	}
}

func TestCmdWorktreeEnsureDryRunIsPure(t *testing.T) {
	repo, base := worktreeTestRepo(t)
	wt := filepath.Join(t.TempDir(), "wt")
	var stdout, stderr bytes.Buffer
	code := runWorktreeEnsure(worktreeCmdOpts{
		Repo: repo, Path: wt, Branch: "feat", Base: base, DryRun: true, JSON: true,
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("dry-run ensure exit = %d, stderr: %s", code, stderr.String())
	}
	if _, err := os.Stat(wt); !os.IsNotExist(err) {
		t.Error("dry-run ensure created the worktree path")
	}
	var rep struct {
		Planned []string `json:"planned"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &rep); err != nil {
		t.Fatalf("unmarshal dry-run output %q: %v", stdout.String(), err)
	}
	if len(rep.Planned) == 0 {
		t.Error("dry-run output has no planned actions")
	}
}

func TestCmdWorktreeRegistered(t *testing.T) {
	root := newRootCmd(&bytes.Buffer{}, &bytes.Buffer{})
	for _, c := range root.Commands() {
		if c.Name() == "worktree" {
			return
		}
	}
	t.Fatal("gc worktree command is not registered on the root command")
}
