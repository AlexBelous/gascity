package doctor

import (
	"fmt"
	"strings"

	"github.com/gastownhall/gascity/internal/config"
	"github.com/gastownhall/gascity/internal/git"
)

// defaultPushRemote is the remote freshness diagnostics compare against and
// generate push/pull guidance for.
const defaultPushRemote = "origin"

// RigFreshnessCheck reports whether a rig's working tree is stale relative to
// its upstream (behind or diverged) or in a ref state that makes push guidance
// unreliable (detached HEAD, or no push remote configured). The diagnosis is
// read-only: it never fetches, checks out, resets, pulls, or pushes. Because a
// stale checkout serves outdated instructions rather than corrupting state, a
// failing result is SeverityAdvisory; the check is WarmupEligible so `gc start`
// surfaces staleness at session start alongside `gc doctor`.
type RigFreshnessCheck struct {
	rig config.Rig
	// freshness is injectable so tests can drive each state without building
	// a real git checkout for every case.
	freshness func(rigPath, remote string) (git.Freshness, error)
}

// NewRigFreshnessCheck creates a rig freshness check for the given rig.
func NewRigFreshnessCheck(rig config.Rig) *RigFreshnessCheck {
	return &RigFreshnessCheck{
		rig: rig,
		freshness: func(rigPath, remote string) (git.Freshness, error) {
			return git.New(rigPath).Freshness(remote)
		},
	}
}

// Name returns the check identifier ("rig:<name>:freshness").
func (c *RigFreshnessCheck) Name() string { return "rig:" + c.rig.Name + ":freshness" }

// WarmupEligible returns true so this check runs during gc start warm-up.
func (c *RigFreshnessCheck) WarmupEligible() bool { return true }

// CanFix returns false; catching a checkout up requires operator judgment and
// this check never mutates worktrees.
func (c *RigFreshnessCheck) CanFix() bool { return false }

// Fix is a no-op.
func (c *RigFreshnessCheck) Fix(_ *CheckContext) error { return nil }

// Run diagnoses the rig checkout's freshness and remote configuration.
func (c *RigFreshnessCheck) Run(_ *CheckContext) *CheckResult {
	r := &CheckResult{Name: c.Name(), Severity: SeverityAdvisory}

	f, err := c.freshness(c.rig.Path, defaultPushRemote)
	if err != nil {
		// An indeterminate probe (path is not a git repository, or git is
		// unavailable) is not a freshness problem this check should raise: the
		// per-rig `git` check and the `git-binary` check already own those
		// signals. Report OK so warmup does not double-warn on the same cause.
		r.Status = StatusOK
		r.Message = fmt.Sprintf("rig %q: freshness not determined (not a git repository or git unavailable)", c.rig.Name)
		r.Details = []string{err.Error()}
		return r
	}

	r.Message = fmt.Sprintf("rig %q %s", c.rig.Name, f.Describe())

	switch f.State {
	case git.FreshnessBehind, git.FreshnessDiverged, git.FreshnessDetached:
		// Actionable staleness / ref anomaly. The guidance is remote-aware and
		// advisory only — it is a hint, never auto-applied.
		r.Status = StatusWarning
		r.FixHint = strings.Join(f.Guidance(), "\n")
	default:
		// current, no-remote, and no-upstream are valid resting states (a
		// local-only rig legitimately has no remote). Surface the guidance as
		// verbose detail so push steps are omitted when there is no remote,
		// without raising a false alarm.
		r.Status = StatusOK
		r.Details = f.Guidance()
	}
	return r
}
