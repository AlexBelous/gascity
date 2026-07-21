package nudgesdb

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/config"
	"github.com/gastownhall/gascity/internal/nudgequeue"
)

func writeCityConfig(t *testing.T, cityPath, nudgesBackend string) {
	t.Helper()
	body := "[workspace]\nname = \"test\"\n"
	if nudgesBackend != "" {
		body += "\n[beads.classes.nudges]\nbackend = \"" + nudgesBackend + "\"\n"
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
	if err := os.WriteFile(MigratedMarkerPath(cityPath), []byte("nudges class migrated\n"), 0o644); err != nil {
		t.Fatalf("writing migrated marker: %v", err)
	}
}

func routingTestItem(id, agent string, now time.Time) nudgequeue.Item {
	return nudgequeue.Item{
		ID:           id,
		Agent:        agent,
		Source:       "session",
		Message:      "wake up",
		CreatedAt:    now.UTC(),
		DeliverAfter: now.UTC(),
		ExpiresAt:    now.Add(nudgequeue.DefaultTTL).UTC(),
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
		config.BeadClassNudges: {Backend: config.BeadsClassBackendSQLite},
	}}}
	routed, err = Routed(cityPath, cfg)
	if err != nil || !routed {
		t.Fatalf("Routed(cfg) = %v, %v; want true, nil", routed, err)
	}
}

// The rollback escape hatch: marker present but the knob flipped back to bd
// routes to the file backend again.
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
// file backend could be the wrong side of the split.
func TestRoutedMarkedCityConfigLoadFailureFailsClosed(t *testing.T) {
	cityPath := t.TempDir()
	writeMigratedMarker(t, cityPath)
	if _, err := Routed(cityPath, nil); err == nil {
		t.Fatal("Routed succeeded on a marked city with no loadable config")
	}
}

// Only an ABSENT marker means "not migrated": any other stat failure (EACCES,
// EIO) must fail closed rather than silently routing a possibly-migrated city
// to the file backend.
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

func TestQueueForCityUnroutedUsesFileBackend(t *testing.T) {
	cityPath := t.TempDir()
	now := time.Now()
	q := QueueForCity(cityPath, nil)
	if err := q.Enqueue(routingTestItem("nudge-file", "boot/dev", now), beads.NudgesStore{}); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	state, err := nudgequeue.LoadState(cityPath)
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	if len(state.Pending) != 1 || state.Pending[0].ID != "nudge-file" {
		t.Fatalf("file state.json pending = %+v, want the enqueued item", state.Pending)
	}
	if _, err := os.Stat(StorePath(cityPath)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("class store created on an unrouted city (stat err %v)", err)
	}
}

func TestQueueForCityRoutedUsesClassStore(t *testing.T) {
	cityPath := t.TempDir()
	writeMigratedMarker(t, cityPath)
	writeCityConfig(t, cityPath, "sqlite")
	now := time.Now()
	q := QueueForCity(cityPath, nil)
	if err := q.Enqueue(routingTestItem("nudge-sql", "boot/dev", now), beads.NudgesStore{}); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	st, err := SharedStoreFor(cityPath)
	if err != nil {
		t.Fatalf("SharedStoreFor: %v", err)
	}
	snap, err := st.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if len(snap.Pending) != 1 || snap.Pending[0].ID != "nudge-sql" {
		t.Fatalf("class-store pending = %+v, want the enqueued item", snap.Pending)
	}
	if _, err := os.Stat(nudgequeue.StatePath(cityPath)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("state.json written on a routed city (stat err %v)", err)
	}
}

// A routed city whose class store cannot open fails every operation instead
// of silently writing to the file backend.
func TestQueueForCityRoutedOpenFailureFailsClosed(t *testing.T) {
	cityPath := t.TempDir()
	writeMigratedMarker(t, cityPath)
	writeCityConfig(t, cityPath, "sqlite")
	// A directory where the database file should be makes Open fail.
	if err := os.MkdirAll(StorePath(cityPath), 0o755); err != nil {
		t.Fatalf("blocking store path: %v", err)
	}
	q := QueueForCity(cityPath, nil)
	if err := q.Enqueue(routingTestItem("nudge-x", "boot/dev", time.Now()), beads.NudgesStore{}); err == nil {
		t.Fatal("Enqueue succeeded with an unopenable class store on a routed city")
	}
	if _, err := os.Stat(nudgequeue.StatePath(cityPath)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("file backend was written on a routed city (stat err %v)", err)
	}
}

func TestSharedStoreForCachesHandle(t *testing.T) {
	cityPath := t.TempDir()
	first, err := SharedStoreFor(cityPath)
	if err != nil {
		t.Fatalf("SharedStoreFor: %v", err)
	}
	second, err := SharedStoreFor(cityPath)
	if err != nil {
		t.Fatalf("SharedStoreFor (second): %v", err)
	}
	if first != second {
		t.Fatal("SharedStoreFor returned distinct handles for one path")
	}
}

func TestTerminalRecordShadowProjection(t *testing.T) {
	now := time.Now().UTC()
	item := routingTestItem("wait-w1-e1-1", "boot/dev", now)
	item.Reference = &nudgequeue.Reference{Kind: "bead", ID: "w1"}

	live := TerminalRecord{Item: item, QueueState: statePending}
	if s := live.Shadow(); !s.Open || s.State != "queued" || s.ID != item.ID || s.Reference == nil || s.Reference.ID != "w1" {
		t.Fatalf("pending projection = %+v, want open queued shadow", s)
	}
	inFlight := TerminalRecord{Item: item, QueueState: stateInFlight}
	if s := inFlight.Shadow(); !s.Open || s.State != "queued" {
		t.Fatalf("in-flight projection = %+v, want open queued shadow", s)
	}
	dead := TerminalRecord{Item: item, QueueState: stateDead, TerminalState: "failed", TerminalReason: "boom"}
	if s := dead.Shadow(); s.Open || s.State != "failed" || s.TerminalReason != "boom" {
		t.Fatalf("dead projection = %+v, want closed failed shadow", s)
	}
	terminal := TerminalRecord{
		Item: item, QueueState: stateTerminal,
		TerminalState: "injected", CommitBoundary: "provider-nudge-return",
	}
	s := terminal.Shadow()
	if s.Open || s.State != "injected" || s.CommitBoundary != "provider-nudge-return" {
		t.Fatalf("terminal projection = %+v, want closed injected shadow", s)
	}
	if !nudgequeue.IsTerminalState(s.State) {
		t.Fatalf("terminal projection state %q is not IsTerminalState", s.State)
	}
}

func TestCountRetentionMatchesSweep(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "nudges.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() {
		if err := st.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	now := time.Now()
	old := now.Add(-48 * time.Hour)
	q := nudgequeue.NewQueueWithBackend(st)

	// One acked (terminal) item well past the ttl, one fresh pending item.
	// The old item keeps a live expiry so Ack's maintenance pass does not
	// dead-letter it before the terminal transition.
	oldItem := routingTestItem("nudge-old", "boot/dev", old)
	oldItem.ExpiresAt = now.Add(nudgequeue.DefaultTTL).UTC()
	if err := q.Enqueue(oldItem, beads.NudgesStore{}); err != nil {
		t.Fatalf("Enqueue old: %v", err)
	}
	if err := q.Ack([]string{"nudge-old"}, "injected", "", "provider-nudge-return"); err != nil {
		t.Fatalf("Ack: %v", err)
	}
	// Backdate the terminal stamp past the ttl (Ack stamps time.Now).
	if _, err := st.db.Read().Exec(`UPDATE nudges SET terminal_at = ? WHERE id = 'nudge-old'`, old.UnixNano()); err != nil {
		t.Fatalf("backdating terminal_at: %v", err)
	}
	if err := q.Enqueue(routingTestItem("nudge-live", "boot/dev", now), beads.NudgesStore{}); err != nil {
		t.Fatalf("Enqueue live: %v", err)
	}

	ttl := 24 * time.Hour
	count, err := st.CountRetention(now, ttl)
	if err != nil {
		t.Fatalf("CountRetention: %v", err)
	}
	if count != 1 {
		t.Fatalf("CountRetention = %d, want 1", count)
	}
	deleted, err := st.SweepRetention(t.Context(), now, ttl)
	if err != nil {
		t.Fatalf("SweepRetention: %v", err)
	}
	if deleted != count {
		t.Fatalf("SweepRetention deleted %d, dry-run counted %d", deleted, count)
	}
	if count, err := st.CountRetention(now, ttl); err != nil || count != 0 {
		t.Fatalf("post-sweep CountRetention = %d, %v; want 0, nil", count, err)
	}
}
