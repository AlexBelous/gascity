package procobserver

import "fmt"

// KernelProofProfile pins the reviewed syscall and inherited-filter provenance.
// A release manifest must attest this profile; detecting a kernel version alone
// cannot authenticate ESRCH against ptrace or a seccomp ERRNO action.
const KernelProofProfile = "linux6.8-pidfd-flags0-no-esrch-filters/v1"

// CensusClosing preserves the raw enumeration and the independently classified
// live set of each closing scan. Retirements never rewrite these raw fields.
type CensusClosing struct {
	ScanIndex         int    `json:"scan_index"`
	EnumeratedCount   int    `json:"enumerated_count"`
	EnumerationDigest string `json:"enumeration_digest"`
	LiveCount         int    `json:"live_count"`
	LiveDigest        string `json:"live_digest"`
}

// CensusProof is a bounded positive kernel witness. A null start is intentional
// only for a PID that vanished before its first readable stat, not a fabricated
// known incarnation. Namespace, boot and profile are bound by the outer frame.
type CensusProof struct {
	Kind              string  `json:"kind"`
	Method            string  `json:"method"`
	PID               int     `json:"pid"`
	StartTicks        *string `json:"start_ticks"`
	ReplacementStart  string  `json:"replacement_start"`
	ScanIndex         int     `json:"scan_index"`
	OffsetMS          int64   `json:"offset_ms"`
	ProtectedIdentity bool    `json:"protected_identity"`
}

// Census is the versioned reconciliation receipt. ReconciledDigest is computed
// from the first closing's surviving rows, excluding only positively proven
// retirements. It must match the freshly classified second closing. A late live
// birth or changed surviving identity cannot satisfy that equality.
type Census struct {
	Closings         []CensusClosing `json:"closings"`
	ReconciledCount  int             `json:"reconciled_count"`
	ReconciledDigest string          `json:"reconciled_digest"`
	Proofs           []CensusProof   `json:"proofs"`
	Seal             CensusSeal      `json:"seal"`
}

// CensusSeal is the final numeric enumeration and stat revalidation. It binds
// the previously classified survivor rows; an uninspected birth is incomplete.
type CensusSeal struct {
	ScanIndex        int    `json:"scan_index"`
	EnumeratedCount  int    `json:"enumerated_count"`
	PIDDigest        string `json:"pid_digest"`
	ClassifiedCount  int    `json:"classified_count"`
	ClassifiedDigest string `json:"classified_digest"`
}

func sameStart(a, b *string) bool {
	return a == nil && b == nil || a != nil && b != nil && *a == *b
}

// ValidateCensus verifies a complete receipt without promoting raw procfs errors
// into kernel evidence. Both helper codec and controller relay use this rule.
func ValidateCensus(c Census, errors []EvidenceError, total int, truncated bool, duration int64) error {
	bad := func() error { return fmt.Errorf("observer reconciled census incomplete") }
	if len(c.Closings) != 2 || c.Proofs == nil || len(c.Proofs) > 128 || errors == nil || total != len(errors) || truncated || duration < 0 || duration > Timeout.Milliseconds() {
		return bad()
	}
	for i, v := range c.Closings {
		if v.ScanIndex != i+2 || v.EnumeratedCount < 1 || v.EnumeratedCount > MaxProcesses || v.LiveCount < 1 || v.LiveCount > v.EnumeratedCount || !isHex(v.EnumerationDigest, 64) || !isHex(v.LiveDigest, 64) {
			return bad()
		}
	}
	final := c.Seal
	if final.ScanIndex != 4 || final.EnumeratedCount < 1 || final.EnumeratedCount > MaxProcesses || final.ClassifiedCount < 1 || final.ClassifiedCount > final.EnumeratedCount || !isHex(final.PIDDigest, 64) || !isHex(final.ClassifiedDigest, 64) {
		return bad()
	}
	if c.ReconciledCount != final.ClassifiedCount || c.ReconciledCount > c.Closings[1].LiveCount || c.ReconciledCount > c.Closings[0].LiveCount || !isHex(c.ReconciledDigest, 64) || c.ReconciledDigest != final.ClassifiedDigest {
		return bad()
	}
	seen := map[string]bool{}
	retirement3, retirement4 := 0, 0
	for _, p := range c.Proofs {
		if p.PID <= 1 || p.PID > 2147483647 || p.ScanIndex < 1 || p.ScanIndex > 4 || p.OffsetMS < 0 || p.OffsetMS > duration || p.ProtectedIdentity {
			return bad()
		}
		switch p.Kind {
		case "enumerated_pid_absent":
			if p.StartTicks != nil || p.Method != "pidfd_no_pid" || p.ReplacementStart != "" {
				return bad()
			}
		case "incarnation_retired":
			if p.ScanIndex >= 3 {
				retirement3++
			}
			if p.ScanIndex == 4 {
				retirement4++
			}
			if p.StartTicks == nil || !positiveNumber(*p.StartTicks) {
				return bad()
			}
			switch p.Method {
			case "pidfd_no_pid", "pidfd_exited":
				if p.ReplacementStart != "" {
					return bad()
				}
			case "pidfd_new_incarnation":
				if p.ScanIndex == 4 {
					return bad()
				}
				if !positiveNumber(p.ReplacementStart) || p.ReplacementStart == *p.StartTicks {
					return bad()
				}
			default:
				return bad()
			}
		default:
			return bad()
		}
		start := ""
		if p.StartTicks != nil {
			start = *p.StartTicks
		}
		key := fmt.Sprintf("%d/%d/%s", p.ScanIndex, p.PID, start)
		if seen[key] {
			return bad()
		}
		seen[key] = true
	}
	if c.Closings[1].LiveCount-final.ClassifiedCount > retirement4 || c.Closings[0].LiveCount-final.ClassifiedCount > retirement3 {
		return bad()
	}
	if c.Closings[1].LiveCount == final.ClassifiedCount && c.Closings[1].LiveDigest != final.ClassifiedDigest {
		return bad()
	}
	if c.Closings[0].LiveCount == c.ReconciledCount && c.Closings[0].LiveDigest != c.ReconciledDigest {
		return bad()
	}
	for _, e := range errors {
		if e.ScanIndex < 1 || e.ScanIndex > 4 {
			return bad()
		}
		// A raw coverage change is resolved by the complete census, not a PID proof.
		if e.Reason == "coverage_changed" && e.Operation == "enumerate" && e.PID == 0 && e.Errno == 0 && e.StartTicks == nil && e.ResolvedBy == -1 {
			if e.ScanIndex != 4 {
				return bad()
			}
			continue
		}
		if e.ResolvedBy < 1 || e.ResolvedBy > len(c.Proofs) || e.Reason != "process_unavailable" || e.ScanIndex < 1 || e.ScanIndex > 4 {
			return bad()
		}
		if !(e.Operation == "stat" && e.Errno == 2 || e.Operation == "environ" && e.Errno == 3 || e.Operation == "comm" && (e.Errno == 2 || e.Errno == 3)) {
			return bad()
		}
		p := c.Proofs[e.ResolvedBy-1]
		if p.PID != e.PID || !sameStart(p.StartTicks, e.StartTicks) || p.ScanIndex < e.ScanIndex || p.Kind == "enumerated_pid_absent" && e.Operation != "stat" {
			return bad()
		}
	}
	return nil
}
