package main

import (
	"strings"

	"github.com/gastownhall/gascity/internal/api"
	"github.com/gastownhall/gascity/internal/beads"
)

// epicIssueType is the bead type the generated routed-pool query excludes
// (--exclude-type=epic): an unassigned parent epic has no executable spec, so a
// pool worker claiming one does undefined work (see EffectiveWorkQuery docs).
const epicIssueType = "epic"

// fastPathReader is the narrow controller-read surface the hook fast path needs
// to reproduce the generated default work_query's three tiers over the running
// controller instead of per-hook bd subprocesses. *api.Client satisfies it;
// tests inject a fake.
type fastPathReader interface {
	ListBeads(opts api.ListBeadsOpts) (api.CachedRead[[]beads.Bead], error)
	BeadsReady() (api.CachedRead[[]beads.Bead], error)
}

// The production controller client is the fast-path reader; pin it so a client
// signature drift breaks here at compile time rather than at the call site.
var _ fastPathReader = (*api.Client)(nil)

// poolDemandOriginEligible mirrors the shell work query's origin gate
// (poolDemandOriginGateScript: `case "$GC_SESSION_ORIGIN" in ephemeral|"") ;; *)
// exit 0 ;;`). Only an ephemeral (managed-pool) or unset origin may claim routed
// pool demand; a user-origin session must not.
func poolDemandOriginEligible(sessionOrigin string) bool {
	switch strings.TrimSpace(sessionOrigin) {
	case "", "ephemeral":
		return true
	default:
		return false
	}
}

// fastPathClaimCandidates reproduces, over controller reads only, the ordered
// candidate list the generated default work_query would emit — so the existing
// tryHookClaim selection/adoption/route logic consumes it unchanged and a worker
// hook never opens its own SQL connection to discover work.
//
// It mirrors the shell query's SHORT-CIRCUIT emit (workquery.go
// standardAssignedWorkQueryScript + the pool-demand probe): the first non-empty
// tier wins, in this order, which is what preserves invariant 2 (the generated
// query's tier/identity ordering):
//
//  1. assigned in_progress (crash recovery), per identity in the given order
//     (session id > session name > alias), first hit — one bead;
//  2. assigned ready, same identity order, first hit — one bead;
//  3. routed pool: origin-gated, unassigned, non-epic ready beads whose route
//     matches a target via hookClaimMatchesRoute (which includes the
//     run_target/workflow migration fallback), in ready order.
//
// identities is the raw [GC_SESSION_ID, GC_SESSION_NAME, GC_ALIAS] set the shell
// assigned tiers iterate (empties skipped); routeTargets is the pool route set.
// Any read error propagates so the caller can classify a pre-request connection
// failure (IsConnError) and fall back to the subprocess path; a non-connection
// error is a real controller verdict and must not silently shell out.
//
// Scope: this reproduces the STANDARD generated default query only. The caller
// must not route legacy control-dispatcher targets or a custom work_query here —
// those keep the subprocess path (see the fast-path gate at the call site).
func fastPathClaimCandidates(r fastPathReader, identities, routeTargets []string, sessionOrigin string) ([]beads.Bead, error) {
	// Tier 1: assigned in_progress, per identity, first hit. A dedicated bounded
	// ListBeads per identity mirrors the shell's `bd list --status in_progress
	// --assignee=$id --limit=1` and short-circuits before the ready read.
	for _, id := range identities {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		cr, err := r.ListBeads(api.ListBeadsOpts{Status: "in_progress", Assignee: id, Limit: 1})
		if err != nil {
			return nil, err
		}
		if len(cr.Body) > 0 {
			return []beads.Bead{cr.Body[0]}, nil
		}
	}

	// Tiers 2 and 3 share a single federated ready read (the controller-side
	// equivalent of `bd ready` across the city and every rig store).
	ready, err := r.BeadsReady()
	if err != nil {
		return nil, err
	}

	// Tier 2: assigned ready, per identity, first hit.
	for _, id := range identities {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		for _, b := range ready.Body {
			if strings.TrimSpace(b.Assignee) == id {
				return []beads.Bead{b}, nil
			}
		}
	}

	// Tier 3: routed pool demand. Origin-gated exactly as the shell query is; a
	// non-eligible origin never claims pool demand.
	if !poolDemandOriginEligible(sessionOrigin) {
		return nil, nil
	}
	var pool []beads.Bead
	for _, b := range ready.Body {
		if strings.TrimSpace(b.Assignee) != "" {
			continue // pool demand is unassigned work only
		}
		if b.Type == epicIssueType {
			continue // --exclude-type=epic
		}
		if hookClaimMatchesRoute(b, routeTargets) {
			pool = append(pool, b)
		}
	}
	return pool, nil
}
