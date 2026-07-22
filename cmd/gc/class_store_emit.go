package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/gastownhall/gascity/internal/beadmeta"
	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/events"
)

// This file threads bead.* event emission through the one-shot CLI
// coordination-class seam. On a split city the graph/orders/nudges/sessions
// classes live in the embedded infra store, which — unlike the controller's
// copy — is opened WITHOUT a CachingStore, so its mutations were event-silent:
// a worker's `gc mol autoclose` / `gc sling` / `gc convoy` write landed in the
// sqlite infra store but appended nothing to <city>/.gc/events.jsonl, so the
// event-sourced run projection (internal/runproj) showed the step "Running"
// forever. This mirrors the controller's CachingStore.notifyChange for the CLI:
// after a successful mutation the policy store emits the SAME canonical bead
// snapshot payload the CachingStore does, decodable by
// beads.DecodeBeadEventPayload and foldable by runproj.Fold.
//
// The emission is gated on beadPolicyStore.emitCityPath, set only by the CLI
// seam (classStoreWithCLIEmission) on a split city. It is added to the EXISTING
// policy wrapper — never a new wrapper LAYER — so the optional-capability
// assertions the create paths rely on (GraphApplyFor / HandlesFor /
// StorageCreateStore / …) are preserved by construction: the emit-augmented
// store is the same *beadPolicyStore / *beadPolicyGraphStore type with the same
// underlying store, so its method set is a superset of the un-augmented store's.
//
// Best-effort by contract: a recorder-open OR a recorder Record failure (flock
// timeout, ENOSPC, short write, rotation error) never fails the mutation that
// already committed — those diagnostics are surfaced to stderr once per process
// via classStoreEmitWarnWriter/warnClassStoreEmitOnce rather than silently
// swallowed.

// emitStoreKey identifies an augmented class store by the pristine (resolved)
// store instance plus the emit target city path. Only the policy wrapper types
// (which are pointers, hence comparable) ever become keys.
type emitStoreKey struct {
	store    beads.Store
	cityPath string
}

// cliEmitAugmentedStores memoizes the emit-augmented store per
// (pristine store, cityPath) so repeated cli*Store calls with the same cached
// infra store return the SAME instance. Pointer identity is load-bearing: the
// workflow-delete membership scan (cmd_convoy_dispatch.go closeWorkflowMatches)
// dedups scan candidates by store identity in a map[beads.Store], so a per-call
// clone would re-scan the one infra store N times. The map is bounded — one
// entry per infra store instance per cityPath, and the infra store is itself
// per-process memoized (cachedCityInfraStore).
var cliEmitAugmentedStores sync.Map // emitStoreKey → beads.Store

// classStoreWithCLIEmission augments a resolved coordination-class store so its
// one-shot CLI mutations emit canonical bead.* events into
// <cityPath>/.gc/events.jsonl. It is applied by the cli*Store helpers only on a
// split city (where the resolved store is the infra store); on a single-store
// city those helpers return the input work store verbatim and never call this,
// so wrapping stays byte-identical. A store that is not one of the policy
// wrappers is returned unchanged (no emission), never re-wrapped, so no optional
// capability can be lost. The augmentation is memoized per (store, cityPath) so
// repeated calls return the SAME instance (identity-stable for the
// workflow-delete store dedup).
func classStoreWithCLIEmission(store beads.Store, cityPath string) beads.Store {
	if store == nil || strings.TrimSpace(cityPath) == "" {
		return store
	}
	// Only the policy wrappers are augmentable — and, being pointers, comparable,
	// so safe to use as a memo key. Anything else is returned unchanged and never
	// memoized (its dynamic type may not even be comparable).
	switch store.(type) {
	case *beadPolicyGraphStore, *beadPolicyStore:
	default:
		return store
	}
	key := emitStoreKey{store: store, cityPath: cityPath}
	if v, ok := cliEmitAugmentedStores.Load(key); ok {
		return v.(beads.Store)
	}
	var augmented beads.Store
	switch s := store.(type) {
	case *beadPolicyGraphStore:
		augmented = s.withEmitTarget(cityPath)
	case *beadPolicyStore:
		augmented = s.withEmitTarget(cityPath)
	}
	actual, _ := cliEmitAugmentedStores.LoadOrStore(key, augmented)
	return actual.(beads.Store)
}

// withEmitTarget returns a shallow clone of the policy store that emits bead.*
// events to cityPath after each successful mutation. The clone shares the exact
// underlying store, cfg, and minting policy, so it behaves identically except
// for the added emission — and is the SAME type, so every capability assertion
// still resolves. The shared cached infra store is left pristine (the emit
// target is per-CLI-invocation state, not process-wide).
func (s *beadPolicyStore) withEmitTarget(cityPath string) *beadPolicyStore {
	if s == nil || s.emitCityPath == cityPath {
		return s
	}
	clone := *s
	clone.emitCityPath = cityPath
	return &clone
}

// withEmitTarget mirrors beadPolicyStore.withEmitTarget for the graph-store
// variant, rebuilding the wrapper over the emit-augmented inner policy store
// with the same graph applier so ApplyGraphPlan keeps its capability.
func (s *beadPolicyGraphStore) withEmitTarget(cityPath string) *beadPolicyGraphStore {
	if s == nil {
		return s
	}
	inner := s.beadPolicyStore.withEmitTarget(cityPath)
	if inner == s.beadPolicyStore {
		return s
	}
	return &beadPolicyGraphStore{beadPolicyStore: inner, applier: s.applier}
}

// emitsClassStoreEvents reports whether this store has an emit target configured.
func (s *beadPolicyStore) emitsClassStoreEvents() bool {
	return s != nil && s.emitCityPath != ""
}

// classStoreEmission is one pending bead.* event: an event type and the fresh
// bead snapshot to carry as the payload.
type classStoreEmission struct {
	eventType string
	bead      beads.Bead
}

// emitClassStoreBead records a single bead.* event, best-effort.
func (s *beadPolicyStore) emitClassStoreBead(eventType string, b beads.Bead) {
	s.emitClassStoreBeads([]classStoreEmission{{eventType: eventType, bead: b}})
}

// emitClassStoreBeads appends one canonical bead.* event per emission to the
// city's events.jsonl using a single FileRecorder (which owns cross-process
// seq/locking via events.jsonl.seq). It mirrors CachingStore.notifyChange: the
// payload is the raw bead snapshot (json.Marshal(bead)), and the run/session/
// step correlation ids are resolved from the bead metadata onto the typed
// envelope fields. Best-effort: a recorder-open or per-record failure is
// surfaced once via warnClassStoreEmitOnce (through classStoreEmitWarnWriter)
// and never fails the mutation. events.WithoutStartupSweep skips the per-open
// rotating-file sweep (owned by the controller's long-lived recorder), so a
// per-mutation open neither scans the directory nor races the supervisor's
// in-flight rotation.
func (s *beadPolicyStore) emitClassStoreBeads(emissions []classStoreEmission) {
	if !s.emitsClassStoreEvents() || len(emissions) == 0 {
		return
	}
	rec, err := events.NewFileRecorder(
		filepath.Join(s.emitCityPath, ".gc", "events.jsonl"),
		classStoreEmitWarnWriter{},
		events.WithoutStartupSweep(),
	)
	if err != nil {
		warnClassStoreEmitOnce(fmt.Errorf("opening event log for %s: %w", s.emitCityPath, err))
		return
	}
	defer rec.Close() //nolint:errcheck // best-effort: emission must not surface I/O errors
	actor := eventActor()
	for _, e := range emissions {
		if strings.TrimSpace(e.bead.ID) == "" {
			continue
		}
		payload, err := json.Marshal(e.bead)
		if err != nil {
			warnClassStoreEmitOnce(fmt.Errorf("marshal %s payload for %s: %w", e.eventType, e.bead.ID, err))
			continue
		}
		rec.Record(events.Event{
			Type:      e.eventType,
			Actor:     actor,
			Subject:   e.bead.ID,
			RunID:     beadmeta.ResolveRunID(e.bead.Metadata, e.bead.ID, ""),
			SessionID: e.bead.Metadata[beadmeta.SessionIDMetadataKey],
			StepID:    e.bead.Metadata[beadmeta.StepIDMetadataKey],
			Payload:   payload,
		})
	}
}

// freshBeadSnapshot re-reads the post-mutation bead so the emitted payload is
// the authoritative fresh snapshot (the controller's CachingStore does the same
// "Get after write"). It returns ok=false on a read miss so callers never emit a
// bare-ID payload (an empty snapshot clobbers the fold via runproj Apply and
// detaches run membership). On a successful Get it hydrates dependency edges
// (best-effort) because the embedded sqlite bead_json never carries dep rows —
// the controller's CachingStore hydrates them before emitting, so this keeps the
// seam payload on the same wire shape as the Dolt-backed controller payload.
func (s *beadPolicyStore) freshBeadSnapshot(id string) (beads.Bead, bool) {
	b, err := s.Get(id)
	if err != nil || strings.TrimSpace(b.ID) == "" {
		return beads.Bead{}, false
	}
	if deps, derr := s.DepList(id, "down"); derr == nil {
		b.Dependencies = deps
		b.Needs = nil
	}
	return b, true
}

// emitCreatedBead emits bead.created for a freshly created bead. Called from the
// policy create paths after a successful create. A post-write read miss falls
// back to the bead the backing Create returned (a full snapshot, not a bare id),
// so a create event is never dropped for a store with read-after-write lag.
func (s *beadPolicyStore) emitCreatedBead(created beads.Bead, err error) {
	if !s.emitsClassStoreEvents() || err != nil {
		return
	}
	b, ok := s.freshBeadSnapshot(created.ID)
	if !ok {
		if strings.TrimSpace(created.ID) == "" {
			warnClassStoreEmitOnce(errors.New("bead.created skipped: create returned an empty id"))
			return
		}
		b = created
	}
	s.emitClassStoreBead(events.BeadCreated, b)
}

// emitAfterUpdate emits the lifecycle event for a landed Update. It prefers
// bead.closed only on a genuine open→closed transition (wasClosed=false and the
// post-write status is closed) — eventexport drops bead.updated, so close edges
// must ride bead.closed; a metadata-only update of an already-closed bead emits
// bead.updated, matching CachingStore.Update. On a post-write read miss it
// synthesizes the snapshot from opts when the write carried a status (so a
// committed close still rides bead.closed with a non-empty status), and skips
// the emission otherwise (never a bare-id payload).
func (s *beadPolicyStore) emitAfterUpdate(id string, opts beads.UpdateOpts, wasClosed bool) {
	if !s.emitsClassStoreEvents() {
		return
	}
	b, ok := s.freshBeadSnapshot(id)
	if !ok {
		if opts.Status == nil {
			warnClassStoreEmitOnce(fmt.Errorf("bead.updated skipped: refresh miss for %s after update", id))
			return
		}
		b = beads.Bead{ID: id, Status: *opts.Status, Metadata: maps.Clone(opts.Metadata)}
	}
	eventType := events.BeadUpdated
	if !wasClosed && strings.EqualFold(strings.TrimSpace(b.Status), "closed") {
		eventType = events.BeadClosed
	}
	s.emitClassStoreBead(eventType, b)
}

// emitUpdatedBead emits bead.updated with the fresh snapshot for the pure
// metadata / assignment / dependency write paths. A read miss skips the
// emission (never a bare-id payload).
func (s *beadPolicyStore) emitUpdatedBead(id string) {
	if !s.emitsClassStoreEvents() {
		return
	}
	b, ok := s.freshBeadSnapshot(id)
	if !ok {
		warnClassStoreEmitOnce(fmt.Errorf("bead.updated skipped: refresh miss for %s", id))
		return
	}
	s.emitClassStoreBead(events.BeadUpdated, b)
}

// snapshotForDelete captures the pre-delete bead snapshot so bead.deleted can
// carry it (the bead is gone after the delete, so Get-after is impossible). Only
// reads when emission is active. Dependency edges are intentionally NOT hydrated
// here: bead.deleted removes the member from the fold, so its deps are
// irrelevant.
func (s *beadPolicyStore) snapshotForDelete(ids ...string) []beads.Bead {
	if !s.emitsClassStoreEvents() {
		return nil
	}
	out := make([]beads.Bead, 0, len(ids))
	for _, id := range ids {
		if b, err := s.Get(id); err == nil && strings.TrimSpace(b.ID) != "" {
			out = append(out, b)
		} else {
			out = append(out, beads.Bead{ID: id})
		}
	}
	return out
}

// emitDeletedBeads emits bead.deleted for each pre-delete snapshot.
func (s *beadPolicyStore) emitDeletedBeads(snapshots []beads.Bead) {
	if !s.emitsClassStoreEvents() || len(snapshots) == 0 {
		return
	}
	emissions := make([]classStoreEmission, 0, len(snapshots))
	for _, b := range snapshots {
		emissions = append(emissions, classStoreEmission{eventType: events.BeadDeleted, bead: b})
	}
	s.emitClassStoreBeads(emissions)
}

// emitGraphApplyCreated emits bead.created for every bead a graph-apply plan
// materialized. Graph apply is a create path (it materializes the molecule root
// and step beads through the infra store), so relocated CLI graph applies must
// emit bead.created just like a direct Create. A per-bead read miss skips that
// bead (never a bare-id payload) rather than dropping the whole batch.
func (s *beadPolicyStore) emitGraphApplyCreated(result *beads.GraphApplyResult, err error) {
	if !s.emitsClassStoreEvents() || err != nil || result == nil || len(result.IDs) == 0 {
		return
	}
	emissions := make([]classStoreEmission, 0, len(result.IDs))
	for _, id := range result.IDs {
		if strings.TrimSpace(id) == "" {
			continue
		}
		b, ok := s.freshBeadSnapshot(id)
		if !ok {
			warnClassStoreEmitOnce(fmt.Errorf("bead.created skipped: refresh miss for graph-applied %s", id))
			continue
		}
		emissions = append(emissions, classStoreEmission{eventType: events.BeadCreated, bead: b})
	}
	s.emitClassStoreBeads(emissions)
}

// The mutation methods below shadow the promoted beads.Store methods on the
// policy store so a successful CLI-seam write emits the matching bead.* event.
// Each is a pure delegation plus emission, and every emission path early-returns
// when emitCityPath is empty (the controller, work store, and single-store
// city), so those callers keep the exact promoted behavior with zero extra reads
// — byte-identical to before this file existed.

// Update shadows the promoted Store.Update. It captures the pre-write closed
// state (only when emitting) so it can promote to bead.closed strictly on an
// open→closed transition, matching CloseAll here and CachingStore.Update.
func (s *beadPolicyStore) Update(id string, opts beads.UpdateOpts) error {
	wasClosed := false
	if s.emitsClassStoreEvents() {
		if b, err := s.Get(id); err == nil {
			wasClosed = strings.EqualFold(strings.TrimSpace(b.Status), "closed")
		}
	}
	if err := s.Store.Update(id, opts); err != nil {
		return err
	}
	s.emitAfterUpdate(id, opts, wasClosed)
	return nil
}

// Close shadows the promoted Store.Close to emit bead.closed with the fresh
// snapshot (status forced closed, mirroring CachingStore, in case a read lag
// serves the pre-close row). Unlike the update path, a read miss still emits a
// close (with the id and forced status) because the terminal transition is
// proven committed.
func (s *beadPolicyStore) Close(id string) error {
	if err := s.Store.Close(id); err != nil {
		return err
	}
	if s.emitsClassStoreEvents() {
		closed, ok := s.freshBeadSnapshot(id)
		if !ok {
			closed = beads.Bead{ID: id}
		}
		closed.Status = "closed"
		s.emitClassStoreBead(events.BeadClosed, closed)
	}
	return nil
}

// Reopen shadows the promoted Store.Reopen to emit bead.updated (the bead
// returns to an active status).
func (s *beadPolicyStore) Reopen(id string) error {
	if err := s.Store.Reopen(id); err != nil {
		return err
	}
	s.emitUpdatedBead(id)
	return nil
}

// CloseAll shadows the promoted Store.CloseAll to emit bead.closed for each id
// that transitioned to closed. Pre-status is captured only when emitting so a
// bead already closed does not produce a spurious event.
func (s *beadPolicyStore) CloseAll(ids []string, metadata map[string]string) (int, error) {
	var wasOpen map[string]bool
	if s.emitsClassStoreEvents() {
		wasOpen = make(map[string]bool, len(ids))
		for _, id := range ids {
			if b, err := s.Get(id); err == nil {
				wasOpen[id] = !strings.EqualFold(strings.TrimSpace(b.Status), "closed")
			} else {
				wasOpen[id] = true
			}
		}
	}
	n, err := s.Store.CloseAll(ids, metadata)
	if s.emitsClassStoreEvents() {
		var emissions []classStoreEmission
		for _, id := range ids {
			if !wasOpen[id] {
				continue
			}
			closed, ok := s.freshBeadSnapshot(id)
			if !ok || !strings.EqualFold(strings.TrimSpace(closed.Status), "closed") {
				continue
			}
			emissions = append(emissions, classStoreEmission{eventType: events.BeadClosed, bead: closed})
		}
		s.emitClassStoreBeads(emissions)
	}
	return n, err
}

// SetMetadata shadows the promoted Store.SetMetadata to emit bead.updated.
func (s *beadPolicyStore) SetMetadata(id, key, value string) error {
	if err := s.Store.SetMetadata(id, key, value); err != nil {
		return err
	}
	s.emitUpdatedBead(id)
	return nil
}

// SetMetadataBatch shadows the promoted Store.SetMetadataBatch to emit
// bead.updated.
func (s *beadPolicyStore) SetMetadataBatch(id string, kvs map[string]string) error {
	if err := s.Store.SetMetadataBatch(id, kvs); err != nil {
		return err
	}
	s.emitUpdatedBead(id)
	return nil
}

// Delete shadows the promoted Store.Delete to emit bead.deleted with the
// pre-delete snapshot.
func (s *beadPolicyStore) Delete(id string) error {
	snapshot := s.snapshotForDelete(id)
	if err := s.Store.Delete(id); err != nil {
		return err
	}
	s.emitDeletedBeads(snapshot)
	return nil
}

// DepAdd shadows the promoted Store.DepAdd to emit bead.updated for the bead
// whose dependency edges changed. The fresh snapshot's Dependencies are
// hydrated in freshBeadSnapshot via DepList (the embedded sqlite bead_json never
// carries dep edges), so the emitted payload reflects the new edge.
func (s *beadPolicyStore) DepAdd(issueID, dependsOnID, depType string) error {
	if err := s.Store.DepAdd(issueID, dependsOnID, depType); err != nil {
		return err
	}
	s.emitUpdatedBead(issueID)
	return nil
}

// DepRemove shadows the promoted Store.DepRemove to emit bead.updated.
func (s *beadPolicyStore) DepRemove(issueID, dependsOnID string) error {
	if err := s.Store.DepRemove(issueID, dependsOnID); err != nil {
		return err
	}
	s.emitUpdatedBead(issueID)
	return nil
}

// classStoreEmitWarnWriter funnels a FileRecorder's own stderr diagnostics
// (flock-timeout drops, short writes, ENOSPC, rotation errors surfaced inside
// Record) into the warn-once path, so a dropped terminal event is visible once
// per process rather than discarded. It always reports a full write so it is a
// safe recorder stderr sink.
type classStoreEmitWarnWriter struct{}

func (classStoreEmitWarnWriter) Write(p []byte) (int, error) {
	if msg := strings.TrimSpace(string(p)); msg != "" {
		warnClassStoreEmitOnce(errors.New(msg))
	}
	return len(p), nil
}

// classStoreEmitWarn is the emission-diagnostic sink. It is a package var so
// tests can capture diagnostics; the default surfaces the first diagnostic to
// stderr and suppresses the rest, so a broken event log is not silent but also
// does not flood a worker's stderr. Emission is best-effort, so this never
// affects the mutation's outcome.
var classStoreEmitWarn = onceWarnToStderr()

func onceWarnToStderr() func(error) {
	var once sync.Once
	return func(err error) {
		once.Do(func() {
			fmt.Fprintf(os.Stderr, "gc: class-store event emission: %v (further emission errors suppressed)\n", err) //nolint:errcheck // best-effort stderr
		})
	}
}

// warnClassStoreEmitOnce routes an emission diagnostic through the current sink.
func warnClassStoreEmitOnce(err error) { classStoreEmitWarn(err) }
