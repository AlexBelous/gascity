package procobserver

import (
	"os"
	"testing"
)

// Optional owned-kernel C artifact replay. This exercises the actual C census
// codec, NOT the complete privileged helper envelope or provider authority.
func TestCensusV3ProducerOwnedVector(t *testing.T) {
	path := os.Getenv("GC_TEST_CENSUS_VECTOR")
	if path == "" {
		t.Skip("owned C source vector not supplied")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	type vector struct {
		Schema                 string          `json:"schema"`
		Census                 CensusV3        `json:"census"`
		Errors                 []EvidenceError `json:"errors"`
		ErrorsTotal            int             `json:"errors_total"`
		ErrorsTruncated        bool            `json:"errors_truncated"`
		DurationMS             int64           `json:"duration_ms"`
		ControllerPID          int             `json:"controller_pid"`
		CertificateDisposition string          `json:"certificate_disposition"`
	}
	decode := func() vector {
		var v vector
		if err := DecodeStrict(raw, &v); err != nil {
			t.Fatal(err)
		}
		return v
	}
	v := decode()
	if v.Schema != "source-proc-census-vector/v1" || v.CertificateDisposition != "provisional" {
		t.Fatal("wrong source-only artifact")
	}
	validate := func(v vector) error {
		return ValidateCensusV3(v.Census, v.Errors, v.ErrorsTotal, v.ErrorsTruncated, v.DurationMS, v.ControllerPID)
	}
	if err := validate(v); err != nil {
		t.Fatalf("actual C owned census codec rejected: %v", err)
	}
	if len(v.Census.Proofs) != 2 || len(v.Census.Certificates) != 1 || len(v.Errors) != 1 || v.Census.Proofs[0].StartTicks != nil || !v.Census.Proofs[1].ProtectedIdentity {
		t.Fatal("actual NULL/known certificate pair missing")
	}
	cases := []struct {
		name   string
		mutate func(*CensusV3)
	}{
		{"wrong genuine witness PID", func(c *CensusV3) { c.Proofs[0].PID++ }},
		{"known protection dropped", func(c *CensusV3) { c.Proofs[1].ProtectedIdentity = false }},
		{"known certificate reference dropped", func(c *CensusV3) { c.Proofs[1].DescendantCertificateID = 0 }},
		{"later root identity changed", func(c *CensusV3) {
			for i := range c.Scans[2].Verified {
				if c.Scans[2].Verified[i].DeclaredRoot {
					c.Scans[2].Verified[i].UIDs[0]++
				}
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			bad := decode()
			tc.mutate(&bad.Census)
			if validate(bad) == nil {
				t.Fatal("altered C source witness accepted")
			}
		})
	}
}
