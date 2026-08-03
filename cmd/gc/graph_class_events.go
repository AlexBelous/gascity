package main

// Controller-side bead.* emission for the graph class (win3 gap G16): the
// routed graph store is wrapped in a CachingStore whose onChange Records
// bead.created/updated/closed — the same envelope shape the work store's
// cache emits — so the runs views, SSE stream, and event-driven read models
// keep folding graph-plane lifecycle after relocation. The wrapper is
// cached per city so every controller resolve shares ONE identity (the
// `graph != CityBeadStore()` comparisons and capability assertions stay
// stable) and the cache tiers prime lazily (CachedReady/CachedList fall to
// Live until primed — never wrong, only cold). CLI one-shot emission (the
// integ 59ebf549f arm) is a recorded follow-up in the fix plan.

import (
	"encoding/json"
	"io"
	"strings"
	"sync"

	"github.com/gastownhall/gascity/internal/beadmeta"
	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/events"
)

var graphEmitWrappers struct {
	mu    sync.Mutex
	byKey map[string]*beads.CachingStore
}

// graphStoreWithEvents wraps the routed graph store for controller use.
// rec == nil (CLI one-shots) returns the store unchanged.
func graphStoreWithEvents(store beads.Store, cityPath string, rec events.Recorder) beads.Store {
	if store == nil || rec == nil {
		return store
	}
	graphEmitWrappers.mu.Lock()
	defer graphEmitWrappers.mu.Unlock()
	if wrapped, ok := graphEmitWrappers.byKey[cityPath]; ok {
		return wrapped
	}
	onChange := func(eventType, beadID, runID, sessionID, stepID string, dependsOnStepIDs *[]string, payload json.RawMessage) {
		rec.Record(events.Event{
			Type:             eventType,
			Actor:            "graph-store",
			Subject:          beadID,
			RunID:            runID,
			SessionID:        sessionID,
			StepID:           stepID,
			DependsOnStepIDs: dependsOnStepIDs,
			Payload:          payload,
		})
	}
	wrapped := beads.NewCachingStore(store, onChange)
	if graphEmitWrappers.byKey == nil {
		graphEmitWrappers.byKey = make(map[string]*beads.CachingStore)
	}
	graphEmitWrappers.byKey[cityPath] = wrapped
	return wrapped
}

// graphStoreMaybeWithEvents returns the city's cached emission wrapper when
// the controller has built one (so create-side policy dispatch and other
// recorder-less seams inherit bead.* emission + cache identity), else the
// raw store — CLI one-shot processes never build a wrapper.
func graphStoreMaybeWithEvents(store beads.Store, cityPath string) beads.Store {
	if store == nil {
		return nil
	}
	graphEmitWrappers.mu.Lock()
	defer graphEmitWrappers.mu.Unlock()
	if wrapped, ok := graphEmitWrappers.byKey[cityPath]; ok {
		return wrapped
	}
	return store
}

// emitGraphBeadLifecycle records a bead.* lifecycle event for an in-process
// graph-store mutation made by a ONE-SHOT CLI process (gc bd close/update on
// gcg ids, gc hook --claim). Controller writes ride the CachingStore wrapper
// above; a CLI process has no cache, so it appends to the city event log
// directly — the same shape and the same log the bd on_close hook writes for
// work beads. Without this the runs views and every event-fold read model
// miss worker-driven graph transitions entirely (win3 gap G17's root cause).
// Best-effort: a recorder failure never fails the mutation.
func emitGraphBeadLifecycle(cityPath, eventType string, b beads.Bead, stderr io.Writer) {
	if strings.TrimSpace(cityPath) == "" || strings.TrimSpace(b.ID) == "" {
		return
	}
	rec := openCityRecorderAt(cityPath, stderr)
	if rec == nil {
		return
	}
	payload, err := json.Marshal(b)
	if err != nil {
		return
	}
	rec.Record(events.Event{
		Type:      eventType,
		Actor:     "gc-cli",
		Subject:   b.ID,
		RunID:     beadmeta.ResolveRunID(b.Metadata, b.ID, ""),
		SessionID: b.Metadata[beadmeta.SessionIDMetadataKey],
		StepID:    b.Metadata[beadmeta.StepIDMetadataKey],
		Payload:   payload,
	})
}
