package config

import (
	"strings"
	"testing"
)

// TestBeadsWorkTopologyDefaultsAbsent pins that a city with no [beads] infra or
// [beads.work] table keeps today's behavior: bd everywhere, scoped/managed
// defaults, and no infra-local resolution.
func TestBeadsWorkTopologyDefaultsAbsent(t *testing.T) {
	cfg, err := Parse([]byte(`[workspace]
name = "test"
`))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	b := cfg.Beads
	if b.EffectiveInfraLocal() {
		t.Error("EffectiveInfraLocal() = true with no topology config; want false")
	}
	if got := b.Work.EffectiveScope(); got != BeadsWorkScopeScoped {
		t.Errorf("EffectiveScope() = %q, want %q", got, BeadsWorkScopeScoped)
	}
	if got := b.Work.EffectiveTarget(); got != BeadsWorkTargetManaged {
		t.Errorf("EffectiveTarget() = %q, want %q", got, BeadsWorkTargetManaged)
	}
	if b.Work.IsUnified() || b.Work.IsRemote() {
		t.Error("scoped/managed defaults reported as unified/remote")
	}
	for _, class := range []string{BeadClassGraph, BeadClassMessaging, BeadClassSessions, BeadClassOrders, BeadClassNudges} {
		if got := b.ClassBackend(class); got != BeadsClassBackendBD {
			t.Errorf("ClassBackend(%q) = %q, want %q with no topology", class, got, BeadsClassBackendBD)
		}
	}
}

// TestBeadsInfraLocalParses pins the [beads] infra="local" aggregate: it parses,
// resolves every relocatable class to sqlite, and leaves the work class on bd.
func TestBeadsInfraLocalParses(t *testing.T) {
	cfg, err := Parse([]byte(`[workspace]
name = "test"

[beads]
infra = "local"
`))
	if err != nil {
		t.Fatalf("Parse rejected infra=local: %v", err)
	}
	if !cfg.Beads.EffectiveInfraLocal() {
		t.Fatal("EffectiveInfraLocal() = false with infra=local")
	}
	for _, class := range []string{BeadClassGraph, BeadClassMessaging, BeadClassSessions, BeadClassOrders, BeadClassNudges} {
		if got := cfg.Beads.ClassBackend(class); got != BeadsClassBackendSQLite {
			t.Errorf("ClassBackend(%q) = %q, want %q under infra=local", class, got, BeadsClassBackendSQLite)
		}
	}
	if got := cfg.Beads.ClassBackend(BeadClassWork); got != BeadsClassBackendBD {
		t.Errorf("ClassBackend(work) = %q, want %q (work never relocates)", got, BeadsClassBackendBD)
	}
}

// TestBeadsInfraUnknownRejected pins that any infra value other than ""/"local"
// (notably "bd", which has no such value) fails load, quoting the offender.
func TestBeadsInfraUnknownRejected(t *testing.T) {
	for _, bad := range []string{"bd", "remote", "sqlite", "Local"} {
		_, err := Parse([]byte(`[workspace]
name = "test"

[beads]
infra = "` + bad + `"
`))
		if err == nil {
			t.Fatalf("Parse accepted infra=%q", bad)
		}
		if !strings.Contains(err.Error(), "beads.infra") || !strings.Contains(err.Error(), bad) {
			t.Errorf("infra=%q error %q does not name the field and the bad value", bad, err)
		}
	}
}

// TestBeadsWorkScopeUnknownRejected pins the scope enum: only ""/scoped/unified
// admit; a typo fails load rather than silently meaning scoped.
func TestBeadsWorkScopeUnknownRejected(t *testing.T) {
	_, err := Parse([]byte(`[workspace]
name = "test"

[beads.work]
scope = "scopd"
`))
	if err == nil {
		t.Fatal("Parse accepted unknown scope value")
	}
	if !strings.Contains(err.Error(), "beads.work.scope") || !strings.Contains(err.Error(), "scopd") {
		t.Errorf("error %q does not name the field and the bad value", err)
	}
}

// TestBeadsWorkScopeUnifiedParses pins scope="unified": it parses, marks the
// config infra-local, and resolves relocatable classes to sqlite.
func TestBeadsWorkScopeUnifiedParses(t *testing.T) {
	cfg, err := Parse([]byte(`[workspace]
name = "test"

[beads.work]
scope = "unified"
`))
	if err != nil {
		t.Fatalf("Parse rejected scope=unified: %v", err)
	}
	if !cfg.Beads.Work.IsUnified() {
		t.Error("IsUnified() = false with scope=unified")
	}
	if !cfg.Beads.EffectiveInfraLocal() {
		t.Error("scope=unified must imply infra-local")
	}
	if got := cfg.Beads.ClassBackend(BeadClassSessions); got != BeadsClassBackendSQLite {
		t.Errorf("ClassBackend(sessions) = %q, want %q under unified", got, BeadsClassBackendSQLite)
	}
}

// TestBeadsWorkTargetParseTable pins the strict dolt://host:port/database parse
// on the RemoteTarget helper: every part required, port numeric and in range.
func TestBeadsWorkTargetParseTable(t *testing.T) {
	accept := []struct {
		target   string
		host     string
		port     int
		database string
	}{
		{"dolt://db.example.com:3306/beads", "db.example.com", 3306, "beads"},
		{"dolt://10.0.0.5:5432/org_tasks", "10.0.0.5", 5432, "org_tasks"},
		{"dolt://[::1]:3306/db", "::1", 3306, "db"},
	}
	for _, tc := range accept {
		h, p, d, ok := BeadsWorkConfig{Target: tc.target}.RemoteTarget()
		if !ok {
			t.Errorf("RemoteTarget(%q) rejected a valid remote target", tc.target)
			continue
		}
		if h != tc.host || p != tc.port || d != tc.database {
			t.Errorf("RemoteTarget(%q) = (%q,%d,%q), want (%q,%d,%q)", tc.target, h, p, d, tc.host, tc.port, tc.database)
		}
	}

	reject := []string{
		"",                          // → managed
		"managed",                   // sentinel, not remote
		"dolt://host/db",            // missing port
		"dolt://host:abc/db",        // non-numeric port
		"dolt://host:3306",          // missing database
		"dolt://host:3306/",         // empty database
		"dolt://:3306/db",           // empty host
		"dolt://host:99999/db",      // port out of range
		"dolt://host:0/db",          // port out of range
		"dolt://host:+3306/db",      // signed port (ParseUint rejects '+')
		"dolt://host:-1/db",         // signed port
		"http://host:3306/db",       // wrong scheme
		"dolt://host:3306/db/extra", // extra path segment
		"dolt://host:3306/db?x=1",   // query in database segment
		"dolt://host:3306/db#frag",  // fragment in database segment
	}
	for _, target := range reject {
		if _, _, _, ok := (BeadsWorkConfig{Target: target}).RemoteTarget(); ok {
			t.Errorf("RemoteTarget(%q) accepted an invalid remote target", target)
		}
	}
}

// TestBeadsWorkTargetRemoteRequiresUnified pins the ladder rule: a remote target
// is legal only with scope=unified; remote+scoped is a load error.
func TestBeadsWorkTargetRemoteRequiresUnified(t *testing.T) {
	_, err := Parse([]byte(`[workspace]
name = "test"

[beads.work]
target = "dolt://db:3306/org"
`))
	if err == nil {
		t.Fatal("Parse accepted a remote target without scope=unified")
	}
	if !strings.Contains(err.Error(), "beads.work.target") || !strings.Contains(err.Error(), "unified") {
		t.Errorf("error %q does not explain the remote-requires-unified rule", err)
	}
}

// TestBeadsWorkTargetRemoteUnifiedParses pins the top rung: scope=unified plus a
// well-formed remote target parses and reports remote+infra-local.
func TestBeadsWorkTargetRemoteUnifiedParses(t *testing.T) {
	cfg, err := Parse([]byte(`[workspace]
name = "test"

[beads.work]
scope = "unified"
target = "dolt://db.example.com:3306/org_tasks"
`))
	if err != nil {
		t.Fatalf("Parse rejected unified+remote: %v", err)
	}
	if !cfg.Beads.Work.IsRemote() || !cfg.Beads.EffectiveInfraLocal() {
		t.Error("unified+remote must report IsRemote() and EffectiveInfraLocal()")
	}
	h, p, d, ok := cfg.Beads.Work.RemoteTarget()
	if !ok || h != "db.example.com" || p != 3306 || d != "org_tasks" {
		t.Errorf("RemoteTarget() = (%q,%d,%q,%v), want (db.example.com,3306,org_tasks,true)", h, p, d, ok)
	}
}

// TestBeadsWorkTargetInvalidRejected pins that a malformed remote target fails
// load rather than silently collapsing to managed.
func TestBeadsWorkTargetInvalidRejected(t *testing.T) {
	_, err := Parse([]byte(`[workspace]
name = "test"

[beads.work]
scope = "unified"
target = "dolt://db/org"
`))
	if err == nil {
		t.Fatal("Parse accepted a malformed remote target")
	}
	if !strings.Contains(err.Error(), "beads.work.target") || !strings.Contains(err.Error(), "dolt://db/org") {
		t.Errorf("error %q does not name the field and the bad value", err)
	}
}

// TestEffectiveInfraLocalTruthTable exercises the resolution directly (no load
// validation), including remote-without-unified which load forbids but the
// resolver still reports local.
func TestEffectiveInfraLocalTruthTable(t *testing.T) {
	cases := []struct {
		name string
		b    BeadsConfig
		want bool
	}{
		{"empty", BeadsConfig{}, false},
		{"infra-local", BeadsConfig{Infra: BeadsInfraLocal}, true},
		{"scope-scoped", BeadsConfig{Work: BeadsWorkConfig{Scope: BeadsWorkScopeScoped}}, false},
		{"scope-unified", BeadsConfig{Work: BeadsWorkConfig{Scope: BeadsWorkScopeUnified}}, true},
		{"target-remote", BeadsConfig{Work: BeadsWorkConfig{Target: "dolt://h:1/d"}}, true},
		{"unified-remote", BeadsConfig{Work: BeadsWorkConfig{Scope: BeadsWorkScopeUnified, Target: "dolt://h:1/d"}}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.b.EffectiveInfraLocal(); got != tc.want {
				t.Errorf("EffectiveInfraLocal() = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestClassBackendPrecedence pins that an explicit per-class value wins over the
// aggregate in both directions: explicit sqlite stands, and explicit bd opts a
// class back out of infra-local (legal because infra-local, unlike a shared
// task DB, does not forbid it).
func TestClassBackendPrecedence(t *testing.T) {
	// explicit sqlite with no aggregate.
	explicitSQLite := BeadsConfig{Classes: map[string]BeadClassConfig{BeadClassOrders: {Backend: BeadsClassBackendSQLite}}}
	if got := explicitSQLite.ClassBackend(BeadClassOrders); got != BeadsClassBackendSQLite {
		t.Errorf("explicit sqlite: ClassBackend(orders) = %q, want sqlite", got)
	}
	if got := explicitSQLite.ClassBackend(BeadClassGraph); got != BeadsClassBackendBD {
		t.Errorf("no aggregate: ClassBackend(graph) = %q, want bd", got)
	}

	// infra-local with an explicit bd opt-out on one class.
	optOut := BeadsConfig{Infra: BeadsInfraLocal, Classes: map[string]BeadClassConfig{BeadClassGraph: {Backend: BeadsClassBackendBD}}}
	if got := optOut.ClassBackend(BeadClassGraph); got != BeadsClassBackendBD {
		t.Errorf("explicit bd under infra-local: ClassBackend(graph) = %q, want bd", got)
	}
	if got := optOut.ClassBackend(BeadClassSessions); got != BeadsClassBackendSQLite {
		t.Errorf("implied under infra-local: ClassBackend(sessions) = %q, want sqlite", got)
	}
}

// TestBeadsInfraLocalExplicitBDOptOutParses pins that the opt-out above is a
// legal load (only a shared task DB — unified/remote — forbids an explicit bd).
func TestBeadsInfraLocalExplicitBDOptOutParses(t *testing.T) {
	cfg, err := Parse([]byte(`[workspace]
name = "test"

[beads]
infra = "local"

[beads.classes.graph]
backend = "bd"
`))
	if err != nil {
		t.Fatalf("Parse rejected an explicit bd opt-out under infra=local: %v", err)
	}
	if got := cfg.Beads.ClassBackend(BeadClassGraph); got != BeadsClassBackendBD {
		t.Errorf("ClassBackend(graph) = %q, want bd", got)
	}
}

// TestExplicitBDUnderSharedTaskDBRejected pins deliverable D.3: pinning a class
// to bd while work beads route to a shared task DB (unified or remote) is a load
// error naming the class and the implying knob.
func TestExplicitBDUnderSharedTaskDBRejected(t *testing.T) {
	t.Run("unified", func(t *testing.T) {
		_, err := Parse([]byte(`[workspace]
name = "test"

[beads.work]
scope = "unified"

[beads.classes.sessions]
backend = "bd"
`))
		if err == nil {
			t.Fatal("Parse accepted backend=bd under scope=unified")
		}
		if !strings.Contains(err.Error(), "beads.classes.sessions") || !strings.Contains(err.Error(), `scope="unified"`) {
			t.Errorf("error %q does not name the class and the unified knob", err)
		}
	})
	t.Run("remote", func(t *testing.T) {
		_, err := Parse([]byte(`[workspace]
name = "test"

[beads.work]
scope = "unified"
target = "dolt://db:3306/org"

[beads.classes.messaging]
backend = "bd"
`))
		if err == nil {
			t.Fatal("Parse accepted backend=bd under a remote target")
		}
		if !strings.Contains(err.Error(), "beads.classes.messaging") || !strings.Contains(err.Error(), "dolt://db:3306/org") {
			t.Errorf("error %q does not name the class and the remote target knob", err)
		}
	})
}

// TestShadowUnderEffectiveSQLiteRejected pins deliverable D.4: shadow=true while
// the EFFECTIVE class backend is sqlite — implied by infra or scope — is a load
// error naming the implying knob. (The explicit-sqlite case is pinned by
// TestBeadsClassesShadowWithSQLiteBackendRejected.)
func TestShadowUnderEffectiveSQLiteRejected(t *testing.T) {
	t.Run("scope-unified", func(t *testing.T) {
		_, err := Parse([]byte(`[workspace]
name = "test"

[beads.work]
scope = "unified"

[beads.classes.sessions]
shadow = true
`))
		if err == nil {
			t.Fatal("Parse accepted shadow=true under scope=unified")
		}
		if !strings.Contains(err.Error(), "beads.classes.sessions") || !strings.Contains(err.Error(), `scope="unified"`) {
			t.Errorf("error %q does not name the class and the unified knob", err)
		}
	})
	t.Run("infra-local", func(t *testing.T) {
		_, err := Parse([]byte(`[workspace]
name = "test"

[beads]
infra = "local"

[beads.classes.sessions]
shadow = true
`))
		if err == nil {
			t.Fatal("Parse accepted shadow=true under infra=local")
		}
		if !strings.Contains(err.Error(), "beads.classes.sessions") || !strings.Contains(err.Error(), `infra="local"`) {
			t.Errorf("error %q does not name the class and the infra knob", err)
		}
	})
}

// TestValidateBeadsClassPrefixesImpliedActive pins deliverable D.5: the reserved
// prefix rejection fires under an implied infra-local backend (no explicit
// [beads.classes] entry), not only under an explicit non-bd class.
func TestValidateBeadsClassPrefixesImpliedActive(t *testing.T) {
	shadowRig := []Rig{{Name: "mailrig", Path: "/tmp/r", Prefix: "gcm"}}

	t.Run("infra-local-rig-shadow-fatal", func(t *testing.T) {
		cfg := &City{Rigs: shadowRig, Beads: BeadsConfig{Infra: BeadsInfraLocal}}
		err := ValidateBeadsClassPrefixes(cfg)
		if err == nil {
			t.Fatal("shadowing rig prefix accepted under infra=local")
		}
		if !strings.Contains(err.Error(), "mailrig") || !strings.Contains(err.Error(), "gcm") {
			t.Errorf("error %q does not name the rig and prefix", err)
		}
		// Finding 5: with only an aggregate set, name the implying knob, not a
		// class backend the operator never configured.
		if !strings.Contains(err.Error(), `infra="local"`) || strings.Contains(err.Error(), "revert the class backend") {
			t.Errorf("implied-activation error should name the infra knob, not a class backend: %q", err)
		}
	})
	t.Run("all-bd-rig-shadow-inert", func(t *testing.T) {
		cfg := &City{Rigs: shadowRig}
		if err := ValidateBeadsClassPrefixes(cfg); err != nil {
			t.Fatalf("shadowing prefix rejected on an all-bd city: %v", err)
		}
	})
}

// TestUnifiedWorkPrefixDistinctness pins deliverable D.6: under scope=unified the
// HQ and rig work prefixes must be pairwise distinct (case-insensitive), and
// none may collide with a reserved class prefix.
func TestUnifiedWorkPrefixDistinctness(t *testing.T) {
	unified := BeadsConfig{Work: BeadsWorkConfig{Scope: BeadsWorkScopeUnified}}

	t.Run("duplicate-rig-prefix", func(t *testing.T) {
		cfg := &City{
			Workspace: Workspace{Name: "hq", Prefix: "hq"},
			Rigs:      []Rig{{Name: "a", Path: "/tmp/a", Prefix: "dup"}, {Name: "b", Path: "/tmp/b", Prefix: "dup"}},
			Beads:     unified,
		}
		err := ValidateBeadsClassPrefixes(cfg)
		if err == nil {
			t.Fatal("unified city accepted two rigs with the same prefix")
		}
		if !strings.Contains(err.Error(), "dup") || !strings.Contains(err.Error(), `"a"`) || !strings.Contains(err.Error(), `"b"`) {
			t.Errorf("error %q does not name the colliding prefix and both rigs", err)
		}
	})
	t.Run("rig-vs-hq-case-insensitive", func(t *testing.T) {
		cfg := &City{
			Workspace: Workspace{Name: "hq", Prefix: "abc"},
			Rigs:      []Rig{{Name: "a", Path: "/tmp/a", Prefix: "ABC"}},
			Beads:     unified,
		}
		err := ValidateBeadsClassPrefixes(cfg)
		if err == nil {
			t.Fatal("unified city accepted a rig prefix that case-insensitively collides with HQ")
		}
		if !strings.Contains(err.Error(), "abc") {
			t.Errorf("error %q does not name the colliding prefix", err)
		}
	})
	t.Run("reserved-prefix-collision", func(t *testing.T) {
		cfg := &City{
			Workspace: Workspace{Name: "hq", Prefix: "hq"},
			Rigs:      []Rig{{Name: "mailrig", Path: "/tmp/r", Prefix: "gcm"}},
			Beads:     unified,
		}
		err := ValidateBeadsClassPrefixes(cfg)
		if err == nil {
			t.Fatal("unified city accepted a rig prefix colliding with a reserved class prefix")
		}
		if !strings.Contains(err.Error(), "gcm") {
			t.Errorf("error %q does not name the reserved prefix", err)
		}
	})
	t.Run("distinct-prefixes-ok", func(t *testing.T) {
		cfg := &City{
			Workspace: Workspace{Name: "hq", Prefix: "hq"},
			Rigs:      []Rig{{Name: "a", Path: "/tmp/a", Prefix: "aa"}, {Name: "b", Path: "/tmp/b", Prefix: "bb"}},
			Beads:     unified,
		}
		if err := ValidateBeadsClassPrefixes(cfg); err != nil {
			t.Fatalf("distinct unified prefixes rejected: %v", err)
		}
	})
	t.Run("scoped-city-skips-distinctness", func(t *testing.T) {
		cfg := &City{
			Workspace: Workspace{Name: "hq", Prefix: "hq"},
			Rigs:      []Rig{{Name: "a", Path: "/tmp/a", Prefix: "dup"}, {Name: "b", Path: "/tmp/b", Prefix: "dup"}},
		}
		if err := ValidateBeadsClassPrefixes(cfg); err != nil {
			t.Fatalf("scoped city must not enforce prefix distinctness: %v", err)
		}
	})
}

// TestInfraLocalClassCapabilityRatchet pins finding 7: when EffectiveInfraLocal
// implies sqlite for a configurable class whose store has NOT landed, load is
// rejected — even though no explicit [beads.classes.<name>] entry set it. The
// capability map is injected so the test can simulate a not-yet-shipped class
// without mutating the package global (race-safe).
func TestInfraLocalClassCapabilityRatchet(t *testing.T) {
	// Simulate a build where graph landed in beadClassConfigurable before its
	// store shipped: every configurable class capable EXCEPT graph.
	narrowed := map[string]bool{
		BeadClassMessaging: true,
		BeadClassSessions:  true,
		BeadClassOrders:    true,
		BeadClassNudges:    true,
	}
	t.Run("aggregate-implies-uncapable-rejected", func(t *testing.T) {
		err := validateInfraLocalClassCapability(BeadsConfig{Infra: BeadsInfraLocal}, narrowed)
		if err == nil {
			t.Fatal("infra=local accepted an implied sqlite backend for a non-capable class")
		}
		if !strings.Contains(err.Error(), "beads.classes.graph") || !strings.Contains(err.Error(), `infra="local"`) {
			t.Errorf("error %q does not name the class and the implying knob", err)
		}
	})
	t.Run("explicit-bd-opt-out-clears-ratchet", func(t *testing.T) {
		b := BeadsConfig{Infra: BeadsInfraLocal, Classes: map[string]BeadClassConfig{BeadClassGraph: {Backend: BeadsClassBackendBD}}}
		if err := validateInfraLocalClassCapability(b, narrowed); err != nil {
			t.Errorf("explicit bd opt-out should clear the ratchet, got %v", err)
		}
	})
	t.Run("all-capable-build-ok", func(t *testing.T) {
		if err := validateInfraLocalClassCapability(BeadsConfig{Infra: BeadsInfraLocal}, sqliteCapableBeadClasses); err != nil {
			t.Errorf("all-capable build rejected infra=local: %v", err)
		}
	})
	t.Run("no-aggregate-skips-ratchet", func(t *testing.T) {
		if err := validateInfraLocalClassCapability(BeadsConfig{}, narrowed); err != nil {
			t.Errorf("ratchet fired with no aggregate active: %v", err)
		}
	})
}

// TestRetiredGraphStoreFoldsToGraphSQLite pins finding 3 on the Parse path: a
// b36 city.toml whose only routing knob is graph_store="sqlite" folds onto the
// replacement graph class backend so the city routes graph to its SQLite store
// instead of booting graph-blind on bd.
func TestRetiredGraphStoreFoldsToGraphSQLite(t *testing.T) {
	t.Run("sqlite-folds", func(t *testing.T) {
		cfg, err := Parse([]byte(`[workspace]
name = "test"

[beads]
graph_store = "sqlite"
`))
		if err != nil {
			t.Fatalf("Parse rejected retired graph_store: %v", err)
		}
		if got := cfg.Beads.ClassBackend(BeadClassGraph); got != BeadsClassBackendSQLite {
			t.Errorf("ClassBackend(graph) = %q, want %q folded from graph_store", got, BeadsClassBackendSQLite)
		}
	})
	t.Run("explicit-class-backend-wins", func(t *testing.T) {
		cfg, err := Parse([]byte(`[workspace]
name = "test"

[beads]
graph_store = "sqlite"

[beads.classes.graph]
backend = "bd"
`))
		if err != nil {
			t.Fatalf("Parse: %v", err)
		}
		if got := cfg.Beads.ClassBackend(BeadClassGraph); got != BeadsClassBackendBD {
			t.Errorf("explicit backend=bd must win over the folded value, got %q", got)
		}
	})
	t.Run("dolt-value-does-not-fold", func(t *testing.T) {
		cfg, err := Parse([]byte(`[workspace]
name = "test"

[beads]
graph_store = "dolt"
`))
		if err != nil {
			t.Fatalf("Parse: %v", err)
		}
		if got := cfg.Beads.ClassBackend(BeadClassGraph); got != BeadsClassBackendBD {
			t.Errorf("graph_store=dolt must leave graph on bd, got %q", got)
		}
	})
}
