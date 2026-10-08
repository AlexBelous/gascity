//go:build integration

package procobserver

import (
	"github.com/gastownhall/gascity/internal/runtime"
)

type joinedProvider struct {
	*runtime.Fake
	names       []string
	handles     []runtime.ProcessHandle
	trackingErr error
}

func (p *joinedProvider) ListRunning(string) ([]string, error) {
	return append([]string{}, p.names...), nil
}

func (p *joinedProvider) TrackProcessRoots(roots []runtime.LiveRuntime) ([]runtime.LiveRuntime, error) {
	if p.trackingErr != nil {
		return nil, p.trackingErr
	}
	return runtime.BindProcessEvidence(roots, p.handles)
}

// Serializes the actual Unix codec -> redacted root conversion -> positive live
// provider-PID attribution -> native observation. The external consumer receives
// these exact bytes, including errors; no lifecycle methods are invoked.
