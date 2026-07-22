package sessionsdb

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/config"
)

func sqliteSessionsConfig() *config.City {
	return &config.City{Beads: config.BeadsConfig{Classes: map[string]config.BeadClassConfig{
		config.BeadClassSessions: {Backend: config.BeadsClassBackendSQLite},
	}}}
}

func writeSessionsMarker(t *testing.T, city string) {
	t.Helper()
	if err := os.MkdirAll(StoreDir(city), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(MigratedMarkerPath(city), []byte("migrated\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestRoutedMarkerFirst(t *testing.T) {
	city := t.TempDir()
	// No marker: never routed, even with the sqlite backend configured —
	// and no config load happens (nil cfg must not read city.toml).
	routed, err := Routed(city, sqliteSessionsConfig())
	if err != nil || routed {
		t.Fatalf("unmarked city routed=%v err=%v", routed, err)
	}
	routed, err = Routed(city, nil)
	if err != nil || routed {
		t.Fatalf("unmarked city (nil cfg) routed=%v err=%v", routed, err)
	}

	writeSessionsMarker(t, city)
	routed, err = Routed(city, sqliteSessionsConfig())
	if err != nil || !routed {
		t.Fatalf("marked+configured city routed=%v err=%v", routed, err)
	}
	// Rollback escape hatch: marker present, knob back to bd.
	routed, err = Routed(city, &config.City{})
	if err != nil || routed {
		t.Fatalf("marked city with bd knob routed=%v err=%v", routed, err)
	}
}

func TestRoutedSelfLoadsConfig(t *testing.T) {
	city := t.TempDir()
	writeSessionsMarker(t, city)
	if err := os.WriteFile(filepath.Join(city, "city.toml"), []byte("[workspace]\nname = \"t\"\n\n[beads.classes.sessions]\nbackend = \"sqlite\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	routed, err := Routed(city, nil)
	if err != nil || !routed {
		t.Fatalf("self-loaded routing routed=%v err=%v", routed, err)
	}
	// A marked city whose config cannot load is an ERROR, never bd.
	broken := t.TempDir()
	writeSessionsMarker(t, broken)
	if _, err := Routed(broken, nil); err == nil {
		t.Fatal("marked city without loadable config must error, not fall back to bd")
	}
}

func TestRoutedNonENOENTMarkerStatFailsClosed(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("permission-based stat failures do not apply to root")
	}
	city := t.TempDir()
	writeSessionsMarker(t, city)
	// Make the marker's parent unreadable so the stat fails with EACCES.
	if err := os.Chmod(StoreDir(city), 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(StoreDir(city), 0o755) })
	if _, err := Routed(city, sqliteSessionsConfig()); err == nil {
		t.Fatal("EACCES on the marker stat must fail closed, not read as unmigrated")
	}
}

func TestRoutedStoreForFailClosed(t *testing.T) {
	city := t.TempDir()
	st, routed, err := RoutedStoreFor(city, sqliteSessionsConfig())
	if err != nil || routed || st != nil {
		t.Fatalf("unmarked: %v %v %v", st, routed, err)
	}
	writeSessionsMarker(t, city)
	st, routed, err = RoutedStoreFor(city, sqliteSessionsConfig())
	if err != nil || !routed || st == nil {
		t.Fatalf("marked: %v %v %v", st, routed, err)
	}
	if st.Path() != StorePath(city) {
		t.Fatalf("routed store path %q", st.Path())
	}
}

func TestUnavailableStoreFailsEveryOp(t *testing.T) {
	st := NewUnavailableStore(os.ErrPermission)
	if _, err := st.Get("gcs-1"); err == nil || !strings.Contains(err.Error(), "permission") {
		t.Fatalf("Get: %v", err)
	}
	if _, err := st.Create(beads.Bead{Title: "x"}); err == nil {
		t.Fatal("Create must fail")
	}
	if err := st.SetMetadataBatch("gcs-1", map[string]string{"k": "v"}); err == nil {
		t.Fatal("SetMetadataBatch must fail")
	}
	if _, err := st.List(beads.ListQuery{AllowScan: true}); err == nil {
		t.Fatal("List must fail")
	}
	if err := st.Ping(); err == nil {
		t.Fatal("Ping must fail")
	}
}
