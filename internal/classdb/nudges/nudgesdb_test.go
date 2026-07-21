package nudgesdb

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/classdb/core"
	"github.com/gastownhall/gascity/internal/nudgequeue"
)

func openTestStore(t *testing.T) *Store {
	t.Helper()
	st, err := Open(filepath.Join(t.TempDir(), "nudges.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() {
		if err := st.Close(); err != nil {
			t.Errorf("close: %v", err)
		}
	})
	return st
}

// TestPersistenceAcrossReopen proves queue rows survive close + reopen — the
// file is the queue.
func TestPersistenceAcrossReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nudges.db")
	st, err := Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	now := time.Now()
	item := nudgequeue.Item{
		ID: "nudge-1", Agent: "boot/dev", Source: "session", Message: "wake",
		SessionID: "gc-s1", ContinuationEpoch: "epoch-1", BeadID: "gc-shadow-1",
		Reference: &nudgequeue.Reference{Kind: "bead", ID: "gc-wait-1"},
		CreatedAt: now.UTC(), DeliverAfter: now.UTC(), ExpiresAt: now.Add(nudgequeue.DefaultTTL).UTC(),
	}
	if err := st.Enqueue(item, beads.NudgesStore{}); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	reopened, err := Open(path, core.WithSingleConn())
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer reopened.Close() //nolint:errcheck
	state, err := reopened.Snapshot()
	if err != nil || len(state.Pending) != 1 {
		t.Fatalf("Snapshot after reopen = (%+v, %v), want the persisted item", state, err)
	}
	got := state.Pending[0]
	if got.ID != item.ID || got.BeadID != "gc-shadow-1" || got.SessionID != "gc-s1" ||
		got.ContinuationEpoch != "epoch-1" || got.Reference == nil || got.Reference.ID != "gc-wait-1" ||
		!got.CreatedAt.Equal(item.CreatedAt) || !got.ExpiresAt.Equal(item.ExpiresAt) {
		t.Fatalf("round-trip = %+v, want %+v", got, item)
	}
}

// TestFindRecordLiveVsTerminal pins the merged-model replacement for the
// shadow Find/FindIncludingTerminal split: live rows visible to both, acked
// rows only to the terminal read, with the terminal stamps carried.
func TestFindRecordLiveVsTerminal(t *testing.T) {
	st := openTestStore(t)
	now := time.Now()
	if err := st.Enqueue(nudgequeue.Item{
		ID: "nudge-1", Agent: "boot/dev", Source: "wait", Message: "ready",
		CreatedAt: now.UTC(), DeliverAfter: now.UTC(), ExpiresAt: now.Add(time.Hour).UTC(),
	}, beads.NudgesStore{}); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	rec, ok, err := st.FindRecord("nudge-1")
	if err != nil || !ok || rec.QueueState != "pending" {
		t.Fatalf("FindRecord(live) = (%+v, %v, %v), want pending", rec, ok, err)
	}

	if err := st.Ack([]string{"nudge-1"}, "injected", "", "provider-nudge-return"); err != nil {
		t.Fatalf("Ack: %v", err)
	}
	if _, ok, err := st.FindRecord("nudge-1"); err != nil || ok {
		t.Fatalf("FindRecord(acked) = (ok=%v, %v), want not found (live read)", ok, err)
	}
	rec, ok, err = st.FindRecordIncludingTerminal("nudge-1")
	if err != nil || !ok {
		t.Fatalf("FindRecordIncludingTerminal = (ok=%v, %v)", ok, err)
	}
	if rec.QueueState != "terminal" || rec.TerminalState != "injected" ||
		rec.CommitBoundary != "provider-nudge-return" || rec.TerminalAt.IsZero() {
		t.Fatalf("terminal record = %+v, want injected/provider-nudge-return stamps", rec)
	}
}

// TestDeadRowsAgeIntoTerminalAndSweep pins the retention pipeline: dead rows
// hold for DeadRetention (the 1h dead bucket), age into terminal on
// maintenance, and SweepRetention deletes terminal rows past the TTL.
func TestDeadRowsAgeIntoTerminalAndSweep(t *testing.T) {
	st := openTestStore(t)
	now := time.Now()
	if err := st.Enqueue(nudgequeue.Item{
		ID: "nudge-1", Agent: "boot/dev", Source: "session", Message: "wake",
		CreatedAt: now.UTC(), DeliverAfter: now.UTC(), ExpiresAt: now.Add(time.Hour).UTC(),
	}, beads.NudgesStore{}); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	if _, err := st.RecordFailure([]string{"nudge-1"}, beads.NudgesStore{}, nudgequeue.ErrSessionFenceMismatch, now, nil); err != nil {
		t.Fatalf("RecordFailure: %v", err)
	}
	state, err := st.Snapshot()
	if err != nil || len(state.Dead) != 1 {
		t.Fatalf("Snapshot dead = (%+v, %v), want the dead-lettered item", state, err)
	}

	// Within retention the dead row survives sweeps.
	if n, err := st.SweepRetention(context.Background(), now, nudgequeue.DefaultTTL); err != nil || n != 0 {
		t.Fatalf("SweepRetention(now) = (%d, %v), want 0 deletions", n, err)
	}

	// Past DeadRetention the row ages into terminal (leaves the dead bucket)…
	afterDead := now.Add(nudgequeue.DeadRetention + time.Minute)
	if n, err := st.SweepRetention(context.Background(), afterDead, nudgequeue.DefaultTTL); err != nil || n != 0 {
		t.Fatalf("SweepRetention(afterDead) = (%d, %v), want aged not deleted", n, err)
	}
	state, err = st.Snapshot()
	if err != nil || len(state.Dead) != 0 {
		t.Fatalf("dead bucket after aging = (%+v, %v), want empty", state.Dead, err)
	}
	rec, ok, err := st.FindRecordIncludingTerminal("nudge-1")
	if err != nil || !ok || rec.QueueState != "terminal" {
		t.Fatalf("aged record = (%+v, ok=%v, %v), want terminal", rec, ok, err)
	}

	// …and past the terminal TTL the sweeper deletes it.
	afterTTL := afterDead.Add(nudgequeue.DefaultTTL + time.Minute)
	n, err := st.SweepRetention(context.Background(), afterTTL, nudgequeue.DefaultTTL)
	if err != nil || n != 1 {
		t.Fatalf("SweepRetention(afterTTL) = (%d, %v), want 1 deletion", n, err)
	}
	if _, ok, err := st.FindRecordIncludingTerminal("nudge-1"); err != nil || ok {
		t.Fatalf("record survived retention sweep (ok=%v, %v)", ok, err)
	}
}

// TestRollbackFallbackWritesTerminalRecord pins the shadow-only fallback of
// the file backend in merged form: rolling back an item that is not queued
// still leaves a durable terminal failure record.
func TestRollbackFallbackWritesTerminalRecord(t *testing.T) {
	st := openTestStore(t)
	now := time.Now()
	item := nudgequeue.Item{
		ID: "nudge-ghost", Agent: "boot/dev", Source: "session", Message: "wake",
		CreatedAt: now.UTC(), DeliverAfter: now.UTC(), ExpiresAt: now.Add(time.Hour).UTC(),
	}
	if err := st.Rollback(nil, item, "wake failed after enqueue"); err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	rec, ok, err := st.FindRecordIncludingTerminal("nudge-ghost")
	if err != nil || !ok || rec.QueueState != "terminal" || rec.TerminalState != "failed" {
		t.Fatalf("fallback record = (%+v, ok=%v, %v), want terminal failed", rec, ok, err)
	}
}
