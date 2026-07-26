package main

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"k8s.io/client-go/util/workqueue"
)

func TestSessionStartControllerCoalescesQueuedHintsAndReplaysInFlightUpdates(t *testing.T) {
	t.Parallel()

	blockerStarted := make(chan struct{})
	releaseBlocker := make(chan struct{})
	firstTargetStarted := make(chan struct{})
	releaseFirstTarget := make(chan struct{})
	reconciled := make(chan sessionStartAdmission, 8)
	var targetAttempts atomic.Int32

	controller := mustStartSessionStartController(t, sessionStartControllerOptions{
		Workers:     1,
		MaxDistinct: 8,
		MaxRetries:  2,
		Reconcile: func(_ context.Context, admission sessionStartAdmission) error {
			switch admission.SessionID {
			case "gcs-blocker1":
				close(blockerStarted)
				<-releaseBlocker
			case "gcs-target1":
				if targetAttempts.Add(1) == 1 {
					close(firstTargetStarted)
					<-releaseFirstTarget
				}
			}
			reconciled <- admission
			return nil
		},
	})

	if outcome, err := controller.Admit("gcs-blocker1", sessionStartAdmissionExplicitWake); err != nil || outcome != sessionStartAdmissionAccepted {
		t.Fatalf("admit blocker = %q, %v", outcome, err)
	}
	awaitClose(t, blockerStarted, "blocker reconcile start")

	if outcome, err := controller.Admit("gcs-target1", sessionStartAdmissionAntiEntropy); err != nil || outcome != sessionStartAdmissionAccepted {
		t.Fatalf("admit target = %q, %v", outcome, err)
	}
	if outcome, err := controller.Admit("gcs-target1", sessionStartAdmissionExplicitWake); err != nil || outcome != sessionStartAdmissionCoalesced {
		t.Fatalf("coalesce target = %q, %v", outcome, err)
	}
	close(releaseBlocker)
	if got := receiveSessionStartAdmission(t, reconciled); got.SessionID != "gcs-blocker1" {
		t.Fatalf("first reconciliation = %+v, want blocker", got)
	}
	awaitClose(t, firstTargetStarted, "first target reconcile start")

	if outcome, err := controller.Admit("gcs-target1", sessionStartAdmissionInProcess); err != nil || outcome != sessionStartAdmissionCoalesced {
		t.Fatalf("admit in-flight update = %q, %v", outcome, err)
	}
	close(releaseFirstTarget)

	first := receiveSessionStartAdmission(t, reconciled)
	second := receiveSessionStartAdmission(t, reconciled)
	if first.SessionID != "gcs-target1" || first.Source != sessionStartAdmissionExplicitWake {
		t.Fatalf("first target reconciliation = %+v, want latest queued explicit wake", first)
	}
	if second.SessionID != "gcs-target1" || second.Source != sessionStartAdmissionInProcess {
		t.Fatalf("replayed target reconciliation = %+v, want in-process update", second)
	}
	if second.Version <= first.Version {
		t.Fatalf("replayed version = %d, want newer than %d", second.Version, first.Version)
	}
}

func TestSessionStartControllerOverflowRequestsAudit(t *testing.T) {
	t.Parallel()

	started := make(chan struct{})
	release := make(chan struct{})
	controller := mustStartSessionStartController(t, sessionStartControllerOptions{
		Workers:     1,
		MaxDistinct: 1,
		MaxRetries:  1,
		Reconcile: func(_ context.Context, admission sessionStartAdmission) error {
			if admission.SessionID == "gcs-first1" {
				close(started)
				<-release
			}
			return nil
		},
	})

	if outcome, err := controller.Admit("gcs-first1", sessionStartAdmissionExplicitWake); err != nil || outcome != sessionStartAdmissionAccepted {
		t.Fatalf("admit first = %q, %v", outcome, err)
	}
	awaitClose(t, started, "first reconcile start")
	if outcome, err := controller.Admit("gcs-overflow1", sessionStartAdmissionExplicitWake); err != nil || outcome != sessionStartAdmissionOverflow {
		t.Fatalf("overflow admission = %q, %v", outcome, err)
	}
	if !controller.TakeAuditRequest() {
		t.Fatal("overflow did not request authoritative audit")
	}
	if controller.TakeAuditRequest() {
		t.Fatal("TakeAuditRequest did not clear the level-triggered request")
	}
	close(release)
}

func TestSessionStartControllerRetriesThenYieldsToAudit(t *testing.T) {
	t.Parallel()

	results := make(chan sessionStartReconcileResult, 8)
	controller := mustStartSessionStartController(t, sessionStartControllerOptions{
		Workers:     1,
		MaxDistinct: 4,
		MaxRetries:  1,
		RateLimiter: workqueue.NewTypedItemExponentialFailureRateLimiter[string](0, 0),
		Reconcile: func(context.Context, sessionStartAdmission) error {
			return errors.New("store unavailable")
		},
		Observer: func(result sessionStartReconcileResult) {
			results <- result
		},
	})

	if _, err := controller.Admit("gcs-retry1", sessionStartAdmissionExplicitWake); err != nil {
		t.Fatalf("Admit: %v", err)
	}
	first := receiveSessionStartResult(t, results)
	second := receiveSessionStartResult(t, results)
	if first.Outcome != sessionStartReconcileRetrying {
		t.Fatalf("first outcome = %q, want retrying", first.Outcome)
	}
	if second.Outcome != sessionStartReconcileExhausted {
		t.Fatalf("second outcome = %q, want exhausted", second.Outcome)
	}
	if first.Err == nil || second.Err == nil {
		t.Fatalf("retry results lost errors: first=%v second=%v", first.Err, second.Err)
	}
	if !controller.TakeAuditRequest() {
		t.Fatal("retry exhaustion did not yield to anti-entropy")
	}
	if got := controller.Pending(); got != 0 {
		t.Fatalf("pending admissions = %d, want 0 after exhaustion", got)
	}
}

func TestSessionStartControllerBoundsAndValidatesAdmissionKeys(t *testing.T) {
	t.Parallel()

	controller := mustStartSessionStartController(t, sessionStartControllerOptions{
		Workers:     1,
		MaxDistinct: 2,
		MaxRetries:  1,
		Reconcile:   func(context.Context, sessionStartAdmission) error { return nil },
	})
	for _, id := range []string{"", " gcs-space1", "gcs-space1 ", "gcs/escape1", "gcs-\nline1"} {
		if _, err := controller.Admit(id, sessionStartAdmissionSocket); err == nil {
			t.Errorf("Admit(%q) error = nil", id)
		}
	}
	oversized := "gcs-" + string(make([]byte, sessionStartAdmissionMaxIDBytes))
	if _, err := controller.Admit(oversized, sessionStartAdmissionSocket); err == nil {
		t.Fatal("oversized session ID accepted")
	}
}

func TestSessionStartControllerStopDoesNotStartQueuedReconciliations(t *testing.T) {
	t.Parallel()

	firstStarted := make(chan struct{})
	reconciled := make(chan string, 2)
	controller := mustStartSessionStartController(t, sessionStartControllerOptions{
		Workers:     1,
		MaxDistinct: 2,
		MaxRetries:  1,
		Reconcile: func(ctx context.Context, admission sessionStartAdmission) error {
			reconciled <- admission.SessionID
			if admission.SessionID == "gcs-running1" {
				close(firstStarted)
				<-ctx.Done()
				return ctx.Err()
			}
			return nil
		},
	})
	if _, err := controller.Admit("gcs-running1", sessionStartAdmissionExplicitWake); err != nil {
		t.Fatalf("admit running key: %v", err)
	}
	awaitClose(t, firstStarted, "running reconciliation")
	if _, err := controller.Admit("gcs-queued1", sessionStartAdmissionExplicitWake); err != nil {
		t.Fatalf("admit queued key: %v", err)
	}

	controller.Stop()

	if got := <-reconciled; got != "gcs-running1" {
		t.Fatalf("first reconciliation = %q, want gcs-running1", got)
	}
	select {
	case got := <-reconciled:
		t.Fatalf("Stop started queued reconciliation %q", got)
	default:
	}
}

func TestSessionStartControllerStopClosesAdmissionAndJoinsWorkers(t *testing.T) {
	t.Parallel()

	started := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseReconcile := func() {
		releaseOnce.Do(func() {
			close(release)
		})
	}
	defer releaseReconcile()
	controller := mustStartSessionStartController(t, sessionStartControllerOptions{
		Workers:     1,
		MaxDistinct: 2,
		MaxRetries:  1,
		Reconcile: func(context.Context, sessionStartAdmission) error {
			close(started)
			<-release
			return nil
		},
	})
	if _, err := controller.Admit("gcs-stop1", sessionStartAdmissionExplicitWake); err != nil {
		t.Fatalf("Admit: %v", err)
	}
	awaitClose(t, started, "reconcile start")

	stopped := make(chan struct{})
	go func() {
		controller.Stop()
		close(stopped)
	}()
	awaitClose(t, controller.ctx.Done(), "controller cancellation")
	select {
	case <-stopped:
		t.Fatal("Stop returned before the active worker joined")
	default:
	}
	if _, err := controller.Admit("gcs-after1", sessionStartAdmissionExplicitWake); err == nil {
		t.Fatal("admission remained open during Stop")
	}
	releaseReconcile()
	awaitClose(t, stopped, "controller stop")
}

func mustStartSessionStartController(t *testing.T, opts sessionStartControllerOptions) *sessionStartController {
	t.Helper()
	controller, err := newSessionStartController(opts)
	if err != nil {
		t.Fatalf("newSessionStartController: %v", err)
	}
	if err := controller.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(controller.Stop)
	return controller
}

func receiveSessionStartAdmission(t *testing.T, ch <-chan sessionStartAdmission) sessionStartAdmission {
	t.Helper()
	select {
	case admission := <-ch:
		return admission
	case <-time.After(hangBudget):
		t.Fatal("timed out waiting for session-start reconciliation")
		return sessionStartAdmission{}
	}
}

func receiveSessionStartResult(t *testing.T, ch <-chan sessionStartReconcileResult) sessionStartReconcileResult {
	t.Helper()
	select {
	case result := <-ch:
		return result
	case <-time.After(hangBudget):
		t.Fatal("timed out waiting for session-start result")
		return sessionStartReconcileResult{}
	}
}
