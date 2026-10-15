// Package observationattempt bounds reacquisition of an entire authenticated
// observation. It supplies neither census validation nor admission authority.
package observationattempt

import (
	"context"
	"encoding/hex"
	"time"
)

// Decision must come from the owning strict validator and typed failure policy.
type Decision uint8

// Fatal, Complete, and TransientCoverage are the owning validator's decisions
// for one whole observation attempt.
const (
	Fatal Decision = iota
	Complete
	TransientCoverage
)

// Rejected is Fatal under a name that cannot be mistaken for a process-exit API
// by source scanners in command packages.
const Rejected = Fatal

// Frame identifies an independently authenticated helper exchange.
type Frame struct {
	Nonce      string    `json:"nonce"`
	StartedAt  time.Time `json:"started_at"`
	FinishedAt time.Time `json:"finished_at"`
}

// Identity binds a whole attempt, including its two original helper frames.
type Identity struct {
	BindingFingerprint  string    `json:"binding_fingerprint"`
	ProviderFingerprint string    `json:"provider_fingerprint"`
	RootFingerprint     string    `json:"root_fingerprint"`
	WholeStartedAt      time.Time `json:"whole_started_at"`
	WholeFinishedAt     time.Time `json:"whole_finished_at"`
	Frames              [2]Frame  `json:"frames"`
}

// Receipt describes immutable collected typed-envelope serialization, not raw stdout.
type Receipt struct {
	Path     string `json:"path"`
	SHA256   string `json:"sha256"`
	Bytes    int64  `json:"bytes"`
	Complete bool   `json:"complete"`
}

// Hooks are mandatory seams. Collect must perform a fresh whole transaction;
// Validate must include every existing strict proof and provider join.
type Hooks[T any] struct {
	Collect   func(context.Context) T
	Validate  func(T) Decision
	Identity  func(T) Identity
	Invariant func(context.Context) error
	Retain    func(context.Context, T) (Receipt, error)
	// Record persists a candidate audit, not a final outcome. Cancellation or
	// an invariant change during its I/O must still deny the public observation.
	Record func(context.Context, Receipt, Identity, Identity, T) (Receipt, error)
	Now    func() time.Time
}

// Result never promotes a partial value. Selected is zero unless one complete
// transaction independently passed all validation and cross-attempt fences.
type Result[T any] struct {
	Value    T
	Attempts int
	Selected int
	Reason   string
	Retained Receipt
	Readback Receipt // immutable candidate audit; only Selected is the outcome
}

func digest(s string) bool {
	if len(s) != 64 {
		return false
	}
	b, err := hex.DecodeString(s)
	return err == nil && len(b) == 32 && hex.EncodeToString(b) == s
}

func identityValid(v Identity, now time.Time) bool {
	if !digest(v.BindingFingerprint) || !digest(v.ProviderFingerprint) || !digest(v.RootFingerprint) {
		return false
	}
	if v.WholeStartedAt.IsZero() || v.WholeFinishedAt.Before(v.WholeStartedAt) || v.WholeFinishedAt.After(now) {
		return false
	}
	for i, f := range v.Frames {
		if !digest(f.Nonce) || f.StartedAt.Before(v.WholeStartedAt) || f.FinishedAt.Before(f.StartedAt) || f.FinishedAt.After(v.WholeFinishedAt) {
			return false
		}
		if i > 0 && (f.Nonce == v.Frames[i-1].Nonce || f.StartedAt.Before(v.Frames[i-1].FinishedAt)) {
			return false
		}
	}
	return true
}

// Run performs at most two whole attempts under one deadline of at most25s.
// It starts no goroutine and never releases the caller's global flight slot.
// Cancellation cannot make an uncooperative Collect disappear; the owning
// service must retain its flight until this call actually terminates.
func Run[T any](parent context.Context, h Hooks[T]) Result[T] {
	var out Result[T]
	if h.Collect == nil || h.Validate == nil || h.Identity == nil || h.Invariant == nil || h.Retain == nil || h.Record == nil {
		out.Reason = "invalid_hooks"
		return out
	}
	if h.Now == nil {
		h.Now = time.Now
	}
	ctx, cancel := context.WithDeadline(parent, time.Now().Add(25*time.Second))
	defer cancel()
	check := func() bool {
		if ctx.Err() != nil || h.Invariant(ctx) != nil || ctx.Err() != nil {
			out.Reason = "canceled_or_invariant_changed"
			return false
		}
		return true
	}
	var first Identity
	for attempt := 1; attempt <= 2; attempt++ {
		if !check() {
			return out
		}
		out.Value = h.Collect(ctx)
		out.Attempts = attempt
		if !check() {
			return out
		}
		decision := h.Validate(out.Value)
		if decision != Complete && decision != TransientCoverage {
			out.Reason = "ineligible_failure"
			return out
		}
		id := h.Identity(out.Value)
		if !identityValid(id, h.Now()) {
			out.Reason = "invalid_attempt_identity"
			return out
		}
		if attempt == 2 {
			if first.BindingFingerprint != id.BindingFingerprint || first.ProviderFingerprint != id.ProviderFingerprint || first.RootFingerprint != id.RootFingerprint || id.WholeStartedAt.Before(first.WholeFinishedAt) {
				out.Reason = "attempt_identity_changed"
				return out
			}
			for _, old := range first.Frames {
				for _, fresh := range id.Frames {
					if old.Nonce == fresh.Nonce {
						out.Reason = "replayed_attempt_frame"
						return out
					}
				}
			}
		}
		if decision == Complete {
			if attempt == 2 {
				var err error
				out.Readback, err = h.Record(ctx, out.Retained, first, id, out.Value)
				if err != nil || !out.Readback.Complete || !digest(out.Readback.SHA256) || out.Readback.Bytes <= 0 || out.Readback.Bytes > MaxCandidateRecordBytes {
					out.Reason = "candidate_readback_unavailable"
					return out
				}
			}
			if !check() {
				return out
			}
			out.Selected = attempt
			out.Reason = "complete_whole_attempt"
			return out
		}
		if attempt == 2 {
			out.Reason = "second_attempt_incomplete"
			return out
		}
		first = id
		var err error
		out.Retained, err = h.Retain(ctx, out.Value)
		if err != nil || !out.Retained.Complete || !digest(out.Retained.SHA256) || out.Retained.Bytes <= 0 || out.Retained.Bytes > MaxEncodedAttemptBytes {
			out.Reason = "failed_attempt_retention_unavailable"
			return out
		}
	}
	return out
}
