package session

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/runtime"
)

const (
	remoteAdmissionHoldMetadataKey     = "remote_admission_hold_v1"
	remoteAdmissionTerminalMetadataKey = "remote_admission_terminal_v1"
	remoteAdmissionEverMetadataKey     = "remote_admission_ever_v1"
	remoteAdmissionStopMetadataKey     = "remote_admission_stop_intent_v1"
)

// These public journal facts omit InstanceToken and every opaque permit.
type remoteHoldRecord struct {
	Version            int                    `json:"version"`
	City               string                 `json:"city"`
	SessionID          string                 `json:"session_id"`
	Template           string                 `json:"template"`
	RuntimeHandle      string                 `json:"runtime_handle"`
	Generation         string                 `json:"generation"`
	Attempt            string                 `json:"attempt"`
	Binding            runtime.RuntimeBinding `json:"binding"`
	SourceCommit       string                 `json:"source_commit"`
	ManifestSHA256     string                 `json:"manifest_sha256"`
	UnitPrefix         string                 `json:"unit_prefix"`
	CgroupParent       string                 `json:"cgroup_parent"`
	ModelUID           uint32                 `json:"model_uid"`
	TransportUID       uint32                 `json:"transport_uid"`
	Phase              string                 `json:"phase"`
	AuthorityReceiptID string                 `json:"authority_receipt_id,omitempty"`
	JournalRevision    uint64                 `json:"journal_revision,omitempty"`
	Identity           RemoteVMIdentity       `json:"identity"`
}

func recordProfile(r remoteHoldRecord) RemoteAdmissionProfile {
	return RemoteAdmissionProfile{Binding: r.Binding, SourceSHA: r.SourceCommit, ManifestSHA256: r.ManifestSHA256, UnitPrefix: r.UnitPrefix, CgroupParent: r.CgroupParent, ModelUID: r.ModelUID, TransportUID: r.TransportUID}
}

func sameRemoteProfile(a, b RemoteAdmissionProfile) bool {
	return a.Binding == b.Binding && a.SourceSHA == b.SourceSHA && a.ManifestSHA256 == b.ManifestSHA256 && a.UnitPrefix == b.UnitPrefix && a.CgroupParent == b.CgroupParent && a.ModelUID == b.ModelUID && a.TransportUID == b.TransportUID
}

func validRemoteRecord(r remoteHoldRecord) bool {
	n, e := strconv.ParseUint(r.Generation, 10, 63)
	return (r.Phase == "prepare_intent" || r.Phase == string(RemoteAdmissionPrepared) || r.Phase == string(RemoteAdmissionEntered) || r.Phase == string(RemoteAdmissionExited)) && r.City != "" && r.Version == 1 && r.SessionID != "" && r.Template != "" && r.RuntimeHandle != "" && e == nil && n > 0 && strconv.FormatUint(n, 10) == r.Generation &&
		remoteHex(r.Attempt, 32) && r.Binding.Selector != "" && r.Binding.Endpoint != "" && r.Binding.ResourceDomain != "" && remoteHex(r.Binding.PolicyDigest, 64) &&
		remoteHex(r.SourceCommit, 40) && remoteHex(r.ManifestSHA256, 64) && r.ModelUID != 0 && r.TransportUID != 0 && r.ModelUID != r.TransportUID && validRemoteSystemdProfile(recordProfile(r))
}

// RemoteAdmissionHeld rereads the uncached source. Malformed/lost metadata and
// read failures hold. The ever marker is never cleared; it distinguishes a
// genuine first occurrence from disappearance of a formerly occupied attempt.
func (s *Store) RemoteAdmissionHeld(id string) (bool, error) {
	b, _, _, held, err := s.readRemoteHeldSource(id)
	return held || err != nil || b.Metadata[remoteAdmissionStopMetadataKey] != "", err
}

// HeldRemoteAdmissionRequest reconstructs the current exact request privately.
// The token is reread from the bead, never persisted in this public journal.
// held=false with a nonzero request is a current terminal tombstone. A valid
// old-generation terminal tombstone returns zero/false: it cannot authorize a
// query using the new token and does not strand the next genuine occurrence.
func (s *Store) HeldRemoteAdmissionRequest(id string) (RemoteAdmissionRequest, bool, error) {
	_, _, request, held, err := s.readRemoteHeldSource(id)
	return request, held, err
}

func (s *Store) readRemoteHeldSource(id string) (beads.Bead, remoteHoldRecord, RemoteAdmissionRequest, bool, error) {
	fail := func(err error) (beads.Bead, remoteHoldRecord, RemoteAdmissionRequest, bool, error) {
		return beads.Bead{}, remoteHoldRecord{}, RemoteAdmissionRequest{}, true, err
	}
	if s == nil || s.store.Store == nil || id == "" {
		return fail(errors.New("remote hold source unavailable"))
	}
	b, err := beads.HandlesFor(s.store.Store).Live.Get(id)
	if err != nil {
		return fail(err)
	}
	return decodeRemoteHeldBead(b, id)
}

func decodeRemoteHeldBead(b beads.Bead, id string) (beads.Bead, remoteHoldRecord, RemoteAdmissionRequest, bool, error) {
	fail := func(err error) (beads.Bead, remoteHoldRecord, RemoteAdmissionRequest, bool, error) {
		return beads.Bead{}, remoteHoldRecord{}, RemoteAdmissionRequest{}, true, err
	}

	if b.ID != id || !IsSessionBeadOrRepairable(b) {
		return fail(errors.New("remote hold source identity invalid"))
	}
	raw := b.Metadata[remoteAdmissionHoldMetadataKey]
	held := raw != ""
	if !held {
		raw = b.Metadata[remoteAdmissionTerminalMetadataKey]
	}
	if raw == "" {
		if b.Metadata[remoteAdmissionEverMetadataKey] != "" {
			return fail(errors.New("remote attempt journal missing"))
		}
		return b, remoteHoldRecord{}, RemoteAdmissionRequest{}, false, nil
	}
	var record remoteHoldRecord
	if json.Unmarshal([]byte(raw), &record) != nil || !validRemoteRecord(record) || record.SessionID != id || b.Metadata[remoteAdmissionEverMetadataKey] != "v1" {
		return fail(errors.New("remote attempt journal invalid"))
	}
	if !held {
		if record.Phase != string(RemoteAdmissionExited) || record.AuthorityReceiptID == "" || record.JournalRevision == 0 || !validRemoteReleaseIdentity(record.Identity, recordProfile(record), RemoteAdmissionRequest{Attempt: record.Attempt}, record.Identity.BootID) {
			return fail(errors.New("remote terminal receipt invalid"))
		}
		if b.Metadata["generation"] != record.Generation {
			return b, record, RemoteAdmissionRequest{}, false, nil
		}
	}
	bound, pinned, bindErr := decodeRuntimeBinding(id, b.Metadata[runtimeBindingMetadataKey])
	if bindErr != nil || !pinned || bound != record.Binding || b.Metadata["template"] != record.Template || b.Metadata["session_name"] != record.RuntimeHandle || b.Metadata["generation"] != record.Generation || !remoteHex(b.Metadata["instance_token"], 32) {
		return fail(errors.New("remote held incarnation changed"))
	}
	request := RemoteAdmissionRequest{City: "", SessionID: id, Template: record.Template, RuntimeHandle: record.RuntimeHandle, Generation: record.Generation, InstanceToken: b.Metadata["instance_token"], Attempt: record.Attempt, Binding: record.Binding}
	// City is pinned as part of the authority request below.
	request.City = record.City
	return b, record, request, held, nil
}

// stampRemoteAdmissionHold is pure CAS: no nested process-local SID mutex,
// unconditional fallback, inferred TTL release or upgrade of writer capability.
func stampRemoteAdmissionHold(store beads.Store, request RemoteAdmissionRequest, profile RemoteAdmissionProfile, fence remoteStartFence) (raw string, attempted bool, err error) {
	front := NewStore(beads.SessionStore{Store: store})
	_, _, terminalRequest, held, readErr := front.readRemoteHeldSource(request.SessionID)
	if readErr != nil || held || terminalRequest.Attempt != "" {
		return fence.Hold, false, errors.New("remote source already consumed, held or unknown")
	}
	writer, _, err := beads.ResolveConditionalWriter(beads.SessionStore{Store: store})
	if err != nil || writer == nil {
		return "", false, errors.New("remote hold conditional writer unavailable")
	}
	b, err := beads.HandlesFor(store).Live.Get(request.SessionID)
	if err != nil {
		return "", false, err
	}
	current, currentFence, err := readRemoteRequest(store, request.City, request.Template, request.SessionID, request.Binding)
	current.Attempt = request.Attempt
	if err != nil || current != request || currentFence != fence {
		return "", false, errors.New("remote hold target changed")
	}
	record := remoteHoldRecord{Version: 1, City: request.City, SessionID: request.SessionID, Template: request.Template, RuntimeHandle: request.RuntimeHandle, Generation: request.Generation, Attempt: request.Attempt, Binding: request.Binding, SourceCommit: profile.SourceSHA, ManifestSHA256: profile.ManifestSHA256, UnitPrefix: profile.UnitPrefix, CgroupParent: profile.CgroupParent, ModelUID: profile.ModelUID, TransportUID: profile.TransportUID, Phase: "prepare_intent"}
	payload, err := json.Marshal(record)
	if err != nil {
		return "", false, err
	}
	raw = string(payload)
	err = writer.UpdateIfMatch(request.SessionID, b.Revision, beads.UpdateOpts{Metadata: map[string]string{remoteAdmissionHoldMetadataKey: raw, remoteAdmissionTerminalMetadataKey: "", remoteAdmissionEverMetadataKey: "v1"}})
	return raw, true, err
}

func persistRemoteReceipt(proof *remoteAdmissionProof, receipt RemoteAdmissionReceipt) error {
	request := proof.reservation.Request
	b, record, current, held, err := NewStore(beads.SessionStore{Store: proof.store}).readRemoteHeldSource(request.SessionID)
	if err != nil || !held || current != request || b.Metadata[remoteAdmissionHoldMetadataKey] != proof.fence.Hold {
		return errors.New("remote receipt source changed")
	}
	record.Phase = string(receipt.Phase)
	record.AuthorityReceiptID = receipt.AuthorityReceiptID
	record.JournalRevision = receipt.JournalRevision
	record.Identity = receipt.Identity
	payload, err := json.Marshal(record)
	if err != nil {
		return err
	}
	writer, _, err := beads.ResolveConditionalWriter(beads.SessionStore{Store: proof.store})
	if err != nil || writer == nil {
		return errors.New("remote receipt conditional writer unavailable")
	}
	raw := string(payload)
	if err = writer.UpdateIfMatch(request.SessionID, b.Revision, beads.UpdateOpts{Metadata: map[string]string{remoteAdmissionHoldMetadataKey: raw}}); err != nil {
		return err
	}
	proof.fence.Hold = raw
	return nil
}

func validRemoteReleaseIdentity(identity RemoteVMIdentity, profile RemoteAdmissionProfile, request RemoteAdmissionRequest, boot string) bool {
	scope := identity.ScopeIdentity
	if !validRemoteProfileIdentity(identity, profile) || identity.BootID != boot || !identity.ScopeEmpty ||
		scope.BootID != boot || scope.CgroupPath != profile.CgroupParent || scope.Device == 0 || scope.Inode == 0 {
		return false
	}
	if identity.NeverEntered {
		return identity.Unit == "" && identity.InvocationID == "" && identity.CgroupPath == "" && identity.CgroupDevice == 0 && identity.CgroupInode == 0
	}
	return identity.Unit == remoteExpectedUnit(profile, request) && identity.CgroupPath == profile.CgroupParent+"/"+identity.Unit && remoteHex(identity.InvocationID, 32) && identity.CgroupDevice > 0 && identity.CgroupInode > 0
}

// ReconcileRemoteAdmissionHold may clear only an exact current attempt after an
// explicit registered VM authority proves terminality AND fresh recursive scope
// emptiness. Historical child identity survives process exit; no live PID is
// required. NeverEntered additionally attests durable revocation before consume.
// Nil authority, ambiguity, changed incarnation or lost CAS leaves the hold.
func (s *Store) ReconcileRemoteAdmissionHold(ctx context.Context, id string) error {
	b, record, request, held, err := s.readRemoteHeldSource(id)
	if err != nil || !held {
		return CapacityAdmissionError("remote_reconciliation_not_held")
	}
	registry, _ := ctx.Value(remoteAdmissionProfilesKey{}).(remoteAdmissionProfiles)
	profile, ok := registry[record.Binding]
	if !ok || profile.Reconcile == nil || !sameRemoteProfile(profile, recordProfile(record)) {
		return CapacityAdmissionError("remote_reconciliation_authority_unavailable")
	}
	receipt, err := profile.Reconcile(ctx, request)
	knownBoot := record.Identity.BootID
	if knownBoot == "" {
		knownBoot = receipt.Identity.BootID
	}
	if err != nil || ctx.Err() != nil || receipt.Request != request || receipt.Phase != RemoteAdmissionExited ||
		receipt.AuthorityReceiptID == "" || (record.AuthorityReceiptID != "" && record.AuthorityReceiptID != receipt.AuthorityReceiptID) ||
		receipt.JournalRevision == 0 || receipt.JournalRevision < record.JournalRevision ||
		(record.Phase != string(RemoteAdmissionExited) && receipt.JournalRevision == record.JournalRevision) ||
		(receipt.Identity.NeverEntered && (record.Phase == string(RemoteAdmissionEntered) || record.Phase == string(RemoteAdmissionExited) && !record.Identity.NeverEntered)) ||
		!remoteHistoricalIdentityMatches(record.Identity, receipt.Identity) ||
		!validRemoteReleaseIdentity(receipt.Identity, profile, request, knownBoot) {
		return CapacityAdmissionError("remote_reconciliation_unknown_hold")
	}
	record.Phase = string(RemoteAdmissionExited)
	record.AuthorityReceiptID = receipt.AuthorityReceiptID
	record.JournalRevision = receipt.JournalRevision
	record.Identity = receipt.Identity
	payload, err := json.Marshal(record)
	if err != nil {
		return err
	}
	writer, _, err := beads.ResolveConditionalWriter(s.store)
	if err != nil || writer == nil {
		return CapacityAdmissionError("remote_reconciliation_writer_unavailable")
	}
	// Exact revision includes the current token and entire held record. Any write
	// during the observer call wins over this clear; reconciliation never retries.
	err = writer.UpdateIfMatch(id, b.Revision, beads.UpdateOpts{Metadata: map[string]string{remoteAdmissionHoldMetadataKey: "", remoteAdmissionTerminalMetadataKey: string(payload)}})
	if err != nil {
		return CapacityAdmissionError("remote_reconciliation_cas_changed")
	}
	return nil
}

func remoteUnfencedCloseUnsafe(b beads.Bead) bool {
	return remoteHeldBeadBlocks(b) || b.Metadata[runtimeBindingMetadataKey] != ""
}

func remotePatchChangesIdentity(patch MetadataPatch) bool {
	for _, key := range []string{"session_name", "session_name_explicit", "alias", "aliases", "template", "generation", "instance_token", runtimeBindingMetadataKey, remoteAdmissionHoldMetadataKey, remoteAdmissionEverMetadataKey, remoteAdmissionTerminalMetadataKey, remoteAdmissionStopMetadataKey} {
		if _, exists := patch[key]; exists {
			return true
		}
	}
	return false
}

// rollbackRemoteCreatedSource fences failed public Create against another
// controller's first reservation. Close wins before identifier clearing, or the
// first hold wins and this path leaves every identifier untouched.
func (s *Store) rollbackRemoteCreatedSource(id string, explicit bool) (bool, error) {
	b, err := beads.HandlesFor(s.store.Store).Live.Get(id)
	if err != nil {
		return true, err
	}
	if b.Metadata[runtimeBindingMetadataKey] == "" && b.Metadata[remoteAdmissionHoldMetadataKey] == "" && b.Metadata[remoteAdmissionEverMetadataKey] == "" {
		return false, nil
	}
	held, holdErr := s.RemoteAdmissionHeld(id)
	if holdErr != nil || held {
		return true, &remoteSourceEffectError{cause: CapacityAdmissionError("remote_source_reconciliation_required")}
	}
	writer, _, err := beads.ResolveConditionalWriter(s.store)
	if err != nil || writer == nil {
		return true, CapacityAdmissionError("remote_rollback_writer_unavailable")
	}
	if err = writer.CloseIfMatch(id, b.Revision); err != nil {
		return true, CapacityAdmissionError("remote_rollback_revision_changed")
	}
	if explicit {
		_, err = s.applyPatchIfClosed(id, MetadataPatch{"session_name": "", "session_name_explicit": ""})
	}
	return true, err
}

func remoteHeldBeadBlocks(b beads.Bead) bool {
	if b.Metadata[remoteAdmissionStopMetadataKey] != "" {
		return true
	}
	if b.Metadata[remoteAdmissionHoldMetadataKey] == "" && b.Metadata[remoteAdmissionEverMetadataKey] == "" && b.Metadata[remoteAdmissionTerminalMetadataKey] == "" {
		return false
	}
	_, _, _, held, err := decodeRemoteHeldBead(b, b.ID)
	return held || err != nil
}

// Compare all previously attested kernel identities. A same-path replacement
// parent or a new invocation of the same unit is not the original authority scope.
func remoteHistoricalIdentityMatches(known, current RemoteVMIdentity) bool {
	if known.ScopeIdentity != (RemoteScopeIdentity{}) && current.ScopeIdentity != known.ScopeIdentity {
		return false
	}
	if known.InvocationID != "" {
		return current.BootID == known.BootID && current.Unit == known.Unit &&
			current.InvocationID == known.InvocationID && current.CgroupPath == known.CgroupPath &&
			current.CgroupDevice == known.CgroupDevice && current.CgroupInode == known.CgroupInode
	}
	return true
}

// A remote stop must win a source CAS before consulting the VM. In particular,
// a proven-virgin VM Stop cannot race a new Prepare into the suspension window.
// An unknown Stop retains this durable barrier without a TTL.
func (m *Manager) suspendRemoteSource(id, name string, observed beads.Bead, binding runtime.RuntimeBinding) error {
	if observed.Status == "closed" {
		return &IllegalTransitionError{From: StateClosed, Command: CmdSuspend}
	}
	current := canonicalLifecycleState(State(observed.Metadata["state"]))
	if current != StateSuspended {
		if _, err := Transition(current, CmdSuspend); err != nil {
			return err
		}
	}
	writer, _, err := beads.ResolveConditionalWriter(beads.SessionStore{Store: m.store})
	if err != nil || writer == nil {
		return CapacityAdmissionError("remote_stop_writer_unavailable")
	}
	front := NewStore(beads.SessionStore{Store: m.store})
	if err := front.PinRuntimeBinding(id, binding); err != nil {
		return err
	}
	b, err := beads.HandlesFor(m.store).Live.Get(id)
	if err != nil {
		return err
	}
	bound, pinned, err := decodeRuntimeBinding(id, b.Metadata[runtimeBindingMetadataKey])
	if err != nil || !pinned || bound != binding {
		return CapacityAdmissionError("remote_stop_binding_changed")
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
		return CapacityAdmissionError("remote_stop_source_changed")
	}
	intent := NewInstanceToken()
	if err := writer.UpdateIfMatch(id, b.Revision, beads.UpdateOpts{Metadata: map[string]string{remoteAdmissionStopMetadataKey: intent}}); err != nil {
		return CapacityAdmissionError("remote_stop_intent_cas_changed")
	}
	if err := m.sp.Stop(name); err != nil {
		return err
	}
	after, err := beads.HandlesFor(m.store).Live.Get(id)
	if err != nil {
		return err
	}
	if !sameRemoteStopSource(b, after) || after.Metadata[remoteAdmissionStopMetadataKey] != intent {
		return CapacityAdmissionError("remote_stop_source_changed")
	}
	now := m.now().UTC()
	patch := map[string]string{
		"state": string(StateSuspended), "held_until": now.Add(indefiniteSuspendHoldDuration).Format(time.RFC3339),
		"sleep_intent": "user-hold", "slept_at": "", "sleep_reason": "", "suspended_at": now.Format(time.RFC3339), remoteAdmissionStopMetadataKey: "",
	}
	if err := writer.UpdateIfMatch(id, after.Revision, beads.UpdateOpts{Metadata: patch}); err != nil {
		return CapacityAdmissionError("remote_stop_completion_cas_changed")
	}
	return nil
}

func sameRemoteStopSource(a, b beads.Bead) bool {
	if a.ID != b.ID || a.Status != b.Status || b.Status == "closed" {
		return false
	}
	ignored := func(key string) bool {
		return key == remoteAdmissionStopMetadataKey || key == remoteAdmissionHoldMetadataKey ||
			key == remoteAdmissionTerminalMetadataKey || key == remoteAdmissionEverMetadataKey
	}
	// VM cancellation/reconciliation may advance only the authority journal.
	// Every other concurrent metadata/lifecycle decision wins over completion.
	for key, value := range a.Metadata {
		if !ignored(key) && b.Metadata[key] != value {
			return false
		}
	}
	for key, value := range b.Metadata {
		if !ignored(key) && a.Metadata[key] != value {
			return false
		}
	}
	return true
}
