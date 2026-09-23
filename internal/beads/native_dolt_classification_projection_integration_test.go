//go:build integration

package beads

import (
	"reflect"
	"testing"
)

// The pinned real backend must preserve classification across full/lite reads,
// including closed, ephemeral and no-history rows, while ordinary List still
// carries the body and dependencies required by migration.
func TestNativeClassificationProjectionAgainstIsolatedDolt(t *testing.T) {
	store := openRealNativeDoltStoreForCAS(t, "classification-projection")
	var created []Bead
	for _, row := range []Bead{
		{Title: "closed session", Type: "task", Description: "full closed body", Labels: []string{"gc:session"}},
		{Title: "ephemeral graph", Type: "task", Description: "full wisp body", Ephemeral: true, Metadata: map[string]string{"gc.kind": "wisp"}},
		{Title: "no-history graph", Type: "task", Description: "full no-history body", NoHistory: true, Metadata: map[string]string{"gc.root_bead_id": "root"}},
	} {
		got, err := store.Create(row)
		if err != nil {
			t.Fatalf("create isolated fixture: %v", err)
		}
		created = append(created, got)
	}
	if err := store.Close(created[0].ID); err != nil {
		t.Fatal(err)
	}
	if err := store.DepAdd(created[1].ID, created[0].ID, "blocks"); err != nil {
		t.Fatal(err)
	}
	full, err := store.List(ListQuery{IncludeClosed: true, TierMode: TierBoth, AllowScan: true})
	if err != nil {
		t.Fatal(err)
	}
	projected, err := store.ReadClassification()
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]ClassificationRow{}
	for _, row := range full {
		if row.Description == "" {
			t.Fatalf("ordinary full read lost body: %s", row.ID)
		}
		if row.ID == created[1].ID && len(row.Dependencies) == 0 {
			t.Fatal("ordinary full read lost dependencies")
		}
		want[row.ID] = ClassificationRow{ID: row.ID, Type: row.Type, Labels: row.Labels, Metadata: row.Metadata}
	}
	got := map[string]ClassificationRow{}
	for _, row := range projected {
		got[row.ID] = row
	}
	if len(want) != 3 || !reflect.DeepEqual(got, want) {
		t.Fatalf("classification parity: got=%#v want=%#v", got, want)
	}
}
