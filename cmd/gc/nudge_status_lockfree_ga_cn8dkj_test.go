package main

import (
	"bytes"
	"os"
	"syscall"
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/nudgequeue"
)

// TestCmdNudgeStatusDoesNotBlockOnHeldQueueLock pins that `gc nudge status` is a
// lock-free reader of the persisted nudge queue.
//
// Regression test for ga-cn8dkj: cmdNudgeStatus reached the queue through
// listQueuedNudgesForTarget -> withNudgeQueueState -> nudgequeue.WithState,
// which takes a city-wide *exclusive* flock (internal/nudgequeue/state.go:111)
// and then runs the full maintenance sweep -- spawning serial `bd` subprocesses
// -- under that lock. On a busy city the lock is permanently contended, so a
// read-only status call blocked in flock(2) and returned NO output and NO
// error until the caller's own timeout killed it.
//
// Status only reads. It must never wait on the queue's writer lock.
func TestCmdNudgeStatusDoesNotBlockOnHeldQueueLock(t *testing.T) {
	t.Setenv("GC_BEADS", "file")
	cityDir := t.TempDir()
	writeNamedSessionCityTOML(t, cityDir)
	t.Setenv("GC_CITY", cityDir)

	// Seed the queue so nudgeQueueHasWork() is true (an empty queue would skip
	// the maintenance path and could mask the block).
	now := time.Now().Add(-time.Minute)
	if err := enqueueQueuedNudge(cityDir, newQueuedNudge("mayor", "review queued work", now)); err != nil {
		t.Fatalf("enqueueQueuedNudge: %v", err)
	}

	// Hold the queue's exclusive lock, exactly as a concurrent `gc nudge poll`
	// tick does while it drains the backlog. flock conflicts are per open file
	// description, so this conflicts with the command's own separate open.
	lockFile, err := os.OpenFile(nudgequeue.LockPath(cityDir), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatalf("opening queue lock: %v", err)
	}
	defer lockFile.Close() //nolint:errcheck
	if err := syscall.Flock(int(lockFile.Fd()), syscall.LOCK_EX); err != nil {
		t.Fatalf("holding queue lock: %v", err)
	}
	defer syscall.Flock(int(lockFile.Fd()), syscall.LOCK_UN) //nolint:errcheck

	type result struct {
		code   int
		stdout string
		stderr string
	}
	done := make(chan result, 1)
	go func() {
		var stdout, stderr bytes.Buffer
		code := cmdNudgeStatus([]string{"mayor"}, true, &stdout, &stderr)
		done <- result{code: code, stdout: stdout.String(), stderr: stderr.String()}
	}()

	select {
	case got := <-done:
		if got.code != 0 {
			t.Fatalf("cmdNudgeStatus = %d, want 0; stderr=%s", got.code, got.stderr)
		}
		if got.stdout == "" {
			t.Fatalf("cmdNudgeStatus produced no output while the queue lock was held")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("cmdNudgeStatus blocked on the held nudge-queue lock: status is a read and must not wait on the writer flock (ga-cn8dkj)")
	}
}
