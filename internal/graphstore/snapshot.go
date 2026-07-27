package graphstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/gastownhall/gascity/internal/graphstore/canon"
	"github.com/gastownhall/gascity/internal/graphstore/fold"
)

// WriteSnapshot stores a reducer snapshot and appends its anchor event in one
// transaction. The snapshot must cover the current journal head exactly.
func (s *Store) WriteSnapshot(
	ctx context.Context,
	engine string,
	leaseEpoch uint64,
	snapshot fold.Snapshot,
	anchor JournalEvent,
) (uint64, error) {
	if snapshot.StreamID == "" {
		return 0, fmt.Errorf("graphstore: write snapshot: empty stream id")
	}
	if snapshot.CoveredSeq == 0 {
		return 0, fmt.Errorf("graphstore: write snapshot %q: covered seq is zero", snapshot.StreamID)
	}
	if len(snapshot.State) == 0 {
		return 0, fmt.Errorf("graphstore: write snapshot %q: empty state", snapshot.StreamID)
	}
	if canon.Hash(snapshot.State) != snapshot.StateHash {
		return 0, fmt.Errorf(
			"graphstore: write snapshot %q at %d: %w",
			snapshot.StreamID,
			snapshot.CoveredSeq,
			ErrSnapshotHashMismatch,
		)
	}
	if anchor.Payload == nil {
		return 0, fmt.Errorf("graphstore: write snapshot %q: nil anchor payload", snapshot.StreamID)
	}
	if !s.isRegistered(engine, anchor.Type) {
		return 0, fmt.Errorf(
			"graphstore: write snapshot %q: event (%s, %s): %w",
			snapshot.StreamID,
			engine,
			anchor.Type,
			ErrUnknownEventType,
		)
	}

	var anchorSeq uint64
	appendedAt := time.Now().UTC().Format(time.RFC3339Nano)
	err := s.write(ctx, func(tx *sql.Tx) error {
		head, previous, err := headAndChain(ctx, tx, snapshot.StreamID)
		if err != nil {
			return err
		}
		if head != snapshot.CoveredSeq {
			return fmt.Errorf(
				"graphstore: write snapshot %q: covered seq %d, head %d: %w",
				snapshot.StreamID,
				snapshot.CoveredSeq,
				head,
				ErrWrongExpectedVersion,
			)
		}
		if err := checkLeaseEpoch(ctx, tx, snapshot.StreamID, leaseEpoch); err != nil {
			return err
		}

		if err := openSnapshotGate(ctx, tx); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO snapshots (
				stream_id, covered_seq, engine, reducer_version,
				snapshot_format_version, state_hash, state, cut_chain_hash, created_at
			) VALUES (?, ?, ?, ?, ?, ?, ?, NULL, ?)`,
			snapshot.StreamID,
			snapshot.CoveredSeq,
			snapshot.Engine,
			snapshot.ReducerVersion,
			snapshot.SnapshotFormatVersion,
			snapshot.StateHash[:],
			snapshot.State,
			appendedAt,
		); err != nil {
			return errors.Join(
				fmt.Errorf(
					"graphstore: write snapshot %q at %d: %w",
					snapshot.StreamID,
					snapshot.CoveredSeq,
					err,
				),
				closeSnapshotGate(ctx, tx),
			)
		}
		if err := closeSnapshotGate(ctx, tx); err != nil {
			return err
		}

		anchorSeq = head + 1
		payloadHash := canon.Hash(anchor.Payload)
		chain := chainHash(
			previous,
			snapshot.StreamID,
			anchorSeq,
			engine,
			anchor.Type,
			anchor.Substream,
			anchor.IRContractVersion,
			payloadHash,
		)
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO journal (
				stream_id, seq, substream, engine, type, ir_contract_version,
				idem_token, payload, payload_hash, chain_hash, lease_epoch, appended_at
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			snapshot.StreamID,
			anchorSeq,
			anchor.Substream,
			engine,
			anchor.Type,
			anchor.IRContractVersion,
			nullableToken(anchor.IdemToken),
			anchor.Payload,
			payloadHash[:],
			chain[:],
			leaseEpoch,
			appendedAt,
		); err != nil {
			return fmt.Errorf(
				"graphstore: write snapshot %q: append anchor at %d: %w",
				snapshot.StreamID,
				anchorSeq,
				err,
			)
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return anchorSeq, nil
}

// LatestSnapshot returns the snapshot with the highest covered sequence.
func (s *Store) LatestSnapshot(
	ctx context.Context,
	streamID string,
) (fold.Snapshot, bool, error) {
	var (
		snapshot  fold.Snapshot
		stateHash []byte
	)
	snapshot.StreamID = streamID
	err := s.ReadDB().QueryRowContext(ctx,
		`SELECT covered_seq, engine, reducer_version, snapshot_format_version,
		        state_hash, state
		   FROM snapshots
		  WHERE stream_id = ?
		  ORDER BY covered_seq DESC
		  LIMIT 1`,
		streamID,
	).Scan(
		&snapshot.CoveredSeq,
		&snapshot.Engine,
		&snapshot.ReducerVersion,
		&snapshot.SnapshotFormatVersion,
		&stateHash,
		&snapshot.State,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return fold.Snapshot{}, false, nil
	}
	if err != nil {
		return fold.Snapshot{}, false, fmt.Errorf(
			"graphstore: latest snapshot %q: %w",
			streamID,
			err,
		)
	}
	if len(stateHash) != len(snapshot.StateHash) {
		return fold.Snapshot{}, false, fmt.Errorf(
			"graphstore: latest snapshot %q: state hash has %d bytes",
			streamID,
			len(stateHash),
		)
	}
	copy(snapshot.StateHash[:], stateHash)
	return snapshot, true, nil
}

func openSnapshotGate(ctx context.Context, tx *sql.Tx) error {
	if _, err := tx.ExecContext(ctx,
		`UPDATE snapshot_write_gate SET open = 1 WHERE singleton = 0`,
	); err != nil {
		return fmt.Errorf("graphstore: open snapshot write gate: %w", err)
	}
	return nil
}

func closeSnapshotGate(ctx context.Context, tx *sql.Tx) error {
	if _, err := tx.ExecContext(ctx,
		`UPDATE snapshot_write_gate SET open = 0 WHERE singleton = 0`,
	); err != nil {
		return fmt.Errorf("graphstore: close snapshot write gate: %w", err)
	}
	return nil
}
