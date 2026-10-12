package procobserver

import "testing"

func TestCompleteCaptureDiagnosticRequiresCompatibleResolution(t *testing.T) {
	yes, no := true, false
	start := "123"
	e := EvidenceError{
		Reason: "process_unavailable", PID: 42, Operation: "environ", Errno: 3,
		StartTicks: &start, ScanIndex: 2, CaptureDiagnostic: &CaptureDiagnostic{1, &yes, &no, "zero", &no},
	}
	c := CensusV3{Proofs: []CensusProofV3{{CensusProof: CensusProof{
		Kind: "incarnation_retired", Method: "pidfd_no_pid", PID: 42,
		StartTicks: &start, ScanIndex: 3,
	}}}, Resolutions: []CensusResolution{{ErrorIndex: 1, Kind: "kernel_absence", ProofIndex: 1}}}
	if !validCompleteCaptureDiagnosticLinks(c, []EvidenceError{e}) {
		t.Fatal("original zero/false annotation lost despite compatible independently validated proof link")
	}
	if e.CaptureDiagnostic.ExitPoll != "zero" || *e.CaptureDiagnostic.ExitWitness {
		t.Fatal("original failed-read annotation was regraded")
	}
	for _, kind := range []string{"fresh_census", "fresh_classification", "retired_positive_birth", "unclassified_retirement"} {
		bad := c
		bad.Resolutions = append([]CensusResolution(nil), c.Resolutions...)
		bad.Resolutions[0].Kind = kind
		if validCompleteCaptureDiagnosticLinks(bad, []EvidenceError{e}) {
			t.Fatalf("incompatible resolution accepted: %s", kind)
		}
	}
	bad := c
	bad.Proofs = append([]CensusProofV3(nil), c.Proofs...)
	bad.Proofs[0].PID++
	if validCompleteCaptureDiagnosticLinks(bad, []EvidenceError{e}) {
		t.Fatal("wrong PID accepted")
	}
	bad = c
	bad.Resolutions = nil
	if validCompleteCaptureDiagnosticLinks(bad, []EvidenceError{e}) {
		t.Fatal("annotation alone became eligibility")
	}
	bad = c
	bad.Resolutions = append(append([]CensusResolution(nil), c.Resolutions...), c.Resolutions[0])
	if validCompleteCaptureDiagnosticLinks(bad, []EvidenceError{e}) {
		t.Fatal("duplicate proof use accepted")
	}
}
