package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/clock"
	"github.com/gastownhall/gascity/internal/config"
	"github.com/gastownhall/gascity/internal/events"
	"github.com/gastownhall/gascity/internal/rollout"
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
	Generation                        uint64
	CityPath                          string
	CityName                          string
	Config                            *config.City
	Provider                          runtime.Provider
	Store                             beads.Store
	StatusWriter                      beads.ConditionalWriter
	StatusWriterError                 error
	Clock                             clock.Clock
	Recorder                          events.Recorder
	Stdout                            io.Writer
	Stderr                            io.Writer
	ObserveLoadedSession              exactLoadedSessionObserver
	StartOptions                      []startExecutionOption
	AsyncStopTracker                  *asyncStartTracker
	AsyncStopCompletion               func(drainAckAsyncStopCompletion)
	AsyncStopQueued                   func()
	RolloutMode                       rollout.Mode
	RigStores                         map[string]beads.Store
	DrainOps                          drainOps
	DrainTracker                      *drainTracker
	Trace                             *SessionReconcilerTracer
	AuthorizePoolStart                func(context.Context, sessionpkg.Info, routedWorkPoolStartLease) (bool, error)
	AuthorizePoolDrainAck             func(sessionpkg.Info, routedWorkPoolDrainAckLease) (bool, error)
	RecoverPoolDrainAck               func(sessionpkg.Info) (routedWorkPoolDrainAckLease, bool, bool, error)
	ValidateWaitDependencyPoolWitness func(sessionpkg.Info, sessionWaitDependencyStartLease) bool
}

// sessionWaitDependencyStartLease binds one dependency-ready wait to the exact
// session row and controller generation that certified it. It is deliberately
// small: the durable wait and session rows remain the source of truth.
type sessionWaitDependencyStartLease struct {
	WaitID                 string
	SessionID              string
	DepIDs                 []string
	DepMode                string
	RegisteredEpoch        string
	WaitRevision           int64
	SessionRevision        int64
	IndexGeneration        uint64
	ControllerGeneration   uint64
	PoolTarget             string
	PoolMembershipRevision uint64
	Operation              string
}

func isCanonicalConfiguredNamedSessionForStart(info sessionpkg.Info, cfg *config.City) bool {
	identity := strings.TrimSpace(info.ConfiguredNamedIdentity)
	if !isNamedSessionInfo(info) || identity == "" || cfg == nil {
		return false
	}
	spec, ok := findNamedSessionSpec(cfg, cfg.EffectiveCityName(), identity)
	return ok && info.SessionName == spec.SessionName
}

func validateSessionWaitDependencyStartLease(lease sessionWaitDependencyStartLease) error {
	if lease.WaitID == "" || strings.TrimSpace(lease.WaitID) != lease.WaitID {
		return errors.New("dependency wait lease has invalid wait id")
	}
	if lease.SessionID == "" || strings.TrimSpace(lease.SessionID) != lease.SessionID {
		return errors.New("dependency wait lease has invalid session id")
	}
	if (lease.DepMode != "all" && lease.DepMode != "any") || len(lease.DepIDs) == 0 {
		return errors.New("dependency wait lease is outside the exact deps cohort")
	}
	for _, dependencyID := range lease.DepIDs {
		if dependencyID == "" || strings.TrimSpace(dependencyID) != dependencyID {
			return errors.New("dependency wait lease is outside the exact deps cohort")
		}
	}
	if lease.WaitRevision == 0 || lease.SessionRevision == 0 || lease.IndexGeneration == 0 || lease.ControllerGeneration == 0 {
		return errors.New("dependency wait lease lacks revision or generation provenance")
	}
	if lease.RegisteredEpoch == "" || strings.TrimSpace(lease.RegisteredEpoch) != lease.RegisteredEpoch {
		return errors.New("dependency wait lease lacks an exact registered epoch")
	}
	if lease.Operation == "" || strings.TrimSpace(lease.Operation) != lease.Operation {
		return errors.New("dependency wait lease has invalid operation")
	}
	if (lease.PoolTarget == "") != (lease.PoolMembershipRevision == 0) ||
		lease.PoolTarget != strings.TrimSpace(lease.PoolTarget) {
		return errors.New("dependency wait lease has an incomplete bounded-pool witness")
	}
	return nil
}

// certifySessionWaitDependencyStartLease rereads the two durable rows that a
// dependency-ready hint names. The producer index is only routing state; this
// certificate is the authority retained by the keyed worker before it mutates
// either row.
func certifySessionWaitDependencyStartLease(
	store beads.Store,
	target sessionWaitDependencyTarget,
	dependencies waitDependencyReader,
	cfg *config.City,
	provider runtime.Provider,
	cityName string,
	generation uint64,
	membership *poolMembershipIndex,
	now time.Time,
) (sessionWaitDependencyStartLease, exactSessionStartOwner, error) {
	if store == nil || cfg == nil || provider == nil || generation == 0 {
		return sessionWaitDependencyStartLease{}, exactSessionStartUnowned, errors.New("dependency wait start prerequisites are unavailable")
	}
	if outcome, err := validateExactSessionWaitDependencyShadow(store, target, dependencies, now); err != nil || outcome != sessionWaitDependencyEvaluationReady {
		if err != nil {
			return sessionWaitDependencyStartLease{}, exactSessionStartUnowned, err
		}
		return sessionWaitDependencyStartLease{}, exactSessionStartUnowned, nil
	}
	readStore := authoritativeSessionStartReadStore{Store: store, live: beads.HandlesFor(store).Live}
	wait, persistedWait, err := sessionFrontDoor(readStore).GetWaitPersistedResponse(target.WaitID)
	if err != nil {
		return sessionWaitDependencyStartLease{}, exactSessionStartUnowned, fmt.Errorf("reading certified dependency wait %q: %w", target.WaitID, err)
	}
	info, persistedSession, err := getAuthoritativeSessionStartPersistedRecord(store, target.SessionID)
	if err != nil {
		return sessionWaitDependencyStartLease{}, exactSessionStartUnowned, fmt.Errorf("reading certified dependency session %q: %w", target.SessionID, err)
	}
	registration, indexable, err := waitDependencyRegistrationFrom(wait)
	if err != nil {
		return sessionWaitDependencyStartLease{}, exactSessionStartUnowned, fmt.Errorf("canonicalizing certified dependency wait %q: %w", target.WaitID, err)
	}
	if wait.ID != target.WaitID || !indexable || registration.sessionID != target.SessionID || registration.depMode != target.DepMode || !slices.Equal(registration.depIDs, target.DepIDs) {
		return sessionWaitDependencyStartLease{}, exactSessionStartUnowned, nil
	}
	if info.ID != target.SessionID || info.Closed || persistedWait.Revision == 0 || persistedSession.Revision == 0 {
		return sessionWaitDependencyStartLease{}, exactSessionStartUnowned, nil
	}
	_, cfgAgent, _ := classifyExactSessionStartOwnership(info, cfg, now)
	if cfgAgent == nil {
		template := resolvedSessionTemplateInfo(info, cfg)
		cfgAgent = findAgentByTemplate(cfg, template)
	}
	if cfgAgent == nil || !waitDependencyConfiguredTemplateEligible(info, cfg, provider, cityName, store, now) || info.DependencyOnly || (isNamedSessionInfo(info) && !isCanonicalConfiguredNamedSessionForStart(info, cfg)) || wait.RegisteredEpoch == "" || info.ContinuationEpoch == "" || wait.RegisteredEpoch != info.ContinuationEpoch || target.generation == 0 {
		return sessionWaitDependencyStartLease{}, exactSessionStartLegacyOwner, nil
	}
	if info.MetadataState != string(sessionpkg.StateAsleep) || info.PendingCreateClaim ||
		info.WaitHold == "" || info.SleepIntent != string(sessionpkg.SleepReasonWaitHold) || info.SleepReason != string(sessionpkg.SleepReasonWaitHold) {
		return sessionWaitDependencyStartLease{}, exactSessionStartLegacyOwner, nil
	}
	poolTarget := ""
	poolMembershipRevision := uint64(0)
	if boundedTarget, bounded := waitDependencyBoundedPoolTarget(info, cfg); bounded {
		observation, memberIDs, exact := membership.observeMemberIDs(boundedTarget)
		if !exact || observation.revision == 0 || !observation.certified || observation.members != 1 || observation.occupied != 0 ||
			len(memberIDs) != 1 || memberIDs[0] != info.ID {
			return sessionWaitDependencyStartLease{}, exactSessionStartLegacyOwner, nil
		}
		poolTarget = boundedTarget
		poolMembershipRevision = observation.revision
	}
	lease := sessionWaitDependencyStartLease{
		WaitID:                 wait.ID,
		SessionID:              info.ID,
		DepIDs:                 append([]string(nil), registration.depIDs...),
		DepMode:                registration.depMode,
		RegisteredEpoch:        wait.RegisteredEpoch,
		WaitRevision:           persistedWait.Revision,
		SessionRevision:        persistedSession.Revision,
		IndexGeneration:        target.generation,
		ControllerGeneration:   generation,
		PoolTarget:             poolTarget,
		PoolMembershipRevision: poolMembershipRevision,
		Operation:              sessionpkg.NewInstanceToken(),
	}
	if err := validateSessionWaitDependencyStartLease(lease); err != nil {
		return sessionWaitDependencyStartLease{}, exactSessionStartUnowned, err
	}
	return lease, exactSessionStartKeyedOwner, nil
}

func waitDependencyBoundedPoolTarget(info sessionpkg.Info, cfg *config.City) (string, bool) {
	if cfg == nil || !isPoolManagedSessionInfo(info) {
		return "", false
	}
	agent := findAgentByTemplate(cfg, resolvedSessionTemplateInfo(info, cfg))
	if agent == nil {
		return "", false
	}
	namedTemplates := make(map[string]struct{}, len(cfg.NamedSessions))
	for i := range cfg.NamedSessions {
		namedTemplates[cfg.NamedSessions[i].TemplateQualifiedName()] = struct{}{}
	}
	policy := newPoolAllocationShadowPolicy(cfg, agent, namedTemplates)
	return agent.QualifiedName(), policy.reason == poolAllocationShadowEligibleAgentCap &&
		policy.maxActiveSessions > 1 && agent.EffectiveMinActiveSessions() == 0
}

// waitDependencyConfiguredTemplateEligible admits ordinary configured sessions
// and configured dependencies only when every dependency is a currently-live
// canonical singleton.
func waitDependencyConfiguredTemplateEligible(
	info sessionpkg.Info,
	cfg *config.City,
	provider runtime.Provider,
	cityName string,
	store beads.Store,
	now time.Time,
) bool {
	template := resolvedSessionTemplateInfo(info, cfg)
	cfgAgent := findAgentByTemplate(cfg, template)
	if cfgAgent == nil {
		return false
	}
	if isPoolManagedSessionInfo(info) {
		poolSlot, err := strconv.Atoi(strings.TrimSpace(info.PoolSlot))
		if info.SessionOrigin != "ephemeral" || info.TriggerBeadID == "" || info.DependencyOnly ||
			isNamedSessionInfo(info) || isManualSessionInfoForAgent(info, cfgAgent) ||
			!isEphemeralSessionInfoForAgent(info, cfgAgent) || err != nil || poolSlot <= 0 ||
			existingPoolSlotWithConfigInfo(cfg, cfgAgent, info) != poolSlot ||
			info.AgentName != cfgAgent.QualifiedInstanceName(poolInstanceName(cfgAgent.Name, poolSlot, cfgAgent)) ||
			info.SessionNameMetadata != PoolSessionName(cfgAgent.QualifiedName(), info.ID) {
			return false
		}
		namedTemplates := make(map[string]struct{}, len(cfg.NamedSessions))
		for i := range cfg.NamedSessions {
			namedTemplates[cfg.NamedSessions[i].TemplateQualifiedName()] = struct{}{}
		}
		policy := newPoolAllocationShadowPolicy(cfg, cfgAgent, namedTemplates)
		return policy.reason == poolAllocationShadowEligible ||
			policy.reason == poolAllocationShadowEligibleAgentCap && policy.maxActiveSessions > 1 && cfgAgent.EffectiveMinActiveSessions() == 0
	}
	if len(cfgAgent.DependsOn) == 0 {
		return true
	}
	for _, dependencyTemplate := range cfgAgent.DependsOn {
		dependency := findAgentByTemplate(cfg, dependencyTemplate)
		if dependency == nil || isMultiSessionCfgAgent(dependency) {
			return false
		}
	}
	return allDependenciesAliveForTemplateWithClock(template, cfg, nil, provider, cityName, store, &clock.Fake{Time: now})
}

// planExactSessionWaitDependencyStartShadow reads one dependency-ready session
// and evaluates the existing start-selection planner without retaining a plan
// or performing a lifecycle effect.
func planExactSessionWaitDependencyStartShadow(
	ctx context.Context,
	sessionID string,
	params exactSessionStartParams,
) (sessionLifecycleStartSelectionPlan, error) {
	plan := sessionLifecycleStartSelectionPlan{SessionID: sessionID}
	if ctx == nil || ctx.Err() != nil {
		plan.Outcome = sessionLifecycleStartSelectionPark
		plan.Reason = sessionLifecycleStartSelectionReasonRuntimeUnknown
		return plan, nil
	}
	if sessionID == "" || strings.TrimSpace(sessionID) != sessionID {
		plan.Outcome = sessionLifecycleStartSelectionPark
		plan.Reason = sessionLifecycleStartSelectionReasonInvalidInput
		return plan, nil
	}
	if params.Config == nil || params.Provider == nil || params.Store == nil {
		plan.Outcome = sessionLifecycleStartSelectionPark
		plan.Reason = sessionLifecycleStartSelectionReasonConfigSuppressed
		return plan, nil
	}
	clk := params.Clock
	if clk == nil {
		clk = clock.Real{}
	}
	info, _, err := getAuthoritativeSessionStartRecord(params.Store, sessionID)
	if err != nil {
		if errors.Is(err, beads.ErrNotFound) || errors.Is(err, sessionpkg.ErrSessionNotFound) {
			plan.Outcome = sessionLifecycleStartSelectionNoop
			plan.Reason = sessionLifecycleStartSelectionReasonTerminal
			return plan, nil
		}
		return plan, fmt.Errorf("%w: planning exact dependency session start %q: %w", errSessionWaitDependencyTargetReadUnavailable, sessionID, err)
	}
	if info.ID != sessionID {
		return plan, fmt.Errorf("planning exact dependency session start %q: authoritative read returned %q", sessionID, info.ID)
	}
	if info.Closed {
		return planSessionLifecycleStartSelection(sessionLifecycleStartShadowInput{Info: info}), nil
	}
	template := resolvedSessionTemplateInfo(info, params.Config)
	cfgAgent := findAgentByTemplate(params.Config, template)
	if cfgAgent == nil {
		return planSessionLifecycleStartSelection(sessionLifecycleStartShadowInput{
			Info:             info,
			ConfigSuppressed: true,
		}), nil
	}
	if isAgentEffectivelySuspendedWith(params.Config, params.CityPath, cfgAgent, loadSuspensionStateBestEffort(params.CityPath)) {
		return planSessionLifecycleStartSelection(sessionLifecycleStartShadowInput{
			Info:                 info,
			WakeDecisionObserved: true,
			ShouldWake:           true,
			ConfigSuppressed:     true,
		}), nil
	}
	resolvedProvider, err := config.ResolveProvider(cfgAgent, &params.Config.Workspace, params.Config.Providers, exec.LookPath)
	if err != nil {
		return planSessionLifecycleStartSelection(sessionLifecycleStartShadowInput{
			Info:                 info,
			WakeDecisionObserved: true,
			ShouldWake:           true,
			ConfigSuppressed:     true,
		}), nil
	}
	observeLoadedSession := params.ObserveLoadedSession
	if observeLoadedSession == nil {
		observeLoadedSession = observeExactSessionWaitDependencyShadowRuntime
	}
	observation, err := observeLoadedSession(ctx, params.CityPath, params.Store, params.Provider, params.Config, info, resolvedProvider.ProcessNames)
	if err != nil {
		return planSessionLifecycleStartSelection(sessionLifecycleStartShadowInput{
			Info:                 info,
			WakeDecisionObserved: true,
			ShouldWake:           true,
		}), nil
	}
	now := clk.Now().UTC()
	circuitOpen := strings.TrimSpace(info.SessionCircuitState) == sessionpkg.SessionCircuitStateOpen
	if cbCfg, enabled := sessionCircuitBreakerConfigFromCity(params.Config); enabled {
		cb := defaultSessionCircuitBreaker()
		cb.configure(cbCfg)
		if identity := namedSessionIdentityInfo(info); identity != "" && cb.IsOpen(identity, now) {
			circuitOpen = true
		}
	}
	providerUnavailable := false
	if resolvedProvider != nil {
		healthy, present := loadProviderHealthSnapshot(params.CityPath).check(resolvedProvider.Name)
		providerUnavailable = present && !healthy
	}
	return planSessionLifecycleStartSelection(sessionLifecycleStartShadowInput{
		Info:                 info,
		WakeDecisionObserved: true,
		ShouldWake:           true,
		RuntimeObserved:      true,
		RuntimeAlive:         runtimeObservationLive(observation),
		ObservedAt:           now,
		StartupTimeout:       params.Config.Session.StartupTimeoutDuration(),
		CircuitOpen:          circuitOpen,
		ProviderUnavailable:  providerUnavailable,
	}), nil
}

// observeExactSessionWaitDependencyShadowRuntime performs the one liveness
// observation needed by the effect-free dependency shadow. It deliberately
// avoids worker/session construction, which can register ACP routing.
func observeExactSessionWaitDependencyShadowRuntime(
	ctx context.Context,
	_ string,
	_ beads.Store,
	provider runtime.Provider,
	_ *config.City,
	info sessionpkg.Info,
	processNames []string,
) (worker.LiveObservation, error) {
	if ctx == nil {
		return worker.LiveObservation{}, fmt.Errorf("observing dependency shadow session: context is nil")
	}
	if err := ctx.Err(); err != nil {
		return worker.LiveObservation{}, err
	}
	if provider == nil {
		return worker.LiveObservation{}, fmt.Errorf("observing dependency shadow session: runtime provider is nil")
	}
	liveness := runtime.ObserveLiveness(provider, info.SessionName, processNames)
	return worker.LiveObservation{
		Running: liveness.Running, Alive: liveness.Alive, SessionID: info.ID, RuntimeSessionID: info.ID, SessionName: info.SessionName,
	}, nil
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

var errExactPoolRecoveryAuthorityLost = errors.New("exact pool recovery authority lost")

func (e *exactSessionStartPreWakeSkip) Error() string {
	return "exact session start became ineligible before pre-wake commit"
}

// authoritativeSessionStartReadStore forces one exact-key read through the
// store's live handle. It is deliberately confined to start admission and
// commit fences: writes and ordinary reads must retain the original store so
// optional write capabilities and cache refreshes survive.
type authoritativeSessionStartReadStore struct {
	beads.Store
	live beads.LiveReader
}

func (s authoritativeSessionStartReadStore) Get(id string) (beads.Bead, error) {
	return s.live.Get(id)
}

func (s authoritativeSessionStartReadStore) IDPrefix() string {
	if prefixed, ok := s.Store.(interface{ IDPrefix() string }); ok {
		return prefixed.IDPrefix()
	}
	return ""
}

// getAuthoritativeSessionStartRecord reads one persisted session through the
// typed session front door while bypassing an eventual-consistency cache. An
// external CLI can commit a wake and send its socket hint before the matching
// event refreshes the controller cache. Its revision is from that same read;
// callers must not refresh it before a future fenced write.
func getAuthoritativeSessionStartPersistedRecord(store beads.Store, id string) (sessionpkg.Info, sessionpkg.PersistedResponse, error) {
	if store == nil {
		return sessionpkg.Info{}, sessionpkg.PersistedResponse{}, fmt.Errorf("session store is nil")
	}
	readStore := authoritativeSessionStartReadStore{
		Store: store,
		live:  beads.HandlesFor(store).Live,
	}
	info, response, err := sessionFrontDoor(readStore).GetPersistedResponse(id)
	if err != nil {
		return sessionpkg.Info{}, sessionpkg.PersistedResponse{}, err
	}
	return info, response, nil
}

func getAuthoritativeSessionStartRecord(store beads.Store, id string) (sessionpkg.Info, int64, error) {
	info, response, err := getAuthoritativeSessionStartPersistedRecord(store, id)
	if err != nil {
		return sessionpkg.Info{}, 0, err
	}
	return info, response.Revision, nil
}

var drainAckStopPendingRollbackKeys = [...]string{
	"state",
	"state_reason",
	"drain_at",
	"pending_create_claim",
	"pending_create_started_at",
	sessionpkg.DrainAckSourceMetadataKey,
	sessionpkg.DrainAckRequesterSessionIDMetadataKey,
	sessionpkg.DrainAckRequesterInstanceTokenMetadataKey,
}

// drainAckStopPendingRollback captures the exact durable values replaced
// by DrainAckStopPendingPatch and the post-CAS revision that exclusively owns
// their restoration. It intentionally derives both from PersistedResponse,
// rather than adding raw metadata mirrors to session.Info.
type drainAckStopPendingRollback struct {
	revision int64
	values   sessionpkg.MetadataPatch
}

type drainAckStopPendingFence struct {
	revision int64
	values   sessionpkg.MetadataPatch
}

func newDrainAckStopPendingFence(response sessionpkg.PersistedResponse) drainAckStopPendingFence {
	values := make(sessionpkg.MetadataPatch, len(drainAckStopPendingRollbackKeys))
	for _, key := range drainAckStopPendingRollbackKeys {
		values[key] = response.Metadata[key]
	}
	return drainAckStopPendingFence{revision: response.Revision, values: values}
}

func (f drainAckStopPendingFence) matches(info sessionpkg.Info, response sessionpkg.PersistedResponse, expectedID, expectedToken string) bool {
	if f.revision == 0 || response.Revision != f.revision || !isCanonicalDrainAckStopPendingRow(info, response, expectedID, expectedToken) {
		return false
	}
	for _, key := range drainAckStopPendingRollbackKeys {
		if response.Metadata[key] != f.values[key] {
			return false
		}
	}
	return true
}

func (f drainAckStopPendingFence) hasAgentProvenance(expectedID, expectedToken string) bool {
	return f.values[sessionpkg.DrainAckSourceMetadataKey] == sessionpkg.DrainAckSourceAgentValue &&
		f.values[sessionpkg.DrainAckRequesterSessionIDMetadataKey] == expectedID &&
		f.values[sessionpkg.DrainAckRequesterInstanceTokenMetadataKey] == expectedToken
}

func isCanonicalDrainAckStopPendingRow(info sessionpkg.Info, response sessionpkg.PersistedResponse, expectedID, expectedToken string) bool {
	if expectedID == "" || response.Revision == 0 || info.ID != expectedID || info.Closed || response.Status != "open" ||
		strings.TrimSpace(info.InstanceToken) != expectedToken || !isDrainAckStopPendingInfo(info) ||
		response.Metadata["pending_create_claim"] != "" || response.Metadata["pending_create_started_at"] != "" {
		return false
	}
	drainAt, err := time.Parse(time.RFC3339, response.Metadata["drain_at"])
	return err == nil && drainAt.UTC().Format(time.RFC3339) == response.Metadata["drain_at"]
}

func newDrainAckStopPendingRollback(response sessionpkg.PersistedResponse) drainAckStopPendingRollback {
	values := make(sessionpkg.MetadataPatch, len(drainAckStopPendingRollbackKeys))
	for _, key := range drainAckStopPendingRollbackKeys {
		values[key] = response.Metadata[key]
	}
	return drainAckStopPendingRollback{values: values}
}

func (r drainAckStopPendingRollback) matches(info sessionpkg.Info, response sessionpkg.PersistedResponse, expectedID, expectedToken string, patch sessionpkg.MetadataPatch) bool {
	if !isCanonicalDrainAckStopPendingRow(info, response, expectedID, expectedToken) {
		return false
	}
	for _, key := range drainAckStopPendingRollbackKeys {
		if response.Metadata[key] != patch[key] {
			return false
		}
	}
	return true
}

func (r drainAckStopPendingRollback) restore(writer beads.ConditionalWriter, id string) error {
	if writer == nil {
		return errors.New("drain acknowledgement conditional writer is unavailable")
	}
	if len(r.values) != len(drainAckStopPendingRollbackKeys) {
		return errors.New("drain acknowledgement rollback values are unavailable")
	}
	if err := writer.UpdateIfMatch(id, r.revision, beads.UpdateOpts{Metadata: r.values}); err != nil {
		return fmt.Errorf("restoring drain acknowledgement stop-pending transition: %w", err)
	}
	return nil
}

// sessionWaitDependencyEvaluation is the durable wait result observed by the
// effect-free dependency shadow immediately before lifecycle planning.
type sessionWaitDependencyEvaluation string

const (
	sessionWaitDependencyEvaluationReady             sessionWaitDependencyEvaluation = "ready"
	sessionWaitDependencyEvaluationPending           sessionWaitDependencyEvaluation = "pending"
	sessionWaitDependencyEvaluationStaleEpoch        sessionWaitDependencyEvaluation = "stale_epoch"
	sessionWaitDependencyEvaluationClosedSession     sessionWaitDependencyEvaluation = "closed_session"
	sessionWaitDependencyEvaluationExpired           sessionWaitDependencyEvaluation = "expired"
	sessionWaitDependencyEvaluationMissingDependency sessionWaitDependencyEvaluation = "missing_dependency"
	sessionWaitDependencyEvaluationNoopTerminal      sessionWaitDependencyEvaluation = "noop_terminal"
	sessionWaitDependencyEvaluationParkReadError     sessionWaitDependencyEvaluation = "park_read_error"
	sessionWaitDependencyEvaluationStaleTarget       sessionWaitDependencyEvaluation = "stale_target"
)

// getAuthoritativeSessionWait reads a single durable wait through the same
// live store handle used for exact lifecycle admission.
func getAuthoritativeSessionWait(store beads.Store, id string) (sessionpkg.WaitInfo, error) {
	if store == nil {
		return sessionpkg.WaitInfo{}, fmt.Errorf("session store is nil")
	}
	readStore := authoritativeSessionStartReadStore{
		Store: store,
		live:  beads.HandlesFor(store).Live,
	}
	return sessionFrontDoor(readStore).GetWait(id)
}

// validateExactSessionWaitDependencyShadow mirrors the read-only portion of
// the legacy wait ladder for one certified target. It never repairs or
// advances durable state: legacy reconciliation remains the sole mutator.
func validateExactSessionWaitDependencyShadow(
	store beads.Store,
	target sessionWaitDependencyTarget,
	dependencies waitDependencyReader,
	now time.Time,
) (sessionWaitDependencyEvaluation, error) {
	wait, err := getAuthoritativeSessionWait(store, target.WaitID)
	if err != nil {
		if errors.Is(err, beads.ErrNotFound) || errors.Is(err, sessionpkg.ErrNotAWait) {
			return sessionWaitDependencyEvaluationNoopTerminal, nil
		}
		return sessionWaitDependencyEvaluationParkReadError, fmt.Errorf("%w: reading dependency wait %q: %w", errSessionWaitDependencyTargetReadUnavailable, target.WaitID, err)
	}
	if wait.Status != "open" || sessionpkg.IsWaitTerminalState(wait.State) {
		return sessionWaitDependencyEvaluationNoopTerminal, nil
	}
	if wait.State != waitStatePending {
		return sessionWaitDependencyEvaluationPending, nil
	}
	registration, indexable, err := waitDependencyRegistrationFrom(wait)
	if err != nil {
		return sessionWaitDependencyEvaluationParkReadError, fmt.Errorf("validating dependency wait %q: %w", target.WaitID, err)
	}
	if !indexable || !sameSessionWaitDependencyTarget(sessionWaitDependencyTarget{
		WaitID: target.WaitID, SessionID: registration.sessionID, DepIDs: registration.depIDs, DepMode: registration.depMode,
	}, target) {
		return sessionWaitDependencyEvaluationStaleTarget, nil
	}
	info, _, err := getAuthoritativeSessionStartRecord(store, target.SessionID)
	if err != nil {
		if errors.Is(err, beads.ErrNotFound) || errors.Is(err, sessionpkg.ErrSessionNotFound) {
			return sessionWaitDependencyEvaluationNoopTerminal, nil
		}
		return sessionWaitDependencyEvaluationParkReadError, fmt.Errorf("%w: reading dependency wait session %q: %w", errSessionWaitDependencyTargetReadUnavailable, target.SessionID, err)
	}
	if wait.RegisteredEpoch != "" && info.ContinuationEpoch != "" && wait.RegisteredEpoch != info.ContinuationEpoch {
		return sessionWaitDependencyEvaluationStaleEpoch, nil
	}
	if info.Closed {
		return sessionWaitDependencyEvaluationClosedSession, nil
	}
	if wait.ExpiresAt != "" {
		expiresAt, err := time.Parse(time.RFC3339, wait.ExpiresAt)
		if err == nil && !expiresAt.After(now) {
			return sessionWaitDependencyEvaluationExpired, nil
		}
	}
	ready, err := depsWaitReadyDetailedFrom(dependencies, wait)
	if err != nil {
		if errors.Is(err, beads.ErrNotFound) {
			return sessionWaitDependencyEvaluationMissingDependency, nil
		}
		return sessionWaitDependencyEvaluationParkReadError, fmt.Errorf("%w: reading dependency wait %q dependencies: %w", errSessionWaitDependencyTargetReadUnavailable, target.WaitID, err)
	}
	if !ready {
		return sessionWaitDependencyEvaluationPending, nil
	}
	return sessionWaitDependencyEvaluationReady, nil
}

func sessionLifecycleStartSelectionTraceOutcome(outcome sessionLifecycleStartSelectionOutcome) string {
	switch outcome {
	case sessionLifecycleStartSelectionNoop:
		return "noop"
	case sessionLifecycleStartSelectionPrepare:
		return "prepare"
	case sessionLifecycleStartSelectionPark:
		return "park"
	default:
		return ""
	}
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
	if isDrainAckStopPendingInfo(info) {
		return sessionpkg.Info{}, &exactSessionStartPreWakeSkip{owner: exactSessionStartKeyedOwner}
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

	info, initialResponse, err := getAuthoritativeSessionStartPersistedRecord(params.Store, admission.SessionID)
	loadedRevision := initialResponse.Revision
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
	var drainAckRollback *drainAckStopPendingRollback
	var drainAckStopPendingPatch sessionpkg.MetadataPatch
	var drainAckStopPendingFence *drainAckStopPendingFence
	if isDrainAckStopPendingInfo(info) {
		fence := newDrainAckStopPendingFence(initialResponse)
		if fence.matches(info, initialResponse, info.ID, strings.TrimSpace(info.InstanceToken)) {
			drainAckStopPendingFence = &fence
		}
	}
	if admission.PoolDrainAck != nil && !isDrainAckStopPendingInfo(info) {
		transitionFailure := func(cause error) (exactSessionStartOwner, error) {
			if params.RolloutMode == rollout.Require {
				return exactSessionStartKeyedOwner, fmt.Errorf("required exact pool drain acknowledgement refused closed: %w", cause)
			}
			return exactSessionStartLegacyOwner, fmt.Errorf("%w: %w", errSessionStartLegacyFallbackRequired, cause)
		}
		if _, ok := beads.AtomicConditionalCloserFor(params.Store); !ok {
			return transitionFailure(errors.New("drain acknowledgement atomic terminal closer is unavailable"))
		}
		if params.AuthorizePoolDrainAck == nil {
			return transitionFailure(errors.New("drain acknowledgement authorization is unavailable"))
		}
		authorized, authorizeErr := params.AuthorizePoolDrainAck(info, *admission.PoolDrainAck)
		if authorizeErr != nil {
			return transitionFailure(fmt.Errorf("drain acknowledgement authorization: %w", authorizeErr))
		}
		if !authorized {
			return transitionFailure(errors.New("drain acknowledgement authorization no longer holds"))
		}
		if params.StatusWriterError != nil {
			return transitionFailure(fmt.Errorf("drain acknowledgement conditional writer: %w", params.StatusWriterError))
		}
		if params.StatusWriter == nil {
			return transitionFailure(errors.New("drain acknowledgement conditional writer is unavailable"))
		}
		if initialResponse.Status != "open" || loadedRevision == 0 {
			return exactSessionStartKeyedOwner, fmt.Errorf("%w: drain acknowledgement initial row is not an exact open revisioned record", errSessionStartPoolDrainAckPending)
		}
		rollback := newDrainAckStopPendingRollback(initialResponse)
		patch := sessionpkg.AgentDrainAckStopPendingPatch(
			clk.Now().UTC(), admission.PoolDrainAck.RequesterSessionID, admission.PoolDrainAck.RequesterInstanceToken,
		)
		writeErr := params.StatusWriter.UpdateIfMatch(info.ID, loadedRevision, beads.UpdateOpts{Metadata: patch})
		postTransitionFailure := func(cause error) (exactSessionStartOwner, error) {
			if params.RolloutMode == rollout.Require {
				return exactSessionStartKeyedOwner, fmt.Errorf("required exact pool drain acknowledgement refused closed after stop-pending transition: %w", cause)
			}
			if rollbackErr := rollback.restore(params.StatusWriter, info.ID); rollbackErr != nil {
				return exactSessionStartKeyedOwner, fmt.Errorf("reconciling exact pool drain acknowledgement %q: %w; rollback: %w", info.ID, cause, rollbackErr)
			}
			return exactSessionStartLegacyOwner, fmt.Errorf("%w: %w", errSessionStartLegacyFallbackRequired, cause)
		}
		postInfo, postResponse, readErr := getAuthoritativeSessionStartPersistedRecord(params.Store, info.ID)
		if readErr != nil {
			return exactSessionStartKeyedOwner, fmt.Errorf("%w: marking drain acknowledgement stop-pending %q; authoritative reread: %w", errSessionStartPoolDrainAckPending, info.ID, readErr)
		}
		if !rollback.matches(postInfo, postResponse, info.ID, admission.PoolDrainAck.InstanceToken, patch) {
			if writeErr != nil {
				unchanged := postInfo.ID == info.ID && !postInfo.Closed && postResponse.Status == "open" &&
					strings.TrimSpace(postInfo.InstanceToken) == admission.PoolDrainAck.InstanceToken && postResponse.Revision == loadedRevision
				for _, key := range drainAckStopPendingRollbackKeys {
					unchanged = unchanged && postResponse.Metadata[key] == initialResponse.Metadata[key]
				}
				if unchanged {
					return transitionFailure(fmt.Errorf("marking drain acknowledgement stop-pending: %w", writeErr))
				}
				return exactSessionStartKeyedOwner, fmt.Errorf("marking drain acknowledgement stop-pending: %w; authoritative reread does not prove unchanged or committed transition", writeErr)
			}
			return exactSessionStartKeyedOwner, fmt.Errorf("%w: stop-pending transition no longer owns the exact session row", errSessionStartPoolDrainAckPending)
		}
		rollback.revision = postResponse.Revision
		authorized, authorizeErr = params.AuthorizePoolDrainAck(postInfo, *admission.PoolDrainAck)
		if authorizeErr != nil {
			return postTransitionFailure(fmt.Errorf("authorizing stop-pending transition: %w", authorizeErr))
		}
		if !authorized {
			return postTransitionFailure(errors.New("drain acknowledgement authorization no longer holds after stop-pending transition"))
		}
		drainAckRollback = &rollback
		drainAckStopPendingPatch = patch
		fence := newDrainAckStopPendingFence(postResponse)
		drainAckStopPendingFence = &fence
		info = postInfo
		loadedRevision = postResponse.Revision
	}
	if isDrainAckStopPendingInfo(info) {
		park := func(cause error) (exactSessionStartOwner, error) {
			return exactSessionStartKeyedOwner, fmt.Errorf("%w: %w", errSessionStartPoolDrainAckPending, cause)
		}
		if drainAckStopPendingFence == nil {
			return park(errors.New("drain acknowledgement stop-pending row is not canonical or lacks revision provenance"))
		}
		var drainAckLease *routedWorkPoolDrainAckLease
		recoverDrainAckLease := func() (exactSessionStartOwner, error) {
			if drainAckLease != nil {
				return exactSessionStartKeyedOwner, nil
			}
			if params.RecoverPoolDrainAck == nil {
				return park(errors.New("drain acknowledgement lease recovery is unavailable"))
			}
			recoveredLease, agentDrainAck, legacyMarker, recoverErr := params.RecoverPoolDrainAck(info)
			if recoverErr != nil {
				return park(fmt.Errorf("recovering drain acknowledgement lease: %w", recoverErr))
			}
			if !agentDrainAck && legacyMarker {
				if params.RolloutMode == rollout.Require {
					return park(errors.New("required drain acknowledgement lease recovery did not prove an agent acknowledgement"))
				}
				return exactSessionStartLegacyOwner, nil
			}
			if !agentDrainAck {
				return park(errors.New("drain acknowledgement provenance is not a confirmed legacy marker"))
			}
			drainAckLease = &recoveredLease
			return exactSessionStartKeyedOwner, nil
		}
		durableAgentProvenance := drainAckStopPendingFence.hasAgentProvenance(info.ID, strings.TrimSpace(info.InstanceToken))
		if !durableAgentProvenance {
			owner, recoverErr := recoverDrainAckLease()
			if recoverErr != nil || owner != exactSessionStartKeyedOwner {
				return owner, recoverErr
			}
		}
		// A durable legacy marker is still owned by legacy reconciliation in auto
		// mode. Only agent-proven exact STOP ownership must prove it can finish
		// with the fenced terminal close before liveness observation or STOP.
		if _, ok := beads.AtomicConditionalCloserFor(params.Store); !ok {
			return park(errors.New("drain acknowledgement atomic terminal closer is unavailable"))
		}
		if _, ok := params.Provider.(runtime.FreshLivenessObserver); !ok {
			switch params.RolloutMode {
			case rollout.Auto:
				if drainAckRollback == nil {
					return park(errors.New("agent drain acknowledgement cannot prove fresh liveness"))
				}
				if rollbackErr := drainAckRollback.restore(params.StatusWriter, info.ID); rollbackErr != nil {
					return park(fmt.Errorf("restoring drain acknowledgement without fresh liveness: %w", rollbackErr))
				}
				return exactSessionStartLegacyOwner, fmt.Errorf("%w: agent drain acknowledgement cannot prove fresh liveness", errSessionStartLegacyFallbackRequired)
			case rollout.Require:
				return park(errors.New("agent drain acknowledgement cannot prove fresh liveness"))
			}
		}
		name := strings.TrimSpace(info.SessionNameMetadata)
		token := strings.TrimSpace(info.InstanceToken)
		if name == "" || token == "" {
			return park(errors.New("drain acknowledgement stop lacks exact session identity"))
		}
		processNames := drainAckStopPendingProcessNames(params.Config, info)
		incarnationStartedAt := drainAckIncarnationStartedAt(info)
		liveness := runtime.ObserveFreshLiveness(params.Provider, runtime.LivenessTarget{
			SessionID:            info.ID,
			SessionName:          name,
			ProcessNames:         processNames,
			IncarnationStartedAt: incarnationStartedAt,
		})
		if !liveness.Running && !liveness.Alive {
			if !liveness.Complete {
				return park(errors.New("drain acknowledgement liveness observation is incomplete"))
			}
			if !durableAgentProvenance {
				return park(errors.New("drain acknowledgement stopped runtime lacks durable agent provenance"))
			}
			result := finalizeDrainAckStoppedSession(
				params.CityPath, params.Config, params.Store, params.RigStores, info,
				normalizedSessionTemplateInfo(info, params.Config), isPoolManagedSessionInfo(info),
				params.DrainOps, params.DrainTracker, clk, recorder, stderr, drainAckStopPendingFence,
			)
			if result.batch == nil && !result.closed && result.folded == nil && result.witnessInfo == nil {
				return park(fmt.Errorf("reconciling exact drain-ack stop %q: durable finalization made no progress", info.ID))
			}
			return exactSessionStartKeyedOwner, nil
		}
		if drainAckLease == nil && admission.PoolDrainAck != nil {
			drainAckLease = admission.PoolDrainAck
		}
		if drainAckLease == nil {
			owner, recoverErr := recoverDrainAckLease()
			if recoverErr != nil || owner != exactSessionStartKeyedOwner {
				return owner, recoverErr
			}
		}
		if params.AuthorizePoolDrainAck == nil {
			return park(errors.New("drain acknowledgement authorization is unavailable"))
		}
		if !durableAgentProvenance {
			if params.StatusWriterError != nil {
				return park(fmt.Errorf("resolving drain acknowledgement provenance writer: %w", params.StatusWriterError))
			}
			if params.StatusWriter == nil {
				return park(errors.New("drain acknowledgement provenance writer is unavailable"))
			}
			authorized, authorizeErr := params.AuthorizePoolDrainAck(info, *drainAckLease)
			if authorizeErr != nil || !authorized {
				if authorizeErr != nil {
					return park(fmt.Errorf("authorizing recovered drain acknowledgement before provenance write: %w", authorizeErr))
				}
				return park(errors.New("recovered drain acknowledgement authorization no longer holds before provenance write"))
			}
			provenance := sessionpkg.MetadataPatch{
				sessionpkg.DrainAckSourceMetadataKey:                 sessionpkg.DrainAckSourceAgentValue,
				sessionpkg.DrainAckRequesterSessionIDMetadataKey:     info.ID,
				sessionpkg.DrainAckRequesterInstanceTokenMetadataKey: info.InstanceToken,
			}
			expectedMetadata := maps.Clone(initialResponse.Metadata)
			for key, value := range provenance {
				expectedMetadata[key] = value
			}
			writeErr := params.StatusWriter.UpdateIfMatch(info.ID, drainAckStopPendingFence.revision, beads.UpdateOpts{Metadata: provenance})
			upgradedInfo, upgradedResponse, readErr := getAuthoritativeSessionStartPersistedRecord(params.Store, info.ID)
			if readErr != nil {
				return park(fmt.Errorf("re-reading recovered drain acknowledgement provenance: %w", readErr))
			}
			upgradedFence := newDrainAckStopPendingFence(upgradedResponse)
			if upgradedResponse.Revision == 0 || upgradedResponse.Revision == drainAckStopPendingFence.revision ||
				upgradedResponse.Status != initialResponse.Status || !maps.Equal(upgradedResponse.Metadata, expectedMetadata) ||
				!upgradedFence.matches(upgradedInfo, upgradedResponse, info.ID, info.InstanceToken) ||
				!upgradedFence.hasAgentProvenance(info.ID, info.InstanceToken) {
				if writeErr != nil {
					return park(fmt.Errorf("recording recovered drain acknowledgement provenance: %w", writeErr))
				}
				return park(errors.New("recovered drain acknowledgement provenance did not persist exactly"))
			}
			authorized, authorizeErr = params.AuthorizePoolDrainAck(upgradedInfo, *drainAckLease)
			if authorizeErr != nil || !authorized {
				if authorizeErr != nil {
					return park(fmt.Errorf("authorizing recovered drain acknowledgement after provenance write: %w", authorizeErr))
				}
				return park(errors.New("recovered drain acknowledgement authorization no longer holds after provenance write"))
			}
			info = upgradedInfo
			drainAckStopPendingFence = &upgradedFence
		}
		if params.AsyncStopTracker == nil {
			return park(errors.New("drain acknowledgement async stop tracker is unavailable"))
		}
		beforeStop := func() error {
			current, response, readErr := getAuthoritativeSessionStartPersistedRecord(params.Store, info.ID)
			if readErr != nil {
				return fmt.Errorf("re-reading drain acknowledgement before stop: %w", readErr)
			}
			if !drainAckStopPendingFence.matches(current, response, info.ID, drainAckLease.InstanceToken) {
				return errors.New("drain acknowledgement stop-pending row no longer matches the admitted lease")
			}
			if drainAckRollback != nil &&
				(!drainAckRollback.matches(current, response, info.ID, drainAckLease.InstanceToken, drainAckStopPendingPatch) || response.Revision != drainAckRollback.revision) {
				return errors.New("drain acknowledgement stop-pending rollback fence no longer matches")
			}
			authorized, authorizeErr := params.AuthorizePoolDrainAck(current, *drainAckLease)
			if authorizeErr == nil && authorized {
				return nil
			}
			cause := errors.New("drain acknowledgement authorization no longer holds before stop")
			if authorizeErr != nil {
				cause = fmt.Errorf("drain acknowledgement authorization before stop: %w", authorizeErr)
			}
			if params.RolloutMode == rollout.Require || drainAckRollback == nil {
				return cause
			}
			if rollbackErr := drainAckRollback.restore(params.StatusWriter, current.ID); rollbackErr != nil {
				return fmt.Errorf("%w; rollback: %w", cause, rollbackErr)
			}
			return errDrainAckAsyncStopYielded
		}
		queued := queueExactDrainAckAsyncStop(
			params.CityPath,
			params.Store,
			params.Provider,
			params.Config,
			info.ID,
			name,
			token,
			processNames,
			incarnationStartedAt,
			params.AsyncStopTracker,
			stderr,
			beforeStop,
			func(completion drainAckAsyncStopCompletion) {
				if params.AsyncStopCompletion != nil {
					params.AsyncStopCompletion(completion)
				}
			},
		)
		if queued && params.AsyncStopQueued != nil {
			params.AsyncStopQueued()
		}
		if queued || params.AsyncStopTracker.drainAckStopInFlight(drainAckAsyncStopKey(info.ID, name)) {
			return exactSessionStartKeyedOwner, errSessionStartPoolDrainAckPending
		}
		return park(errors.New("drain acknowledgement stop could not be queued"))
	}

	ownershipNow := clk.Now().UTC()
	lifecycle, cfgAgent, owner := classifyExactSessionStartOwnership(info, params.Config, ownershipNow)
	if admission.WaitDependency != nil && cfgAgent == nil {
		cfgAgent = findAgentByTemplate(params.Config, resolvedSessionTemplateInfo(info, params.Config))
	}
	if admission.WaitDependency != nil && cfgAgent != nil && waitDependencyConfiguredTemplateEligible(info, params.Config, params.Provider, params.CityName, params.Store, ownershipNow) &&
		!info.DependencyOnly && (!isNamedSessionInfo(info) || isCanonicalConfiguredNamedSessionForStart(info, params.Config)) {
		// A retained dependency-wait lease is the narrow proof that this otherwise
		// legacy sleeping session belongs to the keyed handoff.
		owner = exactSessionStartKeyedOwner
	}
	poolStartAuthorized := false
	if (owner == exactSessionStartLegacyOwner || owner == exactSessionStartUnowned) && admission.PoolAllocation != nil && params.AuthorizePoolStart != nil &&
		isPoolManagedSessionInfo(info) && !isNamedSessionInfo(info) {
		authorized, authorizeErr := params.AuthorizePoolStart(ctx, info, *admission.PoolAllocation)
		if authorizeErr != nil {
			return owner, fmt.Errorf("reconciling exact pool session start %q: authorizing allocation: %w", info.ID, authorizeErr)
		}
		if authorized {
			template := resolvedSessionTemplateInfo(info, params.Config)
			cfgAgent = findAgentByTemplate(params.Config, template)
			if cfgAgent == nil {
				return owner, fmt.Errorf("reconciling exact pool session start %q: authorized template %q is unavailable", info.ID, template)
			}
			owner = exactSessionStartKeyedOwner
			poolStartAuthorized = true
		}
	}
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
	if isAgentEffectivelySuspendedWith(params.Config, params.CityPath, cfgAgent, loadSuspensionStateBestEffort(params.CityPath)) {
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
	if admission.Source == sessionStartAdmissionInProcess || admission.Source == sessionStartAdmissionWaitDependency {
		if invalidator, ok := params.Provider.(runtime.LivenessInvalidator); ok {
			invalidator.InvalidateLiveness(info.SessionName)
		}
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
		Context:             exactSessionLifecycleStatusContextDesired,
		Observation:         observation,
		ObservedAt:          statusObservedAt,
		StartupTimeout:      params.Config.Session.StartupTimeoutDuration(),
		HealInputsRowBacked: exactSessionStatusHealInputsAreRowBacked(info, params.Config),
	})
	if (params.StatusWriter != nil || params.StatusWriterError != nil) &&
		statusResult != nil && statusResult.RuntimeLive && statusResult.Plan != nil && statusResult.Plan.Outcome == sessionLifecycleStatusHeal {
		if params.StatusWriterError != nil {
			return owner, fmt.Errorf("reconciling exact session start %q: resolving session-status writer: %w", info.ID, params.StatusWriterError)
		}
		plan := statusResult.Plan
		if statusResult.Context != exactSessionLifecycleStatusContextDesired ||
			statusResult.Disposition != exactSessionLifecycleStatusDispositionCandidate ||
			statusResult.RequestedID == "" || statusResult.RequestedID != statusResult.LoadedID ||
			statusResult.RequestedID != plan.SessionID || statusResult.LoadedRevision == 0 || len(plan.Patch) == 0 {
			return owner, fmt.Errorf("reconciling exact session start %q: malformed session-status heal candidate", info.ID)
		}
		if err := params.StatusWriter.UpdateIfMatch(statusResult.RequestedID, statusResult.LoadedRevision, beads.UpdateOpts{Metadata: plan.Patch}); err != nil {
			return owner, fmt.Errorf("reconciling exact session start %q: applying session-status heal: %w", info.ID, err)
		}
		statusResult.EffectApplied = true
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
	// The dependency wait itself is the wake reason, so the selection input
	// intentionally says ShouldWake even while wait_hold remains durable. Every
	// other ordinary start gate above still applies before the wait is claimed.
	if admission.WaitDependency != nil {
		return reconcileExactWaitDependencyStart(
			ctx, admission, params, info, initialResponse, startCandidate{info: info, tp: tp}, clk, recorder, stdout, stderr, startupTimeout, startOpts,
		)
	}
	if poolStartAuthorized && admission.PoolAllocation.RecoverActive {
		return reconcileExactPoolRecoveryStart(
			ctx,
			admission,
			params,
			startCandidate{info: info, tp: tp},
			clk,
			recorder,
			stdout,
			stderr,
			startupTimeout,
			startOpts,
		)
	}

	var preWakeRead func(beads.Store, string) (sessionpkg.Info, error)
	if poolStartAuthorized {
		lease := *admission.PoolAllocation
		preWakeRead = func(store beads.Store, id string) (sessionpkg.Info, error) {
			current, _, readErr := getAuthoritativeSessionStartRecord(store, id)
			if readErr != nil {
				return sessionpkg.Info{}, readErr
			}
			authorized, authorizeErr := params.AuthorizePoolStart(ctx, current, lease)
			if authorizeErr != nil {
				return sessionpkg.Info{}, authorizeErr
			}
			if !authorized {
				if lease.RecoverActive && params.RolloutMode == rollout.Require {
					return sessionpkg.Info{}, &exactSessionStartPreWakeSkip{owner: exactSessionStartUnowned}
				}
				return sessionpkg.Info{}, &exactSessionStartPreWakeSkip{owner: exactSessionStartLegacyOwner}
			}
			return current, nil
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
		preWakeRead,
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
	recordExactSessionStartCommit(params, admission, result)
	return owner, nil
}

// reconcileExactWaitDependencyStart owns the short, durable handoff from one
// satisfied wait to one provider start. Before the wait claim Auto may yield;
// once the claim commits every later uncertainty remains keyed.
func reconcileExactWaitDependencyStart(
	ctx context.Context,
	admission sessionStartAdmission,
	params exactSessionStartParams,
	info sessionpkg.Info,
	initial sessionpkg.PersistedResponse,
	candidate startCandidate,
	clk clock.Clock,
	recorder events.Recorder,
	stdout, stderr io.Writer,
	startupTimeout time.Duration,
	startOpts startExecutionOptions,
) (exactSessionStartOwner, error) {
	lease := *admission.WaitDependency
	if err := validateSessionWaitDependencyStartLease(lease); err != nil {
		return exactSessionStartKeyedOwner, fmt.Errorf("dependency wait start lease is invalid: %w", err)
	}
	if lease.ControllerGeneration != params.Generation {
		return exactSessionStartKeyedOwner, errors.New("dependency wait start lease belongs to a different controller generation")
	}
	preClaimFailure := func(cause error) (exactSessionStartOwner, error) {
		if params.RolloutMode == rollout.Auto {
			return exactSessionStartLegacyOwner, nil
		}
		return exactSessionStartKeyedOwner, cause
	}
	readStore := authoritativeSessionStartReadStore{Store: params.Store, live: beads.HandlesFor(params.Store).Live}
	wait, waitPersisted, err := sessionFrontDoor(readStore).GetWaitPersistedResponse(lease.WaitID)
	if err != nil {
		return preClaimFailure(fmt.Errorf("reading dependency wait before claim: %w", err))
	}
	registeredWait := wait
	registeredWait.State = waitStatePending
	registration, indexable, err := waitDependencyRegistrationFrom(registeredWait)
	if err != nil {
		return preClaimFailure(fmt.Errorf("canonicalizing dependency wait before claim: %w", err))
	}
	if wait.ID != lease.WaitID || !indexable || registration.sessionID != lease.SessionID || registration.depMode != lease.DepMode || !slices.Equal(registration.depIDs, lease.DepIDs) || wait.RegisteredEpoch != lease.RegisteredEpoch {
		return preClaimFailure(errors.New("dependency wait no longer matches leased pending revision"))
	}
	alreadyClaimed := wait.State == waitStateReady && wait.ReadyOwner == string(sessionpkg.WaitReadyOwnerDependency) && wait.ReadyOperation == lease.Operation
	if info.ID != lease.SessionID || info.Closed || initial.Revision != lease.SessionRevision {
		if alreadyClaimed {
			return exactSessionStartKeyedOwner, errors.New("dependency session changed after this operation claimed the wait")
		}
		return preClaimFailure(errors.New("dependency wait session no longer matches leased revision"))
	}
	if wait.ExpiresAt != "" {
		expiresAt, parseErr := time.Parse(time.RFC3339, wait.ExpiresAt)
		if parseErr != nil || !expiresAt.After(clk.Now().UTC()) {
			if alreadyClaimed {
				return exactSessionStartKeyedOwner, errors.New("dependency wait expired after this operation claimed it")
			}
			return preClaimFailure(errors.New("dependency wait expired before claim"))
		}
	}
	if !alreadyClaimed && (wait.State != waitStatePending || waitPersisted.Revision != lease.WaitRevision) {
		return preClaimFailure(errors.New("dependency wait no longer matches leased pending revision"))
	}
	ready, err := depsWaitReadyDetailedFrom(newAuthoritativeWaitDependencyStoreSet(params.Store, params.RigStores), wait)
	if err != nil || !ready {
		if alreadyClaimed {
			if err != nil {
				return exactSessionStartKeyedOwner, fmt.Errorf("rechecking dependency readiness after wait claim: %w", err)
			}
			return exactSessionStartKeyedOwner, errors.New("dependency readiness changed after wait claim")
		}
		if err != nil {
			return preClaimFailure(fmt.Errorf("rechecking dependency readiness: %w", err))
		}
		return preClaimFailure(errors.New("dependency wait is no longer ready"))
	}
	boundedPoolTarget, boundedPool := waitDependencyBoundedPoolTarget(info, params.Config)
	if boundedPool && (lease.PoolTarget != boundedPoolTarget || lease.PoolMembershipRevision == 0 ||
		params.ValidateWaitDependencyPoolWitness == nil || !params.ValidateWaitDependencyPoolWitness(info, lease)) {
		return preClaimFailure(errors.New("bounded-pool dependency wait witness changed before claim"))
	}
	if !boundedPool && lease.PoolTarget != "" {
		return preClaimFailure(errors.New("dependency wait retained a bounded-pool witness outside that cohort"))
	}
	waitFront := sessionFrontDoor(params.Store) // retain the original front door so its conditional writer remains reachable.
	if !alreadyClaimed {
		claim, claimErr := waitFront.ClaimPendingWaitReady(wait, waitPersisted, clk.Now().UTC(), sessionpkg.WaitReadyOwnerDependency, lease.Operation)
		if claim.Outcome == sessionpkg.WaitReadyClaimNotApplied {
			return preClaimFailure(claimErr)
		}
		if claimErr != nil || claim.Outcome != sessionpkg.WaitReadyClaimCommitted {
			if claimErr != nil {
				return exactSessionStartKeyedOwner, fmt.Errorf("claiming dependency wait %q: %w", lease.WaitID, claimErr)
			}
			return exactSessionStartKeyedOwner, fmt.Errorf("claiming dependency wait %q did not commit", lease.WaitID)
		}
	}
	if params.StatusWriterError != nil || params.StatusWriter == nil {
		if params.StatusWriterError != nil {
			return exactSessionStartKeyedOwner, fmt.Errorf("resolving dependency start conditional writer: %w", params.StatusWriterError)
		}
		return exactSessionStartKeyedOwner, errors.New("dependency start conditional writer is unavailable")
	}
	current, persisted, err := getAuthoritativeSessionStartPersistedRecord(params.Store, lease.SessionID)
	if err != nil {
		return exactSessionStartKeyedOwner, fmt.Errorf("reading dependency session before pre-wake: %w", err)
	}
	if current.ID != lease.SessionID || current.Closed || persisted.Status != "open" {
		return exactSessionStartKeyedOwner, errors.New("dependency session no longer matches leased revision after wait claim")
	}
	preWakeRecovered := alreadyClaimed && current.InstanceToken == lease.Operation && persisted.Metadata["pending_create_claim"] == "true"
	if !preWakeRecovered && persisted.Revision != lease.SessionRevision {
		return exactSessionStartKeyedOwner, errors.New("dependency session no longer matches leased revision after wait claim")
	}
	committed, committedPersisted := current, persisted
	if !preWakeRecovered {
		_, token, patch, err := buildPreWakePatchWithToken(current, clk, lease.Operation)
		if err != nil {
			return exactSessionStartKeyedOwner, fmt.Errorf("building dependency start pre-wake patch: %w", err)
		}
		if token != lease.Operation {
			return exactSessionStartKeyedOwner, errors.New("dependency start pre-wake token differs from durable wait operation")
		}
		patch["pending_create_claim"] = "true"
		expected := patch.Apply(persisted.Metadata)
		writeErr := params.StatusWriter.UpdateIfMatch(current.ID, persisted.Revision, beads.UpdateOpts{Metadata: patch})
		var readErr error
		committed, committedPersisted, readErr = getAuthoritativeSessionStartPersistedRecord(params.Store, current.ID)
		if readErr != nil {
			return exactSessionStartKeyedOwner, fmt.Errorf("re-reading dependency start pre-wake: %w", readErr)
		}
		if writeErr != nil || committed.ID != current.ID || committed.Closed || committedPersisted.Revision == persisted.Revision || !maps.Equal(committedPersisted.Metadata, expected) {
			if writeErr != nil {
				return exactSessionStartKeyedOwner, fmt.Errorf("committing dependency start pre-wake: %w", writeErr)
			}
			return exactSessionStartKeyedOwner, errors.New("dependency start pre-wake did not persist exactly")
		}
	}
	prepared, _, err := buildPreparedStartWithWorkDirResolver(startCandidate{info: committed, tp: candidate.tp}, params.CityPath, params.Config, params.Store, startOpts.workDirResolver)
	if err != nil {
		return exactSessionStartKeyedOwner, fmt.Errorf("preparing dependency start: %w", err)
	}
	authorize := func(context.Context) error {
		latest, latestPersisted, readErr := getAuthoritativeSessionStartPersistedRecord(params.Store, lease.SessionID)
		if readErr != nil || latest.ID != lease.SessionID || latest.Closed || latestPersisted.Revision != committedPersisted.Revision || latest.InstanceToken != lease.Operation {
			return errors.New("dependency start session changed before provider start")
		}
		if !waitDependencyConfiguredTemplateEligible(latest, params.Config, params.Provider, params.CityName, params.Store, clk.Now().UTC()) {
			return errors.New("configured dependency liveness changed before provider start")
		}
		boundedPoolTarget, boundedPool := waitDependencyBoundedPoolTarget(latest, params.Config)
		if boundedPool && (lease.PoolTarget != boundedPoolTarget || lease.PoolMembershipRevision == 0 ||
			params.ValidateWaitDependencyPoolWitness == nil || !params.ValidateWaitDependencyPoolWitness(latest, lease)) {
			return errors.New("bounded-pool dependency wait witness changed before provider start")
		}
		if !boundedPool && lease.PoolTarget != "" {
			return errors.New("dependency wait retained a bounded-pool witness outside that cohort")
		}
		liveWait, _, waitErr := waitFront.GetWaitPersistedResponse(lease.WaitID)
		registeredLiveWait := liveWait
		registeredLiveWait.State = waitStatePending
		registration, indexable, registrationErr := waitDependencyRegistrationFrom(registeredLiveWait)
		if waitErr != nil || registrationErr != nil || liveWait.ID != lease.WaitID || !indexable || registration.sessionID != lease.SessionID || registration.depMode != lease.DepMode || !slices.Equal(registration.depIDs, lease.DepIDs) || liveWait.State != waitStateReady || liveWait.ReadyOwner != string(sessionpkg.WaitReadyOwnerDependency) || liveWait.ReadyOperation != lease.Operation || liveWait.RegisteredEpoch != lease.RegisteredEpoch {
			return errors.New("dependency wait changed before provider start")
		}
		ready, depErr := depsWaitReadyDetailedFrom(newAuthoritativeWaitDependencyStoreSet(params.Store, params.RigStores), liveWait)
		if depErr != nil || !ready {
			return errors.New("dependency readiness changed before provider start")
		}
		return nil
	}
	result := runPreparedStartCandidateAuthorized(ctx, *prepared, params.CityPath, params.Provider, params.Store, params.Config, startupTimeout, resolveStartStabilityWaiter(startOpts.stabilityWaiter), startOpts.sessionStaleKeyDetectionWaiter, authorize)
	disposition := commitStartResultWithFreshness(ctx, result, params.Provider, params.Store, clk, recorder, 0, stdout, stderr, nil)
	if disposition == startCommitSuperseded {
		return exactSessionStartKeyedOwner, nil
	}
	if disposition != startCommitCommitted {
		if result.err != nil {
			return exactSessionStartKeyedOwner, fmt.Errorf("reconciling dependency wait start %q: %w", lease.SessionID, result.err)
		}
		return exactSessionStartKeyedOwner, fmt.Errorf("reconciling dependency wait start %q: start result did not commit", lease.SessionID)
	}
	recordExactSessionStartCommit(params, admission, result)
	return exactSessionStartKeyedOwner, nil
}

func reconcileExactPoolRecoveryStart(
	ctx context.Context,
	admission sessionStartAdmission,
	params exactSessionStartParams,
	candidate startCandidate,
	clk clock.Clock,
	recorder events.Recorder,
	stdout, stderr io.Writer,
	startupTimeout time.Duration,
	startOpts startExecutionOptions,
) (exactSessionStartOwner, error) {
	lease := *admission.PoolAllocation
	fail := func(cause error) (exactSessionStartOwner, error) {
		if params.RolloutMode == rollout.Require {
			return exactSessionStartKeyedOwner, fmt.Errorf("required exact pool recovery parked: %w", cause)
		}
		return exactSessionStartLegacyOwner, fmt.Errorf("%w: exact pool recovery yielded: %w", errSessionStartLegacyFallbackRequired, cause)
	}
	if params.StatusWriterError != nil {
		return fail(fmt.Errorf("resolving recovery conditional writer: %w", params.StatusWriterError))
	}
	if params.StatusWriter == nil {
		return fail(errors.New("recovery conditional writer is unavailable"))
	}

	current, before, err := getAuthoritativeSessionStartPersistedRecord(params.Store, candidate.info.ID)
	if err != nil {
		return fail(fmt.Errorf("reading exact recovery row before pre-wake: %w", err))
	}
	if current.ID != lease.SessionID || before.Status != "open" || before.Revision != lease.SessionRevision {
		return fail(errors.New("exact recovery row no longer matches its leased revision"))
	}
	authorized, err := params.AuthorizePoolStart(ctx, current, lease)
	if err != nil {
		return fail(fmt.Errorf("authorizing exact recovery before pre-wake: %w", err))
	}
	if !authorized {
		return fail(errors.New("exact recovery authority no longer holds before pre-wake"))
	}

	_, token, patch, err := buildPreWakePatch(current, clk)
	if err != nil {
		return fail(fmt.Errorf("building exact recovery pre-wake patch: %w", err))
	}
	rollbackPatch := make(sessionpkg.MetadataPatch, len(patch))
	for key := range patch {
		rollbackPatch[key] = before.Metadata[key]
	}
	expectedMetadata := patch.Apply(before.Metadata)
	writeErr := params.StatusWriter.UpdateIfMatch(current.ID, before.Revision, beads.UpdateOpts{Metadata: patch})
	committedInfo, committed, readErr := getAuthoritativeSessionStartPersistedRecord(params.Store, current.ID)
	if readErr != nil {
		cause := fmt.Errorf("re-reading exact recovery pre-wake commit: %w", readErr)
		if writeErr != nil {
			cause = fmt.Errorf("%w; conditional write: %w", cause, writeErr)
		}
		return fail(cause)
	}
	if committedInfo.ID != current.ID || committedInfo.Closed || committed.Status != before.Status ||
		committed.Revision == 0 || committed.Revision == before.Revision || !maps.Equal(committed.Metadata, expectedMetadata) {
		if writeErr != nil {
			return fail(fmt.Errorf("committing exact recovery pre-wake metadata: %w", writeErr))
		}
		return fail(errors.New("exact recovery pre-wake metadata did not persist exactly"))
	}
	freshWake := current.WakeMode == "fresh" || pendingContinuationResetNeedsFreshStart(current)
	traceFreshWakeMetadataReset(current.SessionNameMetadata, freshWakeResetPriorValues(current), patch, freshWake)

	rollback := func(cause error) (exactSessionStartOwner, error) {
		if rollbackErr := params.StatusWriter.UpdateIfMatch(current.ID, committed.Revision, beads.UpdateOpts{Metadata: rollbackPatch}); rollbackErr != nil {
			cause = fmt.Errorf("%w; fenced pre-wake restore: %w", cause, rollbackErr)
		}
		return fail(cause)
	}
	prepared, _, err := buildPreparedStartWithWorkDirResolver(
		startCandidate{info: committedInfo, tp: candidate.tp}, params.CityPath, params.Config, params.Store, startOpts.workDirResolver,
	)
	if err != nil {
		return rollback(fmt.Errorf("preparing exact recovery start: %w", err))
	}

	lease.InstanceToken = token
	lease.SessionRevision = committed.Revision
	lease.RecoveryPreWakeCommitted = true
	authorizeAtStart := func(effectCtx context.Context) error {
		latest, persisted, readErr := getAuthoritativeSessionStartPersistedRecord(params.Store, current.ID)
		if readErr != nil {
			return fmt.Errorf("%w: reading effect-boundary row: %w", errExactPoolRecoveryAuthorityLost, readErr)
		}
		if persisted.Revision != lease.SessionRevision || latest.ID != lease.SessionID || strings.TrimSpace(latest.InstanceToken) != lease.InstanceToken {
			return fmt.Errorf("%w: effect-boundary row no longer matches the post-CAS lease", errExactPoolRecoveryAuthorityLost)
		}
		authorized, authorizeErr := params.AuthorizePoolStart(effectCtx, latest, lease)
		if authorizeErr != nil {
			return fmt.Errorf("%w: effect-boundary authorization: %w", errExactPoolRecoveryAuthorityLost, authorizeErr)
		}
		if !authorized {
			return fmt.Errorf("%w: effect-boundary authorization no longer holds", errExactPoolRecoveryAuthorityLost)
		}
		return nil
	}
	result := runPreparedStartCandidateAuthorized(
		ctx,
		*prepared,
		params.CityPath,
		params.Provider,
		params.Store,
		params.Config,
		startupTimeout,
		resolveStartStabilityWaiter(startOpts.stabilityWaiter),
		startOpts.sessionStaleKeyDetectionWaiter,
		authorizeAtStart,
	)
	if errors.Is(result.err, errExactPoolRecoveryAuthorityLost) {
		return rollback(result.err)
	}
	disposition := commitStartResultWithFreshness(
		ctx, result, params.Provider, params.Store, clk, recorder, 0, stdout, stderr, nil,
	)
	if disposition == startCommitSuperseded {
		return exactSessionStartKeyedOwner, nil
	}
	if disposition != startCommitCommitted {
		if result.err != nil {
			return exactSessionStartKeyedOwner, fmt.Errorf("reconciling exact pool recovery %q: %w", current.ID, result.err)
		}
		return exactSessionStartKeyedOwner, fmt.Errorf("reconciling exact pool recovery %q: start result did not commit", current.ID)
	}
	recordExactSessionStartCommit(params, admission, result)
	return exactSessionStartKeyedOwner, nil
}

func recordExactSessionStartCommit(params exactSessionStartParams, admission sessionStartAdmission, result startResult) {
	if params.Trace == nil {
		return
	}
	info := result.prepared.candidate.info
	template := result.prepared.candidate.tp.TemplateName
	cycle := params.Trace.BeginCycle(TraceTickTriggerControl, "exact_session_start_commit", time.Now().UTC(), params.Config)
	if cycle == nil {
		return
	}
	if cycle.detailEnabled(template) {
		duration := result.finished.Sub(result.started)
		payload := result.phases.tracePayload(info.ID, duration)
		payload["admission"] = string(admission.Source)
		payload["admission_version"] = admission.Version
		payload["generation"] = params.Generation
		payload["instance_token"] = info.InstanceToken
		payload["effect_applied"] = true
		cycle.recordAdmittedDetailOperation(
			TraceSiteLifecycleStartCommit,
			TraceReasonStart,
			TraceOutcomeSuccess,
			"exact_session_start_commit",
			template,
			info.ID,
			info.SessionName,
			TraceSource(cycle.sourceFor(template)),
			duration,
			payload,
		)
	}
	if err := cycle.End(TraceCompletionCompleted, nil); err != nil && params.Stderr != nil {
		fmt.Fprintf(params.Stderr, "session reconciler: recording exact start commit trace: %v\n", err) //nolint:errcheck
	}
}

func drainAckIncarnationStartedAt(info sessionpkg.Info) time.Time {
	if wokeAt, ok := parseRFC3339Metadata(info.LastWokeAt); ok {
		return wokeAt
	}
	if awakeStartedAt, ok := parseRFC3339Metadata(info.AwakeStartedAt); ok {
		return awakeStartedAt
	}
	return time.Time{}
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

// exactSessionStatusHealInputsAreRowBacked reports whether the status-heal
// candidate's identity comes from revision-guarded row content. Labels persist
// separately in bd, while common_name and aliases require fallback resolution,
// so only an agent_name that resolves in the current config or a valid stored
// template can authorize a whole-row conditional update.
func exactSessionStatusHealInputsAreRowBacked(info sessionpkg.Info, cfg *config.City) bool {
	if info.Type != sessionpkg.BeadType {
		return false
	}
	if resolvedTemplateForIdentity(info.AgentName, cfg) != "" {
		return true
	}
	return findAgentByTemplate(cfg, info.Template) != nil
}

func resolveExactSessionStartOrDrainAckStopOwnership(
	info sessionpkg.Info,
	cfg *config.City,
	now time.Time,
) bool {
	return isDrainAckStopPendingInfo(info) || resolveExactSessionStartOwnership(info, cfg, now)
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
	if info.Closed {
		return lifecycle, nil, exactSessionStartUnowned
	}
	ownedCause := lifecycle.HasWakeCause(sessionpkg.WakeCausePendingCreate) ||
		lifecycle.HasWakeCause(sessionpkg.WakeCauseExplicit)
	if !ownedCause || lifecycle.Terminal {
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
