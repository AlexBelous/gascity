package main

import (
	"encoding/json"
	"sync/atomic"
	"testing"
	"testing/synctest"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/events"
	sessionpkg "github.com/gastownhall/gascity/internal/session"
)

func sessionWaitShadowEvent(t *testing.T, bead beads.Bead) events.Event {
	t.Helper()
	if bead.ID == "" {
		bead.ID = "wait-event"
	}
	payload, err := json.Marshal(bead)
	if err != nil {
		t.Fatalf("marshal bead event: %v", err)
	}
	return events.Event{
		Type:    events.BeadUpdated,
		Subject: bead.ID,
		Payload: payload,
	}
}

func TestSessionWaitDependencyShadowAdmissionRetriesPendingRequest(t *testing.T) {
	cs := &controllerState{}
	var calls int
	if err := cs.installSessionWaitDependencyShadowAdmission(func() sessionWaitShadowRefreshResult {
		calls++
		if calls > 1 {
			return sessionWaitShadowConverged
		}
		return sessionWaitShadowRetry
	}, func(string) bool { return false }); err != nil {
		t.Fatalf("install admission: %v", err)
	}
	t.Cleanup(cs.stopSessionWaitDependencyShadowAdmission)

	cs.admitSessionWaitDependencyShadowEvent(sessionWaitShadowEvent(
		t,
		sessionWaitShadowBead("session-a", "dep-a"),
	))
	cs.admitSessionWaitDependencyShadowEvent(events.Event{
		Type:    events.BeadUpdated,
		Payload: []byte(`{"malformed"`),
	})

	if calls != 2 {
		t.Fatalf("refresh calls = %d, want retry after pending failure", calls)
	}
}

func TestSessionWaitDependencyShadowAdmissionSkipsCleanUnrelatedEvents(t *testing.T) {
	cs := &controllerState{}
	var calls int
	if err := cs.installSessionWaitDependencyShadowAdmission(func() sessionWaitShadowRefreshResult {
		calls++
		return sessionWaitShadowConverged
	}, func(string) bool { return false }); err != nil {
		t.Fatalf("install admission: %v", err)
	}
	t.Cleanup(cs.stopSessionWaitDependencyShadowAdmission)

	cs.admitSessionWaitDependencyShadowEvent(sessionWaitShadowEvent(t, beads.Bead{
		ID:     "task-1",
		Type:   "task",
		Status: "open",
	}))
	cs.admitSessionWaitDependencyShadowEvent(events.Event{
		Type:    events.BeadUpdated,
		Payload: []byte(`{"malformed"`),
	})
	cs.admitSessionWaitDependencyShadowEvent(events.Event{
		Type:    events.ControllerStarted,
		Payload: []byte(`{"id":"not-a-bead-event"}`),
	})

	if calls != 0 {
		t.Fatalf("refresh calls = %d, want none for a clean unrelated projection", calls)
	}
}

func TestSessionWaitDependencyShadowAdmissionRecognizesWaitIdentityRemoval(t *testing.T) {
	cs := &controllerState{}
	var calls int
	if err := cs.installSessionWaitDependencyShadowAdmission(func() sessionWaitShadowRefreshResult {
		calls++
		return sessionWaitShadowConverged
	}, func(id string) bool { return id == "wait-1" }); err != nil {
		t.Fatalf("install admission: %v", err)
	}
	t.Cleanup(cs.stopSessionWaitDependencyShadowAdmission)

	cs.admitSessionWaitDependencyShadowEvent(sessionWaitShadowEvent(t, beads.Bead{
		ID:     "wait-1",
		Type:   "task",
		Status: "open",
	}))

	if calls != 1 {
		t.Fatalf("refresh calls = %d, want prior wait membership to request one census", calls)
	}
}

func TestSessionWaitDependencyShadowAdmissionOlderSuccessCannotClearNewerFailure(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		cs := &controllerState{}
		firstEntered := make(chan struct{})
		releaseFirst := make(chan struct{})
		firstReturned := make(chan struct{})
		var calls atomic.Int64
		if err := cs.installSessionWaitDependencyShadowAdmission(func() sessionWaitShadowRefreshResult {
			switch calls.Add(1) {
			case 1:
				close(firstEntered)
				<-releaseFirst
				return sessionWaitShadowConverged
			default:
				return sessionWaitShadowRetry
			}
		}, func(string) bool { return false }); err != nil {
			t.Fatalf("install admission: %v", err)
		}
		t.Cleanup(cs.stopSessionWaitDependencyShadowAdmission)

		go func() {
			cs.requestSessionWaitDependencyShadowRefreshForBead(beads.Bead{}, true)
			close(firstReturned)
		}()
		synctest.Wait()
		select {
		case <-firstEntered:
		default:
			t.Fatal("first refresh did not enter")
		}
		cs.requestSessionWaitDependencyShadowRefreshForBead(beads.Bead{}, true)
		close(releaseFirst)
		synctest.Wait()
		select {
		case <-firstReturned:
		default:
			t.Fatal("first refresh did not return")
		}
		cs.requestSessionWaitDependencyShadowRefreshForBead(beads.Bead{}, false)

		if got := calls.Load(); got != 3 {
			t.Fatalf("refresh calls = %d, want pending newer generation retried after older success", got)
		}
	})
}

func TestSessionWaitDependencyShadowAdmissionStopJoinsAndRejectsLaterEvents(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		cs := &controllerState{}
		entered := make(chan struct{})
		release := make(chan struct{})
		requestReturned := make(chan struct{})
		stopReturned := make(chan struct{})
		var calls atomic.Int64
		if err := cs.installSessionWaitDependencyShadowAdmission(func() sessionWaitShadowRefreshResult {
			calls.Add(1)
			close(entered)
			<-release
			return sessionWaitShadowConverged
		}, func(string) bool { return false }); err != nil {
			t.Fatalf("install admission: %v", err)
		}

		go func() {
			cs.requestSessionWaitDependencyShadowRefreshForBead(beads.Bead{}, true)
			close(requestReturned)
		}()
		synctest.Wait()
		select {
		case <-entered:
		default:
			t.Fatal("refresh did not enter")
		}
		go func() {
			cs.stopSessionWaitDependencyShadowAdmission()
			close(stopReturned)
		}()
		synctest.Wait()

		select {
		case <-stopReturned:
			t.Fatal("stop returned while a refresh callback was still in flight")
		default:
		}
		close(release)
		synctest.Wait()
		select {
		case <-requestReturned:
		default:
			t.Fatal("in-flight refresh did not return")
		}
		select {
		case <-stopReturned:
		default:
			t.Fatal("admission stop did not return")
		}

		cs.admitSessionWaitDependencyShadowEvent(sessionWaitShadowEvent(
			t,
			sessionWaitShadowBead("session-after-stop", "dep-after-stop"),
		))
		if got := calls.Load(); got != 1 {
			t.Fatalf("refresh calls after stop = %d, want exactly the joined in-flight callback", got)
		}
	})
}

func TestSessionWaitDependencyShadowAdmissionValidatesCallbacks(t *testing.T) {
	var nilState *controllerState
	if err := nilState.installSessionWaitDependencyShadowAdmission(func() sessionWaitShadowRefreshResult {
		return sessionWaitShadowConverged
	}, func(string) bool { return false }); err == nil {
		t.Fatal("nil controller state install succeeded")
	}
	cs := &controllerState{}
	if err := cs.installSessionWaitDependencyShadowAdmission(nil, func(string) bool { return false }); err == nil {
		t.Fatal("nil refresh callback install succeeded")
	}
	if err := cs.installSessionWaitDependencyShadowAdmission(func() sessionWaitShadowRefreshResult {
		return sessionWaitShadowConverged
	}, nil); err == nil {
		t.Fatal("nil membership callback install succeeded")
	}
}

func TestSessionWaitDependencyShadowAdmissionRecognizesLegacyWait(t *testing.T) {
	cs := &controllerState{}
	var calls int
	if err := cs.installSessionWaitDependencyShadowAdmission(func() sessionWaitShadowRefreshResult {
		calls++
		return sessionWaitShadowConverged
	}, func(string) bool { return false }); err != nil {
		t.Fatalf("install admission: %v", err)
	}
	t.Cleanup(cs.stopSessionWaitDependencyShadowAdmission)

	cs.admitSessionWaitDependencyShadowEvent(sessionWaitShadowEvent(t, beads.Bead{
		ID:     "legacy-wait",
		Type:   sessionpkg.LegacyWaitBeadType,
		Status: "open",
		Labels: []string{sessionpkg.WaitBeadLabel},
	}))
	if calls != 1 {
		t.Fatalf("refresh calls = %d, want legacy wait event admitted", calls)
	}
}

func TestSessionWaitDependencyShadowAdmissionRunsAfterExistingEventEffects(t *testing.T) {
	previousDispatch := beadCloseAutocloseDispatch
	var autocloseDispatched bool
	beadCloseAutocloseDispatch = func(func()) {
		autocloseDispatched = true
	}
	t.Cleanup(func() { beadCloseAutocloseDispatch = previousDispatch })

	backing := beads.NewMemStore()
	wait, err := backing.Create(sessionWaitShadowBead("session-a", "dep-a"))
	if err != nil {
		t.Fatalf("Create(wait): %v", err)
	}
	cache := beads.NewCachingStoreForTest(backing, nil)
	if err := cache.PrimeActive(); err != nil {
		t.Fatalf("PrimeActive: %v", err)
	}
	if err := backing.Close(wait.ID); err != nil {
		t.Fatalf("Close(wait): %v", err)
	}
	closed, err := backing.Get(wait.ID)
	if err != nil {
		t.Fatalf("Get(closed wait): %v", err)
	}

	cs := &controllerState{
		cityBeadStore: cache,
		pokeCh:        make(chan struct{}, 1),
	}
	var refreshCalls int
	if err := cs.installSessionWaitDependencyShadowAdmission(func() sessionWaitShadowRefreshResult {
		refreshCalls++
		if !autocloseDispatched {
			t.Error("wait-shadow refresh ran before bead-close autoclose dispatch")
		}
		select {
		case <-cs.pokeCh:
		default:
			t.Error("wait-shadow refresh ran before the existing controller poke")
		}
		census, censusErr := observeSessionWaitCensus(beads.SessionStore{Store: cache})
		if censusErr != nil {
			t.Errorf("observe post-event wait census: %v", censusErr)
			return sessionWaitShadowRetry
		}
		if len(census.waits) != 0 {
			t.Errorf("post-event wait census = %#v, want closed wait removed before refresh", census.waits)
			return sessionWaitShadowRetry
		}
		return sessionWaitShadowConverged
	}, func(string) bool { return false }); err != nil {
		t.Fatalf("install admission: %v", err)
	}
	t.Cleanup(cs.stopSessionWaitDependencyShadowAdmission)

	cs.applyBeadEventToStores(beadSnapshotEvent(t, events.BeadClosed, closed))
	if refreshCalls != 1 {
		t.Fatalf("refresh calls = %d, want one post-event refresh", refreshCalls)
	}
}
