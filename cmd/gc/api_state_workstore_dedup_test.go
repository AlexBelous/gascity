package main

import (
	"path/filepath"
	"testing"

	"github.com/gastownhall/gascity/internal/config"
)

// TestDedupeWorkStoreKeysEndpointCollapse pins the /v0/convoys residual-C
// surface: on a topology-active city the aliased rig store keys collapse to the
// city key so the convoy listing counts each shared-DB bead once; a marker-less
// city keeps every key (DARK).
func TestDedupeWorkStoreKeysEndpointCollapse(t *testing.T) {
	t.Run("aliased rigs collapse to the city key", func(t *testing.T) {
		city := t.TempDir()
		rigA := filepath.Join(city, "fe")
		rigB := filepath.Join(city, "be")
		writeScopeFiles(t, city, managedCityState(), "acme")
		writeScopeFiles(t, rigA, inheritedRigState(), "acme")
		writeScopeFiles(t, rigB, inheritedRigState(), "acme")
		writeUnifiedMarker(t, city)

		cs := &controllerState{
			cityName: "acme-city",
			cityPath: city,
			cfg: &config.City{Rigs: []config.Rig{
				{Name: "fe", Path: rigA, Prefix: "fe"},
				{Name: "be", Path: rigB, Prefix: "be"},
			}},
		}
		got := cs.DedupeWorkStoreKeys([]string{"acme-city", "be", "fe"})
		if len(got) != 1 || got[0] != "acme-city" {
			t.Fatalf("aliased rig keys must collapse to the city key, got %v", got)
		}
	})

	t.Run("scoped distinct databases keep every key (DARK)", func(t *testing.T) {
		city := t.TempDir()
		rigA := filepath.Join(city, "fe")
		rigB := filepath.Join(city, "be")
		writeScopeFiles(t, city, managedCityState(), "hq")
		writeScopeFiles(t, rigA, managedCityState(), "fe")
		writeScopeFiles(t, rigB, managedCityState(), "be")

		cs := &controllerState{
			cityName: "acme-city",
			cityPath: city,
			cfg: &config.City{Rigs: []config.Rig{
				{Name: "fe", Path: rigA, Prefix: "fe"},
				{Name: "be", Path: rigB, Prefix: "be"},
			}},
		}
		got := cs.DedupeWorkStoreKeys([]string{"acme-city", "be", "fe"})
		if len(got) != 3 {
			t.Fatalf("distinct databases must keep every key, got %v", got)
		}
	})
}
