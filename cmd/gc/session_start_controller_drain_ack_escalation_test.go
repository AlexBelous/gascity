package main

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/rollout"
	"k8s.io/client-go/util/workqueue"
)

// The drain-ack refusal bound (ga-f7v2ft.173). The 5-minute deadline release
// arms an audit that re-detects the same durable stop-pending row, so a lease
// nobody can resolve used to loop retry → release → re-detect → retry forever
// at reconcile cadence (~108 lines/min on the flipped fleet). The bound has two
// halves: the consecutive-refusal count survives the release/re-seed macro
// cycle, and crossing the escalation threshold moves the retained obligation
// onto a slow re-examination cadence under a NAMED outcome.

func drainAckEscalationTestController(
	t *testing.T,
	reconcile func(context.Context, sessionStartAdmission) error,
	observer func(sessionStartReconcileResult),
	now func() time.Time,
) *sessionStartController {
	t.Helper()
	controller, err := newSessionStartController(sessionStartControllerOptions{
		Workers: 1, MaxDistinct: 4, MaxRetries: 2,
		Reconcile:   reconcile,
		Observer:    observer,
		RateLimiter: workqueue.NewTypedItemExponentialFailureRateLimiter[string](0, 0),
		Now:         now,
		Stderr:      &bytes.Buffer{},
	})
	if err != nil {
		t.Fatalf("new controller: %v", err)
	}
	if err := controller.Start(t.Context()); err != nil {
		t.Fatalf("start controller: %v", err)
	}
	t.Cleanup(controller.Stop)
	return controller
}

func testDrainAckEscalationLease() routedWorkPoolDrainAckLease {
	return routedWorkPoolDrainAckLease{
		SessionID: "gc-drain-esc", InstanceToken: "tok-esc",
		RequesterSessionID: "gc-drain-esc", RequesterInstanceToken: "tok-esc",
		ControllerGeneration: 1, PoolTarget: "worker", WorkID: "gc-work-esc",
		SourceStore: "city:test", MembershipRevision: 1,
	}
}

func awaitDrainAckResult(
	t *testing.T,
	results <-chan sessionStartReconcileResult,
	accept func(sessionStartReconcileResult) bool,
	what string,
) sessionStartReconcileResult {
	t.Helper()
	deadline := time.After(30 * time.Second)
	for {
		select {
		case result := <-results:
			if accept(result) {
				return result
			}
		case <-deadline:
			t.Fatalf("timed out waiting for %s", what)
		}
	}
}

// TestSessionStartControllerDrainAckRefusalsSurviveDeadlineRelease pins the
// macro-cycle memory: the deadline release deletes the admission and arms an
// audit, but the refusal count is the OBLIGATION's, not the admission's — a
// re-detected lease continues the count instead of restarting the treadmill
// from one.
func TestSessionStartControllerDrainAckRefusalsSurviveDeadlineRelease(t *testing.T) {
	start := time.Date(2026, 8, 21, 3, 0, 0, 0, time.UTC)
	var mu sync.Mutex
	clockNow := start
	attempts := 0
	results := make(chan sessionStartReconcileResult, 256)
	controller := drainAckEscalationTestController(t,
		func(context.Context, sessionStartAdmission) error {
			// Expire the drain deadline on the third refusal, well inside the
			// escalation threshold, so the release fires while the obligation
			// is still on the hot cadence.
			mu.Lock()
			attempts++
			if attempts >= 3 {
				clockNow = start.Add(drainAckAdmissionBudget + time.Second)
			}
			mu.Unlock()
			return errSessionStartPoolDrainAckPending
		},
		func(result sessionStartReconcileResult) {
			select {
			case results <- result:
			default:
			}
		},
		func() time.Time {
			mu.Lock()
			defer mu.Unlock()
			return clockNow
		},
	)

	if _, err := controller.AdmitPoolDrainAck(testDrainAckEscalationLease()); err != nil {
		t.Fatalf("admit drain ack: %v", err)
	}
	released := awaitDrainAckResult(t, results, func(result sessionStartReconcileResult) bool {
		return result.Outcome == sessionStartReconcileDeadlineExceeded
	}, "the deadline release")
	if released.DrainAckRefusals < 3 {
		t.Fatalf("released refusal count = %d, want the accumulated count", released.DrainAckRefusals)
	}

	// The audit's re-detection: a fresh admission for the SAME obligation.
	if _, err := controller.AdmitPoolDrainAck(testDrainAckEscalationLease()); err != nil {
		t.Fatalf("re-admit drain ack: %v", err)
	}
	next := awaitDrainAckResult(t, results, func(result sessionStartReconcileResult) bool {
		return result.Outcome == sessionStartReconcileRetrying || result.Outcome == sessionStartReconcileDrainAckEscalated
	}, "the first refusal after re-detection")
	if next.DrainAckRefusals <= released.DrainAckRefusals {
		t.Fatalf("refusals after release = %d, want the count to continue past %d: the release must not reset the obligation's history", next.DrainAckRefusals, released.DrainAckRefusals)
	}
}

// TestSessionStartControllerDrainAckRefusalsResetForNewObligation is the
// other half of the obligation scoping (ga-f7v2ft.191, field-proven on the
// mc-enterprise6h soak): the count is the OBLIGATION's, so a release or
// re-seed that genuinely starts a NEW obligation — a fresh drain of a fresh
// incarnation under the same pool identity — starts a fresh streak at one. A
// session-keyed count that survives across incarnations turns the display
// into a monotonic climb into the thousands (06:20=24 … 08:20=389 on the
// soak) and makes every new drain of that identity start life escalated,
// skipping the hot retry cadence it is entitled to.
func TestSessionStartControllerDrainAckRefusalsResetForNewObligation(t *testing.T) {
	start := time.Date(2026, 8, 23, 6, 0, 0, 0, time.UTC)
	var mu sync.Mutex
	clockNow := start
	attempts := 0
	results := make(chan sessionStartReconcileResult, 256)
	controller := drainAckEscalationTestController(t,
		func(context.Context, sessionStartAdmission) error {
			mu.Lock()
			attempts++
			if attempts >= 3 {
				clockNow = start.Add(drainAckAdmissionBudget + time.Second)
			}
			mu.Unlock()
			return errSessionStartPoolDrainAckPending
		},
		func(result sessionStartReconcileResult) {
			select {
			case results <- result:
			default:
			}
		},
		func() time.Time {
			mu.Lock()
			defer mu.Unlock()
			return clockNow
		},
	)

	if _, err := controller.AdmitPoolDrainAck(testDrainAckEscalationLease()); err != nil {
		t.Fatalf("admit drain ack: %v", err)
	}
	released := awaitDrainAckResult(t, results, func(result sessionStartReconcileResult) bool {
		return result.Outcome == sessionStartReconcileDeadlineExceeded
	}, "the deadline release")
	if released.DrainAckRefusals < 3 {
		t.Fatalf("released refusal count = %d, want the accumulated count", released.DrainAckRefusals)
	}

	// The pool identity re-drains as a NEW incarnation: a different instance
	// token is a different obligation, and its streak starts at one.
	mu.Lock()
	clockNow = start.Add(drainAckAdmissionBudget + 2*time.Second)
	mu.Unlock()
	next := testDrainAckEscalationLease()
	next.InstanceToken = "tok-esc-next-incarnation"
	next.RequesterInstanceToken = next.InstanceToken
	if _, err := controller.AdmitPoolDrainAck(next); err != nil {
		t.Fatalf("admit next incarnation's drain ack: %v", err)
	}
	fresh := awaitDrainAckResult(t, results, func(result sessionStartReconcileResult) bool {
		return result.Outcome == sessionStartReconcileRetrying ||
			result.Outcome == sessionStartReconcileDrainAckEscalated ||
			result.Outcome == sessionStartReconcileDeadlineExceeded
	}, "the new obligation's first refusal")
	if fresh.DrainAckRefusals != 1 {
		t.Fatalf("new obligation's first refusal count = %d, want 1: the previous incarnation's streak must not leak into a genuinely new obligation", fresh.DrainAckRefusals)
	}
}

// TestSessionStartControllerNewObligationCrossesEscalationLoudly pins the
// crossing signal's scope: the >= transition is announced once PER OBLIGATION.
// A new obligation that inherits nothing climbs from one and crosses at the
// threshold with the crossing mark set; the same obligation's re-examinations
// after the crossing carry no mark.
func TestSessionStartControllerNewObligationCrossesEscalationLoudly(t *testing.T) {
	start := time.Date(2026, 8, 23, 7, 0, 0, 0, time.UTC)
	results := make(chan sessionStartReconcileResult, 256)
	controller := drainAckEscalationTestController(t,
		func(context.Context, sessionStartAdmission) error {
			return errSessionStartPoolDrainAckPending
		},
		func(result sessionStartReconcileResult) {
			select {
			case results <- result:
			default:
			}
		},
		func() time.Time { return start },
	)

	if _, err := controller.AdmitPoolDrainAck(testDrainAckEscalationLease()); err != nil {
		t.Fatalf("admit drain ack: %v", err)
	}
	crossed := awaitDrainAckResult(t, results, func(result sessionStartReconcileResult) bool {
		return result.Outcome == sessionStartReconcileDrainAckEscalated
	}, "the escalation crossing")
	if !crossed.DrainAckEscalationCrossing {
		t.Fatalf("the first crossing of the threshold carried no crossing mark (refusals=%d); the named line would never fire", crossed.DrainAckRefusals)
	}
	if crossed.DrainAckRefusals != drainAckRefusalEscalationThreshold {
		t.Fatalf("crossing at %d refusals, want the threshold %d", crossed.DrainAckRefusals, drainAckRefusalEscalationThreshold)
	}
}

// TestSessionStartControllerEscalatesUnresolvableDrainAckAfterThreshold is the
// bound itself: at the escalation threshold the outcome becomes the NAMED
// escalated state, the hot rate-limited retry stops (slow re-examination
// cadence), and the obligation — including its legacy-exclusion fence — is
// retained, not dropped.
func TestSessionStartControllerEscalatesUnresolvableDrainAckAfterThreshold(t *testing.T) {
	start := time.Date(2026, 8, 21, 3, 0, 0, 0, time.UTC)
	results := make(chan sessionStartReconcileResult, 256)
	controller := drainAckEscalationTestController(t,
		func(context.Context, sessionStartAdmission) error {
			return errSessionStartPoolDrainAckPending
		},
		func(result sessionStartReconcileResult) {
			select {
			case results <- result:
			default:
			}
		},
		func() time.Time { return start },
	)

	lease := testDrainAckEscalationLease()
	if _, err := controller.AdmitPoolDrainAck(lease); err != nil {
		t.Fatalf("admit drain ack: %v", err)
	}
	escalated := awaitDrainAckResult(t, results, func(result sessionStartReconcileResult) bool {
		if result.Outcome == sessionStartReconcileRetrying && result.DrainAckRefusals >= drainAckRefusalEscalationThreshold {
			t.Fatalf("refusal %d still rides the hot retry cadence, want the named escalated outcome at the threshold", result.DrainAckRefusals)
		}
		return result.Outcome == sessionStartReconcileDrainAckEscalated
	}, "the escalation crossing")
	if escalated.DrainAckRefusals != drainAckRefusalEscalationThreshold {
		t.Fatalf("escalated at %d refusals, want exactly the threshold %d", escalated.DrainAckRefusals, drainAckRefusalEscalationThreshold)
	}

	// The hot cadence must stop: with the slow re-examination interval pending,
	// no further reconcile attempt lands in a short real-time window.
	select {
	case result := <-results:
		t.Fatalf("observed %s (refusals=%d) after escalation, want the retained obligation quiet on the slow cadence", result.Outcome, result.DrainAckRefusals)
	case <-time.After(300 * time.Millisecond):
	}

	// The obligation is retained, not dropped: the fence still excludes legacy
	// while the keyed owner re-examines on evidence changes.
	if !controller.ownsPoolDrainAckStop(lease.SessionID, lease.InstanceToken) {
		t.Fatal("escalation dropped the drain-ack fence; the retained obligation must keep its ownership")
	}
	if !controller.holdsAnyAdmission(lease.SessionID) {
		t.Fatal("escalation deleted the admission; the obligation must be retained on the slow cadence")
	}
}

// TestSessionStartControllerEscalatedDrainAckResolvesWhenReconcileSucceeds is
// the ga-lp5w6 self-recovery half of the .173 bound: escalation parks the
// obligation, it must not entomb it. When a re-examination of the RETAINED
// escalated obligation finally succeeds (the liveness observation regained
// completeness and the stop finalized), the admission resolves, the ownership
// fence lifts, and the refusal history clears — the pool seat is free again
// with no manual close.
func TestSessionStartControllerEscalatedDrainAckResolvesWhenReconcileSucceeds(t *testing.T) {
	start := time.Date(2026, 8, 21, 9, 0, 0, 0, time.UTC)
	var mu sync.Mutex
	resolved := false
	results := make(chan sessionStartReconcileResult, 256)
	controller := drainAckEscalationTestController(t,
		func(context.Context, sessionStartAdmission) error {
			mu.Lock()
			defer mu.Unlock()
			if resolved {
				return nil
			}
			return errSessionStartPoolDrainAckPending
		},
		func(result sessionStartReconcileResult) {
			select {
			case results <- result:
			default:
			}
		},
		func() time.Time { return start },
	)

	lease := testDrainAckEscalationLease()
	if _, err := controller.AdmitPoolDrainAck(lease); err != nil {
		t.Fatalf("admit drain ack: %v", err)
	}
	awaitDrainAckResult(t, results, func(result sessionStartReconcileResult) bool {
		return result.Outcome == sessionStartReconcileDrainAckEscalated
	}, "the escalation crossing")

	// The observation regains completeness: from here every re-examination of
	// the retained obligation succeeds. The escalated slow cadence and the
	// audit's level-triggered re-detection run the same reconcile; drive it via
	// the re-detection so the test does not sleep out the real 5m interval.
	mu.Lock()
	resolved = true
	mu.Unlock()
	if _, err := controller.AdmitPoolDrainAck(lease); err != nil {
		t.Fatalf("re-admit escalated drain ack: %v", err)
	}
	awaitDrainAckResult(t, results, func(result sessionStartReconcileResult) bool {
		return result.Outcome == sessionStartReconcileSucceeded
	}, "the escalated obligation's resolution")

	if controller.ownsPoolDrainAckStop(lease.SessionID, lease.InstanceToken) {
		t.Fatal("resolution left the drain-ack ownership fence in place; the seat is still fenced")
	}
	if controller.holdsAnyAdmission(lease.SessionID) {
		t.Fatal("resolution retained the admission; the obligation must end when the stop finalizes")
	}
	controller.mu.Lock()
	refusals := controller.drainAckRefusalHistory[lease.SessionID].Count
	controller.mu.Unlock()
	if refusals != 0 {
		t.Fatalf("refusal history after resolution = %d, want the obligation's streak cleared", refusals)
	}
}

// TestObserveSessionStartReconcileEmitsNamedEscalationOnceAtThreshold pins the
// loud-not-silent half: the runtime's observer emits ONE named supervisor line
// on the obligation's crossing mark (ga-f7v2ft.191), and escalated
// re-examinations after it — which carry no mark — are quiet.
func TestObserveSessionStartReconcileEmitsNamedEscalationOnceAtThreshold(t *testing.T) {
	var buf bytes.Buffer
	cr := &CityRuntime{stderr: &buf}
	result := sessionStartReconcileResult{
		Admission: sessionStartAdmission{SessionID: "gc-drain-esc", PoolDrainAckUncertain: true},
		Outcome:   sessionStartReconcileDrainAckEscalated,
		Err:       errSessionStartPoolDrainAckPending,
	}

	result.DrainAckRefusals = drainAckRefusalEscalationThreshold
	result.DrainAckEscalationCrossing = true
	cr.observeSessionStartReconcile(nil, rollout.Auto, result)
	crossing := buf.String()
	if !strings.Contains(crossing, "drain-ack reconciliation escalated for gc-drain-esc") ||
		!strings.Contains(crossing, "consecutive refusals") {
		t.Fatalf("crossing line = %q, want the named escalation naming the session and the refusal count", crossing)
	}

	buf.Reset()
	result.DrainAckRefusals = drainAckRefusalEscalationThreshold + 5
	result.DrainAckEscalationCrossing = false
	cr.observeSessionStartReconcile(nil, rollout.Auto, result)
	if got := buf.String(); got != "" {
		t.Fatalf("post-crossing escalated re-examination logged %q, want silence", got)
	}

	// The pre-threshold retrying line is unchanged: still one line per refusal
	// with the cause attached.
	buf.Reset()
	retrying := sessionStartReconcileResult{
		Admission:        sessionStartAdmission{SessionID: "gc-drain-esc", PoolDrainAckUncertain: true},
		Outcome:          sessionStartReconcileRetrying,
		Err:              fmt.Errorf("%w: live runtime holds no recognizable drain acknowledgement provenance", errSessionStartPoolDrainAckPending),
		DrainAckRefusals: 2,
	}
	cr.observeSessionStartReconcile(nil, rollout.Auto, retrying)
	if got := buf.String(); !strings.Contains(got, "drain-ack reconciliation retrying for gc-drain-esc") {
		t.Fatalf("pre-threshold retrying line = %q, want the existing per-refusal diagnostic", got)
	}
}
