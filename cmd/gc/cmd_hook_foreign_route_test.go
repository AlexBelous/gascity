package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/gastownhall/gascity/internal/config"
)

// ga-lmy6yj: `gc hook <agent>` is served work routed to OTHER agents — and other
// RIGS — because the store's routed_to predicate is not applied under the
// native-store fallback. Reproduced 2026-08-05 at the v62-db/v53-binary skew:
// `gc hook gascity/builder` returned five beads and exactly one belonged to it;
// one was routed to beads/reviewer.
//
// The cost is gm-ob7is: the agent wakes, correctly declines work that is not its
// own, and burns a full turn with full cache-read doing so. These tests pin the
// post-filter that stops it without moving the gc/bd pin.

func hookCandidate(id, routedTo string) map[string]any {
	c := map[string]any{"id": id}
	if routedTo != "" {
		c["metadata"] = map[string]any{"gc.routed_to": routedTo}
	}
	return c
}

func hookCandidatesJSON(t *testing.T, items ...map[string]any) string {
	t.Helper()
	arr := make([]any, 0, len(items))
	for _, it := range items {
		arr = append(arr, it)
	}
	b, err := json.Marshal(arr)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(b)
}

func idsIn(t *testing.T, out string) []string {
	t.Helper()
	var arr []map[string]any
	if err := json.Unmarshal([]byte(out), &arr); err != nil {
		t.Fatalf("unmarshal %q: %v", out, err)
	}
	ids := make([]string, 0, len(arr))
	for _, o := range arr {
		if s, ok := o["id"].(string); ok {
			ids = append(ids, s)
		}
	}
	return ids
}

// The exact live shape from the 2026-08-05 reproduction.
func TestFilterForeignRouted_DropsOtherAgentsAndRigs(t *testing.T) {
	in := hookCandidatesJSON(t,
		hookCandidate("ga-2a46gb", "gascity/builder"),  // ours
		hookCandidate("ga-5hdwl6", "gascity/deployer"), // another role
		hookCandidate("ga-drlztz", "beads/reviewer"),   // ANOTHER RIG
		hookCandidate("ga-20zoji", ""),                 // unrouted -> claimable
	)
	got := idsIn(t, filterForeignRoutedHookCandidates(in, []string{"gascity/builder"}))
	want := map[string]bool{"ga-2a46gb": true, "ga-20zoji": true}
	if len(got) != 2 {
		t.Fatalf("kept %v, want exactly ga-2a46gb and ga-20zoji", got)
	}
	for _, id := range got {
		if !want[id] {
			t.Errorf("kept %s, which is routed to another agent", id)
		}
	}
}

// The failure mode that would be WORSE than the bug: dropping the agent's own
// work. Routes are written "rig/role" while session and assignee forms use
// "rig--role"; both must be recognized as ours.
func TestFilterForeignRouted_NeverDropsOwnWorkAcrossSpellings(t *testing.T) {
	for _, tc := range []struct {
		name     string
		route    string
		identity string
	}{
		{"exact", "gascity/builder", "gascity/builder"},
		{"session form identity", "gascity/builder", "gascity--builder"},
		{"session form route", "gascity--builder", "gascity/builder"},
		{"case insensitive", "Gascity/Builder", "gascity/builder"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := hookCandidatesJSON(t, hookCandidate("ga-mine", tc.route))
			got := idsIn(t, filterForeignRoutedHookCandidates(in, []string{tc.identity}))
			if len(got) != 1 || got[0] != "ga-mine" {
				t.Fatalf("dropped the agent's OWN work: route=%q identity=%q kept=%v",
					tc.route, tc.identity, got)
			}
		})
	}
}

// Any one of the agent's several names is enough to claim a route.
func TestFilterForeignRouted_MatchesAnyIdentity(t *testing.T) {
	in := hookCandidatesJSON(t, hookCandidate("ga-mine", "gascity/builder"))
	got := idsIn(t, filterForeignRoutedHookCandidates(in,
		[]string{"", "some-alias", "gascity/builder"}))
	if len(got) != 1 {
		t.Fatalf("kept %v, want the bead matched via the third identity", got)
	}
}

// Fail open everywhere: a filter that can silently empty an agent's queue is a
// worse outage than the over-serving it replaces.
func TestFilterForeignRouted_FailsOpen(t *testing.T) {
	routed := hookCandidatesJSON(t, hookCandidate("ga-x", "someone/else"))

	if got := filterForeignRoutedHookCandidates(routed, nil); got != routed {
		t.Errorf("no identities: filtered anyway, got %q", got)
	}
	if got := filterForeignRoutedHookCandidates("", []string{"a/b"}); got != "" {
		t.Errorf("empty input: got %q", got)
	}
	if got := filterForeignRoutedHookCandidates("not json", []string{"a/b"}); got != "not json" {
		t.Errorf("unparseable: got %q", got)
	}
	obj := `{"id":"solo"}`
	if got := filterForeignRoutedHookCandidates(obj, []string{"a/b"}); got != obj {
		t.Errorf("non-array shape: got %q", got)
	}
	// A candidate with no metadata map at all must survive.
	noMeta := hookCandidatesJSON(t, map[string]any{"id": "ga-nometa"})
	if got := idsIn(t, filterForeignRoutedHookCandidates(noMeta, []string{"a/b"})); len(got) != 1 {
		t.Errorf("candidate without metadata was dropped: %v", got)
	}
}

// doHook must report "no work" once the only candidates belonged to someone
// else — otherwise the agent is still woken, which is the whole cost in
// gm-ob7is.
func TestDoHook_ForeignRoutedOnlyReportsNoWork(t *testing.T) {
	foreign := hookCandidatesJSON(t,
		hookCandidate("ga-a", "gascity/deployer"),
		hookCandidate("ga-b", "beads/reviewer"),
	)
	runner := func(string, string) (string, error) { return foreign, nil }

	var stdout, stderr strings.Builder
	code := doHook("bd ready", "", false, runner, &stdout, &stderr, "gascity/builder")
	if code != 1 {
		t.Fatalf("doHook = %d, want 1 (no work); stdout=%q", code, stdout.String())
	}
	if strings.Contains(stdout.String(), "ga-a") || strings.Contains(stdout.String(), "ga-b") {
		t.Errorf("served another agent's work: %q", stdout.String())
	}
}

// Same input, but one bead is genuinely ours: doHook must still report work.
func TestDoHook_KeepsOwnWorkAmongForeign(t *testing.T) {
	mixed := hookCandidatesJSON(t,
		hookCandidate("ga-a", "gascity/deployer"),
		hookCandidate("ga-mine", "gascity/builder"),
	)
	runner := func(string, string) (string, error) { return mixed, nil }

	var stdout, stderr strings.Builder
	code := doHook("bd ready", "", false, runner, &stdout, &stderr, "gascity/builder")
	if code != 0 {
		t.Fatalf("doHook = %d, want 0 (has work); stdout=%q", code, stdout.String())
	}
	if !strings.Contains(stdout.String(), "ga-mine") {
		t.Errorf("dropped the agent's own work: %q", stdout.String())
	}
}

// With no identities threaded (the pre-existing call shape), behavior is
// exactly as before — this keeps every other doHook caller safe.
func TestDoHook_NoIdentitiesUnchanged(t *testing.T) {
	foreign := hookCandidatesJSON(t, hookCandidate("ga-a", "gascity/deployer"))
	runner := func(string, string) (string, error) { return foreign, nil }

	var stdout, stderr strings.Builder
	code := doHook("bd ready", "", false, runner, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("doHook without identities = %d, want 0 (unfiltered, as before)", code)
	}
}

// Tiers 1 and 2 of the default work query select on --assignee alone and never
// consult routed_to, so an owned crash-recovery bead may carry a stale or
// foreign route. Dropping it would exit "no work" while in_progress work sits
// assigned to this very session — strictly worse than the over-serving.
func TestFilterForeignRouted_KeepsAssignedRowsRegardlessOfRoute(t *testing.T) {
	assigned := func(id, routedTo, assignee string) map[string]any {
		c := hookCandidate(id, routedTo)
		c["assignee"] = assignee
		return c
	}
	in := hookCandidatesJSON(t,
		assigned("ga-mine", "gascity/deployer", "gascity--builder"),
		// The exemption is blanket, not identity-scoped: the store's own
		// assignee matching is what selected the row, and this filter cannot
		// reproduce that matching (session ids, per-store aliases).
		assigned("ga-session-id", "beads/reviewer", "sess-7f3a91c"),
		// Whitespace-only assignee is not an assignee: still filtered.
		assigned("ga-blank", "gascity/deployer", "   "),
	)
	got := idsIn(t, filterForeignRoutedHookCandidates(in, []string{"gascity/builder"}))
	want := map[string]bool{"ga-mine": true, "ga-session-id": true}
	if len(got) != 2 {
		t.Fatalf("kept %v, want exactly ga-mine and ga-session-id", got)
	}
	for _, id := range got {
		if !want[id] {
			t.Errorf("kept %s, which carries no assignee and a foreign route", id)
		}
	}
}

// A pool slot's own QualifiedName is slot-suffixed ("gascity/polecat-2"), but
// the routed-pool tier matches on the BASE pool name (poolDemandTarget), so
// demand work is written as "gascity/polecat". Threading the identity set
// through hookClaimPrimaryRouteTarget (agentutil.RoutedToIdentity) is what
// makes the slot recognize its own base route.
func TestFilterForeignRouted_PoolBaseRouteMatchedViaRoutedToIdentity(t *testing.T) {
	base := config.Agent{Dir: "gascity", Name: "polecat"}
	slot := config.Agent{Dir: "gascity", Name: "polecat-2", PoolName: base.QualifiedName()}
	in := hookCandidatesJSON(t, hookCandidate("ga-pool", base.QualifiedName()))

	// The explicit-arg path blanks GC_TEMPLATE, so this is the full route-target
	// set a slot agent gets.
	identities := hookClaimRouteTargets(
		hookClaimPrimaryRouteTarget(&slot),
		slot.QualifiedName(),
		"",
	)
	if got := idsIn(t, filterForeignRoutedHookCandidates(in, identities)); len(got) != 1 {
		t.Fatalf("dropped the pool slot's own base-route work: identities=%v kept=%v",
			identities, got)
	}

	// Negative control: it is RoutedToIdentity doing the work, not the
	// slot-suffixed qualified name — which must NOT match on its own. If this
	// stops failing, the test above has gone vacuous.
	if got := idsIn(t, filterForeignRoutedHookCandidates(in,
		[]string{slot.QualifiedName()})); len(got) != 0 {
		t.Errorf("slot-suffixed name matched the base route on its own: kept %v", got)
	}
}

// buildWorkQuery actively probes the legacy "<rig>/workflow-control" spelling
// (legacyWorkflowControlQualifiedName), so a bead can carry that route. The raw
// threaded strings never carried it; the hookClaimIdentityCandidates expansion
// does.
func TestFilterForeignRouted_LegacyWorkflowControlAliasMatched(t *testing.T) {
	in := hookCandidatesJSON(t, hookCandidate("ga-legacy", "gascity/workflow-control"))

	identities := hookClaimIdentityCandidates("gascity/control-dispatcher")
	if got := idsIn(t, filterForeignRoutedHookCandidates(in, identities)); len(got) != 1 {
		t.Fatalf("dropped legacy workflow-control work: identities=%v kept=%v",
			identities, got)
	}

	// Negative control: the unexpanded name alone does not carry the alias.
	if got := idsIn(t, filterForeignRoutedHookCandidates(in,
		[]string{"gascity/control-dispatcher"})); len(got) != 0 {
		t.Errorf("unexpanded identity matched the legacy alias: kept %v", got)
	}
}

// A bound→unbound migration leaves routes written in the bound form
// "dir/binding.name" (legacyBoundTemplateMatchesUnboundAgent). Both sides are
// normalized identically, so the agent still recognizes its own work.
func TestFilterForeignRouted_LegacyBoundTemplateSpelling(t *testing.T) {
	for _, tc := range []struct {
		name     string
		route    string
		identity string
	}{
		{"bound route", "gascity/gastown.builder", "gascity/builder"},
		{"bound identity", "gascity/builder", "gascity/gastown.builder"},
		{"bound both", "gascity/gastown.builder", "gascity--gastown.builder"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := hookCandidatesJSON(t, hookCandidate("ga-mine", tc.route))
			got := idsIn(t, filterForeignRoutedHookCandidates(in, []string{tc.identity}))
			if len(got) != 1 || got[0] != "ga-mine" {
				t.Fatalf("dropped the agent's OWN work: route=%q identity=%q kept=%v",
					tc.route, tc.identity, got)
			}
		})
	}

	// The normalization must not collapse genuinely different agents.
	in := hookCandidatesJSON(t, hookCandidate("ga-theirs", "gascity/gastown.deployer"))
	if got := idsIn(t, filterForeignRoutedHookCandidates(in,
		[]string{"gascity/builder"})); len(got) != 0 {
		t.Errorf("kept another agent's bound-form work: %v", got)
	}
}
