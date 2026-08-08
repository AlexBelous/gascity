package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/gastownhall/gascity/internal/config"
	"github.com/gastownhall/gascity/internal/fsys"
)

// tickDebounceRetirementScanSkipDirs are directories with no Go sources worth
// scanning (or none at all) that would otherwise dominate the walk.
var tickDebounceRetirementScanSkipDirs = map[string]bool{
	".git":         true,
	".claude":      true,
	"node_modules": true,
	"vendor":       true,
	"testdata":     true,
}

// TestTickDebounceRetirementLeavesNoSourceReferences pins WD.0's deletion: the
// tick debouncer, its config accessor and TOML key, and the dead IdleDrain
// trace constant must not survive anywhere in production or test Go sources.
// The retirement tests themselves are the only place the retired names may
// appear, so they are skipped by filename.
func TestTickDebounceRetirementLeavesNoSourceReferences(t *testing.T) {
	root := gcRepoRootForTest(t)
	// "TickDebounce" catches the retired config field, its accessor, and
	// newTickDebouncer; "tickDebouncer" catches the lowercase type and its
	// methods.
	retired := []string{
		"tickDebouncer",
		"TickDebounce",
		"tick_debounce",
		"TraceSiteReconcilerIdleDrain",
		"reconciler.session.idle_drain",
	}
	var offenders []string
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if tickDebounceRetirementScanSkipDirs[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(d.Name(), ".go") || d.Name() == "tick_debounce_retirement_test.go" {
			return nil
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		content := string(data)
		for _, needle := range retired {
			if strings.Contains(content, needle) {
				rel, relErr := filepath.Rel(root, path)
				if relErr != nil {
					rel = path
				}
				offenders = append(offenders, rel+": "+needle)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %q: %v", root, err)
	}
	if len(offenders) > 0 {
		t.Fatalf("retired tick-debounce/idle-drain references still present:\n%s", strings.Join(offenders, "\n"))
	}
}

// TestEventDrivenPokeBurstCoalescesToSingleTick proves the property the
// debouncer used to provide on the generic event-driven poke path: a burst of
// follow-up requests collapses into exactly one queued tick, because pokeCh is
// a cap-1 channel written with a non-blocking send. The reconciler loop reads
// one value per iteration, so one queued poke is one tick run.
func TestEventDrivenPokeBurstCoalescesToSingleTick(t *testing.T) {
	cr := &CityRuntime{pokeCh: make(chan struct{}, 1)}

	for range 5 {
		cr.requestAsyncStartFollowUpTick()
	}
	if got := len(cr.pokeCh); got != 1 {
		t.Fatalf("queued pokes after burst = %d, want 1 (cap-1 coalesce)", got)
	}

	<-cr.pokeCh
	if got := len(cr.pokeCh); got != 0 {
		t.Fatalf("queued pokes after one tick consumed = %d, want 0", got)
	}

	// A fresh burst after the tick consumed the signal must arm exactly one
	// more tick — level-triggered re-detection, not an accumulating backlog.
	for range 3 {
		cr.requestAsyncStartFollowUpTick()
	}
	if got := len(cr.pokeCh); got != 1 {
		t.Fatalf("queued pokes after second burst = %d, want 1", got)
	}
}

// TestHyperscaleExampleCityKeepsCadenceAfterTickDebounceRetirement is WD.0's
// no-silent-behavior-change negative for the one shipped city that configured
// the key: examples/hyperscale still loads and still declares the same patrol
// cadence and per-tick budget knobs it declared before the key was dropped.
func TestHyperscaleExampleCityKeepsCadenceAfterTickDebounceRetirement(t *testing.T) {
	path := filepath.Join(gcRepoRootForTest(t), "examples", "hyperscale", "city.toml")
	cfg, err := config.Load(fsys.OSFS{}, path)
	if err != nil {
		t.Fatalf("load hyperscale example city: %v", err)
	}
	if got := cfg.Daemon.PatrolIntervalDuration(); got.String() != "10s" {
		t.Fatalf("patrol interval = %s, want 10s (cadence unchanged by the retirement)", got)
	}
	if got := cfg.Daemon.ShutdownTimeoutDuration(); got.String() != "10s" {
		t.Fatalf("shutdown timeout = %s, want 10s", got)
	}
	if got := cfg.Daemon.MaxRestartsOrDefault(); got != 3 {
		t.Fatalf("max restarts = %d, want 3", got)
	}
	if raw, err := os.ReadFile(path); err != nil {
		t.Fatalf("read hyperscale example city: %v", err)
	} else if strings.Contains(string(raw), "tick_"+"debounce") {
		t.Fatal("hyperscale example city still documents the retired tick-debounce key")
	}
}

func gcRepoRootForTest(t *testing.T) string {
	t.Helper()
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	root, err := filepath.Abs(filepath.Join(filepath.Dir(currentFile), "..", ".."))
	if err != nil {
		t.Fatalf("resolve repo root: %v", err)
	}
	return root
}
