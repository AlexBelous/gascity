//go:build !linux && !darwin

package doltorphan

import (
	"fmt"
	"runtime"
)

func snapshotProcesses() ([]Process, error) {
	return nil, fmt.Errorf("process snapshot is unavailable on %s", runtime.GOOS)
}
