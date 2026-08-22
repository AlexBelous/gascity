// Package execretry retries an [exec.Cmd] when execve() intermittently fails
// with ETXTBSY ("text file busy").
//
// The failure is a fork()/exec() race, not a defect in the command being
// run: fork() transiently duplicates the parent's file descriptor table into
// the child before the child's own O_CLOEXEC cleanup runs, so a sibling
// goroutine's open write descriptor for a path can be briefly (and
// irrelevantly) inherited by an unrelated forking child at the exact moment
// something else tries to execve() that same path. internal/convergence
// retries around this same race for long-lived external gate scripts; this
// package lifts the pattern to a reusable, non-test home for callers (such
// as test harnesses under heavy t.Parallel() fan-out) that hit it against
// their own exec targets.
package execretry

import (
	"os/exec"
	"strings"
)

// DefaultAttempts is a reasonable retry budget for a same-host, sub-millisecond
// fork/exec race: enough headroom to ride out contention without masking a
// genuinely broken command behind a long retry loop.
const DefaultAttempts = 5

// Run executes cmd via run, retrying with a freshly [Clone]d command each
// time run's result satisfies [TextFileBusy], up to attempts additional
// tries. It retries immediately with no backoff delay: unlike a long-lived
// external script's contention, this is a purely local, kernel-internal
// race that clears in microseconds, so a delay would only slow down every
// retry without improving the odds of success.
func Run(cmd *exec.Cmd, attempts int, run func(*exec.Cmd) ([]byte, error)) ([]byte, error) {
	out, err := run(cmd)
	for attempt := 0; attempt < attempts && TextFileBusy(err, out); attempt++ {
		cmd = Clone(cmd)
		out, err = run(cmd)
	}
	return out, err
}

// TextFileBusy reports whether err and out indicate a command failed
// because execve() hit ETXTBSY, either as a pre-exec error (the target
// itself could not be exec'd) or as text an interpreter printed to its own
// stderr after an internal exec it ran failed the same way.
func TextFileBusy(err error, out []byte) bool {
	if err == nil {
		return false
	}
	return strings.Contains(strings.ToLower(string(out)), "text file busy") ||
		strings.Contains(strings.ToLower(err.Error()), "text file busy")
}

// Clone builds a fresh, unstarted *exec.Cmd equivalent to cmd. [exec.Cmd] is
// single-use once Start, Run, Output, or CombinedOutput has been called, so
// retrying a command requires constructing a new one rather than reusing
// cmd. Clone copies Path, Args, Dir, and Env; callers relying on other
// exec.Cmd fields (Stdin/Stdout/Stderr, SysProcAttr, Cancel, WaitDelay,
// ExtraFiles) must set them again on the returned command.
func Clone(cmd *exec.Cmd) *exec.Cmd {
	var args []string
	if len(cmd.Args) > 1 {
		args = cmd.Args[1:]
	}
	clone := exec.Command(cmd.Path, args...)
	clone.Dir = cmd.Dir
	clone.Env = cmd.Env
	return clone
}
