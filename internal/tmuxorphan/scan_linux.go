//go:build linux

package tmuxorphan

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/gastownhall/gascity/internal/pidutil"
)

// tmuxServerComm is the exact /proc/<pid>/comm value a tmux server process
// reports once it has forked off from the client. Confirmed empirically on
// this codebase's target platform and matched by the pre-existing darwin
// fixture in internal/runtime/proctable/scan_darwin_command_test.go;
// internal/runtime/proctable's isInfrastructureParent uses a looser
// substring match against the same string for an unrelated purpose (finding
// infrastructure ancestors to exclude), whereas ListServers needs an exact,
// positive identification of server processes to include.
const tmuxServerComm = "tmux: server"

// ListServers walks /proc for live tmux server processes, identifying each
// by its exact comm value and resolving its implied named-socket path (if
// any) from its own launch argv.
func ListServers() ([]ServerProcess, error) {
	return listServersUnder("/proc")
}

func listServersUnder(root string) ([]ServerProcess, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, fmt.Errorf("tmuxorphan: reading %s: %w", root, err)
	}

	var servers []ServerProcess
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		pid, err := strconv.Atoi(entry.Name())
		if err != nil || pid <= 1 {
			continue
		}
		if !isTmuxServerComm(root, pid) {
			continue
		}
		// A process that exits between this readdir snapshot and the
		// cmdline read below is an ordinary TOCTOU race, not a scan
		// failure -- skip just this PID rather than failing the walk.
		argv, err := pidutil.Cmdline(pid)
		if err != nil {
			continue
		}
		servers = append(servers, ServerProcess{PID: pid, SocketPath: namedSocketPathFromArgv(argv)})
	}
	return servers, nil
}

func isTmuxServerComm(root string, pid int) bool {
	data, err := os.ReadFile(filepath.Join(root, strconv.Itoa(pid), "comm"))
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(data)) == tmuxServerComm
}

// namedSocketPathFromArgv extracts the socket path implied by a tmux
// server's own launch argv: a literal path after -S, or a socket name
// after -L resolved via namedSocketPath. Returns "" when neither flag is
// present -- the default-socket case that must never become a reap
// candidate (AGENTS.md: "treat personal tmux servers as out of bounds").
//
// This codebase's own tmux launcher (internal/runtime/tmux) always passes
// -L/-S as separate argv elements via exec.Command, never the attached
// getopt short-flag form (-Lname); every real server this reaper will ever
// encounter in the fleet was spawned that way, so only the separated form
// is handled.
func namedSocketPathFromArgv(argv []string) string {
	for i := 0; i+1 < len(argv); i++ {
		switch argv[i] {
		case "-S":
			return argv[i+1]
		case "-L":
			return namedSocketPath(argv[i+1])
		}
	}
	return ""
}

// namedSocketPath resolves the on-disk path tmux uses for a named -L
// socket. Mirrors internal/runtime/tmux's unexported namedSocketPath
// (server_socket_probe.go): tmux honors TMUX_TMPDIR here, with TMPDIR
// deliberately not a fallback, matching tmux's own convention.
func namedSocketPath(socketName string) string {
	tmpDir := os.Getenv("TMUX_TMPDIR")
	if tmpDir == "" {
		tmpDir = "/tmp"
	}
	return filepath.Join(tmpDir, fmt.Sprintf("tmux-%d", os.Getuid()), socketName)
}
