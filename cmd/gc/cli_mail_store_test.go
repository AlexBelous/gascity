package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/gastownhall/gascity/internal/beads"
)

// TestCLIHandoffMailLandsInTheBindingOnAMigratedCity is the S-1 defect, pinned
// on the same city shape `gc mail` is pinned on.
//
// `gc handoff` builds its provider with beadmail.NewWithStores(store, sessStore)
// and routed only the session leg. On a migrated city that put the handoff
// message bead in the retained work store while every reader — the controller,
// `gc mail check`, the auto-inject arm — looks at the binding. The handoff is
// sent and never delivered at the same time, which is the one failure a handoff
// cannot survive: the outgoing session is already gone.
func TestCLIHandoffMailLandsInTheBindingOnAMigratedCity(t *testing.T) {
	cityPath, cfg := migratedOneShotCLICity(t)
	captureCLIStorageStderr(t)
	// The auto arm is the one that reaches the write without a session provider,
	// so it is the arm this can drive through the real command root rather than
	// through a store handed in by the test.
	t.Setenv("GC_ALIAS", "mayor")
	t.Setenv("GC_SESSION_NAME", "s-gc-42")

	var stdout, stderr bytes.Buffer
	if code := cmdHandoff([]string{"context cycle", "drain now"}, "", true, "", &stdout, &stderr); code != 0 {
		t.Fatalf("gc handoff --auto exited %d: %s", code, stderr.String())
	}
	id := handoffMailIDFromOutput(t, stdout.String())

	// The funnel's own handle goes first, so what follows reads durable bytes
	// rather than state an open connection is holding.
	if err := closeCLIStorageRoutes(); err != nil {
		t.Fatalf("closing the one-shot routes: %v", err)
	}
	binding := openMigratedDestination(t, mustResolveInfraTarget(t, cityPath, cfg))
	if _, err := binding.Get(id); err != nil {
		t.Errorf("the handoff mail did not land in the binding: %v", err)
	}

	retained, err := openCityStoreAt(cityPath)
	if err != nil {
		t.Fatalf("opening the retained work store: %v", err)
	}
	t.Cleanup(func() { _ = closeBeadStoreHandle(retained) })
	if _, err := retained.Get(id); err == nil {
		t.Errorf("the handoff mail also landed in the work store as %s; a relocated class must be served from its binding only", id)
	} else if !errors.Is(err, beads.ErrNotFound) {
		t.Errorf("reading the retained work store for %s: %v", id, err)
	}
}

// handoffMailIDFromOutput pulls the mail id out of the auto arm's confirmation
// line, which is the only place the command reports what it wrote.
func handoffMailIDFromOutput(t *testing.T, out string) string {
	t.Helper()
	const marker = "sent auto mail "
	i := strings.Index(out, marker)
	if i < 0 {
		t.Fatalf("gc handoff --auto printed no mail id: %q", out)
	}
	id, _, _ := strings.Cut(strings.TrimSpace(out[i+len(marker):]), " ")
	if id == "" {
		t.Fatalf("gc handoff --auto printed an empty mail id: %q", out)
	}
	return id
}

// TestCLIMailMessagesStoreServesTheBinding is the unit under the end-to-end test
// above: the route itself, asserted without a command around it.
func TestCLIMailMessagesStoreServesTheBinding(t *testing.T) {
	cityPath, cfg := migratedOneShotCLICity(t)
	captureCLIStorageStderr(t)

	work, err := openCityStoreAt(cityPath)
	if err != nil {
		t.Fatalf("opening the city work store: %v", err)
	}
	t.Cleanup(func() { _ = closeBeadStoreHandle(work) })

	if got := cliMailMessagesStore(work, cfg, cityPath); got == work {
		t.Error("the messaging route returned the work store on a city that has migrated messaging onto its binding")
	}
}

// TestCLIMailMessagesStoreIsIdentityWhenNothingRelocates is the compatibility
// control. A city that authors no [storage] must get back the EXACT store value
// it passed in — one-shot callers assert optional capabilities on whatever comes
// back, so a wrapper here would change behavior on every city that has not
// migrated.
func TestCLIMailMessagesStoreIsIdentityWhenNothingRelocates(t *testing.T) {
	cityPath := oneShotCLICity(t, "")
	refuseInfraMigrationSource(t)
	captureCLIStorageStderr(t)

	work := beads.NewMemStore()
	if got := cliMailMessagesStore(work, nil, cityPath); got != beads.Store(work) {
		t.Errorf("the one-shot messaging route returned %p, want the work store %p", got, work)
	}
}

// handoffMessagingForbidden are the call shapes that hand a doHandoff* arm the
// raw city work store as its message-persistence leg. Each one is the S-1 defect
// written out: the first parameter of all three arms feeds nothing but
// createHandoffMail, so passing `store` there is passing an unrouted store to a
// messaging-class write.
var handoffMessagingForbidden = []string{
	"doHandoffAuto(store,",
	"doHandoffWithOutcome(store,",
	"doHandoffRemote(store,",
	"beadmail.NewWithStores(store,",
}

// TestHandoffRootsRouteMailThroughTheMessagingClassStore pins the routing at the
// two derivation points, which is the only place it can be pinned:
// createHandoffMail takes its store as a parameter, so no test of that function
// can tell a routed argument from an unrouted one. Mirrors
// TestSessionRelocationRootsRouteThroughSessionClassStore.
func TestHandoffRootsRouteMailThroughTheMessagingClassStore(t *testing.T) {
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	path := filepath.Join(filepath.Dir(currentFile), "cmd_handoff.go")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%q): %v", path, err)
	}
	content := string(data)
	for _, needle := range handoffMessagingForbidden {
		if strings.Contains(content, needle) {
			t.Errorf("cmd_handoff.go contains unrouted messaging-class write %q — the handoff roots must derive their message store through cliMailMessagesStore(store, cfg, cityPath) so a [beads.classes.messaging] relocation reaches them", needle)
		}
	}
	if strings.Count(content, "cliMailMessagesStore(") != 2 {
		t.Errorf("cmd_handoff.go calls cliMailMessagesStore( %d time(s), want 2 — one per command root (cmdHandoff, cmdHandoffRemote)", strings.Count(content, "cliMailMessagesStore("))
	}
}
