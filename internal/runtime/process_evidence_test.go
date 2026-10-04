package runtime

import "testing"

func TestBindProcessEvidenceRequiresExactProviderPIDAndSID(t *testing.T) {
	roots := []LiveRuntime{{PID: 42, PPID: 1, SessionID: "sid", City: "/city", Epoch: 2}}
	for _, tc := range []struct {
		name    string
		handles []ProcessHandle
		tracked bool
		err     bool
	}{
		{"exact", []ProcessHandle{{Name: "live", SessionID: "sid", PID: 42}}, true, false},
		{"wrong pid", []ProcessHandle{{Name: "live", SessionID: "sid", PID: 43}}, false, false},
		{"wrong sid", []ProcessHandle{{Name: "live", SessionID: "other", PID: 42}}, false, false},
		{"orphan", nil, false, false},
		{"alias", []ProcessHandle{{Name: "live", SessionID: "sid", PID: 42}, {Name: "alias", SessionID: "sid", PID: 42}}, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := BindProcessEvidence(roots, tc.handles)
			if (err != nil) != tc.err {
				t.Fatalf("error %v", err)
			}
			if !tc.err && (len(got) != 1 || got[0].IsTracked != tc.tracked || roots[0].IsTracked) {
				t.Fatalf("bad attribution %+v", got)
			}
		})
	}
}
