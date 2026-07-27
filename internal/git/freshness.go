package git

import (
	"fmt"
	"slices"
	"sort"
	"strings"
)

// FreshnessState classifies a checkout's position relative to its upstream
// tracking branch and remote configuration. It is a read-only diagnosis:
// computing it never fetches, checks out, resets, pulls, or pushes.
type FreshnessState string

const (
	// FreshnessCurrent means HEAD is level with, or only ahead of, its
	// upstream: no upstream commits are missing locally, so the checkout is
	// not stale.
	FreshnessCurrent FreshnessState = "current"
	// FreshnessBehind means upstream has commits HEAD lacks while HEAD has
	// none upstream lacks: a fast-forwardable, stale checkout. This is the
	// "far behind" condition (the count is reported, not thresholded).
	FreshnessBehind FreshnessState = "behind"
	// FreshnessDiverged means HEAD and upstream have each accumulated commits
	// the other lacks: reconciliation needs a rebase or merge, not a
	// fast-forward.
	FreshnessDiverged FreshnessState = "diverged"
	// FreshnessDetached means HEAD is not on a branch, so there is no upstream
	// to measure against.
	FreshnessDetached FreshnessState = "detached"
	// FreshnessNoRemote means the requested push remote is not configured, so
	// push/pull guidance cannot reference it. This is the local-only /
	// missing-origin case.
	FreshnessNoRemote FreshnessState = "no-remote"
	// FreshnessNoUpstream means HEAD is on a branch with the push remote
	// configured but no upstream tracking ref, so freshness is indeterminate
	// without choosing a comparison branch.
	FreshnessNoUpstream FreshnessState = "no-upstream"
)

// Freshness is a read-only diagnosis of a checkout's staleness and remote
// configuration. Ahead and Behind are meaningful only for the current,
// behind, and diverged states.
type Freshness struct {
	// State is the classified condition.
	State FreshnessState
	// Branch is the current branch name; empty when detached.
	Branch string
	// Head is the short HEAD revision.
	Head string
	// Remote is the push remote the diagnosis was computed for.
	Remote string
	// Upstream is the upstream tracking ref, when one is configured.
	Upstream string
	// Ahead counts commits on HEAD not on the upstream.
	Ahead int
	// Behind counts commits on the upstream not on HEAD.
	Behind int
	// Remotes lists the configured remote names, sorted.
	Remotes []string
}

// remotes returns the configured remote names, sorted and de-duplicated.
func (g *Git) remotes() ([]string, error) {
	out, err := g.run("remote")
	if err != nil {
		return nil, err
	}
	var names []string
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if name := strings.TrimSpace(line); name != "" {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return slices.Compact(names), nil
}

// Freshness diagnoses the checkout's position relative to the given push
// remote (default "origin") without mutating the worktree. It performs no
// fetch, so Behind and Diverged are measured against the locally known
// upstream ref; a caller that needs current remote state must Fetch first.
//
// The returned state names the exact condition (see the FreshnessState
// constants) so callers can render an accurate diagnostic and remote-aware
// guidance rather than assuming a checkout is current and pushable.
func (g *Git) Freshness(remote string) (Freshness, error) {
	if strings.TrimSpace(remote) == "" {
		remote = "origin"
	}
	f := Freshness{Remote: remote}

	remotes, err := g.remotes()
	if err != nil {
		return f, fmt.Errorf("listing remotes: %w", err)
	}
	f.Remotes = remotes

	branch, err := g.CurrentBranch()
	if err != nil {
		return f, err
	}
	if head, herr := g.run("rev-parse", "--short", "HEAD"); herr == nil {
		f.Head = strings.TrimSpace(head)
	}

	// A detached HEAD is not on a branch and therefore has no upstream to
	// measure, regardless of remote configuration.
	if branch == "" || branch == "HEAD" {
		f.State = FreshnessDetached
		return f, nil
	}
	f.Branch = branch

	// A checkout whose push remote is absent cannot be pushed to or compared
	// against; this is the local-only / missing-origin case that makes push
	// guidance wrong.
	if !slices.Contains(remotes, remote) {
		f.State = FreshnessNoRemote
		return f, nil
	}

	ahead, behind, err := g.AheadBehind()
	if err != nil {
		// The branch tracks no upstream ref; freshness is indeterminate even
		// though the remote exists.
		f.State = FreshnessNoUpstream
		return f, nil
	}
	f.Ahead, f.Behind = ahead, behind
	if up, uerr := g.run("rev-parse", "--abbrev-ref", "--symbolic-full-name", "@{upstream}"); uerr == nil {
		f.Upstream = strings.TrimSpace(up)
	}

	switch {
	case behind > 0 && ahead > 0:
		f.State = FreshnessDiverged
	case behind > 0:
		f.State = FreshnessBehind
	default:
		f.State = FreshnessCurrent
	}
	return f, nil
}

// upstreamLabel returns a stable name for the comparison ref, falling back to
// "<remote>" when the upstream ref name is unknown.
func (f Freshness) upstreamLabel() string {
	if f.Upstream != "" {
		return f.Upstream
	}
	return f.Remote
}

// Describe returns a one-line, human-readable summary that names the exact
// stale/ref condition without prescribing any action.
func (f Freshness) Describe() string {
	switch f.State {
	case FreshnessCurrent:
		if f.Ahead > 0 {
			return fmt.Sprintf("up to date with %s (%d ahead)", f.upstreamLabel(), f.Ahead)
		}
		return fmt.Sprintf("up to date with %s", f.upstreamLabel())
	case FreshnessBehind:
		return fmt.Sprintf("%d commit(s) behind %s — checkout is stale", f.Behind, f.upstreamLabel())
	case FreshnessDiverged:
		return fmt.Sprintf("diverged from %s (%d ahead, %d behind)", f.upstreamLabel(), f.Ahead, f.Behind)
	case FreshnessDetached:
		if f.Head != "" {
			return fmt.Sprintf("detached HEAD at %s (not on a branch)", f.Head)
		}
		return "detached HEAD (not on a branch)"
	case FreshnessNoRemote:
		return fmt.Sprintf("no %q remote configured", f.Remote)
	case FreshnessNoUpstream:
		return fmt.Sprintf("branch %s has no upstream tracking ref", f.Branch)
	default:
		return string(f.State)
	}
}

// Guidance returns remote-aware next-step guidance for the checkout's state.
// When no push remote is configured it deliberately omits push and pull
// commands rather than emitting steps that cannot succeed; a current checkout
// needs no guidance and returns nil. Guidance never mutates the worktree and
// never prescribes an automatic checkout, reset, pull, or push — the returned
// lines are advisory for a human or a higher layer to act on.
func (f Freshness) Guidance() []string {
	switch f.State {
	case FreshnessNoRemote:
		return []string{
			fmt.Sprintf("no %q remote is configured — work stays local; there is nothing to push to", f.Remote),
		}
	case FreshnessDetached:
		return []string{
			"detached HEAD — check out a branch before committing or pushing",
		}
	case FreshnessNoUpstream:
		return []string{
			fmt.Sprintf("branch %s has no upstream — `git branch --set-upstream-to=%s/%s` before relying on push", f.Branch, f.Remote, f.Branch),
		}
	case FreshnessBehind:
		return []string{
			fmt.Sprintf("catch up before working: git pull --rebase %s %s (review first; not applied automatically)", f.Remote, f.Branch),
		}
	case FreshnessDiverged:
		return []string{
			fmt.Sprintf("diverged from %s — reconcile (rebase or merge) before pushing; do not force", f.upstreamLabel()),
		}
	default: // FreshnessCurrent
		return nil
	}
}
