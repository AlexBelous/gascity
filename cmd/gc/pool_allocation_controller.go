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
	"github.com/gastownhall/gascity/internal/runtime"
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

// routedWorkPoolDrainAckLease binds one exact drain acknowledgement to the
// durable pool member and terminal routed-work trigger it was observed for.
// It is deliberately separate from the start lease: stop admission has a
// different effect-time proof and must never inherit start authority.
type routedWorkPoolDrainAckLease struct {
	SessionID              string
	InstanceToken          string
	RequesterSessionID     string
	RequesterInstanceToken string
	ControllerGeneration   uint64
	PoolTarget             string
	WorkID                 string
	SourceStore            string
	MembershipRevision     uint64
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

func validateRoutedWorkPoolDrainAckLease(lease routedWorkPoolDrainAckLease) error {
	if err := validateSessionStartAdmission(lease.SessionID, sessionStartAdmissionInProcess); err != nil {
		return err
	}
	if lease.ControllerGeneration == 0 || lease.MembershipRevision == 0 {
		return fmt.Errorf("admitting pool drain acknowledgement %q: generation and membership revision must be positive", lease.SessionID)
	}
	fields := []struct {
		name  string
		value string
	}{
		{name: "instance token", value: lease.InstanceToken},
		{name: "requester session ID", value: lease.RequesterSessionID},
		{name: "requester instance token", value: lease.RequesterInstanceToken},
		{name: "pool target", value: lease.PoolTarget},
		{name: "work ID", value: lease.WorkID},
		{name: "source store", value: lease.SourceStore},
	}
	for _, field := range fields {
		if field.value == "" || strings.TrimSpace(field.value) != field.value {
			return fmt.Errorf("admitting pool drain acknowledgement %q: %s is not canonical", lease.SessionID, field.name)
		}
	}
	return nil
}

// newRoutedWorkPoolDrainAckLease accepts only the intentionally narrow subset
// of agent acknowledgements that the keyed pool controller can prove safe. A
// false result is not an error: its caller must leave the legacy reconciler as
// the only writer. Any failed observation is returned as an error so it cannot
// be mistaken for a clean "no work" or "no interaction" result.
func (cr *CityRuntime) newRoutedWorkPoolDrainAckLease(
	snapshot controllerSessionStartSnapshot,
	info sessionpkg.Info,
) (routedWorkPoolDrainAckLease, bool, error) {
	if cr == nil || cr.cs == nil || snapshot.Config == nil || snapshot.Provider == nil || snapshot.Store == nil {
		return routedWorkPoolDrainAckLease{}, false, fmt.Errorf("authorizing pool drain acknowledgement: keyed state is unavailable")
	}
	name := strings.TrimSpace(info.SessionNameMetadata)
	if name == "" {
		return routedWorkPoolDrainAckLease{}, false, nil
	}
	source, err := snapshot.Provider.GetMeta(name, reconcilerDrainAckSourceKey)
	if err != nil {
		if snapshot.Provider.IsRunning(name) {
			return routedWorkPoolDrainAckLease{}, true, fmt.Errorf("authorizing pool drain acknowledgement for %q: reading acknowledgement source: %w", info.ID, err)
		}
		return routedWorkPoolDrainAckLease{}, false, nil
	}
	if source != drainAckSourceAgentValue {
		return routedWorkPoolDrainAckLease{}, false, nil
	}
	requesterSessionID, err := snapshot.Provider.GetMeta(name, drainAckRequesterSessionIDKey)
	if err != nil {
		return routedWorkPoolDrainAckLease{}, true, fmt.Errorf("authorizing pool drain acknowledgement for %q: reading requester session ID: %w", info.ID, err)
	}
	requesterInstanceToken, err := snapshot.Provider.GetMeta(name, drainAckRequesterInstanceTokenKey)
	if err != nil {
		return routedWorkPoolDrainAckLease{}, true, fmt.Errorf("authorizing pool drain acknowledgement for %q: reading requester instance token: %w", info.ID, err)
	}
	if cr.poolMembershipShadow == nil {
		return routedWorkPoolDrainAckLease{}, true, fmt.Errorf("authorizing pool drain acknowledgement: keyed state is unavailable")
	}
	template := normalizedSessionTemplateInfo(info, snapshot.Config)
	observation, occupied := cr.poolMembershipShadow.observeOccupiedMember(template, info.ID)
	if !occupied {
		return routedWorkPoolDrainAckLease{}, true, nil
	}
	lease := routedWorkPoolDrainAckLease{
		SessionID:              info.ID,
		InstanceToken:          strings.TrimSpace(info.InstanceToken),
		RequesterSessionID:     strings.TrimSpace(requesterSessionID),
		RequesterInstanceToken: strings.TrimSpace(requesterInstanceToken),
		ControllerGeneration:   snapshot.Generation,
		PoolTarget:             template,
		WorkID:                 strings.TrimSpace(info.TriggerBeadID),
		SourceStore:            strings.TrimSpace(info.TriggerBeadStoreRef),
		MembershipRevision:     observation.revision,
	}
	if err := validateRoutedWorkPoolDrainAckLease(lease); err != nil {
		return routedWorkPoolDrainAckLease{}, true, nil
	}
	return lease, true, nil
}

// recoverRoutedWorkPoolDrainAckLease distinguishes a confirmed legacy marker
// from an unavailable or malformed provenance witness. Only the former may
// yield to legacy; unknown provenance remains parked with zero STOP effects.
func (cr *CityRuntime) recoverRoutedWorkPoolDrainAckLease(
	snapshot controllerSessionStartSnapshot,
	info sessionpkg.Info,
) (routedWorkPoolDrainAckLease, bool, bool, error) {
	lease, agentDrainAck, err := cr.newRoutedWorkPoolDrainAckLease(snapshot, info)
	if err != nil {
		return routedWorkPoolDrainAckLease{}, false, false, err
	}
	if agentDrainAck {
		if err := validateRoutedWorkPoolDrainAckLease(lease); err != nil {
			return routedWorkPoolDrainAckLease{}, false, false, fmt.Errorf("validating recovered drain acknowledgement lease: %w", err)
		}
		return lease, true, false, nil
	}
	name := strings.TrimSpace(info.SessionNameMetadata)
	if name == "" || snapshot.Provider == nil {
		return routedWorkPoolDrainAckLease{}, false, false, errors.New("drain acknowledgement provenance is unavailable")
	}
	source, sourceErr := snapshot.Provider.GetMeta(name, reconcilerDrainAckSourceKey)
	if sourceErr != nil {
		return routedWorkPoolDrainAckLease{}, false, false, fmt.Errorf("reading drain acknowledgement provenance: %w", sourceErr)
	}
	if source == reconcilerDrainAckSourceValue {
		return routedWorkPoolDrainAckLease{}, false, true, nil
	}
	return routedWorkPoolDrainAckLease{}, false, false, errors.New("drain acknowledgement provenance is not a confirmed legacy marker")
}

// authorizeRoutedWorkPoolDrainAck repeats every destructive precondition at
// the effect boundary. It intentionally uses live exact reads and strict
// provider probes rather than the legacy reconciler's best-effort snapshots.
func (cr *CityRuntime) authorizeRoutedWorkPoolDrainAck(
	snapshot controllerSessionStartSnapshot,
	info sessionpkg.Info,
	lease routedWorkPoolDrainAckLease,
) (bool, error) {
	if cr == nil || cr.cs == nil || cr.poolMembershipShadow == nil || snapshot.Config == nil || snapshot.Provider == nil || snapshot.Store == nil {
		return false, fmt.Errorf("authorizing pool drain acknowledgement: keyed state is unavailable")
	}
	if err := validateRoutedWorkPoolDrainAckLease(lease); err != nil {
		return false, err
	}
	cr.serviceStateMu.RLock()
	configCurrent := cr.cfg == snapshot.Config
	cr.serviceStateMu.RUnlock()
	if !configCurrent || snapshot.Generation != lease.ControllerGeneration || info.ID != lease.SessionID || info.Closed ||
		!isRoutedWorkPoolDrainAckLifecycleShape(info) || !isPoolManagedSessionInfo(info) || isNamedSessionInfo(info) ||
		lease.RequesterSessionID != info.ID || lease.RequesterInstanceToken != lease.InstanceToken ||
		strings.TrimSpace(info.InstanceToken) != lease.InstanceToken || normalizedSessionTemplateInfo(info, snapshot.Config) != lease.PoolTarget ||
		strings.TrimSpace(info.TriggerBeadID) != lease.WorkID || strings.TrimSpace(info.TriggerBeadStoreRef) != lease.SourceStore {
		return false, nil
	}
	name := strings.TrimSpace(info.SessionNameMetadata)
	if name == "" {
		return false, nil
	}
	agent := findAgentByTemplate(snapshot.Config, lease.PoolTarget)
	if agent == nil || isAgentEffectivelySuspendedWith(snapshot.Config, snapshot.CityPath, agent, loadSuspensionStateBestEffort(snapshot.CityPath)) {
		return false, nil
	}
	namedTemplates := make(map[string]struct{}, len(snapshot.Config.NamedSessions))
	for i := range snapshot.Config.NamedSessions {
		namedTemplates[snapshot.Config.NamedSessions[i].TemplateQualifiedName()] = struct{}{}
	}
	policy := newPoolAllocationShadowPolicy(snapshot.Config, agent, namedTemplates).
		forSourceStore(snapshot.Config, agent, snapshot.CityPath, lease.SourceStore)
	if !policy.supported() ||
		(policy.maxActiveSessions == 1 && !isCanonicalPoolManagedSessionInfoForTemplate(info, lease.PoolTarget)) {
		return false, nil
	}
	for _, check := range []struct{ key, want string }{
		{"GC_SESSION_ID", info.ID},
		{"GC_INSTANCE_TOKEN", lease.InstanceToken},
		{reconcilerDrainAckSourceKey, drainAckSourceAgentValue},
		{drainAckRequesterSessionIDKey, lease.RequesterSessionID},
		{drainAckRequesterInstanceTokenKey, lease.RequesterInstanceToken},
		{"GC_DRAIN_ACK", "1"},
	} {
		got, err := snapshot.Provider.GetMeta(name, check.key)
		if err != nil {
			return false, fmt.Errorf("authorizing pool drain acknowledgement for %q: reading %s: %w", info.ID, check.key, err)
		}
		if got != check.want {
			return false, nil
		}
	}
	interactionProvider, ok := snapshot.Provider.(runtime.InteractionProvider)
	if !ok {
		return false, fmt.Errorf("authorizing pool drain acknowledgement for %q: provider cannot prove pending-interaction state", info.ID)
	}
	pending, err := interactionProvider.Pending(name)
	if err != nil {
		return false, fmt.Errorf("authorizing pool drain acknowledgement for %q: checking pending interaction: %w", info.ID, err)
	}
	if pending != nil {
		return false, nil
	}
	sourceStore, ok := cr.cs.routedWorkStore(snapshot.Config, lease.SourceStore)
	if !ok || sourceStore == nil {
		return false, fmt.Errorf("authorizing pool drain acknowledgement for %q: source store %q is unavailable", info.ID, lease.SourceStore)
	}
	work, err := beads.HandlesFor(sourceStore).Live.Get(lease.WorkID)
	if err != nil {
		return false, fmt.Errorf("authorizing pool drain acknowledgement for %q: reading trigger work %q: %w", info.ID, lease.WorkID, err)
	}
	if work.ID != lease.WorkID || work.Status != "closed" {
		return false, nil
	}
	hasAssigned, err := sessionHasAwakeAssignedWorkForReachableStore(snapshot.CityPath, snapshot.Config, snapshot.Store, cr.rigBeadStores(), info)
	if err != nil {
		return false, fmt.Errorf("authorizing pool drain acknowledgement for %q: checking assigned work: %w", info.ID, err)
	}
	if hasAssigned {
		return false, nil
	}
	observation, occupied := cr.poolMembershipShadow.observeOccupiedMember(lease.PoolTarget, lease.SessionID)
	if !occupied || observation.revision < lease.MembershipRevision {
		return false, nil
	}
	return true, nil
}

func isRoutedWorkPoolDrainAckLifecycleShape(info sessionpkg.Info) bool {
	if isDrainAckStopPendingInfo(info) {
		return true
	}
	return strings.TrimSpace(info.MetadataState) == string(sessionpkg.StateActive)
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
	if agent == nil || isAgentEffectivelySuspendedWith(snapshot.Config, snapshot.CityPath, agent, loadSuspensionStateBestEffort(snapshot.CityPath)) {
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
	if policy.maxActiveSessions != 0 {
		reused, reuseDisposition, reuseErr := cr.reuseIdleRoutedWorkPoolMember(ctx, snapshot, agent, work, hint, bp, request)
		if reuseErr != nil {
			return routedWorkPoolAllocationResult{}, reuseErr
		}
		switch reuseDisposition {
		case routedWorkPoolReuseReusable:
			return reused, nil
		case routedWorkPoolReuseRefused:
			return routedWorkPoolAllocationResult{}, nil
		}
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
		isAgentEffectivelySuspendedWith(snapshot.Config, snapshot.CityPath, agent, loadSuspensionStateBestEffort(snapshot.CityPath)) {
		return false, nil
	}
	namedTemplates := make(map[string]struct{}, len(snapshot.Config.NamedSessions))
	for i := range snapshot.Config.NamedSessions {
		namedTemplates[snapshot.Config.NamedSessions[i].TemplateQualifiedName()] = struct{}{}
	}
	policy := newPoolAllocationShadowPolicy(snapshot.Config, agent, namedTemplates).
		forSourceStore(snapshot.Config, agent, snapshot.CityPath, lease.SourceStore)
	if !policy.supported() ||
		(policy.maxActiveSessions == 1 && !isCanonicalPoolManagedSessionInfoForTemplate(info, lease.PoolTarget)) ||
		strings.TrimSpace(info.TriggerBeadID) != lease.WorkID ||
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
	if !occupied || observation.revision < lease.MembershipRevision ||
		(policy.maxActiveSessions > 0 && observation.occupied > policy.maxActiveSessions) ||
		!routedWorkPoolProviderHealthy(snapshot.CityPath, snapshot.Config, agent) {
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
