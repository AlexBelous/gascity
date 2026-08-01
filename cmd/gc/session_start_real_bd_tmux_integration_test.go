//go:build integration

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/config"
	"github.com/gastownhall/gascity/internal/fsys"
	"github.com/gastownhall/gascity/internal/rollout"
	"github.com/gastownhall/gascity/internal/runtime"
	runtimetmux "github.com/gastownhall/gascity/internal/runtime/tmux"
	sessionpkg "github.com/gastownhall/gascity/internal/session"
	"github.com/gastownhall/gascity/internal/testutil"
	"github.com/gastownhall/gascity/test/tmuxtest"
)

type exactSessionStartStopDurableSample struct {
	Version                        string `json:"version"`
	SessionID                      string `json:"session_id"`
	SchemaStatus                   string `json:"schema_status"`
	StartAdmissionToFinalizationNS int64  `json:"start_admission_to_finalization_ns"`
	StopAdmissionToFinalizationNS  int64  `json:"stop_admission_to_finalization_ns"`
	WakeCommandToFinalizationNS    int64  `json:"wake_command_to_finalization_ns"`
	StartPersistedState            string `json:"start_persisted_state"`
	StopPersistedState             string `json:"stop_persisted_state"`
	WakePersistedState             string `json:"wake_persisted_state"`
}

func testExactSessionStartSocketLiveSessionRecordsDetachedStatusShadow(t *testing.T) {
	guard := tmuxtest.NewGuard(t)
	cityPath := t.TempDir()
	sessionName := guard.SessionName("worker")
	sessionConfig := config.SessionConfig{
		Socket:         guard.SocketName(),
		SetupTimeout:   "3s",
		StartupTimeout: "10s",
	}
	baseProvider, err := newSessionProviderForCityByName(nil, "", sessionConfig, guard.CityName(), cityPath)
	if err != nil {
		t.Fatalf("construct isolated tmux provider: %v", err)
	}
	provider := &sessionLifecycleShadowJourneyProvider{Provider: baseProvider}
	if err := provider.Start(t.Context(), sessionName, runtime.Config{
		Command: "sleep 600",
		WorkDir: cityPath,
		Env:     map[string]string{"GC_PROVIDER": "codex"},
	}); err != nil {
		t.Fatalf("seed live isolated tmux session: %v", err)
	}
	beforeStarts := len(provider.snapshotStartCalls())
	if beforeStarts != 1 {
		t.Fatalf("seed provider Start calls = %d, want exactly one", beforeStarts)
	}
	beforeServerPID := guard.ServerPID()
	beforeSocket, err := os.Lstat(guard.SocketPath())
	if err != nil {
		t.Fatalf("stat isolated tmux socket before admission: %v", err)
	}
	beforeSessionIDs, err := runtimetmux.NewTmuxWithConfig(runtimetmux.Config{
		SocketName: guard.SocketName(),
	}).ListSessionIDs()
	if err != nil {
		t.Fatalf("list isolated tmux sessions before admission: %v", err)
	}
	if len(beforeSessionIDs) != 1 || strings.TrimSpace(beforeSessionIDs[sessionName]) == "" {
		t.Fatalf("isolated tmux sessions before admission = %v, want exactly live target %q with a non-empty ID", beforeSessionIDs, sessionName)
	}
	t.Logf("LIVE_SOCKET_NOOP before starts=%d server_pid=%d socket=%s sessions=%v", beforeStarts, beforeServerPID, guard.SocketPath(), beforeSessionIDs)

	cfg := &config.City{
		Workspace: config.Workspace{Name: guard.CityName()},
		Agents: []config.Agent{{
			Name:         "worker",
			StartCommand: "sleep 600",
			Env:          map[string]string{"GC_PROVIDER": "codex"},
		}},
		Session: sessionConfig,
	}
	env := newReconcilerTestEnv()
	bead := env.createSessionBead(sessionName, "worker")
	if err := env.store.SetMetadataBatch(bead.ID, map[string]string{
		"state":        string(sessionpkg.StateAwake),
		"wake_request": string(sessionpkg.WakeCauseExplicit),
	}); err != nil {
		t.Fatalf("configure exact-start-owned live row: %v", err)
	}
	trace := newSessionReconcilerTraceManager(cityPath, guard.CityName(), io.Discard)
	t.Cleanup(func() { _ = trace.Close() })
	status := make(chan exactSessionLifecycleStatusResult, 1)
	cs := coherentSessionStartControllerStateForTest(cfg, provider, env.store, rollout.Auto)
	cs.cityName = guard.CityName()
	cs.cityPath = cityPath
	cr := &CityRuntime{
		cityPath: cityPath,
		cityName: guard.CityName(),
		cs:       cs,
		trace:    trace,
		sessionStartOptions: []startExecutionOption{
			withExactSessionLifecycleStatusObserver(func(result exactSessionLifecycleStatusResult) {
				status <- result
			}),
		},
	}
	t.Cleanup(cr.stopSessionStartController)
	if err := cr.ensureSessionStartController(t.Context(), newSessionBeadSnapshotFromInfos(nil)); err != nil {
		t.Fatalf("start keyed session controller: %v", err)
	}

	beforeRow, err := env.store.Get(bead.ID)
	if err != nil {
		t.Fatalf("read live session row before socket admission: %v", err)
	}
	if beforeRow.Revision == 0 {
		t.Fatalf("live session row revision = %d, want nonzero", beforeRow.Revision)
	}
	if reply := cr.admitSessionStartSocketKey(bead.ID); reply != sessionStartSocketReplyOK {
		t.Fatalf("exact session-start admission = %q, want %q", reply, sessionStartSocketReplyOK)
	}

	var got exactSessionLifecycleStatusResult
	select {
	case got = <-status:
	case <-time.After(testutil.GoroutineRaceTimeout):
		t.Fatal("socket-admitted live session did not report exact status")
	}
	if got.Admission.Source != sessionStartAdmissionSocket || !got.RuntimeLive ||
		got.Disposition != exactSessionLifecycleStatusDispositionCandidate ||
		got.Reason != exactSessionLifecycleStatusReasonCandidate || got.Plan == nil ||
		got.Plan.Outcome != sessionLifecycleStatusNoop || got.Plan.Reason != sessionLifecycleStatusReasonConverged ||
		got.EffectApplied {
		t.Fatalf("exact status = %#v, want socket candidate/noop/converged with no effect", got)
	}

	afterRow, err := env.store.Get(bead.ID)
	if err != nil {
		t.Fatalf("read live session row after socket admission: %v", err)
	}
	if afterRow.Revision != beforeRow.Revision || !reflect.DeepEqual(afterRow, beforeRow) {
		t.Fatalf("durable row changed after no-op socket admission:\nbefore=%#v\nafter=%#v", beforeRow, afterRow)
	}
	if got := len(provider.snapshotStartCalls()); got != 1 {
		t.Fatalf("provider Start calls = %d, want absolute total 1", got)
	}
	afterServerPID := guard.ServerPID()
	if afterServerPID != beforeServerPID {
		t.Fatalf("isolated tmux server PID after admission = %d, want unchanged %d", afterServerPID, beforeServerPID)
	}
	afterSocket, err := os.Lstat(guard.SocketPath())
	if err != nil {
		t.Fatalf("stat isolated tmux socket after admission: %v", err)
	}
	if !os.SameFile(beforeSocket, afterSocket) {
		t.Fatalf("isolated tmux socket changed after no-op admission: before=%s after=%s", beforeSocket.Name(), afterSocket.Name())
	}
	afterSessionIDs, err := runtimetmux.NewTmuxWithConfig(runtimetmux.Config{
		SocketName: guard.SocketName(),
	}).ListSessionIDs()
	if err != nil {
		t.Fatalf("list isolated tmux sessions after admission: %v", err)
	}
	if !reflect.DeepEqual(afterSessionIDs, beforeSessionIDs) || !provider.IsRunning(sessionName) || !guard.HasSession(sessionName) {
		t.Fatalf("isolated tmux sessions/live state = %v/%t/%t, want unchanged sessions %v and live target",
			afterSessionIDs, provider.IsRunning(sessionName), guard.HasSession(sessionName), beforeSessionIDs)
	}
	t.Logf("LIVE_SOCKET_NOOP after starts=%d server_pid=%d socket_same=%t sessions=%v", len(provider.snapshotStartCalls()), afterServerPID, os.SameFile(beforeSocket, afterSocket), afterSessionIDs)

	records, err := ReadTraceRecords(traceCityRuntimeDir(cityPath), TraceFilter{})
	if err != nil {
		t.Fatalf("read detached socket shadow trace: %v", err)
	}
	var witnesses []SessionReconcilerTraceRecord
	for _, record := range records {
		if record.RecordType == TraceRecordOperation && record.SiteCode == TraceSiteLifecycleStatusShadow {
			witnesses = append(witnesses, record)
		}
	}
	if len(witnesses) != 1 {
		t.Fatalf("socket status-shadow witnesses = %#v, want exactly one", witnesses)
	}
	witness := witnesses[0]
	if witness.OutcomeCode != TraceOutcomeNoChange || witness.Fields["session_id"] != bead.ID ||
		witness.Fields["admission"] != string(sessionStartAdmissionSocket) ||
		witness.Fields["status_outcome"] != "noop" ||
		witness.Fields["status_reason"] != string(sessionLifecycleStatusReasonConverged) ||
		witness.Fields["effect_applied"] != false {
		t.Fatalf("socket status-shadow witness = %#v, want detached no-effect converged witness", witness)
	}
}

// TestExactSessionStartNativeV59RealBDTmuxJourney proves exact socket admission
// against an already-live no-op and a v59 durable start, status heal, and stop.
func TestExactSessionStartNativeV59RealBDTmuxJourney(t *testing.T) {
	t.Run("live_socket_noop", testExactSessionStartSocketLiveSessionRecordsDetachedStatusShadow)
	t.Run("native_v59_start_stop", testExactSessionStartNativeV59RealBDTmuxJourney)
}

func testExactSessionStartNativeV59RealBDTmuxJourney(t *testing.T) {
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
	t.Setenv("BEADS_DOLT_AUTO_START", "1")
	bdTracePath := strings.TrimSpace(os.Getenv("GC_TEST_BD_TRACE_JSON"))
	if bdTracePath == "" {
		bdTracePath = filepath.Join(t.TempDir(), "bd-trace.jsonl")
	}
	t.Setenv("GC_BD_TRACE_JSON", bdTracePath)
	type bdTraceRecord struct {
		Args    []string `json:"args"`
		Callers []string `json:"callers"`
	}
	readBDTrace := func() []bdTraceRecord {
		t.Helper()
		data, readErr := os.ReadFile(bdTracePath)
		if readErr != nil {
			t.Fatalf("read Bd trace %q: %v", bdTracePath, readErr)
		}
		lines := bytes.Split(bytes.TrimSpace(data), []byte{'\n'})
		records := make([]bdTraceRecord, 0, len(lines))
		for _, line := range lines {
			if len(line) == 0 {
				continue
			}
			var record bdTraceRecord
			if decodeErr := json.Unmarshal(line, &record); decodeErr != nil {
				t.Fatalf("decode isolated bd trace record %q: %v", line, decodeErr)
			}
			records = append(records, record)
		}
		return records
	}
	hasTraceArgument := func(args []string, want string) bool {
		for _, arg := range args {
			if arg == want {
				return true
			}
		}
		return false
	}
	hasTraceCaller := func(callers []string, want string) bool {
		for _, caller := range callers {
			if strings.Contains(caller, want) {
				return true
			}
		}
		return false
	}

	guard := tmuxtest.NewGuard(t)
	cityPath := t.TempDir()
	cleanupManagedDoltTestCity(t, cityPath)
	configPath := filepath.Join(t.TempDir(), "city.toml")
	cfg := &config.City{
		Workspace: config.Workspace{Name: guard.CityName()},
		Beads: config.BeadsConfig{
			Provider:          "bd",
			ConditionalWrites: "require",
		},
		Daemon: config.DaemonConfig{
			SessionReconciler: "auto",
			PatrolInterval:    "1m",
			TickDebounce:      "30s",
		},
		Session: config.SessionConfig{
			Socket:         guard.SocketName(),
			SetupTimeout:   "3s",
			StartupTimeout: "10s",
		},
		Agents: []config.Agent{{
			Name:         "worker",
			StartCommand: "sleep 600",
			Env:          map[string]string{"GC_PROVIDER": "codex"},
		}},
	}
	configData, err := cfg.Marshal()
	if err != nil {
		t.Fatalf("marshal exact start-stop city config: %v", err)
	}
	if err := os.WriteFile(configPath, configData, 0o644); err != nil {
		t.Fatalf("write exact start-stop city config: %v", err)
	}

	gcBinary := currentGCBinaryForTests(t)
	runtimeDir := t.TempDir()
	gcHome := t.TempDir()
	commandEnv := append([]string(nil), os.Environ()...)
	commandEnv = replaceEnvEntry(commandEnv, "PATH", shimDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	commandEnv = replaceEnvEntry(commandEnv, "GC_HOME", gcHome)
	commandEnv = replaceEnvEntry(commandEnv, "XDG_RUNTIME_DIR", runtimeDir)
	commandEnv = replaceEnvEntry(commandEnv, "GC_SESSION", "tmux")
	commandEnv = replaceEnvEntry(commandEnv, "GC_BEADS", "bd")
	commandEnv = replaceEnvEntry(commandEnv, "GC_BEADS_SCOPE_ROOT", cityPath)
	commandEnv = replaceEnvEntry(commandEnv, "GC_DOLT", "")
	commandEnv = replaceEnvEntry(commandEnv, "BEADS_DIR", filepath.Join(cityPath, ".beads"))
	commandEnv = replaceEnvEntry(commandEnv, "BEADS_DOLT_AUTO_START", "1")
	commandEnv = replaceEnvEntry(commandEnv, "GC_ALLOW_PROD_DOLT_PORT_IN_TESTS", "1")

	runGC := func(timeout time.Duration, args ...string) string {
		t.Helper()
		ctx, cancel := context.WithTimeout(t.Context(), timeout)
		defer cancel()
		cmd := newExactStartStopGCCommand(ctx, commandEnv, gcBinary, args...)
		out, runErr := cmd.CombinedOutput()
		if runErr != nil {
			t.Fatalf("gc %s: %v\n%s", strings.Join(args, " "), runErr, out)
		}
		return strings.TrimSpace(string(out))
	}
	runGC(30*time.Second,
		"init",
		"--skip-provider-readiness",
		"--no-start",
		"--name", guard.CityName(),
		"--file", configPath,
		cityPath,
	)
	loaded, err := loadCityConfig(cityPath, io.Discard)
	if err != nil {
		t.Fatalf("load initialized exact start-stop city config: %v", err)
	}
	if loaded.Session.Socket != guard.SocketName() {
		t.Fatalf("initialized session socket = %q, want %q", loaded.Session.Socket, guard.SocketName())
	}

	schemaStatus := exactStartBDCommand(t, cityPath, "migrate", "schema", "--json")
	if strings.TrimSpace(schemaStatus) == "" {
		schemaStatus = exactStartBDCommand(t, cityPath, "migrate", "schema")
	}
	if !strings.Contains(schemaStatus, "v59") {
		t.Fatalf("bd schema status = %q, want v59", schemaStatus)
	}

	controllerCtx, cancelController := context.WithCancel(t.Context())
	var controllerStdout, controllerStderr synchronizedBuffer
	controllerCmd := newExactStartStopGCCommand(
		controllerCtx,
		commandEnv,
		gcBinary,
		"start",
		"--foreground",
		"--no-strict",
		cityPath,
	)
	controllerCmd.Stdout = &controllerStdout
	controllerCmd.Stderr = &controllerStderr
	if err := controllerCmd.Start(); err != nil {
		t.Fatalf("start production controller: %v", err)
	}
	controllerDone := make(chan error, 1)
	go func() {
		controllerDone <- controllerCmd.Wait()
	}()
	controllerStopped := false
	controllerWaited := false
	t.Cleanup(func() {
		if !controllerStopped {
			stopOutput, stopErr := runExactStartStopGC(commandEnv, 30*time.Second, gcBinary, "stop", "--force", cityPath)
			if stopErr != nil {
				t.Errorf("stop production controller: %v\n%s", stopErr, stopOutput)
			}
		}
		cancelController()
		if !controllerWaited {
			select {
			case <-controllerDone:
			case <-time.After(testutil.ExecRaceTimeout):
				t.Errorf("production controller did not exit; stdout=%q stderr=%q", controllerStdout.String(), controllerStderr.String())
			}
		}
	})
	waitForControllerAvailable(t, cityPath)

	var (
		traceOutput string
		traceStatus traceStatusResultJSON
	)
	if err := waitExactStartStopState(t.Context(), 15*time.Second, func() (bool, error) {
		out, runErr := runExactStartStopGC(
			commandEnv,
			10*time.Second,
			gcBinary,
			"--city", cityPath,
			"trace", "status", "--json",
		)
		traceOutput = out
		if runErr != nil {
			return false, runErr
		}
		var status traceStatusResultJSON
		if decodeErr := json.Unmarshal([]byte(exactStartStopJSONPayload(out)), &status); decodeErr != nil {
			return false, decodeErr
		}
		traceStatus = status
		return status.SessionReconciler.Available &&
			status.SessionReconciler.ConfiguredMode == "auto" &&
			status.SessionReconciler.EffectiveOwner == "keyed", nil
	}); err != nil {
		t.Fatalf("production session reconciler did not become auto/keyed: %v; status=%+v output=%q controller stdout=%q stderr=%q",
			err, traceStatus.SessionReconciler, traceOutput, controllerStdout.String(), controllerStderr.String())
	}

	startAdmittedAt := time.Now().UTC()
	createdOutput := runGC(30*time.Second,
		"--city", cityPath,
		"session", "new", "worker",
		"--no-attach",
		"--json",
	)
	var created sessionNewJSON
	if err := json.Unmarshal([]byte(exactStartStopJSONPayload(createdOutput)), &created); err != nil {
		t.Fatalf("decode exact session creation: %v\n%s", err, createdOutput)
	}
	if created.SessionID == "" || created.SessionName == "" || !created.DeferredStart {
		t.Fatalf("exact session creation = %+v, want deferred ID and tmux name", created)
	}

	backingStore := beads.NewBdStoreWithPrefix(cityPath, beads.ExecCommandRunner(), "gct")
	var (
		started          sessionpkg.Info
		lastStartInfo    sessionpkg.Info
		startFinalizedAt time.Time
	)
	if err := waitExactStartStopState(t.Context(), 30*time.Second, func() (bool, error) {
		info, getErr := sessionFrontDoor(backingStore).Get(created.SessionID)
		if getErr != nil {
			return false, getErr
		}
		lastStartInfo = info
		view := sessionpkg.ProjectLifecycle(sessionpkg.LifecycleInputFromInfo(info))
		if view.BaseState != sessionpkg.BaseStateActive || info.PendingCreateClaim {
			return false, nil
		}
		started = info
		startFinalizedAt = time.Now().UTC()
		return true, nil
	}); err != nil {
		t.Fatalf("exact start did not converge: %v; current=%+v controller stdout=%q stderr=%q",
			err, lastStartInfo, controllerStdout.String(), controllerStderr.String())
	}
	tmuxClient := runtimetmux.NewTmuxWithConfig(runtimetmux.Config{SocketName: guard.SocketName()})
	liveToken, err := tmuxClient.GetEnvironment(created.SessionName, "GC_INSTANCE_TOKEN")
	if err != nil {
		t.Fatalf("read live isolated tmux instance token: %v; controller stdout=%q stderr=%q",
			err, controllerStdout.String(), controllerStderr.String())
	}
	if strings.TrimSpace(liveToken) == "" || liveToken != started.InstanceToken {
		t.Fatalf("live/durable instance tokens = %q/%q, want the same non-empty token", liveToken, started.InstanceToken)
	}
	sessionIDs, err := tmuxClient.ListSessionIDs()
	if err != nil {
		t.Fatalf("read exact-start tmux identity: %v", err)
	}
	startedTmuxID := strings.TrimSpace(sessionIDs[created.SessionName])
	if startedTmuxID == "" {
		t.Fatalf("exact-start tmux identity for %q is empty: %v", created.SessionName, sessionIDs)
	}
	startedTmuxServerPID := guard.ServerPID()
	if startedTmuxServerPID <= 0 {
		t.Fatalf("exact-start tmux server PID = %d, want positive", startedTmuxServerPID)
	}

	if err := sessionFrontDoor(backingStore).ApplyPatch(created.SessionID, sessionpkg.MetadataPatch{
		"wake_request": string(sessionpkg.WakeCauseExplicit),
	}); err != nil {
		t.Fatalf("stamp explicit wake marker on live session: %v", err)
	}
	preHeal, err := backingStore.Get(created.SessionID)
	if err != nil {
		t.Fatalf("read v59 pre-heal session: %v", err)
	}
	if preHeal.Revision == 0 || preHeal.Metadata["state"] != string(sessionpkg.StateActive) {
		t.Fatalf("v59 pre-heal revision/state = %d/%q, want nonzero/active", preHeal.Revision, preHeal.Metadata["state"])
	}
	healReply, err := sendControllerCommandWithReadTimeout(
		cityPath,
		sessionStartCommandPrefix+created.SessionID,
		10*time.Second,
	)
	if err != nil {
		t.Fatalf("submit exact status-heal key through controller socket: %v", err)
	}
	if got := strings.TrimSpace(string(healReply)); got != string(sessionStartSocketReplyOK) {
		t.Fatalf("exact status-heal socket reply = %q, want %q", got, sessionStartSocketReplyOK)
	}
	var (
		healedBead     beads.Bead
		lastHealedBead beads.Bead
	)
	if err := waitExactStartStopState(t.Context(), 30*time.Second, func() (bool, error) {
		current, getErr := backingStore.Get(created.SessionID)
		if getErr != nil {
			return false, getErr
		}
		lastHealedBead = current
		if current.Metadata["state"] != string(sessionpkg.StateAwake) {
			return false, nil
		}
		healedBead = current
		return true, nil
	}); err != nil {
		t.Fatalf("v59 status heal did not converge: %v; current=%+v controller stdout=%q stderr=%q",
			err, lastHealedBead, controllerStdout.String(), controllerStderr.String())
	}
	if healedBead.Revision == 0 || healedBead.Revision == preHeal.Revision ||
		healedBead.Metadata["wake_request"] != string(sessionpkg.WakeCauseExplicit) ||
		healedBead.Metadata["session_name"] != created.SessionName ||
		healedBead.Metadata["instance_token"] != started.InstanceToken ||
		healedBead.Metadata["pending_create_claim"] != "" {
		t.Fatalf("v59 healed row = revision %d metadata %#v, want a new revision with identity preserved",
			healedBead.Revision, healedBead.Metadata)
	}
	healedSession, err := sessionFrontDoor(backingStore).Get(created.SessionID)
	if err != nil {
		t.Fatalf("project v59 healed session: %v", err)
	}
	if healedSession.MetadataState != string(sessionpkg.StateAwake) {
		t.Fatalf("v59 healed session state = %q, want awake", healedSession.MetadataState)
	}
	sessionIDs, err = tmuxClient.ListSessionIDs()
	if err != nil || strings.TrimSpace(sessionIDs[created.SessionName]) != startedTmuxID {
		t.Fatalf("v59 status heal changed tmux identity: before=%q after=%q all=%v err=%v",
			startedTmuxID, strings.TrimSpace(sessionIDs[created.SessionName]), sessionIDs, err)
	}
	if err := sessionFrontDoor(backingStore).ApplyPatch(created.SessionID, sessionpkg.MetadataPatch{
		"wake_request":      "",
		"wake_requested_at": "",
	}); err != nil {
		t.Fatalf("clear status-heal wake marker before drain: %v", err)
	}
	clearedBeforeDrain, err := backingStore.Get(created.SessionID)
	if err != nil {
		t.Fatalf("read session after clearing status-heal wake marker: %v", err)
	}
	if clearedBeforeDrain.Metadata["wake_request"] != "" || clearedBeforeDrain.Metadata["wake_requested_at"] != "" {
		t.Fatalf("status-heal wake marker before drain = %#v, want both fields cleared", clearedBeforeDrain.Metadata)
	}
	started, err = sessionFrontDoor(backingStore).Get(created.SessionID)
	if err != nil {
		t.Fatalf("project session after clearing status-heal wake marker: %v", err)
	}

	stopPending, err := sessionFrontDoor(backingStore).ApplyPatchInfo(
		started,
		sessionpkg.DrainAckStopPendingPatch(time.Now().UTC()),
	)
	if err != nil {
		t.Fatalf("persist drain-ack stop-pending through session front door: %v", err)
	}
	if !isDrainAckStopPendingInfo(stopPending) {
		t.Fatalf("stop-pending projection = %#v, want durable drain-ack marker", stopPending)
	}

	stopAdmittedAt := time.Now().UTC()
	eventOutput := runGC(10*time.Second,
		"--city", cityPath,
		"event", "emit", "bead.updated",
		"--subject", created.SessionID,
		"--bead-payload", created.SessionID,
		"--actor", "bd-hook",
		"--json",
	)
	var emitted struct {
		HasPayload bool `json:"has_payload"`
		Submitted  bool `json:"submitted"`
	}
	if err := json.Unmarshal([]byte(exactStartStopJSONPayload(eventOutput)), &emitted); err != nil {
		t.Fatalf("decode typed stop event: %v\n%s", err, eventOutput)
	}
	if !emitted.HasPayload || !emitted.Submitted {
		t.Fatalf("typed stop event = %+v, want submitted bead payload; output=%q", emitted, eventOutput)
	}

	var (
		stopped         sessionpkg.Info
		stopFinalizedAt time.Time
	)
	if err := waitExactStartStopState(t.Context(), 10*time.Second, func() (bool, error) {
		info, getErr := sessionFrontDoor(backingStore).Get(created.SessionID)
		if getErr != nil {
			return false, getErr
		}
		if info.MetadataState != string(sessionpkg.StateDrained) || isDrainAckStopPendingInfo(info) {
			return false, nil
		}
		stopped = info
		stopFinalizedAt = time.Now().UTC()
		return true, nil
	}); err != nil {
		current, currentErr := sessionFrontDoor(backingStore).Get(created.SessionID)
		currentIDs, idsErr := tmuxClient.ListSessionIDs()
		t.Fatalf("exact stop did not converge: %v; current=%+v current_err=%v tmux_ids=%v tmux_err=%v controller stdout=%q stderr=%q",
			err, current, currentErr, currentIDs, idsErr, controllerStdout.String(), controllerStderr.String())
	}
	if !stopFinalizedAt.After(stopAdmittedAt) {
		t.Fatalf("exact-stop finalized at %s before admission at %s", stopFinalizedAt, stopAdmittedAt)
	}
	if stopped.Closed {
		t.Fatal("exact stop closed the durable session bead; want open drained bead")
	}
	stoppedBead, err := backingStore.Get(created.SessionID)
	if err != nil {
		t.Fatalf("read exact-stop bead from real bd: %v", err)
	}
	if stoppedBead.Status != "open" {
		t.Fatalf("exact-stop bead status = %q, want open", stoppedBead.Status)
	}
	afterIDs, listErr := tmuxClient.ListSessionIDs()
	if listErr != nil && !strings.Contains(strings.ToLower(listErr.Error()), "no server running") {
		t.Fatalf("list isolated tmux sessions after exact stop: %v", listErr)
	}
	if afterID := strings.TrimSpace(afterIDs[created.SessionName]); afterID != "" {
		t.Fatalf("exact stop left or replaced tmux target %q: before=%q after=%q all=%v",
			created.SessionName, startedTmuxID, afterID, afterIDs)
	}
	startSuccessLog := fmt.Sprintf(
		"session lifecycle: op=start wave=0 session=%s template=worker outcome=success",
		created.SessionName,
	)
	if count := strings.Count(controllerStderr.String(), startSuccessLog); count != 1 {
		t.Fatalf("successful provider starts = %d, want exactly 1; controller stderr=%q", count, controllerStderr.String())
	}

	runGC(10*time.Second,
		"--city", cityPath,
		"trace", "start",
		"--template", "worker",
		"--for", "2m",
		"--level", string(TraceModeDetail),
	)
	preWakeBDTraceCount := len(readBDTrace())
	wakeCommandAt := time.Now().UTC()
	wakeOutput := runGC(30*time.Second,
		"--city", cityPath,
		"session", "wake", created.SessionID,
		"--json",
	)
	var wakeResult sessionActionResult
	if err := json.Unmarshal([]byte(exactStartStopJSONPayload(wakeOutput)), &wakeResult); err != nil {
		t.Fatalf("decode exact session wake: %v\n%s", err, wakeOutput)
	}
	if wakeResult.Action != "wake" || wakeResult.SessionID != created.SessionID || wakeResult.State != "wake_requested" {
		t.Fatalf("exact session wake result = %+v, want wake_requested for durable session %q", wakeResult, created.SessionID)
	}

	var (
		woken           sessionpkg.Info
		wakeFinalizedAt time.Time
	)
	if err := waitExactStartStopState(t.Context(), 30*time.Second, func() (bool, error) {
		info, getErr := sessionFrontDoor(backingStore).Get(created.SessionID)
		if getErr != nil {
			return false, getErr
		}
		if info.MetadataState != string(sessionpkg.StateAwake) || info.PendingCreateClaim {
			return false, nil
		}
		woken = info
		wakeFinalizedAt = time.Now().UTC()
		return true, nil
	}); err != nil {
		current, currentErr := sessionFrontDoor(backingStore).Get(created.SessionID)
		t.Fatalf("exact wake did not converge: %v; current=%+v current_err=%v controller stdout=%q stderr=%q",
			err, current, currentErr, controllerStdout.String(), controllerStderr.String())
	}
	if !wakeFinalizedAt.After(wakeCommandAt) {
		t.Fatalf("exact wake finalized at %s before command at %s", wakeFinalizedAt, wakeCommandAt)
	}
	wokenBead, err := backingStore.Get(created.SessionID)
	if err != nil {
		t.Fatalf("read exact wake bead from real bd: %v", err)
	}
	if woken.ID != created.SessionID || woken.SessionName != created.SessionName ||
		wokenBead.Metadata["wake_request"] != "" || wokenBead.Metadata["wake_requested_at"] != "" ||
		strings.TrimSpace(woken.InstanceToken) == "" || woken.InstanceToken == started.InstanceToken {
		t.Fatalf("exact wake durable session/bead = %+v/%+v, want same identity, cleared wake marker, and a new non-empty instance token", woken, wokenBead)
	}
	wakeToken, err := tmuxClient.GetEnvironment(created.SessionName, "GC_INSTANCE_TOKEN")
	if err != nil || strings.TrimSpace(wakeToken) == "" || wakeToken != woken.InstanceToken {
		t.Fatalf("exact wake live/durable token = %q/%q err=%v, want same new non-empty token", wakeToken, woken.InstanceToken, err)
	}
	wakeIDs, err := tmuxClient.ListSessionIDs()
	if err != nil {
		t.Fatalf("read exact wake tmux identity: %v", err)
	}
	wakeTmuxID := strings.TrimSpace(wakeIDs[created.SessionName])
	wakeTmuxServerPID := guard.ServerPID()
	if wakeTmuxServerPID <= 0 {
		t.Fatalf("exact wake tmux server PID = %d, want positive", wakeTmuxServerPID)
	}
	if wakeTmuxID == "" {
		t.Fatalf("exact wake tmux identity for %q is empty: %v", created.SessionName, wakeIDs)
	}
	if wakeTmuxServerPID == startedTmuxServerPID && wakeTmuxID == startedTmuxID {
		t.Fatalf("exact wake tmux incarnation reused server/session identity: server=%d session=%q all=%v",
			wakeTmuxServerPID, wakeTmuxID, wakeIDs)
	}
	postWakeBDTrace := readBDTrace()[preWakeBDTraceCount:]
	var exactWakeWitnesses []bdTraceRecord
	for _, record := range postWakeBDTrace {
		if hasTraceArgument(record.Args, created.SessionID) &&
			hasTraceArgument(record.Args, "--set-metadata") &&
			hasTraceArgument(record.Args, "instance_token="+woken.InstanceToken) &&
			hasTraceArgument(record.Args, "state=creating") &&
			hasTraceCaller(record.Callers, "prepareExactStartCandidateForCity") {
			exactWakeWitnesses = append(exactWakeWitnesses, record)
		}
	}
	if len(exactWakeWitnesses) == 0 {
		t.Fatalf("exact wake has no target-specific exact-start Bd witness; target=%q token=%q post_wake_trace=%#v",
			created.SessionID, woken.InstanceToken, postWakeBDTrace)
	}
	if err := waitExactStartStopState(t.Context(), 10*time.Second, func() (bool, error) {
		count := strings.Count(controllerStderr.String(), startSuccessLog)
		if count > 2 {
			return false, fmt.Errorf("successful provider starts after wake = %d, want exactly 2; controller stderr=%q", count, controllerStderr.String())
		}
		return count == 2, nil
	}); err != nil {
		t.Fatalf("successful provider starts after wake did not settle at exactly 2: %v; controller stderr=%q",
			err, controllerStderr.String())
	}
	wakeStartRecords, err := ReadTraceRecords(traceCityRuntimeDir(cityPath), TraceFilter{
		RecordType:  TraceRecordOperation,
		SiteCode:    TraceSiteLifecycleStartRun,
		SessionName: created.SessionName,
	})
	if err != nil {
		t.Fatalf("read exact wake provider-start trace: %v", err)
	}
	for _, record := range wakeStartRecords {
		if record.OutcomeCode == TraceOutcomeStartEnqueued {
			t.Fatalf("exact wake used legacy async start for %q: %#v", created.SessionName, wakeStartRecords)
		}
	}
	traceStatusOutput := runGC(10*time.Second, "--city", cityPath, "trace", "status", "--json")
	var wakeTraceStatus traceStatusResultJSON
	if err := json.Unmarshal([]byte(exactStartStopJSONPayload(traceStatusOutput)), &wakeTraceStatus); err != nil {
		t.Fatalf("decode trace status after exact wake: %v\n%s", err, traceStatusOutput)
	}
	if !wakeTraceStatus.SessionReconciler.Available || wakeTraceStatus.SessionReconciler.ConfiguredMode != "auto" || wakeTraceStatus.SessionReconciler.EffectiveOwner != "keyed" {
		t.Fatalf("trace status after exact wake = %+v, want available auto/keyed", wakeTraceStatus.SessionReconciler)
	}

	sample := exactSessionStartStopDurableSample{
		Version:                        "exact-session-start-stop-v2",
		SessionID:                      created.SessionID,
		SchemaStatus:                   schemaStatus,
		StartAdmissionToFinalizationNS: startFinalizedAt.Sub(startAdmittedAt).Nanoseconds(),
		StopAdmissionToFinalizationNS:  stopFinalizedAt.Sub(stopAdmittedAt).Nanoseconds(),
		WakeCommandToFinalizationNS:    wakeFinalizedAt.Sub(wakeCommandAt).Nanoseconds(),
		StartPersistedState:            started.MetadataState,
		StopPersistedState:             stopped.MetadataState,
		WakePersistedState:             woken.MetadataState,
	}
	if sample.WakeCommandToFinalizationNS <= 0 {
		t.Fatalf("wake command-to-finalization latency = %d, want positive", sample.WakeCommandToFinalizationNS)
	}
	wire, err := json.Marshal(sample)
	if err != nil {
		t.Fatalf("marshal exact start-stop sample: %v", err)
	}
	t.Logf("EXACT_START_STOP_DURABLE_SAMPLE %s", wire)

	stopOutput, stopErr := runExactStartStopGC(commandEnv, 30*time.Second, gcBinary, "stop", "--force", cityPath)
	if stopErr != nil {
		t.Fatalf("stop keyed production controller: %v\n%s", stopErr, stopOutput)
	}
	controllerStopped = true
	cancelController()
	select {
	case controllerErr := <-controllerDone:
		controllerWaited = true
		if controllerErr != nil && !strings.Contains(strings.ToLower(controllerErr.Error()), "signal: killed") {
			t.Fatalf("keyed production controller exit: %v", controllerErr)
		}
	case <-time.After(testutil.ExecRaceTimeout):
		t.Fatalf("keyed production controller did not exit; stdout=%q stderr=%q",
			controllerStdout.String(), controllerStderr.String())
	}
	if count := strings.Count(controllerStderr.String(), startSuccessLog); count != 2 {
		t.Fatalf("successful provider starts after keyed controller exit = %d, want exactly 2; controller stderr=%q",
			count, controllerStderr.String())
	}
	wakeCommitRecords, err := ReadTraceRecords(traceCityRuntimeDir(cityPath), TraceFilter{
		RecordType:  TraceRecordOperation,
		SiteCode:    TraceSiteLifecycleStartCommit,
		SessionName: created.SessionName,
		TraceMode:   TraceModeDetail,
		TraceSource: TraceSourceManual,
	})
	if err != nil {
		t.Fatalf("read exact wake commit trace after keyed controller exit: %v", err)
	}
	var socketWakeCommitRecords []SessionReconcilerTraceRecord
	for _, record := range wakeCommitRecords {
		if record.SessionBeadID == created.SessionID &&
			record.Fields["admission"] == string(sessionStartAdmissionSocket) &&
			record.Fields["session_id"] == created.SessionID &&
			record.Fields["instance_token"] == woken.InstanceToken &&
			record.Fields["effect_applied"] == true {
			socketWakeCommitRecords = append(socketWakeCommitRecords, record)
		}
	}
	if len(socketWakeCommitRecords) != 1 {
		t.Fatalf("socket wake commit traces after keyed controller exit = %#v, want exactly one durable committed socket start for session %q token %q",
			socketWakeCommitRecords, created.SessionID, woken.InstanceToken)
	}
	if err := ensureBeadsProvider(cityPath); err != nil {
		t.Fatalf("restart test-owned bead provider for fixture reset: %v", err)
	}
	fixtureIDs, fixtureListErr := tmuxClient.ListSessionIDs()
	if fixtureListErr != nil && !strings.Contains(strings.ToLower(fixtureListErr.Error()), "no server running") {
		t.Fatalf("list isolated tmux sessions before fixture reset: %v", fixtureListErr)
	}
	if strings.TrimSpace(fixtureIDs[created.SessionName]) != "" {
		if err := tmuxClient.KillSession(created.SessionName); err != nil {
			fixtureIDs, fixtureListErr = tmuxClient.ListSessionIDs()
			if fixtureListErr != nil && !strings.Contains(strings.ToLower(fixtureListErr.Error()), "no server running") {
				t.Fatalf("remove original tmux session %q: %v; list error: %v", created.SessionName, err, fixtureListErr)
			}
			if strings.TrimSpace(fixtureIDs[created.SessionName]) != "" {
				t.Fatalf("remove original tmux session %q: %v; all=%v", created.SessionName, err, fixtureIDs)
			}
		}
	}
	fixtureResetPatch := sessionpkg.AcknowledgeDrainPatch(false)
	fixtureResetPatch["wake_request"] = ""
	fixtureResetPatch["wake_requested_at"] = ""
	fixtureResetPatch["pending_create_claim"] = ""
	fixtureResetPatch["pending_create_started_at"] = ""
	fixtureResetPatch["state_reason"] = ""
	fixtureResetPatch["sleep_reason"] = ""
	if err := sessionFrontDoor(backingStore).ApplyPatch(created.SessionID, fixtureResetPatch); err != nil {
		t.Fatalf("reset original session fixture to drained: %v", err)
	}
	// An open manual session remains desired under the legacy reconciler even
	// after a drain. The wake journey is complete, so retire only this fixture
	// before measuring the unrelated legacy-shadow session.
	if err := backingStore.Close(created.SessionID); err != nil {
		t.Fatalf("close original session fixture after wake journey: %v", err)
	}
	fixtureInfo, err := sessionFrontDoor(backingStore).Get(created.SessionID)
	if err != nil {
		t.Fatalf("read original session after fixture reset: %v", err)
	}
	fixtureBead, err := backingStore.Get(created.SessionID)
	if err != nil {
		t.Fatalf("read original bead after fixture reset: %v", err)
	}
	fixtureLifecycle := sessionpkg.ProjectLifecycle(sessionpkg.LifecycleInputFromInfo(fixtureInfo))
	fixtureIDs, fixtureListErr = tmuxClient.ListSessionIDs()
	if fixtureListErr != nil && !strings.Contains(strings.ToLower(fixtureListErr.Error()), "no server running") {
		t.Fatalf("list isolated tmux sessions after fixture reset: %v", fixtureListErr)
	}
	if !fixtureInfo.Closed || fixtureBead.Status != "closed" ||
		fixtureBead.Metadata["state"] != string(sessionpkg.StateDrained) ||
		fixtureLifecycle.BaseState != sessionpkg.BaseStateClosed ||
		!fixtureLifecycle.Terminal ||
		fixtureBead.Metadata["wake_request"] != "" ||
		fixtureBead.Metadata["wake_requested_at"] != "" ||
		fixtureBead.Metadata["pending_create_claim"] != "" ||
		fixtureBead.Metadata["pending_create_started_at"] != "" ||
		fixtureBead.Metadata["state_reason"] != "" ||
		fixtureBead.Metadata["sleep_reason"] != "" ||
		strings.TrimSpace(fixtureIDs[created.SessionName]) != "" {
		t.Fatalf("original fixture after reset = session %#v bead %#v lifecycle %#v tmux=%v, want terminal closed with stored drained state, cleared markers, and no runtime",
			fixtureInfo, fixtureBead, fixtureLifecycle, fixtureIDs)
	}

	cityConfigPath := filepath.Join(cityPath, "city.toml")
	legacyConfig, err := os.ReadFile(cityConfigPath)
	if err != nil {
		t.Fatalf("read initialized config for legacy shadow: %v", err)
	}
	const (
		autoSessionReconciler   = `session_reconciler = "auto"`
		legacySessionReconciler = `session_reconciler = "off"`
	)
	if count := bytes.Count(legacyConfig, []byte(autoSessionReconciler)); count != 1 {
		t.Fatalf("initialized session_reconciler auto settings = %d, want exactly one", count)
	}
	legacyConfig = bytes.Replace(legacyConfig, []byte(autoSessionReconciler), []byte(legacySessionReconciler), 1)
	if err := fsys.WriteFileAtomic(
		fsys.OSFS{},
		cityConfigPath,
		legacyConfig,
		0o644,
	); err != nil {
		t.Fatalf("write legacy shadow config: %v", err)
	}
	beforeLegacyIDs, err := tmuxClient.ListSessionIDs()
	if err != nil && !strings.Contains(strings.ToLower(err.Error()), "no server running") {
		t.Fatalf("list isolated tmux sessions before legacy shadow: %v", err)
	}
	if originalID := strings.TrimSpace(beforeLegacyIDs[created.SessionName]); originalID != "" {
		t.Fatalf("original session resurrected before legacy shadow: %q; all=%v", originalID, beforeLegacyIDs)
	}

	legacyControllerCtx, cancelLegacyController := context.WithCancel(t.Context())
	var legacyControllerStdout, legacyControllerStderr synchronizedBuffer
	legacyControllerCmd := newExactStartStopGCCommand(
		legacyControllerCtx,
		commandEnv,
		gcBinary,
		"start",
		"--foreground",
		"--no-strict",
		cityPath,
	)
	legacyControllerCmd.Stdout = &legacyControllerStdout
	legacyControllerCmd.Stderr = &legacyControllerStderr
	if err := legacyControllerCmd.Start(); err != nil {
		t.Fatalf("start legacy shadow controller: %v", err)
	}
	legacyControllerDone := make(chan error, 1)
	go func() {
		legacyControllerDone <- legacyControllerCmd.Wait()
	}()
	t.Cleanup(func() {
		stopOutput, stopErr := runExactStartStopGC(commandEnv, 30*time.Second, gcBinary, "stop", "--force", cityPath)
		if stopErr != nil {
			t.Errorf("stop legacy shadow controller: %v\n%s", stopErr, stopOutput)
		}
		cancelLegacyController()
		select {
		case <-legacyControllerDone:
		case <-time.After(testutil.ExecRaceTimeout):
			t.Errorf("legacy shadow controller did not exit; stdout=%q stderr=%q",
				legacyControllerStdout.String(), legacyControllerStderr.String())
		}
	})
	waitForControllerAvailable(t, cityPath)

	if err := waitExactStartStopState(t.Context(), 15*time.Second, func() (bool, error) {
		out, runErr := runExactStartStopGC(
			commandEnv,
			10*time.Second,
			gcBinary,
			"--city", cityPath,
			"trace", "status", "--json",
		)
		traceOutput = out
		if runErr != nil {
			return false, runErr
		}
		var status traceStatusResultJSON
		if decodeErr := json.Unmarshal([]byte(exactStartStopJSONPayload(out)), &status); decodeErr != nil {
			return false, decodeErr
		}
		traceStatus = status
		return status.SessionReconciler.Available &&
			status.SessionReconciler.ConfiguredMode == "off" &&
			status.SessionReconciler.EffectiveOwner == "legacy", nil
	}); err != nil {
		t.Fatalf("production session reconciler did not become off/legacy: %v; status=%+v output=%q controller stdout=%q stderr=%q",
			err, traceStatus.SessionReconciler, traceOutput, legacyControllerStdout.String(), legacyControllerStderr.String())
	}
	runGC(10*time.Second,
		"--city", cityPath,
		"trace", "start",
		"--template", "worker",
		"--for", "2m",
		"--level", string(TraceModeDetail),
	)

	shadowCreatedOutput := runGC(30*time.Second,
		"--city", cityPath,
		"session", "new", "worker",
		"--no-attach",
		"--json",
	)
	var shadowCreated sessionNewJSON
	if err := json.Unmarshal([]byte(exactStartStopJSONPayload(shadowCreatedOutput)), &shadowCreated); err != nil {
		t.Fatalf("decode legacy shadow session creation: %v\n%s", err, shadowCreatedOutput)
	}
	if shadowCreated.SessionID == "" || shadowCreated.SessionName == "" || !shadowCreated.DeferredStart {
		t.Fatalf("legacy shadow session creation = %+v, want deferred ID and tmux name", shadowCreated)
	}

	var shadowStarted sessionpkg.Info
	if err := waitExactStartStopState(t.Context(), 45*time.Second, func() (bool, error) {
		info, getErr := sessionFrontDoor(backingStore).Get(shadowCreated.SessionID)
		if getErr != nil {
			return false, getErr
		}
		view := sessionpkg.ProjectLifecycle(sessionpkg.LifecycleInputFromInfo(info))
		if view.BaseState != sessionpkg.BaseStateActive || info.PendingCreateClaim {
			return false, nil
		}
		shadowStarted = info
		return true, nil
	}); err != nil {
		t.Fatalf("legacy shadow start did not converge: %v; current=%+v controller stdout=%q stderr=%q",
			err, shadowStarted, legacyControllerStdout.String(), legacyControllerStderr.String())
	}
	shadowToken, err := tmuxClient.GetEnvironment(shadowCreated.SessionName, "GC_INSTANCE_TOKEN")
	if err != nil {
		t.Fatalf("read legacy shadow tmux instance token: %v; controller stdout=%q stderr=%q",
			err, legacyControllerStdout.String(), legacyControllerStderr.String())
	}
	if strings.TrimSpace(shadowToken) == "" || shadowToken != shadowStarted.InstanceToken {
		t.Fatalf("legacy shadow live/durable instance tokens = %q/%q, want the same non-empty token",
			shadowToken, shadowStarted.InstanceToken)
	}
	afterLegacyStartIDs, err := tmuxClient.ListSessionIDs()
	if err != nil {
		t.Fatalf("list isolated tmux sessions after legacy shadow start: %v", err)
	}
	if originalID := strings.TrimSpace(afterLegacyStartIDs[created.SessionName]); originalID != "" {
		t.Fatalf("legacy shadow start resurrected original session: %q; all=%v", originalID, afterLegacyStartIDs)
	}
	originalAfterLegacyStart, err := sessionFrontDoor(backingStore).Get(created.SessionID)
	if err != nil {
		t.Fatalf("read original session after legacy shadow start: %v", err)
	}
	originalBeadAfterLegacyStart, err := backingStore.Get(created.SessionID)
	if err != nil {
		t.Fatalf("read original bead after legacy shadow start: %v", err)
	}
	originalLifecycleAfterLegacyStart := sessionpkg.ProjectLifecycle(
		sessionpkg.LifecycleInputFromInfo(originalAfterLegacyStart),
	)
	if !originalAfterLegacyStart.Closed || originalBeadAfterLegacyStart.Status != "closed" ||
		originalLifecycleAfterLegacyStart.BaseState != sessionpkg.BaseStateClosed ||
		!originalLifecycleAfterLegacyStart.Terminal ||
		originalAfterLegacyStart.MetadataState != string(sessionpkg.StateDrained) ||
		originalBeadAfterLegacyStart.Metadata["wake_request"] != "" ||
		originalBeadAfterLegacyStart.Metadata["wake_requested_at"] != "" ||
		originalBeadAfterLegacyStart.Metadata["sleep_reason"] != "" {
		t.Fatalf("original session/bead after legacy shadow start = %#v/%#v (lifecycle=%#v), want terminal closed with stored drained state and cleared wake markers",
			originalAfterLegacyStart, originalBeadAfterLegacyStart, originalLifecycleAfterLegacyStart)
	}

	var shadowWitness SessionReconcilerTraceRecord
	if err := waitExactStartStopState(t.Context(), 15*time.Second, func() (bool, error) {
		records, readErr := ReadTraceRecords(traceCityRuntimeDir(cityPath), TraceFilter{
			SiteCode:    TraceSiteLifecycleStartSelectionShadow,
			Template:    "worker",
			TraceMode:   TraceModeDetail,
			TraceSource: TraceSourceManual,
		})
		if readErr != nil {
			return false, readErr
		}
		matches := 0
		for _, record := range records {
			if record.Fields["session_id"] != shadowCreated.SessionID {
				continue
			}
			matches++
			shadowWitness = record
		}
		if matches > 1 {
			return false, fmt.Errorf("legacy shadow witnesses = %d, want exactly one", matches)
		}
		return matches == 1, nil
	}); err != nil {
		t.Fatalf("legacy START-shadow witness did not converge: %v; controller stdout=%q stderr=%q",
			err, legacyControllerStdout.String(), legacyControllerStderr.String())
	}
	if shadowWitness.RecordType != TraceRecordOperation ||
		shadowWitness.OutcomeCode != TraceOutcomeNoChange ||
		shadowWitness.Fields["admitted_template"] != "worker" ||
		shadowWitness.Fields["admitted_source"] != string(TraceSourceManual) ||
		shadowWitness.Fields["candidate_outcome"] != "prepare" ||
		shadowWitness.Fields["candidate_reason"] != string(sessionLifecycleStartSelectionReasonReady) ||
		shadowWitness.Fields["legacy_selected"] != true ||
		shadowWitness.Fields["comparison_outcome"] != string(sessionLifecycleStartSelectionComparisonMatched) ||
		shadowWitness.Fields["comparison_reason"] != string(sessionLifecycleStartSelectionComparisonReasonEquivalent) ||
		shadowWitness.Fields["effect_applied"] != false {
		t.Fatalf("legacy START-shadow witness = %#v, want matched legacy-owned no-effect evidence", shadowWitness)
	}
	startRecords, err := ReadTraceRecords(traceCityRuntimeDir(cityPath), TraceFilter{
		RecordType:  TraceRecordOperation,
		SiteCode:    TraceSiteLifecycleStartRun,
		Template:    "worker",
		SessionName: shadowCreated.SessionName,
	})
	if err != nil {
		t.Fatalf("read legacy provider-start trace: %v", err)
	}
	// Legacy starts execute asynchronously: this operation record owns the
	// dispatch boundary and therefore reports start_enqueued. Exactly one
	// enqueue plus the durable-active and exact-tmux-identity assertions above
	// proves one successful provider execution without depending on stderr text.
	if len(startRecords) != 1 || startRecords[0].OutcomeCode != TraceOutcomeStartEnqueued {
		t.Fatalf("legacy provider-start trace = %#v, want exactly one async start enqueue", startRecords)
	}
}

func replaceEnvEntry(env []string, key, value string) []string {
	prefix := key + "="
	out := make([]string, 0, len(env)+1)
	for _, entry := range env {
		if strings.HasPrefix(entry, prefix) {
			continue
		}
		out = append(out, entry)
	}
	return append(out, prefix+value)
}

func newExactStartStopGCCommand(ctx context.Context, env []string, binary string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, binary, args...)
	cmd.Env = env
	return cmd
}

func runExactStartStopGC(env []string, timeout time.Duration, binary string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := newExactStartStopGCCommand(ctx, env, binary, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	return strings.TrimSpace(stdout.String() + stderr.String()), err
}

func waitExactStartStopState(ctx context.Context, timeout time.Duration, condition func() (bool, error)) error {
	deadline, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	var lastErr error
	for {
		ok, err := condition()
		if err != nil {
			lastErr = err
		} else if ok {
			return nil
		}
		select {
		case <-deadline.Done():
			return fmt.Errorf("%w (last observation error: %v)", deadline.Err(), lastErr)
		case <-ticker.C:
		}
	}
}

func exactStartBDCommand(t *testing.T, cityPath string, args ...string) string {
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

func exactStartStopJSONPayload(raw string) string {
	data := []byte(raw)
	for i, b := range data {
		if b != '{' && b != '[' {
			continue
		}
		candidate := bytes.TrimSpace(data[i:])
		if json.Valid(candidate) {
			return string(candidate)
		}
	}
	return raw
}
