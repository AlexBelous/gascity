package beadstest

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"testing"

	"golang.org/x/mod/modfile"
	"golang.org/x/mod/module"
)

// PinnedBeadsModulePath is the module gc links its native store against.
const PinnedBeadsModulePath = "github.com/steveyegge/beads"

// PinnedBeadsModuleDir resolves the unpacked beads source and FAILS when absent.
// Compiled dependency metadata takes priority, including its replacement. Go
// test binaries may omit that metadata; only then does this resolve the require
// and matching replace declared by this repository's go.mod. That fallback
// proves the checkout's declared pin, not an embedded binary source identity.
// A missing or unversioned source is fatal: skipping would silence the drift
// check that guards against unexpected shared-database migrations.
//
// It resolves the cache the way the go command does rather than by reading
// GOMODCACHE, because `go test` does not export that variable into the test
// process: a check that read it ran only under wrappers that happen to forward
// it, and went quiet under every plain `go test`, an editor runner and a git
// hook. A contract check that is honest under one wrapper is not a contract
// check. (internal/beads/contract/migrate_journal_test.go learned this the hard
// way and states the reasoning at length.)
//
// It also deliberately shells out to nothing. `go env` or `go list -m` would
// answer in one line, and a subprocess in a test grows the repo's source
// resource census — a shrink-only ratchet — so the resolution is inlined
// instead.
func PinnedBeadsModuleDir(t *testing.T) string {
	t.Helper()
	// Under bazel the module tree arrives as runfiles from the go_deps
	// external repository (the test declares the schema library as data);
	// a pure-Bazel machine has no go module cache at all.
	if dir := bazelRunfilesBeadsModule(); dir != "" {
		return dir
	}
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		bi = nil
	}
	var data []byte
	if compiledPinnedBeadsModule(bi) == nil {
		var err error
		data, err = os.ReadFile(filepath.Join(RepositoryRoot(t), "go.mod"))
		if err != nil {
			t.Fatal(err)
		}
	}
	dep, err := pinnedBeadsSourceForBuild(bi, data)
	if err != nil {
		t.Fatal(err)
	}
	return pinnedBeadsModuleDirOrFatal(t, goModuleCache(t), dep)
}

func compiledPinnedBeadsModule(bi *debug.BuildInfo) *debug.Module {
	if bi != nil {
		for _, dep := range bi.Deps {
			if dep.Path == PinnedBeadsModulePath {
				return dep
			}
		}
	}
	return nil
}

func pinnedBeadsSourceForBuild(bi *debug.BuildInfo, manifest []byte) (*debug.Module, error) {
	if dep := compiledPinnedBeadsModule(bi); dep != nil {
		return dep, nil
	}
	mf, err := modfile.Parse("go.mod", manifest, nil)
	if err != nil {
		return nil, fmt.Errorf("parse repository beads pin: %w", err)
	}
	var dep *debug.Module
	for _, req := range mf.Require {
		if req.Mod.Path == PinnedBeadsModulePath {
			dep = &debug.Module{Path: req.Mod.Path, Version: req.Mod.Version}
			break
		}
	}
	if dep == nil {
		return nil, fmt.Errorf("go.mod does not require %s", PinnedBeadsModulePath)
	}
	// A version-specific replacement wins over a wildcard, regardless of order.
	var replacement *modfile.Replace
	for _, repl := range mf.Replace {
		if repl.Old.Path != dep.Path {
			continue
		}
		if repl.Old.Version == dep.Version {
			replacement = repl
			break
		}
		if repl.Old.Version == "" {
			replacement = repl
		}
	}
	if replacement != nil {
		if replacement.New.Version == "" {
			return nil, fmt.Errorf("pinned beads replacement %s has no versioned source", replacement.New.Path)
		}
		dep.Replace = &debug.Module{Path: replacement.New.Path, Version: replacement.New.Version}
	}
	return dep, nil
}

// bazelRunfilesBeadsModule locates the pinned beads module inside the bazel
// runfiles tree, matching any repository directory whose name ends in the
// go_deps canonical suffix and carrying the module's migration directories.
func bazelRunfilesBeadsModule() string {
	for _, rf := range []string{os.Getenv("RUNFILES_DIR"), os.Getenv("TEST_SRCDIR")} {
		if rf == "" {
			continue
		}
		entries, err := os.ReadDir(rf)
		if err != nil {
			continue
		}
		for _, e := range entries {
			name := e.Name()
			if !strings.Contains(name, "go_deps+com_github_steveyegge_beads") {
				continue
			}
			dir := filepath.Join(rf, name)
			if info, err := os.Stat(filepath.Join(dir, "internal", "storage", "schema", "migrations")); err == nil && info.IsDir() {
				return dir
			}
		}
	}
	return ""
}

// moduleDirReporter is the subset of *testing.T the seam below uses.
//
// It exists so that "an unresolved cache is FATAL, never a skip" can be asserted
// by a recorder in this package's own tests. The only other way to observe it is
// a *testing.T in a subprocess, and a subprocess in a test grows the repo's
// shrink-only resource census — the same reason this file resolves the module
// cache inline instead of asking `go env`. Skipf is part of the interface on
// purpose: a revert to it still compiles, and is caught by a test rather than by
// a reviewer.
type moduleDirReporter interface {
	Helper()
	Fatalf(format string, args ...any)
	Skipf(format string, args ...any)
}

// pinnedBeadsModuleDirOrFatal reports an unresolved module cache as a test
// failure. See PinnedBeadsModuleDir for why it cannot be a skip.
func pinnedBeadsModuleDirOrFatal(t moduleDirReporter, cache string, dep *debug.Module) string {
	t.Helper()
	dir, err := pinnedBeadsResolvedModuleDir(cache, dep)
	if err != nil {
		t.Fatalf("%v\n"+
			"The pinned %s source could not be resolved. "+
			"Check GOMODCACHE, GOPATH and GOENV (resolved cache: %s), and whether go.mod gained a "+
			"replace or the build moved to vendor mode — the pinned-cursor drift check cannot run "+
			"without the module's own migration directories, and it must not pass without running.",
			err, PinnedBeadsModulePath, cache)
	}
	return dir
}

// pinnedBeadsModuleDir is the resolution itself, separated from the test so that
// the failure path has a test of its own.
func pinnedBeadsModuleDir(cache, version string) (string, error) {
	return pinnedBeadsResolvedModuleDir(cache, &debug.Module{Path: PinnedBeadsModulePath, Version: version})
}

// pinnedBeadsResolvedModuleDir follows the selected source replacement and
// the Go module cache's case escaping, without reading another checkout.
func pinnedBeadsResolvedModuleDir(cache string, dep *debug.Module) (string, error) {
	if dep.Replace != nil {
		dep = dep.Replace
	}
	if dep.Version == "" {
		return "", fmt.Errorf("pinned beads %s has no versioned source", dep.Path)
	}
	path, err := module.EscapePath(dep.Path)
	if err != nil {
		return "", err
	}
	version, err := module.EscapeVersion(dep.Version)
	if err != nil {
		return "", err
	}
	dir := filepath.Join(cache, filepath.FromSlash(path)+"@"+version)
	info, err := os.Stat(dir)
	switch {
	case err != nil:
		return "", fmt.Errorf("pinned beads %s@%s is not unpacked in the module cache at %s: %w", dep.Path, dep.Version, dir, err)
	case !info.IsDir():
		return "", fmt.Errorf("pinned beads %s@%s resolved to %s, which is not a directory", dep.Path, dep.Version, dir)
	default:
		return dir, nil
	}
}

// PinnedBeadsVersion reads the beads version this module requires from go.mod.
func PinnedBeadsVersion(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(RepositoryRoot(t), "go.mod"))
	if err != nil {
		t.Fatalf("read go.mod: %v", err)
	}
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(strings.TrimSpace(line))
		if len(fields) >= 2 && fields[0] == PinnedBeadsModulePath {
			return fields[1]
		}
		if len(fields) >= 3 && fields[0] == "require" && fields[1] == PinnedBeadsModulePath {
			return fields[2]
		}
	}
	t.Fatalf("go.mod does not require %s", PinnedBeadsModulePath)
	return ""
}

// RepositoryRoot walks up from the working directory to the module root.
func RepositoryRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("resolve working directory: %v", err)
	}
	for {
		if info, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil && !info.IsDir() {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("no go.mod above the package directory")
		}
		dir = parent
	}
}

// goModuleCache resolves the module cache the way cmd/go/internal/cfg does: the
// process environment first, then the `go env -w` config file ($GOENV, else
// os.UserConfigDir()/go/env, with "off" meaning no file), then the default
// $GOPATH/pkg/mod with GOPATH resolved by the same two steps and defaulting to
// $HOME/go. Only the first element of a GOPATH list holds the module cache.
func goModuleCache(t *testing.T) string {
	t.Helper()
	if dir := goEnvValue("GOMODCACHE"); dir != "" {
		return dir
	}
	gopath := goEnvValue("GOPATH")
	if gopath == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			t.Fatalf("resolve home directory to default GOPATH: %v", err)
		}
		gopath = filepath.Join(home, "go")
	}
	roots := filepath.SplitList(gopath)
	if len(roots) == 0 || roots[0] == "" {
		t.Fatalf("GOPATH %q has no usable first element", gopath)
	}
	return filepath.Join(roots[0], "pkg", "mod")
}

// goEnvValue reads one go environment variable: process environment, then the
// `go env -w` config file.
func goEnvValue(name string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return goEnvFileValue(name)
}

// goEnvFileValue reads one key out of the go env config file.
func goEnvFileValue(name string) string {
	path := strings.TrimSpace(os.Getenv("GOENV"))
	switch path {
	case "off":
		return ""
	case "", "auto":
		dir, err := os.UserConfigDir()
		if err != nil {
			return ""
		}
		path = filepath.Join(dir, "go", "env")
	}
	data, err := os.ReadFile(path) // #nosec G304 -- the go env config path, resolved the way the go command resolves it
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(data), "\n") {
		key, value, ok := strings.Cut(strings.TrimSpace(line), "=")
		if ok && key == name {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
