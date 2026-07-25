package main

// Work-unify quarantine enforcement (engdocs/design/beads-work-topology.md,
// "Unify migration" + Red-team pins): the unify copy stamps every migrated work
// row with the gc.topology_migrating LABEL, and that label makes the row
// invisible to the hook/ready and claim surfaces until the marker step clears
// it. This is the correctness guard for the CLI-one-shot window — a controller
// boot is boot-blocked on a failed unify, but a `gc hook`/`gc claim` one-shot
// runs without the controller, so an aborted copy's mid-flight rows must never
// be handed to an agent. The label is stamped atomically with the row (both
// snapshot import legs persist a snapshot's labels on write) and removed in bulk
// once re-point completes.
//
// The filter keys purely on the label's presence — a row is quarantined iff it
// carries the label — so an unlabeled/marker-less row always flows and the
// common (non-migrating) case is one cheap slice scan.

import (
	"encoding/json"
	"strings"

	"github.com/gastownhall/gascity/internal/beads"
)

const (
	// workTopologyMigratingLabel is the quarantine label the unify copy stamps
	// on every migrated work row. While present, the row is excluded from the
	// hook/ready and claim surfaces (beadIsTopologyQuarantined). The label is
	// removed in bulk after the marker-aware re-point converges every scope.
	workTopologyMigratingLabel = "gc.topology_migrating"

	// workTopologySourceMetadataKey stamps the originating city's identity on
	// every migrated work row (the remote collision discriminator the remote
	// slice's pre-probe reads). Written atomically with the row before import.
	workTopologySourceMetadataKey = "gc.topology_source"
)

// beadIsTopologyQuarantined reports whether a bead carries the unify quarantine
// label and must therefore be withheld from the hook/ready and claim surfaces
// until the marker step clears it.
func beadIsTopologyQuarantined(b beads.Bead) bool {
	for _, l := range b.Labels {
		if l == workTopologyMigratingLabel {
			return true
		}
	}
	return false
}

// filterTopologyQuarantined returns rows with every quarantined bead removed,
// reusing the input backing array (order preserved). It is the single ready/
// work-query seam: mergeGraphReadyIntoWorkQueryOutput calls it on the unioned
// candidate set so a mid-copy row is never surfaced as ready work.
func filterTopologyQuarantined(rows []beads.Bead) []beads.Bead {
	out := rows[:0]
	for _, b := range rows {
		if beadIsTopologyQuarantined(b) {
			continue
		}
		out = append(out, b)
	}
	return out
}

// filterTopologyQuarantinedWorkQueryOutput drops quarantined rows from a JSON
// work-query output string, UNCONDITIONALLY of graph-routing (F17/F21) — so a
// non-graph-routed city's shell output is also filtered. A count form or any
// non-bead-array output (which cannot express row-level filtering) passes through
// untouched.
func filterTopologyQuarantinedWorkQueryOutput(out string) string {
	trimmed := strings.TrimSpace(out)
	if trimmed == "" || !strings.HasPrefix(trimmed, "[") {
		return out
	}
	var rows []beads.Bead
	if err := json.Unmarshal([]byte(trimmed), &rows); err != nil {
		return out // not a bead array: pass through
	}
	filtered := filterTopologyQuarantined(rows)
	if len(filtered) == len(rows) {
		return out // nothing quarantined: preserve the original bytes verbatim
	}
	buf, err := json.Marshal(filtered)
	if err != nil {
		return out
	}
	return string(buf)
}
