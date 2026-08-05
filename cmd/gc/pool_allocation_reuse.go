package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/config"
	"github.com/gastownhall/gascity/internal/runtime"
	sessionpkg "github.com/gastownhall/gascity/internal/session"
	"github.com/gastownhall/gascity/internal/worker"
)

const routedWorkPoolReuseNudgeSource = "routed-work-pool-reuse"

type routedWorkPoolReuseLease struct {
	SessionID            string
	InstanceToken        string
	PoolTarget           string
	PreviousWorkID       string
	PreviousSourceStore  string
	ControllerGeneration uint64
	MembershipRevision   uint64
	Binding              sessionpkg.TriggerBinding
}

func (cr *CityRuntime) reuseIdleRoutedWorkPoolSingleton(
	ctx context.Context,
	snapshot controllerSessionStartSnapshot,
	agent *config.Agent,
	work beads.Bead,
	hint routedWorkPoolAllocationHint,
	bp *agentBuildParams,
	request SessionRequest,
) (routedWorkPoolAllocationResult, error) {
	if strings.TrimSpace(agent.Nudge) == "" {
		return routedWorkPoolAllocationResult{}, nil
	}
	observation, sessionID, sole := cr.poolMembershipShadow.observeSoleMember(hint.PoolTarget)
	if !sole || observation.occupied != 1 {
		return routedWorkPoolAllocationResult{}, nil
	}
	info, persisted, err := getAuthoritativeSessionStartPersistedRecord(snapshot.Store, sessionID)
	if err != nil {
		return routedWorkPoolAllocationResult{}, fmt.Errorf("reading reusable singleton %q: %w", sessionID, err)
	}
	workDir := poolTriggerWorkDir(bp, agent, hint.PoolTarget, request)
	if strings.TrimSpace(workDir) == "" {
		return routedWorkPoolAllocationResult{}, nil
	}
	lease := routedWorkPoolReuseLease{
		SessionID:            info.ID,
		InstanceToken:        strings.TrimSpace(info.InstanceToken),
		PoolTarget:           strings.TrimSpace(hint.PoolTarget),
		PreviousWorkID:       strings.TrimSpace(info.TriggerBeadID),
		PreviousSourceStore:  strings.TrimSpace(info.TriggerBeadStoreRef),
		ControllerGeneration: snapshot.Generation,
		MembershipRevision:   observation.revision,
		Binding: sessionpkg.TriggerBinding{
			WorkID:         strings.TrimSpace(work.ID),
			StoreRef:       strings.TrimSpace(hint.SourceStore),
			BrainParentSID: strings.TrimSpace(request.BrainParentSID),
			Pack:           strings.TrimSpace(request.WorkPack),
			Workspace:      packWorkspaceSlug(request),
			WorkDir:        strings.TrimSpace(workDir),
		},
	}
	if err := validateRoutedWorkPoolReuseLease(lease); err != nil {
		return routedWorkPoolAllocationResult{}, nil
	}
	authorized, err := cr.authorizeRoutedWorkPoolReuse(snapshot, info, lease, false)
	if err != nil || !authorized {
		return routedWorkPoolAllocationResult{}, err
	}
	_, err = sessionFrontDoor(snapshot.Store).RebindTriggerIfMatch(info, persisted, lease.Binding)
	if err != nil {
		if beads.IsPreconditionFailed(err) {
			return routedWorkPoolAllocationResult{}, nil
		}
		return routedWorkPoolAllocationResult{}, err
	}
	current, _, err := getAuthoritativeSessionStartPersistedRecord(snapshot.Store, lease.SessionID)
	if err != nil {
		return routedWorkPoolAllocationResult{}, fmt.Errorf("rereading rebound singleton %q: %w", lease.SessionID, err)
	}
	authorized, err = cr.authorizeRoutedWorkPoolReuse(snapshot, current, lease, true)
	if err != nil {
		return routedWorkPoolAllocationResult{}, err
	}
	if !authorized {
		return routedWorkPoolAllocationResult{}, fmt.Errorf("rebound singleton %q lost authorization before nudge", lease.SessionID)
	}
	handle, err := workerHandleForSessionWithConfig(snapshot.CityPath, snapshot.Store, snapshot.Provider, snapshot.Config, lease.SessionID)
	if err != nil {
		return routedWorkPoolAllocationResult{}, fmt.Errorf("opening rebound singleton %q: %w", lease.SessionID, err)
	}
	nudge, err := handle.Nudge(ctx, worker.NudgeRequest{
		Text:     strings.TrimSpace(agent.Nudge),
		Delivery: worker.NudgeDeliveryWaitIdle,
		Source:   routedWorkPoolReuseNudgeSource,
		Wake:     worker.NudgeWakeLiveOnly,
	})
	if err != nil {
		return routedWorkPoolAllocationResult{}, fmt.Errorf("nudging rebound singleton %q: %w", lease.SessionID, err)
	}
	if !nudge.Delivered {
		return routedWorkPoolAllocationResult{}, fmt.Errorf("nudging rebound singleton %q: live idle delivery was not confirmed", lease.SessionID)
	}
	return routedWorkPoolAllocationResult{Session: current, Handled: true}, nil
}

func validateRoutedWorkPoolReuseLease(lease routedWorkPoolReuseLease) error {
	if lease.ControllerGeneration == 0 || lease.MembershipRevision == 0 {
		return fmt.Errorf("reuse generation and membership revision must be positive")
	}
	for _, field := range []struct {
		name  string
		value string
	}{
		{"session ID", lease.SessionID},
		{"instance token", lease.InstanceToken},
		{"pool target", lease.PoolTarget},
		{"work ID", lease.Binding.WorkID},
		{"source store", lease.Binding.StoreRef},
		{"work directory", lease.Binding.WorkDir},
	} {
		if field.value == "" || strings.TrimSpace(field.value) != field.value {
			return fmt.Errorf("reuse %s is not canonical", field.name)
		}
	}
	if (lease.PreviousWorkID == "") != (lease.PreviousSourceStore == "") {
		return fmt.Errorf("previous work ID and source store must both be set or empty")
	}
	return nil
}

// authorizeRoutedWorkPoolReuse repeats the full non-destructive reuse proof
// immediately before the fenced binding write and again before live delivery.
func (cr *CityRuntime) authorizeRoutedWorkPoolReuse(
	snapshot controllerSessionStartSnapshot,
	info sessionpkg.Info,
	lease routedWorkPoolReuseLease,
	bound bool,
) (bool, error) {
	if cr == nil || cr.cs == nil || cr.poolMembershipShadow == nil || snapshot.Config == nil || snapshot.Provider == nil || snapshot.Store == nil {
		return false, fmt.Errorf("authorizing singleton reuse: keyed state is unavailable")
	}
	if err := validateRoutedWorkPoolReuseLease(lease); err != nil {
		return false, err
	}
	cr.serviceStateMu.RLock()
	configCurrent := cr.cfg == snapshot.Config
	cr.serviceStateMu.RUnlock()
	if !configCurrent || snapshot.Generation != lease.ControllerGeneration || info.ID != lease.SessionID || info.Closed ||
		strings.TrimSpace(info.InstanceToken) != lease.InstanceToken ||
		normalizedSessionTemplateInfo(info, snapshot.Config) != lease.PoolTarget ||
		!isCanonicalPoolManagedSessionInfoForTemplate(info, lease.PoolTarget) || isNamedSessionInfo(info) {
		return false, nil
	}
	lifecycle := sessionpkg.ProjectLifecycle(sessionpkg.LifecycleInputFromInfo(info))
	if lifecycle.BaseState != sessionpkg.BaseStateActive || lifecycle.Terminal || !lifecycle.CountsAgainstCap {
		return false, nil
	}
	if bound {
		if !lease.Binding.Matches(info) {
			return false, nil
		}
	} else if strings.TrimSpace(info.TriggerBeadID) != lease.PreviousWorkID ||
		strings.TrimSpace(info.TriggerBeadStoreRef) != lease.PreviousSourceStore {
		return false, nil
	}
	agent := findAgentByTemplate(snapshot.Config, lease.PoolTarget)
	if agent == nil || !agent.UsesCanonicalSingletonPoolIdentity() || strings.TrimSpace(agent.Nudge) == "" ||
		isAgentEffectivelySuspendedWith(snapshot.Config, snapshot.CityPath, agent, loadSuspensionStateBestEffort(snapshot.CityPath)) {
		return false, nil
	}
	namedTemplates := make(map[string]struct{}, len(snapshot.Config.NamedSessions))
	for i := range snapshot.Config.NamedSessions {
		namedTemplates[snapshot.Config.NamedSessions[i].TemplateQualifiedName()] = struct{}{}
	}
	policy := newPoolAllocationShadowPolicy(snapshot.Config, agent, namedTemplates).
		forSourceStore(snapshot.Config, agent, snapshot.CityPath, lease.Binding.StoreRef)
	if !policy.supported() || policy.maxActiveSessions != 1 {
		return false, nil
	}
	observation, soleID, sole := cr.poolMembershipShadow.observeSoleMember(lease.PoolTarget)
	if !sole || soleID != lease.SessionID || observation.occupied != 1 || observation.revision < lease.MembershipRevision {
		return false, nil
	}
	name := strings.TrimSpace(info.SessionNameMetadata)
	if name == "" || !snapshot.Provider.IsRunning(name) || snapshot.Provider.IsAttached(name) {
		return false, nil
	}
	for _, check := range []struct{ key, want string }{
		{"GC_SESSION_ID", lease.SessionID},
		{"GC_INSTANCE_TOKEN", lease.InstanceToken},
	} {
		got, err := snapshot.Provider.GetMeta(name, check.key)
		if err != nil {
			return false, fmt.Errorf("authorizing singleton reuse for %q: reading %s: %w", lease.SessionID, check.key, err)
		}
		if got != check.want {
			return false, nil
		}
	}
	interactionProvider, ok := snapshot.Provider.(runtime.InteractionProvider)
	if !ok {
		return false, fmt.Errorf("authorizing singleton reuse for %q: provider cannot prove pending-interaction state", lease.SessionID)
	}
	pending, err := interactionProvider.Pending(name)
	if err != nil {
		return false, fmt.Errorf("authorizing singleton reuse for %q: checking pending interaction: %w", lease.SessionID, err)
	}
	if pending != nil {
		return false, nil
	}
	hasAssigned, err := sessionHasAwakeAssignedWorkForReachableStore(snapshot.CityPath, snapshot.Config, snapshot.Store, cr.rigBeadStores(), info)
	if err != nil {
		return false, fmt.Errorf("authorizing singleton reuse for %q: checking assigned work: %w", lease.SessionID, err)
	}
	if hasAssigned {
		return false, nil
	}
	if lease.PreviousWorkID != "" {
		previousStore, ok := cr.cs.routedWorkStore(snapshot.Config, lease.PreviousSourceStore)
		if !ok || previousStore == nil {
			return false, fmt.Errorf("authorizing singleton reuse for %q: previous source store %q is unavailable", lease.SessionID, lease.PreviousSourceStore)
		}
		previous, err := beads.HandlesFor(previousStore).Live.Get(lease.PreviousWorkID)
		if errors.Is(err, beads.ErrNotFound) {
			return false, nil
		}
		if err != nil {
			return false, fmt.Errorf("authorizing singleton reuse for %q: reading previous work %q: %w", lease.SessionID, lease.PreviousWorkID, err)
		}
		if previous.ID != lease.PreviousWorkID || previous.Status != "closed" {
			return false, nil
		}
	}
	sourceStore, ok := cr.cs.routedWorkStore(snapshot.Config, lease.Binding.StoreRef)
	if !ok || sourceStore == nil {
		return false, fmt.Errorf("authorizing singleton reuse for %q: source store %q is unavailable", lease.SessionID, lease.Binding.StoreRef)
	}
	work, ready, err := authoritativeReadyRoutedWorkByID(sourceStore, lease.Binding.WorkID, time.Now().UTC())
	if err != nil {
		return false, err
	}
	if !ready || controllerDemandRouteTarget(snapshot.Config, work, map[string]struct{}{lease.PoolTarget: {}}) != lease.PoolTarget {
		return false, nil
	}
	return true, nil
}
