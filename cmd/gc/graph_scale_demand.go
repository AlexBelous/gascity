package main

// Pool-demand federation for the graph class: the controller's desired-state
// demand pass probes each pool template's scope stores (city/rig) for ready,
// unassigned, routed work — all graph-blind once molecule roots and steps
// relocate. On a graph-routed city every template gains one extra probe
// target backed by the embedded graph store, so routed demand delivered as
// graph beads keeps waking pools and named sessions. The counted-bead dedup
// in defaultScaleCheckCountsAndDemand already guards cross-store unions, and
// a routing failure surfaces as a per-template partial (fail-visible) rather
// than silently reading zero demand.

import (
	"fmt"

	"github.com/gastownhall/gascity/internal/config"
)

// graphScaleTargetStoreKey groups all graph-store probes into one scale
// store group (one Ready read per pass).
const graphScaleTargetStoreKey = "graph"

// appendGraphScaleTargets appends a graph-store demand target per distinct
// template on a graph-routed city. Unrouted cities return targets unchanged.
func appendGraphScaleTargets(targets []defaultScaleCheckTarget, cityPath string, cfg *config.City) []defaultScaleCheckTarget {
	if len(targets) == 0 {
		return targets
	}
	st, routed, err := routedGraphStoreFor(cityPath, cfg)
	if err == nil && !routed {
		return targets
	}
	seen := make(map[string]bool, len(targets))
	out := targets
	for _, t := range targets {
		if t.template == "" || seen[t.template] {
			continue
		}
		seen[t.template] = true
		g := defaultScaleCheckTarget{template: t.template, storeKey: graphScaleTargetStoreKey}
		if err != nil {
			g.err = fmt.Errorf("default scale_check %s: graph-class routing: %w", t.template, err)
		} else {
			g.store = st
		}
		out = append(out, g)
	}
	return out
}
