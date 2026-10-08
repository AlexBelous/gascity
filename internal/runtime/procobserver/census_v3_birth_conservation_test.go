package procobserver

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func birthConservationFixture() (CensusV3, []EvidenceError) {
	c, e := v3Fixture()
	d := strings.Repeat("d", 64)
	root := c.Scans[3].Verified[0]
	born := root
	born.PID = 40
	born.PPID = 2
	born.PGID = 40
	born.StartTicks = "400"
	born.Name = "new-root"
	born.DeclaredRoot = true
	c.Scans[3].Verified = append(c.Scans[3].Verified, born)
	c.Scans[3].EnumeratedCount = 2
	c.Scans[3].LiveCount = 2
	for i := 5; i <= 7; i++ {
		kind := "closing"
		if i == 7 {
			kind = "seal"
		}
		c.Scans = append(c.Scans, CensusScanV3{CensusClosing: CensusClosing{i, 2, d, 2, d}, Round: 2, Kind: kind, OffsetMS: int64(i), Verified: []CensusIdentity{root, born}})
	}
	c.SelectedClosings = [2]int{5, 6}
	c.Seal = CensusSeal{7, 2, d, 2, d}
	c.ReconciledCount = 2
	c.ReconciledDigest = d
	e = append(e, EvidenceError{Reason: "uninspected_birth", Operation: "census", PID: 40, ScanIndex: 4})
	c.Resolutions = append(c.Resolutions, CensusResolution{ErrorIndex: 2, Kind: "fresh_classification", ClassifiedScan: 5, SelectedSeal: 7})
	return c, e
}

func TestCensusV3OriginalPositiveBirthIsConserved(t *testing.T) {
	c, e := birthConservationFixture()
	if err := ValidateCensusV3(c, e, len(e), false, 7, 9); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name   string
		mutate func(*CensusIdentity)
	}{
		{"original root incarnation lost", func(v *CensusIdentity) { v.StartTicks = "399" }},
		{"original root SID lost", func(v *CensusIdentity) { v.SessionID = "old-sid" }},
		{"original root UID changed", func(v *CensusIdentity) { v.UIDs[0]++ }},
		{"original root protection changed", func(v *CensusIdentity) { v.DeclaredRoot = false }},
		{"original parent changed", func(v *CensusIdentity) { v.PPID = 3 }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mutated, errors := birthConservationFixture()
			tc.mutate(&mutated.Scans[3].Verified[1])
			if ValidateCensusV3(mutated, errors, len(errors), false, 7, 9) == nil {
				t.Fatal("fresh pair forgot original positive birth identity/protection")
			}
		})
	}
	t.Run("known birth original identity omitted", func(t *testing.T) {
		mutated, errors := birthConservationFixture()
		start := "400"
		errors[1].StartTicks = &start
		mutated.Scans[3].Verified = mutated.Scans[3].Verified[:1]
		if ValidateCensusV3(mutated, errors, len(errors), false, 7, 9) == nil {
			t.Fatal("known birth lost original full identity")
		}
	})
	if dir := os.Getenv("GC_TEST_V3_VECTOR_DIR"); dir != "" {
		type vector struct {
			Name          string          `json:"name"`
			Census        CensusV3        `json:"census"`
			Errors        []EvidenceError `json:"errors"`
			DurationMS    int64           `json:"duration_ms"`
			ControllerPID int             `json:"controller_pid"`
			Want          bool            `json:"want"`
		}
		vectors := []vector{{"same original positive root", c, e, 7, 9, true}}
		for _, tc := range cases {
			mutated, errors := birthConservationFixture()
			tc.mutate(&mutated.Scans[3].Verified[1])
			vectors = append(vectors, vector{tc.name, mutated, errors, 7, 9, false})
		}
		missing, missingErrors := birthConservationFixture()
		start := "400"
		missingErrors[1].StartTicks = &start
		missing.Scans[3].Verified = missing.Scans[3].Verified[:1]
		vectors = append(vectors, vector{"known birth original identity omitted", missing, missingErrors, 7, 9, false})
		data, err := json.MarshalIndent(vectors, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "source-original-birth-conservation-vectors.json"), append(data, '\n'), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}
