//go:build !windows

package tmux

import (
	"errors"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
)

// processIsAlive reports whether pid still names a live process. Permission
// errors count as alive: cleanup must retain its SIGKILL fallback rather than
// mistake an unobservable process for an exited one.
func processIsAlive(pid string) bool {
	n, err := strconv.Atoi(strings.TrimSpace(pid))
	if err != nil || n <= 0 {
		return false
	}
	process, err := os.FindProcess(n)
	if err != nil {
		return false
	}
	err = process.Signal(syscall.Signal(0))
	return err == nil || errors.Is(err, syscall.EPERM)
}

// getParentPID returns the parent process ID (PPID) for a given PID.
// Returns empty string if the process doesn't exist or PPID can't be determined.
func getParentPID(pid string) string {
	out, err := exec.Command("ps", "-o", "ppid=", "-p", pid).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// getProcessGroupID returns the process group ID (PGID) for a given PID.
// Returns empty string if the process doesn't exist or PGID can't be determined.
func getProcessGroupID(pid string) string {
	out, err := exec.Command("ps", "-o", "pgid=", "-p", pid).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// getProcessGroupMembers returns all PIDs in a process group.
// This finds processes that share the same PGID, including those that reparented to init.
func getProcessGroupMembers(pgid string) []string {
	// Use ps to find all processes with this PGID
	// On macOS: ps -axo pid,pgid
	// On Linux: ps -eo pid,pgid
	out, err := exec.Command("ps", "-axo", "pid=,pgid=").Output()
	if err != nil {
		return nil
	}

	var members []string
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 2 && strings.TrimSpace(fields[1]) == pgid {
			members = append(members, strings.TrimSpace(fields[0]))
		}
	}
	return members
}

// procIdentity is one process's parent, group, and start time captured in a
// single atomic ps snapshot. start is the identity token that survives PID
// reuse: when the kernel recycles a PID onto a new process, that process has a
// different start time, so a stale kill can be detected and skipped.
type procIdentity struct {
	ppid  string
	pgid  string
	start string
}

// snapshotProcessTable captures ppid, pgid, and start time for EVERY process in
// one ps call, keyed by PID. Descendant discovery then walks this in-memory
// snapshot instead of a slow live `pgrep -P` recursion (one exec per node,
// seconds under load). The live walk was the arming half of the session-massacre
// TOCTOU: during a stop/drain wave the agent trees collapse, the kernel recycles
// their PIDs onto unrelated processes inside the walk→kill window, and the kill
// loop then landed on whatever now owned each reused PID. A single atomic
// snapshot removes the multi-second discovery window; killVerified closes the
// residual gap. Returns nil on ps failure, which callers treat as "signal
// nothing" (safe: tmux kill-session still tears the pane down).
func snapshotProcessTable() map[string]procIdentity {
	// pid/ppid/pgid are single numeric tokens; lstart is the trailing
	// (space-containing) field, so parse the first three and join the rest.
	out, err := exec.Command("ps", "-axo", "pid=,ppid=,pgid=,lstart=").Output()
	if err != nil {
		return nil
	}
	table := make(map[string]procIdentity)
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 4 {
			continue
		}
		table[fields[0]] = procIdentity{
			ppid:  fields[1],
			pgid:  fields[2],
			start: strings.Join(fields[3:], " "),
		}
	}
	return table
}

// processStartTime returns pid's current start time, normalized the same way as
// snapshotProcessTable (collapsed whitespace) so the two are directly
// comparable. Returns "" if the process is gone. Used to re-verify identity
// immediately before signaling.
func processStartTime(pid string) string {
	out, err := exec.Command("ps", "-o", "lstart=", "-p", pid).Output()
	if err != nil {
		return ""
	}
	return strings.Join(strings.Fields(string(out)), " ")
}
