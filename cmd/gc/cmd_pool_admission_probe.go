package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"time"

	"github.com/gastownhall/gascity/internal/config"
	"github.com/gastownhall/gascity/internal/fsys"
	"github.com/gastownhall/gascity/internal/runtime"
	"github.com/gastownhall/gascity/internal/runtime/observation"
	"github.com/gastownhall/gascity/internal/runtime/proctable"
	"github.com/spf13/cobra"
)

// pool-admission-probe checks the exact provider capability used by the
// start fence, without creating a session or touching the capacity ledger.
// It runs in a normal gc binary because go test deliberately forbids a live
// /proc scan.
func newPoolAdmissionProbeCmd(stdout, _ io.Writer) *cobra.Command {
	var perSession bool
	var viaController bool
	cmd := &cobra.Command{
		Use:   "pool-admission-probe",
		Short: "Read-only probe of the pool start live census",
		Args:  cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) (retErr error) {
			if viaController && !perSession {
				return fmt.Errorf("controller source requires --per-session")
			}
			cityPath, err := resolveCity()
			if err != nil {
				return err
			}
			if viaController {
				return relayControllerObservation(context.Background(), cityPath, commit, stdout)
			}
			result := map[string]any{"city_path": cityPath, "ok": false}
			observed := observation.Observation{
				Schema: observation.Schema, CityPath: cityPath,
				ObservedAt: time.Now().UTC(), Sessions: []observation.Session{},
				Processes: []observation.Process{}, UnknownReasons: []string{},
			}
			defer func() {
				if perSession {
					if message, ok := result["error"].(string); ok {
						observed.UnknownReasons = append(observed.UnknownReasons, message)
					}
					if err := writePoolSessionObservation(stdout, observed); err != nil {
						retErr = err
					}
				} else {
					if err := json.NewEncoder(stdout).Encode(result); err != nil {
						retErr = err
					}
				}
			}()
			// Use the already-materialized city configuration. loadCityConfig
			// can repair builtin pack caches, which would make this probe write.
			cfg, _, err := config.LoadWithIncludesOptions(fsys.OSFS{}, filepath.Join(cityPath, "city.toml"), skipRevisionSnapshot)
			if err != nil {
				result["error"] = fmt.Sprintf("load city: %v", err)
				return errExit
			}
			if err := validatePackRuntimeRegistrations(cfg); err != nil {
				result["error"] = fmt.Sprintf("validate provider registry: %v", err)
				return errExit
			}
			sp, err := newSessionProviderForCity(cfg, cityPath)
			if err != nil {
				result["error"] = fmt.Sprintf("construct provider: %v", err)
				return errExit
			}
			result["provider_type"] = fmt.Sprintf("%T", sp)
			if perSession {
				observed = observation.Observe(cityPath, sp, proctable.ObserveRoots, time.Now)
				if !observed.ProviderComplete || !observed.ProcessComplete {
					return errExit
				}
				return nil
			}
			running, err := sp.ListRunning("")
			if err != nil {
				result["error"] = fmt.Sprintf("list running: %v", err)
				return errExit
			}
			result["running_count"] = len(running)
			scanner, ok := runtime.AsProcessTableScanner(sp)
			if !ok {
				result["error"] = "provider lacks ProcessTableScanner"
				return errExit
			}
			processes, err := scanner.FindRuntimesBySessionID("")
			if err != nil {
				result["error"] = fmt.Sprintf("process scan: %v", err)
				return errExit
			}
			result["process_root_count"] = len(processes)
			unknownCity := 0
			for _, process := range processes {
				if process.City == "" {
					unknownCity++
				}
			}
			result["unknown_city_roots"] = unknownCity
			if unknownCity > 0 {
				result["error"] = "process roots without city identity"
				return errExit
			}
			result["ok"] = true
			return nil
		},
	}
	cmd.Flags().BoolVar(&perSession, "per-session", false, "Emit managed-session-observation/v1; incomplete coverage exits nonzero")
	cmd.Flags().BoolVar(&viaController, "via-controller", false, "Relay pinned external process evidence from the persistent controller (requires --per-session)")
	return cmd
}

func writePoolSessionObservation(stdout io.Writer, observed observation.Observation) error {
	return json.NewEncoder(stdout).Encode(struct {
		observation.Observation
		SourceRevision    string `json:"source_revision"`
		ControllerBinding string `json:"controller_binding"`
	}{observed, commit, "not_observed"})
}
