package procobserver

import (
	"os"
	"testing"
)

func commBirthFixture() (CensusV3, []EvidenceError) {
	c, e := birthConservationFixture()
	root := c.Scans[3].Verified[0]
	for i := 3; i < 7; i++ {
		born := &c.Scans[i].Verified[1]
		born.PPID = root.PID
		born.PGID = root.PGID
		born.DeclaredRoot = false
		born.Name = "after-exec"
	}
	c.Scans[3].Verified[1].Name = "before-exec"
	start := "400"
	e[1].StartTicks = &start
	return c, e
}

func TestCensusV3ManagedBirthCommRefresh(t *testing.T) {
	c, e := commBirthFixture()
	if err := ValidateCensusV3(c, e, len(e), false, 7, 9); err != nil {
		t.Fatalf("same managed descendant incarnation with three stable fresh reads: %v", err)
	}
	cases := []struct {
		name   string
		change func(*CensusV3)
	}{
		{"changed original PID", func(c *CensusV3) { c.Scans[3].Verified[1].PID++ }},
		{"changed start", func(c *CensusV3) { c.Scans[3].Verified[1].StartTicks = "399" }},
		{"changed parent", func(c *CensusV3) { c.Scans[3].Verified[1].PPID++ }},
		{"changed process group", func(c *CensusV3) { c.Scans[3].Verified[1].PGID++ }},
		{"changed UID", func(c *CensusV3) { c.Scans[3].Verified[1].UIDs[0]++ }},
		{"changed SID", func(c *CensusV3) { c.Scans[3].Verified[1].SessionID = "other" }},
		{"changed city", func(c *CensusV3) { c.Scans[3].Verified[1].City = "/other" }},
		{"changed template", func(c *CensusV3) { c.Scans[3].Verified[1].Template = "other" }},
		{"changed epoch", func(c *CensusV3) { c.Scans[3].Verified[1].Epoch++ }},
		{"changed token", func(c *CensusV3) {
			c.Scans[3].Verified[1].InstanceTokenSHA256 = c.Scans[3].Verified[0].InstanceTokenSHA256[:63] + "f"
		}},
		{"original declared root", func(c *CensusV3) { c.Scans[3].Verified[1].DeclaredRoot = true }},
		{"original infrastructure", func(c *CensusV3) { c.Scans[3].Verified[1].Name = "tmux: server" }},
		{"fresh name not stable", func(c *CensusV3) { c.Scans[5].Verified[1].Name = "still-changing" }},
		{"original root changed", func(c *CensusV3) { c.Scans[3].Verified[0].StartTicks = "201" }},
		{"ancestor omitted", func(c *CensusV3) { c.Scans[3].Verified = c.Scans[3].Verified[1:] }},
		{"ancestor name changed", func(c *CensusV3) { c.Scans[3].Verified[0].Name = "other-root" }},
		{"ancestry cycle", func(c *CensusV3) {
			for i := 3; i < 7; i++ {
				c.Scans[i].Verified[1].PPID = c.Scans[i].Verified[1].PID
			}
		}},
		{"ancestor tuple differs", func(c *CensusV3) { c.Scans[3].Verified[0].SessionID = "other" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, e := commBirthFixture()
			tc.change(&c)
			if ValidateCensusV3(c, e, len(e), false, 7, 9) == nil {
				t.Fatal("mutable comm hid changed ownership/incarnation/root or unstable fresh reads")
			}
		})
	}
}

func TestCensusV3ManagedBirthCommOwnedVector(t *testing.T) {
	path := os.Getenv("GC_COMM_BIRTH_VECTOR")
	if path == "" {
		t.Skip("owned C source vector not supplied")
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
	if err := DecodeStrict(raw, &v); err != nil {
		t.Fatal(err)
	}
	if err := ValidateCensusV3(v.Census, v.Errors, len(v.Errors), false, v.DurationMS, v.ControllerPID); err != nil {
		t.Fatalf("real C mutable-comm birth wire rejected: %v", err)
	}
	if len(v.Errors) != 1 || v.Errors[0].Reason != "uninspected_birth" || len(v.Census.Proofs) != 0 || len(v.Census.Certificates) != 0 {
		t.Fatal("original positive birth was replaced with a retirement proof")
	}
}
