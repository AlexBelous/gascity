package extmsg

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/gastownhall/gascity/internal/beads"
)

// beadBackend is the bead-store implementation of fabricBackend: every extmsg
// record is a Type:"task" bead whose fields live in metadata and whose
// lookups ride the sha256 locator labels (labels.go). The bodies here are the
// pre-seam service bodies, moved verbatim — including the doubled base
// labels, the participant dual-base-label quirk, and AppendTranscript's
// entry-then-state two-write sequence.
type beadBackend struct {
	store beads.Store
}

func newBeadBackend(store beads.Store) beadBackend { return beadBackend{store: store} }

// AtomicTx reports whether the underlying store's Tx provides atomic
// rollback.
func (b beadBackend) AtomicTx() bool { return beads.StoreSupportsAtomicTx(b.store) }

// Writer returns a store-backed writer whose writes commit individually.
func (b beadBackend) Writer() FabricWriter { return beadFabricWriter{w: b.store} }

func conversationMetadataFields(ref ConversationRef) map[string]string {
	return map[string]string{
		"schema_version":         strconv.Itoa(schemaVersion),
		"scope_id":               ref.ScopeID,
		"provider":               ref.Provider,
		"account_id":             ref.AccountID,
		"conversation_id":        ref.ConversationID,
		"parent_conversation_id": ref.ParentConversationID,
		"conversation_kind":      string(ref.Kind),
	}
}

// --- bindings ---

// BindingHistory returns every binding record for ref, including ended ones
// (generation minting needs the full history).
func (b beadBackend) BindingHistory(ref ConversationRef) ([]SessionBindingRecord, error) {
	items, err := b.store.List(beads.ListQuery{
		Label:         bindingConversationLabel(ref),
		IncludeClosed: true,
	})
	if err != nil {
		return nil, fmt.Errorf("list bindings by conversation label: %w", err)
	}
	out := make([]SessionBindingRecord, 0, len(items))
	for _, item := range items {
		if !hasLabel(item, "gc:extmsg-binding") {
			continue
		}
		record, err := decodeBindingBead(item)
		if err != nil {
			return nil, err
		}
		if !sameConversationRef(record.Conversation, ref) {
			continue
		}
		out = append(out, record)
	}
	return out, nil
}

// ActiveBindings returns every open binding record (the reaper scan).
func (b beadBackend) ActiveBindings() ([]SessionBindingRecord, error) {
	items, err := b.store.List(beads.ListQuery{Label: labelBindingBase})
	if err != nil {
		return nil, fmt.Errorf("list active bindings: %w", err)
	}
	out := make([]SessionBindingRecord, 0, len(items))
	for _, item := range items {
		record, err := decodeBindingBead(item)
		if err != nil {
			return nil, fmt.Errorf("decode binding %s: %w", item.ID, err)
		}
		out = append(out, record)
	}
	return out, nil
}

// ActiveBindingsBySession returns the open binding records whose target
// session lookup handle is sessionID. Errors are raw; callers wrap.
func (b beadBackend) ActiveBindingsBySession(sessionID string) ([]SessionBindingRecord, error) {
	return b.activeBindingsByLabel(bindingSessionLabel(sessionID))
}

// ActiveBindingsByAgent returns the open binding records whose target agent
// lookup handle is agentName. Errors are raw; callers wrap.
func (b beadBackend) ActiveBindingsByAgent(agentName string) ([]SessionBindingRecord, error) {
	return b.activeBindingsByLabel(bindingAgentLabel(agentName))
}

func (b beadBackend) activeBindingsByLabel(label string) ([]SessionBindingRecord, error) {
	items, err := b.store.List(beads.ListQuery{Label: label})
	if err != nil {
		return nil, err
	}
	out := make([]SessionBindingRecord, 0, len(items))
	for _, item := range items {
		if !hasLabel(item, "gc:extmsg-binding") || item.Status == "closed" {
			continue
		}
		record, err := decodeBindingBead(item)
		if err != nil {
			return nil, err
		}
		out = append(out, record)
	}
	return out, nil
}

// GetBinding fetches one binding record plus its last-touched clock.
func (b beadBackend) GetBinding(id string) (SessionBindingRecord, time.Time, error) {
	item, err := b.store.Get(id)
	if err != nil {
		return SessionBindingRecord{}, time.Time{}, fmt.Errorf("get binding %s: %w", id, err)
	}
	record, err := decodeBindingBead(item)
	if err != nil {
		return SessionBindingRecord{}, time.Time{}, err
	}
	lastTouched, err := parseTime(item.Metadata, "last_touched_at")
	if err != nil {
		return SessionBindingRecord{}, time.Time{}, err
	}
	return record, lastTouched, nil
}

// GetOpenBinding fetches one binding record for the repair paths; ok is
// false when id is not an open binding record.
func (b beadBackend) GetOpenBinding(id string) (SessionBindingRecord, bool, error) {
	item, err := b.store.Get(id)
	if err != nil {
		return SessionBindingRecord{}, false, fmt.Errorf("get binding %s: %w", id, err)
	}
	if !hasLabel(item, labelBindingBase) || item.Status == "closed" {
		return SessionBindingRecord{}, false, nil
	}
	record, err := decodeBindingBead(item)
	if err != nil {
		return SessionBindingRecord{}, false, fmt.Errorf("decode binding %s: %w", item.ID, err)
	}
	return record, true, nil
}

// CreateBinding coalesces the binding bead, its transcript-membership
// sub-writes, and an optional displaced-binding close into one commit
// (gastownhall/gascity#3735).
func (b beadBackend) CreateBinding(create BindingCreate, displaceID string, membership func(FabricWriter) error) (SessionBindingRecord, error) {
	ref := create.Ref
	// A binding targets either a configured agent (delivery-time resolution)
	// or a concrete session. Agent bindings get only the agent label; session
	// bindings get the volatile session-id label plus the stable session-name
	// label (which survives respawn) when a name is known.
	labels := []string{"gc:extmsg-binding", labelBindingBase, bindingConversationLabel(ref)}
	if create.AgentName != "" {
		labels = append(labels, bindingAgentLabel(create.AgentName))
	} else {
		labels = append(labels, bindingSessionLabel(create.SessionID))
		if create.SessionName != "" {
			labels = append(labels, bindingSessionNameLabel(create.SessionName))
		}
	}
	var out SessionBindingRecord
	if err := b.store.Tx("gc: extmsg bind "+conversationLockKey(ref), func(tx beads.Tx) error {
		if displaceID != "" {
			if err := tx.Close(displaceID); err != nil {
				return fmt.Errorf("close displaced binding %s: %w", displaceID, err)
			}
		}
		fields := conversationMetadataFields(ref)
		fields["session_id"] = create.SessionID
		fields["session_name"] = create.SessionName
		fields["agent_name"] = create.AgentName
		fields["binding_generation"] = strconv.FormatInt(create.Generation, 10)
		fields["bound_at"] = formatTime(create.BoundAt)
		fields["expires_at"] = formatTimePtr(create.ExpiresAt)
		fields["last_touched_at"] = formatTime(create.BoundAt)
		fields["created_by_kind"] = string(create.CreatedByKind)
		fields["created_by_id"] = create.CreatedByID
		created, err := tx.Create(beads.Bead{
			Title:    conversationTitle(ref),
			Type:     "task",
			Labels:   labels,
			Metadata: encodeMetadataFields(create.Meta, fields),
		})
		if err != nil {
			return fmt.Errorf("create external binding: %w", err)
		}
		decoded, err := decodeBindingBead(created)
		if err != nil {
			return err
		}
		out = decoded
		if membership != nil {
			return membership(beadFabricWriter{w: tx})
		}
		return nil
	}); err != nil {
		return SessionBindingRecord{}, err
	}
	return out, nil
}

// RefreshBinding re-stamps an active binding and re-runs its membership
// sub-writes in one commit (the same-target rebind).
func (b beadBackend) RefreshBinding(ref ConversationRef, id string, refresh BindingRefresh, membership func(FabricWriter) error) error {
	return b.store.Tx("gc: extmsg rebind "+conversationLockKey(ref), func(tx beads.Tx) error {
		if refresh.SessionNameBackfill != "" {
			if err := tx.Update(id, beads.UpdateOpts{
				Labels:   []string{bindingSessionNameLabel(refresh.SessionNameBackfill)},
				Metadata: map[string]string{"session_name": refresh.SessionNameBackfill},
			}); err != nil {
				return fmt.Errorf("backfill session name on binding %s: %w", id, err)
			}
		}
		kvs := encodeMetadataFields(refresh.Meta, map[string]string{
			"expires_at":      formatTimePtr(refresh.ExpiresAt),
			"last_touched_at": formatTime(refresh.TouchedAt),
		})
		if len(kvs) > 0 {
			if err := tx.SetMetadataBatch(id, kvs); err != nil {
				return err
			}
		}
		if membership != nil {
			return membership(beadFabricWriter{w: tx})
		}
		return nil
	})
}

// TouchBinding stamps the binding's last-touched clock.
func (b beadBackend) TouchBinding(id string, at time.Time) error {
	return b.store.SetMetadata(id, "last_touched_at", formatTime(at))
}

// CloseBinding ends a binding record.
func (b beadBackend) CloseBinding(id string) error { return b.store.Close(id) }

// ReassignBindingSession re-points a binding at a respawned session's bead
// id, swapping the volatile session lookup handle.
func (b beadBackend) ReassignBindingSession(id string, oldSessionID, newSessionID string, touchedAt time.Time) error {
	latest, err := b.store.Get(id)
	if err != nil {
		return fmt.Errorf("get binding %s: %w", id, err)
	}
	labelsToAdd, labelsToRemove := recordLabels(latest.Labels,
		[]string{bindingSessionLabel(oldSessionID)},
		[]string{bindingSessionLabel(newSessionID)})
	if err := b.store.Update(id, beads.UpdateOpts{
		Labels:       labelsToAdd,
		RemoveLabels: labelsToRemove,
		Metadata: map[string]string{
			"session_id":      newSessionID,
			"last_touched_at": formatTime(touchedAt),
		},
	}); err != nil {
		return fmt.Errorf("reassign binding %s from session %s to %s: %w", id, oldSessionID, newSessionID, err)
	}
	return nil
}

// --- delivery contexts ---

func deliveryMetadataFields(f DeliveryFields) map[string]string {
	fields := conversationMetadataFields(f.Ref)
	fields["session_id"] = f.SessionID
	fields["binding_generation"] = strconv.FormatInt(f.BindingGeneration, 10)
	fields["last_published_at"] = formatTime(f.LastPublishedAt)
	fields["last_message_id"] = strings.TrimSpace(f.LastMessageID)
	fields["source_session_id"] = strings.TrimSpace(f.SourceSessionID)
	return encodeMetadataFields(f.Meta, fields)
}

// OpenDeliveryContexts returns the open delivery contexts for the
// (conversation, session) route.
func (b beadBackend) OpenDeliveryContexts(ref ConversationRef, sessionID string) ([]DeliveryContextRecord, error) {
	items, err := b.store.List(beads.ListQuery{Label: deliveryRouteLabel(ref, sessionID)})
	if err != nil {
		return nil, fmt.Errorf("list delivery contexts: %w", err)
	}
	out := make([]DeliveryContextRecord, 0, len(items))
	for _, item := range items {
		if !hasLabel(item, "gc:extmsg-delivery") || item.Status == "closed" {
			continue
		}
		record, err := decodeDeliveryBead(item)
		if err != nil {
			return nil, err
		}
		if !sameConversationRef(record.Conversation, ref) || record.SessionID != sessionID {
			continue
		}
		out = append(out, record)
	}
	return out, nil
}

// CreateDeliveryContext creates a delivery-context record.
func (b beadBackend) CreateDeliveryContext(f DeliveryFields) error {
	_, err := b.store.Create(beads.Bead{
		Title:    f.SessionID + " -> " + conversationTitle(f.Ref),
		Type:     "task",
		Labels:   []string{"gc:extmsg-delivery", labelDeliveryBase, deliveryRouteLabel(f.Ref, f.SessionID), deliverySessionLabel(f.SessionID)},
		Metadata: deliveryMetadataFields(f),
	})
	if err != nil {
		return fmt.Errorf("create delivery context: %w", err)
	}
	return nil
}

// UpdateDeliveryContext refreshes an existing delivery-context record.
func (b beadBackend) UpdateDeliveryContext(id string, f DeliveryFields) error {
	title := f.SessionID + " -> " + conversationTitle(f.Ref)
	if err := b.store.Update(id, beads.UpdateOpts{Title: &title}); err != nil {
		return fmt.Errorf("update delivery title: %w", err)
	}
	if err := b.store.SetMetadataBatch(id, deliveryMetadataFields(f)); err != nil {
		return fmt.Errorf("update delivery metadata: %w", err)
	}
	return nil
}

// CloseDeliveryContext closes a delivery-context record. Errors are raw;
// callers wrap.
func (b beadBackend) CloseDeliveryContext(id string) error { return b.store.Close(id) }

// --- groups ---

func groupMetadataFields(f GroupFields) map[string]string {
	fields := conversationMetadataFields(f.Ref)
	fields["mode"] = string(f.Mode)
	fields["default_handle"] = f.DefaultHandle
	fields["last_addressed_handle"] = f.LastAddressedHandle
	fields["fanout_enabled"] = strconv.FormatBool(f.Fanout.Enabled)
	fields["fanout_allow_untargeted"] = strconv.FormatBool(f.Fanout.AllowUntargetedPublication)
	fields["fanout_max_peer_triggered_publishes"] = strconv.Itoa(f.Fanout.MaxPeerTriggeredPublishes)
	fields["fanout_max_total_peer_deliveries"] = strconv.Itoa(f.Fanout.MaxTotalPeerDeliveries)
	out := encodeMetadataFields(f.Meta, fields)
	if f.LastAddressedHandle == "" {
		delete(out, "last_addressed_handle")
	}
	return out
}

// OpenGroupsByRoot returns the open groups rooted at ref.
func (b beadBackend) OpenGroupsByRoot(ref ConversationRef) ([]ConversationGroupRecord, error) {
	items, err := b.store.List(beads.ListQuery{Label: groupRootLabel(ref)})
	if err != nil {
		return nil, fmt.Errorf("list groups by root label: %w", err)
	}
	out := make([]ConversationGroupRecord, 0, len(items))
	for _, item := range items {
		if !hasLabel(item, "gc:extmsg-group") || item.Status == "closed" {
			continue
		}
		record, err := decodeGroupBead(item)
		if err != nil {
			return nil, err
		}
		if !sameConversationRef(record.RootConversation, ref) {
			continue
		}
		out = append(out, record)
	}
	return out, nil
}

// GetGroup fetches one group record; ok is false when id is not an open
// group record.
func (b beadBackend) GetGroup(id string) (ConversationGroupRecord, bool, error) {
	item, err := b.store.Get(id)
	if err != nil {
		return ConversationGroupRecord{}, false, fmt.Errorf("get group %s: %w", id, err)
	}
	if !hasLabel(item, "gc:extmsg-group") || item.Status == "closed" {
		return ConversationGroupRecord{}, false, nil
	}
	record, err := decodeGroupBead(item)
	if err != nil {
		return ConversationGroupRecord{}, false, err
	}
	return record, true, nil
}

// RefetchGroup re-reads a group record after an update.
func (b beadBackend) RefetchGroup(id string) (ConversationGroupRecord, error) {
	item, err := b.store.Get(id)
	if err != nil {
		return ConversationGroupRecord{}, fmt.Errorf("get group %s: %w", id, err)
	}
	return decodeGroupBead(item)
}

// CreateGroup creates a group record.
func (b beadBackend) CreateGroup(f GroupFields) (ConversationGroupRecord, error) {
	created, err := b.store.Create(beads.Bead{
		Title:    conversationTitle(f.Ref),
		Type:     "task",
		Labels:   []string{"gc:extmsg-group", labelGroupBase, groupRootLabel(f.Ref)},
		Metadata: groupMetadataFields(f),
	})
	if err != nil {
		return ConversationGroupRecord{}, fmt.Errorf("create group: %w", err)
	}
	return decodeGroupBead(created)
}

// UpdateGroup refreshes an existing group record.
func (b beadBackend) UpdateGroup(id string, f GroupFields) error {
	title := conversationTitle(f.Ref)
	if err := b.store.Update(id, beads.UpdateOpts{Title: &title}); err != nil {
		return fmt.Errorf("update group title: %w", err)
	}
	if err := b.store.SetMetadataBatch(id, groupMetadataFields(f)); err != nil {
		return fmt.Errorf("update group metadata: %w", err)
	}
	return nil
}

// SetGroupCursor sets the group's last-addressed cursor. Errors are raw.
func (b beadBackend) SetGroupCursor(id string, handle string) error {
	return b.store.SetMetadata(id, "last_addressed_handle", handle)
}

func decodeGroupBead(b beads.Bead) (ConversationGroupRecord, error) {
	ref, err := conversationRefFromMetadata(b.Metadata)
	if err != nil {
		return ConversationGroupRecord{}, err
	}
	return ConversationGroupRecord{
		ID:                  b.ID,
		SchemaVersion:       parseInt(b.Metadata, "schema_version"),
		RootConversation:    ref,
		Mode:                GroupMode(strings.TrimSpace(b.Metadata["mode"])),
		DefaultHandle:       normalizeHandle(b.Metadata["default_handle"]),
		LastAddressedHandle: normalizeHandle(b.Metadata["last_addressed_handle"]),
		FanoutPolicy: FanoutPolicy{
			Enabled:                    parseBool(b.Metadata, "fanout_enabled"),
			AllowUntargetedPublication: parseBool(b.Metadata, "fanout_allow_untargeted"),
			MaxPeerTriggeredPublishes:  parseInt(b.Metadata, "fanout_max_peer_triggered_publishes"),
			MaxTotalPeerDeliveries:     parseInt(b.Metadata, "fanout_max_total_peer_deliveries"),
		},
		Metadata: decodePrefixedMetadata(b.Metadata),
	}, nil
}

// --- participants ---

func decodeParticipantRecord(item beads.Bead) (ParticipantRecord, error) {
	record, err := decodeParticipantBead(item)
	if err != nil {
		return ParticipantRecord{}, err
	}
	return ParticipantRecord{
		ConversationGroupParticipant: record,
		Closed:                       item.Status == "closed",
		PendingCleanup:               pendingCleanupSessionIDsFromMetadata(item.Metadata),
	}, nil
}

func participantMetadataFields(f ParticipantFields) map[string]string {
	return encodeMetadataFields(f.Meta, map[string]string{
		"schema_version": strconv.Itoa(schemaVersion),
		"group_id":       f.GroupID,
		"handle":         f.Handle,
		"session_id":     f.SessionID,
		"session_name":   f.SessionName,
		"public":         strconv.FormatBool(f.Public),
	})
}

// ParticipantsByGroup returns the group's participant records, optionally
// including closed ones (RemoveParticipant collects retired sessions from
// them).
func (b beadBackend) ParticipantsByGroup(groupID string, includeClosed bool) ([]ParticipantRecord, error) {
	items, err := b.store.List(beads.ListQuery{
		Label:         groupParticipantLabel(groupID),
		IncludeClosed: includeClosed,
	})
	if err != nil {
		return nil, fmt.Errorf("list group participants: %w", err)
	}
	out := make([]ParticipantRecord, 0, len(items))
	for _, item := range items {
		if !hasLabel(item, "gc:extmsg-participant") {
			continue
		}
		record, err := decodeParticipantRecord(item)
		if err != nil {
			return nil, err
		}
		out = append(out, record)
	}
	return out, nil
}

// ParticipantsBySession returns the open participant records targeting
// sessionID. Errors are raw; callers wrap.
func (b beadBackend) ParticipantsBySession(sessionID string) ([]ParticipantRecord, error) {
	items, err := b.store.List(beads.ListQuery{Label: groupParticipantSessionLabel(sessionID)})
	if err != nil {
		return nil, err
	}
	out := make([]ParticipantRecord, 0, len(items))
	for _, item := range items {
		if !hasLabel(item, "gc:extmsg-participant") || item.Status == "closed" {
			continue
		}
		record, err := decodeParticipantRecord(item)
		if err != nil {
			return nil, err
		}
		out = append(out, record)
	}
	return out, nil
}

// OpenParticipants returns every open participant record (the reaper scan).
func (b beadBackend) OpenParticipants() ([]ParticipantRecord, error) {
	items, err := b.store.List(beads.ListQuery{Label: labelGroupParticipantBase})
	if err != nil {
		return nil, fmt.Errorf("list active group participants: %w", err)
	}
	out := make([]ParticipantRecord, 0, len(items))
	for _, item := range items {
		if !hasLabel(item, "gc:extmsg-participant") || item.Status == "closed" {
			continue
		}
		record, err := decodeParticipantRecord(item)
		if err != nil {
			return nil, fmt.Errorf("decode participant %s: %w", item.ID, err)
		}
		out = append(out, record)
	}
	return out, nil
}

// GetParticipant fetches one participant record for the repair paths; ok is
// false when id is not an open participant record.
func (b beadBackend) GetParticipant(id string) (ParticipantRecord, bool, error) {
	item, err := b.store.Get(id)
	if err != nil {
		return ParticipantRecord{}, false, fmt.Errorf("get participant %s: %w", id, err)
	}
	if !hasLabel(item, "gc:extmsg-participant") || item.Status == "closed" {
		return ParticipantRecord{}, false, nil
	}
	record, err := decodeParticipantRecord(item)
	if err != nil {
		return ParticipantRecord{}, false, fmt.Errorf("decode participant %s: %w", item.ID, err)
	}
	return record, true, nil
}

// RefetchParticipant re-reads a participant record after an update.
func (b beadBackend) RefetchParticipant(id string) (ConversationGroupParticipant, error) {
	item, err := b.store.Get(id)
	if err != nil {
		return ConversationGroupParticipant{}, fmt.Errorf("get participant %s: %w", id, err)
	}
	return decodeParticipantBead(item)
}

// CreateParticipant creates a participant record.
func (b beadBackend) CreateParticipant(f ParticipantFields) (ConversationGroupParticipant, error) {
	createLabels := []string{"gc:extmsg-participant", labelGroupParticipantBase, groupParticipantLabel(f.GroupID), groupParticipantSessionLabel(f.SessionID)}
	if f.SessionName != "" {
		createLabels = append(createLabels, groupParticipantSessionNameLabel(f.SessionName))
	}
	created, err := b.store.Create(beads.Bead{
		Title:    f.GroupID + "/" + f.Handle,
		Type:     "task",
		Labels:   createLabels,
		Metadata: participantMetadataFields(f),
	})
	if err != nil {
		return ConversationGroupParticipant{}, fmt.Errorf("create group participant: %w", err)
	}
	return decodeParticipantBead(created)
}

// RetargetParticipant moves an existing participant to a new session target,
// swapping its session lookup handles and persisting the pending-cleanup
// set.
func (b beadBackend) RetargetParticipant(id string, f ParticipantFields, oldSessionID, oldSessionName string, pendingCleanup []string) error {
	latest, err := b.store.Get(id)
	if err != nil {
		return fmt.Errorf("get participant %s: %w", id, err)
	}
	title := f.GroupID + "/" + f.Handle
	updateFields := participantMetadataFields(f)
	updateFields["previous_session_id_pending_cleanup"] = encodePendingCleanupSessionIDs(pendingCleanup)
	labelsToAdd, labelsToRemove := recordLabels(latest.Labels,
		participantSessionLabels(oldSessionID, oldSessionName),
		participantSessionLabels(f.SessionID, f.SessionName))
	if err := b.store.Update(id, beads.UpdateOpts{
		Title:        &title,
		Labels:       labelsToAdd,
		RemoveLabels: labelsToRemove,
	}); err != nil {
		return fmt.Errorf("update group participant: %w", err)
	}
	if err := b.store.SetMetadataBatch(id, updateFields); err != nil {
		return fmt.Errorf("update participant metadata: %w", err)
	}
	return nil
}

// ReassignParticipantSession points the participant at the replacement
// session while KEEPING the retired-session lookup handle (the handover's
// only retry-discoverable handle until membership migration commits).
func (b beadBackend) ReassignParticipantSession(id string, oldSessionID, newSessionID string, pendingCleanup []string) error {
	latest, err := b.store.Get(id)
	if err != nil {
		return fmt.Errorf("get participant %s: %w", id, err)
	}
	labelsToAdd, _ := recordLabels(latest.Labels, nil, []string{groupParticipantSessionLabel(newSessionID)})
	if err := b.store.Update(id, beads.UpdateOpts{
		Labels: labelsToAdd,
		Metadata: map[string]string{
			"session_id":                          newSessionID,
			"previous_session_id_pending_cleanup": encodePendingCleanupSessionIDs(pendingCleanup),
		},
	}); err != nil {
		return fmt.Errorf("reassign participant %s from session %s to %s: %w", id, oldSessionID, newSessionID, err)
	}
	return nil
}

// DropParticipantSessionLabel retires the old session lookup handle once
// membership migration has committed, completing the handover.
func (b beadBackend) DropParticipantSessionLabel(id string, oldSessionID, newSessionID string) error {
	latest, err := b.store.Get(id)
	if err != nil {
		return fmt.Errorf("get participant %s: %w", id, err)
	}
	_, labelsToRemove := recordLabels(latest.Labels,
		[]string{groupParticipantSessionLabel(oldSessionID)},
		[]string{groupParticipantSessionLabel(newSessionID)})
	if len(labelsToRemove) == 0 {
		return nil
	}
	if err := b.store.Update(id, beads.UpdateOpts{RemoveLabels: labelsToRemove}); err != nil {
		return fmt.Errorf("drop retired session label from participant %s after reassignment to %s: %w", id, newSessionID, err)
	}
	return nil
}

// CloseParticipant closes a participant record. Errors are raw; callers
// wrap.
func (b beadBackend) CloseParticipant(id string) error { return b.store.Close(id) }

// SetParticipantPendingCleanup persists the participant's pending-cleanup
// session set. Errors are raw; the service wraps.
func (b beadBackend) SetParticipantPendingCleanup(id string, sessionIDs []string) error {
	return b.store.SetMetadata(id, "previous_session_id_pending_cleanup", encodePendingCleanupSessionIDs(sessionIDs))
}

// --- transcript state ---

// OpenTranscriptStates returns the open transcript-state records for ref.
func (b beadBackend) OpenTranscriptStates(ref ConversationRef) ([]ConversationTranscriptStateRecord, error) {
	items, err := b.store.List(beads.ListQuery{Label: transcriptStateLabel(ref)})
	if err != nil {
		return nil, fmt.Errorf("list transcript state: %w", err)
	}
	out := make([]ConversationTranscriptStateRecord, 0, len(items))
	for _, item := range items {
		if !hasLabel(item, "gc:extmsg-transcript-state") || item.Status == "closed" {
			continue
		}
		record, err := decodeTranscriptStateBead(item)
		if err != nil {
			return nil, err
		}
		if !sameConversationRef(record.Conversation, ref) {
			continue
		}
		out = append(out, record)
	}
	return out, nil
}

// RefetchTranscriptState re-reads a transcript-state record after an update.
func (b beadBackend) RefetchTranscriptState(id string) (ConversationTranscriptStateRecord, error) {
	item, err := b.store.Get(id)
	if err != nil {
		return ConversationTranscriptStateRecord{}, fmt.Errorf("get state %s: %w", id, err)
	}
	return decodeTranscriptStateBead(item)
}

func statePatchFields(p StatePatch) map[string]string {
	fields := map[string]string{}
	if p.NextSequence != nil {
		fields["next_sequence"] = strconv.FormatInt(*p.NextSequence, 10)
	}
	if p.EarliestFloorOne {
		fields["earliest_available_sequence"] = "1"
	}
	if p.Hydration != nil {
		fields["hydration_status"] = string(*p.Hydration)
	}
	return encodeMetadataFields(p.Meta, fields)
}

// PatchTranscriptState applies a tri-state field patch. Errors are raw;
// callers wrap.
func (b beadBackend) PatchTranscriptState(id string, patch StatePatch) error {
	return b.store.SetMetadataBatch(id, statePatchFields(patch))
}

// --- transcript entries ---

// OpenTranscriptsByProviderMessage returns the open entries carrying the
// provider message id (the inbound idempotency read).
func (b beadBackend) OpenTranscriptsByProviderMessage(ref ConversationRef, providerMessageID string) ([]ConversationTranscriptRecord, error) {
	items, err := b.store.List(beads.ListQuery{Label: transcriptProviderMessageLabel(ref, providerMessageID)})
	if err != nil {
		return nil, fmt.Errorf("list transcript by provider message label: %w", err)
	}
	out := make([]ConversationTranscriptRecord, 0, len(items))
	for _, item := range items {
		if !hasLabel(item, "gc:extmsg-transcript") || item.Status == "closed" {
			continue
		}
		record, err := decodeTranscriptBead(item)
		if err != nil {
			return nil, err
		}
		if !sameConversationRef(record.Conversation, ref) || record.ProviderMessageID != providerMessageID {
			continue
		}
		out = append(out, record)
	}
	return out, nil
}

// AppendTranscript persists one entry and advances the sequence allocator.
// The two writes stay separate on this backend (the pre-seam shape): a crash
// between them re-issues the sequence, which the atomic backend closes.
func (b beadBackend) AppendTranscript(entry TranscriptEntryCreate, stateID string, nextSequence int64, setEarliestFloor bool) (ConversationTranscriptRecord, error) {
	ref := entry.Ref
	fields := conversationMetadataFields(ref)
	fields["sequence"] = strconv.FormatInt(entry.Sequence, 10)
	fields["kind"] = string(entry.Kind)
	fields["provenance"] = string(entry.Provenance)
	fields["provider_message_id"] = entry.ProviderMessageID
	fields["explicit_target"] = entry.ExplicitTarget
	fields["reply_to_message_id"] = entry.ReplyToMessageID
	fields["source_session_id"] = entry.SourceSessionID
	fields["created_at"] = formatTime(entry.CreatedAt)
	fields["actor_json"] = entry.ActorJSON
	fields["attachments_json"] = entry.AttachmentsJSON
	labels := []string{
		labelTranscriptBase,
		transcriptConversationLabel(ref),
		transcriptBucketLabel(ref, transcriptBucket(entry.Sequence)),
	}
	if entry.ProviderMessageID != "" {
		labels = append(labels, transcriptProviderMessageLabel(ref, entry.ProviderMessageID))
	}
	created, err := b.store.Create(beads.Bead{
		Title:       fmt.Sprintf("%s#%d", conversationTitle(ref), entry.Sequence),
		Type:        "task",
		Description: entry.Text,
		Labels:      append([]string{"gc:extmsg-transcript"}, labels...),
		Metadata:    encodeMetadataFields(entry.Meta, fields),
	})
	if err != nil {
		return ConversationTranscriptRecord{}, fmt.Errorf("create transcript entry: %w", err)
	}
	updates := map[string]string{
		"next_sequence": strconv.FormatInt(nextSequence, 10),
	}
	if setEarliestFloor {
		updates["earliest_available_sequence"] = "1"
	}
	if err := b.store.SetMetadataBatch(stateID, updates); err != nil {
		return ConversationTranscriptRecord{}, fmt.Errorf("update transcript state: %w", err)
	}
	return decodeTranscriptBead(created)
}

// ListTranscript walks the bucket labels covering [startSeq, endSeq] and
// returns up to limit entries above after, ordered by sequence (id
// tiebreak).
func (b beadBackend) ListTranscript(ref ConversationRef, after, startSeq, endSeq int64, limit int, descending bool) ([]ConversationTranscriptRecord, error) {
	startBucket := transcriptBucket(startSeq)
	endBucket := transcriptBucket(endSeq)
	records := make([]ConversationTranscriptRecord, 0, limit)
	appendBucket := func(bucket int64) error {
		items, err := b.store.List(beads.ListQuery{Label: transcriptBucketLabel(ref, bucket)})
		if err != nil {
			return fmt.Errorf("list transcript bucket %d: %w", bucket, err)
		}
		bucketRecords := make([]ConversationTranscriptRecord, 0, len(items))
		for _, item := range items {
			if !hasLabel(item, "gc:extmsg-transcript") || item.Status == "closed" {
				continue
			}
			record, err := decodeTranscriptBead(item)
			if err != nil {
				return err
			}
			if !sameConversationRef(record.Conversation, ref) || record.Sequence <= after {
				continue
			}
			bucketRecords = append(bucketRecords, record)
		}
		sortTranscriptRecords(bucketRecords, descending)
		for _, record := range bucketRecords {
			if len(records) >= limit {
				break
			}
			records = append(records, record)
		}
		return nil
	}
	// Descending walks newest bucket first so the most recent entries are
	// collected without scanning the entire stream on busy conversations.
	if descending {
		for bucket := endBucket; bucket >= startBucket && len(records) < limit; bucket-- {
			if err := appendBucket(bucket); err != nil {
				return nil, err
			}
		}
	} else {
		for bucket := startBucket; bucket <= endBucket && len(records) < limit; bucket++ {
			if err := appendBucket(bucket); err != nil {
				return nil, err
			}
		}
	}
	return records, nil
}

// --- memberships ---

// OpenMembershipsExact returns the open memberships for the exact
// (conversation, session) pair.
func (b beadBackend) OpenMembershipsExact(ref ConversationRef, sessionID string) ([]ConversationMembershipRecord, error) {
	items, err := b.store.List(beads.ListQuery{Label: membershipExactLabel(ref, sessionID)})
	if err != nil {
		return nil, fmt.Errorf("list membership by exact label: %w", err)
	}
	out := make([]ConversationMembershipRecord, 0, len(items))
	for _, item := range items {
		if !hasLabel(item, "gc:extmsg-membership") || item.Status == "closed" {
			continue
		}
		record, err := decodeMembershipBead(item)
		if err != nil {
			return nil, err
		}
		if !sameConversationRef(record.Conversation, ref) || record.SessionID != sessionID {
			continue
		}
		out = append(out, record)
	}
	return out, nil
}

// OpenMembershipsByConversation returns the conversation's open memberships.
func (b beadBackend) OpenMembershipsByConversation(ref ConversationRef) ([]ConversationMembershipRecord, error) {
	items, err := b.store.List(beads.ListQuery{Label: membershipConversationLabel(ref)})
	if err != nil {
		return nil, fmt.Errorf("list memberships by conversation label: %w", err)
	}
	out := make([]ConversationMembershipRecord, 0, len(items))
	for _, item := range items {
		if !hasLabel(item, "gc:extmsg-membership") || item.Status == "closed" {
			continue
		}
		record, err := decodeMembershipBead(item)
		if err != nil {
			return nil, err
		}
		if !sameConversationRef(record.Conversation, ref) {
			continue
		}
		out = append(out, record)
	}
	return out, nil
}

// OpenMembershipsBySession returns the session's open memberships.
func (b beadBackend) OpenMembershipsBySession(sessionID string) ([]ConversationMembershipRecord, error) {
	items, err := b.store.List(beads.ListQuery{Label: membershipSessionLabel(sessionID)})
	if err != nil {
		return nil, fmt.Errorf("list memberships by session label: %w", err)
	}
	out := make([]ConversationMembershipRecord, 0, len(items))
	for _, item := range items {
		if !hasLabel(item, "gc:extmsg-membership") || item.Status == "closed" {
			continue
		}
		record, err := decodeMembershipBead(item)
		if err != nil {
			return nil, err
		}
		if record.SessionID != sessionID {
			continue
		}
		out = append(out, record)
	}
	return out, nil
}

// RefetchMembership re-reads a membership record after an update.
func (b beadBackend) RefetchMembership(id string) (ConversationMembershipRecord, error) {
	item, err := b.store.Get(id)
	if err != nil {
		return ConversationMembershipRecord{}, fmt.Errorf("get membership %s: %w", id, err)
	}
	return decodeMembershipBead(item)
}

// CloseMembership stamps the closed clock and closes the membership record
// (the pre-seam two-write order).
func (b beadBackend) CloseMembership(id string, closedAt time.Time) error {
	if err := b.store.SetMetadata(id, "closed_at", formatTime(closedAt)); err != nil {
		return fmt.Errorf("set membership closed_at: %w", err)
	}
	if err := b.store.Close(id); err != nil {
		return fmt.Errorf("close membership %s: %w", id, err)
	}
	return nil
}

// SetMembershipLastRead advances the membership's read cursor. Errors are
// raw.
func (b beadBackend) SetMembershipLastRead(id string, sequence int64) error {
	return b.store.SetMetadata(id, "last_read_sequence", strconv.FormatInt(sequence, 10))
}

// --- writer ---

// beadFabricWriter persists membership/state sub-writes through w — either
// the store itself (standalone commits) or a beads.Tx (riding a binding
// commit).
type beadFabricWriter struct {
	w membershipWriter
}

// CreateMembership creates a membership record with the service-computed
// owner/policy outcome.
func (bw beadFabricWriter) CreateMembership(create MembershipCreate) (ConversationMembershipRecord, error) {
	ref := create.Ref
	fields := conversationMetadataFields(ref)
	fields["session_id"] = create.SessionID
	fields["joined_at"] = formatTime(create.JoinedAt)
	fields["joined_sequence"] = strconv.FormatInt(create.JoinedSequence, 10)
	fields["last_read_sequence"] = "0"
	fields["membership_backfill_policy"] = string(create.Backfill)
	fields["manual_backfill_policy"] = create.ManualBackfill
	fields["membership_owner_kinds"] = encodeMembershipOwners(create.Owners)
	created, err := bw.w.Create(beads.Bead{
		Title:    create.SessionID + " -> " + conversationTitle(ref),
		Type:     "task",
		Labels:   []string{"gc:extmsg-membership", labelMembershipBase, membershipConversationLabel(ref), membershipExactLabel(ref, create.SessionID), membershipSessionLabel(create.SessionID)},
		Metadata: encodeMetadataFields(create.Meta, fields),
	})
	if err != nil {
		return ConversationMembershipRecord{}, fmt.Errorf("create membership: %w", err)
	}
	return decodeMembershipBead(created)
}

func membershipPatchFields(p MembershipPatch) map[string]string {
	fields := map[string]string{}
	if p.SetOwners {
		fields["membership_owner_kinds"] = encodeMembershipOwners(p.Owners)
	}
	if p.SetManual {
		fields["manual_backfill_policy"] = p.Manual
	}
	if p.SetBackfill {
		fields["membership_backfill_policy"] = string(p.Backfill)
	}
	return encodeMetadataFields(p.Meta, fields)
}

// PatchMembership applies a tri-state field patch. Errors are raw; callers
// wrap.
func (bw beadFabricWriter) PatchMembership(id string, patch MembershipPatch) error {
	return bw.w.SetMetadataBatch(id, membershipPatchFields(patch))
}

// CreateTranscriptState creates the conversation's first-touch transcript
// state record.
func (bw beadFabricWriter) CreateTranscriptState(ref ConversationRef) (ConversationTranscriptStateRecord, error) {
	fields := conversationMetadataFields(ref)
	fields["next_sequence"] = "1"
	fields["earliest_available_sequence"] = "1"
	fields["hydration_status"] = string(HydrationLiveOnly)
	fields["max_retained_entries"] = "0"
	created, err := bw.w.Create(beads.Bead{
		Title:    conversationTitle(ref) + "/state",
		Type:     "task",
		Labels:   []string{"gc:extmsg-transcript-state", labelTranscriptStateBase, transcriptStateLabel(ref)},
		Metadata: fields,
	})
	if err != nil {
		return ConversationTranscriptStateRecord{}, fmt.Errorf("create transcript state: %w", err)
	}
	return decodeTranscriptStateBead(created)
}
