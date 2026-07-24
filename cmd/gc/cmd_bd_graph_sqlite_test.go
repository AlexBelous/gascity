package main

import (
	"bytes"
	"context"
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

// TestBdGraphReleaseIfCurrentRouted pins the routed CAS release: released
// when held by the expected assignee, skipped otherwise.
func TestBdGraphReleaseIfCurrentRouted(t *testing.T) {
	cityPath := t.TempDir()
	writeGraphMigratedMarker(t, cityPath)
	cfg := sqliteGraphConfig()
	st, err := graphClassStoreFor(cityPath)
	if err != nil {
		t.Fatal(err)
	}
	b, err := st.Create(beads.Bead{Title: "held", Type: "task"})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok, err := st.Claim(b.ID, "w1"); err != nil || !ok {
		t.Fatalf("claim: %v %v", ok, err)
	}

	var stdout, stderr bytes.Buffer
	code, handled := maybeRouteBdGraphSqliteMutation(cityPath, cfg, []string{"release-if-current", b.ID, "w2"}, &stdout, &stderr)
	if !handled || code != 0 || !strings.Contains(stdout.String(), "skipped") {
		t.Fatalf("wrong-assignee release = (%d, %v, %q)", code, handled, stdout.String())
	}
	stdout.Reset()
	code, handled = maybeRouteBdGraphSqliteMutation(cityPath, cfg, []string{"release-if-current", b.ID, "w1"}, &stdout, &stderr)
	if !handled || code != 0 || !strings.Contains(stdout.String(), "released") {
		t.Fatalf("release = (%d, %v, %q) stderr=%s", code, handled, stdout.String(), stderr.String())
	}
	if got, _ := st.Get(b.ID); got.Assignee != "" {
		t.Fatalf("assignee not cleared: %+v", got)
	}
}

// TestBdGraphReadRefusal pins the fail-closed backstop: unfederated read
// shapes mentioning gcg ids refuse loudly on a routed city instead of
// exec'ing bd into false absence; unrouted cities and non-graph commands
// fall through.
func TestBdGraphReadRefusal(t *testing.T) {
	var stderr bytes.Buffer
	cityPath := t.TempDir()
	// Unrouted: fall through.
	if _, handled := bdGraphReadRefusal(cityPath, nil, []string{"dep", "list", "gcg-1"}, &stderr); handled {
		t.Fatal("unrouted city must fall through")
	}
	writeGraphMigratedMarker(t, cityPath)
	cfg := sqliteGraphConfig()
	for _, args := range [][]string{
		{"dep", "list", "gcg-1"},
		{"show", "gcg-1", "gcg-2"},
		{"show", "gcg-1", "--verbose"},
		{"list", "--parent", "gcg-9"},
	} {
		stderr.Reset()
		code, handled := bdGraphReadRefusal(cityPath, cfg, args, &stderr)
		if !handled || code != 1 || !strings.Contains(stderr.String(), "embedded graph store") {
			t.Fatalf("%v = (%d, %v, %q), want loud refusal", args, code, handled, stderr.String())
		}
	}
	if _, handled := bdGraphReadRefusal(cityPath, cfg, []string{"list", "--status", "open"}, &stderr); handled {
		t.Fatal("non-graph command must fall through")
	}
}

// TestBdGraphMutationLegacyIDRouting pins gap N08: a MIGRATED legacy-id
// graph bead (no gcg prefix, imported with its bd id preserved) routes
// through the in-process mutation arm by store ownership.
func TestBdGraphMutationLegacyIDRouting(t *testing.T) {
	cityPath := t.TempDir()
	writeGraphMigratedMarker(t, cityPath)
	cfg := sqliteGraphConfig()
	st, err := graphClassStoreFor(cityPath)
	if err != nil {
		t.Fatal(err)
	}
	legacy, err := st.CreateWithForeignID(beads.Bead{ID: "gc-legacy-step-7", Title: "migrated step", Type: "task"})
	if err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	code, handled := maybeRouteBdGraphSqliteMutation(cityPath, cfg,
		[]string{"update", legacy.ID, "--set-metadata", "review.verdict=pass"}, &stdout, &stderr)
	if !handled || code != 0 {
		t.Fatalf("legacy update = (%d, %v); stderr=%s", code, handled, stderr.String())
	}
	if got, _ := st.Get(legacy.ID); got.Metadata["review.verdict"] != "pass" {
		t.Fatalf("legacy update did not land: %+v", got)
	}

	// A work-store id (not graph-resident) still falls through to exec.
	if _, handled := maybeRouteBdGraphSqliteMutation(cityPath, cfg,
		[]string{"update", "gc-not-in-graph", "--status", "open"}, &stdout, &stderr); handled {
		t.Fatal("non-resident legacy id must fall through to the exec path")
	}

	// Claim seam honors ownership too.
	ops := graphRoutedHookClaimOps(cityPath, cfg)
	if _, ok, err := ops.Claim(context.Background(), t.TempDir(), nil, legacy.ID, "w1"); err != nil || !ok {
		t.Fatalf("legacy-id claim = (%v, %v)", ok, err)
	}
}
