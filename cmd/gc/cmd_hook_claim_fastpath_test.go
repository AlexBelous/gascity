package main

import (
	"errors"
	"testing"

	"github.com/gastownhall/gascity/internal/api"
	"github.com/gastownhall/gascity/internal/beadmeta"
	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/config"
)

// fakeFastPathReader is a controller-read stub honoring the same status/assignee
// filter the real ListBeads pushes down, so the tier reproduction is exercised
// against realistic responses without a live server.
//
// The flat inProgress/ready slices are the federated (empty-scope) response used
// by the single-store tier tests. readyByScope/inProgressByScope, when set, make
// the fake scope-aware: a read for scope S returns that scope's slice, which is
// how the STORE-outermost precedence tests exercise cross-store ordering. A
// scope with no map entry returns nothing (an empty store), matching the
// server's behavior for an unknown or work-less store.
type fakeFastPathReader struct {
	inProgress        []beads.Bead
	ready             []beads.Bead
	readyByScope      map[string][]beads.Bead
	inProgressByScope map[string][]beads.Bead
	readyCalls        int
	readyScopes       []string
	readyEphemeral    []bool
	listEphemeral     []bool
	listErr           error
	readyErr          error
}

func (f *fakeFastPathReader) inProgressFor(scope string) []beads.Bead {
	if f.inProgressByScope != nil {
		return f.inProgressByScope[scope]
	}
	return f.inProgress
}

func (f *fakeFastPathReader) readyFor(scope string) []beads.Bead {
	if f.readyByScope != nil {
		return f.readyByScope[scope]
	}
	return f.ready
}

func (f *fakeFastPathReader) ListBeads(opts api.ListBeadsOpts) (api.CachedRead[[]beads.Bead], error) {
	f.listEphemeral = append(f.listEphemeral, opts.IncludeEphemeral)
	if f.listErr != nil {
		return api.CachedRead[[]beads.Bead]{}, f.listErr
	}
	var out []beads.Bead
	for _, b := range f.inProgressFor(opts.Rig) {
		if opts.Status != "" && b.Status != opts.Status {
			continue
		}
		if opts.Assignee != "" && b.Assignee != opts.Assignee {
			continue
		}
		out = append(out, b)
		if opts.Limit > 0 && len(out) >= opts.Limit {
			break
		}
	}
	return api.CachedRead[[]beads.Bead]{Body: out}, nil
}

func (f *fakeFastPathReader) BeadsReady(scope string, includeEphemeral bool) (api.CachedRead[[]beads.Bead], error) {
	f.readyCalls++
	f.readyScopes = append(f.readyScopes, scope)
	f.readyEphemeral = append(f.readyEphemeral, includeEphemeral)
	if f.readyErr != nil {
		return api.CachedRead[[]beads.Bead]{}, f.readyErr
	}
	return api.CachedRead[[]beads.Bead]{Body: f.readyFor(scope)}, nil
}

func routed(id, target string) beads.Bead {
	return beads.Bead{ID: id, Type: "task", Metadata: beads.StringMap{beadmeta.RoutedToMetadataKey: target}}
}

// TestFastPathTier1AssignedInProgress proves an assigned in_progress bead
// (crash recovery) short-circuits ahead of the ready tiers and the ready read
// is never issued.
func TestFastPathTier1AssignedInProgress(t *testing.T) {
	r := &fakeFastPathReader{
		inProgress: []beads.Bead{{ID: "gc-crash", Status: "in_progress", Assignee: "sess-name"}},
		ready:      []beads.Bead{{ID: "gc-ready", Assignee: "sess-name"}},
	}
	got, err := fastPathClaimCandidates(r, []string{"", "sess-name", "alias-x"}, []string{"pool-x"}, "ephemeral", nil)
	if err != nil {
		t.Fatalf("fastPathClaimCandidates: %v", err)
	}
	if len(got) != 1 || got[0].ID != "gc-crash" {
		t.Fatalf("got %+v, want [gc-crash]", got)
	}
	if r.readyCalls != 0 {
		t.Errorf("BeadsReady called %d times; tier 1 must short-circuit before the ready read", r.readyCalls)
	}
}

// TestFastPathTier2AssignedReady proves an assigned ready bead wins when no
// in_progress work exists, and identity order is honored.
func TestFastPathTier2AssignedReady(t *testing.T) {
	r := &fakeFastPathReader{
		ready: []beads.Bead{
			{ID: "gc-other", Assignee: "someone-else"},
			{ID: "gc-mine", Assignee: "alias-x"},
		},
	}
	got, err := fastPathClaimCandidates(r, []string{"sess-id", "sess-name", "alias-x"}, []string{"pool-x"}, "", nil)
	if err != nil {
		t.Fatalf("fastPathClaimCandidates: %v", err)
	}
	if len(got) != 1 || got[0].ID != "gc-mine" {
		t.Fatalf("got %+v, want [gc-mine]", got)
	}
}

// TestFastPathTier3RoutedPool proves the pool tier returns unassigned, non-epic,
// route-matching ready beads and excludes assigned and epic rows.
func TestFastPathTier3RoutedPool(t *testing.T) {
	epic := routed("gc-epic", "pool-x")
	epic.Type = "epic"
	assigned := routed("gc-assigned", "pool-x")
	assigned.Assignee = "worker-9"
	wrongPool := routed("gc-wrong", "pool-y")
	match := routed("gc-pool", "pool-x")

	r := &fakeFastPathReader{ready: []beads.Bead{epic, assigned, wrongPool, match}}
	got, err := fastPathClaimCandidates(r, []string{"sess-name"}, []string{"pool-x"}, "ephemeral", nil)
	if err != nil {
		t.Fatalf("fastPathClaimCandidates: %v", err)
	}
	if len(got) != 1 || got[0].ID != "gc-pool" {
		t.Fatalf("got %+v, want [gc-pool] (epic/assigned/wrong-pool excluded)", got)
	}
}

// runTargetRouted builds a legacy migration candidate: no canonical gc.routed_to,
// a workflow kind, and a gc.run_target matching the pool. hookClaimMatchesRoute
// accepts it only through the migration fallback.
func runTargetRouted(id, target string) beads.Bead {
	return beads.Bead{
		ID:   id,
		Type: "task",
		Metadata: beads.StringMap{
			beadmeta.RunTargetMetadataKey: target,
			beadmeta.KindMetadataKey:      beadmeta.KindWorkflow,
		},
	}
}

// TestFastPathTier3CanonicalRouteBeatsMigration proves the shell probe's
// canonical-before-migration precedence: a canonical gc.routed_to match must be
// selected ahead of a legacy gc.run_target migration match even when the
// migration bead appears EARLIER in ready order. A single ready-order pass would
// have returned the migration bead first, diverging from
// poolDemandFirstRowFunctionScript.
func TestFastPathTier3CanonicalRouteBeatsMigration(t *testing.T) {
	migrationFirst := runTargetRouted("gc-migration", "pool-x")
	canonicalLater := routed("gc-canonical", "pool-x")
	r := &fakeFastPathReader{ready: []beads.Bead{migrationFirst, canonicalLater}}
	got, err := fastPathClaimCandidates(r, []string{"sess-name"}, []string{"pool-x"}, "ephemeral", nil)
	if err != nil {
		t.Fatalf("fastPathClaimCandidates: %v", err)
	}
	if len(got) != 1 || got[0].ID != "gc-canonical" {
		t.Fatalf("got %+v, want [gc-canonical]: canonical routed_to must outrank an earlier run_target migration bead", got)
	}
}

// TestFastPathTier3MigrationWhenNoCanonical proves the migration fallback still
// serves demand when no canonical routed_to bead exists — matching the shell's
// fall-through to the run_target probe.
func TestFastPathTier3MigrationWhenNoCanonical(t *testing.T) {
	r := &fakeFastPathReader{ready: []beads.Bead{runTargetRouted("gc-migration", "pool-x")}}
	got, err := fastPathClaimCandidates(r, []string{"sess-name"}, []string{"pool-x"}, "ephemeral", nil)
	if err != nil {
		t.Fatalf("fastPathClaimCandidates: %v", err)
	}
	if len(got) != 1 || got[0].ID != "gc-migration" {
		t.Fatalf("got %+v, want [gc-migration]: migration fallback must serve demand when no canonical exists", got)
	}
}

// TestFastPathTier3OriginGated proves a non-ephemeral, non-empty session origin
// disables the pool tier — matching the shell's GC_SESSION_ORIGIN gate — so a
// user-origin session never claims routed pool demand.
func TestFastPathTier3OriginGated(t *testing.T) {
	r := &fakeFastPathReader{ready: []beads.Bead{routed("gc-pool", "pool-x")}}
	got, err := fastPathClaimCandidates(r, []string{"sess-name"}, []string{"pool-x"}, "user", nil)
	if err != nil {
		t.Fatalf("fastPathClaimCandidates: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("got %+v, want empty (pool tier gated off for non-ephemeral origin)", got)
	}
}

// TestFastPathReadErrorPropagates proves a read error is surfaced (not
// swallowed) so the caller can classify a connection failure and fall back.
func TestFastPathReadErrorPropagates(t *testing.T) {
	sentinel := errors.New("boom")
	r := &fakeFastPathReader{readyErr: sentinel}
	_, err := fastPathClaimCandidates(r, []string{"sess-name"}, []string{"pool-x"}, "ephemeral", nil)
	if !errors.Is(err, sentinel) {
		t.Fatalf("err = %v, want the sentinel read error propagated", err)
	}
}

// TestFastPathScopeOrderRigOwnRigBeatsCityAssigned is the invariant-2 regression
// oracle: a rig-scoped agent's own-rig routed pool work (tier 3 in the rig store)
// must outrank an assigned-ready bead sitting in the city store (tier 2). The
// legacy firstStoreWithWork is STORE-outermost — it runs the whole three-tier
// query against the rig store first and short-circuits on any hit — so the rig's
// tier 3 wins over the city's tier 2. A single federated read (tier-outermost)
// would invert this and hand the agent city work ahead of its own rig work.
func TestFastPathScopeOrderRigOwnRigBeatsCityAssigned(t *testing.T) {
	r := &fakeFastPathReader{
		readyByScope: map[string][]beads.Bead{
			"frontend": {routed("gc-rig-pool", "frontend/polecat")},
			"citytown": {{ID: "gc-city-assigned", Assignee: "frontend/polecat"}},
		},
	}
	// Rig-scoped agent order: own rig first, then city.
	scopes := []string{"frontend", "citytown"}
	got, err := fastPathClaimCandidates(r, []string{"frontend/polecat"}, []string{"frontend/polecat"}, "ephemeral", scopes)
	if err != nil {
		t.Fatalf("fastPathClaimCandidates: %v", err)
	}
	if len(got) != 1 || got[0].ID != "gc-rig-pool" {
		t.Fatalf("got %+v, want [gc-rig-pool]: own-rig routed work must beat city assigned work", got)
	}
	// The city store must never be read once the rig store yields work.
	if len(r.readyScopes) != 1 || r.readyScopes[0] != "frontend" {
		t.Errorf("ready reads = %v, want exactly [frontend] (city short-circuited)", r.readyScopes)
	}
}

// TestFastPathScopeOrderCityFirstThenRigs proves a city-scoped (cross-store)
// agent reads its city store first: a city-store assigned-ready bead outranks
// routed pool work waiting in a rig store, mirroring the legacy own-store-first
// federation order for city agents.
func TestFastPathScopeOrderCityFirstThenRigs(t *testing.T) {
	r := &fakeFastPathReader{
		readyByScope: map[string][]beads.Bead{
			"citytown": {{ID: "gc-city-mine", Assignee: "mayor"}},
			"frontend": {routed("gc-rig-pool", "mayor")},
		},
	}
	scopes := []string{"citytown", "frontend", "backend"}
	got, err := fastPathClaimCandidates(r, []string{"mayor"}, []string{"mayor"}, "ephemeral", scopes)
	if err != nil {
		t.Fatalf("fastPathClaimCandidates: %v", err)
	}
	if len(got) != 1 || got[0].ID != "gc-city-mine" {
		t.Fatalf("got %+v, want [gc-city-mine]: city store is read first for a city-scoped agent", got)
	}
}

// TestFastPathScopeOrderConfigOrderAcrossRigs proves that when only rig stores
// hold work, the FIRST rig in config order wins — the config-order precedence
// the checkpoint requires preserved. The city store is empty, backend and
// frontend both hold matching routed work, and scopes lists frontend before
// backend, so frontend's bead is selected.
func TestFastPathScopeOrderConfigOrderAcrossRigs(t *testing.T) {
	r := &fakeFastPathReader{
		readyByScope: map[string][]beads.Bead{
			"frontend": {routed("gc-frontend-pool", "mayor")},
			"backend":  {routed("gc-backend-pool", "mayor")},
		},
	}
	scopes := []string{"citytown", "frontend", "backend"}
	got, err := fastPathClaimCandidates(r, []string{"mayor"}, []string{"mayor"}, "ephemeral", scopes)
	if err != nil {
		t.Fatalf("fastPathClaimCandidates: %v", err)
	}
	if len(got) != 1 || got[0].ID != "gc-frontend-pool" {
		t.Fatalf("got %+v, want [gc-frontend-pool]: first rig in config order wins", got)
	}
}

// TestFastPathScopeOrderDedupesRepeatedScope proves a scope list carrying the
// same store twice (the rig-scoped hookStore list holds the rig store as both
// the primary and the agent's own rig-coordinate env) reads that store once.
func TestFastPathScopeOrderDedupesRepeatedScope(t *testing.T) {
	r := &fakeFastPathReader{
		readyByScope: map[string][]beads.Bead{
			"frontend": {routed("gc-rig-pool", "frontend/polecat")},
		},
	}
	scopes := []string{"frontend", "frontend", "citytown"}
	got, err := fastPathClaimCandidates(r, []string{"frontend/polecat"}, []string{"frontend/polecat"}, "ephemeral", scopes)
	if err != nil {
		t.Fatalf("fastPathClaimCandidates: %v", err)
	}
	if len(got) != 1 || got[0].ID != "gc-rig-pool" {
		t.Fatalf("got %+v, want [gc-rig-pool]", got)
	}
	if len(r.readyScopes) != 1 || r.readyScopes[0] != "frontend" {
		t.Errorf("ready reads = %v, want exactly [frontend] (duplicate scope collapsed)", r.readyScopes)
	}
}

// TestFastPathReadsIncludeEphemeral proves both fast-path reads opt into the
// ephemeral tier: tier-1 ListBeads sets IncludeEphemeral and the ready read
// passes includeEphemeral=true. The generated query always probes ephemeral work
// (--include-ephemeral), so a fast path that read only the durable tier would
// strand ephemeral molecule/wisp workers when the flag is enabled.
func TestFastPathReadsIncludeEphemeral(t *testing.T) {
	r := &fakeFastPathReader{} // empty: forces both the list and ready reads
	if _, err := fastPathClaimCandidates(r, []string{"sess-name"}, []string{"pool-x"}, "ephemeral", nil); err != nil {
		t.Fatalf("fastPathClaimCandidates: %v", err)
	}
	if len(r.listEphemeral) == 0 {
		t.Fatal("tier-1 ListBeads was never called")
	}
	for i, e := range r.listEphemeral {
		if !e {
			t.Fatalf("ListBeads call %d had IncludeEphemeral=false; every fast-path tier-1 read must span both tiers", i)
		}
	}
	if len(r.readyEphemeral) == 0 {
		t.Fatal("ready read was never called")
	}
	for i, e := range r.readyEphemeral {
		if !e {
			t.Fatalf("BeadsReady call %d had includeEphemeral=false; the fast-path ready read must span both tiers", i)
		}
	}
}

// TestHookFastPathScopeOrder pins the store scope order for each agent shape
// against the legacy hookStore construction in cmd_hook.go.
func TestHookFastPathScopeOrder(t *testing.T) {
	cfg := &config.City{Rigs: []config.Rig{{Name: "frontend"}, {Name: "backend"}}}
	tests := []struct {
		name     string
		agent    *config.Agent
		identity string
		want     []string
	}{
		{
			name:     "city-scoped: city first then rigs in config order",
			agent:    &config.Agent{Scope: "city"},
			identity: "mayor",
			want:     []string{"citytown", "frontend", "backend"},
		},
		{
			name:     "rig-scoped: own rig first then city",
			agent:    &config.Agent{},
			identity: "frontend/polecat",
			want:     []string{"frontend", "citytown"},
		},
		{
			name:     "plain agent: city store only",
			agent:    &config.Agent{},
			identity: "solo",
			want:     []string{"citytown"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := hookFastPathScopeOrder(cfg, tc.agent, tc.identity, "citytown")
			if len(got) != len(tc.want) {
				t.Fatalf("scope order = %v, want %v", got, tc.want)
			}
			for i := range tc.want {
				if got[i] != tc.want[i] {
					t.Fatalf("scope order = %v, want %v", got, tc.want)
				}
			}
		})
	}
}
