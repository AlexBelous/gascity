package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gastownhall/gascity/internal/testutil"
	"github.com/gastownhall/gascity/internal/worktree"
)

func worktreeTestRepo(t *testing.T) (string, string) {
	t.Helper()
	return testutil.InitGitRepo(t)
}

func managedWorktreeCmdOpts(repo, root, path, base string) worktreeCmdOpts {
	return worktreeCmdOpts{
		Repo:       repo,
		Root:       root,
		Path:       path,
		Branch:     "work/gc-test",
		Base:       base,
		BeadID:     "gc-test",
		StoreRef:   "gascity",
		Creator:    "test",
		Owner:      "gc-sling",
		Generation: "1",
		Lifecycle:  worktree.LifecycleActive,
	}
}

func TestCmdWorktreeEnsureCreatesAndVerifies(t *testing.T) {
	repo, base := worktreeTestRepo(t)
	root := t.TempDir()
	wt := filepath.Join(root, "wt")
	var stdout, stderr bytes.Buffer

	opts := managedWorktreeCmdOpts(repo, root, wt, base)
	opts.JSON = true
	code := runWorktreeEnsure(opts, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("ensure exit = %d, stderr: %s", code, stderr.String())
	}
	var rep struct {
		Path          string `json:"path"`
		Branch        string `json:"branch"`
		Created       bool   `json:"created"`
		BranchCreated bool   `json:"branch_created"`
		Provenance    *struct {
			BaseSHA   string `json:"base_sha"`
			AttemptID string `json:"attempt_id"`
		} `json:"provenance"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &rep); err != nil {
		t.Fatalf("unmarshal ensure output %q: %v", stdout.String(), err)
	}
	if !rep.Created || !rep.BranchCreated || rep.Branch != "work/gc-test" ||
		rep.Provenance == nil || rep.Provenance.BaseSHA == "" || rep.Provenance.AttemptID == "" {
		t.Errorf("report = %+v, want created managed worktree with publishable provenance", rep)
	}

	// verify must pass on the ensured worktree.
	stdout.Reset()
	stderr.Reset()
	verifyOpts := opts
	verifyOpts.BaseSHA = rep.Provenance.BaseSHA
	code = runWorktreeVerify(verifyOpts, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("verify exit = %d, stderr: %s", code, stderr.String())
	}
}

func TestCmdWorktreeVerifyFailsOnMissing(t *testing.T) {
	repo, _ := worktreeTestRepo(t)
	root := t.TempDir()
	var stdout, stderr bytes.Buffer
	opts := managedWorktreeCmdOpts(repo, root, filepath.Join(root, "nope"), "main")
	code := runWorktreeVerify(opts, &stdout, &stderr)
	if code == 0 {
		t.Fatal("verify on missing worktree returned 0, want nonzero")
	}
	if !strings.Contains(stderr.String(), "does not exist") {
		t.Errorf("stderr %q does not explain the missing path", stderr.String())
	}
}

func TestCmdWorktreeEnsureDryRunIsPure(t *testing.T) {
	repo, base := worktreeTestRepo(t)
	root := t.TempDir()
	wt := filepath.Join(root, "wt")
	var stdout, stderr bytes.Buffer
	opts := managedWorktreeCmdOpts(repo, root, wt, base)
	opts.DryRun = true
	opts.JSON = true
	code := runWorktreeEnsure(opts, &stdout, &stderr)
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

func TestCmdWorktreeManagedFlagsReachSpec(t *testing.T) {
	repo, base := worktreeTestRepo(t)
	root := t.TempDir()
	path := filepath.Join(root, "gc-test")
	opts := managedWorktreeCmdOpts(repo, root, path, base)
	opts.BaseSHA = strings.Repeat("a", 40)

	spec, err := opts.spec()
	if err != nil {
		t.Fatalf("spec: %v", err)
	}
	if spec.Root != root || spec.BeadID != opts.BeadID || spec.StoreRef != opts.StoreRef ||
		spec.BaseSHA != opts.BaseSHA || spec.Creator != opts.Creator || spec.Owner != opts.Owner ||
		spec.Generation != opts.Generation || spec.Lifecycle != opts.Lifecycle {
		t.Fatalf("spec = %+v, want all managed ownership fields from CLI", spec)
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
