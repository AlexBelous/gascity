package main

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/clock"
	"github.com/gastownhall/gascity/internal/config"
	"github.com/gastownhall/gascity/internal/rollout"
	sessionpkg "github.com/gastownhall/gascity/internal/session"
)

const (
	sessionStartControllerMaxDistinct = 4096
	sessionStartControllerMaxRetries  = 5
	sessionStartSeedPageSize          = 64
)

type sessionStartOwnership uint8

const (
	sessionStartOwnershipLegacy sessionStartOwnership = iota
	sessionStartOwnershipKeyed
	sessionStartOwnershipRequiredBlocked
)

var newCitySessionStartController = newSessionStartController

func (cr *CityRuntime) ensureSessionStartController(ctx context.Context, seed *sessionBeadSnapshot) error {
	if cr == nil {
		return fmt.Errorf("city runtime is nil")
	}
	cr.sessionStartMu.Lock()
	defer cr.sessionStartMu.Unlock()

	mode := rollout.ModeUnset
	if cr.cs != nil {
		mode = cr.cs.RolloutFlags().SessionReconciler()
	}
	cr.sessionStartMode = mode
	if cr.sessionStartController != nil && cr.sessionStartOwnership == sessionStartOwnershipKeyed {
		return cr.seedSessionStartController(cr.sessionStartController, seed)
	}

	var (
		stateSnapshot controllerSessionStartSnapshot
		releaseState  func()
		capabilityErr error
	)
	decision, reason := rollout.ResolveCapability(ctx, mode, func(context.Context) (bool, string) {
		if cr.cs == nil {
			return false, "controller state is unavailable"
		}
		if seed == nil {
			return false, "startup session snapshot is unavailable"
		}
		if err := seed.LoadError(); err != nil {
			return false, "startup session snapshot is incomplete: " + err.Error()
		}
		stateSnapshot, releaseState, capabilityErr = cr.cs.acquireSessionStartSnapshot()
		if capabilityErr != nil {
			return false, capabilityErr.Error()
		}
		return true, "coherent config, provider, and session store are available"
	})
	if releaseState != nil {
		defer releaseState()
	}

	switch decision {
	case rollout.UseLegacy:
		cr.sessionStartOwnership = sessionStartOwnershipLegacy
		return nil
	case rollout.DegradeLoud:
		cr.sessionStartOwnership = sessionStartOwnershipLegacy
		fmt.Fprintf(cr.sessionStartStderr(), "%s: session-start controller unavailable (%s); falling back to legacy reconciliation\n", cr.sessionStartLogPrefix(), reason) //nolint:errcheck // rollout degradation must be loud
		return nil
	case rollout.RefuseClosed:
		cr.sessionStartOwnership = sessionStartOwnershipRequiredBlocked
		return fmt.Errorf("required keyed session-start controller is unavailable: %s", reason)
	case rollout.UseNew:
		// Continue below.
	default:
		cr.sessionStartOwnership = sessionStartOwnershipLegacy
		return fmt.Errorf("unexpected session-start rollout decision %q", decision)
	}

	workers := maxParallelStartsPerTick(stateSnapshot.Config)
	var controller *sessionStartController
	var err error
	controller, err = newCitySessionStartController(sessionStartControllerOptions{
		Workers:     workers,
		MaxDistinct: sessionStartControllerMaxDistinct,
		MaxRetries:  sessionStartControllerMaxRetries,
		Reconcile: func(reconcileCtx context.Context, admission sessionStartAdmission) error {
			snapshot, release, acquireErr := cr.cs.acquireSessionStartSnapshot()
			if acquireErr != nil {
				return acquireErr
			}
			leaseTransferred := false
			defer func() {
				if !leaseTransferred {
					release()
				}
			}()
			startOptions := cr.sessionStartOptions
			if admission.Source == sessionStartAdmissionInProcess || admission.Source == sessionStartAdmissionSocket {
				startOptions = append([]startExecutionOption(nil), startOptions...)
				startOptions = append(startOptions, withAdditionalExactSessionLifecycleStatusObserver(func(result exactSessionLifecycleStatusResult) {
					cr.recordExactSessionLifecycleStatusApplied(snapshot.Config, result)
					cr.recordExactSessionLifecycleStatusShadow(snapshot.Config, result)
				}))
			}
			statusWriter, _, statusWriterErr := beads.ResolveConditionalWriter(snapshot.Store)
			owner, reconcileErr := reconcileExactSessionStartWithOwner(reconcileCtx, admission, exactSessionStartParams{
				Generation:        snapshot.Generation,
				CityPath:          snapshot.CityPath,
				CityName:          snapshot.CityName,
				Config:            snapshot.Config,
				Provider:          snapshot.Provider,
				Store:             snapshot.Store,
				StatusWriter:      statusWriter,
				StatusWriterError: statusWriterErr,
				Recorder:          snapshot.Recorder,
				Stdout:            cr.sessionStartStdout(),
				Stderr:            cr.sessionStartStderr(),
				StartOptions:      startOptions,
				AsyncStopTracker:  &cr.asyncStops,
				AsyncStopCompletion: func(completion drainAckAsyncStopCompletion) {
					release()
					cr.sessionStartMu.Lock()
					activeController := cr.sessionStartController
					cr.sessionStartMu.Unlock()
					if completion == drainAckAsyncStopYielded {
						if admission.PoolDrainAck != nil && activeController != nil && activeController.YieldPoolDrainAck(*admission.PoolDrainAck) {
							cr.requestLegacySessionStartFallback()
						}
						return
					}
					if completion == drainAckAsyncStopParked {
						if admission.PoolDrainAck != nil && activeController != nil {
							if _, err := activeController.AdmitPoolDrainAck(*admission.PoolDrainAck); err != nil {
								fmt.Fprintf(cr.sessionStartStderr(), "%s: retaining parked drain-ack stop for %s: %v\n", cr.sessionStartLogPrefix(), admission.SessionID, err) //nolint:errcheck
							}
						} else {
							cr.admitDrainAckStopCompletion(admission.SessionID)
						}
						return
					}
					cr.admitDrainAckStopCompletion(admission.SessionID)
				},
				AsyncStopQueued: func() {
					leaseTransferred = true
				},
				RolloutMode:              mode,
				RigStores:                cr.rigBeadStores(),
				DrainOps:                 cr.dops,
				DrainTracker:             cr.sessionDrains,
				IdleTracker:              cr.it,
				MaxSessionAgeTracker:     cr.mat,
				AssignedWorkDeferTracker: cr.adt,
				Trace:                    cr.trace,
				AuthorizePoolStart: func(authorizeCtx context.Context, info sessionpkg.Info, lease routedWorkPoolStartLease) (bool, error) {
					return cr.authorizeRoutedWorkPoolStart(authorizeCtx, snapshot, info, lease)
				},
				AuthorizePoolDrainAck: func(info sessionpkg.Info, lease routedWorkPoolDrainAckLease) (bool, error) {
					return cr.authorizeRoutedWorkPoolDrainAck(snapshot, info, lease)
				},
				RecoverPoolDrainAck: func(info sessionpkg.Info) (routedWorkPoolDrainAckLease, bool, bool, error) {
					return cr.recoverRoutedWorkPoolDrainAckLease(snapshot, info)
				},
				ValidateWaitDependencyPoolWitness: func(info sessionpkg.Info, lease sessionWaitDependencyStartLease) bool {
					return cr.sessionWaitDependencyPoolWitnessCurrent(snapshot, info, lease)
				},
				ValidateConfiguredDependencyStart: func(info sessionpkg.Info, lease configuredDependencyStartLease) bool {
					return cr.configuredDependencyStartWitnessCurrent(snapshot, info, lease)
				},
				EnterConfiguredDependencyStart: func(lease configuredDependencyStartLease) bool {
					return controller.enterConfiguredDependencyStart(lease)
				},
				ValidateStrictDefaultPoolWakeStart: func(info sessionpkg.Info, lease strictDefaultPoolWakeStartLease) bool {
					return cr.strictDefaultPoolWakeStartWitnessCurrent(snapshot, info, lease)
				},
				EnterStrictDefaultPoolWakeStart: func(lease strictDefaultPoolWakeStartLease) bool {
					return controller.enterStrictDefaultPoolWakeStart(lease)
				},
				ValidateConfiguredNamedWakeStart: func(info sessionpkg.Info, lease configuredNamedWakeStartLease) bool {
					return cr.configuredNamedWakeStartWitnessCurrent(snapshot, info, lease)
				},
				EnterConfiguredNamedWakeStart: func(lease configuredNamedWakeStartLease) bool {
					return controller.enterConfiguredNamedWakeStart(lease)
				},
			})
			if reconcileErr == nil && owner == exactSessionStartLegacyOwner {
				return errSessionStartLegacyFallbackRequired
			}
			return reconcileErr
		},
		Observer: func(result sessionStartReconcileResult) {
			if result.Outcome == sessionStartReconcileRetrying &&
				(result.Admission.PoolDrainAck != nil || result.Admission.PoolDrainAckUncertain) &&
				result.Err != nil && result.Err.Error() != errSessionStartPoolDrainAckPending.Error() {
				fmt.Fprintf(cr.sessionStartStderr(), "%s: session-start drain-ack reconciliation retrying for %s: %v\n", cr.sessionStartLogPrefix(), result.Admission.SessionID, result.Err) //nolint:errcheck // non-exhausting safety retries must retain their cause
			}
			if result.Outcome == sessionStartReconcileSucceeded && result.LegacyFallback {
				if result.Err != nil {
					fmt.Fprintf(cr.sessionStartStderr(), "%s: exact session reconciliation yielded %s to priority legacy fallback: %v\n", cr.sessionStartLogPrefix(), result.Admission.SessionID, result.Err) //nolint:errcheck // fallback cause must remain visible
				}
				if result.Admission.PoolAllocation != nil {
					cr.requestReadyRoutedWorkLegacyFallback()
				} else {
					cr.requestLegacySessionStartFallback()
				}
			}
			if result.Outcome == sessionStartReconcileSucceeded {
				// A queued nudge can arrive while this exact session is still
				// starting. Once lifecycle work completes, re-poke the nudge
				// dispatcher; it rereads durable queue authority before any effect.
				cr.signalNudgeKeyWake()
			}
			if result.Outcome == sessionStartReconcileExhausted {
				fmt.Fprintf(cr.sessionStartStderr(), "%s: session-start reconciliation exhausted for %s: %v; authoritative audit requested\n", cr.sessionStartLogPrefix(), result.Admission.SessionID, result.Err) //nolint:errcheck // terminal retry diagnostic
				if result.Admission.PoolAllocation != nil {
					cr.requestReadyRoutedWorkLegacyFallback()
				} else if result.Admission.PoolDrainAck != nil && mode == rollout.Auto {
					cr.requestLegacySessionStartFallback()
				}
			}
		},
		Stderr: cr.sessionStartStderr(),
	})
	if err != nil {
		return cr.sessionStartActivationFailure(mode, fmt.Errorf("creating child: %w", err))
	}
	if err := controller.Start(ctx); err != nil {
		controller.Stop()
		return cr.sessionStartActivationFailure(mode, fmt.Errorf("starting child: %w", err))
	}
	admit := func(id string) {
		outcome, admitErr := controller.Admit(id, sessionStartAdmissionInProcess)
		cr.refreshPoolMembershipSession(id)
		if admitErr != nil {
			fmt.Fprintf(cr.sessionStartStderr(), "%s: admitting session-start event for %s: %v\n", cr.sessionStartLogPrefix(), id, admitErr) //nolint:errcheck // admission failure is recoverable via audit
			return
		}
		if outcome == sessionStartAdmissionOverflow {
			fmt.Fprintf(cr.sessionStartStderr(), "%s: session-start admission overflow for %s; authoritative audit requested\n", cr.sessionStartLogPrefix(), id) //nolint:errcheck // bounded queue overflow must be visible
			// This callback is drained before its captured controller stops. Seed
			// that controller directly so shutdown need not release its ownership
			// fence while it waits for admitted callbacks.
			if err := cr.seedSessionStartController(controller, cr.loadSessionBeadSnapshot()); err != nil {
				fmt.Fprintf(cr.sessionStartStderr(), "%s: session-start authoritative audit: %v\n", cr.sessionStartLogPrefix(), err) //nolint:errcheck // audit failure remains level-triggered
			}
			// The eager seed consumes auditPending. Re-arm it so the next full
			// reconciliation still verifies the authoritative snapshot.
			controller.RequestAudit()
			cr.requestLegacySessionStartFallback()
		}
	}
	if err := cr.cs.installSessionStartEventAdmission(admit); err != nil {
		controller.Stop()
		if mode == rollout.Require {
			cr.sessionStartOwnership = sessionStartOwnershipRequiredBlocked
		}
		// An already-installed callback means another keyed admission owner may
		// exist. Auto cannot safely enable legacy in that ambiguous state.
		return fmt.Errorf("installing session-start event admission: %w", err)
	}
	if err := cr.seedSessionStartController(controller, seed); err != nil {
		cr.cs.stopSessionStartEventAdmission()
		controller.Stop()
		return cr.sessionStartActivationFailure(mode, fmt.Errorf("seeding child: %w", err))
	}

	cr.sessionStartController = controller
	cr.sessionStartOwnership = sessionStartOwnershipKeyed
	return nil
}

// admitDrainAckStopCompletion sends confirmed or retryable stop completion
// through the current keyed owner. The durable stop marker, not the completed
// controller instance, is the recovery record across a provider reload.
func (cr *CityRuntime) admitDrainAckStopCompletion(id string) {
	if cr == nil {
		return
	}
	cr.sessionStartMu.Lock()
	controller := cr.sessionStartController
	owned := cr.sessionStartOwnership == sessionStartOwnershipKeyed
	cr.sessionStartMu.Unlock()
	if !owned || controller == nil {
		cr.requestLegacySessionStartFallback()
		return
	}
	outcome, err := controller.Admit(id, sessionStartAdmissionInProcess)
	if err != nil {
		fmt.Fprintf(cr.sessionStartStderr(), "%s: admitting drain-ack stop completion for %s: %v\n", cr.sessionStartLogPrefix(), id, err) //nolint:errcheck // durable audit remains recovery path
		cr.seedActiveSessionStartController(cr.loadSessionBeadSnapshot())
		controller.RequestAudit()
		return
	}
	if outcome == sessionStartAdmissionOverflow {
		cr.seedActiveSessionStartController(cr.loadSessionBeadSnapshot())
		controller.RequestAudit()
	}
}

func withAdditionalExactSessionLifecycleStatusObserver(observer exactSessionLifecycleStatusObserver) startExecutionOption {
	return func(opts *startExecutionOptions) {
		existing := opts.exactStatusObserver
		opts.exactStatusObserver = func(result exactSessionLifecycleStatusResult) {
			if observer != nil {
				observer(result)
			}
			if existing != nil {
				existing(result)
			}
		}
	}
}

func (cr *CityRuntime) recordExactSessionLifecycleStatusApplied(cfg *config.City, result exactSessionLifecycleStatusResult) {
	if cr == nil || cr.trace == nil ||
		(result.Admission.Source != sessionStartAdmissionInProcess && result.Admission.Source != sessionStartAdmissionSocket) ||
		!result.RuntimeLive || result.Disposition != exactSessionLifecycleStatusDispositionCandidate || result.Plan == nil ||
		result.Plan.Outcome != sessionLifecycleStatusHeal || !result.EffectApplied {
		return
	}
	admittedAt := result.Admission.AdmittedAt
	observedAt := result.ObservedAt
	if admittedAt.IsZero() || observedAt.IsZero() || observedAt.Before(admittedAt) {
		return
	}
	trace := cr.trace
	cycle := trace.BeginCycle(TraceTickTriggerControl, "session_lifecycle_status_heal", admittedAt, cfg)
	if cycle == nil {
		return
	}
	cycle.RecordMutation(TraceSiteMutationBeadMetadata, TraceReasonUnknown, TraceOutcomeApplied, "session", result.RequestedID, "update_if_match", map[string]any{
		"session_id":        result.RequestedID,
		"admission":         string(result.Admission.Source),
		"admission_version": result.AdmissionVersion,
		"generation":        result.ControllerGeneration,
		"status_outcome":    exactSessionLifecycleStatusOutcomeTraceValue(result.Plan.Outcome),
		"status_reason":     string(result.Plan.Reason),
		"effect_applied":    true,
	})
	if err := cycle.End(TraceCompletionCompleted, nil); err != nil {
		fmt.Fprintf(cr.sessionStartStderr(), "%s: session lifecycle status heal trace: %v\n", cr.sessionStartLogPrefix(), err) //nolint:errcheck // tracing must not affect reconciliation
	}
}

func (cr *CityRuntime) recordExactSessionLifecycleStatusShadow(cfg *config.City, result exactSessionLifecycleStatusResult) {
	if cr == nil || cr.trace == nil ||
		(result.Admission.Source != sessionStartAdmissionInProcess && result.Admission.Source != sessionStartAdmissionSocket) ||
		!result.RuntimeLive || result.Disposition != exactSessionLifecycleStatusDispositionCandidate || result.Plan == nil ||
		result.Plan.Outcome != sessionLifecycleStatusNoop || result.Plan.Reason != sessionLifecycleStatusReasonConverged || result.EffectApplied {
		return
	}
	admittedAt := result.Admission.AdmittedAt
	observedAt := result.ObservedAt
	if admittedAt.IsZero() || observedAt.IsZero() || observedAt.Before(admittedAt) {
		return
	}
	trace := cr.trace
	cycle := trace.BeginCycle(TraceTickTriggerControl, "session_lifecycle_status_shadow", admittedAt, cfg)
	if cycle == nil {
		return
	}
	cycle.RecordControllerOperation(TraceSiteLifecycleStatusShadow, TraceReasonRetained, TraceOutcomeNoChange, "session_lifecycle_status_shadow", observedAt.Sub(admittedAt), map[string]any{
		"session_id":        result.RequestedID,
		"admission":         string(result.Admission.Source),
		"admission_version": result.AdmissionVersion,
		"generation":        result.ControllerGeneration,
		"status_outcome":    exactSessionLifecycleStatusOutcomeTraceValue(result.Plan.Outcome),
		"status_reason":     string(result.Plan.Reason),
		"effect_applied":    false,
	})
	if err := cycle.End(TraceCompletionCompleted, nil); err != nil {
		fmt.Fprintf(cr.sessionStartStderr(), "%s: session lifecycle status shadow trace: %v\n", cr.sessionStartLogPrefix(), err) //nolint:errcheck // tracing must not affect reconciliation
	}
}

func exactSessionLifecycleStatusOutcomeTraceValue(outcome sessionLifecycleStatusOutcome) string {
	switch outcome {
	case sessionLifecycleStatusNoop:
		return "noop"
	case sessionLifecycleStatusHeal:
		return "heal"
	case sessionLifecycleStatusPark:
		return "park"
	default:
		return "unknown"
	}
}

func (cr *CityRuntime) sessionStartActivationFailure(mode rollout.Mode, err error) error {
	if mode == rollout.Require {
		cr.sessionStartOwnership = sessionStartOwnershipRequiredBlocked
		return err
	}
	cr.sessionStartOwnership = sessionStartOwnershipLegacy
	fmt.Fprintf(cr.sessionStartStderr(), "%s: session-start controller failed (%v); falling back to legacy reconciliation\n", cr.sessionStartLogPrefix(), err) //nolint:errcheck // auto degradation must be loud
	return nil
}

func (cr *CityRuntime) seedSessionStartController(controller *sessionStartController, snapshot *sessionBeadSnapshot) error {
	if controller == nil {
		return fmt.Errorf("controller is nil")
	}
	if snapshot == nil {
		return fmt.Errorf("session snapshot is nil")
	}
	if err := snapshot.LoadError(); err != nil {
		return fmt.Errorf("session snapshot is incomplete: %w", err)
	}
	stateSnapshot, err := cr.cs.sessionStartSnapshot()
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	cursor := 0
	var page []sessionpkg.Info
	return controller.StartAuthoritativeSeed(func(ctx context.Context) sessionStartAuthoritativeSeedResult {
		for {
			if ctx.Err() != nil {
				return sessionStartAuthoritativeSeedResult{Err: ctx.Err()}
			}
			if len(page) == 0 {
				page, cursor = snapshot.openInfoPage(cursor, sessionStartSeedPageSize)
				if len(page) == 0 {
					return sessionStartAuthoritativeSeedResult{Complete: true}
				}
			}
			info := page[0]
			page = page[1:]
			if validateSessionStartAdmission(info.ID, sessionStartAdmissionAntiEntropy) != nil ||
				!resolveExactSessionStartOrDrainAckStopOwnership(info, stateSnapshot.Config, now) {
				continue
			}
			if isDrainAckStopPendingInfo(info) {
				lease, agentDrainAck, legacyMarker, leaseErr := cr.recoverRoutedWorkPoolDrainAckLease(stateSnapshot, info)
				if leaseErr != nil {
					return sessionStartAuthoritativeSeedResult{
						SessionID:             info.ID,
						PoolDrainAckUncertain: true,
					}
				}
				if !agentDrainAck && legacyMarker {
					// A definitely non-agent acknowledgement is legacy-owned. Do
					// not manufacture a keyed STOP admission for it.
					continue
				}
				if !agentDrainAck {
					return sessionStartAuthoritativeSeedResult{SessionID: info.ID, PoolDrainAckUncertain: true}
				}
				return sessionStartAuthoritativeSeedResult{SessionID: info.ID, PoolDrainAck: &lease}
			}
			return sessionStartAuthoritativeSeedResult{SessionID: info.ID}
		}
	})
}

func (cr *CityRuntime) seedActiveSessionStartController(snapshot *sessionBeadSnapshot) {
	if cr == nil {
		return
	}
	cr.sessionStartMu.Lock()
	controller := cr.sessionStartController
	active := cr.sessionStartOwnership == sessionStartOwnershipKeyed
	cr.sessionStartMu.Unlock()
	if !active || controller == nil {
		return
	}
	// Every legacy full tick already carries an authoritative session snapshot.
	// Reusing it here adds no store enumeration and permanently recovers missed
	// event hooks, queue loss, overflow, and retry exhaustion. TakeAuditRequest
	// clears the urgent bit; the periodic seed remains unconditional.
	controller.TakeAuditRequest()
	if err := cr.seedSessionStartController(controller, snapshot); err != nil {
		controller.RequestAudit()
		fmt.Fprintf(cr.sessionStartStderr(), "%s: session-start authoritative audit: %v\n", cr.sessionStartLogPrefix(), err) //nolint:errcheck // audit failure remains level-triggered
	}
}

func (cr *CityRuntime) stopSessionStartController() {
	if cr == nil {
		return
	}
	cr.sessionStartMu.Lock()
	defer cr.sessionStartMu.Unlock()
	controller := cr.sessionStartController
	if cr.cs != nil {
		cr.cs.stopSessionStartEventAdmission()
	}
	if controller != nil {
		controller.Stop()
	}
	cr.sessionStartController = nil
	if cr.sessionStartMode == rollout.Require {
		cr.sessionStartOwnership = sessionStartOwnershipRequiredBlocked
	} else {
		cr.sessionStartOwnership = sessionStartOwnershipLegacy
	}
}

func (cr *CityRuntime) restartSessionStartController(ctx context.Context) error {
	if cr == nil {
		return fmt.Errorf("city runtime is nil")
	}
	return cr.ensureSessionStartController(ctx, cr.loadSessionBeadSnapshot())
}

func (cr *CityRuntime) sessionStartOwnershipState() sessionStartOwnership {
	if cr == nil {
		return sessionStartOwnershipLegacy
	}
	cr.sessionStartMu.Lock()
	defer cr.sessionStartMu.Unlock()
	return cr.sessionStartOwnership
}

// sessionStartSocketFallback records why the exact socket handoff yielded to
// the established legacy poke path. It deliberately does not change the reply
// or admission semantics.
func (cr *CityRuntime) sessionStartSocketFallback(sessionID, reason string) sessionStartSocketReply {
	fmt.Fprintf(cr.sessionStartStderr(), "%s: exact session-start socket fallback for %s: %s\n", cr.sessionStartLogPrefix(), sessionID, reason) //nolint:errcheck // fallback diagnostics must not affect admission
	return sessionStartSocketReplyFallback
}

func (cr *CityRuntime) configuredDependencyStartWitnessCurrent(
	snapshot controllerSessionStartSnapshot,
	info sessionpkg.Info,
	lease configuredDependencyStartLease,
) bool {
	if cr == nil || snapshot.Config == nil || snapshot.Provider == nil || snapshot.Store == nil ||
		validateConfiguredDependencyStartLease(lease) != nil {
		return false
	}
	cr.serviceStateMu.RLock()
	configCurrent := cr.cfg == snapshot.Config
	cr.serviceStateMu.RUnlock()
	return configCurrent && snapshot.Generation == lease.ControllerGeneration &&
		configuredDependencyStartTargetMatches(info, snapshot.Config, lease) &&
		configuredDependencyStartDependencyMatches(snapshot.Store, snapshot.Config, snapshot.CityName, lease) &&
		allDependenciesAliveForTemplateWithClock(
			lease.TargetTemplate, snapshot.Config, nil, snapshot.Provider, snapshot.CityName, snapshot.Store, clock.Real{},
		)
}

func (cr *CityRuntime) strictDefaultPoolWakeStartWitnessCurrent(
	snapshot controllerSessionStartSnapshot,
	info sessionpkg.Info,
	lease strictDefaultPoolWakeStartLease,
) bool {
	if cr == nil || snapshot.Config == nil || snapshot.Provider == nil || snapshot.Store == nil ||
		validateStrictDefaultPoolWakeStartLease(lease) != nil {
		return false
	}
	cr.serviceStateMu.RLock()
	configCurrent := cr.cfg == snapshot.Config
	cr.serviceStateMu.RUnlock()
	return configCurrent && snapshot.Generation == lease.ControllerGeneration &&
		strictDefaultPoolWakeIdentityMatches(info, snapshot.Config, lease)
}

func (cr *CityRuntime) configuredNamedWakeStartWitnessCurrent(
	snapshot controllerSessionStartSnapshot,
	info sessionpkg.Info,
	lease configuredNamedWakeStartLease,
) bool {
	if cr == nil || snapshot.Config == nil || snapshot.Provider == nil || snapshot.Store == nil ||
		validateConfiguredNamedWakeStartLease(lease) != nil {
		return false
	}
	cr.serviceStateMu.RLock()
	configCurrent := cr.cfg == snapshot.Config
	cr.serviceStateMu.RUnlock()
	return configCurrent && snapshot.Generation == lease.ControllerGeneration &&
		configuredNamedWakeIdentityMatches(info, snapshot.Config, snapshot.CityName, lease)
}

func (cr *CityRuntime) admitSessionStartSocketKey(sessionID string) sessionStartSocketReply {
	if cr == nil {
		return cr.sessionStartSocketFallback(sessionID, "controller runtime is nil")
	}
	if err := validateSessionStartAdmission(sessionID, sessionStartAdmissionSocket); err != nil {
		return sessionStartSocketReplyInvalid
	}

	cr.sessionStartMu.Lock()
	controller := cr.sessionStartController
	owned := cr.sessionStartOwnership == sessionStartOwnershipKeyed
	mode := cr.sessionStartMode
	cr.sessionStartMu.Unlock()
	if !owned || controller == nil {
		if mode == rollout.Require {
			return sessionStartSocketReplyBlocked
		}
		return cr.sessionStartSocketFallback(sessionID, "controller unavailable or not keyed")
	}
	snapshot, release, err := cr.cs.acquireSessionStartSnapshot()
	if err != nil {
		if mode == rollout.Require {
			return sessionStartSocketReplyBlocked
		}
		return cr.sessionStartSocketFallback(sessionID, fmt.Sprintf("acquiring controller snapshot: %v", err))
	}
	defer release()
	info, revision, err := getAuthoritativeSessionStartRecord(snapshot.Store, sessionID)
	if err != nil {
		if mode == rollout.Require {
			return sessionStartSocketReplyBlocked
		}
		return cr.sessionStartSocketFallback(sessionID, fmt.Sprintf("reading authoritative session row: %v", err))
	}
	lease, agentDrainAck, leaseErr := cr.newRoutedWorkPoolDrainAckLease(snapshot, info)
	if leaseErr != nil {
		if mode == rollout.Require {
			fmt.Fprintf(cr.sessionStartStderr(), "%s: admitting exact pool drain acknowledgement for %s: %v; required path refused closed\n", cr.sessionStartLogPrefix(), sessionID, leaseErr) //nolint:errcheck // required refusal must remain visible
			return sessionStartSocketReplyBlocked
		}
		fmt.Fprintf(cr.sessionStartStderr(), "%s: admitting exact pool drain acknowledgement for %s: %v; priority legacy fallback requested\n", cr.sessionStartLogPrefix(), sessionID, leaseErr) //nolint:errcheck // admission uncertainty must remain visible
		return cr.sessionStartSocketFallback(sessionID, "pool drain acknowledgement admission uncertainty")
	}
	if agentDrainAck {
		outcome, admitErr := controller.AdmitPoolDrainAck(lease)
		if admitErr != nil || outcome == sessionStartAdmissionOverflow {
			if mode == rollout.Require {
				fmt.Fprintf(cr.sessionStartStderr(), "%s: admitting exact pool drain acknowledgement for %s: outcome=%s err=%v; required path refused closed\n", cr.sessionStartLogPrefix(), sessionID, outcome, admitErr) //nolint:errcheck // required refusal must remain visible
				return sessionStartSocketReplyBlocked
			}
			fmt.Fprintf(cr.sessionStartStderr(), "%s: admitting exact pool drain acknowledgement for %s: outcome=%s err=%v; priority legacy fallback requested\n", cr.sessionStartLogPrefix(), sessionID, outcome, admitErr) //nolint:errcheck // queue rejection must remain visible
			return cr.sessionStartSocketFallback(sessionID, "pool drain acknowledgement admission rejected")
		}
		return sessionStartSocketReplyOK
	}
	now := time.Now().UTC()
	if exactUserHoldSuspendCurrent(info, now) && !controller.ownsPoolDrainAckStop(info.ID, info.InstanceToken) {
		outcome, admitErr := controller.Admit(sessionID, sessionStartAdmissionSocket)
		if admitErr == nil && outcome != sessionStartAdmissionOverflow {
			return sessionStartSocketReplyOK
		}
		if mode == rollout.Require {
			return sessionStartSocketReplyBlocked
		}
		return cr.sessionStartSocketFallback(sessionID, fmt.Sprintf("exact suspend admission rejected (outcome=%s err=%v)", outcome, admitErr))
	}
	if revision != 0 && exactOrdinaryResetCurrent(info, snapshot.Config, now) &&
		!controller.ownsPoolDrainAckStop(info.ID, info.InstanceToken) {
		outcome, admitErr := controller.Admit(sessionID, sessionStartAdmissionSocket)
		if admitErr == nil && outcome != sessionStartAdmissionOverflow {
			return sessionStartSocketReplyOK
		}
		if mode == rollout.Require {
			return sessionStartSocketReplyBlocked
		}
		return cr.sessionStartSocketFallback(sessionID, fmt.Sprintf("exact reset admission rejected (outcome=%s err=%v)", outcome, admitErr))
	}
	_, _, owner := classifyExactSessionStartOwnership(info, snapshot.Config, now)
	if owner != exactSessionStartKeyedOwner {
		if lease, certified := certifyConfiguredNamedWakeStartLease(info, revision, snapshot.Config, snapshot.CityName, snapshot.Generation, now); certified {
			outcome, admitErr := controller.AdmitConfiguredNamedWake(lease)
			if admitErr == nil && outcome != sessionStartAdmissionOverflow {
				return sessionStartSocketReplyOK
			}
			if mode == rollout.Require {
				return sessionStartSocketReplyBlocked
			}
			return cr.sessionStartSocketFallback(sessionID, fmt.Sprintf("configured named wake admission rejected (outcome=%s err=%v)", outcome, admitErr))
		}
		if lease, certified := certifyStrictDefaultPoolWakeStartLease(info, revision, snapshot.Config, snapshot.Generation, now); certified {
			outcome, admitErr := controller.AdmitStrictDefaultPoolWake(lease)
			if admitErr == nil && outcome != sessionStartAdmissionOverflow {
				return sessionStartSocketReplyOK
			}
			if mode == rollout.Require {
				return sessionStartSocketReplyBlocked
			}
			return cr.sessionStartSocketFallback(sessionID, fmt.Sprintf("strict-default pool wake admission rejected (outcome=%s err=%v)", outcome, admitErr))
		}
		if lease, certified := certifyConfiguredDependencyStartLease(info, snapshot.Config, snapshot.Provider, snapshot.CityName, snapshot.Store, snapshot.Generation, now); certified {
			outcome, admitErr := controller.AdmitConfiguredDependency(lease)
			if admitErr == nil && outcome != sessionStartAdmissionOverflow {
				return sessionStartSocketReplyOK
			}
			if mode == rollout.Require {
				return sessionStartSocketReplyBlocked
			}
			return cr.sessionStartSocketFallback(sessionID, fmt.Sprintf("configured-dependency admission rejected (outcome=%s err=%v)", outcome, admitErr))
		}
		if mode == rollout.Require {
			return sessionStartSocketReplyBlocked
		}
		return cr.sessionStartSocketFallback(sessionID, "clean legacy ownership classification")
	}
	outcome, err := controller.Admit(sessionID, sessionStartAdmissionSocket)
	if err != nil || outcome == sessionStartAdmissionOverflow {
		if mode == rollout.Require {
			return sessionStartSocketReplyBlocked
		}
		return cr.sessionStartSocketFallback(sessionID, fmt.Sprintf("exact session-start admission rejected (outcome=%s err=%v)", outcome, err))
	}
	return sessionStartSocketReplyOK
}

// detectorAdmitFunc hands the detector sweep the existing session-start
// controller's Admit entry. It is nil unless keyed ownership is live, so a
// legacy-owned city's sweep stays read-only no matter which detector family has
// flipped to act.
func (cr *CityRuntime) detectorAdmitFunc() func(string, sessionStartAdmissionSource) (sessionStartAdmissionOutcome, error) {
	if cr == nil {
		return nil
	}
	cr.sessionStartMu.Lock()
	controller := cr.sessionStartController
	owned := cr.sessionStartOwnership == sessionStartOwnershipKeyed
	cr.sessionStartMu.Unlock()
	if !owned || controller == nil {
		return nil
	}
	return controller.Admit
}

func (cr *CityRuntime) requestLegacySessionStartFallback() {
	if cr == nil {
		return
	}
	if cr.pokeCh != nil {
		select {
		case cr.pokeCh <- struct{}{}:
		default:
		}
		return
	}
	if cr.cs != nil {
		cr.cs.Poke()
	}
}

func (cr *CityRuntime) sessionStartLegacyExclusionOption() startExecutionOption {
	cr.sessionStartMu.Lock()
	state := cr.sessionStartOwnership
	controller := cr.sessionStartController
	cr.sessionStartMu.Unlock()

	// The detector-family yields are deliberately NOT folded into the start
	// predicate: that one answers "does keyed own this row's START family",
	// which is true for rows legacy must stay free to idle-kill and false for
	// the lifecycle-terminal rows a stale create leaves behind. Each family's
	// bridge answers its own narrow question — is an effect for this exact key
	// in flight right now — and they are installed whenever a controller
	// exists, including the bounded handoff windows where the start predicate
	// stands down, because an admitted key outlives those windows.
	var familyOptions []startExecutionOption
	if controller != nil {
		familyOptions = append(familyOptions,
			withLegacyDeadlineStopExclusion(func(info sessionpkg.Info) bool {
				return controller.ownsDeadlineStop(info.ID)
			}),
			withLegacyStaleCreateRollbackExclusion(func(info sessionpkg.Info) bool {
				return controller.ownsStaleCreateRollback(info.ID)
			}),
		)
	}
	familyOption := combineStartExecutionOptions(familyOptions...)
	excluded := cr.sessionStartLegacyExclusionPredicate()
	if excluded == nil {
		return familyOption
	}
	startOption := combineStartExecutionOptions(withLegacyStartExclusion(excluded), familyOption)
	if state != sessionStartOwnershipKeyed {
		return startOption
	}
	snapshot, err := cr.cs.sessionStartSnapshot()
	if err != nil {
		return startOption
	}
	statusWriter, _, statusWriterErr := beads.ResolveConditionalWriter(snapshot.Store)
	if statusWriter == nil && statusWriterErr == nil {
		return startOption
	}
	statusOption := withLegacyStatusHealExclusion(func(info sessionpkg.Info) bool {
		return validateSessionStartAdmission(info.ID, sessionStartAdmissionInProcess) == nil &&
			(resolveExactSessionStartOwnership(info, snapshot.Config, time.Now().UTC()) ||
				(controller != nil && (controller.ownsPoolAllocationStart(info.ID, info.InstanceToken) ||
					controller.ownsConfiguredDependencyStart(info.ID) ||
					controller.ownsStrictDefaultPoolWakeStart(info.ID) ||
					controller.ownsConfiguredNamedWakeStart(info.ID))))
	})
	return combineStartExecutionOptions(startOption, statusOption)
}

// sessionStartLegacyExclusionPredicate is the single ownership predicate used
// by legacy start and drain-ack stop entry points while keyed reconciliation is
// active. Keyed reconciliation owns drain-ack finalization when the provider
// can produce a fresh observation; Auto otherwise leaves it to legacy.
func (cr *CityRuntime) sessionStartLegacyExclusionPredicate() func(sessionpkg.Info) bool {
	if cr == nil {
		return nil
	}
	cr.sessionStartMu.Lock()
	state := cr.sessionStartOwnership
	mode := cr.sessionStartMode
	controller := cr.sessionStartController
	cr.sessionStartMu.Unlock()
	if state != sessionStartOwnershipKeyed && state != sessionStartOwnershipRequiredBlocked {
		return nil
	}
	if state == sessionStartOwnershipKeyed && mode == rollout.Auto && cr.cs != nil && cr.cs.configMutationPending.Load() {
		// updateWithPendingConfigMutation only sets this marker after the
		// generation fence has drained old keyed work. New keyed snapshots fail
		// closed until the runtime loop applies the same revision, so legacy is
		// the sole available owner during this bounded handoff.
		return nil
	}
	return func(info sessionpkg.Info) bool {
		if validateSessionStartAdmission(info.ID, sessionStartAdmissionInProcess) != nil {
			return false
		}
		if cr.ownsSessionWaitDependencyStart(info.ID) {
			return true
		}
		if controller != nil && controller.ownsConfiguredDependencyStart(info.ID) {
			return true
		}
		if controller != nil && controller.ownsStrictDefaultPoolWakeStart(info.ID) {
			return true
		}
		if controller != nil && controller.ownsConfiguredNamedWakeStart(info.ID) {
			return true
		}
		if controller != nil && controller.ownsPoolDrainAckStop(info.ID, info.InstanceToken) {
			return true
		}
		snapshot, err := cr.cs.sessionStartSnapshot()
		if err != nil {
			if mode == rollout.Require {
				return !info.Closed
			}
			// Once ownership has transferred, an incoherent state generation must
			// stall its start family rather than let both writers enter.
			input := sessionpkg.LifecycleInputFromInfo(info)
			input.Now = time.Now().UTC()
			input.CreatedAt = info.CreatedAt
			input.StaleCreatingAfter = staleCreatingStateTimeout
			lifecycle := sessionpkg.ProjectLifecycle(input)
			return !info.Closed && (!lifecycle.Terminal || isDrainAckStopPendingInfo(info)) &&
				(isDrainAckStopPendingInfo(info) || lifecycle.HasWakeCause(sessionpkg.WakeCausePendingCreate) || lifecycle.HasWakeCause(sessionpkg.WakeCauseExplicit))
		}
		if mode == rollout.Require {
			name := strings.TrimSpace(info.SessionNameMetadata)
			if name != "" {
				source, sourceErr := snapshot.Provider.GetMeta(name, reconcilerDrainAckSourceKey)
				if sourceErr != nil || source == drainAckSourceAgentValue {
					return !info.Closed
				}
			}
		}
		if isDrainAckStopPendingInfo(info) {
			name := strings.TrimSpace(info.SessionNameMetadata)
			if name == "" {
				return true
			}
			source, sourceErr := snapshot.Provider.GetMeta(name, reconcilerDrainAckSourceKey)
			if sourceErr == nil && source == reconcilerDrainAckSourceValue {
				return false
			}
			// Agent, missing, or unreadable provenance is never a reason to
			// let legacy enter the destructive stop path.
			return true
		}
		if controller != nil && controller.ownsPoolAllocationStart(info.ID, info.InstanceToken) {
			return true
		}
		return resolveExactSessionStartOrDrainAckStopOwnership(info, snapshot.Config, time.Now().UTC())
	}
}

func (cr *CityRuntime) sessionStartStdout() io.Writer {
	if cr != nil && cr.stdout != nil {
		return cr.stdout
	}
	return io.Discard
}

func (cr *CityRuntime) sessionStartStderr() io.Writer {
	if cr != nil && cr.stderr != nil {
		return cr.stderr
	}
	return io.Discard
}

func (cr *CityRuntime) sessionStartLogPrefix() string {
	if cr != nil && cr.logPrefix != "" {
		return cr.logPrefix
	}
	return "gc controller"
}
