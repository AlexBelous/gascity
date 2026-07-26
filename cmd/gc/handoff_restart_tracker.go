package main

import (
	"encoding/json"
	"strings"
	"time"

	sessionpkg "github.com/gastownhall/gascity/internal/session"
)

// handoffRestartIdentity captures the generation/awake_started_at identity of
// a session at a specific moment — either the baseline a handoff observed
// just before requesting a restart, or the current identity read later when
// checking whether that restart had any observable effect.
type handoffRestartIdentity struct {
	Generation     string `json:"generation"`
	AwakeStartedAt string `json:"awake_started_at"`
}

// handoffRestartClaim is the JSON value stored under
// session.HandoffRestartClaimKey by a handoff that just requested a restart.
// It records which mode requested the restart, the identity baseline
// observed just before the restart was requested, and when the claim was
// armed.
type handoffRestartClaim struct {
	Mode      string                 `json:"mode"`
	Baseline  handoffRestartIdentity `json:"baseline"`
	ClaimedAt time.Time              `json:"claimed_at"`
}

// handoffRestartIdentityFromInfo reads the current generation/awake_started_at
// identity from a session's projected Info.
func handoffRestartIdentityFromInfo(info sessionpkg.Info) handoffRestartIdentity {
	return handoffRestartIdentity{Generation: info.Generation, AwakeStartedAt: info.AwakeStartedAt}
}

// handoffRestartClaimFromInfo decodes the handoff_restart_claim baseline
// armed by a handoff, if any. It returns ok=false when no claim is present,
// the claim is malformed, or the claim has a zero ClaimedAt (never
// successfully armed).
func handoffRestartClaimFromInfo(info sessionpkg.Info) (handoffRestartClaim, bool) {
	raw := strings.TrimSpace(info.HandoffRestartClaim)
	if raw == "" {
		return handoffRestartClaim{}, false
	}
	var claim handoffRestartClaim
	if err := json.Unmarshal([]byte(raw), &claim); err != nil {
		return handoffRestartClaim{}, false
	}
	if claim.ClaimedAt.IsZero() {
		return handoffRestartClaim{}, false
	}
	return claim, true
}
