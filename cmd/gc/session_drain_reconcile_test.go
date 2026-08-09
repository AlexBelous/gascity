package main

import (
	"bytes"
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/config"
	"github.com/gastownhall/gascity/internal/events"
	"github.com/gastownhall/gascity/internal/rollout"
	"github.com/gastownhall/gascity/internal/runtime"
	sessionpkg "github.com/gastownhall/gascity/internal/session"
	"k8s.io/client-go/util/workqueue"
)

// exactDrainAdvanceTestSessionName is the one fixture session every D-DRAIN
// handler test drains.
const exactDrainAdvanceTestSessionName = "worker"

func drainAdvanceAdmission(id string) sessionStartAdmission {
	return sessionStartAdmission{SessionID: id, Source: sessionStartAdmissionDrainAdvance, Version: 7}
}

// newExactDrainAdvanceParams builds the handler's params for one seeded row.
// The row is DESIRED: family precedence routes an undesired row to D-ORPHAN,
// and the D-DRAIN seam sits above it only for rows that already carry intent.
func newExactDrainAdvanceParams(env *reconcilerTestEnv, provider runtime.Provider) exactSessionStartParams {
	const name = exactDrainAdvanceTestSessionName
	statusWriter, _, statusWriterErr := beads.ResolveConditionalWriter(env.store)
	return exactSessionStartParams{
		Generation: 1, CityPath: "test-city", CityName: "test-city",
		Config: env.cfg, Provider: provider, Store: env.store,
		StatusWriter: statusWriter, StatusWriterError: statusWriterErr,
		Recorder: events.Discard, RolloutMode: rollout.Require,
		Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{},
		DrainTracker:        env.dt,
		DrainOps:            newDrainOps(provider),
		DesiredSessionNames: func() map[string]bool { return map[string]bool{name: true} },
	}
}

// seedDrainingSession seeds the fixture the whole family turns on: a live,
// desired, active session with drain intent already recorded in the shared
// in-memory tracker (Q4). The drain reason is the caller's, because the cancel
// arms partition on it.
func seedDrainingSession(t *testing.T, env *reconcilerTestEnv, reason string) (*deadRuntimeProvider, beads.Bead) {
	t.Helper()
	const name = exactDrainAdvanceTestSessionName
	env.cfg = &config.City{
		Workspace: config.Workspace{Name: "test-city"},
		Agents:    []config.Agent{{Name: name, StartCommand: "true"}},
	}
	provider := &deadRuntimeProvider{Fake: env.sp}
	if err := provider.Start(t.Context(), name, runtime.Config{}); err != nil {
		t.Fatalf("start runtime for %q: %v", name, err)
	}
	bead := env.createSessionBead(name, name)
	env.markSessionActive(&bead)
	now := env.clk.Now()
	env.dt.set(bead.ID, &drainState{
		startedAt:  now.Add(-10 * time.Second),
		deadline:   now.Add(defaultDrainTimeout),
		reason:     reason,
		generation: 1,
	})
	return provider, bead
}

func dispatchExactDrainAdvance(t *testing.T, env *reconcilerTestEnv, params exactSessionStartParams, id string) (bool, exactSessionStartOwner, error) {
	t.Helper()
	info, response, err := getAuthoritativeSessionStartPersistedRecord(env.store, id)
	if err != nil {
		t.Fatalf("authoritative read: %v", err)
	}
	return reconcileExactSessionDetectorFamily(t.Context(), drainAdvanceAdmission(id), params, info, response, env.clk)
}

// TestExactAckedDrainReachesStopPendingOnceByKey is WD.6's primary RED: an
// acknowledged drain reaches drain_ack_stop_pending exactly once by exact key,
// the in-memory intent retires with the transition, and the family then stops
// claiming the row — the stop leg belongs to the existing keyed drain-ack stop,
// which owns the atomic close committed before the stop (A5).
func TestExactAckedDrainReachesStopPendingOnceByKey(t *testing.T) {
	env := newReconcilerTestEnv()
	provider, bead := seedDrainingSession(t, env, "idle")
	params := newExactDrainAdvanceParams(env, provider)

	// Leg 1 — the acknowledgement is still outstanding, so the handler writes the
	// deferred signal and nothing else. That deferral IS the one-cycle rescue
	// window: a falsely-drained session gets a full cycle to be canceled.
	handled, owner, err := dispatchExactDrainAdvance(t, env, params, bead.ID)
	if !handled {
		t.Fatal("the D-DRAIN seam did not claim a row carrying drain intent")
	}
	if err != nil || owner != exactSessionStartKeyedOwner {
		t.Fatalf("deferred-signal leg returned owner=%v err=%v, want keyed ownership and no error", owner, err)
	}
	if acked, _ := provider.GetMeta("worker", "GC_DRAIN_ACK"); acked != "1" {
		t.Fatalf("GC_DRAIN_ACK = %q, want the deferred signal set on the first advance", acked)
	}
	if isDrainAckStopPendingInfo(readSessionInfoForTest(t, env, bead.ID)) {
		t.Fatal("the first advance marked stop-pending; the deferred signal must survive one full cycle first")
	}

	// Leg 2 — the acknowledgement is now readable, so the same key marks
	// stop-pending and retires the intent.
	handled, owner, err = dispatchExactDrainAdvance(t, env, params, bead.ID)
	if !handled || err != nil || owner != exactSessionStartKeyedOwner {
		t.Fatalf("stop-pending leg: handled=%v owner=%v err=%v", handled, owner, err)
	}
	info, _, err := getAuthoritativeSessionStartPersistedRecord(env.store, bead.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !isDrainAckStopPendingInfo(info) {
		t.Fatalf("row = %+v, want drain_ack_stop_pending after the acknowledgement was discovered", info)
	}
	if env.dt.get(bead.ID) != nil {
		t.Fatal("drain intent survived the stop-pending transition; the row is the keyed drain-ack stop's from here")
	}
	if !provider.IsRunning("worker") {
		t.Fatal("the stop-pending transition stopped the runtime; the stop leg is the drain-ack stop's, and it is async")
	}

	// Leg 3 — exactly once. The guard excludes a stop-pending row, so the family
	// releases the key rather than re-marking it.
	handled, _, err = dispatchExactDrainAdvance(t, env, params, bead.ID)
	if handled {
		t.Fatal("the D-DRAIN seam re-claimed a stop-pending row; the keyed drain-ack stop owns it")
	}
	if err != nil {
		t.Fatalf("release leg returned err=%v", err)
	}
}

// TestExactDrainAdvanceCompletesWhenTheProcessExited ports
// TestAdvanceSessionDrains_ProcessExited (session_wake_test.go) onto the exact
// key: a drain whose runtime is provably gone completes through the existing
// library's completeDrain and retires its intent.
func TestExactDrainAdvanceCompletesWhenTheProcessExited(t *testing.T) {
	env := newReconcilerTestEnv()
	provider, bead := seedDrainingSession(t, env, "idle")
	if err := provider.Stop("worker"); err != nil {
		t.Fatalf("stop runtime: %v", err)
	}
	params := newExactDrainAdvanceParams(env, provider)

	handled, owner, err := dispatchExactDrainAdvance(t, env, params, bead.ID)
	if !handled || err != nil || owner != exactSessionStartKeyedOwner {
		t.Fatalf("complete leg: handled=%v owner=%v err=%v", handled, owner, err)
	}
	if env.dt.get(bead.ID) != nil {
		t.Fatal("drain intent survived a completed drain")
	}
	stored, err := env.store.Get(bead.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Metadata["state"] != "asleep" {
		t.Fatalf("state = %q, want asleep", stored.Metadata["state"])
	}
	if stored.Metadata["sleep_reason"] != "idle" {
		t.Fatalf("sleep_reason = %q, want idle", stored.Metadata["sleep_reason"])
	}
}

// TestExactDrainAdvanceClearsAStaleGenerationWithoutStopping ports
// TestCancelSessionDrain_GenerationMismatch: a drain whose session was re-woken
// under a new generation is CLEARED, never stopped — the stale drain is about an
// incarnation that no longer exists.
func TestExactDrainAdvanceClearsAStaleGenerationWithoutStopping(t *testing.T) {
	env := newReconcilerTestEnv()
	provider, bead := seedDrainingSession(t, env, "idle")
	if err := provider.SetMeta("worker", "GC_DRAIN_ACK", "1"); err != nil {
		t.Fatalf("SetMeta(GC_DRAIN_ACK): %v", err)
	}
	env.dt.get(bead.ID).ackSet = true
	env.setSessionMetadata(&bead, map[string]string{"generation": "2"})
	params := newExactDrainAdvanceParams(env, provider)

	handled, owner, err := dispatchExactDrainAdvance(t, env, params, bead.ID)
	if !handled || err != nil || owner != exactSessionStartKeyedOwner {
		t.Fatalf("stale leg: handled=%v owner=%v err=%v", handled, owner, err)
	}
	if env.dt.get(bead.ID) != nil {
		t.Fatal("stale drain intent survived; a re-woken session's drain is cleared")
	}
	if ack, _ := provider.GetMeta("worker", "GC_DRAIN_ACK"); ack != "" {
		t.Fatalf("GC_DRAIN_ACK = %q, want cleared so the stale ack cannot kill the new incarnation", ack)
	}
	if !provider.IsRunning("worker") {
		t.Fatal("the stale-generation arm stopped the re-woken session")
	}
	stored, err := env.store.Get(bead.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Metadata["state"] == "asleep" || isDrainAckStopPendingInfo(readSessionInfoForTest(t, env, bead.ID)) {
		t.Fatalf("row = %+v, want the live row untouched by the stale clear", stored.Metadata)
	}
}

// TestExactDrainAdvanceCancelsForAssignedWork ports
// TestAdvanceSessionDrains_OrphanedDrainCanceledForAssignedWork: a drain whose
// session acquired assigned work is CANCELED rather than completed, and the
// acknowledgement metadata is cleared with it.
func TestExactDrainAdvanceCancelsForAssignedWork(t *testing.T) {
	env := newReconcilerTestEnv()
	provider, bead := seedDrainingSession(t, env, "orphaned")
	if err := provider.SetMeta("worker", "GC_DRAIN_ACK", "1"); err != nil {
		t.Fatalf("SetMeta(GC_DRAIN_ACK): %v", err)
	}
	env.dt.get(bead.ID).ackSet = true
	assignExactDrainWorkForTest(t, env, bead.ID)
	params := newExactDrainAdvanceParams(env, provider)

	handled, owner, err := dispatchExactDrainAdvance(t, env, params, bead.ID)
	if !handled || err != nil || owner != exactSessionStartKeyedOwner {
		t.Fatalf("cancel leg: handled=%v owner=%v err=%v", handled, owner, err)
	}
	if state := env.dt.get(bead.ID); state != nil {
		t.Fatalf("drain = %+v, want canceled for assigned work", state)
	}
	if ack, _ := provider.GetMeta("worker", "GC_DRAIN_ACK"); ack != "" {
		t.Fatalf("GC_DRAIN_ACK = %q, want cleared after the assigned-work cancellation", ack)
	}
	stored, err := env.store.Get(bead.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Metadata["state"] == "asleep" || isDrainAckStopPendingInfo(readSessionInfoForTest(t, env, bead.ID)) {
		t.Fatalf("row = %+v, want a canceled drain to leave the session awake", stored.Metadata)
	}
	if !provider.IsRunning("worker") {
		t.Fatal("a drain canceled for assigned work stopped the runtime")
	}
}

// TestDetectorDrainSweepIssuesNoProviderGetMeta is the third AC negative and the
// whole reason ack discovery is handler-side: a full detection pass over
// draining sessions performs ZERO provider GetMeta calls. The tracker cannot
// distinguish awaiting-ack from acked and does not need to — the handler
// decides, once, for the one key it holds.
func TestDetectorDrainSweepIssuesNoProviderGetMeta(t *testing.T) {
	env := newReconcilerTestEnv()
	env.cfg = &config.City{Workspace: config.Workspace{Name: "test-city"}}
	provider := &deadRuntimeProvider{Fake: env.sp}
	var infos []sessionpkg.Info
	for _, name := range []string{"w1", "w2", "w3"} {
		env.cfg.Agents = append(env.cfg.Agents, config.Agent{Name: name, StartCommand: "true"})
		if err := provider.Start(t.Context(), name, runtime.Config{}); err != nil {
			t.Fatalf("start runtime for %q: %v", name, err)
		}
		bead := env.createSessionBead(name, name)
		env.markSessionActive(&bead)
		if err := provider.SetMeta(name, "GC_DRAIN_ACK", "1"); err != nil {
			t.Fatalf("SetMeta(GC_DRAIN_ACK): %v", err)
		}
		env.dt.set(bead.ID, &drainState{
			startedAt:  env.clk.Now().Add(-time.Minute),
			deadline:   env.clk.Now().Add(defaultDrainTimeout),
			reason:     "idle",
			generation: 1,
		})
		info, _, err := getAuthoritativeSessionStartPersistedRecord(env.store, bead.ID)
		if err != nil {
			t.Fatal(err)
		}
		infos = append(infos, info)
	}

	admitted := map[string]sessionStartAdmissionSource{}
	in := sleepSweepInput(env, provider, infos, env.clk.Now(), func(id string, source sessionStartAdmissionSource) (sessionStartAdmissionOutcome, error) {
		admitted[id] = source
		return sessionStartAdmissionAccepted, nil
	})
	before := countProviderGetMetaCalls(env.sp.SnapshotCalls())
	result := detectSessionConditions(t.Context(), in)
	routeDetectorConditions(in, &result)
	if got := countProviderGetMetaCalls(env.sp.SnapshotCalls()) - before; got != 0 {
		t.Fatalf("detection pass issued %d provider GetMeta calls, want 0; ack discovery is handler-side", got)
	}

	if len(admitted) != len(infos) {
		t.Fatalf("routed %d draining rows, want %d", len(admitted), len(infos))
	}
	for id, source := range admitted {
		if source != sessionStartAdmissionDrainAdvance {
			t.Fatalf("row %s routed under %q, want %q", id, source, sessionStartAdmissionDrainAdvance)
		}
	}
	for _, cond := range result.Conditions {
		if cond.Family != detectorFamilyDrain {
			continue
		}
		if cond.Reason != detectorReasonDrainInFlight || cond.Outcome != TraceOutcomeDrain {
			t.Fatalf("D-DRAIN condition = %+v, want the drain-in-flight arm", cond)
		}
	}
}

func countProviderGetMetaCalls(calls []runtime.Call) int {
	count := 0
	for _, call := range calls {
		if call.Method == "GetMeta" {
			count++
		}
	}
	return count
}

func readSessionInfoForTest(t *testing.T, env *reconcilerTestEnv, id string) sessionpkg.Info {
	t.Helper()
	info, err := sessionFrontDoor(env.store).Get(id)
	if err != nil {
		t.Fatalf("read session info for %s: %v", id, err)
	}
	return info
}

// assignExactDrainWorkForTest gives the session an open, awake assigned work
// bead so the live reachable-store query the handler re-pays answers true.
func assignExactDrainWorkForTest(t *testing.T, env *reconcilerTestEnv, sessionID string) {
	t.Helper()
	if _, err := env.store.Create(beads.Bead{
		Title:    "assigned work",
		Status:   "in_progress",
		Assignee: sessionID,
	}); err != nil {
		t.Fatalf("create assigned work: %v", err)
	}
}

// TestSessionStartControllerReleasesAPermanentlyRefusedDrainAckAtTheDrainDeadline
// is RULING 1b's RED. The drain-ack re-queue is unbounded by design — a drain-ack
// is a durable obligation — but while an admission is parked the keyed
// controller EXCLUDES legacy from the row, so a permanently-refused
// authorization blocks the drain from finishing under any owner. The bound is the
// drain's own ack-or-timeout deadline, not a retry count: on expiry the
// admission is deleted, the retained lease dropped, an audit armed, and the row
// released so level-triggered re-detection re-owns it.
func TestSessionStartControllerReleasesAPermanentlyRefusedDrainAckAtTheDrainDeadline(t *testing.T) {
	now := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	var mu sync.Mutex
	clockNow := now
	attempts := 0
	fencedWhileParked := true
	const attemptsBeforeDeadline = 5
	released := make(chan sessionStartReconcileResult, 1)
	var controller *sessionStartController
	controller, err := newSessionStartController(sessionStartControllerOptions{
		Workers: 1, MaxDistinct: 4, MaxRetries: 2,
		Reconcile: func(context.Context, sessionStartAdmission) error {
			mu.Lock()
			attempts++
			seen := attempts
			mu.Unlock()
			// The whole point of the retained re-queue is that it outlives
			// maxRetries; while it does, the fence must hold.
			if seen == attemptsBeforeDeadline-1 && !controller.ownsPoolDrainAckStop("gc-drain-1", "tok-1") {
				fencedWhileParked = false
			}
			if seen >= attemptsBeforeDeadline {
				mu.Lock()
				clockNow = now.Add(drainAckAdmissionBudget + time.Second)
				mu.Unlock()
			}
			// The bare-"city" storeref refusal shape: authorization permanently
			// answers (false, nil), which is indistinguishable from transient.
			return errSessionStartPoolDrainAckPending
		},
		Observer: func(result sessionStartReconcileResult) {
			if result.Outcome != sessionStartReconcileDeadlineExceeded {
				return
			}
			select {
			case released <- result:
			default:
			}
		},
		RateLimiter: workqueue.NewTypedItemExponentialFailureRateLimiter[string](0, 0),
		Now: func() time.Time {
			mu.Lock()
			defer mu.Unlock()
			return clockNow
		},
		Stderr: &bytes.Buffer{},
	})
	if err != nil {
		t.Fatalf("new controller: %v", err)
	}
	if err := controller.Start(t.Context()); err != nil {
		t.Fatalf("start controller: %v", err)
	}
	defer controller.Stop()

	lease := routedWorkPoolDrainAckLease{
		SessionID: "gc-drain-1", InstanceToken: "tok-1",
		RequesterSessionID: "gc-drain-1", RequesterInstanceToken: "tok-1",
		ControllerGeneration: 1, PoolTarget: "worker", WorkID: "gc-work-1",
		SourceStore: "city:test", MembershipRevision: 1,
	}
	if _, err := controller.AdmitPoolDrainAck(lease); err != nil {
		t.Fatalf("admit drain ack: %v", err)
	}

	var result sessionStartReconcileResult
	select {
	case result = <-released:
	case <-time.After(30 * time.Second):
		mu.Lock()
		seen := attempts
		mu.Unlock()
		t.Fatalf("a permanently refused drain-ack never released its admission after %d attempts; the obligation is unbounded and legacy stays fenced out of the row", seen)
	}
	if !fencedWhileParked {
		t.Fatal("the controller released the drain-ack fence before the drain's own deadline")
	}
	mu.Lock()
	seen := attempts
	mu.Unlock()
	if seen <= 2 {
		t.Fatalf("reconcile ran %d times, want more than maxRetries: a drain-ack obligation is bounded by the drain deadline, not by a retry count", seen)
	}
	if result.Admission.PoolDrainAck == nil {
		t.Fatalf("released result = %+v, want the drain-ack lease carried on the released admission", result)
	}
	if result.DrainAckRefusals < 1 {
		t.Fatalf("released result carried %d consecutive refusals, want the count the diagnostic throttles on", result.DrainAckRefusals)
	}
	if controller.ownsPoolDrainAckStop("gc-drain-1", "tok-1") {
		t.Fatal("the drain-ack fence survived the deadline; legacy stays excluded from a row nobody is finishing")
	}
	if controller.holdsAnyAdmission("gc-drain-1") {
		t.Fatal("the admission survived its deadline release")
	}
	if !controller.TakeAuditRequest() {
		t.Fatal("the deadline release did not arm an authoritative audit")
	}
}

// TestSessionStartControllerAppliesAnAuthorizedDrainAckWithinTheDeadlineOnce is
// the paired positive: an acknowledgement that IS authorized before the deadline
// still applies exactly once and releases its admission normally.
func TestSessionStartControllerAppliesAnAuthorizedDrainAckWithinTheDeadlineOnce(t *testing.T) {
	now := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	var mu sync.Mutex
	applied := 0
	succeeded := make(chan sessionStartReconcileResult, 1)
	controller, err := newSessionStartController(sessionStartControllerOptions{
		Workers: 1, MaxDistinct: 4, MaxRetries: 2,
		Reconcile: func(context.Context, sessionStartAdmission) error {
			mu.Lock()
			applied++
			seen := applied
			mu.Unlock()
			if seen < 2 {
				return errSessionStartPoolDrainAckPending
			}
			return nil
		},
		Observer: func(result sessionStartReconcileResult) {
			if result.Outcome == sessionStartReconcileDeadlineExceeded {
				t.Errorf("an acknowledgement authorized inside the deadline was released as deadline_exceeded: %+v", result)
			}
			if result.Outcome != sessionStartReconcileSucceeded {
				return
			}
			select {
			case succeeded <- result:
			default:
			}
		},
		RateLimiter: workqueue.NewTypedItemExponentialFailureRateLimiter[string](0, 0),
		Now:         func() time.Time { return now },
		Stderr:      &bytes.Buffer{},
	})
	if err != nil {
		t.Fatalf("new controller: %v", err)
	}
	if err := controller.Start(t.Context()); err != nil {
		t.Fatalf("start controller: %v", err)
	}
	defer controller.Stop()

	lease := routedWorkPoolDrainAckLease{
		SessionID: "gc-drain-2", InstanceToken: "tok-2",
		RequesterSessionID: "gc-drain-2", RequesterInstanceToken: "tok-2",
		ControllerGeneration: 1, PoolTarget: "worker", WorkID: "gc-work-2",
		SourceStore: "city:test", MembershipRevision: 1,
	}
	if _, err := controller.AdmitPoolDrainAck(lease); err != nil {
		t.Fatalf("admit drain ack: %v", err)
	}
	select {
	case <-succeeded:
	case <-time.After(30 * time.Second):
		t.Fatal("an acknowledgement authorized inside the deadline never applied")
	}
	mu.Lock()
	seen := applied
	mu.Unlock()
	if seen != 2 {
		t.Fatalf("reconcile ran %d times, want exactly 2 (one refusal, one authorized apply)", seen)
	}
	if controller.holdsAnyAdmission("gc-drain-2") {
		t.Fatal("an authorized drain-ack left its admission behind")
	}
}

// TestExactDrainAdvanceRefusesWhenLivenessIsIncomplete pins the one place the
// keyed arm is deliberately STRICTER than the fleet scan: the fleet loop treats
// an unreadable running-probe as "exited" and completes the drain, which writes
// asleep onto a row whose agent may still be working. The keyed arm refuses with
// zero effect and re-detects.
func TestExactDrainAdvanceRefusesWhenLivenessIsIncomplete(t *testing.T) {
	env := newReconcilerTestEnv()
	provider, bead := seedDrainingSession(t, env, "idle")
	provider.incomplete = true
	params := newExactDrainAdvanceParams(env, provider)

	handled, _, err := dispatchExactDrainAdvance(t, env, params, bead.ID)
	if !handled {
		t.Fatal("the D-DRAIN seam did not claim a row carrying drain intent")
	}
	if err == nil || !strings.Contains(err.Error(), "liveness observation is incomplete") {
		t.Fatalf("err = %v, want an incomplete-liveness refusal", err)
	}
	if env.dt.get(bead.ID) == nil {
		t.Fatal("the refusal retired the drain intent; it must be level-triggered")
	}
	stored, err := env.store.Get(bead.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Metadata["state"] == "asleep" {
		t.Fatal("an unproven absence completed the drain")
	}
}
