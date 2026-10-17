package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/execenv"
)

func TestNudgePollerCommandDoesNotInheritWorkerIdentity(t *testing.T) {
	dir := t.TempDir()
	snapshot := filepath.Join(dir, "child.env")
	spy := filepath.Join(dir, "gc-child-spy")
	keys := []string{"GC_SESSION_ID", "GC_SESSION_NAME", "GC_ALIAS", "GC_AGENT", "GC_TEMPLATE", "GC_SESSION_ORIGIN", "GC_RUNTIME_EPOCH", "GC_CONTINUATION_EPOCH", "GC_INSTANCE_TOKEN", "BEADS_HOLDER_TOKEN", "BEADS_ACTOR", "CLAUDE_CODE_SESSION_ID"}
	script := "#!/bin/sh\n" +
		"printf '%s\\n' \"$GC_DISABLE_USAGE_METRICS\" \"$BD_DISABLE_METRICS\" \"$OTEL_SERVICE_NAME\" \"$GC_CITY_PATH\" \"$GC_CITY\" \"$FOREIGN_API_KEY\" > \"$GC_TEST_POLLER_ENV_SPY\"\n" +
		"for key in " + strings.Join(keys, " ") + "; do\n" +
		"  eval \"present=\\${$key+x}\"; printf '%s=%s\\n' \"$key\" \"$present\" >> \"$GC_TEST_POLLER_ENV_SPY\"\n" +
		"done\n" +
		"printf 'arg=%s\\n' \"$@\" >> \"$GC_TEST_POLLER_ENV_SPY\"\n"
	if err := os.WriteFile(spy, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, key := range keys {
		t.Setenv(key, "fixture-worker-identity")
	}
	t.Setenv("GC_TEST_POLLER_ENV_SPY", snapshot)
	t.Setenv("GC_CITY_PATH", dir)
	t.Setenv("GC_CITY", dir)
	t.Setenv("FOREIGN_API_KEY", "fixture-ordinary-credential")
	t.Setenv(execenv.UsageMetricsDisableEnv, "0")
	t.Setenv("BD_DISABLE_METRICS", "keep-beads-setting")
	t.Setenv("OTEL_SERVICE_NAME", "keep-otel-setting")

	// This is ensureNudgePoller's real construction seam. Only the executable
	// is a finite fixture; no gc poller, provider or queue is opened. Start and
	// synchronously reap it here instead of the launcher's production Release.
	cmd := nudgePollerCommand(spy, dir, "target-sid", "target-provider")
	if cmd.SysProcAttr == nil || !cmd.SysProcAttr.Setpgid {
		t.Fatal("poller construction lost its existing detached-process contract")
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(hangBudget):
		_ = cmd.Process.Kill() // Only this test's owned finite spy.
		<-done
		t.Fatal("owned poller-construction spy did not exit")
	}
	data, err := os.ReadFile(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
	want := []string{execenv.UsageMetricsDisableValue, "keep-beads-setting", "keep-otel-setting", dir, dir, "fixture-ordinary-credential"}
	for _, key := range keys {
		want = append(want, key+"=")
	}
	for _, arg := range []string{"nudge", "poll", "--city", dir, "--session", "target-provider", "target-sid"} {
		want = append(want, "arg="+arg)
	}
	if !slices.Equal(got, want) {
		t.Fatalf("poller construction environment/target = %#v, want %#v", got, want)
	}
}

func TestProductMetricsChildEnvAdapterCanonicalizesExplicitEnvironment(t *testing.T) {
	cmd := exec.Command("unused")
	cmd.Env = []string{
		"KEEP=first",
		execenv.UsageMetricsDisableEnv + "=0",
		"BD_DISABLE_METRICS=1",
		"OTEL_SERVICE_NAME=gascity-test",
		execenv.UsageMetricsDisableEnv + "=yes",
		"KEEP=second",
	}
	want := []string{
		"KEEP=first",
		"BD_DISABLE_METRICS=1",
		"OTEL_SERVICE_NAME=gascity-test",
		"KEEP=second",
		execenv.UsageMetricsDisabledEntry,
	}

	disableProductMetricsForChild(cmd)
	if !slices.Equal(cmd.Env, want) {
		t.Fatalf("child environment = %#v, want %#v", cmd.Env, want)
	}
	disableProductMetricsForChild(cmd)
	if !slices.Equal(cmd.Env, want) {
		t.Fatalf("child environment after second application = %#v, want %#v", cmd.Env, want)
	}
}

func TestProductMetricsChildEnvAdapterMaterializesInheritedEnvironment(t *testing.T) {
	t.Setenv(execenv.UsageMetricsDisableEnv, "0")
	t.Setenv("BD_DISABLE_METRICS", "keep-beads-setting")
	t.Setenv("OTEL_SERVICE_NAME", "keep-otel-setting")

	cmd := exec.Command("unused")
	if cmd.Env != nil {
		t.Fatalf("new command Env = %#v, want nil inheritance marker", cmd.Env)
	}
	disableProductMetricsForChild(cmd)

	for _, want := range []string{
		execenv.UsageMetricsDisabledEntry,
		"BD_DISABLE_METRICS=keep-beads-setting",
		"OTEL_SERVICE_NAME=keep-otel-setting",
	} {
		if !slices.Contains(cmd.Env, want) {
			t.Fatalf("materialized child environment missing %q", want)
		}
	}
	count := 0
	for _, entry := range cmd.Env {
		if strings.HasPrefix(entry, execenv.UsageMetricsDisableEnv+"=") {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("materialized child environment has %d %s entries, want exactly one", count, execenv.UsageMetricsDisableEnv)
	}
}

func TestProductMetricsChildEnvAdapterUsesCmdEnvironForDirPWD(t *testing.T) {
	if runtime.GOOS == "windows" || runtime.GOOS == "plan9" {
		t.Skip("os/exec does not synthesize PWD from Cmd.Dir on this platform")
	}
	staleDir := t.TempDir()
	childDir := t.TempDir()
	t.Setenv("PWD", staleDir)

	cmd := exec.Command("unused")
	cmd.Dir = childDir
	disableProductMetricsForChild(cmd)

	pwdEntries := 0
	for _, entry := range cmd.Env {
		if strings.HasPrefix(entry, "PWD=") {
			pwdEntries++
			if entry != "PWD="+childDir {
				t.Fatalf("child PWD entry = %q, want %q", entry, "PWD="+childDir)
			}
		}
	}
	if pwdEntries != 1 {
		t.Fatalf("child environment has %d PWD entries, want exactly one", pwdEntries)
	}
}

func TestProductMetricsChildEnvAdapterPreservesExplicitEmptyEnvironment(t *testing.T) {
	t.Setenv("H1_AMBIENT_SENTINEL", "must-not-be-inherited")
	cmd := exec.Command("unused")
	cmd.Dir = t.TempDir()
	cmd.Env = []string{}

	disableProductMetricsForChild(cmd)

	want := []string{execenv.UsageMetricsDisabledEntry}
	if !slices.Equal(cmd.Env, want) {
		t.Fatalf("explicit-empty child environment = %#v, want %#v", cmd.Env, want)
	}
}
