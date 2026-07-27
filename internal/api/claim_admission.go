package api

import (
	"os"
	"strconv"
)

// defaultClaimAdmissionSlots bounds how many bead-claim mutations the controller
// admits concurrently. The cure routes every worker's claim through the
// controller's pooled NativeDoltStore instead of a per-hook SQL client, so this
// ceiling is what keeps a claim storm from piling unboundedly onto that pool:
// beyond it, a claim fails fast with a retryable 503 rather than queueing behind
// the bounded connection and dragging the shared Dolt server toward its own
// connection limit. It is deliberately small — the store's SQL pool is bounded
// to a handful of connections (slice 4), so admitting far more mutation handlers
// than that only grows a wait queue, never throughput.
const defaultClaimAdmissionSlots = 32

// claimAdmissionSlotsEnv lets operators tune the ceiling without a rebuild. An
// unset, empty, non-integer, or non-positive value falls back to the default;
// admission never fails open (unbounded) on a bad value.
const claimAdmissionSlotsEnv = "GC_CLAIM_ADMISSION_SLOTS"

// claimAdmitter is a fixed-size, non-blocking admission gate for claim
// mutations. tryAcquire never blocks: it either takes a slot and returns a
// release func, or reports saturation so the caller can fail fast.
type claimAdmitter struct {
	slots chan struct{}
}

// newClaimAdmitter builds an admitter with n slots. n <= 0 is clamped to 1 so a
// misconfiguration degrades to full serialization, never to an unbounded (nil
// channel, always-saturated) or panicking gate.
func newClaimAdmitter(n int) *claimAdmitter {
	if n <= 0 {
		n = 1
	}
	return &claimAdmitter{slots: make(chan struct{}, n)}
}

// tryAcquire takes a slot without blocking. When ok is true the caller MUST call
// the returned release exactly once (typically via defer). When ok is false the
// gate is saturated and release is a no-op.
func (a *claimAdmitter) tryAcquire() (release func(), ok bool) {
	select {
	case a.slots <- struct{}{}:
		return func() { <-a.slots }, true
	default:
		return func() {}, false
	}
}

// defaultClaimAdmitter is the process-wide gate shared by every per-city Server
// (the supervisor caches one Server per city, but they all point at this single
// admitter, making the bound process-wide as the design requires). Tests
// override Server.claimAdmitter with a small-capacity gate to exercise
// saturation deterministically.
var defaultClaimAdmitter = newClaimAdmitter(resolveClaimAdmissionSlots())

// resolveClaimAdmissionSlots reads the env override, falling back to the default
// on any absent/invalid value.
func resolveClaimAdmissionSlots() int {
	raw, ok := os.LookupEnv(claimAdmissionSlotsEnv)
	if !ok {
		return defaultClaimAdmissionSlots
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		return defaultClaimAdmissionSlots
	}
	return n
}
