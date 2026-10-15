package procobserver

import (
	"encoding/json"
	"testing"
)

func bothPartialFixture(t *testing.T) (ReleasePolicyV3, []ResponseV3) {
	t.Helper()
	p, original, _ := reacquireFixture()
	b, _ := json.Marshal([]ResponseV3{original[0], original[0]})
	var frames []ResponseV3
	if err := json.Unmarshal(b, &frames); err != nil {
		t.Fatal(err)
	}
	frames[1].RequestNonce = original[1].RequestNonce
	frames[1].StartedAt, frames[1].FinishedAt = original[1].StartedAt, original[1].FinishedAt
	return p, frames
}

func TestReacquireBothPartialStableManagedCoverage(t *testing.T) {
	p, f := bothPartialFixture(t)
	if !EligibleCapturePairReacquisitionV3(f, p, f[1].FinishedAt) {
		t.Fatal("stable both-partial process shape refused")
	}
	roots, ok := CaptureReacquisitionIdentitiesV3(f[0], p, f[1].FinishedAt)
	if !ok || len(roots) != 1 || !roots[0].DeclaredRoot {
		t.Fatal("missing typed provisional identities")
	}
	for _, name := range []string{"root-missing", "uid-change", "late-managed", "cross-frame-managed-change", "empty-roots", "caller-binding", "permission"} {
		t.Run(name, func(t *testing.T) {
			p, f := bothPartialFixture(t)
			switch name {
			case "root-missing":
				f[1].Census.Scans[0].Verified = nil
			case "uid-change":
				f[1].Census.Scans[0].Verified[0].UIDs[3]++
			case "late-managed":
				v := f[1].Census.Scans[0].Verified[0]
				v.PID = 99
				v.DeclaredRoot = false
				f[1].Census.Scans[1].Verified = append(f[1].Census.Scans[1].Verified, v)
				f[1].Census.Scans[1].LiveCount = 2
			case "cross-frame-managed-change":
				for i := range f[1].Census.Scans {
					f[1].Census.Scans[i].Verified[0].Name = "replaced"
				}
			case "empty-roots":
				for i := range f {
					for j := range f[i].Census.Scans {
						f[i].Census.Scans[j].Verified[0].DeclaredRoot = false
					}
				}
			case "caller-binding":
				f[1].Errors[0].Reason = "caller_binding_changed"
			case "permission":
				f[1].Errors[0].Errno = 13
			}
			if EligibleCapturePairReacquisitionV3(f, p, f[1].FinishedAt) {
				t.Fatal("unsafe partial pair eligible")
			}
		})
	}
}
