package beads

import (
	"context"
	"fmt"

	beadslib "github.com/steveyegge/beads"
)

var _ ClassificationReader = (*NativeDoltStore)(nil)

// ReadClassification reads fresh class inputs without hydrating bodies or
// dependencies. Labels, metadata, closed residents and both tiers are required:
// any of them may identify infrastructure stranded in the retained work store.
func (s *NativeDoltStore) ReadClassification() ([]ClassificationRow, error) {
	var result []ClassificationRow
	err := s.withReadRetry(func(ctx context.Context, storage beadslib.Storage) error {
		filter := nativeIssueFilterFromListQuery(ListQuery{IncludeClosed: true, TierMode: TierBoth})
		filter.Lite = true
		filter.IncludeDependencies = false
		issues, err := storage.SearchIssues(ctx, "", filter)
		if err != nil {
			return err
		}
		rows := make([]ClassificationRow, 0, len(issues))
		for _, issue := range issues {
			if issue == nil {
				return fmt.Errorf("nil issue in classification census")
			}
			metadata, err := metadataMapFromNative(issue.Metadata)
			if err != nil {
				// An unreadable class is unknown, never evidence of no stranded row.
				return fmt.Errorf("parsing metadata for bead %q: %w: %w", issue.ID, errNativeIssueMetadataParse, err)
			}
			rows = append(rows, ClassificationRow{
				ID: issue.ID, Type: string(issue.IssueType),
				Labels: append([]string(nil), issue.Labels...), Metadata: metadata,
			})
		}
		s.noteRows(len(issues))
		result = rows
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("reading native classification census: %w", err)
	}
	return result, nil
}
