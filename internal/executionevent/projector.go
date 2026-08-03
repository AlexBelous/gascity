// Package executionevent projects the current authoritative graph.v2 workflow
// facts into redacted execution event envelopes.
package executionevent

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/gastownhall/gascity/internal/beadmeta"
	"github.com/gastownhall/gascity/internal/beads"
	convoycore "github.com/gastownhall/gascity/internal/convoy"
	"github.com/gastownhall/gascity/internal/events"
	"github.com/gastownhall/gascity/pkg/eventexport"
)

var (
	// ErrNotGraphV2Root means the selected bead is not an authoritative graph.v2
	// workflow root and therefore cannot define execution facts.
	ErrNotGraphV2Root = errors.New("executionevent: root is not a graph.v2 workflow")
	// ErrMissingInputConvoy means a graph.v2 root has no authoritative input
	// convoy reference and therefore has no work association to project.
	ErrMissingInputConvoy = errors.New("executionevent: graph.v2 root is missing gc.input_convoy_id")
	// ErrInvalidRootReference means the selected root cannot be emitted as an
	// opaque execution-run reference.
	ErrInvalidRootReference = errors.New("executionevent: invalid root reference")
)

// WorkAssociation is one authoritative input work bead attached to an
// execution workflow. WorkBeadID is a physical bead identity; ExecutionRunID
// is the graph.v2 workflow root identity.
type WorkAssociation struct {
	WorkBeadID     string
	ExecutionRunID string
}

// StepDefinition is one materialized native execution step. A nil
// DependsOnStepIDs means topology is UNKNOWN; a non-nil empty slice is an
// authoritative root; a non-empty slice is an authoritative prerequisite set.
type StepDefinition struct {
	BeadID           string
	ExecutionRunID   string
	StepID           string
	DependsOnStepIDs *[]string
}

// Projection is the deterministic current-store execution projection for one
// graph.v2 workflow root.
type Projection struct {
	WorkAssociations []WorkAssociation
	Steps            []StepDefinition
}

// ProjectCurrent projects only current, canonical facts for rootID. The root
// must be a graph.v2 workflow with an exact gc.input_convoy_id. Membership is
// the union of explicit tracks dependencies and gc.tracking_convoy_id metadata
// across the supplied current stores; parent-child edges are never treated as
// input-work membership. The root store owns the workflow graph, while optional
// member stores cover cross-store input work.
func ProjectCurrent(rootStore beads.Store, rootID string, memberStores ...beads.Store) (Projection, error) {
	if rootStore == nil {
		return Projection{}, fmt.Errorf("%w: nil store", ErrNotGraphV2Root)
	}
	if !eventexport.IsOpaqueRef(rootID) {
		return Projection{}, fmt.Errorf("%w: %q", ErrInvalidRootReference, rootID)
	}
	root, err := rootStore.Get(strings.TrimSpace(rootID))
	if err != nil {
		return Projection{}, fmt.Errorf("loading workflow root %q: %w", rootID, err)
	}
	if root.Metadata[beadmeta.KindMetadataKey] != beadmeta.KindWorkflow ||
		root.Metadata[beadmeta.FormulaContractMetadataKey] != beadmeta.FormulaContractGraphV2 {
		return Projection{}, ErrNotGraphV2Root
	}
	if !eventexport.IsOpaqueRef(root.ID) {
		return Projection{}, fmt.Errorf("%w: %q", ErrInvalidRootReference, root.ID)
	}
	convoyID := strings.TrimSpace(root.Metadata[beadmeta.InputConvoyIDMetadataKey])
	if convoyID == "" {
		return Projection{}, ErrMissingInputConvoy
	}

	stores := append([]beads.Store{rootStore}, memberStores...)
	workIDs, err := exactMembership(stores, convoyID)
	if err != nil {
		return Projection{}, err
	}
	steps, err := currentSteps(rootStore, root)
	if err != nil {
		return Projection{}, err
	}
	associations := make([]WorkAssociation, 0, len(workIDs))
	for _, workID := range workIDs {
		associations = append(associations, WorkAssociation{WorkBeadID: workID, ExecutionRunID: root.ID})
	}
	return Projection{WorkAssociations: associations, Steps: steps}, nil
}

func exactMembership(stores []beads.Store, convoyID string) ([]string, error) {
	ids := make(map[string]struct{})
	for _, store := range stores {
		if store == nil {
			continue
		}
		deps, err := store.DepList(convoyID, "down")
		if err == nil {
			for _, dep := range deps {
				if dep.Type != convoycore.TrackingDepType || dep.IssueID != convoyID || strings.TrimSpace(dep.DependsOnID) == "" {
					continue
				}
				if !eventexport.IsOpaqueRef(dep.DependsOnID) {
					continue
				}
				if conflict, err := membershipConflict(stores, dep.DependsOnID, convoyID); err != nil {
					return nil, err
				} else if conflict {
					// The two canonical representations disagree. A dangling target
					// remains a usable opaque ref, but an existing contradictory
					// member is UNKNOWN rather than a fabricated association.
					continue
				}
				ids[dep.DependsOnID] = struct{}{}
			}
		} else if !errors.Is(err, beads.ErrNotFound) {
			return nil, fmt.Errorf("listing tracks membership for convoy %s: %w", convoyID, err)
		}

		members, err := store.ListByMetadata(map[string]string{beadmeta.TrackingConvoyIDMetadataKey: convoyID}, 0, beads.IncludeClosed, beads.WithBothTiers)
		if err != nil {
			return nil, fmt.Errorf("listing metadata membership for convoy %s: %w", convoyID, err)
		}
		for _, member := range members {
			if !eventexport.IsOpaqueRef(member.ID) {
				continue
			}
			if conflict, err := membershipConflict(stores, member.ID, convoyID); err != nil {
				return nil, err
			} else if !conflict {
				ids[member.ID] = struct{}{}
			}
		}
	}
	result := make([]string, 0, len(ids))
	for id := range ids {
		result = append(result, id)
	}
	sort.Strings(result)
	return result, nil
}

func membershipConflict(stores []beads.Store, id, convoyID string) (bool, error) {
	for _, store := range stores {
		if store == nil {
			continue
		}
		member, err := store.Get(id)
		if err == nil {
			if trackedBy := strings.TrimSpace(member.Metadata[beadmeta.TrackingConvoyIDMetadataKey]); trackedBy != "" && trackedBy != convoyID {
				return true, nil
			}
		} else if !errors.Is(err, beads.ErrNotFound) {
			return false, fmt.Errorf("loading tracked member %s: %w", id, err)
		}

		deps, err := store.DepList(id, "up")
		if err != nil && !errors.Is(err, beads.ErrNotFound) {
			return false, fmt.Errorf("listing tracks membership for member %s: %w", id, err)
		}
		for _, dep := range deps {
			if dep.Type == convoycore.TrackingDepType && dep.IssueID != convoyID {
				return true, nil
			}
		}
	}
	return false, nil
}

func currentSteps(store beads.Store, root beads.Bead) ([]StepDefinition, error) {
	rows, err := store.ListByMetadata(map[string]string{beadmeta.RootBeadIDMetadataKey: root.ID}, 0, beads.IncludeClosed, beads.WithBothTiers)
	if err != nil {
		return nil, fmt.Errorf("listing workflow steps for root %s: %w", root.ID, err)
	}
	byID := make(map[string]beads.Bead, len(rows))
	for _, row := range rows {
		byID[row.ID] = row
	}
	ids := make([]string, 0, len(byID))
	for id := range byID {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	steps := make([]StepDefinition, 0, len(ids))
	for _, id := range ids {
		row := byID[id]
		if row.ID == root.ID || !eventexport.IsOpaqueRef(row.ID) {
			continue
		}
		stepID := row.Metadata[beadmeta.StepIDMetadataKey]
		if !validNativeStepID(stepID) {
			continue
		}
		steps = append(steps, StepDefinition{
			BeadID:           row.ID,
			ExecutionRunID:   root.ID,
			StepID:           stepID,
			DependsOnStepIDs: canonicalTopology(row.Metadata, stepID),
		})
	}
	return steps, nil
}

func validNativeStepID(id string) bool {
	return len(id) <= 256 && utf8.ValidString(id) && strings.TrimSpace(id) != ""
}

func canonicalTopology(metadata map[string]string, stepID string) *[]string {
	raw, ok := metadata[beadmeta.NativeStepDependenciesMetadataKey]
	if !ok || !validNativeStepID(stepID) {
		return nil
	}
	var dependencies []string
	if err := json.Unmarshal([]byte(raw), &dependencies); err != nil || dependencies == nil {
		return nil
	}
	previous := ""
	for _, dependency := range dependencies {
		if !validNativeStepID(dependency) || dependency == stepID || (previous != "" && dependency <= previous) {
			return nil
		}
		previous = dependency
	}
	canonical, err := json.Marshal(dependencies)
	if err != nil || raw != string(canonical) {
		return nil
	}
	return &dependencies
}

// EmitProjection records a precomputed execution projection as envelope-only
// events. A nil recorder is a no-op so graph materialization remains available
// when event recording is disabled.
func EmitProjection(recorder events.Recorder, actor string, projection Projection) error {
	if recorder == nil {
		return nil
	}
	actor = strings.TrimSpace(actor)
	if actor == "" {
		actor = "execution-projector"
	}
	for _, association := range projection.WorkAssociations {
		recorder.Record(events.Event{
			Type:    events.ExecutionWorkAssociated,
			Actor:   actor,
			Subject: association.WorkBeadID,
			RunID:   association.ExecutionRunID,
		})
	}
	for _, step := range projection.Steps {
		recorder.Record(events.Event{
			Type:             events.ExecutionStepDefined,
			Actor:            actor,
			Subject:          step.BeadID,
			RunID:            step.ExecutionRunID,
			StepID:           step.StepID,
			DependsOnStepIDs: cloneTopology(step.DependsOnStepIDs),
		})
	}
	return nil
}

// EmitCurrent projects and records the current execution facts.
func EmitCurrent(recorder events.Recorder, actor string, rootStore beads.Store, rootID string, memberStores ...beads.Store) error {
	if recorder == nil {
		return nil
	}
	projection, err := ProjectCurrent(rootStore, rootID, memberStores...)
	if err != nil {
		return err
	}
	return EmitProjection(recorder, actor, projection)
}

func cloneTopology(dependencies *[]string) *[]string {
	if dependencies == nil {
		return nil
	}
	clone := make([]string, len(*dependencies))
	copy(clone, *dependencies)
	return &clone
}
