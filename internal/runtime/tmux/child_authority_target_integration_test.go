//go:build integration

package tmux

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"testing"
	"time"
)

func TestAuthorityTargetRealIsolatedTmux(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux unavailable")
	}
	socket := fmt.Sprintf("gctest-at-%d-%d", os.Getpid(), time.Now().UnixNano()%1e9)
	run := func(args ...string) error {
		return exec.Command("tmux", append([]string{"-L", socket}, args...)...).Run()
	}
	if err := run("new-session", "-d", "-s", "target", "-e", "GC_SESSION_ID=fixture", "-e", "GC_TEMPLATE=template", "-e", "GC_RUNTIME_EPOCH=2", "-e", "GC_INSTANCE_TOKEN=fixture-only", "-e", "BEADS_HOLDER_TOKEN=fixture-only", "-e", "GC_CITY_PATH=/fixture", "--", "sleep", "30"); err != nil {
		t.Fatalf("private target tmux start: %v", err)
	}
	defer func() { _ = run("kill-server") }()
	if err := run("new-session", "-d", "-s", "unrelated", "--", "sleep", "30"); err != nil {
		t.Fatalf("private unrelated tmux start: %v", err)
	}
	p := NewProviderWithConfig(Config{SocketName: socket})
	h, err := p.ReadSessionAuthorityTarget(context.Background(), "target", "fixture")
	if err != nil || h.Name != "target" || h.PID <= 1 || len(h.Environment) != 6 {
		t.Fatalf("healthy target denied by unrelated real tmux session: err=%v name=%q pid=%d fields=%d", err, h.Name, h.PID, len(h.Environment))
	}
}
