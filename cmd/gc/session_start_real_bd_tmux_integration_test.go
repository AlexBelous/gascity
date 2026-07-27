//go:build integration

package main

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/config"
	"github.com/gastownhall/gascity/internal/events"
	"github.com/gastownhall/gascity/internal/rollout"
	"github.com/gastownhall/gascity/internal/runtime"
	sessionpkg "github.com/gastownhall/gascity/internal/session"
	"github.com/gastownhall/gascity/test/tmuxtest"
)

type exactSessionStartDurableSample struct {
	SessionID            string `json:"session_id"`
	SchemaStatus         string `json:"schema_status"`
	AdmissionToStartNS   int64  `json:"admission_to_start_ns"`
	ProviderCallNS       int64  `json:"provider_call_ns"`
	AdmissionToPersistNS int64  `json:"admission_to_persist_ns"`
	ProviderStartEffects int    `json:"provider_start_effects"`
	PersistedState       string `json:"persisted_state"`
}

// TestExactSessionStartNativeV59RealBDTmuxJourney proves that one exact socket
// hint can drive a v59 durable pending-create session through the native store,
// keyed controller, and real isolated tmux provider without a fleet tick.
func TestExactSessionStartNativeV59RealBDTmuxJourney(t *testing.T) {
	bdPath := strings.TrimSpace(os.Getenv("GC_TEST_BD_BIN"))
	if bdPath == "" {
		t.Skip("GC_TEST_BD_BIN is not set to a real bd binary")
	}
	bdPath, err := filepath.Abs(bdPath)
	if err != nil {
		t.Fatalf("resolve GC_TEST_BD_BIN: %v", err)
	}
	if info, err := os.Stat(bdPath); err != nil || info.IsDir() || info.Mode()&0o111 == 0 {
		t.Fatalf("GC_TEST_BD_BIN %q is not an executable file: info=%v err=%v", bdPath, info, err)
	}
	shimDir := t.TempDir()
	if err := os.Symlink(bdPath, filepath.Join(shimDir, "bd")); err != nil {
		t.Fatalf("install bd PATH shim: %v", err)
	}
	t.Setenv("PATH", shimDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	if tracePath := strings.TrimSpace(os.Getenv("GC_TEST_BD_TRACE_JSON")); tracePath != "" {
		t.Setenv("GC_BD_TRACE_JSON", tracePath)
	}
	t.Logf("BD_BINARY %s", bdPath)
	guard := tmuxtest.NewGuard(t)
	cityPath := t.TempDir()
	schemaStatus := initializeExactStartBDStore(t, cityPath)
	if !strings.Contains(schemaStatus, "v59") {
		t.Fatalf("bd schema status = %q, want v59", schemaStatus)
	}

	sessionConfig := config.SessionConfig{
		Socket:             guard.SocketName(),
		SetupTimeout:       "3s",
		NudgeReadyTimeout:  "2s",
		NudgeRetryInterval: "50ms",
		NudgeLockTimeout:   "2s",
		StartupTimeout:     "10s",
	}
	baseProvider, err := newSessionProviderForCityByName(
		nil,
		"",
		sessionConfig,
		guard.CityName(),
		cityPath,
	)
	if err != nil {
		t.Fatalf("construct isolated tmux provider: %v", err)
	}
	provider := &sessionLifecycleShadowJourneyProvider{Provider: baseProvider}
	sessionName := guard.SessionName("worker")
	t.Cleanup(func() {
		if provider.IsRunning(sessionName) {
			if err := provider.Stop(sessionName); err != nil {
				t.Errorf("cleanup isolated tmux session: %v", err)
			}
		}
	})

	cfg := &config.City{
		Workspace: config.Workspace{Name: guard.CityName()},
		Agents: []config.Agent{{
			Name:         "worker",
			StartCommand: "sleep 600",
			// Avoid unrelated tmux process-family discovery so this sample
			// measures keyed admission rather than host-wide ps/pgrep latency.
			Env: map[string]string{"GC_PROVIDER": "codex"},
		}},
		Session: sessionConfig,
	}
	backingStore := beads.NewBdStoreWithPrefix(cityPath, beads.ExecCommandRunner(), "gct")
	nativeStore, err := beads.OpenNativeDoltStoreAt(t.Context(), cityPath, nil)
	if err != nil {
		t.Fatalf("open production native session store over v59 data: %v", err)
	}
	t.Cleanup(func() {
		if err := nativeStore.CloseStore(); err != nil {
			t.Errorf("close production native session store: %v", err)
		}
	})
	store := beads.NewCachingStore(nativeStore, nil)
	if err := store.PrimeActive(); err != nil {
		t.Fatalf("prime production-shaped session cache: %v", err)
	}
	cs := coherentSessionStartControllerStateForTest(cfg, provider, store, rollout.Auto)
	cs.cityName = guard.CityName()
	cs.cityPath = cityPath
	cr := &CityRuntime{
		cityPath: cityPath,
		cityName: guard.CityName(),
		cfg:      cfg,
		sp:       provider,
		cs:       cs,
		rec:      events.Discard,
		stdout:   io.Discard,
		stderr:   io.Discard,
		sessionStartOptions: []startExecutionOption{
			withStartStabilityWaiter(immediateStartStabilityWaiter),
			withSessionStaleKeyDetectionWaiter(immediateSessionStaleKeyDetectionWaiter),
		},
	}
	t.Cleanup(cr.stopSessionStartController)
	terminalResults := make(chan sessionStartReconcileResult, 8)
	originalControllerConstructor := newCitySessionStartController
	newCitySessionStartController = func(opts sessionStartControllerOptions) (*sessionStartController, error) {
		originalObserver := opts.Observer
		opts.Observer = func(result sessionStartReconcileResult) {
			if originalObserver != nil {
				originalObserver(result)
			}
			terminalResults <- result
		}
		return originalControllerConstructor(opts)
	}
	t.Cleanup(func() { newCitySessionStartController = originalControllerConstructor })
	if err := cr.ensureSessionStartController(t.Context(), newSessionBeadSnapshotFromInfos(nil)); err != nil {
		t.Fatalf("start keyed session controller: %v", err)
	}

	now := time.Now().UTC()
	info, err := sessionFrontDoor(backingStore).CreateSessionInfo(sessionpkg.CreateSpec{
		Title:     "worker",
		AgentName: "worker",
		Metadata: map[string]string{
			"session_name":              sessionName,
			"agent_name":                "worker",
			"template":                  "worker",
			"generation":                "1",
			"instance_token":            "integration-token",
			"live_hash":                 runtime.LiveFingerprint(runtime.Config{Command: "sleep 600"}),
			"state":                     string(sessionpkg.StateCreating),
			"pending_create_claim":      "true",
			"pending_create_started_at": now.Format(time.RFC3339Nano),
		},
	})
	if err != nil {
		t.Fatalf("create durable session: %v", err)
	}

	admittedAt := time.Now().UTC()
	if reply := cr.admitSessionStartSocketKey(info.ID); reply != sessionStartSocketReplyOK {
		t.Fatalf("exact session-start admission = %q, want %q", reply, sessionStartSocketReplyOK)
	}

	waitCtx, cancelWait := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancelWait()
	for {
		select {
		case result := <-terminalResults:
			switch result.Outcome {
			case sessionStartReconcileRetrying, sessionStartReconcileSuperseded:
				continue
			case sessionStartReconcileSucceeded:
				goto reconciled
			default:
				t.Fatalf("exact start terminal result = %+v, want succeeded", result)
			}
		case <-waitCtx.Done():
			t.Fatalf("exact start did not reach a terminal result: %v", waitCtx.Err())
		}
	}

reconciled:
	persisted, err := sessionFrontDoor(backingStore).Get(info.ID)
	if err != nil {
		t.Fatalf("read exact-start result: %v", err)
	}
	if persisted.MetadataState != string(sessionpkg.StateActive) {
		t.Fatalf("persisted state = %q, want active", persisted.MetadataState)
	}
	persistedAt := time.Now().UTC()

	startCalls := provider.snapshotStartCalls()
	if len(startCalls) != 1 {
		t.Fatalf("provider Start calls = %d, want exactly 1: %#v", len(startCalls), startCalls)
	}
	startCall := startCalls[0]
	if startCall.Name != sessionName || startCall.Err != nil {
		t.Fatalf("provider Start = %#v, want successful %q start", startCall, sessionName)
	}
	if !provider.IsRunning(sessionName) || !guard.HasSession(sessionName) {
		t.Fatalf("exact start returned without live isolated tmux session %q", sessionName)
	}
	if persisted.PendingCreateClaim {
		t.Fatal("pending_create_claim remained set after exact start")
	}

	sample := exactSessionStartDurableSample{
		SessionID:            info.ID,
		SchemaStatus:         schemaStatus,
		AdmissionToStartNS:   startCall.EnteredAt.Sub(admittedAt).Nanoseconds(),
		ProviderCallNS:       startCall.CompletedAt.Sub(startCall.EnteredAt).Nanoseconds(),
		AdmissionToPersistNS: persistedAt.Sub(admittedAt).Nanoseconds(),
		ProviderStartEffects: len(startCalls),
		PersistedState:       persisted.MetadataState,
	}
	wire, err := json.Marshal(sample)
	if err != nil {
		t.Fatalf("marshal exact-start sample: %v", err)
	}
	t.Logf("EXACT_START_DURABLE_SAMPLE %s", wire)
}

func initializeExactStartBDStore(t *testing.T, cityPath string) string {
	t.Helper()
	run := func(args ...string) string {
		t.Helper()
		ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
		defer cancel()
		runner := beads.ExecCommandRunnerWithEnvContext(ctx, map[string]string{
			"BEADS_DIR": filepath.Join(cityPath, ".beads"),
		})
		out, err := runner(cityPath, "bd", args...)
		if err != nil {
			t.Fatalf("bd %s: %v\n%s", strings.Join(args, " "), err, out)
		}
		return strings.TrimSpace(string(out))
	}

	run("init", "--non-interactive", "--skip-hooks", "--skip-agents", "--prefix", "gct")
	run("config", "set", "types.custom", "molecule,convoy,message,event,gate,merge-request,agent,role,rig,session,spec,convergence,step")
	status := run("migrate", "schema", "--json")
	if status == "" {
		status = run("migrate", "schema")
	}
	if status == "" {
		status = "schema status unavailable"
	}
	return status
}
