package main

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/beadmeta"
	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/config"
	"github.com/gastownhall/gascity/internal/runtime"
	"github.com/gastownhall/gascity/internal/testutil"
	"github.com/gastownhall/gascity/internal/worktree"
)

func TestBindPoolSessionTriggerBeadVerifiesManagedWorktreeBeforePublishing(t *testing.T) {
	repo, base := testutil.InitGitRepoWithoutCWD(t)
	root := t.TempDir()
	path := filepath.Join(root, "gc-test")
	spec := worktree.Spec{
		RepoDir: repo, Root: root, Path: path, Branch: "work/gc-test", Base: base,
		BeadID: "gc-test", StoreRef: "rig:gascity", Creator: "gc-sling",
		Owner: "gc-sling", Generation: "1", Lifecycle: worktree.LifecycleActive,
	}
	report, err := worktree.Ensure(spec)
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	spec.BaseSHA = report.Provenance.BaseSHA

	store := beads.NewMemStore()
	sessionBead, err := store.Create(beads.Bead{ID: "session-1", Type: "session"})
	if err != nil {
		t.Fatalf("Create session bead: %v", err)
	}
	info, err := sessionFrontDoor(store).Get(sessionBead.ID)
	if err != nil {
		t.Fatalf("Get session info: %v", err)
	}
	cfg := &config.City{
		Workspace: config.Workspace{Name: "city"},
		Agents: []config.Agent{{
			Name: "worker", WorkDir: filepath.Join(root, "slot"),
		}},
	}
	bp := newAgentBuildParams("city", t.TempDir(), cfg, runtime.NewFake(), time.Now().UTC(), store, &bytes.Buffer{})

	bound, err := bindPoolSessionTriggerBead(bp, &cfg.Agents[0], "worker", info, SessionRequest{
		WorkBeadID: "gc-test", WorkStoreRef: "rig:gascity", WorktreeSpec: &spec,
	})
	if err != nil {
		t.Fatalf("bind: %v", err)
	}
	if bound.WorkDirCanonical != path || bound.WorkDir != path {
		t.Fatalf("bound work dirs = canonical %q legacy %q, want verified %q", bound.WorkDirCanonical, bound.WorkDir, path)
	}
	persisted, err := store.Get(sessionBead.ID)
	if err != nil {
		t.Fatalf("Get persisted session: %v", err)
	}
	if persisted.Metadata[beadmeta.WorkDirMetadataKey] != path ||
		persisted.Metadata[beadmeta.LegacyWorkDirMetadataKey] != path {
		t.Fatalf("persisted metadata = %+v, want verified work dir twins", persisted.Metadata)
	}
}

func TestBindPoolSessionTriggerBeadRejectsCompetingOwnerBeforeMetadata(t *testing.T) {
	repo, base := testutil.InitGitRepoWithoutCWD(t)
	root := t.TempDir()
	path := filepath.Join(root, "gc-test")
	spec := worktree.Spec{
		RepoDir: repo, Root: root, Path: path, Branch: "work/gc-test", Base: base,
		BeadID: "gc-test", StoreRef: "rig:gascity", Creator: "gc-sling",
		Owner: "gc-sling", Generation: "1", Lifecycle: worktree.LifecycleActive,
	}
	report, err := worktree.Ensure(spec)
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	spec.BaseSHA = report.Provenance.BaseSHA
	spec.Owner = "formula"

	store := beads.NewMemStore()
	sessionBead, err := store.Create(beads.Bead{ID: "session-1", Type: "session"})
	if err != nil {
		t.Fatalf("Create session bead: %v", err)
	}
	info, err := sessionFrontDoor(store).Get(sessionBead.ID)
	if err != nil {
		t.Fatalf("Get session info: %v", err)
	}
	cfg := &config.City{Workspace: config.Workspace{Name: "city"}, Agents: []config.Agent{{Name: "worker"}}}
	bp := newAgentBuildParams("city", t.TempDir(), cfg, runtime.NewFake(), time.Now().UTC(), store, &bytes.Buffer{})

	if _, err := bindPoolSessionTriggerBead(bp, &cfg.Agents[0], "worker", info, SessionRequest{
		WorkBeadID: "gc-test", WorkStoreRef: "rig:gascity", WorktreeSpec: &spec,
	}); err == nil {
		t.Fatal("bind accepted competing worktree owner")
	}
	persisted, err := store.Get(sessionBead.ID)
	if err != nil {
		t.Fatalf("Get persisted session: %v", err)
	}
	if persisted.Metadata[beadmeta.WorkDirMetadataKey] != "" ||
		persisted.Metadata[beadmeta.LegacyWorkDirMetadataKey] != "" ||
		persisted.Metadata[beadmeta.TriggerBeadIDMetadataKey] != "" {
		t.Fatalf("failed verification published partial metadata: %+v", persisted.Metadata)
	}
}

func TestBindPoolSessionTriggerBeadRejectsManagedDemandWithoutWorktreeEvidence(t *testing.T) {
	store := beads.NewMemStore()
	sessionBead, err := store.Create(beads.Bead{ID: "session-1", Type: "session"})
	if err != nil {
		t.Fatalf("Create session bead: %v", err)
	}
	info, err := sessionFrontDoor(store).Get(sessionBead.ID)
	if err != nil {
		t.Fatalf("Get session info: %v", err)
	}
	root := t.TempDir()
	cfg := &config.City{
		Workspace: config.Workspace{Name: "city"},
		Agents: []config.Agent{{
			Name: "worker", WorkDir: filepath.Join(root, "slot"),
		}},
	}
	bp := newAgentBuildParams("city", root, cfg, runtime.NewFake(), time.Now().UTC(), store, &bytes.Buffer{})

	if _, err := bindPoolSessionTriggerBead(bp, &cfg.Agents[0], "worker", info, SessionRequest{
		Tier: "new", WorkBeadID: "gc-test", WorkStoreRef: "rig:gascity",
	}); err == nil {
		t.Fatal("bind accepted managed demand without verified worktree evidence")
	}
	persisted, err := store.Get(sessionBead.ID)
	if err != nil {
		t.Fatalf("Get persisted session: %v", err)
	}
	if persisted.Metadata[beadmeta.WorkDirMetadataKey] != "" ||
		persisted.Metadata[beadmeta.LegacyWorkDirMetadataKey] != "" ||
		persisted.Metadata[beadmeta.TriggerBeadIDMetadataKey] != "" {
		t.Fatalf("failed verification published partial metadata: %+v", persisted.Metadata)
	}
}

func TestBindPoolSessionTriggerBeadAllowsExplicitUnmanagedDirectFallback(t *testing.T) {
	store := beads.NewMemStore()
	sessionBead, err := store.Create(beads.Bead{ID: "session-1", Type: "session"})
	if err != nil {
		t.Fatalf("Create session bead: %v", err)
	}
	info, err := sessionFrontDoor(store).Get(sessionBead.ID)
	if err != nil {
		t.Fatalf("Get session info: %v", err)
	}
	root := t.TempDir()
	cfg := &config.City{
		Workspace: config.Workspace{Name: "city"},
		Agents: []config.Agent{{
			Name: "worker", WorkDir: filepath.Join(root, "slot"),
		}},
	}
	bp := newAgentBuildParams("city", root, cfg, runtime.NewFake(), time.Now().UTC(), store, &bytes.Buffer{})

	bound, err := bindPoolSessionTriggerBead(bp, &cfg.Agents[0], "worker", info, SessionRequest{
		Tier: "resume", WorkBeadID: "gc-test", UnmanagedDirect: true,
	})
	if err != nil {
		t.Fatalf("bind explicit unmanaged direct session: %v", err)
	}
	if bound.WorkDirCanonical == "" || bound.WorkDirCanonical != bound.WorkDir {
		t.Fatalf("bound work dirs = canonical %q legacy %q, want matching configured fallback", bound.WorkDirCanonical, bound.WorkDir)
	}
}

func TestClassifyInFlightWorktreeRequestRequiresCurrentDemandEvidence(t *testing.T) {
	request := SessionRequest{Tier: "new", WorkBeadID: "gc-test"}

	unmanaged := classifyInFlightWorktreeRequest(request, scaleCheckDemand{
		WorkBeadIDs: []string{"gc-test"},
	})
	if !unmanaged.UnmanagedDirect || unmanaged.WorktreeError != "" {
		t.Fatalf("no-evidence current demand = %+v, want explicit unmanaged classification", unmanaged)
	}

	incomplete := classifyInFlightWorktreeRequest(request, scaleCheckDemand{
		WorkBeadIDs:    []string{"gc-test"},
		WorktreeErrors: map[string]string{"gc-test": "missing gc.worktree_owner"},
	})
	if incomplete.UnmanagedDirect || !strings.Contains(incomplete.WorktreeError, "gc.worktree_owner") {
		t.Fatalf("incomplete managed demand = %+v, want fail-closed evidence error", incomplete)
	}

	unmatched := classifyInFlightWorktreeRequest(request, scaleCheckDemand{})
	if unmatched.UnmanagedDirect || unmatched.WorktreeError == "" {
		t.Fatalf("unmatched in-flight demand = %+v, want unclassified fail-closed error", unmatched)
	}
}

func TestWorktreeSpecForBeadRequiresCompletePublishedEvidence(t *testing.T) {
	metadata := map[string]string{
		beadmeta.WorkDirMetadataKey:            "/worktrees/gc-test",
		beadmeta.WorkBranchMetadataKey:         "work/gc-test",
		beadmeta.WorktreeRootMetadataKey:       "/worktrees",
		beadmeta.WorktreeRepoMetadataKey:       "/repos/gascity",
		beadmeta.WorktreeBaseRefMetadataKey:    "main",
		beadmeta.WorktreeBaseSHAMetadataKey:    strings.Repeat("a", 40),
		beadmeta.WorktreeCreatorMetadataKey:    "gc-sling",
		beadmeta.WorktreeOwnerMetadataKey:      "gc-sling",
		beadmeta.WorktreeGenerationMetadataKey: "7",
		beadmeta.WorktreeLifecycleMetadataKey:  worktree.LifecycleActive,
	}
	bead := beads.Bead{ID: "gc-test", Metadata: metadata}

	spec, err := worktreeSpecForBead(bead, "rig:gascity")
	if err != nil {
		t.Fatalf("worktreeSpecForBead: %v", err)
	}
	if spec == nil || spec.BeadID != bead.ID || spec.StoreRef != "rig:gascity" ||
		spec.RepoDir != metadata[beadmeta.WorktreeRepoMetadataKey] ||
		spec.Root != metadata[beadmeta.WorktreeRootMetadataKey] ||
		spec.Path != metadata[beadmeta.WorkDirMetadataKey] ||
		spec.Branch != metadata[beadmeta.WorkBranchMetadataKey] ||
		spec.Base != metadata[beadmeta.WorktreeBaseRefMetadataKey] ||
		spec.BaseSHA != metadata[beadmeta.WorktreeBaseSHAMetadataKey] ||
		spec.Creator != metadata[beadmeta.WorktreeCreatorMetadataKey] ||
		spec.Owner != metadata[beadmeta.WorktreeOwnerMetadataKey] ||
		spec.Generation != metadata[beadmeta.WorktreeGenerationMetadataKey] ||
		spec.Lifecycle != metadata[beadmeta.WorktreeLifecycleMetadataKey] {
		t.Fatalf("spec = %+v, want exact published evidence", spec)
	}

	delete(metadata, beadmeta.WorktreeOwnerMetadataKey)
	if _, err := worktreeSpecForBead(bead, "rig:gascity"); err == nil ||
		!strings.Contains(err.Error(), beadmeta.WorktreeOwnerMetadataKey) {
		t.Fatalf("incomplete evidence error = %v, want missing owner key", err)
	}

	delete(metadata, beadmeta.WorkDirMetadataKey)
	if _, err := worktreeSpecForBead(bead, "rig:gascity"); err == nil ||
		!strings.Contains(err.Error(), "managed worktree evidence") {
		t.Fatalf("bead with partial evidence and no work_dir error = %v, want fail closed", err)
	}

	if spec, err := worktreeSpecForBead(beads.Bead{ID: "gc-direct"}, "rig:gascity"); err != nil || spec != nil {
		t.Fatalf("bead without any worktree evidence = spec %+v err %v, want explicit unmanaged boundary", spec, err)
	}
}
