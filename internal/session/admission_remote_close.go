package session

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/runtime"
)

// closeRemoteSource never releases identifiers before a proven stop and a
// source-revision close. Stores with atomic terminal metadata use one CAS;
// the remaining conditional path closes first, then conditionally clears the
// already-closed row. A partial close preserves the intent for exact recovery.
func (m *Manager) closeRemoteSource(id, name string, observed beads.Bead, binding runtime.RuntimeBinding) error {
	if observed.Status == "closed" {
		return m.finishClosedRemoteSource(id, observed)
	}
	if _, err := Transition(canonicalLifecycleState(State(observed.Metadata["state"])), CmdClose); err != nil {
		return err
	}
	stopped, err := m.stopRemoteSource(id, name, observed, binding)
	if err != nil {
		return err
	}
	patch := remoteCloseSourcePatch(stopped.source)
	if closer, ok := beads.AtomicConditionalCloserFor(beads.SessionStore{Store: m.store}); ok {
		_, err = closer.CloseWithMetadataIfMatch(id, stopped.source.Revision, patch)
		return err
	}
	// This is never an unconditional close/cleanup fallback.
	if err := stopped.writer.CloseIfMatch(id, stopped.source.Revision); err != nil {
		return err
	}
	closed, err := beads.HandlesFor(m.store).Live.Get(id)
	if err != nil {
		return err
	}
	if closed.Status != "closed" || closed.ID != id || closed.Metadata[remoteAdmissionStopMetadataKey] != stopped.intent {
		return CapacityAdmissionError("remote_close_source_changed")
	}
	openShape := closed
	openShape.Status = stopped.source.Status
	if !sameRemoteStopSource(stopped.source, openShape) {
		return CapacityAdmissionError("remote_close_source_changed")
	}
	return stopped.writer.UpdateIfMatch(id, closed.Revision, beads.UpdateOpts{Metadata: patch})
}

func (m *Manager) finishClosedRemoteSource(id string, observed beads.Bead) error {
	front := NewStore(beads.SessionStore{Store: m.store})
	current, _, _, held, err := front.readRemoteHeldSource(id)
	if err != nil {
		return err
	}
	if held || current.Status != "closed" {
		return CapacityAdmissionError("remote_close_recovery_held")
	}
	if current.Metadata[remoteAdmissionStopMetadataKey] == "" {
		return nil
	}
	if current.Metadata[remoteAdmissionStopMetadataKey] != observed.Metadata[remoteAdmissionStopMetadataKey] {
		return CapacityAdmissionError("remote_close_recovery_intent_changed")
	}
	oldShape, newShape := observed, current
	oldShape.Status = "open"
	newShape.Status = "open"
	if !sameRemoteStopSource(oldShape, newShape) {
		return CapacityAdmissionError("remote_close_recovery_source_changed")
	}
	writer, _, err := beads.ResolveConditionalWriter(beads.SessionStore{Store: m.store})
	if err != nil || writer == nil {
		return CapacityAdmissionError("remote_close_recovery_writer_unavailable")
	}
	return writer.UpdateIfMatch(id, current.Revision, beads.UpdateOpts{Metadata: remoteCloseSourcePatch(current)})
}

func remoteCloseSourcePatch(b beads.Bead) map[string]string {
	patch := map[string]string{"state": string(StateClosed), "pin_awake": "", "held_until": "", "sleep_intent": "", remoteAdmissionStopMetadataKey: ""}
	if wasConfiguredNamedSession(b) {
		for key, value := range UpdatedAliasMetadata(b.Metadata, "") {
			patch[key] = value
		}
		patch["session_name"] = ""
		patch["session_name_explicit"] = ""
		patch["pending_create_claim"] = ""
		patch["pending_create_started_at"] = ""
		freeCanonicalIdentityMetadata(patch)
	}
	return patch
}

// Early failed setup has not launched a model. A remote pin plus exact close
// CAS arbitrates against another controller's first Prepare without issuing Stop.
func (m *Manager) closeUnstartedSource(id, template string) error {
	binding, err := remoteBinding(context.Background(), m.sp, id, template)
	if err != nil {
		return err
	}
	if binding == (runtime.RuntimeBinding{}) {
		_ = m.store.Close(id) // retain legacy best-effort setup rollback
		return nil
	}
	_, err = NewStore(beads.SessionStore{Store: m.store}).rollbackRemoteCreatedSource(id, false)
	return err
}

func (m *Manager) remoteCleanupBinding(name string) (runtime.RuntimeBinding, error) {
	if resolver, ok := m.sp.(runtime.SessionRuntimeProvider); ok {
		return resolver.ResolveRuntime(name)
	}
	return runtime.RuntimeBinding{}, nil
}

func (m *Manager) remoteCloseArtifacts(id string, result *CloseResult) {
	nudgeIDs, capped, err := NewStore(beads.SessionStore{Store: m.store}).CancelWaits(id, time.Now().UTC())
	if err != nil {
		log.Printf("session %s: closing after wait cancellation lookup failed: %v", id, err)
	}
	if capped {
		log.Printf("session %s: closing after capped wait cancellation lookup", id)
	}
	result.WaitNudgeIDs = append(result.WaitNudgeIDs, nudgeIDs...)
	_ = clearRuntimeMCPServersSnapshot(m.cityPath, id)
}

// closePrunedRemoteSource resolves the immutable historical route; no missing
// lookup or policy error becomes authority for a raw source close.
func (m *Manager) closePrunedRemoteSource(b beads.Bead) (CloseResult, bool, error) {
	binding, err := m.remoteCleanupBinding(b.Metadata["session_name"])
	if err != nil {
		return CloseResult{}, true, fmt.Errorf("resolving pruned session %s: %w", b.ID, err)
	}
	if binding == (runtime.RuntimeBinding{}) {
		return CloseResult{}, false, nil
	}
	result, err := m.CloseDetailed(b.ID)
	return result, true, err
}
