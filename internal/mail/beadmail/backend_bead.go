package beadmail

// beadStore is the bd messages backend: the moved bodies of the Provider's
// former direct beads.Store operations, byte-identical — the
// Type="message" ephemeral-wisp bead shape, the label/metadata codec, the
// two-tier list flags, and the wisp-tier purge's dependency hygiene all
// live here now.

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/mail"
)

type beadStore struct {
	store beads.Store
}

// beadToRecord is the read half of the codec (the former beadToMessage plus
// the lifecycle fields the domain object omits).
func beadToRecord(b beads.Bead) Record {
	read := hasLabel(b.Labels, "read")
	readLabel := read
	switch b.Metadata["mail.read"] {
	case "true":
		read = true
	case "false":
		read = false
	}
	return Record{
		ID:                 b.ID,
		ThreadID:           extractLabel(b.Labels, "thread:"),
		ReplyToID:          extractLabel(b.Labels, "reply-to:"),
		FromAddr:           b.From,
		ToAddr:             b.Assignee,
		FromSessionID:      strings.TrimSpace(b.Metadata[fromSessionIDMetadataKey]),
		FromDisplay:        strings.TrimSpace(b.Metadata[fromDisplayMetadataKey]),
		ToSessionID:        strings.TrimSpace(b.Metadata[toSessionIDMetadataKey]),
		ToDisplay:          strings.TrimSpace(b.Metadata[toDisplayMetadataKey]),
		Subject:            b.Title,
		Body:               b.Description,
		CreatedAt:          b.CreatedAt,
		Read:               read,
		ReadLabel:          readLabel,
		Open:               b.Status == "open",
		CloseReason:        b.Metadata["close_reason"],
		AutoHandoff:        hasLabel(b.Labels, mail.AutoHandoffLabel),
		ArchiveAfterInject: hasLabel(b.Labels, mail.ArchiveAfterInjectLabel),
		Priority:           extractPriority(b.Labels),
		CC:                 extractCC(b.Labels),
	}
}

// Create is the single confined edge where a mail message becomes a
// type=message bead: labels (thread, reply-to, extra) and the conditional
// display/session metadata are assembled exactly as the prior inline
// bodies did.
func (s beadStore) Create(msg NewMessage) (Record, error) {
	labels := make([]string, 0, 2+len(msg.ExtraLabels))
	labels = append(labels, "thread:"+msg.ThreadID)
	if msg.ReplyToID != "" {
		labels = append(labels, "reply-to:"+msg.ReplyToID)
	}
	labels = append(labels, msg.ExtraLabels...)

	var metadata map[string]string
	setMeta := func(key, value string) {
		if value == "" {
			return
		}
		if metadata == nil {
			metadata = make(map[string]string)
		}
		metadata[key] = value
	}
	setMeta(fromSessionIDMetadataKey, msg.FromSessionID)
	setMeta(fromDisplayMetadataKey, msg.FromDisplay)
	setMeta(toSessionIDMetadataKey, msg.ToSessionID)
	setMeta(toDisplayMetadataKey, msg.ToDisplay)

	b, err := s.store.Create(beads.Bead{
		Title:       msg.Subject,
		Description: msg.Body,
		Type:        messageBeadType,
		Assignee:    msg.To,
		From:        msg.From,
		Labels:      labels,
		Metadata:    metadata,
		Ephemeral:   true,
	})
	if err != nil {
		return Record{}, err
	}
	return beadToRecord(b), nil
}

// Get returns the record for id; a non-message bead surfaces as
// NotAMessageError so the Provider can format each operation's established
// wrong-type vocabulary.
func (s beadStore) Get(id string) (Record, bool, error) {
	b, err := s.store.Get(id)
	if err != nil {
		if errors.Is(err, beads.ErrNotFound) {
			return Record{}, false, nil
		}
		return Record{}, false, err
	}
	if b.Type != messageBeadType {
		return Record{}, false, NotAMessageError{ID: id, Type: b.Type}
	}
	return beadToRecord(b), true, nil
}

// SetRead flips the read label plus the mail.read metadata mirror.
func (s beadStore) SetRead(id string, read bool) error {
	if read {
		return s.store.Update(id, beads.UpdateOpts{
			Labels:   []string{"read"},
			Metadata: map[string]string{"mail.read": "true"},
		})
	}
	return s.store.Update(id, beads.UpdateOpts{
		RemoveLabels: []string{"read"},
		Metadata:     map[string]string{"mail.read": "false"},
	})
}

// Delete removes the message bead outright.
func (s beadStore) Delete(id string) error {
	return s.store.Delete(id)
}

// ListOpenForRecipients returns open message beads assigned to any route.
// TierBoth is one logical query; BdStore may satisfy it with separate
// issue-tier and wisp-tier reads before deduping. Empty routes return all
// open messages. Live reads are required so command-visible mail sees fresh
// wisps even when the active store cache was primed earlier. The read
// filter keys on the raw label, matching the historical inbox shape.
func (s beadStore) ListOpenForRecipients(routes []string, includeRead bool) ([]Record, error) {
	candidates, err := s.messageCandidatesAll(routes)
	if err != nil {
		return nil, err
	}
	var out []Record
	for _, b := range candidates {
		if b.Status != "open" {
			continue
		}
		if len(routes) > 0 && !matchesRecipientRoute(routes, b.Assignee) {
			continue
		}
		if !includeRead && hasLabel(b.Labels, "read") {
			continue
		}
		out = append(out, beadToRecord(b))
	}
	return out, nil
}

// ListThread returns the open messages carrying threadID, oldest first.
func (s beadStore) ListThread(threadID string) ([]Record, error) {
	bs, err := s.store.List(beads.ListQuery{
		Label:    "thread:" + threadID,
		Type:     messageBeadType,
		Sort:     beads.SortCreatedAsc,
		TierMode: beads.TierBoth,
	})
	if err != nil {
		return nil, err
	}
	out := make([]Record, 0, len(bs))
	for _, b := range bs {
		if b.Status != "open" {
			// Thread listings show only open messages, matching the list
			// views: a closed message bead — legacy close-on-archive remnant
			// or retention-swept — stays out of thread views (a retention-
			// swept message remains resolvable by direct-ID Get).
			continue
		}
		out = append(out, beadToRecord(b))
	}
	// store.List already sorts by SortCreatedAsc with an ID tie-break
	// (sortBeadsForQuery in internal/beads/query.go), so no post-sort here.
	return out, nil
}

// CountOpenForRecipients tallies open totals and label-unread counts over
// the candidate scan, byte-identical to the prior CountRecipients loop.
func (s beadStore) CountOpenForRecipients(routes []string) (total, unread int, err error) {
	candidates, err := s.messageCandidatesAll(routes)
	if err != nil {
		return 0, 0, err
	}
	for _, b := range candidates {
		if b.Status != "open" {
			continue
		}
		if len(routes) > 0 && !matchesRecipientRoute(routes, b.Assignee) {
			continue
		}
		total++
		if !hasLabel(b.Labels, "read") {
			unread++
		}
	}
	return total, unread, nil
}

// ListReadCreatedBefore lists read message beads created before `before`,
// oldest first — the candidate set for the stale-mail retention sweep. The
// message-bead query shape (Type + "read" label) stays confined to this
// package. limit == 0 means unbounded.
func (s beadStore) ListReadCreatedBefore(before time.Time, limit int) ([]Record, error) {
	bs, err := s.store.List(beads.ListQuery{
		Type:          messageBeadType,
		Label:         "read",
		CreatedBefore: before,
		Limit:         limit,
		Sort:          beads.SortCreatedAsc,
		TierMode:      beads.TierBoth,
	})
	if err != nil {
		return nil, err
	}
	out := make([]Record, 0, len(bs))
	for _, b := range bs {
		out = append(out, beadToRecord(b))
	}
	return out, nil
}

// CloseReadWithReason stamps close_reason and closes the bead; the two-step
// error vocabulary is the retention sweep's established per-bead reporting.
func (s beadStore) CloseReadWithReason(id, reason string) error {
	if err := s.store.SetMetadata(id, "close_reason", reason); err != nil {
		return fmt.Errorf("mail %s: set close_reason: %w", id, err)
	}
	if err := s.store.Close(id); err != nil {
		return fmt.Errorf("mail %s: close: %w", id, err)
	}
	return nil
}

// PurgeReadCreatedBefore deletes read message beads in the wisp tier (open
// or closed) created before cutoff — the wisp-GC retention sweep for
// consumed mail. Each bead's dependencies are stripped before it is deleted
// (dependency-free single-row message beads make the strip a no-op in
// practice, but it preserves the retention delete semantics). Beads with a
// zero or not-yet-past CreatedAt are skipped. Per-bead delete failures are
// joined and returned without aborting the sweep.
func (s beadStore) PurgeReadCreatedBefore(cutoff time.Time) (int, error) {
	entries, err := s.store.List(beads.ListQuery{
		Type:          messageBeadType,
		Metadata:      map[string]string{mail.ReadMetadataKey: "true"},
		IncludeClosed: true,
		TierMode:      beads.TierWisps,
	})
	if err != nil {
		return 0, fmt.Errorf("listing read message wisps: %w", err)
	}
	purged := 0
	var deleteErr error
	for _, entry := range entries {
		if entry.CreatedAt.IsZero() || !entry.CreatedAt.Before(cutoff) {
			continue
		}
		if err := deleteMessageWispBead(s.store, entry.ID); err != nil {
			deleteErr = errors.Join(deleteErr, fmt.Errorf("deleting expired bead %q: %w", entry.ID, err))
			continue
		}
		purged++
	}
	return purged, deleteErr
}

// messageCandidatesAll returns all open message beads matching any route.
func (s beadStore) messageCandidatesAll(routes []string) ([]beads.Bead, error) {
	query := beads.ListQuery{
		Type:     messageBeadType,
		Status:   "open",
		TierMode: beads.TierBoth,
		Live:     true,
	}
	if len(routes) > 0 {
		query.Assignees = routes
	} else {
		query.AllowScan = true
	}
	all, err := s.store.List(query)
	if err != nil {
		return nil, fmt.Errorf("scanning message beads: %w", err)
	}
	return all, nil
}
