package orders

import (
	"context"
	"time"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/convergence"
)

// This file holds the order-class typed READ surface plus the mixed
// orders+graph reads. The orders-leg bodies live on the trackingBackend
// (tracking_beads.go for the bd backend); cmd/gc and internal/api hold
// OrderRun values (or typed verdicts) rather than raw order beads. The mixed
// reads (LastRun / Cursor / HasOpenWork) are composed HERE: an orders-leg half
// answered by the backend, unioned with a graph-leg read the Store performs
// itself, deduped via graphLeg() on a single-store city.

// Get reads the tracking/run record named by handle and projects it onto an
// OrderRun. The handle arrives WITH orders-class context (a typed order endpoint
// or list), so no class discovery is needed. A record that carries no order-run
// label / order: title still decodes (best-effort scoped name) so a caller that
// already holds a valid handle gets its fields back. Provided as the typed
// by-id contract; the API order-history-detail path (an exempt by-id federation
// surface that still emits raw labels on the wire) will migrate onto it in
// WI-6/7.
func (s *Store) Get(handle string) (OrderRun, error) {
	return s.tracking.Get(handle)
}

// RunDetail is the by-id detail projection: an OrderRun paired with the run's
// exec-gate output. It is provided as the typed by-id contract that will back the
// order-history-detail handler once that path migrates off its raw bead + inline
// convergence.gate_* crack (WI-6/7); the handler is an exempt by-id federation
// surface today and has no production caller here yet.
type RunDetail struct {
	// Run is the decoded order run.
	Run OrderRun
	// Gate is the run's captured exec-gate output (empty when the run has none).
	Gate convergence.GateOutput
}

// RunDetail reads the tracking/run record named by handle and projects it onto
// a RunDetail (OrderRun + the run's gate output). The gate-output vocabulary
// stays owned by internal/convergence; only the typed GateOutput escapes.
func (s *Store) RunDetail(handle string) (RunDetail, error) {
	return s.tracking.RunDetail(handle)
}

// RecentRunsAll lists up to limit tracking records across EVERY order
// (newest-first, including closed), decoded into OrderRun. It folds the
// dispatcher's cooldown history index (order_dispatch.go
// historyEntriesForStore) without per-handle Gets — a per-handle read
// reintroduces the cold-cache serial-query hang (#3201/#2893). Records with no
// resolvable order name are skipped, exactly like the index fold.
func (s *Store) RecentRunsAll(limit int) ([]OrderRun, error) {
	return s.tracking.RecentRunsAll(limit)
}

// OpenRuns lists the OPEN tracking records across every order (newest-first),
// decoded into OrderRun. It folds the dispatcher's single-flight open-tracking
// index (order_dispatch.go entriesForStore). Records with no resolvable order
// name are skipped.
func (s *Store) OpenRuns() ([]OrderRun, error) {
	return s.tracking.OpenRuns()
}

// StaleOpenRuns lists OPEN tracking records created at or before cutoff,
// decoded into OrderRun. It is the typed read half of the stale-order-tracking
// sweep: the caller applies any order-name filter (run.Scoped), close budget,
// and the sweep-vocabulary metadata close. Names are resolved best-effort
// (Scoped is "" for an unresolvable record), matching the sweep's "when no
// order filter is set, close every stale tracking bead" behavior.
func (s *Store) StaleOpenRuns(cutoff time.Time) ([]OrderRun, error) {
	return s.tracking.StaleOpenRuns(cutoff)
}

// OrphanedOpenRuns lists every OPEN tracking record EXCEPT pre-dispatch
// trigger-env-failure markers (which the open-work gate intentionally keeps
// open until the normal stale sweep), decoded into OrderRun. It is the typed
// read half of the orphaned-order-tracking startup sweep; the caller closes
// the returned runs via CloseRuns. Names are best-effort (the sweep closes by
// ID and does not resolve names).
func (s *Store) OrphanedOpenRuns() ([]OrderRun, error) {
	return s.tracking.OrphanedOpenRuns()
}

// ClosedRunsForRetention lists the CLOSED tracking records across every order
// (newest-first), decoded into OrderRun — the read half of the closed-tracking
// retention prune. The caller buckets by order name (using the legacy bucket
// for an unresolvable name), keeps the recent-history floor, and deletes the
// aged remainder.
func (s *Store) ClosedRunsForRetention() ([]OrderRun, error) {
	return s.tracking.ClosedRunsForRetention()
}

// CloseRuns closes a batch of tracking records, stamping close_reason so
// validation.on-close cities accept the close, then re-verifies that every id
// closed. It returns the number of records actually closed. ctx cancels the
// beads backend's inter-attempt backoff (see tracking_beads.go for the
// close-verify retry twin and its drift guard).
func (s *Store) CloseRuns(ctx context.Context, ids []string, reason string) (int, error) {
	return s.tracking.CloseRuns(ctx, ids, reason)
}

// MarkFailed stamps the failure outcome on a tracking record, appending the
// event cursor when the order is event-triggered with a non-nil cursor, as ONE
// atomic mutation (see beadsTracking.MarkFailed for why one write is
// load-bearing). cursor is nil for non-event triggers, matching the caller's
// a.Trigger=="event" && headSeq>0 guard.
//
// It returns the backend's error unwrapped: the sole caller (the dispatcher's
// markTrackingFailure) logs it under its own "failed to mark tracking bead %s
// as failed: %v" context, so wrapping here would double the context in the
// operator log.
func (s *Store) MarkFailed(runID, scoped string, outcome RunOutcome, cursor *EventCursor) error {
	return s.tracking.MarkFailed(runID, scoped, outcome, cursor)
}

// LastRun reports the most recent run time (the cooldown clock) for the named
// order, unioning the order-run:<name> evidence across the orders leg and the
// graph leg. It is a MIXED orders+graph read: the order-run label rides both
// order-tracking records (orders class) and wisp/molecule roots (graph class),
// so reading only one class would miss the other under a graph-store split
// (cursor/cooldown regression). Each leg preserves the dispatcher's original
// bare-List tier and its partial-tier-error tolerance (surviving rows win; the
// error is logged, not returned, once any row is in hand).
func (s *Store) LastRun(name string) (time.Time, error) {
	latest, err := s.tracking.LastRunTracking(name)
	if err != nil {
		return time.Time{}, err
	}
	if g := s.graphLeg(); g != nil {
		graphLatest, err := lastRunLeg(g, name)
		if err != nil {
			return time.Time{}, err
		}
		if graphLatest.After(latest) {
			latest = graphLatest
		}
	}
	return latest, nil
}

// Cursor reports the max event seq (the order's event-bus high-water mark) for
// the named order, unioning across the orders leg and the graph leg. Like
// LastRun it is a MIXED orders+graph read (the seq labels ride both tracking
// records and wisp roots); each leg preserves the dispatcher's original
// bare-List tier and partial-tier-error tolerance.
func (s *Store) Cursor(name string) EventCursor {
	latest := uint64(s.tracking.CursorTracking(name))
	if g := s.graphLeg(); g != nil {
		if seq := cursorLeg(g, name); seq > latest {
			latest = seq
		}
	}
	return EventCursor(latest)
}

// HasOpenWork reports whether any in-flight work exists for the scoped order:
// an open order-tracking record (orders class), or an open wisp/molecule root
// whose subtree still holds open work (graph class). It is a MIXED
// orders+graph read: the orders-leg half is answered by the backend
// (HasOpenTracking, which on a single-store city also walks colocated wisp
// roots), then the graph leg is unioned when distinct. wispHasOpenWork is the
// graph-walk predicate that stays graph-owned in the controller (the
// wisp-subtree traversal is graph residual). Only the boolean verdict escapes
// the edge.
func (s *Store) HasOpenWork(scoped string, wispHasOpenWork func(store beads.Store, root beads.Bead) (bool, error)) (bool, error) {
	open, err := s.tracking.HasOpenTracking(scoped, wispHasOpenWork)
	if err != nil || open {
		return open, err
	}
	if g := s.graphLeg(); g != nil {
		return hasOpenWorkLeg(g, scoped, wispHasOpenWork)
	}
	return false, nil
}
