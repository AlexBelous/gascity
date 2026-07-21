package messagingdb

// Both-backend proof for the messaging class. The provider-level suite runs
// the FULL mail.Provider conformance (mailtest) over beadmail with this
// store as its messages backend — the same suite the bd backend passes —
// and the retention tests pin the 6b0eb0d6b addressability contract at the
// store level (swept-still-addressable vs user-removed) on both backends
// through the public beadmail surface.

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/mail"
	"github.com/gastownhall/gascity/internal/mail/beadmail"
	"github.com/gastownhall/gascity/internal/mail/mailtest"
)

func openTestStore(t *testing.T) *Store {
	t.Helper()
	st, err := Open(filepath.Join(t.TempDir(), "messaging.db"))
	if err != nil {
		t.Fatalf("open messaging store: %v", err)
	}
	t.Cleanup(func() {
		if err := st.Close(); err != nil {
			t.Errorf("close messaging store: %v", err)
		}
	})
	return st
}

// TestMailProviderConformanceSQLite runs the full mail.Provider conformance
// suite over beadmail backed by the sqlite messages store (session
// addressing on an empty MemStore, exactly like the bd conformance run).
func TestMailProviderConformanceSQLite(t *testing.T) {
	mailtest.RunProviderTests(t, func(t *testing.T) mail.Provider {
		return beadmail.NewWithBackend(openTestStore(t), beads.NewMemStore())
	})
}

// eachBackend runs fn against a fresh beadmail provider per backend — the
// portable harness for behavior both backends must share beyond the
// provider conformance suite.
func eachBackend(t *testing.T, fn func(t *testing.T, p *beadmail.Provider)) {
	t.Helper()
	t.Run("beads", func(t *testing.T) {
		fn(t, beadmail.New(beads.NewMemStore()))
	})
	t.Run("sqlite", func(t *testing.T) {
		fn(t, beadmail.NewWithBackend(openTestStore(t), beads.NewMemStore()))
	})
}

// The retention-swept addressability contract: read mail closed with the
// retention reason stays addressable by direct id (Get/Reply) while leaving
// the aggregate views; the count and inbox no longer see it.
func TestRetentionSweptMailStaysAddressable(t *testing.T) {
	eachBackend(t, func(t *testing.T, p *beadmail.Provider) {
		sent, err := p.Send("alice", "bob", "aging", "old news")
		if err != nil {
			t.Fatalf("Send: %v", err)
		}
		if _, err := p.Read(sent.ID); err != nil {
			t.Fatalf("Read: %v", err)
		}

		closed, closeErrs, listErr := p.SweepReadMessages(time.Now().Add(time.Hour), 0, beadmail.RetentionSweepCloseReason)
		if listErr != nil || len(closeErrs) > 0 {
			t.Fatalf("sweep: closed=%d closeErrs=%v listErr=%v", closed, closeErrs, listErr)
		}
		if closed != 1 {
			t.Fatalf("sweep closed = %d, want 1", closed)
		}

		// Direct-ID reads still resolve the system-aged message.
		got, err := p.Get(sent.ID)
		if err != nil {
			t.Fatalf("Get after retention sweep: %v (swept mail must stay addressable)", err)
		}
		if got.Subject != "aging" {
			t.Fatalf("Get after sweep = %+v", got)
		}
		if _, err := p.Reply(sent.ID, "bob", "re", "still here"); err != nil {
			t.Fatalf("Reply after retention sweep: %v", err)
		}
		// Aggregate views no longer include it.
		if total, _, err := p.Count("bob"); err != nil || total != 0 {
			t.Fatalf("Count after sweep = %d, %v; want 0", total, err)
		}
	})
}

// User-removed mail (eager archive) is not-found everywhere.
func TestUserRemovedMailIsNotFound(t *testing.T) {
	eachBackend(t, func(t *testing.T, p *beadmail.Provider) {
		sent, err := p.Send("alice", "bob", "gone", "bye")
		if err != nil {
			t.Fatalf("Send: %v", err)
		}
		if err := p.Archive(sent.ID); err != nil {
			t.Fatalf("Archive: %v", err)
		}
		if _, err := p.Get(sent.ID); err == nil {
			t.Fatal("Get after archive succeeded; user-removed mail must be not-found")
		}
	})
}

// The read-mail purge removes consumed mail past the cutoff on both
// backends, leaving unread mail alone.
func TestReadMailPurge(t *testing.T) {
	eachBackend(t, func(t *testing.T, p *beadmail.Provider) {
		read, err := p.Send("alice", "bob", "consumed", "read me")
		if err != nil {
			t.Fatalf("Send: %v", err)
		}
		if _, err := p.Read(read.ID); err != nil {
			t.Fatalf("Read: %v", err)
		}
		if _, err := p.Send("alice", "bob", "fresh", "unread"); err != nil {
			t.Fatalf("Send: %v", err)
		}

		purged, err := p.PurgeReadMessages(time.Now().Add(time.Hour))
		if err != nil {
			t.Fatalf("purge: %v", err)
		}
		if purged != 1 {
			t.Fatalf("purged = %d, want 1 (the read message only)", purged)
		}
		if _, err := p.Get(read.ID); err == nil {
			t.Fatal("purged read message still addressable")
		}
		if total, unread, err := p.Count("bob"); err != nil || total != 1 || unread != 1 {
			t.Fatalf("Count after purge = (%d, %d, %v); want the unread message intact", total, unread, err)
		}
	})
}
