package main

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/beads"
	sessionsdb "github.com/gastownhall/gascity/internal/classdb/sessions"
	sessionscfg "github.com/gastownhall/gascity/internal/config"
	"github.com/gastownhall/gascity/internal/session"
)

// failingListStore makes every List fail — the import must abort before
// the marker.
type failingListStore struct {
	beads.Store
}

func (f failingListStore) List(beads.ListQuery) ([]beads.Bead, error) {
	return nil, errors.New("bd unavailable")
}

func overrideSessionsMigrationStore(t *testing.T, store beads.Store) {
	t.Helper()
	prev := openSessionsClassMigrationStore
	openSessionsClassMigrationStore = func(string) (beads.Store, error) { return store, nil }
	t.Cleanup(func() { openSessionsClassMigrationStore = prev })
}

func seedLegacySessionCity(t *testing.T, bd beads.Store) (open, closedRecent, wait string) {
	t.Helper()
	front := session.NewStore(beads.SessionStore{Store: bd})
	openInfo, err := front.CreateSessionInfo(session.CreateSpec{
		Title: "live", AgentName: "live",
		Metadata: map[string]string{"state": "awake", "session_name": "gc-live", "generation": "4"},
	})
	if err != nil {
		t.Fatal(err)
	}
	w, err := front.CreateWait(session.WaitSpec{
		SessionID: openInfo.ID, Kind: "deps",
		DepIDs: []string{"gc-777"}, DepMode: "all", Note: "n", Now: time.Now().UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	closedInfo, err := front.CreateSessionInfo(session.CreateSpec{Title: "done", AgentName: "done", Metadata: map[string]string{}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := front.Close(closedInfo.ID, "done", time.Now()); err != nil {
		t.Fatal(err)
	}
	// A work bead that must never cross.
	if _, err := bd.Create(beads.Bead{Title: "job", Type: "task"}); err != nil {
		t.Fatal(err)
	}
	return openInfo.ID, closedInfo.ID, w.ID
}

func TestEnsureSessionsClassMigratedFreshCityFlipsImmediately(t *testing.T) {
	city := t.TempDir()
	overrideSessionsMigrationStore(t, beads.NewMemStore())
	var stderr strings.Builder
	if !ensureSessionsClassMigrated(city, sqliteSessionsCityConfig(), &stderr) {
		t.Fatalf("fresh city did not migrate: %s", stderr.String())
	}
	routed, err := sessionsdb.Routed(city, sqliteSessionsCityConfig())
	if err != nil || !routed {
		t.Fatalf("marker missing after fresh flip: routed=%v err=%v", routed, err)
	}
	// Idempotent: the second boot short-circuits on the marker.
	if !ensureSessionsClassMigrated(city, sqliteSessionsCityConfig(), &stderr) {
		t.Fatal("second boot did not report migrated")
	}
	// bd-backend config never migrates.
	other := t.TempDir()
	if ensureSessionsClassMigrated(other, &sessionscfg.City{}, &stderr) {
		t.Fatal("bd-backend city migrated")
	}
}

func TestSessionsMigrationImportsBdTruthWithAgeDrops(t *testing.T) {
	city := t.TempDir()
	bd := beads.NewMemStore()
	openID, closedRecentID, waitID := seedLegacySessionCity(t, bd)
	// An aged-out closed session: closed with an ancient clock via import
	// shaping (memstore stamps clocks itself, so plant the aged row through
	// a raw create + manual sweepable state on the class side is not
	// possible — instead pin the drop rule directly below).
	overrideSessionsMigrationStore(t, bd)

	var stderr strings.Builder
	if !ensureSessionsClassMigrated(city, sqliteSessionsCityConfig(), &stderr) {
		t.Fatalf("migration failed: %s", stderr.String())
	}
	class, err := sessionsdb.SharedStoreFor(city)
	if err != nil {
		t.Fatal(err)
	}
	// Ids preserved; the full metadata map survives; the wait rides along.
	front := session.NewStore(beads.SessionStore{Store: class})
	info, err := front.Get(openID)
	if err != nil {
		t.Fatalf("open session missing after import: %v", err)
	}
	if info.MetadataState != "awake" || info.SessionName != "gc-live" || info.Generation != "4" {
		t.Fatalf("imported session lost state: %+v", info)
	}
	w, err := front.GetWait(waitID)
	if err != nil || w.State != "pending" || len(w.DepIDs) != 1 {
		t.Fatalf("imported wait: %+v %v", w, err)
	}
	if _, closed, err := front.GetState(closedRecentID); err != nil || !closed {
		t.Fatalf("recent closed session missing: closed=%v err=%v", closed, err)
	}
	// The work bead never crossed.
	rows, err := class.List(beads.ListQuery{AllowScan: true, IncludeClosed: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, b := range rows {
		if b.Type == "task" && !session.IsWaitBeadType(b.Type) {
			t.Fatalf("work bead crossed into the sessions store: %+v", b)
		}
	}

	// The age-drop rule: closed past the TTL drops, open never drops.
	aged := beads.Bead{Status: "closed", UpdatedAt: time.Now().Add(-8 * 24 * time.Hour)}
	if !sessionsImportDropsBead(aged, time.Now().Add(-sessionsClosedRetentionTTL)) {
		t.Fatal("aged closed row not dropped")
	}
	openBead := beads.Bead{Status: "open", CreatedAt: time.Now().Add(-365 * 24 * time.Hour)}
	if sessionsImportDropsBead(openBead, time.Now().Add(-sessionsClosedRetentionTTL)) {
		t.Fatal("open row dropped — open rows always import")
	}
}

func TestSessionsMigrationRetryDoesNotResurrect(t *testing.T) {
	city := t.TempDir()
	bd := beads.NewMemStore()
	openID, _, waitID := seedLegacySessionCity(t, bd)
	overrideSessionsMigrationStore(t, bd)
	class, err := sessionsdb.SharedStoreFor(city)
	if err != nil {
		t.Fatal(err)
	}

	// First attempt imports but dies BEFORE the marker.
	if _, err := migrateSessionsIntoClassStore(class, bd, time.Now()); err != nil {
		t.Fatal(err)
	}
	// The still-bd city moves on: the session closes, its wait finalizes.
	bdFront := session.NewStore(beads.SessionStore{Store: bd})
	if _, err := bdFront.Close(openID, "done", time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := bdFront.CloseWaitFromNudge(waitID, time.Now(), "wait-n-1", "b"); err != nil {
		t.Fatal(err)
	}

	// The retry re-syncs to the bd truth: no resurrection of the open row.
	var stderr strings.Builder
	if !ensureSessionsClassMigrated(city, sqliteSessionsCityConfig(), &stderr) {
		t.Fatalf("retry failed: %s", stderr.String())
	}
	front := session.NewStore(beads.SessionStore{Store: class})
	if _, closed, err := front.GetState(openID); err != nil || !closed {
		t.Fatalf("retry resurrected a closed session: closed=%v err=%v", closed, err)
	}
	w, err := front.GetWait(waitID)
	if err != nil || w.State != "closed" || w.NudgeID != "wait-n-1" {
		t.Fatalf("retry resurrected a finalized wait: %+v %v", w, err)
	}
}

func TestSessionsMigrationAbortsBeforeMarkerOnFailure(t *testing.T) {
	city := t.TempDir()
	overrideSessionsMigrationStore(t, failingListStore{Store: beads.NewMemStore()})
	var stderr strings.Builder
	if ensureSessionsClassMigrated(city, sqliteSessionsCityConfig(), &stderr) {
		t.Fatal("migration reported success with a failing bd store")
	}
	if routed, err := sessionsdb.Routed(city, sqliteSessionsCityConfig()); err != nil || routed {
		t.Fatalf("marker written despite import failure: routed=%v err=%v", routed, err)
	}
	if !strings.Contains(stderr.String(), "bd unavailable") {
		t.Fatalf("failure not surfaced: %q", stderr.String())
	}
}

func TestSweepLegacySessionResidueImportsThenClears(t *testing.T) {
	city := t.TempDir()
	bd := beads.NewMemStore()
	openID, closedID, waitID := seedLegacySessionCity(t, bd)
	overrideSessionsMigrationStore(t, bd)
	var stderr strings.Builder
	if !ensureSessionsClassMigrated(city, sqliteSessionsCityConfig(), &stderr) {
		t.Fatalf("migration failed: %s", stderr.String())
	}
	// A session created AFTER the marker (raced write / old binary): the
	// sweep must import it before clearing anything.
	straggler, err := session.NewStore(beads.SessionStore{Store: bd}).CreateSessionInfo(
		session.CreateSpec{Title: "late", AgentName: "late", Metadata: map[string]string{"state": "awake"}})
	if err != nil {
		t.Fatal(err)
	}

	sweepLegacySessionResidue(city, sqliteSessionsCityConfig(), &stderr)

	class, err := sessionsdb.SharedStoreFor(city)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := class.Get(straggler.ID); err != nil {
		t.Fatalf("straggler not imported before the sweep: %v", err)
	}
	// bd is clear of session-class residue (work bead untouched).
	residue, err := sessionsdb.ExportSessionClassBeads(bd, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(residue) != 0 {
		t.Fatalf("bd residue remains after sweep: %v", residue)
	}
	for _, id := range []string{openID, closedID, waitID} {
		if _, err := class.Get(id); err != nil {
			t.Fatalf("class store lost %s after sweep: %v", id, err)
		}
	}
	if work, err := bd.List(beads.ListQuery{Type: "task", IncludeClosed: true}); err != nil || len(work) != 1 {
		t.Fatalf("work bead disturbed by the sweep: %v %v", work, err)
	}

	// The spare arm: a fresh open UNKNOWN row (not yet imported) survives.
	fresh := beads.Bead{ID: "gc-fresh", Status: "open", CreatedAt: time.Now()}
	if keep, err := spareSessionResidueBead(class, fresh, time.Now()); err != nil || !keep {
		t.Fatalf("fresh unknown open row not spared: keep=%v err=%v", keep, err)
	}
	agedOpen := beads.Bead{ID: "gc-aged", Status: "open", CreatedAt: time.Now().Add(-time.Hour)}
	if keep, err := spareSessionResidueBead(class, agedOpen, time.Now()); err != nil || keep {
		t.Fatalf("aged open row spared forever: keep=%v err=%v", keep, err)
	}
}
