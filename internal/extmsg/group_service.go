package extmsg

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/gastownhall/gascity/internal/beads"
)

type groupService struct {
	backend fabricBackend
	// sessionStore holds session-class beads, read only for session-liveness
	// resolution (sessionNameForSelector / overlayLiveParticipantSessionID).
	// Identical to the record store on a single-store city; distinct once a
	// class relocates. Record mutations never touch it.
	sessionStore beads.Store
	locks        *bindingLockPool
	transcript   groupTranscriptSync
}

type groupTranscriptSync interface {
	EnsureMembership(ctx context.Context, input EnsureMembershipInput) (ConversationMembershipRecord, error)
	RemoveMembership(ctx context.Context, input RemoveMembershipInput) error
}

// NewGroupService creates a GroupService backed by the given bead store,
// which serves both record persistence and session-liveness reads (the
// single-store shape).
func NewGroupService(store beads.Store) GroupService {
	locks := sharedBindingLockPool(store)
	return newGroupService(store, store, locks, newTranscriptService(store, locks))
}

func newGroupService(store, sessionStore beads.Store, locks *bindingLockPool, transcript groupTranscriptSync) GroupService {
	if sessionStore == nil {
		sessionStore = store
	}
	return newGroupServiceWithBackend(newBeadBackend(store), sessionStore, locks, transcript)
}

func newGroupServiceWithBackend(backend fabricBackend, sessionStore beads.Store, locks *bindingLockPool, transcript groupTranscriptSync) GroupService {
	return &groupService{backend: backend, sessionStore: sessionStore, locks: locks, transcript: transcript}
}

func groupTranscriptCaller() Caller {
	return Caller{Kind: CallerController, ID: "group-service"}
}

func (s *groupService) EnsureGroup(ctx context.Context, caller Caller, input EnsureGroupInput) (ConversationGroupRecord, error) {
	if err := checkContext(ctx); err != nil {
		return ConversationGroupRecord{}, err
	}
	ref, err := validateConversationRef(input.RootConversation)
	if err != nil {
		return ConversationGroupRecord{}, err
	}
	if err := authorizeMutation(caller, ref); err != nil {
		return ConversationGroupRecord{}, err
	}
	mode := GroupMode(strings.ToLower(strings.TrimSpace(string(input.Mode))))
	switch mode {
	case GroupModeLauncher:
	default:
		return ConversationGroupRecord{}, fmt.Errorf("%w: invalid group mode %q", ErrInvalidInput, input.Mode)
	}
	fields := GroupFields{
		Ref:                 ref,
		Mode:                mode,
		DefaultHandle:       normalizeHandle(input.DefaultHandle),
		LastAddressedHandle: normalizeHandle(input.LastAddressedHandle),
		Fanout:              input.FanoutPolicy,
		Meta:                input.Metadata,
	}
	var out ConversationGroupRecord
	err = withLockKey(s.locks, groupRootLabel(ref), func() error {
		groups, err := s.backend.OpenGroupsByRoot(ref)
		if err != nil {
			return err
		}
		if len(groups) > 0 {
			if err := checkContext(ctx); err != nil {
				return err
			}
			if err := s.backend.UpdateGroup(groups[0].ID, fields); err != nil {
				return err
			}
			out, err = s.backend.RefetchGroup(groups[0].ID)
			return err
		}
		out, err = s.backend.CreateGroup(fields)
		return err
	})
	return out, err
}

func (s *groupService) UpsertParticipant(ctx context.Context, caller Caller, input UpsertParticipantInput) (ConversationGroupParticipant, error) {
	if err := checkContext(ctx); err != nil {
		return ConversationGroupParticipant{}, err
	}
	groupID := strings.TrimSpace(input.GroupID)
	if groupID == "" {
		return ConversationGroupParticipant{}, fmt.Errorf("%w: group_id required", ErrInvalidInput)
	}
	handle, err := validateHandle(input.Handle)
	if err != nil {
		return ConversationGroupParticipant{}, err
	}
	sessionID := strings.TrimSpace(input.SessionID)
	if sessionID == "" {
		return ConversationGroupParticipant{}, fmt.Errorf("%w: session_id required", ErrInvalidInput)
	}
	group, err := s.getGroupByID(groupID)
	if err != nil {
		return ConversationGroupParticipant{}, err
	}
	if err := authorizeMutation(caller, group.RootConversation); err != nil {
		return ConversationGroupParticipant{}, err
	}
	// Capture the stable session name so the participant survives respawn.
	// Best-effort: empty when the selector resolves to no session bead.
	sessionName := sessionNameForSelector(s.sessionStore, sessionID)
	fields := ParticipantFields{
		GroupID:     groupID,
		Handle:      handle,
		SessionID:   sessionID,
		SessionName: sessionName,
		Public:      input.Public,
		Meta:        input.Metadata,
	}
	var out ConversationGroupParticipant
	err = withLockKey(s.locks, groupParticipantsMutationLock(groupID), func() error {
		records, err := s.backend.ParticipantsByGroup(groupID, true)
		if err != nil {
			return err
		}
		for _, record := range records {
			if err := checkContext(ctx); err != nil {
				return err
			}
			if record.Closed {
				continue
			}
			if record.Handle != handle {
				continue
			}
			pendingCleanup := record.PendingCleanup
			if record.SessionID != "" && record.SessionID != sessionID {
				pendingCleanup = append(pendingCleanup, record.SessionID)
			}
			pendingCleanup = removeSessionID(pendingCleanup, sessionID)
			if err := s.backend.RetargetParticipant(record.ID, fields, record.SessionID, record.SessionName, pendingCleanup); err != nil {
				return err
			}
			out, err = s.backend.RefetchParticipant(record.ID)
			if err != nil {
				return err
			}
			return s.migrateParticipantGroupMembership(ctx, group, record.ID, sessionID, pendingCleanup)
		}
		created, err := s.backend.CreateParticipant(fields)
		if err != nil {
			return err
		}
		out = created
		return s.migrateParticipantGroupMembership(ctx, group, created.ID, sessionID, nil)
	})
	if err != nil {
		return ConversationGroupParticipant{}, err
	}
	return out, nil
}

func (s *groupService) RemoveParticipant(ctx context.Context, caller Caller, input RemoveParticipantInput) error {
	if err := checkContext(ctx); err != nil {
		return err
	}
	groupID := strings.TrimSpace(input.GroupID)
	if groupID == "" {
		return fmt.Errorf("%w: group_id required", ErrInvalidInput)
	}
	handle, err := validateHandle(input.Handle)
	if err != nil {
		return err
	}
	group, err := s.getGroupByID(groupID)
	if err != nil {
		return err
	}
	if err := authorizeMutation(caller, group.RootConversation); err != nil {
		return err
	}
	var sessionIDs []string
	var found bool
	err = withLockKey(s.locks, groupParticipantsMutationLock(groupID), func() error {
		records, err := s.backend.ParticipantsByGroup(groupID, true)
		if err != nil {
			return err
		}
		seenSessionIDs := make(map[string]struct{})
		for _, record := range records {
			if record.Handle != handle {
				continue
			}
			found = true
			if record.SessionID != "" {
				if _, ok := seenSessionIDs[record.SessionID]; !ok {
					seenSessionIDs[record.SessionID] = struct{}{}
					sessionIDs = append(sessionIDs, record.SessionID)
				}
			}
			for _, pendingSessionID := range record.PendingCleanup {
				if pendingSessionID == "" {
					continue
				}
				if _, ok := seenSessionIDs[pendingSessionID]; ok {
					continue
				}
				seenSessionIDs[pendingSessionID] = struct{}{}
				sessionIDs = append(sessionIDs, pendingSessionID)
			}
			if record.Closed {
				continue
			}
			if err := s.backend.CloseParticipant(record.ID); err != nil {
				return fmt.Errorf("close participant %s: %w", record.ID, err)
			}
		}
		if s.transcript == nil {
			return nil
		}
		activeSessions, err := s.activeParticipantSessionCounts(ctx, groupID)
		if err != nil {
			return err
		}
		for _, sessionID := range sessionIDs {
			if activeSessions[sessionID] > 0 {
				continue
			}
			err := s.transcript.RemoveMembership(ctx, RemoveMembershipInput{
				Caller:       groupTranscriptCaller(),
				Conversation: group.RootConversation,
				SessionID:    sessionID,
				Owner:        MembershipOwnerGroup,
				Now:          timeNow(),
			})
			if err == nil || errors.Is(err, ErrMembershipNotFound) {
				continue
			}
			return wrapTranscriptSyncError("remove transcript membership after participant removal", err)
		}
		return nil
	})
	if err != nil {
		return err
	}
	if !found {
		return ErrGroupRouteNotFound
	}
	return nil
}

func (s *groupService) ResolveInbound(ctx context.Context, event ExternalInboundMessage) (*GroupRouteDecision, error) {
	if err := checkContext(ctx); err != nil {
		return nil, err
	}
	ref, err := validateConversationRef(event.Conversation)
	if err != nil {
		return nil, err
	}
	group, err := s.findGroupByRoot(ref)
	if err != nil {
		return nil, err
	}
	if group == nil {
		return &GroupRouteDecision{Match: GroupRouteNoGroup}, nil
	}
	participants, err := s.listParticipants(group.ID)
	if err != nil {
		return nil, err
	}
	byHandle := make(map[string]ConversationGroupParticipant, len(participants))
	for _, participant := range participants {
		overlayLiveParticipantSessionID(s.sessionStore, &participant)
		byHandle[participant.Handle] = participant
	}
	if explicit := normalizeHandle(event.ExplicitTarget); explicit != "" {
		target, ok := byHandle[explicit]
		if !ok {
			return &GroupRouteDecision{Match: GroupRouteNoMatch}, nil
		}
		return &GroupRouteDecision{
			Match:           GroupRouteExplicitTarget,
			TargetSessionID: target.SessionID,
			UpdateCursor:    true,
		}, nil
	}
	if target, ok := byHandle[group.LastAddressedHandle]; ok {
		return &GroupRouteDecision{
			Match:           GroupRouteLastAddressed,
			TargetSessionID: target.SessionID,
		}, nil
	}
	if target, ok := byHandle[group.DefaultHandle]; ok {
		return &GroupRouteDecision{
			Match:           GroupRouteDefault,
			TargetSessionID: target.SessionID,
		}, nil
	}
	return &GroupRouteDecision{Match: GroupRouteNoMatch}, nil
}

// ResolveOutbound authorizes an outbound publish from sessionID against the
// conversation's group. Unlike ResolveInbound (which routes a message to a
// participant by handle), ResolveOutbound checks whether sessionID is itself
// a participant of the group bound to ref. This mirrors the authorization
// boundary established by bind-room: any session that is a group participant
// is authorized to publish on behalf of the conversation.
//
// Returns a decision with Match == GroupRouteParticipantMatch and the matching
// participant when sessionID is a participant. Returns Match == GroupRouteNoMatch
// when no group is bound or sessionID is not a participant.
func (s *groupService) ResolveOutbound(ctx context.Context, ref ConversationRef, sessionID string) (*GroupOutboundDecision, error) {
	if err := checkContext(ctx); err != nil {
		return nil, err
	}
	validatedRef, err := validateConversationRef(ref)
	if err != nil {
		return nil, err
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil, fmt.Errorf("%w: session_id required", ErrInvalidInput)
	}
	group, err := s.findGroupByRoot(validatedRef)
	if err != nil {
		return nil, err
	}
	if group == nil {
		return &GroupOutboundDecision{Match: GroupRouteNoMatch}, nil
	}
	participants, err := s.listParticipants(group.ID)
	if err != nil {
		return nil, err
	}
	for _, participant := range participants {
		overlayLiveParticipantSessionID(s.sessionStore, &participant)
		if participant.SessionID == sessionID {
			return &GroupOutboundDecision{
				Match:       GroupRouteParticipantMatch,
				GroupID:     group.ID,
				Participant: participant,
			}, nil
		}
	}
	return &GroupOutboundDecision{Match: GroupRouteNoMatch, GroupID: group.ID}, nil
}

func (s *groupService) UpdateCursor(ctx context.Context, caller Caller, input UpdateCursorInput) error {
	if err := checkContext(ctx); err != nil {
		return err
	}
	ref, err := validateConversationRef(input.RootConversation)
	if err != nil {
		return err
	}
	if err := authorizeMutation(caller, ref); err != nil {
		return err
	}
	handle := normalizeHandle(input.Handle)
	group, err := s.findGroupByRoot(ref)
	if err != nil {
		return err
	}
	if group == nil {
		return ErrGroupNotFound
	}
	if handle == "" {
		return s.backend.SetGroupCursor(group.ID, "")
	}
	participants, err := s.listParticipants(group.ID)
	if err != nil {
		return err
	}
	found := false
	for _, participant := range participants {
		if participant.Handle == handle {
			found = true
			break
		}
	}
	if !found {
		return ErrGroupRouteNotFound
	}
	return s.backend.SetGroupCursor(group.ID, handle)
}

// FindByConversation looks up an existing group by its root conversation.
func (s *groupService) FindByConversation(_ context.Context, _ Caller, ref ConversationRef) (*ConversationGroupRecord, error) {
	group, err := s.findGroupByRoot(ref)
	if err != nil {
		return nil, err
	}
	if group == nil {
		return nil, ErrGroupNotFound
	}
	return group, nil
}

func (s *groupService) findGroupByRoot(ref ConversationRef) (*ConversationGroupRecord, error) {
	groups, err := s.backend.OpenGroupsByRoot(ref)
	if err != nil {
		return nil, err
	}
	var out *ConversationGroupRecord
	for _, record := range groups {
		if out != nil {
			return nil, fmt.Errorf("%w: multiple groups for %s", ErrInvariantViolation, conversationLockKey(ref))
		}
		rec := record
		out = &rec
	}
	return out, nil
}

func (s *groupService) getGroupByID(groupID string) (ConversationGroupRecord, error) {
	record, ok, err := s.backend.GetGroup(groupID)
	if err != nil {
		return ConversationGroupRecord{}, err
	}
	if !ok {
		return ConversationGroupRecord{}, ErrGroupNotFound
	}
	return record, nil
}

func (s *groupService) listParticipants(groupID string) ([]ConversationGroupParticipant, error) {
	records, err := s.backend.ParticipantsByGroup(groupID, false)
	if err != nil {
		return nil, err
	}
	out := make([]ConversationGroupParticipant, 0, len(records))
	seen := make(map[string]ConversationGroupParticipant)
	for _, record := range records {
		if record.Closed {
			continue
		}
		if existing, ok := seen[record.Handle]; ok {
			return nil, fmt.Errorf("%w: duplicate participants for handle %s (%s, %s)", ErrInvariantViolation, record.Handle, existing.ID, record.ID)
		}
		seen[record.Handle] = record.ConversationGroupParticipant
		out = append(out, record.ConversationGroupParticipant)
	}
	return out, nil
}

func (s *groupService) activeParticipantSessionCounts(ctx context.Context, groupID string) (map[string]int, error) {
	records, err := s.backend.ParticipantsByGroup(groupID, false)
	if err != nil {
		return nil, err
	}
	counts := make(map[string]int)
	for _, record := range records {
		if err := checkContext(ctx); err != nil {
			return nil, err
		}
		if record.Closed {
			continue
		}
		if record.SessionID == "" {
			continue
		}
		counts[record.SessionID]++
	}
	return counts, nil
}

func (s *groupService) setParticipantPendingCleanup(participantID string, sessionIDs []string) error {
	if err := s.backend.SetParticipantPendingCleanup(participantID, sessionIDs); err != nil {
		return fmt.Errorf("set participant pending cleanup: %w", err)
	}
	return nil
}

// migrateParticipantGroupMembership ensures newSessionID owns the group's
// transcript membership on the root conversation, then retires the group-owned
// membership for any session in retiredSessionIDs that no active participant in
// the group still references. A retired session whose membership cannot be
// removed yet — a live participant still uses it, or the remove failed — is
// written back onto the participant record's pending-cleanup set so a later
// mutation retries it.
//
// Both UpsertParticipant (when a handle's session changes) and
// ReassignSessionParticipants (canonical respawn handover) move a group
// participant to a new session, so both must carry the session-ID-keyed
// membership with it; sharing this helper keeps those paths from drifting.
// Callers must hold groupParticipantsMutationLock for the group and should have
// already persisted retiredSessionIDs to that record so an ensure failure
// still leaves a durable cleanup record.
//
// The ensure/remove writes timestamp with the package timeNow() clock rather
// than a caller-threaded now (as the sibling ReassignSessionBindings uses):
// this helper is shared with UpsertParticipant, which has no caller-supplied
// now, and timeNow is the package-wide clock seam (frozen in tests), so a single
// clock source keeps both callers consistent without plumbing a now through the
// participant upsert API. The timestamp is a touched-at marker, so the
// sub-operation instant difference is immaterial.
func (s *groupService) migrateParticipantGroupMembership(ctx context.Context, group ConversationGroupRecord, participantID, newSessionID string, retiredSessionIDs []string) error {
	if s.transcript == nil {
		return nil
	}
	if _, err := s.transcript.EnsureMembership(ctx, EnsureMembershipInput{
		Caller:         groupTranscriptCaller(),
		Conversation:   group.RootConversation,
		SessionID:      newSessionID,
		BackfillPolicy: MembershipBackfillAll,
		Owner:          MembershipOwnerGroup,
		Now:            timeNow(),
	}); err != nil {
		return wrapTranscriptSyncError("ensure transcript membership after participant session migration", err)
	}
	if len(retiredSessionIDs) == 0 {
		return nil
	}
	activeSessions, err := s.activeParticipantSessionCounts(ctx, group.ID)
	if err != nil {
		return err
	}
	remainingCleanup := make([]string, 0, len(retiredSessionIDs))
	var cleanupErr error
	for _, cleanupSessionID := range retiredSessionIDs {
		if activeSessions[cleanupSessionID] > 0 {
			continue
		}
		err = s.transcript.RemoveMembership(ctx, RemoveMembershipInput{
			Caller:       groupTranscriptCaller(),
			Conversation: group.RootConversation,
			SessionID:    cleanupSessionID,
			Owner:        MembershipOwnerGroup,
			Now:          timeNow(),
		})
		if err == nil || errors.Is(err, ErrMembershipNotFound) {
			continue
		}
		cleanupErr = err
		remainingCleanup = append(remainingCleanup, cleanupSessionID)
	}
	if err := s.setParticipantPendingCleanup(participantID, remainingCleanup); err != nil {
		return err
	}
	if len(remainingCleanup) > 0 {
		return wrapTranscriptSyncError("remove transcript membership after participant session migration", cleanupErr)
	}
	return nil
}

func groupParticipantsMutationLock(groupID string) string {
	return groupParticipantLabel(groupID) + ":mutation"
}

func pendingCleanupSessionIDsFromMetadata(metadata map[string]string) []string {
	raw := strings.TrimSpace(metadata["previous_session_id_pending_cleanup"])
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	seen := make(map[string]struct{}, len(parts))
	for _, part := range parts {
		sessionID := strings.TrimSpace(part)
		if sessionID == "" {
			continue
		}
		if _, ok := seen[sessionID]; ok {
			continue
		}
		seen[sessionID] = struct{}{}
		out = append(out, sessionID)
	}
	slices.Sort(out)
	return out
}

func encodePendingCleanupSessionIDs(sessionIDs []string) string {
	if len(sessionIDs) == 0 {
		return ""
	}
	normalized := make([]string, 0, len(sessionIDs))
	seen := make(map[string]struct{}, len(sessionIDs))
	for _, sessionID := range sessionIDs {
		sessionID = strings.TrimSpace(sessionID)
		if sessionID == "" {
			continue
		}
		if _, ok := seen[sessionID]; ok {
			continue
		}
		seen[sessionID] = struct{}{}
		normalized = append(normalized, sessionID)
	}
	slices.Sort(normalized)
	return strings.Join(normalized, ",")
}

func removeSessionID(sessionIDs []string, target string) []string {
	target = strings.TrimSpace(target)
	if target == "" {
		return pendingCleanupSessionIDsFromMetadata(map[string]string{"previous_session_id_pending_cleanup": encodePendingCleanupSessionIDs(sessionIDs)})
	}
	out := make([]string, 0, len(sessionIDs))
	for _, sessionID := range sessionIDs {
		sessionID = strings.TrimSpace(sessionID)
		if sessionID == "" || sessionID == target {
			continue
		}
		out = append(out, sessionID)
	}
	return pendingCleanupSessionIDsFromMetadata(map[string]string{"previous_session_id_pending_cleanup": encodePendingCleanupSessionIDs(out)})
}

// participantReassignmentPending reports whether a group-participant record
// still needs its session reassigned from oldSessionID to newSessionID. It is
// true when the record still points at the retired session, or when a prior
// attempt already swapped session_id to the replacement but left the retired
// session in the pending-cleanup set — meaning transcript membership migration
// had not completed yet, so a retry must finish the handover. The
// retired-session lookup handle is retained until that point precisely so such
// a partially migrated participant remains discoverable by
// ReassignSessionParticipants.
func participantReassignmentPending(record ParticipantRecord, oldSessionID, newSessionID string) bool {
	switch record.SessionID {
	case oldSessionID:
		return true
	case newSessionID:
		return slices.Contains(record.PendingCleanup, oldSessionID)
	default:
		return false
	}
}

//nolint:unparam // error return reserved for future decoding failures
func decodeParticipantBead(b beads.Bead) (ConversationGroupParticipant, error) {
	return ConversationGroupParticipant{
		ID:          b.ID,
		GroupID:     strings.TrimSpace(b.Metadata["group_id"]),
		Handle:      normalizeHandle(b.Metadata["handle"]),
		SessionID:   strings.TrimSpace(b.Metadata["session_id"]),
		SessionName: strings.TrimSpace(b.Metadata["session_name"]),
		Public:      parseBool(b.Metadata, "public"),
		Metadata:    decodePrefixedMetadata(b.Metadata),
	}, nil
}

// overlayLiveParticipantSessionID re-points a participant at its session's
// current live bead when the stored session_id has gone stale across a
// respawn. It mutates only the in-memory copy — persistent healing is
// ReassignSessionParticipants' job (runs on session handover).
func overlayLiveParticipantSessionID(store beads.Store, participant *ConversationGroupParticipant) {
	overlayLiveSessionID(store, participant.SessionName, participant.SessionID, &participant.SessionID)
}

// participantSessionLabels returns the label set for a participant given its
// session ID (volatile) and optional session name (stable). The session-name
// label is omitted when name is empty (legacy participants without one).
func participantSessionLabels(sessionID, sessionName string) []string {
	labels := []string{groupParticipantSessionLabel(sessionID)}
	if sessionName != "" {
		labels = append(labels, groupParticipantSessionNameLabel(sessionName))
	}
	return labels
}
