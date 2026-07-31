package tmuxorphan

import "github.com/gastownhall/gascity/internal/runtime/proctable"

// Terminate kills the tmux server process at pid: SIGTERM first, escalating
// to SIGKILL after a grace period, returning only once the process is
// confirmed dead. It delegates to proctable.KillByPID rather than
// re-implementing graceful termination -- the PID-reuse guard (captured
// start time, so a recycled PID during the reap wait is never mistaken for
// the original target still living) and the SIGTERM-then-SIGKILL escalation
// are already hardened there.
//
// KillByPID signals the target's process group before falling back to the
// single PID. This is safe for a tmux server specifically because tmux
// daemonizes: the server is its own session and process-group leader, and
// every pane it spawns is placed in its own distinct process group
// (confirmed empirically: a probe server/pane pair showed pgrp == the
// server's own PID for the server, and pgrp == the pane's own PID, in a
// different session, for the pane child) -- so a group signal aimed at the
// server's PID cannot reach a pane's running program.
func Terminate(pid int) error {
	return proctable.KillByPID(pid)
}
