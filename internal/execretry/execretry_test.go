package execretry

import (
	"errors"
	"os/exec"
	"testing"
)

func TestRunRetriesOnTextFileBusy(t *testing.T) {
	calls := 0
	run := func(*exec.Cmd) ([]byte, error) {
		calls++
		if calls == 1 {
			return []byte("sh: flaky: Text file busy\n"), errors.New("exit status 126")
		}
		return []byte("ok\n"), nil
	}

	out, err := Run(&exec.Cmd{Path: "unused"}, DefaultAttempts, run)
	if err != nil {
		t.Fatalf("err = %v, want nil after retrying past a transient text-file-busy failure", err)
	}
	if string(out) != "ok\n" {
		t.Fatalf("out = %q, want %q", out, "ok\n")
	}
	if calls != 2 {
		t.Fatalf("calls = %d, want exactly 2 (one busy failure, one successful retry)", calls)
	}
}

func TestRunDoesNotRetryUnrelatedFailures(t *testing.T) {
	calls := 0
	wantErr := errors.New("boom: unrelated failure")
	run := func(*exec.Cmd) ([]byte, error) {
		calls++
		return []byte("boom: unrelated failure\n"), wantErr
	}

	_, err := Run(&exec.Cmd{Path: "unused"}, DefaultAttempts, run)
	if !errors.Is(err, wantErr) {
		t.Fatalf("err = %v, want %v", err, wantErr)
	}
	if calls != 1 {
		t.Fatalf("calls = %d, want exactly 1 (must not retry non-text-file-busy failures)", calls)
	}
}

func TestRunGivesUpAfterAttemptsExhausted(t *testing.T) {
	calls := 0
	run := func(*exec.Cmd) ([]byte, error) {
		calls++
		return []byte("Text file busy\n"), errors.New("exit status 126")
	}

	_, _ = Run(&exec.Cmd{Path: "unused"}, 3, run)
	if calls != 4 {
		t.Fatalf("calls = %d, want exactly 4 (one initial try plus 3 retries)", calls)
	}
}

func TestRunPassesAFreshCommandToEachRetry(t *testing.T) {
	orig := &exec.Cmd{Path: "unused", Args: []string{"unused", "a"}}
	var seen []*exec.Cmd
	calls := 0
	run := func(cmd *exec.Cmd) ([]byte, error) {
		calls++
		seen = append(seen, cmd)
		if calls < 3 {
			return []byte("Text file busy\n"), errors.New("exit status 126")
		}
		return []byte("ok\n"), nil
	}

	if _, err := Run(orig, DefaultAttempts, run); err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if len(seen) != 3 {
		t.Fatalf("saw %d run calls, want 3", len(seen))
	}
	if seen[0] != orig {
		t.Fatalf("first attempt must use the original *exec.Cmd unchanged")
	}
	for i, cmd := range seen[1:] {
		if cmd == orig {
			t.Fatalf("retry %d reused the original *exec.Cmd; exec.Cmd is single-use once run", i+1)
		}
		if cmd.Path != orig.Path {
			t.Fatalf("retry %d Path = %q, want %q", i+1, cmd.Path, orig.Path)
		}
	}
}

func TestTextFileBusy(t *testing.T) {
	cases := []struct {
		name string
		err  error
		out  []byte
		want bool
	}{
		{"nil error", nil, []byte("Text file busy"), false},
		{"stderr text match", errors.New("exit status 126"), []byte("sh: flaky: Text file busy"), true},
		{"error text match", errors.New("fork/exec /tmp/x: text file busy"), nil, true},
		{"unrelated failure", errors.New("exit status 7"), []byte("boom"), false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := TextFileBusy(c.err, c.out); got != c.want {
				t.Errorf("TextFileBusy(%v, %q) = %v, want %v", c.err, c.out, got, c.want)
			}
		})
	}
}

func TestClone(t *testing.T) {
	orig := &exec.Cmd{
		Path: "/bin/echo",
		Args: []string{"/bin/echo", "hello", "world"},
		Dir:  "/tmp",
		Env:  []string{"FOO=bar"},
	}

	clone := Clone(orig)

	if clone == orig {
		t.Fatalf("Clone returned the same *exec.Cmd; exec.Cmd is single-use once run")
	}
	if clone.Path != orig.Path {
		t.Errorf("Path = %q, want %q", clone.Path, orig.Path)
	}
	if len(clone.Args) != len(orig.Args) {
		t.Fatalf("Args = %v, want %v", clone.Args, orig.Args)
	}
	for i := range orig.Args {
		if clone.Args[i] != orig.Args[i] {
			t.Errorf("Args[%d] = %q, want %q", i, clone.Args[i], orig.Args[i])
		}
	}
	if clone.Dir != orig.Dir {
		t.Errorf("Dir = %q, want %q", clone.Dir, orig.Dir)
	}
	if len(clone.Env) != len(orig.Env) || clone.Env[0] != orig.Env[0] {
		t.Errorf("Env = %v, want %v", clone.Env, orig.Env)
	}
}
