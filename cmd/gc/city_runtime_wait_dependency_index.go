package main

import (
	"errors"
	"fmt"

	"github.com/gastownhall/gascity/internal/beads"
)

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
	return census.cache.WithCurrentObservation(census.observation, func() error {
		cr.sessionWaitDependencyMu.Lock()
		cr.sessionWaitDependencyIndex = candidate
		cr.sessionWaitDependencyRejectedCensusIDs = nil
		cr.sessionWaitDependencyMu.Unlock()
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
		cr.sessionWaitDependencyRejectedCensusIDs = ids
		cr.sessionWaitDependencyMu.Unlock()
		return nil
	})
}

// startSessionWaitDependencyShadow arms inert steady-state refresh before
// requesting the initial census. Cache unavailability and stale observations
// remain pending; deterministic census errors wait for a relevant wait change.
func (cr *CityRuntime) startSessionWaitDependencyShadow() {
	armed := false
	if cr.cs != nil {
		if err := cr.cs.installSessionWaitDependencyShadowAdmission(func() sessionWaitShadowRefreshResult {
			installed, refreshErr := cr.installObservedSessionWaitDependencyIndex(cr.sessionsBeadStore())
			switch {
			case installed:
				return sessionWaitShadowConverged
			case refreshErr == nil || errors.Is(refreshErr, beads.ErrCacheUnavailable):
				return sessionWaitShadowRetry
			default:
				fmt.Fprintf(cr.stderr, "%s: session-wait shadow refresh: %v\n", cr.logPrefix, refreshErr) //nolint:errcheck
				return sessionWaitShadowAwaitRelevant
			}
		}, cr.sessionWaitDependencyContainsWait); err != nil {
			fmt.Fprintf(cr.stderr, "%s: session-wait shadow admission: %v\n", cr.logPrefix, err) //nolint:errcheck
		} else {
			armed = true
			cr.cs.requestSessionWaitDependencyShadowRefreshForBead(beads.Bead{}, true)
		}
	}
	if !armed {
		if _, err := cr.installObservedSessionWaitDependencyIndex(cr.sessionsBeadStore()); err != nil &&
			!errors.Is(err, beads.ErrCacheUnavailable) {
			fmt.Fprintf(cr.stderr, "%s: session-wait shadow index: %v\n", cr.logPrefix, err) //nolint:errcheck // inert best-effort shadow setup
		}
	}
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
	cr.sessionWaitDependencyMu.RUnlock()
	if index == nil {
		return nil
	}
	return index.SessionsForDependency(depID)
}
