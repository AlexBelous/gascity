package main

import (
	"bytes"
	"os"
	"strings"
	"testing"

	nudgesdb "github.com/gastownhall/gascity/internal/classdb/nudges"
)

// TestStartClassStoreMaintenanceUnroutedIsInert proves an unrouted city
// pays nothing: no store files are created and nothing is reported.
func TestStartClassStoreMaintenanceUnroutedIsInert(t *testing.T) {
	cityPath := t.TempDir()
	var stderr bytes.Buffer
	startClassStoreMaintenance(cityPath, classMigrationConfig(t, ""), &stderr)
	if _, err := os.Stat(nudgesdb.StoreDir(cityPath)); !os.IsNotExist(err) {
		t.Fatalf(".gc/store created on an unrouted city (stat err %v)", err)
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr %q, want empty", stderr.String())
	}
}

// TestStartClassStoreMaintenanceRoutedStarts proves a routed class store
// gets its loop (idempotently — a second call must not stack) and that a
// routing failure is reported without aborting the other classes.
func TestStartClassStoreMaintenanceRoutedStarts(t *testing.T) {
	cityPath := t.TempDir()
	writeOrdersMigratedMarker(t, cityPath)
	cfg := classMigrationConfig(t, `
[beads.classes.orders]
backend = "sqlite"
`)
	var stderr bytes.Buffer
	startClassStoreMaintenance(cityPath, cfg, &stderr)
	startClassStoreMaintenance(cityPath, cfg, &stderr)
	if stderr.Len() != 0 {
		t.Fatalf("stderr %q, want empty", stderr.String())
	}
	if _, err := os.Stat(ordersClassStorePath(cityPath)); err != nil {
		t.Fatalf("orders class store missing after maintenance start: %v", err)
	}
}

// TestStartClassStoreMaintenanceRoutingErrorReported proves an unstatable
// marker is surfaced per class rather than silently skipped.
func TestStartClassStoreMaintenanceRoutingErrorReported(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("permission-based stat failures do not apply to root")
	}
	cityPath := t.TempDir()
	writeOrdersMigratedMarker(t, cityPath)
	if err := os.Chmod(nudgesdb.StoreDir(cityPath), 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(nudgesdb.StoreDir(cityPath), 0o755) })
	var stderr bytes.Buffer
	startClassStoreMaintenance(cityPath, classMigrationConfig(t, ""), &stderr)
	if !strings.Contains(stderr.String(), "class maintenance:") {
		t.Fatalf("stderr %q, want per-class maintenance error", stderr.String())
	}
}
