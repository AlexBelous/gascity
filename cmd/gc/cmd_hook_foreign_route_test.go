package main

import (
	"encoding/json"
	"strings"
	"testing"
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
