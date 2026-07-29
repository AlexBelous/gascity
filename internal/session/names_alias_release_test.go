package session

import (
	"testing"

	"github.com/gastownhall/gascity/internal/beads"
)

// TestEnsureSessionAliasAvailableReleasesDeadHolders pins the alias-release
// semantics for holders whose runtime is gone: a drained pool session
// (drain_at stamped) or a runtime-missing husk (durable sleep_reason) must
// not block a fresh create claiming the same slot alias, while a live open
// holder still does.
func TestEnsureSessionAliasAvailableReleasesDeadHolders(t *testing.T) {
	mk := func(t *testing.T, store beads.Store, alias string, extra map[string]string) beads.Bead {
		t.Helper()
		md := map[string]string{"alias": alias, "gc.kind": "session"}
		for k, v := range extra {
			md[k] = v
		}
		b, err := store.Create(beads.Bead{Title: alias, Type: "session", Labels: []string{"gc:session"}, Metadata: md})
		if err != nil {
			t.Fatal(err)
		}
		return b
	}

	t.Run("drained holder releases the alias", func(t *testing.T) {
		store := beads.NewMemStore()
		mk(t, store, "rig/gc.run-operator-1", map[string]string{"state": "drained", "drain_at": "2026-07-29T00:00:00Z"})
		if err := ensureSessionAliasAvailable(store, nil, "rig/gc.run-operator-1", "gcs-session-new", ""); err != nil {
			t.Fatalf("drained holder must release the alias, got: %v", err)
		}
	})

	t.Run("canceled-drain live holder (drain_at without state=drained) still blocks", func(t *testing.T) {
		store := beads.NewMemStore()
		mk(t, store, "rig/gc.implementation-worker-1", map[string]string{"drain_at": "2026-07-29T11:06:16Z"})
		if err := ensureSessionAliasAvailable(store, nil, "rig/gc.implementation-worker-1", "gcs-session-new", ""); err == nil {
			t.Fatal("a live session with a canceled drain (drain_at stamp only) must keep its alias — releasing it re-orphans a working session every tick")
		}
	})

	t.Run("runtime-missing holder releases the alias", func(t *testing.T) {
		store := beads.NewMemStore()
		mk(t, store, "rig/gc.run-operator-2", map[string]string{"sleep_reason": LifecycleReasonRuntimeMissing})
		if err := ensureSessionAliasAvailable(store, nil, "rig/gc.run-operator-2", "gcs-session-new", ""); err != nil {
			t.Fatalf("runtime-missing holder must release the alias, got: %v", err)
		}
	})

	t.Run("live open holder still blocks", func(t *testing.T) {
		store := beads.NewMemStore()
		mk(t, store, "rig/gc.run-operator-3", nil)
		if err := ensureSessionAliasAvailable(store, nil, "rig/gc.run-operator-3", "gcs-session-new", ""); err == nil {
			t.Fatal("live open holder must still block the alias")
		}
	})
}
