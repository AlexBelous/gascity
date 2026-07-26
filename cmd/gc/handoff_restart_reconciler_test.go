package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/config"
	"github.com/gastownhall/gascity/internal/events"
	sessionpkg "github.com/gastownhall/gascity/internal/session"
	"github.com/gastownhall/gascity/internal/session/sessiontest"
)

// TestReconcileSessionBeads_RecordsHandoffRestartNoEffectDiagnostic covers the
// tier-2 runtime detector: when a handoff armed a restart claim, the grace
// period (startupTimeout) has elapsed, and the session's generation/
// awake_started_at identity is unchanged from the claimed baseline, the
// reconciler must publish a HandoffRestartNoEffect event exactly once,
// record a matching trace decision, debounce a duplicate pass, and be
// willing to fire again after the claim is cleared and re-armed.
func TestReconcileSessionBeads_RecordsHandoffRestartNoEffectDiagnostic(t *testing.T) {
	env := newReconcilerTestEnv()
	rec := events.NewFake()
	env.rec = rec
	env.cfg = &config.City{
		Workspace: config.Workspace{Name: "test-city"},
		Agents:    []config.Agent{{Name: "worker", StartCommand: "true", MaxActiveSessions: intPtr(2)}},
		Session:   config.SessionConfig{StartupTimeout: "60s"},
	}
	env.addDesired("worker", "worker", false)
	session := env.createSessionBead("worker", "worker")

	claimedAt := env.clk.Now().Add(-75 * time.Second).UTC()
	baseline := handoffRestartIdentity{Generation: "1", AwakeStartedAt: "2026-03-08T00:00:00Z"}
	claim := handoffRestartClaim{Mode: "self", Baseline: baseline, ClaimedAt: claimedAt}
	claimJSON, err := json.Marshal(claim)
	if err != nil {
		t.Fatalf("marshal handoff restart claim: %v", err)
	}
	env.setSessionMetadata(&session, map[string]string{
		"awake_started_at":                baseline.AwakeStartedAt,
		"restart_requested":               "true",
		sessionpkg.HandoffRestartClaimKey: string(claimJSON),
	})

	tracer := newSessionReconcilerTracer(t.TempDir(), "test-city", io.Discard)
	t.Cleanup(func() { _ = tracer.Close() })
	tracer.detail = map[string]TraceSource{"worker": TraceSourceManual}
	trace := tracer.BeginCycle(TraceTickTriggerPatrol, "", env.clk.Now().UTC(), env.cfg)

	reconcileSessionBeadsTraced(
		context.Background(), "", []beads.Bead{session}, env.desiredState,
		configuredSessionNames(env.cfg, "", env.store), env.cfg, env.sp, env.store,
		nil, nil, nil, nil, env.dt, map[string]int{"worker": 0}, false, nil,
		"test-city", nil, env.clk, rec, env.cfg.Session.StartupTimeoutDuration(), 0,
		&env.stdout, &env.stderr, trace,
	)

	wantMessage := fmt.Sprintf(
		"session reconciler: handoff restart had no effect for worker: elapsed_s=75 mode=self bead_id=%s",
		session.ID,
	)
	if got := strings.TrimSpace(env.stderr.String()); got != wantMessage {
		t.Fatalf("stderr = %q, want %q", got, wantMessage)
	}
	if len(rec.Events) != 1 {
		t.Fatalf("recorded events = %d, want 1: %#v", len(rec.Events), rec.Events)
	}
	gotEvent := rec.Events[0]
	if gotEvent.Type != events.HandoffRestartNoEffect {
		t.Fatalf("event type = %q, want %q", gotEvent.Type, events.HandoffRestartNoEffect)
	}
	var payload events.HandoffRestartNoEffectPayload
	if err := json.Unmarshal(gotEvent.Payload, &payload); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	if payload.SessionID != session.ID || payload.SessionName != "worker" || payload.Mode != "self" ||
		payload.BeforeGeneration != "1" || payload.BeforeAwakeStartedAt != baseline.AwakeStartedAt ||
		payload.AfterGeneration != "1" || payload.AfterAwakeStartedAt != baseline.AwakeStartedAt ||
		payload.RestartMarkerState != "pending" || payload.ElapsedSeconds != 75 {
		t.Fatalf("payload = %+v, want no-effect diagnostic for worker/self, before==after identity, restart_marker_state=pending, elapsed_s=75", payload)
	}

	foundTrace := false
	if trace != nil {
		for _, r := range trace.records {
			if r.SiteCode == TraceSiteReconcilerHandoffRestartNoEffect && r.ReasonCode == TraceReasonHandoffRestartNoEffect &&
				r.OutcomeCode == TraceOutcomeFailed && r.Template == "worker" && r.SessionName == "worker" {
				foundTrace = true
				break
			}
		}
	}
	if !foundTrace {
		t.Fatalf("handoff restart no-effect trace decision not recorded; records=%+v", trace.records)
	}

	env.stderr.Reset()
	recordHandoffRestartNoEffectIfDue(sessiontest.SeedBead(t, session), "worker", "worker", env.cfg.Session.StartupTimeoutDuration(), env.clk.Now().UTC(), env.dt, rec, &env.stderr, trace)
	if got := strings.TrimSpace(env.stderr.String()); got != "" {
		t.Fatalf("second no-effect pass stderr = %q, want debounce silence", got)
	}
	if len(rec.Events) != 1 {
		t.Fatalf("recorded events after duplicate pass = %d, want 1", len(rec.Events))
	}

	env.setSessionMetadata(&session, map[string]string{sessionpkg.HandoffRestartClaimKey: ""})
	recordHandoffRestartNoEffectIfDue(sessiontest.SeedBead(t, session), "worker", "worker", env.cfg.Session.StartupTimeoutDuration(), env.clk.Now().UTC(), env.dt, rec, &env.stderr, trace)
	env.setSessionMetadata(&session, map[string]string{sessionpkg.HandoffRestartClaimKey: string(claimJSON)})
	env.stderr.Reset()
	recordHandoffRestartNoEffectIfDue(sessiontest.SeedBead(t, session), "worker", "worker", env.cfg.Session.StartupTimeoutDuration(), env.clk.Now().UTC(), env.dt, rec, &env.stderr, trace)
	if got := strings.TrimSpace(env.stderr.String()); got != wantMessage {
		t.Fatalf("re-armed pass stderr = %q, want %q", got, wantMessage)
	}
	if len(rec.Events) != 2 {
		t.Fatalf("recorded events after clear/re-arm = %d, want 2", len(rec.Events))
	}
}
