package beads

import (
	"context"
	"strings"
)

// AtomicClaimer is an optional store capability that atomically claims a bead
// by ID for a specific actor using compare-and-swap semantics: the bead is
// awarded to actor (assignee=actor, status=in_progress) only if it is currently
// open and unassigned (or already assigned to the same actor). It is the narrow
// primitive the controller hot path uses so a worker hook never opens its own
// SQL connection to claim.
//
// Return contract (mirrors BdStore.Claim):
//   - (bead, true, nil): actor now owns the bead. Idempotent — re-claiming a
//     bead already in_progress for the same actor also returns true.
//   - (Bead{}, false, nil): the bead was already claimed by a different actor,
//     or is no longer claimable (status moved out of open). The caller may
//     re-read to surface who won the race; a lost claim is never an error.
//   - (Bead{}, false, err): a hard failure — ErrNotFound if the bead is absent,
//     or a transport/backend error. An ambiguous mutation error must be retried
//     only by the same actor for the same bead (upstream ClaimIssue is
//     idempotent for the same actor and rejects a different one).
//
// The actor argument is authoritative for NativeDoltStore and CachingStore. A
// BdStore claims as the actor its CommandRunner was constructed with
// (BEADS_ACTOR); its implementation forwards to the existing Claim and requires
// the passed actor to equal that configured actor, which the fallback path
// guarantees by building the store with the same identity.
type AtomicClaimer interface {
	ClaimBead(ctx context.Context, id, actor string) (Bead, bool, error)
}

// nativeClaimer matches the upstream beads storage.BulkIssueStore.ClaimIssue
// method without importing beads' internal package (the same local-interface
// technique rawDBGetter uses for RawDBAccessor). A managed Dolt storage handle
// (*dolt.DoltStore) satisfies it; an in-memory test fake implements it
// explicitly. ClaimIssue performs the atomic conditional UPDATE and, for a
// server-backed store, its own DOLT_ADD/COMMIT and cache invalidation.
type nativeClaimer interface {
	ClaimIssue(ctx context.Context, id string, actor string) error
}

// isNativeClaimConflict reports whether err is upstream beads' "already claimed
// by a different actor" sentinel. The sentinel (storage.ErrAlreadyClaimed,
// "issue already claimed") lives in beads' internal package and is not
// re-exported, so it is matched by message, the same way BdStore matches bd's
// CLI conflict phrasings (isBdClaimConflictMessage).
func isNativeClaimConflict(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(strings.ToLower(err.Error()), "already claimed")
}

// isNativeNotClaimable reports whether err is upstream beads' "issue not
// claimable" sentinel (storage.ErrNotClaimable) — the bead moved out of the
// open, unassigned state this claim requires (claimed-and-progressed by another
// actor, deferred, blocked, or closed) between candidate selection and claim.
// Like a conflict it means "we did not acquire it", never a hard error.
func isNativeNotClaimable(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(strings.ToLower(err.Error()), "not claimable")
}
