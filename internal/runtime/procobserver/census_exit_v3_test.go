package procobserver

import (
	"encoding/json"
	"os"
	"reflect"
	"testing"
	"time"
)

type exitFixture struct {
	Census     CensusV3        `json:"census"`
	Errors     []EvidenceError `json:"errors"`
	Total      int             `json:"errors_total"`
	Truncated  bool            `json:"errors_truncated"`
	Duration   int64           `json:"duration_ms"`
	Controller int             `json:"controller_pid"`
}

func readExitFixture(t *testing.T) exitFixture {
	t.Helper()
	raw, err := os.ReadFile("testdata/census-exit-mixed.json")
	if err != nil {
		t.Fatal(err)
	}
	if err = validateRequiredTypes(raw, reflect.TypeOf(exitFixture{})); err != nil {
		t.Fatal(err)
	}
	var x exitFixture
	if err = json.Unmarshal(raw, &x); err != nil {
		t.Fatal(err)
	}
	return x
}

func acceptExitFixture(x exitFixture) bool {
	return ValidateCensusV3(x.Census, x.Errors, x.Total, x.Truncated, x.Duration, x.Controller) == nil && validCompleteCaptureDiagnosticLinks(x.Census, x.Errors)
}

func TestExitMixedActualLinuxProducer(t *testing.T) {
	x := readExitFixture(t)
	if !acceptExitFixture(x) {
		t.Fatal("actual C producer ledger/normalizer rejected")
	}
	if len(x.Census.Scans) != 10 || len(x.Census.Proofs) != 5 || len(x.Census.CoverageRetirements) != 1 || x.Total != 4 {
		t.Fatal("mixed fixture incomplete")
	}
}

func TestExitMixedStrictNegatives(t *testing.T) {
	cases := map[string]func(*exitFixture){
		"missing witness":             func(x *exitFixture) { x.Census.Proofs[0].ExitWitness = nil },
		"wrong method":                func(x *exitFixture) { x.Census.Proofs[0].Method = "pidfd_exited" },
		"wrong stage":                 func(x *exitFixture) { x.Census.Proofs[0].ExitWitness.CaptureStage = "initial_pidfd_poll" },
		"poll zero no witness":        func(x *exitFixture) { x.Census.Proofs[0].ExitWitness.TerminalPoll = "zero" },
		"refused flags":               func(x *exitFixture) { x.Census.Proofs[0].ExitWitness.TerminalPoll = "refused_flags" },
		"wrong original binding":      func(x *exitFixture) { x.Census.Proofs[0].ExitWitness.BoundStartTicks = "9" },
		"UID zero":                    func(x *exitFixture) { x.Census.Proofs[0].ExitWitness.TerminalUIDsBefore[0] = 0 },
		"UID change":                  func(x *exitFixture) { x.Census.Proofs[0].ExitWitness.TerminalUIDsAfter[0]++ },
		"protected proof":             func(x *exitFixture) { x.Census.Proofs[0].ProtectedIdentity = true },
		"managed prior":               func(x *exitFixture) { x.Census.Scans[0].Verified[0].Classification = "managed" },
		"declared root":               func(x *exitFixture) { x.Census.Scans[0].Verified[0].DeclaredRoot = true },
		"tmux prior":                  func(x *exitFixture) { x.Census.Scans[0].Verified[0].Name = "tmux" },
		"permission":                  func(x *exitFixture) { x.Errors[0].Errno = 13 },
		"missing original diagnostic": func(x *exitFixture) { x.Errors[0].CaptureDiagnostic = nil },
		"second ENV pass": func(x *exitFixture) {
			x.Errors[0].CaptureDiagnostic.FieldsPass = 2
			*x.Errors[0].CaptureDiagnostic.FirstIdentityPresent = true
		},
		"regrade original zero": func(x *exitFixture) {
			x.Errors[0].CaptureDiagnostic.ExitPoll = "valid_pollin"
			*x.Errors[0].CaptureDiagnostic.ExitWitness = true
		},
		"before completed fault":   func(x *exitFixture) { x.Census.Proofs[0].OffsetMS = -1 },
		"after chosen seal":        func(x *exitFixture) { x.Census.Proofs[0].OffsetMS = x.Duration + 1 },
		"reuse prior":              func(x *exitFixture) { x.Census.Scans[0].Verified[0].StartTicks = "9" },
		"duplicate error use":      func(x *exitFixture) { x.Census.Resolutions[1].ErrorIndex = 1 },
		"duplicate retirement":     func(x *exitFixture) { x.Census.Proofs = append(x.Census.Proofs, x.Census.Proofs[0]) },
		"C missing full identity":  func(x *exitFixture) { x.Census.Proofs[3].ExitWitness.FullIdentityRevalidated = false },
		"C direct reaped":          func(x *exitFixture) { x.Census.Proofs[3].ExitWitness.CaptureStage = "direct_reaped_birth" },
		"C no original birth link": func(x *exitFixture) { x.Census.Resolutions[1].Kind = "known_nonmanaged_terminal" },
		"C additional compatible ENV duplicate": func(x *exitFixture) {
			x.Census.Resolutions = append(x.Census.Resolutions, x.Census.Resolutions[2])
			x.Errors = append(x.Errors, x.Errors[2])
			x.Total++
			x.Census.Resolutions[4].ErrorIndex = 5
		},
		"D missing numeric list": func(x *exitFixture) { x.Census.Scans[3].NumericPIDs = nil },
		"D wrong numeric digest": func(x *exitFixture) { x.Census.Scans[3].EnumerationDigest = x.Census.Scans[0].EnumerationDigest },
		"D absent verified only": func(x *exitFixture) {
			x.Census.Scans[3].NumericPIDs = append(x.Census.Scans[3].NumericPIDs, x.Census.CoverageRetirements[0].PID)
		},
		"D wrong errno proof type": func(x *exitFixture) { x.Census.Proofs[1].Method = "pidfd_permission" },
		"D duplicate coverage": func(x *exitFixture) {
			x.Census.CoverageRetirements = append(x.Census.CoverageRetirements, x.Census.CoverageRetirements[0])
		},
		"D invented error zero": func(x *exitFixture) { x.Census.Resolutions[0].ErrorIndex = 0 },
		"FD bound":              func(x *exitFixture) { x.Census.PeakFD = 33 },
		"memory bound":          func(x *exitFixture) { x.Census.RetainedBytes = 16*1024*1024 + 1 },
		"deadline":              func(x *exitFixture) { x.Duration = 10000 },
		"partial raw":           func(x *exitFixture) { x.Truncated = true },
		"backdate seal7":        func(x *exitFixture) { x.Census.SelectedClosings = [2]int{5, 6}; x.Census.Seal.ScanIndex = 7 },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			x := readExitFixture(t)
			mutate(&x)
			if acceptExitFixture(x) {
				t.Fatal("unsafe mixed mutation accepted")
			}
		})
	}
}

func TestExitTypedWireRejectsNullPartialAndUnrelated(t *testing.T) {
	raw, err := os.ReadFile("testdata/census-exit-mixed.json")
	if err != nil {
		t.Fatal(err)
	}

	for _, name := range []string{"null witness", "partial witness", "unrelated witness", "unknown witness field", "null numeric list"} {
		t.Run(name, func(t *testing.T) {
			var x map[string]any
			if err := json.Unmarshal(raw, &x); err != nil {
				t.Fatal(err)
			}
			c := x["census"].(map[string]any)
			proofs := c["proofs"].([]any)
			p := proofs[0].(map[string]any)
			switch name {
			case "null witness":
				p["exit_witness"] = nil
			case "partial witness":
				delete(p["exit_witness"].(map[string]any), "terminal_uids_after")
			case "unrelated witness":
				proofs[1].(map[string]any)["exit_witness"] = p["exit_witness"]
			case "unknown witness field":
				p["exit_witness"].(map[string]any)["substituted_fd"] = true
			case "null numeric list":
				c["scans"].([]any)[3].(map[string]any)["numeric_pids"] = nil
			}
			data, _ := json.Marshal(x)
			if validateRequiredTypes(data, reflect.TypeOf(exitFixture{})) != nil {
				return
			}
			var decoded exitFixture
			if err := json.Unmarshal(data, &decoded); err != nil {
				t.Fatal(err)
			}
			if acceptExitFixture(decoded) {
				t.Fatal("invalid typed wire accepted")
			}
		})
	}
}

func TestExitSharedProducerReaderMatrix(t *testing.T) {
	raw, err := os.ReadFile("testdata/census-exit-matrix.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		Name string          `json:"name"`
		Want bool            `json:"want"`
		Case json.RawMessage `json:"case"`
	}
	if err = json.Unmarshal(raw, &cases); err != nil {
		t.Fatal(err)
	}
	for _, tc := range cases {
		t.Run(tc.Name, func(t *testing.T) {
			accepted := false
			if validateRequiredTypes(tc.Case, reflect.TypeOf(exitFixture{})) == nil {
				var x exitFixture
				if json.Unmarshal(tc.Case, &x) == nil {
					accepted = acceptExitFixture(x)
				}
			}
			if accepted != tc.Want {
				t.Fatalf("accepted=%v want=%v", accepted, tc.Want)
			}
		})
	}
}

// This constructs only a synthetic pinned envelope around the actual controlled
// census. It is not host credential/capability or provider admission evidence.
func TestExitMixedWholeHelperEnvelope(t *testing.T) {
	x := readExitFixture(t)
	for _, mode := range []string{"exact", "wrong namespace", "wrong source", "wrong caller", "partial frame", "no witness"} {
		t.Run(mode, func(t *testing.T) {
			p, r, now := clientV3Fixture()
			p.CallerBinding.PID = x.Controller
			r.CallerBinding = p.CallerBinding
			r.Census = x.Census
			r.Errors = x.Errors
			r.ErrorsTotal = x.Total
			r.ErrorsTruncated = x.Truncated
			r.Roots = []Root{}
			r.DurationMS = x.Duration
			r.StartedAt = now.Add(-time.Duration(x.Duration) * time.Millisecond)
			r.EnumeratedCountBefore = x.Census.Scans[0].EnumeratedCount
			r.EnumerationDigestBefore = x.Census.Scans[0].EnumerationDigest
			r.EnumeratedCountAfter = x.Census.Scans[8].EnumeratedCount
			r.EnumerationDigestAfter = x.Census.Scans[8].EnumerationDigest
			switch mode {
			case "wrong namespace":
				r.PIDNamespaceIdentity = "pid:[999]"
			case "wrong source":
				r.HelperSourceRevision = "1111111111111111111111111111111111111111"
			case "wrong caller":
				r.CallerBinding.PID++
			case "partial frame":
				r.Complete = false
			case "no witness":
				fresh := readExitFixture(t)
				fresh.Census.Proofs[0].ExitWitness = nil
				r.Census = fresh.Census
			}
			data, err := json.Marshal(r)
			if err != nil {
				t.Fatal(err)
			}
			_, err = DecodeResponseV3(data, p, r.RequestNonce, now)
			if (err == nil) != (mode == "exact") {
				t.Fatalf("accepted=%v mode=%s err=%v", err == nil, mode, err)
			}
		})
	}
}
