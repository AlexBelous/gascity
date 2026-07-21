package main

import (
	"bytes"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/beads"
	nudgesdb "github.com/gastownhall/gascity/internal/classdb/nudges"
	"github.com/gastownhall/gascity/internal/config"
	"github.com/gastownhall/gascity/internal/nudgequeue"
)

func sqliteNudgesConfig(t *testing.T) *config.City {
	t.Helper()
	cfg, err := config.Parse([]byte(`[workspace]
name = "routing-test"

[beads.classes.nudges]
backend = "sqlite"
`))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	return cfg
}

// legacyNudgeShadowBead builds a shadow bead in the shape the nudge codec
// writes (nudge_beads.go Save / Terminalize vocabulary).
func legacyNudgeShadowBead(beadID, nudgeID, state, status string, createdAt time.Time) beads.Bead {
	meta := map[string]string{
		"nudge_id":      nudgeID,
		"agent":         "boot/dev",
		"state":         state,
		"source":        "session",
		"message":       "wake up",
		"deliver_after": createdAt.UTC().Format(time.RFC3339),
		"expires_at":    createdAt.Add(nudgequeue.DefaultTTL).UTC().Format(time.RFC3339),
	}
	if nudgequeue.IsTerminalState(state) {
		meta["terminal_reason"] = "done"
		meta["commit_boundary"] = "provider-nudge-return"
		meta["terminal_at"] = createdAt.Add(time.Minute).UTC().Format(time.RFC3339)
	}
	return beads.Bead{
		ID:        beadID,
		Title:     "nudge:" + nudgeID,
		Type:      nudgeBeadType,
		Status:    status,
		CreatedAt: createdAt,
		Labels:    []string{nudgeBeadLabel, "agent:boot/dev", "nudge:" + nudgeID, "source:session"},
		Metadata:  meta,
	}
}

func stubNudgeMigrationStore(t *testing.T, store beads.Store) {
	t.Helper()
	prev := openNudgeClassMigrationStore
	openNudgeClassMigrationStore = func(string) (beads.Store, error) { return store, nil }
	t.Cleanup(func() { openNudgeClassMigrationStore = prev })
}

func seedLegacyNudgeQueue(t *testing.T, cityPath string, state nudgequeue.State) {
	t.Helper()
	if err := nudgequeue.WithState(cityPath, func(s *nudgequeue.State) error {
		*s = state
		return nil
	}); err != nil {
		t.Fatalf("seeding legacy nudge queue: %v", err)
	}
}

// TestEnsureNudgesClassMigratedImportsQueueAndShadows is the end-to-end
// seamless-upgrade proof: the first boot imports the live buckets and the
// ≤24h terminal shadow history, writes the marker, and flips routing; older
// shadows age out; open (non-terminal) shadows are not imported as records.
func TestEnsureNudgesClassMigratedImportsQueueAndShadows(t *testing.T) {
	now := time.Now().UTC()
	cityPath := t.TempDir()
	if err := os.WriteFile(cityPath+"/city.toml", []byte("[workspace]\nname = \"test\"\n\n[beads.classes.nudges]\nbackend = \"sqlite\"\n"), 0o644); err != nil {
		t.Fatalf("writing city.toml: %v", err)
	}
	cfg := sqliteNudgesConfig(t)

	pending := nudgequeue.Item{ID: "nudge-p", Agent: "boot/dev", Source: "session", Message: "p", CreatedAt: now.Add(-time.Minute), DeliverAfter: now.Add(-time.Minute), ExpiresAt: now.Add(nudgequeue.DefaultTTL), BeadID: "ga-shadow-open"}
	inFlight := nudgequeue.Item{ID: "nudge-i", Agent: "boot/dev", Source: "session", Message: "i", CreatedAt: now.Add(-time.Minute), DeliverAfter: now.Add(-time.Minute), ExpiresAt: now.Add(nudgequeue.DefaultTTL), ClaimedAt: now, LeaseUntil: now.Add(nudgequeue.ClaimLeaseTTL)}
	dead := nudgequeue.Item{ID: "nudge-d", Agent: "boot/dev", Source: "session", Message: "d", CreatedAt: now.Add(-time.Hour), DeliverAfter: now.Add(-time.Hour), ExpiresAt: now.Add(nudgequeue.DefaultTTL), DeadAt: now.Add(-time.Minute), LastError: "boom"}
	seedLegacyNudgeQueue(t, cityPath, nudgequeue.State{Pending: []nudgequeue.Item{pending}, InFlight: []nudgequeue.Item{inFlight}, Dead: []nudgequeue.Item{dead}})

	// The late-terminal shadow was CREATED beyond the 24h window but turned
	// terminal recently (expired at CreatedAt+TTL): its unfinalized wait
	// still needs the terminal record, so the import keys on the terminal
	// clock too.
	lateTerminal := legacyNudgeShadowBead("ga-shadow-late", "nudge-late-terminal", "expired", "closed", now.Add(-30*time.Hour))
	lateTerminal.Metadata["terminal_at"] = now.Add(-time.Hour).UTC().Format(time.RFC3339)
	store := legacyStoreFrom(t, []beads.Bead{
		legacyNudgeShadowBead("ga-shadow-term", "wait-w1-e1-1", "injected", "closed", now.Add(-2*time.Hour)),
		legacyNudgeShadowBead("ga-shadow-old", "nudge-ancient", "injected", "closed", now.Add(-48*time.Hour)),
		lateTerminal,
		legacyNudgeShadowBead("ga-shadow-open", "nudge-p", "queued", "open", now.Add(-time.Minute)),
	})
	stubNudgeMigrationStore(t, store)

	var log bytes.Buffer
	if !ensureNudgesClassMigrated(cityPath, cfg, &log) {
		t.Fatalf("ensureNudgesClassMigrated = false; log: %s", log.String())
	}
	if _, err := os.Stat(nudgesdb.MigratedMarkerPath(cityPath)); err != nil {
		t.Fatalf("migrated marker missing: %v", err)
	}
	// Second call short-circuits on the marker.
	if !ensureNudgesClassMigrated(cityPath, cfg, &log) {
		t.Fatal("ensureNudgesClassMigrated(again) = false, want marker short-circuit")
	}

	class, err := nudgesdb.SharedStoreFor(cityPath)
	if err != nil {
		t.Fatalf("SharedStoreFor: %v", err)
	}
	snap, err := class.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if len(snap.Pending) != 1 || len(snap.InFlight) != 1 || len(snap.Dead) != 1 {
		t.Fatalf("imported buckets = %d/%d/%d, want 1/1/1", len(snap.Pending), len(snap.InFlight), len(snap.Dead))
	}
	if snap.Pending[0].BeadID != "ga-shadow-open" {
		t.Fatalf("imported pending lost BeadID: %+v", snap.Pending[0])
	}

	rec, ok, err := class.FindRecordIncludingTerminal("wait-w1-e1-1")
	if err != nil || !ok {
		t.Fatalf("terminal shadow not imported: found=%v err=%v", ok, err)
	}
	if rec.TerminalState != "injected" || rec.CommitBoundary != "provider-nudge-return" {
		t.Fatalf("imported terminal record = %+v, want injected stamps", rec)
	}
	if _, ok, _ := class.FindRecordIncludingTerminal("nudge-ancient"); ok {
		t.Fatal("shadow older than the 24h TTL was imported")
	}
	rec, ok, err = class.FindRecordIncludingTerminal("nudge-late-terminal")
	if err != nil || !ok {
		t.Fatalf("late-terminal shadow (old CreatedAt, recent TerminalAt) not imported: found=%v err=%v", ok, err)
	}
	if rec.TerminalState != "expired" {
		t.Fatalf("late-terminal record = %+v, want expired stamps", rec)
	}

	// The routed front door now reads the imported queue.
	pendingList, _, _, err := listQueuedNudges(cityPath, "boot/dev", now)
	if err != nil {
		t.Fatalf("listQueuedNudges: %v", err)
	}
	if len(pendingList) != 1 || pendingList[0].ID != "nudge-p" {
		t.Fatalf("routed pending = %+v, want the imported item", pendingList)
	}
}

// TestEnsureNudgesClassMigratedFreshCity proves a city with no legacy queue
// flips straight to sqlite: zero imports, marker written.
func TestEnsureNudgesClassMigratedFreshCity(t *testing.T) {
	stubNudgeMigrationStore(t, beads.NewMemStore())
	cityPath := t.TempDir()
	var log bytes.Buffer
	cfg := sqliteNudgesConfig(t)
	if !ensureNudgesClassMigrated(cityPath, cfg, &log) {
		t.Fatalf("fresh-city migration failed; log: %s", log.String())
	}
	if _, err := os.Stat(nudgesdb.MigratedMarkerPath(cityPath)); err != nil {
		t.Fatalf("migrated marker missing: %v", err)
	}
	routed, err := nudgesdb.Routed(cityPath, cfg)
	if err != nil || !routed {
		t.Fatalf("Routed = (%v, %v), want routed fresh city", routed, err)
	}
}

// TestEnsureNudgesClassMigratedAbortsWhenStoreUnopenable proves a shadow
// store open failure aborts BEFORE the marker: the ≤24h terminal history
// would otherwise be lost and in-flight waits would wedge.
func TestEnsureNudgesClassMigratedAbortsWhenStoreUnopenable(t *testing.T) {
	prev := openNudgeClassMigrationStore
	openNudgeClassMigrationStore = func(string) (beads.Store, error) {
		return nil, fmt.Errorf("shadow store unavailable")
	}
	t.Cleanup(func() { openNudgeClassMigrationStore = prev })

	cityPath := t.TempDir()
	var log bytes.Buffer
	if ensureNudgesClassMigrated(cityPath, sqliteNudgesConfig(t), &log) {
		t.Fatal("migration reported success with an unopenable shadow store")
	}
	if _, err := os.Stat(nudgesdb.MigratedMarkerPath(cityPath)); err == nil {
		t.Fatal("marker written despite aborted migration")
	}
}

// TestEnsureNudgesClassMigratedNoopOnBD proves a bd-backed city never
// migrates.
func TestEnsureNudgesClassMigratedNoopOnBD(t *testing.T) {
	cityPath := t.TempDir()
	var log bytes.Buffer
	if ensureNudgesClassMigrated(cityPath, nil, &log) {
		t.Fatal("nil-config city migrated")
	}
	if ensureNudgesClassMigrated(cityPath, &config.City{}, &log) {
		t.Fatal("bd-default city migrated")
	}
}

// TestSweepLegacyNudgeResidue proves the post-migration sweep clears the
// imported file items and the bd shadow beads while sparing a fresh open
// shadow the class store does not own (mixed-version grace).
func TestSweepLegacyNudgeResidue(t *testing.T) {
	now := time.Now().UTC()
	cityPath := t.TempDir()
	cfg := sqliteNudgesConfig(t)

	imported := nudgequeue.Item{ID: "nudge-p", Agent: "boot/dev", Source: "session", Message: "p", CreatedAt: now.Add(-time.Minute), DeliverAfter: now.Add(-time.Minute), ExpiresAt: now.Add(nudgequeue.DefaultTTL)}
	seedLegacyNudgeQueue(t, cityPath, nudgequeue.State{Pending: []nudgequeue.Item{imported}})

	store := legacyStoreFrom(t, []beads.Bead{
		// Terminal shadow: always residue once migrated.
		legacyNudgeShadowBead("ga-shadow-term", "wait-w1-e1-1", "injected", "closed", now.Add(-2*time.Hour)),
		// Open shadow of the imported item: class store owns the id — residue.
		legacyNudgeShadowBead("ga-shadow-open", "nudge-p", "queued", "open", now.Add(-time.Minute)),
		// Fresh open shadow of an UNKNOWN item (old-binary enqueue after the
		// straggler pass): spared by the grace window.
		legacyNudgeShadowBead("ga-shadow-fresh", "nudge-unknown", "queued", "open", now.Add(-time.Minute)),
		// Stale open shadow of an unknown item: past the grace — residue.
		legacyNudgeShadowBead("ga-shadow-stale", "nudge-stale", "queued", "open", now.Add(-time.Hour)),
	})
	stubNudgeMigrationStore(t, store)

	var log bytes.Buffer
	if !ensureNudgesClassMigrated(cityPath, cfg, &log) {
		t.Fatalf("migration failed; log: %s", log.String())
	}

	removed := sweepLegacyNudgeResidue(cityPath, cfg, &log)
	// 1 file item + 3 shadow beads (terminal, imported-open, stale-open).
	if removed != 4 {
		t.Fatalf("residue removed = %d, want 4; log: %s", removed, log.String())
	}
	state, err := nudgequeue.LoadState(cityPath)
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	if len(state.Pending)+len(state.InFlight)+len(state.Dead) != 0 {
		t.Fatalf("file queue not cleared: %+v", state)
	}
	if _, err := store.Get("ga-shadow-fresh"); err != nil {
		t.Fatalf("fresh unknown open shadow deleted — the mixed-version grace must spare it: %v", err)
	}
	for _, beadID := range []string{"ga-shadow-term", "ga-shadow-open", "ga-shadow-stale"} {
		if _, err := store.Get(beadID); err == nil {
			t.Fatalf("shadow bead %s survived the residue sweep", beadID)
		}
	}

	// The sweep converges: a second pass finds nothing new.
	if again := sweepLegacyNudgeResidue(cityPath, cfg, &log); again != 0 {
		t.Fatalf("second residue sweep removed %d, want 0", again)
	}
}

// TestSweepLegacyNudgeResidueImportsStragglers pins the documented
// import-then-sweep: an item that landed in state.json AFTER the marker
// flip (an enqueue racing the migration, or a mixed-version old binary's
// append) is merged into the class store by a later boot's residue sweep
// and cleared from the file — never stranded.
func TestSweepLegacyNudgeResidueImportsStragglers(t *testing.T) {
	now := time.Now().UTC()
	cityPath := t.TempDir()
	cfg := sqliteNudgesConfig(t)
	stubNudgeMigrationStore(t, beads.NewMemStore())

	var log bytes.Buffer
	if !ensureNudgesClassMigrated(cityPath, cfg, &log) {
		t.Fatalf("migration failed; log: %s", log.String())
	}

	// A post-marker file-backend append (the race / mixed-version shape).
	straggler := nudgequeue.Item{ID: "nudge-straggler", Agent: "boot/dev", Source: "session", Message: "s", CreatedAt: now, DeliverAfter: now, ExpiresAt: now.Add(nudgequeue.DefaultTTL)}
	seedLegacyNudgeQueue(t, cityPath, nudgequeue.State{Pending: []nudgequeue.Item{straggler}})

	if removed := sweepLegacyNudgeResidue(cityPath, cfg, &log); removed != 1 {
		t.Fatalf("residue sweep removed %d, want the 1 merged file item; log: %s", removed, log.String())
	}
	class, err := nudgesdb.SharedStoreFor(cityPath)
	if err != nil {
		t.Fatalf("SharedStoreFor: %v", err)
	}
	rec, ok, err := class.FindRecord("nudge-straggler")
	if err != nil || !ok || rec.QueueState != "pending" {
		t.Fatalf("straggler not merged into the class store: %+v (%v, %v)", rec, ok, err)
	}
	state, err := nudgequeue.LoadState(cityPath)
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	if len(state.Pending) != 0 {
		t.Fatalf("straggler left in the file queue: %+v", state.Pending)
	}
}

// nudgeShadowListErrorStore fails List so the shadow-history leg of a
// migration attempt aborts after the live import committed.
type nudgeShadowListErrorStore struct {
	beads.Store
	err error
}

func (s nudgeShadowListErrorStore) List(beads.ListQuery) ([]beads.Bead, error) { return nil, s.err }

// TestEnsureNudgesClassMigratedRetryConvergesToFileTruth pins the
// abort-retry contract: an interrupted first attempt leaves committed live
// rows; the still-file-backed city then delivers one of them (removing it
// from state.json, terminalizing its shadow); the retry must NOT resurrect
// the delivered item as pending — it re-syncs the live set to the file's
// current truth and re-imports the terminal record from the shadow.
func TestEnsureNudgesClassMigratedRetryConvergesToFileTruth(t *testing.T) {
	now := time.Now().UTC()
	cityPath := t.TempDir()
	cfg := sqliteNudgesConfig(t)

	delivered := nudgequeue.Item{ID: "nudge-delivered", Agent: "boot/dev", Source: "session", Message: "d", CreatedAt: now.Add(-time.Minute), DeliverAfter: now.Add(-time.Minute), ExpiresAt: now.Add(nudgequeue.DefaultTTL)}
	pending := nudgequeue.Item{ID: "nudge-keep", Agent: "boot/dev", Source: "session", Message: "k", CreatedAt: now.Add(-time.Minute), DeliverAfter: now.Add(-time.Minute), ExpiresAt: now.Add(nudgequeue.DefaultTTL)}
	seedLegacyNudgeQueue(t, cityPath, nudgequeue.State{Pending: []nudgequeue.Item{delivered, pending}})

	// Attempt 1: the live buckets import, then the shadow-history read fails
	// — rows are committed but no marker is written.
	failing := nudgeShadowListErrorStore{Store: beads.NewMemStore(), err: fmt.Errorf("shadow store listing unavailable")}
	stubNudgeMigrationStore(t, failing)
	var log bytes.Buffer
	if ensureNudgesClassMigrated(cityPath, cfg, &log) {
		t.Fatal("attempt 1 reported success despite the shadow-listing failure")
	}
	if _, err := os.Stat(nudgesdb.MigratedMarkerPath(cityPath)); err == nil {
		t.Fatal("marker written by the aborted attempt")
	}

	// The still-file-backed city delivers one item: it leaves state.json and
	// its shadow terminalizes.
	seedLegacyNudgeQueue(t, cityPath, nudgequeue.State{Pending: []nudgequeue.Item{pending}})
	store := legacyStoreFrom(t, []beads.Bead{
		legacyNudgeShadowBead("ga-shadow-del", "nudge-delivered", "injected", "closed", now.Add(-time.Minute)),
	})
	stubNudgeMigrationStore(t, store)

	// Attempt 2 succeeds and converges to the file's current truth.
	if !ensureNudgesClassMigrated(cityPath, cfg, &log) {
		t.Fatalf("retry failed; log: %s", log.String())
	}
	class, err := nudgesdb.SharedStoreFor(cityPath)
	if err != nil {
		t.Fatalf("SharedStoreFor: %v", err)
	}
	if rec, ok, err := class.FindRecord("nudge-delivered"); err != nil || ok {
		t.Fatalf("delivered item resurrected as live: %+v (%v, %v)", rec, ok, err)
	}
	rec, ok, err := class.FindRecordIncludingTerminal("nudge-delivered")
	if err != nil || !ok || rec.TerminalState != "injected" {
		t.Fatalf("delivered item's terminal record missing after retry: %+v (%v, %v)", rec, ok, err)
	}
	rec, ok, err = class.FindRecord("nudge-keep")
	if err != nil || !ok || rec.QueueState != "pending" {
		t.Fatalf("undelivered item lost by the retry: %+v (%v, %v)", rec, ok, err)
	}
}
