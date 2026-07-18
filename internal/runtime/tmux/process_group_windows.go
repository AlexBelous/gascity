//go:build windows

package tmux

import (
	"fmt"
	"os/exec"
	"strings"
)

// procIdentity mirrors the unix type so cross-platform tmux.go compiles.
type procIdentity struct {
	ppid  string
	pgid  string
	start string
}

// snapshotProcessTable is a no-op on Windows: the POSIX PID-reuse kill race the
// unix implementation guards against does not apply here (Gas City does not run
// the signal-based teardown path on Windows). Returning nil makes callers signal
// nothing and fall back to tmux kill-session.
func snapshotProcessTable() map[string]procIdentity {
	return nil
}

// processStartTime is unused on Windows (see snapshotProcessTable).
func processStartTime(_ string) string {
	return ""
}

func processExists(pid int) (bool, error) {
	filter := fmt.Sprintf("PID eq %d", pid)
	out, err := exec.Command("tasklist", "/FI", filter, "/FO", "CSV", "/NH").Output()
	if err != nil {
		return false, err
	}

	text := strings.TrimSpace(string(out))
	if text == "" {
		return false, nil
	}
	if strings.HasPrefix(text, "INFO:") {
		return false, nil
	}

	return true, nil
}
