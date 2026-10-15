package procobserver

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"strconv"
	"strings"
	"testing"
	"time"
)

func reacquireFixture() (ReleasePolicyV3, []ResponseV3, time.Time) {
	p, full, now := clientV3Fixture()
	full.Roots[0].ParentName = "tmux: server"
	full.Roots[0].ParentIsProviderInfrastructure = true
	partial := full
	partial.Complete = false
	partial.Roots = []Root{}
	partial.Census.SelectedClosings = [2]int{}
	partial.Census.Seal = CensusSeal{}
	partial.Census.ReconciledCount = 0
	partial.Census.ReconciledDigest = ""
	partial.Census.Scans = append([]CensusScanV3(nil), full.Census.Scans[:2]...)
	for i := range partial.Census.Scans {
		s := &partial.Census.Scans[i]
		s.Verified = []CensusIdentity{full.Census.Scans[0].Verified[0]}
		s.LiveCount = 1
		s.NumericPIDs = []int{20}
		if i == 1 {
			s.NumericPIDs = append(s.NumericPIDs, 99)
		}
		s.EnumeratedCount = len(s.NumericPIDs)
		h := sha256.New()
		field := func(v string) {
			var b [4]byte
			binary.BigEndian.PutUint32(b[:], uint32(len(v)))
			h.Write(b[:])
			h.Write([]byte(v))
		}
		field("bounded-process-census/v3/enumerated-pids/1")
		for _, v := range s.NumericPIDs {
			field(strconv.Itoa(v))
		}
		s.EnumerationDigest = hex.EncodeToString(h.Sum(nil))
	}
	partial.EnumeratedCountBefore = partial.Census.Scans[0].EnumeratedCount
	partial.EnumerationDigestBefore = partial.Census.Scans[0].EnumerationDigest
	partial.EnumeratedCountAfter = partial.Census.Scans[1].EnumeratedCount
	partial.EnumerationDigestAfter = partial.Census.Scans[1].EnumerationDigest
	partial.Census.Proofs = []CensusProofV3{}
	partial.Census.Certificates = []DescendantCertificate{}
	partial.Census.Resolutions = []CensusResolution{}
	yes, no := true, false
	start := "999"
	partial.Errors = []EvidenceError{{Reason: "process_unavailable", Operation: "environ", Errno: 3, PID: 99, StartTicks: &start, ScanIndex: 2, CaptureDiagnostic: &CaptureDiagnostic{FieldsPass: 1, BoundLive: &yes, FirstIdentityPresent: &no, ExitPoll: "zero", ExitWitness: &no}}}
	partial.ErrorsTotal = 1
	partial.StartedAt = now.Add(-7 * time.Millisecond)
	partial.FinishedAt = now.Add(-4 * time.Millisecond)
	partial.RequestNonce = strings.Repeat("d", 64)
	return p, []ResponseV3{partial, full}, now
}

func TestReacquireEligibleCaptureBothOrientations(t *testing.T) {
	for _, reverse := range []bool{false, true} {
		p, f, now := reacquireFixture()
		if reverse {
			f[0], f[1] = f[1], f[0]
			f[0].StartedAt = now.Add(-7 * time.Millisecond)
			f[0].FinishedAt = now.Add(-4 * time.Millisecond)
			f[1].StartedAt = now.Add(-3 * time.Millisecond)
			f[1].FinishedAt = now
		}
		if !EligibleCapturePairReacquisitionV3(f, p, now) {
			t.Fatal("eligible capture refused; reverse", reverse)
		}
	}
}

// This derives only the reported 20:39 error shape: second frame, first scan,
// first environ failure, no FULL prior, poll ZERO. It is not the original wire
// and does not establish the historical provider tracker's success.
func TestReacquireReportedOneScanEnvironShape(t *testing.T) {
	p, f, now := reacquireFixture()
	partial, complete := f[0], f[1]
	partial.Census.Scans = partial.Census.Scans[:1]
	s := &partial.Census.Scans[0]
	s.NumericPIDs = []int{20, 99}
	s.EnumeratedCount = 2
	s.EnumerationDigest = numericDigestForReacquire(s.NumericPIDs)
	partial.EnumeratedCountBefore, partial.EnumeratedCountAfter = 2, 2
	partial.EnumerationDigestBefore, partial.EnumerationDigestAfter = s.EnumerationDigest, s.EnumerationDigest
	partial.Errors[0].ScanIndex = 1
	complete.StartedAt, complete.FinishedAt = now.Add(-7*time.Millisecond), now.Add(-4*time.Millisecond)
	partial.StartedAt, partial.FinishedAt = now.Add(-3*time.Millisecond), now
	if !EligibleCapturePairReacquisitionV3([]ResponseV3{complete, partial}, p, now) {
		t.Fatal("reported first-scan capture shape refused")
	}
}

func TestReacquireCaptureFatalNegatives(t *testing.T) {
	for _, name := range []string{"pin", "nonce", "namespace", "permission", "unsupported", "resource", "truncated", "malformed-numeric", "root-change", "root-missing", "known-managed", "second-pass", "poll-flags", "both-partial", "no-errors", "unknown-kind", "over-budget", "certificate", "kernel", "schema", "round", "zero-resource", "selected-seal", "complete-protected"} {
		t.Run(name, func(t *testing.T) {
			p, f, now := reacquireFixture()
			v := &f[0]
			switch name {
			case "pin":
				v.HelperBinarySHA256 = strings.Repeat("0", 64)
			case "nonce":
				v.RequestNonce = f[1].RequestNonce
			case "namespace":
				v.PIDNamespaceIdentity = "pid:[999]"
			case "permission":
				v.Errors[0].Errno = 13
			case "unsupported":
				v.Errors[0].Errno = 38
			case "resource":
				v.Errors[0].Reason = "resource_guard"
			case "truncated":
				v.ErrorsTruncated = true
			case "malformed-numeric":
				v.Census.Scans[0].EnumerationDigest = strings.Repeat("0", 64)
			case "root-change":
				v.Census.Scans[0].Verified = append([]CensusIdentity(nil), v.Census.Scans[0].Verified...)
				v.Census.Scans[0].Verified[0].StartTicks = "201"
			case "root-missing":
				v.Census.Scans[0].Verified = []CensusIdentity{}
			case "known-managed":
				v.Errors[0].PID = 30
			case "second-pass":
				v.Errors[0].CaptureDiagnostic.FieldsPass = 2
			case "poll-flags":
				v.Errors[0].CaptureDiagnostic.ExitPoll = "refused_flags"
			case "both-partial":
				f[1].Complete = false
			case "no-errors":
				v.Errors = []EvidenceError{}
				v.ErrorsTotal = 0
			case "unknown-kind":
				v.Errors[0].Operation = "unknown"
			case "over-budget":
				v.Census.PeakFD = 33
			case "certificate":
				v.Census.Certificates = []DescendantCertificate{{ID: 1}}
			case "kernel":
				v.KernelRelease = "7.0.unsupported"
			case "schema":
				v.Census.Schema = "unknown"
			case "round":
				v.Census.Scans[1].Round = 2
			case "zero-resource":
				v.Census.PeakFD = 0
			case "selected-seal":
				v.Census.Seal.ScanIndex = 2
			case "complete-protected":
				v.Errors[0].PID = 30
				v.Census.Scans[1].NumericPIDs = []int{20, 30}
				// Numeric receipt remains coherent, so the protected identity veto
				// is independently tested rather than a broken digest shortcut.
				v.Census.Scans[1].EnumerationDigest = numericDigestForReacquire([]int{20, 30})
				v.EnumerationDigestAfter = v.Census.Scans[1].EnumerationDigest
			}
			if EligibleCapturePairReacquisitionV3(f, p, now) {
				t.Fatal("fatal capture eligible")
			}
		})
	}
}

func numericDigestForReacquire(pids []int) string {
	h := sha256.New()
	field := func(v string) {
		var b [4]byte
		binary.BigEndian.PutUint32(b[:], uint32(len(v)))
		_, _ = h.Write(b[:])
		_, _ = h.Write([]byte(v))
	}
	field("bounded-process-census/v3/enumerated-pids/1")
	for _, pid := range pids {
		field(strconv.Itoa(pid))
	}
	return hex.EncodeToString(h.Sum(nil))
}

func TestReacquireKernelAbsenceLinks(t *testing.T) {
	for _, mode := range []string{"valid", "unknown-resolution", "error-index", "proof-index", "proof-pid", "duplicate-resolution", "shared-proof", "protected-proof"} {
		t.Run(mode, func(t *testing.T) {
			p, f, now := reacquireFixture()
			v := &f[0]
			v.Errors[0].Operation, v.Errors[0].StartTicks, v.Errors[0].CaptureDiagnostic = "pidfd_open", nil, nil
			v.Census.Proofs = []CensusProofV3{{CensusProof: CensusProof{Kind: "enumerated_pid_absent", Method: "pidfd_no_pid", PID: 99, ScanIndex: 2, OffsetMS: 1}}}
			v.Census.Resolutions = []CensusResolution{{ErrorIndex: 1, Kind: "kernel_absence", ProofIndex: 1}}
			switch mode {
			case "unknown-resolution":
				v.Census.Resolutions[0].Kind = "unknown"
			case "error-index":
				v.Census.Resolutions[0].ErrorIndex = 2
			case "proof-index":
				v.Census.Resolutions[0].ProofIndex = 2
			case "proof-pid":
				v.Census.Proofs[0].PID = 98
			case "duplicate-resolution":
				v.Census.Resolutions = append(v.Census.Resolutions, v.Census.Resolutions[0])
			case "shared-proof":
				v.Errors = append(v.Errors, v.Errors[0])
				v.ErrorsTotal++
				v.Census.Resolutions = append(v.Census.Resolutions, CensusResolution{ErrorIndex: 2, Kind: "kernel_absence", ProofIndex: 1})
			case "protected-proof":
				v.Census.Proofs[0].ProtectedIdentity = true
			}
			if EligibleCapturePairReacquisitionV3(f, p, now) != (mode == "valid") {
				t.Fatal("kernel absence link result", mode)
			}
		})
	}
}
