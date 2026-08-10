package main

import (
	"io"
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/config"
	"github.com/gastownhall/gascity/internal/events"
)

func readyRoutedWorkViewRuntime(t *testing.T, city beads.Store, rigs map[string]beads.Store) *CityRuntime {
	t.Helper()
	cr := &CityRuntime{
		cityName: "test-city",
		cityPath: t.TempDir(),
		cfg: &config.City{
			Workspace: config.Workspace{Name: "test-city"},
			Agents: []config.Agent{
				{Name: "worker", MaxActiveSessions: readyRoutedWorkMax(3)},
			},
		},
		cs: &controllerState{
			cityName:      "test-city",
			cityBeadStore: city,
			beadStores:    rigs,
			eventProv:     events.NewFake(),
		},
		stderr: io.Discard,
	}
	return cr
}

// TestReadyRoutedWorkViewCarriesExactKeysPerStore is Q2's promotion proof: the
// per-store ReadyLive read is no longer a hash input, it is the sweep's DECLARED
// routed-work view, and every unallocated row in it carries the exact
// (workID, poolTarget, sourceStore) key the pool-allocation admission is
// enqueued under.
func TestReadyRoutedWorkViewCarriesExactKeysPerStore(t *testing.T) {
	city := &readyStaticStore{Store: beads.NewMemStore(), ready: []beads.Bead{
		{ID: "w-routed", Status: "open", Type: "task", Metadata: map[string]string{"gc.routed_to": "worker"}},
		{ID: "w-assigned", Status: "open", Type: "task", Assignee: "worker-1", Metadata: map[string]string{"gc.routed_to": "worker"}},
		{ID: "w-unrouted", Status: "open", Type: "task"},
	}}
	rig := &readyStaticStore{Store: beads.NewMemStore(), ready: []beads.Bead{
		{ID: "w-rig", Status: "open", Type: "task", Metadata: map[string]string{"gc.routed_to": "worker"}},
	}}
	cr := readyRoutedWorkViewRuntime(t, city, map[string]beads.Store{"rig:work": rig})

	view := cr.readReadyRoutedWorkView()

	if view.Stores != 2 {
		t.Fatalf("view stores = %d, want 2 (city + one rig)", view.Stores)
	}
	if len(view.Entries) != 4 {
		t.Fatalf("view entries = %+v, want every ready row in every store", view.Entries)
	}
	if city.readyCalls != 1 || rig.readyCalls != 1 {
		t.Fatalf("ReadyLive calls = (city=%d, rig=%d), want exactly one bounded read per store", city.readyCalls, rig.readyCalls)
	}

	unallocated := view.unallocated()
	if len(unallocated) != 2 {
		t.Fatalf("unallocated entries = %+v, want the two routed rows with no assignee", unallocated)
	}
	want := map[string]readyRoutedWorkEntry{
		"w-routed": {SourceStore: "test-city", WorkID: "w-routed", PoolTarget: "worker", Status: "open", Type: "task"},
		"w-rig":    {SourceStore: "rig:work", WorkID: "w-rig", PoolTarget: "worker", Status: "open", Type: "task"},
	}
	for _, entry := range unallocated {
		expected, ok := want[entry.WorkID]
		if !ok {
			t.Fatalf("unexpected unallocated entry %+v", entry)
		}
		if entry != expected {
			t.Fatalf("entry %q = %+v, want %+v", entry.WorkID, entry, expected)
		}
	}
}

// TestReadyRoutedWorkViewFingerprintTracksDemandNotTouches pins what the
// promoted view invalidates the demand snapshot on. The retired fingerprint
// hashed each bead's UpdatedAt, so any touch rebuilt desired state for a change
// no demand decision could see. The view hashes the DECLARED projection: a
// re-route moves it, an assignment moves it, a bare touch does not.
func TestReadyRoutedWorkViewFingerprintTracksDemandNotTouches(t *testing.T) {
	base := beads.Bead{
		ID: "w-1", Status: "open", Type: "task",
		UpdatedAt: time.Date(2026, 8, 10, 0, 0, 0, 0, time.UTC),
		Metadata:  map[string]string{"gc.routed_to": "worker"},
	}
	store := &readyStaticStore{Store: beads.NewMemStore(), ready: []beads.Bead{base}}
	cr := readyRoutedWorkViewRuntime(t, store, nil)

	first := cr.readReadyRoutedWorkView().Fingerprint

	touched := base
	touched.UpdatedAt = base.UpdatedAt.Add(time.Hour)
	store.ready = []beads.Bead{touched}
	if got := cr.readReadyRoutedWorkView().Fingerprint; got != first {
		t.Fatalf("fingerprint moved on a demand-irrelevant touch: %q != %q", got, first)
	}

	assigned := base
	assigned.Assignee = "worker-1"
	store.ready = []beads.Bead{assigned}
	if got := cr.readReadyRoutedWorkView().Fingerprint; got == first {
		t.Fatal("fingerprint did not move when the routed work was allocated")
	}

	rerouted := base
	rerouted.Metadata = map[string]string{"gc.routed_to": "other"}
	store.ready = []beads.Bead{rerouted}
	if got := cr.readReadyRoutedWorkView().Fingerprint; got == first {
		t.Fatal("fingerprint did not move when the routed work changed target")
	}
}

// TestReadyRoutedWorkViewChangeEdgeIsConsumedOnce pins the invalidation contract
// the retired snapshot field carried: the change is edge-detected at the read,
// and exactly one demand-snapshot refresh consumes it.
func TestReadyRoutedWorkViewChangeEdgeIsConsumedOnce(t *testing.T) {
	store := &readyStaticStore{Store: beads.NewMemStore(), ready: []beads.Bead{
		{ID: "w-1", Status: "open", Type: "task", Metadata: map[string]string{"gc.routed_to": "worker"}},
	}}
	cr := readyRoutedWorkViewRuntime(t, store, nil)

	cr.flooredReadyRoutedWorkView()
	if cr.takeReadyRoutedWorkViewChanged() {
		t.Fatal("first observation raised a change edge; there is nothing to have changed from")
	}

	store.ready = append(store.ready, beads.Bead{
		ID: "w-2", Status: "open", Type: "task", Metadata: map[string]string{"gc.routed_to": "worker"},
	})
	cr.readyRoutedWorkViewAt = time.Now().Add(-2 * readyRoutedWorkViewFloor)
	cr.flooredReadyRoutedWorkView()
	if !cr.takeReadyRoutedWorkViewChanged() {
		t.Fatal("new unallocated routed work did not raise a change edge")
	}
	if cr.takeReadyRoutedWorkViewChanged() {
		t.Fatal("change edge survived the refresh that consumed it")
	}
}
