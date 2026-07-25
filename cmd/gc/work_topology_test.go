package main

import (
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/beads/contract"
	"github.com/gastownhall/gascity/internal/config"
	"github.com/gastownhall/gascity/internal/fsys"
)

// ── test fixtures ─────────────────────────────────────────────────────────

func writeUnifiedMarker(t *testing.T, city string) {
	t.Helper()
	if err := writeWorkTopologyMarker(workUnifiedMarkerPath(city), &workTopologyMarker{
		Kind:       workMarkerKindUnified,
		RecordedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("write unified marker: %v", err)
	}
}

func writeRemoteMarker(t *testing.T, city, host, port, database string) {
	t.Helper()
	if err := writeWorkTopologyMarker(workRemoteMarkerPath(city), &workTopologyMarker{
		Kind:       workMarkerKindRemote,
		RecordedAt: time.Now().UTC(),
		Target:     &workTopologyTarget{Host: host, Port: port, Database: database},
	}); err != nil {
		t.Fatalf("write remote marker: %v", err)
	}
}

func writeUnifiedStamp(t *testing.T, scopeRoot, database string) {
	t.Helper()
	if err := writeWorkTopologyStamp(scopeRoot, &workTopologyStamp{
		Kind:       workMarkerKindUnified,
		Database:   database,
		RecordedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("write unified stamp: %v", err)
	}
}

func writeRemoteStamp(t *testing.T, scopeRoot, host, port, database string) {
	t.Helper()
	if err := writeWorkTopologyStamp(scopeRoot, &workTopologyStamp{
		Kind:       workMarkerKindRemote,
		Database:   database,
		Host:       host,
		Port:       port,
		RecordedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("write remote stamp: %v", err)
	}
}

func writeScopeFiles(t *testing.T, root string, state contract.ConfigState, database string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(root, ".beads"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := contract.EnsureCanonicalConfig(fsys.OSFS{}, filepath.Join(root, ".beads", "config.yaml"), state); err != nil {
		t.Fatalf("EnsureCanonicalConfig(%s): %v", root, err)
	}
	if _, err := contract.EnsureCanonicalMetadata(fsys.OSFS{}, filepath.Join(root, ".beads", "metadata.json"), contract.MetadataState{
		Database:     "dolt",
		Backend:      "dolt",
		DoltMode:     "server",
		DoltDatabase: database,
	}); err != nil {
		t.Fatalf("EnsureCanonicalMetadata(%s): %v", root, err)
	}
}

func writeScopeMetadataOnly(t *testing.T, root, database string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(root, ".beads"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := contract.EnsureCanonicalMetadata(fsys.OSFS{}, filepath.Join(root, ".beads", "metadata.json"), contract.MetadataState{
		Database:     "dolt",
		Backend:      "dolt",
		DoltMode:     "server",
		DoltDatabase: database,
	}); err != nil {
		t.Fatalf("EnsureCanonicalMetadata(%s): %v", root, err)
	}
}

func managedCityState() contract.ConfigState {
	return contract.ConfigState{EndpointOrigin: contract.EndpointOriginManagedCity, EndpointStatus: contract.EndpointStatusVerified}
}

func inheritedRigState() contract.ConfigState {
	return contract.ConfigState{EndpointOrigin: contract.EndpointOriginInheritedCity, EndpointStatus: contract.EndpointStatusVerified}
}

func cityCanonicalState(host, port string) contract.ConfigState {
	return contract.ConfigState{EndpointOrigin: contract.EndpointOriginCityCanonical, DoltHost: host, DoltPort: port, EndpointStatus: contract.EndpointStatusUnverified}
}

func inheritedCanonicalRigState(host, port string) contract.ConfigState {
	return contract.ConfigState{EndpointOrigin: contract.EndpointOriginInheritedCity, DoltHost: host, DoltPort: port, EndpointStatus: contract.EndpointStatusUnverified}
}

func workCfg(scope, target string, rigs ...string) *config.City {
	cfg := &config.City{}
	cfg.Beads.Work.Scope = scope
	cfg.Beads.Work.Target = target
	for _, r := range rigs {
		cfg.Rigs = append(cfg.Rigs, config.Rig{Name: r, Path: r, Prefix: r})
	}
	return cfg
}

// ── Deliverable B: topology-aware desired state ──────────────────────────

func TestWorkTopologyDarkDefaults(t *testing.T) {
	city := t.TempDir()
	rig := filepath.Join(city, "fe")

	if db, ok := workTopologyScopeDatabase(city, rig); ok {
		t.Fatalf("dark rig db = (%q, %v), want no override", db, ok)
	}
	if got := defaultScopeDoltDatabase(city, rig, "fe"); got != "fe" {
		t.Fatalf("dark rig default database = %q, want fe", got)
	}
	if got := defaultScopeDoltDatabase(city, city, "hqprefix"); got != "hq" {
		t.Fatalf("dark city default database = %q, want hq", got)
	}
	if _, ok := workTopologyDesiredCityState(city, "hqprefix"); ok {
		t.Fatal("dark city must have no desired override")
	}
	if topo := loadWorkTopologyBestEffort(city); topo.sharesCityDatabase() {
		t.Fatal("dark city topology must not share a city database")
	}
}

// TestWorkTopologyUnifiedRigSharesActualCityDatabase pins F3: the shared
// database is the city's ACTUAL dolt_database, not the legacy "hq" constant.
func TestWorkTopologyUnifiedRigSharesActualCityDatabase(t *testing.T) {
	city := t.TempDir()
	rig := filepath.Join(city, "fe")
	// City metadata names a non-default database.
	writeScopeFiles(t, city, managedCityState(), "acme")
	writeUnifiedMarker(t, city)

	if db, ok := workTopologyScopeDatabase(city, rig); !ok || db != "acme" {
		t.Fatalf("unified rig db = (%q, %v), want (acme, true)", db, ok)
	}
	if got := defaultScopeDoltDatabase(city, rig, "fe"); got != "acme" {
		t.Fatalf("unified rig default database = %q, want acme", got)
	}
	// The city keeps its own database; no relocation, so no self-residue source.
	if _, ok := workTopologyScopeDatabase(city, city); ok {
		t.Fatal("unified/managed city database must be unchanged")
	}
	// Re-point wins over stale on-disk rig metadata.
	writeScopeFiles(t, rig, inheritedRigState(), "fe")
	if got := canonicalScopeDoltDatabase(city, rig, "fe"); got != "acme" {
		t.Fatalf("canonical rig db under unified = %q, want acme (topology wins over stale metadata)", got)
	}
	// The city verify passes (city db == wantDB) and records no residue.
	if err := verifyWorkTopologyScope(city, city, "hq"); err != nil {
		t.Fatalf("city verify under unified must pass: %v", err)
	}
	// The city scope is NOT relocated under unified/managed, so recording must
	// never name the LIVE city database ("acme") as a bogus drain source.
	if err := recordWorkTopologyResidueForScope(city, city, "hq"); err != nil {
		t.Fatalf("city residue record must be a no-op: %v", err)
	}
	m, _, _ := readWorkTopologyMarker(workUnifiedMarkerPath(city))
	if m.undrainedResidueCount() != 0 {
		t.Fatalf("city under unified/managed must record no self-residue; got %+v", m.ResidueSources)
	}
}

func TestWorkTopologyRemoteRelocatesCityAndRigs(t *testing.T) {
	city := t.TempDir()
	rig := filepath.Join(city, "fe")
	writeRemoteMarker(t, city, "10.0.0.5", "3306", "orgdb")

	if db, ok := workTopologyScopeDatabase(city, city); !ok || db != "orgdb" {
		t.Fatalf("remote city db = (%q, %v), want (orgdb, true)", db, ok)
	}
	if db, ok := workTopologyScopeDatabase(city, rig); !ok || db != "orgdb" {
		t.Fatalf("remote rig db = (%q, %v), want (orgdb, true)", db, ok)
	}
	state, ok := workTopologyDesiredCityState(city, "hqprefix")
	if !ok || state.EndpointOrigin != contract.EndpointOriginCityCanonical || state.DoltHost != "10.0.0.5" || state.DoltPort != "3306" {
		t.Fatalf("remote city desired state = (%+v, %v)", state, ok)
	}
}

func TestWorkTopologyResolversSurfaceMarkerStatError(t *testing.T) {
	city := t.TempDir()
	if err := os.MkdirAll(filepath.Join(city, ".gc", "store"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(workUnifiedMarkerPath(city), []byte("{bad"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := resolveDesiredCityEndpointState(city, config.DoltConfig{}, "hq"); err == nil {
		t.Fatal("resolveDesiredCityEndpointState must surface an unparseable marker error")
	}
	if _, err := resolveDesiredRigEndpointState(city, config.Rig{Name: "fe", Path: filepath.Join(city, "fe"), Prefix: "fe"}, managedCityState()); err == nil {
		t.Fatal("resolveDesiredRigEndpointState must surface an unparseable marker error")
	}
}

// TestWorkTopologyRemoteMarkerNilTargetMalformed pins F14.
func TestWorkTopologyRemoteMarkerNilTargetMalformed(t *testing.T) {
	city := t.TempDir()
	if err := writeWorkTopologyMarker(workRemoteMarkerPath(city), &workTopologyMarker{Kind: workMarkerKindRemote}); err != nil {
		t.Fatal(err)
	}
	if _, err := loadWorkTopology(city); err == nil {
		t.Fatal("remote marker with no target must be rejected as malformed")
	}
	if err := checkWorkTopologyMarkers(city, workCfg("unified", "dolt://10.0.0.5:3306/orgdb", "fe")); err == nil {
		t.Fatal("checkWorkTopologyMarkers must reject a malformed remote marker")
	}
}

// ── Deliverable C: never-silently-discard (residue recording) ─────────────

func TestRecordWorkTopologyResidueForLateBoundRig(t *testing.T) {
	city := t.TempDir()
	rig := filepath.Join(city, "fe")
	writeScopeFiles(t, city, managedCityState(), "hq")
	writeUnifiedMarker(t, city)
	writeScopeFiles(t, rig, inheritedRigState(), "fe")

	if err := recordWorkTopologyResidueForScope(city, rig, "fe"); err != nil {
		t.Fatalf("recordWorkTopologyResidueForScope: %v", err)
	}
	m, ok, err := readWorkTopologyMarker(workUnifiedMarkerPath(city))
	if err != nil || !ok {
		t.Fatalf("read marker = (ok=%v, err=%v)", ok, err)
	}
	if len(m.ResidueSources) != 1 || m.ResidueSources[0].Database != "fe" {
		t.Fatalf("residue sources = %+v, want one drain source for db fe", m.ResidueSources)
	}

	// Re-pointed rig (db already "hq") records nothing.
	rig2 := filepath.Join(city, "be")
	writeScopeFiles(t, rig2, inheritedRigState(), "hq")
	if err := recordWorkTopologyResidueForScope(city, rig2, "be"); err != nil {
		t.Fatal(err)
	}
	m, _, _ = readWorkTopologyMarker(workUnifiedMarkerPath(city))
	if len(m.ResidueSources) != 1 {
		t.Fatalf("re-pointed rig must not add residue; sources = %+v", m.ResidueSources)
	}
}

// TestRecordWorkTopologyResidueMetadataOnly pins F6: a scope with metadata but
// no config.yaml is still a recordable drain source (the db name is the key).
func TestRecordWorkTopologyResidueMetadataOnly(t *testing.T) {
	city := t.TempDir()
	rig := filepath.Join(city, "fe")
	writeScopeFiles(t, city, managedCityState(), "hq")
	writeUnifiedMarker(t, city)
	writeScopeMetadataOnly(t, rig, "fe") // legacy db, no config.yaml

	if err := recordWorkTopologyResidueForScope(city, rig, "fe"); err != nil {
		t.Fatalf("recordWorkTopologyResidueForScope: %v", err)
	}
	m, _, _ := readWorkTopologyMarker(workUnifiedMarkerPath(city))
	if len(m.ResidueSources) != 1 || m.ResidueSources[0].Database != "fe" {
		t.Fatalf("metadata-only scope must record residue; sources = %+v", m.ResidueSources)
	}
}

// TestRecordWorkTopologyResidueFailsClosedOnReadError pins F4: an unreadable
// scope config aborts the record (and thus the canonicalization write).
func TestRecordWorkTopologyResidueFailsClosedOnReadError(t *testing.T) {
	city := t.TempDir()
	rig := filepath.Join(city, "fe")
	writeScopeFiles(t, city, managedCityState(), "hq")
	writeUnifiedMarker(t, city)
	// Make .beads/config.yaml a directory → ReadConfigState errors.
	if err := os.MkdirAll(filepath.Join(rig, ".beads", "config.yaml"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := recordWorkTopologyResidueForScope(city, rig, "fe"); err == nil {
		t.Fatal("an unreadable scope config must abort residue recording (fail closed)")
	}
}

func TestRecordWorkTopologyResidueDarkNoop(t *testing.T) {
	city := t.TempDir()
	rig := filepath.Join(city, "fe")
	writeScopeFiles(t, rig, inheritedRigState(), "fe")
	if err := recordWorkTopologyResidueForScope(city, rig, "fe"); err != nil {
		t.Fatalf("dark residue record must be a no-op: %v", err)
	}
	if _, ok, _ := readWorkTopologyMarker(workUnifiedMarkerPath(city)); ok {
		t.Fatal("dark residue record must not create a marker")
	}
}

// ── Deliverable D: post-write verify ─────────────────────────────────────

func TestVerifyWorkTopologyScopeDarkNoop(t *testing.T) {
	city := t.TempDir()
	writeScopeFiles(t, city, managedCityState(), "hq")
	if err := verifyWorkTopologyScope(city, city, "hq"); err != nil {
		t.Fatalf("dark verify must be a no-op: %v", err)
	}
}

func TestVerifyWorkTopologyScopeManagedRuntimeUnavailableTolerated(t *testing.T) {
	city := t.TempDir()
	rig := filepath.Join(city, "fe")
	writeScopeFiles(t, city, managedCityState(), "hq")
	writeUnifiedMarker(t, city)
	writeScopeFiles(t, rig, inheritedRigState(), "hq")
	if err := verifyWorkTopologyScope(city, rig, "fe"); err != nil {
		t.Fatalf("verify must tolerate an unavailable managed runtime: %v", err)
	}
}

// ── Deliverable E: the one-way guard ─────────────────────────────────────

// TestCheckWorkTopologyDarkLegacyHqSharedDatabase pins the F2/F5 DARK fix: a
// never-migrated legacy city whose rig metadata already names the city database
// ("hq") and carries NO provenance stamp boots and runs gc bd fine.
func TestCheckWorkTopologyDarkLegacyHqSharedDatabase(t *testing.T) {
	city := t.TempDir()
	writeScopeFiles(t, city, managedCityState(), "hq")
	writeScopeFiles(t, filepath.Join(city, "fe"), inheritedRigState(), "hq") // shares "hq", no stamp
	if err := checkWorkTopologyMarkers(city, workCfg("scoped", "", "fe")); err != nil {
		t.Fatalf("legacy scoped city with a shared-name rig db must pass (DARK): %v", err)
	}
	if err := checkWorkTopologyMarkersCached(city, workCfg("scoped", "", "fe")); err != nil {
		t.Fatalf("cached guard must also pass the legacy shared-name city: %v", err)
	}
}

// TestCheckWorkTopologyHostedExternalCityPasses pins F10: a city_canonical
// (hosted, external) city with an identity.toml and a rig sharing the external
// db, but NO stamp, is not misclassified as a reverted remote migration.
func TestCheckWorkTopologyHostedExternalCityPasses(t *testing.T) {
	city := t.TempDir()
	writeScopeFiles(t, city, cityCanonicalState("10.0.0.5", "3306"), "orgdb")
	writeScopeFiles(t, filepath.Join(city, "fe"), inheritedCanonicalRigState("10.0.0.5", "3306"), "orgdb")
	if err := contract.WriteProjectIdentity(fsys.OSFS{}, city, "proj_hosted"); err != nil {
		t.Fatal(err)
	}
	// target=managed, no stamps → not a remote re-point.
	if err := checkWorkTopologyMarkers(city, workCfg("unified", "managed", "fe")); err != nil {
		t.Fatalf("hosted external city with no stamp must pass: %v", err)
	}
	if err := checkWorkTopologyMarkers(city, workCfg("scoped", "", "fe")); err != nil {
		t.Fatalf("hosted external scoped city with no stamp must pass: %v", err)
	}
}

// TestCheckWorkTopologyStampRevertRefuses pins the stamp-carrying reverted city.
func TestCheckWorkTopologyStampRevertRefuses(t *testing.T) {
	city := t.TempDir()
	rig := filepath.Join(city, "fe")
	writeScopeFiles(t, city, managedCityState(), "hq")
	writeScopeFiles(t, rig, inheritedRigState(), "hq")
	writeUnifiedStamp(t, rig, "hq") // provenance: this rig WAS re-pointed

	// Marker lost, config reverted to scoped → the stamp still refuses.
	if err := checkWorkTopologyMarkers(city, workCfg("scoped", "", "fe")); err == nil {
		t.Fatal("a stamp-carrying rig + scope=scoped must be rejected even without a marker")
	}
	// Config consistent with the stamp → passes.
	if err := checkWorkTopologyMarkers(city, workCfg("unified", "", "fe")); err != nil {
		t.Fatalf("stamp-carrying rig + scope=unified must pass: %v", err)
	}
}

func TestCheckWorkTopologyRemoteStampRevertRefuses(t *testing.T) {
	city := t.TempDir()
	writeScopeFiles(t, city, cityCanonicalState("10.0.0.5", "3306"), "orgdb")
	writeRemoteStamp(t, city, "10.0.0.5", "3306", "orgdb")

	// target=managed contradicts the remote stamp.
	if err := checkWorkTopologyMarkers(city, workCfg("unified", "managed")); err == nil {
		t.Fatal("a remote stamp + target=managed must be rejected even without a marker")
	}
	// Matching remote target passes.
	if err := checkWorkTopologyMarkers(city, workCfg("unified", "dolt://10.0.0.5:3306/orgdb")); err != nil {
		t.Fatalf("remote stamp + matching target must pass: %v", err)
	}
	// A different remote target is a forbidden retarget.
	if err := checkWorkTopologyMarkers(city, workCfg("unified", "dolt://10.0.0.9:3306/orgdb")); err == nil {
		t.Fatal("a remote stamp must reject a retarget")
	}
	// Loopback spelling must not trip the retarget arm (F8).
	writeRemoteStamp(t, city, "localhost", "3306", "orgdb")
	if err := checkWorkTopologyMarkers(city, workCfg("unified", "dolt://127.0.0.1:3306/orgdb")); err != nil {
		t.Fatalf("loopback host spelling must not trip the stamp retarget arm: %v", err)
	}
}

func TestCheckWorkTopologyUnifiedMarkerRejectsScopedConfig(t *testing.T) {
	city := t.TempDir()
	writeScopeFiles(t, city, managedCityState(), "hq")
	writeUnifiedMarker(t, city)
	if err := checkWorkTopologyMarkers(city, workCfg("scoped", "", "fe")); err == nil {
		t.Fatal("unified marker + scope=scoped must be rejected (one-way)")
	}
	if err := checkWorkTopologyMarkers(city, workCfg("unified", "", "fe")); err != nil {
		t.Fatalf("unified marker + scope=unified must pass: %v", err)
	}
}

func TestCheckWorkTopologyRemoteMarkerRejectsManagedTarget(t *testing.T) {
	city := t.TempDir()
	writeScopeFiles(t, city, managedCityState(), "hq")
	writeRemoteMarker(t, city, "10.0.0.5", "3306", "orgdb")
	if err := checkWorkTopologyMarkers(city, workCfg("unified", "managed", "fe")); err == nil {
		t.Fatal("remote marker + target=managed must be rejected (one-way)")
	}
	if err := checkWorkTopologyMarkers(city, workCfg("unified", "dolt://10.0.0.5:3306/orgdb", "fe")); err != nil {
		t.Fatalf("remote marker + matching target must pass: %v", err)
	}
}

func TestCheckWorkTopologyRemoteMarkerRejectsRetarget(t *testing.T) {
	city := t.TempDir()
	writeScopeFiles(t, city, managedCityState(), "hq")
	writeRemoteMarker(t, city, "10.0.0.5", "3306", "orgdb")
	if err := checkWorkTopologyMarkers(city, workCfg("unified", "dolt://10.0.0.9:3306/orgdb", "fe")); err == nil {
		t.Fatal("remote marker recorded target must not be silently retargeted")
	}
}

// TestCheckWorkTopologyMarkerRejectsExplicitRigEndpoint pins F11's marker arm 4.
func TestCheckWorkTopologyMarkerRejectsExplicitRigEndpoint(t *testing.T) {
	city := t.TempDir()
	writeScopeFiles(t, city, managedCityState(), "hq")
	writeUnifiedMarker(t, city)
	cfg := workCfg("unified", "", "fe")
	cfg.Rigs[0].DoltHost = "db.example.com"
	cfg.Rigs[0].DoltPort = "4406"
	err := checkWorkTopologyMarkers(city, cfg)
	if err == nil || !strings.Contains(err.Error(), "fe") {
		t.Fatalf("a unified marker + rig with an explicit dolt endpoint must be rejected naming the rig: %v", err)
	}
}

func TestCheckWorkTopologyUnifiedSelfHealWindow(t *testing.T) {
	city := t.TempDir()
	writeUnifiedMarker(t, city)
	// Rig not yet re-pointed (no stamp, own legacy db) + config matches marker.
	writeScopeFiles(t, city, managedCityState(), "hq")
	writeScopeFiles(t, filepath.Join(city, "fe"), inheritedRigState(), "fe")
	if err := checkWorkTopologyMarkers(city, workCfg("unified", "", "fe")); err != nil {
		t.Fatalf("unified self-heal window must pass: %v", err)
	}
}

func TestCheckWorkTopologyMarkerStatErrorBlocks(t *testing.T) {
	city := t.TempDir()
	if err := os.MkdirAll(filepath.Join(city, ".gc", "store"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(workUnifiedMarkerPath(city), []byte("{bad"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := checkWorkTopologyMarkers(city, workCfg("unified", "", "fe")); err == nil {
		t.Fatal("unparseable marker must surface as a blocking error")
	}
}

// TestCheckWorkTopologyStampStatErrorBlocks pins fail-closed on a corrupt stamp.
func TestCheckWorkTopologyStampStatErrorBlocks(t *testing.T) {
	city := t.TempDir()
	writeScopeFiles(t, city, managedCityState(), "hq")
	if err := os.WriteFile(workTopologyStampPath(city), []byte("{bad"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := checkWorkTopologyMarkers(city, workCfg("unified", "")); err == nil {
		t.Fatal("an unparseable stamp must surface as a blocking error")
	}
}

func TestCheckWorkTopologyNilConfigIsNoop(t *testing.T) {
	if err := checkWorkTopologyMarkers(t.TempDir(), nil); err != nil {
		t.Fatalf("nil config must be a no-op: %v", err)
	}
}

// TestStartBeadsLifecycleRefusesTopologyContradiction pins boot + reload wiring.
func TestStartBeadsLifecycleRefusesTopologyContradiction(t *testing.T) {
	city := t.TempDir()
	writeUnifiedMarker(t, city)
	cfg := &config.City{}
	cfg.Beads.Provider = "file"
	cfg.Beads.Work.Scope = "scoped"
	if err := startBeadsLifecycle(city, "test", cfg, io.Discard); err == nil {
		t.Fatal("startBeadsLifecycle must refuse scope=scoped while a unified marker is present")
	}
	cfg.Beads.Work.Scope = "unified"
	if err := startBeadsLifecycle(city, "test", cfg, io.Discard); err != nil && strings.Contains(err.Error(), "one-way") {
		t.Fatalf("converged config must clear the topology guard: %v", err)
	}
}

// ── markers: cross-process lock + provenance stamp round-trips ────────────

// TestAppendWorkResidueSourceConcurrent exercises the flock+mutex critical
// section: N concurrent appends of distinct sources all persist (no loss).
func TestAppendWorkResidueSourceConcurrent(t *testing.T) {
	city := t.TempDir()
	path := workUnifiedMarkerPath(city)
	if err := writeWorkTopologyMarker(path, &workTopologyMarker{Kind: workMarkerKindUnified, RecordedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	const n = 24
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_ = appendWorkResidueSource(path, workResidueSource{
				Scope:    "rig",
				Host:     "127.0.0.1",
				Port:     "3306",
				Database: "db" + strconv.Itoa(i),
			})
		}(i)
	}
	wg.Wait()
	m, _, err := readWorkTopologyMarker(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(m.ResidueSources) != n {
		t.Fatalf("residue sources = %d, want %d (no concurrent loss)", len(m.ResidueSources), n)
	}
}

// TestAppendWorkResidueSourceCanonicalizesHost pins F8: loopback spellings dedup
// to one physical source.
func TestAppendWorkResidueSourceCanonicalizesHost(t *testing.T) {
	city := t.TempDir()
	path := workUnifiedMarkerPath(city)
	if err := writeWorkTopologyMarker(path, &workTopologyMarker{Kind: workMarkerKindUnified, RecordedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	if err := appendWorkResidueSource(path, workResidueSource{Scope: "a", Host: "localhost", Port: "3306", Database: "fe"}); err != nil {
		t.Fatal(err)
	}
	if err := appendWorkResidueSource(path, workResidueSource{Scope: "b", Host: "127.0.0.1", Port: "3306", Database: "fe"}); err != nil {
		t.Fatal(err)
	}
	m, _, _ := readWorkTopologyMarker(path)
	if len(m.ResidueSources) != 1 {
		t.Fatalf("loopback spellings must dedup to one source; got %+v", m.ResidueSources)
	}
	if m.ResidueSources[0].Host != "127.0.0.1" {
		t.Fatalf("residue host stored uncanonicalized: %q", m.ResidueSources[0].Host)
	}
}

// TestOpenStoreAtForCityFailsClosedOnTopologyContradiction pins F9: the shared
// work-store resolution seam every one-shot (hook/sling/convoy/mail) routes
// through fails closed on a contradicting config — routing resolution, not just
// gc bd.
func TestOpenStoreAtForCityFailsClosedOnTopologyContradiction(t *testing.T) {
	city := t.TempDir()
	if err := os.WriteFile(filepath.Join(city, "city.toml"), []byte("[workspace]\nname = \"topo\"\n\n[beads]\nprovider = \"file\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	writeUnifiedMarker(t, city) // marker present, config scope defaults to scoped
	if _, err := openStoreAtForCity(city, city); err == nil {
		t.Fatal("openStoreAtForCity must fail closed when the loaded config contradicts a work marker")
	}
}

func TestWorkTopologyStampRoundTrip(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".beads"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := readWorkTopologyStamp(root); ok || err != nil {
		t.Fatalf("absent stamp = (ok=%v, err=%v)", ok, err)
	}
	writeRemoteStamp(t, root, "10.0.0.5", "3306", "orgdb")
	s, ok, err := readWorkTopologyStamp(root)
	if err != nil || !ok || s.Kind != workMarkerKindRemote || s.Database != "orgdb" {
		t.Fatalf("stamp round-trip = (%+v, %v, %v)", s, ok, err)
	}
}
