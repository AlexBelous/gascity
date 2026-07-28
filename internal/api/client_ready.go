package api

import (
	"context"
	"fmt"
	"strings"

	"github.com/gastownhall/gascity/internal/api/genclient"
	"github.com/gastownhall/gascity/internal/beads"
)

// BeadsReady fetches the city's ready work via
// GET /v0/city/{cityName}/beads/ready — the controller-side equivalent of
// `bd ready`. It is the read the hook fast path filters into the assigned-ready
// and routed-pool tiers, so a worker never opens its own SQL connection to
// discover ready work. Empty params make the read non-blocking (no Index →
// immediate snapshot).
//
// scope restricts the read to a single backing store — a rig name or the city
// name — which is how the fast path reproduces the legacy per-store work-query
// precedence (invariant 2). An empty scope federates every store, the default
// `bd ready` behavior.
//
// includeEphemeral spans both the durable issues tier and the ephemeral wisps
// tier, so ephemeral molecule/wisp ready work stays visible; the fast path sets
// it to match the generated query's --include-ephemeral probes. Other callers
// leave it false to keep the historical tier.
//
// A pre-request transport failure surfaces as a *connError (IsConnError true);
// the managed-Dolt hook fast path classifies it and fails closed.
func (c *Client) BeadsReady(scope string, includeEphemeral bool) (CachedRead[[]beads.Bead], error) {
	return c.BeadsReadyQuery(ReadyReadOpts{Scope: scope, IncludeEphemeral: includeEphemeral})
}

// Routed-pool match modes for ReadyReadOpts.RouteMode, mirroring the server's
// route_mode enum: canonical matches the persisted gc.routed_to key; migration
// matches the retirement-window legacy shape (gc.run_target on a gc.kind=workflow
// root with no gc.routed_to stamp).
const (
	RouteModeCanonical = "canonical"
	RouteModeMigration = "migration"
)

// ReadyReadOpts are the bounded ready-read parameters for BeadsReadyQuery.
// The zero value reproduces BeadsReady's historical unbounded live read; any
// bounded field (Assignee, Limit, RouteTarget/RouteMode) selects the server's
// strict cache-only fast-path shape, whose filters are applied BEFORE the
// limit and which fails closed (503) when the controller cache cannot answer.
type ReadyReadOpts struct {
	// Scope restricts the read to a single backing store (rig name or city
	// name); empty federates every store.
	Scope string
	// IncludeEphemeral spans both the durable issues tier and the ephemeral
	// wisps tier (TierBoth).
	IncludeEphemeral bool
	// Assignee restricts to ready work assigned to this identity — the
	// assigned-ready tier probe (set with Limit=1).
	Assignee string
	// Limit bounds the rows returned per store, applied AFTER the filters.
	Limit int
	// RouteTarget + RouteMode select the routed-pool tier: unassigned,
	// non-epic ready work routed to the target, oldest-first.
	RouteTarget string
	RouteMode   string
}

// BeadsReadyQuery fetches ready work via GET /v0/city/{cityName}/beads/ready
// with the bounded tier-specific parameters the hook fast path uses to
// reproduce the generated default work_query's assigned-ready and routed-pool
// probes without a worker-side SQL connection and without shipping the full
// ready set over the wire.
func (c *Client) BeadsReadyQuery(opts ReadyReadOpts) (CachedRead[[]beads.Bead], error) {
	if err := c.requireCityScope(); err != nil {
		return CachedRead[[]beads.Bead]{}, err
	}
	params := &genclient.GetV0CityByCityNameBeadsReadyParams{}
	if s := strings.TrimSpace(opts.Scope); s != "" {
		params.Rig = &s
	}
	if opts.IncludeEphemeral {
		t := true
		params.IncludeEphemeral = &t
	}
	if a := strings.TrimSpace(opts.Assignee); a != "" {
		params.Assignee = &a
	}
	if opts.Limit > 0 {
		n := int64(opts.Limit)
		params.Limit = &n
	}
	if rt := strings.TrimSpace(opts.RouteTarget); rt != "" {
		params.RouteTarget = &rt
		mode := genclient.GetV0CityByCityNameBeadsReadyParamsRouteMode(strings.TrimSpace(opts.RouteMode))
		params.RouteMode = &mode
	}
	resp, err := c.cw.GetV0CityByCityNameBeadsReadyWithResponse(
		context.Background(), c.cityName, params)
	if err != nil {
		return CachedRead[[]beads.Bead]{}, &connError{err: fmt.Errorf("request failed: %w", err)}
	}
	if resp == nil {
		return CachedRead[[]beads.Bead]{}, &connError{err: fmt.Errorf("nil response")}
	}
	if err := apiErrorFromResponse(resp.StatusCode(), pdOf(resp)); err != nil {
		return CachedRead[[]beads.Bead]{}, err
	}
	return CachedRead[[]beads.Bead]{
		Body:       beadsFromGenList(resp.JSON200),
		AgeSeconds: cacheAgeFromResponse(resp.HTTPResponse),
	}, nil
}
