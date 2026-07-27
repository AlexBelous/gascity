package exechost_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/lumen/exechost"
	"github.com/gastownhall/gascity/internal/lumen/kernel"
	"github.com/gastownhall/gascity/internal/lumen/program"
	"github.com/gastownhall/gascity/internal/testutil"
)

func TestExecutePreservesCWDEnvironmentStdinAndSeparateStreams(t *testing.T) {
	t.Setenv("REMOVE", "inherited")
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "marker"), []byte("cwd-marker"), 0o600); err != nil {
		t.Fatalf("write cwd marker: %v", err)
	}
	fixture := newExecFixture(t, `printf 'out:'; cat marker; printf ':%s:%s:in=' "$SET" "${REMOVE-unset}"; cat; printf 'err:%s' "$SET" >&2`, program.NewLiteral(program.String(dir)), []program.Environment{
		program.NewEnvironment("SET", program.NewLiteral(program.String("value"))),
		program.NewEnvironment("REMOVE", program.NewLiteral(program.Null{})),
	}, program.NewLiteral(program.String("input")))

	observation, err := exechost.Execute(context.Background(), fixture.command)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	succeeded, ok := pendingOutcome(t, fixture, observation).(kernel.Succeeded)
	if !ok {
		t.Fatalf("outcome = %T, want kernel.Succeeded", pendingOutcome(t, fixture, observation))
	}
	result := succeeded.Result()
	if result.Stdout() != "out:cwd-marker:value:unset:in=input" || result.Stderr() != "err:value" {
		t.Fatalf("streams = (%q, %q)", result.Stdout(), result.Stderr())
	}
	if result.Termination() != kernel.ExitTermination(0) {
		t.Fatalf("termination = %#v, want exit 0", result.Termination())
	}
}

func TestExecutePreservesExitSignalAndSpawnErrorTerminations(t *testing.T) {
	tests := []struct {
		name       string
		fixture    execFixture
		wantReason string
		assertTerm func(t *testing.T, termination kernel.ExecTermination)
	}{
		{
			name:       "exit",
			fixture:    newExecFixture(t, "printf out; printf err >&2; exit 17", nil, nil, nil),
			wantReason: "exit_17",
			assertTerm: func(t *testing.T, termination kernel.ExecTermination) {
				if termination != kernel.ExitTermination(17) {
					t.Fatalf("termination = %#v, want exit 17", termination)
				}
			},
		},
		{
			name:       "signal",
			fixture:    newExecFixture(t, "printf out; printf err >&2; kill -TERM $$", nil, nil, nil),
			wantReason: "signal",
			assertTerm: func(t *testing.T, termination kernel.ExecTermination) {
				if termination != kernel.SignalTermination("SIGTERM") {
					t.Fatalf("termination = %#v, want SIGTERM", termination)
				}
			},
		},
		{
			name:       "spawn error",
			fixture:    newExecFixture(t, "echo never", program.NewLiteral(program.String(filepath.Join(t.TempDir(), "missing"))), nil, nil),
			wantReason: "not_executable",
			assertTerm: func(t *testing.T, termination kernel.ExecTermination) {
				if _, ok := termination.(kernel.SpawnErrorTermination); !ok {
					t.Fatalf("termination = %T, want kernel.SpawnErrorTermination", termination)
				}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			observation, err := exechost.Execute(context.Background(), test.fixture.command)
			if err != nil {
				t.Fatalf("Execute: %v", err)
			}
			failed, ok := pendingOutcome(t, test.fixture, observation).(kernel.Failed)
			if !ok || failed.Reason() != test.wantReason {
				t.Fatalf("outcome = %#v, want failed %q", pendingOutcome(t, test.fixture, observation), test.wantReason)
			}
			detail := failed.Detail()
			if test.name != "spawn error" && (detail.Stdout() != "out" || detail.Stderr() != "err") {
				t.Fatalf("streams = (%q, %q)", detail.Stdout(), detail.Stderr())
			}
			test.assertTerm(t, detail.Termination())
		})
	}
}

func TestExecuteTreatsPresentEmptyCWDAsSpawnError(t *testing.T) {
	fixture := newExecFixture(t, "echo never", program.NewLiteral(program.String("")), nil, nil)
	observation, err := exechost.Execute(context.Background(), fixture.command)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	failed, ok := pendingOutcome(t, fixture, observation).(kernel.Failed)
	if !ok || failed.Reason() != "not_executable" {
		t.Fatalf("outcome = %#v, want failed not_executable", pendingOutcome(t, fixture, observation))
	}
	if _, ok := failed.Detail().Termination().(kernel.SpawnErrorTermination); !ok {
		t.Fatalf("termination = %T, want kernel.SpawnErrorTermination", failed.Detail().Termination())
	}
}

func TestExecuteExpandsPinnedHomeCWDFormsOnly(t *testing.T) {
	base := t.TempDir()
	t.Chdir(base)
	home := filepath.Join(base, "home")
	for _, dir := range []string{home, filepath.Join(home, "child"), filepath.Join(base, "absolute"), filepath.Join(base, "relative")} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatalf("make cwd %q: %v", dir, err)
		}
	}
	t.Setenv("HOME", home)
	tests := []struct {
		name string
		cwd  string
		dir  string
	}{
		{name: "tilde home", cwd: "~", dir: home},
		{name: "tilde child", cwd: "~/child", dir: filepath.Join(home, "child")},
		{name: "home variable", cwd: "$HOME", dir: home},
		{name: "home variable child", cwd: "$HOME/child", dir: filepath.Join(home, "child")},
		{name: "absolute stays literal", cwd: filepath.Join(base, "absolute"), dir: filepath.Join(base, "absolute")},
		{name: "relative stays literal", cwd: "relative", dir: filepath.Join(base, "relative")},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			marker := "cwd-" + test.name
			if err := os.WriteFile(filepath.Join(test.dir, "marker"), []byte(marker), 0o600); err != nil {
				t.Fatalf("write cwd marker: %v", err)
			}
			fixture := newExecFixture(t, "cat marker", program.NewLiteral(program.String(test.cwd)), nil, nil)
			observation, err := exechost.Execute(context.Background(), fixture.command)
			if err != nil {
				t.Fatalf("Execute: %v", err)
			}
			succeeded, ok := pendingOutcome(t, fixture, observation).(kernel.Succeeded)
			if !ok || succeeded.Result().Stdout() != marker {
				t.Fatalf("outcome = %#v, want cwd marker %q", pendingOutcome(t, fixture, observation), marker)
			}
		})
	}
}

func TestExecuteAcceptsExplicitEmptyStdin(t *testing.T) {
	fixture := newExecFixture(t, `if read -r line; then printf "line:%s" "$line"; else printf eof; fi`, nil, nil, program.NewLiteral(program.String("")))
	observation, err := exechost.Execute(context.Background(), fixture.command)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	succeeded, ok := pendingOutcome(t, fixture, observation).(kernel.Succeeded)
	if !ok || succeeded.Result().Stdout() != "eof" {
		t.Fatalf("outcome = %#v, want succeeded with EOF from explicit empty stdin", pendingOutcome(t, fixture, observation))
	}
}

func TestExecuteExpiredDeadlineDoesNotSpawn(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "started")
	fixture := newExecFixture(t, "printf started > "+shellQuote(marker), nil, nil, nil)
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	observation, err := exechost.Execute(ctx, fixture.command)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	canceled, ok := pendingOutcome(t, fixture, observation).(kernel.Canceled)
	if !ok || canceled.Reason() != context.DeadlineExceeded.Error() {
		t.Fatalf("outcome = %#v, want canceled deadline exceeded", pendingOutcome(t, fixture, observation))
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expired deadline spawned command marker: %v", err)
	}
}

func TestExecuteCancelsOwnedDescendantAfterLeaderExits(t *testing.T) {
	dir := t.TempDir()
	readyFIFO := filepath.Join(dir, "ready")
	armedFIFO := filepath.Join(dir, "armed")
	signalFIFO := filepath.Join(dir, "signal")
	releaseFIFO := filepath.Join(dir, "release")
	makeFIFO(t, readyFIFO)
	makeFIFO(t, armedFIFO)
	makeFIFO(t, signalFIFO)
	makeFIFO(t, releaseFIFO)
	parentPIDPath := filepath.Join(dir, "parent.pid")
	childPIDPath := filepath.Join(dir, "child.pid")
	child := fmt.Sprintf("trap %s TERM; printf armed > %s; while :; do read line < %s; [ \"$line\" = release ] && exit 0; done", shellQuote("printf term > "+shellQuote(signalFIFO)), shellQuote(armedFIFO), shellQuote(releaseFIFO))
	script := fmt.Sprintf("sh -c %s & child=$!; cat < %s >/dev/null; printf '%%s' \"$child\" > %s; printf '%%s' \"$$\" > %s; printf ready > %s; exit 0", shellQuote(child), shellQuote(armedFIFO), shellQuote(childPIDPath), shellQuote(parentPIDPath), shellQuote(readyFIFO))
	fixture := newExecFixture(t, script, nil, nil, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	type execution struct {
		observation kernel.Observation
		err         error
	}
	result := make(chan execution, 1)
	go func() {
		observation, err := exechost.Execute(ctx, fixture.command)
		result <- execution{observation: observation, err: err}
	}()
	if got := string(readFIFO(t, readyFIFO)); got != "ready" {
		t.Fatalf("ready FIFO = %q, want ready", got)
	}
	parentPID := readPID(t, parentPIDPath)
	childPID := readPID(t, childPIDPath)
	cleaned := false
	t.Cleanup(func() {
		if !cleaned {
			_ = syscall.Kill(-parentPID, syscall.SIGKILL)
		}
	})
	waitForProcessExit(t, parentPID)

	cancel()
	select {
	case got := <-result:
		if got.err != nil {
			t.Fatalf("Execute: %v", got.err)
		}
		canceled, ok := pendingOutcome(t, fixture, got.observation).(kernel.Canceled)
		if !ok || canceled.Reason() != context.Canceled.Error() {
			t.Fatalf("outcome = %#v, want canceled context canceled", pendingOutcome(t, fixture, got.observation))
		}
	case <-time.After(testutil.ExecRaceTimeout):
		t.Fatal("cancellation did not settle after the leader exited")
	}
	if got := string(readFIFO(t, signalFIFO)); got != "term" {
		t.Fatalf("signal FIFO = %q, want term", got)
	}
	if err := syscall.Kill(childPID, 0); err != nil {
		t.Fatalf("child was not alive after SIGTERM: %v", err)
	}
	writeFIFOValue(t, releaseFIFO, "release\n")
	waitForProcessExit(t, childPID)
	cleaned = true
}

type execFixture struct {
	formula program.Program
	command kernel.ExecCommand
}

func newExecFixture(t *testing.T, script string, cwd program.Expr, env []program.Environment, stdin program.Expr) execFixture {
	t.Helper()
	exec := program.NewExec("exec_1", nil, program.NewInterpolatedText([]program.TextPart{program.NewText(script)}), cwd, env, stdin, program.NewExitMap([]int{0}, nil))
	formula, err := program.ValidateProgram(program.NewProgram(program.NewFormula("main", program.NewInput("main.input", nil), []program.Step{program.NewBlock("block_1", nil, []program.Step{exec})})))
	if err != nil {
		t.Fatalf("ValidateProgram: %v", err)
	}
	state, err := kernel.Fold(formula, []kernel.Record{kernel.NewGenesis("host-private", nil)})
	if err != nil {
		t.Fatalf("Fold: %v", err)
	}
	command, ok := state.Command()
	if !ok {
		t.Fatal("Fold did not derive command")
	}
	return execFixture{formula: formula, command: command}
}

func pendingOutcome(t *testing.T, fixture execFixture, observation kernel.Observation) kernel.Outcome {
	t.Helper()
	state, err := kernel.Fold(fixture.formula, []kernel.Record{
		kernel.NewGenesis("host-private", nil),
		kernel.NewCommandIssued("host-private", 1, "exec_1"),
		observation,
	})
	if err != nil {
		t.Fatalf("Fold observation: %v", err)
	}
	pending, ok := state.PendingTerminal()
	if !ok {
		t.Fatal("Fold did not derive pending terminal")
	}
	return pending.Outcome()
}

func makeFIFO(t *testing.T, path string) {
	t.Helper()
	if err := syscall.Mkfifo(path, 0o600); err != nil {
		t.Fatalf("make FIFO %q: %v", path, err)
	}
}

func readPID(t *testing.T, path string) int {
	t.Helper()
	text, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read PID %q: %v", path, err)
	}
	pid, err := strconv.Atoi(string(text))
	if err != nil {
		t.Fatalf("parse PID %q: %v", text, err)
	}
	return pid
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}

func readFIFO(t *testing.T, path string) []byte {
	t.Helper()
	type opened struct {
		file *os.File
		err  error
	}
	result := make(chan opened, 1)
	go func() {
		file, err := os.Open(path)
		result <- opened{file: file, err: err}
	}()
	select {
	case opened := <-result:
		if opened.err != nil {
			t.Fatalf("open FIFO %q: %v", path, opened.err)
		}
		content, readErr := io.ReadAll(opened.file)
		closeErr := opened.file.Close()
		if readErr != nil {
			t.Fatalf("read FIFO %q: %v", path, readErr)
		}
		if closeErr != nil {
			t.Fatalf("close FIFO %q: %v", path, closeErr)
		}
		return content
	case <-time.After(testutil.ExecRaceTimeout):
		t.Fatalf("FIFO %q did not open within the safety deadline", path)
		return nil
	}
}

func writeFIFOValue(t testing.TB, path, value string) {
	t.Helper()
	written := make(chan error, 1)
	go func() {
		file, err := os.OpenFile(path, os.O_WRONLY, 0)
		if err == nil {
			_, err = file.Write([]byte(value))
			closeErr := file.Close()
			if err == nil {
				err = closeErr
			}
		}
		written <- err
	}()
	select {
	case err := <-written:
		if err != nil {
			t.Fatalf("write FIFO %q: %v", path, err)
		}
	case <-time.After(testutil.ExecRaceTimeout):
		t.Fatalf("FIFO %q did not accept cleanup input within the safety deadline", path)
	}
}

func waitForProcessExit(t *testing.T, pid int) {
	t.Helper()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	timeout := time.NewTimer(testutil.ExecRaceTimeout)
	defer timeout.Stop()
	var last error
	for {
		last = syscall.Kill(pid, 0)
		if errors.Is(last, syscall.ESRCH) {
			return
		}
		select {
		case <-ticker.C:
		case <-timeout.C:
			t.Fatalf("child process %d did not exit within %s; last state probe: %v", pid, testutil.ExecRaceTimeout, last)
		}
	}
}
