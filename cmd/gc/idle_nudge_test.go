package main

import (
	"bytes"
	"context"
	"strconv"
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/beadmeta"
	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/config"
	"github.com/gastownhall/gascity/internal/runtime"
)

func idleClaimTestCfg() *config.City {
	return &config.City{Agents: []config.Agent{{
		Name:  "polecat",
		Nudge: "Run gc hook --claim --json now; if it returns work, execute the claimed formula immediately.",
	}}}
}

func idleClaimPoolSession() beads.Bead {
	return beads.Bead{
		ID:     "s-1",
		Status: "open",
		Type:   "session",
		Metadata: map[string]string{
			"session_name":                    "worker-1",
			"pool_managed":                    "true",
			"template":                        "polecat",
			beadmeta.TriggerBeadIDMetadataKey: "w-1",
		},
	}
}

func runningFake(t *testing.T) *runtime.Fake {
	t.Helper()
	sp := runtime.NewFake()
	if err := sp.Start(context.TODO(), "worker-1", runtime.Config{}); err != nil {
		t.Fatalf("fake start: %v", err)
	}
	return sp
}

// A slot handed work it never claimed (trigger bead still open) is observed on
// the first tick (grace), then nudged once the grace elapses.
func TestNudgeStalledPoolClaims_NudgesAfterGrace(t *testing.T) {
	sp := runningFake(t)
	cfg := idleClaimTestCfg()
	sessions := []beads.Bead{idleClaimPoolSession()}
	work := []beads.Bead{{ID: "w-1", Status: "open"}} // unclaimed
	store := beads.SessionStore{Store: beads.NewMemStoreFrom(0, sessions, nil)}
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	var out bytes.Buffer

	// First tick: observe only — start the grace clock, no nudge.
	nudgeStalledPoolClaims(sp, cfg, store, sessions, work, base, &out)
	if out.Len() != 0 {
		t.Fatalf("first tick should not nudge (grace): %q", out.String())
	}
	if got := sessions[0].Metadata[idleClaimNudgeTriggerKey]; got != "w-1" {
		t.Fatalf("expected marker trigger w-1, got %q", got)
	}

	// Past grace: nudge, and bump the attempt count.
	nudgeStalledPoolClaims(sp, cfg, store, sessions, work, base.Add(idleClaimNudgeGrace+time.Second), &out)
	if !bytes.Contains(out.Bytes(), []byte("nudged worker-1 to claim w-1")) {
		t.Fatalf("expected nudge past grace, got: %q", out.String())
	}
	if got := sessions[0].Metadata[idleClaimNudgeCountKey]; got != "1" {
		t.Fatalf("expected attempt count 1, got %q", got)
	}
}

// The instant a slot claims (trigger bead flips to in_progress) it must never be
// touched — this is the inversion that the reverted #312 nudger got wrong.
func TestNudgeStalledPoolClaims_NeverTouchesWorkingSlot(t *testing.T) {
	sp := runningFake(t)
	cfg := idleClaimTestCfg()
	sessions := []beads.Bead{idleClaimPoolSession()}
	work := []beads.Bead{{ID: "w-1", Status: "in_progress", Assignee: "worker-1"}}
	store := beads.SessionStore{Store: beads.NewMemStoreFrom(0, sessions, nil)}
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	var out bytes.Buffer

	nudgeStalledPoolClaims(sp, cfg, store, sessions, work, base, &out)
	nudgeStalledPoolClaims(sp, cfg, store, sessions, work, base.Add(time.Hour), &out)
	if out.Len() != 0 {
		t.Fatalf("must not nudge a working slot: %q", out.String())
	}
	if got := sessions[0].Metadata[idleClaimNudgeTriggerKey]; got != "" {
		t.Fatalf("marker should stay clear for a claimed bead, got %q", got)
	}
}

// After the attempt cap is reached the backstop gives up — bounded, never an
// every-tick loop.
func TestNudgeStalledPoolClaims_GivesUpAtCap(t *testing.T) {
	sp := runningFake(t)
	cfg := idleClaimTestCfg()
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	s := idleClaimPoolSession()
	s.Metadata[idleClaimNudgeTriggerKey] = "w-1"
	s.Metadata[idleClaimNudgeCountKey] = strconv.Itoa(idleClaimNudgeMaxAttempts)
	s.Metadata[idleClaimNudgeAtKey] = base.Format(time.RFC3339)
	sessions := []beads.Bead{s}
	work := []beads.Bead{{ID: "w-1", Status: "open"}}
	store := beads.SessionStore{Store: beads.NewMemStoreFrom(0, sessions, nil)}
	var out bytes.Buffer

	nudgeStalledPoolClaims(sp, cfg, store, sessions, work, base.Add(time.Hour), &out)
	if out.Len() != 0 {
		t.Fatalf("must not nudge past the attempt cap: %q", out.String())
	}
}

// A non-pool session is ignored entirely.
func TestNudgeStalledPoolClaims_SkipsNonPool(t *testing.T) {
	sp := runningFake(t)
	cfg := idleClaimTestCfg()
	s := idleClaimPoolSession()
	delete(s.Metadata, "pool_managed")
	sessions := []beads.Bead{s}
	work := []beads.Bead{{ID: "w-1", Status: "open"}}
	store := beads.SessionStore{Store: beads.NewMemStoreFrom(0, sessions, nil)}
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	var out bytes.Buffer

	nudgeStalledPoolClaims(sp, cfg, store, sessions, work, base, &out)
	nudgeStalledPoolClaims(sp, cfg, store, sessions, work, base.Add(time.Hour), &out)
	if out.Len() != 0 {
		t.Fatalf("must not touch a non-pool session: %q", out.String())
	}
}

// A ready routed bead is normal reconciler demand. When a warm pool slot is
// already running, the reconciler must deliver the claim prompt itself rather
// than depending on an event order to nudge the worker when routing happened.
func TestNudgeReadyRoutedPoolClaims_NudgesWarmPoolImmediately(t *testing.T) {
	sp := runningFake(t)
	cfg := idleClaimTestCfg()
	session := idleClaimPoolSession()
	delete(session.Metadata, beadmeta.TriggerBeadIDMetadataKey)
	sessions := []beads.Bead{session}
	store := beads.SessionStore{Store: beads.NewMemStoreFrom(0, sessions, nil)}
	ready := map[string]scaleCheckDemand{
		"polecat": {WorkBeadIDs: []string{"w-ready", "w-next"}},
	}
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	var out bytes.Buffer

	nudgeReadyRoutedPoolClaims(sp, cfg, store, sessions, ready, map[string]bool{"w-ready": true, "w-next": true}, base, &out)

	if !bytes.Contains(out.Bytes(), []byte("nudged worker-1 to claim w-ready")) {
		t.Fatalf("expected immediate reconciler nudge, got: %q", out.String())
	}
	if got := sessions[0].Metadata[idleClaimNudgeTriggerKey]; got != "w-ready" {
		t.Fatalf("marker trigger = %q, want w-ready", got)
	}
	if got := sessions[0].Metadata[idleClaimNudgeCountKey]; got != "1" {
		t.Fatalf("marker count = %q, want 1", got)
	}

	// The same ready demand on a later patrol is already acknowledged by the
	// persisted marker, so it must not inject a duplicate prompt.
	nudgeReadyRoutedPoolClaims(sp, cfg, store, sessions, ready, map[string]bool{"w-ready": true, "w-next": true}, base.Add(time.Minute), &out)
	if got := bytes.Count(out.Bytes(), []byte("nudged worker-1 to claim w-ready")); got != 1 {
		t.Fatalf("ready routed work was nudged %d times, want exactly once: %q", got, out.String())
	}
	if bytes.Contains(out.Bytes(), []byte("nudged worker-1 to claim w-next")) {
		t.Fatalf("slot was prompted for a second item before claiming the first: %q", out.String())
	}
}

func TestNudgeReadyRoutedPoolClaims_SkipsBusyAndBlockedDemand(t *testing.T) {
	sp := runningFake(t)
	cfg := idleClaimTestCfg()
	session := idleClaimPoolSession()
	delete(session.Metadata, beadmeta.TriggerBeadIDMetadataKey)
	session.Metadata["currently_processing_bead_id"] = "w-active"
	sessions := []beads.Bead{session}
	store := beads.SessionStore{Store: beads.NewMemStoreFrom(0, sessions, nil)}
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	var out bytes.Buffer

	// The readiness snapshot is empty when the routed work is still blocked, so
	// no prompt is emitted merely because a route exists.
	nudgeReadyRoutedPoolClaims(sp, cfg, store, sessions, nil, nil, base, &out)
	if out.Len() != 0 {
		t.Fatalf("blocked routed work must not nudge: %q", out.String())
	}

	// A ready queued item also must not interrupt a slot with different active
	// work. It will be handed to another compatible slot or wait for this one to
	// return to the idle pool.
	nudgeReadyRoutedPoolClaims(sp, cfg, store, sessions, map[string]scaleCheckDemand{
		"polecat": {WorkBeadIDs: []string{"w-ready"}},
	}, map[string]bool{"w-ready": true}, base, &out)
	if out.Len() != 0 {
		t.Fatalf("busy pool slot must not be nudged: %q", out.String())
	}
}

func TestNudgeReadyAssignedSessionClaims_NudgesReadySessionWorkOnce(t *testing.T) {
	sp := runningFake(t)
	cfg := idleClaimTestCfg()
	session := idleClaimPoolSession()
	delete(session.Metadata, beadmeta.TriggerBeadIDMetadataKey)
	sessions := []beads.Bead{session}
	store := beads.SessionStore{Store: beads.NewMemStoreFrom(0, sessions, nil)}
	work := []beads.Bead{
		{ID: "w-assigned", Status: "open", Assignee: "worker-1"},
		{ID: "w-next", Status: "open", Assignee: "worker-1"},
	}
	ready := map[storeScopedBeadKey]bool{
		{StoreRef: "", ID: "w-assigned"}: true,
		{StoreRef: "", ID: "w-next"}:     true,
	}
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	var out bytes.Buffer

	nudgeReadyAssignedSessionClaims(sp, cfg, store, sessions, work, nil, ready, map[string]bool{"w-assigned": true, "w-next": true}, base, &out)
	if !bytes.Contains(out.Bytes(), []byte("nudged worker-1 to claim w-assigned")) {
		t.Fatalf("expected ready assigned nudge, got: %q", out.String())
	}

	// A second ready item cannot cause a second immediate prompt before the
	// session has claimed the first one.
	nudgeReadyAssignedSessionClaims(sp, cfg, store, sessions, work, nil, ready, map[string]bool{"w-assigned": true, "w-next": true}, base.Add(time.Minute), &out)
	if bytes.Contains(out.Bytes(), []byte("nudged worker-1 to claim w-next")) {
		t.Fatalf("session received a second claim prompt before claiming the first: %q", out.String())
	}
}

func TestNudgeReadyAssignedSessionClaims_SkipsBlockedAndBusyWork(t *testing.T) {
	sp := runningFake(t)
	cfg := idleClaimTestCfg()
	session := idleClaimPoolSession()
	delete(session.Metadata, beadmeta.TriggerBeadIDMetadataKey)
	session.Metadata["currently_processing_bead_id"] = "w-active"
	sessions := []beads.Bead{session}
	store := beads.SessionStore{Store: beads.NewMemStoreFrom(0, sessions, nil)}
	work := []beads.Bead{{ID: "w-assigned", Status: "open", Assignee: "worker-1"}}
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	var out bytes.Buffer

	// Missing ReadyAssigned proof represents an open-but-blocked assignment.
	nudgeReadyAssignedSessionClaims(sp, cfg, store, sessions, work, nil, nil, nil, base, &out)
	if out.Len() != 0 {
		t.Fatalf("blocked assigned work must not nudge: %q", out.String())
	}
	nudgeReadyAssignedSessionClaims(sp, cfg, store, sessions, work, nil, map[storeScopedBeadKey]bool{
		{StoreRef: "", ID: "w-assigned"}: true,
	}, map[string]bool{"w-assigned": true}, base, &out)
	if out.Len() != 0 {
		t.Fatalf("busy session must not be nudged: %q", out.String())
	}
}

func TestReadyClaimNudges_ShareOnePendingPromptPerSession(t *testing.T) {
	sp := runningFake(t)
	cfg := idleClaimTestCfg()
	session := idleClaimPoolSession()
	delete(session.Metadata, beadmeta.TriggerBeadIDMetadataKey)
	sessions := []beads.Bead{session}
	store := beads.SessionStore{Store: beads.NewMemStoreFrom(0, sessions, nil)}
	assigned := []beads.Bead{{ID: "w-assigned", Status: "open", Assignee: "worker-1"}}
	readyAssigned := map[storeScopedBeadKey]bool{{StoreRef: "", ID: "w-assigned"}: true}
	routed := map[string]scaleCheckDemand{"polecat": {WorkBeadIDs: []string{"w-routed"}}}
	readyClaims := readyClaimWorkIDs(routed, assigned, nil, readyAssigned)
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	var out bytes.Buffer

	nudgeReadyRoutedPoolClaims(sp, cfg, store, sessions, routed, readyClaims, base, &out)
	nudgeReadyAssignedSessionClaims(sp, cfg, store, sessions, assigned, nil, readyAssigned, readyClaims, base, &out)
	if bytes.Contains(out.Bytes(), []byte("nudged worker-1 to claim w-assigned")) {
		t.Fatalf("assigned claim nudge duplicated a pending routed prompt: %q", out.String())
	}
}
