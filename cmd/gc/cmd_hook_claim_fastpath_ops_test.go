package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gastownhall/gascity/internal/api"
)

// TestAPIFastPathClaimSuccess proves the API-backed Claim op returns the claimed
// bead through the controller claim route.
func TestAPIFastPathClaimSuccess(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/claim") {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{ //nolint:errcheck
			"claimed": true,
			"bead":    map[string]any{"id": "gc-1", "assignee": "worker-1", "status": "in_progress"},
		})
	}))
	defer ts.Close()

	ops := newAPIFastPathClaimOps(api.NewCityScopedClient(ts.URL, "alpha"))
	ops.applyDefaults()
	bead, claimed, err := ops.Claim(context.Background(), "/wt", nil, "gc-1", "worker-1")
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	if !claimed || bead.ID != "gc-1" || bead.Assignee != "worker-1" {
		t.Fatalf("claim = (%+v, %v), want gc-1/worker-1 claimed", bead, claimed)
	}
}

// TestAPIFastPathClaimLostRaceReReadsOwner proves that on a lost race the op
// re-reads the bead so the caller can surface who won in the claim_rejected
// event, mirroring the bd path.
func TestAPIFastPathClaimLostRaceReReadsOwner(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/claim") {
			json.NewEncoder(w).Encode(map[string]any{"claimed": false, "bead": map[string]any{}}) //nolint:errcheck
			return
		}
		// GET /v0/city/alpha/bead/gc-1 — the re-read for the winner's identity.
		json.NewEncoder(w).Encode(map[string]any{ //nolint:errcheck
			"id": "gc-1", "assignee": "winner-2", "status": "in_progress",
		})
	}))
	defer ts.Close()

	ops := newAPIFastPathClaimOps(api.NewCityScopedClient(ts.URL, "alpha"))
	ops.applyDefaults()
	bead, claimed, err := ops.Claim(context.Background(), "/wt", nil, "gc-1", "worker-1")
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	if claimed {
		t.Fatal("claimed = true for the loser, want false")
	}
	if bead.Assignee != "winner-2" {
		t.Fatalf("lost-race bead assignee = %q, want winner-2 (re-read owner)", bead.Assignee)
	}
}

// TestAPIFastPathStampWorkMeta proves the identity-metadata stamp routes through
// the controller update route.
func TestAPIFastPathStampWorkMeta(t *testing.T) {
	var gotBody map[string]any
	var gotPath string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		json.NewDecoder(r.Body).Decode(&gotBody) //nolint:errcheck
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"status": "updated"}) //nolint:errcheck
	}))
	defer ts.Close()

	ops := newAPIFastPathClaimOps(api.NewCityScopedClient(ts.URL, "alpha"))
	ops.applyDefaults()
	err := ops.StampWorkMeta(context.Background(), "/wt", nil, "gc-1", "worker-1",
		map[string]string{"gc.session_id": "gc-603691"})
	if err != nil {
		t.Fatalf("StampWorkMeta: %v", err)
	}
	if gotPath != "/v0/city/alpha/bead/gc-1/update" {
		t.Errorf("path = %q, want the update route", gotPath)
	}
	md, _ := gotBody["metadata"].(map[string]any)
	if md["gc.session_id"] != "gc-603691" {
		t.Errorf("stamped metadata = %v, want gc.session_id=gc-603691", gotBody["metadata"])
	}
}
