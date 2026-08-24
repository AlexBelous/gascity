package storeref

// What the census costs, and on which population.
//
// HasLegacyResidents lists the binding with AllowScan and IncludeClosed, so its
// cost is the binding's WHOLE history, not its working set — and the verdict is
// load-bearing on `gc bd`'s by-id path, the busiest one-shot route in the CLI.
// A one-shot process pays this before it answers anything.
//
// The benchmark seeds a clean binding, because clean is the case that cannot be
// memoized away: only the TRUE verdict is monotone and safe to cache, so a city
// that has never held a relic keeps paying the scan on every invocation. The
// number here is what that city pays.

import (
	"fmt"
	"testing"

	"github.com/gastownhall/gascity/internal/beads"
)

func benchmarkRelicCensus(b *testing.B, rows int, closedFraction int) {
	b.Helper()
	store, err := beads.OpenSQLiteStore(b.TempDir(), beads.WithSQLiteStoreIDPrefix("gcg"))
	if err != nil {
		b.Fatalf("OpenSQLiteStore: %v", err)
	}
	for i := range rows {
		bead, err := store.Create(beads.Bead{Title: fmt.Sprintf("graph node %d", i), Type: "task"})
		if err != nil {
			b.Fatalf("seeding row %d: %v", i, err)
		}
		if closedFraction > 0 && i%closedFraction == 0 {
			if err := store.Close(bead.ID); err != nil {
				b.Fatalf("closing row %d: %v", i, err)
			}
		}
	}
	binding := ClassBinding{Prefixes: []string{"gcg"}, Leg: Leg{Ref: "graph", Store: store}}
	b.ResetTimer()
	for b.Loop() {
		if HasLegacyResidents(binding) {
			b.Fatal("seeded binding reports relics; the fixture mints under its own prefix")
		}
	}
}

// The closed population is the one ga-qdt5y.19 added to the scan by widening the
// verdict from OPEN residents to ALL of them, so the fixture is mostly closed.
func BenchmarkRelicCensusCleanBinding_1k(b *testing.B)  { benchmarkRelicCensus(b, 1000, 2) }
func BenchmarkRelicCensusCleanBinding_10k(b *testing.B) { benchmarkRelicCensus(b, 10000, 2) }

// The open-only cost, for comparison: what the census charged before the
// verdict was widened.
func BenchmarkRelicCensusOpenOnly_10k(b *testing.B) {
	store, err := beads.OpenSQLiteStore(b.TempDir(), beads.WithSQLiteStoreIDPrefix("gcg"))
	if err != nil {
		b.Fatalf("OpenSQLiteStore: %v", err)
	}
	for i := range 10000 {
		bead, err := store.Create(beads.Bead{Title: fmt.Sprintf("graph node %d", i), Type: "task"})
		if err != nil {
			b.Fatalf("seeding row %d: %v", i, err)
		}
		if i%2 == 0 {
			if err := store.Close(bead.ID); err != nil {
				b.Fatalf("closing row %d: %v", i, err)
			}
		}
	}
	b.ResetTimer()
	for b.Loop() {
		if _, err := OpenLegacyResidents(store, []string{"gcg"}); err != nil {
			b.Fatalf("OpenLegacyResidents: %v", err)
		}
	}
}
