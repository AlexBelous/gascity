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
	"sync"

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
	onChange := func(eventType, beadID, runID, sessionID, stepID string, payload json.RawMessage) {
		rec.Record(events.Event{
			Type:      eventType,
			Actor:     "graph-store",
			Subject:   beadID,
			RunID:     runID,
			SessionID: sessionID,
			StepID:    stepID,
			Payload:   payload,
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
