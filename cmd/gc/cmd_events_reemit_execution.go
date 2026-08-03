package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/config"
	"github.com/gastownhall/gascity/internal/events"
	"github.com/gastownhall/gascity/internal/executionevent"
	"github.com/spf13/cobra"
)

type executionReemitSummary struct {
	City       string `json:"city"`
	Run        string `json:"run"`
	Apply      bool   `json:"apply"`
	WorkCount  int    `json:"work_count"`
	StepCount  int    `json:"step_count"`
	EventCount int    `json:"event_count"`
}

type executionReemitRecorder interface {
	events.Recorder
	Close() error
}

var openExecutionReemitRecorder = func(path string, cfg config.EventsConfig, stderr io.Writer) (executionReemitRecorder, error) {
	return newFileEventsRecorder(path, cfg, stderr)
}

var executionReemitOpenStore = openAuthoritativeStoreAtForCity

var executionReemitGraphStore = func(cityPath string, cfg *config.City) (beads.Store, bool, error) {
	store, routed, err := routedGraphStoreFor(cityPath, cfg)
	return store, routed, err
}

func newEventsReemitExecutionCmd(stdout, stderr io.Writer) *cobra.Command {
	var runID string
	var apply bool
	cmd := &cobra.Command{
		Use:   "reemit-execution",
		Short: "Re-emit current graph execution facts for one explicit local run",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if !commandFlagChanged(cmd, "city") || strings.TrimSpace(cityFlag) == "" {
				return fmt.Errorf("gc events reemit-execution: --city is required")
			}
			if commandFlagChanged(cmd, "context") || commandFlagChanged(cmd, "city-url") || commandFlagChanged(cmd, "rig") {
				return fmt.Errorf("gc events reemit-execution: remote and rig selection are not supported")
			}
			if strings.TrimSpace(runID) == "" {
				return fmt.Errorf("gc events reemit-execution: --run is required")
			}
			cityPath, err := resolveCityFlagValue(strings.TrimSpace(cityFlag))
			if err != nil {
				return fmt.Errorf("gc events reemit-execution: resolving --city: %w", err)
			}
			cfg, err := loadCityConfig(cityPath, io.Discard)
			if err != nil {
				return fmt.Errorf("gc events reemit-execution: loading city: %w", err)
			}
			rootStore, memberStores, err := resolveExecutionReemitStores(cityPath, cfg, strings.TrimSpace(runID))
			if err != nil {
				return err
			}
			projection, err := executionevent.ProjectCurrent(rootStore, strings.TrimSpace(runID), memberStores...)
			if err != nil {
				return fmt.Errorf("gc events reemit-execution: projecting run: %w", err)
			}
			summary := executionReemitSummary{City: strings.TrimSpace(cityFlag), Run: strings.TrimSpace(runID), Apply: apply, WorkCount: len(projection.WorkAssociations), StepCount: len(projection.Steps), EventCount: len(projection.WorkAssociations) + len(projection.Steps)}
			if apply {
				recorder, err := openExecutionReemitRecorder(filepath.Join(cityPath, ".gc", "events.jsonl"), cfg.Events, stderr)
				if err != nil {
					return fmt.Errorf("gc events reemit-execution: opening recorder: %w", err)
				}
				emitErr := executionevent.EmitProjection(recorder, "execution-reemit", projection)
				closeErr := recorder.Close()
				if emitErr != nil {
					return fmt.Errorf("gc events reemit-execution: emitting projection: %w", emitErr)
				}
				if closeErr != nil {
					return fmt.Errorf("gc events reemit-execution: closing recorder: %w", closeErr)
				}
			}
			return json.NewEncoder(stdout).Encode(summary)
		},
	}
	cmd.Flags().StringVar(&runID, "run", "", "exact graph.v2 workflow root id")
	cmd.Flags().BoolVar(&apply, "apply", false, "append the projected execution facts")
	return cmd
}

func commandFlagChanged(cmd *cobra.Command, name string) bool {
	return cmd.Flags().Changed(name) || cmd.InheritedFlags().Changed(name)
}

func resolveExecutionReemitStores(cityPath string, cfg *config.City, runID string) (beads.Store, []beads.Store, error) {
	candidates := convoyStoreCandidatesWithProvider(cfg, cityPath, "", func(scope string) string {
		return authoritativeBeadsProviderForScope(scope, cityPath)
	})
	members := make([]beads.Store, 0, len(candidates))
	for _, candidate := range candidates {
		store, err := executionReemitOpenStore(candidate, cityPath)
		if err != nil {
			return nil, nil, fmt.Errorf("gc events reemit-execution: opening work store %s: %w", candidate, err)
		}
		members = append(members, store)
	}
	graphStore, routed, err := executionReemitGraphStore(cityPath, cfg)
	if err != nil {
		return nil, nil, fmt.Errorf("gc events reemit-execution: resolving graph store: %w", err)
	}
	if routed {
		return graphStore, members, nil
	}
	var root beads.Store
	for _, store := range members {
		if _, err := store.Get(runID); err == nil {
			if root != nil {
				return nil, nil, fmt.Errorf("gc events reemit-execution: run %q exists in multiple work stores", runID)
			}
			root = store
		} else if !isBeadNotFound(err) {
			return nil, nil, fmt.Errorf("gc events reemit-execution: probing run %q: %w", runID, err)
		}
	}
	if root == nil {
		return nil, nil, fmt.Errorf("gc events reemit-execution: run %q not found in configured work stores", runID)
	}
	return root, members, nil
}

func isBeadNotFound(err error) bool {
	return errors.Is(err, beads.ErrNotFound)
}
