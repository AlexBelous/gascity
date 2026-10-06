package runtime

import (
	"context"
	"fmt"
)

// SessionAuthorityHandle is a live handle with an independently read owner PID
// and private exact provider ENV. It is evidence, never an ENV-only capability.
type SessionAuthorityHandle struct {
	Name        string
	PID         int
	Environment []string
}

// Format hides the private provider evidence in diagnostic representations.
func (SessionAuthorityHandle) Format(s fmt.State, _ rune) {
	_, _ = s.Write([]byte("[private provider handle evidence]"))
}

// SessionAuthoritySnapshotProvider reports errors instead of best-effort data.
// Unsupported providers cannot authorize a managed Linux tool child.
type SessionAuthoritySnapshotProvider interface {
	ReadSessionAuthoritySnapshot(context.Context) ([]SessionAuthorityHandle, error)
}
