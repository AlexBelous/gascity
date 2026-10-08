package procobserver

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func unclassifiedRetirementFixture() (CensusV3, []EvidenceError) {
	c, _ := v3ZombieFixture()
	for i := range c.Scans {
		c.Scans[i].OffsetMS = int64(i * 2)
	}
	c.Scans[1].OffsetMS = 4
	c.Scans[2].OffsetMS = 6
	c.Scans[3].OffsetMS = 8
	start := "300"
	c.Proofs = []CensusProofV3{{CensusProof: CensusProof{Kind: "unclassified_incarnation_retired", Method: "bound_pidfd_exited", PID: 30, StartTicks: &start, ScanIndex: 2, OffsetMS: 2}}}
	c.Resolutions = []CensusResolution{{ErrorIndex: 1, Kind: "unclassified_retirement", ProofIndex: 1}}
	return c, []EvidenceError{{Reason: "process_unavailable", PID: 30, Operation: "environ", Errno: 3, StartTicks: &start, ScanIndex: 2}}
}

func reusedUnclassifiedPID(c *CensusV3, start string, scans ...int) {
	v := c.Scans[0].Verified[0]
	v.Classification, v.NoGCEnvironment, v.DeclaredRoot = "nonmanaged", true, false
	v.PID, v.PGID, v.PPID, v.StartTicks, v.Name = 30, 30, 2, start, "owned-reuse"
	v.SessionID, v.City, v.Template, v.InstanceTokenSHA256, v.Epoch = "", "", "", "", 0
	for _, index := range scans {
		c.Scans[index-1].Verified = append(c.Scans[index-1].Verified, v)
		c.Scans[index-1].LiveCount = 2
		c.Scans[index-1].LiveDigest = strings.Repeat("b", 64)
	}
	if len(c.Scans[3].Verified) == 2 {
		c.Seal.ClassifiedCount, c.ReconciledCount = 2, 2
		c.Seal.ClassifiedDigest, c.ReconciledDigest = strings.Repeat("b", 64), strings.Repeat("b", 64)
	}
}

func TestCensusV3UnclassifiedRetirementIsExact(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*CensusV3, *[]EvidenceError)
		want   bool
	}{
		{"typed during scan", func(*CensusV3, *[]EvidenceError) {}, true},
		{"first scan", func(c *CensusV3, e *[]EvidenceError) {
			c.Proofs[0].ScanIndex, c.Proofs[0].OffsetMS, (*e)[0].ScanIndex = 1, 0, 1
		}, true},
		{"at failed scan end", func(c *CensusV3, _ *[]EvidenceError) { c.Proofs[0].OffsetMS = 4 }, true},
		{"after failed scan end", func(c *CensusV3, _ *[]EvidenceError) { c.Proofs[0].OffsetMS = 5 }, false},
		{"before prior scan end", func(c *CensusV3, e *[]EvidenceError) {
			c.Proofs[0].ScanIndex, c.Proofs[0].OffsetMS, (*e)[0].ScanIndex = 3, 3, 3
		}, false},
		{"wrong errno", func(_ *CensusV3, e *[]EvidenceError) { (*e)[0].Errno = 13 }, false},
		{"wrong operation", func(_ *CensusV3, e *[]EvidenceError) { (*e)[0].Operation = "stat" }, false},
		{"wrong reason", func(_ *CensusV3, e *[]EvidenceError) { (*e)[0].Reason = "uninspected_birth" }, false},
		{"null original start", func(_ *CensusV3, e *[]EvidenceError) { (*e)[0].StartTicks = nil }, false},
		{"null proof start", func(c *CensusV3, _ *[]EvidenceError) { c.Proofs[0].StartTicks = nil }, false},
		{"different proof start", func(c *CensusV3, _ *[]EvidenceError) { s := "301"; c.Proofs[0].StartTicks = &s }, false},
		{"wrong method", func(c *CensusV3, _ *[]EvidenceError) { c.Proofs[0].Method = "pidfd_exited" }, false},
		{"protected", func(c *CensusV3, _ *[]EvidenceError) { c.Proofs[0].ProtectedIdentity = true }, false},
		{"certificate", func(c *CensusV3, _ *[]EvidenceError) { c.Proofs[0].DescendantCertificateID = 1 }, false},
		{"replacement", func(c *CensusV3, _ *[]EvidenceError) { c.Proofs[0].ReplacementStart = "301" }, false},
		{"wrong PID", func(c *CensusV3, _ *[]EvidenceError) { c.Proofs[0].PID = 31 }, false},
		{"caller PID", func(c *CensusV3, e *[]EvidenceError) { c.Proofs[0].PID, (*e)[0].PID = 9, 9 }, false},
		{"different scan", func(_ *CensusV3, e *[]EvidenceError) { (*e)[0].ScanIndex = 1 }, false},
		{"raw history rewritten", func(_ *CensusV3, e *[]EvidenceError) { (*e)[0].ResolvedBy = 1 }, false},
		{"legacy resolution alias", func(c *CensusV3, _ *[]EvidenceError) { c.Resolutions[0].Kind = "kernel_absence" }, false},
		{"classified resolution", func(c *CensusV3, _ *[]EvidenceError) { c.Resolutions[0].ClassifiedScan = 2 }, false},
		{"selected resolution", func(c *CensusV3, _ *[]EvidenceError) { c.Resolutions[0].SelectedSeal = 4 }, false},
		{"one proof reused", func(c *CensusV3, e *[]EvidenceError) {
			*e = append(*e, (*e)[0])
			r := c.Resolutions[0]
			r.ErrorIndex = 2
			c.Resolutions = append(c.Resolutions, r)
		}, false},
		{"unlinked proof", func(c *CensusV3, _ *[]EvidenceError) { p := c.Proofs[0]; p.PID = 31; c.Proofs = append(c.Proofs, p) }, false},
		{"duplicate incarnation across scans", func(c *CensusV3, e *[]EvidenceError) {
			p := c.Proofs[0]
			p.ScanIndex, p.OffsetMS = 3, 5
			c.Proofs = append(c.Proofs, p)
			x := (*e)[0]
			x.ScanIndex = 3
			*e = append(*e, x)
			c.Resolutions = append(c.Resolutions, CensusResolution{ErrorIndex: 2, Kind: "unclassified_retirement", ProofIndex: 2})
		}, false},
		{"same incarnation remains", func(c *CensusV3, _ *[]EvidenceError) { reusedUnclassifiedPID(c, "300", 2, 3, 4) }, false},
		{"old and new kind duplicate incarnation", func(c *CensusV3, _ *[]EvidenceError) {
			p := c.Proofs[0]
			p.Kind, p.Method, p.ScanIndex, p.OffsetMS = "incarnation_retired", "pidfd_exited", 3, 6
			c.Proofs = append(c.Proofs, p)
		}, false},
		{"certificate cannot consume unknown proof", func(c *CensusV3, _ *[]EvidenceError) {
			prior, _ := v3Fixture()
			c.Certificates = prior.Certificates
			c.Certificates[0].RetirementProof = 1
		}, false},
		{"different incarnation survives complete closure", func(c *CensusV3, _ *[]EvidenceError) { reusedUnclassifiedPID(c, "301", 2, 3, 4) }, true},
		{"different incarnation retires independently", func(c *CensusV3, _ *[]EvidenceError) {
			reusedUnclassifiedPID(c, "301", 2)
			s := "301"
			c.Proofs = append(c.Proofs, CensusProofV3{CensusProof: CensusProof{Kind: "incarnation_retired", Method: "pidfd_exited", PID: 30, StartTicks: &s, ScanIndex: 3, OffsetMS: 6}})
		}, true},
		{"unknown proof cannot retire reused incarnation", func(c *CensusV3, _ *[]EvidenceError) { reusedUnclassifiedPID(c, "301", 2) }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, e := unclassifiedRetirementFixture()
			tc.mutate(&c, &e)
			before, _ := json.Marshal(struct {
				C CensusV3
				E []EvidenceError
			}{c, e})
			err := ValidateCensusV3(c, e, len(e), false, 8, 9)
			if (err == nil) != tc.want {
				t.Fatalf("accepted=%v want=%v: %v", err == nil, tc.want, err)
			}
			after, _ := json.Marshal(struct {
				C CensusV3
				E []EvidenceError
			}{c, e})
			if string(before) != string(after) {
				t.Fatal("validation rewrote original history")
			}
		})
	}
	for _, kind := range []string{"managed", "declared_root", "nonmanaged"} {
		t.Run("prior "+kind, func(t *testing.T) {
			c, e := unclassifiedRetirementFixture()
			reusedUnclassifiedPID(&c, "301", 1)
			if kind != "nonmanaged" {
				v := c.Scans[0].Verified[0]
				v.PID, v.PGID, v.StartTicks, v.DeclaredRoot = 30, 30, "301", kind == "declared_root"
				c.Scans[0].Verified[1] = v
			}
			if ValidateCensusV3(c, e, len(e), false, 8, 9) == nil {
				t.Fatal("prior positive identity bypassed")
			}
		})
	}
}

func TestCensusV3UnclassifiedRetirementActualKernelVector(t *testing.T) {
	path := os.Getenv("GC_UNCLASSIFIED_RETIREMENT_VECTOR")
	if path == "" {
		t.Skip("owned original C serializer vector is supplied by integration operator")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var v struct {
		Census        CensusV3        `json:"census"`
		Errors        []EvidenceError `json:"errors"`
		DurationMS    int64           `json:"duration_ms"`
		ControllerPID int             `json:"controller_pid"`
	}
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatal(err)
	}
	if err := ValidateCensusV3(v.Census, v.Errors, len(v.Errors), false, v.DurationMS, v.ControllerPID); err != nil {
		t.Fatal(err)
	}
}
