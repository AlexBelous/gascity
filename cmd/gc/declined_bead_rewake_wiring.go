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

// dispatchDeclinedBeadRewakeCheck runs the declined-bead re-wake check
// without blocking its caller on lookup. lookup is typically backed by a `bd
// history` subprocess (noteAuthorLookupWithRunner) with no bounded context
// of its own, so a slow or hung subprocess must delay only this check's own
// deferred log line -- never the sequential wake loop that calls this
// per-target (session_reconciler.go).
func dispatchDeclinedBeadRewakeCheck(assignedByID map[string]beads.Bead, beadID string, lookup noteAuthorLookup, logf func(format string, args ...any)) {
	go checkDeclinedBeadRewakeOnWake(assignedByID, beadID, lookup, logf)
}
