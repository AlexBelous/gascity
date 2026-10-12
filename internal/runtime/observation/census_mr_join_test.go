package observation

import (
	"encoding/json"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/runtime"
	"github.com/gastownhall/gascity/internal/runtime/procobserver"
)

// Whole ordinary join uses TWO independently captured controlled Linux frames.
// The provider is synthetic; this test neither proves live caps nor grants work.
func TestOriginalCaptureMixedOrdinaryBothJoins(t *testing.T) {
	type frameVector struct {
		Census     procobserver.CensusV3        `json:"census"`
		Errors     []procobserver.EvidenceError `json:"errors"`
		Total      int                          `json:"errors_total"`
		Duration   int64                        `json:"duration_ms"`
		Controller int                          `json:"controller_pid"`
	}
	var vector struct {
		frameVector
		Second frameVector `json:"second"`
	}
	data, err := os.ReadFile("../procobserver/testdata/mr-kernel-vector.json")
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(data, &vector); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"accepted", "second-root-lost", "provider-owner-lost", "second-frame-contract-lost", "changed-provider-token"} {
		t.Run(mode, func(t *testing.T) {
			p := &observedProvider{Fake: runtime.NewFake(), capable: true}
			first, second := vector.frameVector, vector.Second
			frames := []procobserver.ResponseV3{}
			now := time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC)
			for _, v := range []frameVector{first, second} {
				if err := procobserver.ValidateCensusV3(v.Census, v.Errors, v.Total, false, v.Duration, v.Controller); err != nil {
					t.Fatal(err)
				}
				r := procobserver.ResponseV3{Census: v.Census, Errors: v.Errors, ErrorsTotal: v.Total, DurationMS: v.Duration, Complete: true, CertificateDisposition: "provisional", CallerBinding: procobserver.CallerBinding{PID: v.Controller}, StartedAt: now, FinishedAt: now, Roots: []procobserver.Root{}}
				for _, row := range v.Census.Scans[len(v.Census.Scans)-1].Verified {
					if row.DeclaredRoot {
						r.Roots = append(r.Roots, procobserver.Root{PID: row.PID, PPID: row.PPID, PGID: row.PGID, StartTicks: row.StartTicks, SessionID: row.SessionID, City: row.City, Template: row.Template, Epoch: row.Epoch, InstanceTokenSHA256: row.InstanceTokenSHA256, Name: row.Name})
					}
				}
				frames = append(frames, r)
			}
			for i, root := range frames[0].Roots {
				name := "fixture-root-" + strconv.Itoa(i)
				p.names = append(p.names, name)
				p.roots = append(p.roots, runtime.LiveRuntime{PID: root.PID, PPID: root.PPID, SessionID: root.SessionID, City: root.City, Epoch: root.Epoch, ProviderName: name, IsTracked: true})
				token := "private-owned-v3-token"
				if mode == "changed-provider-token" {
					token = "different"
				}
				for key, value := range map[string]string{"GC_SESSION_ID": root.SessionID, "GC_TEMPLATE": root.Template, "GC_RUNTIME_EPOCH": strconv.Itoa(root.Epoch), "GC_INSTANCE_TOKEN": token} {
					if err := p.SetMeta(name, key, value); err != nil {
						t.Fatal(err)
					}
				}
			}
			if mode == "provider-owner-lost" {
				p.roots[0].IsTracked = false
			}
			ep := &evidenceProvider{observedProvider: p}
			calls := 0
			got := ObserveProcessEvidence("/disposable-v3-test", ep, func() ProcessEvidence {
				frame := frames[calls]
				calls++
				anchors, err := frame.RetirementAnchors()
				if err != nil {
					t.Fatal(err)
				}
				e := ProcessEvidence{Roots: frame.ObservedRoots(), StartedAt: now, FinishedAt: now, CensusContract: procobserver.CensusV3Schema, RetirementAnchors: anchors}
				if calls == 2 && mode == "second-root-lost" {
					e.Roots = e.Roots[1:]
				}
				if calls == 2 && mode == "second-frame-contract-lost" {
					e.CensusContract = ""
				}
				return e
			}, func() time.Time { return now })
			green := got.ProviderComplete && got.ProcessComplete && got.CertificateDisposition == "provider_verified"
			if green != (mode == "accepted") {
				t.Fatalf("mode=%s green=%v observation=%+v", mode, green, got)
			}
			if calls != 2 {
				t.Fatalf("unexpected frame count %d", calls)
			}
		})
	}
}
