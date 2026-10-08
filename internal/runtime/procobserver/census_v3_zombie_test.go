package procobserver

import (
	"encoding/json"
	"os"
	"testing"
)

func v3ZombieFixture() (CensusV3, []EvidenceError) {
	c, _ := v3Fixture()
	for i := range c.Scans {
		c.Scans[i].EnumeratedCount = 2
		c.Scans[i].LiveCount = 1
		c.Scans[i].LiveDigest = c.ReconciledDigest
		c.Scans[i].Verified = c.Scans[i].Verified[:1]
	}
	c.Seal.EnumeratedCount = 2
	c.Certificates = []DescendantCertificate{}
	start := "300"
	c.Proofs = make([]CensusProofV3, 4)
	c.Resolutions = make([]CensusResolution, 4)
	errors := make([]EvidenceError, 4)
	for i := range c.Proofs {
		c.Proofs[i] = CensusProofV3{CensusProof: CensusProof{
			Kind: "terminal_zombie", Method: "pidfd_zombie_stat", PID: 30,
			StartTicks: &start, ScanIndex: i + 1, OffsetMS: c.Scans[i].OffsetMS,
		}}
		c.Resolutions[i] = CensusResolution{ErrorIndex: i + 1, Kind: "kernel_absence", ProofIndex: i + 1}
		errors[i] = EvidenceError{Reason: "process_unavailable", PID: 30, Operation: "pidfd_poll", Errno: 116, ScanIndex: i + 1}
	}
	return c, errors
}

func TestCensusV3TerminalZombieProofIsExact(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*CensusV3, *[]EvidenceError)
		want   bool
	}{
		{"typed terminal witness", func(*CensusV3, *[]EvidenceError) {}, true},
		{"bare ESTALE", func(c *CensusV3, _ *[]EvidenceError) { c.Resolutions[0].ProofIndex = 0 }, false},
		{"wrong poll errno", func(_ *CensusV3, e *[]EvidenceError) { (*e)[0].Errno = 5 }, false},
		{"wrong operation", func(_ *CensusV3, e *[]EvidenceError) { (*e)[0].Operation = "pidfd_open" }, false},
		{"missing start", func(c *CensusV3, _ *[]EvidenceError) { c.Proofs[0].StartTicks = nil }, false},
		{"wrong method", func(c *CensusV3, _ *[]EvidenceError) { c.Proofs[0].Method = "pidfd_no_pid" }, false},
		{"protected", func(c *CensusV3, _ *[]EvidenceError) { c.Proofs[0].ProtectedIdentity = true }, false},
		{"wrong PID", func(c *CensusV3, _ *[]EvidenceError) { c.Proofs[0].PID++ }, false},
		{"wrong scan", func(c *CensusV3, _ *[]EvidenceError) { c.Proofs[0].ScanIndex = 2 }, false},
		{"wrong start in raw", func(_ *CensusV3, e *[]EvidenceError) { s := "300"; (*e)[0].StartTicks = &s }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, errors := v3ZombieFixture()
			tc.mutate(&c, &errors)
			err := ValidateCensusV3(c, errors, len(errors), false, 3, 9)
			if (err == nil) != tc.want {
				t.Fatalf("accepted=%v want=%v: %v", err == nil, tc.want, err)
			}
		})
	}
}

// The optional vector is emitted by the owned Linux kernel test's actual C
// serializer. It is not a host observation or provider authority.
func TestCensusV3TerminalZombieActualKernelVector(t *testing.T) {
	path := os.Getenv("GC_ZOMBIE_VECTOR")
	if path == "" {
		t.Skip("set GC_ZOMBIE_VECTOR to the owned kernel test output")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var vector struct {
		Census        CensusV3        `json:"census"`
		Errors        []EvidenceError `json:"errors"`
		DurationMS    int64           `json:"duration_ms"`
		ControllerPID int             `json:"controller_pid"`
	}
	if err := json.Unmarshal(raw, &vector); err != nil {
		t.Fatal(err)
	}
	if err := ValidateCensusV3(vector.Census, vector.Errors, len(vector.Errors), false, vector.DurationMS, vector.ControllerPID); err != nil {
		t.Fatal(err)
	}
}
