//go:build linux

package doltorphan

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
)

func snapshotProcesses() ([]Process, error) {
	return scanProcfs("/proc")
}

func scanProcfs(root string) ([]Process, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, fmt.Errorf("read procfs %s: %w", root, err)
	}

	processes := make([]Process, 0, len(entries))
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil || pid <= 0 {
			continue
		}
		processDir := filepath.Join(root, entry.Name())
		rawArgs, err := os.ReadFile(filepath.Join(processDir, "cmdline"))
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return nil, fmt.Errorf("read process %d cmdline: %w", pid, err)
		}
		args := splitProcCmdline(rawArgs)
		if len(args) == 0 {
			continue
		}
		rawStat, err := os.ReadFile(filepath.Join(processDir, "stat"))
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return nil, fmt.Errorf("read process %d stat: %w", pid, err)
		}
		ppid, err := parseProcParentPID(rawStat)
		if err != nil {
			return nil, fmt.Errorf("parse process %d stat: %w", pid, err)
		}
		processes = append(processes, Process{PID: pid, PPID: ppid, Argv: args})
	}
	return processes, nil
}

func splitProcCmdline(raw []byte) []string {
	if len(raw) == 0 {
		return nil
	}
	parts := bytes.Split(raw, []byte{0})
	if len(parts) > 0 && len(parts[len(parts)-1]) == 0 {
		parts = parts[:len(parts)-1]
	}
	args := make([]string, 0, len(parts))
	for _, part := range parts {
		args = append(args, string(part))
	}
	return args
}

func parseProcParentPID(raw []byte) (int, error) {
	closeParen := bytes.LastIndexByte(raw, ')')
	if closeParen < 0 {
		return 0, errors.New("missing command terminator")
	}
	fields := bytes.Fields(raw[closeParen+1:])
	if len(fields) < 2 {
		return 0, errors.New("missing parent pid")
	}
	ppid, err := strconv.Atoi(string(fields[1]))
	if err != nil {
		return 0, fmt.Errorf("parent pid %q: %w", fields[1], err)
	}
	return ppid, nil
}
