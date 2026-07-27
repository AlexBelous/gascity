//go:build integration

package beads

import (
	"context"
	"sync"
	"testing"
)

// TestNativeDoltStoreConcurrentClaimHasExactlyOneWinner is the focused, store-
// level proof of invariant 1 (atomic claim) for the managed-Dolt connection
// cure: many distinct actors racing to claim ONE bead through a single
// controller-style NativeDoltStore, on a multi-connection pool, yield exactly one
// winner. It guards the property the fast path depends on — that the store's
// AtomicClaimer (upstream ClaimIssue's conditional-UPDATE compare-and-swap)
// remains safe under genuine concurrency, so a future beads or Dolt change that
// regressed the CAS to a non-serializable read-then-write would fail here.
//
// The store uses the default (multi-connection) pool plus concurrent read
// pressure, so the claims genuinely run on different connections; the test
// asserts the pool is not the single-connection scoped variant (else the win
// would be serialized by the pool itself and the proof would be vacuous), then
// asserts exactly one of N distinct actors wins while the rest cleanly lose.
func TestNativeDoltStoreConcurrentClaimHasExactlyOneWinner(t *testing.T) {
	const actors = 24

	store := openServerBackedNativeStoreForRace(t)

	// Guard against a vacuous proof: a pool capped at one connection would
	// serialize the claims by itself. The controller store deliberately does not
	// bound its pool, so the CAS can race and the serialization must come from
	// claimMu.
	if accessor, ok := store.storage.(rawDBGetter); ok {
		if max := accessor.DB().Stats().MaxOpenConnections; max == 1 {
			t.Fatalf("race store pool is capped at 1 connection; the concurrent-claim proof would be vacuous")
		}
	}

	bead, err := store.Create(Bead{Title: "single-winner claim race"})
	if err != nil {
		t.Fatalf("create race bead: %v", err)
	}

	// Concurrent read pressure forces the pool to keep several connections warm
	// during the claim race — the exact condition under which the process oracle
	// caught the double-claim. Without it a serialized claim reuses one idle
	// connection and the proof is accidentally consistent; with it, serialized
	// claims land on different connections (different Dolt session snapshots),
	// reproducing the failure this test must guard against.
	readStop := make(chan struct{})
	var readers sync.WaitGroup
	for r := 0; r < 8; r++ {
		readers.Add(1)
		go func() {
			defer readers.Done()
			for {
				select {
				case <-readStop:
					return
				default:
					_, _ = store.List(ListQuery{Limit: 50, TierMode: TierBoth})
				}
			}
		}()
	}
	defer func() { close(readStop); readers.Wait() }()

	// A start barrier releases every claimer at once to maximize contention on
	// the single bead.
	start := make(chan struct{})
	var wg sync.WaitGroup
	var mu sync.Mutex
	winners := make([]string, 0, actors)
	claimErrs := make([]error, 0)
	wg.Add(actors)
	for i := 0; i < actors; i++ {
		go func(i int) {
			defer wg.Done()
			actor := "race-actor-" + itoa(i)
			<-start
			_, claimed, err := store.ClaimBead(context.Background(), bead.ID, actor)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				claimErrs = append(claimErrs, err)
				return
			}
			if claimed {
				winners = append(winners, actor)
			}
		}(i)
	}
	close(start)
	wg.Wait()

	for _, err := range claimErrs {
		t.Errorf("claim returned a hard error (want lost-race, not error): %v", err)
	}
	if len(winners) != 1 {
		t.Fatalf("concurrent claim produced %d winners %v, want exactly 1 (atomic claim broken)", len(winners), winners)
	}

	// The bead is now owned by the single winner, in_progress.
	got, err := store.Get(bead.ID)
	if err != nil {
		t.Fatalf("get claimed bead: %v", err)
	}
	if got.Assignee != winners[0] || got.Status != "in_progress" {
		t.Fatalf("claimed bead = {assignee:%q status:%q}, want {assignee:%q status:in_progress}", got.Assignee, got.Status, winners[0])
	}

	// Idempotent same-actor re-claim: the winner re-claiming still succeeds and
	// no new winner appears; a different actor is rejected (lost race).
	if _, claimed, err := store.ClaimBead(context.Background(), bead.ID, winners[0]); err != nil || !claimed {
		t.Fatalf("same-actor re-claim = (claimed=%v, err=%v), want (true, nil) — claim must be idempotent for the owner", claimed, err)
	}
	if _, claimed, err := store.ClaimBead(context.Background(), bead.ID, "race-actor-intruder"); err != nil || claimed {
		t.Fatalf("different-actor claim of an owned bead = (claimed=%v, err=%v), want (false, nil)", claimed, err)
	}
}

// openServerBackedNativeStoreForRace starts a real dolt sql-server, opens a
// server-backed native storage handle against a fresh database on it (a
// multi-connection pool, like the controller's), and wraps it in a
// NativeDoltStore. It is deliberately NOT the single-connection scoped variant.
func openServerBackedNativeStoreForRace(t *testing.T) *NativeDoltStore {
	t.Helper()
	seedDB, port := startTestDoltServerWithPort(t)
	if _, err := seedDB.Exec("CREATE DATABASE claimrace"); err != nil {
		t.Fatalf("create claimrace database: %v", err)
	}

	env := map[string]string{
		"BEADS_DOLT_SERVER_HOST":     "127.0.0.1",
		"BEADS_DOLT_SERVER_PORT":     port,
		"BEADS_DOLT_SERVER_USER":     "root",
		"BEADS_DOLT_SERVER_DATABASE": "claimrace",
	}
	ctx := context.Background()
	storage, err := OpenNativeStorage(ctx, t.TempDir(), env)
	if err != nil {
		t.Fatalf("open server-backed native storage: %v", err)
	}
	t.Cleanup(func() { _ = storage.Close() })
	if err := storage.SetConfig(ctx, "issue_prefix", "gc"); err != nil {
		t.Fatalf("set issue prefix: %v", err)
	}
	return newNativeDoltStoreWithStorageAndPrefix(storage, "race-controller", "gc")
}

// itoa is a tiny local int->string to avoid importing strconv just for the actor
// labels in this file.
func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b [20]byte
	pos := len(b)
	for i > 0 {
		pos--
		b[pos] = byte('0' + i%10)
		i /= 10
	}
	return string(b[pos:])
}
