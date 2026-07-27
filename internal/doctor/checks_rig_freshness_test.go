package doctor

import (
	"errors"
	"strings"
	"testing"

	"github.com/gastownhall/gascity/internal/config"
	"github.com/gastownhall/gascity/internal/git"
)

// withFreshness returns a check whose git probe is stubbed to the given result.
func withFreshness(rig config.Rig, f git.Freshness, err error) *RigFreshnessCheck {
	c := NewRigFreshnessCheck(rig)
	c.freshness = func(_, remote string) (git.Freshness, error) {
		f.Remote = remote
		return f, err
	}
	return c
}

func TestRigFreshnessCheck_Name(t *testing.T) {
	c := NewRigFreshnessCheck(config.Rig{Name: "gascity"})
	if got, want := c.Name(), "rig:gascity:freshness"; got != want {
		t.Fatalf("Name() = %q, want %q", got, want)
	}
}

func TestRigFreshnessCheck_WarmupEligibleAndNoFix(t *testing.T) {
	c := NewRigFreshnessCheck(config.Rig{Name: "gascity"})
	if !c.WarmupEligible() {
		t.Error("WarmupEligible() = false, want true")
	}
	if c.CanFix() {
		t.Error("CanFix() = true, want false (never auto-mutate a worktree)")
	}
}

func TestRigFreshnessCheck_Run(t *testing.T) {
	rig := config.Rig{Name: "gascity", Path: "/does/not/matter"}

	tests := []struct {
		name        string
		fresh       git.Freshness
		wantStatus  CheckStatus
		wantInMsg   string
		wantFixHint bool
		fixHintHas  string
		fixHintLack []string
	}{
		{
			name:       "current is OK with no fix hint",
			fresh:      git.Freshness{State: git.FreshnessCurrent, Branch: "main", Upstream: "origin/main"},
			wantStatus: StatusOK,
			wantInMsg:  "up to date",
		},
		{
			name:        "behind warns and replaces push with pull",
			fresh:       git.Freshness{State: git.FreshnessBehind, Branch: "main", Upstream: "origin/main", Behind: 2139},
			wantStatus:  StatusWarning,
			wantInMsg:   "behind",
			wantFixHint: true,
			fixHintHas:  "pull --rebase",
		},
		{
			name:        "diverged warns and warns against force",
			fresh:       git.Freshness{State: git.FreshnessDiverged, Branch: "main", Upstream: "origin/main", Ahead: 1, Behind: 3},
			wantStatus:  StatusWarning,
			wantInMsg:   "diverged",
			wantFixHint: true,
			fixHintHas:  "reconcile",
		},
		{
			name:        "detached warns",
			fresh:       git.Freshness{State: git.FreshnessDetached, Head: "abc1234"},
			wantStatus:  StatusWarning,
			wantInMsg:   "detached",
			wantFixHint: true,
			fixHintHas:  "check out a branch",
		},
		{
			name:        "no-remote is OK and omits push/pull commands",
			fresh:       git.Freshness{State: git.FreshnessNoRemote},
			wantStatus:  StatusOK,
			wantInMsg:   "no \"origin\" remote configured",
			fixHintLack: []string{"git push", "git pull"},
		},
		{
			name:       "no-upstream is OK",
			fresh:      git.Freshness{State: git.FreshnessNoUpstream, Branch: "main"},
			wantStatus: StatusOK,
			wantInMsg:  "no upstream",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := withFreshness(rig, tt.fresh, nil)
			r := c.Run(&CheckContext{})

			if r.Status != tt.wantStatus {
				t.Errorf("Status = %d, want %d (msg=%q)", r.Status, tt.wantStatus, r.Message)
			}
			if r.Severity != SeverityAdvisory {
				t.Errorf("Severity = %d, want SeverityAdvisory", r.Severity)
			}
			if !strings.Contains(r.Message, tt.wantInMsg) {
				t.Errorf("Message = %q, want to contain %q", r.Message, tt.wantInMsg)
			}
			if tt.wantFixHint && r.FixHint == "" {
				t.Errorf("FixHint empty, want guidance")
			}
			if !tt.wantFixHint && r.FixHint != "" {
				t.Errorf("FixHint = %q, want empty for a non-warning state", r.FixHint)
			}
			if tt.fixHintHas != "" && !strings.Contains(r.FixHint, tt.fixHintHas) {
				t.Errorf("FixHint = %q, want to contain %q", r.FixHint, tt.fixHintHas)
			}
			// no-remote must never surface a push/pull command anywhere.
			combined := r.FixHint + "\n" + strings.Join(r.Details, "\n")
			for _, banned := range tt.fixHintLack {
				if strings.Contains(combined, banned) {
					t.Errorf("output %q must omit %q when no remote exists", combined, banned)
				}
			}
		})
	}
}

// A probe that cannot determine freshness (path is not a git repository, or
// git is unavailable) is OK-informational, not a warning: the per-rig git
// check and the git-binary check already own those signals, so warmup must not
// double-warn. Regression guard for TestE2c1ProviderConstructionFailuresReturnThroughRun,
// whose fixture rig is a plain directory.
func TestRigFreshnessCheck_ProbeErrorIsInformationalNotWarning(t *testing.T) {
	c := withFreshness(config.Rig{Name: "gascity"}, git.Freshness{}, errors.New("not a git repo"))
	r := c.Run(&CheckContext{})
	if r.Status != StatusOK {
		t.Fatalf("Status = %d, want StatusOK on an indeterminate probe (no double-warning)", r.Status)
	}
	if r.Severity != SeverityAdvisory {
		t.Errorf("Severity = %d, want SeverityAdvisory", r.Severity)
	}
	if len(r.Details) == 0 || !strings.Contains(strings.Join(r.Details, " "), "not a git repo") {
		t.Errorf("Details = %v, want the probe error surfaced", r.Details)
	}
}
