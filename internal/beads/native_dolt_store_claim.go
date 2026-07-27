package beads

import (
	"context"
	"fmt"

	beadslib "github.com/steveyegge/beads"
)

// ClaimBead atomically claims a bead for actor through the upstream beads
// ClaimIssue primitive. It is the controller-side leverage point: a single
// controller-owned NativeDoltStore performs every worker's claim over its
// pooled SQL connection, so no worker hook opens a MySQL connection to claim.
//
// The claim delegates to upstream ClaimIssue (compare-and-swap UPDATE plus, for
// a server-backed store, DOLT_ADD/COMMIT and cache invalidation); it does not
// synthesize the claim from Get plus Update, which would drop the atomic guard
// and re-introduce the double-claim race the CAS exists to prevent. See
// AtomicClaimer for the full return contract.
func (s *NativeDoltStore) ClaimBead(ctx context.Context, id, actor string) (Bead, bool, error) {
	// A nil ctx is normalized to Background by nativeDoltOperationContext below,
	// so no separate guard is needed here.
	storage, release, err := s.acquireStorage()
	if err != nil {
		return Bead{}, false, err
	}
	defer release()

	claimer, ok := storage.(nativeClaimer)
	if !ok {
		// A managed Dolt storage handle always exposes ClaimIssue; a handle that
		// does not is a version/wiring defect, not a lost race. Surface it as a
		// hard error rather than silently reporting a non-claim.
		return Bead{}, false, fmt.Errorf("claiming bead %q: native storage does not expose ClaimIssue", id)
	}

	opCtx, cancel := nativeDoltOperationContext(ctx)
	defer cancel()

	switch claimErr := claimer.ClaimIssue(opCtx, id, actor); {
	case claimErr == nil:
		// Claim (or idempotent same-actor re-claim) succeeded. Re-read the
		// canonical bead within the same storage handle so the caller gets the
		// committed assignee/status.
		bead, readErr := s.claimReadCanonical(opCtx, storage, id)
		if readErr != nil {
			return Bead{}, true, fmt.Errorf("reloading claimed bead %q: %w", id, readErr)
		}
		return bead, true, nil
	case isNativeClaimConflict(claimErr), isNativeNotClaimable(claimErr):
		// Another actor won, or the bead left the open/unassigned state between
		// candidate selection and claim. Not an error — the caller may re-read.
		return Bead{}, false, nil
	default:
		// Wrap upstream not-found in ErrNotFound; pass any other backend error
		// through so the caller (or the fallback gate) can classify it.
		return Bead{}, false, nativeStoreError(id, claimErr)
	}
}

// claimReadCanonical reads a single bead within an already-acquired storage
// handle, mirroring Get's SearchIssues lookup without a second acquireStorage /
// reconnect cycle. Used only on the post-claim success path.
func (s *NativeDoltStore) claimReadCanonical(ctx context.Context, storage beadslib.Storage, id string) (Bead, error) {
	issues, err := storage.SearchIssues(ctx, "", beadslib.IssueFilter{
		IDs:                 []string{id},
		IncludeDependencies: true,
	})
	if err != nil {
		return Bead{}, nativeStoreError(id, err)
	}
	for _, issue := range issues {
		if issue != nil && issue.ID == id {
			return beadFromNativeIssue(issue)
		}
	}
	return Bead{}, fmt.Errorf("bead %q: %w", id, ErrNotFound)
}

var _ AtomicClaimer = (*NativeDoltStore)(nil)
