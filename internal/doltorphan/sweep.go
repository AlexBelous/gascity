// Package doltorphan implements a symptom-based fallback sweep for
// orphaned dolt store directories: a directory is a removal candidate when
// it is old, contains a .dolt marker, and is not held by lsof or referenced
// by a live dolt sql-server process. It composes with, but does not replace,
// process-level classification (e.g. cmd/gc's classifyDoltProcess) — this
// package never kills processes, it only judges directories that are already
// symptomatic of abandonment, which is what lets it catch leaks regardless
// of what created them (a killed test binary, an untracked ad-hoc dolt
// invocation, etc.). Ported from the production-proven heuristic in
// gc-test-dolt-reaper.sh sections 4-5.
package doltorphan

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/gastownhall/gascity/internal/clock"
)

// DefaultMinAge is the age a candidate directory's mtime must clear before
// the sweep will consider it abandoned. Matches acceptance criterion 2 of
// ga-ntbpyb.2.
const DefaultMinAge = 60 * time.Minute

// maxMarkerDepth bounds how deep the .dolt marker search descends below a
// candidate directory, mirroring `find "$d" -maxdepth 3 -type d -name
// '.dolt'` from gc-test-dolt-reaper.sh section 4.
const maxMarkerDepth = 3

// lsofScanTimeout bounds the real `lsof -w` invocation, mirroring the
// shell script's `timeout 30 lsof -w`.
const lsofScanTimeout = 30 * time.Second

// SweepConfig configures a single Sweep pass. Root is required; every
// other field defaults to production behavior when left zero-valued.
type SweepConfig struct {
	// Root is the directory whose direct children are swept, e.g. os.TempDir().
	Root string
	// MinAge overrides DefaultMinAge when positive.
	MinAge time.Duration
	// Clock supplies "now" for age comparisons. Defaults to clock.Real{}.
	Clock clock.Clock
	// RunLsof runs an lsof-equivalent scan and returns its raw stdout.
	// Defaults to candidate-scoped real lsof invocations. Injectable for tests.
	RunLsof func(ctx context.Context) ([]byte, error)
	// ScanProcesses returns one host-wide process snapshot with argv
	// boundaries preserved. Defaults to the platform process scanner.
	// Injectable for tests.
	ScanProcesses func() ([]Process, error)
	// RemoveAll removes a candidate directory. Defaults to os.RemoveAll.
	// Injectable for tests.
	RemoveAll func(path string) error
}

// Process is one live process observed during a sweep.
type Process struct {
	PID  int
	PPID int
	Argv []string
}

// SweepResult reports what a Sweep pass did.
type SweepResult struct {
	// Removed lists the candidate directories that were removed.
	Removed []string
	// Skipped counts candidates that matched age+marker but were held
	// open per lsof, referenced by a live dolt sql-server process, or were
	// held per fail-closed scan-error handling.
	Skipped int
	// Errors collects non-fatal problems (a single candidate's removal
	// failing, or a liveness scan failing) without aborting the rest of
	// the pass.
	Errors []error
}

// Sweep removes direct children of cfg.Root that look like abandoned dolt
// store directories: mtime older than MinAge, a .dolt marker directory
// within maxMarkerDepth levels, and not currently held by either lsof or a
// live dolt sql-server process. Candidate selection intentionally does not
// filter on directory name — the signals above are what establish
// abandonment, not any particular naming convention, so this catches
// leaks "regardless of creation source" (ga-ntbpyb.2 acceptance criterion
// 2) including directories named by Go's t.TempDir() rather than the
// bare-mktemp "tmp.*" pattern the heuristic was first observed against.
//
// If either liveness scan fails, Sweep fails closed: nothing is removed this
// pass (an unverifiable "is this held" check is treated the same as "yes").
func Sweep(cfg SweepConfig) SweepResult {
	var result SweepResult

	removeAll := cfg.RemoveAll
	if removeAll == nil {
		removeAll = os.RemoveAll
	}
	clk := cfg.Clock
	if clk == nil {
		clk = clock.Real{}
	}
	minAge := cfg.MinAge
	if minAge <= 0 {
		minAge = DefaultMinAge
	}

	entries, err := os.ReadDir(cfg.Root)
	if err != nil {
		result.Errors = append(result.Errors, fmt.Errorf("read %s: %w", cfg.Root, err))
		return result
	}

	now := clk.Now()
	var candidates []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		dir := filepath.Join(cfg.Root, e.Name())
		info, err := e.Info()
		if err != nil {
			continue
		}
		if now.Sub(info.ModTime()) < minAge {
			continue
		}
		if !hasDoltMarker(dir, maxMarkerDepth) {
			continue
		}
		candidates = append(candidates, dir)
	}
	if len(candidates) == 0 {
		return result
	}

	processes, err := scanProcessTable(cfg.ScanProcesses)
	if err != nil {
		result.Errors = append(result.Errors, fmt.Errorf("process snapshot: %w", err))
		result.Skipped = len(candidates)
		return result
	}
	processHeld := processHeldCandidates(candidates, processes)
	lsofCandidates := make([]string, 0, len(candidates))
	for _, dir := range candidates {
		if !processHeld[dir] {
			lsofCandidates = append(lsofCandidates, dir)
		}
	}
	held, err := lsofHeldCandidates(lsofCandidates, cfg.RunLsof)
	if err != nil {
		result.Errors = append(result.Errors, fmt.Errorf("lsof -w: %w", err))
		result.Skipped = len(candidates)
		return result
	}

	for _, dir := range candidates {
		if held[dir] || processHeld[dir] {
			result.Skipped++
			continue
		}
		if err := removeAll(dir); err != nil {
			result.Errors = append(result.Errors, fmt.Errorf("remove %s: %w", dir, err))
			continue
		}
		result.Removed = append(result.Removed, dir)
	}
	return result
}

func scanProcessTable(scan func() ([]Process, error)) ([]Process, error) {
	if scan == nil {
		scan = snapshotProcesses
	}
	return scan()
}

// processHeldCandidates reports candidates referenced by a live dolt
// sql-server or by an observed launcher that still has a dolt sql-server
// descendant. The descendant case covers wrappers that retain --data-dir or
// --config while the actual server process has a shorter argv.
func processHeldCandidates(candidates []string, processes []Process) map[string]bool {
	held := make(map[string]bool)
	if len(candidates) == 0 || len(processes) == 0 {
		return held
	}

	byPID := make(map[int]Process, len(processes))
	children := make(map[int][]int, len(processes))
	for _, process := range processes {
		byPID[process.PID] = process
		if process.PID > 0 && process.PPID > 0 && process.PID != process.PPID {
			children[process.PPID] = append(children[process.PPID], process.PID)
		}
	}

	for _, process := range processes {
		var referenced []string
		for _, candidate := range candidates {
			if processReferencesCandidate(process.Argv, candidate) {
				referenced = append(referenced, candidate)
			}
		}
		if len(referenced) == 0 || !doltSQLServerInTree(process.PID, byPID, children) {
			continue
		}
		for _, candidate := range referenced {
			held[candidate] = true
		}
	}
	return held
}

func processReferencesCandidate(args []string, candidate string) bool {
	for i, arg := range args {
		switch arg {
		case "--config", "--data-dir":
			if i+1 < len(args) && pathsOverlap(candidate, args[i+1]) {
				return true
			}
		default:
			for _, flag := range []string{"--config=", "--data-dir="} {
				if strings.HasPrefix(arg, flag) && pathsOverlap(candidate, strings.TrimPrefix(arg, flag)) {
					return true
				}
			}
		}
	}
	return false
}

func pathsOverlap(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	a = filepath.Clean(a)
	b = filepath.Clean(b)
	if a == "." || b == "." {
		return false
	}
	separator := string(os.PathSeparator)
	return a == b ||
		strings.HasPrefix(a, b+separator) ||
		strings.HasPrefix(b, a+separator)
}

func doltSQLServerInTree(root int, byPID map[int]Process, children map[int][]int) bool {
	visited := make(map[int]bool)
	stack := []int{root}
	for len(stack) > 0 {
		pid := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if visited[pid] {
			continue
		}
		visited[pid] = true
		process, ok := byPID[pid]
		if !ok {
			continue
		}
		if looksLikeDoltSQLServer(process.Argv) {
			return true
		}
		stack = append(stack, children[pid]...)
	}
	return false
}

func looksLikeDoltSQLServer(args []string) bool {
	for i := 0; i+1 < len(args); i++ {
		if filepath.Base(args[i]) == "dolt" && args[i+1] == "sql-server" {
			return true
		}
	}
	return false
}

// hasDoltMarker reports whether a directory literally named ".dolt" exists
// within depth levels of dir (dir's direct children are depth 1).
func hasDoltMarker(dir string, depth int) bool {
	if depth <= 0 {
		return false
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if e.Name() == ".dolt" {
			return true
		}
		if hasDoltMarker(filepath.Join(dir, e.Name()), depth-1) {
			return true
		}
	}
	return false
}

// lsofHeldCandidates returns the candidates that appear as a path prefix of
// an open file. An injected scanner runs once and may return ordinary lsof
// output. The production scanner scopes each lsof invocation to one candidate
// so a sweep does not enumerate every open file on the host.
func lsofHeldCandidates(candidates []string, runLsof func(ctx context.Context) ([]byte, error)) (map[string]bool, error) {
	held := make(map[string]bool)
	if len(candidates) == 0 {
		return held, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), lsofScanTimeout)
	defer cancel()

	if runLsof != nil {
		out, err := runLsof(ctx)
		if err != nil {
			return nil, err
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		return heldCandidatesFromLsofOutput(candidates, out), nil
	}
	for _, candidate := range candidates {
		out, err := runLsofCandidate(ctx, candidate)
		if err != nil {
			return nil, fmt.Errorf("scan %s: %w", candidate, err)
		}
		for path := range heldCandidatesFromLsofOutput([]string{candidate}, out) {
			held[path] = true
		}
	}
	return held, nil
}

func heldCandidatesFromLsofOutput(candidates []string, out []byte) map[string]bool {
	held := make(map[string]bool)
	for _, candidate := range candidates {
		candidate = filepath.Clean(candidate)
		pattern := regexp.MustCompile(regexp.QuoteMeta(candidate) + `(?:[/\r\n]|$)`)
		if pattern.Match(out) {
			held[candidate] = true
		}
	}
	return held
}

// runLsofCandidate runs a machine-readable, recursive lsof scan scoped to one
// candidate. lsof exits non-zero when it finds no open files, so an ExitError
// with a live context is accepted; a context cancellation or deadline is
// always an unverifiable scan and therefore an error.
func runLsofCandidate(ctx context.Context, candidate string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "lsof", "-w", "-Fn", "+D", candidate)
	out, err := cmd.Output()
	return normalizeLsofResult(ctx, out, err)
}

func normalizeLsofResult(ctx context.Context, out []byte, err error) ([]byte, error) {
	if ctxErr := ctx.Err(); ctxErr != nil {
		return nil, ctxErr
	}
	var exitErr *exec.ExitError
	if err == nil {
		return out, nil
	}
	if !errors.As(err, &exitErr) {
		return nil, err
	}
	if len(bytes.TrimSpace(exitErr.Stderr)) != 0 {
		return nil, fmt.Errorf("lsof candidate scan exited with diagnostics: %w", err)
	}
	return out, nil
}
