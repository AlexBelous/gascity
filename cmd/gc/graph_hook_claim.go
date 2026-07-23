package main

// Claim-time routing for graph-class beads (the split-branch
// split_city_claim.go pattern, adapted in-process): on a graph-routed city
// the worker's claim/continuation/stamp mutations on gcg- ids apply
// directly against the embedded graph store — a bd subprocess built from
// the winning hookStore's dir/env would run against the WORK store and fail
// "bead not found" (bd cannot reach the embedded store). Work-class ids,
// unrouted cities, and any routing failure keep the *WithBdStore defaults,
// which then fail loud rather than silently writing the wrong store.

import (
	"context"
	"strings"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/config"
)

// graphHookClaimStore resolves the routed graph store when beadID is
// graph-class and the city routes graph to sqlite. ok=false means "not this
// router's business" — the caller uses the bd default.
func graphHookClaimStore(cityPath string, cfg *config.City, beadID string) (*beads.SQLiteStore, bool) {
	prefix, _ := config.ReservedClassPrefix(config.BeadClassGraph)
	if !strings.HasPrefix(beadID, prefix+"-") {
		return nil, false
	}
	st, routed, err := routedGraphStoreFor(cityPath, cfg)
	if err != nil || !routed {
		return nil, false
	}
	return st, true
}

// graphRoutedHookClaimOps returns claim ops whose mutation seams route gcg-
// targets to the embedded graph store. Only the routing-sensitive seams are
// set; applyDefaults fills the rest.
func graphRoutedHookClaimOps(cityPath string, cfg *config.City) hookClaimOps {
	return hookClaimOps{
		Claim: func(ctx context.Context, dir string, env []string, beadID, assignee string) (beads.Bead, bool, error) {
			if st, ok := graphHookClaimStore(cityPath, cfg, beadID); ok {
				return st.Claim(beadID, assignee)
			}
			return hookClaimWithBdStore(ctx, dir, env, beadID, assignee)
		},
		ListContinuation: func(ctx context.Context, dir string, env []string, rootID, group string) ([]beads.Bead, error) {
			if st, ok := graphHookClaimStore(cityPath, cfg, rootID); ok {
				return st.List(beads.ListQuery{
					Status:   "open",
					ParentID: rootID,
					Label:    group,
					TierMode: beads.TierBoth,
				})
			}
			return hookListContinuationWithBdStore(ctx, dir, env, rootID, group)
		},
		AssignContinuation: func(ctx context.Context, dir string, env []string, beadID, assignee string) error {
			if st, ok := graphHookClaimStore(cityPath, cfg, beadID); ok {
				return st.Update(beadID, beads.UpdateOpts{Assignee: &assignee})
			}
			return hookAssignContinuationWithBdStore(ctx, dir, env, beadID, assignee)
		},
		StampWorkMeta: func(ctx context.Context, dir string, env []string, beadID, assignee string, patch map[string]string) error {
			if st, ok := graphHookClaimStore(cityPath, cfg, beadID); ok {
				return st.Update(beadID, beads.UpdateOpts{Metadata: patch})
			}
			return hookStampWorkMetaWithBdStore(ctx, dir, env, beadID, assignee, patch)
		},
	}
}
