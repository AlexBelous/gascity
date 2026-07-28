package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/gastownhall/gascity/internal/api"
	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/config"
	"github.com/gastownhall/gascity/internal/rollout"
)

// fastPathClient is the full controller surface the hook fast path drives:
// tier-discovery reads plus claim/identity mutations. *api.Client satisfies it.
type fastPathClient interface {
	fastPathReader
	fastPathClaimer
}

var _ fastPathClient = (*api.Client)(nil)

// hookFastPathEligible reports whether the controller claim fast path may serve
// this hook claim. All must hold: the rollout gate is on; the agent uses the
// generated default work_query (an explicit custom work_query is the caller's
// full-discovery contract and MUST keep the subprocess adapter — invariant 3);
// and neither the agent nor its route target is a legacy control-dispatcher,
// whose workflow-control aliasing only the shell query reproduces.
func hookFastPathEligible(flags rollout.Flags, workQuery, resolvedAgentName, primaryRouteTarget string) bool {
	if !flags.HookClaimFastPath() {
		return false
	}
	if strings.TrimSpace(workQuery) != "" {
		return false
	}
	if hookLegacyWorkflowControlName(strings.TrimSpace(resolvedAgentName)) != "" {
		return false
	}
	if hookLegacyWorkflowControlName(strings.TrimSpace(primaryRouteTarget)) != "" {
		return false
	}
	return true
}

// claimHookWorkFastPath serves a generated-default hook claim through the
// controller: it reproduces the tier-ordered candidate list via controller reads
// (no worker SQL), then runs the shared tryHookClaim selection with API-backed
// claim ops so the claim and identity stamp also avoid a worker-side connection.
//
// Return contract:
//   - (code, true): the fast path handled the claim — a claim was written, or a
//     clean no-work drain, or a real controller verdict failed fast. code is the
//     process exit code.
//   - (0, false): a PRE-REQUEST connection failure means the controller was
//     unreachable; the caller must fall back to the subprocess path. This is the
//     ONLY fallback trigger (invariant 4). A non-connection controller error is a
//     definite verdict: it is logged and returned as a terminal failure, never
//     shelled out (which would multiply the connection pressure the cure removes).
func claimHookWorkFastPath(client fastPathClient, identities, routeTargets []string, sessionOrigin string, scopes []string, workDir string, claimOpts hookClaimOptions, stdout, stderr io.Writer) (int, bool) {
	const cmdName = "hook --claim"
	candidates, err := fastPathClaimCandidates(client, identities, routeTargets, sessionOrigin, scopes)
	if err != nil {
		if api.IsConnError(err) {
			logRoute(stderr, cmdName, "fallback", "controller-unreachable")
			return 0, false
		}
		logRoute(stderr, cmdName, "api", "read-error")
		fmt.Fprintf(stderr, "gc hook --claim: controller read failed: %v\n", err) //nolint:errcheck // best-effort stderr
		return 1, true
	}
	payload, err := json.Marshal(candidates)
	if err != nil || len(candidates) == 0 {
		// A []beads.Bead never fails to marshal; an empty tier set is an explicit
		// no-work signal the runner must express as "[]", not "null".
		payload = []byte("[]")
	}
	logRoute(stderr, cmdName, "api", "")
	ops := newAPIFastPathClaimOps(client)
	ops.Runner = func(string, string) (string, error) { return string(payload), nil }
	return doHookClaim(cmdName, workDir, claimOpts, ops, stdout, stderr), true
}

// fastPathReader is the narrow controller-read surface the hook fast path needs

// epicIssueType is the bead type the generated routed-pool query excludes
// (--exclude-type=epic): an unassigned parent epic has no executable spec, so a
// pool worker claiming one does undefined work (see EffectiveWorkQuery docs).
const epicIssueType = "epic"

// fastPathClaimer is the narrow controller-mutation surface the fast path's
// claim ops use. *api.Client satisfies it; tests inject the real client against
// an httptest server.
type fastPathClaimer interface {
	ClaimBead(ctx context.Context, id, actor string) (beads.Bead, bool, error)
	GetBead(id string) (api.CachedRead[beads.Bead], error)
	UpdateBeadMetadata(ctx context.Context, id string, patch map[string]string) error
}

var _ fastPathClaimer = (*api.Client)(nil)

// newAPIFastPathClaimOps builds the hookClaimOps whose claim-time MUTATIONS run
// through the controller so a worker hook never opens its own SQL connection:
// Claim uses the atomic controller claim, and StampWorkMeta (the claim-time
// execution-identity write) uses the bead-update API.
//
// The remaining ops are left nil for applyDefaults to fill: DrainAck (runtime
// API), EmitClaimRejected (event bus), ResolveWorkBranch (git), PublishRunMap
// (filesystem) and Now open no bead SQL. ListContinuation / AssignContinuation
// keep the bd defaults deliberately: continuation-group pre-assignment is a rare
// non-hot-path sub-step the design defers ("do not migrate arbitrary gc bd
// commands in this slice … rank remaining subprocess callers" after the hot path
// is proven). The dominant per-tick discovery + claim + identity path is fully
// controller-routed here; Runner is supplied by the caller (the fast-path
// candidate list).
func newAPIFastPathClaimOps(client fastPathClaimer) hookClaimOps {
	return hookClaimOps{
		Claim:         apiFastPathClaim(client),
		StampWorkMeta: apiFastPathStampWorkMeta(client),
	}
}

// apiFastPathClaim adapts the controller claim to hookClaimFunc. On a lost race
// it re-reads the bead so the caller can name the winning claimant in the
// bead.claim_rejected event (best-effort, mirroring hookClaimWithBdStore); a
// read error degrades to the empty bead the claim already returned.
func apiFastPathClaim(client fastPathClaimer) hookClaimFunc {
	return func(ctx context.Context, _ string, _ []string, beadID, assignee string) (beads.Bead, bool, error) {
		bead, claimed, err := client.ClaimBead(ctx, beadID, assignee)
		if err != nil {
			return beads.Bead{}, false, err
		}
		if claimed {
			return bead, true, nil
		}
		if cur, getErr := client.GetBead(beadID); getErr == nil {
			return cur.Body, false, nil
		}
		return bead, false, nil
	}
}

// apiFastPathStampWorkMeta adapts the identity-metadata write to
// hookStampWorkMetaFunc via the bead-update API.
func apiFastPathStampWorkMeta(client fastPathClaimer) hookStampWorkMetaFunc {
	return func(ctx context.Context, _ string, _ []string, beadID, _ string, patch map[string]string) error {
		return client.UpdateBeadMetadata(ctx, beadID, patch)
	}
}

// fastPathReader is the narrow controller-read surface the hook fast path needs
// to reproduce the generated default work_query's three tiers over the running
// controller instead of per-hook bd subprocesses. *api.Client satisfies it;
// tests inject a fake.
//
// Both reads accept a store scope: a rig name or the city name selects a single
// backing store, and an empty scope federates every store. Scope is how the fast
// path reproduces the legacy firstStoreWithWork STORE-outermost precedence
// (invariant 2) — see fastPathClaimCandidates. BeadsReady also takes an
// includeEphemeral flag; the fast path always sets it (and ListBeads.IncludeEphemeral)
// so ephemeral molecule/wisp work stays visible, matching the generated query's
// --include-ephemeral probes.
type fastPathReader interface {
	ListBeads(opts api.ListBeadsOpts) (api.CachedRead[[]beads.Bead], error)
	BeadsReady(scope string, includeEphemeral bool) (api.CachedRead[[]beads.Bead], error)
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
// It mirrors the legacy firstStoreWithWork STORE-outermost precedence
// (cmd_hook.go): the generated query runs against each backing store IN ORDER
// and the FIRST store with ready work wins — its three-tier result is returned
// and later stores are never consulted. scopes is that store order (a rig name
// or the city name per entry; see hookFastPathScopeOrder), which is what
// preserves invariant 2 for BOTH agent shapes:
//
//   - a city-scoped (cross-store) agent reads its city store first, then each
//     rig store in config order;
//   - a rig-scoped agent reads its OWN rig store first, then the city store —
//     so its own-rig routed pool work outranks city-store assigned work, which a
//     single federated read (tier-outermost) would invert.
//
// Within each scope the three tiers keep their order (workquery.go
// standardAssignedWorkQueryScript + the pool-demand probe): the first non-empty
// tier wins:
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
func fastPathClaimCandidates(r fastPathReader, identities, routeTargets []string, sessionOrigin string, scopes []string) ([]beads.Bead, error) {
	// Reproduce legacy firstStoreWithWork's exact primary/secondary error contract
	// (hook_cross_store.go): an error on the agent's OWN store — the FIRST scope in
	// STORE-outermost order — is REMEMBERED, not fatal. Later stores are still
	// consulted, and if one has work that work wins, so a transient own-store outage
	// never masks real work waiting in a federated store. The remembered own-store
	// error is surfaced only when NO scope yields work, so it reaches the caller (a
	// pre-request conn failure -> fallback; a real store outage -> terminal) instead
	// of being silently read as a false no-work drain. An error on a FEDERATED
	// secondary store is best-effort discovery and is dropped, so one flaky or
	// absent rig store cannot wedge the hook.
	var primaryErr error
	for i, scope := range dedupeFastPathScopes(scopes) {
		cands, err := fastPathScopeCandidates(r, scope, identities, routeTargets, sessionOrigin)
		if err != nil {
			if i == 0 {
				primaryErr = err
			}
			continue
		}
		if len(cands) > 0 {
			return cands, nil
		}
	}
	return nil, primaryErr
}

// dedupeFastPathScopes normalizes the scope order, dropping duplicate entries
// while preserving first-seen order (a rig-scoped agent's own store can appear
// twice in the legacy hookStore list — as the rig primary and again as the
// agent's own rig-coordinate env). An empty/absent scope list collapses to a
// single federated pass ("") so a caller that cannot resolve store order still
// reads every store, matching the pre-scope behavior.
func dedupeFastPathScopes(scopes []string) []string {
	if len(scopes) == 0 {
		return []string{""}
	}
	seen := make(map[string]bool, len(scopes))
	out := make([]string, 0, len(scopes))
	for _, s := range scopes {
		s = strings.TrimSpace(s)
		if seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}

// fastPathScopeCandidates runs the three-tier reproduction against a SINGLE
// store scope and returns the first non-empty tier's candidates, mirroring one
// iteration of the legacy firstStoreWithWork loop. An empty scope reads every
// store (federated), the pre-scope behavior.
func fastPathScopeCandidates(r fastPathReader, scope string, identities, routeTargets []string, sessionOrigin string) ([]beads.Bead, error) {
	// Tier 1: assigned in_progress, per identity, first hit. A dedicated bounded
	// ListBeads per identity mirrors the shell's `bd list --status in_progress
	// --assignee=$id --limit=1` and short-circuits before the ready read.
	for _, id := range identities {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		cr, err := r.ListBeads(api.ListBeadsOpts{Status: "in_progress", Assignee: id, Rig: scope, Limit: 1, IncludeEphemeral: true})
		if err != nil {
			return nil, err
		}
		if len(cr.Body) > 0 {
			return []beads.Bead{cr.Body[0]}, nil
		}
	}

	// Tiers 2 and 3 share a single ready read scoped to this store (the
	// controller-side equivalent of `bd ready` against one store). It spans both
	// tiers (includeEphemeral) so ephemeral ready work is not silently dropped.
	ready, err := r.BeadsReady(scope, true)
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
	//
	// The shell probe (poolDemandFirstRowFunctionScript) reads CANONICAL
	// gc.routed_to demand first and only falls through to the legacy
	// gc.run_target migration when no canonical demand exists. Preserve that
	// precedence: collect canonical and migration matches separately in ready
	// order, and return the canonical set whenever it is non-empty so a
	// migration-only bead earlier in ready order never outranks a canonical one.
	if !poolDemandOriginEligible(sessionOrigin) {
		return nil, nil
	}
	var canonical, migration []beads.Bead
	for _, b := range ready.Body {
		if strings.TrimSpace(b.Assignee) != "" {
			continue // pool demand is unassigned work only
		}
		if b.Type == epicIssueType {
			continue // --exclude-type=epic
		}
		if !hookClaimMatchesRoute(b, routeTargets) {
			continue
		}
		if hookClaimCanonicalRouteMatch(b, routeTargets) {
			canonical = append(canonical, b)
		} else {
			migration = append(migration, b)
		}
	}
	if len(canonical) > 0 {
		return canonical, nil
	}
	return migration, nil
}

// hookFastPathScopeOrder returns the store scopes the fast path must read IN
// ORDER to reproduce the legacy firstStoreWithWork precedence (cmd_hook.go's
// `stores` construction). A city-scoped (cross-store) agent reads its city store
// first, then every rig store in config declaration order; a rig-scoped agent
// reads its own rig store first, then the city store. Any other agent reads only
// the city store. cityName is the scope key the controller's BeadStores() uses
// for the city store (api_state.go), matching the rig key for a rig store.
func hookFastPathScopeOrder(cfg *config.City, a *config.Agent, agentIdentity, cityName string) []string {
	cityName = strings.TrimSpace(cityName)
	if agentIsCrossStoreEligible(a) {
		scopes := []string{cityName}
		if cfg != nil {
			for i := range cfg.Rigs {
				scopes = append(scopes, strings.TrimSpace(cfg.Rigs[i].Name))
			}
		}
		return scopes
	}
	if rig := rigScopedHookRig(cfg, agentIdentity); rig != "" {
		return []string{rig, cityName}
	}
	return []string{cityName}
}
