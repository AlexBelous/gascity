package procobserver

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"testing"
)

// This is a modeled compound witness, not a new Linux kernel capture. The
// producer must independently establish both original descriptor witnesses.
func readPollCompanionFixture(t *testing.T) exitFixture {
	t.Helper()
	raw, err := os.ReadFile("testdata/census-exit-poll-companion-modeled.json")
	if err != nil {
		t.Fatal(err)
	}
	var x exitFixture
	if err := json.Unmarshal(raw, &x); err != nil {
		t.Fatal(err)
	}
	return x
}

func TestOriginalFullPollCompanion(t *testing.T) {
	x := readPollCompanionFixture(t)
	if !acceptExitFixture(x) {
		t.Fatal("modeled birth4/FULL4-6/present7/poll7 compound witness rejected")
	}
}

func TestOriginalFullPollOwnedKernelVector(t *testing.T) {
	path, supplied := os.LookupEnv("GC_ORIGINAL_FULL_POLL_VECTOR")
	if !supplied {
		path = "testdata/census-original-full-poll-actual-linux.json"
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !supplied {
		if fmt.Sprintf("%x", sha256.Sum256(raw)) != "f5d360ce429a07b9b80d7f8b133bd25ce0fce7c63572ba8af43de21c419202e0" {
			t.Fatal("retained actual Linux vector bytes changed")
		}
		t.Log("retained real Linux6.8 owned-child vector; not fresh kernel execution")
	} else {
		t.Log("explicit supplied vector; no retained-fixture fallback")
	}
	var x exitFixture
	if err := json.Unmarshal(raw, &x); err != nil {
		t.Fatal(err)
	}
	if !acceptExitFixture(x) || x.Total != 2 || len(x.Census.Scans) != 10 {
		t.Fatal("actual original FULL/poll companion C-to-Go vector rejected")
	}
}

func TestOriginalFullPollCompanionPreservesENVAndSingleCompanion(t *testing.T) {
	for _, mode := range []string{"ENV", "duplicate-poll", "mixed-two"} {
		t.Run(mode, func(t *testing.T) {
			x := readPollCompanionFixture(t)
			env := readExitFixture(t).Errors[2]
			env.ScanIndex = 7
			if mode == "ENV" {
				x.Errors[2] = env
			} else {
				e := x.Errors[2]
				if mode == "mixed-two" {
					e = env
				}
				x.Errors = append(x.Errors, e)
				x.Total++
				link := x.Census.Resolutions[2]
				link.ErrorIndex = len(x.Errors)
				x.Census.Resolutions = append(x.Census.Resolutions, link)
			}
			if acceptExitFixture(x) != (mode == "ENV") {
				t.Fatal("ENV preservation or total companion bound failed")
			}
		})
	}
}

func TestOriginalFullPollCompanionRefusals(t *testing.T) {
	cases := map[string]func(*exitFixture){
		"positive raw start":              func(x *exitFixture) { s := "375301540"; x.Errors[2].StartTicks = &s },
		"wrong operation":                 func(x *exitFixture) { x.Errors[2].Operation = "proc_directory" },
		"wrong errno":                     func(x *exitFixture) { x.Errors[2].Errno = 3 },
		"capture diagnostic on poll":      func(x *exitFixture) { x.Errors[2].CaptureDiagnostic = readExitFixture(t).Errors[2].CaptureDiagnostic },
		"raw resolved_by":                 func(x *exitFixture) { x.Errors[2].ResolvedBy = 4 },
		"stage B substitution":            func(x *exitFixture) { x.Census.Proofs[3].ExitWitness.CaptureStage = "initial_pidfd_poll" },
		"no FULL witness":                 func(x *exitFixture) { x.Census.Proofs[3].ExitWitness.FullIdentityRevalidated = false },
		"original initial poll rewritten": func(x *exitFixture) { x.Census.Proofs[3].ExitWitness.InitialPoll = "valid_pollin" },
		"terminal zero":                   func(x *exitFixture) { x.Census.Proofs[3].ExitWitness.TerminalPoll = "zero" },
		"UID mismatch":                    func(x *exitFixture) { x.Census.Proofs[3].ExitWitness.TerminalUIDsAfter[0]++ },
		"protected proof":                 func(x *exitFixture) { x.Census.Proofs[3].ProtectedIdentity = true },
		"birth link missing":              func(x *exitFixture) { x.Census.Resolutions[1].Kind = "known_nonmanaged_terminal" },
		"duplicate companion":             func(x *exitFixture) { x.Census.Resolutions = append(x.Census.Resolutions, x.Census.Resolutions[2]) },
		"companion points elsewhere":      func(x *exitFixture) { x.Census.Resolutions[2].ProofIndex = 5 },
		"classified scan not birth":       func(x *exitFixture) { x.Census.Resolutions[2].ClassifiedScan = 6 },
		"PID reuse": func(x *exitFixture) {
			for i := range x.Census.Scans[5].Verified {
				if x.Census.Scans[5].Verified[i].PID == x.Errors[2].PID {
					x.Census.Scans[5].Verified[i].StartTicks = "9"
				}
			}
		},
		"deadline":     func(x *exitFixture) { x.Duration = 10000 },
		"FD bound":     func(x *exitFixture) { x.Census.PeakFD = 33 },
		"memory bound": func(x *exitFixture) { x.Census.RetainedBytes = 16*1024*1024 + 1 },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			x := readPollCompanionFixture(t)
			mutate(&x)
			if acceptExitFixture(x) {
				t.Fatal("invalid compound witness accepted")
			}
		})
	}
}
