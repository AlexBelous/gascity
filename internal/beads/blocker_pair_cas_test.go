package beads

import "testing"

func TestNativeBlockerPairCASRequiresExactSnapshot(t *testing.T) {
	store := newNativeDoltStoreForTest(newNativeDoltMemStorage())
	created, err := store.Create(Bead{Title: "blocked", Metadata: map[string]string{
		"gc.blocked_on": "wait", "gc.blocker.v2": `{"version":2}`,
		"gc.routed_to": "deal-executor", "gc.next_owner": "deal-executor",
		"unrelated": "keep",
	}})
	if err != nil {
		t.Fatal(err)
	}
	current, err := store.Get(created.ID)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := BlockerPairSnapshot{
		Revision: current.Revision, Text: "wait", Typed: `{"version":2}`,
		Route: "deal-executor", Owner: "deal-executor",
	}
	if ok, err := store.ClearBlockerPairIfMatch(created.ID, BlockerPairSnapshot{
		Revision: snapshot.Revision, Text: "foreign", Typed: snapshot.Typed,
		Route: snapshot.Route, Owner: snapshot.Owner,
	}); err != nil || ok {
		t.Fatalf("foreign evidence CAS = (%v, %v), want conflict", ok, err)
	}
	if ok, err := store.ClearBlockerPairIfMatch(created.ID, snapshot); err != nil || !ok {
		t.Fatalf("exact CAS = (%v, %v), want swapped", ok, err)
	}
	readback, err := store.Get(created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := readback.Metadata["gc.blocked_on"]; ok {
		t.Fatal("blocker text survived atomic release")
	}
	if _, ok := readback.Metadata["gc.blocker.v2"]; ok {
		t.Fatal("typed blocker survived atomic release")
	}
	if readback.Metadata["unrelated"] != "keep" {
		t.Fatalf("sibling metadata changed: %#v", readback.Metadata)
	}
	if ok, err := store.ClearBlockerPairIfMatch(created.ID, snapshot); err != nil || ok {
		t.Fatalf("duplicate CAS = (%v, %v), want conflict", ok, err)
	}
}

func TestNativeBlockerPairCASRejectsStaleRevisionAndHold(t *testing.T) {
	store := newNativeDoltStoreForTest(newNativeDoltMemStorage())
	created, err := store.Create(Bead{Title: "blocked", Metadata: map[string]string{
		"gc.blocked_on": "wait", "gc.blocker.v2": `{"version":2}`,
		"gc.routed_to": "deal-executor", "gc.next_owner": "deal-executor",
	}})
	if err != nil {
		t.Fatal(err)
	}
	current, err := store.Get(created.ID)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := BlockerPairSnapshot{
		Revision: current.Revision, Text: "wait", Typed: `{"version":2}`,
		Route: "deal-executor", Owner: "deal-executor",
	}
	if err := store.SetMetadata(created.ID, "unrelated", "new revision"); err != nil {
		t.Fatal(err)
	}
	if ok, err := store.ClearBlockerPairIfMatch(created.ID, snapshot); err != nil || ok {
		t.Fatalf("stale revision CAS = (%v, %v), want conflict", ok, err)
	}
	fresh, err := store.Get(created.ID)
	if err != nil {
		t.Fatal(err)
	}
	snapshot.Revision = fresh.Revision
	if err := store.Update(created.ID, UpdateOpts{Labels: []string{"hold:mayor"}}); err != nil {
		t.Fatal(err)
	}
	held, err := store.Get(created.ID)
	if err != nil {
		t.Fatal(err)
	}
	snapshot.Revision = held.Revision
	if ok, err := store.ClearBlockerPairIfMatch(created.ID, snapshot); err != nil || ok {
		t.Fatalf("held parent CAS = (%v, %v), want conflict", ok, err)
	}
	readback, err := store.Get(created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if readback.Metadata["gc.blocked_on"] != "wait" || readback.Metadata["gc.blocker.v2"] != snapshot.Typed {
		t.Fatalf("refused release changed blocker: %#v", readback.Metadata)
	}
}
