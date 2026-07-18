package tmux

import (
	"slices"
	"testing"
	"time"
)

// TestBuildKillTargetsFromSnapshot_ContainsBlastRadius is the acceptance
// surface for the session-massacre fix. It proves the kill set is derived from
// one atomic snapshot and stays confined to the pane's own tree + its genuinely
// reparented group members — NOT every ppid==1 process, which on macOS is every
// GUI app (launchd is PID 1's role), the bug that TERM'd Finder/Dock/tmux/etc.
func TestBuildKillTargetsFromSnapshot_ContainsBlastRadius(t *testing.T) {
	const (
		pane      = "100" // pane leader, pgid 100
		child     = "101" // agent's child shell
		grandkid  = "102" // deepest — must appear before its parent (deepest-first)
		orphan    = "103" // our child that outlived its parent: pgid 100, reparented to init
		subreaper = "104" // our orphan adopted by a user-session subreaper instead of init
		guiApp    = "200" // Finder-like: ppid==1 but a DIFFERENT pgid — must NOT be killed
		otherPane = "300" // an unrelated pane's tree — must NOT be killed
	)
	snap := map[string]procIdentity{
		pane:      {ppid: "1", pgid: "100", start: "Mon Jul 6 08:00:00 2026"},
		child:     {ppid: pane, pgid: "100", start: "Mon Jul 6 08:00:01 2026"},
		grandkid:  {ppid: child, pgid: "100", start: "Mon Jul 6 08:00:02 2026"},
		orphan:    {ppid: "1", pgid: "100", start: "Mon Jul 6 08:00:03 2026"},
		subreaper: {ppid: "900", pgid: "100", start: "Mon Jul 6 08:00:04 2026"},
		guiApp:    {ppid: "1", pgid: "200", start: "Mon Jul 6 07:30:00 2026"},
		otherPane: {ppid: "1", pgid: "300", start: "Mon Jul 6 08:00:00 2026"},
	}

	descendants, reparented, identity := buildKillTargetsFromSnapshot(pane, snap)

	// Descendants: the pane's tree, deepest-first (grandkid before child).
	if gi, ci := slices.Index(descendants, grandkid), slices.Index(descendants, child); gi == -1 || ci == -1 || gi > ci {
		t.Errorf("descendants must be deepest-first with %s before %s, got %v", grandkid, child, descendants)
	}
	// The reparented orphan (our pgid + ppid==1) is collected.
	if !slices.Contains(reparented, orphan) {
		t.Errorf("reparented orphan %s (our pgid, reparented to init) must be collected, got %v", orphan, reparented)
	}
	if !slices.Contains(reparented, subreaper) {
		t.Errorf("subreaper orphan %s (our pgid, parent outside tree) must be collected, got %v", subreaper, reparented)
	}
	// BLAST-RADIUS GUARD: a ppid==1 GUI app on a different pgid is NEVER a target.
	all := append(append([]string{}, descendants...), reparented...)
	if slices.Contains(all, guiApp) {
		t.Errorf("GUI app %s (ppid==1, foreign pgid) must NOT be killed — this is the session-massacre bug", guiApp)
	}
	if slices.Contains(all, otherPane) {
		t.Errorf("unrelated pane %s must NOT be killed, got %v", otherPane, all)
	}
	// Identity map carries the snapshot start time for every target (killVerified
	// re-checks it to skip a recycled PID).
	for _, pid := range append(all, pane) {
		if identity[pid] != snap[pid].start {
			t.Errorf("identity[%s] = %q, want snapshot start %q", pid, identity[pid], snap[pid].start)
		}
	}
}

// TestBuildKillTargetsFromSnapshot_PIDReuseCycleTerminates proves the snapshot
// walk is cycle-guarded: a snapshot whose ppid graph loops (PID reuse can make
// a "child" point back at an ancestor) must terminate rather than recurse
// forever. The visited-set dedup and maxDescendantDepth bound both stop it; a
// regression would hang, which the test process's own timeout would catch.
func TestBuildKillTargetsFromSnapshot_PIDReuseCycleTerminates(t *testing.T) {
	// 100 -> 101 -> 102, plus 103 whose parent 102 also (via reuse) parents 100:
	// the walk from 100 must not revisit 100 through the cycle.
	snap := map[string]procIdentity{
		"100": {ppid: "102", pgid: "100", start: "a"}, // cycle: 100's parent is its own descendant
		"101": {ppid: "100", pgid: "100", start: "b"},
		"102": {ppid: "101", pgid: "100", start: "c"},
		"103": {ppid: "102", pgid: "100", start: "d"},
	}

	descendants, _, _ := buildKillTargetsFromSnapshot("100", snap)

	// Terminates, and never lists the root itself as its own descendant.
	if slices.Contains(descendants, "100") {
		t.Errorf("root must not appear in its own descendants (cycle leak), got %v", descendants)
	}
	if !slices.Contains(descendants, "101") || !slices.Contains(descendants, "103") {
		t.Errorf("expected the acyclic descendants 101 and 103, got %v", descendants)
	}
}

func TestProviderEnvSkipsEscapeForPiAlias(t *testing.T) {
	if !providerEnvSkipsEscape("my-pi/tmux") {
		t.Fatal("pi provider alias should skip pre-enter Escape")
	}
}

func TestProviderEnvSkipsEscapeForCopilot(t *testing.T) {
	if !providerEnvSkipsEscape("copilot") {
		t.Fatal("copilot provider should skip pre-enter Escape")
	}
}

// TestComputeExcludingKillSet_SelfCloseExcludesCallerKeepsAgent locks in the
// fix for the self-close wedge: when `gc session close` runs from inside the
// pane it is tearing down, the caller is a descendant of the pane leader (the
// agent). The caller must be excluded from the TERM list so it survives long
// enough to finish cleanup, while the pane leader (agent) is still reached.
func TestComputeExcludingKillSet_SelfCloseExcludesCallerKeepsAgent(t *testing.T) {
	const (
		agentPID  = "100" // pane leader (e.g. the coding agent) — must be killed
		shellPID  = "101" // intermediate shell spawned by the agent
		callerPID = "102" // gc session close — the excluded caller
	)
	exclude := map[string]bool{callerPID: true}

	killList, killPaneLeader := computeExcludingKillSet(
		agentPID,
		[]string{shellPID, callerPID},
		nil,
		exclude,
	)

	if !killPaneLeader {
		t.Error("pane leader (agent) must be killed, but it was reported excluded")
	}
	if slices.Contains(killList, callerPID) {
		t.Errorf("caller %s must be excluded from TERM list, got %v", callerPID, killList)
	}
	if !slices.Contains(killList, shellPID) {
		t.Errorf("non-excluded descendant %s must be in TERM list, got %v", shellPID, killList)
	}
}

// TestComputeExcludingKillSet_ExternalCallerKillsEverything verifies that when
// the caller lives outside the pane (e.g. the supervisor running the close),
// excluding its PID is a harmless no-op: every process in the pane's tree is
// still terminated.
func TestComputeExcludingKillSet_ExternalCallerKillsEverything(t *testing.T) {
	const agentPID = "200"
	exclude := map[string]bool{"999": true} // external caller, not in the pane tree

	killList, killPaneLeader := computeExcludingKillSet(
		agentPID,
		[]string{"203", "201"},
		[]string{"202", "201"},
		exclude,
	)

	if !killPaneLeader {
		t.Error("pane leader must be killed for an external caller")
	}
	want := []string{"203", "201", "202"}
	if !slices.Equal(killList, want) {
		t.Errorf("kill list = %v, want deepest-first descendants followed by unique reparented processes %v", killList, want)
	}
}

// TestComputeExcludingKillSet_ExcludedPaneLeaderSurvives guards the degenerate
// case where the pane leader itself is in the exclusion set: it must not be
// signaled directly (the final tmux kill-session reaps it instead).
func TestComputeExcludingKillSet_ExcludedPaneLeaderSurvives(t *testing.T) {
	const agentPID = "300"
	exclude := map[string]bool{agentPID: true}

	_, killPaneLeader := computeExcludingKillSet(agentPID, nil, nil, exclude)

	if killPaneLeader {
		t.Error("an excluded pane leader must not be killed directly")
	}
}

// TestKillIdentityMatches_PIDReuseSkipsKill pins the pre-kill identity
// decision that stops the session-massacre TOCTOU: a recycled PID (different
// start time), an already-gone process (empty current), and a target that was
// never in the discovery snapshot (empty want) must all refuse the signal;
// only an exact start-time match may kill.
func TestKillIdentityMatches_PIDReuseSkipsKill(t *testing.T) {
	cases := []struct {
		name    string
		current string
		want    string
		signal  bool
	}{
		{"exact match kills", "Mon Jul 6 08:00:00 2026", "Mon Jul 6 08:00:00 2026", true},
		{"recycled PID (different start) skips", "Mon Jul 6 08:34:37 2026", "Mon Jul 6 08:00:00 2026", false},
		{"process gone (empty current) skips", "", "Mon Jul 6 08:00:00 2026", false},
		{"not in snapshot (empty want) skips", "Mon Jul 6 08:00:00 2026", "", false},
		{"both empty skips", "", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := killIdentityMatches(tc.current, tc.want); got != tc.signal {
				t.Errorf("killIdentityMatches(%q, %q) = %v, want %v", tc.current, tc.want, got, tc.signal)
			}
		})
	}
}

func TestTerminateProcessSetReturnsWhenTerminatedProcessesExit(t *testing.T) {
	alive := map[string]bool{"101": true, "102": true}
	var signals []string
	var sleeps []time.Duration
	now := time.Unix(0, 0)

	terminateProcessSet(
		[]string{"101", "102"},
		time.Second,
		func(pid, signal string) {
			signals = append(signals, signal+":"+pid)
			if signal == "TERM" {
				alive[pid] = false
			}
		},
		func(pid string) bool { return alive[pid] },
		func(delay time.Duration) {
			sleeps = append(sleeps, delay)
			now = now.Add(delay)
		},
		func() time.Time { return now },
	)

	if want := []string{"TERM:101", "TERM:102"}; !slices.Equal(signals, want) {
		t.Fatalf("signals = %v, want %v", signals, want)
	}
	if len(sleeps) != 0 {
		t.Fatalf("sleep calls = %v, want none after TERM made every process exit", sleeps)
	}
}

func TestTerminateProcessSetKillsOnlyProcessesStillAliveAfterGracePeriod(t *testing.T) {
	alive := map[string]bool{"201": true, "202": true}
	var signals []string
	var slept time.Duration
	now := time.Unix(0, 0)

	terminateProcessSet(
		[]string{"201", "202"},
		2*processExitCheckInterval,
		func(pid, signal string) {
			signals = append(signals, signal+":"+pid)
			if signal == "TERM" && pid == "201" {
				alive[pid] = false
			}
		},
		func(pid string) bool { return alive[pid] },
		func(delay time.Duration) {
			slept += delay
			now = now.Add(delay)
		},
		func() time.Time { return now },
	)

	want := []string{"TERM:201", "TERM:202", "KILL:202"}
	if !slices.Equal(signals, want) {
		t.Fatalf("signals = %v, want %v", signals, want)
	}
	if slept != 2*processExitCheckInterval {
		t.Fatalf("slept = %s, want full grace period %s for surviving process", slept, 2*processExitCheckInterval)
	}
}

func TestTerminateProcessSetReturnsWhenProcessExitsDuringGracePeriod(t *testing.T) {
	var signals []string
	checks := 0
	slept := time.Duration(0)
	now := time.Unix(0, 0)

	terminateProcessSet(
		[]string{"301"},
		time.Second,
		func(pid, signal string) { signals = append(signals, signal+":"+pid) },
		func(string) bool {
			checks++
			return checks < 3
		},
		func(delay time.Duration) {
			slept += delay
			now = now.Add(delay)
		},
		func() time.Time { return now },
	)

	if want := []string{"TERM:301"}; !slices.Equal(signals, want) {
		t.Fatalf("signals = %v, want %v", signals, want)
	}
	if slept != 2*processExitCheckInterval {
		t.Fatalf("slept = %s, want two observations (%s)", slept, 2*processExitCheckInterval)
	}
}

func TestTerminateProcessSetCountsProbeTimeAgainstGracePeriod(t *testing.T) {
	var signals []string
	slept := time.Duration(0)
	now := time.Unix(0, 0)
	probeDuration := 2 * processExitCheckInterval

	terminateProcessSet(
		[]string{"401"},
		3*processExitCheckInterval,
		func(pid, signal string) { signals = append(signals, signal+":"+pid) },
		func(string) bool {
			now = now.Add(probeDuration)
			return true
		},
		func(delay time.Duration) {
			slept += delay
			now = now.Add(delay)
		},
		func() time.Time { return now },
	)

	if want := []string{"TERM:401", "KILL:401"}; !slices.Equal(signals, want) {
		t.Fatalf("signals = %v, want %v", signals, want)
	}
	if slept != processExitCheckInterval {
		t.Fatalf("slept = %s, want remaining grace budget %s after slow probe", slept, processExitCheckInterval)
	}
}

// knownSet builds a descendant-set lookup from the given pids.
func knownSet(pids ...string) map[string]bool {
	m := make(map[string]bool, len(pids))
	for _, p := range pids {
		m[p] = true
	}
	return m
}

func TestReparentedOrphans_CollectsInitAndSubreaperOrphans(t *testing.T) {
	// leader=100, one live descendant=200. Group also holds:
	//   300 reparented to init (ppid 1) — classic case
	//   400 reparented to systemd --user subreaper (ppid 900) — the case the
	//        old PPID==1 test missed
	//   500 still a child of a live descendant (ppid 200) — owned elsewhere
	//   600 whose parent read failed ("") — must be skipped
	known := knownSet("100", "200")
	parents := map[string]string{
		"300": "1",
		"400": "900", // systemd --user pid, not init
		"500": "200",
		"600": "",
	}
	parentOf := func(pid string) string { return parents[pid] }

	got := reparentedOrphans([]string{"200", "300", "400", "500", "600"}, known, parentOf)
	slices.Sort(got)
	want := []string{"300", "400"}
	if !slices.Equal(got, want) {
		t.Fatalf("reparentedOrphans = %v, want %v", got, want)
	}
}

func TestReparentedOrphans_SkipsKnownDescendants(t *testing.T) {
	known := knownSet("100", "200", "300")
	parentOf := func(string) string { return "1" }
	if got := reparentedOrphans([]string{"200", "300"}, known, parentOf); len(got) != 0 {
		t.Fatalf("reparentedOrphans = %v, want empty (all are known descendants)", got)
	}
}
