package beads

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/gastownhall/gascity/internal/beadmeta"

	beadslib "github.com/steveyegge/beads"
)

// BlockerPairSnapshot is the native row and exact blocker binding observed by
// an evidence validator. A release must compare every field again in one
// transaction; a one-key metadata CAS cannot clear the two blocker keys safely.
type BlockerPairSnapshot struct {
	// Revision is an opaque non-zero token; negative values are valid.
	Revision int64
	Text     string
	Typed    string
	Route    string
	Owner    string
	GateID   string
}

// ClearBlockerPairIfMatch is a storage primitive, not release authorization.
// Callers must first validate the primary source predicate and then read back
// the parent. Unsupported stores must not emulate this with two key writes.
func (s *NativeDoltStore) ClearBlockerPairIfMatch(id string, expected BlockerPairSnapshot) (bool, error) {
	if err := s.readOnlyGuard(); err != nil {
		return false, err
	}
	if id == "" || expected.Revision == 0 || expected.Text == "" || expected.Typed == "" ||
		expected.Route == "" || expected.Owner == "" {
		return false, fmt.Errorf("clear blocker pair: incomplete exact snapshot")
	}
	storage, release, err := s.acquireStorage()
	if err != nil {
		return false, err
	}
	defer release()
	ctx, cancel := nativeDoltOperationContext(context.TODO())
	defer cancel()

	swapped := false
	err = storage.RunInTransaction(ctx, fmt.Sprintf("gc: clear exact blocker pair on bead %s", id), func(tx beadslib.Transaction) error {
		// The storage layer may retry this callback after a serialization error.
		swapped = false
		issue, err := tx.GetIssue(ctx, id)
		if err != nil {
			return nativeStoreError(id, err)
		}
		if issue == nil {
			return fmt.Errorf("clear blocker pair on %q: %w", id, ErrNotFound)
		}
		if issue.RowVersion != expected.Revision || issue.Status == beadslib.StatusClosed ||
			issue.Assignee != "" {
			return nil
		}
		// Dolt transactional GetIssue does not hydrate labels. Read them in
		// this same transaction; unverifiable holds must fail closed.
		labels, err := tx.GetLabels(ctx, id)
		if err != nil {
			return fmt.Errorf("reading holds for bead %q: %w", id, err)
		}
		for _, label := range labels {
			if label == "hold:mayor" || label == "hold:external" {
				return nil
			}
		}
		metadata, err := metadataMapFromNative(issue.Metadata)
		if err != nil {
			return fmt.Errorf("parsing metadata for bead %q: %w", id, err)
		}
		if metadata[beadmeta.BlockedOnMetadataKey] != expected.Text ||
			metadata[beadmeta.BlockerV2MetadataKey] != expected.Typed ||
			metadata[beadmeta.RoutedToMetadataKey] != expected.Route ||
			metadata[beadmeta.NextOwnerMetadataKey] != expected.Owner ||
			metadata["workflow.gate_id"] != expected.GateID {
			return nil
		}
		rawMetadata, err := metadataRawValuesFromNative(issue.Metadata)
		if err != nil {
			return fmt.Errorf("parsing raw metadata for bead %q: %w", id, err)
		}
		delete(rawMetadata, beadmeta.BlockedOnMetadataKey)
		delete(rawMetadata, beadmeta.BlockerV2MetadataKey)
		rawBytes, err := json.Marshal(rawMetadata)
		if err != nil {
			return fmt.Errorf("marshaling metadata: %w", err)
		}
		if err := tx.UpdateIssue(ctx, id, map[string]interface{}{
			"metadata": json.RawMessage(rawBytes),
		}, s.actor); err != nil {
			return nativeStoreError(id, err)
		}
		result, err := tx.GetIssue(ctx, id)
		if err != nil {
			return nativeStoreError(id, err)
		}
		if result == nil {
			return fmt.Errorf("readback of blocker pair on %q: %w", id, ErrNotFound)
		}
		resultMetadata, err := metadataMapFromNative(result.Metadata)
		if err != nil {
			return fmt.Errorf("readback metadata for bead %q: %w", id, err)
		}
		if _, exists := resultMetadata[beadmeta.BlockedOnMetadataKey]; exists {
			return fmt.Errorf("blocker text survived paired release on %q", id)
		}
		if _, exists := resultMetadata[beadmeta.BlockerV2MetadataKey]; exists {
			return fmt.Errorf("typed blocker survived paired release on %q", id)
		}
		swapped = true
		return nil
	})
	if err != nil {
		return false, err
	}
	return swapped, nil
}
