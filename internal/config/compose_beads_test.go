package config

import (
	"strings"
	"testing"

	"github.com/gastownhall/gascity/internal/fsys"
)

// TestLoadWithIncludesDefaultsConditionalWrites: omitted → default "off".
func TestLoadWithIncludesDefaultsConditionalWrites(t *testing.T) {
	fs := fsys.NewFake()
	fs.Files["/city/city.toml"] = []byte(`
[workspace]
name = "test"
`)
	cfg, _, err := LoadWithIncludes(fs, "/city/city.toml")
	if err != nil {
		t.Fatalf("LoadWithIncludes: %v", err)
	}
	if got := cfg.Beads.NormalizedConditionalWrites(); got != "off" {
		t.Fatalf("NormalizedConditionalWrites = %q, want off when omitted", got)
	}
}

// TestLoadWithIncludesPreservesExplicitConditionalWrites: explicit value with no
// fragment survives.
func TestLoadWithIncludesPreservesExplicitConditionalWrites(t *testing.T) {
	fs := fsys.NewFake()
	fs.Files["/city/city.toml"] = []byte(`
[workspace]
name = "test"

[beads]
conditional_writes = "require"
`)
	cfg, _, err := LoadWithIncludes(fs, "/city/city.toml")
	if err != nil {
		t.Fatalf("LoadWithIncludes: %v", err)
	}
	if got := cfg.Beads.NormalizedConditionalWrites(); got != "require" {
		t.Fatalf("NormalizedConditionalWrites = %q, want require", got)
	}
}

// TestLoadWithIncludesPreservesConditionalWritesAcrossBeadsFragment is the
// load-bearing regression: an included fragment that defines ONLY an unrelated
// [beads] sibling key must NOT reset the root's explicit conditional_writes.
// Without the per-field IsDefined preservation branch this is a silent
// require→off downgrade through routine config layering.
func TestLoadWithIncludesPreservesConditionalWritesAcrossBeadsFragment(t *testing.T) {
	fs := fsys.NewFake()
	fs.Files["/city/city.toml"] = []byte(`
include = ["fragment.toml"]

[workspace]
name = "test"

[beads]
conditional_writes = "require"
`)
	fs.Files["/city/fragment.toml"] = []byte(`
[beads]
bd_compatibility = "bd-1.0.5"
`)
	cfg, _, err := LoadWithIncludes(fs, "/city/city.toml")
	if err != nil {
		t.Fatalf("LoadWithIncludes: %v", err)
	}
	if got := cfg.Beads.NormalizedConditionalWrites(); got != "require" {
		t.Fatalf("NormalizedConditionalWrites = %q, want root require to survive a [beads] fragment", got)
	}
	if cfg.Beads.NormalizedBDCompatibility() != "bd-1.0.5" {
		t.Fatalf("BDCompatibility = %q, want the fragment's bd-1.0.5", cfg.Beads.NormalizedBDCompatibility())
	}
}

// TestLoadWithIncludesFragmentOverridesConditionalWrites is the companion to the
// preservation test: a fragment that DOES set conditional_writes must win (LWW),
// so the preservation branch can't drift into "base value always wins."
func TestLoadWithIncludesFragmentOverridesConditionalWrites(t *testing.T) {
	fs := fsys.NewFake()
	fs.Files["/city/city.toml"] = []byte(`
include = ["fragment.toml"]

[workspace]
name = "test"

[beads]
conditional_writes = "off"
`)
	fs.Files["/city/fragment.toml"] = []byte(`
[beads]
conditional_writes = "auto"
`)
	cfg, _, err := LoadWithIncludes(fs, "/city/city.toml")
	if err != nil {
		t.Fatalf("LoadWithIncludes: %v", err)
	}
	if got := cfg.Beads.NormalizedConditionalWrites(); got != "auto" {
		t.Fatalf("NormalizedConditionalWrites = %q, want the fragment's auto to win", got)
	}
}

// TestConditionalWritesParseAndDefault covers decode and the accessor default.
func TestConditionalWritesParseAndDefault(t *testing.T) {
	// zero value / omitted → default "off".
	if (BeadsConfig{}).NormalizedConditionalWrites() != "off" {
		t.Fatalf("zero-value accessor = %q, want off", (BeadsConfig{}).NormalizedConditionalWrites())
	}
	// an explicit value decodes.
	out, err := Parse([]byte("[beads]\nconditional_writes = \"auto\"\n"))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if out.Beads.ConditionalWrites != "auto" {
		t.Fatalf("decoded conditional_writes = %q, want auto", out.Beads.ConditionalWrites)
	}
	// a [beads] section without the key leaves it empty (→ default via accessor).
	out2, err := Parse([]byte("[beads]\nprovider = \"bd\"\n"))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if out2.Beads.ConditionalWrites != "" || out2.Beads.NormalizedConditionalWrites() != "off" {
		t.Fatalf("unset conditional_writes = %q (norm %q), want empty→off", out2.Beads.ConditionalWrites, out2.Beads.NormalizedConditionalWrites())
	}
}

// TestLoadWithIncludesDefaultsGuardedRelease: omitted → default "off".
func TestLoadWithIncludesDefaultsGuardedRelease(t *testing.T) {
	fs := fsys.NewFake()
	fs.Files["/city/city.toml"] = []byte(`
[workspace]
name = "test"
`)
	cfg, _, err := LoadWithIncludes(fs, "/city/city.toml")
	if err != nil {
		t.Fatalf("LoadWithIncludes: %v", err)
	}
	if got := cfg.Beads.NormalizedGuardedRelease(); got != "off" {
		t.Fatalf("NormalizedGuardedRelease = %q, want off when omitted", got)
	}
}

// TestLoadWithIncludesPreservesGuardedReleaseAcrossBeadsFragment is the
// load-bearing regression, mirroring the conditional_writes case: an included
// fragment that defines ONLY an unrelated [beads] sibling key must NOT reset the
// root's explicit guarded_release. Without the per-field IsDefined preservation
// branch this is a silent require→off downgrade through routine config layering.
func TestLoadWithIncludesPreservesGuardedReleaseAcrossBeadsFragment(t *testing.T) {
	fs := fsys.NewFake()
	fs.Files["/city/city.toml"] = []byte(`
include = ["fragment.toml"]

[workspace]
name = "test"

[beads]
guarded_release = "require"
`)
	fs.Files["/city/fragment.toml"] = []byte(`
[beads]
bd_compatibility = "bd-1.0.5"
`)
	cfg, _, err := LoadWithIncludes(fs, "/city/city.toml")
	if err != nil {
		t.Fatalf("LoadWithIncludes: %v", err)
	}
	if got := cfg.Beads.NormalizedGuardedRelease(); got != "require" {
		t.Fatalf("NormalizedGuardedRelease = %q, want root require to survive a [beads] fragment", got)
	}
	if cfg.Beads.NormalizedBDCompatibility() != "bd-1.0.5" {
		t.Fatalf("BDCompatibility = %q, want the fragment's bd-1.0.5", cfg.Beads.NormalizedBDCompatibility())
	}
}

// TestLoadWithIncludesFragmentOverridesGuardedRelease is the companion: a
// fragment that DOES set guarded_release must win (LWW), so the preservation
// branch can't drift into "base value always wins."
func TestLoadWithIncludesFragmentOverridesGuardedRelease(t *testing.T) {
	fs := fsys.NewFake()
	fs.Files["/city/city.toml"] = []byte(`
include = ["fragment.toml"]

[workspace]
name = "test"

[beads]
guarded_release = "off"
`)
	fs.Files["/city/fragment.toml"] = []byte(`
[beads]
guarded_release = "auto"
`)
	cfg, _, err := LoadWithIncludes(fs, "/city/city.toml")
	if err != nil {
		t.Fatalf("LoadWithIncludes: %v", err)
	}
	if got := cfg.Beads.NormalizedGuardedRelease(); got != "auto" {
		t.Fatalf("NormalizedGuardedRelease = %q, want the fragment's auto to win", got)
	}
}

// TestGuardedReleasePreservationIsIndependentOfConditionalWrites proves the two
// [beads] rollout-gate preservation branches don't clobber each other: a
// fragment that overrides ONE gate must leave the other's explicit root value
// intact. This is the regression a copy-paste of the preservation branch
// (capturing/restoring the wrong field) would introduce.
func TestGuardedReleasePreservationIsIndependentOfConditionalWrites(t *testing.T) {
	fs := fsys.NewFake()
	fs.Files["/city/city.toml"] = []byte(`
include = ["fragment.toml"]

[workspace]
name = "test"

[beads]
conditional_writes = "require"
guarded_release = "require"
`)
	// Fragment overrides only guarded_release; conditional_writes must survive.
	fs.Files["/city/fragment.toml"] = []byte(`
[beads]
guarded_release = "auto"
`)
	cfg, _, err := LoadWithIncludes(fs, "/city/city.toml")
	if err != nil {
		t.Fatalf("LoadWithIncludes: %v", err)
	}
	if got := cfg.Beads.NormalizedGuardedRelease(); got != "auto" {
		t.Fatalf("NormalizedGuardedRelease = %q, want the fragment's auto", got)
	}
	if got := cfg.Beads.NormalizedConditionalWrites(); got != "require" {
		t.Fatalf("NormalizedConditionalWrites = %q, want root require preserved when only guarded_release is overridden", got)
	}
}

// TestGuardedReleaseParseAndValidate covers decode, the accessor default, and
// the load-time rejection of an out-of-enum value.
func TestGuardedReleaseParseAndValidate(t *testing.T) {
	// zero value / omitted → default "off".
	if (BeadsConfig{}).NormalizedGuardedRelease() != "off" {
		t.Fatalf("zero-value accessor = %q, want off", (BeadsConfig{}).NormalizedGuardedRelease())
	}
	// an explicit value decodes.
	out, err := Parse([]byte("[beads]\nguarded_release = \"auto\"\n"))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if out.Beads.GuardedRelease != "auto" {
		t.Fatalf("decoded guarded_release = %q, want auto", out.Beads.GuardedRelease)
	}
	// an out-of-enum value fails load (a typo must never silently mean off).
	if _, err := Parse([]byte("[beads]\nguarded_release = \"requre\"\n")); err == nil {
		t.Fatalf("expected an error for an out-of-enum guarded_release value")
	}
}

// TestLoadWithIncludesEnforcesBeadsTopology pins finding 1: the beads-topology
// load-error contract holds on the LAYERED load path (the one most of cmd/gc
// uses), not only through Parse. A typo'd scope must fail load. The backend
// subtest also pins that the pre-existing validateBeadsClasses hole on this seam
// is now closed as a side effect.
func TestLoadWithIncludesEnforcesBeadsTopology(t *testing.T) {
	t.Run("typoed-scope", func(t *testing.T) {
		fs := fsys.NewFake()
		fs.Files["/city/city.toml"] = []byte(`
[workspace]
name = "test"

[beads.work]
scope = "unifed"
`)
		_, _, err := LoadWithIncludes(fs, "/city/city.toml")
		if err == nil {
			t.Fatal("LoadWithIncludes accepted a typo'd scope (silent collapse to scoped)")
		}
		if !containsAll(err.Error(), "beads.work.scope", "unifed") {
			t.Errorf("error %q does not name the field and the bad value", err)
		}
	})
	t.Run("typoed-class-backend-preexisting-hole-now-closed", func(t *testing.T) {
		fs := fsys.NewFake()
		fs.Files["/city/city.toml"] = []byte(`
[workspace]
name = "test"

[beads.classes.orders]
backend = "sqlit"
`)
		_, _, err := LoadWithIncludes(fs, "/city/city.toml")
		if err == nil {
			t.Fatal("LoadWithIncludes accepted an unknown class backend (pre-existing hole)")
		}
		if !containsAll(err.Error(), "beads.classes.orders", "sqlit") {
			t.Errorf("error %q does not name the class and the bad value", err)
		}
	})
}

// TestLoadWithIncludesPreservesInfraAcrossBeadsFragment pins finding 2: a
// fragment that defines an unrelated [beads] sibling key must NOT reset the
// root's [beads] infra aggregate — otherwise a migrated city silently reverts
// every class to bd while live data sits in the sqlite stores.
func TestLoadWithIncludesPreservesInfraAcrossBeadsFragment(t *testing.T) {
	fs := fsys.NewFake()
	fs.Files["/city/city.toml"] = []byte(`
include = ["fragment.toml"]

[workspace]
name = "test"

[beads]
infra = "local"
`)
	fs.Files["/city/fragment.toml"] = []byte(`
[beads]
event_hooks = false
`)
	cfg, _, err := LoadWithIncludes(fs, "/city/city.toml")
	if err != nil {
		t.Fatalf("LoadWithIncludes: %v", err)
	}
	if !cfg.Beads.EffectiveInfraLocal() {
		t.Fatal("root infra=local did not survive a [beads] fragment")
	}
	if got := cfg.Beads.ClassBackend(BeadClassGraph); got != BeadsClassBackendSQLite {
		t.Errorf("ClassBackend(graph) = %q, want %q after fragment merge", got, BeadsClassBackendSQLite)
	}
	if cfg.Beads.EventHooksEnabled() {
		t.Error("fragment event_hooks=false should still apply (LWW for the key it set)")
	}
}

// TestLoadWithIncludesPreservesWorkAcrossBeadsFragment pins finding 2 for the
// [beads.work] aggregate.
func TestLoadWithIncludesPreservesWorkAcrossBeadsFragment(t *testing.T) {
	fs := fsys.NewFake()
	fs.Files["/city/city.toml"] = []byte(`
include = ["fragment.toml"]

[workspace]
name = "test"

[beads.work]
scope = "unified"
`)
	fs.Files["/city/fragment.toml"] = []byte(`
[beads]
event_hooks = false
`)
	cfg, _, err := LoadWithIncludes(fs, "/city/city.toml")
	if err != nil {
		t.Fatalf("LoadWithIncludes: %v", err)
	}
	if !cfg.Beads.Work.IsUnified() {
		t.Fatal("root [beads.work] scope=unified did not survive a [beads] fragment")
	}
}

// TestLoadWithIncludesFragmentOverridesWork is the LWW companion: a fragment
// that DOES set [beads.work] wins, so the preservation branch can't drift into
// "base always wins."
func TestLoadWithIncludesFragmentOverridesWork(t *testing.T) {
	fs := fsys.NewFake()
	fs.Files["/city/city.toml"] = []byte(`
include = ["fragment.toml"]

[workspace]
name = "test"

[beads.work]
scope = "scoped"
`)
	fs.Files["/city/fragment.toml"] = []byte(`
[beads.work]
scope = "unified"
`)
	cfg, _, err := LoadWithIncludes(fs, "/city/city.toml")
	if err != nil {
		t.Fatalf("LoadWithIncludes: %v", err)
	}
	if !cfg.Beads.Work.IsUnified() {
		t.Fatalf("fragment [beads.work] scope=unified should win, got scope=%q", cfg.Beads.Work.EffectiveScope())
	}
}

// TestLoadWithIncludesHonorsRetiredGraphStore pins finding 3 on the layered
// path: a b36 city.toml carrying only graph_store="sqlite" loads with a retired
// warning AND routes the graph class to its SQLite store.
func TestLoadWithIncludesHonorsRetiredGraphStore(t *testing.T) {
	fs := fsys.NewFake()
	fs.Files["/city/city.toml"] = []byte(`
[workspace]
name = "test"

[beads]
graph_store = "sqlite"
`)
	cfg, prov, err := LoadWithIncludes(fs, "/city/city.toml")
	if err != nil {
		t.Fatalf("LoadWithIncludes rejected retired graph_store: %v", err)
	}
	if got := cfg.Beads.ClassBackend(BeadClassGraph); got != BeadsClassBackendSQLite {
		t.Errorf("ClassBackend(graph) = %q, want %q folded from graph_store", got, BeadsClassBackendSQLite)
	}
	warned := false
	for _, w := range prov.Warnings {
		if containsAll(w, "graph_store", "was retired in") {
			warned = true
			break
		}
	}
	if !warned {
		t.Errorf("expected a retired-key warning for graph_store, got %v", prov.Warnings)
	}
}

func containsAll(s string, subs ...string) bool {
	for _, sub := range subs {
		if !strings.Contains(s, sub) {
			return false
		}
	}
	return true
}
