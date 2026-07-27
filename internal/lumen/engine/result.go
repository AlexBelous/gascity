package engine

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"

	"github.com/gastownhall/gascity/internal/lumen/ir"
)

var errResultUnavailable = errors.New("typed result is unavailable from the pre-v7 journal")

type resultSource uint8

const (
	resultUnavailable resultSource = iota
	resultRecorded
	resultReconstructed
)

func (s resultSource) available() bool {
	return s != resultUnavailable
}

// LumenResultError is the structured failure carried by a Lumen step result.
type LumenResultError struct {
	Reason           string `json:"reason"`
	Message          string `json:"message,omitempty"`
	Retryable        bool   `json:"retryable,omitempty"`
	Step             string `json:"step,omitempty"`
	Attempts         int    `json:"attempts,omitempty"`
	RetriesRemaining *int   `json:"retriesRemaining,omitempty"`
	Canceled         bool   `json:"canceled,omitempty"`
}

// LumenStepResult is the upstream 0.2.5 result of one formula step.
type LumenStepResult struct {
	Value   any               `json:"value"`
	Outcome string            `json:"outcome"`
	Error   *LumenResultError `json:"error"`
}

// LumenPublicOutcome is the structured value produced by an authored outcome
// statement such as succeed, degrade, fail, or skip.
type LumenPublicOutcome struct {
	Kind             string
	Result           any
	Reason           string
	RetriesRemaining *int
}

// MarshalJSON emits the outcome variant's exact public shape.
func (o LumenPublicOutcome) MarshalJSON() ([]byte, error) {
	switch o.Kind {
	case OutcomeSucceeded:
		return json.Marshal(struct {
			Kind   string `json:"kind"`
			Result any    `json:"result"`
		}{Kind: o.Kind, Result: o.Result})
	case OutcomeDegraded:
		return json.Marshal(struct {
			Kind   string `json:"kind"`
			Result any    `json:"result"`
			Reason string `json:"reason"`
		}{Kind: o.Kind, Result: o.Result, Reason: o.Reason})
	case OutcomeFailed:
		return json.Marshal(struct {
			Kind             string `json:"kind"`
			Reason           string `json:"reason"`
			RetriesRemaining *int   `json:"retriesRemaining,omitempty"`
		}{Kind: o.Kind, Reason: o.Reason, RetriesRemaining: o.RetriesRemaining})
	default:
		return json.Marshal(struct {
			Kind   string `json:"kind"`
			Reason string `json:"reason"`
		}{Kind: o.Kind, Reason: o.Reason})
	}
}

func canonicalResultOutcome(outcome string) (canonical string, canceled bool) {
	switch outcome {
	case OutcomeSucceeded, outcomeLegacyPass:
		return OutcomeSucceeded, false
	case OutcomeDegraded, OutcomeFailed, OutcomeSkipped:
		return outcome, false
	case OutcomeCanceled:
		return OutcomeFailed, true
	default:
		return OutcomeFailed, false
	}
}

// runtimeStepResult maps internal runtime outcomes onto the public Lumen result
// vocabulary. Pending and unknown outcomes cannot escape as public outcomes;
// cancellation remains failed-class and is distinguished by error.canceled.
func runtimeStepResult(outcome string, value any, reason string, retryable bool, retriesRemaining *int) LumenStepResult {
	canonical, canceled := canonicalResultOutcome(outcome)
	result := LumenStepResult{Value: value, Outcome: canonical}
	switch {
	case canceled:
		result.Value = nil
		if reason == "" {
			reason = OutcomeCanceled
		}
	case outcome == OutcomePending:
		result.Value = nil
		if reason == "" {
			reason = OutcomePending
		}
	case outcome != canonical && !IsSucceededOutcome(outcome):
		result.Value = nil
		if reason == "" {
			reason = "invalid_outcome"
		}
	case canonical == OutcomeSkipped:
		result.Value = nil
		return result
	case canonical == OutcomeFailed || canonical == OutcomeDegraded:
		if reason == "" {
			reason = canonical
		}
	default:
		return result
	}
	result.Error = &LumenResultError{
		Reason:           reason,
		Retryable:        retryable,
		RetriesRemaining: retriesRemaining,
		Canceled:         canceled,
	}
	return result
}

// authoredSettleResult implements the non-public settle arm. Unlike dependency
// skipping, an authored skip may carry a reason error; its value is always null.
func authoredSettleResult(outcome string, value any, reason string) LumenStepResult {
	canonical, canceled := canonicalResultOutcome(outcome)
	result := LumenStepResult{Value: value, Outcome: canonical}
	if canonical == OutcomeSucceeded {
		return result
	}
	if canonical == OutcomeSkipped {
		result.Value = nil
		if reason == "" {
			return result
		}
	} else if reason == "" {
		if canceled {
			reason = OutcomeCanceled
		} else {
			reason = canonical
		}
	}
	result.Error = &LumenResultError{
		Reason:   reason,
		Canceled: canceled,
	}
	if reason != canonical || canceled || canonical == OutcomeSkipped {
		result.Error.Message = reason
	}
	return result
}

func publicOutcomeResult(outcome string, value any, reason string, retriesRemaining *int) LumenStepResult {
	canonical, canceled := canonicalResultOutcome(outcome)
	kind := canonical
	if canceled {
		kind = OutcomeCanceled
	}
	public := LumenPublicOutcome{
		Kind:             kind,
		Reason:           reason,
		RetriesRemaining: retriesRemaining,
	}
	if kind == OutcomeSucceeded || kind == OutcomeDegraded {
		public.Result = publicOutcomeValue(value)
	}
	if public.Reason == "" && kind != OutcomeSucceeded {
		public.Reason = kind
	}
	result := LumenStepResult{Value: public, Outcome: canonical}
	if canonical != OutcomeSucceeded && reason != "" {
		result.Error = &LumenResultError{
			Reason:           reason,
			Message:          reason,
			RetriesRemaining: retriesRemaining,
			Canceled:         canceled,
		}
	}
	return result
}

func publicOutcomeValue(value any) any {
	switch value := value.(type) {
	case map[string]LumenStepResult:
		result := make(map[string]any, len(value))
		for key, field := range value {
			result[key] = publicOutcomeValue(field.Value)
		}
		return result
	case map[string]any:
		result := make(map[string]any, len(value))
		for key, field := range value {
			if step, ok := decodedStepResult(field); ok {
				result[key] = publicOutcomeValue(step.Value)
				continue
			}
			result[key] = publicOutcomeValue(field)
		}
		return result
	case []any:
		result := make([]any, len(value))
		for i, element := range value {
			result[i] = publicOutcomeValue(element)
		}
		return result
	default:
		return value
	}
}

func legacyOutput(value any) (string, error) {
	switch value := value.(type) {
	case nil:
		return "", nil
	case string:
		return value, nil
	default:
		data, err := json.Marshal(value)
		if err != nil {
			return "", fmt.Errorf("encode legacy output: %w", err)
		}
		return string(data), nil
	}
}

// resultFromNode returns a recorded result or a lossless reconstruction from a
// legacy node. Authored and aggregate values may have been string-encoded, and
// legacy exec failures omitted their exit code and stderr, so those cases stay
// unset rather than masquerading as exact typed results.
func resultFromNode(node *nodeState) (LumenStepResult, resultSource) {
	if node == nil {
		return LumenStepResult{}, resultUnavailable
	}
	if node.Result != nil {
		return *node.Result, resultRecorded
	}
	kind := ir.NodeKind(node.Kind)
	switch kind {
	case ir.NodeDo:
		switch node.Outcome {
		case outcomeLegacyPass, OutcomeSucceeded, OutcomeDegraded,
			OutcomeFailed, OutcomeCanceled, OutcomeSkipped:
		default:
			return LumenStepResult{}, resultUnavailable
		}
		value := any(node.Output)
		if node.Outcome == OutcomeFailed || node.Outcome == OutcomeCanceled ||
			node.Outcome == OutcomeSkipped {
			value = nil
		}
		result := runtimeStepResult(node.Outcome, value, node.Detail, node.Retryable, nil)
		if result.Error != nil {
			result.Error.Message = node.Detail
		}
		return result, resultReconstructed
	case "", ir.NodeExec:
		switch node.Outcome {
		case outcomeLegacyPass, OutcomeSucceeded:
			return runtimeStepResult(node.Outcome, node.Output, "", false, nil), resultReconstructed
		case OutcomeSkipped:
			return runtimeStepResult(node.Outcome, nil, "", false, nil), resultReconstructed
		}
	default:
		if node.Outcome == OutcomeSkipped && kind != ir.NodeSettle {
			return runtimeStepResult(node.Outcome, nil, "", false, nil), resultReconstructed
		}
	}
	return LumenStepResult{}, resultUnavailable
}

func settledPayloadResult(payload outcomeSettledPayload) LumenStepResult {
	if payload.Result != nil {
		return *payload.Result
	}
	reason := payload.Reason
	if reason == "" {
		reason = payload.Detail
	}
	value := any(payload.Output)
	if payload.Outcome == OutcomeSkipped || payload.Outcome == OutcomeCanceled ||
		(payload.Outcome == OutcomeFailed && reason != "loop_cap") {
		value = nil
	}
	return runtimeStepResult(
		payload.Outcome,
		value,
		reason,
		payload.Retryable,
		payload.RetriesRemaining,
	)
}

func cloneStepResult(result LumenStepResult) LumenStepResult {
	clone := result
	if result.Error != nil {
		err := *result.Error
		clone.Error = &err
	}
	return clone
}

func loopStepResult(
	source *LumenStepResult,
	reason string,
	retriesRemaining *int,
	attempts int,
) LumenStepResult {
	if source == nil {
		return runtimeStepResult(OutcomeFailed, nil, reason, false, retriesRemaining)
	}
	result := cloneStepResult(*source)

	if reason == "loop_cap" || reason == "poll_cap" {
		result.Outcome = OutcomeFailed
		result.Error = &LumenResultError{Reason: reason}
		return result
	}
	if result.Outcome == OutcomeFailed && retriesRemaining != nil {
		if result.Error == nil {
			result.Error = &LumenResultError{Reason: OutcomeFailed}
		}
		result.Error.RetriesRemaining = retriesRemaining
		if attempts > 0 {
			result.Error.Attempts = attempts
		}
	}
	return result
}

func (d *driver) appendTransparentResult(
	activation string,
	sourceUnit planUnit,
	scope map[string]string,
) error {
	source := d.st().Nodes[sourceUnit.activation]
	if source == nil || !source.Settled {
		return fmt.Errorf("lumen: transparent result source for %q is not settled", activation)
	}
	result, err := d.settledUnitResult(sourceUnit, scope)
	if err != nil {
		return fmt.Errorf("lumen: transparent result source for %q: %w", activation, err)
	}
	return d.appendOutcomeSettled(outcomeSettledPayload{
		Activation: activation,
		Outcome:    source.Outcome,
		Output:     source.Output,
		Result:     &result,
		Detail:     source.Detail,
		Retryable:  source.Retryable,
	})
}

func (d *driver) settledUnitResult(u planUnit, scope map[string]string) (LumenStepResult, error) {
	stateNode := d.st().Nodes[u.activation]
	if stateNode == nil || !stateNode.Settled {
		return LumenStepResult{}, fmt.Errorf("unit %q is not settled", u.nodeID)
	}
	if result, source := resultFromNode(stateNode); source.available() {
		return result, nil
	}
	context, err := d.resultContextFor(u.ns, scope)
	if err != nil {
		return LumenStepResult{}, err
	}
	return d.settledUnitResultFromContext(u, context, scope)
}

func (d *driver) settledUnitResultFromContext(
	u planUnit,
	context resultContext,
	scope map[string]string,
) (LumenStepResult, error) {
	stateNode := d.st().Nodes[u.activation]
	if stateNode == nil || !stateNode.Settled {
		return LumenStepResult{}, fmt.Errorf("unit %q is not settled", u.nodeID)
	}
	if result, source := resultFromNode(stateNode); source.available() {
		return result, nil
	}
	result, exact, err := d.reconstructUnitResult(u, stateNode, context, scope)
	if err != nil {
		return LumenStepResult{}, err
	}
	if !exact {
		return LumenStepResult{}, fmt.Errorf("unit %q: %w", u.nodeID, errResultUnavailable)
	}
	return result, nil
}

func (d *driver) authoredUnitResult(u planUnit, scope map[string]string) (LumenStepResult, error) {
	context := inputResultContext(d.input)
	if u.kind == unitRun {
		input, err := d.typedSubInput(u.resultPrefix, u.run, scope)
		if err != nil {
			return LumenStepResult{}, err
		}
		context = inputResultContext(input)
	}
	return blockResult(u.resultNodes, u.resultPrefix, d.st(), context, scope)
}

func (d *driver) reconstructUnitResult(
	u planUnit,
	stateNode *nodeState,
	context resultContext,
	scope map[string]string,
) (LumenStepResult, bool, error) {
	if u.kind == unitLeaf && u.irKind == ir.NodeSettle {
		result, err := authoredSettleResultFromIR(ir.Node{
			Kind: ir.NodeSettle,
			ID:   u.leaf.id,
			Raw:  u.leaf.raw,
		}, stateNode, context)
		return result, err == nil, err
	}
	var result LumenStepResult
	var err error
	switch u.kind {
	case unitRun, unitGather, unitCleanupGuarded:
		result, err = d.authoredUnitResult(u, scope)
	case unitScatterAgg:
		result, err = scatterStepResult(
			d.st(),
			u.resultNodes,
			u.resultPrefix,
			context,
			scope,
		)
	case unitForEach:
		elements, valid, evalErr := d.evalForEachArray(u.ns, u.forEach, scope)
		if evalErr != nil {
			return LumenStepResult{}, false, evalErr
		}
		if valid {
			result, err = d.forEachStepResult(u, elements, context, scope)
			break
		}
		result = runtimeStepResult(
			stateNode.Outcome,
			nil,
			stateNode.Detail,
			stateNode.Retryable,
			nil,
		)
	case unitGuard:
		if stateNode.Outcome == OutcomeSkipped {
			result = runtimeStepResult(OutcomeSkipped, nil, "", false, nil)
			break
		}
		result, err = d.settledUnitResultFromContext(d.guardThenUnit(u), context, scope)
	case unitRecover:
		source := d.recoverGuardedUnit(u)
		if recoverCaught(d.settledOutcome(source.activation)) {
			source = d.recoverBodyUnit(u)
		}
		result, err = d.settledUnitResultFromContext(source, context, scope)
	case unitCleanup:
		if d.st().SemanticDialect == SemanticDialectLegacy {
			return LumenStepResult{}, false, nil
		}
		result, err = d.settledUnitResultFromContext(
			d.cleanupGuardedResultUnit(u),
			context,
			scope,
		)
	case unitTimeout:
		result, err = d.settledUnitResultFromContext(d.timeoutBodyUnit(u), context, scope)
	case unitDispatch:
		arm, chosen := d.chosenArm(u)
		if !chosen {
			result = runtimeStepResult(
				stateNode.Outcome,
				stateNode.Output,
				stateNode.Detail,
				stateNode.Retryable,
				nil,
			)
			break
		}
		source := d.decisionBodyUnit(u, arm.bodyNodeID, arm.bodyIRKind, arm.body)
		if arm.bodyRun != nil {
			subUnits, aggregate, mintErr := d.dispatchArmRunBody(u, arm)
			if mintErr != nil {
				return LumenStepResult{}, false, mintErr
			}
			d.registerDispatchArmRunEnv(u, arm, subUnits)
			source = aggregate
		}
		result, err = d.settledUnitResultFromContext(source, context, scope)
	case unitLoop:
		attempt, found := d.lastSettledAttempt(u.loop.bodyNodeID)
		wrapperOutcome, _ := canonicalResultOutcome(stateNode.Outcome)
		if !found || wrapperOutcome == OutcomeFailed {
			return LumenStepResult{}, false, nil
		}
		source := d.attemptUnit(u, attempt)
		if u.loop.bodyRun != nil {
			subUnits, aggregate, mintErr := u.loop.mintRunBodyAttempt(
				attempt,
				u.activation,
				u.ns,
				u.afterDeps,
				u.rawAfter,
			)
			if mintErr != nil {
				return LumenStepResult{}, false, mintErr
			}
			inheritDependencyPolicy(u, subUnits, &aggregate)
			d.registerRunBodyEnv(u.loop, attempt, u.ns, subUnits)
			source = aggregate
		}
		sourceNode := d.st().Nodes[source.activation]
		if sourceNode == nil {
			return LumenStepResult{}, false, nil
		}
		sourceOutcome, _ := canonicalResultOutcome(sourceNode.Outcome)
		if sourceOutcome == OutcomeFailed {
			return LumenStepResult{}, false, nil
		}
		result, err = d.settledUnitResultFromContext(source, context, scope)
	default:
		return LumenStepResult{}, false, nil
	}
	if err != nil {
		return LumenStepResult{}, false, err
	}
	output, err := legacyOutput(result.Value)
	if err != nil {
		return LumenStepResult{}, false, fmt.Errorf(
			"lumen: reconstruct unit %q output: %w",
			u.nodeID,
			err,
		)
	}
	canonical, _ := canonicalResultOutcome(stateNode.Outcome)
	if result.Outcome != canonical || output != stateNode.Output {
		return LumenStepResult{}, false, fmt.Errorf(
			"lumen: reconstructed result for %q does not match journal settlement",
			u.nodeID,
		)
	}
	return result, true, nil
}

func (d *driver) formulaResult(scope map[string]string) (LumenStepResult, error) {
	if d.doc == nil {
		return runtimeStepResult(d.st().runOutcome(), nil, "", false, nil), nil
	}
	return blockResult(
		d.doc.Nodes,
		"",
		d.st(),
		inputResultContext(d.input),
		scope,
	)
}

func (d *driver) completedResult(scope map[string]string) (LumenStepResult, error) {
	if d.st().Result != nil {
		return *d.st().Result, nil
	}
	return d.formulaResult(scope)
}

func blockResult(
	nodes []ir.Node,
	prefix string,
	state *lumenState,
	context resultContext,
	scope map[string]string,
) (LumenStepResult, error) {
	context = cloneResultContext(context)
	acc := newBlockResult(OutcomeSucceeded)
	for _, node := range nodes {
		child, ok, err := authoredNodeResult(node, prefix, state, context, scope)
		unavailable := errors.Is(err, errResultUnavailable)
		if err != nil && !unavailable {
			return LumenStepResult{}, err
		}
		if !ok {
			continue
		}
		if unavailable {
			acc.addUnavailable(child.Outcome)
		} else {
			acc.add(child)
		}
		if node.Name != "" {
			if unavailable {
				context.setUnavailable(node.Name, child)
			} else {
				context.set(node.Name, child)
			}
		}
		if authoredPublicOutcome(node) && (unavailable || isPublicOutcome(child.Value)) {
			break
		}
	}
	return acc.finish()
}

type blockResultAccumulator struct {
	result        LumenStepResult
	succeeded     string
	hasNonSkipped bool
	exact         bool
}

func newBlockResult(succeeded string) blockResultAccumulator {
	return blockResultAccumulator{
		result:    LumenStepResult{Value: nil, Outcome: succeeded},
		succeeded: succeeded,
		exact:     true,
	}
}

func (a *blockResultAccumulator) add(child LumenStepResult) {
	a.addResult(child, true)
}

func (a *blockResultAccumulator) addUnavailable(outcome string) {
	canonical, _ := canonicalResultOutcome(outcome)
	a.addResult(LumenStepResult{Outcome: canonical}, false)
}

func (a *blockResultAccumulator) addResult(child LumenStepResult, exact bool) {
	if child.Outcome == OutcomeSkipped {
		if !a.hasNonSkipped && a.result.Outcome == a.succeeded {
			a.result = child
			a.exact = exact
		}
		return
	}
	previousOutcome := a.result.Outcome
	previousExact := a.exact
	a.hasNonSkipped = true
	a.result.Value = child.Value
	switch {
	case child.Outcome == OutcomeFailed:
		a.result = child
		if !isPublicOutcome(child.Value) &&
			(child.Error == nil || child.Error.Reason != "loop_cap") {
			a.result.Value = nil
		}
	case a.result.Outcome == OutcomeFailed || a.result.Outcome == OutcomeSkipped:
		a.result.Outcome = child.Outcome
		a.result.Error = child.Error
	case child.Outcome == OutcomeDegraded && a.result.Outcome == a.succeeded:
		a.result.Outcome = OutcomeDegraded
		a.result.Error = child.Error
	}
	if previousOutcome == OutcomeDegraded &&
		child.Outcome != OutcomeFailed &&
		child.Outcome != OutcomeSkipped {
		a.exact = previousExact && exact
	} else {
		a.exact = exact
	}
}

func (a *blockResultAccumulator) finish() (LumenStepResult, error) {
	if !a.exact {
		return a.result, errResultUnavailable
	}
	return a.result, nil
}

func scatterStepResult(
	state *lumenState,
	nodes []ir.Node,
	prefix string,
	context resultContext,
	scope map[string]string,
) (LumenStepResult, error) {
	context = cloneResultContext(context)
	harvest := make(map[string]LumenStepResult, len(nodes))
	for _, node := range nodes {
		result, ok, err := authoredNodeResult(node, prefix, state, context, scope)
		if err != nil {
			return LumenStepResult{}, err
		}
		if !ok {
			result = LumenStepResult{Value: nil, Outcome: OutcomeSkipped}
		}
		key := node.Name
		if key == "" {
			key = node.ID
		}
		harvest[key] = result
		if node.Name != "" {
			context.set(node.Name, result)
		}
	}
	return LumenStepResult{
		Value:   harvest,
		Outcome: OutcomeSucceeded,
	}, nil
}

func (d *driver) forEachStepResult(
	u planUnit,
	elements []string,
	context resultContext,
	scope map[string]string,
) (LumenStepResult, error) {
	harvest := make(map[string]LumenStepResult, len(elements))
	for index, element := range elements {
		activation := activationFor(forEachMemberNodeID(u.nodeID, index))
		member := d.st().Nodes[activation]
		if member == nil || !member.Settled {
			return LumenStepResult{}, fmt.Errorf("member %q is not settled", activation)
		}
		result, source := resultFromNode(member)
		if !source.available() {
			memberUnit := d.forEachMemberUnit(u, index)
			if u.forEach.bodyRun != nil {
				subUnits, aggregate, err := d.forEachRunMember(u, index)
				if err != nil {
					return LumenStepResult{}, err
				}
				d.registerForEachRunMemberEnv(u, aggregate.nodeID, subUnits)
				memberUnit = aggregate
			}
			err := d.withBinder(
				scope,
				u.ns+u.forEach.binder,
				element,
				func() error {
					var err error
					result, err = d.settledUnitResultFromContext(
						memberUnit,
						context,
						scope,
					)
					return err
				},
			)
			if err != nil {
				return LumenStepResult{}, fmt.Errorf("member %q: %w", activation, err)
			}
		}
		harvest[fmt.Sprint(index)] = result
	}
	return LumenStepResult{
		Value:   harvest,
		Outcome: OutcomeSucceeded,
	}, nil
}

func authoredNodeResult(
	node ir.Node,
	prefix string,
	state *lumenState,
	context resultContext,
	scope map[string]string,
) (LumenStepResult, bool, error) {
	if node.Kind == ir.NodeBlock {
		members, err := childNodes(node.Raw["members"])
		if err != nil {
			return LumenStepResult{}, false, err
		}
		result, err := blockResult(members, prefix, state, context, scope)
		return result, err == nil || errors.Is(err, errResultUnavailable), err
	}
	if node.Kind == ir.NodeLit {
		value, err := evaluateLumenExpr(node.Raw["value"], context)
		if err != nil {
			return LumenStepResult{}, false, fmt.Errorf("lumen: lit %q result: %w", node.ID, err)
		}
		return LumenStepResult{
			Value:   value,
			Outcome: OutcomeSucceeded,
		}, true, nil
	}
	if node.Kind == ir.NodeInterp {
		if value, ok := scope[prefix+node.ID]; ok {
			return LumenStepResult{
				Value:   value,
				Outcome: OutcomeSucceeded,
			}, true, nil
		}
		legacyScope, err := resultContextScope(context)
		if err != nil {
			return LumenStepResult{}, false, fmt.Errorf("lumen: interp %q result context: %w", node.ID, err)
		}
		value, err := evalInterpWithContext(node.Raw, context, legacyScope)
		if err != nil {
			return LumenStepResult{}, false, fmt.Errorf("lumen: interp %q result: %w", node.ID, err)
		}
		return LumenStepResult{
			Value:   value,
			Outcome: OutcomeSucceeded,
		}, true, nil
	}
	stateNode := state.Nodes[activationFor(prefix+node.ID)]
	if stateNode == nil || !stateNode.Settled {
		return LumenStepResult{}, false, nil
	}
	if stateNode.Result == nil && node.Kind == ir.NodeSettle {
		result, err := authoredSettleResultFromIR(node, stateNode, context)
		return result, err == nil, err
	}
	result, source := resultFromNode(stateNode)
	if !source.available() {
		canonical, _ := canonicalResultOutcome(stateNode.Outcome)
		return LumenStepResult{Outcome: canonical}, true, fmt.Errorf(
			"lumen: node %q: %w",
			prefix+node.ID,
			errResultUnavailable,
		)
	}
	return result, true, nil
}

func authoredSettleResultFromIR(
	node ir.Node,
	stateNode *nodeState,
	context resultContext,
) (LumenStepResult, error) {
	var authoredOutcome string
	if err := json.Unmarshal(node.Raw["outcome"], &authoredOutcome); err != nil {
		return LumenStepResult{}, fmt.Errorf("lumen: settle %q outcome: %w", node.ID, err)
	}
	if stateNode.Outcome == OutcomeSkipped &&
		(authoredOutcome != OutcomeSkipped || stateNode.Detail == dependencySkippedDetail) {
		return runtimeStepResult(OutcomeSkipped, nil, "", false, nil), nil
	}

	var value any
	if raw, ok := node.Raw["value"]; ok {
		var err error
		value, err = evaluateLumenExpr(raw, context)
		if err != nil {
			return LumenStepResult{}, fmt.Errorf("lumen: settle %q result: %w", node.ID, err)
		}
	}
	output, err := legacyOutput(value)
	if err != nil {
		return LumenStepResult{}, fmt.Errorf("lumen: settle %q legacy output: %w", node.ID, err)
	}
	if output != stateNode.Output {
		return LumenStepResult{}, fmt.Errorf(
			"lumen: settle %q reconstructed output %q does not match journal output %q",
			node.ID,
			output,
			stateNode.Output,
		)
	}

	reason := stateNode.Detail
	if reason == "" {
		if raw, ok := node.Raw["reason"]; ok {
			if err := json.Unmarshal(raw, &reason); err != nil {
				return LumenStepResult{}, fmt.Errorf("lumen: settle %q reason: %w", node.ID, err)
			}
		}
	}
	if authoredPublicOutcome(node) {
		return publicOutcomeResult(stateNode.Outcome, value, reason, nil), nil
	}
	return authoredSettleResult(stateNode.Outcome, value, reason), nil
}

func inputResultContext(input map[string]any) resultContext {
	context := make(resultContext, len(input)+1)
	fields := make(map[string]LumenStepResult, len(input))
	for name, value := range input {
		result := LumenStepResult{Value: value, Outcome: OutcomeSucceeded}
		context.set(name, result)
		fields[name] = result
	}
	context.set("input", LumenStepResult{
		Value:   fields,
		Outcome: OutcomeSucceeded,
	})
	return context
}

func cloneResultContext(context resultContext) resultContext {
	clone := make(resultContext, len(context))
	for name, value := range context {
		clone[name] = value
	}
	return clone
}

func resultContextScope(context resultContext) (map[string]string, error) {
	scope := make(map[string]string, len(context))
	for name, result := range context {
		value, err := legacyOutput(result.result.Value)
		if err != nil {
			return nil, fmt.Errorf("encode %q: %w", name, err)
		}
		scope[name] = value
	}
	return scope, nil
}

func authoredPublicOutcome(node ir.Node) bool {
	if node.Kind != ir.NodeSettle {
		return false
	}
	var public bool
	_ = json.Unmarshal(node.Raw["publicOutcome"], &public)
	return public
}

func isPublicOutcome(value any) bool {
	switch value := value.(type) {
	case LumenPublicOutcome:
		switch value.Kind {
		case OutcomeSucceeded:
			return isPublicOutcomeResult(value.Result)
		case OutcomeDegraded:
			return isPublicOutcomeResult(value.Result) && value.Reason != ""
		case OutcomeFailed, OutcomeCanceled, OutcomeSkipped:
			return value.Reason != ""
		default:
			return false
		}
	case map[string]any:
		kind, _ := value["kind"].(string)
		switch kind {
		case OutcomeSucceeded:
			result, ok := value["result"]
			return ok && isPublicOutcomeResult(result)
		case OutcomeDegraded:
			result, ok := value["result"]
			_, reasonOK := value["reason"].(string)
			return ok && isPublicOutcomeResult(result) && reasonOK
		case OutcomeFailed, OutcomeCanceled, OutcomeSkipped:
			_, hasResult := value["result"]
			_, hasReason := value["reason"].(string)
			return hasReason && !hasResult
		}
	}
	return false
}

func isPublicOutcomeResult(value any) bool {
	switch value := value.(type) {
	case nil, string, float64, bool:
		return true
	case []any:
		for _, element := range value {
			if !isPublicOutcomeResult(element) {
				return false
			}
		}
		return true
	case map[string]any:
		for _, field := range value {
			if !isPublicOutcomeResult(field) {
				return false
			}
		}
		return true
	default:
		return false
	}
}

type resultContextValue struct {
	result      LumenStepResult
	unavailable bool
}

type resultContext map[string]resultContextValue

func (c resultContext) set(name string, result LumenStepResult) {
	c[name] = resultContextValue{result: result}
}

func (c resultContext) setUnavailable(name string, result LumenStepResult) {
	c[name] = resultContextValue{result: result, unavailable: true}
}

func (c resultContext) get(name string) (LumenStepResult, bool) {
	value, ok := c[name]
	return value.result, ok
}

func (c resultContext) isUnavailable(name string) bool {
	return c[name].unavailable
}

func stringResultContext(scope map[string]string) resultContext {
	context := make(resultContext, len(scope))
	for name, value := range scope {
		context.set(name, LumenStepResult{Value: value, Outcome: OutcomeSucceeded})
		base, member, ok := strings.Cut(name, ".")
		if !ok {
			continue
		}
		baseResult, _ := context.get(base)
		record, _ := baseResult.Value.(map[string]LumenStepResult)
		if record == nil {
			record = make(map[string]LumenStepResult)
		}
		record[member] = LumenStepResult{Value: value, Outcome: OutcomeSucceeded}
		context.set(base, LumenStepResult{Value: record, Outcome: OutcomeSucceeded})
	}
	return context
}

func (d *driver) resultContextFor(ns string, scope map[string]string) (resultContext, error) {
	view, err := d.scopeFor(ns, scope)
	if err != nil {
		return resultContext{}, err
	}
	context := stringResultContext(view)

	typedInput := d.input
	if ns != "" {
		spec := d.runEnvs[ns]
		if spec == nil {
			return resultContext{}, fmt.Errorf("namespace %q has no registered environment", ns)
		}
		typedInput, err = d.typedSubInput(ns, spec, scope)
		if err != nil {
			return resultContext{}, err
		}
	}
	for name, value := range typedInput {
		rendered, err := legacyOutput(value)
		if err != nil {
			return resultContext{}, fmt.Errorf("encode input %q: %w", name, err)
		}
		if view[name] == rendered {
			context.set(name, LumenStepResult{Value: value, Outcome: OutcomeSucceeded})
		}
	}

	type candidate struct {
		node    *nodeState
		attempt int
	}
	best := make(map[string]candidate)
	for activation, node := range d.st().Nodes {
		if node == nil || !node.Settled {
			continue
		}
		name, ok := directChildKey(activationNodeID(activation), ns)
		if !ok {
			continue
		}
		attempt := activationAttempt(activation)
		if previous, seen := best[name]; seen && attempt <= previous.attempt {
			continue
		}
		best[name] = candidate{node: node, attempt: attempt}
	}

	recovered := make(map[string]bool, len(best))
	for _, unit := range d.units {
		name, ok := directChildKey(unit.nodeID, ns)
		if !ok {
			continue
		}
		candidate, ok := best[name]
		if !ok || candidate.node.NodeID != unit.nodeID ||
			candidate.attempt != activationAttempt(unit.activation) {
			continue
		}
		result, source := resultFromNode(candidate.node)
		if source.available() {
			context.set(name, result)
			recovered[name] = true
			continue
		}
		result, exact, err := d.reconstructUnitResult(unit, candidate.node, context, scope)
		if err != nil && !errors.Is(err, errResultUnavailable) {
			return resultContext{}, err
		}
		if exact {
			context.set(name, result)
		} else {
			canonical, _ := canonicalResultOutcome(candidate.node.Outcome)
			context.setUnavailable(name, LumenStepResult{Outcome: canonical})
		}
		recovered[name] = true
	}

	for name, candidate := range best {
		if recovered[name] {
			continue
		}
		result, source := resultFromNode(candidate.node)
		if !source.available() {
			canonical, _ := canonicalResultOutcome(candidate.node.Outcome)
			context.setUnavailable(name, LumenStepResult{Outcome: canonical})
			continue
		}
		context.set(name, result)
	}
	return context, nil
}

// evaluateLumenExpr is the single recursive evaluator for value expressions.
// It preserves typed values internally; string rendering happens only at the
// template, exec, and legacy-output edges.
func evaluateLumenExpr(raw json.RawMessage, context resultContext) (any, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var expression struct {
		Kind       string            `json:"kind"`
		Value      json.RawMessage   `json:"value"`
		Name       string            `json:"name"`
		Field      string            `json:"field"`
		Base       json.RawMessage   `json:"base"`
		Expr       json.RawMessage   `json:"expr"`
		Elements   []json.RawMessage `json:"elements"`
		Operands   []json.RawMessage `json:"operands"`
		Op         string            `json:"op"`
		Args       []json.RawMessage `json:"args"`
		From       json.RawMessage   `json:"from"`
		To         json.RawMessage   `json:"to"`
		TypeName   string            `json:"typeName"`
		ID         json.RawMessage   `json:"id"`
		Capability string            `json:"capability"`
		Channel    json.RawMessage   `json:"channel"`
		Raw        string            `json:"raw"`
		Template   struct {
			Parts []map[string]json.RawMessage `json:"parts"`
		} `json:"template"`
		Entries []struct {
			Key   string          `json:"key"`
			Value json.RawMessage `json:"value"`
		} `json:"entries"`
	}
	if err := json.Unmarshal(raw, &expression); err != nil {
		var scalar any
		if scalarErr := json.Unmarshal(raw, &scalar); scalarErr != nil {
			return nil, fmt.Errorf("decode value expression: %w", err)
		}
		return scalar, nil
	}
	switch expression.Kind {
	case "":
		var value any
		if len(expression.Value) == 0 {
			if err := json.Unmarshal(raw, &value); err != nil {
				return nil, fmt.Errorf("decode value: %w", err)
			}
			return value, nil
		}
		if err := json.Unmarshal(expression.Value, &value); err != nil {
			return nil, fmt.Errorf("decode value: %w", err)
		}
		return value, nil
	case "literal":
		var value any
		if err := json.Unmarshal(expression.Value, &value); err != nil {
			return nil, fmt.Errorf("decode literal value: %w", err)
		}
		return value, nil
	case "ref":
		result, ok := context.get(expression.Name)
		if context.isUnavailable(expression.Name) && expression.Field != "outcome" {
			return nil, fmt.Errorf("%w: ref %q", errResultUnavailable, expression.Name)
		}
		if !ok {
			return nil, nil
		}
		switch expression.Field {
		case "", "value":
			return result.Value, nil
		case "outcome":
			return result.Outcome, nil
		case "error":
			return resultErrorValue(result.Error), nil
		case "kind", "result", "reason":
			return publicOutcomeField(result, expression.Field), nil
		default:
			return nil, fmt.Errorf("%w: ref field %q", ErrUnsupportedNode, expression.Field)
		}
	case "interp", "expr":
		return evaluateLumenExpr(expression.Expr, context)
	case "member":
		base, err := evaluateLumenExpr(expression.Base, context)
		if err != nil {
			return nil, err
		}
		return memberValue(base, expression.Name), nil
	case "array":
		values := make([]any, len(expression.Elements))
		for i, element := range expression.Elements {
			value, err := evaluateLumenExpr(element, context)
			if err != nil {
				return nil, err
			}
			values[i] = value
		}
		return values, nil
	case "object":
		value := make(map[string]LumenStepResult, len(expression.Entries))
		for _, entry := range expression.Entries {
			field, err := evaluateLumenExpr(entry.Value, context)
			if err != nil {
				return nil, err
			}
			value[entry.Key] = LumenStepResult{
				Value:   field,
				Outcome: OutcomeSucceeded,
			}
		}
		return value, nil
	case "operator":
		return evaluateLumenOperator(expression.Op, expression.Operands, context)
	case "call":
		args := make([]any, len(expression.Args))
		for i, rawArg := range expression.Args {
			arg, err := evaluateLumenExpr(rawArg, context)
			if err != nil {
				return nil, err
			}
			args[i] = arg
		}
		return evaluateLumenCall(expression.Name, args)
	case "path":
		if len(expression.Template.Parts) > 0 {
			return renderLumenParts(expression.Template.Parts, context)
		}
		return expression.Raw, nil
	case "handleConstruct":
		id, err := evaluateLumenExpr(expression.ID, context)
		if err != nil {
			return nil, err
		}
		idString, ok := id.(string)
		if !ok {
			return nil, nil
		}
		return map[string]any{
			"kind": "handle",
			"type": expression.TypeName,
			"id":   strings.TrimSpace(idString),
		}, nil
	case "channel-facet":
		channel, err := evaluateLumenExpr(expression.Channel, context)
		if err != nil {
			return nil, err
		}
		record, ok := channel.(map[string]any)
		if !ok || record["kind"] != "ChannelCapability" || record["capability"] != "both" {
			return nil, nil
		}
		return map[string]any{
			"kind":       "ChannelCapability",
			"channelId":  record["channelId"],
			"capability": expression.Capability,
		}, nil
	case "range":
		// The upstream eager evaluator currently treats range expressions as null.
		return nil, nil
	default:
		return nil, fmt.Errorf("%w: value expression kind %q", ErrUnsupportedNode, expression.Kind)
	}
}

func evaluateLumenOperator(op string, operands []json.RawMessage, context resultContext) (any, error) {
	if op == "?:" {
		if len(operands) != 3 {
			return nil, fmt.Errorf("%w: operator %q wants 3 operands", ErrUnsupportedNode, op)
		}
		condition, err := evaluateLumenExpr(operands[0], context)
		if err != nil {
			return nil, err
		}
		if isExprTruthy(condition) {
			return evaluateLumenExpr(operands[1], context)
		}
		return evaluateLumenExpr(operands[2], context)
	}
	arity := 2
	if op == "!" {
		arity = 1
	}
	if len(operands) != arity {
		return nil, fmt.Errorf("%w: operator %q wants %d operands", ErrUnsupportedNode, op, arity)
	}
	values := make([]any, len(operands))
	for i, operand := range operands {
		value, err := evaluateLumenExpr(operand, context)
		if err != nil {
			return nil, err
		}
		values[i] = value
	}
	if op == "!" {
		return !isExprTruthy(values[0]), nil
	}
	cmp, nan := compareExprValues(values[0], values[1])
	switch op {
	case "==":
		return !nan && cmp == 0, nil
	case "!=":
		return nan || cmp != 0, nil
	case ">=":
		return !nan && cmp >= 0, nil
	case "<=":
		return !nan && cmp <= 0, nil
	case ">":
		return !nan && cmp > 0, nil
	case "<":
		return !nan && cmp < 0, nil
	case "&&":
		return isExprTruthy(values[0]) && isExprTruthy(values[1]), nil
	case "||":
		return isExprTruthy(values[0]) || isExprTruthy(values[1]), nil
	case "in":
		return lumenContains(values[1], values[0]), nil
	case "+":
		left, leftNumber := values[0].(float64)
		right, rightNumber := values[1].(float64)
		if leftNumber && rightNumber {
			return left + right, nil
		}
		if values[0] == nil {
			return values[1], nil
		}
		if values[1] == nil {
			return values[0], nil
		}
		leftText, err := promptString(values[0])
		if err != nil {
			return nil, err
		}
		rightText, err := promptString(values[1])
		if err != nil {
			return nil, err
		}
		return leftText + rightText, nil
	case "-", "*", "/", "%":
		left, leftOK := values[0].(float64)
		right, rightOK := values[1].(float64)
		if !leftOK || !rightOK || ((op == "/" || op == "%") && right == 0) {
			return nil, nil
		}
		switch op {
		case "-":
			return left - right, nil
		case "*":
			return left * right, nil
		case "/":
			return left / right, nil
		default:
			return math.Mod(left, right), nil
		}
	default:
		return nil, fmt.Errorf("%w: operator %q", ErrUnsupportedNode, op)
	}
}

func evaluateLumenCall(name string, args []any) (any, error) {
	switch name {
	case "json":
		var value any
		if len(args) > 0 {
			value = args[0]
		}
		data, err := json.MarshalIndent(value, "", "  ")
		if err != nil {
			return nil, fmt.Errorf("encode json expression: %w", err)
		}
		return string(data), nil
	case "string":
		if len(args) == 0 {
			return "", nil
		}
		text, err := promptString(args[0])
		return text, err
	case "join":
		if len(args) == 0 {
			return "", nil
		}
		items, ok := args[0].([]any)
		if !ok {
			return "", nil
		}
		separator := ", "
		if len(args) > 1 {
			separator = jsString(args[1])
		}
		parts := make([]string, len(items))
		for i, item := range items {
			part, err := promptString(item)
			if err != nil {
				return nil, err
			}
			parts[i] = part
		}
		return strings.Join(parts, separator), nil
	case "length":
		if len(args) == 0 {
			return float64(0), nil
		}
		switch value := args[0].(type) {
		case map[string]LumenStepResult:
			return float64(len(value)), nil
		default:
			return lengthOf(value), nil
		}
	default:
		return nil, fmt.Errorf("%w: expression call %q", ErrUnsupportedNode, name)
	}
}

func renderLumenParts(parts []map[string]json.RawMessage, context resultContext) (string, error) {
	var rendered strings.Builder
	for _, part := range parts {
		var kind string
		if err := json.Unmarshal(part["kind"], &kind); err != nil {
			return "", fmt.Errorf("decode template part kind: %w", err)
		}
		if kind == "text" {
			var text string
			if err := json.Unmarshal(part["value"], &text); err != nil {
				return "", fmt.Errorf("decode template text: %w", err)
			}
			rendered.WriteString(text)
			continue
		}
		expression := part["expr"]
		if len(expression) == 0 {
			encoded, err := json.Marshal(part)
			if err != nil {
				return "", err
			}
			expression = encoded
		}
		value, err := evaluateLumenExpr(expression, context)
		if err != nil {
			return "", err
		}
		text, err := promptString(value)
		if err != nil {
			return "", err
		}
		rendered.WriteString(text)
	}
	return rendered.String(), nil
}

func promptString(value any) (string, error) {
	if value == nil {
		return "", nil
	}
	if text, ok := value.(string); ok {
		return text, nil
	}
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return "", fmt.Errorf("encode prompt value: %w", err)
	}
	return string(data), nil
}

func lumenContains(collection, value any) bool {
	switch collection := collection.(type) {
	case []any:
		for _, item := range collection {
			cmp, nan := compareExprValues(item, value)
			if !nan && cmp == 0 {
				return true
			}
		}
	case string:
		return strings.Contains(collection, jsString(value))
	case map[string]any:
		_, ok := collection[jsString(value)]
		return ok
	case map[string]LumenStepResult:
		_, ok := collection[jsString(value)]
		return ok
	}
	return false
}

func memberValue(base any, name string) any {
	switch record := base.(type) {
	case map[string]LumenStepResult:
		if field, ok := record[name]; ok {
			return field.Value
		}
	case map[string]any:
		field, ok := record[name]
		if !ok {
			return nil
		}
		if result, ok := decodedStepResult(field); ok {
			return result.Value
		}
		return field
	}
	return nil
}

func decodedStepResult(value any) (LumenStepResult, bool) {
	switch value := value.(type) {
	case LumenStepResult:
		return value, true
	case *LumenStepResult:
		if value != nil {
			return *value, true
		}
	case map[string]any:
		outcome, ok := value["outcome"].(string)
		if !ok {
			return LumenStepResult{}, false
		}
		canonical, canceled := canonicalResultOutcome(outcome)
		if outcome != canonical && !IsSucceededOutcome(outcome) && !canceled {
			return LumenStepResult{}, false
		}
		return LumenStepResult{
			Value:   value["value"],
			Outcome: canonical,
		}, true
	}
	return LumenStepResult{}, false
}

func resultErrorValue(resultError *LumenResultError) any {
	if resultError == nil {
		return nil
	}
	value := map[string]LumenStepResult{
		"reason": {Value: resultError.Reason, Outcome: OutcomeSucceeded},
	}
	if resultError.Message != "" {
		value["message"] = LumenStepResult{Value: resultError.Message, Outcome: OutcomeSucceeded}
	}
	if resultError.Retryable {
		value["retryable"] = LumenStepResult{Value: true, Outcome: OutcomeSucceeded}
	}
	if resultError.Step != "" {
		value["step"] = LumenStepResult{Value: resultError.Step, Outcome: OutcomeSucceeded}
	}
	if resultError.Attempts != 0 {
		value["attempts"] = LumenStepResult{Value: float64(resultError.Attempts), Outcome: OutcomeSucceeded}
	}
	if resultError.RetriesRemaining != nil {
		value["retriesRemaining"] = LumenStepResult{Value: float64(*resultError.RetriesRemaining), Outcome: OutcomeSucceeded}
	}
	if resultError.Canceled {
		value["canceled"] = LumenStepResult{Value: true, Outcome: OutcomeSucceeded}
	}
	return value
}

func publicOutcomeField(result LumenStepResult, field string) any {
	value := result.Value
	if !isPublicOutcome(value) {
		reason := ""
		var remaining *int
		if result.Error != nil {
			reason = result.Error.Reason
			remaining = result.Error.RetriesRemaining
		}
		value = publicOutcomeResult(result.Outcome, result.Value, reason, remaining).Value
	}
	switch outcome := value.(type) {
	case LumenPublicOutcome:
		switch field {
		case "kind":
			return outcome.Kind
		case "result":
			if outcome.Kind == OutcomeSucceeded || outcome.Kind == OutcomeDegraded {
				return settledValueFromPublicResult(outcome.Result)
			}
		case "reason":
			if outcome.Kind != OutcomeSucceeded {
				return outcome.Reason
			}
		}
	case map[string]any:
		switch field {
		case "kind":
			return outcome["kind"]
		case "result":
			if resultValue, ok := outcome["result"]; ok {
				return settledValueFromPublicResult(resultValue)
			}
		case "reason":
			return outcome["reason"]
		}
	}
	return nil
}

func settledValueFromPublicResult(value any) any {
	switch value := value.(type) {
	case []any:
		result := make([]any, len(value))
		for i, element := range value {
			result[i] = settledValueFromPublicResult(element)
		}
		return result
	case map[string]any:
		result := make(map[string]LumenStepResult, len(value))
		for key, field := range value {
			result[key] = LumenStepResult{
				Value:   settledValueFromPublicResult(field),
				Outcome: OutcomeSucceeded,
			}
		}
		return result
	default:
		return value
	}
}
