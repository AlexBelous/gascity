package scripts_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const bdToolCarrierProbe = `printf '%s|%s|%s\n' "${GC_ACCEPTANCE_BD_BIN-}" "${GC_DOLT_PORT-}" "${BEADS_DOLT_SERVER_PORT-}"`

func bdToolCarrierEnv() []string {
	return append(os.Environ(), "GC_ACCEPTANCE_BD_BIN=/fixture/native-bd", "GC_DOLT_PORT=62888", "BEADS_DOLT_SERVER_PORT=62889")
}

func TestMakeTestEnvPreservesExplicitBDWithoutLiveEndpoints(t *testing.T) {
	probe := filepath.Join(t.TempDir(), "carrier.mk")
	// Make consumes one dollar; the child shell must see the original expansion.
	recipe := ".PHONY: carrier-probe\ncarrier-probe:\n\t$(TEST_ENV) sh -c '" + strings.ReplaceAll(strings.ReplaceAll(bdToolCarrierProbe, "'", `'"'"'`), "$", "$$") + "'\n"
	if err := os.WriteFile(probe, []byte(recipe), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := makeCommand("-s", "-f", "Makefile", "-f", probe, "carrier-probe")
	cmd.Dir = repoRoot(t)
	cmd.Env = bdToolCarrierEnv()
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("make env probe failed: %v\n%s", err, out)
	}
	if got := strings.TrimSpace(string(out)); got != "/fixture/native-bd||" {
		t.Fatalf("tool carrier/live isolation = %q", got)
	}
}

func TestLocalWorkerEnvPreservesExplicitBDWithoutLiveEndpoints(t *testing.T) {
	source := localParallelScript(t)
	function := strings.Index(source, "run_fan_out()")
	if function < 0 {
		t.Fatal("worker function missing")
	}
	body := source[function:]
	start := strings.Index(body, "env -i")
	if start < 0 {
		t.Fatal("worker env boundary missing")
	}
	body = body[start:]
	end := strings.Index(body, `bash -lc "$command"`)
	if end < 0 {
		t.Fatal("worker command boundary missing")
	}
	prefix := body[:end+len("bash -lc")]
	cmd := exec.Command("bash", "-c", prefix+" '"+strings.ReplaceAll(bdToolCarrierProbe, "'", `'"'"'`)+"'")
	cmd.Dir = repoRoot(t)
	cmd.Env = bdToolCarrierEnv()
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("worker env probe failed: %v\n%s", err, out)
	}
	if got := strings.TrimSpace(string(out)); got != "/fixture/native-bd||" {
		t.Fatalf("tool carrier/live isolation = %q", got)
	}
}
