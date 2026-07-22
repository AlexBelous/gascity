package extmsg

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/gastownhall/gascity/internal/beads"
)

const (
	transcriptBucketSize      = int64(64)
	defaultTranscriptPageSize = 100
	maxTranscriptPageSize     = 500
)

type transcriptService struct {
	backend fabricBackend
	locks   *bindingLockPool
}

func newTranscriptService(store beads.Store, locks *bindingLockPool) *transcriptService {
	return newTranscriptServiceWithBackend(newBeadBackend(store), locks)
}

func newTranscriptServiceWithBackend(backend fabricBackend, locks *bindingLockPool) *transcriptService {
	return &transcriptService{backend: backend, locks: locks}
}

func (s *transcriptService) Append(ctx context.Context, input AppendTranscriptInput) (ConversationTranscriptRecord, error) {
	if err := checkContext(ctx); err != nil {
		return ConversationTranscriptRecord{}, err
	}
	ref, err := validateConversationRef(input.Conversation)
	if err != nil {
		return ConversationTranscriptRecord{}, err
	}
	if err := authorizeMutation(input.Caller, ref); err != nil {
		return ConversationTranscriptRecord{}, err
	}
	kind := TranscriptMessageKind(strings.ToLower(strings.TrimSpace(string(input.Kind))))
	switch kind {
	case TranscriptMessageInbound, TranscriptMessageOutbound:
	default:
		return ConversationTranscriptRecord{}, fmt.Errorf("%w: invalid transcript kind %q", ErrInvalidInput, input.Kind)
	}
	provenance := TranscriptProvenance(strings.ToLower(strings.TrimSpace(string(input.Provenance))))
	switch provenance {
	case "":
		provenance = TranscriptProvenanceLive
	case TranscriptProvenanceLive, TranscriptProvenanceHydrated:
	default:
		return ConversationTranscriptRecord{}, fmt.Errorf("%w: invalid provenance %q", ErrInvalidInput, input.Provenance)
	}
	createdAt := zeroNow(input.CreatedAt)
	providerMessageID := strings.TrimSpace(input.ProviderMessageID)
	text := strings.TrimSpace(input.Text)
	var out ConversationTranscriptRecord
	err = withBindingLock(s.locks, ref, func() error {
		state, err := s.ensureStateLocked(ref)
		if err != nil {
			return err
		}
		if provenance == TranscriptProvenanceHydrated {
			if state.HydrationStatus != HydrationPending {
				return fmt.Errorf("%w: conversation is not pending hydration", ErrInvalidInput)
			}
		} else if state.HydrationStatus == HydrationPending {
			return ErrHydrationPending
		}
		if providerMessageID != "" {
			existing, err := s.findTranscriptByProviderMessageLocked(ref, providerMessageID)
			if err != nil {
				return err
			}
			if existing != nil {
				out = *existing
				return nil
			}
		}
		actorJSON, err := marshalOptionalJSON(input.Actor)
		if err != nil {
			return err
		}
		attachmentsJSON, err := marshalOptionalJSON(input.Attachments)
		if err != nil {
			return err
		}
		sequence := state.NextSequence
		out, err = s.backend.AppendTranscript(TranscriptEntryCreate{
			Ref:               ref,
			Sequence:          sequence,
			Kind:              kind,
			Provenance:        provenance,
			ProviderMessageID: providerMessageID,
			ExplicitTarget:    normalizeHandle(input.ExplicitTarget),
			ReplyToMessageID:  strings.TrimSpace(input.ReplyToMessageID),
			SourceSessionID:   strings.TrimSpace(input.SourceSessionID),
			CreatedAt:         createdAt,
			Text:              text,
			ActorJSON:         actorJSON,
			AttachmentsJSON:   attachmentsJSON,
			Meta:              input.Metadata,
		}, state.ID, sequence+1, state.EarliestAvailableSequence <= 0)
		return err
	})
	return out, err
}

func (s *transcriptService) List(ctx context.Context, input ListTranscriptInput) ([]ConversationTranscriptRecord, error) {
	if err := checkContext(ctx); err != nil {
		return nil, err
	}
	if err := requireControllerCaller(input.Caller); err != nil {
		return nil, err
	}
	ref, err := validateConversationRef(input.Conversation)
	if err != nil {
		return nil, err
	}
	after := input.AfterSequence
	if after < 0 {
		after = 0
	}
	limit := clampTranscriptLimit(input.Limit)
	order, err := normalizeTranscriptOrder(input.Order)
	if err != nil {
		return nil, err
	}
	var out []ConversationTranscriptRecord
	err = withBindingLock(s.locks, ref, func() error {
		state, err := s.findStateLocked(ref)
		if err != nil {
			return err
		}
		if state == nil || state.NextSequence <= 1 {
			out = nil
			return nil
		}
		out, err = s.listTranscriptLocked(ref, after, limit, order)
		return err
	})
	return out, err
}

func (s *transcriptService) EnsureMembership(ctx context.Context, input EnsureMembershipInput) (ConversationMembershipRecord, error) {
	if err := checkContext(ctx); err != nil {
		return ConversationMembershipRecord{}, err
	}
	if err := requireControllerCaller(input.Caller); err != nil {
		return ConversationMembershipRecord{}, err
	}
	ref, err := validateConversationRef(input.Conversation)
	if err != nil {
		return ConversationMembershipRecord{}, err
	}
	sessionID := strings.TrimSpace(input.SessionID)
	if sessionID == "" {
		return ConversationMembershipRecord{}, fmt.Errorf("%w: session_id required", ErrInvalidInput)
	}
	policy, err := normalizeBackfillPolicy(input.BackfillPolicy)
	if err != nil {
		return ConversationMembershipRecord{}, err
	}
	owner, err := normalizeMembershipOwner(input.Owner)
	if err != nil {
		return ConversationMembershipRecord{}, err
	}
	now := zeroNow(input.Now)
	var out ConversationMembershipRecord
	err = withBindingLock(s.locks, ref, func() error {
		var lockedErr error
		out, lockedErr = s.ensureMembershipLocked(EnsureMembershipInput{
			Caller:         input.Caller,
			Conversation:   ref,
			SessionID:      sessionID,
			BackfillPolicy: policy,
			Owner:          owner,
			Metadata:       input.Metadata,
			Now:            now,
		})
		return lockedErr
	})
	return out, err
}

func (s *transcriptService) UpdateMembership(ctx context.Context, input UpdateMembershipInput) (ConversationMembershipRecord, error) {
	if err := checkContext(ctx); err != nil {
		return ConversationMembershipRecord{}, err
	}
	if err := requireControllerCaller(input.Caller); err != nil {
		return ConversationMembershipRecord{}, err
	}
	ref, err := validateConversationRef(input.Conversation)
	if err != nil {
		return ConversationMembershipRecord{}, err
	}
	sessionID := strings.TrimSpace(input.SessionID)
	if sessionID == "" {
		return ConversationMembershipRecord{}, fmt.Errorf("%w: session_id required", ErrInvalidInput)
	}
	policy, err := normalizeBackfillPolicy(input.BackfillPolicy)
	if err != nil {
		return ConversationMembershipRecord{}, err
	}
	var out ConversationMembershipRecord
	err = withBindingLock(s.locks, ref, func() error {
		existing, err := s.findActiveMembershipLocked(ref, sessionID)
		if err != nil {
			return err
		}
		if existing == nil {
			return ErrMembershipNotFound
		}
		owners, _ := addMembershipOwner(existing.Owners, MembershipOwnerManual)
		patch := MembershipPatch{
			Owners:      owners,
			SetOwners:   true,
			Manual:      string(policy),
			SetManual:   true,
			Backfill:    effectiveMembershipBackfillPolicy(owners, policy),
			SetBackfill: true,
			Meta:        input.Metadata,
		}
		if err := s.backend.Writer().PatchMembership(existing.ID, patch); err != nil {
			return fmt.Errorf("update membership metadata: %w", err)
		}
		out, err = s.backend.RefetchMembership(existing.ID)
		return err
	})
	return out, err
}

func (s *transcriptService) RemoveMembership(ctx context.Context, input RemoveMembershipInput) error {
	if err := checkContext(ctx); err != nil {
		return err
	}
	if err := requireControllerCaller(input.Caller); err != nil {
		return err
	}
	ref, err := validateConversationRef(input.Conversation)
	if err != nil {
		return err
	}
	sessionID := strings.TrimSpace(input.SessionID)
	if sessionID == "" {
		return fmt.Errorf("%w: session_id required", ErrInvalidInput)
	}
	owner, err := normalizeMembershipOwner(input.Owner)
	if err != nil {
		return err
	}
	now := zeroNow(input.Now)
	return withBindingLock(s.locks, ref, func() error {
		return s.removeMembershipLocked(RemoveMembershipInput{
			Caller:       input.Caller,
			Conversation: ref,
			SessionID:    sessionID,
			Owner:        owner,
			Now:          now,
		})
	})
}

// membershipWriter is the raw write surface the bead backend's FabricWriter
// persists through. Both beads.Store and beads.Tx satisfy it, so a caller
// can route the membership writes through a coalescing transaction (one
// commit) or commit them standalone.
type membershipWriter interface {
	Create(b beads.Bead) (beads.Bead, error)
	SetMetadataBatch(id string, kvs map[string]string) error
}

func (s *transcriptService) ensureMembershipLocked(input EnsureMembershipInput) (ConversationMembershipRecord, error) {
	return s.ensureMembershipLockedWriter(s.backend.Writer(), input)
}

// ensureMembershipLockedWriter is ensureMembershipLocked with its writes
// routed through w. Reads stay on the backend. When w is transactional, the
// post-write re-fetch of an existing membership reads pre-commit state;
// callers that pass a transactional writer must not depend on the returned
// record (the bind path discards it). The committed writes are correct in
// either case.
func (s *transcriptService) ensureMembershipLockedWriter(w FabricWriter, input EnsureMembershipInput) (ConversationMembershipRecord, error) {
	ref, err := validateConversationRef(input.Conversation)
	if err != nil {
		return ConversationMembershipRecord{}, err
	}
	sessionID := strings.TrimSpace(input.SessionID)
	if sessionID == "" {
		return ConversationMembershipRecord{}, fmt.Errorf("%w: session_id required", ErrInvalidInput)
	}
	policy, err := normalizeBackfillPolicy(input.BackfillPolicy)
	if err != nil {
		return ConversationMembershipRecord{}, err
	}
	owner, err := normalizeMembershipOwner(input.Owner)
	if err != nil {
		return ConversationMembershipRecord{}, err
	}
	now := zeroNow(input.Now)
	state, err := s.ensureStateLockedWriter(w, ref)
	if err != nil {
		return ConversationMembershipRecord{}, err
	}
	existing, err := s.findActiveMembershipLocked(ref, sessionID)
	if err != nil {
		return ConversationMembershipRecord{}, err
	}
	if existing != nil {
		owners, changed := addMembershipOwner(existing.Owners, owner)
		manualPolicy := existing.ManualBackfill
		if owner == MembershipOwnerManual {
			manualPolicy = policy
		}
		effectivePolicy := effectiveMembershipBackfillPolicy(owners, manualPolicy)
		if !changed && effectivePolicy == existing.BackfillPolicy {
			return *existing, nil
		}
		patch := MembershipPatch{}
		if changed {
			patch.Owners = owners
			patch.SetOwners = true
		}
		if owner == MembershipOwnerManual && manualPolicy != existing.ManualBackfill {
			patch.Manual = string(manualPolicy)
			patch.SetManual = true
		}
		if effectivePolicy != existing.BackfillPolicy {
			patch.Backfill = effectivePolicy
			patch.SetBackfill = true
		}
		if err := w.PatchMembership(existing.ID, patch); err != nil {
			return ConversationMembershipRecord{}, fmt.Errorf("update membership owners: %w", err)
		}
		return s.backend.RefetchMembership(existing.ID)
	}
	joinedSequence := state.NextSequence - 1
	if joinedSequence < 0 {
		joinedSequence = 0
	}
	return w.CreateMembership(MembershipCreate{
		Ref:            ref,
		SessionID:      sessionID,
		JoinedAt:       now,
		JoinedSequence: joinedSequence,
		Backfill:       effectiveMembershipBackfillPolicy([]MembershipOwner{owner}, policy),
		ManualBackfill: manualBackfillMetadataValue(owner, policy),
		Owners:         []MembershipOwner{owner},
		Meta:           input.Metadata,
	})
}

func (s *transcriptService) removeMembershipLocked(input RemoveMembershipInput) error {
	ref, err := validateConversationRef(input.Conversation)
	if err != nil {
		return err
	}
	sessionID := strings.TrimSpace(input.SessionID)
	if sessionID == "" {
		return fmt.Errorf("%w: session_id required", ErrInvalidInput)
	}
	owner, err := normalizeMembershipOwner(input.Owner)
	if err != nil {
		return err
	}
	now := zeroNow(input.Now)
	existing, err := s.findActiveMembershipLocked(ref, sessionID)
	if err != nil {
		return err
	}
	if existing == nil {
		return nil
	}
	owners := existing.Owners
	if len(owners) == 0 {
		owners = []MembershipOwner{owner}
	}
	nextOwners, changed := removeMembershipOwner(owners, owner)
	if !changed {
		return nil
	}
	if len(nextOwners) > 0 {
		patch := MembershipPatch{
			Owners:    nextOwners,
			SetOwners: true,
		}
		nextManualPolicy := existing.ManualBackfill
		if owner == MembershipOwnerManual {
			nextManualPolicy = ""
			patch.Manual = ""
			patch.SetManual = true
		}
		nextPolicy := effectiveMembershipBackfillPolicy(nextOwners, nextManualPolicy)
		if nextPolicy != existing.BackfillPolicy {
			patch.Backfill = nextPolicy
			patch.SetBackfill = true
		}
		if err := s.backend.Writer().PatchMembership(existing.ID, patch); err != nil {
			return fmt.Errorf("update membership owners: %w", err)
		}
		return nil
	}
	return s.backend.CloseMembership(existing.ID, now)
}

func (s *transcriptService) ListMemberships(ctx context.Context, caller Caller, ref ConversationRef) ([]ConversationMembershipRecord, error) {
	if err := checkContext(ctx); err != nil {
		return nil, err
	}
	if err := requireControllerCaller(caller); err != nil {
		return nil, err
	}
	ref, err := validateConversationRef(ref)
	if err != nil {
		return nil, err
	}
	records, err := s.backend.OpenMembershipsByConversation(ref)
	if err != nil {
		return nil, err
	}
	out := make([]ConversationMembershipRecord, 0, len(records))
	seen := make(map[string]ConversationMembershipRecord)
	for _, record := range records {
		if existing, ok := seen[record.SessionID]; ok {
			return nil, fmt.Errorf("%w: duplicate memberships for session %s (%s, %s)", ErrInvariantViolation, record.SessionID, existing.ID, record.ID)
		}
		seen[record.SessionID] = record
		out = append(out, record)
	}
	slices.SortFunc(out, func(a, b ConversationMembershipRecord) int {
		return strings.Compare(a.SessionID, b.SessionID)
	})
	return out, nil
}

func (s *transcriptService) ListConversationsBySession(ctx context.Context, caller Caller, sessionID string) ([]ConversationMembershipRecord, error) {
	if err := checkContext(ctx); err != nil {
		return nil, err
	}
	if err := requireControllerCaller(caller); err != nil {
		return nil, err
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil, nil
	}
	records, err := s.backend.OpenMembershipsBySession(sessionID)
	if err != nil {
		return nil, err
	}
	out := make([]ConversationMembershipRecord, 0, len(records))
	seen := make(map[string]bool)
	for _, record := range records {
		key := conversationLockKey(record.Conversation)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, record)
	}
	sortConversationRefsFromMemberships(out)
	return out, nil
}

func (s *transcriptService) ListBackfill(ctx context.Context, input ListBackfillInput) ([]ConversationTranscriptRecord, error) {
	if err := checkContext(ctx); err != nil {
		return nil, err
	}
	if err := requireControllerCaller(input.Caller); err != nil {
		return nil, err
	}
	ref, err := validateConversationRef(input.Conversation)
	if err != nil {
		return nil, err
	}
	sessionID := strings.TrimSpace(input.SessionID)
	if sessionID == "" {
		return nil, fmt.Errorf("%w: session_id required", ErrInvalidInput)
	}
	limit := clampTranscriptLimit(input.Limit)
	var out []ConversationTranscriptRecord
	err = withBindingLock(s.locks, ref, func() error {
		state, err := s.ensureStateLocked(ref)
		if err != nil {
			return err
		}
		if state.HydrationStatus == HydrationPending {
			return ErrHydrationPending
		}
		membership, err := s.findActiveMembershipLocked(ref, sessionID)
		if err != nil {
			return err
		}
		if membership == nil {
			return ErrMembershipNotFound
		}
		after := membership.LastReadSequence
		if after == 0 && membership.BackfillPolicy == MembershipBackfillSinceJoin {
			after = membership.JoinedSequence
		}
		out, err = s.listTranscriptLocked(ref, after, limit, TranscriptOrderAsc)
		return err
	})
	return out, err
}

func (s *transcriptService) Ack(ctx context.Context, input AckMembershipInput) error {
	if err := checkContext(ctx); err != nil {
		return err
	}
	if err := requireControllerCaller(input.Caller); err != nil {
		return err
	}
	ref, err := validateConversationRef(input.Conversation)
	if err != nil {
		return err
	}
	sessionID := strings.TrimSpace(input.SessionID)
	if sessionID == "" {
		return fmt.Errorf("%w: session_id required", ErrInvalidInput)
	}
	if input.Sequence < 0 {
		return fmt.Errorf("%w: sequence must be non-negative", ErrInvalidInput)
	}
	return withBindingLock(s.locks, ref, func() error {
		state, err := s.ensureStateLocked(ref)
		if err != nil {
			return err
		}
		maxSequence := state.NextSequence - 1
		if input.Sequence > maxSequence {
			return fmt.Errorf("%w: ack sequence %d exceeds head %d", ErrInvalidInput, input.Sequence, maxSequence)
		}
		membership, err := s.findActiveMembershipLocked(ref, sessionID)
		if err != nil {
			return err
		}
		if membership == nil {
			return ErrMembershipNotFound
		}
		if input.Sequence <= membership.LastReadSequence {
			return nil
		}
		return s.backend.SetMembershipLastRead(membership.ID, input.Sequence)
	})
}

func (s *transcriptService) BeginHydration(ctx context.Context, caller Caller, ref ConversationRef, metadata map[string]string) (ConversationTranscriptStateRecord, error) {
	if err := checkContext(ctx); err != nil {
		return ConversationTranscriptStateRecord{}, err
	}
	ref, err := validateConversationRef(ref)
	if err != nil {
		return ConversationTranscriptStateRecord{}, err
	}
	if err := authorizeMutation(caller, ref); err != nil {
		return ConversationTranscriptStateRecord{}, err
	}
	var out ConversationTranscriptStateRecord
	err = withBindingLock(s.locks, ref, func() error {
		state, err := s.ensureStateLocked(ref)
		if err != nil {
			return err
		}
		if state.NextSequence > 1 && state.HydrationStatus != HydrationPending {
			return fmt.Errorf("%w: cannot begin hydration after live traffic", ErrInvalidInput)
		}
		pending := HydrationPending
		if err := s.backend.PatchTranscriptState(state.ID, StatePatch{
			Hydration: &pending,
			Meta:      metadata,
		}); err != nil {
			return fmt.Errorf("update hydration state: %w", err)
		}
		out, err = s.backend.RefetchTranscriptState(state.ID)
		return err
	})
	return out, err
}

func (s *transcriptService) CompleteHydration(ctx context.Context, caller Caller, ref ConversationRef) (ConversationTranscriptStateRecord, error) {
	return s.updateHydrationState(ctx, caller, ref, HydrationComplete, nil)
}

func (s *transcriptService) MarkHydrationFailed(ctx context.Context, caller Caller, ref ConversationRef, metadata map[string]string) (ConversationTranscriptStateRecord, error) {
	return s.updateHydrationState(ctx, caller, ref, HydrationFailed, metadata)
}

func (s *transcriptService) State(ctx context.Context, caller Caller, ref ConversationRef) (*ConversationTranscriptStateRecord, error) {
	if err := checkContext(ctx); err != nil {
		return nil, err
	}
	ref, err := validateConversationRef(ref)
	if err != nil {
		return nil, err
	}
	if err := authorizeMutation(caller, ref); err != nil {
		return nil, err
	}
	var out *ConversationTranscriptStateRecord
	err = withBindingLock(s.locks, ref, func() error {
		state, err := s.findStateLocked(ref)
		if err != nil {
			return err
		}
		if state != nil {
			rec := *state
			out = &rec
		}
		return nil
	})
	return out, err
}

func (s *transcriptService) updateHydrationState(ctx context.Context, caller Caller, ref ConversationRef, status HydrationStatus, metadata map[string]string) (ConversationTranscriptStateRecord, error) {
	if err := checkContext(ctx); err != nil {
		return ConversationTranscriptStateRecord{}, err
	}
	switch status {
	case HydrationComplete, HydrationFailed:
	default:
		return ConversationTranscriptStateRecord{}, fmt.Errorf("%w: invalid hydration transition %q", ErrInvalidInput, status)
	}
	ref, err := validateConversationRef(ref)
	if err != nil {
		return ConversationTranscriptStateRecord{}, err
	}
	if err := authorizeMutation(caller, ref); err != nil {
		return ConversationTranscriptStateRecord{}, err
	}
	var out ConversationTranscriptStateRecord
	err = withBindingLock(s.locks, ref, func() error {
		state, err := s.ensureStateLocked(ref)
		if err != nil {
			return err
		}
		if state.HydrationStatus != HydrationPending {
			return fmt.Errorf("%w: hydration transition requires pending status", ErrInvalidInput)
		}
		if err := s.backend.PatchTranscriptState(state.ID, StatePatch{
			Hydration: &status,
			Meta:      metadata,
		}); err != nil {
			return fmt.Errorf("update hydration state: %w", err)
		}
		out, err = s.backend.RefetchTranscriptState(state.ID)
		return err
	})
	return out, err
}

func (s *transcriptService) ensureStateLocked(ref ConversationRef) (ConversationTranscriptStateRecord, error) {
	return s.ensureStateLockedWriter(s.backend.Writer(), ref)
}

// ensureStateLockedWriter is ensureStateLocked with its create routed through
// w, so a first-touch transcript-state record can be created inside the same
// commit as the membership and binding writes it accompanies.
func (s *transcriptService) ensureStateLockedWriter(w FabricWriter, ref ConversationRef) (ConversationTranscriptStateRecord, error) {
	state, err := s.findStateLocked(ref)
	if err != nil {
		return ConversationTranscriptStateRecord{}, err
	}
	if state != nil {
		return *state, nil
	}
	return w.CreateTranscriptState(ref)
}

func (s *transcriptService) findStateLocked(ref ConversationRef) (*ConversationTranscriptStateRecord, error) {
	records, err := s.backend.OpenTranscriptStates(ref)
	if err != nil {
		return nil, err
	}
	var out *ConversationTranscriptStateRecord
	for _, record := range records {
		if out != nil {
			return nil, fmt.Errorf("%w: multiple transcript states for %s", ErrInvariantViolation, conversationLockKey(ref))
		}
		rec := record
		out = &rec
	}
	return out, nil
}

func (s *transcriptService) findTranscriptByProviderMessageLocked(ref ConversationRef, providerMessageID string) (*ConversationTranscriptRecord, error) {
	records, err := s.backend.OpenTranscriptsByProviderMessage(ref, providerMessageID)
	if err != nil {
		return nil, err
	}
	var out *ConversationTranscriptRecord
	for _, record := range records {
		if out != nil {
			return nil, fmt.Errorf("%w: duplicate transcript provider message %q", ErrInvariantViolation, providerMessageID)
		}
		rec := record
		out = &rec
	}
	return out, nil
}

func (s *transcriptService) findActiveMembershipLocked(ref ConversationRef, sessionID string) (*ConversationMembershipRecord, error) {
	records, err := s.backend.OpenMembershipsExact(ref, sessionID)
	if err != nil {
		return nil, err
	}
	var out *ConversationMembershipRecord
	for _, record := range records {
		if out != nil {
			return nil, fmt.Errorf("%w: duplicate memberships for session %s", ErrInvariantViolation, sessionID)
		}
		rec := record
		out = &rec
	}
	return out, nil
}

func (s *transcriptService) listTranscriptLocked(ref ConversationRef, after int64, limit int, order TranscriptOrder) ([]ConversationTranscriptRecord, error) {
	state, err := s.ensureStateLocked(ref)
	if err != nil {
		return nil, err
	}
	if limit <= 0 {
		return nil, nil
	}
	startSeq := after + 1
	if startSeq < state.EarliestAvailableSequence {
		startSeq = state.EarliestAvailableSequence
	}
	endSeq := state.NextSequence - 1
	if startSeq > endSeq {
		return nil, nil
	}
	return s.backend.ListTranscript(ref, after, startSeq, endSeq, limit, order == TranscriptOrderDesc)
}

// sortTranscriptRecords orders records by sequence (ID as tiebreaker),
// ascending by default or descending when newest-first is requested.
func sortTranscriptRecords(records []ConversationTranscriptRecord, descending bool) {
	slices.SortFunc(records, func(a, b ConversationTranscriptRecord) int {
		cmp := compareTranscriptRecords(a, b)
		if descending {
			return -cmp
		}
		return cmp
	})
}

func compareTranscriptRecords(a, b ConversationTranscriptRecord) int {
	switch {
	case a.Sequence < b.Sequence:
		return -1
	case a.Sequence > b.Sequence:
		return 1
	default:
		return strings.Compare(a.ID, b.ID)
	}
}

func decodeTranscriptBead(b beads.Bead) (ConversationTranscriptRecord, error) {
	ref, err := conversationRefFromMetadata(b.Metadata)
	if err != nil {
		return ConversationTranscriptRecord{}, err
	}
	createdAt, err := parseTime(b.Metadata, "created_at")
	if err != nil {
		return ConversationTranscriptRecord{}, err
	}
	actor, err := decodeOptionalActor(b.Metadata["actor_json"])
	if err != nil {
		return ConversationTranscriptRecord{}, err
	}
	attachments, err := decodeOptionalAttachments(b.Metadata["attachments_json"])
	if err != nil {
		return ConversationTranscriptRecord{}, err
	}
	return ConversationTranscriptRecord{
		ID:                b.ID,
		SchemaVersion:     parseInt(b.Metadata, "schema_version"),
		Conversation:      ref,
		Sequence:          parseInt64(b.Metadata, "sequence"),
		Kind:              TranscriptMessageKind(strings.TrimSpace(b.Metadata["kind"])),
		Provenance:        TranscriptProvenance(strings.TrimSpace(b.Metadata["provenance"])),
		ProviderMessageID: strings.TrimSpace(b.Metadata["provider_message_id"]),
		Actor:             actor,
		Text:              b.Description,
		ExplicitTarget:    normalizeHandle(b.Metadata["explicit_target"]),
		ReplyToMessageID:  strings.TrimSpace(b.Metadata["reply_to_message_id"]),
		Attachments:       attachments,
		SourceSessionID:   strings.TrimSpace(b.Metadata["source_session_id"]),
		CreatedAt:         createdAt,
		Metadata:          decodePrefixedMetadata(b.Metadata),
	}, nil
}

func decodeMembershipBead(b beads.Bead) (ConversationMembershipRecord, error) {
	ref, err := conversationRefFromMetadata(b.Metadata)
	if err != nil {
		return ConversationMembershipRecord{}, err
	}
	joinedAt, err := parseTime(b.Metadata, "joined_at")
	if err != nil {
		return ConversationMembershipRecord{}, err
	}
	owners, err := decodeMembershipOwners(b.Metadata["membership_owner_kinds"])
	if err != nil {
		return ConversationMembershipRecord{}, err
	}
	return ConversationMembershipRecord{
		ID:               b.ID,
		SchemaVersion:    parseInt(b.Metadata, "schema_version"),
		Conversation:     ref,
		SessionID:        strings.TrimSpace(b.Metadata["session_id"]),
		JoinedAt:         joinedAt,
		JoinedSequence:   parseInt64(b.Metadata, "joined_sequence"),
		LastReadSequence: parseInt64(b.Metadata, "last_read_sequence"),
		BackfillPolicy:   MembershipBackfillPolicy(strings.TrimSpace(b.Metadata["membership_backfill_policy"])),
		ManualBackfill:   MembershipBackfillPolicy(strings.TrimSpace(b.Metadata["manual_backfill_policy"])),
		Owners:           owners,
		Metadata:         decodePrefixedMetadata(b.Metadata),
	}, nil
}

func decodeTranscriptStateBead(b beads.Bead) (ConversationTranscriptStateRecord, error) {
	ref, err := conversationRefFromMetadata(b.Metadata)
	if err != nil {
		return ConversationTranscriptStateRecord{}, err
	}
	return ConversationTranscriptStateRecord{
		ID:                        b.ID,
		SchemaVersion:             parseInt(b.Metadata, "schema_version"),
		Conversation:              ref,
		NextSequence:              parseInt64(b.Metadata, "next_sequence"),
		EarliestAvailableSequence: parseInt64(b.Metadata, "earliest_available_sequence"),
		HydrationStatus:           HydrationStatus(strings.TrimSpace(b.Metadata["hydration_status"])),
		OldestHydratedMessageID:   strings.TrimSpace(b.Metadata["oldest_hydrated_message_id"]),
		MaxRetainedEntries:        parseInt(b.Metadata, "max_retained_entries"),
		Metadata:                  decodePrefixedMetadata(b.Metadata),
	}, nil
}

func normalizeBackfillPolicy(policy MembershipBackfillPolicy) (MembershipBackfillPolicy, error) {
	switch MembershipBackfillPolicy(strings.ToLower(strings.TrimSpace(string(policy)))) {
	case "", MembershipBackfillAll:
		return MembershipBackfillAll, nil
	case MembershipBackfillSinceJoin:
		return MembershipBackfillSinceJoin, nil
	default:
		return "", fmt.Errorf("%w: invalid backfill policy %q", ErrInvalidInput, policy)
	}
}

func normalizeMembershipOwner(owner MembershipOwner) (MembershipOwner, error) {
	switch MembershipOwner(strings.ToLower(strings.TrimSpace(string(owner)))) {
	case "", MembershipOwnerManual:
		return MembershipOwnerManual, nil
	case MembershipOwnerBinding:
		return MembershipOwnerBinding, nil
	case MembershipOwnerGroup:
		return MembershipOwnerGroup, nil
	default:
		return "", fmt.Errorf("%w: invalid membership owner %q", ErrInvalidInput, owner)
	}
}

func decodeMembershipOwners(raw string) ([]MembershipOwner, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	parts := strings.Split(raw, ",")
	owners := make([]MembershipOwner, 0, len(parts))
	seen := make(map[MembershipOwner]struct{}, len(parts))
	for _, part := range parts {
		owner, err := normalizeMembershipOwner(MembershipOwner(part))
		if err != nil {
			return nil, err
		}
		if _, ok := seen[owner]; ok {
			continue
		}
		seen[owner] = struct{}{}
		owners = append(owners, owner)
	}
	slices.SortFunc(owners, func(a, b MembershipOwner) int {
		return strings.Compare(string(a), string(b))
	})
	return owners, nil
}

func encodeMembershipOwners(owners []MembershipOwner) string {
	normalized := make([]MembershipOwner, 0, len(owners))
	seen := make(map[MembershipOwner]struct{}, len(owners))
	for _, owner := range owners {
		normalizedOwner, err := normalizeMembershipOwner(owner)
		if err != nil {
			continue
		}
		if _, ok := seen[normalizedOwner]; ok {
			continue
		}
		seen[normalizedOwner] = struct{}{}
		normalized = append(normalized, normalizedOwner)
	}
	slices.SortFunc(normalized, func(a, b MembershipOwner) int {
		return strings.Compare(string(a), string(b))
	})
	parts := make([]string, 0, len(normalized))
	for _, owner := range normalized {
		parts = append(parts, string(owner))
	}
	return strings.Join(parts, ",")
}

func addMembershipOwner(owners []MembershipOwner, owner MembershipOwner) ([]MembershipOwner, bool) {
	normalizedOwner, err := normalizeMembershipOwner(owner)
	if err != nil {
		return owners, false
	}
	normalized := make([]MembershipOwner, 0, len(owners)+1)
	seen := false
	for _, existing := range owners {
		current, err := normalizeMembershipOwner(existing)
		if err != nil {
			continue
		}
		normalized = append(normalized, current)
		if current == normalizedOwner {
			seen = true
		}
	}
	if seen {
		return uniqueMembershipOwners(normalized), false
	}
	normalized = append(normalized, normalizedOwner)
	return uniqueMembershipOwners(normalized), true
}

func removeMembershipOwner(owners []MembershipOwner, owner MembershipOwner) ([]MembershipOwner, bool) {
	normalizedOwner, err := normalizeMembershipOwner(owner)
	if err != nil {
		return owners, false
	}
	normalized := make([]MembershipOwner, 0, len(owners))
	removed := false
	for _, existing := range owners {
		current, err := normalizeMembershipOwner(existing)
		if err != nil {
			continue
		}
		if current == normalizedOwner {
			removed = true
			continue
		}
		normalized = append(normalized, current)
	}
	return uniqueMembershipOwners(normalized), removed
}

func uniqueMembershipOwners(owners []MembershipOwner) []MembershipOwner {
	if len(owners) == 0 {
		return nil
	}
	seen := make(map[MembershipOwner]struct{}, len(owners))
	normalized := make([]MembershipOwner, 0, len(owners))
	for _, owner := range owners {
		current, err := normalizeMembershipOwner(owner)
		if err != nil {
			continue
		}
		if _, ok := seen[current]; ok {
			continue
		}
		seen[current] = struct{}{}
		normalized = append(normalized, current)
	}
	slices.SortFunc(normalized, func(a, b MembershipOwner) int {
		return strings.Compare(string(a), string(b))
	})
	return normalized
}

func effectiveMembershipBackfillPolicy(owners []MembershipOwner, manualPolicy MembershipBackfillPolicy) MembershipBackfillPolicy {
	normalizedOwners := uniqueMembershipOwners(owners)
	for _, owner := range normalizedOwners {
		if owner == MembershipOwnerGroup {
			return MembershipBackfillAll
		}
	}
	for _, owner := range normalizedOwners {
		if owner == MembershipOwnerManual {
			policy, err := normalizeBackfillPolicy(manualPolicy)
			if err == nil {
				return policy
			}
			return MembershipBackfillAll
		}
	}
	for _, owner := range normalizedOwners {
		if owner == MembershipOwnerBinding {
			return MembershipBackfillSinceJoin
		}
	}
	policy, err := normalizeBackfillPolicy(manualPolicy)
	if err == nil {
		return policy
	}
	return MembershipBackfillAll
}

func manualBackfillMetadataValue(owner MembershipOwner, policy MembershipBackfillPolicy) string {
	if owner != MembershipOwnerManual {
		return ""
	}
	return string(policy)
}

func requireControllerCaller(caller Caller) error {
	if normalizeCaller(caller).Kind != CallerController {
		return ErrUnauthorized
	}
	return nil
}

// normalizeTranscriptOrder validates the requested order, defaulting empty to
// ascending (oldest-first) for backwards compatibility.
func normalizeTranscriptOrder(order TranscriptOrder) (TranscriptOrder, error) {
	switch order {
	case "", TranscriptOrderAsc:
		return TranscriptOrderAsc, nil
	case TranscriptOrderDesc:
		return TranscriptOrderDesc, nil
	default:
		return "", fmt.Errorf("%w: invalid transcript order %q", ErrInvalidInput, order)
	}
}

func clampTranscriptLimit(limit int) int {
	switch {
	case limit <= 0:
		return defaultTranscriptPageSize
	case limit > maxTranscriptPageSize:
		return maxTranscriptPageSize
	default:
		return limit
	}
}

func transcriptBucket(sequence int64) int64 {
	if sequence <= 0 {
		return 0
	}
	return (sequence - 1) / transcriptBucketSize
}

func marshalOptionalJSON(v any) (string, error) {
	data, err := json.Marshal(v)
	if err != nil {
		return "", fmt.Errorf("marshal json: %w", err)
	}
	if string(data) == "null" || string(data) == "[]" || string(data) == "{}" {
		return "", nil
	}
	return string(data), nil
}

func decodeOptionalActor(raw string) (ExternalActor, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ExternalActor{}, nil
	}
	var actor ExternalActor
	if err := json.Unmarshal([]byte(raw), &actor); err != nil {
		return ExternalActor{}, fmt.Errorf("decode actor_json: %w", err)
	}
	return actor, nil
}

func decodeOptionalAttachments(raw string) ([]ExternalAttachment, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	var attachments []ExternalAttachment
	if err := json.Unmarshal([]byte(raw), &attachments); err != nil {
		return nil, fmt.Errorf("decode attachments_json: %w", err)
	}
	return attachments, nil
}

func sortConversationRefsFromMemberships(memberships []ConversationMembershipRecord) {
	slices.SortFunc(memberships, func(a, b ConversationMembershipRecord) int {
		ka := conversationLockKey(a.Conversation)
		kb := conversationLockKey(b.Conversation)
		switch {
		case ka < kb:
			return -1
		case ka > kb:
			return 1
		default:
			return strings.Compare(a.ID, b.ID)
		}
	})
}
