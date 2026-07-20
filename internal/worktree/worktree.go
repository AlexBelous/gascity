// Package worktree is the transactional owner for agent workspace
// worktrees, exposed through the gc worktree CLI so provisioning paths can
// route through Ensure/Verify instead of running ad hoc git commands. When
// a path goes through this owner, the postconditions are uniform:
//
//   - the path is the root of a git worktree of the requested repository,
//   - the requested branch is checked out with an attached HEAD (never
//     detached),
//   - an explicit base keeps its verbatim local meaning (no remote
//     fallback), and
//   - a failed creation rolls back everything it created.
//
// Dry-run is observationally pure: it plans and validates without touching
// the filesystem or the repository.
package worktree

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/gastownhall/gascity/internal/git"
	"github.com/gastownhall/gascity/internal/pathutil"
)

// ErrWorktreeMissing reports that the spec path does not exist at all. It is
// the only verification failure Ensure treats as creatable; every other
// failure means the path holds state Ensure must not clobber.
var ErrWorktreeMissing = errors.New("worktree path does not exist")

// Spec describes the desired workspace worktree.
type Spec struct {
	// RepoDir is the repository (main checkout or any of its worktrees)
	// the workspace must belong to. Absolute.
	RepoDir string
	// Path is where the worktree must exist. Absolute.
	Path string
	// Branch must be checked out at Path with an attached HEAD.
	Branch string
	// Base is the ref a NEW branch is created from. It is resolved
	// verbatim against the local repository — "main" means local main,
	// never a remote fallback. Required only when Branch does not exist.
	Base string
	// DryRun plans without mutating anything.
	DryRun bool
}

// Report describes the observed or planned workspace state.
type Report struct {
	Path   string `json:"path"`
	Branch string `json:"branch"`
	// Head is the commit SHA at the worktree's HEAD. Empty when a dry-run
	// only planned creation.
	Head string `json:"head,omitempty"`
	// Created reports that Ensure created the worktree in this call.
	Created bool `json:"created"`
	// BranchCreated reports that Ensure created the branch in this call.
	BranchCreated bool `json:"branch_created"`
	// Planned lists the actions a dry-run would have executed.
	Planned []string `json:"planned,omitempty"`
}

func (s Spec) validate() error {
	if s.RepoDir == "" {
		return errors.New("worktree spec: repo dir must not be empty")
	}
	if !filepath.IsAbs(s.RepoDir) {
		return fmt.Errorf("worktree spec: repo dir %q must be absolute", s.RepoDir)
	}
	if s.Path == "" {
		return errors.New("worktree spec: path must not be empty")
	}
	if !filepath.IsAbs(s.Path) {
		return fmt.Errorf("worktree spec: path %q must be absolute", s.Path)
	}
	if s.Branch == "" {
		return errors.New("worktree spec: branch must not be empty")
	}
	return nil
}

// Verify checks that the spec's path is the root of a worktree of the
// spec's repository with the spec's branch checked out on an attached HEAD.
// It never mutates anything. A missing path returns ErrWorktreeMissing;
// any other failure describes the postcondition that does not hold.
func Verify(spec Spec) (Report, error) {
	if err := spec.validate(); err != nil {
		return Report{}, err
	}
	rep := Report{Path: spec.Path, Branch: spec.Branch}

	if _, err := os.Stat(spec.Path); err != nil {
		if os.IsNotExist(err) {
			return rep, fmt.Errorf("verifying worktree %q: %w", spec.Path, ErrWorktreeMissing)
		}
		return rep, fmt.Errorf("verifying worktree %q: %w", spec.Path, err)
	}

	repoGit := git.New(spec.RepoDir)
	if !repoGit.IsRepo() {
		return rep, fmt.Errorf("verifying worktree %q: repo dir %q is not a git repository", spec.Path, spec.RepoDir)
	}
	wtGit := git.New(spec.Path)
	if !wtGit.IsRepo() {
		return rep, fmt.Errorf("verifying worktree %q: path exists but is not inside a git repository", spec.Path)
	}

	// Repository identity: every worktree of one repository shares its
	// common git dir.
	repoCommon, err := canonicalCommonDir(repoGit)
	if err != nil {
		return rep, fmt.Errorf("verifying worktree %q: repo %q: %w", spec.Path, spec.RepoDir, err)
	}
	wtCommon, err := canonicalCommonDir(wtGit)
	if err != nil {
		return rep, fmt.Errorf("verifying worktree %q: %w", spec.Path, err)
	}
	if repoCommon != wtCommon {
		return rep, fmt.Errorf("verifying worktree %q: belongs to a different repository (common dir %q, want %q)", spec.Path, wtCommon, repoCommon)
	}

	// The path must be the worktree root itself, not a directory inside one.
	top, err := wtGit.TopLevel()
	if err != nil {
		return rep, fmt.Errorf("verifying worktree %q: %w", spec.Path, err)
	}
	if !pathutil.SamePath(top, spec.Path) {
		return rep, fmt.Errorf("verifying worktree %q: path is inside worktree rooted at %q, not a worktree root", spec.Path, top)
	}

	// Branch postcondition: attached HEAD on exactly the requested branch.
	ref, err := wtGit.HeadSymbolicRef()
	if err != nil {
		return rep, fmt.Errorf("verifying worktree %q: HEAD is detached: %w", spec.Path, err)
	}
	if want := "refs/heads/" + spec.Branch; ref != want {
		return rep, fmt.Errorf("verifying worktree %q: checked-out ref is %q, want %q", spec.Path, ref, want)
	}
	head, err := wtGit.RevParseVerifyCommit("HEAD")
	if err != nil {
		return rep, fmt.Errorf("verifying worktree %q: %w", spec.Path, err)
	}
	rep.Head = head
	return rep, nil
}

// Ensure makes the spec's worktree exist and verify, creating it when the
// path is absent. It is verify-first and transactional:
//
//   - an already-valid worktree returns unchanged (Created=false);
//   - a path that exists in any other state is an error — Ensure never
//     clobbers or "repairs" state it did not create;
//   - creation never detaches: a new branch is created from the verbatim
//     local base, an existing branch is attached as-is;
//   - every postcondition is re-verified after creation, and a failure
//     rolls back the created worktree and any created branch;
//   - with Spec.DryRun, Ensure plans and validates but mutates nothing.
func Ensure(spec Spec) (Report, error) {
	if err := spec.validate(); err != nil {
		return Report{}, err
	}

	rep, verifyErr := Verify(spec)
	if verifyErr == nil {
		return rep, nil
	}
	if !errors.Is(verifyErr, ErrWorktreeMissing) {
		return rep, verifyErr
	}

	repoGit := git.New(spec.RepoDir)
	if !repoGit.IsRepo() {
		return rep, fmt.Errorf("ensuring worktree %q: repo dir %q is not a git repository", spec.Path, spec.RepoDir)
	}

	branchExists := repoGit.BranchExists(spec.Branch)
	var resolvedBase string
	if !branchExists {
		if spec.Base == "" {
			return rep, fmt.Errorf("ensuring worktree %q: branch %q does not exist and no base was given", spec.Path, spec.Branch)
		}
		sha, err := repoGit.RevParseVerifyCommit(spec.Base)
		if err != nil {
			return rep, fmt.Errorf("ensuring worktree %q: %w", spec.Path, err)
		}
		resolvedBase = sha
	}

	if spec.DryRun {
		if branchExists {
			rep.Planned = []string{
				fmt.Sprintf("git worktree add %s %s", spec.Path, spec.Branch),
			}
		} else {
			rep.Planned = []string{
				fmt.Sprintf("git worktree add -b %s %s %s", spec.Branch, spec.Path, spec.Base),
			}
		}
		return rep, nil
	}

	if branchExists {
		if err := repoGit.WorktreeAddExistingBranch(spec.Path, spec.Branch); err != nil {
			return rep, fmt.Errorf("ensuring worktree %q: %w", spec.Path, err)
		}
	} else {
		// The base is passed verbatim so git's own branch-setup semantics
		// (e.g. upstream tracking) follow the operator's stated intent.
		if err := repoGit.WorktreeAddNewBranch(spec.Path, spec.Branch, spec.Base); err != nil {
			return rep, fmt.Errorf("ensuring worktree %q: %w", spec.Path, err)
		}
	}

	created, err := Verify(Spec{RepoDir: spec.RepoDir, Path: spec.Path, Branch: spec.Branch})
	if err != nil {
		rollbackCreated(repoGit, spec.Path, spec.Branch, !branchExists)
		return rep, fmt.Errorf("worktree %q failed post-create verification (rolled back): %w", spec.Path, err)
	}
	if !branchExists && created.Head != resolvedBase {
		rollbackCreated(repoGit, spec.Path, spec.Branch, true)
		return rep, fmt.Errorf("worktree %q: new branch %q is at %s, want base %q = %s (rolled back)",
			spec.Path, spec.Branch, created.Head, spec.Base, resolvedBase)
	}
	created.Created = true
	created.BranchCreated = !branchExists
	return created, nil
}

// rollbackCreated undoes a creation whose postconditions failed: it removes
// the worktree, prunes stale worktree records, and deletes the branch only
// when this Ensure call created it. Rollback is best-effort — the primary
// error is the postcondition failure the caller is already returning.
func rollbackCreated(repoGit *git.Git, path, branch string, branchCreated bool) {
	_ = repoGit.WorktreeRemove(path, true) //nolint:errcheck // best-effort rollback
	_ = repoGit.WorktreePrune()            //nolint:errcheck // best-effort rollback
	if branchCreated {
		_ = repoGit.BranchDelete(branch) //nolint:errcheck // best-effort rollback
	}
}

// canonicalCommonDir returns the repository common dir with symlinks
// resolved, so identity comparison is not defeated by symlinked paths.
func canonicalCommonDir(g *git.Git) (string, error) {
	common, err := g.CommonDir()
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(common)
	if err != nil {
		return "", fmt.Errorf("resolving common dir %q: %w", common, err)
	}
	return resolved, nil
}
