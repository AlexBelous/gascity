package orders

import (
	"context"
	"time"

	"github.com/gastownhall/gascity/internal/beads"
)

// trackingBackend is the ORDERS-LEG persistence surface behind the Store front
// door: every operation that reads or writes order tracking records in the
// orders class. It is the seam the per-class backend split plugs into — the
// bead/label codec lives in beadsTracking (the bd/beads backend), and an
// embedded-SQLite backend (internal/classdb/orders) implements the same
// surface with typed columns.
//
// The interface deliberately covers only the orders leg. The mixed
// orders+graph reads (Store.LastRun / Cursor / HasOpenWork) compose an
// orders-leg half from this interface (LastRunTracking / CursorTracking /
// HasOpenTracking) with a graph-leg read the Store performs itself, because
// the order-run:<scoped> evidence rides both order-tracking records (orders
// class) and wisp/molecule roots (graph class).
//
// The type is unexported on purpose: consumers hold *Store, never a backend.
// Method names are exported so an implementation in another package satisfies
// the interface structurally.
type trackingBackend interface {
	// CreateRun creates an OPEN tracking record (the in-flight single-flight
	// marker whose CreatedAt advances the cooldown clock).
	CreateRun(scoped string, opts RunOpts) (OrderRun, error)
	// CreateRunClosed creates a tracking record, optionally stamps a cursor
	// and outcome, then closes it — the cooldown-advance-only path.
	CreateRunClosed(scoped string, outcome RunOutcome, cursor *EventCursor, closeReason string) (OrderRun, error)
	// SetOutcome stamps the terminal outcome on an existing run.
	SetOutcome(runID string, outcome RunOutcome) error
	// SetCursor persists the event-bus cursor high-water mark on a run.
	SetCursor(runID, scoped string, cursor EventCursor) error
	// MarkFailed stamps the failure outcome plus (when cursor is non-nil) the
	// event cursor in ONE atomic mutation.
	MarkFailed(runID, scoped string, outcome RunOutcome, cursor *EventCursor) error
	// CloseRun closes one run, stamping close_reason when non-empty.
	CloseRun(runID, reason string) error
	// CloseRuns closes a batch of runs with a shared close reason and reports
	// how many closed.
	CloseRuns(ctx context.Context, ids []string, reason string) (int, error)
	// CloseRunsSwept closes a batch of runs with the stale-sweep audit
	// vocabulary: close_reason plus the sweep marker and initiator (metadata
	// keys on the beads backend, the sweep_by column on sqlite).
	CloseRunsSwept(ctx context.Context, ids []string, reason, sweptBy string) (int, error)
	// DeleteRun permanently removes one tracking record (the retention path).
	DeleteRun(runID string) error
	// Get reads one run by id.
	Get(handle string) (OrderRun, error)
	// RunDetail reads one run by id together with its exec-gate output.
	RunDetail(handle string) (RunDetail, error)
	// RecentRuns lists runs for one scoped order newest-first, incl. closed.
	RecentRuns(scoped string, limit int) ([]OrderRun, error)
	// RecentRunsAll lists up to limit runs across every order newest-first,
	// incl. closed — the dispatch cooldown history index fold.
	RecentRunsAll(limit int) ([]OrderRun, error)
	// OpenRuns lists the OPEN runs across every order — the single-flight
	// open-tracking index fold.
	OpenRuns() ([]OrderRun, error)
	// StaleOpenRuns lists OPEN runs created at or before cutoff.
	StaleOpenRuns(cutoff time.Time) ([]OrderRun, error)
	// OrphanedOpenRuns lists OPEN runs except pre-dispatch trigger-env-failure
	// markers — the startup orphan sweep read.
	OrphanedOpenRuns() ([]OrderRun, error)
	// ClosedRunsForRetention lists the CLOSED runs across every order — the
	// read half of the closed-tracking retention prune.
	ClosedRunsForRetention() ([]OrderRun, error)
	// ListTracking lists every tracking record newest-first — the orders feed
	// scan.
	ListTracking() ([]OrderRun, error)
	// LatestOpenRun returns the newest OPEN run for one scoped order.
	LatestOpenRun(scoped string) (OrderRun, bool, error)
	// LastRunTracking is the orders-leg half of the mixed LastRun read: the
	// most recent run time recorded in this backend, tolerant of partial-tier
	// errors (an error is returned only when no usable row survived).
	LastRunTracking(name string) (time.Time, error)
	// CursorTracking is the orders-leg half of the mixed Cursor read: the max
	// event seq recorded in this backend (read failures are logged, not
	// returned, matching the dispatcher's original tolerance).
	CursorTracking(name string) EventCursor
	// HasOpenTracking is the orders-leg half of the mixed HasOpenWork read.
	// On a single-store city the orders leg also holds the wisp/molecule
	// roots, so the backend defers non-tracking order-run rows to the
	// injected graph-walk predicate; a backend whose rows are only tracking
	// records (sqlite) ignores the predicate.
	HasOpenTracking(scoped string, wispHasOpenWork func(store beads.Store, root beads.Bead) (bool, error)) (bool, error)
}

// beadsBacked is the optional graph-leg dedupe seam: a backend that is itself
// a beads.Store view exposes that store so the mixed reads can recognize the
// single-store city (orders leg == graph leg) and read it exactly once.
// Backends that are not beads-backed (sqlite) simply do not implement it —
// the graph leg then always unions.
type beadsBacked interface {
	underlying() beads.Store
}
