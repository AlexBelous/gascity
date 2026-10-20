// Package routing routes exact sessions through immutable durable bindings.
// It does not authorize launches or advertise remote command containment.
package routing

import (
	"context"
	"fmt"
	"time"

	"github.com/gastownhall/gascity/internal/runtime"
)

// Callbacks keep persistence and configuration ownership outside this adapter.
// Lookup returns a zero binding for the legacy city-default route.
// It does not by itself prove local execution.
// Bind must pin an exact SID and reject any changed launch policy.
type Callbacks struct {
	// LookupID fences controller process scans from pinned remote SIDs.
	LookupID func(string) (runtime.RuntimeBinding, bool, error)
	Bind     func(context.Context, string, string) (runtime.RuntimeBinding, error)
	Lookup   func(string) (runtime.RuntimeBinding, error)
	Factory  func(runtime.RuntimeBinding) (runtime.Provider, error)
	Bindings func() ([]runtime.RuntimeBinding, error)
}

// Provider delegates exact-session operations through durable routing callbacks.
type Provider struct {
	local     runtime.Provider
	callbacks Callbacks
}

// New constructs a routing adapter; callers keep the original provider when no routes exist.
func New(local runtime.Provider, c Callbacks) *Provider { return &Provider{local: local, callbacks: c} }

// BindRuntime forwards the documented runtime operation to its selected backend.
func (p *Provider) BindRuntime(ctx context.Context, id, template string) (runtime.RuntimeBinding, error) {
	if p.callbacks.Bind == nil {
		return runtime.RuntimeBinding{}, fmt.Errorf("%w: missing runtime binder", runtime.ErrRuntimeUnavailable)
	}
	return p.callbacks.Bind(ctx, id, template)
}

// ResolveRuntime forwards the documented runtime operation to its selected backend.
func (p *Provider) ResolveRuntime(name string) (runtime.RuntimeBinding, error) {
	if p.callbacks.Lookup == nil {
		return runtime.RuntimeBinding{}, fmt.Errorf("%w: missing runtime lookup", runtime.ErrRuntimeUnavailable)
	}
	b, err := p.callbacks.Lookup(name)
	if err != nil {
		return b, fmt.Errorf("%w: %w", runtime.ErrRuntimeUnavailable, err)
	}
	return b, nil
}

func (p *Provider) backend(b runtime.RuntimeBinding) (runtime.Provider, error) {
	if b.Selector == "" {
		return p.local, nil
	}
	if p.callbacks.Factory == nil {
		return nil, fmt.Errorf("%w: missing runtime factory", runtime.ErrRuntimeUnavailable)
	}
	sp, err := p.callbacks.Factory(b)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", runtime.ErrRuntimeUnavailable, err)
	}
	if sp == nil {
		return nil, fmt.Errorf("%w: nil runtime", runtime.ErrRuntimeUnavailable)
	}
	return sp, nil
}

func (p *Provider) route(name string) (runtime.Provider, error) {
	b, err := p.ResolveRuntime(name)
	if err != nil {
		return nil, err
	}
	return p.backend(b)
}

func (p *Provider) launch(ctx context.Context, name string, cfg runtime.Config, relaunch bool) error {
	b, err := p.BindRuntime(ctx, cfg.Env["GC_SESSION_ID"], cfg.Env["GC_TEMPLATE"])
	if err != nil {
		return err
	}
	sp, err := p.backend(b)
	if err != nil {
		return err
	}
	if relaunch {
		if rp, ok := sp.(runtime.RelaunchProvider); ok {
			return rp.Relaunch(ctx, name, cfg)
		}
		return runtime.ErrRelaunchUnsupported
	}
	return sp.Start(ctx, name, cfg)
}

// Start forwards the documented runtime operation to its selected backend.
func (p *Provider) Start(ctx context.Context, name string, cfg runtime.Config) error {
	return p.launch(ctx, name, cfg, false)
}

// Relaunch forwards the documented runtime operation to its selected backend.
func (p *Provider) Relaunch(ctx context.Context, name string, cfg runtime.Config) error {
	return p.launch(ctx, name, cfg, true)
}

// Capabilities forwards the documented runtime operation to its selected backend.
func (p *Provider) Capabilities() runtime.ProviderCapabilities { return p.local.Capabilities() }

// SupportsCommandScope forwards the documented runtime operation to its selected backend.
func (p *Provider) SupportsCommandScope(name string) bool {
	b, err := p.ResolveRuntime(name)
	if err != nil || b.Selector != "" {
		return false
	}
	sp, ok := p.local.(runtime.CommandScopeProvider)
	return ok && sp.SupportsCommandScope(name)
}

// Stop forwards the documented runtime operation to its selected backend.
func (p *Provider) Stop(name string) error {
	sp, err := p.route(name)
	if err != nil {
		return err
	}
	return sp.Stop(name)
}

// Interrupt forwards the documented runtime operation to its selected backend.
func (p *Provider) Interrupt(name string) error {
	sp, err := p.route(name)
	if err != nil {
		return err
	}
	return sp.Interrupt(name)
}

// IsRunning forwards the documented runtime operation to its selected backend.
func (p *Provider) IsRunning(name string) bool {
	sp, err := p.route(name)
	if err != nil {
		return true
	}
	return sp.IsRunning(name)
}

// IsAttached forwards the documented runtime operation to its selected backend.
func (p *Provider) IsAttached(name string) bool {
	sp, err := p.route(name)
	if err != nil {
		return true
	}
	return sp.IsAttached(name)
}

// Attach forwards the documented runtime operation to its selected backend.
func (p *Provider) Attach(name string) error {
	sp, err := p.route(name)
	if err != nil {
		return err
	}
	return sp.Attach(name)
}

// ProcessAlive forwards the documented runtime operation to its selected backend.
func (p *Provider) ProcessAlive(name string, names []string) bool {
	sp, err := p.route(name)
	if err != nil {
		return true
	}
	return sp.ProcessAlive(name, names)
}

// Nudge forwards the documented runtime operation to its selected backend.
func (p *Provider) Nudge(name string, content []runtime.ContentBlock) error {
	sp, err := p.route(name)
	if err != nil {
		return err
	}
	return sp.Nudge(name, content)
}

// SetMeta forwards the documented runtime operation to its selected backend.
func (p *Provider) SetMeta(name, key, value string) error {
	sp, err := p.route(name)
	if err != nil {
		return err
	}
	return sp.SetMeta(name, key, value)
}

// GetMeta forwards the documented runtime operation to its selected backend.
func (p *Provider) GetMeta(name, key string) (string, error) {
	sp, err := p.route(name)
	if err != nil {
		return "", err
	}
	return sp.GetMeta(name, key)
}

// RemoveMeta forwards the documented runtime operation to its selected backend.
func (p *Provider) RemoveMeta(name, key string) error {
	sp, err := p.route(name)
	if err != nil {
		return err
	}
	return sp.RemoveMeta(name, key)
}

// Peek forwards the documented runtime operation to its selected backend.
func (p *Provider) Peek(name string, lines int) (string, error) {
	sp, err := p.route(name)
	if err != nil {
		return "", err
	}
	return sp.Peek(name, lines)
}

// GetLastActivity forwards the documented runtime operation to its selected backend.
func (p *Provider) GetLastActivity(name string) (time.Time, error) {
	sp, err := p.route(name)
	if err != nil {
		return time.Time{}, err
	}
	return sp.GetLastActivity(name)
}

// ClearScrollback forwards the documented runtime operation to its selected backend.
func (p *Provider) ClearScrollback(name string) error {
	sp, err := p.route(name)
	if err != nil {
		return err
	}
	return sp.ClearScrollback(name)
}

// CopyTo forwards the documented runtime operation to its selected backend.
func (p *Provider) CopyTo(name, src, relDst string) error {
	sp, err := p.route(name)
	if err != nil {
		return err
	}
	return sp.CopyTo(name, src, relDst)
}

// SendKeys forwards the documented runtime operation to its selected backend.
func (p *Provider) SendKeys(name string, keys ...string) error {
	sp, err := p.route(name)
	if err != nil {
		return err
	}
	return sp.SendKeys(name, keys...)
}

// RunLive forwards the documented runtime operation to its selected backend.
func (p *Provider) RunLive(name string, cfg runtime.Config) error {
	sp, err := p.route(name)
	if err != nil {
		return err
	}
	return sp.RunLive(name, cfg)
}

// IsDeadRuntimeSession forwards the documented runtime operation to its selected backend.
func (p *Provider) IsDeadRuntimeSession(name string) (bool, error) {
	sp, err := p.route(name)
	if err != nil {
		return false, err
	}
	if v, ok := sp.(runtime.DeadRuntimeSessionChecker); ok {
		return v.IsDeadRuntimeSession(name)
	}
	return false, nil
}

// WaitForIdle forwards the documented runtime operation to its selected backend.
func (p *Provider) WaitForIdle(ctx context.Context, name string, timeout time.Duration) error {
	sp, err := p.route(name)
	if err != nil {
		return err
	}
	if v, ok := sp.(runtime.IdleWaitProvider); ok {
		return v.WaitForIdle(ctx, name, timeout)
	}
	return runtime.ErrInteractionUnsupported
}

// SnapshotIdle forwards the documented runtime operation to its selected backend.
func (p *Provider) SnapshotIdle(name string) (bool, error) {
	sp, err := p.route(name)
	if err != nil {
		return false, err
	}
	if v, ok := sp.(runtime.IdleSnapshotProvider); ok {
		return v.SnapshotIdle(name)
	}
	return false, runtime.ErrInteractionUnsupported
}

// NudgeNow forwards the documented runtime operation to its selected backend.
func (p *Provider) NudgeNow(name string, content []runtime.ContentBlock) error {
	sp, err := p.route(name)
	if err != nil {
		return err
	}
	if v, ok := sp.(runtime.ImmediateNudgeProvider); ok {
		return v.NudgeNow(name, content)
	}
	return sp.Nudge(name, content)
}

// ResetInterruptedTurn forwards the documented runtime operation to its selected backend.
func (p *Provider) ResetInterruptedTurn(ctx context.Context, name string) error {
	sp, err := p.route(name)
	if err != nil {
		return err
	}
	if v, ok := sp.(runtime.InterruptedTurnResetProvider); ok {
		return v.ResetInterruptedTurn(ctx, name)
	}
	return runtime.ErrInteractionUnsupported
}

// WaitForInterruptBoundary forwards the documented runtime operation to its selected backend.
func (p *Provider) WaitForInterruptBoundary(ctx context.Context, name string, since time.Time, timeout time.Duration) error {
	sp, err := p.route(name)
	if err != nil {
		return err
	}
	if v, ok := sp.(runtime.InterruptBoundaryWaitProvider); ok {
		return v.WaitForInterruptBoundary(ctx, name, since, timeout)
	}
	return runtime.ErrInteractionUnsupported
}

// Pending forwards the documented runtime operation to its selected backend.
func (p *Provider) Pending(name string) (*runtime.PendingInteraction, error) {
	sp, err := p.route(name)
	if err != nil {
		return nil, err
	}
	if v, ok := sp.(runtime.InteractionProvider); ok {
		return v.Pending(name)
	}
	return nil, runtime.ErrInteractionUnsupported
}

// Respond forwards the documented runtime operation to its selected backend.
func (p *Provider) Respond(name string, response runtime.InteractionResponse) error {
	sp, err := p.route(name)
	if err != nil {
		return err
	}
	if v, ok := sp.(runtime.InteractionProvider); ok {
		return v.Respond(name, response)
	}
	return runtime.ErrInteractionUnsupported
}

// Exec forwards the documented runtime operation to its selected backend.
func (p *Provider) Exec(ctx context.Context, name string, argv []string) ([]byte, int, error) {
	sp, err := p.route(name)
	if err != nil {
		return nil, 0, err
	}
	if v, ok := sp.(runtime.ExecProvider); ok {
		return v.Exec(ctx, name, argv)
	}
	return nil, 0, runtime.ErrExecUnsupported
}

// DismissKnownDialogs forwards the documented runtime operation to its selected backend.
func (p *Provider) DismissKnownDialogs(ctx context.Context, name string, timeout time.Duration) error {
	sp, err := p.route(name)
	if err != nil {
		return err
	}
	if v, ok := sp.(runtime.DialogProvider); ok {
		return v.DismissKnownDialogs(ctx, name, timeout)
	}
	return runtime.ErrInteractionUnsupported
}

// GetAllEnvironment forwards the documented runtime operation to its selected backend.
func (p *Provider) GetAllEnvironment(name string) (map[string]string, error) {
	sp, err := p.route(name)
	if err != nil {
		return nil, err
	}
	if v, ok := sp.(runtime.EnvironmentBatchProvider); ok {
		return v.GetAllEnvironment(name)
	}
	return nil, runtime.ErrInteractionUnsupported
}

// IsAttachedWithError forwards the documented runtime operation to its selected backend.
func (p *Provider) IsAttachedWithError(name string) (bool, error) {
	sp, err := p.route(name)
	if err != nil {
		return true, err
	}
	return runtime.IsAttachedWithError(sp, name)
}

// ObserveLivenessWithError forwards the documented runtime operation to its selected backend.
func (p *Provider) ObserveLivenessWithError(name string, names []string) (runtime.Liveness, error) {
	sp, err := p.route(name)
	if err != nil {
		return runtime.Liveness{}, err
	}
	return runtime.ObserveLivenessWithError(sp, name, names)
}

// ObserveLiveness forwards the documented runtime operation to its selected backend.
func (p *Provider) ObserveLiveness(name string, names []string) runtime.Liveness {
	l, err := p.ObserveLivenessWithError(name, names)
	if err != nil {
		return runtime.Liveness{Running: true, Alive: true}
	}
	return l
}

// SleepCapability forwards the documented runtime operation to its selected backend.
func (p *Provider) SleepCapability(name string) runtime.SessionSleepCapability {
	sp, err := p.route(name)
	if err != nil {
		return runtime.SessionSleepCapabilityDisabled
	}
	if v, ok := sp.(runtime.SleepCapabilityProvider); ok {
		return v.SleepCapability(name)
	}
	return runtime.SessionSleepCapabilityDisabled
}

// ListRunning consults every pinned backend, including runtimes removed from
// current configuration, so orphan observation never silently becomes local.
func (p *Provider) ListRunning(prefix string) ([]string, error) {
	names, err := p.local.ListRunning(prefix)
	results := []runtime.BackendListResult{{Label: "local", Names: names, Err: err}}
	if p.callbacks.Bindings == nil {
		return runtime.MergeBackendListResults(results...)
	}
	bindings, err := p.callbacks.Bindings()
	if err != nil {
		return names, fmt.Errorf("%w: %w", runtime.ErrRuntimeUnavailable, err)
	}
	seen := map[runtime.RuntimeBinding]bool{}
	for _, b := range bindings {
		if b.Selector == "" || seen[b] {
			continue
		}
		seen[b] = true
		sp, err := p.backend(b)
		var ns []string
		if err == nil {
			ns, err = sp.ListRunning(prefix)
		}
		results = append(results, runtime.BackendListResult{Label: b.Selector, Names: ns, Err: err})
	}
	return runtime.MergeBackendListResults(results...)
}

// Process scans remain controller-local. A pinned remote SID cannot use a
// controller PID as evidence or a termination target.
func (p *Provider) remoteSID(id string) (bool, error) {
	if id == "" || p.callbacks.LookupID == nil {
		return false, nil
	}
	b, ok, err := p.callbacks.LookupID(id)
	return ok && b.Selector != "", err
}

// FindRuntimesBySessionID forwards the documented runtime operation to its selected backend.
func (p *Provider) FindRuntimesBySessionID(id string) ([]runtime.LiveRuntime, error) {
	if remote, err := p.remoteSID(id); err != nil {
		return nil, err
	} else if remote {
		return nil, nil
	}
	s, ok := p.local.(runtime.ProcessTableScanner)
	if !ok {
		return nil, nil
	}
	rows, err := s.FindRuntimesBySessionID(id)
	var out []runtime.LiveRuntime
	for _, r := range rows {
		remote, e := p.remoteSID(r.SessionID)
		if e != nil {
			return nil, e
		}
		if !remote {
			out = append(out, r)
		}
	}
	return out, err
}

// TerminateRuntime forwards the documented runtime operation to its selected backend.
func (p *Provider) TerminateRuntime(r runtime.LiveRuntime) error {
	if remote, err := p.remoteSID(r.SessionID); err != nil {
		return err
	} else if remote {
		return fmt.Errorf("refusing local process termination for remote SID")
	}
	if r.ProviderName != "" {
		b, err := p.ResolveRuntime(r.ProviderName)
		if err != nil {
			return err
		}
		if b.Selector != "" {
			return fmt.Errorf("refusing local process termination for remote runtime")
		}
	}
	if s, ok := p.local.(runtime.ProcessTableScanner); ok {
		return s.TerminateRuntime(r)
	}
	return fmt.Errorf("process scan unsupported")
}

// ConfigureServer forwards the documented runtime operation to its selected backend.
func (p *Provider) ConfigureServer() error {
	if s, ok := p.local.(runtime.ServerLifecycleProvider); ok {
		return s.ConfigureServer()
	}
	return nil
}

// TeardownServer forwards the documented runtime operation to its selected backend.
func (p *Provider) TeardownServer() error {
	if s, ok := p.local.(runtime.ServerLifecycleProvider); ok {
		return s.TeardownServer()
	}
	return nil
}

// ReadSessionAuthoritySnapshot forwards the documented runtime operation to its selected backend.
func (p *Provider) ReadSessionAuthoritySnapshot(ctx context.Context) ([]runtime.SessionAuthorityHandle, error) {
	if s, ok := p.local.(runtime.SessionAuthoritySnapshotProvider); ok {
		return s.ReadSessionAuthoritySnapshot(ctx)
	}
	return nil, fmt.Errorf("local session authority unavailable")
}

// SupportsTransport forwards the documented runtime operation to its selected backend.
func (p *Provider) SupportsTransport(t string) bool {
	if s, ok := p.local.(runtime.TransportCapabilityProvider); ok {
		return s.SupportsTransport(t)
	}
	return t == "" || t == "tmux"
}

// SubscribeSessionEvents forwards the documented runtime operation to its selected backend.
func (p *Provider) SubscribeSessionEvents(ctx context.Context) (<-chan runtime.SessionEvent, error) {
	return runtime.SubscribeSessionEventSources(ctx, runtime.SessionEventSource{Name: "local", Provider: p.local}, runtime.SessionEventSource{})
}

// DefaultProvider returns the unchanged city-default backend for overlay refresh.
// Session operations must continue through the router and its pinned lookup.
func (p *Provider) DefaultProvider() runtime.Provider { return p.local }

// RouteACP preserves the local transport router's dynamic route registration.
// Explicit execution bindings still select their own independent backend.
func (p *Provider) RouteACP(name string) {
	if router, ok := p.local.(interface{ RouteACP(string) }); ok {
		router.RouteACP(name)
	}
}
