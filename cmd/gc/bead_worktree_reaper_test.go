package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/config"
	"github.com/gastownhall/gascity/internal/events"
	"github.com/gastownhall/gascity/internal/git"
)

func gaConfig() *config.City {
	return &config.City{
		Workspace: config.Workspace{Name: "test", Prefix: "ga"},
	}
}

func TestExtractBeadIDFromWorktreeNameBareID(t *testing.T) {
	cfg := gaConfig()
	got := extractBeadIDFromWorktreeName(cfg, "ga-n0oafq")
	if got != "ga-n0oafq" {
		t.Errorf("got %q, want %q", got, "ga-n0oafq")
	}
}

func TestExtractBeadIDFromWorktreeNameCompound(t *testing.T) {
	cfg := gaConfig()
	got := extractBeadIDFromWorktreeName(cfg, "builder-ga-34q3ss")
	if got != "ga-34q3ss" {
		t.Errorf("got %q, want %q", got, "ga-34q3ss")
	}
}

func TestExtractBeadIDFromWorktreeNameNoMatch(t *testing.T) {
	cfg := gaConfig()
	got := extractBeadIDFromWorktreeName(cfg, "builder-feature-branch")
	if got != "" {
		t.Errorf("got %q, want empty", got)
	}
}

func TestExtractBeadIDFromWorktreeNameSingleSegment(t *testing.T) {
	cfg := gaConfig()
	got := extractBeadIDFromWorktreeName(cfg, "builder")
	if got != "" {
		t.Errorf("got %q, want empty", got)
	}
}

func TestExtractBeadIDFromWorktreeNameNilConfig(t *testing.T) {
	got := extractBeadIDFromWorktreeName(nil, "ga-n0oafq")
	if got != "" {
		t.Errorf("got %q, want empty for nil config", got)
	}
}

func TestExtractBeadIDFromWorktreeNameEmptyName(t *testing.T) {
	got := extractBeadIDFromWorktreeName(gaConfig(), "")
	if got != "" {
		t.Errorf("got %q, want empty for empty name", got)
	}
}

func TestIsStrictlyUnderDirSubpath(t *testing.T) {
	dir := filepath.Join("a", "b")
	path := filepath.Join("a", "b", "c")
	if !isStrictlyUnderDir(dir, path) {
		t.Errorf("isStrictlyUnderDir(%q, %q) = false, want true", dir, path)
	}
}

func TestIsStrictlyUnderDirSameDir(t *testing.T) {
	dir := filepath.Join("a", "b")
	if isStrictlyUnderDir(dir, dir) {
		t.Errorf("isStrictlyUnderDir(%q, %q) = true, want false (same dir)", dir, dir)
	}
}

func TestIsStrictlyUnderDirPathTraversal(t *testing.T) {
	dir := filepath.Join("a", "b")
	path := filepath.Join("a", "c") // sibling — relative path starts with ".."
	if isStrictlyUnderDir(dir, path) {
		t.Errorf("isStrictlyUnderDir(%q, %q) = true, want false (path traversal)", dir, path)
	}
}

func TestIsStrictlyUnderDirDeepSubpath(t *testing.T) {
	dir := filepath.Join("root", "worktrees")
	path := filepath.Join("root", "worktrees", "gascity", "builder")
	if !isStrictlyUnderDir(dir, path) {
		t.Errorf("isStrictlyUnderDir(%q, %q) = false, want true", dir, path)
	}
}

// runReaperTestGit runs git in dir with git-locating environment variables
// stripped via git.SanitizedEnv, so the subprocess targets dir itself rather
// than leaking the discovery context of whatever repo this test binary
// happens to run inside (e.g. a nested worktree).
func runReaperTestGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = git.SanitizedEnv()
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s (in %s): %v\n%s", strings.Join(args, " "), dir, err, out)
	}
}

// initReaperTestGitRepo creates a real git repository with one commit, for
// tests that need reapClosedBeadWorktrees to run real git worktree plumbing
// rather than a fake probe. A refs/remotes/origin/main ref is pointed at the
// initial commit (without a real remote) so HasUnpushedCommitsResult sees
// the commit as pushed — otherwise every worktree branched from HEAD would
// trip the unpushed-commits safety gate before the containment guard ever
// runs, since a repo with no remotes at all treats every commit as unpushed.
func initReaperTestGitRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	runReaperTestGit(t, dir, "init")
	runReaperTestGit(t, dir, "config", "user.email", "test@example.com")
	runReaperTestGit(t, dir, "config", "user.name", "Test")
	runReaperTestGit(t, dir, "commit", "--allow-empty", "-m", "init")
	runReaperTestGit(t, dir, "update-ref", "refs/remotes/origin/main", "HEAD")
	return dir
}

// TestReapClosedBeadWorktrees_NestedWorktreeBlocks covers the containment
// guard at the one call site with no injectable git interface — unlike the
// prune and agent-home-cleanup sites, reapClosedBeadWorktrees talks to
// git.New directly, so it has no fake-git test coverage of its own. This
// test stands up a real git worktree and only fakes the nested-worktree
// probe (via stubNestedWorktreeFound, shared with the other two call
// sites' guard tests) to pin the guard's integration here specifically.
func TestReapClosedBeadWorktrees_NestedWorktreeBlocks(t *testing.T) {
	cityPath := initReaperTestGitRepo(t)

	worktreePath := filepath.Join(cityPath, ".gc", "worktrees", "gascity", "builder-ga-abc123")
	if err := os.MkdirAll(filepath.Dir(worktreePath), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	runReaperTestGit(t, cityPath, "worktree", "add", "-b", "builder-ga-abc123-branch", worktreePath)

	store := beads.NewMemStoreFrom(1, []beads.Bead{
		{ID: "ga-abc123", Title: "test bead", Status: "closed", Type: "task"},
	}, nil)
	rigBeadStores := map[string]beads.Store{"gascity": store}

	nestedPath := filepath.Join(worktreePath, "child-worktree")
	stubNestedWorktreeFound(t, nestedPath)

	rec := &capturingRecorder{}
	var stderr bytes.Buffer

	reaped := reapClosedBeadWorktrees(cityPath, gaConfig(), rigBeadStores, rec, &stderr)

	if reaped != 0 {
		t.Errorf("reaped = %d, want 0", reaped)
	}
	if _, err := os.Stat(worktreePath); err != nil {
		t.Errorf("worktreePath should still exist after a blocked reap, Stat: %v", err)
	}
	if !strings.Contains(stderr.String(), "nested worktree") {
		t.Errorf("stderr = %q, want it to mention the nested worktree", stderr.String())
	}

	var skipped []events.Event
	for _, e := range rec.events {
		if e.Type == events.BeadWorktreeReapSkipped {
			skipped = append(skipped, e)
		}
	}
	if len(skipped) != 1 {
		t.Fatalf("got %d BeadWorktreeReapSkipped events, want 1 (all events: %+v)", len(skipped), rec.events)
	}
	if skipped[0].Subject != "ga-abc123" {
		t.Errorf("skipped event Subject = %q, want %q", skipped[0].Subject, "ga-abc123")
	}
}
