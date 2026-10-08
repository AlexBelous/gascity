package procobserver

import (
	"os"
	"path/filepath"
	"testing"
)

// Replay unmodified full envelopes emitted by the owned C fixture. These are
// fixture_procfs evidence, never host coverage or provider authority.
func TestCensusV3ProducerFullEnvelope(t *testing.T) {
	dir := os.Getenv("GC_TEST_ENVELOPE_DIR")
	if dir == "" {
		t.Skip("owned C full-envelope artifact not supplied")
	}
	for _, name := range []string{"vector.json.envelope.json", "vector.json.unknown-envelope.json"} {
		t.Run(name, func(t *testing.T) {
			raw, err := os.ReadFile(filepath.Join(dir, name))
			if err != nil {
				t.Fatal(err)
			}
			var r ResponseV3
			if err := DecodeStrict(raw, &r); err != nil {
				t.Fatalf("actual C full envelope fails frozen Go wire grammar: %v", err)
			}
			if r.Schema != ResponseSchemaV3 || r.Scope != "fixture_procfs" || r.CertificateDisposition != "provisional" {
				t.Fatal("source fixture authority or schema changed")
			}
			p := ReleasePolicyV3{Policy: Policy{
				HelperUID: 1000, SocketPath: "/fixture-source-only/observer.sock",
				HelperSourceRevision: r.HelperSourceRevision, HelperBinarySHA256: r.HelperBinarySHA256,
				PolicyDigest: r.PolicyDigest, BootID: r.BootID, PIDNamespaceIdentity: r.PIDNamespaceIdentity,
				CallerBinding: r.CallerBinding,
			}, EvidenceSchema: ResponseSchemaV3}
			// Exact synthetic pins and the fixture's own time avoid conflating
			// fixture-scope rejection with stale wall clock or a pin mismatch.
			if err := validatePolicyV3(p); err != nil {
				t.Fatal(err)
			}
			if _, err := DecodeResponseV3(raw, p, r.RequestNonce, r.FinishedAt); err == nil || err.Error() != "observer v3 response binding mismatch" {
				t.Fatalf("fixture admitted by production reader or rejected at wrong gate: %v", err)
			}
			if len(r.Census.Certificates) != 1 || len(r.Census.Proofs) != 2 || len(r.Census.Resolutions) != 1 || len(r.Errors) == 0 {
				t.Fatal("C envelope dropped original provisional certificate/diagnostic prefix")
			}
			if name == "vector.json.envelope.json" {
				if !r.Complete || ValidateCensusV3(r.Census, r.Errors, r.ErrorsTotal, r.ErrorsTruncated, r.DurationMS, r.CallerBinding.PID) != nil {
					t.Fatal("actual C positive source census invalid")
				}
				seal := r.Census.Scans[len(r.Census.Scans)-1]
				declared := make(map[int]CensusIdentity)
				for _, v := range seal.Verified {
					if v.Classification == "managed" && v.DeclaredRoot {
						declared[v.PID] = v
					}
				}
				if len(declared) != len(r.Roots) {
					t.Fatal("C projection omitted sealed declared root")
				}
				for _, root := range r.Roots {
					if !censusIdentityMatchesRoot(declared[root.PID], root) {
						t.Fatal("C projection changed sealed root identity")
					}
				}
			} else if r.Complete || len(r.Roots) != 0 || r.Census.SelectedClosings != [2]int{} || r.Census.Seal.ScanIndex != 0 {
				t.Fatal("actual later-root-loss UNKNOWN selected stale coverage")
			}
		})
	}
}
