package scripts_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestLintUsesReadonlyModuleDownloads(t *testing.T) {
	configPath := filepath.Join(repoRoot(t), ".golangci.yml")
	body, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read %s: %v", configPath, err)
	}

	var config struct {
		Run struct {
			ModulesDownloadMode string `yaml:"modules-download-mode"`
		} `yaml:"run"`
	}
	if err := yaml.Unmarshal(body, &config); err != nil {
		t.Fatalf("parse %s: %v", configPath, err)
	}
	if config.Run.ModulesDownloadMode != "readonly" {
		t.Fatalf("run.modules-download-mode = %q, want readonly", config.Run.ModulesDownloadMode)
	}

	makefile, err := os.ReadFile(filepath.Join(repoRoot(t), "Makefile"))
	if err != nil {
		t.Fatalf("read Makefile: %v", err)
	}
	const readonlyGOFlags = "LINT_READONLY_GOFLAGS = $$(go env GOFLAGS | sed -E 's/(^|[[:space:]])-mod=[^[:space:]]+//g') -mod=readonly"
	if !strings.Contains(string(makefile), readonlyGOFlags) {
		t.Fatalf("Makefile must derive LINT_READONLY_GOFLAGS from effective GOFLAGS")
	}
	for target, wantGOFLAGS := range map[string]string{
		"lint-full":     `GOFLAGS="$(LINT_READONLY_GOFLAGS)"`,
		"lint-new":      `GOFLAGS="$(LINT_READONLY_GOFLAGS)"`,
		"lint-changed":  `export GOFLAGS="$(LINT_READONLY_GOFLAGS)"`,
		"lint-affected": `GOFLAGS="$(LINT_READONLY_GOFLAGS)"`,
	} {
		t.Run(target, func(t *testing.T) {
			body := makeTargetBody(t, string(makefile), target)
			for _, override := range []string{"--config", "--no-config"} {
				if strings.Contains(body, override) {
					t.Fatalf("%s overrides shared lint configuration with %q", target, override)
				}
			}
			if strings.Contains(body, "--modules-download-mode") {
				t.Fatalf("%s must not rely on a lint CLI module-mode override", target)
			}
			if !strings.Contains(body, wantGOFLAGS) {
				t.Fatalf("%s must scope LINT_READONLY_GOFLAGS to its subprocess tree", target)
			}
		})
	}
}

func makeTargetBody(t *testing.T, makefile, target string) string {
	t.Helper()
	prefix := target + ":"
	start := strings.Index(makefile, prefix)
	if start < 0 {
		t.Fatalf("Makefile has no %s target", target)
	}
	body := makefile[start:]
	if next := strings.Index(body, "\n## "); next >= 0 {
		body = body[:next]
	}
	return body
}
