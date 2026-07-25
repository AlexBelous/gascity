package main

// Work-bead topology: desired-state consultation, the one-way guard, and the
// never-silently-discard / post-write-verify rules
// (engdocs/design/beads-work-topology.md, "Topology-aware canonicalization"
// and "Config surface" one-way-doors).
//
// DARK on a marker-less, stamp-less city: loadWorkTopology returns the zero
// value, sharesCityDatabase() is false, no scope carries a provenance stamp,
// and every consuming resolver and guard is a no-op, so a scoped/managed city
// behaves byte-for-byte as it did before this slice. The markers are only ever
// created by the (future) unify and remote migration slices; the per-scope
// stamps are written only by a topology-driven re-point.

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/gastownhall/gascity/internal/beads/contract"
	"github.com/gastownhall/gascity/internal/config"
	"github.com/gastownhall/gascity/internal/fsys"
)

// errWorkRemoteMarkerMalformed reports a remote marker with no recorded target
// (a crash-window or buggy-migration artifact). Fail-closed like a parse error.
var errWorkRemoteMarkerMalformed = errors.New("work.remote marker is malformed: no recorded target endpoint")

// workTopology is the resolved work-scope topology for a city, read from the
// two markers. The zero value is the default topology (scoped work DBs, one
// per rig plus the city), which is what a marker-less city always resolves to.
type workTopology struct {
	unified        bool
	remote         bool
	remoteHost     string
	remotePort     string
	remoteDatabase string
}

// loadWorkTopology reads both work markers with ENOENT-only discipline. A
// non-ENOENT stat/read/parse failure surfaces as an error so a consuming
// resolver aborts the canonicalization rather than silently re-pointing a
// scope at the wrong database. A remote marker implies unified; a remote marker
// with no target is malformed and rejected.
func loadWorkTopology(cityPath string) (workTopology, error) {
	var t workTopology
	if _, ok, err := readWorkTopologyMarker(workUnifiedMarkerPath(cityPath)); err != nil {
		return workTopology{}, err
	} else if ok {
		t.unified = true
	}
	m, ok, err := readWorkTopologyMarker(workRemoteMarkerPath(cityPath))
	if err != nil {
		return workTopology{}, err
	}
	if ok {
		if m.Target == nil || strings.TrimSpace(m.Target.Host) == "" || strings.TrimSpace(m.Target.Port) == "" || strings.TrimSpace(m.Target.Database) == "" {
			return workTopology{}, errWorkRemoteMarkerMalformed
		}
		t.remote = true
		t.unified = true // remote implies unified
		t.remoteHost = strings.TrimSpace(m.Target.Host)
		t.remotePort = strings.TrimSpace(m.Target.Port)
		t.remoteDatabase = strings.TrimSpace(m.Target.Database)
	}
	return t, nil
}

// loadWorkTopologyBestEffort resolves the topology, degrading a marker
// stat/parse error to the zero (scoped) topology. It is used ONLY by the
// read-only best-effort database resolvers (defaultScopeDoltDatabase,
// canonicalScopeDoltDatabase). Every WRITER path resolves the topology up front
// via the error-returning loadWorkTopology and aborts on fault, so a degraded
// value is never persisted.
func loadWorkTopologyBestEffort(cityPath string) workTopology {
	t, _ := loadWorkTopology(cityPath)
	return t
}

// sharesCityDatabase reports whether the topology merges every scope's work
// beads into one database (unified or remote).
func (t workTopology) sharesCityDatabase() bool { return t.unified || t.remote }

// scopeDatabase returns the database name a scope resolves to under this
// resolved topology, or ("", false) when the topology does not relocate the
// scope's database (marker-less, or a scoped rig). The unified/managed arm
// resolves the city's ACTUAL database on disk (not the legacy "hq" constant),
// so rigs, residue, and verify all name the real shared database.
func (t workTopology) scopeDatabase(cityPath, dir string) (string, bool) {
	if samePath(cityPath, dir) {
		if t.remote {
			return t.remoteDatabase, true
		}
		// A unified/managed city keeps its own database; nothing to relocate.
		return "", false
	}
	if !t.sharesCityDatabase() {
		return "", false
	}
	if t.remote {
		return t.remoteDatabase, true
	}
	// Unified over the managed-local city: a rig's work beads live in the
	// city's ACTUAL database, resolved on-disk-first (readDeferredManagedDolt
	// database never consults the topology, so no recursion).
	return cityResolvedManagedDatabase(cityPath), true
}

// cityResolvedManagedDatabase returns the city's dolt_database from its
// metadata.json, falling back to the legacy default ("hq"). It never consults
// the topology markers.
func cityResolvedManagedDatabase(cityPath string) string {
	return readDeferredManagedDoltDatabase(
		filepath.Join(cityPath, ".beads", "metadata.json"),
		legacyDefaultScopeDoltDatabase(cityPath, cityPath, ""),
	)
}

// workTopologyScopeDatabase is the best-effort read-path helper
// defaultScopeDoltDatabase / canonicalScopeDoltDatabase consult.
func workTopologyScopeDatabase(cityPath, dir string) (string, bool) {
	return loadWorkTopologyBestEffort(cityPath).scopeDatabase(cityPath, dir)
}

// ── Deliverable B: topology-aware desired state ──────────────────────────

// workTopologyDesiredCityState returns the topology-desired city config state
// when a remote marker relocates the city endpoint, else (zero, false). Under
// remote the city is city_canonical with the recorded remote host/port.
func workTopologyDesiredCityState(cityPath string, cityPrefix string) (contract.ConfigState, bool) {
	return loadWorkTopologyBestEffort(cityPath).desiredCityState(cityPath, cityPrefix)
}

// desiredCityState is the threaded variant of workTopologyDesiredCityState.
func (t workTopology) desiredCityState(cityPath, cityPrefix string) (contract.ConfigState, bool) {
	if !t.remote {
		return contract.ConfigState{}, false
	}
	state := contract.ConfigState{
		IssuePrefix:    cityPrefix,
		EndpointOrigin: contract.EndpointOriginCityCanonical,
		DoltHost:       t.remoteHost,
		DoltPort:       t.remotePort,
	}
	state.DoltUser = preservedDoltUser(cityPath, state)
	state.EndpointStatus = preservedEndpointStatus(cityPath, state, contract.EndpointStatusUnverified)
	return state, true
}

// ── Deliverables C + D: never-silently-discard, provenance stamp, verify ──

// recordWorkTopologyResidueForScope appends a scope's CURRENT resolved database
// identity to the work marker's residue list when a work marker is present and
// that identity differs from the topology-desired target — the
// never-silently-discard rule. It fails closed on a scope-file read fault
// (aborting the canonicalization write), and treats the identity as recordable
// when EITHER config.yaml resolved OR metadata named a non-empty database
// (under a managed city the database name alone is the drain key). No-op (nil)
// on a marker-less city.
func recordWorkTopologyResidueForScope(cityPath, scopeRoot, scopeLabel string) error {
	topo, err := loadWorkTopology(cityPath)
	if err != nil {
		return err
	}
	return recordWorkTopologyResidueForScopeWithTopology(topo, cityPath, scopeRoot, scopeLabel)
}

func recordWorkTopologyResidueForScopeWithTopology(topo workTopology, cityPath, scopeRoot, scopeLabel string) error {
	if !topo.sharesCityDatabase() {
		return nil
	}
	wantDB, relocates := topo.scopeDatabase(cityPath, scopeRoot)
	if !relocates {
		// The topology does not relocate this scope (e.g. the city scope under a
		// unified/managed city keeps its own database), so there is nothing to
		// drain — never record the live database as a bogus residue source (F3).
		return nil
	}
	cur, err := resolveScopeObservedIdentity(cityPath, scopeRoot)
	if err != nil {
		// Fail closed: resolveScopeObservedIdentity reads only .beads files, so a
		// non-nil error is a genuine fault — abort the canonicalization rather
		// than re-point over an unread legacy identity.
		return err
	}
	if !cur.recordable() {
		return nil // no existing config.yaml AND no legacy database name — nothing to drain
	}
	if identityMatchesTopologyTarget(cur, topo, wantDB) {
		return nil // already re-pointed; no residue
	}
	markerPath := workUnifiedMarkerPath(cityPath)
	if topo.remote {
		markerPath = workRemoteMarkerPath(cityPath)
	}
	return appendWorkResidueSource(markerPath, workResidueSource{
		Scope:    scopeLabel,
		Host:     cur.host,
		Port:     cur.port,
		Database: cur.database,
	})
}

// stampWorkTopologyScope writes the per-scope provenance stamp when the
// topology relocates this scope's database (a topology-driven re-point), then
// verifies the re-point resolved to the intended target. No-op on a marker-less
// city or a scope the topology does not relocate.
func stampWorkTopologyScope(topo workTopology, cityPath, scopeRoot, scopeLabel string) error {
	if !topo.sharesCityDatabase() {
		return nil
	}
	wantDB, relocates := topo.scopeDatabase(cityPath, scopeRoot)
	if !relocates {
		return nil // e.g. the city scope under unified/managed keeps its own db
	}
	kind := workMarkerKindUnified
	stamp := &workTopologyStamp{Kind: kind, Database: wantDB}
	if topo.remote {
		stamp.Kind = workMarkerKindRemote
		stamp.Host = canonicalWorkHost(topo.remoteHost, topo.remotePort)
		stamp.Port = strings.TrimSpace(topo.remotePort)
	}
	if err := writeWorkTopologyStamp(scopeRoot, stamp); err != nil {
		return err
	}
	return verifyWorkTopologyScopeWithTopology(topo, cityPath, scopeRoot, scopeLabel, wantDB)
}

// scopeDoltDatabaseForTopology returns a scope's database from a resolved
// topology value (no marker re-read), matching canonicalScopeDoltDatabase's
// on-disk-first fallback when the topology does not relocate the scope. Writer
// paths that resolved the topology up front thread this instead of the
// best-effort resolver, so a transient marker fault (already caught up front)
// can never degrade the written value.
func scopeDoltDatabaseForTopology(topo workTopology, cityPath, dir, prefix string) string {
	if db, ok := topo.scopeDatabase(cityPath, dir); ok {
		return db
	}
	return readDeferredManagedDoltDatabase(filepath.Join(dir, ".beads", "metadata.json"), legacyDefaultScopeDoltDatabase(cityPath, dir, prefix))
}

// verifyWorkTopologyScope re-resolves a scope after a topology-driven write and
// confirms it points at the intended target. No-op on a marker-less city or a
// scope the topology does not relocate.
func verifyWorkTopologyScope(cityPath, scopeRoot, scopeLabel string) error {
	topo, err := loadWorkTopology(cityPath)
	if err != nil {
		return err
	}
	if !topo.sharesCityDatabase() {
		return nil
	}
	wantDB, relocates := topo.scopeDatabase(cityPath, scopeRoot)
	if !relocates {
		return nil
	}
	return verifyWorkTopologyScopeWithTopology(topo, cityPath, scopeRoot, scopeLabel, wantDB)
}

// verifyWorkTopologyScopeWithTopology re-resolves a scope after a topology write
// and confirms it points at the intended target (deliverable D). A managed scope
// whose runtime port is not yet published is skipped (not a mismatch); a genuine
// (Host, Port, Database) disagreement is an error.
func verifyWorkTopologyScopeWithTopology(topo workTopology, cityPath, scopeRoot, scopeLabel, wantDB string) error {
	target, err := contract.ResolveDoltConnectionTarget(fsys.OSFS{}, cityPath, scopeRoot)
	if err != nil {
		if contract.IsManagedRuntimeUnavailable(err) {
			return nil // runtime not up; cannot verify against a live endpoint yet
		}
		return fmt.Errorf("verifying work topology for %s: %w", scopeLabel, err)
	}
	if strings.TrimSpace(target.Database) != strings.TrimSpace(wantDB) {
		return fmt.Errorf("work topology re-point of %s wrote database %q but resolves to %q", scopeLabel, wantDB, target.Database)
	}
	if topo.remote {
		if canonicalWorkHost(target.Host, target.Port) != canonicalWorkHost(topo.remoteHost, topo.remotePort) || strings.TrimSpace(target.Port) != strings.TrimSpace(topo.remotePort) {
			return fmt.Errorf("work topology re-point of %s wrote endpoint %s:%s but resolves to %s:%s",
				scopeLabel, topo.remoteHost, topo.remotePort, target.Host, target.Port)
		}
	}
	return nil
}

// ── Deliverable E: the one-way guard (marker arms + stamp-based observed) ──

// checkWorkTopologyMarkers is the shared one-way enforcement called from the
// controller boot lifecycle, config reload, the shared work-store resolution
// seam, and the doBd front door. It refuses a config that contradicts the
// durable unify/remote markers OR the per-scope provenance stamps, so a
// unify/remote city can never be silently reverted to scoped/managed.
//
// DARK on a marker-less, stamp-less city: every arm is a no-op.
func checkWorkTopologyMarkers(cityPath string, cfg *config.City) error {
	if strings.TrimSpace(cityPath) == "" || cfg == nil {
		return nil
	}

	unifiedPresent, err := workMarkerPresent(workUnifiedMarkerPath(cityPath))
	if err != nil {
		return err
	}
	remoteMarker, remotePresent, err := readWorkTopologyMarker(workRemoteMarkerPath(cityPath))
	if err != nil {
		return err
	}

	work := cfg.Beads.Work
	configUnified := work.IsUnified()
	configRemote := work.IsRemote()

	// Marker arm 1: unified is one-way — a present unified marker forbids scoped.
	if unifiedPresent && !configUnified {
		return fmt.Errorf("work.unified marker present but [beads.work] scope is %q; unifying work beads is one-way (scoped is rejected once unified)", work.EffectiveScope())
	}
	// Marker arm 2: remote is one-way — a present remote marker forbids managed.
	if remotePresent {
		if remoteMarker == nil || remoteMarker.Target == nil || strings.TrimSpace(remoteMarker.Target.Host) == "" || strings.TrimSpace(remoteMarker.Target.Port) == "" || strings.TrimSpace(remoteMarker.Target.Database) == "" {
			return errWorkRemoteMarkerMalformed
		}
		if !configRemote {
			return fmt.Errorf("work.remote marker present but [beads.work] target is %q; a remote work DB is one-way (managed is rejected once remote)", work.EffectiveTarget())
		}
		// Marker arm 3: no silent retarget.
		host, port, database, _ := work.RemoteTarget()
		if !sameRemoteEndpoint(*remoteMarker.Target, host, port, database) {
			return fmt.Errorf("work.remote marker recorded target dolt://%s:%s/%s but [beads.work] target is dolt://%s:%d/%s; the remote work target cannot be changed",
				remoteMarker.Target.Host, remoteMarker.Target.Port, remoteMarker.Target.Database, host, port, database)
		}
	}

	// Marker arm 4 (F11): a unified/remote city merges every rig's work beads
	// into the shared city database, so a rig cannot declare a private explicit
	// dolt endpoint — it would route that rig's writes to its own server while
	// routed readers use the shared DB (split-brain).
	if unifiedPresent || remotePresent {
		resolveRigPaths(cityPath, cfg.Rigs)
		for i := range cfg.Rigs {
			if strings.TrimSpace(cfg.Rigs[i].DoltHost) != "" || strings.TrimSpace(cfg.Rigs[i].DoltPort) != "" {
				return fmt.Errorf("rig %q declares an explicit dolt endpoint (%s:%s) but a work-topology marker is present; unified/remote rigs must inherit the shared city work database — remove the rig's dolt_host/dolt_port",
					cfg.Rigs[i].Name, cfg.Rigs[i].DoltHost, cfg.Rigs[i].DoltPort)
			}
		}
	}

	// Observed-state arms keyed on POSITIVE per-scope provenance stamps (never a
	// bare database-name coincidence). Stamps live in scope files and survive
	// marker loss, so these refuse a reverted config even if a marker was
	// deleted — while a legacy city that carries no stamp stays DARK.
	return checkWorkTopologyStamps(cityPath, cfg, configUnified, configRemote)
}

// checkWorkTopologyStamps refuses a config that contradicts a per-scope
// provenance stamp. A stamp read fault surfaces (fail-closed).
func checkWorkTopologyStamps(cityPath string, cfg *config.City, configUnified, configRemote bool) error {
	work := cfg.Beads.Work
	for _, scope := range workTopologyScopes(cityPath, cfg) {
		stamp, ok, err := readWorkTopologyStamp(scope.root)
		if err != nil {
			return err
		}
		if !ok {
			continue
		}
		if !configUnified {
			return fmt.Errorf("scope %q carries a %s work-topology stamp but [beads.work] scope is %q; unifying work beads is one-way",
				scope.label, stamp.Kind, work.EffectiveScope())
		}
		if stamp.Kind == workMarkerKindRemote {
			if !configRemote {
				return fmt.Errorf("scope %q carries a remote work-topology stamp (db %q) but [beads.work] target is %q; a remote work DB is one-way",
					scope.label, stamp.Database, work.EffectiveTarget())
			}
			host, port, database, _ := work.RemoteTarget()
			if canonicalWorkHost(stamp.Host, stamp.Port) != canonicalWorkHost(host, strconv.Itoa(port)) || strings.TrimSpace(stamp.Port) != strconv.Itoa(port) || strings.TrimSpace(stamp.Database) != strings.TrimSpace(database) {
				return fmt.Errorf("scope %q recorded remote work target %s:%s/%s but [beads.work] target is dolt://%s:%d/%s; the remote work target cannot be changed",
					scope.label, stamp.Host, stamp.Port, stamp.Database, host, port, database)
			}
		}
	}
	return nil
}

// workMarkerPresent reports whether a marker exists (ENOENT-only). Any other
// stat/parse fault surfaces as an error.
func workMarkerPresent(path string) (bool, error) {
	_, ok, err := readWorkTopologyMarker(path)
	return ok, err
}

// scopeRef is one scope's root + human label for the guard/doctor surfaces.
type scopeRef struct {
	root  string
	label string
}

// workTopologyScopes returns the city scope plus every bound rig scope.
func workTopologyScopes(cityPath string, cfg *config.City) []scopeRef {
	refs := []scopeRef{{root: cityPath, label: "hq"}}
	resolveRigPaths(cityPath, cfg.Rigs)
	for _, rig := range cfg.Rigs {
		if strings.TrimSpace(rig.Path) == "" {
			continue
		}
		refs = append(refs, scopeRef{root: resolveStoreScopeRoot(cityPath, rig.Path), label: rig.Name})
	}
	return refs
}

// ── cached variant for hot resolution seams (store-open, doBd) ────────────

type workTopologyCheckKey struct {
	cityPath      string
	scope         string
	target        string
	unifiedMarker string
	remoteMarker  string
}

type workTopologyCheckResult struct{ err error }

var workTopologyCheckCache sync.Map // workTopologyCheckKey -> workTopologyCheckResult

// checkWorkTopologyMarkersCached memoizes checkWorkTopologyMarkers keyed by the
// city path, the work-config knobs, and the two marker stat signatures, so the
// hot store-open / doBd paths pay one map lookup plus two marker stats on a
// cache hit. A marker appearing/changing invalidates the entry; stamps only
// appear alongside a marker write, so the cache never serves a stale pass on the
// normal migration flow. Marker-less/stamp-less cities are cached as DARK.
func checkWorkTopologyMarkersCached(cityPath string, cfg *config.City) error {
	if strings.TrimSpace(cityPath) == "" || cfg == nil {
		return nil
	}
	key := workTopologyCheckKey{
		cityPath:      normalizePathForCompare(cityPath),
		scope:         cfg.Beads.Work.EffectiveScope(),
		target:        cfg.Beads.Work.EffectiveTarget(),
		unifiedMarker: workMarkerStatSig(workUnifiedMarkerPath(cityPath)),
		remoteMarker:  workMarkerStatSig(workRemoteMarkerPath(cityPath)),
	}
	if v, ok := workTopologyCheckCache.Load(key); ok {
		return v.(workTopologyCheckResult).err
	}
	err := checkWorkTopologyMarkers(cityPath, cfg)
	workTopologyCheckCache.Store(key, workTopologyCheckResult{err: err})
	return err
}

// workMarkerStatSig returns a cheap stat signature ("absent", "mtime:size", or
// an error marker) for cache keying.
func workMarkerStatSig(path string) string {
	info, err := os.Stat(path)
	if err != nil {
		if workMarkerFileAbsent(err) {
			return "absent"
		}
		return "staterr:" + err.Error()
	}
	return fmt.Sprintf("%d:%d", info.ModTime().UnixNano(), info.Size())
}

// ── observed-identity resolution (pure .beads file reads) ─────────────────

// scopeObservedIdentity is a scope's resolved work-DB identity, read from its
// .beads/config.yaml (endpoint) and metadata.json (database). It deliberately
// avoids the managed-runtime port lookup so residue recording works at boot
// before Dolt is up: two managed scopes on one city share the same local server,
// so a database-name match is a shared-DB match without the live port.
type scopeObservedIdentity struct {
	present  bool // config.yaml exists
	external bool
	host     string
	port     string
	database string
	origin   contract.EndpointOrigin
}

// recordable reports whether the identity carries enough to be a drain source:
// a config.yaml existed OR metadata named a non-empty database (under a managed
// city the database name alone is the full drain key).
func (id scopeObservedIdentity) recordable() bool {
	return id.present || strings.TrimSpace(id.database) != ""
}

// resolveScopeObservedIdentity reads a scope's endpoint + database from its
// .beads files. Inherited rigs consult the city to decide external-ness and to
// mirror the city's host/port.
func resolveScopeObservedIdentity(cityPath, scopeRoot string) (scopeObservedIdentity, error) {
	cfg, ok, err := contract.ReadConfigState(fsys.OSFS{}, filepath.Join(scopeRoot, ".beads", "config.yaml"))
	if err != nil {
		return scopeObservedIdentity{}, fmt.Errorf("reading scope config for %s: %w", scopeRoot, err)
	}
	id := scopeObservedIdentity{present: ok}
	if db, dok, derr := contract.ReadDoltDatabase(fsys.OSFS{}, filepath.Join(scopeRoot, ".beads", "metadata.json")); derr != nil {
		return scopeObservedIdentity{}, fmt.Errorf("reading scope database for %s: %w", scopeRoot, derr)
	} else if dok {
		id.database = strings.TrimSpace(db)
	}
	if !ok {
		return id, nil
	}
	id.origin = cfg.EndpointOrigin
	switch cfg.EndpointOrigin {
	case contract.EndpointOriginCityCanonical, contract.EndpointOriginExplicit:
		id.external = true
		id.host = canonicalWorkHost(cfg.DoltHost, cfg.DoltPort)
		id.port = strings.TrimSpace(cfg.DoltPort)
	case contract.EndpointOriginInheritedCity:
		if strings.TrimSpace(cfg.DoltHost) != "" || strings.TrimSpace(cfg.DoltPort) != "" {
			id.external = true
			id.host = canonicalWorkHost(cfg.DoltHost, cfg.DoltPort)
			id.port = strings.TrimSpace(cfg.DoltPort)
		} else if !samePath(scopeRoot, cityPath) {
			if cityID, cerr := resolveScopeObservedIdentity(cityPath, cityPath); cerr == nil && cityID.external {
				id.external = true
				id.host = cityID.host
				id.port = cityID.port
			}
		}
	case contract.EndpointOriginManagedCity:
		// Managed local server; host/port empty (both scopes share it).
	}
	return id, nil
}

// identityMatchesTopologyTarget reports whether an observed identity already
// points at the topology-desired database (and, for remote, the remote
// endpoint) — i.e. it has been re-pointed and carries no residue.
func identityMatchesTopologyTarget(cur scopeObservedIdentity, topo workTopology, wantDB string) bool {
	if strings.TrimSpace(cur.database) != strings.TrimSpace(wantDB) {
		return false
	}
	if topo.remote {
		return cur.external && canonicalWorkHost(cur.host, cur.port) == canonicalWorkHost(topo.remoteHost, topo.remotePort) && strings.TrimSpace(cur.port) == strings.TrimSpace(topo.remotePort)
	}
	return true
}

// sameRemoteEndpoint compares a recorded marker target with a parsed config
// remote target (host canonicalized, port as string).
func sameRemoteEndpoint(recorded workTopologyTarget, host string, port int, database string) bool {
	return canonicalWorkHost(recorded.Host, recorded.Port) == canonicalWorkHost(host, strconv.Itoa(port)) &&
		strings.TrimSpace(recorded.Port) == strconv.Itoa(port) &&
		strings.TrimSpace(recorded.Database) == strings.TrimSpace(database)
}

// canonicalWorkHost folds loopback spellings (empty-with-port, localhost, ::1,
// [::1]) to 127.0.0.1 so host-spelling differences never split one physical
// endpoint into two. Non-loopback hosts are returned trimmed (both compared
// sides pass through here, so identical spellings compare equal and genuinely
// different hosts stay distinct). It is the single host-canonicalization helper
// used by residue dedup, arm 3, the stamp retarget arm, identity matching, and
// the post-write verify.
func canonicalWorkHost(host, port string) string {
	h := strings.TrimSpace(host)
	folded := strings.Trim(strings.ToLower(h), "[]")
	if folded == "" {
		if strings.TrimSpace(port) != "" {
			return "127.0.0.1"
		}
		return ""
	}
	switch folded {
	case "localhost", "127.0.0.1", "::1", "0:0:0:0:0:0:0:1":
		return "127.0.0.1"
	}
	return h
}
