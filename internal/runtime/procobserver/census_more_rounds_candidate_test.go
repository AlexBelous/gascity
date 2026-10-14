package procobserver

import "testing"

// Pure decoder contract only; the owned C fixture supplies kernel/source proof.
func moreRoundsCandidate(n int) CensusV3 {
	c, _ := v3Fixture()
	row := c.Scans[3].Verified[0]
	scan := c.Scans[3]
	c.Scans = nil
	for i := 0; i < n; i++ {
		s := scan
		s.ScanIndex = i + 1
		s.OffsetMS = int64(i)
		s.Kind, s.Round = "initial", 0
		if i > 0 {
			s.Kind, s.Round = "closing", (i-1)/3+1
			if i%3 == 0 {
				s.Kind = "seal"
			}
		}
		s.Verified = []CensusIdentity{row}
		c.Scans = append(c.Scans, s)
	}
	c.SelectedClosings = [2]int{n - 2, n - 1}
	c.Seal.ScanIndex = n
	c.Proofs = []CensusProofV3{}
	c.Certificates = []DescendantCertificate{}
	c.Resolutions = []CensusResolution{}
	return c
}

func TestCensusMoreRoundsCandidate(t *testing.T) {
	for _, n := range []int{4, 7, 10, 13, 31} {
		if err := ValidateCensusV3(moreRoundsCandidate(n), []EvidenceError{}, 0, false, 100, 9); err != nil {
			t.Fatalf("scans=%d: %v", n, err)
		}
	}
	for _, n := range []int{3, 11, 32, 34} {
		if ValidateCensusV3(moreRoundsCandidate(n), []EvidenceError{}, 0, false, 100, 9) == nil {
			t.Fatalf("bad scans=%d accepted", n)
		}
	}
	for _, tc := range []struct {
		name     string
		mutate   func(*CensusV3)
		duration int64
	}{
		{"deadline", func(*CensusV3) {}, 10000},
		{"fd33", func(c *CensusV3) { c.PeakFD = 33 }, 100},
		{"memory", func(c *CensusV3) { c.RetainedBytes = 16*1024*1024 + 1 }, 100},
		{"earlier_seal", func(c *CensusV3) { c.Seal.ScanIndex = 4; c.SelectedClosings = [2]int{2, 3} }, 100},
		{"missing_row", func(c *CensusV3) { c.Scans[30].Verified = nil }, 100},
		{"uids_not_revalidated", func(c *CensusV3) {
			c.Scans[30].Verified = []CensusIdentity{c.Scans[30].Verified[0]}
			c.Scans[30].Verified[0].UIDsRevalidated = false
		}, 100},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := moreRoundsCandidate(31)
			tc.mutate(&c)
			if ValidateCensusV3(c, []EvidenceError{}, 0, false, tc.duration, 9) == nil {
				t.Fatal("unsafe extended case accepted")
			}
		})
	}
}
