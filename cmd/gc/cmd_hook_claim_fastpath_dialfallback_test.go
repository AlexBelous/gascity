package main

import (
	"bytes"
	"context"
	"net"
	"syscall"
	"testing"

	"github.com/gastownhall/gascity/internal/beads"
)

// TestClaimHookWorkFastPathFailsClosedOnControllerReadFailure pins the managed
// Dolt safety boundary: every controller read failure is terminal. Even a
// provable dial-phase failure must leave work queued rather than opening an
// independent BdStore working set, which can overwrite another actor's claim.
func TestClaimHookWorkFastPathFailsClosedOnControllerReadFailure(t *testing.T) {
	t.Setenv("GC_DEBUG", "1")
	opts := hookClaimOptions{Assignee: "worker-1", RouteTargets: []string{"pool-x"}}
	newClient := func(readyErr error) *fakeFastPathClient {
		return &fakeFastPathClient{
			fakeFastPathReader: fakeFastPathReader{readyErr: readyErr},
			claimBead:          func(string, string) (beads.Bead, bool, error) { return beads.Bead{}, false, nil },
		}
	}

	t.Run("dial failure fails closed", func(t *testing.T) {
		client := newClient(&net.OpError{Op: "dial", Net: "tcp", Err: syscall.ECONNREFUSED})
		var stdout, stderr bytes.Buffer
		code := claimHookWorkFastPath(client, []string{"worker-1"}, opts.RouteTargets, "ephemeral", nil, "/wt", opts, &stdout, &stderr)
		if code == 0 {
			t.Errorf("code = 0 on a dial failure, want non-zero terminal failure")
		}
		if !bytes.Contains(stderr.Bytes(), []byte("route=fail-closed reason=controller-dial-failed")) {
			t.Errorf("stderr = %q, want fail-closed route marker", stderr.String())
		}
	})

	t.Run("response/overall timeout fails fast, does not fall back", func(t *testing.T) {
		client := newClient(context.DeadlineExceeded)
		var stdout, stderr bytes.Buffer
		code := claimHookWorkFastPath(client, []string{"worker-1"}, opts.RouteTargets, "ephemeral", nil, "/wt", opts, &stdout, &stderr)
		if code == 0 {
			t.Error("code = 0 on a timeout, want a non-zero terminal failure")
		}
	})

	t.Run("read-phase timeout fails fast", func(t *testing.T) {
		client := newClient(&net.OpError{Op: "read", Net: "tcp", Err: syscall.ETIMEDOUT})
		var stdout, stderr bytes.Buffer
		code := claimHookWorkFastPath(client, []string{"worker-1"}, opts.RouteTargets, "ephemeral", nil, "/wt", opts, &stdout, &stderr)
		if code == 0 {
			t.Error("code = 0 on a read-phase timeout, want a non-zero terminal failure")
		}
	})
}
