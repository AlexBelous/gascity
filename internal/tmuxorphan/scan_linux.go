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
		// Same TOCTOU/permission convention as Cmdline above: an Environ
		// read failure inside namedSocketPathFromArgv skips this PID
		// rather than falling back to a default that could misclassify a
		// live, differently-configured server as orphaned (ga-18nugn
		// review Finding 1).
		socketPath, err := namedSocketPathFromArgv(pid, argv)
		if err != nil {
			continue
		}
		servers = append(servers, ServerProcess{PID: pid, SocketPath: socketPath})
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
// after -L resolved via namedSocketPath against pid's own environment.
// Returns "" when neither flag is present -- the default-socket case that
// must never become a reap candidate (AGENTS.md: "treat personal tmux
// servers as out of bounds"). Returns a non-nil error only when a -L
// socket name was found but pid's environment could not be read; callers
// must skip the PID on that error rather than treat "" as "no named
// socket" (see namedSocketPath).
//
// This codebase's own tmux launcher (internal/runtime/tmux) always passes
// -L/-S as separate argv elements via exec.Command, never the attached
// getopt short-flag form (-Lname); every real server this reaper will ever
// encounter in the fleet was spawned that way, so only the separated form
// is handled.
func namedSocketPathFromArgv(pid int, argv []string) (string, error) {
	for i := 0; i+1 < len(argv); i++ {
		switch argv[i] {
		case "-S":
			return argv[i+1], nil
		case "-L":
			return namedSocketPath(pid, argv[i+1])
		}
	}
	return "", nil
}

// namedSocketPath resolves the on-disk path tmux uses for a named -L
// socket, honoring TMUX_TMPDIR from the TARGET server process's own
// environment -- never the reaper's own -- with TMPDIR deliberately not a
// fallback, matching tmux's own convention. (Mirrors the resolution logic
// in internal/runtime/tmux's unexported namedSocketPath in
// server_socket_probe.go, which resolves the caller's own prospective
// socket and so correctly uses the caller's own environment; that is a
// different problem from identifying an arbitrary already-running target's
// socket, which is what this function does.)
//
// It returns an error when pid's environment cannot be read (TOCTOU race,
// or a permission denial on a foreign-UID process). That error must never
// be papered over with the /tmp default: a permission error masquerading
// as "TMUX_TMPDIR unset" would compute the wrong path for a live server
// with a real custom TMUX_TMPDIR, making it look orphaned and reapable --
// exactly the misclassification this function exists to prevent (ga-18nugn
// review Finding 1). The /tmp fallback below fires only once the
// environment was actually read and TMUX_TMPDIR was confirmed absent or
// empty within it.
func namedSocketPath(pid int, socketName string) (string, error) {
	env, err := pidutil.Environ(pid)
	if err != nil {
		return "", err
	}
	tmpDir, ok := pidutil.EnvValue(env, "TMUX_TMPDIR")
	if !ok || tmpDir == "" {
		tmpDir = "/tmp"
	}
	return filepath.Join(tmpDir, fmt.Sprintf("tmux-%d", os.Getuid()), socketName), nil
}
