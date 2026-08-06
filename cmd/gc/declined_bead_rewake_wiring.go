package main

import "github.com/gastownhall/gascity/internal/beads"

// checkDeclinedBeadRewakeOnWake resolves beadID against this tick's
// assigned-work-bead snapshot and, if found, runs the declined-bead re-wake
// detector (gm-6a5dt) against it. A missing or empty beadID is a silent
// no-op: not every wake decision carries an assigned work bead, and this
// diagnostic must never gate or alter the wake path that calls it.
func checkDeclinedBeadRewakeOnWake(assignedByID map[string]beads.Bead, beadID string, lookup noteAuthorLookup, logf func(format string, args ...any)) {
	if beadID == "" {
		return
	}
	b, ok := assignedByID[beadID]
	if !ok {
		return
	}
	logDeclinedBeadRewakeIfDetected(b, lookup, logf)
}
