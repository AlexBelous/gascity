package config

import (
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/fsys"
)

// TestLoadWithIncludesWarnsAndIgnoresRetiredTickDebounce is WD.0's second
// negative: a city that still carries the retired [daemon].tick_debounce key
// must keep loading — with a surfaced warning, never a hard failure — and must
// tick at exactly the cadence it declares. It follows the
// session_start_reconciler retirement precedent
// (TestLoadWithIncludesWarnsAndIgnoresRetiredSessionStartReconciler): the key
// is no longer decoded, so it lands in the non-fatal unknown-field warning
// path for city.toml.
func TestLoadWithIncludesWarnsAndIgnoresRetiredTickDebounce(t *testing.T) {
	fs := fsys.NewFake()
	fs.Files["/city/city.toml"] = []byte(`
[workspace]
name = "test"

[daemon]
patrol_interval = "10s"
tick_debounce = "500ms"
`)

	cfg, prov, err := LoadWithIncludes(fs, "/city/city.toml")
	if err != nil {
		t.Fatalf("LoadWithIncludes: %v", err)
	}
	if got := cfg.Daemon.PatrolIntervalDuration(); got != 10*time.Second {
		t.Fatalf("PatrolIntervalDuration() = %v, want 10s (cadence unchanged by the retired key)", got)
	}
	if !containsWarningPrefix(prov.Warnings, `/city/city.toml: unknown field "daemon.tick_debounce"`) {
		t.Fatalf("warnings = %v, want retired tick-debounce warning", prov.Warnings)
	}
}
