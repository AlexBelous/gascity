package main

import (
	"fmt"

	"github.com/gastownhall/gascity/internal/nudgequeue"
)

// legacyNudgeBacklogEvidence is a read-only startup inventory of the old
// state.json queue. Dead entries are retained history and do not own future
// effects; pending and in-flight entries must be drained or explicitly
// migrated before keyed ownership can start.
type legacyNudgeBacklogEvidence struct {
	Drained  bool
	Pending  int
	InFlight int
	Err      error
}

// inspectLegacyNudgeBacklog deliberately uses the read-only loader. It never
// takes the writer lock, creates queue files, expires leases, or repairs state:
// startup evidence must not mutate the backlog it is deciding whether to own.
func inspectLegacyNudgeBacklog(cityPath string) legacyNudgeBacklogEvidence {
	state, err := nudgequeue.LoadState(cityPath)
	evidence := legacyNudgeBacklogEvidence{Err: err}
	if err != nil {
		return evidence
	}
	evidence.Pending = len(state.Pending)
	evidence.InFlight = len(state.InFlight)
	evidence.Drained = evidence.Pending == 0 && evidence.InFlight == 0
	return evidence
}

func (e legacyNudgeBacklogEvidence) diagnostic() string {
	if e.Err != nil {
		return fmt.Sprintf("unreadable: %v", e.Err)
	}
	return fmt.Sprintf("%d pending and %d in-flight entries", e.Pending, e.InFlight)
}
