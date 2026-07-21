package main

// Typed nudge-queue lifecycle events (infra-class-sqlite-stores design,
// Nudges section): nudge.queued on enqueue, nudge.delivered on an injection
// ack, nudge.dead on a delivery-failure dead-letter. They replace the
// incidental bead.* observability the shadow beads produced, so they fire
// for BOTH queue backends — they are queue-level facts, not store writes.
// Emission is best-effort via the city event log; a failure never blocks a
// queue operation.

import (
	"encoding/json"
	"io"

	"github.com/gastownhall/gascity/internal/events"
)

// recordNudgeLifecycleEvents appends one lifecycle event per item to the
// city event log. reasonFor lets the dead path carry per-item causes; nil
// means no reason.
func recordNudgeLifecycleEvents(cityPath, eventType, outcome string, reasonFor func(queuedNudge) string, items []queuedNudge) {
	if cityPath == "" || len(items) == 0 {
		return
	}
	rec := openCityRecorderAt(cityPath, io.Discard)
	defer func() {
		if closer, ok := rec.(io.Closer); ok {
			_ = closer.Close()
		}
	}()
	for _, item := range items {
		reason := ""
		if reasonFor != nil {
			reason = reasonFor(item)
		}
		payload, err := json.Marshal(events.NudgeLifecyclePayload{
			ID:      item.ID,
			Agent:   item.Agent,
			Source:  item.Source,
			Outcome: outcome,
			Reason:  reason,
		})
		if err != nil {
			continue
		}
		rec.Record(events.Event{
			Type:    eventType,
			Actor:   "nudge-queue",
			Subject: item.Agent,
			Payload: payload,
		})
	}
}

// recordNudgeQueuedEvents fires nudge.queued for freshly enqueued items.
func recordNudgeQueuedEvents(cityPath string, items ...queuedNudge) {
	recordNudgeLifecycleEvents(cityPath, events.NudgeQueued, "", nil, items)
}

// recordNudgeDeliveredEvents fires nudge.delivered with the ack outcome
// ("injected" | "accepted_for_injection").
func recordNudgeDeliveredEvents(cityPath, outcome string, items ...queuedNudge) {
	recordNudgeLifecycleEvents(cityPath, events.NudgeDelivered, outcome, nil, items)
}

// recordNudgeDeadEvents fires nudge.dead for dead-lettered items, carrying
// each item's recorded failure cause.
func recordNudgeDeadEvents(cityPath string, items ...queuedNudge) {
	recordNudgeLifecycleEvents(cityPath, events.NudgeDead, "", func(item queuedNudge) string {
		return item.LastError
	}, items)
}
