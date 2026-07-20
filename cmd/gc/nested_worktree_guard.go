package main

import (
	"fmt"

	"github.com/gastownhall/gascity/internal/doctor"
	"github.com/gastownhall/gascity/internal/git"
)

// newNestedWorktreeGitProbe produces a doctor.GitWorktree handle for a
// given path. Indirected through a package-level var so tests can
// inject a fake without standing up real git worktrees.
var newNestedWorktreeGitProbe = func(path string) doctor.GitWorktree { return git.New(path) }

// blockedByNestedWorktree reports whether dir still contains a nested
// git worktree, and if so, a human-readable reason. Every top-level
// worktree-removal call site must check this immediately before
// removing dir, so a still-live nested worktree is never destroyed as
// collateral damage of removing its parent.
//
// This is correct only because nested reaping runs before top-level
// removal within a reconciler tick: by the time this guard runs,
// anything still nested under dir was deliberately left behind by
// that phase — either its owning bead isn't closed yet, or it failed
// a mechanical safety gate. The guard blocks on any finding at all,
// not just unsafe ones: a mechanically clean nested worktree can still
// belong to an open bead, and this guard has no bead-store access to
// tell the difference, so it must not second-guess the reap phase's
// decision to leave it alone. A probe error fails closed rather than
// risking removal of live work.
func blockedByNestedWorktree(dir string) (bool, string) {
	findings, err := doctor.ListNestedWorktrees(dir, newNestedWorktreeGitProbe)
	if err != nil {
		return true, fmt.Sprintf("nested worktree probe failed: %v", err)
	}
	if len(findings) == 0 {
		return false, ""
	}
	return true, fmt.Sprintf("still contains nested worktree %s", findings[0].Path)
}
