package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"time"

	"github.com/gastownhall/gascity/internal/beads"
)

// Per-domain Huma input/output types for the beads handler
// group. Split out of the original huma_types.go; mirrors the layout
// of huma_handlers_beads.go.

// --- Bead types ---

// BeadListInput is the Huma input for GET /v0/city/{cityName}/beads.
type BeadListInput struct {
	CityScope
	BlockingParam
	PaginationParam
	Status   string `query:"status" required:"false" doc:"Filter by bead status."`
	Type     string `query:"type" required:"false" doc:"Filter by bead type."`
	Label    string `query:"label" required:"false" doc:"Filter by label."`
	Assignee string `query:"assignee" required:"false" doc:"Filter by assignee."`
	Rig      string `query:"rig" required:"false" doc:"Filter by rig."`
	All      bool   `query:"all" required:"false" doc:"Include closed beads."`
	// IncludeEphemeral reads both the durable issues tier and the ephemeral
	// wisps tier (TierBoth). Empty/false keeps the caller's default tier. The
	// hook fast path sets it so ephemeral molecule/wisp work stays visible,
	// matching the generated query's --include-ephemeral probes.
	IncludeEphemeral bool `query:"include_ephemeral" required:"false" doc:"Include ephemeral (wisp) beads alongside durable ones."`
}

// BeadReadyInput is the Huma input for GET /v0/city/{cityName}/beads/ready.
type BeadReadyInput struct {
	CityScope
	BlockingParam
	// Rig scopes the ready read to a single backing store — a rig name or the
	// city name. Empty federates every store (the default). The hook fast path
	// sets it to reproduce the legacy per-store work-query precedence.
	Rig string `query:"rig" required:"false" doc:"Scope ready work to a single store (rig name or city name); empty federates all stores."`
	// IncludeEphemeral reads both the durable issues tier and the ephemeral
	// wisps tier (TierBoth). Empty/false keeps the historical TierIssues default
	// for other callers. The hook fast path sets it so ephemeral molecule/wisp
	// ready work stays visible, matching the generated query's --include-ephemeral.
	IncludeEphemeral bool `query:"include_ephemeral" required:"false" doc:"Include ephemeral (wisp) ready work alongside durable ready work."`
	// Assignee restricts the cache-served read to ready work assigned to this
	// identity. The hook fast path sets it (with Limit=1) to reproduce the
	// assigned-ready tier per identity/alias without shipping the full ready set.
	Assignee string `query:"assignee" required:"false" doc:"Restrict ready work to this assignee."`
	// Limit bounds the number of ready beads returned per store. The assignee /
	// route filter is applied BEFORE the limit so a routed candidate buried
	// behind non-matching ready work is not cut. Zero means unbounded.
	Limit int `query:"limit" required:"false" minimum:"0" doc:"Maximum ready beads returned per store; the filter is applied before the limit. 0 = unbounded."`
	// RouteTarget selects the routed-pool tier: only unassigned, non-epic ready
	// beads routed to this target, ordered oldest-first. RouteMode picks
	// canonical (gc.routed_to) vs migration (gc.run_target + gc.kind=workflow
	// with no gc.routed_to). Empty leaves the routed filter off.
	RouteTarget string `query:"route_target" required:"false" doc:"Routed-pool target; returns only unassigned non-epic ready work routed here, oldest-first."`
	// RouteMode selects how RouteTarget is matched. It is only honored with a
	// non-empty RouteTarget; empty leaves the routed filter off.
	RouteMode string `query:"route_mode" required:"false" enum:"canonical,migration" doc:"Routed-pool match mode for route_target: canonical (gc.routed_to) or migration (gc.run_target workflow root)."`
}

// BeadGraphInput is the Huma input for GET /v0/city/{cityName}/beads/graph/{rootID}.
type BeadGraphInput struct {
	CityScope
	RootID string `path:"rootID" doc:"Root bead ID for the graph."`
}

// BeadGetInput is the Huma input for GET /v0/city/{cityName}/bead/{id}.
type BeadGetInput struct {
	CityScope
	ID string `path:"id" doc:"Bead ID."`
}

// BeadDepsInput is the Huma input for GET /v0/city/{cityName}/bead/{id}/deps.
type BeadDepsInput struct {
	CityScope
	ID string `path:"id" doc:"Bead ID."`
}

// BeadCreateInput is the Huma input for POST /v0/city/{cityName}/beads.
type BeadCreateInput struct {
	CityScope
	IdempotencyKey string `header:"Idempotency-Key" required:"false" doc:"Idempotency key for safe retries."`
	Body           struct {
		Rig         string            `json:"rig,omitempty" doc:"Rig name."`
		Title       string            `json:"title" doc:"Bead title." minLength:"1"`
		Type        string            `json:"type,omitempty" doc:"Bead type."`
		Priority    *int              `json:"priority,omitempty" doc:"Bead priority."`
		Assignee    string            `json:"assignee,omitempty" doc:"Assigned agent."`
		Description string            `json:"description,omitempty" doc:"Bead description."`
		Labels      []string          `json:"labels,omitempty" doc:"Bead labels."`
		Parent      string            `json:"parent,omitempty" doc:"Parent bead ID."`
		Metadata    map[string]string `json:"metadata,omitempty" doc:"Metadata key-value pairs to set at create time."`
		DeferUntil  *time.Time        `json:"defer_until,omitempty" doc:"Hide the bead from ready views until this time."`
	}
}

// BeadCloseInput is the Huma input for POST /v0/city/{cityName}/bead/{id}/close.
type BeadCloseInput struct {
	CityScope
	ID string `path:"id" doc:"Bead ID."`
}

// BeadReopenInput is the Huma input for POST /v0/city/{cityName}/bead/{id}/reopen.
type BeadReopenInput struct {
	CityScope
	ID string `path:"id" doc:"Bead ID."`
}

// BeadUpdateInput is the Huma input for POST /v0/city/{cityName}/bead/{id}/update and PATCH /v0/city/{cityName}/bead/{id}.
type BeadUpdateInput struct {
	CityScope
	ID   string `path:"id" doc:"Bead ID."`
	Body beadUpdateBody
}

// beadUpdateBody is the request body for bead update/patch endpoints.
type beadUpdateBody struct {
	Title        *string           `json:"title,omitempty" doc:"Bead title."`
	Status       *string           `json:"status,omitempty" doc:"Bead status."`
	Type         *string           `json:"type,omitempty" doc:"Bead type."`
	Priority     *int              `json:"priority,omitempty" doc:"Bead priority."`
	Assignee     *string           `json:"assignee,omitempty" doc:"Assigned agent."`
	Description  *string           `json:"description,omitempty" doc:"Bead description."`
	Labels       []string          `json:"labels,omitempty" doc:"Bead labels."`
	RemoveLabels []string          `json:"remove_labels,omitempty" doc:"Labels to remove."`
	Parent       *string           `json:"parent,omitempty" nullable:"true" doc:"Parent bead ID. Use null or an empty string to clear."`
	Metadata     map[string]string `json:"metadata,omitempty" doc:"Metadata key-value pairs to set."`
	parentSet    bool
}

// UnmarshalJSON rejects `"priority": null` explicitly. Standard Go JSON decoding
// folds null and absent into a nil pointer, which silently drops clear-intent
// requests. Clients that want to clear priority must use a dedicated endpoint
// (not yet available); until then, null is a 400.
func (b *beadUpdateBody) UnmarshalJSON(data []byte) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	if p, ok := raw["priority"]; ok {
		trimmed := bytes.TrimSpace(p)
		if bytes.Equal(trimmed, []byte("null")) {
			return fmt.Errorf("clearing priority via null is not supported; omit the field to leave it unchanged")
		}
	}
	type alias beadUpdateBody
	var a alias
	if err := json.Unmarshal(data, &a); err != nil {
		return err
	}
	*b = beadUpdateBody(a)
	if p, ok := raw["parent"]; ok {
		b.parentSet = true
		if bytes.Equal(bytes.TrimSpace(p), []byte("null")) {
			parent := ""
			b.Parent = &parent
		}
	}
	return nil
}

// BeadAssignInput is the Huma input for POST /v0/city/{cityName}/bead/{id}/assign.
type BeadAssignInput struct {
	CityScope
	ID   string `path:"id" doc:"Bead ID."`
	Body struct {
		Assignee string `json:"assignee,omitempty" doc:"Assignee name."`
	}
}

// BeadClaimInput is the Huma input for POST /v0/city/{cityName}/bead/{id}/claim.
type BeadClaimInput struct {
	CityScope
	ID   string `path:"id" doc:"Bead ID."`
	Body struct {
		Actor string `json:"actor" doc:"Actor to claim the bead for (becomes the assignee)."`
	}
}

// BeadClaimResult is the response body for a bead claim. Claimed reports whether
// this actor won the bead; on a lost race Claimed is false with no error, and
// Bead is zero-valued. On success Bead carries the canonical post-claim bead so
// the caller need not re-read.
type BeadClaimResult struct {
	Claimed bool       `json:"claimed" doc:"True if this actor now owns the bead (assignee=actor, status=in_progress). False if another actor won or the bead was not claimable."`
	Bead    beads.Bead `json:"bead" doc:"The canonical bead after a successful claim; zero-valued when claimed is false."`
}

// BeadDeleteInput is the Huma input for DELETE /v0/city/{cityName}/bead/{id}.
type BeadDeleteInput struct {
	CityScope
	ID string `path:"id" doc:"Bead ID."`
}
