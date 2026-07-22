package main

// messaging_class_store.go is cmd/gc's messaging-class routing seam
// (engdocs/design/infra-class-sqlite-stores.md, Messaging section): every
// production construction of a mail provider or extmsg service fabric — and
// every session-repair extmsg cascade — resolves its backend HERE, so the
// [beads.classes.messaging] knob + the messaging.migrated marker relocate
// the WHOLE class (mail messages AND all extmsg records) atomically.
// TestMessagingSeamIsTheOnlyConstructionPoint enforces the funnel.
//
// Routing is fail-closed at every root: a marked city whose routing cannot
// be resolved or whose class store cannot open disables the affected
// surface (nil provider / nil services / skipped repair, each logged)
// rather than silently falling back to bd, where a routed reader would
// never see the writes.

import (
	"fmt"
	"reflect"
	"strings"
	"sync"

	"github.com/gastownhall/gascity/internal/beads"
	messagingdb "github.com/gastownhall/gascity/internal/classdb/messaging"
	"github.com/gastownhall/gascity/internal/config"
	"github.com/gastownhall/gascity/internal/events"
	"github.com/gastownhall/gascity/internal/extmsg"
	"github.com/gastownhall/gascity/internal/mail"
	"github.com/gastownhall/gascity/internal/mail/beadmail"
)

// messagingRouting carries the messaging-class backend decision: a nil
// class means bd (today's shape, the fixed shape the unrouted helpers hand
// to tests); a non-nil class means the embedded messaging store serves both
// the mail and extmsg halves.
type messagingRouting struct {
	class *messagingdb.Store
}

// messagingRoutingFor resolves a city's messaging-class routing. Inactive
// routing costs one marker stat. Active routing opens the process-shared
// class store; the error is returned rather than falling back to bd,
// because a bd surface on a migrated city would run against residue instead
// of the class.
func messagingRoutingFor(cityPath string, cfg *config.City) (messagingRouting, error) {
	class, routed, err := messagingdb.RoutedStoreFor(cityPath, cfg)
	if err != nil {
		return messagingRouting{}, err
	}
	if !routed {
		return messagingRouting{}, nil
	}
	return messagingRouting{class: class}, nil
}

// newCityMailProviderRouted is the routed form of newCityMailProvider: the
// controller's cached-addressing mail provider over the class store when
// messaging routes, the bd two-store provider otherwise. The [mail]
// provider knob (exec:/fake/fail) bypasses beadmail on both legs.
func newCityMailProviderRouted(routing messagingRouting, workStore beads.Store, cfg *config.City, cityPath string, rec events.Recorder) mail.Provider {
	if routing.class == nil {
		return newCityMailProvider(workStore, cfg, cityPath, rec)
	}
	v := mailProviderName()
	if strings.HasPrefix(v, "exec:") || v == "fake" || v == "fail" {
		return newMailProviderNamed(v, nil)
	}
	return beadmail.NewCachedWithBackend(routing.class, resolveSessionStore(workStore, cfg, cityPath, rec))
}

// newExtmsgServicesRouted builds the extmsg service fabric for a city: over
// the class store when messaging routes, over the bd class-resolved stores
// otherwise. Session-liveness reads follow the session class on both legs.
func newExtmsgServicesRouted(routing messagingRouting, workStore beads.Store, cfg *config.City, cityPath string, rec events.Recorder) extmsg.Services {
	sessStore := resolveSessionStore(workStore, cfg, cityPath, rec)
	if routing.class == nil {
		return extmsg.NewServicesWithSessionStore(resolveMailMessagesStore(workStore, cfg, cityPath, rec), sessStore)
	}
	return extmsg.NewServicesWithBackend(routing.class, sessStore)
}

// retentionMailProvider returns a headless (no session addressing) mail
// provider over the class store for the routed retention legs — the sweep
// and purge read no session topology.
func retentionMailProvider(class *messagingdb.Store) *beadmail.Provider {
	return beadmail.NewWithBackend(class, nil)
}

// handoffMailProvider builds the handoff command's concrete bead-backed
// provider (cmd_handoff needs SendHandoff, which the generic mail.Provider
// surface omits): over the class store when the store's city routes
// messaging, the bd two-store form otherwise.
func handoffMailProvider(store, sessStore beads.Store) (*beadmail.Provider, error) {
	class, err := messagingRepairClassFor(store)
	if err != nil {
		return nil, err
	}
	if class != nil {
		return beadmail.NewWithBackend(class, sessStore), nil
	}
	return beadmail.NewWithStores(store, sessStore), nil
}

// messagingRepairCities maps a city store handle (by pointer identity, the
// sharedBindingLockPools trick) to its city path. The session-repair extmsg
// cascades (reassignState.../cancelStateAssignedToRetiredSessionBead) sit
// under closeBead, eight call chains below any frame that knows the city, so
// the store-opening roots — the controller (boot + reload) and the CLI
// city-store opener — record the association here instead of threading
// (cityPath, cfg) through every session closer. An unregistered store
// resolves to bd, which is exactly today's behavior for tests and in-memory
// stores; the map is keyed per store value, so a multi-city process (the
// consolidated supervisor) stays correct.
var messagingRepairCities struct {
	mu      sync.Mutex
	byStore map[string]string
}

func storeIdentityKey(store beads.Store) string {
	if store == nil {
		return ""
	}
	// A sessions shadow wrapper keys as the bd store it fronts: the repair
	// registry registered the base handle at the store-opening roots, and a
	// per-resolve wrapper must resolve to the same city.
	if shadowed, ok := store.(interface{ ShadowPrimary() beads.Store }); ok {
		return storeIdentityKey(shadowed.ShadowPrimary())
	}
	value := reflect.ValueOf(store)
	switch value.Kind() {
	case reflect.Pointer, reflect.Map, reflect.Slice, reflect.Func, reflect.Chan, reflect.UnsafePointer:
		return fmt.Sprintf("%T:%x", store, value.Pointer())
	default:
		return fmt.Sprintf("%T:%v", store, store)
	}
}

// registerMessagingRepairCity records which city a store handle belongs to
// so the deep session-repair paths can resolve messaging-class routing.
func registerMessagingRepairCity(store beads.Store, cityPath string) {
	key := storeIdentityKey(store)
	if key == "" || cityPath == "" {
		return
	}
	messagingRepairCities.mu.Lock()
	defer messagingRepairCities.mu.Unlock()
	if messagingRepairCities.byStore == nil {
		messagingRepairCities.byStore = make(map[string]string)
	}
	messagingRepairCities.byStore[key] = cityPath
}

// messagingRepairClassFor resolves the messaging-class store for a city
// store handle's session-repair extmsg cascades: nil means bd (unregistered
// handle or unrouted city); an error means the city is marked migrated but
// routing could not be resolved — the caller must SKIP the bd cascade
// (fail closed), not run it against residue.
func messagingRepairClassFor(store beads.Store) (*messagingdb.Store, error) {
	key := storeIdentityKey(store)
	if key == "" {
		return nil, nil
	}
	messagingRepairCities.mu.Lock()
	cityPath := messagingRepairCities.byStore[key]
	messagingRepairCities.mu.Unlock()
	if cityPath == "" {
		return nil, nil
	}
	routing, err := messagingRoutingFor(cityPath, nil)
	if err != nil {
		return nil, err
	}
	return routing.class, nil
}
