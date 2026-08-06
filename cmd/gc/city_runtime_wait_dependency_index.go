package main

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/clock"
	"github.com/gastownhall/gascity/internal/rollout"
	sessionpkg "github.com/gastownhall/gascity/internal/session"
)

var (
	errSessionWaitDependencyStaleCertification    = errors.New("wait dependency target is no longer certified")
	errSessionWaitDependencySnapshotUnavailable   = errors.New("wait dependency session snapshot is unavailable")
	errSessionWaitDependencyTargetReadUnavailable = errors.New("wait dependency target read is unavailable")
)

// sessionWaitDependencyStartHint is an untrusted, bounded routing hint. It
// contains no authority: the runtime loop certifies the current index and the
// durable wait/session pair before admitting a keyed start.
type sessionWaitDependencyStartHint struct {
	Target sessionWaitDependencyTarget
	Cause  sessionWaitDependencyCause
}

// buildObservedSessionWaitDependencyIndex builds a private candidate from one
// observed census without changing runtime state.
func buildObservedSessionWaitDependencyIndex(store beads.SessionStore) (observedSessionWaitCensus, *sessionWaitDependencyIndex, error) {
	census, err := observeSessionWaitCensus(store)
	if err != nil {
		return census, nil, err
	}
	candidate := newSessionWaitDependencyIndex()
	if err := candidate.Rebuild(census.waits); err != nil {
		return census, nil, fmt.Errorf("building session wait dependency index: %w", err)
	}
	return census, candidate, nil
}

// publishObservedSessionWaitDependencyIndex replaces the runtime's private
// index only while the cache observation that supplied its census is current.
func (cr *CityRuntime) publishObservedSessionWaitDependencyIndex(census observedSessionWaitCensus, candidate *sessionWaitDependencyIndex) (bool, error) {
	if candidate == nil {
		return false, fmt.Errorf("publishing session wait dependency index: candidate is nil")
	}
	return census.cache.WithCurrentObservation(census.observation, func() error {
		cr.sessionWaitDependencyMu.Lock()
		defer cr.sessionWaitDependencyMu.Unlock()
		cr.sessionWaitDependencyIndexGeneration++
		cr.sessionWaitDependencyIndex = candidate
		cr.sessionWaitDependencyRejectedCensusIDs = nil
		return nil
	})
}

func (cr *CityRuntime) installObservedSessionWaitDependencyIndex(store beads.SessionStore) (bool, error) {
	census, candidate, err := buildObservedSessionWaitDependencyIndex(store)
	if err != nil {
		if !errors.Is(err, beads.ErrCacheUnavailable) {
			retained, retainErr := cr.publishRejectedSessionWaitDependencyCensus(census)
			if retainErr != nil {
				return false, retainErr
			}
			if !retained {
				return false, nil
			}
		}
		return false, err
	}
	return cr.publishObservedSessionWaitDependencyIndex(census, candidate)
}

func (cr *CityRuntime) publishRejectedSessionWaitDependencyCensus(census observedSessionWaitCensus) (bool, error) {
	if census.cache == nil {
		return false, fmt.Errorf("publishing rejected session wait census: %w", beads.ErrCacheUnavailable)
	}
	ids := make(map[string]struct{}, len(census.waits))
	for _, wait := range census.waits {
		if wait.ID != "" {
			ids[wait.ID] = struct{}{}
		}
	}
	return census.cache.WithCurrentObservation(census.observation, func() error {
		cr.sessionWaitDependencyMu.Lock()
		defer cr.sessionWaitDependencyMu.Unlock()
		cr.sessionWaitDependencyIndexGeneration++
		cr.sessionWaitDependencyRejectedCensusIDs = ids
		cr.sessionWaitDependencyStartupCensusOwed = true
		return nil
	})
}

// startSessionWaitDependencyShadow arms inert steady-state refresh before
// requesting the initial census. Cache unavailability and stale observations
// remain pending; deterministic census errors wait for a relevant wait change.
func (cr *CityRuntime) startSessionWaitDependencyShadow() {
	cr.startSessionWaitDependencyShadowWithContext(context.Background())
}

func (cr *CityRuntime) startSessionWaitDependencyShadowWithContext(ctx context.Context) {
	cr.enableSessionWaitDependencyLifecycleShadowSink(ctx)
	cr.sessionWaitDependencyMu.Lock()
	cr.sessionWaitDependencyStartupCensusOwed = true
	cr.sessionWaitDependencyMu.Unlock()
	producerStarted := cr.startSessionWaitDependencyProducer()
	armed := false
	if cr.cs != nil {
		var producerAdmission func(sessionWaitDependencyProducerRequest)
		if producerStarted {
			producerAdmission = cr.submitSessionWaitDependencyProducerRequest
		}
		if err := cr.cs.installSessionWaitDependencyShadowAdmissionWithProducer(func() sessionWaitShadowRefreshResult {
			installed, refreshErr := cr.installObservedSessionWaitDependencyIndex(cr.sessionsBeadStore())
			switch {
			case installed:
				cr.submitSessionWaitDependencyStartupCensus()
				return sessionWaitShadowConverged
			case refreshErr == nil || errors.Is(refreshErr, beads.ErrCacheUnavailable):
				return sessionWaitShadowRetry
			default:
				fmt.Fprintf(cr.stderr, "%s: session-wait shadow refresh: %v\n", cr.logPrefix, refreshErr) //nolint:errcheck
				return sessionWaitShadowAwaitRelevant
			}
		}, cr.sessionWaitDependencyContainsWait, producerAdmission); err != nil {
			if producerStarted {
				cr.stopSessionWaitDependencyProducer()
			}
			fmt.Fprintf(cr.stderr, "%s: session-wait shadow admission: %v\n", cr.logPrefix, err) //nolint:errcheck
		} else {
			armed = true
			cr.submitSessionWaitDependencyProducerRequests(cr.cs.requestSessionWaitDependencyShadowRefreshForBead(beads.Bead{}, true))
		}
	}
	if !armed {
		installed, err := cr.installObservedSessionWaitDependencyIndex(cr.sessionsBeadStore())
		if err != nil &&
			!errors.Is(err, beads.ErrCacheUnavailable) {
			fmt.Fprintf(cr.stderr, "%s: session-wait shadow index: %v\n", cr.logPrefix, err) //nolint:errcheck // inert best-effort shadow setup
		}
		if installed {
			cr.submitSessionWaitDependencyStartupCensus()
		}
	}
}

// enableSessionWaitDependencyLifecycleShadowSink connects certified
// dependency-ready waits to a read/observe/plan-only shadow evaluation. The
// rollout gate is boot-latched, and legacy reconciliation remains the sole
// owner of every session, provider, and store mutation.
func (cr *CityRuntime) enableSessionWaitDependencyLifecycleShadowSink(ctx context.Context) {
	if cr == nil || cr.waitDependencyEnqueue != nil || cr.cs == nil {
		return
	}
	mode := cr.cs.RolloutFlags().SessionReconciler()
	if mode != rollout.Auto && mode != rollout.Require {
		return
	}
	if cr.sessionStartOwnershipState() != sessionStartOwnershipKeyed {
		return
	}
	_, release, err := cr.cs.acquireSessionStartSnapshot()
	if err != nil {
		return
	}
	release()
	cr.waitDependencyEnqueue = func(target sessionWaitDependencyTarget, cause sessionWaitDependencyCause) (bool, error) {
		if ctx == nil || ctx.Err() != nil || cr.sessionWaitDependencyStartCh == nil {
			return false, nil
		}
		if err := validateSessionWaitDependencyTarget(target); err != nil {
			return false, err
		}
		hint := sessionWaitDependencyStartHint{Target: cloneSessionWaitDependencyTarget(target), Cause: cause}
		select {
		case cr.sessionWaitDependencyStartCh <- hint:
			return false, nil
		default:
			return false, fmt.Errorf("admitting dependency wait %q: bounded runtime hint queue is full", target.WaitID)
		}
	}
}

// handleSessionWaitDependencyStart turns one producer hint into a certified
// lease. This must run only on CityRuntime.run: producers are intentionally
// unable to mutate, classify lifecycle ownership, or admit starts directly.
func (cr *CityRuntime) handleSessionWaitDependencyStart(ctx context.Context, hint sessionWaitDependencyStartHint) {
	if cr == nil || ctx == nil || ctx.Err() != nil || validateSessionWaitDependencyTarget(hint.Target) != nil || causePrecedence(hint.Cause) == 0 {
		return
	}
	cr.sessionStartMu.Lock()
	owned := cr.sessionStartOwnership == sessionStartOwnershipKeyed
	mode := cr.sessionStartMode
	controller := cr.sessionStartController
	cr.sessionStartMu.Unlock()
	if !owned {
		return
	}
	if !cr.sessionWaitDependencyTargetCertified(hint.Target) {
		return
	}
	snapshot, release, err := cr.cs.acquireSessionStartSnapshot()
	if err != nil {
		cr.handleSessionWaitDependencyAdmissionFailure(hint, mode, controller, err)
		return
	}
	defer release()
	lease, owner, err := certifySessionWaitDependencyStartLease(snapshot.Store, hint.Target,
		newAuthoritativeWaitDependencyStoreSet(cr.cityBeadStore(), cr.rigBeadStores()), snapshot.Config, snapshot.Generation, clock.Real{}.Now())
	if err != nil || owner != exactSessionStartKeyedOwner {
		cr.handleSessionWaitDependencyAdmissionFailure(hint, mode, controller, err)
		return
	}
	if controller == nil {
		cr.handleSessionWaitDependencyAdmissionFailure(hint, mode, controller, errors.New("exact-start controller is unavailable"))
		return
	}
	// Certification is not authority by itself: the current generation and
	// exact index target must still match at the admission effect boundary.
	if lease.IndexGeneration == 0 || lease.IndexGeneration != hint.Target.generation || !cr.sessionWaitDependencyTargetCertified(hint.Target) {
		cr.handleSessionWaitDependencyAdmissionFailure(hint, mode, controller, errors.New("dependency wait index certification changed before admission"))
		return
	}
	outcome, err := controller.AdmitWaitDependency(lease)
	if err != nil || outcome == sessionStartAdmissionOverflow {
		if outcome == sessionStartAdmissionOverflow && err == nil {
			err = errors.New("exact-start admission is full")
		}
		cr.handleSessionWaitDependencyAdmissionFailure(hint, mode, controller, err)
	}
}

func (cr *CityRuntime) handleSessionWaitDependencyAdmissionFailure(hint sessionWaitDependencyStartHint, mode rollout.Mode, controller *sessionStartController, err error) {
	if mode == rollout.Auto {
		cr.sessionWaitDependencyReadyPokePending.Store(true)
		cr.requestLegacySessionStartFallback()
		return
	}
	if controller != nil {
		controller.RequestAudit()
	}
	if err != nil {
		fmt.Fprintf(cr.sessionStartStderr(), "%s: dependency wait %s parked: %v\n", cr.sessionStartLogPrefix(), hint.Target.WaitID, err) //nolint:errcheck
	}
}

func (cr *CityRuntime) sessionWaitDependencyTargetCertified(target sessionWaitDependencyTarget) bool {
	cr.sessionWaitDependencyMu.RLock()
	defer cr.sessionWaitDependencyMu.RUnlock()
	return cr.sessionWaitDependencyTargetCertifiedLocked(target) && target.generation > 0
}

func (cr *CityRuntime) submitSessionWaitDependencyStartupCensus() {
	cr.sessionWaitDependencyMu.Lock()
	if !cr.sessionWaitDependencyStartupCensusOwed ||
		cr.sessionWaitDependencyIndex == nil ||
		cr.sessionWaitDependencyRejectedCensusIDs != nil {
		cr.sessionWaitDependencyMu.Unlock()
		return
	}
	targets := cr.sessionWaitDependencyIndex.AllTargets()
	for n := range targets {
		targets[n].generation = cr.sessionWaitDependencyIndexGeneration
	}
	cr.sessionWaitDependencyStartupCensusOwed = false
	cr.sessionWaitDependencyMu.Unlock()
	cr.submitSessionWaitDependencyTargets(targets, sessionWaitDependencyCauseRegistration)
}

func (cr *CityRuntime) startSessionWaitDependencyProducer() bool {
	if cr == nil || cr.waitDependencyEnqueue == nil || cr.waitDependencyProducer != nil {
		return false
	}
	producer, err := newSessionWaitDependencyProducer(sessionWaitDependencyProducerOptions{
		MaxDistinct: sessionpkg.SessionWaitLookupLimit,
		TargetForWait: func(waitID string) (sessionWaitDependencyTarget, bool) {
			return cr.sessionWaitDependencyTarget(waitID)
		},
		Dependencies: func() waitDependencyReader {
			return newWaitDependencyStoreSet(cr.cityBeadStore(), cr.rigBeadStores())
		},
		EnqueueSession: func(plan sessionWaitDependencyPlan, cause sessionWaitDependencyCause) error {
			cr.sessionWaitDependencyMu.RLock()
			if !cr.sessionWaitDependencyTargetCertifiedLocked(plan.Target) {
				cr.sessionWaitDependencyMu.RUnlock()
				return fmt.Errorf("%w: %s", errSessionWaitDependencyStaleCertification, plan.Target.WaitID)
			}
			enqueue := cr.waitDependencyEnqueue
			retire, err := enqueue(plan.Target, cause)
			cr.sessionWaitDependencyMu.RUnlock()
			if errors.Is(err, errSessionWaitDependencySnapshotUnavailable) ||
				errors.Is(err, errSessionWaitDependencyTargetReadUnavailable) {
				cr.sessionWaitDependencyMu.Lock()
				cr.sessionWaitDependencyStartupCensusOwed = true
				cr.sessionWaitDependencyMu.Unlock()
				if errors.Is(err, errSessionWaitDependencySnapshotUnavailable) {
					cr.requestLegacySessionStartFallback()
					return nil
				}
				return err
			}
			if err != nil {
				return err
			}
			if retire {
				cr.retireCertifiedSessionWaitDependencyTarget(plan.Target)
			}
			return nil
		},
		ReportError: func(err error) {
			fmt.Fprintf(cr.stderr, "%s: wait dependency producer: %v\n", cr.logPrefix, err) //nolint:errcheck
		},
	})
	if err != nil {
		fmt.Fprintf(cr.stderr, "%s: wait dependency producer disabled: %v\n", cr.logPrefix, err) //nolint:errcheck
		return false
	}
	if err := producer.Start(); err != nil {
		fmt.Fprintf(cr.stderr, "%s: wait dependency producer disabled: %v\n", cr.logPrefix, err) //nolint:errcheck
		return false
	}
	cr.sessionWaitDependencyMu.Lock()
	cr.waitDependencyProducer = producer
	cr.sessionWaitDependencyMu.Unlock()
	return true
}

func (cr *CityRuntime) retireCertifiedSessionWaitDependencyTarget(target sessionWaitDependencyTarget) {
	cr.sessionWaitDependencyMu.Lock()
	defer cr.sessionWaitDependencyMu.Unlock()
	if cr.sessionWaitDependencyTargetCertifiedLocked(target) {
		cr.sessionWaitDependencyIndex.Remove(target.WaitID)
	}
}

func newAuthoritativeWaitDependencyStoreSet(cityStore beads.Store, rigStores map[string]beads.Store) waitDependencyStoreSet {
	stores := newWaitDependencyStoreSet(cityStore, rigStores)
	for n, store := range stores {
		stores[n] = authoritativeSessionStartReadStore{Store: store, live: beads.HandlesFor(store).Live}
	}
	return stores
}

func (cr *CityRuntime) stopSessionWaitDependencyProducer() {
	if cr == nil {
		return
	}
	cr.sessionWaitDependencyMu.Lock()
	producer := cr.waitDependencyProducer
	cr.waitDependencyProducer = nil
	cr.sessionWaitDependencyMu.Unlock()
	if producer != nil {
		producer.Stop()
	}
}

func (cr *CityRuntime) sessionWaitDependencyTarget(waitID string) (sessionWaitDependencyTarget, bool) {
	cr.sessionWaitDependencyMu.RLock()
	index := cr.sessionWaitDependencyIndex
	generation := cr.sessionWaitDependencyIndexGeneration
	rejected := cr.sessionWaitDependencyRejectedCensusIDs != nil
	cr.sessionWaitDependencyMu.RUnlock()
	if rejected || index == nil {
		return sessionWaitDependencyTarget{}, false
	}
	target, ok := index.TargetForWait(waitID)
	target.generation = generation
	return target, ok
}

func (cr *CityRuntime) sessionWaitDependencyAllTargets() []sessionWaitDependencyTarget {
	cr.sessionWaitDependencyMu.RLock()
	index, generation := cr.sessionWaitDependencyIndex, cr.sessionWaitDependencyIndexGeneration
	rejected := cr.sessionWaitDependencyRejectedCensusIDs != nil
	cr.sessionWaitDependencyMu.RUnlock()
	if index == nil || rejected {
		return nil
	}
	targets := index.AllTargets()
	for n := range targets {
		targets[n].generation = generation
	}
	return targets
}

func (cr *CityRuntime) sessionWaitDependencyTargetsForDependency(depID string) []sessionWaitDependencyTarget {
	cr.sessionWaitDependencyMu.RLock()
	index, generation := cr.sessionWaitDependencyIndex, cr.sessionWaitDependencyIndexGeneration
	rejected := cr.sessionWaitDependencyRejectedCensusIDs != nil
	cr.sessionWaitDependencyMu.RUnlock()
	if index == nil || rejected {
		return nil
	}
	targets := index.TargetsForDependency(depID)
	for n := range targets {
		targets[n].generation = generation
	}
	return targets
}

func (cr *CityRuntime) submitSessionWaitDependencyTargets(targets []sessionWaitDependencyTarget, cause sessionWaitDependencyCause) {
	cr.sessionWaitDependencyMu.RLock()
	producer := cr.waitDependencyProducer
	cr.sessionWaitDependencyMu.RUnlock()
	if producer == nil {
		return
	}
	for _, target := range targets {
		if err := producer.Admit(target, cause); err != nil {
			fmt.Fprintf(cr.stderr, "%s: wait dependency producer admission: %v\n", cr.logPrefix, err) //nolint:errcheck
		}
	}
}

func (cr *CityRuntime) submitSessionWaitDependencyProducerRequest(request sessionWaitDependencyProducerRequest) {
	type targetAdmission struct {
		target sessionWaitDependencyTarget
		cause  sessionWaitDependencyCause
	}
	byWaitID := make(map[string]targetAdmission)
	add := func(targets []sessionWaitDependencyTarget, cause sessionWaitDependencyCause) {
		for _, target := range targets {
			current, exists := byWaitID[target.WaitID]
			if !exists || target.generation > current.target.generation {
				byWaitID[target.WaitID] = targetAdmission{target: target, cause: cause}
				continue
			}
			if target.generation == current.target.generation {
				current.cause = mergeSessionWaitDependencyCause(current.cause, cause)
				byWaitID[target.WaitID] = current
			}
		}
	}
	if request.fullCensus {
		add(cr.sessionWaitDependencyAllTargets(), sessionWaitDependencyCauseRegistration)
	}
	if request.waitHint {
		if target, ok := cr.sessionWaitDependencyTarget(request.beadID); ok {
			add([]sessionWaitDependencyTarget{target}, sessionWaitDependencyCauseWaitCommit)
		}
	}
	if request.beadID != "" {
		add(cr.sessionWaitDependencyTargetsForDependency(request.beadID), sessionWaitDependencyCauseDependency)
	}
	waitIDs := make([]string, 0, len(byWaitID))
	for waitID := range byWaitID {
		waitIDs = append(waitIDs, waitID)
	}
	slices.Sort(waitIDs)
	for _, waitID := range waitIDs {
		admission := byWaitID[waitID]
		cr.submitSessionWaitDependencyTargets([]sessionWaitDependencyTarget{admission.target}, admission.cause)
	}
}

func (cr *CityRuntime) submitSessionWaitDependencyProducerRequests(requests []sessionWaitDependencyProducerRequest) {
	for _, request := range requests {
		cr.submitSessionWaitDependencyProducerRequest(request)
	}
}

func (cr *CityRuntime) sessionWaitDependencyTargetCertifiedLocked(target sessionWaitDependencyTarget) bool {
	index, generation := cr.sessionWaitDependencyIndex, cr.sessionWaitDependencyIndexGeneration
	rejected := cr.sessionWaitDependencyRejectedCensusIDs != nil
	if rejected || index == nil || generation != target.generation {
		return false
	}
	current, ok := index.TargetForWait(target.WaitID)
	return ok && current.SessionID == target.SessionID && current.DepMode == target.DepMode && slices.Equal(current.DepIDs, target.DepIDs)
}

func (cr *CityRuntime) sessionWaitDependencyContainsWait(id string) bool {
	cr.sessionWaitDependencyMu.RLock()
	index := cr.sessionWaitDependencyIndex
	_, rejected := cr.sessionWaitDependencyRejectedCensusIDs[id]
	cr.sessionWaitDependencyMu.RUnlock()
	return rejected || index != nil && index.containsWait(id)
}

// sessionWaitDependencySessions returns detached session IDs from the current
// private shadow index, if one was installed.
func (cr *CityRuntime) sessionWaitDependencySessions(depID string) []string {
	cr.sessionWaitDependencyMu.RLock()
	index := cr.sessionWaitDependencyIndex
	rejected := cr.sessionWaitDependencyRejectedCensusIDs != nil
	cr.sessionWaitDependencyMu.RUnlock()
	if index == nil || rejected {
		return nil
	}
	return index.SessionsForDependency(depID)
}
