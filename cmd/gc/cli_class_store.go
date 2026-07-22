package main

import (
	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/config"
)

// This file is the CLI one-shot seam for the graph, orders, and nudges
// coordination classes, mirroring cliSessionStore (cli_session_store.go) for
// the session class. Each helper routes a generic CLI work store to its
// coordination-class store so a [beads.classes.<class>] relocation reaches
// one-shot commands (gc sling, gc order run, ...) the same way it reaches the
// running controller (which routes through the CityRuntime.*BeadStore
// accessors). The infra store is sourced lazily from cachedCityInfraStore, so a
// split city's graph/order/nudge writes reach the infra store from a one-shot
// command; it is nil (⇒ identity to the input store) on every single-store
// city, so wrapping stays byte-identical until the split activates.
//
// On a split city each helper additionally augments the infra store with
// classStoreWithCLIEmission (class_store_emit.go) so a one-shot CLI mutation of
// a relocated coordination-class bead emits the canonical bead.* event into
// <cityPath>/.gc/events.jsonl — the CLI analog of the controller's CachingStore
// notifyChange. Without it a worker's `gc mol autoclose` / `gc sling` write
// landed in the event-silent infra store and the run projection showed the step
// "Running" forever. The augmentation reuses the SAME policy-store type (never a
// new wrapper LAYER), so the graph-create optional-capability assertions stay
// intact. On a single-store city (infra nil) the helper returns the input store
// verbatim and never augments, so wrapping stays byte-identical.

// cliGraphStore routes a generic CLI one-shot work store to the graph
// (workflow/v2) coordination-class store: the infra store on a split city, else
// the input store verbatim (identity). This is the CLI analog of
// CityRuntime.graphBeadStore. On a split city it returns the infra store
// augmented to emit bead.* events on mutation; on a single-store city it returns
// the input store verbatim, never a re-wrap, so the graph-create
// optional-capability assertions (GraphApplyFor / HandlesFor /
// StorageCreateStore) that molecule.Instantiate relies on stay intact.
func cliGraphStore(store beads.Store, cfg *config.City, cityPath string) beads.Store {
	infra := cachedCityInfraStore(cityPath, cfg)
	resolved := resolveGraphStore(store, infra, cfg, cityPath, nil)
	if infra == nil {
		return resolved
	}
	return classStoreWithCLIEmission(resolved, cityPath)
}

// cliOrderStore routes a generic CLI one-shot work store to the order-tracking
// coordination-class store: the infra store (augmented to emit bead.* events) on
// a split city, else the input store verbatim (identity). It is the CLI analog
// of CityRuntime.ordersBeadStore, preserving the store capabilities.
func cliOrderStore(store beads.Store, cfg *config.City, cityPath string) beads.Store {
	infra := cachedCityInfraStore(cityPath, cfg)
	resolved := resolveOrderStore(store, infra, cfg, cityPath, nil)
	if infra == nil {
		return resolved
	}
	return classStoreWithCLIEmission(resolved, cityPath)
}

// cliNudgesStore routes a generic CLI one-shot work store to the nudge
// coordination-class store: the infra store (augmented to emit bead.* events) on
// a split city, else the input store verbatim (identity). It is the CLI analog
// of CityRuntime.nudgesBeadStore.
func cliNudgesStore(store beads.Store, cfg *config.City, cityPath string) beads.Store {
	infra := cachedCityInfraStore(cityPath, cfg)
	resolved := resolveNudgesStore(store, infra, cfg, cityPath, nil)
	if infra == nil {
		return resolved
	}
	return classStoreWithCLIEmission(resolved, cityPath)
}

// cliMailStore routes a generic CLI one-shot work store to the messaging
// coordination-class store: the infra store (augmented to emit bead.* events) on
// a split city, else the input store verbatim (identity). It is the CLI analog
// of CityRuntime.mailBeadStore's message-persistence leg, so a one-shot mail
// write on a split city emits the same bead.* events the controller emits
// through its CachingStore.
func cliMailStore(store beads.Store, cfg *config.City, cityPath string) beads.Store {
	infra := cachedCityInfraStore(cityPath, cfg)
	resolved := resolveMailMessagesStore(store, infra, cfg, cityPath, nil)
	if infra == nil {
		return resolved
	}
	return classStoreWithCLIEmission(resolved, cityPath)
}
