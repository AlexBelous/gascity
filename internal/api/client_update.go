package api

import (
	"context"
	"fmt"

	"github.com/gastownhall/gascity/internal/api/genclient"
)

// UpdateBeadMetadata sets the given metadata key/value pairs on a bead via
// POST /v0/city/{cityName}/bead/{id}/update, so the hook fast path can stamp
// claim-time execution identity (gc.work_branch, gc.session_id/name) through the
// controller instead of opening its own SQL connection. An empty patch is a
// no-op (no request): the caller compare-and-skips, so nothing to write means
// nothing to send. A pre-request transport failure surfaces as a *connError.
func (c *Client) UpdateBeadMetadata(ctx context.Context, id string, patch map[string]string) error {
	if len(patch) == 0 {
		return nil
	}
	if err := c.requireCityScope(); err != nil {
		return err
	}
	md := patch
	params := &genclient.PostV0CityByCityNameBeadByIdUpdateParams{XGCRequest: "true"}
	body := genclient.PostV0CityByCityNameBeadByIdUpdateJSONRequestBody{Metadata: &md}
	resp, err := c.cw.PostV0CityByCityNameBeadByIdUpdateWithResponse(ctx, c.cityName, id, params, body)
	if err != nil {
		return &connError{err: fmt.Errorf("request failed: %w", err)}
	}
	if resp == nil {
		return &connError{err: fmt.Errorf("nil response")}
	}
	return apiErrorFromResponse(resp.StatusCode(), pdOf(resp))
}
