package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/events"
	"github.com/gastownhall/gascity/internal/runtime"
	"github.com/gastownhall/gascity/internal/session"
)

// TestDoHandoffWithOutcome_ClaimingRestartArmsHandoffRestartClaim covers the
// general (non-pinned) restartable branch of doHandoffWithOutcome: once a
// restart is actually requested, the session's pre-restart identity
// (generation/awake_started_at) must be captured as a handoff_restart_claim
// baseline so the reconciler can later tell whether the restart took effect.
func TestDoHandoffWithOutcome_ClaimingRestartArmsHandoffRestartClaim(t *testing.T) {
	store := beads.NewMemStore()
	rec := events.NewFake()
	dops := newFakeDrainOps()
	var stdout, stderr bytes.Buffer

	b, err := store.Create(beads.Bead{
		Type:   sessionBeadType,
		Labels: []string{"gc:session"},
	})
	if err != nil {
		t.Fatalf("seeding session bead: %v", err)
	}
	for key, value := range map[string]string{
		"session_name":             "mayor",
		"configured_named_session": "true",
		"configured_named_mode":    "always",
		"generation":               "3",
		"awake_started_at":         "2026-07-24T00:00:00Z",
	} {
		if err := store.SetMetadata(b.ID, key, value); err != nil {
			t.Fatalf("set %s: %v", key, err)
		}
	}

	before := time.Now().UTC()
	outcome := doHandoffWithOutcome(store, store, rec, dops, func() error { return nil },
		"mayor", "mayor", []string{"HANDOFF: context full"}, &stdout, &stderr)
	after := time.Now().UTC()
	if outcome.code != 0 {
		t.Fatalf("code = %d, want 0; stderr: %s", outcome.code, stderr.String())
	}
	if !outcome.restartRequested {
		t.Fatal("restartRequested = false, want true")
	}

	refreshed, err := store.Get(b.ID)
	if err != nil {
		t.Fatalf("fetching seeded bead: %v", err)
	}
	raw := refreshed.Metadata[session.HandoffRestartClaimKey]
	if raw == "" {
		t.Fatal("handoff_restart_claim metadata not set; want an armed claim")
	}
	var claim handoffRestartClaim
	if err := json.Unmarshal([]byte(raw), &claim); err != nil {
		t.Fatalf("decoding handoff_restart_claim: %v", err)
	}
	if claim.Mode != "self" {
		t.Errorf("claim.Mode = %q, want %q", claim.Mode, "self")
	}
	if claim.Baseline.Generation != "3" || claim.Baseline.AwakeStartedAt != "2026-07-24T00:00:00Z" {
		t.Errorf("claim.Baseline = %+v, want generation=3 awake_started_at=2026-07-24T00:00:00Z", claim.Baseline)
	}
	if claim.ClaimedAt.Before(before) || claim.ClaimedAt.After(after) {
		t.Errorf("claim.ClaimedAt = %v, want between %v and %v", claim.ClaimedAt, before, after)
	}
}

// TestDoHandoffWithOutcome_PinnedSuccessfulPersistArmsHandoffRestartClaim
// covers the pinned named-session branch: persistRestart is mandatory there,
// and once it succeeds the claim must be armed just as in the general case.
func TestDoHandoffWithOutcome_PinnedSuccessfulPersistArmsHandoffRestartClaim(t *testing.T) {
	store := beads.NewMemStore()
	rec := events.NewFake()
	dops := newFakeDrainOps()
	var stdout, stderr bytes.Buffer

	b, err := store.Create(beads.Bead{
		Type:   sessionBeadType,
		Labels: []string{"gc:session"},
	})
	if err != nil {
		t.Fatalf("seeding session bead: %v", err)
	}
	for key, value := range map[string]string{
		"session_name":             "mayor",
		"configured_named_session": "true",
		"configured_named_mode":    "always",
		"pin_awake":                "true",
		"generation":               "8",
		"awake_started_at":         "2026-07-23T00:00:00Z",
	} {
		if err := store.SetMetadata(b.ID, key, value); err != nil {
			t.Fatalf("set %s: %v", key, err)
		}
	}

	persistCalled := false
	outcome := doHandoffWithOutcome(store, store, rec, dops, func() error {
		persistCalled = true
		return nil
	}, "mayor", "mayor", []string{"HANDOFF: context full"}, &stdout, &stderr)
	if outcome.code != 0 {
		t.Fatalf("code = %d, want 0; stderr: %s", outcome.code, stderr.String())
	}
	if !outcome.restartRequested {
		t.Fatal("restartRequested = false, want true for pinned session with successful persist")
	}
	if !persistCalled {
		t.Fatal("persistRestart was not called")
	}

	refreshed, err := store.Get(b.ID)
	if err != nil {
		t.Fatalf("fetching seeded bead: %v", err)
	}
	raw := refreshed.Metadata[session.HandoffRestartClaimKey]
	if raw == "" {
		t.Fatal("handoff_restart_claim metadata not set; want an armed claim for pinned success")
	}
	var claim handoffRestartClaim
	if err := json.Unmarshal([]byte(raw), &claim); err != nil {
		t.Fatalf("decoding handoff_restart_claim: %v", err)
	}
	if claim.Mode != "self" {
		t.Errorf("claim.Mode = %q, want %q", claim.Mode, "self")
	}
	if claim.Baseline.Generation != "8" || claim.Baseline.AwakeStartedAt != "2026-07-23T00:00:00Z" {
		t.Errorf("claim.Baseline = %+v, want generation=8 awake_started_at=2026-07-23T00:00:00Z", claim.Baseline)
	}
}

// TestDoHandoffWithOutcome_OnDemandSkipDoesNotArmHandoffRestartClaim covers
// the named on-demand skip branch: no restart is requested, so no claim may
// be armed, and any stale claim left over from a prior handoff must be
// cleared alongside restart_requested/continuation_reset_pending.
func TestDoHandoffWithOutcome_OnDemandSkipDoesNotArmHandoffRestartClaim(t *testing.T) {
	store := beads.NewMemStore()
	rec := events.NewFake()
	dops := newFakeDrainOps()
	var stdout, stderr bytes.Buffer

	staleClaim := handoffRestartClaim{
		Mode:      "self",
		Baseline:  handoffRestartIdentity{Generation: "1", AwakeStartedAt: "2026-07-01T00:00:00Z"},
		ClaimedAt: time.Now().Add(-time.Hour).UTC(),
	}
	staleRaw, err := json.Marshal(staleClaim)
	if err != nil {
		t.Fatalf("marshal stale claim: %v", err)
	}

	b, err := store.Create(beads.Bead{
		Type:   sessionBeadType,
		Labels: []string{"gc:session"},
	})
	if err != nil {
		t.Fatalf("seeding session bead: %v", err)
	}
	for key, value := range map[string]string{
		"session_name":                 "mayor",
		"configured_named_session":     "true",
		"configured_named_mode":        "on_demand",
		"restart_requested":            "true",
		"continuation_reset_pending":   "true",
		session.HandoffRestartClaimKey: string(staleRaw),
	} {
		if err := store.SetMetadata(b.ID, key, value); err != nil {
			t.Fatalf("set %s: %v", key, err)
		}
	}
	dops.restartRequested["mayor"] = true

	outcome := doHandoffWithOutcome(store, store, rec, dops, func() error { return nil },
		"mayor", "mayor", []string{"HANDOFF: context full"}, &stdout, &stderr)
	if outcome.code != 0 {
		t.Fatalf("code = %d, want 0; stderr: %s", outcome.code, stderr.String())
	}
	if outcome.restartRequested {
		t.Fatal("restartRequested = true, want false for on-demand named session")
	}

	refreshed, err := store.Get(b.ID)
	if err != nil {
		t.Fatalf("fetching seeded bead: %v", err)
	}
	if refreshed.Metadata[session.HandoffRestartClaimKey] != "" {
		t.Errorf("handoff_restart_claim = %q, want cleared for named on-demand session", refreshed.Metadata[session.HandoffRestartClaimKey])
	}
}

// TestDoHandoffWithOutcome_PinFailureSkipDoesNotArmHandoffRestartClaim covers
// Fix 1's failure modes (nil or erroring persistRestart for a pinned
// session): doHandoffWithOutcome must fail the handoff outright, and must
// never arm a claim it cannot back with a persisted restart marker.
func TestDoHandoffWithOutcome_PinFailureSkipDoesNotArmHandoffRestartClaim(t *testing.T) {
	for _, tc := range []struct {
		name           string
		persistRestart func() error
	}{
		{name: "nil persistRestart"},
		{name: "persistRestart error", persistRestart: func() error { return errors.New("worker boundary unavailable") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := beads.NewMemStore()
			rec := events.NewFake()
			dops := newFakeDrainOps()
			var stdout, stderr bytes.Buffer

			b, err := store.Create(beads.Bead{
				Type:   sessionBeadType,
				Labels: []string{"gc:session"},
			})
			if err != nil {
				t.Fatalf("seeding session bead: %v", err)
			}
			for key, value := range map[string]string{
				"session_name":             "mayor",
				"configured_named_session": "true",
				"configured_named_mode":    "always",
				"pin_awake":                "true",
			} {
				if err := store.SetMetadata(b.ID, key, value); err != nil {
					t.Fatalf("set %s: %v", key, err)
				}
			}

			outcome := doHandoffWithOutcome(store, store, rec, dops, tc.persistRestart, "mayor", "mayor",
				[]string{"HANDOFF: context full"}, &stdout, &stderr)
			if outcome.code != 1 {
				t.Fatalf("code = %d, want 1; stderr: %s", outcome.code, stderr.String())
			}

			refreshed, err := store.Get(b.ID)
			if err != nil {
				t.Fatalf("fetching seeded bead: %v", err)
			}
			if refreshed.Metadata[session.HandoffRestartClaimKey] != "" {
				t.Errorf("handoff_restart_claim = %q, want empty when persistRestart fails/unavailable for a pinned session", refreshed.Metadata[session.HandoffRestartClaimKey])
			}
		})
	}
}

// TestDoHandoffRemote_ClaimingKillArmsHandoffRestartClaim covers
// doHandoffRemote's restartable kill branch: killing a remote session is
// itself a restart request (the reconciler restarts it), so it must arm a
// handoff_restart_claim baseline exactly as the self-handoff branches do,
// tagged with a distinct mode so the two call sites are distinguishable.
func TestDoHandoffRemote_ClaimingKillArmsHandoffRestartClaim(t *testing.T) {
	store := beads.NewMemStore()
	rec := events.NewFake()
	sp := runtime.NewFake()
	if err := sp.Start(context.Background(), "deacon", runtime.Config{Command: "echo"}); err != nil {
		t.Fatal(err)
	}
	b, err := store.Create(beads.Bead{
		Type:   sessionBeadType,
		Labels: []string{"gc:session"},
	})
	if err != nil {
		t.Fatalf("seeding session bead: %v", err)
	}
	for key, value := range map[string]string{
		"session_name":     "deacon",
		"generation":       "2",
		"awake_started_at": "2026-07-20T00:00:00Z",
	} {
		if err := store.SetMetadata(b.ID, key, value); err != nil {
			t.Fatalf("set %s: %v", key, err)
		}
	}

	before := time.Now().UTC()
	var stdout, stderr bytes.Buffer
	code := doHandoffRemote(store, store, rec, sp, "deacon", "deacon", "mayor",
		[]string{"Context refresh", "Check beads for current state"}, &stdout, &stderr)
	after := time.Now().UTC()
	if code != 0 {
		t.Fatalf("code = %d, want 0; stderr: %s", code, stderr.String())
	}
	if sp.IsRunning("deacon") {
		t.Error("target session should be stopped")
	}

	refreshed, err := store.Get(b.ID)
	if err != nil {
		t.Fatalf("fetching seeded bead: %v", err)
	}
	raw := refreshed.Metadata[session.HandoffRestartClaimKey]
	if raw == "" {
		t.Fatal("handoff_restart_claim metadata not set; want an armed claim after kill")
	}
	var claim handoffRestartClaim
	if err := json.Unmarshal([]byte(raw), &claim); err != nil {
		t.Fatalf("decoding handoff_restart_claim: %v", err)
	}
	if claim.Mode != "remote" {
		t.Errorf("claim.Mode = %q, want %q", claim.Mode, "remote")
	}
	if claim.Baseline.Generation != "2" || claim.Baseline.AwakeStartedAt != "2026-07-20T00:00:00Z" {
		t.Errorf("claim.Baseline = %+v, want generation=2 awake_started_at=2026-07-20T00:00:00Z", claim.Baseline)
	}
	if claim.ClaimedAt.Before(before) || claim.ClaimedAt.After(after) {
		t.Errorf("claim.ClaimedAt = %v, want between %v and %v", claim.ClaimedAt, before, after)
	}
}

// TestDoHandoffRemote_NamedOnDemandSkipDoesNotArmHandoffRestartClaim covers
// doHandoffRemote's named on-demand skip branch: the kill is skipped, so no
// claim may be armed, and a stale one must be cleared alongside
// restart_requested/continuation_reset_pending.
func TestDoHandoffRemote_NamedOnDemandSkipDoesNotArmHandoffRestartClaim(t *testing.T) {
	store := beads.NewMemStore()
	rec := events.NewFake()
	sp := runtime.NewFake()
	if err := sp.Start(context.Background(), "mayor", runtime.Config{Command: "echo"}); err != nil {
		t.Fatal(err)
	}

	staleClaim := handoffRestartClaim{
		Mode:      "remote",
		Baseline:  handoffRestartIdentity{Generation: "1", AwakeStartedAt: "2026-07-01T00:00:00Z"},
		ClaimedAt: time.Now().Add(-time.Hour).UTC(),
	}
	staleRaw, err := json.Marshal(staleClaim)
	if err != nil {
		t.Fatalf("marshal stale claim: %v", err)
	}

	b, err := store.Create(beads.Bead{
		Type:   sessionBeadType,
		Labels: []string{"gc:session"},
	})
	if err != nil {
		t.Fatalf("seeding session bead: %v", err)
	}
	for key, value := range map[string]string{
		"session_name":                 "mayor",
		"configured_named_session":     "true",
		"configured_named_mode":        "on_demand",
		"restart_requested":            "true",
		"continuation_reset_pending":   "true",
		session.HandoffRestartClaimKey: string(staleRaw),
	} {
		if err := store.SetMetadata(b.ID, key, value); err != nil {
			t.Fatalf("set %s: %v", key, err)
		}
	}
	if err := sp.SetMeta("mayor", "GC_RESTART_REQUESTED", "1"); err != nil {
		t.Fatalf("set runtime restart meta: %v", err)
	}

	var stdout, stderr bytes.Buffer
	code := doHandoffRemote(store, store, rec, sp, "mayor", "mayor", "deacon",
		[]string{"Context refresh", "Please pick this up manually"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("code = %d, want 0; stderr: %s", code, stderr.String())
	}
	if !sp.IsRunning("mayor") {
		t.Error("named on-demand target should still be running")
	}

	refreshed, err := store.Get(b.ID)
	if err != nil {
		t.Fatalf("fetching seeded bead: %v", err)
	}
	if refreshed.Metadata[session.HandoffRestartClaimKey] != "" {
		t.Errorf("handoff_restart_claim = %q, want cleared for named on-demand target", refreshed.Metadata[session.HandoffRestartClaimKey])
	}
}

// TestDoHandoffRemote_NotRunningSkipDoesNotArmHandoffRestartClaim covers the
// not-running branch: no kill occurs, so no restart was ever requested of the
// controller, and no claim may be armed against the target's bead.
func TestDoHandoffRemote_NotRunningSkipDoesNotArmHandoffRestartClaim(t *testing.T) {
	store := beads.NewMemStore()
	rec := events.NewFake()
	sp := runtime.NewFake()

	b, err := store.Create(beads.Bead{
		Type:   sessionBeadType,
		Labels: []string{"gc:session"},
	})
	if err != nil {
		t.Fatalf("seeding session bead: %v", err)
	}
	for key, value := range map[string]string{
		"session_name":     "deacon",
		"generation":       "1",
		"awake_started_at": "2026-07-20T00:00:00Z",
	} {
		if err := store.SetMetadata(b.ID, key, value); err != nil {
			t.Fatalf("set %s: %v", key, err)
		}
	}

	var stdout, stderr bytes.Buffer
	code := doHandoffRemote(store, store, rec, sp, "deacon", "deacon", "human",
		[]string{"Please check on PR #42"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("code = %d, want 0; stderr: %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "not running") {
		t.Errorf("stdout = %q, want 'not running' mention", stdout.String())
	}

	refreshed, err := store.Get(b.ID)
	if err != nil {
		t.Fatalf("fetching seeded bead: %v", err)
	}
	if refreshed.Metadata[session.HandoffRestartClaimKey] != "" {
		t.Errorf("handoff_restart_claim = %q, want empty when target session was never running/killed", refreshed.Metadata[session.HandoffRestartClaimKey])
	}
}
