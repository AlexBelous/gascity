package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/events"
	"github.com/gastownhall/gascity/internal/orders"
)

// dispatchExecEffectAssertionFixture wires a single exec order through
// mad.dispatchExec directly (like TestOrderDispatchExecFailure) so the test
// controls cityPath precisely instead of going through cooldown scheduling.
func dispatchExecEffectAssertionFixture(t *testing.T, orderName string, cityPath string, fakeExec ExecRunner) *memRecorder {
	t.Helper()
	store := beads.NewMemStore()
	var rec memRecorder
	tracking, err := store.Create(beads.Bead{
		Title:  "order:" + orderName,
		Labels: []string{"order-run:" + orderName, labelOrderTracking},
	})
	if err != nil {
		t.Fatal(err)
	}
	aa := []orders.Order{{
		Name:     orderName,
		Trigger:  "cooldown",
		Interval: "2m",
		Exec:     "scripts/" + orderName + ".sh",
	}}
	ad := buildOrderDispatcherFromListExec(aa, store, nil, fakeExec, &rec)
	mad := ad.(*memoryOrderDispatcher)
	mad.dispatchExec(context.Background(), orders.NewStore(beads.OrdersStore{Store: store}), execStoreTarget{ScopeRoot: cityPath}, aa[0], cityPath, tracking.ID, nil)
	return &rec
}

// backupEligibleFixtureDB creates a real (non-system) database directory
// under cityPath's managed Dolt data root, so backupEffectSnapshot's
// zero-databases skip guard does not mask the comparison these tests
// mean to exercise. Backup has no .no-sync concept (mol-dog-backup.sh
// never checks it), so unlike the sync fixtures, that marker is
// irrelevant here — only .dolt presence and a non-system name matter.
func backupEligibleFixtureDB(t *testing.T, cityPath string) {
	t.Helper()
	dbDir := filepath.Join(cityPath, ".beads", "dolt", "gascity")
	if err := os.MkdirAll(filepath.Join(dbDir, ".dolt"), 0o755); err != nil {
		t.Fatal(err)
	}
}

func TestOrderEffectAssertionFiresOnNoOpBackupSuccess(t *testing.T) {
	cityPath := t.TempDir()
	backupEligibleFixtureDB(t, cityPath)
	fakeExec := func(_ context.Context, _, _ string, _ []string) ([]byte, error) {
		return []byte("backup: backup — synced: 0/0, offsite: n/a\n"), nil
	}
	rec := dispatchExecEffectAssertionFixture(t, "mol-dog-backup", cityPath, fakeExec)

	if !rec.hasType(events.OrderCompleted) {
		t.Error("missing order.completed event")
	}
	if !rec.hasType(events.OrderEffectAssertionFailed) {
		t.Error("expected order.effect_assertion_failed for a claimed-success run that wrote nothing under .dolt-backup")
	}
}

func TestOrderEffectAssertionSilentOnRealBackupSuccess(t *testing.T) {
	cityPath := t.TempDir()
	backupEligibleFixtureDB(t, cityPath)
	fakeExec := func(_ context.Context, _, _ string, _ []string) ([]byte, error) {
		backupDir := filepath.Join(cityPath, ".dolt-backup", "gascity")
		if err := os.MkdirAll(backupDir, 0o755); err != nil {
			return nil, err
		}
		if err := os.WriteFile(filepath.Join(backupDir, "manifest"), []byte("x"), 0o644); err != nil {
			return nil, err
		}
		return []byte("backup: backup — synced: 1/1, offsite: ok\n"), nil
	}
	rec := dispatchExecEffectAssertionFixture(t, "mol-dog-backup", cityPath, fakeExec)

	if !rec.hasType(events.OrderCompleted) {
		t.Error("missing order.completed event")
	}
	if rec.hasType(events.OrderEffectAssertionFailed) {
		t.Error("did not expect order.effect_assertion_failed when the backup dir actually gained new content")
	}
}

func TestOrderEffectAssertionSkipsBackupWithNoDatabases(t *testing.T) {
	cityPath := t.TempDir()
	// No .beads/dolt/* database dirs at all: mirrors mol-dog-backup.sh's
	// own "backup: no databases found, skipping" exit-0 no-op path (a
	// freshly-initialized city that hasn't onboarded a database yet).
	fakeExec := func(_ context.Context, _, _ string, _ []string) ([]byte, error) {
		return []byte("backup: no databases found, skipping\n"), nil
	}
	rec := dispatchExecEffectAssertionFixture(t, "mol-dog-backup", cityPath, fakeExec)

	if !rec.hasType(events.OrderCompleted) {
		t.Error("missing order.completed event")
	}
	if rec.hasType(events.OrderEffectAssertionFailed) {
		t.Error("did not expect order.effect_assertion_failed when the city has zero databases to back up (a legitimate, script-acknowledged no-op)")
	}
}

func TestOrderEffectAssertionSkipsSyncWithNoEligibleDatabases(t *testing.T) {
	cityPath := t.TempDir()
	dbDir := filepath.Join(cityPath, ".beads", "dolt", "gascity")
	if err := os.MkdirAll(filepath.Join(dbDir, ".dolt"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dbDir, ".no-sync"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	fakeExec := func(_ context.Context, _, _ string, _ []string) ([]byte, error) {
		return []byte("gascity: skipped (.no-sync)\n"), nil
	}
	rec := dispatchExecEffectAssertionFixture(t, "dolt-remotes-patrol", cityPath, fakeExec)

	if !rec.hasType(events.OrderCompleted) {
		t.Error("missing order.completed event")
	}
	if rec.hasType(events.OrderEffectAssertionFailed) {
		t.Error("did not expect order.effect_assertion_failed when every database is .no-sync'd (a fully local-only rig is a legitimate, permanent state)")
	}
}

func TestOrderEffectAssertionFiresOnNoOpSyncSuccess(t *testing.T) {
	cityPath := t.TempDir()
	dbDir := filepath.Join(cityPath, ".beads", "dolt", "gascity")
	if err := os.MkdirAll(filepath.Join(dbDir, ".dolt"), 0o755); err != nil {
		t.Fatal(err)
	}
	// No .no-sync marker: this database is eligible for the assertion.
	fakeExec := func(_ context.Context, _, _ string, _ []string) ([]byte, error) {
		return []byte("gascity: pushed main -> origin:main\n"), nil
	}
	rec := dispatchExecEffectAssertionFixture(t, "dolt-remotes-patrol", cityPath, fakeExec)

	if !rec.hasType(events.OrderEffectAssertionFailed) {
		t.Error("expected order.effect_assertion_failed for an eligible database whose .dolt dir did not change despite a claimed-success push")
	}
}

func TestOrderEffectAssertionSkipsSyncWhenOnlySystemDatabasesExist(t *testing.T) {
	cityPath := t.TempDir()
	// Only reserved system databases present — none of these should ever
	// register as "eligible," matching the case-insensitive skip list in
	// examples/bd/dolt/commands/sync/run.sh's per-database sync loop.
	for _, name := range []string{"mysql", "information_schema", "Dolt_Cluster"} {
		if err := os.MkdirAll(filepath.Join(cityPath, ".beads", "dolt", name, ".dolt"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	fakeExec := func(_ context.Context, _, _ string, _ []string) ([]byte, error) {
		return []byte("gascity: nothing to sync\n"), nil
	}
	rec := dispatchExecEffectAssertionFixture(t, "dolt-remotes-patrol", cityPath, fakeExec)

	if rec.hasType(events.OrderEffectAssertionFailed) {
		t.Error("did not expect order.effect_assertion_failed when only reserved system databases exist (no real user database is eligible)")
	}
}

func TestOrderEffectAssertionSilentOnExecFailure(t *testing.T) {
	cityPath := t.TempDir()
	fakeExec := func(_ context.Context, _, _ string, _ []string) ([]byte, error) {
		return []byte("boom\n"), fmt.Errorf("exit status 1")
	}
	rec := dispatchExecEffectAssertionFixture(t, "mol-dog-backup", cityPath, fakeExec)

	if !rec.hasType(events.OrderFailed) {
		t.Error("missing order.failed event")
	}
	if rec.hasType(events.OrderEffectAssertionFailed) {
		t.Error("did not expect order.effect_assertion_failed on an already-failed exec — that path is covered by order.failed")
	}
}

// The remaining tests exercise the probe functions directly, independent
// of dispatchExec, per t.TempDir() fixtures.

func TestBackupEffectSnapshotSkipsWhenNoUserDatabases(t *testing.T) {
	cityPath := t.TempDir()
	if _, ok := backupEffectSnapshot(cityPath); ok {
		t.Error("expected ok=false when the city has zero user databases")
	}
}

func TestBackupEffectSnapshotDetectsNewContent(t *testing.T) {
	cityPath := t.TempDir()
	backupEligibleFixtureDB(t, cityPath)

	before, ok := backupEffectSnapshot(cityPath)
	if !ok {
		t.Fatal("expected ok=true with an eligible database present")
	}

	backupDir := filepath.Join(cityPath, ".dolt-backup", "gascity")
	if err := os.MkdirAll(backupDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(backupDir, "manifest"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	after, ok := backupEffectSnapshot(cityPath)
	if !ok {
		t.Fatal("expected ok=true after the backup dir gained content")
	}
	if after == before {
		t.Error("expected the snapshot to change once .dolt-backup gained new content")
	}
}

func TestDoltSyncEffectSnapshotExcludesNoSyncDatabases(t *testing.T) {
	cityPath := t.TempDir()
	dbDir := filepath.Join(cityPath, ".beads", "dolt", "gascity")
	if err := os.MkdirAll(filepath.Join(dbDir, ".dolt"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dbDir, ".no-sync"), nil, 0o644); err != nil {
		t.Fatal(err)
	}

	if _, ok := doltSyncEffectSnapshot(cityPath); ok {
		t.Error("expected ok=false when the only database present is .no-sync'd")
	}
}

func TestDoltSyncEffectSnapshotExcludesSystemDatabases(t *testing.T) {
	cityPath := t.TempDir()
	if err := os.MkdirAll(filepath.Join(cityPath, ".beads", "dolt", "mysql", ".dolt"), 0o755); err != nil {
		t.Fatal(err)
	}

	if _, ok := doltSyncEffectSnapshot(cityPath); ok {
		t.Error("expected ok=false when only a reserved system database is present")
	}
}

func TestNewestMTimeReportsNotFoundForMissingDir(t *testing.T) {
	if _, ok := newestMTime(filepath.Join(t.TempDir(), "does-not-exist")); ok {
		t.Error("expected ok=false for a directory that does not exist")
	}
}

func TestNewestMTimeFindsDeepestChange(t *testing.T) {
	dir := t.TempDir()
	nested := filepath.Join(dir, "a", "b")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	before, ok := newestMTime(dir)
	if !ok {
		t.Fatal("expected ok=true for an existing directory")
	}

	if err := os.WriteFile(filepath.Join(nested, "file"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Force the new file's mtime forward so the comparison isn't sensitive
	// to filesystem mtime granularity within the same test tick.
	future := time.Now().Add(time.Hour)
	if err := os.Chtimes(filepath.Join(nested, "file"), future, future); err != nil {
		t.Fatal(err)
	}

	after, ok := newestMTime(dir)
	if !ok {
		t.Fatal("expected ok=true after adding a file")
	}
	if !after.After(before) {
		t.Errorf("expected newest mtime to advance after adding a file under a nested dir, before=%v after=%v", before, after)
	}
}
