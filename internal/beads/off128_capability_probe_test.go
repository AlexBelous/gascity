package beads

import (
	"testing"
)

// TestOff128CurrentReservationGaps proves that a label alone is not a claim
// fence; a blocked status is required at the SQLite claim boundary.
func TestOff128CurrentReservationGaps(t *testing.T) {
	t.Run("sqlite exact ID claim ignores reservation label", func(t *testing.T) {
		opened, err := OpenSQLiteStore(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		store := opened.(*SQLiteStore)
		t.Cleanup(func() { _ = store.CloseStore() })
		created, err := store.Create(Bead{Title: "reserved", Status: "open", Labels: []string{"hold:external"}, Metadata: StringMap{"gc.reservation": "pending"}})
		if err != nil {
			t.Fatal(err)
		}
		claimed, ok, err := store.Claim(created.ID, "probe-worker")
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("created_id=%s created_status=%s claimed=%t claimed_status=%s", created.ID, created.Status, ok, claimed.Status)
		if !ok || claimed.Status != "in_progress" {
			t.Fatalf("negative capability no longer reproduces: claimed=%t status=%q", ok, claimed.Status)
		}
	})
	t.Run("sqlite blocked status refuses exact ID claim", func(t *testing.T) {
		opened, err := OpenSQLiteStore(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		store := opened.(*SQLiteStore)
		t.Cleanup(func() { _ = store.CloseStore() })
		created, err := store.Create(Bead{Title: "reserved", Status: "blocked"})
		if err != nil {
			t.Fatal(err)
		}
		_, ok, err := store.Claim(created.ID, "probe-worker")
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("created_id=%s created_status=%s claimed=%t", created.ID, created.Status, ok)
		if ok {
			t.Fatal("blocked carrier was claimed")
		}
	})
}
