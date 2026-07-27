package graphstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// AcquireWriterLease acquires a stream lease, or reacquires an expired lease,
// and advances its monotonic fencing epoch.
func (s *Store) AcquireWriterLease(
	ctx context.Context,
	streamID string,
	holder string,
	ttl time.Duration,
) (WriterLease, error) {
	if streamID == "" || holder == "" {
		return WriterLease{}, fmt.Errorf("graphstore: acquire lease: empty stream id or holder")
	}

	var lease WriterLease
	err := s.write(ctx, func(tx *sql.Tx) error {
		now := time.Now().UTC()
		expiresAt := now.Add(ttl)
		expiresText := expiresAt.Format(time.RFC3339Nano)

		var currentHolder, currentExpiry string
		var currentEpoch uint64
		err := tx.QueryRowContext(ctx,
			`SELECT holder, epoch, expires_at
			   FROM writer_lease
			  WHERE stream_id = ?`,
			streamID,
		).Scan(&currentHolder, &currentEpoch, &currentExpiry)
		if errors.Is(err, sql.ErrNoRows) {
			if _, err := tx.ExecContext(ctx,
				`INSERT INTO writer_lease(stream_id, holder, epoch, expires_at)
				 VALUES (?, ?, 1, ?)`,
				streamID, holder, expiresText,
			); err != nil {
				return fmt.Errorf("graphstore: acquire lease %q: insert: %w", streamID, err)
			}
			lease = WriterLease{
				StreamID:  streamID,
				Holder:    holder,
				Epoch:     1,
				ExpiresAt: expiresAt,
			}
			return nil
		}
		if err != nil {
			return fmt.Errorf("graphstore: acquire lease %q: read: %w", streamID, err)
		}
		if currentHolder != holder && !expired(currentExpiry, now) {
			return fmt.Errorf(
				"graphstore: acquire lease %q held by %q until %s: %w",
				streamID, currentHolder, currentExpiry, ErrLeaseHeld,
			)
		}

		lease = WriterLease{
			StreamID:  streamID,
			Holder:    holder,
			Epoch:     currentEpoch + 1,
			ExpiresAt: expiresAt,
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE writer_lease
			    SET holder = ?, epoch = ?, expires_at = ?
			  WHERE stream_id = ?`,
			lease.Holder, lease.Epoch, expiresText, streamID,
		); err != nil {
			return fmt.Errorf("graphstore: acquire lease %q: update: %w", streamID, err)
		}
		return nil
	})
	if err != nil {
		return WriterLease{}, err
	}
	return lease, nil
}

// RenewWriterLease extends a lease without changing its epoch.
func (s *Store) RenewWriterLease(
	ctx context.Context,
	lease WriterLease,
	ttl time.Duration,
) (WriterLease, error) {
	expiresAt := time.Now().UTC().Add(ttl)
	err := s.write(ctx, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx,
			`UPDATE writer_lease
			    SET expires_at = ?
			  WHERE stream_id = ? AND holder = ? AND epoch = ?`,
			expiresAt.Format(time.RFC3339Nano),
			lease.StreamID,
			lease.Holder,
			lease.Epoch,
		)
		if err != nil {
			return fmt.Errorf("graphstore: renew lease %q: %w", lease.StreamID, err)
		}
		updated, err := result.RowsAffected()
		if err != nil {
			return fmt.Errorf("graphstore: renew lease %q: rows: %w", lease.StreamID, err)
		}
		if updated == 0 {
			return fmt.Errorf(
				"graphstore: renew lease %q at epoch %d: %w",
				lease.StreamID, lease.Epoch, ErrLeaseFenced,
			)
		}
		return nil
	})
	if err != nil {
		return WriterLease{}, err
	}
	lease.ExpiresAt = expiresAt
	return lease, nil
}

// ReleaseWriterLease expires a lease in place so its fencing epoch is retained.
func (s *Store) ReleaseWriterLease(ctx context.Context, lease WriterLease) error {
	return s.write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			`UPDATE writer_lease
			    SET expires_at = ?
			  WHERE stream_id = ? AND holder = ? AND epoch = ?`,
			time.Unix(0, 0).UTC().Format(time.RFC3339Nano),
			lease.StreamID,
			lease.Holder,
			lease.Epoch,
		)
		if err != nil {
			return fmt.Errorf("graphstore: release lease %q: %w", lease.StreamID, err)
		}
		return nil
	})
}

// CurrentLeaseEpoch returns the stream's current fencing epoch, or zero when
// it has never had a writer lease. Release preserves the row and epoch.
func (s *Store) CurrentLeaseEpoch(ctx context.Context, streamID string) (uint64, error) {
	var epoch uint64
	err := s.ReadDB().QueryRowContext(ctx,
		`SELECT epoch FROM writer_lease WHERE stream_id = ?`,
		streamID,
	).Scan(&epoch)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf(
			"graphstore: current lease epoch of %q: %w",
			streamID,
			err,
		)
	}
	return epoch, nil
}

func expired(value string, now time.Time) bool {
	expiry, err := time.Parse(time.RFC3339Nano, value)
	return err != nil || !expiry.After(now)
}
