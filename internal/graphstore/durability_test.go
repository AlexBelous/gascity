package graphstore

import (
	"context"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/graphstore/canon"
	"github.com/gastownhall/gascity/internal/graphstore/fold"
	"github.com/gastownhall/gascity/internal/graphstore/fold/foldtest"
)

const testSnapshotType = "lumen.snapshot.anchored"

func TestSnapshotRoundTripAnchorsJournal(t *testing.T) {
	store := newTestStore(t)
	store.RegisterEventType(testEngine, testSnapshotType)
	ctx := context.Background()
	const stream = "gcj-snapshot-round-trip"

	head := appendTestEvents(t, store, stream, 3)
	snapshot, anchor := testSnapshot(t, stream, head)

	anchorSeq, err := store.WriteSnapshot(ctx, testEngine, 0, snapshot, anchor)
	if err != nil {
		t.Fatalf("write snapshot: %v", err)
	}
	if anchorSeq != head+1 {
		t.Fatalf("anchor seq = %d, want %d", anchorSeq, head+1)
	}

	got, ok, err := store.LatestSnapshot(ctx, stream)
	if err != nil {
		t.Fatalf("latest snapshot: %v", err)
	}
	if !ok {
		t.Fatal("latest snapshot not found")
	}
	if got.CoveredSeq != head || got.StateHash != snapshot.StateHash ||
		string(got.State) != string(snapshot.State) {
		t.Fatalf("snapshot = %+v, want covered seq %d and original state", got, head)
	}
	if err := store.Verify(ctx, stream); err != nil {
		t.Fatalf("verify anchored journal: %v", err)
	}
}

func TestTruncateBelowSnapshotPreservesVerifiableTail(t *testing.T) {
	store := newTestStore(t)
	store.RegisterEventType(testEngine, testSnapshotType)
	ctx := context.Background()
	const stream = "gcj-retained-tail"

	head := appendTestEvents(t, store, stream, 3)
	snapshot, anchor := testSnapshot(t, stream, head)
	anchorSeq, err := store.WriteSnapshot(ctx, testEngine, 0, snapshot, anchor)
	if err != nil {
		t.Fatalf("write snapshot: %v", err)
	}
	appendTestEventAt(t, store, stream, anchorSeq, 4)

	deleted, err := store.TruncateBelowAnchor(ctx, stream, head)
	if err != nil {
		t.Fatalf("truncate below anchor: %v", err)
	}
	if deleted != int64(head) {
		t.Fatalf("deleted = %d, want %d", deleted, head)
	}
	events, err := store.ReadStream(ctx, stream, 1, 0)
	if err != nil {
		t.Fatalf("read retained tail: %v", err)
	}
	if len(events) != 2 || events[0].Seq != anchorSeq {
		t.Fatalf("retained events = %+v, want anchor and one tail event", events)
	}
	if err := store.Verify(ctx, stream); err != nil {
		t.Fatalf("verify retained tail: %v", err)
	}
}

func TestTierAProjectionRebuildsFromJournal(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	reducer := foldtest.EchoReducer{}
	const stream = "gcj-tier-a-rebuild"
	store.RegisterEventType(foldtest.Engine, foldtest.EventNode)

	state := reducer.Zero(stream)
	for i, id := range []string{"one", "two"} {
		payload := canonPayload(t, fmt.Sprintf(`{"id":%q}`, id))
		if _, err := store.Append(ctx, stream, foldtest.Engine, uint64(i), 0, []JournalEvent{{
			Type:    foldtest.EventNode,
			Payload: payload,
		}}); err != nil {
			t.Fatalf("append %s: %v", id, err)
		}
		next, delta, err := reducer.Apply(state, fold.Event{
			StreamID: stream,
			Seq:      uint64(i + 1),
			Engine:   foldtest.Engine,
			Type:     foldtest.EventNode,
			Payload:  payload,
		})
		if err != nil {
			t.Fatalf("fold %s: %v", id, err)
		}
		if err := store.ApplyDelta(ctx, delta); err != nil {
			t.Fatalf("project %s: %v", id, err)
		}
		state = next
	}

	before := projectedNodeTitles(t, store, stream)
	if err := store.RebuildTierA(ctx, reducer, stream); err != nil {
		t.Fatalf("rebuild tier A: %v", err)
	}
	after := projectedNodeTitles(t, store, stream)
	if before != after || after != "one:,two:," {
		t.Fatalf("projection before=%q after=%q, want byte-identical one:,two:,", before, after)
	}
}

func TestReleasedLeaseRetainsCurrentEpoch(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	const stream = "gcj-lease-epoch"

	lease, err := store.AcquireWriterLease(ctx, stream, "driver", time.Minute)
	if err != nil {
		t.Fatalf("acquire lease: %v", err)
	}
	if err := store.ReleaseWriterLease(ctx, lease); err != nil {
		t.Fatalf("release lease: %v", err)
	}
	epoch, err := store.CurrentLeaseEpoch(ctx, stream)
	if err != nil {
		t.Fatalf("current lease epoch: %v", err)
	}
	if epoch != lease.Epoch {
		t.Fatalf("current epoch = %d, want released epoch %d", epoch, lease.Epoch)
	}
}

func TestSnapshotRejectsMismatchedStateHash(t *testing.T) {
	store := newTestStore(t)
	store.RegisterEventType(testEngine, testSnapshotType)
	ctx := context.Background()
	const stream = "gcj-snapshot-hash"

	head := appendTestEvents(t, store, stream, 1)
	snapshot, anchor := testSnapshot(t, stream, head)
	snapshot.StateHash[0] ^= 0xff

	if _, err := store.WriteSnapshot(ctx, testEngine, 0, snapshot, anchor); !errors.Is(err, ErrSnapshotHashMismatch) {
		t.Fatalf("write snapshot error = %v, want ErrSnapshotHashMismatch", err)
	}
	if got, err := store.Head(ctx, stream); err != nil || got != head {
		t.Fatalf("head after rejected snapshot = %d, %v; want %d, nil", got, err, head)
	}
}

func TestSnapshotRequiresCurrentHead(t *testing.T) {
	store := newTestStore(t)
	store.RegisterEventType(testEngine, testSnapshotType)
	ctx := context.Background()
	const stream = "gcj-snapshot-head"

	head := appendTestEvents(t, store, stream, 2)
	snapshot, anchor := testSnapshot(t, stream, head-1)
	if _, err := store.WriteSnapshot(ctx, testEngine, 0, snapshot, anchor); !errors.Is(err, ErrWrongExpectedVersion) {
		t.Fatalf("write stale snapshot error = %v, want ErrWrongExpectedVersion", err)
	}
}

func TestTruncateRequiresExactCoveringSnapshot(t *testing.T) {
	store := newTestStore(t)
	store.RegisterEventType(testEngine, testSnapshotType)
	ctx := context.Background()
	const stream = "gcj-retention-cover"

	head := appendTestEvents(t, store, stream, 3)
	snapshot, anchor := testSnapshot(t, stream, head)
	if _, err := store.WriteSnapshot(ctx, testEngine, 0, snapshot, anchor); err != nil {
		t.Fatalf("write snapshot: %v", err)
	}
	if _, err := store.TruncateBelowAnchor(ctx, stream, head-1); !errors.Is(err, ErrNoCoveringSnapshot) {
		t.Fatalf("truncate without exact snapshot error = %v, want ErrNoCoveringSnapshot", err)
	}
	if deleted, err := store.TruncateBelowAnchor(ctx, stream, head); err != nil || deleted != int64(head) {
		t.Fatalf("truncate = (%d, %v), want (%d, nil)", deleted, err, head)
	}
	if deleted, err := store.TruncateBelowAnchor(ctx, stream, head); err != nil || deleted != 0 {
		t.Fatalf("repeated truncate = (%d, %v), want (0, nil)", deleted, err)
	}
}

func TestProjectionRejectsNonFoldOwnedNodeCollision(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	if _, err := store.ReadDB().ExecContext(ctx,
		`INSERT INTO nodes(id, created_at, fold_owned)
		 VALUES ('shared-id', '2020-01-01T00:00:00Z', 0)`,
	); err != nil {
		t.Fatalf("insert non-fold node: %v", err)
	}

	err := store.ApplyDelta(ctx, fold.Delta{NodeUpserts: []fold.NodeRow{{
		ID:        "shared-id",
		CreatedAt: "2020-01-01T00:00:00Z",
		StreamID:  "gcj-collision",
	}}})
	if !errors.Is(err, ErrProjectionIDCollision) {
		t.Fatalf("apply colliding projection error = %v, want ErrProjectionIDCollision", err)
	}
}

func TestProjectionRebuildRejectsConcurrentAppend(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	reducer := foldtest.EchoReducer{}
	const stream = "gcj-rebuild-race"
	store.RegisterEventType(foldtest.Engine, foldtest.EventNode)

	payload := canonPayload(t, `{"id":"one"}`)
	if _, err := store.Append(ctx, stream, foldtest.Engine, 0, 0, []JournalEvent{{
		Type:    foldtest.EventNode,
		Payload: payload,
	}}); err != nil {
		t.Fatalf("append initial event: %v", err)
	}
	store.rebuildAfterRead = func() {
		store.rebuildAfterRead = nil
		if _, err := store.Append(ctx, stream, foldtest.Engine, 1, 0, []JournalEvent{{
			Type:    foldtest.EventNode,
			Payload: canonPayload(t, `{"id":"two"}`),
		}}); err != nil {
			t.Errorf("append racing event: %v", err)
		}
	}

	if err := store.RebuildTierA(ctx, reducer, stream); !errors.Is(err, ErrRebuildRaced) {
		t.Fatalf("raced rebuild error = %v, want ErrRebuildRaced", err)
	}
	if err := store.RebuildTierA(ctx, reducer, stream); err != nil {
		t.Fatalf("rebuild retry: %v", err)
	}
}

func TestSnapshotAndProjectionTablesAreWriteClosed(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	if _, err := store.ReadDB().ExecContext(ctx,
		`INSERT INTO snapshots(
			stream_id, covered_seq, engine, reducer_version,
			snapshot_format_version, state_hash, state, created_at
		) VALUES ('forged', 1, 'lumen', 1, 1, zeroblob(32), x'00', 'now')`,
	); err == nil {
		t.Fatal("direct snapshot insert succeeded, want write-closed error")
	}
	if _, err := store.ReadDB().ExecContext(ctx,
		`INSERT INTO frontier(node_id, root_id, created_at, id)
		 VALUES ('forged', 'root', 'now', 'forged')`,
	); err == nil {
		t.Fatal("direct frontier insert succeeded, want write-closed error")
	}
}

func TestVersionOneJournalUpgradesToDurableRuntimeSchema(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "graph.db")
	legacy, err := openSQLite(ctx, path, migrations[:1])
	if err != nil {
		t.Fatalf("open version-one store: %v", err)
	}
	if err := legacy.Close(); err != nil {
		t.Fatalf("close version-one store: %v", err)
	}

	store, err := Open(ctx, path, Options{CityID: "city-upgrade"})
	if err != nil {
		t.Fatalf("upgrade store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	for _, table := range []string{"snapshots", "nodes", "frontier"} {
		var name string
		if err := store.ReadDB().QueryRowContext(ctx,
			`SELECT name FROM sqlite_master WHERE type = 'table' AND name = ?`,
			table,
		).Scan(&name); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				t.Fatalf("upgraded store is missing table %q", table)
			}
			t.Fatalf("query upgraded table %q: %v", table, err)
		}
	}
}

func appendTestEvents(t *testing.T, store *Store, stream string, count int) uint64 {
	t.Helper()
	var head uint64
	for i := 0; i < count; i++ {
		head = appendTestEventAt(t, store, stream, head, i)
	}
	return head
}

func appendTestEventAt(t *testing.T, store *Store, stream string, head uint64, value int) uint64 {
	t.Helper()
	result, err := store.Append(context.Background(), stream, testEngine, head, 0, []JournalEvent{{
		Type:      testType,
		IdemToken: fmt.Sprintf("%s:event:%d", stream, value),
		Payload:   canonPayload(t, fmt.Sprintf(`{"value":%d}`, value)),
	}})
	if err != nil {
		t.Fatalf("append event %d: %v", value, err)
	}
	return result.FirstSeq
}

func testSnapshot(t *testing.T, stream string, coveredSeq uint64) (fold.Snapshot, JournalEvent) {
	t.Helper()
	state := canonPayload(t, fmt.Sprintf(`{"stream":%q,"covered_seq":%d}`, stream, coveredSeq))
	stateHash := canon.Hash(state)
	return fold.Snapshot{
			StreamID:              stream,
			CoveredSeq:            coveredSeq,
			Engine:                testEngine,
			ReducerVersion:        1,
			SnapshotFormatVersion: 1,
			StateHash:             stateHash,
			State:                 state,
		}, JournalEvent{
			Type:              testSnapshotType,
			IRContractVersion: "lumen.ir/0.2.5",
			IdemToken:         fmt.Sprintf("%s:snapshot:%d", stream, coveredSeq),
			Payload: canonPayload(t, fmt.Sprintf(
				`{"covered_seq":%d,"state_hash":%q}`,
				coveredSeq,
				hex.EncodeToString(stateHash[:]),
			)),
		}
}

func projectedNodeTitles(t *testing.T, store *Store, stream string) string {
	t.Helper()
	rows, err := store.ReadDB().Query(
		`SELECT id, title FROM nodes WHERE stream_id = ? ORDER BY id`,
		stream,
	)
	if err != nil {
		t.Fatalf("query projected nodes: %v", err)
	}
	defer rows.Close() //nolint:errcheck

	var result string
	for rows.Next() {
		var id, title string
		if err := rows.Scan(&id, &title); err != nil {
			t.Fatalf("scan projected node: %v", err)
		}
		result += id + ":" + title + ","
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read projected nodes: %v", err)
	}
	return result
}
