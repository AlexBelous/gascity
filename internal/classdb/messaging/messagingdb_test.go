package messagingdb

import (
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/config"
	"github.com/gastownhall/gascity/internal/mail/beadmail"
)

// The minted-id prefix must stay in lockstep with the config registry's
// reserved messaging-class prefix.
func TestIDPrefixMatchesReservedClassPrefix(t *testing.T) {
	want, ok := config.ReservedClassPrefix(config.BeadClassMessaging)
	if !ok {
		t.Fatal("config.ReservedClassPrefix(messaging) not registered")
	}
	if got := openTestStore(t).IDPrefix(); got != want {
		t.Fatalf("IDPrefix() = %q, want reserved messaging prefix %q", got, want)
	}
}

func TestCreateMintsPrefixedIDsAndMapsHandoffFlags(t *testing.T) {
	st := openTestStore(t)
	first, err := st.Create(beadmail.NewMessage{Subject: "a", Body: "b", From: "alice", To: "bob", ThreadID: "t1"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if first.ID != "gcm-1" {
		t.Fatalf("first minted id = %q, want gcm-1", first.ID)
	}
	handoff, err := st.Create(beadmail.NewMessage{
		Subject: "h", Body: "b", From: "alice", To: "bob", ThreadID: "t2",
		ExtraLabels: []string{"gc:auto-handoff", "gc:archive-after-inject", "gc:something-else"},
	})
	if err != nil {
		t.Fatalf("Create handoff: %v", err)
	}
	if !handoff.AutoHandoff || !handoff.ArchiveAfterInject {
		t.Fatalf("handoff flags not mapped: %+v", handoff)
	}
	rec, ok, err := st.Get(handoff.ID)
	if err != nil || !ok || !rec.AutoHandoff || !rec.ArchiveAfterInject {
		t.Fatalf("Get(handoff) = %+v (%v, %v), want persisted flags", rec, ok, err)
	}
}

// The net-new 30d unread TTL: unread messages past the cutoff are dropped,
// read and fresh ones survive.
func TestSweepUnreadBefore(t *testing.T) {
	st := openTestStore(t)
	old, err := st.Create(beadmail.NewMessage{Subject: "stale", Body: "b", From: "a", To: "bob", ThreadID: "t1"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	fresh, err := st.Create(beadmail.NewMessage{Subject: "fresh", Body: "b", From: "a", To: "bob", ThreadID: "t2"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	readMsg, err := st.Create(beadmail.NewMessage{Subject: "kept", Body: "b", From: "a", To: "bob", ThreadID: "t3"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := st.SetRead(readMsg.ID, true); err != nil {
		t.Fatalf("SetRead: %v", err)
	}

	// Only the first unread message is older than the cutoff placed between
	// the creates' clocks — backdate it via import-style rewrite instead:
	// simplest deterministic cutoff = just after old's CreatedAt.
	cutoff := old.CreatedAt.Add(time.Nanosecond)
	if !fresh.CreatedAt.After(old.CreatedAt) {
		t.Fatalf("fixture clocks not distinct: old=%v fresh=%v", old.CreatedAt, fresh.CreatedAt)
	}
	// Make the read message older than the cutoff too: prove read mail is
	// exempt from the unread sweep regardless of age.
	backdated := beadmail.Record{
		ID: "gcm-backdated-read", ThreadID: "t4", FromAddr: "a", ToAddr: "bob",
		Subject: "old but read", Body: "b", CreatedAt: old.CreatedAt.Add(-time.Hour),
		Read: true, ReadLabel: true, Open: true,
	}
	if err := st.ImportMessage(backdated); err != nil {
		t.Fatalf("ImportMessage: %v", err)
	}

	dropped, err := st.SweepUnreadBefore(cutoff)
	if err != nil {
		t.Fatalf("SweepUnreadBefore: %v", err)
	}
	if dropped != 1 {
		t.Fatalf("dropped = %d, want 1 (the stale unread message)", dropped)
	}
	if _, ok, _ := st.Get(old.ID); ok {
		t.Fatal("stale unread message survived the unread sweep")
	}
	for _, id := range []string{fresh.ID, readMsg.ID, "gcm-backdated-read"} {
		if _, ok, err := st.Get(id); err != nil || !ok {
			t.Fatalf("message %s lost by the unread sweep (%v, %v)", id, ok, err)
		}
	}
}

// ImportMessage preserves ids, clocks, and lifecycle verbatim and is
// idempotent.
func TestImportMessageRoundTrip(t *testing.T) {
	st := openTestStore(t)
	created := time.Now().Add(-48 * time.Hour).UTC()
	rec := beadmail.Record{
		ID: "ga-legacy1", ThreadID: "t1", ReplyToID: "ga-orig",
		FromAddr: "ga-sess1", ToAddr: "bob", FromSessionID: "ga-sess1", FromDisplay: "alice",
		Subject: "old", Body: "body", CreatedAt: created,
		Read: true, ReadLabel: true, Open: false,
		CloseReason: beadmail.RetentionSweepCloseReason,
	}
	if err := st.ImportMessage(rec); err != nil {
		t.Fatalf("ImportMessage: %v", err)
	}
	// Re-import with different content: the existing row wins.
	changed := rec
	changed.Subject = "mutated"
	if err := st.ImportMessage(changed); err != nil {
		t.Fatalf("ImportMessage re-import: %v", err)
	}
	got, ok, err := st.Get("ga-legacy1")
	if err != nil || !ok {
		t.Fatalf("Get imported = %v, %v", ok, err)
	}
	if got.Subject != "old" || !got.CreatedAt.Equal(created) || got.Open || !got.Read {
		t.Fatalf("import round-trip mutated the record: %+v", got)
	}
	if got.CloseReason != beadmail.RetentionSweepCloseReason {
		t.Fatalf("import lost the close reason: %q", got.CloseReason)
	}
}

// Native counts agree with the list view.
func TestCountOpenForRecipientsMatchesList(t *testing.T) {
	st := openTestStore(t)
	for i, to := range []string{"bob", "bob", "carol"} {
		msg, err := st.Create(beadmail.NewMessage{Subject: "m", Body: "b", From: "a", To: to, ThreadID: "t"})
		if err != nil {
			t.Fatalf("Create: %v", err)
		}
		if i == 0 {
			if err := st.SetRead(msg.ID, true); err != nil {
				t.Fatalf("SetRead: %v", err)
			}
		}
	}
	total, unread, err := st.CountOpenForRecipients([]string{"bob"})
	if err != nil {
		t.Fatalf("CountOpenForRecipients: %v", err)
	}
	if total != 2 || unread != 1 {
		t.Fatalf("counts = (%d, %d), want (2, 1)", total, unread)
	}
	all, err := st.ListOpenForRecipients([]string{"bob"}, true)
	if err != nil || len(all) != total {
		t.Fatalf("list/count disagree: %d vs %d (%v)", len(all), total, err)
	}
}
