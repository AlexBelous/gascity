package extmsg

import "time"

// fabricBackend is the persistence edge of the extmsg fabric: every read and
// write of the seven messaging-class record kinds (bindings, delivery
// contexts, groups, participants, transcript entries, memberships, transcript
// state) crosses this seam. The bead backend (backend_bead.go) carries the
// label/metadata codec verbatim; the embedded-SQLite messaging-class store
// implements the same ops structurally (the interface stays unexported, its
// method names and transport types are exported — the orders/nudges seam
// pattern). Everything above the seam — validation, authorization, routing
// precedence, the expiry cascade, the membership owner algebra, hydration
// gates, session-liveness overlay, and the in-process conversation lock
// pool — is shared service logic and stays in this package.
//
// Conversation refs crossing the seam are always normalized (every service
// entry point validates first). No label string or metadata key crosses it.
//
// Error contract: ops whose pre-seam body carried a single call-site wrap
// keep that wrap inside the op; ops shared by callers with divergent wrap
// texts return the raw error and callers wrap as before.
type fabricBackend interface {
	// AtomicTx reports whether composite writes commit-or-rollback as a
	// unit. The handoff swap sequences differently on non-atomic stores
	// (see handoffActiveBindingLocked).
	AtomicTx() bool
	// Writer returns a standalone typed writer whose writes commit
	// individually (the non-transactional counterpart of the writer handed
	// to CreateBinding/RefreshBinding callbacks).
	Writer() FabricWriter

	// bindings
	BindingHistory(ref ConversationRef) ([]SessionBindingRecord, error)
	ActiveBindings() ([]SessionBindingRecord, error)
	ActiveBindingsBySession(sessionID string) ([]SessionBindingRecord, error)
	ActiveBindingsByAgent(agentName string) ([]SessionBindingRecord, error)
	// GetBinding returns the record plus its last-touched clock (the touch
	// debounce input, which is persistence-level and not part of the record).
	GetBinding(id string) (SessionBindingRecord, time.Time, error)
	// GetOpenBinding is the repair-path read: ok is false when id is not an
	// open binding record (deleted, ended, or another record kind).
	GetOpenBinding(id string) (SessionBindingRecord, bool, error)
	// CreateBinding creates a binding in one commit, optionally closing a
	// displaced binding first and running the membership sub-writes through
	// the same commit (gastownhall/gascity#3735).
	CreateBinding(create BindingCreate, displaceID string, membership func(FabricWriter) error) (SessionBindingRecord, error)
	// RefreshBinding re-stamps an active binding (same-target rebind):
	// optional stable-name backfill, expiry/touch/meta update, and the
	// membership sub-writes, all in one commit.
	RefreshBinding(ref ConversationRef, id string, refresh BindingRefresh, membership func(FabricWriter) error) error
	TouchBinding(id string, at time.Time) error
	CloseBinding(id string) error
	// ReassignBindingSession re-points a binding at a respawned session's
	// bead id (canonical session repair).
	ReassignBindingSession(id string, oldSessionID, newSessionID string, touchedAt time.Time) error

	// delivery contexts
	OpenDeliveryContexts(ref ConversationRef, sessionID string) ([]DeliveryContextRecord, error)
	CreateDeliveryContext(fields DeliveryFields) error
	UpdateDeliveryContext(id string, fields DeliveryFields) error
	CloseDeliveryContext(id string) error

	// groups
	OpenGroupsByRoot(ref ConversationRef) ([]ConversationGroupRecord, error)
	// GetGroup: ok is false when id is not an open group record.
	GetGroup(id string) (ConversationGroupRecord, bool, error)
	RefetchGroup(id string) (ConversationGroupRecord, error)
	CreateGroup(fields GroupFields) (ConversationGroupRecord, error)
	UpdateGroup(id string, fields GroupFields) error
	SetGroupCursor(id string, handle string) error

	// participants
	ParticipantsByGroup(groupID string, includeClosed bool) ([]ParticipantRecord, error)
	ParticipantsBySession(sessionID string) ([]ParticipantRecord, error)
	OpenParticipants() ([]ParticipantRecord, error)
	// GetParticipant: ok is false when id is not an open participant record.
	GetParticipant(id string) (ParticipantRecord, bool, error)
	RefetchParticipant(id string) (ConversationGroupParticipant, error)
	CreateParticipant(fields ParticipantFields) (ConversationGroupParticipant, error)
	// RetargetParticipant moves an existing participant to a new session
	// target (the handle keeps its identity; the session lookup handles
	// swap).
	RetargetParticipant(id string, fields ParticipantFields, oldSessionID, oldSessionName string, pendingCleanup []string) error
	// ReassignParticipantSession is the repair-path first half: point the
	// participant at the replacement session and persist the pending-cleanup
	// set while KEEPING the retired-session lookup handle discoverable.
	ReassignParticipantSession(id string, oldSessionID, newSessionID string, pendingCleanup []string) error
	// DropParticipantSessionLabel completes the handover: retire the old
	// session lookup handle once membership migration has committed.
	DropParticipantSessionLabel(id string, oldSessionID, newSessionID string) error
	CloseParticipant(id string) error
	SetParticipantPendingCleanup(id string, sessionIDs []string) error

	// transcript state
	OpenTranscriptStates(ref ConversationRef) ([]ConversationTranscriptStateRecord, error)
	RefetchTranscriptState(id string) (ConversationTranscriptStateRecord, error)
	PatchTranscriptState(id string, patch StatePatch) error

	// transcript entries
	OpenTranscriptsByProviderMessage(ref ConversationRef, providerMessageID string) ([]ConversationTranscriptRecord, error)
	// AppendTranscript persists one entry and advances the conversation's
	// sequence allocator. The bd body keeps today's two writes in order
	// (entry create, then state update — the historical crash window); an
	// atomic backend does both in one transaction.
	AppendTranscript(entry TranscriptEntryCreate, stateID string, nextSequence int64, setEarliestFloor bool) (ConversationTranscriptRecord, error)
	// ListTranscript returns entries with sequence above after, clamped to
	// [startSeq, endSeq], ordered by sequence (id tiebreak), up to limit.
	ListTranscript(ref ConversationRef, after, startSeq, endSeq int64, limit int, descending bool) ([]ConversationTranscriptRecord, error)

	// memberships
	OpenMembershipsExact(ref ConversationRef, sessionID string) ([]ConversationMembershipRecord, error)
	OpenMembershipsByConversation(ref ConversationRef) ([]ConversationMembershipRecord, error)
	OpenMembershipsBySession(sessionID string) ([]ConversationMembershipRecord, error)
	RefetchMembership(id string) (ConversationMembershipRecord, error)
	CloseMembership(id string, closedAt time.Time) error
	SetMembershipLastRead(id string, sequence int64) error
}

// FabricWriter is the typed write surface membership/state sub-writes use so
// they can ride a binding commit (CreateBinding/RefreshBinding hand a
// transactional writer to their callback) or commit standalone
// (fabricBackend.Writer). The membership owner algebra runs above the seam;
// a writer persists its precomputed outcome.
type FabricWriter interface {
	CreateMembership(create MembershipCreate) (ConversationMembershipRecord, error)
	PatchMembership(id string, patch MembershipPatch) error
	CreateTranscriptState(ref ConversationRef) (ConversationTranscriptStateRecord, error)
}

// BindingCreate is the persistence-edge input for a new binding record.
// Exactly one of SessionID/AgentName is set; SessionName is the stable
// session identity captured at bind time (empty when unresolvable).
type BindingCreate struct {
	Ref         ConversationRef
	SessionID   string
	SessionName string
	AgentName   string
	Generation  int64
	// BoundAt stamps bound_at and the initial last-touched clock.
	BoundAt       time.Time
	ExpiresAt     *time.Time
	CreatedByKind CallerKind
	CreatedByID   string
	Meta          map[string]string
}

// BindingRefresh is the same-target rebind patch.
type BindingRefresh struct {
	// SessionNameBackfill, when non-empty, records a now-known stable
	// session name on a binding created before one was resolvable.
	SessionNameBackfill string
	ExpiresAt           *time.Time
	TouchedAt           time.Time
	Meta                map[string]string
}

// DeliveryFields is the persistence-edge shape of a delivery-context write.
type DeliveryFields struct {
	Ref               ConversationRef
	SessionID         string
	BindingGeneration int64
	LastPublishedAt   time.Time
	LastMessageID     string
	SourceSessionID   string
	Meta              map[string]string
}

// GroupFields is the persistence-edge shape of a group create/update.
type GroupFields struct {
	Ref           ConversationRef
	Mode          GroupMode
	DefaultHandle string
	// LastAddressedHandle empty means the cursor field is left unwritten
	// (matching the pre-seam delete-from-fields behavior).
	LastAddressedHandle string
	Fanout              FanoutPolicy
	Meta                map[string]string
}

// ParticipantFields is the persistence-edge shape of a participant
// create/retarget.
type ParticipantFields struct {
	GroupID     string
	Handle      string
	SessionID   string
	SessionName string
	Public      bool
	Meta        map[string]string
}

// ParticipantRecord augments the exported participant shape with the
// persistence-level fields the repair paths need: lifecycle state and the
// pending-cleanup session set.
type ParticipantRecord struct {
	ConversationGroupParticipant
	Closed         bool
	PendingCleanup []string
}

// MembershipCreate is the persistence-edge input for a new membership row.
// The owner algebra (effective policy, owner set) is computed by the
// service; the backend persists the outcome.
type MembershipCreate struct {
	Ref            ConversationRef
	SessionID      string
	JoinedAt       time.Time
	JoinedSequence int64
	Backfill       MembershipBackfillPolicy
	// ManualBackfill is the stored manual-policy value ("" for non-manual
	// owners — see manualBackfillMetadataValue).
	ManualBackfill string
	Owners         []MembershipOwner
	Meta           map[string]string
}

// MembershipPatch is a tri-state field patch on an existing membership.
type MembershipPatch struct {
	Owners      []MembershipOwner
	SetOwners   bool
	Manual      string
	SetManual   bool
	Backfill    MembershipBackfillPolicy
	SetBackfill bool
	// Meta is the user-metadata passthrough (UpdateMembership); nil
	// elsewhere.
	Meta map[string]string
}

// StatePatch is a tri-state field patch on a transcript-state record.
type StatePatch struct {
	NextSequence *int64
	// EarliestFloorOne raises the earliest-available sequence to 1 on the
	// first append (the pre-seam literal write).
	EarliestFloorOne bool
	Hydration        *HydrationStatus
	Meta             map[string]string
}

// TranscriptEntryCreate is the persistence-edge input for one transcript
// entry. Text is the entry body; actor/attachments arrive pre-serialized
// (edge serialization stays at the edge on every backend).
type TranscriptEntryCreate struct {
	Ref               ConversationRef
	Sequence          int64
	Kind              TranscriptMessageKind
	Provenance        TranscriptProvenance
	ProviderMessageID string
	ExplicitTarget    string
	ReplyToMessageID  string
	SourceSessionID   string
	CreatedAt         time.Time
	Text              string
	ActorJSON         string
	AttachmentsJSON   string
	Meta              map[string]string
}
