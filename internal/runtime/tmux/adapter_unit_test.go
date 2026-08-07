package tmux

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
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

func TestProviderStopUnattendedSession(t *testing.T) {
	const (
		onePane = "$1\tworker\t@1\t%1\t0\t0\t0"
		twoPane = "$1\tworker\t@1\t%1\t0\t0\t0\n$1\tworker\t@2\t%2\t0\t0\t0"
	)
	sentinel := errors.New("tmux census failed")
	for _, test := range []struct {
		name               string
		before             string
		token              string
		after              string
		errs               []error
		wantOK             bool
		wantPaneIDs        []string
		emptyExpectedToken bool
		wantErrContains    string
		wantErrIs          error
		mustNotCallContain []string
	}{
		{name: "stable multiple windows and panes", before: twoPane, token: "GC_INSTANCE_TOKEN=token", after: twoPane, wantOK: true, wantPaneIDs: []string{"%1", "%2"}},
		{name: "attached client", before: "$1\tworker\t@1\t%1\t1\t0\t0", token: "GC_INSTANCE_TOKEN=token", after: onePane},
		{name: "multiple clients", before: "$1\tworker\t@1\t%1\t2\t0\t0", token: "GC_INSTANCE_TOKEN=token", after: onePane},
		{name: "linked window before token read", before: "$1\tworker\t@1\t%1\t0\t0\t1", token: "GC_INSTANCE_TOKEN=token", after: onePane, wantErrContains: "linked windows"},
		{name: "linked window after token read", before: onePane, token: "GC_INSTANCE_TOKEN=token", after: "$1\tworker\t@1\t%1\t0\t0\t1", wantErrContains: "linked windows"},
		{name: "copy mode on non-active pane", before: "$1\tworker\t@1\t%1\t0\t0\t0\n$1\tworker\t@2\t%2\t0\t1\t0", token: "GC_INSTANCE_TOKEN=token", after: twoPane},
		{name: "partial row", before: "$1\tworker\t@1\t%1\t0", token: "GC_INSTANCE_TOKEN=token", after: onePane},
		{name: "malformed count", before: "$1\tworker\t@1\t%1\tmany\t0\t0", token: "GC_INSTANCE_TOKEN=token", after: onePane},
		{name: "signed count", before: "$1\tworker\t@1\t%1\t+1\t0\t0", token: "GC_INSTANCE_TOKEN=token", after: onePane},
		{name: "duplicate pane", before: "$1\tworker\t@1\t%1\t0\t0\t0\n$1\tworker\t@1\t%1\t0\t0\t0", token: "GC_INSTANCE_TOKEN=token", after: onePane},
		{name: "mixed session IDs", before: "$1\tworker\t@1\t%1\t0\t0\t0\n$2\tworker\t@2\t%2\t0\t0\t0", token: "GC_INSTANCE_TOKEN=token", after: twoPane},
		{name: "wrong session name", before: "$1\treplacement\t@1\t%1\t0\t0\t0", token: "GC_INSTANCE_TOKEN=token", after: onePane},
		{name: "replacement between censuses", before: onePane, token: "GC_INSTANCE_TOKEN=token", after: "$2\tworker\t@1\t%1\t0\t0\t0"},
		{name: "pane topology replacement between censuses", before: onePane, token: "GC_INSTANCE_TOKEN=token", after: "$1\tworker\t@2\t%2\t0\t0\t0"},
		{name: "attachment after token read", before: onePane, token: "GC_INSTANCE_TOKEN=token", after: "$1\tworker\t@1\t%1\t1\t0\t0"},
		{name: "expected token missing", before: onePane, token: "GC_INSTANCE_TOKEN=token", after: onePane, emptyExpectedToken: true},
		{name: "token missing", before: onePane, token: "GC_INSTANCE_TOKEN=", after: onePane},
		{name: "token mismatch", before: onePane, token: "GC_INSTANCE_TOKEN=replacement", after: onePane},
		{name: "token read error", before: onePane, after: onePane, errs: []error{nil, sentinel}},
		{name: "first census error", errs: []error{sentinel}},
		{name: "second census error", before: onePane, token: "GC_INSTANCE_TOKEN=token", errs: []error{nil, nil, sentinel}},
		{name: "certified pane disappears", before: twoPane, token: "GC_INSTANCE_TOKEN=token", after: twoPane, errs: []error{nil, nil, nil, ErrSessionNotFound}, wantErrIs: ErrSessionNotFound, mustNotCallContain: []string{"%2", "kill-session"}},
		{name: "final exact session disappears", before: onePane, token: "GC_INSTANCE_TOKEN=token", after: onePane, errs: []error{nil, nil, nil, nil, ErrSessionNotFound}, wantOK: true, wantPaneIDs: []string{"%1"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			outputs := []string{test.before, test.token, test.after}
			if test.wantOK {
				for range test.wantPaneIDs {
					outputs = append(outputs, "999999999")
				}
				outputs = append(outputs, "")
			}
			fe := &fakeExecutor{outs: outputs, errs: test.errs}
			p := NewProviderWithConfig(Config{SocketName: "cert-socket"})
			p.tm.exec = fe

			expectedToken := "token"
			if test.emptyExpectedToken {
				expectedToken = ""
			}
			err := p.StopUnattendedSession("worker", expectedToken)
			if test.wantOK && err != nil {
				t.Fatalf("StopUnattendedSession: %v", err)
			}
			if !test.wantOK && err == nil {
				t.Fatal("StopUnattendedSession = nil, want fail-closed error")
			}
			if test.wantErrContains != "" && !strings.Contains(err.Error(), test.wantErrContains) {
				t.Fatalf("StopUnattendedSession error = %v, want %q", err, test.wantErrContains)
			}
			if test.wantErrIs != nil && !errors.Is(err, test.wantErrIs) {
				t.Fatalf("StopUnattendedSession error = %v, want wrapped %v", err, test.wantErrIs)
			}
			for _, call := range fe.calls {
				joined := strings.Join(call, " ")
				for _, forbidden := range test.mustNotCallContain {
					if strings.Contains(joined, forbidden) {
						t.Fatalf("StopUnattendedSession continued after certified-pane loss: %v", call)
					}
				}
				if strings.Contains(joined, "detach-client") {
					t.Fatalf("certification detached an untracked client: %v", call)
				}
				if strings.Contains(joined, "list-panes") {
					want := []string{
						"-u", "-L", "cert-socket", "list-panes", "-s", "-t", "=worker", "-F",
						"#{session_id}\t#{session_name}\t#{window_id}\t#{pane_id}\t#{session_attached}\t#{pane_in_mode}\t#{window_linked}",
					}
					if !reflect.DeepEqual(call, want) {
						t.Fatalf("census argv = %#v, want %#v", call, want)
					}
				}
			}
			if test.wantOK {
				wantTail := make([][]string, 0, len(test.wantPaneIDs)+1)
				for _, paneID := range test.wantPaneIDs {
					wantTail = append(wantTail, []string{"-u", "-L", "cert-socket", "display-message", "-t", paneID, "-p", "#{pane_pid}"})
				}
				wantTail = append(wantTail, []string{"-u", "-L", "cert-socket", "kill-session", "-t", "$1"})
				gotTail := fe.calls[len(fe.calls)-len(wantTail):]
				if !reflect.DeepEqual(gotTail, wantTail) {
					t.Fatalf("bound stop tail argv = %#v, want %#v", gotTail, wantTail)
				}
			}
		})
	}
}

func TestProviderStopUnattendedSessionLaterCertifiedPaneLossDoesNotTerminateEarlierPane(t *testing.T) {
	const twoPane = "$1\tworker\t@1\t%1\t0\t0\t0\n$1\tworker\t@2\t%2\t0\t0\t0"
	binDir := t.TempDir()
	killInvocations := filepath.Join(binDir, "kill-invocations")
	fakeKill := filepath.Join(binDir, "kill")
	if err := os.WriteFile(fakeKill, []byte("#!/bin/sh\nprintf '%s\n' \"$*\" >> "+killInvocations+"\n"), 0o755); err != nil {
		t.Fatalf("write recording kill: %v", err)
	}
	t.Setenv("PATH", binDir)

	fe := &fakeExecutor{
		outs: []string{
			twoPane,
			"GC_INSTANCE_TOKEN=token",
			twoPane,
			"42424242",
			"",
		},
		errs: []error{nil, nil, nil, nil, ErrSessionNotFound},
	}
	p := NewProviderWithConfig(Config{SocketName: "cert-socket"})
	p.tm.exec = fe

	err := p.StopUnattendedSession("worker", "token")
	if !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("StopUnattendedSession error = %v, want wrapped ErrSessionNotFound", err)
	}
	if output, readErr := os.ReadFile(killInvocations); readErr == nil && len(output) != 0 {
		t.Fatalf("first certified pane was terminated before later lookup failed: %s", output)
	} else if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
		t.Fatalf("read kill invocations: %v", readErr)
	}
	for _, call := range fe.calls {
		if slices.Contains(call, "kill-session") {
			t.Fatalf("session kill followed failed certified-pane lookup: %v", call)
		}
	}
}

type certificationWriteCloser struct {
	closed bool
}

func (*certificationWriteCloser) Write(p []byte) (int, error) {
	return len(p), nil
}

func (w *certificationWriteCloser) Close() error {
	w.closed = true
	return nil
}

func TestProviderStopUnattendedSessionClosesOnlyTrackedHiddenClient(t *testing.T) {
	fe := &fakeExecutor{outs: []string{
		"$1\tworker\t@1\t%1\t0\t0\t0",
		"GC_INSTANCE_TOKEN=token",
		"$1\tworker\t@1\t%1\t0\t0\t0",
		"999999999",
		"",
	}}
	p := NewProviderWithConfig(Config{SocketName: "cert-socket"})
	p.tm.exec = fe
	targetDone := make(chan error)
	close(targetDone)
	otherDone := make(chan error)
	close(otherDone)
	targetWriter := &certificationWriteCloser{}
	otherWriter := &certificationWriteCloser{}
	p.tm.hiddenAttachClients = map[string]*hiddenAttachClient{
		"worker": {cancel: func() {}, done: targetDone, stdin: targetWriter},
		"other":  {cancel: func() {}, done: otherDone, stdin: otherWriter},
	}

	if err := p.StopUnattendedSession("worker", "token"); err != nil {
		t.Fatalf("StopUnattendedSession: %v", err)
	}
	if !targetWriter.closed {
		t.Fatal("tracked hidden client was not closed before bound stop")
	}
	if otherWriter.closed || p.tm.hiddenAttachClient("other") == nil {
		t.Fatal("bound stop disturbed another tracked client")
	}
	if p.tm.hiddenAttachClient("worker") != nil {
		t.Fatal("closed hidden client remained tracked")
	}
	for _, call := range fe.calls {
		if strings.Contains(strings.Join(call, " "), "detach-client") {
			t.Fatalf("bound stop used detach-client: %v", call)
		}
	}
	p.tm.CloseHiddenAttachClient("other")
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
