package api

import (
	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/orders"
)

// orderFrontForStore is the single construction point for orders front doors
// over a workflow-store handle in the API layer: the feed and check paths
// route here instead of constructing orders.NewStore inline, so the
// [beads.classes.orders] backend dispatch is a change to this one seam. At
// the bd backend the store is used as BOTH the orders leg and the graph leg
// (single-store colocation dedups to one read — byte-identical to the prior
// inline constructions).
func orderFrontForStore(store beads.Store) *orders.Store {
	return orders.NewStoreWithGraph(beads.OrdersStore{Store: store}, beads.GraphStore{Store: store})
}
