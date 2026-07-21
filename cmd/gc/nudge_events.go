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
	"path/filepath"

	"github.com/gastownhall/gascity/internal/events"
)

// recordNudgeLifecycleEvents appends one lifecycle event per item to the
// city event log (one recorder open per batch). reasonFor lets the dead
// path carry per-item causes; nil means no reason.
//
// The recorder is opened DIRECTLY on the event log with default rotation
// options — deliberately not openCityRecorderAt, whose loadCityConfig runs
// a full pack-expansion parse per call. These emissions sit on delivery
// paths (every enqueue before the wake ping; the controller's per-ready-wait
// dispatch loop; drain/poller acks), where the hook-emission norm (#2099,
// fastEventsProviderName) forbids config loads. Matches the ad-hoc
// direct-open precedent in bd_env.go / dolt_project_id.go / cmd_hook_claim's
// recorder use; [events] rotation overrides do not apply to these appends.
func recordNudgeLifecycleEvents(cityPath, eventType, outcome string, reasonFor func(queuedNudge) string, items []queuedNudge) {
	if cityPath == "" || len(items) == 0 {
		return
	}
	rec, err := events.NewFileRecorder(filepath.Join(cityPath, ".gc", "events.jsonl"), io.Discard)
	if err != nil {
		return
	}
	defer rec.Close() //nolint:errcheck // best-effort close
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
