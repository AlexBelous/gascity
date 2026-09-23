//go:build integration

package beads

import (
	"context"
	"reflect"
	"testing"
)

// The pinned real backend must preserve classification across full/lite reads,
// including closed, ephemeral and no-history rows, while ordinary List still
// carries the body and dependencies required by migration.
func TestNativeClassificationProjectionAgainstIsolatedDolt(t *testing.T) {
	store := openRealNativeDoltStoreForCAS(t, "classification-projection")
	if _, ok := store.storage.(nativeClassificationQuerier); !ok {
		t.Fatal("real native backend lacks guarded classification query capability")
	}
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
	// Compare with the pinned canonical SearchIssues reader, hiding only the
	// optional optimized capability on the same isolated database.
	legacy := newNativeDoltStoreForTest(readyOutcomeLegacyStorage{store.storage})
	canonical, err := legacy.ReadClassification()
	if err != nil {
		t.Fatal(err)
	}
	legacyMap := map[string]ClassificationRow{}
	for _, row := range canonical {
		legacyMap[row.ID] = row
	}
	if !reflect.DeepEqual(got, legacyMap) {
		t.Fatalf("guarded projection differs from canonical census: got=%#v old=%#v", got, legacyMap)
	}
	raw, ok := store.storage.(testRawDBGetter)
	if !ok {
		t.Fatal("isolated schema proof needs DB accessor")
	}
	// These schema mutations affect only this test's freshly created database.
	if _, err := raw.DB().ExecContext(context.Background(), "RENAME TABLE wisp_labels TO classification_saved_labels"); err != nil {
		t.Fatal(err)
	}
	if rows, err := store.ReadClassification(); err == nil || rows != nil {
		t.Fatalf("required labels silently absent: %#v %v", rows, err)
	}
	if _, err := raw.DB().ExecContext(context.Background(), "RENAME TABLE classification_saved_labels TO wisp_labels"); err != nil {
		t.Fatal(err)
	}
	if _, err := raw.DB().ExecContext(context.Background(), "RENAME TABLE wisps TO classification_saved_wisps"); err != nil {
		t.Fatal(err)
	}
	rows, err := store.ReadClassification()
	if err != nil || len(rows) != 1 || rows[0].ID != created[0].ID {
		t.Fatalf("optional absent wisps fallback: %#v %v", rows, err)
	}
}
