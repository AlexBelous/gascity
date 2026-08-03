package testutil

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime/debug"
)

// pinnedBeadsModulePath is the module gascity's own in-process beads code
// (internal/beads) imports directly, and therefore the module whose exact
// resolved version a test-built bd CLI must match to guarantee compatible
// schema/migration knowledge (ga-r9cvmi).
const pinnedBeadsModulePath = "github.com/steveyegge/beads"

// PinnedBeadsModuleVersion reports the github.com/steveyegge/beads version
// this process's own build actually resolved, read from this binary's
// embedded build info rather than a `go list -m` subprocess or a go.mod text
// scan: debug.ReadBuildInfo reflects the exact resolved dependency graph
// (including any replace/exclude directives) with zero process spawn, and it
// can never itself drift from go.mod the way a second hardcoded version
// string could, since the compiler stamps it in at build time.
func PinnedBeadsModuleVersion() (string, error) {
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return "", fmt.Errorf("read build info: not available (binary not built with module support)")
	}
	for _, dep := range bi.Deps {
		if dep.Path != pinnedBeadsModulePath {
			continue
		}
		if dep.Replace != nil {
			return dep.Replace.Version, nil
		}
		return dep.Version, nil
	}
	return "", fmt.Errorf("%s not found in build info deps", pinnedBeadsModulePath)
}

// BuildPinnedBDBinary builds the bd CLI from the exact
// github.com/steveyegge/beads module version this test binary was built
// against, into binDir/bd. A bd resolved by searching PATH/home-dir
// locations instead carries no such guarantee: it can drift to a different
// schema version and fail deep inside a test with a cryptic mismatch error
// instead of cleanly at the point the drift actually originates (ga-r9cvmi).
//
// go install's "@version" form deliberately ignores any enclosing module's
// go.mod/go.sum and resolves the target module's own dependency closure in
// isolation, which is required here: cmd/bd's full dependency graph (CLI
// extras like AI-assisted duplicate detection, ADO rich-text rendering,
// telemetry exporters) is broader than what gascity's own go.sum carries,
// since gascity only imports internal/beads's storage packages.
//
// Callers own binDir's lifecycle (creation and cleanup).
func BuildPinnedBDBinary(binDir string) (string, error) {
	version, err := PinnedBeadsModuleVersion()
	if err != nil {
		return "", fmt.Errorf("resolve pinned beads module version: %w", err)
	}

	cmd := exec.Command("go", "install", "-tags", "gms_pure_go",
		pinnedBeadsModulePath+"/cmd/bd@"+version)
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0", "GOBIN="+binDir)
	if out, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("go install %s/cmd/bd@%s: %w\n%s", pinnedBeadsModulePath, version, err, out)
	}
	return filepath.Join(binDir, "bd"), nil
}
