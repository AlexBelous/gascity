package main

import (
	"context"
	"io"
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/clock"
	"github.com/gastownhall/gascity/internal/config"
	"github.com/gastownhall/gascity/internal/events"
	"github.com/gastownhall/gascity/internal/runtime"
	sessionpkg "github.com/gastownhall/gascity/internal/session"
	"github.com/gastownhall/gascity/internal/session/sessiontest"
)

func TestExecutePreparedStartWaveUsesWorkerBoundaryForKnownSession(t *testing.T) {
	store := beads.NewMemStore()
	sp := runtime.NewFake()
	mgr := newSessionManagerWithConfig("", store, sp, nil)
	info, err := mgr.CreateSession(context.Background(), sessionpkg.CreateOptions{BeadOnly: true, Template: "worker", Title: "Worker", Command: "claude", WorkDir: t.TempDir(), Provider: "claude", Transport: "", Resume: sessionpkg.ProviderResume{}})
	if err != nil {
		t.Fatalf("CreateBeadOnly: %v", err)
	}
	bead, err := store.Get(info.ID)
	if err != nil {
		t.Fatalf("Get bead: %v", err)
	}

	results := executePreparedStartWave(
		context.Background(),
		[]preparedStart{{
			candidate: startCandidate{
				info: sessiontest.SeedBead(t, bead),
				tp:   TemplateParams{TemplateName: "worker"},
			},
			cfg: runtime.Config{
				Command: "claude --resume seeded-session",
				WorkDir: info.WorkDir,
			},
		}},
		sp,
		store,
		10*time.Second,
	)
	if len(results) != 1 {
		t.Fatalf("len(results) = %d, want 1", len(results))
	}
	if results[0].err != nil {
		t.Fatalf("start result err = %v, want nil", results[0].err)
	}

	got, err := mgr.Get(info.ID)
	if err != nil {
		t.Fatalf("Get session: %v", err)
	}
	if got.State != sessionpkg.StateStartPending {
		t.Fatalf("state = %q, want %q before lifecycle commit", got.State, sessionpkg.StateStartPending)
	}
	updatedBead, err := store.Get(info.ID)
	if err != nil {
		t.Fatalf("Get updated bead: %v", err)
	}
	if updatedBead.Metadata["pending_create_claim"] != "true" {
		t.Fatalf("pending_create_claim = %q, want preserved before commit", updatedBead.Metadata["pending_create_claim"])
	}
	if !sp.IsRunning(info.SessionName) {
		t.Fatal("session should be running after prepared start")
	}
}

func TestStartPreparedStartCandidateUsesWorkerBoundaryForRuntimeOnlyTarget(t *testing.T) {
	sp := runtime.NewFake()

	usedWorker, err := startPreparedStartCandidate(
		context.Background(),
		preparedStart{
			candidate: startCandidate{
				info: sessionpkg.Info{SessionName: "legacy-runtime-only", SessionNameMetadata: "legacy-runtime-only"},
				tp:   TemplateParams{TemplateName: "worker"},
			},
			cfg: runtime.Config{
				Command: "claude --resume seeded",
				WorkDir: t.TempDir(),
			},
		},
		"",
		nil,
		sp,
		nil,
		nil,
		nil,
		nil,
	)
	if err != nil {
		t.Fatalf("startPreparedStartCandidate: %v", err)
	}
	if !usedWorker {
		t.Fatal("usedWorker = false, want true")
	}
	if !sp.IsRunning("legacy-runtime-only") {
		t.Fatal("legacy-runtime-only should be running after prepared start")
	}
	var start runtime.Call
	foundStart := false
	for _, call := range sp.Calls {
		if call.Method == "Start" {
			start = call
			foundStart = true
			break
		}
	}
	if !foundStart {
		t.Fatalf("runtime calls = %#v, want Start", sp.Calls)
	}
	if start.Name != "legacy-runtime-only" {
		t.Fatalf("start name = %q, want legacy-runtime-only", start.Name)
	}
	if start.Config.Command != "claude --resume seeded" {
		t.Fatalf("start command = %q, want claude --resume seeded", start.Config.Command)
	}
}

// Suspension can land after the awake decision and after a start candidate has
// been prepared. The execution boundary must re-check it immediately before
// touching the provider, and a canceled pending create must remain durable for
// a later resume rather than being committed or rolled back as a failed start.
func TestRunPreparedStartCandidate_CitySuspendedAfterPreparationSkipsProviderStart(t *testing.T) {
	store := beads.NewMemStore()
	sp := runtime.NewFake()
	bead, err := store.Create(beads.Bead{
		Title:  "cloud-sentinel",
		Type:   sessionBeadType,
		Status: "open",
		Labels: []string{sessionBeadLabel},
		Metadata: map[string]string{
			"state":                "creating",
			"session_name":         "cloud-sentinel",
			"template":             "cloud/sentinel",
			"pending_create_claim": "true",
		},
	})
	if err != nil {
		t.Fatalf("create session bead: %v", err)
	}
	item := preparedStart{
		candidate: startCandidate{
			info: sessiontest.SeedBead(t, bead),
			tp: TemplateParams{
				Command:      "sleep 60",
				SessionName:  "cloud-sentinel",
				TemplateName: "cloud/sentinel",
			},
		},
		cfg: runtime.Config{Command: "sleep 60", WorkDir: t.TempDir()},
	}
	cfg := &config.City{Workspace: config.Workspace{SuspendedOnStart: true}}
	clk := &clock.Fake{Time: time.Date(2026, 9, 2, 22, 0, 0, 0, time.UTC)}

	result := runPreparedStartCandidate(
		context.Background(), item, t.TempDir(), sp, store, cfg, 0,
		nil, nil, nil,
	)
	if !result.suppressed {
		t.Fatalf("result = %+v, want start suppressed by current city suspension", result)
	}
	if result.err != nil {
		t.Fatalf("suppressed start err = %v, want nil (not a provider failure)", result.err)
	}
	if sp.CountCalls("Start", "cloud-sentinel") != 0 || sp.IsRunning("cloud-sentinel") {
		t.Fatalf("runtime calls = %#v, suspended city must not reach provider Start", sp.SnapshotCalls())
	}
	if commitStartResult(result, sessionFrontDoor(store), clk, events.NewFake(), 0, io.Discard, io.Discard) {
		t.Fatal("suppressed start unexpectedly committed")
	}
	got, err := store.Get(bead.ID)
	if err != nil {
		t.Fatalf("get session bead: %v", err)
	}
	if got.Metadata["pending_create_claim"] != "true" {
		t.Fatalf("pending_create_claim = %q, want durable claim preserved for resume", got.Metadata["pending_create_claim"])
	}
}
