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
