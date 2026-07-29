package sessionsdb

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/gastownhall/gascity/internal/beads"
)

func newTxTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "sessions.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.CloseStore() })
	return s
}

// TestStoreTxRollbackPendingCreateShape pins the exact multi-step write the
// session reconciler's pending-create rollback performs (metadata clears +
// close + post-close clears in ONE transaction) — the sequence that wedged
// implementation-worker pools when Tx was unsupported: rollbacks failed every
// tick, zombie pending creates held pool slots forever, and no fresh spawn
// could start.
func TestStoreTxRollbackPendingCreateShape(t *testing.T) {
	s := newTxTestStore(t)
	b, err := s.Create(beads.Bead{Title: "worker-1", Type: "session", Metadata: map[string]string{
		"last_woke_at": "2026-07-29T00:00:00Z",
		"session_name": "gc__implementation-worker-x",
	}})
	if err != nil {
		t.Fatal(err)
	}
	err = s.Tx("rollback pending-create", func(tx beads.Tx) error {
		if err := tx.SetMetadataBatch(b.ID, map[string]string{"last_woke_at": ""}); err != nil {
			return err
		}
		if err := tx.Close(b.ID); err != nil {
			return err
		}
		// The post-close clear must see the tx's own earlier writes.
		return tx.SetMetadataBatch(b.ID, map[string]string{"session_name": ""})
	})
	if err != nil {
		t.Fatalf("Tx: %v", err)
	}
	got, err := s.Get(b.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "closed" {
		t.Fatalf("status = %q, want closed", got.Status)
	}
	if got.Metadata["last_woke_at"] != "" || got.Metadata["session_name"] != "" {
		t.Fatalf("clears lost across tx steps: %+v", got.Metadata)
	}
}

// TestStoreTxAtomicRollback pins atomicity: a failing step rolls back every
// earlier step's write.
func TestStoreTxAtomicRollback(t *testing.T) {
	s := newTxTestStore(t)
	b, err := s.Create(beads.Bead{Title: "worker-2", Type: "session", Metadata: map[string]string{"k": "v"}})
	if err != nil {
		t.Fatal(err)
	}
	boom := errors.New("boom")
	err = s.Tx("failing", func(tx beads.Tx) error {
		if err := tx.SetMetadataBatch(b.ID, map[string]string{"k": "changed"}); err != nil {
			return err
		}
		if err := tx.Close(b.ID); err != nil {
			return err
		}
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("Tx error = %v, want boom", err)
	}
	got, err := s.Get(b.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "open" || got.Metadata["k"] != "v" {
		t.Fatalf("tx not rolled back: status=%q metadata=%+v", got.Status, got.Metadata)
	}
}

// TestStoreTxCreateMintsAndCommits covers the Tx Create arm (id mint inside
// the transaction) and out-of-surface Update options failing loud.
func TestStoreTxCreateMintsAndCommits(t *testing.T) {
	s := newTxTestStore(t)
	var created beads.Bead
	err := s.Tx("create", func(tx beads.Tx) error {
		b, err := tx.Create(beads.Bead{Title: "in-tx", Type: "session"})
		if err != nil {
			return err
		}
		created = b
		pri := 1
		if err := tx.Update(b.ID, beads.UpdateOpts{Priority: &pri}); !errors.Is(err, ErrUnsupported) {
			return errors.New("Update with Priority must stay unsupported inside Tx")
		}
		title := "renamed"
		return tx.Update(b.ID, beads.UpdateOpts{Title: &title})
	})
	if err != nil {
		t.Fatalf("Tx: %v", err)
	}
	got, err := s.Get(created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Title != "renamed" {
		t.Fatalf("title = %q, want renamed", got.Title)
	}
}
