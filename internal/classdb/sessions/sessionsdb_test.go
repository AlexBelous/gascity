package sessionsdb

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/config"
	"github.com/gastownhall/gascity/internal/session"
)

func openTestStore(t *testing.T) *Store {
	t.Helper()
	st, err := Open(filepath.Join(t.TempDir(), "sessions.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = st.CloseStore() })
	return st
}

func TestIDPrefixMatchesReservedClassPrefix(t *testing.T) {
	want, ok := config.ReservedClassPrefix(config.BeadClassSessions)
	if !ok {
		t.Fatal("no reserved prefix registered for the sessions class")
	}
	if idPrefix != want {
		t.Fatalf("idPrefix %q drifted from reserved class prefix %q", idPrefix, want)
	}
}

func TestMintSequenceAndExplicitIDs(t *testing.T) {
	st := openTestStore(t)
	b1, err := st.Create(beads.Bead{Title: "a", Type: session.BeadType})
	if err != nil {
		t.Fatal(err)
	}
	b2, err := st.Create(beads.Bead{Title: "b", Type: session.BeadType})
	if err != nil {
		t.Fatal(err)
	}
	if b1.ID != "gcs-1" || b2.ID != "gcs-2" {
		t.Fatalf("minted ids %q, %q; want gcs-1, gcs-2", b1.ID, b2.ID)
	}
	// Explicit id honored (pool pre-allocation parity with bd).
	b3, err := st.Create(beads.Bead{ID: "gcs-explicit", Title: "c", Type: session.BeadType})
	if err != nil {
		t.Fatal(err)
	}
	if b3.ID != "gcs-explicit" {
		t.Fatalf("explicit id not honored: %q", b3.ID)
	}
	if _, err := st.Create(beads.Bead{ID: "gcs-explicit", Title: "dup", Type: session.BeadType}); err == nil {
		t.Fatal("duplicate explicit id must error")
	}
}

func TestCreateContractDefaults(t *testing.T) {
	st := openTestStore(t)
	b, err := st.Create(beads.Bead{Title: "t"})
	if err != nil {
		t.Fatal(err)
	}
	if b.Type != "task" || b.Status != "open" || b.CreatedAt.IsZero() {
		t.Fatalf("create defaults: %+v", b)
	}
	got, err := st.Get(b.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Type != "task" || got.Status != "open" || !got.CreatedAt.Equal(b.CreatedAt) {
		t.Fatalf("echo != Get: echo %+v got %+v", b, got)
	}
}

func TestCreateRejectsUnsupportedFields(t *testing.T) {
	st := openTestStore(t)
	p := 1
	cases := map[string]beads.Bead{
		"priority":  {Title: "x", Priority: &p},
		"parent":    {Title: "x", ParentID: "gc-1"},
		"needs":     {Title: "x", Needs: []string{"gc-1"}},
		"ephemeral": {Title: "x", Ephemeral: true},
		"nohistory": {Title: "x", NoHistory: true},
		"ref":       {Title: "x", Ref: "step"},
	}
	for name, b := range cases {
		if _, err := st.Create(b); !errors.Is(err, ErrUnsupported) {
			t.Errorf("%s: want ErrUnsupported, got %v", name, err)
		}
	}
}

func TestUnsupportedOpsFailLoud(t *testing.T) {
	st := openTestStore(t)
	if _, err := st.Ready(); !errors.Is(err, ErrUnsupported) {
		t.Errorf("Ready: %v", err)
	}
	if _, err := st.Children("gc-1"); !errors.Is(err, ErrUnsupported) {
		t.Errorf("Children: %v", err)
	}
	if _, err := st.ListByAssignee("a", "open", 0); !errors.Is(err, ErrUnsupported) {
		t.Errorf("ListByAssignee: %v", err)
	}
	if err := st.Tx("msg", func(beads.Tx) error { return nil }); !errors.Is(err, ErrUnsupported) {
		t.Errorf("Tx: %v", err)
	}
	if err := st.DepAdd("a", "b", "blocks"); !errors.Is(err, ErrUnsupported) {
		t.Errorf("DepAdd: %v", err)
	}
	if err := st.DepRemove("a", "b"); !errors.Is(err, ErrUnsupported) {
		t.Errorf("DepRemove: %v", err)
	}
	if _, err := st.DepList("a", "down"); !errors.Is(err, ErrUnsupported) {
		t.Errorf("DepList: %v", err)
	}
	if err := st.Update("gcs-1", beads.UpdateOpts{ParentID: strPtr("x")}); !errors.Is(err, ErrUnsupported) {
		t.Errorf("Update ParentID: %v", err)
	}
}

func strPtr(s string) *string { return &s }

func TestListScanGuard(t *testing.T) {
	st := openTestStore(t)
	if _, err := st.List(beads.ListQuery{}); !errors.Is(err, beads.ErrQueryRequiresScan) {
		t.Fatalf("zero query: want ErrQueryRequiresScan, got %v", err)
	}
	if _, err := st.List(beads.ListQuery{AllowScan: true}); err != nil {
		t.Fatalf("AllowScan: %v", err)
	}
}

func TestEmptyStringMetadataStaysPresent(t *testing.T) {
	// The observable clear contract stores "" verbatim; the key must stay
	// PRESENT in the decoded map so SetFingerprint (which hashes key
	// presence) agrees with the in-process reference stores.
	st := openTestStore(t)
	b, err := st.Create(beads.Bead{
		Title: "x", Type: session.BeadType,
		Metadata: map[string]string{"state": "awake"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetMetadataBatch(b.ID, map[string]string{"state": ""}); err != nil {
		t.Fatal(err)
	}
	got, err := st.Get(b.ID)
	if err != nil {
		t.Fatal(err)
	}
	if v, present := got.Metadata["state"]; !present || v != "" {
		t.Fatalf("empty-string write must keep the key present with %q; metadata %v", "", got.Metadata)
	}
	before := beads.Bead{ID: b.ID, Metadata: map[string]string{}}
	if session.SetFingerprint([]beads.Bead{got}) == session.SetFingerprint([]beads.Bead{before}) {
		t.Fatal("fingerprint blind to empty-string key presence")
	}
}

func TestGetSpansTablesAndDelete(t *testing.T) {
	st := openTestStore(t)
	sess, err := st.Create(beads.Bead{Title: "s", Type: session.BeadType})
	if err != nil {
		t.Fatal(err)
	}
	wait, err := st.Create(beads.Bead{
		Title: "w", Type: session.WaitBeadType,
		Labels:   []string{session.WaitBeadLabel, "session:" + sess.ID},
		Metadata: map[string]string{"session_id": sess.ID, "state": "pending"},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{sess.ID, wait.ID} {
		if _, err := st.Get(id); err != nil {
			t.Fatalf("Get %s: %v", id, err)
		}
	}
	if _, err := st.Get("gcs-999"); !errors.Is(err, beads.ErrNotFound) {
		t.Fatalf("absent id: want ErrNotFound, got %v", err)
	}
	if err := st.Delete(wait.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := st.Get(wait.ID); !errors.Is(err, beads.ErrNotFound) {
		t.Fatalf("deleted wait still readable: %v", err)
	}
	if err := st.Delete(wait.ID); !errors.Is(err, beads.ErrNotFound) {
		t.Fatalf("double delete: want ErrNotFound, got %v", err)
	}
}

func TestTypeChangeReclassifiesTable(t *testing.T) {
	// The dispatch invariant: the waits table holds exactly wait-typed rows,
	// so a Type update crossing the boundary must move the row.
	st := openTestStore(t)
	b, err := st.Create(beads.Bead{
		Title: "x", Type: session.BeadType,
		Metadata: map[string]string{"session_id": "gcs-9", "state": "pending"},
	})
	if err != nil {
		t.Fatal(err)
	}
	gate := session.WaitBeadType
	if err := st.Update(b.ID, beads.UpdateOpts{Type: &gate}); err != nil {
		t.Fatal(err)
	}
	waits, err := st.List(beads.ListQuery{Type: session.WaitBeadType})
	if err != nil || len(waits) != 1 || waits[0].ID != b.ID {
		t.Fatalf("type-narrowed list after reclassify: %v, %v", waits, err)
	}
	sessions, err := st.List(beads.ListQuery{Type: session.BeadType})
	if err != nil || len(sessions) != 0 {
		t.Fatalf("old table still lists row: %v, %v", sessions, err)
	}
}

func TestImportBeadVerbatimAndReset(t *testing.T) {
	st := openTestStore(t)
	created := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	legacy := beads.Bead{
		ID:        "gc-777",
		Title:     "legacy",
		Type:      "", // repairable empty type preserved verbatim
		Status:    "closed",
		Assignee:  "polly",
		CreatedAt: created,
		Labels:    []string{session.LabelSession},
		Metadata:  map[string]string{"state": "orphaned", "closed_at": "2026-01-03T00:00:00Z"},
	}
	inserted, err := st.ImportBead(legacy)
	if err != nil || !inserted {
		t.Fatalf("import: inserted=%v err=%v", inserted, err)
	}
	again, err := st.ImportBead(legacy)
	if err != nil || again {
		t.Fatalf("re-import must OR-IGNORE: inserted=%v err=%v", again, err)
	}
	got, err := st.Get("gc-777")
	if err != nil {
		t.Fatal(err)
	}
	if got.Type != "" || got.Status != "closed" || got.Assignee != "polly" ||
		!got.CreatedAt.Equal(created) || got.Metadata["state"] != "orphaned" {
		t.Fatalf("import not verbatim: %+v", got)
	}
	if err := st.DeleteAllRows(); err != nil {
		t.Fatalf("reset: %v", err)
	}
	if _, err := st.Get("gc-777"); !errors.Is(err, beads.ErrNotFound) {
		t.Fatalf("reset left row behind: %v", err)
	}
	// The mint never recycles across resets.
	b, err := st.Create(beads.Bead{Title: "post-reset", Type: session.BeadType})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(b.ID, "gcs-") {
		t.Fatalf("post-reset mint: %q", b.ID)
	}
}

func TestSessionNameIndexedProbeShape(t *testing.T) {
	// The adoption barrier's Live existence probe: Metadata filter +
	// Live:true must behave identically with rows in both tables.
	st := openTestStore(t)
	front := session.NewStore(beads.SessionStore{Store: st})
	if _, err := front.CreateSessionInfo(session.CreateSpec{
		Title: "a", AgentName: "a",
		Metadata: map[string]string{"session_name": "gc-target"},
	}); err != nil {
		t.Fatal(err)
	}
	ok, err := front.HasOpenSessionNamed("gc-target")
	if err != nil || !ok {
		t.Fatalf("probe: %v %v", ok, err)
	}
	ok, err = front.HasOpenSessionNamed("gc-absent")
	if err != nil || ok {
		t.Fatalf("absent probe: %v %v", ok, err)
	}
}

func TestSweepClosedBefore(t *testing.T) {
	st := openTestStore(t)
	oldClosed, err := st.Create(beads.Bead{Title: "old", Type: session.BeadType})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Close(oldClosed.ID); err != nil {
		t.Fatal(err)
	}
	open, err := st.Create(beads.Bead{Title: "open", Type: session.BeadType})
	if err != nil {
		t.Fatal(err)
	}
	oldWait, err := st.Create(beads.Bead{
		Title: "w", Type: session.WaitBeadType,
		Labels: []string{session.WaitBeadLabel},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Close(oldWait.ID); err != nil {
		t.Fatal(err)
	}
	// A cutoff in the future sweeps everything closed; open rows survive.
	deleted, err := st.SweepClosedBefore(t.Context(), time.Now().Add(time.Hour))
	if err != nil || deleted != 2 {
		t.Fatalf("sweep deleted=%d err=%v, want 2", deleted, err)
	}
	if _, err := st.Get(open.ID); err != nil {
		t.Fatalf("open row swept: %v", err)
	}
	if _, err := st.Get(oldClosed.ID); !errors.Is(err, beads.ErrNotFound) {
		t.Fatalf("closed session not swept: %v", err)
	}
	// A cutoff in the past deletes nothing.
	fresh, err := st.Create(beads.Bead{Title: "fresh", Type: session.BeadType})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Close(fresh.ID); err != nil {
		t.Fatal(err)
	}
	deleted, err = st.SweepClosedBefore(t.Context(), time.Now().Add(-time.Hour))
	if err != nil || deleted != 0 {
		t.Fatalf("past-cutoff sweep deleted=%d err=%v", deleted, err)
	}
}
