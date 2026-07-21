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
