package main

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestMeasureReconcilerPerfStartPairsProductionPaths(t *testing.T) {
	report, err := measureReconcilerPerfStart(
		t.Context(),
		3,
		1,
		t.TempDir(),
		validReconcilerPerfProvenance(),
	)
	if err != nil {
		t.Fatalf("measure paired start: %v", err)
	}

	if report.Coverage.MeasuredActions != 1 ||
		strings.Join(report.Coverage.MissingActions, ",") != "stop,nudge" {
		t.Fatalf("coverage = %+v, want only start measured", report.Coverage)
	}
	if report.Warmup.PairsPerAction != 1 || !report.Warmup.Excluded {
		t.Fatalf("warmup policy = %+v, want one excluded pair", report.Warmup)
	}
	if len(report.Actions) != 1 {
		t.Fatalf("actions = %d, want 1", len(report.Actions))
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
}

func TestMeasureReconcilerPerfStartRejectsInvalidCounts(t *testing.T) {
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
			_, err := measureReconcilerPerfStart(
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
		len(report.Actions) != 1 ||
		report.Actions[0].PairCount != 2 {
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
