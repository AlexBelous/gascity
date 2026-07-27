//go:build integration

package beads

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"

	beadslib "github.com/steveyegge/beads"
)

// openClaimIntegrationStore opens a real upstream beads storage handle and wraps
// it in a NativeDoltStore, skipping when the native backend is unavailable.
func openClaimIntegrationStore(t *testing.T) *NativeDoltStore {
	t.Helper()
	ctx := context.Background()
	storage, err := beadslib.OpenBestAvailable(ctx, filepath.Join(t.TempDir(), ".beads"))
	if err != nil {
		t.Skipf("upstream native beads storage unavailable: %v", err)
	}
	t.Cleanup(func() {
		if err := storage.Close(); err != nil {
			t.Fatalf("close upstream storage: %v", err)
		}
	})
	if err := storage.SetConfig(ctx, "issue_prefix", "gc"); err != nil {
		t.Fatalf("set issue prefix: %v", err)
	}
	return newNativeDoltStoreWithStorageAndPrefix(storage, "native-claim-integration", "gc")
}

// TestNativeDoltStoreClaimBeadRealBackend proves ClaimBead delegates to the real
// upstream ClaimIssue CAS: a fresh open bead claims, a same-actor re-claim is
// idempotent, and a different actor loses without error.
func TestNativeDoltStoreClaimBeadRealBackend(t *testing.T) {
	store := openClaimIntegrationStore(t)
	ctx := context.Background()

	seed, err := store.Create(Bead{Title: "real claim target"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	claimed, ok, err := store.ClaimBead(ctx, seed.ID, "worker-1")
	if err != nil || !ok {
		t.Fatalf("first claim ok=%v err=%v, want true nil", ok, err)
	}
	if claimed.Assignee != "worker-1" || claimed.Status != "in_progress" {
		t.Fatalf("claimed = %+v, want worker-1 in_progress", claimed)
	}

	if _, ok, err := store.ClaimBead(ctx, seed.ID, "worker-1"); err != nil || !ok {
		t.Fatalf("idempotent re-claim ok=%v err=%v, want true nil", ok, err)
	}

	if _, ok, err := store.ClaimBead(ctx, seed.ID, "worker-2"); err != nil || ok {
		t.Fatalf("foreign claim ok=%v err=%v, want false nil (lost race, not error)", ok, err)
	}

	if _, ok, err := store.ClaimBead(ctx, "gc-missing", "worker-1"); ok || !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing-bead claim ok=%v err=%v, want false ErrNotFound", ok, err)
	}
}

// TestNativeDoltStoreClaimBeadRaceSingleWinner runs many concurrent claims of one
// open bead through the real backend and asserts exactly one actor wins. This is
// the atomicity invariant (invariant 1) at unit scale; the max_connections=32 /
// 200-hook oracle in slice 4 proves it at production concurrency.
func TestNativeDoltStoreClaimBeadRaceSingleWinner(t *testing.T) {
	store := openClaimIntegrationStore(t)
	ctx := context.Background()

	seed, err := store.Create(Bead{Title: "contended target"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	const racers = 24
	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		winners []string
	)
	wg.Add(racers)
	for i := 0; i < racers; i++ {
		actor := "worker-" + string(rune('a'+i))
		go func(actor string) {
			defer wg.Done()
			_, ok, err := store.ClaimBead(ctx, seed.ID, actor)
			if err != nil {
				return // a lost race must be ok=false,nil; only a real fault errors
			}
			if ok {
				mu.Lock()
				winners = append(winners, actor)
				mu.Unlock()
			}
		}(actor)
	}
	wg.Wait()

	if len(winners) != 1 {
		t.Fatalf("winners = %v, want exactly one (atomic claim violated)", winners)
	}

	final, err := store.Get(seed.ID)
	if err != nil {
		t.Fatalf("Get after race: %v", err)
	}
	if final.Assignee != winners[0] || final.Status != "in_progress" {
		t.Fatalf("final bead = %+v, want assignee %s in_progress", final, winners[0])
	}
}
