package api

import (
	"context"
	"errors"
	"log"

	"github.com/gastownhall/gascity/internal/api/apierr"
	"github.com/gastownhall/gascity/internal/beadmeta"
	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/runproj"
)

const (
	runDetailProjectionVersion               = 1
	runDetailPartialReasonGraphInstantiating = "graph_instantiating"
)

// humaHandleRunDetail is the Huma-typed handler for
// GET /v0/city/{cityName}/runs/{run_id}/detail. Unlike the hot list and summary
// routes, detail reads the authoritative run-rooted bead graph so persisted
// branches and dynamic children cannot disappear when an event projection is
// incomplete.
func (s *Server) humaHandleRunDetail(_ context.Context, input *RunDetailInput) (*RunDetailOutput, error) {
	graph, err := s.readBeadGraph(input.RunID)
	if err != nil {
		if errors.Is(err, beads.ErrNotFound) {
			return nil, apierr.RunNotFound.Msgf("run not found: %s", input.RunID)
		}
		return nil, runProjectionUnavailable(err)
	}

	index := s.latestIndex()
	graphBeads := hydrateRunDetailDependencies(graph.Beads, graph.Deps)
	detail, err := runproj.BuildRunDetail(graphBeads, input.RunID, runDetailProjectionVersion, int64(index))
	if err != nil {
		var unsupported *runproj.UnsupportedRunError
		if errors.As(err, &unsupported) && unsupported.Reason == runproj.ReasonNotRunView {
			return nil, apierr.RunDetailUnavailable.Msg("run has no graph-v2 execution detail")
		}
		if errors.Is(err, runproj.ErrRunNotFound) {
			return nil, apierr.RunNotFound.Msgf("run not found: %s", input.RunID)
		}
		log.Printf("run detail projection failed for %q: %v", input.RunID, err)
		return nil, apierr.Internal.Msg("run detail projection failed")
	}

	out := &RunDetailOutput{Index: index}
	out.Body = runDetailFromProjection(s.state.CityName(), graph.Root, graph.Beads, detail, index)
	return out, nil
}

// hydrateRunDetailDependencies places the authoritative graph edges back on
// their dependent beads, which is the existing runproj input contract. Parent
// edges are excluded because runproj already derives them from run membership.
func hydrateRunDetailDependencies(graphBeads []beads.Bead, deps []workflowDepResponse) []beads.Bead {
	out := make([]beads.Bead, len(graphBeads))
	copy(out, graphBeads)

	byID := make(map[string]int, len(out))
	for i := range out {
		byID[out[i].ID] = i
		out[i].Needs = nil
		out[i].Dependencies = nil
	}
	for _, dep := range deps {
		if dep.Kind == "parent-child" {
			continue
		}
		i, ok := byID[dep.To]
		if !ok {
			continue
		}
		out[i].Dependencies = append(out[i].Dependencies, beads.Dep{
			IssueID:     dep.To,
			DependsOnID: dep.From,
			Type:        dep.Kind,
		})
	}
	return out
}

func runDetailFromProjection(city string, root beads.Bead, graphBeads []beads.Bead, detail runproj.FormulaRunDetail, index uint64) RunDetail {
	beadsByID := make(map[string]beads.Bead, len(graphBeads))
	for _, bead := range graphBeads {
		beadsByID[bead.ID] = bead
	}

	partial := detail.Completeness.Kind == "partial"
	reasons := append([]string{}, detail.Completeness.Reasons...)
	if runDetailGraphInstantiating(graphBeads) {
		partial = true
		reasons = append(reasons, runDetailPartialReasonGraphInstantiating)
	}
	source := RunDetailSource{
		Kind:              "gascity_bead_graph",
		Available:         true,
		ProjectionVersion: detail.SnapshotVersion,
		Partial:           partial,
		Truncated:         false,
		Reasons:           reasons,
	}
	if index > 0 {
		source.EventSequence = &index
	}

	nodes := make([]RunDetailNode, 0, len(detail.Nodes))
	status := RunDetailNodeUnknown
	for _, node := range detail.Nodes {
		projected := runDetailNodeFromProjection(node, beadsByID)
		nodes = append(nodes, projected)
		if node.ID == detail.RootBeadID {
			status = projected.Status
		}
	}
	edges := make([]RunDetailEdge, 0, len(detail.Edges))
	for _, edge := range detail.Edges {
		edges = append(edges, RunDetailEdge{
			From:       edge.From,
			To:         edge.To,
			Kind:       runDetailEdgeKind(edge.Kind),
			SourceKind: edge.Kind,
		})
	}

	formulaName := ""
	if detail.Formula.Kind == "known" && detail.Formula.Source == "metadata" {
		formulaName = detail.Formula.Name
	}
	return RunDetail{
		RunID:        detail.RunID,
		RootBeadID:   detail.RootBeadID,
		City:         city,
		Scope:        RunDetailScope{Kind: detail.ScopeKind, Ref: detail.ScopeRef},
		RootStoreRef: detail.RootStoreRef,
		Title:        detail.Title,
		Status:       status,
		Formula: RunDetailFormula{
			Name:     formulaName,
			Hash:     root.Metadata[beadmeta.FormulaHashMetadataKey],
			Source:   root.Metadata[beadmeta.FormulaSourceMetadataKey],
			Contract: root.Metadata[beadmeta.FormulaContractMetadataKey],
		},
		Source: source,
		Nodes:  nodes,
		Edges:  edges,
	}
}

func runDetailGraphInstantiating(graphBeads []beads.Bead) bool {
	for _, bead := range graphBeads {
		if bead.Metadata[beadmeta.InstantiatingMetadataKey] == "true" {
			return true
		}
	}
	return false
}

func runDetailNodeFromProjection(node runproj.RunDisplayNode, graphBeads map[string]beads.Bead) RunDetailNode {
	executions := make([]RunDetailExecution, 0, len(node.ExecutionInstances))
	dynamic := false
	for _, execution := range node.ExecutionInstances {
		backing := graphBeads[execution.BeadID]
		executionDynamic := backing.Metadata[beadmeta.DynamicFragmentMetadataKey] == "true"
		dynamic = dynamic || executionDynamic
		executions = append(executions, RunDetailExecution{
			PhysicalID:       execution.ID,
			BeadID:           execution.BeadID,
			SemanticID:       execution.SemanticNodeID,
			Status:           runDetailNodeStatus(execution.Status),
			Attempt:          runDetailAttempt(execution.Attempt),
			Iteration:        runDetailIteration(execution.Iteration),
			Session:          runDetailSession(execution.Session),
			CurrentIteration: execution.CurrentIteration,
			Historical:       execution.Historical,
			Dynamic:          executionDynamic,
		})
	}

	badges := make([]RunDetailControlBadge, 0, len(node.ControlBadges))
	for _, badge := range node.ControlBadges {
		backing := graphBeads[badge.ID]
		badges = append(badges, RunDetailControlBadge{
			ID:     badge.ID,
			Kind:   runDetailControlKind(backing.Metadata[beadmeta.KindMetadataKey]),
			Label:  badge.Label,
			Status: runDetailNodeStatus(badge.Status),
		})
	}

	scopeRef := ""
	if node.Scope.Kind == "scoped" {
		scopeRef = node.Scope.Ref
	}
	constructKind := runDetailConstructKind(node.ConstructKind)
	return RunDetailNode{
		SemanticID:    node.SemanticNodeID,
		Title:         node.Title,
		Status:        runDetailNodeStatus(node.Status),
		ExecutionKind: node.Kind,
		ConstructKind: constructKind,
		ScopeRef:      scopeRef,
		Visible:       node.VisibleInGraph,
		Historical:    node.HistoricalOnly,
		Dynamic:       dynamic,
		Executions:    executions,
		ControlBadges: badges,
	}
}

func runDetailNodeStatus(status string) RunDetailNodeStatus {
	switch status {
	case "pending":
		return RunDetailNodePending
	case "ready":
		return RunDetailNodeReady
	case "active", "running":
		return RunDetailNodeActive
	case "blocked":
		return RunDetailNodeBlocked
	case "completed", "done":
		return RunDetailNodeCompleted
	case "failed":
		return RunDetailNodeFailed
	case "skipped":
		return RunDetailNodeSkipped
	case "canceled":
		return RunDetailNodeCanceled
	default:
		return RunDetailNodeUnknown
	}
}

func runDetailConstructKind(kind string) RunDetailConstructKind {
	switch kind {
	case "run-root":
		return RunDetailConstructRoot
	case "step":
		return RunDetailConstructStep
	case "retry":
		return RunDetailConstructRetry
	case "check-loop":
		return RunDetailConstructCheckLoop
	case "scope":
		return RunDetailConstructScope
	case "condition":
		return RunDetailConstructCondition
	case "fanout":
		return RunDetailConstructFanout
	case "expansion":
		return RunDetailConstructExpansion
	case "scope-check":
		return RunDetailConstructScopeCheck
	case "run-finalize":
		return RunDetailConstructFinalize
	case "spec":
		return RunDetailConstructSpec
	case "control":
		return RunDetailConstructControl
	default:
		return RunDetailConstructUnknown
	}
}

func runDetailControlKind(kind string) RunDetailConstructKind {
	switch kind {
	case beadmeta.KindScopeCheck:
		return RunDetailConstructScopeCheck
	case "run-finalize", beadmeta.KindWorkflowFinalize:
		return RunDetailConstructFinalize
	case beadmeta.KindCleanup:
		return RunDetailConstructControl
	case beadmeta.KindSpec:
		return RunDetailConstructSpec
	default:
		return RunDetailConstructUnknown
	}
}

func runDetailEdgeKind(kind string) RunDetailEdgeKind {
	switch kind {
	case "parent", "parent-child":
		return RunDetailEdgeParent
	case "blocks":
		return RunDetailEdgeBlocks
	case "waits-for":
		return RunDetailEdgeWaitsFor
	case "conditional-blocks":
		return RunDetailEdgeConditionalBlocks
	case "", "dependency":
		return RunDetailEdgeDependency
	default:
		return RunDetailEdgeUnknown
	}
}

func runDetailAttempt(attempt runproj.RunAttempt) *int {
	if attempt.Kind != "attempt" {
		return nil
	}
	value := attempt.Value
	return &value
}

func runDetailIteration(iteration runproj.RunIteration) *int {
	if iteration.Kind != "loop" {
		return nil
	}
	value := iteration.Value
	return &value
}

func runDetailSession(session runproj.RunSessionAttachment) RunDetailSession {
	var projected RunDetailSession
	switch session.Kind {
	case "attached":
		projected.Availability = RunDetailSessionAttached
		projected.ID = session.Link.SessionID
		projected.Name = session.Link.SessionName
		projected.Assignee = session.Link.Assignee
	case "none":
		switch session.Reason {
		case "not_started":
			projected.Availability = RunDetailSessionNotStarted
		case "session_unresolved":
			projected.Availability = RunDetailSessionUnresolved
		default:
			projected.Availability = RunDetailSessionUnknown
		}
	default:
		projected.Availability = RunDetailSessionUnknown
	}
	return projected
}
