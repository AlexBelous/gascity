package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/config"
	"github.com/gastownhall/gascity/internal/nudgeparity"
	"github.com/gastownhall/gascity/internal/nudgequeue"
	"github.com/gastownhall/gascity/internal/runtime"
	"github.com/gastownhall/gascity/internal/testutil"
)

func TestLegacyImmediateNudgeSubmitsSameOperationToEffectFreeParityShadow(t *testing.T) {
	store := nudgequeue.CommandStoreBinding{
		StoreUUID:    "11111111-1111-4111-8111-111111111111",
		RestoreEpoch: 3,
	}
	index, err := nudgequeue.BuildCommandIndex(nudgequeue.CommandIndexSnapshot{
		Store:             store,
		Revision:          17,
		SequenceHighWater: 0,
	})
	if err != nil {
		t.Fatalf("BuildCommandIndex: %v", err)
	}

	results := make(chan nudgeparity.Result, 2)
	cr := &CityRuntime{
		configRev:            "config-revision-9",
		nudgeEffectOwnership: nudgeEffectOwnershipLegacy,
		nudgeKeyReader:       &nudgeKeyReadShadow{index: index, store: store},
		nudgeParityTargets: staticNudgeParityTargetReader{target: nudgeEffectTarget{
			sessionID:        "session-a",
			sessionName:      "runtime-a",
			intentGeneration: 23,
			launchIdentity:   "launch-a",
			provider:         "fake",
			transport:        "fake",
		}},
		nudgeParityEnabled: true,
		nudgeParityResultSink: func(_ context.Context, result nudgeparity.Result) error {
			results <- result
			return nil
		},
		stderr: io.Discard,
	}
	cr.publishNudgeParityConfigRevision("config-revision-9")
	stop, err := cr.startNudgeParityShadowBeforeReadiness(t.Context())
	if err != nil {
		t.Fatalf("startNudgeParityShadowBeforeReadiness: %v", err)
	}
	t.Cleanup(func() {
		if err := stop(); err != nil {
			t.Errorf("stop nudge parity shadow: %v", err)
		}
	})

	binding := &recordingCityRuntimeNudgeBinding{}
	woke := false
	authority := &cityRuntimeSessionNudgeAuthority{
		cr:      cr,
		binding: binding,
		wake:    func(context.Context, nudgeWakeHint) { woke = true },
	}
	request := nudgequeue.NudgeIngressRequest{
		RequestID: "request-parity-a",
		Mode:      nudgequeue.DeliveryModeImmediate,
		Target: nudgequeue.CommandTarget{
			SessionID:        "session-a",
			IntentGeneration: 23,
			LaunchIdentity:   "launch-a",
			Policy:           nudgequeue.TargetPolicyExactLaunch,
		},
		Source:    nudgequeue.CommandSourceSession,
		Message:   "inspect the deploy",
		ExpiresAt: time.Now().UTC().Add(time.Minute),
	}

	_, admissionErr := authority.Admit(t.Context(), request)
	if !errors.Is(admissionErr, errNudgeLegacyEffectOwnership) ||
		!errors.Is(admissionErr, nudgequeue.ErrLocalNudgeAuthorityUnavailable) {
		t.Fatalf("Admit error = %v, want unchanged legacy ownership result", admissionErr)
	}
	provider := runtime.NewFake()
	if err := provider.Start(t.Context(), "runtime-a", runtime.Config{}); err != nil {
		t.Fatalf("start legacy runtime: %v", err)
	}
	target := nudgeTarget{
		agent:       config.Agent{Name: "worker"},
		resolved:    &config.ResolvedProvider{Name: "fake"},
		sessionName: "runtime-a",
	}
	var stdout, stderr bytes.Buffer
	if code := deliverSessionNudgeWithProvider(target, provider, nudgeDeliveryImmediate, &stdout, &stderr); code != 0 {
		t.Fatalf("legacy provider fallback code = %d, stderr=%q", code, stderr.String())
	}
	legacyEffects := 0
	for _, call := range provider.Calls {
		if call.Method == "NudgeNow" {
			legacyEffects++
		}
		if call.Method == "Nudge" {
			t.Fatalf("shadow or fallback entered an unexpected regular nudge: %#v", call)
		}
	}
	if legacyEffects != 1 {
		t.Fatalf("legacy physical nudge effects = %d, want 1; calls=%#v", legacyEffects, provider.Calls)
	}

	var result nudgeparity.Result
	select {
	case result = <-results:
	case <-time.After(testutil.GoroutineRaceTimeout):
		t.Fatal("timed out waiting for production nudge parity comparison")
	}
	if result.OperationID != request.RequestID || !result.HasExpected || !result.HasActual {
		t.Fatalf("comparison identity/sides = %#v, want same request with both plans", result)
	}
	wantDigest, err := nudgeParityIngressRequestDigest(request)
	if err != nil {
		t.Fatalf("nudgeParityIngressRequestDigest: %v", err)
	}
	for side, expectation := range map[string]struct {
		observation nudgeparity.Observation
		plan        nudgeparity.Plan
	}{
		"legacy": {
			observation: result.Expected,
			plan: nudgeparity.Plan{
				Decision:          nudgeparity.DecisionExecute,
				Action:            nudgeparity.ActionNudge,
				InteractionPolicy: nudgeparity.InteractionPolicyForce,
			},
		},
		"keyed": {
			observation: result.Actual,
			plan: nudgeparity.Plan{
				Decision:          nudgeparity.DecisionExecute,
				Action:            nudgeparity.ActionNudge,
				InteractionPolicy: nudgeparity.InteractionPolicyRequireUnattachedNormal,
			},
		},
	} {
		observation := expectation.observation
		if observation.OperationID != request.RequestID ||
			observation.Input.CommandDigest != wantDigest ||
			observation.Input.TargetSession != request.Target.SessionID ||
			observation.Input.TargetGeneration != request.Target.IntentGeneration ||
			observation.Input.TargetLaunch != request.Target.LaunchIdentity {
			t.Fatalf("%s immutable input = %#v, want exact request-derived input", side, observation.Input)
		}
		if observation.Watermarks.StoreLineage != nudgeCommandReconcileScope(store) ||
			observation.Watermarks.DurableRevision != 17 ||
			observation.Watermarks.ConfigRevision != "config-revision-9" ||
			observation.Watermarks.RuntimeRevision != request.Target.IntentGeneration {
			t.Fatalf("%s watermarks = %#v, want captured production projections", side, observation.Watermarks)
		}
		if observation.Watermarks.OwnerEpoch != 0 {
			t.Fatalf("%s owner epoch = %d, want honest unavailable evidence", side, observation.Watermarks.OwnerEpoch)
		}
		if observation.Plan != expectation.plan {
			t.Fatalf("%s plan = %#v, want %#v", side, observation.Plan, expectation.plan)
		}
	}
	if result.Classification != nudgeparity.ClassificationIncomparable || result.Reason != nudgeparity.ReasonWatermarkIncomplete {
		t.Fatalf("comparison = %s/%s, want typed watermark_incomplete until owner evidence exists",
			result.Classification, result.Reason)
	}
	if got := binding.admissionCount(); got != 0 {
		t.Fatalf("shadow durable admissions = %d, want 0", got)
	}
	if woke {
		t.Fatal("shadow emitted a keyed wake")
	}
	if err := stop(); err != nil {
		t.Fatalf("stop nudge parity shadow: %v", err)
	}
	select {
	case extra := <-results:
		t.Fatalf("unexpected second parity comparison: %#v", extra)
	default:
	}
}

func TestLegacyNudgeBeforeParityReadinessIsExplicitLossAndKeepsFallback(t *testing.T) {
	cr := &CityRuntime{
		nudgeEffectOwnership: nudgeEffectOwnershipLegacy,
		nudgeParityEnabled:   true,
	}
	if err := cr.prepareNudgeParityShadow(); err != nil {
		t.Fatalf("prepareNudgeParityShadow: %v", err)
	}
	shadow := cr.nudgeParityShadow.Load()
	if shadow == nil {
		t.Fatal("prepareNudgeParityShadow did not publish admission before readiness")
	}

	authority := &cityRuntimeSessionNudgeAuthority{cr: cr}
	_, err := authority.Admit(t.Context(), nudgequeue.NudgeIngressRequest{
		RequestID: "request-before-ready",
		Mode:      nudgequeue.DeliveryModeImmediate,
		Target: nudgequeue.CommandTarget{
			SessionID:        "session-before-ready",
			IntentGeneration: 1,
			LaunchIdentity:   "launch-before-ready",
			Policy:           nudgequeue.TargetPolicyExactLaunch,
		},
		Source:    nudgequeue.CommandSourceSession,
		Message:   "do not drop silently",
		ExpiresAt: time.Now().UTC().Add(time.Minute),
	})
	if !errors.Is(err, errNudgeLegacyEffectOwnership) {
		t.Fatalf("Admit error = %v, want unchanged legacy fallback", err)
	}
	if snapshot := shadow.Snapshot(); snapshot.NotRunning != 1 || snapshot.Accepted != 0 {
		t.Fatalf("pre-readiness snapshot = %#v, want one explicit not_running loss", snapshot)
	}
}

func TestLegacyNudgeParityQueueSaturationCannotChangeOwnershipOrEnterEffects(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	planner := &blockingProductionNudgeParityPlanner{entered: entered, release: release}
	cr := &CityRuntime{
		nudgeEffectOwnership:  nudgeEffectOwnershipLegacy,
		nudgeParityEnabled:    true,
		nudgeParityPlanner:    planner,
		nudgeParityResultSink: func(context.Context, nudgeparity.Result) error { return nil },
		nudgeParityNow:        time.Now,
	}
	cr.publishNudgeParityConfigRevision("config-saturation")
	stop, err := cr.startNudgeParityShadowBeforeReadiness(t.Context())
	if err != nil {
		t.Fatalf("startNudgeParityShadowBeforeReadiness: %v", err)
	}
	var releaseOnce sync.Once
	t.Cleanup(func() {
		releaseOnce.Do(func() { close(release) })
		if err := stop(); err != nil {
			t.Errorf("stop nudge parity shadow: %v", err)
		}
	})

	binding := &recordingCityRuntimeNudgeBinding{}
	authority := &cityRuntimeSessionNudgeAuthority{
		cr:      cr,
		binding: binding,
		wake:    func(context.Context, nudgeWakeHint) { t.Error("saturated shadow emitted keyed wake") },
	}
	request := nudgequeue.NudgeIngressRequest{
		RequestID: "request-saturation-0",
		Mode:      nudgequeue.DeliveryModeImmediate,
		Target: nudgequeue.CommandTarget{
			SessionID:        "session-saturation",
			IntentGeneration: 5,
			LaunchIdentity:   "launch-saturation",
			Policy:           nudgequeue.TargetPolicyExactLaunch,
		},
		Source:    nudgequeue.CommandSourceSession,
		Message:   "bounded",
		ExpiresAt: time.Now().UTC().Add(time.Minute),
	}
	if _, err := authority.Admit(t.Context(), request); !errors.Is(err, errNudgeLegacyEffectOwnership) {
		t.Fatalf("first Admit error = %v, want legacy ownership", err)
	}
	select {
	case <-entered:
	case <-time.After(testutil.GoroutineRaceTimeout):
		t.Fatal("blocking parity planner did not receive first operation")
	}

	for i := 1; i <= productionNudgeParityQueueCapacity+2; i++ {
		request.RequestID = "request-saturation-" + strconv.Itoa(i)
		if _, err := authority.Admit(t.Context(), request); !errors.Is(err, errNudgeLegacyEffectOwnership) {
			t.Fatalf("Admit %d error = %v, want unchanged legacy ownership", i, err)
		}
	}
	shadow := cr.nudgeParityShadow.Load()
	if shadow == nil {
		t.Fatal("running parity shadow was unpublished before shutdown")
	}
	if snapshot := shadow.Snapshot(); snapshot.Full == 0 || snapshot.Accepted != productionNudgeParityQueueCapacity+1 {
		t.Fatalf("saturated snapshot = %#v, want explicit full loss and bounded acceptance", snapshot)
	}
	if got := binding.admissionCount(); got != 0 {
		t.Fatalf("saturated shadow durable admissions = %d, want 0", got)
	}

	releaseOnce.Do(func() { close(release) })
}

func TestProductionNudgeParityRetainsDivergentInjectedKeyedPlan(t *testing.T) {
	results := make(chan nudgeparity.Result, 1)
	cr := &CityRuntime{
		nudgeEffectOwnership: nudgeEffectOwnershipLegacy,
		nudgeParityEnabled:   true,
		nudgeParityPlanner: nudgeparity.PlannerFunc(func(_ context.Context, input nudgeparity.PlanningInput) (nudgeparity.Planned, error) {
			return nudgeparity.Planned{
				Input:      input.Input,
				Watermarks: input.Watermarks,
				Plan: nudgeparity.Plan{
					Decision:          nudgeparity.DecisionReject,
					Action:            nudgeparity.ActionNone,
					InteractionPolicy: nudgeparity.InteractionPolicyRequireUnattachedNormal,
				},
				PlannedAt: input.CapturedAt,
			}, nil
		}),
		nudgeParityResultSink: func(_ context.Context, result nudgeparity.Result) error {
			results <- result
			return nil
		},
	}
	stop, err := cr.startNudgeParityShadowBeforeReadiness(t.Context())
	if err != nil {
		t.Fatalf("startNudgeParityShadowBeforeReadiness: %v", err)
	}
	t.Cleanup(func() { _ = stop() })
	authority := &cityRuntimeSessionNudgeAuthority{cr: cr}
	_, err = authority.Admit(t.Context(), validProductionParityIngressRequest(time.Now().UTC().Add(time.Minute)))
	if !errors.Is(err, errNudgeLegacyEffectOwnership) {
		t.Fatalf("Admit error = %v, want legacy ownership", err)
	}
	select {
	case result := <-results:
		if result.Expected.Plan != (nudgeparity.Plan{
			Decision:          nudgeparity.DecisionExecute,
			Action:            nudgeparity.ActionNudge,
			InteractionPolicy: nudgeparity.InteractionPolicyForce,
		}) || result.Actual.Plan != (nudgeparity.Plan{
			Decision:          nudgeparity.DecisionReject,
			Action:            nudgeparity.ActionNone,
			InteractionPolicy: nudgeparity.InteractionPolicyRequireUnattachedNormal,
		}) {
			t.Fatalf("divergent plans = expected %#v actual %#v", result.Expected.Plan, result.Actual.Plan)
		}
	case <-time.After(testutil.GoroutineRaceTimeout):
		t.Fatal("timed out waiting for divergent production comparison")
	}
}

func TestImmediateNudgeParityInputValidationIsTotal(t *testing.T) {
	now := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)
	valid := nudgeparity.Input{
		CommandDigest:    strings.Repeat("a", 64),
		TargetSession:    "session-planner",
		TargetGeneration: 8,
		TargetLaunch:     "launch-planner",
		DeliveryMode:     string(nudgequeue.DeliveryModeImmediate),
		TargetPolicy:     string(nudgequeue.TargetPolicyExactLaunch),
		ExpiresAt:        now.Add(time.Minute),
	}
	tests := []struct {
		name   string
		mutate func(*nudgeparity.Input)
		want   bool
	}{
		{name: "valid", mutate: func(*nudgeparity.Input) {}, want: true},
		{name: "expired", mutate: func(input *nudgeparity.Input) { input.ExpiresAt = now }},
		{name: "missing launch", mutate: func(input *nudgeparity.Input) { input.TargetLaunch = "" }},
		{name: "missing generation", mutate: func(input *nudgeparity.Input) { input.TargetGeneration = 0 }},
		{name: "wrong target policy", mutate: func(input *nudgeparity.Input) {
			input.TargetPolicy = string(nudgequeue.TargetPolicyContinuation)
		}},
		{name: "scheduled immediate", mutate: func(input *nudgeparity.Input) { input.DeliverAfter = now.Add(time.Second) }},
		{name: "bad digest", mutate: func(input *nudgeparity.Input) { input.CommandDigest = "not-a-digest" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := valid
			test.mutate(&input)
			if got := validImmediateNudgeParityInput(input, now); got != test.want {
				t.Fatalf("validImmediateNudgeParityInput = %v, want %v", got, test.want)
			}
		})
	}
}

func TestProductionNudgeParityPlannerUsesKernelAndIndependentCurrentTarget(t *testing.T) {
	now := time.Date(2026, 7, 16, 12, 30, 0, 0, time.UTC)
	store := nudgequeue.CommandStoreBinding{
		StoreUUID:    "33333333-3333-4333-8333-333333333333",
		RestoreEpoch: 5,
	}
	index, err := nudgequeue.BuildCommandIndex(nudgequeue.CommandIndexSnapshot{
		Store:    store,
		Revision: 41,
	})
	if err != nil {
		t.Fatalf("BuildCommandIndex: %v", err)
	}
	cr := &CityRuntime{
		nudgeKeyReader: &nudgeKeyReadShadow{index: index, store: store},
		nudgeParityTargets: staticNudgeParityTargetReader{target: nudgeEffectTarget{
			sessionID:        "session-planner-current",
			sessionName:      "runtime-planner-current",
			intentGeneration: 12,
			launchIdentity:   "launch-planner-current",
			provider:         "fake",
			transport:        "fake",
		}},
	}
	cr.publishNudgeParityConfigRevision("config-planner-current")
	planner := productionNudgeParityPlanner{runtime: cr, now: func() time.Time { return now }}
	planned, err := planner.Plan(t.Context(), nudgeparity.PlanningInput{
		OperationID: "request-planner-current",
		Input: nudgeparity.Input{
			CommandDigest:    strings.Repeat("a", 64),
			TargetSession:    "session-planner-current",
			TargetGeneration: 12,
			TargetLaunch:     "launch-planner-current",
			DeliveryMode:     string(nudgequeue.DeliveryModeImmediate),
			TargetPolicy:     string(nudgequeue.TargetPolicyExactLaunch),
			ExpiresAt:        now.Add(time.Minute),
		},
		CapturedAt: now.Add(-time.Second),
		EnqueuedAt: now.Add(-time.Second),
	})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	wantPlan := nudgeparity.Plan{
		Decision:          nudgeparity.DecisionExecute,
		Action:            nudgeparity.ActionNudge,
		InteractionPolicy: nudgeparity.InteractionPolicyRequireUnattachedNormal,
	}
	if planned.Plan != wantPlan {
		t.Fatalf("Plan = %#v, want %#v", planned.Plan, wantPlan)
	}
	if planned.Input.TargetGeneration != 12 || planned.Input.TargetLaunch != "launch-planner-current" {
		t.Fatalf("independent target input = %#v", planned.Input)
	}
	wantWatermarks := nudgeparity.Watermarks{
		StoreLineage:    nudgeCommandReconcileScope(store),
		DurableRevision: 41,
		ConfigRevision:  "config-planner-current",
		RuntimeRevision: 12,
	}
	if planned.Watermarks != wantWatermarks {
		t.Fatalf("independent watermarks = %#v, want %#v", planned.Watermarks, wantWatermarks)
	}
}

type staticNudgeParityTargetReader struct {
	target nudgeEffectTarget
	err    error
}

func (r staticNudgeParityTargetReader) Read(context.Context, string) (nudgeEffectTarget, error) {
	return r.target, r.err
}

func validProductionParityIngressRequest(expiresAt time.Time) nudgequeue.NudgeIngressRequest {
	return nudgequeue.NudgeIngressRequest{
		RequestID: "request-production-parity",
		Mode:      nudgequeue.DeliveryModeImmediate,
		Target: nudgequeue.CommandTarget{
			SessionID:        "session-production-parity",
			IntentGeneration: 9,
			LaunchIdentity:   "launch-production-parity",
			Policy:           nudgequeue.TargetPolicyExactLaunch,
		},
		Source:    nudgequeue.CommandSourceSession,
		Message:   "production parity",
		ExpiresAt: expiresAt,
	}
}

type blockingProductionNudgeParityPlanner struct {
	once    sync.Once
	entered chan struct{}
	release <-chan struct{}
}

type parityEffectSourceSpy struct {
	snapshotReads atomic.Int32
	exactReads    atomic.Int32
	claims        atomic.Int32
	completions   atomic.Int32
	retries       atomic.Int32
}

func (s *parityEffectSourceSpy) Snapshot(context.Context, int) (nudgequeue.CommandIndexSnapshot, error) {
	s.snapshotReads.Add(1)
	return nudgequeue.CommandIndexSnapshot{}, errors.New("unexpected parity snapshot read")
}

func (s *parityEffectSourceSpy) Get(context.Context, string) (nudgequeue.CommandIndexResolution, error) {
	s.exactReads.Add(1)
	return nudgequeue.CommandIndexResolution{}, errors.New("unexpected parity exact read")
}

func (s *parityEffectSourceSpy) ClaimAuthorized(context.Context, nudgeEffectClaimRequest, nudgequeue.NudgeClaimAuthorizer) (nudgequeue.CommandClaimResult, error) {
	s.claims.Add(1)
	return nudgequeue.CommandClaimResult{}, errors.New("unexpected parity claim")
}

func (s *parityEffectSourceSpy) CompleteProviderAttempt(context.Context, nudgequeue.CommandCompletionRequest) (nudgequeue.CommandCompletionResult, error) {
	s.completions.Add(1)
	return nudgequeue.CommandCompletionResult{}, errors.New("unexpected parity completion")
}

func (s *parityEffectSourceSpy) RetryProviderAttempt(context.Context, nudgequeue.CommandRetryRequest) (nudgequeue.CommandRetryResult, error) {
	s.retries.Add(1)
	return nudgequeue.CommandRetryResult{}, errors.New("unexpected parity retry")
}

var _ nudgeCommandEffectSource = (*parityEffectSourceSpy)(nil)

func (p *blockingProductionNudgeParityPlanner) Plan(ctx context.Context, input nudgeparity.PlanningInput) (nudgeparity.Planned, error) {
	p.once.Do(func() { close(p.entered) })
	select {
	case <-p.release:
		return nudgeparity.Planned{
			Input:      input.Input,
			Watermarks: input.Watermarks,
			Plan: nudgeparity.Plan{
				Decision:          nudgeparity.DecisionExecute,
				Action:            nudgeparity.ActionNudge,
				InteractionPolicy: nudgeparity.InteractionPolicyRequireUnattachedNormal,
			},
			PlannedAt: time.Now(),
		}, nil
	case <-ctx.Done():
		return nudgeparity.Planned{}, ctx.Err()
	}
}
