package main

import (
	"context"
	"errors"
	"os/exec"
	goruntime "runtime"
	"testing"
)

func TestBDCommandMacExistingBranch(t *testing.T) {
	if goruntime.GOOS != "darwin" {
		t.Skip("Mac branch is tested only on actual Darwin")
	}
	cmd := exec.Command("/bin/sh", "-c", "exit 17")
	cmd.Env = []string{"GC_SESSION_ID=fixture-only-partial", "BEADS_ACTOR=fixture-only-actor"}
	err := runBDCommandWithChildAuthority(context.Background(), "/fixture-only-city", nil, cmd)
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 17 {
		t.Fatal("existing Mac child exit behavior changed or unsupported authority reader invoked")
	}
}
