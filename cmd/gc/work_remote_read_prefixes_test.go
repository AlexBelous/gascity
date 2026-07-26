package main

import (
	"slices"
	"testing"

	"github.com/gastownhall/gascity/internal/config"
)

// TestRemoteWorkReadPrefixes pins deliverable D's remote gate: a complete
// work.remote marker yields the city's prefix set; scoped/unified/managed cities
// stay DARK (nil, false).
func TestRemoteWorkReadPrefixes(t *testing.T) {
	newCfg := func() *config.City {
		cfg := &config.City{}
		cfg.Workspace.Prefix = "hq"
		cfg.Rigs = []config.Rig{{Name: "fe", Prefix: "fe"}}
		return cfg
	}

	t.Run("DARK on a marker-less city", func(t *testing.T) {
		city := t.TempDir()
		if _, ok := remoteWorkReadPrefixes(city, newCfg()); ok {
			t.Fatal("marker-less city must be DARK")
		}
	})

	t.Run("DARK on a unified-but-not-remote city", func(t *testing.T) {
		city := t.TempDir()
		writeUnifiedMarker(t, city)
		if _, ok := remoteWorkReadPrefixes(city, newCfg()); ok {
			t.Fatal("unified (non-remote) city must be DARK — no org DB to isolate")
		}
	})

	t.Run("remote city yields the prefix set", func(t *testing.T) {
		city := t.TempDir()
		writeRemoteMarker(t, city, "db.example.com", "org")
		prefixes, ok := remoteWorkReadPrefixes(city, newCfg())
		if !ok {
			t.Fatal("remote city must expose its prefix set")
		}
		if !slices.Equal(prefixes, []string{"hq", "fe"}) {
			t.Fatalf("prefixes = %v, want [hq fe]", prefixes)
		}
	})
}
