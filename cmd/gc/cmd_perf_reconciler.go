package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	goruntime "runtime"
	"strings"
	"sync"
	"time"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/clock"
	"github.com/gastownhall/gascity/internal/config"
	"github.com/gastownhall/gascity/internal/events"
	gcruntime "github.com/gastownhall/gascity/internal/runtime"
	sessionpkg "github.com/gastownhall/gascity/internal/session"
	"github.com/spf13/cobra"
)

const (
	reconcilerPerfStartTemplate = "perf-template"
	reconcilerPerfStartWorkload = "fresh-pending-create-sequential-v1"
	reconcilerPerfArmTimeout    = 30 * time.Second
)

type reconcilerPerfCmdOptions struct {
	iter    int
	warmup  int
	jsonOut bool
}

func newPerfReconcilerCompareCmd(stdout io.Writer) *cobra.Command {
	opts := reconcilerPerfCmdOptions{}
	cmd := &cobra.Command{
		Use:   "reconciler-compare",
		Short: "Compare legacy and keyed reconciler latency",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cityPath, err := os.MkdirTemp("", "gc-reconciler-perf-*")
			if err != nil {
				return fmt.Errorf("creating reconciler performance workspace: %w", err)
			}
			runErr := runPerfReconcilerCompare(
				cmd.Context(),
				opts,
				cityPath,
				currentReconcilerPerfProvenance(),
				stdout,
			)
			cleanupErr := os.RemoveAll(cityPath)
			if cleanupErr != nil {
				cleanupErr = fmt.Errorf("removing reconciler performance workspace: %w", cleanupErr)
			}
			return errors.Join(runErr, cleanupErr)
		},
	}
	cmd.Flags().IntVar(&opts.iter, "iter", 20, "number of measured legacy/keyed pairs")
	cmd.Flags().IntVar(&opts.warmup, "warmup", 2, "warmup pairs excluded from statistics")
	cmd.Flags().BoolVar(&opts.jsonOut, "json", false, "emit versioned JSON instead of a summary")
	return cmd
}

func currentReconcilerPerfProvenance() reconcilerPerfProvenance {
	revision := strings.TrimSuffix(commit, "-dirty")
	return reconcilerPerfProvenance{
		Commit: revision,
		Dirty:  revision != commit,
		GOOS:   goruntime.GOOS,
		GOARCH: goruntime.GOARCH,
		CPUs:   goruntime.NumCPU(),
	}
}

func runPerfReconcilerCompare(
	ctx context.Context,
	opts reconcilerPerfCmdOptions,
	cityPath string,
	provenance reconcilerPerfProvenance,
	stdout io.Writer,
) error {
	if stdout == nil {
		return fmt.Errorf("gc perf reconciler-compare: stdout is nil")
	}
	report, err := measureReconcilerPerfStart(ctx, opts.iter, opts.warmup, cityPath, provenance)
	if err != nil {
		return fmt.Errorf("gc perf reconciler-compare: %w", err)
	}
	if opts.jsonOut {
		if err := writeCLIJSONLine(stdout, report); err != nil {
			return fmt.Errorf("gc perf reconciler-compare: writing JSON: %w", err)
		}
		return nil
	}
	if err := writeReconcilerPerfReport(stdout, report); err != nil {
		return fmt.Errorf("gc perf reconciler-compare: %w", err)
	}
	return nil
}

type reconcilerPerfStartMeasurement struct {
	sample   reconcilerPerfArmSample
	windowNS int64
}

func measureReconcilerPerfStart(
	ctx context.Context,
	iterations int,
	warmup int,
	cityPath string,
	provenance reconcilerPerfProvenance,
) (reconcilerPerfReport, error) {
	switch {
	case ctx == nil:
		return reconcilerPerfReport{}, fmt.Errorf("context is nil")
	case iterations <= 0:
		return reconcilerPerfReport{}, fmt.Errorf("iterations must be positive")
	case warmup < 0:
		return reconcilerPerfReport{}, fmt.Errorf("warmup must be non-negative")
	case strings.TrimSpace(cityPath) == "":
		return reconcilerPerfReport{}, fmt.Errorf("workspace path is empty")
	}

	provenance.Store = "beads.MemStore"
	provenance.StoreSchema = "none"
	provenance.Runtime = "runtime.Fake"
	provenance.Workload = reconcilerPerfStartWorkload
	pairs := make([]reconcilerPerfPairSample, 0, iterations)
	var legacyWindowNS, keyedWindowNS int64
	for sequence := 0; sequence < warmup+iterations; sequence++ {
		if err := ctx.Err(); err != nil {
			return reconcilerPerfReport{}, err
		}
		measuredIndex := sequence - warmup + 1
		pairID := fmt.Sprintf("warmup-%06d", sequence+1)
		if measuredIndex > 0 {
			pairID = fmt.Sprintf("start-%06d", measuredIndex)
		}

		var legacy, keyed reconcilerPerfStartMeasurement
		var err error
		if sequence%2 == 0 {
			legacy, err = measureLegacyReconcilerPerfStart(ctx, cityPath, pairID)
			if err == nil {
				keyed, err = measureKeyedReconcilerPerfStart(ctx, cityPath, pairID)
			}
		} else {
			keyed, err = measureKeyedReconcilerPerfStart(ctx, cityPath, pairID)
			if err == nil {
				legacy, err = measureLegacyReconcilerPerfStart(ctx, cityPath, pairID)
			}
		}
		if err != nil {
			return reconcilerPerfReport{}, fmt.Errorf("measuring pair %q: %w", pairID, err)
		}
		if measuredIndex <= 0 {
			continue
		}
		pairs = append(pairs, reconcilerPerfPairSample{
			PairID: pairID,
			Legacy: legacy.sample,
			Keyed:  keyed.sample,
		})
		legacyWindowNS += legacy.windowNS
		keyedWindowNS += keyed.windowNS
	}

	return buildReconcilerPerfReport(reconcilerPerfReportInput{
		Provenance: provenance,
		Warmup: reconcilerPerfWarmupPolicy{
			PairsPerAction: warmup,
			Excluded:       true,
			ExecutionOrder: "alternating_first_arm_legacy_first",
		},
		Cohorts: []reconcilerPerfActionCohort{{
			Action:         reconcilerPerfActionStart,
			LegacyWindowNS: legacyWindowNS,
			KeyedWindowNS:  keyedWindowNS,
			Pairs:          pairs,
		}},
	})
}

type reconcilerPerfStartCall struct {
	enteredAt time.Time
	err       error
}

type reconcilerPerfStartProvider struct {
	gcruntime.Provider

	mu    sync.Mutex
	calls []reconcilerPerfStartCall
}

func (p *reconcilerPerfStartProvider) Start(
	ctx context.Context,
	name string,
	cfg gcruntime.Config,
) error {
	call := reconcilerPerfStartCall{enteredAt: time.Now().UTC()}
	call.err = p.Provider.Start(ctx, name, cfg)
	p.mu.Lock()
	p.calls = append(p.calls, call)
	p.mu.Unlock()
	return call.err
}

func (p *reconcilerPerfStartProvider) snapshotCalls() []reconcilerPerfStartCall {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]reconcilerPerfStartCall(nil), p.calls...)
}

type reconcilerPerfStartFixture struct {
	cityPath    string
	cityName    string
	sessionName string
	infoID      string
	bead        beads.Bead
	cfg         *config.City
	desired     map[string]TemplateParams
	store       beads.Store
	provider    *reconcilerPerfStartProvider
}

func newReconcilerPerfStartFixture(
	cityPath string,
	pairID string,
) (*reconcilerPerfStartFixture, error) {
	const cityName = "reconciler-perf"
	sessionName := "gc-reconciler-perf-" + pairID
	store := beads.NewMemStore()
	provider := &reconcilerPerfStartProvider{Provider: gcruntime.NewFake()}
	cfg := &config.City{
		Workspace: config.Workspace{Name: cityName},
		Agents: []config.Agent{{
			Name:         reconcilerPerfStartTemplate,
			StartCommand: "true",
		}},
		Session: config.SessionConfig{StartupTimeout: "10s"},
	}
	template := TemplateParams{
		Command:      "true",
		WorkDir:      cityPath,
		SessionName:  sessionName,
		TemplateName: reconcilerPerfStartTemplate,
	}
	now := time.Now().UTC()
	info, err := sessionFrontDoor(store).CreateSessionInfo(sessionpkg.CreateSpec{
		Title:     pairID,
		AgentName: reconcilerPerfStartTemplate,
		Metadata: map[string]string{
			"session_name":              sessionName,
			"agent_name":                reconcilerPerfStartTemplate,
			"template":                  reconcilerPerfStartTemplate,
			"generation":                "1",
			"instance_token":            "perf-token-" + pairID,
			"live_hash":                 gcruntime.LiveFingerprint(templateParamsToConfig(template)),
			"state":                     string(sessionpkg.StateCreating),
			"pending_create_claim":      "true",
			"pending_create_started_at": now.Format(time.RFC3339Nano),
		},
	})
	if err != nil {
		return nil, fmt.Errorf("creating pending session: %w", err)
	}
	bead, err := store.Get(info.ID)
	if err != nil {
		return nil, fmt.Errorf("reading pending session: %w", err)
	}
	return &reconcilerPerfStartFixture{
		cityPath:    cityPath,
		cityName:    cityName,
		sessionName: sessionName,
		infoID:      info.ID,
		bead:        bead,
		cfg:         cfg,
		desired:     map[string]TemplateParams{sessionName: template},
		store:       store,
		provider:    provider,
	}, nil
}

func measureLegacyReconcilerPerfStart(
	ctx context.Context,
	cityPath string,
	pairID string,
) (reconcilerPerfStartMeasurement, error) {
	fixture, err := newReconcilerPerfStartFixture(cityPath, pairID)
	if err != nil {
		return reconcilerPerfStartMeasurement{}, err
	}
	var stdout, stderr bytes.Buffer
	neededAt := time.Now().UTC()
	woken := reconcileSessionBeadsAtPath(
		ctx,
		fixture.cityPath,
		[]beads.Bead{fixture.bead},
		fixture.desired,
		configuredSessionNames(fixture.cfg, fixture.cityName, fixture.store),
		fixture.cfg,
		fixture.provider,
		fixture.store,
		nil,
		nil,
		nil,
		nil,
		newDrainTracker(),
		map[string]int{reconcilerPerfStartTemplate: 1},
		false,
		nil,
		fixture.cityName,
		nil,
		clock.Real{},
		events.Discard,
		fixture.cfg.Session.StartupTimeoutDuration(),
		0,
		&stdout,
		&stderr,
	)
	finishedAt := time.Now().UTC()
	var reconcileErr error
	if woken != 1 {
		reconcileErr = fmt.Errorf("legacy wake count = %d, want 1; stderr=%q", woken, stderr.String())
	}
	return fixture.finish(neededAt, finishedAt, reconcileErr), nil
}

func measureKeyedReconcilerPerfStart(
	ctx context.Context,
	cityPath string,
	pairID string,
) (reconcilerPerfStartMeasurement, error) {
	fixture, err := newReconcilerPerfStartFixture(cityPath, pairID)
	if err != nil {
		return reconcilerPerfStartMeasurement{}, err
	}
	results := make(chan sessionStartReconcileResult, 8)
	controller, err := newSessionStartController(sessionStartControllerOptions{
		Workers:     maxParallelStartsPerTick(fixture.cfg),
		MaxDistinct: sessionStartControllerMaxDistinct,
		MaxRetries:  sessionStartControllerMaxRetries,
		Reconcile: func(reconcileCtx context.Context, admission sessionStartAdmission) error {
			return reconcileExactSessionStart(reconcileCtx, admission, exactSessionStartParams{
				CityPath: fixture.cityPath,
				CityName: fixture.cityName,
				Config:   fixture.cfg,
				Provider: fixture.provider,
				Store:    fixture.store,
				Clock:    clock.Real{},
				Recorder: events.Discard,
				Stdout:   io.Discard,
				Stderr:   io.Discard,
			})
		},
		Observer: func(result sessionStartReconcileResult) {
			results <- result
		},
		Stderr: io.Discard,
	})
	if err != nil {
		return reconcilerPerfStartMeasurement{}, fmt.Errorf("creating keyed controller: %w", err)
	}
	if err := controller.Start(ctx); err != nil {
		controller.Stop()
		return reconcilerPerfStartMeasurement{}, fmt.Errorf("starting keyed controller: %w", err)
	}
	defer controller.Stop()

	neededAt := time.Now().UTC()
	owner, ownerErr := exactSessionStartOwnerForKey(
		fixture.store,
		fixture.cfg,
		fixture.infoID,
		neededAt,
	)
	if ownerErr != nil || owner != exactSessionStartKeyedOwner {
		finishedAt := time.Now().UTC()
		if ownerErr == nil {
			ownerErr = fmt.Errorf("keyed ownership = %d, want %d", owner, exactSessionStartKeyedOwner)
		}
		return fixture.finish(neededAt, finishedAt, ownerErr), nil
	}
	admission, admitErr := controller.Admit(fixture.infoID, sessionStartAdmissionSocket)
	if admitErr != nil || admission == sessionStartAdmissionOverflow {
		finishedAt := time.Now().UTC()
		if admitErr == nil {
			admitErr = fmt.Errorf("keyed admission overflow")
		}
		return fixture.finish(neededAt, finishedAt, admitErr), nil
	}

	waitCtx, cancel := context.WithTimeout(ctx, reconcilerPerfArmTimeout)
	defer cancel()
	for {
		select {
		case result := <-results:
			if result.Outcome == sessionStartReconcileRetrying {
				continue
			}
			finishedAt := result.FinishedAt
			if finishedAt.IsZero() {
				finishedAt = time.Now().UTC()
			}
			resultErr := result.Err
			if result.Outcome != sessionStartReconcileSucceeded && resultErr == nil {
				resultErr = fmt.Errorf("keyed reconciliation outcome = %s", result.Outcome)
			}
			return fixture.finish(neededAt, finishedAt, resultErr), nil
		case <-waitCtx.Done():
			return fixture.finish(neededAt, time.Now().UTC(), waitCtx.Err()), nil
		}
	}
}

func (f *reconcilerPerfStartFixture) finish(
	neededAt time.Time,
	finishedAt time.Time,
	reconcileErr error,
) reconcilerPerfStartMeasurement {
	calls := f.provider.snapshotCalls()
	problems := make([]error, 0, 4)
	if reconcileErr != nil {
		problems = append(problems, reconcileErr)
	}
	if len(calls) != 1 {
		problems = append(problems, fmt.Errorf("provider Start calls = %d, want 1", len(calls)))
	}

	var latency *int64
	outcome := "not_started"
	if len(calls) != 0 {
		value := calls[0].enteredAt.Sub(neededAt).Nanoseconds()
		latency = &value
		outcome = "started"
		if value < 0 {
			problems = append(problems, fmt.Errorf("provider Start preceded action-needed timestamp"))
		}
		if calls[0].err != nil {
			outcome = "provider_error"
			problems = append(problems, calls[0].err)
		}
	}
	info, err := sessionFrontDoor(f.store).Get(f.infoID)
	switch {
	case err != nil:
		problems = append(problems, fmt.Errorf("reading reconciled session: %w", err))
	case info.MetadataState != string(sessionpkg.StateActive):
		problems = append(problems, fmt.Errorf("persisted state = %q, want active", info.MetadataState))
		if outcome == "started" {
			outcome = "started_not_active"
		}
	default:
		if outcome == "started" {
			outcome = "started_active"
		}
	}

	sample := reconcilerPerfArmSample{LatencyNS: latency, Outcome: outcome}
	if joined := errors.Join(problems...); joined != nil {
		sample.Error = joined.Error()
	}
	return reconcilerPerfStartMeasurement{
		sample:   sample,
		windowNS: finishedAt.Sub(neededAt).Nanoseconds(),
	}
}
