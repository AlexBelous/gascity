package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/clock"
	"github.com/gastownhall/gascity/internal/config"
	"github.com/gastownhall/gascity/internal/events"
	"github.com/gastownhall/gascity/internal/runtime"
	sessionpkg "github.com/gastownhall/gascity/internal/session"
	"github.com/gastownhall/gascity/internal/worker"
)

type exactLoadedSessionObserver func(
	context.Context,
	string,
	beads.Store,
	runtime.Provider,
	*config.City,
	sessionpkg.Info,
	[]string,
) (worker.LiveObservation, error)

// exactSessionStartParams is one coherent runtime generation for exact-key
// start reconciliation. Callers must capture Generation, Config, Provider,
// and Store together before invoking reconcileExactSessionStart.
type exactSessionStartParams struct {
	Generation           uint64
	CityPath             string
	CityName             string
	Config               *config.City
	Provider             runtime.Provider
	Store                beads.Store
	StatusWriter         beads.ConditionalWriter
	StatusWriterError    error
	Clock                clock.Clock
	Recorder             events.Recorder
	Stdout               io.Writer
	Stderr               io.Writer
	ObserveLoadedSession exactLoadedSessionObserver
	StartOptions         []startExecutionOption
}

type exactSessionStartOwner uint8

const (
	exactSessionStartUnowned exactSessionStartOwner = iota
	exactSessionStartKeyedOwner
	exactSessionStartLegacyOwner
)

type exactSessionStartPreWakeSkip struct {
	owner exactSessionStartOwner
}

func (e *exactSessionStartPreWakeSkip) Error() string {
	return "exact session start became ineligible before pre-wake commit"
}

// authoritativeSessionStartReadStore forces one exact-key read through the
// store's live handle. It is deliberately confined to
// getAuthoritativeSessionStartRecord: writes and ordinary reads must retain the
// original store so optional write capabilities and cache refreshes survive.
type authoritativeSessionStartReadStore struct {
	beads.Store
	live beads.LiveReader
}

func (s authoritativeSessionStartReadStore) Get(id string) (beads.Bead, error) {
	return s.live.Get(id)
}

// getAuthoritativeSessionStartRecord reads one persisted session through the
// typed session front door while bypassing an eventual-consistency cache. An
// external CLI can commit a wake and send its socket hint before the matching
// event refreshes the controller cache. Its revision is from that same read;
// callers must not refresh it before a future fenced write.
func getAuthoritativeSessionStartRecord(store beads.Store, id string) (sessionpkg.Info, int64, error) {
	if store == nil {
		return sessionpkg.Info{}, 0, fmt.Errorf("session store is nil")
	}
	readStore := authoritativeSessionStartReadStore{
		Store: store,
		live:  beads.HandlesFor(store).Live,
	}
	info, response, err := sessionFrontDoor(readStore).GetPersistedResponse(id)
	if err != nil {
		return sessionpkg.Info{}, 0, err
	}
	return info, response.Revision, nil
}

func getAuthoritativeExactSessionStartInfoBeforeWake(
	store beads.Store,
	id string,
	cfg *config.City,
	now time.Time,
) (sessionpkg.Info, error) {
	info, _, err := getAuthoritativeSessionStartRecord(store, id)
	if err != nil {
		return sessionpkg.Info{}, err
	}
	lifecycle, _, owner := classifyExactSessionStartOwnership(info, cfg, now)
	if owner != exactSessionStartKeyedOwner {
		return sessionpkg.Info{}, &exactSessionStartPreWakeSkip{owner: owner}
	}
	if lifecycle.HasBlocker(sessionpkg.BlockerHeld) || lifecycle.HasBlocker(sessionpkg.BlockerQuarantined) {
		return sessionpkg.Info{}, &exactSessionStartPreWakeSkip{owner: owner}
	}
	return info, nil
}

// reconcileExactSessionStart rereads one durable session key and executes only
// the pending-create and explicit-wake start family. The admission source is a
// scheduling hint; persisted lifecycle state remains authoritative.
func reconcileExactSessionStart(ctx context.Context, admission sessionStartAdmission, params exactSessionStartParams) error {
	_, err := reconcileExactSessionStartWithOwner(ctx, admission, params)
	return err
}

// reconcileExactSessionStartWithOwner returns the durable row's owner as seen
// by the same authoritative read used for reconciliation. CityRuntime uses a
// legacy result to request an immediate fleet tick, closing the race where a
// key changes ownership after socket admission.
func reconcileExactSessionStartWithOwner(
	ctx context.Context,
	admission sessionStartAdmission,
	params exactSessionStartParams,
) (exactSessionStartOwner, error) {
	if ctx == nil {
		return exactSessionStartUnowned, fmt.Errorf("reconciling exact session start %q: context is nil", admission.SessionID)
	}
	if err := ctx.Err(); err != nil {
		return exactSessionStartUnowned, err
	}
	if params.Config == nil {
		return exactSessionStartUnowned, fmt.Errorf("reconciling exact session start %q: config is nil", admission.SessionID)
	}
	if params.Provider == nil {
		return exactSessionStartUnowned, fmt.Errorf("reconciling exact session start %q: runtime provider is nil", admission.SessionID)
	}
	if params.Store == nil {
		return exactSessionStartUnowned, fmt.Errorf("reconciling exact session start %q: session store is nil", admission.SessionID)
	}
	clk := params.Clock
	if clk == nil {
		clk = clock.Real{}
	}
	stdout := params.Stdout
	if stdout == nil {
		stdout = io.Discard
	}
	stderr := params.Stderr
	if stderr == nil {
		stderr = io.Discard
	}
	recorder := params.Recorder
	if recorder == nil {
		recorder = events.Discard
	}
	startOpts := startExecutionOptions{}
	for _, apply := range params.StartOptions {
		if apply != nil {
			apply(&startOpts)
		}
	}
	observeLoadedSession := params.ObserveLoadedSession
	if observeLoadedSession == nil {
		observeLoadedSession = workerObserveLoadedSessionWithRuntimeHintsWithConfig
	}
	var statusResult *exactSessionLifecycleStatusResult
	retainStatus := func(input exactSessionLifecycleStatusInput) {
		if startOpts.exactStatusObserver == nil && params.StatusWriter == nil && params.StatusWriterError == nil {
			return
		}
		result := evaluateExactSessionLifecycleStatus(input)
		statusResult = &result
	}
	defer func() {
		if statusResult != nil {
			reportExactSessionLifecycleStatus(stderr, startOpts.exactStatusObserver, *statusResult)
		}
	}()

	info, loadedRevision, err := getAuthoritativeSessionStartRecord(params.Store, admission.SessionID)
	if err != nil {
		if errors.Is(err, beads.ErrIDCollision) {
			retainStatus(exactSessionLifecycleStatusInput{
				Admission:            admission,
				ControllerGeneration: params.Generation,
				RequestedID:          admission.SessionID,
				Info:                 sessionpkg.Info{ID: admission.SessionID},
				UnavailableReason:    exactSessionLifecycleStatusReasonInvalidInput,
				Error:                err.Error(),
			})
			return exactSessionStartUnowned, fmt.Errorf("reconciling exact session start %q: authoritative ID collision: %w", admission.SessionID, err)
		}
		if errors.Is(err, beads.ErrNotFound) {
			return exactSessionStartUnowned, nil
		}
		if errors.Is(err, sessionpkg.ErrSessionNotFound) {
			retainStatus(exactSessionLifecycleStatusInput{
				Admission:            admission,
				ControllerGeneration: params.Generation,
				RequestedID:          admission.SessionID,
				Info:                 sessionpkg.Info{ID: admission.SessionID},
				UnavailableReason:    exactSessionLifecycleStatusReasonInvalidInput,
				Error:                err.Error(),
			})
			return exactSessionStartUnowned, nil
		}
		return exactSessionStartUnowned, fmt.Errorf("reconciling exact session start %q: %w", admission.SessionID, err)
	}
	retainStatusFromInitialRead := func(input exactSessionLifecycleStatusInput) {
		input.Admission = admission
		input.ControllerGeneration = params.Generation
		input.RequestedID = admission.SessionID
		input.Info = info
		input.LoadedRevision = loadedRevision
		retainStatus(input)
	}
	if info.ID != admission.SessionID {
		mismatchErr := fmt.Errorf("authoritative read returned %q", info.ID)
		retainStatusFromInitialRead(exactSessionLifecycleStatusInput{
			UnavailableReason: exactSessionLifecycleStatusReasonInvalidInput,
			Error:             mismatchErr.Error(),
		})
		return exactSessionStartUnowned, fmt.Errorf("reconciling exact session start %q: %w", admission.SessionID, mismatchErr)
	}
	if info.Closed {
		retainStatusFromInitialRead(exactSessionLifecycleStatusInput{})
		return exactSessionStartUnowned, nil
	}

	ownershipNow := clk.Now().UTC()
	lifecycle, cfgAgent, owner := classifyExactSessionStartOwnership(info, params.Config, ownershipNow)
	if owner != exactSessionStartKeyedOwner {
		reason := exactSessionLifecycleStatusReasonNotObserved
		if owner == exactSessionStartLegacyOwner {
			template := resolvedSessionTemplateInfo(info, params.Config)
			if template == "" || findAgentByTemplate(params.Config, template) == nil {
				reason = exactSessionLifecycleStatusReasonPrerequisiteUnavailable
			}
		}
		retainStatusFromInitialRead(exactSessionLifecycleStatusInput{
			Context:           exactSessionLifecycleStatusContextUnavailable,
			UnavailableReason: reason,
		})
		return owner, nil
	}

	template := resolvedSessionTemplateInfo(info, params.Config)
	if template == "" {
		templateErr := fmt.Errorf("persisted template is empty")
		retainStatusFromInitialRead(exactSessionLifecycleStatusInput{UnavailableReason: exactSessionLifecycleStatusReasonPrerequisiteUnavailable, Error: templateErr.Error()})
		return owner, fmt.Errorf("reconciling exact session start %q: %w", info.ID, templateErr)
	}
	if cfgAgent == nil {
		retainStatusFromInitialRead(exactSessionLifecycleStatusInput{UnavailableReason: exactSessionLifecycleStatusReasonPrerequisiteUnavailable})
		return owner, nil
	}
	if isAgentEffectivelySuspendedWith(params.Config, cfgAgent, loadSuspensionStateBestEffort(params.CityPath)) {
		retainStatusFromInitialRead(exactSessionLifecycleStatusInput{UnavailableReason: exactSessionLifecycleStatusReasonNotObserved})
		return owner, nil
	}
	if lifecycle.HasBlocker(sessionpkg.BlockerHeld) || lifecycle.HasBlocker(sessionpkg.BlockerQuarantined) {
		retainStatusFromInitialRead(exactSessionLifecycleStatusInput{UnavailableReason: exactSessionLifecycleStatusReasonNotObserved})
		return owner, nil
	}

	tp, err := resolveExactSessionStartTemplate(params, info, cfgAgent, clk, stderr)
	if err != nil {
		retainStatusFromInitialRead(exactSessionLifecycleStatusInput{UnavailableReason: exactSessionLifecycleStatusReasonPrerequisiteUnavailable, Error: err.Error()})
		return owner, fmt.Errorf("reconciling exact session start %q: resolving template: %w", info.ID, err)
	}
	observation, err := observeLoadedSession(
		ctx, params.CityPath, params.Store, params.Provider, params.Config, info, tp.Hints.ProcessNames,
	)
	if err != nil {
		retainStatusFromInitialRead(exactSessionLifecycleStatusInput{UnavailableReason: exactSessionLifecycleStatusReasonObservationUnavailable, Error: err.Error()})
		return owner, fmt.Errorf("reconciling exact session start %q: observing runtime: %w", info.ID, err)
	}
	statusObservedAt := clk.Now().UTC()
	retainStatusFromInitialRead(exactSessionLifecycleStatusInput{
		Context:            exactSessionLifecycleStatusContextDesired,
		Observation:        observation,
		ObservedAt:         statusObservedAt,
		StartupTimeout:     params.Config.Session.StartupTimeoutDuration(),
		PrerequisitesReady: true,
	})
	if (params.StatusWriter != nil || params.StatusWriterError != nil) &&
		statusResult != nil && statusResult.Plan != nil && statusResult.Plan.Outcome == sessionLifecycleStatusHeal {
		if params.StatusWriterError != nil {
			return owner, fmt.Errorf("reconciling exact session start %q: resolving session-status writer: %w", info.ID, params.StatusWriterError)
		}
		plan := statusResult.Plan
		if statusResult.Context != exactSessionLifecycleStatusContextDesired ||
			statusResult.Disposition != exactSessionLifecycleStatusDispositionCandidate ||
			statusResult.RequestedID == "" || statusResult.RequestedID != statusResult.LoadedID ||
			statusResult.RequestedID != plan.SessionID || statusResult.LoadedRevision <= 0 || len(plan.Patch) == 0 {
			return owner, fmt.Errorf("reconciling exact session start %q: malformed session-status heal candidate", info.ID)
		}
		if err := params.StatusWriter.UpdateIfMatch(statusResult.RequestedID, statusResult.LoadedRevision, beads.UpdateOpts{Metadata: plan.Patch}); err != nil {
			return owner, fmt.Errorf("reconciling exact session start %q: applying session-status heal: %w", info.ID, err)
		}
	}

	startupTimeout := params.Config.Session.StartupTimeoutDuration()
	circuitOpen := strings.TrimSpace(info.SessionCircuitState) == sessionpkg.SessionCircuitStateOpen
	if cbCfg, enabled := sessionCircuitBreakerConfigFromCity(params.Config); enabled {
		cb := defaultSessionCircuitBreaker()
		cb.configure(cbCfg)
		if identity := namedSessionIdentityInfo(info); identity != "" && cb.IsOpen(identity, ownershipNow) {
			circuitOpen = true
		}
	}
	providerUnavailable := false
	if tp.ResolvedProvider != nil {
		healthy, present := loadProviderHealthSnapshot(params.CityPath).check(tp.ResolvedProvider.Name)
		providerUnavailable = present && !healthy
	}
	plan := planSessionLifecycleStartSelection(sessionLifecycleStartShadowInput{
		Info:                 info,
		WakeDecisionObserved: true,
		ShouldWake:           true,
		RuntimeObserved:      true,
		RuntimeAlive:         runtimeObservationLive(observation),
		ObservedAt:           ownershipNow,
		StartupTimeout:       startupTimeout,
		CircuitOpen:          circuitOpen,
		ProviderUnavailable:  providerUnavailable,
	})
	if plan.Outcome != sessionLifecycleStartSelectionPrepare {
		return owner, nil
	}

	prepared, err := prepareExactStartCandidateForCity(
		startCandidate{info: info, tp: tp},
		params.CityPath,
		params.CityName,
		params.Config,
		params.Provider,
		params.Store,
		clk,
		stderr,
		startOpts.workDirResolver,
	)
	if err != nil {
		var skip *exactSessionStartPreWakeSkip
		if errors.As(err, &skip) {
			return skip.owner, nil
		}
		return owner, fmt.Errorf("reconciling exact session start %q: preparing start: %w", info.ID, err)
	}
	results := executePreparedStartWaveForCity(
		ctx,
		[]preparedStart{*prepared},
		params.CityPath,
		params.Provider,
		params.Store,
		params.Config,
		startupTimeout,
		1,
		params.StartOptions...,
	)
	if len(results) != 1 {
		return owner, fmt.Errorf("reconciling exact session start %q: start returned %d results", info.ID, len(results))
	}
	result := results[0]
	disposition := commitStartResultWithFreshness(
		ctx, result, params.Provider, params.Store, clk, recorder, 0, stdout, stderr, nil,
	)
	if disposition == startCommitSuperseded {
		return owner, nil
	}
	if disposition != startCommitCommitted {
		if result.err != nil {
			return owner, fmt.Errorf("reconciling exact session start %q: %w", info.ID, result.err)
		}
		return owner, fmt.Errorf("reconciling exact session start %q: start result did not commit", info.ID)
	}
	return owner, nil
}

// resolveExactSessionStartOwnership projects the durable start family once and
// returns whether the keyed controller owns it. Dependency-bearing templates
// remain legacy-owned until keyed dependency fan-out exists.
func resolveExactSessionStartOwnership(
	info sessionpkg.Info,
	cfg *config.City,
	now time.Time,
) bool {
	_, _, owner := classifyExactSessionStartOwnership(info, cfg, now)
	return owner == exactSessionStartKeyedOwner
}

func classifyExactSessionStartOwnership(
	info sessionpkg.Info,
	cfg *config.City,
	now time.Time,
) (sessionpkg.LifecycleView, *config.Agent, exactSessionStartOwner) {
	lifecycleInput := sessionpkg.LifecycleInputFromInfo(info)
	lifecycleInput.Now = now
	lifecycleInput.CreatedAt = info.CreatedAt
	lifecycleInput.StaleCreatingAfter = staleCreatingStateTimeout
	lifecycle := sessionpkg.ProjectLifecycle(lifecycleInput)
	ownedCause := lifecycle.HasWakeCause(sessionpkg.WakeCausePendingCreate) ||
		lifecycle.HasWakeCause(sessionpkg.WakeCauseExplicit)
	if info.Closed || !ownedCause || lifecycle.Terminal {
		return lifecycle, nil, exactSessionStartUnowned
	}
	// Named-session canonicalization and pool capacity/slot validation are fleet
	// invariants. Until those projections are available by immutable key, the
	// fleet reconciler remains their sole effect owner.
	if isNamedSessionInfo(info) || isPoolManagedSessionInfo(info) {
		return lifecycle, nil, exactSessionStartLegacyOwner
	}

	template := resolvedSessionTemplateInfo(info, cfg)
	if template == "" {
		return lifecycle, nil, exactSessionStartKeyedOwner
	}
	cfgAgent := findAgentByTemplate(cfg, template)
	if cfgAgent == nil {
		return lifecycle, nil, exactSessionStartLegacyOwner
	}
	// Dependency-bearing templates remain legacy-owned until the keyed reverse
	// dependency index lands. Starting them here would bypass the existing
	// dependency wave gate.
	if len(cfgAgent.DependsOn) > 0 {
		return lifecycle, cfgAgent, exactSessionStartLegacyOwner
	}
	if lifecycle.HasWakeCause(sessionpkg.WakeCauseExplicit) && info.DependencyOnly {
		return lifecycle, cfgAgent, exactSessionStartLegacyOwner
	}
	return lifecycle, cfgAgent, exactSessionStartKeyedOwner
}

func exactSessionStartOwnerForKey(
	store beads.Store,
	cfg *config.City,
	sessionID string,
	now time.Time,
) (exactSessionStartOwner, error) {
	if store == nil {
		return exactSessionStartUnowned, fmt.Errorf("session store is nil")
	}
	info, _, err := getAuthoritativeSessionStartRecord(store, sessionID)
	if err != nil {
		return exactSessionStartUnowned, err
	}
	_, _, owner := classifyExactSessionStartOwnership(info, cfg, now)
	return owner, nil
}

func resolveExactSessionStartTemplate(
	params exactSessionStartParams,
	info sessionpkg.Info,
	cfgAgent *config.Agent,
	clk clock.Clock,
	stderr io.Writer,
) (TemplateParams, error) {
	cityName := params.CityName
	if cityName == "" {
		cityName = config.EffectiveCityName(params.Config, "")
	}
	if isNamedSessionInfo(info) {
		return resolvePreservedConfiguredNamedSessionTemplate(
			params.CityPath,
			cityName,
			params.Config,
			params.Provider,
			params.Store,
			[]sessionpkg.Info{info},
			info,
			clk,
			stderr,
		)
	}

	bp := newAgentBuildParams(cityName, params.CityPath, params.Config, params.Provider, clk.Now().UTC(), params.Store, stderr)
	bp.sessionBeads = newSessionBeadSnapshotFromInfos([]sessionpkg.Info{info})
	var (
		resolveAgent  *config.Agent
		qualifiedName string
	)
	if isManualSessionInfoForAgent(info, cfgAgent) {
		qualifiedName = sessionBeadQualifiedNameInfo(params.CityPath, cfgAgent, bp.rigs, info)
		resolveAgent = sessionBeadConfigAgent(cfgAgent, qualifiedName)
	} else {
		resolveAgent, qualifiedName = canonicalSessionIdentityWithConfigInfo(params.Config, cfgAgent, info)
	}
	if resolveAgent == nil || qualifiedName == "" {
		return TemplateParams{}, fmt.Errorf("configured session identity is unresolved")
	}
	tp, err := resolveTemplateForSessionBeadInfo(bp, resolveAgent, qualifiedName, buildFingerprintExtra(resolveAgent), info)
	if err != nil {
		return TemplateParams{}, err
	}
	tp.ManualSession = isManualSessionInfoForAgent(info, cfgAgent)
	if tp.ManualSession {
		if alias := strings.TrimSpace(info.Alias); alias != "" {
			tp.Alias = alias
		}
	}
	if isEphemeralSessionInfoForAgent(info, cfgAgent) {
		if !tp.ManualSession || strings.TrimSpace(info.Alias) == "" {
			tp.Alias = ""
		}
		if tp.ManualSession && qualifiedName != "" {
			tp.InstanceName = qualifiedName
		} else {
			tp.InstanceName = info.SessionNameMetadata
		}
	}
	installAgentSideEffects(bp, cfgAgent, tp, stderr)
	return tp, nil
}
