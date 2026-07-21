package nudgesdb

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/nudgequeue"
)

func importTestStore(t *testing.T) *Store {
	t.Helper()
	st, err := Open(filepath.Join(t.TempDir(), "nudges.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() {
		if err := st.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	return st
}

func TestImportItemPreservesBucketsAndClocks(t *testing.T) {
	st := importTestStore(t)
	now := time.Now().UTC().Truncate(time.Nanosecond)

	pending := routingTestItem("nudge-p", "boot/dev", now.Add(-time.Hour))
	pending.BeadID = "ga-legacy1"
	pending.Attempts = 2
	pending.LastError = "boom"
	if err := st.ImportItem(pending, "pending"); err != nil {
		t.Fatalf("ImportItem pending: %v", err)
	}
	inFlight := routingTestItem("nudge-i", "boot/dev", now)
	inFlight.ClaimedAt = now
	inFlight.LeaseUntil = now.Add(nudgequeue.ClaimLeaseTTL)
	if err := st.ImportItem(inFlight, "in_flight"); err != nil {
		t.Fatalf("ImportItem in_flight: %v", err)
	}
	dead := routingTestItem("nudge-d", "boot/dev", now.Add(-2*time.Hour))
	dead.DeadAt = now.Add(-time.Minute)
	dead.LastError = "boom: delivery failed"
	if err := st.ImportItem(dead, "dead"); err != nil {
		t.Fatalf("ImportItem dead: %v", err)
	}
	if err := st.ImportItem(routingTestItem("nudge-p", "other/agent", now), "pending"); err != nil {
		t.Fatalf("ImportItem duplicate: %v", err)
	}
	if err := st.ImportItem(routingTestItem("nudge-bad", "boot/dev", now), "terminal"); err == nil {
		t.Fatal("ImportItem accepted an invalid bucket")
	}

	snap, err := st.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if len(snap.Pending) != 1 || len(snap.InFlight) != 1 || len(snap.Dead) != 1 {
		t.Fatalf("snapshot buckets = %d/%d/%d, want 1/1/1", len(snap.Pending), len(snap.InFlight), len(snap.Dead))
	}
	got := snap.Pending[0]
	if got.Agent != "boot/dev" || got.BeadID != "ga-legacy1" || got.Attempts != 2 || got.LastError != "boom" {
		t.Fatalf("re-import mutated the existing row: %+v", got)
	}
	if !got.CreatedAt.Equal(pending.CreatedAt) {
		t.Fatalf("CreatedAt = %v, want %v", got.CreatedAt, pending.CreatedAt)
	}
	if !snap.Dead[0].DeadAt.Equal(dead.DeadAt) {
		t.Fatalf("DeadAt = %v, want %v", snap.Dead[0].DeadAt, dead.DeadAt)
	}

	// Dead imports carry their terminal stamps immediately (the merged-model
	// dead shape), so routed wait finalization does not wait out the 1h
	// DeadRetention aging pass.
	rec, ok, err := st.FindRecord("nudge-d")
	if err != nil || !ok {
		t.Fatalf("FindRecord(dead) = %v, %v", ok, err)
	}
	if rec.TerminalState != "failed" || rec.TerminalReason != "boom: delivery failed" {
		t.Fatalf("imported dead row terminal stamps = %+v, want failed/boom", rec)
	}
	if !rec.TerminalAt.Equal(dead.DeadAt) {
		t.Fatalf("imported dead row TerminalAt = %v, want DeadAt %v", rec.TerminalAt, dead.DeadAt)
	}
	if s := rec.Shadow(); s.Open || s.State != "failed" {
		t.Fatalf("dead import Shadow() = %+v, want closed failed", s)
	}
}

// ResetLive is the pre-marker convergence primitive: it clears every live
// bucket while terminal history survives.
func TestResetLiveKeepsTerminalHistory(t *testing.T) {
	st := importTestStore(t)
	now := time.Now().UTC()
	if err := st.ImportItem(routingTestItem("nudge-p", "boot/dev", now), "pending"); err != nil {
		t.Fatalf("ImportItem: %v", err)
	}
	dead := routingTestItem("nudge-d", "boot/dev", now)
	dead.DeadAt = now
	if err := st.ImportItem(dead, "dead"); err != nil {
		t.Fatalf("ImportItem dead: %v", err)
	}
	if err := st.ImportTerminalShadow(nudgequeue.NudgeShadow{ID: "nudge-t", State: "injected"}, now, now); err != nil {
		t.Fatalf("ImportTerminalShadow: %v", err)
	}

	deleted, err := st.ResetLive()
	if err != nil {
		t.Fatalf("ResetLive: %v", err)
	}
	if deleted != 2 {
		t.Fatalf("ResetLive deleted %d, want 2", deleted)
	}
	snap, err := st.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if len(snap.Pending)+len(snap.InFlight)+len(snap.Dead) != 0 {
		t.Fatalf("live rows survived ResetLive: %+v", snap)
	}
	if _, ok, err := st.FindRecordIncludingTerminal("nudge-t"); err != nil || !ok {
		t.Fatalf("terminal history lost by ResetLive: %v, %v", ok, err)
	}
}

func TestImportTerminalShadowRoundTrip(t *testing.T) {
	st := importTestStore(t)
	created := time.Now().Add(-3 * time.Hour).UTC()
	terminal := created.Add(time.Minute)

	shadow := nudgequeue.NudgeShadow{
		ID:             "wait-w1-e1-1",
		BeadID:         "ga-shadow1",
		State:          "injected",
		TerminalReason: "",
		CommitBoundary: "provider-nudge-return",
		Agent:          "boot/dev",
		SessionID:      "ga-sess1",
		Source:         "wait",
		Message:        "Wait satisfied.",
		Reference:      &nudgequeue.Reference{Kind: "bead", ID: "ga-w1"},
		DeliverAfter:   created,
		ExpiresAt:      created.Add(nudgequeue.DefaultTTL),
	}
	if err := st.ImportTerminalShadow(shadow, created, terminal); err != nil {
		t.Fatalf("ImportTerminalShadow: %v", err)
	}

	rec, ok, err := st.FindRecordIncludingTerminal("wait-w1-e1-1")
	if err != nil || !ok {
		t.Fatalf("FindRecordIncludingTerminal = %v, %v; want the imported record", ok, err)
	}
	if rec.QueueState != "terminal" || rec.TerminalState != "injected" || rec.CommitBoundary != "provider-nudge-return" {
		t.Fatalf("imported record = %+v, want terminal injected", rec)
	}
	if !rec.TerminalAt.Equal(terminal) {
		t.Fatalf("TerminalAt = %v, want %v", rec.TerminalAt, terminal)
	}
	if rec.Item.BeadID != "ga-shadow1" || rec.Item.Reference == nil || rec.Item.Reference.ID != "ga-w1" {
		t.Fatalf("imported item lost identity fields: %+v", rec.Item)
	}
	// The wait finalization projection reads the terminal stamps.
	if s := rec.Shadow(); s.Open || s.State != "injected" {
		t.Fatalf("Shadow() = %+v, want closed injected", s)
	}

	// A live row of the same id is never demoted by a stale shadow import.
	live := routingTestItem("nudge-live", "boot/dev", time.Now())
	if err := st.ImportItem(live, "pending"); err != nil {
		t.Fatalf("ImportItem: %v", err)
	}
	if err := st.ImportTerminalShadow(nudgequeue.NudgeShadow{ID: "nudge-live", State: "failed"}, created, terminal); err != nil {
		t.Fatalf("ImportTerminalShadow duplicate: %v", err)
	}
	rec, ok, err = st.FindRecord("nudge-live")
	if err != nil || !ok || rec.QueueState != "pending" {
		t.Fatalf("live row after stale shadow import = %+v (%v, %v), want pending", rec, ok, err)
	}
}
