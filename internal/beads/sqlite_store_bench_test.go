package beads_test

import (
	"fmt"
	"testing"

	"github.com/gastownhall/gascity/internal/beads"
)

// The graph ADR's ship gates ("must prove, not assume"): point p99 <=1ms,
// filter <=10ms, writes <=5ms on current hardware, re-established against
// the ported store. Run with:
//
//	go test ./internal/beads/ -run '^$' -bench BenchmarkSQLiteStore -benchtime 2s
//
// and record the results in the infra-class-sqlite-stores HANDOFF before a
// production city flips the graph backend.

func benchSQLiteStore(b *testing.B, rows int) *beads.SQLiteStore {
	b.Helper()
	s, err := beads.OpenSQLiteStore(b.TempDir())
	if err != nil {
		b.Fatal(err)
	}
	store := s.(*beads.SQLiteStore)
	b.Cleanup(func() { _ = store.CloseStore() })
	for i := 0; i < rows; i++ {
		bead := beads.Bead{
			Title:    fmt.Sprintf("bead %d", i),
			Type:     "task",
			Labels:   []string{fmt.Sprintf("order-run:scope-%d", i%50), "gc:wisp"},
			Metadata: map[string]string{"gc.routed_to": fmt.Sprintf("pool-%d", i%10)},
		}
		if i%4 == 0 {
			bead.Ephemeral = true
		}
		created, err := store.Create(bead)
		if err != nil {
			b.Fatal(err)
		}
		if i%3 == 0 {
			if err := store.Close(created.ID); err != nil {
				b.Fatal(err)
			}
		}
	}
	return store
}

func BenchmarkSQLiteStorePointGet(b *testing.B) {
	store := benchSQLiteStore(b, 5000)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := store.Get(fmt.Sprintf("gc-%d", i%5000+1)); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkSQLiteStoreFilterList(b *testing.B) {
	store := benchSQLiteStore(b, 5000)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := store.ListByLabel(fmt.Sprintf("order-run:scope-%d", i%50), 0); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkSQLiteStoreReadyBothTiers(b *testing.B) {
	store := benchSQLiteStore(b, 5000)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := store.Ready(beads.ReadyQuery{TierMode: beads.TierBoth}); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkSQLiteStoreWrite(b *testing.B) {
	store := benchSQLiteStore(b, 1000)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := store.Create(beads.Bead{Title: fmt.Sprintf("w%d", i), Type: "task"}); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkSQLiteStoreClaim(b *testing.B) {
	store := benchSQLiteStore(b, 1000)
	ids := make([]string, 0, b.N)
	for i := 0; i < b.N; i++ {
		created, err := store.Create(beads.Bead{Title: fmt.Sprintf("c%d", i), Type: "task"})
		if err != nil {
			b.Fatal(err)
		}
		ids = append(ids, created.ID)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, ok, err := store.Claim(ids[i], "worker"); err != nil || !ok {
			b.Fatalf("claim %d: %v %v", i, ok, err)
		}
	}
}
