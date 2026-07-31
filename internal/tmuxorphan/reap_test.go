package tmuxorphan

import (
	"errors"
	"testing"
)

func TestScanClassifiesAbsentSocketAsOrphaned(t *testing.T) {
	proc := ServerProcess{PID: 111, SocketPath: "/run/tmux-1000/gctest-abc123"}
	result := Scan(ScanConfig{
		ListServers:  func() ([]ServerProcess, error) { return []ServerProcess{proc}, nil },
		SocketExists: func(string) (bool, error) { return false, nil },
	})
	if len(result.Orphaned) != 1 || result.Orphaned[0] != proc {
		t.Fatalf("Orphaned = %v, want [%v]", result.Orphaned, proc)
	}
	if result.Skipped != 0 {
		t.Fatalf("Skipped = %d, want 0", result.Skipped)
	}
}

func TestScanClassifiesPresentSocketAsSkipped(t *testing.T) {
	proc := ServerProcess{PID: 111, SocketPath: "/run/tmux-1000/gctest-abc123"}
	result := Scan(ScanConfig{
		ListServers:  func() ([]ServerProcess, error) { return []ServerProcess{proc}, nil },
		SocketExists: func(string) (bool, error) { return true, nil },
	})
	if len(result.Orphaned) != 0 {
		t.Fatalf("Orphaned = %v, want none (socket still exists on disk)", result.Orphaned)
	}
	if result.Skipped != 1 {
		t.Fatalf("Skipped = %d, want 1", result.Skipped)
	}
}

func TestScanFailsClosedOnSocketExistsError(t *testing.T) {
	proc := ServerProcess{PID: 111, SocketPath: "/run/tmux-1000/gctest-abc123"}
	wantErr := errors.New("stat: permission denied")
	result := Scan(ScanConfig{
		ListServers:  func() ([]ServerProcess, error) { return []ServerProcess{proc}, nil },
		SocketExists: func(string) (bool, error) { return false, wantErr },
	})
	if len(result.Orphaned) != 0 {
		t.Fatalf("Orphaned = %v, want none: an unverifiable socket check must never yield a reap candidate", result.Orphaned)
	}
	if result.Skipped != 1 {
		t.Fatalf("Skipped = %d, want 1", result.Skipped)
	}
	if len(result.Errors) != 1 {
		t.Fatalf("Errors = %v, want 1 entry", result.Errors)
	}
}

func TestScanFailsClosedOnListServersError(t *testing.T) {
	wantErr := errors.New("reading /proc: permission denied")
	result := Scan(ScanConfig{
		ListServers:  func() ([]ServerProcess, error) { return nil, wantErr },
		SocketExists: func(string) (bool, error) { return false, nil },
	})
	if len(result.Orphaned) != 0 {
		t.Fatalf("Orphaned = %v, want none when the process listing itself fails", result.Orphaned)
	}
	if len(result.Errors) != 1 {
		t.Fatalf("Errors = %v, want 1 entry", result.Errors)
	}
}

// TestScanSkipsProcessWithNoNamedSocket encodes the AGENTS.md tmux-safety
// invariant ("treat personal tmux servers as out of bounds") at the
// classification layer: a tmux server process with no discoverable named
// (-L/-S) socket is indistinguishable from a user's personal default-socket
// server, so it must never become a reap candidate -- regardless of what
// SocketExists would otherwise report.
func TestScanSkipsProcessWithNoNamedSocket(t *testing.T) {
	proc := ServerProcess{PID: 111, SocketPath: ""}
	socketExistsCalled := false
	result := Scan(ScanConfig{
		ListServers: func() ([]ServerProcess, error) { return []ServerProcess{proc}, nil },
		SocketExists: func(string) (bool, error) {
			socketExistsCalled = true
			return false, nil
		},
	})
	if len(result.Orphaned) != 0 {
		t.Fatalf("Orphaned = %v, want none for a process with no named socket", result.Orphaned)
	}
	if result.Skipped != 1 {
		t.Fatalf("Skipped = %d, want 1", result.Skipped)
	}
	if socketExistsCalled {
		t.Fatalf("SocketExists was called for a process with no named socket path; it must be skipped before any existence check")
	}
}

func TestReapTerminatesScannedOrphans(t *testing.T) {
	proc := ServerProcess{PID: 111, SocketPath: "/run/tmux-1000/gctest-abc123"}
	var terminatedPIDs []int
	result := Reap(ReapConfig{
		ScanConfig: ScanConfig{
			ListServers:  func() ([]ServerProcess, error) { return []ServerProcess{proc}, nil },
			SocketExists: func(string) (bool, error) { return false, nil },
		},
		Terminate: func(pid int) error {
			terminatedPIDs = append(terminatedPIDs, pid)
			return nil
		},
	})
	if len(result.Terminated) != 1 || result.Terminated[0] != proc {
		t.Fatalf("Terminated = %v, want [%v]", result.Terminated, proc)
	}
	if len(terminatedPIDs) != 1 || terminatedPIDs[0] != proc.PID {
		t.Fatalf("Terminate called with %v, want [%d]", terminatedPIDs, proc.PID)
	}
}

func TestReapRecordsTerminateFailureAsSkipped(t *testing.T) {
	proc := ServerProcess{PID: 111, SocketPath: "/run/tmux-1000/gctest-abc123"}
	wantErr := errors.New("no such process")
	result := Reap(ReapConfig{
		ScanConfig: ScanConfig{
			ListServers:  func() ([]ServerProcess, error) { return []ServerProcess{proc}, nil },
			SocketExists: func(string) (bool, error) { return false, nil },
		},
		Terminate: func(int) error { return wantErr },
	})
	if len(result.Terminated) != 0 {
		t.Fatalf("Terminated = %v, want none when Terminate fails", result.Terminated)
	}
	if result.Skipped != 1 {
		t.Fatalf("Skipped = %d, want 1", result.Skipped)
	}
	if len(result.Errors) != 1 {
		t.Fatalf("Errors = %v, want 1 entry", result.Errors)
	}
}

func TestReapNeverCallsTerminateForNonOrphans(t *testing.T) {
	proc := ServerProcess{PID: 111, SocketPath: "/run/tmux-1000/gctest-abc123"}
	terminateCalled := false
	result := Reap(ReapConfig{
		ScanConfig: ScanConfig{
			ListServers:  func() ([]ServerProcess, error) { return []ServerProcess{proc}, nil },
			SocketExists: func(string) (bool, error) { return true, nil }, // socket present: not orphaned
		},
		Terminate: func(int) error {
			terminateCalled = true
			return nil
		},
	})
	if terminateCalled {
		t.Fatalf("Terminate was called for a process whose socket still exists")
	}
	if len(result.Terminated) != 0 {
		t.Fatalf("Terminated = %v, want none", result.Terminated)
	}
}
