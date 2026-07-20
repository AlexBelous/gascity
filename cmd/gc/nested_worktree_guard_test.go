package main

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gastownhall/gascity/internal/doctor"
	"github.com/gastownhall/gascity/internal/git"
)

// fakeNestedGuardGit implements doctor.GitWorktree for
// blockedByNestedWorktree tests. WorktreeList is served identically
// regardless of which path constructed it (mirrors a shared admin
// dir); per-path uncommitted flags drive whether a listed nested
// candidate classifies as safe or unsafe.
type fakeNestedGuardGit struct {
	listResp    []git.Worktree
	listErr     error
	uncommitted map[string]bool
	currentPath string
}

func (f *fakeNestedGuardGit) IsRepo() bool { return true }
func (f *fakeNestedGuardGit) WorktreeList() ([]git.Worktree, error) {
	return f.listResp, f.listErr
}
func (f *fakeNestedGuardGit) HasUncommittedWork() bool                { return f.uncommitted[f.currentPath] }
func (f *fakeNestedGuardGit) HasUnpushedCommitsResult() (bool, error) { return false, nil }
func (f *fakeNestedGuardGit) HasStashesResult() (bool, error)         { return false, nil }
func (f *fakeNestedGuardGit) WorktreeRemove(string, bool) error       { return nil }

func withNestedGuardGit(t *testing.T, factory func(path string) doctor.GitWorktree) {
	t.Helper()
	orig := newNestedWorktreeGitProbe
	t.Cleanup(func() { newNestedWorktreeGitProbe = orig })
	newNestedWorktreeGitProbe = factory
}

// stubNoNestedWorktrees points newNestedWorktreeGitProbe at a fake reporting
// no nested worktrees. Other call sites' tests use this as the permissive
// default so the containment guard never activates for scenarios that aren't
// exercising it.
func stubNoNestedWorktrees(t *testing.T) {
	t.Helper()
	withNestedGuardGit(t, func(path string) doctor.GitWorktree {
		return &fakeNestedGuardGit{currentPath: path}
	})
}

// stubNestedWorktreeFound points newNestedWorktreeGitProbe at a fake
// reporting a single nested worktree at nestedPath, for tests exercising the
// containment guard's blocking behavior at real call sites.
func stubNestedWorktreeFound(t *testing.T, nestedPath string) {
	t.Helper()
	withNestedGuardGit(t, func(path string) doctor.GitWorktree {
		return &fakeNestedGuardGit{
			currentPath: path,
			listResp:    []git.Worktree{{Path: nestedPath, Branch: "child-branch"}},
		}
	})
}

func TestBlockedByNestedWorktree_NoNested(t *testing.T) {
	dir := t.TempDir()
	withNestedGuardGit(t, func(path string) doctor.GitWorktree {
		return &fakeNestedGuardGit{
			currentPath: path,
			listResp: []git.Worktree{
				{Path: dir, Branch: "home-branch"},
				{Path: filepath.Join(filepath.Dir(dir), "sibling"), Branch: "sibling"},
			},
		}
	})

	blocked, reason := blockedByNestedWorktree(dir)
	if blocked {
		t.Errorf("blocked = true, want false (reason: %q)", reason)
	}
	if reason != "" {
		t.Errorf("reason = %q, want empty", reason)
	}
}

func TestBlockedByNestedWorktree_UnsafeNestedBlocks(t *testing.T) {
	dir := t.TempDir()
	nested := filepath.Join(dir, "child-worktree")
	withNestedGuardGit(t, func(path string) doctor.GitWorktree {
		return &fakeNestedGuardGit{
			currentPath: path,
			listResp:    []git.Worktree{{Path: nested, Branch: "child-branch"}},
			uncommitted: map[string]bool{nested: true},
		}
	})

	blocked, reason := blockedByNestedWorktree(dir)
	if !blocked {
		t.Fatal("blocked = false, want true")
	}
	if !strings.Contains(reason, nested) {
		t.Errorf("reason = %q, want it to mention %q", reason, nested)
	}
}

// TestBlockedByNestedWorktree_SafeNestedStillBlocks pins a deliberate
// design decision: the guard blocks on ANY nested finding, not just
// ones the mechanical safety gates rejected. Nested-reap runs before
// the guard in a tick, so a nested worktree that survives to this
// point and is also git-clean may still belong to a bead that just
// isn't closed yet — the guard has no bead-store access to tell the
// difference, so it must not assume clean means safe to destroy.
func TestBlockedByNestedWorktree_SafeNestedStillBlocks(t *testing.T) {
	dir := t.TempDir()
	nested := filepath.Join(dir, "child-worktree")
	withNestedGuardGit(t, func(path string) doctor.GitWorktree {
		return &fakeNestedGuardGit{
			currentPath: path,
			listResp:    []git.Worktree{{Path: nested, Branch: "child-branch"}},
		}
	})

	blocked, reason := blockedByNestedWorktree(dir)
	if !blocked {
		t.Fatal("blocked = false, want true: a mechanically-clean nested worktree may still belong to an open bead")
	}
	if !strings.Contains(reason, nested) {
		t.Errorf("reason = %q, want it to mention %q", reason, nested)
	}
}

func TestBlockedByNestedWorktree_ProbeErrorFailsClosed(t *testing.T) {
	dir := t.TempDir()
	withNestedGuardGit(t, func(path string) doctor.GitWorktree {
		return &fakeNestedGuardGit{
			currentPath: path,
			listErr:     errors.New("boom"),
		}
	})

	blocked, reason := blockedByNestedWorktree(dir)
	if !blocked {
		t.Fatal("blocked = false, want true (fail closed on probe error)")
	}
	if reason == "" {
		t.Error("reason = \"\", want non-empty explanation")
	}
}
