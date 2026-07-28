package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/beads/beadstest"
	"github.com/gastownhall/gascity/internal/config"
	"github.com/gastownhall/gascity/internal/events"
	sessionpkg "github.com/gastownhall/gascity/internal/session"
)

type sessionWaitShadowReadAuditStore struct {
	beads.Store
	listCalls atomic.Int64
	getCalls  atomic.Int64
}

func (s *sessionWaitShadowReadAuditStore) List(query beads.ListQuery) ([]beads.Bead, error) {
	s.listCalls.Add(1)
	return s.Store.List(query)
}

func (s *sessionWaitShadowReadAuditStore) Get(id string) (beads.Bead, error) {
	s.getCalls.Add(1)
	return s.Store.Get(id)
}

func sessionWaitShadowBead(sessionID, dependencyID string) beads.Bead {
	return beads.Bead{
		Type:   sessionpkg.WaitBeadType,
		Status: "open",
		Labels: []string{sessionpkg.WaitBeadLabel},
		Metadata: map[string]string{
			"session_id": sessionID,
			"kind":       "deps",
			"state":      waitStatePending,
			"dep_ids":    dependencyID,
			"dep_mode":   "all",
		},
	}
}

func installSessionWaitShadowSentinel(t *testing.T, cityRuntime *CityRuntime) {
	t.Helper()
	cityRuntime.sessionWaitDependencyIndex = newSessionWaitDependencyIndex()
	err := cityRuntime.sessionWaitDependencyIndex.Rebuild([]sessionpkg.WaitInfo{{
		ID:        "sentinel-wait",
		SessionID: "sentinel-session",
		Status:    "open",
		Kind:      "deps",
		State:     waitStatePending,
		DepMode:   "all",
		DepIDs:    []string{"sentinel-dependency"},
	}})
	if err != nil {
		t.Fatalf("Rebuild sentinel: %v", err)
	}
}

func sessionWaitShadowIndex(cityRuntime *CityRuntime) *sessionWaitDependencyIndex {
	cityRuntime.sessionWaitDependencyMu.RLock()
	defer cityRuntime.sessionWaitDependencyMu.RUnlock()
	return cityRuntime.sessionWaitDependencyIndex
}

func sessionWaitShadowWaitIDs(waits []sessionpkg.WaitInfo) map[string]bool {
	ids := make(map[string]bool, len(waits))
	for _, wait := range waits {
		ids[wait.ID] = true
	}
	return ids
}

func TestSessionWaitDependencyShadowInstallsAndReplacesObservedCensus(t *testing.T) {
	backing := beads.NewMemStore()
	wait, err := backing.Create(sessionWaitShadowBead("session-a", "dep-x"))
	if err != nil {
		t.Fatalf("Create(wait): %v", err)
	}
	cache := beads.NewCachingStoreForTest(backing, nil)
	if err := cache.PrimeActive(); err != nil {
		t.Fatalf("PrimeActive: %v", err)
	}

	cityRuntime := &CityRuntime{}
	if installed, err := cityRuntime.installObservedSessionWaitDependencyIndex(beads.SessionStore{Store: cache}); err != nil || !installed {
		t.Fatalf("installObservedSessionWaitDependencyIndex = %v, %v; want true, nil", installed, err)
	}
	assertSessionWaitDependencyIndexSessions(t, cityRuntime.sessionWaitDependencySessions("dep-x"), []string{"session-a"})
	firstIndex := sessionWaitShadowIndex(cityRuntime)
	if firstIndex == nil {
		t.Fatal("installed index is nil")
	}

	if err := backing.Delete(wait.ID); err != nil {
		t.Fatalf("Delete(wait): %v", err)
	}
	if err := cache.Prime(t.Context()); err != nil {
		t.Fatalf("Prime(empty): %v", err)
	}
	if installed, err := cityRuntime.installObservedSessionWaitDependencyIndex(beads.SessionStore{Store: cache}); err != nil || !installed {
		t.Fatalf("install empty census = %v, %v; want true, nil", installed, err)
	}
	assertSessionWaitDependencyIndexSessions(t, cityRuntime.sessionWaitDependencySessions("dep-x"), nil)
	emptyIndex := sessionWaitShadowIndex(cityRuntime)
	if emptyIndex == nil {
		t.Fatal("authoritative empty census left index nil; want installed empty index")
	}
	if emptyIndex == firstIndex {
		t.Fatal("authoritative empty census did not replace the prior index")
	}
}

func TestSessionWaitDependencyProducerStartupAllTargetsThenSteadyWaitExact(t *testing.T) {
	backing := beads.NewMemStore()
	depA, err := backing.Create(beads.Bead{})
	if err != nil {
		t.Fatal(err)
	}
	if err := backing.Close(depA.ID); err != nil {
		t.Fatal(err)
	}
	depB, err := backing.Create(beads.Bead{})
	if err != nil {
		t.Fatal(err)
	}
	if err := backing.Close(depB.ID); err != nil {
		t.Fatal(err)
	}
	waitA, err := backing.Create(sessionWaitShadowBead("session-a", depA.ID))
	if err != nil {
		t.Fatal(err)
	}
	_, err = backing.Create(sessionWaitShadowBead("session-b", depB.ID))
	if err != nil {
		t.Fatal(err)
	}
	cache := beads.NewCachingStoreForTest(backing, nil)
	if err := cache.PrimeActive(); err != nil {
		t.Fatal(err)
	}
	cs := &controllerState{cfg: &config.City{}, cityBeadStore: cache, pokeCh: make(chan struct{}, 8)}
	admissions := make(chan string, 4)
	cr := &CityRuntime{cs: cs, cfg: &config.City{}, logPrefix: "test", stderr: io.Discard, waitDependencyEnqueue: func(id string, _ sessionWaitDependencyCause) error { admissions <- id; return nil }}
	cr.startSessionWaitDependencyShadow()
	t.Cleanup(func() { cs.stopSessionWaitDependencyShadowAdmission(); cr.stopSessionWaitDependencyProducer() })
	got := []string{
		receiveString(t, admissions, "first startup admission"),
		receiveString(t, admissions, "second startup admission"),
	}
	slices.Sort(got)
	if !slices.Equal(got, []string{"session-a", "session-b"}) {
		t.Fatalf("startup=%v", got)
	}
	if err := backing.Update(waitA.ID, beads.UpdateOpts{Metadata: map[string]string{"note": "changed"}}); err != nil {
		t.Fatal(err)
	}
	changed, err := backing.Get(waitA.ID)
	if err != nil {
		t.Fatal(err)
	}
	cs.applyBeadEventToStores(beadSnapshotEvent(t, events.BeadUpdated, changed))
	if got := receiveString(t, admissions, "steady exact admission"); got != "session-a" {
		t.Fatalf("steady=%q", got)
	}
	select {
	case extra := <-admissions:
		t.Fatalf("unexpected steady admission %q", extra)
	default:
	}
}

func TestSessionWaitDependencyProducerStartupCensusWaitsForFirstSuccessfulInstall(t *testing.T) {
	backing := beads.NewMemStore()
	for _, sessionID := range []string{"session-a", "session-b"} {
		dependency, err := backing.Create(beads.Bead{Status: "closed"})
		if err != nil {
			t.Fatal(err)
		}
		if err := backing.Close(dependency.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := backing.Create(sessionWaitShadowBead(sessionID, dependency.ID)); err != nil {
			t.Fatal(err)
		}
	}
	cache := beads.NewCachingStoreForTest(backing, nil)
	cs := &controllerState{cfg: &config.City{}, cityBeadStore: cache, pokeCh: make(chan struct{}, 2)}
	admissions := make(chan string, 3)
	cr := &CityRuntime{cs: cs, cfg: &config.City{}, stderr: io.Discard, waitDependencyEnqueue: func(id string, _ sessionWaitDependencyCause) error { admissions <- id; return nil }}
	cr.startSessionWaitDependencyShadow()
	t.Cleanup(func() { cs.stopSessionWaitDependencyShadowAdmission(); cr.stopSessionWaitDependencyProducer() })
	select {
	case got := <-admissions:
		t.Fatalf("admitted before initial cache install: %q", got)
	default:
	}
	if err := cache.PrimeActive(); err != nil {
		t.Fatal(err)
	}
	cs.applyBeadEventToStores(beadSnapshotEvent(t, events.BeadUpdated, beads.Bead{ID: "unrelated", Type: "task", Status: "closed"}))
	got := []string{receiveString(t, admissions, "first delayed startup admission"), receiveString(t, admissions, "second delayed startup admission")}
	slices.Sort(got)
	if !slices.Equal(got, []string{"session-a", "session-b"}) {
		t.Fatalf("delayed startup admissions = %v", got)
	}
	select {
	case extra := <-admissions:
		t.Fatalf("startup census replayed: %q", extra)
	default:
	}
}

func TestSessionWaitDependencyProducerRejectedPublicationKeepsStartupCensusDebt(t *testing.T) {
	store := beads.NewMemStore()
	dependency, err := store.Create(beads.Bead{Status: "closed"})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(dependency.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Create(sessionWaitShadowBead("session-a", dependency.ID)); err != nil {
		t.Fatal(err)
	}
	cache := beads.NewCachingStoreForTest(store, nil)
	if err := cache.PrimeActive(); err != nil {
		t.Fatal(err)
	}
	census, index, err := buildObservedSessionWaitDependencyIndex(beads.SessionStore{Store: cache})
	if err != nil {
		t.Fatal(err)
	}
	admissions := make(chan string, 1)
	cr := &CityRuntime{
		cfg:                                    &config.City{},
		stderr:                                 io.Discard,
		standaloneCityStore:                    store,
		sessionWaitDependencyIndex:             index,
		sessionWaitDependencyIndexGeneration:   1,
		sessionWaitDependencyStartupCensusOwed: true,
		waitDependencyEnqueue: func(id string, _ sessionWaitDependencyCause) error {
			admissions <- id
			return nil
		},
	}
	if !cr.startSessionWaitDependencyProducer() {
		t.Fatal("producer did not start")
	}
	t.Cleanup(cr.stopSessionWaitDependencyProducer)

	cr.submitSessionWaitDependencyStartupCensus()
	if got := receiveString(t, admissions, "initial startup census"); got != "session-a" {
		t.Fatalf("initial admission = %q, want session-a", got)
	}
	if cr.sessionWaitDependencyStartupCensusOwed {
		t.Fatal("initial census retained startup debt")
	}
	if published, err := cr.publishRejectedSessionWaitDependencyCensus(census); err != nil || !published {
		t.Fatalf("publish rejected census = %v, %v", published, err)
	}
	if !cr.sessionWaitDependencyStartupCensusOwed {
		t.Fatal("rejected publication did not re-arm startup debt")
	}
	cr.submitSessionWaitDependencyStartupCensus()
	select {
	case got := <-admissions:
		t.Fatalf("rejected census admitted %q", got)
	default:
	}
	if !cr.sessionWaitDependencyStartupCensusOwed {
		t.Fatal("rejected census cleared startup debt")
	}

	if published, err := cr.publishObservedSessionWaitDependencyIndex(census, index); err != nil || !published {
		t.Fatalf("publish recovered census = %v, %v", published, err)
	}
	cr.submitSessionWaitDependencyStartupCensus()
	if got := receiveString(t, admissions, "recovered startup census"); got != "session-a" {
		t.Fatalf("recovered admission = %q, want session-a", got)
	}
	if cr.sessionWaitDependencyStartupCensusOwed {
		t.Fatal("successful census retained startup debt")
	}
}

func TestSessionWaitDependencyProducerDependencyEventAdmitsOnlyExactSession(t *testing.T) {
	backing := beads.NewMemStore()
	depA, err := backing.Create(beads.Bead{})
	if err != nil {
		t.Fatal(err)
	}
	depB, err := backing.Create(beads.Bead{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := backing.Create(sessionWaitShadowBead("session-a", depA.ID)); err != nil {
		t.Fatal(err)
	}
	if _, err := backing.Create(sessionWaitShadowBead("session-b", depB.ID)); err != nil {
		t.Fatal(err)
	}
	cache := beads.NewCachingStoreForTest(backing, nil)
	if err := cache.PrimeActive(); err != nil {
		t.Fatal(err)
	}
	cs := &controllerState{cfg: &config.City{}, cityBeadStore: cache, pokeCh: make(chan struct{}, 2)}
	admissions := make(chan string, 2)
	cr := &CityRuntime{cs: cs, cfg: &config.City{}, stderr: io.Discard, waitDependencyEnqueue: func(id string, _ sessionWaitDependencyCause) error { admissions <- id; return nil }}
	cr.startSessionWaitDependencyShadow()
	t.Cleanup(func() { cs.stopSessionWaitDependencyShadowAdmission(); cr.stopSessionWaitDependencyProducer() })
	if err := backing.Close(depA.ID); err != nil {
		t.Fatal(err)
	}
	closed, err := backing.Get(depA.ID)
	if err != nil {
		t.Fatal(err)
	}
	cs.applyBeadEventToStores(beadSnapshotEvent(t, events.BeadClosed, closed))
	if got := receiveString(t, admissions, "exact dependency admission"); got != "session-a" {
		t.Fatalf("admitted session = %q, want session-a", got)
	}
	select {
	case extra := <-admissions:
		t.Fatalf("unrelated dependency admitted %q", extra)
	default:
	}
}

func TestSessionWaitDependencyRegistrationRecheckRecoversDependencyEventBeforePublication(t *testing.T) {
	backing := beads.NewMemStore()
	dep, err := backing.Create(beads.Bead{})
	if err != nil {
		t.Fatal(err)
	}
	cache := beads.NewCachingStoreForTest(backing, nil)
	if err := cache.PrimeActive(); err != nil {
		t.Fatal(err)
	}
	cs := &controllerState{cfg: &config.City{}, cityBeadStore: cache, pokeCh: make(chan struct{}, 1)}
	sink := make(chan string, 1)
	cr := &CityRuntime{cs: cs, cfg: &config.City{}, stderr: io.Discard, waitDependencyEnqueue: func(id string, _ sessionWaitDependencyCause) error { sink <- id; return nil }}
	cr.startSessionWaitDependencyShadow()
	t.Cleanup(func() { cs.stopSessionWaitDependencyShadowAdmission(); cr.stopSessionWaitDependencyProducer() })

	if err := backing.Close(dep.ID); err != nil {
		t.Fatal(err)
	}
	closed, err := backing.Get(dep.ID)
	if err != nil {
		t.Fatal(err)
	}
	// The dependency transition arrives while the authoritative index is empty.
	cs.applyBeadEventToStores(beadSnapshotEvent(t, events.BeadClosed, closed))
	select {
	case got := <-sink:
		t.Fatalf("pre-publication sink=%q", got)
	default:
	}

	wait, err := backing.Create(sessionWaitShadowBead("session-a", dep.ID))
	if err != nil {
		t.Fatal(err)
	}
	cs.applyBeadEventToStores(beadSnapshotEvent(t, events.BeadCreated, wait))
	if got := receiveString(t, sink, "registration recheck"); got != "session-a" {
		t.Fatalf("sink=%q, want session-a for wait %q", got, wait.ID)
	}
}

func TestSessionWaitDependencyShadowPreservesPolicyTierAndPerformsNoBackingEffects(t *testing.T) {
	recording := beadstest.NewRecordingStore(nil)
	durable, err := recording.Create(sessionWaitShadowBead("session-b", "dep-shared"))
	if err != nil {
		t.Fatalf("Create(durable wait): %v", err)
	}
	ephemeralBead := sessionWaitShadowBead("session-a", "dep-shared")
	ephemeralBead.Ephemeral = true
	ephemeral, err := recording.Create(ephemeralBead)
	if err != nil {
		t.Fatalf("Create(ephemeral wait): %v", err)
	}
	audited := &sessionWaitShadowReadAuditStore{Store: recording}
	cache := beads.NewCachingStoreForTest(audited, nil)
	if err := cache.PrimeActive(); err != nil {
		t.Fatalf("PrimeActive: %v", err)
	}
	primeListCalls := audited.listCalls.Load()
	primeGetCalls := audited.getCalls.Load()
	recording.Reset()

	rawCensus, err := observeSessionWaitCensus(beads.SessionStore{Store: cache})
	if err != nil {
		t.Fatalf("observe raw cache: %v", err)
	}
	rawIDs := sessionWaitShadowWaitIDs(rawCensus.waits)
	if !rawIDs[durable.ID] || rawIDs[ephemeral.ID] || len(rawIDs) != 1 {
		t.Fatalf("raw TierIssues wait IDs = %v, want durable %q only", rawIDs, durable.ID)
	}

	policyStore := wrapStoreWithBeadPolicies(cache, &config.City{})
	policyCensus, err := observeSessionWaitCensus(beads.SessionStore{Store: policyStore})
	if err != nil {
		t.Fatalf("observe policy-wrapped cache: %v", err)
	}
	policyIDs := sessionWaitShadowWaitIDs(policyCensus.waits)
	if !policyIDs[durable.ID] || !policyIDs[ephemeral.ID] || len(policyIDs) != 2 {
		t.Fatalf("policy TierBoth wait IDs = %v, want %q and %q", policyIDs, durable.ID, ephemeral.ID)
	}

	cityRuntime := &CityRuntime{}
	if installed, err := cityRuntime.installObservedSessionWaitDependencyIndex(beads.SessionStore{Store: policyStore}); err != nil || !installed {
		t.Fatalf("install policy census = %v, %v; want true, nil", installed, err)
	}
	assertSessionWaitDependencyIndexSessions(
		t,
		cityRuntime.sessionWaitDependencySessions("dep-shared"),
		[]string{"session-a", "session-b"},
	)
	if got := audited.listCalls.Load(); got != primeListCalls {
		t.Fatalf("backing List calls after PrimeActive = %d, want unchanged %d", got, primeListCalls)
	}
	if got := audited.getCalls.Load(); got != primeGetCalls {
		t.Fatalf("backing Get calls after PrimeActive = %d, want unchanged %d", got, primeGetCalls)
	}
	if calls := recording.Calls(); len(calls) != 0 {
		t.Fatalf("backing mutations during observe/install = %#v, want none", calls)
	}
}

func TestSessionWaitDependencyShadowRejectsUnavailableCache(t *testing.T) {
	_, err := observeSessionWaitCensus(beads.SessionStore{Store: beads.NewMemStore()})
	if !errors.Is(err, beads.ErrCacheUnavailable) {
		t.Fatalf("observe without cache error = %v, want ErrCacheUnavailable", err)
	}

	cache := beads.NewCachingStoreForTest(beads.NewMemStore(), nil)
	if _, err := observeSessionWaitCensus(beads.SessionStore{Store: cache}); !errors.Is(err, beads.ErrCacheUnavailable) {
		t.Fatalf("observe unprimed cache error = %v, want ErrCacheUnavailable", err)
	}
}

func TestSessionWaitDependencyShadowRejectsCappedCensusWithoutReplacingIndex(t *testing.T) {
	rows := make([]beads.Bead, 0, sessionpkg.SessionWaitLookupLimit+1)
	for index := 0; index <= sessionpkg.SessionWaitLookupLimit; index++ {
		row := sessionWaitShadowBead(fmt.Sprintf("session-%04d", index), "dep-overflow")
		row.ID = fmt.Sprintf("wait-%04d", index)
		rows = append(rows, row)
	}
	cache := beads.NewCachingStoreForTest(beads.NewMemStoreFrom(0, rows, nil), nil)
	if err := cache.PrimeActive(); err != nil {
		t.Fatalf("PrimeActive: %v", err)
	}
	cityRuntime := &CityRuntime{}
	installSessionWaitShadowSentinel(t, cityRuntime)
	sentinel := sessionWaitShadowIndex(cityRuntime)

	installed, err := cityRuntime.installObservedSessionWaitDependencyIndex(beads.SessionStore{Store: cache})
	if installed || !beads.IsLookupLimitError(err) {
		t.Fatalf("install capped census = %v, %v; want false, LookupLimitError", installed, err)
	}
	if got := sessionWaitShadowIndex(cityRuntime); got != sentinel {
		t.Fatal("capped census replaced the retained index")
	}
	assertSessionWaitDependencyIndexSessions(t, cityRuntime.sessionWaitDependencySessions("sentinel-dependency"), nil)
	assertSessionWaitDependencyIndexSessions(t, cityRuntime.sessionWaitDependencySessions("dep-overflow"), nil)
	if id := rows[len(rows)-1].ID; !cityRuntime.sessionWaitDependencyContainsWait(id) {
		t.Fatalf("capped census did not retain overflow wait identity %q", id)
	}
}

func TestSessionWaitDependencyShadowRejectsMalformedActiveCensusWithoutReplacingIndex(t *testing.T) {
	malformed := sessionWaitShadowBead("candidate-session", "candidate-dependency")
	malformed.ID = "malformed-wait"
	malformed.Status = "in_progress"
	backing := beads.NewMemStoreFrom(0, []beads.Bead{malformed}, nil)
	cache := beads.NewCachingStoreForTest(backing, nil)
	if err := cache.PrimeActive(); err != nil {
		t.Fatalf("PrimeActive: %v", err)
	}
	cityRuntime := &CityRuntime{}
	installSessionWaitShadowSentinel(t, cityRuntime)
	sentinel := sessionWaitShadowIndex(cityRuntime)

	installed, err := cityRuntime.installObservedSessionWaitDependencyIndex(beads.SessionStore{Store: cache})
	if installed || err == nil || !strings.Contains(err.Error(), `unsupported status "in_progress"`) {
		t.Fatalf("install malformed census = %v, %v; want false and unsupported-status error", installed, err)
	}
	if got := sessionWaitShadowIndex(cityRuntime); got != sentinel {
		t.Fatal("malformed census replaced the retained index")
	}
	assertSessionWaitDependencyIndexSessions(t, cityRuntime.sessionWaitDependencySessions("sentinel-dependency"), nil)
	assertSessionWaitDependencyIndexSessions(t, cityRuntime.sessionWaitDependencySessions("candidate-dependency"), nil)
}

func TestSessionWaitDependencyShadowRejectsStaleObservedCandidate(t *testing.T) {
	backing := beads.NewMemStore()
	if _, err := backing.Create(sessionWaitShadowBead("candidate-session", "candidate-dependency")); err != nil {
		t.Fatalf("Create: %v", err)
	}
	cache := beads.NewCachingStoreForTest(backing, nil)
	if err := cache.PrimeActive(); err != nil {
		t.Fatalf("PrimeActive: %v", err)
	}
	cityRuntime := &CityRuntime{}
	installSessionWaitShadowSentinel(t, cityRuntime)
	census, candidate, err := buildObservedSessionWaitDependencyIndex(beads.SessionStore{Store: cache})
	if err != nil {
		t.Fatalf("build observed candidate: %v", err)
	}
	if err := cache.PrimeActive(); err != nil {
		t.Fatalf("PrimeActive invalidation: %v", err)
	}

	installed, err := cityRuntime.publishObservedSessionWaitDependencyIndex(census, candidate)
	if err != nil || installed {
		t.Fatalf("publish stale candidate = %v, %v; want false, nil", installed, err)
	}
	assertSessionWaitDependencyIndexSessions(
		t,
		cityRuntime.sessionWaitDependencySessions("sentinel-dependency"),
		[]string{"sentinel-session"},
	)
	assertSessionWaitDependencyIndexSessions(t, cityRuntime.sessionWaitDependencySessions("candidate-dependency"), nil)
}

func TestSessionWaitDependencyShadowStartupErrorsAreBestEffort(t *testing.T) {
	malformed := sessionWaitShadowBead("candidate-session", "candidate-dependency")
	malformed.ID = "malformed-wait"
	malformed.Status = "in_progress"
	cache := beads.NewCachingStoreForTest(beads.NewMemStoreFrom(0, []beads.Bead{malformed}, nil), nil)
	if err := cache.PrimeActive(); err != nil {
		t.Fatalf("PrimeActive: %v", err)
	}
	var stderr bytes.Buffer
	cityRuntime := &CityRuntime{
		cfg:                 &config.City{},
		standaloneCityStore: cache,
		logPrefix:           "gc start",
		stderr:              &stderr,
	}
	installSessionWaitShadowSentinel(t, cityRuntime)

	cityRuntime.startSessionWaitDependencyShadow()

	if output := stderr.String(); !strings.Contains(output, "session-wait shadow index") ||
		!strings.Contains(output, `unsupported status "in_progress"`) {
		t.Fatalf("startup diagnostic = %q, want shadow-index and malformed-status context", output)
	}
	assertSessionWaitDependencyIndexSessions(
		t,
		cityRuntime.sessionWaitDependencySessions("sentinel-dependency"),
		nil,
	)

	stderr.Reset()
	cityRuntime.standaloneCityStore = beads.NewMemStore()
	cityRuntime.startSessionWaitDependencyShadow()
	if output := stderr.String(); output != "" {
		t.Fatalf("unavailable-cache startup diagnostic = %q, want silent best-effort disable", output)
	}
}

func newSessionWaitDependencyEventShadow(
	t *testing.T,
) (*CityRuntime, *controllerState, *beads.CachingStore, *beads.MemStore, *bytes.Buffer) {
	t.Helper()
	backing := beads.NewMemStore()
	cache := beads.NewCachingStoreForTest(backing, nil)
	if err := cache.PrimeActive(); err != nil {
		t.Fatalf("PrimeActive: %v", err)
	}
	cfg := &config.City{}
	cs := &controllerState{
		cfg:           cfg,
		cityBeadStore: cache,
		pokeCh:        make(chan struct{}, 16),
	}
	var stderr bytes.Buffer
	cityRuntime := &CityRuntime{
		cs:        cs,
		cfg:       cfg,
		logPrefix: "gc start",
		stderr:    &stderr,
	}
	cityRuntime.startSessionWaitDependencyShadow()
	t.Cleanup(cs.stopSessionWaitDependencyShadowAdmission)
	return cityRuntime, cs, cache, backing, &stderr
}

func beadSnapshotEvent(t *testing.T, eventType string, bead beads.Bead) events.Event {
	t.Helper()
	payload, err := json.Marshal(bead)
	if err != nil {
		t.Fatalf("marshal bead snapshot: %v", err)
	}
	return events.Event{
		Type:    eventType,
		Actor:   "bd-hook",
		Subject: bead.ID,
		Payload: payload,
	}
}

func TestSessionWaitDependencyShadowConvergesFromPostCacheEvents(t *testing.T) {
	previousDispatch := beadCloseAutocloseDispatch
	beadCloseAutocloseDispatch = func(func()) {}
	t.Cleanup(func() { beadCloseAutocloseDispatch = previousDispatch })

	cityRuntime, cs, _, backing, _ := newSessionWaitDependencyEventShadow(t)
	assertSessionWaitDependencyIndexSessions(t, cityRuntime.sessionWaitDependencySessions("dep-a"), nil)

	wait, err := backing.Create(sessionWaitShadowBead("session-a", "dep-a"))
	if err != nil {
		t.Fatalf("Create(wait): %v", err)
	}
	cs.applyBeadEventToStores(beadSnapshotEvent(t, events.BeadCreated, wait))
	assertSessionWaitDependencyIndexSessions(
		t,
		cityRuntime.sessionWaitDependencySessions("dep-a"),
		[]string{"session-a"},
	)

	if err := backing.Update(wait.ID, beads.UpdateOpts{Metadata: map[string]string{"dep_ids": "dep-b"}}); err != nil {
		t.Fatalf("Update wait dependency: %v", err)
	}
	wait, err = backing.Get(wait.ID)
	if err != nil {
		t.Fatalf("Get updated wait: %v", err)
	}
	cs.applyBeadEventToStores(beadSnapshotEvent(t, events.BeadUpdated, wait))
	assertSessionWaitDependencyIndexSessions(t, cityRuntime.sessionWaitDependencySessions("dep-a"), nil)
	assertSessionWaitDependencyIndexSessions(
		t,
		cityRuntime.sessionWaitDependencySessions("dep-b"),
		[]string{"session-a"},
	)

	if err := backing.Close(wait.ID); err != nil {
		t.Fatalf("Close wait: %v", err)
	}
	wait, err = backing.Get(wait.ID)
	if err != nil {
		t.Fatalf("Get closed wait: %v", err)
	}
	cs.applyBeadEventToStores(beadSnapshotEvent(t, events.BeadClosed, wait))
	assertSessionWaitDependencyIndexSessions(t, cityRuntime.sessionWaitDependencySessions("dep-b"), nil)

	wait, err = backing.Create(sessionWaitShadowBead("session-b", "dep-c"))
	if err != nil {
		t.Fatalf("Create second wait: %v", err)
	}
	cs.applyBeadEventToStores(beadSnapshotEvent(t, events.BeadCreated, wait))
	assertSessionWaitDependencyIndexSessions(
		t,
		cityRuntime.sessionWaitDependencySessions("dep-c"),
		[]string{"session-b"},
	)
	if err := backing.Delete(wait.ID); err != nil {
		t.Fatalf("Delete wait: %v", err)
	}
	cs.applyBeadEventToStores(beadSnapshotEvent(t, events.BeadDeleted, wait))
	assertSessionWaitDependencyIndexSessions(t, cityRuntime.sessionWaitDependencySessions("dep-c"), nil)
}

func TestSessionWaitDependencyShadowRemovesWaitThatLosesIdentity(t *testing.T) {
	cityRuntime, cs, _, backing, _ := newSessionWaitDependencyEventShadow(t)
	wait, err := backing.Create(sessionWaitShadowBead("session-a", "dep-a"))
	if err != nil {
		t.Fatalf("Create(wait): %v", err)
	}
	cs.applyBeadEventToStores(beadSnapshotEvent(t, events.BeadCreated, wait))
	assertSessionWaitDependencyIndexSessions(
		t,
		cityRuntime.sessionWaitDependencySessions("dep-a"),
		[]string{"session-a"},
	)

	nonWaitType := "task"
	if err := backing.Update(wait.ID, beads.UpdateOpts{
		Type:         &nonWaitType,
		RemoveLabels: []string{sessionpkg.WaitBeadLabel},
	}); err != nil {
		t.Fatalf("remove wait identity: %v", err)
	}
	nonWait, err := backing.Get(wait.ID)
	if err != nil {
		t.Fatalf("Get non-wait: %v", err)
	}
	if sessionpkg.IsWaitBead(nonWait) {
		t.Fatalf("updated bead = %#v, want non-wait test precondition", nonWait)
	}

	cs.applyBeadEventToStores(beadSnapshotEvent(t, events.BeadUpdated, nonWait))
	assertSessionWaitDependencyIndexSessions(t, cityRuntime.sessionWaitDependencySessions("dep-a"), nil)
}

func TestSessionWaitDependencyShadowDeterministicErrorAwaitsRelevantChange(t *testing.T) {
	backing := beads.NewMemStore()
	wait, err := backing.Create(sessionWaitShadowBead("candidate-session", "candidate-dependency"))
	if err != nil {
		t.Fatalf("Create(wait): %v", err)
	}
	cache := beads.NewCachingStoreForTest(backing, nil)
	if err := cache.PrimeActive(); err != nil {
		t.Fatalf("PrimeActive: %v", err)
	}
	cfg := &config.City{}
	cs := &controllerState{
		cfg:           cfg,
		cityBeadStore: cache,
		pokeCh:        make(chan struct{}, 4),
	}
	var stderr bytes.Buffer
	cityRuntime := &CityRuntime{
		cs:        cs,
		cfg:       cfg,
		logPrefix: "gc start",
		stderr:    &stderr,
	}
	cityRuntime.startSessionWaitDependencyShadow()
	t.Cleanup(cs.stopSessionWaitDependencyShadowAdmission)
	assertSessionWaitDependencyIndexSessions(
		t,
		cityRuntime.sessionWaitDependencySessions("candidate-dependency"),
		[]string{"candidate-session"},
	)

	malformedStatus := "in_progress"
	if err := cache.Update(wait.ID, beads.UpdateOpts{Status: &malformedStatus}); err != nil {
		t.Fatalf("make cached wait malformed: %v", err)
	}
	malformed, err := cache.Get(wait.ID)
	if err != nil {
		t.Fatalf("Get malformed wait: %v", err)
	}
	cs.admitSessionWaitDependencyShadowEvent(beadSnapshotEvent(t, events.BeadUpdated, malformed))
	assertSessionWaitDependencyIndexSessions(
		t,
		cityRuntime.sessionWaitDependencySessions("candidate-dependency"),
		nil,
	)
	for index := 0; index < 3; index++ {
		cs.admitSessionWaitDependencyShadowEvent(beadSnapshotEvent(t, events.BeadUpdated, beads.Bead{
			ID:     fmt.Sprintf("unrelated-task-%d", index),
			Type:   "task",
			Status: "open",
		}))
	}
	if got := strings.Count(stderr.String(), "session-wait shadow refresh:"); got != 1 {
		t.Fatalf("refresh diagnostics = %d in %q, want one malformed-census episode", got, stderr.String())
	}

	open := "open"
	if err := cache.Update(wait.ID, beads.UpdateOpts{
		Status:   &open,
		Metadata: map[string]string{"dep_ids": "repaired-dependency"},
	}); err != nil {
		t.Fatalf("repair cached wait: %v", err)
	}
	cs.admitSessionWaitDependencyShadowEvent(beadSnapshotEvent(t, events.BeadUpdated, beads.Bead{
		ID:     "unrelated-task",
		Type:   "task",
		Status: "open",
	}))
	assertSessionWaitDependencyIndexSessions(
		t,
		cityRuntime.sessionWaitDependencySessions("candidate-dependency"),
		nil,
	)
	assertSessionWaitDependencyIndexSessions(t, cityRuntime.sessionWaitDependencySessions("repaired-dependency"), nil)

	repaired, err := cache.Get(wait.ID)
	if err != nil {
		t.Fatalf("Get repaired wait: %v", err)
	}
	cs.admitSessionWaitDependencyShadowEvent(beadSnapshotEvent(t, events.BeadUpdated, repaired))
	assertSessionWaitDependencyIndexSessions(t, cityRuntime.sessionWaitDependencySessions("candidate-dependency"), nil)
	assertSessionWaitDependencyIndexSessions(
		t,
		cityRuntime.sessionWaitDependencySessions("repaired-dependency"),
		[]string{"candidate-session"},
	)
}

func TestSessionWaitDependencyShadowDeterministicBlockerIdentityRemovalRearms(t *testing.T) {
	cityRuntime, cs, cache, backing, stderr := newSessionWaitDependencyEventShadow(t)

	indexed, err := backing.Create(sessionWaitShadowBead("indexed-session", "dep-old"))
	if err != nil {
		t.Fatalf("Create(indexed wait): %v", err)
	}
	cs.applyBeadEventToStores(beadSnapshotEvent(t, events.BeadCreated, indexed))
	assertSessionWaitDependencyIndexSessions(
		t,
		cityRuntime.sessionWaitDependencySessions("dep-old"),
		[]string{"indexed-session"},
	)

	malformedBead := sessionWaitShadowBead("blocked-session", "dep-blocked")
	malformed, err := cache.Create(malformedBead)
	if err != nil {
		t.Fatalf("Create(malformed wait): %v", err)
	}
	malformedStatus := "in_progress"
	if err := cache.Update(malformed.ID, beads.UpdateOpts{Status: &malformedStatus}); err != nil {
		t.Fatalf("make wait malformed: %v", err)
	}
	malformed, err = cache.Get(malformed.ID)
	if err != nil {
		t.Fatalf("Get malformed wait: %v", err)
	}
	cs.admitSessionWaitDependencyShadowEvent(beadSnapshotEvent(t, events.BeadCreated, malformed))
	if !strings.Contains(stderr.String(), `unsupported status "in_progress"`) {
		t.Fatalf("refresh diagnostic = %q, want malformed blocker", stderr.String())
	}
	if !cityRuntime.sessionWaitDependencyContainsWait(malformed.ID) {
		t.Fatal("rejected census did not retain malformed wait identity")
	}

	if err := backing.Update(indexed.ID, beads.UpdateOpts{
		Metadata: map[string]string{"dep_ids": "dep-new"},
	}); err != nil {
		t.Fatalf("Update indexed wait behind blocker: %v", err)
	}
	indexed, err = backing.Get(indexed.ID)
	if err != nil {
		t.Fatalf("Get updated indexed wait: %v", err)
	}
	cs.applyBeadEventToStores(beadSnapshotEvent(t, events.BeadUpdated, indexed))
	assertSessionWaitDependencyIndexSessions(
		t,
		cityRuntime.sessionWaitDependencySessions("dep-old"),
		nil,
	)
	assertSessionWaitDependencyIndexSessions(t, cityRuntime.sessionWaitDependencySessions("dep-new"), nil)

	nonWaitType := "task"
	if err := cache.Update(malformed.ID, beads.UpdateOpts{
		Type:         &nonWaitType,
		RemoveLabels: []string{sessionpkg.WaitBeadLabel},
	}); err != nil {
		t.Fatalf("remove blocker wait identity: %v", err)
	}
	nonWait, err := cache.Get(malformed.ID)
	if err != nil {
		t.Fatalf("Get repaired non-wait: %v", err)
	}
	if sessionpkg.IsWaitBead(nonWait) {
		t.Fatalf("repaired cache row = %#v, want non-wait", nonWait)
	}
	cs.admitSessionWaitDependencyShadowEvent(beadSnapshotEvent(t, events.BeadUpdated, nonWait))

	assertSessionWaitDependencyIndexSessions(t, cityRuntime.sessionWaitDependencySessions("dep-old"), nil)
	assertSessionWaitDependencyIndexSessions(
		t,
		cityRuntime.sessionWaitDependencySessions("dep-new"),
		[]string{"indexed-session"},
	)
	if cityRuntime.sessionWaitDependencyContainsWait(malformed.ID) {
		t.Fatal("successful recovery retained rejected non-wait identity")
	}
}

func TestSessionWaitDependencyShadowStaleRejectedCensusCannotReplaceNewerBlocker(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		cache := beads.NewCachingStoreForTest(beads.NewMemStore(), nil)
		if err := cache.PrimeActive(); err != nil {
			t.Fatalf("PrimeActive: %v", err)
		}
		first, err := cache.Create(sessionWaitShadowBead("first-session", "first-dependency"))
		if err != nil {
			t.Fatalf("Create(first wait): %v", err)
		}
		malformedStatus := "in_progress"
		if err := cache.Update(first.ID, beads.UpdateOpts{Status: &malformedStatus}); err != nil {
			t.Fatalf("make first wait malformed: %v", err)
		}

		cityRuntime := &CityRuntime{}
		cs := &controllerState{}
		firstBuilt := make(chan struct{})
		releaseFirst := make(chan struct{})
		firstReturned := make(chan struct{})
		var calls atomic.Int64
		if err := cs.installSessionWaitDependencyShadowAdmission(func() sessionWaitShadowRefreshResult {
			attempt := calls.Add(1)
			census, _, buildErr := buildObservedSessionWaitDependencyIndex(beads.SessionStore{Store: cache})
			if buildErr == nil {
				t.Errorf("build rejected census attempt %d returned nil error", attempt)
				return sessionWaitShadowRetry
			}
			if attempt == 1 {
				close(firstBuilt)
				<-releaseFirst
			}
			retained, retainErr := cityRuntime.publishRejectedSessionWaitDependencyCensus(census)
			if retainErr != nil {
				t.Errorf("publish rejected census attempt %d: %v", attempt, retainErr)
				return sessionWaitShadowRetry
			}
			if retained {
				return sessionWaitShadowAwaitRelevant
			}
			return sessionWaitShadowRetry
		}, cityRuntime.sessionWaitDependencyContainsWait); err != nil {
			t.Fatalf("install admission: %v", err)
		}
		t.Cleanup(cs.stopSessionWaitDependencyShadowAdmission)

		go func() {
			cs.requestSessionWaitDependencyShadowRefreshForBead(beads.Bead{}, true)
			close(firstReturned)
		}()
		synctest.Wait()
		select {
		case <-firstBuilt:
		default:
			t.Fatal("first rejected census did not build")
		}

		nonWaitType := "task"
		if err := cache.Update(first.ID, beads.UpdateOpts{
			Type:         &nonWaitType,
			RemoveLabels: []string{sessionpkg.WaitBeadLabel},
		}); err != nil {
			t.Fatalf("remove first wait identity: %v", err)
		}
		second, err := cache.Create(sessionWaitShadowBead("second-session", "second-dependency"))
		if err != nil {
			t.Fatalf("Create(second wait): %v", err)
		}
		if err := cache.Update(second.ID, beads.UpdateOpts{Status: &malformedStatus}); err != nil {
			t.Fatalf("make second wait malformed: %v", err)
		}

		cs.requestSessionWaitDependencyShadowRefreshForBead(beads.Bead{}, true)
		if !cityRuntime.sessionWaitDependencyContainsWait(second.ID) {
			t.Fatal("newer rejected census did not retain its malformed wait")
		}
		if cityRuntime.sessionWaitDependencyContainsWait(first.ID) {
			t.Fatal("newer rejected census retained removed first wait")
		}

		close(releaseFirst)
		synctest.Wait()
		select {
		case <-firstReturned:
		default:
			t.Fatal("older rejected census did not return")
		}
		if !cityRuntime.sessionWaitDependencyContainsWait(second.ID) {
			t.Fatal("older rejected census replaced the newer malformed wait identity")
		}
		if cityRuntime.sessionWaitDependencyContainsWait(first.ID) {
			t.Fatal("older rejected census reintroduced the removed first wait identity")
		}
	})
}

func TestSessionWaitDependencyShadowRetriesUnavailableStartupAfterCacheHeals(t *testing.T) {
	backing := beads.NewMemStore()
	if _, err := backing.Create(sessionWaitShadowBead("session-a", "dep-a")); err != nil {
		t.Fatalf("Create(wait): %v", err)
	}
	cache := beads.NewCachingStoreForTest(backing, nil)
	cfg := &config.City{}
	cs := &controllerState{
		cfg:           cfg,
		cityBeadStore: cache,
		pokeCh:        make(chan struct{}, 2),
	}
	var stderr bytes.Buffer
	cityRuntime := &CityRuntime{
		cs:        cs,
		cfg:       cfg,
		logPrefix: "gc start",
		stderr:    &stderr,
	}

	cityRuntime.startSessionWaitDependencyShadow()
	t.Cleanup(cs.stopSessionWaitDependencyShadowAdmission)
	assertSessionWaitDependencyIndexSessions(t, cityRuntime.sessionWaitDependencySessions("dep-a"), nil)
	if stderr.String() != "" {
		t.Fatalf("unavailable startup diagnostic = %q, want silent pending retry", stderr.String())
	}

	if err := cache.PrimeActive(); err != nil {
		t.Fatalf("PrimeActive after startup: %v", err)
	}
	cs.admitSessionWaitDependencyShadowEvent(beadSnapshotEvent(t, events.BeadUpdated, beads.Bead{
		ID:     "unrelated-task",
		Type:   "task",
		Status: "open",
	}))

	assertSessionWaitDependencyIndexSessions(
		t,
		cityRuntime.sessionWaitDependencySessions("dep-a"),
		[]string{"session-a"},
	)
}

func TestSessionWaitDependencyShadowUsesCacheTruthAfterRejectedStalePayload(t *testing.T) {
	cityRuntime, cs, cache, backing, _ := newSessionWaitDependencyEventShadow(t)
	wait, err := backing.Create(sessionWaitShadowBead("session-a", "dep-original"))
	if err != nil {
		t.Fatalf("Create(wait): %v", err)
	}
	cs.applyBeadEventToStores(beadSnapshotEvent(t, events.BeadCreated, wait))
	assertSessionWaitDependencyIndexSessions(
		t,
		cityRuntime.sessionWaitDependencySessions("dep-original"),
		[]string{"session-a"},
	)

	stale := sessionWaitShadowBead("session-a", "dep-stale")
	stale.ID = wait.ID
	if err := cache.Update(wait.ID, beads.UpdateOpts{
		Metadata: map[string]string{"dep_ids": "dep-live"},
	}); err != nil {
		t.Fatalf("update cached wait: %v", err)
	}

	cs.applyBeadEventToStores(beadSnapshotEvent(t, events.BeadUpdated, stale))

	cached, err := cache.Get(wait.ID)
	if err != nil {
		t.Fatalf("Get cached wait: %v", err)
	}
	if got := cached.Metadata["dep_ids"]; got != "dep-live" {
		t.Fatalf("cached dependency after stale payload = %q, want dep-live", got)
	}
	assertSessionWaitDependencyIndexSessions(t, cityRuntime.sessionWaitDependencySessions("dep-original"), nil)
	assertSessionWaitDependencyIndexSessions(t, cityRuntime.sessionWaitDependencySessions("dep-stale"), nil)
	assertSessionWaitDependencyIndexSessions(
		t,
		cityRuntime.sessionWaitDependencySessions("dep-live"),
		[]string{"session-a"},
	)
}

func TestSessionWaitDependencyShadowRetriesRealStaleObservationOnUnrelatedEvent(t *testing.T) {
	backing := beads.NewMemStore()
	if _, err := backing.Create(sessionWaitShadowBead("candidate-session", "candidate-dependency")); err != nil {
		t.Fatalf("Create(wait): %v", err)
	}
	cache := beads.NewCachingStoreForTest(backing, nil)
	if err := cache.PrimeActive(); err != nil {
		t.Fatalf("PrimeActive: %v", err)
	}
	cityRuntime := &CityRuntime{}
	installSessionWaitShadowSentinel(t, cityRuntime)
	cs := &controllerState{}
	var calls int
	if err := cs.installSessionWaitDependencyShadowAdmission(func() sessionWaitShadowRefreshResult {
		calls++
		census, candidate, buildErr := buildObservedSessionWaitDependencyIndex(beads.SessionStore{Store: cache})
		if buildErr != nil {
			t.Errorf("build observed candidate: %v", buildErr)
			return sessionWaitShadowRetry
		}
		if calls == 1 {
			if primeErr := cache.PrimeActive(); primeErr != nil {
				t.Errorf("PrimeActive invalidation: %v", primeErr)
				return sessionWaitShadowRetry
			}
		}
		installed, publishErr := cityRuntime.publishObservedSessionWaitDependencyIndex(census, candidate)
		if publishErr != nil {
			t.Errorf("publish observed candidate: %v", publishErr)
			return sessionWaitShadowRetry
		}
		if installed {
			return sessionWaitShadowConverged
		}
		return sessionWaitShadowRetry
	}, cityRuntime.sessionWaitDependencyContainsWait); err != nil {
		t.Fatalf("install admission: %v", err)
	}
	t.Cleanup(cs.stopSessionWaitDependencyShadowAdmission)

	cs.requestSessionWaitDependencyShadowRefreshForBead(beads.Bead{}, true)
	if calls != 1 {
		t.Fatalf("refresh calls after stale publication = %d, want 1", calls)
	}
	assertSessionWaitDependencyIndexSessions(
		t,
		cityRuntime.sessionWaitDependencySessions("sentinel-dependency"),
		[]string{"sentinel-session"},
	)
	assertSessionWaitDependencyIndexSessions(t, cityRuntime.sessionWaitDependencySessions("candidate-dependency"), nil)

	cs.admitSessionWaitDependencyShadowEvent(beadSnapshotEvent(t, events.BeadUpdated, beads.Bead{
		ID:     "unrelated-task",
		Type:   "task",
		Status: "open",
	}))
	if calls != 2 {
		t.Fatalf("refresh calls after unrelated retry signal = %d, want 2", calls)
	}
	assertSessionWaitDependencyIndexSessions(t, cityRuntime.sessionWaitDependencySessions("sentinel-dependency"), nil)
	assertSessionWaitDependencyIndexSessions(
		t,
		cityRuntime.sessionWaitDependencySessions("candidate-dependency"),
		[]string{"candidate-session"},
	)
}

func TestSessionWaitDependencyShadowBootstrapIdentityRemovalRaceConverges(t *testing.T) {
	backing := beads.NewMemStore()
	wait, err := backing.Create(sessionWaitShadowBead("candidate-session", "candidate-dependency"))
	if err != nil {
		t.Fatalf("Create(wait): %v", err)
	}
	cache := beads.NewCachingStoreForTest(backing, nil)
	if err := cache.PrimeActive(); err != nil {
		t.Fatalf("PrimeActive: %v", err)
	}
	cs := &controllerState{
		cityBeadStore: cache,
		pokeCh:        make(chan struct{}, 2),
	}
	cityRuntime := &CityRuntime{}
	var calls int
	if err := cs.installSessionWaitDependencyShadowAdmission(func() sessionWaitShadowRefreshResult {
		calls++
		census, candidate, buildErr := buildObservedSessionWaitDependencyIndex(beads.SessionStore{Store: cache})
		if buildErr != nil {
			t.Errorf("build observed candidate: %v", buildErr)
			return sessionWaitShadowRetry
		}
		if calls == 1 {
			nonWaitType := "task"
			if updateErr := backing.Update(wait.ID, beads.UpdateOpts{
				Type:         &nonWaitType,
				RemoveLabels: []string{sessionpkg.WaitBeadLabel},
			}); updateErr != nil {
				t.Errorf("remove wait identity: %v", updateErr)
				return sessionWaitShadowRetry
			}
			nonWait, getErr := backing.Get(wait.ID)
			if getErr != nil {
				t.Errorf("Get non-wait: %v", getErr)
				return sessionWaitShadowRetry
			}
			cs.applyBeadEventToStores(beadSnapshotEvent(t, events.BeadUpdated, nonWait))
		}
		installed, publishErr := cityRuntime.publishObservedSessionWaitDependencyIndex(census, candidate)
		if publishErr != nil {
			t.Errorf("publish observed candidate: %v", publishErr)
			return sessionWaitShadowRetry
		}
		if installed {
			return sessionWaitShadowConverged
		}
		return sessionWaitShadowRetry
	}, cityRuntime.sessionWaitDependencyContainsWait); err != nil {
		t.Fatalf("install admission: %v", err)
	}
	t.Cleanup(cs.stopSessionWaitDependencyShadowAdmission)

	cs.requestSessionWaitDependencyShadowRefreshForBead(beads.Bead{}, true)

	if calls != 2 {
		t.Fatalf("refresh calls = %d, want stale first census plus converged post-event census", calls)
	}
	if index := sessionWaitShadowIndex(cityRuntime); index == nil {
		t.Fatal("bootstrap race left the shadow index uninstalled")
	}
	assertSessionWaitDependencyIndexSessions(t, cityRuntime.sessionWaitDependencySessions("candidate-dependency"), nil)
}
