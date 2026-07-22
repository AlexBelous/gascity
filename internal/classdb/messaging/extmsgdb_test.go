package messagingdb

// Backend-level pins for the extmsg typed tables: the repair-op semantics
// the service-surface conformance cannot reach until the wiring slice adds
// routed twins of the package-level repair funcs, plus the schema
// constraints, import primitives, and dormant retention.

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/extmsg"
)

func openExtmsgStore(t *testing.T) *Store {
	t.Helper()
	st, err := Open(filepath.Join(t.TempDir(), "messaging.db"))
	if err != nil {
		t.Fatalf("open messaging store: %v", err)
	}
	t.Cleanup(func() {
		if err := st.Close(); err != nil {
			t.Errorf("close: %v", err)
		}
	})
	return st
}

func extRef(conv string) extmsg.ConversationRef {
	return extmsg.ConversationRef{
		ScopeID:        "city",
		Provider:       "slack",
		AccountID:      "acct-1",
		ConversationID: conv,
		Kind:           extmsg.ConversationRoom,
	}
}

func extNow() time.Time { return time.Date(2026, 7, 2, 9, 0, 0, 0, time.UTC) }

func mustCreateBinding(t *testing.T, st *Store, ref extmsg.ConversationRef, sessionID string) extmsg.SessionBindingRecord {
	t.Helper()
	rec, err := st.CreateBinding(extmsg.BindingCreate{
		Ref:        ref,
		SessionID:  sessionID,
		Generation: 1,
		BoundAt:    extNow(),
	}, "", nil)
	if err != nil {
		t.Fatalf("CreateBinding(%s): %v", sessionID, err)
	}
	return rec
}

func TestActiveBindingUniqueConstraintMapsToConflict(t *testing.T) {
	st := openExtmsgStore(t)
	ref := extRef("room-conflict")
	mustCreateBinding(t, st, ref, "sess-a")
	_, err := st.CreateBinding(extmsg.BindingCreate{
		Ref:        ref,
		SessionID:  "sess-b",
		Generation: 2,
		BoundAt:    extNow(),
	}, "", nil)
	if !errors.Is(err, extmsg.ErrBindingConflict) {
		t.Fatalf("second active bind err = %v, want ErrBindingConflict (schema-enforced)", err)
	}
}

func TestCreateBindingDisplaceIsAtomic(t *testing.T) {
	st := openExtmsgStore(t)
	ref := extRef("room-displace")
	displaced := mustCreateBinding(t, st, ref, "sess-a")

	// A displace whose membership sub-write fails rolls the whole swap
	// back: the displaced binding stays active, no replacement exists.
	boom := errors.New("membership boom")
	_, err := st.CreateBinding(extmsg.BindingCreate{
		Ref:        ref,
		SessionID:  "sess-b",
		Generation: 2,
		BoundAt:    extNow(),
	}, displaced.ID, func(extmsg.FabricWriter) error { return boom })
	if !errors.Is(err, boom) {
		t.Fatalf("displace err = %v, want the membership failure", err)
	}
	history, err := st.BindingHistory(ref)
	if err != nil {
		t.Fatalf("BindingHistory: %v", err)
	}
	if len(history) != 1 || history[0].ID != displaced.ID || history[0].Status != extmsg.BindingActive {
		t.Fatalf("history after failed swap = %+v, want displaced still active alone", history)
	}

	// A clean displace swaps in one commit.
	replacement, err := st.CreateBinding(extmsg.BindingCreate{
		Ref:        ref,
		SessionID:  "sess-b",
		Generation: 2,
		BoundAt:    extNow(),
	}, displaced.ID, nil)
	if err != nil {
		t.Fatalf("CreateBinding(displace): %v", err)
	}
	got, ok, err := st.GetOpenBinding(replacement.ID)
	if err != nil || !ok || got.SessionID != "sess-b" {
		t.Fatalf("replacement = %+v ok=%v err=%v, want active sess-b", got, ok, err)
	}
	if _, ok, err := st.GetOpenBinding(displaced.ID); err != nil || ok {
		t.Fatalf("displaced open = %v err=%v, want ended", ok, err)
	}
}

func TestProviderMessageUniqueBackstop(t *testing.T) {
	st := openExtmsgStore(t)
	ref := extRef("room-pmid")
	state, err := st.Writer().CreateTranscriptState(ref)
	if err != nil {
		t.Fatalf("CreateTranscriptState: %v", err)
	}
	entry := extmsg.TranscriptEntryCreate{
		Ref:               ref,
		Sequence:          1,
		Kind:              extmsg.TranscriptMessageInbound,
		Provenance:        extmsg.TranscriptProvenanceLive,
		ProviderMessageID: "pm-1",
		CreatedAt:         extNow(),
		Text:              "one",
	}
	if _, err := st.AppendTranscript(entry, state.ID, 2, true); err != nil {
		t.Fatalf("AppendTranscript: %v", err)
	}
	// The service dedupes before inserting; the schema is the cross-process
	// backstop — a second insert with the same provider message id is
	// rejected, not silently duplicated.
	entry.Sequence = 2
	entry.Text = "dup"
	if _, err := st.AppendTranscript(entry, state.ID, 3, false); err == nil {
		t.Fatalf("duplicate provider message insert succeeded, want UNIQUE rejection")
	}
	// The rejected append left the allocator untouched (single tx).
	states, err := st.OpenTranscriptStates(ref)
	if err != nil || len(states) != 1 {
		t.Fatalf("OpenTranscriptStates = %v, %v", states, err)
	}
	if states[0].NextSequence != 2 {
		t.Fatalf("next_sequence after rejected append = %d, want 2 (rollback)", states[0].NextSequence)
	}
}

func TestParticipantHandoverStaysDiscoverableUntilCleanupClears(t *testing.T) {
	st := openExtmsgStore(t)
	created, err := st.CreateParticipant(extmsg.ParticipantFields{
		GroupID:   "gcm-group-1",
		Handle:    "alpha",
		SessionID: "sess-old",
	})
	if err != nil {
		t.Fatalf("CreateParticipant: %v", err)
	}

	if err := st.ReassignParticipantSession(created.ID, "sess-old", "sess-new", []string{"sess-old"}); err != nil {
		t.Fatalf("ReassignParticipantSession: %v", err)
	}
	// Mid-handover the participant is discoverable by BOTH the retired and
	// the replacement session — the bd retained-label contract.
	byOld, err := st.ParticipantsBySession("sess-old")
	if err != nil || len(byOld) != 1 || byOld[0].SessionID != "sess-new" {
		t.Fatalf("ParticipantsBySession(old) = %+v, %v; want the handed-over participant", byOld, err)
	}
	if len(byOld[0].PendingCleanup) != 1 || byOld[0].PendingCleanup[0] != "sess-old" {
		t.Fatalf("pending cleanup = %v, want [sess-old]", byOld[0].PendingCleanup)
	}
	byNew, err := st.ParticipantsBySession("sess-new")
	if err != nil || len(byNew) != 1 {
		t.Fatalf("ParticipantsBySession(new) = %+v, %v; want 1", byNew, err)
	}

	// Clearing the pending set (the membership-migration writeback)
	// completes the handover: the retired handle stops matching.
	if err := st.SetParticipantPendingCleanup(created.ID, nil); err != nil {
		t.Fatalf("SetParticipantPendingCleanup: %v", err)
	}
	byOld, err = st.ParticipantsBySession("sess-old")
	if err != nil || len(byOld) != 0 {
		t.Fatalf("ParticipantsBySession(old) after cleanup = %+v, %v; want empty", byOld, err)
	}
	// DropParticipantSessionLabel is the bd label op; here the cleanup
	// writeback already retired the handle, so it is a no-op.
	if err := st.DropParticipantSessionLabel(created.ID, "sess-old", "sess-new"); err != nil {
		t.Fatalf("DropParticipantSessionLabel: %v", err)
	}
}

func TestReassignBindingSessionRepointsRow(t *testing.T) {
	st := openExtmsgStore(t)
	ref := extRef("room-reassign")
	rec := mustCreateBinding(t, st, ref, "sess-old")
	if err := st.ReassignBindingSession(rec.ID, "sess-old", "sess-new", extNow()); err != nil {
		t.Fatalf("ReassignBindingSession: %v", err)
	}
	byNew, err := st.ActiveBindingsBySession("sess-new")
	if err != nil || len(byNew) != 1 || byNew[0].ID != rec.ID {
		t.Fatalf("ActiveBindingsBySession(new) = %+v, %v; want the repointed binding", byNew, err)
	}
	byOld, err := st.ActiveBindingsBySession("sess-old")
	if err != nil || len(byOld) != 0 {
		t.Fatalf("ActiveBindingsBySession(old) = %+v, %v; want empty", byOld, err)
	}
}

func TestBindingRowLifecycleErrors(t *testing.T) {
	st := openExtmsgStore(t)
	if _, _, err := st.GetBinding("missing"); !errors.Is(err, beads.ErrNotFound) {
		t.Fatalf("GetBinding(missing) err = %v, want ErrNotFound", err)
	}
	if err := st.CloseBinding("missing"); !errors.Is(err, beads.ErrNotFound) {
		t.Fatalf("CloseBinding(missing) err = %v, want ErrNotFound", err)
	}
	ref := extRef("room-lifecycle")
	rec := mustCreateBinding(t, st, ref, "sess-a")
	if err := st.CloseBinding(rec.ID); err != nil {
		t.Fatalf("CloseBinding: %v", err)
	}
	// Closing an ended binding is a not-found (only actives close).
	if err := st.CloseBinding(rec.ID); !errors.Is(err, beads.ErrNotFound) {
		t.Fatalf("CloseBinding(ended) err = %v, want ErrNotFound", err)
	}
}

func TestImportRoundTripAndIdempotence(t *testing.T) {
	st := openExtmsgStore(t)
	ref := extRef("room-import")
	expiry := extNow().Add(24 * time.Hour)

	binding := extmsg.SessionBindingRecord{
		ID:                "gc-legacy-1",
		SchemaVersion:     1,
		Conversation:      ref,
		SessionID:         "sess-imp",
		SessionName:       "boot/imp",
		Status:            extmsg.BindingActive,
		BoundAt:           extNow(),
		ExpiresAt:         &expiry,
		BindingGeneration: 3,
		Metadata:          map[string]string{"origin": "legacy"},
	}
	if err := st.ImportBinding(binding, extNow()); err != nil {
		t.Fatalf("ImportBinding: %v", err)
	}
	// An ended predecessor imports too (the generation ceiling carrier).
	ended := binding
	ended.ID = "gc-legacy-0"
	ended.Status = extmsg.BindingEnded
	ended.BindingGeneration = 2
	if err := st.ImportBinding(ended, extNow()); err != nil {
		t.Fatalf("ImportBinding(ended): %v", err)
	}
	// Re-import (interrupted migration resume) is idempotent.
	if err := st.ImportBinding(binding, extNow()); err != nil {
		t.Fatalf("ImportBinding(again): %v", err)
	}
	history, err := st.BindingHistory(ref)
	if err != nil || len(history) != 2 {
		t.Fatalf("BindingHistory = %d records, %v; want 2", len(history), err)
	}
	got, _, err := st.GetBinding("gc-legacy-1")
	if err != nil {
		t.Fatalf("GetBinding(imported): %v", err)
	}
	if got.SessionName != "boot/imp" || got.BindingGeneration != 3 || got.ExpiresAt == nil || !got.ExpiresAt.Equal(expiry) || got.Metadata["origin"] != "legacy" {
		t.Fatalf("imported binding = %+v, want fields preserved verbatim", got)
	}

	if err := st.ImportGroup(extmsg.ConversationGroupRecord{
		ID: "gc-legacy-g", RootConversation: ref, Mode: extmsg.GroupModeLauncher, DefaultHandle: "alpha",
	}); err != nil {
		t.Fatalf("ImportGroup: %v", err)
	}
	if err := st.ImportParticipant(extmsg.ParticipantRecord{
		ConversationGroupParticipant: extmsg.ConversationGroupParticipant{
			ID: "gc-legacy-p", GroupID: "gc-legacy-g", Handle: "alpha", SessionID: "sess-imp",
		},
		PendingCleanup: []string{"sess-prior"},
	}); err != nil {
		t.Fatalf("ImportParticipant: %v", err)
	}
	if err := st.ImportMembership(extmsg.ConversationMembershipRecord{
		ID: "gc-legacy-m", Conversation: ref, SessionID: "sess-imp", JoinedAt: extNow(),
		BackfillPolicy: extmsg.MembershipBackfillAll,
		Owners:         []extmsg.MembershipOwner{extmsg.MembershipOwnerGroup},
	}); err != nil {
		t.Fatalf("ImportMembership: %v", err)
	}
	if err := st.ImportTranscriptState(extmsg.ConversationTranscriptStateRecord{
		ID: "gc-legacy-s", Conversation: ref, NextSequence: 3, EarliestAvailableSequence: 1,
		HydrationStatus: extmsg.HydrationLiveOnly,
	}); err != nil {
		t.Fatalf("ImportTranscriptState: %v", err)
	}
	if err := st.ImportTranscriptEntry(extmsg.ConversationTranscriptRecord{
		ID: "gc-legacy-e1", Conversation: ref, Sequence: 1, Kind: extmsg.TranscriptMessageInbound,
		Provenance: extmsg.TranscriptProvenanceLive, CreatedAt: extNow(), Text: "hello",
		Actor: extmsg.ExternalActor{ID: "U1", DisplayName: "Uma"},
	}); err != nil {
		t.Fatalf("ImportTranscriptEntry: %v", err)
	}

	// The in-flight handover import stays discoverable by the retired
	// session through pending_cleanup.
	byPrior, err := st.ParticipantsBySession("sess-prior")
	if err != nil || len(byPrior) != 1 || byPrior[0].ID != "gc-legacy-p" {
		t.Fatalf("ParticipantsBySession(prior) = %+v, %v; want the imported handover", byPrior, err)
	}
	memberships, err := st.OpenMembershipsBySession("sess-imp")
	if err != nil || len(memberships) != 1 || len(memberships[0].Owners) != 1 {
		t.Fatalf("imported membership = %+v, %v", memberships, err)
	}
	entries, err := st.ListTranscript(ref, 0, 1, 2, 10, false)
	if err != nil || len(entries) != 1 || entries[0].Actor.DisplayName != "Uma" {
		t.Fatalf("imported entry = %+v, %v; want actor preserved", entries, err)
	}
}

func TestSweepExtmsgRetentionSparesGenerationCeiling(t *testing.T) {
	st := openExtmsgStore(t)
	ref := extRef("room-retention")
	old := extNow().Add(-40 * 24 * time.Hour)

	mkEnded := func(id string, generation int64) {
		t.Helper()
		if err := st.ImportBinding(extmsg.SessionBindingRecord{
			ID: id, Conversation: ref, SessionID: "sess-r",
			Status: extmsg.BindingEnded, BoundAt: old, BindingGeneration: generation,
		}, old); err != nil {
			t.Fatalf("ImportBinding(%s): %v", id, err)
		}
	}
	mkEnded("gc-r-1", 1)
	mkEnded("gc-r-2", 2)

	// Closed satellite rows age out with the same cutoff.
	if err := st.ImportParticipant(extmsg.ParticipantRecord{
		ConversationGroupParticipant: extmsg.ConversationGroupParticipant{
			ID: "gc-r-p", GroupID: "g", Handle: "h", SessionID: "sess-r",
		},
	}); err != nil {
		t.Fatalf("ImportParticipant: %v", err)
	}
	if err := st.CloseParticipant("gc-r-p"); err != nil {
		t.Fatalf("CloseParticipant: %v", err)
	}
	if err := st.ImportMembership(extmsg.ConversationMembershipRecord{
		ID: "gc-r-m", Conversation: ref, SessionID: "sess-r", JoinedAt: old,
		BackfillPolicy: extmsg.MembershipBackfillAll,
	}); err != nil {
		t.Fatalf("ImportMembership: %v", err)
	}
	if err := st.CloseMembership("gc-r-m", old); err != nil {
		t.Fatalf("CloseMembership: %v", err)
	}

	// Cutoff in the future relative to the terminal clocks above but the
	// participant closed_at is wall-clock "now", so use a future cutoff for
	// everything.
	deleted, err := st.SweepExtmsgRetention(time.Now().UTC().Add(time.Hour))
	if err != nil {
		t.Fatalf("SweepExtmsgRetention: %v", err)
	}
	// gc-r-1 (superseded ended binding) + participant + membership die;
	// gc-r-2 carries the generation ceiling and survives.
	if deleted != 3 {
		t.Fatalf("deleted = %d, want 3", deleted)
	}
	history, err := st.BindingHistory(ref)
	if err != nil || len(history) != 1 || history[0].ID != "gc-r-2" {
		t.Fatalf("history after sweep = %+v, %v; want only the max-generation row", history, err)
	}
}

func TestPruneTranscriptsAdvancesEarliestFloor(t *testing.T) {
	st := openExtmsgStore(t)
	ref := extRef("room-prune")
	state, err := st.Writer().CreateTranscriptState(ref)
	if err != nil {
		t.Fatalf("CreateTranscriptState: %v", err)
	}
	for i := int64(1); i <= 10; i++ {
		if _, err := st.AppendTranscript(extmsg.TranscriptEntryCreate{
			Ref: ref, Sequence: i, Kind: extmsg.TranscriptMessageInbound,
			Provenance: extmsg.TranscriptProvenanceLive, CreatedAt: extNow(), Text: "entry",
		}, state.ID, i+1, i == 1); err != nil {
			t.Fatalf("AppendTranscript(%d): %v", i, err)
		}
	}

	deleted, err := st.PruneTranscripts(4)
	if err != nil {
		t.Fatalf("PruneTranscripts: %v", err)
	}
	if deleted != 6 {
		t.Fatalf("pruned = %d, want 6 (keep newest 4 of 10)", deleted)
	}
	states, err := st.OpenTranscriptStates(ref)
	if err != nil || len(states) != 1 {
		t.Fatalf("OpenTranscriptStates = %v, %v", states, err)
	}
	if states[0].EarliestAvailableSequence != 7 {
		t.Fatalf("earliest after prune = %d, want 7", states[0].EarliestAvailableSequence)
	}
	remaining, err := st.ListTranscript(ref, 0, 1, 10, 100, false)
	if err != nil || len(remaining) != 4 || remaining[0].Sequence != 7 {
		t.Fatalf("remaining entries = %+v, %v; want [7..10]", remaining, err)
	}

	// A second prune with nothing outside the window is a no-op; a
	// defaultMaxRetained of 0 disables pruning entirely.
	if deleted, err := st.PruneTranscripts(4); err != nil || deleted != 0 {
		t.Fatalf("re-prune = %d, %v; want 0", deleted, err)
	}
	if deleted, err := st.PruneTranscripts(0); err != nil || deleted != 0 {
		t.Fatalf("prune(disabled) = %d, %v; want 0", deleted, err)
	}
}
