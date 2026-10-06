//go:build linux

package proctable

import (
	"bufio"
	"context"
	"io"
	"os"
	"strings"
	"syscall"
	"testing"
	"time"
)

// One artificial child at a time. ForkExec intentionally preserves original
// duplicate ENV vector entries, unlike os/exec's ordinary deduplication.
func TestCallerLinuxOwnedArtificialChild(t *testing.T) {
	if len(os.Args) > 1 && os.Args[len(os.Args)-1] == "owned-linux-fixture" {
		_, _ = io.WriteString(os.Stdout, "ready\n")
		var b [1]byte
		_, _ = os.Stdin.Read(b[:])
		return
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal("test executable unavailable")
	}
	for _, duplicate := range []bool{false, true} {
		t.Run(map[bool]string{false: "positive", true: "raw-duplicate-denied"}[duplicate], func(t *testing.T) {
			inputRead, inputWrite, err := os.Pipe()
			if err != nil {
				t.Fatal("owned pipe unavailable")
			}
			defer func() { _ = inputRead.Close() }()
			defer func() { _ = inputWrite.Close() }()
			outputRead, outputWrite, err := os.Pipe()
			if err != nil {
				t.Fatal("owned pipe unavailable")
			}
			defer func() { _ = outputRead.Close() }()
			defer func() { _ = outputWrite.Close() }()
			env := []string{"GC_SESSION_ID=fixture-only-id", "GC_TEMPLATE=fixture-only-template", "GC_RUNTIME_EPOCH=2", "GC_INSTANCE_TOKEN=fixture-only-token", "BEADS_HOLDER_TOKEN=fixture-only-token", "GC_CITY_PATH=/fixture-only-city"}
			if duplicate {
				env = append(env, "GC_SESSION_ID=fixture-only-id")
			}
			pid, err := syscall.ForkExec(exe, []string{exe, "-test.run=^TestCallerLinuxOwnedArtificialChild$", "GC_INSTANCE_TOKEN=argv-spoof", "owned-linux-fixture"}, &syscall.ProcAttr{Env: env, Files: []uintptr{inputRead.Fd(), outputWrite.Fd(), outputWrite.Fd()}})
			if err != nil {
				t.Fatal("owned artificial child failed to start")
			}
			child, err := os.FindProcess(pid)
			if err != nil {
				t.Fatal("owned child process handle unavailable")
			}
			timer := time.AfterFunc(10*time.Second, func() { _ = child.Kill() })
			defer timer.Stop()
			defer func() { _, _ = inputWrite.Write([]byte("x")); _ = inputWrite.Close(); _, _ = child.Wait() }()
			_ = inputRead.Close()
			_ = outputWrite.Close()
			line, err := bufio.NewReader(outputRead).ReadString('\n')
			if err != nil || line != "ready\n" {
				t.Fatal("owned fixture not ready")
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			p, err := ReadCallerIncarnation(ctx, pid)
			if duplicate {
				stage, _ := CallerDiagnostic(err)
				if err == nil || stage != "environment-decode" {
					t.Fatalf("raw duplicate not proven denied at ENV decode; static stage=%s", stage)
				}
				return
			}
			if err != nil || p.PID != pid || !p.Environment.Complete() || !p.UIDs.HasFileSystem || p.Platform != "linux" {
				stage, errno := CallerDiagnostic(err)
				t.Fatalf("owned Linux capture unavailable; static stage=%s errno=%d", stage, errno)
			}
			found := false
			for _, entry := range p.Environment.Entries() {
				if entry == "GC_INSTANCE_TOKEN=fixture-only-token" {
					found = true
				}
				if strings.Contains(entry, "argv-spoof") {
					t.Fatal("argv entered environment")
				}
			}
			if !found {
				t.Fatal("original fixture environment missing")
			}
			anchor := CallerAnchor{PID: p.PID, Start: p.Start, Domain: p.Domain, Platform: p.Platform, UIDs: p.UIDs}
			chain, err := ReadCallerIncarnations(ctx, pid, anchor)
			if err != nil || len(chain) != 1 {
				stage, errno := CallerDiagnostic(err)
				t.Fatalf("owned Linux anchored chain unavailable; static stage=%s errno=%d", stage, errno)
			}
			wrong := anchor
			wrong.Domain = anchor.Domain + "/mismatch"
			if _, err := ReadCallerIncarnations(ctx, pid, wrong); err == nil {
				t.Fatal("actual Linux kernel domain accepted wrong anchor")
			}
		})
	}
}
