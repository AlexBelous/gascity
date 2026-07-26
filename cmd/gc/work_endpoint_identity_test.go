package main

import (
	"path/filepath"
	"testing"
)

// TestSameResolvedWorkEndpoint pins deliverable A: two scope roots that resolve
// to one physical work database report as aliased, while distinct-database
// scopes do not. The managed arm exercises the port-free shared-server case; the
// remote arm exercises canonical host/port comparison.
func TestSameResolvedWorkEndpoint(t *testing.T) {
	t.Run("managed rigs re-pointed at the city database alias", func(t *testing.T) {
		city := t.TempDir()
		rigA := filepath.Join(city, "fe")
		rigB := filepath.Join(city, "be")
		// City + both rigs all name the same managed database "acme" (post-unify
		// canonicalization: rigs inherit the city's managed server).
		writeScopeFiles(t, city, managedCityState(), "acme")
		writeScopeFiles(t, rigA, inheritedRigState(), "acme")
		writeScopeFiles(t, rigB, inheritedRigState(), "acme")

		same, err := sameResolvedWorkEndpoint(city, rigA, rigB)
		if err != nil {
			t.Fatalf("sameResolvedWorkEndpoint: %v", err)
		}
		if !same {
			t.Fatal("rigs sharing the city managed database must alias")
		}
		same, err = sameResolvedWorkEndpoint(city, city, rigA)
		if err != nil {
			t.Fatalf("sameResolvedWorkEndpoint(city,rig): %v", err)
		}
		if !same {
			t.Fatal("a rig inheriting the managed city database must alias the city")
		}
	})

	t.Run("scoped rigs on distinct databases do not alias (DARK)", func(t *testing.T) {
		city := t.TempDir()
		rigA := filepath.Join(city, "fe")
		rigB := filepath.Join(city, "be")
		writeScopeFiles(t, city, managedCityState(), "hq")
		writeScopeFiles(t, rigA, managedCityState(), "fe")
		writeScopeFiles(t, rigB, managedCityState(), "be")

		same, err := sameResolvedWorkEndpoint(city, rigA, rigB)
		if err != nil {
			t.Fatalf("sameResolvedWorkEndpoint: %v", err)
		}
		if same {
			t.Fatal("distinct-database scoped rigs must not alias")
		}
	})

	t.Run("remote scopes compare canonical host/port/db", func(t *testing.T) {
		city := t.TempDir()
		rigA := filepath.Join(city, "fe")
		rigB := filepath.Join(city, "be")
		// localhost and 127.0.0.1 fold to one host; same port + database → alias.
		writeScopeFiles(t, city, cityCanonicalState("localhost"), "org")
		writeScopeFiles(t, rigA, inheritedCanonicalRigState("127.0.0.1", "3306"), "org")
		writeScopeFiles(t, rigB, inheritedCanonicalRigState("localhost", "3306"), "org")

		same, err := sameResolvedWorkEndpoint(city, rigA, rigB)
		if err != nil {
			t.Fatalf("sameResolvedWorkEndpoint: %v", err)
		}
		if !same {
			t.Fatal("remote scopes with folded loopback host + same port/db must alias")
		}

		// A different port is a different endpoint.
		rigC := filepath.Join(city, "svc")
		writeScopeFiles(t, rigC, inheritedCanonicalRigState("127.0.0.1", "3307"), "org")
		same, err = sameResolvedWorkEndpoint(city, rigA, rigC)
		if err != nil {
			t.Fatalf("sameResolvedWorkEndpoint(port): %v", err)
		}
		if same {
			t.Fatal("different ports must not alias")
		}
	})

	t.Run("unresolvable scope never aliases", func(t *testing.T) {
		city := t.TempDir()
		writeScopeFiles(t, city, managedCityState(), "hq")
		bare := filepath.Join(city, "bare") // no .beads at all
		same, err := sameResolvedWorkEndpoint(city, city, bare)
		if err != nil {
			t.Fatalf("sameResolvedWorkEndpoint(bare): %v", err)
		}
		if same {
			t.Fatal("an unresolvable scope must never be reported as aliasing")
		}
	})

	t.Run("identical roots always alias", func(t *testing.T) {
		city := t.TempDir()
		same, err := sameResolvedWorkEndpoint(city, city, city)
		if err != nil || !same {
			t.Fatalf("same root must alias: same=%v err=%v", same, err)
		}
	})
}
