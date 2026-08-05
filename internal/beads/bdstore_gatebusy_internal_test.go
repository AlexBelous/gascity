package beads

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// TestIsWorkspaceGateBusyError exercises the string classifier that detects
// bd's workspace-gate contention error (workspacegate.ErrBusy, confirmed
// against beads@eada50a02 source as the literal string "workspace gate
// busy"). Classification is message-substring based, never a Go sentinel,
// because bd is invoked as an external process via CommandRunner -- matching
// the existing isBdTransientWriteError/isBdSqliteBusyError convention.
func TestIsWorkspaceGateBusyError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"nil error", nil, false},
		{
			"shared-acquire wrapped message",
			errors.New("exit status 1: a maintenance operation is running on this workspace; retry when it completes: workspace gate busy"),
			true,
		},
		{
			"exclusive-acquire wrapped message",
			errors.New("exit status 1: other bd commands are using this workspace; wait for them to finish and retry: workspace gate busy"),
			true,
		},
		{"bare marker", errors.New("workspace gate busy"), true},
		{
			"unrelated httpapi busy error must not false-positive",
			errors.New("exit status 1: server busy"),
			false,
		},
		{
			"sqlite busy is a different transient class",
			errors.New("exit status 1: database is locked"),
			false,
		},
		{
			"generic bd error",
			errors.New("exit status 1: bead not found: ga-999"),
			false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsWorkspaceGateBusyError(tt.err); got != tt.want {
				t.Fatalf("IsWorkspaceGateBusyError(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}

// TestGateBusyBackoffIsBoundedAndJittered pins the jittered-exponential
// shape shared with conditionalWriteBackoff (bdstore_conditional.go): each
// attempt's backoff falls within [base/2, base] where base doubles per
// attempt from gateBusyBaseBackoff.
func TestGateBusyBackoffIsBoundedAndJittered(t *testing.T) {
	for attempt := 1; attempt <= 3; attempt++ {
		base := gateBusyBaseBackoff << (attempt - 1)
		for i := 0; i < 20; i++ {
			got := gateBusyBackoff(attempt)
			if got < base/2 || got > base {
				t.Fatalf("attempt %d: gateBusyBackoff() = %v, want in [%v, %v]", attempt, got, base/2, base)
			}
		}
	}
}

// disableGateBusySleep no-ops the backoff seam for the duration of the test
// and restores it after, mirroring disableConditionalWriteSleep. Tests that
// touch this package-level seam must not run in parallel.
func disableGateBusySleep(t *testing.T) {
	t.Helper()
	prev := gateBusySleep
	gateBusySleep = func(time.Duration) {}
	t.Cleanup(func() { gateBusySleep = prev })
}

// TestRunWithGateBusyRetrySucceedsAfterTransientBusy proves the retry loop
// rides out incidental gate contention: fn fails twice with the gate-busy
// error, then succeeds -- the caller sees the successful result, not the
// transient failures.
func TestRunWithGateBusyRetrySucceedsAfterTransientBusy(t *testing.T) {
	disableGateBusySleep(t)
	var calls int
	fn := func() ([]byte, error) {
		calls++
		if calls < 3 {
			return nil, errors.New("exit status 1: workspace gate busy")
		}
		return []byte("ok"), nil
	}
	out, err := RunWithGateBusyRetry(fn)
	if err != nil {
		t.Fatalf("RunWithGateBusyRetry: got err %v, want nil after transient busy resolves", err)
	}
	if string(out) != "ok" {
		t.Fatalf("RunWithGateBusyRetry: out = %q, want %q", out, "ok")
	}
	if calls != 3 {
		t.Fatalf("fn call count = %d, want 3 (2 busy + 1 success)", calls)
	}
}

// TestRunWithGateBusyRetrySuccessOnFirstAttemptNoSleep guards against an
// off-by-one that sleeps before the first attempt or calls fn more than once
// when it succeeds immediately.
func TestRunWithGateBusyRetrySuccessOnFirstAttemptNoSleep(t *testing.T) {
	var slept bool
	prev := gateBusySleep
	gateBusySleep = func(time.Duration) { slept = true }
	t.Cleanup(func() { gateBusySleep = prev })

	var calls int
	fn := func() ([]byte, error) {
		calls++
		return []byte("ok"), nil
	}
	if _, err := RunWithGateBusyRetry(fn); err != nil {
		t.Fatalf("RunWithGateBusyRetry: got err %v, want nil", err)
	}
	if calls != 1 {
		t.Fatalf("fn call count = %d, want 1 (no retry needed)", calls)
	}
	if slept {
		t.Fatal("RunWithGateBusyRetry slept even though fn succeeded on the first attempt")
	}
}

// TestRunWithGateBusyRetryNeverRetriesNonGateBusyError proves a real bd
// error is never masked as transient: fn fails once with an unrelated
// error, and the retry loop must surface it immediately without a second
// attempt. Naive retry-until-green is the anti-pattern ga-20zoji itself
// warns against.
func TestRunWithGateBusyRetryNeverRetriesNonGateBusyError(t *testing.T) {
	disableGateBusySleep(t)
	var calls int
	wantErr := errors.New("exit status 1: bead not found: ga-999")
	fn := func() ([]byte, error) {
		calls++
		return nil, wantErr
	}
	_, err := RunWithGateBusyRetry(fn)
	if !errors.Is(err, wantErr) {
		t.Fatalf("RunWithGateBusyRetry: got err %v, want %v surfaced unchanged", err, wantErr)
	}
	if calls != 1 {
		t.Fatalf("fn call count = %d, want 1 (non-gate-busy errors must never retry)", calls)
	}
}

// TestRunWithGateBusyRetryExhaustionSurfacesRaw proves the retry is
// bounded: if fn ALWAYS fails with the gate-busy error (e.g. a stuck
// maintenance operation holding the gate EXCLUSIVE), the loop must stop at
// gateBusyMaxAttempts and surface the raw gate-busy error rather than
// looping forever or masking it as something else.
func TestRunWithGateBusyRetryExhaustionSurfacesRaw(t *testing.T) {
	disableGateBusySleep(t)
	var calls int
	fn := func() ([]byte, error) {
		calls++
		return nil, errors.New("exit status 1: workspace gate busy")
	}
	_, err := RunWithGateBusyRetry(fn)
	if err == nil || !strings.Contains(err.Error(), "workspace gate busy") {
		t.Fatalf("RunWithGateBusyRetry exhaustion: got err %v, want raw workspace-gate-busy error surfaced", err)
	}
	if calls != gateBusyMaxAttempts {
		t.Fatalf("fn call count = %d, want exactly gateBusyMaxAttempts (%d)", calls, gateBusyMaxAttempts)
	}
}
