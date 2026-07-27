//go:build !windows

// Package exechost executes the Lumen kernel's concrete shell command.
package exechost

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/gastownhall/gascity/internal/lumen/kernel"
	"github.com/gastownhall/gascity/internal/processgroup"
	"golang.org/x/sys/unix"
)

// Execute runs command through /bin/sh and returns its one closed kernel observation.
func Execute(ctx context.Context, command kernel.ExecCommand) (kernel.Observation, error) {
	if err := ctx.Err(); err != nil {
		return kernel.NewCanceledObservation(command.HostRunKey(), command.PrivateSequence(), err.Error()), nil
	}

	cmd := exec.Command("/bin/sh", "-c", command.Script())
	if cwd, ok := command.CWD(); ok {
		cwd = resolveCWD(cwd)
		if cwd == "" {
			return kernel.NewObservation(command.HostRunKey(), command.PrivateSequence(), "", "", kernel.SpawnErrorTermination("empty working directory")), nil
		}
		cmd.Dir = cwd
	}
	cmd.Env = mergeEnvironment(os.Environ(), command.Environment())
	if stdin, ok := command.Stdin(); ok {
		cmd.Stdin = strings.NewReader(stdin)
	}
	processgroup.StartCommandInNewGroup(cmd)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return kernel.NewObservation(command.HostRunKey(), command.PrivateSequence(), "", "", kernel.SpawnErrorTermination(err.Error())), nil
	}

	waited := make(chan error, 1)
	go func() { waited <- cmd.Wait() }()
	select {
	case err := <-waited:
		return completedObservation(command, cmd, stdout.String(), stderr.String(), err), nil
	case <-ctx.Done():
		select {
		case err := <-waited:
			return completedObservation(command, cmd, stdout.String(), stderr.String(), err), nil
		default:
		}
		signalErr := terminateOwnedGroup(cmd.Process.Pid)
		if signalErr != nil && !errors.Is(signalErr, syscall.ESRCH) {
			return kernel.Observation{}, fmt.Errorf("terminate canceled exec process group: %w", signalErr)
		}
		if errors.Is(signalErr, syscall.ESRCH) {
			select {
			case waitErr := <-waited:
				return completedObservation(command, cmd, stdout.String(), stderr.String(), waitErr), nil
			default:
			}
		}
		// Wait continues to own stdout and stderr. In particular, never read either
		// buffer after returning this cancellation observation.
		return kernel.NewCanceledObservation(command.HostRunKey(), command.PrivateSequence(), ctx.Err().Error()), nil
	}
}

func resolveCWD(cwd string) string {
	home := os.Getenv("HOME")
	switch {
	case cwd == "~", cwd == "$HOME":
		return home
	case strings.HasPrefix(cwd, "~/"):
		return filepath.Join(home, cwd[2:])
	case strings.HasPrefix(cwd, "$HOME/"):
		return filepath.Join(home, cwd[len("$HOME/"):])
	default:
		return cwd
	}
}

func completedObservation(command kernel.ExecCommand, cmd *exec.Cmd, stdout, stderr string, waitErr error) kernel.Observation {
	return kernel.NewObservation(command.HostRunKey(), command.PrivateSequence(), stdout, stderr, termination(cmd, waitErr))
}

func mergeEnvironment(inherited []string, changes []kernel.RenderedEnvironment) []string {
	merged := append([]string(nil), inherited...)
	for _, change := range changes {
		merged = removeEnvironment(merged, change.Key())
		if !change.Remove() {
			merged = append(merged, change.Key()+"="+change.Value())
		}
	}
	return merged
}

func removeEnvironment(values []string, key string) []string {
	kept := values[:0]
	for _, value := range values {
		name, _, found := strings.Cut(value, "=")
		if !found || name != key {
			kept = append(kept, value)
		}
	}
	return kept
}

func termination(cmd *exec.Cmd, waitErr error) kernel.ExecTermination {
	if cmd.ProcessState == nil {
		if waitErr == nil {
			return kernel.SpawnErrorTermination("process ended without a process state")
		}
		return kernel.SpawnErrorTermination(waitErr.Error())
	}
	status, ok := cmd.ProcessState.Sys().(syscall.WaitStatus)
	if !ok {
		if waitErr == nil {
			return kernel.SpawnErrorTermination("process ended without a Unix wait status")
		}
		return kernel.SpawnErrorTermination(waitErr.Error())
	}
	if status.Signaled() {
		name := unix.SignalName(status.Signal())
		if name == "" {
			name = fmt.Sprintf("SIG%d", status.Signal())
		}
		return kernel.SignalTermination(name)
	}
	return kernel.ExitTermination(status.ExitStatus())
}

func terminateOwnedGroup(pgid int) error {
	if pgid <= 1 || pgid == syscall.Getpgrp() {
		return fmt.Errorf("refusing to signal unsafe process group %d", pgid)
	}
	return syscall.Kill(-pgid, syscall.SIGTERM)
}
