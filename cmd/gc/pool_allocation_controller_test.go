package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/beadmeta"
	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/config"
	"github.com/gastownhall/gascity/internal/events"
	"github.com/gastownhall/gascity/internal/rollout"
	"github.com/gastownhall/gascity/internal/rollout/gate"
	"github.com/gastownhall/gascity/internal/runtime"
	sessionpkg "github.com/gastownhall/gascity/internal/session"
)

func TestAuthoritativeReadyRoutedWorkByIDReadsBackingStateWithoutFleetScan(t *testing.T) {
	now := time.Now().UTC()

	t.Run("event cache says ready but backing work is closed", func(t *testing.T) {
		backing := &readyRoutedWorkReadAuditStore{Store: beads.NewMemStore()}
		work, err := backing.Create(beads.Bead{
			Title:    "stale cached work",
			Type:     "task",
			Status:   "open",
			Metadata: map[string]string{"gc.routed_to": "worker"},
		})
		if err != nil {
			t.Fatalf("create work: %v", err)
		}
		cache := beads.NewCachingStoreForTest(backing, nil)
		if err := cache.Prime(context.Background()); err != nil {
			t.Fatalf("prime cache: %v", err)
		}
		if _, ready := cache.CachedReadyByID(work.ID, now); !ready {
			t.Fatal("precondition: cache did not retain ready work")
		}
		if err := backing.Close(work.ID); err != nil {
			t.Fatalf("close backing work without cache event: %v", err)
		}
		backing.getCalls.Store(0)
		backing.listCalls.Store(0)
		backing.readyCalls.Store(0)
		backing.depListCalls.Store(0)

		got, ready, err := authoritativeReadyRoutedWorkByID(cache, work.ID, now)
		if err != nil {
			t.Fatalf("authoritative ready read: %v", err)
		}
		if ready || got.ID != "" {
			t.Fatalf("authoritative ready read = (%+v, %t), want not ready", got, ready)
		}
		if got := backing.getCalls.Load(); got != 1 {
			t.Fatalf("backing Get calls = %d, want 1", got)
		}
		if got := backing.listCalls.Load(); got != 0 {
			t.Fatalf("backing List calls = %d, want 0", got)
		}
		if got := backing.readyCalls.Load(); got != 0 {
			t.Fatalf("backing Ready calls = %d, want 0", got)
		}
	})

	t.Run("open blocking dependency fails closed with exact reads", func(t *testing.T) {
		backing := &readyRoutedWorkReadAuditStore{Store: beads.NewMemStore()}
		work, err := backing.Create(beads.Bead{Title: "blocked work", Type: "task", Status: "open"})
		if err != nil {
			t.Fatalf("create work: %v", err)
		}
		blocker, err := backing.Create(beads.Bead{Title: "blocker", Type: "task", Status: "open"})
		if err != nil {
			t.Fatalf("create blocker: %v", err)
		}
		if err := backing.DepAdd(work.ID, blocker.ID, "blocks"); err != nil {
			t.Fatalf("add dependency: %v", err)
		}
		backing.getCalls.Store(0)

		got, ready, err := authoritativeReadyRoutedWorkByID(backing, work.ID, now)
		if err != nil {
			t.Fatalf("authoritative ready read: %v", err)
		}
		if ready || got.ID != "" {
			t.Fatalf("authoritative ready read = (%+v, %t), want blocked", got, ready)
		}
		if got := backing.getCalls.Load(); got != 2 {
			t.Fatalf("backing Get calls = %d, want work plus blocker", got)
		}
		if got := backing.depListCalls.Load(); got != 1 {
			t.Fatalf("backing DepList calls = %d, want 1", got)
		}
		if got := backing.listCalls.Load(); got != 0 {
			t.Fatalf("backing List calls = %d, want 0", got)
		}
		if got := backing.readyCalls.Load(); got != 0 {
			t.Fatalf("backing Ready calls = %d, want 0", got)
		}
	})

	t.Run("dependency read uncertainty is an error", func(t *testing.T) {
		base := beads.NewMemStore()
		work, err := base.Create(beads.Bead{Title: "uncertain work", Type: "task", Status: "open"})
		if err != nil {
			t.Fatalf("create work: %v", err)
		}
		readErr := errors.New("dependency store unavailable")
		store := &poolAllocationDepListErrorStore{Store: base, err: readErr}

		_, ready, err := authoritativeReadyRoutedWorkByID(store, work.ID, now)
		if ready || !errors.Is(err, readErr) {
			t.Fatalf("authoritative ready read = (ready=%t, err=%v), want dependency error", ready, err)
		}
	})
}

func TestRoutedWorkPoolAllocationMaterializesOneDurableSessionAndUsesExactStart(t *testing.T) {
	fixture := newRoutedWorkPoolAllocationFixture(t, beads.NewMemStore())
	priority := 3
	work, err := fixture.store.Create(beads.Bead{
		Title:    "ready routed work",
		Type:     "task",
		Status:   "open",
		Priority: &priority,
		Metadata: map[string]string{
			"gc.routed_to":                    "worker",
			beadmeta.PackMetadataKey:          "review-pack",
			beadmeta.PackWorkspaceMetadataKey: "workspace-a",
		},
	})
	if err != nil {
		t.Fatalf("create ready work: %v", err)
	}
	hint := routedWorkPoolAllocationHint{
		WorkID:      work.ID,
		PoolTarget:  "worker",
		SourceStore: "city:test-city",
		EventAt:     time.Now().UTC().Add(-time.Second),
		EnqueuedAt:  time.Now().UTC(),
	}

	first, err := fixture.cr.reconcileRoutedWorkPoolAllocation(t.Context(), hint)
	if err != nil {
		t.Fatalf("reconcile routed-work allocation: %v", err)
	}
	if !first.Handled || !first.Created || first.Session.ID == "" {
		t.Fatalf("first allocation = %+v, want one created session", first)
	}

	second, err := fixture.cr.reconcileRoutedWorkPoolAllocation(t.Context(), hint)
	if err != nil {
		t.Fatalf("reconcile duplicate routed-work allocation: %v", err)
	}
	if !second.Handled || second.Created || second.Session.ID != first.Session.ID {
		t.Fatalf("duplicate allocation = %+v, want existing session %s", second, first.Session.ID)
	}

	stored, err := fixture.store.Get(first.Session.ID)
	if err != nil {
		t.Fatalf("read created session: %v", err)
	}
	if stored.Metadata[beadmeta.TriggerBeadIDMetadataKey] != work.ID ||
		stored.Metadata[beadmeta.TriggerBeadStoreRefMetadataKey] != hint.SourceStore ||
		stored.Metadata[beadmeta.PackMetadataKey] != "review-pack" ||
		stored.Metadata[beadmeta.PackWorkspaceMetadataKey] != "workspace-a" {
		t.Fatalf("created session trigger metadata = %+v, want authoritative work binding", stored.Metadata)
	}

	infos, err := loadSessionBeadSnapshot(fixture.store)
	if err != nil {
		t.Fatalf("load created sessions: %v", err)
	}
	if got := len(infos.OpenInfos()); got != 1 {
		t.Fatalf("open sessions = %d, want exactly 1", got)
	}
	awaitCond(t, func() bool {
		return fixture.provider.IsRunning(first.Session.SessionName) || fixture.cr.sessionStartController.Pending() == 0
	}, "exact-start controller to finish directly materialized pool session")
	if !fixture.provider.IsRunning(first.Session.SessionName) {
		current := sessionpkg.Info{}
		if currentInfo, _, currentErr := sessionFrontDoor(fixture.store).GetPersistedResponse(first.Session.ID); currentErr == nil {
			current = currentInfo
		}
		snapshot, release, snapshotErr := fixture.cr.cs.acquireSessionStartSnapshot()
		var lease routedWorkPoolStartLease
		var leaseErr error
		var authorized bool
		var authorizeErr error
		if snapshotErr == nil {
			defer release()
			lease, leaseErr = fixture.cr.newRoutedWorkPoolStartLease(snapshot, first.Session, hint)
			if leaseErr == nil {
				authorized, authorizeErr = fixture.cr.authorizeRoutedWorkPoolStart(t.Context(), snapshot, first.Session, lease)
			}
		}
		t.Fatalf("directly materialized pool session %q is not running; current=%+v snapshot=%v lease=%+v lease_err=%v authorized=%t authorize_err=%v membership=%+v fallback=%t controller stderr:\n%s\nruntime calls: %+v", first.Session.SessionName, current, snapshotErr, lease, leaseErr, authorized, authorizeErr, fixture.cr.poolMembershipShadow.observe("worker"), fixture.cr.readyRoutedWorkPokePending.Load(), fixture.stderr.String(), fixture.provider.SnapshotCalls())
	}
}

func TestRoutedWorkPoolAllocationGrowsOccupiedUnlimitedPoolForDistinctRoutedWork(t *testing.T) {
	fixture := newRoutedWorkPoolAllocationFixture(t, beads.NewMemStore())

	firstWork, err := fixture.store.Create(beads.Bead{
		Title:    "first ready routed work",
		Type:     "task",
		Status:   "open",
		Metadata: map[string]string{"gc.routed_to": "worker"},
	})
	if err != nil {
		t.Fatalf("create first routed work: %v", err)
	}
	firstHint := routedWorkPoolAllocationHint{
		WorkID:      firstWork.ID,
		PoolTarget:  "worker",
		SourceStore: "city:test-city",
		EventAt:     time.Now().UTC().Add(-time.Second),
		EnqueuedAt:  time.Now().UTC(),
	}
	first, err := fixture.cr.reconcileRoutedWorkPoolAllocation(t.Context(), firstHint)
	if err != nil {
		t.Fatalf("allocate first routed work: %v", err)
	}
	if !first.Handled || !first.Created || first.Session.PoolSlot != "1" {
		t.Fatalf("first allocation = %+v, want created slot-1 session", first)
	}
	awaitCond(t, func() bool {
		return fixture.provider.IsRunning(first.Session.SessionName) && fixture.cr.sessionStartController.Pending() == 0
	}, "first pool session to become active through keyed exact start")
	fixture.cr.handleRoutedWorkPoolAllocation(t.Context(), firstHint)
	awaitCond(t, func() bool { return fixture.cr.sessionStartController.Pending() == 0 }, "active duplicate to settle without another keyed start")
	if starts := fixture.provider.CountCalls("Start", first.Session.SessionName); starts != 1 {
		t.Fatalf("provider Start calls for active duplicate = %d, want 1", starts)
	}
	if fixture.cr.readyRoutedWorkPokePending.Load() || len(fixture.cr.pokeCh) != 0 {
		t.Fatalf("legacy fallback after active duplicate = (pending=%t, pokes=%d), want none", fixture.cr.readyRoutedWorkPokePending.Load(), len(fixture.cr.pokeCh))
	}

	secondWork, err := fixture.store.Create(beads.Bead{
		Title:    "second distinct ready routed work",
		Type:     "task",
		Status:   "open",
		Metadata: map[string]string{"gc.routed_to": "worker"},
	})
	if err != nil {
		t.Fatalf("create second routed work: %v", err)
	}
	secondHint := routedWorkPoolAllocationHint{
		WorkID:      secondWork.ID,
		PoolTarget:  "worker",
		SourceStore: "city:test-city",
		EventAt:     time.Now().UTC().Add(-time.Second),
		EnqueuedAt:  time.Now().UTC(),
	}
	second, err := fixture.cr.reconcileRoutedWorkPoolAllocation(t.Context(), secondHint)
	if err != nil {
		t.Fatalf("allocate second routed work: %v", err)
	}
	if !second.Handled || !second.Created || second.Session.ID == first.Session.ID || second.Session.PoolSlot != "2" {
		t.Fatalf("second allocation = %+v, want one distinct created slot-2 session", second)
	}
	awaitCond(t, func() bool {
		return fixture.provider.IsRunning(second.Session.SessionName) && fixture.cr.sessionStartController.Pending() == 0
	}, "second pool session to become active through keyed exact start")

	infos, err := loadSessionBeadSnapshot(fixture.store)
	if err != nil {
		t.Fatalf("load pool sessions after second allocation: %v", err)
	}
	if open := infos.OpenInfos(); len(open) != 2 {
		t.Fatalf("open sessions after distinct routed work = %d, want 2: %+v", len(open), open)
	}

	replay, err := fixture.cr.reconcileRoutedWorkPoolAllocation(t.Context(), secondHint)
	if err != nil {
		t.Fatalf("replay second routed work: %v", err)
	}
	if !replay.Handled || replay.Created || replay.Session.ID != second.Session.ID {
		t.Fatalf("replayed second allocation = %+v, want rediscovered session %q without create", replay, second.Session.ID)
	}
	awaitCond(t, func() bool { return fixture.cr.sessionStartController.Pending() == 0 }, "replayed exact start admission to settle")
	infos, err = loadSessionBeadSnapshot(fixture.store)
	if err != nil {
		t.Fatalf("load pool sessions after replay: %v", err)
	}
	if open := infos.OpenInfos(); len(open) != 2 {
		t.Fatalf("open sessions after replay = %d, want 2: %+v", len(open), open)
	}
	if starts := fixture.provider.CountCalls("Start", second.Session.SessionName); starts != 1 {
		t.Fatalf("provider Start calls for replayed slot-2 session = %d, want 1", starts)
	}
}

func TestRoutedWorkPoolAllocationStartsBelowBoundedAgentCapAndFallsBackAtCap(t *testing.T) {
	fixture := newRoutedWorkPoolAllocationFixture(t, beads.NewMemStore())
	maximum := 2
	fixture.cr.cfg.Agents[0].MaxActiveSessions = &maximum

	allocate := func(title string) routedWorkPoolAllocationResult {
		t.Helper()
		work, err := fixture.store.Create(beads.Bead{
			Title:    title,
			Type:     "task",
			Status:   "open",
			Metadata: map[string]string{"gc.routed_to": "worker"},
		})
		if err != nil {
			t.Fatalf("create %s: %v", title, err)
		}
		hint := routedWorkPoolAllocationHint{
			WorkID:      work.ID,
			PoolTarget:  "worker",
			SourceStore: "city:test-city",
			EventAt:     time.Now().UTC().Add(-time.Second),
			EnqueuedAt:  time.Now().UTC(),
		}
		result, err := fixture.cr.reconcileRoutedWorkPoolAllocation(t.Context(), hint)
		if err != nil {
			t.Fatalf("allocate %s: %v", title, err)
		}
		return result
	}

	first := allocate("first bounded-pool work")
	if !first.Handled || !first.Created || first.Session.PoolSlot != "1" {
		t.Fatalf("first bounded allocation = %+v, want created slot-1 session", first)
	}
	awaitCond(t, func() bool {
		return fixture.provider.IsRunning(first.Session.SessionName) && fixture.cr.sessionStartController.Pending() == 0
	}, "first bounded-pool session to start")

	second := allocate("second bounded-pool work")
	if !second.Handled || !second.Created || second.Session.PoolSlot != "2" {
		t.Fatalf("second bounded allocation = %+v, want created slot-2 session", second)
	}
	awaitCond(t, func() bool {
		return fixture.provider.IsRunning(second.Session.SessionName) && fixture.cr.sessionStartController.Pending() == 0
	}, "second bounded-pool session to start")

	thirdWork, err := fixture.store.Create(beads.Bead{
		Title:    "third bounded-pool work",
		Type:     "task",
		Status:   "open",
		Metadata: map[string]string{"gc.routed_to": "worker"},
	})
	if err != nil {
		t.Fatalf("create third bounded-pool work: %v", err)
	}
	fixture.cr.handleRoutedWorkPoolAllocation(t.Context(), routedWorkPoolAllocationHint{
		WorkID:      thirdWork.ID,
		PoolTarget:  "worker",
		SourceStore: "city:test-city",
	})
	assertRoutedWorkPoolAllocationFallback(t, fixture.cr)

	infos, err := loadSessionBeadSnapshot(fixture.store)
	if err != nil {
		t.Fatalf("load bounded pool sessions: %v", err)
	}
	if open := infos.OpenInfos(); len(open) != maximum {
		t.Fatalf("open bounded pool sessions = %d, want cap %d: %+v", len(open), maximum, open)
	}
	if starts := fixture.provider.CountCalls("Start", first.Session.SessionName) + fixture.provider.CountCalls("Start", second.Session.SessionName); starts != maximum {
		t.Fatalf("bounded pool provider starts = %d, want %d", starts, maximum)
	}
}

func TestRoutedWorkPoolAllocationStartsColdCanonicalSingletonAndFallsBackAtCap(t *testing.T) {
	fixture := newRoutedWorkPoolAllocationFixture(t, beads.NewMemStore())
	maximum := 1
	fixture.cr.cfg.Agents[0].MaxActiveSessions = &maximum

	firstWork, err := fixture.store.Create(beads.Bead{
		Title:    "first singleton work",
		Type:     "task",
		Status:   "open",
		Metadata: map[string]string{"gc.routed_to": "worker"},
	})
	if err != nil {
		t.Fatalf("create first singleton work: %v", err)
	}
	firstHint := routedWorkPoolAllocationHint{
		WorkID:      firstWork.ID,
		PoolTarget:  "worker",
		SourceStore: "city:test-city",
		EventAt:     time.Now().UTC().Add(-time.Second),
		EnqueuedAt:  time.Now().UTC(),
	}
	first, err := fixture.cr.reconcileRoutedWorkPoolAllocation(t.Context(), firstHint)
	if err != nil {
		t.Fatalf("allocate cold singleton work: %v", err)
	}
	if !first.Handled || !first.Created || first.Session.PoolSlot != "" {
		t.Fatalf("cold singleton allocation = %+v, want one canonical slotless session", first)
	}
	stored, err := fixture.store.Get(first.Session.ID)
	if err != nil {
		t.Fatalf("read canonical singleton session: %v", err)
	}
	if stored.Metadata["agent_name"] != "worker" || stored.Metadata["alias"] != "worker" || stored.Metadata["pool_slot"] != "" {
		t.Fatalf("singleton identity metadata = %+v, want canonical unsuffixed worker", stored.Metadata)
	}
	awaitCond(t, func() bool {
		return fixture.provider.IsRunning(first.Session.SessionName) && fixture.cr.sessionStartController.Pending() == 0
	}, "cold canonical singleton to start")

	secondWork, err := fixture.store.Create(beads.Bead{
		Title:    "second singleton work",
		Type:     "task",
		Status:   "open",
		Metadata: map[string]string{"gc.routed_to": "worker"},
	})
	if err != nil {
		t.Fatalf("create second singleton work: %v", err)
	}
	fixture.cr.handleRoutedWorkPoolAllocation(t.Context(), routedWorkPoolAllocationHint{
		WorkID:      secondWork.ID,
		PoolTarget:  "worker",
		SourceStore: "city:test-city",
	})
	assertRoutedWorkPoolAllocationFallback(t, fixture.cr)

	infos, err := loadSessionBeadSnapshot(fixture.store)
	if err != nil {
		t.Fatalf("load singleton sessions: %v", err)
	}
	if open := infos.OpenInfos(); len(open) != maximum {
		t.Fatalf("open singleton sessions = %d, want cap %d: %+v", len(open), maximum, open)
	}
	if starts := fixture.provider.CountCalls("Start", first.Session.SessionName); starts != 1 {
		t.Fatalf("singleton provider starts = %d, want 1", starts)
	}
}

func TestRoutedWorkPoolAllocationReusesIdleCanonicalSingletonForNewWork(t *testing.T) {
	opened, err := beads.OpenStoreAtForCity(t.Context(), beads.StoreOpenOptions{
		Provider:          "file",
		OpenFileStore:     func() (beads.Store, error) { return beads.NewMemStore(), nil },
		ConditionalWrites: gate.Auto,
	})
	if err != nil {
		t.Fatalf("open conditional-write singleton store: %v", err)
	}
	fixture := newRoutedWorkPoolAllocationFixture(t, opened.Store)
	maximum := 1
	fixture.cr.cfg.Agents[0].MaxActiveSessions = &maximum
	fixture.cr.cfg.Agents[0].Provider = "claude"
	fixture.cr.cfg.Agents[0].WorkDir = "worker-root"
	fixture.cr.cfg.Agents[0].Nudge = "Run gc hook --claim --json now."

	firstWork, err := fixture.store.Create(beads.Bead{
		Title:    "first singleton work",
		Type:     "task",
		Status:   "open",
		Metadata: map[string]string{"gc.routed_to": "worker"},
	})
	if err != nil {
		t.Fatalf("create first singleton work: %v", err)
	}
	firstHint := routedWorkPoolAllocationHint{
		WorkID:      firstWork.ID,
		PoolTarget:  "worker",
		SourceStore: "city:test-city",
	}
	first, err := fixture.cr.reconcileRoutedWorkPoolAllocation(t.Context(), firstHint)
	if err != nil || !first.Handled || !first.Created {
		t.Fatalf("allocate cold singleton = (%+v, %v), want one keyed create", first, err)
	}
	awaitCond(t, func() bool {
		return fixture.provider.IsRunning(first.Session.SessionName) && fixture.cr.sessionStartController.Pending() == 0
	}, "cold canonical singleton to start")
	if err := fixture.store.Close(firstWork.ID); err != nil {
		t.Fatalf("close first singleton work: %v", err)
	}
	active, err := sessionFrontDoor(fixture.store).Get(first.Session.ID)
	if err != nil {
		t.Fatalf("read active singleton: %v", err)
	}
	setRoutedWorkPoolRuntimeIdentity(t, fixture, active)
	fixture.provider.WaitForIdleErrors[first.Session.SessionName] = nil
	baselineNudges := fixture.provider.CountCalls("Nudge", first.Session.SessionName) +
		fixture.provider.CountCalls("NudgeNow", first.Session.SessionName)

	secondWork, err := fixture.store.Create(beads.Bead{
		Title:  "second singleton work",
		Type:   "task",
		Status: "open",
		Metadata: map[string]string{
			"gc.routed_to":                    "worker",
			beadmeta.PackMetadataKey:          "review-pack",
			beadmeta.PackWorkspaceMetadataKey: "workspace-b",
		},
	})
	if err != nil {
		t.Fatalf("create second singleton work: %v", err)
	}
	secondHint := routedWorkPoolAllocationHint{
		WorkID:      secondWork.ID,
		PoolTarget:  "worker",
		SourceStore: "city:test-city",
	}
	fixture.cr.handleRoutedWorkPoolAllocation(t.Context(), secondHint)

	infos, err := loadSessionBeadSnapshot(fixture.store)
	if err != nil {
		t.Fatalf("load singleton sessions after reuse: %v", err)
	}
	open := infos.OpenInfos()
	if len(open) != 1 || open[0].ID != first.Session.ID {
		t.Fatalf("open singleton sessions after reuse = %+v, want only %q", open, first.Session.ID)
	}
	stored, err := fixture.store.Get(first.Session.ID)
	if err != nil {
		t.Fatalf("read rebound singleton session: %v", err)
	}
	wantWorkDir := filepath.Join(fixture.cr.cityPath, "review-pack", "workspace-b")
	if stored.Metadata[beadmeta.TriggerBeadIDMetadataKey] != secondWork.ID ||
		stored.Metadata[beadmeta.TriggerBeadStoreRefMetadataKey] != secondHint.SourceStore ||
		stored.Metadata[beadmeta.PackMetadataKey] != "review-pack" ||
		stored.Metadata[beadmeta.PackWorkspaceMetadataKey] != "workspace-b" ||
		stored.Metadata[beadmeta.WorkDirMetadataKey] != wantWorkDir ||
		stored.Metadata[beadmeta.LegacyWorkDirMetadataKey] != wantWorkDir {
		t.Fatalf("rebound singleton trigger metadata = %+v, want exact second-work provenance; fallback=(%t,%d) stderr=%q",
			stored.Metadata, fixture.cr.readyRoutedWorkPokePending.Load(), len(fixture.cr.pokeCh), fixture.stderr.String())
	}
	if got := fixture.provider.CountCalls("Start", first.Session.SessionName); got != 1 {
		t.Fatalf("provider Start calls after singleton reuse = %d, want 1", got)
	}
	if got := fixture.provider.CountCalls("Stop", first.Session.SessionName); got != 0 {
		t.Fatalf("provider Stop calls after singleton reuse = %d, want 0", got)
	}
	if got := fixture.provider.CountCalls("Nudge", first.Session.SessionName) +
		fixture.provider.CountCalls("NudgeNow", first.Session.SessionName); got != baselineNudges+1 {
		t.Fatalf("claim nudge calls after singleton reuse = %d, want %d", got, baselineNudges+1)
	}
	if fixture.cr.readyRoutedWorkPokePending.Load() || len(fixture.cr.pokeCh) != 0 {
		t.Fatalf("legacy fallback after singleton reuse = (pending=%t, pokes=%d), want none", fixture.cr.readyRoutedWorkPokePending.Load(), len(fixture.cr.pokeCh))
	}

	reboundRevision := stored.Revision
	fixture.cr.handleRoutedWorkPoolAllocation(t.Context(), secondHint)
	replayed, err := fixture.store.Get(first.Session.ID)
	if err != nil {
		t.Fatalf("read singleton session after replay: %v", err)
	}
	if replayed.Revision != reboundRevision {
		t.Fatalf("singleton replay revision = %d, want unchanged %d", replayed.Revision, reboundRevision)
	}
	if got := fixture.provider.CountCalls("Nudge", first.Session.SessionName) +
		fixture.provider.CountCalls("NudgeNow", first.Session.SessionName); got != baselineNudges+1 {
		t.Fatalf("claim nudge calls after replay = %d, want %d", got, baselineNudges+1)
	}
	if fixture.cr.readyRoutedWorkPokePending.Load() || len(fixture.cr.pokeCh) != 0 {
		t.Fatalf("legacy fallback after singleton replay = (pending=%t, pokes=%d), want none", fixture.cr.readyRoutedWorkPokePending.Load(), len(fixture.cr.pokeCh))
	}
}

func TestRoutedWorkPoolAllocationReusesSoleIdleGenericMemberForNewWork(t *testing.T) {
	for _, maximum := range []int{2, -1} {
		t.Run(fmt.Sprintf("max_active_sessions=%d", maximum), func(t *testing.T) {
			fixture, firstWork, info := prepareIdleGenericPoolMemberForReuse(t, true, maximum)
			fixture.cr.cfg.Agents[0].WorkDir = ".gc/worktrees/{{.AgentBase}}"
			if err := fixture.store.Close(firstWork.ID); err != nil {
				t.Fatalf("close prior routed work: %v", err)
			}
			baselineNudges := providerNudgeCalls(fixture.provider, info.SessionNameMetadata)
			baselineCalls := len(fixture.provider.SnapshotCalls())
			secondWork, err := fixture.store.Create(beads.Bead{
				Title:  "second generic work",
				Type:   "task",
				Status: "open",
				Metadata: map[string]string{
					"gc.routed_to":                    "worker",
					beadmeta.PackWorkspaceMetadataKey: "workspace-b",
				},
			})
			if err != nil {
				t.Fatalf("create second routed work: %v", err)
			}
			result, err := fixture.cr.reconcileRoutedWorkPoolAllocation(t.Context(), routedWorkPoolAllocationHint{
				WorkID: secondWork.ID, PoolTarget: "worker", SourceStore: "city:test-city",
			})
			if err != nil || !result.Handled || result.Created || result.Session.ID != info.ID || result.Session.PoolSlot != info.PoolSlot {
				t.Fatalf("reuse sole generic member = (%+v, %v), want existing %q without create", result, err, info.ID)
			}
			stored, err := fixture.store.Get(info.ID)
			if err != nil {
				t.Fatalf("read rebound generic member: %v", err)
			}
			wantWorkDir := filepath.Join(fixture.cr.cityPath, ".gc", "worktrees", "worker-1", "workspace-b")
			if stored.Metadata[beadmeta.TriggerBeadIDMetadataKey] != secondWork.ID ||
				stored.Metadata[beadmeta.TriggerBeadStoreRefMetadataKey] != "city:test-city" ||
				stored.Metadata["pool_slot"] != info.PoolSlot ||
				stored.Metadata[beadmeta.PackMetadataKey] != "" ||
				stored.Metadata[beadmeta.PackWorkspaceMetadataKey] != "workspace-b" ||
				stored.Metadata[beadmeta.WorkDirMetadataKey] != wantWorkDir ||
				stored.Metadata[beadmeta.LegacyWorkDirMetadataKey] != wantWorkDir {
				t.Fatalf("rebound generic metadata = %#v, want exact second-work provenance with work dir %q", stored.Metadata, wantWorkDir)
			}
			infos, err := loadSessionBeadSnapshot(fixture.store)
			if err != nil {
				t.Fatalf("load generic sessions: %v", err)
			}
			if open := infos.OpenInfos(); len(open) != 1 || open[0].ID != info.ID {
				t.Fatalf("open generic sessions = %+v, want only %q", open, info.ID)
			}
			if got := providerCallCount(fixture.provider, "Start"); got != 1 {
				t.Fatalf("global provider Start calls after generic reuse = %d, want 1", got)
			}
			if got := providerCallCount(fixture.provider, "Stop"); got != 0 {
				t.Fatalf("global provider Stop calls after generic reuse = %d, want 0", got)
			}
			if got := providerNudgeCalls(fixture.provider, info.SessionNameMetadata); got != baselineNudges+1 {
				t.Fatalf("generic nudge calls = %d, want %d", got, baselineNudges+1)
			}
			assertExactProviderNudgeSince(t, fixture.provider, baselineCalls, info.SessionNameMetadata, "<system-reminder>\nYou have a deferred reminder that was queued until a safe boundary:\n\n- [routed-work-pool-reuse] Run gc hook --claim --json now.\n\nHandle them after this turn.\n</system-reminder>\n")
			if fixture.cr.readyRoutedWorkPokePending.Load() || len(fixture.cr.pokeCh) != 0 {
				t.Fatalf("legacy fallback after generic reuse = (pending=%t, pokes=%d), want none", fixture.cr.readyRoutedWorkPokePending.Load(), len(fixture.cr.pokeCh))
			}

			reboundRevision := stored.Revision
			replay, err := fixture.cr.reconcileRoutedWorkPoolAllocation(t.Context(), routedWorkPoolAllocationHint{
				WorkID: secondWork.ID, PoolTarget: "worker", SourceStore: "city:test-city",
			})
			if err != nil || !replay.Handled || replay.Created || replay.Session.ID != info.ID {
				t.Fatalf("replay generic reuse = (%+v, %v), want same existing member", replay, err)
			}
			afterReplay, err := fixture.store.Get(info.ID)
			if err != nil {
				t.Fatalf("read generic member after replay: %v", err)
			}
			if afterReplay.Revision != reboundRevision {
				t.Fatalf("generic replay revision = %d, want unchanged %d", afterReplay.Revision, reboundRevision)
			}
			if got := providerNudgeCalls(fixture.provider, info.SessionNameMetadata); got != baselineNudges+1 {
				t.Fatalf("generic replay nudge calls = %d, want %d", got, baselineNudges+1)
			}
			if fixture.cr.readyRoutedWorkPokePending.Load() || len(fixture.cr.pokeCh) != 0 {
				t.Fatalf("legacy fallback after generic replay = (pending=%t, pokes=%d), want none", fixture.cr.readyRoutedWorkPokePending.Load(), len(fixture.cr.pokeCh))
			}
		})
	}
}

func TestRoutedWorkPoolAllocationBusyGenericReuseGrowsWithoutRebinding(t *testing.T) {
	tests := []struct {
		name       string
		closePrior bool
		mutate     func(testing.TB, routedWorkPoolAllocationFixture, sessionpkg.Info)
	}{
		{name: "open prior trigger"},
		{
			name:       "assigned work",
			closePrior: true,
			mutate: func(t testing.TB, fixture routedWorkPoolAllocationFixture, info sessionpkg.Info) {
				t.Helper()
				if _, err := fixture.store.Create(beads.Bead{Title: "assigned", Type: "task", Status: "open", Assignee: info.ID}); err != nil {
					t.Fatalf("create assigned work: %v", err)
				}
			},
		},
		{
			name:       "blocked assigned work",
			closePrior: true,
			mutate: func(t testing.TB, fixture routedWorkPoolAllocationFixture, info sessionpkg.Info) {
				t.Helper()
				assigned, err := fixture.store.Create(beads.Bead{Title: "blocked assigned", Type: "task", Status: "open", Assignee: info.ID})
				if err != nil {
					t.Fatalf("create blocked assigned work: %v", err)
				}
				blocker, err := fixture.store.Create(beads.Bead{Title: "blocker", Type: "task", Status: "open"})
				if err != nil {
					t.Fatalf("create assigned-work blocker: %v", err)
				}
				if err := fixture.store.DepAdd(assigned.ID, blocker.ID, "blocks"); err != nil {
					t.Fatalf("block assigned work: %v", err)
				}
			},
		},
		{
			name:       "future-deferred assigned work",
			closePrior: true,
			mutate: func(t testing.TB, fixture routedWorkPoolAllocationFixture, info sessionpkg.Info) {
				t.Helper()
				future := time.Now().UTC().Add(time.Hour)
				if _, err := fixture.store.Create(beads.Bead{Title: "deferred assigned", Type: "task", Status: "open", Assignee: info.ID, DeferUntil: &future}); err != nil {
					t.Fatalf("create future-deferred assigned work: %v", err)
				}
			},
		},
		{
			name:       "human attachment",
			closePrior: true,
			mutate: func(_ testing.TB, fixture routedWorkPoolAllocationFixture, info sessionpkg.Info) {
				fixture.provider.SetAttached(info.SessionNameMetadata, true)
			},
		},
		{
			name:       "pending interaction",
			closePrior: true,
			mutate: func(_ testing.TB, fixture routedWorkPoolAllocationFixture, info sessionpkg.Info) {
				fixture.provider.SetPendingInteraction(info.SessionNameMetadata, &runtime.PendingInteraction{RequestID: "approval-1"})
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture, firstWork, info := prepareIdleGenericPoolMemberForReuse(t, true, 2)
			if test.closePrior {
				if err := fixture.store.Close(firstWork.ID); err != nil {
					t.Fatalf("close prior routed work: %v", err)
				}
			}
			if test.mutate != nil {
				test.mutate(t, fixture, info)
			}
			before, err := fixture.store.Get(info.ID)
			if err != nil {
				t.Fatalf("read original generic member: %v", err)
			}
			secondWork, err := fixture.store.Create(beads.Bead{Title: "second generic work", Type: "task", Status: "open", Metadata: map[string]string{"gc.routed_to": "worker"}})
			if err != nil {
				t.Fatalf("create second routed work: %v", err)
			}
			result, err := fixture.cr.reconcileRoutedWorkPoolAllocation(t.Context(), routedWorkPoolAllocationHint{WorkID: secondWork.ID, PoolTarget: "worker", SourceStore: "city:test-city"})
			if err != nil || !result.Handled || !result.Created || result.Session.ID == info.ID || result.Session.PoolSlot != "2" {
				t.Fatalf("busy generic allocation = (%+v, %v), want new slot-2 member below capacity", result, err)
			}
			awaitCond(t, func() bool {
				return fixture.provider.IsRunning(result.Session.SessionName) && fixture.cr.sessionStartController.Pending() == 0
			}, "busy generic allocation to start its second member")
			after, err := fixture.store.Get(info.ID)
			if err != nil {
				t.Fatalf("read original generic member after growth: %v", err)
			}
			if !reflect.DeepEqual(after, before) {
				t.Fatalf("busy generic allocation rebound original member\n before=%+v\n  after=%+v", before, after)
			}
			infos, err := loadSessionBeadSnapshot(fixture.store)
			if err != nil {
				t.Fatalf("load grown generic pool: %v", err)
			}
			if open := infos.OpenInfos(); len(open) != 2 {
				t.Fatalf("open generic sessions after busy growth = %+v, want two", open)
			}
			if fixture.cr.readyRoutedWorkPokePending.Load() || len(fixture.cr.pokeCh) != 0 {
				t.Fatalf("legacy fallback after proven-busy growth = (pending=%t, pokes=%d), want none", fixture.cr.readyRoutedWorkPokePending.Load(), len(fixture.cr.pokeCh))
			}
			if got := providerCallCount(fixture.provider, "Start"); got != 2 {
				t.Fatalf("global provider Start calls after busy growth = %d, want 2", got)
			}
			if got := providerCallCount(fixture.provider, "Stop"); got != 0 {
				t.Fatalf("global provider Stop calls after busy growth = %d, want 0", got)
			}
		})
	}
}

func TestRoutedWorkPoolAllocationRefusesManualAndDependencyOnlySoleMembers(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(testing.TB, routedWorkPoolAllocationFixture, sessionpkg.Info)
	}{
		{
			name: "legacy manual row",
			mutate: func(t testing.TB, fixture routedWorkPoolAllocationFixture, info sessionpkg.Info) {
				t.Helper()
				if err := fixture.store.SetMetadata(info.ID, poolManagedMetadataKey, ""); err != nil {
					t.Fatalf("clear legacy manual pool marker: %v", err)
				}
				if err := fixture.store.SetMetadata(info.ID, "pool_slot", ""); err != nil {
					t.Fatalf("clear legacy manual pool slot: %v", err)
				}
			},
		},
		{
			name: "dependency-only row",
			mutate: func(t testing.TB, fixture routedWorkPoolAllocationFixture, info sessionpkg.Info) {
				t.Helper()
				if err := fixture.store.SetMetadata(info.ID, "dependency_only", "true"); err != nil {
					t.Fatalf("mark member dependency-only: %v", err)
				}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture, firstWork, info := prepareIdleGenericPoolMemberForReuse(t, true, 2)
			if err := fixture.store.Close(firstWork.ID); err != nil {
				t.Fatalf("close prior routed work: %v", err)
			}
			test.mutate(t, fixture, info)
			before, err := fixture.store.Get(info.ID)
			if err != nil {
				t.Fatalf("read protected member before allocation: %v", err)
			}
			baselineNudges := providerNudgeCalls(fixture.provider, info.SessionNameMetadata)
			secondWork, err := fixture.store.Create(beads.Bead{Title: "second routed work", Type: "task", Status: "open", Metadata: map[string]string{"gc.routed_to": "worker"}})
			if err != nil {
				t.Fatalf("create second routed work: %v", err)
			}

			fixture.cr.handleRoutedWorkPoolAllocation(t.Context(), routedWorkPoolAllocationHint{WorkID: secondWork.ID, PoolTarget: "worker", SourceStore: "city:test-city"})

			after, err := fixture.store.Get(info.ID)
			if err != nil {
				t.Fatalf("read protected member after allocation: %v", err)
			}
			if !reflect.DeepEqual(after, before) {
				t.Fatalf("refused reuse changed protected durable member\n before=%+v\n  after=%+v", before, after)
			}
			if got := providerCallCount(fixture.provider, "Start"); got != 1 {
				t.Fatalf("global provider Start calls after refused reuse = %d, want 1", got)
			}
			if got := providerCallCount(fixture.provider, "Stop"); got != 0 {
				t.Fatalf("global provider Stop calls after refused reuse = %d, want 0", got)
			}
			if got := providerNudgeCalls(fixture.provider, info.SessionNameMetadata); got != baselineNudges {
				t.Fatalf("protected member nudge calls = %d, want unchanged %d", got, baselineNudges)
			}
			assertRoutedWorkPoolAllocationFallback(t, fixture.cr)
		})
	}
}

func TestRoutedWorkPoolAllocationRefusedGenericReuseFallsBackWithoutGrowth(t *testing.T) {
	tests := []struct {
		name        string
		conditional bool
		mutate      func(testing.TB, routedWorkPoolAllocationFixture, sessionpkg.Info)
	}{
		{name: "conditional writer unavailable"},
		{
			name:        "runtime instance token mismatch",
			conditional: true,
			mutate: func(t testing.TB, fixture routedWorkPoolAllocationFixture, info sessionpkg.Info) {
				t.Helper()
				if err := fixture.provider.SetMeta(info.SessionNameMetadata, "GC_INSTANCE_TOKEN", "replacement-token"); err != nil {
					t.Fatalf("replace runtime token: %v", err)
				}
			},
		},
		{
			name:        "identity uncertainty overrides attachment",
			conditional: true,
			mutate: func(t testing.TB, fixture routedWorkPoolAllocationFixture, info sessionpkg.Info) {
				t.Helper()
				fixture.provider.SetAttached(info.SessionNameMetadata, true)
				if err := fixture.provider.SetMeta(info.SessionNameMetadata, "GC_INSTANCE_TOKEN", "replacement-token"); err != nil {
					t.Fatalf("replace attached runtime token: %v", err)
				}
			},
		},
		{
			name:        "uncertified membership",
			conditional: true,
			mutate: func(_ testing.TB, fixture routedWorkPoolAllocationFixture, _ sessionpkg.Info) {
				fixture.cr.poolMembershipShadow.invalidate(poolMembershipUncertifiedSnapshotGap)
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture, firstWork, info := prepareIdleGenericPoolMemberForReuse(t, test.conditional, 2)
			if err := fixture.store.Close(firstWork.ID); err != nil {
				t.Fatalf("close prior routed work: %v", err)
			}
			if test.mutate != nil {
				test.mutate(t, fixture, info)
			}
			secondWork, err := fixture.store.Create(beads.Bead{Title: "second generic work", Type: "task", Status: "open", Metadata: map[string]string{"gc.routed_to": "worker"}})
			if err != nil {
				t.Fatalf("create second routed work: %v", err)
			}
			before, err := fixture.store.Get(info.ID)
			if err != nil {
				t.Fatalf("read generic member before refusal: %v", err)
			}
			baselineNudges := fixture.provider.CountCalls("Nudge", info.SessionNameMetadata) + fixture.provider.CountCalls("NudgeNow", info.SessionNameMetadata)

			fixture.cr.handleRoutedWorkPoolAllocation(t.Context(), routedWorkPoolAllocationHint{WorkID: secondWork.ID, PoolTarget: "worker", SourceStore: "city:test-city"})

			after, err := fixture.store.Get(info.ID)
			if err != nil {
				t.Fatalf("read generic member after refusal: %v", err)
			}
			if !reflect.DeepEqual(after, before) {
				t.Fatalf("refused generic reuse changed durable member\n before=%+v\n  after=%+v", before, after)
			}
			infos, err := loadSessionBeadSnapshot(fixture.store)
			if err != nil {
				t.Fatalf("load generic sessions after refusal: %v", err)
			}
			if open := infos.OpenInfos(); len(open) != 1 || open[0].ID != info.ID {
				t.Fatalf("refused generic reuse open sessions = %+v, want only %q", open, info.ID)
			}
			if got := fixture.provider.CountCalls("Start", info.SessionNameMetadata); got != 1 {
				t.Fatalf("refused generic reuse Start calls = %d, want 1", got)
			}
			if got := fixture.provider.CountCalls("Nudge", info.SessionNameMetadata) + fixture.provider.CountCalls("NudgeNow", info.SessionNameMetadata); got != baselineNudges {
				t.Fatalf("refused generic reuse nudge calls = %d, want %d", got, baselineNudges)
			}
			assertRoutedWorkPoolAllocationFallback(t, fixture.cr)
		})
	}
}

func TestRoutedWorkPoolAllocationStaleGenericReuseRevisionFallsBackWithoutGrowth(t *testing.T) {
	fixture, firstWork, info := prepareIdleGenericPoolMemberForReuse(t, true, 2)
	if err := fixture.store.Close(firstWork.ID); err != nil {
		t.Fatalf("close prior routed work: %v", err)
	}
	secondWork, err := fixture.store.Create(beads.Bead{
		Title:    "second generic work",
		Type:     "task",
		Status:   "open",
		Metadata: map[string]string{"gc.routed_to": "worker"},
	})
	if err != nil {
		t.Fatalf("create second generic work: %v", err)
	}
	underlying := fixture.store
	hooked := &triggerMatchingReadHookStore{
		Store:     underlying,
		sessionID: info.ID,
		workID:    firstWork.ID,
		after: func() {
			if err := underlying.SetMetadata(info.ID, "test_revision_race", "1"); err != nil {
				t.Fatalf("advance reusable member revision: %v", err)
			}
		},
	}
	fixture.store = hooked
	fixture.cr.cs.mu.Lock()
	fixture.cr.cs.cityBeadStore = hooked
	fixture.cr.cs.mu.Unlock()
	baselineNudges := fixture.provider.CountCalls("Nudge", info.SessionNameMetadata) +
		fixture.provider.CountCalls("NudgeNow", info.SessionNameMetadata)

	fixture.cr.handleRoutedWorkPoolAllocation(t.Context(), routedWorkPoolAllocationHint{
		WorkID: secondWork.ID, PoolTarget: "worker", SourceStore: "city:test-city",
	})

	after, err := underlying.Get(info.ID)
	if err != nil {
		t.Fatalf("read generic member after revision race: %v", err)
	}
	if after.Metadata[beadmeta.TriggerBeadIDMetadataKey] != firstWork.ID ||
		after.Metadata[beadmeta.TriggerBeadStoreRefMetadataKey] != "city:test-city" {
		t.Fatalf("stale generic reuse changed trigger binding: %#v", after.Metadata)
	}
	infos, err := loadSessionBeadSnapshot(underlying)
	if err != nil {
		t.Fatalf("load generic sessions after revision race: %v", err)
	}
	if open := infos.OpenInfos(); len(open) != 1 || open[0].ID != info.ID {
		t.Fatalf("stale generic reuse open sessions = %+v, want only %q", open, info.ID)
	}
	if got := fixture.provider.CountCalls("Nudge", info.SessionNameMetadata) +
		fixture.provider.CountCalls("NudgeNow", info.SessionNameMetadata); got != baselineNudges {
		t.Fatalf("stale generic reuse nudge calls = %d, want unchanged %d", got, baselineNudges)
	}
	assertRoutedWorkPoolAllocationFallback(t, fixture.cr)
}

func TestRoutedWorkPoolSingletonReuseLeavesAmbiguousStatesLegacyOwned(t *testing.T) {
	tests := []struct {
		name             string
		conditionalStore bool
		keepPreviousOpen bool
		mutate           func(testing.TB, routedWorkPoolAllocationFixture, sessionpkg.Info, beads.Bead)
	}{
		{
			name:             "human attached",
			conditionalStore: true,
			mutate: func(_ testing.TB, fixture routedWorkPoolAllocationFixture, info sessionpkg.Info, _ beads.Bead) {
				fixture.provider.SetAttached(info.SessionNameMetadata, true)
			},
		},
		{
			name:             "pending interaction",
			conditionalStore: true,
			mutate: func(_ testing.TB, fixture routedWorkPoolAllocationFixture, info sessionpkg.Info, _ beads.Bead) {
				fixture.provider.SetPendingInteraction(info.SessionNameMetadata, &runtime.PendingInteraction{RequestID: "approval-1"})
			},
		},
		{
			name:             "actionable assigned work",
			conditionalStore: true,
			mutate: func(t testing.TB, fixture routedWorkPoolAllocationFixture, info sessionpkg.Info, _ beads.Bead) {
				t.Helper()
				if _, err := fixture.store.Create(beads.Bead{Title: "already assigned", Type: "task", Status: "open", Assignee: info.ID}); err != nil {
					t.Fatalf("create assigned work: %v", err)
				}
			},
		},
		{
			name:             "previous trigger still open",
			conditionalStore: true,
			keepPreviousOpen: true,
		},
		{
			name:             "membership uncertified",
			conditionalStore: true,
			mutate: func(_ testing.TB, fixture routedWorkPoolAllocationFixture, _ sessionpkg.Info, _ beads.Bead) {
				fixture.cr.poolMembershipShadow.invalidate(poolMembershipUncertifiedSnapshotGap)
			},
		},
		{
			name:             "membership no longer sole",
			conditionalStore: true,
			mutate: func(t testing.TB, fixture routedWorkPoolAllocationFixture, info sessionpkg.Info, _ beads.Bead) {
				t.Helper()
				duplicate := info
				duplicate.ID = info.ID + "-duplicate"
				duplicate.InstanceToken = info.InstanceToken + "-duplicate"
				duplicate.SessionName = info.SessionName + "-duplicate"
				duplicate.SessionNameMetadata = duplicate.SessionName
				if err := fixture.cr.poolMembershipShadow.replace(fixture.cr.cfg, duplicate); err != nil {
					t.Fatalf("publish duplicate membership: %v", err)
				}
			},
		},
		{
			name:             "runtime instance token drift",
			conditionalStore: true,
			mutate: func(t testing.TB, fixture routedWorkPoolAllocationFixture, info sessionpkg.Info, _ beads.Bead) {
				t.Helper()
				if err := fixture.provider.SetMeta(info.SessionNameMetadata, "GC_INSTANCE_TOKEN", "replacement-token"); err != nil {
					t.Fatalf("replace runtime instance token: %v", err)
				}
			},
		},
		{
			name:             "new work route changed",
			conditionalStore: true,
			mutate: func(t testing.TB, fixture routedWorkPoolAllocationFixture, _ sessionpkg.Info, work beads.Bead) {
				t.Helper()
				if err := fixture.store.SetMetadata(work.ID, "gc.routed_to", "other"); err != nil {
					t.Fatalf("change new work route: %v", err)
				}
			},
		},
		{
			name: "conditional writes unavailable",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture, firstWork, info := prepareIdleCanonicalSingletonForReuse(t, test.conditionalStore)
			if !test.keepPreviousOpen {
				if err := fixture.store.Close(firstWork.ID); err != nil {
					t.Fatalf("close previous trigger work: %v", err)
				}
			}
			secondWork, err := fixture.store.Create(beads.Bead{
				Title:    "second singleton work",
				Type:     "task",
				Status:   "open",
				Metadata: map[string]string{"gc.routed_to": "worker"},
			})
			if err != nil {
				t.Fatalf("create second singleton work: %v", err)
			}
			if test.mutate != nil {
				test.mutate(t, fixture, info, secondWork)
			}
			before, err := fixture.store.Get(info.ID)
			if err != nil {
				t.Fatalf("read singleton before refused reuse: %v", err)
			}
			baselineNudges := fixture.provider.CountCalls("Nudge", info.SessionNameMetadata) +
				fixture.provider.CountCalls("NudgeNow", info.SessionNameMetadata)

			fixture.cr.handleRoutedWorkPoolAllocation(t.Context(), routedWorkPoolAllocationHint{
				WorkID: secondWork.ID, PoolTarget: "worker", SourceStore: "city:test-city",
			})

			after, err := fixture.store.Get(info.ID)
			if err != nil {
				t.Fatalf("read singleton after refused reuse: %v", err)
			}
			if !reflect.DeepEqual(after, before) {
				t.Fatalf("refused reuse changed durable session\n before=%+v\n  after=%+v", before, after)
			}
			if got := fixture.provider.CountCalls("Nudge", info.SessionNameMetadata) +
				fixture.provider.CountCalls("NudgeNow", info.SessionNameMetadata); got != baselineNudges {
				t.Fatalf("refused reuse nudge calls = %d, want unchanged %d", got, baselineNudges)
			}
			if got := fixture.provider.CountCalls("Start", info.SessionNameMetadata); got != 1 {
				t.Fatalf("refused reuse Start calls = %d, want 1", got)
			}
			if got := fixture.provider.CountCalls("Stop", info.SessionNameMetadata); got != 0 {
				t.Fatalf("refused reuse Stop calls = %d, want 0", got)
			}
			assertRoutedWorkPoolAllocationFallback(t, fixture.cr)
		})
	}
}

func TestRoutedWorkPoolSingletonReuseFallsBackAfterUnconfirmedIdleDelivery(t *testing.T) {
	fixture, firstWork, info := prepareIdleCanonicalSingletonForReuse(t, true)
	if err := fixture.store.Close(firstWork.ID); err != nil {
		t.Fatalf("close previous trigger work: %v", err)
	}
	fixture.provider.WaitForIdleErrors[info.SessionNameMetadata] = errors.New("not idle")
	secondWork, err := fixture.store.Create(beads.Bead{
		Title:    "second singleton work",
		Type:     "task",
		Status:   "open",
		Metadata: map[string]string{"gc.routed_to": "worker"},
	})
	if err != nil {
		t.Fatalf("create second singleton work: %v", err)
	}
	baselineNudges := fixture.provider.CountCalls("Nudge", info.SessionNameMetadata) +
		fixture.provider.CountCalls("NudgeNow", info.SessionNameMetadata)

	fixture.cr.handleRoutedWorkPoolAllocation(t.Context(), routedWorkPoolAllocationHint{
		WorkID: secondWork.ID, PoolTarget: "worker", SourceStore: "city:test-city",
	})

	stored, err := fixture.store.Get(info.ID)
	if err != nil {
		t.Fatalf("read rebound singleton: %v", err)
	}
	if stored.Metadata[beadmeta.TriggerBeadIDMetadataKey] != secondWork.ID {
		t.Fatalf("rebound trigger = %q, want durable %q despite unconfirmed delivery", stored.Metadata[beadmeta.TriggerBeadIDMetadataKey], secondWork.ID)
	}
	if got := fixture.provider.CountCalls("Nudge", info.SessionNameMetadata) +
		fixture.provider.CountCalls("NudgeNow", info.SessionNameMetadata); got != baselineNudges {
		t.Fatalf("unconfirmed delivery nudge calls = %d, want unchanged %d", got, baselineNudges)
	}
	assertRoutedWorkPoolAllocationFallback(t, fixture.cr)
}

func TestRoutedWorkPoolGenericReuseDoesNotStartAfterCommittedBindingLosesAuthorization(t *testing.T) {
	fixture, firstWork, info := prepareIdleGenericPoolMemberForReuse(t, true, 2)
	if err := fixture.store.Close(firstWork.ID); err != nil {
		t.Fatalf("close previous trigger work: %v", err)
	}
	secondWork, err := fixture.store.Create(beads.Bead{
		Title:    "second generic work",
		Type:     "task",
		Status:   "open",
		Metadata: map[string]string{"gc.routed_to": "worker"},
	})
	if err != nil {
		t.Fatalf("create second generic work: %v", err)
	}
	underlying := fixture.store
	hooked := &triggerMatchingReadHookStore{
		Store:     underlying,
		sessionID: info.ID,
		workID:    secondWork.ID,
		after: func() {
			if err := underlying.Close(info.ID); err != nil {
				t.Fatalf("retire singleton after committed rebind: %v", err)
			}
			fixture.cr.poolMembershipShadow.remove(info.ID)
		},
	}
	fixture.store = hooked
	fixture.cr.cs.mu.Lock()
	fixture.cr.cs.cityBeadStore = hooked
	fixture.cr.cs.mu.Unlock()
	baselineNudges := fixture.provider.CountCalls("Nudge", info.SessionNameMetadata) +
		fixture.provider.CountCalls("NudgeNow", info.SessionNameMetadata)

	fixture.cr.handleRoutedWorkPoolAllocation(t.Context(), routedWorkPoolAllocationHint{
		WorkID: secondWork.ID, PoolTarget: "worker", SourceStore: "city:test-city",
	})

	snapshot, err := loadSessionBeadSnapshot(fixture.store)
	if err != nil {
		t.Fatalf("load sessions after lost reuse authorization: %v", err)
	}
	if open := snapshot.OpenInfos(); len(open) != 0 {
		t.Fatalf("open sessions after lost reuse authorization = %+v, want no replacement after %q retired", open, info.ID)
	}
	if got := fixture.provider.CountCalls("Nudge", info.SessionNameMetadata) +
		fixture.provider.CountCalls("NudgeNow", info.SessionNameMetadata); got != baselineNudges {
		t.Fatalf("nudges after lost reuse authorization = %d, want unchanged %d", got, baselineNudges)
	}
	assertRoutedWorkPoolAllocationFallback(t, fixture.cr)
}

func TestRoutedWorkPoolAllocationCanonicalSingletonRetiresByExactDrainAck(t *testing.T) {
	opened, err := beads.OpenStoreAtForCity(t.Context(), beads.StoreOpenOptions{
		Provider:          "file",
		OpenFileStore:     func() (beads.Store, error) { return beads.NewAtomicCloseMemStore(), nil },
		ConditionalWrites: gate.Auto,
	})
	if err != nil {
		t.Fatalf("open conditional-write singleton store: %v", err)
	}
	fixture := newRoutedWorkPoolAllocationFixture(t, opened.Store)
	maximum := 1
	fixture.cr.cfg.Agents[0].MaxActiveSessions = &maximum
	work, err := fixture.store.Create(beads.Bead{
		Title:    "singleton lifecycle work",
		Type:     "task",
		Status:   "open",
		Metadata: map[string]string{"gc.routed_to": "worker"},
	})
	if err != nil {
		t.Fatalf("create singleton lifecycle work: %v", err)
	}
	hint := routedWorkPoolAllocationHint{WorkID: work.ID, PoolTarget: "worker", SourceStore: "city:test-city"}
	allocated, err := fixture.cr.reconcileRoutedWorkPoolAllocation(t.Context(), hint)
	if err != nil || !allocated.Handled || !allocated.Created {
		t.Fatalf("allocate canonical singleton = (%+v, %v), want one keyed create", allocated, err)
	}
	awaitCond(t, func() bool {
		return fixture.provider.IsRunning(allocated.Session.SessionName) && fixture.cr.sessionStartController.Pending() == 0
	}, "canonical singleton to start before drain acknowledgement")
	if err := fixture.store.Close(work.ID); err != nil {
		t.Fatalf("close singleton trigger work: %v", err)
	}
	info, err := sessionFrontDoor(fixture.store).Get(allocated.Session.ID)
	if err != nil {
		t.Fatalf("read active singleton session: %v", err)
	}
	for key, value := range map[string]string{
		"GC_SESSION_ID":                   info.ID,
		"GC_INSTANCE_TOKEN":               info.InstanceToken,
		reconcilerDrainAckSourceKey:       drainAckSourceAgentValue,
		drainAckRequesterSessionIDKey:     info.ID,
		drainAckRequesterInstanceTokenKey: info.InstanceToken,
		"GC_DRAIN_ACK":                    "1",
	} {
		if err := fixture.provider.SetMeta(info.SessionName, key, value); err != nil {
			t.Fatalf("set singleton runtime metadata %s: %v", key, err)
		}
	}

	snapshot, release, err := fixture.cr.cs.acquireSessionStartSnapshot()
	if err != nil {
		t.Fatalf("acquire singleton stop snapshot: %v", err)
	}
	lease, agentAck, leaseErr := fixture.cr.newRoutedWorkPoolDrainAckLease(snapshot, info)
	if leaseErr != nil || !agentAck {
		release()
		t.Fatalf("create singleton drain-ack lease = (%+v, %t, %v), want exact agent lease", lease, agentAck, leaseErr)
	}
	authorized, authorizeErr := fixture.cr.authorizeRoutedWorkPoolDrainAck(snapshot, info, lease)
	release()
	if authorizeErr != nil || !authorized {
		t.Fatalf("authorize canonical singleton drain acknowledgement = (%t, %v), want true", authorized, authorizeErr)
	}
	if reply := fixture.cr.admitSessionStartSocketKey(info.ID); reply != sessionStartSocketReplyOK {
		t.Fatalf("singleton drain-ack socket reply = %q, want %q", reply, sessionStartSocketReplyOK)
	}
	awaitCond(t, func() bool {
		row, getErr := fixture.store.Get(info.ID)
		return getErr == nil && row.Status == "closed" && row.Metadata["state"] == string(sessionpkg.StateDrained) &&
			row.Metadata["state_reason"] == "" && row.Metadata["close_reason"] == sessionpkg.CanonicalCloseReason("drained") &&
			row.Metadata["closed_at"] != "" && !fixture.provider.IsRunning(info.SessionName)
	}, "canonical singleton exact durable retirement")
	if got := fixture.provider.CountCalls("Stop", info.SessionName); got != 1 {
		t.Fatalf("canonical singleton provider Stop calls = %d, want 1", got)
	}
	if fixture.cr.readyRoutedWorkPokePending.Load() || len(fixture.cr.pokeCh) != 0 {
		t.Fatalf("legacy fallback after canonical singleton retirement = (pending=%t, pokes=%d), want none", fixture.cr.readyRoutedWorkPokePending.Load(), len(fixture.cr.pokeCh))
	}
}

func TestRoutedWorkPoolAllocationRediscoverPendingBindingFromStaleEmptyIndex(t *testing.T) {
	fixture := newRoutedWorkPoolAllocationFixture(t, beads.NewMemStore())
	work, err := fixture.store.Create(beads.Bead{
		Title:    "ready routed work with preexisting pending binding",
		Type:     "task",
		Status:   "open",
		Metadata: map[string]string{"gc.routed_to": "worker"},
	})
	if err != nil {
		t.Fatalf("create routed work: %v", err)
	}
	hint := routedWorkPoolAllocationHint{WorkID: work.ID, PoolTarget: "worker", SourceStore: "city:test-city"}
	pending := createRoutedWorkPoolBinding(t, fixture.store, fixture.cr.cfg, hint, 1)

	result, err := fixture.cr.reconcileRoutedWorkPoolAllocation(t.Context(), hint)
	if err != nil {
		t.Fatalf("reconcile stale-index pending binding: %v", err)
	}
	if !result.Handled || result.Created || result.Session.ID != pending.ID {
		t.Fatalf("stale-index allocation = %+v, want rediscovered pending binding %q without create", result, pending.ID)
	}
	awaitCond(t, func() bool {
		return fixture.provider.IsRunning(pending.SessionName) && fixture.cr.sessionStartController.Pending() == 0
	}, "rediscovered pending binding to start through its exact lease")
	infos, err := loadSessionBeadSnapshot(fixture.store)
	if err != nil {
		t.Fatalf("load sessions after stale-index recovery: %v", err)
	}
	if open := infos.OpenInfos(); len(open) != 1 || open[0].ID != pending.ID {
		t.Fatalf("open sessions after stale-index recovery = %+v, want only %q", open, pending.ID)
	}
}

func TestRoutedWorkPoolAllocationFailsClosedOnAmbiguousBinding(t *testing.T) {
	fixture := newRoutedWorkPoolAllocationFixture(t, beads.NewMemStore())
	work, err := fixture.store.Create(beads.Bead{
		Title:    "ready routed work with ambiguous bindings",
		Type:     "task",
		Status:   "open",
		Metadata: map[string]string{"gc.routed_to": "worker"},
	})
	if err != nil {
		t.Fatalf("create routed work: %v", err)
	}
	hint := routedWorkPoolAllocationHint{WorkID: work.ID, PoolTarget: "worker", SourceStore: "city:test-city"}
	first := createRoutedWorkPoolBinding(t, fixture.store, fixture.cr.cfg, hint, 1)
	second := createRoutedWorkPoolBinding(t, fixture.store, fixture.cr.cfg, hint, 2)

	if got, found, err := findRoutedWorkPoolSession(fixture.store, fixture.cr.cfg, hint); err == nil || found || got.ID != "" {
		t.Fatalf("find ambiguous routed-work bindings = (%+v, %t, %v), want ambiguity error", got, found, err)
	}
	fixture.cr.handleRoutedWorkPoolAllocation(t.Context(), hint)
	assertRoutedWorkPoolAllocationFallback(t, fixture.cr)
	if got := fixture.provider.CountCalls("Start", first.SessionName) + fixture.provider.CountCalls("Start", second.SessionName); got != 0 {
		t.Fatalf("provider starts for ambiguous bindings = %d, want 0", got)
	}
	infos, err := loadSessionBeadSnapshot(fixture.store)
	if err != nil {
		t.Fatalf("load ambiguous bindings: %v", err)
	}
	if open := infos.OpenInfos(); len(open) != 2 {
		t.Fatalf("open sessions after ambiguous binding = %d, want 2", len(open))
	}
}

func TestRoutedWorkPoolAllocationLeavesUnsafeExistingBindingsLegacyOwned(t *testing.T) {
	newActiveBinding := func(t *testing.T) (routedWorkPoolAllocationFixture, routedWorkPoolAllocationHint, sessionpkg.Info) {
		t.Helper()
		fixture := newRoutedWorkPoolAllocationFixture(t, beads.NewMemStore())
		work, err := fixture.store.Create(beads.Bead{
			Title:    "ready routed work",
			Type:     "task",
			Status:   "open",
			Metadata: map[string]string{"gc.routed_to": "worker"},
		})
		if err != nil {
			t.Fatalf("create routed work: %v", err)
		}
		hint := routedWorkPoolAllocationHint{WorkID: work.ID, PoolTarget: "worker", SourceStore: "city:test-city"}
		result, err := fixture.cr.reconcileRoutedWorkPoolAllocation(t.Context(), hint)
		if err != nil || !result.Created {
			t.Fatalf("create active binding = (%+v, %v), want created", result, err)
		}
		awaitCond(t, func() bool {
			return fixture.provider.IsRunning(result.Session.SessionName) && fixture.cr.sessionStartController.Pending() == 0
		}, "initial keyed pool start to settle")
		return fixture, hint, result.Session
	}

	t.Run("unsupported policy", func(t *testing.T) {
		fixture, hint, _ := newActiveBinding(t)
		fixture.cr.cfg.Agents[0].DependsOn = []string{"another-template"}
		fixture.cr.handleRoutedWorkPoolAllocation(t.Context(), hint)
		assertRoutedWorkPoolAllocationFallback(t, fixture.cr)
	})

	t.Run("asleep existing binding", func(t *testing.T) {
		fixture, hint, session := newActiveBinding(t)
		if err := fixture.store.SetMetadata(session.ID, "state", string(sessionpkg.StateAsleep)); err != nil {
			t.Fatalf("mark existing binding asleep: %v", err)
		}
		fixture.cr.handleRoutedWorkPoolAllocation(t.Context(), hint)
		assertRoutedWorkPoolAllocationFallback(t, fixture.cr)
	})
}

func TestRoutedWorkPoolAllocationEventCoalescingDoesNotActivateLegacyFallback(t *testing.T) {
	genericEntered := make(chan struct{})
	genericSuperseded := make(chan struct{}, 1)
	releaseGeneric := make(chan struct{})
	var enterOnce sync.Once
	var releaseOnce sync.Once

	originalNewController := newCitySessionStartController
	newCitySessionStartController = func(opts sessionStartControllerOptions) (*sessionStartController, error) {
		reconcile := opts.Reconcile
		observe := opts.Observer
		opts.Reconcile = func(ctx context.Context, admission sessionStartAdmission) error {
			if admission.PoolAllocation == nil {
				enterOnce.Do(func() { close(genericEntered) })
				select {
				case <-releaseGeneric:
				case <-ctx.Done():
					return ctx.Err()
				}
			}
			return reconcile(ctx, admission)
		}
		opts.Observer = func(result sessionStartReconcileResult) {
			if result.Admission.PoolAllocation == nil && result.Outcome == sessionStartReconcileSuperseded {
				select {
				case genericSuperseded <- struct{}{}:
				default:
				}
			}
			if observe != nil {
				observe(result)
			}
		}
		return originalNewController(opts)
	}
	t.Cleanup(func() { newCitySessionStartController = originalNewController })

	var controllerState *controllerState
	store := beads.NewCachingStoreForTest(beads.NewMemStore(), func(eventType, beadID string, payload json.RawMessage) {
		if controllerState == nil {
			return
		}
		controllerState.admitSessionStartEvent(events.Event{
			Type:    eventType,
			Subject: beadID,
			Payload: payload,
		})
		if eventType == events.BeadCreated {
			var bead beads.Bead
			if err := json.Unmarshal(payload, &bead); err == nil && bead.Type == sessionpkg.BeadType {
				awaitClose(t, genericEntered, "generic session-create event reconciliation")
			}
		}
	})
	fixture := newRoutedWorkPoolAllocationFixture(t, store)
	t.Cleanup(func() { releaseOnce.Do(func() { close(releaseGeneric) }) })
	controllerState = fixture.cr.cs

	work, err := fixture.store.Create(beads.Bead{
		Title:    "ready routed work with emitted session events",
		Type:     "task",
		Status:   "open",
		Metadata: map[string]string{"gc.routed_to": "worker"},
	})
	if err != nil {
		t.Fatalf("create ready work: %v", err)
	}
	hint := routedWorkPoolAllocationHint{
		WorkID:      work.ID,
		PoolTarget:  "worker",
		SourceStore: "city:test-city",
		EventAt:     time.Now().UTC().Add(-time.Second),
		EnqueuedAt:  time.Now().UTC(),
	}

	fixture.cr.handleRoutedWorkPoolAllocation(t.Context(), hint)
	awaitCond(t, func() bool {
		controller := fixture.cr.sessionStartController
		controller.mu.Lock()
		defer controller.mu.Unlock()
		for _, admission := range controller.admissions {
			if admission.PoolAllocation != nil {
				return true
			}
		}
		return false
	}, "pool-allocation lease to supersede generic create event")
	releaseOnce.Do(func() { close(releaseGeneric) })
	awaitClose(t, genericSuperseded, "generic create event to resolve as superseded")
	awaitCond(t, func() bool {
		return fixture.cr.sessionStartController.Pending() == 0
	}, "leased exact start and emitted update events to settle")

	infos, err := loadSessionBeadSnapshot(fixture.store)
	if err != nil {
		t.Fatalf("load materialized session: %v", err)
	}
	open := infos.OpenInfos()
	if len(open) != 1 {
		t.Fatalf("open sessions = %d, want exactly 1", len(open))
	}
	if !fixture.provider.IsRunning(open[0].SessionName) {
		t.Fatalf("materialized session %q is not running", open[0].SessionName)
	}
	if got := fixture.provider.CountCalls("Start", open[0].SessionName); got != 1 {
		t.Fatalf("provider Start calls for %q = %d, want exactly 1", open[0].SessionName, got)
	}
	if fixture.cr.readyRoutedWorkPokePending.Load() || len(fixture.cr.pokeCh) != 0 {
		t.Fatalf("legacy fallback after allocator-owned events = (pending=%t, pokes=%d), want none", fixture.cr.readyRoutedWorkPokePending.Load(), len(fixture.cr.pokeCh))
	}
}

func TestRoutedWorkPoolAllocationLeaseExcludesLegacyStartWhileKeyedStartIsInFlight(t *testing.T) {
	fixture := newRoutedWorkPoolAuthorizationFixture(t)
	opened, err := beads.OpenStoreAtForCity(t.Context(), beads.StoreOpenOptions{
		Provider:          "file",
		OpenFileStore:     func() (beads.Store, error) { return fixture.store, nil },
		ConditionalWrites: gate.Auto,
	})
	if err != nil {
		t.Fatalf("enable conditional writes on collision fixture: %v", err)
	}
	if opened.Store != fixture.store {
		t.Fatalf("conditional-writes fixture store = %T, want original %T", opened.Store, fixture.store)
	}
	commitEntered := make(chan struct{})
	releaseCommit := make(chan struct{})
	var releaseOnce sync.Once
	barrierStore := &poolAllocationStartCommitBarrierStore{
		Store:       fixture.snapshot.Store,
		provider:    fixture.provider,
		sessionID:   fixture.info.ID,
		sessionName: fixture.info.SessionName,
		entered:     commitEntered,
		release:     releaseCommit,
	}

	controller, err := newSessionStartController(sessionStartControllerOptions{
		Workers:     1,
		MaxDistinct: 4,
		MaxRetries:  0,
		Reconcile: func(ctx context.Context, admission sessionStartAdmission) error {
			owner, reconcileErr := reconcileExactSessionStartWithOwner(ctx, admission, exactSessionStartParams{
				Generation:   fixture.snapshot.Generation,
				CityPath:     fixture.snapshot.CityPath,
				CityName:     fixture.snapshot.CityName,
				Config:       fixture.snapshot.Config,
				Provider:     fixture.snapshot.Provider,
				Store:        barrierStore,
				Recorder:     events.Discard,
				Stdout:       io.Discard,
				Stderr:       io.Discard,
				StartOptions: []startExecutionOption{withStartStabilityWaiter(immediateStartStabilityWaiter)},
				AuthorizePoolStart: func(authorizeCtx context.Context, info sessionpkg.Info, lease routedWorkPoolStartLease) (bool, error) {
					return fixture.cr.authorizeRoutedWorkPoolStart(authorizeCtx, fixture.snapshot, info, lease)
				},
			})
			if reconcileErr == nil && owner == exactSessionStartLegacyOwner {
				return errSessionStartLegacyFallbackRequired
			}
			return reconcileErr
		},
	})
	if err != nil {
		t.Fatalf("create blocked exact-start controller: %v", err)
	}
	if err := controller.Start(t.Context()); err != nil {
		t.Fatalf("start blocked exact-start controller: %v", err)
	}
	t.Cleanup(func() {
		releaseOnce.Do(func() { close(releaseCommit) })
		controller.Stop()
	})
	fixture.cr.sessionStartMu.Lock()
	fixture.cr.sessionStartController = controller
	fixture.cr.sessionStartOwnership = sessionStartOwnershipKeyed
	fixture.cr.sessionStartMode = rollout.Auto
	fixture.cr.sessionStartMu.Unlock()

	if outcome, err := controller.AdmitPoolAllocation(fixture.lease); err != nil || outcome != sessionStartAdmissionAccepted {
		t.Fatalf("admit pool allocation = (%q, %v), want accepted", outcome, err)
	}
	awaitClose(t, commitEntered, "keyed provider start to reach the durable commit boundary")
	if !fixture.provider.IsRunning(fixture.info.SessionName) {
		t.Fatal("keyed provider is not live at the pre-commit barrier")
	}
	if got := fixture.provider.CountCalls("Start", fixture.info.SessionName); got != 1 {
		t.Fatalf("provider Start calls at the pre-commit barrier = %d, want exactly 1", got)
	}
	beforeLegacy, err := fixture.store.Get(fixture.info.ID)
	if err != nil {
		t.Fatalf("read allocator-owned row at pre-commit barrier: %v", err)
	}
	preCommitInfo, err := sessionFrontDoor(fixture.store).Get(fixture.info.ID)
	if err != nil {
		t.Fatalf("read allocator-owned session info at pre-commit barrier: %v", err)
	}
	if preCommitInfo.InstanceToken == "" || preCommitInfo.InstanceToken == fixture.lease.InstanceToken {
		t.Fatalf("pre-commit instance token = %q, want a nonempty rotation from lease token %q", preCommitInfo.InstanceToken, fixture.lease.InstanceToken)
	}
	legacyExclusion := fixture.cr.sessionStartLegacyExclusionOption()
	if legacyExclusion == nil {
		t.Fatal("keyed pool allocation did not install legacy exclusions")
	}
	legacyOptions := startExecutionOptions{}
	legacyExclusion(&legacyOptions)
	if legacyOptions.legacyStartExcluded == nil || !legacyOptions.legacyStartExcluded(preCommitInfo) {
		t.Fatal("keyed pool allocation did not exclude legacy start after pre-wake token rotation")
	}
	if legacyOptions.legacyStatusHealExcluded == nil || !legacyOptions.legacyStatusHealExcluded(preCommitInfo) {
		t.Fatal("keyed pool allocation did not exclude legacy status heal after pre-wake token rotation")
	}

	legacy := newReconcilerTestEnv()
	legacy.store = fixture.store
	legacy.sp = fixture.provider
	legacy.cfg = fixture.snapshot.Config
	legacy.clk.Time = time.Now().UTC()
	legacy.addDesired(fixture.info.SessionName, fixture.lease.PoolTarget, false)
	legacy.startOptions = append(legacy.startOptions, legacyExclusion)
	if starts := legacy.reconcile([]beads.Bead{beforeLegacy}); starts != 0 {
		t.Fatalf("legacy wake attempts while keyed lease is in flight = %d, want 0", starts)
	}
	afterLegacy, err := fixture.store.Get(fixture.info.ID)
	if err != nil {
		t.Fatalf("read allocator-owned row after legacy pass: %v", err)
	}
	if !reflect.DeepEqual(afterLegacy, beforeLegacy) {
		t.Fatalf("legacy pass mutated live keyed row before durable commit:\nbefore=%+v\nafter=%+v", beforeLegacy, afterLegacy)
	}
	if got := fixture.provider.CountCalls("Start", fixture.info.SessionName); got != 1 {
		t.Fatalf("provider Start calls before keyed commit release = %d, want exactly 1", got)
	}

	releaseOnce.Do(func() { close(releaseCommit) })
	awaitCond(t, func() bool { return controller.Pending() == 0 }, "keyed pool-allocation start to settle")
	if got := fixture.provider.CountCalls("Start", fixture.info.SessionName); got != 1 {
		t.Fatalf("provider Start calls after keyed commit = %d, want exactly 1", got)
	}
	committed, err := fixture.store.Get(fixture.info.ID)
	if err != nil {
		t.Fatalf("read allocator-owned row after keyed commit: %v", err)
	}
	if committed.Revision <= beforeLegacy.Revision || committed.Metadata["state"] != string(sessionpkg.StateActive) || committed.Metadata["pending_create_claim"] != "" {
		t.Fatalf("keyed commit row = %+v, want newer active row with cleared pending-create claim", committed)
	}
}

func TestRoutedWorkPoolAllocationExhaustionReleasesLeaseForLegacyFallback(t *testing.T) {
	originalNewController := newCitySessionStartController
	newCitySessionStartController = func(opts sessionStartControllerOptions) (*sessionStartController, error) {
		reconcile := opts.Reconcile
		opts.MaxRetries = 0
		opts.Reconcile = func(ctx context.Context, admission sessionStartAdmission) error {
			if admission.PoolAllocation != nil {
				return errors.New("authoritative store unavailable")
			}
			return reconcile(ctx, admission)
		}
		return originalNewController(opts)
	}
	t.Cleanup(func() { newCitySessionStartController = originalNewController })

	fixture := newRoutedWorkPoolAllocationFixture(t, beads.NewMemStore())
	work, err := fixture.store.Create(beads.Bead{
		Title:    "ready routed work for exhausted exact start",
		Type:     "task",
		Status:   "open",
		Metadata: map[string]string{"gc.routed_to": "worker"},
	})
	if err != nil {
		t.Fatalf("create ready work: %v", err)
	}
	fixture.cr.handleRoutedWorkPoolAllocation(t.Context(), routedWorkPoolAllocationHint{
		WorkID: work.ID, PoolTarget: "worker", SourceStore: "city:test-city",
	})
	awaitCond(t, func() bool {
		return fixture.cr.sessionStartController.Pending() == 0 &&
			fixture.cr.readyRoutedWorkPokePending.Load() && len(fixture.cr.pokeCh) == 1
	}, "production observer to request priority legacy fallback")
	assertRoutedWorkPoolAllocationFallback(t, fixture.cr)

	snapshot, err := loadSessionBeadSnapshot(fixture.store)
	if err != nil {
		t.Fatalf("load exhausted allocator-owned pool row: %v", err)
	}
	open := snapshot.OpenInfos()
	if len(open) != 1 {
		t.Fatalf("open sessions after keyed exhaustion = %d, want 1", len(open))
	}
	info := open[0]
	if fixture.cr.sessionStartController.ownsPoolAllocationStart(info.ID, info.InstanceToken) {
		t.Fatal("exhausted pool-allocation lease still excludes legacy start")
	}

	legacy := newReconcilerTestEnv()
	legacy.store = fixture.store
	legacy.sp = fixture.provider
	legacy.cfg = fixture.cr.cfg
	legacy.clk.Time = time.Now().UTC()
	legacy.addDesired(info.SessionName, "worker", false)
	legacy.startOptions = append(legacy.startOptions, fixture.cr.sessionStartLegacyExclusionOption())
	row, err := fixture.store.Get(info.ID)
	if err != nil {
		t.Fatalf("read exhausted allocator-owned pool row: %v", err)
	}
	if starts := legacy.reconcile([]beads.Bead{row}); starts != 1 {
		t.Fatalf("legacy fallback wake attempts after keyed exhaustion = %d, want 1", starts)
	}
	if got := fixture.provider.CountCalls("Start", info.SessionName); got != 1 {
		t.Fatalf("provider Start calls after legacy fallback = %d, want exactly 1", got)
	}
}

func TestRoutedWorkPoolAllocationFallsBackWithoutCreatingOnUncertainty(t *testing.T) {
	t.Run("work became closed", func(t *testing.T) {
		fixture := newRoutedWorkPoolAllocationFixture(t, beads.NewMemStore())
		work, err := fixture.store.Create(beads.Bead{
			Title:    "stale routed work",
			Type:     "task",
			Status:   "open",
			Metadata: map[string]string{"gc.routed_to": "worker"},
		})
		if err != nil {
			t.Fatalf("create work: %v", err)
		}
		if err := fixture.store.Close(work.ID); err != nil {
			t.Fatalf("close work: %v", err)
		}

		fixture.cr.handleRoutedWorkPoolAllocation(t.Context(), routedWorkPoolAllocationHint{
			WorkID: work.ID, PoolTarget: "worker", SourceStore: "city:test-city",
		})

		assertRoutedWorkPoolAllocationFallback(t, fixture.cr)
		assertNoPoolAllocationSession(t, fixture.store)
	})

	t.Run("session create fails", func(t *testing.T) {
		store := &poolAllocationFailSessionCreateStore{Store: beads.NewMemStore()}
		fixture := newRoutedWorkPoolAllocationFixture(t, store)
		work, err := fixture.store.Create(beads.Bead{
			Title:    "ready routed work",
			Type:     "task",
			Status:   "open",
			Metadata: map[string]string{"gc.routed_to": "worker"},
		})
		if err != nil {
			t.Fatalf("create work: %v", err)
		}
		store.fail.Store(true)

		fixture.cr.handleRoutedWorkPoolAllocation(t.Context(), routedWorkPoolAllocationHint{
			WorkID: work.ID, PoolTarget: "worker", SourceStore: "city:test-city",
		})

		assertRoutedWorkPoolAllocationFallback(t, fixture.cr)
		assertNoPoolAllocationSession(t, fixture.store)
	})

	t.Run("exact admission fails after durable create", func(t *testing.T) {
		fixture := newRoutedWorkPoolAllocationFixture(t, beads.NewMemStore())
		work, err := fixture.store.Create(beads.Bead{
			Title:    "ready routed work",
			Type:     "task",
			Status:   "open",
			Metadata: map[string]string{"gc.routed_to": "worker"},
		})
		if err != nil {
			t.Fatalf("create work: %v", err)
		}
		fixture.cr.sessionStartController.Stop()

		fixture.cr.handleRoutedWorkPoolAllocation(t.Context(), routedWorkPoolAllocationHint{
			WorkID: work.ID, PoolTarget: "worker", SourceStore: "city:test-city",
		})

		assertRoutedWorkPoolAllocationFallback(t, fixture.cr)
		infos, err := loadSessionBeadSnapshot(fixture.store)
		if err != nil {
			t.Fatalf("load durable session after admission failure: %v", err)
		}
		if got := len(infos.OpenInfos()); got != 1 {
			t.Fatalf("open sessions after admission failure = %d, want durable binding retained", got)
		}
	})
}

func TestAdmitRoutedWorkPoolSessionReportsQueueOverflow(t *testing.T) {
	fixture := newRoutedWorkPoolAllocationFixture(t, beads.NewMemStore())
	controller := fixture.cr.sessionStartController
	controller.mu.Lock()
	controller.maxDistinct = 0
	controller.mu.Unlock()

	err := fixture.cr.admitRoutedWorkPoolSession(routedWorkPoolStartLease{
		SessionID:            "gcs-created",
		InstanceToken:        "instance-token",
		ControllerGeneration: 1,
		PoolTarget:           "worker",
		WorkID:               "ga-work",
		SourceStore:          "city:test-city",
		MembershipRevision:   1,
	})
	if err == nil || !strings.Contains(err.Error(), "queue overflow") {
		t.Fatalf("admit saturated exact-start controller error = %v, want queue overflow", err)
	}
	if !controller.TakeAuditRequest() {
		t.Fatal("queue overflow did not request an authoritative audit")
	}
}

func TestSessionStartAdmissionPreservesPoolAllocationLeaseAcrossGenericCoalescing(t *testing.T) {
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	controller, err := newSessionStartController(sessionStartControllerOptions{
		Workers:     1,
		MaxDistinct: 4,
		MaxRetries:  0,
		Reconcile: func(context.Context, sessionStartAdmission) error {
			select {
			case entered <- struct{}{}:
			default:
			}
			<-release
			return nil
		},
	})
	if err != nil {
		t.Fatalf("new exact-start controller: %v", err)
	}
	if err := controller.Start(t.Context()); err != nil {
		t.Fatalf("start exact-start controller: %v", err)
	}
	t.Cleanup(controller.Stop)
	defer close(release)

	lease := routedWorkPoolStartLease{
		SessionID:            "gcs-pool1",
		InstanceToken:        "instance-token",
		ControllerGeneration: 7,
		PoolTarget:           "worker",
		WorkID:               "ga-work",
		SourceStore:          "city:test-city",
		MembershipRevision:   11,
	}
	if outcome, err := controller.AdmitPoolAllocation(lease); err != nil || outcome != sessionStartAdmissionAccepted {
		t.Fatalf("admit pool allocation = (%q, %v), want accepted", outcome, err)
	}
	awaitClose(t, entered, "pool allocation reconcile to enter")
	if outcome, err := controller.Admit(lease.SessionID, sessionStartAdmissionInProcess); err != nil || outcome != sessionStartAdmissionCoalesced {
		t.Fatalf("coalesce generic event = (%q, %v), want coalesced", outcome, err)
	}

	admission, ok := controller.readAdmission(lease.SessionID)
	if !ok || admission.PoolAllocation == nil || *admission.PoolAllocation != lease {
		t.Fatalf("coalesced admission lease = %+v, want %+v", admission.PoolAllocation, lease)
	}
	if !admission.PoolStartEntered {
		t.Fatal("generic coalescing cleared entered pool-allocation ownership")
	}
}

func TestAuthorizeRoutedWorkPoolStartRejectsStaleLeaseAuthority(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*routedWorkPoolAuthorizationFixture)
	}{
		{
			name: "instance changed",
			mutate: func(f *routedWorkPoolAuthorizationFixture) {
				f.info.InstanceToken = "replacement-token"
			},
		},
		{
			name: "controller generation changed",
			mutate: func(f *routedWorkPoolAuthorizationFixture) {
				f.lease.ControllerGeneration++
			},
		},
		{
			name: "trigger work changed",
			mutate: func(f *routedWorkPoolAuthorizationFixture) {
				f.lease.WorkID = "gc-other-work"
			},
		},
		{
			name: "source store changed",
			mutate: func(f *routedWorkPoolAuthorizationFixture) {
				f.lease.SourceStore = "city:other"
			},
		},
		{
			name: "pool target changed",
			mutate: func(f *routedWorkPoolAuthorizationFixture) {
				f.lease.PoolTarget = "other"
			},
		},
		{
			name: "membership became uncertified",
			mutate: func(f *routedWorkPoolAuthorizationFixture) {
				f.cr.poolMembershipShadow.invalidate(poolMembershipUncertifiedSnapshotGap)
			},
		},
		{
			name: "allocated member disappeared",
			mutate: func(f *routedWorkPoolAuthorizationFixture) {
				f.cr.poolMembershipShadow.remove(f.info.ID)
			},
		},
		{
			name: "membership revision not observed",
			mutate: func(f *routedWorkPoolAuthorizationFixture) {
				f.lease.MembershipRevision++
			},
		},
		{
			name: "work no longer ready",
			mutate: func(f *routedWorkPoolAuthorizationFixture) {
				if err := f.store.Close(f.work.ID); err != nil {
					f.t.Fatalf("close routed work: %v", err)
				}
			},
		},
		{
			name: "config reloaded",
			mutate: func(f *routedWorkPoolAuthorizationFixture) {
				next := *f.snapshot.Config
				f.cr.cfg = &next
			},
		},
		{
			name: "canonical singleton rejects numbered allocation",
			mutate: func(f *routedWorkPoolAuthorizationFixture) {
				maximum := 1
				f.snapshot.Config.Agents[0].MaxActiveSessions = &maximum
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newRoutedWorkPoolAuthorizationFixture(t)
			test.mutate(&fixture)

			authorized, err := fixture.cr.authorizeRoutedWorkPoolStart(t.Context(), fixture.snapshot, fixture.info, fixture.lease)
			if err != nil {
				t.Fatalf("authorize stale lease: %v", err)
			}
			if authorized {
				t.Fatal("stale pool-allocation lease retained start authority")
			}
		})
	}
}

func TestAuthorizeRoutedWorkPoolStartRetainsExactMemberAuthorityAfterPoolGrowth(t *testing.T) {
	fixture := newRoutedWorkPoolAuthorizationFixture(t)
	other, err := createPoolSessionBeadWithAlias(fixture.store, "worker", fixture.snapshot.Config, nil, time.Now().UTC(), poolSessionCreateIdentity{
		AgentName: "worker-2",
		Slot:      2,
		Metadata: map[string]string{
			beadmeta.TriggerBeadIDMetadataKey:       "gc-other-work",
			beadmeta.TriggerBeadStoreRefMetadataKey: fixture.lease.SourceStore,
		},
	}, "")
	if err != nil {
		t.Fatalf("create second occupied pool session: %v", err)
	}
	if err := fixture.store.SetMetadata(other.ID, "state", string(sessionpkg.StateActive)); err != nil {
		t.Fatalf("make second pool session active: %v", err)
	}
	other, err = sessionFrontDoor(fixture.store).Get(other.ID)
	if err != nil {
		t.Fatalf("read second occupied pool session: %v", err)
	}
	if err := fixture.cr.poolMembershipShadow.replace(fixture.snapshot.Config, other); err != nil {
		t.Fatalf("publish second occupied pool session: %v", err)
	}

	observation := fixture.cr.poolMembershipShadow.observe("worker")
	if !observation.certified || observation.members != 2 || observation.occupied != 2 {
		t.Fatalf("grown pool membership = %+v, want two certified occupied members", observation)
	}
	authorized, err := fixture.cr.authorizeRoutedWorkPoolStart(t.Context(), fixture.snapshot, fixture.info, fixture.lease)
	if err != nil || !authorized {
		t.Fatalf("authorize original exact member after pool growth = (%t, %v), want true", authorized, err)
	}
}

func TestAuthorizeRoutedWorkPoolStartRejectsBoundedPoolGrowthPastCap(t *testing.T) {
	tests := []struct {
		name      string
		maximum   int
		occupancy int
	}{
		{name: "multi-session pool", maximum: 2, occupancy: 3},
		{name: "canonical singleton", maximum: 1, occupancy: 2},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newRoutedWorkPoolAuthorizationFixture(t)
			for slot := 2; slot <= test.occupancy; slot++ {
				other, err := createPoolSessionBeadWithAlias(fixture.store, "worker", fixture.snapshot.Config, nil, time.Now().UTC(), poolSessionCreateIdentity{
					AgentName: fmt.Sprintf("worker-%d", slot),
					Slot:      slot,
					Metadata: map[string]string{
						beadmeta.TriggerBeadIDMetadataKey:       fmt.Sprintf("gc-other-work-%d", slot),
						beadmeta.TriggerBeadStoreRefMetadataKey: fixture.lease.SourceStore,
					},
				}, "")
				if err != nil {
					t.Fatalf("create occupied pool session %d: %v", slot, err)
				}
				if err := fixture.store.SetMetadata(other.ID, "state", string(sessionpkg.StateActive)); err != nil {
					t.Fatalf("make pool session %d active: %v", slot, err)
				}
				other, err = sessionFrontDoor(fixture.store).Get(other.ID)
				if err != nil {
					t.Fatalf("read occupied pool session %d: %v", slot, err)
				}
				if err := fixture.cr.poolMembershipShadow.replace(fixture.snapshot.Config, other); err != nil {
					t.Fatalf("publish occupied pool session %d: %v", slot, err)
				}
			}

			observation := fixture.cr.poolMembershipShadow.observe("worker")
			if !observation.certified || observation.occupied != test.occupancy {
				t.Fatalf("grown bounded membership = %+v, want %d certified occupied members", observation, test.occupancy)
			}
			fixture.snapshot.Config.Agents[0].MaxActiveSessions = &test.maximum
			policy := newPoolAllocationShadowPolicy(fixture.snapshot.Config, &fixture.snapshot.Config.Agents[0], nil)
			if !policy.supported() {
				t.Fatalf("bounded policy = %+v, want supported start policy", policy)
			}
			authorized, err := fixture.cr.authorizeRoutedWorkPoolStart(t.Context(), fixture.snapshot, fixture.info, fixture.lease)
			if err != nil {
				t.Fatalf("authorize over-cap pool start: %v", err)
			}
			if authorized {
				t.Fatal("over-cap bounded pool retained exact start authority")
			}
		})
	}
}

func TestAuthorizeRoutedWorkPoolDrainAckRequiresExactLiveEvidence(t *testing.T) {
	tests := []struct {
		name      string
		mutate    func(*routedWorkPoolDrainAckAuthorizationFixture)
		wantError bool
	}{
		{
			name: "config generation changed",
			mutate: func(f *routedWorkPoolDrainAckAuthorizationFixture) {
				f.lease.ControllerGeneration++
			},
		},
		{
			name: "config instance changed",
			mutate: func(f *routedWorkPoolDrainAckAuthorizationFixture) {
				next := *f.snapshot.Config
				f.cr.cfg = &next
			},
		},
		{
			name: "durable instance token changed",
			mutate: func(f *routedWorkPoolDrainAckAuthorizationFixture) {
				f.info.InstanceToken = "replacement-token"
			},
		},
		{
			name: "durable trigger changed",
			mutate: func(f *routedWorkPoolDrainAckAuthorizationFixture) {
				f.info.TriggerBeadID = "ga-other-work"
			},
		},
		{
			name: "requester session changed",
			mutate: func(f *routedWorkPoolDrainAckAuthorizationFixture) {
				f.lease.RequesterSessionID = "gc-other-session"
			},
		},
		{
			name: "requester runtime token changed",
			mutate: func(f *routedWorkPoolDrainAckAuthorizationFixture) {
				if err := f.provider.SetMeta(f.info.SessionName, drainAckRequesterInstanceTokenKey, "replacement-token"); err != nil {
					f.t.Fatalf("change requester runtime token: %v", err)
				}
			},
		},
		{
			name:      "source store unavailable",
			wantError: true,
			mutate: func(f *routedWorkPoolDrainAckAuthorizationFixture) {
				f.info.TriggerBeadStoreRef = "city:missing"
				f.lease.SourceStore = "city:missing"
			},
		},
		{
			name: "runtime session id changed",
			mutate: func(f *routedWorkPoolDrainAckAuthorizationFixture) {
				if err := f.provider.SetMeta(f.info.SessionName, "GC_SESSION_ID", "gcs-other"); err != nil {
					f.t.Fatalf("change runtime session id: %v", err)
				}
			},
		},
		{
			name: "runtime token changed",
			mutate: func(f *routedWorkPoolDrainAckAuthorizationFixture) {
				if err := f.provider.SetMeta(f.info.SessionName, "GC_INSTANCE_TOKEN", "replacement-token"); err != nil {
					f.t.Fatalf("change runtime token: %v", err)
				}
			},
		},
		{
			name: "ack source is not agent",
			mutate: func(f *routedWorkPoolDrainAckAuthorizationFixture) {
				if err := f.provider.SetMeta(f.info.SessionName, reconcilerDrainAckSourceKey, reconcilerDrainAckSourceValue); err != nil {
					f.t.Fatalf("change acknowledgement source: %v", err)
				}
			},
		},
		{
			name: "ack bit cleared",
			mutate: func(f *routedWorkPoolDrainAckAuthorizationFixture) {
				if err := f.provider.RemoveMeta(f.info.SessionName, "GC_DRAIN_ACK"); err != nil {
					f.t.Fatalf("clear acknowledgement: %v", err)
				}
			},
		},
		{
			name: "pending interaction",
			mutate: func(f *routedWorkPoolDrainAckAuthorizationFixture) {
				f.provider.SetPendingInteraction(f.info.SessionName, &runtime.PendingInteraction{RequestID: "approval-1"})
			},
		},
		{
			name:      "provider cannot prove pending interaction",
			wantError: true,
			mutate: func(f *routedWorkPoolDrainAckAuthorizationFixture) {
				f.snapshot.Provider = poolDrainAckProviderWithoutInteraction{Provider: f.provider}
			},
		},
		{
			name:      "runtime metadata read failed",
			wantError: true,
			mutate: func(f *routedWorkPoolDrainAckAuthorizationFixture) {
				f.snapshot.Provider = runtime.NewFailFake()
			},
		},
		{
			name: "trigger work reopened",
			mutate: func(f *routedWorkPoolDrainAckAuthorizationFixture) {
				status := "open"
				if err := f.store.Update(f.work.ID, beads.UpdateOpts{Status: &status}); err != nil {
					f.t.Fatalf("reopen trigger work: %v", err)
				}
			},
		},
		{
			name:      "trigger work disappeared",
			wantError: true,
			mutate: func(f *routedWorkPoolDrainAckAuthorizationFixture) {
				f.info.TriggerBeadID = "ga-missing-work"
				f.lease.WorkID = "ga-missing-work"
			},
		},
		{
			name: "other awake assigned work",
			mutate: func(f *routedWorkPoolDrainAckAuthorizationFixture) {
				if _, err := f.store.Create(beads.Bead{
					Title:    "new assigned work",
					Type:     "task",
					Status:   "in_progress",
					Assignee: f.info.SessionName,
				}); err != nil {
					f.t.Fatalf("create assigned work: %v", err)
				}
			},
		},
		{
			name: "unsupported pool policy",
			mutate: func(f *routedWorkPoolDrainAckAuthorizationFixture) {
				f.snapshot.Config.Agents[0].DependsOn = []string{"database"}
			},
		},
		{
			name: "numbered singleton stop remains legacy owned",
			mutate: func(f *routedWorkPoolDrainAckAuthorizationFixture) {
				maximum := 1
				f.snapshot.Config.Agents[0].MaxActiveSessions = &maximum
			},
		},
		{
			name: "membership lost exact member",
			mutate: func(f *routedWorkPoolDrainAckAuthorizationFixture) {
				f.cr.poolMembershipShadow.remove(f.info.ID)
			},
		},
		{
			name: "membership revision not observed",
			mutate: func(f *routedWorkPoolDrainAckAuthorizationFixture) {
				f.lease.MembershipRevision++
			},
		},
		{
			name: "session already closed",
			mutate: func(f *routedWorkPoolDrainAckAuthorizationFixture) {
				f.info.Closed = true
			},
		},
		{
			name: "unsupported pre-CAS lifecycle shape",
			mutate: func(f *routedWorkPoolDrainAckAuthorizationFixture) {
				f.info.MetadataState = string(sessionpkg.StateAsleep)
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newRoutedWorkPoolDrainAckAuthorizationFixture(t)
			test.mutate(&fixture)
			before, err := fixture.store.Get(fixture.info.ID)
			if err != nil {
				t.Fatalf("read row before authorization: %v", err)
			}

			authorized, authorizeErr := fixture.cr.authorizeRoutedWorkPoolDrainAck(fixture.snapshot, fixture.info, fixture.lease)
			if authorized {
				t.Fatal("stale or unsafe drain acknowledgement retained stop authority")
			}
			if (authorizeErr != nil) != test.wantError {
				t.Fatalf("authorization error = %v, wantError=%t", authorizeErr, test.wantError)
			}
			after, err := fixture.store.Get(fixture.info.ID)
			if err != nil {
				t.Fatalf("read row after authorization: %v", err)
			}
			if !reflect.DeepEqual(after, before) {
				t.Fatalf("authorization mutated durable row:\nbefore=%+v\nafter=%+v", before, after)
			}
			if got := fixture.provider.CountCalls("Stop", fixture.info.SessionName); got != 0 {
				t.Fatalf("provider Stop calls = %d, want 0", got)
			}
		})
	}
}

func TestAuthorizeRoutedWorkPoolDrainAckAcceptsExactStopPendingRow(t *testing.T) {
	fixture := newRoutedWorkPoolDrainAckAuthorizationFixture(t)
	if err := fixture.store.SetMetadataBatch(fixture.info.ID, sessionpkg.DrainAckStopPendingPatch(time.Now().UTC())); err != nil {
		t.Fatalf("mark drain acknowledgement stop-pending: %v", err)
	}
	info, err := sessionFrontDoor(fixture.store).Get(fixture.info.ID)
	if err != nil {
		t.Fatalf("read stop-pending pool session: %v", err)
	}
	if !isDrainAckStopPendingInfo(info) {
		t.Fatal("fixture did not enter drain-ack stop-pending")
	}

	authorized, err := fixture.cr.authorizeRoutedWorkPoolDrainAck(fixture.snapshot, info, fixture.lease)
	if err != nil || !authorized {
		t.Fatalf("authorize exact stop-pending drain acknowledgement = (%t, %v), want true", authorized, err)
	}
}

func TestRecoverRoutedWorkPoolDrainAckLeaseDistinguishesLegacyFromUnknownProvenance(t *testing.T) {
	fixture := newRoutedWorkPoolDrainAckAuthorizationFixture(t)
	if err := fixture.provider.SetMeta(fixture.info.SessionName, reconcilerDrainAckSourceKey, reconcilerDrainAckSourceValue); err != nil {
		t.Fatalf("set legacy drain acknowledgement source: %v", err)
	}
	_, agent, legacy, err := fixture.cr.recoverRoutedWorkPoolDrainAckLease(fixture.snapshot, fixture.info)
	if err != nil || agent || !legacy {
		t.Fatalf("recover legacy provenance = (agent=%t, legacy=%t, err=%v), want false/true/nil", agent, legacy, err)
	}
	if err := fixture.provider.RemoveMeta(fixture.info.SessionName, reconcilerDrainAckSourceKey); err != nil {
		t.Fatalf("remove drain acknowledgement source: %v", err)
	}
	_, agent, legacy, err = fixture.cr.recoverRoutedWorkPoolDrainAckLease(fixture.snapshot, fixture.info)
	if err == nil || agent || legacy {
		t.Fatalf("recover unknown provenance = (agent=%t, legacy=%t, err=%v), want false/false/error", agent, legacy, err)
	}
}

func TestRecoverRoutedWorkPoolDrainAckLeaseRejectsUnadmittedAgentAcknowledgement(t *testing.T) {
	fixture := newRoutedWorkPoolDrainAckAuthorizationFixture(t)
	fixture.cr.poolMembershipShadow.remove(fixture.info.ID)
	lease, agent, legacy, err := fixture.cr.recoverRoutedWorkPoolDrainAckLease(fixture.snapshot, fixture.info)
	if err == nil || agent || legacy || lease != (routedWorkPoolDrainAckLease{}) {
		t.Fatalf("recover unadmitted agent acknowledgement = (%+v, agent=%t, legacy=%t, err=%v), want zero/false/false/error", lease, agent, legacy, err)
	}
}

func TestNewRoutedWorkPoolDrainAckLeaseDistinguishesAgentAckFromOrdinaryStart(t *testing.T) {
	t.Run("certified agent acknowledgement", func(t *testing.T) {
		fixture := newRoutedWorkPoolDrainAckAuthorizationFixture(t)
		lease, agentAck, err := fixture.cr.newRoutedWorkPoolDrainAckLease(fixture.snapshot, fixture.info)
		if err != nil || !agentAck || lease != fixture.lease {
			t.Fatalf("new drain acknowledgement lease = (%+v, %t, %v), want %+v", lease, agentAck, err, fixture.lease)
		}
	})

	t.Run("admission does not reread work stores", func(t *testing.T) {
		fixture := newRoutedWorkPoolDrainAckAuthorizationFixture(t)
		store := &poolDrainAckAdmissionReadRejectStore{Store: fixture.store}
		fixture.snapshot.Store = store
		fixture.cr.cs.cityBeadStore = store
		lease, agentAck, err := fixture.cr.newRoutedWorkPoolDrainAckLease(fixture.snapshot, fixture.info)
		if err != nil || !agentAck || lease != fixture.lease {
			t.Fatalf("new drain acknowledgement lease = (%+v, %t, %v), want cheap lease %+v", lease, agentAck, err, fixture.lease)
		}
		if store.reads != 0 {
			t.Fatalf("admission store reads = %d, want 0 before effect-time authorization", store.reads)
		}
	})

	t.Run("ordinary live session", func(t *testing.T) {
		fixture := newRoutedWorkPoolDrainAckAuthorizationFixture(t)
		if err := fixture.provider.RemoveMeta(fixture.info.SessionName, reconcilerDrainAckSourceKey); err != nil {
			t.Fatalf("clear acknowledgement source: %v", err)
		}
		lease, agentAck, err := fixture.cr.newRoutedWorkPoolDrainAckLease(fixture.snapshot, fixture.info)
		if err != nil || agentAck || lease != (routedWorkPoolDrainAckLease{}) {
			t.Fatalf("ordinary session drain lease = (%+v, %t, %v), want no acknowledgement", lease, agentAck, err)
		}
	})

	t.Run("ordinary live session without membership index", func(t *testing.T) {
		fixture := newRoutedWorkPoolDrainAckAuthorizationFixture(t)
		if err := fixture.provider.RemoveMeta(fixture.info.SessionName, reconcilerDrainAckSourceKey); err != nil {
			t.Fatalf("clear acknowledgement source: %v", err)
		}
		fixture.cr.poolMembershipShadow = nil
		lease, agentAck, err := fixture.cr.newRoutedWorkPoolDrainAckLease(fixture.snapshot, fixture.info)
		if err != nil || agentAck || lease != (routedWorkPoolDrainAckLease{}) {
			t.Fatalf("ordinary session drain lease = (%+v, %t, %v), want no acknowledgement", lease, agentAck, err)
		}
	})

	t.Run("agent acknowledgement without membership index", func(t *testing.T) {
		fixture := newRoutedWorkPoolDrainAckAuthorizationFixture(t)
		fixture.cr.poolMembershipShadow = nil
		lease, agentAck, err := fixture.cr.newRoutedWorkPoolDrainAckLease(fixture.snapshot, fixture.info)
		if err == nil || !agentAck || lease != (routedWorkPoolDrainAckLease{}) || !strings.Contains(err.Error(), "keyed state is unavailable") {
			t.Fatalf("uncertain drain lease = (%+v, %t, %v), want visible acknowledged uncertainty", lease, agentAck, err)
		}
	})

	t.Run("agent acknowledgement without requester binding", func(t *testing.T) {
		fixture := newRoutedWorkPoolDrainAckAuthorizationFixture(t)
		if err := fixture.provider.RemoveMeta(fixture.info.SessionName, drainAckRequesterInstanceTokenKey); err != nil {
			t.Fatalf("clear acknowledgement requester token: %v", err)
		}
		lease, agentAck, err := fixture.cr.newRoutedWorkPoolDrainAckLease(fixture.snapshot, fixture.info)
		if err != nil || !agentAck || lease != (routedWorkPoolDrainAckLease{}) {
			t.Fatalf("unbound drain lease = (%+v, %t, %v), want acknowledged but unadmitted", lease, agentAck, err)
		}
	})

	t.Run("uncertified occupied member", func(t *testing.T) {
		fixture := newRoutedWorkPoolDrainAckAuthorizationFixture(t)
		fixture.cr.poolMembershipShadow.remove(fixture.info.ID)
		lease, agentAck, err := fixture.cr.newRoutedWorkPoolDrainAckLease(fixture.snapshot, fixture.info)
		if err != nil || !agentAck || lease != (routedWorkPoolDrainAckLease{}) {
			t.Fatalf("uncertified drain lease = (%+v, %t, %v), want acknowledged but unadmitted", lease, agentAck, err)
		}
	})

	t.Run("running provider metadata uncertainty", func(t *testing.T) {
		fixture := newRoutedWorkPoolDrainAckAuthorizationFixture(t)
		fixture.snapshot.Provider = poolDrainAckGetMetaErrorProvider{
			Provider: fixture.provider,
			err:      errors.New("runtime metadata unavailable"),
		}
		lease, agentAck, err := fixture.cr.newRoutedWorkPoolDrainAckLease(fixture.snapshot, fixture.info)
		if err == nil || !agentAck || lease != (routedWorkPoolDrainAckLease{}) || !strings.Contains(err.Error(), "runtime metadata unavailable") {
			t.Fatalf("uncertain drain lease = (%+v, %t, %v), want visible acknowledged uncertainty", lease, agentAck, err)
		}
	})
}

func TestCityRuntimeSocketReportsDrainAckAdmissionUncertainty(t *testing.T) {
	fixture := newRoutedWorkPoolDrainAckAuthorizationFixture(t)
	readErr := errors.New("runtime metadata unavailable")
	fixture.cr.cs.mu.Lock()
	fixture.cr.cs.sp = poolDrainAckGetMetaErrorProvider{Provider: fixture.provider, err: readErr}
	fixture.cr.cs.mu.Unlock()
	controller, err := newSessionStartController(sessionStartControllerOptions{
		Workers:     1,
		MaxDistinct: 1,
		MaxRetries:  0,
		Reconcile:   func(context.Context, sessionStartAdmission) error { return nil },
	})
	if err != nil {
		t.Fatalf("create exact controller: %v", err)
	}
	t.Cleanup(controller.Stop)
	var stderr bytes.Buffer
	fixture.cr.stderr = &stderr
	fixture.cr.sessionStartController = controller
	fixture.cr.sessionStartOwnership = sessionStartOwnershipKeyed

	if reply := fixture.cr.admitSessionStartSocketKey(fixture.info.ID); reply != sessionStartSocketReplyFallback {
		t.Fatalf("socket reply = %q, want fallback", reply)
	}
	if !strings.Contains(stderr.String(), readErr.Error()) {
		t.Fatalf("socket fallback diagnostic = %q, want %q", stderr.String(), readErr)
	}
}

func TestCityRuntimeSocketRequireRefusesDrainAckAdmissionUncertainty(t *testing.T) {
	fixture := newRoutedWorkPoolDrainAckAuthorizationFixture(t)
	readErr := errors.New("runtime metadata unavailable")
	fixture.cr.cs.mu.Lock()
	fixture.cr.cs.sp = poolDrainAckGetMetaErrorProvider{Provider: fixture.provider, err: readErr}
	fixture.cr.cs.mu.Unlock()
	controller, err := newSessionStartController(sessionStartControllerOptions{
		Workers:     1,
		MaxDistinct: 1,
		MaxRetries:  0,
		Reconcile:   func(context.Context, sessionStartAdmission) error { return nil },
	})
	if err != nil {
		t.Fatalf("create exact controller: %v", err)
	}
	t.Cleanup(controller.Stop)
	var stderr bytes.Buffer
	fixture.cr.stderr = &stderr
	fixture.cr.sessionStartController = controller
	fixture.cr.sessionStartOwnership = sessionStartOwnershipKeyed
	fixture.cr.sessionStartMode = rollout.Require

	if reply := fixture.cr.admitSessionStartSocketKey(fixture.info.ID); reply != sessionStartSocketReplyBlocked {
		t.Fatalf("socket reply = %q, want blocked", reply)
	}
	if !strings.Contains(stderr.String(), readErr.Error()) {
		t.Fatalf("socket refusal diagnostic = %q, want %q", stderr.String(), readErr)
	}
}

func TestSessionStartLegacyExclusionRequireRetainsAgentDrainAckAfterAdmissionEnds(t *testing.T) {
	fixture := newRoutedWorkPoolDrainAckAuthorizationFixture(t)
	fixture.cr.sessionStartOwnership = sessionStartOwnershipKeyed
	fixture.cr.sessionStartMode = rollout.Require
	fixture.cr.sessionStartController = nil
	excluded := fixture.cr.sessionStartLegacyExclusionPredicate()
	if excluded == nil || !excluded(fixture.info) {
		t.Fatal("require mode allowed legacy drain-ack entry after exact admission ended")
	}
}

func TestSessionStartLegacyExclusionLeavesConfirmedLegacyDrainAckOwned(t *testing.T) {
	fixture := newRoutedWorkPoolDrainAckAuthorizationFixture(t)
	if err := fixture.store.SetMetadataBatch(fixture.info.ID, sessionpkg.DrainAckStopPendingPatch(time.Now().UTC())); err != nil {
		t.Fatalf("mark legacy drain acknowledgement stop-pending: %v", err)
	}
	if err := fixture.provider.SetMeta(fixture.info.SessionName, reconcilerDrainAckSourceKey, reconcilerDrainAckSourceValue); err != nil {
		t.Fatalf("mark reconciler-authored acknowledgement: %v", err)
	}
	info, err := sessionFrontDoor(fixture.store).Get(fixture.info.ID)
	if err != nil {
		t.Fatalf("read legacy stop-pending session: %v", err)
	}
	fixture.cr.sessionStartOwnership = sessionStartOwnershipKeyed
	fixture.cr.sessionStartController = nil
	for _, mode := range []rollout.Mode{rollout.Auto, rollout.Require} {
		fixture.cr.sessionStartMode = mode
		excluded := fixture.cr.sessionStartLegacyExclusionPredicate()
		if excluded == nil || excluded(info) {
			t.Fatalf("%s mode excluded a confirmed reconciler-authored stop-pending row", mode)
		}
	}
}

func TestReconcileExactSessionStartKeepsOrdinaryPoolRowsLegacyOwned(t *testing.T) {
	fixture := newRoutedWorkPoolAuthorizationFixture(t)

	owner, err := reconcileExactSessionStartWithOwner(t.Context(), sessionStartAdmission{
		SessionID: fixture.info.ID,
		Source:    sessionStartAdmissionInProcess,
	}, exactSessionStartParams{
		Generation: fixture.snapshot.Generation,
		CityPath:   fixture.snapshot.CityPath,
		CityName:   fixture.snapshot.CityName,
		Config:     fixture.snapshot.Config,
		Provider:   fixture.snapshot.Provider,
		Store:      fixture.snapshot.Store,
		Recorder:   events.Discard,
		Stdout:     io.Discard,
		Stderr:     io.Discard,
	})
	if err != nil {
		t.Fatalf("reconcile ordinary pool row: %v", err)
	}
	if owner != exactSessionStartLegacyOwner {
		t.Fatalf("ordinary pool row owner = %v, want legacy", owner)
	}
	if fixture.provider.IsRunning(fixture.info.SessionName) {
		t.Fatal("ordinary pool row started without allocation authority")
	}
	current, _, err := sessionFrontDoor(fixture.store).GetPersistedResponse(fixture.info.ID)
	if err != nil {
		t.Fatalf("read ordinary pool row: %v", err)
	}
	if current.LastWokeAt != "" || current.InstanceToken != fixture.info.InstanceToken {
		t.Fatalf("ordinary pool row mutated without authority: before=%+v after=%+v", fixture.info, current)
	}
}

type routedWorkPoolAuthorizationFixture struct {
	t        *testing.T
	cr       *CityRuntime
	store    beads.Store
	provider *runtime.Fake
	snapshot controllerSessionStartSnapshot
	work     beads.Bead
	info     sessionpkg.Info
	lease    routedWorkPoolStartLease
}

type routedWorkPoolDrainAckAuthorizationFixture struct {
	*routedWorkPoolAuthorizationFixture
	lease routedWorkPoolDrainAckLease
}

type poolDrainAckProviderWithoutInteraction struct {
	runtime.Provider
}

type poolDrainAckGetMetaErrorProvider struct {
	runtime.Provider
	err error
}

type poolDrainAckAdmissionReadRejectStore struct {
	beads.Store
	reads int
}

func (s *poolDrainAckAdmissionReadRejectStore) Get(string) (beads.Bead, error) {
	s.reads++
	return beads.Bead{}, errors.New("work-store read attempted during admission")
}

func (s *poolDrainAckAdmissionReadRejectStore) List(beads.ListQuery) ([]beads.Bead, error) {
	s.reads++
	return nil, errors.New("work-store list attempted during admission")
}

func (s *poolDrainAckAdmissionReadRejectStore) Ready(...beads.ReadyQuery) ([]beads.Bead, error) {
	s.reads++
	return nil, errors.New("work-store ready scan attempted during admission")
}

func (s *poolDrainAckAdmissionReadRejectStore) DepList(string, string) ([]beads.Dep, error) {
	s.reads++
	return nil, errors.New("work-store dependency scan attempted during admission")
}

func (s *poolDrainAckAdmissionReadRejectStore) Handles() beads.StoreHandles {
	return beads.StoreHandles{Cached: s, Live: s, Writer: s.Store}
}

func (p poolDrainAckGetMetaErrorProvider) GetMeta(string, string) (string, error) {
	return "", p.err
}

func newRoutedWorkPoolDrainAckAuthorizationFixture(t *testing.T) routedWorkPoolDrainAckAuthorizationFixture {
	t.Helper()
	base := newRoutedWorkPoolAuthorizationFixtureWithStore(t, beads.NewAtomicCloseMemStore())
	if err := base.provider.Start(t.Context(), base.info.SessionName, runtime.Config{Command: "true"}); err != nil {
		t.Fatalf("start pool runtime: %v", err)
	}
	for key, value := range map[string]string{
		"GC_SESSION_ID":                   base.info.ID,
		"GC_INSTANCE_TOKEN":               base.info.InstanceToken,
		reconcilerDrainAckSourceKey:       drainAckSourceAgentValue,
		drainAckRequesterSessionIDKey:     base.info.ID,
		drainAckRequesterInstanceTokenKey: base.info.InstanceToken,
		"GC_DRAIN_ACK":                    "1",
	} {
		if err := base.provider.SetMeta(base.info.SessionName, key, value); err != nil {
			t.Fatalf("set runtime metadata %s: %v", key, err)
		}
	}
	if err := base.store.SetMetadataBatch(base.info.ID, map[string]string{
		"state":                     string(sessionpkg.StateActive),
		"pending_create_claim":      "",
		"pending_create_started_at": "",
	}); err != nil {
		t.Fatalf("mark pool session active: %v", err)
	}
	if err := base.store.Close(base.work.ID); err != nil {
		t.Fatalf("close trigger work: %v", err)
	}
	info, err := sessionFrontDoor(base.store).Get(base.info.ID)
	if err != nil {
		t.Fatalf("read active pool session: %v", err)
	}
	base.info = info
	if err := base.cr.poolMembershipShadow.replace(base.snapshot.Config, info); err != nil {
		t.Fatalf("publish active pool membership: %v", err)
	}
	observation, occupied := base.cr.poolMembershipShadow.observeOccupiedMember("worker", info.ID)
	if !occupied {
		t.Fatal("active pool session is not an occupied member")
	}
	lease := routedWorkPoolDrainAckLease{
		SessionID:              info.ID,
		InstanceToken:          info.InstanceToken,
		RequesterSessionID:     info.ID,
		RequesterInstanceToken: info.InstanceToken,
		ControllerGeneration:   base.snapshot.Generation,
		PoolTarget:             "worker",
		WorkID:                 base.work.ID,
		SourceStore:            "city:test-city",
		MembershipRevision:     observation.revision,
	}
	authorized, err := base.cr.authorizeRoutedWorkPoolDrainAck(base.snapshot, info, lease)
	if err != nil || !authorized {
		t.Fatalf("baseline drain acknowledgement authorization = (%t, %v), want true", authorized, err)
	}
	enableDrainAckAtomicCloseForFixture(&base)
	return routedWorkPoolDrainAckAuthorizationFixture{
		routedWorkPoolAuthorizationFixture: &base,
		lease:                              lease,
	}
}

func newRoutedWorkPoolAuthorizationFixture(t *testing.T) routedWorkPoolAuthorizationFixture {
	return newRoutedWorkPoolAuthorizationFixtureWithStore(t, beads.NewMemStore())
}

func newRoutedWorkPoolAuthorizationFixtureWithStore(t *testing.T, store beads.Store) routedWorkPoolAuthorizationFixture {
	t.Helper()
	unlimited := -1
	cityPath := t.TempDir()
	cfg := &config.City{
		Workspace: config.Workspace{Name: "test-city", Prefix: "hq"},
		Agents: []config.Agent{{
			Name:              "worker",
			StartCommand:      "true",
			MaxActiveSessions: &unlimited,
		}},
	}
	provider := runtime.NewFake()
	cs := coherentSessionStartControllerStateForTest(cfg, provider, store, rollout.Auto)
	cs.cityPath = cityPath
	cs.cityName = "test-city"
	cr := &CityRuntime{
		cityPath:             cityPath,
		cityName:             "test-city",
		cfg:                  cfg,
		sp:                   provider,
		cs:                   cs,
		rec:                  events.Discard,
		poolMembershipShadow: newPoolMembershipIndex(),
		stdout:               io.Discard,
		stderr:               io.Discard,
	}
	if !cr.poolMembershipShadow.publishRebuild(0, newPoolMembershipState()) {
		t.Fatal("publish empty pool membership")
	}
	work, err := store.Create(beads.Bead{
		Title:  "ready routed work",
		Type:   "task",
		Status: "open",
		Metadata: map[string]string{
			"gc.routed_to": "worker",
		},
	})
	if err != nil {
		t.Fatalf("create routed work: %v", err)
	}
	hint := routedWorkPoolAllocationHint{
		WorkID:      work.ID,
		PoolTarget:  "worker",
		SourceStore: "city:test-city",
	}
	info, err := createPoolSessionBeadWithAlias(store, "worker", cfg, nil, time.Now().UTC(), poolSessionCreateIdentity{
		AgentName: "worker-1",
		Slot:      1,
		Metadata: map[string]string{
			beadmeta.TriggerBeadIDMetadataKey:       work.ID,
			beadmeta.TriggerBeadStoreRefMetadataKey: hint.SourceStore,
		},
	}, "")
	if err != nil {
		t.Fatalf("create pool session: %v", err)
	}
	if err := cr.poolMembershipShadow.replace(cfg, info); err != nil {
		t.Fatalf("publish pool session membership: %v", err)
	}
	snapshot, release, err := cs.acquireSessionStartSnapshot()
	if err != nil {
		t.Fatalf("acquire start snapshot: %v", err)
	}
	t.Cleanup(release)
	lease, err := cr.newRoutedWorkPoolStartLease(snapshot, info, hint)
	if err != nil {
		t.Fatalf("create pool start lease: %v", err)
	}
	authorized, err := cr.authorizeRoutedWorkPoolStart(t.Context(), snapshot, info, lease)
	if err != nil || !authorized {
		t.Fatalf("baseline pool start authorization = (%t, %v), want true", authorized, err)
	}
	return routedWorkPoolAuthorizationFixture{
		t: t, cr: cr, store: store, provider: provider, snapshot: snapshot,
		work: work, info: info, lease: lease,
	}
}

type routedWorkPoolAllocationFixture struct {
	cr       *CityRuntime
	store    beads.Store
	provider *runtime.Fake
	stderr   *bytes.Buffer
}

type triggerMatchingReadHookStore struct {
	beads.Store
	sessionID string
	workID    string
	after     func()
	once      sync.Once
}

func (s *triggerMatchingReadHookStore) Get(id string) (beads.Bead, error) {
	row, err := s.Store.Get(id)
	if err == nil && id == s.sessionID && row.Metadata[beadmeta.TriggerBeadIDMetadataKey] == s.workID {
		s.once.Do(s.after)
	}
	return row, err
}

func (s *triggerMatchingReadHookStore) ConditionalWritesResolveTarget() beads.Store {
	return s.Store
}

func newRoutedWorkPoolAllocationFixture(t *testing.T, store beads.Store) routedWorkPoolAllocationFixture {
	t.Helper()
	unlimited := -1
	cityPath := t.TempDir()
	cfg := &config.City{
		Workspace: config.Workspace{Name: "test-city", Prefix: "hq"},
		Agents: []config.Agent{{
			Name:              "worker",
			StartCommand:      "true",
			MaxActiveSessions: &unlimited,
		}},
	}
	provider := runtime.NewFake()
	sessionProvider := &sequenceGetMetaProvider{Fake: provider}
	stderr := &bytes.Buffer{}
	cs := coherentSessionStartControllerStateForTest(cfg, sessionProvider, store, rollout.Auto)
	cs.cityPath = cityPath
	cs.cityName = "test-city"
	cr := &CityRuntime{
		cityPath:             cityPath,
		cityName:             "test-city",
		cfg:                  cfg,
		sp:                   sessionProvider,
		cs:                   cs,
		rec:                  events.Discard,
		poolMembershipShadow: newPoolMembershipIndex(),
		pokeCh:               make(chan struct{}, 1),
		sessionStartOptions: []startExecutionOption{
			withStartStabilityWaiter(immediateStartStabilityWaiter),
		},
		stdout: io.Discard,
		stderr: stderr,
	}
	if !cr.poolMembershipShadow.publishRebuild(0, newPoolMembershipState()) {
		t.Fatal("publish empty pool membership")
	}
	if err := cr.ensureSessionStartController(context.Background(), newSessionBeadSnapshotFromInfos(nil)); err != nil {
		t.Fatalf("ensure exact-start controller: %v", err)
	}
	t.Cleanup(cr.stopSessionStartController)
	return routedWorkPoolAllocationFixture{cr: cr, store: store, provider: provider, stderr: stderr}
}

func prepareIdleCanonicalSingletonForReuse(t *testing.T, conditional bool) (routedWorkPoolAllocationFixture, beads.Bead, sessionpkg.Info) {
	return prepareIdleGenericPoolMemberForReuse(t, conditional, 1)
}

func prepareIdleGenericPoolMemberForReuse(t *testing.T, conditional bool, maximum int) (routedWorkPoolAllocationFixture, beads.Bead, sessionpkg.Info) {
	t.Helper()
	var store beads.Store = beads.NewMemStore()
	if conditional {
		opened, err := beads.OpenStoreAtForCity(t.Context(), beads.StoreOpenOptions{
			Provider:          "file",
			OpenFileStore:     func() (beads.Store, error) { return beads.NewMemStore(), nil },
			ConditionalWrites: gate.Auto,
		})
		if err != nil {
			t.Fatalf("open conditional-write singleton store: %v", err)
		}
		store = opened.Store
	}
	fixture := newRoutedWorkPoolAllocationFixture(t, store)
	fixture.cr.cfg.Agents[0].MaxActiveSessions = &maximum
	fixture.cr.cfg.Agents[0].Provider = "claude"
	fixture.cr.cfg.Agents[0].Nudge = "Run gc hook --claim --json now."
	firstWork, err := fixture.store.Create(beads.Bead{
		Title:    "first singleton work",
		Type:     "task",
		Status:   "open",
		Metadata: map[string]string{"gc.routed_to": "worker"},
	})
	if err != nil {
		t.Fatalf("create first singleton work: %v", err)
	}
	first, err := fixture.cr.reconcileRoutedWorkPoolAllocation(t.Context(), routedWorkPoolAllocationHint{
		WorkID: firstWork.ID, PoolTarget: "worker", SourceStore: "city:test-city",
	})
	if err != nil || !first.Handled || !first.Created {
		t.Fatalf("allocate cold singleton = (%+v, %v), want one create", first, err)
	}
	awaitCond(t, func() bool {
		return fixture.provider.IsRunning(first.Session.SessionName) && fixture.cr.sessionStartController.Pending() == 0
	}, "cold singleton to start before reuse refusal")
	info, err := sessionFrontDoor(fixture.store).Get(first.Session.ID)
	if err != nil {
		t.Fatalf("read active singleton: %v", err)
	}
	setRoutedWorkPoolRuntimeIdentity(t, fixture, info)
	fixture.provider.WaitForIdleErrors[info.SessionNameMetadata] = nil
	return fixture, firstWork, info
}

func setRoutedWorkPoolRuntimeIdentity(t testing.TB, fixture routedWorkPoolAllocationFixture, info sessionpkg.Info) {
	t.Helper()
	// The wait-idle worker path resolves its provider family from the durable
	// session record. Production rows have this after provider resolution; the
	// direct keyed-start fixture bypasses that legacy metadata-refresh pass.
	if err := fixture.store.SetMetadata(info.ID, "provider_kind", "claude"); err != nil {
		t.Fatalf("stamp singleton provider family: %v", err)
	}
	for key, value := range map[string]string{
		"GC_SESSION_ID":     info.ID,
		"GC_INSTANCE_TOKEN": info.InstanceToken,
	} {
		if err := fixture.provider.SetMeta(info.SessionNameMetadata, key, value); err != nil {
			t.Fatalf("set singleton runtime metadata %s: %v", key, err)
		}
	}
}

func assertRoutedWorkPoolAllocationFallback(t *testing.T, cr *CityRuntime) {
	t.Helper()
	if !cr.readyRoutedWorkPokePending.Load() || len(cr.pokeCh) != 1 {
		t.Fatalf("legacy fallback = (pending=%t, pokes=%d), want one priority poke", cr.readyRoutedWorkPokePending.Load(), len(cr.pokeCh))
	}
}

func providerCallCount(provider *runtime.Fake, method string) int {
	count := 0
	for _, call := range provider.SnapshotCalls() {
		if call.Method == method {
			count++
		}
	}
	return count
}

func providerNudgeCalls(provider *runtime.Fake, name string) int {
	return provider.CountCalls("Nudge", name) + provider.CountCalls("NudgeNow", name)
}

func assertExactProviderNudgeSince(t *testing.T, provider *runtime.Fake, baseline int, name, message string) {
	t.Helper()
	calls := provider.SnapshotCalls()
	if baseline > len(calls) {
		t.Fatalf("provider call baseline = %d, want at most %d", baseline, len(calls))
	}
	var nudges []runtime.Call
	for _, call := range calls[baseline:] {
		if call.Method == "Nudge" || call.Method == "NudgeNow" {
			nudges = append(nudges, call)
		}
	}
	if len(nudges) != 1 || nudges[0].Name != name || nudges[0].Message != message {
		t.Fatalf("provider nudges since reuse = %+v, want exactly one nudge to %q with %q", nudges, name, message)
	}
}

func assertNoPoolAllocationSession(t *testing.T, store beads.Store) {
	t.Helper()
	snapshot, err := loadSessionBeadSnapshot(store)
	if err != nil {
		t.Fatalf("load sessions: %v", err)
	}
	if got := len(snapshot.OpenInfos()); got != 0 {
		t.Fatalf("open sessions = %d, want 0", got)
	}
}

func createRoutedWorkPoolBinding(t *testing.T, store beads.Store, cfg *config.City, hint routedWorkPoolAllocationHint, slot int) sessionpkg.Info {
	t.Helper()
	info, err := createPoolSessionBeadWithAlias(store, hint.PoolTarget, cfg, nil, time.Now().UTC(), poolSessionCreateIdentity{
		AgentName: fmt.Sprintf("%s-%d", hint.PoolTarget, slot),
		Slot:      slot,
		Metadata: map[string]string{
			beadmeta.TriggerBeadIDMetadataKey:       hint.WorkID,
			beadmeta.TriggerBeadStoreRefMetadataKey: hint.SourceStore,
		},
	}, "")
	if err != nil {
		t.Fatalf("create slot-%d routed-work binding: %v", slot, err)
	}
	return info
}

type poolAllocationDepListErrorStore struct {
	beads.Store
	err error
}

func (s *poolAllocationDepListErrorStore) DepList(string, string) ([]beads.Dep, error) {
	return nil, s.err
}

type poolAllocationFailSessionCreateStore struct {
	beads.Store
	fail atomic.Bool
}

type poolAllocationStartCommitBarrierStore struct {
	beads.Store
	provider    *runtime.Fake
	sessionID   string
	sessionName string
	entered     chan struct{}
	release     chan struct{}
	once        sync.Once
}

func (s *poolAllocationStartCommitBarrierStore) Get(id string) (beads.Bead, error) {
	if id == s.sessionID && s.provider.IsRunning(s.sessionName) {
		s.once.Do(func() {
			close(s.entered)
			<-s.release
		})
	}
	return s.Store.Get(id)
}

func (s *poolAllocationFailSessionCreateStore) Create(bead beads.Bead) (beads.Bead, error) {
	if s.fail.Load() && bead.Type == sessionpkg.BeadType {
		return beads.Bead{}, errors.New("session create unavailable")
	}
	return s.Store.Create(bead)
}
