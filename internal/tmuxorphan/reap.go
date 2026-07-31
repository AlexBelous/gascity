// Package tmuxorphan classifies and reaps orphaned tmux server processes --
// tmux servers whose named (-L/-S) socket is provably absent from disk, so
// nothing can ever reach them again (see ga-026hrg). Classification fails
// closed: a process is only ever a reap candidate when its implied socket
// path is known and confirmed absent. Any process with no discoverable
// named socket -- including every default-socket personal tmux server --
// is skipped, never reported as orphaned, per the AGENTS.md tmux-safety
// invariant ("treat personal tmux servers as out of bounds").
package tmuxorphan

// ServerProcess identifies a single tmux server process discovered by
// ListServers. SocketPath is the filesystem path implied by the process's
// own -L/-S launch argument, resolved to tmux's actual on-disk socket
// convention; it is empty when the process has no discoverable named
// socket (e.g. a default-socket server).
type ServerProcess struct {
	PID        int
	SocketPath string
}

// ScanConfig supplies the injectable dependencies Scan needs to classify
// tmux server processes as orphaned or not.
type ScanConfig struct {
	// ListServers returns every tmux server process currently running.
	ListServers func() ([]ServerProcess, error)
	// SocketExists reports whether the given socket path is still present
	// on disk.
	SocketExists func(string) (bool, error)
}

// ScanResult holds the outcome of a Scan.
type ScanResult struct {
	// Orphaned holds every server process whose implied socket path was
	// confirmed absent from disk.
	Orphaned []ServerProcess
	// Skipped counts server processes that were not classified as
	// orphaned: no named socket, a socket that still exists, or a socket
	// whose existence could not be verified.
	Skipped int
	// Errors collects listing/verification failures encountered while
	// scanning. A non-empty Errors does not imply Orphaned is incomplete
	// only for the specific processes each error concerns -- classification
	// fails closed per process, not for the whole scan.
	Errors []error
}

// Scan classifies each tmux server process returned by ListServers as
// orphaned (its implied socket path is provably absent from disk) or not.
// It fails closed: a process is only ever added to Orphaned when its socket
// path is known and SocketExists affirmatively reports it absent. A process
// with no named socket, or one whose SocketExists call errors, is recorded
// as Skipped instead -- an unverifiable process is never a reap candidate.
func Scan(cfg ScanConfig) ScanResult {
	var result ScanResult

	servers, err := cfg.ListServers()
	if err != nil {
		result.Errors = append(result.Errors, err)
		return result
	}

	for _, proc := range servers {
		if proc.SocketPath == "" {
			result.Skipped++
			continue
		}
		exists, err := cfg.SocketExists(proc.SocketPath)
		if err != nil {
			result.Skipped++
			result.Errors = append(result.Errors, err)
			continue
		}
		if exists {
			result.Skipped++
			continue
		}
		result.Orphaned = append(result.Orphaned, proc)
	}

	return result
}

// ReapConfig extends ScanConfig with the ability to terminate a confirmed
// orphan.
type ReapConfig struct {
	ScanConfig
	// Terminate kills the tmux server process with the given PID.
	Terminate func(int) error
}

// ReapResult holds the outcome of a Reap.
type ReapResult struct {
	// Terminated holds every orphan whose Terminate call succeeded.
	Terminated []ServerProcess
	// Skipped counts processes not terminated: everything Scan skipped,
	// plus any orphan whose Terminate call itself failed.
	Skipped int
	// Errors collects both scan-time and terminate-time failures.
	Errors []error
}

// Reap scans for orphaned tmux server processes and terminates each one.
// A process whose Terminate call fails is recorded as Skipped, not
// Terminated -- Reap never reports success it did not observe.
func Reap(cfg ReapConfig) ReapResult {
	scan := Scan(cfg.ScanConfig)

	result := ReapResult{
		Skipped: scan.Skipped,
		Errors:  scan.Errors,
	}

	for _, proc := range scan.Orphaned {
		if err := cfg.Terminate(proc.PID); err != nil {
			result.Skipped++
			result.Errors = append(result.Errors, err)
			continue
		}
		result.Terminated = append(result.Terminated, proc)
	}

	return result
}
