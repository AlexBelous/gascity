package extmsg

import (
	"time"

	"github.com/gastownhall/gascity/internal/beads"
)

// MigrationExport is the messaging-class migration's read of every extmsg
// record family from a bd store, in the persistence-edge shapes the class
// store imports verbatim. Record IDs double as the bd bead ids for the
// residue sweep.
type MigrationExport struct {
	Bindings     []BindingExportRecord
	Deliveries   []DeliveryContextRecord
	Groups       []ConversationGroupRecord
	Participants []ParticipantRecord
	Memberships  []ConversationMembershipRecord
	States       []ConversationTranscriptStateRecord
	Entries      []ConversationTranscriptRecord
}

// BindingExportRecord pairs a binding record with its last-touched clock
// (persistence-level, not part of the record — the import preserves it).
type BindingExportRecord struct {
	SessionBindingRecord
	LastTouchedAt time.Time
}

// ExportRecords reads every extmsg record family from a bd store for the
// messaging-class migration: bindings INCLUDING ended ones (the migration
// carries the per-conversation generation ceiling), and the open/live rows
// of every other family. Decode failures abort — a partial export must not
// look complete to the copy-verify step.
func ExportRecords(store beads.Store) (MigrationExport, error) {
	out := MigrationExport{}

	bindingItems, err := store.List(beads.ListQuery{Label: labelBindingBase, IncludeClosed: true})
	if err != nil {
		return out, err
	}
	for _, item := range bindingItems {
		if !hasLabel(item, "gc:extmsg-binding") {
			continue
		}
		record, err := decodeBindingBead(item)
		if err != nil {
			return out, err
		}
		lastTouched, err := parseTime(item.Metadata, "last_touched_at")
		if err != nil {
			return out, err
		}
		out.Bindings = append(out.Bindings, BindingExportRecord{SessionBindingRecord: record, LastTouchedAt: lastTouched})
	}

	deliveryItems, err := store.List(beads.ListQuery{Label: labelDeliveryBase})
	if err != nil {
		return out, err
	}
	for _, item := range deliveryItems {
		if !hasLabel(item, "gc:extmsg-delivery") || item.Status == "closed" {
			continue
		}
		record, err := decodeDeliveryBead(item)
		if err != nil {
			return out, err
		}
		out.Deliveries = append(out.Deliveries, record)
	}

	groupItems, err := store.List(beads.ListQuery{Label: labelGroupBase})
	if err != nil {
		return out, err
	}
	for _, item := range groupItems {
		if !hasLabel(item, "gc:extmsg-group") || item.Status == "closed" {
			continue
		}
		record, err := decodeGroupBead(item)
		if err != nil {
			return out, err
		}
		out.Groups = append(out.Groups, record)
	}

	participantItems, err := store.List(beads.ListQuery{Label: labelGroupParticipantBase})
	if err != nil {
		return out, err
	}
	for _, item := range participantItems {
		if !hasLabel(item, "gc:extmsg-participant") || item.Status == "closed" {
			continue
		}
		record, err := decodeParticipantRecord(item)
		if err != nil {
			return out, err
		}
		out.Participants = append(out.Participants, record)
	}

	membershipItems, err := store.List(beads.ListQuery{Label: labelMembershipBase})
	if err != nil {
		return out, err
	}
	for _, item := range membershipItems {
		if !hasLabel(item, "gc:extmsg-membership") || item.Status == "closed" {
			continue
		}
		record, err := decodeMembershipBead(item)
		if err != nil {
			return out, err
		}
		out.Memberships = append(out.Memberships, record)
	}

	stateItems, err := store.List(beads.ListQuery{Label: labelTranscriptStateBase})
	if err != nil {
		return out, err
	}
	for _, item := range stateItems {
		if !hasLabel(item, "gc:extmsg-transcript-state") || item.Status == "closed" {
			continue
		}
		record, err := decodeTranscriptStateBead(item)
		if err != nil {
			return out, err
		}
		out.States = append(out.States, record)
	}

	entryItems, err := store.List(beads.ListQuery{Label: labelTranscriptBase})
	if err != nil {
		return out, err
	}
	for _, item := range entryItems {
		if !hasLabel(item, "gc:extmsg-transcript") || item.Status == "closed" {
			continue
		}
		record, err := decodeTranscriptBead(item)
		if err != nil {
			return out, err
		}
		out.Entries = append(out.Entries, record)
	}

	return out, nil
}

// RecordIDs returns every bd bead id the export covers, for the residue
// sweep.
func (e MigrationExport) RecordIDs() []string {
	ids := make([]string, 0, len(e.Bindings)+len(e.Deliveries)+len(e.Groups)+len(e.Participants)+len(e.Memberships)+len(e.States)+len(e.Entries))
	for _, r := range e.Bindings {
		ids = append(ids, r.ID)
	}
	for _, r := range e.Deliveries {
		ids = append(ids, r.ID)
	}
	for _, r := range e.Groups {
		ids = append(ids, r.ID)
	}
	for _, r := range e.Participants {
		ids = append(ids, r.ID)
	}
	for _, r := range e.Memberships {
		ids = append(ids, r.ID)
	}
	for _, r := range e.States {
		ids = append(ids, r.ID)
	}
	for _, r := range e.Entries {
		ids = append(ids, r.ID)
	}
	return ids
}

// ResidueBead is one bd extmsg bead's residue-sweep view: its id, lifecycle
// state, and creation clock (the mixed-version grace input).
type ResidueBead struct {
	ID        string
	Open      bool
	CreatedAt time.Time
}

// ExportResidueBeadIDs enumerates EVERY bd extmsg record bead (open and
// closed, all seven families) for the messaging-class residue sweep,
// without decoding.
func ExportResidueBeadIDs(store beads.Store) ([]ResidueBead, error) {
	labels := []string{
		labelBindingBase, labelDeliveryBase, labelGroupBase,
		labelGroupParticipantBase, labelMembershipBase,
		labelTranscriptStateBase, labelTranscriptBase,
	}
	seen := map[string]bool{}
	var out []ResidueBead
	for _, label := range labels {
		items, err := store.List(beads.ListQuery{Label: label, IncludeClosed: true})
		if err != nil {
			return nil, err
		}
		for _, item := range items {
			if seen[item.ID] {
				continue
			}
			seen[item.ID] = true
			out = append(out, ResidueBead{ID: item.ID, Open: item.Status != "closed", CreatedAt: item.CreatedAt})
		}
	}
	return out, nil
}
