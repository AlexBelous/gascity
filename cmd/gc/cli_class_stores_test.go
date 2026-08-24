package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/config"
	"github.com/gastownhall/gascity/internal/session"
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
	id := handoffMailIDFromOutput(t, stdout.String(), "sent auto mail ")

	assertHandoffMailResidesInTheBinding(t, cityPath, cfg, id)
}

// TestCLIHandoffLocalArmMailLandsInTheBindingOnAMigratedCity is the same defect
// on the arm the auto test cannot reach. cmdHandoff derives ONE msgStore and
// hands it to either doHandoffAuto or doHandoffWithOutcome, so today the two
// arms cannot disagree — but "today" is a property of one line of cmdHandoff,
// and the routed store is a parameter to both, which is exactly the shape that
// lets a later edit route one arm and not the other.
//
// A named on-demand session is the drivable shape: restartable is false, so the
// command sends the mail and returns without waiting on a controller.
func TestCLIHandoffLocalArmMailLandsInTheBindingOnAMigratedCity(t *testing.T) {
	cityPath, cfg := migratedOneShotCLICity(t)
	captureCLIStorageStderr(t)
	t.Setenv("GC_ALIAS", "mayor")
	t.Setenv("GC_SESSION_NAME", "mayor")
	seedNamedOnDemandSession(t, cityPath, cfg, "mayor")

	var stdout, stderr bytes.Buffer
	if code := cmdHandoff([]string{"context cycle"}, "", false, "", &stdout, &stderr); code != 0 {
		t.Fatalf("gc handoff exited %d: %s", code, stderr.String())
	}
	id := handoffMailIDFromOutput(t, stdout.String(), "sent mail ")

	assertHandoffMailResidesInTheBinding(t, cityPath, cfg, id)
}

// TestCLIHandoffRemoteArmMailLandsInTheBindingOnAMigratedCity covers the second
// derivation point. cmdHandoffRemote opens its own store and loads its own cfg,
// so its routing is genuinely independent of cmdHandoff's — nothing but this row
// stands between it and the S-1 defect.
func TestCLIHandoffRemoteArmMailLandsInTheBindingOnAMigratedCity(t *testing.T) {
	cityPath, cfg := migratedOneShotCLICity(t)
	captureCLIStorageStderr(t)
	t.Setenv("GC_MAIL", "")
	t.Setenv("GC_SESSION_ID", "")
	t.Setenv("GC_ALIAS", "sender")
	seedNamedOnDemandSession(t, cityPath, cfg, "sender")
	seedNamedOnDemandSession(t, cityPath, cfg, "recipient")

	var stdout, stderr bytes.Buffer
	if code := cmdHandoffRemote([]string{"context cycle"}, "recipient", &stdout, &stderr); code != 0 {
		t.Fatalf("gc handoff --target exited %d: %s %s", code, stdout.String(), stderr.String())
	}
	id := handoffMailIDFromOutput(t, stdout.String(), "sent mail ")

	assertHandoffMailResidesInTheBinding(t, cityPath, cfg, id)
}

// seedNamedOnDemandSession plants the session bead the handoff arms resolve
// through, in the SESSION-class store rather than the work store — on a migrated
// city those are different ledgers, and a bead seeded in the wrong one makes the
// command fail for a reason that has nothing to do with what is being asserted.
func seedNamedOnDemandSession(t *testing.T, cityPath string, cfg *config.City, alias string) {
	t.Helper()
	work, err := openCityStoreAt(cityPath)
	if err != nil {
		t.Fatalf("opening the city work store: %v", err)
	}
	t.Cleanup(func() { _ = closeBeadStoreHandle(work) })

	sess := cliSessionStore(work, cfg, cityPath)
	b, err := sess.Create(beads.Bead{
		Type:   session.BeadType,
		Labels: []string{session.LabelSession},
		Metadata: map[string]string{
			"alias":                    alias,
			"session_name":             alias,
			"configured_named_session": "true",
			"configured_named_mode":    "on_demand",
		},
	})
	if err != nil {
		t.Fatalf("seeding the %q session bead: %v", alias, err)
	}
	if b.ID == "" {
		t.Fatalf("the %q session bead was created with no id", alias)
	}
}

// assertHandoffMailResidesInTheBinding is the identity assertion the three rows
// above share: the message bead is in the store the messaging class was migrated
// onto, and is NOT in the work store the migration retained. Both halves are
// needed — "it is in the binding" alone passes on a co-resident write, which is
// the shape a handoff that routes its read but not its write produces.
func assertHandoffMailResidesInTheBinding(t *testing.T, cityPath string, cfg *config.City, id string) {
	t.Helper()
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

// handoffMailIDFromOutput pulls the mail id out of an arm's confirmation line,
// which is the only place the command reports what it wrote.
func handoffMailIDFromOutput(t *testing.T, out, marker string) string {
	t.Helper()
	i := strings.Index(out, marker)
	if i < 0 {
		t.Fatalf("gc handoff printed no mail id (marker %q): %q", marker, out)
	}
	id, _, _ := strings.Cut(strings.TrimSpace(out[i+len(marker):]), " ")
	if id == "" {
		t.Fatalf("gc handoff printed an empty mail id: %q", out)
	}
	return id
}

// TestCLIMailStoreServesTheBinding is the unit under the end-to-end test above:
// the route itself, asserted without a command around it.
func TestCLIMailStoreServesTheBinding(t *testing.T) {
	cityPath, cfg := migratedOneShotCLICity(t)
	captureCLIStorageStderr(t)

	work, err := openCityStoreAt(cityPath)
	if err != nil {
		t.Fatalf("opening the city work store: %v", err)
	}
	t.Cleanup(func() { _ = closeBeadStoreHandle(work) })

	if got := cliMailStore(work, cfg, cityPath).Store; got == work {
		t.Error("the messaging route returned the work store on a city that has migrated messaging onto its binding")
	}
}

// TestCLIMailStoreIsIdentityWhenNothingRelocates is the compatibility control. A
// city that authors no [storage] must get back the EXACT store value it passed
// in — one-shot callers assert optional capabilities on whatever comes back, so
// a wrapper here would change behavior on every city that has not migrated.
//
// The assertion is on the EMBEDDED store, not on the beads.MailStore around it.
// The wrapper is always a new value, so a row that compared the wrapper would
// pass on nothing at all.
func TestCLIMailStoreIsIdentityWhenNothingRelocates(t *testing.T) {
	cityPath := oneShotCLICity(t, "")
	refuseInfraMigrationSource(t)
	captureCLIStorageStderr(t)

	work := beads.NewMemStore()
	if got := cliMailStore(work, nil, cityPath).Store; got != beads.Store(work) {
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

// TestHandoffRootsRouteMailThroughTheMessagingClassStore is the TRIPWIRE, not
// the pin. What each existing root actually writes is asserted by store identity
// on a migrated city (TestCLIHandoff{,Local,Remote}Arm...), and those rows are
// what would go red if a root stopped routing. This scan covers the one thing
// they structurally cannot: a root that does not exist yet. A fourth arm added
// with the raw store passed straight through gets no behavioral row until
// someone writes one, so the source shape is guarded here.
//
// Mirrors TestSessionRelocationRootsRouteThroughSessionClassStore.
func TestHandoffRootsRouteMailThroughTheMessagingClassStore(t *testing.T) {
	content := handoffCommandSource(t)
	for _, needle := range handoffMessagingForbidden {
		if strings.Contains(content, needle) {
			t.Errorf("cmd_handoff.go contains unrouted messaging-class write %q — the handoff roots must derive their message store through cliMailStore(store, cfg, cityPath).Store so a [beads.classes.messaging] relocation reaches them", needle)
		}
	}
	calls := strings.Count(content, "cliMailStore(")
	if calls != 2 {
		t.Errorf("cmd_handoff.go calls cliMailStore( %d time(s), want 2 — one per command root (cmdHandoff, cmdHandoffRemote); a third root needs its own store-identity row beside the two above, not just this scan", calls)
	}
	// Every one of those calls must unwrap. beads.MailStore embeds beads.Store,
	// so handing the wrapper to doHandoff* compiles and delivers mail correctly
	// — and moves every optional-capability assertion downstream onto the
	// wrapper, which answers no to all of them. Nothing else in the tree fails
	// on that, so the shape is pinned here.
	if unwrapped := len(mailStoreUnwrapCall.FindAllString(content, -1)); unwrapped != calls {
		t.Errorf("cmd_handoff.go unwraps %d of its %d cliMailStore( call(s); the messaging leg of beadmail.NewWithStores is a beads.Store and must be the embedded store, not the typed wrapper around it", unwrapped, calls)
	}
}

// mailStoreUnwrapCall matches a cliMailStore call that reads its embedded store.
// The argument list is matched without nested parens because both roots pass
// three plain identifiers; a call that grew a nested expression would stop
// matching and fail the count above, which is the direction that wants a human.
var mailStoreUnwrapCall = regexp.MustCompile(`cliMailStore\([^()]*\)\.Store`)

// TestHandoffMessagingScanDetectsTheDefectItGuards is the control the scan above
// cannot supply for itself. A source scan over a clean file passes identically
// whether the needles describe the defect or describe nothing at all — a typo in
// any one of them, or a rename in cmd_handoff.go that leaves the needle behind,
// turns the guard into decoration with no test going red. Running the same scan
// over source that DOES contain the defect proves it can still fire.
func TestHandoffMessagingScanDetectsTheDefectItGuards(t *testing.T) {
	content := handoffCommandSource(t)
	for _, needle := range handoffMessagingForbidden {
		// The needle must name a call shape the file actually has, differing only
		// in the store argument. Otherwise it guards a spelling nothing would ever
		// produce.
		routed := strings.Replace(needle, "(store,", "(msgStore,", 1)
		if routed == needle {
			t.Errorf("forbidden needle %q does not name a store argument, so it cannot be the unrouted spelling of anything", needle)
			continue
		}
		if !strings.Contains(content, routed) {
			t.Errorf("cmd_handoff.go contains no %q, so the forbidden needle %q guards a call shape that does not exist — the scan would stay green through the defect", routed, needle)
		}
		if !strings.Contains(strings.Replace(content, routed, needle, 1), needle) {
			t.Errorf("planting %q in the source did not make the scan match it", needle)
		}
	}
}

// handoffCommandSource reads cmd_handoff.go beside this test file.
func handoffCommandSource(t *testing.T) string {
	t.Helper()
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	path := filepath.Join(filepath.Dir(currentFile), "cmd_handoff.go")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%q): %v", path, err)
	}
	return string(data)
}
