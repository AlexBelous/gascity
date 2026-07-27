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
// A pre-request transport failure surfaces as a *connError (IsConnError true),
// the signal the fast path uses to fall back to the subprocess shell reads.
func (c *Client) BeadsReady(scope string) (CachedRead[[]beads.Bead], error) {
	if err := c.requireCityScope(); err != nil {
		return CachedRead[[]beads.Bead]{}, err
	}
	params := &genclient.GetV0CityByCityNameBeadsReadyParams{}
	if s := strings.TrimSpace(scope); s != "" {
		params.Rig = &s
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
