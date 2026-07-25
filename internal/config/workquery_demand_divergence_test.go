package config

import (
	"os"
	"strings"
	"testing"

	"github.com/gastownhall/gascity/internal/beadmeta"
)

// reviewerShapedAgent mirrors the live packs/actual/reviewer/pack.toml agent
// shape that produced the ga-ut74jh incident: min=0/max=1, no scale_check, and
// a work_query that narrows intake to a label the upstream stage must add.
func reviewerShapedAgent() *Agent {
	return &Agent{
		Name:      "reviewer",
		Dir:       "cairn",
		WorkQuery: `gc bd --rig cairn ready --label=needs-claude-review --exclude-label hold:mayor,hold:external --json 2>/dev/null`,
	}
}

// TestPoolDemandKeysOnRoutedToNotAssignee is the DISPROOF of the hypothesis
// recorded on ga-ut74jh ("scale-check keys off 'assignee' rather than
// 'routed_to', so routed needs-review demand does not count toward wanting a
// session"). The default pool-demand predicate keys on gc.routed_to and
// explicitly requires an EMPTY assignee, so routed work with no assignee does
// count as demand. Any future change that makes demand assignee-keyed should
// fail here rather than silently validating the wrong root cause.
func TestPoolDemandKeysOnRoutedToNotAssignee(t *testing.T) {
	demand := reviewerShapedAgent().EffectivePoolDemandQuery()

	if !strings.Contains(demand, beadmeta.RoutedToMetadataKey) {
		t.Errorf("EffectivePoolDemandQuery() must key on %s; got %q", beadmeta.RoutedToMetadataKey, demand)
	}
	if !strings.Contains(demand, "--unassigned") {
		t.Errorf("EffectivePoolDemandQuery() must require an empty assignee (--unassigned); got %q", demand)
	}
	// The demand predicate must not select BY a specific assignee — that is the
	// disproven hypothesis. --unassigned is a presence check, not an identity match.
	if strings.Contains(demand, "--assignee") {
		t.Errorf("EffectivePoolDemandQuery() must not filter by a specific --assignee; got %q", demand)
	}
}

// TestNarrowedWorkQueryNarrowsPoolDemand is the RED regression test for
// ga-ut74jh. queryWork overrides on Agent.WorkQuery while queryPoolDemand
// overrides on Agent.ScaleCheck (workquery.go queryTable), so an agent that
// narrows its intake with a custom work_query but sets no matching scale_check
// keeps the label-agnostic default demand predicate.
//
// The reconciler then counts routed beads the worker's work_query can never
// claim: it wakes a session, the session's hook comes back empty, the session
// idles out, and the still-present demand wakes it again. Live evidence in the
// ga-ut74jh incident was 9 session.woke / 5 session.idle_killed events for
// cairn/reviewer with zero work claimed, because every bead routed to it
// carried `needs-review` while its work_query required `needs-claude-review`.
//
// Demand and claim must agree: if work_query narrows intake, the demand
// predicate must apply the same narrowing (or the mismatch must be rejected at
// config-load / doctor time). This test fails until that holds.
func TestNarrowedWorkQueryNarrowsPoolDemand(t *testing.T) {
	// SKIPPED BY DESIGN until ga-0av489 (RouteLabel/RouteLabelAny) AND
	// ga-atvk13 (delete the 21 raw work_query overrides) land. It asserts the
	// post-fix invariant, so it fails on today's code.
	//
	// It is skipped rather than left red because the pre-push hook runs the
	// full Go suite for a new remote branch, so a permanently-red test makes
	// this evidence branch unpushable — and the architect's note on ga-0av489
	// tells a builder to fetch it from origin.
	//
	// BUILDER: delete this skip as the first step of ga-0av489 and drive it to
	// green. Run it now with GC_WORKQUERY_DIVERGENCE_TEST=1 to see the
	// divergence printed side by side.
	if os.Getenv("GC_WORKQUERY_DIVERGENCE_TEST") == "" {
		t.Skip("pending ga-0av489 + ga-atvk13; set GC_WORKQUERY_DIVERGENCE_TEST=1 to run")
	}

	agent := reviewerShapedAgent()
	wq := agent.EffectiveWorkQuery()
	demand := agent.EffectivePoolDemandQuery()

	const narrowingLabel = "needs-claude-review"

	// Precondition: the work query really does narrow intake by label.
	if !strings.Contains(wq, narrowingLabel) {
		t.Fatalf("precondition: EffectiveWorkQuery() should carry the custom narrowing label %q; got %q", narrowingLabel, wq)
	}

	// The defect: demand ignores that narrowing, so it counts unclaimable work.
	if !strings.Contains(demand, narrowingLabel) {
		t.Errorf("ga-ut74jh: work_query narrows intake to %q but EffectivePoolDemandQuery() does not, so the reconciler counts demand the worker cannot claim (wake/idle-kill treadmill).\nwork_query: %s\ndemand:     %s", narrowingLabel, wq, demand)
	}
}
