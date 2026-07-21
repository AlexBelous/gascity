package nudgequeue

import (
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/gastownhall/gascity/internal/beads"
)

// This file is the two-tier file backend: the queue-operation bodies moved
// verbatim from cmd/gc/cmd_nudge.go (the WithState closures) — flock'd
// state.json as the queue authority, shadow beads for observability, the
// bounded maintenance passes, and the retry/dead-letter policy. Behavior is
// byte-identical to the pre-extraction cmd/gc functions; only the shadow
// store acquisition is injected (ShadowStoreOpener) because store opening
// stays a cmd/gc concern.

// fileQueue is the flock'd state.json + shadow-bead backend.
type fileQueue struct {
	cityPath   string
	openShadow ShadowStoreOpener
}

// lazyShadow lazily opens the shadow-bead store the first time an operation
// actually has shadow work to do — i.e. the queue is non-empty and a
// recover/prune/terminalize pass may need to stamp a bead. On the common
// idle tick the queue is empty, the maintenance passes are no-ops, and the
// store is never opened (N idle poll sidecars stop dialing the sql-server).
// It owns the handle it opens and releases exactly that handle.
type lazyShadow struct {
	open    ShadowStoreOpener
	opened  bool
	store   beads.NudgesStore
	release func()
	front   *Store
}

// frontForState returns the shadow front door for the maintenance passes
// over state, opening the underlying store on first need. An empty queue
// leaves the store closed and returns a nil front — every maintenance pass
// only dereferences the front while iterating a non-empty slice, and the
// *Store methods are nil-receiver safe besides.
func (l *lazyShadow) frontForState(state *State) *Store {
	if QueueHasWork(state) {
		l.ensureOpen()
	}
	return l.front
}

// ensureOpen opens the underlying store exactly once (idempotent) and
// returns it. Ack uses it directly once it has confirmed terminal items.
func (l *lazyShadow) ensureOpen() beads.NudgesStore {
	if !l.opened {
		l.opened = true
		if l.open != nil {
			l.store, l.release = l.open()
			if l.store.Store != nil {
				l.front = NewStore(l.store)
			}
		}
	}
	return l.store
}

// close releases the store this frame opened (if any). It never touches a
// caller-passed store because this type only ever holds a store it opened.
func (l *lazyShadow) close() {
	if l.opened && l.release != nil {
		l.release()
	}
}

// QueueHasWork reports whether the queue holds any item a maintenance pass
// could act on. An empty queue means recover/prune/terminalize are all
// no-ops, so the shadow store need not be opened for the tick.
func QueueHasWork(state *State) bool {
	return len(state.Pending) > 0 || len(state.InFlight) > 0 || len(state.Dead) > 0
}

func (f *fileQueue) shadow() *lazyShadow {
	return &lazyShadow{open: f.openShadow}
}

// resolveShadow returns the provided shadow store, or opens one through the
// injected opener when the caller passed a zero store — the ...WithStore
// ownership contract: the backend releases only handles it opened.
func (f *fileQueue) resolveShadow(shadow beads.NudgesStore) (beads.NudgesStore, func()) {
	if shadow.Store != nil {
		return shadow, func() {}
	}
	if f.openShadow == nil {
		return beads.NudgesStore{}, func() {}
	}
	store, release := f.openShadow()
	if release == nil {
		release = func() {}
	}
	return store, release
}

// ClaimDue moves due pending items claimable by target to in-flight.
func (f *fileQueue) ClaimDue(target ClaimTarget, now time.Time) ([]Item, error) {
	return f.claimDueMatching(now, target.Claimable)
}

// claimDueMatching is the func-predicate claim core shared by ClaimDue and
// the file-backend-only ClaimDueMatching compatibility surface.
func (f *fileQueue) claimDueMatching(now time.Time, match func(Item) bool) ([]Item, error) {
	maint := f.shadow()
	defer maint.close()
	var claimed []Item
	err := WithState(f.cityPath, func(state *State) error {
		front := maint.frontForState(state)
		deadline := NoMaintenanceDeadline()
		if err := RecoverExpiredInFlight(state, front, now, deadline); err != nil {
			return err
		}
		if err := PruneExpired(state, front, now, deadline); err != nil {
			return err
		}
		if err := PruneDead(state, front, now, deadline); err != nil {
			return err
		}
		pending := state.Pending[:0]
		for _, item := range state.Pending {
			if !match(item) {
				pending = append(pending, item)
				continue
			}
			if !item.DeliverAfter.IsZero() && item.DeliverAfter.After(now) {
				pending = append(pending, item)
				continue
			}
			item.ClaimedAt = now.UTC()
			item.LeaseUntil = now.Add(ClaimLeaseTTL).UTC()
			state.InFlight = append(state.InFlight, item)
			claimed = append(claimed, item)
		}
		state.Pending = pending
		SortState(state)
		return nil
	})
	return claimed, err
}

// ClaimDueMatching claims due pending items satisfying an arbitrary
// predicate against the FILE backend for cityPath. It is the compatibility
// surface for callers (and tests) that predate ClaimTarget; the backend
// interface deliberately carries only the data-shaped ClaimDue, because a
// func predicate cannot cross into a SQL backend.
func ClaimDueMatching(cityPath string, openShadow ShadowStoreOpener, now time.Time, match func(Item) bool) ([]Item, error) {
	f := &fileQueue{cityPath: cityPath, openShadow: openShadow}
	return f.claimDueMatching(now, match)
}

// ListForAgent returns the items addressed exactly to agentName.
func (f *fileQueue) ListForAgent(agentName string, now time.Time) (pending, inFlight, dead []Item, err error) {
	return f.list(now, func(item Item) bool { return item.Agent == agentName })
}

// ListFor returns the items matching any of target's queue keys.
func (f *fileQueue) ListFor(target ClaimTarget, now time.Time) (pending, inFlight, dead []Item, err error) {
	return f.list(now, func(item Item) bool { return target.MatchesAgent(item.Agent) })
}

func (f *fileQueue) list(now time.Time, match func(Item) bool) (pending, inFlight, dead []Item, err error) {
	maint := f.shadow()
	defer maint.close()
	err = WithState(f.cityPath, func(state *State) error {
		front := maint.frontForState(state)
		deadline := NoMaintenanceDeadline()
		if err := RecoverExpiredInFlight(state, front, now, deadline); err != nil {
			return err
		}
		if err := PruneExpired(state, front, now, deadline); err != nil {
			return err
		}
		if err := PruneDead(state, front, now, deadline); err != nil {
			return err
		}
		for _, item := range state.Pending {
			if match(item) {
				pending = append(pending, item)
			}
		}
		for _, item := range state.InFlight {
			if match(item) {
				inFlight = append(inFlight, item)
			}
		}
		for _, item := range state.Dead {
			if match(item) {
				dead = append(dead, item)
			}
		}
		return nil
	})
	return pending, inFlight, dead, err
}

// Snapshot returns the queue state via the lock-free read the dispatcher and
// sweeps have always used.
func (f *fileQueue) Snapshot() (State, error) {
	return LoadState(f.cityPath)
}

// Enqueue runs the full enqueue transaction: shadow save, bounded
// maintenance, same-reference supersession, append; a failed transaction
// rolls back the just-created shadow bead.
func (f *fileQueue) Enqueue(item Item, shadow beads.NudgesStore) error {
	store, release := f.resolveShadow(shadow)
	defer release()
	var front *Store
	if store.Store != nil {
		front = NewStore(store)
	}
	beadID, created, err := front.Save(item)
	if err != nil {
		return err
	}
	if beadID != "" {
		item.BeadID = beadID
	}
	err = WithState(f.cityPath, func(state *State) error {
		now := time.Now()
		deadline := now.Add(EnqueueMaintenanceBudget)
		if err := RecoverExpiredInFlight(state, front, now, deadline); err != nil {
			return err
		}
		if err := PruneExpired(state, front, now, deadline); err != nil {
			return err
		}
		if err := PruneDead(state, front, now, deadline); err != nil {
			return err
		}
		if queuedItemExists(state, item.ID) {
			return nil
		}
		// Supersede pending and in-flight nudges for the same (agent, source, reference).
		if item.Reference != nil && item.Reference.ID != "" {
			matchesSupersession := func(existing Item) bool {
				return existing.Agent == item.Agent && existing.Source == item.Source &&
					existing.Reference != nil && existing.Reference.Kind == item.Reference.Kind &&
					existing.Reference.ID == item.Reference.ID
			}
			filtered := state.Pending[:0]
			for i, existing := range state.Pending {
				if time.Now().After(deadline) {
					filtered = append(filtered, state.Pending[i:]...)
					break
				}
				if matchesSupersession(existing) {
					existing.DeadAt = now.UTC()
					existing.LastError = "superseded"
					state.Dead = append(state.Dead, existing)
					if err := front.Terminalize(existing, "superseded", "superseded", "", now); err != nil {
						return err
					}
					continue
				}
				filtered = append(filtered, existing)
			}
			state.Pending = filtered
			// Also supersede in-flight nudges. Note: an active delivery may
			// already be running for a superseded item. When it completes, its
			// ack/failure won't find the item in InFlight and will no-op.
			// This causes at most one redundant delivery, not data corruption.
			inFlight := state.InFlight[:0]
			for i, existing := range state.InFlight {
				if time.Now().After(deadline) {
					inFlight = append(inFlight, state.InFlight[i:]...)
					break
				}
				if matchesSupersession(existing) {
					existing.DeadAt = now.UTC()
					existing.LastError = "superseded"
					state.Dead = append(state.Dead, existing)
					if err := front.Terminalize(existing, "superseded", "superseded", "", now); err != nil {
						return err
					}
					continue
				}
				inFlight = append(inFlight, existing)
			}
			state.InFlight = inFlight
		}
		state.Pending = append(state.Pending, item)
		SortState(state)
		return nil
	})
	if err != nil && created && store.Store != nil && beadID != "" {
		// Roll back the leaked shadow bead through the nudge front door, which
		// stamps the canonical close_reason before Close so BdStore.Close can
		// forward it as `bd close --reason` and satisfy validation.on-close=error.
		// Preserve the original enqueue error, but return rollback failures too
		// so leaked open nudge beads are diagnosable.
		if rbErr := NewStore(store).RollbackEnqueue(beadID); rbErr != nil {
			err = errors.Join(err, fmt.Errorf("rollback nudge bead %q: %w", beadID, rbErr))
		}
	}
	return err
}

// EnqueueDeferred appends item with no shadow, maintenance, or supersession —
// the session.Manager deferred-submit shape, byte-identical to its former
// direct WithState append.
func (f *fileQueue) EnqueueDeferred(item Item) error {
	return WithState(f.cityPath, func(state *State) error {
		state.Pending = append(state.Pending, item)
		SortState(state)
		return nil
	})
}

// Ack removes ids from pending/in-flight and terminalizes their shadows.
func (f *fileQueue) Ack(ids []string, outcome, reason, commitBoundary string) error {
	if len(ids) == 0 {
		return nil
	}
	maint := f.shadow()
	defer maint.close()
	want := make(map[string]bool, len(ids))
	for _, id := range ids {
		want[id] = true
	}
	return WithState(f.cityPath, func(state *State) error {
		now := time.Now()
		front := maint.frontForState(state)
		deadline := NoMaintenanceDeadline()
		if err := RecoverExpiredInFlight(state, front, now, deadline); err != nil {
			return err
		}
		if err := PruneExpired(state, front, now, deadline); err != nil {
			return err
		}
		if err := PruneDead(state, front, now, deadline); err != nil {
			return err
		}
		var terminal []Item
		filtered := state.Pending[:0]
		for _, item := range state.Pending {
			if want[item.ID] {
				terminal = append(terminal, item)
				continue
			}
			filtered = append(filtered, item)
		}
		state.Pending = filtered
		inFlight := state.InFlight[:0]
		for _, item := range state.InFlight {
			if want[item.ID] {
				terminal = append(terminal, item)
				continue
			}
			inFlight = append(inFlight, item)
		}
		state.InFlight = inFlight
		for _, item := range terminal {
			// terminal items come from a non-empty Pending/InFlight, so the
			// store is already open; ensureOpen is idempotent and just returns
			// the cached handle here.
			if err := NewStore(maint.ensureOpen()).Terminalize(item, outcome, reason, commitBoundary, now); err != nil {
				return err
			}
		}
		return nil
	})
}

// ReleaseClaims returns undelivered in-flight ids to pending.
func (f *fileQueue) ReleaseClaims(ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	maint := f.shadow()
	defer maint.close()
	want := make(map[string]bool, len(ids))
	for _, id := range ids {
		want[id] = true
	}
	return WithState(f.cityPath, func(state *State) error {
		now := time.Now()
		front := maint.frontForState(state)
		deadline := NoMaintenanceDeadline()
		if err := RecoverExpiredInFlight(state, front, now, deadline); err != nil {
			return err
		}
		if err := PruneExpired(state, front, now, deadline); err != nil {
			return err
		}
		if err := PruneDead(state, front, now, deadline); err != nil {
			return err
		}
		var released []Item
		inFlight := state.InFlight[:0]
		for _, item := range state.InFlight {
			if !want[item.ID] {
				inFlight = append(inFlight, item)
				continue
			}
			item.ClaimedAt = time.Time{}
			item.LeaseUntil = time.Time{}
			released = append(released, item)
		}
		state.InFlight = inFlight
		state.Pending = append(state.Pending, released...)
		SortState(state)
		return nil
	})
}

// RecordFailure applies the retry policy to ids, returning the dead-lettered
// items. Shadow terminalization for dead-lettered items happens OUTSIDE the
// queue lock, best-effort: the dead-letter transition is authoritative and a
// failed bead write must not roll it back, or the items bounce between
// in-flight and pending forever. pruneDead repairs entries whose backing
// record missed terminal state, so drift converges on later operations.
func (f *fileQueue) RecordFailure(ids []string, shadow beads.NudgesStore, cause error, now time.Time, warn io.Writer) ([]Item, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	store, release := f.resolveShadow(shadow)
	defer release()
	var front *Store
	if store.Store != nil {
		front = NewStore(store)
	}
	want := make(map[string]bool, len(ids))
	for _, id := range ids {
		want[id] = true
	}
	var deadLettered []Item
	err := WithState(f.cityPath, func(state *State) error {
		deadLettered = deadLettered[:0]
		deadline := NoMaintenanceDeadline()
		if err := RecoverExpiredInFlight(state, front, now, deadline); err != nil {
			return err
		}
		if err := PruneExpired(state, front, now, deadline); err != nil {
			return err
		}
		if err := PruneDead(state, front, now, deadline); err != nil {
			return err
		}
		var requeued []Item
		var dead []Item
		pending := state.Pending[:0]
		for _, item := range state.Pending {
			if !want[item.ID] {
				pending = append(pending, item)
				continue
			}
			updated, deadLetter := FailedItem(item, cause, now)
			if deadLetter {
				dead = append(dead, updated)
				deadLettered = append(deadLettered, updated)
				continue
			}
			requeued = append(requeued, updated)
		}
		state.Pending = pending
		inFlight := state.InFlight[:0]
		for _, item := range state.InFlight {
			if !want[item.ID] {
				inFlight = append(inFlight, item)
				continue
			}
			updated, deadLetter := FailedItem(item, cause, now)
			if deadLetter {
				dead = append(dead, updated)
				deadLettered = append(deadLettered, updated)
				continue
			}
			requeued = append(requeued, updated)
		}
		state.InFlight = inFlight
		state.Pending = append(state.Pending, requeued...)
		state.Dead = append(state.Dead, dead...)
		SortState(state)
		return nil
	})
	if err != nil {
		return nil, err
	}
	for _, item := range deadLettered {
		if markErr := front.Terminalize(item, "failed", item.LastError, "", now); markErr != nil && warn != nil {
			fmt.Fprintf(warn, "gc nudge: warning: marking dead-lettered nudge %q terminal: %v\n", item.ID, markErr) //nolint:errcheck
		}
	}
	return deadLettered, nil
}

// Rollback removes item by id, dead-lettering it as failed with reason; when
// the item is no longer queued, the shadow alone is terminalized.
func (f *fileQueue) Rollback(front *Store, item Item, reason string) error {
	if f.cityPath == "" || item.ID == "" {
		return nil
	}
	now := time.Now()
	found := false
	err := WithState(f.cityPath, func(state *State) error {
		removed := []Item(nil)
		state.Pending, removed = takeItemsByID(state.Pending, item.ID, removed)
		state.InFlight, removed = takeItemsByID(state.InFlight, item.ID, removed)
		for _, queued := range removed {
			found = true
			queued.LastError = reason
			queued.DeadAt = now.UTC()
			state.Dead = append(state.Dead, queued)
			if err := front.Terminalize(queued, "failed", reason, "", now); err != nil {
				return err
			}
		}
		SortState(state)
		return nil
	})
	if err != nil {
		return err
	}
	if found {
		return nil
	}
	item.LastError = reason
	return front.Terminalize(item, "failed", reason, "", now)
}

// WithdrawWaitNudges removes still-queued wait nudges by id and marks their
// snapshotted shadow beads terminal wait-canceled (the existing waits.go
// two-phase body).
func (f *fileQueue) WithdrawWaitNudges(store beads.Store, ids []string) error {
	return WithdrawWaitNudges(store, f.cityPath, ids)
}

// FailedItem applies the retry policy to one failed delivery: fence
// mismatches dead-letter immediately; MaxAttempts or TTL expiry dead-letter;
// anything else requeues with the RetryDelay backoff.
func FailedItem(item Item, cause error, now time.Time) (Item, bool) {
	item.Attempts++
	item.LastAttemptAt = now.UTC()
	item.LastError = cause.Error()
	item.ClaimedAt = time.Time{}
	item.LeaseUntil = time.Time{}
	if errors.Is(cause, ErrSessionFenceMismatch) {
		item.DeadAt = now.UTC()
		return item, true
	}
	if item.Attempts >= MaxAttempts || (!item.ExpiresAt.IsZero() && !item.ExpiresAt.After(now)) {
		item.DeadAt = now.UTC()
		return item, true
	}
	item.DeliverAfter = now.Add(RetryDelay).UTC()
	return item, false
}

// TerminalStateForDeadItem maps a dead-lettered item's LastError onto the
// terminal-state vocabulary for the repair path.
func TerminalStateForDeadItem(item Item) string {
	switch strings.TrimSpace(item.LastError) {
	case "expired":
		return "expired"
	case "superseded":
		return "superseded"
	default:
		return "failed"
	}
}

// NoMaintenanceDeadline returns a deadline far enough in the future that a
// maintenance pass never stops early. Callers outside the latency-sensitive
// foreground enqueue path (poller, doctor, list/ack/release) want the full
// backlog drained every time, matching pre-budget behavior.
func NoMaintenanceDeadline() time.Time {
	return time.Now().Add(24 * time.Hour)
}

// PruneExpired dead-letters pending items past their deliver-by deadline.
// Exported with RecoverExpiredInFlight / PruneDead as the file backend's
// maintenance surface (cmd/gc test shims exercise the passes directly).
func PruneExpired(state *State, front *Store, now, deadline time.Time) error {
	filtered := state.Pending[:0]
	for i, item := range state.Pending {
		if time.Now().After(deadline) {
			filtered = append(filtered, state.Pending[i:]...)
			break
		}
		if !item.ExpiresAt.IsZero() && !item.ExpiresAt.After(now) {
			item.DeadAt = now.UTC()
			if item.LastError == "" {
				item.LastError = "expired"
			}
			state.Dead = append(state.Dead, item)
			// Best-effort: remove expired item from pending even if bead update fails.
			// A failed bead update here would trap the item in pending forever.
			_ = front.Terminalize(item, "expired", item.LastError, "", now)
			continue
		}
		filtered = append(filtered, item)
	}
	state.Pending = filtered
	SortState(state)
	return nil
}

// RecoverExpiredInFlight dead-letters expired in-flight items and returns
// lease-expired claims to pending for immediate redelivery.
func RecoverExpiredInFlight(state *State, front *Store, now, deadline time.Time) error {
	filtered := state.InFlight[:0]
	for i, item := range state.InFlight {
		if time.Now().After(deadline) {
			filtered = append(filtered, state.InFlight[i:]...)
			break
		}
		if !item.ExpiresAt.IsZero() && !item.ExpiresAt.After(now) {
			item.DeadAt = now.UTC()
			if item.LastError == "" {
				item.LastError = "expired"
			}
			state.Dead = append(state.Dead, item)
			// Best-effort: remove expired item from in-flight even if bead update fails.
			_ = front.Terminalize(item, "expired", item.LastError, "", now)
			continue
		}
		if item.LeaseUntil.IsZero() || !item.LeaseUntil.After(now) {
			item.ClaimedAt = time.Time{}
			item.LeaseUntil = time.Time{}
			item.DeliverAfter = now.UTC()
			state.Pending = append(state.Pending, item)
			continue
		}
		filtered = append(filtered, item)
	}
	state.InFlight = filtered
	SortState(state)
	return nil
}

// PruneDead removes dead-letter items older than DeadRetention when a
// durable terminal bead record exists in the store. Items without a
// confirmed terminal bead are retained so terminal history is not lost if
// the bead store write failed.
func PruneDead(state *State, front *Store, now, deadline time.Time) error {
	cutoff := now.Add(-DeadRetention)
	filtered := state.Dead[:0]
	for i, item := range state.Dead {
		if time.Now().After(deadline) {
			filtered = append(filtered, state.Dead[i:]...)
			break
		}
		if item.BeadID != "" {
			if front == nil {
				// No store available — retain the item to avoid data loss.
				filtered = append(filtered, item)
				continue
			}
			shadow, ok, err := front.FindIncludingTerminal(item.ID)
			if err != nil {
				// Fail open: store lookup errors retain the item rather than
				// blocking the entire queue operation. Pruning is best-effort.
				filtered = append(filtered, item)
				continue
			}
			if !ok || !isTerminalNudgeState(shadow.State) {
				// Repair historical dead-letter entries whose queue state was
				// durable but whose backing bead never received terminal state.
				reason := strings.TrimSpace(item.LastError)
				if reason == "" {
					reason = "failed"
				}
				terminalAt := now
				if !item.DeadAt.IsZero() {
					terminalAt = item.DeadAt
				}
				if err := front.Terminalize(item, TerminalStateForDeadItem(item), reason, "", terminalAt); err != nil {
					filtered = append(filtered, item)
					continue
				}
				shadow, ok, err = front.FindIncludingTerminal(item.ID)
				if err != nil || !ok || !isTerminalNudgeState(shadow.State) {
					filtered = append(filtered, item)
					continue
				}
			}
			if !item.DeadAt.IsZero() && item.DeadAt.Before(cutoff) {
				// Terminal bead confirmed in store — safe to prune once past retention.
				continue
			}
		}
		filtered = append(filtered, item)
	}
	state.Dead = filtered
	return nil
}

// queuedItemExists reports whether id is present in any bucket.
func queuedItemExists(state *State, id string) bool {
	for _, item := range state.Pending {
		if item.ID == id {
			return true
		}
	}
	for _, item := range state.InFlight {
		if item.ID == id {
			return true
		}
	}
	for _, item := range state.Dead {
		if item.ID == id {
			return true
		}
	}
	return false
}

// takeItemsByID splits items into (remaining, removed ∪ matches of id).
func takeItemsByID(items []Item, id string, removed []Item) ([]Item, []Item) {
	if len(items) == 0 {
		return items, removed
	}
	filtered := make([]Item, 0, len(items))
	for _, item := range items {
		if item.ID == id {
			removed = append(removed, item)
			continue
		}
		filtered = append(filtered, item)
	}
	return filtered, removed
}
