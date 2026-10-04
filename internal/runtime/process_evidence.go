package runtime

import "fmt"

// ProcessRootTracker attributes supplied, independently observed process roots
// to live provider-owned PIDs. It performs no process-table scan or mutation.
// Providers lacking positive PID attribution cannot use external evidence.
type ProcessRootTracker interface {
	TrackProcessRoots(roots []LiveRuntime) ([]LiveRuntime, error)
}

// ProcessHandle is a live provider handle with an independently read owner PID.
type ProcessHandle struct {
	Name, SessionID string
	PID             int
}

// BindProcessEvidence preserves every root, including orphans. Matching SID
// alone is insufficient: both provider PID and SID must match, uniquely.
func BindProcessEvidence(roots []LiveRuntime, handles []ProcessHandle) ([]LiveRuntime, error) {
	byPID := map[int]ProcessHandle{}
	bySID := map[string]bool{}
	byName := map[string]bool{}
	for _, h := range handles {
		if h.Name == "" || h.SessionID == "" || h.PID <= 1 || bySID[h.SessionID] || byName[h.Name] {
			return nil, fmt.Errorf("provider process handle unavailable or ambiguous")
		}
		if _, exists := byPID[h.PID]; exists {
			return nil, fmt.Errorf("multiple provider handles for one PID")
		}
		byPID[h.PID] = h
		bySID[h.SessionID] = true
		byName[h.Name] = true
	}
	out := append([]LiveRuntime{}, roots...)
	for i := range out {
		out[i].IsTracked = false
		out[i].ProviderName = ""
		h, ok := byPID[out[i].PID]
		if ok && out[i].SessionID == h.SessionID {
			out[i].IsTracked = true
			out[i].ProviderName = h.Name
		}
	}
	return out, nil
}
