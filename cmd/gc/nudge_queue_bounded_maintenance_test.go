package main

import (
	"os"
	"syscall"
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/nudgequeue"
)

// TestRunNudgeQueueMaintenanceSweep_BoundedPassPreservesBacklogThenConverges
// guards ga-2kzci3 FR3: a maintenance pass must respect a deadline derived
// from the caller's `now`, not silently run unbounded. A stale `now` (whose
// derived deadline has already elapsed relative to the real clock) must
// leave the backlog untouched rather than draining it in one shot; a fresh
// `now` must then converge the same backlog fully.
func TestRunNudgeQueueMaintenanceSweep_BoundedPassPreservesBacklogThenConverges(t *testing.T) {
	clearGCEnv(t)
	disableManagedDoltRecoveryForTest(t)

	dir := t.TempDir()
	base := time.Now()
	const backlog = 5
	for i := 0; i < backlog; i++ {
		// Created 25h before base, so ExpiresAt (created+24h TTL) is ~1h
		// before base -- already expired by the time any of the `now`
		// values below are evaluated against it.
		item := newQueuedNudge("ghost-agent", "msg", base.Add(-25*time.Hour))
		if err := enqueueQueuedNudge(dir, item); err != nil {
			t.Fatalf("enqueueQueuedNudge(%d): %v", i, err)
		}
	}

	// staleNow is far enough in the past that staleNow+maintenanceBudget is
	// already behind the real wall clock: a bounded pass must bail before
	// touching any item, not silently ignore the stale `now` and drain the
	// whole backlog like an unbounded (24h-deadline) pass would.
	staleNow := base.Add(-30 * time.Minute)
	if err := runNudgeQueueMaintenanceSweep(dir, staleNow); err != nil {
		t.Fatalf("runNudgeQueueMaintenanceSweep(staleNow): %v", err)
	}
	state, err := nudgequeue.LoadState(dir)
	if err != nil {
		t.Fatalf("LoadState after bounded pass: %v", err)
	}
	if len(state.Pending) != backlog {
		t.Fatalf("pending after bounded pass = %d, want %d (bounded pass must preserve the backlog, not drain it)", len(state.Pending), backlog)
	}
	if len(state.Dead) != 0 {
		t.Fatalf("dead after bounded pass = %d, want 0", len(state.Dead))
	}

	// A fresh `now` gives the pass a deadline safely in the future, so it
	// must fully converge the preserved backlog.
	if err := runNudgeQueueMaintenanceSweep(dir, time.Now()); err != nil {
		t.Fatalf("runNudgeQueueMaintenanceSweep(fresh now): %v", err)
	}
	state, err = nudgequeue.LoadState(dir)
	if err != nil {
		t.Fatalf("LoadState after convergence pass: %v", err)
	}
	if len(state.Pending) != 0 {
		t.Fatalf("pending after convergence pass = %d, want 0", len(state.Pending))
	}
	if len(state.Dead) != backlog {
		t.Fatalf("dead after convergence pass = %d, want %d", len(state.Dead), backlog)
	}
}

// TestQueuedNudgeMaintenanceDebounce_SkipsRedundantSameTickSweep guards
// ga-2kzci3 FR4: two maintenance sweeps against the same city whose `now`
// values fall within the debounce window must not both run the full
// recover/prune passes -- the second is a redundant same-tick sweep and
// should be skipped. A sweep whose `now` falls outside the window must run
// normally.
func TestQueuedNudgeMaintenanceDebounce_SkipsRedundantSameTickSweep(t *testing.T) {
	clearGCEnv(t)
	disableManagedDoltRecoveryForTest(t)

	dir := t.TempDir()
	t0 := time.Now()

	first := newQueuedNudge("ghost-1", "msg", t0.Add(-25*time.Hour))
	if err := enqueueQueuedNudge(dir, first); err != nil {
		t.Fatalf("enqueueQueuedNudge(first): %v", err)
	}
	if err := runNudgeQueueMaintenanceSweep(dir, t0); err != nil {
		t.Fatalf("runNudgeQueueMaintenanceSweep(t0): %v", err)
	}
	state, err := nudgequeue.LoadState(dir)
	if err != nil {
		t.Fatalf("LoadState after first sweep: %v", err)
	}
	if len(state.Pending) != 0 || len(state.Dead) != 1 {
		t.Fatalf("after first sweep: pending=%d dead=%d, want 0/1", len(state.Pending), len(state.Dead))
	}

	second := newQueuedNudge("ghost-2", "msg", t0.Add(-25*time.Hour))
	if err := enqueueQueuedNudge(dir, second); err != nil {
		t.Fatalf("enqueueQueuedNudge(second): %v", err)
	}

	// Within the debounce window: this sweep must be skipped entirely, so
	// the newly-enqueued (already-expired) second item stays pending.
	if err := runNudgeQueueMaintenanceSweep(dir, t0.Add(200*time.Millisecond)); err != nil {
		t.Fatalf("runNudgeQueueMaintenanceSweep(t0+200ms): %v", err)
	}
	state, err = nudgequeue.LoadState(dir)
	if err != nil {
		t.Fatalf("LoadState after debounced sweep: %v", err)
	}
	if len(state.Pending) != 1 || state.Pending[0].ID != second.ID {
		t.Fatalf("after debounced sweep: pending = %#v, want only %q untouched (redundant same-tick sweep should have been skipped)", state.Pending, second.ID)
	}
	if len(state.Dead) != 1 {
		t.Fatalf("after debounced sweep: dead = %d, want still 1", len(state.Dead))
	}

	// Past the debounce window: the sweep must run again and converge.
	if err := runNudgeQueueMaintenanceSweep(dir, t0.Add(2*time.Second)); err != nil {
		t.Fatalf("runNudgeQueueMaintenanceSweep(t0+2s): %v", err)
	}
	state, err = nudgequeue.LoadState(dir)
	if err != nil {
		t.Fatalf("LoadState after post-window sweep: %v", err)
	}
	if len(state.Pending) != 0 {
		t.Fatalf("after post-window sweep: pending = %d, want 0", len(state.Pending))
	}
	if len(state.Dead) != 2 {
		t.Fatalf("after post-window sweep: dead = %d, want 2", len(state.Dead))
	}
}

// TestShouldKeepNudgePollerAlive_DoesNotBlockOnHeldQueueLock guards ga-2kzci3
// FR5: whether a poller should stay alive is a liveness *read* and must not
// wait on the nudge queue's writer lock. Modeled on the sibling regression
// test for cmdNudgeStatus (ga-cn8dkj / PR #5408,
// TestCmdNudgeStatusDoesNotBlockOnHeldQueueLock) -- the same lock-free
// snapshot-read pattern applies here.
//
// Today, shouldKeepNudgePollerAlive reaches the queue through
// listQueuedNudgesForTarget -> withNudgeQueueState -> nudgequeue.WithState,
// which takes the city-wide *exclusive* flock and runs the full maintenance
// sweep under it. On a busy city that lock is contended, so this liveness
// check can block indefinitely instead of returning promptly.
func TestShouldKeepNudgePollerAlive_DoesNotBlockOnHeldQueueLock(t *testing.T) {
	clearGCEnv(t)
	disableManagedDoltRecoveryForTest(t)

	dir := t.TempDir()
	now := time.Now()
	item := newQueuedNudge("mayor", "review queued work", now.Add(-time.Minute))
	if err := enqueueQueuedNudge(dir, item); err != nil {
		t.Fatalf("enqueueQueuedNudge: %v", err)
	}

	// Hold the queue's exclusive lock, exactly as a concurrent maintenance
	// sweep does while it drains the backlog. flock conflicts are per open
	// file description, so this conflicts with shouldKeepNudgePollerAlive's
	// own separate open.
	lockFile, err := os.OpenFile(nudgequeue.LockPath(dir), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatalf("opening queue lock: %v", err)
	}
	defer lockFile.Close() //nolint:errcheck
	if err := syscall.Flock(int(lockFile.Fd()), syscall.LOCK_EX); err != nil {
		t.Fatalf("holding queue lock: %v", err)
	}
	defer syscall.Flock(int(lockFile.Fd()), syscall.LOCK_UN) //nolint:errcheck

	target := nudgeTarget{cityPath: dir, alias: "mayor"}

	done := make(chan bool, 1)
	go func() {
		done <- shouldKeepNudgePollerAlive(target, time.Time{}, now)
	}()

	select {
	case alive := <-done:
		if !alive {
			t.Fatal("shouldKeepNudgePollerAlive = false, want true (a pending item is queued for this agent)")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("shouldKeepNudgePollerAlive blocked on the held nudge-queue lock: liveness checks must be lock-free (ga-2kzci3)")
	}
}
