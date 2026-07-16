//go:build integration

package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/events"
	"github.com/gastownhall/gascity/internal/nudgequeue"
	"github.com/gastownhall/gascity/internal/reconcilekey"
	"github.com/gastownhall/gascity/internal/runtime"
	tmuxruntime "github.com/gastownhall/gascity/internal/runtime/tmux"
	"github.com/gastownhall/gascity/internal/session"
	"github.com/gastownhall/gascity/internal/testutil"
	"github.com/gastownhall/gascity/test/tmuxtest"
)

const authenticatedTmuxNudgeRetryDelay = 250 * time.Millisecond

func TestAuthenticatedKeyedNudgeRealTmuxRetriesUnsafeInteractionExactlyOnce(t *testing.T) {
	tmuxtest.RequireTmux(t)

	for _, interaction := range []string{"human-attached", "copy-mode"} {
		t.Run(interaction, func(t *testing.T) {
			guard := tmuxtest.NewGuard(t)
			sessionName := guard.SessionName("worker")
			launchIdentity := "launch-" + strings.ReplaceAll(interaction, "-", "_")
			capturePath, readyPath := startNudgeCaptureSession(t, guard, sessionName, launchIdentity)

			cfg := tmuxruntime.DefaultConfig()
			cfg.SocketName = guard.SocketName()
			cfg.DebounceMs = 0
			cfg.NudgeLockTimeout = 2 * time.Second
			rawProvider := tmuxruntime.NewProviderWithConfig(cfg)
			provider := newCountingNudgeEffectProvider(rawProvider)
			releaseInteraction := blockRealTmuxNudgeInteraction(t, guard.SocketName(), sessionName, interaction)
			t.Cleanup(releaseInteraction)

			binding := newRestartableProductionNudgeBinding(t)
			harness := newProductionNudgeAdmissionHTTPHarness(t, func(city string) (productionSessionNudgeAuthority, bool) {
				if city != "city-a" {
					t.Fatalf("admission resolver city = %q, want city-a", city)
				}
				return binding.current(), true
			}, true)
			response := harness.serve(t, "city-a", "write-key", "grant-"+interaction)
			if response.Code != http.StatusAccepted {
				t.Fatalf("authenticated nudge admission status = %d body=%s, want 202", response.Code, response.Body.String())
			}

			command := singleRepositoryCommand(t, binding.repository)
			clock := &mutableNudgeAcceptanceClock{now: command.CreatedAt.Add(time.Millisecond)}
			ids := &nudgeAcceptanceIDs{}
			targets := &productionNudgeEffectTargets{store: &nudgeSessionTargetStoreFake{info: session.Info{
				ID:            command.Target.SessionID,
				Generation:    fmt.Sprint(command.Target.IntentGeneration),
				SessionKey:    command.Target.ContinuationIdentity,
				InstanceToken: launchIdentity,
				SessionName:   sessionName,
				Provider:      "tmux",
				Transport:     "tmux-cli",
			}}}
			owner, key := newRealTmuxNudgeOwner(t, binding.current(), targets, provider, clock, ids, "tmux-owner-1")

			first := owner.reconcile(t.Context(), key, nudgeReconcileBatch{
				Causes:          nudgeCauseCommandCommit,
				FirstEnqueuedAt: clock.now,
			})
			assertValidNudgeAcceptanceOutcome(t, first)
			retrying := repositoryCommand(t, binding.repository, command.ID)
			wantDeadline := clock.now.Add(authenticatedTmuxNudgeRetryDelay)
			if first.disposition != nudgeReconcileOutcomeRetryDeadline || !first.notBefore.Equal(wantDeadline) {
				t.Fatalf("blocked reconcile outcome = %#v, want exact retry deadline %v", first, wantDeadline)
			}
			if retrying.State != nudgequeue.CommandStatePending || retrying.Claim != nil || retrying.Terminal != nil ||
				retrying.Retry == nil || retrying.Retry.NextEligibleAt == nil || !retrying.Retry.NextEligibleAt.Equal(wantDeadline) {
				t.Fatalf("blocked durable command = %#v, want pending exact retry at %v", retrying, wantDeadline)
			}
			firstAttemptID := retrying.Retry.AttemptID
			if got := provider.callCount(); got != 1 {
				t.Fatalf("classified provider attempts while blocked = %d, want 1", got)
			}
			assertCapturedNudges(t, capturePath, nil)

			duplicate := owner.reconcile(t.Context(), key, nudgeReconcileBatch{
				Causes:          nudgeCauseRuntimeReadiness,
				WorkqueueReplay: true,
			})
			assertValidNudgeAcceptanceOutcome(t, duplicate)
			if duplicate.disposition != nudgeReconcileOutcomeRetryDeadline || !duplicate.notBefore.Equal(wantDeadline) {
				t.Fatalf("duplicate wake outcome = %#v, want unchanged deadline %v", duplicate, wantDeadline)
			}
			if got := provider.callCount(); got != 1 {
				t.Fatalf("provider attempts after duplicate pre-deadline wake = %d, want 1", got)
			}
			assertCapturedNudges(t, capturePath, nil)

			binding.restart(t)
			restartedOwner, restartedKey := newRealTmuxNudgeOwner(t, binding.current(), targets, provider, clock, ids, "tmux-owner-2")
			restarted := restartedOwner.reconcile(t.Context(), restartedKey, nudgeReconcileBatch{
				Causes:          nudgeCauseCommandCommit | nudgeCauseRuntimeReadiness,
				WorkqueueReplay: true,
			})
			assertValidNudgeAcceptanceOutcome(t, restarted)
			if restarted.disposition != nudgeReconcileOutcomeRetryDeadline || !restarted.notBefore.Equal(wantDeadline) {
				t.Fatalf("restart outcome = %#v, want recovered deadline %v", restarted, wantDeadline)
			}
			if got := provider.callCount(); got != 1 {
				t.Fatalf("provider attempts after restart before deadline = %d, want 1", got)
			}
			assertCapturedNudges(t, capturePath, nil)

			releaseInteraction()
			clock.now = wantDeadline
			completedOutcome := restartedOwner.reconcile(t.Context(), restartedKey, nudgeReconcileBatch{Causes: nudgeCauseRetryDeadline})
			assertValidNudgeAcceptanceOutcome(t, completedOutcome)
			if completedOutcome.disposition != nudgeReconcileOutcomeForget {
				t.Fatalf("released reconcile outcome = %#v, want forget after terminal evidence", completedOutcome)
			}
			completed := repositoryCommand(t, binding.repository, command.ID)
			if completed.State != nudgequeue.CommandStateInjectedUnconfirmed || completed.Terminal == nil || completed.Retry == nil ||
				completed.Retry.AttemptCount != 2 || completed.Retry.AttemptID == firstAttemptID ||
				completed.Terminal.AttemptID != completed.Retry.AttemptID ||
				completed.Terminal.ActionResult != nudgequeue.CommandActionResultInjectedUnconfirmed {
				t.Fatalf("released durable command = %#v, want one accepted second attempt", completed)
			}
			if got := provider.callCount(); got != 2 {
				t.Fatalf("classified provider attempts after release = %d, want blocked attempt plus one accepted attempt", got)
			}
			awaitCapturedNudges(t, capturePath, []string{command.Message})

			postTerminalDuplicate := restartedOwner.reconcile(t.Context(), restartedKey, nudgeReconcileBatch{
				Causes:          nudgeCauseProviderResult | nudgeCauseRetryDeadline,
				WorkqueueReplay: true,
			})
			assertValidNudgeAcceptanceOutcome(t, postTerminalDuplicate)
			binding.restart(t)
			finalOwner, finalKey := newRealTmuxNudgeOwner(t, binding.current(), targets, provider, clock, ids, "tmux-owner-3")
			postTerminalRestart := finalOwner.reconcile(t.Context(), finalKey, nudgeReconcileBatch{
				Causes:          nudgeCauseCommandCommit | nudgeCauseProviderResult,
				WorkqueueReplay: true,
			})
			assertValidNudgeAcceptanceOutcome(t, postTerminalRestart)
			if got := provider.callCount(); got != 2 {
				t.Fatalf("provider attempts after terminal duplicate and restart = %d, want 2", got)
			}
			assertCapturedNudges(t, capturePath, []string{command.Message})

			legacyRelease := blockRealTmuxNudgeInteraction(t, guard.SocketName(), sessionName, interaction)
			t.Cleanup(legacyRelease)
			legacyMessage := "legacy-baseline-" + interaction
			if err := rawProvider.NudgeNow(sessionName, runtime.TextContent(legacyMessage)); err != nil {
				t.Fatalf("legacy NudgeNow while %s: %v", interaction, err)
			}
			awaitCapturedNudges(t, capturePath, []string{command.Message, legacyMessage})
			legacyRelease()
			if got := provider.callCount(); got != 2 {
				t.Fatalf("classified provider attempts after legacy comparison = %d, want unchanged 2", got)
			}
			if _, err := os.Stat(readyPath); err != nil {
				t.Fatalf("capture process lost readiness marker: %v", err)
			}
		})
	}
}

type restartableProductionNudgeBinding struct {
	cityPath   string
	store      *nudgeCommandSourceAtomicStore
	repository *nudgequeue.CommandRepository
	binding    *productionNudgeAuthorityBinding
}

func newRestartableProductionNudgeBinding(t *testing.T) *restartableProductionNudgeBinding {
	t.Helper()
	store := newNudgeCommandSourceAtomicStore()
	repository, err := nudgequeue.NewCommandRepository(store, nudgequeue.NewRestoreAnchorRepositoryVerifier(t.TempDir()))
	if err != nil {
		t.Fatalf("NewCommandRepository: %v", err)
	}
	if _, err := repository.Provision(t.Context()); err != nil {
		t.Fatalf("Provision: %v", err)
	}
	fixture := &restartableProductionNudgeBinding{cityPath: t.TempDir(), store: store, repository: repository}
	fixture.open(t)
	t.Cleanup(func() {
		if fixture.binding != nil {
			_ = fixture.binding.Close()
		}
	})
	return fixture
}

func (f *restartableProductionNudgeBinding) current() *productionNudgeAuthorityBinding {
	return f.binding
}

func (f *restartableProductionNudgeBinding) restart(t *testing.T) {
	t.Helper()
	if err := f.binding.Close(); err != nil {
		t.Fatalf("close production nudge binding for restart: %v", err)
	}
	f.open(t)
}

func (f *restartableProductionNudgeBinding) open(t *testing.T) {
	t.Helper()
	binding, err := bindProductionNudgeAuthority(t.Context(), f.cityPath, "city-a", f.store, f.repository)
	if err != nil {
		t.Fatalf("bindProductionNudgeAuthority: %v", err)
	}
	f.binding = binding
}

func newRealTmuxNudgeOwner(
	t *testing.T,
	binding *productionNudgeAuthorityBinding,
	targets nudgeEffectTargetReader,
	provider runtime.Provider,
	clock *mutableNudgeAcceptanceClock,
	ids *nudgeAcceptanceIDs,
	ownerID string,
) (*nudgeKeyEffectOwner, reconcilekey.Session) {
	t.Helper()
	reader, err := newNudgeKeyReadShadow(t.Context(), binding.source, 8, nil)
	if err != nil {
		t.Fatalf("newNudgeKeyReadShadow: %v", err)
	}
	owner, err := newNudgeKeyEffectOwner(nudgeKeyEffectOwnerConfig{
		reader:            reader,
		source:            binding.source,
		authorizer:        binding.claimAuthorizer,
		targets:           targets,
		handles:           &productionNudgeEffectHandles{provider: provider, recorder: events.Discard},
		ownerID:           ownerID,
		now:               clock.Now,
		newID:             ids.New,
		claimLease:        time.Minute,
		completionTimeout: 5 * time.Second,
		retryDelay:        authenticatedTmuxNudgeRetryDelay,
	})
	if err != nil {
		t.Fatalf("newNudgeKeyEffectOwner: %v", err)
	}
	key, err := reader.key("session-1")
	if err != nil {
		t.Fatalf("reader.key: %v", err)
	}
	return owner, key
}

type mutableNudgeAcceptanceClock struct{ now time.Time }

func (c *mutableNudgeAcceptanceClock) Now() time.Time { return c.now }

type nudgeAcceptanceIDs struct{ sequence atomic.Uint64 }

func (g *nudgeAcceptanceIDs) New(kind string) (string, error) {
	return fmt.Sprintf("%s-%d", kind, g.sequence.Add(1)), nil
}

type countingNudgeEffectProvider struct {
	runtime.Provider
	effect runtime.NudgeEffectProvider

	mu    sync.Mutex
	calls int
}

func newCountingNudgeEffectProvider(provider *tmuxruntime.Provider) *countingNudgeEffectProvider {
	return &countingNudgeEffectProvider{Provider: provider, effect: provider}
}

func (p *countingNudgeEffectProvider) NudgeEffect(ctx context.Context, name string, request runtime.NudgeEffectRequest) (runtime.NudgeEffectResult, error) {
	p.mu.Lock()
	p.calls++
	p.mu.Unlock()
	return p.effect.NudgeEffect(ctx, name, request)
}

func (p *countingNudgeEffectProvider) callCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls
}

func startNudgeCaptureSession(t *testing.T, guard *tmuxtest.Guard, sessionName, launchIdentity string) (string, string) {
	t.Helper()
	dir := t.TempDir()
	capturePath := filepath.Join(dir, "nudges.log")
	readyPath := filepath.Join(dir, "ready")
	scriptPath := filepath.Join(dir, "capture.sh")
	script := "#!/bin/sh\nset -eu\nstty -echo\n: > \"$GC_NUDGE_CAPTURE\"\n: > \"$GC_NUDGE_READY\"\nwhile IFS= read -r line; do\n  printf '%s\\n' \"$line\" >> \"$GC_NUDGE_CAPTURE\"\ndone\n"
	if err := os.WriteFile(scriptPath, []byte(script), 0o700); err != nil {
		t.Fatalf("write capture script: %v", err)
	}
	cfg := tmuxruntime.DefaultConfig()
	cfg.SocketName = guard.SocketName()
	tm := tmuxruntime.NewProviderWithConfig(cfg).Tmux()
	if err := tm.NewSessionWithCommandAndEnv(sessionName, dir, scriptPath, map[string]string{
		"GC_INSTANCE_TOKEN": launchIdentity,
		"GC_NUDGE_CAPTURE":  capturePath,
		"GC_NUDGE_READY":    readyPath,
		"GC_PROVIDER":       "codex",
	}); err != nil {
		t.Fatalf("start isolated tmux capture session: %v", err)
	}
	awaitFile(t, readyPath)
	return capturePath, readyPath
}

func blockRealTmuxNudgeInteraction(t *testing.T, socketName, sessionName, interaction string) func() {
	t.Helper()
	var once sync.Once
	switch interaction {
	case "human-attached":
		cmd := exec.Command("tmux", "-C", "-L", socketName, "attach-session", "-t", sessionName)
		stdin, err := cmd.StdinPipe()
		if err != nil {
			t.Fatalf("create isolated tmux control-client stdin: %v", err)
		}
		cmd.Stdout = io.Discard
		cmd.Stderr = io.Discard
		if err := cmd.Start(); err != nil {
			t.Fatalf("start isolated tmux control client: %v", err)
		}
		done := make(chan error, 1)
		go func() { done <- cmd.Wait() }()
		awaitTmuxFormat(t, socketName, sessionName, "#{session_attached}", "1")
		return func() {
			once.Do(func() {
				_, _ = runIsolatedTmux(t.Context(), socketName, "detach-client", "-s", sessionName)
				_ = stdin.Close()
				select {
				case <-done:
				case <-time.After(testutil.GoroutineRaceTimeout):
					_ = cmd.Process.Kill()
					<-done
				}
				awaitTmuxFormat(t, socketName, sessionName, "#{session_attached}", "0")
			})
		}
	case "copy-mode":
		if _, err := runIsolatedTmux(t.Context(), socketName, "copy-mode", "-t", sessionName); err != nil {
			t.Fatalf("enter isolated tmux copy mode: %v", err)
		}
		awaitTmuxFormat(t, socketName, sessionName, "#{pane_in_mode}", "1")
		return func() {
			once.Do(func() {
				mode, _ := runIsolatedTmux(t.Context(), socketName, "display-message", "-t", sessionName, "-p", "#{pane_in_mode}")
				if mode != "0" {
					if _, err := runIsolatedTmux(t.Context(), socketName, "send-keys", "-t", sessionName, "-X", "cancel"); err != nil {
						t.Fatalf("leave isolated tmux copy mode: %v", err)
					}
				}
				awaitTmuxFormat(t, socketName, sessionName, "#{pane_in_mode}", "0")
			})
		}
	default:
		t.Fatalf("unknown real tmux interaction %q", interaction)
		return func() {}
	}
}

func runIsolatedTmux(ctx context.Context, socketName string, args ...string) (string, error) {
	allArgs := append([]string{"-L", socketName}, args...)
	output, err := exec.CommandContext(ctx, "tmux", allArgs...).CombinedOutput()
	return strings.TrimSpace(string(output)), err
}

func awaitTmuxFormat(t *testing.T, socketName, sessionName, format, want string) {
	t.Helper()
	deadline := time.NewTimer(testutil.GoroutineRaceTimeout)
	defer deadline.Stop()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		got, err := runIsolatedTmux(t.Context(), socketName, "display-message", "-t", sessionName, "-p", format)
		if err == nil && got == want {
			return
		}
		select {
		case <-ticker.C:
		case <-deadline.C:
			t.Fatalf("tmux %s = %q, err=%v; want %q", format, got, err, want)
		}
	}
}

func awaitFile(t *testing.T, path string) {
	t.Helper()
	deadline := time.NewTimer(testutil.GoroutineRaceTimeout)
	defer deadline.Stop()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		if _, err := os.Stat(path); err == nil {
			return
		}
		select {
		case <-ticker.C:
		case <-deadline.C:
			t.Fatalf("timed out waiting for %s", path)
		}
	}
}

func awaitCapturedNudges(t *testing.T, path string, want []string) {
	t.Helper()
	deadline := time.NewTimer(testutil.GoroutineRaceTimeout)
	defer deadline.Stop()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		got := capturedNudges(t, path)
		if equalNudgeLines(got, want) {
			return
		}
		select {
		case <-ticker.C:
		case <-deadline.C:
			t.Fatalf("captured nudges = %#v, want %#v", got, want)
		}
	}
}

func assertCapturedNudges(t *testing.T, path string, want []string) {
	t.Helper()
	if got := capturedNudges(t, path); !equalNudgeLines(got, want) {
		t.Fatalf("captured nudges = %#v, want %#v", got, want)
	}
}

func capturedNudges(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read captured nudges: %v", err)
	}
	text := strings.TrimSuffix(string(data), "\n")
	if text == "" {
		return nil
	}
	return strings.Split(text, "\n")
}

func equalNudgeLines(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

func singleRepositoryCommand(t *testing.T, repository *nudgequeue.CommandRepository) nudgequeue.Command {
	t.Helper()
	snapshot, err := repository.Snapshot(t.Context(), nudgequeue.MaxCommandRepositorySnapshotCommands)
	if err != nil {
		t.Fatalf("repository Snapshot: %v", err)
	}
	if len(snapshot.Entries) != 1 || snapshot.Entries[0].Command == nil {
		t.Fatalf("repository entries = %#v, want one known command", snapshot.Entries)
	}
	return *snapshot.Entries[0].Command
}

func repositoryCommand(t *testing.T, repository *nudgequeue.CommandRepository, commandID string) nudgequeue.Command {
	t.Helper()
	resolved, err := repository.Get(t.Context(), commandID)
	if err != nil {
		t.Fatalf("repository Get(%q): %v", commandID, err)
	}
	if !resolved.Found || resolved.Entry.Command == nil {
		t.Fatalf("repository Get(%q) = %#v, want known command", commandID, resolved)
	}
	return *resolved.Entry.Command
}

func assertValidNudgeAcceptanceOutcome(t *testing.T, outcome nudgeReconcileOutcome) {
	t.Helper()
	if err := outcome.validate(); err != nil {
		t.Fatalf("reconcile outcome is invalid: %v (outcome=%#v)", err, outcome)
	}
}

var _ runtime.NudgeEffectProvider = (*countingNudgeEffectProvider)(nil)
