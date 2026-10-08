package procobserver

import (
	"os"
	"testing"
)

func TestCensusV3ProducerNullableEINVALVector(t *testing.T) {
	path := os.Getenv("GC_TEST_NULL_EINVAL_VECTOR")
	if path == "" {
		t.Skip("new producer-owned original codec vector not supplied")
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
	var v vector
	if DecodeStrict(raw, &v) != nil || v.Schema != "source-proc-census-vector/v1" || v.CertificateDisposition != "provisional" {
		t.Fatal("wrong producer-only artifact")
	}
	if len(v.Errors) != 1 || v.Errors[0].Errno != 22 || v.Errors[0].Operation != "pidfd_open" || v.Errors[0].StartTicks != nil || v.Errors[0].ResolvedBy != 0 || len(v.Census.Proofs) != 1 || v.Census.Proofs[0].Method != "pidfd_no_pid" || len(v.Census.Resolutions) != 1 {
		t.Fatal("original NULL22/separate witness ledger not conserved")
	}
	if err := ValidateCensusV3(v.Census, v.Errors, v.ErrorsTotal, v.ErrorsTruncated, v.DurationMS, v.ControllerPID); err != nil {
		t.Fatalf("original producer codec rejected: %v", err)
	}
	v.Census.Resolutions = nil
	if ValidateCensusV3(v.Census, v.Errors, v.ErrorsTotal, v.ErrorsTruncated, v.DurationMS, v.ControllerPID) == nil {
		t.Fatal("bare originalEINVAL accepted without its indexed witness")
	}
}
