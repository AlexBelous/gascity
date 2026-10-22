package beads

import (
	"strings"
	"testing"
)

// TestOff128CurrentReservationGaps records the supported store behavior that
// prevents a reservation label from being a safe execution fence.
func TestOff128CurrentReservationGaps(t *testing.T) {
	t.Run("bd adapter omits create status", func(t *testing.T) {
		var argv []string
		runner := func(_, _ string, args ...string) ([]byte, error) {
			argv = append([]string(nil), args...)
			return []byte(`{"id":"probe-1","title":"reserved","status":"open","issue_type":"task","created_at":"2026-10-22T00:00:00Z"}`), nil
		}
		store := NewBdStore(t.TempDir(), runner)
		created, err := store.Create(Bead{Title: "reserved", Status: "blocked", Labels: []string{"hold:external"}})
		if err != nil {
			t.Fatal(err)
		}
		got := strings.Join(argv, " ")
		t.Logf("requested_status=blocked argv=%q returned_status=%q", got, created.Status)
		if strings.Contains(got, "--status") || created.Status != "open" {
			t.Fatalf("negative capability no longer reproduces: argv=%q status=%q", got, created.Status)
		}
	})
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
