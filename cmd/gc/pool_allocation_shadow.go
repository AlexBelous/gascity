package main

import (
	"strings"

	"github.com/gastownhall/gascity/internal/agentutil"
	"github.com/gastownhall/gascity/internal/config"
)

type poolAllocationShadowAction string

const (
	poolAllocationShadowLegacy   poolAllocationShadowAction = "legacy"
	poolAllocationShadowStartOne poolAllocationShadowAction = "start_one"
)

type poolAllocationShadowReason string

const (
	poolAllocationShadowEligible              poolAllocationShadowReason = "eligible_default_pool"
	poolAllocationShadowColdFromZero          poolAllocationShadowReason = "cold_from_zero"
	poolAllocationShadowInvalidConfig         poolAllocationShadowReason = "invalid_config"
	poolAllocationShadowSuspended             poolAllocationShadowReason = "suspended"
	poolAllocationShadowDisabled              poolAllocationShadowReason = "disabled"
	poolAllocationShadowCustomScaleCheck      poolAllocationShadowReason = "custom_scale_check"
	poolAllocationShadowNamedSession          poolAllocationShadowReason = "named_session"
	poolAllocationShadowMinFloor              poolAllocationShadowReason = "min_floor"
	poolAllocationShadowWorkspaceCap          poolAllocationShadowReason = "workspace_cap"
	poolAllocationShadowRigCap                poolAllocationShadowReason = "rig_cap"
	poolAllocationShadowNamepool              poolAllocationShadowReason = "namepool"
	poolAllocationShadowSingletonIdentity     poolAllocationShadowReason = "singleton_identity"
	poolAllocationShadowAgentCap              poolAllocationShadowReason = "agent_cap"
	poolAllocationShadowSourceStore           poolAllocationShadowReason = "source_store"
	poolAllocationShadowDemandUnsupported     poolAllocationShadowReason = "demand_unsupported"
	poolAllocationShadowMembershipUncertified poolAllocationShadowReason = "membership_uncertified"
	poolAllocationShadowInvalidMembership     poolAllocationShadowReason = "invalid_membership"
	poolAllocationShadowNonemptyPool          poolAllocationShadowReason = "nonempty_pool"
)

type poolAllocationShadowPolicy struct {
	reason              poolAllocationShadowReason
	contributionPresent bool
}

func (p poolAllocationShadowPolicy) supported() bool {
	return p.reason == poolAllocationShadowEligible
}

func newPoolAllocationShadowPolicy(
	cfg *config.City,
	agent *config.Agent,
	namedTemplates map[string]struct{},
) poolAllocationShadowPolicy {
	policy := poolAllocationShadowPolicy{
		reason:              poolAllocationShadowEligible,
		contributionPresent: true,
	}
	if cfg == nil || agent == nil {
		policy.reason = poolAllocationShadowInvalidConfig
		policy.contributionPresent = false
		return policy
	}
	if agent.Suspended {
		policy.reason = poolAllocationShadowSuspended
		policy.contributionPresent = false
		return policy
	}
	if !agent.SupportsGenericEphemeralSessions() {
		policy.reason = poolAllocationShadowDisabled
		policy.contributionPresent = false
		return policy
	}
	if strings.TrimSpace(agent.ScaleCheck) != "" {
		policy.reason = poolAllocationShadowCustomScaleCheck
		policy.contributionPresent = false
		return policy
	}
	if _, exists := namedTemplates[agent.QualifiedName()]; exists {
		policy.reason = poolAllocationShadowNamedSession
		policy.contributionPresent = false
		return policy
	}
	if agent.EffectiveMinActiveSessions() > 0 {
		policy.reason = poolAllocationShadowMinFloor
		return policy
	}
	if poolAllocationShadowHasCap(cfg.Workspace.MaxActiveSessions) {
		policy.reason = poolAllocationShadowWorkspaceCap
		return policy
	}
	for i := range cfg.Rigs {
		if cfg.Rigs[i].Name == agent.Dir && poolAllocationShadowHasCap(cfg.Rigs[i].MaxActiveSessions) {
			policy.reason = poolAllocationShadowRigCap
			return policy
		}
	}
	if strings.TrimSpace(agent.Namepool) != "" || len(agent.NamepoolNames) > 0 {
		policy.reason = poolAllocationShadowNamepool
		return policy
	}
	if agent.UsesCanonicalSingletonPoolIdentity() {
		policy.reason = poolAllocationShadowSingletonIdentity
		return policy
	}
	if poolAllocationShadowHasCap(agent.EffectiveMaxActiveSessions()) {
		policy.reason = poolAllocationShadowAgentCap
		return policy
	}
	return policy
}

func (p poolAllocationShadowPolicy) forSourceStore(
	cfg *config.City,
	agent *config.Agent,
	cityPath string,
	storeRef string,
) poolAllocationShadowPolicy {
	if !p.supported() {
		return p
	}
	if strings.TrimSpace(storeRef) == "" || !agentutil.AgentReachesWorkflowStore(storeRef, agent, cityPath, cfg) {
		p.reason = poolAllocationShadowSourceStore
	}
	return p
}

func poolAllocationShadowHasCap(limit *int) bool {
	return limit != nil && *limit >= 0
}

type poolAllocationShadowDecision struct {
	workID             string
	poolTarget         string
	action             poolAllocationShadowAction
	reason             poolAllocationShadowReason
	startCount         int
	membershipRevision uint64
}

func decideRoutedWorkPoolAllocationShadow(
	contribution readyRoutedWorkDemandContribution,
	membership poolMembershipObservation,
) poolAllocationShadowDecision {
	decision := poolAllocationShadowDecision{
		workID:             contribution.WorkID,
		poolTarget:         contribution.PoolTarget,
		action:             poolAllocationShadowLegacy,
		reason:             contribution.AllocationPolicy.reason,
		membershipRevision: membership.revision,
	}
	if !contribution.ContributionPresent {
		if decision.reason == poolAllocationShadowEligible || decision.reason == "" {
			decision.reason = poolAllocationShadowDemandUnsupported
		}
		return decision
	}
	if !contribution.AllocationPolicy.supported() {
		if decision.reason == "" {
			decision.reason = poolAllocationShadowInvalidConfig
		}
		return decision
	}
	if !membership.certified {
		decision.reason = poolAllocationShadowMembershipUncertified
		return decision
	}
	if membership.members < 0 || membership.occupied < 0 || membership.occupied > membership.members {
		decision.reason = poolAllocationShadowInvalidMembership
		return decision
	}
	if membership.members != 0 {
		decision.reason = poolAllocationShadowNonemptyPool
		return decision
	}
	decision.action = poolAllocationShadowStartOne
	decision.reason = poolAllocationShadowColdFromZero
	decision.startCount = 1
	return decision
}
