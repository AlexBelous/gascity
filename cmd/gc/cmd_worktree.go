package main

import (
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"

	"github.com/gastownhall/gascity/internal/worktree"
	"github.com/spf13/cobra"
)

// worktreeCmdOpts carries the flag values for gc worktree subcommands.
type worktreeCmdOpts struct {
	Repo       string
	Root       string
	Path       string
	Branch     string
	Base       string
	BaseSHA    string
	BeadID     string
	StoreRef   string
	Creator    string
	Owner      string
	Generation string
	Lifecycle  string
	DryRun     bool
	JSON       bool
}

// newWorktreeCmd returns the gc worktree command group. It is the CLI face
// of internal/worktree — the transactional workspace owner (gc-r9fx) that
// sling and formula-managed workspace setup can route through instead of
// running competing ad hoc provisioning.
func newWorktreeCmd(stdout, stderr io.Writer) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "worktree",
		Short: "Ensure or verify agent workspace worktrees",
		Long: `Ensure or verify agent workspace worktrees.

gc worktree is the single transactional owner for workspace provisioning.
Postconditions: the path is a direct child of the configured per-rig root and
the root of a worktree of the given repository, with the bead's uniquely named
branch checked out on an attached HEAD (never detached). Durable provenance is
stored in the worktree's private git directory and returned as JSON so callers
can atomically publish the same evidence on the bead. A new branch is created
from --base, resolved verbatim against the local repository. Failed creation
rolls back everything it created; --dry-run plans without mutating anything.`,
	}
	cmd.AddCommand(newWorktreeEnsureCmd(stdout, stderr))
	cmd.AddCommand(newWorktreeVerifyCmd(stdout, stderr))
	return cmd
}

func worktreeFlagSet(cmd *cobra.Command, opts *worktreeCmdOpts) {
	cmd.Flags().StringVar(&opts.Repo, "repo", "", "repository directory the worktree belongs to (required)")
	cmd.Flags().StringVar(&opts.Root, "root", "", "configured per-rig worktree root; path must be its direct child (required)")
	cmd.Flags().StringVar(&opts.Path, "path", "", "worktree path (required)")
	cmd.Flags().StringVar(&opts.Branch, "branch", "", "branch that must be checked out (required)")
	cmd.Flags().StringVar(&opts.Base, "base", "", "exact base ref used for this worktree (required)")
	cmd.Flags().StringVar(&opts.BaseSHA, "base-sha", "", "recorded base SHA to verify when reusing a worktree")
	cmd.Flags().StringVar(&opts.BeadID, "bead", "", "work bead bound to this worktree (required)")
	cmd.Flags().StringVar(&opts.StoreRef, "store-ref", "", "work bead store reference (required)")
	cmd.Flags().StringVar(&opts.Creator, "creator", "", "mechanism creating the worktree (required)")
	cmd.Flags().StringVar(&opts.Owner, "owner", "", "single selected provisioning owner (required)")
	cmd.Flags().StringVar(&opts.Generation, "generation", "", "provisioning generation fence (required)")
	cmd.Flags().StringVar(&opts.Lifecycle, "lifecycle", worktree.LifecycleActive, "worktree lifecycle state")
	cmd.Flags().BoolVar(&opts.JSON, "json", false, "emit the report as JSON")
	for _, name := range []string{
		"repo", "root", "path", "branch", "base", "bead", "store-ref", "creator", "owner", "generation",
	} {
		_ = cmd.MarkFlagRequired(name) //nolint:errcheck // flags exist
	}
}

func newWorktreeEnsureCmd(stdout, stderr io.Writer) *cobra.Command {
	var opts worktreeCmdOpts
	cmd := &cobra.Command{
		Use:   "ensure",
		Short: "Ensure the worktree exists and satisfies all postconditions",
		Args:  cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			if runWorktreeEnsure(opts, stdout, stderr) != 0 {
				return errExit
			}
			return nil
		},
	}
	worktreeFlagSet(cmd, &opts)
	cmd.Flags().BoolVarP(&opts.DryRun, "dry-run", "n", false, "plan without mutating anything")
	return cmd
}

func newWorktreeVerifyCmd(stdout, stderr io.Writer) *cobra.Command {
	var opts worktreeCmdOpts
	cmd := &cobra.Command{
		Use:   "verify",
		Short: "Verify the worktree satisfies all postconditions without mutating",
		Args:  cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			if runWorktreeVerify(opts, stdout, stderr) != 0 {
				return errExit
			}
			return nil
		},
	}
	worktreeFlagSet(cmd, &opts)
	return cmd
}

func (o worktreeCmdOpts) spec() (worktree.Spec, error) {
	repo, err := filepath.Abs(o.Repo)
	if err != nil {
		return worktree.Spec{}, fmt.Errorf("resolving --repo %q: %w", o.Repo, err)
	}
	path, err := filepath.Abs(o.Path)
	if err != nil {
		return worktree.Spec{}, fmt.Errorf("resolving --path %q: %w", o.Path, err)
	}
	root, err := filepath.Abs(o.Root)
	if err != nil {
		return worktree.Spec{}, fmt.Errorf("resolving --root %q: %w", o.Root, err)
	}
	return worktree.Spec{
		RepoDir:    repo,
		Root:       root,
		Path:       path,
		Branch:     o.Branch,
		Base:       o.Base,
		BaseSHA:    o.BaseSHA,
		BeadID:     o.BeadID,
		StoreRef:   o.StoreRef,
		Creator:    o.Creator,
		Owner:      o.Owner,
		Generation: o.Generation,
		Lifecycle:  o.Lifecycle,
		DryRun:     o.DryRun,
	}, nil
}

func runWorktreeEnsure(opts worktreeCmdOpts, stdout, stderr io.Writer) int {
	spec, err := opts.spec()
	if err != nil {
		fmt.Fprintf(stderr, "gc worktree ensure: %v\n", err) //nolint:errcheck
		return 1
	}
	rep, err := worktree.Ensure(spec)
	if err != nil {
		fmt.Fprintf(stderr, "gc worktree ensure: %v\n", err) //nolint:errcheck
		return 1
	}
	return writeWorktreeReport(rep, opts, stdout, stderr)
}

func runWorktreeVerify(opts worktreeCmdOpts, stdout, stderr io.Writer) int {
	spec, err := opts.spec()
	if err != nil {
		fmt.Fprintf(stderr, "gc worktree verify: %v\n", err) //nolint:errcheck
		return 1
	}
	rep, err := worktree.Verify(spec)
	if err != nil {
		fmt.Fprintf(stderr, "gc worktree verify: %v\n", err) //nolint:errcheck
		return 1
	}
	return writeWorktreeReport(rep, opts, stdout, stderr)
}

func writeWorktreeReport(rep worktree.Report, opts worktreeCmdOpts, stdout, stderr io.Writer) int {
	if opts.JSON {
		if err := json.NewEncoder(stdout).Encode(rep); err != nil {
			fmt.Fprintf(stderr, "gc worktree: encoding report: %v\n", err) //nolint:errcheck
			return 1
		}
		return 0
	}
	switch {
	case len(rep.Planned) > 0:
		fmt.Fprintf(stdout, "would run (dry-run):\n") //nolint:errcheck
		for _, action := range rep.Planned {
			fmt.Fprintf(stdout, "  %s\n", action) //nolint:errcheck
		}
	case rep.Created:
		fmt.Fprintf(stdout, "created worktree %s on branch %s at %s\n", rep.Path, rep.Branch, rep.Head) //nolint:errcheck
	default:
		fmt.Fprintf(stdout, "worktree %s on branch %s at %s\n", rep.Path, rep.Branch, rep.Head) //nolint:errcheck
	}
	return 0
}
