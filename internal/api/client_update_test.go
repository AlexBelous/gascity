package api

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
)

// TestClientUpdateBeadMetadataSuccess proves the wrapper POSTs the metadata
// patch to the per-city update route with the anti-CSRF header.
func TestClientUpdateBeadMetadataSuccess(t *testing.T) {
	var gotMethod, gotPath, gotCSRF string
	var gotBody map[string]any
	ts := newAPIClientTestServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotCSRF = r.Header.Get("X-GC-Request")
		json.NewDecoder(r.Body).Decode(&gotBody) //nolint:errcheck
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"status": "updated"}) //nolint:errcheck
	}))
	defer ts.Close()

	c := NewCityScopedClient(ts.URL, "alpha")
	err := c.UpdateBeadMetadata(context.Background(), "gc-1", map[string]string{"gc.work_branch": "work/gc-1"})
	if err != nil {
		t.Fatalf("UpdateBeadMetadata: %v", err)
	}
	if gotMethod != http.MethodPost || gotPath != "/v0/city/alpha/bead/gc-1/update" {
		t.Errorf("request = %s %s, want POST /v0/city/alpha/bead/gc-1/update", gotMethod, gotPath)
	}
	if gotCSRF == "" {
		t.Error("X-GC-Request header missing on mutation request")
	}
	md, _ := gotBody["metadata"].(map[string]any)
	if md["gc.work_branch"] != "work/gc-1" {
		t.Errorf("metadata = %v, want gc.work_branch=work/gc-1", gotBody["metadata"])
	}
}

// TestClientUpdateBeadMetadataEmptyPatchNoRequest proves an empty patch issues
// no request (nothing to write), so a compare-and-skipped stamp never hits the
// controller.
func TestClientUpdateBeadMetadataEmptyPatchNoRequest(t *testing.T) {
	called := false
	ts := newAPIClientTestServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	c := NewCityScopedClient(ts.URL, "alpha")
	if err := c.UpdateBeadMetadata(context.Background(), "gc-1", nil); err != nil {
		t.Fatalf("UpdateBeadMetadata(nil): %v", err)
	}
	if called {
		t.Error("empty patch issued an HTTP request, want none")
	}
}

// TestClientUpdateBeadMetadataConnError proves a transport failure surfaces as a
// *connError.
func TestClientUpdateBeadMetadataConnError(t *testing.T) {
	ts := newAPIClientTestServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := ts.URL
	ts.Close()

	c := NewCityScopedClient(url, "alpha")
	err := c.UpdateBeadMetadata(context.Background(), "gc-1", map[string]string{"k": "v"})
	if err == nil || !IsConnError(err) {
		t.Fatalf("err = %v, want a *connError", err)
	}
}
