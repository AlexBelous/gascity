package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
)

//nolint:unused // exercised by native_dolt_rebind_integration_test.go
var (
	testGCBinaryOnce sync.Once
	testGCBinaryPath string
	testGCBinaryErr  error
)

// reexecGCTestBinaryForTests returns the current Go test executable through a
// symlink named gc. TestMain recognizes that basename and dispatches the
// supplied arguments through the real CLI command tree without rebuilding gc.
func reexecGCTestBinaryForTests(t *testing.T) string {
	t.Helper()
	testExecutable, err := os.Executable()
	if err != nil {
		t.Fatalf("resolve test executable: %v", err)
	}
	gcPath := filepath.Join(t.TempDir(), "gc")
	if err := os.Symlink(testExecutable, gcPath); err != nil {
		t.Fatalf("symlink test executable as gc: %v", err)
	}
	return gcPath
}

//nolint:unused // exercised by native_dolt_rebind_integration_test.go
func currentGCBinaryForTests(t *testing.T) string {
	t.Helper()
	testGCBinaryOnce.Do(func() {
		sweepOrphanPIDPrefixedDirs(os.TempDir(), testGCBinaryDirPrefix)
		buildDir, err := os.MkdirTemp("", pidPrefixedTempPattern(testGCBinaryDirPrefix))
		if err != nil {
			testGCBinaryErr = fmt.Errorf("mktemp gc binary dir: %w", err)
			return
		}
		realBinPath := filepath.Join(buildDir, "gc-real")
		binPath := filepath.Join(buildDir, "gc")
		wd, err := os.Getwd()
		if err != nil {
			testGCBinaryErr = fmt.Errorf("getwd: %w", err)
			return
		}
		cmd := exec.Command("go", "build", "-o", realBinPath, ".")
		cmd.Dir = wd
		out, err := cmd.CombinedOutput()
		if err != nil {
			testGCBinaryErr = fmt.Errorf("go build -o %s .: %w\n%s", realBinPath, err, string(out))
			return
		}
		if err := writeManagedDoltTestGCWrapper(binPath, realBinPath); err != nil {
			testGCBinaryErr = fmt.Errorf("write gc test wrapper: %w", err)
			return
		}
		testGCBinaryPath = binPath
	})
	if testGCBinaryErr != nil {
		t.Fatal(testGCBinaryErr)
	}
	return testGCBinaryPath
}

// currentGCBinaryForTestsWithProcessScanRoot builds a real gc binary whose
// process-table scanner sees only the supplied controlled procfs fixture. It
// is used by real tmux journeys that must prove exact absence without making
// their result depend on unrelated same-UID processes running on the test host.
func currentGCBinaryForTestsWithProcessScanRoot(t *testing.T, processScanRoot string) string {
	t.Helper()
	processScanRoot, err := filepath.Abs(processScanRoot)
	if err != nil {
		t.Fatalf("resolve controlled process-scan root: %v", err)
	}
	info, err := os.Stat(processScanRoot)
	if err != nil || !info.IsDir() {
		t.Fatalf("controlled process-scan root %q is not a directory: info=%v err=%v", processScanRoot, info, err)
	}
	buildDir := t.TempDir()
	realBinPath := filepath.Join(buildDir, "gc-real")
	binPath := filepath.Join(buildDir, "gc")
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	const scanRootSymbol = "github.com/gastownhall/gascity/internal/runtime/proctable.scanRoot"
	cmd := exec.Command("go", "build", "-ldflags", "-X="+scanRootSymbol+"="+processScanRoot, "-o", realBinPath, ".")
	cmd.Dir = wd
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go build controlled-process-scan gc binary: %v\n%s", err, out)
	}
	if err := writeManagedDoltTestGCWrapper(binPath, realBinPath); err != nil {
		t.Fatalf("write controlled-process-scan gc wrapper: %v", err)
	}
	return binPath
}

func writeManagedDoltTestGCWrapper(binPath, realBinPath string) error {
	wrapper := fmt.Sprintf("#!/bin/sh\nexport %s=1\nif [ -z \"${%s:-}\" ]; then\n  export %s=$PPID\nfi\nexec %q \"$@\"\n",
		managedDoltTestModeEnv,
		managedDoltTestParentPIDEnv,
		managedDoltTestParentPIDEnv,
		realBinPath,
	)
	return os.WriteFile(binPath, []byte(wrapper), 0o755)
}
