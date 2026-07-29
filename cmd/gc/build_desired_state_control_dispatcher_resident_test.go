package main

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/config"
	"github.com/gastownhall/gascity/internal/runtime"
)

// A deterministic control-dispatcher is a RESIDENT lane: its start command runs
// `gc convoy control --serve --follow`, a serve loop that blocks on city events
// rather than exiting per bead. It ships max_active_sessions=1 with no min floor
// (internal/bootstrap/packs/core/agents/control-dispatcher/agent.toml), so once
// its control queue empties no demand source keeps the singleton desired. Before
// the gc-0ychy fix the overlay dropped the live session on that zero-demand
// tick and the reconciler orphan-drained the healthy serve loop, respawning a
// replacement on the next control bead — a per-rig drain/respawn treadmill.
//
// These tests pin the retention invariant AND its guards: only a genuinely
// running (active/awake) singleton dispatcher is retained; asleep/drained/
// creating/draining/duplicate/removed-from-config sessions still follow the
// existing scale-to-zero / pending-create-rollback / orphan-cleanup paths. The
// negatives assert "no dispatcher session is desired for the template at all"
// (countDesiredDispatcherSessions == 0), not merely that one specific bead is
// absent: dropping the running-state gate would spawn a PHANTOM second
// dispatcher under a fresh name, which a per-bead absence check would miss.

const residentControlDispatcherTemplate = "core.control-dispatcher"

// residentControlDispatcherCfg builds a city with one deterministic
// control-dispatcher agent in the shipped shape (max_active_sessions=1, no min
// floor). Agent.QualifiedName() resolves to "core.control-dispatcher".
func residentControlDispatcherCfg() *config.City {
	maxActive := 1
	return &config.City{
		Workspace: config.Workspace{Name: "gc"},
		Agents: []config.Agent{{
			Name:              config.ControlDispatcherAgentName,
			BindingName:       "core",
			StartCommand:      config.ControlDispatcherStartCommandFor("{{.Agent}}"),
			MaxActiveSessions: &maxActive,
		}},
	}
}

// createControlDispatcherSessionBead materializes a pool-managed singleton
// control-dispatcher session bead in the given lifecycle state, owning its
// canonical pool session name (beadOwnsPoolSessionName). Returns (beadID,
// session_name).
func createControlDispatcherSessionBead(t *testing.T, store beads.Store, state string) (string, string) {
	t.Helper()
	created, err := store.Create(beads.Bead{
		Title:  "control-dispatcher session",
		Type:   sessionBeadType,
		Status: "open",
		Labels: []string{sessionBeadLabel},
		Metadata: map[string]string{
			"template":                residentControlDispatcherTemplate,
			"agent_name":              residentControlDispatcherTemplate,
			"alias":                   residentControlDispatcherTemplate,
			"canonical_instance_name": residentControlDispatcherTemplate,
			"state":                   state,
			"pool_managed":            "true",
			"session_origin":          "ephemeral",
		},
	})
	if err != nil {
		t.Fatalf("create control-dispatcher session bead: %v", err)
	}
	sn := PoolSessionName(residentControlDispatcherTemplate, created.ID)
	if err := store.SetMetadata(created.ID, "session_name", sn); err != nil {
		t.Fatalf("set session_name: %v", err)
	}
	return created.ID, sn
}

// countDesiredDispatcherSessions counts desired-state entries for the
// control-dispatcher template. Negatives assert this is 0 (no retention AND no
// phantom spawn); the duplicate test asserts 1.
func countDesiredDispatcherSessions(res DesiredStateResult) int {
	n := 0
	for _, tp := range res.State {
		if tp.TemplateName == residentControlDispatcherTemplate {
			n++
		}
	}
	return n
}

// requireLiveControlDispatcherFixture asserts the fixture actually registers as
// a live, pool-managed control-dispatcher session using the same predicates the
// reconciler uses. Without it a mis-built fixture could silently pin a different
// code path and pass for the wrong reason.
func requireLiveControlDispatcherFixture(t *testing.T, cfg *config.City, store beads.Store, sn string) {
	t.Helper()
	infos, err := collectAllOpenSessionInfos(cfg, store, nil, nil)
	if err != nil {
		t.Fatalf("collectAllOpenSessionInfos: %v", err)
	}
	for _, si := range infos {
		if si.SessionNameMetadata == sn && isPoolManagedSessionInfo(si) && poolSessionIsLiveInfo(si) &&
			agentTemplateIdentitiesEquivalent(cfg, si.Template, residentControlDispatcherTemplate) {
			return
		}
	}
	t.Fatalf("fixture is not a live pool-managed control-dispatcher session %q (infos=%d) — "+
		"the test would exercise a different path", sn, len(infos))
}

// TestBuildDesiredState_LiveControlDispatcherRetainedOnZeroDemandTick proves a
// running singleton control-dispatcher stays desired across repeated
// zero-demand builds (reconciles) and reuses the SAME session bead — exactly
// one entry, neither drained nor replaced — with a stable template identity
// across the refresh.
func TestBuildDesiredState_LiveControlDispatcherRetainedOnZeroDemandTick(t *testing.T) {
	cityPath := t.TempDir()
	cfg := residentControlDispatcherCfg()
	store := beads.NewMemStore()
	_, sn := createControlDispatcherSessionBead(t, store, "active")
	requireLiveControlDispatcherFixture(t, cfg, store, sn)

	for i := 0; i < 2; i++ {
		snap, err := loadSessionBeadSnapshot(store)
		if err != nil {
			t.Fatalf("iter %d: load snapshot: %v", i, err)
		}
		res := buildDesiredStateWithSessionBeads(
			"gc", cityPath, time.Now().UTC(), cfg, runtime.NewFake(),
			store, nil, snap, nil, io.Discard,
		)
		tp, ok := res.State[sn]
		if !ok {
			t.Fatalf("iter %d: live control-dispatcher %q dropped from desired state on a zero-demand tick "+
				"(orphan-drain/respawn treadmill, gc-0ychy); the live bead must stay desired. State=%v ScaleCheckCounts=%v",
				i, sn, mapKeys(res.State), res.ScaleCheckCounts)
		}
		if tp.TemplateName != residentControlDispatcherTemplate {
			t.Fatalf("iter %d: desired TemplateName = %q, want %q (exact template identity must survive session-bead refresh)",
				i, tp.TemplateName, residentControlDispatcherTemplate)
		}
		// Reuse of the existing bead, not a phantom spawned alongside it.
		if got := countDesiredDispatcherSessions(res); got != 1 {
			t.Fatalf("iter %d: %d dispatcher sessions desired, want exactly 1 (the reused live bead, no phantom); State=%v",
				i, got, mapKeys(res.State))
		}
	}
}

// TestReconcileSessionBeads_LiveControlDispatcherNotOrphanDrainedOnZeroDemand is
// the end-to-end proof: the desired state built for a live dispatcher on an
// empty control queue must carry the session, so the reconciler does not
// orphan-drain the healthy serve loop.
func TestReconcileSessionBeads_LiveControlDispatcherNotOrphanDrainedOnZeroDemand(t *testing.T) {
	cityPath := t.TempDir()
	cfg := residentControlDispatcherCfg()
	env := newReconcilerTestEnv()
	env.cfg = cfg
	id, sn := createControlDispatcherSessionBead(t, env.store, "active")
	requireLiveControlDispatcherFixture(t, cfg, env.store, sn)
	if err := env.sp.Start(context.Background(), sn, runtime.Config{}); err != nil {
		t.Fatalf("start dispatcher runtime: %v", err)
	}

	snap, err := loadSessionBeadSnapshot(env.store)
	if err != nil {
		t.Fatalf("load snapshot: %v", err)
	}
	res := buildDesiredStateWithSessionBeads(
		"gc", cityPath, time.Now().UTC(), cfg, env.sp,
		env.store, nil, snap, nil, io.Discard,
	)
	env.desiredState = res.State

	sessionBead, err := env.store.Get(id)
	if err != nil {
		t.Fatalf("get session bead: %v", err)
	}
	env.reconcile([]beads.Bead{sessionBead})

	if ds := env.dt.get(id); ds != nil {
		t.Fatalf("healthy live control-dispatcher orphan-drained (reason=%q) on a zero-demand tick — "+
			"an empty control queue must not drain the resident serve loop (gc-0ychy)", ds.reason)
	}
	if got := env.stdout.String(); strings.Contains(got, "Draining session '"+sn+"': orphaned") {
		t.Fatalf("orphan-drain log emitted for a healthy live control-dispatcher:\n%s", got)
	}
}

// TestBuildDesiredState_AsleepControlDispatcherNotRetainedOnZeroDemand guards
// the scale-to-zero path: an idle/asleep singleton dispatcher is not a live
// serve loop, so nothing must be desired for the template — neither the asleep
// bead retained nor a phantom spawned by the floor.
func TestBuildDesiredState_AsleepControlDispatcherNotRetainedOnZeroDemand(t *testing.T) {
	assertControlDispatcherStateNotFloored(t, "asleep")
}

// TestBuildDesiredState_DrainedControlDispatcherNotRetained guards the cleanup
// path: a drained singleton dispatcher must not be resurrected by the floor.
func TestBuildDesiredState_DrainedControlDispatcherNotRetained(t *testing.T) {
	assertControlDispatcherStateNotFloored(t, "drained")
}

// TestBuildDesiredState_CreatingControlDispatcherNotFloored pins the
// running-state gate for a mid-create dispatcher: a "creating" bead is not a
// running serve loop, so the resident floor must not fire (that would fight the
// separate pending-create lease / rollback path). Catches a regression to the
// broader poolSessionIsLiveInfo, which returns true for "creating".
func TestBuildDesiredState_CreatingControlDispatcherNotFloored(t *testing.T) {
	assertControlDispatcherStateNotFloored(t, "creating")
}

// TestBuildDesiredState_DrainingControlDispatcherNotFloored pins the
// running-state gate for a mid-drain dispatcher: a "draining" bead is being shut
// down and is not reuse-eligible, so flooring its template would spawn a SECOND
// session before the drain completes. Catches a regression to
// poolSessionIsLiveInfo, which returns true for "draining".
func TestBuildDesiredState_DrainingControlDispatcherNotFloored(t *testing.T) {
	assertControlDispatcherStateNotFloored(t, "draining")
}

// assertControlDispatcherStateNotFloored builds desired state for a lone
// singleton dispatcher session in the given non-running state on a zero-demand
// tick and asserts the resident floor did NOT fire: no floored demand and no
// desired session for the template (retained or phantom-spawned). Dropping the
// running-state gate makes every one of these fail.
func assertControlDispatcherStateNotFloored(t *testing.T, state string) {
	t.Helper()
	cityPath := t.TempDir()
	cfg := residentControlDispatcherCfg()
	store := beads.NewMemStore()
	_, sn := createControlDispatcherSessionBead(t, store, state)
	snap, err := loadSessionBeadSnapshot(store)
	if err != nil {
		t.Fatalf("load snapshot: %v", err)
	}
	res := buildDesiredStateWithSessionBeads(
		"gc", cityPath, time.Now().UTC(), cfg, runtime.NewFake(),
		store, nil, snap, nil, io.Discard,
	)
	if got := res.ScaleCheckCounts[residentControlDispatcherTemplate]; got != 0 {
		t.Fatalf("%s control-dispatcher floored demand to %d, want 0 — the resident floor must gate on a running "+
			"(active/awake) state, not %q", state, got, state)
	}
	if got := countDesiredDispatcherSessions(res); got != 0 {
		t.Fatalf("%s control-dispatcher: %d dispatcher sessions desired for template, want 0 — a non-running "+
			"singleton must not be retained or spawn a phantom via the resident floor (bead sn=%q, State=%v)",
			state, got, sn, mapKeys(res.State))
	}
}

// TestBuildDesiredState_DuplicateLiveControlDispatchersCollapseToOne guards the
// duplicate-cleanup path: with two live singleton dispatcher beads, exactly one
// stays desired (max_active_sessions=1); the floor must not retain both.
func TestBuildDesiredState_DuplicateLiveControlDispatchersCollapseToOne(t *testing.T) {
	cityPath := t.TempDir()
	cfg := residentControlDispatcherCfg()
	store := beads.NewMemStore()
	_, sn1 := createControlDispatcherSessionBead(t, store, "active")
	_, sn2 := createControlDispatcherSessionBead(t, store, "active")
	snap, err := loadSessionBeadSnapshot(store)
	if err != nil {
		t.Fatalf("load snapshot: %v", err)
	}
	res := buildDesiredStateWithSessionBeads(
		"gc", cityPath, time.Now().UTC(), cfg, runtime.NewFake(),
		store, nil, snap, nil, io.Discard,
	)
	if got := countDesiredDispatcherSessions(res); got != 1 {
		t.Fatalf("duplicate live control-dispatchers: %d desired for template, want exactly 1 "+
			"(max_active_sessions=1 must collapse duplicates; the floor must not retain both). sn1=%q sn2=%q State=%v",
			got, sn1, sn2, mapKeys(res.State))
	}
}

// TestBuildDesiredState_ControlDispatcherRemovedFromConfigNotRetained guards the
// config-removal path: a live dispatcher session whose agent is no longer
// configured has no template to floor and must drain as orphaned.
func TestBuildDesiredState_ControlDispatcherRemovedFromConfigNotRetained(t *testing.T) {
	cityPath := t.TempDir()
	store := beads.NewMemStore()
	_, sn := createControlDispatcherSessionBead(t, store, "active")
	cfg := &config.City{Workspace: config.Workspace{Name: "gc"}} // dispatcher agent removed
	snap, err := loadSessionBeadSnapshot(store)
	if err != nil {
		t.Fatalf("load snapshot: %v", err)
	}
	res := buildDesiredStateWithSessionBeads(
		"gc", cityPath, time.Now().UTC(), cfg, runtime.NewFake(),
		store, nil, snap, nil, io.Discard,
	)
	if got := countDesiredDispatcherSessions(res); got != 0 {
		t.Fatalf("control-dispatcher retained after its agent was removed from config (%d desired) — a session "+
			"with no configured agent must drain as orphaned, not be floored alive (sn=%q)", got, sn)
	}
}
