package config

import (
	"strings"
	"testing"
)

func TestBeadsClassBackendDefaultsToBD(t *testing.T) {
	cfg, err := Parse([]byte(`[workspace]
name = "test"
`))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	for _, class := range []string{BeadClassGraph, BeadClassMessaging, BeadClassSessions, BeadClassOrders, BeadClassNudges} {
		if got := cfg.Beads.ClassBackend(class); got != BeadsClassBackendBD {
			t.Errorf("ClassBackend(%q) = %q, want %q", class, got, BeadsClassBackendBD)
		}
	}
}

func TestBeadsClassesParseExplicitBD(t *testing.T) {
	cfg, err := Parse([]byte(`[workspace]
name = "test"

[beads.classes.orders]
backend = "bd"
`))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got := cfg.Beads.ClassBackend(BeadClassOrders); got != BeadsClassBackendBD {
		t.Errorf("ClassBackend(orders) = %q, want %q", got, BeadsClassBackendBD)
	}
}

func TestBeadsClassesUnknownClassRejected(t *testing.T) {
	_, err := Parse([]byte(`[workspace]
name = "test"

[beads.classes.mailx]
backend = "bd"
`))
	if err == nil {
		t.Fatal("Parse accepted unknown class name mailx")
	}
	if !strings.Contains(err.Error(), "beads.classes.mailx") {
		t.Errorf("error %q does not name the offending key", err)
	}
}

func TestBeadsClassesWorkNotConfigurable(t *testing.T) {
	_, err := Parse([]byte(`[workspace]
name = "test"

[beads.classes.work]
backend = "bd"
`))
	if err == nil {
		t.Fatal("Parse accepted [beads.classes.work]")
	}
	if !strings.Contains(err.Error(), "work") {
		t.Errorf("error %q does not mention the work class", err)
	}
}

func TestBeadsClassesUnknownBackendRejected(t *testing.T) {
	_, err := Parse([]byte(`[workspace]
name = "test"

[beads.classes.orders]
backend = "dolt"
`))
	if err == nil {
		t.Fatal("Parse accepted unknown backend value")
	}
	if !strings.Contains(err.Error(), "beads.classes.orders") || !strings.Contains(err.Error(), "dolt") {
		t.Errorf("error %q does not name the key and the bad value", err)
	}
}

// Reserved-class prefix shadowing stays a non-fatal advisory while every
// class rides bd (relocation inert; an existing city must keep starting), and
// becomes fatal the moment any class backend goes non-bd (per-class by-id
// routing would be ambiguous). Configs are built as literals because Parse
// rejects backend=sqlite until stores land.
func TestValidateBeadsClassPrefixes(t *testing.T) {
	shadowRig := []Rig{{Name: "mailrig", Path: "/tmp/r", Prefix: "gcm"}}
	activeClasses := BeadsConfig{Classes: map[string]BeadClassConfig{
		BeadClassOrders: {Backend: BeadsClassBackendSQLite},
	}}

	t.Run("inert-city-shadow-allowed", func(t *testing.T) {
		cfg := &City{Rigs: shadowRig}
		if err := ValidateBeadsClassPrefixes(cfg); err != nil {
			t.Fatalf("shadowing prefix rejected on an all-bd city: %v", err)
		}
	})
	t.Run("active-city-rig-shadow-fatal", func(t *testing.T) {
		cfg := &City{Rigs: shadowRig, Beads: activeClasses}
		err := ValidateBeadsClassPrefixes(cfg)
		if err == nil {
			t.Fatal("shadowing rig prefix accepted with an active non-bd class backend")
		}
		if !strings.Contains(err.Error(), "mailrig") || !strings.Contains(err.Error(), "gcm") {
			t.Errorf("error %q does not name the rig and prefix", err)
		}
	})
	t.Run("active-city-hq-shadow-fatal", func(t *testing.T) {
		cfg := &City{Workspace: Workspace{Name: "x", Prefix: "gcs"}, Beads: activeClasses}
		err := ValidateBeadsClassPrefixes(cfg)
		if err == nil {
			t.Fatal("shadowing HQ prefix accepted with an active non-bd class backend")
		}
		if !strings.Contains(err.Error(), "gcs") {
			t.Errorf("error %q does not name the HQ prefix", err)
		}
	})
	t.Run("active-city-clean-prefixes-ok", func(t *testing.T) {
		cfg := &City{Rigs: []Rig{{Name: "r1", Path: "/tmp/r", Prefix: "r1"}}, Beads: activeClasses}
		if err := ValidateBeadsClassPrefixes(cfg); err != nil {
			t.Fatalf("clean prefixes rejected: %v", err)
		}
	})
}

// The sqlite backend unlocks per class as each class's store lands. Until
// then a config requesting it must fail at load, not silently run on bd.
func TestBeadsClassesSQLiteRejectedUntilImplemented(t *testing.T) {
	for _, class := range []string{BeadClassGraph, BeadClassMessaging, BeadClassSessions, BeadClassOrders, BeadClassNudges} {
		if sqliteCapableBeadClasses[class] {
			continue // exercised by that class's own store tests once it lands
		}
		_, err := Parse([]byte(`[workspace]
name = "test"

[beads.classes.` + class + `]
backend = "sqlite"
`))
		if err == nil {
			t.Fatalf("Parse accepted backend=sqlite for unimplemented class %q", class)
		}
		if !strings.Contains(err.Error(), "beads.classes."+class) {
			t.Errorf("error %q does not name the offending class %q", err, class)
		}
	}
}

// TestBeadsClassesOrdersSQLiteAccepted pins the orders capability flip: the
// internal/classdb/orders store has landed, so [beads.classes.orders]
// backend="sqlite" parses (other classes stay fail-closed via
// TestBeadsClassesSQLiteRejectedUntilImplemented's ratchet).
func TestBeadsClassesOrdersSQLiteAccepted(t *testing.T) {
	cfg, err := Parse([]byte(`[workspace]
name = "test"

[beads.classes.orders]
backend = "sqlite"
`))
	if err != nil {
		t.Fatalf("Parse rejected orders sqlite backend: %v", err)
	}
	if got := cfg.Beads.ClassBackend(BeadClassOrders); got != BeadsClassBackendSQLite {
		t.Errorf("ClassBackend(orders) = %q, want %q", got, BeadsClassBackendSQLite)
	}
}

// TestBeadsClassesNudgesSQLiteAccepted pins the nudges capability flip: the
// internal/classdb/nudges merged-queue store has landed, so
// [beads.classes.nudges] backend="sqlite" parses.
func TestBeadsClassesNudgesSQLiteAccepted(t *testing.T) {
	cfg, err := Parse([]byte(`[workspace]
name = "test"

[beads.classes.nudges]
backend = "sqlite"
`))
	if err != nil {
		t.Fatalf("Parse rejected nudges sqlite backend: %v", err)
	}
	if got := cfg.Beads.ClassBackend(BeadClassNudges); got != BeadsClassBackendSQLite {
		t.Errorf("ClassBackend(nudges) = %q, want %q", got, BeadsClassBackendSQLite)
	}
}

// TestBeadsClassesSessionsSQLiteAccepted pins the sessions capability
// flip: the internal/classdb/sessions store has landed, so
// [beads.classes.sessions] backend="sqlite" parses.
func TestBeadsClassesSessionsSQLiteAccepted(t *testing.T) {
	cfg, err := Parse([]byte(`[workspace]
name = "test"

[beads.classes.sessions]
backend = "sqlite"
`))
	if err != nil {
		t.Fatalf("Parse rejected sessions sqlite backend: %v", err)
	}
	if got := cfg.Beads.ClassBackend(BeadClassSessions); got != BeadsClassBackendSQLite {
		t.Errorf("ClassBackend(sessions) = %q, want %q", got, BeadsClassBackendSQLite)
	}
}

// TestBeadsClassesSessionsShadowAccepted pins the sessions shadow-write
// gate knob: [beads.classes.sessions] shadow=true parses while the backend
// stays bd, and ClassShadow reports it.
func TestBeadsClassesSessionsShadowAccepted(t *testing.T) {
	cfg, err := Parse([]byte(`[workspace]
name = "test"

[beads.classes.sessions]
shadow = true
`))
	if err != nil {
		t.Fatalf("Parse rejected sessions shadow: %v", err)
	}
	if !cfg.Beads.ClassShadow(BeadClassSessions) {
		t.Error("ClassShadow(sessions) = false, want true")
	}
	if got := cfg.Beads.ClassBackend(BeadClassSessions); got != BeadsClassBackendBD {
		t.Errorf("ClassBackend(sessions) = %q, want %q (shadow keeps bd authoritative)", got, BeadsClassBackendBD)
	}
}

// TestBeadsClassesShadowRejectedForOtherClasses pins that shadow is a
// sessions-only stage: the ephemeral classes cut over on their migrated
// marker alone and must not accept the knob.
func TestBeadsClassesShadowRejectedForOtherClasses(t *testing.T) {
	for _, class := range []string{BeadClassGraph, BeadClassMessaging, BeadClassOrders, BeadClassNudges} {
		_, err := Parse([]byte(`[workspace]
name = "test"

[beads.classes.` + class + `]
shadow = true
`))
		if err == nil {
			t.Fatalf("Parse accepted shadow=true for class %q", class)
		}
		if !strings.Contains(err.Error(), "beads.classes."+class) {
			t.Errorf("error %q does not name the offending class %q", err, class)
		}
	}
}

// TestBeadsClassesShadowWithSQLiteBackendRejected pins the contradictory
// combination: once the backend routes the class to its store, shadow has
// nothing to tee.
func TestBeadsClassesShadowWithSQLiteBackendRejected(t *testing.T) {
	_, err := Parse([]byte(`[workspace]
name = "test"

[beads.classes.sessions]
backend = "sqlite"
shadow = true
`))
	if err == nil {
		t.Fatal("Parse accepted shadow=true with backend=sqlite")
	}
	if !strings.Contains(err.Error(), "beads.classes.sessions") {
		t.Errorf("error %q does not name the sessions class", err)
	}
}

// TestBeadsClassesMessagingSQLiteAccepted pins the messaging capability
// flip: the internal/classdb/messaging store (mail messages + extmsg typed
// tables, one file) has landed, so [beads.classes.messaging]
// backend="sqlite" parses.
func TestBeadsClassesMessagingSQLiteAccepted(t *testing.T) {
	cfg, err := Parse([]byte(`[workspace]
name = "test"

[beads.classes.messaging]
backend = "sqlite"
`))
	if err != nil {
		t.Fatalf("Parse rejected messaging sqlite backend: %v", err)
	}
	if got := cfg.Beads.ClassBackend(BeadClassMessaging); got != BeadsClassBackendSQLite {
		t.Errorf("ClassBackend(messaging) = %q, want %q", got, BeadsClassBackendSQLite)
	}
}
