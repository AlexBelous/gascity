package main

import (
	"context"
	"path/filepath"
	"reflect"
	"sync"
	"time"

	"github.com/gastownhall/gascity/internal/runtime"
	"github.com/gastownhall/gascity/internal/runtime/procobserver"
)

// controllerObservationServiceV3 is prospective source only. It shares V2's
// process-wide flight slot, provider generation and whole-request budget, but
// installs no socket option, command, handler or runtime selector.
type controllerObservationServiceV3 struct {
	ctx               context.Context
	city              string
	mu                sync.Mutex
	state             *controllerState
	installGeneration uint64
	policy            *procobserver.ReleasePolicyV3
	sourceRevision    string
	currentSource     func() string
	loadPolicy        func() (procobserver.ReleasePolicyV3, error)
	checkCaller       func(procobserver.Policy) error
	readEvidence      func(context.Context, procobserver.ReleasePolicyV3) (procobserver.ResponseV3, error)
	collect           func(context.Context, procobserver.ReleasePolicyV3, runtime.Provider) controllerObservationReplyV3
	now               func() time.Time
}

func newControllerObservationServiceV3(ctx context.Context, city string) *controllerObservationServiceV3 {
	revision := commit
	s := &controllerObservationServiceV3{ctx: ctx, city: filepath.Clean(city), sourceRevision: revision, now: time.Now}
	s.currentSource = func() string { return commit }
	s.loadPolicy = func() (procobserver.ReleasePolicyV3, error) {
		return procobserver.LoadPolicyV3(controllerObservationPolicyPath, revision)
	}
	s.checkCaller = procobserver.CheckCaller
	s.readEvidence = procobserver.ReadContextV3
	s.collect = func(ctx context.Context, p procobserver.ReleasePolicyV3, sp runtime.Provider) controllerObservationReplyV3 {
		return collectControllerObservationV3(ctx, s.city, p, sp, s.readEvidence, s.now)
	}
	return s
}

func (s *controllerObservationServiceV3) install(cs *controllerState) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.state = cs
	s.installGeneration++
}

func (s *controllerObservationServiceV3) unknown(reason string) controllerObservationReplyV3 {
	s.mu.Lock()
	source := s.sourceRevision
	s.mu.Unlock()
	return unknownControllerObservationV3(s.ctx, s.city, source, reason)
}

func controllerObservationV3PolicySource(p procobserver.ReleasePolicyV3, source string) bool {
	return controllerObservationHex(source, 40) && p.EvidenceSchema == procobserver.ResponseSchemaV3 && p.HelperSourceRevision == source && p.CallerBinding.ControllerSourceRevision == source
}

// Providers normally have pointer identity. An uncomparable implementation
// cannot prove that it is the same observed instance, so it fails closed.
func sameControllerObservationV3Provider(a, b runtime.Provider) bool {
	if a == nil || b == nil {
		return false
	}
	typ := reflect.TypeOf(a)
	return typ == reflect.TypeOf(b) && typ.Comparable() && a == b
}

func (s *controllerObservationServiceV3) observe(request context.Context) controllerObservationReplyV3 {
	started := time.Now()
	s.mu.Lock()
	cs, installed, source := s.state, s.installGeneration, s.sourceRevision
	s.mu.Unlock()
	if cs == nil {
		return s.unknown("controller observation not ready")
	}
	if request.Err() != nil || s.ctx.Err() != nil {
		return s.unknown("observation canceled")
	}
	if !controllerObservationHex(source, 40) || s.currentSource() != source {
		return s.unknown("controller v3 build source unavailable or changed")
	}
	select {
	case controllerObservationFlight <- struct{}{}:
	default:
		return s.unknown("observation busy")
	}
	// The deadline covers policy proof and the entire pair of helper reads;
	// an earlier caller deadline remains earlier. There is no per-attempt reset.
	ctx, cancel := context.WithDeadline(request, started.Add(controllerObservationBudget))
	defer cancel()
	stopShutdown := context.AfterFunc(s.ctx, cancel)
	defer stopShutdown()
	done := make(chan controllerObservationReplyV3, 1)
	go func() {
		result := s.unknown("controller v3 observation incomplete")
		// Even a canceled request cannot release this slot while a noncooperative
		// provider/policy/helper call still runs. Release precedes completion ACK.
		defer func() { <-controllerObservationFlight; done <- result }()
		cs.mu.RLock()
		sp, generation := cs.sp, cs.observationGeneration
		cs.mu.RUnlock()
		if sp == nil || !sameControllerObservationV3Provider(sp, sp) {
			result = s.unknown("controller provider unavailable or identity unprovable")
			return
		}
		changed := func() bool {
			cs.mu.RLock()
			drift := generation != cs.observationGeneration || !sameControllerObservationV3Provider(cs.sp, sp)
			cs.mu.RUnlock()
			s.mu.Lock()
			drift = drift || s.state != cs || s.installGeneration != installed || s.sourceRevision != source
			s.mu.Unlock()
			return drift || s.currentSource() != source
		}
		aborted := func() bool { return ctx.Err() != nil || s.ctx.Err() != nil || changed() }
		if aborted() {
			result = s.unknown("controller source changed or observation canceled")
			return
		}
		s.mu.Lock()
		policy := s.policy
		s.mu.Unlock()
		var p procobserver.ReleasePolicyV3
		if policy == nil {
			loaded, err := s.loadPolicy()
			if err != nil {
				result = s.unknown("observer v3 deployment policy unavailable")
				return
			}
			p = loaded
		} else {
			p = *policy
		}
		if aborted() {
			result = s.unknown("controller source changed or observation canceled")
			return
		}
		if !controllerObservationV3PolicySource(p, source) {
			result = s.unknown("observer v3 deployment source or selector mismatched")
			return
		}
		if s.checkCaller(p.Policy) != nil {
			result = s.unknown("observer v3 exact caller binding unverified")
			return
		}
		if aborted() {
			result = s.unknown("controller source changed or observation canceled")
			return
		}
		if policy == nil {
			s.mu.Lock()
			// install is the sole mutation path for this prospective wrapper. A state
			// swap-and-back still increments its generation and cannot latch a policy.
			stable := s.state == cs && s.installGeneration == installed && s.sourceRevision == source
			if stable {
				latched := p
				s.policy = &latched
			}
			s.mu.Unlock()
			if !stable || aborted() {
				result = s.unknown("controller source changed or observation canceled")
				return
			}
		}
		// Exactly ONE complete collector attempt; V3 retry grammar is not inferred
		// from V2 partial diagnostics. Ordinary provider ownership remains authority.
		result = s.collect(ctx, p, sp)
		if validateControllerObservationReplyV3(result, p, s.city, source, s.now().UTC()) != nil {
			result = s.unknown("controller v3 collected reply invalid, stale or mismatched")
			return
		}
		if aborted() {
			result.ProviderComplete = false
			result.ProcessComplete = false
			result.ControllerBinding = "not_observed"
			result.CertificateDisposition = "provisional"
			result.UnknownReasons = append(result.UnknownReasons, "controller source changed or observation canceled")
		}
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
