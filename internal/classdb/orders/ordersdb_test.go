package ordersdb

import (
	"context"
	"database/sql"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/classdb/core"
	"github.com/gastownhall/gascity/internal/config"
	"github.com/gastownhall/gascity/internal/orders"
)

func openTestStore(t *testing.T) *Store {
	t.Helper()
	st, err := Open(filepath.Join(t.TempDir(), "orders.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() {
		if err := st.Close(); err != nil {
			t.Errorf("close: %v", err)
		}
	})
	return st
}

// TestIDPrefixMatchesReservedClassPrefix pins the local idPrefix constant to
// the config registry's reserved orders-class prefix without the production
// package importing internal/config.
func TestIDPrefixMatchesReservedClassPrefix(t *testing.T) {
	want, ok := config.ReservedClassPrefix(config.BeadClassOrders)
	if !ok {
		t.Fatal("config.ReservedClassPrefix(orders) not registered")
	}
	if got := openTestStore(t).IDPrefix(); got != want {
		t.Fatalf("IDPrefix() = %q, want reserved orders prefix %q", got, want)
	}
}

// TestMintedIDsAreSequential proves the id mint produces unique
// "<prefix>-<seq>" ids that advance per create.
func TestMintedIDsAreSequential(t *testing.T) {
	st := openTestStore(t)
	want := []string{"gco-1", "gco-2", "gco-3"}
	for i, w := range want {
		run, err := st.CreateRun("rig/agent", orders.RunOpts{})
		if err != nil {
			t.Fatalf("CreateRun %d: %v", i, err)
		}
		if run.ID != w {
			t.Fatalf("run %d id = %q, want %q", i, run.ID, w)
		}
	}
}

// TestPersistenceAcrossReopen proves runs survive a full close + reopen of
// the store file — the file is the store.
func TestPersistenceAcrossReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "orders.db")
	st, err := Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	run, err := st.CreateRun("rig/agent", orders.RunOpts{})
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	reopened, err := Open(path, core.WithSingleConn())
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer reopened.Close() //nolint:errcheck
	got, err := reopened.Get(run.ID)
	if err != nil {
		t.Fatalf("Get after reopen: %v", err)
	}
	if !got.Open || got.Scoped != "rig/agent" || !got.CreatedAt.Equal(run.CreatedAt) {
		t.Fatalf("Get after reopen = %+v, want the persisted open run %+v", got, run)
	}
	// The mint counter also persists: the next id continues the sequence.
	next, err := reopened.CreateRun("rig/agent", orders.RunOpts{})
	if err != nil {
		t.Fatalf("CreateRun after reopen: %v", err)
	}
	if next.ID != "gco-2" {
		t.Fatalf("post-reopen id = %q, want gco-2 (mint counter persisted)", next.ID)
	}
}

// TestNewestFirstIDTieBreakOnEqualCreatedAt pins the ordering tie-break the
// composite index bakes in: equal created_at rows order id-DESC (the
// nativeCreatedLimitPushdown contract).
func TestNewestFirstIDTieBreakOnEqualCreatedAt(t *testing.T) {
	st := openTestStore(t)
	first, err := st.CreateRun("digest", orders.RunOpts{})
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	second, err := st.CreateRun("digest", orders.RunOpts{})
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	// Force a created_at tie so only the id tie-break decides.
	if err := st.db.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec(`UPDATE order_run SET created_at = (SELECT created_at FROM order_run WHERE id = ?)`, first.ID)
		return err
	}); err != nil {
		t.Fatalf("forcing created_at tie: %v", err)
	}
	runs, err := st.RecentRuns("digest", 0)
	if err != nil {
		t.Fatalf("RecentRuns: %v", err)
	}
	if len(runs) != 2 || runs[0].ID != second.ID || runs[1].ID != first.ID {
		t.Fatalf("RecentRuns on created_at tie = %+v, want [%s %s] (id DESC)", runs, second.ID, first.ID)
	}
}

// TestUpdatedAtAdvancesOnClose pins the retention reference clock: closing a
// run bumps updated_at past created_at (orderTrackingClosedReferenceTime
// prefers UpdatedAt as the close-time proxy).
func TestUpdatedAtAdvancesOnClose(t *testing.T) {
	st := openTestStore(t)
	run, err := st.CreateRun("digest", orders.RunOpts{})
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	if err := st.CloseRun(run.ID, "retention clock close reason pad"); err != nil {
		t.Fatalf("CloseRun: %v", err)
	}
	got, err := st.Get(run.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !got.UpdatedAt.After(got.CreatedAt) {
		t.Fatalf("UpdatedAt %s not after CreatedAt %s — the retention reference must reflect close time", got.UpdatedAt, got.CreatedAt)
	}
}

// TestUnknownOutcomeTokenFailsLoudly proves a row whose outcome column holds
// an unknown token surfaces an error instead of silently reclassifying the
// run (the outcomeFromLabels precedence lesson).
func TestUnknownOutcomeTokenFailsLoudly(t *testing.T) {
	st := openTestStore(t)
	run, err := st.CreateRun("digest", orders.RunOpts{})
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	if err := st.db.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec(`UPDATE order_run SET outcome = 'mystery' WHERE id = ?`, run.ID)
		return err
	}); err != nil {
		t.Fatalf("forcing unknown token: %v", err)
	}
	if _, err := st.Get(run.ID); err == nil {
		t.Fatal("Get with unknown outcome token succeeded, want loud decode error")
	}
}

// TestCloseRefreshesReasonOnAlreadyClosedRun mirrors the beads backend, where
// CloseRun on a closed bead still applies the close_reason metadata write.
func TestCloseRefreshesReasonOnAlreadyClosedRun(t *testing.T) {
	st := openTestStore(t)
	run, err := st.CreateRun("digest", orders.RunOpts{})
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	if err := st.CloseRun(run.ID, "original close reason padding"); err != nil {
		t.Fatalf("CloseRun: %v", err)
	}
	if err := st.CloseRun(run.ID, "refreshed close reason padding"); err != nil {
		t.Fatalf("CloseRun(closed): %v", err)
	}
	var reason string
	if err := st.db.Read().QueryRow(`SELECT close_reason FROM order_run WHERE id = ?`, run.ID).Scan(&reason); err != nil {
		t.Fatalf("reading close_reason: %v", err)
	}
	if reason != "refreshed close reason padding" {
		t.Fatalf("close_reason = %q, want the refreshed reason", reason)
	}
	// An empty reason leaves the stored reason untouched.
	if err := st.CloseRun(run.ID, ""); err != nil {
		t.Fatalf("CloseRun(empty reason): %v", err)
	}
	if err := st.db.Read().QueryRow(`SELECT close_reason FROM order_run WHERE id = ?`, run.ID).Scan(&reason); err != nil {
		t.Fatalf("reading close_reason: %v", err)
	}
	if reason != "refreshed close reason padding" {
		t.Fatalf("close_reason after empty-reason close = %q, want unchanged", reason)
	}
}

// TestCreateRunRejectsEmptyScopedName fails loudly instead of minting an
// unaddressable row.
func TestCreateRunRejectsEmptyScopedName(t *testing.T) {
	st := openTestStore(t)
	if _, err := st.CreateRun("  ", orders.RunOpts{}); err == nil {
		t.Fatal("CreateRun(blank) succeeded, want error")
	}
	if _, err := st.CreateRunClosed("", orders.RunOutcomeNone, nil, ""); err == nil {
		t.Fatal("CreateRunClosed(blank) succeeded, want error")
	}
}

// TestImportRunPreservesLegacyRow pins the migration import contract: legacy
// id, clocks, open state, outcome, and cursor round-trip verbatim, and
// re-import is an idempotent no-op.
func TestImportRunPreservesLegacyRow(t *testing.T) {
	st := openTestStore(t)
	created := time.Unix(0, time.Now().Add(-48*time.Hour).UnixNano())
	updated := created.Add(time.Hour)
	legacy := orders.OrderRun{
		ID:        "gc-legacy-7",
		Scoped:    "digest:rig:demo",
		Outcome:   orders.RunOutcomeWispFailed,
		CreatedAt: created,
		UpdatedAt: updated,
		Open:      false,
		Cursor:    9,
	}
	if err := st.ImportRun(legacy); err != nil {
		t.Fatalf("ImportRun: %v", err)
	}
	got, err := st.Get("gc-legacy-7")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !reflect.DeepEqual(got, legacy) {
		t.Fatalf("imported run = %+v, want %+v", got, legacy)
	}
	// Re-import (resumed migration) leaves the row untouched.
	changed := legacy
	changed.Cursor = 1
	if err := st.ImportRun(changed); err != nil {
		t.Fatalf("re-ImportRun: %v", err)
	}
	if got, err := st.Get("gc-legacy-7"); err != nil || got.Cursor != 9 {
		t.Fatalf("row after re-import = (%+v, %v), want the original untouched", got, err)
	}

	// Rejections: empty id / scoped / zero clock.
	for _, bad := range []orders.OrderRun{
		{Scoped: "x", CreatedAt: created},
		{ID: "gc-1", CreatedAt: created},
		{ID: "gc-1", Scoped: "x"},
	} {
		if err := st.ImportRun(bad); err == nil {
			t.Fatalf("ImportRun(%+v) succeeded, want rejection", bad)
		}
	}
}
