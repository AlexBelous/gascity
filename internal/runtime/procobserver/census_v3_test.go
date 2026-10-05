package procobserver

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func v3Fixture() (CensusV3, []EvidenceError) {
	d := strings.Repeat("a", 64)
	tokenHash := sha256.Sum256([]byte("private-v3-fixture-token"))
	root := CensusIdentity{Classification: "managed", EnvironmentRevalidated: true, PID: 20, PPID: 2, PGID: 20, UIDs: [4]uint32{1000, 1000, 1000, 1000}, StartTicks: "200", SessionID: "sid", City: "/city", Template: "worker", Epoch: 1, InstanceTokenSHA256: hex.EncodeToString(tokenHash[:]), Name: "gc", DeclaredRoot: true, StatRevalidated: true, PIDFDBound: true, UIDsRevalidated: true}
	child := root
	child.PID = 30
	child.PPID = 20
	child.PGID = 30
	child.StartTicks = "300"
	child.Name = "child"
	child.DeclaredRoot = false
	childStart := "300"
	c := CensusV3{
		Schema: CensusV3Schema, SelectedClosings: [2]int{2, 3}, Seal: CensusSeal{ScanIndex: 4, EnumeratedCount: 1, PIDDigest: d, ClassifiedCount: 1, ClassifiedDigest: d}, ReconciledCount: 1, ReconciledDigest: d, PeakFD: 32, RetainedBytes: 4096,
		Scans: []CensusScanV3{
			{CensusClosing: CensusClosing{1, 2, d, 2, strings.Repeat("b", 64)}, Round: 0, Kind: "initial", OffsetMS: 0, Verified: []CensusIdentity{root, child}},
			{CensusClosing: CensusClosing{2, 2, d, 2, strings.Repeat("b", 64)}, Round: 1, Kind: "closing", OffsetMS: 1, Verified: []CensusIdentity{root, child}},
			{CensusClosing: CensusClosing{3, 2, d, 1, d}, Round: 1, Kind: "closing", OffsetMS: 2, Verified: []CensusIdentity{root}},
			{CensusClosing: CensusClosing{4, 1, d, 1, d}, Round: 1, Kind: "seal", OffsetMS: 3, Verified: []CensusIdentity{root}},
		},
		Proofs: []CensusProofV3{
			{CensusProof: CensusProof{Kind: "enumerated_pid_absent", Method: "pidfd_no_pid", PID: 30, ScanIndex: 3, OffsetMS: 2}},
			{CensusProof: CensusProof{Kind: "incarnation_retired", Method: "pidfd_no_pid", PID: 30, StartTicks: &childStart, ScanIndex: 3, OffsetMS: 2, ProtectedIdentity: true}, DescendantCertificateID: 1},
		},
		Certificates: []DescendantCertificate{{ID: 1, PriorScan: 2, Chain: []CensusIdentity{child, root}, AbsenceProof: 1, RetirementProof: 2}},
		Resolutions:  []CensusResolution{{ErrorIndex: 1, Kind: "kernel_absence", ProofIndex: 1}},
	}
	return c, []EvidenceError{{Reason: "process_unavailable", PID: 30, Operation: "stat", Errno: 2, ScanIndex: 3}}
}

func TestCensusV3EarlyPIDFDRequiresExactKernelWitness(t *testing.T) {
	for _, tc := range []struct {
		name   string
		op     string
		errno  int
		mutate func(*CensusV3)
		want   bool
	}{
		{"early pidfd no PID", "pidfd_open", 3, func(*CensusV3) {}, true},
		{"early pidfd permission", "pidfd_open", 13, func(*CensusV3) {}, false},
		{"early pidfd wrong errno", "pidfd_open", 2, func(*CensusV3) {}, false},
		{"early pidfd wrong witness", "pidfd_open", 3, func(c *CensusV3) { c.Proofs[0].PID = 31 }, false},
		{"early pidfd bare errno", "pidfd_open", 3, func(c *CensusV3) { c.Resolutions[0].ProofIndex = 0 }, false},
		{"early pidfd fixture witness", "pidfd_open", 3, func(c *CensusV3) { c.Proofs[0].Method = "fixture_pidfd_no_pid" }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, e := v3Fixture()
			e[0].Operation = tc.op
			e[0].Errno = tc.errno
			tc.mutate(&c)
			err := ValidateCensusV3(c, e, len(e), false, 3, 9)
			if (err == nil) != tc.want {
				t.Fatalf("accepted=%v want=%v: %v", err == nil, tc.want, err)
			}
		})
	}
}

func TestCensusV3VerifiedDescendantAndAdversaries(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*CensusV3, *[]EvidenceError)
		want   bool
	}{
		{"verified inherited descendant", func(*CensusV3, *[]EvidenceError) {}, true},
		{"root lost", func(c *CensusV3, _ *[]EvidenceError) { c.Scans[3].Verified = []CensusIdentity{} }, false},
		{"root reused", func(c *CensusV3, _ *[]EvidenceError) { c.Scans[3].Verified[0].StartTicks = "201" }, false},
		{"same SID different token", func(c *CensusV3, _ *[]EvidenceError) {
			c.Certificates[0].Chain[0].InstanceTokenSHA256 = strings.Repeat("c", 64)
		}, false},
		{"partial identity", func(c *CensusV3, _ *[]EvidenceError) { c.Certificates[0].Chain[0].Template = "" }, false},
		{"root false alone", func(c *CensusV3, _ *[]EvidenceError) { c.Certificates[0].Chain = c.Certificates[0].Chain[:1] }, false},
		{"unknown ancestry", func(c *CensusV3, _ *[]EvidenceError) { c.Certificates[0].Chain[0].PPID = 99 }, false},
		{"protected root retired", func(c *CensusV3, _ *[]EvidenceError) { c.Certificates[0].Chain[0].DeclaredRoot = true }, false},
		{"controller retired", func(c *CensusV3, _ *[]EvidenceError) { c.Certificates[0].Chain[0].PID = 9 }, false},
		{"UID unreadable", func(c *CensusV3, _ *[]EvidenceError) { c.Certificates[0].Chain[0].UIDsRevalidated = false }, false},
		{"incarnation unbound", func(c *CensusV3, _ *[]EvidenceError) { c.Certificates[0].Chain[0].PIDFDBound = false }, false},
		{"null promoted in place", func(c *CensusV3, _ *[]EvidenceError) { c.Proofs[0].StartTicks = c.Proofs[1].StartTicks }, false},
		{"wrong witness scan", func(c *CensusV3, _ *[]EvidenceError) { c.Proofs[1].ScanIndex = 4 }, false},
		{"wrong original witness", func(c *CensusV3, _ *[]EvidenceError) { c.Certificates[0].AbsenceProof = 2 }, false},
		{"fixture is not kernel", func(c *CensusV3, _ *[]EvidenceError) { c.Proofs[0].Method = "fixture_pidfd_no_pid" }, false},
		{"missing certificate", func(c *CensusV3, _ *[]EvidenceError) { c.Certificates = []DescendantCertificate{} }, false},
		{"certificate ID not caller slot", func(c *CensusV3, _ *[]EvidenceError) {
			c.Certificates[0].ID = 2
			c.Certificates = append(c.Certificates, c.Certificates[0])
		}, false},
		{"managed proof mislabeled unprotected", func(c *CensusV3, _ *[]EvidenceError) {
			c.Certificates = []DescendantCertificate{}
			c.Proofs[1].ProtectedIdentity = false
			c.Proofs[1].DescendantCertificateID = 0
		}, false},
		{"root proof mislabeled unprotected", func(c *CensusV3, e *[]EvidenceError) {
			c.Certificates = []DescendantCertificate{}
			start := "200"
			c.Proofs[0].PID = 20
			c.Proofs[1].PID = 20
			c.Proofs[1].StartTicks = &start
			c.Proofs[1].ProtectedIdentity = false
			c.Proofs[1].DescendantCertificateID = 0
			(*e)[0].PID = 20
			for i := 2; i < 4; i++ {
				c.Scans[i].Verified[0].PID = 21
				c.Scans[i].Verified[0].StartTicks = "201"
			}
		}, false},
		{"same retired incarnation twice", func(c *CensusV3, _ *[]EvidenceError) {
			start := "500"
			c.Proofs = append(c.Proofs, CensusProofV3{CensusProof: CensusProof{Kind: "incarnation_retired", Method: "pidfd_no_pid", PID: 50, StartTicks: &start, ScanIndex: 3, OffsetMS: 2}}, CensusProofV3{CensusProof: CensusProof{Kind: "incarnation_retired", Method: "pidfd_no_pid", PID: 50, StartTicks: &start, ScanIndex: 4, OffsetMS: 3}})
		}, false},
		{"FD exceeded", func(c *CensusV3, _ *[]EvidenceError) { c.PeakFD = 33 }, false},
		{"aggregate memory exceeded", func(c *CensusV3, _ *[]EvidenceError) { c.RetainedBytes = 16777217 }, false},
		{"old scan relabeled", func(c *CensusV3, _ *[]EvidenceError) { c.Scans[2].ScanIndex = 2 }, false},
		{"unresolved permission", func(_ *CensusV3, e *[]EvidenceError) { (*e)[0].Errno = 13 }, false},
		{"uninspected late birth", func(_ *CensusV3, e *[]EvidenceError) { (*e)[0].Reason = "uninspected_birth" }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, e := v3Fixture()
			raw, _ := json.Marshal(c)
			_ = json.Unmarshal(raw, &c) // own slices per adversary
			tc.mutate(&c, &e)
			err := ValidateCensusV3(c, e, len(e), false, 3, 9)
			if (err == nil) != tc.want {
				t.Fatalf("accepted=%v want=%v: %v", err == nil, tc.want, err)
			}
		})
	}
}

func TestCensusV3FreshPairPreservesLateBirthHistory(t *testing.T) {
	c, e := v3Fixture()
	d := strings.Repeat("d", 64)
	root := c.Scans[3].Verified[0]
	born := root
	born.PID = 40
	born.PPID = 20
	born.PGID = 40
	born.StartTicks = "400"
	born.Name = "new-child"
	born.DeclaredRoot = false
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
	if err := ValidateCensusV3(c, e, len(e), false, 7, 9); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"nonmanaged", "kernel"} {
		t.Run("truthful latebirth "+kind, func(t *testing.T) {
			raw, _ := json.Marshal(c)
			var fresh CensusV3
			_ = json.Unmarshal(raw, &fresh)
			for i := 4; i < 7; i++ {
				v := &fresh.Scans[i].Verified[1]
				v.Classification = kind
				v.SessionID = ""
				v.City = ""
				v.Template = ""
				v.Epoch = 0
				v.InstanceTokenSHA256 = ""
				v.NoGCEnvironment = kind == "nonmanaged"
				v.EnvironmentRevalidated = kind == "nonmanaged"
				if kind == "kernel" {
					v.KernelFlags = 0x00200000
					v.PGID = 0
					v.StartTicks = "0"
					v.UIDs = [4]uint32{}
					v.Name = []string{"kworker/1:0", "kworker/u3", "kworker/5:7"}[i-4]
				}
			}
			if err := ValidateCensusV3(fresh, e, len(e), false, 7, 9); err != nil {
				t.Fatal(err)
			}
			fresh.Scans[4].Verified[1].NoGCEnvironment = !fresh.Scans[4].Verified[1].NoGCEnvironment
			if ValidateCensusV3(fresh, e, len(e), false, 7, 9) == nil {
				t.Fatal("fabricated no-GC environment accepted")
			}
		})
	}
	t.Run("late birth not actually classified", func(t *testing.T) {
		mutated := c
		mutated.Scans = append([]CensusScanV3{}, c.Scans...)
		mutated.Scans[4].Verified = []CensusIdentity{root}
		if ValidateCensusV3(mutated, e, len(e), false, 7, 9) == nil {
			t.Fatal("accepted absent birth classification")
		}
	})
	t.Run("older successful seal selected", func(t *testing.T) {
		mutated := c
		mutated.SelectedClosings = [2]int{2, 3}
		mutated.Seal.ScanIndex = 4
		if ValidateCensusV3(mutated, e, len(e), false, 7, 9) == nil {
			t.Fatal("discarded later raw scans")
		}
	})
	t.Run("known incarnation changed", func(t *testing.T) {
		mutated := append([]EvidenceError{}, e...)
		start := "401"
		mutated[1].StartTicks = &start
		if ValidateCensusV3(c, mutated, len(mutated), false, 7, 9) == nil {
			t.Fatal("reused PID treated as old incarnation")
		}
	})
	t.Run("permission hidden by fresh census", func(t *testing.T) {
		mutated := append([]EvidenceError{}, e...)
		mutated[1].Reason = "process_unavailable"
		mutated[1].Operation = "environ"
		mutated[1].Errno = 13
		if ValidateCensusV3(c, mutated, len(mutated), false, 7, 9) == nil {
			t.Fatal("permission error erased")
		}
	})
	t.Run("deadline not reset", func(t *testing.T) {
		if ValidateCensusV3(c, e, len(e), false, 10000, 9) == nil {
			t.Fatal("extended original deadline")
		}
	})
	t.Run("truncation never success", func(t *testing.T) {
		if ValidateCensusV3(c, e, len(e), true, 7, 9) == nil {
			t.Fatal("discarded truncated history")
		}
	})
}

func TestCensusV3SerializedShapeIsExact(t *testing.T) {
	c, e := v3Fixture()
	data, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	var decoded CensusV3
	if err := DecodeStrict(data, &decoded); err != nil {
		t.Fatal(err)
	}
	if err := ValidateCensusV3(decoded, e, len(e), false, 3, 9); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{
		strings.Replace(string(data), `"selected_closings":[2,3]`, `"selected_closings":[2,3,4]`, 1),
		strings.Replace(string(data), `"uids":[1000,1000,1000,1000]`, `"uids":[1000]`, 1),
		strings.Replace(string(data), `"uids":[1000,1000,1000,1000]`, `"uids":null`, 1),
		strings.Replace(string(data), `"schema":`, `"extra":1,"schema":`, 1),
		strings.Replace(string(data), `"schema":`, `"schema":"duplicate","schema":`, 1),
		strings.Replace(string(data), `"pidfd_bound":true`, `"pidfd_bound":null`, 1),
	} {
		if err := DecodeStrict([]byte(bad), &decoded); err == nil {
			t.Fatalf("accepted wrong serialized shape: %s", bad)
		}
	}
	if dir := os.Getenv("GC_TEST_V3_VECTOR_DIR"); dir != "" {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		vector := struct {
			Census                 CensusV3        `json:"census"`
			Errors                 []EvidenceError `json:"errors"`
			DurationMS             int64           `json:"duration_ms"`
			ControllerPID          int             `json:"controller_pid"`
			CertificateDisposition string          `json:"certificate_disposition"`
		}{c, e, 3, 9, "provisional"}
		raw, err := json.MarshalIndent(vector, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "provisional-census-v3.json"), append(raw, '\n'), 0o600); err != nil {
			t.Fatal(err)
		}
		wire := ResponseV3{Schema: ResponseSchemaV3, Scope: "fixture", RequestNonce: strings.Repeat("0", 64), HelperSourceRevision: strings.Repeat("0", 40), HelperBinarySHA256: strings.Repeat("0", 64), PolicyDigest: strings.Repeat("0", 64), BootID: "00000000-0000-0000-0000-000000000000", PIDNamespaceIdentity: "pid:[123]", Complete: true, Roots: []Root{}, Errors: e, ErrorsTotal: len(e), DurationMS: 3, KernelRelease: "6.8.fixture", KernelProofProfile: KernelProofProfile, Census: c, CertificateDisposition: "provisional"}
		// Deliberately fixture scope/zero pins/time, so this serialized shape
		// cannot masquerade as a supported live helper response or provider proof.
		raw, err = json.MarshalIndent(wire, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "synthetic-provisional-host-v3.json"), append(raw, '\n'), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}
