package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gastownhall/gascity/internal/beadmeta"
	"github.com/gastownhall/gascity/internal/beads"
	sessionsdb "github.com/gastownhall/gascity/internal/classdb/sessions"
	"github.com/gastownhall/gascity/internal/config"
	"github.com/gastownhall/gascity/internal/mail/beadmail"
	"github.com/gastownhall/gascity/internal/session"
)

func infraLocalCityConfig() *config.City {
	return &config.City{Beads: config.BeadsConfig{Infra: config.BeadsInfraLocal}}
}

// seedCombinedInfraScope opens a read-write gcg-prefixed sqlite store at the
// window-3 combined scope path and returns it for the caller to populate; it is
// closed via t.Cleanup so the migration can reopen it read-only.
func seedCombinedInfraScope(t *testing.T, cityPath string) beads.Store {
	t.Helper()
	st, err := beads.OpenSQLiteStore(infraCombinedScopeDir(cityPath), beads.WithSQLiteStoreIDPrefix("gcg"))
	if err != nil {
		t.Fatalf("open combined scope: %v", err)
	}
	return st
}

func TestInfraScopeMigrationSourceDetection(t *testing.T) {
	t.Run("absent", func(t *testing.T) {
		if dir, ok := infraScopeMigrationSource(t.TempDir()); ok {
			t.Fatalf("bare city reported a source: %q", dir)
		}
	})
	t.Run("infra_scope", func(t *testing.T) {
		city := t.TempDir()
		dir := infraCombinedScopeDir(city)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "beads.sqlite"), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		got, ok := infraScopeMigrationSource(city)
		if !ok || got != dir {
			t.Fatalf("infraScopeMigrationSource = %q,%v want %q,true", got, ok, dir)
		}
	})
	t.Run("legacy_fallback", func(t *testing.T) {
		city := t.TempDir()
		dir := legacyCombinedScopeDir(city)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "beads.sqlite"), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		got, ok := infraScopeMigrationSource(city)
		if !ok || got != dir {
			t.Fatalf("legacy infraScopeMigrationSource = %q,%v want %q,true", got, ok, dir)
		}
	})
	t.Run("prefers_infra_over_legacy", func(t *testing.T) {
		city := t.TempDir()
		for _, dir := range []string{infraCombinedScopeDir(city), legacyCombinedScopeDir(city)} {
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "beads.sqlite"), []byte("x"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		got, _ := infraScopeMigrationSource(city)
		if got != infraCombinedScopeDir(city) {
			t.Fatalf("preferred source = %q, want the .gc/infra scope", got)
		}
	})
}

func TestOpenInfraCombinedScopeSourceDarkWhenAbsent(t *testing.T) {
	store, closeFn, ok, err := openInfraCombinedScopeSource(t.TempDir())
	defer closeFn()
	if err != nil || ok || store != nil {
		t.Fatalf("bare city: got store=%v ok=%v err=%v, want nil,false,nil", store, ok, err)
	}
}

// Deliverable B + C(i): a session bead minted in the combined scope under the
// gcg prefix migrates into sessions.db with its gcg id KEPT verbatim.
func TestEnsureSessionsClassMigratedImportsCombinedScopeKeepingGcgID(t *testing.T) {
	city := t.TempDir()
	src := seedCombinedInfraScope(t, city)
	front := session.NewStore(beads.SessionStore{Store: src})
	info, err := front.CreateSessionInfo(session.CreateSpec{
		Title: "live", AgentName: "live",
		Metadata: map[string]string{"state": "awake", "session_name": "gc-live"},
	})
	if err != nil {
		t.Fatal(err)
	}
	closeBeadStoreHandle(src) //nolint:errcheck
	if !strings.HasPrefix(info.ID, "gcg-") {
		t.Fatalf("combined-scope session id = %q, want gcg- prefix", info.ID)
	}

	// The work-store source is empty; the combined scope carries the session.
	overrideSessionsMigrationStore(t, beads.NewMemStore())
	var stderr strings.Builder
	if !ensureSessionsClassMigrated(city, sqliteSessionsCityConfig(), &stderr) {
		t.Fatalf("migration did not flip: %s", stderr.String())
	}

	class, err := sessionsdb.SharedStoreFor(city)
	if err != nil {
		t.Fatal(err)
	}
	got, err := class.Get(info.ID)
	if err != nil {
		t.Fatalf("session %s absent from sessions.db after migration: %v", info.ID, err)
	}
	if got.ID != info.ID {
		t.Fatalf("imported id = %q, want %q (id must be kept verbatim)", got.ID, info.ID)
	}
}

// Deliverable B + C collision guard: graph beads import from the combined scope
// keeping gcg ids; non-graph gcg beads do NOT leak into the graph store; and the
// graph id floor is lifted above the GLOBAL max gcg suffix so a fresh graph mint
// cannot reuse a suffix now owned by a sibling class store.
func TestEnsureGraphClassMigratedImportsCombinedScopeAndLiftsIdFloor(t *testing.T) {
	city := t.TempDir()
	src := seedCombinedInfraScope(t, city)
	graphBead, err := src.Create(beads.Bead{
		Type:     "molecule",
		Title:    "root",
		Metadata: map[string]string{beadmeta.RootBeadIDMetadataKey: "gcg-root"},
	})
	if err != nil {
		t.Fatal(err)
	}
	// A high-numbered NON-graph (session) bead sharing the gcg namespace.
	if _, err := src.Create(beads.Bead{
		ID: "gcg-9000", Type: "session", Status: "open",
		Labels: []string{"gc:session"}, Metadata: map[string]string{"state": "awake"},
	}); err != nil {
		t.Fatal(err)
	}
	closeBeadStoreHandle(src) //nolint:errcheck

	// Empty work-store sources.
	prev := openGraphClassMigrationStores
	openGraphClassMigrationStores = func(string, *config.City) ([]beads.Store, func(), error) {
		return nil, func() {}, nil
	}
	t.Cleanup(func() { openGraphClassMigrationStores = prev })

	cfg := infraLocalCityConfig()
	var stderr strings.Builder
	if !ensureGraphClassMigrated(city, cfg, &stderr) {
		t.Fatalf("graph migration did not flip: %s", stderr.String())
	}

	class, err := graphClassStoreFor(city)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := class.Get(graphBead.ID); err != nil {
		t.Fatalf("graph bead %s not imported: %v", graphBead.ID, err)
	}
	if _, err := class.Get("gcg-9000"); err == nil {
		t.Fatal("non-graph gcg-9000 leaked into the graph store")
	}
	floor, ferr := readGraphSeqFloor(city)
	if ferr != nil {
		t.Fatalf("readGraphSeqFloor: %v", ferr)
	}
	if floor < 9000 {
		t.Fatalf("persisted graph id floor = %d, want >= 9000", floor)
	}
	minted, err := class.Create(beads.Bead{Title: "fresh", Type: "molecule"})
	if err != nil {
		t.Fatal(err)
	}
	if n := reservedPrefixNumericSuffix(minted.ID, "gcg"); n <= 9000 {
		t.Fatalf("fresh graph mint %q reused suffix %d <= 9000; floor not lifted", minted.ID, n)
	}
}

// Deliverable C proof: a gcg id that misses the graph store but was reclassified
// into a non-graph class store resolves through the widened read federation.
func TestMaybeRouteBdShowLocalResolvesReclassifiedGcgSessionID(t *testing.T) {
	city := t.TempDir()
	class, err := sessionsdb.SharedStoreFor(city)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := class.ImportBead(beads.Bead{
		ID: "gcg-4242", Type: "session", Status: "open",
		Labels: []string{"gc:session"}, Title: "reclassified",
	}); err != nil {
		t.Fatal(err)
	}
	writeSessionsMigratedMarker(t, city)

	var stdout, stderr strings.Builder
	code, handled := maybeRouteBdShowLocal(city, sqliteSessionsCityConfig(), []string{"show", "gcg-4242"}, &stdout, &stderr)
	if !handled {
		t.Fatalf("federation did not handle the gcg id (stderr=%s)", stderr.String())
	}
	if code != 0 {
		t.Fatalf("show returned %d; stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "gcg-4242") {
		t.Fatalf("show output missing the id: %q", stdout.String())
	}
}

// Deliverable B: order-tracking runs minted in the combined scope migrate into
// the orders class store keeping their gcg id.
func TestEnsureOrdersClassMigratedImportsCombinedScope(t *testing.T) {
	city := t.TempDir()
	src := seedCombinedInfraScope(t, city)
	run, err := src.Create(beads.Bead{
		Title: "order:digest", Status: "open",
		Labels: []string{"order-run:digest", "order:digest", labelOrderTracking, "seq:5"},
	})
	if err != nil {
		t.Fatal(err)
	}
	closeBeadStoreHandle(src) //nolint:errcheck
	if !strings.HasPrefix(run.ID, "gcg-") {
		t.Fatalf("combined-scope order id = %q, want gcg- prefix", run.ID)
	}

	prev := openOrderClassMigrationStore
	openOrderClassMigrationStore = func(string, string) (beads.Store, error) { return beads.NewMemStore(), nil }
	t.Cleanup(func() { openOrderClassMigrationStore = prev })

	var stderr strings.Builder
	if !ensureOrdersClassMigrated(city, infraLocalCityConfig(), &stderr) {
		t.Fatalf("orders migration did not flip: %s", stderr.String())
	}
	class, err := ordersClassStoreFor(city)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := class.Get(run.ID); err != nil {
		t.Fatalf("order run %s absent from orders class store: %v", run.ID, err)
	}
}

// Deliverable B: mail sent into the combined scope migrates into the messaging
// class store keeping its gcg id.
func TestEnsureMessagingClassMigratedImportsCombinedScope(t *testing.T) {
	city := t.TempDir()
	src := seedCombinedInfraScope(t, city)
	msg, err := beadmail.NewWithStores(src, src).Send("boot/alpha", "boot/beta", "hello", "body")
	if err != nil {
		t.Fatal(err)
	}
	closeBeadStoreHandle(src) //nolint:errcheck
	if !strings.HasPrefix(msg.ID, "gcg-") {
		t.Fatalf("combined-scope mail id = %q, want gcg- prefix", msg.ID)
	}

	prev := openMessagingClassMigrationStore
	openMessagingClassMigrationStore = func(string) (beads.Store, error) { return beads.NewMemStore(), nil }
	t.Cleanup(func() { openMessagingClassMigrationStore = prev })

	var stderr strings.Builder
	if !ensureMessagingClassMigrated(city, infraLocalCityConfig(), &stderr) {
		t.Fatalf("messaging migration did not flip: %s", stderr.String())
	}
	class, err := messagingClassStoreHandle(city)
	if err != nil {
		t.Fatal(err)
	}
	if _, found, err := class.Get(msg.ID); err != nil || !found {
		t.Fatalf("mail %s absent from messaging class store: found=%v err=%v", msg.ID, found, err)
	}
}

// Deliverable E: the usage compute lane must read session beads through the
// sessions-class seam, not the raw work store. After the flip the session bead
// lives only in sessions.db, so a work-store Get misses ("bead not found") and
// resolveSessionStore is what finds it.
func TestUsageComputeSessionReadRoutesThroughSessionStore(t *testing.T) {
	city := t.TempDir()
	cfg := sqliteSessionsCityConfig()
	overrideSessionsMigrationStore(t, beads.NewMemStore())
	if !ensureSessionsClassMigrated(city, cfg, &strings.Builder{}) {
		t.Fatal("sessions migration did not flip")
	}
	class, err := sessionsdb.SharedStoreFor(city)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := class.ImportBead(beads.Bead{
		ID: "gcs-1", Type: "session", Status: "open",
		Labels: []string{"gc:session"}, Metadata: map[string]string{"state": "asleep"},
	}); err != nil {
		t.Fatal(err)
	}
	// The raw work store — what the pre-fix code read — does NOT hold the bead.
	workStore := beads.NewMemStore()
	if _, err := workStore.Get("gcs-1"); err == nil {
		t.Fatal("precondition failed: work store unexpectedly holds the routed session")
	}
	// The fix routes through resolveSessionStore, which DOES find it.
	routed := resolveSessionStore(workStore, cfg, city, nil)
	if _, err := routed.Get("gcs-1"); err != nil {
		t.Fatalf("resolveSessionStore did not find the routed session bead (E regression): %v", err)
	}
}

// Deliverable F (DARK): a city with no combined infra scope writes no graph id
// floor sidecar — the collision guard is inert off the deploy lineage.
func TestGraphMigrationDarkWithoutInfraScopeWritesNoSeqFloor(t *testing.T) {
	city := t.TempDir()
	prev := openGraphClassMigrationStores
	openGraphClassMigrationStores = func(string, *config.City) ([]beads.Store, func(), error) {
		return nil, func() {}, nil
	}
	t.Cleanup(func() { openGraphClassMigrationStores = prev })

	var stderr strings.Builder
	if !ensureGraphClassMigrated(city, infraLocalCityConfig(), &stderr) {
		t.Fatalf("graph migration did not flip on a bare city: %s", stderr.String())
	}
	if _, err := os.Stat(graphSeqFloorPath(city)); !os.IsNotExist(err) {
		t.Fatalf("seq floor sidecar written on a city with no infra scope: stat err = %v", err)
	}
	floor, err := readGraphSeqFloor(city)
	if err != nil {
		t.Fatalf("readGraphSeqFloor errored on an absent sidecar (must be DARK): %v", err)
	}
	if floor != 0 {
		t.Fatalf("readGraphSeqFloor = %d on a bare city, want 0", floor)
	}
}

// Finding 4: a present-but-corrupt seq-floor sidecar must fail CLOSED — the read
// errors (and so aborts the graph store open), never silently returns 0, which
// would re-arm the gcg re-mint collision the floor prevents.
func TestReadGraphSeqFloorFailsClosedOnCorruptSidecar(t *testing.T) {
	city := t.TempDir()
	if err := os.MkdirAll(graphClassStoreDir(city), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(graphSeqFloorPath(city), []byte("not-an-int\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := readGraphSeqFloor(city); err == nil {
		t.Fatal("readGraphSeqFloor returned nil error on a corrupt sidecar (must fail closed)")
	}
	// The store open must propagate the failure, not proceed with floor 0.
	if _, err := graphClassStoreFor(city); err == nil {
		t.Fatal("graphClassStoreFor opened despite a corrupt seq-floor sidecar (guard silently disabled)")
	}
}

// Finding 3: a CLOSED graph bead in the combined scope must cross (mid-flight
// molecules' closed steps feed finalize votes) and resolve by id.
func TestEnsureGraphClassMigratedImportsClosedInfraScopeGraphBeads(t *testing.T) {
	city := t.TempDir()
	src := seedCombinedInfraScope(t, city)
	closedStep, err := src.Create(beads.Bead{
		Type:     "step",
		Title:    "closed step",
		Metadata: map[string]string{beadmeta.RootBeadIDMetadataKey: "gcg-root"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := src.Close(closedStep.ID); err != nil {
		t.Fatal(err)
	}
	closeBeadStoreHandle(src) //nolint:errcheck

	prev := openGraphClassMigrationStores
	openGraphClassMigrationStores = func(string, *config.City) ([]beads.Store, func(), error) {
		return nil, func() {}, nil
	}
	t.Cleanup(func() { openGraphClassMigrationStores = prev })

	var stderr strings.Builder
	if !ensureGraphClassMigrated(city, infraLocalCityConfig(), &stderr) {
		t.Fatalf("graph migration did not flip: %s", stderr.String())
	}
	class, err := graphClassStoreFor(city)
	if err != nil {
		t.Fatal(err)
	}
	got, err := class.Get(closedStep.ID)
	if err != nil {
		t.Fatalf("closed graph step %s did not cross from the combined scope: %v", closedStep.ID, err)
	}
	if got.Status != "closed" {
		t.Fatalf("imported step status = %q, want closed", got.Status)
	}
}
