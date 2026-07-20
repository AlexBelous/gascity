package worktree

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gastownhall/gascity/internal/git"
)

// runGit runs a git command in dir and fails the test on error. Strips
// repository-locating git env vars so host hooks cannot interfere.
func runGit(t *testing.T, dir string, args ...string) string {
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

// initTestRepo creates a git repo with one commit and returns its path and
// the name of its initial branch.
func initTestRepo(t *testing.T) (string, string) {
	t.Helper()
	dir := t.TempDir()
	runGit(t, dir, "init")
	runGit(t, dir, "config", "user.email", "test@test.com")
	runGit(t, dir, "config", "user.name", "Test")
	runGit(t, dir, "commit", "--allow-empty", "-m", "init")
	branch := runGit(t, dir, "rev-parse", "--abbrev-ref", "HEAD")
	return dir, branch
}

// snapshotDir returns the sorted entries of a directory, or nil when it does
// not exist. Used to assert dry-run filesystem purity.
func snapshotDir(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatalf("ReadDir(%q): %v", dir, err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

func TestVerifyValidWorktree(t *testing.T) {
	repo, base := initTestRepo(t)
	wt := filepath.Join(t.TempDir(), "wt")
	runGit(t, repo, "worktree", "add", "-b", "feat", wt, base)

	rep, err := Verify(Spec{RepoDir: repo, Path: wt, Branch: "feat"})
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if rep.Path != wt || rep.Branch != "feat" {
		t.Errorf("report = %+v, want path %q branch feat", rep, wt)
	}
	if len(rep.Head) != 40 {
		t.Errorf("report.Head = %q, want 40-char SHA", rep.Head)
	}
}

func TestVerifyMissingPath(t *testing.T) {
	repo, _ := initTestRepo(t)
	missing := filepath.Join(t.TempDir(), "nope")
	_, err := Verify(Spec{RepoDir: repo, Path: missing, Branch: "feat"})
	if !errors.Is(err, ErrWorktreeMissing) {
		t.Errorf("Verify on missing path: err = %v, want ErrWorktreeMissing", err)
	}
}

func TestVerifyNotAWorktree(t *testing.T) {
	repo, _ := initTestRepo(t)
	plain := t.TempDir()
	_, err := Verify(Spec{RepoDir: repo, Path: plain, Branch: "feat"})
	if err == nil {
		t.Fatal("Verify on plain dir succeeded, want error")
	}
	if errors.Is(err, ErrWorktreeMissing) {
		t.Error("plain existing dir reported as ErrWorktreeMissing; must be a distinct error so Ensure never clobbers it")
	}
}

func TestVerifyWrongBranch(t *testing.T) {
	repo, base := initTestRepo(t)
	wt := filepath.Join(t.TempDir(), "wt")
	runGit(t, repo, "worktree", "add", "-b", "actual", wt, base)
	_, err := Verify(Spec{RepoDir: repo, Path: wt, Branch: "expected"})
	if err == nil || !strings.Contains(err.Error(), "expected") {
		t.Errorf("Verify wrong branch: err = %v, want branch mismatch mentioning %q", err, "expected")
	}
}

func TestVerifyDetachedHead(t *testing.T) {
	repo, base := initTestRepo(t)
	wt := filepath.Join(t.TempDir(), "wt")
	runGit(t, repo, "worktree", "add", "-b", "feat", wt, base)
	sha := runGit(t, wt, "rev-parse", "HEAD")
	runGit(t, wt, "checkout", "--detach", sha)
	_, err := Verify(Spec{RepoDir: repo, Path: wt, Branch: "feat"})
	if err == nil {
		t.Fatal("Verify on detached HEAD succeeded, want error")
	}
}

func TestVerifyDifferentRepo(t *testing.T) {
	repoA, _ := initTestRepo(t)
	repoB, baseB := initTestRepo(t)
	wt := filepath.Join(t.TempDir(), "wt")
	runGit(t, repoB, "worktree", "add", "-b", "feat", wt, baseB)
	_, err := Verify(Spec{RepoDir: repoA, Path: wt, Branch: "feat"})
	if err == nil {
		t.Fatal("Verify against wrong repo succeeded, want repository identity error")
	}
}

func TestVerifySubdirOfWorktreeFails(t *testing.T) {
	repo, base := initTestRepo(t)
	wt := filepath.Join(t.TempDir(), "wt")
	runGit(t, repo, "worktree", "add", "-b", "feat", wt, base)
	sub := filepath.Join(wt, "sub")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	_, err := Verify(Spec{RepoDir: repo, Path: sub, Branch: "feat"})
	if err == nil {
		t.Fatal("Verify on worktree subdirectory succeeded, want error")
	}
}

func TestVerifySpecValidation(t *testing.T) {
	repo, _ := initTestRepo(t)
	cases := []Spec{
		{RepoDir: "", Path: "/tmp/x", Branch: "b"},
		{RepoDir: repo, Path: "", Branch: "b"},
		{RepoDir: repo, Path: "/tmp/x", Branch: ""},
		{RepoDir: repo, Path: "relative/path", Branch: "b"},
		{RepoDir: "relative", Path: "/tmp/x", Branch: "b"},
	}
	for i, spec := range cases {
		if _, err := Verify(spec); err == nil {
			t.Errorf("case %d: Verify(%+v) succeeded, want validation error", i, spec)
		}
		if _, err := Ensure(spec); err == nil {
			t.Errorf("case %d: Ensure(%+v) succeeded, want validation error", i, spec)
		}
	}
}

func TestEnsureCreatesNewBranchWorktree(t *testing.T) {
	repo, base := initTestRepo(t)
	wt := filepath.Join(t.TempDir(), "wt")
	baseSHA := runGit(t, repo, "rev-parse", base)

	rep, err := Ensure(Spec{RepoDir: repo, Path: wt, Branch: "feat", Base: base})
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if !rep.Created || !rep.BranchCreated {
		t.Errorf("report = %+v, want Created=true BranchCreated=true", rep)
	}
	if rep.Head != baseSHA {
		t.Errorf("report.Head = %q, want base SHA %q", rep.Head, baseSHA)
	}
	// Postconditions on disk: attached HEAD on the right branch.
	if got := runGit(t, wt, "symbolic-ref", "HEAD"); got != "refs/heads/feat" {
		t.Errorf("worktree HEAD = %q, want refs/heads/feat", got)
	}
	if got := runGit(t, wt, "rev-parse", "HEAD"); got != baseSHA {
		t.Errorf("worktree HEAD SHA = %q, want %q", got, baseSHA)
	}
}

func TestEnsureIdempotent(t *testing.T) {
	repo, base := initTestRepo(t)
	wt := filepath.Join(t.TempDir(), "wt")
	if _, err := Ensure(Spec{RepoDir: repo, Path: wt, Branch: "feat", Base: base}); err != nil {
		t.Fatalf("first Ensure: %v", err)
	}
	rep, err := Ensure(Spec{RepoDir: repo, Path: wt, Branch: "feat", Base: base})
	if err != nil {
		t.Fatalf("second Ensure: %v", err)
	}
	if rep.Created || rep.BranchCreated {
		t.Errorf("second Ensure report = %+v, want Created=false BranchCreated=false", rep)
	}
}

func TestEnsureAttachesExistingBranch(t *testing.T) {
	repo, base := initTestRepo(t)
	runGit(t, repo, "branch", "feat", base)
	tip := runGit(t, repo, "rev-parse", "feat")
	wt := filepath.Join(t.TempDir(), "wt")

	rep, err := Ensure(Spec{RepoDir: repo, Path: wt, Branch: "feat"})
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if !rep.Created || rep.BranchCreated {
		t.Errorf("report = %+v, want Created=true BranchCreated=false", rep)
	}
	if got := runGit(t, repo, "rev-parse", "feat"); got != tip {
		t.Errorf("attaching moved branch tip from %q to %q", tip, got)
	}
}

func TestEnsureBaseRequiredForNewBranch(t *testing.T) {
	repo, _ := initTestRepo(t)
	wt := filepath.Join(t.TempDir(), "wt")
	_, err := Ensure(Spec{RepoDir: repo, Path: wt, Branch: "feat"})
	if err == nil {
		t.Fatal("Ensure with no base and missing branch succeeded, want error")
	}
	if _, statErr := os.Stat(wt); statErr == nil {
		t.Error("failed Ensure created the worktree path")
	}
}

func TestEnsureUnresolvableBaseFails(t *testing.T) {
	repo, _ := initTestRepo(t)
	wt := filepath.Join(t.TempDir(), "wt")
	_, err := Ensure(Spec{RepoDir: repo, Path: wt, Branch: "feat", Base: "no-such-ref"})
	if err == nil {
		t.Fatal("Ensure with unresolvable base succeeded, want error")
	}
	if _, statErr := os.Stat(wt); statErr == nil {
		t.Error("failed Ensure created the worktree path")
	}
	if out := runGit(t, repo, "branch", "--list", "feat"); out != "" {
		t.Errorf("failed Ensure created branch: %q", out)
	}
}

func TestEnsurePreservesLocalBaseIntent(t *testing.T) {
	// A base of "main" must resolve to the LOCAL main, even when a
	// same-named remote-tracking ref points elsewhere.
	repo, base := initTestRepo(t)
	localSHA := runGit(t, repo, "rev-parse", base)
	runGit(t, repo, "commit", "--allow-empty", "-m", "remote-ahead")
	remoteSHA := runGit(t, repo, "rev-parse", "HEAD")
	runGit(t, repo, "update-ref", "refs/remotes/origin/"+base, remoteSHA)
	runGit(t, repo, "reset", "--hard", localSHA)

	wt := filepath.Join(t.TempDir(), "wt")
	rep, err := Ensure(Spec{RepoDir: repo, Path: wt, Branch: "feat", Base: base})
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if rep.Head != localSHA {
		t.Errorf("Ensure based branch on %q, want LOCAL %s %q (remote was %q)", rep.Head, base, localSHA, remoteSHA)
	}
}

func TestEnsureRefusesExistingNonWorktreePath(t *testing.T) {
	repo, base := initTestRepo(t)
	plain := t.TempDir()
	marker := filepath.Join(plain, "keep.txt")
	if err := os.WriteFile(marker, []byte("data"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	_, err := Ensure(Spec{RepoDir: repo, Path: plain, Branch: "feat", Base: base})
	if err == nil {
		t.Fatal("Ensure over plain dir succeeded, want error")
	}
	if _, statErr := os.Stat(marker); statErr != nil {
		t.Error("Ensure touched contents of a pre-existing non-worktree dir")
	}
}

func TestEnsureDryRunIsPure(t *testing.T) {
	repo, base := initTestRepo(t)
	parent := t.TempDir()
	wt := filepath.Join(parent, "wt")
	beforeParent := snapshotDir(t, parent)
	beforeBranches := runGit(t, repo, "branch", "--list")
	beforeWorktrees := runGit(t, repo, "worktree", "list", "--porcelain")

	rep, err := Ensure(Spec{RepoDir: repo, Path: wt, Branch: "feat", Base: base, DryRun: true})
	if err != nil {
		t.Fatalf("dry-run Ensure: %v", err)
	}
	if rep.Created || rep.BranchCreated {
		t.Errorf("dry-run report = %+v, want Created=false BranchCreated=false", rep)
	}
	if len(rep.Planned) == 0 {
		t.Error("dry-run report has no Planned actions")
	}
	if _, statErr := os.Stat(wt); statErr == nil {
		t.Error("dry-run created the worktree path")
	}
	if got := snapshotDir(t, parent); len(got) != len(beforeParent) {
		t.Errorf("dry-run mutated parent dir: before %v after %v", beforeParent, got)
	}
	if got := runGit(t, repo, "branch", "--list"); got != beforeBranches {
		t.Errorf("dry-run mutated branches: before %q after %q", beforeBranches, got)
	}
	if got := runGit(t, repo, "worktree", "list", "--porcelain"); got != beforeWorktrees {
		t.Errorf("dry-run mutated worktree list: before %q after %q", beforeWorktrees, got)
	}
}

func TestEnsureDryRunOnValidWorktree(t *testing.T) {
	repo, base := initTestRepo(t)
	wt := filepath.Join(t.TempDir(), "wt")
	if _, err := Ensure(Spec{RepoDir: repo, Path: wt, Branch: "feat", Base: base}); err != nil {
		t.Fatalf("setup Ensure: %v", err)
	}
	rep, err := Ensure(Spec{RepoDir: repo, Path: wt, Branch: "feat", Base: base, DryRun: true})
	if err != nil {
		t.Fatalf("dry-run Ensure on valid worktree: %v", err)
	}
	if rep.Created || len(rep.Planned) != 0 {
		t.Errorf("report = %+v, want no creation and no planned actions", rep)
	}
}

func TestEnsureDryRunStillFailsOnWrongState(t *testing.T) {
	// Dry-run must report the same error a real run would, without mutating.
	repo, base := initTestRepo(t)
	plain := t.TempDir()
	_, err := Ensure(Spec{RepoDir: repo, Path: plain, Branch: "feat", Base: base, DryRun: true})
	if err == nil {
		t.Fatal("dry-run Ensure over plain dir succeeded, want error")
	}
}

func TestRollbackRemovesCreatedState(t *testing.T) {
	repo, base := initTestRepo(t)
	wt := filepath.Join(t.TempDir(), "wt")
	g := git.New(repo)
	if err := g.WorktreeAddNewBranch(wt, "doomed", base); err != nil {
		t.Fatalf("WorktreeAddNewBranch: %v", err)
	}
	rollbackCreated(g, wt, "doomed", true)
	if _, statErr := os.Stat(wt); statErr == nil {
		t.Error("rollback left the worktree path in place")
	}
	if out := runGit(t, repo, "branch", "--list", "doomed"); out != "" {
		t.Errorf("rollback left branch in place: %q", out)
	}
}

func TestRollbackKeepsPreexistingBranch(t *testing.T) {
	repo, base := initTestRepo(t)
	runGit(t, repo, "branch", "keep", base)
	wt := filepath.Join(t.TempDir(), "wt")
	g := git.New(repo)
	if err := g.WorktreeAddExistingBranch(wt, "keep"); err != nil {
		t.Fatalf("WorktreeAddExistingBranch: %v", err)
	}
	rollbackCreated(g, wt, "keep", false)
	if _, statErr := os.Stat(wt); statErr == nil {
		t.Error("rollback left the worktree path in place")
	}
	if out := runGit(t, repo, "branch", "--list", "keep"); out == "" {
		t.Error("rollback deleted a pre-existing branch")
	}
}
