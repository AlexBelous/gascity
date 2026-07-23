package main

import (
	"bytes"
	"os"
	"testing"

	"github.com/gastownhall/gascity/internal/beads"
)

// TestEnsureGraphClassMigrated pins the boot cutover: open graph beads
// (both tiers) import with ids + within-graph deps preserved, closed and
// work beads never cross, the marker flips routing, and the residue sweep
// clears bd copies while sparing fresh unknown open beads.
func TestEnsureGraphClassMigrated(t *testing.T) {
	store := beads.NewMemStore()
	prevOpen := openGraphClassMigrationStore
	openGraphClassMigrationStore = func(_ string) (beads.Store, error) { return store, nil }
	t.Cleanup(func() { openGraphClassMigrationStore = prevOpen })

	root, err := store.Create(beads.Bead{Title: "wisp: run", Type: "molecule", Labels: []string{"gc:wisp"}})
	if err != nil {
		t.Fatal(err)
	}
	step, err := store.Create(beads.Bead{Title: "step", Type: "task", Labels: []string{"gc:wisp"}, ParentID: root.ID})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.DepAdd(step.ID, root.ID, "tracks"); err != nil {
		t.Fatal(err)
	}
	closedBead, err := store.Create(beads.Bead{Title: "old wisp", Type: "wisp"})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(closedBead.ID); err != nil {
		t.Fatal(err)
	}
	work, err := store.Create(beads.Bead{Title: "plain work", Type: "task"})
	if err != nil {
		t.Fatal(err)
	}

	cityPath := t.TempDir()
	cfg := sqliteGraphConfig()
	var log bytes.Buffer
	if !ensureGraphClassMigrated(cityPath, cfg, &log) {
		t.Fatalf("migration failed; log: %s", log.String())
	}
	if _, err := os.Stat(graphMigratedMarkerPath(cityPath)); err != nil {
		t.Fatalf("marker missing: %v", err)
	}
	if !ensureGraphClassMigrated(cityPath, cfg, &log) {
		t.Fatal("second call must short-circuit true on the marker")
	}

	class, err := graphClassStoreFor(cityPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{root.ID, step.ID} {
		if _, err := class.Get(id); err != nil {
			t.Fatalf("imported %s missing: %v", id, err)
		}
	}
	if _, err := class.Get(closedBead.ID); err == nil {
		t.Fatal("closed graph bead crossed; it must not")
	}
	if _, err := class.Get(work.ID); err == nil {
		t.Fatal("work bead crossed; it must not")
	}
	deps, err := class.DepList(step.ID, "down")
	if err != nil || len(deps) != 1 || deps[0].DependsOnID != root.ID {
		t.Fatalf("dep edge did not cross: %+v %v", deps, err)
	}

	// Residue sweep: class-owned open + closed bd copies deleted; work spared.
	deleted := sweepLegacyGraphResidue(cityPath, cfg, &log)
	if deleted != 3 {
		t.Fatalf("residue deleted = %d, want 3 (root, step, closed); log: %s", deleted, log.String())
	}
	if _, err := store.Get(work.ID); err != nil {
		t.Fatalf("work bead deleted by graph sweep: %v", err)
	}

	// A fresh unknown open bd graph bead is spared (grace) but imported.
	fresh, err := store.Create(beads.Bead{Title: "racing pour", Type: "wisp", Labels: []string{"gc:wisp"}})
	if err != nil {
		t.Fatal(err)
	}
	if deleted := sweepLegacyGraphResidue(cityPath, cfg, &log); deleted != 1 {
		// import-then-sweep: once imported it is class-owned, so it sweeps.
		t.Fatalf("second sweep deleted = %d, want 1 (imported racing pour)", deleted)
	}
	if _, err := class.Get(fresh.ID); err != nil {
		t.Fatalf("racing pour not merge-imported: %v", err)
	}
}

// TestEnsureGraphClassMigratedAbortsBeforeMarker proves an unopenable bd
// store aborts with no marker.
func TestEnsureGraphClassMigratedAbortsBeforeMarker(t *testing.T) {
	prevOpen := openGraphClassMigrationStore
	openGraphClassMigrationStore = func(_ string) (beads.Store, error) { return nil, os.ErrPermission }
	t.Cleanup(func() { openGraphClassMigrationStore = prevOpen })
	cityPath := t.TempDir()
	var log bytes.Buffer
	if ensureGraphClassMigrated(cityPath, sqliteGraphConfig(), &log) {
		t.Fatal("migration reported success with an unopenable bd store")
	}
	if _, err := os.Stat(graphMigratedMarkerPath(cityPath)); err == nil {
		t.Fatal("marker written despite aborted migration")
	}
}
