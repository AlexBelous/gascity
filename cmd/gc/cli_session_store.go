package main

import (
	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/config"
	"github.com/gastownhall/gascity/internal/session"
)

// cliSessionStore routes a generic CLI one-shot work store to the session
// coordination-class store, so a [beads.classes.sessions] relocation reaches
// one-shot commands the same way it reaches the running controller (which routes
// through resolveSessionStore via CityRuntime.sessionsBeadStore). Identity to the
// input store at the default single-store backend (resolveSessionStore returns
// the store verbatim there), so wrapping is byte-identical until a session
// relocation is configured.
//
// The infra store is sourced lazily from cachedCityInfraStore, so a split city's
// session reads/writes reach the infra store from a one-shot command the same way
// they reach the controller. On a split city the resolved infra store is
// augmented with classStoreWithCLIEmission (class_store_emit.go) so a one-shot
// CLI session-bead mutation emits the canonical bead.* event — the CLI analog of
// the controller's CachingStore notifyChange. It is nil (⇒ identity to the input
// store, no augmentation) on every single-store city, so wrapping stays
// byte-identical until the split activates.
//
// Coverage caveat: the emission shadows the direct beads.Store mutators
// (Create/Update/Close/SetMetadata/…), so `gc session close` is covered
// (Manager.CloseDetailed → store.Close). Tx-shaped session writes are NOT
// covered — Tx is promoted from the inner store (unlike CachingStore.Tx, which
// notifies via refreshTxTouchedBeads), so a one-shot reopen of a closed named
// session through reopenClosedConfiguredNamedSessionBead (session_beads.go),
// reached from gc nudge / gc session wake / gc sling / gc session pin session
// materialization, emits no bead.updated on a split city. A follow-up to shadow
// Tx on beadPolicyStore closes that gap; the controller-resident Tx sites
// (closeBead / rollbackPendingCreate) already emit via CachingStore.
func cliSessionStore(store beads.Store, cfg *config.City, cityPath string) beads.Store {
	infra := cachedCityInfraStore(cityPath, cfg)
	resolved := resolveSessionStore(store, infra, cfg, cityPath, nil)
	if infra == nil {
		return resolved
	}
	return classStoreWithCLIEmission(resolved, cityPath)
}

// cliSessionFrontDoor builds the typed session write front door over the
// session-class store for a CLI one-shot command. It is the relocation-safe
// replacement for sessionFrontDoor(store) at CLI command roots. The name
// deliberately does not contain the substring "sessionFrontDoor(" so the
// relocation guard (TestSessionRelocationRootsRouteThroughSessionClassStore) can
// forbid the unrouted form while allowing this one.
func cliSessionFrontDoor(store beads.Store, cfg *config.City, cityPath string) *session.Store {
	return sessionFrontDoor(cliSessionStore(store, cfg, cityPath))
}
