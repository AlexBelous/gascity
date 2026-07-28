package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestClientClaimBeadSuccess proves the wrapper POSTs to the per-city claim
// route with the actor body and the anti-CSRF header, and maps a claimed
// result back to (bead, true, nil).
func TestClientClaimBeadSuccess(t *testing.T) {
	var gotMethod, gotPath, gotCSRF string
	var gotBody map[string]any

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotCSRF = r.Header.Get("X-GC-Request")
		json.NewDecoder(r.Body).Decode(&gotBody) //nolint:errcheck
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{ //nolint:errcheck
			"claimed": true,
			"bead": map[string]any{
				"id":       "gc-1",
				"assignee": "worker-1",
				"status":   "in_progress",
			},
		})
	}))
	defer ts.Close()

	c := NewCityScopedClient(ts.URL, "alpha")
	bead, claimed, err := c.ClaimBead(context.Background(), "gc-1", "worker-1")
	if err != nil {
		t.Fatalf("ClaimBead: %v", err)
	}
	if !claimed {
		t.Fatal("claimed = false, want true")
	}
	if bead.ID != "gc-1" || bead.Assignee != "worker-1" || bead.Status != "in_progress" {
		t.Fatalf("bead = %+v, want gc-1/worker-1/in_progress", bead)
	}
	if gotMethod != http.MethodPost {
		t.Errorf("method = %q, want POST", gotMethod)
	}
	if gotPath != "/v0/city/alpha/bead/gc-1/claim" {
		t.Errorf("path = %q, want /v0/city/alpha/bead/gc-1/claim", gotPath)
	}
	if gotCSRF == "" {
		t.Error("X-GC-Request header missing on mutation request")
	}
	if gotBody["actor"] != "worker-1" {
		t.Errorf("body actor = %v, want worker-1", gotBody["actor"])
	}
}

// TestClientClaimBeadLostRace proves a not-claimed result is (Bead{}, false,
// nil): losing a claim race is never an error.
func TestClientClaimBeadLostRace(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{ //nolint:errcheck
			"claimed": false,
			"bead":    map[string]any{},
		})
	}))
	defer ts.Close()

	c := NewCityScopedClient(ts.URL, "alpha")
	bead, claimed, err := c.ClaimBead(context.Background(), "gc-1", "worker-1")
	if err != nil {
		t.Fatalf("ClaimBead returned err = %v, want nil (lost race is not an error)", err)
	}
	if claimed {
		t.Fatal("claimed = true, want false for the loser")
	}
	if bead.ID != "" {
		t.Fatalf("bead = %+v, want zero value on lost race", bead)
	}
}

// TestClientClaimBeadConnErrorIsClassified proves a pre-request transport
// failure surfaces as a *connError so the hook can distinguish transport
// ambiguity while still failing closed.
func TestClientClaimBeadConnErrorIsClassified(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := ts.URL
	ts.Close() // nothing listening: the request cannot connect.

	c := NewCityScopedClient(url, "alpha")
	_, claimed, err := c.ClaimBead(context.Background(), "gc-1", "worker-1")
	if err == nil {
		t.Fatal("ClaimBead err = nil, want a transport error")
	}
	if claimed {
		t.Fatal("claimed = true on transport failure, want false")
	}
	if !IsConnError(err) {
		t.Fatalf("IsConnError = false for err %v (%T), want true", err, err)
	}
}

// TestClientClaimBeadAdmission503IsNotConnError proves admission saturation
// (503) is a definite server verdict, NOT a transport failure: the fast path
// must fail fast on it rather than shelling out and multiplying pressure.
func TestClientClaimBeadAdmission503IsNotConnError(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/problem+json")
		w.WriteHeader(http.StatusServiceUnavailable)
		json.NewEncoder(w).Encode(map[string]any{ //nolint:errcheck
			"status": 503,
			"detail": "claim admission saturated; retry",
			"title":  "Service Unavailable",
		})
	}))
	defer ts.Close()

	c := NewCityScopedClient(ts.URL, "alpha")
	_, claimed, err := c.ClaimBead(context.Background(), "gc-1", "worker-1")
	if err == nil {
		t.Fatal("ClaimBead err = nil on 503, want an error")
	}
	if claimed {
		t.Fatal("claimed = true on 503, want false")
	}
	if IsConnError(err) {
		t.Fatalf("IsConnError = true for admission 503 (%v); the fast path would wrongly shell out", err)
	}
}

// TestClientClaimBeadRequiresCityScope proves a supervisor-scope client rejects
// the per-city claim rather than building a /v0/city//bead/... request.
func TestClientClaimBeadRequiresCityScope(t *testing.T) {
	c := NewClient("http://127.0.0.1:0")
	_, _, err := c.ClaimBead(context.Background(), "gc-1", "worker-1")
	if err == nil {
		t.Fatal("ClaimBead on a supervisor-scope client err = nil, want a city-scope error")
	}
}
