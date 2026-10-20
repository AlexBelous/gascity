package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"time"

	"github.com/gastownhall/gascity/internal/config"
	"github.com/gastownhall/gascity/internal/runtime"
	"github.com/gastownhall/gascity/internal/runtime/routing"
	sshruntime "github.com/gastownhall/gascity/internal/runtime/ssh"
	"github.com/gastownhall/gascity/internal/session"
	"github.com/gastownhall/gascity/internal/shellquote"
)

type operatorRemoteProvider struct {
	*routing.Provider
	store             *session.Store
	profiles          []session.RemoteAdmissionProfile
	clients           map[runtime.RuntimeBinding]remoteAuthorityClient
	desiredByTemplate map[string]runtime.RuntimeBinding
}

// RemoteAdmissionProfiles returns immutable trusted operator registrations.
func (p *operatorRemoteProvider) RemoteAdmissionProfiles() []session.RemoteAdmissionProfile {
	return append([]session.RemoteAdmissionProfile(nil), p.profiles...)
}

func composeOperatorRemoteRuntimeRouting(ctx sessionProviderContext, local runtime.Provider, store *session.Store) (runtime.Provider, error) {
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
	profiles, err := loadRemoteOperatorProfiles()
	if err != nil {
		return nil, err
	}
	return composeLoadedOperatorRemoteRuntimeRouting(ctx, local, store, profiles, nil, nil)
}

func composeLoadedOperatorRemoteRuntimeRouting(ctx sessionProviderContext, local runtime.Provider, store *session.Store, loaded remoteOperatorProfiles, execute remoteRPCExecutor, transportFactory func(remoteOperatorProfile) (runtime.Provider, error)) (runtime.Provider, error) {
	if loaded.Version != 1 {
		return nil, errors.New("unsupported remote operator profile version")
	}
	selected := map[runtime.RuntimeBinding]remoteOperatorProfile{}
	selectors := map[string]runtime.RuntimeBinding{}
	clients := map[runtime.RuntimeBinding]remoteAuthorityClient{}
	var registrations []session.RemoteAdmissionProfile
	for _, profile := range loaded.Profiles {
		if profile.City != ctx.cityPath {
			continue
		}
		if err := validateRemoteOperatorProfile(profile); err != nil {
			return nil, err
		}
		if execute == nil {
			for _, key := range []string{profile.ControlKeyPath, profile.TransportKeyPath} {
				if err := checkRemoteCredentialFile(key, true); err != nil {
					return nil, err
				}
			}
			if err := checkRemoteCredentialFile(profile.KnownHostsPath, false); err != nil {
				return nil, err
			}
		}
		if _, exists := selectors[profile.Binding.Selector]; exists {
			return nil, errors.New("duplicate remote operator selector")
		}
		selected[profile.Binding] = profile
		selectors[profile.Binding.Selector] = profile.Binding
		client := remoteAuthorityClient{profile: profile, execute: execute}
		clients[profile.Binding] = client
		registrations = append(registrations, makeOperatorRemoteAdmissionProfile(client))
	}
	resolver := func(_ *config.City, a config.Agent) (runtime.RuntimeBinding, error) {
		binding, ok := selectors[a.Runtime]
		if !ok || selected[binding].Template != a.QualifiedName() {
			return runtime.RuntimeBinding{}, errors.New("explicit runtime has no trusted operator policy")
		}
		return binding, nil
	}
	factory := func(binding runtime.RuntimeBinding) (runtime.Provider, error) {
		profile, ok := selected[binding]
		if !ok {
			return nil, errors.New("pinned runtime policy unavailable")
		}
		if transportFactory != nil {
			return transportFactory(profile)
		}
		endpoint, err := sshruntime.ParseEndpoint(binding.Endpoint)
		if err != nil {
			return nil, err
		}
		endpoint.KeyPath = profile.TransportKeyPath
		endpoint.KnownHostsPath = profile.KnownHostsPath
		endpoint.Strict = true
		return sshruntime.NewSeamBacked(endpoint), nil
	}
	composed, err := composeAgentRuntimeRoutingUsing(ctx, local, store, resolver, factory)
	if err != nil {
		return nil, err
	}
	router, ok := composed.(*routing.Provider)
	if !ok {
		return composed, nil
	}
	desired := map[string]runtime.RuntimeBinding{}
	for _, agent := range ctx.agents {
		if binding, ok := selectors[agent.Runtime]; ok && selected[binding].Template == agent.QualifiedName() {
			desired[agent.QualifiedName()] = binding
		}
	}
	return &operatorRemoteProvider{Provider: router, store: store, profiles: registrations, clients: clients, desiredByTemplate: desired}, nil
}

func remoteRequestToWire(r session.RemoteAdmissionRequest) remoteWireRequest {
	return remoteWireRequest{r.City, r.SessionID, r.Template, r.RuntimeHandle, r.Generation, r.InstanceToken, r.Attempt, r.Binding}
}

func remoteReceiptFromWire(w remoteWireReceipt) session.RemoteAdmissionReceipt {
	r := w.Request
	i := w.Identity
	return session.RemoteAdmissionReceipt{
		Request:            session.RemoteAdmissionRequest{City: r.City, SessionID: r.SessionID, Template: r.Template, RuntimeHandle: r.RuntimeHandle, Generation: r.Generation, InstanceToken: r.InstanceToken, Attempt: r.Attempt, Binding: r.Binding},
		AuthorityReceiptID: w.AuthorityReceiptID, JournalRevision: w.JournalRevision, Phase: session.RemoteAdmissionPhase(w.Phase),
		Identity: session.RemoteVMIdentity{BootID: i.BootID, Unit: i.Unit, InvocationID: i.InvocationID, CgroupPath: i.CgroupPath, SourceSHA: i.SourceSHA, ManifestSHA256: i.ManifestSHA256, CgroupDevice: i.CgroupDevice, CgroupInode: i.CgroupInode, ModelUID: i.ModelUID, TransportUID: i.TransportUID, ScopeEmpty: i.ScopeEmpty, NeverEntered: i.NeverEntered, ScopeIdentity: session.RemoteScopeIdentity{BootID: i.ScopeIdentity.BootID, CgroupPath: i.ScopeIdentity.CgroupPath, Device: i.ScopeIdentity.Device, Inode: i.ScopeIdentity.Inode}},
	}
}

func makeOperatorRemoteAdmissionProfile(client remoteAuthorityClient) session.RemoteAdmissionProfile {
	policy := client.profile
	call := func(ctx context.Context, op string, r session.RemoteAdmissionRequest, prompt *string, environment map[string]string) (session.RemoteAdmissionReceipt, error) {
		if r.City != policy.City || r.Template != policy.Template {
			return session.RemoteAdmissionReceipt{}, errors.New("remote city identity changed")
		}
		receipt, err := client.call(ctx, op, remoteRequestToWire(r), prompt, environment)
		if err != nil {
			return session.RemoteAdmissionReceipt{}, err
		}
		return remoteReceiptFromWire(receipt), nil
	}
	profile := session.RemoteAdmissionProfile{
		Binding: policy.Binding, SourceSHA: policy.SourceSHA, ManifestSHA256: policy.ManifestSHA256, ModelUID: policy.ModelUID, TransportUID: policy.TransportUID, UnitPrefix: policy.UnitPrefix, CgroupParent: policy.CgroupParent,
		Launch: session.RemoteLaunchDescriptor{Command: "/usr/bin/sudo -n " + shellquote.Quote(policy.EntryPath) + " enter", WorkDir: policy.TransportWorkDir},
		Prepare: func(ctx context.Context, r session.RemoteAdmissionRequest) (session.RemoteAdmissionReceipt, error) {
			return call(ctx, "prepare", r, nil, nil)
		},
		Finish: func(ctx context.Context, b session.RemoteBeginReceipt, _ error) (session.RemoteAdmissionReceipt, error) {
			return awaitOperatorRemoteStart(ctx, b, func(ctx context.Context, r session.RemoteAdmissionRequest) (session.RemoteAdmissionReceipt, error) {
				return call(ctx, "observe", r, nil, nil)
			})
		},
		Reconcile: func(ctx context.Context, r session.RemoteAdmissionRequest) (session.RemoteAdmissionReceipt, error) {
			return call(ctx, "observe", r, nil, nil)
		},
	}
	profile.Begin = func(ctx context.Context, receipt session.RemoteAdmissionReceipt, cfg runtime.Config) (session.RemoteBeginReceipt, runtime.Config, error) {
		digest := sha256.Sum256([]byte(cfg.Command))
		if hex.EncodeToString(digest[:]) != policy.AgentCommandSHA256 {
			return session.RemoteBeginReceipt{}, runtime.Config{}, errors.New("remote agent command policy changed")
		}
		// Optional city context may not retarget a pinned operator policy.
		if city, present := cfg.Env["GC_CITY"]; present && city != policy.City {
			return session.RemoteBeginReceipt{}, runtime.Config{}, errors.New("remote city environment rejected")
		}
		identity := map[string]string{}
		for _, key := range []string{"GC_SESSION_ID", "GC_TEMPLATE", "GC_SESSION_NAME", "GC_RUNTIME_EPOCH", "GC_INSTANCE_TOKEN"} {
			if value, ok := cfg.Env[key]; ok {
				identity[key] = value
			}
		}
		if _, present := cfg.Env["GC_CITY"]; present {
			identity["GC_CITY"] = policy.City
		}
		if !cfg.PromptText.IsSet() {
			return session.RemoteBeginReceipt{}, runtime.Config{}, errors.New("remote literal prompt unavailable")
		}
		prompt := cfg.PromptText.Literal()
		if cfg.PromptSuffix != "" && cfg.PromptSuffix != shellquote.Quote(prompt) {
			return session.RemoteBeginReceipt{}, runtime.Config{}, errors.New("remote literal prompt mismatch")
		}
		staged, err := call(ctx, "stage_prompt", receipt.Request, &prompt, map[string]string{})
		if err != nil || staged.Request != receipt.Request || staged.AuthorityReceiptID != receipt.AuthorityReceiptID || staged.JournalRevision <= receipt.JournalRevision || staged.Phase != session.RemoteAdmissionPrepared || staged.Identity.BootID != receipt.Identity.BootID || staged.Identity.SourceSHA != policy.SourceSHA || staged.Identity.ManifestSHA256 != policy.ManifestSHA256 || staged.Identity.ModelUID != policy.ModelUID || staged.Identity.TransportUID != policy.TransportUID || staged.Identity.NeverEntered || staged.Identity.ScopeIdentity != receipt.Identity.ScopeIdentity {
			return session.RemoteBeginReceipt{}, runtime.Config{}, errors.New("remote prompt stage unconfirmed")
		}
		rewritten := runtime.Config{Command: profile.Launch.Command + " " + receipt.Request.Attempt, WorkDir: profile.Launch.WorkDir, Env: identity, Lifecycle: runtime.LifecycleOneShot, ProcessNames: []string{"sudo"}}
		return session.RemoteBeginReceipt{Reservation: receipt, IntentID: receipt.AuthorityReceiptID + ":" + strconv.FormatUint(staged.JournalRevision, 10)}, rewritten, nil
	}
	return profile
}

// remoteAuthorityForName recovers the exact persisted incarnation, including a
// terminal tombstone after release. A zero request is permitted only when
// the source accessor proves a virgin or newly fenced incarnation; malformed,
// missing historical records and read failures remain unknown.
func (p *operatorRemoteProvider) remoteAuthorityForName(name string) (remoteAuthorityClient, session.RemoteAdmissionRequest, bool, error) {
	binding, err := p.ResolveRuntime(name)
	if err != nil {
		// A configured fresh SID may be observed before first binding. Only the
		// exact uncached virgin incarnation is known to have no authorized attempt.
		id, template, pin, pinned, readErr := p.store.RuntimeRoute(name)
		desired, configured := p.desiredByTemplate[template]
		if readErr == nil && id != "" && !pinned && pin == (runtime.RuntimeBinding{}) && configured {
			request, _, requestErr := p.store.HeldRemoteAdmissionRequest(id)
			if requestErr == nil && request.Attempt == "" {
				if client, ok := p.clients[desired]; ok {
					return client, request, true, nil
				}
			}
		}
		return remoteAuthorityClient{}, session.RemoteAdmissionRequest{}, true, err
	}
	if binding == (runtime.RuntimeBinding{}) {
		return remoteAuthorityClient{}, session.RemoteAdmissionRequest{}, false, nil
	}
	id, _, recorded, found, err := p.store.RuntimeRoute(name)
	if err != nil || !found || recorded != binding {
		return remoteAuthorityClient{}, session.RemoteAdmissionRequest{}, true, errors.New("remote session route unconfirmed")
	}
	request, _, err := p.store.HeldRemoteAdmissionRequest(id)
	if err != nil || (request.Attempt != "" && request.Binding != binding) {
		return remoteAuthorityClient{}, session.RemoteAdmissionRequest{}, true, errors.New("remote attempt unconfirmed")
	}
	client, ok := p.clients[binding]
	if !ok {
		return client, request, true, errors.New("remote authority unavailable")
	}
	return client, request, true, nil
}

func validateOperatorRemoteReceipt(receipt remoteWireReceipt, request session.RemoteAdmissionRequest, policy remoteOperatorProfile) error {
	identity := receipt.Identity
	if receipt.Request != remoteRequestToWire(request) || identity.SourceSHA != policy.SourceSHA || identity.ManifestSHA256 != policy.ManifestSHA256 || identity.ModelUID != policy.ModelUID || identity.TransportUID != policy.TransportUID ||
		!remoteHexPin(identity.BootID, 32) {
		return errors.New("remote kernel identity unconfirmed")
	}
	if receipt.Phase == "prepared" {
		if identity.ScopeIdentity.BootID == identity.BootID && identity.ScopeIdentity.CgroupPath == policy.CgroupParent && identity.ScopeIdentity.Device > 0 && identity.ScopeIdentity.Inode > 0 && !identity.ScopeEmpty && !identity.NeverEntered && identity.Unit == "" && identity.InvocationID == "" && identity.CgroupPath == "" && identity.CgroupDevice == 0 && identity.CgroupInode == 0 {
			return nil
		}
		return errors.New("remote prepared identity unconfirmed")
	}
	if receipt.Phase == "exited" && (identity.ScopeIdentity.BootID != identity.BootID || identity.ScopeIdentity.CgroupPath != policy.CgroupParent || identity.ScopeIdentity.Device == 0 || identity.ScopeIdentity.Inode == 0) {
		return errors.New("remote parent scope identity unconfirmed")
	}
	if identity.NeverEntered {
		if receipt.Phase == "exited" && identity.ScopeEmpty && identity.Unit == "" && identity.InvocationID == "" && identity.CgroupPath == "" && identity.CgroupDevice == 0 && identity.CgroupInode == 0 {
			return nil
		}
		return errors.New("remote never-entered identity invalid")
	}
	if identity.Unit != policy.UnitPrefix+"-"+request.Attempt+".service" || identity.CgroupPath != policy.CgroupParent+"/"+identity.Unit || !remoteHexPin(identity.InvocationID, 32) || identity.CgroupDevice == 0 || identity.CgroupInode == 0 {
		return errors.New("remote kernel identity unconfirmed")
	}
	if receipt.Phase != "entered" && (receipt.Phase != "exited" || !identity.ScopeEmpty) {
		return errors.New("remote attempt state unconfirmed")
	}
	return nil
}

// ObserveLivenessWithError observes the root-owned model cgroup, not its SSH
// bridge. Uncertain observations preserve the session and admission hold.
func (p *operatorRemoteProvider) ObserveLivenessWithError(name string, names []string) (runtime.Liveness, error) {
	client, request, remote, err := p.remoteAuthorityForName(name)
	if !remote {
		return p.Provider.ObserveLivenessWithError(name, names)
	}
	unknown := runtime.Liveness{Running: true, Alive: true}
	if err != nil {
		return unknown, fmt.Errorf("%w: remote authority unavailable", runtime.ErrRuntimeUnavailable)
	}
	if request.Attempt == "" {
		return runtime.Liveness{}, nil
	}
	receipt, err := client.call(context.Background(), "observe", remoteRequestToWire(request), nil, nil)
	if err != nil || validateOperatorRemoteReceipt(receipt, request, client.profile) != nil {
		return unknown, fmt.Errorf("%w: remote model observation unknown", runtime.ErrRuntimeUnavailable)
	}
	if receipt.Phase == "prepared" {
		if err := p.store.ValidatePreparedRemoteAdmissionObservation(remoteReceiptFromWire(receipt)); err != nil {
			return unknown, fmt.Errorf("%w: remote prepared source unconfirmed", runtime.ErrRuntimeUnavailable)
		}
		// A reserved unconsumed attempt has no model yet. Its durable source hold
		// remains authoritative; this is never permission to release capacity.
		return runtime.Liveness{}, nil
	}
	live := receipt.Phase == "entered"
	return runtime.Liveness{Running: live, Alive: live}, nil
}

// ObserveLiveness conservatively holds on an unknown root-authority result.
func (p *operatorRemoteProvider) ObserveLiveness(name string, names []string) runtime.Liveness {
	value, _ := p.ObserveLivenessWithError(name, names)
	return value
}

// IsRunning observes the remote model authority for a bound remote session.
func (p *operatorRemoteProvider) IsRunning(name string) bool {
	if !p.remoteOperationUnsupported(name) {
		return p.Provider.IsRunning(name)
	}
	return p.ObserveLiveness(name, nil).Running
}

// ProcessAlive observes the model even though it is not a bridge descendant.
func (p *operatorRemoteProvider) ProcessAlive(name string, names []string) bool {
	if !p.remoteOperationUnsupported(name) {
		return p.Provider.ProcessAlive(name, names)
	}
	return p.ObserveLiveness(name, names).Alive
}

// IsDeadRuntimeSession never treats an unavailable authority as a dead model.
func (p *operatorRemoteProvider) IsDeadRuntimeSession(name string) (bool, error) {
	if !p.remoteOperationUnsupported(name) {
		return p.Provider.IsDeadRuntimeSession(name)
	}
	v, err := p.ObserveLivenessWithError(name, nil)
	return !v.Running, err
}

// Stop cancels the exact root-owned attempt. It never kills a reused tmux name.
func (p *operatorRemoteProvider) Stop(name string) error {
	client, request, remote, err := p.remoteAuthorityForName(name)
	if !remote {
		return p.Provider.Stop(name)
	}
	if err != nil {
		return fmt.Errorf("%w: remote cancellation identity unknown", runtime.ErrStopRefused)
	}
	if request.Attempt == "" {
		return nil
	}
	receipt, err := client.call(context.Background(), "cancel", remoteRequestToWire(request), nil, nil)
	if err != nil || validateOperatorRemoteReceipt(receipt, request, client.profile) != nil || receipt.Phase != "exited" || !receipt.Identity.ScopeEmpty {
		return fmt.Errorf("%w: remote cancellation unconfirmed", runtime.ErrStopRefused)
	}
	// The finite bridge exits with its exact service; blindly stopping a tmux
	// handle here could race a subsequent attempt reusing that handle.
	return nil
}

func (p *operatorRemoteProvider) remoteOperationUnsupported(name string) bool {
	binding, err := p.ResolveRuntime(name)
	return err != nil || binding != (runtime.RuntimeBinding{})
}

// Interrupt refuses a transport-only signal that cannot prove model delivery.
func (p *operatorRemoteProvider) Interrupt(name string) error {
	if p.remoteOperationUnsupported(name) {
		return runtime.ErrStopRefused
	}
	return p.Provider.Interrupt(name)
}

// Relaunch requires a fresh admission instead of a warm bridge command.
func (p *operatorRemoteProvider) Relaunch(ctx context.Context, name string, cfg runtime.Config) error {
	if p.remoteOperationUnsupported(name) {
		return runtime.ErrRelaunchUnsupported
	}
	return p.Provider.Relaunch(ctx, name, cfg)
}

// RunLive prevents shell hooks from running outside the guarded model service.
func (p *operatorRemoteProvider) RunLive(name string, cfg runtime.Config) error {
	if p.remoteOperationUnsupported(name) {
		return runtime.ErrInteractionUnsupported
	}
	return p.Provider.RunLive(name, cfg)
}

// CopyTo refuses misleading deployment to a bridge's separate filesystem.
func (p *operatorRemoteProvider) CopyTo(name, src, dst string) error {
	if p.remoteOperationUnsupported(name) {
		return runtime.ErrInteractionUnsupported
	}
	return p.Provider.CopyTo(name, src, dst)
}

// Exec refuses arbitrary commands in the transport account for guarded routes.
func (p *operatorRemoteProvider) Exec(ctx context.Context, name string, argv []string) ([]byte, int, error) {
	if p.remoteOperationUnsupported(name) {
		return nil, 0, runtime.ErrExecUnsupported
	}
	return p.Provider.Exec(ctx, name, argv)
}

// Nudge refuses a transport-only input path that does not reach the model.
func (p *operatorRemoteProvider) Nudge(name string, content []runtime.ContentBlock) error {
	if p.remoteOperationUnsupported(name) {
		return runtime.ErrInteractionUnsupported
	}
	return p.Provider.Nudge(name, content)
}

// SendKeys refuses a transport-only input path that does not reach the model.
func (p *operatorRemoteProvider) SendKeys(name string, keys ...string) error {
	if p.remoteOperationUnsupported(name) {
		return runtime.ErrInteractionUnsupported
	}
	return p.Provider.SendKeys(name, keys...)
}

// Start permits only the exact pre-authorized bridge descriptor, even when a
// caller invokes the runtime directly instead of using the session manager.
func (p *operatorRemoteProvider) Start(ctx context.Context, name string, cfg runtime.Config) error {
	client, request, remote, err := p.remoteAuthorityForName(name)
	if !remote {
		return p.Provider.Start(ctx, name, cfg)
	}
	if err != nil || request.Attempt == "" {
		return fmt.Errorf("%w: remote start identity unknown", runtime.ErrRuntimeUnavailable)
	}
	identity := map[string]string{"GC_SESSION_ID": request.SessionID, "GC_TEMPLATE": request.Template, "GC_SESSION_NAME": request.RuntimeHandle, "GC_RUNTIME_EPOCH": request.Generation, "GC_INSTANCE_TOKEN": request.InstanceToken}
	if city, ok := cfg.Env["GC_CITY"]; ok {
		if city != client.profile.City {
			return errors.New("remote city environment rejected")
		}
		identity["GC_CITY"] = client.profile.City
	}
	expected := runtime.Config{Command: "/usr/bin/sudo -n " + shellquote.Quote(client.profile.EntryPath) + " enter " + request.Attempt, WorkDir: client.profile.TransportWorkDir, Env: identity, Lifecycle: runtime.LifecycleOneShot, ProcessNames: []string{"sudo"}}
	if !reflect.DeepEqual(cfg, expected) {
		return errors.New("remote launch descriptor rejected")
	}
	observed, err := client.call(ctx, "observe", remoteRequestToWire(request), nil, nil)
	if err != nil || observed.Phase != "prepared" || validateOperatorRemoteReceipt(observed, request, client.profile) != nil || p.store.ValidatePreparedRemoteAdmissionObservation(remoteReceiptFromWire(observed)) != nil {
		return fmt.Errorf("%w: remote prepared authority unconfirmed", runtime.ErrRuntimeUnavailable)
	}
	return p.Provider.Start(ctx, name, cfg)
}

// NudgeNow refuses the bridge channel for a separately owned finite model.
func (p *operatorRemoteProvider) NudgeNow(name string, content []runtime.ContentBlock) error {
	if p.remoteOperationUnsupported(name) {
		return runtime.ErrInteractionUnsupported
	}
	return p.Provider.NudgeNow(name, content)
}

// SnapshotIdle uses authoritative model liveness rather than tmux prompt text.
func (p *operatorRemoteProvider) SnapshotIdle(name string) (bool, error) {
	if !p.remoteOperationUnsupported(name) {
		return p.Provider.SnapshotIdle(name)
	}
	value, err := p.ObserveLivenessWithError(name, nil)
	return !value.Alive, err
}

// Attach refuses an interactive bridge that does not own the model terminal.
func (p *operatorRemoteProvider) Attach(name string) error {
	if p.remoteOperationUnsupported(name) {
		return runtime.ErrInteractionUnsupported
	}
	return p.Provider.Attach(name)
}

// IsAttached reports no interactive attachment for a guarded finite model.
func (p *operatorRemoteProvider) IsAttached(name string) bool {
	if p.remoteOperationUnsupported(name) {
		return false
	}
	return p.Provider.IsAttached(name)
}

// IsAttachedWithError distinguishes a failed route lookup from finite policy.
func (p *operatorRemoteProvider) IsAttachedWithError(name string) (bool, error) {
	binding, err := p.ResolveRuntime(name)
	if err != nil {
		return true, err
	}
	if binding != (runtime.RuntimeBinding{}) {
		return false, nil
	}
	return p.Provider.IsAttachedWithError(name)
}

// awaitOperatorRemoteStart bridges asynchronous SSH/tmux and root service entry.
// Read-only reconciliation never repeats Start or frees capacity on timeout.
func awaitOperatorRemoteStart(ctx context.Context, begin session.RemoteBeginReceipt, observe func(context.Context, session.RemoteAdmissionRequest) (session.RemoteAdmissionReceipt, error)) (session.RemoteAdmissionReceipt, error) {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	previous := begin.Reservation
	for {
		receipt, err := observe(ctx, previous.Request)
		if err != nil {
			return session.RemoteAdmissionReceipt{}, err
		}
		if receipt.Request != previous.Request || receipt.AuthorityReceiptID != previous.AuthorityReceiptID || receipt.JournalRevision < previous.JournalRevision || receipt.Identity.BootID != previous.Identity.BootID || receipt.Identity.ScopeIdentity != previous.Identity.ScopeIdentity || receipt.Identity.SourceSHA != previous.Identity.SourceSHA || receipt.Identity.ManifestSHA256 != previous.Identity.ManifestSHA256 || receipt.Identity.ModelUID != previous.Identity.ModelUID || receipt.Identity.TransportUID != previous.Identity.TransportUID {
			return session.RemoteAdmissionReceipt{}, errors.New("remote startup authority changed")
		}
		if receipt.Identity.NeverEntered {
			return session.RemoteAdmissionReceipt{}, errors.New("remote model entry was canceled")
		}
		if receipt.Phase == session.RemoteAdmissionEntered || receipt.Phase == session.RemoteAdmissionExited {
			return receipt, nil
		}
		if receipt.Phase != session.RemoteAdmissionPrepared && receipt.Phase != session.RemoteAdmissionUnknown {
			return session.RemoteAdmissionReceipt{}, errors.New("remote startup phase invalid")
		}
		previous = receipt
		select {
		case <-ctx.Done():
			return session.RemoteAdmissionReceipt{}, errors.New("remote startup remains unconfirmed")
		case <-ticker.C:
		}
	}
}
