package main

// Reproducer for the symlinked-city CreateRig rejection (adjacent defect found
// while investigating ga-xbilek). Drop this into cmd/gc/ and run:
//
//	go test ./cmd/gc/ -run TestAssertRigPathWithinCity -v
//
// It needs no macOS and no GOTMPDIR emulation — it creates its own symlink.

import (
	"os"
	"path/filepath"
	"testing"
)

// TestAssertRigPathWithinCityRejectsResolvedTargetUnderRawCity shows that
// controllerState.CreateRig rejects a rig living INSIDE the city with
// "rig path %s escapes the city root" whenever the city is reached through a
// symlinked ancestor (e.g. ~/gc -> /data/gc, the exact case
// resolveStoreScopeRoot's own comment says it supports).
//
// CreateRig (api_state.go:1788) sets r.Path = resolveStoreScopeRoot(...), which
// normalizes through pathutil and therefore RESOLVES the symlink. It then calls
// assertRigPathWithinCity(cs.cityPath, r.Path) (api_state.go:1796) where
// cs.cityPath is still the UNRESOLVED form. The lexical first pass
// (relWithinCity, api_state.go:1740) compares those two mismatched forms and
// reports an escape.
func TestAssertRigPathWithinCityRejectsResolvedTargetUnderRawCity(t *testing.T) {
	root := t.TempDir()
	realDir := filepath.Join(root, "real")
	if err := os.MkdirAll(realDir, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(realDir, link); err != nil {
		t.Skipf("symlinks unsupported on this platform: %v", err)
	}

	cityPath := filepath.Join(link, "city")
	rigPath := filepath.Join(cityPath, "repo")
	if err := os.MkdirAll(rigPath, 0o755); err != nil {
		t.Fatal(err)
	}

	resolved := resolveStoreScopeRoot(cityPath, rigPath)
	if resolved == rigPath {
		t.Fatalf("precondition: resolveStoreScopeRoot did not resolve the symlink (got %q)", resolved)
	}

	if err := assertRigPathWithinCity(cityPath, resolved); err != nil {
		t.Fatalf("assertRigPathWithinCity(%q, %q) = %v, want nil: the rig is inside the city, "+
			"only the two arguments disagree about symlink resolution", cityPath, resolved, err)
	}
}

// TestAssertRigPathWithinCityAcceptsWhenBothSidesResolved disproves the
// alternative hypothesis that the symlink-AWARE second pass is at fault: with
// both arguments in the same form the identical layout validates cleanly.
func TestAssertRigPathWithinCityAcceptsWhenBothSidesResolved(t *testing.T) {
	root := t.TempDir()
	realDir := filepath.Join(root, "real")
	if err := os.MkdirAll(realDir, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(realDir, link); err != nil {
		t.Skipf("symlinks unsupported on this platform: %v", err)
	}

	cityPath := filepath.Join(link, "city")
	rigPath := filepath.Join(cityPath, "repo")
	if err := os.MkdirAll(rigPath, 0o755); err != nil {
		t.Fatal(err)
	}

	resolvedCity, err := filepath.EvalSymlinks(cityPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := assertRigPathWithinCity(resolvedCity, resolveStoreScopeRoot(cityPath, rigPath)); err != nil {
		t.Fatalf("assertRigPathWithinCity(%q, ...) = %v, want nil", resolvedCity, err)
	}
}
