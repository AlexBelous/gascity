package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"strconv"
	"time"

	"github.com/gastownhall/gascity/internal/runtime"
	"github.com/gastownhall/gascity/internal/runtime/observation"
	"github.com/gastownhall/gascity/internal/runtime/observationattempt"
	"github.com/gastownhall/gascity/internal/runtime/procobserver"
)

// Activation is a separately reviewed source change, not an environment or CLI
// escape hatch. The owner SOP amendment and coherent release must precede ON.
const controllerObservationV3WholeReacquisitionEnabled = false

func wholeAttemptDigest(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// Full declared-root identity is an additional cross-attempt fence. Unchanged
// SID/token metadata cannot hide a replaced PID/start or changed UID tuple.
func controllerV3CompleteRootFingerprint(frames []procobserver.ResponseV3) string {
	var fingerprint string
	for _, f := range frames {
		if !f.Complete || len(f.Census.Scans) == 0 {
			continue
		}
		var roots []struct {
			Root     procobserver.Root
			Identity procobserver.CensusIdentity
		}
		for _, root := range f.Roots {
			found := false
			for _, v := range f.Census.Scans[len(f.Census.Scans)-1].Verified {
				if v.PID == root.PID && v.DeclaredRoot {
					roots = append(roots, struct {
						Root     procobserver.Root
						Identity procobserver.CensusIdentity
					}{root, v})
					found = true
				}
			}
			if !found {
				return ""
			}
		}
		next := wholeAttemptDigest(roots)
		if fingerprint != "" && fingerprint != next {
			return ""
		}
		fingerprint = next
	}
	return fingerprint
}

// The same positively known managed identity fields fence both partial and
// complete attempts. Parent metadata absent from partial scans is never guessed.
func controllerV3CaptureFingerprint(frames []procobserver.ResponseV3, p procobserver.ReleasePolicyV3, now time.Time) string {
	var fingerprint string
	for _, f := range frames {
		if !f.Complete {
			continue
		}
		identities, ok := procobserver.CaptureReacquisitionIdentitiesV3(f, p, now)
		if !ok {
			return ""
		}
		next := wholeAttemptDigest(identities)
		if fingerprint != "" && fingerprint != next {
			return ""
		}
		fingerprint = next
	}
	if fingerprint != "" {
		return fingerprint
	}
	for _, f := range frames {
		identities, ok := procobserver.CaptureReacquisitionIdentitiesV3(f, p, now)
		if !ok || len(identities) == 0 {
			return ""
		}
		next := wholeAttemptDigest(identities)
		if fingerprint != "" && fingerprint != next {
			return ""
		}
		fingerprint = next
	}
	return fingerprint
}

// These values are internal provenance, deliberately excluded from the wire.
// Original helper frames and their wire roots remain unchanged.
type controllerV3CaptureJoin struct {
	nonce      string
	identities []procobserver.CensusIdentity
	tracked    []runtime.LiveRuntime
	valid      bool
}

// Audit records live beside the original failed typed envelope, inside the
// existing bounded immutable bundle; they never enter the observation wire.
type controllerV3CaptureJoinAudit struct {
	Classification    string                         `json:"classification"`
	RequestNonce      string                         `json:"request_nonce"`
	ManagedIdentities []procobserver.CensusIdentity  `json:"managed_identities"`
	TrackedRoots      []controllerV3TrackedRootAudit `json:"tracked_roots"`
}

type controllerV3TrackedRootAudit struct {
	PID          int    `json:"pid"`
	SessionID    string `json:"session_id"`
	ProviderName string `json:"provider_name"`
}

func controllerV3CaptureJoinAudits(joins []controllerV3CaptureJoin) []controllerV3CaptureJoinAudit {
	var out []controllerV3CaptureJoinAudit
	for _, j := range joins {
		if j.valid {
			roots := make([]controllerV3TrackedRootAudit, 0, len(j.tracked))
			for _, v := range j.tracked {
				roots = append(roots, controllerV3TrackedRootAudit{v.PID, v.SessionID, v.ProviderName})
			}
			out = append(out, controllerV3CaptureJoinAudit{"eligibility_only_not_complete", j.nonce, j.identities, roots})
		}
	}
	return out
}

func controllerV3JoinCapture(ctx context.Context, f procobserver.ResponseV3, p procobserver.ReleasePolicyV3, sp runtime.Provider, now func() time.Time) controllerV3CaptureJoin {
	out := controllerV3CaptureJoin{nonce: f.RequestNonce}
	identities, ok := procobserver.CaptureReacquisitionIdentitiesV3(f, p, now())
	tracker, capable := sp.(runtime.ProcessRootTracker)
	if !ok || !capable || ctx.Err() != nil {
		return out
	}
	roots := []runtime.LiveRuntime{}
	for _, v := range identities {
		if v.DeclaredRoot {
			roots = append(roots, runtime.LiveRuntime{PID: v.PID, PPID: v.PPID, SessionID: v.SessionID, City: v.City, Epoch: v.Epoch, Name: v.Name})
		}
	}
	if len(roots) == 0 {
		return out
	}
	expected := append([]runtime.LiveRuntime{}, roots...)
	tracked, err := tracker.TrackProcessRoots(roots)
	if err != nil || ctx.Err() != nil || len(tracked) != len(roots) {
		return out
	}
	for i, v := range tracked {
		if !v.IsTracked || v.ProviderName == "" {
			return out
		}
		original := v
		original.IsTracked = false
		original.ProviderName = ""
		if original != expected[i] {
			return out
		}
	}
	out.identities = identities
	out.tracked = append([]runtime.LiveRuntime{}, tracked...)
	out.valid = true
	return out
}

func controllerV3BothPartialJoined(r controllerObservationReplyV3, p procobserver.ReleasePolicyV3, now time.Time) bool {
	if len(r.reacquisitionJoins) != 2 || len(r.Sessions) == 0 {
		return false
	}
	for i, f := range r.ProcessDiagnostics {
		j := r.reacquisitionJoins[i]
		identities, ok := procobserver.CaptureReacquisitionIdentitiesV3(f, p, now)
		if f.Complete || !ok || !j.valid || j.nonce != f.RequestNonce || !reflect.DeepEqual(j.identities, identities) || len(j.tracked) != len(r.Sessions) {
			return false
		}
		seenSID, seenPID := map[string]bool{}, map[int]bool{}
		for _, tracked := range j.tracked {
			if tracked.City != r.CityPath || seenSID[tracked.SessionID] || seenPID[tracked.PID] {
				return false
			}
			seenSID[tracked.SessionID], seenPID[tracked.PID] = true, true
			matched := false
			for _, v := range identities {
				if !v.DeclaredRoot || v.PID != tracked.PID {
					continue
				}
				for _, s := range r.Sessions {
					if s.Running && s.RuntimeName == tracked.ProviderName && s.SessionID == v.SessionID && s.Template == v.Template && s.RunEpoch == v.Epoch && s.InstanceTokenSHA256 == v.InstanceTokenSHA256 {
						matched = true
					}
				}
			}
			if !matched {
				return false
			}
		}
	}
	return true
}

func controllerV3TransientWhole(r controllerObservationReplyV3, p procobserver.ReleasePolicyV3, now func() time.Time) bool {
	if !r.ProviderComplete || r.ProcessComplete || len(r.FailureKinds) == 0 {
		return false
	}
	for _, k := range r.FailureKinds {
		if k != observation.FailureIncompleteProjection {
			return false
		}
	}
	if !procobserver.EligibleCapturePairReacquisitionV3(r.ProcessDiagnostics, p, now()) {
		return false
	}
	if !r.ProcessDiagnostics[0].Complete && !r.ProcessDiagnostics[1].Complete {
		return controllerV3BothPartialJoined(r, p, now())
	}
	for _, f := range r.ProcessDiagnostics {
		if !f.Complete {
			continue
		}
		seen := map[string]bool{}
		for _, root := range f.Roots {
			if filepath.Clean(root.City) != filepath.Clean(r.CityPath) {
				continue
			}
			if !root.ParentIsProviderInfrastructure || seen[root.SessionID] {
				return false
			}
			matched := false
			for _, s := range r.Sessions {
				if s.Running && s.SessionID == root.SessionID && s.Template == root.Template && s.RunEpoch == root.Epoch && s.InstanceTokenSHA256 == root.InstanceTokenSHA256 {
					matched = true
				}
			}
			if !matched {
				return false
			}
			seen[root.SessionID] = true
		}
		if len(seen) != len(r.Sessions) {
			return false
		}
	}
	return true
}

func (s *controllerObservationServiceV3) collectReacquired(ctx context.Context, p procobserver.ReleasePolicyV3, sp runtime.Provider, aborted func() bool) controllerObservationReplyV3 {
	checks := 0
	var strictManaged []procobserver.CensusIdentity
	identityFingerprint := func(r controllerObservationReplyV3) string {
		if len(r.ProcessDiagnostics) == 2 && !r.ProcessDiagnostics[0].Complete && !r.ProcessDiagnostics[1].Complete {
			identities, ok := procobserver.CaptureReacquisitionIdentitiesV3(r.ProcessDiagnostics[0], p, s.now())
			if !ok {
				return ""
			}
			strictManaged = identities
		} else if strictManaged != nil {
			for _, f := range r.ProcessDiagnostics {
				for _, scan := range f.Census.Scans {
					seen := map[int]procobserver.CensusIdentity{}
					for _, v := range scan.Verified {
						if v.Classification == "managed" {
							seen[v.PID] = v
						}
					}
					if len(seen) != len(strictManaged) {
						return ""
					}
					for _, v := range strictManaged {
						if seen[v.PID] != v {
							return ""
						}
					}
				}
			}
		}
		if strictManaged == nil {
			return controllerV3CompleteRootFingerprint(r.ProcessDiagnostics)
		}
		return controllerV3CaptureFingerprint(r.ProcessDiagnostics, p, s.now())
	}
	h := observationattempt.Hooks[controllerObservationReplyV3]{
		Collect: func(ctx context.Context) controllerObservationReplyV3 { return s.collect(ctx, p, sp) },
		Validate: func(r controllerObservationReplyV3) observationattempt.Decision {
			if validateControllerObservationReplyV3(r, p, s.city, s.sourceRevision, s.now()) != nil {
				return observationattempt.Rejected
			}
			if r.ProviderComplete && r.ProcessComplete {
				return observationattempt.Complete
			}
			if controllerV3TransientWhole(r, p, s.now) {
				return observationattempt.TransientCoverage
			}
			return observationattempt.Rejected
		},
		Identity: func(r controllerObservationReplyV3) observationattempt.Identity {
			v := observationattempt.Identity{BindingFingerprint: wholeAttemptDigest(p), ProviderFingerprint: wholeAttemptDigest(r.Sessions), RootFingerprint: identityFingerprint(r), WholeStartedAt: r.ObservedAt, WholeFinishedAt: r.FinishedAt}
			if len(r.ProcessDiagnostics) == 2 {
				for i, f := range r.ProcessDiagnostics {
					v.Frames[i] = observationattempt.Frame{Nonce: f.RequestNonce, StartedAt: f.StartedAt, FinishedAt: f.FinishedAt}
				}
			}
			return v
		},
		Invariant: func(ctx context.Context) error {
			if ctx.Err() != nil || aborted() {
				return errors.New("whole observation fence changed")
			}
			checks++
			// Re-read trusted policy and caller before each possible whole attempt;
			// the wrapper's generation/flight remains held across both transactions.
			if checks > 1 {
				q, err := s.loadPolicy()
				if err != nil || wholeAttemptDigest(q) != wholeAttemptDigest(p) || s.checkCaller(p.Policy) != nil {
					return errors.New("whole observation policy or caller changed")
				}
			}
			return nil
		},
		Retain: s.retainAttempt, Record: s.recordAttempt, Now: s.now,
	}
	r := observationattempt.Run(ctx, h)
	if r.Value.Schema == "" {
		return s.unknown(r.Reason)
	}
	if r.Selected == 0 {
		r.Value.ProcessComplete = false
		r.Value.ControllerBinding = "not_observed"
		r.Value.CertificateDisposition = "provisional"
		r.Value.UnknownReasons = append(r.Value.UnknownReasons, "whole observation reacquisition: "+r.Reason)
		for _, cause := range r.Value.FailureKinds {
			if cause == observation.FailureProviderTracker {
				r.Value.UnknownReasons = append(r.Value.UnknownReasons, "whole observation reacquisition: provider_tracker_error")
				break
			}
		}
	}
	return r.Value
}

// The ordinary provider re-read precedes frame two. Retry-only coverage also
// fences the complete live handle/incarnation set AFTER its final PID join.
func controllerV3CaptureProviderUnchanged(ctx context.Context, sp runtime.Provider, sessions []observation.Session) bool {
	if ctx.Err() != nil {
		return false
	}
	names, err := sp.ListRunning("")
	if err != nil || ctx.Err() != nil || len(names) != len(sessions) {
		return false
	}
	seen := map[string]bool{}
	for _, name := range names {
		if seen[name] {
			return false
		}
		seen[name] = true
		var session *observation.Session
		for i := range sessions {
			if sessions[i].RuntimeName == name {
				session = &sessions[i]
			}
		}
		if session == nil || !session.Running {
			return false
		}
		for _, key := range []string{"GC_SESSION_ID", "GC_TEMPLATE", "GC_RUNTIME_EPOCH", "GC_INSTANCE_TOKEN"} {
			if ctx.Err() != nil {
				return false
			}
			value, err := sp.GetMeta(name, key)
			if err != nil || ctx.Err() != nil || value == "" {
				return false
			}
			switch key {
			case "GC_SESSION_ID":
				if value != session.SessionID {
					return false
				}
			case "GC_TEMPLATE":
				if value != session.Template {
					return false
				}
			case "GC_RUNTIME_EPOCH":
				epoch, err := strconv.Atoi(value)
				if err != nil || epoch != session.RunEpoch {
					return false
				}
			case "GC_INSTANCE_TOKEN":
				if observation.TokenDigest(value) != session.InstanceTokenSHA256 {
					return false
				}
			}
		}
	}
	return ctx.Err() == nil
}
