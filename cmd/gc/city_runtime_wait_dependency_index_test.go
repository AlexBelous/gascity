package main

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/beads/beadstest"
	"github.com/gastownhall/gascity/internal/config"
	sessionpkg "github.com/gastownhall/gascity/internal/session"
)

type sessionWaitShadowReadAuditStore struct {
	beads.Store
	listCalls atomic.Int64
}

func (s *sessionWaitShadowReadAuditStore) List(query beads.ListQuery) ([]beads.Bead, error) {
	s.listCalls.Add(1)
	return s.Store.List(query)
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

	installed, err := cityRuntime.installObservedSessionWaitDependencyIndex(beads.SessionStore{Store: cache})
	if installed || !beads.IsLookupLimitError(err) {
		t.Fatalf("install capped census = %v, %v; want false, LookupLimitError", installed, err)
	}
	assertSessionWaitDependencyIndexSessions(
		t,
		cityRuntime.sessionWaitDependencySessions("sentinel-dependency"),
		[]string{"sentinel-session"},
	)
	assertSessionWaitDependencyIndexSessions(t, cityRuntime.sessionWaitDependencySessions("dep-overflow"), nil)
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

	installed, err := cityRuntime.installObservedSessionWaitDependencyIndex(beads.SessionStore{Store: cache})
	if installed || err == nil || !strings.Contains(err.Error(), `unsupported status "in_progress"`) {
		t.Fatalf("install malformed census = %v, %v; want false and unsupported-status error", installed, err)
	}
	assertSessionWaitDependencyIndexSessions(
		t,
		cityRuntime.sessionWaitDependencySessions("sentinel-dependency"),
		[]string{"sentinel-session"},
	)
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
		[]string{"sentinel-session"},
	)

	stderr.Reset()
	cityRuntime.standaloneCityStore = beads.NewMemStore()
	cityRuntime.startSessionWaitDependencyShadow()
	if output := stderr.String(); output != "" {
		t.Fatalf("unavailable-cache startup diagnostic = %q, want silent best-effort disable", output)
	}
}
