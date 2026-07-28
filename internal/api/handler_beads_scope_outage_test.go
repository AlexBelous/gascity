package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gastownhall/gascity/internal/beads"
)

// scopedGetStatus issues GET path with an optional ?rig= scope and an
// include_ephemeral toggle, returning the HTTP status code.
func scopedGetStatus(t *testing.T, h http.Handler, fs *fakeState, path, scope string, includeEphemeral bool) int {
	t.Helper()
	url := cityURL(fs, path)
	var q []string
	if scope != "" {
		q = append(q, "rig="+scope)
	}
	if includeEphemeral {
		q = append(q, "include_ephemeral=true")
	}
	if len(q) > 0 {
		url += "?" + strings.Join(q, "&")
	}
	req := httptest.NewRequest(http.MethodGet, url, nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec.Code
}

// TestScopedReadConfiguredStoreOutageSurfaces proves a scoped bead read of a
// CONFIGURED-but-unavailable store — a present store-map entry with a nil value,
// or a nil city store — surfaces a 503 outage on BOTH the ready and list handlers
// instead of a false-empty 200 the hook fast path would read as a no-work drain
// (and instead of a nil store.List panic on the list path). A genuinely UNKNOWN
// scope still returns 200 empty, preserving the existing contract.
func TestScopedReadConfiguredStoreOutageSurfaces(t *testing.T) {
	withNilRig := func(t *testing.T) *fakeState {
		fs := newFakeState(t)
		fs.cityBeadStore = beads.NewMemStore()
		fs.stores["outaged"] = nil // configured rig whose store failed to open
		return fs
	}

	t.Run("ready: nil rig store -> 503", func(t *testing.T) {
		fs := withNilRig(t)
		h := newTestCityHandler(t, fs)
		if code := scopedGetStatus(t, h, fs, "/beads/ready", "outaged", true); code != http.StatusServiceUnavailable {
			t.Fatalf("ready(rig=outaged) = %d, want 503 (an empty 200 would false-drain the hook)", code)
		}
	})

	t.Run("list(include_ephemeral): nil rig store -> 503, not a nil store.List panic", func(t *testing.T) {
		fs := withNilRig(t)
		h := newTestCityHandler(t, fs)
		if code := scopedGetStatus(t, h, fs, "/beads", "outaged", true); code != http.StatusServiceUnavailable {
			t.Fatalf("list(rig=outaged) = %d, want 503", code)
		}
	})

	t.Run("ready: nil city store -> 503 for city scope", func(t *testing.T) {
		fs := newFakeState(t)
		fs.cityBeadStore = nil
		h := newTestCityHandler(t, fs)
		if code := scopedGetStatus(t, h, fs, "/beads/ready", fs.CityName(), false); code != http.StatusServiceUnavailable {
			t.Fatalf("ready(rig=%s) with nil city store = %d, want 503", fs.CityName(), code)
		}
	})

	t.Run("ready: unknown scope stays 200 empty", func(t *testing.T) {
		fs := newFakeState(t)
		fs.cityBeadStore = beads.NewMemStore()
		h := newTestCityHandler(t, fs)
		if code := scopedGetStatus(t, h, fs, "/beads/ready", "does-not-exist", true); code != http.StatusOK {
			t.Fatalf("ready(rig=does-not-exist) = %d, want 200 empty", code)
		}
	})

	t.Run("list: unknown scope stays 200 empty", func(t *testing.T) {
		fs := newFakeState(t)
		fs.cityBeadStore = beads.NewMemStore()
		h := newTestCityHandler(t, fs)
		if code := scopedGetStatus(t, h, fs, "/beads", "does-not-exist", true); code != http.StatusOK {
			t.Fatalf("list(rig=does-not-exist) = %d, want 200 empty", code)
		}
	})
}
