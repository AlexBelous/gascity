package main

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/gastownhall/gascity/internal/rollout"
	sessionpkg "github.com/gastownhall/gascity/internal/session"
)

const (
	sessionStartControllerMaxDistinct = 4096
	sessionStartControllerMaxRetries  = 5
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
		mode = cr.cs.RolloutFlags().SessionStartReconciler()
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
	controller, err := newCitySessionStartController(sessionStartControllerOptions{
		Workers:     workers,
		MaxDistinct: sessionStartControllerMaxDistinct,
		MaxRetries:  sessionStartControllerMaxRetries,
		Reconcile: func(reconcileCtx context.Context, admission sessionStartAdmission) error {
			snapshot, release, acquireErr := cr.cs.acquireSessionStartSnapshot()
			if acquireErr != nil {
				return acquireErr
			}
			defer release()
			owner, reconcileErr := reconcileExactSessionStartWithOwner(reconcileCtx, admission, exactSessionStartParams{
				CityPath:     snapshot.CityPath,
				CityName:     snapshot.CityName,
				Config:       snapshot.Config,
				Provider:     snapshot.Provider,
				Store:        snapshot.Store,
				Recorder:     snapshot.Recorder,
				Stdout:       cr.sessionStartStdout(),
				Stderr:       cr.sessionStartStderr(),
				StartOptions: cr.sessionStartOptions,
			})
			if reconcileErr == nil && owner == exactSessionStartLegacyOwner {
				cr.requestLegacySessionStartFallback()
			}
			return reconcileErr
		},
		Observer: func(result sessionStartReconcileResult) {
			if result.Outcome == sessionStartReconcileExhausted {
				fmt.Fprintf(cr.sessionStartStderr(), "%s: session-start reconciliation exhausted for %s: %v; authoritative audit requested\n", cr.sessionStartLogPrefix(), result.Admission.SessionID, result.Err) //nolint:errcheck // terminal retry diagnostic
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
		if admitErr != nil {
			fmt.Fprintf(cr.sessionStartStderr(), "%s: admitting session-start event for %s: %v\n", cr.sessionStartLogPrefix(), id, admitErr) //nolint:errcheck // admission failure is recoverable via audit
			return
		}
		if outcome == sessionStartAdmissionOverflow {
			fmt.Fprintf(cr.sessionStartStderr(), "%s: session-start admission overflow for %s; authoritative audit requested\n", cr.sessionStartLogPrefix(), id) //nolint:errcheck // bounded queue overflow must be visible
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
	for _, info := range snapshot.OpenInfos() {
		if validateSessionStartAdmission(info.ID, sessionStartAdmissionAntiEntropy) != nil {
			continue
		}
		owned := resolveExactSessionStartOwnership(info, stateSnapshot.Config, now)
		if !owned {
			continue
		}
		if _, err := controller.Admit(info.ID, sessionStartAdmissionAntiEntropy); err != nil {
			return fmt.Errorf("admitting %q from authoritative snapshot: %w", info.ID, err)
		}
	}
	return nil
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

func (cr *CityRuntime) admitSessionStartSocketKey(sessionID string) sessionStartSocketReply {
	if cr == nil {
		return sessionStartSocketReplyFallback
	}
	if err := validateSessionStartAdmission(sessionID, sessionStartAdmissionSocket); err != nil {
		return sessionStartSocketReplyInvalid
	}

	cr.sessionStartMu.Lock()
	controller := cr.sessionStartController
	owned := cr.sessionStartOwnership == sessionStartOwnershipKeyed
	cr.sessionStartMu.Unlock()
	if !owned || controller == nil {
		return sessionStartSocketReplyFallback
	}
	snapshot, release, err := cr.cs.acquireSessionStartSnapshot()
	if err != nil {
		return sessionStartSocketReplyFallback
	}
	defer release()
	owner, err := exactSessionStartOwnerForKey(snapshot.Store, snapshot.Config, sessionID, time.Now().UTC())
	if err != nil || owner != exactSessionStartKeyedOwner {
		return sessionStartSocketReplyFallback
	}
	outcome, err := controller.Admit(sessionID, sessionStartAdmissionSocket)
	if err != nil || outcome == sessionStartAdmissionOverflow {
		return sessionStartSocketReplyFallback
	}
	return sessionStartSocketReplyOK
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
	if cr == nil {
		return nil
	}
	cr.sessionStartMu.Lock()
	state := cr.sessionStartOwnership
	mode := cr.sessionStartMode
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
	return withLegacyStartExclusion(func(info sessionpkg.Info) bool {
		if validateSessionStartAdmission(info.ID, sessionStartAdmissionInProcess) != nil {
			return false
		}
		snapshot, err := cr.cs.sessionStartSnapshot()
		if err != nil {
			// Once ownership has transferred, an incoherent state generation must
			// stall its start family rather than let both writers enter.
			input := sessionpkg.LifecycleInputFromInfo(info)
			input.Now = time.Now().UTC()
			input.CreatedAt = info.CreatedAt
			input.StaleCreatingAfter = staleCreatingStateTimeout
			lifecycle := sessionpkg.ProjectLifecycle(input)
			return !info.Closed && !lifecycle.Terminal &&
				(lifecycle.HasWakeCause(sessionpkg.WakeCausePendingCreate) || lifecycle.HasWakeCause(sessionpkg.WakeCauseExplicit))
		}
		return resolveExactSessionStartOwnership(info, snapshot.Config, time.Now().UTC())
	})
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
