package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/gastownhall/gascity/internal/clock"
	"github.com/gastownhall/gascity/internal/config"
	"github.com/gastownhall/gascity/internal/events"
	"github.com/gastownhall/gascity/internal/rollout"
	"github.com/gastownhall/gascity/internal/runtime"
	sessionpkg "github.com/gastownhall/gascity/internal/session"
)

// exactSessionConfigDriftHalf names which fingerprint moved on one durable row.
// Legacy spells the two halves as two trace sites reached through one compare
// chain — the live half is the ELSE of the core compare — so the keyed family
// carries the same exclusivity: a core-drifted row is never also a live-drift
// candidate on the same pass.
type exactSessionConfigDriftHalf string

const (
	exactSessionConfigDriftCore exactSessionConfigDriftHalf = "core"
	exactSessionConfigDriftLive exactSessionConfigDriftHalf = "live"
)

// exactSessionConfigDrift is one resolved drift condition: which half moved,
// the two hashes, and the resolved template the convergence effects execute
// against. Resolving the template is the expensive rung, so the seam guard and
// the handler share ONE resolution rather than answering the same question from
// two derivations that could skew.
type exactSessionConfigDrift struct {
	Half        exactSessionConfigDriftHalf
	Site        TraceSiteCode
	StoredHash  string
	CurrentHash string
	// DriftKey is legacy's own deferral key — "<storedCore>:<currentCore>" — so
	// a deferral stamp legacy wrote is read back by exactly the same name.
	DriftKey string
	Template TemplateParams
	AgentCfg runtime.Config
	// LaunchOnly reports that the provision half held while the launch half
	// moved, so the agent can be relaunched into the existing warm box.
	LaunchOnly           bool
	StoredProvisionHash  string
	StoredLaunchHash     string
	DriftedFields        []string
	SessionName          string
	Named                bool
	CurrentLiveFinger    string
	SessionLiveConfigLen int
}

// resolveExactSessionConfigDrift re-derives the whole condition from ONE durable
// row: the template, the executable-config-for-hash form, and both fingerprint
// compares. It is the family's single source of truth — the detector's reason is
// a scheduling hint and the row is the authority (the seam's second rule) — and
// it is pure apart from the template resolution's idempotent hook install, which
// the ordinary start path already pays per admission.
//
// The cheap durable rungs run first so a row that cannot possibly be drifted
// (closed, unnamed, no baseline stamped yet, a create still in flight) never
// pays the resolution at all.
func resolveExactSessionConfigDrift(
	params exactSessionStartParams,
	info sessionpkg.Info,
	clk clock.Clock,
) (exactSessionConfigDrift, bool) {
	if clk == nil {
		clk = clock.Real{}
	}
	name := strings.TrimSpace(info.SessionNameMetadata)
	storedHash := strings.TrimSpace(info.StartedConfigHash)
	// #127: before the startup window stamps a baseline there is nothing to
	// compare against, and calling a starting session drifted would drain it.
	if info.Closed || name == "" || storedHash == "" {
		return exactSessionConfigDrift{}, false
	}
	// A create still holds the row. Legacy's drift arms run on rows that are
	// alive (or asleep and named); a queued or leased create is the pending-create
	// recovery path's, and D-STALE-CREATE's when its lease expires.
	if info.PendingCreateClaim || pendingCreateQueuedOrCreatingState(info.MetadataState) {
		return exactSessionConfigDrift{}, false
	}
	template := normalizedSessionTemplateInfo(info, params.Config)
	if template == "" {
		template = strings.TrimSpace(info.Template)
	}
	cfgAgent := findAgentByTemplate(params.Config, template)
	if template == "" || cfgAgent == nil {
		return exactSessionConfigDrift{}, false
	}
	stderr := params.Stderr
	if stderr == nil {
		stderr = io.Discard
	}
	tp, err := resolveExactSessionStartTemplate(params, info, cfgAgent, clk, stderr)
	if err != nil {
		// An unresolvable template is not a drift verdict. Fail closed: the row
		// keeps whatever legacy makes of it, and the condition is re-detected.
		return exactSessionConfigDrift{}, false
	}
	agentCfg := sessionCoreConfigForHashInfo(tp, info)
	currentHash := runtime.CoreFingerprint(agentCfg)
	drift := exactSessionConfigDrift{
		Template:             tp,
		AgentCfg:             agentCfg,
		SessionName:          name,
		Named:                isNamedSessionInfo(info),
		DriftKey:             storedHash + ":" + currentHash,
		CurrentLiveFinger:    runtime.LiveFingerprint(agentCfg),
		SessionLiveConfigLen: len(agentCfg.SessionLive),
	}
	if storedHash != currentHash {
		drift.Half = exactSessionConfigDriftCore
		drift.Site = TraceSiteReconcilerConfigDrift
		drift.StoredHash = storedHash
		drift.CurrentHash = currentHash
		drift.StoredProvisionHash = info.StartedProvisionHash
		drift.StoredLaunchHash = info.StartedLaunchHash
		// Empty sub-hashes (a session started before the partitioned
		// fingerprints existed) are NOT launch-only: the full restart re-stamps
		// them and self-heals.
		drift.LaunchOnly = drift.StoredProvisionHash != "" && drift.StoredLaunchHash != "" &&
			drift.StoredProvisionHash == runtime.ProvisionFingerprint(agentCfg) &&
			drift.StoredLaunchHash != runtime.LaunchFingerprint(agentCfg)
		drift.DriftedFields = runtime.CoreFingerprintDriftFieldsFromJSON(info.CoreHashBreakdown, agentCfg)
		return drift, true
	}
	storedLive := info.StartedLiveHash
	if storedLive == drift.CurrentLiveFinger {
		return exactSessionConfigDrift{}, false
	}
	drift.Half = exactSessionConfigDriftLive
	drift.Site = TraceSiteReconcilerLiveDrift
	drift.StoredHash = storedLive
	drift.CurrentHash = drift.CurrentLiveFinger
	return drift, true
}

// exactSessionConfigDriftConvergeCandidate is the D-DRIFT seam guard. It reads
// nothing but the durable row and the config — never admission.Source — because
// drift is level-triggered and the controller coalesces admissions on a key: a
// config edit drifts the fleet onto keys that already carry ordinary start
// admissions, and every one of those must reach this family.
func exactSessionConfigDriftConvergeCandidate(
	params exactSessionStartParams,
	info sessionpkg.Info,
	response sessionpkg.PersistedResponse,
	clk clock.Clock,
) bool {
	if response.Revision == 0 {
		return false
	}
	_, ok := resolveExactSessionConfigDrift(params, info, clk)
	return ok
}

// exactSessionConfigDriftDeferral is one WD.9 rung: a reason a human is engaged
// with this session, so the convergence must wait. WD.8 detects them and applies
// nothing; WD.9 lands the deferral write, the queued drift-drain cancel, and the
// legacy yield for those arms.
type exactSessionConfigDriftDeferral struct {
	Reason  string
	Outcome TraceOutcomeCode
}

// exactSessionConfigDriftDeferralReason answers, read-only, whether the row sits
// on one of the ladder's deferral rungs. Legacy's own answer STAMPS as it reads
// (shouldDeferNamedSessionConfigDrift persists config_drift_deferred_at to start
// the bounded window); this one never writes, because the deferral record is the
// effect WD.9 owns and WD.8 may not apply it early.
//
// The consequence is deliberate and bounded: legacy's deferral arms sit ABOVE
// the convergence yield and keep running, so legacy stamps the window on the
// same tick and this predicate reads it back through the same key. A rung whose
// bounded window has elapsed is no longer a deferral, which is exactly how
// legacy retires activity_unknown and recent_activity.
//
// It returns an error only when the observation itself failed. Legacy `continue`s
// on that error, so the handler refuses with zero effect for the same reason: an
// unreadable attachment probe is not "nobody is attached".
func exactSessionConfigDriftDeferralReason(
	params exactSessionStartParams,
	info sessionpkg.Info,
	drift exactSessionConfigDrift,
	clk clock.Clock,
) (exactSessionConfigDriftDeferral, error) {
	name := drift.SessionName
	attached, attachErr := sessionAttachedForConfigDrift(info.ID, params.Provider, params.CityPath, params.Store, params.Config, name)
	if attachErr != nil {
		return exactSessionConfigDriftDeferral{}, fmt.Errorf("observing config-drift attachment for %q: %w", name, attachErr)
	}
	if attached {
		return exactSessionConfigDriftDeferral{Reason: "attached", Outcome: TraceOutcomeDeferredAttached}, nil
	}
	// A single transient IsAttached false negative would destroy an attached
	// conversation irreversibly, so a recent deferral for the SAME drift key
	// still counts as attached.
	if recentlyDeferredSessionAttachedConfigDrift(info, clk, drift.DriftKey) {
		return exactSessionConfigDriftDeferral{Reason: "attached_recently", Outcome: TraceOutcomeDeferredAttached}, nil
	}
	if drift.Named {
		// An operator-pinned named session is a declared critical conversation:
		// config drift must never collaterally recycle it.
		if pinnedConfiguredNamedSessionKillProtected(info) {
			return exactSessionConfigDriftDeferral{Reason: "pinned", Outcome: TraceOutcomeDeferredActive}, nil
		}
		reason, active := namedSessionActiveUseReasonInfo(info, params.Provider, name, clk)
		if active && namedSessionConfigDriftDeferralStillBinding(info, clk, drift.DriftKey, reason) {
			return exactSessionConfigDriftDeferral{Reason: reason, Outcome: TraceOutcomeDeferredActive}, nil
		}
		return exactSessionConfigDriftDeferral{}, nil
	}
	if pendingInteractionKeepsAwakeInfo(info, params.Provider, name, clk) {
		return exactSessionConfigDriftDeferral{Reason: "pending_interaction", Outcome: TraceOutcomeDeferredPending}, nil
	}
	// A pool-routed session mid-task must not be drained: the assigned bead
	// would be orphaned (assignee pointing at a dead session, status stuck at
	// in_progress). The next pass sees no assigned work and converges naturally.
	hasAssignedWork, assignedErr := sessionHasOpenAssignedWorkForReachableStore(params.CityPath, params.Config, params.Store, params.RigStores, info)
	if assignedErr != nil {
		return exactSessionConfigDriftDeferral{}, fmt.Errorf("checking assigned work before config-drift convergence of %q: %w", name, assignedErr)
	}
	if hasAssignedWork {
		return exactSessionConfigDriftDeferral{Reason: "live_assigned_work", Outcome: TraceOutcomeDeferredActive}, nil
	}
	return exactSessionConfigDriftDeferral{}, nil
}

// namedSessionConfigDriftDeferralStillBinding is the read-only half of legacy's
// boundedNamedSessionConfigDriftDeferral. The two bounded rungs — the provider
// cannot report activity, or it reported activity recently — are deferrals only
// until their window elapses; every other active reason binds unconditionally.
func namedSessionConfigDriftDeferralStillBinding(info sessionpkg.Info, clk clock.Clock, driftKey, reason string) bool {
	var limit time.Duration
	switch reason {
	case "activity_unknown":
		limit = namedSessionActivityThreshold
	case "recent_activity":
		limit = namedSessionRecentActivityConfigDriftDeferralLimit
	default:
		return true
	}
	if clk == nil || info.ConfigDriftDeferredKey != driftKey {
		return true
	}
	deferredAt, ok := parseRFC3339Metadata(info.ConfigDriftDeferredAt)
	if !ok {
		return true
	}
	return clk.Now().UTC().Sub(deferredAt) < limit
}

// reconcileExactSessionConfigDriftConverge converges ONE drifted session by
// exact key. The ladder is legacy's, rung for rung and in legacy's order:
//
//	version artifact  → silent rebaseline, no restart
//	human engaged     → WD.9's deferral (shadow-recorded here, never applied)
//	launch-only drift → relaunch the agent in the existing warm box
//	named + detached  → restart in place
//	ordinary          → begin the config-drift drain
//	live half only    → backfill, rebaseline, or re-apply session_live
//
// Every effect is an EXISTING helper called once behind a revision fence; the
// handler adds no second convergence implementation. The row is re-read and the
// compare re-run before any effect, because the detector's reason is a hint.
//
// The ASLEEP-named repair arm is not this family's yet: it is legacy's own
// separate site, guarded by tick-local restart state the keyed handler cannot
// see, so a row whose runtime is not alive is refused here with zero effect.
func reconcileExactSessionConfigDriftConverge(
	ctx context.Context,
	admission sessionStartAdmission,
	params exactSessionStartParams,
	info sessionpkg.Info,
	response sessionpkg.PersistedResponse,
	clk clock.Clock,
) (exactSessionStartOwner, error) {
	if clk == nil {
		clk = clock.Real{}
	}
	stderr := params.Stderr
	if stderr == nil {
		stderr = io.Discard
	}
	stdout := params.Stdout
	if stdout == nil {
		stdout = io.Discard
	}
	yieldOrPark := func(cause error) (exactSessionStartOwner, error) {
		if params.RolloutMode == rollout.Auto {
			return exactSessionStartLegacyOwner, fmt.Errorf("%w: %w", errSessionStartLegacyFallbackRequired, cause)
		}
		return exactSessionStartKeyedOwner, cause
	}
	if !detectorActDriftConverge {
		return exactSessionStartKeyedOwner, nil
	}

	// The fence: re-read the authoritative row and refuse unless it is still the
	// exact incarnation the condition was detected on.
	latest, latestResponse, readErr := getAuthoritativeSessionStartPersistedRecord(params.Store, info.ID)
	if readErr != nil || latestResponse.Revision == 0 || latestResponse.Revision != response.Revision ||
		latest.Closed || strings.TrimSpace(latest.InstanceToken) != strings.TrimSpace(info.InstanceToken) ||
		strings.TrimSpace(latest.SessionNameMetadata) != strings.TrimSpace(info.SessionNameMetadata) {
		return exactSessionStartKeyedOwner, nil
	}
	drift, drifted := resolveExactSessionConfigDrift(params, latest, clk)
	if !drifted {
		// Converged between detection and dispatch. Zero effect, and no trace
		// noise: the condition simply no longer holds.
		return exactSessionStartKeyedOwner, nil
	}
	name := drift.SessionName

	// Proven presence, not assumed presence. Every convergence effect below acts
	// on a RUNNING agent — relaunch it, kill and reset it, drain it, re-apply its
	// live config — so the observation is paid per key and fails CLOSED: an
	// unreadable provider is not "alive", and the condition is re-detected.
	running, livenessErr := workerSessionTargetRunningWithConfig(params.CityPath, params.Store, params.Provider, params.Config, latest.ID)
	if livenessErr != nil {
		recordExactSessionConfigDriftTrace(params, admission, latest, drift, TraceOutcomeSkippedLivenessError, false, map[string]any{
			"liveness_error": livenessErr.Error(),
		})
		return exactSessionStartKeyedOwner, nil
	}
	if !running {
		// Legacy's alive lane is the only drift lane this family owns. The
		// asleep-named repair stays legacy's for the WD wave.
		recordExactSessionConfigDriftTrace(params, admission, latest, drift, TraceOutcomeNoChange, false, map[string]any{
			"refusal": "runtime_not_alive",
		})
		return exactSessionStartKeyedOwner, nil
	}

	// A version artifact is not real drift: rebaseline all four fingerprints
	// rather than disturbing the agent. It sits above the deferral rungs because
	// it is a silent metadata write, exactly as in legacy.
	if runtime.IsLegacyOrMismatchedVersion(drift.StoredHash) {
		if ctx != nil && ctx.Err() != nil {
			return exactSessionStartKeyedOwner, nil
		}
		_, rebaseErr := silentRebaselineSessionHashes(latest.ID, sessionFrontDoor(params.Store), drift.AgentCfg)
		if rebaseErr != nil {
			return exactSessionStartKeyedOwner, fmt.Errorf("rebaselining legacy hash for %q: %w", name, rebaseErr)
		}
		fmt.Fprintf(stderr, "rebaselined legacy hash for %s (stored=%s current=%s)\n", name, truncateHashForLog(drift.StoredHash), truncateHashForLog(drift.CurrentHash)) //nolint:errcheck
		recordExactSessionConfigDriftTrace(params, admission, latest, drift, rebaselineLegacyHashOutcome(drift.StoredHash), true, nil)
		return exactSessionStartKeyedOwner, nil
	}

	if drift.Half == exactSessionConfigDriftLive {
		return reconcileExactSessionLiveDriftReapply(ctx, admission, params, latest, drift, stdout)
	}

	deferral, deferErr := exactSessionConfigDriftDeferralReason(params, latest, drift, clk)
	if deferErr != nil {
		fmt.Fprintf(stderr, "session reconciler: %v\n", deferErr) //nolint:errcheck
		return exactSessionStartKeyedOwner, nil
	}
	if deferral.Reason != "" {
		// WD.9's arm. Recorded as a detector-shadow PREDICTION at the legacy
		// site — the reason is detector_-prefixed so this record can never
		// auto-arm — and applied by nobody here: legacy's own deferral arms sit
		// above the convergence yield and still stamp the window and cancel the
		// queued drift drain on this tick.
		recordExactSessionConfigDriftShadowDeferral(params, admission, latest, drift, deferral)
		return exactSessionStartKeyedOwner, nil
	}

	if ctx != nil && ctx.Err() != nil {
		return exactSessionStartKeyedOwner, nil
	}
	if drift.LaunchOnly {
		// The box held and only the agent moved: relaunch into the warm box
		// rather than re-provisioning. The helper owns its own anti-skew gate,
		// its speculative-resume-key guard, and the sub-hash rebaseline; the
		// keyed record is written here so the trace carries effect_owner.
		//
		// The returned batch is the fleet tick's snapshot fold, which a keyed
		// handler has no use for: every write in it is already persisted, and the
		// next admission re-reads the authoritative row. Passing a nil trace
		// cycle is deliberate for the same reason — the helper would otherwise
		// write legacy's own payload at the same site, without effect_owner.
		relaunched, _ := relaunchAgentForLaunchDrift(ctx, params.Provider, sessionFrontDoor(params.Store), latest, name,
			drift.Template, params.CityPath, params.Config, params.Store, drift.StoredHash, drift.CurrentHash,
			drift.StoredProvisionHash, drift.StoredLaunchHash, drift.DriftedFields,
			exactSessionConfigDriftRecorder(params), nil, stdout, stderr)
		if relaunched {
			recordExactSessionConfigDriftTrace(params, admission, latest, drift, TraceOutcomeRelaunch, true, nil)
			return exactSessionStartKeyedOwner, nil
		}
		// The provider could not relaunch, or the prepared config skewed. Fall
		// through to the full restart, which is what legacy does — and what
		// re-stamps the sub-hashes so the next pass self-heals.
	}

	if drift.Named {
		if params.Store == nil {
			return yieldOrPark(errors.New("exact config-drift restart-in-place has no store to reset through"))
		}
		// The reset stages start-pending WITH the pending-create claim, which is
		// what makes an off-tick keyed restart safe: legacy protects its own
		// in-tick reset with the tick-local driftRestartedInPlace flag, but the
		// staged row also satisfies pendingResumePreservingNamedRestartInfo, so
		// the next fleet pass's asleep-named repair leaves the preserved
		// session_key and baseline alone and simply starts the session.
		batch := resetConfiguredNamedSessionForConfigDriftInfo(latest, params.Store, params.Provider, name,
			true, string(sessionpkg.StateStartPending), clk.Now().UTC(), stderr)
		if batch == nil {
			return exactSessionStartKeyedOwner, fmt.Errorf("restarting exact config-drift named session %q in place: reset was not recorded", latest.ID)
		}
		exactSessionConfigDriftRecorder(params).Record(events.Event{
			Type:      events.SessionDraining,
			Actor:     "gc",
			Subject:   drift.Template.DisplayName(),
			Message:   "config drift detected",
			SessionID: latest.ID,
		})
		recordExactSessionConfigDriftTrace(params, admission, latest, drift, TraceOutcomeRestartInPlace, true, nil)
		return exactSessionStartKeyedOwner, nil
	}

	if params.DrainTracker == nil {
		return yieldOrPark(errors.New("exact config-drift drain has no tracker to record drain intent in"))
	}
	// The drain library, not a second drain engine, with its enqueue-only begin
	// semantics preserved: the interrupt is deferred to the next advance, which
	// is what gives a session one full pass to be rescued.
	if !beginSessionDrainInfo(latest, params.Provider, params.DrainTracker, "config-drift", clk, exactSessionConfigDriftDrainTimeout(params.Config)) {
		// A drain is already in flight for this key; advancing it is D-DRAIN's.
		recordExactSessionConfigDriftTrace(params, admission, latest, drift, TraceOutcomeNoChange, false, nil)
		return exactSessionStartKeyedOwner, nil
	}
	state := params.DrainTracker.get(latest.ID)
	if state == nil || state.reason != "config-drift" {
		return exactSessionStartKeyedOwner, fmt.Errorf(
			"beginning exact config-drift drain of %q: drain intent is absent after begin", latest.ID)
	}
	fmt.Fprintf(stdout, "Draining session '%s': config-drift\n", name) //nolint:errcheck
	exactSessionConfigDriftRecorder(params).Record(events.Event{
		Type:      events.SessionDraining,
		Actor:     "gc",
		Subject:   drift.Template.DisplayName(),
		Message:   "config drift detected",
		SessionID: latest.ID,
	})
	recordExactSessionConfigDriftTrace(params, admission, latest, drift, TraceOutcomeDrain, true, map[string]any{
		"drain_timeout_seconds": int64(state.deadline.Sub(state.startedAt).Seconds()),
	})
	return exactSessionStartKeyedOwner, nil
}

// reconcileExactSessionLiveDriftReapply converges the live half. It carries no
// deferral rungs on purpose: legacy's live-drift clause has none either, because
// re-applying session_live neither stops nor interrupts the agent.
func reconcileExactSessionLiveDriftReapply(
	ctx context.Context,
	admission sessionStartAdmission,
	params exactSessionStartParams,
	info sessionpkg.Info,
	drift exactSessionConfigDrift,
	stdout io.Writer,
) (exactSessionStartOwner, error) {
	if ctx != nil && ctx.Err() != nil {
		return exactSessionStartKeyedOwner, nil
	}
	livePatch := sessionpkg.MetadataPatch{
		"live_hash":         drift.CurrentLiveFinger,
		"started_live_hash": drift.CurrentLiveFinger,
	}
	// No stored hash and no live config: there is nothing to run, so stamp the
	// baseline and stop calling it drift.
	if drift.StoredHash == "" && drift.SessionLiveConfigLen == 0 {
		if err := sessionFrontDoor(params.Store).ApplyPatch(info.ID, livePatch); err != nil {
			return exactSessionStartKeyedOwner, fmt.Errorf("backfilling live hash for %q: %w", drift.SessionName, err)
		}
		recordExactSessionConfigDriftTrace(params, admission, info, drift, TraceOutcomeNoChange, true, map[string]any{
			"live_effect": "backfill",
		})
		return exactSessionStartKeyedOwner, nil
	}
	if params.Provider == nil {
		return exactSessionStartKeyedOwner, fmt.Errorf("re-applying session_live for %q: no provider", drift.SessionName)
	}
	fmt.Fprintf(stdout, "Live config changed for '%s', re-applying...\n", drift.Template.DisplayName()) //nolint:errcheck
	if err := params.Provider.RunLive(drift.SessionName, drift.AgentCfg); err != nil {
		return exactSessionStartKeyedOwner, fmt.Errorf("re-applying session_live for %q: %w", drift.SessionName, err)
	}
	if err := sessionFrontDoor(params.Store).ApplyPatch(info.ID, livePatch); err != nil {
		return exactSessionStartKeyedOwner, fmt.Errorf("rebaselining live hash for %q: %w", drift.SessionName, err)
	}
	exactSessionConfigDriftRecorder(params).Record(events.Event{
		Type:    events.SessionUpdated,
		Actor:   "gc",
		Subject: drift.Template.DisplayName(),
		Message: "session_live re-applied",
	})
	recordExactSessionConfigDriftTrace(params, admission, info, drift, TraceOutcomeSuccess, true, map[string]any{
		"live_effect": "run_live",
	})
	return exactSessionStartKeyedOwner, nil
}

// exactSessionConfigDriftDrainTimeout reads the configured drift-drain window,
// falling back to the shared default exactly as the fleet arm does.
func exactSessionConfigDriftDrainTimeout(cfg *config.City) time.Duration {
	if cfg != nil {
		if ddt := cfg.Daemon.DriftDrainTimeoutDuration(); ddt > 0 {
			return ddt
		}
	}
	return defaultDrainTimeout
}

func exactSessionConfigDriftRecorder(params exactSessionStartParams) events.Recorder {
	if params.Recorder == nil {
		return events.Discard
	}
	return params.Recorder
}

// recordExactSessionConfigDriftTrace fires the SAME legacy trace sites the fleet
// drift arms fire — ConfigDrift and LiveDrift — with effect_owner=keyed and the
// honest effect_applied, so the WD.15 parity join can put the keyed convergence
// beside legacy's on one cycle.
func recordExactSessionConfigDriftTrace(
	params exactSessionStartParams,
	admission sessionStartAdmission,
	info sessionpkg.Info,
	drift exactSessionConfigDrift,
	outcome TraceOutcomeCode,
	applied bool,
	extra map[string]any,
) {
	reason := TraceReasonConfigDrift
	if drift.Half == exactSessionConfigDriftLive {
		reason = TraceReasonLiveDrift
	}
	recordExactSessionConfigDriftRecord(params, admission, info, drift, reason, outcome, detectorKeyedEffectOwner, applied, extra)
}

// recordExactSessionConfigDriftShadowDeferral records WD.9's arm as a PREDICTION
// at the legacy site: effect_owner=detector-shadow, effect_applied=false, and a
// detector_-prefixed reason so the record can never reach shouldAutoArmForTrace's
// reason leg. WD.9 replaces it with the keyed record when it lands the deferral
// write and its own legacy yield.
func recordExactSessionConfigDriftShadowDeferral(
	params exactSessionStartParams,
	admission sessionStartAdmission,
	info sessionpkg.Info,
	drift exactSessionConfigDrift,
	deferral exactSessionConfigDriftDeferral,
) {
	recordExactSessionConfigDriftRecord(params, admission, info, drift,
		detectorReasonConfigDriftDeferred, deferral.Outcome, detectorShadowEffectOwner, false, map[string]any{
			"active_reason": deferral.Reason,
			"owned_by":      "wd9",
		})
}

func recordExactSessionConfigDriftRecord(
	params exactSessionStartParams,
	admission sessionStartAdmission,
	info sessionpkg.Info,
	drift exactSessionConfigDrift,
	reason TraceReasonCode,
	outcome TraceOutcomeCode,
	effectOwner string,
	applied bool,
	extra map[string]any,
) {
	if params.Trace == nil || drift.Site == "" {
		return
	}
	cycle := params.Trace.BeginCycle(TraceTickTriggerControl, "exact_session_config_drift_converge", time.Now().UTC(), params.Config)
	if cycle == nil {
		return
	}
	template := drift.Template.TemplateName
	if template == "" {
		template = normalizedSessionTemplateInfo(info, params.Config)
	}
	if cycle.detailEnabled(template) {
		fields := map[string]any{
			"admission":         string(admission.Source),
			"admission_version": admission.Version,
			"generation":        params.Generation,
			"instance_token":    info.InstanceToken,
			"drift_half":        string(drift.Half),
			"stored_hash":       drift.StoredHash,
			"current_hash":      drift.CurrentHash,
			"launch_only":       drift.LaunchOnly,
			"effect_owner":      effectOwner,
			"effect_applied":    applied,
		}
		for k, v := range extra {
			fields[k] = v
		}
		cycle.recordAdmittedDetailOperation(
			drift.Site,
			reason,
			outcome,
			"exact_session_config_drift_converge",
			template,
			info.ID,
			info.SessionNameMetadata,
			TraceSource(cycle.sourceFor(template)),
			0,
			fields,
		)
	}
	if err := cycle.End(TraceCompletionCompleted, nil); err != nil && params.Stderr != nil {
		fmt.Fprintf(params.Stderr, "session reconciler: recording exact config-drift trace: %v\n", err) //nolint:errcheck // tracing is observational
	}
}
