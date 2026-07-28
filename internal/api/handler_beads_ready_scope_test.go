package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/beadmeta"
	"github.com/gastownhall/gascity/internal/beads"
)

// readyTitles hits GET /beads/ready with an optional ?rig= scope and returns the
// set of bead titles the controller federated for that scope. Titles (not IDs)
// key the assertions because MemStore IDs restart per store, so each fixture
// would otherwise collide on gc-1.
func readyTitles(t *testing.T, h http.Handler, state State, scope string) map[string]bool {
	t.Helper()
	return readyTitlesQuery(t, h, state, scope, false)
}

// readyTitlesQuery is readyTitles with an explicit include_ephemeral toggle.
func readyTitlesQuery(t *testing.T, h http.Handler, state State, scope string, includeEphemeral bool) map[string]bool {
	t.Helper()
	url := cityURL(state, "/beads/ready")
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

// TestBeadReadyIncludeEphemeralSurfacesWispWork proves the include_ephemeral
// query param actually reads the ephemeral wisps tier: an ephemeral ready bead
// is invisible to the default read (TierIssues) and visible only when
// include_ephemeral=true (TierBoth). This is the server half of the fast path's
// ephemeral-visibility fix — without it, the generated query's --include-ephemeral
// probes would find work the fast path could not.
func TestBeadReadyIncludeEphemeralSurfacesWispWork(t *testing.T) {
	state := newFakeState(t)
	store := beads.NewMemStore()
	if _, err := store.Create(beads.Bead{Type: "task", Title: "durable ready"}); err != nil {
		t.Fatalf("seed durable: %v", err)
	}
	if _, err := store.Create(beads.Bead{Type: "task", Title: "ephemeral ready", Ephemeral: true}); err != nil {
		t.Fatalf("seed ephemeral: %v", err)
	}
	state.stores = map[string]beads.Store{"myrig": store}
	state.cityBeadStore = beads.NewMemStore()
	h := newTestCityHandler(t, state)

	t.Run("default read omits ephemeral", func(t *testing.T) {
		titles := readyTitlesQuery(t, h, state, "", false)
		if !titles["durable ready"] {
			t.Fatalf("default read titles = %v, want the durable bead", titles)
		}
		if titles["ephemeral ready"] {
			t.Fatalf("default read titles = %v, want ephemeral EXCLUDED (TierIssues)", titles)
		}
	})
	t.Run("include_ephemeral surfaces the wisp", func(t *testing.T) {
		titles := readyTitlesQuery(t, h, state, "", true)
		if !titles["durable ready"] || !titles["ephemeral ready"] {
			t.Fatalf("include_ephemeral titles = %v, want BOTH durable and ephemeral", titles)
		}
	})
}

// backingReadyCounter wraps a store and counts live Ready reads, so an
// API-level test can prove the bounded fast-path shape never touches the
// backing store while the zero-param shape still does.
type backingReadyCounter struct {
	beads.Store
	readyCalls atomic.Int64
}

func (s *backingReadyCounter) Ready(query ...beads.ReadyQuery) ([]beads.Bead, error) {
	s.readyCalls.Add(1)
	return s.Store.Ready(query...)
}

// readyStrictSeed builds a warm CachingStore over a counted backing holding 25
// unrelated older unassigned ready beads plus one NEWER bead routed to pool-x —
// the buried-eligible-row fixture: a limit-20 read that filtered after the
// limit would never reach the match.
func readyStrictSeed(t *testing.T) (*backingReadyCounter, *beads.CachingStore) {
	t.Helper()
	base := time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)
	var seed []beads.Bead
	for i := 0; i < 25; i++ {
		seed = append(seed, beads.Bead{
			ID: "gc-noise-" + string(rune('a'+i)), Title: "noise", Status: "open", Type: "task",
			CreatedAt: base.Add(time.Duration(i) * time.Minute),
			Metadata:  beads.StringMap{beadmeta.RoutedToMetadataKey: "pool-other"},
		})
	}
	seed = append(seed, beads.Bead{
		ID: "gc-buried-match", Title: "buried match", Status: "open", Type: "task",
		CreatedAt: base.Add(999 * time.Minute),
		Metadata:  beads.StringMap{beadmeta.RoutedToMetadataKey: "pool-x"},
	})
	backing := &backingReadyCounter{Store: beads.NewMemStoreFrom(len(seed), seed, nil)}
	cache := beads.NewCachingStoreForTest(backing, nil)
	if err := cache.Prime(context.Background()); err != nil {
		t.Fatalf("Prime: %v", err)
	}
	return backing, cache
}

func readyGet(t *testing.T, h http.Handler, state State, query string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, cityURL(state, "/beads/ready")+query, nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// TestBeadReadyBoundedShapeStrictCacheOnly is the API-level half of the P1-2
// cure: a warm controller cache serves the bounded routed fast-path shape with
// the filter applied BEFORE the limit (the eligible routed row buried behind 25
// unrelated ready rows is found) and with ZERO live backing Ready reads.
func TestBeadReadyBoundedShapeStrictCacheOnly(t *testing.T) {
	state := newFakeState(t)
	backing, cache := readyStrictSeed(t)
	state.stores = map[string]beads.Store{"myrig": cache}
	state.cityBeadStore = beads.NewMemStore()
	h := newTestCityHandler(t, state)
	backing.readyCalls.Store(0) // ignore priming-time reads; the cure is steady-state

	rec := readyGet(t, h, state, "?rig=myrig&include_ephemeral=true&route_target=pool-x&route_mode=canonical&limit=20")
	if rec.Code != http.StatusOK {
		t.Fatalf("bounded routed read status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var body struct {
		Items []beads.Bead `json:"items"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Items) != 1 || body.Items[0].ID != "gc-buried-match" {
		t.Fatalf("items = %+v, want exactly the buried routed match (filter before limit)", body.Items)
	}
	if n := backing.readyCalls.Load(); n != 0 {
		t.Fatalf("backing Ready called %d times, want 0 (strict cache-only)", n)
	}
}

// TestBeadReadyBoundedShapePrimeActiveOnlyCacheServes pins the paused-rig
// resume window: a CachingStore that has only completed PrimeActive (partial
// state, full prime still pending) must serve the bounded fast-path shape with
// 200 and ZERO live backing Ready reads, because PrimeActive's active
// projection carries per-candidate dependency coverage. Without this, every
// worker hook arriving before the async full prime finished was a spurious 503.
func TestBeadReadyBoundedShapePrimeActiveOnlyCacheServes(t *testing.T) {
	state := newFakeState(t)
	seed := []beads.Bead{{
		ID: "gc-routed", Title: "routed", Status: "open", Type: "task",
		CreatedAt: time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC),
		Metadata:  beads.StringMap{beadmeta.RoutedToMetadataKey: "pool-x"},
	}}
	backing := &backingReadyCounter{Store: beads.NewMemStoreFrom(len(seed), seed, nil)}
	cache := beads.NewCachingStoreForTest(backing, nil)
	if err := cache.PrimeActive(); err != nil { // partial prime ONLY, no full Prime
		t.Fatalf("PrimeActive: %v", err)
	}
	state.stores = map[string]beads.Store{"myrig": cache}
	state.cityBeadStore = beads.NewMemStore()
	h := newTestCityHandler(t, state)
	backing.readyCalls.Store(0)

	rec := readyGet(t, h, state, "?rig=myrig&include_ephemeral=true&route_target=pool-x&route_mode=canonical&limit=20")
	if rec.Code != http.StatusOK {
		t.Fatalf("bounded read on PrimeActive-only cache status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var body struct {
		Items []beads.Bead `json:"items"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Items) != 1 || body.Items[0].ID != "gc-routed" {
		t.Fatalf("items = %+v, want exactly the routed match from the partial cache", body.Items)
	}
	if n := backing.readyCalls.Load(); n != 0 {
		t.Fatalf("backing Ready called %d times, want 0 (partial cache must serve without backing I/O)", n)
	}
}

// TestBeadReadyBoundedShapeFailsClosedWhenCacheUnavailable pins the fail-closed
// contract at the API boundary: an unprimed cache makes the bounded shape a 503
// with ZERO backing reads — never a silent live fallback, never a prime.
func TestBeadReadyBoundedShapeFailsClosedWhenCacheUnavailable(t *testing.T) {
	state := newFakeState(t)
	seed := []beads.Bead{{
		ID: "gc-live", Title: "live only", Status: "open", Type: "task",
		Metadata: beads.StringMap{beadmeta.RoutedToMetadataKey: "pool-x"},
	}}
	backing := &backingReadyCounter{Store: beads.NewMemStoreFrom(len(seed), seed, nil)}
	cold := beads.NewCachingStoreForTest(backing, nil) // deliberately unprimed
	state.stores = map[string]beads.Store{"myrig": cold}
	state.cityBeadStore = beads.NewMemStore()
	h := newTestCityHandler(t, state)

	rec := readyGet(t, h, state, "?rig=myrig&route_target=pool-x&route_mode=canonical&limit=20")
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("bounded read on cold cache status = %d, want 503; body=%s", rec.Code, rec.Body.String())
	}
	if n := backing.readyCalls.Load(); n != 0 {
		t.Fatalf("backing Ready called %d times, want 0 (fail closed, no live fallback)", n)
	}

	// The zero-param historical shape against the SAME cold store keeps its live
	// read: 200 with the backing row, served by a backing Ready call.
	rec = readyGet(t, h, state, "?rig=myrig")
	if rec.Code != http.StatusOK {
		t.Fatalf("zero-param read status = %d, want 200 (historical live behavior); body=%s", rec.Code, rec.Body.String())
	}
	var body struct {
		Items []beads.Bead `json:"items"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Items) != 1 || body.Items[0].ID != "gc-live" {
		t.Fatalf("zero-param items = %+v, want the live backing row", body.Items)
	}
	if n := backing.readyCalls.Load(); n == 0 {
		t.Fatal("zero-param read issued no backing Ready call; historical live behavior regressed")
	}
}

// TestBeadReadyBoundedShapeParamValidation pins the routed-param contract:
// route_target and route_mode must travel together and route_mode must be a
// known mode, so a malformed probe fails loudly instead of degrading into an
// unfiltered (and unbounded-looking) read.
func TestBeadReadyBoundedShapeParamValidation(t *testing.T) {
	state := newFakeState(t)
	state.stores = map[string]beads.Store{"myrig": beads.NewMemStore()}
	state.cityBeadStore = beads.NewMemStore()
	h := newTestCityHandler(t, state)

	for _, query := range []string{
		"?route_target=pool-x",                     // target without mode
		"?route_mode=canonical",                    // mode without target
		"?route_target=pool-x&route_mode=sideways", // unknown mode
	} {
		if rec := readyGet(t, h, state, query); rec.Code < 400 || rec.Code >= 500 {
			t.Fatalf("GET %s status = %d, want a 4xx validation rejection; body=%s", query, rec.Code, rec.Body.String())
		}
	}
}

// TestBeadListCountQueryMatchesRowTierForEphemeral proves the bounded-count query
// spans the same tier as the row query when include_ephemeral is set, so the
// all=true Total and pagination boundary cannot disagree with the listed rows
// (a TierBoth row read paired with a TierIssues count would undercount).
func TestBeadListCountQueryMatchesRowTierForEphemeral(t *testing.T) {
	withEph := beadListCountQuery("worker-1", &BeadListInput{All: true, IncludeEphemeral: true})
	if withEph.TierMode != beads.TierBoth {
		t.Fatalf("count TierMode = %v with include_ephemeral, want TierBoth to match the row query", withEph.TierMode)
	}
	withoutEph := beadListCountQuery("worker-1", &BeadListInput{All: true})
	if withoutEph.TierMode != beads.TierIssues {
		t.Fatalf("count TierMode = %v without include_ephemeral, want the default TierIssues", withoutEph.TierMode)
	}
}
