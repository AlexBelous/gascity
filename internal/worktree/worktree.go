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
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gastownhall/gascity/internal/git"
	"github.com/gastownhall/gascity/internal/pathutil"
)

// ErrWorktreeMissing reports that the spec path does not exist at all. It is
// the only verification failure Ensure treats as creatable; every other
// failure means the path holds state Ensure must not clobber.
var ErrWorktreeMissing = errors.New("worktree path does not exist")

const (
	provenanceSchemaVersion = 1
	provenanceFilename      = "gascity-worktree-provenance.json"

	// LifecycleActive is the initial lifecycle state of a usable workspace.
	LifecycleActive = "active"
)

// Spec describes the desired workspace worktree.
type Spec struct {
	// RepoDir is the repository (main checkout or any of its worktrees)
	// the workspace must belong to. Absolute.
	RepoDir string
	// Path is where the worktree must exist. Absolute.
	Path string
	// Root is the configured per-rig worktree root. Managed worktrees must be
	// direct children of this root; callers must not derive it from their cwd.
	Root string
	// Branch must be checked out at Path with an attached HEAD.
	Branch string
	// Base is the ref a NEW branch is created from. It is resolved
	// verbatim against the local repository — "main" means local main,
	// never a remote fallback. Required only when Branch does not exist.
	Base string
	// BaseSHA is the exact base commit recorded by a prior successful Ensure.
	// On first creation it may be empty; Ensure resolves Base and returns the
	// resulting SHA for atomic publication by the caller.
	BaseSHA string
	// BeadID and StoreRef bind the worktree to its work item.
	BeadID   string
	StoreRef string
	// Creator identifies the mechanism that created the workspace; Owner is
	// the single provisioner selected for its lifecycle.
	Creator string
	Owner   string
	// Generation fences stale metadata from an earlier provisioning attempt.
	Generation string
	// Lifecycle is the durable lifecycle state, initially "active".
	Lifecycle string
	// DryRun plans without mutating anything.
	DryRun bool
}

// Provenance is the durable, worktree-specific ownership record. It lives in
// the worktree's private git directory, never in the checked-out files.
type Provenance struct {
	SchemaVersion int       `json:"schema_version"`
	BeadID        string    `json:"bead_id"`
	StoreRef      string    `json:"store_ref"`
	RepoIdentity  string    `json:"repo_identity"`
	Path          string    `json:"path"`
	Branch        string    `json:"branch"`
	BaseRef       string    `json:"base_ref"`
	BaseSHA       string    `json:"base_sha"`
	Creator       string    `json:"creator"`
	Owner         string    `json:"owner"`
	Generation    string    `json:"generation"`
	CreatedAt     time.Time `json:"created_at"`
	Lifecycle     string    `json:"lifecycle"`
	AttemptID     string    `json:"attempt_id"`
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
	// Provenance is the verified durable identity, or the planned identity in
	// dry-run output (without CreatedAt or AttemptID).
	Provenance *Provenance `json:"provenance,omitempty"`
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
	if s.managed() {
		required := []struct {
			name  string
			value string
		}{
			{"root", s.Root},
			{"base", s.Base},
			{"bead id", s.BeadID},
			{"store ref", s.StoreRef},
			{"creator", s.Creator},
			{"owner", s.Owner},
			{"generation", s.Generation},
			{"lifecycle", s.Lifecycle},
		}
		for _, field := range required {
			if strings.TrimSpace(field.value) == "" {
				return fmt.Errorf("worktree spec: %s must not be empty for a managed worktree", field.name)
			}
		}
		if !filepath.IsAbs(s.Root) {
			return fmt.Errorf("worktree spec: root %q must be absolute", s.Root)
		}
		if err := validateManagedPath(s); err != nil {
			return err
		}
		if err := rejectRegisteredWorktreeAncestor(s); err != nil {
			return err
		}
	}
	return nil
}

func (s Spec) managed() bool {
	return s.Root != "" || s.BeadID != "" || s.StoreRef != "" || s.Creator != "" ||
		s.Owner != "" || s.Generation != "" || s.Lifecycle != "" || s.BaseSHA != ""
}

// Verify checks that the spec's path is the root of a worktree of the
// spec's repository with the spec's branch checked out on an attached HEAD.
// It never mutates anything. A missing path returns ErrWorktreeMissing;
// any other failure describes the postcondition that does not hold.
func Verify(spec Spec) (Report, error) {
	if err := spec.validate(); err != nil {
		return Report{}, err
	}
	rep, err := verifyGitState(spec)
	if err != nil {
		return rep, err
	}
	if !spec.managed() {
		return rep, nil
	}
	provenance, err := readProvenance(spec.Path)
	if err != nil {
		return rep, fmt.Errorf("verifying worktree %q provenance: %w", spec.Path, err)
	}
	if err := verifyProvenance(spec, provenance); err != nil {
		return rep, fmt.Errorf("verifying worktree %q provenance: %w", spec.Path, err)
	}
	rep.Provenance = &provenance
	return rep, nil
}

func verifyGitState(spec Spec) (Report, error) {
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

	repoGit, branchExists, resolvedBase, err := resolveCreationState(spec)
	if err != nil {
		return rep, err
	}
	if spec.DryRun {
		return planDryRun(spec, rep, branchExists, resolvedBase)
	}

	if err := createWorktree(repoGit, spec, branchExists); err != nil {
		return rep, err
	}
	created, err := verifyCreatedWorktree(repoGit, spec, branchExists, resolvedBase)
	if err != nil {
		return rep, err
	}
	if spec.managed() {
		created, err = publishAndVerifyProvenance(spec, resolvedBase)
		if err != nil {
			return rep, rollbackResult(err, rollbackCreated(repoGit, spec.Path, spec.Branch, !branchExists))
		}
	}
	created.Created = true
	created.BranchCreated = !branchExists
	return created, nil
}

func resolveCreationState(spec Spec) (*git.Git, bool, string, error) {
	repoGit := git.New(spec.RepoDir)
	if !repoGit.IsRepo() {
		return nil, false, "", fmt.Errorf("ensuring worktree %q: repo dir %q is not a git repository", spec.Path, spec.RepoDir)
	}
	branchExists := repoGit.BranchExists(spec.Branch)
	if !branchExists && spec.Base == "" {
		return nil, false, "", fmt.Errorf("ensuring worktree %q: branch %q does not exist and no base was given", spec.Path, spec.Branch)
	}
	var resolvedBase string
	if spec.Base != "" {
		sha, err := repoGit.RevParseVerifyCommit(spec.Base)
		if err != nil {
			return nil, false, "", fmt.Errorf("ensuring worktree %q: %w", spec.Path, err)
		}
		resolvedBase = sha
	}
	if spec.BaseSHA != "" && resolvedBase != spec.BaseSHA {
		return nil, false, "", fmt.Errorf("ensuring worktree %q: base ref %q resolves to %s, want recorded base SHA %s",
			spec.Path, spec.Base, resolvedBase, spec.BaseSHA)
	}
	return repoGit, branchExists, resolvedBase, nil
}

func planDryRun(spec Spec, rep Report, branchExists bool, resolvedBase string) (Report, error) {
	if branchExists {
		rep.Planned = []string{fmt.Sprintf("git worktree add %s %s", spec.Path, spec.Branch)}
	} else {
		rep.Planned = []string{fmt.Sprintf("git worktree add -b %s %s %s", spec.Branch, spec.Path, spec.Base)}
	}
	if spec.managed() {
		provenance, err := plannedProvenance(spec, resolvedBase)
		if err != nil {
			return rep, err
		}
		rep.Provenance = &provenance
	}
	return rep, nil
}

func createWorktree(repoGit *git.Git, spec Spec, branchExists bool) error {
	var err error
	if branchExists {
		err = repoGit.WorktreeAddExistingBranch(spec.Path, spec.Branch)
	} else {
		// Preserve the explicit base verbatim so git's own branch-setup
		// semantics follow the operator's stated intent.
		err = repoGit.WorktreeAddNewBranch(spec.Path, spec.Branch, spec.Base)
	}
	if err != nil {
		return fmt.Errorf("ensuring worktree %q: %w", spec.Path, err)
	}
	return nil
}

func verifyCreatedWorktree(repoGit *git.Git, spec Spec, branchExists bool, resolvedBase string) (Report, error) {
	created, err := verifyGitState(Spec{RepoDir: spec.RepoDir, Path: spec.Path, Branch: spec.Branch})
	if err != nil {
		cause := fmt.Errorf("worktree %q failed post-create verification: %w", spec.Path, err)
		return Report{}, rollbackResult(cause, rollbackCreated(repoGit, spec.Path, spec.Branch, !branchExists))
	}
	if !branchExists && created.Head != resolvedBase {
		cause := fmt.Errorf("worktree %q: new branch %q is at %s, want base %q = %s",
			spec.Path, spec.Branch, created.Head, spec.Base, resolvedBase)
		return Report{}, rollbackResult(cause, rollbackCreated(repoGit, spec.Path, spec.Branch, true))
	}
	return created, nil
}

func publishAndVerifyProvenance(spec Spec, resolvedBase string) (Report, error) {
	provenance, err := plannedProvenance(spec, resolvedBase)
	if err != nil {
		return Report{}, fmt.Errorf("worktree %q provenance preparation failed: %w", spec.Path, err)
	}
	provenance.CreatedAt = time.Now().UTC()
	provenance.AttemptID, err = newAttemptID()
	if err != nil {
		return Report{}, fmt.Errorf("worktree %q provenance attempt id failed: %w", spec.Path, err)
	}
	if err := writeProvenance(spec.Path, provenance); err != nil {
		return Report{}, fmt.Errorf("worktree %q provenance publish failed: %w", spec.Path, err)
	}
	verifySpec := spec
	verifySpec.BaseSHA = provenance.BaseSHA
	verified, err := Verify(verifySpec)
	if err != nil {
		return Report{}, fmt.Errorf("worktree %q failed provenance verification: %w", spec.Path, err)
	}
	return verified, nil
}

// RollbackAttempt removes only a workspace created by the supplied Ensure
// report. It fences against stale/cross-owner reports using the durable
// attempt ID and refuses to remove a worktree that acquired WIP.
func RollbackAttempt(spec Spec, report Report) error {
	if !report.Created {
		return errors.New("rollback refused: report does not describe an attempt-created worktree")
	}
	if report.Provenance == nil || report.Provenance.AttemptID == "" {
		return errors.New("rollback refused: report has no provenance attempt id")
	}
	verified, err := Verify(spec)
	if err != nil {
		return fmt.Errorf("rollback refused: %w", err)
	}
	if verified.Provenance == nil || verified.Provenance.AttemptID != report.Provenance.AttemptID {
		return fmt.Errorf("rollback refused: provenance attempt %q does not match report attempt %q",
			provenanceAttempt(verified.Provenance), report.Provenance.AttemptID)
	}
	status, err := git.New(spec.Path).StatusPorcelain()
	if err != nil {
		return fmt.Errorf("rollback refused: checking worktree status: %w", err)
	}
	if strings.TrimSpace(status) != "" {
		return errors.New("rollback refused: worktree contains changes")
	}
	repoGit := git.New(spec.RepoDir)
	if err := repoGit.WorktreeRemove(spec.Path, false); err != nil {
		return fmt.Errorf("rolling back worktree: %w", err)
	}
	if err := repoGit.WorktreePrune(); err != nil {
		return fmt.Errorf("pruning rolled-back worktree: %w", err)
	}
	if report.BranchCreated {
		if err := repoGit.BranchDelete(spec.Branch); err != nil {
			return fmt.Errorf("deleting attempt-created branch %q: %w", spec.Branch, err)
		}
	}
	return nil
}

// rollbackCreated undoes only artifacts created by the current Ensure call.
// Removal is never forced: if the new worktree acquired WIP, rollback retains
// it and reports the cleanup failure.
func rollbackCreated(repoGit *git.Git, path, branch string, branchCreated bool) error {
	var errs []error
	if err := repoGit.WorktreeRemove(path, false); err != nil {
		errs = append(errs, err)
	}
	if err := repoGit.WorktreePrune(); err != nil {
		errs = append(errs, err)
	}
	if branchCreated {
		if err := repoGit.BranchDelete(branch); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func rollbackResult(cause, rollbackErr error) error {
	if rollbackErr != nil {
		return fmt.Errorf("%w; rollback incomplete: %w", cause, rollbackErr)
	}
	return fmt.Errorf("%w (rolled back)", cause)
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

func validateManagedPath(spec Spec) error {
	root, err := canonicalPathAllowMissing(spec.Root)
	if err != nil {
		return fmt.Errorf("worktree spec: canonicalizing root %q: %w", spec.Root, err)
	}
	path, err := canonicalPathAllowMissing(spec.Path)
	if err != nil {
		return fmt.Errorf("worktree spec: canonicalizing path %q: %w", spec.Path, err)
	}
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return fmt.Errorf("worktree spec: comparing path %q to root %q: %w", path, root, err)
	}
	if rel == "." || rel == ".." || filepath.IsAbs(rel) || strings.HasPrefix(rel, ".."+string(filepath.Separator)) ||
		strings.Contains(rel, string(filepath.Separator)) {
		return fmt.Errorf("worktree spec: path %q must be a direct child of configured root %q", path, root)
	}
	return nil
}

func rejectRegisteredWorktreeAncestor(spec Spec) error {
	worktrees, err := git.New(spec.RepoDir).WorktreeList()
	if err != nil {
		return fmt.Errorf("worktree spec: listing registered worktrees: %w", err)
	}
	candidate, err := canonicalPathAllowMissing(spec.Path)
	if err != nil {
		return fmt.Errorf("worktree spec: canonicalizing candidate path: %w", err)
	}
	for _, existing := range worktrees {
		existingPath, pathErr := canonicalPathAllowMissing(existing.Path)
		if pathErr != nil {
			continue
		}
		if pathutil.SamePath(existingPath, candidate) {
			continue
		}
		rel, relErr := filepath.Rel(existingPath, candidate)
		if relErr == nil && rel != "." && rel != ".." && !filepath.IsAbs(rel) &&
			!strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return fmt.Errorf("worktree spec: path %q is inside registered worktree %q", candidate, existingPath)
		}
	}
	return nil
}

func canonicalPathAllowMissing(path string) (string, error) {
	path = filepath.Clean(path)
	current := path
	var suffix []string
	for {
		resolved, err := filepath.EvalSymlinks(current)
		if err == nil {
			for i := len(suffix) - 1; i >= 0; i-- {
				resolved = filepath.Join(resolved, suffix[i])
			}
			return filepath.Clean(resolved), nil
		}
		if !os.IsNotExist(err) {
			return "", err
		}
		parent := filepath.Dir(current)
		if parent == current {
			return "", err
		}
		suffix = append(suffix, filepath.Base(current))
		current = parent
	}
}

func plannedProvenance(spec Spec, resolvedBase string) (Provenance, error) {
	repoIdentity, err := canonicalCommonDir(git.New(spec.RepoDir))
	if err != nil {
		return Provenance{}, fmt.Errorf("resolving repository identity: %w", err)
	}
	path, err := canonicalPathAllowMissing(spec.Path)
	if err != nil {
		return Provenance{}, fmt.Errorf("canonicalizing worktree path: %w", err)
	}
	if spec.BaseSHA != "" {
		resolvedBase = spec.BaseSHA
	}
	return Provenance{
		SchemaVersion: provenanceSchemaVersion,
		BeadID:        strings.TrimSpace(spec.BeadID),
		StoreRef:      strings.TrimSpace(spec.StoreRef),
		RepoIdentity:  repoIdentity,
		Path:          path,
		Branch:        strings.TrimSpace(spec.Branch),
		BaseRef:       strings.TrimSpace(spec.Base),
		BaseSHA:       strings.TrimSpace(resolvedBase),
		Creator:       strings.TrimSpace(spec.Creator),
		Owner:         strings.TrimSpace(spec.Owner),
		Generation:    strings.TrimSpace(spec.Generation),
		Lifecycle:     strings.TrimSpace(spec.Lifecycle),
	}, nil
}

func verifyProvenance(spec Spec, got Provenance) error {
	want, err := plannedProvenance(spec, got.BaseSHA)
	if err != nil {
		return err
	}
	if got.SchemaVersion != provenanceSchemaVersion {
		return fmt.Errorf("schema version is %d, want %d", got.SchemaVersion, provenanceSchemaVersion)
	}
	checks := []struct {
		name string
		got  string
		want string
	}{
		{"bead id", got.BeadID, want.BeadID},
		{"store ref", got.StoreRef, want.StoreRef},
		{"repository identity", got.RepoIdentity, want.RepoIdentity},
		{"path", got.Path, want.Path},
		{"branch", got.Branch, want.Branch},
		{"base ref", got.BaseRef, want.BaseRef},
		{"creator", got.Creator, want.Creator},
		{"owner", got.Owner, want.Owner},
		{"generation", got.Generation, want.Generation},
		{"lifecycle", got.Lifecycle, want.Lifecycle},
	}
	for _, check := range checks {
		if check.got != check.want {
			return fmt.Errorf("%s is %q, want %q", check.name, check.got, check.want)
		}
	}
	if spec.BaseSHA != "" && got.BaseSHA != spec.BaseSHA {
		return fmt.Errorf("base SHA is %q, want %q", got.BaseSHA, spec.BaseSHA)
	}
	if got.BaseSHA == "" {
		return errors.New("base SHA is empty")
	}
	if got.CreatedAt.IsZero() {
		return errors.New("creation time is empty")
	}
	if got.AttemptID == "" {
		return errors.New("attempt id is empty")
	}
	return nil
}

func provenanceFilePath(worktreePath string) (string, error) {
	gitDir, err := git.New(worktreePath).GitDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(gitDir, provenanceFilename), nil
}

func readProvenance(worktreePath string) (Provenance, error) {
	path, err := provenanceFilePath(worktreePath)
	if err != nil {
		return Provenance{}, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return Provenance{}, fmt.Errorf("reading %q: %w", path, err)
	}
	var provenance Provenance
	if err := json.Unmarshal(data, &provenance); err != nil {
		return Provenance{}, fmt.Errorf("decoding %q: %w", path, err)
	}
	return provenance, nil
}

func writeProvenance(worktreePath string, provenance Provenance) error {
	path, err := provenanceFilePath(worktreePath)
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(provenance, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	temp, err := os.CreateTemp(filepath.Dir(path), "."+provenanceFilename+".*")
	if err != nil {
		return err
	}
	tempPath := temp.Name()
	defer func() { _ = os.Remove(tempPath) }()
	if err := temp.Chmod(0o600); err != nil {
		_ = temp.Close()
		return err
	}
	if _, err := temp.Write(data); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Sync(); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	return os.Rename(tempPath, path)
}

func newAttemptID() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw[:]), nil
}

func provenanceAttempt(provenance *Provenance) string {
	if provenance == nil {
		return ""
	}
	return provenance.AttemptID
}
