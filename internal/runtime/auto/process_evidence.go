package auto

import (
	"fmt"
	"sort"

	"github.com/gastownhall/gascity/internal/runtime"
)

// TrackProcessRoots merges positive PID attribution without process rescans.
// Every configured backend must support this evidence seam.
func (p *Provider) TrackProcessRoots(roots []runtime.LiveRuntime) ([]runtime.LiveRuntime, error) {
	if p.defaultSP == nil {
		return nil, fmt.Errorf("default provider unavailable")
	}
	backends := []runtime.Provider{p.defaultSP, p.acpSP}
	merged := map[int]runtime.LiveRuntime{}
	input := map[int]runtime.LiveRuntime{}
	for _, r := range roots {
		r.IsTracked = false
		r.ProviderName = ""
		if _, dup := input[r.PID]; dup {
			return nil, fmt.Errorf("duplicate evidence PID")
		}
		input[r.PID] = r
	}
	for _, backend := range backends {
		if backend == nil {
			continue
		}
		tracker, ok := backend.(runtime.ProcessRootTracker)
		if !ok {
			return nil, fmt.Errorf("backend lacks positive process PID attribution")
		}
		rows, err := tracker.TrackProcessRoots(roots)
		if err != nil {
			return nil, fmt.Errorf("backend process attribution incomplete")
		}
		if len(rows) != len(roots) {
			return nil, fmt.Errorf("backend omitted process roots")
		}
		seen := map[int]bool{}
		for _, r := range rows {
			expected, exists := input[r.PID]
			normalized := r
			normalized.IsTracked = false
			normalized.ProviderName = ""
			if !exists || normalized != expected {
				return nil, fmt.Errorf("backend changed process identity")
			}
			if seen[r.PID] {
				return nil, fmt.Errorf("backend duplicate process PID")
			}
			seen[r.PID] = true
			old, exists := merged[r.PID]
			if exists && old.IsTracked && r.IsTracked && old.ProviderName != r.ProviderName {
				return nil, fmt.Errorf("multiple providers claim one process root")
			}
			if !exists || (r.IsTracked && !old.IsTracked) {
				merged[r.PID] = r
			}
		}
	}
	out := make([]runtime.LiveRuntime, 0, len(merged))
	for _, r := range merged {
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].PID < out[j].PID })
	return out, nil
}
