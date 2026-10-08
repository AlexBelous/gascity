package observation

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/runtime"
	"github.com/gastownhall/gascity/internal/runtime/procobserver"
	"github.com/gastownhall/gascity/internal/runtime/proctable"
)

// Optional artifact generator. Production ordinary join executes with the unit
// provider; ALL wire metadata stays fixture-only, never a supported live proof.
func TestGenerateV3OrdinaryJoinGolden(t *testing.T) {
	dir := os.Getenv("GC_TEST_V3_VECTOR_DIR")
	if dir == "" {
		t.Skip("explicit fixture artifact directory required")
	}
	var v struct {
		Census                 procobserver.CensusV3        `json:"census"`
		Errors                 []procobserver.EvidenceError `json:"errors"`
		DurationMS             int64                        `json:"duration_ms"`
		ControllerPID          int                          `json:"controller_pid"`
		CertificateDisposition string                       `json:"certificate_disposition"`
	}
	data, err := os.ReadFile(filepath.Join(dir, "provisional-census-v3.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := procobserver.DecodeStrict(data, &v); err != nil {
		t.Fatal(err)
	}
	if err := procobserver.ValidateCensusV3(v.Census, v.Errors, len(v.Errors), false, v.DurationMS, v.ControllerPID); err != nil {
		t.Fatal(err)
	}
	anchor := v.Census.Certificates[0].Chain[1]
	rt := runtime.LiveRuntime{PID: anchor.PID, PPID: anchor.PPID, SessionID: anchor.SessionID, City: anchor.City, Epoch: anchor.Epoch, ProviderName: "fixture-v3-root", IsTracked: true}
	root := proctable.ObservedRoot{Runtime: rt, Template: anchor.Template, StartIdentity: anchor.StartTicks, InstanceTokenSHA256: anchor.InstanceTokenSHA256}
	p := &observedProvider{Fake: runtime.NewFake(), names: []string{"fixture-v3-root"}, roots: []runtime.LiveRuntime{rt}, capable: true}
	for key, value := range map[string]string{"GC_SESSION_ID": anchor.SessionID, "GC_TEMPLATE": anchor.Template, "GC_RUNTIME_EPOCH": strconv.Itoa(anchor.Epoch), "GC_INSTANCE_TOKEN": "private-v3-fixture-token"} {
		if err := p.SetMeta("fixture-v3-root", key, value); err != nil {
			t.Fatal(err)
		}
	}
	if TokenDigest("private-v3-fixture-token") != anchor.InstanceTokenSHA256 {
		t.Fatal("golden token is not actual unit-provider join input")
	}
	ep := &evidenceProvider{observedProvider: p}
	now := time.Date(2026, 10, 4, 23, 0, 0, 0, time.UTC)
	var first procobserver.ResponseV3
	data, err = os.ReadFile(filepath.Join(dir, "synthetic-provisional-host-v3.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := procobserver.DecodeStrict(data, &first); err != nil {
		t.Fatal(err)
	}
	first.StartedAt = now.Add(-6 * time.Millisecond)
	first.FinishedAt = now.Add(-3 * time.Millisecond)
	first.Roots = []procobserver.Root{{PID: anchor.PID, PPID: anchor.PPID, PGID: anchor.PGID, StartTicks: anchor.StartTicks, SessionID: anchor.SessionID, City: anchor.City, Template: anchor.Template, Epoch: anchor.Epoch, InstanceTokenSHA256: anchor.InstanceTokenSHA256, Name: anchor.Name}}
	first.EnumeratedCountBefore = 2
	first.EnumeratedCountAfter = 2
	first.EnumerationDigestBefore = v.Census.Scans[0].EnumerationDigest
	first.EnumerationDigestAfter = v.Census.Scans[2].EnumerationDigest
	second := first
	second.StartedAt = first.FinishedAt
	second.FinishedAt = now
	// Frame2 is an independent fresh root-only classification: it does NOT
	// resurrect the already retired child or splice a first-frame certificate.
	raw, _ := json.Marshal(v.Census)
	var independent procobserver.CensusV3
	if err := json.Unmarshal(raw, &independent); err != nil {
		t.Fatal(err)
	}
	second.Census = independent
	for i := range second.Census.Scans {
		s := &second.Census.Scans[i]
		s.EnumeratedCount = 1
		s.LiveCount = 1
		s.LiveDigest = second.Census.ReconciledDigest
		s.Verified = []procobserver.CensusIdentity{anchor}
	}
	second.Census.Proofs = []procobserver.CensusProofV3{}
	second.Census.Certificates = []procobserver.DescendantCertificate{}
	second.Census.Resolutions = []procobserver.CensusResolution{}
	second.Errors = []procobserver.EvidenceError{}
	second.ErrorsTotal = 0
	second.EnumeratedCountBefore = 1
	second.EnumeratedCountAfter = 1
	if err := procobserver.ValidateCensusV3(second.Census, second.Errors, 0, false, 3, 9); err != nil {
		t.Fatal(err)
	}
	frames := []procobserver.ResponseV3{first, second}
	calls := 0
	for i, frame := range frames {
		if err := procobserver.ValidateCensusV3(frame.Census, frame.Errors, frame.ErrorsTotal, frame.ErrorsTruncated, frame.DurationMS, 9); err != nil {
			t.Fatalf("frame%d altered before join: %v", i+1, err)
		}
	}
	got := ObserveProcessEvidence("/city", ep, func() ProcessEvidence {
		frame := frames[calls]
		calls++
		anchors := []proctable.ObservedRoot{}
		if len(frame.Census.Certificates) > 0 {
			anchors = append(anchors, root)
		}
		return ProcessEvidence{Roots: []proctable.ObservedRoot{root}, StartedAt: frame.StartedAt, FinishedAt: frame.FinishedAt, CensusContract: procobserver.CensusV3Schema, RetirementAnchors: anchors}
	}, func() time.Time { return now })
	if calls != 2 || ep.tracks != 2 || !got.ProviderComplete || !got.ProcessComplete || got.CertificateDisposition != "provider_verified" {
		t.Fatalf("ordinary join failed %+v", got)
	}
	golden := struct {
		Observation
		SourceRevision          string                    `json:"source_revision"`
		ControllerBinding       string                    `json:"controller_binding"`
		ControllerPID           int                       `json:"controller_pid"`
		ControllerBinarySHA256  string                    `json:"controller_binary_sha256"`
		ControllerStartIdentity string                    `json:"controller_start_identity"`
		ControllerBootID        string                    `json:"controller_boot_id"`
		HelperBinarySHA256      string                    `json:"helper_binary_sha256"`
		HelperPolicyDigest      string                    `json:"helper_policy_digest"`
		ProcessDiagnostics      []procobserver.ResponseV3 `json:"process_diagnostics"`
	}{got, first.HelperSourceRevision, "fixture_unit_provider_join", 9, first.CallerBinding.ControllerBinarySHA256, "0", first.BootID, first.HelperBinarySHA256, first.PolicyDigest, frames}
	raw, err = json.MarshalIndent(golden, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	var serialized struct {
		ProcessDiagnostics []procobserver.ResponseV3 `json:"process_diagnostics"`
	}
	if err := json.Unmarshal(raw, &serialized); err != nil {
		t.Fatal(err)
	}
	if len(serialized.ProcessDiagnostics) != 2 {
		t.Fatal("lost independent frame")
	}
	for i, frame := range serialized.ProcessDiagnostics {
		if err := procobserver.ValidateCensusV3(frame.Census, frame.Errors, frame.ErrorsTotal, frame.ErrorsTruncated, frame.DurationMS, 9); err != nil {
			t.Fatalf("serialized frame%d invalid: %v", i+1, err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "synthetic-ordinary-final-join-v3.json"), append(raw, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
}
