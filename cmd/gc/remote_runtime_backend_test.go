package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/agent"
	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/clock"
	"github.com/gastownhall/gascity/internal/config"
	"github.com/gastownhall/gascity/internal/events"
	"github.com/gastownhall/gascity/internal/rollout/gate"
	"github.com/gastownhall/gascity/internal/runtime"
	"github.com/gastownhall/gascity/internal/session"
	"github.com/gastownhall/gascity/internal/session/sessiontest"
)

func remoteBackendFixture(t *testing.T) (remoteOperatorProfile, *session.Store, beads.Store, sessionProviderContext) {
	t.Helper()
	command := "codex exec --model pinned"
	digest := sha256.Sum256([]byte(command))
	profile := remoteOperatorProfile{
		City: "/controller/city", Template: "worker", AgentCommandSHA256: hex.EncodeToString(digest[:]),
		Binding:   runtime.RuntimeBinding{Selector: "ssh:transport@192.0.2.1", Endpoint: "transport@192.0.2.1", ResourceDomain: "fixture-vm", PolicyDigest: strings.Repeat("a", 64)},
		SourceSHA: strings.Repeat("b", 40), ManifestSHA256: strings.Repeat("c", 64), ModelUID: 1001, TransportUID: 1002, UnitPrefix: "gc-fixture", CgroupParent: "/gc-fixture.slice",
		ControlUser: "control", ControlKeyPath: "/keys/control", TransportKeyPath: "/keys/transport", KnownHostsPath: "/keys/hosts", EntryPath: "/usr/local/libexec/gascity-remote-entry", TransportWorkDir: "/transport",
	}
	mem := beads.NewMemStore()
	mem.HonorExplicitIDs = true
	_, err := beads.OpenStoreAtForCity(context.Background(), beads.StoreOpenOptions{Provider: "file", ConditionalWrites: gate.Require, OpenFileStore: func() (beads.Store, error) { return mem, nil }})
	if err != nil {
		t.Fatal(err)
	}
	store := session.NewStore(beads.SessionStore{Store: mem})
	_, err = store.CreateSessionInfo(session.CreateSpec{ID: "sid-remote-fixture", AgentName: "worker", Metadata: map[string]string{"template": "worker", "session_name": "runtime-fixture", "generation": "1", "instance_token": strings.Repeat("d", 32)}})
	if err != nil {
		t.Fatal(err)
	}
	cfg := &config.City{Agents: []config.Agent{{Name: "worker", Runtime: profile.Binding.Selector}}}
	return profile, store, mem, sessionProviderContext{cfg: cfg, agents: cfg.Agents, cityPath: profile.City}
}

func TestOperatorRemoteBackendRegistryAndLiteralStage(t *testing.T) {
	profile, store, mem, providerCtx := remoteBackendFixture(t)
	phase := "prepared"
	cancelCalls := 0
	revision := uint64(0)
	literal := "literal $HOME $(false) ' \" \n prompt"
	executor := func(_ context.Context, _ []string, input []byte) ([]byte, error) {
		var request remoteRPCRequest
		if err := strictRemoteJSON(input, &request); err != nil {
			t.Fatal(err)
		}
		if request.Operation == "cancel" {
			cancelCalls++
		}
		revision++
		if request.Operation == "stage_prompt" && (request.Prompt == nil || *request.Prompt != literal) {
			t.Fatal("prompt changed")
		}
		identity := remoteWireIdentity{BootID: strings.Repeat("e", 32), Unit: profile.UnitPrefix + "-" + request.Request.Attempt + ".service", InvocationID: strings.Repeat("f", 32), CgroupDevice: 1, CgroupInode: 2, SourceSHA: profile.SourceSHA, ManifestSHA256: profile.ManifestSHA256, ModelUID: profile.ModelUID, TransportUID: profile.TransportUID}
		identity.CgroupPath = profile.CgroupParent + "/" + identity.Unit
		if phase == "prepared" || phase == "unknown" {
			identity.Unit = ""
			identity.InvocationID = ""
			identity.CgroupPath = ""
			identity.CgroupDevice = 0
			identity.CgroupInode = 0
		}
		empty := phase == "exited"
		never := false
		return json.Marshal(remoteRPCResponse{SchemaVersion: 1, Receipt: remoteWireReceipt{Request: request.Request, AuthorityReceiptID: "fixture-receipt", JournalRevision: revision, Phase: phase, Identity: identity}, ScopeEmpty: &empty, NeverEntered: &never, ScopeIdentity: &remoteScopeIdentity{BootID: identity.BootID, Device: 3, Inode: 4, CgroupPath: profile.CgroupParent}})
	}
	composed, err := composeLoadedOperatorRemoteRuntimeRouting(providerCtx, runtime.NewFake(), store, remoteOperatorProfiles{Version: 1, Profiles: []remoteOperatorProfile{profile}}, executor, nil)
	if err != nil {
		t.Fatal(err)
	}
	wrapped := composed.(*operatorRemoteProvider)
	registrations := wrapped.RemoteAdmissionProfiles()
	if len(registrations) != 1 {
		t.Fatal("missing registry")
	}
	admitted, err := session.WithRemoteAdmissionProfiles(context.Background(), registrations...)
	if err != nil {
		t.Fatal(err)
	}
	admitted, allowed, reason := session.AdmitCapacityStart(admitted, profile.City, "worker", "sid-remote-fixture", time.Now(), wrapped, mem)
	if !allowed {
		t.Fatalf("admission refused: %s", reason)
	}
	_ = admitted
	request, held, err := store.HeldRemoteAdmissionRequest("sid-remote-fixture")
	if err != nil || !held {
		t.Fatalf("missing durable hold %v", err)
	}
	liveBefore, err := wrapped.ObserveLivenessWithError("runtime-fixture", nil)
	if err != nil || liveBefore.Running || liveBefore.Alive {
		t.Fatalf("prepared reservation blocked first model start: %v %v", liveBefore, err)
	}
	info, err := store.Get("sid-remote-fixture")
	if err != nil {
		t.Fatal(err)
	}
	if releaseBeadScopedPoolRuntime(info, wrapped, store, io.Discard) || cancelCalls != 0 {
		t.Fatal("prepared false liveness bypassed canonical cleanup hold")
	}
	if held, err := store.RemoteAdmissionHeld("sid-remote-fixture"); err != nil || !held {
		t.Fatal("prepared liveness released capacity")
	}
	prepared, err := registrations[0].Prepare(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	cfg := runtime.Config{Command: "codex exec --model pinned", PromptText: runtime.PromptTextFromLiteral(literal), Env: map[string]string{"GC_SESSION_ID": request.SessionID, "GC_TEMPLATE": request.Template, "GC_SESSION_NAME": request.RuntimeHandle, "GC_RUNTIME_EPOCH": request.Generation, "GC_INSTANCE_TOKEN": request.InstanceToken}, PreStart: []string{"must not run"}}
	begin, rewritten, err := registrations[0].Begin(context.Background(), prepared, cfg)
	if err != nil || begin.IntentID == "" {
		t.Fatalf("stage: %v", err)
	}
	if rewritten.PromptText.Literal() != "" || len(rewritten.PreStart) != 0 || rewritten.Command != registrations[0].Launch.Command+" "+request.Attempt {
		t.Fatal("unsafe launch descriptor")
	}
	phase = "entered"
	live, err := wrapped.ObserveLivenessWithError("runtime-fixture", []string{"codex"})
	if err != nil || !live.Alive || !live.Running {
		t.Fatalf("model health: %v %v", live, err)
	}
	phase = "exited"
	live, err = wrapped.ObserveLivenessWithError("runtime-fixture", nil)
	if err != nil || live.Alive || live.Running {
		t.Fatalf("terminal: %v %v", live, err)
	}
	if err := wrapped.Stop("runtime-fixture"); err != nil {
		t.Fatal(err)
	}
}

func TestOperatorRemoteBackendUnknownIsHold(t *testing.T) {
	profile, store, mem, providerCtx := remoteBackendFixture(t)
	composed, err := composeLoadedOperatorRemoteRuntimeRouting(providerCtx, runtime.NewFake(), store, remoteOperatorProfiles{Version: 1, Profiles: []remoteOperatorProfile{profile}}, func(context.Context, []string, []byte) ([]byte, error) { return nil, context.DeadlineExceeded }, nil)
	if err != nil {
		t.Fatal(err)
	}
	wrapped := composed.(*operatorRemoteProvider)
	if _, err = wrapped.BindRuntime(context.Background(), "sid-remote-fixture", "worker"); err != nil {
		t.Fatal(err)
	}
	if err := mem.Update("sid-remote-fixture", beads.UpdateOpts{Metadata: map[string]string{"remote_admission_hold_v1": "invalid"}}); err != nil {
		t.Fatal(err)
	}
	live, err := wrapped.ObserveLivenessWithError("runtime-fixture", nil)
	if err == nil || !live.Alive || !live.Running {
		t.Fatalf("lost authority treated as dead %v %v", live, err)
	}
	if err := wrapped.Stop("runtime-fixture"); err == nil {
		t.Fatal("stopped without exactattempt")
	}
	if err := wrapped.Start(context.Background(), "runtime-fixture", runtime.Config{Command: "arbitrary"}); err == nil {
		t.Fatal("started unguarded command")
	}
}

type remoteBackendStartingTransport struct {
	*runtime.Fake
	started func()
}

func (p *remoteBackendStartingTransport) Start(ctx context.Context, name string, cfg runtime.Config) error {
	err := p.Fake.Start(ctx, name, cfg)
	if err == nil && p.started != nil {
		p.started()
	}
	return err
}

func TestOperatorRemoteBackendPublicCreateReachesRootAuthority(t *testing.T) {
	profile, store, mem, providerCtx := remoteBackendFixture(t)
	phase := "prepared"
	revision := uint64(0)
	prepares := 0
	startupReads := 0
	executor := func(_ context.Context, _ []string, input []byte) ([]byte, error) {
		var request remoteRPCRequest
		if err := strictRemoteJSON(input, &request); err != nil {
			t.Fatal(err)
		}
		if request.Operation == "prepare" {
			prepares++
		}
		revision++
		if phase == "unknown" && request.Operation == "observe" {
			startupReads++
			if startupReads >= 2 {
				phase = "entered"
			}
		}
		identity := remoteWireIdentity{BootID: strings.Repeat("e", 32), Unit: profile.UnitPrefix + "-" + request.Request.Attempt + ".service", InvocationID: strings.Repeat("f", 32), CgroupDevice: 1, CgroupInode: 2, SourceSHA: profile.SourceSHA, ManifestSHA256: profile.ManifestSHA256, ModelUID: profile.ModelUID, TransportUID: profile.TransportUID}
		identity.CgroupPath = profile.CgroupParent + "/" + identity.Unit
		if phase == "prepared" || phase == "unknown" {
			identity.Unit = ""
			identity.InvocationID = ""
			identity.CgroupPath = ""
			identity.CgroupDevice = 0
			identity.CgroupInode = 0
		}
		empty := phase == "exited"
		never := false
		return json.Marshal(remoteRPCResponse{SchemaVersion: 1, Receipt: remoteWireReceipt{Request: request.Request, AuthorityReceiptID: "create-receipt", JournalRevision: revision, Phase: phase, Identity: identity}, ScopeEmpty: &empty, NeverEntered: &never, ScopeIdentity: &remoteScopeIdentity{BootID: identity.BootID, CgroupPath: profile.CgroupParent, Device: 3, Inode: 4}})
	}
	transport := &remoteBackendStartingTransport{Fake: runtime.NewFake(), started: func() { phase = "unknown" }}
	composed, err := composeLoadedOperatorRemoteRuntimeRouting(providerCtx, runtime.NewFake(), store, remoteOperatorProfiles{Version: 1, Profiles: []remoteOperatorProfile{profile}}, executor, func(remoteOperatorProfile) (runtime.Provider, error) { return transport, nil })
	if err != nil {
		t.Fatal(err)
	}
	wrapped := composed.(*operatorRemoteProvider)
	// No caller-provided registry: production provider registration must reach
	// the public manager path, including its first incarnation and binding.
	manager := session.NewManagerWithOptions(mem, wrapped, session.WithCityPath(profile.City))
	created, err := manager.CreateSession(context.Background(), session.CreateOptions{Template: "worker", ExplicitName: "runtime-created", Command: "codex exec --model pinned", WorkDir: profile.City, Provider: "codex", Hints: runtime.Config{PromptText: runtime.PromptTextFromLiteral("fixture prompt"), Lifecycle: runtime.LifecycleOneShot}})
	if err != nil || created.ID == "" || prepares != 1 || startupReads != 2 || transport.CountCalls("Start", "runtime-created") != 1 {
		t.Fatalf("public firstcreate failed: id=%s prepare=%d starts=%d err=%v", created.ID, prepares, transport.CountCalls("Start", "runtime-created"), err)
	}
	cfg := transport.LastStartConfig("runtime-created")
	if cfg == nil || len(cfg.PreStart) > 0 || cfg.Command == "codex exec --model pinned" || cfg.PromptText.IsSet() {
		t.Fatal("unguarded command reached transport")
	}
	t.Run("status keeps historical route after agent removal", func(t *testing.T) {
		removed := providerCtx
		removed.cfg = &config.City{}
		removed.agents = nil
		local := runtime.NewFake()
		historical, e := composeLoadedOperatorRemoteRuntimeRouting(removed, local, store, remoteOperatorProfiles{Version: 1, Profiles: []remoteOperatorProfile{profile}}, executor, func(remoteOperatorProfile) (runtime.Provider, error) { return transport, nil })
		if e != nil {
			t.Fatal(e)
		}
		status := newBoundedStatusProvider(historical)
		beforeReads := revision
		beforeStarts := transport.CountCalls("Start", "runtime-created")
		beforeStops := transport.CountCalls("Stop", "runtime-created")
		observer := status.(runtime.LivenessObserverWithError)
		live, e := observer.ObserveLivenessWithError("runtime-created", nil)
		if e != nil || !live.Running || !live.Alive || revision <= beforeReads {
			t.Fatalf("historical remote status not observed: live=%+v err=%v", live, e)
		}
		if local.CountCalls("IsRunning", "runtime-created") != 0 || local.CountCalls("Start", "runtime-created") != 0 || local.CountCalls("Stop", "runtime-created") != 0 || transport.CountCalls("Start", "runtime-created") != beforeStarts || transport.CountCalls("Stop", "runtime-created") != beforeStops {
			t.Fatal("historical status performed a native fallback or lifecycle effect")
		}
	})
	phase = "prepared"
	live, err := wrapped.ObserveLivenessWithError("runtime-created", nil)
	if err == nil || !live.Running || !live.Alive {
		t.Fatal("stale root prepared over entered source was accepted")
	}
	phase = "exited"
	authority, err := session.ContextWithProviderRemoteAdmission(context.Background(), wrapped)
	if err != nil {
		t.Fatal(err)
	}
	if err = store.ReconcileRemoteAdmissionHold(authority, created.ID); err != nil {
		t.Fatal(err)
	}
	phase = "prepared"
	live, err = wrapped.ObserveLivenessWithError("runtime-created", nil)
	if err == nil || !live.Running || !live.Alive {
		t.Fatal("prepared observation without active hold was accepted")
	}
}

func TestOperatorRemoteBackendKnownVirginIsAbsent(t *testing.T) {
	profile, store, _, providerCtx := remoteBackendFixture(t)
	calls := 0
	composed, err := composeLoadedOperatorRemoteRuntimeRouting(providerCtx, runtime.NewFake(), store, remoteOperatorProfiles{Version: 1, Profiles: []remoteOperatorProfile{profile}}, func(context.Context, []string, []byte) ([]byte, error) { calls++; return nil, context.DeadlineExceeded }, nil)
	if err != nil {
		t.Fatal(err)
	}
	wrapped := composed.(*operatorRemoteProvider)
	// The controller observes before BindRuntime/admission on a fresh SID.
	live, err := wrapped.ObserveLivenessWithError("runtime-fixture", nil)
	if err != nil || live.Running || live.Alive || calls != 0 {
		t.Fatalf("fresh SID blocked before first admission: %v %v calls=%d", live, err, calls)
	}
}

func TestOperatorRemoteBackendReloadPreservesExactLocalProvider(t *testing.T) {
	profile, store, _, providerCtx := remoteBackendFixture(t)
	local := runtime.NewFake()
	if err := local.Start(context.Background(), "existing-local", runtime.Config{Command: "fixture"}); err != nil {
		t.Fatal(err)
	}
	load := func(ctx sessionProviderContext, got runtime.Provider) (runtime.Provider, error) {
		if got != local {
			t.Fatal("reload wrapped a prior routing overlay as local")
		}
		return composeLoadedOperatorRemoteRuntimeRouting(ctx, got, store, remoteOperatorProfiles{Version: 1, Profiles: []remoteOperatorProfile{profile}}, func(context.Context, []string, []byte) ([]byte, error) { return nil, context.DeadlineExceeded }, nil)
	}
	initial, err := load(providerCtx, local)
	if err != nil {
		t.Fatal(err)
	}
	refreshed, err := refreshAgentRuntimeRoutingUsing(providerCtx, initial, load)
	if err != nil {
		t.Fatal(err)
	}
	next, ok := refreshed.(*operatorRemoteProvider)
	if !ok || next == initial || next.DefaultProvider() != local || len(next.RemoteAdmissionProfiles()) != 1 {
		t.Fatal("reload lost registry or exact local provider")
	}
	if !local.IsRunning("existing-local") || local.CountCalls("Stop", "existing-local") != 0 {
		t.Fatal("reload stopped a shared local session")
	}
	if !agentRuntimeRoutingActive(refreshed, &config.City{}) {
		t.Fatal("routing overlay disappeared after agent config change")
	}
}

func TestOperatorRemoteBackendControllerTwoOccurrencesUseFinalIncarnation(t *testing.T) {
	profile, store, mem, providerCtx := remoteBackendFixture(t)
	phases := map[string]string{}
	revisions := map[string]uint64{}
	var prepared []remoteWireRequest
	executor := func(_ context.Context, _ []string, input []byte) ([]byte, error) {
		var request remoteRPCRequest
		if err := strictRemoteJSON(input, &request); err != nil {
			return nil, err
		}
		attempt := request.Request.Attempt
		if request.Operation == "prepare" {
			prepared = append(prepared, request.Request)
			phases[attempt] = "prepared"
		}
		revisions[attempt]++
		phase := phases[attempt]
		identity := remoteWireIdentity{BootID: strings.Repeat("e", 32), Unit: profile.UnitPrefix + "-" + attempt + ".service", InvocationID: strings.Repeat("f", 32), CgroupDevice: 1, CgroupInode: 2, SourceSHA: profile.SourceSHA, ManifestSHA256: profile.ManifestSHA256, ModelUID: profile.ModelUID, TransportUID: profile.TransportUID}
		identity.CgroupPath = profile.CgroupParent + "/" + identity.Unit
		if phase == "prepared" || phase == "unknown" {
			identity.Unit = ""
			identity.InvocationID = ""
			identity.CgroupPath = ""
			identity.CgroupDevice = 0
			identity.CgroupInode = 0
		}
		empty := phase == "exited"
		never := false
		return json.Marshal(remoteRPCResponse{SchemaVersion: 1, Receipt: remoteWireReceipt{Request: request.Request, AuthorityReceiptID: "receipt-" + attempt, JournalRevision: revisions[attempt], Phase: phase, Identity: identity}, ScopeEmpty: &empty, NeverEntered: &never, ScopeIdentity: &remoteScopeIdentity{BootID: identity.BootID, CgroupPath: profile.CgroupParent, Device: 3, Inode: 4}})
	}
	transport := &remoteBackendStartingTransport{Fake: runtime.NewFake(), started: func() { phases[prepared[len(prepared)-1].Attempt] = "entered" }}
	composed, err := composeLoadedOperatorRemoteRuntimeRouting(providerCtx, runtime.NewFake(), store, remoteOperatorProfiles{Version: 1, Profiles: []remoteOperatorProfile{profile}}, executor, func(remoteOperatorProfile) (runtime.Provider, error) { return transport, nil })
	if err != nil {
		t.Fatal(err)
	}
	wrapped := composed.(*operatorRemoteProvider)
	tp := TemplateParams{Command: "codex exec --model pinned", SessionName: "runtime-fixture", TemplateName: "worker", WorkDir: profile.City, Prompt: "literal controller fixture prompt", Hints: agent.StartupHints{Lifecycle: runtime.LifecycleOneShot}, ResolvedProvider: &config.ResolvedProvider{Name: "codex", Command: "codex", PromptMode: "arg"}}
	if err := mem.Update("sid-remote-fixture", beads.UpdateOpts{Metadata: creatingMeta(map[string]string{"pending_create_claim": "true", "continuation_epoch": "1"})}); err != nil {
		t.Fatal(err)
	}
	run := func() {
		bead, err := mem.Get("sid-remote-fixture")
		if err != nil {
			t.Fatal(err)
		}
		var output bytes.Buffer
		count := executePlannedStartsTraced(context.Background(), []startCandidate{{info: sessiontest.SeedBead(t, bead), tp: tp}}, providerCtx.cfg, map[string]TemplateParams{"runtime-fixture": tp}, wrapped, mem, "fixture", profile.City, &clock.Fake{Time: time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)}, events.Discard, 5*time.Second, io.Discard, &output, nil)
		if count != 1 {
			t.Fatalf("controller occurrence did not start: prepared=%d starts=%d %s", len(prepared), transport.CountCalls("Start", "runtime-fixture"), output.String())
		}
	}
	run()
	if len(prepared) != 1 || prepared[0].Generation != "2" {
		t.Fatalf("prepared pre-wake incarnation: count=%d", len(prepared))
	}
	phases[prepared[0].Attempt] = "exited"
	// Simulate the finite bridge's natural exit, not a controller kill by name.
	if err := transport.Stop("runtime-fixture"); err != nil {
		t.Fatal(err)
	}
	if err := mem.Update("sid-remote-fixture", beads.UpdateOpts{Metadata: map[string]string{"state": "asleep", "sleep_reason": "work-complete", "pending_create_claim": ""}}); err != nil {
		t.Fatal(err)
	}
	run()
	if len(prepared) != 2 || prepared[1].Generation != "3" || prepared[1].Attempt == prepared[0].Attempt || prepared[1].InstanceToken == prepared[0].InstanceToken {
		t.Fatal("next occurrence reused stale attempt or incarnation")
	}
	if transport.CountCalls("Start", "runtime-fixture") != 2 {
		t.Fatal("unexpected model starts across occurrences")
	}
}

func TestOperatorRemoteBackendNeverEnteredRequiresExplicitEmptyParent(t *testing.T) {
	profile, _, _, _ := remoteBackendFixture(t)
	request := session.RemoteAdmissionRequest{City: profile.City, SessionID: "sid-fixture", Template: "worker", RuntimeHandle: "runtime-fixture", Generation: "2", InstanceToken: strings.Repeat("d", 32), Attempt: strings.Repeat("a", 32), Binding: profile.Binding}
	receipt := remoteWireReceipt{Request: remoteRequestToWire(request), AuthorityReceiptID: "no-entry", JournalRevision: 2, Phase: "exited", Identity: remoteWireIdentity{BootID: strings.Repeat("b", 32), SourceSHA: profile.SourceSHA, ManifestSHA256: profile.ManifestSHA256, ModelUID: profile.ModelUID, TransportUID: profile.TransportUID, ScopeEmpty: true, NeverEntered: true, ScopeIdentity: remoteScopeIdentity{BootID: strings.Repeat("b", 32), CgroupPath: profile.CgroupParent, Device: 1, Inode: 2}}}
	if err := validateOperatorRemoteReceipt(receipt, request, profile); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*remoteWireReceipt){
		func(r *remoteWireReceipt) { r.Identity.NeverEntered = false },
		func(r *remoteWireReceipt) { r.Identity.ScopeEmpty = false },
		func(r *remoteWireReceipt) { r.Identity.ScopeIdentity.CgroupPath = "/unrelated.slice" },
		func(r *remoteWireReceipt) { r.Identity.Unit = "unexpected.service" },
	} {
		mutated := receipt
		mutate(&mutated)
		if err := validateOperatorRemoteReceipt(mutated, request, profile); err == nil {
			t.Fatal("accepted incomplete or contradictory no-entry proof")
		}
	}
}

func TestOperatorRemoteBackendPartialPreparedAndConsumedUnknownHold(t *testing.T) {
	for _, mode := range []string{"partial", "parent", "child", "empty", "consumed_unknown"} {
		t.Run(mode, func(t *testing.T) {
			profile, store, mem, providerCtx := remoteBackendFixture(t)
			revision := uint64(0)
			executor := func(_ context.Context, _ []string, input []byte) ([]byte, error) {
				var request remoteRPCRequest
				if err := strictRemoteJSON(input, &request); err != nil {
					return nil, err
				}
				revision++
				identity := remoteWireIdentity{BootID: strings.Repeat("e", 32), SourceSHA: profile.SourceSHA, ManifestSHA256: profile.ManifestSHA256, ModelUID: profile.ModelUID, TransportUID: profile.TransportUID}
				scope := remoteScopeIdentity{BootID: identity.BootID, CgroupPath: profile.CgroupParent, Device: 3, Inode: 4}
				phase := "prepared"
				empty := false
				never := false
				if request.Operation == "observe" {
					switch mode {
					case "partial":
						identity.BootID = ""
					case "parent":
						scope.Inode++
					case "child":
						identity.Unit = "unexpected.service"
					case "empty":
						empty = true
					case "consumed_unknown":
						phase = "unknown"
					}
				}
				return json.Marshal(remoteRPCResponse{SchemaVersion: 1, Receipt: remoteWireReceipt{Request: request.Request, AuthorityReceiptID: "negative-receipt", JournalRevision: revision, Phase: phase, Identity: identity}, ScopeEmpty: &empty, NeverEntered: &never, ScopeIdentity: &scope})
			}
			composed, err := composeLoadedOperatorRemoteRuntimeRouting(providerCtx, runtime.NewFake(), store, remoteOperatorProfiles{Version: 1, Profiles: []remoteOperatorProfile{profile}}, executor, nil)
			if err != nil {
				t.Fatal(err)
			}
			wrapped := composed.(*operatorRemoteProvider)
			if _, ok, reason := session.AdmitCapacityStart(context.Background(), profile.City, "worker", "sid-remote-fixture", time.Now(), wrapped, mem); !ok {
				t.Fatal(reason)
			}
			live, err := wrapped.ObserveLivenessWithError("runtime-fixture", nil)
			if err == nil || !live.Alive || !live.Running {
				t.Fatal("unknown prepared/consumed authority was treated as no model")
			}
			if !reconcileRemoteAdmissionSourceHold(context.Background(), "sid-remote-fixture", store, wrapped, io.Discard) {
				t.Fatal("controller released ambiguous root reservation")
			}
		})
	}
}

func TestOperatorRemoteBackendSnapshotPreservesPendingHint(t *testing.T) {
	rows := []session.ReconcileSession{
		{Info: session.Info{ID: "closed", Closed: true}, RemoteAdmissionPending: true},
		{Info: session.Info{ID: "local"}},
		{Info: session.Info{ID: "remote"}, RemoteAdmissionPending: true},
	}
	snapshot := newSessionBeadSnapshotFromReconcileRows(rows)
	snapshot.addInfo(session.Info{ID: "new"})
	snapshot.WriteBackReconcileInfos(map[string]session.Info{"remote": {ID: "remote", SessionNameMetadata: "updated"}})
	out := snapshot.OpenForReconcile()
	if len(out) != 3 || out[0].Info.ID != "local" || out[0].RemoteAdmissionPending || out[1].Info.ID != "remote" || !out[1].RemoteAdmissionPending || out[2].RemoteAdmissionPending {
		t.Fatal("pending hint lost row lockstep through snapshot/fold/add")
	}
}

type remoteBackendLocalReadStore struct {
	beads.Store
	localID string
	mu      sync.Mutex
	reads   int
}

func (s *remoteBackendLocalReadStore) Get(id string) (beads.Bead, error) {
	if id == s.localID {
		s.mu.Lock()
		s.reads++
		s.mu.Unlock()
	}
	return s.Store.Get(id)
}

func TestOperatorRemoteBackendMixedReconcilePreservesLocalGetBudget(t *testing.T) {
	env, local, name := newProgressStallTestEnv(t)
	env.setSessionMetadata(&local, map[string]string{"state": "awake"})
	env.sp.SetActivity(name, env.clk.Now())
	remote, err := env.store.Create(beads.Bead{Type: session.BeadType, Labels: []string{session.LabelSession}, Metadata: map[string]string{"template": "remote", "session_name": "remote-fixture", "state": "creating", "remote_admission_hold_v1": "invalid", "remote_admission_ever_v1": "v1"}})
	if err != nil {
		t.Fatal(err)
	}
	counting := &remoteBackendLocalReadStore{Store: env.store, localID: local.ID}
	cfgNames := configuredSessionNames(env.cfg, "", env.store)
	reconcileSessionBeads(context.Background(), []beads.Bead{local, remote}, env.desiredState, cfgNames, env.cfg, env.sp, counting, nil, nil, nil, env.dt, map[string]int{"worker": 1}, false, nil, "", nil, env.clk, env.rec, 0, 0, &env.stdout, &env.stderr)
	counting.mu.Lock()
	reads := counting.reads
	counting.mu.Unlock()
	if reads != 0 {
		t.Fatalf("remote hint added %d reads to local row", reads)
	}
	actual, err := env.store.Get(remote.ID)
	if err != nil || actual.Status == "closed" || actual.Metadata["remote_admission_hold_v1"] != "invalid" {
		t.Fatal("mixed tick damaged unknown remote source")
	}
}

func TestOperatorRemoteBackendPinsOptionalCityBeforeEffects(t *testing.T) {
	for _, mode := range []string{"absent", "canonical", "wrong"} {
		t.Run(mode, func(t *testing.T) {
			profile, store, mem, providerCtx := remoteBackendFixture(t)
			revision := uint64(0)
			stages := 0
			observes := 0
			var prepared remoteWireReceipt
			executor := func(_ context.Context, _ []string, input []byte) ([]byte, error) {
				var request remoteRPCRequest
				if err := strictRemoteJSON(input, &request); err != nil {
					return nil, err
				}
				revision++
				if request.Operation == "stage_prompt" {
					stages++
					if request.Environment == nil || len(*request.Environment) != 0 {
						t.Fatal("transport city reached model environment")
					}
				}
				if request.Operation == "observe" {
					observes++
				}
				scope := remoteScopeIdentity{BootID: strings.Repeat("e", 32), CgroupPath: profile.CgroupParent, Device: 3, Inode: 4}
				identity := remoteWireIdentity{ScopeIdentity: scope, BootID: scope.BootID, SourceSHA: profile.SourceSHA, ManifestSHA256: profile.ManifestSHA256, ModelUID: profile.ModelUID, TransportUID: profile.TransportUID}
				receipt := remoteWireReceipt{Request: request.Request, AuthorityReceiptID: "city-receipt", JournalRevision: revision, Phase: "prepared", Identity: identity}
				if request.Operation == "prepare" {
					prepared = receipt
				}
				empty := false
				never := false
				return json.Marshal(remoteRPCResponse{SchemaVersion: 1, Receipt: receipt, ScopeEmpty: &empty, NeverEntered: &never, ScopeIdentity: &scope})
			}
			transport := runtime.NewFake()
			composed, err := composeLoadedOperatorRemoteRuntimeRouting(providerCtx, runtime.NewFake(), store, remoteOperatorProfiles{Version: 1, Profiles: []remoteOperatorProfile{profile}}, executor, func(remoteOperatorProfile) (runtime.Provider, error) { return transport, nil })
			if err != nil {
				t.Fatal(err)
			}
			wrapped := composed.(*operatorRemoteProvider)
			if _, ok, reason := session.AdmitCapacityStart(context.Background(), profile.City, "worker", "sid-remote-fixture", time.Now(), wrapped, mem); !ok {
				t.Fatal(reason)
			}
			request, _, err := store.HeldRemoteAdmissionRequest("sid-remote-fixture")
			if err != nil {
				t.Fatal(err)
			}
			env := map[string]string{"GC_SESSION_ID": request.SessionID, "GC_TEMPLATE": request.Template, "GC_SESSION_NAME": request.RuntimeHandle, "GC_RUNTIME_EPOCH": request.Generation, "GC_INSTANCE_TOKEN": request.InstanceToken}
			switch mode {
			case "canonical":
				env["GC_CITY"] = profile.City
			case "wrong":
				env["GC_CITY"] = "/wrong/city"
			}
			registration := wrapped.RemoteAdmissionProfiles()[0]
			cfg := runtime.Config{Command: "codex exec --model pinned", PromptText: runtime.PromptTextFromLiteral("city fixture prompt"), Env: env}
			_, rewritten, beginErr := registration.Begin(context.Background(), remoteReceiptFromWire(prepared), cfg)
			direct := runtime.Config{Command: registration.Launch.Command + " " + request.Attempt, WorkDir: registration.Launch.WorkDir, Env: env, Lifecycle: runtime.LifecycleOneShot, ProcessNames: []string{"sudo"}}
			startErr := wrapped.Start(context.Background(), "runtime-fixture", direct)
			if mode == "wrong" {
				if beginErr == nil || startErr == nil || stages != 0 || observes != 0 || transport.CountCalls("Start", "runtime-fixture") != 0 {
					t.Fatalf("wrong city reached effects: stages=%d observes=%d starts=%d beginDenied=%v startDenied=%v", stages, observes, transport.CountCalls("Start", "runtime-fixture"), beginErr != nil, startErr != nil)
				}
				return
			}
			if beginErr != nil || startErr != nil || stages != 1 || observes != 1 || transport.CountCalls("Start", "runtime-fixture") != 1 {
				t.Fatalf("valid optional city rejected: begin=%v start=%v", beginErr, startErr)
			}
			value, present := rewritten.Env["GC_CITY"]
			if mode == "absent" && present || mode == "canonical" && value != profile.City {
				t.Fatal("optional canonical city changed during guarded rewrite")
			}
		})
	}
}
