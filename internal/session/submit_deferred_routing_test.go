package session

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/gastownhall/gascity/internal/beads"
	nudgesdb "github.com/gastownhall/gascity/internal/classdb/nudges"
	"github.com/gastownhall/gascity/internal/nudgequeue"
)

// stubDeferredSubmitPoller replaces the poller spawn with a no-op for the
// duration of the test (package-var stub — keep these tests serial).
func stubDeferredSubmitPoller(t *testing.T) {
	t.Helper()
	orig := startSessionSubmitPoller
	startSessionSubmitPoller = func(string, string, string) error { return nil }
	t.Cleanup(func() { startSessionSubmitPoller = orig })
}

func deferredSubmitBead() beads.Bead {
	return beads.Bead{ID: "ga-sess1", Metadata: map[string]string{"alias": "boot/dev"}}
}

// The deferred-submit fold-in: on an unrouted city the bare append lands in
// the flock'd state.json exactly as before.
func TestEnqueueDeferredSubmitUnroutedWritesFileQueue(t *testing.T) {
	stubDeferredSubmitPoller(t)
	cityPath := t.TempDir()
	m := NewManagerWithOptions(nil, nil, WithCityPath(cityPath))
	if err := m.enqueueDeferredSubmitLocked(deferredSubmitBead(), "sess", "hello"); err != nil {
		t.Fatalf("enqueueDeferredSubmitLocked: %v", err)
	}
	state, err := nudgequeue.LoadState(cityPath)
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	if len(state.Pending) != 1 || state.Pending[0].Agent != "boot/dev" || state.Pending[0].Source != "session" {
		t.Fatalf("state.json pending = %+v, want the deferred item", state.Pending)
	}
}

// On a routed city ([beads.classes.nudges] backend="sqlite" plus the
// migrated marker), the deferred submit lands in the embedded class store
// and never touches state.json.
func TestEnqueueDeferredSubmitRoutedWritesClassStore(t *testing.T) {
	stubDeferredSubmitPoller(t)
	cityPath := t.TempDir()
	if err := os.WriteFile(filepath.Join(cityPath, "city.toml"), []byte("[workspace]\nname = \"test\"\n\n[beads.classes.nudges]\nbackend = \"sqlite\"\n"), 0o644); err != nil {
		t.Fatalf("writing city.toml: %v", err)
	}
	if err := os.MkdirAll(nudgesdb.StoreDir(cityPath), 0o755); err != nil {
		t.Fatalf("creating store dir: %v", err)
	}
	if err := os.WriteFile(nudgesdb.MigratedMarkerPath(cityPath), []byte("nudges class migrated\n"), 0o644); err != nil {
		t.Fatalf("writing migrated marker: %v", err)
	}

	m := NewManagerWithOptions(nil, nil, WithCityPath(cityPath))
	if err := m.enqueueDeferredSubmitLocked(deferredSubmitBead(), "sess", "hello"); err != nil {
		t.Fatalf("enqueueDeferredSubmitLocked: %v", err)
	}

	st, err := nudgesdb.SharedStoreFor(cityPath)
	if err != nil {
		t.Fatalf("SharedStoreFor: %v", err)
	}
	snap, err := st.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if len(snap.Pending) != 1 || snap.Pending[0].Agent != "boot/dev" || snap.Pending[0].SessionID != "ga-sess1" {
		t.Fatalf("class-store pending = %+v, want the deferred item", snap.Pending)
	}
	if _, err := os.Stat(nudgequeue.StatePath(cityPath)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("state.json written on a routed city (stat err %v)", err)
	}
}
