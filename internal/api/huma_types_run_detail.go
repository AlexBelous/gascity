package api

// RunDetailNodeStatus is the lifecycle state of a semantic node, physical
// execution, or hidden control.
type RunDetailNodeStatus string

// Run detail node lifecycle values.
const (
	RunDetailNodePending   RunDetailNodeStatus = "pending"
	RunDetailNodeReady     RunDetailNodeStatus = "ready"
	RunDetailNodeActive    RunDetailNodeStatus = "active"
	RunDetailNodeBlocked   RunDetailNodeStatus = "blocked"
	RunDetailNodeCompleted RunDetailNodeStatus = "completed"
	RunDetailNodeFailed    RunDetailNodeStatus = "failed"
	RunDetailNodeSkipped   RunDetailNodeStatus = "skipped"
	RunDetailNodeCanceled  RunDetailNodeStatus = "canceled"
	RunDetailNodeUnknown   RunDetailNodeStatus = "unknown"
)

// RunDetailConstructKind is the formula/control construct represented by a
// semantic node or hidden control badge.
type RunDetailConstructKind string

// Run detail formula and control construct values.
const (
	RunDetailConstructRoot       RunDetailConstructKind = "run-root"
	RunDetailConstructStep       RunDetailConstructKind = "step"
	RunDetailConstructRetry      RunDetailConstructKind = "retry"
	RunDetailConstructCheckLoop  RunDetailConstructKind = "check-loop"
	RunDetailConstructScope      RunDetailConstructKind = "scope"
	RunDetailConstructCondition  RunDetailConstructKind = "condition"
	RunDetailConstructFanout     RunDetailConstructKind = "fanout"
	RunDetailConstructExpansion  RunDetailConstructKind = "expansion"
	RunDetailConstructScopeCheck RunDetailConstructKind = "scope-check"
	RunDetailConstructFinalize   RunDetailConstructKind = "run-finalize"
	RunDetailConstructSpec       RunDetailConstructKind = "spec"
	RunDetailConstructControl    RunDetailConstructKind = "control"
	RunDetailConstructUnknown    RunDetailConstructKind = "unknown"
)

// RunDetailEdgeKind is the typed relationship between two semantic execution
// nodes.
type RunDetailEdgeKind string

// Run detail execution relationship values.
const (
	RunDetailEdgeParent            RunDetailEdgeKind = "parent"
	RunDetailEdgeBlocks            RunDetailEdgeKind = "blocks"
	RunDetailEdgeWaitsFor          RunDetailEdgeKind = "waits-for"
	RunDetailEdgeConditionalBlocks RunDetailEdgeKind = "conditional-blocks"
	RunDetailEdgeDependency        RunDetailEdgeKind = "dependency"
	RunDetailEdgeUnknown           RunDetailEdgeKind = "unknown"
)

// RunDetailSessionAvailability describes the session evidence attached to one
// physical execution.
type RunDetailSessionAvailability string

// Run detail session evidence availability values.
const (
	RunDetailSessionAttached   RunDetailSessionAvailability = "attached"
	RunDetailSessionNotStarted RunDetailSessionAvailability = "not_started"
	RunDetailSessionUnresolved RunDetailSessionAvailability = "unresolved"
	RunDetailSessionUnknown    RunDetailSessionAvailability = "unknown"
)

// RunDetailScope is one concrete city or rig scope.
type RunDetailScope struct {
	Kind string `json:"kind" enum:"city,rig" doc:"Scope kind."`
	Ref  string `json:"ref" doc:"Concrete scope reference."`
}

// RunDetailFormula identifies the formula version that produced the graph.
type RunDetailFormula struct {
	Name     string `json:"name,omitempty" doc:"Formula name, when recorded."`
	Hash     string `json:"hash,omitempty" doc:"Recorded formula content hash, when available."`
	Source   string `json:"source,omitempty" doc:"Recorded formula source, when available."`
	Contract string `json:"contract" doc:"Formula execution contract."`
}

// RunDetailSource describes the authoritative graph snapshot and its explicit
// completeness.
type RunDetailSource struct {
	Kind              string   `json:"kind" enum:"gascity_bead_graph" doc:"Authoritative producer."`
	Available         bool     `json:"available" doc:"Whether the authoritative graph read succeeded."`
	ProjectionVersion int      `json:"projection_version" doc:"Run detail projection schema version."`
	EventSequence     *uint64  `json:"event_sequence,omitempty" doc:"Latest city event sequence observed at response time; not a store snapshot token."`
	Partial           bool     `json:"partial" doc:"Whether the producer reported missing graph data."`
	Truncated         bool     `json:"truncated" doc:"Whether graph data was capped."`
	Reasons           []string `json:"reasons" doc:"Explicit partial or truncation reasons."`
}

// RunDetailSession is the grounded session evidence for one physical
// execution.
type RunDetailSession struct {
	Availability RunDetailSessionAvailability `json:"availability" enum:"attached,not_started,unresolved,unknown" doc:"Session evidence availability."`
	ID           string                       `json:"id,omitempty" doc:"Durable session identifier, when attached."`
	Name         string                       `json:"name,omitempty" doc:"Session display name, when attached."`
	Assignee     string                       `json:"assignee,omitempty" doc:"Recorded assignee, when available."`
}

// RunDetailExecution is one stable physical execution instance behind a
// semantic formula node.
type RunDetailExecution struct {
	PhysicalID       string              `json:"physical_id" doc:"Stable physical execution identifier."`
	BeadID           string              `json:"bead_id" doc:"Backing bead identifier."`
	SemanticID       string              `json:"semantic_id" doc:"Semantic formula-step identifier."`
	Status           RunDetailNodeStatus `json:"status" enum:"pending,ready,active,blocked,completed,failed,skipped,canceled,unknown" doc:"Execution lifecycle."`
	Attempt          *int                `json:"attempt,omitempty" doc:"One-based attempt number, when tracked."`
	Iteration        *int                `json:"iteration,omitempty" doc:"One-based loop iteration, when tracked."`
	Session          RunDetailSession    `json:"session" doc:"Grounded session evidence."`
	CurrentIteration bool                `json:"current_iteration" doc:"Whether this execution belongs to the current iteration."`
	Historical       bool                `json:"historical" doc:"Whether this is retained historical execution."`
	Dynamic          bool                `json:"dynamic" doc:"Whether runtime fragment expansion created this execution."`
}

// RunDetailControlBadge is a hidden control attached to a visible semantic
// node.
type RunDetailControlBadge struct {
	ID     string                 `json:"id" doc:"Backing control bead identifier."`
	Kind   RunDetailConstructKind `json:"kind" enum:"scope-check,run-finalize,spec,control,unknown" doc:"Typed control construct."`
	Label  string                 `json:"label" doc:"Human-readable control label."`
	Status RunDetailNodeStatus    `json:"status" enum:"pending,ready,active,blocked,completed,failed,skipped,canceled,unknown" doc:"Control lifecycle."`
}

// RunDetailNode is one semantic node in a run's execution graph.
type RunDetailNode struct {
	SemanticID    string                  `json:"semantic_id" doc:"Stable semantic formula-step identifier."`
	Title         string                  `json:"title" doc:"Node title."`
	Status        RunDetailNodeStatus     `json:"status" enum:"pending,ready,active,blocked,completed,failed,skipped,canceled,unknown" doc:"Aggregated node lifecycle."`
	ExecutionKind string                  `json:"execution_kind" doc:"Producer-recorded execution kind."`
	ConstructKind RunDetailConstructKind  `json:"construct_kind" enum:"run-root,step,retry,check-loop,scope,condition,fanout,expansion,control,unknown" doc:"Typed formula/control construct."`
	ScopeRef      string                  `json:"scope_ref,omitempty" doc:"Node-specific scope reference, when present."`
	Visible       bool                    `json:"visible" doc:"Whether this semantic node belongs in the execution graph."`
	Historical    bool                    `json:"historical" doc:"Whether only historical executions remain."`
	Dynamic       bool                    `json:"dynamic" doc:"Whether any physical execution was runtime-expanded."`
	Executions    []RunDetailExecution    `json:"executions" doc:"Physical executions grouped under this semantic node."`
	ControlBadges []RunDetailControlBadge `json:"control_badges" doc:"Hidden controls attached to this node."`
}

// RunDetailEdge is a directed relationship between semantic execution nodes.
type RunDetailEdge struct {
	From       string            `json:"from" doc:"Source semantic node identifier."`
	To         string            `json:"to" doc:"Target semantic node identifier."`
	Kind       RunDetailEdgeKind `json:"kind" enum:"parent,blocks,waits-for,conditional-blocks,dependency,unknown" doc:"Typed relationship kind."`
	SourceKind string            `json:"source_kind" doc:"Producer's original relationship kind."`
}

// RunDetail is the authoritative execution topology for one graph-v2 run.
type RunDetail struct {
	RunID        string              `json:"run_id" doc:"Canonical run identifier."`
	RootBeadID   string              `json:"root_bead_id" doc:"Authoritative run-root bead identifier."`
	City         string              `json:"city" doc:"Concrete city that owns this run."`
	Scope        RunDetailScope      `json:"scope" doc:"Concrete run scope."`
	RootStoreRef string              `json:"root_store_ref,omitempty" doc:"Recorded authoritative root-store reference."`
	Title        string              `json:"title" doc:"Run title."`
	Status       RunDetailNodeStatus `json:"status" enum:"pending,ready,active,blocked,completed,failed,skipped,canceled,unknown" doc:"Run-root lifecycle."`
	Formula      RunDetailFormula    `json:"formula" doc:"Formula identity and version."`
	Source       RunDetailSource     `json:"source" doc:"Topology provenance and completeness."`
	Nodes        []RunDetailNode     `json:"nodes" doc:"Semantic execution nodes."`
	Edges        []RunDetailEdge     `json:"edges" doc:"Directed execution relationships."`
}

// RunDetailInput is the request for
// GET /v0/city/{cityName}/runs/{run_id}/detail.
type RunDetailInput struct {
	CityScope
	RunID string `path:"run_id" minLength:"1" pattern:"\\S" doc:"Run identifier."`
}

// RunDetailOutput is the typed response for one authoritative run detail.
type RunDetailOutput struct {
	Index uint64 `header:"X-GC-Index" doc:"Latest city event sequence number."`
	Body  RunDetail
}
