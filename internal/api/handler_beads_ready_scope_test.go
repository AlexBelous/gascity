package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gastownhall/gascity/internal/beads"
)

// readyTitles hits GET /beads/ready with an optional ?rig= scope and returns the
// set of bead titles the controller federated for that scope. Titles (not IDs)
// key the assertions because MemStore IDs restart per store, so each fixture
// would otherwise collide on gc-1.
func readyTitles(t *testing.T, h http.Handler, state State, scope string) map[string]bool {
	t.Helper()
	url := cityURL(state, "/beads/ready")
	if scope != "" {
		url += "?rig=" + scope
	}
	req := httptest.NewRequest(http.MethodGet, url, nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("ready(scope=%q) status = %d, want %d; body=%s", scope, rec.Code, http.StatusOK, rec.Body.String())
	}
	var body struct {
		Items []beads.Bead `json:"items"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode ready(scope=%q): %v", scope, err)
	}
	titles := make(map[string]bool, len(body.Items))
	for _, b := range body.Items {
		titles[b.Title] = true
	}
	return titles
}

// TestBeadReadyScopeFiltersToSingleStore proves the ?rig= scope restricts the
// federated ready read to one backing store — a rig name selects that rig, the
// city name selects the city store, and an empty scope federates every store.
// This is the server half of the invariant-2 per-store precedence the hook fast
// path relies on: without a real per-store filter, the fast path could not
// reproduce the legacy firstStoreWithWork STORE-outermost ordering.
func TestBeadReadyScopeFiltersToSingleStore(t *testing.T) {
	state := newFakeState(t)
	front := beads.NewMemStore()
	back := beads.NewMemStore()
	city := beads.NewMemStore()
	// MemStore restarts its gc-<n> counter per store, so bump each store's
	// sequence with closed throwaways before seeding the fixture. That gives the
	// three fixtures globally-unique IDs, so the federated read's same-ID dedup
	// (legacy file-mode alias guard) does not collapse them — a real multi-rig
	// city already has globally-unique bead IDs.
	seed := func(store *beads.MemStore, bump int, title string) {
		t.Helper()
		for i := 0; i < bump; i++ {
			b, err := store.Create(beads.Bead{Type: "task", Title: "throwaway"})
			if err != nil {
				t.Fatalf("bump %s: %v", title, err)
			}
			if err := store.Close(b.ID); err != nil { // closed → excluded from ready
				t.Fatalf("close throwaway %s: %v", b.ID, err)
			}
		}
		if _, err := store.Create(beads.Bead{Type: "task", Title: title}); err != nil {
			t.Fatalf("seed %s: %v", title, err)
		}
	}
	const frontTitle, backTitle, cityTitle = "frontend ready", "backend ready", "city ready"
	seed(front, 0, frontTitle)
	seed(back, 1, backTitle)
	seed(city, 2, cityTitle)

	state.stores = map[string]beads.Store{"frontend": front, "backend": back}
	state.cityBeadStore = city
	h := newTestCityHandler(t, state)

	t.Run("rig scope returns only that rig", func(t *testing.T) {
		titles := readyTitles(t, h, state, "frontend")
		if !titles[frontTitle] || titles[backTitle] || titles[cityTitle] {
			t.Fatalf("frontend scope titles = %v, want only %q", titles, frontTitle)
		}
	})
	t.Run("city scope returns only the city store", func(t *testing.T) {
		titles := readyTitles(t, h, state, state.CityName())
		if !titles[cityTitle] || titles[frontTitle] || titles[backTitle] {
			t.Fatalf("city scope titles = %v, want only %q", titles, cityTitle)
		}
	})
	t.Run("empty scope federates every store", func(t *testing.T) {
		titles := readyTitles(t, h, state, "")
		if !titles[frontTitle] || !titles[backTitle] || !titles[cityTitle] {
			t.Fatalf("federated titles = %v, want all three fixtures", titles)
		}
	})
	t.Run("unknown scope federates nothing", func(t *testing.T) {
		titles := readyTitles(t, h, state, "does-not-exist")
		if len(titles) != 0 {
			t.Fatalf("unknown scope titles = %v, want empty", titles)
		}
	})
}
