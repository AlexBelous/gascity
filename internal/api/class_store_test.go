package api

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/danielgtaylor/huma/v2"
	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/config"
)

// writeRoutesJSONL writes a routes.jsonl into scopeDir/.beads/, creating the
// directory. Lines are already-encoded JSON objects.
func writeRoutesJSONL(t *testing.T, scopeDir string, lines ...string) {
	t.Helper()
	beadsDir := filepath.Join(scopeDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0o700); err != nil {
		t.Fatalf("MkdirAll(%s): %v", beadsDir, err)
	}
	var body string
	for _, line := range lines {
		body += line + "\n"
	}
	if err := os.WriteFile(filepath.Join(beadsDir, "routes.jsonl"), []byte(body), 0o644); err != nil {
		t.Fatalf("WriteFile(%s/routes.jsonl): %v", beadsDir, err)
	}
}

// TestBeadStoresForIDDefaultBackendIsCityLed pins the single-store invariant:
// when the graph class is NOT relocated (GraphBeadStore() == CityBeadStore()),
// the class-prefix arm never fires, so the unrouted by-id candidate set leads
// with the city store ahead of the per-rig work stores — byte-identical to the
// pre-seam ordering.
func TestBeadStoresForIDDefaultBackendIsCityLed(t *testing.T) {
	st := newFakeState(t)
	city := beads.NewMemStore()
	st.cityBeadStore = city
	// Drop the rig store so the city store is the only by-id candidate.
	st.stores = map[string]beads.Store{}
	st.cfg.Rigs = nil
	s := New(st)

	got := s.beadStoresForID("gcg-1")
	if len(got) != 1 {
		t.Fatalf("beadStoresForID returned %d stores, want 1 (city-led, no graph arm); got %v", len(got), got)
	}
	if got[0] != city {
		t.Errorf("beadStoresForID[0] = %p, want CityBeadStore %p", got[0], city)
	}
}

// TestBeadStoresForIDClassAwareGraphArm pins the relocated-graph behavior: with a
// DISTINCT dedicated graph store, a graph-class id (reserved prefix "gcg") that is
// not reachable via a rig/HQ prefix resolves to [graph, work] — graph-first — so
// the by-id Get-then-mutate handler loop pins the graph store on the first probe.
// On a single-store city (graph == city) the arm is skipped, so this path stays
// byte-identical there (covered by TestBeadStoresForIDDefaultBackendIsCityLed).
func TestBeadStoresForIDClassAwareGraphArm(t *testing.T) {
	work := beads.NewMemStore()
	graph := beads.NewMemStore()

	st := newFakeState(t)
	st.cityBeadStore = work   // plain work store
	st.graphBeadStore = graph // dedicated, distinct graph store
	st.stores = nil
	st.cfg.Rigs = nil

	prefix, ok := config.ReservedClassPrefix(config.BeadClassGraph)
	if !ok {
		t.Fatalf("ReservedClassPrefix(graph) returned ok=false; expected a reserved prefix")
	}
	s := New(st)

	got := s.beadStoresForID(prefix + "-1")
	if len(got) != 2 || got[0] != s.state.GraphBeadStore().Store || got[1] != s.state.CityBeadStore() {
		t.Fatalf("beadStoresForID(%s-1) = %v (len %d), want [graph, work]", prefix, got, len(got))
	}
}

// relocatedGraphRouteState builds a relocated-graph city with one rig, and
// returns the state plus the graph, city and rig stores. The graph store lives
// at <city>/.gc/infra, which is NOT a registered rig path.
func relocatedGraphRouteState(t *testing.T) (*fakeState, beads.Store, beads.Store, beads.Store) {
	t.Helper()
	st := newFakeState(t)
	city := beads.NewMemStore()
	graph := beads.NewMemStore()
	rig := beads.NewMemStore()
	st.cityBeadStore = city
	st.graphBeadStore = graph
	st.stores = map[string]beads.Store{"myrig": rig}
	st.cfg.Rigs = []config.Rig{{Name: "myrig", Path: filepath.Join(st.cityPath, "rigs", "myrig")}}
	return st, graph, city, rig
}

// TestBeadStoresForIDClassArmBeatsRigRouteCapture pins the ordering the split
// city depends on: a rig routes.jsonl entry for the reserved graph prefix
// resolves to the relocated graph directory, which is not a rig, so the rig
// must NOT answer for it — the class arm must.
func TestBeadStoresForIDClassArmBeatsRigRouteCapture(t *testing.T) {
	st, graph, city, rig := relocatedGraphRouteState(t)
	prefix, ok := config.ReservedClassPrefix(config.BeadClassGraph)
	if !ok {
		t.Fatalf("ReservedClassPrefix(graph) returned ok=false; expected a reserved prefix")
	}
	// The rig's routes.jsonl routes the graph class OUT of the rig, to the
	// relocated graph store directory.
	writeRoutesJSONL(t, st.cfg.Rigs[0].Path,
		`{"prefix":"mr","path":"."}`,
		`{"prefix":"`+prefix+`","path":"../../.gc/infra"}`,
	)

	s := New(st)
	got := s.beadStoresForID(prefix + "-1")
	if len(got) != 2 || got[0] != graph || got[1] != city {
		t.Fatalf("beadStoresForID(%s-1) = %v (len %d), want [graph, city]; rig store is %p", prefix, got, len(got), rig)
	}
}

// TestBeadStoresForIDClassArmBeatsCityRouteCapture pins the same ordering for a
// city-scope route: a routes.jsonl entry that resolves the reserved graph prefix
// back to the city directory must not hand graph-class ids to the city work
// store — the class arm owns reserved prefixes.
func TestBeadStoresForIDClassArmBeatsCityRouteCapture(t *testing.T) {
	st, graph, city, _ := relocatedGraphRouteState(t)
	prefix, ok := config.ReservedClassPrefix(config.BeadClassGraph)
	if !ok {
		t.Fatalf("ReservedClassPrefix(graph) returned ok=false; expected a reserved prefix")
	}
	writeRoutesJSONL(t, st.cityPath, `{"prefix":"`+prefix+`","path":"."}`)

	s := New(st)
	got := s.beadStoresForID(prefix + "-1")
	if len(got) != 2 || got[0] != graph || got[1] != city {
		t.Fatalf("beadStoresForID(%s-1) = %v (len %d), want [graph, city]; graph is %p, city is %p", prefix, got, len(got), graph, city)
	}
}

// TestBeadStoresForIDClassArmBeatsShadowingRigPrefix pins the ordering against
// the other way a work store can name a reserved prefix: a rig configured with
// the class prefix itself. config.ReservedPrefixWarnings allows that today but
// documents the class store as the owner once relocation is active, so on a
// relocated city the class arm — not the shadowing rig — answers FIRST.
//
// The shadowing rig store still has to be IN the list, behind the class store.
// The prefix is warned-and-allowed, not rejected (config.ValidateRigs lets it
// through), so that rig's beads are real; a list that dropped it made every one
// of them unreachable by id the moment graph relocated. That was carried minor
// (a) from PR #5128's council, and this is where it is closed.
//
// It goes behind the CITY store too, so [graph, city] — the whole of the
// pre-seam list — stays the head. The handlers fail a by-id read fast on a
// non-ErrNotFound probe, so a leg inserted ahead of the city store would let
// that rig's outage answer for a bead the city store was serving.
func TestBeadStoresForIDClassArmBeatsShadowingRigPrefix(t *testing.T) {
	st, graph, city, rig := relocatedGraphRouteState(t)
	prefix, ok := config.ReservedClassPrefix(config.BeadClassGraph)
	if !ok {
		t.Fatalf("ReservedClassPrefix(graph) returned ok=false; expected a reserved prefix")
	}
	st.cfg.Rigs[0].Prefix = prefix

	s := New(st)
	got := s.beadStoresForID(prefix + "-1")
	if len(got) != 3 || got[0] != graph || got[1] != city || got[2] != rig {
		t.Fatalf("beadStoresForID(%s-1) = %v (len %d), want [graph, city, rig]; graph is %p, city is %p, rig is %p", prefix, got, len(got), graph, city, rig)
	}
}

// TestBeadStoresForIDClassArmKeepsLongerRigPrefix closes carried minor (b): an
// id under a LONGER configured prefix that starts with the reserved one is
// inside the class namespace by the exact-or-hyphen rule, so the class arm
// fires — and used to return [graph, city], silently losing the rig store that
// declares the longer prefix and actually mints the id.
//
// The rig is appended behind the pre-seam [graph, city] head: it is a leg this
// slice ADDS, and an added leg extends reachability rather than re-answering an
// id the old list already resolved.
func TestBeadStoresForIDClassArmKeepsLongerRigPrefix(t *testing.T) {
	st, graph, city, rig := relocatedGraphRouteState(t)
	prefix, ok := config.ReservedClassPrefix(config.BeadClassGraph)
	if !ok {
		t.Fatalf("ReservedClassPrefix(graph) returned ok=false; expected a reserved prefix")
	}
	longer := prefix + "-alpha"
	st.cfg.Rigs[0].Prefix = longer

	s := New(st)
	got := s.beadStoresForID(longer + "-1")
	if len(got) != 3 || got[0] != graph || got[1] != city || got[2] != rig {
		t.Fatalf("beadStoresForID(%s-1) = %v (len %d), want [graph, city, rig]; graph is %p, city is %p, rig is %p", longer, got, len(got), graph, city, rig)
	}
}

// getFailStore is a store whose by-id read fails HARD — the shape an
// unreachable rig backend takes on the by-id probe path, as distinct from a
// clean ErrNotFound miss.
type getFailStore struct {
	beads.Store
	err error
}

func (s getFailStore) Get(string) (beads.Bead, error) { return beads.Bead{}, s.err }

// TestBeadGetSurvivesAShadowingRigOutage is the availability half of the
// shadowing-store fix, and the reason the added legs go LAST.
//
// The by-id handlers fail fast on a non-ErrNotFound probe: an unreachable store
// must not be reported as a missing bead, and where two stores claim one
// namespace it must not be silently replaced by the other store's row of the
// same id. That rule costs availability the moment a leg is inserted AHEAD of a
// store that was answering — a rig outage would then 500 a read the city store
// had been serving. Appending the shadows behind [graph, city] is what removes
// that exposure, and this drives it through the real handler.
func TestBeadGetSurvivesAShadowingRigOutage(t *testing.T) {
	prefix, ok := config.ReservedClassPrefix(config.BeadClassGraph)
	if !ok {
		t.Fatalf("ReservedClassPrefix(graph) returned ok=false; expected a reserved prefix")
	}
	st, _, city, rig := relocatedGraphRouteState(t)
	st.cfg.Rigs[0].Prefix = prefix
	st.stores["myrig"] = getFailStore{Store: rig, err: errors.New("rig backend unreachable")}

	memCity, isMem := city.(*beads.MemStore)
	if !isMem {
		t.Fatalf("fixture city store is %T, want *beads.MemStore so the test can pin its minted prefix", city)
	}
	memCity.IDPrefix = prefix
	created, err := memCity.Create(beads.Bead{Title: "city work bead in the class namespace"})
	if err != nil {
		t.Fatalf("seeding the city store: %v", err)
	}

	out, err := New(st).humaHandleBeadGet(context.Background(), &BeadGetInput{ID: created.ID})
	if err != nil {
		t.Fatalf("GET bead %s: %v — an unrelated rig's outage answered for a bead the city store holds", created.ID, err)
	}
	if out.Body.ID != created.ID {
		t.Fatalf("resolved bead id = %q, want %q", out.Body.ID, created.ID)
	}
}

// TestClassArmStillDoesNotCoverMigratedLegacyIDs pins the limitation of the
// CLASS ARM specifically, which the residency fallback does not remove.
//
// storeref.ClassCandidates gates on the class NAMESPACE before it builds a
// list, so a bead `gc storage migrate` relocated with its HQ-prefixed id kept
// gets nil back from it — that is a property of the namespace rule and stays
// true. What changed is that the resolver no longer STOPS there: the fallback
// appends the class binding behind the prefix-routed candidates, which is what
// TestBeadStoresForIDAppendsClassResidencyFallback covers. Keeping this
// assertion separate keeps the two claims from being confused for each other.
func TestClassArmStillDoesNotCoverMigratedLegacyIDs(t *testing.T) {
	st, graph, _, _ := relocatedGraphRouteState(t)
	st.cfg.Workspace.Prefix = "mc"

	if got := New(st).classStoresForID("mc-123", nil); got != nil {
		t.Fatalf("classStoresForID(mc-123) = %v, want nil — a legacy-prefixed id is outside the class namespace; graph is %p", got, graph)
	}
}

// TestBeadGetResolvesAShadowingRigID is the mutation proof behind the two
// carried minors: a bead that exists ONLY in a rig whose configured prefix sits
// inside the relocated class namespace must still resolve through the handler
// the by-id candidate list feeds. Before the fix the rig store was not a
// candidate at all, so this read 404'd.
func TestBeadGetResolvesAShadowingRigID(t *testing.T) {
	prefix, ok := config.ReservedClassPrefix(config.BeadClassGraph)
	if !ok {
		t.Fatalf("ReservedClassPrefix(graph) returned ok=false; expected a reserved prefix")
	}
	for _, rigPrefix := range []string{prefix, prefix + "-alpha"} {
		t.Run(rigPrefix, func(t *testing.T) {
			st, graph, city, rig := relocatedGraphRouteState(t)
			st.cfg.Rigs[0].Prefix = rigPrefix
			memRig, isMem := rig.(*beads.MemStore)
			if !isMem {
				t.Fatalf("fixture rig store is %T, want *beads.MemStore so the test can pin its minted prefix", rig)
			}
			memRig.IDPrefix = rigPrefix
			created, err := memRig.Create(beads.Bead{Title: "rig work bead in the class namespace"})
			if err != nil {
				t.Fatalf("seeding the rig store: %v", err)
			}
			for name, other := range map[string]beads.Store{"graph": graph, "city": city} {
				if _, err := other.Get(created.ID); err == nil {
					t.Fatalf("the %s store also holds %s; the fixture proves nothing", name, created.ID)
				}
			}

			out, err := New(st).humaHandleBeadGet(context.Background(), &BeadGetInput{ID: created.ID})
			if err != nil {
				t.Fatalf("GET bead %s: %v — a shadowing rig's bead is unreachable by id on a relocated city", created.ID, err)
			}
			if out.Body.ID != created.ID {
				t.Fatalf("resolved bead id = %q, want %q", out.Body.ID, created.ID)
			}
		})
	}
}

// TestBeadStoresForIDShadowingRigPrefixStillWinsOnDefaultCity pins the other
// side of that rule: with no relocation (GraphBeadStore() == CityBeadStore())
// the class arm never fires, so a rig configured with the reserved prefix keeps
// owning those ids exactly as it does today.
func TestBeadStoresForIDShadowingRigPrefixStillWinsOnDefaultCity(t *testing.T) {
	st := newFakeState(t)
	city := beads.NewMemStore()
	rig := beads.NewMemStore()
	st.cityBeadStore = city
	st.stores = map[string]beads.Store{"myrig": rig}
	prefix, ok := config.ReservedClassPrefix(config.BeadClassGraph)
	if !ok {
		t.Fatalf("ReservedClassPrefix(graph) returned ok=false; expected a reserved prefix")
	}
	st.cfg.Rigs = []config.Rig{{Name: "myrig", Path: filepath.Join(st.cityPath, "rigs", "myrig"), Prefix: prefix}}

	s := New(st)
	got := s.beadStoresForID(prefix + "-1")
	if len(got) != 1 || got[0] != rig {
		t.Fatalf("beadStoresForID(%s-1) = %v (len %d), want only the shadowing rig store %p", prefix, got, len(got), rig)
	}
}

// The tests below cover the residency fallback: the leg that reaches a bead
// RESIDENT in the class binding under a work-shaped id.
//
// Two populations wear that shape. `gc storage migrate` preserves ids, so every
// row it relocated kept its HQ/rig-era prefix; and a class store MINTS from its
// own binding workspace's prefix, so a synthetic created there with no id — an
// input convoy, a patrol root, a wisp — is born work-shaped and class-resident.
// Both are outside the class namespace, so the class arm never fires for them,
// and before the fallback the prefix resolver answered them from the work store
// alone: a 404 on every read and every write of a bead the city really holds.

// classResidentWorkShapedID seeds a bead into the graph store under an id in
// the WORK prefix's namespace, and proves no other candidate store holds it.
func classResidentWorkShapedID(t *testing.T, graph beads.Store, id string) string {
	t.Helper()
	return seedWithPinnedID(t, graph, id, "class-resident under a work prefix")
}

// seedWithPinnedID creates a bead under an exact id, which is what makes these
// fixtures able to model an id the class binding holds and a prefix store
// routes elsewhere.
func seedWithPinnedID(t *testing.T, store beads.Store, id, title string) string {
	t.Helper()
	mem, ok := store.(*beads.MemStore)
	if !ok {
		t.Fatalf("fixture store is %T, want *beads.MemStore so the test can pin the seeded id", store)
	}
	mem.HonorExplicitIDs = true
	created, err := mem.Create(beads.Bead{ID: id, Title: title})
	if err != nil {
		t.Fatalf("seeding %s: %v", id, err)
	}
	if created.ID != id {
		t.Fatalf("the fixture store minted %q instead of the pinned %q", created.ID, id)
	}
	return id
}

// TestBeadStoresForIDAppendsClassResidencyFallback is the resolver-level
// contract, arm by arm. It replaces TestBeadStoresForIDDoesNotCoverMigratedLegacyIDs,
// which pinned this coverage gap so it could not be forgotten; this is the
// close-out.
//
// The fallback is APPENDED, never inserted: every store the resolver already
// returned keeps its position and its precedence, so no answer this path
// already served can change. What changes is only what happens after all of
// them have missed.
func TestBeadStoresForIDAppendsClassResidencyFallback(t *testing.T) {
	prefix, ok := config.ReservedClassPrefix(config.BeadClassGraph)
	if !ok {
		t.Fatalf("ReservedClassPrefix(graph) returned ok=false; expected a reserved prefix")
	}

	t.Run("configured prefix arm", func(t *testing.T) {
		st, graph, city, _ := relocatedGraphRouteState(t)
		st.cfg.Workspace.Prefix = "mc"

		got := New(st).beadStoresForID("mc-123")
		if len(got) != 2 || got[0] != city || got[1] != graph {
			t.Fatalf("beadStoresForID(mc-123) = %v (len %d), want [city %p, graph %p] — the prefix store keeps the lead, the class binding is the fallback", got, len(got), city, graph)
		}
	})

	t.Run("routes.jsonl arm", func(t *testing.T) {
		st, graph, _, rig := relocatedGraphRouteState(t)
		st.cfg.Workspace.Prefix = "mc"
		writeRoutesJSONL(t, st.cityPath, `{"prefix":"rt","path":"rigs/myrig"}`)

		got := New(st).beadStoresForID("rt-1")
		if len(got) != 2 || got[0] != rig || got[1] != graph {
			t.Fatalf("beadStoresForID(rt-1) = %v (len %d), want [rig %p, graph %p]", got, len(got), rig, graph)
		}
	})

	t.Run("legacy scan arm", func(t *testing.T) {
		st, graph, city, rig := relocatedGraphRouteState(t)
		st.cfg.Workspace.Prefix = "mc"

		got := New(st).beadStoresForID("zz-1")
		if len(got) != 3 || got[0] != city || got[1] != rig || got[2] != graph {
			t.Fatalf("beadStoresForID(zz-1) = %v (len %d), want [city %p, rig %p, graph %p]", got, len(got), city, rig, graph)
		}
	})

	t.Run("class arm is untouched", func(t *testing.T) {
		st, graph, city, _ := relocatedGraphRouteState(t)

		got := New(st).beadStoresForID(prefix + "-1")
		if len(got) != 2 || got[0] != graph || got[1] != city {
			t.Fatalf("beadStoresForID(%s-1) = %v (len %d), want the class arm's own [graph %p, city %p] with no second graph leg", prefix, got, len(got), graph, city)
		}
	})

	// A rig whose store IS the relocated class store. State hands out shared
	// store INSTANCES in the file provider — sortedRigNames dedupes by store
	// identity for exactly that reason — so an arm can already contain the store
	// the fallback would add. Appending it a second time would probe the same
	// store twice, and on the fail-fast by-id path a duplicated failing leg is a
	// duplicated 500 attributed to a store already reported.
	t.Run("class store already among the candidates", func(t *testing.T) {
		st, graph, city, _ := relocatedGraphRouteState(t)
		st.cfg.Workspace.Prefix = "mc"
		st.cfg.Rigs[0].Prefix = "rw"
		st.stores["myrig"] = graph

		s := New(st)
		if got := s.beadStoresForID("rw-1"); len(got) != 1 || got[0] != graph {
			t.Fatalf("beadStoresForID(rw-1) = %v (len %d), want only the graph store %p once", got, len(got), graph)
		}
		if got := s.beadStoresForID("zz-1"); len(got) != 2 || got[0] != city || got[1] != graph {
			t.Fatalf("beadStoresForID(zz-1) = %v (len %d), want [city %p, graph %p] with the graph leg listed once", got, len(got), city, graph)
		}
	})

	t.Run("single-store city is identity", func(t *testing.T) {
		st := newFakeState(t)
		city := beads.NewMemStore()
		rig := beads.NewMemStore()
		st.cityBeadStore = city
		st.stores = map[string]beads.Store{"myrig": rig}
		st.cfg.Workspace.Prefix = "mc"
		st.cfg.Rigs = []config.Rig{{Name: "myrig", Path: filepath.Join(st.cityPath, "rigs", "myrig"), Prefix: "rw"}}

		s := New(st)
		if got := s.beadStoresForID("mc-123"); len(got) != 1 || got[0] != city {
			t.Fatalf("beadStoresForID(mc-123) = %v (len %d), want only the city store %p", got, len(got), city)
		}
		if got := s.beadStoresForID("rw-1"); len(got) != 1 || got[0] != rig {
			t.Fatalf("beadStoresForID(rw-1) = %v (len %d), want only the rig store %p", got, len(got), rig)
		}
		if got := s.beadStoresForID("zz-1"); len(got) != 2 || got[0] != city || got[1] != rig {
			t.Fatalf("beadStoresForID(zz-1) = %v (len %d), want the unchanged legacy scan [city %p, rig %p]", got, len(got), city, rig)
		}
	})
}

// TestBeadGetServesClassResident is the read half through the real handler:
// GET /v0/bead/{id} answered 404 for a bead the city holds, because the
// prefix-routed work store was the only candidate.
func TestBeadGetServesClassResident(t *testing.T) {
	st, graph, city, _ := relocatedGraphRouteState(t)
	st.cfg.Workspace.Prefix = "mc"
	id := classResidentWorkShapedID(t, graph, "mc-relic1")
	if _, err := city.Get(id); err == nil {
		t.Fatalf("the work store also holds %s; the fixture proves nothing", id)
	}

	out, err := New(st).humaHandleBeadGet(context.Background(), &BeadGetInput{ID: id})
	if err != nil {
		t.Fatalf("GET bead %s: %v — a class-resident work-shaped id is unreachable by id", id, err)
	}
	if out.Body.ID != id {
		t.Fatalf("resolved bead id = %q, want %q", out.Body.ID, id)
	}
}

// TestBeadCloseLandsInClassStoreForWorkPrefixedResident is the write half,
// across every mutating by-id handler.
//
// No handler changes: each one probes beadStoresForID in order, stops at the
// first store whose Get answers, and then binds its write to THAT store —
// "once Get succeeded in this store, treat Update-ErrNotFound as a
// concurrent-delete race rather than try the next store". Read/write coherence
// on this surface is therefore structural, and adding a read leg adds the write
// leg with it.
func TestBeadCloseLandsInClassStoreForWorkPrefixedResident(t *testing.T) {
	const renamed = "renamed by the api"
	for name, tc := range map[string]struct {
		setup  func(*testing.T, beads.Store, string)
		mutate func(*Server, string) error
		verify func(*testing.T, beads.Bead)
	}{
		"close": {
			mutate: func(s *Server, id string) error {
				_, err := s.humaHandleBeadClose(context.Background(), &BeadCloseInput{ID: id})
				return err
			},
			verify: func(t *testing.T, b beads.Bead) {
				if b.Status != "closed" {
					t.Errorf("status = %q, want closed", b.Status)
				}
			},
		},
		"delete": {
			mutate: func(s *Server, id string) error {
				_, err := s.humaHandleBeadDelete(context.Background(), &BeadDeleteInput{ID: id})
				return err
			},
			verify: func(t *testing.T, b beads.Bead) {
				if b.Status != "closed" {
					t.Errorf("status = %q, want closed (DELETE is a soft close on this surface)", b.Status)
				}
			},
		},
		"update": {
			mutate: func(s *Server, id string) error {
				title := renamed
				_, err := s.humaHandleBeadUpdate(context.Background(), &BeadUpdateInput{ID: id, Body: beadUpdateBody{Title: &title}})
				return err
			},
			verify: func(t *testing.T, b beads.Bead) {
				if b.Title != renamed {
					t.Errorf("title = %q, want %q", b.Title, renamed)
				}
			},
		},
		"assign": {
			setup: func(t *testing.T, graph beads.Store, id string) {
				held := "previous-holder"
				if err := graph.Update(id, beads.UpdateOpts{Assignee: &held}); err != nil {
					t.Fatalf("seeding an assignee on %s: %v", id, err)
				}
			},
			mutate: func(s *Server, id string) error {
				in := &BeadAssignInput{ID: id}
				in.Body.Assignee = ""
				_, err := s.humaHandleBeadAssign(context.Background(), in)
				return err
			},
			verify: func(t *testing.T, b beads.Bead) {
				if b.Assignee != "" {
					t.Errorf("assignee = %q, want the routed assign to have cleared it", b.Assignee)
				}
			},
		},
		"reopen": {
			setup: func(t *testing.T, graph beads.Store, id string) {
				if err := graph.Close(id); err != nil {
					t.Fatalf("pre-closing %s: %v", id, err)
				}
			},
			mutate: func(s *Server, id string) error {
				_, err := s.humaHandleBeadReopen(context.Background(), &BeadReopenInput{ID: id})
				return err
			},
			verify: func(t *testing.T, b beads.Bead) {
				if b.Status == "closed" {
					t.Errorf("status = %q, want reopened", b.Status)
				}
			},
		},
	} {
		t.Run(name, func(t *testing.T) {
			st, graph, city, _ := relocatedGraphRouteState(t)
			st.cfg.Workspace.Prefix = "mc"
			id := classResidentWorkShapedID(t, graph, "mc-relic1")
			if tc.setup != nil {
				tc.setup(t, graph, id)
			}

			if err := tc.mutate(New(st), id); err != nil {
				t.Fatalf("%s %s: %v — the write 404'd on a bead resident in the class binding", name, id, err)
			}
			if _, err := city.Get(id); err == nil {
				t.Errorf("the work store holds %s after the routed %s; the write must land in the store whose Get answered", id, name)
			}
			after, err := graph.Get(id)
			if err != nil {
				t.Fatalf("re-reading %s from the class binding: %v", id, err)
			}
			tc.verify(t, after)
		})
	}
}

// TestBeadWriteDualResidentPrefersPrefixStore is the ambiguity pin and the
// control in one: for an id BOTH stores hold, the API keeps answering — and now
// writing — from the prefix store, byte-identically to pre-fix behavior.
//
// The migration never deletes its source, so a relocated bead can be resident
// in both. Appending the class leg BEHIND is what keeps that population's
// answers unchanged; a class-first order here would silently repoint every
// dual-resident id at the other copy.
func TestBeadWriteDualResidentPrefersPrefixStore(t *testing.T) {
	st, graph, city, _ := relocatedGraphRouteState(t)
	st.cfg.Workspace.Prefix = "mc"
	id := classResidentWorkShapedID(t, graph, "mc-dual1")
	seedWithPinnedID(t, city, id, "the retained work copy")

	s := New(st)
	out, err := s.humaHandleBeadGet(context.Background(), &BeadGetInput{ID: id})
	if err != nil {
		t.Fatalf("GET bead %s: %v", id, err)
	}
	if out.Body.Title != "the retained work copy" {
		t.Fatalf("GET served %q, want the prefix store's copy — the added leg must not re-answer an id the resolver already resolved", out.Body.Title)
	}
	if _, err := s.humaHandleBeadClose(context.Background(), &BeadCloseInput{ID: id}); err != nil {
		t.Fatalf("close %s: %v", id, err)
	}
	workCopy, err := city.Get(id)
	if err != nil {
		t.Fatalf("re-reading the work copy: %v", err)
	}
	if workCopy.Status != "closed" {
		t.Errorf("the work copy's status = %q, want closed — the write must follow the read", workCopy.Status)
	}
	classCopy, err := graph.Get(id)
	if err != nil {
		t.Fatalf("re-reading the class copy: %v", err)
	}
	if classCopy.Status == "closed" {
		t.Errorf("the class copy was closed too; one id, one owner, one write")
	}
}

// TestBeadMissThenUnreachableClassStoreIs500Not404 keeps the fail-fast doctrine
// on the added leg: an unreachable store must not be reported as a missing
// bead. The added leg slightly STRENGTHENS the rule — a prefix miss followed by
// an unreachable class store used to be a confident 404, and is now a 500 — and
// the control proves the change is about reachability, not about turning every
// miss into an error.
func TestBeadMissThenUnreachableClassStoreIs500Not404(t *testing.T) {
	t.Run("unreachable class store", func(t *testing.T) {
		st, graph, _, _ := relocatedGraphRouteState(t)
		st.cfg.Workspace.Prefix = "mc"
		st.graphBeadStore = getFailStore{Store: graph, err: errors.New("infra binding unreachable")}

		_, err := New(st).humaHandleBeadGet(context.Background(), &BeadGetInput{ID: "mc-relic1"})
		var statusErr huma.StatusError
		if !errors.As(err, &statusErr) {
			t.Fatalf("GET returned %T %v, want a Huma status error", err, err)
		}
		if statusErr.GetStatus() != http.StatusInternalServerError {
			t.Fatalf("status = %d, want 500 — an unreachable store reported as a missing bead is the root-loss shape", statusErr.GetStatus())
		}
	})

	t.Run("clean miss stays 404", func(t *testing.T) {
		st, _, _, _ := relocatedGraphRouteState(t)
		st.cfg.Workspace.Prefix = "mc"

		_, err := New(st).humaHandleBeadGet(context.Background(), &BeadGetInput{ID: "mc-relic1"})
		var statusErr huma.StatusError
		if !errors.As(err, &statusErr) {
			t.Fatalf("GET returned %T %v, want a Huma status error", err, err)
		}
		if statusErr.GetStatus() != http.StatusNotFound {
			t.Fatalf("status = %d, want 404 — a bead no store holds is absent, not an outage", statusErr.GetStatus())
		}
	})
}

// TestBeadGetResolvesARelocatedGraphID is the evidence behind the one command
// beads.RelocatedClassRefusal tells an operator to run.
//
// That refusal fires when a bd-ledger read names a relocated class's id
// namespace, and it has to name a read that DOES resolve such an id. It used to
// name `gc bd show` / `gc bd dep tree`, which are raw bd passthroughs against
// the same blind ledger — following the advice reproduced the bug being
// reported. The verb it names now, `gc beads show <id>`, routes through this
// handler (GET /v0/city/{cityName}/bead/{id}), so this test drives the handler
// end to end against a bead that exists ONLY in the relocated graph store. If it
// ever stops resolving, the refusal is giving bad advice again.
func TestBeadGetResolvesARelocatedGraphID(t *testing.T) {
	work := beads.NewMemStore()
	graph := beads.NewMemStore()

	prefix, ok := config.ReservedClassPrefix(config.BeadClassGraph)
	if !ok {
		t.Fatalf("ReservedClassPrefix(graph) returned ok=false; expected a reserved prefix")
	}
	graph.IDPrefix = prefix
	created, err := graph.Create(beads.Bead{Title: "molecule root"})
	if err != nil {
		t.Fatalf("seeding the graph store: %v", err)
	}
	relocatedID := created.ID
	if _, err := work.Get(relocatedID); err == nil {
		t.Fatalf("the work store holds %s; the fixture proves nothing", relocatedID)
	}

	st := newFakeState(t)
	st.cityBeadStore = work
	st.graphBeadStore = graph
	st.stores = nil
	st.cfg.Rigs = nil

	out, err := New(st).humaHandleBeadGet(context.Background(), &BeadGetInput{ID: relocatedID})
	if err != nil {
		t.Fatalf("GET /v0/city/{cityName}/bead/%s: %v — the verb the refusal recommends cannot resolve a relocated id", relocatedID, err)
	}
	if out.Body.ID != relocatedID {
		t.Fatalf("resolved bead id = %q, want %q", out.Body.ID, relocatedID)
	}
}
