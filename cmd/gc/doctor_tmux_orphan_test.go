package main

import (
	"errors"
	"strings"
	"testing"

	"github.com/gastownhall/gascity/internal/doctor"
	"github.com/gastownhall/gascity/internal/tmuxorphan"
)

// tmuxOrphanCheckWith builds a tmuxOrphanCheck with injected server listing,
// socket-existence, and termination functions, for deterministic testing
// without touching real tmux servers or the filesystem.
func tmuxOrphanCheckWith(servers []tmuxorphan.ServerProcess, socketExists func(string) (bool, error), terminate func(int) error) *tmuxOrphanCheck {
	return &tmuxOrphanCheck{
		listServers:  func() ([]tmuxorphan.ServerProcess, error) { return servers, nil },
		socketExists: socketExists,
		terminate:    terminate,
	}
}

func TestTmuxOrphanCheck_NoOrphansReportsOK(t *testing.T) {
	c := tmuxOrphanCheckWith(
		[]tmuxorphan.ServerProcess{{PID: 111, SocketPath: "/run/tmux-1000/gctest-abc123"}},
		func(string) (bool, error) { return true, nil }, // socket present: not orphaned
		func(int) error { return nil },
	)
	r := c.Run(nil)
	if r.Status != doctor.StatusOK {
		t.Fatalf("status = %v, want StatusOK (no orphans)", r.Status)
	}
}

func TestTmuxOrphanCheck_OrphansFoundReportsAdvisoryWarning(t *testing.T) {
	c := tmuxOrphanCheckWith(
		[]tmuxorphan.ServerProcess{
			{PID: 111, SocketPath: "/run/tmux-1000/gctest-abc123"},
			{PID: 222, SocketPath: "/run/tmux-1000/gctest-def456"},
		},
		func(string) (bool, error) { return false, nil }, // both sockets absent: orphaned
		func(int) error { return nil },
	)
	r := c.Run(nil)
	if r.Status != doctor.StatusWarning {
		t.Fatalf("status = %v, want StatusWarning (2 orphans found)", r.Status)
	}
	if r.Severity != doctor.SeverityAdvisory {
		t.Fatalf("severity = %v, want SeverityAdvisory (a leaking tmux server is never a gating failure)", r.Severity)
	}
	if !strings.Contains(r.Message, "2") {
		t.Fatalf("message should report the orphan count, got %q", r.Message)
	}
}

// TestTmuxOrphanCheck_RunNeverTerminates is the load-bearing non-mutation
// proof: `gc doctor` without --fix must never terminate a process, even when
// orphans are found. Run must only classify (via tmuxorphan.Scan), never
// reap.
func TestTmuxOrphanCheck_RunNeverTerminates(t *testing.T) {
	terminateCalled := false
	c := tmuxOrphanCheckWith(
		[]tmuxorphan.ServerProcess{{PID: 111, SocketPath: "/run/tmux-1000/gctest-abc123"}},
		func(string) (bool, error) { return false, nil }, // orphaned
		func(int) error {
			terminateCalled = true
			return nil
		},
	)
	c.Run(nil)
	if terminateCalled {
		t.Fatalf("Run() invoked terminate; Run must only classify, never mutate")
	}
}

func TestTmuxOrphanCheck_FixTerminatesOrphans(t *testing.T) {
	var terminatedPIDs []int
	c := tmuxOrphanCheckWith(
		[]tmuxorphan.ServerProcess{{PID: 111, SocketPath: "/run/tmux-1000/gctest-abc123"}},
		func(string) (bool, error) { return false, nil }, // orphaned
		func(pid int) error {
			terminatedPIDs = append(terminatedPIDs, pid)
			return nil
		},
	)
	if err := c.Fix(nil); err != nil {
		t.Fatalf("Fix returned error: %v", err)
	}
	if len(terminatedPIDs) != 1 || terminatedPIDs[0] != 111 {
		t.Fatalf("terminated PIDs = %v, want [111]", terminatedPIDs)
	}
}

func TestTmuxOrphanCheck_LiveSocketsAreNeverFixed(t *testing.T) {
	terminateCalled := false
	c := tmuxOrphanCheckWith(
		[]tmuxorphan.ServerProcess{{PID: 111, SocketPath: "/run/tmux-1000/gctest-abc123"}},
		func(string) (bool, error) { return true, nil }, // socket present: not orphaned
		func(int) error {
			terminateCalled = true
			return nil
		},
	)
	if err := c.Fix(nil); err != nil {
		t.Fatalf("Fix returned error: %v", err)
	}
	if terminateCalled {
		t.Fatalf("Fix terminated a process whose socket still exists")
	}
}

func TestTmuxOrphanCheck_NameCanFixWarmup(t *testing.T) {
	c := tmuxOrphanCheckWith(nil, func(string) (bool, error) { return false, nil }, func(int) error { return nil })
	if c.Name() != "tmux-orphan" {
		t.Fatalf("Name() = %q, want %q", c.Name(), "tmux-orphan")
	}
	if !c.CanFix() {
		t.Fatalf("CanFix() = false, want true")
	}
	if c.WarmupEligible() {
		t.Fatalf("WarmupEligible() = true, want false (opt-in via `gc doctor`, not the `gc start` warm-up scan)")
	}
}

// TestTmuxOrphanCheck_ListServersErrorSkipsCleanly mirrors forkRateCheck's
// established convention (see TestForkRateCheck_ReadErrorSkips): when the
// underlying scan itself cannot run, an advisory check reports a clean skip
// rather than an alarming error, since the check genuinely does not know
// whether orphans exist.
func TestTmuxOrphanCheck_ListServersErrorSkipsCleanly(t *testing.T) {
	terminateCalled := false
	c := &tmuxOrphanCheck{
		listServers:  func() ([]tmuxorphan.ServerProcess, error) { return nil, errors.New("reading /proc: permission denied") },
		socketExists: func(string) (bool, error) { return false, nil },
		terminate:    func(int) error { terminateCalled = true; return nil },
	}
	r := c.Run(nil)
	if r.Status != doctor.StatusOK {
		t.Fatalf("status = %v, want StatusOK (scan unavailable -> skip, not a false warning)", r.Status)
	}
	if terminateCalled {
		t.Fatalf("Run() invoked terminate on a failed scan")
	}
}
