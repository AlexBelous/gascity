package main

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gastownhall/gascity/internal/agentutil"
	"github.com/gastownhall/gascity/internal/config"
	"github.com/gastownhall/gascity/internal/fsys"
	"github.com/gastownhall/gascity/internal/session"
)

func rigPathFor(t *testing.T, fixture packNamedPoolFixture, rigName string) string {
	t.Helper()
	for _, rig := range fixture.cfg.Rigs {
		if rig.Name == rigName {
			return rig.Path
		}
	}
	t.Fatalf("fixture rig %q not found", rigName)
	return ""
}

func TestResolvePoolShapedNamedSessionInstanceQualifiedInRange(t *testing.T) {
	t.Setenv("GC_DIR", "")
	fixture := newPackNamedPoolFixture(t, "gascity", "builder", 2, 3)
	cityName := fixture.cfg.EffectiveCityName()

	spec, ok, err := resolvePoolShapedNamedSessionInstance(fixture.cfg, cityName, fixture.identity+"-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ok {
		t.Fatalf("expected qualified in-range instance to resolve")
	}
	if got, want := spec.Identity, fixture.identity+"-1"; got != want {
		t.Fatalf("spec.Identity = %q, want %q", got, want)
	}
	if spec.Agent == nil {
		t.Fatalf("spec.Agent = nil, want populated instance agent")
	}
	if got, want := spec.Agent.QualifiedName(), fixture.identity+"-1"; got != want {
		t.Fatalf("spec.Agent.QualifiedName() = %q, want %q", got, want)
	}
	if spec.Named == nil {
		t.Fatalf("spec.Named = nil, want the backing named session")
	}
	if spec.SessionName == "" {
		t.Fatalf("spec.SessionName = empty, want a computed runtime session name")
	}
}

func TestResolvePoolShapedNamedSessionInstanceBareWithRigContext(t *testing.T) {
	fixture := newPackNamedPoolFixture(t, "gascity", "builder", 2, 3)
	t.Setenv("GC_DIR", rigPathFor(t, fixture, "gascity"))
	cityName := fixture.cfg.EffectiveCityName()

	spec, ok, err := resolvePoolShapedNamedSessionInstance(fixture.cfg, cityName, "builder-2")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ok {
		t.Fatalf("expected bare in-range instance to resolve under matching rig context")
	}
	if got, want := spec.Identity, fixture.identity+"-2"; got != want {
		t.Fatalf("spec.Identity = %q, want %q", got, want)
	}
}

func TestResolvePoolShapedNamedSessionInstanceBareWithoutRigContext(t *testing.T) {
	t.Setenv("GC_DIR", "")
	fixture := newPackNamedPoolFixture(t, "gascity", "builder", 2, 3)
	cityName := fixture.cfg.EffectiveCityName()

	_, ok, err := resolvePoolShapedNamedSessionInstance(fixture.cfg, cityName, "builder-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ok {
		t.Fatalf("bare target without rig context must not resolve a rig-scoped named session")
	}
}

func TestResolvePoolShapedNamedSessionInstanceOutOfRange(t *testing.T) {
	t.Setenv("GC_DIR", "")
	fixture := newPackNamedPoolFixture(t, "gascity", "builder", 2, 3)
	cityName := fixture.cfg.EffectiveCityName()

	_, ok, err := resolvePoolShapedNamedSessionInstance(fixture.cfg, cityName, fixture.identity+"-99")
	if ok {
		t.Fatalf("expected out-of-range slot to be rejected")
	}
	if err == nil {
		t.Fatalf("expected a clear error for an out-of-range slot")
	}
	if !errors.Is(err, session.ErrSessionNotFound) {
		t.Fatalf("error %v does not wrap session.ErrSessionNotFound", err)
	}
	if !strings.Contains(err.Error(), fixture.identity+"-99") {
		t.Fatalf("error %v does not name the offending target", err)
	}
}

func TestResolvePoolShapedNamedSessionInstanceNonNumericSuffix(t *testing.T) {
	t.Setenv("GC_DIR", "")
	fixture := newPackNamedPoolFixture(t, "gascity", "builder", 2, 3)
	cityName := fixture.cfg.EffectiveCityName()

	_, ok, err := resolvePoolShapedNamedSessionInstance(fixture.cfg, cityName, fixture.identity+"-abc")
	if ok {
		t.Fatalf("expected non-numeric slot suffix to be rejected")
	}
	if err == nil {
		t.Fatalf("expected a clear error for a non-numeric slot suffix")
	}
	if !errors.Is(err, session.ErrSessionNotFound) {
		t.Fatalf("error %v does not wrap session.ErrSessionNotFound", err)
	}
}

func TestResolvePoolShapedNamedSessionInstanceUnrelatedTarget(t *testing.T) {
	t.Setenv("GC_DIR", "")
	fixture := newPackNamedPoolFixture(t, "gascity", "builder", 2, 3)
	cityName := fixture.cfg.EffectiveCityName()

	_, ok, err := resolvePoolShapedNamedSessionInstance(fixture.cfg, cityName, "totally-unrelated-5")
	if err != nil {
		t.Fatalf("unexpected error for an unrelated target: %v", err)
	}
	if ok {
		t.Fatalf("unrelated target must not resolve")
	}
}

func TestResolvePoolShapedNamedSessionInstanceSingletonNotPoolShaped(t *testing.T) {
	t.Setenv("GC_DIR", "")
	fixture := newPackNamedPoolFixture(t, "gascity", "builder", 0, 1)
	cityName := fixture.cfg.EffectiveCityName()

	_, ok, err := resolvePoolShapedNamedSessionInstance(fixture.cfg, cityName, fixture.identity+"-1")
	if err != nil {
		t.Fatalf("unexpected error for a singleton named session: %v", err)
	}
	if ok {
		t.Fatalf("a singleton-shaped named session must not resolve a numbered instance")
	}
}

// TestSlingResolversHandlePoolShapedNamedSessionInstanceWithoutChange is FR-5 /
// NFR-2 coverage: cmd_agent.go's independent resolver family
// (resolveAgentIdentity, agentutil.NormalizePoolRouteTarget) must already
// route a pool-shaped named session's numbered instance correctly with no
// code change, since it resolves purely off the backing config.Agent's own
// pool capacity fields (SupportsInstanceExpansion /
// UsesCanonicalSingletonPoolIdentity) and is agnostic to whether that agent
// is also referenced by a [[named_session]]. This is deliberately independent
// of any session-bead/reconciler state (unlike
// TestPackNamedSessionPoolPatchWarmsFromBaseSessionToMin, which exercises the
// same assertions downstream of pool warm-to-min reconciliation that ga-3prlpb.4
// has not yet implemented).
func TestSlingResolversHandlePoolShapedNamedSessionInstanceWithoutChange(t *testing.T) {
	t.Setenv("GC_DIR", "")
	fixture := newPackNamedPoolFixture(t, "gascity", "builder", 2, 3)

	resolved, ok := resolveAgentIdentity(fixture.cfg, fixture.identity+"-1", "")
	if !ok {
		t.Fatalf("resolveAgentIdentity failed to resolve pool-shaped named session instance %s-1", fixture.identity)
	}
	if got, want := resolved.QualifiedName(), fixture.identity+"-1"; got != want {
		t.Fatalf("resolveAgentIdentity QualifiedName() = %q, want %q", got, want)
	}
	if got, want := agentutil.NormalizePoolRouteTarget(fixture.cfg, fixture.identity+"-1"), fixture.identity; got != want {
		t.Fatalf("agentutil.NormalizePoolRouteTarget(%s-1) = %q, want %q", fixture.identity, got, want)
	}

	if _, ok := resolveAgentIdentity(fixture.cfg, fixture.identity+"-99", ""); ok {
		t.Fatalf("resolveAgentIdentity must not resolve an out-of-range pool instance")
	}
}

// newPackNamedPoolCustomNameFixture is a sibling of newPackNamedPoolFixture
// with one difference: the named session sets a custom `name` distinct from
// its `template`. NamedSession.Name is "the configured public session
// identity" (config.go) — a documented, supported field — and FR-3 requires
// pool-instance routing/addressing to key off NamedSession.IdentityName(),
// not the backing agent's own QualifiedName(). newPackNamedPoolFixture never
// exercises this divergence (it never sets `name`), so this fixture exists to
// prove resolvePoolShapedNamedSessionInstance re-stamps the synthesized
// instance's identity onto the named session's identity rather than leaking
// the backing template's identity.
func newPackNamedPoolCustomNameFixture(t *testing.T, rigName, agentName, sessionName string, minActive, maxActive int) packNamedPoolFixture {
	t.Helper()

	root := t.TempDir()
	rigPath := filepath.Join(root, "rigs", rigName)
	packPath := filepath.Join(rigPath, "packs", "workflow")
	mustMkdirAllPackNamed(t, packPath)

	writeFixtureFile(t, filepath.Join(root, "city.toml"), fmt.Sprintf(`
[workspace]
name = "test-city"
session_template = "{{.City}}--{{.Agent}}"

[[rigs]]
name = %q
path = %q
includes = [%q]

[[rigs.patches]]
agent = %q
min_active_sessions = %d
max_active_sessions = %d
`, rigName, rigPath, packPath, agentName, minActive, maxActive))

	writeFixtureFile(t, filepath.Join(packPath, "pack.toml"), fmt.Sprintf(`
[pack]
name = "workflow"
schema = 1

[[agent]]
name = %q
scope = "rig"
start_command = "true"
min_active_sessions = 0
max_active_sessions = 1

[[named_session]]
name = %q
template = %q
scope = "rig"
mode = "on_demand"
`, agentName, sessionName, agentName))

	cityPath := filepath.Join(root, "city.toml")
	cfg, _, err := config.LoadWithIncludes(fsys.OSFS{}, cityPath)
	if err != nil {
		t.Fatalf("loading fixture city config: %v", err)
	}

	identity := rigName + "/" + sessionName
	if config.FindNamedSession(cfg, identity) == nil {
		t.Fatalf("fixture named session %s not found", identity)
	}

	return packNamedPoolFixture{cityPath: cityPath, cfg: cfg, identity: identity}
}

// TestResolvePoolShapedNamedSessionInstanceCustomNameDiffersFromTemplate is
// FR-3 coverage for the case its own doc comment calls out explicitly: the
// base string for pool-instance routing/addressing must be
// NamedSession.IdentityName(), not assumed equal to Agent.QualifiedName().
// Before the identity re-stamp in resolvePoolShapedNamedSessionInstance,
// spec.Agent.QualifiedName() leaked the backing template's identity
// ("rig/writer-1") instead of the named session's own identity
// ("rig/leadwriter-1") — which would make downstream materialization
// (materializeSessionForAgentConfig, keyed off spec.Agent's own identity
// fields) create/address the session under the wrong name.
func TestResolvePoolShapedNamedSessionInstanceCustomNameDiffersFromTemplate(t *testing.T) {
	t.Setenv("GC_DIR", "")
	fixture := newPackNamedPoolCustomNameFixture(t, "gascity", "writer", "leadwriter", 2, 3)
	cityName := fixture.cfg.EffectiveCityName()

	spec, ok, err := resolvePoolShapedNamedSessionInstance(fixture.cfg, cityName, fixture.identity+"-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ok {
		t.Fatalf("expected custom-named pool instance to resolve")
	}
	if got, want := spec.Identity, fixture.identity+"-1"; got != want {
		t.Fatalf("spec.Identity = %q, want %q", got, want)
	}
	if spec.Agent == nil {
		t.Fatalf("spec.Agent = nil, want populated instance agent")
	}
	if got, want := spec.Agent.QualifiedName(), fixture.identity+"-1"; got != want {
		t.Fatalf("spec.Agent.QualifiedName() = %q, want %q (must key off the named session's identity, not the backing template's)", got, want)
	}
}
