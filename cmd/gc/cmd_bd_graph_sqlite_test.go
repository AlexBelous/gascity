package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/gastownhall/gascity/internal/beads"
)

// TestBdGraphSqliteMutationArm pins the in-process graph mutation arm: on a
// routed city, close (with --reason) and update (--set-metadata/--status/
// --assignee) on gcg ids land in the embedded graph store; unrouted cities
// and non-gcg ids fall through (handled=false) to the guard/exec path.
func TestBdGraphSqliteMutationArm(t *testing.T) {
	cityPath := t.TempDir()
	cfg := sqliteGraphConfig()
	var stdout, stderr bytes.Buffer

	// Unrouted (no marker): falls through.
	if _, handled := maybeRouteBdGraphSqliteMutation(cityPath, cfg, []string{"close", "gcg-1"}, &stdout, &stderr); handled {
		t.Fatal("unrouted city must fall through to the guard")
	}

	writeGraphMigratedMarker(t, cityPath)
	st, err := graphClassStoreFor(cityPath)
	if err != nil {
		t.Fatal(err)
	}
	b, err := st.Create(beads.Bead{Title: "step", Type: "task"})
	if err != nil {
		t.Fatal(err)
	}

	// Non-gcg ids fall through even routed.
	if _, handled := maybeRouteBdGraphSqliteMutation(cityPath, cfg, []string{"close", "gc-work-1"}, &stdout, &stderr); handled {
		t.Fatal("work-prefixed id must fall through")
	}
	// Reads fall through.
	if _, handled := maybeRouteBdGraphSqliteMutation(cityPath, cfg, []string{"show", b.ID}, &stdout, &stderr); handled {
		t.Fatal("show must fall through (served by the show federation)")
	}

	// update --set-metadata + --status.
	code, handled := maybeRouteBdGraphSqliteMutation(cityPath, cfg,
		[]string{"update", b.ID, "--set-metadata", "review.verdict=pass", "--status", "in_progress", "--assignee", "worker-1"}, &stdout, &stderr)
	if !handled || code != 0 {
		t.Fatalf("update = (%d, %v); stderr=%s", code, handled, stderr.String())
	}
	got, err := st.Get(b.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Metadata["review.verdict"] != "pass" || got.Status != "in_progress" || got.Assignee != "worker-1" {
		t.Fatalf("update did not land: %+v", got)
	}

	// close --reason.
	stdout.Reset()
	code, handled = maybeRouteBdGraphSqliteMutation(cityPath, cfg,
		[]string{"close", b.ID, "--reason", "step complete and verified today"}, &stdout, &stderr)
	if !handled || code != 0 {
		t.Fatalf("close = (%d, %v); stderr=%s", code, handled, stderr.String())
	}
	got, err = st.Get(b.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "closed" || got.Metadata["close_reason"] != "step complete and verified today" {
		t.Fatalf("close did not land: %+v", got)
	}
	if !strings.Contains(stdout.String(), "closed "+b.ID) {
		t.Fatalf("stdout %q", stdout.String())
	}

	// Unsupported flag is a loud error, not a silent exec.
	stderr.Reset()
	code, handled = maybeRouteBdGraphSqliteMutation(cityPath, cfg,
		[]string{"update", b.ID, "--claim"}, &stdout, &stderr)
	if !handled || code != 1 || !strings.Contains(stderr.String(), "not supported") {
		t.Fatalf("unsupported flag = (%d, %v, %q)", code, handled, stderr.String())
	}
}
