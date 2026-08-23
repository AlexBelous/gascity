package main

import (
	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/config"
)

// cliMailMessagesStore routes a generic CLI one-shot work store to the
// messaging coordination-class store, so a [beads.classes.messaging] relocation
// reaches one-shot commands the same way it reaches the running controller
// (which routes through resolveMailMessagesStore via newCityMailProvider).
// Identity to the input store at the default single-store backend, so wrapping
// is byte-identical until a messaging relocation is configured.
//
// The recorder is nil for the same reason cliSessionStore's is: resolveClassStore
// ignores it, and what makes a relocated one-shot write observable is the emit
// target the funnel puts on the ROUTES (class_store_emit.go), which
// cliStorageRoutes has already applied by the time this returns.
func cliMailMessagesStore(store beads.Store, cfg *config.City, cityPath string) beads.Store {
	return resolveMailMessagesStore(cliStorageRoutes(cityPath), store, cfg, cityPath, nil)
}
