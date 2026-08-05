package beads

import (
	"math/rand"
	"strings"
	"time"
)

// Retry tuning for bd's workspace-gate contention (workspacegate.ErrBusy):
// a SHARED acquire fails fast (non-blocking) when an EXCLUSIVE maintenance
// holder (dolt migrate/restore) is active on the workspace. The holder
// releases when its own operation completes, so a short bounded retry rides
// out incidental overlap without masking a real bd error as transient
// (ga-20zoji Cause 1 -- naive retry-until-green is explicitly the
// anti-pattern that issue warns against, so this only retries the one
// literal, confirmed-transient condition below).
const (
	// gateBusyMaxAttempts bounds the retry loop (no infinite loop).
	gateBusyMaxAttempts = 4
	// gateBusyBaseBackoff is the first backoff step; it doubles per attempt
	// and is jittered, mirroring the conditionalWriteBackoff idiom in
	// bdstore_conditional.go.
	gateBusyBaseBackoff = 25 * time.Millisecond
)

// gateBusySleep is the backoff seam; tests override it to a no-op so
// contention/exhaustion cases don't actually sleep.
var gateBusySleep = func(d time.Duration) { time.Sleep(d) }

// IsWorkspaceGateBusyError reports whether err is bd's workspace-gate
// contention error (workspacegate.ErrBusy, literal string "workspace gate
// busy", confirmed against beads@eada50a02 source). Message-substring
// based, never a Go sentinel, because bd is invoked as an external process
// via CommandRunner -- matching the isBdTransientWriteError/
// isBdSqliteBusyError convention.
func IsWorkspaceGateBusyError(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), "workspace gate busy")
}

// gateBusyBackoff returns the jittered backoff for the attempt-th retry:
// gateBusyBaseBackoff doubled per attempt, then equal-jittered to spread
// concurrent racers (same shape as conditionalWriteBackoff).
func gateBusyBackoff(attempt int) time.Duration {
	base := gateBusyBaseBackoff << (attempt - 1)
	return base/2 + time.Duration(rand.Int63n(int64(base)/2+1))
}

// RunWithGateBusyRetry runs fn, retrying up to gateBusyMaxAttempts times
// with jittered-exponential backoff ONLY when fn's error is bd's
// workspace-gate contention error -- any other failure, including on the
// very first attempt, surfaces immediately unretried so a real bd error is
// never masked as transient.
func RunWithGateBusyRetry(fn func() ([]byte, error)) ([]byte, error) {
	var out []byte
	var err error
	for attempt := 1; attempt <= gateBusyMaxAttempts; attempt++ {
		out, err = fn()
		if err == nil || !IsWorkspaceGateBusyError(err) || attempt == gateBusyMaxAttempts {
			return out, err
		}
		gateBusySleep(gateBusyBackoff(attempt))
	}
	return out, err
}
