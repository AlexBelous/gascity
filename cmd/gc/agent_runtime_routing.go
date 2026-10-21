package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/config"
	"github.com/gastownhall/gascity/internal/runtime"
	"github.com/gastownhall/gascity/internal/runtime/routing"
	"github.com/gastownhall/gascity/internal/session"
)

// agentRuntimeProfileResolver is the explicit integration point for resource
// policy. A selector/SSH endpoint alone is never a sufficient resource policy.
// The remote admission slice must supply an owned resource domain and a digest
// of its complete launch policy. Binding remains separate from admission.
type agentRuntimeProfileResolver func(*config.City, config.Agent) (runtime.RuntimeBinding, error)

var resolveAgentRuntimeProfile agentRuntimeProfileResolver = func(_ *config.City, a config.Agent) (runtime.RuntimeBinding, error) {
	return runtime.RuntimeBinding{}, fmt.Errorf("agent %q: explicit runtime %q has no resource policy", a.QualifiedName(), a.Runtime)
}

func withAgentRuntimeRouting(ctx sessionProviderContext, local runtime.Provider) (runtime.Provider, error) {
	if ctx.cfg == nil || ctx.cityPath == "" {
		return local, nil
	}
	raw, err := openSessionProviderStore(ctx.cityPath)
	if err != nil {
		return nil, fmt.Errorf("loading pinned runtime routes: %w", err)
	}
	store := session.NewStore(beads.SessionStore{Store: cliSessionStore(raw, ctx.cfg, ctx.cityPath)})
	return composeOperatorRemoteRuntimeRouting(ctx, local, store)
}

// composeAgentRuntimeRouting owns only transport selection. Store owns all SID
// metadata writes; the caller still owns resource authorization before launch.
func composeAgentRuntimeRouting(ctx sessionProviderContext, local runtime.Provider, store *session.Store, profile agentRuntimeProfileResolver) (runtime.Provider, error) {
	return composeAgentRuntimeRoutingUsing(ctx, local, store, profile, nil)
}

func composeAgentRuntimeRoutingUsing(ctx sessionProviderContext, local runtime.Provider, store *session.Store, profile agentRuntimeProfileResolver, trustedFactory func(runtime.RuntimeBinding) (runtime.Provider, error)) (runtime.Provider, error) {
	pins, err := store.RuntimeBindings()
	if err != nil {
		return nil, err
	}
	explicit := false
	for _, a := range ctx.agents {
		if a.Runtime != "" {
			explicit = true
			break
		}
	}
	if !explicit && len(pins) == 0 {
		return local, nil
	}
	var mu sync.Mutex
	providers := map[runtime.RuntimeBinding]runtime.Provider{}
	factory := func(b runtime.RuntimeBinding) (runtime.Provider, error) {
		mu.Lock()
		defer mu.Unlock()
		if sp := providers[b]; sp != nil {
			return sp, nil
		}
		if !strings.HasPrefix(b.Selector, "ssh:") || b.Endpoint != strings.TrimPrefix(b.Selector, "ssh:") {
			return nil, fmt.Errorf("pinned runtime requires an immutable SSH endpoint/reference")
		}
		if trustedFactory != nil {
			sp, err := trustedFactory(b)
			if err != nil {
				return nil, err
			}
			providers[b] = sp
			return sp, nil
		}
		registry, err := runtimeRegistryForCity(ctx.cfg)
		if err != nil {
			return nil, err
		}
		// City-default compatibility includes a tmux fallback. Explicit per-agent
		// selectors must instead reject unknown names, including old pinned names.
		registry = registry.Clone()
		registry.SetFallback(nil)
		sp, err := registry.New(b.Selector, ctx.sc, ctx.cityName, ctx.cityPath)
		if err != nil {
			return nil, err
		}
		providers[b] = sp
		return sp, nil
	}
	desired := func(template string) (runtime.RuntimeBinding, error) {
		var found *config.Agent
		for i := range ctx.agents {
			a := &ctx.agents[i]
			if a.QualifiedName() == template {
				if found != nil {
					return runtime.RuntimeBinding{}, fmt.Errorf("ambiguous runtime template %q", template)
				}
				found = a
			}
		}
		if found == nil {
			return runtime.RuntimeBinding{}, fmt.Errorf("unknown runtime template %q", template)
		}
		if found.Runtime == "" {
			return runtime.RuntimeBinding{}, nil
		}
		if profile == nil {
			return runtime.RuntimeBinding{}, fmt.Errorf("runtime policy unavailable")
		}
		b, err := profile(ctx.cfg, *found)
		if err != nil {
			return b, err
		}
		if b.Selector != found.Runtime || b.ResourceDomain == "" || b.PolicyDigest == "" {
			return b, fmt.Errorf("incomplete or mismatched runtime policy for %q", template)
		}
		if _, err := factory(b); err != nil {
			return b, err
		}
		return b, nil
	}
	bind := func(_ context.Context, id, template string) (runtime.RuntimeBinding, error) {
		info, err := store.Get(id)
		if err != nil {
			return runtime.RuntimeBinding{}, err
		}
		if info.Template != template {
			return runtime.RuntimeBinding{}, fmt.Errorf("runtime binding template mismatch for session %q", id)
		}
		old, pinned, err := store.RuntimeBinding(id)
		if err != nil {
			return old, err
		}
		b, err := desired(template)
		if err != nil {
			return old, err
		}
		if pinned {
			if old != b {
				return old, fmt.Errorf("session %q: runtime launch policy changed", id)
			}
			return old, nil
		}
		if b.Selector == "" {
			return b, nil
		}
		if err = store.PinRuntimeBinding(id, b); err != nil {
			return b, err
		}
		return b, nil
	}
	lookup := func(name string) (runtime.RuntimeBinding, error) {
		_, template, b, pinned, err := store.RuntimeRoute(name)
		if err != nil {
			return b, err
		}
		if pinned {
			return b, nil
		}
		// An explicit remote template without a persisted binding cannot silently
		// target the local backend, even for observation or cleanup.
		if template != "" {
			for _, a := range ctx.agents {
				if a.QualifiedName() == template && a.Runtime != "" {
					return b, fmt.Errorf("session %q has no pinned runtime", name)
				}
			}
		}
		return runtime.RuntimeBinding{}, nil
	}
	return routing.New(local, routing.Callbacks{Bind: bind, Lookup: lookup, Factory: factory, Bindings: store.RuntimeBindings, LookupID: func(id string) (runtime.RuntimeBinding, bool, error) {
		b, ok, err := store.RuntimeBinding(id)
		if errors.Is(err, beads.ErrNotFound) {
			return runtime.RuntimeBinding{}, false, nil
		}
		return b, ok, err
	}}), nil
}

// Every accepted config generation refreshes an active overlay: profile policy
// may depend on model/provider/workspace inputs beyond the Runtime selector.
func agentRuntimeRoutingActive(sp runtime.Provider, configs ...*config.City) bool {
	if _, ok := sp.(*operatorRemoteProvider); ok {
		return true
	}
	if _, ok := sp.(*routing.Provider); ok {
		return true
	}
	for _, cfg := range configs {
		if cfg == nil {
			continue
		}
		for _, a := range cfg.Agents {
			if a.Runtime != "" {
				return true
			}
		}
	}
	return false
}

func refreshAgentRuntimeRouting(ctx sessionProviderContext, sp runtime.Provider) (runtime.Provider, error) {
	return refreshAgentRuntimeRoutingUsing(ctx, sp, withAgentRuntimeRouting)
}

func refreshAgentRuntimeRoutingUsing(ctx sessionProviderContext, sp runtime.Provider, compose func(sessionProviderContext, runtime.Provider) (runtime.Provider, error)) (runtime.Provider, error) {
	if router, ok := sp.(interface{ DefaultProvider() runtime.Provider }); ok {
		sp = router.DefaultProvider()
	}
	return compose(ctx, sp)
}
