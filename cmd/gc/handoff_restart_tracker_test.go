package main

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/events"
	"github.com/gastownhall/gascity/internal/session"
)

func TestHandoffRestartIdentityFromInfo(t *testing.T) {
	info := seedSessionInfo(makeBead("b1", map[string]string{
		"generation":       "9",
		"awake_started_at": "2026-07-21T00:00:00Z",
	}))
	got := handoffRestartIdentityFromInfo(info)
	want := handoffRestartIdentity{Generation: "9", AwakeStartedAt: "2026-07-21T00:00:00Z"}
	if got != want {
		t.Errorf("handoffRestartIdentityFromInfo = %+v, want %+v", got, want)
	}
}

func TestHandoffRestartClaimFromInfo(t *testing.T) {
	const validClaim = `{"mode":"self","baseline":{"generation":"7","awake_started_at":"2026-07-20T00:00:00Z"},"claimed_at":"2026-07-20T00:05:00Z"}`
	tests := []struct {
		name     string
		md       map[string]string
		wantMode string
		wantOK   bool
	}{
		{"absent", map[string]string{}, "", false},
		{"empty", map[string]string{session.HandoffRestartClaimKey: ""}, "", false},
		{"whitespace", map[string]string{session.HandoffRestartClaimKey: "   "}, "", false},
		{"malformed json", map[string]string{session.HandoffRestartClaimKey: "{not json"}, "", false},
		{"zero claimed_at", map[string]string{session.HandoffRestartClaimKey: `{"mode":"self","baseline":{},"claimed_at":"0001-01-01T00:00:00Z"}`}, "", false},
		{"valid", map[string]string{session.HandoffRestartClaimKey: validClaim}, "self", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			info := seedSessionInfo(makeBead("b1", tt.md))
			claim, ok := handoffRestartClaimFromInfo(info)
			if ok != tt.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tt.wantOK)
			}
			if !tt.wantOK {
				return
			}
			if claim.Mode != tt.wantMode {
				t.Errorf("claim.Mode = %q, want %q", claim.Mode, tt.wantMode)
			}
			if claim.Baseline.Generation != "7" {
				t.Errorf("claim.Baseline.Generation = %q, want %q", claim.Baseline.Generation, "7")
			}
			if claim.Baseline.AwakeStartedAt != "2026-07-20T00:00:00Z" {
				t.Errorf("claim.Baseline.AwakeStartedAt = %q, want %q", claim.Baseline.AwakeStartedAt, "2026-07-20T00:00:00Z")
			}
			if claim.ClaimedAt.IsZero() {
				t.Error("claim.ClaimedAt is zero, want a parsed time")
			}
		})
	}
}

func TestDrainTrackerHandoffRestartNoEffectDedup(t *testing.T) {
	dt := newDrainTracker()
	if !dt.markHandoffRestartNoEffect("b1") {
		t.Fatal("first mark should return true (fresh episode)")
	}
	if dt.markHandoffRestartNoEffect("b1") {
		t.Fatal("second mark should return false (already marked)")
	}
	dt.clearHandoffRestartNoEffect("b1")
	if !dt.markHandoffRestartNoEffect("b1") {
		t.Fatal("mark after clear should return true (new episode)")
	}
}

func TestDrainTrackerHandoffRestartNoEffectNilSafe(t *testing.T) {
	var dt *drainTracker
	if !dt.markHandoffRestartNoEffect("b1") {
		t.Error("nil drainTracker markHandoffRestartNoEffect should fail open (return true)")
	}
	dt.clearHandoffRestartNoEffect("b1") // must not panic
}

func TestDrainTrackerHandoffRestartNoEffectEmptyBeadID(t *testing.T) {
	dt := newDrainTracker()
	if !dt.markHandoffRestartNoEffect("") {
		t.Error("empty bead id should fail open (return true)")
	}
}

func TestRecordHandoffRestartNoEffectIfDue(t *testing.T) {
	const template = "worker"
	const name = "worker-1"
	startupTimeout := 60 * time.Second
	baseline := map[string]string{"generation": "5", "awake_started_at": "2026-07-20T00:00:00Z"}

	t.Run("no claim is a no-op", func(t *testing.T) {
		dt := newDrainTracker()
		rec := events.NewFake()
		var stderr bytes.Buffer
		info := seedSessionInfo(makeBead("b1", map[string]string{}))
		recordHandoffRestartNoEffectIfDue(info, template, name, startupTimeout, time.Now().UTC(), dt, rec, &stderr, nil)
		if len(rec.Events) != 0 {
			t.Fatalf("got %d events, want 0", len(rec.Events))
		}
		if stderr.Len() != 0 {
			t.Fatalf("stderr = %q, want empty", stderr.String())
		}
	})

	t.Run("within grace period does not fire", func(t *testing.T) {
		dt := newDrainTracker()
		rec := events.NewFake()
		var stderr bytes.Buffer
		claimedAt := time.Now().UTC()
		claim := handoffRestartClaim{Mode: "self", Baseline: handoffRestartIdentity{Generation: baseline["generation"], AwakeStartedAt: baseline["awake_started_at"]}, ClaimedAt: claimedAt}
		raw, err := json.Marshal(claim)
		if err != nil {
			t.Fatalf("marshal claim: %v", err)
		}
		md := map[string]string{session.HandoffRestartClaimKey: string(raw)}
		for k, v := range baseline {
			md[k] = v
		}
		info := seedSessionInfo(makeBead("b1", md))
		now := claimedAt.Add(10 * time.Second)
		recordHandoffRestartNoEffectIfDue(info, template, name, startupTimeout, now, dt, rec, &stderr, nil)
		if len(rec.Events) != 0 {
			t.Fatalf("got %d events, want 0 within grace period", len(rec.Events))
		}
	})

	t.Run("elapsed with unchanged identity fires exactly once", func(t *testing.T) {
		dt := newDrainTracker()
		rec := events.NewFake()
		var stderr bytes.Buffer
		claimedAt := time.Now().UTC()
		claim := handoffRestartClaim{Mode: "self", Baseline: handoffRestartIdentity{Generation: baseline["generation"], AwakeStartedAt: baseline["awake_started_at"]}, ClaimedAt: claimedAt}
		raw, err := json.Marshal(claim)
		if err != nil {
			t.Fatalf("marshal claim: %v", err)
		}
		md := map[string]string{session.HandoffRestartClaimKey: string(raw)}
		for k, v := range baseline {
			md[k] = v
		}
		info := seedSessionInfo(makeBead("b1", md))
		now := claimedAt.Add(startupTimeout + 5*time.Second)

		recordHandoffRestartNoEffectIfDue(info, template, name, startupTimeout, now, dt, rec, &stderr, nil)
		if len(rec.Events) != 1 {
			t.Fatalf("got %d events, want 1", len(rec.Events))
		}
		if rec.Events[0].Type != events.HandoffRestartNoEffect {
			t.Fatalf("event type = %q, want %q", rec.Events[0].Type, events.HandoffRestartNoEffect)
		}

		recordHandoffRestartNoEffectIfDue(info, template, name, startupTimeout, now, dt, rec, &stderr, nil)
		if len(rec.Events) != 1 {
			t.Fatalf("got %d events after repeat, want 1 (deduped)", len(rec.Events))
		}
	})

	t.Run("identity change clears the tracker and stays silent", func(t *testing.T) {
		dt := newDrainTracker()
		rec := events.NewFake()
		var stderr bytes.Buffer
		dt.markHandoffRestartNoEffect("b1") // simulate a prior firing

		claimedAt := time.Now().UTC()
		claim := handoffRestartClaim{Mode: "self", Baseline: handoffRestartIdentity{Generation: baseline["generation"], AwakeStartedAt: baseline["awake_started_at"]}, ClaimedAt: claimedAt}
		raw, err := json.Marshal(claim)
		if err != nil {
			t.Fatalf("marshal claim: %v", err)
		}
		md := map[string]string{
			session.HandoffRestartClaimKey: string(raw),
			"generation":                   "6", // changed from baseline "5" -- restart took effect
			"awake_started_at":             "2026-07-21T00:00:00Z",
		}
		info := seedSessionInfo(makeBead("b1", md))
		now := claimedAt.Add(startupTimeout + 5*time.Second)

		recordHandoffRestartNoEffectIfDue(info, template, name, startupTimeout, now, dt, rec, &stderr, nil)
		if len(rec.Events) != 0 {
			t.Fatalf("got %d events, want 0 when identity changed (restart succeeded)", len(rec.Events))
		}
		if !dt.markHandoffRestartNoEffect("b1") {
			t.Error("tracker should have been cleared after identity changed; markHandoffRestartNoEffect should return true again")
		}
	})
}
