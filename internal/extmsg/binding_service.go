package extmsg

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/gastownhall/gascity/internal/beads"
)

const defaultTouchDebounce = 30 * time.Second

type bindingLockEntry struct {
	mu   sync.Mutex
	refs int
}

type bindingLockPool struct {
	mu    sync.Mutex
	locks map[string]*bindingLockEntry
}

var sharedBindingLockPools sync.Map

type bindingCleaner interface {
	ClearForConversation(ctx context.Context, sessionID string, ref ConversationRef) error
}

type bindingMembershipEnsurer interface {
	EnsureMembership(ctx context.Context, input EnsureMembershipInput) (ConversationMembershipRecord, error)
	RemoveMembership(ctx context.Context, input RemoveMembershipInput) error
	ensureMembershipLocked(input EnsureMembershipInput) (ConversationMembershipRecord, error)
	ensureMembershipLockedWriter(w FabricWriter, input EnsureMembershipInput) (ConversationMembershipRecord, error)
	removeMembershipLocked(input RemoveMembershipInput) error
}

type bindingService struct {
	backend fabricBackend
	// sessionStore is the store holding session-class beads, read for
	// session-liveness resolution (sessionNameForSelector /
	// overlayLiveSession). Identical to the record store on a single-store
	// city; distinct once [beads.classes.messaging] or
	// [beads.classes.sessions] relocates a class. Record mutations never
	// touch it.
	sessionStore  beads.Store
	delivery      bindingCleaner
	transcript    bindingMembershipEnsurer
	touchDebounce time.Duration
	locks         *bindingLockPool
}

// BindingServiceOption configures a binding service instance.
type BindingServiceOption func(*bindingService)

// WithBindingTouchDebounce sets the minimum interval between touch updates.
func WithBindingTouchDebounce(d time.Duration) BindingServiceOption {
	return func(s *bindingService) {
		if d > 0 {
			s.touchDebounce = d
		}
	}
}

func newBindingService(store, sessionStore beads.Store, delivery bindingCleaner, transcript bindingMembershipEnsurer, locks *bindingLockPool, opts ...BindingServiceOption) BindingService {
	if sessionStore == nil {
		sessionStore = store
	}
	return newBindingServiceWithBackend(newBeadBackend(store), sessionStore, delivery, transcript, locks, opts...)
}

func newBindingServiceWithBackend(backend fabricBackend, sessionStore beads.Store, delivery bindingCleaner, transcript bindingMembershipEnsurer, locks *bindingLockPool, opts ...BindingServiceOption) BindingService {
	svc := &bindingService{
		backend:       backend,
		sessionStore:  sessionStore,
		touchDebounce: defaultTouchDebounce,
		locks:         locks,
	}
	if delivery != nil {
		svc.delivery = delivery
	}
	if transcript != nil {
		svc.transcript = transcript
	}
	for _, opt := range opts {
		if opt != nil {
			opt(svc)
		}
	}
	return svc
}

// bindTarget is the resolved endpoint a Bind call creates a binding for:
// exactly one of sessionID/agentName is set, and sessionName is the stable
// session identity captured for session bindings (empty for agent bindings or
// when no live session bead resolved). It bundles the three values the bind
// helpers thread together so the create/rebind/handoff paths share one shape.
type bindTarget struct {
	sessionID   string
	agentName   string
	sessionName string
}

func (s *bindingService) Bind(ctx context.Context, caller Caller, input BindInput) (SessionBindingRecord, error) {
	if err := checkContext(ctx); err != nil {
		return SessionBindingRecord{}, err
	}
	ref, err := validateConversationRef(input.Conversation)
	if err != nil {
		return SessionBindingRecord{}, err
	}
	if err := authorizeMutation(caller, ref); err != nil {
		return SessionBindingRecord{}, err
	}
	sessionID := strings.TrimSpace(input.SessionID)
	agentName := strings.TrimSpace(input.AgentName)
	switch {
	case sessionID == "" && agentName == "":
		return SessionBindingRecord{}, fmt.Errorf("%w: session_id or agent_name required", ErrInvalidInput)
	case sessionID != "" && agentName != "":
		return SessionBindingRecord{}, fmt.Errorf("%w: session_id and agent_name are mutually exclusive", ErrInvalidInput)
	}
	// Capture the target's stable session name so the binding survives respawn.
	// Best-effort: empty when the selector resolves to no session bead.
	target := bindTarget{
		sessionID:   sessionID,
		agentName:   agentName,
		sessionName: sessionNameForSelector(s.sessionStore, sessionID),
	}
	now := zeroNow(input.Now)

	var out SessionBindingRecord
	err = withBindingLock(s.locks, ref, func() error {
		if err := checkContext(ctx); err != nil {
			return err
		}
		history, err := s.listBindingsForConversation(ref)
		if err != nil {
			return err
		}
		active, err := s.activeBinding(ctx, history, now)
		if err != nil {
			return err
		}
		switch {
		case active != nil && (active.SessionID != sessionID || active.AgentName != agentName):
			if !input.Replace {
				return fmt.Errorf("%w: conversation already bound to %s", ErrBindingConflict, bindingTarget(*active))
			}
			out, err = s.handoffActiveBindingLocked(ctx, caller, *active, ref, target, input, history, now)
			return err
		case active != nil:
			out, err = s.rebindActiveLocked(caller, ref, *active, target, input, now)
			return err
		default:
			out, err = s.createBindingLocked(caller, ref, target, input, history, now, "")
			return err
		}
	})
	if err != nil {
		return SessionBindingRecord{}, err
	}
	return out, nil
}

// bindMembershipWrites returns the membership sub-write callback a binding
// commit runs: it re-ensures the binding-owned transcript membership through
// the commit's writer. Nil when no transcript service is wired.
func (s *bindingService) bindMembershipWrites(caller Caller, ref ConversationRef, membershipKey string, now time.Time) func(FabricWriter) error {
	if s.transcript == nil {
		return nil
	}
	return func(w FabricWriter) error {
		if _, err := s.transcript.ensureMembershipLockedWriter(w, EnsureMembershipInput{
			Caller:         caller,
			Conversation:   ref,
			SessionID:      membershipKey,
			BackfillPolicy: MembershipBackfillSinceJoin,
			Owner:          MembershipOwnerBinding,
			Now:            now,
		}); err != nil {
			return wrapTranscriptSyncError("ensure transcript membership after bind", err)
		}
		return nil
	}
}

// createBindingLocked creates a fresh binding for ref under the conversation
// lock. It coalesces the binding record, its transcript membership, and an
// optional displaced-binding close into a single commit so a bind costs one
// DOLT_COMMIT (gastownhall/gascity#3735). When displaceID is non-empty the
// displaced binding is closed in the SAME commit; on a backend with atomic
// transactions (the only caller that passes displaceID, see
// handoffActiveBindingLocked) a failure creating the replacement or its
// membership rolls the whole swap back and leaves the displaced binding active,
// so the conversation is never left unbound. history supplies the next
// generation.
func (s *bindingService) createBindingLocked(caller Caller, ref ConversationRef, target bindTarget, input BindInput, history []SessionBindingRecord, now time.Time, displaceID string) (SessionBindingRecord, error) {
	normalized := normalizeCaller(caller)
	membershipKey := bindingMembershipKey(SessionBindingRecord{SessionID: target.sessionID, AgentName: target.agentName})
	out, err := s.backend.CreateBinding(BindingCreate{
		Ref:           ref,
		SessionID:     target.sessionID,
		SessionName:   target.sessionName,
		AgentName:     target.agentName,
		Generation:    nextBindingGeneration(history),
		BoundAt:       now,
		ExpiresAt:     input.ExpiresAt,
		CreatedByKind: normalized.Kind,
		CreatedByID:   normalized.ID,
		Meta:          input.Metadata,
	}, displaceID, s.bindMembershipWrites(caller, ref, membershipKey, now))
	if err != nil {
		return SessionBindingRecord{}, err
	}
	return out, nil
}

// rebindActiveLocked refreshes the already-active binding when Bind targets the
// same endpoint: it backfills a now-known session name, updates binding
// metadata, and re-ensures transcript membership, coalescing all writes into a
// single commit (gastownhall/gascity#3735).
func (s *bindingService) rebindActiveLocked(caller Caller, ref ConversationRef, active SessionBindingRecord, target bindTarget, input BindInput, now time.Time) (SessionBindingRecord, error) {
	refresh := BindingRefresh{
		ExpiresAt: input.ExpiresAt,
		TouchedAt: now,
		Meta:      input.Metadata,
	}
	if active.SessionName == "" && target.sessionName != "" {
		refresh.SessionNameBackfill = target.sessionName
	}
	if err := s.backend.RefreshBinding(ref, active.ID, refresh, s.bindMembershipWrites(caller, ref, bindingMembershipKey(active), now)); err != nil {
		return SessionBindingRecord{}, err
	}
	return s.getBinding(active.ID)
}

// handoffActiveBindingLocked swaps the conversation's active binding from
// displaced to the caller's new target, all under the conversation's binding
// lock. Delivery contexts for the displaced session are cleared first, so a
// clear failure leaves the displaced binding fully intact and the whole handoff
// retryable (the same end-then-clear ordering the expiry path uses). The
// displaced binding's transcript membership is dropped before the swap and
// re-ensured if the swap fails while the displaced binding is still active.
//
// How the close-of-displaced and create-of-replacement are sequenced depends on
// the backend's transaction guarantee, because a handoff is the first write pair
// whose atomicity is a correctness requirement rather than a commit-count
// optimization:
//
//   - On a backend with atomic transactions, both happen in one commit that
//     rolls back as a unit (see createBindingLocked). A failure leaves the
//     displaced binding active and creates no replacement.
//   - On a non-atomic backend, a single commit cannot do both atomically and
//     partial writes persist on failure, so the displaced binding is closed
//     first as its own write and the replacement is created after. A mid-swap
//     failure can then only leave the conversation unbound (recoverable by a
//     fresh bind) or bound to the new target without transcript membership
//     (recovered by the next same-target rebind) — never two active bindings at
//     once, which selectActiveBinding rejects as an unrecoverable invariant
//     violation.
func (s *bindingService) handoffActiveBindingLocked(ctx context.Context, caller Caller, displaced SessionBindingRecord, ref ConversationRef, target bindTarget, input BindInput, history []SessionBindingRecord, now time.Time) (SessionBindingRecord, error) {
	if s.delivery != nil && displaced.SessionID != "" {
		if err := s.delivery.ClearForConversation(ctx, displaced.SessionID, displaced.Conversation); err != nil {
			return SessionBindingRecord{}, err
		}
	}
	if err := s.backend.TouchBinding(displaced.ID, now); err != nil {
		return SessionBindingRecord{}, fmt.Errorf("update binding %s metadata: %w", displaced.ID, err)
	}
	if s.transcript != nil {
		if err := s.transcript.removeMembershipLocked(RemoveMembershipInput{
			Caller:       caller,
			Conversation: displaced.Conversation,
			SessionID:    bindingMembershipKey(displaced),
			Owner:        MembershipOwnerBinding,
			Now:          now,
		}); err != nil {
			return SessionBindingRecord{}, wrapTranscriptSyncError("remove transcript membership after unbind", err)
		}
	}

	if s.backend.AtomicTx() {
		out, err := s.createBindingLocked(caller, ref, target, input, history, now, displaced.ID)
		if err != nil {
			// The swap rolled back, so the displaced binding is still active —
			// restore its membership to match.
			return SessionBindingRecord{}, s.restoreDisplacedMembershipLocked(caller, displaced, now, err)
		}
		return out, nil
	}

	if err := s.backend.CloseBinding(displaced.ID); err != nil {
		// The displaced binding is still active because the close did not land —
		// restore its membership to match.
		return SessionBindingRecord{}, s.restoreDisplacedMembershipLocked(caller, displaced, now,
			fmt.Errorf("close displaced binding %s: %w", displaced.ID, err))
	}
	out, err := s.createBindingLocked(caller, ref, target, input, history, now, "")
	if err != nil {
		// The displaced binding is already closed, so the conversation is unbound
		// or bound to a replacement still missing its membership; both states are
		// recoverable on retry. Do not re-open the displaced binding.
		return SessionBindingRecord{}, err
	}
	return out, nil
}

// restoreDisplacedMembershipLocked re-ensures the displaced binding's transcript
// membership after a failed swap that left the displaced binding active, and
// returns cause. If the restore itself fails, it joins that failure onto cause
// so a transcript that could not be put back is surfaced rather than silently
// dropped.
func (s *bindingService) restoreDisplacedMembershipLocked(caller Caller, displaced SessionBindingRecord, now time.Time, cause error) error {
	if s.transcript == nil {
		return cause
	}
	if _, err := s.transcript.ensureMembershipLocked(EnsureMembershipInput{
		Caller:         caller,
		Conversation:   displaced.Conversation,
		SessionID:      bindingMembershipKey(displaced),
		BackfillPolicy: MembershipBackfillSinceJoin,
		Owner:          MembershipOwnerBinding,
		Now:            now,
	}); err != nil {
		return errors.Join(cause, wrapTranscriptSyncError("restore displaced transcript membership after failed handoff", err))
	}
	return cause
}

func (s *bindingService) ResolveByConversation(ctx context.Context, ref ConversationRef) (*SessionBindingRecord, error) {
	if err := checkContext(ctx); err != nil {
		return nil, err
	}
	ref, err := validateConversationRef(ref)
	if err != nil {
		return nil, err
	}
	record, err := resolveActiveBinding(ctx, s.locks, s.backend, s.delivery, s.transcript, ref, timeNow())
	if err != nil || record == nil {
		return record, err
	}
	overlayLiveSession(s.sessionStore, record)
	return record, nil
}

// overlayLiveSession re-points a binding record at its session's current live
// bead when the stored session_id has gone stale across a respawn. It mutates
// only the in-memory copy — persistent healing is the binding reaper's job.
//
// Both layers are intentional: this overlay corrects routing immediately after
// a respawn, before the next reconciler tick arrives. Without it, inbound
// traffic would resolve to the dead bead ID for up to one full reconciler
// interval. The reaper's persistent write is still needed to update the
// labelBindingSessionPrefix label (indexed on the volatile ID) and keep
// label-based lookups correct across ticks.
func overlayLiveSession(store beads.Store, record *SessionBindingRecord) {
	overlayLiveSessionID(store, record.SessionName, record.SessionID, &record.SessionID)
}

func (s *bindingService) ListBySession(ctx context.Context, sessionID string) ([]SessionBindingRecord, error) {
	if err := checkContext(ctx); err != nil {
		return nil, err
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil, nil
	}
	records, err := s.backend.ActiveBindingsBySession(sessionID)
	if err != nil {
		return nil, fmt.Errorf("list bindings by session label: %w", err)
	}
	seen := make(map[string]bool, len(records))
	out := make([]SessionBindingRecord, 0, len(records))
	for _, record := range records {
		if err := checkContext(ctx); err != nil {
			return nil, err
		}
		if record.SessionID != sessionID {
			continue
		}
		key := conversationLockKey(record.Conversation)
		if seen[key] {
			continue
		}
		seen[key] = true
		active, err := resolveActiveBinding(ctx, s.locks, s.backend, s.delivery, s.transcript, record.Conversation, timeNow())
		if err != nil {
			return nil, err
		}
		if active != nil && active.SessionID == sessionID {
			out = append(out, *active)
		}
	}
	return out, nil
}

func (s *bindingService) Touch(ctx context.Context, caller Caller, bindingID string, now time.Time) error {
	if err := checkContext(ctx); err != nil {
		return err
	}
	bindingID = strings.TrimSpace(bindingID)
	if bindingID == "" {
		return nil
	}
	record, lastTouched, err := s.backend.GetBinding(bindingID)
	if err != nil {
		return err
	}
	if err := authorizeMutation(caller, record.Conversation); err != nil {
		return err
	}
	if record.Status != BindingActive {
		return nil
	}
	now = zeroNow(now)
	if !lastTouched.IsZero() && now.Sub(lastTouched) < s.touchDebounce {
		return nil
	}
	return s.backend.TouchBinding(bindingID, now)
}

func (s *bindingService) Unbind(ctx context.Context, caller Caller, input UnbindInput) ([]SessionBindingRecord, error) {
	if err := checkContext(ctx); err != nil {
		return nil, err
	}
	now := zeroNow(input.Now)
	sessionID := strings.TrimSpace(input.SessionID)
	agentName := strings.TrimSpace(input.AgentName)
	if input.Conversation == nil && sessionID == "" && agentName == "" {
		return nil, fmt.Errorf("%w: conversation, session_id, or agent_name required", ErrInvalidInput)
	}
	matchesFilter := func(record SessionBindingRecord) bool {
		if sessionID != "" && record.SessionID != sessionID {
			return false
		}
		if agentName != "" && record.AgentName != agentName {
			return false
		}
		return true
	}

	var seeds []SessionBindingRecord
	if input.Conversation != nil {
		ref, err := validateConversationRef(*input.Conversation)
		if err != nil {
			return nil, err
		}
		if err := authorizeMutation(caller, ref); err != nil {
			return nil, err
		}
		history, err := s.listBindingsForConversation(ref)
		if err != nil {
			return nil, err
		}
		for _, record := range history {
			if record.Status != BindingActive {
				continue
			}
			if !matchesFilter(record) {
				continue
			}
			seeds = append(seeds, record)
		}
	} else {
		var records []SessionBindingRecord
		var err error
		if sessionID != "" {
			records, err = s.backend.ActiveBindingsBySession(sessionID)
		} else {
			records, err = s.backend.ActiveBindingsByAgent(agentName)
		}
		if err != nil {
			return nil, fmt.Errorf("list bindings by target label: %w", err)
		}
		for _, record := range records {
			if record.Status != BindingActive || !matchesFilter(record) {
				continue
			}
			if err := authorizeMutation(caller, record.Conversation); err != nil {
				return nil, err
			}
			seeds = append(seeds, record)
		}
	}
	if len(seeds) == 0 {
		return nil, nil
	}
	sortConversationRefs(seeds)

	closed := make([]SessionBindingRecord, 0, len(seeds))
	for _, seed := range seeds {
		err := withBindingLock(s.locks, seed.Conversation, func() error {
			if err := checkContext(ctx); err != nil {
				return err
			}
			history, err := s.listBindingsForConversation(seed.Conversation)
			if err != nil {
				return err
			}
			active, err := s.activeBinding(ctx, history, now)
			if err != nil {
				return err
			}
			if active == nil {
				return nil
			}
			if !matchesFilter(*active) {
				return nil
			}
			if err := s.endActiveBindingLocked(ctx, caller, *active, now); err != nil {
				return err
			}
			active.Status = BindingEnded
			if active.Metadata == nil {
				active.Metadata = make(map[string]string)
			}
			active.Metadata["last_touched_at"] = formatTime(now)
			closed = append(closed, *active)
			if s.delivery != nil && active.SessionID != "" {
				if err := s.delivery.ClearForConversation(ctx, active.SessionID, active.Conversation); err != nil {
					return err
				}
			}
			return nil
		})
		if err != nil {
			return closed, err
		}
	}
	return closed, nil
}

// ReassignSessionBindings moves active bindings from one session bead ID to
// another during canonical session repair, over a bd record store.
func ReassignSessionBindings(ctx context.Context, store beads.Store, oldSessionID, newSessionID string, now time.Time) error {
	if store == nil {
		return nil
	}
	return reassignSessionBindings(ctx, newBeadBackend(store), sharedBindingLockPool(store), oldSessionID, newSessionID, now)
}

// ReassignSessionBindingsWithBackend is ReassignSessionBindings over a
// routed messaging-class backend (the wiring for relocated cities — the
// bd-store form would silently no-op there, the record labels being empty).
func ReassignSessionBindingsWithBackend(ctx context.Context, backend fabricBackend, oldSessionID, newSessionID string, now time.Time) error {
	if backend == nil {
		return nil
	}
	return reassignSessionBindings(ctx, backend, sharedBindingLockPoolForBackend(backend), oldSessionID, newSessionID, now)
}

func reassignSessionBindings(ctx context.Context, backend fabricBackend, locks *bindingLockPool, oldSessionID, newSessionID string, now time.Time) error {
	if err := checkContext(ctx); err != nil {
		return err
	}
	oldSessionID = strings.TrimSpace(oldSessionID)
	newSessionID = strings.TrimSpace(newSessionID)
	if oldSessionID == "" || newSessionID == "" || oldSessionID == newSessionID {
		return nil
	}
	seeds, err := backend.ActiveBindingsBySession(oldSessionID)
	if err != nil {
		return fmt.Errorf("list bindings by retired session label: %w", err)
	}
	transcript := newTranscriptServiceWithBackend(backend, locks)
	delivery := deliveryCleaner{backend: backend, locks: locks}
	caller := Caller{Kind: CallerController, ID: "session-retirement"}
	now = zeroNow(now)
	for _, seed := range seeds {
		if err := checkContext(ctx); err != nil {
			return err
		}
		if seed.Status != BindingActive || seed.SessionID != oldSessionID {
			continue
		}
		if err := withLockKey(locks, conversationLockKey(seed.Conversation), func() error {
			record, ok, err := backend.GetOpenBinding(seed.ID)
			if err != nil {
				return err
			}
			if !ok {
				return nil
			}
			if record.Status != BindingActive || record.SessionID != oldSessionID {
				return nil
			}
			hasTargetBinding, err := activeBindingExistsForSession(backend, record.Conversation, record.ID, newSessionID)
			if err != nil {
				return err
			}
			if hasTargetBinding {
				if err := transcript.removeMembershipLocked(RemoveMembershipInput{
					Caller:       caller,
					Conversation: record.Conversation,
					SessionID:    oldSessionID,
					Owner:        MembershipOwnerBinding,
					Now:          now,
				}); err != nil {
					return wrapTranscriptSyncError("remove transcript membership after duplicate binding repair", err)
				}
				if err := delivery.ClearForConversation(ctx, oldSessionID, record.Conversation); err != nil {
					return err
				}
				if err := backend.CloseBinding(record.ID); err != nil {
					return fmt.Errorf("close duplicate binding %s during session reassignment: %w", record.ID, err)
				}
				return nil
			}
			if _, err := transcript.ensureMembershipLocked(EnsureMembershipInput{
				Caller:         caller,
				Conversation:   record.Conversation,
				SessionID:      newSessionID,
				BackfillPolicy: MembershipBackfillSinceJoin,
				Owner:          MembershipOwnerBinding,
				Now:            now,
			}); err != nil {
				return wrapTranscriptSyncError("ensure transcript membership after binding reassignment", err)
			}
			if err := backend.ReassignBindingSession(record.ID, oldSessionID, newSessionID, now); err != nil {
				return err
			}
			if err := transcript.removeMembershipLocked(RemoveMembershipInput{
				Caller:       caller,
				Conversation: record.Conversation,
				SessionID:    oldSessionID,
				Owner:        MembershipOwnerBinding,
				Now:          now,
			}); err != nil {
				return wrapTranscriptSyncError("remove transcript membership after binding reassignment", err)
			}
			if err := delivery.ClearForConversation(ctx, oldSessionID, record.Conversation); err != nil {
				return err
			}
			return nil
		}); err != nil {
			return err
		}
	}
	return nil
}

// newReassignmentTranscript constructs the transcript syncer used by
// ReassignSessionParticipants. It is a package-level var so tests can substitute
// a flaky transcript and exercise retry idempotence after a membership-migration
// failure (mirrors resolveLiveSessionID and timeNow).
var newReassignmentTranscript = func(store beads.Store, locks *bindingLockPool) groupTranscriptSync {
	return newTranscriptService(store, locks)
}

// ReassignSessionParticipants moves active group participants from one session
// bead ID to another during canonical session repair. It mirrors
// ReassignSessionBindings: the volatile session_id and its lookup handle are
// updated; the stable session_name and its handle are left untouched because
// the name is the same before and after a respawn. Like the participant upsert
// path, it also carries the group-owned transcript membership (keyed by session
// ID) to the replacement session and retires the old one, so transcript
// discovery follows the respawn instead of stranding the conversation on the
// dead session bead.
//
// The handover is retry-idempotent across a partial transcript-migration
// failure. The participant is discovered by the retired-session lookup handle,
// so that handle is retained until migrateParticipantGroupMembership commits:
// session_id is swapped to the replacement (and the new handle added) first so
// the membership count logic sees the post-handover state, but the
// retired-session handle is dropped only after migration succeeds. A failure
// therefore leaves the participant rediscoverable by both the retired-session
// handle and participantReassignmentPending, so a later
// ReassignSessionParticipants call (or the participant reaper) finishes the
// handover instead of stranding the group-owned membership on the dead session.
func ReassignSessionParticipants(ctx context.Context, store beads.Store, oldSessionID, newSessionID string) error {
	if store == nil {
		return nil
	}
	locks := sharedBindingLockPool(store)
	return reassignSessionParticipants(ctx, newBeadBackend(store), locks, newReassignmentTranscript(store, locks), oldSessionID, newSessionID)
}

// ReassignSessionParticipantsWithBackend is ReassignSessionParticipants over
// a routed messaging-class backend.
func ReassignSessionParticipantsWithBackend(ctx context.Context, backend fabricBackend, oldSessionID, newSessionID string) error {
	if backend == nil {
		return nil
	}
	locks := sharedBindingLockPoolForBackend(backend)
	return reassignSessionParticipants(ctx, backend, locks, newTranscriptServiceWithBackend(backend, locks), oldSessionID, newSessionID)
}

func reassignSessionParticipants(ctx context.Context, backend fabricBackend, locks *bindingLockPool, transcript groupTranscriptSync, oldSessionID, newSessionID string) error {
	if err := checkContext(ctx); err != nil {
		return err
	}
	oldSessionID = strings.TrimSpace(oldSessionID)
	newSessionID = strings.TrimSpace(newSessionID)
	if oldSessionID == "" || newSessionID == "" || oldSessionID == newSessionID {
		return nil
	}
	seeds, err := backend.ParticipantsBySession(oldSessionID)
	if err != nil {
		return fmt.Errorf("list participants by retired session label: %w", err)
	}
	svc := &groupService{backend: backend, locks: locks, transcript: transcript}
	for _, seed := range seeds {
		if err := checkContext(ctx); err != nil {
			return err
		}
		if !participantReassignmentPending(seed, oldSessionID, newSessionID) {
			continue
		}
		seedGroupID := seed.GroupID
		if err := withLockKey(locks, groupParticipantsMutationLock(seedGroupID), func() error {
			record, ok, err := backend.GetParticipant(seed.ID)
			if err != nil {
				return err
			}
			if !ok {
				return nil
			}
			if !participantReassignmentPending(record, oldSessionID, newSessionID) {
				return nil
			}
			group, err := svc.getGroupByID(record.GroupID)
			if err != nil {
				return fmt.Errorf("resolve group %s for participant %s during session reassignment: %w", record.GroupID, record.ID, err)
			}
			// Queue the retired session for group-membership cleanup, mirroring
			// the upsert reassignment path. Persist it in the same update as the
			// session_id swap so an ensure-membership failure still leaves a
			// durable cleanup record.
			pendingCleanup := record.PendingCleanup
			pendingCleanup = append(pendingCleanup, oldSessionID)
			pendingCleanup = removeSessionID(pendingCleanup, newSessionID)
			// Point the participant at the replacement and add the new session
			// handle, but KEEP the retired-session handle until membership
			// migration commits. The retired-session handle is this handover's
			// only retry-discoverable handle, so dropping it before
			// migrateParticipantGroupMembership succeeds would strand the
			// participant on a transcript-sync failure.
			if err := backend.ReassignParticipantSession(record.ID, oldSessionID, newSessionID, pendingCleanup); err != nil {
				return err
			}
			if err := svc.migrateParticipantGroupMembership(ctx, group, record.ID, newSessionID, pendingCleanup); err != nil {
				return err
			}
			// Membership migration committed: the retired-session handle is now
			// safe to drop, completing the handover.
			return backend.DropParticipantSessionLabel(record.ID, oldSessionID, newSessionID)
		}); err != nil {
			return err
		}
	}
	return nil
}

// CloseSessionBindings terminates active bindings, group participants, AND any
// residual conversation memberships for a retired session bead ID.
//
// The Unbind cascade only closes memberships through the binding seed loop, so
// a session whose only extmsg state is a participant-driven membership (e.g.
// created via POST /extmsg/participants by gc slack bind-room, with no
// corresponding gc:extmsg-binding bead) is left as a zombie. Worse, the
// gc:extmsg-participant beads themselves stay open and remain visible to
// ResolveInbound / ResolveOutbound, so group routing can still target the
// dead session. Sweep both explicitly.
func CloseSessionBindings(ctx context.Context, store beads.Store, sessionID string, now time.Time) error {
	if store == nil {
		return nil
	}
	return closeSessionBindingsOver(ctx, newBeadBackend(store), NewServices(store), sessionID, now)
}

// CloseSessionBindingsWithBackend is CloseSessionBindings over a routed
// messaging-class backend; sessionStore carries the session-class liveness
// reads (unused by the teardown flows themselves, but required by the
// services construction).
func CloseSessionBindingsWithBackend(ctx context.Context, backend fabricBackend, sessionStore beads.Store, sessionID string, now time.Time) error {
	if backend == nil {
		return nil
	}
	return closeSessionBindingsOver(ctx, backend, NewServicesWithBackend(backend, sessionStore), sessionID, now)
}

func closeSessionBindingsOver(ctx context.Context, backend fabricBackend, svc Services, sessionID string, now time.Time) error {
	if err := checkContext(ctx); err != nil {
		return err
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil
	}
	caller := Caller{Kind: CallerController, ID: "session-retirement"}
	if _, err := svc.Bindings.Unbind(ctx, caller, UnbindInput{
		SessionID: sessionID,
		Now:       now,
	}); err != nil {
		return err
	}
	if err := closeSessionParticipants(ctx, backend, svc, caller, sessionID); err != nil {
		return err
	}
	return closeSessionMemberships(ctx, svc, caller, sessionID, now)
}

// closeSessionParticipants closes every participant record targeting
// sessionID by delegating to RemoveParticipant, which also cleans up the
// group-owned portion of the corresponding membership.
func closeSessionParticipants(ctx context.Context, backend fabricBackend, svc Services, caller Caller, sessionID string) error {
	records, err := backend.ParticipantsBySession(sessionID)
	if err != nil {
		return fmt.Errorf("list residual participants for retired session %s: %w", sessionID, err)
	}
	type pair struct {
		groupID string
		handle  string
	}
	seen := make(map[pair]struct{}, len(records))
	for _, record := range records {
		if record.SessionID != sessionID {
			continue
		}
		key := pair{groupID: record.GroupID, handle: record.Handle}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		removeErr := svc.Groups.RemoveParticipant(ctx, caller, RemoveParticipantInput{
			GroupID: record.GroupID,
			Handle:  record.Handle,
		})
		if removeErr == nil || errors.Is(removeErr, ErrGroupRouteNotFound) {
			continue
		}
		return fmt.Errorf("remove residual participant %s (group=%s handle=%s) for retired session %s: %w", record.ID, record.GroupID, record.Handle, sessionID, removeErr)
	}
	return nil
}

// closeSessionMemberships closes any membership record still listing sessionID
// after the binding and participant sweeps. Catches binding-owned memberships
// whose binding record never existed (legacy data) and any other orphan paths.
func closeSessionMemberships(ctx context.Context, svc Services, caller Caller, sessionID string, now time.Time) error {
	memberships, err := svc.Transcript.ListConversationsBySession(ctx, caller, sessionID)
	if err != nil {
		return fmt.Errorf("list residual memberships for retired session %s: %w", sessionID, err)
	}
	for _, m := range memberships {
		// Iterate stored owners so removeMembershipLocked decrements the
		// owners slice to empty and closes the record. Legacy records with
		// empty owners still need closing; passing any single owner
		// triggers removeMembershipLocked's empty-owners substitution
		// path (transcript_service.go) which closes the record in one call.
		owners := m.Owners
		if len(owners) == 0 {
			owners = []MembershipOwner{MembershipOwnerManual}
		}
		for _, owner := range owners {
			removeErr := svc.Transcript.RemoveMembership(ctx, RemoveMembershipInput{
				Caller:       caller,
				Conversation: m.Conversation,
				SessionID:    sessionID,
				Owner:        owner,
				Now:          now,
			})
			if removeErr == nil || errors.Is(removeErr, ErrMembershipNotFound) {
				continue
			}
			return fmt.Errorf("remove residual membership %s (owner=%s) for retired session %s: %w", m.ID, owner, sessionID, removeErr)
		}
	}
	return nil
}

func activeBindingExistsForSession(backend fabricBackend, ref ConversationRef, currentID, sessionID string) (bool, error) {
	history, err := backend.BindingHistory(ref)
	if err != nil {
		return false, err
	}
	for _, record := range history {
		if record.ID == currentID || record.Status != BindingActive {
			continue
		}
		if record.SessionID == sessionID {
			return true, nil
		}
	}
	return false, nil
}

func (s *bindingService) listBindingsForConversation(ref ConversationRef) ([]SessionBindingRecord, error) {
	return s.backend.BindingHistory(ref)
}

func (s *bindingService) activeBinding(ctx context.Context, history []SessionBindingRecord, now time.Time) (*SessionBindingRecord, error) {
	return selectActiveBinding(ctx, history, now, expireBindingFunc(ctx, s.backend, s.delivery, s.transcript, now))
}

func (s *bindingService) getBinding(id string) (SessionBindingRecord, error) {
	record, _, err := s.backend.GetBinding(id)
	return record, err
}

func decodeBindingBead(b beads.Bead) (SessionBindingRecord, error) {
	ref, err := conversationRefFromMetadata(b.Metadata)
	if err != nil {
		return SessionBindingRecord{}, err
	}
	boundAt, err := parseTime(b.Metadata, "bound_at")
	if err != nil {
		return SessionBindingRecord{}, err
	}
	expiresAtRaw, err := parseTime(b.Metadata, "expires_at")
	if err != nil {
		return SessionBindingRecord{}, err
	}
	var expiresAt *time.Time
	if !expiresAtRaw.IsZero() {
		expiresAt = &expiresAtRaw
	}
	return SessionBindingRecord{
		ID:                b.ID,
		SchemaVersion:     parseInt(b.Metadata, "schema_version"),
		Conversation:      ref,
		SessionID:         strings.TrimSpace(b.Metadata["session_id"]),
		SessionName:       strings.TrimSpace(b.Metadata["session_name"]),
		AgentName:         strings.TrimSpace(b.Metadata["agent_name"]),
		Status:            recordStatus(b),
		BoundAt:           boundAt,
		ExpiresAt:         expiresAt,
		BindingGeneration: parseInt64(b.Metadata, "binding_generation"),
		Metadata:          decodePrefixedMetadata(b.Metadata),
	}, nil
}

// endActiveBindingLocked terminates an active binding while the caller
// holds the conversation's binding lock: it stamps the touched clock,
// removes the binding-owned transcript membership, and closes the binding
// record (re-ensuring the membership when the close fails). Clearing
// delivery contexts stays with the callers — Unbind reports the ended
// binding even when the subsequent clear fails.
func (s *bindingService) endActiveBindingLocked(_ context.Context, caller Caller, active SessionBindingRecord, now time.Time) error {
	if err := s.backend.TouchBinding(active.ID, now); err != nil {
		return fmt.Errorf("update binding %s metadata: %w", active.ID, err)
	}
	if s.transcript != nil {
		if err := s.transcript.removeMembershipLocked(RemoveMembershipInput{
			Caller:       caller,
			Conversation: active.Conversation,
			SessionID:    bindingMembershipKey(active),
			Owner:        MembershipOwnerBinding,
			Now:          now,
		}); err != nil {
			return wrapTranscriptSyncError("remove transcript membership after unbind", err)
		}
	}
	if err := s.backend.CloseBinding(active.ID); err != nil {
		if s.transcript != nil {
			_, _ = s.transcript.ensureMembershipLocked(EnsureMembershipInput{
				Caller:         caller,
				Conversation:   active.Conversation,
				SessionID:      bindingMembershipKey(active),
				BackfillPolicy: MembershipBackfillSinceJoin,
				Owner:          MembershipOwnerBinding,
				Now:            now,
			})
		}
		return fmt.Errorf("close binding %s: %w", active.ID, err)
	}
	return nil
}

// bindingTarget renders the bound endpoint for error messages: the agent
// identity for agent bindings, the session ID otherwise.
func bindingTarget(record SessionBindingRecord) string {
	if record.AgentName != "" {
		return "agent " + record.AgentName
	}
	return record.SessionID
}

// bindingMembershipKey is the transcript-membership key for a binding: the
// agent identity for agent bindings (a session selector the delivery layer
// resolves — materializing a session when none is live), the concrete
// session ID otherwise.
func bindingMembershipKey(record SessionBindingRecord) string {
	if record.AgentName != "" {
		return record.AgentName
	}
	return record.SessionID
}

func nextBindingGeneration(records []SessionBindingRecord) int64 {
	var maxGeneration int64
	for _, record := range records {
		if record.BindingGeneration > maxGeneration {
			maxGeneration = record.BindingGeneration
		}
	}
	return maxGeneration + 1
}

func bindingExpired(record SessionBindingRecord, now time.Time) bool {
	return record.ExpiresAt != nil && !record.ExpiresAt.After(now)
}

// expireBindingFunc builds the expiry cascade selectActiveBinding runs on a
// past-expiry binding: remove the binding-owned transcript membership, clear
// its delivery contexts, and close the binding, re-ensuring the membership
// when the close fails.
func expireBindingFunc(ctx context.Context, backend fabricBackend, delivery bindingCleaner, transcript bindingMembershipEnsurer, now time.Time) func(SessionBindingRecord) error {
	return func(record SessionBindingRecord) error {
		if transcript != nil {
			if err := transcript.removeMembershipLocked(RemoveMembershipInput{
				Caller:       Caller{Kind: CallerController, ID: "binding-expiry"},
				Conversation: record.Conversation,
				SessionID:    bindingMembershipKey(record),
				Owner:        MembershipOwnerBinding,
				Now:          now,
			}); err != nil {
				return wrapTranscriptSyncError("remove transcript membership after binding expiry", err)
			}
		}
		if delivery != nil && record.SessionID != "" {
			if err := delivery.ClearForConversation(ctx, record.SessionID, record.Conversation); err != nil {
				return err
			}
		}
		if err := backend.CloseBinding(record.ID); err != nil {
			if transcript != nil {
				_, _ = transcript.ensureMembershipLocked(EnsureMembershipInput{
					Caller:         Caller{Kind: CallerController, ID: "binding-expiry"},
					Conversation:   record.Conversation,
					SessionID:      bindingMembershipKey(record),
					BackfillPolicy: MembershipBackfillSinceJoin,
					Owner:          MembershipOwnerBinding,
					Now:            now,
				})
			}
			return fmt.Errorf("close expired binding %s: %w", record.ID, err)
		}
		return nil
	}
}

func resolveActiveBinding(ctx context.Context, locks *bindingLockPool, backend fabricBackend, delivery bindingCleaner, transcript bindingMembershipEnsurer, ref ConversationRef, now time.Time) (*SessionBindingRecord, error) {
	if err := checkContext(ctx); err != nil {
		return nil, err
	}
	var out *SessionBindingRecord
	err := withBindingLock(locks, ref, func() error {
		var err error
		out, err = resolveActiveBindingLocked(ctx, backend, delivery, transcript, ref, now)
		return err
	})
	return out, err
}

func resolveActiveBindingLocked(ctx context.Context, backend fabricBackend, delivery bindingCleaner, transcript bindingMembershipEnsurer, ref ConversationRef, now time.Time) (*SessionBindingRecord, error) {
	if err := checkContext(ctx); err != nil {
		return nil, err
	}
	history, err := backend.BindingHistory(ref)
	if err != nil {
		return nil, err
	}
	return selectActiveBinding(ctx, history, now, expireBindingFunc(ctx, backend, delivery, transcript, now))
}

func selectActiveBinding(ctx context.Context, history []SessionBindingRecord, now time.Time, expire func(SessionBindingRecord) error) (*SessionBindingRecord, error) {
	var active *SessionBindingRecord
	for _, record := range history {
		if err := checkContext(ctx); err != nil {
			return nil, err
		}
		if record.Status != BindingActive {
			continue
		}
		if bindingExpired(record, now) {
			if err := expire(record); err != nil {
				return nil, err
			}
			continue
		}
		if active != nil {
			return nil, fmt.Errorf("%w: multiple active bindings for %s", ErrInvariantViolation, conversationLockKey(record.Conversation))
		}
		rec := record
		active = &rec
	}
	return active, nil
}

func withBindingLock(pool *bindingLockPool, ref ConversationRef, fn func() error) error {
	return withLockKey(pool, conversationLockKey(ref), fn)
}

func withLockKey(pool *bindingLockPool, key string, fn func() error) error {
	lock := pool.acquire(key)
	defer pool.release(key, lock)
	return fn()
}

func newBindingLockPool() *bindingLockPool {
	return &bindingLockPool{locks: map[string]*bindingLockEntry{}}
}

func sharedBindingLockPool(store beads.Store) *bindingLockPool {
	return sharedBindingLockPoolForKey(bindingLockPoolKey(store))
}

// sharedBindingLockPoolForBackend returns the process-wide lock pool for a
// backend handle, so backend-constructed services over the same handle share
// one pool (the class-store construction roots cache one handle per db).
func sharedBindingLockPoolForBackend(backend fabricBackend) *bindingLockPool {
	if bead, ok := backend.(beadBackend); ok {
		return sharedBindingLockPool(bead.store)
	}
	return sharedBindingLockPoolForKey(fabricLockPoolKey(backend))
}

func sharedBindingLockPoolForKey(key string) *bindingLockPool {
	if existing, ok := sharedBindingLockPools.Load(key); ok {
		return existing.(*bindingLockPool)
	}
	created := newBindingLockPool()
	actual, _ := sharedBindingLockPools.LoadOrStore(key, created)
	return actual.(*bindingLockPool)
}

func bindingLockPoolKey(store beads.Store) string {
	if store == nil {
		return "<nil>"
	}
	return reflectivePoolKey(store)
}

func fabricLockPoolKey(backend fabricBackend) string {
	if backend == nil {
		return "<nil-backend>"
	}
	return "backend:" + reflectivePoolKey(backend)
}

func reflectivePoolKey(v any) string {
	value := reflect.ValueOf(v)
	switch value.Kind() {
	case reflect.Pointer, reflect.Map, reflect.Slice, reflect.Func, reflect.Chan, reflect.UnsafePointer:
		return fmt.Sprintf("%T:%x", v, value.Pointer())
	default:
		return fmt.Sprintf("%T:%v", v, v)
	}
}

func (p *bindingLockPool) acquire(key string) *bindingLockEntry {
	p.mu.Lock()
	lock := p.locks[key]
	if lock == nil {
		lock = &bindingLockEntry{}
		p.locks[key] = lock
	}
	lock.refs++
	p.mu.Unlock()

	lock.mu.Lock()
	return lock
}

func (p *bindingLockPool) release(key string, lock *bindingLockEntry) {
	lock.mu.Unlock()

	p.mu.Lock()
	lock.refs--
	if lock.refs == 0 {
		delete(p.locks, key)
	}
	p.mu.Unlock()
}

func conversationRefFromMetadata(meta map[string]string) (ConversationRef, error) {
	return validateConversationRef(ConversationRef{
		ScopeID:              meta["scope_id"],
		Provider:             meta["provider"],
		AccountID:            meta["account_id"],
		ConversationID:       meta["conversation_id"],
		ParentConversationID: meta["parent_conversation_id"],
		Kind:                 ConversationKind(meta["conversation_kind"]),
	})
}

func recordLabels(oldLabels []string, remove []string, add []string) ([]string, []string) {
	desired := make(map[string]bool, len(add))
	for _, label := range add {
		label = strings.TrimSpace(label)
		if label == "" {
			continue
		}
		desired[label] = true
	}
	updatedAdd := make([]string, 0, len(add))
	for _, label := range add {
		if label == "" || slices.Contains(oldLabels, label) {
			continue
		}
		updatedAdd = append(updatedAdd, label)
	}
	updatedRemove := make([]string, 0, len(remove))
	for _, label := range remove {
		label = strings.TrimSpace(label)
		if label == "" || !slices.Contains(oldLabels, label) || desired[label] {
			continue
		}
		updatedRemove = append(updatedRemove, label)
	}
	return updatedAdd, updatedRemove
}
