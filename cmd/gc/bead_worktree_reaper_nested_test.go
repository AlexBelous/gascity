package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/config"
	"github.com/gastownhall/gascity/internal/doctor"
	"github.com/gastownhall/gascity/internal/events"
	"github.com/gastownhall/gascity/internal/git"
	"github.com/gastownhall/gascity/internal/pathutil"
)

// makeNestedReapHome creates cityPath/.gc/worktrees/gascity/builder/ with a
// stub .git file so doctor.DiscoverNestedWorktrees's isGitWorktreePath scan
// finds it as an agent home. Mirrors internal/doctor's makeAgentHome. The
// returned path is canonicalized via pathutil.NormalizePathForCompare to
// match what DiscoverNestedWorktrees stores internally. The rig/agent
// segments are always "gascity"/"builder" — every test in this file matches
// them against a rigBeadStores map keyed the same way, so neither is a
// parameter.
func makeNestedReapHome(t *testing.T, cityPath string) string {
	t.Helper()
	home := filepath.Join(cityPath, ".gc", "worktrees", "gascity", "builder")
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".git"), []byte("gitdir: /tmp/none\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return pathutil.NormalizePathForCompare(home)
}

func TestReapNestedClosedBeadWorktrees_NoFindings(t *testing.T) {
	cityPath := t.TempDir()
	stubNoNestedWorktrees(t)

	store := beads.NewMemStoreFrom(1, nil, nil)
	rec := &capturingRecorder{}

	reaped := reapNestedClosedBeadWorktrees(cityPath, gaConfig(), map[string]beads.Store{"gascity": store}, rec, nil)

	if reaped != 0 {
		t.Errorf("reaped = %d, want 0", reaped)
	}
	if len(rec.events) != 0 {
		t.Errorf("events = %+v, want none", rec.events)
	}
}

func TestReapNestedClosedBeadWorktrees_RemovesSafeClosedNested(t *testing.T) {
	cityPath := t.TempDir()
	home := makeNestedReapHome(t, cityPath)
	nested := filepath.Join(home, "ga-abc123")

	withNestedGuardGit(t, func(path string) doctor.GitWorktree {
		return &fakeNestedGuardGit{
			currentPath: path,
			listResp:    []git.Worktree{{Path: nested, Branch: "nested-branch"}},
		}
	})

	store := beads.NewMemStoreFrom(1, []beads.Bead{
		{ID: "ga-abc123", Title: "test bead", Status: "closed", Type: "task"},
	}, nil)
	rec := &capturingRecorder{}

	reaped := reapNestedClosedBeadWorktrees(cityPath, gaConfig(), map[string]beads.Store{"gascity": store}, rec, nil)

	if reaped != 1 {
		t.Fatalf("reaped = %d, want 1", reaped)
	}
	var reapedEvents []events.Event
	for _, e := range rec.events {
		if e.Type == events.BeadWorktreeReaped {
			reapedEvents = append(reapedEvents, e)
		}
	}
	if len(reapedEvents) != 1 {
		t.Fatalf("got %d BeadWorktreeReaped events, want 1 (all events: %+v)", len(reapedEvents), rec.events)
	}
	if reapedEvents[0].Subject != "ga-abc123" {
		t.Errorf("Subject = %q, want %q", reapedEvents[0].Subject, "ga-abc123")
	}
}

func TestReapNestedClosedBeadWorktrees_SkipsUnsafeClosedNested(t *testing.T) {
	cityPath := t.TempDir()
	home := makeNestedReapHome(t, cityPath)
	nested := filepath.Join(home, "ga-abc123")

	withNestedGuardGit(t, func(path string) doctor.GitWorktree {
		return &fakeNestedGuardGit{
			currentPath: path,
			listResp:    []git.Worktree{{Path: nested, Branch: "nested-branch"}},
			uncommitted: map[string]bool{nested: true},
		}
	})

	store := beads.NewMemStoreFrom(1, []beads.Bead{
		{ID: "ga-abc123", Title: "test bead", Status: "closed", Type: "task"},
	}, nil)
	rec := &capturingRecorder{}

	reaped := reapNestedClosedBeadWorktrees(cityPath, gaConfig(), map[string]beads.Store{"gascity": store}, rec, nil)

	if reaped != 0 {
		t.Errorf("reaped = %d, want 0", reaped)
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
		t.Errorf("Subject = %q, want %q", skipped[0].Subject, "ga-abc123")
	}
}

func TestReapNestedClosedBeadWorktrees_SkipsOpenBead(t *testing.T) {
	cityPath := t.TempDir()
	home := makeNestedReapHome(t, cityPath)
	nested := filepath.Join(home, "ga-abc123")

	withNestedGuardGit(t, func(path string) doctor.GitWorktree {
		return &fakeNestedGuardGit{
			currentPath: path,
			listResp:    []git.Worktree{{Path: nested, Branch: "nested-branch"}},
		}
	})

	store := beads.NewMemStoreFrom(1, []beads.Bead{
		{ID: "ga-abc123", Title: "test bead", Status: "open", Type: "task"},
	}, nil)
	rec := &capturingRecorder{}

	reaped := reapNestedClosedBeadWorktrees(cityPath, gaConfig(), map[string]beads.Store{"gascity": store}, rec, nil)

	if reaped != 0 {
		t.Errorf("reaped = %d, want 0", reaped)
	}
	if len(rec.events) != 0 {
		t.Errorf("events = %+v, want none (open bead skip is silent)", rec.events)
	}
}

func TestReapNestedClosedBeadWorktrees_SkipsUnknownBead(t *testing.T) {
	cityPath := t.TempDir()
	home := makeNestedReapHome(t, cityPath)
	nested := filepath.Join(home, "ga-abc123")

	withNestedGuardGit(t, func(path string) doctor.GitWorktree {
		return &fakeNestedGuardGit{
			currentPath: path,
			listResp:    []git.Worktree{{Path: nested, Branch: "nested-branch"}},
		}
	})

	store := beads.NewMemStoreFrom(1, nil, nil)
	rec := &capturingRecorder{}

	reaped := reapNestedClosedBeadWorktrees(cityPath, gaConfig(), map[string]beads.Store{"gascity": store}, rec, nil)

	if reaped != 0 {
		t.Errorf("reaped = %d, want 0", reaped)
	}
	if len(rec.events) != 0 {
		t.Errorf("events = %+v, want none (unknown bead skip is silent)", rec.events)
	}
}

func TestReapNestedClosedBeadWorktrees_SkipsNoBeadIDMatch(t *testing.T) {
	cityPath := t.TempDir()
	home := makeNestedReapHome(t, cityPath)
	nested := filepath.Join(home, "scratch-dir")

	withNestedGuardGit(t, func(path string) doctor.GitWorktree {
		return &fakeNestedGuardGit{
			currentPath: path,
			listResp:    []git.Worktree{{Path: nested, Branch: "nested-branch"}},
		}
	})

	store := beads.NewMemStoreFrom(1, nil, nil)
	rec := &capturingRecorder{}

	reaped := reapNestedClosedBeadWorktrees(cityPath, gaConfig(), map[string]beads.Store{"gascity": store}, rec, nil)

	if reaped != 0 {
		t.Errorf("reaped = %d, want 0", reaped)
	}
	if len(rec.events) != 0 {
		t.Errorf("events = %+v, want none (no bead-ID match skip is silent)", rec.events)
	}
}

// TestReapNestedClosedBeadWorktrees_SkipsSessionHomeName pins the session
// home guard as distinct from the bead-ID-match gate: the nested worktree's
// directory name is deliberately bead-ID-shaped and matches a closed, safe
// bead, so without the guard this candidate would be reaped. Registering
// that same name as an agent's BindingQualifiedName must still block it.
func TestReapNestedClosedBeadWorktrees_SkipsSessionHomeName(t *testing.T) {
	cityPath := t.TempDir()
	home := makeNestedReapHome(t, cityPath)
	nested := filepath.Join(home, "ga-abc123")

	withNestedGuardGit(t, func(path string) doctor.GitWorktree {
		return &fakeNestedGuardGit{
			currentPath: path,
			listResp:    []git.Worktree{{Path: nested, Branch: "nested-branch"}},
		}
	})

	store := beads.NewMemStoreFrom(1, []beads.Bead{
		{ID: "ga-abc123", Title: "test bead", Status: "closed", Type: "task"},
	}, nil)
	cfg := gaConfig()
	cfg.Agents = []config.Agent{{Name: "ga-abc123"}}
	rec := &capturingRecorder{}

	reaped := reapNestedClosedBeadWorktrees(cityPath, cfg, map[string]beads.Store{"gascity": store}, rec, nil)

	if reaped != 0 {
		t.Errorf("reaped = %d, want 0", reaped)
	}
	if len(rec.events) != 0 {
		t.Errorf("events = %+v, want none (session-home skip is silent)", rec.events)
	}
}

// TestReapNestedClosedBeadWorktrees_BlocksOnDeeperNesting is the key
// collateral-damage regression test: a first-level nested worktree (n1) is
// mechanically clean and belongs to a closed bead, so it looks completely
// safe to remove — except a second-level worktree (n2) is nested inside it.
// n1 must be blocked, not removed, or n2's content would be destroyed as
// collateral damage of removing n1.
func TestReapNestedClosedBeadWorktrees_BlocksOnDeeperNesting(t *testing.T) {
	cityPath := t.TempDir()
	home := makeNestedReapHome(t, cityPath)
	n1 := filepath.Join(home, "ga-abc123")
	n2 := filepath.Join(n1, "child-worktree")

	withNestedGuardGit(t, func(path string) doctor.GitWorktree {
		return &fakeNestedGuardGit{
			currentPath: path,
			listResp: []git.Worktree{
				{Path: n1, Branch: "n1-branch"},
				{Path: n2, Branch: "n2-branch"},
			},
		}
	})

	store := beads.NewMemStoreFrom(1, []beads.Bead{
		{ID: "ga-abc123", Title: "test bead", Status: "closed", Type: "task"},
	}, nil)
	rec := &capturingRecorder{}

	reaped := reapNestedClosedBeadWorktrees(cityPath, gaConfig(), map[string]beads.Store{"gascity": store}, rec, nil)

	if reaped != 0 {
		t.Errorf("reaped = %d, want 0 (n1 must be blocked by n2 nested inside it)", reaped)
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
		t.Errorf("Subject = %q, want %q", skipped[0].Subject, "ga-abc123")
	}
}

// fakeStaleNestedGit simulates a nested worktree whose HasUncommittedWork
// answer changes between the call that classifies it during
// doctor.DiscoverNestedWorktrees's discovery pass and a later call that
// revalidates it immediately before removal. Real elapsed time can't pass
// within one synchronous reapNestedClosedBeadWorktrees invocation, so
// staleness is simulated with a shared call counter instead: clean on the
// first probe, dirty on every probe after that. WorktreeList always
// reports the single nested candidate regardless of which path constructed
// the fake, mirroring fakeNestedGuardGit's shared-admin-dir behavior.
type fakeStaleNestedGit struct {
	path             string
	nested           string
	uncommittedCalls *int
}

func (f *fakeStaleNestedGit) IsRepo() bool { return true }
func (f *fakeStaleNestedGit) WorktreeList() ([]git.Worktree, error) {
	return []git.Worktree{{Path: f.nested, Branch: "nested-branch"}}, nil
}

func (f *fakeStaleNestedGit) HasUncommittedWork() bool {
	if f.path != f.nested {
		return false
	}
	*f.uncommittedCalls++
	return *f.uncommittedCalls > 1
}
func (f *fakeStaleNestedGit) HasUnpushedCommitsResult() (bool, error) { return false, nil }
func (f *fakeStaleNestedGit) HasStashesResult() (bool, error)         { return false, nil }
func (f *fakeStaleNestedGit) WorktreeRemove(string, bool) error       { return nil }

// TestReapNestedClosedBeadWorktrees_RevalidatesBeforeRemove is the
// regression test for ga-8hq7be's blocking TOCTOU finding:
// doctor.DiscoverNestedWorktrees classifies SafeToRm once up front, then
// reapNestedClosedBeadWorktrees does bead-store I/O before acting on that
// now-possibly-stale classification and calling WorktreeRemove with
// force=true — which bypasses git's own dirty-worktree refusal, so a stale
// SafeToRm is the only thing standing between this call and destroying
// live work. fakeStaleNestedGit reports clean on the first
// HasUncommittedWork probe (the initial discovery-time classification) and
// dirty on every probe after that, simulating work landing in the nested
// worktree during the window between discovery and removal. A correct
// implementation must revalidate immediately before removing and skip
// rather than destroy the newly-dirty worktree.
func TestReapNestedClosedBeadWorktrees_RevalidatesBeforeRemove(t *testing.T) {
	cityPath := t.TempDir()
	home := makeNestedReapHome(t, cityPath)
	nested := filepath.Join(home, "ga-abc123")

	uncommittedCalls := 0
	withNestedGuardGit(t, func(path string) doctor.GitWorktree {
		return &fakeStaleNestedGit{path: path, nested: nested, uncommittedCalls: &uncommittedCalls}
	})

	store := beads.NewMemStoreFrom(1, []beads.Bead{
		{ID: "ga-abc123", Title: "test bead", Status: "closed", Type: "task"},
	}, nil)
	rec := &capturingRecorder{}

	reaped := reapNestedClosedBeadWorktrees(cityPath, gaConfig(), map[string]beads.Store{"gascity": store}, rec, nil)

	if reaped != 0 {
		t.Errorf("reaped = %d, want 0 (revalidation must catch work that landed after discovery)", reaped)
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
		t.Errorf("Subject = %q, want %q", skipped[0].Subject, "ga-abc123")
	}
	if uncommittedCalls < 2 {
		t.Fatalf("HasUncommittedWork called %d times, want >= 2 (discovery + revalidation) — test didn't exercise the revalidation path", uncommittedCalls)
	}
}

func TestRigNameForWorktreePathTopLevel(t *testing.T) {
	wtRoot := filepath.Join("city", ".gc", "worktrees")
	rig, ok := rigNameForWorktreePath(wtRoot, filepath.Join(wtRoot, "gascity", "builder"))
	if !ok || rig != "gascity" {
		t.Errorf("rigNameForWorktreePath = (%q, %v), want (%q, true)", rig, ok, "gascity")
	}
}

func TestRigNameForWorktreePathDeeplyNested(t *testing.T) {
	wtRoot := filepath.Join("city", ".gc", "worktrees")
	path := filepath.Join(wtRoot, "gascity", "builder", "ga-abc123", "child-worktree")
	rig, ok := rigNameForWorktreePath(wtRoot, path)
	if !ok || rig != "gascity" {
		t.Errorf("rigNameForWorktreePath = (%q, %v), want (%q, true)", rig, ok, "gascity")
	}
}

func TestRigNameForWorktreePathNotUnderRoot(t *testing.T) {
	wtRoot := filepath.Join("city", ".gc", "worktrees")
	rig, ok := rigNameForWorktreePath(wtRoot, filepath.Join("other", "path"))
	if ok {
		t.Errorf("rigNameForWorktreePath = (%q, %v), want ok=false", rig, ok)
	}
}

func TestRigNameForWorktreePathSameAsRoot(t *testing.T) {
	wtRoot := filepath.Join("city", ".gc", "worktrees")
	rig, ok := rigNameForWorktreePath(wtRoot, wtRoot)
	if ok {
		t.Errorf("rigNameForWorktreePath = (%q, %v), want ok=false for wtRoot itself", rig, ok)
	}
}
