package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/beads/beadstest"
	"github.com/gastownhall/gascity/internal/config"
	"github.com/gastownhall/gascity/internal/events"
	"github.com/gastownhall/gascity/internal/rollout"
	sessionpkg "github.com/gastownhall/gascity/internal/session"
)

type sessionWaitShadowReadAuditStore struct {
	beads.Store
	listCalls atomic.Int64
	getCalls  atomic.Int64
	onGet     func(string)
	blockID   string
	entered   chan<- struct{}
	release   <-chan struct{}
	failID    string
	failErr   error
}

type sessionWaitDependencyPrefixAuditStore struct {
	beads.Store
	prefix string
	gets   *atomic.Int64
}

func (s sessionWaitDependencyPrefixAuditStore) IDPrefix() string { return s.prefix }

func (s sessionWaitDependencyPrefixAuditStore) Get(id string) (beads.Bead, error) {
	s.gets.Add(1)
	return s.Store.Get(id)
}

func (s *sessionWaitShadowReadAuditStore) List(query beads.ListQuery) ([]beads.Bead, error) {
	s.listCalls.Add(1)
	return s.Store.List(query)
}

func (s *sessionWaitShadowReadAuditStore) Get(id string) (beads.Bead, error) {
	s.getCalls.Add(1)
	if s.onGet != nil {
		s.onGet(id)
	}
	if id == s.blockID && s.entered != nil {
		s.entered <- struct{}{}
		<-s.release
	}
	if id == s.failID && s.failErr != nil {
		return beads.Bead{}, s.failErr
	}
	return s.Store.Get(id)
}

func (s *sessionWaitShadowReadAuditStore) UpdateIfMatch(id string, revision int64, opts beads.UpdateOpts) error {
	writer, ok := s.Store.(beads.ConditionalWriter)
	if !ok {
		return beads.ErrConditionalWriteUnsupported
	}
	return writer.UpdateIfMatch(id, revision, opts)
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

func TestSessionWaitDependencyLifecycleShadowSinkFollowsSessionReconcilerRollout(t *testing.T) {
	off := &CityRuntime{cs: &controllerState{}}
	off.enableSessionWaitDependencyLifecycleShadowSink(t.Context())
	if off.waitDependencyEnqueue != nil {
		t.Fatal("default/off session reconciler installed a dependency shadow sink")
	}

	autoDegraded := &CityRuntime{cs: &controllerState{
		rolloutFlags: rollout.ForTest(rollout.WithSessionReconciler(rollout.Auto)),
	}}
	autoDegraded.enableSessionWaitDependencyLifecycleShadowSink(t.Context())
	if autoDegraded.waitDependencyEnqueue != nil {
		t.Fatal("auto session reconciler without keyed ownership installed a dependency shadow sink")
	}
}

func TestSessionWaitDependencyEventUsesInstalledLifecycleShadowSinkForExactTarget(t *testing.T) {
	for _, test := range []struct {
		name string
		mode rollout.Mode
	}{
		{name: "auto", mode: rollout.Auto},
		{name: "require", mode: rollout.Require},
	} {
		t.Run(test.name, func(t *testing.T) {
			testSessionWaitDependencyEventUsesInstalledLifecycleShadowSinkForExactTarget(t, test.mode)
		})
	}
}

func TestSessionWaitDependencyLivePendingRetainsTargetUntilAuthoritativeClose(t *testing.T) {
	env := newReconcilerTestEnv()
	env.cfg = &config.City{
		Workspace: config.Workspace{Name: "test-city"},
		Agents:    []config.Agent{{Name: "worker", StartCommand: "true"}},
	}
	dependency, err := env.store.Create(beads.Bead{})
	if err != nil {
		t.Fatal(err)
	}
	target := env.createSessionBead("worker", "worker")
	env.setSessionMetadata(&target, map[string]string{
		"state":                     string(sessionpkg.StateCreating),
		"pending_create_claim":      "true",
		"pending_create_started_at": env.clk.Now().UTC().Format(time.RFC3339),
	})
	wait, err := env.store.Create(sessionWaitShadowBead(target.ID, dependency.ID))
	if err != nil {
		t.Fatal(err)
	}
	targetRead := make(chan struct{}, 2)
	audited := &sessionWaitShadowReadAuditStore{Store: env.store, onGet: func(id string) {
		if id == target.ID {
			targetRead <- struct{}{}
		}
	}}
	cache := beads.NewCachingStoreForTest(audited, nil)
	if err := cache.PrimeActive(); err != nil {
		t.Fatal(err)
	}
	cs := &controllerState{
		cfg: env.cfg, sp: env.sp, cityPath: t.TempDir(), cityBeadStore: cache,
		eventProv: events.NewFake(), pokeCh: make(chan struct{}, 2),
		rolloutFlags:           rollout.ForTest(rollout.WithSessionReconciler(rollout.Auto)),
		sessionStartGeneration: 1, sessionStartStoreGeneration: 1,
	}
	cr := &CityRuntime{
		cs: cs, cfg: env.cfg, stderr: io.Discard,
		sessionStartOwnership: sessionStartOwnershipKeyed,
	}
	cr.startSessionWaitDependencyShadowWithContext(t.Context())
	t.Cleanup(func() { cs.stopSessionWaitDependencyShadowAdmission(); cr.stopSessionWaitDependencyProducer() })
	cr.stopSessionWaitDependencyProducer()
	if !cr.startSessionWaitDependencyProducer() {
		t.Fatal("restart wait dependency producer")
	}

	cachedReady := dependency
	cachedReady.Status = "closed"
	cachedReady.Revision++
	cacheReadyEvent := beadSnapshotEvent(t, events.BeadUpdated, cachedReady)
	cache.ApplyEvent(cacheReadyEvent.Type, cacheReadyEvent.Payload)
	if got, err := cache.Get(dependency.ID); err != nil || got.Status != "closed" {
		t.Fatalf("cached dependency = (%q, %v), want closed", got.Status, err)
	}
	if got, err := env.store.Get(dependency.ID); err != nil || got.Status != "open" {
		t.Fatalf("live dependency = (%q, %v), want open", got.Status, err)
	}
	cs.admitSessionWaitDependencyShadowEvent(cacheReadyEvent)
	awaitClose(t, targetRead, "authoritative pending dependency evaluation")
	cr.stopSessionWaitDependencyProducer()
	if _, ok := cr.sessionWaitDependencyTarget(wait.ID); !ok {
		t.Fatal("authoritative pending dependency retired certified wait target")
	}
	if got := env.sp.CountCalls("IsRunning", "worker"); got != 0 {
		t.Fatalf("runtime observations while dependency live-pending = %d, want 0", got)
	}

	if !cr.startSessionWaitDependencyProducer() {
		t.Fatal("restart wait dependency producer after live-pending evaluation")
	}
	if err := env.store.Close(dependency.ID); err != nil {
		t.Fatal(err)
	}
	closed, err := env.store.Get(dependency.ID)
	if err != nil {
		t.Fatal(err)
	}
	closeEvent := beadSnapshotEvent(t, events.BeadClosed, closed)
	cache.ApplyEvent(closeEvent.Type, closeEvent.Payload)
	cs.admitSessionWaitDependencyShadowEvent(closeEvent)
	awaitClose(t, targetRead, "authoritative closed dependency evaluation")
	cr.stopSessionWaitDependencyProducer()
	if _, ok := cr.sessionWaitDependencyTarget(wait.ID); !ok {
		t.Fatal("read-only dependency evaluation retired the still-open durable wait target")
	}
	if got := env.sp.CountCalls("IsRunning", "worker"); got != 1 {
		t.Fatalf("runtime observations after authoritative close = %d, want 1", got)
	}
}

func TestSessionWaitDependencyDelayedWaitCommitRetainsTargetUntilDurableWaitClose(t *testing.T) {
	env := newReconcilerTestEnv()
	env.cfg = &config.City{
		Workspace: config.Workspace{Name: "test-city"},
		Agents:    []config.Agent{{Name: "worker", StartCommand: "true"}},
	}
	dependency, err := env.store.Create(beads.Bead{})
	if err != nil {
		t.Fatal(err)
	}
	if err := env.store.Close(dependency.ID); err != nil {
		t.Fatal(err)
	}
	target := env.createSessionBead("worker", "worker")
	env.setSessionMetadata(&target, map[string]string{
		"state":                     string(sessionpkg.StateCreating),
		"pending_create_claim":      "true",
		"pending_create_started_at": env.clk.Now().UTC().Format(time.RFC3339),
	})
	cache := beads.NewCachingStoreForTest(env.store, nil)
	if err := cache.PrimeActive(); err != nil {
		t.Fatal(err)
	}
	cityPath := t.TempDir()
	tracer := newSessionReconcilerTraceManager(cityPath, "test-city", io.Discard)
	t.Cleanup(func() { _ = tracer.Close() })
	cs := &controllerState{
		cfg: env.cfg, sp: env.sp, cityPath: cityPath, cityBeadStore: cache,
		eventProv: events.NewFake(), pokeCh: make(chan struct{}, 2),
		rolloutFlags:           rollout.ForTest(rollout.WithSessionReconciler(rollout.Auto)),
		sessionStartGeneration: 1, sessionStartStoreGeneration: 1,
	}
	cr := &CityRuntime{
		cs: cs, cfg: env.cfg, stderr: io.Discard, trace: tracer,
		sessionStartOwnership: sessionStartOwnershipKeyed,
	}
	cr.startSessionWaitDependencyShadowWithContext(t.Context())
	t.Cleanup(func() { cs.stopSessionWaitDependencyShadowAdmission(); cr.stopSessionWaitDependencyProducer() })
	// Drain the startup census before admitting the delayed wait.created event;
	// this keeps the wait_commit outcome independent of startup work.
	cr.stopSessionWaitDependencyProducer()
	if !cr.startSessionWaitDependencyProducer() {
		t.Fatal("restart wait dependency producer after startup census")
	}

	// The delayed wait.created arrives only after the cache and durable store
	// already agree the dependency is closed.
	wait, err := env.store.Create(sessionWaitShadowBead(target.ID, dependency.ID))
	if err != nil {
		t.Fatal(err)
	}
	cs.applyBeadEventToStores(beadSnapshotEvent(t, events.BeadCreated, wait))
	cr.stopSessionWaitDependencyProducer()
	if _, ok := cr.sessionWaitDependencyTarget(wait.ID); !ok {
		t.Fatal("read-only wait-commit retired the still-open durable wait target")
	}
	if got := env.sp.CountCalls("IsRunning", "worker"); got != 1 {
		t.Fatalf("runtime observations after wait-commit = %d, want 1", got)
	}
	storedWait, err := env.store.Get(wait.ID)
	if err != nil {
		t.Fatal(err)
	}
	if storedWait.Status != "open" || storedWait.Metadata["state"] != waitStatePending {
		t.Fatalf("durable wait = status %q state %q, want open pending", storedWait.Status, storedWait.Metadata["state"])
	}
	if !cr.startSessionWaitDependencyProducer() {
		t.Fatal("restart wait dependency producer for dependency-commit")
	}

	closed, err := env.store.Get(dependency.ID)
	if err != nil {
		t.Fatal(err)
	}
	cs.applyBeadEventToStores(beadSnapshotEvent(t, events.BeadClosed, closed))
	cr.stopSessionWaitDependencyProducer()
	if _, ok := cr.sessionWaitDependencyTarget(wait.ID); !ok {
		t.Fatal("dependency-commit retired the still-open durable wait target")
	}
	if got := env.sp.CountCalls("IsRunning", "worker"); got != 2 {
		t.Fatalf("runtime observations after dependency-commit = %d, want 2", got)
	}

	if err := env.store.Close(wait.ID); err != nil {
		t.Fatal(err)
	}
	closedWait, err := env.store.Get(wait.ID)
	if err != nil {
		t.Fatal(err)
	}
	cs.applyBeadEventToStores(beadSnapshotEvent(t, events.BeadClosed, closedWait))
	if _, ok := cr.sessionWaitDependencyTarget(wait.ID); ok {
		t.Fatal("durable wait close did not remove the wait target from the private index")
	}

	if err := tracer.Close(); err != nil {
		t.Fatalf("close trace: %v", err)
	}
	records, err := ReadTraceRecords(traceCityRuntimeDir(cityPath), TraceFilter{})
	if err != nil {
		t.Fatalf("read trace: %v", err)
	}
	causes := make([]string, 0, 2)
	for _, record := range records {
		if record.RecordType == TraceRecordOperation && record.Fields["operation_name"] == "wait_dependency_shadow" && record.Fields["wait_id"] == wait.ID {
			causes = append(causes, record.Fields["cause"].(string))
		}
	}
	if !slices.Contains(causes, string(sessionWaitDependencyCauseWaitCommit)) ||
		!slices.Contains(causes, string(sessionWaitDependencyCauseDependency)) {
		t.Fatalf("shadow evaluation causes = %v, want wait_commit and dependency_commit", causes)
	}
}

func testSessionWaitDependencyEventUsesInstalledLifecycleShadowSinkForExactTarget(t *testing.T, mode rollout.Mode) {
	t.Helper()
	env := newReconcilerTestEnv()
	env.cfg = &config.City{
		Workspace: config.Workspace{Name: "test-city"},
		Agents:    []config.Agent{{Name: "worker", StartCommand: "true"}},
	}
	dependency, err := env.store.Create(beads.Bead{})
	if err != nil {
		t.Fatal(err)
	}
	a := env.createSessionBead("worker", "worker")
	env.setSessionMetadata(&a, map[string]string{
		"state":                     string(sessionpkg.StateCreating),
		"pending_create_claim":      "true",
		"pending_create_started_at": env.clk.Now().UTC().Format(time.RFC3339),
	})
	unrelated := env.createSessionBead("unrelated", "worker")
	wait, err := env.store.Create(sessionWaitShadowBead(a.ID, dependency.ID))
	if err != nil {
		t.Fatal(err)
	}
	readA := make(chan struct{}, 1)
	recording := beadstest.NewRecordingStore(env.store)
	var unrelatedGets atomic.Int64
	audited := &sessionWaitShadowReadAuditStore{Store: recording, onGet: func(id string) {
		if id == a.ID {
			select {
			case readA <- struct{}{}:
			default:
			}
		}
		if id == unrelated.ID {
			unrelatedGets.Add(1)
		}
	}}
	cache := beads.NewCachingStoreForTest(audited, nil)
	if err := cache.PrimeActive(); err != nil {
		t.Fatal(err)
	}
	cityPath := t.TempDir()
	tracer := newSessionReconcilerTraceManager(cityPath, "test-city", io.Discard)
	traceClosed := false
	t.Cleanup(func() {
		if !traceClosed {
			_ = tracer.Close()
		}
	})
	cs := &controllerState{
		cfg:                         env.cfg,
		sp:                          env.sp,
		cityPath:                    cityPath,
		cityBeadStore:               cache,
		eventProv:                   events.NewFake(),
		pokeCh:                      make(chan struct{}, 2),
		rolloutFlags:                rollout.ForTest(rollout.WithSessionReconciler(mode)),
		sessionStartGeneration:      1,
		sessionStartStoreGeneration: 1,
	}
	var stderr bytes.Buffer
	cr := &CityRuntime{
		cs:                    cs,
		cfg:                   env.cfg,
		stderr:                &stderr,
		sessionStartOwnership: sessionStartOwnershipKeyed,
		trace:                 tracer,
	}
	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("wait dependency stderr: %s", stderr.String())
		}
	})
	cr.startSessionWaitDependencyShadowWithContext(t.Context())
	if cr.waitDependencyEnqueue == nil {
		t.Fatal("production lifecycle shadow sink was not installed")
	}
	t.Cleanup(func() { cs.stopSessionWaitDependencyShadowAdmission(); cr.stopSessionWaitDependencyProducer() })
	// Join the initial pending census before closing the dependency so this
	// assertion observes the exact dependency event, not a startup evaluation
	// that races and sees the newly closed row.
	cr.stopSessionWaitDependencyProducer()
	if !cr.startSessionWaitDependencyProducer() {
		t.Fatal("restart wait dependency producer")
	}
	baselineLists := audited.listCalls.Load()
	recording.Reset()

	if err := env.store.Close(dependency.ID); err != nil {
		t.Fatal(err)
	}
	closed, err := env.store.Get(dependency.ID)
	if err != nil {
		t.Fatal(err)
	}
	event := beadSnapshotEvent(t, events.BeadClosed, closed)
	cache.ApplyEvent(event.Type, event.Payload)
	cs.admitSessionWaitDependencyShadowEvent(event)
	awaitClose(t, readA, "installed dependency sink exact target read")
	cr.stopSessionWaitDependencyProducer()
	if _, ok := cr.sessionWaitDependencyTarget(wait.ID); !ok {
		t.Fatal("read-only dependency evaluation retired the still-open durable wait target")
	}
	if got := env.sp.CountCalls("IsRunning", "worker"); got != 1 {
		t.Fatalf("target runtime observations = %d, want 1", got)
	}
	if got := env.sp.CountCalls("IsRunning", "unrelated"); got != 0 {
		t.Fatalf("unrelated runtime observations = %d, want 0", got)
	}
	if got := unrelatedGets.Load(); got != 0 {
		t.Fatalf("unrelated authoritative Gets = %d, want 0", got)
	}
	if got := audited.listCalls.Load(); got != baselineLists {
		t.Fatalf("post-baseline List calls = %d, want unchanged %d", got, baselineLists)
	}
	for _, call := range env.sp.SnapshotCalls() {
		switch call.Method {
		case "Start", "Stop", "Nudge":
			t.Fatalf("dependency shadow provider effect: %+v", call)
		}
	}
	if calls := recording.Calls(); len(calls) != 0 {
		t.Fatalf("dependency shadow store effects = %#v, want none", calls)
	}
	if got := audited.getCalls.Load(); got < 2 {
		t.Fatalf("exact event reads = %d, want dependency and target reads", got)
	}
	if err := tracer.Close(); err != nil {
		t.Fatalf("close trace: %v", err)
	}
	traceClosed = true
	records, err := ReadTraceRecords(traceCityRuntimeDir(cityPath), TraceFilter{})
	if err != nil {
		t.Fatalf("read trace: %v", err)
	}
	matching := 0
	for _, record := range records {
		if record.RecordType != TraceRecordOperation || record.OperationID == "" || record.Fields["operation_name"] != "wait_dependency_shadow" {
			continue
		}
		matching++
		if record.Fields["wait_outcome"] != string(sessionWaitDependencyEvaluationReady) ||
			record.Fields["start_outcome"] != "prepare" ||
			record.Fields["start_reason"] != string(sessionLifecycleStartSelectionReasonReady) ||
			record.Fields["cause"] != string(sessionWaitDependencyCauseDependency) ||
			record.Fields["wait_id"] != wait.ID || record.Fields["session_id"] != a.ID ||
			record.Fields["effect_applied"] != false || record.SiteCode != TraceSiteWaitDependencyShadow ||
			record.OutcomeCode != TraceOutcomeStartCandidate || record.DurationMS < 0 {
			t.Fatalf("shadow trace record = %+v", record)
		}
	}
	if matching != 1 {
		t.Fatalf("dependency-ready shadow trace operation count = %d, want 1", matching)
	}
}

// TestSessionWaitDependencyReadyStartsExactSleepingSessionThroughKeyedController
// pins the first production ownership transfer out of the dependency-wait
// shadow: one durable deps/all wait with one closed dependency must wake only
// its ordinary existing session through the keyed controller.  In particular,
// this must not wait for, or wake, the fleet reconciler.
func TestSessionWaitDependencyReadyStartsExactSleepingSessionThroughKeyedController(t *testing.T) {
	for _, mode := range []rollout.Mode{rollout.Auto, rollout.Require} {
		t.Run(string(mode), func(t *testing.T) {
			env := newReconcilerTestEnv()
			env.cfg = &config.City{
				Workspace: config.Workspace{Name: "test-city"},
				Agents:    []config.Agent{{Name: "worker", StartCommand: "true"}},
			}
			dependency, err := env.store.Create(beads.Bead{Title: "dependency"})
			if err != nil {
				t.Fatal(err)
			}
			target := env.createSessionBead("worker", "worker")
			env.setSessionMetadata(&target, map[string]string{
				"state":              string(sessionpkg.StateAsleep),
				"continuation_epoch": "7",
				"wait_hold":          "true",
				"sleep_intent":       string(sessionpkg.SleepReasonWaitHold),
				"sleep_reason":       string(sessionpkg.SleepReasonWaitHold),
			})
			unrelated := env.createSessionBead("unrelated", "worker")
			wait, err := env.store.Create(sessionWaitShadowBead(target.ID, dependency.ID))
			if err != nil {
				t.Fatal(err)
			}
			if err := env.store.SetMetadata(wait.ID, "registered_epoch", "7"); err != nil {
				t.Fatal(err)
			}

			var unrelatedGets atomic.Int64
			audited := &sessionWaitShadowReadAuditStore{Store: env.store, onGet: func(id string) {
				if id == unrelated.ID {
					unrelatedGets.Add(1)
				}
			}}
			pokeCh := make(chan struct{}, 2)
			cs := &controllerState{
				cfg:                         env.cfg,
				sp:                          env.sp,
				cityPath:                    t.TempDir(),
				cityBeadStore:               audited,
				eventProv:                   events.NewFake(),
				pokeCh:                      pokeCh,
				rolloutFlags:                rollout.ForTest(rollout.WithSessionReconciler(mode)),
				sessionStartGeneration:      1,
				sessionStartStoreGeneration: 1,
			}
			params := exactSessionStartTestParams(t, env)
			params.Store = audited
			params.Generation = 1
			params.RolloutMode = mode
			params.StatusWriter, _, params.StatusWriterError = beads.ResolveConditionalWriter(audited)
			results := make(chan sessionStartReconcileResult, 2)
			controller, err := newSessionStartController(sessionStartControllerOptions{
				Workers:     1,
				MaxDistinct: 8,
				MaxRetries:  0,
				Reconcile: func(ctx context.Context, admission sessionStartAdmission) error {
					return reconcileExactSessionStart(ctx, admission, params)
				},
				Observer: func(result sessionStartReconcileResult) { results <- result },
				Stderr:   io.Discard,
			})
			if err != nil {
				t.Fatalf("new keyed session-start controller: %v", err)
			}
			if err := controller.Start(t.Context()); err != nil {
				t.Fatalf("start keyed session-start controller: %v", err)
			}
			t.Cleanup(controller.Stop)
			cr := &CityRuntime{
				cs: cs, cfg: env.cfg, pokeCh: pokeCh, stderr: io.Discard,
				sessionStartOwnership: sessionStartOwnershipKeyed, sessionStartController: controller,
				sessionWaitDependencyStartCh: make(chan sessionWaitDependencyStartHint, 1),
			}
			cr.sessionWaitDependencyIndex = newSessionWaitDependencyIndex()
			if err := cr.sessionWaitDependencyIndex.Rebuild([]sessionpkg.WaitInfo{{ID: wait.ID, SessionID: target.ID, Kind: "deps", Status: "open", State: waitStatePending, DepIDs: []string{dependency.ID}, DepMode: "all"}}); err != nil {
				t.Fatal(err)
			}
			cr.sessionWaitDependencyIndexGeneration = 1
			cr.enableSessionWaitDependencyLifecycleShadowSink(t.Context())
			if cr.waitDependencyEnqueue == nil {
				t.Fatal("keyed dependency-ready production sink was not installed")
			}

			if err := env.store.Close(dependency.ID); err != nil {
				t.Fatal(err)
			}
			closed, err := env.store.Get(dependency.ID)
			if err != nil {
				t.Fatal(err)
			}
			if closed.Status != "closed" {
				t.Fatalf("dependency status = %q, want closed", closed.Status)
			}
			_, err = cr.waitDependencyEnqueue(sessionWaitDependencyTarget{
				WaitID: wait.ID, SessionID: target.ID, DepIDs: []string{dependency.ID}, DepMode: "all", generation: 1,
			}, sessionWaitDependencyCauseDependency)
			if err != nil {
				t.Fatalf("enqueue closed dependency target: %v", err)
			}
			cr.handleSessionWaitDependencyStart(t.Context(), <-cr.sessionWaitDependencyStartCh)
			select {
			case result := <-results:
				if result.Err != nil || result.Outcome != sessionStartReconcileSucceeded {
					t.Fatalf("keyed dependency start = outcome=%s err=%v", result.Outcome, result.Err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("timed out waiting for keyed dependency start")
			}

			if got := env.sp.CountCalls("Start", "worker"); got != 1 {
				t.Fatalf("target provider Starts = %d, want 1", got)
			}
			if got := env.sp.CountCalls("Start", "unrelated"); got != 0 {
				t.Fatalf("unrelated provider Starts = %d, want 0", got)
			}
			if got := unrelatedGets.Load(); got != 0 {
				t.Fatalf("unrelated authoritative Gets = %d, want 0", got)
			}
			if got := audited.listCalls.Load(); got != 0 {
				t.Fatalf("fleet List calls = %d, want 0", got)
			}
			if cr.sessionWaitDependencyReadyPokePending.Load() {
				t.Fatal("keyed dependency start left a legacy priority poke pending")
			}
			select {
			case <-pokeCh:
				t.Fatal("keyed dependency start poked legacy reconciliation")
			default:
			}
			storedWait, err := env.store.Get(wait.ID)
			if err != nil {
				t.Fatal(err)
			}
			if storedWait.Status != "open" || storedWait.Metadata["state"] != waitStateReady || storedWait.Metadata["ready_at"] == "" {
				t.Fatalf("durable wait after keyed start = status=%q metadata=%v, want open ready with ready_at", storedWait.Status, storedWait.Metadata)
			}
		})
	}
}

// TestSessionWaitDependencyRevokedBeforePreWakeYieldsOrParks exercises the
// durable race which the controller must close at its effect boundary.  The
// wait is canceled only after its first authoritative validation read; a
// later pre-wake revalidation must therefore stop the keyed start.  Auto
// yields once to the legacy owner, while Require parks without a poke.
func TestSessionWaitDependencyRevokedBeforePreWakeYieldsOrParks(t *testing.T) {
	for _, mode := range []rollout.Mode{rollout.Auto, rollout.Require} {
		t.Run(string(mode), func(t *testing.T) {
			env := newReconcilerTestEnv()
			env.cfg = &config.City{
				Workspace: config.Workspace{Name: "test-city"},
				Agents:    []config.Agent{{Name: "worker", StartCommand: "true"}},
			}
			dependency, err := env.store.Create(beads.Bead{Title: "dependency"})
			if err != nil {
				t.Fatal(err)
			}
			if err := env.store.Close(dependency.ID); err != nil {
				t.Fatal(err)
			}
			target := env.createSessionBead("worker", "worker")
			env.setSessionMetadata(&target, map[string]string{
				"state":              string(sessionpkg.StateAsleep),
				"continuation_epoch": "7",
				"wait_hold":          "true",
				"sleep_intent":       string(sessionpkg.SleepReasonWaitHold),
				"sleep_reason":       string(sessionpkg.SleepReasonWaitHold),
			})
			wait, err := env.store.Create(sessionWaitShadowBead(target.ID, dependency.ID))
			if err != nil {
				t.Fatal(err)
			}
			if err := env.store.SetMetadata(wait.ID, "registered_epoch", "7"); err != nil {
				t.Fatal(err)
			}

			var dependencyReads atomic.Int64
			audited := &sessionWaitShadowReadAuditStore{Store: env.store, onGet: func(id string) {
				if id != dependency.ID || dependencyReads.Add(1) != 1 {
					return
				}
				if err := env.store.SetMetadata(wait.ID, "state", waitStateCanceled); err != nil {
					t.Errorf("cancel wait after certification: %v", err)
				}
			}}
			pokeCh := make(chan struct{}, 2)
			cs := &controllerState{
				cfg:                         env.cfg,
				sp:                          env.sp,
				cityPath:                    t.TempDir(),
				cityBeadStore:               audited,
				eventProv:                   events.NewFake(),
				pokeCh:                      pokeCh,
				rolloutFlags:                rollout.ForTest(rollout.WithSessionReconciler(mode)),
				sessionStartGeneration:      1,
				sessionStartStoreGeneration: 1,
			}
			cr := &CityRuntime{cs: cs, cfg: env.cfg, pokeCh: pokeCh, stderr: io.Discard}
			if err := cr.ensureSessionStartController(t.Context(), newSessionBeadSnapshotFromInfos(nil)); err != nil {
				t.Fatalf("ensure keyed session-start controller: %v", err)
			}
			t.Cleanup(cr.stopSessionStartController)
			cr.sessionWaitDependencyStartCh = make(chan sessionWaitDependencyStartHint, 1)
			cr.sessionWaitDependencyIndex = newSessionWaitDependencyIndex()
			if err := cr.sessionWaitDependencyIndex.Rebuild([]sessionpkg.WaitInfo{{ID: wait.ID, SessionID: target.ID, Kind: "deps", Status: "open", State: waitStatePending, DepIDs: []string{dependency.ID}, DepMode: "all"}}); err != nil {
				t.Fatal(err)
			}
			cr.sessionWaitDependencyIndexGeneration = 1
			cr.enableSessionWaitDependencyLifecycleShadowSink(t.Context())
			if cr.waitDependencyEnqueue == nil {
				t.Fatal("keyed dependency-ready production sink was not installed")
			}

			_, err = cr.waitDependencyEnqueue(sessionWaitDependencyTarget{
				WaitID: wait.ID, SessionID: target.ID, DepIDs: []string{dependency.ID}, DepMode: "all", generation: 1,
			}, sessionWaitDependencyCauseDependency)
			if err != nil {
				t.Fatalf("enqueue certified wait target: %v", err)
			}
			cr.handleSessionWaitDependencyStart(t.Context(), <-cr.sessionWaitDependencyStartCh)
			if got := dependencyReads.Load(); got != 1 {
				t.Fatalf("dependency reads = %d, want initial certification read", got)
			}
			if got := env.sp.CountCalls("Start", "worker"); got != 0 {
				t.Fatalf("provider Starts after revoked certification = %d, want 0", got)
			}
			storedWait, err := env.store.Get(wait.ID)
			if err != nil {
				t.Fatal(err)
			}
			if got := storedWait.Metadata["state"]; got != waitStateCanceled {
				t.Fatalf("revoked wait state = %q, want %q", got, waitStateCanceled)
			}

			wantPokes := 1
			if mode == rollout.Require {
				wantPokes = 0
			}
			gotPokes := 0
			for {
				select {
				case <-pokeCh:
					gotPokes++
				default:
					goto drained
				}
			}
		drained:
			if gotPokes != wantPokes {
				t.Fatalf("legacy pokes after revoked pre-wake certification = %d, want %d for %s", gotPokes, wantPokes, mode)
			}
			if mode == rollout.Require && cr.sessionWaitDependencyReadyPokePending.Load() {
				t.Fatal("require mode retained a legacy dependency-ready poke")
			}
		})
	}
}

func TestSessionWaitDependencyShadowDoesNotObserveCanceledWaitAfterIndexCertification(t *testing.T) {
	env := newReconcilerTestEnv()
	env.cfg = &config.City{
		Workspace: config.Workspace{Name: "test-city"},
		Agents:    []config.Agent{{Name: "worker", StartCommand: "true"}},
	}
	dependency, err := env.store.Create(beads.Bead{})
	if err != nil {
		t.Fatal(err)
	}
	if err := env.store.Close(dependency.ID); err != nil {
		t.Fatal(err)
	}
	target := env.createSessionBead("worker", "worker")
	env.setSessionMetadata(&target, map[string]string{
		"state":                     string(sessionpkg.StateCreating),
		"pending_create_claim":      "true",
		"pending_create_started_at": env.clk.Now().UTC().Format(time.RFC3339),
	})
	wait, err := env.store.Create(sessionWaitShadowBead(target.ID, dependency.ID))
	if err != nil {
		t.Fatal(err)
	}
	targetRead := make(chan struct{}, 1)
	audited := &sessionWaitShadowReadAuditStore{Store: env.store, onGet: func(id string) {
		if id == target.ID {
			select {
			case targetRead <- struct{}{}:
			default:
			}
		}
	}}
	cache := beads.NewCachingStoreForTest(audited, nil)
	if err := cache.PrimeActive(); err != nil {
		t.Fatal(err)
	}
	cs := &controllerState{
		cfg:                         env.cfg,
		sp:                          env.sp,
		cityPath:                    t.TempDir(),
		cityBeadStore:               cache,
		rolloutFlags:                rollout.ForTest(rollout.WithSessionReconciler(rollout.Auto)),
		sessionStartGeneration:      1,
		sessionStartStoreGeneration: 1,
	}
	cr := &CityRuntime{cs: cs, cfg: env.cfg, stderr: io.Discard, sessionStartOwnership: sessionStartOwnershipKeyed}
	if installed, err := cr.installObservedSessionWaitDependencyIndex(beads.SessionStore{Store: cache}); err != nil || !installed {
		t.Fatalf("install wait index = %v, %v", installed, err)
	}
	cr.enableSessionWaitDependencyLifecycleShadowSink(t.Context())
	if cr.waitDependencyEnqueue == nil {
		t.Fatal("production lifecycle shadow sink was not installed")
	}
	if !cr.startSessionWaitDependencyProducer() {
		t.Fatal("start wait dependency producer")
	}
	t.Cleanup(cr.stopSessionWaitDependencyProducer)

	exact, ok := cr.sessionWaitDependencyTarget(wait.ID)
	if !ok {
		t.Fatal("missing certified wait target")
	}
	if err := env.store.SetMetadata(wait.ID, "state", waitStateCanceled); err != nil {
		t.Fatalf("cancel wait after certification: %v", err)
	}
	cr.submitSessionWaitDependencyTargets([]sessionWaitDependencyTarget{exact}, sessionWaitDependencyCauseDependency)
	cr.stopSessionWaitDependencyProducer()

	select {
	case <-targetRead:
		t.Fatal("canceled wait reached runtime observation")
	default:
	}
	if got := env.sp.CountCalls("IsRunning", "worker"); got != 0 {
		t.Fatalf("target runtime observations = %d, want 0", got)
	}
}

func TestSessionWaitDependencyShutdownDrainsWithoutLiveValidation(t *testing.T) {
	env := newReconcilerTestEnv()
	env.cfg = &config.City{
		Workspace: config.Workspace{Name: "test-city"},
		Agents:    []config.Agent{{Name: "worker", StartCommand: "true"}},
	}
	dependency, err := env.store.Create(beads.Bead{})
	if err != nil {
		t.Fatal(err)
	}
	target := env.createSessionBead("worker", "worker")
	wait, err := env.store.Create(sessionWaitShadowBead(target.ID, dependency.ID))
	if err != nil {
		t.Fatal(err)
	}
	var validationReads atomic.Int64
	audited := &sessionWaitShadowReadAuditStore{Store: env.store, onGet: func(id string) {
		switch id {
		case wait.ID, target.ID:
			validationReads.Add(1)
		}
	}}
	cache := beads.NewCachingStoreForTest(audited, nil)
	if err := cache.PrimeActive(); err != nil {
		t.Fatal(err)
	}
	cs := &controllerState{
		cfg: env.cfg, sp: env.sp, cityPath: t.TempDir(), cityBeadStore: cache,
		eventProv: events.NewFake(), pokeCh: make(chan struct{}, 2),
		rolloutFlags:           rollout.ForTest(rollout.WithSessionReconciler(rollout.Auto)),
		sessionStartGeneration: 1, sessionStartStoreGeneration: 1,
	}
	ctx, cancel := context.WithCancel(t.Context())
	cr := &CityRuntime{
		cs: cs, cfg: env.cfg, stdout: io.Discard, stderr: io.Discard,
		sessionStartOwnership: sessionStartOwnershipKeyed,
	}
	cr.preserveSessionsShutdown.Store(true)
	cr.startSessionWaitDependencyShadowWithContext(ctx)
	cr.stopSessionWaitDependencyProducer()
	if got := validationReads.Load(); got != 0 {
		t.Fatalf("startup live validation reads = %d, want 0 for pending dependency", got)
	}
	if !cr.startSessionWaitDependencyProducer() {
		t.Fatal("restart wait dependency producer")
	}

	cancel()
	if err := env.store.Close(dependency.ID); err != nil {
		t.Fatal(err)
	}
	closed, err := env.store.Get(dependency.ID)
	if err != nil {
		t.Fatal(err)
	}
	cs.applyBeadEventToStores(beadSnapshotEvent(t, events.BeadClosed, closed))
	cr.shutdown()

	if got := validationReads.Load(); got != 0 {
		t.Fatalf("live validation reads after cancellation = %d, want 0", got)
	}
	if cr.waitDependencyProducer != nil {
		t.Fatal("shutdown returned before wait dependency producer joined")
	}
	for _, call := range env.sp.SnapshotCalls() {
		switch call.Method {
		case "Start", "Stop", "Nudge":
			t.Fatalf("shutdown dependency shadow provider effect: %+v", call)
		}
	}
}

func TestValidateExactSessionWaitDependencyShadow(t *testing.T) {
	tests := []struct {
		name     string
		mutate   func(t *testing.T, env *reconcilerTestEnv, wait, session, dependency beads.Bead)
		want     sessionWaitDependencyEvaluation
		hardRead string
	}{
		{name: "ready", want: sessionWaitDependencyEvaluationReady},
		{
			name: "pending reopened dependency",
			mutate: func(t *testing.T, env *reconcilerTestEnv, _ beads.Bead, _ beads.Bead, dependency beads.Bead) {
				t.Helper()
				if err := env.store.Reopen(dependency.ID); err != nil {
					t.Fatal(err)
				}
			},
			want: sessionWaitDependencyEvaluationPending,
		},
		{
			name: "stale epoch",
			mutate: func(t *testing.T, env *reconcilerTestEnv, wait, session, _ beads.Bead) {
				t.Helper()
				if err := env.store.SetMetadata(wait.ID, "registered_epoch", "old"); err != nil {
					t.Fatal(err)
				}
				env.setSessionMetadata(&session, map[string]string{"continuation_epoch": "new"})
			},
			want: sessionWaitDependencyEvaluationStaleEpoch,
		},
		{
			name: "closed session",
			mutate: func(t *testing.T, env *reconcilerTestEnv, _ beads.Bead, session, _ beads.Bead) {
				t.Helper()
				if err := env.store.Close(session.ID); err != nil {
					t.Fatal(err)
				}
			},
			want: sessionWaitDependencyEvaluationClosedSession,
		},
		{
			name: "stale epoch precedes closed session",
			mutate: func(t *testing.T, env *reconcilerTestEnv, wait, session, _ beads.Bead) {
				t.Helper()
				if err := env.store.SetMetadata(wait.ID, "registered_epoch", "old"); err != nil {
					t.Fatal(err)
				}
				env.setSessionMetadata(&session, map[string]string{"continuation_epoch": "new"})
				if err := env.store.Close(session.ID); err != nil {
					t.Fatal(err)
				}
			},
			want: sessionWaitDependencyEvaluationStaleEpoch,
		},
		{
			name: "missing session",
			mutate: func(t *testing.T, env *reconcilerTestEnv, _ beads.Bead, session, _ beads.Bead) {
				t.Helper()
				if err := env.store.Delete(session.ID); err != nil {
					t.Fatal(err)
				}
			},
			want: sessionWaitDependencyEvaluationNoopTerminal,
		},
		{
			name: "expired",
			mutate: func(t *testing.T, env *reconcilerTestEnv, wait, _ beads.Bead, _ beads.Bead) {
				t.Helper()
				if err := env.store.SetMetadata(wait.ID, "expires_at", env.clk.Now().Add(-time.Second).UTC().Format(time.RFC3339)); err != nil {
					t.Fatal(err)
				}
			},
			want: sessionWaitDependencyEvaluationExpired,
		},
		{
			name: "terminal wait",
			mutate: func(t *testing.T, env *reconcilerTestEnv, wait, _ beads.Bead, _ beads.Bead) {
				t.Helper()
				if err := env.store.SetMetadata(wait.ID, "state", waitStateCanceled); err != nil {
					t.Fatal(err)
				}
			},
			want: sessionWaitDependencyEvaluationNoopTerminal,
		},
		{
			name: "removed wait",
			mutate: func(t *testing.T, env *reconcilerTestEnv, wait, _ beads.Bead, _ beads.Bead) {
				t.Helper()
				if err := env.store.Delete(wait.ID); err != nil {
					t.Fatal(err)
				}
			},
			want: sessionWaitDependencyEvaluationNoopTerminal,
		},
		{
			name: "rebound target",
			mutate: func(t *testing.T, env *reconcilerTestEnv, wait, _ beads.Bead, _ beads.Bead) {
				t.Helper()
				if err := env.store.SetMetadata(wait.ID, "session_id", "replacement-session"); err != nil {
					t.Fatal(err)
				}
			},
			want: sessionWaitDependencyEvaluationStaleTarget,
		},
		{
			name: "missing dependency",
			mutate: func(t *testing.T, env *reconcilerTestEnv, _ beads.Bead, _ beads.Bead, dependency beads.Bead) {
				t.Helper()
				if err := env.store.Delete(dependency.ID); err != nil {
					t.Fatal(err)
				}
			},
			want: sessionWaitDependencyEvaluationMissingDependency,
		},
		{name: "wait hard read error", want: sessionWaitDependencyEvaluationParkReadError, hardRead: "wait"},
		{name: "session hard read error", want: sessionWaitDependencyEvaluationParkReadError, hardRead: "session"},
		{name: "dependency hard read error", want: sessionWaitDependencyEvaluationParkReadError, hardRead: "dependency"},
		{
			name: "malformed expiry retains ready classification",
			mutate: func(t *testing.T, env *reconcilerTestEnv, wait, _ beads.Bead, _ beads.Bead) {
				t.Helper()
				if err := env.store.SetMetadata(wait.ID, "expires_at", "not-a-timestamp"); err != nil {
					t.Fatal(err)
				}
			},
			want: sessionWaitDependencyEvaluationReady,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			env := newReconcilerTestEnv()
			dependency, err := env.store.Create(beads.Bead{})
			if err != nil {
				t.Fatal(err)
			}
			if err := env.store.Close(dependency.ID); err != nil {
				t.Fatal(err)
			}
			session := env.createSessionBead("worker", "worker")
			wait, err := env.store.Create(sessionWaitShadowBead(session.ID, dependency.ID))
			if err != nil {
				t.Fatal(err)
			}
			if test.mutate != nil {
				test.mutate(t, env, wait, session, dependency)
			}
			hardErr := errors.New("authoritative read unavailable")
			store := env.store
			dependencies := waitDependencyReader(newWaitDependencyStoreSet(env.store, nil))
			switch test.hardRead {
			case "wait":
				store = &sessionWaitShadowReadAuditStore{Store: env.store, failID: wait.ID, failErr: hardErr}
			case "session":
				store = &sessionWaitShadowReadAuditStore{Store: env.store, failID: session.ID, failErr: hardErr}
			case "dependency":
				dependencies = waitDependencyReaderFunc(func(string) (beads.Bead, error) {
					return beads.Bead{}, hardErr
				})
			}
			got, err := validateExactSessionWaitDependencyShadow(
				store,
				sessionWaitDependencyTarget{WaitID: wait.ID, SessionID: session.ID, DepIDs: []string{dependency.ID}, DepMode: "all"},
				dependencies,
				env.clk.Now(),
			)
			if test.hardRead != "" {
				if err == nil || !errors.Is(err, errSessionWaitDependencyTargetReadUnavailable) || !errors.Is(err, hardErr) {
					t.Fatalf("hard read error = %v, want target-read sentinel wrapping %v", err, hardErr)
				}
			} else if err != nil {
				t.Fatalf("validate: %v", err)
			}
			if got != test.want {
				t.Fatalf("outcome = %q, want %q (err=%v)", got, test.want, err)
			}
		})
	}
}

func TestSessionWaitDependencyReadyPokesLegacyReconcileOnly(t *testing.T) {
	for _, test := range []struct {
		name        string
		mutate      func(t *testing.T, env *reconcilerTestEnv, wait, dependency beads.Bead)
		wantPoke    bool
		invocations int
		readError   bool
	}{
		{name: "ready", wantPoke: true, invocations: 2},
		{
			name: "pending",
			mutate: func(t *testing.T, env *reconcilerTestEnv, _ beads.Bead, dependency beads.Bead) {
				t.Helper()
				if err := env.store.Reopen(dependency.ID); err != nil {
					t.Fatal(err)
				}
			},
			invocations: 1,
		},
		{name: "read error", invocations: 1, readError: true},
		{
			name: "stale target",
			mutate: func(t *testing.T, env *reconcilerTestEnv, wait, _ beads.Bead) {
				t.Helper()
				if err := env.store.SetMetadata(wait.ID, "session_id", "replacement-session"); err != nil {
					t.Fatal(err)
				}
			},
			invocations: 1,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			env := newReconcilerTestEnv()
			env.cfg = &config.City{Workspace: config.Workspace{Name: "test-city"}, Agents: []config.Agent{{Name: "worker", StartCommand: "true"}}}
			dependency, err := env.store.Create(beads.Bead{})
			if err != nil {
				t.Fatal(err)
			}
			if err := env.store.Close(dependency.ID); err != nil {
				t.Fatal(err)
			}
			target := env.createSessionBead("worker", "worker")
			wait, err := env.store.Create(sessionWaitShadowBead(target.ID, dependency.ID))
			if err != nil {
				t.Fatal(err)
			}
			if test.mutate != nil {
				test.mutate(t, env, wait, dependency)
			}
			pokeCh := make(chan struct{}, 1)
			store := env.store
			if test.readError {
				store = &sessionWaitShadowReadAuditStore{Store: env.store, failID: target.ID, failErr: errors.New("target unavailable")}
			}
			cs := &controllerState{
				cfg: env.cfg, sp: env.sp, cityPath: t.TempDir(), cityBeadStore: store,
				eventProv: events.NewFake(), pokeCh: pokeCh,
				rolloutFlags:           rollout.ForTest(rollout.WithSessionReconciler(rollout.Auto)),
				sessionStartGeneration: 1, sessionStartStoreGeneration: 1,
			}
			cr := &CityRuntime{cs: cs, cfg: env.cfg, pokeCh: pokeCh, stderr: io.Discard, sessionStartOwnership: sessionStartOwnershipKeyed}
			cr.enableSessionWaitDependencyLifecycleShadowSink(t.Context())
			if cr.waitDependencyEnqueue == nil {
				t.Fatal("active keyed shadow did not install dependency sink")
			}
			targetRef := sessionWaitDependencyTarget{WaitID: wait.ID, SessionID: target.ID, DepIDs: []string{dependency.ID}, DepMode: "all"}
			for range test.invocations {
				_, err := cr.waitDependencyEnqueue(targetRef, sessionWaitDependencyCauseDependency)
				if test.readError {
					if !errors.Is(err, errSessionWaitDependencyTargetReadUnavailable) {
						t.Fatalf("enqueue read error = %v, want target-read sentinel", err)
					}
					continue
				}
				if err != nil {
					t.Fatalf("enqueue dependency target: %v", err)
				}
			}
			select {
			case <-pokeCh:
				if !test.wantPoke {
					t.Fatal("non-ready dependency target poked legacy reconciliation")
				}
			default:
				if test.wantPoke {
					t.Fatal("ready dependency target did not poke legacy reconciliation")
				}
			}
			select {
			case <-pokeCh:
				t.Fatal("ready dependency targets did not coalesce on the bounded poke channel")
			default:
			}
			if got := cr.sessionWaitDependencyReadyPokePending.Load(); got != test.wantPoke {
				t.Fatalf("priority poke pending = %t, want %t", got, test.wantPoke)
			}
		})
	}
}

func TestSessionWaitDependencyCanceledReadyValidationDoesNotRequestPriorityPoke(t *testing.T) {
	env := newReconcilerTestEnv()
	env.cfg = &config.City{Workspace: config.Workspace{Name: "test-city"}, Agents: []config.Agent{{Name: "worker", StartCommand: "true"}}}
	dependency, err := env.store.Create(beads.Bead{})
	if err != nil {
		t.Fatal(err)
	}
	if err := env.store.Close(dependency.ID); err != nil {
		t.Fatal(err)
	}
	target := env.createSessionBead("worker", "worker")
	wait, err := env.store.Create(sessionWaitShadowBead(target.ID, dependency.ID))
	if err != nil {
		t.Fatal(err)
	}
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	store := &sessionWaitShadowReadAuditStore{Store: env.store, blockID: target.ID, entered: entered, release: release}
	pokeCh := make(chan struct{}, 1)
	cs := &controllerState{
		cfg: env.cfg, sp: env.sp, cityPath: t.TempDir(), cityBeadStore: store,
		eventProv: events.NewFake(), pokeCh: pokeCh,
		rolloutFlags:           rollout.ForTest(rollout.WithSessionReconciler(rollout.Auto)),
		sessionStartGeneration: 1, sessionStartStoreGeneration: 1,
	}
	cr := &CityRuntime{cs: cs, cfg: env.cfg, pokeCh: pokeCh, stderr: io.Discard, sessionStartOwnership: sessionStartOwnershipKeyed}
	ctx, cancel := context.WithCancel(t.Context())
	cr.enableSessionWaitDependencyLifecycleShadowSink(ctx)
	done := make(chan error, 1)
	go func() {
		_, err := cr.waitDependencyEnqueue(sessionWaitDependencyTarget{WaitID: wait.ID, SessionID: target.ID, DepIDs: []string{dependency.ID}, DepMode: "all"}, sessionWaitDependencyCauseDependency)
		done <- err
	}()
	awaitClose(t, entered, "blocked exact dependency target read")
	cancel()
	close(release)
	if err := <-done; err != nil {
		t.Fatalf("canceled ready validation: %v", err)
	}
	if cr.sessionWaitDependencyReadyPokePending.Load() {
		t.Fatal("canceled ready validation retained priority poke")
	}
	select {
	case <-pokeCh:
		t.Fatal("canceled ready validation poked reconciliation")
	default:
	}
}

func TestSessionWaitDependencyTraceStartOutcomeLiterals(t *testing.T) {
	tests := []struct {
		outcome sessionLifecycleStartSelectionOutcome
		want    string
	}{
		{sessionLifecycleStartSelectionUnknown, ""},
		{sessionLifecycleStartSelectionNoop, "noop"},
		{sessionLifecycleStartSelectionPrepare, "prepare"},
		{sessionLifecycleStartSelectionPark, "park"},
	}
	for _, test := range tests {
		if got := sessionLifecycleStartSelectionTraceOutcome(test.outcome); got != test.want {
			t.Fatalf("trace outcome for %d = %q, want %q", test.outcome, got, test.want)
		}
	}
}

func TestAuthoritativeWaitDependencyStoreSetRoutesByPrefixAtConstantCost(t *testing.T) {
	run := func(rigCount int) int64 {
		t.Helper()
		gets := &atomic.Int64{}
		rigs := make(map[string]beads.Store, rigCount)
		for n := 0; n < rigCount-1; n++ {
			prefix := fmt.Sprintf("r%04d", n)
			rigs[prefix] = sessionWaitDependencyPrefixAuditStore{
				Store: beads.NewMemStore(), prefix: prefix, gets: gets,
			}
		}
		owner := beads.NewMemStoreFrom(0, []beads.Bead{{
			ID: "zzzz-dependency", Status: "closed",
		}}, nil)
		rigs["zzzz"] = sessionWaitDependencyPrefixAuditStore{
			Store: owner, prefix: "zzzz", gets: gets,
		}
		stores := newAuthoritativeWaitDependencyStoreSet(nil, rigs)
		got, err := stores.Get("zzzz-dependency")
		if err != nil || got.ID != "zzzz-dependency" {
			t.Fatalf("authoritative owner read = (%q, %v)", got.ID, err)
		}
		return gets.Load()
	}

	if one, many := run(1), run(1_000); one != 1 || many != one {
		t.Fatalf("owner-routed Get count = one:%d many:%d, want 1/1", one, many)
	}
}

func TestSessionWaitDependencyShadowExactReadAndProbeCostDoesNotGrowWithFleet(t *testing.T) {
	run := func(unrelated int) (int, int) {
		t.Helper()
		env := newReconcilerTestEnv()
		env.cfg = &config.City{Agents: []config.Agent{{Name: "worker", StartCommand: "true"}}}
		dependency, err := env.store.Create(beads.Bead{})
		if err != nil {
			t.Fatal(err)
		}
		if err := env.store.Close(dependency.ID); err != nil {
			t.Fatal(err)
		}
		target := env.createSessionBead("worker", "worker")
		env.setSessionMetadata(&target, map[string]string{
			"state": string(sessionpkg.StateCreating), "pending_create_claim": "true",
			"pending_create_started_at": env.clk.Now().UTC().Format(time.RFC3339),
		})
		wait, err := env.store.Create(sessionWaitShadowBead(target.ID, dependency.ID))
		if err != nil {
			t.Fatal(err)
		}
		for n := 0; n < unrelated; n++ {
			env.createSessionBead(fmt.Sprintf("unrelated-%d", n), "worker")
		}
		store := &getCountingStore{Store: env.store}
		targetRef := sessionWaitDependencyTarget{WaitID: wait.ID, SessionID: target.ID, DepIDs: []string{dependency.ID}, DepMode: "all"}
		outcome, err := validateExactSessionWaitDependencyShadow(store, targetRef, newWaitDependencyStoreSet(store, nil), env.clk.Now())
		if err != nil || outcome != sessionWaitDependencyEvaluationReady {
			t.Fatalf("validate = (%q, %v), want ready", outcome, err)
		}
		params := exactSessionStartTestParams(t, env)
		params.Store = store
		plan, err := planExactSessionWaitDependencyStartShadow(t.Context(), target.ID, params)
		if err != nil || plan.Outcome != sessionLifecycleStartSelectionPrepare {
			t.Fatalf("plan = (%+v, %v), want prepare", plan, err)
		}
		probes := 0
		for _, call := range env.sp.SnapshotCalls() {
			if call.Method == "IsRunning" {
				probes++
			}
		}
		return store.count(), probes
	}

	readsOne, probesOne := run(1)
	readsFleet, probesFleet := run(10_000)
	if readsOne != readsFleet || probesOne != probesFleet {
		t.Fatalf("one/fleet cost = (%d reads, %d probes)/(%d reads, %d probes), want identical", readsOne, probesOne, readsFleet, probesFleet)
	}
	if readsFleet != 4 || probesFleet != 1 {
		t.Fatalf("fleet cost = (%d reads, %d probes), want (4, 1)", readsFleet, probesFleet)
	}
}

func TestSessionWaitDependencySnapshotSwapRearmsCensusUntilExactTargetEvaluates(t *testing.T) {
	env := newReconcilerTestEnv()
	env.cfg = &config.City{Workspace: config.Workspace{Name: "test-city"}, Agents: []config.Agent{{Name: "worker", StartCommand: "true"}}}
	dependency, err := env.store.Create(beads.Bead{})
	if err != nil {
		t.Fatal(err)
	}
	target := env.createSessionBead("worker", "worker")
	env.setSessionMetadata(&target, map[string]string{
		"state": string(sessionpkg.StateCreating), "pending_create_claim": "true",
		"pending_create_started_at": env.clk.Now().UTC().Format(time.RFC3339),
	})
	wait, err := env.store.Create(sessionWaitShadowBead(target.ID, dependency.ID))
	if err != nil {
		t.Fatal(err)
	}
	targetRead := make(chan struct{}, 1)
	audited := &sessionWaitShadowReadAuditStore{Store: env.store, onGet: func(id string) {
		if id == target.ID {
			select {
			case targetRead <- struct{}{}:
			default:
			}
		}
	}}
	cache := beads.NewCachingStoreForTest(audited, nil)
	if err := cache.PrimeActive(); err != nil {
		t.Fatal(err)
	}
	cs := &controllerState{
		cfg: env.cfg, sp: env.sp, cityPath: t.TempDir(), cityBeadStore: cache,
		eventProv: events.NewFake(), pokeCh: make(chan struct{}, 1),
		rolloutFlags:           rollout.ForTest(rollout.WithSessionReconciler(rollout.Auto)),
		sessionStartGeneration: 1, sessionStartStoreGeneration: 1,
	}
	cr := &CityRuntime{cs: cs, cfg: env.cfg, stderr: io.Discard, sessionStartOwnership: sessionStartOwnershipKeyed}
	cr.startSessionWaitDependencyShadowWithContext(t.Context())
	t.Cleanup(func() { cs.stopSessionWaitDependencyShadowAdmission(); cr.stopSessionWaitDependencyProducer() })

	releaseSwap := cs.beginSessionStartGenerationSwap()
	if err := env.store.Close(dependency.ID); err != nil {
		t.Fatal(err)
	}
	closed, err := env.store.Get(dependency.ID)
	if err != nil {
		t.Fatal(err)
	}
	event := beadSnapshotEvent(t, events.BeadClosed, closed)
	cache.ApplyEvent(event.Type, event.Payload)
	exactTarget, ok := cr.sessionWaitDependencyTarget(wait.ID)
	if !ok {
		t.Fatal("installed dependency index did not retain exact wait target")
	}
	cr.submitSessionWaitDependencyTargets([]sessionWaitDependencyTarget{exactTarget}, sessionWaitDependencyCauseDependency)
	awaitClose(t, cs.pokeCh, "snapshot-unavailable recovery poke")
	cr.sessionWaitDependencyMu.RLock()
	owed := cr.sessionWaitDependencyStartupCensusOwed
	cr.sessionWaitDependencyMu.RUnlock()
	if !owed {
		t.Fatal("snapshot-unavailable sink did not retain startup census debt")
	}
	if got := env.sp.CountCalls("IsRunning", "worker"); got != 0 {
		t.Fatalf("target observed during swap = %d, want 0", got)
	}

	releaseSwap()
	cr.submitSessionWaitDependencyStartupCensus()
	awaitClose(t, targetRead, "exact target evaluation after swap recovery")
	cr.stopSessionWaitDependencyProducer()
	cr.sessionWaitDependencyMu.RLock()
	owed = cr.sessionWaitDependencyStartupCensusOwed
	cr.sessionWaitDependencyMu.RUnlock()
	if owed {
		t.Fatal("successful recovery retained startup census debt")
	}
	if got := env.sp.CountCalls("IsRunning", "worker"); got != 1 {
		t.Fatalf("target observations after recovery = %d, want 1", got)
	}
}

func TestSessionWaitDependencyTargetReadFailureRearmsWithoutPokeThenRecovers(t *testing.T) {
	env := newReconcilerTestEnv()
	env.cfg = &config.City{Workspace: config.Workspace{Name: "test-city"}, Agents: []config.Agent{{Name: "worker", StartCommand: "true"}}}
	dep, err := env.store.Create(beads.Bead{})
	if err != nil {
		t.Fatal(err)
	}
	target := env.createSessionBead("worker", "worker")
	env.setSessionMetadata(&target, map[string]string{"state": string(sessionpkg.StateCreating), "pending_create_claim": "true", "pending_create_started_at": env.clk.Now().UTC().Format(time.RFC3339)})
	wait, err := env.store.Create(sessionWaitShadowBead(target.ID, dep.ID))
	if err != nil {
		t.Fatal(err)
	}
	read := make(chan struct{}, 1)
	audited := &sessionWaitShadowReadAuditStore{Store: env.store, failID: target.ID, failErr: errors.New("target unavailable"), onGet: func(id string) {
		if id == target.ID {
			select {
			case read <- struct{}{}:
			default:
			}
		}
	}}
	cache := beads.NewCachingStoreForTest(audited, nil)
	if err := cache.PrimeActive(); err != nil {
		t.Fatal(err)
	}
	cs := &controllerState{cfg: env.cfg, sp: env.sp, cityPath: t.TempDir(), cityBeadStore: cache, eventProv: events.NewFake(), pokeCh: make(chan struct{}, 1), rolloutFlags: rollout.ForTest(rollout.WithSessionReconciler(rollout.Auto)), sessionStartGeneration: 1, sessionStartStoreGeneration: 1}
	var stderr bytes.Buffer
	cr := &CityRuntime{cs: cs, cfg: env.cfg, stderr: &stderr, sessionStartOwnership: sessionStartOwnershipKeyed}
	cr.startSessionWaitDependencyShadowWithContext(t.Context())
	t.Cleanup(func() { cs.stopSessionWaitDependencyShadowAdmission(); cr.stopSessionWaitDependencyProducer() })
	if err := env.store.Close(dep.ID); err != nil {
		t.Fatal(err)
	}
	closed, err := env.store.Get(dep.ID)
	if err != nil {
		t.Fatal(err)
	}
	event := beadSnapshotEvent(t, events.BeadClosed, closed)
	cache.ApplyEvent(event.Type, event.Payload)
	exact, ok := cr.sessionWaitDependencyTarget(wait.ID)
	if !ok {
		t.Fatal("missing exact wait target")
	}
	cr.submitSessionWaitDependencyTargets([]sessionWaitDependencyTarget{exact}, sessionWaitDependencyCauseDependency)
	awaitClose(t, read, "failed target read")
	cr.stopSessionWaitDependencyProducer()
	if stderr.Len() == 0 {
		t.Fatal("target read failure was not reported by the producer")
	}
	cr.sessionWaitDependencyMu.RLock()
	owed := cr.sessionWaitDependencyStartupCensusOwed
	cr.sessionWaitDependencyMu.RUnlock()
	if !owed {
		t.Fatal("target read failure did not rearm census debt")
	}
	select {
	case <-cs.pokeCh:
		t.Fatal("target read failure self-triggered poke")
	default:
	}
	audited.failErr = nil
	cr.startSessionWaitDependencyProducer()
	cr.submitSessionWaitDependencyStartupCensus()
	awaitClose(t, read, "recovered target evaluation")
	cr.stopSessionWaitDependencyProducer()
	if got := env.sp.CountCalls("IsRunning", "worker"); got != 1 {
		t.Fatalf("successful target observations = %d, want 1", got)
	}
	cr.sessionWaitDependencyMu.RLock()
	owed = cr.sessionWaitDependencyStartupCensusOwed
	cr.sessionWaitDependencyMu.RUnlock()
	if owed {
		t.Fatal("recovered target retained census debt")
	}
}

func TestSessionWaitDependencySinkFencesOwnershipTransitionAcrossExactRead(t *testing.T) {
	env := newReconcilerTestEnv()
	env.cfg = &config.City{Workspace: config.Workspace{Name: "test-city"}, Agents: []config.Agent{{Name: "worker", StartCommand: "true"}}}
	dependency, err := env.store.Create(beads.Bead{})
	if err != nil {
		t.Fatal(err)
	}
	if err := env.store.Close(dependency.ID); err != nil {
		t.Fatal(err)
	}
	target := env.createSessionBead("worker", "worker")
	env.setSessionMetadata(&target, map[string]string{
		"state": string(sessionpkg.StateCreating), "pending_create_claim": "true",
		"pending_create_started_at": env.clk.Now().UTC().Format(time.RFC3339),
	})
	wait, err := env.store.Create(sessionWaitShadowBead(target.ID, dependency.ID))
	if err != nil {
		t.Fatal(err)
	}
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	audited := &sessionWaitShadowReadAuditStore{Store: env.store, blockID: target.ID, entered: entered, release: release}
	cs := &controllerState{
		cfg: env.cfg, sp: env.sp, cityPath: t.TempDir(), cityBeadStore: audited,
		eventProv: events.NewFake(), rolloutFlags: rollout.ForTest(rollout.WithSessionReconciler(rollout.Auto)),
		sessionStartGeneration: 1, sessionStartStoreGeneration: 1,
	}
	cr := &CityRuntime{cs: cs, cfg: env.cfg, stderr: io.Discard, sessionStartOwnership: sessionStartOwnershipKeyed}
	cr.enableSessionWaitDependencyLifecycleShadowSink(t.Context())
	if cr.waitDependencyEnqueue == nil {
		t.Fatal("production lifecycle shadow sink was not installed")
	}

	sinkDone := make(chan struct{})
	go func() {
		defer close(sinkDone)
		if _, err := cr.waitDependencyEnqueue(sessionWaitDependencyTarget{WaitID: wait.ID, SessionID: target.ID, DepIDs: []string{dependency.ID}, DepMode: "all"}, sessionWaitDependencyCauseDependency); err != nil {
			t.Errorf("waitDependencyEnqueue: %v", err)
		}
	}()
	awaitClose(t, entered, "exact target read under ownership fence")
	if cr.sessionStartMu.TryLock() {
		cr.sessionStartMu.Unlock()
		t.Fatal("exact target read did not retain session-start ownership fence")
	}
	stopDone := make(chan struct{})
	go func() { cr.stopSessionStartController(); close(stopDone) }()
	select {
	case <-stopDone:
		t.Fatal("ownership transition completed while exact read remained fenced")
	default:
	}
	close(release)
	awaitClose(t, sinkDone, "fenced exact evaluation")
	awaitClose(t, stopDone, "ownership transition after exact evaluation")
	if got := cr.sessionStartOwnershipState(); got != sessionStartOwnershipLegacy {
		t.Fatalf("ownership = %v, want legacy", got)
	}
	if got := env.sp.CountCalls("Start", "worker"); got != 0 {
		t.Fatalf("provider Start calls = %d, want 0", got)
	}
	if got := env.sessionInfo(target.ID).MetadataState; got != string(sessionpkg.StateCreating) {
		t.Fatalf("persisted target state = %q, want creating", got)
	}
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
	cr := &CityRuntime{cs: cs, cfg: &config.City{}, logPrefix: "test", stderr: io.Discard, waitDependencyEnqueue: func(target sessionWaitDependencyTarget, _ sessionWaitDependencyCause) (bool, error) {
		admissions <- target.SessionID
		return false, nil
	}}
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
	cr := &CityRuntime{cs: cs, cfg: &config.City{}, stderr: io.Discard, waitDependencyEnqueue: func(target sessionWaitDependencyTarget, _ sessionWaitDependencyCause) (bool, error) {
		admissions <- target.SessionID
		return false, nil
	}}
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
		waitDependencyEnqueue: func(target sessionWaitDependencyTarget, _ sessionWaitDependencyCause) (bool, error) {
			admissions <- target.SessionID
			return false, nil
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
	cr := &CityRuntime{cs: cs, cfg: &config.City{}, stderr: io.Discard, waitDependencyEnqueue: func(target sessionWaitDependencyTarget, _ sessionWaitDependencyCause) (bool, error) {
		admissions <- target.SessionID
		return false, nil
	}}
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
	cr := &CityRuntime{cs: cs, cfg: &config.City{}, stderr: io.Discard, waitDependencyEnqueue: func(target sessionWaitDependencyTarget, _ sessionWaitDependencyCause) (bool, error) {
		sink <- target.SessionID
		return false, nil
	}}
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
