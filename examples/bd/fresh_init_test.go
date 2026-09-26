package bd

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The migration opt-in belongs to one init of a database proven absent before
// registration and empty afterward. Existing schema-less stores must not get it.
func TestBdFreshInitMigrationFlagFence(t *testing.T) {
	scriptBytes, err := PackFS.ReadFile("assets/scripts/gc-beads-bd.sh")
	if err != nil {
		t.Fatal(err)
	}
	source := string(scriptBytes)
	functions := []string{"fresh_bd_database_absent", "fresh_bd_database_empty", "run_bd_init_pinned"}
	var program strings.Builder
	for _, name := range functions {
		program.WriteString(extractFreshInitShellFunction(t, source, name))
		program.WriteByte('\n')
	}
	program.WriteString(`
set -eu
DATA_DIR="$TEST_DATA_DIR"
DOLT_PORT=12345
DOLT_USER=root
DOLT_PASSWORD=
valid_sql_name() { [ "$1" = testdb ]; }
server_reachable() { return 0; }
connect_host() { printf '127.0.0.1\n'; }
database_exists() { [ "${DB_EXISTS:-0}" = 1 ]; }
dolt() {
    [ "${SQL_FAIL:-0}" != 1 ] || return 1
    case "$*" in
        *information_schema.schemata*) printf 'schema_count\n%s\n' "${DB_EXISTS:-0}" ;;
        *information_schema.tables*) printf 'table_count\n%s\n' "${TABLE_COUNT:-0}" ;;
        *) return 1 ;;
    esac
}

run_bd_pinned() {
    printf '%s|%s\n' "${BD_ALLOW_REMOTE_MIGRATE:-unset}" "$*" >> "$TEST_CAPTURE"
    case "$*" in *' ready') [ "${TEST_READY_FAIL:-0}" != 1 ] ;; esac
}
wait_for_bd_runtime_schema() { printf 'schema\n' >> "$TEST_CAPTURE"; }
normalize_scope_after_init() { printf 'normalize\n' >> "$TEST_CAPTURE"; }
ensure_project_identity() { printf 'identity\n' >> "$TEST_CAPTURE"; }
verify_bd_project_identity() { printf 'verify\n' >> "$TEST_CAPTURE"; }
write_bd_current_version_witness() {
    printf 'witness\n' >> "$TEST_CAPTURE"
    : > "$TEST_WORK/.beads/.local_version"
}
die() { printf '%s\n' "$*" >&2; exit 99; }
case "$TEST_CASE" in
    fresh|fresh_nonempty|fresh_ready_failure)
        fresh_bd_database_absent testdb || exit 41
        DB_EXISTS=1
        run_bd_init_pinned "$TEST_WORK" testdb testdb 127.0.0.1 true true
        ;;
    existing_empty|existing_nonempty|catalog_error)
        if fresh_bd_database_absent testdb; then exit 42; fi
        run_bd_init_pinned "$TEST_WORK" testdb testdb 127.0.0.1 true false
        ;;
esac
`)

	for _, tc := range []struct {
		name, exists, tables, sqlFail string
		wantMigration                 bool
		wantError                     bool
		readyFail                     string
		wantWitness                   bool
	}{
		{"fresh", "0", "0", "0", true, false, "0", true},
		{"fresh_ready_failure", "0", "0", "0", true, true, "1", true},
		{"fresh_nonempty", "0", "3", "0", false, true, "0", false},
		{"existing_empty", "1", "0", "0", false, false, "0", false},
		{"existing_nonempty", "1", "3", "0", false, false, "0", false},
		{"catalog_error", "0", "0", "1", false, false, "0", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			work := t.TempDir()
			if err := os.MkdirAll(filepath.Join(work, ".beads"), 0o700); err != nil {
				t.Fatal(err)
			}
			data := t.TempDir()
			capture := filepath.Join(t.TempDir(), "calls")
			if err := os.WriteFile(capture, nil, 0o600); err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command("bash", "-c", program.String())
			cmd.Env = append(os.Environ(),
				"TEST_CASE="+tc.name, "TEST_WORK="+work,
				"TEST_DATA_DIR="+data, "TEST_CAPTURE="+capture,
				"DB_EXISTS="+tc.exists, "TABLE_COUNT="+tc.tables,
				"SQL_FAIL="+tc.sqlFail,
				"TEST_READY_FAIL="+tc.readyFail,
			)
			out, runErr := cmd.CombinedOutput()
			if tc.wantError && runErr == nil {
				t.Fatalf("unsafe fresh init unexpectedly succeeded:\n%s", out)
			}
			if !tc.wantError && runErr != nil {
				t.Fatalf("shell fence failed: %v\n%s", runErr, out)
			}
			calls, err := os.ReadFile(capture)
			if err != nil {
				t.Fatal(err)
			}
			migration := strings.Contains(string(calls), "1|"+work+" init --force --quiet --server --external")
			if migration != tc.wantMigration {
				t.Fatalf("migration flag = %v, want %v; calls:\n%s", migration, tc.wantMigration, calls)
			}
			if !tc.wantMigration && strings.Contains(string(calls), "1|") {
				t.Fatalf("existing or unverified database received migration authority:\n%s", calls)
			}
			_, witnessErr := os.Stat(filepath.Join(work, ".beads", ".local_version"))
			if (witnessErr == nil) != tc.wantWitness {
				t.Fatalf("validated version witness present = %v, want %v; calls:\n%s", witnessErr == nil, tc.wantWitness, calls)
			}
			if tc.wantMigration {
				last := -1
				for _, marker := range []string{" init --force", "schema\n", "normalize\n", "identity\n", "verify\n", "witness\n", " ready\n"} {
					at := strings.Index(string(calls), marker)
					if at <= last {
						t.Fatalf("fresh init proof order missing %q after offset %d:\n%s", marker, last, calls)
					}
					last = at
				}
			}
		})
	}
}

func extractFreshInitShellFunction(t *testing.T, script, name string) string {
	t.Helper()
	pattern := regexp.MustCompile(`(?ms)^` + regexp.QuoteMeta(name) + `\(\)\s*\{.*?\n\}`)
	function := pattern.FindString(script)
	if function == "" {
		t.Fatalf("missing shell function %q", name)
	}
	return function
}
