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
)

// exactSessionStartParams is one coherent runtime generation for exact-key
// start reconciliation. Callers must capture Config, Provider, and Store
// together before invoking reconcileExactSessionStart.
type exactSessionStartParams struct {
	CityPath     string
	CityName     string
	Config       *config.City
	Provider     runtime.Provider
	Store        beads.Store
	Clock        clock.Clock
	Recorder     events.Recorder
	Stdout       io.Writer
	Stderr       io.Writer
	StartOptions []startExecutionOption
}

// reconcileExactSessionStart rereads one durable session key and executes only
// the pending-create and explicit-wake start family. The admission source is a
// scheduling hint; persisted lifecycle state remains authoritative.
func reconcileExactSessionStart(ctx context.Context, admission sessionStartAdmission, params exactSessionStartParams) error {
	if ctx == nil {
		return fmt.Errorf("reconciling exact session start %q: context is nil", admission.SessionID)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if params.Config == nil {
		return fmt.Errorf("reconciling exact session start %q: config is nil", admission.SessionID)
	}
	if params.Provider == nil {
		return fmt.Errorf("reconciling exact session start %q: runtime provider is nil", admission.SessionID)
	}
	if params.Store == nil {
		return fmt.Errorf("reconciling exact session start %q: session store is nil", admission.SessionID)
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

	sessFront := sessionFrontDoor(params.Store)
	info, _, err := sessFront.GetPersistedResponse(admission.SessionID)
	if err != nil {
		if errors.Is(err, beads.ErrNotFound) || errors.Is(err, sessionpkg.ErrSessionNotFound) {
			return nil
		}
		return fmt.Errorf("reconciling exact session start %q: %w", admission.SessionID, err)
	}
	if info.Closed {
		return nil
	}

	now := clk.Now().UTC()
	lifecycle, cfgAgent, owned := resolveExactSessionStartOwnership(info, params.Config, now)
	if !owned {
		return nil
	}

	template := resolvedSessionTemplateInfo(info, params.Config)
	if template == "" {
		return fmt.Errorf("reconciling exact session start %q: persisted template is empty", info.ID)
	}
	if cfgAgent == nil {
		return nil
	}
	if isAgentEffectivelySuspendedWith(params.Config, cfgAgent, loadSuspensionStateBestEffort(params.CityPath)) {
		return nil
	}
	if lifecycle.HasBlocker(sessionpkg.BlockerHeld) || lifecycle.HasBlocker(sessionpkg.BlockerQuarantined) {
		return nil
	}

	tp, err := resolveExactSessionStartTemplate(params, info, cfgAgent, clk, stderr)
	if err != nil {
		return fmt.Errorf("reconciling exact session start %q: resolving template: %w", info.ID, err)
	}
	observation, err := workerObserveLoadedSessionWithRuntimeHintsWithConfig(
		ctx, params.CityPath, params.Store, params.Provider, params.Config, info, tp.Hints.ProcessNames,
	)
	if err != nil {
		return fmt.Errorf("reconciling exact session start %q: observing runtime: %w", info.ID, err)
	}

	startupTimeout := params.Config.Session.StartupTimeoutDuration()
	circuitOpen := strings.TrimSpace(info.SessionCircuitState) == sessionpkg.SessionCircuitStateOpen
	if cbCfg, enabled := sessionCircuitBreakerConfigFromCity(params.Config); enabled {
		cb := defaultSessionCircuitBreaker()
		cb.configure(cbCfg)
		if identity := namedSessionIdentityInfo(info); identity != "" && cb.IsOpen(identity, now) {
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
		ObservedAt:           now,
		StartupTimeout:       startupTimeout,
		CircuitOpen:          circuitOpen,
		ProviderUnavailable:  providerUnavailable,
	})
	if plan.Outcome != sessionLifecycleStartSelectionPrepare {
		return nil
	}

	startOpts := startExecutionOptions{}
	for _, apply := range params.StartOptions {
		if apply != nil {
			apply(&startOpts)
		}
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
		return fmt.Errorf("reconciling exact session start %q: preparing start: %w", info.ID, err)
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
		return fmt.Errorf("reconciling exact session start %q: start returned %d results", info.ID, len(results))
	}
	result := results[0]
	disposition := commitStartResultWithFreshness(
		ctx, result, params.Provider, params.Store, clk, recorder, 0, stdout, stderr, nil,
	)
	if disposition == startCommitSuperseded {
		return nil
	}
	if disposition != startCommitCommitted {
		if result.err != nil {
			return fmt.Errorf("reconciling exact session start %q: %w", info.ID, result.err)
		}
		return fmt.Errorf("reconciling exact session start %q: start result did not commit", info.ID)
	}
	return nil
}

// resolveExactSessionStartOwnership projects the durable start family once and
// returns whether the keyed controller owns it. Dependency-bearing templates
// remain legacy-owned until keyed dependency fan-out exists.
func resolveExactSessionStartOwnership(
	info sessionpkg.Info,
	cfg *config.City,
	now time.Time,
) (sessionpkg.LifecycleView, *config.Agent, bool) {
	lifecycleInput := sessionpkg.LifecycleInputFromInfo(info)
	lifecycleInput.Now = now
	lifecycleInput.CreatedAt = info.CreatedAt
	lifecycleInput.StaleCreatingAfter = staleCreatingStateTimeout
	lifecycle := sessionpkg.ProjectLifecycle(lifecycleInput)
	ownedCause := lifecycle.HasWakeCause(sessionpkg.WakeCausePendingCreate) ||
		lifecycle.HasWakeCause(sessionpkg.WakeCauseExplicit)
	if info.Closed || !ownedCause || lifecycle.Terminal {
		return lifecycle, nil, false
	}

	template := resolvedSessionTemplateInfo(info, cfg)
	if template == "" {
		return lifecycle, nil, true
	}
	cfgAgent := findAgentByTemplate(cfg, template)
	if cfgAgent == nil {
		return lifecycle, nil, false
	}
	// Dependency-bearing templates remain legacy-owned until the keyed reverse
	// dependency index lands. Starting them here would bypass the existing
	// dependency wave gate.
	if len(cfgAgent.DependsOn) > 0 {
		return lifecycle, cfgAgent, false
	}
	if lifecycle.HasWakeCause(sessionpkg.WakeCauseExplicit) && info.DependencyOnly {
		return lifecycle, cfgAgent, false
	}
	return lifecycle, cfgAgent, true
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
