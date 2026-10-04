package worker

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/gastownhall/gascity/internal/runtime"
)

func TestRuntimeOnlyManagedStartCannotBypassSIDClaim(t *testing.T) {
	city := t.TempDir()
	if err := os.MkdirAll(filepath.Join(city, "config"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(city, "config/capacity-scheduler-v2.toml"), []byte("[queue]\nmanaged_exact_routes=[\"worker\"]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, route := range []string{"worker", "protected", "unknown-alias"} {
		t.Run(route, func(t *testing.T) {
			sp := runtime.NewFake()
			factory, err := NewFactory(FactoryConfig{Provider: sp, CityPath: city})
			if err != nil {
				t.Fatal(err)
			}
			h, err := factory.RuntimeHandle(route, "fake", "", nil)
			if err != nil {
				t.Fatal(err)
			}
			env := map[string]string{"GC_TEMPLATE": route}
			if route == "unknown-alias" {
				env = nil
			}
			err = h.StartResolved(context.Background(), "fake-command", runtime.Config{Env: env})
			if route != "protected" {
				if err == nil || !runtime.IsProviderCapacity(err) {
					t.Fatalf("managed noSID start allowed: %v", err)
				}
				for _, c := range sp.Calls {
					if c.Method == "Start" {
						t.Fatal("legacy start bypassed grant")
					}
				}
			} else if err != nil {
				t.Fatalf("protected runtime blocked: %v", err)
			}
		})
	}
}
