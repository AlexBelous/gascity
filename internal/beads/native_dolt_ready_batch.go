package beads

import (
	"context"
	"fmt"

	beadslib "github.com/steveyegge/beads"
)

// nativeDependencyBatchReader is the optional pinned Beads storage capability.
// Its implementation partitions source IDs by their live wisp membership and
// reads each dependency table in bounded IN-clause batches. Do not merge both
// tables here: a source must use the same authoritative plane as the single-ID
// GetDependenciesWithMetadata read.
type nativeDependencyBatchReader interface {
	GetDependencyRecordsForIssues(context.Context, []string) (map[string][]*beadslib.Dependency, error)
}

func nativeReadyOutcomeDependencies(ctx context.Context, storage beadslib.Storage, candidates []Bead) (map[string][]*beadslib.IssueWithDependencyMetadata, bool, error) {
	reader, ok := storage.(nativeDependencyBatchReader)
	if !ok {
		return nil, false, nil
	}
	ids := make([]string, len(candidates))
	for i, candidate := range candidates {
		ids[i] = candidate.ID
	}
	edges, err := reader.GetDependencyRecordsForIssues(ctx, ids)
	if err != nil {
		return nil, true, fmt.Errorf("checking blocking dependency outcomes: reading dependency batch: %w", err)
	}
	// Hydrate all targets, including non-blocking edges, just as the single-ID
	// method does. Besides retaining read errors, this preserves its missing-target
	// behavior. GetIssuesByIDs also partitions wisps and bounds its SQL batches.
	var targetIDs []string
	seen := make(map[string]bool)
	for _, id := range ids {
		for _, edge := range edges[id] {
			if edge != nil && !seen[edge.DependsOnID] {
				seen[edge.DependsOnID] = true
				targetIDs = append(targetIDs, edge.DependsOnID)
			}
		}
	}
	var issues []*beadslib.Issue
	if len(targetIDs) > 0 {
		issues, err = storage.GetIssuesByIDs(ctx, targetIDs)
		if err != nil {
			return nil, true, fmt.Errorf("checking blocking dependency outcomes: reading blocker batch: %w", err)
		}
	}
	targets := make(map[string]*beadslib.Issue, len(issues))
	for _, issue := range issues {
		if issue != nil {
			targets[issue.ID] = issue
		}
	}
	result := make(map[string][]*beadslib.IssueWithDependencyMetadata, len(ids))
	for _, id := range ids {
		for _, edge := range edges[id] {
			if edge == nil {
				continue
			}
			// Pinned Beads GetDependenciesWithMetadata omits missing target rows.
			if target := targets[edge.DependsOnID]; target != nil {
				result[id] = append(result[id], &beadslib.IssueWithDependencyMetadata{Issue: *target, DependencyType: edge.Type})
			}
		}
	}
	return result, true, nil
}
