package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"path/filepath"
	"reflect"
	"time"

	"github.com/gastownhall/gascity/internal/runtime"
	"github.com/gastownhall/gascity/internal/runtime/observation"
	"github.com/gastownhall/gascity/internal/runtime/procobserver"
)

// This separate consumer retains BOTH full, provisional helper frames. Only
// the unchanged ordinary provider join may promote the outer disposition.
// These callable seams register no command, handler, selector or rollout.
type controllerObservationReplyV3 struct {
	controllerObservationReply
	ProcessDiagnostics []procobserver.ResponseV3 `json:"process_diagnostics"`
}

// Two bounded helper frames plus the existing bounded outer observation. A
// full frame cannot be silently trimmed to fit the legacy controller limit.
const controllerObservationV3Limit = 2*procobserver.MaxResponseBytes + controllerObservationLimit

func unknownControllerObservationV3(ctx context.Context, city, source, reason string) controllerObservationReplyV3 {
	base := newControllerObservationService(ctx, city).unknown(reason)
	base.Schema = observation.SchemaV3
	base.CertificateDisposition = "provisional"
	base.SourceRevision = source
	return controllerObservationReplyV3{controllerObservationReply: base, ProcessDiagnostics: []procobserver.ResponseV3{}}
}

// collectControllerObservationV3 is an inert single-attempt seam. A future
// registered service must retain the existing global flight/generation guards;
// this function neither starts a service nor changes those active guards.
func collectControllerObservationV3(ctx context.Context, city string, p procobserver.ReleasePolicyV3, sp runtime.Provider, read func(context.Context, procobserver.ReleasePolicyV3) (procobserver.ResponseV3, error), now func() time.Time) controllerObservationReplyV3 {
	frames := []procobserver.ResponseV3{}
	observed := observation.ObserveProcessEvidenceContext(ctx, city, sp, func() observation.ProcessEvidence {
		frame, err := read(ctx, p)
		// Re-decode injected reads too: pins cannot be bypassed by a typed fake,
		// and retained nested slices belong to the immutable producer frame.
		data, marshalErr := json.Marshal(frame)
		checked, decodeErr := procobserver.DecodeResponseV3(data, p, frame.RequestNonce, now().UTC())
		err = errors.Join(err, marshalErr, decodeErr)
		if checked.Schema == procobserver.ResponseSchemaV3 {
			frames = append(frames, checked)
		}
		anchors, anchorErr := checked.RetirementAnchors()
		return observation.ProcessEvidence{Roots: checked.ObservedRoots(), StartedAt: checked.StartedAt, FinishedAt: checked.FinishedAt, Err: errors.Join(err, anchorErr), CensusContract: procobserver.CensusV3Schema, RetirementAnchors: anchors}
	}, now)
	r := unknownControllerObservationV3(ctx, city, p.HelperSourceRevision, "")
	r.Observation = observed
	r.Schema = observation.SchemaV3
	if r.CertificateDisposition == "" {
		r.CertificateDisposition = "provisional"
	}
	r.ProcessDiagnostics = frames
	r.ControllerPID = p.CallerBinding.PID
	r.ControllerBinarySHA256 = p.CallerBinding.ControllerBinarySHA256
	r.ControllerStartIdentity = p.CallerBinding.StartTicks
	r.ControllerBootID = p.CallerBinding.BootID
	r.HelperBinarySHA256 = p.HelperBinarySHA256
	r.HelperPolicyDigest = p.PolicyDigest
	if r.ProviderComplete && r.ProcessComplete {
		r.ControllerBinding = "verified_local_process"
	}
	return r
}

func validateControllerObservationReplyV3(r controllerObservationReplyV3, p procobserver.ReleasePolicyV3, city, source string, now time.Time) error {
	if p.EvidenceSchema != procobserver.ResponseSchemaV3 || !controllerObservationHex(source, 40) || p.HelperSourceRevision != source || p.CallerBinding.ControllerSourceRevision != source ||
		r.Schema != observation.SchemaV3 || r.CityPath != filepath.Clean(city) || r.SourceRevision != source {
		return fmt.Errorf("controller v3 source, selector or city mismatch")
	}
	if r.Sessions == nil || r.Processes == nil || r.UnknownReasons == nil || r.ProcessDiagnostics == nil || r.ControllerPID <= 1 || len(r.ProcessDiagnostics) > 2 || r.ProcessComplete && len(r.ProcessDiagnostics) != 2 {
		return fmt.Errorf("controller v3 fields invalid")
	}
	if r.ObservedAt.IsZero() || r.ProcessObservedAt.IsZero() || r.FinishedAt.IsZero() || r.ProcessObservedAt.Before(r.ObservedAt) || r.ProcessObservedAt.After(r.FinishedAt) || r.FinishedAt.Before(r.ObservedAt) || r.FinishedAt.After(now) || now.Sub(r.ObservedAt) > 60*time.Second || r.FinishedAt.Sub(r.ObservedAt) > 60*time.Second {
		return fmt.Errorf("controller v3 interval invalid or stale")
	}
	complete := r.ProviderComplete && r.ProcessComplete
	if complete && (len(r.ProcessDiagnostics) != 2 || r.ControllerBinding != "verified_local_process" || r.CertificateDisposition != "provider_verified" || len(r.UnknownReasons) != 0) {
		return fmt.Errorf("controller v3 provider authority unverified")
	}
	if !complete && (r.CertificateDisposition != "provisional" || len(r.UnknownReasons) == 0) {
		return fmt.Errorf("controller v3 partial disposition invalid")
	}
	if len(r.ProcessDiagnostics) > 0 || complete {
		if r.ControllerPID != p.CallerBinding.PID || r.ControllerStartIdentity != p.CallerBinding.StartTicks || r.ControllerBinarySHA256 != p.CallerBinding.ControllerBinarySHA256 || r.ControllerBootID != p.CallerBinding.BootID || r.HelperBinarySHA256 != p.HelperBinarySHA256 || r.HelperPolicyDigest != p.PolicyDigest {
			return fmt.Errorf("controller v3 reply disagrees with approved pins")
		}
	}
	for i, frame := range r.ProcessDiagnostics {
		data, err := json.Marshal(frame)
		checked, decodeErr := procobserver.DecodeResponseV3(data, p, frame.RequestNonce, now)
		if err != nil || checked.Schema != procobserver.ResponseSchemaV3 || complete && decodeErr != nil || r.ProcessComplete && decodeErr != nil || frame.StartedAt.Before(r.ObservedAt) || frame.FinishedAt.After(r.FinishedAt) {
			return fmt.Errorf("controller v3 helper frame invalid")
		}
		if i > 0 && (frame.RequestNonce == r.ProcessDiagnostics[i-1].RequestNonce || frame.StartedAt.Before(r.ProcessDiagnostics[i-1].FinishedAt)) {
			return fmt.Errorf("controller v3 helper replay or interval overlap")
		}
	}
	if !complete {
		return nil
	}
	if !r.ProcessObservedAt.Equal(r.ProcessDiagnostics[0].StartedAt) || !reflect.DeepEqual(r.ProcessDiagnostics[0].ObservedRoots(), r.ProcessDiagnostics[1].ObservedRoots()) {
		return fmt.Errorf("controller v3 helper observations disagree")
	}
	// Validate the producer's ordinary positive join without manufacturing a
	// new provider observation. Exact live PID ownership must match BOTH frames.
	seenPID := map[int]bool{}
	seenSID := map[string]bool{}
	for _, process := range r.Processes {
		if seenPID[process.PID] || seenSID[process.SessionID] {
			return fmt.Errorf("controller v3 duplicate provider identity")
		}
		seenPID[process.PID] = true
		seenSID[process.SessionID] = true
		if process.CityPath != r.CityPath || !controllerV3OwnedSession(process, r.Sessions) {
			return fmt.Errorf("controller v3 process has no provider incarnation")
		}
		for _, frame := range r.ProcessDiagnostics {
			matched := false
			for _, root := range frame.Roots {
				if controllerV3ProcessMatches(process, root) {
					matched = true
					break
				}
			}
			if !matched {
				return fmt.Errorf("controller v3 provider process absent from helper frame")
			}
		}
	}
	for _, session := range r.Sessions {
		if !session.Running || !seenSID[session.SessionID] {
			return fmt.Errorf("controller v3 live provider handle lacks process")
		}
	}
	if len(r.Sessions) != len(r.Processes) {
		return fmt.Errorf("controller v3 provider incarnation count mismatch")
	}
	for _, root := range r.ProcessDiagnostics[0].Roots {
		if root.City == r.CityPath && !seenPID[root.PID] {
			return fmt.Errorf("controller v3 city root lacks provider ownership")
		}
	}
	for _, frame := range r.ProcessDiagnostics {
		anchors, err := frame.RetirementAnchors()
		if err != nil {
			return fmt.Errorf("controller v3 retirement anchor invalid")
		}
		for _, anchor := range anchors {
			matched := false
			for _, process := range r.Processes {
				if process.PID == anchor.Runtime.PID && process.PPID == anchor.Runtime.PPID && process.StartIdentity == anchor.StartIdentity && process.SessionID == anchor.Runtime.SessionID && process.CityPath == anchor.Runtime.City && process.Template == anchor.Template && process.RunEpoch == anchor.Runtime.Epoch && process.InstanceTokenSHA256 == anchor.InstanceTokenSHA256 {
					matched = true
					break
				}
			}
			if !matched {
				return fmt.Errorf("controller v3 certificate lacks provider-owned survivor")
			}
		}
	}
	return nil
}

func controllerV3ProcessMatches(p observation.Process, r procobserver.Root) bool {
	return p.PID == r.PID && p.PPID == r.PPID && p.StartIdentity == r.StartTicks && p.SessionID == r.SessionID && p.CityPath == r.City && p.Template == r.Template && p.RunEpoch == r.Epoch && p.InstanceTokenSHA256 == r.InstanceTokenSHA256
}

func controllerV3OwnedSession(p observation.Process, sessions []observation.Session) bool {
	for _, s := range sessions {
		if s.Running && s.SessionID == p.SessionID && s.Template == p.Template && s.RuntimeName == p.RuntimeName && s.RunEpoch == p.RunEpoch && s.InstanceTokenSHA256 == p.InstanceTokenSHA256 {
			return true
		}
	}
	return false
}

// relayControllerObservationV3At is deliberately unregistered. The caller
// supplies its separately approved request writer; no new verb is invented.
// Successful producer bytes and BOTH provisional frames pass unchanged.
func relayControllerObservationV3At(ctx context.Context, city, socketPath, source string, stdout io.Writer, loadPolicy func() (procobserver.ReleasePolicyV3, error), verifyPeer func(net.Conn, procobserver.CallerBinding) error, request func(net.Conn) error, now func() time.Time) error {
	unknown := func(reason string) error {
		r := unknownControllerObservationV3(ctx, city, source, reason)
		if err := json.NewEncoder(stdout).Encode(r); err != nil {
			return err
		}
		return errExit
	}
	if ctx.Err() != nil {
		return unknown("controller v3 observation canceled")
	}
	p, err := loadPolicy()
	if err != nil || p.EvidenceSchema != procobserver.ResponseSchemaV3 || !controllerObservationHex(source, 40) || p.HelperSourceRevision != source || p.CallerBinding.ControllerSourceRevision != source || request == nil {
		return unknown("controller v3 approved policy or request unavailable")
	}
	if ctx.Err() != nil {
		return unknown("controller v3 observation canceled")
	}
	dialer := net.Dialer{Timeout: time.Second}
	conn, err := dialer.DialContext(ctx, "unix", socketPath)
	if err != nil {
		return unknown("controller v3 transport unavailable")
	}
	defer conn.Close() //nolint:errcheck // bounded relay cleanup
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	deadline := time.Now().Add(controllerObservationBudget + 2*time.Second)
	if bound, ok := ctx.Deadline(); ok && bound.Before(deadline) {
		deadline = bound
	}
	if conn.SetDeadline(deadline) != nil {
		return unknown("controller v3 deadline unavailable")
	}
	if verifyPeer(conn, p.CallerBinding) != nil {
		return unknown("controller v3 peer identity mismatch")
	}
	if ctx.Err() != nil {
		return unknown("controller v3 observation canceled")
	}
	if request(conn) != nil {
		return unknown("controller v3 request failed")
	}
	data, err := io.ReadAll(io.LimitReader(conn, controllerObservationV3Limit+1))
	if err != nil || len(data) > controllerObservationV3Limit {
		return unknown("controller v3 reply unavailable or oversized")
	}
	if ctx.Err() != nil {
		return unknown("controller v3 observation canceled")
	}
	var reply controllerObservationReplyV3
	if procobserver.DecodeStrict(data, &reply) != nil || validateControllerObservationReplyV3(reply, p, city, source, now().UTC()) != nil {
		return unknown("controller v3 reply invalid, stale or mismatched")
	}
	if ctx.Err() != nil {
		return unknown("controller v3 observation canceled")
	}
	if verifyPeer(conn, p.CallerBinding) != nil {
		return unknown("controller v3 peer changed during read")
	}
	if ctx.Err() != nil {
		return unknown("controller v3 observation canceled")
	}
	if _, err = stdout.Write(data); err != nil {
		return err
	}
	if !reply.ProviderComplete || !reply.ProcessComplete {
		return errExit
	}
	return nil
}
