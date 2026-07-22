package main

// Nudges-class backend routing, cmd/gc side. The queue itself routes inside
// nudgesdb.QueueForCity (cityNudgeQueue delegates there); this file carries
// the two cmd/gc-shaped seams that sit NEXT to the queue: the wait paths'
// shadow reads (Find / FindIncludingTerminal over the shadow bead on bd,
// FindRecord* over the merged row when routed) and the nudge-mail sweep's
// nudge leg (stale shadow-bead closes on bd, merged-queue terminal-row
// retention when routed). Both fail CLOSED on a routed city whose class
// store cannot be reached — a silent bd fallback would split the class.

import (
	"time"

	"github.com/gastownhall/gascity/internal/beads"
	messagingdb "github.com/gastownhall/gascity/internal/classdb/messaging"
	nudgesdb "github.com/gastownhall/gascity/internal/classdb/nudges"
	"github.com/gastownhall/gascity/internal/config"
	"github.com/gastownhall/gascity/internal/nudgequeue"
)

// nudgeTerminalRetentionTTL is how long terminal nudge rows are retained in
// the routed class store before the retention sweep deletes them (the
// design's terminal-row TTL; the 1h dead-bucket behavior is preserved
// upstream of it via nudgequeue.DeadRetention).
const nudgeTerminalRetentionTTL = 24 * time.Hour

// nudgeShadowReader is the wait paths' read surface over a nudge's durable
// record: the shadow bead on the bd backend (*nudgequeue.Store satisfies it
// structurally), the merged queue row when the class is routed.
type nudgeShadowReader interface {
	Find(nudgeID string) (nudgequeue.NudgeShadow, bool, error)
	FindIncludingTerminal(nudgeID string) (nudgequeue.NudgeShadow, bool, error)
}

// routedNudgeShadowReader serves the wait paths' shadow reads from the
// merged-queue rows of the routed class store.
type routedNudgeShadowReader struct {
	class *nudgesdb.Store
}

func (r routedNudgeShadowReader) Find(nudgeID string) (nudgequeue.NudgeShadow, bool, error) {
	rec, ok, err := r.class.FindRecord(nudgeID)
	if err != nil || !ok {
		return nudgequeue.NudgeShadow{}, ok, err
	}
	return rec.Shadow(), true, nil
}

func (r routedNudgeShadowReader) FindIncludingTerminal(nudgeID string) (nudgequeue.NudgeShadow, bool, error) {
	rec, ok, err := r.class.FindRecordIncludingTerminal(nudgeID)
	if err != nil || !ok {
		return nudgequeue.NudgeShadow{}, ok, err
	}
	return rec.Shadow(), true, nil
}

// nudgeShadowReaderFor resolves the wait paths' shadow reader for a city:
// the routed class store when [beads.classes.nudges] has relocated the class
// (fail-closed on resolve/open errors), else the shadow-bead front door over
// the caller's nudges-class store. cfg may be nil; a marked city then loads
// its config from disk inside nudgesdb.Routed.
func nudgeShadowReaderFor(cityPath string, cfg *config.City, nudges beads.NudgesStore) (nudgeShadowReader, error) {
	routed, err := nudgesdb.Routed(cityPath, cfg)
	if err != nil {
		return nil, err
	}
	if !routed {
		return nudgeFrontDoor(nudges), nil
	}
	class, err := nudgesdb.SharedStoreFor(cityPath)
	if err != nil {
		return nil, err
	}
	return routedNudgeShadowReader{class: class}, nil
}

// nudgeSweepRouting carries the nudge-mail sweep's per-leg backend
// decisions: a nil class means the bd shadow sweep for the nudge leg
// (today's shape, the fixed shape the unrouted helpers hand to tests), and a
// nil mailClass means the bd read-mail sweep for the mail leg; the non-nil
// forms route each leg to its embedded class store.
type nudgeSweepRouting struct {
	class     *nudgesdb.Store
	mailClass *messagingdb.Store
}

// nudgeSweepRoutingFor resolves a city's nudge-sweep routing for BOTH legs.
// Inactive routing costs one marker stat per class. Active routing opens the
// class store; the error is returned rather than falling back to the bd
// sweep, because a bd sweep on a migrated city would run against residue
// instead of the class.
func nudgeSweepRoutingFor(cityPath string, cfg *config.City) (nudgeSweepRouting, error) {
	routing := nudgeSweepRouting{}
	routed, err := nudgesdb.Routed(cityPath, cfg)
	if err != nil {
		return nudgeSweepRouting{}, err
	}
	if routed {
		class, err := nudgesdb.SharedStoreFor(cityPath)
		if err != nil {
			return nudgeSweepRouting{}, err
		}
		routing.class = class
	}
	mailRouting, err := messagingRoutingFor(cityPath, cfg)
	if err != nil {
		return nudgeSweepRouting{}, err
	}
	routing.mailClass = mailRouting.class
	return routing, nil
}
