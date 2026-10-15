package procobserver

import (
	"encoding/json"
	"strconv"
	"strings"
	"time"
)

// EligibleCapturePairReacquisitionV3 checks only the captured helper pair.
// The owning service must separately require typed projection-only failures,
// stable live provider ownership, and its original caller/generation fences.
// It supplies no COMPLETE authority and never repairs a partial frame.
func EligibleCapturePairReacquisitionV3(frames []ResponseV3, p ReleasePolicyV3, now time.Time) bool {
	if len(frames) != 2 {
		return false
	}
	var complete *ResponseV3
	partial := 0
	for i := range frames {
		f := &frames[i]
		b, err := json.Marshal(f)
		if err != nil {
			return false
		}
		checked, err := DecodeResponseV3(b, p, f.RequestNonce, now)
		if checked.Schema != ResponseSchemaV3 || f.KernelProofProfile != KernelProofProfile || !strings.HasPrefix(f.KernelRelease, "6.8.") || (f.Complete && err != nil) {
			return false
		}
		if f.Complete {
			complete = f
		} else {
			partial++
		}
		if i > 0 && (f.RequestNonce == frames[i-1].RequestNonce || f.StartedAt.Before(frames[i-1].FinishedAt)) {
			return false
		}
	}
	if partial != 1 || complete == nil {
		return false
	}
	// A complete helper half is only an eligibility veto. It is never combined
	// with any subsequent helper half or used as positive final provider authority.
	roots := map[int]Root{}
	for _, r := range complete.Roots {
		roots[r.PID] = r
	}
	for _, f := range frames {
		if f.Complete {
			continue
		}
		c := f.Census
		if c.Schema != CensusV3Schema || len(c.Scans) < 1 || len(c.Scans) > 31 || len(f.Errors) == 0 || len(f.Errors) > 128 || f.ErrorsTotal != len(f.Errors) || f.ErrorsTruncated ||
			c.PeakFD < 1 || c.PeakFD > 32 || c.RetainedBytes < 1 || c.RetainedBytes > 16*1024*1024 || c.Proofs == nil || len(c.Proofs) > 128 || c.Resolutions == nil || len(c.Resolutions) > 128 ||
			c.Certificates == nil || len(c.Certificates) != 0 || len(c.CoverageRetirements) != 0 || c.SelectedClosings != [2]int{} || c.Seal != (CensusSeal{}) || c.ReconciledCount != 0 || c.ReconciledDigest != "" || f.DurationMS >= 10000 {
			return false
		}
		first, last := c.Scans[0], c.Scans[len(c.Scans)-1]
		if f.EnumeratedCountBefore != first.EnumeratedCount || f.EnumerationDigestBefore != first.EnumerationDigest || f.EnumeratedCountAfter != last.EnumeratedCount || f.EnumerationDigestAfter != last.EnumerationDigest {
			return false
		}
		rows := map[int]map[int]CensusIdentity{}
		managed := map[int]CensusIdentity{}
		for i, s := range c.Scans {
			kind := "closing"
			round := (i-1)/3 + 1
			if i == 0 {
				kind = "initial"
				round = 0
			} else if i%3 == 0 {
				kind = "seal"
			}
			if s.ScanIndex != i+1 || s.Kind != kind || s.Round != round || s.OffsetMS < 0 || s.OffsetMS > f.DurationMS ||
				i > 0 && s.OffsetMS < c.Scans[i-1].OffsetMS || !validNumericEnumeration(s) || s.LiveCount < 1 || s.LiveCount > s.EnumeratedCount || s.Verified == nil || len(s.Verified) > s.LiveCount || !isHex(s.LiveDigest, 64) {
				return false
			}
			rows[s.ScanIndex] = map[int]CensusIdentity{}
			for _, v := range s.Verified {
				if !validCensusIdentity(v, p.CallerBinding.PID) || rows[s.ScanIndex][v.PID].PID != 0 || originalNumericAbsent(s, v.PID) {
					return false
				}
				rows[s.ScanIndex][v.PID] = v
				if v.Classification == "managed" {
					if old, exists := managed[v.PID]; exists && old != v {
						return false
					}
					managed[v.PID] = v
				}
				if v.DeclaredRoot {
					r, ok := roots[v.PID]
					if !ok || !censusIdentityMatchesRoot(v, r) {
						return false
					}
				}
			}
			for pid, v := range managed {
				if rows[s.ScanIndex][pid] != v {
					return false
				}
			}
			// Every already observed provider/root must remain FULL throughout the
			// failed prefix. A missing or changed root is not a transient leaf exception.
			for pid, r := range roots {
				v, ok := rows[s.ScanIndex][pid]
				if !ok || !censusIdentityMatchesRoot(v, r) {
					return false
				}
			}
		}
		for _, e := range f.Errors {
			if e.ScanIndex < 1 || e.ScanIndex > len(c.Scans) || e.PID <= 1 || e.PID == p.CallerBinding.PID || e.ResolvedBy != 0 || e.StartTicks != nil && !positiveNumber(*e.StartTicks) || !validCaptureDiagnostic(e, false) || originalNumericAbsent(c.Scans[e.ScanIndex-1], e.PID) {
				return false
			}
			for _, scan := range rows {
				if v, ok := scan[e.PID]; ok && (v.Classification != "nonmanaged" || v.DeclaredRoot || infrastructureName(v.Name)) {
					return false
				}
			}
			for _, s := range complete.Census.Scans {
				for _, v := range s.Verified {
					if v.PID == e.PID && (v.Classification != "nonmanaged" || v.DeclaredRoot || infrastructureName(v.Name) || e.StartTicks != nil && v.StartTicks != *e.StartTicks) {
						return false
					}
				}
			}
			allowed := false
			switch e.Reason {
			case "process_unavailable":
				switch e.Operation {
				case "pidfd_open":
					allowed = e.Errno == 3 && e.StartTicks == nil && e.CaptureDiagnostic == nil
				case "stat":
					allowed = e.Errno == 2 && e.CaptureDiagnostic == nil
				case "environ":
					d := e.CaptureDiagnostic
					allowed = e.Errno == 3 && e.StartTicks != nil && positiveNumber(*e.StartTicks) && d != nil && d.FieldsPass == 1 &&
						d.BoundLive != nil && *d.BoundLive && d.FirstIdentityPresent != nil && !*d.FirstIdentityPresent && d.ExitPoll == "zero" && d.ExitWitness != nil && !*d.ExitWitness
				}
			case "uninspected_birth":
				v, ok := rows[e.ScanIndex][e.PID]
				allowed = e.Operation == "census" && e.Errno == 0 && e.StartTicks != nil && ok && v.StartTicks == *e.StartTicks &&
					e.ScanIndex >= 4 && c.Scans[e.ScanIndex-1].Kind == "seal" && originalNumericAbsent(c.Scans[e.ScanIndex-2], e.PID)
			}
			if !allowed {
				return false
			}
		}
		// Eligibility accepts only existing unprotected positive proof shapes;
		// they remain non-authoritative until a whole complete observation succeeds.
		retired := map[string]bool{}
		for _, q := range c.Proofs {
			if q.PID <= 1 || q.PID == p.CallerBinding.PID || q.ScanIndex < 1 || q.ScanIndex > len(c.Scans) || q.OffsetMS < c.Scans[q.ScanIndex-1].OffsetMS || q.OffsetMS > f.DurationMS || q.ProtectedIdentity || q.DescendantCertificateID != 0 || q.ReplacementStart != "" || q.ExitWitness != nil {
				return false
			}
			switch q.Kind {
			case "enumerated_pid_absent":
				if q.Method != "pidfd_no_pid" || q.StartTicks != nil {
					return false
				}
			case "incarnation_retired":
				if q.StartTicks == nil || !positiveNumber(*q.StartTicks) || q.Method != "pidfd_no_pid" {
					return false
				}
				key := strconv.Itoa(q.PID) + ":" + *q.StartTicks
				if retired[key] {
					return false
				}
				retired[key] = true
			default:
				return false
			}
			for _, scan := range rows {
				if v, ok := scan[q.PID]; ok && (v.Classification != "nonmanaged" || v.DeclaredRoot || infrastructureName(v.Name) || q.StartTicks != nil && v.StartTicks != *q.StartTicks) {
					return false
				}
			}
		}
		seen, usedProofs := map[int]bool{}, map[int]bool{}
		for _, r := range c.Resolutions {
			if r.Kind != "kernel_absence" || r.ErrorIndex < 1 || r.ErrorIndex > len(f.Errors) || seen[r.ErrorIndex] || r.ProofIndex < 1 || r.ProofIndex > len(c.Proofs) || usedProofs[r.ProofIndex] || r.ClassifiedScan != 0 || r.SelectedSeal != 0 || r.OriginalCapture != nil {
				return false
			}
			seen[r.ErrorIndex] = true
			usedProofs[r.ProofIndex] = true
			e, q := f.Errors[r.ErrorIndex-1], c.Proofs[r.ProofIndex-1]
			if e.Reason != "process_unavailable" || q.PID != e.PID || q.ScanIndex < e.ScanIndex || !sameStart(q.StartTicks, e.StartTicks) {
				return false
			}
		}
		// Protected descendants are not silently discarded between attempts.
		// Their separate retirement grammar remains outside this retry taxonomy.
		for _, s := range complete.Census.Scans {
			for pid, v := range managed {
				found := false
				for _, current := range s.Verified {
					found = found || current.PID == pid && current == v
				}
				if !found {
					return false
				}
			}
		}
	}
	return true
}
