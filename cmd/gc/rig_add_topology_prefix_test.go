package main

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/config"
	"github.com/gastownhall/gascity/internal/fsys"
)

func TestRigAddSharesWorkDBScopedIsDark(t *testing.T) {
	city := t.TempDir()
	cfg := &config.City{}
	shared, err := rigAddSharesWorkDB(city, cfg)
	if err != nil {
		t.Fatalf("rigAddSharesWorkDB: %v", err)
	}
	if shared {
		t.Fatal("a scoped, marker-less city must be DARK (not shared)")
	}
}

func TestRigAddSharesWorkDBUnifiedConfig(t *testing.T) {
	city := t.TempDir()
	cfg := &config.City{}
	cfg.Beads.Work.Scope = config.BeadsWorkScopeUnified
	shared, err := rigAddSharesWorkDB(city, cfg)
	if err != nil || !shared {
		t.Fatalf("unified config must share the work DB (shared=%v err=%v)", shared, err)
	}
}

func TestRigAddSharesWorkDBUnifiedMarker(t *testing.T) {
	city := t.TempDir()
	writeUnifiedMarker(t, city)
	// Even with a scoped/empty config, a completed unify marker means shared.
	shared, err := rigAddSharesWorkDB(city, &config.City{})
	if err != nil || !shared {
		t.Fatalf("unified marker must share the work DB (shared=%v err=%v)", shared, err)
	}
}

func TestValidateNewRigPrefixForSharedWorkDB(t *testing.T) {
	cfg := &config.City{}
	cfg.Workspace.Prefix = "ga"
	cfg.Rigs = []config.Rig{{Name: "fe", Prefix: "fe"}}

	// Distinct, non-reserved prefix passes.
	if err := validateNewRigPrefixForSharedWorkDB(cfg, "be", ""); err != nil {
		t.Fatalf("distinct prefix must pass: %v", err)
	}
	// Collision with an existing rig prefix (case-insensitive) is rejected.
	if err := validateNewRigPrefixForSharedWorkDB(cfg, "frontend", "FE"); err == nil ||
		!strings.Contains(err.Error(), "collides") {
		t.Fatalf("colliding rig prefix must be rejected, got %v", err)
	}
	// Collision with the HQ prefix is rejected.
	if err := validateNewRigPrefixForSharedWorkDB(cfg, "another", "ga"); err == nil ||
		!strings.Contains(err.Error(), "HQ") {
		t.Fatalf("HQ prefix collision must be rejected, got %v", err)
	}
	// Reserved coordination-class prefix is rejected.
	if err := validateNewRigPrefixForSharedWorkDB(cfg, "graphy", "gcg"); err == nil ||
		!strings.Contains(err.Error(), "reserved") {
		t.Fatalf("reserved prefix must be rejected, got %v", err)
	}
}

// TestValidateNewRigPrefixExcludesRigBeingReAdded pins the blocker fix: an
// idempotent re-add / adopt / retry of an EXISTING rig must not falsely
// self-collide, or a stranded unregistered prefix could never be retried.
func TestValidateNewRigPrefixExcludesRigBeingReAdded(t *testing.T) {
	cfg := &config.City{}
	cfg.Workspace.Prefix = "ga"
	cfg.Rigs = []config.Rig{{Name: "fe", Prefix: "fe"}}

	if err := validateNewRigPrefixForSharedWorkDB(cfg, "fe", "fe"); err != nil {
		t.Fatalf("re-add of existing rig must pass the pre-check, got %v", err)
	}
	// Case-insensitive name match also excludes the rig being re-added.
	if err := validateNewRigPrefixForSharedWorkDB(cfg, "FE", "fe"); err != nil {
		t.Fatalf("case-insensitive re-add must pass, got %v", err)
	}
	// A genuinely different new rig reusing fe's prefix is STILL rejected.
	if err := validateNewRigPrefixForSharedWorkDB(cfg, "be", "fe"); err == nil ||
		!strings.Contains(err.Error(), "collides") {
		t.Fatalf("a new rig reusing an existing prefix must be rejected, got %v", err)
	}
}

func TestRegisterNewRigPrefixInSharedWorkDB(t *testing.T) {
	city := t.TempDir()

	added := map[string]bool{}
	origOpen := openWorkUnifyScopeStore
	origAdd := workRemoteAddPrefixToSet
	origRead := workRemoteReadAllowedPrefixes
	t.Cleanup(func() {
		openWorkUnifyScopeStore = origOpen
		workRemoteAddPrefixToSet = origAdd
		workRemoteReadAllowedPrefixes = origRead
	})
	openWorkUnifyScopeStore = func(string, string) (beads.Store, func(), error) {
		return newFakeWorkStore(), func() {}, nil
	}
	workRemoteAddPrefixToSet = func(_ beads.Store, prefix string) error {
		added[prefix] = true
		return nil
	}
	workRemoteReadAllowedPrefixes = func(beads.Store) (map[string]bool, error) {
		out := map[string]bool{"ga": true}
		for p := range added {
			out[p] = true
		}
		return out, nil
	}

	var stderr bytes.Buffer
	if err := registerNewRigPrefixInSharedWorkDB(city, "be", &stderr); err != nil {
		t.Fatalf("register: %v", err)
	}
	if !added["be"] {
		t.Fatalf("add-to-set was not called for the new prefix; added=%v", added)
	}
}

// TestRigAddRejectsCollisionWithFragmentRigOnUnifiedCity pins the MAJOR fix:
// the collision guard consults the EXPANDED (include-composed) rig set, so a new
// rig colliding with a fragment-declared rig's prefix is rejected pre-create.
// (A raw config.Load would omit the fragment rig and let the city brick at boot.)
func TestRigAddRejectsCollisionWithFragmentRigOnUnifiedCity(t *testing.T) {
	city := t.TempDir()
	write := func(rel, data string) {
		p := filepath.Join(city, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(data), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("city.toml", "include = [\"fragments/rig.toml\"]\n\n[workspace]\nname = \"topo\"\nprefix = \"ga\"\n\n[beads.work]\nscope = \"unified\"\n")
	// The colliding rig "fe" (prefix fe) exists ONLY in the include fragment.
	write("fragments/rig.toml", "[[rigs]]\nname = \"fe\"\nprefix = \"fe\"\npath = \"fe\"\n")
	// The new rig dir must exist for the StatRigPath preflight (the collision
	// fires before Provision, so no real store init is needed).
	if err := os.MkdirAll(filepath.Join(city, "be"), 0o755); err != nil {
		t.Fatal(err)
	}

	var stderr bytes.Buffer
	_, code := doRigAddWithResult(fsys.OSFS{}, city, filepath.Join(city, "be"),
		nil, "", "fe", "", false, false, io.Discard, &stderr)
	if code == 0 {
		t.Fatalf("collision with a fragment rig prefix must be rejected pre-create; stderr=%s", stderr.String())
	}
	if !strings.Contains(stderr.String(), "collides") {
		t.Fatalf("rejection must name the collision, got %q", stderr.String())
	}
}

// TestRegisterNewRigPrefixReadOnlyCredential pins the S8-style actionable error
// when the prefix is still missing after add-to-set (read-only credential).
func TestRegisterNewRigPrefixReadOnlyCredential(t *testing.T) {
	city := t.TempDir()
	origOpen := openWorkUnifyScopeStore
	origAdd := workRemoteAddPrefixToSet
	origRead := workRemoteReadAllowedPrefixes
	t.Cleanup(func() {
		openWorkUnifyScopeStore = origOpen
		workRemoteAddPrefixToSet = origAdd
		workRemoteReadAllowedPrefixes = origRead
	})
	openWorkUnifyScopeStore = func(string, string) (beads.Store, func(), error) {
		return newFakeWorkStore(), func() {}, nil
	}
	workRemoteAddPrefixToSet = func(beads.Store, string) error { return nil }
	workRemoteReadAllowedPrefixes = func(beads.Store) (map[string]bool, error) {
		return map[string]bool{"ga": true}, nil // "be" never lands
	}
	var stderr bytes.Buffer
	err := registerNewRigPrefixInSharedWorkDB(city, "be", &stderr)
	if err == nil || !strings.Contains(err.Error(), "allowed_prefixes") {
		t.Fatalf("read-only credential must surface an actionable error, got %v", err)
	}
}
