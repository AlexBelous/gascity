//go:build integration && ((linux && !android) || (darwin && !ios))

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/config"
	"github.com/gastownhall/gascity/internal/nudgeparity"
	"github.com/gastownhall/gascity/internal/nudgequeue"
	"github.com/gastownhall/gascity/internal/runtime"
	"github.com/gastownhall/gascity/internal/testutil"
)

const nudgeParityCLIProcessHelperArg = "nudge-parity-cli-helper"

func TestLegacyImmediateNudgeCrossProcessCLIControllerSocketParity(t *testing.T) {
	cityPath := t.TempDir()
	store := nudgequeue.CommandStoreBinding{
		StoreUUID:    "22222222-2222-4222-8222-222222222222",
		RestoreEpoch: 4,
	}
	index, err := nudgequeue.BuildCommandIndex(nudgequeue.CommandIndexSnapshot{
		Store:             store,
		Revision:          29,
		SequenceHighWater: 0,
	})
	if err != nil {
		t.Fatalf("BuildCommandIndex: %v", err)
	}
	source := &parityEffectSourceSpy{}
	results := make(chan nudgeparity.Result, 2)
	cr := &CityRuntime{
		cityPath:             cityPath,
		nudgeEffectOwnership: nudgeEffectOwnershipLegacy,
		nudgeKeyReader:       &nudgeKeyReadShadow{source: source, index: index, store: store},
		nudgeParityEnabled:   true,
		nudgeParityResultSink: func(_ context.Context, result nudgeparity.Result) error {
			results <- result
			return nil
		},
		stderr: io.Discard,
	}
	cr.publishNudgeParityConfigRevision("config-cross-process")
	stop, err := cr.startNudgeParityShadowBeforeReadiness(t.Context())
	if err != nil {
		t.Fatalf("startNudgeParityShadowBeforeReadiness: %v", err)
	}
	t.Cleanup(func() {
		if err := stop(); err != nil {
			t.Errorf("stop nudge parity shadow: %v", err)
		}
	})

	binding := &recordingCityRuntimeNudgeBinding{}
	authority := &cityRuntimeSessionNudgeAuthority{cr: cr, binding: binding}
	token := "cross-process-controller-token"
	lis, err := startControllerSocket(
		cityPath,
		func() {},
		nil,
		nil,
		nil,
		make(chan convergenceRequest, 1),
		make(chan struct{}, 1),
		make(chan struct{}, 1),
		withControllerNudgeAdmission(token, authority),
	)
	if err != nil {
		t.Fatalf("startControllerSocket: %v", err)
	}
	t.Cleanup(func() {
		_ = lis.Close()
		_ = os.Remove(controllerSocketPath(cityPath))
	})

	fixturePath := writeNudgeParityCLIProcessFixture(t, nudgeParityCLIProcessFixture{
		CityPath: cityPath,
		Token:    token,
	})
	process, err := runTestProcessRaw(
		t,
		os.Args[0],
		cityPath,
		sanitizedBaseEnv(),
		"-test.run=^TestNudgeParityCLIProcessHelper$",
		"-test.v",
		"--",
		nudgeParityCLIProcessHelperArg,
		fixturePath,
	)
	if err != nil {
		t.Fatalf("CLI helper: %v\nstdout:\n%s\nstderr:\n%s", err, process.stdout, process.stderr)
	}
	summary, err := decodeNudgeParityCLIHelperSummary(process.stdout)
	if err != nil {
		t.Fatalf("decode CLI helper summary: %v\nstdout:\n%s\nstderr:\n%s", err, process.stdout, process.stderr)
	}
	if summary.RequestID == "" || summary.ProviderEffects != 1 || summary.ExitCode != 0 {
		t.Fatalf("CLI helper summary = %#v, want stable request and one CLI-owned provider effect", summary)
	}

	var result nudgeparity.Result
	select {
	case result = <-results:
	case <-time.After(testutil.GoroutineRaceTimeout):
		t.Fatal("timed out waiting for cross-process parity comparison")
	}
	if result.OperationID != summary.RequestID || result.Expected.OperationID != summary.RequestID || result.Actual.OperationID != summary.RequestID {
		t.Fatalf("cross-process operation IDs = result=%q expected=%q actual=%q, want CLI request %q",
			result.OperationID, result.Expected.OperationID, result.Actual.OperationID, summary.RequestID)
	}
	if result.Actual.Plan != (nudgeparity.Plan{Decision: nudgeparity.DecisionExecute, Action: nudgeparity.ActionNudge}) {
		t.Fatalf("cross-process keyed plan = %#v, want validated immediate nudge", result.Actual.Plan)
	}
	if got := binding.admissionCount(); got != 0 {
		t.Fatalf("cross-process shadow durable admissions = %d, want 0", got)
	}
	if snapshotReads, exactReads, claims, completions, retries := source.counts(); snapshotReads != 0 || exactReads != 0 || claims != 0 || completions != 0 || retries != 0 {
		t.Fatalf("parity source effects = snapshot=%d get=%d claim=%d complete=%d retry=%d, want all zero",
			snapshotReads, exactReads, claims, completions, retries)
	}
	if err := stop(); err != nil {
		t.Fatalf("stop nudge parity shadow: %v", err)
	}
	select {
	case extra := <-results:
		t.Fatalf("unexpected second cross-process comparison: %#v", extra)
	default:
	}
}

func TestNudgeParityCLIProcessHelper(t *testing.T) {
	fixturePath, ok := nudgeParityCLIProcessFixtureArg(os.Args)
	if !ok {
		t.Skip("cross-process helper only")
	}
	fixture := readNudgeParityCLIProcessFixture(t, fixturePath)
	provider := runtime.NewFake()
	if err := provider.Start(t.Context(), "runtime-cross-process", runtime.Config{}); err != nil {
		t.Fatalf("start fake CLI provider: %v", err)
	}
	target := nudgeTarget{
		cityPath:         fixture.CityPath,
		agent:            config.Agent{Name: "worker"},
		resolved:         &config.ResolvedProvider{Name: "fake"},
		sessionID:        "session-cross-process",
		sessionName:      "runtime-cross-process",
		intentGeneration: 31,
		launchIdentity:   "launch-cross-process",
	}
	effectTarget := nudgeTarget{
		agent:       target.agent,
		resolved:    target.resolved,
		sessionName: target.sessionName,
	}
	var requestID string
	var stdout, stderr bytes.Buffer
	dialer := &net.Dialer{Timeout: testutil.ExecRaceTimeout}
	code := deliverSessionNudgeWithIngressAndLegacy(
		target,
		"cross-process parity",
		nudgeDeliveryImmediate,
		false,
		&stdout,
		&stderr,
		func(ctx context.Context, path string, request nudgequeue.NudgeIngressRequest) nudgeBridgeResult {
			requestID = request.RequestID
			return admitNudgeThroughLocalControllerWithDeps(
				ctx,
				path,
				request,
				func(string) (string, error) { return fixture.Token, nil },
				dialer.DialContext,
			)
		},
		func() int {
			return deliverSessionNudgeWithProvider(effectTarget, provider, nudgeDeliveryImmediate, &stdout, &stderr)
		},
	)
	effects := 0
	for _, call := range provider.Calls {
		if call.Method == "NudgeNow" {
			effects++
		}
	}
	summary := nudgeParityCLIHelperSummary{RequestID: requestID, ProviderEffects: effects, ExitCode: code}
	wire, err := json.Marshal(summary)
	if err != nil {
		t.Fatalf("marshal helper summary: %v", err)
	}
	fmt.Printf("NUDGE_PARITY_HELPER:%s\n", wire)
	if code != 0 || effects != 1 || requestID == "" {
		t.Fatalf("helper result code=%d effects=%d request=%q stderr=%q", code, effects, requestID, stderr.String())
	}
}

type nudgeParityCLIProcessFixture struct {
	CityPath string `json:"city_path"`
	Token    string `json:"token"`
}

func writeNudgeParityCLIProcessFixture(t *testing.T, fixture nudgeParityCLIProcessFixture) string {
	t.Helper()
	wire, err := json.Marshal(fixture)
	if err != nil {
		t.Fatalf("marshal CLI process fixture: %v", err)
	}
	path := filepath.Join(t.TempDir(), "nudge-parity-helper.json")
	if err := os.WriteFile(path, wire, 0o600); err != nil {
		t.Fatalf("write CLI process fixture: %v", err)
	}
	return path
}

func nudgeParityCLIProcessFixtureArg(args []string) (string, bool) {
	for index, arg := range args {
		if arg != "--" {
			continue
		}
		tail := args[index+1:]
		if len(tail) == 2 && tail[0] == nudgeParityCLIProcessHelperArg {
			return tail[1], true
		}
	}
	return "", false
}

func readNudgeParityCLIProcessFixture(t *testing.T, path string) nudgeParityCLIProcessFixture {
	t.Helper()
	wire, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read CLI process fixture: %v", err)
	}
	var fixture nudgeParityCLIProcessFixture
	if err := json.Unmarshal(wire, &fixture); err != nil {
		t.Fatalf("decode CLI process fixture: %v", err)
	}
	if strings.TrimSpace(fixture.CityPath) == "" || strings.TrimSpace(fixture.Token) == "" {
		t.Fatal("CLI process fixture is incomplete")
	}
	return fixture
}

type nudgeParityCLIHelperSummary struct {
	RequestID       string `json:"request_id"`
	ProviderEffects int    `json:"provider_effects"`
	ExitCode        int    `json:"exit_code"`
}

func decodeNudgeParityCLIHelperSummary(output []byte) (nudgeParityCLIHelperSummary, error) {
	const prefix = "NUDGE_PARITY_HELPER:"
	for _, line := range strings.Split(string(output), "\n") {
		if !strings.HasPrefix(line, prefix) {
			continue
		}
		var summary nudgeParityCLIHelperSummary
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, prefix)), &summary); err != nil {
			return nudgeParityCLIHelperSummary{}, err
		}
		return summary, nil
	}
	return nudgeParityCLIHelperSummary{}, errors.New("helper summary is absent")
}

func (s *parityEffectSourceSpy) counts() (snapshotReads, exactReads, claims, completions, retries int32) {
	return s.snapshotReads.Load(), s.exactReads.Load(), s.claims.Load(), s.completions.Load(), s.retries.Load()
}
