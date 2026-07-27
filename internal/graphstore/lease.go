package graphstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"time"
)

// WriterLease is the opaque fencing token required to append after genesis.
type WriterLease struct {
	stream StreamAddress
	holder string
	epoch  uint64
}

// Epoch returns this lease's monotonic fencing epoch.
func (l WriterLease) Epoch() uint64 {
	return l.epoch
}

// AcquireLease obtains the sole lease for an existing stream. It advances the
// fencing epoch on every successful acquisition, including same-holder renewal.
func (s *Store) AcquireLease(ctx context.Context, stream StreamAddress, holder string, ttl time.Duration) (WriterLease, error) {
	if err := requireStream(stream); err != nil {
		return WriterLease{}, err
	}
	if holder == "" {
		return WriterLease{}, fmt.Errorf("graphstore: acquire lease: holder is required")
	}
	if ttl <= 0 {
		return WriterLease{}, fmt.Errorf("graphstore: acquire lease: ttl must be positive")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return WriterLease{}, fmt.Errorf("graphstore: acquire lease: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	head, err := streamHead(ctx, tx, stream)
	if err != nil {
		return WriterLease{}, err
	}
	if head == 0 {
		return WriterLease{}, fmt.Errorf("graphstore: acquire lease for %q: %w", stream.value, ErrStreamNotFound)
	}
	now := s.now().UTC()
	expiresAt := now.Add(ttl)
	var currentHolder string
	var currentEpoch uint64
	var currentExpiry int64
	err = tx.QueryRowContext(ctx,
		`SELECT holder, epoch, expires_at FROM writer_lease WHERE stream = ?`, stream.value,
	).Scan(&currentHolder, &currentEpoch, &currentExpiry)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO writer_lease(stream, holder, epoch, expires_at) VALUES (?, ?, 1, ?)`,
			stream.value, holder, expiresAt.UnixNano()); err != nil {
			return WriterLease{}, fmt.Errorf("graphstore: acquire lease: %w", err)
		}
		currentEpoch = 1
	case err != nil:
		return WriterLease{}, fmt.Errorf("graphstore: acquire lease: %w", err)
	case currentExpiry > now.UnixNano() && currentHolder != holder:
		return WriterLease{}, fmt.Errorf("graphstore: acquire lease for %q: %w", stream.value, ErrLeaseHeld)
	default:
		if currentEpoch >= uint64(math.MaxInt64) {
			return WriterLease{}, fmt.Errorf("graphstore: acquire lease: epoch overflow")
		}
		currentEpoch++
		if _, err := tx.ExecContext(ctx,
			`UPDATE writer_lease SET holder = ?, epoch = ?, expires_at = ? WHERE stream = ?`,
			holder, currentEpoch, expiresAt.UnixNano(), stream.value); err != nil {
			return WriterLease{}, fmt.Errorf("graphstore: acquire lease: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return WriterLease{}, fmt.Errorf("graphstore: acquire lease: commit: %w", err)
	}
	return WriterLease{stream: stream, holder: holder, epoch: currentEpoch}, nil
}

func (s *Store) requireLease(ctx context.Context, tx *sql.Tx, stream StreamAddress, lease WriterLease) error {
	if lease.stream != stream {
		return fmt.Errorf("graphstore: append: lease stream does not match: %w", ErrLeaseFenced)
	}
	var holder string
	var epoch uint64
	var expiresAt int64
	err := tx.QueryRowContext(ctx,
		`SELECT holder, epoch, expires_at FROM writer_lease WHERE stream = ?`, stream.value,
	).Scan(&holder, &epoch, &expiresAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("graphstore: append: no lease: %w", ErrLeaseFenced)
		}
		return fmt.Errorf("graphstore: append: read lease: %w", err)
	}
	if holder != lease.holder || epoch != lease.epoch || expiresAt <= s.now().UTC().UnixNano() {
		return fmt.Errorf("graphstore: append: stale lease: %w", ErrLeaseFenced)
	}
	return nil
}
