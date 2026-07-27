package beads

import (
	"context"
	"time"
)

// ClaimBead forwards an atomic claim to the backing store and, on success,
// reflects the new assignee/in_progress state into the cache. It satisfies
// AtomicClaimer only when the backing store does; a backing without the
// capability yields ErrConditionalReleaseUnsupported's claim analog via the
// ok==false, err!=nil path so callers can fall back rather than silently
// treat an unsupported backing as a lost race.
func (c *CachingStore) ClaimBead(ctx context.Context, id, actor string) (Bead, bool, error) {
	claimer, ok := c.backing.(AtomicClaimer)
	if !ok {
		return Bead{}, false, ErrAtomicClaimUnsupported
	}
	bead, claimed, err := claimer.ClaimBead(ctx, id, actor)
	if err != nil || !claimed {
		// A lost race (claimed=false, err=nil) leaves the cache untouched: the
		// backing state did not change on our behalf, and the winner's own
		// claim reaches this cache through the event bus.
		return bead, claimed, err
	}

	fresh, refreshed := c.refreshBeadAfterWrite(id, "refresh bead after claim")
	var updated Bead
	notify := false
	c.mu.Lock()
	c.noteLocalMutationLocked(id)
	if refreshed {
		c.absorbFreshLocked(id, fresh, time.Now(), absorbOpts{
			depsMode:   depsFromFields,
			seqMode:    seqKeep,
			clearDirty: true,
		})
		updated = cloneBead(fresh)
		notify = true
	} else if cached, ok := c.beads[id]; ok {
		// Backing refresh read failed; patch the known post-claim fields onto the
		// cached entry and mark it dirty so the reconciler re-reads. The claim is
		// proven committed, so status/assignee are forced regardless of read lag.
		cached.Status = "in_progress"
		cached.Assignee = actor
		cached.UpdatedAt = time.Now()
		c.absorbFreshLocked(id, cached, time.Now(), absorbOpts{
			depsMode:   depsKeepCached,
			seqMode:    seqKeep,
			clearDirty: false,
		})
		c.markDirtyLocked(id)
		updated = cloneBead(cached)
		notify = true
	} else {
		c.markDirtyLocked(id)
	}
	c.clearDependentReadyProjectionsLocked(id)
	c.markFreshLocked(time.Now())
	c.updateStatsLocked()
	c.mu.Unlock()
	if notify {
		c.notifyChange("bead.updated", updated)
	}

	// Prefer the backing's canonical bead (it carries the committed revision and
	// dependencies); fall back to the cache-refreshed copy only if the backing
	// returned an empty bead.
	if bead.ID == "" {
		return updated, true, nil
	}
	return bead, true, nil
}

var _ AtomicClaimer = (*CachingStore)(nil)
