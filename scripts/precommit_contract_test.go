package scripts_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func runPrecommitContractCommand(dir string, env []string, name string, args ...string) ([]byte, error) {
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	cmd.Env = env
	return cmd.CombinedOutput()
}

func TestPreCommitFormatterPreservesFileMode(t *testing.T) {
	repoRoot := repoRoot(t)
	binDir := t.TempDir()
	fakeLint := filepath.Join(binDir, "golangci-lint")
	writeExecutable(t, fakeLint, `#!/usr/bin/env bash
set -euo pipefail
if [ "$#" -ne 2 ] || [ "$1" != "fmt" ] || [ "$2" != "--stdin" ]; then
  echo "unexpected golangci-lint args: $*" >&2
  exit 2
fi
cat
printf '\n'
`)

	source := filepath.Join(t.TempDir(), "needs_format.go")
	if err := os.WriteFile(source, []byte("package main"), 0o644); err != nil {
		t.Fatalf("write source: %v", err)
	}

	cmd := exec.Command(filepath.Join(repoRoot, "scripts", "precommit-format-staged-go"))
	cmd.Dir = repoRoot
	cmd.Env = []string{
		"PATH=" + binDir + string(os.PathListSeparator) + os.Getenv("PATH"),
		"HOME=" + t.TempDir(),
		"TMPDIR=" + t.TempDir(),
	}
	cmd.Stdin = strings.NewReader(source + "\n")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("precommit formatter failed: %v\n%s", err, out)
	}

	info, err := os.Stat(source)
	if err != nil {
		t.Fatalf("stat formatted source: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o644 {
		t.Fatalf("formatted source mode = %o, want 644", got)
	}
	content, err := os.ReadFile(source)
	if err != nil {
		t.Fatalf("read formatted source: %v", err)
	}
	if string(content) != "package main\n" {
		t.Fatalf("formatted content = %q, want package main with newline", content)
	}
}

func TestTestLocalJobCountUsesMachineAwareConcurrency(t *testing.T) {
	repoRoot := repoRoot(t)
	baseEnv := make([]string, 0, len(os.Environ()))
	for _, entry := range os.Environ() {
		if strings.HasPrefix(entry, "LOCAL_TEST_JOBS=") ||
			strings.HasPrefix(entry, "GC_TEST_LOCAL_CPUS=") ||
			strings.HasPrefix(entry, "GC_TEST_LOCAL_MEMORY_KIB=") ||
			strings.HasPrefix(entry, "GC_TEST_LOCAL_MEMINFO=") ||
			strings.HasPrefix(entry, "GC_TEST_LOCAL_PROC_CGROUP=") ||
			strings.HasPrefix(entry, "GC_TEST_LOCAL_CGROUP_ROOT=") {
			continue
		}
		baseEnv = append(baseEnv, entry)
	}
	tests := []struct {
		name            string
		cpus            string
		memoryKiB       string
		wantJobs        string
		cgroup          string
		memoryLimit     string
		memoryCurrent   string
		pidsLimit       string
		pidsCurrent     string
		pidsLeafLimit   string
		pidsLeafCurrent string
	}{
		{name: "large host uses automatic ceiling", cpus: "192", memoryKiB: "536870912", wantJobs: "16", cgroup: "v2", pidsLimit: "max", pidsCurrent: "0"},
		{name: "memory constrains fanout", cpus: "16", memoryKiB: "12582912", wantJobs: "3", cgroup: "v2", pidsLimit: "max", pidsCurrent: "0"},
		{name: "cpu constrains fanout", cpus: "2", memoryKiB: "67108864", wantJobs: "2", cgroup: "v2", pidsLimit: "max", pidsCurrent: "0"},
		{name: "small machine still runs one job", cpus: "8", memoryKiB: "2097152", wantJobs: "1", cgroup: "v2", pidsLimit: "max", pidsCurrent: "0"},
		{name: "unknown memory preserves safe fallback", cpus: "64", memoryKiB: "0", wantJobs: "3", cgroup: "v2", pidsLimit: "max", pidsCurrent: "0"},
		{name: "nested cgroup v2 memory ancestor constrains fanout", cpus: "16", wantJobs: "3", cgroup: "v2", memoryLimit: "12884901888", memoryCurrent: "0", pidsLimit: "max", pidsCurrent: "0"},
		{name: "nested cgroup v1 memory ancestor constrains fanout", cpus: "16", wantJobs: "2", cgroup: "v1", memoryLimit: "8589934592", memoryCurrent: "0", pidsLimit: "max", pidsCurrent: "0"},
		{name: "hybrid cgroup falls through to v1 memory controller", cpus: "16", wantJobs: "3", cgroup: "hybrid", memoryLimit: "12884901888", memoryCurrent: "0", pidsLimit: "max", pidsCurrent: "0"},
		{name: "exhausted memory cgroup forces one job", cpus: "16", wantJobs: "1", cgroup: "v2", memoryLimit: "4294967296", memoryCurrent: "4294967296", pidsLimit: "max", pidsCurrent: "0"},
		{name: "nested cgroup v2 pids ancestor constrains fanout", cpus: "16", memoryKiB: "67108864", wantJobs: "4", cgroup: "v2", pidsLimit: "8192", pidsCurrent: "4096"},
		{name: "nested cgroup v1 pids ancestor constrains fanout", cpus: "16", memoryKiB: "67108864", wantJobs: "2", cgroup: "v1", pidsLimit: "4096", pidsCurrent: "2048"},
		{name: "unlimited pids leaf still honors finite ancestor", cpus: "16", memoryKiB: "67108864", wantJobs: "4", cgroup: "v2", pidsLimit: "6144", pidsCurrent: "2048", pidsLeafLimit: "max", pidsLeafCurrent: "128"},
		{name: "exhausted pids cgroup forces one job", cpus: "16", memoryKiB: "67108864", wantJobs: "1", cgroup: "v2", pidsLimit: "4096", pidsCurrent: "4096"},
		{name: "malformed pids cgroup preserves safe fallback", cpus: "16", memoryKiB: "67108864", wantJobs: "3", cgroup: "v2", pidsLimit: "invalid", pidsCurrent: "0"},
		{name: "unknown pids budget preserves safe fallback", cpus: "16", memoryKiB: "67108864", wantJobs: "3"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := append(append([]string(nil), baseEnv...), "GC_TEST_LOCAL_CPUS="+tt.cpus)
			if tt.memoryKiB != "" {
				env = append(env, "GC_TEST_LOCAL_MEMORY_KIB="+tt.memoryKiB)
			}
			if tt.cgroup != "" {
				env = append(env, localTestCgroupEnv(
					t,
					tt.cgroup,
					tt.memoryLimit,
					tt.memoryCurrent,
					tt.pidsLimit,
					tt.pidsCurrent,
					tt.pidsLeafLimit,
					tt.pidsLeafCurrent,
				)...)
			} else {
				missingRoot := t.TempDir()
				env = append(
					env,
					"GC_TEST_LOCAL_PROC_CGROUP="+filepath.Join(missingRoot, "missing-proc-cgroup"),
					"GC_TEST_LOCAL_CGROUP_ROOT="+filepath.Join(missingRoot, "missing-cgroup-root"),
				)
			}
			out, err := runPrecommitContractCommand(
				repoRoot,
				env,
				filepath.Join(repoRoot, "scripts", "test-local-job-count"),
			)
			if err != nil {
				t.Fatalf("test-local-job-count failed: %v\n%s", err, out)
			}
			if got := strings.TrimSpace(string(out)); got != tt.wantJobs {
				t.Fatalf("test-local-job-count = %q, want %q", got, tt.wantJobs)
			}
		})
	}
}

func TestTestFastParallelDefersAutomaticConcurrencyUntilSliceEnrollment(t *testing.T) {
	repoRoot := repoRoot(t)
	baseEnv := make([]string, 0, len(os.Environ()))
	for _, entry := range os.Environ() {
		if !strings.HasPrefix(entry, "LOCAL_TEST_JOBS=") {
			baseEnv = append(baseEnv, entry)
		}
	}

	tests := []struct {
		name         string
		makeArgs     []string
		wantJobValue string
	}{
		{name: "automatic sizing remains unset for the runner", wantJobValue: ""},
		{name: "explicit override wins", makeArgs: []string{"LOCAL_TEST_JOBS=7"}, wantJobValue: "7"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			args := append([]string{"-n"}, tt.makeArgs...)
			args = append(args, "test-fast-parallel")
			out, err := runPrecommitContractCommand(repoRoot, baseEnv, "make", args...)
			if err != nil {
				t.Fatalf("make -n test-fast-parallel failed: %v\n%s", err, out)
			}
			command := string(out)
			if !strings.Contains(command, "env -i") {
				t.Fatalf("test-fast-parallel recipe should use TEST_ENV env -i wrapper:\n%s", command)
			}
			if !strings.Contains(command, "./scripts/test-local-parallel fast") {
				t.Fatalf("test-fast-parallel recipe should still dispatch the sharded fast runner:\n%s", command)
			}
			wantJobAssignment := " LOCAL_TEST_JOBS=" + tt.wantJobValue + " CMD_GC_PROCESS_TOTAL="
			if !strings.Contains(command, wantJobAssignment) {
				t.Fatalf("test-fast-parallel should pass LOCAL_TEST_JOBS=%q through unchanged:\n%s", tt.wantJobValue, command)
			}
		})
	}

	makefile, err := os.ReadFile(filepath.Join(repoRoot, "Makefile"))
	if err != nil {
		t.Fatalf("read Makefile: %v", err)
	}
	if strings.Contains(string(makefile), "$(shell ./scripts/test-local-job-count)") {
		t.Fatal("Makefile must not evaluate test-local-job-count before the runner enters its test slice")
	}

	runner, err := os.ReadFile(filepath.Join(repoRoot, "scripts", "test-local-parallel"))
	if err != nil {
		t.Fatalf("read test-local-parallel: %v", err)
	}
	content := string(runner)
	enrollAt := strings.Index(content, `gc_test_slice_reexec "$repo_root/scripts/test-local-parallel" "$@"`)
	detectAt := strings.Index(content, `local_jobs="${LOCAL_TEST_JOBS:-$("$repo_root/scripts/test-local-job-count")}"`)
	if enrollAt < 0 || detectAt < 0 || enrollAt >= detectAt {
		t.Fatal("test-local-parallel must detect automatic concurrency only after test-slice enrollment")
	}
}

func localTestCgroupEnv(
	t *testing.T,
	version, memoryLimit, memoryCurrent, pidsLimit, pidsCurrent, pidsLeafLimit, pidsLeafCurrent string,
) []string {
	t.Helper()
	root := t.TempDir()
	cgroupRoot := filepath.Join(root, "cgroup")
	procCgroup := filepath.Join(root, "proc-self-cgroup")
	meminfo := filepath.Join(root, "meminfo")
	writeTestFile(t, meminfo, "MemAvailable: 67108864 kB\n")

	var memoryRoot, pidsRoot, procLine string
	switch version {
	case "v2":
		memoryRoot = cgroupRoot
		pidsRoot = cgroupRoot
		procLine = "0::/parent/child\n"
	case "v1":
		memoryRoot = filepath.Join(cgroupRoot, "memory")
		pidsRoot = filepath.Join(cgroupRoot, "pids")
		procLine = "5:memory:/parent/child\n6:pids:/parent/child\n"
	case "hybrid":
		memoryRoot = filepath.Join(cgroupRoot, "memory")
		pidsRoot = filepath.Join(cgroupRoot, "unified")
		procLine = "0::/unified/child\n5:memory:/parent/child\n"
	default:
		t.Fatalf("unsupported cgroup fixture version %q", version)
	}

	writeTestFile(t, procCgroup, procLine)
	if memoryLimit != "" {
		memoryLimitFile := "memory.max"
		memoryCurrentFile := "memory.current"
		if version == "v1" || version == "hybrid" {
			memoryLimitFile = "memory.limit_in_bytes"
			memoryCurrentFile = "memory.usage_in_bytes"
		}
		if err := os.MkdirAll(filepath.Join(memoryRoot, "parent", "child"), 0o755); err != nil {
			t.Fatalf("create nested memory cgroup fixture: %v", err)
		}
		writeTestFile(t, filepath.Join(memoryRoot, "parent", memoryLimitFile), memoryLimit+"\n")
		writeTestFile(t, filepath.Join(memoryRoot, "parent", memoryCurrentFile), memoryCurrent+"\n")
	}
	if pidsLimit != "" {
		if err := os.MkdirAll(filepath.Join(pidsRoot, "parent", "child"), 0o755); err != nil {
			t.Fatalf("create nested pids cgroup fixture: %v", err)
		}
		writeTestFile(t, filepath.Join(pidsRoot, "parent", "pids.max"), pidsLimit+"\n")
		writeTestFile(t, filepath.Join(pidsRoot, "parent", "pids.current"), pidsCurrent+"\n")
	}
	if pidsLeafLimit != "" {
		writeTestFile(t, filepath.Join(pidsRoot, "parent", "child", "pids.max"), pidsLeafLimit+"\n")
		writeTestFile(t, filepath.Join(pidsRoot, "parent", "child", "pids.current"), pidsLeafCurrent+"\n")
	}

	return []string{
		"GC_TEST_LOCAL_MEMINFO=" + meminfo,
		"GC_TEST_LOCAL_PROC_CGROUP=" + procCgroup,
		"GC_TEST_LOCAL_CGROUP_ROOT=" + cgroupRoot,
	}
}

func TestPrePushUsesCanonicalMachineAwareConcurrency(t *testing.T) {
	repoRoot := repoRoot(t)
	script, err := os.ReadFile(filepath.Join(repoRoot, ".githooks", "pre-push"))
	if err != nil {
		t.Fatalf("read pre-push hook: %v", err)
	}
	content := string(script)
	if strings.Contains(content, `LOCAL_TEST_JOBS="${LOCAL_TEST_JOBS:-3}"`) {
		t.Fatal("pre-push hook must not replace the canonical machine-aware default with a fixed three-job cap")
	}
	if !strings.Contains(content, "exec make test-fast-parallel") {
		t.Fatal("pre-push hook must continue delegating the unchanged fast-suite inventory to make test-fast-parallel")
	}
	runner, err := os.ReadFile(filepath.Join(repoRoot, "scripts", "test-local-parallel"))
	if err != nil {
		t.Fatalf("read test-local-parallel: %v", err)
	}
	if !strings.Contains(string(runner), "scripts/test-local-job-count") {
		t.Fatal("test-local-parallel must use the canonical machine-aware job detector")
	}
}

func TestPreCommitRegeneratesDashboardClientOnSpecChange(t *testing.T) {
	repoRoot := repoRoot(t)
	script, err := os.ReadFile(filepath.Join(repoRoot, ".githooks", "pre-commit"))
	if err != nil {
		t.Fatalf("read pre-commit hook: %v", err)
	}
	content := string(script)

	npmBlockStart := strings.Index(content, "if command -v npm")
	if npmBlockStart < 0 {
		t.Fatal("pre-commit hook must guard dashboard regeneration on npm availability")
	}
	npmBlock := content[npmBlockStart:]

	genClientIdx := strings.Index(npmBlock, "npm run generate:client")
	if genClientIdx < 0 {
		t.Fatal("pre-commit hook must run 'npm run generate:client' when internal/api/openapi.json changes — " +
			"make dashboard-check only builds and typechecks against whatever client is already on disk, it never " +
			"regenerates it (that's make dashboard-ci's job, which the hook never calls). A spec-only commit " +
			"currently ships a stale generated TS client (see PR #4627, #4607)")
	}

	dashboardCheckIdx := strings.Index(npmBlock, "make dashboard-check")
	if dashboardCheckIdx < 0 {
		t.Fatal("pre-commit hook must still run make dashboard-check dashboard-smoke")
	}
	if genClientIdx > dashboardCheckIdx {
		t.Fatal("pre-commit hook must regenerate the dashboard client BEFORE typecheck/build, so a client that " +
			"doesn't match the new spec fails typecheck immediately instead of silently building against stale types")
	}

	clientAddNeedle := "git add internal/api/dashboardspa/web/shared/src/generated/gc-supervisor-client"
	genClientAddIdx := strings.Index(npmBlock, clientAddNeedle)
	if genClientAddIdx < 0 {
		t.Fatal("pre-commit hook must stage the regenerated dashboard client so a spec-only commit includes it")
	}
	if genClientAddIdx < genClientIdx {
		t.Fatal("pre-commit hook must stage the generated client after regenerating it, not before")
	}

	if strings.Contains(content, "regenerate the TS types, typecheck, and rebuild") {
		t.Fatal("pre-commit hook's dashboard block comment must not claim it regenerates the TS types unless it " +
			"actually calls npm run generate:client")
	}

	if !strings.Contains(content, `echo "warning: npm not on PATH`) {
		t.Fatal("pre-commit hook must still warn and no-op cleanly when npm is not on PATH")
	}
}

func TestPreCommitReachesDashboardBlockWhenOnlySpecFileStaged(t *testing.T) {
	repoRoot := repoRoot(t)
	hookPath := filepath.Join(repoRoot, ".githooks", "pre-commit")

	tmpRepo := t.TempDir()
	runGit := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = tmpRepo
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@test.invalid",
			"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@test.invalid",
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}

	specPath := filepath.Join(tmpRepo, "internal", "api", "openapi.json")
	clientPath := filepath.Join(tmpRepo, "internal", "api", "dashboardspa", "web", "shared", "src", "generated", "gc-supervisor-client")
	distPath := filepath.Join(tmpRepo, "internal", "api", "dashboardspa", "dist", "placeholder")

	runGit("init")
	writeTestFile(t, specPath, "{}\n")
	writeTestFile(t, clientPath, "placeholder\n")
	writeTestFile(t, distPath, "placeholder\n")
	runGit("add", "-A")
	runGit("commit", "-m", "init")

	// Stage ONLY a change to openapi.json -- no .go, web-src, or doc files
	// are staged, matching the reviewer's criterion-2 repro scenario.
	writeTestFile(t, specPath, `{"changed":true}`+"\n")
	runGit("add", "internal/api/openapi.json")

	binDir := t.TempDir()
	npmLog := filepath.Join(binDir, "npm.log")
	writeExecutable(t, filepath.Join(binDir, "npm"), `#!/usr/bin/env bash
set -euo pipefail
echo "$*" >> "`+npmLog+`"
exit 0
`)
	// Stub make: this test verifies the control-flow reaches the dashboard
	// block at all (the reviewer's criterion-2 gap), not the real
	// dashboard-check/dashboard-smoke targets, which need the full repo.
	writeExecutable(t, filepath.Join(binDir, "make"), `#!/usr/bin/env bash
exit 0
`)

	cmd := exec.Command("bash", hookPath)
	cmd.Dir = tmpRepo
	cmd.Env = []string{
		"PATH=" + binDir + string(os.PathListSeparator) + os.Getenv("PATH"),
		"HOME=" + t.TempDir(),
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("pre-commit hook failed: %v\n%s", err, out)
	}

	logContent, readErr := os.ReadFile(npmLog)
	if readErr != nil {
		t.Fatalf("pre-commit hook exited early and never invoked npm when only internal/api/openapi.json was "+
			"staged -- the go/web/docs early guard must not skip a spec-only commit (hook output: %s)", out)
	}
	if !strings.Contains(string(logContent), "generate:client") {
		t.Fatalf("pre-commit hook must run 'npm run generate:client' when only internal/api/openapi.json is "+
			"staged, got npm invocations:\n%s", logContent)
	}
}

func TestNativeDoltliteBeadsTargetRunsTaggedSuite(t *testing.T) {
	repoRoot := repoRoot(t)
	makefile, err := os.ReadFile(filepath.Join(repoRoot, "Makefile"))
	if err != nil {
		t.Fatalf("read Makefile: %v", err)
	}
	if err := validateNativeDoltliteMakefile(string(makefile)); err != nil {
		t.Fatalf("test-native-doltlite-beads recipe: %v", err)
	}

	cmd := exec.Command("make", "-n", "test-native-doltlite-beads")
	cmd.Dir = repoRoot
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("make -n test-native-doltlite-beads failed: %v\n%s", err, out)
	}
	command := string(out)
	if err := validateNativeDoltliteDryRun(command); err != nil {
		t.Fatalf("make -n test-native-doltlite-beads output: %v", err)
	}
	for _, want := range []string{
		"CGO_ENABLED=0",
		"-tags gascity_native_beads",
		"-run '^TestDoltlite'",
		"./internal/beads",
	} {
		if !strings.Contains(command, want) {
			t.Fatalf("test-native-doltlite-beads recipe missing %q:\n%s", want, command)
		}
	}
	for _, banned := range []string{
		"CGO_ENABLED=1",
		"cgo,gascity_native_beads",
	} {
		if strings.Contains(command, banned) {
			t.Fatalf("test-native-doltlite-beads recipe must not contain %q (doltlite store now uses pure-Go modernc):\n%s", banned, command)
		}
	}
	assertNativeDoltliteBeadsSelectionMatchesTaggedOwners(t, repoRoot)
}

func TestLocalParallelAllowlistIncludesObservableEnv(t *testing.T) {
	repoRoot := repoRoot(t)
	script, err := os.ReadFile(filepath.Join(repoRoot, "scripts", "test-local-parallel"))
	if err != nil {
		t.Fatalf("read test-local-parallel: %v", err)
	}
	content := string(script)
	for _, key := range []string{"OBSERVABLE_TEST_LOG", "OBSERVABLE_FAILURE_LINES"} {
		if !strings.Contains(content, key+"=") {
			t.Fatalf("test-local-parallel job env should pass through %s", key)
		}
	}
	for _, key := range []string{"GC_CITY", "GC_HOME", "GC_SESSION_ID"} {
		if strings.Contains(content, key+"=") {
			t.Fatalf("test-local-parallel job env must not pass through live session env %s", key)
		}
	}
}

func repoRoot(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	return filepath.Dir(wd)
}

func writeExecutable(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatalf("write executable %s: %v", path, err)
	}
}

func writeTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("create parent for %s: %v", path, err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
