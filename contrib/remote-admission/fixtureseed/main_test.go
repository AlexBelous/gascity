package main

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/coordclass"
	"github.com/gastownhall/gascity/internal/session"
	"github.com/gastownhall/gascity/internal/storebinding"
	sqliteprovider "github.com/gastownhall/gascity/internal/storebinding/sqlite"
)

func TestSeedFreshSQLiteLeavesClaimForRealHook(t *testing.T) {
	root := filepath.Join(t.TempDir(), "city")
	public, err := seed(root, func(_ string) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	spec := storebinding.BindingSpec{Name: "infra", Provider: sqliteprovider.BeadsProviderID, Path: filepath.Join(root, ".gc/store")}
	provider, err := (sqliteprovider.BeadsProviderFactory{}).New(spec)
	if err != nil {
		t.Fatal(err)
	}
	classes := []coordclass.Class{}
	for _, c := range coordclass.Classes() {
		if c.IsInfrastructure() {
			classes = append(classes, c)
		}
	}
	served, err := storebinding.NewClassSet(classes...)
	if err != nil {
		t.Fatal(err)
	}
	st, closer, err := provider.(storebinding.EngineOpener).OpenEngine(spec, served)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = closer.Close() }()
	s, err := st.Get(public.SID)
	if err != nil {
		t.Fatal(err)
	}
	b, err := st.Get(public.BeadID)
	if err != nil {
		t.Fatal(err)
	}
	if b.Status != "open" || b.Assignee != "" || b.Metadata["gc.session_id"] != "" {
		t.Fatal("fixture preclaimed work")
	}
	claim, err := session.NewStore(beads.SessionStore{Store: st}).CurrentClaimBeadID(public.SID)
	if err != nil || claim != "" {
		t.Fatal("fixture session preclaimed work")
	}
	if b.Metadata["gc.routed_to"] != template || b.Metadata["gc.kind"] != "" || b.Metadata["gc.root_bead_id"] == "" {
		t.Fatal("fixture is not routed graph work")
	}
	if s.Metadata["instance_token"] == "" || s.Metadata["runtime_binding_v1"] == "" {
		t.Fatal("missing canonical identity")
	}
	info, err := os.Stat(filepath.Join(root, "fixture-private.json"))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatal("private seed is not 0600")
	}
}

func TestSeedNeverOverwritesExistingCity(t *testing.T) {
	root := filepath.Join(t.TempDir(), "city")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := seed(root, func(_ string) error { return nil }); err == nil {
		t.Fatal("existing city accepted")
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 0 {
		t.Fatal("refused city was mutated")
	}
}

func TestSeedConvergesEmptyCityBeforeAnyCreate(t *testing.T) {
	root := filepath.Join(t.TempDir(), "city")
	sentinel := errors.New("migration failed")
	called := false
	_, err := seed(root, func(city string) error {
		called = true
		if city != root {
			t.Fatal("wrong city")
		}
		if _, err := os.Stat(filepath.Join(city, "city.toml")); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(filepath.Join(city, ".gc")); !os.IsNotExist(err) {
			t.Fatal("storage populated before convergence")
		}
		return sentinel
	})
	if !called || !errors.Is(err, sentinel) {
		t.Fatal("migration failure did not stop seed")
	}
	if _, err := os.Stat(filepath.Join(root, "fixture-private.json")); !os.IsNotExist(err) {
		t.Fatal("identity published after migration failure")
	}
}
