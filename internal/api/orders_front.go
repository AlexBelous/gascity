package api

import (
	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/orders"
)

// orderFrontDoorProvider is the optional State capability behind the orders
// front-door seam: a state that knows the city's [beads.classes.orders]
// backend routing (the controller) supplies routed front doors; states
// without it (tests, fixtures) get the byte-identical bd shape.
type orderFrontDoorProvider interface {
	// OrderFrontDoor returns the orders front door over one workflow-store
	// handle under the city's class routing. An error means the routed class
	// store is unavailable — callers must fail the read rather than fall back
	// to bd (a silent fallback would read a store the writes no longer land
	// in).
	OrderFrontDoor(scope beads.Store) (*orders.Store, error)
}

// orderFrontForState is the single construction point for orders front doors
// over a workflow-store handle in the API layer: the feed and check paths
// route here instead of constructing orders.NewStore inline, so the
// [beads.classes.orders] backend dispatch is a change to this one seam. At
// the bd backend the store is used as BOTH the orders leg and the graph leg
// (single-store colocation dedups to one read — byte-identical to the prior
// inline constructions).
func orderFrontForState(state State, store beads.Store) (*orders.Store, error) {
	if provider, ok := state.(orderFrontDoorProvider); ok {
		return provider.OrderFrontDoor(store)
	}
	return orders.NewStoreWithGraph(beads.OrdersStore{Store: store}, beads.GraphStore{Store: store}), nil
}
