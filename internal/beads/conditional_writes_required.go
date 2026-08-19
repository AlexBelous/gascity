package beads

// The REQUIRED half of conditional writes: capability with no policy in it.
//
// ResolveConditionalWriter answers a rollout question — "does this city's
// configured mode, ANDed with what this store can do, permit a fenced write
// here?" — and its nil return is a legitimate answer meaning "take the legacy
// path". That is the right shape for a gate being rolled out across a fleet.
//
// It is the wrong shape for a caller whose correctness DEPENDS on the fence.
// The keyed reconciler owns session rows: it reads a revision, decides, and
// writes back under that revision, so a stale owner cannot overwrite a fresher
// one. Handing that caller a nil writer does not degrade it to a safe legacy
// path — it removes the fence the decision was made under, silently. Such a
// caller must state the requirement instead, and a store that cannot meet it
// must produce a named error at startup rather than an absence at write time.

import (
	"errors"
	"fmt"
)

// ConditionalWritesUnavailableError reports that a store cannot perform
// conditional writes at all. It is a capability fact about the store, not a
// policy decision about the city: no mode participates, and no configuration
// makes it go away.
type ConditionalWritesUnavailableError struct {
	// StoreKind names the store in the diagnostic vocabulary.
	StoreKind string
	// Reason carries the capability answer verbatim.
	Reason string
}

// Error reports the missing capability and the store that lacks it.
func (e *ConditionalWritesUnavailableError) Error() string {
	if e == nil {
		return "<nil>"
	}
	return fmt.Sprintf("conditional writes unavailable: store=%s reason=%q", e.StoreKind, e.Reason)
}

// RequiredConditionalWriter returns the store's conditional writer, or a typed
// error naming the store and what it lacks.
//
// Capability IS the interface implementation (plus whatever the store's own
// probe says about the database in front of it) — never a stamp, never a mode.
// A store that implements ConditionalWriter and reports itself capable yields
// its writer here whatever the city's rollout gate says, because the caller
// asking this question is not offering the city a choice.
//
// Wrapper following matches the resolve seam exactly, so a class front door or
// a cache wrapper resolves to the same store the write path would use.
func RequiredConditionalWriter(store Store) (ConditionalWriter, error) {
	if store == nil {
		return nil, &ConditionalWritesUnavailableError{StoreKind: "<nil>", Reason: "no store"}
	}
	resolved := followConditionalWritesResolveTarget(store)
	kind := conditionalStoreKind(resolved)
	writer, ok := ConditionalWriterFor(resolved)
	if !ok {
		return nil, &ConditionalWritesUnavailableError{
			StoreKind: kind,
			Reason:    "the store does not implement conditional writes",
		}
	}
	if prober, ok := resolved.(conditionalWriteCapabilityProber); ok {
		if capable, why := prober.probeConditionalWriteCapability(); !capable {
			if why == "" {
				why = "the store reports it cannot fence a write"
			}
			return nil, &ConditionalWritesUnavailableError{StoreKind: kind, Reason: why}
		}
	}
	return writer, nil
}

// IsConditionalWritesUnavailable reports whether err is or wraps a
// *ConditionalWritesUnavailableError.
func IsConditionalWritesUnavailable(err error) bool {
	var cue *ConditionalWritesUnavailableError
	return errors.As(err, &cue)
}
