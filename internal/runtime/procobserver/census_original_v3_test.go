package procobserver

import (
	"encoding/json"
	"os"
	"testing"
	"time"
)

type originalKernelVector struct {
	Truncated   bool            `json:"errors_truncated"`
	Census      CensusV3        `json:"census"`
	Errors      []EvidenceError `json:"errors"`
	ErrorsTotal int             `json:"errors_total"`
	Duration    int64           `json:"duration_ms"`
	Controller  int             `json:"controller_pid"`
}

func originalVector(t *testing.T) originalKernelVector {
	t.Helper()
	data, err := os.ReadFile("testdata/mr-kernel-vector.json")
	if err != nil {
		t.Fatal(err)
	}
	var v originalKernelVector
	if err = json.Unmarshal(data, &v); err != nil {
		t.Fatal(err)
	}
	return v
}

// This vector is produced by the persistent controlled Linux M/R whole census
// with genuine FD/UID/exit/numeric witnesses, synthetic ENV failure, no live RPC.
func TestOriginalCaptureKernelMixedReaders(t *testing.T) {
	v := originalVector(t)
	if err := ValidateCensusV3(v.Census, v.Errors, v.ErrorsTotal, false, v.Duration, v.Controller); err != nil {
		t.Fatal(err)
	}
	if !validCompleteCaptureDiagnosticLinks(v.Census, v.Errors) {
		t.Fatal("original diagnostic lost")
	}
	kinds := map[string]bool{}
	for _, r := range v.Census.Resolutions {
		kinds[r.Kind] = true
	}
	for _, k := range []string{"deferred_managed_retirement", "managed_birth_retirement", "direct_reaped_birth"} {
		if !kinds[k] {
			t.Fatal("missing", k)
		}
	}
}

func TestOriginalCaptureStrictNegatives(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*originalKernelVector)
	}{
		{"missing-custody", func(v *originalKernelVector) { v.Census.Resolutions[0].OriginalCapture = nil }},
		{"wrong-original-binding", func(v *originalKernelVector) { v.Census.Resolutions[0].OriginalCapture.PID++ }},
		{"wrong-start", func(v *originalKernelVector) { v.Census.Resolutions[0].OriginalCapture.BoundStartTicks = "1" }},
		{"missing-original-fd", func(v *originalKernelVector) { v.Census.Resolutions[0].OriginalCapture.OriginalPIDFDInode = 0 }},
		{"missing-original-directory", func(v *originalKernelVector) { v.Census.Resolutions[0].OriginalCapture.OriginalProcdirInode = 0 }},
		{"not-same-descriptors", func(v *originalKernelVector) { v.Census.Resolutions[0].OriginalCapture.SameOriginalDescriptors = false }},
		{"live-fdinfo", func(v *originalKernelVector) {
			v.Census.Resolutions[0].OriginalCapture.TerminalFDInfoPID = v.Census.Resolutions[0].OriginalCapture.PID
		}},
		{"wrong-flags", func(v *originalKernelVector) { v.Census.Resolutions[0].OriginalCapture.TerminalPoll = "refused_flags" }},
		{"changed-uid", func(v *originalKernelVector) { v.Census.Resolutions[0].OriginalCapture.OriginalUIDs[0]++ }},
		{"root-uid", func(v *originalKernelVector) { v.Census.Resolutions[0].OriginalCapture.OriginalUIDs[0] = 0 }},
		{"late-witness", func(v *originalKernelVector) { v.Census.Resolutions[0].OriginalCapture.TerminalOffsetMS = 10000 }},
		{"before-numeric-receipt", func(v *originalKernelVector) {
			o := v.Census.Resolutions[0].OriginalCapture
			v.Census.Scans[o.MissingScan-1].OffsetMS = o.TerminalOffsetMS + 1
		}},
		{"wrong-numeric-digest", func(v *originalKernelVector) {
			v.Census.Scans[2].EnumerationDigest = "0000000000000000000000000000000000000000000000000000000000000000"
		}},
		{"missing-numeric-membership", func(v *originalKernelVector) { v.Census.Scans[2].NumericPIDs = nil }},
		{"no-later-real-seal", func(v *originalKernelVector) { v.Census.Resolutions[0].OriginalCapture.MissingScan = 10 }},
		{"wrong-certificate", func(v *originalKernelVector) { v.Census.Resolutions[0].OriginalCapture.CertificateID = 2 }},
		{"wrong-absence-link", func(v *originalKernelVector) { v.Census.Resolutions[0].OriginalCapture.AbsenceProof = 3 }},
		{"second-pass", func(v *originalKernelVector) { v.Errors[0].CaptureDiagnostic.FieldsPass = 2 }},
		{"rewrite-initial-poll", func(v *originalKernelVector) { v.Errors[0].CaptureDiagnostic.ExitPoll = "valid_pollin" }},
		{"rewrite-raw-witness", func(v *originalKernelVector) { yes := true; v.Errors[0].CaptureDiagnostic.ExitWitness = &yes }},
		{"duplicate-error", func(v *originalKernelVector) { v.Census.Resolutions[1].ErrorIndex = v.Census.Resolutions[0].ErrorIndex }},
		{"root-loss", func(v *originalKernelVector) {
			cert := v.Census.Certificates[0]
			root := cert.Chain[len(cert.Chain)-1].PID
			for i, r := range v.Census.Scans[8].Verified {
				if r.PID == root {
					v.Census.Scans[8].Verified = append(v.Census.Scans[8].Verified[:i], v.Census.Scans[8].Verified[i+1:]...)
					break
				}
			}
		}},
		{"changed-ancestry", func(v *originalKernelVector) { v.Census.Certificates[0].Chain[0].PPID++ }},
		{"changed-owner", func(v *originalKernelVector) { v.Census.Certificates[0].Chain[0].SessionID = "different" }},
		{"fd-budget", func(v *originalKernelVector) { v.Census.PeakFD = 33 }},
		{"memory-budget", func(v *originalKernelVector) { v.Census.RetainedBytes = 16*1024*1024 + 1 }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			v := originalVector(t)
			tc.mutate(&v)
			if ValidateCensusV3(v.Census, v.Errors, v.ErrorsTotal, false, v.Duration, v.Controller) == nil {
				t.Fatal("accepted invalid witness")
			}
		})
	}
}

func TestOriginalCaptureSharedKernelMatrix(t *testing.T) {
	data, err := os.ReadFile("testdata/native_mr_matrix.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		Name string          `json:"name"`
		Case json.RawMessage `json:"case"`
		Want bool            `json:"want"`
	}
	if err = json.Unmarshal(data, &cases); err != nil {
		t.Fatal(err)
	}
	for _, tc := range cases {
		t.Run(tc.Name, func(t *testing.T) {
			var v originalKernelVector
			err := json.Unmarshal(tc.Case, &v)
			accepted := err == nil && ValidateCensusV3(v.Census, v.Errors, v.ErrorsTotal, v.Truncated, v.Duration, v.Controller) == nil && validCompleteCaptureDiagnosticLinks(v.Census, v.Errors)
			if accepted != tc.Want {
				t.Fatalf("accepted=%v want=%v err=%v", accepted, tc.Want, err)
			}
		})
	}
}

// The helper envelope is synthetic, while its mixed census/FD witnesses come
// from the real controlled Linux producer. No live helper/capability claim.
func TestOriginalCaptureWholeHelperEnvelope(t *testing.T) {
	v := originalVector(t)
	p, r, now := clientV3Fixture()
	p.CallerBinding.PID = v.Controller
	r.CallerBinding = p.CallerBinding
	r.StartedAt = now.Add(-time.Duration(v.Duration) * time.Millisecond)
	r.FinishedAt = now
	r.Census = v.Census
	r.Errors = v.Errors
	r.ErrorsTotal = v.ErrorsTotal
	r.DurationMS = v.Duration
	r.Roots = []Root{}
	for _, row := range v.Census.Scans[len(v.Census.Scans)-1].Verified {
		if row.Classification == "managed" && row.DeclaredRoot {
			r.Roots = append(r.Roots, Root{PID: row.PID, PPID: row.PPID, PGID: row.PGID, StartTicks: row.StartTicks, SessionID: row.SessionID, City: row.City, Template: row.Template, Epoch: row.Epoch, InstanceTokenSHA256: row.InstanceTokenSHA256, Name: row.Name})
		}
	}
	r.EnumeratedCountBefore = v.Census.Scans[0].EnumeratedCount
	r.EnumerationDigestBefore = v.Census.Scans[0].EnumerationDigest
	lastClosing := v.Census.Scans[v.Census.SelectedClosings[1]-1]
	r.EnumeratedCountAfter = lastClosing.EnumeratedCount
	r.EnumerationDigestAfter = lastClosing.EnumerationDigest
	data, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	got, err := DecodeResponseV3(data, p, r.RequestNonce, now)
	if err != nil {
		t.Fatal(err)
	}
	anchors, err := got.RetirementAnchors()
	if err != nil || len(anchors) != 2 {
		t.Fatalf("anchors=%d err=%v", len(anchors), err)
	}
	for _, mode := range []string{"null-custody", "extra-custody-key", "UID5", "wrong-source", "wrong-namespace", "wrong-caller", "missing-protected-root"} {
		t.Run(mode, func(t *testing.T) {
			var raw map[string]any
			if err := json.Unmarshal(data, &raw); err != nil {
				t.Fatal(err)
			}
			links := raw["census"].(map[string]any)["resolutions"].([]any)
			link := links[0].(map[string]any)
			switch mode {
			case "null-custody":
				link["original_capture"] = nil
			case "extra-custody-key":
				link["original_capture"].(map[string]any)["unreviewed"] = true
			case "UID5":
				o := link["original_capture"].(map[string]any)
				o["original_uids"] = append(o["original_uids"].([]any), float64(1000))
			case "wrong-source":
				raw["helper_source_revision"] = "0000000000000000000000000000000000000000"
			case "wrong-namespace":
				raw["pid_namespace_identity"] = "pid:[other]"
			case "wrong-caller":
				raw["caller_binding"].(map[string]any)["pid"] = float64(1)
			case "missing-protected-root":
				raw["roots"] = []any{}
			}
			bad, err := json.Marshal(raw)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = DecodeResponseV3(bad, p, r.RequestNonce, now); err == nil {
				t.Fatal("accepted broken whole envelope")
			}
		})
	}
}
