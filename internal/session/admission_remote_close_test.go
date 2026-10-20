package session

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/runtime"
)

func TestRemoteHoldCloseFencesVirginUnknownAndCAS(t *testing.T) {
	for _, fault := range []string{"virgin", "unknown", "lost-cas"} {
		t.Run(fault, func(t *testing.T) {
			city, store, b, p, a := remoteAdmissionFixture(t)
			if err := store.SetMetadata(b.ID, "state", "active"); err != nil {
				t.Fatal(err)
			}
			stops := 0
			sp := &remoteSuspendTestProvider{remoteAdmissionTestProvider: p}
			sp.onStop = func() error {
				stops++
				if fault == "unknown" {
					return errors.New("lost root cancel")
				}
				if _, ok, _ := AdmitCapacityStart(remoteAdmissionContext(t, a), city, "worker", b.ID, time.Now(), p, store); ok || a.prepares != 0 {
					t.Fatal("Close Stopnil raced a new Prepare")
				}
				return nil
			}
			var source beads.Store = store
			if fault == "lost-cas" {
				source = &remoteHoldRaceStore{MemStore: store}
			}
			_, err := NewManagerWithOptions(source, sp, WithCityPath(city)).CloseDetailed(b.ID)
			live, _ := store.Get(b.ID)
			if fault == "virgin" {
				if err != nil || live.Status != "closed" {
					t.Fatalf("virgin close failed: %v", err)
				}
			} else {
				if err == nil || live.Status == "closed" || live.Metadata["session_name"] != b.Metadata["session_name"] {
					t.Fatal("unknown/lostCAS retired source")
				}
				if fault == "unknown" && live.Metadata["remote_admission_stop_intent_v1"] == "" {
					t.Fatal("unknown close lost durable intent")
				}
				if fault == "lost-cas" && stops != 0 {
					t.Fatal("lostCAS still called Stop")
				}
			}
		})
	}
}

type remoteEarlySetupFailureStore struct {
	*beads.MemStore
	inject   func(string)
	injected bool
}

func (s *remoteEarlySetupFailureStore) SetMetadata(id, key, value string) error {
	if key == MCPServersSnapshotMetadataKey && !s.injected {
		s.injected = true
		s.inject(id)
		return errors.New("injected MCP setup failure")
	}
	return s.MemStore.SetMetadata(id, key, value)
}

func TestRemoteHoldEarlyCreateRollbackPreservesConcurrentPrepare(t *testing.T) {
	city, _, _, p, a := remoteAdmissionFixture(t)
	base := runtimeBindingMemStore(t)
	p.store = base
	source := &remoteEarlySetupFailureStore{MemStore: base}
	source.inject = func(id string) {
		if _, ok, reason := AdmitCapacityStart(remoteAdmissionContext(t, a), city, "worker", id, time.Now(), p, source); !ok {
			t.Fatal(reason)
		}
	}
	_, err := NewManagerWithOptions(source, p, WithCityPath(city)).CreateSession(context.Background(), CreateOptions{
		Template: "worker", ExplicitName: "early-held", Command: "fake-command", WorkDir: t.TempDir(), Provider: "fake",
		Hints: runtime.Config{MCPServers: []runtime.MCPServerConfig{{Name: "fixture", Transport: "stdio", Command: "fake"}}},
	})
	if err == nil || !source.injected || a.prepares != 1 || p.starts != 0 {
		t.Fatal("setup failure did not exercise one concurrent Prepare")
	}
	live, getErr := base.Get(p.lastSID)
	if getErr != nil || live.Status == "closed" || live.Metadata["session_name"] != "early-held" || live.Metadata[remoteAdmissionHoldMetadataKey] == "" {
		t.Fatal("early setup rollback retired another controller's occupied source")
	}
}

func TestRemoteHoldPruneUsesProvenConditionalClose(t *testing.T) {
	for _, fault := range []string{"virgin", "unknown", "lost-cas"} {
		t.Run(fault, func(t *testing.T) {
			city, store, b, p, a := remoteAdmissionFixture(t)
			if err := store.SetMetadataBatch(b.ID, map[string]string{"state": "suspended", "suspended_at": time.Now().Add(-48 * time.Hour).UTC().Format(time.RFC3339)}); err != nil {
				t.Fatal(err)
			}
			stops := 0
			sp := &remoteSuspendTestProvider{remoteAdmissionTestProvider: p}
			sp.onStop = func() error {
				stops++
				if fault == "unknown" {
					return errors.New("unknown root cancel")
				}
				if _, ok, _ := AdmitCapacityStart(remoteAdmissionContext(t, a), city, "worker", b.ID, time.Now(), p, store); ok || a.prepares != 0 {
					t.Fatal("Prune Stopnil raced new Prepare")
				}
				return nil
			}
			var source beads.Store = store
			if fault == "lost-cas" {
				source = &remoteHoldRaceStore{MemStore: store}
			}
			result, err := NewManagerWithOptions(source, sp, WithCityPath(city)).PruneDetailed(time.Now().Add(-24 * time.Hour))
			live, _ := store.Get(b.ID)
			if fault == "virgin" {
				if err != nil || result.Count != 1 || stops != 1 || live.Status != "closed" {
					t.Fatalf("prune bypassed proven Stop/close: count=%d stops=%d err=%v", result.Count, stops, err)
				}
			} else if err == nil || live.Status == "closed" || result.Count != 0 || live.Metadata["session_name"] != b.Metadata["session_name"] {
				t.Fatal("unknown/lostCAS prune retired a source")
			}
		})
	}
}

type remoteCloseFinalizeFailureStore struct {
	*beads.MemStore
	failed bool
}

func (s *remoteCloseFinalizeFailureStore) UpdateIfMatch(id string, rev int64, opts beads.UpdateOpts) error {
	b, err := s.Get(id)
	if err != nil {
		return err
	}
	if value, ok := opts.Metadata[remoteAdmissionStopMetadataKey]; ok && value == "" && b.Status == "closed" && !s.failed {
		s.failed = true
		return errors.New("lost metadata finalization")
	}
	return s.MemStore.UpdateIfMatch(id, rev, opts)
}

func TestRemoteHoldConditionalPartialCloseRecoversWithoutAnotherStop(t *testing.T) {
	city, store, b, p, _ := remoteAdmissionFixture(t)
	if err := store.SetMetadataBatch(b.ID, map[string]string{"state": "active", NamedSessionMetadataKey: "true", NamedSessionIdentityMetadata: "worker"}); err != nil {
		t.Fatal(err)
	}
	source := &remoteCloseFinalizeFailureStore{MemStore: store}
	stops := 0
	sp := &remoteSuspendTestProvider{remoteAdmissionTestProvider: p, onStop: func() error { stops++; return nil }}
	manager := NewManagerWithOptions(source, sp, WithCityPath(city))
	if _, err := manager.CloseDetailed(b.ID); err == nil {
		t.Fatal("partial finalization error suppressed")
	}
	partial, _ := store.Get(b.ID)
	if partial.Status != "closed" || partial.Metadata["session_name"] == "" || partial.Metadata[remoteAdmissionStopMetadataKey] == "" || stops != 1 {
		t.Fatal("partial close did not preserve recovery identity")
	}
	if _, err := manager.CloseDetailed(b.ID); err != nil {
		t.Fatalf("exact closed recovery failed: %v", err)
	}
	final, _ := store.Get(b.ID)
	if final.Status != "closed" || final.Metadata["session_name"] != "" || final.Metadata[remoteAdmissionStopMetadataKey] != "" || stops != 1 {
		t.Fatal("closed recovery restarted Stop or retained released name")
	}
}

type remoteCloseProfileProvider struct {
	*remoteSuspendTestProvider
	profiles []RemoteAdmissionProfile
}

func (p *remoteCloseProfileProvider) RemoteAdmissionProfiles() []RemoteAdmissionProfile {
	return p.profiles
}

func TestRemoteHoldCloseReconcilesOccupiedAttemptBeforeRetirement(t *testing.T) {
	for _, fault := range []string{"valid", "unknown"} {
		t.Run(fault, func(t *testing.T) {
			city, store, b, p, a := remoteAdmissionFixture(t)
			if err := store.SetMetadataBatch(b.ID, map[string]string{"state": "active", NamedSessionMetadataKey: "true", NamedSessionIdentityMetadata: "worker"}); err != nil {
				t.Fatal(err)
			}
			ctx, ok, reason := AdmitCapacityStart(remoteAdmissionContext(t, a), city, "worker", b.ID, time.Now(), p, store)
			if !ok {
				t.Fatal(reason)
			}
			if err := NewManagerWithOptions(store, p, WithCityPath(city)).startRuntime(ctx, b.Metadata["session_name"], remoteAdmissionConfig(b)); err != nil {
				t.Fatal(err)
			}
			stops, observations := 0, 0
			a.profile.Reconcile = func(_ context.Context, r RemoteAdmissionRequest) (RemoteAdmissionReceipt, error) {
				observations++
				if stops != 1 || r != a.prepared.Request {
					t.Fatal("reconcile did not follow exact occupied stop")
				}
				if fault == "unknown" {
					return RemoteAdmissionReceipt{}, errors.New("lost root observation")
				}
				return remoteHoldReconcileReceipt(a, r), nil
			}
			sp := &remoteCloseProfileProvider{remoteSuspendTestProvider: &remoteSuspendTestProvider{remoteAdmissionTestProvider: p, onStop: func() error { stops++; return nil }}, profiles: []RemoteAdmissionProfile{a.profile}}
			_, err := NewManagerWithOptions(store, sp, WithCityPath(city)).CloseDetailed(b.ID)
			live, getErr := store.Get(b.ID)
			if getErr != nil || stops != 1 || observations != 1 {
				t.Fatal("occupied close did not use one stop and trusted reconciliation")
			}
			if fault == "valid" {
				if err != nil || live.Status != "closed" || live.Metadata["session_name"] != "" || live.Metadata[remoteAdmissionHoldMetadataKey] != "" || live.Metadata[remoteAdmissionTerminalMetadataKey] == "" || live.Metadata[remoteAdmissionStopMetadataKey] != "" {
					t.Fatalf("proven occupied close failed: %v", err)
				}
			} else if err == nil || live.Status == "closed" || live.Metadata["session_name"] != b.Metadata["session_name"] || live.Metadata[remoteAdmissionHoldMetadataKey] == "" || live.Metadata[remoteAdmissionStopMetadataKey] == "" {
				t.Fatal("unknown observation retired occupied source")
			}
		})
	}
}
