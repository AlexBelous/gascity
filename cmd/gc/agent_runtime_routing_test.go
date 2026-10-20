package main

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gastownhall/gascity/internal/events"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/config"
	"github.com/gastownhall/gascity/internal/rollout/gate"
	"github.com/gastownhall/gascity/internal/runtime"
	"github.com/gastownhall/gascity/internal/session"
)

func TestAgentRuntimeRoutingDefaultIdentityAndRemotePolicy(t *testing.T) {
	local := runtime.NewFake()
	mem := beads.NewMemStore()
	_, err := beads.OpenStoreAtForCity(context.Background(), beads.StoreOpenOptions{Provider: "file", ConditionalWrites: gate.Require, OpenFileStore: func() (beads.Store, error) { return mem, nil }})
	if err != nil {
		t.Fatal(err)
	}
	store := session.NewStore(beads.SessionStore{Store: mem})
	ctx := sessionProviderContext{cfg: &config.City{}, agents: []config.Agent{{Name: "worker"}}}
	got, err := composeAgentRuntimeRouting(ctx, local, store, nil)
	if err != nil || got != local {
		t.Fatalf("local provider shape changed: %T %v", got, err)
	}
	ctx.agents[0].Runtime = "ssh:worker@vm"
	ctx.cfg.Agents = ctx.agents
	created, err := store.CreateSessionInfo(session.CreateSpec{ID: "sid-a", AgentName: "worker", Metadata: map[string]string{"template": "worker", "session_name": "runtime-a"}})
	if err != nil {
		t.Fatal(err)
	}
	routed, err := composeAgentRuntimeRouting(ctx, local, store, resolveAgentRuntimeProfile)
	if err != nil {
		t.Fatal(err)
	}
	binder := routed.(runtime.SessionRuntimeProvider)
	if _, err = binder.BindRuntime(context.Background(), created.ID, "worker"); err == nil || !strings.Contains(err.Error(), "no resource policy") {
		t.Fatalf("remote without policy: %v", err)
	}
	policy := func(_ *config.City, a config.Agent) (runtime.RuntimeBinding, error) {
		return runtime.RuntimeBinding{Selector: a.Runtime, Endpoint: "worker@vm", ResourceDomain: "owned-vm", PolicyDigest: "reviewed-policy-v1"}, nil
	}
	routed, err = composeAgentRuntimeRouting(ctx, local, store, policy)
	if err != nil {
		t.Fatal(err)
	}
	binder = routed.(runtime.SessionRuntimeProvider)
	pin, err := binder.BindRuntime(context.Background(), created.ID, "worker")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = binder.BindRuntime(context.Background(), created.ID, "worker-alias"); err == nil {
		t.Fatal("accepted nonexact template")
	}
	// Reconstruct after controller restart with the template removed: reads still
	// resolve the old endpoint without invoking the current profile resolver.
	ctx.agents = nil
	restarted, err := composeAgentRuntimeRouting(ctx, local, store, nil)
	if err != nil {
		t.Fatal(err)
	}
	gotPin, err := restarted.(runtime.SessionRuntimeProvider).ResolveRuntime("runtime-a")
	if err != nil || gotPin != pin {
		t.Fatalf("lost pin: %+v %v", gotPin, err)
	}
	if _, err = restarted.(runtime.SessionRuntimeProvider).BindRuntime(context.Background(), created.ID, "worker"); err == nil {
		t.Fatal("removed policy allowed restart")
	}
}

func TestAgentRuntimeRoutingReloadAddsRouterAndRefusesUnavailableRoutes(t *testing.T) {
	for _, unavailable := range []bool{false, true} {
		name := "new-route"
		if unavailable {
			name = "unavailable-route"
		}
		t.Run(name, func(t *testing.T) {
			cityPath := t.TempDir()
			tomlPath := filepath.Join(cityPath, "city.toml")
			writeCityRuntimeConfig(t, tomlPath, "fake")
			cfg, err := config.Load(osFS{}, tomlPath)
			if err != nil {
				t.Fatal(err)
			}
			local := runtime.NewFake()
			if err = local.Start(context.Background(), "existing-local", runtime.Config{Command: "fixture"}); err != nil {
				t.Fatal(err)
			}
			cr := newTestCityRuntime(t, CityRuntimeParams{CityPath: cityPath, CityName: "test-city", TomlPath: tomlPath, Cfg: cfg, SP: local, Dops: newDrainOps(local), Rec: events.Discard, Stdout: io.Discard, Stderr: io.Discard, BuildFn: func(*config.City, runtime.Provider, beads.Store) DesiredStateResult {
				return DesiredStateResult{State: map[string]TemplateParams{}}
			}})
			originalOpen := openSessionProviderStore
			t.Cleanup(func() { openSessionProviderStore = originalOpen })
			mem := beads.NewMemStore()
			openSessionProviderStore = func(string) (beads.Store, error) {
				if unavailable {
					return nil, errors.New("fixture session store unavailable")
				}
				return mem, nil
			}
			data, err := os.ReadFile(tomlPath)
			if err != nil {
				t.Fatal(err)
			}
			data = append(data, []byte("\n[[agent]]\nname = \"remote-worker\"\nruntime = \"ssh:worker@vm\"\nstart_command = \"echo fixture\"\n")...)
			if err = os.WriteFile(tomlPath, data, 0o644); err != nil {
				t.Fatal(err)
			}
			last := "fake"
			reply := cr.reloadConfigTraced(context.Background(), &last, cityPath, nil, reloadSourceManual)
			if local.CountCalls("Stop", "existing-local") != 0 {
				t.Fatal("routing overlay stopped a shared local session")
			}
			if unavailable {
				if reply.Outcome != reloadOutcomeFailed || cr.sp != local || cr.cfg != cfg {
					t.Fatalf("unsafe route reload accepted: %+v", reply)
				}
			} else {
				if reply.Outcome != reloadOutcomeApplied {
					t.Fatalf("route reload failed: %+v", reply)
				}
				if _, ok := cr.sp.(runtime.SessionRuntimeProvider); !ok {
					t.Fatalf("agent Runtime reload kept local provider %T", cr.sp)
				}
			}
		})
	}
}
