package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/clock"
	"github.com/gastownhall/gascity/internal/events"
	sessionpkg "github.com/gastownhall/gascity/internal/session"
)

func TestMeasureReconcilerPerfComparePairsStartAndStopProductionPaths(t *testing.T) {
	report, err := measureReconcilerPerfCompare(
		t.Context(),
		3,
		1,
		t.TempDir(),
		validReconcilerPerfProvenance(),
	)
	if err != nil {
		t.Fatalf("measure paired start and stop: %v", err)
	}

	if !strings.Contains(report.Provenance.Store, "synthetic") ||
		!strings.Contains(report.Provenance.Runtime, "synthetic") ||
		!strings.Contains(report.Provenance.Workload, "synthetic") {
		t.Fatalf("provenance = %+v, want explicit synthetic provenance", report.Provenance)
	}
	if report.Coverage.MeasuredActions != 2 ||
		strings.Join(report.Coverage.MissingActions, ",") != "nudge" {
		t.Fatalf("coverage = %+v, want start and stop measured", report.Coverage)
	}
	if report.Warmup.PairsPerAction != 1 || !report.Warmup.Excluded {
		t.Fatalf("warmup policy = %+v, want one excluded pair", report.Warmup)
	}
	if len(report.Actions) != 2 {
		t.Fatalf("actions = %d, want 2", len(report.Actions))
	}
	start := report.Actions[0]
	if start.Action != reconcilerPerfActionStart ||
		start.PairCount != 3 ||
		start.MismatchCount != 0 {
		t.Fatalf("start comparison = %+v", start)
	}
	for name, arm := range map[string]reconcilerPerfArmSummary{
		"legacy": start.Legacy,
		"keyed":  start.Keyed,
	} {
		if arm.AttemptedCount != 3 ||
			arm.SampleCount != 3 ||
			arm.ErrorCount != 0 ||
			arm.MeasurementWindowNS <= 0 ||
			arm.ThroughputPerSecond <= 0 ||
			arm.Latency == nil {
			t.Errorf("%s summary = %+v, want three successful measured starts", name, arm)
		}
	}
	stop := report.Actions[1]
	if stop.Action != reconcilerPerfActionStop ||
		stop.PairCount != 3 ||
		stop.MismatchCount != 0 {
		t.Fatalf("stop comparison = %+v", stop)
	}
	for name, arm := range map[string]reconcilerPerfArmSummary{
		"legacy": stop.Legacy,
		"keyed":  stop.Keyed,
	} {
		if arm.AttemptedCount != 3 ||
			arm.SampleCount != 3 ||
			arm.ErrorCount != 0 ||
			arm.MeasurementWindowNS <= 0 ||
			arm.ThroughputPerSecond <= 0 ||
			arm.Latency == nil {
			t.Errorf("%s stop summary = %+v, want three successful measured stops", name, arm)
		}
	}
}

func TestMeasureReconcilerPerfCompareRejectsInvalidCounts(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		iter   int
		warmup int
	}{
		{name: "zero iterations", iter: 0},
		{name: "negative iterations", iter: -1},
		{name: "negative warmup", iter: 1, warmup: -1},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := measureReconcilerPerfCompare(
				context.Background(),
				tt.iter,
				tt.warmup,
				t.TempDir(),
				validReconcilerPerfProvenance(),
			)
			if err == nil {
				t.Fatal("measureReconcilerPerfStart error = nil")
			}
		})
	}
}

func TestReconcilerPerfStopLatencyEndsAtProviderEntry(t *testing.T) {
	fixture, err := newReconcilerPerfStopFixture(t.TempDir(), "entry-latency")
	if err != nil {
		t.Fatalf("new stop fixture: %v", err)
	}
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	fixture.provider.entered = entered
	fixture.provider.block = release
	neededAt := time.Unix(100, 0).UTC()
	stopEnteredAt := neededAt.Add(17 * time.Millisecond)
	fixture.provider.now = func() time.Time { return stopEnteredAt }
	tracker := &asyncStartTracker{}
	go finalizeDrainAckStopPendingSessions(
		fixture.cityPath, fixture.cfg, fixture.provider, beads.SessionStore{Store: fixture.store}, nil,
		[]sessionpkg.Info{fixture.info}, newFakeDrainOps(), newDrainTracker(), tracker, clock.Real{}, events.Discard, io.Discard,
	)
	select {
	case <-entered:
	case <-time.After(reconcilerPerfArmTimeout):
		t.Fatal("provider Stop was not entered")
	}
	close(release)
	if !tracker.wait(reconcilerPerfArmTimeout) {
		t.Fatal("blocked stop did not drain")
	}
	measurement := fixture.finish(neededAt, stopEnteredAt.Add(time.Second), nil)
	if measurement.sample.Error != "" || measurement.sample.LatencyNS == nil {
		t.Fatalf("stop measurement = %+v, want successful latency sample", measurement.sample)
	}
	if got, want := *measurement.sample.LatencyNS, stopEnteredAt.Sub(neededAt).Nanoseconds(); got != want {
		t.Fatalf("latency = %dns, want provider-entry timestamp %dns", got, want)
	}
}

func TestReconcilerPerfStopMismatchAndFailureAreNotSuccess(t *testing.T) {
	tests := []struct {
		name   string
		pairID string
		setup  func(*reconcilerPerfStopFixture)
	}{
		{
			name:   "token mismatch",
			pairID: "mismatch",
			setup: func(fixture *reconcilerPerfStopFixture) {
				if err := fixture.provider.SetMeta(fixture.sessionName, "GC_INSTANCE_TOKEN", "replacement-token"); err != nil {
					t.Fatalf("set mismatch token: %v", err)
				}
			},
		},
		{
			name:   "stop failure",
			pairID: "failure",
			setup: func(fixture *reconcilerPerfStopFixture) {
				fixture.provider.StopErrors[fixture.sessionName] = errors.New("stop failed")
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture, err := newReconcilerPerfStopFixture(t.TempDir(), test.pairID)
			if err != nil {
				t.Fatalf("new stop fixture: %v", err)
			}
			test.setup(fixture)
			tracker := &asyncStartTracker{}
			neededAt := time.Now().UTC()
			if _, err := reconcileExactSessionStartWithOwner(context.Background(), sessionStartAdmission{SessionID: fixture.info.ID, Source: sessionStartAdmissionInProcess}, exactSessionStartParams{
				CityPath: fixture.cityPath, Config: fixture.cfg, Provider: fixture.provider, Store: fixture.store,
				Clock: clock.Real{}, Recorder: events.Discard, Stdout: io.Discard, Stderr: io.Discard, AsyncStopTracker: tracker,
			}); err != nil {
				t.Fatalf("keyed stop reconciliation: %v", err)
			}
			if !tracker.wait(reconcilerPerfArmTimeout) {
				t.Fatal("keyed failed stop did not drain")
			}
			measurement := fixture.finish(neededAt, time.Now().UTC(), nil)
			if measurement.sample.Error == "" || measurement.sample.Outcome == "stopped_runtime_dead_pending_finalize" {
				t.Fatalf("failed stop reported false success: %+v", measurement.sample)
			}
		})
	}
}

func TestWaitReconcilerPerfStopTrackerDistinguishesCancellationAndTimeout(t *testing.T) {
	t.Run("canceled", func(t *testing.T) {
		tracker := &asyncStartTracker{}
		done, ok := tracker.start()
		if !ok {
			t.Fatal("start tracker = false")
		}
		defer done()
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if err := waitReconcilerPerfStopTracker(ctx, tracker, time.Second); !errors.Is(err, context.Canceled) {
			t.Fatalf("wait error = %v, want context.Canceled", err)
		}
	})
	t.Run("timeout", func(t *testing.T) {
		tracker := &asyncStartTracker{}
		done, ok := tracker.start()
		if !ok {
			t.Fatal("start tracker = false")
		}
		defer done()
		err := waitReconcilerPerfStopTracker(context.Background(), tracker, 0)
		if err == nil || !strings.Contains(err.Error(), "timed out") {
			t.Fatalf("wait error = %v, want timeout", err)
		}
	})
}

func TestReconcilerPerfStopResultErrorSynthesizesTerminalFailure(t *testing.T) {
	result := sessionStartReconcileResult{Outcome: sessionStartReconcileExhausted}
	if err := reconcilerPerfStopResultError(result); err == nil ||
		!strings.Contains(err.Error(), string(sessionStartReconcileExhausted)) {
		t.Fatalf("result error = %v, want synthesized exhausted error", err)
	}
}

func TestRunPerfReconcilerCompareEmitsVersionedJSON(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run(
		[]string{"perf", "reconciler-compare", "--iter", "2", "--warmup", "0", "--json"},
		&stdout,
		&stderr,
	)
	if code != 0 {
		t.Fatalf(
			"gc perf reconciler-compare --json exit = %d, stderr=%q stdout=%q",
			code,
			stderr.String(),
			stdout.String(),
		)
	}

	var report reconcilerPerfReport
	if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
		t.Fatalf("decode report: %v\n%s", err, stdout.String())
	}
	if report.SchemaVersion != reconcilerPerfSchemaV1 ||
		!report.OK ||
		len(report.Actions) != 2 ||
		report.Actions[0].PairCount != 2 ||
		report.Actions[1].PairCount != 2 {
		t.Fatalf("JSON report = %+v", report)
	}
}

func TestPerfReconcilerCompareFlagDefaults(t *testing.T) {
	t.Parallel()

	cmd := newPerfReconcilerCompareCmd(nil)
	iter, _ := cmd.Flags().GetInt("iter")
	warmup, _ := cmd.Flags().GetInt("warmup")
	jsonOut, _ := cmd.Flags().GetBool("json")
	if iter != 20 || warmup != 2 || jsonOut {
		t.Fatalf("defaults = iter:%d warmup:%d json:%t, want 20/2/false", iter, warmup, jsonOut)
	}
}
