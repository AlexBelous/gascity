package bd

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// A verified-empty init must not silently grant remote migration authority or
// use force; a failed init must restore the pre-seeded metadata stub.
func TestBdFreshInitMigrationFlagFence(t *testing.T) {
	source, err := PackFS.ReadFile("assets/scripts/gc-beads-bd.sh")
	if err != nil {
		t.Fatal(err)
	}
	function := regexp.MustCompile(`(?ms)^run_bd_init_pinned_over_verified_empty\(\)\s*\{.*?\n\}`).Find(source)
	if len(function) == 0 {
		t.Fatal("missing verified-empty init")
	}
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "failure"}[fail], func(t *testing.T) {
			dir := t.TempDir()
			if err := os.MkdirAll(filepath.Join(dir, ".beads"), 0o700); err != nil {
				t.Fatal(err)
			}
			metadata := filepath.Join(dir, ".beads", "metadata.json")
			if err := os.WriteFile(metadata, []byte("stub"), 0o600); err != nil {
				t.Fatal(err)
			}
			program := string(function) + `
DOLT_PORT=1234
run_bd_pinned() { printf '%s|%s\n' "${BD_ALLOW_REMOTE_MIGRATE:-unset}" "$*" > "$CAPTURE"; [ "$FAIL" != true ]; }
dump_bd_init_forensics() { :; }
die() { exit 99; }
run_bd_init_pinned_over_verified_empty "$WORK" gc testdb localhost
`
			capture := filepath.Join(t.TempDir(), "calls")
			cmd := exec.Command("bash", "-c", program)
			env := slices.DeleteFunc(os.Environ(), func(v string) bool { return strings.HasPrefix(v, "BD_ALLOW_REMOTE_MIGRATE=") })
			cmd.Env = append(env, "WORK="+dir, "CAPTURE="+capture, "FAIL="+map[bool]string{false: "false", true: "true"}[fail])
			out, runErr := cmd.CombinedOutput()
			if (runErr != nil) != fail {
				t.Fatalf("unexpected result: %v: %s", runErr, out)
			}
			calls, err := os.ReadFile(capture)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.HasPrefix(string(calls), "unset|") || strings.Contains(string(calls), "--force") || strings.Contains(string(calls), "--external") {
				t.Fatalf("unexpected authority or argv: %s", calls)
			}
			data, err := os.ReadFile(metadata)
			if fail && (err != nil || string(data) != "stub") {
				t.Fatalf("failed init did not restore stub: %q %v", data, err)
			}
		})
	}
}
