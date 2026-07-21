package nudgequeue

// Behavioral tests for the Queue front door over the file backend. These
// exercise ONLY the public Queue surface (no state.json internals, no
// recorded call shapes), so they are the portable conformance base the
// embedded-SQLite backend will run against in the P2 store slice.

import (
	"errors"
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/beads"
)

func testQueue(t *testing.T) (*Queue, *beads.MemStore) {
	t.Helper()
	mem := beads.NewMemStore()
	q := NewFileQueue(t.TempDir(), func() (beads.NudgesStore, func()) {
		return beads.NudgesStore{Store: mem}, func() {}
	})
	return q, mem
}

func testItem(id, agent string, now time.Time) Item { //nolint:unparam // fixture knob; today's cases share one agent key
	return Item{
		ID:           id,
		Agent:        agent,
		Source:       "session",
		Message:      "wake up",
		CreatedAt:    now.UTC(),
		DeliverAfter: now.UTC(),
		ExpiresAt:    now.Add(DefaultTTL).UTC(),
	}
}

func agentTarget(agent string) ClaimTarget { //nolint:unparam // fixture knob; today's cases share one agent key
	return ClaimTarget{QueueKeys: []string{agent}}
}

func TestQueueEnqueueClaimAckRoundTrip(t *testing.T) {
	q, _ := testQueue(t)
	now := time.Now()
	if err := q.Enqueue(testItem("nudge-1", "boot/dev", now), beads.NudgesStore{}); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}

	claimed, err := q.ClaimDue(agentTarget("boot/dev"), now)
	if err != nil {
		t.Fatalf("ClaimDue: %v", err)
	}
	if len(claimed) != 1 || claimed[0].ID != "nudge-1" {
		t.Fatalf("ClaimDue = %+v, want the enqueued item", claimed)
	}
	if claimed[0].ClaimedAt.IsZero() || claimed[0].LeaseUntil.IsZero() {
		t.Fatalf("claimed item missing lease stamps: %+v", claimed[0])
	}

	// Claim-once-until-ack: a second claim gets nothing.
	again, err := q.ClaimDue(agentTarget("boot/dev"), now)
	if err != nil || len(again) != 0 {
		t.Fatalf("second ClaimDue = (%d items, %v), want none", len(again), err)
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
}

func TestQueueClaimHonorsDeliverAfterAndFence(t *testing.T) {
	q, _ := testQueue(t)
	now := time.Now()

	future := testItem("nudge-future", "boot/dev", now)
	future.DeliverAfter = now.Add(time.Hour).UTC()
	if err := q.Enqueue(future, beads.NudgesStore{}); err != nil {
		t.Fatalf("Enqueue(future): %v", err)
	}
	fenced := testItem("nudge-fenced", "boot/dev", now)
	fenced.SessionID = "gc-session-9"
	if err := q.Enqueue(fenced, beads.NudgesStore{}); err != nil {
		t.Fatalf("Enqueue(fenced): %v", err)
	}

	// A fence-less target claims neither: the future item is not due, the
	// fenced item requires the matching session id.
	claimed, err := q.ClaimDue(agentTarget("boot/dev"), now)
	if err != nil || len(claimed) != 0 {
		t.Fatalf("ClaimDue = (%d items, %v), want none", len(claimed), err)
	}

	// The matching-fence target claims the fenced item.
	target := ClaimTarget{QueueKeys: []string{"boot/dev"}, SessionID: "gc-session-9"}
	claimed, err = q.ClaimDue(target, now)
	if err != nil || len(claimed) != 1 || claimed[0].ID != "nudge-fenced" {
		t.Fatalf("fenced ClaimDue = (%+v, %v), want nudge-fenced", claimed, err)
	}
}

func TestQueueReleaseClaimsReturnsToPending(t *testing.T) {
	q, _ := testQueue(t)
	now := time.Now()
	if err := q.Enqueue(testItem("nudge-1", "boot/dev", now), beads.NudgesStore{}); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	if _, err := q.ClaimDue(agentTarget("boot/dev"), now); err != nil {
		t.Fatalf("ClaimDue: %v", err)
	}
	if err := q.ReleaseClaims([]string{"nudge-1"}); err != nil {
		t.Fatalf("ReleaseClaims: %v", err)
	}
	claimed, err := q.ClaimDue(agentTarget("boot/dev"), now)
	if err != nil || len(claimed) != 1 {
		t.Fatalf("re-claim after release = (%d items, %v), want 1", len(claimed), err)
	}
}

func TestQueueRecordFailureRetriesThenDeadLetters(t *testing.T) {
	q, _ := testQueue(t)
	now := time.Now()
	if err := q.Enqueue(testItem("nudge-1", "boot/dev", now), beads.NudgesStore{}); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	cause := errors.New("delivery blew up")
	for attempt := 1; attempt < MaxAttempts; attempt++ {
		dead, err := q.RecordFailure([]string{"nudge-1"}, beads.NudgesStore{}, cause, now, nil)
		if err != nil {
			t.Fatalf("RecordFailure %d: %v", attempt, err)
		}
		if len(dead) != 0 {
			t.Fatalf("attempt %d dead-lettered early: %+v", attempt, dead)
		}
	}
	dead, err := q.RecordFailure([]string{"nudge-1"}, beads.NudgesStore{}, cause, now, nil)
	if err != nil {
		t.Fatalf("final RecordFailure: %v", err)
	}
	if len(dead) != 1 || dead[0].Attempts != MaxAttempts {
		t.Fatalf("dead = %+v, want nudge-1 at MaxAttempts", dead)
	}
	state, err := q.Snapshot()
	if err != nil || len(state.Dead) != 1 {
		t.Fatalf("Snapshot dead bucket = (%+v, %v), want 1 item", state.Dead, err)
	}
}

func TestQueueRecordFailureFenceMismatchDeadLettersInstantly(t *testing.T) {
	q, _ := testQueue(t)
	now := time.Now()
	if err := q.Enqueue(testItem("nudge-1", "boot/dev", now), beads.NudgesStore{}); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	dead, err := q.RecordFailure([]string{"nudge-1"}, beads.NudgesStore{}, ErrSessionFenceMismatch, now, nil)
	if err != nil {
		t.Fatalf("RecordFailure: %v", err)
	}
	if len(dead) != 1 || dead[0].Attempts != 1 {
		t.Fatalf("dead = %+v, want instant dead-letter on first attempt", dead)
	}
}

func TestQueueEnqueueSupersedesSameReference(t *testing.T) {
	q, mem := testQueue(t)
	now := time.Now()
	first := testItem("nudge-old", "boot/dev", now)
	first.Source = "wait"
	first.Reference = &Reference{Kind: "bead", ID: "gc-wait-1"}
	if err := q.Enqueue(first, beads.NudgesStore{}); err != nil {
		t.Fatalf("Enqueue(first): %v", err)
	}
	second := testItem("nudge-new", "boot/dev", now.Add(time.Second))
	second.Source = "wait"
	second.Reference = &Reference{Kind: "bead", ID: "gc-wait-1"}
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
	// The superseded record is terminal in the shadow store.
	shadow, ok, err := NewStore(beads.NudgesStore{Store: mem}).FindIncludingTerminal("nudge-old")
	if err != nil || !ok {
		t.Fatalf("FindIncludingTerminal = (ok=%v, %v)", ok, err)
	}
	if shadow.State != "superseded" {
		t.Fatalf("superseded shadow state = %q, want superseded", shadow.State)
	}
}

func TestQueueEnqueueDeferredIsBareAppend(t *testing.T) {
	q, mem := testQueue(t)
	now := time.Now()
	item := testItem("nudge-deferred", "boot/dev", now)
	if err := q.EnqueueDeferred(item); err != nil {
		t.Fatalf("EnqueueDeferred: %v", err)
	}
	state, err := q.Snapshot()
	if err != nil || len(state.Pending) != 1 {
		t.Fatalf("Snapshot = (%+v, %v), want the deferred item pending", state, err)
	}
	// No shadow bead: the deferred-submit path never wrote one.
	if _, ok, err := NewStore(beads.NudgesStore{Store: mem}).Find("nudge-deferred"); err != nil || ok {
		t.Fatalf("deferred item grew a shadow bead (ok=%v, %v)", ok, err)
	}
}

func TestQueueRollbackDeadLettersQueuedItem(t *testing.T) {
	q, mem := testQueue(t)
	now := time.Now()
	item := testItem("nudge-1", "boot/dev", now)
	if err := q.Enqueue(item, beads.NudgesStore{}); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	front := NewStore(beads.NudgesStore{Store: mem})
	if err := q.Rollback(front, item, "wake failed after enqueue"); err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	state, err := q.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if len(state.Pending) != 0 || len(state.Dead) != 1 {
		t.Fatalf("state after rollback = %+v, want dead-lettered item", state)
	}
	shadow, ok, err := front.FindIncludingTerminal("nudge-1")
	if err != nil || !ok || shadow.State != "failed" {
		t.Fatalf("rollback shadow = (%+v, ok=%v, %v), want terminal failed", shadow, ok, err)
	}
}

func TestClaimTargetPredicates(t *testing.T) {
	target := ClaimTarget{
		QueueKeys:         []string{"alias", "gc-session-1", "boot/dev"},
		SessionID:         "gc-session-1",
		ContinuationEpoch: "epoch-2",
	}
	base := Item{Agent: "alias"}
	if !target.Claimable(base) {
		t.Fatal("unfenced item not claimable by matching target")
	}
	if target.Claimable(Item{Agent: "stranger"}) {
		t.Fatal("non-matching agent claimable")
	}
	fenced := Item{Agent: "alias", SessionID: "gc-session-1"}
	if !target.Claimable(fenced) || !target.FencePasses(fenced) {
		t.Fatal("matching fence rejected")
	}
	wrongFence := Item{Agent: "alias", SessionID: "gc-session-OTHER"}
	if target.Claimable(wrongFence) || target.FencePasses(wrongFence) {
		t.Fatal("mismatched session fence accepted")
	}
	epochOnly := Item{Agent: "alias", ContinuationEpoch: "epoch-1"}
	if (ClaimTarget{QueueKeys: []string{"alias"}}).Claimable(epochOnly) {
		t.Fatal("epoch-fenced item claimable by epoch-less target")
	}
	if target.FencePasses(epochOnly) {
		t.Fatal("mismatched epoch fence accepted")
	}
}
