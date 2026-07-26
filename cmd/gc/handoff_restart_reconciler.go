package main

import (
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/gastownhall/gascity/internal/events"
	sessionpkg "github.com/gastownhall/gascity/internal/session"
)

// recordHandoffRestartNoEffectIfDue is the tier-2 runtime detector for a
// handoff-requested restart that never rotated the session's identity. When a
// handoff arms a handoff_restart_claim baseline and the configured startup
// grace period elapses with the session's generation/awake_started_at
// identity still identical to that baseline, this publishes a
// HandoffRestartNoEffect event (deduped per bead ID via dt) so operators can
// tell a restart that silently did nothing from one that worked. A changed
// identity clears the dedup tracker so a later re-armed claim can fire again.
func recordHandoffRestartNoEffectIfDue(
	info sessionpkg.Info,
	template string,
	name string,
	startupTimeout time.Duration,
	now time.Time,
	dt *drainTracker,
	rec events.Recorder,
	stderr io.Writer,
	trace *sessionReconcilerTraceCycle,
) {
	claim, ok := handoffRestartClaimFromInfo(info)
	if !ok {
		if dt != nil {
			dt.clearHandoffRestartNoEffect(info.ID)
		}
		return
	}
	if startupTimeout <= 0 {
		return
	}
	elapsed := now.Sub(claim.ClaimedAt)
	if elapsed <= startupTimeout {
		return
	}
	current := handoffRestartIdentityFromInfo(info)
	if current != claim.Baseline {
		if dt != nil {
			dt.clearHandoffRestartNoEffect(info.ID)
		}
		return
	}
	if dt != nil && !dt.markHandoffRestartNoEffect(info.ID) {
		return
	}
	if stderr == nil {
		stderr = io.Discard
	}
	elapsedSeconds := int(elapsed / time.Second)
	msg := fmt.Sprintf(
		"session reconciler: handoff restart had no effect for %s: elapsed_s=%d mode=%s bead_id=%s",
		name, elapsedSeconds, claim.Mode, info.ID,
	)
	fmt.Fprintln(stderr, msg) //nolint:errcheck

	restartMarkerState := "cleared"
	if strings.TrimSpace(info.RestartRequested) == "true" {
		restartMarkerState = "pending"
	}

	if rec != nil {
		rec.Record(events.Event{
			Type:      events.HandoffRestartNoEffect,
			Actor:     "gc",
			Subject:   name,
			Message:   msg,
			SessionID: info.ID,
			Payload: events.HandoffRestartNoEffectPayloadJSON(events.HandoffRestartNoEffectPayload{
				SessionID:            info.ID,
				SessionName:          name,
				Mode:                 claim.Mode,
				BeforeGeneration:     claim.Baseline.Generation,
				BeforeAwakeStartedAt: claim.Baseline.AwakeStartedAt,
				AfterGeneration:      current.Generation,
				AfterAwakeStartedAt:  current.AwakeStartedAt,
				RestartMarkerState:   restartMarkerState,
				Reason:               "handoff_restart_no_effect",
				ElapsedSeconds:       elapsedSeconds,
			}),
		})
	}
	if trace != nil {
		trace.RecordDecision(
			TraceSiteReconcilerHandoffRestartNoEffect, TraceReasonHandoffRestartNoEffect, TraceOutcomeFailed,
			template, name,
			traceRecordPayload{"bead_id": info.ID, "elapsed_s": elapsedSeconds, "mode": claim.Mode},
		)
	}
}
