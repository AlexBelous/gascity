package nudgesdb

// Legacy-queue import (design "Migration & cutover" row 2: drain-or-import
// live queue items; shadow history ≤24h). These are the primitives the
// cmd/gc migration slice drives: verbatim inserts that preserve legacy ids
// and clocks, idempotent via INSERT OR IGNORE so interrupted first boots and
// straggler re-imports simply resume.

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/gastownhall/gascity/internal/nudgequeue"
)

// ImportItem inserts a legacy queue item verbatim under the given bucket
// ("pending" | "in_flight" | "dead"), preserving its id, clocks, attempts,
// and BeadID (the bead_id transition column keeps the Item projection
// lossless). Existing rows are left untouched (INSERT OR IGNORE), so
// re-import is idempotent. Dead items keep their DeadAt/LastError; the
// maintenance pass stamps their terminal vocabulary when they age.
func (s *Store) ImportItem(item nudgequeue.Item, queueState string) error {
	switch queueState {
	case statePending, stateInFlight, stateDead:
	default:
		return fmt.Errorf("importing nudge %q: invalid queue state %q", item.ID, queueState)
	}
	if item.ID == "" {
		return fmt.Errorf("importing nudge: empty id")
	}
	return s.db.Write(context.Background(), func(tx *sql.Tx) error {
		return insertItem(tx, item, queueState)
	})
}

// ImportTerminalShadow inserts a terminal row reconstructed from a legacy
// shadow bead, so wait finalization that runs after the cutover still reads
// the terminal stamps of nudges delivered before it. createdAt and
// terminalAt preserve the legacy clocks (a zero terminalAt falls back to
// createdAt, keeping the row sweepable by the retention TTL). Existing rows
// are left untouched, so a live imported item is never demoted to terminal
// by a stale shadow of the same id.
func (s *Store) ImportTerminalShadow(shadow nudgequeue.NudgeShadow, createdAt, terminalAt time.Time) error {
	if shadow.ID == "" {
		return fmt.Errorf("importing terminal nudge shadow: empty id")
	}
	if terminalAt.IsZero() {
		terminalAt = createdAt
	}
	item := nudgequeue.Item{
		ID:           shadow.ID,
		BeadID:       shadow.BeadID,
		Agent:        shadow.Agent,
		SessionID:    shadow.SessionID,
		Source:       shadow.Source,
		Message:      shadow.Message,
		Reference:    shadow.Reference,
		CreatedAt:    createdAt,
		DeliverAfter: shadow.DeliverAfter,
		ExpiresAt:    shadow.ExpiresAt,
	}
	return s.db.Write(context.Background(), func(tx *sql.Tx) error {
		if err := insertItem(tx, item, stateTerminal); err != nil {
			return err
		}
		_, err := tx.Exec(
			`UPDATE nudges SET terminal_state = ?, terminal_reason = ?, commit_boundary = ?, terminal_at = ?
			 WHERE id = ? AND queue_state = ? AND terminal_state = ''`,
			shadow.State, shadow.TerminalReason, shadow.CommitBoundary, nanos(terminalAt),
			shadow.ID, stateTerminal,
		)
		return err
	})
}
