package beads

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	beadslib "github.com/steveyegge/beads"
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

// Real Dolt GetIssue reads row columns without hydrating labels. Keep that
// boundary behavior here so a hydrated in-memory Issue cannot mask a hold.
type blockerLabelsStorage struct {
	*nativeDoltMemStorage
	labels   []string
	labelErr error
}

func (s *blockerLabelsStorage) RunInTransaction(ctx context.Context, msg string, fn func(beadslib.Transaction) error) error {
	return s.nativeDoltMemStorage.RunInTransaction(ctx, msg, func(tx beadslib.Transaction) error {
		return fn(blockerLabelsTransaction{Transaction: tx, labels: s.labels, labelErr: s.labelErr})
	})
}

type blockerLabelsTransaction struct {
	beadslib.Transaction
	labels   []string
	labelErr error
}

func (tx blockerLabelsTransaction) GetIssue(ctx context.Context, id string) (*beadslib.Issue, error) {
	issue, err := tx.Transaction.GetIssue(ctx, id)
	if issue != nil {
		issue.Labels = nil
	}
	return issue, err
}

func (tx blockerLabelsTransaction) GetLabels(context.Context, string) ([]string, error) {
	return tx.labels, tx.labelErr
}

func TestNativeBlockerPairCASReadsTransactionalLabels(t *testing.T) {
	for _, tc := range []struct {
		name   string
		labels []string
		err    error
	}{
		{name: "mayor", labels: []string{"hold:mayor"}},
		{name: "external", labels: []string{"hold:external"}},
		{name: "unavailable", err: errors.New("labels unavailable")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			storage := &blockerLabelsStorage{nativeDoltMemStorage: newNativeDoltMemStorage(), labels: tc.labels, labelErr: tc.err}
			store := newNativeDoltStoreForTest(storage)
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
			snapshot := BlockerPairSnapshot{Revision: current.Revision, Text: "wait", Typed: `{"version":2}`, Route: "deal-executor", Owner: "deal-executor"}
			swapped, err := store.ClearBlockerPairIfMatch(created.ID, snapshot)
			if swapped || (tc.err != nil && !errors.Is(err, tc.err)) || (tc.err == nil && err != nil) {
				t.Fatalf("held or unverifiable labels CAS=(%v,%v)", swapped, err)
			}
			readback, err := store.Get(created.ID)
			if err != nil {
				t.Fatal(err)
			}
			if readback.Metadata["gc.blocked_on"] != snapshot.Text || readback.Metadata["gc.blocker.v2"] != snapshot.Typed {
				t.Fatalf("blocker changed: %#v", readback.Metadata)
			}
		})
	}
}

func TestNativeBlockerPairCASPreservesRawMetadataSiblings(t *testing.T) {
	row := &beadslib.Issue{ID: "test-raw", RowVersion: -7, Status: beadslib.StatusOpen, Metadata: json.RawMessage(`{"gc.blocked_on":"wait","gc.blocker.v2":"{}","gc.routed_to":"r","gc.next_owner":"o","count":518,"links":{"source":"tg"},"active":true,"note":null}`)}
	var before map[string]json.RawMessage
	if err := json.Unmarshal(row.Metadata, &before); err != nil {
		t.Fatal(err)
	}
	storage := &nativeDoltStorageSpy{
		getIssue: func(context.Context, string) (*beadslib.Issue, error) { return row, nil },
		updateIssue: func(_ context.Context, _ string, patch map[string]interface{}, _ string) error {
			row.Metadata = patch["metadata"].(json.RawMessage)
			row.RowVersion++
			return nil
		},
	}
	store := newNativeDoltStoreForTest(storage)
	swapped, err := store.ClearBlockerPairIfMatch(row.ID, BlockerPairSnapshot{Revision: -7, Text: "wait", Typed: "{}", Route: "r", Owner: "o"})
	if err != nil || !swapped {
		t.Fatalf("exact CAS=(%v,%v)", swapped, err)
	}
	delete(before, "gc.blocked_on")
	delete(before, "gc.blocker.v2")
	var after map[string]json.RawMessage
	if err := json.Unmarshal(row.Metadata, &after); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(after, before) {
		t.Fatalf("raw metadata changed: before=%s after=%s", before, row.Metadata)
	}
}
