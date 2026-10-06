//go:build darwin

package proctable

import (
	"bufio"
	"bytes"
	"context"
	"io"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func callerDarwinInfoFixture() *unix.KinfoProc {
	p := new(unix.KinfoProc)
	p.Proc.P_pid = 777
	p.Proc.P_stat = 2
	p.Proc.P_starttime.Sec = 100
	p.Proc.P_starttime.Usec = 7
	p.Eproc.Ppid = 1
	p.Eproc.Pgid = 777
	p.Eproc.Pcred.P_ruid = 1000
	p.Eproc.Pcred.P_svuid = 1000
	p.Eproc.Ucred.Uid = 1000
	return p
}

func TestCallerDarwinCaptureInjected(t *testing.T) {
	modes := []string{"valid", "omit", "permission", "start", "uid", "env", "boot"}
	for _, mode := range modes {
		t.Run(mode, func(t *testing.T) {
			infos, args, boots := 0, 0, 0
			r := callerDarwinReaders{
				info: func(int) (*unix.KinfoProc, error) {
					infos++
					p := callerDarwinInfoFixture()
					if infos == 2 && mode == "start" {
						p.Proc.P_starttime.Usec++
					}
					if infos == 2 && mode == "uid" {
						p.Eproc.Ucred.Uid++
					}
					return p, nil
				},
				args: func(int) ([]byte, error) {
					args++
					if mode == "permission" {
						return nil, unix.EPERM
					}
					if mode == "omit" {
						return callerRawFixture([]string{"fixture", "GC_SESSION_ID=argv-spoof"}, nil), nil
					}
					value := "fixture-only"
					if mode == "env" && args == 2 {
						value = "different"
					}
					return callerRawFixture([]string{"fixture"}, []string{"GC_INSTANCE_TOKEN=" + value}), nil
				},
				boot: func() ([]byte, error) {
					boots++
					if mode == "boot" && boots == 2 {
						return []byte("different"), nil
					}
					return []byte("fixture-boot"), nil
				},
			}
			_, err := callerDarwinCapture(context.Background(), 777, r)
			if (err == nil) != (mode == "valid") {
				t.Fatal("Darwin fixture verdict incorrect")
			}
		})
	}
}

func TestCallerDarwinOwnedArtificialChild(t *testing.T) {
	if len(os.Args) > 1 && os.Args[len(os.Args)-1] == "owned-caller-fixture" {
		_, _ = io.WriteString(os.Stdout, "ready\n")
		var b [1]byte
		_, _ = os.Stdin.Read(b[:])
		return
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal("test executable unavailable")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, executable, "-test.run=^TestCallerDarwinOwnedArtificialChild$", "GC_INSTANCE_TOKEN=argv-spoof", "owned-caller-fixture")
	cmd.Env = []string{"GC_SESSION_ID=fixture-id", "GC_TEMPLATE=fixture-template", "GC_RUNTIME_EPOCH=2", "GC_INSTANCE_TOKEN=fixture-only-fence", "BEADS_HOLDER_TOKEN=fixture-only-fence", "GC_CITY_PATH=/fixture-city"}
	in, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal("fixture pipe unavailable")
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal("fixture pipe unavailable")
	}
	if err = cmd.Start(); err != nil {
		t.Fatal("owned fixture did not start")
	}
	defer func() { _, _ = in.Write([]byte("x")); _ = in.Close(); _ = cmd.Wait() }()
	line, err := bufio.NewReader(out).ReadString('\n')
	if err != nil || line != "ready\n" {
		t.Fatal("owned fixture not ready")
	}
	captured, err := readCallerPID(context.Background(), cmd.Process.Pid)
	if err != nil {
		// This own artificial fixture establishes the actual kernel primitive
		// path, including a safe UNKNOWN when procargs2 has no strict ENV end.
		// It does not count an ambiguous OS read as positive caller authority.
		info, infoErr := unix.SysctlKinfoProc("kern.proc.pid", cmd.Process.Pid)
		_, identityErr := callerDarwinIdentity(info, cmd.Process.Pid)
		raw, rawErr := unix.SysctlRaw("kern.procargs2", cmd.Process.Pid)
		defer func() {
			for i := range raw {
				raw[i] = 0
			}
		}()
		_, decodeErr := callerDarwinProcargs(raw)
		boot, bootErr := unix.SysctlRaw("kern.boottime")
		if infoErr != nil || identityErr != nil || rawErr != nil || bootErr != nil || len(boot) == 0 || decodeErr == nil || !bytes.Contains(raw, []byte("GC_INSTANCE_TOKEN=fixture-only-fence\x00")) || !bytes.Contains(raw, []byte("GC_INSTANCE_TOKEN=argv-spoof\x00")) {
			t.Fatal("owned fixture did not establish the raw-layout UNKNOWN boundary")
		}
		t.Log("owned artificial child: kernel primitives PASS; strict raw ENV acquisition UNKNOWN (ambiguous post-ENV tail); no positive ancestry claim")
		return
	}
	if captured.PID != cmd.Process.Pid || captured.Platform != "darwin" || captured.UIDs.HasFileSystem {
		t.Fatal("owned fixture identity incorrect")
	}
	entries := captured.Environment.Entries()
	found := false
	for _, e := range entries {
		if e == "GC_INSTANCE_TOKEN=fixture-only-fence" {
			found = true
		}
		if strings.Contains(e, "argv-spoof") {
			t.Fatal("argv spoof entered environment")
		}
	}
	if !found {
		t.Fatal("owned fixture token entry missing")
	}
	anchor := CallerAnchor{PID: captured.PID, Start: captured.Start, Domain: captured.Domain, Platform: captured.Platform, UIDs: captured.UIDs}
	chain, err := ReadCallerIncarnations(context.Background(), cmd.Process.Pid, anchor)
	if err == nil || chain != nil || captured.Environment.Complete() {
		t.Fatal("Darwin syntax promoted to complete ENV ancestry authority")
	}
}

func TestCallerDarwinStartOverflowUnknown(t *testing.T) {
	info := callerDarwinInfoFixture()
	info.Proc.P_starttime.Sec = (1<<63 - 1) / 1_000_000_000
	info.Proc.P_starttime.Usec = 999999
	if _, err := callerDarwinIdentity(info, 777); err == nil {
		t.Fatal("start overflow accepted")
	}
}
