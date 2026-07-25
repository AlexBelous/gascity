package beads

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestSQLiteStoreCreatesAndGets(t *testing.T) {
	s, err := OpenSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() {
		if c, ok := s.(interface{ CloseStore() error }); ok {
			c.CloseStore() //nolint:errcheck
		}
	}()

	b := Bead{Title: "hello world", Type: "task"}
	created, err := s.Create(b)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if created.ID == "" {
		t.Fatal("created bead has empty ID")
	}
	if created.Status != "open" {
		t.Fatalf("expected status=open, got %q", created.Status)
	}

	got, err := s.Get(created.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Title != "hello world" {
		t.Fatalf("expected title %q, got %q", "hello world", got.Title)
	}
}

func TestSQLiteStoreReleaseIfCurrent(t *testing.T) {
	s, err := OpenSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() {
		if c, ok := s.(interface{ CloseStore() error }); ok {
			c.CloseStore() //nolint:errcheck
		}
	}()

	created, err := s.Create(Bead{Title: "work", Assignee: "worker-1"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	status := "in_progress"
	if err := s.Update(created.ID, UpdateOpts{Status: &status}); err != nil {
		t.Fatalf("Update status: %v", err)
	}

	releaser := s.(ConditionalAssignmentReleaser)
	released, err := releaser.ReleaseIfCurrent(created.ID, "worker-2")
	if err != nil {
		t.Fatalf("ReleaseIfCurrent wrong assignee: %v", err)
	}
	if released {
		t.Fatal("ReleaseIfCurrent released a bead with the wrong assignee")
	}
	got, err := s.Get(created.ID)
	if err != nil {
		t.Fatalf("Get after skipped release: %v", err)
	}
	if got.Status != "in_progress" || got.Assignee != "worker-1" {
		t.Fatalf("skipped release mutated bead: %+v", got)
	}

	released, err = releaser.ReleaseIfCurrent("missing", "worker-1")
	if err != nil {
		t.Fatalf("ReleaseIfCurrent missing id: %v", err)
	}
	if released {
		t.Fatal("ReleaseIfCurrent released missing bead")
	}

	openBead, err := s.Create(Bead{Title: "open work", Assignee: "worker-1"})
	if err != nil {
		t.Fatalf("Create open bead: %v", err)
	}
	released, err = releaser.ReleaseIfCurrent(openBead.ID, "worker-1")
	if err != nil {
		t.Fatalf("ReleaseIfCurrent wrong status: %v", err)
	}
	if released {
		t.Fatal("ReleaseIfCurrent released non-in-progress bead")
	}

	released, err = releaser.ReleaseIfCurrent(created.ID, "worker-1")
	if err != nil {
		t.Fatalf("ReleaseIfCurrent matching assignee: %v", err)
	}
	if !released {
		t.Fatal("ReleaseIfCurrent did not release matching in-progress assignment")
	}
	got, err = s.Get(created.ID)
	if err != nil {
		t.Fatalf("Get after release: %v", err)
	}
	if got.Status != "open" || got.Assignee != "" {
		t.Fatalf("released bead = %+v, want open and unassigned", got)
	}
}

func TestSQLiteStoreReady(t *testing.T) {
	s, err := OpenSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() {
		if c, ok := s.(interface{ CloseStore() error }); ok {
			c.CloseStore() //nolint:errcheck
		}
	}()

	// Create an unblocked bead.
	free, err := s.Create(Bead{Title: "free task", Type: "task"})
	if err != nil {
		t.Fatalf("create free: %v", err)
	}

	// Create a blocker and a blocked bead (dependency wired via DepAdd).
	blocker, err := s.Create(Bead{Title: "blocker", Type: "task"})
	if err != nil {
		t.Fatalf("create blocker: %v", err)
	}
	blocked, err := s.Create(Bead{Title: "blocked task", Type: "task"})
	if err != nil {
		t.Fatalf("create blocked: %v", err)
	}
	if err := s.DepAdd(blocked.ID, blocker.ID, "blocks"); err != nil {
		t.Fatalf("dep add: %v", err)
	}

	ready, err := s.Ready()
	if err != nil {
		t.Fatalf("ready: %v", err)
	}

	readyIDs := make(map[string]bool)
	for _, b := range ready {
		readyIDs[b.ID] = true
	}
	if !readyIDs[free.ID] {
		t.Errorf("free bead %q should be ready", free.ID)
	}
	if !readyIDs[blocker.ID] {
		t.Errorf("blocker %q should be ready", blocker.ID)
	}
	if readyIDs[blocked.ID] {
		t.Errorf("blocked bead %q should NOT be ready", blocked.ID)
	}
}

func TestSQLiteStoreReadyHonorsTierMode(t *testing.T) {
	s, err := OpenSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() {
		if c, ok := s.(interface{ CloseStore() error }); ok {
			c.CloseStore() //nolint:errcheck
		}
	}()

	history, err := s.Create(Bead{Title: "history", Type: "task"})
	if err != nil {
		t.Fatalf("create history: %v", err)
	}
	noHistory, err := s.Create(Bead{Title: "no history", Type: "task", NoHistory: true})
	if err != nil {
		t.Fatalf("create no history: %v", err)
	}
	ephemeral, err := s.Create(Bead{Title: "ephemeral", Type: "task", Ephemeral: true})
	if err != nil {
		t.Fatalf("create ephemeral: %v", err)
	}

	defaultReady, err := s.Ready()
	if err != nil {
		t.Fatalf("Ready(default): %v", err)
	}
	if sqliteReadyIDSet(defaultReady)[ephemeral.ID] {
		t.Fatalf("Ready(default) included ephemeral row %q: %+v", ephemeral.ID, defaultReady)
	}
	if !sqliteReadyIDSet(defaultReady)[history.ID] || !sqliteReadyIDSet(defaultReady)[noHistory.ID] {
		t.Fatalf("Ready(default) = %+v, want history and no-history rows", defaultReady)
	}

	wisps, err := s.Ready(ReadyQuery{TierMode: TierWisps})
	if err != nil {
		t.Fatalf("Ready(TierWisps): %v", err)
	}
	wispIDs := sqliteReadyIDSet(wisps)
	if wispIDs[history.ID] {
		t.Fatalf("Ready(TierWisps) included history row %q: %+v", history.ID, wisps)
	}
	if !wispIDs[noHistory.ID] || !wispIDs[ephemeral.ID] {
		t.Fatalf("Ready(TierWisps) = %+v, want no-history and ephemeral rows", wisps)
	}

	both, err := s.Ready(ReadyQuery{TierMode: TierBoth})
	if err != nil {
		t.Fatalf("Ready(TierBoth): %v", err)
	}
	bothIDs := sqliteReadyIDSet(both)
	for _, id := range []string{history.ID, noHistory.ID, ephemeral.ID} {
		if !bothIDs[id] {
			t.Fatalf("Ready(TierBoth) = %+v, missing %s", both, id)
		}
	}
}

func TestSQLiteStoreCloseStore(t *testing.T) {
	// settleBelow yields until the goroutine count drops to at most target
	// (CloseStore joins the sweeper synchronously; only database/sql's
	// internal closer goroutines need a beat), bounded so a real leak still
	// fails. No fixed sleep — the fixed_sleep census ratchet is hard.
	settleBelow := func(target int) int {
		n := runtime.NumGoroutine()
		for i := 0; i < 200_000 && n > target; i++ {
			runtime.Gosched()
			if i%1000 == 0 {
				runtime.GC()
			}
			n = runtime.NumGoroutine()
		}
		return n
	}

	settleBelow(0)
	base := runtime.NumGoroutine()

	s, err := OpenSQLiteStore(t.TempDir(),
		WithSQLiteStoreRetention(4*time.Hour, 30*time.Second))
	if err != nil {
		t.Fatalf("open: %v", err)
	}

	closer, ok := s.(interface{ CloseStore() error })
	if !ok {
		t.Fatal("SQLiteStore does not implement CloseStore() error")
	}
	if err := closer.CloseStore(); err != nil {
		t.Fatalf("CloseStore: %v", err)
	}
	// Idempotent second call must not error.
	if err := closer.CloseStore(); err != nil {
		t.Fatalf("second CloseStore: %v", err)
	}

	residual := settleBelow(base+5) - base
	if residual > 5 {
		t.Fatalf("CloseStore leaked goroutines: residual=%d after open+close (want <=5)", residual)
	}
}

// TestSQLiteStoreNoLeakOnDiscard is the goroutine-leak regression test ported
// from investigate/ga-qsvwe1-coordstore-leak @1ea16a7a3. Opening N stores with
// the retention sweeper enabled and calling CloseStore on each must keep the
// goroutine count at ~baseline. Without CloseStore the count would grow by
// >=1 goroutine per store per tick.
func TestSQLiteStoreNoLeakOnDiscard(t *testing.T) {
	const n = 25

	// settleBelow yields until the goroutine count drops to at most target
	// (CloseStore joins the sweeper synchronously; only database/sql's
	// internal closer goroutines need a beat), bounded so a real leak still
	// fails. No fixed sleep — the fixed_sleep census ratchet is hard.
	settleBelow := func(target int) int {
		n := runtime.NumGoroutine()
		for i := 0; i < 200_000 && n > target; i++ {
			runtime.Gosched()
			if i%1000 == 0 {
				runtime.GC()
			}
			n = runtime.NumGoroutine()
		}
		return n
	}

	settleBelow(0)
	base := runtime.NumGoroutine()

	for i := 0; i < n; i++ {
		s, err := OpenSQLiteStore(t.TempDir(),
			WithSQLiteStoreRetention(4*time.Hour, 30*time.Second))
		if err != nil {
			t.Fatalf("open %d: %v", i, err)
		}
		closer, ok := s.(interface{ CloseStore() error })
		if !ok {
			t.Fatalf("SQLiteStore does not implement CloseStore() error")
		}
		if err := closer.CloseStore(); err != nil {
			t.Fatalf("CloseStore %d: %v", i, err)
		}
	}

	residual := settleBelow(base+5) - base
	t.Logf("goroutines: base=%d after=%d residual=%d (opened+closed %d stores)",
		base, base+residual, residual, n)

	if residual > 5 {
		t.Fatalf("SQLiteStore CloseStore did not release resources: residual goroutines=%d after %d open+close cycles (want <=5)", residual, n)
	}
}

func sqliteReadyIDSet(rows []Bead) map[string]bool {
	ids := make(map[string]bool, len(rows))
	for _, row := range rows {
		ids[row.ID] = true
	}
	return ids
}

func TestIsSQLiteBusy(t *testing.T) {
	cases := []struct {
		err  error
		want bool
	}{
		{nil, false},
		{errors.New("some other error"), false},
		{errors.New("database is locked (5) (SQLITE_BUSY)"), true},
		{errors.New("SQLITE_BUSY (5)"), true},
		{errors.New("database is locked"), true},
		{fmt.Errorf("sqlite update: begin tx: %w", errors.New("database is locked (5) (SQLITE_BUSY)")), true},
	}
	for _, tc := range cases {
		if got := isSQLiteBusy(tc.err); got != tc.want {
			t.Errorf("isSQLiteBusy(%v) = %v, want %v", tc.err, got, tc.want)
		}
	}
}

func TestRetryOnBusy(t *testing.T) {
	t.Run("succeeds_immediately", func(t *testing.T) {
		calls := 0
		err := retryOnBusy(func() error {
			calls++
			return nil
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if calls != 1 {
			t.Fatalf("expected 1 call, got %d", calls)
		}
	})

	t.Run("retries_on_busy_then_succeeds", func(t *testing.T) {
		calls := 0
		busyErr := errors.New("database is locked (5) (SQLITE_BUSY)")
		err := retryOnBusy(func() error {
			calls++
			if calls < 3 {
				return busyErr
			}
			return nil
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if calls != 3 {
			t.Fatalf("expected 3 calls, got %d", calls)
		}
	})

	t.Run("exhausts_retries_and_returns_busy_error", func(t *testing.T) {
		calls := 0
		busyErr := errors.New("database is locked (5) (SQLITE_BUSY)")
		err := retryOnBusy(func() error {
			calls++
			return busyErr
		})
		if err == nil {
			t.Fatal("expected error, got nil")
		}
		if calls != 1+sqliteBusyRetryAttempts {
			t.Fatalf("expected %d calls, got %d", 1+sqliteBusyRetryAttempts, calls)
		}
	})

	t.Run("does_not_retry_non_busy_error", func(t *testing.T) {
		calls := 0
		err := retryOnBusy(func() error {
			calls++
			return errors.New("something else went wrong")
		})
		if err == nil {
			t.Fatal("expected error, got nil")
		}
		if calls != 1 {
			t.Fatalf("expected 1 call, got %d", calls)
		}
	})
}

func closeSQLiteTestStore(t *testing.T, s Store) {
	t.Helper()
	if c, ok := s.(interface{ CloseStore() error }); ok {
		c.CloseStore() //nolint:errcheck
	}
}

func TestSQLiteStoreReadOnlyReadsWithoutMutating(t *testing.T) {
	dir := t.TempDir()
	// Seed a source db through the normal read-write path.
	rw, err := OpenSQLiteStore(dir, WithSQLiteStoreIDPrefix("gcg"))
	if err != nil {
		t.Fatalf("open rw: %v", err)
	}
	seeded, err := rw.Create(Bead{ID: "gcg-42", Title: "src", Type: "session", Status: "open"})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	closeSQLiteTestStore(t, rw)

	ro, err := OpenSQLiteStore(dir, WithSQLiteStoreReadOnly(), WithSQLiteStoreIDPrefix("gcg"))
	if err != nil {
		t.Fatalf("open ro: %v", err)
	}
	defer closeSQLiteTestStore(t, ro)

	got, err := ro.Get(seeded.ID)
	if err != nil {
		t.Fatalf("ro get: %v", err)
	}
	if got.Title != "src" {
		t.Fatalf("ro get title = %q, want src", got.Title)
	}
	rows, err := ro.List(ListQuery{IncludeClosed: true, TierMode: TierBoth, AllowScan: true})
	if err != nil {
		t.Fatalf("ro list: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("ro list = %d rows, want 1", len(rows))
	}

	// Writes must be rejected by the driver, never mutate the source.
	if _, err := ro.Create(Bead{Title: "nope", Type: "task"}); err == nil {
		t.Fatal("read-only Create unexpectedly succeeded")
	}
}

func fileSHA256(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("read %s: %v", p, err)
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// TestSQLiteStoreReadOnlyLeavesSourceByteIdenticalWithLiveWAL reproduces the
// exact scenario the query_only(1) form failed: a read-only open over a source
// db that carries a POPULATED, un-checkpointed -wal (a stopped writer's crash
// state), as the SOLE connection. A mode=ro open must read the WAL-resident
// rows AND leave both the main db file and the -wal byte-identical across
// open/read/close — a query_only connection instead auto-checkpoints on close,
// rewriting the main db and deleting the -wal (mutating the migration source).
func TestSQLiteStoreReadOnlyLeavesSourceByteIdenticalWithLiveWAL(t *testing.T) {
	// Seed a checkpointed row through the store, then a second row via a raw
	// writer with autocheckpoint disabled so it stays WAL-resident.
	seed := t.TempDir()
	rw, err := OpenSQLiteStore(seed, WithSQLiteStoreIDPrefix("gcg"))
	if err != nil {
		t.Fatalf("open seed: %v", err)
	}
	if _, err := rw.Create(Bead{ID: "gcg-1", Type: "session", Title: "checkpointed", Status: "open"}); err != nil {
		t.Fatalf("seed create: %v", err)
	}
	closeSQLiteTestStore(t, rw)

	raw, err := sql.Open("sqlite", filepath.Join(seed, "beads.sqlite")+"?_pragma=wal_autocheckpoint(0)")
	if err != nil {
		t.Fatalf("open raw writer: %v", err)
	}
	raw.SetMaxOpenConns(1)
	beadJSON := `{"id":"gcg-2","issue_type":"session","status":"open","title":"wal-resident"}`
	if _, err := raw.Exec(
		"INSERT INTO beads(id,tier,title,status,issue_type,created_at,updated_at,bead_json) VALUES('gcg-2','main','wal-resident','open','session',1,1,?)",
		beadJSON); err != nil {
		t.Fatalf("wal-resident insert: %v", err)
	}
	// Copy main + -wal out from under the still-open writer so the copy carries a
	// live, un-checkpointed WAL but has NO holding connection (the stopped-writer
	// crash state), then release the writer.
	src := t.TempDir()
	for _, suffix := range []string{"", "-wal"} {
		b, rerr := os.ReadFile(filepath.Join(seed, "beads.sqlite"+suffix))
		if rerr != nil {
			t.Fatalf("read seed%s: %v", suffix, rerr)
		}
		if werr := os.WriteFile(filepath.Join(src, "beads.sqlite"+suffix), b, 0o644); werr != nil {
			t.Fatalf("write src%s: %v", suffix, werr)
		}
	}
	_ = raw.Close()

	mainPath := filepath.Join(src, "beads.sqlite")
	walPath := mainPath + "-wal"
	if _, err := os.Stat(walPath); err != nil {
		t.Fatalf("precondition: source has no live -wal: %v", err)
	}
	mainBefore, walBefore := fileSHA256(t, mainPath), fileSHA256(t, walPath)

	ro, err := OpenSQLiteStore(src, WithSQLiteStoreReadOnly(), WithSQLiteStoreIDPrefix("gcg"))
	if err != nil {
		t.Fatalf("open ro: %v", err)
	}
	rows, err := ro.List(ListQuery{IncludeClosed: true, TierMode: TierBoth, AllowScan: true})
	if err != nil {
		t.Fatalf("ro list: %v", err)
	}
	closeSQLiteTestStore(t, ro)

	// The read must see BOTH the checkpointed and the WAL-resident row.
	if len(rows) != 2 {
		t.Fatalf("ro list = %d rows, want 2 (incl the WAL-resident row)", len(rows))
	}
	if _, err := os.Stat(walPath); err != nil {
		t.Fatalf("read-only open DELETED the source -wal (checkpoint-on-close): %v", err)
	}
	if got := fileSHA256(t, mainPath); got != mainBefore {
		t.Fatalf("read-only open MUTATED the source main db (checkpoint-on-close): %s != %s", got, mainBefore)
	}
	if got := fileSHA256(t, walPath); got != walBefore {
		t.Fatalf("read-only open MUTATED the source -wal: %s != %s", got, walBefore)
	}
}

func TestSQLiteStoreReadOnlyMissingFileErrors(t *testing.T) {
	// A read-only open never creates the file or its parent directory.
	dir := filepath.Join(t.TempDir(), "absent")
	if _, err := OpenSQLiteStore(dir, WithSQLiteStoreReadOnly()); err == nil {
		t.Fatal("expected error opening a nonexistent read-only store")
	}
	if _, statErr := os.Stat(dir); !os.IsNotExist(statErr) {
		t.Fatalf("read-only open created the directory: stat err = %v", statErr)
	}
}
