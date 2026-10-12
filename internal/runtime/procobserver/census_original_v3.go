package procobserver

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
)

func originalResolutionKind(kind string) bool {
	return kind == "deferred_managed_retirement" || kind == "managed_birth_retirement" || kind == "direct_reaped_birth"
}

// validateOriginalExtensions checks distinct M/R links; it never relaxes a
// legacy certificate's same-scan own-fault relation or infers terminal metadata.
func validateOriginalExtensions(c CensusV3, errors []EvidenceError, rows map[int]map[int]CensusIdentity, controller int) bool {
	n := len(c.Scans)
	proofUses := map[int]int{}
	handles := map[uint64]bool{}
	for _, r := range c.Resolutions {
		o := r.OriginalCapture
		if !originalResolutionKind(r.Kind) {
			if o != nil {
				return false
			}
			continue
		}
		if o == nil || r.ErrorIndex < 1 || r.ErrorIndex > len(errors) || r.ProofIndex < 1 || r.ProofIndex > len(c.Proofs) ||
			r.ClassifiedScan < 1 || r.ClassifiedScan > n || r.SelectedSeal != n || o.CaptureScan < 1 || o.CaptureScan > n ||
			o.MissingScan <= o.CaptureScan || o.MissingScan >= n || o.AbsenceProof < 1 || o.AbsenceProof >= r.ProofIndex ||
			o.BindingOffsetMS < 0 || o.BindingOffsetMS > c.Scans[o.CaptureScan-1].OffsetMS ||
			o.TerminalOffsetMS < c.Scans[o.MissingScan-1].OffsetMS || o.TerminalOffsetMS > c.Scans[n-1].OffsetMS ||
			o.TerminalOffsetMS >= 10000 || o.CaptureHandle == 0 || handles[o.CaptureHandle] || o.OriginalPIDFDGeneration == 0 || o.OriginalProcdirGeneration == 0 || o.OriginalPIDFDInode == 0 || o.OriginalProcdirInode == 0 ||
			o.TerminalFDInfoPID != -1 || o.TerminalPoll != "valid_pollin" || !o.SameOriginalDescriptors {
			return false
		}
		handles[o.CaptureHandle] = true
		e := errors[r.ErrorIndex-1]
		p, a := c.Proofs[r.ProofIndex-1], c.Proofs[o.AbsenceProof-1]
		prior, present := rows[r.ClassifiedScan][o.PID]
		if !present || !validCensusIdentity(prior, controller) || prior.DeclaredRoot || infrastructureName(prior.Name) ||
			o.PID <= 1 || o.PID == controller || e.PID != o.PID || e.StartTicks == nil || *e.StartTicks != o.BoundStartTicks ||
			prior.StartTicks != o.BoundStartTicks || e.ScanIndex != o.CaptureScan || e.ResolvedBy != 0 ||
			p.PID != o.PID || p.StartTicks == nil || *p.StartTicks != o.BoundStartTicks || p.Kind != "incarnation_retired" ||
			a.PID != o.PID || a.StartTicks != nil || a.Kind != "enumerated_pid_absent" ||
			p.Method != "pidfd_no_pid" || a.Method != "pidfd_no_pid" || p.ReplacementStart != "" || a.ReplacementStart != "" ||
			a.ScanIndex != p.ScanIndex || a.OffsetMS != p.OffsetMS || p.ExitWitness != nil || a.ExitWitness != nil ||
			a.ProtectedIdentity || a.DescendantCertificateID != 0 || p.OffsetMS > c.Scans[n-1].OffsetMS {
			return false
		}
		if r.Kind == "managed_birth_retirement" {
			if p.ScanIndex > o.MissingScan || p.ScanIndex <= o.CaptureScan || p.OffsetMS < c.Scans[o.CaptureScan-1].OffsetMS {
				return false
			}
		} else if p.ScanIndex != o.MissingScan || p.OffsetMS < o.TerminalOffsetMS {
			return false
		}
		for i, u := range prior.UIDs {
			if u == 0 || o.OriginalUIDs[i] != u {
				return false
			}
		}
		managed := r.Kind != "direct_reaped_birth"
		if managed != p.ProtectedIdentity || (managed && (prior.Classification != "managed" || o.CertificateID < 1 ||
			o.CertificateID > len(c.Certificates) || p.DescendantCertificateID != o.CertificateID)) ||
			(!managed && (prior.Classification != "nonmanaged" || o.CertificateID != 0 || p.DescendantCertificateID != 0)) {
			return false
		}
		if r.Kind == "deferred_managed_retirement" {
			d := e.CaptureDiagnostic
			if r.ClassifiedScan >= e.ScanIndex || e.Reason != "process_unavailable" || e.Operation != "environ" || e.Errno != 3 ||
				d == nil || !validCaptureDiagnostic(e, false) || d.FieldsPass != 1 || !*d.BoundLive || *d.FirstIdentityPresent ||
				d.ExitPoll != "zero" || *d.ExitWitness || !o.CommVerifiedBeforeFirstEnviron {
				return false
			}
		} else if o.CommVerifiedBeforeFirstEnviron || e.Reason != "uninspected_birth" || e.Operation != "census" || e.Errno != 0 ||
			r.ClassifiedScan != e.ScanIndex || e.ScanIndex < 4 || c.Scans[e.ScanIndex-1].Kind != "seal" ||
			!originalNumericAbsent(c.Scans[e.ScanIndex-2], o.PID) {
			return false
		}
		for scan := 1; scan <= n; scan++ {
			row, found := rows[scan][o.PID]
			if scan >= o.MissingScan {
				if found || !originalNumericAbsent(c.Scans[scan-1], o.PID) {
					return false
				}
			} else if found && row != prior {
				return false
			}
		}
		if managed {
			cert := c.Certificates[o.CertificateID-1]
			if cert.AbsenceProof != o.AbsenceProof || cert.RetirementProof != r.ProofIndex || len(cert.Chain) < 2 || cert.Chain[0] != prior || !certificateOwnFault(c, errors, cert) ||
				(r.Kind == "deferred_managed_retirement" && cert.PriorScan != r.ClassifiedScan) {
				return false
			}
			for _, ancestor := range cert.Chain[1:] {
				for scan := o.CaptureScan; scan <= n; scan++ {
					if rows[scan][ancestor.PID] != ancestor {
						return false
					}
				}
			}
		} else {
			if _, ok := exitPrior(c, rows, o.PID, r.ClassifiedScan, controller); !ok {
				return false
			}
		}
		proofUses[r.ProofIndex]++
		if proofUses[r.ProofIndex] > 2 {
			return false
		}
	}
	for proof, uses := range proofUses {
		absence := 0
		for _, r := range c.Resolutions {
			if r.ProofIndex == proof && r.OriginalCapture != nil {
				absence = r.OriginalCapture.AbsenceProof
			}
		}
		birth, deferred, legacy := 0, 0, 0
		for _, r := range c.Resolutions {
			if r.ProofIndex != proof && r.ProofIndex != absence {
				continue
			}
			switch r.Kind {
			case "managed_birth_retirement":
				birth++
			case "deferred_managed_retirement":
				deferred++
			case "kernel_absence":
				legacy++
			case "direct_reaped_birth":
				if uses != 1 {
					return false
				}
			default:
				return false
			}
		}
		if birth > 1 || deferred > 1 || legacy > 1 || (birth > 0 && deferred+legacy != 1) || (deferred > 0 && legacy > 0) {
			return false
		}
	}
	return true
}

func originalNumericAbsent(s CensusScanV3, pid int) bool {
	if !validNumericEnumeration(s) {
		return false
	}
	index := sort.SearchInts(s.NumericPIDs, pid)
	return index == len(s.NumericPIDs) || s.NumericPIDs[index] != pid
}

// UnmarshalJSON preserves the distinction between omitted legacy custody and
// an explicit null or malformed original-capture extension.
func (r *CensusResolution) UnmarshalJSON(data []byte) error {
	type alias CensusResolution
	var v alias
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(&v); err != nil {
		return err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	if raw, ok := fields["original_capture"]; ok && bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return fmt.Errorf("null original capture")
	}
	*r = CensusResolution(v)
	return nil
}

// UnmarshalJSON requires exact UID4 and all typed custody fields, including
// meaningful zero-valued device/offset/certificate metadata.
func (o *CensusOriginalCapture) UnmarshalJSON(data []byte) error {
	type alias CensusOriginalCapture
	var v alias
	w := struct {
		*alias
		OriginalUIDs []uint32 `json:"original_uids"`
	}{alias: &v}
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(&w); err != nil {
		return err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	if len(fields) != 20 || len(w.OriginalUIDs) != 4 {
		return fmt.Errorf("original custody shape")
	}
	copy(v.OriginalUIDs[:], w.OriginalUIDs)
	*o = CensusOriginalCapture(v)
	return nil
}
