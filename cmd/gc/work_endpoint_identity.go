package main

// Deliverable A of the work-topology runtime aftermath
// (engdocs/design/beads-work-topology.md, "Endpoint-identity dedup where handles
// can't be shared"): a shared, cached answer to "do these two scope roots resolve
// to the same (Host, Port, Database)?".
//
// Unify makes N+1 scope directories resolve to ONE database and remote makes
// that database org-shared, so the candidate builders that enumerate scope DIRS
// must collapse endpoint-identical scopes before probing (else a
// release/reassign/count acts once per aliased leg). This helper is that
// collapse key. It reuses the observed-identity reader (pure .beads file reads,
// managed-runtime-port-free so it works at boot before Dolt is up) and the single
// host-canonicalization helper (canonicalWorkHost), and is cached on the scope's
// (and, for inherited rigs, the city's) config/metadata stat signatures — the
// same cheap-per-tick approach as checkWorkTopologyMarkersCached.
//
// DARK on a marker-less city: the identity read is the same one residue recording
// already performs, and endpoint dedup only ever COLLAPSES scopes that genuinely
// resolve to one database. A scoped city's rigs resolve to distinct databases, so
// their keys differ and nothing collapses — behavior is byte-identical.

import (
	"path/filepath"
	"strings"
	"sync"

	"github.com/gastownhall/gascity/internal/config"
)

// scopeEndpointKey is the canonical (external, host, port, database) tuple two
// scope roots must share to resolve to one physical work database. Managed
// scopes carry empty host/port (both scopes on a city share the one local
// server), so a database-name match alone is a shared-DB match for them — the
// same reasoning scopeObservedIdentity documents. External scopes additionally
// compare the canonicalized host and port.
type scopeEndpointKey struct {
	external bool
	host     string
	port     string
	database string
}

// resolvable reports whether the scope carried enough .beads state to name a
// database. An unresolvable scope (no config.yaml AND no metadata database) has
// no addressable endpoint, so it must never be treated as aliasing another
// scope — two unresolvable scopes are NOT the same endpoint.
func (k scopeEndpointKey) resolvable() bool { return k.database != "" }

func (id scopeObservedIdentity) endpointKey() scopeEndpointKey {
	return scopeEndpointKey{
		external: id.external,
		host:     canonicalWorkHost(id.host, id.port),
		port:     strings.TrimSpace(id.port),
		database: strings.TrimSpace(id.database),
	}
}

type scopeEndpointCacheKey struct {
	cityPath    string
	scopeRoot   string
	scopeConfig string
	scopeMeta   string
	cityConfig  string
	cityMeta    string
}

type scopeEndpointCacheValue struct {
	key scopeEndpointKey
	err error
}

var scopeEndpointKeyCache sync.Map // scopeEndpointCacheKey -> scopeEndpointCacheValue

// resolveScopeEndpointKeyCached resolves a scope's endpoint key, memoized on the
// scope's config.yaml + metadata.json stat signatures (and the city's, because
// an inherited rig consults the city to decide external-ness and mirror its
// host/port). A .beads write invalidates the entry via the stat signature, so
// the cache never serves a stale key across a re-point.
func resolveScopeEndpointKeyCached(cityPath, scopeRoot string) (scopeEndpointKey, error) {
	key := scopeEndpointCacheKey{
		cityPath:    normalizePathForCompare(cityPath),
		scopeRoot:   normalizePathForCompare(scopeRoot),
		scopeConfig: workMarkerStatSig(filepath.Join(scopeRoot, ".beads", "config.yaml")),
		scopeMeta:   workMarkerStatSig(filepath.Join(scopeRoot, ".beads", "metadata.json")),
		cityConfig:  workMarkerStatSig(filepath.Join(cityPath, ".beads", "config.yaml")),
		cityMeta:    workMarkerStatSig(filepath.Join(cityPath, ".beads", "metadata.json")),
	}
	if v, ok := scopeEndpointKeyCache.Load(key); ok {
		val := v.(scopeEndpointCacheValue)
		return val.key, val.err
	}
	id, err := resolveScopeObservedIdentity(cityPath, scopeRoot)
	val := scopeEndpointCacheValue{}
	if err != nil {
		val.err = err
	} else {
		val.key = id.endpointKey()
	}
	scopeEndpointKeyCache.Store(key, val)
	return val.key, val.err
}

// workTopologyElidesClassSweepScope reports whether a class residue sweep must
// SKIP this rig scope (deliverable E). Once a work marker shares the city
// database, a re-pointed rig resolves to that shared/org database, so sourcing
// it would re-scan the shared DB — over WAN once remote, with deletion authority
// in a multi-tenant DB. The rig's class beads were migrated from its LEGACY
// database before the re-point, and the recorded residue sources drain any late
// arrivals, so the re-pointed rig leg is pure redundant hazard.
//
// It fires ONLY on a topology-active city and ONLY for a rig scope that
// positively resolves to the city endpoint; a not-yet-re-pointed rig (still on
// its own legacy database) is NOT elided, so its class residue is still swept.
// DARK: false on a marker-less city (nothing shares), for the city scope itself,
// and on any read fault (keep sourcing — the pre-existing, safe behavior; the
// recorded-residue-source drain remains the primary correctness mechanism).
func workTopologyElidesClassSweepScope(cityPath, scopeRoot string) bool {
	if samePath(cityPath, scopeRoot) {
		return false // never elide the city scope itself
	}
	topo, err := loadWorkTopology(cityPath)
	if err != nil || !topo.sharesCityDatabase() {
		return false
	}
	same, err := sameResolvedWorkEndpoint(cityPath, cityPath, scopeRoot)
	if err != nil {
		return false
	}
	return same
}

// remoteWorkReadPrefixes returns the city's own work-bead prefix set to
// constrain aggregate/list/count reads on a REMOTE-target city (the work.remote
// marker is complete), and (nil, false) otherwise (deliverable D's remote
// application). On a shared org work DB an unconstrained read counts other
// cities' work, so every read surface must scope to these prefixes and BYPASS
// the beads.Counter fast path (org-wide counts are wrong). DARK on
// scoped/unified/managed cities: returns (nil, false), leaving today's paths —
// Counter fast path included — untouched. Fails DARK on a marker read fault
// (loadWorkTopology error) rather than silently applying a partial filter; a
// running controller has already validated its markers fail-closed at boot.
func remoteWorkReadPrefixes(cityPath string, cfg *config.City) ([]string, bool) {
	topo, err := loadWorkTopology(cityPath)
	if err != nil || !topo.remote {
		return nil, false
	}
	prefixes := config.CityWorkPrefixes(cfg)
	if len(prefixes) == 0 {
		return nil, false
	}
	return prefixes, true
}

// workTopologyActive reports whether cityPath carries an active work-topology
// marker (unified or remote). It is the DARK gate for the residual
// endpoint-identity dedups (deliverable C): a marker-less city returns false, so
// every fan-out that enumerates scope DIRS stays on its exact pre-topology code
// path. It degrades a marker read fault to false (best-effort), matching the
// class-sweep-elision gate — the dedup is an optimization/correctness collapse
// that only ever merges scopes genuinely sharing one database, never a behavior
// a scoped city depends on.
func workTopologyActive(cityPath string) bool {
	return loadWorkTopologyBestEffort(cityPath).sharesCityDatabase()
}

// dedupEndpointIdenticalScopeRoots collapses scope roots that resolve to the
// same physical work endpoint, preserving order and keeping the FIRST
// occurrence. It is the residual-C collapse for the fan-outs that enumerate
// scope DIRS (convoy candidates, the work-assignment sweep), where the
// shared-handle registry's instance identity does not reach.
//
// It is CONSERVATIVE on a read fault: a root whose identity cannot be resolved
// (or cannot be compared to a kept root) is KEPT and probed on its own, never
// silently dropped — the same discipline sameResolvedWorkEndpoint documents.
// Callers gate it on workTopologyActive so a marker-less city never pays the
// per-pair identity read and stays byte-identical.
func dedupEndpointIdenticalScopeRoots(cityPath string, roots []string) []string {
	if len(roots) < 2 {
		return roots
	}
	out := make([]string, 0, len(roots))
	for _, root := range roots {
		aliased := false
		for _, kept := range out {
			same, err := sameResolvedWorkEndpoint(cityPath, kept, root)
			if err == nil && same {
				aliased = true
				break
			}
		}
		if !aliased {
			out = append(out, root)
		}
	}
	return out
}

// sameResolvedWorkEndpoint reports whether two scope roots resolve to the same
// (Host, Port, Database) work database. It is the collapse key for the
// candidate-builder dedups: aliased scopes (post-unify/remote) return true so a
// fan-out probes their shared endpoint once.
//
// It is CONSERVATIVE on ambiguity: a scope that carries no addressable endpoint
// (unresolvable) is never reported as aliasing another, and a read fault
// surfaces as an error so a caller can choose to probe both rather than silently
// drop a leg. Identical roots always alias (an identity fast path that also
// covers an unresolvable scope compared to itself).
func sameResolvedWorkEndpoint(cityPath, rootA, rootB string) (bool, error) {
	if samePath(rootA, rootB) {
		return true, nil
	}
	ka, err := resolveScopeEndpointKeyCached(cityPath, rootA)
	if err != nil {
		return false, err
	}
	kb, err := resolveScopeEndpointKeyCached(cityPath, rootB)
	if err != nil {
		return false, err
	}
	if !ka.resolvable() || !kb.resolvable() {
		return false, nil
	}
	return ka == kb, nil
}
