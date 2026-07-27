package main

import (
	"fmt"
	"sort"
	"strings"
	"sync"

	sessionpkg "github.com/gastownhall/gascity/internal/session"
)

// sessionWaitDependencyIndex maps each pending dependency wait to the session
// it should wake when an exact dependency becomes ready.
type sessionWaitDependencyIndex struct {
	mu           sync.Mutex
	byWaitID     map[string]waitDependencyRegistration
	byDependency map[string]map[string]string
}

type waitDependencyRegistration struct {
	sessionID string
	depIDs    []string
}

func newSessionWaitDependencyIndex() *sessionWaitDependencyIndex {
	return &sessionWaitDependencyIndex{
		byWaitID:     make(map[string]waitDependencyRegistration),
		byDependency: make(map[string]map[string]string),
	}
}

// Replace replaces a wait's registration. Known non-pending dependency waits
// remove any prior registration. Malformed dependency waits leave prior state
// unchanged.
func (i *sessionWaitDependencyIndex) Replace(wait sessionpkg.WaitInfo) error {
	registration, indexable, err := waitDependencyRegistrationFrom(wait)
	if err != nil {
		return err
	}

	i.mu.Lock()
	defer i.mu.Unlock()
	i.removeLocked(wait.ID)
	if !indexable {
		return nil
	}

	i.byWaitID[wait.ID] = registration
	for _, depID := range registration.depIDs {
		edges := i.byDependency[depID]
		if edges == nil {
			edges = make(map[string]string)
			i.byDependency[depID] = edges
		}
		edges[wait.ID] = registration.sessionID
	}
	return nil
}

// Remove removes a wait's registration when the durable wait disappears.
func (i *sessionWaitDependencyIndex) Remove(waitID string) {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.removeLocked(waitID)
}

// SessionsForDependency returns detached session IDs registered for one exact
// dependency, sorted for deterministic wake scheduling.
func (i *sessionWaitDependencyIndex) SessionsForDependency(depID string) []string {
	i.mu.Lock()
	defer i.mu.Unlock()

	edges := i.byDependency[depID]
	if len(edges) == 0 {
		return nil
	}
	sessions := make(map[string]struct{}, len(edges))
	for _, sessionID := range edges {
		sessions[sessionID] = struct{}{}
	}
	result := make([]string, 0, len(sessions))
	for sessionID := range sessions {
		result = append(result, sessionID)
	}
	sort.Strings(result)
	return result
}

func waitDependencyRegistrationFrom(wait sessionpkg.WaitInfo) (waitDependencyRegistration, bool, error) {
	if wait.Kind != "deps" {
		return waitDependencyRegistration{}, false, nil
	}
	switch wait.Status {
	case "closed":
		return waitDependencyRegistration{}, false, nil
	case "open":
	default:
		return waitDependencyRegistration{}, false, fmt.Errorf("dependency wait %q has unsupported status %q", wait.ID, wait.Status)
	}
	switch wait.State {
	case waitStatePending:
	case waitStateReady:
		return waitDependencyRegistration{}, false, nil
	default:
		if sessionpkg.IsWaitTerminalState(wait.State) {
			return waitDependencyRegistration{}, false, nil
		}
		return waitDependencyRegistration{}, false, fmt.Errorf("open dependency wait %q has unsupported state %q", wait.ID, wait.State)
	}
	if err := validateWaitDependencyIndexID("wait ID", wait.ID); err != nil {
		return waitDependencyRegistration{}, false, err
	}
	if err := validateWaitDependencyIndexID("session ID", wait.SessionID); err != nil {
		return waitDependencyRegistration{}, false, err
	}
	if wait.DepMode != "all" && wait.DepMode != "any" {
		return waitDependencyRegistration{}, false, fmt.Errorf("pending dependency wait %q has invalid dependency mode %q", wait.ID, wait.DepMode)
	}
	if len(wait.DepIDs) == 0 {
		return waitDependencyRegistration{}, false, fmt.Errorf("pending dependency wait %q has no dependency IDs", wait.ID)
	}

	depIDs := make([]string, 0, len(wait.DepIDs))
	seen := make(map[string]struct{}, len(wait.DepIDs))
	for _, depID := range wait.DepIDs {
		if err := validateWaitDependencyIndexID("dependency ID", depID); err != nil {
			return waitDependencyRegistration{}, false, fmt.Errorf("pending dependency wait %q: %w", wait.ID, err)
		}
		if _, exists := seen[depID]; exists {
			continue
		}
		seen[depID] = struct{}{}
		depIDs = append(depIDs, depID)
	}
	return waitDependencyRegistration{sessionID: wait.SessionID, depIDs: depIDs}, true, nil
}

func validateWaitDependencyIndexID(field, value string) error {
	if value == "" || strings.TrimSpace(value) != value {
		return fmt.Errorf("%s is empty or has surrounding whitespace", field)
	}
	return nil
}

func (i *sessionWaitDependencyIndex) removeLocked(waitID string) {
	registration, exists := i.byWaitID[waitID]
	if !exists {
		return
	}
	for _, depID := range registration.depIDs {
		edges := i.byDependency[depID]
		delete(edges, waitID)
		if len(edges) == 0 {
			delete(i.byDependency, depID)
		}
	}
	delete(i.byWaitID, waitID)
}
