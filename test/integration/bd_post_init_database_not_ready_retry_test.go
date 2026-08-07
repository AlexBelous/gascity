//go:build integration

package integration

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/cenkalti/backoff/v4"
)

const (
	// bdPostInitDatabaseNotReadyInitialInterval is the starting backoff
	// interval for retrying the first external `bd` subprocess issued
	// against a freshly-initialized workspace after losing the
	// success-without-ready-database race described below. Matches the
	// InitialInterval that beads' own `bd init --server` uses internally
	// (github.com/steveyegge/beads internal/storage/dolt/store.go,
	// openServerConnection) to wait out the same underlying Dolt server
	// catalog-visibility race (upstream beads issue GH-1851) before it will
	// even report success. A subsequent, independent `bd` process's first
	// connection needs a comparable budget to reliably observe the same
	// condition clear — the previous fixed 3×25ms budget was undersized
	// against that upstream-established real-world worst case by roughly
	// two orders of magnitude.
	bdPostInitDatabaseNotReadyInitialInterval = 100 * time.Millisecond
	// bdPostInitDatabaseNotReadyMaxElapsedTime bounds the total retry
	// window. Matches the MaxElapsedTime beads' own internal retry uses for
	// the identical race (see bdPostInitDatabaseNotReadyInitialInterval).
	bdPostInitDatabaseNotReadyMaxElapsedTime = 10 * time.Second
	// bdPostInitDatabaseNotReadySignature is the success-without-ready-
	// database race signature this retries: `bd init --server ...` returns
	// exit 0 before the database it just created is visible to a fresh
	// connection, so the next `bd` command against that workspace fails
	// with "database \"<prefix>\" not found on Dolt server at <host:port>".
	// Matched on the host:port suffix only, so it applies regardless of
	// which prefix (mc/bd/dc) the caller initialized.
	bdPostInitDatabaseNotReadySignature = "not found on Dolt server at"
)

// runWithBDPostInitDatabaseNotReadyRetry runs an external `bd` subprocess
// (the first one issued against a freshly-initialized workspace) via run,
// retrying with exponential backoff when its combined output matches
// bdPostInitDatabaseNotReadySignature, up to
// bdPostInitDatabaseNotReadyMaxElapsedTime total. A success, a non-matching
// failure, or context cancellation all return immediately without
// retrying.
func runWithBDPostInitDatabaseNotReadyRetry(ctx context.Context, run func() ([]byte, error)) ([]byte, error) {
	return runWithBDPostInitDatabaseNotReadyRetryBackoff(ctx, run,
		bdPostInitDatabaseNotReadyInitialInterval, bdPostInitDatabaseNotReadyMaxElapsedTime)
}

// runWithBDPostInitDatabaseNotReadyRetryBackoff is
// runWithBDPostInitDatabaseNotReadyRetry with injectable timing, so tests
// can exercise the retry and give-up paths without waiting out the
// production backoff schedule.
func runWithBDPostInitDatabaseNotReadyRetryBackoff(ctx context.Context, run func() ([]byte, error), initialInterval, maxElapsedTime time.Duration) ([]byte, error) {
	var out []byte
	bo := backoff.NewExponentialBackOff()
	bo.InitialInterval = initialInterval
	bo.MaxElapsedTime = maxElapsedTime

	err := backoff.Retry(func() error {
		var runErr error
		out, runErr = run()
		if runErr == nil {
			return nil
		}
		if !strings.Contains(string(out), bdPostInitDatabaseNotReadySignature) {
			return backoff.Permanent(runErr)
		}
		return runErr
	}, backoff.WithContext(bo, ctx))

	return out, err
}

// These tests exercise runWithBDPostInitDatabaseNotReadyRetry (and its
// injectable-timing variant) directly through an injected fake, rather than
// a real dolt server, because the race it recovers from is timing-dependent
// and not deterministically reproducible: `bd init --server ...` can return
// success (exit 0) before the database it just created is actually
// visible/queryable on the shared Dolt server — the CLI subprocess exits
// once its own local workspace config is written, without confirming the
// CREATE DATABASE has propagated to where a fresh connection can see it.
// The very next `bd` command issued against that workspace then fails with
// `database "<prefix>" not found on Dolt server at <host:port>`. This is a
// different race than bdInitMigrationRaceSignature
// (bd_init_migration_race_retry_test.go), which retries a *failing* `bd
// init` itself; this one retries the first command *after* a `bd init` that
// already reported success. See configureCustomTypes (bdstore_test.go) and
// the `bd create` call in TestDoltConfigWiringExternalHost
// (dolt_config_test.go) for the real subprocess call sites this wraps.

func TestRunWithBDPostInitDatabaseNotReadyRetryRetriesAndSucceeds(t *testing.T) {
	calls := 0
	run := func() ([]byte, error) {
		calls++
		if calls < 3 {
			return []byte(`Error: failed to open database: database "mc" not found on Dolt server at 127.0.0.1:43033`), errors.New("exit status 1")
		}
		return []byte("bead created"), nil
	}

	out, err := runWithBDPostInitDatabaseNotReadyRetry(context.Background(), run)
	if err != nil {
		t.Fatalf("runWithBDPostInitDatabaseNotReadyRetry() error = %v, want nil", err)
	}
	if string(out) != "bead created" {
		t.Fatalf("output = %q, want %q", out, "bead created")
	}
	if calls != 3 {
		t.Fatalf("calls = %d, want 3", calls)
	}
}

func TestRunWithBDPostInitDatabaseNotReadyRetryExhaustsRetriesAndReturnsError(t *testing.T) {
	calls := 0
	raceOut := []byte(`Error: failed to open database: database "bd" not found on Dolt server at 127.0.0.1:43033`)
	run := func() ([]byte, error) {
		calls++
		return raceOut, errors.New("exit status 1")
	}

	// Injected fast timing (not the production 100ms/10s budget) keeps this
	// test quick while still proving the retry is exponential-backoff-based
	// and genuinely bounded, not unbounded.
	start := time.Now()
	out, err := runWithBDPostInitDatabaseNotReadyRetryBackoff(context.Background(), run,
		2*time.Millisecond, 50*time.Millisecond)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("runWithBDPostInitDatabaseNotReadyRetryBackoff() error = nil, want non-nil after exhausting retries")
	}
	if err.Error() != "exit status 1" {
		t.Fatalf("error = %q, want the real operation error %q to surface (not a generic backoff/timeout error)", err, "exit status 1")
	}
	if string(out) != string(raceOut) {
		t.Fatalf("output = %q, want %q", out, raceOut)
	}
	if calls < 2 {
		t.Fatalf("calls = %d, want >= 2 (must retry at least once, not give up after one attempt)", calls)
	}
	if elapsed >= time.Second {
		t.Fatalf("elapsed = %s, want < 1s (retry must still be bounded by MaxElapsedTime, not run away)", elapsed)
	}
}

func TestRunWithBDPostInitDatabaseNotReadyRetryNonMatchingErrorDoesNotRetry(t *testing.T) {
	calls := 0
	run := func() ([]byte, error) {
		calls++
		return []byte("Error: unknown flag: --bogus"), errors.New("exit status 2")
	}

	_, err := runWithBDPostInitDatabaseNotReadyRetry(context.Background(), run)
	if err == nil {
		t.Fatal("runWithBDPostInitDatabaseNotReadyRetry() error = nil, want non-nil")
	}
	if calls != 1 {
		t.Fatalf("calls = %d, want 1 (non-matching error must not retry)", calls)
	}
}

func TestRunWithBDPostInitDatabaseNotReadyRetryRespectsContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	calls := 0
	raceOut := []byte(`Error: failed to open database: database "dc" not found on Dolt server at 127.0.0.1:43033`)
	run := func() ([]byte, error) {
		calls++
		cancel()
		return raceOut, errors.New("exit status 1")
	}

	start := time.Now()
	_, err := runWithBDPostInitDatabaseNotReadyRetry(ctx, run)
	elapsed := time.Since(start)

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled (cancellation must surface as the returned error)", err)
	}
	if calls != 1 {
		t.Fatalf("calls = %d, want 1 (cancellation must short-circuit the retry loop)", calls)
	}
	if elapsed >= 100*time.Millisecond {
		t.Fatalf("elapsed = %s, want < 100ms (must not sleep through a cancelled context)", elapsed)
	}
}
