package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/beads"
	nudgesdb "github.com/gastownhall/gascity/internal/classdb/nudges"
	"github.com/gastownhall/gascity/internal/nudgequeue"
)

// writeNudgesRoutedCity builds a city directory committed to sqlite-backed
// nudges routing: [beads.classes.nudges] backend="sqlite" plus the migrated
// marker (both keys of the routing decision).
func writeNudgesRoutedCity(t *testing.T) string {
	t.Helper()
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
	return cityPath
}

// TestNudgeQueueSeamIsTheOnlyConstructionPoint is the completeness ratchet
// for the routing seam: production cmd/gc code must never construct a nudge
// queue or class store directly — a direct construction would bypass the
// [beads.classes.nudges] backend dispatch and split the queue across two
// backends on a migrated city.
func TestNudgeQueueSeamIsTheOnlyConstructionPoint(t *testing.T) {
	// cityNudgeQueue (the sole nudgesdb.QueueForCity call) and the file-only
	// ClaimDueMatching compat surface both live in cmd_nudge.go.
	allowedInNudgeFile := map[string]bool{
		"nudgesdb.QueueForCity(":       true,
		"nudgequeue.ClaimDueMatching(": true,
	}
	forbidden := []string{
		"nudgequeue.NewFileQueue(",
		"nudgequeue.NewQueueWithBackend(",
		"nudgequeue.NewUnavailableQueue(",
		"nudgequeue.WithdrawWaitNudges(",
		"nudgesdb.Open(",
		"nudgesdb.QueueForCity(",
		"nudgequeue.ClaimDueMatching(",
	}
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || filepath.Ext(name) != ".go" || strings.HasSuffix(name, "_test.go") {
			continue
		}
		data, err := os.ReadFile(name) //nolint:gosec // test reads its own package sources
		if err != nil {
			t.Fatal(err)
		}
		for _, pattern := range forbidden {
			if name == "cmd_nudge.go" && allowedInNudgeFile[pattern] {
				continue
			}
			if strings.Contains(string(data), pattern) {
				t.Errorf("%s constructs a nudge queue/class store directly (%s...); route through cityNudgeQueue / nudgeShadowReaderFor / nudgeSweepRoutingFor instead", name, pattern)
			}
		}
	}
}

// TestCityNudgeQueueRoutesToClassStore proves the front-door wrappers write
// to the embedded class store — and never the file queue — on a routed city.
func TestCityNudgeQueueRoutesToClassStore(t *testing.T) {
	cityPath := writeNudgesRoutedCity(t)
	now := time.Now()
	item := newQueuedNudgeWithOptions("boot/dev", "wake up", "session", now, queuedNudgeOptions{ID: "nudge-routed1"})
	if err := enqueueQueuedNudge(cityPath, item); err != nil {
		t.Fatalf("enqueueQueuedNudge: %v", err)
	}

	pending, _, _, err := listQueuedNudges(cityPath, "boot/dev", now)
	if err != nil {
		t.Fatalf("listQueuedNudges: %v", err)
	}
	if len(pending) != 1 || pending[0].ID != "nudge-routed1" {
		t.Fatalf("pending = %+v, want the enqueued item", pending)
	}
	if _, err := os.Stat(nudgequeue.StatePath(cityPath)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("file queue written on a routed city (stat err %v)", err)
	}

	claimed, err := claimDueQueuedNudgesForTarget(cityPath, nudgeTarget{alias: "boot/dev"}, now)
	if err != nil {
		t.Fatalf("claimDueQueuedNudgesForTarget: %v", err)
	}
	if len(claimed) != 1 {
		t.Fatalf("claimed = %+v, want one item", claimed)
	}
	if err := ackQueuedNudges(cityPath, []string{"nudge-routed1"}); err != nil {
		t.Fatalf("ackQueuedNudges: %v", err)
	}

	class, err := nudgesdb.SharedStoreFor(cityPath)
	if err != nil {
		t.Fatalf("SharedStoreFor: %v", err)
	}
	rec, ok, err := class.FindRecordIncludingTerminal("nudge-routed1")
	if err != nil || !ok {
		t.Fatalf("FindRecordIncludingTerminal = %v, %v; want the acked record", ok, err)
	}
	if rec.TerminalState != "injected" {
		t.Fatalf("terminal state = %q, want injected", rec.TerminalState)
	}
}

// TestCityNudgeQueueFailsClosedOnRoutedOpenFailure proves a routed city with
// an unreachable class store errors instead of writing to the file backend.
func TestCityNudgeQueueFailsClosedOnRoutedOpenFailure(t *testing.T) {
	cityPath := writeNudgesRoutedCity(t)
	// A directory where the database file should be makes Open fail.
	if err := os.MkdirAll(nudgesdb.StorePath(cityPath), 0o755); err != nil {
		t.Fatalf("blocking store path: %v", err)
	}
	item := newQueuedNudgeWithOptions("boot/dev", "wake up", "session", time.Now(), queuedNudgeOptions{ID: "nudge-x"})
	if err := enqueueQueuedNudge(cityPath, item); err == nil {
		t.Fatal("enqueueQueuedNudge succeeded with an unopenable class store")
	}
	if _, err := os.Stat(nudgequeue.StatePath(cityPath)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("file queue written on a routed city (stat err %v)", err)
	}
}

// TestNudgeShadowReaderForRouted proves the wait paths' reads come from the
// merged queue row on a routed city: live rows read as open queued shadows,
// acked rows surface their terminal stamps only through
// FindIncludingTerminal.
func TestNudgeShadowReaderForRouted(t *testing.T) {
	cityPath := writeNudgesRoutedCity(t)
	now := time.Now()
	item := newQueuedNudgeWithOptions("boot/dev", "Wait satisfied.", "wait", now, queuedNudgeOptions{ID: "wait-w1-e1-1"})
	if err := enqueueQueuedNudge(cityPath, item); err != nil {
		t.Fatalf("enqueueQueuedNudge: %v", err)
	}

	reader, err := nudgeShadowReaderFor(cityPath, nil, beads.NudgesStore{})
	if err != nil {
		t.Fatalf("nudgeShadowReaderFor: %v", err)
	}
	if _, ok := reader.(routedNudgeShadowReader); !ok {
		t.Fatalf("reader = %T, want routedNudgeShadowReader", reader)
	}
	shadow, ok, err := reader.Find("wait-w1-e1-1")
	if err != nil || !ok {
		t.Fatalf("Find = %v, %v; want the live record", ok, err)
	}
	if !shadow.Open || shadow.State != "queued" {
		t.Fatalf("live shadow = %+v, want open queued", shadow)
	}

	if err := ackQueuedNudgesWithOutcome(cityPath, []string{"wait-w1-e1-1"}, "injected", "", "provider-nudge-return"); err != nil {
		t.Fatalf("ackQueuedNudgesWithOutcome: %v", err)
	}
	if _, ok, err := reader.Find("wait-w1-e1-1"); err != nil || ok {
		t.Fatalf("Find after ack = %v, %v; want not found (terminal rows are history)", ok, err)
	}
	shadow, ok, err = reader.FindIncludingTerminal("wait-w1-e1-1")
	if err != nil || !ok {
		t.Fatalf("FindIncludingTerminal = %v, %v; want the terminal record", ok, err)
	}
	if shadow.Open || shadow.State != "injected" || shadow.CommitBoundary != "provider-nudge-return" {
		t.Fatalf("terminal shadow = %+v, want closed injected with commit boundary", shadow)
	}
}

// TestNudgeShadowReaderForUnrouted pins the bd shape: without the marker the
// reader is the shadow-bead front door over the caller's store.
func TestNudgeShadowReaderForUnrouted(t *testing.T) {
	reader, err := nudgeShadowReaderFor(t.TempDir(), nil, beads.NudgesStore{Store: beads.NewMemStore()})
	if err != nil {
		t.Fatalf("nudgeShadowReaderFor: %v", err)
	}
	if _, ok := reader.(*nudgequeue.Store); !ok {
		t.Fatalf("reader = %T, want *nudgequeue.Store", reader)
	}
}

// TestSweepStaleNudgeMailRoutedNudgeLeg proves the routed sweep's nudge leg
// is the merged queue's terminal-row retention: terminal rows past the TTL
// are deleted, live rows survive, and the dry-run count matches.
func TestSweepStaleNudgeMailRoutedNudgeLeg(t *testing.T) {
	cityPath := writeNudgesRoutedCity(t)
	class, err := nudgesdb.SharedStoreFor(cityPath)
	if err != nil {
		t.Fatalf("SharedStoreFor: %v", err)
	}
	now := time.Now()
	old := now.Add(-48 * time.Hour)

	// A terminal record aged past the retention TTL (the import primitive is
	// the one public surface that can carry a legacy terminal clock).
	if err := class.ImportTerminalShadow(nudgequeue.NudgeShadow{
		ID: "nudge-old", Agent: "boot/dev", Source: "session", Message: "old", State: "injected",
	}, old, old); err != nil {
		t.Fatalf("importing aged terminal record: %v", err)
	}
	liveItem := newQueuedNudgeWithOptions("boot/dev", "live", "session", now, queuedNudgeOptions{ID: "nudge-live"})
	if err := enqueueQueuedNudge(cityPath, liveItem); err != nil {
		t.Fatalf("enqueue live: %v", err)
	}

	routing, err := nudgeSweepRoutingFor(cityPath, nil)
	if err != nil {
		t.Fatalf("nudgeSweepRoutingFor: %v", err)
	}
	if routing.class == nil {
		t.Fatal("routing not active on a routed city")
	}

	mailStore := beads.MailStore{Store: beads.NewMemStore()}
	counts, err := countStaleNudgeMailRouted(routing, beads.NudgesStore{}, mailStore, nil, now, nudgeMailSweepDefaultNudgeTTL, nudgeMailSweepDefaultMailTTL, nudgeMailSweepCloseBudget)
	if err != nil {
		t.Fatalf("countStaleNudgeMailRouted: %v", err)
	}
	if counts.NudgeClosed != 1 {
		t.Fatalf("dry-run NudgeClosed = %d, want 1", counts.NudgeClosed)
	}

	result, err := sweepStaleNudgeMailRouted(routing, beads.NudgesStore{}, mailStore, nil, now, nudgeMailSweepDefaultNudgeTTL, nudgeMailSweepDefaultMailTTL, nudgeMailSweepCloseBudget)
	if err != nil {
		t.Fatalf("sweepStaleNudgeMailRouted: %v", err)
	}
	if result.NudgeClosed != 1 {
		t.Fatalf("NudgeClosed = %d, want 1", result.NudgeClosed)
	}
	if _, ok, err := class.FindRecordIncludingTerminal("nudge-old"); err != nil || ok {
		t.Fatalf("swept row still present = %v, %v", ok, err)
	}
	pending, _, _, err := listQueuedNudges(cityPath, "boot/dev", now)
	if err != nil {
		t.Fatalf("listQueuedNudges: %v", err)
	}
	if len(pending) != 1 || pending[0].ID != "nudge-live" {
		t.Fatalf("pending after sweep = %+v, want the live item", pending)
	}
}
