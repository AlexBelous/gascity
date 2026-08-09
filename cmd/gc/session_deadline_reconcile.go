package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/clock"
	"github.com/gastownhall/gascity/internal/events"
	"github.com/gastownhall/gascity/internal/rollout"
	"github.com/gastownhall/gascity/internal/runtime"
	sessionpkg "github.com/gastownhall/gascity/internal/session"
	"github.com/gastownhall/gascity/internal/telemetry"
)

// reconcileExactSessionDetectorFamily is the WD handler-dispatch seam. The
// detector sweep hands an exact key to the session-start controller under a
// family admission source; this block routes that key to the family's handler.
// Each later WD slice adds EXACTLY ONE case, and two rules bind every one:
//
//   - The case's guard is a predicate over the DURABLE ROW just read, never
//     over admission.Source. The controller coalesces admissions on a key and
//     keeps the earlier source, so a source-gated arm silently routes a
//     level-triggered condition into the ordinary start path's dead end — the
//     ga-f7v2ft.125 failure, exactly.
//   - The handler re-derives its own condition from that row and refuses with
//     zero effect the moment it no longer holds. The detector's reason is a
//     scheduling hint; the row is the authority.
//
// It returns handled=false for every key no family claims, which is every key
// in a city where no act constant has flipped.
func reconcileExactSessionDetectorFamily(
	ctx context.Context,
	admission sessionStartAdmission,
	params exactSessionStartParams,
	info sessionpkg.Info,
	response sessionpkg.PersistedResponse,
	clk clock.Clock,
) (bool, exactSessionStartOwner, error) {
	if clk == nil {
		clk = clock.Real{}
	}
	// Case order mirrors legacy's forward pass, where the not-desired block runs
	// before the deadline arms and early-continues: an undesired row past its
	// idle deadline is the orphan family's, not the deadline family's.
	switch {
	case detectorActOrphan && exactSessionOrphanCloseCandidate(params, info, response, clk) != "":
		owner, err := reconcileExactSessionOrphanClose(ctx, admission, params, info, response, clk)
		return true, owner, err
	case detectorActDeadline && exactSessionDeadlineStopCandidate(params, info, response, clk.Now().UTC()):
		owner, err := reconcileExactSessionDeadlineStop(ctx, admission, params, info, response, clk)
		return true, owner, err
	}
	return false, exactSessionStartUnowned, nil
}

// exactSessionDeadline names which lifecycle timer fired for one durable row.
type exactSessionDeadline struct {
	Site   TraceSiteCode
	MaxAge bool
}

// exactSessionDeadlineTriggered re-derives the D-DEADLINE trigger rung from the
// durable row and the fleet's own timer trackers. It is the seam's dispatch
// predicate and the handler's pre-stop re-verification, so both answer from the
// same source. Legacy's arm order is preserved: max-session-age is consulted
// before idle-timeout.
func exactSessionDeadlineTriggered(params exactSessionStartParams, info sessionpkg.Info, now time.Time) []exactSessionDeadline {
	name := strings.TrimSpace(info.SessionNameMetadata)
	if name == "" || info.Closed || !detectorBeadAwake(info) {
		return nil
	}
	template := normalizedSessionTemplateInfo(info, params.Config)
	if template == "" {
		template = info.Template
	}
	var fired []exactSessionDeadline
	if params.MaxSessionAgeTracker != nil {
		if completeAt, ok := parseRFC3339Metadata(info.CreationCompleteAt); ok &&
			params.MaxSessionAgeTracker.shouldRestart(name, template, completeAt, now) {
			fired = append(fired, exactSessionDeadline{Site: TraceSiteReconcilerMaxSessionAge, MaxAge: true})
		}
	}
	if params.IdleTracker != nil && params.IdleTracker.checkIdle(name, template, params.Provider, now) {
		fired = append(fired, exactSessionDeadline{Site: TraceSiteReconcilerIdleTimeout})
	}
	return fired
}

// exactSessionDeadlineStopCandidate is the seam's guard. A durable blocker
// keeps the row out of the family entirely — a user-hold row belongs to the
// suspend arm above, and a quarantined row belongs to nobody.
func exactSessionDeadlineStopCandidate(params exactSessionStartParams, info sessionpkg.Info, response sessionpkg.PersistedResponse, now time.Time) bool {
	if response.Revision == 0 || (params.IdleTracker == nil && params.MaxSessionAgeTracker == nil) {
		return false
	}
	if lifecycleTimerBlockerInfo(info, now) != "" {
		return false
	}
	return len(exactSessionDeadlineTriggered(params, info, now)) > 0
}

// reconcileExactSessionDeadlineStop stops one over-deadline session by exact
// key. It reuses ga-f7v2ft.102's stop machinery verbatim — the D2 capability
// pair, the token-bound unattended stop, and the fresh-death confirmation — and
// adds the one thing a deadline stop needs that a suspend stop does not: the
// sleep patch lands BEFORE the key is released, so a same-tick D-WAKE cannot
// respawn the incarnation this handler just killed.
func reconcileExactSessionDeadlineStop(
	ctx context.Context,
	admission sessionStartAdmission,
	params exactSessionStartParams,
	info sessionpkg.Info,
	response sessionpkg.PersistedResponse,
	clk clock.Clock,
) (exactSessionStartOwner, error) {
	if clk == nil {
		clk = clock.Real{}
	}
	stderr := params.Stderr
	if stderr == nil {
		stderr = io.Discard
	}
	recorder := params.Recorder
	if recorder == nil {
		recorder = events.Discard
	}
	yieldOrPark := func(cause error) (exactSessionStartOwner, error) {
		if params.RolloutMode == rollout.Auto {
			return exactSessionStartLegacyOwner, fmt.Errorf("%w: %w", errSessionStartLegacyFallbackRequired, cause)
		}
		return exactSessionStartKeyedOwner, cause
	}
	if params.DrainTracker != nil && params.DrainTracker.get(info.ID) != nil {
		return yieldOrPark(errors.New("exact over-deadline session has an active legacy drain"))
	}
	if _, ok := params.Provider.(runtime.FreshLivenessObserver); !ok {
		return yieldOrPark(errors.New("exact over-deadline session provider cannot prove fresh liveness"))
	}
	if _, ok := params.Provider.(runtime.UnattendedSessionStopper); !ok {
		return yieldOrPark(errors.New("exact over-deadline session provider cannot prove unattended stop"))
	}
	// The sleep patch is the whole point of the ordering guarantee, so an
	// AMBIGUOUS writer must never enter the provider. A merely absent
	// conditional writer is not ambiguous: conditional writes are gated per
	// store and off by default, and the legacy arm this replaces writes the
	// same patch through the plain front door. persistExactSessionDeadlineSleep
	// takes the CAS fence when it exists and the front door when it does not.
	if params.StatusWriterError != nil {
		return yieldOrPark(fmt.Errorf("exact over-deadline sleep writer: %w", params.StatusWriterError))
	}

	now := clk.Now().UTC()
	name := strings.TrimSpace(info.SessionNameMetadata)
	decision, deadline, ok := decideExactSessionDeadline(params, info, clk, now)
	if !ok {
		// Every rung below the trigger is a defer: blocker, pending interaction,
		// or open assigned work. Record it and release the key with zero effect;
		// the condition is level-triggered and re-detected next sweep.
		recordExactSessionDeadlineTrace(params, admission, info, deadline.Site, decision, 0, false)
		return exactSessionStartKeyedOwner, nil
	}

	processNames := drainAckStopPendingProcessNames(params.Config, info)
	incarnationStartedAt := drainAckIncarnationStartedAt(info)
	liveness := runtime.ObserveFreshLiveness(params.Provider, runtime.LivenessTarget{
		SessionID:            info.ID,
		SessionName:          name,
		ProcessNames:         processNames,
		IncarnationStartedAt: incarnationStartedAt,
	})
	if !liveness.Complete {
		return yieldOrPark(errors.New("exact over-deadline session liveness observation is incomplete"))
	}
	if !liveness.Running && !liveness.Alive {
		// Nothing to stop. A durably-awake row with a dead runtime is D-ORPHAN's
		// and D-SLEEP's condition, not this family's.
		return exactSessionStartKeyedOwner, nil
	}

	latest, latestResponse, readErr := getAuthoritativeSessionStartPersistedRecord(params.Store, info.ID)
	if readErr != nil || latestResponse.Revision != response.Revision || latest.Closed ||
		strings.TrimSpace(latest.InstanceToken) != strings.TrimSpace(info.InstanceToken) ||
		strings.TrimSpace(latest.SessionNameMetadata) != name ||
		!exactSessionDeadlineStopCandidate(params, latest, latestResponse, clk.Now().UTC()) {
		return exactSessionStartKeyedOwner, nil
	}
	if params.DrainTracker != nil && params.DrainTracker.get(info.ID) != nil {
		return yieldOrPark(errors.New("exact over-deadline session entered an active legacy drain before stop"))
	}

	stopStartedAt := time.Now()
	if stopErr := workerStopUnattendedSessionByIDWithConfig(params.CityPath, params.Store, params.Provider, params.Config, info.ID, info.InstanceToken); stopErr != nil {
		return exactSessionStartKeyedOwner, fmt.Errorf("stopping exact over-deadline session %q: %w", info.ID, stopErr)
	}
	if completion := confirmDrainAckRuntimeDeadCompletion(params.CityPath, params.Store, params.Provider, params.Config, info.ID, name, info.InstanceToken, processNames, stderr, incarnationStartedAt, true); completion != drainAckAsyncStopConfirmed {
		return exactSessionStartKeyedOwner, fmt.Errorf("confirming exact over-deadline session %q stopped: %v", info.ID, completion)
	}
	_ = params.Provider.ClearScrollback(name) //nolint:errcheck // scrollback clearing is best-effort, matching the legacy arm

	if err := persistExactSessionDeadlineSleep(params, info, latestResponse.Revision, sessionpkg.SleepPatch(clk.Now().UTC(), decision.SleepReason)); err != nil {
		return exactSessionStartKeyedOwner, err
	}

	subject := strings.TrimSpace(info.AgentName)
	if subject == "" {
		subject = name
	}
	if deadline.MaxAge {
		recorder.Record(events.Event{Type: events.SessionMaxAgeKilled, Actor: "gc", Subject: subject})
		telemetry.RecordAgentMaxAgeKill(ctx, subject)
	} else {
		recorder.Record(events.Event{Type: events.SessionIdleKilled, Actor: "gc", Subject: subject})
		telemetry.RecordAgentIdleKill(ctx, subject)
	}
	recordExactSessionDeadlineTrace(params, admission, info, deadline.Site, decision, time.Since(stopStartedAt), true)
	return exactSessionStartKeyedOwner, nil
}

// decideExactSessionDeadline runs the existing pure ladders over facts gathered
// per key. Legacy gathers the same facts fleet-wide; the only difference is that
// here the pending probe and the reachable-store scan are paid once, for one
// session that actually hit a deadline.
func decideExactSessionDeadline(
	params exactSessionStartParams,
	info sessionpkg.Info,
	clk clock.Clock,
	now time.Time,
) (sessionpkg.TimerDecision, exactSessionDeadline, bool) {
	name := strings.TrimSpace(info.SessionNameMetadata)
	var lastDecision sessionpkg.TimerDecision
	var lastDeadline exactSessionDeadline
	for _, deadline := range exactSessionDeadlineTriggered(params, info, now) {
		decide := sessionpkg.DecideIdleTimeout
		hasAssignedWork := sessionHasAwakeAssignedWorkForReachableStore
		if deadline.MaxAge {
			decide = sessionpkg.DecideMaxSessionAge
			hasAssignedWork = sessionHasOpenAssignedWorkForReachableStore
		}
		facts := sessionpkg.TimerFacts{Triggered: true, Blocker: lifecycleTimerBlockerInfo(info, now)}
		dec := decide(facts)
		for dec.Action == sessionpkg.TimerActionGatherPending || dec.Action == sessionpkg.TimerActionGatherAssignedWork {
			if dec.Action == sessionpkg.TimerActionGatherPending {
				facts.Pending = sessionpkg.PendingNo
				if pendingInteractionKeepsAwakeInfo(info, params.Provider, name, clk) {
					facts.Pending = sessionpkg.PendingYes
				}
			} else {
				// Fail closed on a store blip, exactly as the fleet arms do: a
				// session that may still hold in-flight work is not killed.
				has, err := hasAssignedWork(params.CityPath, params.Config, params.Store, params.RigStores, info)
				if err != nil {
					has = true
				}
				facts.AssignedWork = sessionpkg.AssignedWorkNone
				if has {
					facts.AssignedWork = sessionpkg.AssignedWorkHas
				}
			}
			dec = decide(facts)
		}
		// The consecutive same-bead assigned-work backstop travels with the
		// ladder (ga-nllza6). Without it here, an acting D-DEADLINE would defer a
		// wedged session forever: legacy yields the key, so its own backstop
		// never sees the defer.
		if !deadline.MaxAge && params.AssignedWorkDeferTracker != nil {
			if dec.Action == sessionpkg.TimerActionDefer && dec.TraceReason == string(TraceReasonAssignedWork) {
				if params.AssignedWorkDeferTracker.recordDefer(name, normalizedSessionTemplateInfo(info, params.Config), strings.TrimSpace(info.CurrentlyProcessingBeadID)) {
					dec = sessionpkg.DecideAssignedWorkExhausted()
				}
			} else {
				params.AssignedWorkDeferTracker.reset(name)
			}
		}
		if dec.Action == sessionpkg.TimerActionStop {
			return dec, deadline, true
		}
		lastDecision, lastDeadline = dec, deadline
	}
	return lastDecision, lastDeadline, false
}

// persistExactSessionDeadlineSleep lands the sleep patch BEFORE the key is
// released, so a D-WAKE admission on the same key cannot respawn the
// incarnation this handler just killed. Where the store offers conditional
// writes the patch is fenced on the revision the pre-stop reread proved, with
// one bounded retry: the stop already happened, so a lost CAS must not leave
// the row claiming awake. Where it does not, the patch goes through the same
// session front door the legacy arm writes it through.
func persistExactSessionDeadlineSleep(params exactSessionStartParams, info sessionpkg.Info, revision int64, patch sessionpkg.MetadataPatch) error {
	if params.StatusWriter == nil {
		if err := sessionFrontDoor(params.Store).ApplyPatch(info.ID, patch); err != nil {
			return fmt.Errorf("persisting exact over-deadline sleep for %q: %w", info.ID, err)
		}
		return nil
	}
	err := params.StatusWriter.UpdateIfMatch(info.ID, revision, beads.UpdateOpts{Metadata: patch})
	if err == nil {
		return nil
	}
	latest, latestResponse, readErr := getAuthoritativeSessionStartPersistedRecord(params.Store, info.ID)
	if readErr != nil || latest.Closed || latestResponse.Revision == 0 ||
		strings.TrimSpace(latest.InstanceToken) != strings.TrimSpace(info.InstanceToken) {
		return fmt.Errorf("persisting exact over-deadline sleep for %q: %w", info.ID, err)
	}
	if retryErr := params.StatusWriter.UpdateIfMatch(info.ID, latestResponse.Revision, beads.UpdateOpts{Metadata: patch}); retryErr != nil {
		return fmt.Errorf("persisting exact over-deadline sleep for %q: %w", info.ID, retryErr)
	}
	return nil
}

// recordExactSessionDeadlineTrace fires the SAME legacy trace site the fleet arm
// fires, with effect_owner=keyed and the honest effect_applied. The WD.15 parity
// join reads exactly these fields to separate the legacy, keyed, and
// detector-shadow populations on a shared cycle.
func recordExactSessionDeadlineTrace(
	params exactSessionStartParams,
	admission sessionStartAdmission,
	info sessionpkg.Info,
	site TraceSiteCode,
	decision sessionpkg.TimerDecision,
	duration time.Duration,
	applied bool,
) {
	if params.Trace == nil || site == "" || decision.Action == sessionpkg.TimerActionNone {
		return
	}
	cycle := params.Trace.BeginCycle(TraceTickTriggerControl, "exact_session_deadline_stop", time.Now().UTC(), params.Config)
	if cycle == nil {
		return
	}
	template := normalizedSessionTemplateInfo(info, params.Config)
	if cycle.detailEnabled(template) {
		reason, outcome := timerTraceCodes(decision)
		cycle.recordAdmittedDetailOperation(
			site,
			reason,
			outcome,
			"exact_session_deadline_stop",
			template,
			info.ID,
			info.SessionNameMetadata,
			TraceSource(cycle.sourceFor(template)),
			duration,
			map[string]any{
				"admission":         string(admission.Source),
				"admission_version": admission.Version,
				"generation":        params.Generation,
				"instance_token":    info.InstanceToken,
				"sleep_reason":      decision.SleepReason,
				"effect_owner":      detectorKeyedEffectOwner,
				"effect_applied":    applied,
			},
		)
	}
	if err := cycle.End(TraceCompletionCompleted, nil); err != nil && params.Stderr != nil {
		fmt.Fprintf(params.Stderr, "session reconciler: recording exact deadline stop trace: %v\n", err) //nolint:errcheck // tracing is observational
	}
}
