package nudgequeue

import (
	"errors"
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/beads"
)

// A routed city whose class store cannot open must fail every queue operation
// with the open error rather than silently falling back to the file backend
// (a silent fallback would split the class across two backends).
func TestUnavailableQueueFailsEveryOperation(t *testing.T) {
	cause := errors.New("nudges class store unavailable")
	q := NewUnavailableQueue(cause)
	now := time.Now()

	if err := q.Enqueue(testItem("nudge-1", "boot/dev", now), beads.NudgesStore{}); !errors.Is(err, cause) {
		t.Errorf("Enqueue error = %v, want %v", err, cause)
	}
	if err := q.EnqueueDeferred(testItem("nudge-2", "boot/dev", now)); !errors.Is(err, cause) {
		t.Errorf("EnqueueDeferred error = %v, want %v", err, cause)
	}
	if _, err := q.ClaimDue(agentTarget("boot/dev"), now); !errors.Is(err, cause) {
		t.Errorf("ClaimDue error = %v, want %v", err, cause)
	}
	if _, _, _, err := q.ListForAgent("boot/dev", now); !errors.Is(err, cause) {
		t.Errorf("ListForAgent error = %v, want %v", err, cause)
	}
	if _, _, _, err := q.ListFor(agentTarget("boot/dev"), now); !errors.Is(err, cause) {
		t.Errorf("ListFor error = %v, want %v", err, cause)
	}
	if _, err := q.Snapshot(); !errors.Is(err, cause) {
		t.Errorf("Snapshot error = %v, want %v", err, cause)
	}
	if err := q.Ack([]string{"nudge-1"}, "injected", "", ""); !errors.Is(err, cause) {
		t.Errorf("Ack error = %v, want %v", err, cause)
	}
	if err := q.ReleaseClaims([]string{"nudge-1"}); !errors.Is(err, cause) {
		t.Errorf("ReleaseClaims error = %v, want %v", err, cause)
	}
	if _, err := q.RecordFailure([]string{"nudge-1"}, beads.NudgesStore{}, errors.New("boom"), now, nil); !errors.Is(err, cause) {
		t.Errorf("RecordFailure error = %v, want %v", err, cause)
	}
	if err := q.Rollback(nil, testItem("nudge-1", "boot/dev", now), "reason"); !errors.Is(err, cause) {
		t.Errorf("Rollback error = %v, want %v", err, cause)
	}
	if err := q.WithdrawQueuedWaitNudges(nil, []string{"nudge-1"}); !errors.Is(err, cause) {
		t.Errorf("WithdrawQueuedWaitNudges error = %v, want %v", err, cause)
	}
}
