package session

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/pathutil"
	"github.com/gastownhall/gascity/internal/runtime"
)

type capacityAdmissionKey struct{}

// A proof exists only in this invocation, never in environment or durable row
// metadata. The ledger claim is durable; this one-shot proof lets controller
// preparation and the canonical manager share that exact claim.
type capacityAdmissionProof struct {
	city, route, id  string
	minted, deadline time.Time
	used             atomic.Bool
	native           *nativeCapacityProof
	remote           *remoteAdmissionProof
}

// AdmitCapacityStart spends at most one SID grant before start preparation.
// The returned context carries an opaque one-shot proof for that same attempt.
// Unmanaged targets and cities with no scheduler retain existing behavior.
func AdmitCapacityStart(ctx context.Context, city, route, id string, now time.Time, sp runtime.Provider, store beads.Store) (context.Context, bool, string) {
	if resolver, ok := sp.(runtime.SessionRuntimeProvider); ok {
		if _, err := resolver.BindRuntime(ctx, id, route); err != nil {
			return ctx, false, "runtime_binding_invalid"
		}
	}

	return admitCapacityStartWithHost(ctx, city, route, id, now, sp, store, newNativeCapacityHost())
}

func admitCapacityStartWithHost(ctx context.Context, city, route, id string, now time.Time, sp runtime.Provider, store beads.Store, host nativeCapacityHost) (context.Context, bool, string) {
	binding, err := remoteBinding(ctx, sp, id, route)
	if err != nil {
		return ctx, false, "runtime_binding_invalid"
	}
	existing, _ := ctx.Value(capacityAdmissionKey{}).(*capacityAdmissionProof)
	if binding != (runtime.RuntimeBinding{}) || existing != nil && existing.remote != nil {
		return admitRemoteCapacity(ctx, city, route, id, now, binding, store)
	}
	if proof, ok := ctx.Value(capacityAdmissionKey{}).(*capacityAdmissionProof); ok {
		if !pathutil.SamePath(city, proof.city) || route != proof.route || id != proof.id {
			return ctx, false, "capacity_claim_identity_mismatch"
		}
		if proof.used.Load() {
			return ctx, false, "capacity_session_already_claimed"
		}
		if proof.native == nil && (now.Before(proof.minted) || now.After(proof.deadline)) {
			return ctx, false, "capacity_census_stale"
		}
		return ctx, true, "capacity_grant_prepared"
	}
	c, nativeMode, err := readNativeCapacityConfig(city)
	if err != nil {
		return ctx, false, "capacity_scope_invalid"
	}
	if nativeMode && managedCapacityRoute(route, c.Queue.Exact, c.Queue.Prefixes) {
		native, err := claimNativeCapacityStart(ctx, city, route, id, now, c, host, sp, store)
		if err != nil {
			return ctx, false, err.Error()
		}
		proof := &capacityAdmissionProof{city: city, route: route, id: id, minted: now, native: native}
		return context.WithValue(ctx, capacityAdmissionKey{}, proof), true, "native_capacity_reserved"
	}
	var deadline time.Time
	allowed, reason := claimCapacityStart(city, route, id, now, sp, store, &deadline)
	if !allowed || deadline.IsZero() {
		return ctx, allowed, reason
	}
	proof := &capacityAdmissionProof{city: city, route: route, id: id, minted: now, deadline: deadline}
	return context.WithValue(ctx, capacityAdmissionKey{}, proof), true, reason
}

// WithCapacityAdmission transfers only the private admission proof to an
// execution context, preserving that context's cancellation and deadlines. A remote source-effect
// marker survives independently when Prepare had no trustworthy reply/proof.
func WithCapacityAdmission(ctx, admitted context.Context) context.Context {
	if admitted == nil {
		return ctx
	}
	if effect, ok := admitted.Value(remoteSourceEffectKey{}).(RemoteAdmissionRequest); ok {
		ctx = context.WithValue(ctx, remoteSourceEffectKey{}, effect)
	}
	if proof, ok := admitted.Value(capacityAdmissionKey{}).(*capacityAdmissionProof); ok {
		return context.WithValue(ctx, capacityAdmissionKey{}, proof)
	}
	return ctx
}

func (m *Manager) admitRuntime(ctx context.Context, id, route string) (context.Context, error) {
	admitted, allowed, reason := AdmitCapacityStart(ctx, m.cityPath, route, id, m.now().UTC(), m.sp, m.store)
	if !allowed {
		return admitted, remoteSourceHold(admitted, CapacityAdmissionError(reason))
	}
	return admitted, nil
}

// startRuntime consumes the private proof exactly once before provider.Start.
// Every caller admits before orphan cleanup or continuation mutation; a fresh
// retry cannot reuse the claim after an ambiguous provider start.
func (m *Manager) startRuntime(ctx context.Context, name string, cfg runtime.Config) error {
	admitted, err := m.admitRuntime(ctx, cfg.Env["GC_SESSION_ID"], cfg.Env["GC_TEMPLATE"])
	if err != nil {
		return err
	}
	if proof, ok := admitted.Value(capacityAdmissionKey{}).(*capacityAdmissionProof); ok {
		if !proof.used.CompareAndSwap(false, true) {
			return CapacityAdmissionError("capacity_session_already_claimed")
		}
		if proof.remote != nil {
			begin, launchCfg, beginErr := beginRemoteCapacityStart(admitted, proof.remote, name, cfg)
			if beginErr != nil {
				return remoteSourceHold(admitted, CapacityAdmissionError(beginErr.Error()))
			}
			startErr := m.sp.Start(admitted, name, launchCfg)
			// Cancellation is not VM exit evidence. Observe using an independent,
			// bounded context; ambiguous authority results retain their VM seat.
			finalCtx, cancel := context.WithTimeout(context.WithoutCancel(admitted), 10*time.Second)
			defer cancel()
			if finalErr := finishRemoteCapacityStart(finalCtx, proof.remote, begin, startErr); finalErr != nil {
				return remoteSourceHold(admitted, errors.Join(startErr, CapacityAdmissionError(finalErr.Error())))
			}
			return remoteSourceHold(admitted, startErr)
		}
		if proof.native != nil {
			cfg, err = beginNativeCapacityStart(admitted, proof, name, cfg)
			if err != nil {
				return errors.Join(CapacityAdmissionError(err.Error()), abandonNativeReservedSeat(proof))
			}
			startErr := m.sp.Start(admitted, name, cfg)
			// Persistence after a canceled provider call still matters. A failed
			// finalization leaves the durable launch-in-flight seat occupied.
			finalCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			stopCancellation := context.AfterFunc(admitted, cancel)
			defer stopCancellation()
			finalErr := finishNativeCapacityStart(finalCtx, proof, startErr, m.now().UTC())
			if finalErr != nil {
				return errors.Join(startErr, CapacityAdmissionError(finalErr.Error()))
			}
			return errors.Join(startErr, finalErr)
		}
	}
	return m.sp.Start(admitted, name, cfg)
}

// ErrCapacityAdmission identifies a local pre-launch denial separately from a
// provider refusal after launch. The controller defers it without wake-failure
// accounting or pending-create rollback.
var ErrCapacityAdmission = errors.New("capacity admission refused")

// CapacityAdmissionError keeps the manager's typed retryable-refusal path.
func CapacityAdmissionError(reason string) error {
	return &runtime.CapacityError{Source: "admission", Err: fmt.Errorf("%w: %s", ErrCapacityAdmission, reason)}
}
