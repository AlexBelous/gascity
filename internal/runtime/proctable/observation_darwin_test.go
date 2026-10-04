//go:build darwin

package proctable

import "testing"

func TestDarwinFlattenedEnvironmentCannotProveCompleteCoverage(t *testing.T) {
	restore := SetScanRootForTesting(t.TempDir())
	defer restore()
	roots, err := ObserveRoots()
	if err == nil || len(roots) != 0 {
		t.Fatal("Darwin best-effort ps falsely established complete coverage")
	}
}
