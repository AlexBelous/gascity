package config

import (
	"slices"
	"testing"
)

func TestCityWorkPrefixes(t *testing.T) {
	cfg := &City{}
	cfg.Workspace.Prefix = "hq"
	cfg.Rigs = []Rig{
		{Name: "frontend", Prefix: "fe"},
		{Name: "backend", Prefix: "be"},
		{Name: "dup", Prefix: "HQ"}, // case-insensitive dedup against the HQ prefix
	}
	got := CityWorkPrefixes(cfg)
	want := []string{"hq", "fe", "be"}
	if !slices.Equal(got, want) {
		t.Fatalf("CityWorkPrefixes = %v, want %v (HQ first, rigs in order, case-insensitive dedup)", got, want)
	}

	if CityWorkPrefixes(nil) != nil {
		t.Fatal("nil config must yield nil prefixes")
	}
}

func TestCityWorkPrefixesDerivesMissing(t *testing.T) {
	cfg := &City{}
	cfg.Workspace.Name = "acme"
	cfg.Rigs = []Rig{{Name: "web"}} // no explicit prefix → derived
	got := CityWorkPrefixes(cfg)
	if len(got) != 2 {
		t.Fatalf("expected HQ + one rig derived prefix, got %v", got)
	}
	if got[0] != EffectiveHQPrefix(cfg) {
		t.Fatalf("HQ prefix must lead, got %v", got)
	}
	if got[1] != (&cfg.Rigs[0]).EffectivePrefix() {
		t.Fatalf("rig derived prefix mismatch, got %v", got)
	}
}
