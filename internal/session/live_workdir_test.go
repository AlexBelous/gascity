package session

import (
	"os"
	"os/exec"
	"testing"

	"github.com/gastownhall/gascity/internal/pathutil"
	"github.com/gastownhall/gascity/internal/runtime"
)

// chdir switches the test process's cwd to dir and restores the original
// cwd on cleanup. Must not be used by parallel subtests: cwd is global
// process state.
func chdir(t *testing.T, dir string) {
	t.Helper()
	orig, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("Chdir(%q): %v", dir, err)
	}
	t.Cleanup(func() { _ = os.Chdir(orig) })
}

func TestResolveLiveWorkDir_LivePIDResolvesToRealCwd(t *testing.T) {
	dir := t.TempDir()
	chdir(t, dir)

	sp := runtime.NewFake()
	sp.OrphanedRuntimes["sess-1"] = runtime.LiveRuntime{SessionID: "sess-1", PID: os.Getpid()}

	info := Info{ID: "sess-1", WorkDir: "/stale/metadata/path"}
	want := pathutil.NormalizePathForCompare(dir)
	if got := ResolveLiveWorkDir(sp, info); got != want {
		t.Fatalf("ResolveLiveWorkDir() = %q, want live cwd %q", got, want)
	}
}

func TestResolveLiveWorkDir_DistinctPooledSessionsShowDistinctPaths(t *testing.T) {
	dirA := t.TempDir()
	dirB := t.TempDir()
	chdir(t, dirA)

	cmd := exec.Command("sleep", "30")
	cmd.Dir = dirB
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting helper process: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})

	sp := runtime.NewFake()
	sp.OrphanedRuntimes["sess-a"] = runtime.LiveRuntime{SessionID: "sess-a", PID: os.Getpid()}
	sp.OrphanedRuntimes["sess-b"] = runtime.LiveRuntime{SessionID: "sess-b", PID: cmd.Process.Pid}

	gotA := ResolveLiveWorkDir(sp, Info{ID: "sess-a", WorkDir: "/stale/shared/base"})
	gotB := ResolveLiveWorkDir(sp, Info{ID: "sess-b", WorkDir: "/stale/shared/base"})

	wantA := pathutil.NormalizePathForCompare(dirA)
	wantB := pathutil.NormalizePathForCompare(dirB)

	if gotA != wantA {
		t.Errorf("gotA = %q, want %q", gotA, wantA)
	}
	if gotB != wantB {
		t.Errorf("gotB = %q, want %q", gotB, wantB)
	}
	if gotA == gotB {
		t.Fatalf("expected distinct paths for distinct sessions, both resolved to %q", gotA)
	}
}

func TestResolveLiveWorkDir_ZeroPIDFallsBackToStoredWorkDir(t *testing.T) {
	sp := runtime.NewFake()
	sp.OrphanedRuntimes["sess-1"] = runtime.LiveRuntime{SessionID: "sess-1", PID: 0}

	info := Info{ID: "sess-1", WorkDir: "  /stored/fallback  "}
	if got := ResolveLiveWorkDir(sp, info); got != "/stored/fallback" {
		t.Fatalf("ResolveLiveWorkDir() = %q, want trimmed stored fallback %q", got, "/stored/fallback")
	}
}

func TestResolveLiveWorkDir_DeadPIDFallsBackToStoredWorkDir(t *testing.T) {
	cmd := exec.Command("true")
	if err := cmd.Run(); err != nil {
		t.Fatalf("running short-lived helper process: %v", err)
	}
	deadPID := cmd.Process.Pid

	sp := runtime.NewFake()
	sp.OrphanedRuntimes["sess-1"] = runtime.LiveRuntime{SessionID: "sess-1", PID: deadPID}

	info := Info{ID: "sess-1", WorkDir: "/stored/fallback"}
	if got := ResolveLiveWorkDir(sp, info); got != "/stored/fallback" {
		t.Fatalf("ResolveLiveWorkDir() = %q, want stored fallback %q", got, "/stored/fallback")
	}
}

func TestResolveLiveWorkDir_DeletedCwdFallsBackToStoredWorkDir(t *testing.T) {
	dir := t.TempDir()
	chdir(t, dir)
	if err := os.RemoveAll(dir); err != nil {
		t.Fatalf("removing cwd out from under the process: %v", err)
	}

	sp := runtime.NewFake()
	sp.OrphanedRuntimes["sess-1"] = runtime.LiveRuntime{SessionID: "sess-1", PID: os.Getpid()}

	info := Info{ID: "sess-1", WorkDir: "/stored/fallback"}
	if got := ResolveLiveWorkDir(sp, info); got != "/stored/fallback" {
		t.Fatalf("ResolveLiveWorkDir() = %q, want stored fallback %q", got, "/stored/fallback")
	}
}

func TestResolveLiveWorkDir_ScanErrorFallsBackToStoredWorkDir(t *testing.T) {
	sp := runtime.NewFailFake()
	sp.OrphanedRuntimes["sess-1"] = runtime.LiveRuntime{SessionID: "sess-1", PID: os.Getpid()}

	info := Info{ID: "sess-1", WorkDir: "/stored/fallback"}
	if got := ResolveLiveWorkDir(sp, info); got != "/stored/fallback" {
		t.Fatalf("ResolveLiveWorkDir() = %q, want stored fallback %q", got, "/stored/fallback")
	}
}

func TestResolveLiveWorkDir_NonScannerProviderFallsBackToStoredWorkDir(t *testing.T) {
	info := Info{ID: "sess-1", WorkDir: "/stored/fallback"}
	if got := ResolveLiveWorkDir(nil, info); got != "/stored/fallback" {
		t.Fatalf("ResolveLiveWorkDir() = %q, want stored fallback %q", got, "/stored/fallback")
	}
}

func TestResolveLiveWorkDir_EmptySessionIDFallsBackToStoredWorkDir(t *testing.T) {
	sp := runtime.NewFake()
	sp.OrphanedRuntimes["sess-1"] = runtime.LiveRuntime{SessionID: "sess-1", PID: os.Getpid()}

	info := Info{ID: "", WorkDir: "/stored/fallback"}
	if got := ResolveLiveWorkDir(sp, info); got != "/stored/fallback" {
		t.Fatalf("ResolveLiveWorkDir() = %q, want stored fallback %q", got, "/stored/fallback")
	}
}

func TestResolveLiveWorkDir_NoMatchingRuntimeFallsBackToStoredWorkDir(t *testing.T) {
	sp := runtime.NewFake()
	sp.OrphanedRuntimes["sess-other"] = runtime.LiveRuntime{SessionID: "sess-other", PID: os.Getpid()}

	info := Info{ID: "sess-1", WorkDir: "/stored/fallback"}
	if got := ResolveLiveWorkDir(sp, info); got != "/stored/fallback" {
		t.Fatalf("ResolveLiveWorkDir() = %q, want stored fallback %q", got, "/stored/fallback")
	}
}
