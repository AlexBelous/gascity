package main

// Unify migration (engdocs/design/beads-work-topology.md, "Unify migration"):
// on a controller boot of a [beads.work] scope="unified" city whose rig scopes
// still resolve to their own per-rig work databases, ensureWorkUnified merges
// every rig's WORK beads into the city scope's database — ids, status, and all
// clocks preserved via the Slice-2 snapshot copy primitive — then writes the
// work.unified marker and drives the marker-aware canonicalizer to re-point
// every scope at the shared database.
//
// BOOT-BLOCKING: unlike the return-false-and-continue class migrations, a failed
// or aborted unify refuses the controller boot (the error is surfaced through
// newCityRuntime.bootBlockingErr, checked at every start/reload call site the
// same way checkWorkTopologyMarkers's refusal is). A partial copy is therefore
// never exposed to a live reconciler/dispatcher. Because CLI one-shots run
// WITHOUT the controller, every copied row additionally carries the
// gc.topology_migrating quarantine label until the marker step clears it, so a
// mid-copy row is invisible to the hook/claim/ready surfaces even outside the
// boot-block (work_unify_quarantine.go).
//
// DARK on a scoped/managed city: ensureWorkUnified returns nil immediately when
// the work scope is not unified.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/gastownhall/gascity/internal/beadmeta"
	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/beads/contract"
	nudgesdb "github.com/gastownhall/gascity/internal/classdb/nudges"
	sessionsdb "github.com/gastownhall/gascity/internal/classdb/sessions"
	"github.com/gastownhall/gascity/internal/config"
	"github.com/gastownhall/gascity/internal/coordclass"
	"github.com/gastownhall/gascity/internal/fsys"
)

// workUnifyVerifySampleSize bounds the per-rig copy-verify sample. Every flagged
// id (kept-local, stale-skipped, updated) is verified in addition to this
// sample, so the bound only caps the redundant re-read of freshly-inserted rows.
const workUnifyVerifySampleSize = 16

// ── injectable seams (real exec-backed defaults; overridden in unit tests) ──

// openWorkUnifyScopeStore opens a scope's RAW work store (the concrete
// BdStore/NativeDoltStore that implements the snapshot Export/Import/Fetch
// capabilities — the bead-policy wrapper hides optional interfaces, so it is
// unwrapped here). The returned close func releases the underlying handle.
var openWorkUnifyScopeStore = func(cityPath, scopeRoot string) (beads.Store, func(), error) {
	wrapped, err := openStoreAtForCity(scopeRoot, cityPath)
	if err != nil {
		return nil, func() {}, err
	}
	raw, _, _ := unwrapBeadPolicyStore(wrapped)
	return raw, func() { closeBeadStoreHandle(wrapped) }, nil //nolint:errcheck // best-effort close
}

// workUnifyRequireBdProvider refuses a non-bd city work store EARLY (F11): the
// unify prefix-override mint and the allowed_prefixes add-to-set are bd-leg only
// in this slice. A native/other provider must keep scope="scoped".
var workUnifyRequireBdProvider = func(cityStore beads.Store) error {
	if _, ok := cityStore.(*beads.BdStore); ok {
		return nil
	}
	return fmt.Errorf("work unify blocked: scope=\"unified\" currently requires the bd work-store provider; keep scope=\"scoped\" or switch the work provider")
}

// workUnifyProbeCapability answers the bd prefix-override capability gate for the
// city work store (the provider gate has already guaranteed a BdStore).
var workUnifyProbeCapability = func(cityStore beads.Store) (bool, error) {
	if bd, ok := cityStore.(*beads.BdStore); ok {
		return bd.HasWorkspacePrefixMintCapability()
	}
	return false, fmt.Errorf("capability probe requires the bd provider")
}

// workUnifyIsCounterModeWorkDB detects a counter-mode (sequential-id) work
// database, which v1 refuses to unify (no per-prefix counter-advance guard).
// The fleet default is hash-id, which is immune, so the default returns false; a
// counter-mode backend would flip this via a future id-generation probe. It is a
// seam so the refuse gate is unit-testable.
var workUnifyIsCounterModeWorkDB = func(beads.Store) (bool, error) {
	return false, nil
}

// workUnifyConfigAddPrefixes unions the given prefixes into the city database's
// allowed_prefixes set via the transactional bd config add-to-set primitive. The
// provider gate guarantees a BdStore by the time this runs.
var workUnifyConfigAddPrefixes = func(cityStore beads.Store, key string, prefixes []string) error {
	bd, ok := cityStore.(*beads.BdStore)
	if !ok {
		return fmt.Errorf("work unify config step: setting %s requires the bd provider", key)
	}
	for _, p := range prefixes {
		if err := bd.ConfigAddToSet(key, p); err != nil {
			return err
		}
	}
	return nil
}

// workUnifyImportRigClassResidue runs one synchronous, import-only class-residue
// pass over a rig's bd store into the shared class stores (gate B.2), reusing the
// per-class migration import legs so no infra bead is still bd-resident in the
// rig database when its scope stops resolving there.
var workUnifyImportRigClassResidue = importRigInfraClassResidue

// workUnifyRepointScopes drives the marker-aware canonicalization pass over all
// scopes (deliverable E.2). It is normalizeCanonicalBdScopeFiles, which — once
// the marker is present — recomputes each scope's desired endpoint at the shared
// city database, records the pre-canonicalization identity as a residue source,
// stamps the scope's provenance, and post-write verifies the resolved target.
var workUnifyRepointScopes = func(cityPath string, cfg *config.City, stderr io.Writer) error {
	return normalizeCanonicalBdScopeFiles(cityPath, cfg, stderr)
}

// openWorkUnifyStragglerStore opens a rig's OLD database via its recorded residue
// identity (never via scope resolution) using a temporary scope root under
// .gc/store/, torn down by the returned close func.
var openWorkUnifyStragglerStore = openStragglerScopeStore

// workUnifyResolveIdentity resolves a scope's work-database identity (the unify
// trigger + residue-payload input). The default reads the scope's .beads state;
// it is a seam so unit tests can drive the trigger without a live Dolt endpoint.
var workUnifyResolveIdentity = resolveWorkUnifyScopeIdentity

// ── the migration ──────────────────────────────────────────────────────────

// ensureWorkUnified performs the boot-time work-scope unify. It returns nil when
// the city is not unified (dark), when the marker already records completion, or
// when the migration succeeds; it returns a non-nil BOOT-BLOCKING error on any
// gate failure or copy/re-point fault.
func ensureWorkUnified(cityPath string, cfg *config.City, stderr io.Writer) error {
	if cfg == nil || !cfg.Beads.Work.IsUnified() {
		return nil // dark: scoped/managed city
	}
	// A present marker means the canonicalizer (run in startBeadsLifecycle before
	// this) has already re-pointed every scope and the residue-convergence pass
	// drains any late rig — the per-rig one-shot migration has nothing to do. But
	// the quarantine clear is CONVERGENT, not a one-shot tail step (F1/F3/F4): a
	// crash/error after the marker but before the clear would otherwise leave
	// copied rows quarantined forever, so every marker-present boot sweeps the
	// label off the city store (the canonicalizer has already re-pointed here).
	if present, err := workMarkerPresent(workUnifiedMarkerPath(cityPath)); err != nil {
		return err
	} else if present {
		return sweepQuarantineOnMarkerPresentBoot(cityPath, stderr)
	}

	cityID, err := workUnifyResolveIdentity(cityPath, cityPath)
	if err != nil {
		return fmt.Errorf("work unify: resolving city work database: %w", err)
	}
	scopes, err := workUnifyTriggerScopes(cityPath, cfg, cityID)
	if err != nil {
		return err
	}
	if len(scopes) == 0 {
		return nil // fresh unified city, or every rig already resolves to the city DB
	}

	// ── Gates (each failure aborts boot BEFORE any copy or re-point) ──
	if err := gateClassMarkersPresent(cityPath, cfg); err != nil {
		return err
	}
	if err := gatePrefixDistinct(cfg); err != nil {
		return err
	}

	cityStore, closeCity, err := openWorkUnifyScopeStore(cityPath, cityPath)
	if err != nil {
		return fmt.Errorf("work unify: opening city work store: %w", err)
	}
	defer closeCity()

	// Provider gate (F11): fail EARLY, before any B.2 import, on a non-bd work
	// store. The prefix-override mint and allowed_prefixes add-to-set are bd-leg
	// only in this slice.
	if err := workUnifyRequireBdProvider(cityStore); err != nil {
		return err
	}
	if capable, err := workUnifyProbeCapability(cityStore); err != nil {
		return fmt.Errorf("work unify: bd capability probe: %w", err)
	} else if !capable {
		return fmt.Errorf("work unify: the city bd binary lacks the %q capability required to mint per-rig prefixes into one database; install the forked bd that advertises it (bd version --json capabilities)", "workspace-prefix-mint")
	}
	// Counter-mode refuse gate (F5/F10/F15): v1 has no counter-advance guard, so a
	// counter-mode (sequential-id) work database is refused rather than risk a
	// post-re-point mint reusing a copy-window number. The fleet default (hash-id)
	// is immune and passes.
	if counter, err := workUnifyIsCounterModeWorkDB(cityStore); err != nil {
		return fmt.Errorf("work unify: probing id-generation mode: %w", err)
	} else if counter {
		return fmt.Errorf("work unify blocked: the city work database uses counter-mode (sequential) id generation, which v1 cannot safely unify — the per-prefix counter-advance guard is not implemented; use a hash-id work database or keep scope=\"scoped\"")
	}

	// Gate B.2: synchronous per-rig class-residue import to convergence, and
	// Gate 5: every bound rig scope store must OPEN. Open them once and hold the
	// handles for the collision check and the copy.
	rigStores := make([]beads.Store, len(scopes))
	for i := range scopes {
		st, closeFn, err := openWorkUnifyScopeStore(cityPath, scopes[i].root)
		if err != nil {
			return fmt.Errorf("work unify: opening rig %q work store: %w", scopes[i].label, err)
		}
		defer closeFn()
		rigStores[i] = st
		if err := workUnifyImportRigClassResidue(cityPath, cfg, st, stderr); err != nil {
			return fmt.Errorf("work unify: rig %q class-residue import: %w", scopes[i].label, err)
		}
	}

	// Gate 6: cross-source collision check.
	if err := gateCrossSourceCollision(cityStore, scopes, rigStores); err != nil {
		return err
	}

	// Config step C: union of the HQ prefix and EVERY bound rig prefix (not just
	// the trigger set — F9) into allowed_prefixes.
	if err := configStepAllowedPrefixes(cityStore, cfg); err != nil {
		return fmt.Errorf("work unify: %w", err)
	}

	// Per-rig copy (deliverable D).
	source := workTopologyCityIdentityStamp(cityPath)
	var (
		allSkippedDeps []beads.DepPair
		totalImported  int
	)
	for i := range scopes {
		result, err := copyRigWorkBeads(cityStore, rigStores[i], source, scopes[i], stderr)
		if err != nil {
			return fmt.Errorf("work unify: rig %q copy: %w", scopes[i].label, err)
		}
		allSkippedDeps = append(allSkippedDeps, result.skippedDeps...)
		totalImported += result.imported
	}

	// Convert skipped edges whose far endpoint is an open graph bead into the
	// gc.attached_workflow_root metadata linkage; log every other dropped edge.
	if err := convertSkippedGraphAttachEdges(cityPath, cityStore, allSkippedDeps, stderr); err != nil {
		return fmt.Errorf("work unify: converting skipped graph-attach edges: %w", err)
	}

	// Marker (deliverable E.1): written only after every rig imported + verified,
	// recording each rig's OLD identity as an undrained residue source.
	if err := writeWorkUnifiedMarker(cityPath, scopes, allSkippedDeps, totalImported); err != nil {
		return fmt.Errorf("work unify: writing marker: %w", err)
	}

	// Re-point (E.2): drive the now-marker-aware canonicalizer over all scopes.
	if err := workUnifyRepointScopes(cityPath, cfg, stderr); err != nil {
		return fmt.Errorf("work unify: re-pointing scopes: %w", err)
	}

	// Straggler pass (E.4) + created_at conflict guard (E.5): converge copy-window
	// work AND infra-class writes from each old database via the guarded upsert.
	for i := range scopes {
		if err := stragglerPass(cityPath, cfg, cityStore, scopes[i], stderr); err != nil {
			return fmt.Errorf("work unify: rig %q straggler pass: %w", scopes[i].label, err)
		}
	}

	// Quarantine clear (E.3): sweep the gc.topology_migrating label off the city
	// store by label (convergent — F1/F3/F4), now that re-point has converged
	// every scope on the shared database.
	if err := sweepWorkUnifyQuarantine(cityStore, stderr); err != nil {
		return fmt.Errorf("work unify: clearing quarantine: %w", err)
	}

	fmt.Fprintf(stderr, "gc start: work unified — %d work beads from %d rig(s) merged into the city database\n", totalImported, len(scopes)) //nolint:errcheck // best-effort stderr
	return nil
}

// ── scope enumeration + identity ─────────────────────────────────────────────

// workUnifyScope is one bound rig scope the unify copy migrates, carrying its
// OLD resolved work-database identity (the residue source).
type workUnifyScope struct {
	label    string
	root     string
	prefix   string
	host     string
	port     string
	database string
}

// resolveWorkUnifyScopeIdentity resolves a scope's work-database identity,
// preferring the fully resolved live target and falling back to the .beads-file
// observed identity when the managed runtime is not yet published (so the
// trigger and the residue payload work at boot before Dolt is up). Host is
// canonicalized.
func resolveWorkUnifyScopeIdentity(cityPath, scopeRoot string) (workUnifyScope, error) {
	if target, err := contract.ResolveDoltConnectionTarget(fsys.OSFS{}, cityPath, scopeRoot); err == nil {
		return workUnifyScope{
			root:     scopeRoot,
			host:     canonicalWorkHost(target.Host, target.Port),
			port:     strings.TrimSpace(target.Port),
			database: strings.TrimSpace(target.Database),
		}, nil
	} else if !contract.IsManagedRuntimeUnavailable(err) {
		return workUnifyScope{}, err
	}
	obs, err := resolveScopeObservedIdentity(cityPath, scopeRoot)
	if err != nil {
		return workUnifyScope{}, err
	}
	return workUnifyScope{
		root:     scopeRoot,
		host:     canonicalWorkHost(obs.host, obs.port),
		port:     strings.TrimSpace(obs.port),
		database: strings.TrimSpace(obs.database),
	}, nil
}

// sameEndpoint reports whether two identities name the same physical work
// database (host canonicalized, port + database exact).
func (s workUnifyScope) sameEndpoint(o workUnifyScope) bool {
	return canonicalWorkHost(s.host, s.port) == canonicalWorkHost(o.host, o.port) &&
		strings.TrimSpace(s.port) == strings.TrimSpace(o.port) &&
		strings.TrimSpace(s.database) == strings.TrimSpace(o.database)
}

// workUnifyTriggerScopes returns the bound rig scopes whose resolved work
// database differs from the city's — the unify trigger set. Scopes already
// resolving to the city database are skipped (already re-pointed or born
// unified).
func workUnifyTriggerScopes(cityPath string, cfg *config.City, cityID workUnifyScope) ([]workUnifyScope, error) {
	resolveRigPaths(cityPath, cfg.Rigs)
	var out []workUnifyScope
	for i := range cfg.Rigs {
		rig := cfg.Rigs[i]
		if strings.TrimSpace(rig.Path) == "" {
			continue
		}
		root := resolveStoreScopeRoot(cityPath, rig.Path)
		if samePath(root, cityPath) {
			continue
		}
		id, err := workUnifyResolveIdentity(cityPath, root)
		if err != nil {
			return nil, fmt.Errorf("work unify: resolving rig %q work database: %w", rig.Name, err)
		}
		if id.sameEndpoint(cityID) {
			continue // already resolves to the shared city database
		}
		id.label = rig.Name
		id.prefix = rig.EffectivePrefix()
		out = append(out, id)
	}
	return out, nil
}

// ── gates ────────────────────────────────────────────────────────────────────

// gateClassMarkersPresent verifies that every relocatable class EffectiveInfraLocal
// routes to sqlite has its migrated marker present (gate B.1). A unified city
// implies infra=local, so all five classes must have completed their bd->sqlite
// cutover before work beads are merged — otherwise an infra bead could still be
// bd-resident in a rig database that is about to stop resolving.
func gateClassMarkersPresent(cityPath string, cfg *config.City) error {
	for _, cl := range classMigrationStates(cityPath, cfg) {
		if cfg.Beads.ClassBackend(cl.class) != config.BeadsClassBackendSQLite {
			continue
		}
		if cl.statErr != nil {
			return fmt.Errorf("work unify: class %q migrated marker unstatable: %w", cl.class, cl.statErr)
		}
		if cl.marker != "present" {
			return fmt.Errorf("work unify blocked: class %q is not yet migrated to sqlite (marker absent) — every infra class must complete its bd->sqlite cutover before work beads are unified", cl.class)
		}
	}
	return nil
}

// gatePrefixDistinct re-verifies pairwise prefix distinctness against the live
// config (case-insensitive across the HQ prefix and every rig prefix). The
// Slice-1 load validation already guards this; the re-check fails closed if a
// reload slipped a colliding prefix past the boot check.
func gatePrefixDistinct(cfg *config.City) error {
	type owner struct{ label, prefix string }
	owners := []owner{{"hq", config.EffectiveHQPrefix(cfg)}}
	for i := range cfg.Rigs {
		owners = append(owners, owner{cfg.Rigs[i].Name, cfg.Rigs[i].EffectivePrefix()})
	}
	seen := make(map[string]string, len(owners))
	for _, o := range owners {
		key := strings.ToLower(strings.TrimSpace(o.prefix))
		if key == "" {
			continue
		}
		if prev, ok := seen[key]; ok {
			return fmt.Errorf("work unify blocked: scopes %q and %q share the work prefix %q — unified scopes must keep pairwise-distinct prefixes so ids stay disjoint in one database", prev, o.label, o.prefix)
		}
		seen[key] = o.label
	}
	return nil
}

// gateCrossSourceCollision lists the city store once and each rig store once
// (IncludeClosed), then diffs in memory (F19/F22 — no per-row subprocess). It
// refuses when a city-store row carries a rig's prefix that the rig store does
// NOT hold — split-brain: two independent beads minted the same id in two
// databases (gate 6). Rows both stores hold are a prior partial copy the guarded
// upsert converges, and so are allowed.
func gateCrossSourceCollision(cityStore beads.Store, scopes []workUnifyScope, rigStores []beads.Store) error {
	cityRows, err := cityStore.List(beads.ListQuery{IncludeClosed: true, AllowScan: true, TierMode: beads.TierBoth})
	if err != nil {
		return fmt.Errorf("work unify: listing city store for collision check: %w", err)
	}
	rigIDs := make([]map[string]bool, len(scopes))
	for i := range scopes {
		rows, err := rigStores[i].List(beads.ListQuery{IncludeClosed: true, AllowScan: true, TierMode: beads.TierBoth})
		if err != nil {
			return fmt.Errorf("work unify: listing rig %q for collision check: %w", scopes[i].label, err)
		}
		set := make(map[string]bool, len(rows))
		for _, b := range rows {
			set[b.ID] = true
		}
		rigIDs[i] = set
	}
	for _, row := range cityRows {
		for i := range scopes {
			prefix := strings.TrimSpace(scopes[i].prefix)
			if prefix == "" || !idHasPrefix(row.ID, prefix) {
				continue
			}
			if !rigIDs[i][row.ID] {
				return fmt.Errorf("work unify blocked: id %q carries rig %q's prefix and is present in the city database but ABSENT from the rig database %q — this is a split-brain prefix collision, not a partial copy; reconcile it before unifying",
					row.ID, scopes[i].label, scopes[i].database)
			}
		}
	}
	return nil
}

// idHasPrefix reports whether id begins with "<prefix>-".
func idHasPrefix(id, prefix string) bool {
	return strings.HasPrefix(id, prefix+"-")
}

// ── config step ──────────────────────────────────────────────────────────────

// configStepAllowedPrefixes unions the HQ prefix and EVERY bound rig's prefix
// (all rigs, not just the trigger set — F9) into the city database's
// allowed_prefixes (deliverable C), normalized to the minted-id casing
// (lowercase, matching bd's id-prefix convention). Needed for both explicit-id
// writes and per-rig prefix-override minting after unify.
func configStepAllowedPrefixes(cityStore beads.Store, cfg *config.City) error {
	set := map[string]bool{}
	var prefixes []string
	add := func(p string) {
		p = strings.ToLower(strings.TrimSpace(p))
		if p == "" || set[p] {
			return
		}
		set[p] = true
		prefixes = append(prefixes, p)
	}
	add(config.EffectiveHQPrefix(cfg))
	for i := range cfg.Rigs {
		if strings.TrimSpace(cfg.Rigs[i].Path) == "" {
			continue
		}
		add(cfg.Rigs[i].EffectivePrefix())
	}
	sort.Strings(prefixes)
	return workUnifyConfigAddPrefixes(cityStore, "allowed_prefixes", prefixes)
}

// ── the copy ─────────────────────────────────────────────────────────────────

type rigCopyResult struct {
	imported    int
	skippedDeps []beads.DepPair
}

// copyRigWorkBeads exports a rig's WORK beads (durable + ephemeral tiers), stamps
// each with the city topology source and the quarantine label, imports them into
// the city store with the guarded upsert, then copy-verifies a sample plus every
// flagged id and re-imports same-second ties with the conditional stale override.
func copyRigWorkBeads(cityStore beads.Store, rigStore beads.Store, source string, scope workUnifyScope, stderr io.Writer) (rigCopyResult, error) {
	ctx := context.Background()
	raw, err := beads.ExportBeadSnapshotsFrom(ctx, rigStore, beads.ExportOptions{IncludeEphemeral: true})
	if err != nil {
		return rigCopyResult{}, fmt.Errorf("exporting rig snapshots: %w", err)
	}
	var work []beads.Snapshot
	for _, snap := range raw {
		if coordclass.Classify(snap.Bead()) != coordclass.ClassWork {
			continue
		}
		stamped, err := snap.StampMetadata(workTopologySourceMetadataKey, source)
		if err != nil {
			return rigCopyResult{}, fmt.Errorf("stamping snapshot %s: %w", snap.ID(), err)
		}
		stamped, err = stamped.StampLabel(workTopologyMigratingLabel)
		if err != nil {
			return rigCopyResult{}, fmt.Errorf("labeling snapshot %s: %w", snap.ID(), err)
		}
		work = append(work, stamped)
	}
	if len(work) == 0 {
		return rigCopyResult{}, nil
	}

	report, err := beads.ImportBeadSnapshotsTo(ctx, cityStore, work, beads.ImportOptions{})
	if err != nil {
		return rigCopyResult{}, fmt.Errorf("importing snapshots: %w", err)
	}
	fmt.Fprintf(stderr, "unify: rig %s: %d/%d imported\n", scope.label, len(work), len(work)) //nolint:errcheck // best-effort stderr

	// Copy-verify: a sample per rig plus EVERY flagged id, comparing status,
	// close clock, and the topology-source stamp (routing-proof — F7).
	if err := verifyCopiedRows(ctx, cityStore, work, report, source, scope); err != nil {
		return rigCopyResult{}, err
	}

	// Reconcile flagged rows (F8/F13): re-import kept-local/stale-skipped ids with
	// the conditional stale override, then apply any missing dep/label edges to
	// every flagged row (dep adds don't bump updated_at, so the guarded upsert
	// alone never carries them).
	if err := reconcileFlaggedRows(ctx, cityStore, work, report, stderr); err != nil {
		return rigCopyResult{}, err
	}

	return rigCopyResult{imported: len(work), skippedDeps: report.SkippedDependencies}, nil
}

// verifyCopiedRows reads back a bounded sample plus every flagged id via the
// snapshot fetcher and compares status and close clock against the source. It
// ALSO asserts the destination row carries THIS city's gc.topology_source stamp
// (F7): bd `show` is prefix-routed, so a routing leak that satisfied the read
// from the SOURCE database would return an UN-stamped row — the stamp check makes
// copy-verify routing-proof (the stamp is written only on the city-imported copy,
// never on the source row).
func verifyCopiedRows(ctx context.Context, cityStore beads.Store, work []beads.Snapshot, report beads.ImportReport, source string, scope workUnifyScope) error {
	bySrc := make(map[string]beads.Snapshot, len(work))
	for _, snap := range work {
		bySrc[snap.ID()] = snap
	}
	check := map[string]bool{}
	for i, snap := range work {
		if i >= workUnifyVerifySampleSize {
			break
		}
		check[snap.ID()] = true
	}
	for _, id := range report.KeptLocal {
		check[id] = true
	}
	for _, id := range report.StaleSkipped {
		check[id] = true
	}
	for _, id := range report.Updated {
		check[id] = true
	}
	if len(check) == 0 {
		return nil
	}
	ids := make([]string, 0, len(check))
	for id := range check {
		ids = append(ids, id)
	}
	dest, err := beads.GetBeadSnapshotsFrom(ctx, cityStore, ids)
	if err != nil {
		return fmt.Errorf("copy-verify fetch: %w", err)
	}
	got := make(map[string]beads.Snapshot, len(dest))
	for _, d := range dest {
		got[d.ID()] = d
	}
	for id := range check {
		d, ok := got[id]
		if !ok {
			return fmt.Errorf("copy-verify: rig %q id %q missing from the city store after import", scope.label, id)
		}
		// Routing-proof stamp check (F7): the destination row must carry THIS
		// city's topology source; a source-database routing leak would return an
		// unstamped (or foreign-stamped) row.
		if got := d.Metadata()[workTopologySourceMetadataKey]; got != source {
			return fmt.Errorf("copy-verify: rig %q id %q resolved to a row stamped %q, want this city's %q — the verify read may have routed to the source database", scope.label, id, got, source)
		}
		src := bySrc[id]
		// A tie/stale-skipped row legitimately keeps the destination's newer
		// status/clock; only assert on rows the import actually wrote.
		if importReportWroteID(report, id) {
			if !sameClosedClock(src.ClosedAt(), d.ClosedAt()) || mapBdClosedness(src.Status()) != mapBdClosedness(d.Status()) {
				return fmt.Errorf("copy-verify: rig %q id %q status/close-clock mismatch after import (src=%s dest=%s)", scope.label, id, src.Status(), d.Status())
			}
		}
	}
	return nil
}

// importReportWroteID reports whether the import inserted or updated id (as
// opposed to keeping the local row).
func importReportWroteID(report beads.ImportReport, id string) bool {
	for _, w := range report.Inserted {
		if w == id {
			return true
		}
	}
	for _, w := range report.Updated {
		if w == id {
			return true
		}
	}
	return false
}

// mapBdClosedness folds a bd status onto whether it is terminal, the granularity
// copy-verify asserts (a closed bead must never appear open mid-import).
func mapBdClosedness(status string) bool {
	return strings.EqualFold(strings.TrimSpace(status), "closed")
}

// sameClosedClock compares two close clocks at second granularity, treating both
// nil (open) as equal.
func sameClosedClock(a, b *time.Time) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return a.UTC().Truncate(time.Second).Equal(b.UTC().Truncate(time.Second))
}

// selectSnapshots returns the snapshots whose ids are in want.
func selectSnapshots(snaps []beads.Snapshot, want []string) []beads.Snapshot {
	set := make(map[string]bool, len(want))
	for _, id := range want {
		set[id] = true
	}
	var out []beads.Snapshot
	for _, snap := range snaps {
		if set[snap.ID()] {
			out = append(out, snap)
		}
	}
	return out
}

// ── skipped-edge conversion (landmine #4) ────────────────────────────────────

// convertSkippedGraphAttachEdges walks the copy's skipped dep edges: an edge
// whose far endpoint is an OPEN graph-class bead becomes the
// gc.attached_workflow_root metadata linkage on the work bead (the cross-store
// attach block the ready federation enforces); every other dropped edge is
// logged id-pair by id-pair.
func convertSkippedGraphAttachEdges(cityPath string, cityStore beads.Store, skipped []beads.DepPair, stderr io.Writer) error {
	if len(skipped) == 0 {
		return nil
	}
	graph, err := graphClassStoreFor(cityPath)
	if err != nil {
		return fmt.Errorf("opening graph class store: %w", err)
	}
	// assigned records the workflow root already stamped on each work bead so a
	// second skipped edge for the same bead is deterministic: the FIRST open root
	// (edges arrive sorted below) wins and the later one is logged as displaced.
	assigned := map[string]string{}
	sorted := append([]beads.DepPair(nil), skipped...)
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].IssueID != sorted[j].IssueID {
			return sorted[i].IssueID < sorted[j].IssueID
		}
		return sorted[i].DependsOnID < sorted[j].DependsOnID
	})
	for _, edge := range sorted {
		if config.IsReservedClassBeadID(edge.DependsOnID) {
			root, gerr := graph.Get(edge.DependsOnID)
			if gerr != nil && !errors.Is(gerr, beads.ErrNotFound) {
				return fmt.Errorf("resolving graph attach root %s for %s: %w", edge.DependsOnID, edge.IssueID, gerr)
			}
			if gerr == nil && root.Status != "closed" {
				if prev, ok := assigned[edge.IssueID]; ok {
					fmt.Fprintf(stderr, "unify: %s already attached to workflow root %s; displaced duplicate skipped edge -> %s\n", edge.IssueID, prev, edge.DependsOnID) //nolint:errcheck // best-effort stderr
					continue
				}
				if err := cityStore.SetMetadata(edge.IssueID, beadmeta.AttachedWorkflowRootMetadataKey, edge.DependsOnID); err != nil {
					return fmt.Errorf("stamping %s attached_workflow_root=%s: %w", edge.IssueID, edge.DependsOnID, err)
				}
				assigned[edge.IssueID] = edge.DependsOnID
				fmt.Fprintf(stderr, "unify: converted skipped edge %s -> %s into an attached-workflow-root linkage\n", edge.IssueID, edge.DependsOnID) //nolint:errcheck // best-effort stderr
				continue
			}
		}
		fmt.Fprintf(stderr, "unify: dropped dependency edge %s -> %s (endpoint not present in the unified database)\n", edge.IssueID, edge.DependsOnID) //nolint:errcheck // best-effort stderr
	}
	return nil
}

// ── marker + quarantine clear ────────────────────────────────────────────────

// writeWorkUnifiedMarker writes work.unified under the cross-process lock, recording
// each migrating rig's OLD database identity as an undrained residue source plus
// the copy's skipped dependencies and import count (deliverable E.1).
func writeWorkUnifiedMarker(cityPath string, scopes []workUnifyScope, skippedDeps []beads.DepPair, imported int) error {
	now := time.Now().UTC()
	sources := make([]workResidueSource, 0, len(scopes))
	for _, s := range scopes {
		sources = append(sources, workResidueSource{
			Scope:      s.label,
			Host:       canonicalWorkHost(s.host, s.port),
			Port:       strings.TrimSpace(s.port),
			Database:   strings.TrimSpace(s.database),
			RecordedAt: now,
		})
	}
	marker := &workTopologyMarker{
		Kind:                workMarkerKindUnified,
		RecordedAt:          now,
		ResidueSources:      sources,
		SkippedDependencies: dedupDepPairs(skippedDeps),
		Counts:              workTopologyCounts{Imported: imported, Verified: imported},
	}
	return writeWorkTopologyMarkerLocked(workUnifiedMarkerPath(cityPath), marker)
}

// dedupDepPairs removes duplicate id pairs, preserving order.
func dedupDepPairs(pairs []beads.DepPair) []beads.DepPair {
	if len(pairs) == 0 {
		return nil
	}
	seen := make(map[beads.DepPair]bool, len(pairs))
	var out []beads.DepPair
	for _, p := range pairs {
		if seen[p] {
			continue
		}
		seen[p] = true
		out = append(out, p)
	}
	return out
}

// sweepWorkUnifyQuarantine removes the gc.topology_migrating label from EVERY row
// on the city store that still carries it — a CONVERGENT clear (F1/F3/F4) keyed on
// the label itself, not a one-shot list of copied ids, so a crash/error after the
// marker but before an earlier clear can never strand a row quarantined. A missing
// row (deleted mid-window) is tolerated.
func sweepWorkUnifyQuarantine(cityStore beads.Store, stderr io.Writer) error {
	rows, err := cityStore.List(beads.ListQuery{Label: workTopologyMigratingLabel, IncludeClosed: true, TierMode: beads.TierBoth, AllowScan: true})
	if err != nil {
		return fmt.Errorf("listing quarantined rows: %w", err)
	}
	cleared := 0
	for _, b := range rows {
		if err := cityStore.Update(b.ID, beads.UpdateOpts{RemoveLabels: []string{workTopologyMigratingLabel}}); err != nil {
			if errors.Is(err, beads.ErrNotFound) {
				continue
			}
			return fmt.Errorf("removing quarantine label from %s: %w", b.ID, err)
		}
		cleared++
	}
	if cleared > 0 {
		fmt.Fprintf(stderr, "unify: cleared quarantine label from %d migrated rows\n", cleared) //nolint:errcheck // best-effort stderr
	}
	return nil
}

// sweepQuarantineOnMarkerPresentBoot opens the city store and runs the convergent
// quarantine sweep — the marker-present boot arm (F1/F3/F4), where the
// canonicalizer has already re-pointed every scope.
func sweepQuarantineOnMarkerPresentBoot(cityPath string, stderr io.Writer) error {
	cityStore, closeCity, err := openWorkUnifyScopeStore(cityPath, cityPath)
	if err != nil {
		return fmt.Errorf("work unify: opening city store for quarantine sweep: %w", err)
	}
	defer closeCity()
	return sweepWorkUnifyQuarantine(cityStore, stderr)
}

// reconcileFlaggedRows converges the aux (dep + label) sets of rows the guarded
// upsert did NOT fully write (F8/F13): it re-imports kept-local/stale-skipped ids
// with the conditional stale override (which protects a strictly-newer
// destination), then, for every flagged id, applies the source's missing dep
// edges and labels that a scalar upsert never carries (dep/label adds don't bump
// updated_at). Dep edges whose far endpoint is a reserved-class bead are skipped
// (F14) — they are represented by the gc.attached_workflow_root metadata linkage,
// not a work-store dep. Import-only convergence: a dangling/failed edge is logged
// and retried by a later drain pass, never aborting.
func reconcileFlaggedRows(ctx context.Context, cityStore beads.Store, work []beads.Snapshot, report beads.ImportReport, stderr io.Writer) error {
	bySrc := make(map[string]beads.Snapshot, len(work))
	for _, s := range work {
		bySrc[s.ID()] = s
	}
	tie := append(append([]string(nil), report.KeptLocal...), report.StaleSkipped...)
	if len(tie) > 0 {
		if forced := selectSnapshots(work, tie); len(forced) > 0 {
			if _, err := beads.ImportBeadSnapshotsTo(ctx, cityStore, forced, beads.ImportOptions{AllowStaleIDs: tie}); err != nil {
				return fmt.Errorf("tie re-import: %w", err)
			}
		}
	}
	flagged := uniqueStrings(report.KeptLocal, report.StaleSkipped, report.ConflictSkipped)
	for _, id := range flagged {
		src, ok := bySrc[id]
		if !ok {
			continue
		}
		dest, err := cityStore.Get(id)
		if err != nil {
			if errors.Is(err, beads.ErrNotFound) {
				continue
			}
			return fmt.Errorf("reconciling flagged row %s: %w", id, err)
		}
		diff := beads.DiffSnapshotLinks(src, dest)
		for _, dep := range diff.MissingDeps {
			if config.IsReservedClassBeadID(dep.DependsOnID) {
				continue // represented by the attach metadata linkage (F14)
			}
			if err := cityStore.DepAdd(dep.IssueID, dep.DependsOnID, dep.Type); err != nil {
				fmt.Fprintf(stderr, "unify: reconcile: dep %s -> %s not applied (retried next drain): %v\n", dep.IssueID, dep.DependsOnID, err) //nolint:errcheck // best-effort stderr
			}
		}
		if len(diff.MissingLabels) > 0 {
			if err := cityStore.Update(id, beads.UpdateOpts{Labels: diff.MissingLabels}); err != nil && !errors.Is(err, beads.ErrNotFound) {
				return fmt.Errorf("reconciling labels on %s: %w", id, err)
			}
		}
	}
	return nil
}

// uniqueStrings returns the union of the given id slices, de-duplicated,
// preserving first-seen order.
func uniqueStrings(lists ...[]string) []string {
	seen := map[string]bool{}
	var out []string
	for _, list := range lists {
		for _, s := range list {
			if s == "" || seen[s] {
				continue
			}
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

// ── straggler + residue convergence ──────────────────────────────────────────

// stragglerPass re-opens a rig's OLD database via its recorded identity and runs
// one more import pass so WORK and infra-class writes that landed during the copy
// window converge via the guarded upsert (deliverables E.4 + F12). The created_at
// conflict guard (E.5) filters an incoming id that exists in the unified DB with a
// DIFFERENT created_at — never an upsert.
func stragglerPass(cityPath string, cfg *config.City, cityStore beads.Store, scope workUnifyScope, stderr io.Writer) error {
	src := workResidueSource{Scope: scope.label, Host: scope.host, Port: scope.port, Database: scope.database}
	oldStore, closeFn, err := openWorkUnifyStragglerStore(cityPath, src)
	if err != nil {
		return fmt.Errorf("opening old database: %w", err)
	}
	defer closeFn()
	// Infra-class residue that landed in the old database during the copy window
	// (F12) — import-only into the shared class stores.
	if err := workUnifyImportRigClassResidue(cityPath, cfg, oldStore, stderr); err != nil {
		return fmt.Errorf("infra-class straggler: %w", err)
	}
	err = importResidueFromSource(cityStore, oldStore, workTopologyCityIdentityStamp(cityPath), stderr)
	return err
}

// importResidueFromSource exports the source's WORK beads and imports them into
// the unified store with the guarded upsert, first filtering ids whose
// destination holds a DIFFERENT created_at (a reported conflict, never an upsert),
// then reconciling flagged rows' dep/label sets. Returns the number of source
// WORK beads processed. Shared by the straggler pass and the later-boot
// residue-convergence pass — import only; nothing is ever deleted from the old
// database (cold backup).
func importResidueFromSource(cityStore, oldStore beads.Store, source string, stderr io.Writer) error {
	ctx := context.Background()
	raw, err := beads.ExportBeadSnapshotsFrom(ctx, oldStore, beads.ExportOptions{IncludeEphemeral: true})
	if err != nil {
		return fmt.Errorf("exporting old-database snapshots: %w", err)
	}
	var work []beads.Snapshot
	for _, snap := range raw {
		if coordclass.Classify(snap.Bead()) != coordclass.ClassWork {
			continue
		}
		stamped, err := snap.StampMetadata(workTopologySourceMetadataKey, source)
		if err != nil {
			return fmt.Errorf("stamping straggler snapshot %s: %w", snap.ID(), err)
		}
		work = append(work, stamped)
	}
	if len(work) == 0 {
		return nil
	}
	kept, conflicts, err := filterCreatedAtConflicts(ctx, cityStore, work)
	if err != nil {
		return err
	}
	for _, c := range conflicts {
		fmt.Fprintf(stderr, "unify residue: id %s exists in the unified database with a different created_at than the old database — reported as a conflict, not upserted\n", c) //nolint:errcheck // best-effort stderr
	}
	if len(kept) > 0 {
		report, err := beads.ImportBeadSnapshotsTo(ctx, cityStore, kept, beads.ImportOptions{})
		if err != nil {
			return fmt.Errorf("importing straggler snapshots: %w", err)
		}
		// Converge flagged rows' dep/label sets from the still-authoritative source
		// (F8/F13) — the AllowStale re-import protects a strictly-newer destination.
		if err := reconcileFlaggedRows(ctx, cityStore, kept, report, stderr); err != nil {
			return err
		}
	}
	return nil
}

// filterCreatedAtConflicts drops incoming snapshots whose id already exists in
// the destination with a different created_at (second granularity) — the
// counter-mode created_at conflict guard (E.5). It returns the kept snapshots
// and the conflicting ids. Absent ids and matching-clock ids are kept.
func filterCreatedAtConflicts(ctx context.Context, dest beads.Store, snaps []beads.Snapshot) ([]beads.Snapshot, []string, error) {
	ids := make([]string, len(snaps))
	for i, s := range snaps {
		ids[i] = s.ID()
	}
	existing, err := beads.GetBeadSnapshotsFrom(ctx, dest, ids)
	if err != nil {
		return nil, nil, fmt.Errorf("probing destination created_at: %w", err)
	}
	destCreated := make(map[string]time.Time, len(existing))
	for _, e := range existing {
		destCreated[e.ID()] = e.CreatedAt().UTC().Truncate(time.Second)
	}
	var kept []beads.Snapshot
	var conflicts []string
	for _, s := range snaps {
		if dc, ok := destCreated[s.ID()]; ok && !dc.Equal(s.CreatedAt().UTC().Truncate(time.Second)) {
			conflicts = append(conflicts, s.ID())
			continue
		}
		kept = append(kept, s)
	}
	return kept, conflicts, nil
}

// convergeWorkUnifiedResidue is the later-boot background residue pass
// (deliverable F): for each undrained recorded source it re-imports (import
// only) and runs the drain check — all source WORK rows present-or-older in the
// unified DB with their dep/label sets present — recording Drained=true via the
// locked marker writer once it passes. Nothing is ever deleted from the old
// databases (cold backup). Launched as a goroutine after boot, mirroring the
// class residue sweeps.
func convergeWorkUnifiedResidue(cityPath string, cfg *config.City, stderr io.Writer) {
	marker, ok, err := readWorkTopologyMarker(workUnifiedMarkerPath(cityPath))
	if err != nil || !ok || marker == nil {
		if err != nil {
			fmt.Fprintf(stderr, "gc: work unify residue: %v\n", err) //nolint:errcheck // best-effort stderr
		}
		return
	}
	// Cheap early-out (marker read only) once every source is drained: the
	// convergent quarantine clear is handled by the two boot paths (the
	// marker-present early-return arm and the end-of-success sweep), which run on
	// EVERY unified boot before this loop starts, so the ticker need not re-open
	// the city store on a fully-drained city.
	if marker.undrainedResidueCount() == 0 {
		return
	}
	cityStore, closeCity, err := openWorkUnifyScopeStore(cityPath, cityPath)
	if err != nil {
		fmt.Fprintf(stderr, "gc: work unify residue: opening city store: %v\n", err) //nolint:errcheck // best-effort stderr
		return
	}
	defer closeCity()
	source := workTopologyCityIdentityStamp(cityPath)
	for _, src := range marker.ResidueSources {
		if src.Drained {
			continue
		}
		if err := convergeOneResidueSource(cityPath, cfg, cityStore, src, source, stderr); err != nil {
			fmt.Fprintf(stderr, "gc: work unify residue: source %s not yet drained: %v\n", src.Database, err) //nolint:errcheck // best-effort stderr
		}
	}
}

// ── in-process residue-convergence re-arm (F16) ──────────────────────────────

// workUnifyResiduePokes maps a normalized city path to the poke channel of its
// running residue-convergence loop, so an in-process residue-source append (the
// rig-add / canonicalization seam) can trigger convergence WITHOUT a reboot.
var workUnifyResiduePokes sync.Map // string -> chan struct{}

// workUnifyResidueTickInterval is the slow retry cadence for undrained residue
// sources (the order-rescan cadence). A function so tests can shorten it.
var workUnifyResidueTickInterval = func() time.Duration { return orderRescanInterval }

// kickWorkUnifyResidueConvergence pokes a running convergence loop for cityPath
// (non-blocking; a no-op when none is registered). Called after a residue-source
// append so a late rig converges without waiting for the next boot or tick.
func kickWorkUnifyResidueConvergence(cityPath string) {
	if v, ok := workUnifyResiduePokes.Load(normalizePathForCompare(cityPath)); ok {
		select {
		case v.(chan struct{}) <- struct{}{}:
		default:
		}
	}
}

// workUnifyResidueConvergePassHook is a test seam fired after every convergence
// pass so tests can await a lifecycle signal instead of a fixed sleep. No-op in
// production.
var workUnifyResidueConvergePassHook = func() {}

// runWorkUnifyResidueConvergenceLoop runs residue convergence immediately, then
// on every poke (append) and on a slow ticker, until the runtime context is
// canceled. convergeWorkUnifiedResidue is cheap when nothing is undrained, so an
// idle drained city costs one marker read per tick.
func runWorkUnifyResidueConvergenceLoop(ctx context.Context, cityPath string, cfg *config.City, stderr io.Writer) {
	key := normalizePathForCompare(cityPath)
	poke := make(chan struct{}, 1)
	workUnifyResiduePokes.Store(key, poke)
	defer workUnifyResiduePokes.Delete(key)

	pass := func() {
		convergeWorkUnifiedResidue(cityPath, cfg, stderr)
		workUnifyResidueConvergePassHook()
	}
	pass()
	ticker := time.NewTicker(workUnifyResidueTickInterval())
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-poke:
			pass()
		case <-ticker.C:
			pass()
		}
	}
}

// cityPathFromWorkMarkerPath recovers a city root from a work marker path
// (<city>/.gc/store/work.{unified,remote}).
func cityPathFromWorkMarkerPath(markerPath string) string {
	return filepath.Dir(filepath.Dir(filepath.Dir(markerPath)))
}

// convergeOneResidueSource imports WORK and infra-class residue from one recorded
// source and, when the drain check passes, records it drained.
func convergeOneResidueSource(cityPath string, cfg *config.City, cityStore beads.Store, src workResidueSource, source string, stderr io.Writer) error {
	oldStore, closeFn, err := openWorkUnifyStragglerStore(cityPath, src)
	if err != nil {
		return fmt.Errorf("opening old database: %w", err)
	}
	defer closeFn()
	// Infra-class residue too (F12): the drain check requires it reflected.
	if err := workUnifyImportRigClassResidue(cityPath, cfg, oldStore, stderr); err != nil {
		return fmt.Errorf("infra-class residue: %w", err)
	}
	if err := importResidueFromSource(cityStore, oldStore, source, stderr); err != nil {
		return err
	}
	drained, err := residueSourceDrained(context.Background(), cityStore, oldStore)
	if err != nil {
		return err
	}
	if !drained {
		return fmt.Errorf("drain check incomplete")
	}
	return markResidueSourceDrained(workUnifiedMarkerPath(cityPath), src)
}

// residueSourceDrained reports whether every source WORK row is present in the
// unified DB, not older there than in the source, and carries the source's
// dep/label sets (DiffSnapshotLinks empty). Import-only convergence: a source is
// drained only when its rows AND edges are all reflected.
func residueSourceDrained(ctx context.Context, cityStore, oldStore beads.Store) (bool, error) {
	srcSnaps, err := beads.ExportBeadSnapshotsFrom(ctx, oldStore, beads.ExportOptions{IncludeEphemeral: true})
	if err != nil {
		return false, fmt.Errorf("exporting source for drain check: %w", err)
	}
	var work []beads.Snapshot
	ids := []string{}
	for _, s := range srcSnaps {
		if coordclass.Classify(s.Bead()) != coordclass.ClassWork {
			continue
		}
		work = append(work, s)
		ids = append(ids, s.ID())
	}
	if len(work) == 0 {
		return true, nil
	}
	destSnaps, err := beads.GetBeadSnapshotsFrom(ctx, cityStore, ids)
	if err != nil {
		return false, fmt.Errorf("fetching unified rows for drain check: %w", err)
	}
	dest := make(map[string]beads.Snapshot, len(destSnaps))
	for _, d := range destSnaps {
		dest[d.ID()] = d
	}
	for _, s := range work {
		d, ok := dest[s.ID()]
		if !ok {
			return false, nil // a source row is not present yet
		}
		if s.UpdatedAt().UTC().Truncate(time.Second).After(d.UpdatedAt().UTC().Truncate(time.Second)) {
			return false, nil // the unified copy is older than the source
		}
		if !linkDiffDrained(beads.DiffSnapshotLinks(s, d.Bead())) {
			return false, nil // a work-store dep/label edge is not reflected yet
		}
	}
	return true, nil
}

// linkDiffDrained reports whether a source→dest link diff is fully reflected,
// EXCLUDING dep edges whose far endpoint is a reserved-class bead id (F14): those
// were converted to the gc.attached_workflow_root metadata linkage during copy,
// so they are represented by metadata, not a work-store dep, and must not count as
// "missing" forever.
func linkDiffDrained(diff beads.LinkDiff) bool {
	if len(diff.MissingLabels) > 0 {
		return false
	}
	for _, dep := range diff.MissingDeps {
		if config.IsReservedClassBeadID(dep.DependsOnID) {
			continue
		}
		return false
	}
	return true
}

// markResidueSourceDrained flips a residue source's Drained flag under the
// cross-process marker lock, preserving every other field.
func markResidueSourceDrained(markerPath string, src workResidueSource) error {
	return withWorkMarkerLock(markerPath, func() error {
		m, ok, err := readWorkTopologyMarker(markerPath)
		if err != nil {
			return err
		}
		if !ok || m == nil {
			return fmt.Errorf("marking residue drained: marker %s absent", markerPath)
		}
		changed := false
		for i := range m.ResidueSources {
			if sameWorkResidueIdentity(m.ResidueSources[i], src) && !m.ResidueSources[i].Drained {
				m.ResidueSources[i].Drained = true
				changed = true
			}
		}
		if !changed {
			return nil
		}
		return writeWorkTopologyMarker(markerPath, m)
	})
}

// ── straggler scope open (real, exec-backed default) ─────────────────────────

// openStragglerScopeStore materializes a TEMPORARY scope root under .gc/store/
// whose canonical .beads state points at the recorded OLD identity, opens a store
// there, and returns a close func that closes the handle and removes the temp
// scope. Used for the straggler and residue passes so the old database is read
// via its recorded identity, never via scope resolution.
func openStragglerScopeStore(cityPath string, src workResidueSource) (beads.Store, func(), error) {
	root := filepath.Join(nudgesdb.StoreDir(cityPath), "work-residue", sanitizeResidueDir(src))
	state, err := stragglerScopeConfigState(cityPath, src)
	if err != nil {
		return nil, func() {}, err
	}
	if err := ensureCanonicalScopeConfigState(fsys.OSFS{}, root, state); err != nil {
		return nil, func() {}, fmt.Errorf("writing straggler scope config: %w", err)
	}
	if err := ensureCanonicalScopeMetadataForInit(fsys.OSFS{}, root, strings.TrimSpace(src.Database)); err != nil {
		return nil, func() {}, fmt.Errorf("writing straggler scope metadata: %w", err)
	}
	store, closeFn, err := openWorkUnifyScopeStore(cityPath, root)
	if err != nil {
		_ = removeAllResidueScope(root)
		return nil, func() {}, err
	}
	return store, func() {
		closeFn()
		_ = removeAllResidueScope(root)
	}, nil
}

// stragglerScopeConfigState builds the canonical config for a residue scope from
// its recorded identity. The temp scope is a non-city scope, and the contract
// REJECTS a managed_city origin there (F2), so when the recorded host/port are
// empty (both scopes shared the managed-local server) it resolves the CURRENT
// managed endpoint from the city root and writes EndpointOriginExplicit at
// 127.0.0.1:<current-port>. A recorded external endpoint is written verbatim.
func stragglerScopeConfigState(cityPath string, src workResidueSource) (contract.ConfigState, error) {
	host := strings.TrimSpace(src.Host)
	port := strings.TrimSpace(src.Port)
	if host == "" && port == "" {
		port = strings.TrimSpace(currentDoltPort(cityPath))
		if port == "" {
			return contract.ConfigState{}, fmt.Errorf("work unify residue: managed Dolt port unavailable for old database %q; cannot open its residue scope", src.Database)
		}
		host = "127.0.0.1"
	}
	return contract.ConfigState{
		EndpointOrigin: contract.EndpointOriginExplicit,
		DoltHost:       host,
		DoltPort:       port,
	}, nil
}

// ── gate B.2: per-rig infra-class residue import ─────────────────────────────

// importRigInfraClassResidue runs an import-only pass of the bd-backed infra
// classes (graph, sessions, messaging, orders) from a rig's work store into the
// shared class stores, reusing the per-class migration import legs. Nudges live
// in the city file queue, never a rig bd store, so they have no rig-scope
// residue. Any failure aborts unify.
func importRigInfraClassResidue(cityPath string, cfg *config.City, rigStore beads.Store, _ io.Writer) error {
	now := time.Now()
	if graph, err := graphClassStoreFor(cityPath); err != nil {
		return fmt.Errorf("graph class store: %w", err)
	} else if _, err := importGraphSnapshot(graph, rigStore, false); err != nil {
		return fmt.Errorf("graph residue: %w", err)
	}
	if sess, err := sessionsdb.SharedStoreFor(cityPath); err != nil {
		return fmt.Errorf("sessions class store: %w", err)
	} else if _, err := importSessionsSnapshot(sess, rigStore, now, false); err != nil {
		return fmt.Errorf("sessions residue: %w", err)
	}
	if msg, err := messagingClassStoreHandle(cityPath); err != nil {
		return fmt.Errorf("messaging class store: %w", err)
	} else if _, err := importMessagingSnapshot(msg, rigStore, cfg, cityPath, now, false); err != nil {
		return fmt.Errorf("messaging residue: %w", err)
	}
	if ord, err := ordersClassStoreFor(cityPath); err != nil {
		return fmt.Errorf("orders class store: %w", err)
	} else if _, err := migrateOrdersTrackingIntoClassStore(ord, []beads.Store{rigStore}, orderTrackingRetentionPolicyForConfig(cfg)); err != nil {
		return fmt.Errorf("orders residue: %w", err)
	}
	return nil
}

// ── city identity stamp ──────────────────────────────────────────────────────

// workTopologyCityIdentityStamp returns the stable per-city identity stamped in
// gc.topology_source on every migrated row. Choice: the SHA-256 (first 16 hex
// chars) of the cleaned absolute city path, prefixed "gc-city:". Rationale — the
// city path is always available at boot with no network or hosted-identity
// dependency and is stable across boots for a given city directory, while being
// distinct per city directory. (Known limitation carried into the remote slice:
// two DIFFERENT cities at the same absolute path on different hosts would collide
// on a shared org DB; the remote migration incorporates host identity into the
// discriminator. For the local unify this stamp only needs to be present and
// stable, which this guarantees.)
func workTopologyCityIdentityStamp(cityPath string) string {
	return "gc-city:" + shortPathHash(filepath.Clean(cityPath))
}

// shortPathHash returns the first 16 hex chars of the SHA-256 of s.
func shortPathHash(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])[:16]
}

// residueDirUnsafe matches every character not allowed in a residue temp-dir
// segment, so an arbitrary database/host string cannot escape the scope root.
var residueDirUnsafe = regexp.MustCompile(`[^a-zA-Z0-9._-]+`)

// sanitizeResidueDir derives a filesystem-safe, collision-resistant directory
// name for a residue source's temporary scope (host+port+database).
func sanitizeResidueDir(src workResidueSource) string {
	base := residueDirUnsafe.ReplaceAllString(strings.TrimSpace(src.Database), "_")
	if base == "" {
		base = "db"
	}
	return base + "-" + shortPathHash(src.Host+"|"+src.Port+"|"+src.Database)
}

// removeAllResidueScope tears down a temporary residue scope directory.
func removeAllResidueScope(root string) error {
	return os.RemoveAll(root)
}
