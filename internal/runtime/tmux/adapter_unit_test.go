package tmux

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/gastownhall/gascity/internal/runtime"
)

func TestBuildLaunchCommandUnsetsColorKillersForInteractiveExecutables(t *testing.T) {
	for _, tc := range []struct {
		name     string
		provider string
		command  string
		want     string
	}{
		{name: "claude", provider: "claude", command: "claude", want: "env -u CI -u NO_COLOR claude"},
		{name: "claude alias", provider: "qlandia/claude", command: "claude", want: "env -u CI -u NO_COLOR claude"},
		{name: "claude without provider", command: "claude", want: "env -u CI -u NO_COLOR claude"},
		{name: "codex", provider: "codex", command: "codex", want: "env -u CI -u NO_COLOR codex"},
		{name: "kiro command", provider: "claude", command: "kiro-cli", want: "kiro-cli"},
		{name: "omp", provider: "omp", command: "omp", want: "omp"},
		{name: "custom", provider: "custom", command: "custom-agent", want: "custom-agent"},
		{name: "custom codex", provider: "custom-codex", command: "custom-codex", want: "custom-codex"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, _, err := buildLaunchCommand("worker", runtime.Config{Command: tc.command, ProviderName: tc.provider})
			if err != nil {
				t.Fatalf("buildLaunchCommand: %v", err)
			}
			if got != tc.want {
				t.Fatalf("command = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestBuildLaunchCommandColorWrapsLongPromptCommand(t *testing.T) {
	got, promptFile, err := buildLaunchCommand("worker", runtime.Config{
		Command:      "/opt/bin/claude",
		ProviderName: "kiro",
		WorkDir:      t.TempDir(),
		PromptSuffix: strings.Repeat("prompt ", maxInlinePromptLen),
	})
	if err != nil {
		t.Fatalf("buildLaunchCommand: %v", err)
	}
	if promptFile == "" {
		t.Fatal("long prompt did not create a prompt file")
	}
	if !strings.HasPrefix(got, "env -u CI -u NO_COLOR sh -c ") {
		t.Fatalf("command = %q, want env wrapper around final sh -c command", got)
	}
}

func TestProviderAttachRefusesDeadPane(t *testing.T) {
	fe := &fakeExecutor{
		outs: []string{"", "1"},
	}
	p := NewProviderWithConfig(Config{SocketName: "x"})
	p.tm.exec = fe

	err := p.Attach("runner")
	if err == nil {
		t.Fatal("Attach = nil, want dead pane error")
	}
	if !strings.Contains(err.Error(), "dead pane") {
		t.Fatalf("Attach error = %v, want dead pane context", err)
	}
	for _, call := range fe.calls {
		if strings.Contains(strings.Join(call, " "), "attach-session") {
			t.Fatalf("Attach attempted tmux attach-session for dead pane: %v", fe.calls)
		}
	}
}

func TestProviderAttachMissingSessionWrapsRuntimeSentinel(t *testing.T) {
	fe := &fakeExecutor{
		err: ErrSessionNotFound,
	}
	p := NewProviderWithConfig(Config{SocketName: "x"})
	p.tm.exec = fe

	err := p.Attach("runner")
	if !errors.Is(err, runtime.ErrSessionNotFound) {
		t.Fatalf("Attach error = %v, want runtime.ErrSessionNotFound", err)
	}
	if !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("Attach error = %v, want tmux ErrSessionNotFound", err)
	}
	for _, call := range fe.calls {
		if strings.Contains(strings.Join(call, " "), "attach-session") {
			t.Fatalf("Attach attempted tmux attach-session for missing session: %v", fe.calls)
		}
	}
}

func TestProviderListRunningReportsPartialOnNoServer(t *testing.T) {
	fe := &fakeExecutor{err: ErrNoServer}
	p := NewProviderWithConfig(Config{SocketName: "x"})
	p.tm.exec = fe

	names, err := p.ListRunning("")
	if names != nil {
		t.Fatalf("ListRunning names = %v, want nil on unreachable server", names)
	}
	if !runtime.IsPartialListError(err) {
		t.Fatalf("ListRunning err = %v, want runtime.PartialListError so reconciler guards defer", err)
	}
	if !errors.Is(err, ErrNoServer) {
		t.Fatalf("ListRunning err = %v, want wrapped ErrNoServer cause", err)
	}
}

func TestProviderListRunningPropagatesNonServerError(t *testing.T) {
	sentinel := errors.New("tmux exploded")
	fe := &fakeExecutor{err: sentinel}
	p := NewProviderWithConfig(Config{SocketName: "x"})
	p.tm.exec = fe

	names, err := p.ListRunning("")
	if names != nil {
		t.Fatalf("ListRunning names = %v, want nil on error", names)
	}
	if runtime.IsPartialListError(err) {
		t.Fatalf("ListRunning err = %v, want a plain error (not partial) for a real tmux failure", err)
	}
	if !errors.Is(err, sentinel) {
		t.Fatalf("ListRunning err = %v, want the underlying tmux error", err)
	}
}

// TestListSessionsAbsorbsNoServer pins the tmux-internal contract that the
// change deliberately preserves: ListSessions still reports an unreachable
// server as an empty result so FindSessionByWorkDir and CleanupOrphanedSessions
// keep treating "server down" as "no sessions". Only Provider.ListRunning
// surfaces the outage as a PartialListError.
func TestListSessionsAbsorbsNoServer(t *testing.T) {
	fe := &fakeExecutor{err: ErrNoServer}
	tm := NewTmux()
	tm.exec = fe

	names, err := tm.ListSessions()
	if err != nil {
		t.Fatalf("ListSessions err = %v, want nil (no server absorbed)", err)
	}
	if names != nil {
		t.Fatalf("ListSessions names = %v, want nil", names)
	}
}

func TestProviderAttachReportsHasSessionError(t *testing.T) {
	fe := &fakeExecutor{
		err: errors.New("tmux unavailable"),
	}
	p := NewProviderWithConfig(Config{SocketName: "x"})
	p.tm.exec = fe

	err := p.Attach("runner")
	if err == nil {
		t.Fatal("Attach = nil, want has-session error")
	}
	if !strings.Contains(err.Error(), "checking tmux session before attach") {
		t.Fatalf("Attach error = %v, want checking context", err)
	}
	for _, call := range fe.calls {
		if strings.Contains(strings.Join(call, " "), "attach-session") {
			t.Fatalf("Attach attempted tmux attach-session after has-session error: %v", fe.calls)
		}
	}
}

func TestProviderAttachNamedSocketNoServerPreflightRefusesBeforeLaunchingTmux(t *testing.T) {
	for _, tc := range []struct {
		name        string
		observation error
		want        string
	}{
		{name: "missing-or-stale", want: "socket absent or stale"},
		{name: "live-socket-observation", observation: errors.New("live-unix-socket"), want: "live-unix-socket"},
		{name: "ambiguous-observation", observation: errors.New("permission denied"), want: "permission denied"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			binDir := t.TempDir()
			invocations := filepath.Join(binDir, "tmux-invocations")
			fakeTmux := filepath.Join(binDir, "tmux")
			if err := os.WriteFile(fakeTmux, []byte("#!/bin/sh\nprintf '%s\\n' \"$*\" >> "+invocations+"\n"), 0o755); err != nil {
				t.Fatalf("write fake tmux: %v", err)
			}
			t.Setenv("PATH", binDir)

			p := NewProviderWithConfig(Config{SocketName: "city-socket"})
			p.tm.exec = &fakeExecutor{
				outs: []string{"", "", ""},
				errs: []error{nil, nil, ErrNoServer},
			}
			p.tm.serverSocketObserver = func(context.Context, string) error {
				return tc.observation
			}

			err := p.Attach("runner")
			if !errors.Is(err, ErrServerDegraded) {
				t.Fatalf("Attach error = %v, want ErrServerDegraded", err)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Attach error = %q, want %q", err, tc.want)
			}
			if output, readErr := os.ReadFile(invocations); readErr == nil && len(output) != 0 {
				t.Fatalf("Attach launched tmux after named-socket preflight failed: %s", output)
			} else if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
				t.Fatalf("read fake tmux invocations: %v", readErr)
			}

			if got := len(p.tm.exec.(*fakeExecutor).calls); got != 3 {
				t.Fatalf("tmux preflight calls = %d, want has-session, pane check, and socket probe", got)
			}
		})
	}
}

func TestNamedSocketAttachPreflightRefusesUnavailableServer(t *testing.T) {
	newTmux := func(errs []error) *Tmux {
		return &Tmux{
			cfg:  Config{SocketName: "gc-test"},
			exec: &fakeExecutor{errs: errs},
			serverSocketObserver: func(context.Context, string) error {
				return nil
			},
		}
	}

	t.Run("direct", func(t *testing.T) {
		tm := newTmux([]error{ErrNoServer})
		err := tm.AttachSession("runner")
		if !errors.Is(err, ErrServerDegraded) {
			t.Fatalf("AttachSession error = %v, want ErrServerDegraded", err)
		}
		if got := len(tm.exec.(*fakeExecutor).calls); got != 1 {
			t.Fatalf("tmux calls = %d, want only attach preflight", got)
		}
	})

	t.Run("hidden", func(t *testing.T) {
		tm := newTmux([]error{nil, ErrNoServer})
		err := tm.ensureHiddenAttachedClient("runner")
		if !errors.Is(err, ErrServerDegraded) {
			t.Fatalf("ensureHiddenAttachedClient error = %v, want ErrServerDegraded", err)
		}
		if got := len(tm.exec.(*fakeExecutor).calls); got != 2 {
			t.Fatalf("tmux calls = %d, want attachment check and attach preflight", got)
		}
	})
}

func TestNamedSocketAttachPreflightSkipsDefaultSocket(t *testing.T) {
	fe := &fakeExecutor{}
	tm := &Tmux{exec: fe}
	if err := tm.probeServerAliveForAttach(); err != nil {
		t.Fatalf("probeServerAliveForAttach: %v", err)
	}
	if len(fe.calls) != 0 {
		t.Fatalf("default-socket attach preflight made tmux calls: %v", fe.calls)
	}
}

func TestAttachSessionNamedSocketUsesNoStartServer(t *testing.T) {
	fe := &fakeExecutor{errs: []error{ErrSessionNotFound, nil}}
	tm := &Tmux{cfg: Config{SocketName: "city-socket"}, exec: fe}
	if err := tm.AttachSession("runner"); err != nil {
		t.Fatalf("AttachSession: %v", err)
	}
	want := [][]string{
		{"-u", "-L", "city-socket", "-N", "has-session", "-t", "=" + probeSessionName},
		{"-u", "-L", "city-socket", "-N", "attach-session", "-t", "runner"},
	}
	if !reflect.DeepEqual(fe.calls, want) {
		t.Fatalf("tmux calls = %#v, want %#v", fe.calls, want)
	}
}

func TestProviderAttachNamedSocketUsesNoStartServer(t *testing.T) {
	binDir := t.TempDir()
	invocations := filepath.Join(binDir, "tmux-invocations")
	fakeTmux := filepath.Join(binDir, "tmux")
	if err := os.WriteFile(fakeTmux, []byte("#!/bin/sh\nprintf '%s\\n' \"$*\" >> "+invocations+"\n"), 0o755); err != nil {
		t.Fatalf("write fake tmux: %v", err)
	}
	t.Setenv("PATH", binDir)

	p := NewProviderWithConfig(Config{SocketName: "city-socket"})
	p.tm.exec = &fakeExecutor{errs: []error{nil, nil, ErrSessionNotFound}}
	if err := p.Attach("runner"); err != nil {
		t.Fatalf("Attach: %v", err)
	}
	wantPreflight := [][]string{
		{"-u", "-L", "city-socket", "-N", "has-session", "-t", "=runner"},
		{"-u", "-L", "city-socket", "-N", "display-message", "-t", "runner:^.0", "-p", "#{pane_dead}"},
		{"-u", "-L", "city-socket", "-N", "has-session", "-t", "=" + probeSessionName},
	}
	if !reflect.DeepEqual(p.tm.exec.(*fakeExecutor).calls, wantPreflight) {
		t.Fatalf("provider preflight argv = %#v, want %#v", p.tm.exec.(*fakeExecutor).calls, wantPreflight)
	}
	output, err := os.ReadFile(invocations)
	if err != nil {
		t.Fatalf("read fake tmux invocations: %v", err)
	}
	if got, want := string(output), "-u -N -L city-socket attach-session -t runner\n"; got != want {
		t.Fatalf("provider attach argv = %q, want %q", got, want)
	}
}
