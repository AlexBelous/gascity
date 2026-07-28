package main

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/api"
)

// TestAPIFastPathClaimSuccess proves the API-backed Claim op returns the claimed
// bead through the controller claim route.
func TestAPIFastPathClaimSuccess(t *testing.T) {
	ts := newGCClientTestServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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

// TestAPIFastPathClaimAmbiguousTimeoutRetriesSameBeadActor pins the canonical
// response-loss rule: once a claim request may have reached the controller, the
// hook may retry only that exact bead and actor. It must not return an ordinary
// candidate error that lets claimFirstEligibleHookCandidate move to another
// bead while the first request can still commit.
func TestAPIFastPathClaimAmbiguousTimeoutRetriesSameBeadActor(t *testing.T) {
	var (
		mu     sync.Mutex
		paths  []string
		actors []string
		calls  int
	)
	ts := newGCClientTestServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Actor string `json:"actor"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		calls++
		call := calls
		paths = append(paths, r.URL.Path)
		actors = append(actors, body.Actor)
		mu.Unlock()

		if call == 1 {
			<-r.Context().Done()
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"claimed": true,
			"bead":    map[string]any{"id": "gc-ambiguous", "assignee": "worker-1", "status": "in_progress"},
		})
	}))
	defer ts.Close()

	oldTimeout := hookClaimMutationTimeout
	hookClaimMutationTimeout = 500 * time.Millisecond
	t.Cleanup(func() { hookClaimMutationTimeout = oldTimeout })

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	claim := apiFastPathClaim(api.NewCityScopedClient(ts.URL, "alpha"))
	bead, claimed, err := claim(ctx, "/wt", nil, "gc-ambiguous", "worker-1")
	if err != nil {
		t.Fatalf("Claim after same-actor retry: %v", err)
	}
	if !claimed || bead.ID != "gc-ambiguous" || bead.Assignee != "worker-1" {
		t.Fatalf("claim = (%+v, %t), want gc-ambiguous owned by worker-1", bead, claimed)
	}
	mu.Lock()
	defer mu.Unlock()
	if calls != 2 {
		t.Fatalf("claim calls = %d, want initial request + one retry", calls)
	}
	for i := range paths {
		if paths[i] != "/v0/city/alpha/bead/gc-ambiguous/claim" || actors[i] != "worker-1" {
			t.Fatalf("claim call %d = path %q actor %q, want same bead and actor", i+1, paths[i], actors[i])
		}
	}
}

func TestAPIFastPathClaimAmbiguousRetryFailureStopsCandidateWalk(t *testing.T) {
	var (
		mu     sync.Mutex
		paths  []string
		actors []string
	)
	ts := newGCClientTestServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		var body struct {
			Actor string `json:"actor"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		paths = append(paths, r.URL.Path)
		actors = append(actors, body.Actor)
		mu.Unlock()
		<-r.Context().Done()
	}))
	defer ts.Close()

	oldTimeout := hookClaimMutationTimeout
	hookClaimMutationTimeout = 30 * time.Millisecond
	t.Cleanup(func() { hookClaimMutationTimeout = oldTimeout })

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	claim := apiFastPathClaim(api.NewCityScopedClient(ts.URL, "alpha"))
	_, mayHaveCommitted, err := claim(ctx, "/wt", nil, "gc-ambiguous", "worker-1")
	if err == nil || !strings.Contains(err.Error(), "same-actor retry failed") {
		t.Fatalf("Claim error = %v, want explicit ambiguous same-actor retry failure", err)
	}
	if !mayHaveCommitted {
		t.Fatal("mayHaveCommitted = false; caller could walk to a different candidate after an ambiguous claim")
	}
	mu.Lock()
	defer mu.Unlock()
	if len(paths) != 2 {
		t.Fatalf("claim calls = %d, want initial request + one retry", len(paths))
	}
	for i := range paths {
		if paths[i] != "/v0/city/alpha/bead/gc-ambiguous/claim" || actors[i] != "worker-1" {
			t.Fatalf("claim call %d = path %q actor %q, want same bead and actor", i+1, paths[i], actors[i])
		}
	}
}

// TestAPIFastPathClaimLostRaceReReadsOwner proves that on a lost race the op
// re-reads the bead so the caller can surface who won in the claim_rejected
// event, mirroring the bd path.
func TestAPIFastPathClaimLostRaceReReadsOwner(t *testing.T) {
	ts := newGCClientTestServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
	ts := newGCClientTestServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
