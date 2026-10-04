package auto

import (
	"testing"

	"github.com/gastownhall/gascity/internal/runtime"
)

type evidenceTracker struct {
	*runtime.Fake
	handles []runtime.ProcessHandle
	drop    bool
}

func (p *evidenceTracker) TrackProcessRoots(roots []runtime.LiveRuntime) ([]runtime.LiveRuntime, error) {
	if p.drop {
		return nil, nil
	}
	return runtime.BindProcessEvidence(roots, p.handles)
}

func TestProcessEvidenceCompositeCannotHideOrphansOrDoubleOwners(t *testing.T) {
	roots := []runtime.LiveRuntime{{PID: 42, SessionID: "sid", City: "/city", Epoch: 2}, {PID: 43, SessionID: "sid", City: "/city", Epoch: 1}}
	for _, mode := range []string{"exact", "missing tracker", "omission", "conflicting owners"} {
		t.Run(mode, func(t *testing.T) {
			def := &evidenceTracker{Fake: runtime.NewFake(), handles: []runtime.ProcessHandle{{Name: "live", PID: 42, SessionID: "sid"}}}
			acp := &evidenceTracker{Fake: runtime.NewFake()}
			var backend runtime.Provider = acp
			switch mode {
			case "missing tracker":
				backend = runtime.NewFake()
			case "omission":
				acp.drop = true
			case "conflicting owners":
				acp.handles = []runtime.ProcessHandle{{Name: "alias", PID: 42, SessionID: "sid"}}
			}
			got, err := New(def, backend).TrackProcessRoots(roots)
			if mode != "exact" {
				if err == nil {
					t.Fatal("incomplete/conflicting attribution accepted")
				}
				return
			}
			if err != nil || len(got) != 2 || !got[0].IsTracked || got[1].IsTracked {
				t.Fatalf("bad composite %+v %v", got, err)
			}
		})
	}
}
