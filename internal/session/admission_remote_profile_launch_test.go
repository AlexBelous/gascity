package session

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/beads"
)

func TestRemoteAdmissionProviderRejectsConflictingLaunch(t *testing.T) {
	for _, field := range []string{"command", "workdir"} {
		t.Run(field, func(t *testing.T) {
			city, store, b, p, a := remoteAdmissionFixture(t)
			ctx := remoteAdmissionContext(t, a)
			changed := a.profile
			if field == "command" {
				changed.Launch.Command += " changed"
			} else {
				changed.Launch.WorkDir += "/changed"
			}
			wrapper := &remoteAdmissionProfileTestProvider{remoteAdmissionTestProvider: p, profiles: []RemoteAdmissionProfile{changed}}
			if _, ok, _ := AdmitCapacityStart(ctx, city, "worker", b.ID, time.Now(), wrapper, store); ok || a.prepares != 0 {
				t.Fatal("conflicting trusted launch descriptor reached Prepare")
			}
		})
	}
}

func TestRemoteHoldPersistsAndReconcilesExactLaunch(t *testing.T) {
	for _, mutation := range []string{"same", "command", "workdir"} {
		t.Run(mutation, func(t *testing.T) {
			city, store, b, p, a := remoteAdmissionFixture(t)
			if _, ok, reason := AdmitCapacityStart(remoteAdmissionContext(t, a), city, "worker", b.ID, time.Now(), p, store); !ok {
				t.Fatal(reason)
			}
			live, err := store.Get(b.ID)
			if err != nil {
				t.Fatal(err)
			}
			var raw map[string]any
			if err = json.Unmarshal([]byte(live.Metadata[remoteAdmissionHoldMetadataKey]), &raw); err != nil {
				t.Fatal(err)
			}
			if raw["launch_command_prefix"] != a.profile.Launch.Command || raw["launch_work_dir"] != a.profile.Launch.WorkDir {
				t.Error("durable profile lost exact public launch descriptor")
			}
			a.profile.Reconcile = func(_ context.Context, r RemoteAdmissionRequest) (RemoteAdmissionReceipt, error) {
				return remoteHoldReconcileReceipt(a, r), nil
			}
			switch mutation {
			case "command":
				a.profile.Launch.Command += " changed"
			case "workdir":
				a.profile.Launch.WorkDir += "/changed"
			}
			front := NewStore(beads.SessionStore{Store: store})
			err = front.ReconcileRemoteAdmissionHold(remoteAdmissionContext(t, a), b.ID)
			if mutation == "same" {
				if err != nil {
					t.Fatal(err)
				}
			} else {
				if err == nil {
					t.Fatal("changed launch descriptor released durable hold")
				}
				held, holdErr := front.RemoteAdmissionHeld(b.ID)
				if holdErr != nil || !held {
					t.Fatal("changed launch descriptor lost durable hold")
				}
			}
		})
	}
}

func TestRemoteHoldMissingLaunchDescriptorIsUnknown(t *testing.T) {
	for _, phase := range []string{"held", "terminal", "older-terminal"} {
		t.Run(phase, func(t *testing.T) {
			city, store, b, p, a := remoteAdmissionFixture(t)
			if _, ok, reason := AdmitCapacityStart(remoteAdmissionContext(t, a), city, "worker", b.ID, time.Now(), p, store); !ok {
				t.Fatal(reason)
			}
			key := remoteAdmissionHoldMetadataKey
			front := NewStore(beads.SessionStore{Store: store})
			if phase != "held" {
				a.profile.Reconcile = func(_ context.Context, r RemoteAdmissionRequest) (RemoteAdmissionReceipt, error) {
					return remoteHoldReconcileReceipt(a, r), nil
				}
				if err := front.ReconcileRemoteAdmissionHold(remoteAdmissionContext(t, a), b.ID); err != nil {
					t.Fatal(err)
				}
				key = remoteAdmissionTerminalMetadataKey
			}
			live, err := store.Get(b.ID)
			if err != nil {
				t.Fatal(err)
			}
			var raw map[string]any
			if err = json.Unmarshal([]byte(live.Metadata[key]), &raw); err != nil {
				t.Fatal(err)
			}
			delete(raw, "launch_command_prefix")
			value, err := json.Marshal(raw)
			if err != nil {
				t.Fatal(err)
			}
			if err = store.SetMetadata(b.ID, key, string(value)); err != nil {
				t.Fatal(err)
			}
			if phase == "older-terminal" {
				if err = store.SetMetadataBatch(b.ID, map[string]string{"generation": "8", "instance_token": "ffffffffffffffffffffffffffffffff"}); err != nil {
					t.Fatal(err)
				}
			}
			if request, _, err := front.HeldRemoteAdmissionRequest(b.ID); err == nil {
				t.Fatalf("missing launch descriptor accepted as known history: attempt present=%v", request.Attempt != "")
			}
		})
	}
}

func TestRemotePreparedObservationRequiresExactActiveHold(t *testing.T) {
	for _, mode := range []string{"valid", "parent", "boot", "request", "authority", "revision", "empty", "never", "child", "unknown", "malformed", "no-active", "stale-entered", "closed", "stop-intent"} {
		t.Run(mode, func(t *testing.T) {
			city, store, b, p, a := remoteAdmissionFixture(t)
			if _, ok, reason := AdmitCapacityStart(remoteAdmissionContext(t, a), city, "worker", b.ID, time.Now(), p, store); !ok {
				t.Fatal(reason)
			}
			front := NewStore(beads.SessionStore{Store: store})
			live, err := store.Get(b.ID)
			if err != nil {
				t.Fatal(err)
			}
			before := live.Metadata[remoteAdmissionHoldMetadataKey]
			receipt := a.prepared
			switch mode {
			case "closed":
				if err = store.Close(b.ID); err != nil {
					t.Fatal(err)
				}
			case "stop-intent":
				if err = store.SetMetadata(b.ID, remoteAdmissionStopMetadataKey, "pending"); err != nil {
					t.Fatal(err)
				}
			case "parent":
				receipt.Identity.ScopeIdentity.Inode++
			case "boot":
				receipt.Identity.BootID = "ffffffffffffffffffffffffffffffff"
			case "request":
				receipt.Request.InstanceToken = "ffffffffffffffffffffffffffffffff"
			case "authority":
				receipt.AuthorityReceiptID += "changed"
			case "revision":
				receipt.JournalRevision = 0
			case "empty":
				receipt.Identity.ScopeEmpty = true
			case "never":
				receipt.Identity.NeverEntered = true
			case "child":
				receipt.Identity.Unit = "unexpected.service"
			case "unknown":
				receipt.Phase = RemoteAdmissionUnknown
			case "malformed":
				if err = store.SetMetadata(b.ID, remoteAdmissionHoldMetadataKey, "invalid"); err != nil {
					t.Fatal(err)
				}
			case "no-active":
				a.profile.Reconcile = func(_ context.Context, r RemoteAdmissionRequest) (RemoteAdmissionReceipt, error) {
					return remoteHoldReconcileReceipt(a, r), nil
				}
				if err = front.ReconcileRemoteAdmissionHold(remoteAdmissionContext(t, a), b.ID); err != nil {
					t.Fatal(err)
				}
			case "stale-entered":
				var record remoteHoldRecord
				if err = json.Unmarshal([]byte(before), &record); err != nil {
					t.Fatal(err)
				}
				record.Phase = string(RemoteAdmissionEntered)
				raw, e := json.Marshal(record)
				if e != nil {
					t.Fatal(e)
				}
				if err = store.SetMetadata(b.ID, remoteAdmissionHoldMetadataKey, string(raw)); err != nil {
					t.Fatal(err)
				}
			}
			err = front.ValidatePreparedRemoteAdmissionObservation(receipt)
			if mode != "valid" {
				if err == nil {
					t.Fatal("ambiguous prepared observation treated as no model")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			held, err := front.RemoteAdmissionHeld(b.ID)
			if err != nil || !held {
				t.Fatal("prepared liveness released source hold")
			}
			live, err = store.Get(b.ID)
			if err != nil || live.Metadata[remoteAdmissionHoldMetadataKey] != before {
				t.Fatal("read-only prepared observation changed durable hold")
			}
		})
	}
}

func TestRemoteReconcileProjectionCarriesPendingHint(t *testing.T) {
	for _, tc := range []struct {
		name    string
		meta    map[string]string
		pending bool
	}{
		{"local", map[string]string{}, false},
		{"held", map[string]string{remoteAdmissionHoldMetadataKey: "invalid"}, true},
		{"stop", map[string]string{remoteAdmissionStopMetadataKey: "pending"}, true},
		{"orphan", map[string]string{remoteAdmissionEverMetadataKey: "v1"}, true},
		{"malformed-history", map[string]string{remoteAdmissionEverMetadataKey: "v1", remoteAdmissionTerminalMetadataKey: "invalid"}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := beads.NewMemStore()
			b, err := store.Create(beads.Bead{Type: BeadType, Labels: []string{LabelSession}, Metadata: tc.meta})
			if err != nil {
				t.Fatal(err)
			}
			front := NewStore(beads.SessionStore{Store: store})
			listed, err := front.ListAllForReconcile(ListAllOptions{})
			if err != nil {
				t.Fatal(err)
			}
			fingerprinted, _, err := front.ListAllForReconcileWithFingerprint(ListAllOptions{})
			if err != nil {
				t.Fatal(err)
			}
			for _, rows := range [][]ReconcileSession{ReconcileRowsFromBeads([]beads.Bead{b}), listed, fingerprinted} {
				if len(rows) != 1 || rows[0].RemoteAdmissionPending != tc.pending {
					t.Fatal("remote pending hint lost at pure reconcile projection")
				}
			}
		})
	}
	city, store, b, p, a := remoteAdmissionFixture(t)
	if _, ok, reason := AdmitCapacityStart(remoteAdmissionContext(t, a), city, "worker", b.ID, time.Now(), p, store); !ok {
		t.Fatal(reason)
	}
	a.profile.Reconcile = func(_ context.Context, r RemoteAdmissionRequest) (RemoteAdmissionReceipt, error) {
		return remoteHoldReconcileReceipt(a, r), nil
	}
	if err := NewStore(beads.SessionStore{Store: store}).ReconcileRemoteAdmissionHold(remoteAdmissionContext(t, a), b.ID); err != nil {
		t.Fatal(err)
	}
	for _, generation := range []string{"7", "8"} {
		if err := store.SetMetadata(b.ID, "generation", generation); err != nil {
			t.Fatal(err)
		}
		live, err := store.Get(b.ID)
		if err != nil {
			t.Fatal(err)
		}
		if ReconcileRowsFromBeads([]beads.Bead{live})[0].RemoteAdmissionPending {
			t.Fatal("valid terminal history marked as pending authority")
		}
	}
}
