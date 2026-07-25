package main

// Remote migration (engdocs/design/beads-work-topology.md, "Remote migration"):
// on a controller boot of a [beads.work] target="dolt://host:port/database" city
// whose unified rung has completed, ensureWorkRemote copies the LOCAL unified
// city work database into the shared org Dolt endpoint — ids, status, and clocks
// preserved via the Slice-2 snapshot copy primitive under a TWO-armed
// pre-destructive collision protocol — then writes the work.remote marker and
// drives the marker-aware canonicalizer to re-point the city (city_canonical) and
// its inherited rigs at the remote endpoint.
//
// BOOT-BLOCKING like ensureWorkUnified: a failed or aborted remote migration
// refuses the controller boot via newCityRuntime.bootBlockingErr, so a partial
// org-DB copy is never exposed to a live reconciler/dispatcher.
//
// DARK on a managed/scoped city: ensureWorkRemote returns nil immediately when
// the work target is not remote. The migration only fires when the remote marker
// is absent (one-shot); once present, the generalized residue-convergence loop
// (work_unify_migrate.go) drains the recorded LOCAL source into the org DB and
// the managed-local Dolt lifecycle stays enabled until it does (F.4).

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/beads/contract"
	nudgesdb "github.com/gastownhall/gascity/internal/classdb/nudges"
	"github.com/gastownhall/gascity/internal/config"
	"github.com/gastownhall/gascity/internal/coordclass"
	"github.com/gastownhall/gascity/internal/fsys"
)

// workRemoteProbeBatchSize bounds the id count per `bd show` / GetIssuesByIDs
// batch in the collision pre-probe, so the stamp-check reads stay bounded per-id
// (never a full org-DB export).
const workRemoteProbeBatchSize = 200

// allowedPrefixesConfigKey is the org DB config-table set the remote config step
// unions this city's prefixes into (and the doctor/self-heal verifies).
const allowedPrefixesConfigKey = "allowed_prefixes"

// ── injectable seams (real exec-backed defaults; overridden in unit tests) ──

// openWorkRemoteScopeStore materializes a TEMPORARY scope root under
// .gc/store/work-remote/ whose canonical .beads state points at the remote target
// (EndpointOriginExplicit host/port + metadata database), verifies it resolves
// via the rig-grade contract, opens a store there, and returns a close func that
// closes the handle and removes the temp scope. Explicit NEVER lands in a
// persisted (non-temp) scope — only here.
var openWorkRemoteScopeStore = defaultOpenWorkRemoteScopeStore

// workRemoteCredentialPreflight runs one authenticated, BOUNDED probe through the
// temp remote scope BEFORE any copy (deliverable C). It is bd's bounded list
// (never Ping's full org-DB pull); on failure the caller names the required
// credential env.
var workRemoteCredentialPreflight = func(store beads.Store) error {
	bd, ok := store.(*beads.BdStore)
	if !ok {
		return fmt.Errorf("remote credential preflight requires the bd work-store provider")
	}
	return bd.CredentialPreflight(context.Background())
}

// workRemoteAddPrefixToSet appends one prefix to the org DB's allowed_prefixes
// via the transactional bd config add-to-set (deliverable D), never removing
// another city's entries. Shared by the config step (unions the whole set) and
// the convergent self-heal (re-appends only evicted entries); a seam so tests can
// capture the writes without a live org DB.
var workRemoteAddPrefixToSet = func(store beads.Store, prefix string) error {
	bd, ok := store.(*beads.BdStore)
	if !ok {
		return fmt.Errorf("remote config step requires the bd work-store provider")
	}
	return bd.ConfigAddToSet(allowedPrefixesConfigKey, prefix)
}

// configStepRemoteAllowedPrefixes unions this city's full prefix set (HQ + every
// bound rig) into the org DB's allowed_prefixes.
func configStepRemoteAllowedPrefixes(store beads.Store, cfg *config.City) error {
	for _, p := range cityScopePrefixes(cfg) {
		if err := workRemoteAddPrefixToSet(store, p); err != nil {
			return err
		}
	}
	return nil
}

// workRemoteReadAllowedPrefixes reads the org DB's current allowed_prefixes set
// (the doctor + self-heal presence check). A non-bd store yields an empty set.
var workRemoteReadAllowedPrefixes = func(store beads.Store) (map[string]bool, error) {
	bd, ok := store.(*beads.BdStore)
	if !ok {
		return map[string]bool{}, nil
	}
	raw, err := bd.ConfigGet(allowedPrefixesConfigKey)
	if err != nil {
		return nil, err
	}
	return parseAllowedPrefixSet(raw), nil
}

// workRemoteRepointScopes drives the marker-aware canonicalization pass over all
// scopes (deliverable F.2). Once the remote marker is present it recomputes the
// city as city_canonical(remote) and mirrors that host/port onto inherited rigs,
// stamps each scope's provenance, and post-write verifies the resolved target.
var workRemoteRepointScopes = func(cityPath string, cfg *config.City, stderr io.Writer) error {
	return normalizeCanonicalBdScopeFiles(cityPath, cfg, stderr)
}

// ── the migration ──────────────────────────────────────────────────────────

// ensureWorkRemote performs the boot-time work-DB relocation to a remote org Dolt
// endpoint. It returns nil when the city is not remote (dark), when the remote
// marker already records completion (converging its quarantine sweep first), or
// when the migration succeeds; it returns a non-nil BOOT-BLOCKING error on any
// gate failure or copy/re-point fault.
func ensureWorkRemote(cityPath string, cfg *config.City, stderr io.Writer) error {
	if cfg == nil || !cfg.Beads.Work.IsRemote() {
		return nil // dark: managed/scoped city
	}
	marker, remotePresent, err := readWorkTopologyMarker(workRemoteMarkerPath(cityPath))
	if err != nil {
		return fmt.Errorf("work remote: reading remote marker: %w", err)
	}
	if remotePresent && marker.isComplete() {
		// Already migrated. Convergently clear any lingering quarantine label from
		// our own copied rows on the shared org DB (the crash-window between marker
		// finalize and the success-path sweep — F6), scoped to THIS city's stamp so a
		// sibling city's migration is never touched. The residue loop drains the
		// recorded local source and the managed-local Dolt stays alive until it does.
		return sweepRemoteQuarantineOnMarkerPresentBoot(cityPath, marker.Stamp, stderr)
	}

	host, port, database, ok := cfg.Beads.Work.RemoteTarget()
	if !ok {
		return fmt.Errorf("work remote: [beads.work] target is not a well-formed dolt://host:port/database endpoint")
	}
	target := workTopologyTarget{Host: strings.TrimSpace(host), Port: strconv.Itoa(port), Database: strings.TrimSpace(database)}

	// Resume vs fresh (F1/F8): a STARTED marker is a durable pre-copy intent. Its
	// recorded Target pins the endpoint — a config retarget between a crashed first
	// copy and completion is refused (the partial rows + allowed_prefixes in the
	// recorded org DB are otherwise stranded invisibly). Its Stamp is the durable
	// discriminator we reuse, so a host reschedule never re-derives a value that
	// turns our own copied rows into a false foreign collision.
	var stamp string
	if remotePresent { // started (isComplete already handled above)
		if marker.Target == nil || !sameRemoteTarget(*marker.Target, target) {
			return fmt.Errorf("work remote blocked: a remote migration to %s is already in progress but [beads.work] target is now %s — resume against the recorded endpoint or explicitly clean up the partial copy (rows + allowed_prefixes) there before retargeting",
				recordedTargetURL(marker.Target), remoteTargetURL(target))
		}
		if strings.TrimSpace(marker.Stamp) == "" {
			return fmt.Errorf("work remote blocked: the in-progress remote marker has no recorded stamp (corrupt) — clean up the partial copy and retry")
		}
		stamp = marker.Stamp
	}

	// Ladder rung gate (success, not order): remote requires the unified rung to
	// have completed. ensureWorkUnified runs first and is boot-blocking.
	satisfied, err := workRemoteUnifiedRungSatisfied(cityPath, cfg)
	if err != nil {
		return fmt.Errorf("work remote: checking unified rung: %w", err)
	}
	if !satisfied {
		return fmt.Errorf("work remote blocked: the unified rung has not completed (work.unified marker absent and rigs still resolve to their own databases) — remote requires unify first")
	}

	// Record the LOCAL unified city database identity BEFORE re-point — the
	// managed endpoint the residue pass drains from (F.1). A managed-local city is
	// recorded with an EMPTY loopback host/port so the straggler technique resolves
	// the CURRENT managed port on every boot (the live port is dynamic and would be
	// stale on a later boot); a genuinely external local endpoint is preserved
	// verbatim.
	localID, err := workUnifyResolveIdentity(cityPath, cityPath)
	if err != nil {
		return fmt.Errorf("work remote: resolving local city work database: %w", err)
	}
	localSrc := workResidueSource{Scope: "hq", Database: localID.database}
	if h := canonicalWorkHost(localID.host, localID.port); h != "" && h != "127.0.0.1" {
		localSrc.Host = localID.host
		localSrc.Port = localID.port
	}

	// Age-gated sweep of crashed temp remote scopes before opening a fresh one.
	sweepStaleRemoteScopes(cityPath, stderr)

	// Open the LOCAL city store (copy source, still resolving managed-local before
	// re-point) and the REMOTE org store (dest, via a per-use Explicit temp scope).
	localStore, closeLocal, err := openWorkUnifyScopeStore(cityPath, cityPath)
	if err != nil {
		return fmt.Errorf("work remote: opening local city work store: %w", err)
	}
	defer closeLocal()
	remoteStore, closeRemote, err := openWorkRemoteScopeStore(cityPath, target)
	if err != nil {
		return fmt.Errorf("work remote: opening remote work store: %w", err)
	}
	defer closeRemote()

	// Credential preflight (C): one bounded authenticated probe BEFORE any write.
	if err := workRemoteCredentialPreflight(remoteStore); err != nil {
		return fmt.Errorf("work remote blocked: cannot authenticate to %s — set BEADS_DOLT_CREDENTIAL_COMMAND or GC_DOLT_PASSWORD for the remote endpoint: %w", remoteTargetURL(target), err)
	}

	// Durable pre-copy intent (F1/F8): BEFORE any org-DB write, persist a STARTED
	// remote marker pinning {Target, Stamp}. A fresh migration mints the stamp once;
	// a resume already loaded it above.
	if !remotePresent {
		stamp, err = mintTopologyStamp()
		if err != nil {
			return fmt.Errorf("work remote: %w", err)
		}
		if err := writeWorkRemoteStartedMarker(cityPath, target, stamp); err != nil {
			return fmt.Errorf("work remote: recording migration intent: %w", err)
		}
	}

	// Config step (D): union this city's prefixes into the org DB allowed_prefixes.
	if err := configStepRemoteAllowedPrefixes(remoteStore, cfg); err != nil {
		return fmt.Errorf("work remote: appending allowed_prefixes: %w", err)
	}

	// Source set (E): export the LOCAL unified city DB (durable + ephemeral,
	// ClassWork only — infra classes never cross), stamped with the persisted
	// discriminator AND the gc.topology_migrating quarantine label (F6) — mid-copy
	// rows must be invisible to every city's claim/ready surfaces on the shared DB
	// until finalize clears the label.
	work, err := exportRemoteWorkSet(localStore, stamp, true)
	if err != nil {
		return fmt.Errorf("work remote: exporting local work set: %w", err)
	}

	imported := 0
	if len(work) > 0 {
		// Two-armed pre-destructive collision protocol (E): pre-probe + first copy
		// with ConflictSkip + conflicted stamp-check.
		report, err := remoteFirstCopy(context.Background(), remoteStore, work, stamp)
		if err != nil {
			return fmt.Errorf("work remote: %w", err)
		}
		if err := verifyCopiedRows(context.Background(), remoteStore, work, report, stamp, workUnifyScope{label: "remote"}); err != nil {
			return fmt.Errorf("work remote: copy-verify: %w", err)
		}
		imported = len(work)
		fmt.Fprintf(stderr, "work remote: %d work beads copied to %s\n", imported, remoteTargetURL(target)) //nolint:errcheck // best-effort stderr
	}

	// Finalize (F.1): upgrade the started marker to complete, recording the local
	// unified database as the (undrained) residue source — only after copy + verify.
	if err := finalizeWorkRemoteMarker(cityPath, target, stamp, localSrc, imported); err != nil {
		return fmt.Errorf("work remote: finalizing marker: %w", err)
	}

	// Re-point (F.2): the now-remote-aware canonicalizer relocates the city to
	// city_canonical(remote) and mirrors it onto inherited rigs, stamping and
	// per-scope-verifying each write (stampWorkTopologyScope → verify) as it goes.
	if err := workRemoteRepointScopes(cityPath, cfg, stderr); err != nil {
		return fmt.Errorf("work remote: re-pointing scopes: %w", err)
	}

	// Straggler pass (F.3): converge copy-window writes from the recorded LOCAL
	// identity into the org DB via a resume copy (pre-probe + guarded upsert). The
	// managed-local Dolt is still running this boot; the background residue loop
	// marks the source drained on a later tick.
	if err := remoteStragglerPass(cityPath, remoteStore, localSrc, stamp, stderr); err != nil {
		return fmt.Errorf("work remote: straggler pass: %w", err)
	}

	// Quarantine clear (F6): convergently remove the migrating label from our own
	// org rows now that the marker is finalized and scopes re-pointed, scoped to our
	// stamp so a sibling city's concurrent migration is never touched.
	if err := sweepRemoteQuarantine(remoteStore, stamp, stderr); err != nil {
		return fmt.Errorf("work remote: clearing quarantine: %w", err)
	}

	fmt.Fprintf(stderr, "gc start: work remote — city work DB moved to %s\n", remoteTargetURL(target)) //nolint:errcheck // best-effort stderr
	return nil
}

// workRemoteUnifiedRungSatisfied reports whether the unified rung has completed:
// the work.unified marker is present, OR unify legitimately had nothing to
// migrate (no bound rig scope resolves away from the city). Because
// ensureWorkUnified runs first and is boot-blocking, marker-absence past it means
// the trivial (nothing-to-unify) case, so a fresh top-rung city still relocates.
func workRemoteUnifiedRungSatisfied(cityPath string, cfg *config.City) (bool, error) {
	present, err := workMarkerPresent(workUnifiedMarkerPath(cityPath))
	if err != nil {
		return false, err
	}
	if present {
		return true, nil
	}
	cityID, err := workUnifyResolveIdentity(cityPath, cityPath)
	if err != nil {
		return false, err
	}
	scopes, err := workUnifyTriggerScopes(cityPath, cfg, cityID)
	if err != nil {
		return false, err
	}
	return len(scopes) == 0, nil
}

// ── the copy: two-armed pre-destructive collision protocol (E) ───────────────

// exportRemoteWorkSet exports the local store's WORK beads (durable + ephemeral),
// dropping infra classes, and stamps each with the persisted remote discriminator
// in gc.topology_source. When quarantine is true (the FIRST-copy call site only —
// resume/residue rows must be live post-marker) it also stamps the
// gc.topology_migrating label so mid-copy rows are withheld from every city's
// claim/ready surfaces on the shared org DB until finalize clears it.
func exportRemoteWorkSet(localStore beads.Store, stamp string, quarantine bool) ([]beads.Snapshot, error) {
	ctx := context.Background()
	raw, err := beads.ExportBeadSnapshotsFrom(ctx, localStore, beads.ExportOptions{IncludeEphemeral: true})
	if err != nil {
		return nil, fmt.Errorf("exporting local snapshots: %w", err)
	}
	var work []beads.Snapshot
	for _, snap := range raw {
		if coordclass.Classify(snap.Bead()) != coordclass.ClassWork {
			continue
		}
		stamped, err := snap.StampMetadata(workTopologySourceMetadataKey, stamp)
		if err != nil {
			return nil, fmt.Errorf("stamping snapshot %s: %w", snap.ID(), err)
		}
		if quarantine {
			stamped, err = stamped.StampLabel(workTopologyMigratingLabel)
			if err != nil {
				return nil, fmt.Errorf("labeling snapshot %s: %w", snap.ID(), err)
			}
		}
		work = append(work, stamped)
	}
	return work, nil
}

// remoteFirstCopy runs the FIRST-copy arm: a pre-destructive pre-probe of the
// entire copy-set on the remote (any id present WITHOUT our stamp = a foreign
// prefix collision → abort before any write), then an insert-if-new ConflictSkip
// import (nothing existing is overwritten even if the probe raced a concurrent
// writer), then a stamp-check of every ConflictSkipped id.
func remoteFirstCopy(ctx context.Context, remoteStore beads.Store, work []beads.Snapshot, ourStamp string) (beads.ImportReport, error) {
	ids := snapshotIDs(work)
	if err := remoteStampCollisionCheck(ctx, remoteStore, ids, ourStamp); err != nil {
		return beads.ImportReport{}, err
	}
	report, err := beads.ImportBeadSnapshotsTo(ctx, remoteStore, work, beads.ImportOptions{ConflictSkip: true})
	if err != nil {
		return beads.ImportReport{}, fmt.Errorf("first copy import: %w", err)
	}
	if len(report.ConflictSkipped) > 0 {
		if err := remoteStampCollisionCheck(ctx, remoteStore, report.ConflictSkipped, ourStamp); err != nil {
			return beads.ImportReport{}, err
		}
	}
	return report, nil
}

// remoteResumeCopy runs a RESUME-pass copy that is genuinely PRE-destructive
// (F5/F10): the stamp collision pre-probe runs over the FULL incoming id set
// BEFORE the guarded upsert — so a foreign row whose clock is older than ours can
// never be silently replaced (and its stamp erased) before the check sees it. A
// residual post-import check covers only the arms where a foreign stamp could
// still surface via the narrow probe→write race (KeptLocal/StaleSkipped/
// ConflictSkipped — Inserted/Updated rows carry our just-written stamp and can
// only "fail" on a sub-second racer already ruled out by the pre-probe).
func remoteResumeCopy(ctx context.Context, remoteStore beads.Store, work []beads.Snapshot, ourStamp string) (beads.ImportReport, error) {
	if err := remoteStampCollisionCheck(ctx, remoteStore, snapshotIDs(work), ourStamp); err != nil {
		return beads.ImportReport{}, err
	}
	report, err := beads.ImportBeadSnapshotsTo(ctx, remoteStore, work, beads.ImportOptions{})
	if err != nil {
		return beads.ImportReport{}, fmt.Errorf("resume copy import: %w", err)
	}
	arms := uniqueStrings(report.KeptLocal, report.StaleSkipped, report.ConflictSkipped)
	if err := remoteStampCollisionCheck(ctx, remoteStore, arms, ourStamp); err != nil {
		return beads.ImportReport{}, err
	}
	return report, nil
}

// remoteStampCollisionCheck fetches the given ids from the remote in bounded
// batches and aborts (naming the id and the foreign source) if any present row is
// not stamped with THIS city's remote discriminator — the prefix-collision guard.
// Ids absent on the remote are fine (they are ours to insert).
func remoteStampCollisionCheck(ctx context.Context, remoteStore beads.Store, ids []string, ourStamp string) error {
	for start := 0; start < len(ids); start += workRemoteProbeBatchSize {
		end := start + workRemoteProbeBatchSize
		if end > len(ids) {
			end = len(ids)
		}
		got, err := beads.GetBeadSnapshotsFrom(ctx, remoteStore, ids[start:end])
		if err != nil {
			return fmt.Errorf("remote collision pre-probe: %w", err)
		}
		for _, snap := range got {
			foreign := strings.TrimSpace(snap.Metadata()[workTopologySourceMetadataKey])
			if foreign != ourStamp {
				if foreign == "" {
					foreign = "(unstamped)"
				}
				return fmt.Errorf("work remote blocked: id %q is already present on the remote work DB stamped %q, not this city's %q — a foreign-city prefix collision; reconcile it (cross-org prefix governance is operator responsibility) before migrating", snap.ID(), foreign, ourStamp)
			}
		}
	}
	return nil
}

// snapshotIDs returns the id list of a snapshot slice.
func snapshotIDs(snaps []beads.Snapshot) []string {
	ids := make([]string, len(snaps))
	for i, s := range snaps {
		ids[i] = s.ID()
	}
	return ids
}

// ── straggler + residue (F.3) ────────────────────────────────────────────────

// remoteStragglerPass re-opens the LOCAL unified database via its recorded
// identity (the temp-scope technique) and runs one resume copy into the org DB so
// writes that landed during the copy window converge.
func remoteStragglerPass(cityPath string, remoteStore beads.Store, localSrc workResidueSource, stamp string, stderr io.Writer) error {
	oldStore, closeFn, err := openWorkUnifyStragglerStore(cityPath, localSrc)
	if err != nil {
		return fmt.Errorf("opening local database: %w", err)
	}
	defer closeFn()
	return importRemoteResidueFromSource(remoteStore, oldStore, stamp, stderr)
}

// importRemoteResidueFromSource exports the LOCAL source's WORK beads, re-stamps
// them with the remote discriminator, and runs a resume copy into the org DB
// (plain guarded upsert + all-arm stamp-check), then reconciles flagged rows'
// dep/label deltas. Import only; nothing is ever deleted from the local database.
func importRemoteResidueFromSource(remoteStore, oldStore beads.Store, ourStamp string, stderr io.Writer) error {
	ctx := context.Background()
	// Resume/residue rows must be LIVE on the shared DB (the migration is past its
	// quarantine window), so no migrating label here.
	work, err := exportRemoteWorkSet(oldStore, ourStamp, false)
	if err != nil {
		return fmt.Errorf("exporting local residue: %w", err)
	}
	if len(work) == 0 {
		return nil
	}
	report, err := remoteResumeCopy(ctx, remoteStore, work, ourStamp)
	if err != nil {
		return err
	}
	return reconcileFlaggedRows(ctx, remoteStore, work, report, stderr)
}

// ── allowed_prefixes self-heal (D, convergent) ───────────────────────────────

// reconcileRemoteAllowedPrefixes re-appends this city's prefixes to the org DB's
// allowed_prefixes when a concurrent city's lost-update evicted them — the
// spec-mandated convergent self-heal, run from the residue/doctor convergence
// path (never the hot boot check). It never removes another city's entries.
func reconcileRemoteAllowedPrefixes(store beads.Store, cfg *config.City, stderr io.Writer) error {
	present, err := workRemoteReadAllowedPrefixes(store)
	if err != nil {
		return err
	}
	var missing []string
	for _, p := range cityScopePrefixes(cfg) {
		if !present[p] {
			missing = append(missing, p)
		}
	}
	if len(missing) == 0 {
		return nil
	}
	for _, p := range missing {
		if err := workRemoteAddPrefixToSet(store, p); err != nil {
			return err
		}
	}
	fmt.Fprintf(stderr, "gc: work remote: re-appended %d evicted prefix(es) to the org DB allowed_prefixes\n", len(missing)) //nolint:errcheck // best-effort stderr
	return nil
}

// parseAllowedPrefixSet tokenizes bd's allowed_prefixes config value into a
// lowercased membership set, tolerating comma/space/bracket separators and bd's
// "(not set)" sentinel (which yields the empty set).
func parseAllowedPrefixSet(raw string) map[string]bool {
	out := map[string]bool{}
	raw = strings.TrimSpace(raw)
	if raw == "" || strings.Contains(raw, "(not set") {
		return out
	}
	isSeparator := func(r rune) bool {
		switch {
		case unicode.IsLetter(r), unicode.IsDigit(r), r == '-', r == '_':
			return false
		default:
			return true
		}
	}
	fields := strings.FieldsFunc(raw, isSeparator)
	for _, f := range fields {
		f = strings.ToLower(strings.TrimSpace(f))
		if f != "" {
			out[f] = true
		}
	}
	return out
}

// ── marker: started intent + finalize (F.1, F8) ──────────────────────────────

// writeWorkRemoteStartedMarker records the durable pre-copy INTENT under the
// cross-process lock — {Target, Stamp} in the started phase — BEFORE the first
// org-DB write. A started marker does not activate the topology (loadWorkTopology
// ignores it); it exists so a crash mid-copy is resumed against the SAME endpoint
// and stamp, and a config retarget is refused instead of stranding a partial copy.
func writeWorkRemoteStartedMarker(cityPath string, target workTopologyTarget, stamp string) error {
	marker := &workTopologyMarker{
		Kind:       workMarkerKindRemote,
		Phase:      workMarkerPhaseStarted,
		RecordedAt: time.Now().UTC(),
		Stamp:      stamp,
		Target:     &target,
	}
	return writeWorkTopologyMarkerLocked(workRemoteMarkerPath(cityPath), marker)
}

// finalizeWorkRemoteMarker upgrades the started marker to COMPLETE under the lock,
// preserving the persisted stamp and target and recording the LOCAL unified
// database identity as the single undrained residue source. Only complete
// activates the topology (re-point, keep-alive, residue).
func finalizeWorkRemoteMarker(cityPath string, target workTopologyTarget, stamp string, localSrc workResidueSource, imported int) error {
	now := time.Now().UTC()
	marker := &workTopologyMarker{
		Kind:       workMarkerKindRemote,
		Phase:      workMarkerPhaseComplete,
		RecordedAt: now,
		Stamp:      stamp,
		Target:     &target,
		ResidueSources: []workResidueSource{{
			Scope:      localSrc.Scope,
			Host:       canonicalWorkHost(localSrc.Host, localSrc.Port),
			Port:       strings.TrimSpace(localSrc.Port),
			Database:   strings.TrimSpace(localSrc.Database),
			RecordedAt: now,
		}},
		Counts: workTopologyCounts{Imported: imported, Verified: imported},
	}
	return writeWorkTopologyMarkerLocked(workRemoteMarkerPath(cityPath), marker)
}

// sameRemoteTarget reports whether two targets name the same endpoint (host
// canonicalized, port + database exact).
func sameRemoteTarget(a, b workTopologyTarget) bool {
	return canonicalWorkHost(a.Host, a.Port) == canonicalWorkHost(b.Host, b.Port) &&
		strings.TrimSpace(a.Port) == strings.TrimSpace(b.Port) &&
		strings.TrimSpace(a.Database) == strings.TrimSpace(b.Database)
}

// recordedTargetURL renders a recorded (possibly nil) marker target for messages.
func recordedTargetURL(t *workTopologyTarget) string {
	if t == nil {
		return "dolt://(unrecorded)"
	}
	return remoteTargetURL(*t)
}

// ── quarantine sweep on the shared org DB (F6) ───────────────────────────────

// sweepRemoteQuarantine removes the gc.topology_migrating label from EVERY row on
// the org store that carries it AND is stamped with THIS city's discriminator — a
// convergent, stamp-SCOPED clear (never a bare label sweep, which on a shared org
// DB would strip a sibling city's in-flight quarantine). A missing row is tolerated.
func sweepRemoteQuarantine(orgStore beads.Store, ourStamp string, stderr io.Writer) error {
	rows, err := orgStore.List(beads.ListQuery{Label: workTopologyMigratingLabel, IncludeClosed: true, TierMode: beads.TierBoth, AllowScan: true})
	if err != nil {
		return fmt.Errorf("listing quarantined org rows: %w", err)
	}
	cleared := 0
	for _, b := range rows {
		if strings.TrimSpace(b.Metadata[workTopologySourceMetadataKey]) != ourStamp {
			continue // a sibling city's row — never touch it
		}
		if err := orgStore.Update(b.ID, beads.UpdateOpts{RemoveLabels: []string{workTopologyMigratingLabel}}); err != nil {
			if errors.Is(err, beads.ErrNotFound) {
				continue
			}
			return fmt.Errorf("removing quarantine label from %s: %w", b.ID, err)
		}
		cleared++
	}
	if cleared > 0 {
		fmt.Fprintf(stderr, "work remote: cleared quarantine label from %d migrated org rows\n", cleared) //nolint:errcheck // best-effort stderr
	}
	return nil
}

// sweepRemoteQuarantineOnMarkerPresentBoot opens the org store (the city resolves
// remote once complete) and runs the stamp-scoped quarantine sweep — the
// convergent marker-present-boot arm (F6). Best-effort store open (a transient org
// unreachability must not brick an otherwise-migrated boot).
func sweepRemoteQuarantineOnMarkerPresentBoot(cityPath, ourStamp string, stderr io.Writer) error {
	if strings.TrimSpace(ourStamp) == "" {
		return nil
	}
	orgStore, closeOrg, err := openWorkUnifyScopeStore(cityPath, cityPath)
	if err != nil {
		fmt.Fprintf(stderr, "gc: work remote: quarantine sweep: opening org store: %v\n", err) //nolint:errcheck // best-effort stderr
		return nil
	}
	defer closeOrg()
	if err := sweepRemoteQuarantine(orgStore, ourStamp, stderr); err != nil {
		fmt.Fprintf(stderr, "gc: work remote: quarantine sweep: %v\n", err) //nolint:errcheck // best-effort stderr
	}
	return nil
}

// ── temp remote scope (real, exec-backed default) ────────────────────────────

// workRemoteScopeBaseDir is the parent dir under which per-use temp remote scopes
// are materialized.
func workRemoteScopeBaseDir(cityPath string) string {
	return filepath.Join(nudgesdb.StoreDir(cityPath), "work-remote")
}

// workRemoteScopeStaleAge bounds how long a crashed temp remote scope may linger
// before the boot sweep removes it. A function so tests can shorten it.
var workRemoteScopeStaleAge = func() time.Duration { return time.Hour }

// sweepStaleRemoteScopes removes temp remote scope dirs older than the stale age
// (leftovers from a crashed open — the close func RemoveAlls on the happy path).
// Best-effort: any error is logged and ignored.
func sweepStaleRemoteScopes(cityPath string, stderr io.Writer) {
	base := workRemoteScopeBaseDir(cityPath)
	entries, err := os.ReadDir(base)
	if err != nil {
		return // absent base dir (never migrated) or transient — nothing to sweep
	}
	cutoff := time.Now().Add(-workRemoteScopeStaleAge())
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		info, err := e.Info()
		if err != nil || info.ModTime().After(cutoff) {
			continue
		}
		if err := os.RemoveAll(filepath.Join(base, e.Name())); err != nil {
			fmt.Fprintf(stderr, "gc: work remote: sweeping stale scope %s: %v\n", e.Name(), err) //nolint:errcheck // best-effort stderr
		}
	}
}

// defaultOpenWorkRemoteScopeStore materializes a per-use TEMPORARY scope root
// under .gc/store/work-remote/ (a RANDOM suffix via os.MkdirTemp, so concurrent
// opens — boot migration vs a cron `gc doctor` probe — never share a dir and can
// never RemoveAll each other's scope out from under a live copy). Its canonical
// .beads state points at the remote target (EndpointOriginExplicit host/port +
// metadata database); it is verified through the rig-grade contract, opened, and
// torn down by the returned close func. The Explicit origin lives ONLY here.
func defaultOpenWorkRemoteScopeStore(cityPath string, target workTopologyTarget) (beads.Store, func(), error) {
	base := workRemoteScopeBaseDir(cityPath)
	if err := os.MkdirAll(base, 0o755); err != nil {
		return nil, func() {}, fmt.Errorf("creating remote scope base dir: %w", err)
	}
	root, err := os.MkdirTemp(base, sanitizeResidueDir(workResidueSource{
		Host: target.Host, Port: target.Port, Database: target.Database,
	})+"-*")
	if err != nil {
		return nil, func() {}, fmt.Errorf("creating remote scope dir: %w", err)
	}
	state := contract.ConfigState{
		EndpointOrigin: contract.EndpointOriginExplicit,
		DoltHost:       strings.TrimSpace(target.Host),
		DoltPort:       strings.TrimSpace(target.Port),
	}
	if err := ensureCanonicalScopeConfigState(fsys.OSFS{}, root, state); err != nil {
		_ = removeAllResidueScope(root)
		return nil, func() {}, fmt.Errorf("writing remote scope config: %w", err)
	}
	if err := ensureCanonicalScopeMetadataForInit(fsys.OSFS{}, root, strings.TrimSpace(target.Database)); err != nil {
		return nil, func() {}, fmt.Errorf("writing remote scope metadata: %w", err)
	}
	// Rig-grade contract validation: confirm the Explicit temp scope resolves to
	// the intended endpoint before opening a store against it.
	resolved, err := contract.ResolveDoltConnectionTarget(fsys.OSFS{}, cityPath, root)
	if err != nil {
		_ = removeAllResidueScope(root)
		return nil, func() {}, fmt.Errorf("resolving remote scope: %w", err)
	}
	if canonicalWorkHost(resolved.Host, resolved.Port) != canonicalWorkHost(target.Host, target.Port) ||
		strings.TrimSpace(resolved.Port) != strings.TrimSpace(target.Port) ||
		strings.TrimSpace(resolved.Database) != strings.TrimSpace(target.Database) {
		_ = removeAllResidueScope(root)
		return nil, func() {}, fmt.Errorf("remote scope resolved to %s:%s/%s, want %s:%s/%s",
			resolved.Host, resolved.Port, resolved.Database, target.Host, target.Port, target.Database)
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

// ── remote collision discriminator (E) — durable, persisted once ─────────────

// mintTopologyStamp mints a fresh collision discriminator: "gc-city:" + 16 random
// bytes hex. It is an IDENTITY, not a derivation — minted ONCE at the start of a
// remote migration and persisted in the marker's Stamp field, so it survives host
// reschedules (a container hostname change must never re-derive a different value
// and turn our own already-copied rows into a false "foreign-city" collision).
var mintTopologyStamp = func() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("minting topology stamp: %w", err)
	}
	return "gc-city:" + hex.EncodeToString(buf), nil
}

// readPersistedRemoteStamp returns the durable collision discriminator recorded in
// the city's remote marker (started or complete). Every remote consumer routes
// through it rather than re-deriving. Returns ("", false, nil) when no remote
// marker exists.
func readPersistedRemoteStamp(cityPath string) (string, bool, error) {
	m, ok, err := readWorkTopologyMarker(workRemoteMarkerPath(cityPath))
	if err != nil {
		return "", false, err
	}
	if !ok || m == nil || strings.TrimSpace(m.Stamp) == "" {
		return "", false, nil
	}
	return m.Stamp, true, nil
}

// remoteTargetURL renders a dolt:// URL for operator-facing messages.
func remoteTargetURL(t workTopologyTarget) string {
	return fmt.Sprintf("dolt://%s:%s/%s", t.Host, t.Port, t.Database)
}

// ── deliverable F.4: managed-local Dolt lifecycle gate ───────────────────────

// workTopologyManagedDoltKeepAlive reports whether a remote city must keep its
// managed-local Dolt lifecycle ENABLED even though its own work endpoint is now
// external: the remote (or the still-draining unify) marker records LOCAL
// databases as residue sources, and the straggler/residue passes read them
// through the managed-local server. It fires ONLY once a COMPLETE remote marker is
// present (i.e. the city has gone external); a plain unified/managed city — and a
// city still in the started (intent) phase — is owned through the normal path and
// never reaches this gate.
//
// FAIL-CLOSED (F12): a marker read fault returns (true, err). Keep-alive is the
// safe default once the gate is reachable at all — a corrupt/EACCES work.remote
// marker on an external city with (presumed) undrained residue must keep the local
// server alive, not release it. Callers that discard the error (bd_env.go's
// last-resort ownership probe) therefore still read owned=true.
func workTopologyManagedDoltKeepAlive(cityPath string) (bool, error) {
	remote, ok, err := readWorkTopologyMarker(workRemoteMarkerPath(cityPath))
	if err != nil {
		return true, err // fail closed: keep the local server alive on a read fault
	}
	if !ok || remote == nil || !remote.isComplete() {
		return false, nil // not external yet (absent or started); normal ownership
	}
	if remote.undrainedResidueCount() > 0 {
		return true, nil
	}
	// External, remote residue drained — but the unify marker may still hold
	// undrained old-rig sources whose content the residue pass reads locally;
	// keep the server alive until those drain too.
	unified, uok, uerr := readWorkTopologyMarker(workUnifiedMarkerPath(cityPath))
	if uerr != nil {
		return true, uerr // fail closed on the unify marker read fault too
	}
	if uok && unified != nil && unified.undrainedResidueCount() > 0 {
		return true, nil
	}
	return false, nil
}

// ── deliverable F.4: managed-LOCAL Dolt launch/stop for the keep-alive arm ───

// managedLocalKeepAliveProviderEnv builds the provider-op env for the keep-alive
// arm (F7): the standard managed provider env with the PROJECTED REMOTE endpoint
// stripped, so the gc-beads-bd script's is_remote check is false and
// op_start/op_health/op_stop run the managed-LOCAL server against the local data
// dir (GC_DOLT_DATA_DIR is untouched). NEVER the remote projection.
func managedLocalKeepAliveProviderEnv(cityPath, provider string) ([]string, error) {
	env, err := providerLifecycleProcessEnvWithError(cityPath, provider)
	if err != nil {
		return nil, err
	}
	m := runtimeEnvEntriesToMap(env)
	clearProjectedDoltEnv(m)
	clearProjectedDoltPasswordEnv(m)
	delete(m, "BEADS_DOLT_CREDENTIAL_COMMAND")
	return mergeRuntimeEnv(nil, m), nil
}

// ensureManagedLocalDoltForKeepAlive is the launch-and-retry side of F.4: it
// starts (or health-confirms) the managed-LOCAL Dolt for a re-pointed remote city
// whose residue is undrained, using the managed-local env so the server binds the
// local data dir. Best-effort (logged, never fatal) — the residue loop and the
// per-tick publisher retry. A seam so the per-tick dispatch is unit-testable.
var ensureManagedLocalDoltForKeepAlive = func(cityPath string, stderr io.Writer) {
	provider := beadsProvider(cityPath)
	if !strings.HasPrefix(provider, "exec:") {
		return
	}
	script := strings.TrimPrefix(provider, "exec:")
	env, err := managedLocalKeepAliveProviderEnv(cityPath, provider)
	if err != nil {
		fmt.Fprintf(stderr, "gc: work remote keep-alive: env: %v\n", err) //nolint:errcheck // best-effort stderr
		return
	}
	release, err := acquireProviderSemaphoreForOp(cityPath, "start")
	if err != nil {
		fmt.Fprintf(stderr, "gc: work remote keep-alive: semaphore: %v\n", err) //nolint:errcheck // best-effort stderr
		return
	}
	defer release()
	if err := runProviderOpWithEnv(script, env, "start"); err != nil {
		if healthErr := runProviderOpWithEnv(script, env, "health"); healthErr != nil {
			fmt.Fprintf(stderr, "gc: work remote keep-alive: managed-local dolt not up (retried next tick): %v\n", err) //nolint:errcheck // best-effort stderr
			return
		}
	}
	if err := publishManagedDoltRuntimeStateIfOwned(cityPath); err != nil {
		fmt.Fprintf(stderr, "gc: work remote keep-alive: publish: %v\n", err) //nolint:errcheck // best-effort stderr
	}
}

// stopManagedLocalDoltAfterKeepAlive is the release side of F.4: once every
// residue source drains, it stops a leftover managed-LOCAL server (started for
// keep-alive) with the managed-local env and clears its runtime state. Gated on a
// live provider state so a normal external (hosted-gateway) city that never ran a
// local server is never touched. A seam for unit-testability.
var stopManagedLocalDoltAfterKeepAlive = func(cityPath string, stderr io.Writer) {
	if _, ok := readValidProviderManagedDoltState(cityPath); !ok {
		return // no leftover local server for this city
	}
	provider := beadsProvider(cityPath)
	if !strings.HasPrefix(provider, "exec:") {
		return
	}
	script := strings.TrimPrefix(provider, "exec:")
	env, err := managedLocalKeepAliveProviderEnv(cityPath, provider)
	if err != nil {
		return
	}
	release, err := acquireProviderSemaphoreForOp(cityPath, "stop")
	if err != nil {
		return
	}
	defer release()
	if err := runProviderOpWithEnv(script, env, "stop"); err != nil {
		fmt.Fprintf(stderr, "gc: work remote: stopping drained keep-alive dolt: %v\n", err) //nolint:errcheck // best-effort stderr
		return
	}
	_ = clearManagedDoltRuntimeStateUnlessPostgres(cityPath)                                   //nolint:errcheck // best-effort
	fmt.Fprintf(stderr, "gc: work remote: managed-local dolt stopped — all residue drained\n") //nolint:errcheck // best-effort stderr
}

// reconcileWorkRemoteKeepAliveTick is the per-tick driver of the F.4 managed-local
// lifecycle: on a COMPLETED remote city it launches (undrained) or stops (drained)
// the managed-local server and reports handled=true so the caller skips the normal
// (remote-env) publisher. It is a no-op / handled=false on every other city.
func reconcileWorkRemoteKeepAliveTick(cityPath string, stderr io.Writer) (handled bool) {
	m, ok, err := readWorkTopologyMarker(workRemoteMarkerPath(cityPath))
	if err != nil || !ok || m == nil || !m.isComplete() {
		return false
	}
	keep, _ := workTopologyManagedDoltKeepAlive(cityPath) // fail-closed (true on fault)
	if keep {
		if currentResolvableManagedDoltPort(cityPath) == "" {
			ensureManagedLocalDoltForKeepAlive(cityPath, stderr)
		}
		return true
	}
	stopManagedLocalDoltAfterKeepAlive(cityPath, stderr)
	return true
}

// reconcileWorkRemoteShutdown stops a completed remote-work city's managed-LOCAL
// Dolt at gc stop with the managed-local env, and reports handled=true so the
// caller skips the remote-env stop (which would resolve is_remote and leak the
// local process). handled=false on every other city.
func reconcileWorkRemoteShutdown(cityPath string) bool {
	m, ok, err := readWorkTopologyMarker(workRemoteMarkerPath(cityPath))
	if err != nil || !ok || m == nil || !m.isComplete() {
		return false
	}
	stopManagedLocalDoltAfterKeepAlive(cityPath, io.Discard)
	return true
}

// ── deliverable G: doctor remote-auth + allowed_prefixes surface ─────────────

// remoteDoctorStatus is the cached remote-auth + allowed_prefixes surface the
// doctor line renders on a remote-target city.
type remoteDoctorStatus struct {
	reachable       bool
	authDetail      string
	prefixesPresent bool
	missing         []string
}

// workRemoteDoctorProbe is the seam the doctor line calls; the default is cached
// and rate-limited so the doctor never becomes a hot-path org-DB scan. Overridden
// in unit tests with a pure fake.
var workRemoteDoctorProbe = cachedRemoteDoctorProbe

var (
	remoteDoctorProbeMu    sync.Mutex
	remoteDoctorProbeCache = map[string]remoteDoctorProbeEntry{}
)

type remoteDoctorProbeEntry struct {
	status remoteDoctorStatus
	at     time.Time
}

// remoteDoctorProbeTTL rate-limits the doctor's authenticated probe. A function
// so tests can shorten it.
var remoteDoctorProbeTTL = func() time.Duration { return time.Minute }

// cachedRemoteDoctorProbe returns a recent cached probe when one is fresher than
// the TTL, else runs one authenticated bounded probe + allowed_prefixes read and
// caches it.
func cachedRemoteDoctorProbe(cityPath string, cfg *config.City) remoteDoctorStatus {
	key := normalizePathForCompare(cityPath)
	remoteDoctorProbeMu.Lock()
	if e, ok := remoteDoctorProbeCache[key]; ok && time.Since(e.at) < remoteDoctorProbeTTL() {
		remoteDoctorProbeMu.Unlock()
		return e.status
	}
	remoteDoctorProbeMu.Unlock()

	status := rawRemoteDoctorProbe(cityPath, cfg)

	remoteDoctorProbeMu.Lock()
	remoteDoctorProbeCache[key] = remoteDoctorProbeEntry{status: status, at: time.Now()}
	remoteDoctorProbeMu.Unlock()
	return status
}

// workRemoteDoctorProbeTimeout is the wall-clock bound on the doctor's
// authenticated probe (F9): an unreachable remote must degrade the doctor line in
// seconds, not bd's flat 120s. A function so tests can adjust it.
var workRemoteDoctorProbeTimeout = func() time.Duration { return 8 * time.Second }

// rawRemoteDoctorProbe opens the temp remote scope and runs the bounded credential
// probe + allowed_prefixes read, ALL under a short context deadline enforced on
// the bd subprocess (F9), so an unreachable remote degrades the doctor line within
// workRemoteDoctorProbeTimeout instead of hanging the whole `gc doctor` run.
func rawRemoteDoctorProbe(cityPath string, cfg *config.City) remoteDoctorStatus {
	host, port, database, ok := cfg.Beads.Work.RemoteTarget()
	if !ok {
		return remoteDoctorStatus{authDetail: "target not a well-formed dolt:// endpoint"}
	}
	target := workTopologyTarget{Host: strings.TrimSpace(host), Port: strconv.Itoa(port), Database: strings.TrimSpace(database)}
	ctx, cancel := context.WithTimeout(context.Background(), workRemoteDoctorProbeTimeout())
	defer cancel()
	store, closeFn, err := openWorkRemoteScopeStore(cityPath, target)
	if err != nil {
		return remoteDoctorStatus{authDetail: err.Error()}
	}
	defer closeFn()
	// Prefer the ctx-enforcing BdStore methods (real subprocess cancellation); a
	// non-bd store falls back to the unbounded seams (native leg is out of v1).
	if bd, isBd := store.(*beads.BdStore); isBd {
		if err := bd.CredentialPreflight(ctx); err != nil {
			return remoteDoctorStatus{authDetail: err.Error()}
		}
		status := remoteDoctorStatus{reachable: true, prefixesPresent: true}
		raw, err := bd.ConfigGetContext(ctx, allowedPrefixesConfigKey)
		if err != nil {
			status.prefixesPresent = false
			status.authDetail = "allowed_prefixes read: " + err.Error()
			return status
		}
		return doctorPrefixStatus(status, parseAllowedPrefixSet(raw), cfg)
	}
	if err := workRemoteCredentialPreflight(store); err != nil {
		return remoteDoctorStatus{authDetail: err.Error()}
	}
	status := remoteDoctorStatus{reachable: true, prefixesPresent: true}
	present, err := workRemoteReadAllowedPrefixes(store)
	if err != nil {
		status.prefixesPresent = false
		status.authDetail = "allowed_prefixes read: " + err.Error()
		return status
	}
	return doctorPrefixStatus(status, present, cfg)
}

// doctorPrefixStatus fills the prefix-presence fields of a reachable status.
func doctorPrefixStatus(status remoteDoctorStatus, present map[string]bool, cfg *config.City) remoteDoctorStatus {
	for _, p := range cityScopePrefixes(cfg) {
		if !present[p] {
			status.missing = append(status.missing, p)
		}
	}
	status.prefixesPresent = len(status.missing) == 0
	return status
}
