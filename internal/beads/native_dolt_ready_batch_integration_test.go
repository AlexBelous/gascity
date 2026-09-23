//go:build integration

package beads

import (
	"context"
	"reflect"
	"testing"

	beadslib "github.com/steveyegge/beads"
)

// Hiding the optional capability exercises the unmodified single-ID path with
// the very same backend and data, including wisps and closed targets.
type readyOutcomeLegacyStorage struct{ beadslib.Storage }

func TestNativeReadyOutcomeBatchAgainstIsolatedDolt(t *testing.T) {
	store := openRealNativeDoltStoreForCAS(t, "ready-outcome-batch")
	if _, ok := store.storage.(nativeDependencyBatchReader); !ok {
		t.Fatal("real pinned backend lacks dependency batch capability")
	}
	var candidates []Bead
	var targets []Bead
	for _, ephemeral := range []bool{false, true} {
		target, err := store.Create(Bead{Title: "closed target", Ephemeral: ephemeral})
		if err != nil {
			t.Fatal(err)
		}
		if err := store.Close(target.ID); err != nil {
			t.Fatal(err)
		}
		candidate, err := store.Create(Bead{Title: "candidate", Ephemeral: ephemeral})
		if err != nil {
			t.Fatal(err)
		}
		if err := store.DepAdd(candidate.ID, target.ID, "blocks"); err != nil {
			t.Fatal(err)
		}
		candidates = append(candidates, candidate)
		targets = append(targets, target)
	}
	ctx := context.Background()
	compare := func(wantCount int) {
		t.Helper()
		old, err := store.filterReadyByWorkOutcome(ctx, readyOutcomeLegacyStorage{store.storage}, candidates)
		if err != nil {
			t.Fatal(err)
		}
		got, err := store.filterReadyByWorkOutcome(ctx, store.storage, candidates)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != wantCount || !reflect.DeepEqual(got, old) {
			t.Fatalf("batch=%v legacy=%v wantCount=%d", got, old, wantCount)
		}
	}
	compare(2)
	for _, target := range targets {
		if err := store.SetMetadata(target.ID, "gc.work_outcome", "blocked"); err != nil {
			t.Fatal(err)
		}
	}
	compare(0)
	// Every invocation must read current outcomes; there is no verdict cache.
	for _, target := range targets {
		if err := store.SetMetadata(target.ID, "gc.work_outcome", "shipped"); err != nil {
			t.Fatal(err)
		}
	}
	compare(2)
}
