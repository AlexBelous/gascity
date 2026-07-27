package graphstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/gastownhall/gascity/internal/graphstore/canon"
)

// TruncateBelowAnchor removes journal rows through anchorSeq only when a
// durable snapshot covers that exact sequence. The covering snapshot records
// the removed prefix's final chain hash so Verify can continue across the cut.
func (s *Store) TruncateBelowAnchor(
	ctx context.Context,
	streamID string,
	anchorSeq uint64,
) (int64, error) {
	if streamID == "" {
		return 0, fmt.Errorf("graphstore: truncate: empty stream id")
	}
	if anchorSeq == 0 {
		return 0, fmt.Errorf("graphstore: truncate %q: anchor seq is zero", streamID)
	}

	var deleted int64
	err := s.write(ctx, func(tx *sql.Tx) error {
		var state, stateHash []byte
		err := tx.QueryRowContext(ctx,
			`SELECT state, state_hash
			   FROM snapshots
			  WHERE stream_id = ? AND covered_seq = ?`,
			streamID,
			anchorSeq,
		).Scan(&state, &stateHash)
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf(
				"graphstore: truncate %q at %d: %w",
				streamID,
				anchorSeq,
				ErrNoCoveringSnapshot,
			)
		}
		if err != nil {
			return fmt.Errorf("graphstore: truncate %q: read snapshot: %w", streamID, err)
		}
		if len(stateHash) != 32 {
			return fmt.Errorf(
				"graphstore: truncate %q at %d: snapshot hash has %d bytes: %w",
				streamID,
				anchorSeq,
				len(stateHash),
				ErrSnapshotHashMismatch,
			)
		}
		var expectedHash [32]byte
		copy(expectedHash[:], stateHash)
		if canon.Hash(state) != expectedHash {
			return fmt.Errorf(
				"graphstore: truncate %q at %d: %w",
				streamID,
				anchorSeq,
				ErrSnapshotHashMismatch,
			)
		}

		var cutChainHash []byte
		err = tx.QueryRowContext(ctx,
			`SELECT chain_hash
			   FROM journal
			  WHERE stream_id = ? AND seq = ?`,
			streamID,
			anchorSeq,
		).Scan(&cutChainHash)
		if errors.Is(err, sql.ErrNoRows) {
			deleted = 0
			return nil
		}
		if err != nil {
			return fmt.Errorf(
				"graphstore: truncate %q: read cut at %d: %w",
				streamID,
				anchorSeq,
				err,
			)
		}

		if err := openSnapshotGate(ctx, tx); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE snapshots
			    SET cut_chain_hash = ?
			  WHERE stream_id = ? AND covered_seq = ?`,
			cutChainHash,
			streamID,
			anchorSeq,
		); err != nil {
			return errors.Join(
				fmt.Errorf("graphstore: truncate %q: record cut hash: %w", streamID, err),
				closeSnapshotGate(ctx, tx),
			)
		}
		if err := closeSnapshotGate(ctx, tx); err != nil {
			return err
		}

		if _, err := tx.ExecContext(ctx,
			`INSERT INTO retention_gate(stream_id, max_seq)
			 VALUES (?, ?)
			 ON CONFLICT(stream_id) DO UPDATE SET max_seq = excluded.max_seq`,
			streamID,
			anchorSeq,
		); err != nil {
			return fmt.Errorf("graphstore: truncate %q: open retention gate: %w", streamID, err)
		}
		result, err := tx.ExecContext(ctx,
			`DELETE FROM journal WHERE stream_id = ? AND seq <= ?`,
			streamID,
			anchorSeq,
		)
		if err != nil {
			return fmt.Errorf(
				"graphstore: truncate %q: delete through %d: %w",
				streamID,
				anchorSeq,
				err,
			)
		}
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM retention_gate WHERE stream_id = ?`,
			streamID,
		); err != nil {
			return fmt.Errorf("graphstore: truncate %q: close retention gate: %w", streamID, err)
		}
		deleted, err = result.RowsAffected()
		if err != nil {
			return fmt.Errorf("graphstore: truncate %q: rows affected: %w", streamID, err)
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return deleted, nil
}

func (s *Store) cutChainHashAt(
	ctx context.Context,
	streamID string,
	coveredSeq uint64,
) ([32]byte, bool, error) {
	var value []byte
	err := s.ReadDB().QueryRowContext(ctx,
		`SELECT cut_chain_hash
		   FROM snapshots
		  WHERE stream_id = ? AND covered_seq = ?`,
		streamID,
		coveredSeq,
	).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) || value == nil {
		return [32]byte{}, false, nil
	}
	if err != nil {
		return [32]byte{}, false, fmt.Errorf(
			"graphstore: cut chain hash %q at %d: %w",
			streamID,
			coveredSeq,
			err,
		)
	}
	if len(value) != 32 {
		return [32]byte{}, false, fmt.Errorf(
			"graphstore: cut chain hash %q at %d has %d bytes",
			streamID,
			coveredSeq,
			len(value),
		)
	}
	var hash [32]byte
	copy(hash[:], value)
	return hash, true, nil
}
