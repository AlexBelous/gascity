package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/clock"
	"github.com/gastownhall/gascity/internal/events"
	"github.com/gastownhall/gascity/internal/runtime"
	sshruntime "github.com/gastownhall/gascity/internal/runtime/ssh"
	sessionpkg "github.com/gastownhall/gascity/internal/session"
	"github.com/gastownhall/gascity/internal/session/sessiontest"
)

func remoteHoldControllerFixture(t *testing.T) (*beads.MemStore, *runtime.Fake, sessionpkg.Info, startResult, *clock.Fake) {
	t.Helper()
	store := beads.NewMemStore()
	store.HonorExplicitIDs = true
	clk := &clock.Fake{Time: time.Date(2026, 10, 20, 10, 0, 0, 0, time.UTC)}
	binding := runtime.RuntimeBinding{Selector: "ssh:transport@fixture-vm", Endpoint: "transport@fixture-vm", ResourceDomain: "fixture-vm", PolicyDigest: strings.Repeat("f", 64)}
	bindingJSON, err := json.Marshal(binding)
	if err != nil {
		t.Fatal(err)
	}
	recordJSON, err := json.Marshal(map[string]any{
		"launch_command_prefix": "/usr/bin/sudo -n /usr/local/libexec/gc-entry enter", "launch_work_dir": "/fixture",
		"version": 1, "city": "/fixture", "session_id": "gc-held", "template": "worker", "runtime_handle": "worker-gc-held", "generation": "2", "attempt": strings.Repeat("a", 32), "binding": binding,
		"source_commit": strings.Repeat("b", 40), "manifest_sha256": strings.Repeat("c", 64), "unit_prefix": "gc-remote", "cgroup_parent": "/system.slice", "model_uid": 997, "transport_uid": 998, "phase": "prepare_intent",
	})
	if err != nil {
		t.Fatal(err)
	}
	b, e := store.Create(beads.Bead{ID: "gc-held", Type: sessionBeadType, Labels: []string{sessionBeadLabel}, Metadata: creatingMeta(map[string]string{
		"template": "worker", "session_name": "worker-gc-held", "session_name_explicit": "true", "generation": "2", "instance_token": strings.Repeat("e", 32), "pending_create_claim": "true", "last_woke_at": clk.Now().Add(-time.Hour).Format(time.RFC3339), "pool_slot": "1",
		"remote_admission_hold_v1": string(recordJSON), "remote_admission_ever_v1": "v1", "runtime_binding_v1": string(bindingJSON),
	})})
	if e != nil {
		t.Fatal(e)
	}
	if _, held, err := sessionFrontDoor(store).HeldRemoteAdmissionRequest(b.ID); err != nil || !held {
		t.Fatalf("fixture must be a valid exact held incarnation: %v", err)
	}
	sp := runtime.NewFake()
	if e = sp.Start(context.Background(), "worker-gc-held", runtime.Config{}); e != nil {
		t.Fatal(e)
	}
	for k, v := range map[string]string{"GC_SESSION_ID": b.ID, "GC_INSTANCE_TOKEN": strings.Repeat("e", 32), "GC_RUNTIME_EPOCH": "2"} {
		if e = sp.SetMeta("worker-gc-held", k, v); e != nil {
			t.Fatal(e)
		}
	}
	info := sessiontest.SeedBead(t, b)
	result := startResult{prepared: preparedStart{candidate: startCandidate{info: info, tp: TemplateParams{Command: "worker", SessionName: "worker-gc-held", TemplateName: "worker"}}}, outcome: "success", started: clk.Now(), finished: clk.Now(), provider: sp, rollbackPending: true}
	return store, sp, info, result, clk
}

func assertRemoteHoldControllerUnchanged(t *testing.T, store *beads.MemStore, sp *runtime.Fake, before beads.Bead) {
	t.Helper()
	after, e := store.Get(before.ID)
	if e != nil {
		t.Fatal(e)
	}
	if after.Status != before.Status || !reflect.DeepEqual(after.Metadata, before.Metadata) {
		t.Fatalf("held source changed: status=%s", after.Status)
	}
	if !sp.IsRunning(before.Metadata["session_name"]) {
		t.Fatal("controller stopped occupied remote runtime")
	}
}

func TestRemoteHoldControllerRestartCannotExpirePendingCreate(t *testing.T) {
	store, sp, info, _, clk := remoteHoldControllerFixture(t)
	before, _ := store.Get(info.ID)
	// Reconstruct front door without any in-memory admission proof/context.
	fresh := sessionFrontDoor(store)
	rollbackPendingCreate(info, fresh, clk.Now().Add(365*24*time.Hour), ioDiscard{})
	assertRemoteHoldControllerUnchanged(t, store, sp, before)
}

func TestRemoteHoldControllerCapacityFailureDoesNotClearLease(t *testing.T) {
	store, sp, info, result, clk := remoteHoldControllerFixture(t)
	before, _ := store.Get(info.ID)
	result.err = sessionpkg.CapacityAdmissionError("remote_admission_prepare_unknown")
	commitStartFailure(result, sessionFrontDoor(store), clk, events.Discard, 0, ioDiscard{}, nil)
	assertRemoteHoldControllerUnchanged(t, store, sp, before)
}

func TestRemoteHoldControllerAsyncCancellationCannotStopOrClose(t *testing.T) {
	store, sp, info, result, clk := remoteHoldControllerFixture(t)
	before, _ := store.Get(info.ID)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	commitAsyncStartResultWithContext(ctx, result, sp, store, clk, events.Discard, 0, ioDiscard{}, ioDiscard{}, nil)
	assertRemoteHoldControllerUnchanged(t, store, sp, before)
}

func TestRemoteHoldControllerAsyncDriftCannotStopOrClear(t *testing.T) {
	store, sp, info, result, clk := remoteHoldControllerFixture(t)
	before, _ := store.Get(info.ID)
	settleUncommittedAsyncStart(result, asyncStartRefreshVerdict{cleanupRuntime: true, releaseInFlight: true}, sp, sessionFrontDoor(store), clk, ioDiscard{})
	assertRemoteHoldControllerUnchanged(t, store, sp, before)
}

func TestRemoteHoldControllerMalformedStampRemainsHeld(t *testing.T) {
	store, sp, info, _, clk := remoteHoldControllerFixture(t)
	if e := store.SetMetadata(info.ID, "remote_admission_hold_v1", "malformed"); e != nil {
		t.Fatal(e)
	}
	before, _ := store.Get(info.ID)
	rollbackPendingCreate(info, sessionFrontDoor(store), clk.Now().Add(365*24*time.Hour), ioDiscard{})
	assertRemoteHoldControllerUnchanged(t, store, sp, before)
}

type nilSourceRoutedCleanupProvider struct {
	*deadRuntimeArtifactProvider
	binding    runtime.RuntimeBinding
	resolveErr error
}

func (p *nilSourceRoutedCleanupProvider) BindRuntime(context.Context, string, string) (runtime.RuntimeBinding, error) {
	panic("cleanup must not bind a runtime")
}

func (p *nilSourceRoutedCleanupProvider) ResolveRuntime(string) (runtime.RuntimeBinding, error) {
	return p.binding, p.resolveErr
}

type nilSourceAuthorityCleanupProvider struct {
	*deadRuntimeArtifactProvider
}

func (p *nilSourceAuthorityCleanupProvider) RemoteAdmissionProfiles() []sessionpkg.RemoteAdmissionProfile {
	panic("cleanup must not inspect or trust an empty registry")
}

func TestRemoteAdmissionCleanupNilSourceRequiresLegacyProvider(t *testing.T) {
	for _, mode := range []string{"native", "routed_zero", "routed_error", "routed_bound", "authority"} {
		t.Run(mode, func(t *testing.T) {
			base := newDeadRuntimeArtifactProvider()
			base.visible["worker"] = true
			base.dead["worker"] = true
			var provider runtime.Provider = base
			switch mode {
			case "routed_zero":
				provider = &nilSourceRoutedCleanupProvider{deadRuntimeArtifactProvider: base}
			case "routed_error":
				provider = &nilSourceRoutedCleanupProvider{deadRuntimeArtifactProvider: base, resolveErr: errors.New("route unavailable")}
			case "routed_bound":
				provider = &nilSourceRoutedCleanupProvider{deadRuntimeArtifactProvider: base, binding: runtime.RuntimeBinding{Selector: "ssh:transport@fixture-vm", Endpoint: "transport@fixture-vm", ResourceDomain: "fixture-vm", PolicyDigest: strings.Repeat("f", 64)}}
			case "authority":
				provider = &nilSourceAuthorityCleanupProvider{deadRuntimeArtifactProvider: base}
			}
			snapshot := newSessionBeadSnapshot([]beads.Bead{{ID: "gc-fixture", Status: "open", Metadata: map[string]string{"session_name": "worker"}}})
			got := cleanupDeadRuntimeSessionCorpses(nil, nil, nil, snapshot, nil, provider, nil, io.Discard)
			want := 0
			if mode == "native" {
				want = 1
			}
			if got != want || base.stopCalls["worker"] != want {
				t.Fatalf("cleanup=%d stops=%d want=%d", got, base.stopCalls["worker"], want)
			}
		})
	}
	if !remoteAdmissionCleanupHeld("gc-fixture", "worker", sessionFrontDoor(nil), nil, io.Discard) {
		t.Fatal("unknown provider without source was treated as legacy")
	}
}

func TestRemoteAdmissionCleanupPreservesSourceReadAndIdentityFailures(t *testing.T) {
	for _, malformed := range []bool{false, true} {
		store := beads.NewMemStore()
		store.HonorExplicitIDs = true
		if malformed {
			if _, err := store.Create(beads.Bead{ID: "gc-fixture", Type: "task", Metadata: map[string]string{"remote_admission_ever_v1": "v1"}}); err != nil {
				t.Fatal(err)
			}
		}
		provider := newDeadRuntimeArtifactProvider()
		provider.visible["worker"] = true
		provider.dead["worker"] = true
		snapshot := newSessionBeadSnapshot([]beads.Bead{{ID: "gc-fixture", Status: "open", Metadata: map[string]string{"session_name": "worker"}}})
		got := cleanupDeadRuntimeSessionCorpses(store, nil, nil, snapshot, nil, provider, nil, io.Discard)
		if got != 0 || provider.stopCalls["worker"] != 0 {
			t.Fatalf("malformed=%v cleanup=%d stops=%d", malformed, got, provider.stopCalls["worker"])
		}
	}
}

// localCleanupFake is a test-only positive local-launch attestation. Keep the
// underlying Fake separately so existing runtime call assertions remain intact.
type localCleanupFake struct {
	*runtime.Fake
	exactName string
}

func (p *localCleanupFake) SupportsCommandScope(name string) bool {
	return p.exactName == "" || name == p.exactName
}

func (p *deadRuntimeArtifactProvider) SupportsCommandScope(string) bool { return true }

func TestRemoteAdmissionCleanupRequiresPositiveExactHandleProof(t *testing.T) {
	ep, err := sshruntime.ParseEndpoint("transport@192.0.2.1")
	if err != nil {
		t.Fatal(err)
	}
	// Construct real legacy providers without invoking an SSH operation.
	for _, provider := range []runtime.Provider{nil, runtime.NewFake(), sshruntime.NewProvider(ep), sshruntime.NewSeamBacked(ep), &localCleanupFake{Fake: runtime.NewFake(), exactName: "other-handle"}} {
		if !remoteAdmissionCleanupHeld("gc-sid", "worker-handle", sessionFrontDoor(nil), provider, io.Discard) {
			t.Fatalf("provider %T without exact local proof was not held", provider)
		}
	}
	local := &localCleanupFake{Fake: runtime.NewFake(), exactName: "worker-handle"}
	if remoteAdmissionCleanupHeld("different-sid", "worker-handle", sessionFrontDoor(nil), local, io.Discard) {
		t.Fatal("exact runtime handle local proof was not used")
	}
}

// Preserve the custom startup/liveness behavior in the zombie-after-start case.
type localCleanupZombieProvider struct {
	*zombieAfterStartProvider
}

func (p *localCleanupZombieProvider) SupportsCommandScope(string) bool { return true }
