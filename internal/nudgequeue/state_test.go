package nudgequeue

import (
	"os"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/clock"
)

// TestWithStateBounded_TimesOutInsteadOfBlockingForever guards ga-2kzci3 FR1/FR2:
// a caller that cannot acquire the queue lock within its budget must get a
// clear, actionable error, never an unbounded hang.
func TestWithStateBounded_TimesOutInsteadOfBlockingForever(t *testing.T) {
	cityPath := t.TempDir()

	if err := WithState(cityPath, func(state *State) error {
		state.Pending = append(state.Pending, Item{ID: "seed"})
		return nil
	}); err != nil {
		t.Fatalf("seed queue state: %v", err)
	}

	lockFile, err := os.OpenFile(LockPath(cityPath), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatalf("open lock file: %v", err)
	}
	defer lockFile.Close() //nolint:errcheck
	if err := syscall.Flock(int(lockFile.Fd()), syscall.LOCK_EX); err != nil {
		t.Fatalf("hold queue lock: %v", err)
	}
	defer syscall.Flock(int(lockFile.Fd()), syscall.LOCK_UN) //nolint:errcheck

	const waitTimeout = 150 * time.Millisecond
	done := make(chan error, 1)
	var called bool
	var mu sync.Mutex
	go func() {
		done <- withStateBounded(cityPath, waitTimeout, clock.Real{}, func(_ *State) error {
			mu.Lock()
			called = true
			mu.Unlock()
			return nil
		})
	}()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("withStateBounded returned nil error while queue lock was held; want a timeout error")
		}
		lower := strings.ToLower(err.Error())
		mentionsLock := strings.Contains(lower, "lock")
		mentionsTimeout := strings.Contains(lower, "timeout") || strings.Contains(lower, "timed out")
		if !mentionsLock || !mentionsTimeout {
			t.Fatalf("withStateBounded error = %q, want a descriptive lock-timeout error (mentioning lock + timeout)", err)
		}
		mu.Lock()
		gotCalled := called
		mu.Unlock()
		if gotCalled {
			t.Fatal("withStateBounded invoked fn after failing to acquire the lock")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("withStateBounded blocked far past its wait timeout while the queue lock was held (unbounded wait regression)")
	}
}
