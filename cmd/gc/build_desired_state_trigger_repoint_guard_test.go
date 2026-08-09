package main

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/beadmeta"
	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/config"
	"github.com/gastownhall/gascity/internal/runtime"
	sessionpkg "github.com/gastownhall/gascity/internal/session"
)

// triggerRepointGuardEnv is one active pool member bound to "wb-A", plus the
// build params that would re-point it.
type triggerRepointGuardEnv struct {
	bp     *agentBuildParams
	info   sessionpkg.Info
	store  beads.Store
	stderr *bytes.Buffer
}

func newTriggerRepointGuardEnv(t *testing.T) triggerRepointGuardEnv {
	t.Helper()
	mem := beads.NewMemStore()
	created, err := mem.Create(beads.Bead{
		Title:  "worker-1",
		Type:   sessionBeadType,
		Status: "open",
		Labels: []string{sessionBeadLabel},
		Metadata: map[string]string{
			"session_name":                          "s-worker",
			"template":                              "city/worker",
			"state":                                 string(sessionpkg.StateActive),
			beadmeta.TriggerBeadIDMetadataKey:       "wb-A",
			beadmeta.TriggerBeadStoreRefMetadataKey: "city:city",
		},
	})
	if err != nil {
		t.Fatalf("create session bead: %v", err)
	}
	info, err := sessionFrontDoor(mem).Get(created.ID)
	if err != nil {
		t.Fatalf("read session info: %v", err)
	}
	cfg := &config.City{Workspace: config.Workspace{Name: "city"}}
	stderr := &bytes.Buffer{}
	bp := newAgentBuildParams("city", t.TempDir(), cfg, runtime.NewFake(), time.Now().UTC(), mem, stderr)
	return triggerRepointGuardEnv{bp: bp, info: info, store: mem, stderr: stderr}
}

func (e triggerRepointGuardEnv) repoint(t *testing.T) sessionpkg.Info {
	t.Helper()
	bound, err := bindPoolSessionTriggerBead(e.bp, &config.Agent{Name: "worker"}, "city/worker", e.info, SessionRequest{
		WorkBeadID:   "wb-B",
		WorkStoreRef: "city:city",
	})
	if err != nil {
		t.Fatalf("bind pool session trigger bead: %v", err)
	}
	return bound
}

func (e triggerRepointGuardEnv) durableTrigger(t *testing.T) string {
	t.Helper()
	current, err := sessionFrontDoor(e.store).Get(e.info.ID)
	if err != nil {
		t.Fatalf("read current session info: %v", err)
	}
	return strings.TrimSpace(current.TriggerBeadID)
}

// TestPoolTriggerRepointSkipsAcknowledgedMember is the ga-f7v2ft.131 Commit 2
// red. The candidate is decided from a per-tick snapshot in which the member is
// still active; between that snapshot and the write it acknowledges its drain
// and the controller stamps drain-ack-stop-pending. Committing the re-point
// then re-targets a member that is already retiring, and its trigger binding is
// load-bearing on the keyed stop path — the acknowledged drain never finalizes.
func TestPoolTriggerRepointSkipsAcknowledgedMember(t *testing.T) {
	env := newTriggerRepointGuardEnv(t)

	// The keyed stop-pending transition lands after the snapshot the candidate
	// was decided on.
	if err := sessionFrontDoor(env.store).ApplyPatch(env.info.ID,
		sessionpkg.AgentDrainAckStopPendingPatch(time.Now().UTC(), env.info.ID, "token-1")); err != nil {
		t.Fatalf("mark drain-ack stop pending: %v", err)
	}

	bound := env.repoint(t)

	if got := env.durableTrigger(t); got != "wb-A" {
		t.Fatalf("durable trigger = %q, want the acknowledged %q: legacy re-pointed a member that is already retiring (ga-f7v2ft.131)", got, "wb-A")
	}
	if strings.TrimSpace(bound.TriggerBeadID) != "wb-A" {
		t.Fatalf("returned Info trigger = %q, want the unchanged %q", bound.TriggerBeadID, "wb-A")
	}
	if !strings.Contains(env.stderr.String(), "superseded") {
		t.Fatalf("stderr = %q, want a traced supersede for the skipped re-point", env.stderr.String())
	}
}

// TestPoolTriggerRepointStillRetargetsFreeMember is the negative that keeps the
// guard narrow: re-targeting a freed member onto the next ready work item is the
// intended system response (ga-f7v2ft.112 round-4 ruling 3), and only a member
// that already acknowledged its drain is exempt.
func TestPoolTriggerRepointStillRetargetsFreeMember(t *testing.T) {
	env := newTriggerRepointGuardEnv(t)

	bound := env.repoint(t)

	if got := env.durableTrigger(t); got != "wb-B" {
		t.Fatalf("durable trigger = %q, want the re-pointed %q: a free member must still re-point", got, "wb-B")
	}
	if strings.TrimSpace(bound.TriggerBeadID) != "wb-B" {
		t.Fatalf("returned Info trigger = %q, want %q", bound.TriggerBeadID, "wb-B")
	}
	if strings.Contains(env.stderr.String(), "superseded") {
		t.Fatalf("stderr = %q, want no supersede for an ordinary re-target", env.stderr.String())
	}
}

// TestPoolTriggerClearIsNotGatedByDraining pins the arm boundary: the guard is
// on the REASSIGN arm. Dropping a retiring member's trigger cluster is a
// release, not a re-target, and must still commit.
func TestPoolTriggerClearIsNotGatedByDraining(t *testing.T) {
	env := newTriggerRepointGuardEnv(t)
	if err := sessionFrontDoor(env.store).ApplyPatch(env.info.ID,
		sessionpkg.AgentDrainAckStopPendingPatch(time.Now().UTC(), env.info.ID, "token-1")); err != nil {
		t.Fatalf("mark drain-ack stop pending: %v", err)
	}

	if _, err := bindPoolSessionTriggerBead(env.bp, &config.Agent{Name: "worker"}, "city/worker", env.info, SessionRequest{WorkBeadID: ""}); err != nil {
		t.Fatalf("clear pool session trigger bead: %v", err)
	}

	if got := env.durableTrigger(t); got != "" {
		t.Fatalf("durable trigger = %q, want the cluster cleared", got)
	}
}
