package session

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/rollout/gate"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/runtime"
)

func TestRuntimeBindingImmutableAndExact(t *testing.T) {
	s := NewStore(beads.SessionStore{Store: runtimeBindingMemStore(t)})
	info, err := s.CreateSessionInfo(CreateSpec{ID: "sid-a", AgentName: "worker", Metadata: map[string]string{"template": "worker", "session_name": "runtime-a"}})
	if err != nil {
		t.Fatal(err)
	}
	b := runtime.RuntimeBinding{Selector: "ssh:worker@vm", Endpoint: "worker@vm", ResourceDomain: "vm", PolicyDigest: "v1"}
	if err = s.PinRuntimeBinding(info.ID, b); err != nil {
		t.Fatal(err)
	}
	if err = s.PinRuntimeBinding(info.ID, b); err != nil {
		t.Fatal(err)
	}
	got, ok, err := s.RuntimeBinding(info.ID)
	if err != nil || !ok || got != b {
		t.Fatalf("%+v %v %v", got, ok, err)
	}
	changed := b
	changed.Endpoint = "worker@other"
	if err = s.PinRuntimeBinding(info.ID, changed); err == nil {
		t.Fatal("binding replaced")
	}
	if err = s.PinRuntimeBinding("worker", b); err == nil {
		t.Fatal("template accepted as SID")
	}
}

func TestRuntimeBindingLookupIncludesClosedAndRejectsDuplicateNames(t *testing.T) {
	mem := runtimeBindingMemStore(t)
	s := NewStore(beads.SessionStore{Store: mem})
	info, err := s.CreateSessionInfo(CreateSpec{AgentName: "worker", Metadata: map[string]string{"template": "worker", "session_name": "runtime-a"}})
	if err != nil {
		t.Fatal(err)
	}
	b := runtime.RuntimeBinding{Selector: "ssh:worker@vm", Endpoint: "worker@vm", ResourceDomain: "vm", PolicyDigest: "v1"}
	if err = s.PinRuntimeBinding(info.ID, b); err != nil {
		t.Fatal(err)
	}
	if err = mem.Close(info.ID); err != nil {
		t.Fatal(err)
	}
	_, _, got, pinned, err := s.RuntimeRoute("runtime-a")
	if err != nil || !pinned || got != b {
		t.Fatalf("closed pin lost: %+v %v %v", got, pinned, err)
	}
	pins, err := s.RuntimeBindings()
	if err != nil || len(pins) != 1 || pins[0] != b {
		t.Fatalf("closed pin omitted: %v %v", pins, err)
	}
	_, err = s.CreateSessionInfo(CreateSpec{AgentName: "other-worker", Metadata: map[string]string{"template": "other-worker", "session_name": "runtime-a"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, _, err = s.RuntimeRoute("runtime-a"); err == nil {
		t.Fatal("duplicate name routed")
	}
}

func runtimeBindingMemStore(t *testing.T) *beads.MemStore {
	t.Helper()
	mem := beads.NewMemStore()
	_, err := beads.OpenStoreAtForCity(context.Background(), beads.StoreOpenOptions{Provider: "file", ConditionalWrites: gate.Require, OpenFileStore: func() (beads.Store, error) { return mem, nil }})
	if err != nil {
		t.Fatal(err)
	}
	return mem
}

func TestRuntimeBindingRequiresConditionalWriter(t *testing.T) {
	mem := beads.NewMemStore() // Unstamped/off mode must never gain new CAS authority.
	s := NewStore(beads.SessionStore{Store: mem})
	info, err := s.CreateSessionInfo(CreateSpec{AgentName: "worker", Metadata: map[string]string{"template": "worker", "session_name": "n"}})
	if err != nil {
		t.Fatal(err)
	}
	b := runtime.RuntimeBinding{Selector: "ssh:worker@vm", Endpoint: "worker@vm", ResourceDomain: "vm", PolicyDigest: "v1"}
	if err = s.PinRuntimeBinding(info.ID, b); err == nil {
		t.Fatal("remote binding used an unconditional write")
	}
	if _, ok, err := s.RuntimeBinding(info.ID); err != nil || ok {
		t.Fatalf("failed pin left metadata: %v %v", ok, err)
	}
}

type runtimeBindingRaceStore struct {
	*beads.MemStore
	winner runtime.RuntimeBinding
	writes int
}

func (s *runtimeBindingRaceStore) UpdateIfMatch(id string, revision int64, opts beads.UpdateOpts) error {
	s.writes++
	raw, err := json.Marshal(s.winner)
	if err != nil {
		return err
	}
	if err = s.SetMetadata(id, runtimeBindingMetadataKey, string(raw)); err != nil {
		return err
	}
	return s.MemStore.UpdateIfMatch(id, revision, opts)
}

func TestRuntimeBindingConcurrentWriterCannotReplaceWinner(t *testing.T) {
	winner := runtime.RuntimeBinding{Selector: "ssh:worker@first", Endpoint: "worker@first", ResourceDomain: "vm-first", PolicyDigest: "policy-first"}
	mem := &runtimeBindingRaceStore{MemStore: runtimeBindingMemStore(t), winner: winner}
	s := NewStore(beads.SessionStore{Store: mem})
	info, err := s.CreateSessionInfo(CreateSpec{AgentName: "worker", Metadata: map[string]string{"template": "worker", "session_name": "n"}})
	if err != nil {
		t.Fatal(err)
	}
	loser := winner
	loser.Endpoint = "worker@second"
	loser.Selector = "ssh:worker@second"
	if err = s.PinRuntimeBinding(info.ID, loser); err == nil {
		t.Fatal("concurrent binding overwrite accepted")
	}
	got, ok, err := s.RuntimeBinding(info.ID)
	if err != nil || !ok || got != winner || mem.writes != 1 {
		t.Fatalf("winner lost: %+v %v %v writes=%d", got, ok, err, mem.writes)
	}
}

// Manager.Start/StartRuntimeOnly already hold the SID mutation lock before
// admission calls the runtime binder. First pinning must not reacquire it.
func TestRuntimeBindingFirstPinInsideManagerMutationLock(t *testing.T) {
	s := NewStore(beads.SessionStore{Store: runtimeBindingMemStore(t)})
	info, err := s.CreateSessionInfo(CreateSpec{ID: "sid-nested-first-pin", AgentName: "worker", Metadata: map[string]string{"template": "worker", "session_name": "nested-first-pin", "state": "asleep", "provider": "fake", "generation": "1", "instance_token": NewInstanceToken()}})
	if err != nil {
		t.Fatal(err)
	}
	binding := runtime.RuntimeBinding{Selector: "ssh:worker@vm", Endpoint: "worker@vm", ResourceDomain: "owned-vm", PolicyDigest: "reviewed-policy"}
	provider := &runtimeBindingManagerProvider{Fake: runtime.NewFake(), store: s, binding: binding}
	manager := NewManagerWithOptions(s.store, provider, WithCityPath(t.TempDir()))
	result := make(chan error, 1)
	go func() {
		result <- manager.Start(context.Background(), info.ID, "fixture-command", runtime.Config{WorkDir: t.TempDir()})
	}()
	select {
	case err = <-result:
		// A subsequent remote-admission slice may deny the prepared route;
		// first pinning must complete before that denial, without a nested lock.
		if err != nil && !errors.Is(err, ErrCapacityAdmission) {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("first explicit runtime pin deadlocked inside Manager SID mutation lock")
	}
	got, pinned, err := s.RuntimeBinding(info.ID)
	if err != nil || !pinned || got != binding {
		t.Fatalf("pin/start mismatch: %+v %v %v", got, pinned, err)
	}
}

type runtimeBindingManagerProvider struct {
	*runtime.Fake
	store   *Store
	binding runtime.RuntimeBinding
}

func (p *runtimeBindingManagerProvider) BindRuntime(_ context.Context, id string, _ string) (runtime.RuntimeBinding, error) {
	if err := p.store.PinRuntimeBinding(id, p.binding); err != nil {
		return runtime.RuntimeBinding{}, err
	}
	return p.binding, nil
}

func (p *runtimeBindingManagerProvider) ResolveRuntime(string) (runtime.RuntimeBinding, error) {
	return p.binding, nil
}
