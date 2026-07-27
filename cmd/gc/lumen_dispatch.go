package main

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	"github.com/gastownhall/gascity/internal/beadmeta"
	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/config"
	"github.com/gastownhall/gascity/internal/lumen/engine"
)

var errLumenDispatchRoute = errors.New("route resolves to no pool-capable agent template")

// lumenDispatchWork materializes ready Lumen do nodes as ordinary work beads.
func lumenDispatchWork(store beads.Store, cfg *config.City) func(context.Context, engine.WorkDispatch) (string, error) {
	return func(_ context.Context, work engine.WorkDispatch) (string, error) {
		if store == nil {
			return "", fmt.Errorf("lumen dispatch: nil work store")
		}
		existing, err := store.List(beads.ListQuery{
			Metadata: map[string]string{
				beadmeta.LumenRunMetadataKey:        work.StreamID,
				beadmeta.LumenActivationMetadataKey: work.Activation,
			},
			IncludeClosed: true,
			Live:          true,
		})
		if err != nil {
			return "", fmt.Errorf("lumen dispatch: find %s/%s: %w", work.StreamID, work.Activation, err)
		}
		if len(existing) > 0 {
			return existing[0].ID, nil
		}
		agent := findAgentByTemplate(cfg, work.Route)
		if agent == nil || !agent.SupportsGenericEphemeralSessions() {
			return "", fmt.Errorf("lumen dispatch: node %q route %q: %w", work.NodeID, work.Route, errLumenDispatchRoute)
		}

		metadata := make(map[string]string, len(work.Metadata)+6)
		for key, value := range work.Metadata {
			metadata[key] = value
		}
		metadata[beadmeta.RoutedToMetadataKey] = work.Route
		metadata[beadmeta.LumenRunMetadataKey] = work.StreamID
		metadata[beadmeta.LumenActivationMetadataKey] = work.Activation
		metadata[beadmeta.LumenAttemptMetadataKey] = strconv.Itoa(work.Attempt)
		metadata[beadmeta.RootBeadIDMetadataKey] = work.StreamID
		metadata[beadmeta.StepIDMetadataKey] = work.NodeID

		created, err := store.Create(beads.Bead{
			Type:        "task",
			Title:       work.NodeID,
			Description: work.Prompt,
			Metadata:    metadata,
		})
		if err != nil {
			return "", fmt.Errorf("lumen dispatch: create %s/%s: %w", work.StreamID, work.Activation, err)
		}
		return created.ID, nil
	}
}

// lumenObserveWork projects an ordinary work-bead close into a Lumen outcome.
func lumenObserveWork(store beads.Store) func(context.Context, string) (engine.WorkObservation, error) {
	return func(_ context.Context, beadID string) (engine.WorkObservation, error) {
		if store == nil {
			return engine.WorkObservation{}, fmt.Errorf("lumen dispatch: nil work store")
		}
		bead, err := beads.HandlesFor(store).Live.Get(beadID)
		if err != nil {
			return engine.WorkObservation{}, fmt.Errorf("lumen dispatch: observe %q: %w", beadID, err)
		}
		if bead.Status != "closed" {
			return engine.WorkObservation{}, nil
		}
		outcome := bead.Metadata[beadmeta.OutcomeMetadataKey]
		return engine.WorkObservation{
			Terminal:  true,
			Outcome:   engine.LumenOutcomeForGCOutcome(outcome),
			Output:    bead.Metadata[beadmeta.OutputJSONMetadataKey],
			Retryable: engine.LumenFailRetryableForGCOutcome(outcome),
		}, nil
	}
}
