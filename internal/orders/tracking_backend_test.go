package orders

import (
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/beads/beadstest"
)

// TestDeleteRunDeletesTrackingBead proves DeleteRun issues exactly one raw
// Delete for the tracking bead. Tracking beads are standalone (CreateRun mints
// them with no deps), so the domain-level delete is a plain store delete — the
// graph-aware dep-unwind delete the cmd/gc retention prune uses stays at that
// call site as graph residual.
func TestDeleteRunDeletesTrackingBead(t *testing.T) {
	st, rec := recordingOrdersStore()
	run, err := st.CreateRun("rig/agent", RunOpts{})
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	rec.Reset()

	if err := st.DeleteRun(run.ID); err != nil {
		t.Fatalf("DeleteRun: %v", err)
	}
	calls := rec.Calls()
	if len(calls) != 1 || calls[0].Op != "Delete" || calls[0].ID != run.ID {
		t.Fatalf("ops = %+v, want exactly one Delete of %s", calls, run.ID)
	}
	if _, err := rec.Get(run.ID); err == nil {
		t.Fatalf("tracking bead %s still present after DeleteRun", run.ID)
	}

	if err := st.DeleteRun("gc-missing"); err == nil {
		t.Fatalf("DeleteRun(missing) = nil error, want the store's not-found error surfaced")
	}
}

// stubTrackingBackend is a non-beads-backed trackingBackend for the mixed-read
// composition tests: it answers the orders-leg halves with fixed values and
// implements no underlying() accessor, so the graph leg must always union.
// The embedded interface backs every method not explicitly stubbed (calling
// one panics, which is the point: only the mixed-read halves may be consulted).
type stubTrackingBackend struct {
	trackingBackend
	lastRun time.Time
	cursor  EventCursor
	open    bool
}

func (s stubTrackingBackend) LastRunTracking(string) (time.Time, error) { return s.lastRun, nil }
func (s stubTrackingBackend) CursorTracking(string) EventCursor         { return s.cursor }
func (s stubTrackingBackend) HasOpenTracking(string, func(beads.Store, beads.Bead) (bool, error)) (bool, error) {
	return s.open, nil
}

// TestGraphLegAlwaysUnionsForNonBeadsBackend pins the graph-leg dedupe
// contract: a backend that does not expose underlying() (the sqlite shape)
// can never be the same physical store as the graph leg, so the mixed reads
// must always union the graph evidence on top of the backend halves.
func TestGraphLegAlwaysUnionsForNonBeadsBackend(t *testing.T) {
	graphLeg := beads.NewMemStore()
	root, err := graphLeg.Create(beads.Bead{
		Title:  "wisp: digest",
		Type:   "molecule",
		Labels: []string{"order-run:digest", "order:digest", "seq:7"},
	})
	if err != nil {
		t.Fatal(err)
	}

	st := &Store{
		tracking: stubTrackingBackend{},
		graph:    beads.GraphStore{Store: graphLeg},
	}

	gotLast, err := st.LastRun("digest")
	if err != nil {
		t.Fatalf("LastRun(): %v", err)
	}
	if !gotLast.Equal(root.CreatedAt) {
		t.Fatalf("LastRun() = %s, want the graph wisp root's CreatedAt %s", gotLast, root.CreatedAt)
	}
	if got := st.Cursor("digest"); got != 7 {
		t.Fatalf("Cursor() = %d, want 7 from the graph wisp root seq", got)
	}
	open, err := st.HasOpenWork("digest", func(_ beads.Store, root beads.Bead) (bool, error) {
		return beads.IsMoleculeType(root.Type), nil
	})
	if err != nil {
		t.Fatalf("HasOpenWork(): %v", err)
	}
	if !open {
		t.Fatal("HasOpenWork() = false, want true (open wisp root in the graph leg)")
	}
}

// TestMixedReadsFoldBackendHalvesWithGraphLeg proves the backend halves win
// when they carry the newer evidence: the fold is a max across legs, not a
// graph-leg override.
func TestMixedReadsFoldBackendHalvesWithGraphLeg(t *testing.T) {
	graphLeg := beads.NewMemStore()
	if _, err := graphLeg.Create(beads.Bead{
		Title:  "wisp: digest",
		Labels: []string{"order-run:digest", "order:digest", "seq:3"},
	}); err != nil {
		t.Fatal(err)
	}

	newer := time.Now().Add(time.Hour)
	st := &Store{
		tracking: stubTrackingBackend{lastRun: newer, cursor: 9, open: true},
		graph:    beads.GraphStore{Store: graphLeg},
	}

	gotLast, err := st.LastRun("digest")
	if err != nil {
		t.Fatalf("LastRun(): %v", err)
	}
	if !gotLast.Equal(newer) {
		t.Fatalf("LastRun() = %s, want the backend half's newer %s", gotLast, newer)
	}
	if got := st.Cursor("digest"); got != 9 {
		t.Fatalf("Cursor() = %d, want the backend half's 9", got)
	}
	open, err := st.HasOpenWork("digest", nil)
	if err != nil {
		t.Fatalf("HasOpenWork(): %v", err)
	}
	if !open {
		t.Fatal("HasOpenWork() = false, want true (backend half short-circuits)")
	}
}

// TestBeadsTrackingUnderlyingExposesOrdersLeg pins the dedupe seam itself:
// the beads backend advertises its raw store via underlying(), and the zero
// OrdersStore advertises nil (which must never dedupe the graph leg away).
func TestBeadsTrackingUnderlyingExposesOrdersLeg(t *testing.T) {
	mem := beads.NewMemStore()
	rec := beadstest.NewRecordingStore(mem)
	backend := beadsTracking{store: beads.OrdersStore{Store: rec}}
	if got := backend.underlying(); got != beads.Store(rec) {
		t.Fatalf("underlying() = %v, want the wrapped store", got)
	}
	if got := (beadsTracking{}).underlying(); got != nil {
		t.Fatalf("zero beadsTracking underlying() = %v, want nil", got)
	}
}
