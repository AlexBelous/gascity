package beads

import (
	"reflect"
	"testing"
)

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
	title := "new revision"
	if err := store.Update(created.ID, UpdateOpts{Title: &title}); err != nil {
		t.Fatal(err)
	}
	pre, err := store.Get(created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if pre.Revision <= snapshot.Revision {
		t.Fatalf("fixture revision did not advance: before=%d after=%d", snapshot.Revision, pre.Revision)
	}
	if pre.Metadata["gc.blocked_on"] != "wait" || pre.Metadata["gc.blocker.v2"] != snapshot.Typed {
		t.Fatalf("fixture pre-state lost blocker before stale CAS: %#v", pre.Metadata)
	}
	if ok, err := store.ClearBlockerPairIfMatch(created.ID, snapshot); err != nil || ok {
		t.Fatalf("stale revision CAS = (%v, %v), want conflict", ok, err)
	}
	fresh, err := store.Get(created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(fresh.Metadata, pre.Metadata) {
		t.Fatalf("stale refusal changed metadata: before=%#v after=%#v", pre.Metadata, fresh.Metadata)
	}
	snapshot.Revision = fresh.Revision
	if err := store.Update(created.ID, UpdateOpts{Labels: []string{"hold:mayor"}}); err != nil {
		t.Fatal(err)
	}
	held, err := store.Get(created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if held.Metadata["gc.blocked_on"] != "wait" || held.Metadata["gc.blocker.v2"] != snapshot.Typed {
		t.Fatalf("fixture pre-state lost blocker before held CAS: %#v", held.Metadata)
	}
	snapshot.Revision = held.Revision
	if ok, err := store.ClearBlockerPairIfMatch(created.ID, snapshot); err != nil || ok {
		t.Fatalf("held parent CAS = (%v, %v), want conflict", ok, err)
	}
	readback, err := store.Get(created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(readback.Metadata, held.Metadata) {
		t.Fatalf("held refusal changed metadata: before=%#v after=%#v", held.Metadata, readback.Metadata)
	}
}

// Every mismatch must preserve both keys, independently of revision fencing.
func TestNativeBlockerPairCASRefusesEveryAuthorityMismatch(t *testing.T) {
	for _, name := range []string{"text", "typed", "route", "owner", "gate", "assignee", "closed", "hold:mayor", "hold:external"} {
		t.Run(name, func(t *testing.T) {
			store := newNativeDoltStoreForTest(newNativeDoltMemStorage())
			created, err := store.Create(Bead{Title: "blocked", Metadata: map[string]string{
				"gc.blocked_on": "wait", "gc.blocker.v2": `{"version":2}`,
				"gc.routed_to": "worker", "gc.next_owner": "owner", "workflow.gate_id": "gate",
			}})
			if err != nil {
				t.Fatal(err)
			}
			snapshot := BlockerPairSnapshot{Text: "wait", Typed: `{"version":2}`, Route: "worker", Owner: "owner", GateID: "gate"}
			switch name {
			case "text":
				snapshot.Text = "other"
			case "typed":
				snapshot.Typed = `{"version":3}`
			case "route":
				snapshot.Route = "other"
			case "owner":
				snapshot.Owner = "other"
			case "gate":
				snapshot.GateID = "other"
			case "assignee":
				assignee := "someone"
				if err := store.Update(created.ID, UpdateOpts{Assignee: &assignee}); err != nil {
					t.Fatal(err)
				}
			case "closed":
				if err := store.Close(created.ID); err != nil {
					t.Fatal(err)
				}
			default:
				if err := store.Update(created.ID, UpdateOpts{Labels: []string{name}}); err != nil {
					t.Fatal(err)
				}
			}
			current, err := store.Get(created.ID)
			if err != nil {
				t.Fatal(err)
			}
			snapshot.Revision = current.Revision
			if ok, err := store.ClearBlockerPairIfMatch(created.ID, snapshot); err != nil || ok {
				t.Fatalf("mismatch CAS = (%v, %v), want refusal", ok, err)
			}
			got, err := store.Get(created.ID)
			if err != nil {
				t.Fatal(err)
			}
			if got.Metadata["gc.blocked_on"] != "wait" || got.Metadata["gc.blocker.v2"] != `{"version":2}` {
				t.Fatalf("refusal changed pair: %#v", got.Metadata)
			}
		})
	}
}
