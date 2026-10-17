package nudgepoller

import (
	"slices"
	"testing"
)

func TestChildEnvRemovesSessionAuthority(t *testing.T) {
	keys := []string{
		"GC_SESSION_ID", "GC_SESSION_NAME", "GC_ALIAS", "GC_AGENT",
		"GC_TEMPLATE", "GC_SESSION_ORIGIN", "GC_RUNTIME_EPOCH",
		"GC_CONTINUATION_EPOCH", "GC_INSTANCE_TOKEN", "BEADS_HOLDER_TOKEN",
		"BEADS_ACTOR", "CLAUDE_CODE_SESSION_ID",
	}
	for _, key := range keys {
		for _, entry := range []string{key + "=fixture", key + "=", key} {
			t.Run(entry, func(t *testing.T) {
				parent := []string{"GC_CITY_PATH=/fixture", entry, entry, "GC_CITY=fixture"}
				original := slices.Clone(parent)
				want := []string{"GC_CITY_PATH=/fixture", "GC_CITY=fixture", "GC_DISABLE_USAGE_METRICS=1"}
				if got := ChildEnv(parent); !slices.Equal(got, want) {
					t.Fatalf("ChildEnv retains session authority %q", key)
				}
				if !slices.Equal(parent, original) {
					t.Fatal("ChildEnv changed its input")
				}
			})
		}
	}
}

func TestChildEnvPreservesInfrastructureInputs(t *testing.T) {
	parent := []string{
		"PATH=/bin", "GC_CITY_PATH=/fixture", "GC_CITY=fixture", "GC_BIN=/fixture/gc",
		"GC_SESSION_ID_EXTRA=ordinary", "X_GC_INSTANCE_TOKEN=ordinary",
		"BEADS_HOLDER_TOKEN_EXTRA=ordinary", "GC_TELEMETRY=off",
		"OTHER=one", "OTHER=two", "FOREIGN_API_KEY=fixture-credential",
		"GC_DISABLE_USAGE_METRICS=0", "GC_DISABLE_USAGE_METRICS=",
	}
	want := append(slices.Clone(parent[:len(parent)-2]), "GC_DISABLE_USAGE_METRICS=1")
	if got := ChildEnv(parent); !slices.Equal(got, want) {
		t.Fatal("unrelated routing/configuration/credential entries or their order changed")
	}
	if got := ChildEnv(ChildEnv(parent)); !slices.Equal(got, want) {
		t.Fatal("ChildEnv is not idempotent")
	}
	if got := ChildEnv(nil); !slices.Equal(got, []string{"GC_DISABLE_USAGE_METRICS=1"}) {
		t.Fatal("empty infrastructure environment lacks existing metrics opt-out")
	}
}
