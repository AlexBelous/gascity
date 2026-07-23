package main

// Worker ready-discovery federation for the graph class: the hook's
// work-query runner execs bd shells (`bd ready …`), which cannot see the
// embedded graph store — so on a graph-routed city the runner is wrapped to
// UNION the store's in-process ready rows into the shell's JSON output.
// Over-inclusion is safe: the downstream claim pipeline's identity/route
// filters (hookClaimExistingOrAssigned, claimFirstEligibleHookCandidate)
// are the authoritative selectors. Non-JSON outputs (count forms) pass
// through untouched; routed-pool demand wakes ride the controller dispatch
// path, which is federated separately. This is the split-branch gc-ready
// composite (63235fe0a) folded into the runner seam this branch already
// threads.

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/gastownhall/gascity/internal/beadmeta"
	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/config"
)

// graphFederatedWorkQueryRunner wraps the shell work-query runner with the
// graph-store union. Fail-loud contract: on a routed city a graph-store read
// failure errors the whole discovery — a silent work-only result would hide
// graph-resident steps (the exact fail-open bug the split branch guarded
// against).
func graphFederatedWorkQueryRunner(cityPath string, cfg *config.City) hookStoreRunner {
	return func(command, dir string, env []string) (string, error) {
		out, err := shellWorkQueryWithEnv(command, dir, env)
		if err != nil {
			return out, err
		}
		st, routed, rerr := routedGraphStoreFor(cityPath, cfg)
		if rerr != nil {
			return "", fmt.Errorf("graph-class routing: %w", rerr)
		}
		if !routed {
			return out, nil
		}
		merged, mergeErr := mergeGraphReadyIntoWorkQueryOutput(out, st)
		if mergeErr != nil {
			return "", mergeErr
		}
		return merged, nil
	}
}

// mergeGraphReadyIntoWorkQueryOutput unions the graph store's ready rows
// into a JSON-array work-query output, deduped by id (shell rows win).
// Output that is not a JSON bead array (count forms, no-work markers other
// than an empty array) passes through untouched.
func mergeGraphReadyIntoWorkQueryOutput(out string, st *beads.SQLiteStore) (string, error) {
	normalized := strings.TrimSpace(normalizeWorkQueryOutput(strings.TrimSpace(out)))
	var rows []beads.Bead
	switch {
	case normalized == "" || !strings.HasPrefix(normalized, "["):
		if normalized != "" {
			return out, nil // count form / prose: not a candidate list
		}
	default:
		if err := json.Unmarshal([]byte(normalized), &rows); err != nil {
			return out, nil //nolint:nilerr // non-bead JSON output: pass through
		}
	}
	graphRows, err := st.Ready(beads.ReadyQuery{TierMode: beads.TierBoth})
	if err != nil {
		return "", fmt.Errorf("graph-class ready: %w", err)
	}
	rows, err = filterAttachBlockedByGraphRoot(rows, st)
	if err != nil {
		return "", err
	}
	seen := make(map[string]bool, len(rows))
	for _, b := range rows {
		seen[b.ID] = true
	}
	for _, b := range graphRows {
		if !seen[b.ID] {
			rows = append(rows, b)
		}
	}
	buf, err := json.Marshal(rows)
	if err != nil {
		return "", fmt.Errorf("graph-class ready merge: %w", err)
	}
	return string(buf), nil
}

// filterAttachBlockedByGraphRoot withholds work-store candidates whose
// gc.attached_workflow_root names a still-open root in the graph store —
// the cross-store attach block bd cannot express as a dep edge. A dangling
// marker (root missing from its owning store) fails LOUD, matching the
// federation's fail-loud contract; unmarked beads pass untouched.
func filterAttachBlockedByGraphRoot(rows []beads.Bead, st *beads.SQLiteStore) ([]beads.Bead, error) {
	out := rows[:0]
	for _, b := range rows {
		rootID := strings.TrimSpace(b.Metadata[beadmeta.AttachedWorkflowRootMetadataKey])
		if rootID == "" || !config.IsReservedClassBeadID(rootID) {
			out = append(out, b)
			continue
		}
		root, err := st.Get(rootID)
		if err != nil {
			return nil, fmt.Errorf("resolving attached workflow root %s for %s: %w", rootID, b.ID, err)
		}
		if root.Status == "closed" {
			out = append(out, b)
		}
	}
	return out, nil
}
