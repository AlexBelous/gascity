package main

import (
	"path/filepath"
	"testing"
)

// TestClassSweepSourceElidesRepointedRig pins deliverable E: on a topology-active
// city the class residue sweep's source list drops rig scopes re-pointed at the
// shared city database (they would re-scan the shared/org DB with deletion
// authority), while a marker-less city keeps every rig scope (DARK).
func TestClassSweepSourceElidesRepointedRig(t *testing.T) {
	newCity := func(t *testing.T, unified bool, rigDB string) string {
		city := t.TempDir()
		writeScopeFiles(t, city, managedCityState(), "hq")
		rig := filepath.Join(city, "fe")
		writeScopeFiles(t, rig, inheritedRigState(), rigDB)
		if unified {
			writeUnifiedMarker(t, city)
		}
		return city
	}

	t.Run("DARK: no marker keeps the rig scope", func(t *testing.T) {
		city := newCity(t, false, "fe")
		cfg := workCfg("", "", "fe")
		targets := orderTrackingSweepTargetsForConfig(city, cfg)
		if len(targets) != 2 {
			t.Fatalf("marker-less city must source city + rig (2 targets), got %d", len(targets))
		}
	})

	t.Run("unified + re-pointed rig is elided", func(t *testing.T) {
		// Rig resolves to the city database "hq" (re-pointed) → elided.
		city := newCity(t, true, "hq")
		cfg := workCfg("unified", "", "fe")
		targets := orderTrackingSweepTargetsForConfig(city, cfg)
		if len(targets) != 1 {
			t.Fatalf("re-pointed rig must be elided (city-only), got %d targets", len(targets))
		}
		if targets[0].target.ScopeKind != "city" {
			t.Fatalf("remaining target must be the city, got %q", targets[0].target.ScopeKind)
		}
	})

	t.Run("unified but rig NOT yet re-pointed is still sourced", func(t *testing.T) {
		// Rig still names its own legacy database "fe" (distinct endpoint) → kept,
		// so its class residue is still drained from the legacy DB.
		city := newCity(t, true, "fe")
		cfg := workCfg("unified", "", "fe")
		targets := orderTrackingSweepTargetsForConfig(city, cfg)
		if len(targets) != 2 {
			t.Fatalf("not-yet-re-pointed rig must still be sourced, got %d targets", len(targets))
		}
	})
}
