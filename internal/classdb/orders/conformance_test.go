package ordersdb

// Both-backend conformance: every behavioral contract of the orders front
// door (internal/orders.Store) must hold identically over the beads backend
// (a MemStore-backed beads.OrdersStore) and this embedded-SQLite backend.
// Cases exercise ONLY the public Store surface — no ListQuery flags, no
// recorded call sequences — so the suite is the portable, backend-agnostic
// half of the orders store tests (the bd-shaped query/tier assertions stay in
// internal/orders).

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/orders"
)

// eachBackend runs fn against a fresh front door per backend.
func eachBackend(t *testing.T, fn func(t *testing.T, front *orders.Store)) {
	t.Helper()
	t.Run("beads", func(t *testing.T) {
		fn(t, orders.NewStore(beads.OrdersStore{Store: beads.NewMemStore()}))
	})
	t.Run("sqlite", func(t *testing.T) {
		st, err := Open(filepath.Join(t.TempDir(), "orders.db"))
		if err != nil {
			t.Fatalf("open sqlite orders store: %v", err)
		}
		t.Cleanup(func() {
			if err := st.Close(); err != nil {
				t.Errorf("close sqlite orders store: %v", err)
			}
		})
		fn(t, orders.NewStoreWithTracking(st, beads.GraphStore{}))
	})
}

func TestConformanceCreateRunLifecycle(t *testing.T) {
	eachBackend(t, func(t *testing.T, front *orders.Store) {
		run, err := front.CreateRun("rig/agent", orders.RunOpts{})
		if err != nil {
			t.Fatalf("CreateRun: %v", err)
		}
		if run.ID == "" || !run.Open || run.Scoped != "rig/agent" || run.CreatedAt.IsZero() {
			t.Fatalf("CreateRun = %+v, want open run with id, scoped, cooldown clock", run)
		}
		if run.Outcome != orders.RunOutcomeNone {
			t.Fatalf("CreateRun outcome = %v, want None (in-flight)", run.Outcome)
		}

		open, err := front.OpenRuns()
		if err != nil {
			t.Fatalf("OpenRuns: %v", err)
		}
		if len(open) != 1 || open[0].ID != run.ID {
			t.Fatalf("OpenRuns = %+v, want the created run", open)
		}

		latest, found, err := front.LatestOpenRun("rig/agent")
		if err != nil || !found || latest.ID != run.ID {
			t.Fatalf("LatestOpenRun = (%+v, %v, %v), want the created run", latest, found, err)
		}

		got, err := front.Get(run.ID)
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if got.ID != run.ID || got.Scoped != "rig/agent" || !got.Open {
			t.Fatalf("Get = %+v, want the open created run", got)
		}
		if !got.CreatedAt.Equal(run.CreatedAt) {
			t.Fatalf("Get CreatedAt = %s, want %s (cooldown clock must round-trip)", got.CreatedAt, run.CreatedAt)
		}
	})
}

func TestConformanceSetOutcomeRoundTripsEveryOutcome(t *testing.T) {
	outcomes := []orders.RunOutcome{
		orders.RunOutcomeExec,
		orders.RunOutcomeExecFailed,
		orders.RunOutcomeExecEnvFailed,
		orders.RunOutcomeWisp,
		orders.RunOutcomeWispFailed,
		orders.RunOutcomeWispCanceled,
		orders.RunOutcomeTriggerEnvFailed,
	}
	eachBackend(t, func(t *testing.T, front *orders.Store) {
		for _, outcome := range outcomes {
			run, err := front.CreateRun("rig/agent", orders.RunOpts{})
			if err != nil {
				t.Fatalf("CreateRun: %v", err)
			}
			if err := front.SetOutcome(run.ID, outcome); err != nil {
				t.Fatalf("SetOutcome(%v): %v", outcome, err)
			}
			got, err := front.Get(run.ID)
			if err != nil {
				t.Fatalf("Get: %v", err)
			}
			if got.Outcome != outcome {
				t.Fatalf("outcome round-trip = %v, want %v", got.Outcome, outcome)
			}
		}
	})
}

func TestConformanceCursorIsMonotonicHighWater(t *testing.T) {
	eachBackend(t, func(t *testing.T, front *orders.Store) {
		run, err := front.CreateRun("digest", orders.RunOpts{})
		if err != nil {
			t.Fatalf("CreateRun: %v", err)
		}
		if err := front.SetCursor(run.ID, "digest", 9); err != nil {
			t.Fatalf("SetCursor(9): %v", err)
		}
		if got := front.Cursor("digest"); got != 9 {
			t.Fatalf("Cursor = %d, want 9", got)
		}
		// A late lower stamp must never regress the high-water mark: the beads
		// backend accumulates seq labels and decodes the max; the sqlite
		// backend keeps MAX(seq, new).
		if err := front.SetCursor(run.ID, "digest", 5); err != nil {
			t.Fatalf("SetCursor(5): %v", err)
		}
		if got := front.Cursor("digest"); got != 9 {
			t.Fatalf("Cursor after lower stamp = %d, want 9 (monotonic)", got)
		}
	})
}

func TestConformanceCloseRunLifecycle(t *testing.T) {
	eachBackend(t, func(t *testing.T, front *orders.Store) {
		run, err := front.CreateRun("rig/agent", orders.RunOpts{})
		if err != nil {
			t.Fatalf("CreateRun: %v", err)
		}
		if err := front.CloseRun(run.ID, "conformance close reason padding"); err != nil {
			t.Fatalf("CloseRun: %v", err)
		}

		open, err := front.OpenRuns()
		if err != nil {
			t.Fatalf("OpenRuns: %v", err)
		}
		if len(open) != 0 {
			t.Fatalf("OpenRuns after close = %+v, want none", open)
		}
		if _, found, err := front.LatestOpenRun("rig/agent"); err != nil || found {
			t.Fatalf("LatestOpenRun after close = (found=%v, err=%v), want (false, nil)", found, err)
		}

		closed, err := front.ClosedRunsForRetention()
		if err != nil {
			t.Fatalf("ClosedRunsForRetention: %v", err)
		}
		if len(closed) != 1 || closed[0].ID != run.ID || closed[0].Open {
			t.Fatalf("ClosedRunsForRetention = %+v, want the closed run", closed)
		}

		recent, err := front.RecentRuns("rig/agent", 10)
		if err != nil {
			t.Fatalf("RecentRuns: %v", err)
		}
		if len(recent) != 1 || recent[0].Open {
			t.Fatalf("RecentRuns after close = %+v, want the closed run visible", recent)
		}

		// Closing an already-closed run is a no-op, not an error.
		if err := front.CloseRun(run.ID, "second close is a no-op reason"); err != nil {
			t.Fatalf("CloseRun(closed): %v", err)
		}
	})
}

func TestConformanceCreateRunClosedAdvancesCooldownOnly(t *testing.T) {
	eachBackend(t, func(t *testing.T, front *orders.Store) {
		cursor := orders.EventCursor(4)
		run, err := front.CreateRunClosed("digest", orders.RunOutcomeExec, &cursor, "manual run close reason padding")
		if err != nil {
			t.Fatalf("CreateRunClosed: %v", err)
		}
		if run.Open {
			t.Fatal("CreateRunClosed run.Open = true, want false")
		}
		if run.Outcome != orders.RunOutcomeExec || run.Cursor != cursor {
			t.Fatalf("CreateRunClosed = %+v, want exec outcome + cursor 4", run)
		}

		// The cooldown clock advances even though no open marker lingers.
		last, err := front.LastRun("digest")
		if err != nil {
			t.Fatalf("LastRun: %v", err)
		}
		if !last.Equal(run.CreatedAt) {
			t.Fatalf("LastRun = %s, want %s", last, run.CreatedAt)
		}
		if got := front.Cursor("digest"); got != cursor {
			t.Fatalf("Cursor = %d, want %d", got, cursor)
		}
		if open, err := front.OpenRuns(); err != nil || len(open) != 0 {
			t.Fatalf("OpenRuns = (%+v, %v), want none (no in-flight marker)", open, err)
		}
		got, err := front.Get(run.ID)
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if got.Open || got.Outcome != orders.RunOutcomeExec || got.Cursor != cursor {
			t.Fatalf("Get = %+v, want closed exec run with cursor", got)
		}
	})
}

func TestConformanceMarkFailedStampsOutcomeAndCursor(t *testing.T) {
	eachBackend(t, func(t *testing.T, front *orders.Store) {
		run, err := front.CreateRun("digest", orders.RunOpts{})
		if err != nil {
			t.Fatalf("CreateRun: %v", err)
		}
		cursor := orders.EventCursor(11)
		if err := front.MarkFailed(run.ID, "digest", orders.RunOutcomeWispFailed, &cursor); err != nil {
			t.Fatalf("MarkFailed: %v", err)
		}
		got, err := front.Get(run.ID)
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if got.Outcome != orders.RunOutcomeWispFailed || got.Cursor != cursor {
			t.Fatalf("Get after MarkFailed = %+v, want wisp-failed + cursor 11", got)
		}
		if got.State() != "failed" {
			t.Fatalf("State = %q, want failed", got.State())
		}
	})
}

func TestConformanceCloseRunsBatch(t *testing.T) {
	eachBackend(t, func(t *testing.T, front *orders.Store) {
		var ids []string
		for _, name := range []string{"a", "b"} {
			run, err := front.CreateRun(name, orders.RunOpts{})
			if err != nil {
				t.Fatalf("CreateRun(%s): %v", name, err)
			}
			ids = append(ids, run.ID)
		}
		// Duplicates and blanks are hygiene the batch must absorb.
		batch := append([]string{"", ids[0]}, ids...)
		n, err := front.CloseRuns(context.Background(), batch, "batch close conformance reason")
		if err != nil {
			t.Fatalf("CloseRuns: %v", err)
		}
		if n != 2 {
			t.Fatalf("CloseRuns closed %d, want 2", n)
		}
		for _, id := range ids {
			got, err := front.Get(id)
			if err != nil {
				t.Fatalf("Get(%s): %v", id, err)
			}
			if got.Open {
				t.Fatalf("run %s still open after CloseRuns", id)
			}
		}
		// Already-closed ids are skipped, not re-closed.
		n, err = front.CloseRuns(context.Background(), ids, "second batch close reason pad")
		if err != nil {
			t.Fatalf("CloseRuns(closed): %v", err)
		}
		if n != 0 {
			t.Fatalf("CloseRuns on closed batch = %d, want 0", n)
		}
	})
}

func TestConformanceDeleteRun(t *testing.T) {
	eachBackend(t, func(t *testing.T, front *orders.Store) {
		run, err := front.CreateRun("rig/agent", orders.RunOpts{})
		if err != nil {
			t.Fatalf("CreateRun: %v", err)
		}
		if err := front.DeleteRun(run.ID); err != nil {
			t.Fatalf("DeleteRun: %v", err)
		}
		if _, err := front.Get(run.ID); err == nil {
			t.Fatal("Get after DeleteRun succeeded, want error")
		}
		if runs, err := front.RecentRuns("rig/agent", 0); err != nil || len(runs) != 0 {
			t.Fatalf("RecentRuns after delete = (%+v, %v), want none", runs, err)
		}
		if err := front.DeleteRun(run.ID); err == nil {
			t.Fatal("DeleteRun(missing) = nil error, want not-found error")
		}
	})
}

func TestConformanceStaleOpenRunsCutoff(t *testing.T) {
	eachBackend(t, func(t *testing.T, front *orders.Store) {
		run, err := front.CreateRun("old", orders.RunOpts{})
		if err != nil {
			t.Fatalf("CreateRun: %v", err)
		}
		stale, err := front.StaleOpenRuns(run.CreatedAt.Add(time.Hour))
		if err != nil {
			t.Fatalf("StaleOpenRuns: %v", err)
		}
		if len(stale) != 1 || stale[0].Scoped != "old" {
			t.Fatalf("StaleOpenRuns(after) = %+v, want the old run", stale)
		}
		fresh, err := front.StaleOpenRuns(run.CreatedAt.Add(-time.Hour))
		if err != nil {
			t.Fatalf("StaleOpenRuns: %v", err)
		}
		if len(fresh) != 0 {
			t.Fatalf("StaleOpenRuns(before) = %+v, want none", fresh)
		}
	})
}

func TestConformanceOrphanedOpenRunsExcludeTriggerEnvFailed(t *testing.T) {
	eachBackend(t, func(t *testing.T, front *orders.Store) {
		normal, err := front.CreateRun("a", orders.RunOpts{})
		if err != nil {
			t.Fatalf("CreateRun(a): %v", err)
		}
		if _, err := front.CreateRun("b", orders.RunOpts{Outcome: orders.RunOutcomeTriggerEnvFailed}); err != nil {
			t.Fatalf("CreateRun(b): %v", err)
		}
		orphans, err := front.OrphanedOpenRuns()
		if err != nil {
			t.Fatalf("OrphanedOpenRuns: %v", err)
		}
		if len(orphans) != 1 || orphans[0].ID != normal.ID {
			t.Fatalf("OrphanedOpenRuns = %+v, want only run a (trigger-env-failed excluded)", orphans)
		}
	})
}

func TestConformanceOrderingNewestFirst(t *testing.T) {
	eachBackend(t, func(t *testing.T, front *orders.Store) {
		var created []orders.OrderRun
		for i := 0; i < 3; i++ {
			run, err := front.CreateRun("digest", orders.RunOpts{})
			if err != nil {
				t.Fatalf("CreateRun %d: %v", i, err)
			}
			created = append(created, run)
		}
		runs, err := front.RecentRuns("digest", 0)
		if err != nil {
			t.Fatalf("RecentRuns: %v", err)
		}
		if len(runs) != 3 {
			t.Fatalf("RecentRuns = %d runs, want 3", len(runs))
		}
		for i := range runs {
			wantID := created[len(created)-1-i].ID
			if runs[i].ID != wantID {
				t.Fatalf("RecentRuns[%d].ID = %s, want %s (newest first)", i, runs[i].ID, wantID)
			}
		}
		all, err := front.RecentRunsAll(2)
		if err != nil {
			t.Fatalf("RecentRunsAll: %v", err)
		}
		if len(all) != 2 || all[0].ID != created[2].ID || all[1].ID != created[1].ID {
			t.Fatalf("RecentRunsAll(2) = %+v, want the 2 newest", all)
		}
	})
}

func TestConformanceLastRunIsNewestCreatedAt(t *testing.T) {
	eachBackend(t, func(t *testing.T, front *orders.Store) {
		if last, err := front.LastRun("digest"); err != nil || !last.IsZero() {
			t.Fatalf("LastRun(empty) = (%s, %v), want zero", last, err)
		}
		first, err := front.CreateRun("digest", orders.RunOpts{})
		if err != nil {
			t.Fatalf("CreateRun: %v", err)
		}
		if err := front.CloseRun(first.ID, "first run close reason padding"); err != nil {
			t.Fatalf("CloseRun: %v", err)
		}
		second, err := front.CreateRun("digest", orders.RunOpts{})
		if err != nil {
			t.Fatalf("CreateRun 2: %v", err)
		}
		last, err := front.LastRun("digest")
		if err != nil {
			t.Fatalf("LastRun: %v", err)
		}
		if !last.Equal(second.CreatedAt) {
			t.Fatalf("LastRun = %s, want the newest run's CreatedAt %s (closed runs still count)", last, second.CreatedAt)
		}
	})
}

func TestConformanceHasOpenWorkTrackingShortCircuits(t *testing.T) {
	eachBackend(t, func(t *testing.T, front *orders.Store) {
		open, err := front.HasOpenWork("digest", nil)
		if err != nil || open {
			t.Fatalf("HasOpenWork(empty) = (%v, %v), want (false, nil)", open, err)
		}
		run, err := front.CreateRun("digest", orders.RunOpts{})
		if err != nil {
			t.Fatalf("CreateRun: %v", err)
		}
		open, err = front.HasOpenWork("digest", func(beads.Store, beads.Bead) (bool, error) {
			t.Fatal("wisp walk must not run for an open tracking record")
			return false, nil
		})
		if err != nil {
			t.Fatalf("HasOpenWork: %v", err)
		}
		if !open {
			t.Fatal("HasOpenWork = false, want true (open tracking record)")
		}
		if err := front.CloseRun(run.ID, "close for open-work conformance"); err != nil {
			t.Fatalf("CloseRun: %v", err)
		}
		open, err = front.HasOpenWork("digest", nil)
		if err != nil || open {
			t.Fatalf("HasOpenWork(closed) = (%v, %v), want (false, nil)", open, err)
		}
	})
}

func TestConformanceListTrackingOpenRunsNewestFirst(t *testing.T) {
	eachBackend(t, func(t *testing.T, front *orders.Store) {
		a, err := front.CreateRun("rig/a", orders.RunOpts{})
		if err != nil {
			t.Fatalf("CreateRun(a): %v", err)
		}
		b, err := front.CreateRun("rig/b", orders.RunOpts{})
		if err != nil {
			t.Fatalf("CreateRun(b): %v", err)
		}
		closed, err := front.CreateRun("rig/closed", orders.RunOpts{})
		if err != nil {
			t.Fatalf("CreateRun(closed): %v", err)
		}
		if err := front.CloseRun(closed.ID, "closed run stays off the feed"); err != nil {
			t.Fatalf("CloseRun: %v", err)
		}
		runs, err := front.ListTracking()
		if err != nil {
			t.Fatalf("ListTracking: %v", err)
		}
		if len(runs) != 2 || runs[0].ID != b.ID || runs[1].ID != a.ID {
			t.Fatalf("ListTracking = %+v, want open runs [b a] newest-first", runs)
		}
	})
}

func TestConformanceGraphLegUnionsWispEvidence(t *testing.T) {
	// The two-leg union: an order whose ONLY order-run evidence is a wisp root
	// in a DISTINCT graph store must still report LastRun / Cursor /
	// HasOpenWork through either backend's front door.
	graphFor := func(t *testing.T) (beads.GraphStore, beads.Bead) {
		t.Helper()
		graphLeg := beads.NewMemStore()
		root, err := graphLeg.Create(beads.Bead{
			Title:  "wisp: digest",
			Type:   "molecule",
			Labels: []string{"order-run:digest", "order:digest", "seq:7"},
		})
		if err != nil {
			t.Fatal(err)
		}
		return beads.GraphStore{Store: graphLeg}, root
	}
	check := func(t *testing.T, front *orders.Store, root beads.Bead) {
		t.Helper()
		last, err := front.LastRun("digest")
		if err != nil {
			t.Fatalf("LastRun: %v", err)
		}
		if !last.Equal(root.CreatedAt) {
			t.Fatalf("LastRun = %s, want the wisp root's CreatedAt %s", last, root.CreatedAt)
		}
		if got := front.Cursor("digest"); got != 7 {
			t.Fatalf("Cursor = %d, want 7 from the wisp root seq", got)
		}
		open, err := front.HasOpenWork("digest", func(_ beads.Store, b beads.Bead) (bool, error) {
			return beads.IsMoleculeType(b.Type), nil
		})
		if err != nil || !open {
			t.Fatalf("HasOpenWork = (%v, %v), want (true, nil) via the graph leg", open, err)
		}
	}
	t.Run("beads", func(t *testing.T) {
		graph, root := graphFor(t)
		check(t, orders.NewStoreWithGraph(beads.OrdersStore{Store: beads.NewMemStore()}, graph), root)
	})
	t.Run("sqlite", func(t *testing.T) {
		st, err := Open(filepath.Join(t.TempDir(), "orders.db"))
		if err != nil {
			t.Fatalf("open sqlite orders store: %v", err)
		}
		t.Cleanup(func() {
			if err := st.Close(); err != nil {
				t.Errorf("close: %v", err)
			}
		})
		graph, root := graphFor(t)
		check(t, orders.NewStoreWithTracking(st, graph), root)
	})
}
