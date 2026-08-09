package main

import (
	"testing"

	"github.com/gastownhall/gascity/internal/beadmeta"
	"github.com/gastownhall/gascity/internal/beads"
	sessionpkg "github.com/gastownhall/gascity/internal/session"
)

// TestRoutedWorkPoolDrainAckSurvivesLegacyTriggerRepoint is the ga-f7v2ft.131
// repro.
//
// The keyed drain-ack sweep rebuilds its lease FROM the member row every tick
// (newRoutedWorkPoolDrainAckLease, pool_allocation_controller.go:181 —
// WorkID: info.TriggerBeadID), and the effect boundary then requires that work
// to be closed (pool_allocation_controller.go:295-301). Between the worker's
// ack and the keyed stop, the row is still state=active, so the legacy pool
// builder may re-point it to the next ready work item — that is exactly the
// reassign arm of computePoolTriggerBindingPatch
// (build_desired_state.go:3022-3025), and re-targeting a freed member is the
// intended system response to the drained member's trigger closing.
//
// After that re-point every rebuilt lease names a DIFFERENT, genuinely OPEN
// trigger, so the guard refuses with got_id == want_id and status=open for the
// whole finalize budget and forever after. The instrumented journey signature
// (refused=work_not_closed[got_id=X got_status=open want_id=X store=city:...])
// is that refusal, not a stale read: this test reproduces it with no store,
// cache, or Dolt session involved at all.
func TestRoutedWorkPoolDrainAckSurvivesLegacyTriggerRepoint(t *testing.T) {
	fixture := newRoutedWorkPoolDrainAckAuthorizationFixture(t)

	// The sibling member's routed work: a second, genuinely open trigger in the
	// same store, which is what legacy re-targets a freed member onto.
	sibling, err := fixture.workStore.Create(beads.Bead{
		Title:    "sibling routed work",
		Type:     "task",
		Status:   "open",
		Metadata: map[string]string{"gc.routed_to": fixture.template},
	})
	if err != nil {
		t.Fatalf("create sibling routed work: %v", err)
	}

	// Legacy re-points the acknowledged member onto the still-open sibling work
	// while the drain-ack is pending and the row is still active.
	if err := sessionFrontDoor(fixture.store).ApplyPatch(fixture.info.ID, sessionpkg.MetadataPatch{
		beadmeta.TriggerBeadIDMetadataKey: sibling.ID,
	}); err != nil {
		t.Fatalf("legacy re-points the drained member: %v", err)
	}
	repointed, err := sessionFrontDoor(fixture.store).Get(fixture.info.ID)
	if err != nil {
		t.Fatalf("read re-pointed member: %v", err)
	}
	if repointed.TriggerBeadID != sibling.ID {
		t.Fatalf("re-pointed member trigger = %q, want %q", repointed.TriggerBeadID, sibling.ID)
	}

	// The sweep rebuilds the lease from the row, exactly as it does every tick.
	lease, agentDrainAck, err := fixture.cr.newRoutedWorkPoolDrainAckLease(fixture.snapshot, repointed)
	if err != nil {
		t.Fatalf("rebuild drain acknowledgement lease: %v", err)
	}
	if !agentDrainAck {
		t.Fatal("rebuilt lease is not an agent drain acknowledgement; the ack evidence was lost")
	}

	// The acknowledged work is still closed and the ack is unchanged, so the
	// drain must still finalize. Today the rebuilt lease names the sibling's
	// open trigger and the effect boundary refuses forever.
	if lease.WorkID != fixture.work.ID {
		t.Fatalf("rebuilt drain acknowledgement lease names work %q, want the acknowledged work %q: "+
			"a drain acknowledgement is about the unit of work the agent finished, not whatever "+
			"trigger the row carries when the sweep next runs (ga-f7v2ft.131)",
			lease.WorkID, fixture.work.ID)
	}
	authorized, err := fixture.cr.authorizeRoutedWorkPoolDrainAck(fixture.snapshot, repointed, lease)
	if err != nil {
		t.Fatalf("authorize drain acknowledgement after re-point: %v", err)
	}
	if !authorized {
		t.Fatal("drain acknowledgement is refused after a legacy trigger re-point; the acknowledged drain can never finalize (ga-f7v2ft.131)")
	}
}
