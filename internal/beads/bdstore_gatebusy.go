package beads

import "time"

// RED stub (ga-n1z9ev): signatures only, no retry/classification logic yet.
// Filled in during the GREEN step.

const (
	gateBusyMaxAttempts = 4
	gateBusyBaseBackoff = 25 * time.Millisecond
)

var gateBusySleep = func(d time.Duration) { time.Sleep(d) }

// IsWorkspaceGateBusyError reports whether err is bd's workspace-gate
// contention error (workspacegate.ErrBusy, literal string "workspace gate
// busy").
func IsWorkspaceGateBusyError(_ error) bool {
	return false
}

// gateBusyBackoff returns the jittered backoff for the attempt-th retry.
func gateBusyBackoff(_ int) time.Duration {
	return 0
}

// RunWithGateBusyRetry runs fn, retrying with jittered-exponential backoff
// only when fn's error is bd's workspace-gate contention error.
func RunWithGateBusyRetry(fn func() ([]byte, error)) ([]byte, error) {
	return fn()
}
