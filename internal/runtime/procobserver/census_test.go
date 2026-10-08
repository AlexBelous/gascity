package procobserver

import (
	"strings"
	"testing"
)

func TestCensusProofCannotForgiveUnprovedLivePID(t *testing.T) {
	for _, mode := range []string{"kernel absent", "known exit", "environ errno alone", "permission", "no start", "wrong incarnation", "late proof", "missing closure", "late birth", "synthetic", "unknown syscall", "protected", "truncated"} {
		t.Run(mode, func(t *testing.T) {
			start := "456"
			c := Census{Seal: CensusSeal{ScanIndex: 4, EnumeratedCount: 11, PIDDigest: strings.Repeat("a", 64), ClassifiedCount: 11, ClassifiedDigest: strings.Repeat("b", 64)}, Closings: []CensusClosing{{ScanIndex: 2, EnumeratedCount: 12, EnumerationDigest: strings.Repeat("a", 64), LiveCount: 11, LiveDigest: strings.Repeat("b", 64)}, {ScanIndex: 3, EnumeratedCount: 11, EnumerationDigest: strings.Repeat("c", 64), LiveCount: 11, LiveDigest: strings.Repeat("b", 64)}}, ReconciledCount: 11, ReconciledDigest: strings.Repeat("b", 64), Proofs: []CensusProof{{Kind: "incarnation_retired", Method: "pidfd_no_pid", PID: 123, StartTicks: &start, ScanIndex: 2, OffsetMS: 1}}}
			e := []EvidenceError{{Reason: "process_unavailable", PID: 123, Operation: "environ", Errno: 3, StartTicks: &start, ScanIndex: 2, ResolvedBy: 1}}
			truncated := false
			switch mode {
			case "kernel absent":
				c.Proofs[0].Kind = "enumerated_pid_absent"
				c.Proofs[0].StartTicks = nil
				e[0].StartTicks = nil
				e[0].Operation = "stat"
				e[0].Errno = 2
			case "known exit":
				c.Proofs[0].Method = "pidfd_exited"
			case "environ errno alone":
				e[0].ResolvedBy = 0
			case "permission":
				e[0].Errno = 13
			case "no start":
				c.Proofs[0].StartTicks = nil
			case "wrong incarnation":
				other := "789"
				c.Proofs[0].StartTicks = &other
			case "late proof":
				c.Proofs[0].OffsetMS = 10001
			case "missing closure":
				c.Closings = c.Closings[:1]
			case "late birth":
				c.Closings[1].LiveDigest = strings.Repeat("d", 64)
			case "synthetic":
				c.Proofs[0].Method = "procfs_esrch"
			case "unknown syscall":
				c.Proofs[0].Method = "pidfd_eperm"
			case "protected":
				c.Proofs[0].ProtectedIdentity = true
			case "truncated":
				truncated = true
			}
			err := ValidateCensus(c, e, len(e), truncated, 10)
			want := mode == "kernel absent" || mode == "known exit"
			if (err == nil) != want {
				t.Fatalf("accepted=%v want=%v error=%v", err == nil, want, err)
			}
		})
	}
}
