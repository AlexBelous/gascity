package graphstore

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestStoreCreatesAppendsAndReadsCompletePrefix(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "journal.db"), Options{})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	cleanupStore(t, store)

	stream, err := NewStreamAddress("formula/run-1")
	if err != nil {
		t.Fatalf("NewStreamAddress: %v", err)
	}
	genesis := mustRecord(t, 1, "kernel.command", `{"id":"run-1"}`)
	head, err := store.Create(ctx, stream, genesis)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if got, want := head, CursorAt(1); got != want {
		t.Fatalf("Create cursor = %v, want %v", got, want)
	}
	lease, err := store.AcquireLease(ctx, stream, "writer-a", time.Minute)
	if err != nil {
		t.Fatalf("AcquireLease: %v", err)
	}

	next, err := store.Append(ctx, stream, head, lease, []Record{
		mustRecord(t, 2, "kernel.observation", `{"status":"ok"}`),
	})
	if err != nil {
		t.Fatalf("Append: %v", err)
	}
	if got, want := next, CursorAt(2); got != want {
		t.Fatalf("Append cursor = %v, want %v", got, want)
	}

	prefix, err := store.Read(ctx, stream, CursorAt(0))
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if got, want := prefix.After(), CursorAt(0); got != want {
		t.Fatalf("prefix after = %v, want %v", got, want)
	}
	if got, want := prefix.Through(), CursorAt(2); got != want {
		t.Fatalf("prefix through = %v, want %v", got, want)
	}
	if got, want := prefix.Records(), []Record{genesis, mustRecord(t, 2, "kernel.observation", `{"status":"ok"}`)}; !recordsEqual(got, want) {
		t.Fatalf("prefix records = %#v, want %#v", got, want)
	}

	if _, err := store.Append(ctx, stream, next, lease, []Record{
		mustRecord(t, 3, "kernel.terminal", `{}`),
	}); err != nil {
		t.Fatalf("Append after cursor: %v", err)
	}
	delta, err := store.Read(ctx, stream, prefix.Through())
	if err != nil {
		t.Fatalf("Read after reused cursor: %v", err)
	}
	if got := delta.Records(); len(got) != 1 || got[0].Sequence() != 3 {
		t.Fatalf("reused cursor records = %#v, want only sequence 3", got)
	}
}

func TestStoreCreateAndAppendUseExpectedCursorCAS(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "journal.db"), Options{})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	cleanupStore(t, store)
	stream, err := NewStreamAddress("formula/run-2")
	if err != nil {
		t.Fatalf("NewStreamAddress: %v", err)
	}
	genesis := mustRecord(t, 1, "kernel.command", `{}`)
	if _, err := store.Create(ctx, stream, genesis); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := store.Create(ctx, stream, genesis); !errors.Is(err, ErrStreamExists) {
		t.Fatalf("second Create error = %v, want ErrStreamExists", err)
	}
	lease, err := store.AcquireLease(ctx, stream, "writer-a", time.Minute)
	if err != nil {
		t.Fatalf("AcquireLease: %v", err)
	}
	if _, err := store.Append(ctx, stream, CursorAt(0), lease, []Record{mustRecord(t, 1, "kernel.command", `{}`)}); !errors.Is(err, ErrCursorMismatch) {
		t.Fatalf("Append stale cursor error = %v, want ErrCursorMismatch", err)
	}
	if prefix, err := store.Read(ctx, stream, CursorAt(0)); err != nil || prefix.Through() != CursorAt(1) {
		t.Fatalf("Read after rejected CAS = (%v, %v), want genesis only", prefix, err)
	}
}

func TestStoreRejectsZeroStreamAddress(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "journal.db"), Options{})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	cleanupStore(t, store)
	if _, err := NewStreamAddress(""); err == nil {
		t.Fatal("NewStreamAddress accepted an empty address")
	}
	zero := StreamAddress{}
	genesis := mustRecord(t, 1, "kernel.command", `{}`)
	if _, err := store.Create(ctx, zero, genesis); err == nil {
		t.Fatal("Create accepted a zero stream address")
	}
	if _, err := store.Read(ctx, zero, CursorAt(0)); err == nil {
		t.Fatal("Read accepted a zero stream address")
	}
	if _, err := store.AcquireLease(ctx, zero, "writer-a", time.Minute); err == nil {
		t.Fatal("AcquireLease accepted a zero stream address")
	}
	if _, err := store.Append(ctx, zero, CursorAt(1), WriterLease{}, []Record{
		mustRecord(t, 2, "kernel.observation", `{}`),
	}); err == nil {
		t.Fatal("Append accepted a zero stream address")
	}
}

func TestStoreReadersSeeCommittedPrefixAcrossHandlesAndReopen(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "journal.db")
	first, err := Open(ctx, path, Options{})
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	stream, err := NewStreamAddress("formula/run-3")
	if err != nil {
		t.Fatalf("NewStreamAddress: %v", err)
	}
	if _, err := first.Create(ctx, stream, mustRecord(t, 1, "kernel.command", `{}`)); err != nil {
		t.Fatalf("Create: %v", err)
	}
	second, err := Open(ctx, path, Options{})
	if err != nil {
		t.Fatalf("second Open: %v", err)
	}
	tx, err := first.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin uncommitted append: %v", err)
	}
	if err := insertRecord(ctx, tx, stream, mustRecord(t, 2, "kernel.observation", `{}`)); err != nil {
		_ = tx.Rollback()
		t.Fatalf("insert uncommitted append: %v", err)
	}
	prefix, err := second.Read(ctx, stream, CursorAt(0))
	if err != nil || prefix.Through() != CursorAt(1) {
		_ = tx.Rollback()
		t.Fatalf("concurrent Read = (%v, %v), want committed genesis only", prefix, err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatalf("rollback uncommitted append: %v", err)
	}
	lease, err := first.AcquireLease(ctx, stream, "writer-a", time.Minute)
	if err != nil {
		t.Fatalf("AcquireLease: %v", err)
	}
	if _, err := first.Append(ctx, stream, CursorAt(1), lease, []Record{mustRecord(t, 2, "kernel.observation", `{}`)}); err != nil {
		t.Fatalf("Append: %v", err)
	}

	prefix, err = second.Read(ctx, stream, CursorAt(0))
	if err != nil || prefix.Through() != CursorAt(2) {
		t.Fatalf("cross-handle Read = (%v, %v), want through 2", prefix, err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if err := second.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}

	reopened, err := Open(ctx, path, Options{})
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	cleanupStore(t, reopened)
	prefix, err = reopened.Read(ctx, stream, CursorAt(1))
	if err != nil || prefix.Through() != CursorAt(2) || len(prefix.Records()) != 1 {
		t.Fatalf("reopened Read = (%v, %v), want one record through 2", prefix, err)
	}
}

func TestStoreConcurrentCASHasOneWinner(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "journal.db"), Options{})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	cleanupStore(t, store)

	createStream, err := NewStreamAddress("formula/run-4-create")
	if err != nil {
		t.Fatalf("NewStreamAddress create: %v", err)
	}
	createStart := make(chan struct{})
	createErrs := make(chan error, 2)
	var createWG sync.WaitGroup
	for range 2 {
		createWG.Add(1)
		go func() {
			defer createWG.Done()
			<-createStart
			_, err := store.Create(ctx, createStream, mustRecord(t, 1, "kernel.command", `{}`))
			createErrs <- err
		}()
	}
	close(createStart)
	createWG.Wait()
	close(createErrs)
	var creates, existing int
	for err := range createErrs {
		switch {
		case err == nil:
			creates++
		case errors.Is(err, ErrStreamExists):
			existing++
		default:
			t.Fatalf("concurrent Create error = %v", err)
		}
	}
	if creates != 1 || existing != 1 {
		t.Fatalf("Create outcomes = %d created, %d existing; want one each", creates, existing)
	}

	stream, err := NewStreamAddress("formula/run-4")
	if err != nil {
		t.Fatalf("NewStreamAddress: %v", err)
	}
	if _, err := store.Create(ctx, stream, mustRecord(t, 1, "kernel.command", `{}`)); err != nil {
		t.Fatalf("Create: %v", err)
	}
	lease, err := store.AcquireLease(ctx, stream, "writer-a", time.Minute)
	if err != nil {
		t.Fatalf("AcquireLease: %v", err)
	}

	start := make(chan struct{})
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for _, typ := range []string{"kernel.observation", "kernel.terminal"} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := store.Append(ctx, stream, CursorAt(1), lease, []Record{mustRecord(t, 2, typ, `{}`)})
			errs <- err
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	var successes, conflicts int
	for err := range errs {
		if err == nil {
			successes++
			continue
		}
		if errors.Is(err, ErrCursorMismatch) {
			conflicts++
			continue
		}
		t.Fatalf("concurrent Append error = %v", err)
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("CAS outcomes = %d successes, %d conflicts; want one each", successes, conflicts)
	}
}

func TestStoreFencesStaleLeaseWithInjectedClock(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, time.July, 27, 12, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	store, err := Open(ctx, filepath.Join(t.TempDir(), "journal.db"), Options{Clock: clock})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	cleanupStore(t, store)
	absent, err := NewStreamAddress("formula/absent")
	if err != nil {
		t.Fatalf("NewStreamAddress absent: %v", err)
	}
	if _, err := store.AcquireLease(ctx, absent, "writer-a", time.Minute); !errors.Is(err, ErrStreamNotFound) {
		t.Fatalf("absent AcquireLease error = %v, want ErrStreamNotFound", err)
	}
	stream, err := NewStreamAddress("formula/run-5")
	if err != nil {
		t.Fatalf("NewStreamAddress: %v", err)
	}
	if _, err := store.Create(ctx, stream, mustRecord(t, 1, "kernel.command", `{}`)); err != nil {
		t.Fatalf("Create: %v", err)
	}
	first, err := store.AcquireLease(ctx, stream, "writer-a", time.Minute)
	if err != nil {
		t.Fatalf("first AcquireLease: %v", err)
	}
	if _, err := store.AcquireLease(ctx, stream, "writer-b", time.Minute); !errors.Is(err, ErrLeaseHeld) {
		t.Fatalf("contended AcquireLease error = %v, want ErrLeaseHeld", err)
	}
	now = now.Add(30 * time.Second)
	renewed, err := store.AcquireLease(ctx, stream, "writer-a", time.Minute)
	if err != nil {
		t.Fatalf("same-holder AcquireLease: %v", err)
	}
	if got, want := renewed.Epoch(), first.Epoch()+1; got != want {
		t.Fatalf("renewed epoch = %d, want %d", got, want)
	}
	if _, err := store.Append(ctx, stream, CursorAt(1), first, []Record{mustRecord(t, 2, "kernel.observation", `{}`)}); !errors.Is(err, ErrLeaseFenced) {
		t.Fatalf("same-holder stale Append error = %v, want ErrLeaseFenced", err)
	}
	now = now.Add(2 * time.Minute)
	second, err := store.AcquireLease(ctx, stream, "writer-b", time.Minute)
	if err != nil {
		t.Fatalf("expired AcquireLease: %v", err)
	}
	if got, want := second.Epoch(), renewed.Epoch()+1; got != want {
		t.Fatalf("replacement epoch = %d, want %d", got, want)
	}
	if _, err := store.Append(ctx, stream, CursorAt(1), renewed, []Record{mustRecord(t, 2, "kernel.observation", `{}`)}); !errors.Is(err, ErrLeaseFenced) {
		t.Fatalf("stale Append error = %v, want ErrLeaseFenced", err)
	}
	if _, err := store.Append(ctx, stream, CursorAt(1), second, []Record{mustRecord(t, 2, "kernel.observation", `{}`)}); err != nil {
		t.Fatalf("current Append: %v", err)
	}
}

func TestStoreRefusesMutationAndCorruptCommittedRows(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "journal.db"), Options{})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	cleanupStore(t, store)
	stream, err := NewStreamAddress("formula/run-6")
	if err != nil {
		t.Fatalf("NewStreamAddress: %v", err)
	}
	if _, err := store.Create(ctx, stream, mustRecord(t, 1, "kernel.command", `{}`)); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE journal SET typ = 'tampered' WHERE stream = ? AND sequence = 1`, stream.value); err == nil {
		t.Fatal("UPDATE journal succeeded, want append-only refusal")
	}
	if _, err := store.db.ExecContext(ctx, `DELETE FROM journal WHERE stream = ? AND sequence = 1`, stream.value); err == nil {
		t.Fatal("DELETE journal succeeded, want append-only refusal")
	}
	if _, err := store.db.ExecContext(ctx, `DROP TRIGGER journal_no_update`); err != nil {
		t.Fatalf("DROP update trigger: %v", err)
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE journal SET payload_hash = zeroblob(32) WHERE stream = ? AND sequence = 1`, stream.value); err != nil {
		t.Fatalf("corrupt payload hash: %v", err)
	}
	if _, err := store.Read(ctx, stream, CursorAt(0)); !errors.Is(err, ErrCorruptJournal) {
		t.Fatalf("Read corrupted hash error = %v, want ErrCorruptJournal", err)
	}
}

func TestStoreRefusesSparsePersistedJournal(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "journal.db"), Options{})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	cleanupStore(t, store)
	stream, err := NewStreamAddress("formula/run-6-sparse")
	if err != nil {
		t.Fatalf("NewStreamAddress: %v", err)
	}
	if _, err := store.Create(ctx, stream, mustRecord(t, 1, "kernel.command", `{}`)); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := store.db.ExecContext(ctx,
		`INSERT INTO journal(stream, sequence, typ, payload, payload_hash)
		 SELECT stream, 3, typ, payload, payload_hash FROM journal WHERE stream = ? AND sequence = 1`, stream.value,
	); err != nil {
		t.Fatalf("insert sparse row: %v", err)
	}
	if _, err := store.Read(ctx, stream, CursorAt(0)); !errors.Is(err, ErrCorruptJournal) {
		t.Fatalf("Read sparse journal error = %v, want ErrCorruptJournal", err)
	}
}

func TestStoreRefusesSparseAndInvalidBatchesWithoutWriting(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "journal.db"), Options{})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	cleanupStore(t, store)
	stream, err := NewStreamAddress("formula/run-7")
	if err != nil {
		t.Fatalf("NewStreamAddress: %v", err)
	}
	if _, err := store.Create(ctx, stream, mustRecord(t, 1, "kernel.command", `{}`)); err != nil {
		t.Fatalf("Create: %v", err)
	}
	lease, err := store.AcquireLease(ctx, stream, "writer-a", time.Minute)
	if err != nil {
		t.Fatalf("AcquireLease: %v", err)
	}
	if _, err := store.Append(ctx, stream, CursorAt(1), lease, []Record{
		mustRecord(t, 2, "kernel.observation", `{}`),
		mustRecord(t, 4, "kernel.terminal", `{}`),
	}); !errors.Is(err, ErrIncompletePrefix) {
		t.Fatalf("sparse Append error = %v, want ErrIncompletePrefix", err)
	}
	if prefix, err := store.Read(ctx, stream, CursorAt(0)); err != nil || prefix.Through() != CursorAt(1) {
		t.Fatalf("Read after rejected batch = (%v, %v), want genesis only", prefix, err)
	}
}

func TestStoreRollsBackBatchWhenSecondInsertAborts(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "journal.db"), Options{})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	cleanupStore(t, store)
	stream, err := NewStreamAddress("formula/run-rollback")
	if err != nil {
		t.Fatalf("NewStreamAddress: %v", err)
	}
	if _, err := store.Create(ctx, stream, mustRecord(t, 1, "kernel.command", `{}`)); err != nil {
		t.Fatalf("Create: %v", err)
	}
	lease, err := store.AcquireLease(ctx, stream, "writer-a", time.Minute)
	if err != nil {
		t.Fatalf("AcquireLease: %v", err)
	}
	if _, err := store.db.ExecContext(ctx, `
		CREATE TRIGGER test_abort_second_insert BEFORE INSERT ON journal
		WHEN NEW.stream = 'formula/run-rollback' AND NEW.sequence = 3
		BEGIN SELECT RAISE(ABORT, 'test abort'); END;
	`); err != nil {
		t.Fatalf("create abort trigger: %v", err)
	}
	if _, err := store.Append(ctx, stream, CursorAt(1), lease, []Record{
		mustRecord(t, 2, "kernel.observation", `{}`),
		mustRecord(t, 3, "kernel.terminal", `{}`),
	}); err == nil {
		t.Fatal("Append succeeded despite second-row abort trigger")
	}
	prefix, err := store.Read(ctx, stream, CursorAt(0))
	if err != nil || prefix.Through() != CursorAt(1) || len(prefix.Records()) != 1 {
		t.Fatalf("Read after rolled-back batch = (%v, %v), want genesis only", prefix, err)
	}
}

func recordsEqual(got, want []Record) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i].Sequence() != want[i].Sequence() || got[i].Type() != want[i].Type() || string(got[i].Payload()) != string(want[i].Payload()) {
			return false
		}
	}
	return true
}

func cleanupStore(t *testing.T, store *Store) {
	t.Helper()
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
}
