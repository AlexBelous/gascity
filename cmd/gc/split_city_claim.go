package main

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"

	"github.com/gastownhall/gascity/internal/beadmeta"
	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/config"
)

// On a split city the worker's work_query (now composite `gc ready`) surfaces
// graph-class step beads that live in the INFRA store, but the winning hookStore
// still points at the WORK store — so a claim/update `bd` subprocess built from
// its dir/env would run `bd update --claim` against the work store and fail
// ("bead not found"). These helpers route each claim-time mutation to the store
// that OWNS the target bead, by reserved id-prefix (gcg- → infra), mirroring the
// read-side storeForID routing and the existing per-rig crossStoreClaimDir.

// hookClaimTargetsInfra reports whether a claim-time mutation on beadID must be
// routed to the infra store: true only on a split city for a reserved-class id
// namespace ("gcg-...", including bd's wisp-tier "gcg-wisp-..." ids — the shape
// production molecules actually claim). This is the by-prefix ownership
// decision, mirroring the read-side claimableStore.storeForID and
// slingSourceStoreRootForCandidate.
func hookClaimTargetsInfra(cityPath, beadID string) bool {
	return cityHasInfraStore(cityPath) && config.IsReservedClassBeadID(beadID)
}

// hookClaimInfraDirEnv returns the (dir, env) a claim-time bd mutation on beadID
// must use. A reserved-class (gcg-...) id on a split city is owned by the infra
// store, so the mutation targets the infra scope's dir/env (the same
// bdRuntimeEnvForRigWithError projection the infra store's opener and the sling
// write path use). Work-class ids, single-store cities, and any infra-env
// resolution failure fall back to the passed (dir, env) — a wrong-store write
// then fails loud rather than silently hitting the wrong store.
func hookClaimInfraDirEnv(cityPath string, cfg *config.City, beadID, dir string, env []string) (string, []string) {
	if !hookClaimTargetsInfra(cityPath, beadID) {
		return dir, env
	}
	infraDir := infraScopeRoot(cityPath)
	overrides, err := bdRuntimeEnvForRigWithError(cityPath, cfg, infraDir)
	if err != nil {
		return dir, env
	}
	return infraDir, mergeRuntimeEnv(env, overrides)
}

// splitCityHookClaimOps returns the claim ops with the mutation seams wrapped to
// route by-id to the infra store on a split city. Only the routing-sensitive
// seams are set; tryHookClaim's applyDefaults fills the rest (Runner, DrainAck,
// EmitClaimRejected, ResolveWorkBranch, Now) with their production defaults. The
// wrappers delegate to the same *WithBdStore implementations, only swapping the
// (dir, env) for a reserved-class target.
// hookInfraSQLiteOp runs fn against a freshly opened raw embedded-sqlite infra
// store when targetID is resident there (reserved-class prefix, or a migrated
// legacy-prefix bead found by point-read). handled=false means the caller must
// use the bd-routing path (non-sqlite city, or the bead is not infra-resident).
// bd cannot read the embedded store, so without this every claim-time mutation
// on an infra-resident bead hangs or fails "not found" in the bd subprocess.
// Opening the raw store per op is the infra ADR's direct multi-process WAL
// model; gc hook is a short-lived subprocess.
func hookInfraSQLiteOp(cityPath, targetID string, fn func(store beads.Store) error) (handled bool, err error) {
	if !cityInfraScopeIsSQLite(cityPath) {
		return false, nil
	}
	scope := infraScopeRoot(cityPath)
	st, openErr := beads.OpenSQLiteStore(
		filepath.Join(scope, ".beads"),
		beads.WithSQLiteStoreIDPrefix(readScopeIssuePrefix(scope)),
	)
	if openErr != nil {
		return true, fmt.Errorf("opening embedded sqlite infra store for %s: %w", targetID, openErr)
	}
	defer func() { _ = closeBeadStoreHandle(st) }()
	if !config.IsReservedClassBeadID(targetID) {
		// Migrated legacy-prefix infra beads (ga-wisp roots, mc-wisp session
		// beads) keep their rig/HQ-era prefixes, so residence — not prefix —
		// decides ownership.
		if _, getErr := st.Get(targetID); getErr != nil {
			if errors.Is(getErr, beads.ErrNotFound) {
				return false, nil
			}
			return true, getErr
		}
	}
	return true, fn(st)
}

func splitCityHookClaimOps(cityPath string, cfg *config.City) hookClaimOps {
	route := func(beadID, dir string, env []string) (string, []string) {
		return hookClaimInfraDirEnv(cityPath, cfg, beadID, dir, env)
	}
	return hookClaimOps{
		Claim: func(ctx context.Context, dir string, env []string, beadID, assignee string) (beads.Bead, bool, error) {
			var claimed beads.Bead
			var ok bool
			if handled, err := hookInfraSQLiteOp(cityPath, beadID, func(st beads.Store) error {
				claimer, has := st.(interface {
					Claim(id, assignee string) (beads.Bead, bool, error)
				})
				if !has {
					return fmt.Errorf("embedded sqlite infra store has no native Claim")
				}
				var cerr error
				claimed, ok, cerr = claimer.Claim(beadID, assignee)
				return cerr
			}); handled {
				return claimed, ok, err
			}
			d, e := route(beadID, dir, env)
			return hookClaimWithBdStore(ctx, d, e, beadID, assignee)
		},
		// StampWorkMeta writes the claim-time execution-identity patch
		// (gc.work_branch + session back-reference) onto the claimed bead. On a
		// split city that bead may be reserved-class, so route the write by the
		// bead's own id prefix to the infra store.
		StampWorkMeta: func(ctx context.Context, dir string, env []string, beadID, assignee string, patch map[string]string) error {
			if handled, err := hookInfraSQLiteOp(cityPath, beadID, func(st beads.Store) error {
				return st.Update(beadID, beads.UpdateOpts{Metadata: patch})
			}); handled {
				return err
			}
			d, e := route(beadID, dir, env)
			return hookStampWorkMetaWithBdStore(ctx, d, e, beadID, assignee, patch)
		},
		// Continuation siblings live in the same store as the workflow root, so
		// route the list read by the ROOT bead's prefix.
		ListContinuation: func(ctx context.Context, dir string, env []string, rootID, group string) ([]beads.Bead, error) {
			var listed []beads.Bead
			if handled, err := hookInfraSQLiteOp(cityPath, rootID, func(st beads.Store) error {
				var lerr error
				listed, lerr = st.List(beads.ListQuery{
					Status: "open",
					Metadata: map[string]string{
						beadmeta.RootBeadIDMetadataKey:        rootID,
						beadmeta.ContinuationGroupMetadataKey: group,
					},
					TierMode: beads.TierBoth,
				})
				return lerr
			}); handled {
				return listed, err
			}
			d, e := route(rootID, dir, env)
			return hookListContinuationWithBdStore(ctx, d, e, rootID, group)
		},
		AssignContinuation: func(ctx context.Context, dir string, env []string, beadID, assignee string) error {
			if handled, err := hookInfraSQLiteOp(cityPath, beadID, func(st beads.Store) error {
				return st.Update(beadID, beads.UpdateOpts{Assignee: &assignee})
			}); handled {
				return err
			}
			d, e := route(beadID, dir, env)
			return hookAssignContinuationWithBdStore(ctx, d, e, beadID, assignee)
		},
		// The session bead is session-class, which on a split city also lives in
		// the infra store — route by the session bead's own id prefix.
		RecordSessionPointers: func(ctx context.Context, dir string, env []string, assignee, sessionBeadID, runID, stepID string) error {
			if handled, err := hookInfraSQLiteOp(cityPath, sessionBeadID, func(st beads.Store) error {
				return st.Update(sessionBeadID, beads.UpdateOpts{Metadata: map[string]string{
					beadmeta.CurrentRunIDMetadataKey:   runID,
					beadmeta.ActiveWorkBeadMetadataKey: stepID,
				}})
			}); handled {
				return err
			}
			d, e := route(sessionBeadID, dir, env)
			return hookRecordSessionPointersWithBdStore(ctx, d, e, assignee, sessionBeadID, runID, stepID)
		},
	}
}
