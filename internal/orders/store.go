package orders

import (
	"strings"
	"time"

	"github.com/gastownhall/gascity/internal/beads"
)

// This file is the order-class front-door skeleton per
// OBJECT-MODEL-FRONT-DOOR-DESIGN sec 3.4 / 6.4.
//
// Unlike session (Info), nudge (Item), and mail (Message), the order object has
// NO pre-existing domain type. OrderRun / RunOutcome / EventCursor are net-new
// and designed here. The dispatcher deliberately exploits bead mechanics, and
// the typed API NAMES them rather than hiding them:
//
//   - CreatedAt on the tracking bead is the COOLDOWN CLOCK (LastRun reads it).
//   - an OPEN tracking bead == an in-flight single-flight marker.
//   - created-then-closed-immediately == cooldown-advance-only.
//   - reads union the two tiers (wisps + issues) — TierBoth.
//
// The Store methods (CreateRun, CreateRunClosed, SetOutcome, SetCursor,
// CloseRun, RecentRuns) emit byte-identical bead writes to the raw ops they
// replace and are wired into order_dispatch.go / cmd_order.go. The cooldown
// clock (last-run) and event-cursor READS the dispatch gate uses go through the
// Store's mixed orders+graph reads (LastRun / Cursor / HasOpenWork, and the
// LastRunAcross / CursorAcross federation helpers), which the in-memory tracking
// index batches per store — see cmd/gc/order_dispatch.go and store_reads.go.

// Order-class label constants. These MUST stay in sync with the canonical
// declarations in cmd/gc/order_dispatch.go and the private mirrors in
// internal/coordclass (guarded by the coordclass drift test). They are
// re-declared here only so the codec edge can build label sets without
// importing package main (a layering inversion).
const (
	labelOrderTracking    = "order-tracking"
	labelOrderRunPrefix   = "order-run:"
	labelOrderTitlePrefix = "order:"
	labelSeqPrefix        = "seq:"

	labelExec           = "exec"
	labelExecFailed     = "exec-failed"
	labelExecEnvFailed  = "exec-env-failed"
	labelWisp           = "wisp"
	labelWispFailed     = "wisp-failed"
	labelWispCanceled   = "wisp-canceled"
	labelTriggerEnvFail = "trigger-env-failed"
)

// RunOutcome enumerates the terminal outcome of an order run. Each value maps
// to a fixed label set that the dispatcher stamps on the tracking bead. The
// zero value (RunOutcomeNone) means "no outcome stamped yet" (an open,
// in-flight run).
type RunOutcome int

const (
	// RunOutcomeNone is the zero value: no terminal outcome stamped (in-flight).
	RunOutcomeNone RunOutcome = iota
	// RunOutcomeExec — synchronous trigger executed successfully.
	RunOutcomeExec
	// RunOutcomeExecFailed — synchronous trigger ran but failed.
	RunOutcomeExecFailed
	// RunOutcomeExecEnvFailed — synchronous trigger failed building its env.
	RunOutcomeExecEnvFailed
	// RunOutcomeWisp — wisp dispatch succeeded.
	RunOutcomeWisp
	// RunOutcomeWispFailed — wisp dispatch failed.
	RunOutcomeWispFailed
	// RunOutcomeWispCanceled — wisp dispatch was canceled.
	RunOutcomeWispCanceled
	// RunOutcomeTriggerEnvFailed — pre-dispatch trigger env build failed.
	RunOutcomeTriggerEnvFailed
)

// Labels returns the exact label set the dispatcher stamps for this outcome,
// matching cmd/gc/order_dispatch.go verbatim. RunOutcomeNone returns nil.
func (o RunOutcome) Labels() []string {
	switch o {
	case RunOutcomeExec:
		return []string{labelExec}
	case RunOutcomeExecFailed:
		return []string{labelExecFailed}
	case RunOutcomeExecEnvFailed:
		return []string{labelExecEnvFailed}
	case RunOutcomeWisp:
		return []string{labelWisp}
	case RunOutcomeWispFailed:
		return []string{labelWisp, labelWispFailed}
	case RunOutcomeWispCanceled:
		return []string{labelWisp, labelWispCanceled}
	case RunOutcomeTriggerEnvFailed:
		return []string{labelTriggerEnvFail}
	default:
		return nil
	}
}

// IsExec reports whether the outcome belongs to the synchronous-exec family
// (Exec, ExecFailed, ExecEnvFailed). It is the typed replacement for the order
// feed's exec-label fallback (orderLabelsContainExec) used to derive an exec
// target/type for a run whose order definition is no longer registered.
func (o RunOutcome) IsExec() bool {
	switch o {
	case RunOutcomeExec, RunOutcomeExecFailed, RunOutcomeExecEnvFailed:
		return true
	default:
		return false
	}
}

// Display returns the human-facing outcome string the check/history API reports
// for a run: "" for no outcome yet, "success" for a clean exec or wisp dispatch,
// "failed" for any failure family (exec/env/trigger failure or a failed wisp),
// and "canceled" for a canceled wisp. It is the typed replacement for the API's
// lastRunOutcomeFromLabels label crack.
func (o RunOutcome) Display() string {
	switch o {
	case RunOutcomeExec, RunOutcomeWisp:
		return "success"
	case RunOutcomeExecFailed, RunOutcomeExecEnvFailed, RunOutcomeWispFailed, RunOutcomeTriggerEnvFailed:
		return "failed"
	case RunOutcomeWispCanceled:
		return "canceled"
	default:
		return ""
	}
}

// EventCursor is the per-order event-bus cursor, encoded on the tracking bead
// as the label pair ("order:<scoped>", "seq:<N>"). It is the high-water mark of
// events the order has already consumed.
type EventCursor uint64

// OrderRun is the net-new domain type for one order tracking record. It names
// the load-bearing bead mechanics the dispatcher relies on.
type OrderRun struct {
	// ID is the tracking bead id.
	ID string
	// Scoped is the scoped order name ("<rig>/<agent>" style).
	Scoped string
	// Outcome is the terminal outcome, or RunOutcomeNone for an in-flight run.
	Outcome RunOutcome
	// CreatedAt is the COOLDOWN CLOCK: the dispatcher reads the most recent
	// run's CreatedAt to decide whether the cooldown has elapsed.
	CreatedAt time.Time
	// UpdatedAt is the last-modified time of the tracking bead, or zero for
	// legacy beads that never recorded one. The closed-tracking retention prune
	// uses it (falling back to CreatedAt) as the reference time that orders the
	// recent-history floor — see order_dispatch.go orderTrackingClosedReferenceTime.
	UpdatedAt time.Time
	// Open reports whether the tracking bead is still open. An open run is the
	// in-flight single-flight marker that suppresses repeat dispatch.
	Open bool
	// Cursor is the decoded EventCursor (max seq across the run's labels).
	Cursor EventCursor
}

// State returns the feed-facing lifecycle status of the run: "failed" when the
// terminal outcome is a failure or cancellation, "active" for an open run with
// no failure, and "completed" for a closed run with no failure. It is the exact
// truth-table replacement for the order feed's orderTrackingStatus label crack.
func (r OrderRun) State() string {
	switch r.Outcome.Display() {
	case "failed", "canceled":
		return "failed"
	}
	if r.Open {
		return "active"
	}
	return "completed"
}

// RunOpts configures CreateRun.
type RunOpts struct {
	// Outcome, when non-None, is stamped on the created (open) bead — used by
	// the trigger-env-failed pre-dispatch path which creates an already-labeled
	// open bead so the open-work gate suppresses repeat ticks.
	Outcome RunOutcome
}

// Store is the order-class domain wrapper: the front door every consumer
// holds. It routes all orders-leg operations through a trackingBackend (the
// bead/label codec for the bd backend, typed columns for the sqlite backend)
// and keeps the graph-leg composition itself.
//
// It optionally carries a graph-class leg. The order-run:<scoped> and
// order:<scoped>+seq:<N> labels the dispatcher stamps ride BOTH order-tracking
// records (orders class) AND the wisp/molecule roots created by instantiation
// (graph class). The single-flight and cooldown/cursor reads therefore span two
// classes, so the mixed reads (LastRun, Cursor, HasOpenWork) union order-run
// evidence across the orders leg and the graph leg. On a single-store city the
// two legs wrap the same underlying store and the union deduplicates to a single
// read, so the verdict is byte-identical to the pre-split behavior; under a
// graph-store split they are distinct physical stores and the union is what
// keeps the reads correct (never rebase them onto a single class store — that is
// the single-store-assumption bug the graph-store-split audit root-caused).
type Store struct {
	tracking trackingBackend
	graph    beads.GraphStore
}

// NewStore wraps a strongly-typed orders-class store as the order front door.
// The mixed orders+graph reads (LastRun/Cursor/HasOpenWork) fall back to the
// orders leg alone; on a single-store city that leg's TierBoth reads already see
// the colocated wisp roots, so this is byte-identical to the pre-split behavior.
// Use NewStoreWithGraph where the graph store is separately resolvable so the
// reads stay correct under a graph-store split.
func NewStore(store beads.OrdersStore) *Store {
	return &Store{tracking: beadsTracking{store: store}}
}

// NewStoreWithGraph wraps an orders-class store together with the graph-class
// store that owns its wisp/molecule roots, enabling the mixed orders+graph reads
// to union order-run evidence across both classes.
func NewStoreWithGraph(store beads.OrdersStore, graph beads.GraphStore) *Store {
	return &Store{tracking: beadsTracking{store: store}, graph: graph}
}

// graphLeg returns the graph-class store the mixed reads must union on top of
// the orders-leg backend halves, or nil when there is nothing extra to read:
// no graph leg was supplied, or the backend advertises (via the optional
// beadsBacked assertion) that it wraps the SAME underlying store — the
// single-store city, where reading the graph leg again would double-read.
// A backend that is not beads-backed (sqlite) never dedupes: its rows can
// never be the graph store's, so the graph leg always unions.
func (s *Store) graphLeg() beads.Store {
	if s.graph.Store == nil {
		return nil
	}
	if backed, ok := s.tracking.(beadsBacked); ok {
		if under := backed.underlying(); under != nil && under == s.graph.Store {
			return nil
		}
	}
	return s.graph.Store
}

// trackingTitle returns the canonical tracking-bead title for a scoped order.
func trackingTitle(scoped string) string { return labelOrderTitlePrefix + scoped }

// baseLabels returns the order-run + order-tracking labels every tracking bead
// carries, plus any outcome labels.
func baseLabels(scoped string, outcome RunOutcome) []string {
	labels := []string{labelOrderRunPrefix + scoped, labelOrderTracking}
	return append(labels, outcome.Labels()...)
}

// CreateRun creates an OPEN tracking record for scoped (the in-flight marker
// whose CreatedAt advances the cooldown clock). On the beads backend it is the
// byte-identical replacement for the raw Create sites in order_dispatch.go.
func (s *Store) CreateRun(scoped string, opts RunOpts) (OrderRun, error) {
	return s.tracking.CreateRun(scoped, opts)
}

// SetOutcome stamps the terminal outcome on an existing tracking record. On
// the beads backend it is the byte-identical replacement for the
// store.Update(id, {Labels: outcome}) sites in order_dispatch.go / cmd_order.go.
func (s *Store) SetOutcome(runID string, outcome RunOutcome) error {
	return s.tracking.SetOutcome(runID, outcome)
}

// SetCursor persists the event-bus cursor high-water mark on an existing
// tracking record. Replaces the cursor-persist Update sites in
// order_dispatch.go.
func (s *Store) SetCursor(runID, scoped string, cursor EventCursor) error {
	return s.tracking.SetCursor(runID, scoped, cursor)
}

// CloseRun closes a tracking record, stamping close_reason so
// validation.on-close cities accept it. Replaces the defer-Close /
// immediate-close sites in cmd_order.go.
func (s *Store) CloseRun(runID, reason string) error {
	return s.tracking.CloseRun(runID, reason)
}

// DeleteRun permanently removes one tracking record — the retention path.
// Tracking records are standalone (CreateRun mints them dep-free), so the
// domain delete is a plain per-record delete; the graph-aware dep-unwind
// delete the cmd/gc retention prune performs stays at that call site as graph
// residual.
func (s *Store) DeleteRun(runID string) error {
	return s.tracking.DeleteRun(runID)
}

// CreateRunClosed creates a tracking record, optionally stamps an event cursor
// and outcome, then closes it — the cooldown-advance-only path used by manual
// `gc order run`. The record's CreatedAt advances the cooldown clock, and it is
// closed immediately so a lingering open record is not read as in-flight work
// (ga-jra/ga-lo8c). The returned OrderRun is closed (Open=false).
func (s *Store) CreateRunClosed(scoped string, outcome RunOutcome, cursor *EventCursor, closeReason string) (OrderRun, error) {
	return s.tracking.CreateRunClosed(scoped, outcome, cursor, closeReason)
}

// RecentRuns lists the tracking/order-run records for scoped newest-first
// (including closed), decoded into OrderRun values. It is the typed face of the
// `gc order history` read (cmd_order.go).
func (s *Store) RecentRuns(scoped string, limit int) ([]OrderRun, error) {
	return s.tracking.RecentRuns(scoped, limit)
}

// ListTracking lists every order tracking record newest-first, decoded into
// OrderRun values. It is the typed face of the /v0/orders/feed read it
// replaces. Decoded rows and any list error are returned together (the
// RecentRuns pattern) so callers keep the feed's err-branch semantics.
func (s *Store) ListTracking() ([]OrderRun, error) {
	return s.tracking.ListTracking()
}

// LatestOpenRun returns the newest OPEN order-run record for scoped, if any.
// Closed runs deliberately never surface: the order feed uses the most recent
// OPEN run as the freshness signal for a tracking row's UpdatedAt, so a closed
// run must not advance it. The decoded row, a found flag, and any list error
// are returned together; found can be true alongside a partial-tier error,
// mirroring the feed's prior handling.
func (s *Store) LatestOpenRun(scoped string) (OrderRun, bool, error) {
	return s.tracking.LatestOpenRun(scoped)
}

// RunFromTrackingBead projects an order tracking/run bead onto an OrderRun and
// is the exported decode entry other front-door callers (the API feed/history
// edges) use; decodeRun stays private. It is pure, side-effect-free, and
// backend-invariant (reads only bead fields), mirroring decodeRun and
// session.InfoFromPersistedBead. The scoped order name is taken from the first
// non-empty "order-run:<scoped>" label (identical to the feed's former
// orderTrackingScopedName scan); a bead with no such label is not an order
// tracking record, so ok=false.
func RunFromTrackingBead(b beads.Bead) (OrderRun, bool) {
	for _, label := range b.Labels {
		if scoped, ok := strings.CutPrefix(label, labelOrderRunPrefix); ok {
			if scoped = strings.TrimSpace(scoped); scoped != "" {
				return decodeRun(scoped, b), true
			}
		}
	}
	return OrderRun{}, false
}

// decodeRun projects an order tracking/run bead onto an OrderRun. It is pure,
// side-effect-free, and backend-invariant (reads only bead fields), matching the
// projection-invariance invariant. The cooldown clock (CreatedAt), open flag,
// outcome (from labels), and event cursor (max seq from labels) are decoded here.
func decodeRun(scoped string, b beads.Bead) OrderRun {
	return OrderRun{
		ID:        b.ID,
		Scoped:    scoped,
		Outcome:   outcomeFromLabels(b.Labels),
		CreatedAt: b.CreatedAt,
		UpdatedAt: b.UpdatedAt,
		Open:      b.Status != "closed",
		Cursor:    EventCursor(MaxSeqFromLabels([][]string{b.Labels})),
	}
}

// NameFromOrderRunLabel resolves the scoped order name from a bead's
// order-run:<scoped> label ONLY. Paths that select beads for destructive action
// (force-close wisp-root matching) must use this rather than NameFromTrackingBead
// so a bead can never be selected on its title alone. It is the orders-class
// codec that absorbs order_dispatch.go's orderNameFromOrderRunLabel.
func NameFromOrderRunLabel(b beads.Bead) (string, bool) {
	for _, label := range b.Labels {
		if name, ok := strings.CutPrefix(label, labelOrderRunPrefix); ok && name != "" {
			return name, true
		}
	}
	return "", false
}

// NameFromTrackingBead resolves the scoped order name from the order-run:<scoped>
// label, falling back to the legacy order:<scoped> title prefix used by old
// tracking beads. The title fallback is for tracking-bead selection, cooldown
// history folding, and retention bucketing only; force-close root matching uses
// NameFromOrderRunLabel. It absorbs order_dispatch.go's orderNameFromTrackingBead.
func NameFromTrackingBead(b beads.Bead) (string, bool) {
	if name, ok := NameFromOrderRunLabel(b); ok {
		return name, true
	}
	if name, ok := strings.CutPrefix(b.Title, labelOrderTitlePrefix); ok && name != "" {
		return name, true
	}
	return "", false
}

// decodeTrackingRun projects a tracking bead onto an OrderRun using the
// tracking-bead name resolution (order-run label with the legacy order:<title>
// fallback), matching the dispatcher's cooldown/sweep index folding. ok is false
// when the bead is neither order-run-labeled nor order:-titled.
func decodeTrackingRun(b beads.Bead) (OrderRun, bool) {
	name, ok := NameFromTrackingBead(b)
	if !ok {
		return OrderRun{}, false
	}
	return decodeRun(name, b), true
}

// decodeTrackingRuns decodes a list of tracking beads, skipping any bead with no
// resolvable order name (the same skip the dispatcher's index fold performs).
func decodeTrackingRuns(list []beads.Bead) []OrderRun {
	out := make([]OrderRun, 0, len(list))
	for _, b := range list {
		if run, ok := decodeTrackingRun(b); ok {
			out = append(out, run)
		}
	}
	return out
}

func decodeRuns(scoped string, list []beads.Bead) []OrderRun {
	out := make([]OrderRun, 0, len(list))
	for _, b := range list {
		out = append(out, decodeRun(scoped, b))
	}
	return out
}

// outcomeFromLabels reverses RunOutcome.Labels, reporting the terminal outcome a
// tracking bead's labels encode, or RunOutcomeNone for an in-flight run.
//
// This relies on the invariant that a tracking bead is stamped with exactly ONE
// outcome family: either a single RunOutcome via SetOutcome, or the fixed
// {wisp, wisp-failed} pair from the failure path. Given that, the decode order
// (wisp family before exec/trigger) is unambiguous. A future writer that
// double-stamps mixed families (e.g. {wisp, exec-failed}) would be silently
// reclassified by this precedence; such a case must instead be modeled
// explicitly as its own RunOutcome rather than allowed to fall through here.
func outcomeFromLabels(labels []string) RunOutcome {
	wisp := beadLabelsContain(labels, labelWisp)
	switch {
	case beadLabelsContain(labels, labelWispCanceled):
		return RunOutcomeWispCanceled
	case beadLabelsContain(labels, labelWispFailed):
		return RunOutcomeWispFailed
	case wisp:
		return RunOutcomeWisp
	case beadLabelsContain(labels, labelExecEnvFailed):
		return RunOutcomeExecEnvFailed
	case beadLabelsContain(labels, labelExecFailed):
		return RunOutcomeExecFailed
	case beadLabelsContain(labels, labelExec):
		return RunOutcomeExec
	case beadLabelsContain(labels, labelTriggerEnvFail):
		return RunOutcomeTriggerEnvFailed
	default:
		return RunOutcomeNone
	}
}

func beadLabelsContain(labels []string, want string) bool {
	for _, l := range labels {
		if l == want {
			return true
		}
	}
	return false
}
