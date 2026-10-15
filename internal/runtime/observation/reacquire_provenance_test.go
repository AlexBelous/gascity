package observation

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/runtime"
	"github.com/gastownhall/gascity/internal/runtime/proctable"
)

type provenanceProvider struct {
	*observedProvider
	trackingErr error
}

func (p *provenanceProvider) TrackProcessRoots(in []runtime.LiveRuntime) ([]runtime.LiveRuntime, error) {
	if p.trackingErr != nil {
		return nil, p.trackingErr
	}
	out := append([]runtime.LiveRuntime{}, in...)
	for i := range out {
		for _, r := range p.roots {
			if out[i].PID == r.PID {
				out[i].IsTracked = true
				out[i].ProviderName = r.ProviderName
			}
		}
	}
	return out, nil
}

func TestIncompleteProjectionHasTypedNonFatalProvenance(t *testing.T) {
	for _, fatal := range []bool{false, true} {
		p, roots := fixture(t)
		ep := &provenanceProvider{observedProvider: p}
		if fatal {
			ep.trackingErr = errors.New("fixture tracker unavailable")
		}
		now := time.Now()
		calls := 0
		out := ObserveProcessEvidence("/city", ep, func() ProcessEvidence {
			calls++
			e := ProcessEvidence{Roots: append([]proctable.ObservedRoot{}, roots...), StartedAt: now, FinishedAt: now, CensusContract: "bounded-process-census/v3"}
			if calls == 1 {
				e.Roots = nil
				e.Err = errors.New("fixture incomplete capture")
			}
			return e
		}, func() time.Time { return now })
		if out.ProcessComplete || len(out.FailureKinds) == 0 {
			t.Fatal("partial became complete or lost cause")
		}
		invariant := false
		for _, k := range out.FailureKinds {
			invariant = invariant || k == FailureInvariant
		}
		if invariant != fatal {
			t.Fatal("tracker failure confused with projection", fatal, out.FailureKinds)
		}
		b, err := json.Marshal(out)
		if err != nil {
			t.Fatal(err)
		}
		var wire map[string]any
		if err = json.Unmarshal(b, &wire); err != nil {
			t.Fatal(err)
		}
		if _, exists := wire["FailureKinds"]; exists {
			t.Fatal("internal classifier leaked into public wire")
		}
	}
}

func TestIncompleteProjectionNeverHidesTrackingOwnershipChange(t *testing.T) {
	for _, mode := range []string{"projection-add", "projection-remove", "same-pid-owner", "untracked", "missing-tracker-row", "foreign-extra", "duplicate"} {
		t.Run(mode, func(t *testing.T) {
			p, roots := fixture(t)
			before, after := append([]runtime.LiveRuntime{}, p.roots...), append([]runtime.LiveRuntime{}, p.roots...)
			previous, current := append([]proctable.ObservedRoot{}, roots...), append([]proctable.ObservedRoot{}, roots...)
			switch mode {
			case "projection-add":
				before, previous = before[:1], previous[:1]
			case "projection-remove":
				after, current = after[:1], current[:1]
			case "same-pid-owner":
				after[0].ProviderName = "different-owner"
			case "untracked":
				after[0].IsTracked = false
			case "missing-tracker-row":
				after = after[:1]
			case "foreign-extra":
				after[0].PID = 999
			case "duplicate":
				after = append(after, after[0])
			}
			if onlyTrackingProjectionChanged(before, after, previous, current) != (mode == "projection-add" || mode == "projection-remove") {
				t.Fatal("incorrect ownership provenance", mode)
			}
		})
	}
}
