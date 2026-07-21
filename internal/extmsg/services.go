package extmsg

import "github.com/gastownhall/gascity/internal/beads"

// Services bundles the Phase 1 fabric services built over a shared lock pool.
type Services struct {
	Bindings   BindingService
	Delivery   DeliveryContextService
	Groups     GroupService
	Transcript TranscriptService
}

// NewServices creates binding, delivery, and group services that share the
// same per-fabric binding lock pool. The one store serves both extmsg record
// persistence and session-liveness reads — the single-store shape, kept
// byte-identical for existing callers.
func NewServices(store beads.Store, opts ...BindingServiceOption) Services {
	return NewServicesWithSessionStore(store, store, opts...)
}

// NewServicesWithSessionStore creates the fabric services with the two-store
// split: extmsg records (bindings, delivery contexts, groups, participants,
// memberships, transcripts) persist in store — the MESSAGING-class store —
// while session-liveness resolution (stable-name capture at bind time, live
// overlay on resolve, reaper liveness probes) reads sessionStore — the
// SESSION-class store. Both resolve to the same work store on a single-store
// city; they diverge once [beads.classes.messaging] or
// [beads.classes.sessions] relocates a class, and no single-store handle is
// then correct for extmsg as a whole. A nil sessionStore falls back to store.
func NewServicesWithSessionStore(store, sessionStore beads.Store, opts ...BindingServiceOption) Services {
	locks := sharedBindingLockPool(store)
	transcript := newTranscriptService(store, locks)
	delivery := newDeliveryContextService(store, locks, transcript)
	return Services{
		Bindings:   newBindingService(store, sessionStore, delivery, transcript, locks, opts...),
		Delivery:   delivery,
		Groups:     newGroupService(store, sessionStore, locks, transcript),
		Transcript: transcript,
	}
}
