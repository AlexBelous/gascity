package ssh

import "testing"

// A partial process snapshot must never prove that the selected agent died.
func TestProcessSnapshotIncompleteHolds(t *testing.T) {
	for _, tc := range []struct{ name, snapshot string }{
		{"comm missing", "agent 100\n--gc-comm--\n--gc-args--\n100 1 2.1.30\n"},
		{"args missing", "agent 100\n--gc-comm--\n100 1 wrapper\n--gc-args--\n"},
		{"selected root missing", "agent 100\n--gc-comm--\n200 1 cat\n--gc-comm-ok--\n--gc-args--\n200 1 cat\n--gc-args-ok--\n"},
		{"args row missing", "agent 100\n--gc-comm--\n100 1 sh\n101 100 wrapper\n--gc-comm-ok--\n--gc-args--\n100 1 sh\n--gc-args-ok--\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			alive, degraded := paneTreeRunsNames(tc.snapshot, map[string]struct{}{"claude": {}})
			if !alive || !degraded {
				t.Fatalf("alive=%v degraded=%v, want conservative alive/degraded", alive, degraded)
			}
		})
	}
}
