//go:build integration

package main

import (
	"fmt"
	"maps"
	"reflect"
	"strings"
	"testing"

	sessionpkg "github.com/gastownhall/gascity/internal/session"
)

// siblingPoolIsolationLiveStateClass is the one lifecycle class a sibling must
// stay inside across a drain of another member. "active" and "awake" are the
// same class: pending_create_lease.go treats them identically and manager.go
// documents awake as the reconciler's alias for active, so the routine alias
// heal is not a state change. A transition to draining/asleep/drained/suspended
// is precisely the drain-effect leak this comparison exists to catch.
var siblingPoolIsolationLiveStateClass = map[string]struct{}{
	string(sessionpkg.StateActive): {},
	string(sessionpkg.StateAwake):  {},
}

// siblingPoolIsolationAllowedChurn lists the metadata keys a level-triggered
// controller legitimately writes on a sibling while another member drains. Each
// entry cites the writer, and that citation is the admission ticket: an
// unexplained new key failing the compare is a FINDING, not an allowlist
// candidate (ga-f7v2ft.112 ruling 3).
//
//   - idleClaimNudge{Trigger,Count,At}Key: the idle-claim nudge scheduler's
//     bookkeeping (cmd/gc/idle_nudge.go:21-23). The drained member's trigger
//     closure can make the sibling the next claim target, so this churn is
//     drain-ADJACENT by design -- re-targeting work is the intended system
//     response, not an isolation violation.
//   - synced_at: the session sync stamp, written by syncSessionBeads
//     (cmd/gc/session_beads.go) on every tick that touches a row and by the
//     lifecycle patch builders (internal/session/lifecycle_transition.go).
//     Neither writer carries drain intent.
//
// The nudge keys are taken from the production constants rather than respelled
// so a rename cannot silently widen this allowlist into a stale no-op.
var siblingPoolIsolationAllowedChurn = []string{
	idleClaimNudgeTriggerKey,
	idleClaimNudgeCountKey,
	idleClaimNudgeAtKey,
	"synced_at",
}

// legacyDrainEffectSites are the trace sites at which a WRITE to a session row
// carries drain, stop, or wake intent. They are the sites the drain-finalize
// purity assertion is scoped to (ga-f7v2ft.112 :1779 ruling): the invariant is
// "legacy applied no drain EFFECT to the drained row", not "no legacy cycle ran
// anywhere", so a poke-triggered cycle that touches none of these on this row is
// background activity and is tolerated.
var legacyDrainEffectSites = map[TraceSiteCode]struct{}{
	TraceSiteReconcilerDrainAck:      {},
	TraceSiteReconcilerDrainDecision: {},
	TraceSiteDrainStale:              {},
	TraceSiteDrainComplete:           {},
	TraceSiteDrainCancel:             {},
	TraceSiteDrainTimeout:            {},
	TraceSiteLifecycleDrainAdvance:   {},
	TraceSiteReconcilerWakeDecision:  {},
	TraceSiteReconcilerIdleTimeout:   {},
	TraceSiteReconcilerMaxSessionAge: {},
}

// legacyDrainEffectOutcomes are the outcomes that mean the record APPLIED
// something rather than merely observing or declining. A kept-open, skipped,
// deferred or no-change record at a drain site is not an effect.
var legacyDrainEffectOutcomes = map[TraceOutcomeCode]struct{}{
	TraceOutcomeStopPending:         {},
	TraceOutcomeStop:                {},
	TraceOutcomeComplete:            {},
	TraceOutcomeClosed:              {},
	TraceOutcomeDrain:               {},
	TraceOutcomeCancel:              {},
	TraceOutcomeCancelPending:       {},
	TraceOutcomeCancelAssignedWork:  {},
	TraceOutcomeCancelReconcilerAck: {},
	TraceOutcomeClear:               {},
}

// legacyDrainEffectRecord reports whether one trace record is a LEGACY-owned
// drain/stop/wake effect. A record the keyed owner wrote carries
// effect_owner=keyed and is this slice's own work, not a coexistence violation.
func legacyDrainEffectRecord(record SessionReconcilerTraceRecord) bool {
	if _, ok := legacyDrainEffectSites[record.SiteCode]; !ok {
		return false
	}
	if _, ok := legacyDrainEffectOutcomes[record.OutcomeCode]; !ok {
		return false
	}
	if owner, ok := record.Fields["effect_owner"]; ok {
		if text, isText := owner.(string); isText &&
			(text == detectorKeyedEffectOwner || text == detectorShadowEffectOwner) {
			return false
		}
	}
	return true
}

// siblingPoolIsolationMetadataDiff reports whether a sibling's session metadata
// carries any effect of another member's drain. Revision equality and a blanket
// DeepEqual are deliberately NOT asserted: any allowlisted bookkeeping write
// moves the revision, and the blanket compare cannot distinguish that from a
// real leak. The teeth stay: the lifecycle class must not leave the live class,
// and every non-allowlisted key must be byte-equal.
func siblingPoolIsolationMetadataDiff(before, after map[string]string) error {
	normalizedBefore := maps.Clone(before)
	normalizedAfter := maps.Clone(after)
	if normalizedBefore == nil {
		normalizedBefore = map[string]string{}
	}
	if normalizedAfter == nil {
		normalizedAfter = map[string]string{}
	}
	for name, metadata := range map[string]map[string]string{"before": normalizedBefore, "after": normalizedAfter} {
		state := strings.TrimSpace(metadata["state"])
		if _, live := siblingPoolIsolationLiveStateClass[state]; !live {
			return fmt.Errorf("sibling state %s = %q, want the live class %v", name, state, siblingPoolIsolationLiveStateClass)
		}
		metadata["state"] = string(sessionpkg.StateActive)
		for _, key := range siblingPoolIsolationAllowedChurn {
			delete(metadata, key)
		}
	}
	if !reflect.DeepEqual(normalizedBefore, normalizedAfter) {
		return fmt.Errorf("metadata outside the churn allowlist changed: before=%v after=%v", normalizedBefore, normalizedAfter)
	}
	return nil
}

// TestSiblingPoolIsolationMetadataDiff pins the respecced sibling-isolation
// comparison (ga-f7v2ft.112 ruling 3). The proof's teeth are the incarnation
// and lifecycle class plus the absence of any drain effect; the two writers
// that legitimately run inside the drain window are tolerated by an explicit
// allowlist, not by weakening the compare.
func TestSiblingPoolIsolationMetadataDiff(t *testing.T) {
	base := func() map[string]string {
		return map[string]string{
			"state":              "active",
			"instance_token":     "tok-1",
			"generation":         "3",
			"continuation_epoch": "7",
			"session_name":       "worker-gc-1",
			"pool_managed":       "true",
			"pool_slot":          "2",
			"gc.trigger_bead_id": "gc-work-2",
		}
	}
	mutate := func(changes map[string]string) map[string]string {
		after := base()
		for key, value := range changes {
			if value == "" {
				delete(after, key)
				continue
			}
			after[key] = value
		}
		return after
	}

	for _, test := range []struct {
		name    string
		after   map[string]string
		wantErr bool
	}{
		{name: "unchanged", after: base()},
		{
			name:  "live-state alias heal is not a change",
			after: mutate(map[string]string{"state": "awake"}),
		},
		{
			name: "idle-claim nudge bookkeeping is allowlisted",
			after: mutate(map[string]string{
				"idle_claim_nudge_trigger": "gc-work-9",
				"idle_claim_nudge_count":   "1",
				"idle_claim_nudge_at":      "2026-08-09T00:00:00Z",
			}),
		},
		{name: "sync stamp is allowlisted", after: mutate(map[string]string{"synced_at": "2026-08-09T00:00:00Z"})},
		{
			name:    "a drain moved the sibling out of the live class",
			after:   mutate(map[string]string{"state": "draining"}),
			wantErr: true,
		},
		{
			name:    "a drain put the sibling to sleep",
			after:   mutate(map[string]string{"state": "asleep"}),
			wantErr: true,
		},
		{
			name:    "a drain acknowledgement key appeared",
			after:   mutate(map[string]string{"drain_ack_stop_pending_at": "2026-08-09T00:00:00Z"}),
			wantErr: true,
		},
		{
			name:    "the incarnation changed",
			after:   mutate(map[string]string{"instance_token": "tok-2"}),
			wantErr: true,
		},
		{
			name:    "the trigger binding moved",
			after:   mutate(map[string]string{"gc.trigger_bead_id": "gc-work-9"}),
			wantErr: true,
		},
		{
			name:    "an unexplained new key is a finding, not an allowlist candidate",
			after:   mutate(map[string]string{"quarantined_until": "2026-08-09T00:00:00Z"}),
			wantErr: true,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := siblingPoolIsolationMetadataDiff(base(), test.after)
			if (err != nil) != test.wantErr {
				t.Fatalf("siblingPoolIsolationMetadataDiff = %v, wantErr=%t", err, test.wantErr)
			}
		})
	}
}
