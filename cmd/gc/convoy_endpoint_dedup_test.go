package main

import (
	"path/filepath"
	"testing"
)

// TestConvoyStoreCandidatesEndpointDedup pins the residual-C convoy fix: on a
// topology-active city the aliased rig scopes collapse to a single candidate so
// a by-id resolution / live-roots scan sees ONE store per endpoint instead of
// hard-failing "exists in multiple stores"; on a marker-less city the candidate
// list is byte-identical to the pre-topology behavior (DARK).
func TestConvoyStoreCandidatesEndpointDedup(t *testing.T) {
	nativeProvider := func(string) string { return "doltlite" }

	t.Run("unified aliases collapse to one candidate", func(t *testing.T) {
		city := t.TempDir()
		rigA := filepath.Join(city, "fe")
		rigB := filepath.Join(city, "be")
		writeScopeFiles(t, city, managedCityState(), "acme")
		writeScopeFiles(t, rigA, inheritedRigState(), "acme")
		writeScopeFiles(t, rigB, inheritedRigState(), "acme")
		writeUnifiedMarker(t, city)

		cfg := workCfg("unified", "managed", rigA, rigB)
		got := convoyStoreCandidatesWithProvider(cfg, city, "", nativeProvider)
		if len(got) != 1 || got[0] != city {
			t.Fatalf("aliased rigs must collapse to the single city candidate, got %v", got)
		}
	})

	t.Run("scoped distinct databases keep every candidate (DARK)", func(t *testing.T) {
		city := t.TempDir()
		rigA := filepath.Join(city, "fe")
		rigB := filepath.Join(city, "be")
		writeScopeFiles(t, city, managedCityState(), "hq")
		writeScopeFiles(t, rigA, managedCityState(), "fe")
		writeScopeFiles(t, rigB, managedCityState(), "be")
		// No unified marker: marker-less, DARK.

		cfg := workCfg("scoped", "managed", rigA, rigB)
		got := convoyStoreCandidatesWithProvider(cfg, city, "", nativeProvider)
		if len(got) != 3 {
			t.Fatalf("distinct-database scopes must each keep a candidate, got %v", got)
		}
	})

	t.Run("unified with a not-yet-re-pointed rig keeps the legacy leg", func(t *testing.T) {
		city := t.TempDir()
		rigA := filepath.Join(city, "fe")
		rigB := filepath.Join(city, "be")
		writeScopeFiles(t, city, managedCityState(), "acme")
		writeScopeFiles(t, rigA, inheritedRigState(), "acme")
		// rigB still on its own legacy database — a late-bound rig awaiting the
		// next canonicalization. It must NOT collapse into the city endpoint.
		writeScopeFiles(t, rigB, managedCityState(), "legacy-be")
		writeUnifiedMarker(t, city)

		cfg := workCfg("unified", "managed", rigA, rigB)
		got := convoyStoreCandidatesWithProvider(cfg, city, "", nativeProvider)
		if len(got) != 2 {
			t.Fatalf("re-pointed rig collapses but a legacy rig keeps its leg; got %v", got)
		}
		if got[0] != city || got[1] != rigB {
			t.Fatalf("expected [city, legacy-rig], got %v", got)
		}
	})
}
