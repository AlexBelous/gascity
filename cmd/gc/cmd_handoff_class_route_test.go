package main

import (
	"bytes"
	"errors"
	"regexp"
	"testing"

	"github.com/gastownhall/gascity/internal/beads"
)

var handoffMailIDPattern = regexp.MustCompile(`sent auto mail (\S+)`)

// TestHandoffMailWritesTheBindingOnAMigratedCity is the handoff twin of
// TestCLIMailWritesAndReadsTheBindingOnAMigratedCity, and it pins the same
// defect on the one root that was left behind when the session arm was routed.
//
// `gc handoff` builds its mail provider itself — beadmail.NewWithStores, so it
// can reach SendHandoff's thread label — and for a while that construction took
// the routed sessions store for addressing and the UNROUTED work store for the
// message bead. On a migrated city that writes the handoff into the ledger the
// messaging class was moved off: the send succeeds, `gc mail check` reads the
// binding, and the handoff is never delivered.
//
// It drives cmdHandoff rather than createHandoffMail because the defect lives
// at the ROOT — which store the command derives and threads down — and a test
// that hands a routed store in would pass with the root still unrouted. The
// two assertions are that one defect from both sides: the bead IS in the
// binding, and it is NOT in the retained work store.
func TestHandoffMailWritesTheBindingOnAMigratedCity(t *testing.T) {
	cityPath, cfg := migratedOneShotCLICity(t)
	captureCLIStorageStderr(t)
	t.Setenv("GC_ALIAS", "worker")
	t.Setenv("GC_SESSION_NAME", "gc-worker")

	var stdout, stderr bytes.Buffer
	// --auto: the handoff send without the restart request, so the assertion is
	// about the message bead and nothing else.
	if code := cmdHandoff([]string{"context cycle"}, "", true, "", &stdout, &stderr); code != 0 {
		t.Fatalf("gc handoff --auto exited %d: %s", code, stderr.String())
	}
	match := handoffMailIDPattern.FindStringSubmatch(stdout.String())
	if match == nil {
		t.Fatalf("could not find the handoff mail id in %q", stdout.String())
	}
	msgID := match[1]

	// The funnel's own handle goes first, so the assertions below read durable
	// bytes rather than state an open connection is holding.
	if err := closeCLIStorageRoutes(); err != nil {
		t.Fatalf("closing the one-shot routes: %v", err)
	}
	binding := openMigratedDestination(t, mustResolveInfraTarget(t, cityPath, cfg))
	if _, err := binding.Get(msgID); err != nil {
		t.Errorf("the handoff message did not land in the binding: %v", err)
	}
	work, err := openCityStoreAt(cityPath)
	if err != nil {
		t.Fatalf("opening the retained work store: %v", err)
	}
	t.Cleanup(func() { _ = closeBeadStoreHandle(work) })
	if _, err := work.Get(msgID); err == nil {
		t.Errorf("the handoff message also landed in the work store as %s; a relocated class must be served from its binding only", msgID)
	} else if !errors.Is(err, beads.ErrNotFound) {
		t.Errorf("reading the work store for %s: %v", msgID, err)
	}
}
