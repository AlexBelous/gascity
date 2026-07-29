package main

import (
	"testing"

	"github.com/gastownhall/gascity/internal/beads"
	sessionsdb "github.com/gastownhall/gascity/internal/classdb/sessions"
	"github.com/gastownhall/gascity/internal/config"
)

// TestReleaseOrphanedPoolAssignments_SplitTopologySessionsClassStoreDefense
// pins the split-topology last defense: work assigned to a session whose bead
// lives ONLY in the routed sessions class store (fresh session racing the
// tick's snapshot) must NOT be released as orphaned.
func TestReleaseOrphanedPoolAssignments_SplitTopologySessionsClassStoreDefense(t *testing.T) {
	cityPath := t.TempDir()
	writeSessionsMigratedMarker(t, cityPath)

	// The session bead exists only in the routed class store — not in the
	// work store and not in the openSessionInfos snapshot.
	class, routed, err := sessionsdb.RoutedStoreFor(cityPath, sqliteSessionsCityConfig())
	if err != nil || !routed {
		t.Fatalf("RoutedStoreFor = (routed=%v, err=%v), want routed", routed, err)
	}
	assignee := "gc__implementation-worker-gcs-session-fresh1"
	if _, err := class.Create(beads.Bead{
		Title:  "gascity/gc.implementation-worker-1",
		Type:   "session",
		Labels: []string{"gc:session"},
		Metadata: map[string]string{
			"session_name": assignee,
			"state":        "awake",
		},
	}); err != nil {
		t.Fatal(err)
	}

	work := beads.NewMemStore()
	wb, err := work.Create(beads.Bead{Title: "Apply review fixes and set verdict", Type: "task", Metadata: map[string]string{
		"gc.routed_to": "gascity/gc.implementation-worker",
	}})
	if err != nil {
		t.Fatal(err)
	}
	if err := work.Update(wb.ID, beads.UpdateOpts{Assignee: &assignee}); err != nil {
		t.Fatal(err)
	}
	got, _ := work.Get(wb.ID)

	cfg := sqliteSessionsCityConfig()
	cfg.Agents = []config.Agent{{Name: "gc.implementation-worker", Dir: "gascity"}}

	released := releaseOrphanedPoolAssignments(work, cfg, cityPath, nil, []beads.Bead{got}, nil, nil, nil)
	if len(released) != 0 {
		t.Fatalf("split-topology fresh-session work was released as orphaned: %+v", released)
	}
}
