//go:build integration

// Package emitseed produces the dashboard e2e "run-emit" scenario EXCLUSIVELY by
// driving the production write pipeline — a real events.FileRecorder fed by a
// real beads.CachingStore over a beads.MemStore, wired with the exact onChange
// event mapping cmd/gc/api_state.go's wrapWithCachingStore uses in the
// supervisor. Nothing here hand-writes an event: every record in
// <cityPath>/.gc/events.jsonl is emitted by the CachingStore observer as a side
// effect of a real bead mutation, so a harness that serves this state proves the
// event-emission pipeline (PR #4397) actually populates the run view and home
// page.
//
// It is the emission-driven counterpart to package corpus (which replays a
// hand-authored events.jsonl). The build tag keeps it out of the production
// binary; it compiles only under -tags integration, mirroring
// api.ServeSeededCity.
//
// The scenario is ONE completed run:
//
//   - run-emit: a graph.v2 molecule root (gc.kind=workflow) created from a
//     closed source task, driven to a closed (completed) terminal state.
//   - run-emit.step-a and run-emit.step-b: two step beads whose Updates and
//     Closes flow through the CachingStore, so their bead.created / bead.updated
//     / bead.closed edges are real emissions.
//   - step-a additionally exercises the #4397 pending-publication machinery: a
//     one-shot backing refresh failure defers its bead.updated publication
//     behind an ordered gate; a later close resolves the gate and publishes the
//     recovered event with its ORIGINAL (backdated) occurredAt, producing the
//     non-monotonic Ts/seq pair that is the occurredAt-preservation proof.
//
// The closed source task and the active session are store-only state (seeded
// straight onto the MemStore, no run events), exactly as package corpus keeps
// its standalone beads out of the event log so they do not project as runs.
package emitseed

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/gastownhall/gascity/internal/beadmeta"
	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/config"
	"github.com/gastownhall/gascity/internal/events"
	"github.com/gastownhall/gascity/internal/session"
)

// Well-known ids/values the emission scenario seeds. The Playwright expected
// strings (e2e/fixtures/expected.ts) mirror these; keep them in lockstep.
const (
	// CityName is the served city; it is the {cityName} path segment on every
	// route the emission dashboard drives. Distinct from the corpus city so the
	// two Playwright projects never collide.
	CityName = "dashport-emit"

	// RigName is the one seeded rig the agents/rigs views project.
	RigName = "demo"

	// RunEmitID is the completed run root's bead id and workflow id. Both the
	// store /workflow/{id} read and the event-log run routes address it by this
	// id.
	RunEmitID = "run-emit"

	// RunEmitFormula is the run's formula name; the run-detail title and its
	// run-list label.
	RunEmitFormula = "mol-emit-v1"

	// StepAID is the first step: it carries the fail-once/recovered bead.updated
	// edge (the backdated-occurredAt proof). StepATitle is its rendered title.
	StepAID    = "run-emit.step-a"
	StepATitle = "step-a"

	// StepBID is the second step, driven normally. StepBTitle is its title.
	StepBID    = "run-emit.step-b"
	StepBTitle = "step-b"

	// SourceEmitID is the closed source task run-emit was created from
	// (gc.source_bead_id → this id). Store-only; it projects as a closed bead.
	SourceEmitID = "src-emit"

	// AgentName is the seeded pool agent AND the active session's alias /
	// session_name; it is the assignee stamped on the steps.
	AgentName = "emitter"

	// AgentTemplate is the session template ("<rig>/<agent>"); the agent-detail
	// AgentMetadata block parses the rig from it.
	AgentTemplate = RigName + "/" + AgentName

	// AgentState is the session runtime state; the home dial counts it as one
	// active session.
	AgentState = "active"

	// eventActor is the actor every CachingStore-emitted lifecycle event carries,
	// matching cmd/gc/api_state.go wrapWithCachingStore exactly.
	eventActor = "cache-reconcile"

	// disableFailOnceEnv, when set to a non-empty value, makes the fail-once
	// backing wrapper inert so NO refresh failure is injected. It is the STEP 4b
	// anti-vacuity switch: with it set, the recovered/backdated event never
	// publishes and the Layer A backdated-Ts assertion must fail.
	disableFailOnceEnv = "EMITSEED_DISABLE_FAILONCE"

	// disableRootCloseEnv, when set to a non-empty value, skips the run root's
	// close so the run never reaches a terminal state. It is the STEP 4a
	// anti-vacuity switch: with it set, the run-view completed assertions must
	// fail.
	disableRootCloseEnv = "EMITSEED_DISABLE_ROOT_CLOSE"
)

// failOnceGetStore wraps a MemStore and fails the FIRST backing Get for an armed
// bead id, then behaves as the underlying store forever after. It embeds the
// concrete *beads.MemStore so every optional store capability (transitioner,
// canonical getter, conditional writer) promotes unchanged — only Get is
// shadowed — so the CachingStore drives the same code paths it would over a real
// backing, save the single injected refresh fault.
//
// The CachingStore's post-commit refresh is readBeadWithDeps → backing.Get, so
// failing that one Get leaves the mutation durably committed but defers its
// observer publication into the pending-publication registry behind an ordered
// gate — the exact #4397 path a real transient backing read fault takes.
type failOnceGetStore struct {
	*beads.MemStore

	mu       sync.Mutex
	failID   string
	armed    bool
	fired    bool
	disabled bool
}

func (s *failOnceGetStore) arm(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.disabled {
		return
	}
	s.failID = id
	s.armed = true
}

func (s *failOnceGetStore) Get(id string) (beads.Bead, error) {
	s.mu.Lock()
	if s.armed && id == s.failID {
		s.armed = false
		s.fired = true
		s.mu.Unlock()
		return beads.Bead{}, fmt.Errorf("emitseed: injected one-shot refresh failure for %s", id)
	}
	s.mu.Unlock()
	return s.MemStore.Get(id)
}

func (s *failOnceGetStore) didFire() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.fired
}

// Result carries the live, emission-produced stores and providers a harness
// wires into api.ServeSeededCity. CityStore is the underlying MemStore (its final
// state is the real product of the pipeline mutations); EventProv is the live
// FileRecorder, which both wrote <cityPath>/.gc/events.jsonl and serves reads of
// it (so the backdated occurredAt survives verbatim — nothing re-stamps it).
type Result struct {
	CityName  string
	CityPath  string
	Config    *config.City
	CityStore beads.Store
	RigStores map[string]beads.Store
	EventProv events.Provider

	// FailOnceFired reports whether the injected one-shot refresh fault actually
	// tripped during seeding. A harness asserts it to keep the recovery path from
	// silently going untested (unless the fault was disabled for anti-vacuity).
	FailOnceFired bool

	closeRec func() error
}

// Close drains the event recorder. Safe on a nil Result and idempotent for a
// single deferred call.
func (r *Result) Close() error {
	if r == nil || r.closeRec == nil {
		return nil
	}
	return r.closeRec()
}

// SeedByEmission drives the production write pipeline to produce
// <cityPath>/.gc/events.jsonl and the final bead-store state for the run-emit
// scenario, then returns the live stores/providers a harness serves. Every event
// in the log is emitted by the real CachingStore observer; none is hand-written.
func SeedByEmission(cityPath string) (*Result, error) {
	logPath := filepath.Join(cityPath, ".gc", "events.jsonl")
	rec, err := events.NewFileRecorder(logPath, os.Stderr)
	if err != nil {
		return nil, fmt.Errorf("new file recorder %s: %w", logPath, err)
	}

	// The exact observer mapping cmd/gc/api_state.go wrapWithCachingStore installs
	// in the supervisor: opaque run/session/step correlation ids resolved by the
	// CachingStore, actor "cache-reconcile", occurredAt as the envelope Ts.
	onChange := func(eventType, beadID, runID, sessionID, stepID string, payload json.RawMessage, occurredAt time.Time) {
		rec.Record(events.Event{
			Type:      eventType,
			Ts:        occurredAt,
			Actor:     eventActor,
			Subject:   beadID,
			RunID:     runID,
			SessionID: sessionID,
			StepID:    stepID,
			Payload:   payload,
		})
	}

	memStore := seedStore()
	if err := seedSession(memStore); err != nil {
		_ = rec.Close()
		return nil, err
	}

	backing := &failOnceGetStore{
		MemStore: memStore,
		disabled: os.Getenv(disableFailOnceEnv) != "",
	}
	cs := beads.NewCachingStoreWithEventTimestamp(backing, onChange)

	if err := driveRunEmit(cs, backing); err != nil {
		_ = rec.Close()
		return nil, err
	}

	return &Result{
		CityName:      CityName,
		CityPath:      cityPath,
		Config:        emitConfig(),
		CityStore:     memStore,
		RigStores:     map[string]beads.Store{RigName: beads.NewMemStore()},
		EventProv:     rec,
		FailOnceFired: backing.didFire(),
		closeRec:      rec.Close,
	}, nil
}

// seedStore pre-seeds the MemStore with the run beads (root + two steps) at their
// stable, assertion-referenced ids, plus the closed source task, and the
// root→step / step→step dependency edges. MemStore.Create rewrites the id to a
// generated gc-N value, so explicit ids can only be installed through
// NewMemStoreFrom; the run's lifecycle EDGES are then emitted by driving Updates
// and Closes through the CachingStore, which is exactly what STEP 1 specifies
// ("step Updates and Closes and the root Close through the CachingStore").
//
// The beads start open (the source closed) so priming the CachingStore caches
// them and the first real mutation is authoritative.
func seedStore() *beads.MemStore {
	beadList := []beads.Bead{
		{
			ID:     SourceEmitID,
			Title:  "Emit the seeded dashboard run",
			Type:   "task",
			Status: "closed",
		},
		{
			ID:     RunEmitID,
			Title:  RunEmitFormula,
			Type:   "task",
			Status: "open",
			Metadata: beads.StringMap{
				beadmeta.KindMetadataKey:            beadmeta.KindWorkflow,
				beadmeta.FormulaContractMetadataKey: "graph.v2",
				beadmeta.FormulaMetadataKey:         RunEmitFormula,
				beadmeta.SourceBeadIDMetadataKey:    SourceEmitID,
				beadmeta.RunTargetMetadataKey:       "rig:" + RigName,
				beadmeta.RootStoreRefMetadataKey:    "city:" + CityName,
				beadmeta.ScopeKindMetadataKey:       "city",
				beadmeta.ScopeRefMetadataKey:        CityName,
			},
		},
		stepBead(StepAID, StepATitle, "step-a", []string{RunEmitID}),
		stepBead(StepBID, StepBTitle, "step-b", []string{StepAID}),
	}
	deps := []beads.Dep{
		{IssueID: StepAID, DependsOnID: RunEmitID, Type: "blocks"},
		{IssueID: StepBID, DependsOnID: StepAID, Type: "blocks"},
	}
	return beads.NewMemStoreFrom(100, beadList, deps)
}

// seedSession seeds one active session bead (store-only, off the emission path so
// it mints no run lane) so the home dial counts a real active session. It uses
// the session store front door for the correct Type/labels; the generated bead id
// is irrelevant — every projection resolves the session by its alias.
func seedSession(store *beads.MemStore) error {
	sessStore := session.NewStore(beads.SessionStore{Store: store})
	if _, err := sessStore.CreateSessionInfo(session.CreateSpec{
		Title:     AgentName,
		AgentName: AgentName,
		Metadata: map[string]string{
			"alias":        AgentName,
			"session_name": AgentName,
			"template":     AgentTemplate,
			"provider":     "test-agent",
			"state":        AgentState,
		},
	}); err != nil {
		return fmt.Errorf("seed session: %w", err)
	}
	return nil
}

// driveRunEmit performs the run's lifecycle mutations through the CachingStore so
// every edge is a real emission. The ordering is deliberate:
//
//  1. prime the cache so every run bead is resident and the first mutation is
//     authoritative (and, crucially, so step-a's fail-once fault hits the
//     POST-commit refresh, not the pre-write snapshot read);
//  2. arm the one-shot fault and update step-a — its refresh fails, deferring a
//     bead.updated behind an ordered gate at time t1 (no event yet);
//  3. drive step-b to in_progress — a NORMAL bead.updated recorded at t2 > t1
//     with a LOWER seq: the intervening event that makes the pair non-monotonic;
//  4. close step-a — the differing event type resolves the gate WITHOUT
//     coalescing, publishing the recovered bead.updated at its backdated t1
//     (now a HIGHER seq than step-b's t2 event) followed by the bead.closed;
//  5. close step-b, then the root (completed).
func driveRunEmit(cs *beads.CachingStore, backing *failOnceGetStore) error {
	// (1) Prime: cache every open bead so hadPrevious is true on the first mutation.
	if err := cs.PrimeActive(); err != nil {
		return fmt.Errorf("prime cache: %w", err)
	}

	// (2) Fail-once on step-a's first update: the backing refresh (readBeadWithDeps
	// → backing.Get) fails, so the CachingStore commits the mutation but retains a
	// bead.updated intent behind an ordered gate, timestamped at this instant (t1).
	backing.arm(StepAID)
	if err := cs.Update(StepAID, beads.UpdateOpts{Assignee: strPtr(AgentName)}); err != nil {
		return fmt.Errorf("fail-once update step-a: %w", err)
	}

	// A short, deliberate gap so t2 (step-b's normal update, below) is strictly
	// after t1 on any clock granularity, making the non-monotonic pair robust.
	time.Sleep(3 * time.Millisecond)

	// (3) Intervening NORMAL update: recorded immediately at t2 > t1 with a lower
	// seq than the still-gated recovered event.
	if err := cs.Update(StepBID, beads.UpdateOpts{Status: strPtr("in_progress")}); err != nil {
		return fmt.Errorf("update step-b in_progress: %w", err)
	}

	time.Sleep(3 * time.Millisecond)

	// (4) Close step-a: the close (bead.closed) differs in type from the gated
	// bead.updated, so gate resolution publishes the recovered bead.updated at its
	// backdated t1 (higher seq than step-b's t2 event → non-monotonic) and then
	// the bead.closed.
	if err := cs.Close(StepAID); err != nil {
		return fmt.Errorf("close step-a: %w", err)
	}

	if err := cs.Close(StepBID); err != nil {
		return fmt.Errorf("close step-b: %w", err)
	}

	// (5) Close the root: its bead.closed drives the run to a completed terminal
	// state in every run projection. Skippable for STEP 4a anti-vacuity.
	if os.Getenv(disableRootCloseEnv) == "" {
		if err := cs.Close(RunEmitID); err != nil {
			return fmt.Errorf("close run root: %w", err)
		}
	}
	return nil
}

// stepBead builds a step member bead carrying the correlation metadata the
// CachingStore resolves into event envelope fields (run id from gc.root_bead_id,
// session id/name, step id) and the graph.v2 step kind.
func stepBead(id, title, stepID string, needs []string) beads.Bead {
	return beads.Bead{
		ID:     id,
		Title:  title,
		Type:   "task",
		Status: "open",
		Needs:  needs,
		Metadata: beads.StringMap{
			beadmeta.KindMetadataKey:        "step",
			beadmeta.RootBeadIDMetadataKey:  RunEmitID,
			beadmeta.StepIDMetadataKey:      stepID,
			beadmeta.SessionIDMetadataKey:   AgentName,
			beadmeta.SessionNameMetadataKey: AgentName,
			beadmeta.ScopeRefMetadataKey:    CityName,
		},
	}
}

// emitConfig builds the served city config: one rig, one agent, one provider —
// mirroring corpus.corpusConfig but named for the emission scenario.
func emitConfig() *config.City {
	return &config.City{
		Workspace: config.Workspace{Name: CityName},
		Agents: []config.Agent{
			{Name: AgentName, Dir: RigName, Provider: "test-agent", MaxActiveSessions: intPtr(2)},
		},
		Rigs: []config.Rig{
			{Name: RigName, Path: filepath.Join(os.TempDir(), "dashport-emit-"+RigName)},
		},
		Providers: map[string]config.ProviderSpec{
			"test-agent": {DisplayName: "Test Agent"},
		},
	}
}

func strPtr(s string) *string { return &s }
func intPtr(n int) *int       { return &n }
