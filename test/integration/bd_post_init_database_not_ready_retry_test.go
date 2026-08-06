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
	// bdPostInitDatabaseNotReadyMaxAttempts bounds how many times the first
	// external `bd` subprocess issued against a freshly-initialized
	// workspace is retried after losing the success-without-ready-database
	// race described below. Matches the bound used by the sibling
	// bdInitMigrationRaceMaxAttempts retry for consistency across the two
	// external-bd CLI races.
	bdPostInitDatabaseNotReadyMaxAttempts = 3
	// bdPostInitDatabaseNotReadyBackoff is the delay between retry attempts.
	bdPostInitDatabaseNotReadyBackoff = 25 * time.Millisecond
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
// retrying when its combined output matches
// bdPostInitDatabaseNotReadySignature, up to
// bdPostInitDatabaseNotReadyMaxAttempts total attempts. A success, a
// non-matching failure, or context cancellation all return immediately
// without retrying.
func runWithBDPostInitDatabaseNotReadyRetry(ctx context.Context, run func() ([]byte, error)) ([]byte, error) {
	var out []byte
	var err error
	for attempt := 1; attempt <= bdPostInitDatabaseNotReadyMaxAttempts; attempt++ {
		out, err = run()
		if err == nil {
			return out, nil
		}
		if !strings.Contains(string(out), bdPostInitDatabaseNotReadySignature) {
			return out, err
		}
		if attempt == bdPostInitDatabaseNotReadyMaxAttempts {
			break
		}
		select {
		case <-ctx.Done():
			return out, err
		case <-time.After(bdPostInitDatabaseNotReadyBackoff):
		}
	}
	return out, err
}

// These tests exercise runWithBDPostInitDatabaseNotReadyRetry directly
// through an injected fake, rather than a real dolt server, because the
// race it recovers from is timing-dependent and not deterministically
// reproducible: `bd init --server ...` can return success (exit 0) before
// the database it just created is actually visible/queryable on the shared
// Dolt server — the CLI subprocess exits once its own local workspace
// config is written, without confirming the CREATE DATABASE has propagated
// to where a fresh connection can see it. The very next `bd` command issued
// against that workspace then fails with `database "<prefix>" not found on
// Dolt server at <host:port>`. This is a different race than
// bdInitMigrationRaceSignature (bd_init_migration_race_retry_test.go),
// which retries a *failing* `bd init` itself; this one retries the first
// command *after* a `bd init` that already reported success. See
// configureCustomTypes (bdstore_test.go) and the `bd create` call in
// TestDoltConfigWiringExternalHost (dolt_config_test.go) for the real
// subprocess call sites this wraps.

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

	out, err := runWithBDPostInitDatabaseNotReadyRetry(context.Background(), run)
	if err == nil {
		t.Fatal("runWithBDPostInitDatabaseNotReadyRetry() error = nil, want non-nil after exhausting retries")
	}
	if string(out) != string(raceOut) {
		t.Fatalf("output = %q, want %q", out, raceOut)
	}
	if calls != bdPostInitDatabaseNotReadyMaxAttempts {
		t.Fatalf("calls = %d, want %d (bounded retry, not unbounded)", calls, bdPostInitDatabaseNotReadyMaxAttempts)
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

	if err == nil {
		t.Fatal("runWithBDPostInitDatabaseNotReadyRetry() error = nil, want non-nil")
	}
	if calls != 1 {
		t.Fatalf("calls = %d, want 1 (cancellation must short-circuit the retry loop)", calls)
	}
	if elapsed >= bdPostInitDatabaseNotReadyBackoff {
		t.Fatalf("elapsed = %s, want < backoff %s (must not sleep through a cancelled context)", elapsed, bdPostInitDatabaseNotReadyBackoff)
	}
}
