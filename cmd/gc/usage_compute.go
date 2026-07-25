package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/gastownhall/gascity/internal/beadmeta"
	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/session"
	"github.com/gastownhall/gascity/internal/usage"
	"github.com/gastownhall/gascity/internal/worker"
)

// usageComputeEmittedAtKey marks the awake interval (by its awake_started_at
// value) whose compute Fact has already been recorded, so a later tick does
// not re-emit it. A new awake interval has a new awake_started_at, so emission
// across intervals is allowed.
const usageComputeEmittedAtKey = "usage_compute_emitted_at"

// usageModelSweptAtKey marks the awake interval (by its awake_started_at value)
// whose model-usage sweep has settled — recorded independently of the compute
// marker so a transient sweep miss (transcript not yet flushed, extraction tear,
// or sink failure) retries on the next tick instead of being permanently lost
// with the compute fact. Once stamped, the sweep is not re-run for the interval.
// Non-gc-prefixed to match its sibling usageComputeEmittedAtKey (both are
// session-interval accounting markers, not domain metadata).
const usageModelSweptAtKey = "usage_model_swept_at"

// isComputeTerminalState reports whether a session state marks the end of an
// awake interval, at which a compute fact should be emitted. It covers every
// non-running lifecycle endpoint the controller's open-bead scan can observe:
// idle-sleep (asleep), controller drain (drained), retirement (archived),
// operator suspend (suspended), and crash-loop quarantine (quarantined). A
// session closed directly from active without first passing through one of
// these open states is the known v0 scan limitation (see
// engdocs/design/usage-facts-v0.md).
func isComputeTerminalState(state string) bool {
	switch session.State(strings.TrimSpace(state)) {
	case session.StateAsleep, session.StateDrained, session.StateArchived,
		session.StateSuspended, session.StateQuarantined:
		return true
	}
	return false
}

// isLiveModelSweepState reports whether a session state marks a live, still-running
// session that may be actively making model calls — active, or its awake alias —
// as opposed to a compute-terminal endpoint (isComputeTerminalState) or a
// transitional pre-run state (creating / start-pending / draining / failed-create)
// that has no stable transcript to sweep yet. It is the live counterpart to
// isComputeTerminalState; the two are disjoint, so processSessionBead routes a bead
// down at most one of the terminal-or-live arms per tick.
func isLiveModelSweepState(state string) bool {
	switch session.State(strings.TrimSpace(state)) {
	case session.StateActive, session.StateAwake:
		return true
	}
	return false
}

// emitComputeFactForBead records one compute Fact for a session bead's
// completed awake interval, exactly once per awake_started_at epoch. Returns
// true when a fact was recorded. It is a no-op when the sink is discard/nil,
// when there is no awake_started_at (the session never confirmed a start), or
// when the interval was already recorded. Sink and marker write failures are
// reported through logf (when non-nil) rather than dropped silently.
//
// commit governs the interval-accounting side effects, decoupled from the fact
// write so the model-usage sweep can retry across ticks: when commit is true the
// usage_compute_emitted_at marker is stamped (closing the interval to further
// Gets) and the active_work_bead pointer is cleared; when false the fact is still
// recorded but the interval stays open, so a caller that has not yet settled the
// model sweep leaves the session a candidate for the next tick. Re-recording the
// fact on a later tick is collapsed by ComputeIdempotencyKey at read time, and
// active_work_bead is preserved so the retrying sweep still resolves the step.
//
// SessionID is stamped from bead.ID so compute facts carry the same session
// bead join key as model facts.
//
// wall_seconds is measured from awake_started_at to slept_at when present (the
// graceful-sleep end), else to now (best-effort for other terminal transitions).
//
// RunID is resolved from the session bead's own run chain (workflow_id ||
// molecule_id || gc.root_bead_id-or-self || bead id). Per-work-bead attribution
// is deferred until a dispatch/claim writer exists, so pooled sessions roll up
// per-session for now (see engdocs/design/usage-facts-v0.md).
func emitComputeFactForBead(ctx context.Context, sink usage.Sink, store beads.Store, bead beads.Bead, runtimeKind, city string, now time.Time, logf func(string, ...any), commit bool) bool {
	if sink == nil || sink == usage.Discard || store == nil {
		return false
	}
	meta := bead.Metadata
	if meta == nil {
		return false
	}
	startRaw := strings.TrimSpace(meta["awake_started_at"])
	if startRaw == "" {
		return false
	}
	if strings.TrimSpace(meta[usageComputeEmittedAtKey]) == startRaw {
		return false // already emitted this interval
	}
	startedAt, err := time.Parse(time.RFC3339, startRaw)
	if err != nil {
		return false
	}
	// Prefer the recorded sleep time as the interval end, but only when it falls
	// after this interval's start — slept_at can be stale for non-sleep terminal
	// states (drained/archived) that don't refresh it. Otherwise use now.
	end := now
	if sleptRaw := strings.TrimSpace(meta["slept_at"]); sleptRaw != "" {
		if t, perr := time.Parse(time.RFC3339, sleptRaw); perr == nil && t.After(startedAt) {
			end = t
		}
	}
	wall := end.Sub(startedAt).Seconds()
	if wall < 0 {
		wall = 0
	}
	runID := beadmeta.ResolveRunID(bead.Metadata, bead.ID, "")
	fact := usage.Fact{
		RunID: runID,
		// The reconcile snapshot hands us the session bead directly, so bead.ID IS
		// the session bead id — the same value RunID resolution and the idempotency
		// key already consume below. Stamp it so compute facts carry the session
		// join key symmetrically with model facts (a session-keyed cost rollup must
		// union both Kinds; an unset SessionID here would silently drop compute/wall
		// cost from the join).
		SessionID:      strings.TrimSpace(bead.ID),
		Worker:         strings.TrimSpace(meta["session_name"]),
		City:           city,
		Kind:           usage.KindCompute,
		Runtime:        runtimeKind,
		WallSeconds:    wall,
		UpstreamReqID:  bead.ID + ":" + startRaw,
		At:             now.UnixMilli(),
		IdempotencyKey: usage.ComputeIdempotencyKey(runID, bead.ID, startRaw),
	}
	if err := sink.Record(ctx, fact); err != nil {
		// Surface the failure instead of dropping it silently; leave the marker
		// unset so a later tick retries. The durable LocalSink's read-time dedup
		// by IdempotencyKey backstops a partial double-emit.
		if logf != nil {
			logf("usage: recording compute fact for session %s failed; will retry next tick: %v", bead.ID, err)
		}
		return false
	}
	if !commit {
		// The fact is durably recorded, but the interval is intentionally left open
		// (marker unset, active_work_bead preserved) so the model-usage sweep retries
		// on a later tick. The re-recorded fact is collapsed by IdempotencyKey.
		return true
	}
	// Single-key marker → atomic on every store impl.
	if err := store.SetMetadata(bead.ID, usageComputeEmittedAtKey, startRaw); err != nil {
		// The fact is durably recorded; a missed marker only risks a re-emit that
		// IdempotencyKey collapses at read time. Still surface it.
		if logf != nil {
			logf("usage: marking compute fact emitted for session %s failed; may re-emit (deduped by idempotency key): %v", bead.ID, err)
		}
	}
	// Clear the session's active-work-bead pointer at this terminal/sleep transition,
	// so a model invocation made while idle (between this work and the next claim) is
	// attributed at run level (StepID="") rather than to the step that just ended.
	// Best-effort: a stale pointer is overwritten by the next claim regardless.
	if err := store.SetMetadata(bead.ID, beadmeta.ActiveWorkBeadMetadataKey, ""); err != nil {
		if logf != nil {
			logf("usage: clearing active_work_bead for session %s failed (overwritten by next claim): %v", bead.ID, err)
		}
	}
	return true
}

// computeFactGetCandidate reports whether a session is worth a per-session store Get for
// a compute Fact, decided purely from its Info projection — BEFORE any Get. A session
// qualifies only when it is in a compute-terminal state, has an awake interval to account
// (awake_started_at set), and that interval is not already recorded
// (usage_compute_emitted_at != awake_started_at). This is the same short-circuit
// emitComputeFactForBead applies AFTER the Get, hoisted onto Info so a parked (idle/
// asleep) session whose interval is already accounted costs zero Gets — the common steady
// state. It is the pure, testable gate behind emitDueComputeFacts's per-session Get.
func computeFactGetCandidate(info session.Info) bool {
	if !isComputeTerminalState(info.MetadataState) {
		return false
	}
	start := strings.TrimSpace(info.AwakeStartedAt)
	if start == "" {
		return false
	}
	return strings.TrimSpace(info.UsageComputeEmittedAt) != start
}

// liveModelSweepCandidate reports whether a still-running session should have its
// transcript swept for NEW model usage this tick, decided purely from its Info
// projection — BEFORE any Get. A session qualifies when it is in a live, awake state
// (isLiveModelSweepState) AND has a confirmed awake interval (awake_started_at set):
// that timestamp anchors the codex discovery window, and requiring it keeps the
// keyed-codex lookup from widening to a full-history scan for a session that never
// recorded a wake. Unlike computeFactGetCandidate there is no "already-emitted"
// short-circuit — a live session is swept EVERY tick, and the persisted
// invocation-usage cursor is what makes that idempotent (only transcript content
// beyond the cursor is counted). It is the main-tier snapshot arm's pre-Get gate;
// the split-city all-tier arm reaches wisp-tier live sessions through
// processSessionBead directly.
func liveModelSweepCandidate(info session.Info) bool {
	if !isLiveModelSweepState(info.MetadataState) {
		return false
	}
	return strings.TrimSpace(info.AwakeStartedAt) != ""
}

// emitDueComputeFacts emits a compute Fact for any of the given open sessions whose
// awake interval has ended (terminal state) and has not yet been recorded. It reuses the
// reconcile tick's already-loaded Info snapshot for the cheap candidate filter
// (computeFactGetCandidate), then fetches the raw bead ONLY for the few sessions that
// pass it: the usage lane genuinely needs the whole bead (ResolveRunID walks the
// run-chain keys, and slept_at is not projected onto session.Info), so this is the usage
// lane's OWN edge read rather than a snapshot raw-half read. A steady fleet of parked
// sessions whose intervals are already accounted issues zero Gets. Best-effort: it never
// blocks or fails the reconcile tick.
func (cr *CityRuntime) emitDueComputeFacts(ctx context.Context, sessions []session.Info) {
	if cr.cs == nil {
		return
	}
	sink := cr.cs.UsageSink()
	if sink == nil || sink == usage.Discard {
		return
	}
	store := cr.cityBeadStore()
	if store == nil {
		return
	}
	// Session-class beads live in the INFRA store on a split city; reading them
	// through the city work store returns not-found for every session id, which
	// killed usage compute entirely after the infra-store migration (zero facts,
	// factory token views all-zero). Resolve the class store once — every
	// session-bead operation in this tick (Get, sweep-marker write, the sweep
	// factory's cursor persist, and the compute commit) flows through it. On a
	// single-store city cachedCityInfraStore is nil and behavior is unchanged.
	if infra := cachedCityInfraStore(cr.cityPath, cr.cfg); infra != nil {
		store = infra
	}
	runtimeKind := ""
	if cr.cfg != nil {
		runtimeKind = cr.cfg.Session.Provider
	}
	// Throttle sink-failure noise: a persistently broken sink would otherwise log
	// once per terminal bead per tick. One line per tick is enough signal that
	// the sink is failing without flooding the controller log.
	logged := false
	logf := func(format string, args ...any) {
		if logged || cr.stderr == nil {
			return
		}
		logged = true
		fmt.Fprintf(cr.stderr, format+"\n", args...) //nolint:errcheck // best-effort stderr
	}
	// Lazily built worker factory for the end-of-interval model-usage sweep. It is
	// constructed at most once per tick, and only when a terminal session actually
	// needs it, so a steady fleet of parked sessions builds nothing. A build
	// failure (or nil cfg) degrades to compute-only accounting for this tick.
	var (
		sweepFactory      *worker.Factory
		sweepFactoryTried bool
	)
	modelSweepFactory := func() *worker.Factory {
		if sweepFactoryTried {
			return sweepFactory
		}
		sweepFactoryTried = true
		if cr.cfg == nil {
			return nil
		}
		f, ferr := workerFactoryWithConfig(cr.cityPath, store, cr.sp, cr.cfg)
		if ferr != nil {
			logf("usage: building worker factory for model-usage sweep failed: %v", ferr)
			return nil
		}
		sweepFactory = f
		return sweepFactory
	}
	now := time.Now().UTC()
	processed := make(map[string]bool)
	processSessionBead := func(b beads.Bead) {
		processed[b.ID] = true
		if b.Metadata == nil {
			return
		}
		state := b.Metadata["state"]
		// Live, still-running sessions: sweep NEW transcript content incrementally so
		// the "model calls today" / token counters advance in near-real-time (long-lived
		// control dispatchers and wisp operators otherwise mint nothing until they
		// retire). No compute fact and no per-interval marker here — the awake interval
		// is still open; the shared invocation-usage cursor dedupes across ticks AND
		// across the eventual retirement sweep, which advances the same cursor.
		if isLiveModelSweepState(state) {
			cr.sweepLiveSessionModelUsage(ctx, b, now, logf, modelSweepFactory)
			return
		}
		// Re-check the terminal state from the FRESH bead: a session that re-awoke in
		// the window since the snapshot was taken must not mint a tiny-wall fact for its
		// just-STARTED interval and suppress the real end-of-interval emission. Best-
		// effort accounting, the same NDI class as the sync-tail re-list delta.
		if !isComputeTerminalState(state) {
			return
		}
		awakeStart := strings.TrimSpace(b.Metadata["awake_started_at"])
		// Model-usage lane FIRST, symmetric to and beside the compute fact: recover the
		// terminal interval's trailing model-token usage that the prompt-op seam never
		// recorded (pool-routed, hook-self-driven agents self-drive after the claim
		// nudge). It runs before the compute commit so the active_work_bead pointer the
		// sweep reads for StepID is still intact. Best-effort — a sweep error never
		// fails the reconcile tick; overlap with the prompt-op seam is collapsed at read
		// time by the shared usage.ModelIdempotencyKey.
		//
		// The sweep is gated by its OWN per-interval marker (usageModelSweptAtKey),
		// distinct from the compute marker, so a transient miss retries on a later tick
		// instead of being lost. sweepSettled defaults true so a nil factory / no-op
		// sink never blocks the compute commit.
		sweepSettled := true
		if factory := modelSweepFactory(); factory != nil && awakeStart != "" &&
			strings.TrimSpace(b.Metadata[usageModelSweptAtKey]) != awakeStart {
			_, settled, serr := factory.SweepSessionModelUsage(ctx, b.ID, b.Metadata, now)
			if serr != nil {
				logf("usage: model-usage sweep for session %s failed; will retry: %v", b.ID, serr)
			}
			sweepSettled = settled
			if settled {
				if merr := store.SetMetadata(b.ID, usageModelSweptAtKey, awakeStart); merr != nil {
					logf("usage: marking model-usage swept for session %s failed; may re-sweep (deduped by idempotency key): %v", b.ID, merr)
				}
			}
		}
		// Commit the interval (stamp usage_compute_emitted_at, clear active_work_bead)
		// only once the sweep has settled — an unsettled sweep leaves the interval a
		// candidate so both lanes retry next tick. The compute fact itself is always
		// recorded (idempotent), so wall-time accounting is never delayed by a pending
		// sweep.
		emitComputeFactForBead(ctx, sink, store, b, runtimeKind, cr.cityName, now, logf, sweepSettled)
	}
	for _, info := range sessions {
		// Terminal sessions get their end-of-interval compute+model sweep; live
		// (still-awake) sessions get the incremental model sweep. Everything else is
		// skipped before any Get — the two candidate classes are disjoint.
		if !computeFactGetCandidate(info) && !liveModelSweepCandidate(info) {
			continue
		}
		b, err := store.Get(info.ID)
		if err != nil {
			logf("usage: loading session %s for usage facts failed: %v", info.ID, err)
			continue
		}
		processSessionBead(b)
	}
	// Wisp-lifecycle sessions (split cities) close AT retirement, so they never
	// appear in the open-only snapshot above — during activity this zeroed usage
	// facts entirely on maintainer-city while classic cities (whose terminal
	// sessions linger open in "drained") kept accounting. Sweep recently-closed,
	// still-unmarked session beads from the class store through the same path;
	// the usage_compute_emitted_at and model-sweep markers keep this idempotent,
	// and the 2h window comfortably beats both the tick cadence and the 4h
	// terminal-wisp retention purge. Single-store cities skip (nil infra store).
	if cachedCityInfraStore(cr.cityPath, cr.cfg) != nil {
		// TierBoth + all statuses: the reconcile snapshot lists MAIN-tier session
		// beads only (ListAllSessionBeads never sets TierMode), so the wisp-tier
		// half of a split city's session population — precisely the churn-heavy
		// wisp operators that mint usage — never reaches the open-infos loop
		// above. The per-bead gates (terminal state, interval markers) and the
		// processed set keep this pass idempotent with the snapshot arm.
		rows, lerr := store.List(beads.ListQuery{
			Type:          "session",
			IncludeClosed: true,
			TierMode:      beads.TierBoth,
			AllowScan:     true,
		})
		if lerr != nil {
			logf("usage: listing sessions for compute sweep failed: %v", lerr)
			return
		}
		cutoff := now.Add(-2 * time.Hour)
		for _, b := range rows {
			if processed[b.ID] {
				continue
			}
			// Closed rows only within the retention-safe window; open terminal
			// rows (e.g. a drained wisp awaiting retire) have no window.
			if b.Status == "closed" && b.UpdatedAt.Before(cutoff) {
				continue
			}
			processSessionBead(b)
		}
	}
}

// sweepLiveSessionModelUsage sweeps a still-running (awake) session's transcript for
// model usage that appeared since the shared invocation-usage cursor, so the "model
// calls today" / token counters advance in near-real-time for long-lived sessions
// (control dispatchers, wisp operators) instead of only jumping when a session
// retires.
//
// It reuses the same cursor-guarded, 64KB-tail-bounded, idempotent sweep the
// end-of-interval arm runs, but deliberately bills NO compute fact and stamps NO
// per-interval marker (usageModelSweptAtKey): the awake interval is still open, so
// the session must stay a candidate on every tick, and the persisted cursor is what
// prevents double-counting — across ticks AND across the eventual retirement sweep,
// since both arms advance the same cursor and usage.ModelIdempotencyKey collapses any
// residual overlap at read time. Catch-up on a long-unswept transcript is bounded by
// the extractor's fixed tail window, so a multi-day session cannot blow memory or
// stall the tick on its first live sweep.
//
// Transcript discovery is memoized per session bead id (cr.liveSweepTranscriptPaths):
// a live session is swept every tick, and re-running the O(awake-days) codex rollout
// scan each time would dominate the cost, so the resolved path is cached for the
// process lifetime (a rollout path never changes). Best-effort: a nil factory or an
// as-yet-unresolvable transcript is a no-op this tick (retried next), and a sweep
// error is logged and retried (the cursor advances only through entries that reached
// the sink).
func (cr *CityRuntime) sweepLiveSessionModelUsage(ctx context.Context, b beads.Bead, now time.Time, logf func(string, ...any), modelSweepFactory func() *worker.Factory) {
	if b.Metadata == nil || !isLiveModelSweepState(b.Metadata["state"]) {
		return
	}
	factory := modelSweepFactory()
	if factory == nil {
		return
	}
	path, cached := cr.cachedLiveTranscriptPath(b.ID)
	if !cached {
		path = factory.DiscoverSweepTranscript(b.ID, b.Metadata, now)
		if path != "" {
			cr.rememberLiveTranscriptPath(b.ID, path)
		}
	}
	if path == "" {
		// Not resolvable yet (rollout not flushed, or a codex session with no
		// session_key on this branch): nothing to sweep this tick; retry next tick.
		return
	}
	if _, _, err := factory.SweepSessionModelUsageAtPath(ctx, b.ID, b.Metadata, path, now); err != nil {
		logf("usage: live model-usage sweep for session %s failed; will retry: %v", b.ID, err)
	}
}

// cachedLiveTranscriptPath returns the memoized transcript rollout path for a live
// session bead id, reporting whether an entry was present. A present entry short-
// circuits per-tick discovery.
func (cr *CityRuntime) cachedLiveTranscriptPath(id string) (string, bool) {
	if v, ok := cr.liveSweepTranscriptPaths.Load(id); ok {
		p, _ := v.(string)
		return p, true
	}
	return "", false
}

// rememberLiveTranscriptPath records the resolved transcript rollout path for a live
// session bead id so later ticks skip discovery. Only non-empty paths are cached; an
// unresolved ("") result is retried next tick.
func (cr *CityRuntime) rememberLiveTranscriptPath(id, path string) {
	cr.liveSweepTranscriptPaths.Store(id, path)
}
