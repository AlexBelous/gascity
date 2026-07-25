package beads

import (
	"context"
	"fmt"
)

var (
	_ SnapshotExporter = (*SQLiteStore)(nil)
	_ SnapshotImporter = (*SQLiteStore)(nil)
	_ SnapshotFetcher  = (*SQLiteStore)(nil)
)

// ExportBeadSnapshots is unsupported on the class SQLite stores: the topology
// migrations never source work beads from a class store, so the raw-fidelity
// export surface exists only on the work backends (BdStore, NativeDoltStore).
func (s *SQLiteStore) ExportBeadSnapshots(_ context.Context, _ ExportOptions) ([]Snapshot, error) {
	return nil, fmt.Errorf("class sqlite store: %w", ErrExportUnsupported)
}

// ImportBeadSnapshots is unsupported on the class SQLite stores; the class
// migrations copy through CreateWithForeignID, not the work-bead guarded upsert.
func (s *SQLiteStore) ImportBeadSnapshots(_ context.Context, _ []Snapshot, _ ImportOptions) (ImportReport, error) {
	return ImportReport{}, fmt.Errorf("class sqlite store: %w", ErrImportUnsupported)
}

// GetBeadSnapshots is unsupported on the class SQLite stores; copy-verify reads
// the work backends' raw surface, not a class store.
func (s *SQLiteStore) GetBeadSnapshots(_ context.Context, _ []string) ([]Snapshot, error) {
	return nil, fmt.Errorf("class sqlite store: %w", ErrFetchUnsupported)
}
