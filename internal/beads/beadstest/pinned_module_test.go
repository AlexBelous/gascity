package beadstest

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/mod/module"
)

// TestPinnedBeadsModuleDirRefusesAnUnresolvedCache is the fence under the drift
// check's one silent-green path.
//
// The resolver is a hand-rolled copy of cmd/go's GOMODCACHE -> go env file ->
// GOPATH[0]/pkg/mod chain, kept inlined because shelling out to `go env` would
// grow the repo's shrink-only subprocess census. An inlined copy is only
// defensible if disagreeing with cmd/go is fatal, so that is asserted here
// rather than left to the one caller.
func TestPinnedBeadsModuleDirRefusesAnUnresolvedCache(t *testing.T) {
	t.Run("a cache that does not hold the module", func(t *testing.T) {
		if _, err := pinnedBeadsModuleDir(filepath.Join(t.TempDir(), "empty"), "v1.3.0"); err == nil {
			t.Fatal("pinnedBeadsModuleDir accepted a cache with no pinned module in it; a skip here lets a resolver bug read as green")
		}
	})

	t.Run("a path that is not a directory", func(t *testing.T) {
		cache := t.TempDir()
		path := filepath.Join(cache, filepath.FromSlash(PinnedBeadsModulePath)+"@v1.3.0")
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("not a module"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := pinnedBeadsModuleDir(cache, "v1.3.0"); err == nil {
			t.Fatal("pinnedBeadsModuleDir accepted a file where the module source should be")
		}
	})

	t.Run("the unpacked module", func(t *testing.T) {
		cache := t.TempDir()
		want := filepath.Join(cache, filepath.FromSlash(PinnedBeadsModulePath)+"@v1.3.0")
		if err := os.MkdirAll(want, 0o755); err != nil {
			t.Fatal(err)
		}
		got, err := pinnedBeadsModuleDir(cache, "v1.3.0")
		if err != nil {
			t.Fatalf("pinnedBeadsModuleDir: %v", err)
		}
		if got != want {
			t.Fatalf("pinnedBeadsModuleDir = %q, want %q", got, want)
		}
	})
}

// TestPinnedBeadsModuleDirFailsRatherThanSkips pins the seam itself.
//
// The subtests above prove the resolver returns an error; this proves what the
// caller does with it. A revert of the t.Fatalf to a t.Skipf would leave every
// other test in this package green while the pinned-cursor drift check silently
// stopped running — which is the failure mode it was written against, and it was
// reproduced with nothing more than GOMODCACHE pointed somewhere else.
func TestPinnedBeadsModuleDirFailsRatherThanSkips(t *testing.T) {
	reporter := &recordingModuleDirReporter{}
	if dir := pinnedBeadsModuleDirOrFatal(reporter, filepath.Join(t.TempDir(), "empty"), "v1.3.0"); dir != "" {
		t.Fatalf("an unresolved cache produced the directory %q", dir)
	}
	if len(reporter.skips) != 0 {
		t.Fatalf("an unresolved module cache was SKIPPED (%q); a skip lets the drift check go quiet and read as green", reporter.skips)
	}
	if len(reporter.fatals) != 1 {
		t.Fatalf("an unresolved module cache reported %d fatal(s), want exactly 1: %q", len(reporter.fatals), reporter.fatals)
	}
	for _, want := range []string{"GOMODCACHE", PinnedBeadsModulePath} {
		if !strings.Contains(reporter.fatals[0], want) {
			t.Errorf("the failure message does not name %q, so the operator cannot act on it:\n%s", want, reporter.fatals[0])
		}
	}
}

// recordingModuleDirReporter records what the resolver reports instead of
// failing or skipping the test that drives it. It does not call runtime.Goexit
// on Fatalf, so the caller returns normally and the zero value it hands back is
// asserted too.
type recordingModuleDirReporter struct {
	fatals []string
	skips  []string
}

func (r *recordingModuleDirReporter) Helper() {}

func (r *recordingModuleDirReporter) Fatalf(format string, args ...any) {
	r.fatals = append(r.fatals, fmt.Sprintf(format, args...))
}

func (r *recordingModuleDirReporter) Skipf(format string, args ...any) {
	r.skips = append(r.skips, fmt.Sprintf(format, args...))
}

// The drift check must resolve the replacement's source even when an ambient
// upstream cache exists; an uppercase fork path uses Go's escaped cache name.
func TestPinnedBeadsSourceAndCacheHonorReplacement(t *testing.T) {
	const fork = "github.com/AlexBelous/beads"
	for _, tc := range []struct {
		name, directive, path, version string
		invalid                        bool
	}{
		{name: "original", path: PinnedBeadsModulePath, version: "v1.3.0"},
		{name: "fork", directive: "replace github.com/steveyegge/beads => github.com/AlexBelous/beads v1.1.1-0.20260928222722-da08f27390f1", path: fork, version: "v1.1.1-0.20260928222722-da08f27390f1"},
		{name: "version specific", directive: "replace (\n github.com/steveyegge/beads => github.com/AlexBelous/beads v1.2.0\n github.com/steveyegge/beads v1.3.0 => github.com/AlexBelous/beads v1.2.1\n)", path: fork, version: "v1.2.1"},
		{name: "other version", directive: "replace github.com/steveyegge/beads v1.2.0 => github.com/AlexBelous/beads v1.2.1", path: PinnedBeadsModulePath, version: "v1.3.0"},
		{name: "local rejected", directive: "replace github.com/steveyegge/beads => ../beads", invalid: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			source, err := pinnedBeadsSource([]byte("module gascity.test/pins\ngo 1.26.6\nrequire github.com/steveyegge/beads v1.3.0\n" + tc.directive + "\n"))
			if tc.invalid {
				if err == nil {
					t.Fatal("unversioned source accepted")
				}
				return
			}
			if err != nil || source != (module.Version{Path: tc.path, Version: tc.version}) {
				t.Fatalf("source=%#v err=%v", source, err)
			}
			cache := t.TempDir()
			// Only the original module is cached: a replacement must still fail closed.
			upstream := filepath.Join(cache, "github.com/steveyegge/beads@v1.3.0")
			if err := os.MkdirAll(upstream, 0o700); err != nil {
				t.Fatal(err)
			}
			if source.Path != PinnedBeadsModulePath {
				if _, err := pinnedModuleDir(cache, source); err == nil {
					t.Fatal("ambient upstream cache substituted for replacement")
				}
			}
			escaped, err := module.EscapePath(source.Path)
			if err != nil {
				t.Fatal(err)
			}
			want := filepath.Join(cache, escaped+"@"+source.Version)
			if err := os.MkdirAll(want, 0o700); err != nil {
				t.Fatal(err)
			}
			got, err := pinnedModuleDir(cache, source)
			if err != nil || got != want {
				t.Fatalf("cache=%q want=%q err=%v", got, want, err)
			}
		})
	}
}
