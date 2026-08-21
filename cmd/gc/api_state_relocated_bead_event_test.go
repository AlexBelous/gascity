package main

import (
	"encoding/json"
	"testing"

	"github.com/gastownhall/gascity/internal/beadmeta"
	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/config"
	"github.com/gastownhall/gascity/internal/events"
)

// TestBeadEventStoresResolveRelocatedClassPrefixes pins that a bead.closed for a
// bead living in the infrastructure binding reaches the binding, not the work
// stores.
//
// The controller resolved the owning store by scanning the HQ prefix and each
// rig prefix. A relocated class mints under a reserved prefix that is neither,
// so a "gcg-*" close matched nothing and fell through to the all-work-stores
// broadcast — which is not merely wasteful. Every autoclose arm starts by
// reading the just-closed bead out of stores[0], an arbitrary work store that
// does not hold it, so all three returned on the not-found and the molecule in
// the binding stayed open forever.
//
// The assertion is the molecule root's status rather than the identity of the
// resolved store: the store is a means, and pinning the reap is what says the
// hook still does its job on a migrated city.
func TestBeadEventStoresResolveRelocatedClassPrefixes(t *testing.T) {
	prev := beadCloseAutocloseDispatch
	beadCloseAutocloseDispatch = func(fn func()) { fn() } // synchronous in tests
	t.Cleanup(func() { beadCloseAutocloseDispatch = prev })

	// The binding mints under the reserved graph prefix; the work store keeps
	// the default. Distinct prefixes are the whole point — they are what makes
	// the owning store resolvable by id at all, and they keep a wrong-store read
	// a miss rather than a collision.
	binding := &beads.MemStore{IDPrefix: "gcg"}
	work := beads.NewMemStore()

	root, err := binding.Create(beads.Bead{Title: "Formula: mol-relocated", Type: "molecule"})
	if err != nil {
		t.Fatalf("Create molecule root in the binding: %v", err)
	}
	step, err := binding.Create(beads.Bead{
		Title:    "Step 1: implement",
		Type:     "step",
		ParentID: root.ID,
		Metadata: map[string]string{beadmeta.RootBeadIDMetadataKey: root.ID},
	})
	if err != nil {
		t.Fatalf("Create step in the binding: %v", err)
	}
	if err := binding.Close(step.ID); err != nil {
		t.Fatalf("Close step: %v", err)
	}

	payload, err := json.Marshal(beads.Bead{ID: step.ID, Title: step.Title, Type: "step", Status: "closed"})
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}

	cs := &controllerState{
		cfg: &config.City{
			Workspace: config.Workspace{Name: "test-city"},
			Rigs:      []config.Rig{{Name: "alpha", Path: "/tmp/alpha", Prefix: "ra"}},
		},
		cityBeadStore: work,
		beadStores:    map[string]beads.Store{"alpha": beads.NewMemStore()},
		storageRoutes: splitRoutes(binding),
		pokeCh:        make(chan struct{}, 1),
	}

	cs.applyBeadEventToStores(events.Event{
		Type:    events.BeadClosed,
		Actor:   "agent",
		Subject: step.ID,
		Payload: payload,
	})

	got, err := binding.Get(root.ID)
	if err != nil {
		t.Fatalf("Get molecule root: %v", err)
	}
	if got.Status != "closed" {
		t.Errorf("molecule root %s status = %q after its only step closed, want %q; the close event never resolved to the binding, so autoclose read the bead out of a work store that does not hold it and returned", root.ID, got.Status, "closed")
	}
}

// TestBeadEventStoresIgnoreReservedPrefixesWithoutARelocation is the control for
// the test above: the same reserved-prefix id on a city that relocates nothing
// must NOT become a candidate. No engine mints under a reserved prefix on such
// a city, so a match there could only come from an id that is not what it looks
// like — and routing it to a binding that does not exist would be a nil store
// reported as the authoritative owner, suppressing the work-store fallback.
func TestBeadEventStoresIgnoreReservedPrefixesWithoutARelocation(t *testing.T) {
	cs := &controllerState{
		cfg:           &config.City{Workspace: config.Workspace{Name: "test-city"}},
		cityBeadStore: beads.NewMemStore(),
		beadStores:    map[string]beads.Store{},
		storageRoutes: nil, // no [storage] section
	}
	if store, known := cs.beadEventConfiguredStoreLocked("gcg-1"); known {
		t.Errorf("a city that relocates nothing claimed to own %q (store=%v); the reserved-prefix arm must be gated on an actual relocation", "gcg-1", store)
	}
}
