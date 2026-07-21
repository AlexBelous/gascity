package nudgequeue

import (
	"errors"
	"io"
	"time"

	"github.com/gastownhall/gascity/internal/beads"
)

// This file is the nudge-queue front door: the NAMED operations over the
// queue authority, extracted from the WithState closures that previously
// lived in cmd/gc/cmd_nudge.go (engdocs/plans/infra-class-sqlite-stores/
// P2-NUDGES-SEAM-PLAN.md). The Queue routes every operation through an
// unexported queueBackend — the two-tier file backend (queue_file.go:
// flock'd state.json authority + shadow beads) today, the embedded-SQLite
// merged queue when [beads.classes.nudges] relocates the class. cmd/gc keeps
// thin wrappers under its existing function names plus everything that is
// not queue persistence: store opening, the wake-socket ping, poller
// spawning, session observation and delivery.

// Queue policy constants — the timing/retry vocabulary of the deferred-nudge
// queue, shared by every backend.
const (
	// DefaultTTL is a queued nudge's deliver-by deadline.
	DefaultTTL = 24 * time.Hour
	// ClaimLeaseTTL is how long an in-flight claim holds its lease before an
	// expired-lease recovery pass returns the item to pending.
	ClaimLeaseTTL = 2 * time.Minute
	// RetryDelay is the redelivery backoff after a failed attempt.
	RetryDelay = 15 * time.Second
	// MaxAttempts dead-letters an item after this many failed deliveries.
	MaxAttempts = 5
	// DeadRetention is how long dead-lettered items are retained (once their
	// terminal record is durable) before pruning.
	DeadRetention = 1 * time.Hour
	// EnqueueMaintenanceBudget bounds the wall-clock time the foreground
	// enqueue path spends on best-effort queue maintenance while holding the
	// queue transaction. Without this, maintenance does O(backlog) serial
	// store writes with no cap, so a large backlog turns a sub-second
	// foreground call into a multi-minute hang. Items skipped once the budget
	// is exceeded are left for the next maintenance pass — never dropped.
	EnqueueMaintenanceBudget = 2 * time.Second
)

// ErrSessionFenceMismatch marks a delivery failure caused by a queued nudge's
// session fence not matching the live target; RecordFailure dead-letters such
// items immediately instead of retrying (the fenced generation is gone).
var ErrSessionFenceMismatch = errors.New("queued nudge session fence mismatch")

// ClaimTarget is the plain-values claim identity of one delivery target: the
// set of queue keys its items may be addressed by (alias plus alias history,
// session id, qualified agent name, identity, session name) and the session
// fence the target carries. It is deliberately data-only — a SQL backend
// translates it into a WHERE clause, which a func predicate could not cross.
type ClaimTarget struct {
	// QueueKeys are the agent keys this target claims for.
	QueueKeys []string
	// SessionID / ContinuationEpoch are the target's live-generation fence.
	SessionID         string
	ContinuationEpoch string
}

// MatchesAgent reports whether an item addressed to agent belongs to this
// target (set membership over QueueKeys).
func (t ClaimTarget) MatchesAgent(agent string) bool {
	if agent == "" {
		return false
	}
	for _, key := range t.QueueKeys {
		if key == agent {
			return true
		}
	}
	return false
}

// Claimable is the claim gate: whether this target may claim item. A fenced
// item is unclaimable by a target lacking the matching session id, and an
// epoch-fenced item requires the target to carry an epoch at all.
func (t ClaimTarget) Claimable(item Item) bool {
	if !t.MatchesAgent(item.Agent) {
		return false
	}
	if item.SessionID != "" {
		if t.SessionID == "" {
			return false
		}
		return item.SessionID == t.SessionID
	}
	if item.ContinuationEpoch != "" && t.ContinuationEpoch == "" {
		return false
	}
	return true
}

// FencePasses is the delivery gate: a non-empty item fence field must equal
// the target's. Mismatches are dead-lettered with ErrSessionFenceMismatch by
// the delivery paths.
func (t ClaimTarget) FencePasses(item Item) bool {
	if item.SessionID != "" && item.SessionID != t.SessionID {
		return false
	}
	if item.ContinuationEpoch != "" && item.ContinuationEpoch != t.ContinuationEpoch {
		return false
	}
	return true
}

// ShadowStoreOpener lazily opens the shadow-bead store a queue operation may
// need for bead maintenance, returning the store and a release func. The
// opener preserves the caller's fail-open semantics: an open failure returns
// a zero store (nil release allowed), and every shadow write degrades to the
// nil-safe *Store no-op — the queue authority never blocks on the shadow.
type ShadowStoreOpener func() (beads.NudgesStore, func())

// queueBackend is the nudge-queue persistence authority behind the Queue
// front door. The file backend implements it over the flock'd state.json
// plus shadow beads; the sqlite backend implements it over one merged
// nudges table (shadow parameters are then ignored — the row IS the record).
// The type is unexported on purpose: consumers hold *Queue, never a backend;
// method names are exported so another package can satisfy it structurally.
type queueBackend interface {
	// Enqueue runs the full enqueue transaction: shadow save, maintenance
	// (bounded by EnqueueMaintenanceBudget), same-reference supersession,
	// append; on transaction failure the just-created shadow is rolled back.
	// shadow may be a zero store, in which case the backend opens its own.
	Enqueue(item Item, shadow beads.NudgesStore) error
	// EnqueueDeferred appends item with no shadow, no maintenance, and no
	// supersession — the session.Manager deferred-submit shape.
	EnqueueDeferred(item Item) error
	// ClaimDue moves due pending items claimable by target to in-flight
	// (lease ClaimLeaseTTL), running the maintenance passes first.
	ClaimDue(target ClaimTarget, now time.Time) ([]Item, error)
	// ListForAgent returns the pending/in-flight/dead items addressed
	// exactly to agentName, after maintenance.
	ListForAgent(agentName string, now time.Time) (pending, inFlight, dead []Item, err error)
	// ListFor returns the pending/in-flight/dead items matching any of
	// target's queue keys, after maintenance.
	ListFor(target ClaimTarget, now time.Time) (pending, inFlight, dead []Item, err error)
	// Snapshot returns the current queue state without maintenance or
	// locking guarantees (the dispatcher/sweep read-only view).
	Snapshot() (State, error)
	// Ack removes ids from pending/in-flight and terminalizes their records
	// with the given outcome vocabulary.
	Ack(ids []string, outcome, reason, commitBoundary string) error
	// ReleaseClaims returns in-flight ids to pending (undelivered).
	ReleaseClaims(ids []string) error
	// RecordFailure applies the retry policy to ids (requeue with backoff,
	// or dead-letter on fence mismatch / MaxAttempts / expiry) and returns
	// the dead-lettered items. shadow may be a zero store; warn receives
	// best-effort warnings from the outside-the-lock record terminalization
	// (nil disables them).
	RecordFailure(ids []string, shadow beads.NudgesStore, cause error, now time.Time, warn io.Writer) ([]Item, error)
	// Rollback removes item by id and terminalizes it as failed with reason;
	// when the item is no longer queued the record alone is terminalized.
	Rollback(front *Store, item Item, reason string) error
	// WithdrawWaitNudges removes still-queued wait nudges by id and marks
	// their records terminal wait-canceled.
	WithdrawWaitNudges(store beads.Store, ids []string) error
}

// Queue is the nudge-queue front door every consumer holds.
type Queue struct {
	backend queueBackend
}

// NewFileQueue builds the two-tier file-backed queue for a city: the flock'd
// state.json authority with shadow beads opened lazily through openShadow
// (nil = no shadow store available; all bead writes no-op).
func NewFileQueue(cityPath string, openShadow ShadowStoreOpener) *Queue {
	return &Queue{backend: &fileQueue{cityPath: cityPath, openShadow: openShadow}}
}

// NewQueueWithBackend wraps a non-file backend (the embedded class store) as
// the queue front door. The backend parameter is deliberately the unexported
// interface: callers pass any structural implementation, but only *Queue
// escapes.
func NewQueueWithBackend(backend queueBackend) *Queue {
	return &Queue{backend: backend}
}

// NewUnavailableQueue returns a Queue whose every operation fails with err.
// It is the fail-closed shape for a routed city whose class store cannot be
// reached: falling back to the file backend would split the class across two
// backends (writes landing where reads no longer look), so every root must
// surface the error instead.
func NewUnavailableQueue(err error) *Queue {
	return &Queue{backend: unavailableBackend{err: err}}
}

// unavailableBackend fails every queue operation with the same error.
type unavailableBackend struct {
	err error
}

func (u unavailableBackend) Enqueue(Item, beads.NudgesStore) error { return u.err }
func (u unavailableBackend) EnqueueDeferred(Item) error            { return u.err }
func (u unavailableBackend) ClaimDue(ClaimTarget, time.Time) ([]Item, error) {
	return nil, u.err
}

func (u unavailableBackend) ListForAgent(string, time.Time) (pending, inFlight, dead []Item, err error) {
	return nil, nil, nil, u.err
}

func (u unavailableBackend) ListFor(ClaimTarget, time.Time) (pending, inFlight, dead []Item, err error) {
	return nil, nil, nil, u.err
}
func (u unavailableBackend) Snapshot() (State, error)                   { return State{}, u.err }
func (u unavailableBackend) Ack([]string, string, string, string) error { return u.err }
func (u unavailableBackend) ReleaseClaims([]string) error               { return u.err }
func (u unavailableBackend) RecordFailure([]string, beads.NudgesStore, error, time.Time, io.Writer) ([]Item, error) {
	return nil, u.err
}
func (u unavailableBackend) Rollback(*Store, Item, string) error            { return u.err }
func (u unavailableBackend) WithdrawWaitNudges(beads.Store, []string) error { return u.err }

// Enqueue runs the full enqueue transaction. See queueBackend.Enqueue.
func (q *Queue) Enqueue(item Item, shadow beads.NudgesStore) error {
	return q.backend.Enqueue(item, shadow)
}

// EnqueueDeferred appends item with no shadow, maintenance, or supersession.
func (q *Queue) EnqueueDeferred(item Item) error {
	return q.backend.EnqueueDeferred(item)
}

// ClaimDue claims due pending items for target. See queueBackend.ClaimDue.
func (q *Queue) ClaimDue(target ClaimTarget, now time.Time) ([]Item, error) {
	return q.backend.ClaimDue(target, now)
}

// ListForAgent lists items addressed exactly to agentName.
func (q *Queue) ListForAgent(agentName string, now time.Time) (pending, inFlight, dead []Item, err error) {
	return q.backend.ListForAgent(agentName, now)
}

// ListFor lists items matching any of target's queue keys.
func (q *Queue) ListFor(target ClaimTarget, now time.Time) (pending, inFlight, dead []Item, err error) {
	return q.backend.ListFor(target, now)
}

// Snapshot returns the current queue state (read-only view).
func (q *Queue) Snapshot() (State, error) {
	return q.backend.Snapshot()
}

// Ack terminalizes delivered ids with the given outcome vocabulary.
func (q *Queue) Ack(ids []string, outcome, reason, commitBoundary string) error {
	return q.backend.Ack(ids, outcome, reason, commitBoundary)
}

// ReleaseClaims returns undelivered in-flight ids to pending.
func (q *Queue) ReleaseClaims(ids []string) error {
	return q.backend.ReleaseClaims(ids)
}

// RecordFailure applies the retry policy to ids and returns the dead-lettered
// items.
func (q *Queue) RecordFailure(ids []string, shadow beads.NudgesStore, cause error, now time.Time, warn io.Writer) ([]Item, error) {
	return q.backend.RecordFailure(ids, shadow, cause, now, warn)
}

// Rollback removes item and terminalizes it as failed with reason.
func (q *Queue) Rollback(front *Store, item Item, reason string) error {
	return q.backend.Rollback(front, item, reason)
}

// WithdrawQueuedWaitNudges removes still-queued wait nudges by id and marks
// their records terminal wait-canceled.
func (q *Queue) WithdrawQueuedWaitNudges(store beads.Store, ids []string) error {
	return q.backend.WithdrawWaitNudges(store, ids)
}
