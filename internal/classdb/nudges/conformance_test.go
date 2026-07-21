package nudgesdb

// Both-backend conformance: the behavioral contracts of the nudge queue
// front door (internal/nudgequeue.Queue) must hold identically over the
// two-tier file backend and this merged-table backend. Cases exercise ONLY
// the public Queue surface; shadow-bead observability assertions stay in
// internal/nudgequeue's own tests (the file backend's mechanics).

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/nudgequeue"
)

func eachBackend(t *testing.T, fn func(t *testing.T, q *nudgequeue.Queue)) {
	t.Helper()
	t.Run("file", func(t *testing.T) {
		mem := beads.NewMemStore()
		fn(t, nudgequeue.NewFileQueue(t.TempDir(), func() (beads.NudgesStore, func()) {
			return beads.NudgesStore{Store: mem}, func() {}
		}))
	})
	t.Run("sqlite", func(t *testing.T) {
		st, err := Open(filepath.Join(t.TempDir(), "nudges.db"))
		if err != nil {
			t.Fatalf("open sqlite nudges store: %v", err)
		}
		t.Cleanup(func() {
			if err := st.Close(); err != nil {
				t.Errorf("close: %v", err)
			}
		})
		fn(t, nudgequeue.NewQueueWithBackend(st))
	})
}

func confItem(id, agent string, now time.Time) nudgequeue.Item {
	return nudgequeue.Item{
		ID:           id,
		Agent:        agent,
		Source:       "session",
		Message:      "wake up",
		CreatedAt:    now.UTC(),
		DeliverAfter: now.UTC(),
		ExpiresAt:    now.Add(nudgequeue.DefaultTTL).UTC(),
	}
}

func confTarget(agent string) nudgequeue.ClaimTarget { //nolint:unparam // fixture knob; today's cases share one agent key
	return nudgequeue.ClaimTarget{QueueKeys: []string{agent}}
}

func TestConformanceEnqueueClaimAckRoundTrip(t *testing.T) {
	eachBackend(t, func(t *testing.T, q *nudgequeue.Queue) {
		now := time.Now()
		if err := q.Enqueue(confItem("nudge-1", "boot/dev", now), beads.NudgesStore{}); err != nil {
			t.Fatalf("Enqueue: %v", err)
		}
		claimed, err := q.ClaimDue(confTarget("boot/dev"), now)
		if err != nil {
			t.Fatalf("ClaimDue: %v", err)
		}
		if len(claimed) != 1 || claimed[0].ID != "nudge-1" {
			t.Fatalf("ClaimDue = %+v, want the enqueued item", claimed)
		}
		if claimed[0].ClaimedAt.IsZero() || claimed[0].LeaseUntil.IsZero() {
			t.Fatalf("claimed item missing lease stamps: %+v", claimed[0])
		}
		if again, err := q.ClaimDue(confTarget("boot/dev"), now); err != nil || len(again) != 0 {
			t.Fatalf("second ClaimDue = (%d, %v), want none (claim-once-until-ack)", len(again), err)
		}
		if err := q.Ack([]string{"nudge-1"}, "injected", "", "provider-nudge-return"); err != nil {
			t.Fatalf("Ack: %v", err)
		}
		state, err := q.Snapshot()
		if err != nil {
			t.Fatalf("Snapshot: %v", err)
		}
		if len(state.Pending)+len(state.InFlight)+len(state.Dead) != 0 {
			t.Fatalf("queue not empty after ack: %+v", state)
		}
	})
}

func TestConformanceClaimHonorsDeliverAfterAndFence(t *testing.T) {
	eachBackend(t, func(t *testing.T, q *nudgequeue.Queue) {
		now := time.Now()
		future := confItem("nudge-future", "boot/dev", now)
		future.DeliverAfter = now.Add(time.Hour).UTC()
		if err := q.Enqueue(future, beads.NudgesStore{}); err != nil {
			t.Fatalf("Enqueue(future): %v", err)
		}
		fenced := confItem("nudge-fenced", "boot/dev", now)
		fenced.SessionID = "gc-session-9"
		if err := q.Enqueue(fenced, beads.NudgesStore{}); err != nil {
			t.Fatalf("Enqueue(fenced): %v", err)
		}
		epochFenced := confItem("nudge-epoch", "boot/dev", now)
		epochFenced.ContinuationEpoch = "epoch-3"
		if err := q.Enqueue(epochFenced, beads.NudgesStore{}); err != nil {
			t.Fatalf("Enqueue(epoch): %v", err)
		}

		if claimed, err := q.ClaimDue(confTarget("boot/dev"), now); err != nil || len(claimed) != 0 {
			t.Fatalf("fence-less ClaimDue = (%+v, %v), want none", claimed, err)
		}
		target := nudgequeue.ClaimTarget{QueueKeys: []string{"boot/dev"}, SessionID: "gc-session-9", ContinuationEpoch: "epoch-3"}
		claimed, err := q.ClaimDue(target, now)
		if err != nil || len(claimed) != 2 {
			t.Fatalf("fenced ClaimDue = (%+v, %v), want the fenced + epoch items", claimed, err)
		}
	})
}

func TestConformanceReleaseClaimsReturnsToPending(t *testing.T) {
	eachBackend(t, func(t *testing.T, q *nudgequeue.Queue) {
		now := time.Now()
		if err := q.Enqueue(confItem("nudge-1", "boot/dev", now), beads.NudgesStore{}); err != nil {
			t.Fatalf("Enqueue: %v", err)
		}
		if _, err := q.ClaimDue(confTarget("boot/dev"), now); err != nil {
			t.Fatalf("ClaimDue: %v", err)
		}
		if err := q.ReleaseClaims([]string{"nudge-1"}); err != nil {
			t.Fatalf("ReleaseClaims: %v", err)
		}
		claimed, err := q.ClaimDue(confTarget("boot/dev"), now)
		if err != nil || len(claimed) != 1 {
			t.Fatalf("re-claim after release = (%d, %v), want 1", len(claimed), err)
		}
	})
}

func TestConformanceRecordFailureRetriesThenDeadLetters(t *testing.T) {
	eachBackend(t, func(t *testing.T, q *nudgequeue.Queue) {
		now := time.Now()
		if err := q.Enqueue(confItem("nudge-1", "boot/dev", now), beads.NudgesStore{}); err != nil {
			t.Fatalf("Enqueue: %v", err)
		}
		cause := errors.New("delivery blew up")
		for attempt := 1; attempt < nudgequeue.MaxAttempts; attempt++ {
			dead, err := q.RecordFailure([]string{"nudge-1"}, beads.NudgesStore{}, cause, now, nil)
			if err != nil || len(dead) != 0 {
				t.Fatalf("attempt %d = (%+v, %v), want retry", attempt, dead, err)
			}
		}
		dead, err := q.RecordFailure([]string{"nudge-1"}, beads.NudgesStore{}, cause, now, nil)
		if err != nil || len(dead) != 1 || dead[0].Attempts != nudgequeue.MaxAttempts {
			t.Fatalf("final failure = (%+v, %v), want dead-letter at MaxAttempts", dead, err)
		}
		state, err := q.Snapshot()
		if err != nil || len(state.Dead) != 1 || state.Dead[0].ID != "nudge-1" {
			t.Fatalf("Snapshot dead = (%+v, %v), want nudge-1", state.Dead, err)
		}
	})
}

func TestConformanceFenceMismatchDeadLettersInstantly(t *testing.T) {
	eachBackend(t, func(t *testing.T, q *nudgequeue.Queue) {
		now := time.Now()
		if err := q.Enqueue(confItem("nudge-1", "boot/dev", now), beads.NudgesStore{}); err != nil {
			t.Fatalf("Enqueue: %v", err)
		}
		dead, err := q.RecordFailure([]string{"nudge-1"}, beads.NudgesStore{}, nudgequeue.ErrSessionFenceMismatch, now, nil)
		if err != nil || len(dead) != 1 || dead[0].Attempts != 1 {
			t.Fatalf("fence-mismatch failure = (%+v, %v), want instant dead-letter", dead, err)
		}
	})
}

func TestConformanceEnqueueSupersedesSameReference(t *testing.T) {
	eachBackend(t, func(t *testing.T, q *nudgequeue.Queue) {
		now := time.Now()
		first := confItem("nudge-old", "boot/dev", now)
		first.Source = "wait"
		first.Reference = &nudgequeue.Reference{Kind: "bead", ID: "gc-wait-1"}
		if err := q.Enqueue(first, beads.NudgesStore{}); err != nil {
			t.Fatalf("Enqueue(first): %v", err)
		}
		second := confItem("nudge-new", "boot/dev", now.Add(time.Second))
		second.Source = "wait"
		second.Reference = &nudgequeue.Reference{Kind: "bead", ID: "gc-wait-1"}
		if err := q.Enqueue(second, beads.NudgesStore{}); err != nil {
			t.Fatalf("Enqueue(second): %v", err)
		}
		state, err := q.Snapshot()
		if err != nil {
			t.Fatalf("Snapshot: %v", err)
		}
		if len(state.Pending) != 1 || state.Pending[0].ID != "nudge-new" {
			t.Fatalf("Pending = %+v, want only the superseding item", state.Pending)
		}
		if len(state.Dead) != 1 || state.Dead[0].ID != "nudge-old" || state.Dead[0].LastError != "superseded" {
			t.Fatalf("Dead = %+v, want the superseded original", state.Dead)
		}
	})
}

func TestConformanceEnqueueExistingIDIsNoOp(t *testing.T) {
	eachBackend(t, func(t *testing.T, q *nudgequeue.Queue) {
		now := time.Now()
		if err := q.Enqueue(confItem("nudge-1", "boot/dev", now), beads.NudgesStore{}); err != nil {
			t.Fatalf("Enqueue: %v", err)
		}
		dup := confItem("nudge-1", "boot/dev", now.Add(time.Minute))
		dup.Message = "changed"
		if err := q.Enqueue(dup, beads.NudgesStore{}); err != nil {
			t.Fatalf("Enqueue(dup): %v", err)
		}
		state, err := q.Snapshot()
		if err != nil || len(state.Pending) != 1 || state.Pending[0].Message != "wake up" {
			t.Fatalf("Snapshot = (%+v, %v), want the original item untouched", state.Pending, err)
		}
	})
}

func TestConformanceExpiryDeadLettersOnMaintenance(t *testing.T) {
	eachBackend(t, func(t *testing.T, q *nudgequeue.Queue) {
		now := time.Now()
		expired := confItem("nudge-exp", "boot/dev", now)
		expired.ExpiresAt = now.Add(-time.Minute).UTC()
		if err := q.Enqueue(expired, beads.NudgesStore{}); err != nil {
			t.Fatalf("Enqueue: %v", err)
		}
		// Any maintenance-bearing operation dead-letters the expired item.
		if claimed, err := q.ClaimDue(confTarget("boot/dev"), now); err != nil || len(claimed) != 0 {
			t.Fatalf("ClaimDue = (%+v, %v), want none (expired)", claimed, err)
		}
		state, err := q.Snapshot()
		if err != nil || len(state.Dead) != 1 || state.Dead[0].LastError != "expired" {
			t.Fatalf("Snapshot dead = (%+v, %v), want the expired item", state.Dead, err)
		}
	})
}

func TestConformanceLeaseExpiryReturnsToPending(t *testing.T) {
	eachBackend(t, func(t *testing.T, q *nudgequeue.Queue) {
		now := time.Now()
		if err := q.Enqueue(confItem("nudge-1", "boot/dev", now), beads.NudgesStore{}); err != nil {
			t.Fatalf("Enqueue: %v", err)
		}
		if _, err := q.ClaimDue(confTarget("boot/dev"), now); err != nil {
			t.Fatalf("ClaimDue: %v", err)
		}
		// Past the lease TTL, a new claim pass recovers and re-claims.
		later := now.Add(nudgequeue.ClaimLeaseTTL + time.Second)
		claimed, err := q.ClaimDue(confTarget("boot/dev"), later)
		if err != nil || len(claimed) != 1 {
			t.Fatalf("post-lease ClaimDue = (%+v, %v), want re-claim", claimed, err)
		}
	})
}

func TestConformanceWithdrawWaitNudges(t *testing.T) {
	eachBackend(t, func(t *testing.T, q *nudgequeue.Queue) {
		now := time.Now()
		item := confItem("wait-gc-1-0-1", "boot/dev", now)
		item.Source = "wait"
		item.Reference = &nudgequeue.Reference{Kind: "bead", ID: "gc-wait-1"}
		if err := q.Enqueue(item, beads.NudgesStore{}); err != nil {
			t.Fatalf("Enqueue: %v", err)
		}
		if err := q.WithdrawQueuedWaitNudges(nil, []string{"wait-gc-1-0-1"}); err != nil {
			t.Fatalf("Withdraw: %v", err)
		}
		state, err := q.Snapshot()
		if err != nil {
			t.Fatalf("Snapshot: %v", err)
		}
		if len(state.Pending)+len(state.InFlight) != 0 {
			t.Fatalf("withdrawn item still queued: %+v", state)
		}
	})
}

func TestConformanceListForTargetKeys(t *testing.T) {
	eachBackend(t, func(t *testing.T, q *nudgequeue.Queue) {
		now := time.Now()
		if err := q.Enqueue(confItem("nudge-a", "alias-old", now), beads.NudgesStore{}); err != nil {
			t.Fatalf("Enqueue(a): %v", err)
		}
		if err := q.Enqueue(confItem("nudge-b", "boot/dev", now), beads.NudgesStore{}); err != nil {
			t.Fatalf("Enqueue(b): %v", err)
		}
		if err := q.Enqueue(confItem("nudge-c", "stranger", now), beads.NudgesStore{}); err != nil {
			t.Fatalf("Enqueue(c): %v", err)
		}
		target := nudgequeue.ClaimTarget{QueueKeys: []string{"boot/dev", "alias-old"}}
		pending, inFlight, dead, err := q.ListFor(target, now)
		if err != nil {
			t.Fatalf("ListFor: %v", err)
		}
		if len(pending) != 2 || len(inFlight) != 0 || len(dead) != 0 {
			t.Fatalf("ListFor = (%d,%d,%d), want (2,0,0)", len(pending), len(inFlight), len(dead))
		}
		pOnly, _, _, err := q.ListForAgent("stranger", now)
		if err != nil || len(pOnly) != 1 || pOnly[0].ID != "nudge-c" {
			t.Fatalf("ListForAgent(stranger) = (%+v, %v), want nudge-c", pOnly, err)
		}
	})
}
