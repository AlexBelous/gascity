package main

// The control-dispatch routing gates stop re-deriving the binding.
//
// Four surfaces used to ask the routes directly whether the graph class is
// relocated — the same question resolveClassStore asks to pick its branch, asked
// a second time, which is the shape the residency boundary check calls family
// (b) and the way #5125/#5127 reproduce. They now read the resolver's own
// grouping instead. This pins the claim that makes the swap safe: for every
// topology a city can be in, the grouping's answer IS the routes' answer.

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/coordclass"
)

// TestSoleClassBindingStoreMatchesTheRoutesGraphAnswer compares the two
// derivations over every shape a city's storage can take.
//
// The refused row is the one that matters most. A refused city produces a
// binding whose every read returns the boot gate's sentence, and the callers
// want exactly that store: it is what turns an unserveable city into a loud
// failure instead of a quiet read of the work-ledger copies `gc storage migrate`
// retained and no longer mutates.
func TestSoleClassBindingStoreMatchesTheRoutesGraphAnswer(t *testing.T) {
	tests := []struct {
		name  string
		setup func(t *testing.T) string
	}{
		{
			name: "city serving its infrastructure classes from a binding",
			setup: func(t *testing.T) string {
				cityPath, _, _ := byIDRouteCity(t)
				return cityPath
			},
		},
		{
			name: "city that relocates nothing",
			setup: func(t *testing.T) string {
				cityPath := t.TempDir()
				seedCLIStorageRoutes(t, cityPath, nil)
				return cityPath
			},
		},
		{
			name: "refused city",
			setup: func(t *testing.T) string {
				cityPath := t.TempDir()
				seedCLIStorageRoutes(t, cityPath, refusingStorageRoutes("infra", errors.New("this city's storage cannot be served")))
				return cityPath
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cityPath := filepath.Clean(tt.setup(t))
			wantStore, wantRelocated := cliStorageRoutes(cityPath).storeFor(coordclass.ClassGraph)
			gotStore, gotRelocated := cliSoleClassBindingStore(cityPath)
			if gotRelocated != wantRelocated {
				t.Fatalf("the grouping reports relocated=%v where the routes report %v; a disagreement here routes control beads to a different database than the one that owns them", gotRelocated, wantRelocated)
			}
			if gotStore != wantStore {
				t.Errorf("the grouping resolves to %p where the routes resolve to %p — two answers to one question is the split-store bug class", gotStore, wantStore)
			}
		})
	}
}

// TestSoleClassBindingStoreRefusesAFanOut is the arm the equivalence rows cannot
// reach: a city serving its classes from more than one binding.
//
// cliSoleClassBinding reports that as an error, and these callers have no error
// channel — they answer a routing question per tick. The answer is a store whose
// every read returns the fault, which is relocated=true and loud, because the
// alternative ("not relocated") sends the scan to `bd` in the work directory for
// a confidently stale answer. storageSplitShapeOf refuses this shape at boot, so
// nothing serving can be in it; the row exists so the direction stays chosen
// rather than inherited.
func TestSoleClassBindingStoreRefusesAFanOut(t *testing.T) {
	cityPath := t.TempDir()
	first, second := beads.NewMemStore(), beads.NewMemStore()
	fanOut := &storageRoutes{stores: map[coordclass.Class]beads.Store{
		coordclass.ClassGraph:    first,
		coordclass.ClassSessions: second,
	}, binding: "infra"}
	seedCLIStorageRoutes(t, cityPath, fanOut)

	store, relocated := cliSoleClassBindingStore(filepath.Clean(cityPath))
	if !relocated {
		t.Fatal("a fan-out city reports relocated=false; its control reads would fall back to the work ledger the classes were migrated off")
	}
	if _, err := store.Get("ga-1"); err == nil {
		t.Error("a fan-out city's binding answered a read; the topology fault has to surface at the read, since there is no error channel to carry it")
	}
}
