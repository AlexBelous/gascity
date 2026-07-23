package beads_test

import (
	"testing"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/beads/beadstest"
)

// newSQLiteForConformance returns a fresh, empty SQLite store with cleanup
// registered.
func newSQLiteForConformance(t *testing.T) *beads.SQLiteStore {
	t.Helper()
	s, err := beads.OpenSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatalf("OpenSQLiteStore: %v", err)
	}
	store := s.(*beads.SQLiteStore)
	t.Cleanup(func() { _ = store.CloseStore() })
	return store
}

// TestSQLiteStoreConformance runs the tree's shared beads.Store conformance
// suite against the recovered SQLite store (the graph class backend), the
// same suite MemStore/FileStore/NativeDoltStore pass. b36's shim ran the
// deploy lineage's coordtest suites; this tree's canonical kit is beadstest.
func TestSQLiteStoreConformance(t *testing.T) {
	factory := func() beads.Store { return newSQLiteForConformance(t) }
	beadstest.RunStoreTests(t, factory)
	beadstest.RunSequentialIDTests(t, factory)
	beadstest.RunCreationOrderTests(t, factory)
	beadstest.RunDepTests(t, factory)
	beadstest.RunMetadataTests(t, factory)
}
