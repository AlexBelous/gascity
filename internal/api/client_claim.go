package api

import (
	"context"
	"fmt"

	"github.com/gastownhall/gascity/internal/api/genclient"
	"github.com/gastownhall/gascity/internal/beads"
)

// ClaimBead atomically claims bead id for actor through the controller's pooled
// store (POST /v0/city/{cityName}/bead/{id}/claim), so a worker hook never opens
// its own SQL connection to claim. It mirrors beads.AtomicClaimer.ClaimBead:
//
//   - (bead, true, nil): actor now owns the bead. Idempotent for the same actor.
//   - (Bead{}, false, nil): another actor won or the bead was not claimable — a
//     lost race is never an error.
//   - (Bead{}, false, err): a hard failure. The hook fast path fails closed for
//     every error and never opens an independent BdStore. A transport error
//     after dial is ambiguous and may be retried only for the same bead and
//     actor; an admission-saturation 503, a 409, or a 500 is a definite server
//     verdict and must not be replayed.
func (c *Client) ClaimBead(ctx context.Context, id, actor string) (beads.Bead, bool, error) {
	if err := c.requireCityScope(); err != nil {
		return beads.Bead{}, false, err
	}
	params := &genclient.PostV0CityByCityNameBeadByIdClaimParams{XGCRequest: "true"}
	body := genclient.PostV0CityByCityNameBeadByIdClaimJSONRequestBody{Actor: actor}
	resp, err := c.cw.PostV0CityByCityNameBeadByIdClaimWithResponse(ctx, c.cityName, id, params, body)
	if err != nil {
		return beads.Bead{}, false, &connError{err: fmt.Errorf("request failed: %w", err)}
	}
	if resp == nil {
		return beads.Bead{}, false, &connError{err: fmt.Errorf("nil response")}
	}
	if err := apiErrorFromResponse(resp.StatusCode(), pdOf(resp)); err != nil {
		return beads.Bead{}, false, err
	}
	if resp.JSON200 == nil {
		return beads.Bead{}, false, fmt.Errorf("API returned %d with no body", resp.StatusCode())
	}
	return beadFromGen(resp.JSON200.Bead), resp.JSON200.Claimed, nil
}
