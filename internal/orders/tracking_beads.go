package orders

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/convergence"
)

// This file is the bd/beads trackingBackend: the order-class bead/label codec
// bodies, moved verbatim from the Store methods they used to be. Every write
// is byte-identical to the raw bead op it replaced in order_dispatch.go /
// cmd_order.go, and every read preserves its original tier:
//   - RecentRunsAll / OpenRuns (the dispatch cooldown/single-flight index) read
//     the LIVE tier — cache-bypass is the duplicate-dispatch guarantee.
//   - LastRunTracking / CursorTracking preserve the dispatcher's pre-existing
//     bare-List tier so the migration is behavior-preserving.
//   - Get / RunDetail are the by-id detail reads (bare Get).

// The close-verify retry parameters mirror the dispatcher's original
// closeAndVerifyOrderTrackingBeads: three attempts with a short backoff between
// each, so a store that briefly reports a bead still open (Dolt write lag) is
// re-verified rather than treated as a failed close.
const (
	closeVerifyAttempts   = 3
	closeVerifyRetryDelay = 25 * time.Millisecond
)

// beadsTracking implements trackingBackend over a beads.OrdersStore — the
// bd-backed orders leg. A zero value (nil wrapped store) answers reads with
// empty results and writes with errors, matching the pre-extraction nil-store
// behavior of the Store methods.
type beadsTracking struct {
	store beads.OrdersStore
}

// underlying exposes the wrapped raw store for the graph-leg dedupe: on a
// single-store city the graph leg wraps this same store, and the mixed reads
// must read it exactly once to stay byte-identical to the pre-split behavior.
func (b beadsTracking) underlying() beads.Store { return b.store.Store }

var (
	_ trackingBackend = beadsTracking{}
	_ beadsBacked     = beadsTracking{}
)

// CreateRun creates an OPEN tracking bead for scoped. It is the byte-identical
// replacement for the store.Create(beads.Bead{Title:"order:"+scoped, Labels:
// {order-run, order-tracking[, outcome]}, NoHistory:true}) sites in
// order_dispatch.go.
func (b beadsTracking) CreateRun(scoped string, opts RunOpts) (OrderRun, error) {
	created, err := b.store.Create(beads.Bead{
		Title:     trackingTitle(scoped),
		Labels:    baseLabels(scoped, opts.Outcome),
		NoHistory: true,
	})
	if err != nil {
		return OrderRun{}, fmt.Errorf("creating order run for %q: %w", scoped, err)
	}
	return OrderRun{
		ID:        created.ID,
		Scoped:    scoped,
		Outcome:   opts.Outcome,
		CreatedAt: created.CreatedAt,
		Open:      true,
	}, nil
}

// SetOutcome stamps the outcome label set on an existing tracking bead. It is
// the byte-identical replacement for the store.Update(id, {Labels: outcome})
// sites in order_dispatch.go / cmd_order.go.
func (b beadsTracking) SetOutcome(runID string, outcome RunOutcome) error {
	if err := b.store.Update(runID, beads.UpdateOpts{Labels: outcome.Labels()}); err != nil {
		return fmt.Errorf("setting order run outcome on %q: %w", runID, err)
	}
	return nil
}

// SetCursor stamps the event cursor as the label pair (order:<scoped>,
// seq:<N>) on an existing tracking bead. Replaces the cursor-persist Update
// sites in order_dispatch.go.
func (b beadsTracking) SetCursor(runID, scoped string, cursor EventCursor) error {
	labels := []string{
		labelOrderTitlePrefix + scoped,
		fmt.Sprintf("%s%d", labelSeqPrefix, uint64(cursor)),
	}
	if err := b.store.Update(runID, beads.UpdateOpts{Labels: labels}); err != nil {
		return fmt.Errorf("setting order run cursor on %q: %w", runID, err)
	}
	return nil
}

// CloseRun closes a tracking bead, stamping close_reason so validation.on-close
// cities accept it. Replaces the defer-Close / immediate-close sites in
// cmd_order.go.
func (b beadsTracking) CloseRun(runID, reason string) error {
	if reason != "" {
		if err := b.store.SetMetadata(runID, "close_reason", reason); err != nil {
			return fmt.Errorf("stamping close reason on order run %q: %w", runID, err)
		}
	}
	if err := b.store.Close(runID); err != nil {
		return fmt.Errorf("closing order run %q: %w", runID, err)
	}
	return nil
}

// CreateRunClosed creates a tracking bead, optionally stamps an event cursor
// and outcome, then closes it. It emits byte-identical bead writes to the prior
// raw Create + (cursor Update) + (outcome Update) + (close_reason SetMetadata) +
// Close sequence in cmd_order.go. The returned OrderRun is closed (Open=false).
func (b beadsTracking) CreateRunClosed(scoped string, outcome RunOutcome, cursor *EventCursor, closeReason string) (OrderRun, error) {
	created, err := b.store.Create(beads.Bead{
		Title:     trackingTitle(scoped),
		Labels:    baseLabels(scoped, RunOutcomeNone),
		NoHistory: true,
	})
	if err != nil {
		return OrderRun{}, fmt.Errorf("creating closed order run for %q: %w", scoped, err)
	}
	run := OrderRun{ID: created.ID, Scoped: scoped, CreatedAt: created.CreatedAt}
	if cursor != nil {
		if err := b.SetCursor(created.ID, scoped, *cursor); err != nil {
			return run, err
		}
		run.Cursor = *cursor
	}
	if outcome != RunOutcomeNone {
		if err := b.SetOutcome(created.ID, outcome); err != nil {
			return run, err
		}
		run.Outcome = outcome
	}
	if err := b.CloseRun(created.ID, closeReason); err != nil {
		return run, err
	}
	return run, nil
}

// MarkFailed stamps the failure outcome on a tracking bead in ONE Update,
// optionally appending the event cursor labels (order:<scoped>, seq:<N>).
// Combining the outcome and cursor labels in a single Update is load-bearing:
// SetOutcome followed by SetCursor would be two writes and is NOT
// byte-equivalent to the dispatcher's original markTrackingFailure.
//
// It returns the RAW Update error unwrapped: the sole caller (the dispatcher's
// markTrackingFailure) logs it under its own "failed to mark tracking bead %s
// as failed: %v" context, so wrapping here would double the context in the
// operator log.
func (b beadsTracking) MarkFailed(runID, scoped string, outcome RunOutcome, cursor *EventCursor) error {
	labels := outcome.Labels()
	if cursor != nil {
		labels = append(labels,
			labelOrderTitlePrefix+scoped,
			fmt.Sprintf("%s%d", labelSeqPrefix, uint64(*cursor)),
		)
	}
	return b.store.Update(runID, beads.UpdateOpts{Labels: labels})
}

// DeleteRun permanently removes one tracking bead. Tracking beads are
// standalone (CreateRun mints them dep-free), so the domain delete is a plain
// store delete; the graph-aware dep-unwind delete the cmd/gc retention prune
// performs stays at that call site as graph residual.
func (b beadsTracking) DeleteRun(runID string) error {
	if b.store.Store == nil {
		return fmt.Errorf("deleting order run %q: nil store", runID)
	}
	if err := b.store.Delete(runID); err != nil {
		return fmt.Errorf("deleting order run %q: %w", runID, err)
	}
	return nil
}

// Get reads the tracking/run bead named by handle on the bare tier (a by-id
// detail read is cache-tolerant). A bead that carries no order-run label /
// order: title still decodes (best-effort scoped name) so a caller that
// already holds a valid handle gets its fields back.
func (b beadsTracking) Get(handle string) (OrderRun, error) {
	if b.store.Store == nil {
		return OrderRun{}, fmt.Errorf("orders get %q: nil store", handle)
	}
	bead, err := b.store.Get(handle)
	if err != nil {
		return OrderRun{}, fmt.Errorf("orders get %q: %w", handle, err)
	}
	name, _ := NameFromTrackingBead(bead)
	return decodeRun(name, bead), nil
}

// RunDetail reads the tracking/run bead named by handle and projects it onto a
// RunDetail (OrderRun + the run's gate output). The gate-output vocabulary
// stays owned by internal/convergence; only the typed GateOutput escapes.
func (b beadsTracking) RunDetail(handle string) (RunDetail, error) {
	if b.store.Store == nil {
		return RunDetail{}, fmt.Errorf("orders run detail %q: nil store", handle)
	}
	bead, err := b.store.Get(handle)
	if err != nil {
		return RunDetail{}, fmt.Errorf("orders run detail %q: %w", handle, err)
	}
	name, _ := NameFromTrackingBead(bead)
	return RunDetail{
		Run:  decodeRun(name, bead),
		Gate: convergence.GateOutputFromMetadata(bead.Metadata),
	}, nil
}

// RecentRuns lists the tracking/order-run beads for scoped newest-first
// (including closed). It reads through the raw store with TierMode TierBoth
// (unioning wisp + issue tiers), byte-identical to the `gc order history` loop.
func (b beadsTracking) RecentRuns(scoped string, limit int) ([]OrderRun, error) {
	if b.store.Store == nil {
		return nil, nil
	}
	beadsList, err := b.store.List(beads.ListQuery{
		Label:         labelOrderRunPrefix + scoped,
		Limit:         limit,
		IncludeClosed: true,
		Sort:          beads.SortCreatedDesc,
		TierMode:      beads.TierBoth,
	})
	if err != nil {
		return decodeRuns(scoped, beadsList), err
	}
	return decodeRuns(scoped, beadsList), nil
}

// RecentRunsAll lists up to limit tracking beads across EVERY order
// (newest-first, including closed). It folds the dispatcher's cooldown history
// index (order_dispatch.go historyEntriesForStore) without per-handle Gets — a
// per-handle read reintroduces the cold-cache serial-query hang (#3201/#2893).
// It reads the LIVE tier (cache-bypass is the duplicate-dispatch guarantee);
// beads with no resolvable order name are skipped, exactly like the index fold.
func (b beadsTracking) RecentRunsAll(limit int) ([]OrderRun, error) {
	if b.store.Store == nil {
		return nil, nil
	}
	list, err := beads.HandlesFor(b.store.Store).Live.List(beads.ListQuery{
		Label:         labelOrderTracking,
		Limit:         limit,
		IncludeClosed: true,
		Sort:          beads.SortCreatedDesc,
		// Aggregate read: the rows fold into entries[order] = max(CreatedAt), so
		// the backing's created-desc tie-break at the limit boundary is
		// irrelevant. Opt into the bounded backing limit to keep the fetch off
		// the full retained corpus (sr-dp9o).
		AllowBackingCreatedLimit: true,
	})
	return decodeTrackingRuns(list), err
}

// OpenRuns lists the OPEN tracking beads across every order (newest-first). It
// folds the dispatcher's single-flight open-tracking index (order_dispatch.go
// entriesForStore) and reads the LIVE tier for the same cache-bypass reason as
// RecentRunsAll. Beads with no resolvable order name are skipped.
func (b beadsTracking) OpenRuns() ([]OrderRun, error) {
	if b.store.Store == nil {
		return nil, nil
	}
	list, err := beads.HandlesFor(b.store.Store).Live.List(beads.ListQuery{
		Label:  labelOrderTracking,
		Status: "open",
		Sort:   beads.SortCreatedDesc,
	})
	return decodeTrackingRuns(list), err
}

// StaleOpenRuns lists OPEN tracking beads whose CreatedAt is at or before
// cutoff (both tiers — legacy issues and wisp — like the sweep's ListByLabel).
// Names are resolved best-effort (Scoped is "" for a tracking bead that
// carries neither an order-run label nor an order: title), matching the
// sweep's "when no order filter is set, close every stale tracking bead"
// behavior.
func (b beadsTracking) StaleOpenRuns(cutoff time.Time) ([]OrderRun, error) {
	if b.store.Store == nil {
		return nil, nil
	}
	all, err := b.store.ListByLabel(labelOrderTracking, 0, beads.WithBothTiers)
	if err != nil {
		return nil, err
	}
	out := make([]OrderRun, 0, len(all))
	for _, bd := range all {
		if bd.CreatedAt.IsZero() || bd.CreatedAt.After(cutoff) {
			continue
		}
		name, _ := NameFromTrackingBead(bd)
		out = append(out, decodeRun(name, bd))
	}
	return out, nil
}

// OrphanedOpenRuns lists every OPEN tracking bead EXCEPT pre-dispatch
// trigger-env-failure markers (which the open-work gate intentionally keeps
// open until the normal stale sweep), across both tiers. Names are best-effort
// (the sweep closes by ID and does not resolve names).
func (b beadsTracking) OrphanedOpenRuns() ([]OrderRun, error) {
	if b.store.Store == nil {
		return nil, nil
	}
	all, err := b.store.ListByLabel(labelOrderTracking, 0, beads.WithBothTiers)
	if err != nil {
		return nil, err
	}
	out := make([]OrderRun, 0, len(all))
	for _, bd := range all {
		if beadLabelsContain(bd.Labels, labelTriggerEnvFail) {
			continue
		}
		name, _ := NameFromTrackingBead(bd)
		out = append(out, decodeRun(name, bd))
	}
	return out, nil
}

// ClosedRunsForRetention lists the CLOSED tracking beads across every order
// (newest-first, both tiers) on the LIVE tier — the read half of the
// closed-tracking retention prune. Names are resolved best-effort so an
// unresolvable-name bead can be routed to the legacy retention bucket.
func (b beadsTracking) ClosedRunsForRetention() ([]OrderRun, error) {
	if b.store.Store == nil {
		return nil, nil
	}
	list, err := beads.HandlesFor(b.store.Store).Live.List(beads.ListQuery{
		Status:   "closed",
		Label:    labelOrderTracking,
		Sort:     beads.SortCreatedDesc,
		TierMode: beads.TierBoth,
	})
	if err != nil {
		return nil, err
	}
	out := make([]OrderRun, 0, len(list))
	for _, bd := range list {
		name, _ := NameFromTrackingBead(bd)
		out = append(out, decodeRun(name, bd))
	}
	return out, nil
}

// ListTracking lists every order tracking bead across both tiers,
// newest-first. The query is byte-identical to the /v0/orders/feed's prior raw
// scan — order-tracking label, created-desc, both tiers, and no IncludeClosed
// so only in-flight/open tracking beads surface. Beads with no order-run label
// (which RunFromTrackingBead rejects) are skipped. Decoded rows and any list
// error are returned together (the RecentRuns pattern) so callers keep the
// feed's err-branch semantics.
func (b beadsTracking) ListTracking() ([]OrderRun, error) {
	if b.store.Store == nil {
		return nil, nil
	}
	list, err := b.store.List(beads.ListQuery{
		Label:    labelOrderTracking,
		Sort:     beads.SortCreatedDesc,
		TierMode: beads.TierBoth,
	})
	runs := make([]OrderRun, 0, len(list))
	for _, bd := range list {
		if run, ok := RunFromTrackingBead(bd); ok {
			runs = append(runs, run)
		}
	}
	return runs, err
}

// LatestOpenRun returns the newest OPEN order-run bead for scoped, if any. The
// query deliberately omits IncludeClosed: the order feed uses the most recent
// OPEN run as the freshness signal for a tracking row's UpdatedAt, so a closed
// run must not advance it. It is byte-identical to the feed's prior raw
// order-run:<scoped> lookup (limit 1, created-desc, both tiers). The decoded
// row, a found flag, and any list error are returned together; found can be
// true alongside a partial-tier error, mirroring the feed's prior handling.
func (b beadsTracking) LatestOpenRun(scoped string) (OrderRun, bool, error) {
	if b.store.Store == nil {
		return OrderRun{}, false, nil
	}
	list, err := b.store.List(beads.ListQuery{
		Label:    labelOrderRunPrefix + scoped,
		Limit:    1,
		Sort:     beads.SortCreatedDesc,
		TierMode: beads.TierBoth,
	})
	if len(list) == 0 {
		return OrderRun{}, false, err
	}
	return decodeRun(scoped, list[0]), true, err
}

// CloseRuns closes a batch of tracking beads, stamping close_reason so
// validation.on-close cities accept the close, then re-verifies that every id
// is closed — retrying a bounded number of times with a short backoff to
// tolerate a store that briefly reports a just-closed bead as still open (Dolt
// write lag). It returns the number of beads actually closed. It is the
// byte-identical replacement for the dispatcher's
// closeAndVerifyOrderTrackingBeads for the close_reason-only close sites
// (dispatch completion, orphaned-startup sweep). ctx cancels the inter-attempt
// backoff.
//
// DRIFT GUARD: this retry loop (attempts/backoff via closeVerifyAttempts +
// closeVerifyRetryDelay, plus uniqueNonEmptyIDs / openIDs / waitCloseRetry) is
// a deliberate twin of cmd/gc/order_dispatch.go closeAndVerifyOrderTrackingBeads,
// which survives for the stale sweep's richer sweep-vocabulary metadata close.
// Any change to the retry policy MUST land in both.
func (b beadsTracking) CloseRuns(ctx context.Context, ids []string, reason string) (int, error) {
	ids = uniqueNonEmptyIDs(ids)
	if len(ids) == 0 {
		return 0, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if b.store.Store == nil {
		return 0, fmt.Errorf("order-tracking close: nil store")
	}
	metadata := map[string]string{"close_reason": reason}

	closed := 0
	var lastErr error
	for attempt := 1; attempt <= closeVerifyAttempts; attempt++ {
		n, err := b.store.CloseAll(ids, metadata)
		closed += n
		if closed > len(ids) {
			closed = len(ids)
		}
		if err != nil {
			lastErr = fmt.Errorf("closing order-tracking beads %s: %w", strings.Join(ids, ", "), err)
			if attempt < closeVerifyAttempts {
				if waitErr := b.waitCloseRetry(ctx); waitErr != nil {
					return closed, errors.Join(lastErr, waitErr)
				}
			}
			continue
		}
		openIDs, err := b.openIDs(ids)
		if err != nil {
			lastErr = fmt.Errorf("verifying order-tracking close for %s: %w", strings.Join(ids, ", "), err)
			if attempt < closeVerifyAttempts {
				if waitErr := b.waitCloseRetry(ctx); waitErr != nil {
					return closed, errors.Join(lastErr, waitErr)
				}
			}
			continue
		}
		if len(openIDs) == 0 {
			return closed, nil
		}
		lastErr = fmt.Errorf("verifying order-tracking close: still open: %s", strings.Join(openIDs, ", "))
		if attempt < closeVerifyAttempts {
			if waitErr := b.waitCloseRetry(ctx); waitErr != nil {
				return closed, errors.Join(lastErr, waitErr)
			}
		}
	}
	return closed, lastErr
}

func (b beadsTracking) waitCloseRetry(ctx context.Context) error {
	timer := time.NewTimer(closeVerifyRetryDelay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// openIDs returns the subset of ids whose tracking bead is still open. Beads
// that no longer exist are treated as closed (dropped).
func (b beadsTracking) openIDs(ids []string) ([]string, error) {
	var openIDs []string
	for _, id := range ids {
		bd, err := b.store.Get(id)
		if errors.Is(err, beads.ErrNotFound) {
			continue
		}
		if err != nil {
			return openIDs, err
		}
		if bd.Status != "closed" {
			openIDs = append(openIDs, id)
		}
	}
	return openIDs, nil
}

func uniqueNonEmptyIDs(ids []string) []string {
	out := make([]string, 0, len(ids))
	seen := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out
}

// LastRunTracking reports the most recent run time recorded in the orders leg
// — the orders-leg half of the mixed LastRun read. It preserves the
// dispatcher's original bare-List tier and its partial-tier-error tolerance
// (surviving rows win; the error is logged, not returned, once any row is in
// hand). A nil store contributes nothing, matching the pre-extraction
// mixed-leg skip.
func (b beadsTracking) LastRunTracking(name string) (time.Time, error) {
	if b.store.Store == nil {
		return time.Time{}, nil
	}
	return lastRunLeg(b.store.Store, name)
}

// CursorTracking reports the max event seq recorded in the orders leg — the
// orders-leg half of the mixed Cursor read. Like LastRunTracking it preserves
// the original bare-List tier and error tolerance (failures are logged and
// contribute zero). A nil store contributes nothing.
func (b beadsTracking) CursorTracking(name string) EventCursor {
	if b.store.Store == nil {
		return 0
	}
	return EventCursor(cursorLeg(b.store.Store, name))
}

// HasOpenTracking reports whether the orders leg holds in-flight work for the
// scoped order — the orders-leg half of the mixed HasOpenWork read. On a
// single-store city the orders leg also holds the wisp/molecule roots, so
// non-tracking order-run beads defer to the injected graph-walk predicate,
// exactly like the pre-extraction per-leg loop body.
func (b beadsTracking) HasOpenTracking(scoped string, wispHasOpenWork func(store beads.Store, root beads.Bead) (bool, error)) (bool, error) {
	if b.store.Store == nil {
		return false, nil
	}
	return hasOpenWorkLeg(b.store.Store, scoped, wispHasOpenWork)
}

// lastRunLeg is the per-leg body of the mixed LastRun read: the newest
// order-run:<name> CreatedAt in one store, on the dispatcher's original
// bare-List tier, tolerating a partial-tier error once any row is in hand.
// Shared by the beads backend (orders leg) and the Store's graph leg so both
// legs stay byte-identical to the pre-extraction loop.
func lastRunLeg(store beads.Store, name string) (time.Time, error) {
	results, err := store.List(beads.ListQuery{
		Label:         labelOrderRunPrefix + name,
		Limit:         1,
		IncludeClosed: true,
		Sort:          beads.SortCreatedDesc,
		TierMode:      beads.TierBoth,
		// Aggregate read: reduces to max(CreatedAt), so the backing tie-break
		// at the boundary is irrelevant. Opt into the bounded backing limit.
		AllowBackingCreatedLimit: true,
	})
	if err != nil {
		if len(results) == 0 {
			return time.Time{}, err
		}
		runtimeHelpersLogf("orders: last-run lookup partially failed for %s: %v", name, err)
	}
	if len(results) == 0 {
		return time.Time{}, nil
	}
	return results[0].CreatedAt, nil
}

// cursorLeg is the per-leg body of the mixed Cursor read: the max seq across
// the newest order-run:<name> beads in one store. Read failures are logged and
// contribute zero, preserving the dispatcher's original tolerance. Shared by
// the beads backend (orders leg) and the Store's graph leg.
func cursorLeg(store beads.Store, name string) uint64 {
	results, err := store.List(beads.ListQuery{
		Label:         labelOrderRunPrefix + name,
		Limit:         10,
		IncludeClosed: true,
		Sort:          beads.SortCreatedDesc,
		TierMode:      beads.TierBoth,
		// Deliberately NOT an AllowBackingCreatedLimit caller. This read reduces
		// to MaxSeqFromLabels — a max over seq, a DIFFERENT column than the
		// created_at sort key. The backing breaks created_at ties by id ASC, so a
		// bounded backing created-desc read keeps the smaller-id members of the
		// newest second and drops the larger ids; because seq is forward-only the
		// max-seq run is exactly that newest largest-id row, so a bounded backing
		// read could omit it and regress the event cursor into replaying consumed
		// events. Fetch the full candidate set so ApplyListQuery cuts the canonical
		// (created_at DESC, id DESC) prefix, which keeps the max-seq run at the
		// front (the Limit above is an exact client-side cap).
	})
	if err != nil {
		if len(results) == 0 {
			runtimeHelpersLogf("orders: cursor lookup failed for %s: %v", name, err)
			return 0
		}
		runtimeHelpersLogf("orders: cursor lookup partially failed for %s: %v", name, err)
	}
	if len(results) == 0 {
		return 0
	}
	labelSets := make([][]string, 0, len(results))
	for _, bd := range results {
		labelSets = append(labelSets, bd.Labels)
	}
	return MaxSeqFromLabels(labelSets)
}

// hasOpenWorkLeg is the per-leg body of the mixed HasOpenWork read: it lists
// the order-run:<scoped> beads in one store on the LIVE tier, classifies
// order-tracking beads inline, and defers each wisp-root subtree verdict to
// wispHasOpenWork — the graph-walk predicate that stays graph-owned in the
// controller. Shared by the beads backend (orders leg) and the Store's graph
// leg.
func hasOpenWorkLeg(store beads.Store, scoped string, wispHasOpenWork func(store beads.Store, root beads.Bead) (bool, error)) (bool, error) {
	results, err := beads.HandlesFor(store).Live.List(beads.ListQuery{
		Label:    labelOrderRunPrefix + scoped,
		Sort:     beads.SortCreatedDesc,
		TierMode: beads.TierBoth,
	})
	if err != nil {
		return false, fmt.Errorf("listing order work beads: %w", err)
	}
	for _, bd := range results {
		if bd.Status == "closed" {
			continue
		}
		if beadLabelsContain(bd.Labels, labelOrderTracking) {
			return true, nil
		}
		if wispHasOpenWork == nil {
			continue
		}
		open, err := wispHasOpenWork(store, bd)
		if err != nil {
			return false, err
		}
		if open {
			return true, nil
		}
	}
	return false, nil
}
