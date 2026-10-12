package procobserver

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"sort"
	"strconv"
)

// Numeric membership is the actual complete enumeration, including unverified
// entries. Its ordered digest is checked independently of the live row digest.
func validNumericEnumeration(s CensusScanV3) bool {
	if len(s.NumericPIDs) != s.EnumeratedCount || len(s.NumericPIDs) == 0 || len(s.NumericPIDs) > MaxProcesses {
		return false
	}
	h := sha256.New()
	field := func(v string) {
		var n [4]byte
		binary.BigEndian.PutUint32(n[:], uint32(len(v)))
		h.Write(n[:])
		h.Write([]byte(v))
	}
	field("bounded-process-census/v3/enumerated-pids/1")
	previous := 0
	for _, pid := range s.NumericPIDs {
		if pid <= previous || pid > 2147483647 {
			return false
		}
		previous = pid
		field(strconv.Itoa(pid))
	}
	if hex.EncodeToString(h.Sum(nil)) != s.EnumerationDigest {
		return false
	}
	for _, row := range s.Verified {
		index := sort.SearchInts(s.NumericPIDs, row.PID)
		if index == len(s.NumericPIDs) || s.NumericPIDs[index] != row.PID {
			return false
		}
	}

	return true
}

func exitPrior(c CensusV3, rows map[int]map[int]CensusIdentity, pid, through, controller int) (CensusIdentity, bool) {
	var prior CensusIdentity
	found := false
	for scan := 1; scan <= len(c.Scans); scan++ {
		v, ok := rows[scan][pid]
		if !ok {
			continue
		}
		if !validCensusIdentity(v, controller) || v.Classification != "nonmanaged" || v.DeclaredRoot || infrastructureName(v.Name) {
			return prior, false
		}
		for _, uid := range v.UIDs {
			if uid == 0 {
				return prior, false
			}
		}
		if found && !sameFreshClassification(prior, v) {
			return prior, false
		}
		if scan <= through {
			prior = v
			found = true
		}
	}
	return prior, found
}

func validateExitExtensions(c CensusV3, errors []EvidenceError, rows map[int]map[int]CensusIdentity, controller int) bool {
	n := len(c.Scans)
	active := len(c.CoverageRetirements) > 0
	if len(c.CoverageRetirements) > 128 {
		return false
	}
	numericValid := map[int]bool{}
	for _, s := range c.Scans {
		if s.NumericPIDs != nil {
			if !validNumericEnumeration(s) {
				return false
			}
			numericValid[s.ScanIndex] = true
		}
	}
	absent := func(s CensusScanV3, pid int) bool {
		if !numericValid[s.ScanIndex] {
			return false
		}
		index := sort.SearchInts(s.NumericPIDs, pid)
		return index == len(s.NumericPIDs) || s.NumericPIDs[index] != pid
	}
	usedProof := map[int]bool{}
	retired := map[string]bool{}
	for index, p := range c.Proofs {
		w := p.ExitWitness
		if w == nil {
			continue
		}
		active = true
		if p.Kind != "terminal_zombie" || p.Method != "pidfd_zombie_stat" || p.ProtectedIdentity || p.DescendantCertificateID != 0 || p.ReplacementStart != "" ||
			p.StartTicks == nil || !positiveNumber(*p.StartTicks) || w.BoundStartTicks != *p.StartTicks || w.TerminalPoll != "valid_pollin" ||
			p.ScanIndex < 1 || p.ScanIndex >= n || p.OffsetMS < c.Scans[p.ScanIndex-1].OffsetMS || p.OffsetMS > c.Scans[n-1].OffsetMS {
			return false
		}
		key := strconv.Itoa(p.PID) + "/" + *p.StartTicks
		if retired[key] {
			return false
		}
		retired[key] = true
		for scan := p.ScanIndex; scan <= n; scan++ {
			if _, ok := rows[scan][p.PID]; ok {
				original := false
				if scan == p.ScanIndex && w.CaptureStage == "bound_seal_birth_terminal" {
					for _, r := range c.Resolutions {
						if r.ProofIndex == index+1 && r.Kind == "bound_seal_birth_terminal" && r.ClassifiedScan == scan {
							original = true
						}
					}
				}
				if !original {
					return false
				}
			}
		}
		births, envs, links := 0, 0, 0
		birthScan := 0
		for _, r := range c.Resolutions {
			if r.ProofIndex != index+1 {
				continue
			}
			if r.ErrorIndex < 1 || r.ErrorIndex > len(errors) || r.SelectedSeal != n || r.ClassifiedScan < 1 || r.ClassifiedScan > n {
				return false
			}
			e := errors[r.ErrorIndex-1]
			if e.PID != p.PID || e.ScanIndex < 1 || e.ScanIndex > p.ScanIndex || e.ResolvedBy != 0 ||
				p.OffsetMS < c.Scans[e.ScanIndex-1].OffsetMS || p.OffsetMS < c.Scans[r.ClassifiedScan-1].OffsetMS {
				return false
			}
			prior, ok := exitPrior(c, rows, p.PID, r.ClassifiedScan, controller)
			original, present := rows[r.ClassifiedScan][p.PID]
			if !ok || !present || original != prior || prior.StartTicks != *p.StartTicks {
				return false
			}
			for i, uid := range prior.UIDs {
				if uid == 0 || w.TerminalUIDsBefore[i] != uid || w.TerminalUIDsAfter[i] != uid {
					return false
				}
			}
			links++
			switch w.CaptureStage {
			case "bound_first_environ":
				if w.FullIdentityRevalidated || r.Kind != "known_nonmanaged_terminal" || r.ClassifiedScan >= e.ScanIndex ||
					e.Reason != "process_unavailable" || e.Operation != "environ" || e.Errno != 3 || !sameStart(e.StartTicks, p.StartTicks) ||
					(w.InitialPoll != "zero" && w.InitialPoll != "valid_pollin") {
					return false
				}
				d := e.CaptureDiagnostic
				if d == nil || !validCaptureDiagnostic(e, false) || d.FieldsPass != 1 || !*d.BoundLive || *d.FirstIdentityPresent || d.ExitPoll != w.InitialPoll {
					return false
				}
			case "initial_pidfd_poll":
				if w.FullIdentityRevalidated || w.InitialPoll != "valid_pollin" || r.Kind != "known_nonmanaged_terminal" || r.ClassifiedScan >= e.ScanIndex ||
					e.Reason != "process_unavailable" || e.Operation != "pidfd_poll" || e.Errno != 116 || e.StartTicks != nil || e.CaptureDiagnostic != nil {
					return false
				}
			case "bound_seal_birth_terminal":
				if !w.FullIdentityRevalidated || w.InitialPoll != "zero" {
					return false
				}
				if r.Kind == "bound_seal_birth_terminal" {
					if e.Reason != "uninspected_birth" || e.Operation != "census" || e.Errno != 0 || !sameStart(e.StartTicks, p.StartTicks) ||
						r.ClassifiedScan != e.ScanIndex || e.ScanIndex < 4 || e.ScanIndex >= n-2 || c.Scans[e.ScanIndex-1].Kind != "seal" ||
						!absent(c.Scans[e.ScanIndex-2], p.PID) {
						return false
					}
					births++
					birthScan = e.ScanIndex
				} else {
					if r.Kind != "known_nonmanaged_terminal" || r.ClassifiedScan >= e.ScanIndex || e.Reason != "process_unavailable" || e.Operation != "environ" ||
						e.Errno != 3 || !sameStart(e.StartTicks, p.StartTicks) {
						return false
					}
					d := e.CaptureDiagnostic
					if d == nil || !validCaptureDiagnostic(e, false) || d.FieldsPass != 1 || !*d.BoundLive ||
						*d.FirstIdentityPresent || d.ExitPoll != "zero" || *d.ExitWitness {
						return false
					}
					envs++
				}
			default:
				return false
			}
		}
		if w.CaptureStage == "bound_seal_birth_terminal" {
			if births != 1 || envs > 1 || links != births+envs {
				return false
			}
			for _, r := range c.Resolutions {
				if r.ProofIndex == index+1 && r.ClassifiedScan != birthScan {
					return false
				}
			}
		} else if links != 1 {
			return false
		}
		usedProof[index+1] = true
	}
	for _, v := range c.CoverageRetirements {
		if v.PID <= 1 || v.PID == controller || !positiveNumber(v.StartTicks) || v.PriorScan < 1 || v.PriorScan >= v.MissingScan ||
			v.MissingScan >= n || v.MissingScan < 1 || v.SelectedSeal != n || v.AbsenceProof < 1 || v.AbsenceProof >= v.RetirementProof ||
			v.RetirementProof > len(c.Proofs) || usedProof[v.AbsenceProof] || usedProof[v.RetirementProof] {
			return false
		}
		a, p := c.Proofs[v.AbsenceProof-1], c.Proofs[v.RetirementProof-1]
		if a.Kind != "enumerated_pid_absent" || p.Kind != "incarnation_retired" || a.Method != "pidfd_no_pid" || p.Method != "pidfd_no_pid" ||
			a.PID != v.PID || p.PID != v.PID || a.StartTicks != nil || p.StartTicks == nil || *p.StartTicks != v.StartTicks ||
			a.ScanIndex != v.MissingScan || p.ScanIndex != v.MissingScan || a.OffsetMS != p.OffsetMS ||
			a.ProtectedIdentity || p.ProtectedIdentity || a.DescendantCertificateID != 0 || p.DescendantCertificateID != 0 ||
			a.ReplacementStart != "" || p.ReplacementStart != "" || a.ExitWitness != nil || p.ExitWitness != nil ||
			p.OffsetMS < c.Scans[v.MissingScan-1].OffsetMS || p.OffsetMS > c.Scans[n-1].OffsetMS || !absent(c.Scans[v.MissingScan-1], v.PID) {
			return false
		}
		prior, ok := exitPrior(c, rows, v.PID, v.PriorScan, controller)
		if !ok || rows[v.PriorScan][v.PID] != prior || prior.StartTicks != v.StartTicks {
			return false
		}
		for scan := v.PriorScan + 1; scan < v.MissingScan; scan++ {
			if _, ok := rows[scan][v.PID]; ok {
				return false
			}
		}
		for scan := v.MissingScan; scan <= n; scan++ {
			if !absent(c.Scans[scan-1], v.PID) {
				return false
			}
		}
		for _, e := range errors {
			if e.PID == v.PID {
				return false
			}
		}
		for _, r := range c.Resolutions {
			if r.ProofIndex == v.AbsenceProof || r.ProofIndex == v.RetirementProof {
				return false
			}
		}
		key := strconv.Itoa(v.PID) + "/" + v.StartTicks
		if retired[key] {
			return false
		}
		retired[key] = true
		usedProof[v.AbsenceProof] = true
		usedProof[v.RetirementProof] = true
	}
	// Every extension link must name its validated proof, never just a new label.
	for _, r := range c.Resolutions {
		if (r.Kind == "known_nonmanaged_terminal" || r.Kind == "bound_seal_birth_terminal") &&
			(r.ProofIndex < 1 || r.ProofIndex > len(c.Proofs) || c.Proofs[r.ProofIndex-1].ExitWitness == nil || !usedProof[r.ProofIndex]) {
			return false
		}
	}
	if active {
		// Every surviving verified row must join BOTH actual selected closings.
		for pid, last := range rows[n] {
			if !sameFreshClassification(rows[n-2][pid], last) || !sameFreshClassification(rows[n-1][pid], last) {
				return false
			}
		}
		for _, scan := range []int{n - 2, n - 1} {
			removals := 0
			for pid, prior := range rows[scan] {
				if last, ok := rows[n][pid]; ok && sameFreshClassification(prior, last) {
					continue
				}
				matches := 0
				for _, p := range c.Proofs {
					if p.PID == pid && p.StartTicks != nil && *p.StartTicks == prior.StartTicks && p.ScanIndex > scan &&
						(p.Kind == "incarnation_retired" || p.ExitWitness != nil) {
						matches++
					}
				}
				if matches != 1 {
					return false
				}
				removals++
			}
			if c.Scans[scan-1].LiveCount-removals != c.ReconciledCount {
				return false
			}
		}
	}
	return true
}
