package procobserver

import "testing"

// Exact source stage: successful stat supplied a known start, status returned
// genuine ESRCH, independently certified descendant while full root continues.
func TestCensusV3StatusRequiresKnownIncarnationAndWitness(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*CensusV3, *[]EvidenceError)
		want   bool
	}{
		{"known status ESRCH", func(*CensusV3, *[]EvidenceError) {}, true},
		{"unobserved status ENOENT", func(_ *CensusV3, e *[]EvidenceError) { (*e)[0].Errno = 2 }, false},
		{"status permission", func(_ *CensusV3, e *[]EvidenceError) { (*e)[0].Errno = 13 }, false},
		{"status NULL start", func(_ *CensusV3, e *[]EvidenceError) { (*e)[0].StartTicks = nil }, false},
		{"bare errno", func(c *CensusV3, _ *[]EvidenceError) { c.Resolutions[0].ProofIndex = 0 }, false},
		{"wrong referenced witness", func(c *CensusV3, _ *[]EvidenceError) { c.Proofs[1].PID++ }, false},
		{"wrong original incarnation", func(_ *CensusV3, e *[]EvidenceError) { s := "301"; (*e)[0].StartTicks = &s }, false},
		{"original NULL instead of known witness", func(c *CensusV3, _ *[]EvidenceError) { c.Resolutions[0].ProofIndex = 1 }, false},
		{"status lost UID proof", func(c *CensusV3, _ *[]EvidenceError) { c.Certificates[0].Chain[0].UIDsRevalidated = false }, false},
		{"status root loss", func(c *CensusV3, _ *[]EvidenceError) { c.Scans[3].Verified = nil }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, e := v3Fixture()
			start := "300"
			e[0].Operation = "status"
			e[0].Errno = 3
			e[0].StartTicks = &start
			c.Resolutions[0].ProofIndex = 2
			tc.mutate(&c, &e)
			err := ValidateCensusV3(c, e, len(e), false, 3, 9)
			if (err == nil) != tc.want {
				t.Fatalf("accepted=%v want=%v: %v", err == nil, tc.want, err)
			}
		})
	}
}
