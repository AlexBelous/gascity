package main

import (
	"context"
	"errors"
	"testing"

	"github.com/gastownhall/gascity/internal/beads"
)

// fakeClaimerStore is a MemStore-backed store that also implements
// beads.AtomicClaimer, recording the claim it was asked to perform. It stands in
// for the controller's real CachingStore-over-NativeDoltStore, which implements
// AtomicClaimer, so the test can prove the policy wrapper forwards the capability.
type fakeClaimerStore struct {
	beads.Store
	gotID    string
	gotActor string
	claimed  bool
}

// error return; this fake never errs, which is exactly the success path the
// forwarding test exercises.
//
//nolint:unparam // implements beads.AtomicClaimer, whose contract includes an
func (s *fakeClaimerStore) ClaimBead(_ context.Context, id, actor string) (beads.Bead, bool, error) {
	s.gotID = id
	s.gotActor = actor
	s.claimed = true
	return beads.Bead{ID: id, Assignee: actor, Status: "in_progress"}, true, nil
}

// TestBeadPolicyStoreForwardsAtomicClaimer is the unit regression guard for the
// wiring defect the managed-Dolt connection cure's process-level oracle caught:
// the policy wrapper embeds the beads.Store interface, which hides the optional
// AtomicClaimer capability, so a policy-wrapped store must forward ClaimBead
// explicitly or the controller claim handler rejects every generated-default
// hook claim ("bead store does not support atomic claim"), defeating the cure.
func TestBeadPolicyStoreForwardsAtomicClaimer(t *testing.T) {
	backing := &fakeClaimerStore{Store: beads.NewMemStore()}
	wrapped := wrapStoreWithBeadPolicies(backing, nil)

	claimer, ok := wrapped.(beads.AtomicClaimer)
	if !ok {
		t.Fatalf("policy-wrapped store implements beads.AtomicClaimer = false; the controller claim handler would reject the claim")
	}
	bead, claimed, err := claimer.ClaimBead(context.Background(), "gc-abc123", "worker-7")
	if err != nil {
		t.Fatalf("ClaimBead through policy wrapper: %v", err)
	}
	if !claimed {
		t.Fatal("ClaimBead claimed = false, want true")
	}
	if bead.ID != "gc-abc123" || bead.Assignee != "worker-7" {
		t.Fatalf("claimed bead = %+v, want id=gc-abc123 assignee=worker-7", bead)
	}
	if backing.gotID != "gc-abc123" || backing.gotActor != "worker-7" {
		t.Fatalf("backing claimer got (id=%q, actor=%q), want (gc-abc123, worker-7); the wrapper did not forward to the inner claimer",
			backing.gotID, backing.gotActor)
	}
}

// TestBeadPolicyStoreClaimUnsupportedBackingReportsSentinel confirms the wrapper
// surfaces beads.ErrAtomicClaimUnsupported (not a nil claim or a panic) when the
// backing store cannot claim atomically, matching the Count/DeleteBatch/
// ReleaseIfCurrent unsupported-capability contract.
func TestBeadPolicyStoreClaimUnsupportedBackingReportsSentinel(t *testing.T) {
	// A plain MemStore does not implement AtomicClaimer.
	wrapped := wrapStoreWithBeadPolicies(beads.NewMemStore(), nil)
	claimer, ok := wrapped.(beads.AtomicClaimer)
	if !ok {
		t.Fatalf("policy-wrapped store must always expose the AtomicClaimer method surface")
	}
	if _, _, err := claimer.ClaimBead(context.Background(), "gc-none", "worker-1"); !errors.Is(err, beads.ErrAtomicClaimUnsupported) {
		t.Fatalf("ClaimBead over a non-claimer backing = %v, want ErrAtomicClaimUnsupported", err)
	}
}

// TestBeadPolicyGraphStoreForwardsAtomicClaimer proves the graph-store variant
// (used when the backing supports GraphApply) inherits the forwarding through its
// embedded *beadPolicyStore, so the controller's graph-capable stores claim too.
func TestBeadPolicyGraphStoreForwardsAtomicClaimer(t *testing.T) {
	backing := &fakeClaimerGraphStore{fakeClaimerStore: fakeClaimerStore{Store: beads.NewMemStore()}}
	wrapped := wrapStoreWithBeadPolicies(backing, nil)
	if _, isGraph := wrapped.(*beadPolicyGraphStore); !isGraph {
		t.Fatalf("wrapStoreWithBeadPolicies did not select the graph store for a GraphApply-capable backing")
	}
	claimer, ok := wrapped.(beads.AtomicClaimer)
	if !ok {
		t.Fatalf("policy-wrapped graph store implements beads.AtomicClaimer = false")
	}
	if _, claimed, err := claimer.ClaimBead(context.Background(), "gc-g1", "worker-2"); err != nil || !claimed {
		t.Fatalf("ClaimBead through graph policy wrapper = (claimed=%v, err=%v), want (true, nil)", claimed, err)
	}
	if backing.gotActor != "worker-2" {
		t.Fatalf("graph backing claimer actor = %q, want worker-2", backing.gotActor)
	}
}

// fakeClaimerGraphStore adds GraphApply support so wrapStoreWithBeadPolicies
// selects the beadPolicyGraphStore branch.
type fakeClaimerGraphStore struct {
	fakeClaimerStore
}

func (s *fakeClaimerGraphStore) ApplyGraphPlan(_ context.Context, _ *beads.GraphApplyPlan) (*beads.GraphApplyResult, error) {
	return &beads.GraphApplyResult{}, nil
}
