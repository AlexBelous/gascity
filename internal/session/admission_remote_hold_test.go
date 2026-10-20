package session

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/beads"
)

func TestRemoteHoldStampedBeforePrepareAndSurvivesRestart(t *testing.T) {
	city, store, b, p, a := remoteAdmissionFixture(t)
	original := a.profile.Prepare
	a.profile.Prepare = func(ctx context.Context, r RemoteAdmissionRequest) (RemoteAdmissionReceipt, error) {
		live, e := store.Get(r.SessionID)
		if e != nil {
			t.Fatal(e)
		}
		raw := live.Metadata["remote_admission_hold_v1"]
		var stamp map[string]any
		if e = json.Unmarshal([]byte(raw), &stamp); e != nil {
			t.Fatalf("durable intent absent before Prepare: %v", e)
		}
		if stamp["attempt"] != r.Attempt || stamp["generation"] != r.Generation || stamp["phase"] != "prepare_intent" {
			t.Fatal("hold stamp identity does not match")
		}
		if strings.Contains(raw, r.InstanceToken) || strings.Contains(raw, "instance_token") {
			t.Fatal("hold exposed private fencing token")
		}
		_, _ = original(ctx, r)
		return RemoteAdmissionReceipt{}, errors.New("lost Prepare response")
	}
	if _, ok, _ := AdmitCapacityStart(remoteAdmissionContext(t, a), city, "worker", b.ID, time.Now(), p, store); ok {
		t.Fatal("unknown Prepare accepted")
	}
	before := a.prepares
	// New context/controller has no private proof or registry inherited from start.
	if _, ok, _ := AdmitCapacityStart(remoteAdmissionContext(t, a), city, "worker", b.ID, time.Now().Add(365*24*time.Hour), p, store); ok || a.prepares != before {
		t.Fatal("restart/TTL launched another Prepare on held source")
	}
	held, e := NewStore(beads.SessionStore{Store: store}).RemoteAdmissionHeld(b.ID)
	if e != nil || !held || !a.occupied {
		t.Fatal("restart lost durable unknown occupancy")
	}
}

func TestRemoteHoldConditionalWriterRequiredBeforePrepare(t *testing.T) {
	city, _, b, p, a := remoteAdmissionFixture(t)
	store := beads.NewMemStore()
	b.ID = ""
	b, e := store.Create(b)
	if e != nil {
		t.Fatal(e)
	}
	if _, ok, _ := AdmitCapacityStart(remoteAdmissionContext(t, a), city, "worker", b.ID, time.Now(), p, store); ok || a.prepares != 0 {
		t.Fatal("Prepare used unstamped/unconditional source store")
	}
}

type remoteHoldRaceStore struct {
	*beads.MemStore
	writes int
}

func (s *remoteHoldRaceStore) UpdateIfMatch(id string, revision int64, opts beads.UpdateOpts) error {
	s.writes++
	if e := s.SetMetadata(id, "remote_admission_hold_v1", "another-owner"); e != nil {
		return e
	}
	return s.MemStore.UpdateIfMatch(id, revision, opts)
}

func TestRemoteHoldCASCannotReplaceConcurrentOwner(t *testing.T) {
	city, store, b, p, a := remoteAdmissionFixture(t)
	race := &remoteHoldRaceStore{MemStore: store}
	if _, ok, _ := AdmitCapacityStart(remoteAdmissionContext(t, a), city, "worker", b.ID, time.Now(), p, race); ok || a.prepares != 0 {
		t.Fatal("Prepare proceeded after lost intent CAS")
	}
	live, e := store.Get(b.ID)
	if e != nil || live.Metadata["remote_admission_hold_v1"] != "another-owner" || race.writes != 1 {
		t.Fatal("concurrent source hold overwritten")
	}
}

func TestRemoteHoldMalformedStampAndClearedProofCannotLaunch(t *testing.T) {
	t.Run("malformed", func(t *testing.T) {
		city, store, b, p, a := remoteAdmissionFixture(t)
		if e := store.SetMetadata(b.ID, "remote_admission_hold_v1", "malformed"); e != nil {
			t.Fatal(e)
		}
		if _, ok, _ := AdmitCapacityStart(remoteAdmissionContext(t, a), city, "worker", b.ID, time.Now(), p, store); ok || a.prepares != 0 {
			t.Fatal("malformed hold released")
		}
	})
	t.Run("cleared-private-proof", func(t *testing.T) {
		city, store, b, p, a := remoteAdmissionFixture(t)
		ctx, ok, reason := AdmitCapacityStart(remoteAdmissionContext(t, a), city, "worker", b.ID, time.Now(), p, store)
		if !ok {
			t.Fatal(reason)
		}
		if e := store.SetMetadata(b.ID, "remote_admission_hold_v1", ""); e != nil {
			t.Fatal(e)
		}
		m := NewManagerWithOptions(store, p, WithCityPath(city))
		if e := m.startRuntime(ctx, b.Metadata["session_name"], remoteAdmissionConfig(b)); e == nil || p.starts != 0 {
			t.Fatal("private proof ignored lost durable hold")
		}
	})
}

func TestRemoteHoldTypedStoreCannotCloseOrClearIdentifiers(t *testing.T) {
	for _, action := range []string{"close", "close-without-reason", "close-terminal", "name", "alias"} {
		t.Run(action, func(t *testing.T) {
			city, store, b, p, a := remoteAdmissionFixture(t)
			if _, ok, reason := AdmitCapacityStart(remoteAdmissionContext(t, a), city, "worker", b.ID, time.Now(), p, store); !ok {
				t.Fatal(reason)
			}
			front := NewStore(beads.SessionStore{Store: store})
			before, _ := store.Get(b.ID)
			var e error
			switch action {
			case "close":
				_, e = front.Close(b.ID, "fixture", time.Now())
			case "close-without-reason":
				e = front.CloseWithoutReason(b.ID)
			case "close-terminal":
				_, e = front.CloseWithTerminalPatch(b.ID, ClosePatch(time.Now(), "fixture"), "fixture", time.Now())
			case "name":
				e = front.ApplyPatch(b.ID, MetadataPatch{"session_name": ""})
			case "alias":
				e = front.ApplyPatch(b.ID, MetadataPatch{"alias": "new-owner"})
			}
			after, _ := store.Get(b.ID)
			if e == nil || after.Status != before.Status || after.Metadata["session_name"] != before.Metadata["session_name"] {
				t.Fatal("held source retired or identifier changed")
			}
		})
	}
}

func remoteHoldReconcileReceipt(a *remoteAdmissionTestAuthority, request RemoteAdmissionRequest) RemoteAdmissionReceipt {
	r := a.prepared
	r.Request = request
	r.Phase = RemoteAdmissionExited
	r.JournalRevision = 3
	r.Identity.Unit = remoteExpectedUnit(a.profile, request)
	r.Identity.CgroupPath = a.profile.CgroupParent + "/" + r.Identity.Unit
	r.Identity.InvocationID = strings.Repeat("d", 32)
	r.Identity.CgroupDevice = 1
	r.Identity.CgroupInode = 2
	r.Identity.ScopeEmpty = true
	r.Identity.ScopeIdentity = RemoteScopeIdentity{BootID: r.Identity.BootID, CgroupPath: a.profile.CgroupParent, Device: 1, Inode: 9}
	return r
}

func TestRemoteHoldReconcileRequiresExactTerminalEmptyAuthority(t *testing.T) {
	for _, fault := range []string{"valid", "nil", "error", "unknown", "not-empty", "attempt", "token", "binding", "source", "revision", "unit", "cgroup"} {
		t.Run(fault, func(t *testing.T) {
			city, store, b, p, a := remoteAdmissionFixture(t)
			if fault != "nil" {
				a.profile.Reconcile = func(_ context.Context, request RemoteAdmissionRequest) (RemoteAdmissionReceipt, error) {
					r := remoteHoldReconcileReceipt(a, request)
					switch fault {
					case "error":
						return RemoteAdmissionReceipt{}, errors.New("lost observe reply")
					case "unknown":
						r.Phase = RemoteAdmissionUnknown
					case "not-empty":
						r.Identity.ScopeEmpty = false
					case "attempt":
						r.Request.Attempt = strings.Repeat("0", 32)
					case "token":
						r.Request.InstanceToken = strings.Repeat("0", 32)
					case "binding":
						r.Request.Binding.PolicyDigest = strings.Repeat("0", 64)
					case "source":
						r.Identity.SourceSHA = strings.Repeat("0", 40)
					case "revision":
						r.JournalRevision = 1
					case "unit":
						r.Identity.Unit = "other.service"
					case "cgroup":
						r.Identity.CgroupPath = "/other.slice/" + r.Identity.Unit
					}
					return r, nil
				}
			}
			if _, ok, reason := AdmitCapacityStart(remoteAdmissionContext(t, a), city, "worker", b.ID, time.Now(), p, store); !ok {
				t.Fatal(reason)
			}
			front := NewStore(beads.SessionStore{Store: store})
			request, held, e := front.HeldRemoteAdmissionRequest(b.ID)
			if e != nil || !held || request != a.prepared.Request {
				t.Fatal("cannot recover exact attempt after controller restart")
			}
			e = front.ReconcileRemoteAdmissionHold(remoteAdmissionContext(t, a), b.ID)
			live, _ := store.Get(b.ID)
			if fault == "valid" {
				if e != nil || live.Metadata["remote_admission_hold_v1"] != "" || live.Metadata["remote_admission_terminal_v1"] == "" {
					t.Fatalf("exact authoritative terminal empty not released: %v", e)
				}
				terminal, stillHeld, readErr := front.HeldRemoteAdmissionRequest(b.ID)
				if readErr != nil || stillHeld || terminal != request {
					t.Fatal("terminal attempt identity lost after release")
				}
				if strings.Contains(live.Metadata["remote_admission_terminal_v1"], request.InstanceToken) {
					t.Fatal("terminal receipt exposed fencing token")
				}
			} else if e == nil || live.Metadata["remote_admission_hold_v1"] == "" {
				t.Fatal("unknown or mismatched authority released hold")
			}
		})
	}
}

func TestRemoteHoldEverMarkerDetectsLostAttempt(t *testing.T) {
	city, store, b, p, a := remoteAdmissionFixture(t)
	if _, ok, reason := AdmitCapacityStart(remoteAdmissionContext(t, a), city, "worker", b.ID, time.Now(), p, store); !ok {
		t.Fatal(reason)
	}
	if e := store.SetMetadataBatch(b.ID, map[string]string{"remote_admission_hold_v1": "", "remote_admission_terminal_v1": ""}); e != nil {
		t.Fatal(e)
	}
	live, _ := store.Get(b.ID)
	if live.Metadata["remote_admission_ever_v1"] != "v1" {
		t.Fatal("ever marker absent")
	}
	front := NewStore(beads.SessionStore{Store: store})
	if _, _, e := front.HeldRemoteAdmissionRequest(b.ID); e == nil {
		t.Fatal("lost attempt was treated as virgin")
	}
	before := a.prepares
	if _, ok, _ := AdmitCapacityStart(remoteAdmissionContext(t, a), city, "worker", b.ID, time.Now(), p, store); ok || a.prepares != before {
		t.Fatal("lost attempt re-prepared")
	}
}

func TestRemoteHoldNeverEnteredRequiresRootRevocationAndParentProof(t *testing.T) {
	for _, fault := range []string{"valid", "not-empty", "parent", "inode", "boot", "child", "consumed"} {
		t.Run(fault, func(t *testing.T) {
			city, store, b, p, a := remoteAdmissionFixture(t)
			a.profile.Reconcile = func(_ context.Context, r RemoteAdmissionRequest) (RemoteAdmissionReceipt, error) {
				receipt := remoteHoldReconcileReceipt(a, r)
				receipt.Identity.NeverEntered = true
				receipt.Identity.Unit = ""
				receipt.Identity.InvocationID = ""
				receipt.Identity.CgroupPath = ""
				receipt.Identity.CgroupDevice = 0
				receipt.Identity.CgroupInode = 0
				switch fault {
				case "not-empty":
					receipt.Identity.ScopeEmpty = false
				case "parent":
					receipt.Identity.ScopeIdentity.CgroupPath = "/other.slice"
				case "inode":
					receipt.Identity.ScopeIdentity.Inode = 0
				case "boot":
					receipt.Identity.ScopeIdentity.BootID = strings.Repeat("0", 32)
				case "child":
					receipt.Identity.InvocationID = strings.Repeat("f", 32)
				}
				return receipt, nil
			}
			ctx, ok, reason := AdmitCapacityStart(remoteAdmissionContext(t, a), city, "worker", b.ID, time.Now(), p, store)
			if !ok {
				t.Fatal(reason)
			}
			if fault == "consumed" {
				m := NewManagerWithOptions(store, p, WithCityPath(city))
				if e := m.startRuntime(ctx, b.Metadata["session_name"], remoteAdmissionConfig(b)); e != nil {
					t.Fatal(e)
				}
			}
			e := NewStore(beads.SessionStore{Store: store}).ReconcileRemoteAdmissionHold(remoteAdmissionContext(t, a), b.ID)
			live, _ := store.Get(b.ID)
			if fault == "valid" {
				if e != nil || live.Metadata["remote_admission_hold_v1"] != "" {
					t.Fatalf("root proved never-entered empty not reconciled: %v", e)
				}
			} else if e == nil || live.Metadata["remote_admission_hold_v1"] == "" {
				t.Fatal("unknown/consumed/misbound no-entry released")
			}
		})
	}
}

func TestRemoteHoldTerminalHistoryDoesNotStrandNextGeneration(t *testing.T) {
	city, store, b, p, a := remoteAdmissionFixture(t)
	a.profile.Reconcile = func(_ context.Context, r RemoteAdmissionRequest) (RemoteAdmissionReceipt, error) {
		a.occupied = false
		return remoteHoldReconcileReceipt(a, r), nil
	}
	if _, ok, reason := AdmitCapacityStart(remoteAdmissionContext(t, a), city, "worker", b.ID, time.Now(), p, store); !ok {
		t.Fatal(reason)
	}
	old := a.prepared.Request
	front := NewStore(beads.SessionStore{Store: store})
	if e := front.ReconcileRemoteAdmissionHold(remoteAdmissionContext(t, a), b.ID); e != nil {
		t.Fatal(e)
	}
	if e := store.SetMetadataBatch(b.ID, map[string]string{"generation": "8", "instance_token": strings.Repeat("9", 32)}); e != nil {
		t.Fatal(e)
	}
	if r, held, e := front.HeldRemoteAdmissionRequest(b.ID); e != nil || held || r.Attempt != "" {
		t.Fatal("old terminal attempt was rebuilt with a new token")
	}
	if _, ok, reason := AdmitCapacityStart(remoteAdmissionContext(t, a), city, "worker", b.ID, time.Now(), p, store); !ok {
		t.Fatalf("next occurrence stranded: %s", reason)
	}
	if a.prepared.Request.Generation != "8" || a.prepared.Request.Attempt == old.Attempt {
		t.Fatal("old occupied attempt overwritten/reused")
	}
	live, _ := store.Get(b.ID)
	if live.Metadata["remote_admission_terminal_v1"] != "" || live.Metadata["remote_admission_ever_v1"] != "v1" {
		t.Fatal("next intent did not atomically replace terminal history")
	}
}

func TestRemoteHoldReviewIdentityPatchCAS(t *testing.T) {
	_, store, b, p, _ := remoteAdmissionFixture(t)
	if err := NewStore(beads.SessionStore{Store: store}).PinRuntimeBinding(b.ID, p.binding); err != nil {
		t.Fatal(err)
	}
	race := &remoteHoldRaceStore{MemStore: store}
	if err := NewStore(beads.SessionStore{Store: race}).ApplyPatch(b.ID, MetadataPatch{"session_name": ""}); err == nil {
		t.Fatal("unconditional identity patch accepted")
	}
	after, _ := store.Get(b.ID)
	if after.Metadata["session_name"] != b.Metadata["session_name"] || after.Metadata[remoteAdmissionHoldMetadataKey] != "another-owner" {
		t.Fatal("concurrent reservation was erased")
	}
}

func TestRemoteHoldReviewLifecyclePatchCannotBypassHold(t *testing.T) {
	city, store, b, p, a := remoteAdmissionFixture(t)
	if _, ok, r := AdmitCapacityStart(remoteAdmissionContext(t, a), city, "worker", b.ID, time.Now(), p, store); !ok {
		t.Fatal(r)
	}
	front := NewStore(beads.SessionStore{Store: store})
	info, err := front.Get(b.ID)
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := front.ApplyPatchIfLifecycleUnchanged(info, MetadataPatch{"session_name": ""}); ok || err == nil {
		t.Fatal("conditional API bypassed hold")
	}
}

func TestRemoteHoldReviewTerminalCannotReprepareSameIncarnation(t *testing.T) {
	city, store, b, p, a := remoteAdmissionFixture(t)
	a.profile.Reconcile = func(_ context.Context, r RemoteAdmissionRequest) (RemoteAdmissionReceipt, error) {
		a.occupied = false
		return remoteHoldReconcileReceipt(a, r), nil
	}
	if _, ok, r := AdmitCapacityStart(remoteAdmissionContext(t, a), city, "worker", b.ID, time.Now(), p, store); !ok {
		t.Fatal(r)
	}
	if err := NewStore(beads.SessionStore{Store: store}).ReconcileRemoteAdmissionHold(remoteAdmissionContext(t, a), b.ID); err != nil {
		t.Fatal(err)
	}
	before := a.prepares
	if _, ok, _ := AdmitCapacityStart(remoteAdmissionContext(t, a), city, "worker", b.ID, time.Now(), p, store); ok || a.prepares != before {
		t.Fatal("terminal incarnation was consumed twice")
	}
}

func TestRemoteHoldReviewHistoricalIdentityCannotChange(t *testing.T) {
	for _, fault := range []string{"parent-inode", "parent-device", "child-invocation", "child-device", "child-inode"} {
		t.Run(fault, func(t *testing.T) {
			city, store, b, p, a := remoteAdmissionFixture(t)
			a.profile.Reconcile = func(_ context.Context, r RemoteAdmissionRequest) (RemoteAdmissionReceipt, error) {
				receipt := remoteHoldReconcileReceipt(a, r)
				switch fault {
				case "parent-inode":
					receipt.Identity.ScopeIdentity.Inode++
				case "parent-device":
					receipt.Identity.ScopeIdentity.Device++
				case "child-invocation":
					receipt.Identity.InvocationID = strings.Repeat("8", 32)
				case "child-device":
					receipt.Identity.CgroupDevice++
				case "child-inode":
					receipt.Identity.CgroupInode++
				}
				return receipt, nil
			}
			ctx, ok, r := AdmitCapacityStart(remoteAdmissionContext(t, a), city, "worker", b.ID, time.Now(), p, store)
			if !ok {
				t.Fatal(r)
			}
			if err := NewManagerWithOptions(store, p, WithCityPath(city)).startRuntime(ctx, b.Metadata["session_name"], remoteAdmissionConfig(b)); err != nil {
				t.Fatal(err)
			}
			if err := NewStore(beads.SessionStore{Store: store}).ReconcileRemoteAdmissionHold(remoteAdmissionContext(t, a), b.ID); err == nil {
				t.Fatal("different historical scope released hold")
			}
		})
	}
}

func TestRemoteHoldReviewPrepareRequiresParentScope(t *testing.T) {
	city, store, b, p, a := remoteAdmissionFixture(t)
	old := a.profile.Prepare
	a.profile.Prepare = func(ctx context.Context, r RemoteAdmissionRequest) (RemoteAdmissionReceipt, error) {
		receipt, err := old(ctx, r)
		receipt.Identity.ScopeIdentity = RemoteScopeIdentity{}
		return receipt, err
	}
	if _, ok, _ := AdmitCapacityStart(remoteAdmissionContext(t, a), city, "worker", b.ID, time.Now(), p, store); ok {
		t.Fatal("missing initial parent proof accepted")
	}
}

type remoteSuspendTestProvider struct {
	*remoteAdmissionTestProvider
	onStop func() error
}

func (p *remoteSuspendTestProvider) Stop(string) error { return p.onStop() }
func TestRemoteHoldSuspendFencesVirginAdmissionBeforeStop(t *testing.T) {
	city, store, b, p, a := remoteAdmissionFixture(t)
	if err := store.SetMetadata(b.ID, "state", "active"); err != nil {
		t.Fatal(err)
	}
	sp := &remoteSuspendTestProvider{remoteAdmissionTestProvider: p}
	sp.onStop = func() error {
		if _, ok, _ := AdmitCapacityStart(remoteAdmissionContext(t, a), city, "worker", b.ID, time.Now(), p, store); ok || a.prepares != 0 {
			t.Fatal("new remote Prepare landed after Stop read virgin source")
		}
		return nil
	}
	if err := NewManagerWithOptions(store, sp, WithCityPath(city)).Suspend(b.ID); err != nil {
		t.Fatal(err)
	}
	live, _ := store.Get(b.ID)
	if live.Metadata["state"] != "suspended" || live.Metadata["sleep_intent"] != "user-hold" {
		t.Fatal("operator suspension not persisted")
	}
}

func TestRemoteHoldSuspendUnknownStopRetainsIntent(t *testing.T) {
	city, store, b, p, a := remoteAdmissionFixture(t)
	if err := store.SetMetadata(b.ID, "state", "active"); err != nil {
		t.Fatal(err)
	}
	sp := &remoteSuspendTestProvider{remoteAdmissionTestProvider: p, onStop: func() error { return errors.New("lost root cancel response") }}
	if err := NewManagerWithOptions(store, sp, WithCityPath(city)).Suspend(b.ID); err == nil {
		t.Fatal("unknown remote Stop reported success")
	}
	live, _ := store.Get(b.ID)
	if live.Metadata["remote_admission_stop_intent_v1"] == "" || live.Metadata["state"] == "suspended" {
		t.Fatal("unknown Stop erased intent or claimed terminal state")
	}
	if _, ok, _ := AdmitCapacityStart(remoteAdmissionContext(t, a), city, "worker", b.ID, time.Now(), p, store); ok || a.prepares != 0 {
		t.Fatal("unknown Stop allowed new Prepare")
	}
	if held, err := NewStore(beads.SessionStore{Store: store}).RemoteAdmissionHeld(b.ID); err != nil || !held {
		t.Fatal("controller lost durable stop barrier")
	}
}

func TestRemoteHoldSuspendLosingIntentCASNeverStops(t *testing.T) {
	city, store, b, p, _ := remoteAdmissionFixture(t)
	if err := store.SetMetadata(b.ID, "state", "active"); err != nil {
		t.Fatal(err)
	}
	race := &remoteHoldRaceStore{MemStore: store}
	stops := 0
	sp := &remoteSuspendTestProvider{remoteAdmissionTestProvider: p, onStop: func() error { stops++; return nil }}
	if err := NewManagerWithOptions(race, sp, WithCityPath(city)).Suspend(b.ID); err == nil || stops != 0 {
		t.Fatal("lost stop-intent CAS still called provider Stop")
	}
}

func TestRemoteHoldStopCompletionCannotOverwriteLifecycleChange(t *testing.T) {
	city, store, b, p, _ := remoteAdmissionFixture(t)
	if err := store.SetMetadata(b.ID, "state", "active"); err != nil {
		t.Fatal(err)
	}
	sp := &remoteSuspendTestProvider{remoteAdmissionTestProvider: p, onStop: func() error { return store.SetMetadata(b.ID, "state", "asleep") }}
	if err := NewManagerWithOptions(store, sp, WithCityPath(city)).Suspend(b.ID); err == nil {
		t.Fatal("completion overwrote a later lifecycle decision")
	}
	live, _ := store.Get(b.ID)
	if live.Metadata["state"] != "asleep" || live.Metadata["remote_admission_stop_intent_v1"] == "" {
		t.Fatal("later state or durable intent lost")
	}
}
