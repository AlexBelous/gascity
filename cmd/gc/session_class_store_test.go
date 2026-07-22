package main

import (
	"os"
	"strings"
	"testing"

	"github.com/gastownhall/gascity/internal/beads"
	sessionsdb "github.com/gastownhall/gascity/internal/classdb/sessions"
	"github.com/gastownhall/gascity/internal/config"
	"github.com/gastownhall/gascity/internal/doctor"
	"github.com/gastownhall/gascity/internal/session"
)

func shadowSessionsConfig() *config.City {
	return &config.City{Beads: config.BeadsConfig{Classes: map[string]config.BeadClassConfig{
		config.BeadClassSessions: {Shadow: true},
	}}}
}

func TestMaybeShadowSessionStoreGate(t *testing.T) {
	base := beads.NewMemStore()
	city := t.TempDir()

	if got := maybeShadowSessionStore(base, nil, city); got != beads.Store(base) {
		t.Fatal("nil cfg must return the base store unwrapped")
	}
	if got := maybeShadowSessionStore(base, &config.City{}, city); got != beads.Store(base) {
		t.Fatal("shadow-off cfg must return the base store unwrapped")
	}

	cfg := shadowSessionsConfig()
	wrapped := maybeShadowSessionStore(base, cfg, city)
	shadowed, ok := wrapped.(interface{ ShadowPrimary() beads.Store })
	if !ok {
		t.Fatal("shadow-on cfg must return the sessions shadow wrapper")
	}
	if shadowed.ShadowPrimary() != beads.Store(base) {
		t.Fatal("wrapper does not front the base store")
	}
	// Identity stability: repeated resolves return the SAME wrapper value so
	// identity-keyed consumers (the messaging repair registry) see one handle.
	if again := maybeShadowSessionStore(base, cfg, city); again != wrapped {
		t.Fatal("repeated resolve returned a distinct wrapper")
	}
	// The identity key unwraps to the base store for the repair registry.
	if storeIdentityKey(wrapped) != storeIdentityKey(base) {
		t.Fatal("storeIdentityKey does not unwrap the shadow wrapper")
	}
}

func sqliteSessionsCityConfig() *config.City {
	return &config.City{Beads: config.BeadsConfig{Classes: map[string]config.BeadClassConfig{
		config.BeadClassSessions: {Backend: config.BeadsClassBackendSQLite},
	}}}
}

func writeSessionsMigratedMarker(t *testing.T, city string) {
	t.Helper()
	if err := os.MkdirAll(sessionsdb.StoreDir(city), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sessionsdb.MigratedMarkerPath(city), []byte("migrated\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestResolveSessionStoreRoutesOnMarkedCity(t *testing.T) {
	base := beads.NewMemStore()
	city := t.TempDir()
	cfg := sqliteSessionsCityConfig()

	// Unmarked: bd (the base store), regardless of the knob.
	if got := resolveSessionStore(base, cfg, city, nil); got != beads.Store(base) {
		t.Fatal("unmarked city must resolve to the bd store")
	}

	writeSessionsMigratedMarker(t, city)
	routed := resolveSessionStore(base, cfg, city, nil)
	class, ok := routed.(*sessionsdb.Store)
	if !ok {
		t.Fatalf("marked+configured city resolved %T, want *sessionsdb.Store", routed)
	}
	// End-to-end through the public front door against the routed store.
	front := session.NewStore(beads.SessionStore{Store: routed})
	info, err := front.CreateSessionInfo(session.CreateSpec{
		Title: "r", AgentName: "r",
		Metadata: map[string]string{"state": "awake"},
	})
	if err != nil {
		t.Fatalf("routed create: %v", err)
	}
	if got, err := front.Get(info.ID); err != nil || got.MetadataState != "awake" {
		t.Fatalf("routed get: %+v %v", got, err)
	}
	if _, err := base.Get(info.ID); err == nil {
		t.Fatal("routed create leaked into the bd store")
	}
	if class.Path() != sessionsdb.StorePath(city) {
		t.Fatalf("routed store path %q", class.Path())
	}

	// Rollback escape hatch: knob back to bd resolves the base store again.
	if got := resolveSessionStore(base, &config.City{}, city, nil); got != beads.Store(base) {
		t.Fatal("marked city with bd knob must resolve to the bd store")
	}
}

func TestResolveSessionStoreFailsClosedOnUnresolvableMarkedCity(t *testing.T) {
	base := beads.NewMemStore()
	city := t.TempDir()
	writeSessionsMigratedMarker(t, city)
	// A directory squatting on the db path makes the class store unopenable.
	if err := os.MkdirAll(sessionsdb.StorePath(city), 0o755); err != nil {
		t.Fatal(err)
	}
	st := resolveSessionStore(base, sqliteSessionsCityConfig(), city, nil)
	if st == beads.Store(base) {
		t.Fatal("marked-but-unopenable city must NOT fall back to bd")
	}
	if _, err := st.Get("gcs-1"); err == nil || !strings.Contains(err.Error(), "sessions-class store unavailable") {
		t.Fatalf("want fail-closed erroring store, got err=%v", err)
	}
}

func TestSeedSessionsShadowAtBootAndDoctorDiff(t *testing.T) {
	city := t.TempDir()
	primary := beads.NewMemStore()
	front := session.NewStore(beads.SessionStore{Store: primary})
	info, err := front.CreateSessionInfo(session.CreateSpec{
		Title: "a", AgentName: "a",
		Metadata: map[string]string{"state": "awake"},
	})
	if err != nil {
		t.Fatal(err)
	}

	prev := openSessionsShadowSeedStore
	openSessionsShadowSeedStore = func(string) (beads.Store, error) { return primary, nil }
	t.Cleanup(func() { openSessionsShadowSeedStore = prev })

	var stderr strings.Builder
	cfg := shadowSessionsConfig()
	seedSessionsShadowAtBoot(city, cfg, &stderr)
	if !strings.Contains(stderr.String(), "seeded 1 rows") {
		t.Fatalf("seed output: %q", stderr.String())
	}
	class, err := sessionsdb.SharedStoreFor(city)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := class.Get(info.ID); err != nil {
		t.Fatalf("seeded row missing: %v", err)
	}

	check := &sessionsShadowDoctorCheck{
		cfg: cfg, cityPath: city,
		newStore: func(string) (beads.Store, error) { return primary, nil },
	}
	res := check.Run(&doctor.CheckContext{})
	if res.Status != doctor.StatusOK || !strings.Contains(res.Message, "clean") {
		t.Fatalf("clean shadow reported %v %q", res.Status, res.Message)
	}

	// Persistent divergence flips the check to warning.
	if err := class.SetMetadata(info.ID, "state", "diverged"); err != nil {
		t.Fatal(err)
	}
	res = check.Run(&doctor.CheckContext{})
	if res.Status != doctor.StatusWarning || !strings.Contains(res.Message, "DIVERGED") {
		t.Fatalf("diverged shadow reported %v %q", res.Status, res.Message)
	}

	// Shadow off: the check reports disabled and never opens stores.
	off := &sessionsShadowDoctorCheck{
		cfg: &config.City{}, cityPath: city,
		newStore: func(string) (beads.Store, error) { t.Fatal("store opened with shadow off"); return nil, nil },
	}
	res = off.Run(&doctor.CheckContext{})
	if res.Status != doctor.StatusOK || !strings.Contains(res.Message, "disabled") {
		t.Fatalf("shadow-off check reported %v %q", res.Status, res.Message)
	}
}
