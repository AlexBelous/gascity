package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/gastownhall/gascity/internal/nudgeparity"
	"github.com/gastownhall/gascity/internal/nudgequeue"
	"github.com/gastownhall/gascity/internal/telemetry"
)

const (
	productionNudgeParityQueueCapacity = 256
	productionNudgeParityMaxPending    = 512
	productionNudgeParityMaxRecent     = 512
	productionNudgeParityRetention     = 5 * time.Minute
	productionNudgeParitySnapshotEvery = 30 * time.Second

	productionNudgeParityDigestDomain = "gascity.nudge-parity.ingress-request.v1"
)

var noopNudgeParityStop = func() error { return nil }

// publishNudgeParityConfigRevision publishes the exact accepted config
// revision to the concurrent admission tap. The projection is immutable and
// process-local; it is never a substitute for config persistence.
func (cr *CityRuntime) publishNudgeParityConfigRevision(revision string) {
	if cr == nil {
		return
	}
	publishedRevision := revision
	cr.nudgeParityConfigRevision.Store(&publishedRevision)
}

// prepareNudgeParityShadow constructs and publishes the bounded shadow before
// the runtime itself is exposed to the controller socket. Admissions that race
// startup therefore become explicit not_running loss instead of disappearing.
func (cr *CityRuntime) prepareNudgeParityShadow() error {
	if cr == nil || !cr.nudgeParityEnabled || cr.nudgeEffectOwnership != nudgeEffectOwnershipLegacy {
		return nil
	}
	if cr.nudgeParityShadow.Load() != nil {
		return nil
	}
	now := cr.nudgeParityNow
	if now == nil {
		now = time.Now
	}
	sink := cr.nudgeParityResultSink
	if sink == nil {
		sink = telemetry.RecordNudgeParityResult
	}
	planner := cr.nudgeParityPlanner
	if planner == nil {
		planner = validatedImmediateNudgeParityPlanner{now: now}
	}
	shadow, err := nudgeparity.NewShadow(nudgeparity.ShadowConfig{
		Comparator: nudgeparity.Config{
			Retention:  productionNudgeParityRetention,
			MaxPending: productionNudgeParityMaxPending,
			MaxRecent:  productionNudgeParityMaxRecent,
			Now:        now,
		},
		QueueCapacity: productionNudgeParityQueueCapacity,
		Planner:       planner,
		Sink:          sink,
	})
	if err != nil {
		return fmt.Errorf("preparing production nudge parity shadow: %w", err)
	}
	if !cr.nudgeParityShadow.CompareAndSwap(nil, shadow) {
		return errors.New("preparing production nudge parity shadow: concurrent publication")
	}
	return nil
}

// startNudgeParityShadowBeforeReadiness opens the observational child and
// returns a stop function that unpublishes admission, drains every accepted
// sample, records final loss counters, and only then returns to authority
// shutdown. It performs no command or provider operation.
func (cr *CityRuntime) startNudgeParityShadowBeforeReadiness(parent context.Context) (func() error, error) {
	if cr == nil || !cr.nudgeParityEnabled || cr.nudgeEffectOwnership != nudgeEffectOwnershipLegacy {
		return noopNudgeParityStop, nil
	}
	cr.nudgeParityLifecycleMu.Lock()
	defer cr.nudgeParityLifecycleMu.Unlock()
	if cr.nudgeParityClosing {
		return noopNudgeParityStop, errors.New("starting production nudge parity shadow: runtime is closing")
	}
	if cr.nudgeParityStop != nil {
		return noopNudgeParityStop, errors.New("starting production nudge parity shadow: lifecycle is already registered")
	}
	if parent == nil {
		return noopNudgeParityStop, errors.New("starting production nudge parity shadow: context is nil")
	}
	if err := parent.Err(); err != nil {
		return noopNudgeParityStop, err
	}
	if err := cr.prepareNudgeParityShadow(); err != nil {
		return noopNudgeParityStop, err
	}
	shadow := cr.nudgeParityShadow.Load()
	if shadow == nil {
		return noopNudgeParityStop, errors.New("starting production nudge parity shadow: publication is absent")
	}

	ctx, cancel := context.WithCancel(parent)
	done := make(chan error, 1)
	go func() {
		done <- shadow.Run(ctx)
	}()
	select {
	case <-shadow.Ready():
	case err := <-done:
		cancel()
		cr.nudgeParityShadow.CompareAndSwap(shadow, nil)
		return noopNudgeParityStop, fmt.Errorf("starting production nudge parity shadow: %w", err)
	case <-parent.Done():
		cancel()
		cr.nudgeParityShadow.CompareAndSwap(shadow, nil)
		<-done
		return noopNudgeParityStop, parent.Err()
	}

	snapshotRecorder := telemetry.NewNudgeParitySnapshotRecorder()
	snapshotDone := make(chan struct{})
	go func() {
		defer close(snapshotDone)
		ticker := time.NewTicker(productionNudgeParitySnapshotEvery)
		defer ticker.Stop()
		var recordErrorOnce sync.Once
		for {
			select {
			case <-ticker.C:
				if err := snapshotRecorder.Record(context.WithoutCancel(ctx), shadow.Snapshot()); err != nil {
					recordErrorOnce.Do(func() {
						if cr.stderr != nil {
							fmt.Fprintf(cr.stderr, "%s: nudge parity snapshot: %v\n", cr.logPrefix, err) //nolint:errcheck // one-shot observational diagnostic
						}
					})
				}
			case <-ctx.Done():
				return
			}
		}
	}()

	var stopOnce sync.Once
	var stopErr error
	stop := func() error {
		stopOnce.Do(func() {
			// Unpublish before closing Run admission. A producer that already
			// loaded shadow is covered by Shadow's in-flight admission drain.
			cr.nudgeParityShadow.CompareAndSwap(shadow, nil)
			cancel()
			workerErr := <-done
			<-snapshotDone
			snapshotErr := snapshotRecorder.Record(context.Background(), shadow.Snapshot())
			stopErr = errors.Join(workerErr, snapshotErr)
		})
		return stopErr
	}
	cr.nudgeParityStop = stop
	return cr.stopNudgeParityShadowBeforeAuthority, nil
}

// stopNudgeParityShadowBeforeAuthority serializes every direct supervisor and
// runtime shutdown path with child startup. It marks admission permanently
// closing, unpublishes a prepared-but-not-running child, and waits for a
// running child to drain before its caller may close durable authority.
func (cr *CityRuntime) stopNudgeParityShadowBeforeAuthority() error {
	if cr == nil {
		return nil
	}
	cr.nudgeParityLifecycleMu.Lock()
	cr.nudgeParityClosing = true
	stop := cr.nudgeParityStop
	if stop == nil {
		cr.nudgeParityShadow.Store(nil)
	}
	cr.nudgeParityLifecycleMu.Unlock()
	if stop == nil {
		return nil
	}
	err := stop()
	cr.nudgeParityLifecycleMu.Lock()
	if cr.nudgeParityStop != nil {
		cr.nudgeParityStop = nil
	}
	cr.nudgeParityLifecycleMu.Unlock()
	return err
}

// submitLegacyImmediateNudgeParity mirrors one immutable immediate request to
// the planning-only child. Every outcome is deliberately ignored by the
// legacy authority edge: full, stopped, invalid, or failed telemetry can never
// change provider fallback ownership.
func (cr *CityRuntime) submitLegacyImmediateNudgeParity(request nudgequeue.NudgeIngressRequest) {
	if cr == nil || cr.nudgeEffectOwnership != nudgeEffectOwnershipLegacy ||
		request.Mode != nudgequeue.DeliveryModeImmediate ||
		request.Target.Policy != nudgequeue.TargetPolicyExactLaunch {
		return
	}
	shadow := cr.nudgeParityShadow.Load()
	if shadow == nil {
		return
	}
	// An unencodable timestamp leaves the digest empty. The comparator then
	// emits input_incomplete; it must never be silently dropped or repaired.
	digest, _ := nudgeParityIngressRequestDigest(request)
	now := cr.nudgeParityNow
	if now == nil {
		now = time.Now
	}
	capturedAt := now()
	watermarks := cr.captureNudgeParityWatermarks(request)
	_, _ = shadow.Submit(nudgeparity.Sample{
		OperationID: request.RequestID,
		Input: nudgeparity.Input{
			CommandDigest:    digest,
			TargetSession:    request.Target.SessionID,
			TargetGeneration: request.Target.IntentGeneration,
			TargetLaunch:     request.Target.LaunchIdentity,
			DeliveryMode:     string(request.Mode),
			TargetPolicy:     string(request.Target.Policy),
			DeliverAfter:     request.DeliverAfter,
			ExpiresAt:        request.ExpiresAt,
		},
		Watermarks:   watermarks,
		ExpectedPlan: nudgeparity.Plan{Decision: nudgeparity.DecisionExecute, Action: nudgeparity.ActionNudge},
		CapturedAt:   capturedAt,
		ExpectedTiming: nudgeparity.TimingEvidence{
			EnqueuedAt: capturedAt,
		},
	})
}

func (cr *CityRuntime) captureNudgeParityWatermarks(request nudgequeue.NudgeIngressRequest) nudgeparity.Watermarks {
	watermarks := nudgeparity.Watermarks{
		RuntimeRevision: request.Target.IntentGeneration,
		// OwnerEpoch intentionally remains zero. The legacy provider effect is
		// entered by the requesting CLI process, and no shared executor epoch
		// currently fences that process to this controller. Calling a process
		// counter or rollout enum an owner epoch would manufacture parity.
		OwnerEpoch: 0,
	}
	if revision := cr.nudgeParityConfigRevision.Load(); revision != nil {
		watermarks.ConfigRevision = *revision
	}
	cr.nudgeKeyShadowMu.RLock()
	reader := cr.nudgeKeyReader
	if reader != nil && reader.index != nil {
		status := reader.index.Status()
		if status.Synced {
			watermarks.StoreLineage = nudgeCommandReconcileScope(status.Store)
			watermarks.DurableRevision = status.Revision
		}
	}
	cr.nudgeKeyShadowMu.RUnlock()
	return watermarks
}

type validatedImmediateNudgeParityPlanner struct {
	now func() time.Time
}

func (p validatedImmediateNudgeParityPlanner) Plan(ctx context.Context, input nudgeparity.PlanningInput) (nudgeparity.Planned, error) {
	if ctx == nil {
		return nudgeparity.Planned{}, errors.New("planning immediate nudge parity: context is nil")
	}
	if err := ctx.Err(); err != nil {
		return nudgeparity.Planned{}, err
	}
	if p.now == nil {
		return nudgeparity.Planned{}, errors.New("planning immediate nudge parity: clock is nil")
	}
	plannedAt := p.now()
	if plannedAt.IsZero() || plannedAt.Before(input.CapturedAt) {
		return nudgeparity.Planned{}, errors.New("planning immediate nudge parity: clock is invalid or regressed")
	}
	plan := nudgeparity.Plan{Decision: nudgeparity.DecisionReject, Action: nudgeparity.ActionNone}
	facts := input.Input
	digest, digestErr := hex.DecodeString(facts.CommandDigest)
	validDigest := digestErr == nil && len(digest) == sha256.Size && strings.ToLower(facts.CommandDigest) == facts.CommandDigest
	if validDigest &&
		facts.DeliveryMode == string(nudgequeue.DeliveryModeImmediate) &&
		facts.TargetPolicy == string(nudgequeue.TargetPolicyExactLaunch) &&
		facts.TargetSession != "" && facts.TargetGeneration != 0 && facts.TargetLaunch != "" &&
		facts.DeliverAfter.IsZero() && !facts.ExpiresAt.IsZero() && facts.ExpiresAt.After(plannedAt) {
		plan = nudgeparity.Plan{Decision: nudgeparity.DecisionExecute, Action: nudgeparity.ActionNudge}
	}
	return nudgeparity.Planned{
		Plan:      plan,
		PlannedAt: plannedAt,
	}, nil
}

func nudgeParityIngressRequestDigest(request nudgequeue.NudgeIngressRequest) (string, error) {
	payload := struct {
		Domain       string                   `json:"domain"`
		RequestID    string                   `json:"request_id"`
		Mode         nudgequeue.DeliveryMode  `json:"mode"`
		Target       nudgequeue.CommandTarget `json:"target"`
		Source       nudgequeue.CommandSource `json:"source"`
		Message      string                   `json:"message"`
		Reference    *nudgequeue.Reference    `json:"reference,omitempty"`
		DeliverAfter time.Time                `json:"deliver_after"`
		ExpiresAt    time.Time                `json:"expires_at"`
	}{
		Domain:       productionNudgeParityDigestDomain,
		RequestID:    request.RequestID,
		Mode:         request.Mode,
		Target:       request.Target,
		Source:       request.Source,
		Message:      request.Message,
		Reference:    request.Reference,
		DeliverAfter: request.DeliverAfter,
		ExpiresAt:    request.ExpiresAt,
	}
	wire, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("digesting nudge parity ingress request: %w", err)
	}
	digest := sha256.Sum256(wire)
	return hex.EncodeToString(digest[:]), nil
}
