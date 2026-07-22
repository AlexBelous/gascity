package messagingdb

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/gastownhall/gascity/internal/config"
)

func writeCityConfig(t *testing.T, cityPath, messagingBackend string) {
	t.Helper()
	body := "[workspace]\nname = \"test\"\n"
	if messagingBackend != "" {
		body += "\n[beads.classes.messaging]\nbackend = \"" + messagingBackend + "\"\n"
	}
	if err := os.WriteFile(filepath.Join(cityPath, "city.toml"), []byte(body), 0o644); err != nil {
		t.Fatalf("writing city.toml: %v", err)
	}
}

func writeMigratedMarker(t *testing.T, cityPath string) {
	t.Helper()
	if err := os.MkdirAll(StoreDir(cityPath), 0o755); err != nil {
		t.Fatalf("creating store dir: %v", err)
	}
	if err := os.WriteFile(MigratedMarkerPath(cityPath), []byte("messaging class migrated\n"), 0o644); err != nil {
		t.Fatalf("writing migrated marker: %v", err)
	}
}

// Without the migrated marker, routing is off and the config is never
// consulted — proven by the absence of city.toml not producing an error.
func TestRoutedMarkerAbsentSkipsConfig(t *testing.T) {
	cityPath := t.TempDir()
	routed, err := Routed(cityPath, nil)
	if err != nil {
		t.Fatalf("Routed on unmarked configless city: %v", err)
	}
	if routed {
		t.Fatal("Routed = true without a migrated marker")
	}
	if routed, err := Routed("", nil); err != nil || routed {
		t.Fatalf("Routed(\"\") = %v, %v; want false, nil", routed, err)
	}
}

func TestRoutedMarkerAndSQLiteConfig(t *testing.T) {
	cityPath := t.TempDir()
	writeMigratedMarker(t, cityPath)
	writeCityConfig(t, cityPath, "sqlite")

	// Self-loading form (nil cfg).
	routed, err := Routed(cityPath, nil)
	if err != nil {
		t.Fatalf("Routed(nil cfg): %v", err)
	}
	if !routed {
		t.Fatal("Routed = false with marker + sqlite backend")
	}

	// Caller-supplied cfg form.
	cfg := &config.City{Beads: config.BeadsConfig{Classes: map[string]config.BeadClassConfig{
		config.BeadClassMessaging: {Backend: config.BeadsClassBackendSQLite},
	}}}
	routed, err = Routed(cityPath, cfg)
	if err != nil || !routed {
		t.Fatalf("Routed(cfg) = %v, %v; want true, nil", routed, err)
	}
}

// The rollback escape hatch: marker present but the knob flipped back to bd
// routes to the bead backend again.
func TestRoutedRollbackKnobWins(t *testing.T) {
	cityPath := t.TempDir()
	writeMigratedMarker(t, cityPath)
	writeCityConfig(t, cityPath, "bd")
	routed, err := Routed(cityPath, nil)
	if err != nil {
		t.Fatalf("Routed: %v", err)
	}
	if routed {
		t.Fatal("Routed = true with backend flipped back to bd")
	}
}

// A marked city whose config cannot be loaded must error, not guess bd: the
// bead backend could be the wrong side of the split.
func TestRoutedMarkedCityConfigLoadFailureFailsClosed(t *testing.T) {
	cityPath := t.TempDir()
	writeMigratedMarker(t, cityPath)
	if _, err := Routed(cityPath, nil); err == nil {
		t.Fatal("Routed succeeded on a marked city with no loadable config")
	}
}

// Only an ABSENT marker means "not migrated": any other stat failure (EACCES,
// EIO) must fail closed rather than silently routing a possibly-migrated city
// to the bead backend.
func TestRoutedMarkerStatErrorFailsClosed(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("permission-based stat failures are not observable as root")
	}
	cityPath := t.TempDir()
	writeMigratedMarker(t, cityPath)
	writeCityConfig(t, cityPath, "sqlite")
	if err := os.Chmod(StoreDir(cityPath), 0o000); err != nil {
		t.Fatalf("chmod store dir: %v", err)
	}
	t.Cleanup(func() {
		_ = os.Chmod(StoreDir(cityPath), 0o755)
	})
	if _, err := Routed(cityPath, nil); err == nil {
		t.Fatal("Routed succeeded with an unstatable marker; a non-ENOENT stat failure must fail closed")
	}
}

// The self-load config decision is cached by city.toml (mtime, size); a knob
// rewrite invalidates it.
func TestRoutedConfigDecisionCacheInvalidatesOnRewrite(t *testing.T) {
	cityPath := t.TempDir()
	writeMigratedMarker(t, cityPath)
	writeCityConfig(t, cityPath, "sqlite")
	routed, err := Routed(cityPath, nil)
	if err != nil || !routed {
		t.Fatalf("Routed = (%v, %v), want routed", routed, err)
	}
	// Rollback: flip the knob back to bd (different content length, so the
	// (mtime, size) key misses even on a coarse-clock filesystem).
	writeCityConfig(t, cityPath, "bd")
	routed, err = Routed(cityPath, nil)
	if err != nil {
		t.Fatalf("Routed after rollback: %v", err)
	}
	if routed {
		t.Fatal("Routed = true after the knob flipped back to bd (stale cache entry)")
	}
}

func TestRoutedStoreForUnrouted(t *testing.T) {
	cityPath := t.TempDir()
	st, routed, err := RoutedStoreFor(cityPath, nil)
	if err != nil || routed || st != nil {
		t.Fatalf("RoutedStoreFor(unrouted) = %v, %v, %v; want nil, false, nil", st, routed, err)
	}
	if _, err := os.Stat(StorePath(cityPath)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("class store created on an unrouted city (stat err %v)", err)
	}
}

func TestRoutedStoreForRoutedOpensSharedHandle(t *testing.T) {
	cityPath := t.TempDir()
	writeMigratedMarker(t, cityPath)
	writeCityConfig(t, cityPath, "sqlite")
	st, routed, err := RoutedStoreFor(cityPath, nil)
	if err != nil || !routed || st == nil {
		t.Fatalf("RoutedStoreFor(routed) = %v, %v, %v; want store, true, nil", st, routed, err)
	}
	again, err := SharedStoreFor(cityPath)
	if err != nil {
		t.Fatalf("SharedStoreFor: %v", err)
	}
	if st != again {
		t.Fatal("RoutedStoreFor and SharedStoreFor returned distinct handles for one path")
	}
}

// A routed city whose class store cannot open must surface the error —
// callers fail closed instead of falling back to bd.
func TestRoutedStoreForOpenFailureFailsClosed(t *testing.T) {
	cityPath := t.TempDir()
	writeMigratedMarker(t, cityPath)
	writeCityConfig(t, cityPath, "sqlite")
	// A directory where the database file should be makes Open fail.
	if err := os.MkdirAll(StorePath(cityPath), 0o755); err != nil {
		t.Fatalf("blocking store path: %v", err)
	}
	if _, _, err := RoutedStoreFor(cityPath, nil); err == nil {
		t.Fatal("RoutedStoreFor succeeded with an unopenable class store on a routed city")
	}
}
