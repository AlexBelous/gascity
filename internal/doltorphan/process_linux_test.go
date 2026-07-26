//go:build linux

package doltorphan

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestScanProcfsPreservesArgvBoundariesAndParent(t *testing.T) {
	root := t.TempDir()
	processDir := filepath.Join(root, "99")
	if err := os.MkdirAll(processDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	wantArgs := []string{
		"/usr/local/bin/dolt",
		"sql-server",
		"--data-dir",
		"/tmp/data dir with spaces",
	}
	rawArgs := []byte(wantArgs[0] + "\x00" + wantArgs[1] + "\x00" + wantArgs[2] + "\x00" + wantArgs[3] + "\x00")
	if err := os.WriteFile(filepath.Join(processDir, "cmdline"), rawArgs, 0o644); err != nil {
		t.Fatalf("WriteFile(cmdline): %v", err)
	}
	if err := os.WriteFile(filepath.Join(processDir, "stat"), []byte("99 (dolt worker) S 12 0 0"), 0o644); err != nil {
		t.Fatalf("WriteFile(stat): %v", err)
	}

	got, err := scanProcfs(root)
	if err != nil {
		t.Fatalf("scanProcfs: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("processes = %v, want exactly one", got)
	}
	if got[0].PID != 99 || got[0].PPID != 12 {
		t.Fatalf("process identity = pid %d ppid %d, want pid 99 ppid 12", got[0].PID, got[0].PPID)
	}
	if !slices.Equal(got[0].Argv, wantArgs) {
		t.Fatalf("Argv = %#v, want %#v", got[0].Argv, wantArgs)
	}
}

func TestScanProcfsIgnoresProcessThatExitsBetweenReads(t *testing.T) {
	root := t.TempDir()
	processDir := filepath.Join(root, "100")
	if err := os.MkdirAll(processDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(filepath.Join(processDir, "cmdline"), []byte("dolt\x00sql-server\x00"), 0o644); err != nil {
		t.Fatalf("WriteFile(cmdline): %v", err)
	}

	got, err := scanProcfs(root)
	if err != nil {
		t.Fatalf("scanProcfs: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("processes = %v, want none after process stat vanished", got)
	}
}

func TestScanProcfsReportsMalformedLiveProcessStat(t *testing.T) {
	root := t.TempDir()
	processDir := filepath.Join(root, "101")
	if err := os.MkdirAll(processDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(filepath.Join(processDir, "cmdline"), []byte("dolt\x00sql-server\x00"), 0o644); err != nil {
		t.Fatalf("WriteFile(cmdline): %v", err)
	}
	if err := os.WriteFile(filepath.Join(processDir, "stat"), []byte("malformed"), 0o644); err != nil {
		t.Fatalf("WriteFile(stat): %v", err)
	}

	if _, err := scanProcfs(root); err == nil {
		t.Fatal("scanProcfs succeeded with malformed live process stat")
	}
}
