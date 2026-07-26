package main

import (
	"fmt"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/config"
	"github.com/gastownhall/gascity/internal/events"
	"github.com/gastownhall/gascity/internal/runtime"
	"github.com/gastownhall/gascity/internal/session"
)

type controllerSessionStartSnapshot struct {
	Generation uint64
	CityPath   string
	CityName   string
	Config     *config.City
	Provider   runtime.Provider
	Store      beads.Store
	Recorder   events.Recorder
}

func (cs *controllerState) sessionStartSnapshot() (controllerSessionStartSnapshot, error) {
	if cs == nil {
		return controllerSessionStartSnapshot{}, fmt.Errorf("capturing session-start state: controller state is nil")
	}
	cs.mu.RLock()
	defer cs.mu.RUnlock()

	snapshot := controllerSessionStartSnapshot{
		Generation: cs.sessionStartGeneration,
		CityPath:   cs.cityPath,
		CityName:   cs.cityName,
		Config:     cs.cfg,
		Provider:   cs.sp,
		Store:      resolveSessionStore(cs.cityBeadStore, cs.cfg, cs.cityPath, cs.eventProv),
		Recorder:   cs.eventProv,
	}
	switch {
	case snapshot.Generation == 0:
		return controllerSessionStartSnapshot{}, fmt.Errorf("capturing session-start state: runtime generation is unavailable")
	case cs.sessionStartStoreGeneration != snapshot.Generation:
		return controllerSessionStartSnapshot{}, fmt.Errorf(
			"capturing session-start state: session store generation %d does not match runtime generation %d",
			cs.sessionStartStoreGeneration, snapshot.Generation,
		)
	case snapshot.Config == nil:
		return controllerSessionStartSnapshot{}, fmt.Errorf("capturing session-start state: config is unavailable")
	case snapshot.Provider == nil:
		return controllerSessionStartSnapshot{}, fmt.Errorf("capturing session-start state: runtime provider is unavailable")
	case snapshot.Store == nil:
		return controllerSessionStartSnapshot{}, fmt.Errorf("capturing session-start state: session store is unavailable")
	}
	if snapshot.Recorder == nil {
		snapshot.Recorder = events.Discard
	}
	return snapshot, nil
}

func (cs *controllerState) advanceSessionStartGenerationLocked() {
	cs.sessionStartGeneration++
	if cs.sessionStartGeneration == 0 {
		// Zero is permanently invalid, so an impossible uint64 wrap fails closed
		// rather than making a future snapshot look like the initial generation.
		cs.sessionStartStoreGeneration = 0
	}
}

func (cs *controllerState) installSessionStartEventAdmission(admit func(string)) error {
	if cs == nil {
		return fmt.Errorf("installing session-start event admission: controller state is nil")
	}
	if admit == nil {
		return fmt.Errorf("installing session-start event admission: callback is nil")
	}
	cs.mu.Lock()
	defer cs.mu.Unlock()
	if cs.sessionStartEventAdmissionStopping {
		return fmt.Errorf("installing session-start event admission: admission is stopping")
	}
	if cs.sessionStartEventAdmission != nil {
		return fmt.Errorf("installing session-start event admission: callback is already installed")
	}
	cs.sessionStartEventAdmission = admit
	return nil
}

func (cs *controllerState) stopSessionStartEventAdmission() {
	if cs == nil {
		return
	}
	cs.mu.Lock()
	cs.sessionStartEventAdmissionStopping = true
	cs.sessionStartEventAdmission = nil
	cs.mu.Unlock()
	cs.sessionStartEventAdmissionWG.Wait()
}

func (cs *controllerState) admitSessionStartEvent(evt events.Event) {
	if cs == nil || !isBeadMutationEvent(evt.Type) {
		return
	}
	bead, ok := beads.DecodeBeadEventPayload(evt.Payload)
	if !ok || !session.IsSessionBeadOrRepairable(bead) {
		return
	}
	if err := validateSessionStartAdmission(bead.ID, sessionStartAdmissionInProcess); err != nil {
		return
	}

	cs.mu.Lock()
	admit := cs.sessionStartEventAdmission
	if admit != nil && !cs.sessionStartEventAdmissionStopping {
		cs.sessionStartEventAdmissionWG.Add(1)
	} else {
		admit = nil
	}
	cs.mu.Unlock()
	if admit == nil {
		return
	}
	defer cs.sessionStartEventAdmissionWG.Done()
	admit(bead.ID)
}

func isBeadMutationEvent(eventType string) bool {
	switch eventType {
	case events.BeadCreated, events.BeadUpdated, events.BeadClosed, events.BeadDeleted:
		return true
	default:
		return false
	}
}
