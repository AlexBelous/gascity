//go:build integration

package beads

import (
	"database/sql"
	"sync"
	"sync/atomic"
	"testing"
)

// peakOpenConnectionsUnderLoad drives `workers` concurrent, deliberately
// overlapping queries through db and returns the highest OpenConnections the
// pool reached while they ran. SLEEP overlaps the queries so a pool that CAN
// open more than one connection will.
func peakOpenConnectionsUnderLoad(t *testing.T, db *sql.DB, workers int) int64 {
	t.Helper()
	var peak int64
	stop := make(chan struct{})
	var sampler sync.WaitGroup
	sampler.Add(1)
	go func() {
		defer sampler.Done()
		for {
			select {
			case <-stop:
				return
			default:
				if o := int64(db.Stats().OpenConnections); o > atomic.LoadInt64(&peak) {
					atomic.StoreInt64(&peak, o)
				}
			}
		}
	}()

	var wg sync.WaitGroup
	wg.Add(workers)
	errs := make(chan error, workers)
	for i := 0; i < workers; i++ {
		go func() {
			defer wg.Done()
			var n int
			if err := db.QueryRow("SELECT SLEEP(0.02)").Scan(&n); err != nil {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(stop)
	sampler.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("query under load failed (connection exhaustion?): %v", err)
	}
	return atomic.LoadInt64(&peak)
}

// TestBoundNativeDoltPoolCapsLiveConnectionsUnderLoad is the real-Dolt proof of
// the managed-Dolt connection cure's core invariant (invariant 6: every scoped
// SQL pool is bounded). It drives 200 concurrent, overlapping queries through a
// bounded pool against a live dolt sql-server and asserts the client never opens
// more than one server connection — a scoped store cannot fan a connection storm
// onto the shared server no matter how many callers race it.
//
// The unbounded control proves the load is genuinely concurrent (the same race
// opens more than one connection without the bound), so the bounded assertion is
// not vacuous.
//
// This is the store-level oracle. The full end-to-end version (200
// generated-default hook PROCESSES routed through the controller against a
// max_connections=32 server, with killed clients and return-to-baseline) is the
// production-rollout gate for the fast path wired in cmd/gc; it needs a
// controller+supervisor harness this package does not host and is tracked
// separately.
func TestBoundNativeDoltPoolCapsLiveConnectionsUnderLoad(t *testing.T) {
	const workers = 200

	bounded := startTestDoltServer(t)
	boundNativeDoltPool(bounded)
	if peak := peakOpenConnectionsUnderLoad(t, bounded, workers); peak > 1 {
		t.Fatalf("bounded pool peaked at %d open connections under load, want <= 1", peak)
	}

	unbounded := startTestDoltServer(t)
	if peak := peakOpenConnectionsUnderLoad(t, unbounded, workers); peak <= 1 {
		t.Fatalf("unbounded pool peaked at %d open connections; the load did not overlap, so the bounded proof is vacuous", peak)
	}
}
