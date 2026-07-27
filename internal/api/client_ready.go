package api

import (
	"context"
	"fmt"

	"github.com/gastownhall/gascity/internal/api/genclient"
	"github.com/gastownhall/gascity/internal/beads"
)

// BeadsReady fetches the city's federated ready work via
// GET /v0/city/{cityName}/beads/ready — the controller-side equivalent of
// `bd ready` across the city and every rig store. It is the read the hook fast
// path filters into the assigned-ready and routed-pool tiers, so a worker never
// opens its own SQL connection to discover ready work. Empty params make the
// read non-blocking (no Index → immediate snapshot).
//
// A pre-request transport failure surfaces as a *connError (IsConnError true),
// the signal the fast path uses to fall back to the subprocess shell reads.
func (c *Client) BeadsReady() (CachedRead[[]beads.Bead], error) {
	if err := c.requireCityScope(); err != nil {
		return CachedRead[[]beads.Bead]{}, err
	}
	resp, err := c.cw.GetV0CityByCityNameBeadsReadyWithResponse(
		context.Background(), c.cityName, &genclient.GetV0CityByCityNameBeadsReadyParams{})
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
