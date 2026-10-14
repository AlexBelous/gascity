package procobserver

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"
)

type ownedRoundsVector struct {
	Schema                 string          `json:"schema"`
	Census                 CensusV3        `json:"census"`
	Errors                 []EvidenceError `json:"errors"`
	ErrorsTotal            int             `json:"errors_total"`
	ErrorsTruncated        bool            `json:"errors_truncated"`
	DurationMS             int64           `json:"duration_ms"`
	ControllerPID          int             `json:"controller_pid"`
	CertificateDisposition string          `json:"certificate_disposition"`
}

func TestCensusMoreRoundsOwnedWire(t *testing.T) {
	for _, n := range []int{13, 31} {
		t.Run(fmt.Sprint(n), func(t *testing.T) {
			raw, err := os.ReadFile(fmt.Sprintf("testdata/census_owned_rounds_%d.json", n))
			if err != nil {
				t.Fatal(err)
			}
			var original ownedRoundsVector
			if err := DecodeStrict(raw, &original); err != nil {
				t.Fatal(err)
			}
			check := func(v ownedRoundsVector) error {
				return ValidateCensusV3(v.Census, v.Errors, v.ErrorsTotal, v.ErrorsTruncated, v.DurationMS, v.ControllerPID)
			}
			if err := check(original); err != nil {
				t.Fatalf("real owned C wire: %v", err)
			}
			if len(original.Census.Scans) != n || len(original.Errors) != (n-4)/3 || len(original.Census.Proofs) != 0 || len(original.Census.Certificates) != 0 {
				t.Fatal("unexpected owned birth vector")
			}
			// Existing full decoder, not pure census, binds EVERY sealed root.
			policy, envelope, now := clientV3Fixture()
			policy.CallerBinding.PID = original.ControllerPID
			envelope.CallerBinding = policy.CallerBinding
			envelope.Census, envelope.Errors = original.Census, original.Errors
			envelope.ErrorsTotal = original.ErrorsTotal
			envelope.DurationMS = original.DurationMS
			envelope.StartedAt = now.Add(-time.Duration(original.DurationMS) * time.Millisecond)
			initial := original.Census.Scans[0]
			closing := original.Census.Scans[n-2]
			envelope.EnumeratedCountBefore, envelope.EnumerationDigestBefore = initial.EnumeratedCount, initial.EnumerationDigest
			envelope.EnumeratedCountAfter, envelope.EnumerationDigestAfter = closing.EnumeratedCount, closing.EnumerationDigest
			envelope.Roots = []Root{}
			for _, v := range original.Census.Scans[n-1].Verified {
				if v.DeclaredRoot {
					envelope.Roots = append(envelope.Roots, Root{PID: v.PID, PPID: v.PPID, PGID: v.PGID, StartTicks: v.StartTicks, SessionID: v.SessionID, City: v.City, Template: v.Template, Epoch: v.Epoch, InstanceTokenSHA256: v.InstanceTokenSHA256, Name: v.Name})
				}
			}
			// Envelope/profile/binding values are explicit synthetic UNIT inputs;
			// kernel7 C vectors do NOT become a live supported6.8 attestation.
			rawEnvelope, err := json.Marshal(envelope)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := DecodeResponseV3(rawEnvelope, policy, envelope.RequestNonce, now); err != nil {
				t.Fatalf("synthetic full-envelope positive: %v", err)
			}
			exact := append(append([]byte{}, rawEnvelope...), bytes.Repeat([]byte(" "), MaxResponseBytes-len(rawEnvelope))...)
			if _, err := DecodeResponseV3(exact, policy, envelope.RequestNonce, now); err != nil {
				t.Fatalf("16MiB exact bound: %v", err)
			}
			if _, err := DecodeResponseV3(append(exact, ' '), policy, envelope.RequestNonce, now); err == nil {
				t.Fatal("16MiB+1 accepted")
			}

			for _, mode := range []string{"projection-root-omitted", "sealed-root-omitted"} {
				var changed ResponseV3
				if err := json.Unmarshal(rawEnvelope, &changed); err != nil {
					t.Fatal(err)
				}
				if mode == "projection-root-omitted" {
					changed.Roots = []Root{}
				} else {
					rows := changed.Census.Scans[n-1].Verified
					for i, v := range rows {
						if v.DeclaredRoot {
							rows = append(rows[:i], rows[i+1:]...)
							changed.Census.Scans[n-1].Verified = rows
							break
						}
					}
				}
				bad, _ := json.Marshal(changed)
				if _, err := DecodeResponseV3(bad, policy, envelope.RequestNonce, now); err == nil {
					t.Fatalf("%s accepted", mode)
				}
			}
			for _, tc := range []struct {
				name   string
				mutate func(*ownedRoundsVector)
			}{
				{"deadline", func(v *ownedRoundsVector) { v.DurationMS = 10000 }},
				{"proof129", func(v *ownedRoundsVector) { v.Census.Proofs = make([]CensusProofV3, 129) }},
				{"certificate129", func(v *ownedRoundsVector) { v.Census.Certificates = make([]DescendantCertificate, 129) }},
				{"error129", func(v *ownedRoundsVector) { v.Errors = make([]EvidenceError, 129); v.ErrorsTotal = 129 }},

				{"fd33", func(v *ownedRoundsVector) { v.Census.PeakFD = 33 }},
				{"memory", func(v *ownedRoundsVector) { v.Census.RetainedBytes = 16*1024*1024 + 1 }},
				{"truncated", func(v *ownedRoundsVector) { v.ErrorsTruncated = true }},
				{"missing-resolution", func(v *ownedRoundsVector) { v.Census.Resolutions = v.Census.Resolutions[1:] }},
				{"bad-proof-index", func(v *ownedRoundsVector) { v.Census.Resolutions[0].ProofIndex = 129 }},
				{"early-seal", func(v *ownedRoundsVector) { v.Census.SelectedClosings = [2]int{2, 3}; v.Census.Seal.ScanIndex = 4 }},
				{"wrong-scan-id", func(v *ownedRoundsVector) { v.Census.Scans[n-1].ScanIndex++ }},
				{"root-uids-not-revalidated", func(v *ownedRoundsVector) {
					for i, r := range v.Census.Scans[n-1].Verified {
						if r.DeclaredRoot {
							v.Census.Scans[n-1].Verified[i].UIDsRevalidated = false
							break
						}
					}
				}},
			} {
				t.Run(tc.name, func(t *testing.T) {
					var v ownedRoundsVector
					if err := json.Unmarshal(raw, &v); err != nil {
						t.Fatal(err)
					}
					tc.mutate(&v)
					if check(v) == nil {
						t.Fatal("unsafe real-wire mutation accepted")
					}
				})
			}
		})
	}
}

// Original C faults and SIX genuine kernel proofs are retained; a later triplet
// converged. This fixture is not a whole-host/provider acceptance.
func TestCensusMoreRoundsShortLivedWire(t *testing.T) {
	raw, err := os.ReadFile("testdata/census_shortlived_rounds_13.json")
	if err != nil {
		t.Fatal(err)
	}
	var v ownedRoundsVector
	if err := DecodeStrict(raw, &v); err != nil {
		t.Fatal(err)
	}
	if len(v.Census.Scans) != 13 || len(v.Census.Resolutions) != 3 || len(v.Census.Proofs) != 6 {
		t.Fatal("lost short-lived producer history")
	}
	if err := ValidateCensusV3(v.Census, v.Errors, v.ErrorsTotal, v.ErrorsTruncated, v.DurationMS, v.ControllerPID); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"missing-proof", "missing-resolution", "early-seal"} {
		var c ownedRoundsVector
		if err := json.Unmarshal(raw, &c); err != nil {
			t.Fatal(err)
		}
		switch mode {
		case "missing-proof":
			c.Census.Proofs = c.Census.Proofs[1:]
		case "missing-resolution":
			c.Census.Resolutions = c.Census.Resolutions[1:]
		case "early-seal":
			c.Census.Seal.ScanIndex = 10
			c.Census.SelectedClosings = [2]int{8, 9}
		}
		if ValidateCensusV3(c.Census, c.Errors, c.ErrorsTotal, c.ErrorsTruncated, c.DurationMS, c.ControllerPID) == nil {
			t.Fatalf("%s accepted", mode)
		}
	}
}
