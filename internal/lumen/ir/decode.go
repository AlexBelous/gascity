package ir

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// Decode parses a lumen.ir document and validates its contract identity and node
// taxonomy. It fails at load time — never at run time — on an unknown contract
// name/version or an unknown node kind, so a bad or drifted IR is a load error,
// not a runtime surprise.
func Decode(data []byte) (*IR, error) {
	if err := validateDocumentSchema(data); err != nil {
		return nil, err
	}
	var doc IR
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("decoding lumen.ir: %w", err)
	}
	if err := doc.Validate(); err != nil {
		return nil, err
	}
	return &doc, nil
}

// Validate checks the contract envelope and every node (including nested ones)
// against the closed emitted taxonomy. It also enforces the one closed node
// payload the schema pins: the run node.
func (ir *IR) Validate() error {
	if ir.Contract.Name != ContractName {
		return fmt.Errorf("lumen.ir: contract.name is %q, want %q", ir.Contract.Name, ContractName)
	}
	if !SupportedVersions[ir.Contract.Version] {
		return fmt.Errorf("lumen.ir: unsupported contract.version %q (supported: %s)",
			ir.Contract.Version, supportedVersionList())
	}

	var problems []string
	type afterEdge struct {
		nodeID string
		after  string
	}
	nodeIDs := make(map[string]bool)
	var afterEdges []afterEdge
	ir.WalkNodes(func(node map[string]json.RawMessage) {
		var kind string
		_ = json.Unmarshal(node["kind"], &kind)
		if !KnownNodeKinds[NodeKind(kind)] {
			var id string
			_ = json.Unmarshal(node["id"], &id)
			problems = append(problems, fmt.Sprintf("unknown node kind %q (node %q)", kind, id))
		}

		var id string
		if rawID, ok := node["id"]; !ok {
			problems = append(problems, fmt.Sprintf("%s node missing required field %q", kind, "id"))
		} else if err := json.Unmarshal(rawID, &id); err != nil {
			problems = append(problems, fmt.Sprintf("%s node has invalid id: %v", kind, err))
		} else {
			nodeIDs[id] = true
		}
		for _, field := range []string{"after", "origin"} {
			if _, ok := node[field]; !ok {
				problems = append(problems, fmt.Sprintf("%s node %q missing required field %q", kind, id, field))
			}
		}
		if rawAfter, ok := node["after"]; ok {
			var after []string
			if err := json.Unmarshal(rawAfter, &after); err != nil {
				problems = append(problems, fmt.Sprintf("%s node %q has invalid after: %v", kind, id, err))
			} else {
				for _, dependency := range after {
					afterEdges = append(afterEdges, afterEdge{nodeID: id, after: dependency})
				}
			}
		}

		if KnownNodeKinds[NodeKind(kind)] {
			problems = append(problems, validateNodePayload(NodeKind(kind), id, node)...)
		}
		if NodeKind(kind) == NodeRun {
			if err := validateRunNode(node); err != nil {
				problems = append(problems, err.Error())
			}
		}
	})
	for _, edge := range afterEdges {
		if !nodeIDs[edge.after] {
			problems = append(problems, fmt.Sprintf("node %q 'after' references unknown id %q", edge.nodeID, edge.after))
		}
	}

	seenTopLevel := make(map[string]bool)
	for _, node := range ir.Nodes {
		if seenTopLevel[node.ID] {
			problems = append(problems, fmt.Sprintf("duplicate top-level node id %q", node.ID))
		}
		seenTopLevel[node.ID] = true
	}
	if err := validateInputTypes(ir.Raw["input"], "input"); err != nil {
		problems = append(problems, err.Error())
	}

	// Recurse into the sub-formula bundle (§A). WalkNodes visits only the top
	// document's nodes, so a sub-doc's node taxonomy and run-node closed payload
	// would otherwise be a runtime surprise; validating each sub-doc here keeps a
	// drifted bundle a load error. Sorted names give deterministic error text.
	for _, name := range sortedFormulaNames(ir.Formulas) {
		sub := ir.Formulas[name]
		switch {
		case sub == nil:
			problems = append(problems, fmt.Sprintf("formulas[%q] is null", name))
		case sub.Name != name:
			problems = append(problems, fmt.Sprintf("formulas[%q] declares name %q", name, sub.Name))
		case len(sub.Formulas) > 0:
			problems = append(problems, fmt.Sprintf("formulas[%q] carries a nested formulas bundle (bundle must be flat)", name))
		default:
			if err := sub.Validate(); err != nil {
				problems = append(problems, fmt.Sprintf("formulas[%q]: %v", name, err))
			}
		}
	}

	if len(problems) > 0 {
		return fmt.Errorf("lumen.ir: %s", strings.Join(problems, "; "))
	}
	return nil
}

// sortedFormulaNames returns the bundle's formula names in lexical order so
// Validate emits deterministic error text across runs.
func sortedFormulaNames(formulas map[string]*IR) []string {
	names := make([]string, 0, len(formulas))
	for name := range formulas {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// validateRunNode applies the schema's closed runNode definition at any nesting
// depth, where the root schema's nodes.items validation cannot reach.
func validateRunNode(node map[string]json.RawMessage) error {
	var id string
	_ = json.Unmarshal(node["id"], &id)
	if err := validateRunNodeSchema(node); err != nil {
		return fmt.Errorf("run node %q violates the closed payload schema: %w", id, err)
	}
	return nil
}

var requiredNodePayloadFields = map[NodeKind][]string{
	NodeAsync:       {"body"},
	NodeAwait:       {"target"},
	NodeBlock:       {"members"},
	NodeCancel:      {"target"},
	NodeChannel:     {"type"},
	NodeCleanup:     {"guarded", "body"},
	NodeClose:       {"target"},
	NodeDispatch:    {"subject", "exhaustive", "arms"},
	NodeDo:          {"interpreter", "body"},
	NodeExec:        {"interpreter", "body", "exitMap"},
	NodeFailChannel: {"target", "reason"},
	NodeForEach:     {"binder", "over", "body", "on_fail"},
	NodeGather:      {"over", "combine"},
	NodeGuard:       {"cond", "then"},
	NodeLit:         {"type", "value"},
	NodeMap:         {"binder", "over", "body"},
	NodeQuote:       {"callee", "graph", "input"},
	NodeRaise:       {"value", "target"},
	NodeRecover:     {"guarded", "body", "errorBinding"},
	NodeRepeat:      {"body", "cond", "iterationName"},
	NodeRetry:       {"attempts", "body"},
	NodeRun:         {"target", "outcome"},
	NodeScatter:     {"form", "on_fail"},
	NodeSettle:      {"outcome"},
	NodeTimeout:     {"duration", "body"},
}

var knownSettleOutcomes = map[string]bool{
	"pass":      true, // Legacy emitted IR; new compilers emit succeeded.
	"canceled":  true, // Legacy Gas fixtures and journals may carry cancellation directly.
	"succeeded": true,
	"degraded":  true,
	"failed":    true,
	"skipped":   true,
}

func validateNodePayload(kind NodeKind, id string, node map[string]json.RawMessage) []string {
	var problems []string
	for _, field := range requiredNodePayloadFields[kind] {
		if _, ok := node[field]; !ok {
			problems = append(problems, fmt.Sprintf("%s node %q missing required field %q", kind, id, field))
		}
	}
	if kind == NodeScatter {
		var form string
		if raw, ok := node["form"]; ok && json.Unmarshal(raw, &form) == nil {
			var fields []string
			switch form {
			case "members":
				fields = []string{"members"}
			case "each":
				fields = []string{"binder", "over", "body"}
			default:
				problems = append(problems, fmt.Sprintf("scatter node %q has unknown form %q", id, form))
			}
			for _, field := range fields {
				if _, ok := node[field]; !ok {
					problems = append(problems, fmt.Sprintf("scatter node %q with form %q missing required field %q", id, form, field))
				}
			}
		}
	}
	if kind == NodeInterp {
		_, hasParts := node["parts"]
		_, hasLegacyBody := node["body"]
		if !hasParts && !hasLegacyBody {
			problems = append(problems, fmt.Sprintf("interp node %q missing required field %q", id, "parts"))
		}
		if hasParts {
			if _, hasType := node["type"]; !hasType {
				problems = append(problems, fmt.Sprintf("interp node %q missing required field %q", id, "type"))
			}
		}
	}
	if kind == NodeSettle {
		var outcome string
		if raw, ok := node["outcome"]; ok {
			if err := json.Unmarshal(raw, &outcome); err != nil {
				problems = append(problems, fmt.Sprintf("settle node %q has invalid outcome: %v", id, err))
			} else if !knownSettleOutcomes[outcome] {
				problems = append(problems, fmt.Sprintf("unknown settle outcome %q (node %q)", outcome, id))
			}
		}
	}
	for _, field := range nodeTypeFields[kind] {
		if raw, ok := node[field]; ok {
			if err := validateType(raw, fmt.Sprintf("%s node %q field %q", kind, id, field)); err != nil {
				problems = append(problems, err.Error())
			}
		}
	}
	return problems
}

var nodeTypeFields = map[NodeKind][]string{
	NodeAwait:   {"resultType"},
	NodeChannel: {"type"},
	NodeDo:      {"returns"},
	NodeExec:    {"returns"},
	NodeInterp:  {"type"},
	NodeLit:     {"type"},
	NodeSettle:  {"type"},
}

// Kinds returns the census of node kinds used across the document (including
// nested nodes), with counts.
func (ir *IR) Kinds() map[NodeKind]int {
	census := map[NodeKind]int{}
	ir.WalkNodes(func(node map[string]json.RawMessage) {
		var kind string
		_ = json.Unmarshal(node["kind"], &kind)
		census[NodeKind(kind)]++
	})
	return census
}

func supportedVersionList() string {
	vs := make([]string, 0, len(SupportedVersions))
	for v := range SupportedVersions {
		vs = append(vs, v)
	}
	return strings.Join(vs, ", ")
}
