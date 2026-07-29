package beads

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/gastownhall/gascity/internal/beads/contract"
)

// writeScopeMetadata writes a .beads/metadata.json under scope and returns scope.
func writeScopeMetadata(t *testing.T, scope, json string) {
	t.Helper()
	dir := filepath.Join(scope, ".beads")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir .beads: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "metadata.json"), []byte(json), 0o600); err != nil {
		t.Fatalf("write metadata.json: %v", err)
	}
}

// fixtureResolver is an injected PostgresPasswordResolver that records the
// endpoint it was asked about and returns a fixed non-secret password.
func fixtureResolver(pw string, seen *PostgresEndpoint) PostgresPasswordResolver {
	return func(_ string, endpoint PostgresEndpoint) (string, error) {
		if seen != nil {
			*seen = endpoint
		}
		return pw, nil
	}
}

func TestNativePostgresResolveDSN(t *testing.T) {
	const fakePassword = "fixture-pw-not-a-secret"

	t.Run("storage endpoint form resolves via injected resolver", func(t *testing.T) {
		scope := t.TempDir()
		writeScopeMetadata(t, scope, `{"backend":"postgres","project_id":"prj-1","storage_endpoint":"postgres://ro_user@db.example.com:5432?sslmode=verify-full","storage_database":"bd_prj_test"}`)
		var seen PostgresEndpoint
		dsn, err := resolveNativePostgresDSN(scope, func(string) string { return "" }, fixtureResolver(fakePassword, &seen))
		if err != nil {
			t.Fatalf("resolve: %v", err)
		}
		if dsn.baseURL != "postgres://ro_user@db.example.com:5432?sslmode=verify-full" {
			t.Errorf("baseURL = %q", dsn.baseURL)
		}
		if dsn.password != fakePassword {
			t.Errorf("password not resolved via injected resolver")
		}
		if dsn.database != "bd_prj_test" || dsn.projectID != "prj-1" {
			t.Errorf("database/projectID = %q/%q", dsn.database, dsn.projectID)
		}
		if seen.Host != "db.example.com" || seen.Port != "5432" || seen.User != "ro_user" {
			t.Errorf("resolver saw endpoint %+v", seen)
		}
	})

	t.Run("default port when endpoint omits it", func(t *testing.T) {
		scope := t.TempDir()
		writeScopeMetadata(t, scope, `{"backend":"postgres","storage_endpoint":"postgres://ro_user@db.example.com?sslmode=verify-full","storage_database":"bd_prj_test"}`)
		var seen PostgresEndpoint
		if _, err := resolveNativePostgresDSN(scope, func(string) string { return "" }, fixtureResolver(fakePassword, &seen)); err != nil {
			t.Fatalf("resolve: %v", err)
		}
		if seen.Port != "5432" {
			t.Errorf("default port not applied: %q", seen.Port)
		}
	})

	t.Run("storage_endpoint wins over a process-global BEADS_POSTGRES_URL", func(t *testing.T) {
		// Per-scope safety: a lingering global URL must NOT redirect a scope
		// that has its own storage_endpoint to another database.
		scope := t.TempDir()
		writeScopeMetadata(t, scope, `{"backend":"postgres","storage_endpoint":"postgres://ro_user@db.example.com:5432","storage_database":"bd_prj_test"}`)
		getenv := func(k string) string {
			if k == BeadsPostgresURLEnv {
				return "postgres://u:embedded@override.example.com:5433/other?sslmode=require"
			}
			return ""
		}
		resolverCalled := false
		dsn, err := resolveNativePostgresDSN(scope, getenv, func(_ string, _ PostgresEndpoint) (string, error) {
			resolverCalled = true
			return fakePassword, nil
		})
		if err != nil {
			t.Fatalf("resolve: %v", err)
		}
		if dsn.baseURL != "postgres://ro_user@db.example.com:5432" {
			t.Errorf("storage_endpoint did not win over URL: %q", dsn.baseURL)
		}
		if !resolverCalled {
			t.Error("resolver should have been consulted for the storage_endpoint form")
		}
	})

	t.Run("BEADS_POSTGRES_URL only when the scope has no storage_endpoint", func(t *testing.T) {
		scope := t.TempDir()
		writeScopeMetadata(t, scope, `{"backend":"postgres"}`)
		full := "postgres://u:embedded@only.example.com:5433/db?sslmode=require"
		getenv := func(k string) string {
			if k == BeadsPostgresURLEnv {
				return full
			}
			return ""
		}
		dsn, err := resolveNativePostgresDSN(scope, getenv, func(_ string, _ PostgresEndpoint) (string, error) {
			return "", errors.New("resolver must not be called for the URL form")
		})
		if err != nil {
			t.Fatalf("resolve: %v", err)
		}
		if dsn.baseURL != full || dsn.password != "" {
			t.Errorf("URL fallback not honored: %+v", dsn)
		}
	})

	t.Run("missing storage_endpoint and no URL is an error", func(t *testing.T) {
		scope := t.TempDir()
		writeScopeMetadata(t, scope, `{"backend":"postgres"}`)
		if _, err := resolveNativePostgresDSN(scope, func(string) string { return "" }, fixtureResolver(fakePassword, nil)); err == nil {
			t.Fatal("expected error for missing storage_endpoint")
		}
	})

	t.Run("nil resolver rejects the storage_endpoint form", func(t *testing.T) {
		scope := t.TempDir()
		writeScopeMetadata(t, scope, `{"backend":"postgres","storage_endpoint":"postgres://ro_user@db.example.com:5432"}`)
		if _, err := resolveNativePostgresDSN(scope, func(string) string { return "" }, nil); err == nil {
			t.Fatal("expected error when no credential resolver is configured")
		}
	})
}

// failingPGPool is a pgxQuerier stub whose every query fails, used to drive the
// fallback-on-error path without a live database.
type failingPGPool struct {
	err     error
	queries int
	closed  bool
}

func (p *failingPGPool) Query(context.Context, string, ...any) (pgx.Rows, error) {
	p.queries++
	return nil, p.err
}

func (p *failingPGPool) Close() { p.closed = true }

func fallbackBdRunner(calls *[][]string) CommandRunner {
	const bead = `[{"id":"mc-1","title":"T","status":"open","issue_type":"task","priority":1,` +
		`"created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-01T00:00:00Z"}]`
	return func(_, name string, args ...string) ([]byte, error) {
		*calls = append(*calls, append([]string{name}, args...))
		if name != "bd" || len(args) == 0 {
			return nil, fmt.Errorf("unexpected command %s %v", name, args)
		}
		switch args[0] {
		case "show", "list", "ready":
			return []byte(bead), nil
		case "dep":
			return []byte(`[]`), nil
		default:
			return nil, fmt.Errorf("unexpected bd subcommand %q", args[0])
		}
	}
}

func TestNativePostgresReadFallbackOnError(t *testing.T) {
	var calls [][]string
	bd := NewBdStore(t.TempDir(), fallbackBdRunner(&calls))
	pool := &failingPGPool{err: errors.New("pool unavailable")}
	store, err := OpenNativePostgresReadStore(
		context.Background(), t.TempDir(), bd,
		withNativePostgresPool(pool),
		WithNativePostgresLogger(nil),
	)
	if err != nil {
		t.Fatalf("open: %v", err)
	}

	t.Run("Get falls back to bd", func(t *testing.T) {
		got, err := store.Get("mc-1")
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if got.ID != "mc-1" {
			t.Errorf("Get returned %q", got.ID)
		}
		assertRan(t, calls, "show")
	})

	t.Run("List falls back to bd", func(t *testing.T) {
		got, err := store.List(ListQuery{AllowScan: true})
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		if len(got) != 1 {
			t.Fatalf("List returned %d beads", len(got))
		}
		assertRan(t, calls, "list")
	})

	t.Run("Ready falls back to bd", func(t *testing.T) {
		got, err := store.Ready()
		if err != nil {
			t.Fatalf("Ready: %v", err)
		}
		if len(got) != 1 {
			t.Fatalf("Ready returned %d beads", len(got))
		}
		assertRan(t, calls, "ready")
	})

	t.Run("DepList falls back to bd", func(t *testing.T) {
		if _, err := store.DepList("mc-1", "down"); err != nil {
			t.Fatalf("DepList: %v", err)
		}
		assertRan(t, calls, "dep")
	})

	t.Run("Count reports unsupported so callers List-fallback", func(t *testing.T) {
		_, err := store.Count(context.Background(), ListQuery{AllowScan: true})
		if !errors.Is(err, ErrCountUnsupported) {
			t.Fatalf("Count error = %v, want ErrCountUnsupported", err)
		}
	})

	if pool.queries == 0 {
		t.Error("native pool was never queried; fallback did not attempt native first")
	}

	if err := store.CloseStore(); err != nil {
		t.Fatalf("CloseStore: %v", err)
	}
	if !pool.closed {
		t.Error("CloseStore did not close the pool")
	}
}

func TestNativePostgresReadActivated(t *testing.T) {
	scope := t.TempDir()
	writeScopeMetadata(t, scope, `{"backend":"postgres","storage_endpoint":"postgres://u@h:5432"}`)

	t.Setenv(contract.NativePostgresReadEnv, "")
	if NativePostgresReadActivated(scope) {
		t.Error("flag unset should be inactive")
	}

	t.Setenv(contract.NativePostgresReadEnv, "1")
	if !NativePostgresReadActivated(scope) {
		t.Error("flag on + postgres endpoint should be active")
	}

	dolt := t.TempDir()
	writeScopeMetadata(t, dolt, `{"backend":"dolt","dolt_database":"mc"}`)
	if NativePostgresReadActivated(dolt) {
		t.Error("dolt backend should be inactive even with the flag on")
	}

	missing := t.TempDir()
	if NativePostgresReadActivated(missing) {
		t.Error("scope without metadata should be inactive")
	}
}

func assertRan(t *testing.T, calls [][]string, sub string) {
	t.Helper()
	for _, c := range calls {
		if len(c) >= 2 && c[0] == "bd" && c[1] == sub {
			return
		}
	}
	t.Errorf("expected a bd %q call in %v", sub, calls)
}
