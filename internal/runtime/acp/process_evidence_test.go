//go:build integration

package acp

import (
	"bufio"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/gastownhall/gascity/internal/runtime"
)

func TestTrackProcessEvidenceACPUsesLiveOwnerPIDAndPreservesOrphan(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "acpev-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir) //nolint:errcheck
	p := NewProviderWithDir(dir, Config{})
	name := "live"
	if err = p.SetMeta(name, "GC_SESSION_ID", "sid"); err != nil {
		t.Fatal(err)
	}
	lis, err := net.Listen("unix", filepath.Join(dir, name+".sock"))
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			c, err := lis.Accept()
			if err != nil {
				return
			}
			wg.Add(1)
			go func() {
				defer wg.Done()
				defer c.Close() //nolint:errcheck
				s := bufio.NewScanner(c)
				if !s.Scan() {
					return
				}
				switch s.Text() {
				case "ping":
					_, _ = fmt.Fprintln(c, "ok")
				case "pid":
					_, _ = fmt.Fprintln(c, "42")
				default:
					_, _ = fmt.Fprintln(c, "forbidden")
				}
			}()
		}
	}()
	defer func() { _ = lis.Close(); wg.Wait() }()
	roots := []runtime.LiveRuntime{{PID: 42, PPID: 1, SessionID: "sid", Epoch: 2, City: "/city"}, {PID: 43, PPID: 1, SessionID: "sid", Epoch: 1, City: "/city"}}
	got, err := seamBack(p).TrackProcessRoots(roots)
	if err != nil || len(got) != 2 || !got[0].IsTracked || got[0].ProviderName != name || got[1].IsTracked {
		t.Fatalf("wrong ACP attribution %+v %v", got, err)
	}
	_ = lis.Close()
	wg.Wait()
	got, err = seamBack(p).TrackProcessRoots(roots)
	if err != nil || len(got) != 2 || got[0].IsTracked || got[1].IsTracked {
		t.Fatalf("dead ACP socket kept roots tracked %+v %v", got, err)
	}
}
