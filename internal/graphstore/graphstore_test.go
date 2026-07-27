package graphstore

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"testing"
)

func TestOpenCreatesRuntimeTables(t *testing.T) {
	store := newTestStore(t)
	rows, err := store.ReadDB().Query(
		`SELECT name FROM sqlite_master
		  WHERE type = 'table' AND name NOT LIKE 'sqlite_%'
		  ORDER BY name`,
	)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close() //nolint:errcheck

	var got []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		got = append(got, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"channel_cursors",
		"defer_wakeups",
		"edges",
		"frontier",
		"graph_meta",
		"journal",
		"node_labels",
		"node_metadata",
		"nodes",
		"retention_gate",
		"snapshot_write_gate",
		"snapshots",
		"tier_a_write_gate",
		"writer_lease",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("tables = %v, want %v", got, want)
	}
}

func TestAppendRejectsDivergentIdempotencyReplay(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	const stream = "gcj-root-idem-reuse"
	const token = "act:node-1:0"

	original := JournalEvent{
		Type:      testType,
		IdemToken: token,
		Payload:   canonPayload(t, `{"n":1}`),
	}
	if _, err := store.Append(
		ctx,
		stream,
		testEngine,
		0,
		0,
		[]JournalEvent{original},
	); err != nil {
		t.Fatalf("first append: %v", err)
	}

	divergent := original
	divergent.Payload = canonPayload(t, `{"n":2}`)
	if _, err := store.Append(
		ctx,
		stream,
		testEngine,
		0,
		0,
		[]JournalEvent{divergent},
	); !errors.Is(err, ErrIdemTokenReuse) {
		t.Fatalf("divergent replay error = %v, want ErrIdemTokenReuse", err)
	}
	if head, err := store.Head(ctx, stream); err != nil || head != 1 {
		t.Fatalf("head after divergent replay = %d, %v; want 1, nil", head, err)
	}

	replay, err := store.Append(
		ctx,
		stream,
		testEngine,
		0,
		0,
		[]JournalEvent{original},
	)
	if err != nil {
		t.Fatalf("identical replay: %v", err)
	}
	if got := replay.Duplicates[0]; got != 1 {
		t.Fatalf("replay duplicate seq = %d, want 1", got)
	}
}

func TestOpenRejectsDifferentCity(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "city.db")

	first, err := Open(ctx, path, Options{CityID: "city-a"})
	if err != nil {
		t.Fatal(err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}

	if _, err := Open(ctx, path, Options{CityID: "city-b"}); !errors.Is(err, ErrCityMismatch) {
		t.Fatalf("cross-city open error = %v, want ErrCityMismatch", err)
	}

	adopted, err := Open(ctx, path, Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = adopted.Close() })
	if got := adopted.CityID(); got != "city-a" {
		t.Fatalf("adopted city id = %q, want city-a", got)
	}
}

func TestMapSQLiteBusy(t *testing.T) {
	if got := mapSQLiteBusy(nil); got != nil {
		t.Fatalf("mapSQLiteBusy(nil) = %v, want nil", got)
	}
	if got := mapSQLiteBusy(errors.New("database is locked")); !errors.Is(got, ErrBusy) {
		t.Fatalf("locked error = %v, want ErrBusy", got)
	}
	other := errors.New("unrelated")
	if got := mapSQLiteBusy(other); !errors.Is(got, other) {
		t.Fatalf("unrelated error = %v, want original", got)
	}
}
