package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/beadmeta"
	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/config"
	"github.com/gastownhall/gascity/internal/events"
	"github.com/gastownhall/gascity/internal/runproj"
)

// readEmittedEvents parses <cityPath>/.gc/events.jsonl into a slice of events.
// The log is append-only JSONL (one event per line), so it is read line by
// line. A missing file is treated as "no events" so a no-emission assertion
// does not depend on the file having been created.
func readEmittedEvents(t *testing.T, cityPath string) []events.Event {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(cityPath, ".gc", "events.jsonl"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatalf("reading events.jsonl: %v", err)
	}
	trimmed := strings.TrimSpace(string(data))
	if trimmed == "" {
		return nil
	}
	var out []events.Event
	for i, line := range strings.Split(trimmed, "\n") {
		var e events.Event
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			t.Fatalf("unmarshal events.jsonl line %d (%q): %v", i, line, err)
		}
		out = append(out, e)
	}
	return out
}

// newSQLiteInfraStoreForTest opens a real embedded sqlite infra store under a
// temp dir, wrapped in the production infra policy stack — the exact store shape
// cachedCityInfraStore returns on a split city.
func newSQLiteInfraStoreForTest(t *testing.T, cfg *config.City) beads.Store {
	t.Helper()
	scope := t.TempDir()
	if err := os.MkdirAll(filepath.Join(scope, ".beads"), 0o755); err != nil {
		t.Fatalf("mkdir infra .beads: %v", err)
	}
	store, err := beads.OpenSQLiteStore(
		filepath.Join(scope, ".beads"),
		beads.WithSQLiteStoreIDPrefix(config.InfraScopePrefix),
	)
	if err != nil {
		t.Fatalf("open embedded sqlite infra store: %v", err)
	}
	t.Cleanup(func() { _ = closeBeadStoreHandle(store) })
	return wrapInfraStoreWithBeadPolicies(store, cfg)
}

// withInjectedInfraStore points cachedCityInfraStore at the given store for the
// duration of the test — the split-city seam the CLI helpers source the infra
// store from.
func withInjectedInfraStore(t *testing.T, cityPath string, infra beads.Store) {
	t.Helper()
	clearInfraStoreCacheKey(cityPath)
	restore := swapCachedInfraStoreOpen(func(string) (beads.Store, bool, error) {
		return infra, true, nil
	})
	t.Cleanup(func() {
		restore()
		clearInfraStoreCacheKey(cityPath)
	})
}

// TestCLIGraphStoreEmitsBeadEventsOnSplitCity is the core regression: a one-shot
// CLI mutation through the graph coordination-class seam (cliGraphStore) on a
// split city must emit a canonical bead.* event whose payload decodes via
// beads.DecodeBeadEventPayload and folds correctly in runproj.Fold. Before the
// fix these paths wrote to the embedded sqlite infra store silently, so the
// event-sourced run projection showed steps "Running" forever.
func TestCLIGraphStoreEmitsBeadEventsOnSplitCity(t *testing.T) {
	cfg := &config.City{}
	cityPath := t.TempDir()
	withInjectedInfraStore(t, cityPath, newSQLiteInfraStoreForTest(t, cfg))

	// The input store is the city WORK store; on a split city cliGraphStore routes
	// past it to the infra store.
	workStore := wrapStoreWithBeadPolicies(beads.NewMemStore(), cfg)
	graph := cliGraphStore(workStore, cfg, cityPath)

	// Create → bead.created.
	created, err := graph.Create(beads.Bead{
		Title:    "step one",
		Type:     "task",
		Metadata: map[string]string{beadmeta.RootBeadIDMetadataKey: "gcg-root", beadmeta.StepIDMetadataKey: "step-1", beadmeta.SessionIDMetadataKey: "sess-1"},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if strings.TrimSpace(created.ID) == "" {
		t.Fatalf("Create returned empty id")
	}

	// Claim: Update status→in_progress → bead.updated.
	inProgress := "in_progress"
	if err := graph.Update(created.ID, beads.UpdateOpts{Status: &inProgress}); err != nil {
		t.Fatalf("Update(in_progress): %v", err)
	}

	// SetMetadata → bead.updated (metadata-only write).
	if err := graph.SetMetadata(created.ID, "review.verdict", "pass"); err != nil {
		t.Fatalf("SetMetadata: %v", err)
	}

	// Close → bead.closed (the terminal transition workers perform).
	if err := graph.Close(created.ID); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// A second bead exercises Update(status→closed) preferring bead.closed, then
	// Delete → bead.deleted.
	second, err := graph.Create(beads.Bead{Title: "step two", Type: "task"})
	if err != nil {
		t.Fatalf("Create second: %v", err)
	}
	closedStatus := "closed"
	if err := graph.Update(second.ID, beads.UpdateOpts{Status: &closedStatus}); err != nil {
		t.Fatalf("Update(closed): %v", err)
	}
	if err := graph.Delete(second.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	evts := readEmittedEvents(t, cityPath)
	if len(evts) == 0 {
		t.Fatalf("no bead.* events emitted through the CLI graph seam on a split city")
	}

	// Every payload must decode via the shared canonical decoder.
	for _, e := range evts {
		if !strings.HasPrefix(e.Type, "bead.") {
			continue
		}
		b, ok := beads.DecodeBeadEventPayload(e.Payload)
		if !ok {
			t.Fatalf("event %s payload did not decode via DecodeBeadEventPayload: %s", e.Type, string(e.Payload))
		}
		if b.ID == "" {
			t.Fatalf("event %s decoded to a bead with empty id", e.Type)
		}
	}

	// bead.created for both beads; a close transition prefers bead.closed over
	// bead.updated (eventexport drops bead.updated, so close edges must survive).
	if got := len(eventsOfType(evts, events.BeadCreated)); got != 2 {
		t.Errorf("bead.created count = %d, want 2", got)
	}
	if got := len(eventsOfType(evts, events.BeadClosed)); got < 2 {
		t.Errorf("bead.closed count = %d, want >= 2 (Close + Update(status=closed))", got)
	}
	if got := len(eventsOfType(evts, events.BeadDeleted)); got != 1 {
		t.Errorf("bead.deleted count = %d, want 1", got)
	}
	if got := len(eventsOfType(evts, events.BeadUpdated)); got < 2 {
		t.Errorf("bead.updated count = %d, want >= 2 (claim + set-metadata)", got)
	}

	// Correlation envelope fields ride the metadata like the controller emitter.
	for _, e := range eventsOfType(evts, events.BeadCreated) {
		if e.Subject != created.ID && e.Subject != second.ID {
			t.Errorf("bead.created subject = %q, want a created bead id", e.Subject)
		}
	}
	var firstCreated *events.Event
	for i := range evts {
		if evts[i].Type == events.BeadCreated && evts[i].Subject == created.ID {
			firstCreated = &evts[i]
			break
		}
	}
	if firstCreated == nil {
		t.Fatalf("no bead.created for %s", created.ID)
	}
	if firstCreated.RunID != "gcg-root" {
		t.Errorf("bead.created run_id = %q, want gcg-root (ResolveRunID from gc.root_bead_id)", firstCreated.RunID)
	}
	if firstCreated.SessionID != "sess-1" {
		t.Errorf("bead.created session_id = %q, want sess-1", firstCreated.SessionID)
	}
	if firstCreated.StepID != "step-1" {
		t.Errorf("bead.created step_id = %q, want step-1", firstCreated.StepID)
	}

	// The production signature: fold the emitted stream and the claimed-then-closed
	// bead must land terminal (status closed), not stuck "Running".
	folded := runproj.Fold(evts)
	if b, ok := folded[created.ID]; !ok {
		t.Errorf("folded projection is missing %s", created.ID)
	} else if b.Status != "closed" {
		t.Errorf("folded %s status = %q, want closed (claim in_progress → CLI close → fold shows closed)", created.ID, b.Status)
	}
	if _, ok := folded[second.ID]; ok {
		t.Errorf("folded projection still holds deleted bead %s", second.ID)
	}
}

// TestCLIClassStoreSeamsAllEmitOnSplitCity proves the orders, nudges, and
// sessions coordination-class CLI seams share the graph seam's emission cleanly:
// a mutation through each augmented store appends a bead.* event, so a
// [beads.classes.<class>] relocation reaches one-shot commands with the same
// event fidelity the running controller gets from its CachingStore.
func TestCLIClassStoreSeamsAllEmitOnSplitCity(t *testing.T) {
	cfg := &config.City{}
	seams := map[string]func(beads.Store, *config.City, string) beads.Store{
		"order":   cliOrderStore,
		"nudges":  cliNudgesStore,
		"session": cliSessionStore,
		"mail":    cliMailStore,
	}
	for name, seam := range seams {
		t.Run(name, func(t *testing.T) {
			cityPath := t.TempDir()
			withInjectedInfraStore(t, cityPath, newSQLiteInfraStoreForTest(t, cfg))
			workStore := wrapStoreWithBeadPolicies(beads.NewMemStore(), cfg)
			store := seam(workStore, cfg, cityPath)

			created, err := store.Create(beads.Bead{Title: name + " bead", Type: "task"})
			if err != nil {
				t.Fatalf("%s seam Create: %v", name, err)
			}
			if err := store.Close(created.ID); err != nil {
				t.Fatalf("%s seam Close: %v", name, err)
			}

			evts := readEmittedEvents(t, cityPath)
			if len(eventsOfType(evts, events.BeadCreated)) != 1 {
				t.Errorf("%s seam: bead.created count = %d, want 1", name, len(eventsOfType(evts, events.BeadCreated)))
			}
			if len(eventsOfType(evts, events.BeadClosed)) != 1 {
				t.Errorf("%s seam: bead.closed count = %d, want 1", name, len(eventsOfType(evts, events.BeadClosed)))
			}
		})
	}
}

// TestCLIGraphStoreSingleStoreNoEmission proves the single-store city stays
// byte-identical: cliGraphStore is identity over the work store (no infra store),
// so no recorder is constructed and no events are emitted.
func TestCLIGraphStoreSingleStoreNoEmission(t *testing.T) {
	cfg := &config.City{}
	cityPath := t.TempDir()
	// No infra store: cachedCityInfraStore resolves to nil (single-store city).
	clearInfraStoreCacheKey(cityPath)
	restore := swapCachedInfraStoreOpen(func(string) (beads.Store, bool, error) {
		return nil, false, nil
	})
	t.Cleanup(func() { restore(); clearInfraStoreCacheKey(cityPath) })

	workStore := wrapStoreWithBeadPolicies(beads.NewMemStore(), cfg)
	graph := cliGraphStore(workStore, cfg, cityPath)

	// Identity: the resolved store must be the exact input store (no wrapping).
	if graph != workStore {
		t.Fatalf("cliGraphStore on a single-store city must return the input store verbatim (identity)")
	}

	created, err := graph.Create(beads.Bead{Title: "solo", Type: "task"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := graph.Close(created.ID); err != nil {
		t.Fatalf("Close: %v", err)
	}

	if evts := readEmittedEvents(t, cityPath); len(evts) != 0 {
		t.Fatalf("single-store city emitted %d events, want 0 (byte-identical to upstream)", len(evts))
	}
}

// TestCLIClassStoreEmitterPreservesCapabilities is the capability-preservation
// guard: the emit-augmented store the CLI seam returns on a split city must
// still satisfy every optional capability the un-augmented infra store does
// (GraphApplyFor / StorageCreateStore / HandlesFor and the rest), because
// molecule.Instantiate and the create paths type-assert them.
func TestCLIClassStoreEmitterPreservesCapabilities(t *testing.T) {
	cfg := &config.City{}
	cityPath := t.TempDir()
	infra := newSQLiteInfraStoreForTest(t, cfg)
	withInjectedInfraStore(t, cityPath, infra)

	workStore := wrapStoreWithBeadPolicies(beads.NewMemStore(), cfg)
	augmented := cliGraphStore(workStore, cfg, cityPath)

	// The un-augmented infra store is the baseline: whatever capability it has,
	// the augmented store must have too.
	assertSameCapability := func(name string, base, aug bool) {
		if base != aug {
			t.Errorf("capability %s: baseline=%v augmented=%v (emit wrap must preserve it)", name, base, aug)
		}
		if !aug {
			t.Errorf("capability %s: infra store is expected to expose it but augmented store does not", name)
		}
	}

	_, baseGraph := beads.GraphApplyFor(infra)
	_, augGraph := beads.GraphApplyFor(augmented)
	assertSameCapability("GraphApplyStore", baseGraph, augGraph)

	_, baseStorage := infra.(beads.StorageCreateStore)
	_, augStorage := augmented.(beads.StorageCreateStore)
	// StorageCreateStore is exposed by the sqlite store; keep them in lockstep.
	if baseStorage != augStorage {
		t.Errorf("capability StorageCreateStore: baseline=%v augmented=%v", baseStorage, augStorage)
	}

	assertSame := func(name string, base, aug bool) {
		if base != aug {
			t.Errorf("capability %s: baseline=%v augmented=%v (emit wrap must preserve it)", name, base, aug)
		}
	}
	_, baseRel := infra.(beads.ConditionalAssignmentReleaser)
	_, augRel := augmented.(beads.ConditionalAssignmentReleaser)
	assertSame("ConditionalAssignmentReleaser", baseRel, augRel)

	_, baseBatch := infra.(beads.BatchDeleter)
	_, augBatch := augmented.(beads.BatchDeleter)
	assertSame("BatchDeleter", baseBatch, augBatch)

	_, baseCounter := infra.(beads.Counter)
	_, augCounter := augmented.(beads.Counter)
	assertSame("Counter", baseCounter, augCounter)

	_, baseTarget := infra.(beads.ConditionalWritesResolveTargeter)
	_, augTarget := augmented.(beads.ConditionalWritesResolveTargeter)
	assertSame("ConditionalWritesResolveTargeter", baseTarget, augTarget)

	// HandlesFor must resolve the same handle surface (never nil sub-readers).
	baseHandles := beads.HandlesFor(infra)
	augHandles := beads.HandlesFor(augmented)
	if (baseHandles.Live == nil) != (augHandles.Live == nil) {
		t.Errorf("HandlesFor().Live presence differs: baseline=%v augmented=%v", baseHandles.Live != nil, augHandles.Live != nil)
	}
}

// failingGetStore wraps a backing store so its Get can be forced to miss after a
// write commits, exercising the emitter's read-after-write-miss handling. All
// other operations (Create/Update/Close/SetMetadata) delegate to the backing.
type failingGetStore struct {
	beads.Store
	failGet bool
}

func (f *failingGetStore) Get(id string) (beads.Bead, error) {
	if f.failGet {
		return beads.Bead{}, beads.ErrNotFound
	}
	return f.Store.Get(id)
}

// TestCLIClassStoreHydratesDependencyEdges proves the seam payload carries the
// bead's dependency edges even though the embedded sqlite bead_json never stores
// them: freshBeadSnapshot hydrates Dependencies via DepList, so a DepAdd through
// the emitting seam records a bead.updated whose payload decodes with the edge.
func TestCLIClassStoreHydratesDependencyEdges(t *testing.T) {
	cfg := &config.City{}
	cityPath := t.TempDir()
	withInjectedInfraStore(t, cityPath, wrapInfraStoreWithBeadPolicies(beads.NewMemStoreHonoringIDs(), cfg))
	graph := cliGraphStore(wrapStoreWithBeadPolicies(beads.NewMemStore(), cfg), cfg, cityPath)

	blocker, err := graph.Create(beads.Bead{Title: "blocker", Type: "task"})
	if err != nil {
		t.Fatalf("Create blocker: %v", err)
	}
	blocked, err := graph.Create(beads.Bead{Title: "blocked", Type: "task"})
	if err != nil {
		t.Fatalf("Create blocked: %v", err)
	}
	if err := graph.DepAdd(blocked.ID, blocker.ID, "blocks"); err != nil {
		t.Fatalf("DepAdd: %v", err)
	}

	evts := readEmittedEvents(t, cityPath)
	var found bool
	for _, e := range eventsOfType(evts, events.BeadUpdated) {
		b, ok := beads.DecodeBeadEventPayload(e.Payload)
		if !ok || b.ID != blocked.ID {
			continue
		}
		for _, d := range b.Dependencies {
			if d.DependsOnID == blocker.ID {
				found = true
			}
		}
	}
	if !found {
		t.Fatalf("bead.updated for %s did not carry the hydrated dependency on %s; events=%s", blocked.ID, blocker.ID, string(mustJSON(t, evts)))
	}
}

// TestCLIClassStoreReadMissNeverEmitsBareID proves a post-write refresh miss
// never writes a bare-ID payload (which would clobber the fold), and that a
// committed close still rides bead.closed with a non-empty status synthesized
// from opts.
func TestCLIClassStoreReadMissNeverEmitsBareID(t *testing.T) {
	cfg := &config.City{}
	cityPath := t.TempDir()
	backing := &failingGetStore{Store: beads.NewMemStoreHonoringIDs()}
	withInjectedInfraStore(t, cityPath, wrapInfraStoreWithBeadPolicies(backing, cfg))
	graph := cliGraphStore(wrapStoreWithBeadPolicies(beads.NewMemStore(), cfg), cfg, cityPath)

	created, err := graph.Create(beads.Bead{Title: "step", Type: "task"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// From now on every post-write Get misses.
	backing.failGet = true

	// A metadata-only update whose refresh misses must SKIP (no bare-ID payload).
	if err := graph.SetMetadata(created.ID, "k", "v"); err != nil {
		t.Fatalf("SetMetadata: %v", err)
	}
	// A committed close (via Update status=closed) whose refresh misses must still
	// emit bead.closed with a non-empty status synthesized from opts.
	closed := "closed"
	if err := graph.Update(created.ID, beads.UpdateOpts{Status: &closed}); err != nil {
		t.Fatalf("Update(closed): %v", err)
	}

	evts := readEmittedEvents(t, cityPath)
	for _, e := range evts {
		if !strings.HasPrefix(e.Type, "bead.") {
			continue
		}
		b, ok := beads.DecodeBeadEventPayload(e.Payload)
		if !ok {
			t.Fatalf("event %s payload did not decode", e.Type)
		}
		// The bead.created carried a full snapshot (created before failGet); the
		// only other emission must be the bead.closed with a non-empty status.
		if e.Type == events.BeadUpdated {
			t.Fatalf("a refresh-miss metadata update emitted a bead.updated (bare-ID clobber): %s", string(e.Payload))
		}
		if e.Type == events.BeadClosed {
			if !strings.EqualFold(b.Status, "closed") {
				t.Fatalf("bead.closed on refresh miss has status %q, want closed", b.Status)
			}
		}
	}
	if n := len(eventsOfType(evts, events.BeadClosed)); n != 1 {
		t.Fatalf("bead.closed count = %d, want 1 (committed close on refresh miss)", n)
	}
}

// TestCLIClassStoreUpdateTransitionAware proves a metadata-only update of an
// already-closed bead emits bead.updated, not a spurious bead.closed — the close
// edge is promoted strictly on an open→closed transition.
func TestCLIClassStoreUpdateTransitionAware(t *testing.T) {
	cfg := &config.City{}
	cityPath := t.TempDir()
	withInjectedInfraStore(t, cityPath, wrapInfraStoreWithBeadPolicies(beads.NewMemStoreHonoringIDs(), cfg))
	graph := cliGraphStore(wrapStoreWithBeadPolicies(beads.NewMemStore(), cfg), cfg, cityPath)

	created, err := graph.Create(beads.Bead{Title: "step", Type: "task"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := graph.Close(created.ID); err != nil {
		t.Fatalf("Close: %v", err)
	}
	// Metadata-only update of the already-closed bead.
	if err := graph.SetMetadata(created.ID, "review.verdict", "pass"); err != nil {
		t.Fatalf("SetMetadata on closed bead: %v", err)
	}

	evts := readEmittedEvents(t, cityPath)
	if n := len(eventsOfType(evts, events.BeadClosed)); n != 1 {
		t.Fatalf("bead.closed count = %d, want 1 (no spurious close on already-closed update)", n)
	}
	if n := len(eventsOfType(evts, events.BeadUpdated)); n != 1 {
		t.Fatalf("bead.updated count = %d, want 1 (metadata update of closed bead)", n)
	}
	if last := evts[len(evts)-1]; last.Type != events.BeadUpdated {
		t.Fatalf("final event = %s, want bead.updated", last.Type)
	}
}

// TestClassStoreEmitWarnWriterFunnelsRecordFailures proves the recorder's own
// stderr diagnostics (flock-timeout drops, write failures) are funneled through
// the warn sink instead of discarded, so a dropped terminal event is visible.
func TestClassStoreEmitWarnWriterFunnelsRecordFailures(t *testing.T) {
	var got string
	prev := classStoreEmitWarn
	classStoreEmitWarn = func(err error) { got = err.Error() }
	t.Cleanup(func() { classStoreEmitWarn = prev })

	n, err := classStoreEmitWarnWriter{}.Write([]byte("events: lock: timed out after 5000ms\n"))
	if err != nil || n == 0 {
		t.Fatalf("adapter Write returned n=%d err=%v", n, err)
	}
	if !strings.Contains(got, "timed out") {
		t.Fatalf("recorder diagnostic not funneled to warn sink; got %q", got)
	}
}

// TestCLIClassStoreReleaseIfCurrentEmits proves a landed conditional
// assignment release through the augmented infra store emits bead.updated (the
// path doBdReleaseIfCurrent drives for a split city's reserved-prefix ids).
func TestCLIClassStoreReleaseIfCurrentEmits(t *testing.T) {
	cfg := &config.City{}
	cityPath := t.TempDir()
	withInjectedInfraStore(t, cityPath, wrapInfraStoreWithBeadPolicies(beads.NewMemStoreHonoringIDs(), cfg))
	store := cliGraphStore(wrapStoreWithBeadPolicies(beads.NewMemStore(), cfg), cfg, cityPath)

	assignee := "worker-1"
	created, err := store.Create(beads.Bead{Title: "claimed", Type: "task", Assignee: assignee})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	inProgress := "in_progress"
	if err := store.Update(created.ID, beads.UpdateOpts{Status: &inProgress}); err != nil {
		t.Fatalf("Update(in_progress): %v", err)
	}

	releaser, ok := store.(beads.ConditionalAssignmentReleaser)
	if !ok {
		t.Fatalf("augmented store does not expose ConditionalAssignmentReleaser")
	}
	released, err := releaser.ReleaseIfCurrent(created.ID, assignee)
	if err != nil {
		t.Fatalf("ReleaseIfCurrent: %v", err)
	}
	if !released {
		t.Fatalf("ReleaseIfCurrent reported not released")
	}

	// The last emission must be a bead.updated for the released bead.
	evts := readEmittedEvents(t, cityPath)
	var sawReleaseUpdate bool
	for _, e := range eventsOfType(evts, events.BeadUpdated) {
		if e.Subject == created.ID {
			sawReleaseUpdate = true
		}
	}
	if !sawReleaseUpdate {
		t.Fatalf("release-if-current did not emit bead.updated for %s", created.ID)
	}
}

// TestOpenNudgeBeadStoreEmitsOnSplitCity proves the nudge bead seam
// (openNudgeBeadStore) routes through the emitting cliNudgesStore on a split
// city and stays byte-identical (no emission) on a single-store city.
func TestOpenNudgeBeadStoreEmitsOnSplitCity(t *testing.T) {
	cfg := &config.City{}

	t.Run("split city emits", func(t *testing.T) {
		cityPath := t.TempDir()
		withInjectedInfraStore(t, cityPath, wrapInfraStoreWithBeadPolicies(beads.NewMemStoreHonoringIDs(), cfg))
		ns := openNudgeBeadStore(cityPath)
		if ns.Store == nil {
			t.Fatalf("openNudgeBeadStore returned a nil store")
		}
		created, err := ns.Create(beads.Bead{Title: "nudge", Type: nudgeBeadType, Labels: []string{nudgeBeadLabel}})
		if err != nil {
			t.Fatalf("Create nudge: %v", err)
		}
		if err := ns.Close(created.ID); err != nil {
			t.Fatalf("Close nudge: %v", err)
		}
		evts := readEmittedEvents(t, cityPath)
		if len(eventsOfType(evts, events.BeadClosed)) != 1 {
			t.Fatalf("openNudgeBeadStore split-city: bead.closed count = %d, want 1", len(eventsOfType(evts, events.BeadClosed)))
		}
	})

	t.Run("single-store city is not augmented", func(t *testing.T) {
		cityPath := t.TempDir()
		clearInfraStoreCacheKey(cityPath)
		restore := swapCachedInfraStoreOpen(func(string) (beads.Store, bool, error) { return nil, false, nil })
		t.Cleanup(func() { restore(); clearInfraStoreCacheKey(cityPath) })
		ns := openNudgeBeadStore(cityPath)
		if ns.Store == nil {
			t.Fatalf("openNudgeBeadStore returned a nil store")
		}
		// Byte-identical: the seam adds no emit augmentation on a single-store city
		// (the underlying store keeps whatever emission behavior it already had).
		if e, ok := ns.Store.(interface{ emitsClassStoreEvents() bool }); ok && e.emitsClassStoreEvents() {
			t.Fatalf("single-store openNudgeBeadStore returned an emit-augmented store; must be byte-identical")
		}
	})
}

// TestWorkflowServeWakeIgnoresSelfActorEvents proves the control-dispatcher
// serve-follow wake filters its own emissions (a usable self identity) while
// still waking on foreign bead.* events, and never filters when the identity is
// the ambiguous "human"/empty fallback.
func TestWorkflowServeWakeIgnoresSelfActorEvents(t *testing.T) {
	prevDebounce := workflowServeWakeDebounce
	workflowServeWakeDebounce = time.Millisecond
	t.Cleanup(func() { workflowServeWakeDebounce = prevDebounce })

	deliver := func(actor string) chan workflowWatchResult {
		ch := make(chan workflowWatchResult, 1)
		ch <- workflowWatchResult{evt: events.Event{Type: events.BeadClosed, Subject: "gcg-1", Actor: actor}}
		return ch
	}

	// Self-actor event with a usable identity does not wake (timer fires).
	if wake, err := waitForRelevantWorkflowWakeWithTrace(deliver("dispatcher-1"), 15*time.Millisecond, -1, "dispatcher-1", true); err != nil || wake {
		t.Fatalf("self-actor event: wake=%v err=%v, want no wake", wake, err)
	}
	// Foreign actor wakes the loop.
	if wake, err := waitForRelevantWorkflowWakeWithTrace(deliver("worker-7"), time.Second, -1, "dispatcher-1", true); err != nil || !wake {
		t.Fatalf("foreign-actor event: wake=%v err=%v, want wake", wake, err)
	}
	// Unusable identity ("human", selfUsable=false) never filters.
	if wake, err := waitForRelevantWorkflowWakeWithTrace(deliver("human"), time.Second, -1, "human", false); err != nil || !wake {
		t.Fatalf("unusable identity: wake=%v err=%v, want wake (no self-filter)", wake, err)
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return b
}
