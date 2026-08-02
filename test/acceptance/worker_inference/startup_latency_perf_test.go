//go:build acceptance_c

package workerinference_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/config"
	"github.com/gastownhall/gascity/internal/fsys"
	sessionpkg "github.com/gastownhall/gascity/internal/session"
	workerpkg "github.com/gastownhall/gascity/internal/worker"
	helpers "github.com/gastownhall/gascity/test/acceptance/helpers"
)

const (
	agentStartPerfOptInEnv       = "GC_RUN_AGENT_START_PERF"
	agentStartPerfReportEnv      = "GC_AGENT_START_PERF_REPORT"
	agentStartPerfSamplesEnv     = "GC_AGENT_START_PERF_SAMPLES"
	agentStartPerfObserverPeriod = 50 * time.Millisecond
	agentStartPerfSampleTimeout  = 6 * time.Minute
	agentStartPerfOutputFile     = "agent-start-latency-proof.txt"
	agentStartPerfOutputText     = "agent-start-latency-proof-v1"
)

func TestAgentStartLatencyPerf(t *testing.T) {
	if os.Getenv(agentStartPerfOptInEnv) != "1" {
		t.Skipf("set %s=1 to run the exact-binary agent-start benchmark", agentStartPerfOptInEnv)
	}
	reportPath := strings.TrimSpace(os.Getenv(agentStartPerfReportEnv))
	if reportPath == "" || !filepath.IsAbs(reportPath) {
		t.Fatalf("%s must name an absolute report path", agentStartPerfReportEnv)
	}
	requested, err := agentStartPerfRequestedSamples(os.Getenv(agentStartPerfSamplesEnv))
	if err != nil {
		t.Fatal(err)
	}

	run := agentStartLatencyRunState{
		Requested: requested,
		Warmup: agentStartLatencySample{
			Outcome: agentStartOutcomeNotAttempted,
			Error:   "warmup was not attempted",
		},
		Samples: agentStartPerfNotAttemptedSamples(requested, "sample was not attempted"),
	}
	provenance := agentStartPerfBaseProvenance()
	defer func() {
		if err := writeAgentStartLatencyReport(reportPath, provenance, run); err != nil {
			t.Errorf("publish agent-start latency report: %v", err)
			return
		}
		t.Logf("agent-start latency report: %s", reportPath)
	}()

	harness, err := newAgentStartPerfHarness(t)
	if err != nil {
		provenance.Error = err.Error()
		run.FatalError = "benchmark setup failed: " + err.Error()
		for i := range run.Samples {
			run.Samples[i].Error = run.FatalError
		}
		run.Warmup.Error = run.FatalError
		t.Fatal(run.FatalError)
	}
	provenance = harness.provenance

	warmup, safe := harness.runSample(t.Context(), 0)
	run.Warmup = warmup
	if warmup.Outcome != agentStartOutcomeCompleted || !safe {
		run.FatalError = "excluded warmup did not complete safely"
		for i := range run.Samples {
			run.Samples[i].Error = run.FatalError
		}
		t.Fatalf("%s: %s", run.FatalError, warmup.Error)
	}

	for i := range run.Samples {
		sample, sampleSafe := harness.runSample(t.Context(), i+1)
		run.Samples[i] = sample
		if sampleSafe {
			continue
		}
		run.FatalError = fmt.Sprintf("sample %d cleanup was not proven safe", i+1)
		for remaining := i + 1; remaining < len(run.Samples); remaining++ {
			run.Samples[remaining].Error = run.FatalError
		}
		break
	}

	report, err := buildAgentStartLatencyReport(provenance, run)
	if err != nil {
		t.Fatalf("validate agent-start latency report: %v", err)
	}
	if !report.OK {
		t.Fatalf("agent-start benchmark retained non-completed samples: %+v", report.LatencyOutcomeCounts)
	}
}

func agentStartPerfRequestedSamples(raw string) (int, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return agentStartLatencyDefaultSamples, nil
	}
	count, err := strconv.Atoi(raw)
	if err != nil || count <= 0 {
		return 0, fmt.Errorf("%s must be a positive integer, got %q", agentStartPerfSamplesEnv, raw)
	}
	return count, nil
}

func agentStartPerfNotAttemptedSamples(count int, reason string) []agentStartLatencySample {
	samples := make([]agentStartLatencySample, count)
	for i := range samples {
		samples[i] = agentStartLatencySample{Index: i + 1, Outcome: agentStartOutcomeNotAttempted, Error: reason}
	}
	return samples
}

type agentStartPerfHarness struct {
	cityDir      string
	socket       string
	prompt       string
	outputPath   string
	outputText   string
	processNames []string
	readiness    agentStartLatencyReadiness
	adapter      workerpkg.SessionLogAdapter
	provenance   agentStartLatencyProvenance
}

func newAgentStartPerfHarness(t *testing.T) (*agentStartPerfHarness, error) {
	t.Helper()
	if liveSetup.SetupError != "" {
		return nil, errors.New(liveSetup.SetupError)
	}
	if bdPath := helpers.FindBD(); bdPath != "" {
		originalPath := liveEnv.Get("PATH")
		liveEnv.With("PATH", filepath.Dir(bdPath)+string(os.PathListSeparator)+originalPath)
		t.Cleanup(func() { liveEnv.With("PATH", originalPath) })
	}
	if liveSetup.Profile == workerpkg.ProfileClaudeTmuxCLI && strings.TrimSpace(liveEnv.Get("CLAUDE_CONFIG_DIR")) == "" {
		liveEnv.With("CLAUDE_CONFIG_DIR", filepath.Join(liveEnv.Get("GC_HOME"), ".claude"))
		t.Cleanup(func() { liveEnv.Without("CLAUDE_CONFIG_DIR") })
	}
	t.Setenv("GC_HOME", liveEnv.Get("GC_HOME"))
	t.Setenv("PATH", liveEnv.Get("PATH"))
	city := newLiveCity(t)
	initArgs := []string{"init", "--skip-provider-readiness", "--no-start"}
	if liveSetup.Provider != "" {
		initArgs = append(initArgs, "--provider", liveSetup.Provider)
	}
	initArgs = append(initArgs, city.Dir)
	if out, err := runGCWithTimeout(liveBootstrapTimeout, liveEnv, "", initArgs...); err != nil {
		return nil, fmt.Errorf("gc init: %w: %s", err, strings.TrimSpace(out))
	}
	if err := seedLiveProviderState(city.Dir); err != nil {
		return nil, fmt.Errorf("seed provider state: %w", err)
	}
	if err := installInferenceProbeAgent(city.Dir, false); err != nil {
		return nil, fmt.Errorf("install probe agent: %w", err)
	}
	prompt := fmt.Sprintf("Create a file named %s containing exactly %q and nothing else. After the file is written, respond with exactly DONE.", agentStartPerfOutputFile, agentStartPerfOutputText)
	promptPath := filepath.Join(city.Dir, "agents", inferenceProbeTemplate, "prompt.template.md")
	if err := os.WriteFile(promptPath, []byte(prompt+"\n"), 0o644); err != nil {
		return nil, fmt.Errorf("write probe startup prompt: %w", err)
	}
	if err := installLiveProviderCommandOverrideWithArgs(city.Dir, liveSetup.Provider, liveSetup.BinaryPath, liveSetup.ProcessNames, liveProviderArgsAppend()); err != nil {
		return nil, fmt.Errorf("install live provider override: %w", err)
	}
	if err := agentStartPerfRequireReconciler(city.Dir); err != nil {
		return nil, err
	}
	if err := agentStartPerfSuspendNamedDefaults(city.Dir); err != nil {
		return nil, err
	}

	_, _ = runGCWithTimeout(liveShutdownTimeout, liveEnv, city.Dir, "stop", city.Dir)
	_, _ = runGCWithTimeout(liveShutdownTimeout, liveEnv, "", "supervisor", "stop")
	_, _ = waitForManagedDoltStopped(city.Dir, liveStopBarrierTimeout)
	startOut, startErr := runGCWithTimeout(liveBootstrapTimeout, liveEnv, city.Dir, "start", city.Dir)
	if startErr != nil && !isRunTimeout(startErr) {
		return nil, fmt.Errorf("gc start: %w: %s", startErr, strings.TrimSpace(startOut))
	}
	t.Cleanup(func() {
		_, _ = runGCWithTimeout(liveShutdownTimeout, liveEnv, city.Dir, "stop", city.Dir)
		_, _ = runGCWithTimeout(liveShutdownTimeout, liveEnv, "", "supervisor", "stop")
		_, _ = waitForManagedDoltStopped(city.Dir, liveStopBarrierTimeout)
	})
	if !pollForCondition(60*time.Second, 250*time.Millisecond, func() bool {
		_, err := bdCmd(liveEnv, city.Dir, "list", "--json", "--limit=1")
		return err == nil
	}) {
		return nil, fmt.Errorf("bead store did not become ready after gc start: %s", strings.TrimSpace(startOut))
	}
	if out, err := runGCWithTimeout(liveControlTimeout, liveEnv, city.Dir, "trace", "start", "--template", inferenceProbeTemplate, "--for", "2h", "--level", "detail"); err != nil {
		return nil, fmt.Errorf("arm probe detail trace: %w: %s", err, strings.TrimSpace(out))
	}
	if err := agentStartPerfWaitForKeyedController(city.Dir); err != nil {
		return nil, err
	}

	cfg, _, err := config.LoadWithIncludes(fsys.OSFS{}, filepath.Join(city.Dir, "city.toml"))
	if err != nil {
		return nil, fmt.Errorf("load measured city config: %w", err)
	}
	probeAgent := config.Agent{Name: inferenceProbeTemplate}
	for i := range cfg.Agents {
		if config.AgentMatchesIdentity(&cfg.Agents[i], inferenceProbeTemplate) {
			probeAgent = cfg.Agents[i]
			break
		}
	}
	resolved, err := config.ResolveProvider(&probeAgent, &cfg.Workspace, cfg.Providers, exec.LookPath)
	if err != nil {
		return nil, fmt.Errorf("resolve measured provider: %w", err)
	}
	readiness := agentStartLatencyReadiness{Delay: time.Duration(resolved.ReadyDelayMs) * time.Millisecond}
	switch {
	case strings.TrimSpace(resolved.ReadyPromptPrefix) != "":
		readiness.Strategy = agentStartReadinessPromptPrefix
		readiness.PromptPrefix = resolved.ReadyPromptPrefix
	case resolved.ReadyDelayMs > 0:
		readiness.Strategy = agentStartReadinessFixedDelay
	default:
		readiness.Strategy = agentStartReadinessNone
	}
	processNames := append([]string(nil), resolved.ProcessNames...)
	if len(processNames) == 0 {
		processNames = []string{filepath.Base(liveSetup.BinaryPath)}
	}
	socket, err := tmuxSocketNameForCity(city.Dir)
	if err != nil {
		return nil, err
	}
	provenance, err := agentStartPerfProvenance(city.Dir, promptPath, socket, readiness)
	if err != nil {
		return nil, err
	}
	return &agentStartPerfHarness{
		cityDir:      city.Dir,
		socket:       socket,
		prompt:       prompt,
		outputPath:   filepath.Join(city.Dir, agentStartPerfOutputFile),
		outputText:   agentStartPerfOutputText,
		processNames: processNames,
		readiness:    readiness,
		adapter:      workerpkg.SessionLogAdapter{SearchPaths: liveSetup.SearchPaths},
		provenance:   provenance,
	}, nil
}

func agentStartPerfBaseProvenance() agentStartLatencyProvenance {
	return agentStartLatencyProvenance{
		Profile:         string(liveSetup.Profile),
		Provider:        liveSetup.Provider,
		RuntimeProvider: "tmux",
		ReconcilerMode:  "require",
		HostOS:          runtime.GOOS,
		HostArch:        runtime.GOARCH,
		CPUCount:        runtime.NumCPU(),
		AuthSource:      liveSetup.AuthSource,
	}
}

func agentStartPerfRequireReconciler(cityDir string) error {
	path := filepath.Join(cityDir, "city.toml")
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read city config for required reconciler: %w", err)
	}
	updated, err := withRequiredSessionReconciler(string(data))
	if err != nil {
		return err
	}
	if err := fsys.WriteFileAtomic(fsys.OSFS{}, path, []byte(updated), 0o644); err != nil {
		return fmt.Errorf("write required reconciler config: %w", err)
	}
	return nil
}

func agentStartPerfSuspendNamedDefaults(cityDir string) error {
	cfg, _, err := config.LoadWithIncludes(fsys.OSFS{}, filepath.Join(cityDir, "city.toml"))
	if err != nil {
		return fmt.Errorf("load named sessions for benchmark isolation: %w", err)
	}
	seen := make(map[string]struct{})
	for _, named := range cfg.NamedSessions {
		template := strings.TrimSpace(named.Template)
		if template == "" || template == inferenceProbeTemplate {
			continue
		}
		if _, ok := seen[template]; ok {
			continue
		}
		seen[template] = struct{}{}
		if err := setAgentSuspended(cityDir, template, true); err != nil {
			return fmt.Errorf("suspend default named template %q: %w", template, err)
		}
	}
	return nil
}

func agentStartPerfWaitForKeyedController(cityDir string) error {
	type traceStatus struct {
		ControllerRunning bool `json:"controller_running"`
		SessionReconciler struct {
			Available      bool   `json:"available"`
			ConfiguredMode string `json:"configured_mode"`
			EffectiveOwner string `json:"effective_owner"`
		} `json:"session_reconciler"`
	}
	deadline := time.Now().Add(30 * time.Second)
	var lastDetail string
	for time.Now().Before(deadline) {
		out, err := runGCWithTimeout(5*time.Second, liveEnv, cityDir, "trace", "status", "--json")
		if err != nil {
			lastDetail = strings.TrimSpace(err.Error() + ": " + out)
			time.Sleep(100 * time.Millisecond)
			continue
		}
		payloads := jsonPayloads(out)
		if len(payloads) == 0 {
			lastDetail = "trace status returned no JSON payload"
			time.Sleep(100 * time.Millisecond)
			continue
		}
		var status traceStatus
		if err := json.Unmarshal(payloads[len(payloads)-1], &status); err != nil {
			lastDetail = err.Error()
			time.Sleep(100 * time.Millisecond)
			continue
		}
		if status.ControllerRunning && status.SessionReconciler.Available && status.SessionReconciler.ConfiguredMode == "require" && status.SessionReconciler.EffectiveOwner == "keyed" {
			return nil
		}
		lastDetail = fmt.Sprintf("running=%t available=%t mode=%q owner=%q", status.ControllerRunning, status.SessionReconciler.Available, status.SessionReconciler.ConfiguredMode, status.SessionReconciler.EffectiveOwner)
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("required keyed session controller did not become ready: %s", lastDetail)
}

func agentStartPerfProvenance(cityDir, promptPath, socket string, readiness agentStartLatencyReadiness) (agentStartLatencyProvenance, error) {
	provenance := agentStartPerfBaseProvenance()
	provenance.CityPath = cityDir
	provenance.TmuxSocket = socket
	provenance.Readiness = readiness
	var err error
	provenance.CityConfigSHA256, err = agentStartPerfHashFiles(filepath.Join(cityDir, "city.toml"))
	if err != nil {
		return provenance, fmt.Errorf("hash city config: %w", err)
	}
	provenance.AgentConfigSHA256, err = agentStartPerfHashFiles(filepath.Join(cityDir, "agents", inferenceProbeTemplate, "agent.toml"), promptPath)
	if err != nil {
		return provenance, fmt.Errorf("hash probe config: %w", err)
	}
	provenance.Binaries, provenance.GCCommit, err = agentStartPerfCollectBinaries()
	if err != nil {
		return provenance, err
	}
	return provenance, nil
}

func agentStartPerfCollectBinaries() ([]agentStartLatencyBinary, string, error) {
	gcPath, err := helpers.ResolveGCPath(liveEnv)
	if err != nil {
		return nil, "", fmt.Errorf("resolve measured gc binary: %w", err)
	}
	paths := map[string]string{
		"gc":       gcPath,
		"provider": liveSetup.BinaryPath,
	}
	for _, name := range []string{"tmux", "bd", "dolt"} {
		path, err := exec.LookPath(name)
		if err != nil {
			return nil, "", fmt.Errorf("resolve %s binary: %w", name, err)
		}
		paths[name] = path
	}
	binaries := make([]agentStartLatencyBinary, 0, len(agentStartLatencyRequiredBinaries))
	gcCommit := ""
	for _, name := range agentStartLatencyRequiredBinaries {
		path, err := filepath.Abs(paths[name])
		if err != nil {
			return nil, "", fmt.Errorf("absolute %s binary path: %w", name, err)
		}
		hash, err := agentStartPerfHashFiles(path)
		if err != nil {
			return nil, "", fmt.Errorf("hash %s binary: %w", name, err)
		}
		version, err := agentStartPerfBinaryVersion(name, path)
		if err != nil {
			return nil, "", err
		}
		binaries = append(binaries, agentStartLatencyBinary{Name: name, Path: path, SHA256: hash, Version: version})
		if name == "gc" {
			gcCommit, err = agentStartPerfGCCommit(version)
			if err != nil {
				return nil, "", err
			}
		}
	}
	return binaries, gcCommit, nil
}

func agentStartPerfBinaryVersion(name, path string) (string, error) {
	args := []string{"--version"}
	switch name {
	case "gc":
		args = []string{"version", "--json"}
	case "tmux":
		args = []string{"-V"}
	case "dolt":
		args = []string{"version"}
	}
	out, err := runExternalWithTimeout(10*time.Second, liveEnv, "", path, args...)
	if err != nil {
		return "", fmt.Errorf("read %s version from %q: %w: %s", name, path, err, strings.TrimSpace(out))
	}
	version := strings.TrimSpace(out)
	if version == "" {
		return "", fmt.Errorf("read %s version from %q: empty output", name, path)
	}
	return version, nil
}

func agentStartPerfGCCommit(versionOutput string) (string, error) {
	payloads := jsonPayloads(versionOutput)
	if len(payloads) == 0 {
		return "", fmt.Errorf("gc version returned no JSON payload")
	}
	var version struct {
		Commit string `json:"commit"`
	}
	if err := json.Unmarshal(payloads[len(payloads)-1], &version); err != nil {
		return "", fmt.Errorf("decode gc version: %w", err)
	}
	if strings.TrimSpace(version.Commit) == "" {
		return "", fmt.Errorf("gc version commit is empty")
	}
	return strings.TrimSpace(version.Commit), nil
}

func agentStartPerfHashFiles(paths ...string) (string, error) {
	hash := sha256.New()
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			return "", err
		}
		_, _ = hash.Write([]byte(filepath.Base(path)))
		_, _ = hash.Write([]byte{0})
		_, _ = hash.Write(data)
		_, _ = hash.Write([]byte{0})
	}
	return fmt.Sprintf("%x", hash.Sum(nil)), nil
}

type agentStartRuntimeObservation struct {
	RuntimeAvailableAt *time.Time
	CLIProcessExecAt   *time.Time
	CLIReadyAt         *time.Time
	LastError          string
}

type agentStartRuntimeObserver struct {
	cityDir      string
	socket       string
	processNames []string
	readyPrefix  string
	baseline     map[string]struct{}

	mu     sync.Mutex
	events map[string]agentStartRuntimeObservation
	cancel context.CancelFunc
	done   chan struct{}
}

func startAgentStartRuntimeObserver(ctx context.Context, cityDir, socket string, processNames []string, readyPrefix string) (*agentStartRuntimeObserver, error) {
	panes, err := agentStartPerfListPanes(ctx, socket)
	if err != nil {
		return nil, fmt.Errorf("snapshot tmux sessions before start: %w", err)
	}
	baseline := make(map[string]struct{}, len(panes))
	for _, pane := range panes {
		baseline[pane.SessionName] = struct{}{}
	}
	observerCtx, cancel := context.WithCancel(ctx)
	observer := &agentStartRuntimeObserver{
		cityDir:      cityDir,
		socket:       socket,
		processNames: append([]string(nil), processNames...),
		readyPrefix:  readyPrefix,
		baseline:     baseline,
		events:       make(map[string]agentStartRuntimeObservation),
		cancel:       cancel,
		done:         make(chan struct{}),
	}
	go observer.run(observerCtx)
	return observer, nil
}

func (o *agentStartRuntimeObserver) run(ctx context.Context) {
	defer close(o.done)
	ticker := time.NewTicker(agentStartPerfObserverPeriod)
	defer ticker.Stop()
	for {
		o.poll(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (o *agentStartRuntimeObserver) poll(ctx context.Context) {
	panes, err := agentStartPerfListPanes(ctx, o.socket)
	if err != nil {
		o.recordError(err)
		return
	}
	observedAt := time.Now().UTC()
	var candidates []agentStartObservedPane
	for _, pane := range panes {
		if pane.Dead {
			continue
		}
		if _, existed := o.baseline[pane.SessionName]; existed {
			continue
		}
		candidates = append(candidates, pane)
		o.mu.Lock()
		event := o.events[pane.SessionName]
		if event.RuntimeAvailableAt == nil {
			at := observedAt
			event.RuntimeAvailableAt = &at
			o.events[pane.SessionName] = event
		}
		o.mu.Unlock()
	}
	if len(candidates) == 0 {
		return
	}
	processes, processErr := agentStartPerfProcessSnapshot(ctx)
	if processErr != nil {
		o.recordError(processErr)
	}
	for _, pane := range candidates {
		o.mu.Lock()
		event := o.events[pane.SessionName]
		o.mu.Unlock()
		directCommand := agentStartProcessNameListed(pane.Command, o.processNames)
		if event.CLIProcessExecAt == nil && (directCommand || (processErr == nil && agentStartProcessTreeContains(pane.PID, o.processNames, processes))) {
			at := time.Now().UTC()
			event.CLIProcessExecAt = &at
		}
		if event.CLIReadyAt == nil && strings.TrimSpace(o.readyPrefix) != "" {
			paneText, captureErr := captureTmuxPane(o.cityDir, pane.SessionName, 80)
			if captureErr != nil {
				o.recordError(captureErr)
			} else if strings.Contains(paneText, strings.TrimSpace(o.readyPrefix)) {
				at := time.Now().UTC()
				event.CLIReadyAt = &at
			}
		}
		o.mu.Lock()
		o.events[pane.SessionName] = event
		o.mu.Unlock()
	}
}

func (o *agentStartRuntimeObserver) recordError(err error) {
	if err == nil {
		return
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	for name, event := range o.events {
		event.LastError = err.Error()
		o.events[name] = event
	}
}

func (o *agentStartRuntimeObserver) snapshot(sessionName string) agentStartRuntimeObservation {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.events[strings.TrimSpace(sessionName)]
}

func (o *agentStartRuntimeObserver) stop() {
	if o == nil || o.cancel == nil {
		return
	}
	o.cancel()
	<-o.done
	o.cancel = nil
}

type agentStartObservedPane struct {
	SessionName string
	Dead        bool
	PID         int
	Command     string
}

func agentStartPerfListPanes(parent context.Context, socket string) ([]agentStartObservedPane, error) {
	ctx, cancel := context.WithTimeout(parent, time.Second)
	defer cancel()
	tmuxPath, err := exec.LookPath("tmux")
	if err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(ctx, tmuxPath, "-L", socket, "list-panes", "-a", "-F", "#{session_name}\t#{pane_dead}\t#{pane_pid}\t#{pane_current_command}")
	cmd.Env = liveEnv.List()
	out, err := cmd.CombinedOutput()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && agentStartPerfEmptyTmuxListResult(exitErr.ExitCode(), string(out)) {
			return nil, nil
		}
		return nil, fmt.Errorf("tmux -L %q list-panes: %w: %s", socket, err, strings.TrimSpace(string(out)))
	}
	var panes []agentStartObservedPane
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		parts := strings.SplitN(line, "\t", 4)
		if len(parts) != 4 {
			continue
		}
		pid, err := strconv.Atoi(strings.TrimSpace(parts[2]))
		if err != nil || strings.TrimSpace(parts[0]) == "" {
			continue
		}
		panes = append(panes, agentStartObservedPane{
			SessionName: strings.TrimSpace(parts[0]),
			Dead:        strings.TrimSpace(parts[1]) == "1",
			PID:         pid,
			Command:     strings.TrimSpace(parts[3]),
		})
	}
	return panes, nil
}

func agentStartPerfEmptyTmuxListResult(exitCode int, output string) bool {
	if exitCode != 1 {
		return false
	}
	text := strings.ToLower(strings.TrimSpace(output))
	return strings.Contains(text, "no server") ||
		strings.Contains(text, "failed to connect") ||
		strings.Contains(text, "error connecting to") ||
		strings.Contains(text, "no current target")
}

func agentStartPerfProcessSnapshot(parent context.Context) ([]agentStartObservedProcess, error) {
	ctx, cancel := context.WithTimeout(parent, time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "ps", "-e", "-o", "pid=,ppid=,comm=,args=")
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("snapshot process table: %w", err)
	}
	var processes []agentStartObservedProcess
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}
		pid, pidErr := strconv.Atoi(fields[0])
		ppid, ppidErr := strconv.Atoi(fields[1])
		if pidErr != nil || ppidErr != nil {
			continue
		}
		args := ""
		if len(fields) > 3 {
			args = strings.Join(fields[3:], " ")
		}
		processes = append(processes, agentStartObservedProcess{PID: pid, PPID: ppid, Command: fields[2], Args: args})
	}
	return processes, nil
}

func agentStartProcessNameListed(command string, names []string) bool {
	command = agentStartProcessName(command)
	for _, name := range names {
		if command != "" && command == agentStartProcessName(name) {
			return true
		}
	}
	return false
}

type agentStartTurnObservation struct {
	TranscriptPath        string
	Snapshot              *workerpkg.HistorySnapshot
	PromptAt              *time.Time
	FirstAssistantOutput  *time.Time
	FirstTurnCompletedAt  *time.Time
	ExpectedOutputMatched bool
	AssistantAfterPrompt  bool
	TranscriptIdle        bool
	NoOpenToolUse         bool
	NoPendingInteraction  bool
}

type agentStartSessionNewResult struct {
	OK            bool   `json:"ok"`
	SessionID     string `json:"session_id"`
	SessionName   string `json:"session_name"`
	DeferredStart bool   `json:"deferred_start"`
}

func (h *agentStartPerfHarness) runSample(parent context.Context, index int) (agentStartLatencySample, bool) {
	sample := agentStartLatencySample{Index: index, Outcome: agentStartOutcomeError}
	if err := os.Remove(h.outputPath); err != nil && !os.IsNotExist(err) {
		sample.Error = fmt.Sprintf("remove prior output: %v", err)
		return sample, true
	}
	baseline := h.agentStartTranscriptBaseline()
	measurementCtx, cancelMeasurement := context.WithTimeout(parent, agentStartPerfSampleTimeout)
	defer cancelMeasurement()
	readyPrefix := ""
	if h.readiness.Strategy == agentStartReadinessPromptPrefix {
		readyPrefix = h.readiness.PromptPrefix
	}
	observer, err := startAgentStartRuntimeObserver(measurementCtx, h.cityDir, h.socket, h.processNames, readyPrefix)
	if err != nil {
		sample.Error = err.Error()
		return sample, true
	}

	alias := fmt.Sprintf("latency-%03d-%d", index, time.Now().UTC().UnixNano())
	sample.Timestamps.StartInitiatedAt = time.Now().UTC()
	newOut, newErr := runGCWithTimeout(90*time.Second, liveEnv, h.cityDir,
		"session", "new", inferenceProbeTemplate, "--alias", alias, "--no-attach", "--json")
	intentReturnedAt := time.Now().UTC()
	sample.Timestamps.IntentReturnedAt = &intentReturnedAt
	created, parseErr := parseAgentStartSessionNew([]byte(newOut))
	if parseErr == nil {
		sample.SessionID = created.SessionID
		sample.SessionName = created.SessionName
	}
	if strings.TrimSpace(sample.SessionID) == "" || strings.TrimSpace(sample.SessionName) == "" {
		observer.stop()
		problems := []string{}
		if newErr != nil {
			problems = append(problems, "gc session new: "+newErr.Error())
		}
		if parseErr != nil {
			problems = append(problems, parseErr.Error())
		}
		if len(problems) == 0 {
			problems = append(problems, "gc session new returned an empty session identity")
		}
		sample.Error = strings.Join(problems, "; ")
		return sample, false
	}

	turn, turnErr := h.waitForFirstTurn(measurementCtx, sample.SessionID, baseline)
	observer.stop()
	runtimeObservation := observer.snapshot(sample.SessionName)
	sample.Timestamps.RuntimeAvailableAt = runtimeObservation.RuntimeAvailableAt
	sample.Timestamps.CLIProcessExecAt = runtimeObservation.CLIProcessExecAt
	if h.readiness.Strategy == agentStartReadinessPromptPrefix {
		sample.Timestamps.CLIReadyAt = runtimeObservation.CLIReadyAt
	}
	sample.Timestamps.PromptDeliveredAt = turn.PromptAt
	sample.Timestamps.FirstAssistantOutputAt = turn.FirstAssistantOutput
	sample.Timestamps.FirstTurnCompletedAt = turn.FirstTurnCompletedAt
	sample.Terminal.ExpectedOutputMatched = turn.ExpectedOutputMatched
	sample.Terminal.AssistantAfterPrompt = turn.AssistantAfterPrompt
	sample.Terminal.TranscriptIdle = turn.TranscriptIdle
	sample.Terminal.NoOpenToolUse = turn.NoOpenToolUse
	sample.Terminal.NoPendingInteraction = turn.NoPendingInteraction

	var problems []string
	if newErr != nil {
		problems = append(problems, "gc session new: "+newErr.Error())
	}
	if parseErr != nil {
		problems = append(problems, parseErr.Error())
	}
	if !created.OK || !created.DeferredStart {
		problems = append(problems, fmt.Sprintf("session new result ok=%t deferred_start=%t", created.OK, created.DeferredStart))
	}
	if turnErr != nil {
		problems = append(problems, "first turn: "+turnErr.Error())
	}
	if sample.Timestamps.RuntimeAvailableAt == nil {
		problems = append(problems, "tmux runtime was not observed")
	}
	if sample.Timestamps.CLIProcessExecAt == nil {
		problems = append(problems, "provider CLI process exec was not observed")
	}
	if h.readiness.Strategy == agentStartReadinessPromptPrefix && sample.Timestamps.CLIReadyAt == nil {
		problems = append(problems, "configured prompt-prefix readiness was not observed")
	}
	if runtimeObservation.LastError != "" && (sample.Timestamps.RuntimeAvailableAt == nil || sample.Timestamps.CLIProcessExecAt == nil) {
		problems = append(problems, "runtime observer: "+runtimeObservation.LastError)
	}

	store, storeErr := openLiveCityStore(h.cityDir)
	if storeErr != nil {
		problems = append(problems, "open session store after measured turn: "+storeErr.Error())
	} else {
		defer closeLiveHandleStore(store)
		front := sessionpkg.NewStore(beads.SessionStore{Store: store})
		info, _, infoErr := front.GetPersistedResponse(sample.SessionID)
		if infoErr != nil {
			problems = append(problems, "read correlated session: "+infoErr.Error())
		} else {
			if info.ID != sample.SessionID || info.SessionName != sample.SessionName {
				problems = append(problems, fmt.Sprintf("durable identity = %q/%q, want %q/%q", info.ID, info.SessionName, sample.SessionID, sample.SessionName))
			}
			if requiresStableProviderSession(liveSetup.Profile) {
				providerSessionID := ""
				if turn.Snapshot != nil {
					providerSessionID = strings.TrimSpace(turn.Snapshot.ProviderSessionID)
				}
				if strings.TrimSpace(info.SessionKey) == "" || providerSessionID == "" || !sameContinuationIdentity(liveSetup.Profile, info.SessionKey, providerSessionID) {
					problems = append(problems, fmt.Sprintf("transcript provider identity %q does not match durable session key %q", providerSessionID, info.SessionKey))
				}
			}
		}

		traceCtx, cancelTrace := context.WithTimeout(parent, 15*time.Second)
		sample.Controller, err = h.waitForControllerTiming(traceCtx, sample.SessionID)
		cancelTrace()
		if err != nil {
			problems = append(problems, "controller timing: "+err.Error())
		}

		cleanupCtx, cancelCleanup := context.WithTimeout(parent, liveStopBarrierTimeout)
		cleanupAt, cleanupProof, cleanupErr := h.cleanupSession(cleanupCtx, front, sample.SessionID, sample.SessionName)
		cancelCleanup()
		sample.Terminal.DurableSessionRetired = cleanupProof.DurableSessionRetired
		sample.Terminal.TmuxSessionAbsent = cleanupProof.TmuxSessionAbsent
		sample.Timestamps.CleanupCompletedAt = cleanupAt
		if cleanupErr != nil {
			problems = append(problems, "cleanup: "+cleanupErr.Error())
		}
	}

	safe := sample.Terminal.DurableSessionRetired && sample.Terminal.TmuxSessionAbsent
	if storeErr != nil {
		fallbackCtx, cancelFallback := context.WithTimeout(parent, liveStopBarrierTimeout)
		absent, fallbackErr := h.cleanupWithoutStore(fallbackCtx, sample.SessionID, sample.SessionName)
		cancelFallback()
		sample.Terminal.TmuxSessionAbsent = absent
		if fallbackErr != nil {
			problems = append(problems, "fallback cleanup: "+fallbackErr.Error())
		}
		safe = false
	}

	if len(problems) == 0 {
		sample.Outcome = agentStartOutcomeCompleted
		sample.Error = ""
		return sample, safe
	}
	switch {
	case errors.Is(parent.Err(), context.Canceled):
		sample.Outcome = agentStartOutcomeCanceled
	case errors.Is(measurementCtx.Err(), context.DeadlineExceeded):
		sample.Outcome = agentStartOutcomeIncomplete
	default:
		sample.Outcome = agentStartOutcomeError
	}
	sample.Error = strings.Join(problems, "; ")
	return sample, safe
}

func parseAgentStartSessionNew(output []byte) (agentStartSessionNewResult, error) {
	payloads := jsonPayloads(string(output))
	for i := len(payloads) - 1; i >= 0; i-- {
		var result agentStartSessionNewResult
		if err := json.Unmarshal(payloads[i], &result); err != nil {
			continue
		}
		if strings.TrimSpace(result.SessionID) != "" {
			return result, nil
		}
	}
	return agentStartSessionNewResult{}, fmt.Errorf("gc session new returned no correlated JSON session identity: %s", truncateEvidence(strings.TrimSpace(string(output)), 500))
}

func (h *agentStartPerfHarness) agentStartTranscriptBaseline() map[string]struct{} {
	baseline := make(map[string]struct{})
	for _, path := range transcriptCandidatePaths(h.adapter, liveSetup.Profile, h.cityDir, "") {
		baseline[path] = struct{}{}
	}
	if path := strings.TrimSpace(h.adapter.DiscoverWorkDirTranscript(string(liveSetup.Profile), h.cityDir)); path != "" {
		baseline[path] = struct{}{}
	}
	return baseline
}

func (h *agentStartPerfHarness) waitForFirstTurn(ctx context.Context, sessionID string, baseline map[string]struct{}) (agentStartTurnObservation, error) {
	ticker := time.NewTicker(agentStartPerfObserverPeriod)
	defer ticker.Stop()
	var (
		lastErr  error
		lastTurn agentStartTurnObservation
	)
	for {
		observedAt := time.Now().UTC()
		candidates := transcriptCandidatePaths(h.adapter, liveSetup.Profile, h.cityDir, "")
		if path := strings.TrimSpace(h.adapter.DiscoverWorkDirTranscript(string(liveSetup.Profile), h.cityDir)); path != "" {
			candidates = append(candidates, path)
		}
		for _, path := range uniqueNonEmptyPaths(candidates) {
			if _, old := baseline[path]; old {
				continue
			}
			snapshot, err := h.adapter.LoadHistory(workerpkg.LoadRequest{
				Provider:       string(liveSetup.Profile),
				TranscriptPath: path,
				GCSessionID:    sessionID,
			})
			if err != nil {
				lastErr = err
				continue
			}
			observed := observeAgentStartTranscript(snapshot, h.prompt, observedAt)
			turn := agentStartTurnObservation{
				TranscriptPath:       path,
				Snapshot:             snapshot,
				PromptAt:             observed.PromptAt,
				FirstAssistantOutput: observed.FirstAssistantOutputAt,
				AssistantAfterPrompt: observed.AssistantAfterPrompt,
				TranscriptIdle:       observed.TranscriptIdle,
				NoOpenToolUse:        observed.NoOpenToolUse,
				NoPendingInteraction: observed.NoPendingInteraction,
			}
			if turn.PromptAt != nil || lastTurn.Snapshot == nil {
				lastTurn = turn
			}
			data, readErr := os.ReadFile(h.outputPath)
			turn.ExpectedOutputMatched = readErr == nil && strings.TrimSpace(string(data)) == h.outputText
			if turn.PromptAt != nil || lastTurn.Snapshot == nil {
				lastTurn = turn
			}
			if turn.PromptAt != nil && turn.AssistantAfterPrompt && turn.TranscriptIdle && turn.NoOpenToolUse && turn.NoPendingInteraction && turn.ExpectedOutputMatched && !snapshot.TailState.Degraded {
				completedAt := observedAt
				turn.FirstTurnCompletedAt = &completedAt
				return turn, nil
			}
			lastErr = fmt.Errorf("new transcript %q has not reached an idle, machine-checked first turn", path)
		}
		select {
		case <-ctx.Done():
			if lastErr != nil {
				return lastTurn, fmt.Errorf("%w: %v", ctx.Err(), lastErr)
			}
			return lastTurn, ctx.Err()
		case <-ticker.C:
		}
	}
}

func (h *agentStartPerfHarness) waitForControllerTiming(ctx context.Context, sessionID string) (agentStartLatencyControllerTiming, error) {
	var lastErr error
	for {
		out, err := runGCWithTimeout(5*time.Second, liveEnv, h.cityDir,
			"trace", "show", "--template", inferenceProbeTemplate, "--since", "30m", "--type", "operation", "--json")
		if err != nil {
			lastErr = fmt.Errorf("gc trace show: %w: %s", err, strings.TrimSpace(out))
		} else if timing, parseErr := parseAgentStartCommitTrace([]byte(out), sessionID); parseErr == nil {
			return timing, nil
		} else {
			lastErr = parseErr
		}
		select {
		case <-ctx.Done():
			if lastErr != nil {
				return agentStartLatencyControllerTiming{}, fmt.Errorf("%w: %v", ctx.Err(), lastErr)
			}
			return agentStartLatencyControllerTiming{}, ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
}

func (h *agentStartPerfHarness) cleanupSession(ctx context.Context, front *sessionpkg.Store, sessionID, sessionName string) (*time.Time, agentStartLatencyTerminalProof, error) {
	proof := agentStartLatencyTerminalProof{}
	suspendOut, suspendErr := runGCWithTimeout(30*time.Second, liveEnv, h.cityDir, "session", "suspend", sessionID)
	var retireErr error
	if suspendErr == nil {
		_, retireErr = front.Close(sessionID, "drained", time.Now().UTC())
	}
	var (
		lastInfo sessionpkg.Info
		lastErr  error
	)
	for {
		info, _, err := front.GetPersistedResponse(sessionID)
		if err != nil {
			lastErr = err
		} else {
			lastInfo = info
			proof.DurableSessionRetired = info.Closed && strings.TrimSpace(info.MetadataState) == string(sessionpkg.StateDrained)
		}
		exists, err := tmuxSessionExistsOnCitySocket(h.cityDir, sessionName)
		if err != nil {
			lastErr = err
		} else {
			proof.TmuxSessionAbsent = !exists
		}
		if proof.DurableSessionRetired && proof.TmuxSessionAbsent {
			at := time.Now().UTC()
			if suspendErr != nil {
				return &at, proof, fmt.Errorf("gc session suspend returned %v after converging: %s", suspendErr, strings.TrimSpace(suspendOut))
			}
			if retireErr != nil {
				return &at, proof, fmt.Errorf("retiring suspended benchmark session: %w", retireErr)
			}
			return &at, proof, nil
		}
		select {
		case <-ctx.Done():
			detail := fmt.Sprintf("last durable state closed=%t metadata_state=%q; tmux_absent=%t", lastInfo.Closed, lastInfo.MetadataState, proof.TmuxSessionAbsent)
			if lastErr != nil {
				detail += "; last error=" + lastErr.Error()
			}
			if suspendErr != nil {
				detail += "; suspend error=" + suspendErr.Error() + "; suspend output=" + strings.TrimSpace(suspendOut)
			}
			if retireErr != nil {
				detail += "; retirement error=" + retireErr.Error()
			}
			return nil, proof, fmt.Errorf("%w: %s", ctx.Err(), detail)
		case <-time.After(100 * time.Millisecond):
		}
	}
}

func (h *agentStartPerfHarness) cleanupWithoutStore(ctx context.Context, sessionID, sessionName string) (bool, error) {
	closeOut, closeErr := runGCWithTimeout(30*time.Second, liveEnv, h.cityDir, "session", "close", sessionID)
	var lastErr error
	for {
		exists, err := tmuxSessionExistsOnCitySocket(h.cityDir, sessionName)
		if err != nil {
			lastErr = err
		} else if !exists {
			if closeErr != nil {
				return true, fmt.Errorf("gc session close returned %v after tmux disappeared: %s", closeErr, strings.TrimSpace(closeOut))
			}
			return true, nil
		}
		select {
		case <-ctx.Done():
			if lastErr != nil {
				return false, fmt.Errorf("%w: %v", ctx.Err(), lastErr)
			}
			return false, ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
}

func TestAgentStartPerfParsesOnlyCorrelatedCommitTrace(t *testing.T) {
	payload := []byte(`{
		"schema_version":"1",
		"records":[
			{"record_type":"operation","site_code":"lifecycle.start.commit","session_bead_id":"other","outcome_code":"success","fields":{"session_id":"other","duration_ns":99,"effect_applied":true}},
			{"record_type":"operation","site_code":"lifecycle.start.commit","session_bead_id":"session-1","outcome_code":"success","fields":{"session_id":"session-1","duration_ns":12000000000,"start_call_ns":9000000000,"zombie_recycle_ns":100000000,"state_sync_recovery_ns":200000000,"post_start_observe_ns":2000000000,"commit_refresh_ns":300000000,"effect_applied":true}}
		]
	}`)

	got, err := parseAgentStartCommitTrace(payload, "session-1")
	if err != nil {
		t.Fatal(err)
	}
	want := agentStartLatencyControllerTiming{
		SessionID:         "session-1",
		Total:             12 * time.Second,
		StartCall:         9 * time.Second,
		ZombieRecycle:     100 * time.Millisecond,
		StateSyncRecovery: 200 * time.Millisecond,
		PostStartObserve:  2 * time.Second,
		CommitRefresh:     300 * time.Millisecond,
	}
	if got != want {
		t.Fatalf("controller timing = %+v, want %+v", got, want)
	}
}

func TestAgentStartPerfRecognizesEmptyTmuxListResult(t *testing.T) {
	for _, output := range []string{
		"no server running on /tmp/tmux.sock",
		"failed to connect to server",
		"error connecting to /tmp/tmux.sock",
		"no current target",
	} {
		if !agentStartPerfEmptyTmuxListResult(1, output) {
			t.Errorf("exit 1 output %q was not recognized as an empty tmux server", output)
		}
	}
	if agentStartPerfEmptyTmuxListResult(2, "no current target") {
		t.Fatal("nonstandard tmux exit code was recognized as an empty server")
	}
	if agentStartPerfEmptyTmuxListResult(1, "permission denied") {
		t.Fatal("unrelated tmux failure was recognized as an empty server")
	}
}

func TestAgentStartPerfListPanesTreatsMissingSocketAsEmpty(t *testing.T) {
	panes, err := agentStartPerfListPanes(t.Context(), "agent-start-missing-"+strconv.FormatInt(time.Now().UnixNano(), 10))
	if err != nil {
		t.Fatal(err)
	}
	if len(panes) != 0 {
		t.Fatalf("panes = %+v, want none", panes)
	}
}

func TestAgentStartPerfObservesPromptFirstOutputAndIdleCompletion(t *testing.T) {
	promptAt := time.Date(2026, 8, 2, 12, 0, 1, 0, time.UTC)
	firstOutputAt := promptAt.Add(2 * time.Second)
	snapshot := &workerpkg.HistorySnapshot{
		Entries: []workerpkg.HistoryEntry{
			{Actor: workerpkg.ActorUser, Timestamp: &promptAt, Text: "Create the exact startup proof file."},
			{Actor: workerpkg.ActorAssistant, Timestamp: &firstOutputAt, Blocks: []workerpkg.HistoryBlock{{Kind: workerpkg.BlockKindToolUse, Name: "write_file"}}},
		},
		TailState: workerpkg.TailState{Activity: workerpkg.TailActivityIdle},
	}

	got := observeAgentStartTranscript(snapshot, "exact startup proof", firstOutputAt.Add(time.Second))
	if got.PromptAt == nil || !got.PromptAt.Equal(promptAt) {
		t.Fatalf("prompt timestamp = %v, want %v", got.PromptAt, promptAt)
	}
	if got.FirstAssistantOutputAt == nil || !got.FirstAssistantOutputAt.Equal(firstOutputAt) {
		t.Fatalf("first output timestamp = %v, want %v", got.FirstAssistantOutputAt, firstOutputAt)
	}
	if !got.AssistantAfterPrompt || !got.TranscriptIdle || !got.NoOpenToolUse || !got.NoPendingInteraction {
		t.Fatalf("terminal observation = %+v, want complete idle transcript proof", got)
	}
}

func TestAgentStartPerfRecognizesProviderDescendant(t *testing.T) {
	processes := []agentStartObservedProcess{
		{PID: 100, PPID: 1, Command: "zsh"},
		{PID: 200, PPID: 100, Command: "env"},
		{PID: 300, PPID: 200, Command: "node", Args: "node /opt/provider/claude"},
	}
	if !agentStartProcessTreeContains(100, []string{"claude", "node"}, processes) {
		t.Fatal("provider descendant was not recognized")
	}
	if agentStartProcessTreeContains(100, []string{"codex"}, processes) {
		t.Fatal("unrelated provider was recognized")
	}
}

func TestAgentStartPerfSetsRequiredReconcilerInExistingDaemonSection(t *testing.T) {
	input := "[workspace]\nname = \"perf\"\n\n[daemon]\nformula_v2 = true\nsession_reconciler = \"off\"\n\n[orders]\n"
	want := "[workspace]\nname = \"perf\"\n\n[daemon]\nformula_v2 = true\nsession_reconciler = \"require\"\n\n[orders]\n"
	got, err := withRequiredSessionReconciler(input)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("updated city config:\n%s\nwant:\n%s", got, want)
	}
	if gotAgain, err := withRequiredSessionReconciler(got); err != nil || gotAgain != want {
		t.Fatalf("idempotent update = %q, %v", gotAgain, err)
	}
}

type agentStartTraceShow struct {
	Records []agentStartTraceRecord `json:"records"`
}

type agentStartTraceRecord struct {
	RecordType    string                     `json:"record_type"`
	SiteCode      string                     `json:"site_code"`
	SessionBeadID string                     `json:"session_bead_id"`
	OutcomeCode   string                     `json:"outcome_code"`
	Fields        map[string]json.RawMessage `json:"fields"`
}

func parseAgentStartCommitTrace(output []byte, sessionID string) (agentStartLatencyControllerTiming, error) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return agentStartLatencyControllerTiming{}, fmt.Errorf("session id is empty")
	}
	payload := output
	if payloads := jsonPayloads(string(output)); len(payloads) > 0 {
		payload = payloads[len(payloads)-1]
	}
	var shown agentStartTraceShow
	if err := json.Unmarshal(payload, &shown); err != nil {
		return agentStartLatencyControllerTiming{}, fmt.Errorf("decode trace show: %w", err)
	}

	var matches []agentStartLatencyControllerTiming
	for _, record := range shown.Records {
		if record.RecordType != "operation" || record.SiteCode != "lifecycle.start.commit" ||
			strings.TrimSpace(record.SessionBeadID) != sessionID || record.OutcomeCode != "success" {
			continue
		}
		fieldSessionID, err := agentStartTraceString(record.Fields, "session_id")
		if err != nil || fieldSessionID != sessionID {
			continue
		}
		effectApplied, err := agentStartTraceBool(record.Fields, "effect_applied")
		if err != nil || !effectApplied {
			continue
		}
		timing := agentStartLatencyControllerTiming{SessionID: sessionID}
		for key, dst := range map[string]*time.Duration{
			"duration_ns":            &timing.Total,
			"start_call_ns":          &timing.StartCall,
			"zombie_recycle_ns":      &timing.ZombieRecycle,
			"state_sync_recovery_ns": &timing.StateSyncRecovery,
			"post_start_observe_ns":  &timing.PostStartObserve,
			"commit_refresh_ns":      &timing.CommitRefresh,
		} {
			ns, parseErr := agentStartTraceInt64(record.Fields, key)
			if parseErr != nil {
				return agentStartLatencyControllerTiming{}, parseErr
			}
			*dst = time.Duration(ns)
		}
		if timing.Total <= 0 || timing.StartCall < 0 || timing.ZombieRecycle < 0 || timing.StateSyncRecovery < 0 || timing.PostStartObserve < 0 || timing.CommitRefresh < 0 {
			return agentStartLatencyControllerTiming{}, fmt.Errorf("trace for session %q has invalid durations: %+v", sessionID, timing)
		}
		matches = append(matches, timing)
	}
	if len(matches) != 1 {
		return agentStartLatencyControllerTiming{}, fmt.Errorf("correlated lifecycle.start.commit traces for session %q = %d, want 1", sessionID, len(matches))
	}
	return matches[0], nil
}

func agentStartTraceString(fields map[string]json.RawMessage, key string) (string, error) {
	raw, ok := fields[key]
	if !ok {
		return "", fmt.Errorf("trace field %q is missing", key)
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", fmt.Errorf("decode trace field %q: %w", key, err)
	}
	return strings.TrimSpace(value), nil
}

func agentStartTraceBool(fields map[string]json.RawMessage, key string) (bool, error) {
	raw, ok := fields[key]
	if !ok {
		return false, fmt.Errorf("trace field %q is missing", key)
	}
	var value bool
	if err := json.Unmarshal(raw, &value); err != nil {
		return false, fmt.Errorf("decode trace field %q: %w", key, err)
	}
	return value, nil
}

func agentStartTraceInt64(fields map[string]json.RawMessage, key string) (int64, error) {
	raw, ok := fields[key]
	if !ok {
		return 0, fmt.Errorf("trace field %q is missing", key)
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var value json.Number
	if err := dec.Decode(&value); err != nil {
		return 0, fmt.Errorf("decode trace field %q: %w", key, err)
	}
	ns, err := value.Int64()
	if err != nil {
		return 0, fmt.Errorf("decode trace field %q: %w", key, err)
	}
	return ns, nil
}

type agentStartTranscriptObservation struct {
	PromptAt               *time.Time
	FirstAssistantOutputAt *time.Time
	AssistantAfterPrompt   bool
	TranscriptIdle         bool
	NoOpenToolUse          bool
	NoPendingInteraction   bool
}

func observeAgentStartTranscript(snapshot *workerpkg.HistorySnapshot, prompt string, observedAt time.Time) agentStartTranscriptObservation {
	observation := agentStartTranscriptObservation{}
	if snapshot == nil {
		return observation
	}
	promptIndex := findEntryTextIndex(snapshot.Entries, 0, strings.TrimSpace(prompt))
	if promptIndex >= 0 {
		observation.PromptAt = agentStartEntryTimestamp(snapshot.Entries[promptIndex], observedAt)
		for _, entry := range snapshot.Entries[promptIndex+1:] {
			if entry.Actor != workerpkg.ActorAssistant || (strings.TrimSpace(entry.Text) == "" && len(entry.Blocks) == 0) {
				continue
			}
			observation.AssistantAfterPrompt = true
			observation.FirstAssistantOutputAt = agentStartEntryTimestamp(entry, observedAt)
			break
		}
	}
	observation.TranscriptIdle = snapshot.TailState.Activity == workerpkg.TailActivityIdle
	observation.NoOpenToolUse = len(snapshot.TailState.OpenToolUseIDs) == 0
	observation.NoPendingInteraction = len(snapshot.TailState.PendingInteractionIDs) == 0
	return observation
}

func agentStartEntryTimestamp(entry workerpkg.HistoryEntry, fallback time.Time) *time.Time {
	if entry.Timestamp != nil && !entry.Timestamp.IsZero() {
		at := entry.Timestamp.UTC()
		return &at
	}
	at := fallback.UTC()
	return &at
}

type agentStartObservedProcess struct {
	PID     int
	PPID    int
	Command string
	Args    string
}

func agentStartProcessTreeContains(rootPID int, names []string, processes []agentStartObservedProcess) bool {
	wanted := make(map[string]struct{}, len(names))
	for _, name := range names {
		if normalized := agentStartProcessName(name); normalized != "" {
			wanted[normalized] = struct{}{}
		}
	}
	if rootPID <= 0 || len(wanted) == 0 {
		return false
	}
	byPID := make(map[int]agentStartObservedProcess, len(processes))
	children := make(map[int][]int)
	for _, process := range processes {
		byPID[process.PID] = process
		children[process.PPID] = append(children[process.PPID], process.PID)
	}
	queue := []int{rootPID}
	seen := make(map[int]struct{}, len(processes))
	for len(queue) > 0 {
		pid := queue[0]
		queue = queue[1:]
		if _, ok := seen[pid]; ok {
			continue
		}
		seen[pid] = struct{}{}
		if process, ok := byPID[pid]; ok && agentStartProcessMatches(process, wanted) {
			return true
		}
		queue = append(queue, children[pid]...)
	}
	return false
}

func agentStartProcessMatches(process agentStartObservedProcess, wanted map[string]struct{}) bool {
	if _, ok := wanted[agentStartProcessName(process.Command)]; ok {
		return true
	}
	for _, arg := range strings.Fields(process.Args) {
		if _, ok := wanted[agentStartProcessName(strings.Trim(arg, "'\""))]; ok {
			return true
		}
	}
	return false
}

func agentStartProcessName(value string) string {
	return strings.ToLower(strings.TrimSpace(filepath.Base(value)))
}

func withRequiredSessionReconciler(input string) (string, error) {
	if _, err := config.Parse([]byte(input)); err != nil {
		return "", fmt.Errorf("parse city config before setting required reconciler: %w", err)
	}
	lines := strings.Split(input, "\n")
	daemonStart, daemonEnd := -1, len(lines)
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "[daemon]" {
			daemonStart = i
			continue
		}
		if daemonStart >= 0 && i > daemonStart && strings.HasPrefix(trimmed, "[") {
			daemonEnd = i
			break
		}
	}
	if daemonStart < 0 {
		if len(lines) > 0 && lines[len(lines)-1] == "" {
			lines = lines[:len(lines)-1]
		}
		lines = append(lines, "", "[daemon]", `session_reconciler = "require"`, "")
	} else {
		updated := false
		for i := daemonStart + 1; i < daemonEnd; i++ {
			left, _, found := strings.Cut(lines[i], "=")
			if !found || strings.TrimSpace(left) != "session_reconciler" {
				continue
			}
			indent := lines[i][:len(lines[i])-len(strings.TrimLeft(lines[i], " \t"))]
			lines[i] = indent + `session_reconciler = "require"`
			updated = true
			break
		}
		if !updated {
			lines = append(lines[:daemonEnd], append([]string{`session_reconciler = "require"`}, lines[daemonEnd:]...)...)
		}
	}
	output := strings.Join(lines, "\n")
	cfg, err := config.Parse([]byte(output))
	if err != nil {
		return "", fmt.Errorf("parse city config after setting required reconciler: %w", err)
	}
	if cfg.Daemon.SessionReconciler != "require" {
		return "", fmt.Errorf("session reconciler mode = %q, want require", cfg.Daemon.SessionReconciler)
	}
	return output, nil
}
