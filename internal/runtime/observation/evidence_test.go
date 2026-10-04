package observation

import (
	"errors"
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/runtime"
	"github.com/gastownhall/gascity/internal/runtime/proctable"
)

type evidenceProvider struct {
	*observedProvider
	trackingErr error
	tracks      int
	scans       int
}

func (p *evidenceProvider) TrackProcessRoots(_ []runtime.LiveRuntime) ([]runtime.LiveRuntime, error) {
	p.tracks++
	return append([]runtime.LiveRuntime{}, p.roots...), p.trackingErr
}

func (p *evidenceProvider) FindRuntimesBySessionID(string) ([]runtime.LiveRuntime, error) {
	p.scans++
	return nil, errors.New("unprivileged environment unavailable")
}

func TestObserveProcessEvidenceClosesSecondScanWithoutTrustBypass(t *testing.T) {
	p, roots := fixture(t)
	ep := &evidenceProvider{observedProvider: p}
	now := time.Date(2026, 10, 4, 9, 0, 0, 0, time.UTC)
	read := func() ProcessEvidence { return ProcessEvidence{Roots: roots, StartedAt: now, FinishedAt: now} }
	out := ObserveProcessEvidence("/city", ep, read, func() time.Time { return now })
	if !out.ProviderComplete || !out.ProcessComplete || ep.scans != 0 || ep.tracks != 2 {
		t.Fatalf("not coherent %+v scans=%d tracks=%d", out, ep.scans, ep.tracks)
	}
	ep.trackingErr = errors.New("live owner PID unavailable")
	out = ObserveProcessEvidence("/city", ep, read, func() time.Time { return now })
	if out.ProcessComplete {
		t.Fatal("tracker failure became authority")
	}
}

func TestObserveProcessEvidencePreservesOldestTimestampAndUnknown(t *testing.T) {
	for _, mode := range []string{"old", "future", "reverse", "missing tracker", "partial", "changed tracking"} {
		t.Run(mode, func(t *testing.T) {
			p, roots := fixture(t)
			ep := &evidenceProvider{observedProvider: p}
			var sp runtime.Provider = ep
			now := time.Date(2026, 10, 4, 9, 0, 0, 0, time.UTC)
			calls := 0
			read := func() ProcessEvidence {
				calls++
				e := ProcessEvidence{Roots: append([]proctable.ObservedRoot{}, roots...), StartedAt: now, FinishedAt: now}
				switch mode {
				case "old":
					e.StartedAt = now.Add(-61 * time.Second)
				case "future":
					e.FinishedAt = now.Add(time.Second)
				case "reverse":
					e.StartedAt = now.Add(time.Second)
				case "partial":
					e.Err = errors.New("partial")
				case "changed tracking":
					if calls > 1 {
						ep.roots = nil
					}
				}
				return e
			}
			if mode == "missing tracker" {
				sp = p
			}
			out := ObserveProcessEvidence("/city", sp, read, func() time.Time { return now })
			if out.ProcessComplete {
				t.Fatalf("%s became complete %+v", mode, out)
			}
			if mode == "old" && !out.ObservedAt.Equal(now.Add(-61*time.Second)) {
				t.Fatal("stale evidence rejuvenated")
			}
		})
	}
}
