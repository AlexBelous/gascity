package main

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/gastownhall/gascity/internal/beadmeta"
	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/config"
	sessionpkg "github.com/gastownhall/gascity/internal/session"
)

const routedWorkPoolAllocationQueueSize = 256

type routedWorkPoolAllocationHint struct {
	WorkID      string
	PoolTarget  string
	SourceStore string
	EventAt     time.Time
	EnqueuedAt  time.Time
}

type routedWorkPoolAllocationResult struct {
	Session sessionpkg.Info
	Handled bool
	Created bool
}

// routedWorkPoolStartLease binds one exact-start admission to the certified
// allocation that created or rediscovered its durable session row.
type routedWorkPoolStartLease struct {
	SessionID            string
	InstanceToken        string
	ControllerGeneration uint64
	PoolTarget           string
	WorkID               string
	SourceStore          string
	MembershipRevision   uint64
}

func validateRoutedWorkPoolStartLease(lease routedWorkPoolStartLease) error {
	if err := validateSessionStartAdmission(lease.SessionID, sessionStartAdmissionInProcess); err != nil {
		return err
	}
	if lease.ControllerGeneration == 0 || lease.MembershipRevision == 0 {
		return fmt.Errorf("admitting pool allocation %q: generation and membership revision must be positive", lease.SessionID)
	}
	fields := []struct {
		name  string
		value string
	}{
		{name: "instance token", value: lease.InstanceToken},
		{name: "pool target", value: lease.PoolTarget},
		{name: "work ID", value: lease.WorkID},
		{name: "source store", value: lease.SourceStore},
	}
	for _, field := range fields {
		if field.value == "" || strings.TrimSpace(field.value) != field.value {
			return fmt.Errorf("admitting pool allocation %q: %s is not canonical", lease.SessionID, field.name)
		}
	}
	return nil
}

// authoritativeReadyRoutedWorkByID verifies one work bead and its blocking
// dependencies through the store's live handle. It never calls List or Ready.
func authoritativeReadyRoutedWorkByID(store beads.Store, id string, now time.Time) (beads.Bead, bool, error) {
	if store == nil {
		return beads.Bead{}, false, fmt.Errorf("reading routed work %q: store is nil", id)
	}
	id = strings.TrimSpace(id)
	if id == "" || now.IsZero() {
		return beads.Bead{}, false, fmt.Errorf("reading routed work: invalid id or observation time")
	}
	live := beads.HandlesFor(store).Live
	work, err := live.Get(id)
	if errors.Is(err, beads.ErrNotFound) {
		return beads.Bead{}, false, nil
	}
	if err != nil {
		return beads.Bead{}, false, fmt.Errorf("reading routed work %q: %w", id, err)
	}
	if work.ID != id {
		return beads.Bead{}, false, fmt.Errorf("reading routed work %q: store returned %q", id, work.ID)
	}
	if !beads.IsReadyCandidateForTier(work, now, beads.TierBoth) || strings.TrimSpace(work.Assignee) != "" {
		return beads.Bead{}, false, nil
	}
	if work.IsBlocked != nil && *work.IsBlocked {
		return beads.Bead{}, false, nil
	}
	deps, err := live.DepList(id, "down")
	if err != nil {
		return beads.Bead{}, false, fmt.Errorf("reading dependencies for routed work %q: %w", id, err)
	}
	seen := make(map[string]struct{}, len(deps))
	for _, dep := range deps {
		if !beads.IsReadyBlockingDependencyType(dep.Type) {
			continue
		}
		dependencyID := strings.TrimSpace(dep.DependsOnID)
		if dependencyID == "" {
			return beads.Bead{}, false, fmt.Errorf("reading dependencies for routed work %q: empty blocking dependency id", id)
		}
		if _, duplicate := seen[dependencyID]; duplicate {
			continue
		}
		seen[dependencyID] = struct{}{}
		dependency, err := live.Get(dependencyID)
		if err != nil {
			return beads.Bead{}, false, fmt.Errorf("reading dependency %q for routed work %q: %w", dependencyID, id, err)
		}
		if dependency.Status != "closed" {
			return beads.Bead{}, false, nil
		}
	}
	return work, true, nil
}

func (cr *CityRuntime) enqueueRoutedWorkPoolAllocation(contribution readyRoutedWorkDemandContribution) bool {
	if cr == nil || cr.routedWorkPoolAllocationCh == nil {
		return false
	}
	hint := routedWorkPoolAllocationHint{
		WorkID:      strings.TrimSpace(contribution.WorkID),
		PoolTarget:  strings.TrimSpace(contribution.PoolTarget),
		SourceStore: strings.TrimSpace(contribution.SourceStore),
		EventAt:     contribution.EventAt.UTC(),
		EnqueuedAt:  contribution.DecidedAt.UTC(),
	}
	if hint.WorkID == "" || hint.PoolTarget == "" || hint.SourceStore == "" {
		return false
	}
	if hint.EnqueuedAt.IsZero() {
		hint.EnqueuedAt = time.Now().UTC()
	}
	select {
	case cr.routedWorkPoolAllocationCh <- hint:
		return true
	default:
		return false
	}
}

func (cr *CityRuntime) handleRoutedWorkPoolAllocation(ctx context.Context, hint routedWorkPoolAllocationHint) {
	result, err := cr.reconcileRoutedWorkPoolAllocation(ctx, hint)
	if err != nil {
		fmt.Fprintf(cr.sessionStartStderr(), "%s: routed-work pool allocation for %s: %v; falling back to legacy reconciliation\n", cr.sessionStartLogPrefix(), hint.WorkID, err) //nolint:errcheck // fallback must remain visible
	}
	if err != nil || !result.Handled {
		cr.requestReadyRoutedWorkLegacyFallback()
	}
	if !result.Handled {
		return
	}
	if result.Created {
		cr.recordRoutedWorkPoolAllocationMaterialized(hint, result.Session)
	}
}

func (cr *CityRuntime) reconcileRoutedWorkPoolAllocation(ctx context.Context, hint routedWorkPoolAllocationHint) (routedWorkPoolAllocationResult, error) {
	if cr == nil || cr.cs == nil || cr.poolMembershipShadow == nil {
		return routedWorkPoolAllocationResult{}, fmt.Errorf("keyed allocation state is unavailable")
	}
	if ctx == nil {
		return routedWorkPoolAllocationResult{}, fmt.Errorf("allocation context is nil")
	}
	if err := ctx.Err(); err != nil {
		return routedWorkPoolAllocationResult{}, err
	}
	if cr.sessionStartOwnershipState() != sessionStartOwnershipKeyed {
		return routedWorkPoolAllocationResult{}, nil
	}
	snapshot, release, err := cr.cs.acquireSessionStartSnapshot()
	if err != nil {
		return routedWorkPoolAllocationResult{}, err
	}
	defer release()
	cr.serviceStateMu.RLock()
	configCurrent := cr.cfg == snapshot.Config
	cr.serviceStateMu.RUnlock()
	if !configCurrent {
		return routedWorkPoolAllocationResult{}, nil
	}
	sourceStore, ok := cr.cs.routedWorkStore(snapshot.Config, hint.SourceStore)
	if !ok || sourceStore == nil {
		return routedWorkPoolAllocationResult{}, fmt.Errorf("source store %q is unavailable", hint.SourceStore)
	}
	work, ready, err := authoritativeReadyRoutedWorkByID(sourceStore, hint.WorkID, time.Now().UTC())
	if err != nil {
		return routedWorkPoolAllocationResult{}, err
	}
	if !ready {
		return routedWorkPoolAllocationResult{}, nil
	}

	agent := findAgentByTemplate(snapshot.Config, hint.PoolTarget)
	if agent == nil || isAgentEffectivelySuspendedWith(snapshot.Config, agent, loadSuspensionStateBestEffort(snapshot.CityPath)) {
		return routedWorkPoolAllocationResult{}, nil
	}
	namedTemplates := make(map[string]struct{}, len(snapshot.Config.NamedSessions))
	for i := range snapshot.Config.NamedSessions {
		namedTemplates[snapshot.Config.NamedSessions[i].TemplateQualifiedName()] = struct{}{}
	}
	policy := newPoolAllocationShadowPolicy(snapshot.Config, agent, namedTemplates).
		forSourceStore(snapshot.Config, agent, snapshot.CityPath, hint.SourceStore)
	if target := controllerDemandRouteTarget(snapshot.Config, work, map[string]struct{}{hint.PoolTarget: {}}); target != hint.PoolTarget {
		return routedWorkPoolAllocationResult{}, nil
	}
	if !policy.supported() {
		return routedWorkPoolAllocationResult{}, nil
	}
	existing, found, findErr := findRoutedWorkPoolSession(snapshot.Store, snapshot.Config, hint)
	if findErr != nil {
		return routedWorkPoolAllocationResult{}, findErr
	}
	if found {
		if err := cr.poolMembershipShadow.replace(snapshot.Config, existing); err != nil {
			return routedWorkPoolAllocationResult{}, fmt.Errorf("publishing existing session membership: %w", err)
		}
		lifecycle := sessionpkg.ProjectLifecycle(sessionpkg.LifecycleInputFromInfo(existing))
		if lifecycle.HasWakeCause(sessionpkg.WakeCausePendingCreate) {
			lease, leaseErr := cr.newRoutedWorkPoolStartLease(snapshot, existing, hint)
			if leaseErr != nil {
				return routedWorkPoolAllocationResult{Session: existing, Handled: true}, leaseErr
			}
			if err := cr.admitRoutedWorkPoolSession(lease); err != nil {
				return routedWorkPoolAllocationResult{Session: existing, Handled: true}, err
			}
			return routedWorkPoolAllocationResult{Session: existing, Handled: true}, nil
		}
		_, occupied := cr.poolMembershipShadow.observeOccupiedMember(hint.PoolTarget, existing.ID)
		if lifecycle.BaseState == sessionpkg.BaseStateActive && occupied && existing.SessionName != "" && snapshot.Provider.IsRunning(existing.SessionName) {
			return routedWorkPoolAllocationResult{Session: existing, Handled: true}, nil
		}
		return routedWorkPoolAllocationResult{}, nil
	}
	decision := decideRoutedWorkPoolAllocationShadow(readyRoutedWorkDemandContribution{
		WorkID:              work.ID,
		PoolTarget:          hint.PoolTarget,
		SourceStore:         hint.SourceStore,
		ContributionPresent: policy.contributionPresent,
		AllocationPolicy:    policy,
	}, cr.poolMembershipShadow.observe(hint.PoolTarget))
	if decision.action != poolAllocationShadowStartOne || decision.poolSlot <= 0 {
		return routedWorkPoolAllocationResult{}, nil
	}
	if !routedWorkPoolProviderHealthy(snapshot.CityPath, snapshot.Config, agent) {
		return routedWorkPoolAllocationResult{}, nil
	}

	request := SessionRequest{
		Template:       hint.PoolTarget,
		BeadPriority:   beadPriority(work),
		Tier:           "new",
		WorkBeadID:     work.ID,
		WorkBeadTitle:  work.Title,
		WorkPack:       strings.TrimSpace(work.Metadata[beadmeta.PackMetadataKey]),
		WorkWorkspace:  strings.TrimSpace(work.Metadata[beadmeta.PackWorkspaceMetadataKey]),
		WorkStoreRef:   hint.SourceStore,
		BrainParentSID: strings.TrimSpace(work.Metadata[beadmeta.BrainParentSIDMetadataKey]),
	}
	bp := &agentBuildParams{
		city:      snapshot.Config,
		cityName:  snapshot.CityName,
		cityPath:  snapshot.CityPath,
		workspace: &snapshot.Config.Workspace,
		agents:    snapshot.Config.Agents,
		providers: snapshot.Config.Providers,
		lookPath:  exec.LookPath,
		sp:        snapshot.Provider,
		rigs:      snapshot.Config.Rigs,
		beadStore: snapshot.Store,
		stderr:    cr.sessionStartStderr(),
	}
	_, qualifiedInstance, poolSlot := poolDesiredRequestIdentity(agent, decision.poolSlot)
	metadata := poolTriggerMetadata(bp, agent, qualifiedInstance, request)
	info, err := createPoolSessionBeadWithGuardedAlias(bp, agent, hint.PoolTarget, qualifiedInstance, poolSlot, metadata)
	if err != nil {
		return routedWorkPoolAllocationResult{}, fmt.Errorf("creating one session for pool %q: %w", hint.PoolTarget, err)
	}
	result := routedWorkPoolAllocationResult{Session: info, Handled: true, Created: true}
	if err := cr.poolMembershipShadow.replace(snapshot.Config, info); err != nil {
		return result, fmt.Errorf("publishing created session membership: %w", err)
	}
	lease, err := cr.newRoutedWorkPoolStartLease(snapshot, info, hint)
	if err != nil {
		return result, err
	}
	if err := cr.admitRoutedWorkPoolSession(lease); err != nil {
		return result, err
	}
	return result, nil
}

func (cr *CityRuntime) newRoutedWorkPoolStartLease(
	snapshot controllerSessionStartSnapshot,
	info sessionpkg.Info,
	hint routedWorkPoolAllocationHint,
) (routedWorkPoolStartLease, error) {
	observation, occupied := cr.poolMembershipShadow.observeOccupiedMember(hint.PoolTarget, info.ID)
	if !occupied {
		return routedWorkPoolStartLease{}, fmt.Errorf("certifying created session %q: pool membership does not contain an occupied member", info.ID)
	}
	lease := routedWorkPoolStartLease{
		SessionID:            info.ID,
		InstanceToken:        strings.TrimSpace(info.InstanceToken),
		ControllerGeneration: snapshot.Generation,
		PoolTarget:           strings.TrimSpace(hint.PoolTarget),
		WorkID:               strings.TrimSpace(hint.WorkID),
		SourceStore:          strings.TrimSpace(hint.SourceStore),
		MembershipRevision:   observation.revision,
	}
	if err := validateRoutedWorkPoolStartLease(lease); err != nil {
		return routedWorkPoolStartLease{}, err
	}
	return lease, nil
}

func (cr *CityRuntime) authorizeRoutedWorkPoolStart(
	ctx context.Context,
	snapshot controllerSessionStartSnapshot,
	info sessionpkg.Info,
	lease routedWorkPoolStartLease,
) (bool, error) {
	if cr == nil || cr.cs == nil || cr.poolMembershipShadow == nil || snapshot.Config == nil {
		return false, fmt.Errorf("authorizing pool allocation start: keyed state is unavailable")
	}
	if ctx == nil {
		return false, fmt.Errorf("authorizing pool allocation start: context is nil")
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if err := validateRoutedWorkPoolStartLease(lease); err != nil {
		return false, err
	}
	if snapshot.Generation != lease.ControllerGeneration || info.ID != lease.SessionID ||
		strings.TrimSpace(info.InstanceToken) != lease.InstanceToken || info.Closed ||
		!isPoolManagedSessionInfo(info) || isNamedSessionInfo(info) {
		return false, nil
	}
	cr.serviceStateMu.RLock()
	configCurrent := cr.cfg == snapshot.Config
	cr.serviceStateMu.RUnlock()
	if !configCurrent {
		return false, nil
	}

	lifecycleInput := sessionpkg.LifecycleInputFromInfo(info)
	lifecycleInput.Now = time.Now().UTC()
	lifecycleInput.CreatedAt = info.CreatedAt
	lifecycleInput.StaleCreatingAfter = staleCreatingStateTimeout
	lifecycle := sessionpkg.ProjectLifecycle(lifecycleInput)
	if lifecycle.Terminal || !lifecycle.HasWakeCause(sessionpkg.WakeCausePendingCreate) {
		return false, nil
	}
	agent := findAgentByTemplate(snapshot.Config, lease.PoolTarget)
	if agent == nil || normalizedSessionTemplateInfo(info, snapshot.Config) != lease.PoolTarget ||
		isAgentEffectivelySuspendedWith(snapshot.Config, agent, loadSuspensionStateBestEffort(snapshot.CityPath)) {
		return false, nil
	}
	namedTemplates := make(map[string]struct{}, len(snapshot.Config.NamedSessions))
	for i := range snapshot.Config.NamedSessions {
		namedTemplates[snapshot.Config.NamedSessions[i].TemplateQualifiedName()] = struct{}{}
	}
	policy := newPoolAllocationShadowPolicy(snapshot.Config, agent, namedTemplates).
		forSourceStore(snapshot.Config, agent, snapshot.CityPath, lease.SourceStore)
	if !policy.supported() || strings.TrimSpace(info.TriggerBeadID) != lease.WorkID ||
		strings.TrimSpace(info.TriggerBeadStoreRef) != lease.SourceStore {
		return false, nil
	}
	sourceStore, ok := cr.cs.routedWorkStore(snapshot.Config, lease.SourceStore)
	if !ok || sourceStore == nil {
		return false, fmt.Errorf("authorizing pool allocation start: source store %q is unavailable", lease.SourceStore)
	}
	work, ready, err := authoritativeReadyRoutedWorkByID(sourceStore, lease.WorkID, time.Now().UTC())
	if err != nil {
		return false, err
	}
	if !ready || controllerDemandRouteTarget(snapshot.Config, work, map[string]struct{}{lease.PoolTarget: {}}) != lease.PoolTarget {
		return false, nil
	}
	observation, occupied := cr.poolMembershipShadow.observeOccupiedMember(lease.PoolTarget, lease.SessionID)
	if !occupied || observation.revision < lease.MembershipRevision || !routedWorkPoolProviderHealthy(snapshot.CityPath, snapshot.Config, agent) {
		return false, nil
	}
	return true, nil
}

func (cs *controllerState) routedWorkStore(cfg *config.City, sourceStore string) (beads.Store, bool) {
	if cs == nil || cfg == nil {
		return nil, false
	}
	sourceStore = strings.TrimSpace(sourceStore)
	cs.mu.RLock()
	defer cs.mu.RUnlock()
	if cs.cfg != cfg {
		return nil, false
	}
	cityName := loadedCityName(cfg, cs.cityPath)
	if workflowStoreRefForDir(cs.cityPath, cs.cityPath, cityName, cfg) == sourceStore {
		return cs.cityBeadStore, cs.cityBeadStore != nil
	}
	for i := range cfg.Rigs {
		rig := &cfg.Rigs[i]
		rigPath := rig.Path
		if !filepath.IsAbs(rigPath) {
			rigPath = filepath.Join(cs.cityPath, rigPath)
		}
		if workflowStoreRefForDir(rigPath, cs.cityPath, cityName, cfg) == sourceStore {
			store := cs.beadStores[rig.Name]
			return store, store != nil
		}
	}
	return nil, false
}

func findRoutedWorkPoolSession(store beads.Store, cfg *config.City, hint routedWorkPoolAllocationHint) (sessionpkg.Info, bool, error) {
	rows, err := beads.HandlesFor(store).Live.List(beads.ListQuery{
		Metadata: map[string]string{
			beadmeta.TriggerBeadIDMetadataKey:       hint.WorkID,
			beadmeta.TriggerBeadStoreRefMetadataKey: hint.SourceStore,
		},
		Sort:     beads.SortCreatedAsc,
		TierMode: beads.TierBoth,
	})
	if err != nil {
		return sessionpkg.Info{}, false, fmt.Errorf("finding existing routed-work pool session: %w", err)
	}
	var found sessionpkg.Info
	for _, row := range rows {
		if row.Status != "closed" && isPoolManagedSessionBead(row) && normalizedSessionTemplate(row, cfg) == hint.PoolTarget {
			info, err := sessionFrontDoor(store).Get(row.ID)
			if err != nil {
				return sessionpkg.Info{}, false, fmt.Errorf("projecting existing routed-work pool session %q: %w", row.ID, err)
			}
			if found.ID != "" {
				return sessionpkg.Info{}, false, fmt.Errorf("ambiguous routed-work pool sessions %q and %q", found.ID, info.ID)
			}
			found = info
		}
	}
	return found, found.ID != "", nil
}

func routedWorkPoolProviderHealthy(cityPath string, cfg *config.City, agent *config.Agent) bool {
	providerName := strings.TrimSpace(agent.Provider)
	if providerName == "" {
		providerName = strings.TrimSpace(agent.InheritedProvider)
	}
	if providerName == "" && cfg != nil {
		providerName = strings.TrimSpace(cfg.Workspace.Provider)
	}
	healthy, present := loadProviderHealthSnapshot(cityPath).check(providerName)
	return !present || healthy
}

func (cr *CityRuntime) admitRoutedWorkPoolSession(lease routedWorkPoolStartLease) error {
	cr.sessionStartMu.Lock()
	controller := cr.sessionStartController
	owned := cr.sessionStartOwnership == sessionStartOwnershipKeyed
	cr.sessionStartMu.Unlock()
	if !owned || controller == nil {
		return fmt.Errorf("exact-start controller is unavailable after session creation")
	}
	outcome, err := controller.AdmitPoolAllocation(lease)
	if err != nil {
		controller.RequestAudit()
		return fmt.Errorf("admitting created session %q: %w", lease.SessionID, err)
	}
	if outcome == sessionStartAdmissionOverflow {
		controller.RequestAudit()
		return fmt.Errorf("admitting created session %q: exact-start queue overflow", lease.SessionID)
	}
	return nil
}

func (cr *CityRuntime) requestReadyRoutedWorkLegacyFallback() {
	if cr == nil {
		return
	}
	cr.readyRoutedWorkPokePending.Store(true)
	cr.requestLegacySessionStartFallback()
}

func (cr *CityRuntime) recordRoutedWorkPoolAllocationMaterialized(hint routedWorkPoolAllocationHint, info sessionpkg.Info) {
	if cr == nil || cr.trace == nil || info.ID == "" {
		return
	}
	now := time.Now().UTC()
	startedAt := hint.EnqueuedAt
	if startedAt.IsZero() || startedAt.After(now) {
		startedAt = now
	}
	cr.serviceStateMu.RLock()
	cfg := cr.cfg
	cr.serviceStateMu.RUnlock()
	cycle := cr.trace.BeginCycle(TraceTickTriggerControl, "pool_allocation.materialize", startedAt, cfg)
	if cycle == nil {
		return
	}
	eventTimestampValid := !hint.EventAt.IsZero() && !hint.EventAt.After(now)
	eventLatency := int64(0)
	if eventTimestampValid {
		eventLatency = now.Sub(hint.EventAt).Nanoseconds()
	}
	cycle.RecordControllerOperation(
		TraceSitePoolAllocationMaterialize,
		TraceReasonRetained,
		TraceOutcomeApplied,
		"pool_allocation.materialize",
		now.Sub(startedAt),
		map[string]any{
			"work_id":                     hint.WorkID,
			"pool_target":                 hint.PoolTarget,
			"source_store":                hint.SourceStore,
			"session_id":                  info.ID,
			"event_timestamp_valid":       eventTimestampValid,
			"event_to_materialization_ns": eventLatency,
			"queue_to_materialization_ns": now.Sub(startedAt).Nanoseconds(),
			"effect_owner":                "keyed",
			"effect_applied":              true,
		},
	)
	if err := cycle.End(TraceCompletionCompleted, nil); err != nil {
		fmt.Fprintf(cr.sessionStartStderr(), "%s: routed-work pool allocation trace: %v\n", cr.sessionStartLogPrefix(), err) //nolint:errcheck // tracing cannot affect allocation
	}
}
