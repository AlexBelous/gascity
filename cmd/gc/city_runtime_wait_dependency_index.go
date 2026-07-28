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
		return observedSessionWaitCensus{}, nil, err
	}
	candidate := newSessionWaitDependencyIndex()
	if err := candidate.Rebuild(census.waits); err != nil {
		return observedSessionWaitCensus{}, nil, fmt.Errorf("building session wait dependency index: %w", err)
	}
	return census, candidate, nil
}

// publishObservedSessionWaitDependencyIndex replaces the runtime's private
// index only while the cache observation that supplied its census is current.
func (cr *CityRuntime) publishObservedSessionWaitDependencyIndex(census observedSessionWaitCensus, candidate *sessionWaitDependencyIndex) (bool, error) {
	return census.cache.WithCurrentObservation(census.observation, func() error {
		cr.sessionWaitDependencyMu.Lock()
		cr.sessionWaitDependencyIndex = candidate
		cr.sessionWaitDependencyMu.Unlock()
		return nil
	})
}

func (cr *CityRuntime) installObservedSessionWaitDependencyIndex(store beads.SessionStore) (bool, error) {
	census, candidate, err := buildObservedSessionWaitDependencyIndex(store)
	if err != nil {
		return false, err
	}
	return cr.publishObservedSessionWaitDependencyIndex(census, candidate)
}

// startSessionWaitDependencyShadow performs one inert, best-effort startup
// install. Cache unavailability is expected; malformed complete censuses are
// diagnosed without changing legacy startup behavior.
func (cr *CityRuntime) startSessionWaitDependencyShadow() {
	if _, err := cr.installObservedSessionWaitDependencyIndex(cr.sessionsBeadStore()); err != nil &&
		!errors.Is(err, beads.ErrCacheUnavailable) {
		fmt.Fprintf(cr.stderr, "%s: session-wait shadow index: %v\n", cr.logPrefix, err) //nolint:errcheck // inert best-effort shadow setup
	}
}

// sessionWaitDependencySessions returns detached session IDs from the current
// private startup index, if one was installed.
func (cr *CityRuntime) sessionWaitDependencySessions(depID string) []string {
	cr.sessionWaitDependencyMu.RLock()
	index := cr.sessionWaitDependencyIndex
	cr.sessionWaitDependencyMu.RUnlock()
	if index == nil {
		return nil
	}
	return index.SessionsForDependency(depID)
}
