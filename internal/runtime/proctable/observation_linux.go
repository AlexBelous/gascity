//go:build linux

package proctable

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

func observeRoots() ([]ObservedRoot, error) {
	return observeLinuxRoot(scanRoot, os.ReadFile)
}

func observeLinuxRoot(root string, readFile func(string) ([]byte, error)) ([]ObservedRoot, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, fmt.Errorf("enumerating process table: %w", err)
	}
	var processes []observedProcess
	var errs []error
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil || pid <= 0 || !entry.IsDir() {
			continue
		}
		p, err := observeLinuxProcess(root, pid, readFile)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		processes = append(processes, p)
	}
	roots, err := rootsFromObservedProcesses(processes)
	return roots, errors.Join(append(errs, err)...)
}

func observeLinuxProcess(root string, pid int, readFile func(string) ([]byte, error)) (observedProcess, error) {
	dir := filepath.Join(root, strconv.Itoa(pid))
	fail := func() (observedProcess, error) {
		return observedProcess{}, fmt.Errorf("process environment or identity unavailable for PID %d", pid)
	}
	// Read stat on both sides of environ: a recycled PID must never splice an
	// old incarnation's environment onto a new process-start identity.
	first, err := readFile(filepath.Join(dir, "stat"))
	if err != nil {
		return fail()
	}
	ppid, pgid, start, ok, err := parseProcStatIdentity(string(first))
	if err != nil || !ok {
		return fail()
	}
	data, err := readFile(filepath.Join(dir, "environ"))
	if err != nil {
		return fail()
	}
	env, err := parseObservedEnvironment(data)
	if err != nil {
		return fail()
	}
	comm, err := readFile(filepath.Join(dir, "comm"))
	if err != nil {
		return fail()
	}
	last, err := readFile(filepath.Join(dir, "stat"))
	if err != nil {
		return fail()
	}
	lastPPID, lastPGID, lastStart, ok, err := parseProcStatIdentity(string(last))
	if err != nil || !ok || start != lastStart || ppid != lastPPID || pgid != lastPGID {
		return fail()
	}
	return observedProcess{record: ProcessRecord{PID: pid, PPID: ppid, PGID: pgid, StartTime: start, Name: strings.TrimSpace(string(comm))}, env: env}, nil
}
