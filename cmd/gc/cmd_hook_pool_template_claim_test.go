package main

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	"github.com/gastownhall/gascity/internal/beads"
)

// TestPoolMemberClaimsOpenTemplateAssignmentAsConcreteOwner is the hook-level
// behavior missing from the existing exact-identity ready-assignment tier. The
// shared template is eligible only while the bead is open; the persisted claim
// receipt must name the concrete session, never the fungible pool template.
func TestPoolMemberClaimsOpenTemplateAssignmentAsConcreteOwner(t *testing.T) {
	runner := func(string, string) (string, error) {
		return `[{"id":"pool-ready","status":"open","assignee":"pool-worker","metadata":{"gc.routed_to":"pool-worker"}}]`, nil
	}
	var claimCalls [][2]string
	ops := hookClaimOps{
		Runner: runner,
		Claim: func(_ context.Context, _ string, _ []string, beadID, assignee string) (beads.Bead, bool, error) {
			claimCalls = append(claimCalls, [2]string{beadID, assignee})
			return beads.Bead{
				ID:       beadID,
				Status:   "in_progress",
				Assignee: assignee,
				Metadata: map[string]string{"gc.routed_to": "pool-worker"},
			}, true, nil
		},
		ListContinuation: func(context.Context, string, []string, string, string) ([]beads.Bead, error) {
			return nil, nil
		},
	}
	opts := hookClaimOptions{
		Assignee:           "pool-worker-1",
		IdentityCandidates: []string{"pool-worker-1"},
		RouteTargets:       []string{"pool-worker"},
		JSON:               true,
	}

	var stdout, stderr bytes.Buffer
	code := doHookClaim("bd ready --json", "/tmp/work", opts, ops, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("doHookClaim(template assignment) = %d, want 0; stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	if len(claimCalls) != 1 || claimCalls[0] != [2]string{"pool-ready", "pool-worker-1"} {
		t.Fatalf("claim calls = %v, want pool-ready claimed as concrete member pool-worker-1", claimCalls)
	}
	var result hookClaimJSONResult
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatalf("stdout is not JSON: %v\nraw: %q", err, stdout.String())
	}
	if !result.OK || result.Action != "work" || result.Reason != "ready_assignment" ||
		result.BeadID != "pool-ready" || result.Assignee != "pool-worker-1" {
		t.Fatalf("claim result = %+v, want concrete ready-assignment ownership", result)
	}
}
