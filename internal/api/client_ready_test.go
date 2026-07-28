package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestClientBeadsReadySuccess proves the wrapper reads the per-city ready route
// and maps the list body into []beads.Bead.
func TestClientBeadsReadySuccess(t *testing.T) {
	var gotPath, gotRig string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotRig = r.URL.Query().Get("rig")
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{ //nolint:errcheck
			"items": []map[string]any{
				{"id": "gc-a", "issue_type": "task", "assignee": "worker-1"},
				{"id": "gc-b", "issue_type": "epic"},
			},
			"total": 2,
		})
	}))
	defer ts.Close()

	c := NewCityScopedClient(ts.URL, "alpha")
	got, err := c.BeadsReady("", false)
	if err != nil {
		t.Fatalf("BeadsReady: %v", err)
	}
	if gotPath != "/v0/city/alpha/beads/ready" {
		t.Errorf("path = %q, want /v0/city/alpha/beads/ready", gotPath)
	}
	if gotRig != "" {
		t.Errorf("rig query = %q, want empty for a federated read", gotRig)
	}
	if len(got.Body) != 2 {
		t.Fatalf("got %d ready beads, want 2", len(got.Body))
	}
	if got.Body[0].ID != "gc-a" || got.Body[0].Assignee != "worker-1" {
		t.Errorf("bead[0] = %+v, want gc-a/worker-1", got.Body[0])
	}
	if got.Body[1].Type != "epic" {
		t.Errorf("bead[1] type = %q, want epic", got.Body[1].Type)
	}
}

// TestClientBeadsReadyConnErrorIsClassified proves a transport failure
// surfaces as a *connError so the fast path can fail closed.
func TestClientBeadsReadyConnErrorIsClassified(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := ts.URL
	ts.Close()

	c := NewCityScopedClient(url, "alpha")
	_, err := c.BeadsReady("", false)
	if err == nil {
		t.Fatal("BeadsReady err = nil on a dead server, want a transport error")
	}
	if !IsConnError(err) {
		t.Fatalf("IsConnError = false for %v (%T), want true", err, err)
	}
}

// TestClientBeadsReadyScopePassesRigParam proves a non-empty scope is sent as
// the ?rig= query param so the controller federates only that one store.
func TestClientBeadsReadyScopePassesRigParam(t *testing.T) {
	var gotRig string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotRig = r.URL.Query().Get("rig")
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"items": []map[string]any{}, "total": 0}) //nolint:errcheck
	}))
	defer ts.Close()

	c := NewCityScopedClient(ts.URL, "alpha")
	if _, err := c.BeadsReady("frontend", false); err != nil {
		t.Fatalf("BeadsReady: %v", err)
	}
	if gotRig != "frontend" {
		t.Errorf("rig query = %q, want frontend", gotRig)
	}
}

// TestClientBeadsReadyQuerySerializesBoundedParams pins the exact wire shape of
// the bounded fast-path read: every ReadyReadOpts field maps to its query param
// verbatim (rig, include_ephemeral, assignee, limit, route_target, route_mode),
// and the zero value sends none of them, preserving the historical unbounded
// read. Route-shape validation stays server-side (Huma) — the client only pins
// serialization.
func TestClientBeadsReadyQuerySerializesBoundedParams(t *testing.T) {
	var gotQuery map[string][]string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.Query()
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"items": []map[string]any{}, "total": 0}) //nolint:errcheck
	}))
	defer ts.Close()
	c := NewCityScopedClient(ts.URL, "alpha")

	if _, err := c.BeadsReadyQuery(ReadyReadOpts{
		Scope:            "myrig",
		IncludeEphemeral: true,
		Assignee:         "worker-1",
		Limit:            20,
		RouteTarget:      "pool-x",
		RouteMode:        RouteModeCanonical,
	}); err != nil {
		t.Fatalf("BeadsReadyQuery(full opts): %v", err)
	}
	want := map[string]string{
		"rig":               "myrig",
		"include_ephemeral": "true",
		"assignee":          "worker-1",
		"limit":             "20",
		"route_target":      "pool-x",
		"route_mode":        "canonical",
	}
	if len(gotQuery) != len(want) {
		t.Fatalf("query params = %v, want exactly %v", gotQuery, want)
	}
	for k, v := range want {
		if got := gotQuery[k]; len(got) != 1 || got[0] != v {
			t.Errorf("query %s = %v, want [%s]", k, got, v)
		}
	}

	gotQuery = nil
	if _, err := c.BeadsReadyQuery(ReadyReadOpts{}); err != nil {
		t.Fatalf("BeadsReadyQuery(zero opts): %v", err)
	}
	if len(gotQuery) != 0 {
		t.Errorf("zero-value opts sent params %v, want none (historical unbounded read)", gotQuery)
	}

	gotQuery = nil
	if _, err := c.BeadsReadyQuery(ReadyReadOpts{Limit: -3}); err != nil {
		t.Fatalf("BeadsReadyQuery(negative limit): %v", err)
	}
	if len(gotQuery) != 0 {
		t.Errorf("nonpositive limit sent params %v, want limit omitted", gotQuery)
	}
}

// TestClientBeadsReadyIncludeEphemeralPassesParam proves includeEphemeral=true is
// sent as ?include_ephemeral=true so the controller reads both tiers; false omits
// it, preserving the historical default for other callers.
func TestClientBeadsReadyIncludeEphemeralPassesParam(t *testing.T) {
	var gotEph string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotEph = r.URL.Query().Get("include_ephemeral")
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"items": []map[string]any{}, "total": 0}) //nolint:errcheck
	}))
	defer ts.Close()

	c := NewCityScopedClient(ts.URL, "alpha")
	if _, err := c.BeadsReady("", true); err != nil {
		t.Fatalf("BeadsReady: %v", err)
	}
	if gotEph != "true" {
		t.Errorf("include_ephemeral query = %q, want true", gotEph)
	}

	gotEph = ""
	if _, err := c.BeadsReady("", false); err != nil {
		t.Fatalf("BeadsReady: %v", err)
	}
	if gotEph != "" {
		t.Errorf("include_ephemeral query = %q, want empty when not requested", gotEph)
	}
}
