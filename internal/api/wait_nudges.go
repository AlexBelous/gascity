package api

import (
	"github.com/gastownhall/gascity/internal/beads"
	nudgesdb "github.com/gastownhall/gascity/internal/classdb/nudges"
)

// withdrawQueuedWaitNudges withdraws the queued wait nudges with the given
// ids through the nudge-queue front door: the file backend (flock'd
// state.json plus shadow beads over the caller's strongly-typed
// beads.NudgesStore) until [beads.classes.nudges] relocates the class, the
// embedded class store afterwards (QueueForCity resolves the routing;
// fail-closed when a marked city's class store cannot be reached).
func withdrawQueuedWaitNudges(store beads.NudgesStore, cityPath string, ids []string) error {
	return nudgesdb.QueueForCity(cityPath, nil).WithdrawQueuedWaitNudges(store.Store, ids)
}
