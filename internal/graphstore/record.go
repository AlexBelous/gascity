// Package graphstore defines the storage-neutral graph journal values shared by
// the journal and its pure consumers.
package graphstore

import (
	"errors"
	"fmt"
	"math"

	"github.com/gastownhall/gascity/internal/graphstore/canon"
)

var (
	// ErrCursorMismatch reports an attempted cursor advance over a prefix that
	// begins after a different committed position.
	ErrCursorMismatch = errors.New("graphstore: cursor does not match prefix")

	// ErrIncompletePrefix reports records that do not form a dense continuation
	// of their starting cursor.
	ErrIncompletePrefix = errors.New("graphstore: records are not a complete prefix")
)

// Record is a validated journal envelope. Its payload is canonical JSON and
// its sequence is positive. Record has no event vocabulary: the kernel owns
// the meaning of Type and Payload.
type Record struct {
	sequence uint64
	typ      string
	payload  []byte
}

// NewRecord validates an envelope and canonicalizes its JSON payload.
func NewRecord(sequence uint64, typ string, payload []byte) (Record, error) {
	if sequence == 0 {
		return Record{}, fmt.Errorf("graphstore: record sequence must be positive")
	}
	if typ == "" {
		return Record{}, fmt.Errorf("graphstore: record type is required")
	}
	if payload == nil {
		return Record{}, fmt.Errorf("graphstore: record payload is required")
	}
	canonical, err := canon.Canonicalize(payload)
	if err != nil {
		return Record{}, fmt.Errorf("graphstore: record payload: %w", err)
	}
	return Record{sequence: sequence, typ: typ, payload: canonical}, nil
}

// Sequence returns the record's committed sequence.
func (r Record) Sequence() uint64 {
	return r.sequence
}

// Type returns the kernel-owned record type.
func (r Record) Type() string {
	return r.typ
}

// Payload returns a copy of the canonical JSON payload.
func (r Record) Payload() []byte {
	return append([]byte(nil), r.payload...)
}

// Cursor identifies a committed record boundary. It advances only over the
// complete prefix that starts at its current sequence.
type Cursor struct {
	sequence uint64
}

// CursorAt returns the cursor after sequence. Zero is the empty-stream cursor.
func CursorAt(sequence uint64) Cursor {
	return Cursor{sequence: sequence}
}

// Sequence returns the last committed sequence covered by the cursor.
func (c Cursor) Sequence() uint64 {
	return c.sequence
}

// Advance returns the prefix's ending cursor when the prefix starts at c.
func (c Cursor) Advance(prefix CompletePrefix) (Cursor, error) {
	if c.sequence != prefix.after.sequence {
		return Cursor{}, fmt.Errorf("graphstore: advance from %d over prefix after %d: %w", c.sequence, prefix.after.sequence, ErrCursorMismatch)
	}
	return prefix.through, nil
}

// CompletePrefix is an immutable dense record sequence that follows after.
// It represents a read that can safely become a cursor value without skipping
// or duplicating a committed record.
type CompletePrefix struct {
	after   Cursor
	through Cursor
	records []Record
}

// NewCompletePrefix validates that records cover exactly (after, through] and
// copies them so later caller mutation cannot affect a replay.
func NewCompletePrefix(after, through Cursor, records []Record) (CompletePrefix, error) {
	if through.sequence < after.sequence {
		return CompletePrefix{}, fmt.Errorf("graphstore: prefix through %d precedes after %d: %w", through.sequence, after.sequence, ErrIncompletePrefix)
	}
	expected := after.sequence
	cloned := make([]Record, len(records))
	for index, record := range records {
		if expected == math.MaxUint64 {
			return CompletePrefix{}, fmt.Errorf("graphstore: prefix sequence overflow: %w", ErrIncompletePrefix)
		}
		expected++
		normalized, err := NewRecord(record.sequence, record.typ, record.payload)
		if err != nil {
			return CompletePrefix{}, fmt.Errorf("graphstore: prefix record %d: %w", index, err)
		}
		if normalized.sequence != expected {
			return CompletePrefix{}, fmt.Errorf("graphstore: prefix record %d has sequence %d, want %d: %w", index, normalized.sequence, expected, ErrIncompletePrefix)
		}
		cloned[index] = normalized
	}
	if expected != through.sequence {
		return CompletePrefix{}, fmt.Errorf("graphstore: prefix ends at %d, through is %d: %w", expected, through.sequence, ErrIncompletePrefix)
	}
	return CompletePrefix{after: after, through: through, records: cloned}, nil
}

// After returns the cursor immediately before this complete prefix.
func (p CompletePrefix) After() Cursor {
	return p.after
}

// Through returns the cursor after every record in this complete prefix.
func (p CompletePrefix) Through() Cursor {
	return p.through
}

// Records returns copies of the records in sequence order.
func (p CompletePrefix) Records() []Record {
	records := make([]Record, len(p.records))
	for index, record := range p.records {
		records[index] = Record{sequence: record.sequence, typ: record.typ, payload: record.Payload()}
	}
	return records
}
