package extmsg

import (
	"context"
	"testing"

	"github.com/gastownhall/gascity/internal/beads"
)

// These tests pin the two-store seam: extmsg records (bindings, groups,
// participants, memberships, transcripts) are MESSAGING-class beads, while
// session-liveness resolution reads SESSION-class beads. With the classes on
// distinct stores, liveness must resolve from the session store and every
// record mutation must land in the messaging store — a reaper that read
// liveness from the messaging store would find no session bead and wrongly
// clear a live binding as dead.

// requireNoSessionBeads fails if any session-class bead leaked into the
// messaging store.
func requireNoSessionBeads(t *testing.T, msg beads.Store) {
	t.Helper()
	got, err := msg.List(beads.ListQuery{Type: "session", IncludeClosed: true, AllowScan: true})
	if err != nil {
		t.Fatalf("list session beads in messaging store: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("messaging store holds %d session beads; session-class reads must go to the session store", len(got))
	}
}

func TestReapStaleBindingsResolvesLivenessFromSessionStore(t *testing.T) {
	freezeTestClock(t)
	msg := beads.NewMemStore()
	sess := beads.NewMemStore()
	svc := NewServicesWithSessionStore(msg, sess).Bindings
	ref := testConversationRef()

	oldID := makeSessionBead(t, sess, "gc-pl")
	if _, err := svc.Bind(context.Background(), testControllerCaller(), BindInput{
		Conversation: ref,
		SessionID:    oldID,
		Now:          testNow(),
	}); err != nil {
		t.Fatalf("Bind: %v", err)
	}
	newID := respawn(t, sess, oldID, "gc-pl")

	stats, err := ReapStaleBindings(context.Background(), msg, sess, testNow())
	if err != nil {
		t.Fatalf("ReapStaleBindings: %v", err)
	}
	if stats.Scanned != 1 || stats.Reassigned != 1 || stats.Cleared != 0 {
		t.Fatalf("stats = %+v, want Scanned=1 Reassigned=1 Cleared=0 (a Cleared=1 means liveness resolved against the messaging store)", stats)
	}
	got, err := svc.ResolveByConversation(context.Background(), ref)
	if err != nil {
		t.Fatalf("ResolveByConversation: %v", err)
	}
	if got == nil || got.SessionID != newID {
		t.Fatalf("after reap, SessionID = %+v, want %q", got, newID)
	}
	requireNoSessionBeads(t, msg)
}

func TestReapStaleParticipantsResolvesLivenessFromSessionStore(t *testing.T) {
	freezeTestClock(t)
	msg := beads.NewMemStore()
	sess := beads.NewMemStore()
	sessAID := makeSessionBead(t, sess, "pl-alpha")

	fabric := NewServicesWithSessionStore(msg, sess)
	svc := fabric.Groups
	ref := testConversationRef()
	group, err := svc.EnsureGroup(context.Background(), testControllerCaller(), EnsureGroupInput{
		RootConversation: ref,
		Mode:             GroupModeLauncher,
		DefaultHandle:    "alpha",
	})
	if err != nil {
		t.Fatalf("EnsureGroup: %v", err)
	}
	participant, err := svc.UpsertParticipant(context.Background(), testControllerCaller(), UpsertParticipantInput{
		GroupID:   group.ID,
		Handle:    "alpha",
		SessionID: sessAID,
	})
	if err != nil {
		t.Fatalf("UpsertParticipant: %v", err)
	}
	sessBID := respawn(t, sess, sessAID, "pl-alpha")

	stats, err := ReapStaleParticipants(context.Background(), msg, sess)
	if err != nil {
		t.Fatalf("ReapStaleParticipants: %v", err)
	}
	if stats.Reassigned != 1 || stats.Scanned != 1 {
		t.Fatalf("stats = %+v, want Reassigned=1 Scanned=1", stats)
	}
	bead, err := msg.Get(participant.ID)
	if err != nil {
		t.Fatalf("Get(participant) from messaging store: %v", err)
	}
	if bead.Metadata["session_id"] != sessBID {
		t.Errorf("participant session_id = %q, want %q (respawned bead)", bead.Metadata["session_id"], sessBID)
	}
	requireNoSessionBeads(t, msg)
}

// NewServices with one store must stay byte-identical: the session store
// defaults to the messaging store, preserving today's single-store behavior.
func TestNewServicesSingleStoreIdentity(t *testing.T) {
	freezeTestClock(t)
	store := beads.NewMemStore()
	svc := NewServices(store).Bindings
	ref := testConversationRef()

	oldID := makeSessionBead(t, store, "gc-id")
	if _, err := svc.Bind(context.Background(), testControllerCaller(), BindInput{
		Conversation: ref,
		SessionID:    oldID,
		Now:          testNow(),
	}); err != nil {
		t.Fatalf("Bind: %v", err)
	}
	newID := respawn(t, store, oldID, "gc-id")
	stats, err := ReapStaleBindings(context.Background(), store, store, testNow())
	if err != nil {
		t.Fatalf("ReapStaleBindings: %v", err)
	}
	if stats.Reassigned != 1 {
		t.Fatalf("stats = %+v, want Reassigned=1", stats)
	}
	got, err := svc.ResolveByConversation(context.Background(), ref)
	if err != nil || got == nil || got.SessionID != newID {
		t.Fatalf("ResolveByConversation = %+v, %v; want SessionID %q", got, err, newID)
	}
}
