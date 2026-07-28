package main

import (
	"fmt"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/events"
	"github.com/gastownhall/gascity/internal/session"
)

type sessionWaitShadowRefreshResult uint8

const (
	sessionWaitShadowRetry sessionWaitShadowRefreshResult = iota
	sessionWaitShadowConverged
	sessionWaitShadowAwaitRelevant
)

func (cs *controllerState) installSessionWaitDependencyShadowAdmission(admit func() sessionWaitShadowRefreshResult, mayContain func(string) bool) error {
	if cs == nil {
		return fmt.Errorf("installing session-wait shadow admission: controller state is nil")
	}
	if admit == nil || mayContain == nil {
		return fmt.Errorf("installing session-wait shadow admission: callback is nil")
	}
	cs.mu.Lock()
	defer cs.mu.Unlock()
	if cs.sessionWaitShadowAdmission != nil || cs.sessionWaitShadowAdmissionStopping {
		return fmt.Errorf("installing session-wait shadow admission: admission unavailable")
	}
	cs.sessionWaitShadowAdmission = admit
	cs.sessionWaitShadowMayContain = mayContain
	return nil
}

func (cs *controllerState) stopSessionWaitDependencyShadowAdmission() {
	if cs == nil {
		return
	}
	cs.mu.Lock()
	cs.sessionWaitShadowAdmissionStopping = true
	cs.sessionWaitShadowAdmission = nil
	cs.sessionWaitShadowMayContain = nil
	cs.mu.Unlock()
	cs.sessionWaitShadowAdmissionWG.Wait()
	cs.mu.Lock()
	cs.sessionWaitShadowPending = false
	cs.sessionWaitShadowAdmissionStopping = false
	cs.mu.Unlock()
}

func (cs *controllerState) admitSessionWaitDependencyShadowEvent(evt events.Event) {
	if cs == nil || !isBeadMutationEvent(evt.Type) {
		return
	}
	bead, decoded := beads.DecodeBeadEventPayload(evt.Payload)
	cs.requestSessionWaitDependencyShadowRefreshForBead(bead, decoded && session.IsWaitBead(bead))
}

func (cs *controllerState) requestSessionWaitDependencyShadowRefreshForBead(bead beads.Bead, mayHaveChanged bool) {
	if cs == nil {
		return
	}
	cs.mu.Lock()
	admit := cs.sessionWaitShadowAdmission
	mayContain := cs.sessionWaitShadowMayContain
	if admit == nil || mayContain == nil || cs.sessionWaitShadowAdmissionStopping {
		cs.mu.Unlock()
		return
	}
	cs.sessionWaitShadowAdmissionWG.Add(1)
	cs.mu.Unlock()
	defer cs.sessionWaitShadowAdmissionWG.Done()

	if !mayHaveChanged && bead.ID != "" {
		mayHaveChanged = mayContain(bead.ID)
	}

	cs.mu.Lock()
	if mayHaveChanged {
		cs.sessionWaitShadowPending = true
		cs.sessionWaitShadowGeneration++
	}
	if !cs.sessionWaitShadowPending {
		cs.mu.Unlock()
		return
	}
	generation := cs.sessionWaitShadowGeneration
	cs.mu.Unlock()

	result := admit()
	cs.mu.Lock()
	if cs.sessionWaitShadowGeneration == generation {
		switch result {
		case sessionWaitShadowConverged, sessionWaitShadowAwaitRelevant:
			cs.sessionWaitShadowPending = false
		}
	}
	cs.mu.Unlock()
}
