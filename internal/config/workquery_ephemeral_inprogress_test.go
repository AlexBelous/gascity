package config

import (
	"encoding/json"
	"os/exec"
	"strings"
	"testing"
)

// Regression coverage for the ephemeral counterpart of the crash-recovery
// re-serve defect fixed for the list-based in_progress tier by
// inProgressBlockedByEnrichmentScript (see workquery_inprogress_blocked_test.go).
// #4726 hardened standardAssignedInProgressWorkQueryScript and
// legacyControlAssignedInProgressWorkQueryScript's `bd list`-backed candidate
// against gate/dependency blocking, but left
// ephemeralAssignedInProgressProbeScript on its original unguarded shape: it
// matches an ephemeral in_progress bead to this session's identity via
// `bd query` plus a client-side jq assignee filter, then serves it with no
// readiness check at all. That re-serves a gate-blocked or dependency-blocked
// ephemeral bead on every hook tick -- the same defect signature #4726 fixed
// for the list-based tiers (ga-7xx459).
//
// These tests EXECUTE the generated shell against a fake `bd` on PATH, so they
// pin observable behavior rather than the script's spelling (the byte-for-byte
// shape is pinned separately by TestWorkQueryGolden).

const ephemeralInProgressQueryRow = `[{"id":"eph-1","status":"in_progress","assignee":"sess-1","title":"ephemeral work","ephemeral":true}]`

// fakeBdForEphemeralInProgress returns a fake bd that reports one ephemeral
// in_progress bead assigned to "sess-1" from `bd query` (the subcommand
// bdQueryEphemeralStatusShell actually issues -- unlike the list-based tiers,
// the ephemeral probe has no server-side assignee filter, so a client-side jq
// select narrows the result), and the given dependency rows from `bd show`.
// `bd list` returns empty so assertions isolate the ephemeral probe from the
// list-based in_progress tier that runs immediately before it in the same
// loop iteration.
func fakeBdForEphemeralInProgress(depsJSON string) string {
	return `#!/bin/sh
case "$1" in
  list) printf '[]' ;;
  query) printf '%s' '` + ephemeralInProgressQueryRow + `' ;;
  show) printf '%s' '[{"id":"eph-1","status":"in_progress","dependencies":` + depsJSON + `}]' ;;
  *) printf '[]' ;;
esac
`
}

// runEphemeralInProgressProbe executes the in_progress tier of the default
// work query (the list-based candidate check, then the ephemeral probe)
// against a fake bd and returns the decoded rows.
func runEphemeralInProgressProbe(t *testing.T, bdScript string) []map[string]any {
	t.Helper()
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq not available; the work-query shell requires it")
	}
	// `printf "[]"` is the terminal fallback the real query uses when no tier
	// produces a candidate.
	script := standardAssignedInProgressWorkQueryScript(false) + `printf "[]"`
	out := runShellWithFakeBd(t, script, map[string]string{"GC_SESSION_ID": "sess-1"}, bdScript)

	var rows []map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &rows); err != nil {
		t.Fatalf("tier output is not a JSON array: %v (output %q)", err, out)
	}
	return rows
}

// TestEphemeralInProgressProbeSkipsGateBlockedCandidate is the primary
// regression: a human gate filed after an ephemeral step was claimed stores a
// ready-blocking "blocks" edge on the blocked bead. The probe must not serve
// it -- same requirement #4726 already pins for the list-based tiers.
func TestEphemeralInProgressProbeSkipsGateBlockedCandidate(t *testing.T) {
	rows := runEphemeralInProgressProbe(t, fakeBdForEphemeralInProgress(
		`[{"id":"gate-1","status":"open","dependency_type":"blocks","await_type":"human"}]`))
	if len(rows) != 0 {
		t.Fatalf("gate-blocked ephemeral in_progress bead was re-served by the crash-recovery probe: %v", rows)
	}
}

// TestEphemeralInProgressProbeServesUnblockedCandidate is the anti-regression
// guard for the fix itself: crash recovery must still work for ephemeral
// beads, and the served row must carry blocked_by so the hook-side filter
// (filterUnreadyHookCandidates) can reason about it downstream. The stock
// (unfixed) probe serves the candidate too, but never attaches blocked_by --
// that omission is what this test pins.
func TestEphemeralInProgressProbeServesUnblockedCandidate(t *testing.T) {
	rows := runEphemeralInProgressProbe(t, fakeBdForEphemeralInProgress(`[]`))
	if len(rows) != 1 {
		t.Fatalf("unblocked ephemeral in_progress bead was NOT served; crash recovery is broken: %v", rows)
	}
	if rows[0]["id"] != "eph-1" {
		t.Fatalf("served the wrong bead: %v", rows)
	}
	if _, ok := rows[0]["blocked_by"]; !ok {
		t.Errorf("served row is missing the blocked_by array the hook-side filter reads: %v", rows)
	}
}

// TestEphemeralInProgressProbeServesCandidateWithClosedBlocker pins that a
// resolved gate releases an ephemeral step exactly as it does for the
// list-based tiers. The unfixed probe already "passes" the row-count half of
// this (it never checks blocking status at all), so the blocked_by assertion
// is what makes this a genuine differentiator between old and new behavior.
func TestEphemeralInProgressProbeServesCandidateWithClosedBlocker(t *testing.T) {
	rows := runEphemeralInProgressProbe(t, fakeBdForEphemeralInProgress(
		`[{"id":"gate-1","status":"closed","dependency_type":"blocks","await_type":"human"}]`))
	if len(rows) != 1 {
		t.Fatalf("ephemeral step with a CLOSED blocker was not resumed: %v", rows)
	}
	if _, ok := rows[0]["blocked_by"]; !ok {
		t.Errorf("served row is missing the blocked_by array the hook-side filter reads: %v", rows)
	}
}

// TestEphemeralInProgressProbeServesCandidateWhenShowOutputMalformed pins the
// fail-open policy of the shared blocked_by enrichment (#4726's
// inProgressBlockedByEnrichmentScript) as applied to the ephemeral probe.
//
// Unlike the list-based tiers -- where `bd list --assignee=$id` is already
// server-side scoped to this session, so the raw candidate itself can safely
// be echoed back unchanged on a parse failure -- the ephemeral query
// (`bd query "ephemeral=true AND status=..."`) is NOT scoped to this session:
// it returns rows for every session's ephemeral work, and a client-side jq
// filter is what narrows it to this session's candidate. A malformed `bd
// query` response therefore cannot be blindly served (there would be no way
// to know it was ever this session's row to begin with), so the meaningful
// fail-open boundary for this probe is one layer in: once a well-formed
// candidate has been matched to this session, a malformed/erroring `bd show`
// (the dependency lookup) must not drop it.
func TestEphemeralInProgressProbeServesCandidateWhenShowOutputMalformed(t *testing.T) {
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq not available; the work-query shell requires it")
	}
	bdScript := `#!/bin/sh
case "$1" in
  list) printf '[]' ;;
  query) printf '%s' '` + ephemeralInProgressQueryRow + `' ;;
  show) printf 'warning: index rebuild in progress' ;;
  *) printf '[]' ;;
esac
`
	rows := runEphemeralInProgressProbe(t, bdScript)
	if len(rows) != 1 {
		t.Fatalf("ephemeral candidate was dropped when bd show returned unparseable output: %v", rows)
	}
	if rows[0]["id"] != "eph-1" {
		t.Fatalf("served the wrong bead: %v", rows)
	}
	if _, ok := rows[0]["blocked_by"]; !ok {
		t.Errorf("served row is missing the blocked_by array the hook-side filter reads: %v", rows)
	}
}

// TestLegacyControlEphemeralInProgressProbeSkipsBlockedCandidate pins that
// the control-dispatcher variant (legacyControlAssignedInProgressWorkQueryScript)
// calls the identical ephemeralAssignedInProgressProbeScript and therefore
// needs the identical fix, matching the coverage
// TestLegacyControlInProgressTierSkipsBlockedCandidate provides for the
// list-based tier. Fixing only the standard call site would leave rigs on the
// legacy control shape churning exactly as before.
func TestLegacyControlEphemeralInProgressProbeSkipsBlockedCandidate(t *testing.T) {
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq not available; the work-query shell requires it")
	}
	script := legacyControlAssignedInProgressWorkQueryScript(false) + `printf "[]"`
	out := runShellWithFakeBd(t, script, map[string]string{"GC_SESSION_ID": "sess-1"},
		fakeBdForEphemeralInProgress(`[{"id":"gate-1","status":"open","dependency_type":"blocks","await_type":"human"}]`))

	var rows []map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &rows); err != nil {
		t.Fatalf("tier output is not a JSON array: %v (output %q)", err, out)
	}
	if len(rows) != 0 {
		t.Fatalf("legacy-control probe re-served a gate-blocked ephemeral in_progress bead: %v", rows)
	}
}
