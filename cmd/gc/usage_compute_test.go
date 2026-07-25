package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/config"
	"github.com/gastownhall/gascity/internal/runtime"
	"github.com/gastownhall/gascity/internal/session"
	"github.com/gastownhall/gascity/internal/session/sessiontest"
	"github.com/gastownhall/gascity/internal/usage"
)

// TestComputeFactGetCandidate is the usage-lane Get-budget gate: emitDueComputeFacts only
// issues a per-session store Get when computeFactGetCandidate returns true, so this pins
// the pre-Get filter that keeps a steady fleet of parked, already-accounted sessions at
// zero Gets. A mutation that drops any filter clause (terminal-state, awake-interval
// present, or interval-not-already-emitted) flips a case and fails.
func TestComputeFactGetCandidate(t *testing.T) {
	info := func(state, awake, emitted string) session.Info {
		return sessiontest.SeedBead(t, beads.Bead{
			ID: "gc-x", Type: session.BeadType, Status: "open", Labels: []string{session.LabelSession},
			Metadata: map[string]string{"state": state, "awake_started_at": awake, "usage_compute_emitted_at": emitted},
		})
	}
	const t1 = "2026-01-02T00:30:00Z"
	cases := []struct {
		name string
		info session.Info
		want bool
	}{
		{"active-not-terminal", info("active", t1, ""), false},
		{"terminal-no-awake", info("asleep", "", ""), false},
		{"terminal-awake-not-emitted", info("asleep", t1, ""), true},
		{"terminal-awake-already-emitted", info("asleep", t1, t1), false},
		{"terminal-awake-emitted-stale-interval", info("asleep", t1, "2026-01-01T00:00:00Z"), true},
		{"drained-terminal", info("drained", t1, ""), true},
	}
	for _, tc := range cases {
		if got := computeFactGetCandidate(tc.info); got != tc.want {
			t.Errorf("%s: computeFactGetCandidate = %v, want %v", tc.name, got, tc.want)
		}
	}
}

type captureSink struct{ facts []usage.Fact }

func (c *captureSink) Record(_ context.Context, f usage.Fact) error {
	c.facts = append(c.facts, f)
	return nil
}

type erroringSink struct{ calls int }

func (e *erroringSink) Record(context.Context, usage.Fact) error {
	e.calls++
	return errors.New("disk full")
}

func TestEmitComputeFactForBead(t *testing.T) {
	store := beads.NewMemStore()
	start := time.Date(2026, 6, 15, 10, 0, 0, 0, time.UTC)
	slept := start.Add(90 * time.Second)
	b, err := store.Create(beads.Bead{
		Title: "session",
		Metadata: map[string]string{
			"state":               "asleep",
			"session_name":        "s-x",
			"awake_started_at":    start.Format(time.RFC3339),
			"slept_at":            slept.Format(time.RFC3339),
			"molecule_id":         "mol-7",
			"gc.active_work_bead": "mol.finalize", // the step the session was on; cleared at this terminal pass
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	sink := &captureSink{}
	now := slept.Add(5 * time.Second)

	if !emitComputeFactForBead(context.Background(), sink, store, b, "fake", "demo", now, nil, true) {
		t.Fatal("expected first emit to record a fact")
	}
	if len(sink.facts) != 1 {
		t.Fatalf("want 1 fact, got %d", len(sink.facts))
	}
	f := sink.facts[0]
	if f.Kind != usage.KindCompute {
		t.Fatalf("kind = %q", f.Kind)
	}
	if f.WallSeconds != 90 {
		t.Fatalf("wall = %v, want 90 (slept_at - awake_started_at)", f.WallSeconds)
	}
	if f.RunID != "mol-7" {
		t.Fatalf("runID = %q, want mol-7", f.RunID)
	}
	// SessionID is the session bead id (distinct from RunID mol-7 here), so a
	// session-keyed rollup joins compute facts symmetrically with model facts.
	if f.SessionID != b.ID {
		t.Fatalf("SessionID = %q, want the session bead id %q", f.SessionID, b.ID)
	}
	if f.Runtime != "fake" || f.City != "demo" || f.Worker != "s-x" {
		t.Fatalf("unexpected fact fields: %+v", f)
	}
	if f.IdempotencyKey == "" {
		t.Fatal("missing idempotency key")
	}

	// Marker should now suppress re-emit. Re-fetch the bead (marker persisted).
	refreshed, err := store.Get(b.ID)
	if err != nil {
		t.Fatal(err)
	}
	// The terminal pass also CLEARS the active-work-bead pointer, so an idle
	// invocation after this work attributes at run level (StepID="") not the old step.
	if got := refreshed.Metadata["gc.active_work_bead"]; got != "" {
		t.Fatalf("gc.active_work_bead = %q, want cleared (\"\") at the terminal pass", got)
	}
	if emitComputeFactForBead(context.Background(), sink, store, refreshed, "fake", "demo", now, nil, true) {
		t.Fatal("second emit on same interval must no-op (marker set)")
	}
	if len(sink.facts) != 1 {
		t.Fatalf("no new fact expected, got %d", len(sink.facts))
	}
}

// TestEmitComputeFactForBeadMultiInterval proves the create -> sleep -> wake ->
// sleep path bills two distinct compute facts. A reused session bead gets a
// fresh awake_started_at epoch on the second wake, so the interval-1 emit marker
// (keyed on the first epoch) does not suppress interval 2.
func TestEmitComputeFactForBeadMultiInterval(t *testing.T) {
	store := beads.NewMemStore()
	t1 := time.Date(2026, 6, 15, 10, 0, 0, 0, time.UTC)
	s1 := t1.Add(60 * time.Second)
	b, err := store.Create(beads.Bead{
		Title: "session",
		Metadata: map[string]string{
			"state":            "asleep",
			"session_name":     "pool-1",
			"awake_started_at": t1.Format(time.RFC3339Nano),
			"slept_at":         s1.Format(time.RFC3339Nano),
			"molecule_id":      "run-A",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	sink := &captureSink{}

	if !emitComputeFactForBead(context.Background(), sink, store, b, "fake", "demo", s1.Add(time.Second), nil, true) {
		t.Fatal("interval 1 should emit")
	}

	// Second awake interval: the controller stamps a fresh epoch on wake.
	t2 := t1.Add(2 * time.Hour)
	s2 := t2.Add(30 * time.Second)
	if err := store.SetMetadata(b.ID, "awake_started_at", t2.Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	if err := store.SetMetadata(b.ID, "slept_at", s2.Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	refreshed, err := store.Get(b.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !emitComputeFactForBead(context.Background(), sink, store, refreshed, "fake", "demo", s2.Add(time.Second), nil, true) {
		t.Fatal("interval 2 should emit a second compute fact")
	}
	if len(sink.facts) != 2 {
		t.Fatalf("want 2 compute facts across two awake intervals, got %d", len(sink.facts))
	}
	if sink.facts[0].IdempotencyKey == sink.facts[1].IdempotencyKey {
		t.Fatal("two intervals must have distinct idempotency keys")
	}
	if sink.facts[0].WallSeconds != 60 || sink.facts[1].WallSeconds != 30 {
		t.Fatalf("interval wall seconds wrong: %v, %v", sink.facts[0].WallSeconds, sink.facts[1].WallSeconds)
	}
}

func TestEmitComputeFactForBeadSinkErrorIsLogged(t *testing.T) {
	store := beads.NewMemStore()
	start := time.Date(2026, 6, 15, 10, 0, 0, 0, time.UTC)
	b, err := store.Create(beads.Bead{Title: "s", Metadata: map[string]string{
		"state":            "asleep",
		"awake_started_at": start.Format(time.RFC3339Nano),
	}})
	if err != nil {
		t.Fatal(err)
	}
	var logged []string
	logf := func(format string, args ...any) { logged = append(logged, fmt.Sprintf(format, args...)) }
	sink := &erroringSink{}

	if emitComputeFactForBead(context.Background(), sink, store, b, "fake", "demo", start.Add(time.Minute), logf, true) {
		t.Fatal("a failing sink must not report success")
	}
	if len(logged) == 0 {
		t.Fatal("sink failure must be surfaced via logf, not dropped silently")
	}
	// Marker must stay unset so a later tick retries.
	refreshed, err := store.Get(b.ID)
	if err != nil {
		t.Fatal(err)
	}
	if refreshed.Metadata[usageComputeEmittedAtKey] != "" {
		t.Fatal("marker must not be set when the sink write failed (so the fact retries)")
	}
}

func TestEmitComputeFactForBeadNoOps(t *testing.T) {
	store := beads.NewMemStore()
	ctx := context.Background()
	now := time.Now().UTC()
	sink := &captureSink{}

	// No awake_started_at → nothing to bill.
	b1, _ := store.Create(beads.Bead{Title: "s1", Metadata: map[string]string{"state": "asleep"}})
	if emitComputeFactForBead(ctx, sink, store, b1, "fake", "demo", now, nil, true) {
		t.Fatal("no awake_started_at must no-op")
	}
	// Discard sink → no-op even with a valid interval.
	b2, _ := store.Create(beads.Bead{Title: "s2", Metadata: map[string]string{"state": "asleep", "awake_started_at": now.Format(time.RFC3339)}})
	if emitComputeFactForBead(ctx, usage.Discard, store, b2, "fake", "demo", now, nil, true) {
		t.Fatal("discard sink must no-op")
	}
	if len(sink.facts) != 0 {
		t.Fatalf("expected no facts, got %d", len(sink.facts))
	}
}

// TestEmitComputeFactForBeadHungSinkReturnsPromptly is the reconcile-path half
// of the hung-exec-sink regression: a compute fact whose sink write hangs must
// not stall the reconcile tick, and must leave the emit marker unset so the
// fact retries on a later tick.
func TestEmitComputeFactForBeadHungSinkReturnsPromptly(t *testing.T) {
	script := filepath.Join(t.TempDir(), "hang.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nsleep 30\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	store := beads.NewMemStore()
	start := time.Date(2026, 6, 15, 10, 0, 0, 0, time.UTC)
	b, err := store.Create(beads.Bead{Title: "s", Metadata: map[string]string{
		"state":            "asleep",
		"awake_started_at": start.Format(time.RFC3339Nano),
	}})
	if err != nil {
		t.Fatal(err)
	}
	sink := usage.NewExecSinkWithTimeout(script, 100*time.Millisecond)

	done := make(chan bool, 1)
	began := time.Now()
	go func() {
		done <- emitComputeFactForBead(context.Background(), sink, store, b, "fake", "demo", start.Add(time.Minute), nil, true)
	}()
	select {
	case ok := <-done:
		if elapsed := time.Since(began); elapsed > 5*time.Second {
			t.Fatalf("reconcile compute path blocked on a hung sink: took %s", elapsed)
		}
		if ok {
			t.Fatal("a timed-out sink write must report failure, not success")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("reconcile compute path did not return under a hung sink")
	}
	refreshed, err := store.Get(b.ID)
	if err != nil {
		t.Fatal(err)
	}
	if refreshed.Metadata[usageComputeEmittedAtKey] != "" {
		t.Fatal("marker must stay unset when the sink timed out (so the fact retries)")
	}
}

// writeCodexRolloutForSweep fabricates a codex rollout transcript
// (rollout-<localtime>-<sessionID>.jsonl) under root/YYYY/MM/DD reachable by the
// window-free keyed discovery: a session_meta line whose cwd is workDir, a
// turn_context supplying the model, and one event_msg token_count per element of
// tokenCounts ({total, lastInput, lastOutput}). Returns the rollout path.
func writeCodexRolloutForSweep(t *testing.T, root, workDir, sessionID string, tokenCounts [][3]int) {
	t.Helper()
	dayDir := filepath.Join(root, "2026", "06", "15")
	if err := os.MkdirAll(dayDir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dayDir, "rollout-2026-06-15T10-00-00-"+sessionID+".jsonl")
	lines := []string{
		fmt.Sprintf(`{"timestamp":"2026-06-15T10:00:00.000Z","type":"session_meta","payload":{"id":%q,"cwd":%q}}`, sessionID, workDir),
		`{"timestamp":"2026-06-15T10:00:01.000Z","type":"turn_context","payload":{"model":"gpt-5-codex"}}`,
	}
	for i, tc := range tokenCounts {
		lines = append(lines, fmt.Sprintf(
			`{"timestamp":"2026-06-15T10:00:%02dZ","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"total_tokens":%d},"last_token_usage":{"input_tokens":%d,"cached_input_tokens":0,"output_tokens":%d}}}}`,
			i+2, tc[0], tc[1], tc[2]))
	}
	body := ""
	for _, l := range lines {
		body += l + "\n"
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func kindCount(facts []usage.Fact, kind usage.Kind) int {
	n := 0
	for _, f := range facts {
		if f.Kind == kind {
			n++
		}
	}
	return n
}

// TestEmitDueComputeFactsAlsoSweepsModelUsage is the CORE regression for the
// token-starvation bug: the controller reconcile tick emits per-interval compute
// facts but never any model facts for pool-routed, hook-self-driven codex agents,
// because the only model-fact emitter is coupled to prompt-op finish. The tick
// must, beside the compute fact, sweep the terminal session's transcript for the
// trailing invocations no prompt op recorded and emit one model fact per
// invocation. Before the sweep existed only the compute fact appeared.
func TestEmitDueComputeFactsAlsoSweepsModelUsage(t *testing.T) {
	cityPath := t.TempDir()
	workDir := t.TempDir()
	codexRoot := t.TempDir()
	sinkPath := filepath.Join(cityPath, ".gc", "usage.jsonl")

	// Codex names rollouts by the session_key uuid suffix; the keyed no-window
	// discovery matches on exactly that.
	sessionKey := "019e3e8e-3591-7532-a1ef-8b9e882bea2f"
	writeCodexRolloutForSweep(t, codexRoot, workDir, sessionKey, [][3]int{
		{150, 100, 50},  // total=150, last input=100, output=50
		{450, 200, 100}, // total=450, last input=200, output=100
	})

	store := beads.NewMemStore()
	start := time.Date(2026, 6, 15, 10, 0, 0, 0, time.UTC)
	slept := start.Add(90 * time.Second)
	b, err := store.Create(beads.Bead{
		Type:   session.BeadType,
		Status: "open",
		Title:  "codex session",
		Labels: []string{session.LabelSession},
		Metadata: map[string]string{
			"state":               "asleep",
			"session_name":        "codex-1",
			"awake_started_at":    start.Format(time.RFC3339),
			"slept_at":            slept.Format(time.RFC3339),
			"session_key":         sessionKey,
			"work_dir":            workDir,
			"provider":            "mc-codex-wrap", // wrapped manifold name
			"builtin_ancestor":    "codex",         // canonical ladder resolves this to codex
			"molecule_id":         "run-Z",
			"gc.active_work_bead": "run-Z.step-1",
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	cfg := &config.City{Daemon: config.DaemonConfig{ObservePaths: []string{codexRoot}}}
	cs := &controllerState{
		cityBeadStore: store,
		usageSink:     usage.NewLocalSink(sinkPath),
		cityName:      "demo",
		cityPath:      cityPath,
	}
	cr := &CityRuntime{cs: cs, cfg: cfg, sp: runtime.NewFake(), cityName: "demo", cityPath: cityPath, stderr: io.Discard}

	info := session.Info{ID: b.ID, MetadataState: "asleep", AwakeStartedAt: start.Format(time.RFC3339)}
	cr.emitDueComputeFacts(context.Background(), []session.Info{info})

	facts, warnings, err := usage.ReadFacts(sinkPath)
	if err != nil {
		t.Fatalf("ReadFacts: %v", err)
	}
	if len(warnings) != 0 {
		t.Fatalf("unexpected sink warnings: %v", warnings)
	}
	if got := kindCount(facts, usage.KindCompute); got != 1 {
		t.Fatalf("compute facts = %d, want 1", got)
	}
	if got := kindCount(facts, usage.KindModel); got != 2 {
		t.Fatalf("model facts = %d, want 2 (the tick must sweep the two trailing invocations); got facts: %+v", got, facts)
	}

	// Every fact — compute and both model — must carry the SAME RunID so gc costs
	// groups them under one run.
	for _, f := range facts {
		if f.RunID != "run-Z" {
			t.Fatalf("fact RunID = %q, want run-Z (shared across kinds): %+v", f.RunID, f)
		}
	}
	// Token deltas: the two model facts carry the per-invocation last-usage.
	seen := map[string]bool{}
	for _, f := range facts {
		if f.Kind != usage.KindModel {
			continue
		}
		seen[fmt.Sprintf("%d/%d", f.InputTokens, f.OutputTokens)] = true
		if f.StepID != "run-Z.step-1" {
			t.Fatalf("model fact StepID = %q, want run-Z.step-1 (the interval's active work bead)", f.StepID)
		}
		if f.Provider != "codex" {
			t.Fatalf("model fact Provider = %q, want codex (wrapped name resolved via builtin_ancestor)", f.Provider)
		}
	}
	if !seen["100/50"] || !seen["200/100"] {
		t.Fatalf("model facts missing expected token deltas; saw %v", seen)
	}

	// The invocation-usage cursor advanced to the newest invocation identity.
	refreshed, err := store.Get(b.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got := refreshed.Metadata[session.MetadataKeyInvocationUsageCursor]; got != "total:450" {
		t.Fatalf("invocation_usage_cursor = %q, want total:450 (advanced past the swept batch)", got)
	}

	// A second tick must add no new facts: the cursor blocks the model re-record
	// and the emit marker blocks the compute re-emit; ReadFacts also dedups any
	// replay by IdempotencyKey.
	cr.emitDueComputeFacts(context.Background(), []session.Info{info})
	facts2, _, err := usage.ReadFacts(sinkPath)
	if err != nil {
		t.Fatalf("ReadFacts (second tick): %v", err)
	}
	if kindCount(facts2, usage.KindCompute) != 1 || kindCount(facts2, usage.KindModel) != 2 {
		t.Fatalf("second tick changed fact counts: compute=%d model=%d, want 1/2",
			kindCount(facts2, usage.KindCompute), kindCount(facts2, usage.KindModel))
	}
}

// TestEmitDueComputeFactsRetriesUnsettledModelSweep pins P2-2: a transient
// model-sweep miss (here the codex rollout has not been flushed to disk yet at
// interval end) must NOT permanently lose the interval's model usage. Because the
// sweep is gated by its own marker (distinct from the compute marker), the
// interval stays a candidate and the sweep retries on the next tick once the
// transcript appears — recovering the model facts without duplicating the compute
// fact.
func TestEmitDueComputeFactsRetriesUnsettledModelSweep(t *testing.T) {
	cityPath := t.TempDir()
	workDir := t.TempDir()
	codexRoot := t.TempDir()
	sinkPath := filepath.Join(cityPath, ".gc", "usage.jsonl")
	sessionKey := "019e3e8e-3591-7532-a1ef-8b9e882bea2f"

	store := beads.NewMemStore()
	start := time.Date(2026, 6, 15, 10, 0, 0, 0, time.UTC)
	slept := start.Add(90 * time.Second)
	b, err := store.Create(beads.Bead{
		Type:   session.BeadType,
		Status: "open",
		Title:  "codex session",
		Labels: []string{session.LabelSession},
		Metadata: map[string]string{
			"state":               "asleep",
			"session_name":        "codex-1",
			"awake_started_at":    start.Format(time.RFC3339),
			"slept_at":            slept.Format(time.RFC3339),
			"session_key":         sessionKey,
			"work_dir":            workDir,
			"provider":            "codex",
			"builtin_ancestor":    "codex",
			"molecule_id":         "run-Z",
			"gc.active_work_bead": "run-Z.step-1",
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	cfg := &config.City{Daemon: config.DaemonConfig{ObservePaths: []string{codexRoot}}}
	cs := &controllerState{cityBeadStore: store, usageSink: usage.NewLocalSink(sinkPath), cityName: "demo", cityPath: cityPath}
	cr := &CityRuntime{cs: cs, cfg: cfg, sp: runtime.NewFake(), cityName: "demo", cityPath: cityPath, stderr: io.Discard}
	info := session.Info{ID: b.ID, MetadataState: "asleep", AwakeStartedAt: start.Format(time.RFC3339)}

	// Tick 1: the rollout is not on disk yet → the sweep misses (transient). The
	// compute fact still records, but neither the compute marker nor the sweep
	// marker is stamped, so the interval stays open for retry.
	cr.emitDueComputeFacts(context.Background(), []session.Info{info})
	facts1, _, err := usage.ReadFacts(sinkPath)
	if err != nil {
		t.Fatalf("ReadFacts (tick 1): %v", err)
	}
	if kindCount(facts1, usage.KindCompute) != 1 {
		t.Fatalf("tick 1 compute facts = %d, want 1 (compute is never delayed by a pending sweep)", kindCount(facts1, usage.KindCompute))
	}
	if kindCount(facts1, usage.KindModel) != 0 {
		t.Fatalf("tick 1 model facts = %d, want 0 (rollout not flushed yet)", kindCount(facts1, usage.KindModel))
	}
	afterTick1, err := store.Get(b.ID)
	if err != nil {
		t.Fatal(err)
	}
	if afterTick1.Metadata[usageComputeEmittedAtKey] != "" {
		t.Fatal("tick 1 must leave usage_compute_emitted_at unset so the interval stays a candidate for the sweep retry")
	}
	if afterTick1.Metadata[usageModelSweptAtKey] != "" {
		t.Fatal("tick 1 must leave the sweep marker unset (the sweep did not settle)")
	}

	// The transcript is flushed to disk between ticks.
	writeCodexRolloutForSweep(t, codexRoot, workDir, sessionKey, [][3]int{
		{150, 100, 50},
		{450, 200, 100},
	})

	// Tick 2: the interval is still a candidate → the sweep retries, discovers the
	// rollout, and recovers the model facts. No duplicate compute fact.
	cr.emitDueComputeFacts(context.Background(), []session.Info{info})
	facts2, _, err := usage.ReadFacts(sinkPath)
	if err != nil {
		t.Fatalf("ReadFacts (tick 2): %v", err)
	}
	if got := kindCount(facts2, usage.KindCompute); got != 1 {
		t.Fatalf("tick 2 compute facts = %d, want 1 (the re-recorded compute fact dedups by IdempotencyKey)", got)
	}
	if got := kindCount(facts2, usage.KindModel); got != 2 {
		t.Fatalf("tick 2 model facts = %d, want 2 (recovered on retry): %+v", got, facts2)
	}
	afterTick2, err := store.Get(b.ID)
	if err != nil {
		t.Fatal(err)
	}
	awake := start.Format(time.RFC3339)
	if afterTick2.Metadata[usageComputeEmittedAtKey] != awake {
		t.Fatalf("tick 2 must commit the interval (usage_compute_emitted_at=%q), got %q", awake, afterTick2.Metadata[usageComputeEmittedAtKey])
	}
	if afterTick2.Metadata[usageModelSweptAtKey] != awake {
		t.Fatalf("tick 2 must stamp the sweep marker (%q), got %q", awake, afterTick2.Metadata[usageModelSweptAtKey])
	}

	// Tick 3: both markers set → no re-Get work, no new facts.
	info.UsageComputeEmittedAt = awake // reflects the committed interval on the snapshot
	cr.emitDueComputeFacts(context.Background(), []session.Info{info})
	facts3, _, err := usage.ReadFacts(sinkPath)
	if err != nil {
		t.Fatalf("ReadFacts (tick 3): %v", err)
	}
	if kindCount(facts3, usage.KindCompute) != 1 || kindCount(facts3, usage.KindModel) != 2 {
		t.Fatalf("tick 3 changed counts: compute=%d model=%d, want 1/2", kindCount(facts3, usage.KindCompute), kindCount(facts3, usage.KindModel))
	}
}

func TestIsComputeTerminalState(t *testing.T) {
	// Every non-running endpoint the open-bead scan can observe.
	for _, s := range []string{"asleep", "drained", "archived", "suspended", "quarantined"} {
		if !isComputeTerminalState(s) {
			t.Errorf("%q should be terminal", s)
		}
	}
	// Running states, transient states, and closed (which leaves the open set the
	// scan reads) are not emitted by the scan.
	for _, s := range []string{"active", "awake", "creating", "draining", "closed", ""} {
		if isComputeTerminalState(s) {
			t.Errorf("%q should not be terminal", s)
		}
	}
}

// seedSplitCityInfraStore makes cachedCityInfraStore(cityPath, _) return infra for
// the duration of the test, simulating a split city whose session beads live in the
// INFRA store (the deployed fork shape). The process-global cache is cleaned up
// afterward so the fixture never leaks into another test's cityPath.
func seedSplitCityInfraStore(t *testing.T, cityPath string, infra beads.Store) {
	t.Helper()
	key := filepath.Clean(cityPath)
	cityInfraStoreCache.Store(key, infra)
	t.Cleanup(func() { cityInfraStoreCache.Delete(key) })
}

// codexRolloutPathForSweep returns the on-disk path writeCodexRolloutForSweep writes
// for a given root/sessionID, so a test can seed the discovery memo with it.
func codexRolloutPathForSweep(root, sessionID string) string {
	return filepath.Join(root, "2026", "06", "15", "rollout-2026-06-15T10-00-00-"+sessionID+".jsonl")
}

// TestEmitDueComputeFactsSweepsLiveWispSessionInInfraStore is THE producer-path
// regression the single-store version silently missed: on the deployed split-store
// fork the token producers are EPHEMERAL wisp-tier sessions whose beads live in the
// INFRA store, and the main-tier OpenInfos snapshot (default TierMode) drops every
// ephemeral bead. A live wisp session must therefore be reached through the
// split-city all-tier (TierBoth) infra-store enumeration, NOT the snapshot — and
// swept incrementally so its model calls are counted while it is still awake.
func TestEmitDueComputeFactsSweepsLiveWispSessionInInfraStore(t *testing.T) {
	cityPath := t.TempDir()
	workDir := t.TempDir()
	codexRoot := t.TempDir()
	sinkPath := filepath.Join(cityPath, ".gc", "usage.jsonl")

	sessionKey := "019e3e8e-3591-7532-a1ef-8b9e882bea2f"
	writeCodexRolloutForSweep(t, codexRoot, workDir, sessionKey, [][3]int{
		{150, 100, 50},
		{450, 200, 100},
	})

	// Session beads live in the INFRA store on a split city; the work store does NOT
	// hold them.
	infra := beads.NewMemStore()
	workStore := beads.NewMemStore()
	seedSplitCityInfraStore(t, cityPath, infra)

	start := time.Date(2026, 6, 15, 10, 0, 0, 0, time.UTC)
	b, err := infra.Create(beads.Bead{
		Type:      session.BeadType,
		Status:    "open",
		Ephemeral: true, // wisp-tier: DROPPED by the default-TierMode main snapshot
		Title:     "live codex wisp session",
		Labels:    []string{session.LabelSession},
		Metadata: map[string]string{
			"state":            "active", // AWAKE, still producing model calls
			"session_name":     "codex-wisp-1",
			"awake_started_at": start.Format(time.RFC3339),
			"session_key":      sessionKey,
			"work_dir":         workDir,
			"provider":         "mc-codex-wrap",
			"builtin_ancestor": "codex",
			"molecule_id":      "run-W",
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	cfg := &config.City{Daemon: config.DaemonConfig{ObservePaths: []string{codexRoot}}}
	cs := &controllerState{cityBeadStore: workStore, usageSink: usage.NewLocalSink(sinkPath), cityName: "demo", cityPath: cityPath}
	cr := &CityRuntime{cs: cs, cfg: cfg, sp: runtime.NewFake(), cityName: "demo", cityPath: cityPath, stderr: io.Discard}

	// Empty snapshot: the ephemeral wisp is NOT in the main-tier OpenInfos, so it can
	// only be reached via the split-city all-tier infra-store enumeration.
	cr.emitDueComputeFacts(context.Background(), nil)

	facts, warnings, err := usage.ReadFacts(sinkPath)
	if err != nil {
		t.Fatalf("ReadFacts: %v", err)
	}
	if len(warnings) != 0 {
		t.Fatalf("unexpected sink warnings: %v", warnings)
	}
	if got := kindCount(facts, usage.KindModel); got != 2 {
		t.Fatalf("model facts = %d, want 2 (a LIVE wisp session in the INFRA store must be swept via the all-tier enumeration); facts: %+v", got, facts)
	}
	if got := kindCount(facts, usage.KindCompute); got != 0 {
		t.Fatalf("compute facts = %d, want 0 (the wisp is still awake — no interval to bill)", got)
	}
	for _, f := range facts {
		if f.Kind == usage.KindModel && f.Provider != "codex" {
			t.Fatalf("model fact Provider = %q, want codex (wrapped name resolved via builtin_ancestor)", f.Provider)
		}
	}
	// Cursor advanced in the INFRA store (where the bead lives), and NO per-interval
	// sweep marker (the interval is still open).
	refreshed, err := infra.Get(b.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got := refreshed.Metadata[session.MetadataKeyInvocationUsageCursor]; got != "total:450" {
		t.Fatalf("cursor = %q, want total:450 (advanced in the infra store)", got)
	}
	if got := refreshed.Metadata[usageModelSweptAtKey]; got != "" {
		t.Fatalf("usage_model_swept_at = %q, want empty (a live sweep leaves the interval open)", got)
	}
	// The work store never held the session bead — proving the live arm read the
	// infra store, not the work store.
	if _, err := workStore.Get(b.ID); err == nil {
		t.Fatal("work store must not contain the wisp session bead (it lives in the infra store)")
	}
}

// liveCodexSessionRuntime builds a single-store CityRuntime + a live (awake) keyed
// codex session bead for the incremental-sweep tests, returning the runtime, the
// bead, the store, and the sink path. No infra store is seeded, so the session flows
// through the main-tier snapshot arm.
//
//nolint:unparam // sessionKey is a fixed UUIDv7 across tests by design; it documents the keyed-session contract the helper builds.
func liveCodexSessionRuntime(t *testing.T, codexRoot, workDir, sessionKey, state string) (*CityRuntime, beads.Bead, beads.Store, string) {
	t.Helper()
	cityPath := t.TempDir()
	sinkPath := filepath.Join(cityPath, ".gc", "usage.jsonl")
	store := beads.NewMemStore()
	start := time.Date(2026, 6, 15, 10, 0, 0, 0, time.UTC)
	b, err := store.Create(beads.Bead{
		Type:   session.BeadType,
		Status: "open",
		Title:  "live codex session",
		Labels: []string{session.LabelSession},
		Metadata: map[string]string{
			"state":            state,
			"session_name":     "codex-live-1",
			"awake_started_at": start.Format(time.RFC3339),
			"session_key":      sessionKey,
			"work_dir":         workDir,
			"provider":         "codex",
			"builtin_ancestor": "codex",
			"molecule_id":      "run-L",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	cfg := &config.City{Daemon: config.DaemonConfig{ObservePaths: []string{codexRoot}}}
	cs := &controllerState{cityBeadStore: store, usageSink: usage.NewLocalSink(sinkPath), cityName: "demo", cityPath: cityPath}
	cr := &CityRuntime{cs: cs, cfg: cfg, sp: runtime.NewFake(), cityName: "demo", cityPath: cityPath, stderr: io.Discard}
	return cr, b, store, sinkPath
}

// TestEmitDueComputeFactsSweepsLiveSessionModelUsage pins the incremental live sweep
// through the main-tier snapshot arm: a still-AWAKE codex session mints model facts
// for the content beyond its cursor, is idempotent when nothing changed, and mints
// only the delta when more is appended — never a compute fact or a per-interval
// marker while awake.
func TestEmitDueComputeFactsSweepsLiveSessionModelUsage(t *testing.T) {
	workDir := t.TempDir()
	codexRoot := t.TempDir()
	sessionKey := "019e3e8e-3591-7532-a1ef-8b9e882bea2f"
	writeCodexRolloutForSweep(t, codexRoot, workDir, sessionKey, [][3]int{
		{150, 100, 50},
		{450, 200, 100},
	})
	cr, b, store, sinkPath := liveCodexSessionRuntime(t, codexRoot, workDir, sessionKey, "active")
	info := session.Info{ID: b.ID, MetadataState: "active", AwakeStartedAt: b.Metadata["awake_started_at"]}

	// Tick 1: the two trailing invocations of a still-awake session are swept.
	cr.emitDueComputeFacts(context.Background(), []session.Info{info})
	facts, _, err := usage.ReadFacts(sinkPath)
	if err != nil {
		t.Fatalf("ReadFacts: %v", err)
	}
	if got := kindCount(facts, usage.KindModel); got != 2 {
		t.Fatalf("tick 1 model facts = %d, want 2 (a live session must be swept, not only at retirement); facts: %+v", got, facts)
	}
	if got := kindCount(facts, usage.KindCompute); got != 0 {
		t.Fatalf("tick 1 compute facts = %d, want 0 (still awake)", got)
	}
	refreshed, err := store.Get(b.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got := refreshed.Metadata[session.MetadataKeyInvocationUsageCursor]; got != "total:450" {
		t.Fatalf("cursor = %q, want total:450", got)
	}
	if got := refreshed.Metadata[usageModelSweptAtKey]; got != "" {
		t.Fatalf("usage_model_swept_at = %q, want empty (a live sweep leaves the interval open)", got)
	}

	// Tick 2: nothing new → idempotent.
	cr.emitDueComputeFacts(context.Background(), []session.Info{info})
	facts2, _, err := usage.ReadFacts(sinkPath)
	if err != nil {
		t.Fatalf("ReadFacts (tick 2): %v", err)
	}
	if got := kindCount(facts2, usage.KindModel); got != 2 {
		t.Fatalf("tick 2 model facts = %d, want 2 (idempotent)", got)
	}

	// Tick 3: one more invocation appended → only the delta is minted.
	writeCodexRolloutForSweep(t, codexRoot, workDir, sessionKey, [][3]int{
		{150, 100, 50},
		{450, 200, 100},
		{750, 300, 150},
	})
	cr.emitDueComputeFacts(context.Background(), []session.Info{info})
	facts3, _, err := usage.ReadFacts(sinkPath)
	if err != nil {
		t.Fatalf("ReadFacts (tick 3): %v", err)
	}
	if got := kindCount(facts3, usage.KindModel); got != 3 {
		t.Fatalf("tick 3 model facts = %d, want 3 (only the delta added); facts: %+v", got, facts3)
	}
	refreshed3, err := store.Get(b.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got := refreshed3.Metadata[session.MetadataKeyInvocationUsageCursor]; got != "total:750" {
		t.Fatalf("cursor = %q, want total:750", got)
	}
}

// TestEmitDueComputeFactsLiveSweepSkipsPartialTrailingLine pins partial-read safety:
// a live codex rollout being appended to can end mid-line (invalid JSON). The
// complete records count and the cursor advances only through them; the torn
// trailing line is not counted and the cursor does not advance onto it — so once the
// writer finishes the line, the next tick counts it exactly once.
func TestEmitDueComputeFactsLiveSweepSkipsPartialTrailingLine(t *testing.T) {
	workDir := t.TempDir()
	codexRoot := t.TempDir()
	sessionKey := "019e3e8e-3591-7532-a1ef-8b9e882bea2f"

	dayDir := filepath.Join(codexRoot, "2026", "06", "15")
	if err := os.MkdirAll(dayDir, 0o755); err != nil {
		t.Fatal(err)
	}
	rolloutPath := filepath.Join(dayDir, "rollout-2026-06-15T10-00-00-"+sessionKey+".jsonl")
	meta := fmt.Sprintf(`{"timestamp":"2026-06-15T10:00:00.000Z","type":"session_meta","payload":{"id":%q,"cwd":%q}}`, sessionKey, workDir)
	turn := `{"timestamp":"2026-06-15T10:00:01.000Z","type":"turn_context","payload":{"model":"gpt-5-codex"}}`
	complete := `{"timestamp":"2026-06-15T10:00:02Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"total_tokens":150},"last_token_usage":{"input_tokens":100,"cached_input_tokens":0,"output_tokens":50}}}}`
	partial := `{"timestamp":"2026-06-15T10:00:03Z","type":"event_msg","payload":{"type":"token_count","info":{"total_to`
	writeBody := func(body string) {
		if err := os.WriteFile(rolloutPath, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	writeBody(meta + "\n" + turn + "\n" + complete + "\n" + partial)

	cr, b, store, sinkPath := liveCodexSessionRuntime(t, codexRoot, workDir, sessionKey, "awake")
	info := session.Info{ID: b.ID, MetadataState: "awake", AwakeStartedAt: b.Metadata["awake_started_at"]}

	// Tick 1: the complete record counts; the torn trailing line does not.
	cr.emitDueComputeFacts(context.Background(), []session.Info{info})
	facts, _, err := usage.ReadFacts(sinkPath)
	if err != nil {
		t.Fatalf("ReadFacts: %v", err)
	}
	if got := kindCount(facts, usage.KindModel); got != 1 {
		t.Fatalf("tick 1 model facts = %d, want 1 (the half-written trailing line must not be counted); facts: %+v", got, facts)
	}
	refreshed, err := store.Get(b.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got := refreshed.Metadata[session.MetadataKeyInvocationUsageCursor]; got != "total:150" {
		t.Fatalf("cursor = %q, want total:150 (must not advance onto the partial line)", got)
	}

	// The writer finishes the line: the previously-partial record is now complete.
	writeBody(meta + "\n" + turn + "\n" + complete + "\n" +
		`{"timestamp":"2026-06-15T10:00:03Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"total_tokens":450},"last_token_usage":{"input_tokens":200,"cached_input_tokens":0,"output_tokens":100}}}}` + "\n")

	// Tick 2: the now-complete record is counted exactly once.
	cr.emitDueComputeFacts(context.Background(), []session.Info{info})
	facts2, _, err := usage.ReadFacts(sinkPath)
	if err != nil {
		t.Fatalf("ReadFacts (tick 2): %v", err)
	}
	if got := kindCount(facts2, usage.KindModel); got != 2 {
		t.Fatalf("tick 2 model facts = %d, want 2 (the completed line counted, exactly once); facts: %+v", got, facts2)
	}
}

// TestEmitDueComputeFactsLiveSweepCatchUp pins first-sweep catch-up: a long-lived
// session accumulates many unswept invocations before the live sweep ever runs (its
// cursor is empty). The first live sweep recovers the WHOLE bounded tail at once (not
// just the newest, the prompt-op seam's conservative pick) and then goes incremental;
// the batch is bounded by the extractor's 64KB tail window.
func TestEmitDueComputeFactsLiveSweepCatchUp(t *testing.T) {
	workDir := t.TempDir()
	codexRoot := t.TempDir()
	sessionKey := "019e3e8e-3591-7532-a1ef-8b9e882bea2f"
	writeCodexRolloutForSweep(t, codexRoot, workDir, sessionKey, [][3]int{
		{150, 100, 50},
		{450, 200, 100},
		{750, 300, 150},
		{1000, 250, 250},
	})
	cr, b, store, sinkPath := liveCodexSessionRuntime(t, codexRoot, workDir, sessionKey, "active")
	info := session.Info{ID: b.ID, MetadataState: "active", AwakeStartedAt: b.Metadata["awake_started_at"]}

	cr.emitDueComputeFacts(context.Background(), []session.Info{info})
	facts, _, err := usage.ReadFacts(sinkPath)
	if err != nil {
		t.Fatalf("ReadFacts: %v", err)
	}
	if got := kindCount(facts, usage.KindModel); got != 4 {
		t.Fatalf("first live sweep model facts = %d, want 4 (catch-up recovers all unswept window invocations, not just the newest); facts: %+v", got, facts)
	}
	refreshed, err := store.Get(b.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got := refreshed.Metadata[session.MetadataKeyInvocationUsageCursor]; got != "total:1000" {
		t.Fatalf("cursor = %q, want total:1000", got)
	}
	// Second sweep with no new content mints nothing — each caught-up invocation
	// counted exactly once.
	cr.emitDueComputeFacts(context.Background(), []session.Info{info})
	facts2, _, err := usage.ReadFacts(sinkPath)
	if err != nil {
		t.Fatalf("ReadFacts (second): %v", err)
	}
	if got := kindCount(facts2, usage.KindModel); got != 4 {
		t.Fatalf("second sweep model facts = %d, want 4 (counted exactly once)", got)
	}
}

// TestEmitDueComputeFactsLiveThenRetirementNoDoubleCountWisp is the residual-risk
// regression the two arms share a cursor for, on the SPLIT-CITY producer path: a
// wisp is swept live for a while, then retires and the terminal arm sweeps its final
// invocations. Because both arms advance the SAME infra-store invocation-usage cursor
// (and stamp the same ModelIdempotencyKey), the retirement sweep records ONLY the
// invocations after the last live sweep — never re-counting the live-swept ones —
// while adding the interval's single compute fact.
func TestEmitDueComputeFactsLiveThenRetirementNoDoubleCountWisp(t *testing.T) {
	cityPath := t.TempDir()
	workDir := t.TempDir()
	codexRoot := t.TempDir()
	sinkPath := filepath.Join(cityPath, ".gc", "usage.jsonl")
	sessionKey := "019e3e8e-3591-7532-a1ef-8b9e882bea2f"

	start := time.Date(2026, 6, 15, 10, 0, 0, 0, time.UTC)
	writeCodexRolloutForSweep(t, codexRoot, workDir, sessionKey, [][3]int{
		{150, 100, 50},
		{450, 200, 100},
	})

	infra := beads.NewMemStore()
	seedSplitCityInfraStore(t, cityPath, infra)
	b, err := infra.Create(beads.Bead{
		Type:      session.BeadType,
		Status:    "open",
		Ephemeral: true,
		Title:     "codex wisp session",
		Labels:    []string{session.LabelSession},
		Metadata: map[string]string{
			"state":            "active",
			"session_name":     "codex-wisp-1",
			"awake_started_at": start.Format(time.RFC3339),
			"session_key":      sessionKey,
			"work_dir":         workDir,
			"provider":         "codex",
			"builtin_ancestor": "codex",
			"molecule_id":      "run-W",
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	cfg := &config.City{Daemon: config.DaemonConfig{ObservePaths: []string{codexRoot}}}
	cs := &controllerState{cityBeadStore: beads.NewMemStore(), usageSink: usage.NewLocalSink(sinkPath), cityName: "demo", cityPath: cityPath}
	cr := &CityRuntime{cs: cs, cfg: cfg, sp: runtime.NewFake(), cityName: "demo", cityPath: cityPath, stderr: io.Discard}

	// Tick 1 (awake): the live arm sweeps the two invocations so far (via the all-tier
	// infra enumeration; empty snapshot).
	cr.emitDueComputeFacts(context.Background(), nil)
	facts1, _, err := usage.ReadFacts(sinkPath)
	if err != nil {
		t.Fatalf("ReadFacts (tick 1): %v", err)
	}
	if got := kindCount(facts1, usage.KindModel); got != 2 {
		t.Fatalf("tick 1 model facts = %d, want 2 (live wisp sweep)", got)
	}
	if got := kindCount(facts1, usage.KindCompute); got != 0 {
		t.Fatalf("tick 1 compute facts = %d, want 0 (still awake)", got)
	}

	// One final call, then retirement.
	writeCodexRolloutForSweep(t, codexRoot, workDir, sessionKey, [][3]int{
		{150, 100, 50},
		{450, 200, 100},
		{750, 300, 150},
	})
	slept := start.Add(90 * time.Second)
	if err := infra.SetMetadata(b.ID, "state", "asleep"); err != nil {
		t.Fatal(err)
	}
	if err := infra.SetMetadata(b.ID, "slept_at", slept.Format(time.RFC3339)); err != nil {
		t.Fatal(err)
	}

	// Tick 2 (retired): the terminal arm sweeps ONLY the trailing invocation after the
	// shared cursor and bills the single compute fact.
	cr.emitDueComputeFacts(context.Background(), nil)
	facts2, _, err := usage.ReadFacts(sinkPath)
	if err != nil {
		t.Fatalf("ReadFacts (tick 2): %v", err)
	}
	if got := kindCount(facts2, usage.KindModel); got != 3 {
		t.Fatalf("model facts after retirement = %d, want 3 (retirement adds only the 1 trailing invocation — NO double-count of the 2 live-swept ones); facts: %+v", got, facts2)
	}
	if got := kindCount(facts2, usage.KindCompute); got != 1 {
		t.Fatalf("compute facts after retirement = %d, want 1", got)
	}
	refreshed, err := infra.Get(b.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got := refreshed.Metadata[session.MetadataKeyInvocationUsageCursor]; got != "total:750" {
		t.Fatalf("cursor = %q, want total:750 (advanced across both arms)", got)
	}
	awake := start.Format(time.RFC3339)
	if got := refreshed.Metadata[usageModelSweptAtKey]; got != awake {
		t.Fatalf("usage_model_swept_at = %q, want %q (retirement settles the interval)", got, awake)
	}
	if got := refreshed.Metadata[usageComputeEmittedAtKey]; got != awake {
		t.Fatalf("usage_compute_emitted_at = %q, want %q (retirement commits the interval)", got, awake)
	}
}

// TestLiveModelSweepMemoizesTranscriptPath proves the first live sweep resolves the
// transcript path and caches it on the CityRuntime, so subsequent ticks skip the
// O(awake-days) codex rollout discovery.
func TestLiveModelSweepMemoizesTranscriptPath(t *testing.T) {
	workDir := t.TempDir()
	codexRoot := t.TempDir()
	sessionKey := "019e3e8e-3591-7532-a1ef-8b9e882bea2f"
	writeCodexRolloutForSweep(t, codexRoot, workDir, sessionKey, [][3]int{{150, 100, 50}})
	cr, b, _, _ := liveCodexSessionRuntime(t, codexRoot, workDir, sessionKey, "active")
	info := session.Info{ID: b.ID, MetadataState: "active", AwakeStartedAt: b.Metadata["awake_started_at"]}

	if _, cached := cr.cachedLiveTranscriptPath(b.ID); cached {
		t.Fatal("precondition: memo must start empty")
	}
	cr.emitDueComputeFacts(context.Background(), []session.Info{info})
	got, cached := cr.cachedLiveTranscriptPath(b.ID)
	if !cached {
		t.Fatal("first live sweep must cache the resolved transcript path")
	}
	if want := codexRolloutPathForSweep(codexRoot, sessionKey); got != want {
		t.Fatalf("memoized path = %q, want %q", got, want)
	}
}

// TestLiveModelSweepUsesMemoizedPathSkippingDiscovery proves the memo SHORT-CIRCUITS
// discovery: with the memo pre-seeded to a specific rollout, the sweep reads THAT
// path and never runs discovery — which would otherwise resolve the session's own
// keyed rollout (with different tokens). Distinct token values make the choice
// observable.
func TestLiveModelSweepUsesMemoizedPathSkippingDiscovery(t *testing.T) {
	workDir := t.TempDir()
	codexRoot := t.TempDir()
	realKey := "019e3e8e-3591-7532-a1ef-8b9e882bea2f"
	// The session's own keyed rollout that discovery WOULD find (two invocations).
	writeCodexRolloutForSweep(t, codexRoot, workDir, realKey, [][3]int{
		{150, 100, 50},
		{450, 200, 100},
	})
	// A DIFFERENT rollout with distinct tokens the memo points at directly.
	seededKey := "019e4444-0000-7000-8000-00000000000b"
	writeCodexRolloutForSweep(t, codexRoot, workDir, seededKey, [][3]int{{999, 777, 333}})
	seededPath := codexRolloutPathForSweep(codexRoot, seededKey)

	cr, b, store, sinkPath := liveCodexSessionRuntime(t, codexRoot, workDir, realKey, "active")
	cr.rememberLiveTranscriptPath(b.ID, seededPath) // pre-seed the memo
	info := session.Info{ID: b.ID, MetadataState: "active", AwakeStartedAt: b.Metadata["awake_started_at"]}

	cr.emitDueComputeFacts(context.Background(), []session.Info{info})
	facts, _, err := usage.ReadFacts(sinkPath)
	if err != nil {
		t.Fatalf("ReadFacts: %v", err)
	}
	// The memoized (seeded) rollout has exactly ONE invocation (777/333); the
	// discoverable one would have TWO (100/50, 200/100). Seeing exactly the seeded
	// one proves discovery was skipped.
	if got := kindCount(facts, usage.KindModel); got != 1 {
		t.Fatalf("model facts = %d, want 1 (the single-invocation memoized rollout, not the 2-invocation discoverable one — discovery must be skipped); facts: %+v", got, facts)
	}
	if facts[0].InputTokens != 777 || facts[0].OutputTokens != 333 {
		t.Fatalf("fact tokens = %d/%d, want 777/333 (from the memoized path, not discovery)", facts[0].InputTokens, facts[0].OutputTokens)
	}
	refreshed, err := store.Get(b.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got := refreshed.Metadata[session.MetadataKeyInvocationUsageCursor]; got != "total:999" {
		t.Fatalf("cursor = %q, want total:999 (from the memoized rollout)", got)
	}
}

// TestIsLiveModelSweepState pins the live-sweep state gate and its disjointness from
// isComputeTerminalState: only a running session (active/awake) is swept live; no
// state is BOTH live and compute-terminal.
func TestIsLiveModelSweepState(t *testing.T) {
	for _, s := range []string{"active", "awake"} {
		if !isLiveModelSweepState(s) {
			t.Errorf("%q should be a live model-sweep state", s)
		}
	}
	for _, s := range []string{
		"asleep", "drained", "archived", "suspended", "quarantined",
		"creating", "start-pending", "draining", "failed-create", "closed", "",
	} {
		if isLiveModelSweepState(s) {
			t.Errorf("%q should not be a live model-sweep state", s)
		}
	}
	for _, s := range []string{
		"active", "awake", "asleep", "drained", "archived",
		"suspended", "quarantined", "creating", "draining", "closed", "",
	} {
		if isLiveModelSweepState(s) && isComputeTerminalState(s) {
			t.Errorf("%q is both live and compute-terminal; the gates must be disjoint", s)
		}
	}
}

// TestLiveModelSweepCandidate pins the live-sweep pre-Get gate: a live session with a
// confirmed awake interval qualifies (and keeps qualifying every tick — the cursor,
// not a marker, makes the sweep idempotent); a terminal or anchor-less session does
// not.
func TestLiveModelSweepCandidate(t *testing.T) {
	info := func(state, awake string) session.Info {
		return session.Info{MetadataState: state, AwakeStartedAt: awake}
	}
	const t1 = "2026-01-02T00:30:00Z"
	cases := []struct {
		name string
		info session.Info
		want bool
	}{
		{"active-with-awake", info("active", t1), true},
		{"awake-with-awake", info("awake", t1), true},
		{"active-no-awake-anchor", info("active", ""), false},
		{"terminal-asleep", info("asleep", t1), false},
		{"transitional-creating", info("creating", t1), false},
		{"draining", info("draining", t1), false},
		{"closed", info("closed", t1), false},
	}
	for _, tc := range cases {
		if got := liveModelSweepCandidate(tc.info); got != tc.want {
			t.Errorf("%s: liveModelSweepCandidate = %v, want %v", tc.name, got, tc.want)
		}
	}
}
