package beads

import (
	"context"
	"errors"
	"fmt"
	"strings"

	beadslib "github.com/steveyegge/beads"
)

// ClassificationFingerprinter reports a value that changes whenever any table
// in the store's working set changes, committed or not. Two equal values mean
// no row the classification census reads can have changed between them.
// Unsupported stores must report ErrClassificationUnsupported, and a read
// failure must never be represented as a stable fingerprint.
type ClassificationFingerprinter interface {
	ClassificationFingerprint() (string, error)
}

var _ ClassificationFingerprinter = (*NativeDoltStore)(nil)

// nativeWorkingSetHashQuery hashes every table of the working set, so any write
// — including an uncommitted one — produces a new value.
const nativeWorkingSetHashQuery = "SELECT DOLT_HASHOF_DB()"

// ClassificationFingerprint returns the Dolt working-set hash of the store.
func (s *NativeDoltStore) ClassificationFingerprint() (string, error) {
	var hash string
	err := s.withReadRetry(func(ctx context.Context, storage beadslib.Storage) error {
		provider, ok := storage.(nativeClassificationQuerier)
		if !ok {
			return ErrClassificationUnsupported
		}
		value, err := readNativeWorkingSetHash(ctx, provider)
		if err != nil {
			return err
		}
		hash = value
		return nil
	})
	if err != nil {
		if errors.Is(err, ErrClassificationUnsupported) {
			return "", err
		}
		return "", fmt.Errorf("reading working-set fingerprint: %w", err)
	}
	return hash, nil
}

func readNativeWorkingSetHash(ctx context.Context, provider nativeClassificationQuerier) (string, error) {
	rows, err := provider.QueryContext(ctx, nativeWorkingSetHashQuery)
	if err != nil {
		return "", err
	}
	defer func() { _ = rows.Close() }()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return "", err
		}
		return "", errors.New("working-set hash query returned no row")
	}
	var hash string
	if err := rows.Scan(&hash); err != nil {
		return "", err
	}
	if rows.Next() {
		return "", errors.New("working-set hash query returned more than one row")
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	hash = strings.TrimSpace(hash)
	if hash == "" {
		return "", errors.New("working-set hash query returned an empty hash")
	}
	return hash, nil
}
