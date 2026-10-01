//go:build integration

package beads

import (
	"encoding/json"
	"testing"
)

// The unit owner checks the refusal matrix. This one isolated SQL-server
// boundary proves the pinned backend reads labels transactionally and preserves
// arbitrary sibling JSON while clearing the pair atomically.
func TestNativeBlockerPairCASAgainstRealDolt(t *testing.T) {
	store := openServerNativeDoltStoreForMergeProof(t)
	created, err := store.Create(Bead{Title: "paired blocker boundary", Metadata: map[string]string{
		"gc.blocked_on": "wait", "gc.blocker.v2": "{}", "gc.routed_to": "r", "gc.next_owner": "o",
	}})
	if err != nil {
		t.Fatal(err)
	}
	storage, release, err := store.acquireStorage()
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	raw := json.RawMessage(`{"gc.blocked_on":"wait","gc.blocker.v2":"{}","gc.routed_to":"r","gc.next_owner":"o","count":518,"links":{"source":"tg"},"active":true,"note":null}`)
	if err := storage.UpdateIssue(t.Context(), created.ID, map[string]interface{}{"metadata": raw}, "boundary"); err != nil {
		t.Fatal(err)
	}
	if err := storage.AddLabel(t.Context(), created.ID, "hold:mayor", "boundary"); err != nil {
		t.Fatal(err)
	}
	snapshot := func() BlockerPairSnapshot {
		t.Helper()
		row, err := store.Get(created.ID)
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("native opaque revision: %d", row.Revision)
		return BlockerPairSnapshot{Revision: row.Revision, Text: "wait", Typed: "{}", Route: "r", Owner: "o"}
	}
	if swapped, err := store.ClearBlockerPairIfMatch(created.ID, snapshot()); err != nil || swapped {
		t.Fatalf("held SQL row CAS=(%v,%v)", swapped, err)
	}
	if err := storage.RemoveLabel(t.Context(), created.ID, "hold:mayor", "boundary"); err != nil {
		t.Fatal(err)
	}
	if swapped, err := store.ClearBlockerPairIfMatch(created.ID, snapshot()); err != nil || !swapped {
		t.Fatalf("exact SQL row CAS=(%v,%v)", swapped, err)
	}
	row, err := storage.GetIssue(t.Context(), created.ID)
	if err != nil {
		t.Fatal(err)
	}
	var metadata map[string]json.RawMessage
	if err := json.Unmarshal(row.Metadata, &metadata); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"gc.blocked_on", "gc.blocker.v2"} {
		if _, exists := metadata[key]; exists {
			t.Fatalf("%s survived pair CAS", key)
		}
	}
	for key, want := range map[string]string{"count": "518", "links": `{"source":"tg"}`, "active": "true", "note": "null"} {
		if string(metadata[key]) != want {
			t.Errorf("sibling %s=%s want %s", key, metadata[key], want)
		}
	}
}
