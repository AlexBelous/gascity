package main

import (
	"testing"

	"github.com/gastownhall/gascity/internal/beadmeta"
	"github.com/gastownhall/gascity/internal/beads"
)

// TestAppendGraphScaleTargets pins the demand federation: routed cities gain
// one graph probe per distinct template; unrouted cities are unchanged; and
// the appended target actually surfaces routed graph demand through
// defaultScaleCheckCounts.
func TestAppendGraphScaleTargets(t *testing.T) {
	work := beads.NewMemStore()
	targets := []defaultScaleCheckTarget{
		{template: "pool-a", store: work, storeKey: "city"},
		{template: "pool-a", store: work, storeKey: "rig:r1"},
		{template: "pool-b", store: work, storeKey: "city"},
	}

	// Unrouted: unchanged.
	if got := appendGraphScaleTargets(targets, t.TempDir(), nil); len(got) != 3 {
		t.Fatalf("unrouted city appended targets: %d", len(got))
	}

	cityPath := t.TempDir()
	writeGraphMigratedMarker(t, cityPath)
	cfg := sqliteGraphConfig()
	st, err := graphClassStoreFor(cityPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Create(beads.Bead{
		Title:    "routed step",
		Type:     "task",
		Metadata: map[string]string{beadmeta.RoutedToMetadataKey: "pool-b"},
	}); err != nil {
		t.Fatal(err)
	}

	got := appendGraphScaleTargets(targets, cityPath, cfg)
	if len(got) != 5 {
		t.Fatalf("routed targets = %d, want 5 (3 + one graph probe per distinct template)", len(got))
	}
	counts, _, errs := defaultScaleCheckCounts(got)
	if len(errs) != 0 {
		t.Fatalf("errs: %v", errs)
	}
	if counts["pool-b"] != 1 || counts["pool-a"] != 0 {
		t.Fatalf("counts = %v, want pool-b=1 from the graph store", counts)
	}
}
