//go:build !linux

package tmuxorphan

import "fmt"

// ListServers is unavailable on platforms without /proc. It returns an
// error rather than an empty result: Scan's fail-closed design (see
// ScanConfig.ListServers) treats a ListServers error as "orphan status
// unknown," which is the honest answer here -- unlike a genuine empty scan,
// this platform cannot confirm zero tmux servers are running.
func ListServers() ([]ServerProcess, error) {
	return nil, fmt.Errorf("tmuxorphan: ListServers is unsupported on this platform (no /proc)")
}
