package procobserver

import (
	"encoding/json"
	"os"
	"testing"
)

func chainCompositionVector(t *testing.T) (CensusV3, []EvidenceError, int64, int) {
	t.Helper()
	path := os.Getenv("GC_CHAIN_COMPOSITION_VECTOR")
	if path == "" {
		path = "testdata/protected-chain-f386.json"
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var v struct {
		Census     CensusV3        `json:"census"`
		Errors     []EvidenceError `json:"errors"`
		Duration   int64           `json:"duration_ms"`
		Controller int             `json:"controller_pid"`
	}
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatal(err)
	}
	return v.Census, v.Errors, v.Duration, v.Controller
}

func compositionSibling(c *CensusV3, errors *[]EvidenceError) {
	child := c.Certificates[1].Chain[0]
	child.PID += 100
	child.PGID = child.PID
	child.StartTicks += "1"
	c.Scans[0].Verified = append(c.Scans[0].Verified, child)
	c.Scans[0].EnumeratedCount++
	c.Scans[0].LiveCount++
	absence, retirement := c.Proofs[2], c.Proofs[3]
	absence.PID, retirement.PID = child.PID, child.PID
	s := child.StartTicks
	retirement.StartTicks, retirement.DescendantCertificateID = &s, 3
	c.Proofs = append(c.Proofs, absence, retirement)
	chain := append([]CensusIdentity{child}, c.Certificates[1].Chain[1:]...)
	c.Certificates = append(c.Certificates, DescendantCertificate{ID: 3, PriorScan: 1, Chain: chain, AbsenceProof: 5, RetirementProof: 6})
	x := (*errors)[0]
	x.PID, x.StartTicks = child.PID, nil
	*errors = append(*errors, x)
	c.Resolutions = append(c.Resolutions, CensusResolution{ErrorIndex: len(*errors), Kind: "kernel_absence", ProofIndex: 5})
}

func compositionUnequalPrior(c *CensusV3, errors *[]EvidenceError) {
	first := c.Scans[0]
	second := first
	second.ScanIndex, second.Kind, second.Round, second.OffsetMS = 2, "closing", 1, first.OffsetMS+1
	c.Scans = append([]CensusScanV3{first, second}, c.Scans[1:]...)
	for len(c.Scans) < 7 {
		c.Scans = append(c.Scans, c.Scans[len(c.Scans)-1])
	}
	for i := 2; i < 7; i++ {
		c.Scans[i].ScanIndex, c.Scans[i].OffsetMS = i+1, int64(i+1)
		c.Scans[i].Kind, c.Scans[i].Round = "closing", (i-1)/3+1
		if i%3 == 0 {
			c.Scans[i].Kind = "seal"
		}
	}
	c.SelectedClosings, c.Seal.ScanIndex = [2]int{5, 6}, 7
	c.Certificates[0].PriorScan = 2
	for i := range c.Proofs {
		c.Proofs[i].ScanIndex, c.Proofs[i].OffsetMS = 3, 3
	}
	for i := range *errors {
		(*errors)[i].ScanIndex = 3
	}
}

func TestCensusV3ProtectedChainComposition(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*CensusV3, *[]EvidenceError)
		want   bool
	}{
		{"original independent C witnesses", func(*CensusV3, *[]EvidenceError) {}, true},
		{"siblings reuse verified parent attestation", compositionSibling, true},
		{"independent unequal prior scans", compositionUnequalPrior, true},
		{"unequal prior changed intermediate", func(c *CensusV3, e *[]EvidenceError) {
			compositionUnequalPrior(c, e)
			c.Scans[1].Verified = append([]CensusIdentity(nil), c.Scans[1].Verified...)
			c.Scans[1].Verified[1].Name += "-changed"
		}, false},
		{"different start verified at terminal is not a failed row", func(c *CensusV3, _ *[]EvidenceError) {
			v := c.Certificates[0].Chain[0]
			v.StartTicks += "1"
			v.Classification, v.NoGCEnvironment, v.DeclaredRoot = "nonmanaged", true, false
			v.SessionID, v.City, v.Template, v.InstanceTokenSHA256, v.Epoch = "", "", "", "", 0
			c.Scans[1].Verified = append(c.Scans[1].Verified, v)
			c.Scans[1].LiveCount++
			c.Scans[1].EnumeratedCount++
			c.Scans[1].LiveDigest = "dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"
			s := v.StartTicks
			c.Proofs = append(c.Proofs, CensusProofV3{CensusProof: CensusProof{Kind: "incarnation_retired", Method: "pidfd_exited", PID: v.PID, StartTicks: &s, ScanIndex: 3, OffsetMS: c.Scans[2].OffsetMS}})
		}, false},
		{"parent missing raw fault", func(c *CensusV3, e *[]EvidenceError) {
			*e = (*e)[1:]
			c.Resolutions = c.Resolutions[1:]
			c.Resolutions[0].ErrorIndex = 1
		}, false},
		{"parent unknown proof", func(c *CensusV3, _ *[]EvidenceError) {
			c.Proofs[1].Kind = "unclassified_incarnation_retired"
			c.Proofs[1].Method = "bound_pidfd_exited"
			c.Proofs[1].ProtectedIdentity = false
		}, false},
		{"parent method fabricated", func(c *CensusV3, _ *[]EvidenceError) { c.Proofs[1].Method = "fixture_pidfd_no_pid" }, false},
		{"parent cross scan", func(c *CensusV3, _ *[]EvidenceError) { c.Proofs[0].ScanIndex++; c.Proofs[1].ScanIndex++ }, false},
		{"child suffix different name", func(c *CensusV3, _ *[]EvidenceError) { c.Certificates[1].Chain[1].Name += "-changed" }, false},
		{"child suffix different UID", func(c *CensusV3, _ *[]EvidenceError) { c.Certificates[1].Chain[1].UIDs[0]++ }, false},
		{"child suffix different PGID", func(c *CensusV3, _ *[]EvidenceError) { c.Certificates[1].Chain[1].PGID++ }, false},
		{"root loss", func(c *CensusV3, _ *[]EvidenceError) { c.Scans[len(c.Scans)-1].Verified = []CensusIdentity{} }, false},
		{"root substitution", func(c *CensusV3, _ *[]EvidenceError) { c.Certificates[0].Chain[0].DeclaredRoot = true }, false},
		{"same incarnation returns", func(c *CensusV3, _ *[]EvidenceError) {
			c.Scans[2].Verified = append(c.Scans[2].Verified, c.Certificates[0].Chain[0])
			c.Scans[2].LiveCount++
		}, false},
		{"one raw fault reused", func(c *CensusV3, _ *[]EvidenceError) { c.Resolutions[1].ErrorIndex = c.Resolutions[0].ErrorIndex }, false},
		{"parent proof reused as child resolution", func(c *CensusV3, _ *[]EvidenceError) { c.Resolutions[1].ProofIndex = c.Resolutions[0].ProofIndex }, false},
		{"wrong prior scan", func(c *CensusV3, _ *[]EvidenceError) { c.Certificates[1].PriorScan = 2 }, false},
		{"forward reference", func(c *CensusV3, _ *[]EvidenceError) {
			c.Proofs[0], c.Proofs[2] = c.Proofs[2], c.Proofs[0]
			c.Proofs[1], c.Proofs[3] = c.Proofs[3], c.Proofs[1]
			c.Certificates[0].AbsenceProof, c.Certificates[0].RetirementProof = 3, 4
			c.Certificates[1].AbsenceProof, c.Certificates[1].RetirementProof = 1, 2
			c.Resolutions[0].ProofIndex, c.Resolutions[1].ProofIndex = 3, 1
		}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, e, duration, controller := chainCompositionVector(t)
			tc.mutate(&c, &e)
			if len(c.Scans) == 7 {
				// Explicit synthetic seven-scan context; original C clocks stay intact.
				duration = 8
			}
			err := ValidateCensusV3(c, e, len(e), false, duration, controller)
			if (err == nil) != tc.want {
				t.Fatalf("accepted=%v want=%v: %v", err == nil, tc.want, err)
			}
		})
	}
}
