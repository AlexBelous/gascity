package procobserver

import "testing"

// Modeled original nullable errno22 is NOT a claimed reproduction of the
// historical PID. Only independently indexed existing kernel-proof semantics
// may resolve it, with original raw fields and every certificate fence intact.
func TestCensusV3NullablePIDFDEINVALRequiresSeparateWitness(t *testing.T) {
	for _, row := range []struct {
		name   string
		mutate func(*CensusV3, *[]EvidenceError)
		known  bool
	}{
		{"indexed genuine witness", func(*CensusV3, *[]EvidenceError) {}, true},
		{"bare errno", func(c *CensusV3, _ *[]EvidenceError) { c.Resolutions = nil }, false},
		{"no witness", func(c *CensusV3, _ *[]EvidenceError) { c.Resolutions[0].ProofIndex = 0 }, false},
		{"fixture witness", func(c *CensusV3, _ *[]EvidenceError) { c.Proofs[0].Method = "fixture_pidfd_no_pid" }, false},
		{"wrong PID", func(c *CensusV3, _ *[]EvidenceError) { c.Proofs[0].PID++ }, false},
		{"wrong index", func(c *CensusV3, _ *[]EvidenceError) { c.Resolutions[0].ErrorIndex = 2 }, false},
		{"known original start", func(_ *CensusV3, e *[]EvidenceError) { s := "300"; (*e)[0].StartTicks = &s }, false},
		{"permission error", func(_ *CensusV3, e *[]EvidenceError) { (*e)[0].Errno = 13 }, false},
		{"lost declared root", func(c *CensusV3, _ *[]EvidenceError) { c.Scans[3].Verified = nil }, false},
		{"wrong knownbirth", func(c *CensusV3, _ *[]EvidenceError) { *c.Proofs[1].StartTicks = "301" }, false},
		{"wrong protection", func(c *CensusV3, _ *[]EvidenceError) { c.Proofs[1].ProtectedIdentity = false }, false},
		{"wrong later root", func(c *CensusV3, _ *[]EvidenceError) { c.Scans[3].Verified[0].StartTicks = "201" }, false},
	} {
		t.Run(row.name, func(t *testing.T) {
			c, e := v3Fixture()
			e[0].Operation = "pidfd_open"
			e[0].Errno = 22
			row.mutate(&c, &e)
			err := ValidateCensusV3(c, e, len(e), false, 3, 9)
			if (err == nil) != row.known {
				t.Fatalf("nullable original22 result accepted=%t want=%t", err == nil, row.known)
			}
			if e[0].Errno == 22 && (e[0].Operation != "pidfd_open" || e[0].ResolvedBy != 0 || e[0].ScanIndex != 3) {
				t.Fatal("original fault changed during validation")
			}
		})
	}
}
