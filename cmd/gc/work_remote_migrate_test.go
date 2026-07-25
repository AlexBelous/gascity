package main

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/config"
)

// remoteCityConfig is a unified city whose work target is a well-formed remote
// dolt:// endpoint.
func remoteCityConfig() *config.City {
	return &config.City{
		Workspace: config.Workspace{Prefix: "hq"},
		Beads: config.BeadsConfig{Work: config.BeadsWorkConfig{
			Scope:  config.BeadsWorkScopeUnified,
			Target: "dolt://db.example:3306/orgdb",
		}},
		Rigs: []config.Rig{{Name: "alpha", Prefix: "al", Path: "rigs/alpha"}},
	}
}

func testRemoteTarget() workTopologyTarget {
	return workTopologyTarget{Host: "db.example", Port: "3306", Database: "orgdb"}
}

// gatewayRemoteCityConfig is a remote city whose target is declared a hosted,
// server-authoritative gateway via [beads.work] remote_config="verify".
func gatewayRemoteCityConfig() *config.City {
	cfg := remoteCityConfig()
	cfg.Beads.Work.RemoteConfig = config.BeadsWorkRemoteConfigVerify
	return cfg
}

// withWorkRemoteSeams overrides the exec-backed seams with fakes for the duration
// of a test. local is the source (city's managed-local work DB); remote is the
// org endpoint. addedPrefixes captures the allowed_prefixes writes.
func withWorkRemoteSeams(t *testing.T, local, remote *fakeWorkStore, addedPrefixes *[]string) {
	t.Helper()
	orig := struct {
		open     func(string, string) (beads.Store, func(), error)
		remoteOp func(string, workTopologyTarget) (beads.Store, func(), error)
		cred     func(beads.Store) error
		addPref  func(beads.Store, string) error
		readPref func(beads.Store) (map[string]bool, error)
		repoint  func(string, *config.City, io.Writer) error
		straggle func(string, workResidueSource) (beads.Store, func(), error)
		identity func(string, string) (workUnifyScope, error)
	}{
		openWorkUnifyScopeStore, openWorkRemoteScopeStore, workRemoteCredentialPreflight,
		workRemoteAddPrefixToSet, workRemoteReadAllowedPrefixes, workRemoteRepointScopes,
		openWorkUnifyStragglerStore, workUnifyResolveIdentity,
	}
	t.Cleanup(func() {
		openWorkUnifyScopeStore = orig.open
		openWorkRemoteScopeStore = orig.remoteOp
		workRemoteCredentialPreflight = orig.cred
		workRemoteAddPrefixToSet = orig.addPref
		workRemoteReadAllowedPrefixes = orig.readPref
		workRemoteRepointScopes = orig.repoint
		openWorkUnifyStragglerStore = orig.straggle
		workUnifyResolveIdentity = orig.identity
	})

	openWorkUnifyScopeStore = func(_, _ string) (beads.Store, func(), error) { return local, func() {}, nil }
	openWorkRemoteScopeStore = func(string, workTopologyTarget) (beads.Store, func(), error) {
		return remote, func() {}, nil
	}
	workRemoteCredentialPreflight = func(beads.Store) error { return nil }
	workRemoteAddPrefixToSet = func(_ beads.Store, prefix string) error {
		*addedPrefixes = append(*addedPrefixes, prefix)
		return nil
	}
	// Report every configured prefix already present so the self-heal is a no-op
	// unless a test overrides this.
	workRemoteReadAllowedPrefixes = func(beads.Store) (map[string]bool, error) {
		return map[string]bool{"hq": true, "al": true}, nil
	}
	workRemoteRepointScopes = func(string, *config.City, io.Writer) error { return nil }
	openWorkUnifyStragglerStore = func(string, workResidueSource) (beads.Store, func(), error) {
		return local, func() {}, nil
	}
	workUnifyResolveIdentity = func(_, scopeRoot string) (workUnifyScope, error) {
		return workUnifyScope{root: scopeRoot, database: "hq"}, nil
	}
}

func TestEnsureWorkRemoteDarkOnManagedCity(t *testing.T) {
	city := t.TempDir()
	if err := ensureWorkRemote(city, unifiedCityConfig(), &strings.Builder{}); err != nil {
		t.Fatalf("managed-target city must be dark, got %v", err)
	}
	if _, ok, _ := readWorkTopologyMarker(workRemoteMarkerPath(city)); ok {
		t.Fatalf("dark city must not write a remote marker")
	}
}

func TestEnsureWorkRemoteHappyPath(t *testing.T) {
	city := t.TempDir()
	writeUnifiedMarker(t, city) // unified rung satisfied
	local := newFakeWorkStore()
	remote := newFakeWorkStore()
	created := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	closed := created.Add(time.Hour)
	local.seed(&fakeWorkRec{id: "al-1", status: "open", issueType: "task", createdAt: created, updatedAt: created})
	local.seed(&fakeWorkRec{id: "al-2", status: "closed", issueType: "task", createdAt: created, updatedAt: closed, closedAt: &closed})

	var added []string
	withWorkRemoteSeams(t, local, remote, &added)

	var out strings.Builder
	if err := ensureWorkRemote(city, remoteCityConfig(), &out); err != nil {
		t.Fatalf("remote migration: %v\n%s", err, out.String())
	}

	// The marker is COMPLETE, carries the persisted (random) stamp and the local
	// residue source (undrained).
	m, ok, err := readWorkTopologyMarker(workRemoteMarkerPath(city))
	if err != nil || !ok {
		t.Fatalf("remote marker not written: ok=%v err=%v", ok, err)
	}
	if !m.isComplete() {
		t.Fatalf("marker must be complete, phase=%q", m.Phase)
	}
	if !strings.HasPrefix(m.Stamp, "gc-city:") {
		t.Fatalf("marker must carry a persisted gc-city: stamp, got %q", m.Stamp)
	}
	if m.Target == nil || m.Target.Database != "orgdb" || m.Target.Port != "3306" {
		t.Fatalf("unexpected remote marker target: %+v", m.Target)
	}
	if m.undrainedResidueCount() != 1 || m.ResidueSources[0].Database != "hq" {
		t.Fatalf("expected 1 undrained local residue source, got %+v", m.ResidueSources)
	}

	// Both work beads copied to the remote org DB, carrying the PERSISTED stamp,
	// status preserved, and NO lingering migrating label (swept on the success path).
	b1, err := remote.Get("al-1")
	if err != nil {
		t.Fatalf("al-1 not copied to remote: %v", err)
	}
	if b1.Metadata[workTopologySourceMetadataKey] != m.Stamp {
		t.Fatalf("al-1 must carry the persisted stamp %q, got %v", m.Stamp, b1.Metadata)
	}
	if recHasLabelBead(b1, workTopologyMigratingLabel) {
		t.Fatalf("quarantine label must be cleared after finalize, labels=%v", b1.Labels)
	}
	if b2, err := remote.Get("al-2"); err != nil || b2.Status != "closed" {
		t.Fatalf("al-2 must cross as closed: status=%q err=%v", b2.Status, err)
	}

	// Config step appended this city's prefixes.
	if !sliceContains(added, "hq") || !sliceContains(added, "al") {
		t.Fatalf("allowed_prefixes must include hq and al, got %v", added)
	}
}

// TestEnsureWorkRemoteMigratingLabelStampedMidCopy pins F6: if the migration
// aborts after the first copy (before finalize), copied org rows carry the
// gc.topology_migrating quarantine label so foreign cities' claim/ready surfaces
// withhold them.
func TestEnsureWorkRemoteMigratingLabelStampedMidCopy(t *testing.T) {
	city := t.TempDir()
	writeUnifiedMarker(t, city)
	local := newFakeWorkStore()
	remote := newFakeWorkStore()
	created := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	local.seed(&fakeWorkRec{id: "al-1", status: "open", issueType: "task", createdAt: created, updatedAt: created})

	var added []string
	withWorkRemoteSeams(t, local, remote, &added)
	// Abort the migration right after the first copy by failing the re-point.
	workRemoteRepointScopes = func(string, *config.City, io.Writer) error { return errForcedRepointFail }

	err := ensureWorkRemote(city, remoteCityConfig(), &strings.Builder{})
	if err == nil || !strings.Contains(err.Error(), "re-pointing") {
		t.Fatalf("expected re-point failure, got %v", err)
	}
	b1, gerr := remote.Get("al-1")
	if gerr != nil {
		t.Fatalf("al-1 should be copied even on abort: %v", gerr)
	}
	if !recHasLabelBead(b1, workTopologyMigratingLabel) {
		t.Fatalf("mid-copy org row must carry the quarantine label, labels=%v", b1.Labels)
	}
}

var errForcedRepointFail = context.DeadlineExceeded

func TestEnsureWorkRemoteCredentialPreflightFailure(t *testing.T) {
	city := t.TempDir()
	writeUnifiedMarker(t, city)
	local := newFakeWorkStore()
	remote := newFakeWorkStore()
	var added []string
	withWorkRemoteSeams(t, local, remote, &added)
	workRemoteCredentialPreflight = func(beads.Store) error { return context.DeadlineExceeded }

	err := ensureWorkRemote(city, remoteCityConfig(), &strings.Builder{})
	if err == nil || !strings.Contains(err.Error(), "BEADS_DOLT_CREDENTIAL_COMMAND") {
		t.Fatalf("expected credential-preflight abort naming the env, got %v", err)
	}
	// Preflight runs BEFORE the started-marker write and the config step.
	if _, ok, _ := readWorkTopologyMarker(workRemoteMarkerPath(city)); ok {
		t.Fatalf("preflight failure must not write any marker")
	}
	if len(added) != 0 {
		t.Fatalf("preflight failure must abort before the config step, got %v", added)
	}
}

func TestEnsureWorkRemoteForeignCollisionAborts(t *testing.T) {
	city := t.TempDir()
	writeUnifiedMarker(t, city)
	local := newFakeWorkStore()
	remote := newFakeWorkStore()
	created := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	local.seed(&fakeWorkRec{id: "al-1", status: "open", createdAt: created, updatedAt: created})
	// A FOREIGN city already holds al-1 on the org DB (different stamp), older than ours.
	remote.seed(&fakeWorkRec{
		id: "al-1", status: "open", createdAt: created, updatedAt: created,
		metadata: map[string]string{workTopologySourceMetadataKey: "gc-city:feedface00000000"},
	})

	var added []string
	withWorkRemoteSeams(t, local, remote, &added)

	err := ensureWorkRemote(city, remoteCityConfig(), &strings.Builder{})
	if err == nil || !strings.Contains(err.Error(), "foreign-city prefix collision") {
		t.Fatalf("expected foreign-collision abort, got %v", err)
	}
	// Pre-destructive: the foreign row is NOT overwritten and the marker never completes.
	fb, _ := remote.Get("al-1")
	if fb.Metadata[workTopologySourceMetadataKey] != "gc-city:feedface00000000" {
		t.Fatalf("foreign row must be untouched, got %v", fb.Metadata)
	}
	if m, ok, _ := readWorkTopologyMarker(workRemoteMarkerPath(city)); ok && m.isComplete() {
		t.Fatalf("collision abort must not complete the marker")
	}
}

// TestEnsureWorkRemoteResumeSameStamp pins F1/F8: a started marker present ⇒ the
// migration resumes with the SAME persisted stamp (never a re-mint), so a host
// reschedule can't turn our own rows into a false foreign collision.
func TestEnsureWorkRemoteResumeSameStamp(t *testing.T) {
	city := t.TempDir()
	writeUnifiedMarker(t, city)
	// A started marker recorded a prior boot's intent with a fixed stamp.
	priorStamp := "gc-city:aaaabbbbccccdddd"
	if err := writeWorkRemoteStartedMarker(city, testRemoteTarget(), priorStamp); err != nil {
		t.Fatal(err)
	}
	local := newFakeWorkStore()
	remote := newFakeWorkStore()
	created := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	local.seed(&fakeWorkRec{id: "al-1", status: "open", issueType: "task", createdAt: created, updatedAt: created})
	var added []string
	withWorkRemoteSeams(t, local, remote, &added)

	// mintTopologyStamp must NOT be called on resume — fail if it is.
	origMint := mintTopologyStamp
	t.Cleanup(func() { mintTopologyStamp = origMint })
	mintTopologyStamp = func() (string, error) { t.Fatal("resume must reuse the persisted stamp, not mint"); return "", nil }

	if err := ensureWorkRemote(city, remoteCityConfig(), &strings.Builder{}); err != nil {
		t.Fatalf("resume: %v", err)
	}
	m, _, _ := readWorkTopologyMarker(workRemoteMarkerPath(city))
	if m.Stamp != priorStamp {
		t.Fatalf("resume must preserve the persisted stamp, got %q want %q", m.Stamp, priorStamp)
	}
	b1, _ := remote.Get("al-1")
	if b1.Metadata[workTopologySourceMetadataKey] != priorStamp {
		t.Fatalf("resumed rows must carry the persisted stamp, got %v", b1.Metadata)
	}
}

// TestEnsureWorkRemoteRetargetRefused pins F8: a started marker pinning target A
// with config now naming target B is refused (the partial copy in A is otherwise
// stranded invisibly).
func TestEnsureWorkRemoteRetargetRefused(t *testing.T) {
	city := t.TempDir()
	writeUnifiedMarker(t, city)
	if err := writeWorkRemoteStartedMarker(city, workTopologyTarget{Host: "orgA", Port: "3306", Database: "a"}, "gc-city:1111"); err != nil {
		t.Fatal(err)
	}
	// Config now points at a DIFFERENT endpoint.
	cfg := remoteCityConfig() // dolt://db.example:3306/orgdb
	err := ensureWorkRemote(city, cfg, &strings.Builder{})
	if err == nil || !strings.Contains(err.Error(), "already in progress") {
		t.Fatalf("expected retarget refusal, got %v", err)
	}
}

func TestEnsureWorkRemoteMarkerCompleteSweepsQuarantine(t *testing.T) {
	city := t.TempDir()
	stamp := "gc-city:5555666677778888"
	// A completed remote city whose org store still holds one of OUR quarantined
	// rows plus a sibling city's quarantined row.
	org := newFakeWorkStore()
	org.seed(&fakeWorkRec{
		id: "al-1", status: "open", labels: []string{workTopologyMigratingLabel},
		metadata: map[string]string{workTopologySourceMetadataKey: stamp},
	})
	org.seed(&fakeWorkRec{
		id: "zz-9", status: "open", labels: []string{workTopologyMigratingLabel},
		metadata: map[string]string{workTopologySourceMetadataKey: "gc-city:sibling000000000"},
	})
	origOpen := openWorkUnifyScopeStore
	t.Cleanup(func() { openWorkUnifyScopeStore = origOpen })
	openWorkUnifyScopeStore = func(string, string) (beads.Store, func(), error) { return org, func() {}, nil }

	if err := finalizeWorkRemoteMarker(city, testRemoteTarget(), stamp, workResidueSource{Scope: "hq", Database: "hq", Drained: true}, 1); err != nil {
		t.Fatal(err)
	}
	if err := ensureWorkRemote(city, remoteCityConfig(), &strings.Builder{}); err != nil {
		t.Fatalf("marker-complete boot must no-op (post-sweep), got %v", err)
	}
	our, _ := org.Get("al-1")
	if recHasLabelBead(our, workTopologyMigratingLabel) {
		t.Fatalf("our quarantined row must be swept, labels=%v", our.Labels)
	}
	sibling, _ := org.Get("zz-9")
	if !recHasLabelBead(sibling, workTopologyMigratingLabel) {
		t.Fatalf("a SIBLING city's quarantined row must NEVER be swept, labels=%v", sibling.Labels)
	}
}

func TestMintTopologyStampRandom(t *testing.T) {
	a, err := mintTopologyStamp()
	if err != nil {
		t.Fatal(err)
	}
	b, _ := mintTopologyStamp()
	if a == b {
		t.Fatalf("mint must be random, got identical %q", a)
	}
	if !strings.HasPrefix(a, "gc-city:") {
		t.Fatalf("stamp must carry the gc-city: prefix, got %q", a)
	}
}

func TestWorkTopologyManagedDoltKeepAlive(t *testing.T) {
	// No remote marker → not external yet → false.
	if keep, err := workTopologyManagedDoltKeepAlive(t.TempDir()); err != nil || keep {
		t.Fatalf("no remote marker must not keep managed dolt alive: keep=%v err=%v", keep, err)
	}

	// A STARTED marker does not activate keep-alive.
	started := t.TempDir()
	if err := writeWorkRemoteStartedMarker(started, testRemoteTarget(), "gc-city:1234"); err != nil {
		t.Fatal(err)
	}
	if keep, err := workTopologyManagedDoltKeepAlive(started); err != nil || keep {
		t.Fatalf("a started (intent) marker must not keep managed dolt alive: keep=%v err=%v", keep, err)
	}

	// A COMPLETE marker with an undrained residue source → keep alive.
	city2 := t.TempDir()
	if err := finalizeWorkRemoteMarker(city2, testRemoteTarget(), "gc-city:abcd", workResidueSource{Scope: "hq", Database: "hq"}, 0); err != nil {
		t.Fatal(err)
	}
	if keep, err := workTopologyManagedDoltKeepAlive(city2); err != nil || !keep {
		t.Fatalf("undrained remote residue must keep managed dolt alive: keep=%v err=%v", keep, err)
	}

	// Drained → false (no undrained unify sources here).
	if err := markResidueSourceDrained(workRemoteMarkerPath(city2), workResidueSource{Scope: "hq", Database: "hq"}); err != nil {
		t.Fatal(err)
	}
	if keep, err := workTopologyManagedDoltKeepAlive(city2); err != nil || keep {
		t.Fatalf("drained remote residue must release managed dolt: keep=%v err=%v", keep, err)
	}
}

// TestWorkTopologyManagedDoltKeepAliveFailsClosed pins F12: a corrupt/unreadable
// remote marker returns keep-alive=true (fail closed), so a swallowed error still
// keeps the local server owned.
func TestWorkTopologyManagedDoltKeepAliveFailsClosed(t *testing.T) {
	city := t.TempDir()
	// Write a non-JSON remote marker so readWorkTopologyMarker errors.
	if err := writeCorruptRemoteMarker(city); err != nil {
		t.Fatal(err)
	}
	keep, err := workTopologyManagedDoltKeepAlive(city)
	if err == nil {
		t.Fatalf("a corrupt marker should surface a read error")
	}
	if !keep {
		t.Fatalf("keep-alive must FAIL CLOSED (true) on a marker read fault")
	}
}

func TestParseAllowedPrefixSet(t *testing.T) {
	got := parseAllowedPrefixSet("hq, al, be")
	for _, want := range []string{"hq", "al", "be"} {
		if !got[want] {
			t.Fatalf("expected %q present, got %v", want, got)
		}
	}
	if len(parseAllowedPrefixSet("(not set)")) != 0 {
		t.Fatalf("(not set) must parse as the empty set")
	}
	if len(parseAllowedPrefixSet("")) != 0 {
		t.Fatalf("empty must parse as the empty set")
	}
	set := parseAllowedPrefixSet("[HQ AL]")
	if !set["hq"] || !set["al"] {
		t.Fatalf("bracket/space/case tolerant parse failed: %v", set)
	}
}

func TestReconcileRemoteAllowedPrefixesReAppends(t *testing.T) {
	orig := workRemoteReadAllowedPrefixes
	origAdd := workRemoteAddPrefixToSet
	t.Cleanup(func() {
		workRemoteReadAllowedPrefixes = orig
		workRemoteAddPrefixToSet = origAdd
	})
	// The org DB currently holds only "hq"; "al" was evicted by a concurrent city.
	workRemoteReadAllowedPrefixes = func(beads.Store) (map[string]bool, error) {
		return map[string]bool{"hq": true}, nil
	}
	var added []string
	workRemoteAddPrefixToSet = func(_ beads.Store, p string) error {
		added = append(added, p)
		return nil
	}
	if err := reconcileRemoteAllowedPrefixes(newFakeWorkStore(), remoteCityConfig(), &strings.Builder{}); err != nil {
		t.Fatal(err)
	}
	if len(added) != 1 || added[0] != "al" {
		t.Fatalf("only the evicted prefix must be re-appended, got %v", added)
	}
}

// TestReconcileRemoteAllowedPrefixesGatewayVerifiesNoAppend pins deliverable C on a
// verify-mode (server-authoritative) gateway: the self-heal reads presence and, on
// a missing prefix, surfaces a doctor error and logs loudly — it never attempts
// add-to-set against the read-only config.
func TestReconcileRemoteAllowedPrefixesGatewayVerifiesNoAppend(t *testing.T) {
	origRead := workRemoteReadAllowedPrefixes
	origAdd := workRemoteAddPrefixToSet
	t.Cleanup(func() {
		workRemoteReadAllowedPrefixes = origRead
		workRemoteAddPrefixToSet = origAdd
	})
	workRemoteReadAllowedPrefixes = func(beads.Store) (map[string]bool, error) {
		return map[string]bool{"hq": true}, nil // "al" missing, server-provisioned incompletely
	}
	workRemoteAddPrefixToSet = func(beads.Store, string) error {
		t.Fatal("gateway self-heal must NEVER attempt add-to-set against read-only config")
		return nil
	}
	var log strings.Builder
	err := reconcileRemoteAllowedPrefixes(newFakeWorkStore(), gatewayRemoteCityConfig(), &log)
	if err == nil || !strings.Contains(err.Error(), "provision server-side") || !strings.Contains(err.Error(), "al") {
		t.Fatalf("gateway self-heal must surface a doctor error naming the missing prefix + provisioning, got %v", err)
	}
	if !strings.Contains(log.String(), "server-authoritative") {
		t.Fatalf("gateway self-heal must log loudly, got %q", log.String())
	}

	// All prefixes present on the gateway → clean, no error, no append.
	workRemoteReadAllowedPrefixes = func(beads.Store) (map[string]bool, error) {
		return map[string]bool{"hq": true, "al": true}, nil
	}
	if err := reconcileRemoteAllowedPrefixes(newFakeWorkStore(), gatewayRemoteCityConfig(), &strings.Builder{}); err != nil {
		t.Fatalf("gateway self-heal with all prefixes present must be clean, got %v", err)
	}
}

// TestRemoteResumeCopyPreProbeAborts pins F5/F10: the resume copy pre-probes the
// FULL incoming id set BEFORE the guarded upsert, so a foreign row (even one older
// than ours, which the guarded upsert would have replaced) is never overwritten.
func TestRemoteResumeCopyPreProbeAborts(t *testing.T) {
	ctx := context.Background()
	stamp := "gc-city:ourstamp0000000"
	remote := newFakeWorkStore()
	now := time.Now().UTC()
	// A FOREIGN bead on the remote OLDER than ours — the guarded upsert would have
	// replaced it; the pre-probe must abort first.
	remote.seed(&fakeWorkRec{
		id: "al-1", status: "open", updatedAt: now.Add(-time.Hour),
		metadata: map[string]string{workTopologySourceMetadataKey: "gc-city:foreign000000000"},
	})
	ours, err := recToSnapshot(&fakeWorkRec{
		id: "al-1", status: "open", updatedAt: now,
		metadata: map[string]string{workTopologySourceMetadataKey: stamp},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = remoteResumeCopy(ctx, remote, []beads.Snapshot{ours}, stamp)
	if err == nil || !strings.Contains(err.Error(), "foreign-city prefix collision") {
		t.Fatalf("resume copy must pre-probe and abort, got %v", err)
	}
	// Pre-destructive: the foreign row's stamp is untouched.
	fb, _ := remote.Get("al-1")
	if fb.Metadata[workTopologySourceMetadataKey] != "gc-city:foreign000000000" {
		t.Fatalf("foreign row must be untouched by a pre-destructive resume, got %v", fb.Metadata)
	}
}

// TestConvergeResidueRemoteMarkerDrains pins the generalized loop against a remote
// marker: two consecutive clean checks (F11) drain the local source into the org
// DB carrying the PERSISTED stamp.
func TestConvergeResidueRemoteMarkerDrains(t *testing.T) {
	city := t.TempDir()
	created := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	remote := newFakeWorkStore()
	local := newFakeWorkStore()
	local.seed(&fakeWorkRec{id: "al-1", status: "open", issueType: "task", createdAt: created, updatedAt: created})

	var added []string
	withWorkRemoteSeams(t, local, remote, &added)
	openWorkUnifyScopeStore = func(string, string) (beads.Store, func(), error) { return remote, func() {}, nil }

	stamp := "gc-city:persisted00000000"
	if err := finalizeWorkRemoteMarker(city, testRemoteTarget(), stamp, workResidueSource{Scope: "hq", Database: "hq"}, 0); err != nil {
		t.Fatal(err)
	}

	// First pass: imports + provisionally pending (F11), not yet drained.
	convergeWorkUnifiedResidue(city, remoteCityConfig(), &strings.Builder{})
	rb, err := remote.Get("al-1")
	if err != nil {
		t.Fatalf("residue row must be imported into the org DB: %v", err)
	}
	if rb.Metadata[workTopologySourceMetadataKey] != stamp {
		t.Fatalf("residue row must carry the persisted stamp, got %v", rb.Metadata)
	}
	m, _, _ := readWorkTopologyMarker(workRemoteMarkerPath(city))
	if m.undrainedResidueCount() != 1 || !m.ResidueSources[0].DrainPending {
		t.Fatalf("first clean check must be provisionally pending, not drained: %+v", m.ResidueSources)
	}

	// Second pass: the confirming clean check flips Drained.
	convergeWorkUnifiedResidue(city, remoteCityConfig(), &strings.Builder{})
	m, _, _ = readWorkTopologyMarker(workRemoteMarkerPath(city))
	if m.undrainedResidueCount() != 0 {
		t.Fatalf("second consecutive clean check must drain the source, undrained=%d", m.undrainedResidueCount())
	}
}

// TestConvergeResidueTwoMarkerRemoteProtocol pins F2/F4: with a remote marker
// present, an UNDRAINED unify (rig) source drains via the REMOTE protocol —
// org rows keep the persisted remote stamp (never the path-only local stamp) and
// the rig source drains without a false foreign collision.
func TestConvergeResidueTwoMarkerRemoteProtocol(t *testing.T) {
	city := t.TempDir()
	created := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	org := newFakeWorkStore()
	rig := newFakeWorkStore()
	// A rig row already copied to org under the REMOTE stamp (equal clock), as the
	// remote first-copy would have produced; the unify leg must NOT re-stamp it local.
	stamp := "gc-city:persisted99999999"
	org.seed(&fakeWorkRec{
		id: "al-7", status: "open", issueType: "task", createdAt: created, updatedAt: created,
		metadata: map[string]string{workTopologySourceMetadataKey: stamp},
	})
	rig.seed(&fakeWorkRec{id: "al-7", status: "open", issueType: "task", createdAt: created, updatedAt: created})

	var added []string
	withWorkRemoteSeams(t, org, org, &added) // city store resolves to org
	openWorkUnifyScopeStore = func(string, string) (beads.Store, func(), error) { return org, func() {}, nil }
	openWorkUnifyStragglerStore = func(string, workResidueSource) (beads.Store, func(), error) { return rig, func() {}, nil }
	// Class residue import is a no-op in this fake world.
	origClass := workUnifyImportRigClassResidue
	t.Cleanup(func() { workUnifyImportRigClassResidue = origClass })
	workUnifyImportRigClassResidue = func(string, *config.City, beads.Store, io.Writer) error { return nil }

	// Unified marker with an undrained rig source + a COMPLETE remote marker.
	if err := writeWorkUnifiedMarker(city, []workUnifyScope{{label: "alpha", database: "al"}}, nil, 0); err != nil {
		t.Fatal(err)
	}
	if err := finalizeWorkRemoteMarker(city, testRemoteTarget(), stamp, workResidueSource{Scope: "hq", Database: "hq", Drained: true}, 0); err != nil {
		t.Fatal(err)
	}

	// Two passes to clear the F11 two-check confirm; must never error a collision.
	for i := 0; i < 2; i++ {
		convergeWorkUnifiedResidue(city, remoteCityConfig(), &strings.Builder{})
	}
	// The org row keeps the REMOTE stamp (was NOT flipped to the local path stamp).
	b, _ := org.Get("al-7")
	if b.Metadata[workTopologySourceMetadataKey] != stamp {
		t.Fatalf("unify-leg residue must keep the remote stamp, got %v", b.Metadata)
	}
	// The rig source drained.
	m, _, _ := readWorkTopologyMarker(workUnifiedMarkerPath(city))
	if m.undrainedResidueCount() != 0 {
		t.Fatalf("rig source must drain via the remote protocol, undrained=%d", m.undrainedResidueCount())
	}
}

// TestReconcileWorkRemoteKeepAliveTick pins F7 dispatch: undrained → launch;
// drained → stop; non-remote → not handled.
func TestReconcileWorkRemoteKeepAliveTick(t *testing.T) {
	origLaunch := ensureManagedLocalDoltForKeepAlive
	origStop := stopManagedLocalDoltAfterKeepAlive
	t.Cleanup(func() {
		ensureManagedLocalDoltForKeepAlive = origLaunch
		stopManagedLocalDoltAfterKeepAlive = origStop
	})
	var launched, stopped bool
	ensureManagedLocalDoltForKeepAlive = func(string, io.Writer) { launched = true }
	stopManagedLocalDoltAfterKeepAlive = func(string, io.Writer) { stopped = true }

	// Non-remote city → not handled, neither called.
	if reconcileWorkRemoteKeepAliveTick(t.TempDir(), io.Discard) {
		t.Fatalf("non-remote city must not be handled")
	}
	if launched || stopped {
		t.Fatalf("non-remote city must not launch/stop")
	}

	// Completed remote, undrained → handled + launch (no published port in test).
	undrained := t.TempDir()
	if err := finalizeWorkRemoteMarker(undrained, testRemoteTarget(), "gc-city:ka1", workResidueSource{Scope: "hq", Database: "hq"}, 0); err != nil {
		t.Fatal(err)
	}
	launched, stopped = false, false
	if !reconcileWorkRemoteKeepAliveTick(undrained, io.Discard) {
		t.Fatalf("completed undrained remote must be handled")
	}
	if !launched || stopped {
		t.Fatalf("undrained → launch only, got launched=%v stopped=%v", launched, stopped)
	}

	// Completed remote, drained → handled + stop.
	drained := t.TempDir()
	if err := finalizeWorkRemoteMarker(drained, testRemoteTarget(), "gc-city:ka2", workResidueSource{Scope: "hq", Database: "hq"}, 0); err != nil {
		t.Fatal(err)
	}
	if err := markResidueSourceDrained(workRemoteMarkerPath(drained), workResidueSource{Scope: "hq", Database: "hq"}); err != nil {
		t.Fatal(err)
	}
	launched, stopped = false, false
	if !reconcileWorkRemoteKeepAliveTick(drained, io.Discard) {
		t.Fatalf("completed drained remote must be handled")
	}
	if launched || !stopped {
		t.Fatalf("drained → stop only, got launched=%v stopped=%v", launched, stopped)
	}
}

func TestWorkTopologyDoctorStateRemote(t *testing.T) {
	orig := workRemoteDoctorProbe
	t.Cleanup(func() { workRemoteDoctorProbe = orig })

	workRemoteDoctorProbe = func(string, *config.City) remoteDoctorStatus {
		return remoteDoctorStatus{reachable: true, prefixesPresent: true}
	}
	detail, err := workTopologyDoctorState(t.TempDir(), remoteCityConfig())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(detail, "remote-auth=ok") || !strings.Contains(detail, "prefixes=present") {
		t.Fatalf("remote doctor line missing auth/prefix surface: %q", detail)
	}

	workRemoteDoctorProbe = func(string, *config.City) remoteDoctorStatus {
		return remoteDoctorStatus{reachable: false, authDetail: "bad password", prefixesPresent: false, missing: []string{"al"}}
	}
	detail, err = workTopologyDoctorState(t.TempDir(), remoteCityConfig())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(detail, "remote-auth=unreachable") || !strings.Contains(detail, "prefixes=missing:al") {
		t.Fatalf("remote doctor line missing unreachable/missing surface: %q", detail)
	}
}

// TestRemoteTargetIsHostedGatewaySignal pins deliverable A: the gateway/plain arm
// keys on the EXPLICIT [beads.work] remote_config mode, not any inferred heuristic.
// The default (empty / "write") is PLAIN; only "verify" is a gateway.
func TestRemoteTargetIsHostedGatewaySignal(t *testing.T) {
	if remoteTargetIsHostedGateway(remoteCityConfig()) {
		t.Fatal("default remote_config (empty) must be PLAIN (writable)")
	}
	writeCfg := remoteCityConfig()
	writeCfg.Beads.Work.RemoteConfig = config.BeadsWorkRemoteConfigWrite
	if remoteTargetIsHostedGateway(writeCfg) {
		t.Fatal("remote_config=\"write\" must be PLAIN (writable)")
	}
	if !remoteTargetIsHostedGateway(gatewayRemoteCityConfig()) {
		t.Fatal("remote_config=\"verify\" must be a hosted GATEWAY")
	}
	if remoteTargetIsHostedGateway(nil) {
		t.Fatal("nil cfg must not be a gateway")
	}
}

// TestConfigStepRemoteAllowedPrefixesModeAware pins deliverable B in isolation:
// WRITE mode writes via add-to-set and re-read-guards; VERIFY mode verifies (read)
// and aborts on a missing prefix without ever writing.
func TestConfigStepRemoteAllowedPrefixesModeAware(t *testing.T) {
	origRead := workRemoteReadAllowedPrefixes
	origAdd := workRemoteAddPrefixToSet
	t.Cleanup(func() {
		workRemoteReadAllowedPrefixes = origRead
		workRemoteAddPrefixToSet = origAdd
	})

	t.Run("write mode writes the union then re-read passes", func(t *testing.T) {
		var added []string
		workRemoteAddPrefixToSet = func(_ beads.Store, p string) error { added = append(added, p); return nil }
		workRemoteReadAllowedPrefixes = func(beads.Store) (map[string]bool, error) {
			return map[string]bool{"hq": true, "al": true}, nil // reflects the write
		}
		if err := configStepRemoteAllowedPrefixes(newFakeWorkStore(), remoteCityConfig()); err != nil {
			t.Fatal(err)
		}
		if !sliceContains(added, "hq") || !sliceContains(added, "al") {
			t.Fatalf("write config step must add-to-set hq and al, got %v", added)
		}
	})

	t.Run("write mode re-read guard aborts on a silent no-op", func(t *testing.T) {
		workRemoteAddPrefixToSet = func(beads.Store, string) error { return nil } // silent no-op
		workRemoteReadAllowedPrefixes = func(beads.Store) (map[string]bool, error) {
			return map[string]bool{"hq": true}, nil // "al" never landed
		}
		err := configStepRemoteAllowedPrefixes(newFakeWorkStore(), remoteCityConfig())
		if err == nil || !strings.Contains(err.Error(), "al") || !strings.Contains(err.Error(), "remote_config") {
			t.Fatalf("re-read guard must abort naming the still-absent prefix + the verify hint, got %v", err)
		}
	})

	t.Run("verify mode verifies present, no write", func(t *testing.T) {
		workRemoteReadAllowedPrefixes = func(beads.Store) (map[string]bool, error) {
			return map[string]bool{"hq": true, "al": true}, nil
		}
		workRemoteAddPrefixToSet = func(beads.Store, string) error {
			t.Fatal("verify config step must NEVER write")
			return nil
		}
		if err := configStepRemoteAllowedPrefixes(newFakeWorkStore(), gatewayRemoteCityConfig()); err != nil {
			t.Fatalf("verify with all prefixes present must pass, got %v", err)
		}
	})

	t.Run("verify mode aborts on a missing prefix", func(t *testing.T) {
		workRemoteReadAllowedPrefixes = func(beads.Store) (map[string]bool, error) {
			return map[string]bool{"hq": true}, nil // "al" not provisioned server-side
		}
		workRemoteAddPrefixToSet = func(beads.Store, string) error {
			t.Fatal("verify config step must NEVER write, even on a missing prefix")
			return nil
		}
		err := configStepRemoteAllowedPrefixes(newFakeWorkStore(), gatewayRemoteCityConfig())
		if err == nil || !strings.Contains(err.Error(), "al") || !strings.Contains(err.Error(), "server-side") {
			t.Fatalf("verify must abort naming the missing prefix + server-side provisioning, got %v", err)
		}
	})
}

// TestEnsureWorkRemoteGatewayVerifiesPrefixes pins deliverable B end-to-end on a
// verify-mode target: with every required prefix present the migration completes
// and NEVER writes allowed_prefixes (verify-not-write).
func TestEnsureWorkRemoteGatewayVerifiesPrefixes(t *testing.T) {
	city := t.TempDir()
	writeUnifiedMarker(t, city)
	local := newFakeWorkStore()
	remote := newFakeWorkStore()
	created := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	local.seed(&fakeWorkRec{id: "al-1", status: "open", issueType: "task", createdAt: created, updatedAt: created})

	var added []string
	withWorkRemoteSeams(t, local, remote, &added)
	// The gateway already has both prefixes provisioned server-side.
	workRemoteReadAllowedPrefixes = func(beads.Store) (map[string]bool, error) {
		return map[string]bool{"hq": true, "al": true}, nil
	}

	if err := ensureWorkRemote(city, gatewayRemoteCityConfig(), &strings.Builder{}); err != nil {
		t.Fatalf("gateway migration with provisioned prefixes must succeed: %v", err)
	}
	if len(added) != 0 {
		t.Fatalf("gateway migration must VERIFY, never write allowed_prefixes, got %v", added)
	}
	m, ok, err := readWorkTopologyMarker(workRemoteMarkerPath(city))
	if err != nil || !ok || !m.isComplete() {
		t.Fatalf("gateway migration must complete the marker: ok=%v complete=%v err=%v", ok, ok && m.isComplete(), err)
	}
	if _, err := remote.Get("al-1"); err != nil {
		t.Fatalf("gateway migration must still copy work beads: %v", err)
	}
}

// TestEnsureWorkRemoteGatewayAbortsOnMissingPrefixes pins deliverable B's
// boot-blocking abort: a verify-mode target missing a required prefix aborts BEFORE
// any marker, names the missing prefix + server-side provisioning, and never writes.
func TestEnsureWorkRemoteGatewayAbortsOnMissingPrefixes(t *testing.T) {
	city := t.TempDir()
	writeUnifiedMarker(t, city)
	local := newFakeWorkStore()
	remote := newFakeWorkStore()
	created := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	local.seed(&fakeWorkRec{id: "al-1", status: "open", issueType: "task", createdAt: created, updatedAt: created})

	var added []string
	withWorkRemoteSeams(t, local, remote, &added)
	workRemoteReadAllowedPrefixes = func(beads.Store) (map[string]bool, error) {
		return map[string]bool{"hq": true}, nil // "al" NOT provisioned server-side
	}

	err := ensureWorkRemote(city, gatewayRemoteCityConfig(), &strings.Builder{})
	if err == nil || !strings.Contains(err.Error(), "al") || !strings.Contains(err.Error(), "server-side") {
		t.Fatalf("gateway missing-prefix must abort naming the prefix + server-side provisioning, got %v", err)
	}
	// Boot-blocking BEFORE the marker (like the credential preflight): no marker, no
	// write, no copy.
	if _, ok, _ := readWorkTopologyMarker(workRemoteMarkerPath(city)); ok {
		t.Fatal("gateway missing-prefix abort must leave NO marker")
	}
	if len(added) != 0 {
		t.Fatalf("gateway missing-prefix abort must not write allowed_prefixes, got %v", added)
	}
	if _, err := remote.Get("al-1"); err == nil {
		t.Fatal("gateway missing-prefix abort must not copy any work beads")
	}
}

// TestWorkTopologyDoctorStateGatewayMode pins deliverable D: the doctor line
// reflects the verify mode and, when a REACHABLE gateway is missing a prefix, the
// server-side provisioning hint. An UNREACHABLE gateway shows no misleading hint.
func TestWorkTopologyDoctorStateGatewayMode(t *testing.T) {
	origProbe := workRemoteDoctorProbe
	t.Cleanup(func() { workRemoteDoctorProbe = origProbe })

	workRemoteDoctorProbe = func(string, *config.City) remoteDoctorStatus {
		return remoteDoctorStatus{reachable: true, prefixesPresent: true}
	}
	detail, err := workTopologyDoctorState(t.TempDir(), gatewayRemoteCityConfig())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(detail, "remote-config=verify") || !strings.Contains(detail, "prefixes=present") {
		t.Fatalf("gateway doctor line must show verify mode + present prefixes: %q", detail)
	}

	workRemoteDoctorProbe = func(string, *config.City) remoteDoctorStatus {
		return remoteDoctorStatus{reachable: true, prefixesPresent: false, missing: []string{"al"}}
	}
	detail, err = workTopologyDoctorState(t.TempDir(), gatewayRemoteCityConfig())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(detail, "remote-config=verify") || !strings.Contains(detail, "prefixes=missing:al") || !strings.Contains(detail, "provision server-side") {
		t.Fatalf("reachable gateway doctor line must surface the missing prefix + provisioning hint: %q", detail)
	}

	// UNREACHABLE gateway: verify mode still shown, but NO provisioning hint (the
	// probe could not read the prefixes — blaming provisioning would mislead).
	workRemoteDoctorProbe = func(string, *config.City) remoteDoctorStatus {
		return remoteDoctorStatus{reachable: false, authDetail: "bad password", prefixesPresent: false}
	}
	detail, err = workTopologyDoctorState(t.TempDir(), gatewayRemoteCityConfig())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(detail, "remote-auth=unreachable") || !strings.Contains(detail, "remote-config=verify") {
		t.Fatalf("unreachable gateway doctor line must show unreachable + verify mode: %q", detail)
	}
	if strings.Contains(detail, "provision server-side") {
		t.Fatalf("unreachable gateway must NOT show the provisioning hint: %q", detail)
	}
}

func sliceContains(xs []string, want string) bool {
	for _, x := range xs {
		if x == want {
			return true
		}
	}
	return false
}

// writeCorruptRemoteMarker writes a non-JSON payload at the remote marker path so
// readWorkTopologyMarker returns a parse error (the fail-closed test).
func writeCorruptRemoteMarker(city string) error {
	path := workRemoteMarkerPath(city)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte("this is not json"), 0o644)
}
