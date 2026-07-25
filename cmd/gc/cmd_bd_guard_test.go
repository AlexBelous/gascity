package main

import (
	"io"
	"os"
	"strings"
	"testing"

	"github.com/gastownhall/gascity/internal/beads"
	sessionsdb "github.com/gastownhall/gascity/internal/classdb/sessions"
	"github.com/gastownhall/gascity/internal/config"
)

// TestBdInfraWriteRefusalMutations pins the reserved-prefix arm: every bd
// mutation verb targeting a reserved class id is refused with a message
// naming the class and the gc replacement; work-prefixed ids pass.
func TestBdInfraWriteRefusalMutations(t *testing.T) {
	cases := []struct {
		args    []string
		refuse  bool
		mention string
	}{
		{[]string{"update", "gco-5", "--status", "closed"}, true, "gc order"},
		{[]string{"close", "gcs-12", "--reason", "done for the day today"}, true, "gc session"},
		{[]string{"delete", "gcn-3", "--force"}, true, "gc nudge"},
		{[]string{"reopen", "gcm-9"}, true, "gc mail"},
		{[]string{"update", "gcg-wisp-abc", "--claim"}, true, "gc mol"},
		{[]string{"release-if-current", "gcs-4", "worker-1"}, true, "gc session"},
		{[]string{"close", "gc-abc123"}, false, ""},
		{[]string{"update", "mc-77", "--priority", "1"}, false, ""},
		{[]string{"update", "gcodex-1"}, false, ""}, // prefix match is on "gco-", not "gco"
		{[]string{"show", "gco-5"}, false, ""},      // reads stay unguarded
		{[]string{"list", "--json"}, false, ""},
	}
	// A bare city routes no class store, so the residence arm is dark: the
	// non-reserved mutation cases pass through exactly as before.
	city := t.TempDir()
	for _, tc := range cases {
		msg, refuse := bdInfraWriteRefusal(city, nil, tc.args, io.Discard)
		if refuse != tc.refuse {
			t.Errorf("bdInfraWriteRefusal(%v) = (%q, %v), want refuse=%v", tc.args, msg, refuse, tc.refuse)
			continue
		}
		if refuse && !strings.Contains(msg, tc.mention) {
			t.Errorf("bdInfraWriteRefusal(%v) message %q does not name %q", tc.args, msg, tc.mention)
		}
	}
}

// TestBdInfraWriteRefusalRedirectsReclassifiedResidentID pins the G4 residence
// arm: a bd mutation of a reclassified mixed-prefix (mc-) infra bead now living
// in a routed class store is refused with the "use gc <X>" redirect, even though
// its id carries no reserved prefix.
func TestBdInfraWriteRefusalRedirectsReclassifiedResidentID(t *testing.T) {
	city := t.TempDir()
	class, err := sessionsdb.SharedStoreFor(city)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := class.ImportBead(beads.Bead{
		ID: "mc-333", Type: "session", Status: "open",
		Labels: []string{"gc:session"}, Title: "reclassified session",
	}); err != nil {
		t.Fatal(err)
	}
	writeSessionsMigratedMarker(t, city)

	msg, refuse := bdInfraWriteRefusal(city, sqliteSessionsCityConfig(), []string{"update", "mc-333", "--status", "closed"}, io.Discard)
	if !refuse {
		t.Fatalf("guard did not refuse a write to a reclassified resident infra id (msg=%q)", msg)
	}
	if !strings.Contains(msg, "gc session") {
		t.Fatalf("refusal does not redirect to gc session: %q", msg)
	}
}

// TestBdInfraWriteRefusalDarkOnNativeCity pins the G4 DARK path: on a city with
// no class store routed (marker absent), the residence arm never fires and a
// work-prefixed write passes through unrefused.
func TestBdInfraWriteRefusalDarkOnNativeCity(t *testing.T) {
	city := t.TempDir()
	if msg, refuse := bdInfraWriteRefusal(city, sqliteSessionsCityConfig(), []string{"update", "mc-404", "--status", "closed"}, io.Discard); refuse {
		t.Fatalf("guard refused a work id on a native city: %q", msg)
	}
}

// TestBdInfraWriteRefusalFallsThroughOnProbeError pins the advisory-only
// contract: when a routed class store's residence probe ERRORS (here the graph
// class store, routed but with a corrupt seq-floor sidecar), the guard degrades
// to bd passthrough for a non-reserved work id (logging, not refusing) — a
// transient class-store fault must not block unrelated work-bead mutations. A
// reserved-prefix id on the same city still refuses (the fail-closed data gate
// is unchanged).
func TestBdInfraWriteRefusalFallsThroughOnProbeError(t *testing.T) {
	city := t.TempDir()
	if err := writeGraphMigratedMarkerFile(city); err != nil {
		t.Fatal(err)
	}
	// A corrupt seq-floor sidecar makes graphClassStoreFor (and thus the residence
	// probe) fail closed while graph routing reads active.
	if err := os.WriteFile(graphSeqFloorPath(city), []byte("not-an-int\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := &config.City{Beads: config.BeadsConfig{Classes: map[string]config.BeadClassConfig{
		config.BeadClassGraph: {Backend: config.BeadsClassBackendSQLite},
	}}}

	var stderr strings.Builder
	if msg, refuse := bdInfraWriteRefusal(city, cfg, []string{"update", "mc-77", "--status", "closed"}, &stderr); refuse {
		t.Fatalf("guard refused a work id on a residence-probe error (must fall through): %q", msg)
	}
	if !strings.Contains(stderr.String(), "residence probe") {
		t.Fatalf("probe error was not logged before falling through: %q", stderr.String())
	}
	// The reserved-prefix data gate is unaffected by the same broken city.
	if _, refuse := bdInfraWriteRefusal(city, cfg, []string{"update", "gcs-1", "--status", "closed"}, &stderr); !refuse {
		t.Fatal("reserved-prefix id must still refuse (fail-closed) despite the broken graph store")
	}
}

// TestBdInfraWriteRefusalCreate pins the create arm: a create whose declared
// type/labels classify off ClassWork is refused; plain work creates pass.
func TestBdInfraWriteRefusalCreate(t *testing.T) {
	cases := []struct {
		args    []string
		refuse  bool
		mention string
	}{
		{[]string{"create", "hello", "-t", "message"}, true, "gc mail"},
		{[]string{"create", "hello", "--type=session"}, true, "gc session"},
		{[]string{"create", "x", "--labels", "order-tracking,extra"}, true, "gc order"},
		{[]string{"create", "x", "-l", "gc:nudge"}, true, "gc nudge"},
		{[]string{"create", "x", "-l", "gc:session"}, true, "gc session"},
		{[]string{"create", "x", "-l", "gc:wait"}, true, "gc session"},
		{[]string{"create", "x", "--labels=gc:extmsg-binding"}, true, "gc mail"},
		{[]string{"create", "w", "--wisp-type", "patrol"}, true, "gc mol"},
		{[]string{"create", "x", "-t", "convergence"}, true, "gc mol"},
		{[]string{"create", "normal task", "-t", "task", "-l", "sprint-1", "-p", "1"}, false, ""},
		{[]string{"create", "titled message", "-t", "task", "-d", "type message in prose"}, false, ""},
		// A value flag whose value looks like an infra label must not confuse
		// the walk: --assignee consumes its value.
		{[]string{"create", "x", "--assignee", "gc:nudge", "-t", "task"}, false, ""},
	}
	city := t.TempDir()
	for _, tc := range cases {
		msg, refuse := bdInfraWriteRefusal(city, nil, tc.args, io.Discard)
		if refuse != tc.refuse {
			t.Errorf("bdInfraWriteRefusal(%v) = (%q, %v), want refuse=%v", tc.args, msg, refuse, tc.refuse)
			continue
		}
		if refuse && !strings.Contains(msg, tc.mention) {
			t.Errorf("bdInfraWriteRefusal(%v) message %q does not name %q", tc.args, msg, tc.mention)
		}
	}
}
