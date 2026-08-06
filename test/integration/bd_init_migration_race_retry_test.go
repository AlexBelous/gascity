//go:build integration

package integration

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

const (
	// bdInitMigrationRaceMaxAttempts bounds how many times an external
	// `bd init` subprocess is retried after losing the dolt
	// schema-migration race described below. Matches the bound used by
	// the equivalent in-process retry in internal/beads/factory.go
	// (openNativeStoreWithMigrationRaceRetry).
	bdInitMigrationRaceMaxAttempts = 3
	// bdInitMigrationRaceBackoff is the delay between retry attempts.
	bdInitMigrationRaceBackoff = 25 * time.Millisecond
	// bdInitMigrationRaceSignature is the dolt schema-migration race
	// signature this retries, here on the external `bd` CLI subprocess
	// path rather than the in-process Go store-open path:
	// startSharedDoltServer and startDoltServerOnAllInterfaces only
	// probe TCP readiness, not Dolt's internal migration completion, so
	// the immediately-following `bd init` can start against a server
	// that is up but still mid-migration. Must stay in sync with
	// internal/beads's nativeOpenMigrationRaceSignature.
	bdInitMigrationRaceSignature = "pre-existing dirty tables changed during schema migration"
)

// runWithBDInitMigrationRaceRetry runs an external `bd init` subprocess via
// run, retrying when its combined output matches
// bdInitMigrationRaceSignature, up to bdInitMigrationRaceMaxAttempts total
// attempts. A success, a non-matching failure, or context cancellation all
// return immediately without retrying.
func runWithBDInitMigrationRaceRetry(ctx context.Context, run func() ([]byte, error)) ([]byte, error) {
	var out []byte
	var err error
	for attempt := 1; attempt <= bdInitMigrationRaceMaxAttempts; attempt++ {
		out, err = run()
		if err == nil {
			return out, nil
		}
		if !strings.Contains(string(out), bdInitMigrationRaceSignature) {
			return out, err
		}
		if attempt == bdInitMigrationRaceMaxAttempts {
			break
		}
		select {
		case <-ctx.Done():
			return out, err
		case <-time.After(bdInitMigrationRaceBackoff):
		}
	}
	return out, err
}

// These tests exercise runWithBDInitMigrationRaceRetry directly through an
// injected fake, rather than a real dolt server, because the migration race
// it recovers from is timing-dependent: startSharedDoltServer and
// startDoltServerOnAllInterfaces only probe TCP readiness, not Dolt's
// internal migration completion, so a real reproduction is not
// deterministic. See runBDInit (bdstore_test.go) and runBDInitCompat
// (dolt_config_test.go) for the real subprocess call sites this wraps.

func TestRunWithBDInitMigrationRaceRetryRetriesAndSucceeds(t *testing.T) {
	calls := 0
	run := func() ([]byte, error) {
		calls++
		if calls < 3 {
			return []byte("Error: failed to initialize schema: schema migration: pre-existing dirty tables changed during schema migration"), errors.New("exit status 1")
		}
		return []byte("bd initialized"), nil
	}

	out, err := runWithBDInitMigrationRaceRetry(context.Background(), run)
	if err != nil {
		t.Fatalf("runWithBDInitMigrationRaceRetry() error = %v, want nil", err)
	}
	if string(out) != "bd initialized" {
		t.Fatalf("output = %q, want %q", out, "bd initialized")
	}
	if calls != 3 {
		t.Fatalf("calls = %d, want 3", calls)
	}
}

func TestRunWithBDInitMigrationRaceRetryExhaustsRetriesAndReturnsError(t *testing.T) {
	calls := 0
	raceOut := []byte("Error: failed to initialize schema: schema migration: pre-existing dirty tables changed during schema migration")
	run := func() ([]byte, error) {
		calls++
		return raceOut, errors.New("exit status 1")
	}

	out, err := runWithBDInitMigrationRaceRetry(context.Background(), run)
	if err == nil {
		t.Fatal("runWithBDInitMigrationRaceRetry() error = nil, want non-nil after exhausting retries")
	}
	if string(out) != string(raceOut) {
		t.Fatalf("output = %q, want %q", out, raceOut)
	}
	if calls != bdInitMigrationRaceMaxAttempts {
		t.Fatalf("calls = %d, want %d (bounded retry, not unbounded)", calls, bdInitMigrationRaceMaxAttempts)
	}
}

func TestRunWithBDInitMigrationRaceRetryNonMatchingErrorDoesNotRetry(t *testing.T) {
	calls := 0
	run := func() ([]byte, error) {
		calls++
		return []byte("Error: unknown flag: --bogus"), errors.New("exit status 2")
	}

	_, err := runWithBDInitMigrationRaceRetry(context.Background(), run)
	if err == nil {
		t.Fatal("runWithBDInitMigrationRaceRetry() error = nil, want non-nil")
	}
	if calls != 1 {
		t.Fatalf("calls = %d, want 1 (non-matching error must not retry)", calls)
	}
}

func TestRunWithBDInitMigrationRaceRetryRespectsContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	calls := 0
	raceOut := []byte("Error: failed to initialize schema: schema migration: pre-existing dirty tables changed during schema migration")
	run := func() ([]byte, error) {
		calls++
		cancel()
		return raceOut, errors.New("exit status 1")
	}

	start := time.Now()
	_, err := runWithBDInitMigrationRaceRetry(ctx, run)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("runWithBDInitMigrationRaceRetry() error = nil, want non-nil")
	}
	if calls != 1 {
		t.Fatalf("calls = %d, want 1 (cancellation must short-circuit the retry loop)", calls)
	}
	if elapsed >= bdInitMigrationRaceBackoff {
		t.Fatalf("elapsed = %s, want < backoff %s (must not sleep through a cancelled context)", elapsed, bdInitMigrationRaceBackoff)
	}
}
