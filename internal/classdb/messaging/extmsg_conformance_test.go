package messagingdb

// Both-backend conformance for the extmsg half of the messaging class: the
// behavioral contracts of the extmsg fabric services must hold identically
// over the bead backend and this typed-table backend. Cases exercise ONLY
// the public extmsg.Services surface (extmsg's own suite pins the bd
// backend's mechanics); the package-level repair funcs
// (ReassignSessionBindings et al.) keep bd-store signatures until the wiring
// slice adds routed twins, so their sqlite semantics are pinned at the
// backend-op level in extmsgdb_test.go instead.

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/extmsg"
)

func eachFabric(t *testing.T, fn func(t *testing.T, svc extmsg.Services)) {
	t.Helper()
	t.Run("bd", func(t *testing.T) {
		fn(t, extmsg.NewServicesWithSessionStore(beads.NewMemStore(), beads.NewMemStore()))
	})
	t.Run("sqlite", func(t *testing.T) {
		st, err := Open(filepath.Join(t.TempDir(), "messaging.db"))
		if err != nil {
			t.Fatalf("open sqlite messaging store: %v", err)
		}
		t.Cleanup(func() {
			if err := st.Close(); err != nil {
				t.Errorf("close: %v", err)
			}
		})
		fn(t, extmsg.NewServicesWithBackend(st, beads.NewMemStore()))
	})
}

func confRef(conv string) extmsg.ConversationRef {
	return extmsg.ConversationRef{
		ScopeID:        "city",
		Provider:       "slack",
		AccountID:      "acct-1",
		ConversationID: conv,
		Kind:           extmsg.ConversationDM,
	}
}

func confController() extmsg.Caller {
	return extmsg.Caller{Kind: extmsg.CallerController, ID: "conformance"}
}

func confNow() time.Time {
	return time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
}

func TestConformanceBindResolveRebindHandoff(t *testing.T) {
	eachFabric(t, func(t *testing.T, svc extmsg.Services) {
		ctx := context.Background()
		ref := confRef("C-bind")

		first, err := svc.Bindings.Bind(ctx, confController(), extmsg.BindInput{
			Conversation: ref,
			SessionID:    "sess-a",
			Metadata:     map[string]string{"origin": "one"},
			Now:          confNow(),
		})
		if err != nil {
			t.Fatalf("Bind(fresh): %v", err)
		}
		if first.BindingGeneration != 1 || first.SessionID != "sess-a" || first.Status != extmsg.BindingActive {
			t.Fatalf("fresh binding = %+v, want generation 1 active sess-a", first)
		}
		if first.Metadata["origin"] != "one" {
			t.Fatalf("fresh binding metadata = %v, want origin=one", first.Metadata)
		}

		got, err := svc.Bindings.ResolveByConversation(ctx, ref)
		if err != nil || got == nil || got.SessionID != "sess-a" {
			t.Fatalf("ResolveByConversation = %+v, %v; want sess-a", got, err)
		}

		// Same-target rebind: generation stable, metadata MERGES (bd
		// SetMetadataBatch semantics — prior keys persist).
		rebound, err := svc.Bindings.Bind(ctx, confController(), extmsg.BindInput{
			Conversation: ref,
			SessionID:    "sess-a",
			Metadata:     map[string]string{"channel": "alerts"},
			Now:          confNow().Add(time.Minute),
		})
		if err != nil {
			t.Fatalf("Bind(rebind): %v", err)
		}
		if rebound.BindingGeneration != 1 {
			t.Fatalf("rebind generation = %d, want 1", rebound.BindingGeneration)
		}
		if rebound.Metadata["origin"] != "one" || rebound.Metadata["channel"] != "alerts" {
			t.Fatalf("rebind metadata = %v, want origin+channel merged", rebound.Metadata)
		}

		// Conflicting target without Replace is rejected.
		if _, err := svc.Bindings.Bind(ctx, confController(), extmsg.BindInput{
			Conversation: ref,
			SessionID:    "sess-b",
			Now:          confNow().Add(2 * time.Minute),
		}); !errors.Is(err, extmsg.ErrBindingConflict) {
			t.Fatalf("Bind(conflict) err = %v, want ErrBindingConflict", err)
		}

		// Replace hands the conversation off: next generation, old ended.
		handed, err := svc.Bindings.Bind(ctx, confController(), extmsg.BindInput{
			Conversation: ref,
			SessionID:    "sess-b",
			Replace:      true,
			Now:          confNow().Add(3 * time.Minute),
		})
		if err != nil {
			t.Fatalf("Bind(replace): %v", err)
		}
		if handed.BindingGeneration != 2 || handed.SessionID != "sess-b" {
			t.Fatalf("handoff binding = %+v, want generation 2 sess-b", handed)
		}
		got, err = svc.Bindings.ResolveByConversation(ctx, ref)
		if err != nil || got == nil || got.SessionID != "sess-b" {
			t.Fatalf("post-handoff resolve = %+v, %v; want sess-b", got, err)
		}

		listA, err := svc.Bindings.ListBySession(ctx, "sess-a")
		if err != nil || len(listA) != 0 {
			t.Fatalf("ListBySession(sess-a) = %v, %v; want empty", listA, err)
		}
		listB, err := svc.Bindings.ListBySession(ctx, "sess-b")
		if err != nil || len(listB) != 1 {
			t.Fatalf("ListBySession(sess-b) = %v, %v; want 1", listB, err)
		}
	})
}

func TestConformanceBindingExpiryCascade(t *testing.T) {
	eachFabric(t, func(t *testing.T, svc extmsg.Services) {
		ctx := context.Background()
		ref := confRef("C-expiry")
		expired := time.Now().UTC().Add(-time.Hour)

		if _, err := svc.Bindings.Bind(ctx, confController(), extmsg.BindInput{
			Conversation: ref,
			SessionID:    "sess-exp",
			ExpiresAt:    &expired,
			Now:          expired.Add(-time.Hour),
		}); err != nil {
			t.Fatalf("Bind(expiring): %v", err)
		}
		// The binding-owned membership exists until the expiry cascade runs.
		before, err := svc.Transcript.ListConversationsBySession(ctx, confController(), "sess-exp")
		if err != nil || len(before) != 1 {
			t.Fatalf("memberships before expiry = %v, %v; want 1", before, err)
		}

		got, err := svc.Bindings.ResolveByConversation(ctx, ref)
		if err != nil {
			t.Fatalf("ResolveByConversation(expired): %v", err)
		}
		if got != nil {
			t.Fatalf("ResolveByConversation(expired) = %+v, want nil", got)
		}
		after, err := svc.Transcript.ListConversationsBySession(ctx, confController(), "sess-exp")
		if err != nil || len(after) != 0 {
			t.Fatalf("memberships after expiry = %v, %v; want 0 (cascade removed)", after, err)
		}

		// The expired history still counts for generation minting.
		next, err := svc.Bindings.Bind(ctx, confController(), extmsg.BindInput{
			Conversation: ref,
			SessionID:    "sess-next",
			Now:          time.Now().UTC(),
		})
		if err != nil {
			t.Fatalf("Bind(after expiry): %v", err)
		}
		if next.BindingGeneration != 2 {
			t.Fatalf("post-expiry generation = %d, want 2", next.BindingGeneration)
		}
	})
}

func TestConformanceUnbindCascade(t *testing.T) {
	eachFabric(t, func(t *testing.T, svc extmsg.Services) {
		ctx := context.Background()
		ref := confRef("C-unbind")

		bound, err := svc.Bindings.Bind(ctx, confController(), extmsg.BindInput{
			Conversation: ref,
			SessionID:    "sess-u",
			Now:          confNow(),
		})
		if err != nil {
			t.Fatalf("Bind: %v", err)
		}
		if err := svc.Delivery.Record(ctx, confController(), extmsg.DeliveryContextRecord{
			SessionID:         "sess-u",
			Conversation:      ref,
			BindingGeneration: bound.BindingGeneration,
			LastPublishedAt:   confNow(),
			LastMessageID:     "m-1",
		}); err != nil {
			t.Fatalf("Delivery.Record: %v", err)
		}

		closed, err := svc.Bindings.Unbind(ctx, confController(), extmsg.UnbindInput{
			Conversation: &ref,
			Now:          confNow().Add(time.Minute),
		})
		if err != nil {
			t.Fatalf("Unbind: %v", err)
		}
		if len(closed) != 1 || closed[0].Status != extmsg.BindingEnded {
			t.Fatalf("Unbind closed = %+v, want 1 ended", closed)
		}
		got, err := svc.Bindings.ResolveByConversation(ctx, ref)
		if err != nil || got != nil {
			t.Fatalf("post-unbind resolve = %+v, %v; want nil", got, err)
		}
		dc, err := svc.Delivery.Resolve(ctx, "sess-u", ref)
		if err != nil || dc != nil {
			t.Fatalf("post-unbind delivery = %+v, %v; want nil (cleared)", dc, err)
		}
		memberships, err := svc.Transcript.ListConversationsBySession(ctx, confController(), "sess-u")
		if err != nil || len(memberships) != 0 {
			t.Fatalf("post-unbind memberships = %v, %v; want 0", memberships, err)
		}
	})
}

func TestConformanceDeliveryContextGating(t *testing.T) {
	eachFabric(t, func(t *testing.T, svc extmsg.Services) {
		ctx := context.Background()
		ref := confRef("C-delivery")

		// No binding: recording is a mismatch.
		if err := svc.Delivery.Record(ctx, confController(), extmsg.DeliveryContextRecord{
			SessionID:         "sess-d",
			Conversation:      ref,
			BindingGeneration: 1,
		}); !errors.Is(err, extmsg.ErrBindingMismatch) {
			t.Fatalf("Record(no binding) err = %v, want ErrBindingMismatch", err)
		}

		bound, err := svc.Bindings.Bind(ctx, confController(), extmsg.BindInput{
			Conversation: ref,
			SessionID:    "sess-d",
			Now:          confNow(),
		})
		if err != nil {
			t.Fatalf("Bind: %v", err)
		}
		if err := svc.Delivery.Record(ctx, confController(), extmsg.DeliveryContextRecord{
			SessionID:         "sess-d",
			Conversation:      ref,
			BindingGeneration: bound.BindingGeneration,
			LastPublishedAt:   confNow(),
			LastMessageID:     "m-1",
		}); err != nil {
			t.Fatalf("Record: %v", err)
		}
		// A stale generation is rejected.
		if err := svc.Delivery.Record(ctx, confController(), extmsg.DeliveryContextRecord{
			SessionID:         "sess-d",
			Conversation:      ref,
			BindingGeneration: bound.BindingGeneration + 1,
		}); !errors.Is(err, extmsg.ErrBindingMismatch) {
			t.Fatalf("Record(stale gen) err = %v, want ErrBindingMismatch", err)
		}
		// Re-recording updates in place.
		if err := svc.Delivery.Record(ctx, confController(), extmsg.DeliveryContextRecord{
			SessionID:         "sess-d",
			Conversation:      ref,
			BindingGeneration: bound.BindingGeneration,
			LastPublishedAt:   confNow().Add(time.Minute),
			LastMessageID:     "m-2",
		}); err != nil {
			t.Fatalf("Record(update): %v", err)
		}
		dc, err := svc.Delivery.Resolve(ctx, "sess-d", ref)
		if err != nil || dc == nil || dc.LastMessageID != "m-2" {
			t.Fatalf("Resolve = %+v, %v; want m-2", dc, err)
		}

		// Handoff clears the displaced session's context.
		if _, err := svc.Bindings.Bind(ctx, confController(), extmsg.BindInput{
			Conversation: ref,
			SessionID:    "sess-d2",
			Replace:      true,
			Now:          confNow().Add(2 * time.Minute),
		}); err != nil {
			t.Fatalf("Bind(replace): %v", err)
		}
		dc, err = svc.Delivery.Resolve(ctx, "sess-d", ref)
		if err != nil || dc != nil {
			t.Fatalf("post-handoff Resolve(old) = %+v, %v; want nil", dc, err)
		}
	})
}

func TestConformanceGroupRoutingAndParticipants(t *testing.T) {
	eachFabric(t, func(t *testing.T, svc extmsg.Services) {
		ctx := context.Background()
		ref := confRef("C-group")

		group, err := svc.Groups.EnsureGroup(ctx, confController(), extmsg.EnsureGroupInput{
			RootConversation: ref,
			Mode:             extmsg.GroupModeLauncher,
			DefaultHandle:    "alpha",
		})
		if err != nil {
			t.Fatalf("EnsureGroup: %v", err)
		}
		if _, err := svc.Groups.UpsertParticipant(ctx, confController(), extmsg.UpsertParticipantInput{
			GroupID: group.ID, Handle: "alpha", SessionID: "sess-a",
		}); err != nil {
			t.Fatalf("UpsertParticipant(alpha): %v", err)
		}
		if _, err := svc.Groups.UpsertParticipant(ctx, confController(), extmsg.UpsertParticipantInput{
			GroupID: group.ID, Handle: "beta", SessionID: "sess-b",
		}); err != nil {
			t.Fatalf("UpsertParticipant(beta): %v", err)
		}

		inbound := func(explicit string) *extmsg.GroupRouteDecision {
			t.Helper()
			route, err := svc.Groups.ResolveInbound(ctx, extmsg.ExternalInboundMessage{
				Conversation:   ref,
				ExplicitTarget: explicit,
				Text:           "hi",
			})
			if err != nil {
				t.Fatalf("ResolveInbound(%q): %v", explicit, err)
			}
			return route
		}

		if route := inbound(""); route.Match != extmsg.GroupRouteDefault || route.TargetSessionID != "sess-a" {
			t.Fatalf("default route = %+v, want default->sess-a", route)
		}
		if err := svc.Groups.UpdateCursor(ctx, confController(), extmsg.UpdateCursorInput{
			RootConversation: ref, Handle: "beta",
		}); err != nil {
			t.Fatalf("UpdateCursor: %v", err)
		}
		if route := inbound(""); route.Match != extmsg.GroupRouteLastAddressed || route.TargetSessionID != "sess-b" {
			t.Fatalf("last-addressed route = %+v, want beta/sess-b", route)
		}
		if route := inbound("alpha"); route.Match != extmsg.GroupRouteExplicitTarget || route.TargetSessionID != "sess-a" || !route.UpdateCursor {
			t.Fatalf("explicit route = %+v, want alpha/sess-a + cursor update", route)
		}
		if route := inbound("ghost"); route.Match != extmsg.GroupRouteNoMatch {
			t.Fatalf("ghost route = %+v, want no-match", route)
		}

		out, err := svc.Groups.ResolveOutbound(ctx, ref, "sess-a")
		if err != nil || out.Match != extmsg.GroupRouteParticipantMatch {
			t.Fatalf("ResolveOutbound(sess-a) = %+v, %v; want participant match", out, err)
		}
		out, err = svc.Groups.ResolveOutbound(ctx, ref, "sess-x")
		if err != nil || out.Match != extmsg.GroupRouteNoMatch {
			t.Fatalf("ResolveOutbound(sess-x) = %+v, %v; want no-match", out, err)
		}

		// Group participants own transcript memberships for their sessions.
		members, err := svc.Transcript.ListMemberships(ctx, confController(), ref)
		if err != nil || len(members) != 2 {
			t.Fatalf("ListMemberships = %v, %v; want 2", members, err)
		}

		// Retargeting a handle carries the membership to the new session.
		if _, err := svc.Groups.UpsertParticipant(ctx, confController(), extmsg.UpsertParticipantInput{
			GroupID: group.ID, Handle: "alpha", SessionID: "sess-c",
		}); err != nil {
			t.Fatalf("UpsertParticipant(retarget): %v", err)
		}
		members, err = svc.Transcript.ListMemberships(ctx, confController(), ref)
		if err != nil {
			t.Fatalf("ListMemberships(after retarget): %v", err)
		}
		sessions := map[string]bool{}
		for _, m := range members {
			sessions[m.SessionID] = true
		}
		if !sessions["sess-c"] || !sessions["sess-b"] || sessions["sess-a"] {
			t.Fatalf("memberships after retarget = %v, want sess-b+sess-c only", sessions)
		}

		// Removal drops routing and the orphaned membership. Removing an
		// already-removed handle stays idempotent (the closed row still
		// matches); only a never-known handle reports no route.
		if err := svc.Groups.RemoveParticipant(ctx, confController(), extmsg.RemoveParticipantInput{
			GroupID: group.ID, Handle: "beta",
		}); err != nil {
			t.Fatalf("RemoveParticipant(beta): %v", err)
		}
		if err := svc.Groups.RemoveParticipant(ctx, confController(), extmsg.RemoveParticipantInput{
			GroupID: group.ID, Handle: "beta",
		}); err != nil {
			t.Fatalf("RemoveParticipant(again) err = %v, want idempotent nil", err)
		}
		if err := svc.Groups.RemoveParticipant(ctx, confController(), extmsg.RemoveParticipantInput{
			GroupID: group.ID, Handle: "never-existed",
		}); !errors.Is(err, extmsg.ErrGroupRouteNotFound) {
			t.Fatalf("RemoveParticipant(unknown) err = %v, want ErrGroupRouteNotFound", err)
		}
		members, err = svc.Transcript.ListMemberships(ctx, confController(), ref)
		if err != nil || len(members) != 1 || members[0].SessionID != "sess-c" {
			t.Fatalf("memberships after removal = %v, %v; want sess-c only", members, err)
		}

		// An EnsureGroup update with no cursor preserves the stored cursor
		// (the bd delete-from-fields semantics).
		if _, err := svc.Groups.EnsureGroup(ctx, confController(), extmsg.EnsureGroupInput{
			RootConversation: ref,
			Mode:             extmsg.GroupModeLauncher,
			DefaultHandle:    "gamma",
		}); err != nil {
			t.Fatalf("EnsureGroup(update): %v", err)
		}
		updated, err := svc.Groups.FindByConversation(ctx, confController(), ref)
		if err != nil {
			t.Fatalf("FindByConversation: %v", err)
		}
		if updated.DefaultHandle != "gamma" {
			t.Fatalf("updated default handle = %q, want gamma", updated.DefaultHandle)
		}
		// The cursor survives the update: ResolveInbound only REPORTS
		// UpdateCursor (the API pipeline applies it), so the stored cursor
		// is still the explicit UpdateCursor(beta) from above.
		if updated.LastAddressedHandle != "beta" {
			t.Fatalf("updated cursor = %q, want beta preserved", updated.LastAddressedHandle)
		}
	})
}

func TestConformanceTranscriptSequenceDedupeListAck(t *testing.T) {
	eachFabric(t, func(t *testing.T, svc extmsg.Services) {
		ctx := context.Background()
		ref := confRef("C-transcript")

		appendEntry := func(text, pmid string) extmsg.ConversationTranscriptRecord {
			t.Helper()
			entry, err := svc.Transcript.Append(ctx, extmsg.AppendTranscriptInput{
				Caller:            confController(),
				Conversation:      ref,
				Kind:              extmsg.TranscriptMessageInbound,
				ProviderMessageID: pmid,
				Actor:             extmsg.ExternalActor{ID: "U1", DisplayName: "Uma"},
				Text:              text,
				Attachments:       []extmsg.ExternalAttachment{{ProviderID: "f1", URL: "https://x/f1", MIMEType: "text/plain"}},
				CreatedAt:         confNow(),
			})
			if err != nil {
				t.Fatalf("Append(%q): %v", text, err)
			}
			return entry
		}

		if seq := appendEntry("one", "").Sequence; seq != 1 {
			t.Fatalf("first sequence = %d, want 1", seq)
		}
		if seq := appendEntry("two", "").Sequence; seq != 2 {
			t.Fatalf("second sequence = %d, want 2", seq)
		}
		three := appendEntry("three", "pm-3")
		if three.Sequence != 3 {
			t.Fatalf("third sequence = %d, want 3", three.Sequence)
		}
		// Same provider message id: the existing entry is returned, no new
		// sequence is burned.
		dup := appendEntry("three again", "pm-3")
		if dup.ID != three.ID || dup.Sequence != 3 {
			t.Fatalf("dup append = %+v, want the original pm-3 entry", dup)
		}
		if seq := appendEntry("four", "").Sequence; seq != 4 {
			t.Fatalf("post-dup sequence = %d, want 4", seq)
		}

		list, err := svc.Transcript.List(ctx, extmsg.ListTranscriptInput{
			Caller: confController(), Conversation: ref,
		})
		if err != nil || len(list) != 4 {
			t.Fatalf("List = %d entries, %v; want 4", len(list), err)
		}
		if list[0].Actor.DisplayName != "Uma" || len(list[0].Attachments) != 1 || list[0].Attachments[0].URL != "https://x/f1" {
			t.Fatalf("entry round-trip = %+v, want actor+attachment preserved", list[0])
		}
		if list[0].Text != "one" || list[3].Text != "four" {
			t.Fatalf("ascending order = [%q..%q], want one..four", list[0].Text, list[3].Text)
		}

		desc, err := svc.Transcript.List(ctx, extmsg.ListTranscriptInput{
			Caller: confController(), Conversation: ref, Order: extmsg.TranscriptOrderDesc, Limit: 2,
		})
		if err != nil || len(desc) != 2 || desc[0].Sequence != 4 || desc[1].Sequence != 3 {
			t.Fatalf("desc list = %+v, %v; want [4,3]", desc, err)
		}

		after, err := svc.Transcript.List(ctx, extmsg.ListTranscriptInput{
			Caller: confController(), Conversation: ref, AfterSequence: 2,
		})
		if err != nil || len(after) != 2 || after[0].Sequence != 3 {
			t.Fatalf("after=2 list = %+v, %v; want [3,4]", after, err)
		}

		// Membership + ack + backfill.
		if _, err := svc.Transcript.EnsureMembership(ctx, extmsg.EnsureMembershipInput{
			Caller:       confController(),
			Conversation: ref,
			SessionID:    "sess-reader",
			Owner:        extmsg.MembershipOwnerManual,
			Now:          confNow(),
		}); err != nil {
			t.Fatalf("EnsureMembership: %v", err)
		}
		if err := svc.Transcript.Ack(ctx, extmsg.AckMembershipInput{
			Caller: confController(), Conversation: ref, SessionID: "sess-reader", Sequence: 10,
		}); err == nil {
			t.Fatalf("Ack beyond head succeeded, want error")
		}
		if err := svc.Transcript.Ack(ctx, extmsg.AckMembershipInput{
			Caller: confController(), Conversation: ref, SessionID: "sess-reader", Sequence: 2,
		}); err != nil {
			t.Fatalf("Ack(2): %v", err)
		}
		backfill, err := svc.Transcript.ListBackfill(ctx, extmsg.ListBackfillInput{
			Caller: confController(), Conversation: ref, SessionID: "sess-reader",
		})
		if err != nil || len(backfill) != 2 || backfill[0].Sequence != 3 {
			t.Fatalf("backfill after ack(2) = %+v, %v; want [3,4]", backfill, err)
		}

		// A since-join member only sees entries after its join point.
		if _, err := svc.Transcript.EnsureMembership(ctx, extmsg.EnsureMembershipInput{
			Caller:         confController(),
			Conversation:   ref,
			SessionID:      "sess-late",
			BackfillPolicy: extmsg.MembershipBackfillSinceJoin,
			Owner:          extmsg.MembershipOwnerBinding,
			Now:            confNow(),
		}); err != nil {
			t.Fatalf("EnsureMembership(late): %v", err)
		}
		if seq := appendEntry("five", "").Sequence; seq != 5 {
			t.Fatalf("fifth sequence = %d, want 5", seq)
		}
		late, err := svc.Transcript.ListBackfill(ctx, extmsg.ListBackfillInput{
			Caller: confController(), Conversation: ref, SessionID: "sess-late",
		})
		if err != nil || len(late) != 1 || late[0].Sequence != 5 {
			t.Fatalf("since-join backfill = %+v, %v; want [5]", late, err)
		}
	})
}

func TestConformanceHydrationGates(t *testing.T) {
	eachFabric(t, func(t *testing.T, svc extmsg.Services) {
		ctx := context.Background()
		ref := confRef("C-hydration")

		state, err := svc.Transcript.BeginHydration(ctx, confController(), ref, nil)
		if err != nil {
			t.Fatalf("BeginHydration: %v", err)
		}
		if state.HydrationStatus != extmsg.HydrationPending {
			t.Fatalf("hydration status = %q, want pending", state.HydrationStatus)
		}

		if _, err := svc.Transcript.Append(ctx, extmsg.AppendTranscriptInput{
			Caller:       confController(),
			Conversation: ref,
			Kind:         extmsg.TranscriptMessageInbound,
			Text:         "live during hydration",
			CreatedAt:    confNow(),
		}); !errors.Is(err, extmsg.ErrHydrationPending) {
			t.Fatalf("live append during hydration err = %v, want ErrHydrationPending", err)
		}
		if _, err := svc.Transcript.Append(ctx, extmsg.AppendTranscriptInput{
			Caller:       confController(),
			Conversation: ref,
			Kind:         extmsg.TranscriptMessageInbound,
			Provenance:   extmsg.TranscriptProvenanceHydrated,
			Text:         "backfilled",
			CreatedAt:    confNow(),
		}); err != nil {
			t.Fatalf("hydrated append: %v", err)
		}

		state, err = svc.Transcript.CompleteHydration(ctx, confController(), ref)
		if err != nil || state.HydrationStatus != extmsg.HydrationComplete {
			t.Fatalf("CompleteHydration = %+v, %v; want complete", state, err)
		}
		if _, err := svc.Transcript.Append(ctx, extmsg.AppendTranscriptInput{
			Caller:       confController(),
			Conversation: ref,
			Kind:         extmsg.TranscriptMessageInbound,
			Text:         "live after hydration",
			CreatedAt:    confNow(),
		}); err != nil {
			t.Fatalf("live append after hydration: %v", err)
		}
		// Live traffic blocks re-entering hydration.
		if _, err := svc.Transcript.BeginHydration(ctx, confController(), ref, nil); err == nil {
			t.Fatalf("BeginHydration after live traffic succeeded, want error")
		}
	})
}

func TestConformanceMembershipOwnerAlgebra(t *testing.T) {
	eachFabric(t, func(t *testing.T, svc extmsg.Services) {
		ctx := context.Background()
		ref := confRef("C-owners")

		// Binding + manual owners stack on one membership; removing one
		// owner keeps the membership open until the last owner leaves.
		if _, err := svc.Bindings.Bind(ctx, confController(), extmsg.BindInput{
			Conversation: ref,
			SessionID:    "sess-o",
			Now:          confNow(),
		}); err != nil {
			t.Fatalf("Bind: %v", err)
		}
		if _, err := svc.Transcript.EnsureMembership(ctx, extmsg.EnsureMembershipInput{
			Caller:       confController(),
			Conversation: ref,
			SessionID:    "sess-o",
			Owner:        extmsg.MembershipOwnerManual,
			Now:          confNow(),
		}); err != nil {
			t.Fatalf("EnsureMembership(manual): %v", err)
		}
		members, err := svc.Transcript.ListMemberships(ctx, confController(), ref)
		if err != nil || len(members) != 1 || len(members[0].Owners) != 2 {
			t.Fatalf("stacked membership = %+v, %v; want one row, two owners", members, err)
		}

		if err := svc.Transcript.RemoveMembership(ctx, extmsg.RemoveMembershipInput{
			Caller:       confController(),
			Conversation: ref,
			SessionID:    "sess-o",
			Owner:        extmsg.MembershipOwnerManual,
			Now:          confNow(),
		}); err != nil {
			t.Fatalf("RemoveMembership(manual): %v", err)
		}
		members, err = svc.Transcript.ListMemberships(ctx, confController(), ref)
		if err != nil || len(members) != 1 || len(members[0].Owners) != 1 {
			t.Fatalf("after manual removal = %+v, %v; want binding owner only", members, err)
		}

		// Unbind removes the last owner and closes the membership.
		if _, err := svc.Bindings.Unbind(ctx, confController(), extmsg.UnbindInput{
			Conversation: &ref,
			Now:          confNow().Add(time.Minute),
		}); err != nil {
			t.Fatalf("Unbind: %v", err)
		}
		members, err = svc.Transcript.ListMemberships(ctx, confController(), ref)
		if err != nil || len(members) != 0 {
			t.Fatalf("after unbind = %+v, %v; want no memberships", members, err)
		}
	})
}
