package session

import (
	"context"
	"errors"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/pathutil"
	"github.com/gastownhall/gascity/internal/runtime"
)

// RemoteAdmissionPhase is an authority-observed VM attempt state.
type RemoteAdmissionPhase string

// Remote VM phases are strict: missing or unknown evidence never confirms entry.
const (
	RemoteAdmissionPrepared RemoteAdmissionPhase = "prepared"
	RemoteAdmissionEntered  RemoteAdmissionPhase = "entered"
	RemoteAdmissionExited   RemoteAdmissionPhase = "exited"
	RemoteAdmissionUnknown  RemoteAdmissionPhase = "unknown"
)

// RemoteAdmissionRequest fences one immutable session incarnation and attempt.
// Generation is the canonical positive decimal string from the live store.
type RemoteAdmissionRequest struct {
	City, SessionID, Template, RuntimeHandle, Generation, InstanceToken, Attempt string
	Binding                                                                      runtime.RuntimeBinding
}

// RemoteVMIdentity carries root-profile pins and observed kernel identity.
// SourceSHA is a 40-hex Git commit, ManifestSHA256 a 64-hex byte digest.
type RemoteVMIdentity struct {
	BootID, Unit, InvocationID, CgroupPath, SourceSHA, ManifestSHA256 string
	CgroupDevice, CgroupInode                                         uint64
	ModelUID, TransportUID                                            uint32
}

// RemoteAdmissionReceipt is VM authority evidence, never an opaque permit.
type RemoteAdmissionReceipt struct {
	Request            RemoteAdmissionRequest
	AuthorityReceiptID string
	JournalRevision    uint64
	Phase              RemoteAdmissionPhase
	Identity           RemoteVMIdentity
}

// RemoteBeginReceipt records controller begin intent, not VM consumption.
type RemoteBeginReceipt struct {
	Reservation RemoteAdmissionReceipt
	IntentID    string
}

// RemoteAdmissionProfile registers a trusted exact VM authority for this call.
// Callbacks own durable journals and reconciliation. Prepare must not replace
// an occupied unknown attempt. Begin journals controller intent only; the VM
// permit is consumed inside the future provider transport, not by this API.
// Opaque permits stay private to that backend/transport, never in receipts.
type RemoteAdmissionProfile struct {
	UnitPrefix, CgroupParent  string
	Binding                   runtime.RuntimeBinding
	SourceSHA, ManifestSHA256 string
	ModelUID, TransportUID    uint32
	Prepare                   func(context.Context, RemoteAdmissionRequest) (RemoteAdmissionReceipt, error)
	Begin                     func(context.Context, RemoteAdmissionReceipt) (RemoteBeginReceipt, error)
	Finish                    func(context.Context, RemoteBeginReceipt, error) (RemoteAdmissionReceipt, error)
}

type (
	remoteAdmissionProfilesKey struct{}
	remoteAdmissionProfiles    map[runtime.RuntimeBinding]RemoteAdmissionProfile
)

// WithRemoteAdmissionProfiles copies an immutable registry into this invocation.
// There is deliberately no default backend, global registry or env authority.
func WithRemoteAdmissionProfiles(ctx context.Context, profiles ...RemoteAdmissionProfile) (context.Context, error) {
	registry := make(remoteAdmissionProfiles, len(profiles))
	for _, p := range profiles {
		if p.Binding.Selector == "" || p.Binding.Endpoint == "" || p.Binding.ResourceDomain == "" ||
			!remoteHex(p.Binding.PolicyDigest, 64) || !remoteHex(p.SourceSHA, 40) || !remoteHex(p.ManifestSHA256, 64) ||
			p.ModelUID == 0 || p.TransportUID == 0 || p.ModelUID == p.TransportUID ||
			!validRemoteSystemdProfile(p) ||
			p.Prepare == nil || p.Begin == nil || p.Finish == nil {
			return ctx, errors.New("remote admission profile invalid")
		}
		if _, exists := registry[p.Binding]; exists {
			return ctx, errors.New("remote admission profile duplicate")
		}
		registry[p.Binding] = p
	}
	return context.WithValue(ctx, remoteAdmissionProfilesKey{}, registry), nil
}

type remoteAdmissionProof struct {
	profile     RemoteAdmissionProfile
	reservation RemoteAdmissionReceipt
	store       beads.Store
	fence       remoteStartFence
}

func remoteHex(value string, size int) bool {
	if len(value) != size {
		return false
	}
	for _, r := range value {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return true
}

func remoteBinding(ctx context.Context, sp runtime.Provider, id, route string) (runtime.RuntimeBinding, error) {
	if resolver, ok := sp.(runtime.SessionRuntimeProvider); ok {
		return resolver.BindRuntime(ctx, id, route)
	}
	return runtime.RuntimeBinding{}, nil
}

// Read the uncached store, including raw incarnation fields: codec defaults
// must not turn a missing generation or provider handle into launch authority.
func readRemoteRequest(store beads.Store, city, route, id string, binding runtime.RuntimeBinding) (RemoteAdmissionRequest, remoteStartFence, error) {
	if store == nil || id == "" {
		return RemoteAdmissionRequest{}, remoteStartFence{}, errors.New("remote identity unavailable")
	}
	b, err := beads.HandlesFor(store).Live.Get(id)
	if err != nil || b.ID != id || !IsSessionBeadOrRepairable(b) || b.Status == "closed" ||
		b.Metadata["template"] != route || route == "" || b.Metadata["session_name"] == "" {
		return RemoteAdmissionRequest{}, remoteStartFence{}, errors.New("remote identity unavailable")
	}
	if KillPendingMetadata(b.Metadata["state"], b.Metadata["state_reason"], b.Metadata["sleep_reason"], b.Metadata["slept_at"], time.Now().UTC()) {
		return RemoteAdmissionRequest{}, remoteStartFence{}, errors.New("remote start kill fenced")
	}
	generation := b.Metadata["generation"]
	n, err := strconv.ParseUint(generation, 10, 63)
	if err != nil || n == 0 || strconv.FormatUint(n, 10) != generation || !remoteHex(b.Metadata["instance_token"], 32) {
		return RemoteAdmissionRequest{}, remoteStartFence{}, errors.New("remote incarnation unavailable")
	}
	return RemoteAdmissionRequest{City: city, SessionID: id, Template: route, RuntimeHandle: b.Metadata["session_name"], Generation: generation, InstanceToken: b.Metadata["instance_token"], Binding: binding}, remoteFenceFromMetadata(b.Metadata), nil
}

func admitRemoteCapacity(ctx context.Context, city, route, id string, now time.Time, binding runtime.RuntimeBinding, store beads.Store) (context.Context, bool, string) {
	if existing, ok := ctx.Value(capacityAdmissionKey{}).(*capacityAdmissionProof); ok {
		if existing.remote == nil || !pathutil.SamePath(existing.city, city) || existing.route != route || existing.id != id || existing.used.Load() {
			return ctx, false, "remote_admission_proof_invalid"
		}
		r := existing.remote.reservation.Request
		current, fence, err := readRemoteRequest(existing.remote.store, r.City, route, id, binding)
		current.Attempt = r.Attempt
		if err != nil || current != r || fence != existing.remote.fence {
			return ctx, false, "remote_admission_identity_changed"
		}
		return ctx, true, "remote_admission_prepared"
	}
	registry, _ := ctx.Value(remoteAdmissionProfilesKey{}).(remoteAdmissionProfiles)
	profile, ok := registry[binding]
	if !ok || binding == (runtime.RuntimeBinding{}) {
		return ctx, false, "remote_admission_backend_unavailable"
	}
	request, fence, err := readRemoteRequest(store, city, route, id, binding)
	if err != nil || ctx.Err() != nil {
		return ctx, false, "remote_admission_identity_invalid"
	}
	request.Attempt = NewInstanceToken()
	ctx = context.WithValue(ctx, remoteSourceEffectKey{}, request)
	receipt, err := profile.Prepare(ctx, request)
	if err != nil || receipt.Request != request || receipt.AuthorityReceiptID == "" || receipt.JournalRevision == 0 ||
		receipt.Phase != RemoteAdmissionPrepared || !validRemoteProfileIdentity(receipt.Identity, profile) {
		// Failure is a hold, never permission to fall through or cancel an attempt.
		return ctx, false, "remote_admission_prepare_unknown"
	}
	proof := &capacityAdmissionProof{city: city, route: route, id: id, minted: now, remote: &remoteAdmissionProof{profile: profile, reservation: receipt, store: store, fence: fence}}
	ctx = context.WithValue(ctx, capacityAdmissionKey{}, proof)
	if !remoteProofCurrent(proof.remote) {
		return ctx, false, "remote_admission_target_changed"
	}
	return ctx, true, "remote_admission_prepared"
}

func validRemoteProfileIdentity(identity RemoteVMIdentity, profile RemoteAdmissionProfile) bool {
	return remoteHex(identity.BootID, 32) && identity.SourceSHA == profile.SourceSHA &&
		identity.ManifestSHA256 == profile.ManifestSHA256 && identity.ModelUID == profile.ModelUID && identity.TransportUID == profile.TransportUID
}

func remoteProofCurrent(proof *remoteAdmissionProof) bool {
	r := proof.reservation.Request
	current, fence, err := readRemoteRequest(proof.store, r.City, r.Template, r.SessionID, r.Binding)
	current.Attempt = r.Attempt
	return err == nil && current == r && fence == proof.fence
}

func beginRemoteCapacityStart(ctx context.Context, proof *remoteAdmissionProof, name string, cfg runtime.Config) (RemoteBeginReceipt, error) {
	r := proof.reservation.Request
	if ctx.Err() != nil || name != r.RuntimeHandle || cfg.Command == "" || cfg.CommandScope != nil ||
		cfg.Env["GC_SESSION_ID"] != r.SessionID || cfg.Env["GC_TEMPLATE"] != r.Template ||
		cfg.Env["GC_SESSION_NAME"] != r.RuntimeHandle || cfg.Env["GC_RUNTIME_EPOCH"] != r.Generation ||
		cfg.Env["GC_INSTANCE_TOKEN"] != r.InstanceToken || !remoteProofCurrent(proof) {
		return RemoteBeginReceipt{}, errors.New("remote_admission_begin_identity_changed")
	}
	begin, err := proof.profile.Begin(ctx, proof.reservation)
	if err != nil || begin.Reservation != proof.reservation || begin.IntentID == "" || !remoteProofCurrent(proof) || ctx.Err() != nil {
		return RemoteBeginReceipt{}, errors.New("remote_admission_begin_unknown")
	}
	return begin, nil
}

func finishRemoteCapacityStart(ctx context.Context, proof *remoteAdmissionProof, begin RemoteBeginReceipt, startErr error) error {
	receipt, err := proof.profile.Finish(ctx, begin, startErr)
	previous := proof.reservation
	identity := receipt.Identity
	if err != nil || ctx.Err() != nil || receipt.Request != previous.Request ||
		receipt.AuthorityReceiptID != previous.AuthorityReceiptID || receipt.JournalRevision <= previous.JournalRevision ||
		(receipt.Phase != RemoteAdmissionEntered && receipt.Phase != RemoteAdmissionExited) ||
		!validRemoteProfileIdentity(identity, proof.profile) || identity.BootID != previous.Identity.BootID ||
		identity.Unit != remoteExpectedUnit(proof.profile, previous.Request) ||
		identity.CgroupPath != proof.profile.CgroupParent+"/"+identity.Unit ||
		!remoteHex(identity.InvocationID, 32) || !path.IsAbs(identity.CgroupPath) || path.Clean(identity.CgroupPath) != identity.CgroupPath ||
		identity.CgroupPath == "/" || identity.CgroupDevice == 0 || identity.CgroupInode == 0 {
		return errors.New("remote_admission_finish_unknown_hold")
	}
	return nil
}

// remoteStartFence binds lifecycle launch intent independently of incarnation.
// Unrelated continuation/prompt changes do not revoke a reservation.
type remoteStartFence struct {
	State, Reason, SleepReason, SleptAt, SleepIntent, HeldUntil, PendingCreate, PendingStarted string
}

func remoteFenceFromMetadata(meta map[string]string) remoteStartFence {
	return remoteStartFence{meta["state"], meta["state_reason"], meta["sleep_reason"], meta["slept_at"], meta["sleep_intent"], meta["held_until"], meta["pending_create_claim"], meta["pending_create_started_at"]}
}

type remoteSourceEffectKey struct{}

// A callback may have a durable effect even when its response was lost. Keep
// the source identity and private authority reachable for reconciliation.
func remoteSourceEffect(ctx context.Context) bool {
	if _, ok := ctx.Value(remoteSourceEffectKey{}).(RemoteAdmissionRequest); ok {
		return true
	}
	proof, _ := ctx.Value(capacityAdmissionKey{}).(*capacityAdmissionProof)
	return proof != nil && proof.remote != nil
}

type remoteSourceEffectError struct{ cause error }

func (*remoteSourceEffectError) Error() string {
	return "remote attempt retained for authoritative reconciliation"
}
func (e *remoteSourceEffectError) Unwrap() error { return e.cause }
func remoteSourceHold(ctx context.Context, cause error) error {
	if cause == nil || !remoteSourceEffect(ctx) {
		return cause
	}
	return &remoteSourceEffectError{cause: errors.Join(CapacityAdmissionError("remote_admission_reconciliation_required"), cause)}
}

// This slice accepts only root-controlled systemd service cgroups. A future
// backend with a different layout needs an explicit separately reviewed policy.
func validRemoteSystemdProfile(profile RemoteAdmissionProfile) bool {
	if len(profile.UnitPrefix) == 0 || len(profile.UnitPrefix) > 64 || profile.UnitPrefix[0] == '-' || strings.HasSuffix(profile.UnitPrefix, "-") {
		return false
	}
	for _, r := range profile.UnitPrefix {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '-' {
			return false
		}
	}
	parent := profile.CgroupParent
	if !path.IsAbs(parent) || path.Clean(parent) != parent || parent == "/" {
		return false
	}
	for _, part := range strings.Split(strings.TrimPrefix(parent, "/"), "/") {
		if !strings.HasSuffix(part, ".slice") || strings.ContainsAny(part, "\x00\r\n") {
			return false
		}
	}
	return true
}

func remoteExpectedUnit(profile RemoteAdmissionProfile, request RemoteAdmissionRequest) string {
	return profile.UnitPrefix + "-" + request.Attempt + ".service"
}

// IsRemoteAdmissionHeld identifies a possibly occupied remote attempt whose
// source session must survive until authoritative reconciliation. It exposes
// no fencing tokens or private authority material.
func IsRemoteAdmissionHeld(err error) bool {
	var held *remoteSourceEffectError
	return errors.As(err, &held)
}

// RemoteAdmissionMayHaveEffect reports whether admission callbacks may have
// durably reserved this invocation's remote attempt, even after a lost reply.
// It is not evidence that the remote process entered or that capacity is free.
func RemoteAdmissionMayHaveEffect(ctx context.Context) bool {
	return ctx != nil && remoteSourceEffect(ctx)
}
