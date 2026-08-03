package main

import (
	"fmt"
	"sort"
	"strings"

	"github.com/gastownhall/gascity/internal/agentutil"
	"github.com/gastownhall/gascity/internal/beadmeta"
	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/config"
	sessionpkg "github.com/gastownhall/gascity/internal/session"
	"github.com/gastownhall/gascity/internal/worktree"
)

// SessionRequest represents a single session the reconciler should start.
type SessionRequest struct {
	Template     string // agent template qualified name (e.g., "gascity/claude")
	BeadPriority int    // priority of the driving work bead
	// Tier is "resume" for in-progress work with a live session,
	// "wake-known-identity" for in-progress work whose session exited but
	// template is configured, or "new" for ready unassigned work.
	Tier          string
	SessionBeadID string // concrete session to preserve for resume or in-flight new demand
	WorkBeadID    string // the work bead driving this request
	WorkBeadTitle string // title of the work bead driving this request, when known
	WorkPack      string // pack route key from the work bead, when known
	WorkWorkspace string // explicit pack workspace route key from the work bead, when known
	WorkStoreRef  string // city or rig:<name> store reference for WorkBeadID when known
	// WorktreeSpec is the single-owner evidence published by the provisioner.
	// When present, the pool path verifies it before publishing work_dir on a
	// session bead; a mismatch fails the whole atomic metadata update.
	WorktreeSpec *worktree.Spec
	// WorktreeError carries incomplete/conflicting worktree evidence discovered
	// while building demand. The realization path fails closed before creating
	// or updating a session bead.
	WorktreeError string
	// UnmanagedDirect is the explicit compatibility boundary for a session
	// that already owns work outside the managed worktree provisioner. Only
	// these direct continuations may derive a legacy configured work directory
	// without WorktreeSpec; new routed pool demand must fail closed.
	UnmanagedDirect bool
	// BrainParentSID is gc.brain_parent_sid from the driving work bead, when
	// set: the parent session to fork this launch off of (warm-arm fork-launch).
	BrainParentSID string
	// FloorGuarantee marks a "new" request created to satisfy an agent's
	// min_active_sessions floor (as opposed to elastic scale-check demand).
	// The per-tick create-budget allocator reserves a token for each
	// floor-bearing template before round-robining the remainder, so a cold
	// pool's floor spawn cannot be starved by a warm pool's large elastic
	// demand (follow-up to #2893).
	FloorGuarantee bool
}

func beadPriority(b beads.Bead) int {
	if b.Priority != nil {
		return *b.Priority
	}
	return 0
}

// worktreeSpecForBead reconstructs the exact single-owner verification input
// from metadata published after gc worktree ensure succeeds. A work_dir with
// incomplete or conflicting evidence is an error, never permission to launch
// a session into an unverified directory.
func worktreeSpecForBead(bead beads.Bead, storeRef string) (*worktree.Spec, error) {
	canonicalPath := strings.TrimSpace(bead.Metadata[beadmeta.WorkDirMetadataKey])
	legacyPath := strings.TrimSpace(bead.Metadata[beadmeta.LegacyWorkDirMetadataKey])
	if canonicalPath != "" && legacyPath != "" && canonicalPath != legacyPath {
		return nil, fmt.Errorf("work bead %s has conflicting %s=%q and %s=%q",
			bead.ID, beadmeta.WorkDirMetadataKey, canonicalPath, beadmeta.LegacyWorkDirMetadataKey, legacyPath)
	}
	path := canonicalPath
	if path == "" {
		path = legacyPath
	}
	if path == "" {
		for _, key := range []string{
			beadmeta.WorkBranchMetadataKey,
			beadmeta.WorktreeRepoMetadataKey,
			beadmeta.WorktreeRootMetadataKey,
			beadmeta.WorktreeBaseRefMetadataKey,
			beadmeta.WorktreeBaseSHAMetadataKey,
			beadmeta.WorktreeCreatorMetadataKey,
			beadmeta.WorktreeOwnerMetadataKey,
			beadmeta.WorktreeGenerationMetadataKey,
			beadmeta.WorktreeLifecycleMetadataKey,
		} {
			if strings.TrimSpace(bead.Metadata[key]) != "" {
				return nil, fmt.Errorf("work bead %s has managed worktree evidence %s=%q but no %s or %s",
					bead.ID, key, bead.Metadata[key], beadmeta.WorkDirMetadataKey, beadmeta.LegacyWorkDirMetadataKey)
			}
		}
		return nil, nil
	}
	values := []struct {
		key   string
		value string
	}{
		{beadmeta.WorktreeRepoMetadataKey, bead.Metadata[beadmeta.WorktreeRepoMetadataKey]},
		{beadmeta.WorktreeRootMetadataKey, bead.Metadata[beadmeta.WorktreeRootMetadataKey]},
		{beadmeta.WorkBranchMetadataKey, bead.Metadata[beadmeta.WorkBranchMetadataKey]},
		{beadmeta.WorktreeBaseRefMetadataKey, bead.Metadata[beadmeta.WorktreeBaseRefMetadataKey]},
		{beadmeta.WorktreeBaseSHAMetadataKey, bead.Metadata[beadmeta.WorktreeBaseSHAMetadataKey]},
		{beadmeta.WorktreeCreatorMetadataKey, bead.Metadata[beadmeta.WorktreeCreatorMetadataKey]},
		{beadmeta.WorktreeOwnerMetadataKey, bead.Metadata[beadmeta.WorktreeOwnerMetadataKey]},
		{beadmeta.WorktreeGenerationMetadataKey, bead.Metadata[beadmeta.WorktreeGenerationMetadataKey]},
		{beadmeta.WorktreeLifecycleMetadataKey, bead.Metadata[beadmeta.WorktreeLifecycleMetadataKey]},
	}
	for _, item := range values {
		if strings.TrimSpace(item.value) == "" {
			return nil, fmt.Errorf("work bead %s has %s=%q but is missing %s",
				bead.ID, beadmeta.WorkDirMetadataKey, path, item.key)
		}
	}
	if strings.TrimSpace(storeRef) == "" {
		return nil, fmt.Errorf("work bead %s has %s=%q but no store reference", bead.ID, beadmeta.WorkDirMetadataKey, path)
	}
	return &worktree.Spec{
		RepoDir:    strings.TrimSpace(bead.Metadata[beadmeta.WorktreeRepoMetadataKey]),
		Root:       strings.TrimSpace(bead.Metadata[beadmeta.WorktreeRootMetadataKey]),
		Path:       path,
		Branch:     strings.TrimSpace(bead.Metadata[beadmeta.WorkBranchMetadataKey]),
		Base:       strings.TrimSpace(bead.Metadata[beadmeta.WorktreeBaseRefMetadataKey]),
		BaseSHA:    strings.TrimSpace(bead.Metadata[beadmeta.WorktreeBaseSHAMetadataKey]),
		BeadID:     strings.TrimSpace(bead.ID),
		StoreRef:   strings.TrimSpace(storeRef),
		Creator:    strings.TrimSpace(bead.Metadata[beadmeta.WorktreeCreatorMetadataKey]),
		Owner:      strings.TrimSpace(bead.Metadata[beadmeta.WorktreeOwnerMetadataKey]),
		Generation: strings.TrimSpace(bead.Metadata[beadmeta.WorktreeGenerationMetadataKey]),
		Lifecycle:  strings.TrimSpace(bead.Metadata[beadmeta.WorktreeLifecycleMetadataKey]),
	}, nil
}

func classifyWorktreeRequest(bead beads.Bead, storeRef, repoDir string) (*worktree.Spec, string, bool) {
	spec, err := worktreeSpecForBead(bead, storeRef)
	if err != nil {
		return nil, err.Error(), false
	}
	if spec != nil {
		return spec, "", false
	}
	if evidenceErr := unpublishedWorktreeOwnerEvidence(repoDir, bead.ID); evidenceErr != "" {
		return nil, evidenceErr, false
	}
	return nil, "", true
}

// PoolDesiredState holds the desired state for a single agent template.
type PoolDesiredState struct {
	Template string
	Requests []SessionRequest // accepted requests (within all caps)
}

// ReconcileDecision is the output of the nested cap enforcement.
type ReconcileDecision struct {
	Start []SessionRequest // sessions to start
	// Stop is computed by the reconciler by comparing Start against running sessions.
}

func PoolDesiredCounts(states []PoolDesiredState) map[string]int {
	if len(states) == 0 {
		return nil
	}
	counts := make(map[string]int, len(states))
	for _, state := range states {
		counts[state.Template] = len(state.Requests)
	}
	return counts
}

// ComputePoolDesiredStates computes the desired state for all pool agents.
// assignedWorkBeads contains actionable assigned work beads only: in-progress
// work and open work that was already proven ready upstream. Routed but
// unassigned pool queue work must not be passed here; new-session demand comes
// from scale_check, while this function only preserves sessions that already
// own actionable work.
// Each bead's gc.routed_to determines which agent template it belongs to.
// scaleCheckCounts maps agent template → new session demand from scale_check.
// Pass nil for either when unavailable.
func ComputePoolDesiredStates(
	cfg *config.City,
	assignedWorkBeads []beads.Bead,
	sessionInfos []sessionpkg.Info,
	scaleCheckCounts map[string]int,
) []PoolDesiredState {
	return computePoolDesiredStates(cfg, assignedWorkBeads, sessionInfos, scaleCheckCounts, nil, nil)
}

func ComputePoolDesiredStatesTraced(
	cfg *config.City,
	assignedWorkBeads []beads.Bead,
	sessionInfos []sessionpkg.Info,
	scaleCheckCounts map[string]int,
	trace *sessionReconcilerTraceCycle,
) []PoolDesiredState {
	return computePoolDesiredStates(cfg, assignedWorkBeads, sessionInfos, scaleCheckCounts, nil, trace)
}

func ComputePoolDesiredStatesWithDemandTraced(
	cfg *config.City,
	assignedWorkBeads []beads.Bead,
	sessionInfos []sessionpkg.Info,
	scaleCheckCounts map[string]int,
	scaleCheckDemand map[string]scaleCheckDemand,
	trace *sessionReconcilerTraceCycle,
) []PoolDesiredState {
	return computePoolDesiredStates(cfg, assignedWorkBeads, sessionInfos, scaleCheckCounts, scaleCheckDemand, trace)
}

func computePoolDesiredStates(
	cfg *config.City,
	assignedWorkBeads []beads.Bead,
	sessionInfos []sessionpkg.Info,
	scaleCheckCounts map[string]int,
	scaleCheckDemand map[string]scaleCheckDemand,
	trace *sessionReconcilerTraceCycle,
) []PoolDesiredState {
	// Build reverse lookup: any identifier → session bead ID.
	// Assignee on work beads may be a bead ID, session name, alias, or
	// a prior alias preserved in alias_history. Resume-tier dispatch
	// drops in-progress work whose owning session can't be resolved
	// from this map, so missing identities cause live sessions to look
	// orphaned and let a duplicate spawn for the same bead.
	assigneeToSessionBeadID := make(map[string]string)
	sessionBeadTemplate := make(map[string]string)
	namedSessionBeadIDs := make(map[string]bool)
	for _, sb := range sessionInfos {
		if sb.Closed {
			continue
		}
		if sessionHasProviderTerminalErrorInfo(sb) {
			continue
		}
		template := strings.TrimSpace(normalizedSessionTemplateInfo(sb, cfg))
		if template != "" {
			sessionBeadTemplate[sb.ID] = template
		}
		for _, id := range sessionBeadAssigneeIdentitiesInfo(sb) {
			assigneeToSessionBeadID[id] = sb.ID
		}
		if isNamedSessionInfo(sb) {
			namedSessionBeadIDs[sb.ID] = true
		}
	}

	aliasHeldTemplates := canonicalSingletonAliasHeldTemplates(cfg, sessionInfos)
	// Canonical-singleton residency (gc-0ychy): keep exactly one resident occupant
	// for a min-floored canonical singleton without minting a colliding
	// replacement. retentionResume names the owner to reuse/WAKE in place (added as
	// a resume request below); minFillSpawnSuppressed marks templates whose only
	// owner is draining or drained-but-not-closed (holds the alias, not reusable)
	// so the floor's min-fill spawn waits rather than mints. Scale-check demand is
	// never suppressed — queued control work still schedules a runtime-missing
	// restart.
	retentionResume, minFillSpawnSuppressed := canonicalSingletonResidency(cfg, sessionInfos)

	var resumeRequests []SessionRequest
	wakeRequestedTemplates := make(map[string]struct{})

	for i := range cfg.Agents {
		agent := &cfg.Agents[i]
		if agent.Suspended {
			continue
		}
		if !agent.SupportsGenericEphemeralSessions() {
			continue
		}
		template := agent.QualifiedName()

		// Resume tier: actionable assigned work beads whose assignee resolves
		// to a non-closed session bead. These sessions must stay alive.
		for _, wb := range assignedWorkBeads {
			routedTo := routedToOrLegacyWorkflowTarget(wb)
			if wb.Status != "in_progress" && wb.Status != "open" {
				continue
			}
			assignee := strings.TrimSpace(wb.Assignee)
			if assignee == "" {
				continue
			}
			sessionBeadID := assigneeToSessionBeadID[assignee]
			if routedTo == "" && sessionBeadID != "" {
				routedTo = sessionBeadTemplate[sessionBeadID]
				if routedTo == "" && len(cfg.Agents) == 1 {
					routedTo = cfg.Agents[0].QualifiedName()
				}
			}
			routedTo = normalizeAgentTemplateIdentity(cfg, agentutil.NormalizePoolRouteTarget(cfg, routedTo))
			if sessionBeadID != "" {
				sessionTemplate := strings.TrimSpace(sessionBeadTemplate[sessionBeadID])
				if sessionTemplate != "" && routedTo != "" && !agentTemplateIdentitiesEquivalent(cfg, routedTo, sessionTemplate) {
					continue
				}
			}
			if routedTo != template {
				continue
			}
			if sessionBeadID != "" {
				// Named-session beads are materialized by the named-session
				// loop in buildDesiredState, not by the pool path. Skipping
				// here prevents realizePoolDesiredSessions from renaming the
				// canonical named identity to a phantom "{name}-1" pool
				// instance — which would create two desired sessions for the
				// same agent even when max_active_sessions=1.
				if namedSessionBeadIDs[sessionBeadID] {
					continue
				}
				workStoreRef := strings.TrimSpace(wb.Metadata[beadmeta.RootStoreRefMetadataKey])
				worktreeSpec, worktreeError, unmanagedDirect := classifyWorktreeRequest(
					wb,
					workStoreRef,
					scaleCheckDemand[template].RepoDir,
				)
				resumeRequests = append(resumeRequests, SessionRequest{
					Template:        template,
					BeadPriority:    beadPriority(wb),
					Tier:            "resume",
					SessionBeadID:   sessionBeadID,
					WorkBeadID:      wb.ID,
					WorkBeadTitle:   strings.TrimSpace(wb.Title),
					WorkPack:        strings.TrimSpace(wb.Metadata[beadmeta.PackMetadataKey]),
					WorkWorkspace:   strings.TrimSpace(wb.Metadata[beadmeta.PackWorkspaceMetadataKey]),
					WorkStoreRef:    workStoreRef,
					WorktreeSpec:    worktreeSpec,
					WorktreeError:   worktreeError,
					BrainParentSID:  strings.TrimSpace(wb.Metadata[beadmeta.BrainParentSIDMetadataKey]),
					UnmanagedDirect: unmanagedDirect,
				})
				continue
			}
			if isConfiguredNamedSessionIdentity(cfg, assignee) {
				// A configured named session's own bare identity never
				// generates pool demand — namedWorkReady recovers it
				// instead (ga-i1d0tr Candidate B). Mirrors the resume
				// tier's namedSessionBeadIDs skip above, extended to the
				// case where no live session bead resolves the assignee
				// at all.
				continue
			}
			if !agentTemplateIdentitiesEquivalent(cfg, assignee, template) || !isKnownPoolTemplate(assignee, cfg) {
				// Assignee set but session closed/unknown and not a configured
				// pool template — orphaned work, not our job to respawn. The
				// identity-equivalence compare keeps work assigned under a
				// legacy bound form of this template eligible for the
				// wake-known-identity tier; the emitted request carries the
				// canonical template.
				continue
			}
			if _, ok := wakeRequestedTemplates[template]; ok {
				continue
			}
			wakeRequestedTemplates[template] = struct{}{}
			workStoreRef := strings.TrimSpace(wb.Metadata[beadmeta.RootStoreRefMetadataKey])
			worktreeSpec, worktreeError, unmanagedDirect := classifyWorktreeRequest(
				wb,
				workStoreRef,
				scaleCheckDemand[template].RepoDir,
			)
			resumeRequests = append(resumeRequests, SessionRequest{
				Template:        template,
				BeadPriority:    beadPriority(wb),
				Tier:            "wake-known-identity",
				WorkBeadID:      wb.ID,
				WorkBeadTitle:   strings.TrimSpace(wb.Title),
				WorkPack:        strings.TrimSpace(wb.Metadata[beadmeta.PackMetadataKey]),
				WorkWorkspace:   strings.TrimSpace(wb.Metadata[beadmeta.PackWorkspaceMetadataKey]),
				WorkStoreRef:    workStoreRef,
				WorktreeSpec:    worktreeSpec,
				WorktreeError:   worktreeError,
				BrainParentSID:  strings.TrimSpace(wb.Metadata[beadmeta.BrainParentSIDMetadataKey]),
				UnmanagedDirect: unmanagedDirect,
			})
			if trace != nil {
				trace.RecordDecision(TraceSitePoolWakeKnownIdentity, TraceReasonAssignedWork, TraceOutcomeScheduled, template, "", traceRecordPayload{
					"tier":      "wake-known-identity",
					"work_bead": wb.ID,
				})
			}
		}
	}

	// Residency reuse/wake: for each min-floored canonical singleton with a live
	// owner (active/awake/asleep/in-flight), resume the SAME bead so the floor is
	// satisfied in place — no colliding fresh generation (gc-0ychy). Iterate
	// cfg.Agents for determinism; isDuplicateSessionRequest drops this when the
	// owner already has a work-driven resume. The retained resume carries the full
	// routing context (occupant trigger metadata, else routed scale demand) so
	// reusing the occupant does not consume the singleton cap ahead of routed
	// demand and silently drop it, nor clear an in-flight bead's trigger metadata.
	sessionInfoByID := make(map[string]sessionpkg.Info, len(sessionInfos))
	for _, sb := range sessionInfos {
		sessionInfoByID[sb.ID] = sb
	}
	for i := range cfg.Agents {
		template := cfg.Agents[i].QualifiedName()
		beadID, ok := retentionResume[template]
		if !ok {
			continue
		}
		delete(retentionResume, template) // one resume per template
		req := SessionRequest{
			Template:      template,
			Tier:          "resume",
			SessionBeadID: beadID,
		}
		applyResidencyResumeContext(&req, sessionInfoByID[beadID], scaleCheckDemand[template])
		resumeRequests = append(resumeRequests, req)
	}

	limits := newNestedCapLimits(cfg)
	usage := acceptedNestedCapUsage(limits, resumeRequests)
	allRequests := append([]SessionRequest(nil), resumeRequests...)
	resumeSessionBeadIDs := make(map[string]struct{}, len(resumeRequests))
	for _, req := range resumeRequests {
		if req.SessionBeadID != "" {
			resumeSessionBeadIDs[req.SessionBeadID] = struct{}{}
		}
	}
	inFlightNewRequests := poolInFlightNewRequests(cfg, sessionInfos, resumeSessionBeadIDs)

	// Merge scale_check demand. In bead-backed reconciliation, scale_check is
	// the authoritative signal for new unassigned demand only; resume requests
	// are calculated independently from assigned work and must not be deducted
	// from that count. Pool-created sessions that have not claimed work yet
	// represent already-spent new demand, so they occupy the first new-demand
	// slots explicitly before anonymous creates are materialized.
	if len(scaleCheckCounts) > 0 {
		for i := range cfg.Agents {
			agent := &cfg.Agents[i]
			if agent.Suspended {
				continue
			}
			template := agent.QualifiedName()
			scaleCount, ok := scaleCheckCounts[template]
			if !ok {
				continue
			}
			if _, ok := aliasHeldTemplates[template]; ok {
				continue
			}
			newCount := capNewDemandCount(limits, usage, agent, scaleCount)
			recordNewDemandCapTrace(trace, template, agent, limits, usage, scaleCount, newCount)
			inFlight := inFlightNewRequests[template]
			inFlightCount := minInt(len(inFlight), newCount)
			if scaleCount > 0 && len(inFlight) > 0 && trace != nil {
				trace.RecordDecision(TraceSitePoolInFlightReuse, TraceReasonInFlightReuse, TraceOutcomeAccepted, template, "", traceRecordPayload{
					"scale_check":   scaleCount,
					"in_flight":     len(inFlight),
					"reused":        inFlightCount,
					"anonymous_new": newCount - inFlightCount,
				})
			}
			for j := 0; j < inFlightCount; j++ {
				req := classifyInFlightWorktreeRequest(inFlight[j], scaleCheckDemand[template])
				allRequests = append(allRequests, req)
				usage.accept(req, limits)
			}
			for j := inFlightCount; j < newCount; j++ {
				workBeadID := ""
				workBeadTitle := ""
				workPack := ""
				workWorkspace := ""
				workStoreRef := ""
				workParentSID := ""
				var worktreeSpec *worktree.Spec
				worktreeError := ""
				if demand := scaleCheckDemand[template]; len(demand.WorkBeadIDs) > j {
					workBeadID = strings.TrimSpace(demand.WorkBeadIDs[j])
					if demand.Titles != nil {
						workBeadTitle = strings.TrimSpace(demand.Titles[workBeadID])
					}
					if demand.Packs != nil {
						workPack = strings.TrimSpace(demand.Packs[workBeadID])
					}
					if demand.Workspaces != nil {
						workWorkspace = strings.TrimSpace(demand.Workspaces[workBeadID])
					}
					if demand.StoreRefs != nil {
						workStoreRef = strings.TrimSpace(demand.StoreRefs[workBeadID])
					}
					if demand.ParentSIDs != nil {
						workParentSID = strings.TrimSpace(demand.ParentSIDs[workBeadID])
					}
					if demand.WorktreeSpecs != nil {
						worktreeSpec = demand.WorktreeSpecs[workBeadID]
					}
					if demand.WorktreeErrors != nil {
						worktreeError = strings.TrimSpace(demand.WorktreeErrors[workBeadID])
					}
				}
				req := SessionRequest{
					Template:        template,
					Tier:            "new",
					WorkBeadID:      workBeadID,
					WorkBeadTitle:   workBeadTitle,
					WorkPack:        workPack,
					WorkWorkspace:   workWorkspace,
					WorkStoreRef:    workStoreRef,
					BrainParentSID:  workParentSID,
					WorktreeSpec:    worktreeSpec,
					WorktreeError:   worktreeError,
					UnmanagedDirect: workBeadID != "" && worktreeSpec == nil && worktreeError == "",
				}
				allRequests = append(allRequests, req)
				usage.accept(req, limits)
			}
		}
	}

	return applyNestedCaps(cfg, allRequests, aliasHeldTemplates, minFillSpawnSuppressed, trace)
}

// canonicalSingletonResidency computes how to keep exactly one resident occupant
// for each min-floored canonical-singleton pool agent (e.g. the deterministic
// control-dispatcher, floored by config.ApplyResidentAgentFloor via its
// resident=true opt-in) WITHOUT minting a colliding replacement (gc-0ychy). It
// returns:
//
//   - retentionResume[template] = bead ID of the owner to REUSE/WAKE in place (the
//     oldest active/awake/asleep owner). A resume request for it satisfies the min
//     floor via the SAME bead and canonical alias — no new generation. This also
//     wakes an asleep bead left by a completed drain, which would otherwise be
//     stranded.
//   - minFillSuppressed[template] set when the only owner is draining or
//     drained-but-not-closed: it still holds the alias/identity but cannot be
//     reused, so the floor's min-fill spawn must WAIT (not mint) until it closes.
//
// Identity ownership: an occupant OWNS the canonical identity until the bead is
// CLOSED (skipped here) or its create failed. A failed-create bead has released
// the identity (failedCreateIdentityReleased, internal/session/names.go), so it is
// ignored — a fresh spawn is allowed. A state=drained bead still owns the alias
// until close, so it suppresses (never "clean respawn while drained").
//
// Complements canonicalSingletonAliasHeldTemplates, which handles the
// non-pool-managed (named-singleton) alias holders it deliberately excludes.
func canonicalSingletonResidency(cfg *config.City, sessionInfos []sessionpkg.Info) (retentionResume map[string]string, minFillSuppressed map[string]struct{}) {
	retentionResume = make(map[string]string)
	minFillSuppressed = make(map[string]struct{})
	if cfg == nil {
		return retentionResume, minFillSuppressed
	}
	sorted := append([]sessionpkg.Info(nil), sessionInfos...)
	sortSessionInfosByCreatedAtThenID(sorted)
	for i := range cfg.Agents {
		agent := &cfg.Agents[i]
		if agent.Suspended || !agent.UsesCanonicalSingletonPoolIdentity() || agent.EffectiveMinActiveSessions() < 1 {
			continue
		}
		canonical := agent.QualifiedName()
		var reuseWakeOwner string
		var hasHeldOwner bool
		for _, sb := range sorted {
			if sb.Closed || !isPoolManagedSessionInfo(sb) || !infoIdentifiesAsCanonicalOwner(sb, agent, canonical) {
				continue
			}
			if isFailedCreateSessionInfo(sb) {
				continue // identity released -> allow a fresh spawn
			}
			if isDrainedSessionInfo(sb) {
				hasHeldOwner = true // still owns the alias until the bead closes
				continue
			}
			switch strings.TrimSpace(sb.MetadataState) {
			case "active", "awake", string(sessionpkg.StateCreating), string(sessionpkg.StateStartPending):
				// A live owner, or one still being created (creating/start-pending):
				// preserve it in place. Keeping the in-flight create desired via a
				// resume request stops the reconciler from rolling it back on cold
				// startup, and never mints a colliding fresh generation.
				if reuseWakeOwner == "" {
					reuseWakeOwner = sb.ID
				}
			case "asleep":
				// A sleeper's fate depends on WHY it slept, and residency reuse is
				// an ALLOWLIST — never a default. Only two sleeps are retained
				// resident lanes to reuse/WAKE in place:
				//   - city-stop: the city was stopped; the lane returns on resume;
				//   - no-wake-reason: a completed no-wake-reason drain of a
				//     resident serve loop (the gc-0ychy case), scoped to
				//     resident agents so a role-neutral pack resident opts in too.
				// Every other sleep (idle, idle-timeout, config-drift, user-hold,
				// wait-hold, rate-limit, provider-terminal-error, quarantine, ...) is
				// a deliberate or terminal park: do NOT mint a colliding replacement.
				// Hold the mint; the reconciler frees the slot and (once the bead
				// closes / releases the alias) a clean replacement spawns, or a
				// deliberately parked session stays parked. ComputeAwakeSet's own
				// wake paths (wait-ready, min-active city-stop revive) are unaffected.
				reason := strings.TrimSpace(sb.SleepReason)
				retainSleeper := reason == string(sessionpkg.SleepReasonCityStop) ||
					(reason == string(sessionpkg.SleepReasonNoWakeReason) && agent.IsResident())
				if retainSleeper {
					if reuseWakeOwner == "" {
						reuseWakeOwner = sb.ID
					}
				} else {
					hasHeldOwner = true
				}
			default:
				hasHeldOwner = true // draining, creating, stopped, ... — holds the alias, not reusable
			}
		}
		switch {
		case reuseWakeOwner != "":
			retentionResume[canonical] = reuseWakeOwner
		case hasHeldOwner:
			minFillSuppressed[canonical] = struct{}{}
		}
	}
	return retentionResume, minFillSuppressed
}

// applyResidencyResumeContext fills the routing context of a residency-retention
// resume so reusing a resident singleton in place preserves the work binding
// instead of dropping it (gc-0ychy). Reusing the occupant consumes the
// singleton's single cap slot ahead of routed scale demand, so a context-free
// resume would starve that demand and silently drop its WorkBeadID / pack /
// workspace / store / fork-parent; and for an in-flight (creating/start-pending)
// occupant it would clear the bead's own trigger metadata on bind. Precedence:
// the occupant's own trigger metadata (never cleared), else the first routed work
// in the template's scale demand; title/pack/workspace/store/parent are enriched
// from the demand keyed on the resolved work bead id (reads of a nil demand map
// return the zero value, so no routed demand simply leaves the context empty).
func applyResidencyResumeContext(req *SessionRequest, info sessionpkg.Info, demand scaleCheckDemand) {
	req.WorkStoreRef = strings.TrimSpace(info.TriggerBeadStoreRef)
	req.BrainParentSID = strings.TrimSpace(info.BrainParentSID)
	workBeadID := strings.TrimSpace(info.TriggerBeadID)
	if workBeadID == "" && len(demand.WorkBeadIDs) > 0 {
		workBeadID = strings.TrimSpace(demand.WorkBeadIDs[0])
	}
	if workBeadID == "" {
		return
	}
	req.WorkBeadID = workBeadID
	if title := strings.TrimSpace(demand.Titles[workBeadID]); title != "" {
		req.WorkBeadTitle = title
	}
	if pack := strings.TrimSpace(demand.Packs[workBeadID]); pack != "" {
		req.WorkPack = pack
	}
	if ws := strings.TrimSpace(demand.Workspaces[workBeadID]); ws != "" {
		req.WorkWorkspace = ws
	}
	if sr := strings.TrimSpace(demand.StoreRefs[workBeadID]); sr != "" {
		req.WorkStoreRef = sr
	}
	if parent := strings.TrimSpace(demand.ParentSIDs[workBeadID]); parent != "" {
		req.BrainParentSID = parent
	}
	// Carry the worktree ownership evidence the stable line requires on every
	// work-bound request: without it verifiedPoolTriggerWorkDir rejects the
	// chosen resume and realization proceeds without trigger environment
	// (gc-0ychy stable-line integration review HIGH-2).
	*req = classifyInFlightWorktreeRequest(*req, demand)
}

func canonicalSingletonAliasHeldTemplates(cfg *config.City, sessionInfos []sessionpkg.Info) map[string]struct{} {
	held := make(map[string]struct{})
	if cfg == nil {
		return held
	}
	for i := range cfg.Agents {
		agent := &cfg.Agents[i]
		if agent.Suspended || !agent.UsesCanonicalSingletonPoolIdentity() {
			continue
		}
		template := agent.QualifiedName()
		for _, sb := range sessionInfos {
			// None of these own the canonical alias: a closed or drained named
			// session released it at close via the retire path, a pool-managed bead
			// never held it, and a failed-create bead released it via
			// failedCreateIdentityReleased (names.go). Counting any as a live holder
			// would suppress demand while the alias is actually free, hanging routed
			// work.
			if sb.Closed || isPoolManagedSessionInfo(sb) || isDrainedSessionInfo(sb) || isFailedCreateSessionInfo(sb) {
				continue
			}
			if strings.TrimSpace(sb.Alias) == template {
				held[template] = struct{}{}
				break
			}
			// A named session's Alias holds its own configured identity, not
			// the backing template (build_desired_state.go sets tp.Alias =
			// identity for every named session, e.g. "primary" bound to
			// template "worker"). When identity != template, the Alias check
			// above never matches even though this bead is the singleton
			// slot's sole occupant. Its Template field is always the backing
			// template's qualified name, so use that as the named-session
			// match instead of Alias.
			if isNamedSessionInfo(sb) && strings.TrimSpace(sb.Template) == template {
				held[template] = struct{}{}
				break
			}
		}
	}
	return held
}

func poolInFlightNewRequests(cfg *config.City, sessionInfos []sessionpkg.Info, resumeSessionBeadIDs map[string]struct{}) map[string][]SessionRequest {
	requests := make(map[string][]SessionRequest)
	sortedSessionInfos := append([]sessionpkg.Info(nil), sessionInfos...)
	sort.SliceStable(sortedSessionInfos, func(i, j int) bool {
		if !sortedSessionInfos[i].CreatedAt.Equal(sortedSessionInfos[j].CreatedAt) {
			return sortedSessionInfos[i].CreatedAt.Before(sortedSessionInfos[j].CreatedAt)
		}
		return sortedSessionInfos[i].ID < sortedSessionInfos[j].ID
	})
	for i := range cfg.Agents {
		agent := &cfg.Agents[i]
		if agent.Suspended || !agent.SupportsGenericEphemeralSessions() {
			continue
		}
		template := agent.QualifiedName()
		for _, sb := range sortedSessionInfos {
			if sb.ID == "" || sb.Closed {
				continue
			}
			if sessionHasProviderTerminalErrorInfo(sb) {
				continue
			}
			if _, ok := resumeSessionBeadIDs[sb.ID]; ok {
				continue
			}
			if !isEphemeralSessionInfoForAgent(sb, agent) || !isPoolManagedSessionInfo(sb) {
				continue
			}
			if normalizedSessionTemplateInfo(sb, cfg) != template {
				continue
			}
			if !poolSessionConsumesNewDemandInfo(sb) {
				continue
			}
			requests[template] = append(requests[template], SessionRequest{
				Template:       template,
				Tier:           "new",
				SessionBeadID:  sb.ID,
				WorkBeadID:     strings.TrimSpace(sb.TriggerBeadID),
				WorkStoreRef:   strings.TrimSpace(sb.TriggerBeadStoreRef),
				BrainParentSID: strings.TrimSpace(sb.BrainParentSID),
			})
		}
	}
	return requests
}

func classifyInFlightWorktreeRequest(request SessionRequest, demand scaleCheckDemand) SessionRequest {
	workID := strings.TrimSpace(request.WorkBeadID)
	if workID == "" {
		return request
	}
	if demand.WorktreeErrors != nil {
		if worktreeError := strings.TrimSpace(demand.WorktreeErrors[workID]); worktreeError != "" {
			request.WorktreeError = worktreeError
			return request
		}
	}
	if demand.WorktreeSpecs != nil {
		if spec := demand.WorktreeSpecs[workID]; spec != nil {
			request.WorktreeSpec = spec
			return request
		}
	}
	for _, candidate := range demand.WorkBeadIDs {
		if strings.TrimSpace(candidate) == workID {
			request.UnmanagedDirect = true
			return request
		}
	}
	request.WorktreeError = "in-flight worktree ownership cannot be classified from current demand"
	return request
}

// poolSessionConsumesNewDemandInfo reports whether a pool session already
// represents spent "new" demand: it holds an active pending_create_claim, or
// its raw state is creating/start-pending. It reads PendingCreateClaim and the
// raw MetadataState. This pure desired-state pass has no reconciler clock:
// creating sessions still represent already-spent new demand; lifecycle code
// owns stale-creating recovery with its clock-aware predicate.
func poolSessionConsumesNewDemandInfo(info sessionpkg.Info) bool {
	if info.PendingCreateClaim {
		return true
	}
	state := strings.TrimSpace(info.MetadataState)
	return state == "creating" || state == string(sessionpkg.StateStartPending)
}

// applyNestedCaps enforces workspace, rig, and agent max_active_sessions caps.
// Accepts requests in priority order, rejecting any that would exceed a cap.
func applyNestedCaps(cfg *config.City, requests []SessionRequest, aliasHeldTemplates, minFillSpawnSuppressed map[string]struct{}, trace *sessionReconcilerTraceCycle) []PoolDesiredState {
	// Sort by priority DESC, resume tier first within same priority.
	sort.SliceStable(requests, func(i, j int) bool {
		if requests[i].BeadPriority != requests[j].BeadPriority {
			return requests[i].BeadPriority > requests[j].BeadPriority
		}
		// Resume-like tiers before new tier at same priority.
		if requests[i].Tier != requests[j].Tier {
			return isResumeLikeTier(requests[i].Tier) && !isResumeLikeTier(requests[j].Tier)
		}
		return false
	})

	limits := newNestedCapLimits(cfg)
	usage := newNestedCapUsage()

	// Walk sorted requests, accepting each if all caps have room.
	accepted := make(map[string][]SessionRequest) // template → accepted requests

	for _, req := range requests {
		template := req.Template
		if usage.isDuplicateSessionRequest(req) {
			continue
		}
		if site, reason, payload, rejected := usage.rejection(req, limits); rejected {
			if trace != nil {
				trace.RecordDecision(site, reason, TraceOutcomeRejected, template, "", payload)
			}
			continue
		}

		// Accept.
		accepted[template] = append(accepted[template], req)
		if trace != nil {
			trace.RecordDecision(TraceSitePoolAccept, TraceReasonCap, TraceOutcomeAccepted, template, "", traceRecordPayload{
				"tier": req.Tier,
			})
		}
		usage.accept(req, limits)
	}

	// Fill agent mins (if caps allow).
	for i := range cfg.Agents {
		agent := &cfg.Agents[i]
		if agent.Suspended {
			continue
		}
		template := agent.QualifiedName()
		minSess := agent.EffectiveMinActiveSessions()
		if _, ok := aliasHeldTemplates[template]; ok {
			continue
		}
		if _, ok := minFillSpawnSuppressed[template]; ok {
			continue
		}
		for usage.agentCount[template] < minSess {
			req := SessionRequest{
				Template:       template,
				Tier:           "new",
				FloorGuarantee: true,
			}
			if _, _, _, rejected := usage.rejection(req, limits); rejected {
				break
			}
			accepted[template] = append(accepted[template], req)
			if trace != nil {
				trace.RecordDecision(TraceSitePoolMinFill, TraceReasonMinFill, TraceOutcomeAccepted, template, "", traceRecordPayload{
					"min":     minSess,
					"current": usage.agentCount[template],
					"tier":    "new",
				})
			}
			usage.accept(req, limits)
		}
	}

	// Build output.
	var result []PoolDesiredState
	for template, reqs := range accepted {
		result = append(result, PoolDesiredState{
			Template: template,
			Requests: reqs,
		})
	}
	// Stable output order.
	sort.Slice(result, func(i, j int) bool {
		return result[i].Template < result[j].Template
	})
	return result
}

type nestedCapLimits struct {
	workspaceMax int
	rigMax       map[string]int
	agentMax     map[string]int
	agentRig     map[string]string
}

type nestedCapUsage struct {
	agentCount      map[string]int
	rigCount        map[string]int
	workspaceCount  int
	seenSessionBead map[string]bool
	requests        []SessionRequest
}

func newNestedCapLimits(cfg *config.City) nestedCapLimits {
	limits := nestedCapLimits{
		workspaceMax: -1,
		rigMax:       make(map[string]int),
		agentMax:     make(map[string]int),
		agentRig:     make(map[string]string),
	}
	if cfg.Workspace.MaxActiveSessions != nil {
		limits.workspaceMax = *cfg.Workspace.MaxActiveSessions
	}
	for _, rig := range cfg.Rigs {
		if rig.MaxActiveSessions != nil {
			limits.rigMax[rig.Name] = *rig.MaxActiveSessions
		} else {
			limits.rigMax[rig.Name] = -1
		}
	}
	for i := range cfg.Agents {
		agent := &cfg.Agents[i]
		template := agent.QualifiedName()
		limits.agentRig[template] = agent.Dir
		resolved := agent.ResolvedMaxActiveSessions(cfg)
		if resolved != nil {
			limits.agentMax[template] = *resolved
		} else {
			limits.agentMax[template] = -1
		}
	}
	return limits
}

func newNestedCapUsage() nestedCapUsage {
	return nestedCapUsage{
		agentCount:      make(map[string]int),
		rigCount:        make(map[string]int),
		seenSessionBead: make(map[string]bool),
	}
}

func acceptedNestedCapUsage(limits nestedCapLimits, requests []SessionRequest) nestedCapUsage {
	usage := newNestedCapUsage()
	sorted := append([]SessionRequest(nil), requests...)
	sort.SliceStable(sorted, func(i, j int) bool {
		if sorted[i].BeadPriority != sorted[j].BeadPriority {
			return sorted[i].BeadPriority > sorted[j].BeadPriority
		}
		if sorted[i].Tier != sorted[j].Tier {
			return isResumeLikeTier(sorted[i].Tier) && !isResumeLikeTier(sorted[j].Tier)
		}
		return false
	})
	for _, req := range sorted {
		if usage.canAccept(req, limits) {
			usage.accept(req, limits)
		}
	}
	return usage
}

func capNewDemandCount(limits nestedCapLimits, usage nestedCapUsage, agent *config.Agent, demand int) int {
	if demand <= 0 {
		return 0
	}
	template := agent.QualifiedName()
	remaining := demand
	if agentMax := limits.agentMax[template]; agentMax >= 0 {
		remaining = minInt(remaining, agentMax-usage.agentCount[template])
	}
	if rig := limits.agentRig[template]; rig != "" {
		rigMax, ok := limits.rigMax[rig]
		if !ok {
			rigMax = -1
		}
		if rigMax >= 0 {
			remaining = minInt(remaining, rigMax-usage.rigCount[rig])
		}
	}
	if limits.workspaceMax >= 0 {
		remaining = minInt(remaining, limits.workspaceMax-usage.workspaceCount)
	}
	if remaining < 0 {
		return 0
	}
	return remaining
}

func (u nestedCapUsage) canAccept(req SessionRequest, limits nestedCapLimits) bool {
	if u.isDuplicateSessionRequest(req) {
		return false
	}
	_, _, _, rejected := u.rejection(req, limits)
	return !rejected
}

func (u nestedCapUsage) isDuplicateSessionRequest(req SessionRequest) bool {
	return req.SessionBeadID != "" && u.seenSessionBead[req.SessionBeadID]
}

func (u nestedCapUsage) rejection(req SessionRequest, limits nestedCapLimits) (TraceSiteCode, TraceReasonCode, traceRecordPayload, bool) {
	template := req.Template
	if agentMax := limits.agentMax[template]; agentMax >= 0 && u.agentCount[template] >= agentMax {
		return TraceSitePoolAgentCap, TraceReasonAgentCap, traceRecordPayload{
			"agent_max": agentMax,
			"current":   u.agentCount[template],
			"tier":      req.Tier,
		}, true
	}
	rig := limits.agentRig[template]
	if rig != "" {
		rigMax, ok := limits.rigMax[rig]
		if !ok {
			rigMax = -1
		}
		if rigMax >= 0 && u.rigCount[rig] >= rigMax {
			return TraceSitePoolRigCap, TraceReasonRigCap, traceRecordPayload{
				"rig":     rig,
				"rig_max": rigMax,
				"current": u.rigCount[rig],
				"tier":    req.Tier,
			}, true
		}
	}
	if limits.workspaceMax >= 0 && u.workspaceCount >= limits.workspaceMax {
		return TraceSitePoolWorkspaceCap, TraceReasonWorkspaceCap, traceRecordPayload{
			"workspace_max": limits.workspaceMax,
			"current":       u.workspaceCount,
			"tier":          req.Tier,
		}, true
	}
	return "", "", nil, false
}

func (u *nestedCapUsage) accept(req SessionRequest, limits nestedCapLimits) {
	u.agentCount[req.Template]++
	if rig := limits.agentRig[req.Template]; rig != "" {
		u.rigCount[rig]++
	}
	u.workspaceCount++
	if req.SessionBeadID != "" {
		u.seenSessionBead[req.SessionBeadID] = true
	}
	u.requests = append(u.requests, req)
}

func recordNewDemandCapTrace(
	trace *sessionReconcilerTraceCycle,
	template string,
	agent *config.Agent,
	limits nestedCapLimits,
	usage nestedCapUsage,
	scaleCount int,
	newCount int,
) {
	if trace == nil || scaleCount <= 0 || newCount >= scaleCount {
		return
	}
	site, reason, capMax, current, blockers := newDemandBlockingScope(template, agent, limits, usage, newCount)
	if site == "" {
		return
	}
	blockingSessions := make([]string, 0, len(blockers))
	blockingWork := make([]string, 0, len(blockers))
	for _, req := range blockers {
		if req.SessionBeadID != "" {
			blockingSessions = append(blockingSessions, req.SessionBeadID)
		}
		if req.WorkBeadID != "" {
			blockingWork = append(blockingWork, req.WorkBeadID)
		}
	}
	trace.RecordDecision(site, reason, TraceOutcomeRejected, template, "", traceRecordPayload{
		"scale_check":          scaleCount,
		"accepted_new":         newCount,
		"blocked_new":          scaleCount - newCount,
		"current":              current,
		"max":                  capMax,
		"blocking_sessions":    blockingSessions,
		"blocking_work_beads":  blockingWork,
		"active_capacity_kind": string(reason),
	})
}

func newDemandBlockingScope(
	template string,
	agent *config.Agent,
	limits nestedCapLimits,
	usage nestedCapUsage,
	newCount int,
) (TraceSiteCode, TraceReasonCode, int, int, []SessionRequest) {
	if agentMax := limits.agentMax[template]; agentMax >= 0 && agentMax-usage.agentCount[template] <= newCount {
		return TraceSitePoolNewDemandCap, TraceReasonAgentCap, agentMax, usage.agentCount[template], filterCapBlockers(usage.requests, func(req SessionRequest) bool {
			return req.Template == template
		})
	}
	if agent != nil {
		if rig := limits.agentRig[template]; rig != "" {
			rigMax, ok := limits.rigMax[rig]
			if !ok {
				rigMax = -1
			}
			if rigMax >= 0 && rigMax-usage.rigCount[rig] <= newCount {
				return TraceSitePoolNewDemandCap, TraceReasonRigCap, rigMax, usage.rigCount[rig], filterCapBlockers(usage.requests, func(req SessionRequest) bool {
					return limits.agentRig[req.Template] == rig
				})
			}
		}
	}
	if limits.workspaceMax >= 0 && limits.workspaceMax-usage.workspaceCount <= newCount {
		return TraceSitePoolNewDemandCap, TraceReasonWorkspaceCap, limits.workspaceMax, usage.workspaceCount, usage.requests
	}
	return "", "", 0, 0, nil
}

func filterCapBlockers(requests []SessionRequest, keep func(SessionRequest) bool) []SessionRequest {
	out := make([]SessionRequest, 0, len(requests))
	for _, req := range requests {
		if keep(req) {
			out = append(out, req)
		}
	}
	return out
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func isKnownPoolTemplate(assignee string, cfg *config.City) bool {
	assignee = strings.TrimSpace(assignee)
	if assignee == "" || cfg == nil {
		return false
	}
	for i := range cfg.Agents {
		agent := &cfg.Agents[i]
		if agent.Suspended || !agent.SupportsGenericEphemeralSessions() {
			continue
		}
		if agentTemplateIdentitiesEquivalent(cfg, assignee, agent.QualifiedName()) {
			return true
		}
	}
	return false
}

func isResumeLikeTier(tier string) bool {
	return tier == "resume" || tier == "wake-known-identity"
}

// isConfiguredNamedSessionIdentity reports whether assignee names a
// configured [[named_session]]'s own identity — checked structurally via
// cfg.NamedSessions, with no live-session/store lookup, so it holds even
// when the named session has no live session bead at all. A named session
// whose backing agent is suspended is excluded: the named-session tier
// never claims work for a suspended agent (mirrors the namedSpecs filter in
// build_desired_state.go), so exempting it here too would orphan the bead
// with neither side picking it up.
func isConfiguredNamedSessionIdentity(cfg *config.City, assignee string) bool {
	assignee = strings.TrimSpace(assignee)
	if assignee == "" || cfg == nil {
		return false
	}
	spec, ok := findNamedSessionSpec(cfg, "", assignee)
	if !ok || spec.Agent == nil {
		return false
	}
	return !spec.Agent.Suspended
}
