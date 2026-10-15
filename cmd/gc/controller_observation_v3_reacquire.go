package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"path/filepath"
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
	h := observationattempt.Hooks[controllerObservationReplyV3]{
		Collect: func(ctx context.Context) controllerObservationReplyV3 { return s.collect(ctx, p, sp) },
		Validate: func(r controllerObservationReplyV3) observationattempt.Decision {
			if validateControllerObservationReplyV3(r, p, s.city, s.sourceRevision, s.now()) != nil {
				return observationattempt.Fatal
			}
			if r.ProviderComplete && r.ProcessComplete {
				return observationattempt.Complete
			}
			if controllerV3TransientWhole(r, p, s.now) {
				return observationattempt.TransientCoverage
			}
			return observationattempt.Fatal
		},
		Identity: func(r controllerObservationReplyV3) observationattempt.Identity {
			v := observationattempt.Identity{BindingFingerprint: wholeAttemptDigest(p), ProviderFingerprint: wholeAttemptDigest(r.Sessions), RootFingerprint: controllerV3CompleteRootFingerprint(r.ProcessDiagnostics), WholeStartedAt: r.ObservedAt, WholeFinishedAt: r.FinishedAt}
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
