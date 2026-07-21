package beadmail

// The messages-backend seam (engdocs/plans/infra-class-sqlite-stores/
// P3-MESSAGING-SEAM-PLAN.md, slice 1): the NAMED persistence operations of
// the mail store, extracted from the Provider's direct beads.Store calls.
// The bd backend (backend_bead.go) carries the moved Message⇄bead codec;
// the embedded-SQLite messages table implements the same interface when
// [beads.classes.messaging] relocates the class. Everything that is NOT
// message persistence stays on the Provider: recipient-route expansion and
// sender resolution (the SESSION-class store), title derivation, the
// removed-message addressability gate, and per-operation error vocabulary.

import (
	"fmt"
	"time"

	"github.com/gastownhall/gascity/internal/mail"
)

// Record is the persistence-edge view of one stored message: the design's
// messages-row shape plus the bd-compat decode-only fields. FromAddr/ToAddr
// carry the raw stored addresses; the display fields override them in the
// mail.Message projection (Message()).
type Record struct {
	ID        string
	ThreadID  string
	ReplyToID string
	// FromAddr / ToAddr are the raw stored sender/recipient addresses
	// (bead From / Assignee on bd; from_addr / to_addr columns on sqlite).
	FromAddr string
	ToAddr   string
	// FromSessionID / FromDisplay / ToSessionID / ToDisplay are the
	// reply-routing and display fields (mail.* metadata on bd).
	FromSessionID string
	FromDisplay   string
	ToSessionID   string
	ToDisplay     string
	Subject       string
	Body          string
	CreatedAt     time.Time
	// Read is the message's read state as the domain sees it (the
	// metadata-wins reconciliation on bd; the read column on sqlite).
	Read bool
	// ReadLabel is the raw read-label bit the bd backend's conditional
	// mark-read write keys on; backends without the label/metadata split
	// set it equal to Read.
	ReadLabel bool
	// Open reports whether the message is open (not closed). CloseReason
	// carries the close vocabulary for closed messages — the
	// RetentionSweepCloseReason marker distinguishes system-aged from
	// legacy user-removed closes (the Provider's addressability gate).
	Open        bool
	CloseReason string
	// AutoHandoff / ArchiveAfterInject are the handoff delivery flags
	// (gc:auto-handoff / gc:archive-after-inject labels on bd).
	AutoHandoff        bool
	ArchiveAfterInject bool
	// Priority / CC are decode-only bd fields with no in-tree producer
	// (design open question 2); the sqlite backend reports zero values.
	Priority int
	CC       []string
}

// Message projects the record onto the domain object, byte-identical to the
// prior beadToMessage: display fields win over raw addresses.
func (r Record) Message() mail.Message {
	from := r.FromAddr
	if r.FromDisplay != "" {
		from = r.FromDisplay
	}
	to := r.ToAddr
	if r.ToDisplay != "" {
		to = r.ToDisplay
	}
	return mail.Message{
		ID:        r.ID,
		From:      from,
		To:        to,
		Subject:   r.Subject,
		Body:      r.Body,
		CreatedAt: r.CreatedAt,
		Read:      r.Read,
		ThreadID:  r.ThreadID,
		ReplyTo:   r.ReplyToID,
		Priority:  r.Priority,
		CC:        r.CC,
	}
}

// NewMessage is the create shape every mail-creating path (Send,
// SendHandoff, Reply) funnels through: already-resolved fields only — the
// Provider resolves sender routes and derives titles before this point.
type NewMessage struct {
	Subject string
	Body    string
	From    string
	To      string
	// ThreadID is always set by the Provider (generated or inherited);
	// ReplyToID is set on replies.
	ThreadID  string
	ReplyToID string
	// Reply-routing / display metadata (empty fields are omitted from
	// storage, matching the prior conditional metadata map).
	FromSessionID string
	FromDisplay   string
	ToSessionID   string
	ToDisplay     string
	// ExtraLabels is the bd-only passthrough for HandoffIntent.ExtraLabels
	// (in-tree callers pass only the auto-handoff / archive-after-inject
	// flag labels, which the sqlite backend maps to its two flag columns;
	// other extra labels are a bd-backend concept and are dropped there).
	ExtraLabels []string
}

// NotAMessageError reports that an id resolved to a stored record of a
// different class (a non-message bead on bd). The Provider formats it with
// each operation's established vocabulary; the sqlite backend never
// produces it (foreign classes are invisible there).
type NotAMessageError struct {
	ID   string
	Type string
}

func (e NotAMessageError) Error() string {
	return fmt.Sprintf("bead %s is type %q, not message", e.ID, e.Type)
}

// messagesBackend is the mail persistence authority behind the Provider.
// The bd backend implements it over Type="message" wisp beads; the sqlite
// backend implements it over the messages table. The type is unexported on
// purpose: consumers hold *Provider, never a backend; method names are
// exported so another package can satisfy it structurally.
type messagesBackend interface {
	// Create persists msg and returns its stored record.
	Create(msg NewMessage) (Record, error)
	// Get returns the record for id, reporting found=false for an absent
	// (or hard-deleted) message and NotAMessageError when id belongs to a
	// different class.
	Get(id string) (Record, bool, error)
	// SetRead flips the read state (unconditionally — the caller owns any
	// only-if-changed shape).
	SetRead(id string, read bool) error
	// Delete removes the message outright (the eager archive).
	Delete(id string) error
	// ListOpenForRecipients returns open messages addressed to any of
	// routes (nil/empty routes = every open message), excluding read
	// messages unless includeRead. The read filter is the backend's raw
	// read bit (the label on bd), matching the historical inbox shape.
	ListOpenForRecipients(routes []string, includeRead bool) ([]Record, error)
	// ListThread returns the open messages carrying threadID, oldest
	// first.
	ListThread(threadID string) ([]Record, error)
	// CountOpenForRecipients returns deduplicated total and unread counts
	// over the open messages addressed to any of routes.
	CountOpenForRecipients(routes []string) (total, unread int, err error)
	// ListReadCreatedBefore returns read messages created before the
	// cutoff, oldest first (limit 0 = unbounded) — the retention
	// candidate read.
	ListReadCreatedBefore(before time.Time, limit int) ([]Record, error)
	// CloseReadWithReason stamps reason as the close vocabulary and closes
	// the message (the retention sweep's transition).
	CloseReadWithReason(id, reason string) error
	// PurgeReadCreatedBefore deletes read messages (open or closed)
	// created before the cutoff, returning the purge count.
	PurgeReadCreatedBefore(cutoff time.Time) (int, error)
}
