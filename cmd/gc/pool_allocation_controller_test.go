package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
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

func newRoutedWorkPoolAuthorizationFixture(t *testing.T) routedWorkPoolAuthorizationFixture {
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
	store := beads.NewMemStore()
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
	stderr := &bytes.Buffer{}
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

func assertRoutedWorkPoolAllocationFallback(t *testing.T, cr *CityRuntime) {
	t.Helper()
	if !cr.readyRoutedWorkPokePending.Load() || len(cr.pokeCh) != 1 {
		t.Fatalf("legacy fallback = (pending=%t, pokes=%d), want one priority poke", cr.readyRoutedWorkPokePending.Load(), len(cr.pokeCh))
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
