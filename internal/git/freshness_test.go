package git

import (
	"strings"
	"testing"
)

// gitOut runs a git command in dir and returns trimmed stdout, failing the
// test on error. It reuses the package's own sanitized-env runner rather than
// spawning its own exec.Command, so it adds no new subprocess call site to the
// resource-census ledger (test/test-resources.toml).
func gitOut(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := New(dir).run(args...)
	if err != nil {
		t.Fatalf("git %s: %v", strings.Join(args, " "), err)
	}
	return strings.TrimSpace(out)
}

// newOriginAndClone creates a bare origin on main with one commit and a work
// clone whose main branch tracks origin/main. Both sit at the same commit, so
// a fresh clone diagnoses as current.
func newOriginAndClone(t *testing.T) (origin, work string) {
	t.Helper()
	origin = t.TempDir()
	runGit(t, origin, "init", "--bare", "-b", "main")
	work = t.TempDir()
	runGit(t, work, "clone", origin, ".")
	runGit(t, work, "config", "user.email", "test@test.com")
	runGit(t, work, "config", "user.name", "Test")
	runGit(t, work, "commit", "--allow-empty", "-m", "c1")
	runGit(t, work, "push", "-u", "origin", "main")
	return origin, work
}

// advanceOrigin pushes one new commit to origin/main from a throwaway clone,
// leaving other checkouts of origin behind until they fetch.
func advanceOrigin(t *testing.T, origin, msg string) {
	t.Helper()
	tmp := t.TempDir()
	runGit(t, tmp, "clone", origin, ".")
	runGit(t, tmp, "config", "user.email", "test@test.com")
	runGit(t, tmp, "config", "user.name", "Test")
	runGit(t, tmp, "commit", "--allow-empty", "-m", msg)
	runGit(t, tmp, "push", "origin", "main")
}

func TestFreshness_ClassifiesEveryState(t *testing.T) {
	tests := []struct {
		name       string
		setup      func(t *testing.T) string
		wantState  FreshnessState
		wantAhead  int
		wantBehind int
	}{
		{
			name: "current",
			setup: func(t *testing.T) string {
				_, work := newOriginAndClone(t)
				return work
			},
			wantState: FreshnessCurrent,
		},
		{
			name: "current with local ahead",
			setup: func(t *testing.T) string {
				_, work := newOriginAndClone(t)
				runGit(t, work, "commit", "--allow-empty", "-m", "local")
				return work
			},
			wantState: FreshnessCurrent,
			wantAhead: 1,
		},
		{
			name: "behind (far behind)",
			setup: func(t *testing.T) string {
				origin, work := newOriginAndClone(t)
				advanceOrigin(t, origin, "c2")
				advanceOrigin(t, origin, "c3")
				// Fetch only updates the remote-tracking ref; HEAD stays put.
				runGit(t, work, "fetch", "origin")
				return work
			},
			wantState:  FreshnessBehind,
			wantBehind: 2,
		},
		{
			name: "diverged",
			setup: func(t *testing.T) string {
				origin, work := newOriginAndClone(t)
				runGit(t, work, "commit", "--allow-empty", "-m", "local")
				advanceOrigin(t, origin, "c2")
				runGit(t, work, "fetch", "origin")
				return work
			},
			wantState:  FreshnessDiverged,
			wantAhead:  1,
			wantBehind: 1,
		},
		{
			name: "detached",
			setup: func(t *testing.T) string {
				_, work := newOriginAndClone(t)
				runGit(t, work, "checkout", "--detach", "HEAD")
				return work
			},
			wantState: FreshnessDetached,
		},
		{
			name:      "no-remote",
			setup:     initTestRepo,
			wantState: FreshnessNoRemote,
		},
		{
			name: "no-upstream (remote configured, branch untracked)",
			setup: func(t *testing.T) string {
				origin := t.TempDir()
				runGit(t, origin, "init", "--bare", "-b", "main")
				dir := t.TempDir()
				runGit(t, dir, "init", "-b", "main")
				runGit(t, dir, "config", "user.email", "test@test.com")
				runGit(t, dir, "config", "user.name", "Test")
				runGit(t, dir, "commit", "--allow-empty", "-m", "c1")
				runGit(t, dir, "remote", "add", "origin", origin)
				return dir
			},
			wantState: FreshnessNoUpstream,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := tt.setup(t)

			headBefore := gitOut(t, dir, "rev-parse", "HEAD")
			statusBefore := gitOut(t, dir, "status", "--porcelain")

			f, err := New(dir).Freshness("origin")
			if err != nil {
				t.Fatalf("Freshness: %v", err)
			}

			if f.State != tt.wantState {
				t.Errorf("State = %q, want %q (%+v)", f.State, tt.wantState, f)
			}
			if f.Ahead != tt.wantAhead {
				t.Errorf("Ahead = %d, want %d", f.Ahead, tt.wantAhead)
			}
			if f.Behind != tt.wantBehind {
				t.Errorf("Behind = %d, want %d", f.Behind, tt.wantBehind)
			}
			if strings.TrimSpace(f.Describe()) == "" {
				t.Errorf("Describe() is empty for state %q", f.State)
			}

			// Diagnosis must never mutate the worktree.
			if got := gitOut(t, dir, "rev-parse", "HEAD"); got != headBefore {
				t.Errorf("HEAD moved: before=%s after=%s", headBefore, got)
			}
			if got := gitOut(t, dir, "status", "--porcelain"); got != statusBefore {
				t.Errorf("working tree changed: before=%q after=%q", statusBefore, got)
			}
		})
	}
}

func TestFreshness_DescribeNamesTheCondition(t *testing.T) {
	origin, work := newOriginAndClone(t)
	advanceOrigin(t, origin, "c2")
	runGit(t, work, "fetch", "origin")

	f, err := New(work).Freshness("origin")
	if err != nil {
		t.Fatalf("Freshness: %v", err)
	}
	if !strings.Contains(f.Describe(), "behind") {
		t.Errorf("Describe() = %q, want it to name the behind condition", f.Describe())
	}
}

func TestFreshness_GuidanceOmitsPushWhenNoRemote(t *testing.T) {
	dir := initTestRepo(t)

	f, err := New(dir).Freshness("origin")
	if err != nil {
		t.Fatalf("Freshness: %v", err)
	}
	if f.State != FreshnessNoRemote {
		t.Fatalf("State = %q, want %q", f.State, FreshnessNoRemote)
	}
	guidance := f.Guidance()
	if len(guidance) == 0 {
		t.Fatalf("Guidance() empty; want a note that push is skipped")
	}
	for _, line := range guidance {
		if strings.Contains(line, "git push") || strings.Contains(line, "git pull") {
			t.Errorf("no-remote guidance must omit push/pull commands, got %q", line)
		}
	}
}

func TestFreshness_GuidanceReplacesPushWithPullWhenBehind(t *testing.T) {
	origin, work := newOriginAndClone(t)
	advanceOrigin(t, origin, "c2")
	runGit(t, work, "fetch", "origin")

	f, err := New(work).Freshness("origin")
	if err != nil {
		t.Fatalf("Freshness: %v", err)
	}
	guidance := strings.Join(f.Guidance(), "\n")
	if !strings.Contains(guidance, "pull --rebase") {
		t.Errorf("behind guidance = %q, want a pull --rebase step", guidance)
	}
}

func TestFreshness_CurrentNeedsNoGuidance(t *testing.T) {
	_, work := newOriginAndClone(t)

	f, err := New(work).Freshness("origin")
	if err != nil {
		t.Fatalf("Freshness: %v", err)
	}
	if f.State != FreshnessCurrent {
		t.Fatalf("State = %q, want %q", f.State, FreshnessCurrent)
	}
	if g := f.Guidance(); g != nil {
		t.Errorf("Guidance() = %v, want nil for a current checkout", g)
	}
}
