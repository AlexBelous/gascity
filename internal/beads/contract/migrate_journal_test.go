package contract

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gastownhall/gascity/internal/bazeltest"
	"golang.org/x/mod/modfile"
	"golang.org/x/mod/module"
)

// TestMigrateJournalFileMatchesPinnedBeads reads the name out of the pinned
// beads module rather than restating it.
//
// The previous spelling of this constant was a plausible-looking guess
// ("migrate-dolt-mode.json"). Nothing failed: the product's post-migration
// verification stat'd a file that could never exist, so it always passed, and so
// did the tests asserting the residue was gone. A constant that is only ever
// compared against itself proves nothing, so this one is compared against bd.
//
// It resolves the module directory from go.mod plus the module cache, and it
// resolves the cache the way the go command does rather than by reading the
// GOMODCACHE variable. Reading the variable silently disabled the comparison:
// `go test` does not export GOMODCACHE into the test process, so the test
// skipped under every plain `go test` — an editor runner, a hook, `go test
// ./...` — and ran only because make's TEST_ENV happens to forward it. A
// contract check that is honest under one wrapper is not a contract check.
//
// Neither does it shell out to `go env`: a subprocess here grows the source
// resource census (verified — the untagged subprocess baseline rejects it) and
// that ratchet is shrink-only, so the resolution is inlined instead.
func TestMigrateJournalFileMatchesPinnedBeads(t *testing.T) {
	modCache := goModuleCache(t)
	data, err := os.ReadFile(filepath.Join(repositoryRoot(t), "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := pinnedBeadsSource(data)
	if err != nil {
		t.Fatal(err)
	}
	version := resolved.Version
	file, err := pinnedBeadsJournalFile(modCache, resolved, []string{os.Getenv("RUNFILES_DIR"), os.Getenv("TEST_SRCDIR")})
	if err != nil {
		t.Fatal(err)
	}
	source, err := os.ReadFile(file)
	if err != nil {
		t.Fatalf("read resolved beads journal source %s: %v", file, err)
	}

	const decl = `migrateJournalFileName = "`
	idx := strings.Index(string(source), decl)
	if idx < 0 {
		t.Fatalf("pinned beads %s no longer declares migrateJournalFileName; re-derive %s by hand", version, MigrateDoltModeJournalFile)
	}
	rest := string(source)[idx+len(decl):]
	end := strings.Index(rest, `"`)
	if end < 0 {
		t.Fatalf("could not parse migrateJournalFileName out of the pinned beads source")
	}
	if got := rest[:end]; got != MigrateDoltModeJournalFile {
		t.Fatalf("MigrateDoltModeJournalFile = %q, pinned bd writes %q", MigrateDoltModeJournalFile, got)
	}
}

// goModuleCache resolves the module cache the way the go command does.
//
// Mirrors cmd/go/internal/cfg: the process environment first, then the `go env
// -w` config file ($GOENV, else os.UserConfigDir()/go/env, and "off" means no
// file), then the default $GOPATH/pkg/mod with GOPATH resolved by the same two
// steps and defaulting to $HOME/go. Only the first element of a GOPATH list
// holds the module cache. Every step is a file or an environment read, so the
// test carries no subprocess.
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
	data, err := os.ReadFile(path)
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

// pinnedBeadsJournalFile uses declared Bazel data before the Go module cache.
// A Bazel sandbox must not silently fall back to undeclared host files.
func pinnedBeadsJournalFile(cache string, source module.Version, runfileRoots []string) (string, error) {
	bazel := false
	for _, root := range runfileRoots {
		if root == "" {
			continue
		}
		bazel = true
		entries, err := os.ReadDir(root)
		if err != nil {
			return "", err
		}
		for _, entry := range entries {
			if !strings.Contains(entry.Name(), "go_deps+com_github_steveyegge_beads") {
				continue
			}
			file := filepath.Join(root, entry.Name(), "cmd", "bd", "migrate_dolt_mode.go")
			if info, err := os.Stat(file); err == nil && !info.IsDir() {
				return file, nil
			}
		}
	}
	if bazel {
		return "", fmt.Errorf("pinned beads migration journal source is absent from declared Bazel runfiles")
	}
	path, err := module.EscapePath(source.Path)
	if err != nil {
		return "", err
	}
	version, err := module.EscapeVersion(source.Version)
	if err != nil {
		return "", err
	}
	return filepath.Join(cache, path+"@"+version, "cmd", "bd", "migrate_dolt_mode.go"), nil
}

func TestPinnedBeadsJournalUsesDeclaredRunfiles(t *testing.T) {
	root := t.TempDir()
	want := filepath.Join(root, "gazelle++go_deps+com_github_steveyegge_beads", "cmd", "bd", "migrate_dolt_mode.go")
	if err := os.MkdirAll(filepath.Dir(want), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(want, []byte("source"), 0o600); err != nil {
		t.Fatal(err)
	}
	source := module.Version{Path: "github.com/AlexBelous/beads", Version: "v1.3.0"}
	got, err := pinnedBeadsJournalFile("/unused-host-cache", source, []string{root})
	if err != nil || got != want {
		t.Fatalf("runfile=%q err=%v, want %q", got, err, want)
	}
	if _, err := pinnedBeadsJournalFile("/unused-host-cache", source, []string{t.TempDir()}); err == nil {
		t.Fatal("missing Bazel data fell back to host cache")
	}
}

// pinnedBeadsSource reads the requirement from go.mod, then applies a
// matching version-specific replacement before a wildcard replacement.
func pinnedBeadsSource(data []byte) (module.Version, error) {
	f, err := modfile.Parse("go.mod", data, nil)
	if err != nil {
		return module.Version{}, err
	}
	var source module.Version
	for _, dep := range f.Require {
		if dep.Mod.Path == "github.com/steveyegge/beads" {
			source = dep.Mod
			break
		}
	}
	if source.Path == "" {
		return source, fmt.Errorf("go.mod does not require beads")
	}
	var wildcard *modfile.Replace
	for _, replacement := range f.Replace {
		if replacement.Old.Path != source.Path {
			continue
		}
		if replacement.Old.Version == source.Version {
			if replacement.New.Version == "" {
				return module.Version{}, fmt.Errorf("unversioned beads source %s", replacement.New.Path)
			}
			return replacement.New, nil
		}
		if replacement.Old.Version == "" {
			wildcard = replacement
		}
	}
	if wildcard != nil {
		source = wildcard.New
	}
	if source.Version == "" {
		return module.Version{}, fmt.Errorf("unversioned beads source %s", source.Path)
	}
	return source, nil
}

func TestPinnedBeadsJournalSourceUsesMatchingReplacement(t *testing.T) {
	manifest := []byte("module example.test/fixture\nrequire github.com/steveyegge/beads v1.3.0\nreplace (\ngithub.com/steveyegge/beads => github.com/other/beads v1.2.0\ngithub.com/steveyegge/beads v1.3.0 => github.com/AlexBelous/beads v1.1.1-0.20260928222722-da08f27390f1\n)\n")
	got, err := pinnedBeadsSource(manifest)
	if err != nil {
		t.Fatal(err)
	}
	want := (module.Version{Path: "github.com/AlexBelous/beads", Version: "v1.1.1-0.20260928222722-da08f27390f1"})
	if got != want {
		t.Fatalf("journal source = %#v, want resolved fork %#v", got, want)
	}
	for _, test := range []struct {
		name, manifest string
		want           module.Version
		wantError      bool
	}{
		{"unreplaced", "module example.test/fixture\nrequire github.com/steveyegge/beads v1.3.0\n", module.Version{Path: "github.com/steveyegge/beads", Version: "v1.3.0"}, false},
		{"wildcard with unrelated version", "module example.test/fixture\nrequire github.com/steveyegge/beads v1.3.0\nreplace github.com/steveyegge/beads v1.2.0 => github.com/wrong/beads v1.2.0\nreplace github.com/steveyegge/beads => github.com/AlexBelous/beads v1.3.0\n", module.Version{Path: "github.com/AlexBelous/beads", Version: "v1.3.0"}, false},
		{"local source", "module example.test/fixture\nrequire github.com/steveyegge/beads v1.3.0\nreplace github.com/steveyegge/beads => ../beads\n", module.Version{}, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := pinnedBeadsSource([]byte(test.manifest))
			if (err != nil) != test.wantError || (!test.wantError && got != test.want) {
				t.Fatalf("source=%#v err=%v, want %#v error=%v", got, err, test.want, test.wantError)
			}
		})
	}
}

// TestGoModuleCacheResolutionOrder pins the precedence the skip regression turned
// on. A plain `go test` exports no GOMODCACHE, so if the config file and the
// GOPATH default are not reachable the contract check above goes quiet again.
func TestGoModuleCacheResolutionOrder(t *testing.T) {
	envFile := filepath.Join(t.TempDir(), "env")
	contents := "GOFLAGS=-mod=mod\nGOMODCACHE=/from/config/file\nGOPATH=/from/config/gopath\n"
	if err := os.WriteFile(envFile, []byte(contents), 0o600); err != nil {
		t.Fatalf("write go env file: %v", err)
	}

	t.Run("environment wins", func(t *testing.T) {
		t.Setenv("GOENV", envFile)
		t.Setenv("GOMODCACHE", "/from/environment")
		if got := goModuleCache(t); got != "/from/environment" {
			t.Fatalf("goModuleCache() = %q, want /from/environment", got)
		}
	})

	t.Run("config file when the environment is silent", func(t *testing.T) {
		t.Setenv("GOENV", envFile)
		t.Setenv("GOMODCACHE", "")
		if got := goModuleCache(t); got != "/from/config/file" {
			t.Fatalf("goModuleCache() = %q, want /from/config/file", got)
		}
	})

	t.Run("GOPATH default when neither names the cache", func(t *testing.T) {
		t.Setenv("GOENV", "off")
		t.Setenv("GOMODCACHE", "")
		t.Setenv("GOPATH", "/from/gopath"+string(os.PathListSeparator)+"/ignored")
		want := filepath.Join("/from/gopath", "pkg", "mod")
		if got := goModuleCache(t); got != want {
			t.Fatalf("goModuleCache() = %q, want %q", got, want)
		}
	})
}

// repositoryRoot walks up from the package directory to the module root.
func repositoryRoot(t *testing.T) string {
	t.Helper()
	if root := bazeltest.OverrideRoot(); root != "" {
		if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
			t.Fatalf("GC_TEST_REPO_ROOT=%s has no go.mod: %v", root, err)
		}
		return root
	}
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
