package main

import (
	"bufio"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gastownhall/gascity/internal/runtime"
	"github.com/gastownhall/gascity/internal/runtime/observation"
	"github.com/gastownhall/gascity/internal/runtime/procobserver"
)

const (
	controllerObservationCommand    = "observe-managed-sessions"
	controllerObservationLimit      = 1024 * 1024
	controllerObservationBudget     = 25 * time.Second
	controllerObservationAttempts   = 3
	controllerObservationPolicyPath = "/etc/gascity-observer/client.json"
)

// One slot for the persistent process, including all supervisor-managed cities.
// A timed-out noncooperative provider retains it until its read really returns.
var controllerObservationFlight = make(chan struct{}, 1)

type controllerObservationReply struct {
	observation.Observation
	SourceRevision          string                         `json:"source_revision"`
	ControllerBinding       string                         `json:"controller_binding"`
	ControllerPID           int                            `json:"controller_pid"`
	ControllerBinarySHA256  string                         `json:"controller_binary_sha256"`
	ControllerStartIdentity string                         `json:"controller_start_identity"`
	ControllerBootID        string                         `json:"controller_boot_id"`
	HelperBinarySHA256      string                         `json:"helper_binary_sha256"`
	HelperPolicyDigest      string                         `json:"helper_policy_digest"`
	ProcessDiagnostics      []controllerProcessDiagnostics `json:"process_diagnostics,omitempty"`
}

// The two independently decoded helper frames retain only bounded, redacted
// diagnostics. They do not replace the provider/process join or authorize it.
type controllerProcessDiagnostics struct {
	StartedAt               time.Time                    `json:"started_at"`
	FinishedAt              time.Time                    `json:"finished_at"`
	Complete                bool                         `json:"complete"`
	EnumeratedCountBefore   int                          `json:"enumerated_count_before"`
	EnumeratedCountAfter    int                          `json:"enumerated_count_after"`
	EnumerationDigestBefore string                       `json:"enumeration_digest_before"`
	EnumerationDigestAfter  string                       `json:"enumeration_digest_after"`
	Errors                  []procobserver.EvidenceError `json:"errors"`
	ErrorsTotal             int                          `json:"errors_total"`
	ErrorsTruncated         bool                         `json:"errors_truncated"`
	DurationMS              int64                        `json:"duration_ms"`
	KernelRelease           string                       `json:"kernel_release"`
	KernelProofProfile      string                       `json:"kernel_proof_profile"`
	Census                  procobserver.Census          `json:"census"`
}

type controllerSocketOptions struct {
	observe   func(context.Context) controllerObservationReply
	observeV3 func(context.Context) controllerObservationReplyV3
}

type controllerObservationService struct {
	ctx          context.Context
	city         string
	mu           sync.Mutex
	state        *controllerState
	policy       *procobserver.Policy
	loadPolicy   func() (procobserver.Policy, error)
	checkCaller  func(procobserver.Policy) error
	readEvidence func(context.Context, procobserver.Policy) (procobserver.Response, error)
	collect      func(context.Context, procobserver.Policy, runtime.Provider) controllerObservationReply
}

func newControllerObservationService(ctx context.Context, city string) *controllerObservationService {
	s := &controllerObservationService{ctx: ctx, city: filepath.Clean(city)}
	s.loadPolicy = func() (procobserver.Policy, error) {
		return procobserver.LoadPolicy(controllerObservationPolicyPath, commit)
	}
	s.checkCaller = procobserver.CheckCaller
	s.readEvidence = procobserver.ReadContext
	s.collect = s.collectOnce
	return s
}

func (s *controllerObservationService) collectOnce(ctx context.Context, p procobserver.Policy, sp runtime.Provider) controllerObservationReply {
	diagnostics := []controllerProcessDiagnostics{}
	observed := observation.ObserveProcessEvidenceContext(ctx, s.city, sp, func() observation.ProcessEvidence {
		evidence, err := s.readEvidence(ctx, p)
		// A contract/pin failure returns a zero Response, not trusted details.
		if evidence.Schema == procobserver.Schema {
			diagnostics = append(diagnostics, controllerProcessDiagnostics{
				StartedAt: evidence.StartedAt, FinishedAt: evidence.FinishedAt, Complete: evidence.Complete,
				EnumeratedCountBefore: evidence.EnumeratedCountBefore, EnumeratedCountAfter: evidence.EnumeratedCountAfter,
				EnumerationDigestBefore: evidence.EnumerationDigestBefore, EnumerationDigestAfter: evidence.EnumerationDigestAfter,
				Errors: append([]procobserver.EvidenceError{}, evidence.Errors...), ErrorsTotal: evidence.ErrorsTotal, ErrorsTruncated: evidence.ErrorsTruncated,
				DurationMS: evidence.DurationMS, KernelRelease: evidence.KernelRelease, KernelProofProfile: evidence.KernelProofProfile, Census: evidence.Census,
			})
		}
		return observation.ProcessEvidence{Roots: evidence.ObservedRoots(), StartedAt: evidence.StartedAt, FinishedAt: evidence.FinishedAt, Err: err}
	}, time.Now)
	r := s.unknown("")
	r.Observation = observed
	r.ProcessDiagnostics = diagnostics
	r.ControllerPID = p.CallerBinding.PID
	r.ControllerBinarySHA256 = p.CallerBinding.ControllerBinarySHA256
	r.ControllerStartIdentity = p.CallerBinding.StartTicks
	r.ControllerBootID = p.CallerBinding.BootID
	r.HelperBinarySHA256 = p.HelperBinarySHA256
	r.HelperPolicyDigest = p.PolicyDigest
	if observed.ProviderComplete && observed.ProcessComplete {
		r.ControllerBinding = "verified_local_process"
	}
	return r
}

func (s *controllerObservationService) install(cs *controllerState) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.state = cs
}

func (s *controllerObservationService) unknown(reason string) controllerObservationReply {
	now := time.Now().UTC()
	reasons := []string{}
	if reason != "" {
		reasons = append(reasons, reason)
	}
	return controllerObservationReply{Observation: observation.Observation{Schema: observation.Schema, CityPath: s.city, ObservedAt: now, ProcessObservedAt: now, FinishedAt: now, Sessions: []observation.Session{}, Processes: []observation.Process{}, UnknownReasons: reasons}, SourceRevision: commit, ControllerBinding: "not_observed", ControllerPID: os.Getpid()}
}

func (s *controllerObservationService) observe(request context.Context) controllerObservationReply {
	s.mu.Lock()
	cs := s.state
	s.mu.Unlock()
	if cs == nil {
		return s.unknown("controller observation not ready")
	}
	if request.Err() != nil || s.ctx.Err() != nil {
		return s.unknown("observation canceled")
	}
	select {
	case controllerObservationFlight <- struct{}{}:
	default:
		return s.unknown("observation busy")
	}
	ctx, cancel := context.WithTimeout(request, controllerObservationBudget)
	defer cancel()
	stopShutdown := context.AfterFunc(s.ctx, cancel)
	defer stopShutdown()
	done := make(chan controllerObservationReply, 1)
	go func() {
		flightHeld := true
		defer func() {
			if flightHeld {
				<-controllerObservationFlight
			}
		}()
		cs.mu.RLock()
		sp, generation := cs.sp, cs.observationGeneration
		cs.mu.RUnlock()
		if sp == nil {
			done <- s.unknown("controller provider not ready")
			return
		}
		// Retry an absent or stale startup binding until the approved root binder
		// publishes this exact persistent PID. Latch only after real caller proof.
		s.mu.Lock()
		p := s.policy
		s.mu.Unlock()
		if p == nil {
			loaded, err := s.loadPolicy()
			if err != nil {
				done <- s.unknown("observer deployment policy unavailable")
				return
			}
			if err = s.checkCaller(loaded); err != nil {
				done <- s.unknown("observer caller binding unverified")
				return
			}
			s.mu.Lock()
			s.policy = &loaded
			s.mu.Unlock()
			p = &loaded
		}
		if ctx.Err() != nil {
			done <- s.unknown("observation canceled")
			return
		}
		providerChanged := func() bool {
			cs.mu.RLock()
			changed := generation != cs.observationGeneration
			cs.mu.RUnlock()
			s.mu.Lock()
			changed = changed || s.state != cs
			s.mu.Unlock()
			return changed
		}
		result := s.unknown("observation canceled or provider changed")
		// Keep the same request deadline, latched policy/provider generation and
		// global flight slot. Every retry replaces the entire prior observation.
		for attempt := 0; attempt < controllerObservationAttempts; attempt++ {
			if ctx.Err() != nil || s.ctx.Err() != nil || providerChanged() {
				break
			}
			result = s.collect(ctx, *p, sp)
			if ctx.Err() != nil || s.ctx.Err() != nil || providerChanged() || !controllerObservationRetryable(result) {
				break
			}
		}
		changed := providerChanged()
		if changed || ctx.Err() != nil || s.ctx.Err() != nil {
			result.ProviderComplete = false
			result.ProcessComplete = false
			result.ControllerBinding = "not_observed"
			result.UnknownReasons = append(result.UnknownReasons, "controller provider changed or observation canceled")
		}
		<-controllerObservationFlight
		flightHeld = false
		done <- result
	}()
	select {
	case result := <-done:
		if ctx.Err() != nil || s.ctx.Err() != nil {
			return s.unknown("observation canceled")
		}
		return result
	case <-ctx.Done():
		return s.unknown("observation canceled or deadline exceeded")
	}
}

func controllerObservationHex(s string, n int) bool {
	if len(s) != n {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil
}

func validateControllerObservationReply(r controllerObservationReply, city, source string, now time.Time) error {
	if r.Schema != observation.Schema || r.CityPath != filepath.Clean(city) || !controllerObservationHex(r.SourceRevision, 40) || r.SourceRevision != source {
		return fmt.Errorf("controller observation source or city mismatch")
	}
	if r.Sessions == nil || r.Processes == nil || r.UnknownReasons == nil || r.ControllerPID <= 1 {
		return fmt.Errorf("controller observation fields invalid")
	}
	if r.ObservedAt.IsZero() || r.ProcessObservedAt.IsZero() || r.FinishedAt.IsZero() || r.ProcessObservedAt.Before(r.ObservedAt) || r.ProcessObservedAt.After(r.FinishedAt) || r.FinishedAt.Before(r.ObservedAt) || r.FinishedAt.After(now) || now.Sub(r.ObservedAt) > 60*time.Second || r.FinishedAt.Sub(r.ObservedAt) > 60*time.Second {
		return fmt.Errorf("controller observation interval invalid or stale")
	}
	if len(r.ProcessDiagnostics) > 2 || r.ProcessComplete && len(r.ProcessDiagnostics) != 2 {
		return fmt.Errorf("controller process diagnostics exceed frame bound")
	}
	for i, d := range r.ProcessDiagnostics {
		if err := validateControllerProcessDiagnostics(d, r.ObservedAt, r.FinishedAt, r.ProcessComplete); err != nil {
			return err
		}
		if r.ProcessComplete && (len(r.Processes) > d.Census.ReconciledCount || len(r.Processes) > d.Census.Seal.ClassifiedCount) {
			return fmt.Errorf("controller roots exceed classified census")
		}
		if i > 0 && d.StartedAt.Before(r.ProcessDiagnostics[i-1].FinishedAt) {
			return fmt.Errorf("controller process diagnostics overlap or are reordered")
		}
	}
	if r.ProviderComplete && r.ProcessComplete {
		start, err := strconv.ParseUint(r.ControllerStartIdentity, 10, 64)
		if r.ControllerBinding != "verified_local_process" || !controllerObservationHex(r.ControllerBinarySHA256, 64) || !controllerObservationHex(r.HelperBinarySHA256, 64) || !controllerObservationHex(r.HelperPolicyDigest, 64) || err != nil || start == 0 || strconv.FormatUint(start, 10) != r.ControllerStartIdentity || len(r.ControllerBootID) != 36 || len(r.UnknownReasons) != 0 {
			return fmt.Errorf("controller observation binding unverified")
		}
	} else if len(r.UnknownReasons) == 0 {
		return fmt.Errorf("partial controller observation lacks reason")
	}
	return nil
}

// Only two authenticated, untruncated helper frames describing enumeration
// change or typed stat/environment read disappearance permit another whole
// attempt. All other process/provider/contract failures remain terminal UNKNOWN.
func controllerObservationRetryable(r controllerObservationReply) bool {
	if !r.ProviderComplete || r.ProcessComplete || len(r.ProcessDiagnostics) != 2 || len(r.UnknownReasons) == 0 {
		return false
	}
	now := time.Now().UTC()
	if r.ObservedAt.IsZero() || r.ProcessObservedAt.Before(r.ObservedAt) || r.ProcessObservedAt.After(r.FinishedAt) || r.FinishedAt.Before(r.ObservedAt) || r.FinishedAt.After(now) || now.Sub(r.ObservedAt) > 60*time.Second {
		return false
	}
	for _, reason := range r.UnknownReasons {
		switch reason {
		case "strict process scan incomplete: observer process coverage incomplete",
			"process identities changed or became unavailable during observation":
		default:
			return false
		}
	}
	partial := false
	for i, d := range r.ProcessDiagnostics {
		if validateControllerProcessDiagnostics(d, r.ObservedAt, r.FinishedAt, d.Complete) != nil || d.ErrorsTruncated || d.ErrorsTotal != len(d.Errors) || (i > 0 && d.StartedAt.Before(r.ProcessDiagnostics[i-1].FinishedAt)) {
			return false
		}
		if !d.Complete {
			if len(d.Errors) == 0 {
				return false
			}
			partial = true
		}
		for _, e := range d.Errors {
			if e.Reason == "coverage_changed" && e.Operation == "enumerate" && e.PID == 0 && e.Errno == 0 {
				continue
			}
			statGone := e.Operation == "stat" && e.Errno == 2
			environmentGone := e.Operation == "environ" && e.Errno == 3
			if e.Reason != "process_unavailable" || e.PID <= 0 || (!statGone && !environmentGone) {
				return false
			}
		}
	}
	return partial
}

func validateControllerProcessDiagnostics(d controllerProcessDiagnostics, started, finished time.Time, complete bool) error {
	if d.StartedAt.IsZero() || d.FinishedAt.Before(d.StartedAt) || d.StartedAt.Before(started) || d.FinishedAt.After(finished) || d.FinishedAt.Sub(d.StartedAt) > procobserver.Timeout || d.EnumeratedCountBefore < 1 || d.EnumeratedCountBefore > procobserver.MaxProcesses || d.EnumeratedCountAfter < 1 || d.EnumeratedCountAfter > procobserver.MaxProcesses || !controllerObservationHex(d.EnumerationDigestBefore, 64) || !controllerObservationHex(d.EnumerationDigestAfter, 64) || d.Errors == nil || len(d.Errors) > 256 || d.ErrorsTotal < len(d.Errors) || d.ErrorsTruncated != (d.ErrorsTotal > len(d.Errors)) {
		return fmt.Errorf("controller process diagnostics invalid")
	}
	for _, e := range d.Errors {
		if len(e.Reason) < 1 || len(e.Reason) > 64 || len(e.Operation) < 1 || len(e.Operation) > 32 || e.PID < 0 || e.Errno < 0 || e.Errno > 4095 {
			return fmt.Errorf("controller process diagnostic item invalid")
		}
	}
	if complete && (!d.Complete || !strings.HasPrefix(d.KernelRelease, "6.8.") || d.KernelProofProfile != procobserver.KernelProofProfile || d.DurationMS < 0 || d.DurationMS > procobserver.Timeout.Milliseconds() || procobserver.ValidateCensus(d.Census, d.Errors, d.ErrorsTotal, d.ErrorsTruncated, d.DurationMS) != nil || d.Census.Closings[1].EnumeratedCount != d.EnumeratedCountAfter || d.Census.Closings[1].EnumerationDigest != d.EnumerationDigestAfter) {
		return fmt.Errorf("controller complete reply has partial helper diagnostics")
	}
	return nil
}

// relayControllerObservation does not construct a provider or call a helper.
// Successful wire bytes, source pins and producer timestamps pass unchanged.
func relayControllerObservation(ctx context.Context, city, source string, stdout io.Writer) error {
	return relayControllerObservationAuthenticated(ctx, city, source, stdout, func() (procobserver.Policy, error) {
		return procobserver.LoadPolicy(controllerObservationPolicyPath, source)
	}, procobserver.VerifyControllerPeer, time.Now)
}

func relayControllerObservationAuthenticated(ctx context.Context, city, source string, stdout io.Writer, loadPolicy func() (procobserver.Policy, error), verifyPeer func(net.Conn, procobserver.CallerBinding) error, now func() time.Time) error {
	return relayControllerObservationAt(ctx, city, controllerSocketPath(city), source, stdout, loadPolicy, verifyPeer, now)
}

func relayControllerObservationAt(ctx context.Context, city, socketPath, source string, stdout io.Writer, loadPolicy func() (procobserver.Policy, error), verifyPeer func(net.Conn, procobserver.CallerBinding) error, now func() time.Time) error {
	unknown := func(reason string) error {
		r := newControllerObservationService(ctx, city).unknown(reason)
		r.SourceRevision = source
		if err := json.NewEncoder(stdout).Encode(r); err != nil {
			return err
		}
		return errExit
	}
	policy, err := loadPolicy()
	if err != nil {
		return unknown("controller observation approved peer policy unavailable")
	}
	dialer := net.Dialer{Timeout: time.Second}
	conn, err := dialer.DialContext(ctx, "unix", socketPath)
	if err != nil {
		return unknown("controller observation transport unavailable")
	}
	defer conn.Close() //nolint:errcheck // bounded relay cleanup
	if verifyPeer(conn, policy.CallerBinding) != nil {
		return unknown("controller observation peer identity mismatch")
	}
	stopClose := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stopClose()
	deadline := time.Now().Add(controllerObservationBudget + 2*time.Second)
	if bounded, ok := ctx.Deadline(); ok && bounded.Before(deadline) {
		deadline = bounded
	}
	if err = conn.SetDeadline(deadline); err != nil {
		return unknown("controller observation deadline unavailable")
	}
	if _, err = io.WriteString(conn, controllerObservationCommand+"\n"); err != nil {
		return unknown("controller observation request failed")
	}
	data, err := io.ReadAll(io.LimitReader(conn, controllerObservationLimit+1))
	if err != nil || len(data) > controllerObservationLimit {
		return unknown("controller observation reply unavailable or oversized")
	}
	var reply controllerObservationReply
	if procobserver.DecodeStrict(data, &reply) != nil || validateControllerObservationReply(reply, city, source, now()) != nil {
		return unknown("controller observation reply invalid, stale or mismatched")
	}
	if reply.ProviderComplete && reply.ProcessComplete && (reply.ControllerPID != policy.CallerBinding.PID || reply.ControllerStartIdentity != policy.CallerBinding.StartTicks || reply.ControllerBinarySHA256 != policy.CallerBinding.ControllerBinarySHA256 || reply.ControllerBootID != policy.CallerBinding.BootID || reply.HelperBinarySHA256 != policy.HelperBinarySHA256 || reply.HelperPolicyDigest != policy.PolicyDigest) {
		return unknown("controller observation reply disagrees with approved peer pins")
	}
	if verifyPeer(conn, policy.CallerBinding) != nil {
		return unknown("controller observation peer changed during read")
	}
	if _, err = stdout.Write(data); err != nil {
		return err
	}
	if !reply.ProviderComplete || !reply.ProcessComplete {
		return errExit
	}
	return nil
}

// Use the same bounded reader for dispatch and the observer extra-data watcher.
// Scanner prefetch must not hide additional request bytes from cancellation.
func readControllerCommandLine(reader *bufio.Reader) ([]byte, error) {
	line := make([]byte, 0, 256)
	for {
		part, err := reader.ReadSlice('\n')
		if len(line)+len(part) > controllerObservationLimit {
			return nil, fmt.Errorf("controller request too large")
		}
		line = append(line, part...)
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		if err != nil {
			if err == io.EOF && len(line) > 0 {
				break
			}
			return nil, err
		}
		break
	}
	if len(line) > 0 && line[len(line)-1] == '\n' {
		line = line[:len(line)-1]
	}
	if len(line) > 0 && line[len(line)-1] == '\r' {
		line = line[:len(line)-1]
	}
	return line, nil
}
