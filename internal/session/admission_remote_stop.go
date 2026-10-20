package session

import (
	"context"
	"time"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/runtime"
)

type remoteStoppedSource struct {
	source beads.Bead
	intent string
	writer beads.ConditionalWriter
}

// stopRemoteSource is shared by remote suspend/close. Every VM effect follows
// the durable source intent; only explicit authority reconciliation releases
// an occupied attempt. Unknowns retain both journals and the stop intent.
func (m *Manager) stopRemoteSource(id, name string, observed beads.Bead, binding runtime.RuntimeBinding) (remoteStoppedSource, error) {
	writer, _, err := beads.ResolveConditionalWriter(beads.SessionStore{Store: m.store})
	if err != nil || writer == nil {
		return remoteStoppedSource{}, CapacityAdmissionError("remote_stop_writer_unavailable")
	}
	front := NewStore(beads.SessionStore{Store: m.store})
	if err := front.PinRuntimeBinding(id, binding); err != nil {
		return remoteStoppedSource{}, err
	}
	b, err := beads.HandlesFor(m.store).Live.Get(id)
	if err != nil {
		return remoteStoppedSource{}, err
	}
	bound, pinned, err := decodeRuntimeBinding(id, b.Metadata[runtimeBindingMetadataKey])
	if err != nil || !pinned || bound != binding {
		return remoteStoppedSource{}, CapacityAdmissionError("remote_stop_binding_changed")
	}
	// ResolveRuntime may select an as-yet unpinned virgin source. The canonical
	// write-once pin above closes that window before Stop; it never changes an
	// existing pin. Compare every original incarnation field as well.
	originalPin := observed.Metadata[runtimeBindingMetadataKey]
	if originalPin == "" {
		copyMeta := make(map[string]string, len(observed.Metadata))
		for key, value := range observed.Metadata {
			copyMeta[key] = value
		}
		copyMeta[runtimeBindingMetadataKey] = b.Metadata[runtimeBindingMetadataKey]
		observed.Metadata = copyMeta
	}
	if !sameRemoteStopSource(observed, b) {
		return remoteStoppedSource{}, CapacityAdmissionError("remote_stop_source_changed")
	}
	intent := NewInstanceToken()
	if err := writer.UpdateIfMatch(id, b.Revision, beads.UpdateOpts{Metadata: map[string]string{remoteAdmissionStopMetadataKey: intent}}); err != nil {
		return remoteStoppedSource{}, CapacityAdmissionError("remote_stop_intent_cas_changed")
	}
	if err := m.sp.Stop(name); err != nil {
		return remoteStoppedSource{}, err
	}
	_, _, _, held, readErr := front.readRemoteHeldSource(id)
	if readErr != nil {
		return remoteStoppedSource{}, readErr
	}
	if held {
		reconcileCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		reconcileCtx, profileErr := ContextWithProviderRemoteAdmission(reconcileCtx, m.sp)
		if profileErr == nil {
			profileErr = front.ReconcileRemoteAdmissionHold(reconcileCtx, id)
		}
		cancel()
		if profileErr != nil {
			return remoteStoppedSource{}, profileErr
		}
	}
	after, err := beads.HandlesFor(m.store).Live.Get(id)
	if err != nil {
		return remoteStoppedSource{}, err
	}
	if !sameRemoteStopSource(b, after) || after.Metadata[remoteAdmissionStopMetadataKey] != intent {
		return remoteStoppedSource{}, CapacityAdmissionError("remote_stop_source_changed")
	}

	return remoteStoppedSource{source: after, intent: intent, writer: writer}, nil
}
