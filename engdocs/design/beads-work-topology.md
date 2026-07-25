# Work-bead topology: the three axes

Status: Accepted, revised after two adversarial review rounds (round 1:
36 findings, `wf_3692f135-fd4`; round 2 verified 30 closed and attacked
the new machinery, 18 further findings, `wf_959ea736-89e` — all folded in
below). Companion to
`engdocs/design/infra-class-sqlite-stores.md` (the infra-class relocation
this builds on) and the deploy-lineage integration goal: replace window 3's
ad hoc split with this branch so a remote task DB can be shared by all
cities while every city/rig keeps local private sqlite stores for the infra
classes.

## The axes

1. **Infra residence** — `[beads] infra = "local"`: aggregate sugar that
   resolves every relocatable class (graph, messaging, sessions, orders,
   nudges) to `backend = "sqlite"`. The existing `[beads.classes.<name>]`
   knobs remain the fine-grained form and EXPLICIT per-class settings win
   over the aggregate. The already-shipped marker-gated boot migrations do
   the moves; after one boot the Dolt side holds only work beads.
2. **Task-DB scope** — `[beads.work] scope = "scoped" (default) | "unified"`:
   scoped is today's topology (one Dolt DB per rig + one for the city).
   Unified merges every rig's work beads into the CITY scope's database;
   each scope keeps its own `issue_prefix` for history AND for new mints
   (via the prefix-override prerequisite below), so ids stay disjoint and
   cross-rig references become real edges in one `deps` table.
3. **Task-DB target** — `[beads.work] target = "managed" (default) |
   "dolt://host:port/database"`: where the unified DB lives. Remote is the
   enterprise end state: one org task DB shared by many cities, each city
   keeping its own prefixes. ONE-WAY: once remote, `managed` is rejected.

Ladder rule: `target != managed` requires `scope = "unified"`; `scope =
"unified"` implies infra = local (all five classes). Boot walks the rungs
in order — classes, then unify, then remote — and **each rung gates on the
previous rung's SUCCESS, not on call order**: `ensureWorkUnified` requires
all five class-migrated markers present, `ensureWorkRemote` requires the
unified marker present. Each rung is idempotent and prints progress. A
fresh city configured at the top rung starts there with nothing to migrate.

## Prerequisite: bd workspace-prefix minting (beads-repo slice, ships first)

bd today mints auto ids from the DATABASE's config-table `issue_prefix`
(`ReadConfigPrefix` in `internal/storage/issueops/create.go`); the
workspace `config.yaml` prefix is consulted only when the DB row is
missing. One database has one mint prefix, so after re-pointing rig scopes
at a shared database every auto-minted bead would take the DB's prefix and
per-scope id attribution would silently die. `types.Issue.PrefixOverride`
already exists in the storage layer ("for cross-rig creation") and the
create path honors it, but nothing exposes it.

Required beads change (small, backward-compatible): when the workspace
`config.yaml` `issue-prefix` differs from the DB config prefix AND is
listed in the DB's `allowed_prefixes`, bd mints under the workspace
prefix. The yaml-preference is implemented at the SHARED mint-prefix
resolution seam (set `Issue.PrefixOverride` where the CLI resolves the
workspace prefix), so `bd create`/`quick`/`batch`/`markdown` and molecule
creation all inherit it — pinned by a test on at least one non-create
surface. Also expose `bd create --prefix`, and a `bd config add-to-set`
primitive (transactional server-side append — needed for the
`allowed_prefixes` step below). Behavior is unchanged when
`allowed_prefixes` is empty or the workspace prefix is not listed. This
slice lands and is pinned by the side-by-side and mint-continuation tests
(see Red-team pins) BEFORE any city flips `scope = "unified"`. Both
migrations refuse to run against a bd binary that fails a capability
probe for this behavior; the probe is fork-proof — `bd create --help`
advertising `--prefix`, or a capabilities key in `bd version --json` —
never a version-number comparison (this fleet pins forked bd builds).

## Config surface (internal/config)

- `BeadsConfig` gains `Infra string` (toml `infra`, admitted values: `""` |
  `"local"` — there is no `"bd"` value; the default IS bd) and
  `Work BeadsWorkConfig` (toml `work`).
- `BeadsWorkConfig{ Scope string; Target string }` — toml `scope`,
  `target`. Empty scope = "scoped"; empty target = "managed". Remote
  target parse: `dolt://host:port/database` (all three parts required;
  anything else is a load error).
- jsonschema tags: `Infra` `enum=local`; `Scope` `enum=scoped,enum=unified`;
  `Target` documented as `managed | dolt://host:port/database` (pattern).
  Regenerate `docs/reference/schema/city-schema.json` via the doc-gen
  hook. `BeadsWorkConfig` is appended to the `knownTOMLKeys` type list
  explicitly (`collectTOMLTags` is non-recursive).
- Resolution: `EffectiveInfraLocal() bool` := infra=="local" || scope
  unified || target remote. `ClassBackend(class)`: explicit per-class value
  wins; else sqlite when `EffectiveInfraLocal()`; else bd.
- Validation (`validateBeads*`):
  - unknown infra/scope/target values: load error.
  - target remote && scope != unified: load error.
  - explicit `[beads.classes.<x>] backend="bd"` while scope unified or
    target remote: load error (there is no correct reading of "share my
    sessions through the org task DB").
  - `[beads.classes.<x>] shadow = true` while the EFFECTIVE
    `ClassBackend(x)` is sqlite — explicit or implied via infra/scope/
    target: load error naming the implying knob ("shadow is a pre-flip
    soak knob; `[beads.work] scope=\"unified\"` implies
    backend=\"sqlite\" for sessions — drop shadow before unifying").
    Implementation: the check in `validateBeadsClasses` tests
    `ClassBackend(class)`, not `entry.Backend`. Belt-and-braces:
    `seedSessionsShadowAtBoot` additionally refuses to run when sessions
    routing is marker-active.
  - `ValidateBeadsClassPrefixes` activates whenever
    `EffectiveInfraLocal()` OR any configurable class has effective
    `ClassBackend(class) != "bd"` (iterate `beadClassConfigurable` over
    the effective backend, not the explicit Classes map) — the implied
    form triggers the same reserved-prefix rejection as the explicit form.
  - scope unified: pairwise-distinct effective prefixes across HQ
    (`EffectiveHQPrefix`) + every rig (`EffectivePrefix`),
    case-insensitive, and none may collide with a reserved class prefix.
- One-way doors — enforcement is a shared check, not a boot-only check.
  `checkWorkTopologyMarkers(cityPath, cfg)` compares the
  `work.unified`/`work.remote` markers (and the recorded endpoint
  identities in their payloads) against the loaded config and the LIVE
  resolved connection targets, and is called from: (a) controller boot —
  refuse to start; (b) config reload — reject the new config and keep the
  old; (c) the class routing resolvers and the `doBd` front door — on a
  city whose markers contradict the loaded config, routing resolution
  returns an ERROR (fail closed), never the bd fallback. The guard keys on
  observed state, not only the marker: re-pointed scopes + `scope=scoped`,
  or a scope resolving to the recorded remote endpoint + `target=managed`,
  refuse even if a marker file was lost. Observed state means POSITIVE
  PROVENANCE, never name coincidence: every topology-driven
  canonicalization write stamps the scope's `.beads` state with a
  work-topology provenance mark, and the marker-less observed arms fire
  only on that stamp — a legacy rig whose metadata happens to name the
  city database (a real, preserved state) must stay DARK, while stamps
  survive marker loss, which is the arm's whole purpose. A unified
  marker present suppresses the reverted-remote inference (the durable
  marker beats endpoint inference), and a city whose external endpoint
  is corroborated by hosted project identity (`.beads/identity.toml`)
  is external-from-birth, exempt. Live-resolved-target evidence
  triggers refusal ONLY for those enumerated reverted-config cases:
  marker-present scopes still resolving to their recorded LEGACY
  identities with config matching the marker are the self-heal window,
  and the boot check runs after (or tolerates) the canonicalization pass
  that converges them. Marker writes and residue-source appends hold a
  cross-process file lock (concurrent `gc rig add` vs controller
  canonicalization must never lose a recorded residue source), residue
  identities are stored host-canonicalized, and the unified desired rig
  database is the city's RESOLVED database (its metadata may carry an
  imported name), never the legacy default constant.

## The copy primitive: snapshot import with a guarded upsert

`CreateWithForeignID` exists only on the sqlite class stores; no bd/Dolt
create path preserves status or clocks, and per-row creates would fire the
bd `on_create`/`on_update` hooks (a `gc event emit` storm) for every
historical bead. Both migrations therefore use ONE primitive, defined at
the store boundary:

- `ImportBeads(ctx, snapshots) (ImportReport, error)` — a full-fidelity,
  hook-silent, guarded upsert. Each snapshot carries id, status, all
  clocks (created/updated/started/closed), close reason, labels, metadata,
  deps, comments, tier — verbatim. Semantics per row: absent → insert;
  present with incoming `updated_at` newer → replace; present with equal
  clock or newer local → keep local and REPORT the id. Dangling dep edges
  (endpoint not present) are skipped and REPORTED, never silently dropped.
  A closed bead never transits through an open state.
- Backends: `BdStore` implements it by shelling `bd import` (JSONL on
  stdin), which already has exactly these semantics — verbatim clocks and
  status, `updated_at`-guarded upsert with `StaleSkippedIDs` /
  `TieKeptLocalIDs` / `SkippedDependencies` reporting, and a batch write
  path (`CreateIssuesWithFullOptions`) that the hook decorator does not
  wrap, so no per-issue hooks fire. `NativeDoltStore` implements the same
  semantics in direct SQL. Title-dedup stays OFF (`bd import` default).
- The snapshot SOURCE is a raw-fidelity export surface, never the gc
  `Bead` struct (`Bead`/the `bdIssue` decode envelope carry no
  closed_at/started_at/close_reason/comments — marshalling through them
  would silently fabricate close clocks and drop comment history, and bd
  import would "fill in" closed_at with import time). `BdStore` sources
  snapshots by shelling `bd export` (the canonical full-fidelity pair to
  `bd import` — JSONL passthrough); `NativeDoltStore` sources via direct
  SQL over the full column set. Copy-verify reads through the SAME raw
  surface. Class membership is decided by `coordclass.Classify` over the
  decoded rows; gc owns the stream, so it stamps every snapshot's
  metadata with `gc.topology_source = <city identity>` before import (the
  remote collision discriminator below). Pinned by an export→import
  round-trip test.
- The guarded upsert IS the resume mechanism: re-running an interrupted
  copy converges every row to the newest content — a bead closed in the
  still-authoritative source after a partial copy wins over the stale open
  city copy on the re-run. No skip-existing, no delete-then-recopy.
- Same-second ties don't converge on their own (`updated_at` compares at
  second granularity; equal-clock-different-content keeps local). For
  every reported tie-kept/kept-local id while the SOURCE is still
  authoritative (pre-re-point, and straggler/residue passes from a
  recorded identity), the migration re-imports exactly those ids with the
  stale-guard override — and the override is CONDITIONAL, not absolute:
  it overwrites only when the incoming clock is >= the destination's
  (native: in the upsert arm; bd leg: a bounded per-id pre-probe filters
  ids whose destination has advanced), so a destination write landing
  between the tie report and the re-import is never clobbered. The
  source-is-authoritative precondition is part of the API contract.
  ConflictSkip and the stale override are mutually exclusive (validation
  error), mirroring bd's own flag exclusion.
- Leg realities (both migrations depend on these): the bd-leg import
  subprocess inherits the SAME scoped env every other bd call on that
  store gets (BEADS_DIR, auto-start/auto-export suppression, routing
  opt-outs, credentials) — a bare env import can land in a different
  database than export read; import deadlines scale with batch size on
  both legs; the bd leg parses `conflict_skipped_ids` from the capable
  bd's JSON result (the remote stamp-check needs it); the bd leg
  preflights `export.exclude_owners` and refuses export while it is set
  (silently thinned streams are invisible to copy-verify); per-id raw
  snapshot fetch (`bd show <ids...> --json` / native bulk read) is the
  copy-verify and stamp-check read path — never a full destination
  export.
- The EPHEMERAL (wisp) tier CROSSES: the export surface takes an
  include-ephemeral option (native drops its filter; bd leg exports the
  full set and re-filters infra/templates/memories at decode) and the
  unify snapshot step uses it — TierBoth means both tiers, and an
  in-flight wisp molecule stranded in an abandoned rig database would
  otherwise never complete. A label-stamp sibling of the metadata stamp
  carries the `gc.topology_migrating` quarantine label on copied rows.
- Dep edges attached to stale-skipped/tie-kept rows never enter the
  import batch and are NOT reported in `SkippedDependencies` (and dep
  adds don't bump `updated_at`). So for every id the report lists as
  stale-skipped or tie-kept, the residue pass additionally diffs the
  dependency (and label) sets between source and destination via Get and
  applies missing edges; a row counts as drained only when its edges do.
- Known residual (accepted, v1): a bead TOMBSTONED in the source after
  copy is not deleted from the destination by re-import (import skips
  tombstones). Work-bead deletion is not a gc flow; the doctor line
  surfaces residue-drain state and the operator can reconcile manually.
- No `bead.created` events are emitted for migrated history — pinned by
  test (see Red-team pins).

## Topology-aware canonicalization (the re-point mechanism)

gc has standing machinery that recomputes desired scope endpoint state from
`city.toml` and rewrites `.beads/config.yaml` + `.beads/metadata.json`
(`seedDeferredManagedBeadsBeforeProviderReadiness` →
`desiredScopeDoltConfigStateForInit` → `resolveDesiredCityEndpointState` /
`ensureCanonicalScopeConfigState`, plus the recurring
metadata.json-recreation incident where `defaultScopeDoltDatabase`
resurrects the per-rig database). Any out-of-band re-point would be
REVERTED by the next canonicalization pass — split-brain by design.

So the re-point is not a migration side effect; it is the canonicalizer
converging on topology-aware desired state:

- The desired-endpoint-state resolvers (`resolveDesiredCityEndpointState`,
  the rig desired-state path, `defaultScopeDoltDatabase`,
  `desiredScopeDoltConfigStateForInit`) consult the topology markers:
  - `work.unified` present → desired RIG database = the city database;
    rig origin `inherited_city`; under a managed city rigs track no
    host/port (contract rule), under a canonical city they mirror it.
  - `work.remote` present → desired CITY state = origin
    `city_canonical` with the target's host/port (+ user); desired RIG
    state = `inherited_city` MIRRORING that host/port (the contract
    requires mirrored host/port for inherited rigs under a canonical
    city). `EndpointOriginExplicit` never appears in a persisted scope —
    the contract rejects it for the city scope.
  - Every scope keeps its own `issue_prefix` in all states.
- **A topology-aware canonicalizer never silently discards an observed
  endpoint.** Canonicalization runs at rig-add/init-preflight, BEFORE the
  boot migration block — so before rewriting any scope whose CURRENT
  resolved (Host, Port, Database) differs from the desired topology
  target, it appends that identity to the work marker payload's
  residue-source list (atomic read-modify-write). A late-bound rig that
  arrives carrying legacy `.beads` state (unbound during the original
  unify, a dir moved from another city, a backup restore) is therefore
  drained by the residue-convergence pass — that recording, not
  ensureWorkUnified observing the pre-canonicalized state, is what
  migrates late rigs.
- Because every boot's canonicalization now converges scopes TOWARD the
  topology, a crash between marker and re-point self-heals, a deleted
  metadata.json is recreated pointing at the unified/remote database (not
  the legacy per-rig one), and **rigs added fresh after the marker are
  born pointing at the city database** — provisioning needs no special
  case.
- After each canonicalization write the migration VERIFIES by calling
  `contract.ResolveDoltConnectionTarget` on the scope and comparing
  (Host, Port, Database) to the intended target; a mismatch aborts. The
  metadata write goes through `EnsureCanonicalMetadata` writing the field
  `ReadDoltDatabase` consumes — never a hand-guessed key.
- Once the city's work origin is external (`work.remote`), the managed
  local Dolt lifecycle for WORK stays ENABLED until every residue source
  recorded in the marker payload is verified drained and recorded as such
  — the recorded local identities ARE that server's host/port, and the
  straggler/residue passes need it running. The residue pass treats
  "local server not running" as a launch-and-retry condition, not a
  skip. Only after all sources are drained does boot stop launching the
  local server.

## Unify migration (cmd/gc, boot)

`ensureWorkUnified(cityPath, cfg, stderr) bool`, called in city_runtime
after the five ensure*ClassMigrated calls.

- Trigger: scope unified && ANY bound rig scope's resolved connection
  target differs from the city's (the `work.unified` marker records
  completion for doctor and for the desired-state resolvers; the per-rig
  resolved-identity check is the trigger). Rigs that arrive later with
  legacy state are handled by the canonicalizer's residue-source
  recording (previous section), not by this trigger.
- **A failed or aborted ensureWorkUnified is BOOT-BLOCKING** — the
  controller refuses to start (same refusal surface as
  `checkWorkTopologyMarkers`), so a partial copy is never exposed to a
  live reconciler/dispatcher. Because CLI one-shots run without the
  controller, mid-copy rows are additionally QUARANTINED until the
  marker: the import stamps a `gc.topology_migrating` label on every
  copied row, the hook/claim/ready surfaces exclude labeled rows
  (unconditionally in the work-query runner — not only on graph-routed
  cities), and the label clear is CONVERGENT, not a one-shot tail step:
  a sweep-by-label runs on every marker-present boot until no labeled
  rows remain, so a crash anywhere after the marker write cannot leave
  the migrated backlog invisible. (Otherwise: unify
  copies rig A then aborts on rig B; the city runs for days; an agent
  claims the city COPY of an open rig-A bead, its `updated_at` now
  newest, and the eventual re-run's guarded upsert keeps the claim and
  discards a rig-side close.)
- Gate (success, not order): all five class-migrated markers present for
  every class `EffectiveInfraLocal` routes to sqlite; if any is absent,
  print the blocking class and return false BEFORE any copy or re-point.
  Then run one SYNCHRONOUS class-residue import pass per rig scope to
  convergence (import only — the 10-minute grace governs deletion, never
  import), so no infra bead is still bd-resident when the rig's scope
  stops resolving to its legacy database. Any failure aborts unify.
- Preflight, before any copy:
  - prefix distinctness (again, against live config);
  - every bound rig scope store must OPEN (abort otherwise);
  - bd prefix-override capability probe passes (Prerequisite above);
  - cross-source collision check: List the city store once
    (IncludeClosed); a city-store row carrying a rig's prefix that the
    rig store does NOT hold is split-brain → abort naming the id and
    both sources. (Rows both stores hold are a prior partial copy; the
    guarded upsert converges them.)
- Config step: set the city database's `allowed_prefixes` to the union of
  all scope prefixes (needed for both explicit-id writes and
  prefix-override minting).
- Per rig scope (skip scopes already resolving to the city database):
  1. Snapshot: `List(IncludeClosed, TierBoth)` from the rig store; keep
     `Classify == ClassWork` rows. Closed beads DO cross — task history
     is the product, unlike infra.
  2. `ImportBeads` into the city store (guarded upsert; ids, status,
     clocks preserved; deps ride along; source-stamped).
  3. Copy-verify a sample per rig plus every id the report flagged;
     verification compares status and close clock, not just presence.
     Surface `SkippedDependencies` in the progress output and the marker
     payload. For a skipped edge whose far endpoint is an open GRAPH
     bead, stamp the work bead with the `gc.attached_workflow_root`
     metadata linkage instead (the landmine-#4 conversion); other
     dropped edges are logged id-pair by id-pair.
  4. Progress: one line per batch ("unify: rig X: 400/3200 imported").
- Marker: written atomically (temp+rename) only after every rig imported
  + verified. The payload records, per rig, the OLD resolved database
  identity (host, port, database) — the straggler/residue source list.
- Re-point: run the (now marker-aware) canonicalization pass over all
  scopes; verify each scope's resolved target per the section above.
- Straggler pass: immediately after re-pointing each rig, re-open its OLD
  database via the recorded identity (never via scope resolution; when
  the recorded host/port are empty, the temp scope writes an EXPLICIT
  loopback endpoint resolved from the current managed runtime — never
  `managed_city`, which the contract rejects for non-city scopes) and
  run one more `ImportBeads` pass — writes that landed during the copy
  window converge via the guarded upsert, the infra-class residue import
  re-runs against the same recorded identity, and flagged rows
  (tie-kept/stale-skipped/conflict-skipped) get their dep/label deltas
  applied via the link diff plus the conditional stale re-import. On
  COUNTER-mode databases v1 REFUSES unify at preflight, naming the
  missing counter-advance guard (hash-id databases, the gc default, are
  unaffected); the straggler/residue imports additionally treat an
  incoming id that exists with a DIFFERENT `created_at` as a reported
  conflict (id + both sources named), never an upsert. Copy-verify reads
  are routing-proof (a verify satisfiable by the SOURCE database via bd
  prefix routing is no verify at all). v1 unify requires the bd
  work-store provider — a native-provider city is refused at the
  capability gate with the config alternatives named.
- Residue convergence: a background pass re-imports from each recorded
  identity until a drain check passes — source ClassWork rows all
  present-or-older in the unified DB AND their dep/label sets reflected,
  where dep edges whose far endpoint is a reserved-class bead are
  represented by the graph-attach metadata conversion, not work-store
  deps, and are excluded from the diff. The pass runs at boot, RE-ARMS
  in-process whenever a residue source is appended (a rig added to a
  long-lived controller must not wait for a reboot), and retries on a
  slow ticker while undrained sources remain. Only after drained is the
  old database cold backup. Old rig databases are LEFT IN PLACE; the
  city teardown machinery already knows how to drop them later.
- Quiesce: unify runs at controller boot before the reconciler and
  dispatcher start. Live agents in surviving tmux sessions are the
  residual concurrent-writer risk — the straggler + residue passes (not
  quiesce) are the correctness mechanism for them.

## Remote migration (cmd/gc, boot)

`ensureWorkRemote(cityPath, cfg, stderr) bool`, after ensureWorkUnified.

- Trigger: target remote && unified marker present &&
  `.gc/store/work.remote` marker absent.
- Remote open: materialize a TEMPORARY scope root under `.gc/store/`
  (rig-grade contract validation applies there) whose `config.yaml`
  (`EndpointOriginExplicit`, host/port from the target) and
  `metadata.json` (database from the target) are written with
  `EnsureCanonicalConfig`/`EnsureCanonicalMetadata`; torn down after the
  migration. `Explicit` lives ONLY here, never in a persisted scope.
- Credential preflight: one authenticated BOUNDED probe through the temp
  scope BEFORE any copy — `bd list --limit 1` or a config-table read
  (`bd config get issue_prefix`), never the current `Ping` (which is
  `bd list --limit 0`, a full org-DB pull); on failure abort naming the
  required env (`BEADS_DOLT_CREDENTIAL_COMMAND` / `GC_DOLT_PASSWORD`).
  The one-way boot check distinguishes "remote configured but
  unauthenticatable" (refuse loudly, name the env) from "marker/config
  mismatch". No new auth machinery — the existing bd
  env/credential-command path.
- Config step: append this city's prefixes to the org DB's
  `allowed_prefixes` via the prerequisite slice's transactional
  `bd config add-to-set` (a plain read-modify-write is a lost-update race
  against concurrent cities; never remove other cities' entries). The
  remote-target boot topology check and the doctor line verify this
  city's prefixes are still present and re-append when absent
  (convergent self-heal), so an eviction is detected and repaired
  instead of silently degrading mints to the org prefix.
- Copy — collision discrimination is TWO-armed and pre-destructive,
  because the guarded upsert would otherwise overwrite a foreign city's
  same-id bead whenever OUR row is newer (reported only in
  `UpdatedIssues`) and install our own stamp over the evidence:
  1. Pre-probe: before the FIRST import stream, Get the entire copy-set
     id list on the remote in batches (`bd show` accepts multiple ids;
     `GetIssuesByIDs` on the native store). Any id present WITHOUT our
     `gc.topology_source` stamp is a PREFIX COLLISION → abort before any
     write, naming the id and the foreign source.
  2. First copy runs INSERT-IF-NEW (expose `ImportOptions.ConflictSkip`
     as `bd import --conflict-skip`; the native equivalent), so nothing
     existing is overwritten even if the probe raced a concurrent
     writer; every conflicted id is stamped-checked as in (1).
  3. Resume passes (after a recorded partial copy) run the plain guarded
     upsert but stamp-check ALL report arms — `UpdatedIssues` included,
     not just kept-local/stale-skipped.
  Whole-prefix discovery of foreign beads we do not hold locally is OUT
  of v1: no prefix-filtered list exists on any bd surface, and a full
  org-DB scan is not a "probe". Cross-org prefix governance is operator
  responsibility, documented.
- Marker (payload records the local unified database identity as the
  residue source), then the marker-aware canonicalization re-points city
  + rigs per the origin matrix above, then straggler + residue passes
  from the recorded local identity, same as unify.
- A dolt-native push/clone fast path is a recorded optimization, not v1.

## Runtime aftermath (required changes, not follow-ups)

Unify makes N+1 scope directories resolve to ONE database; remote makes
that database org-shared. Both break assumptions baked into the fan-outs:

- **Shared handle per endpoint.** A process-level store registry keyed by
  resolved (Host, Port, Database) returns per-scope FACADES over one
  shared underlying handle. The shared identity deduplicates the
  query/by-id plane — every existing identity-keyed dedup
  (`sortedRigNames`, `seen[beads.Store]`) becomes correct again for
  free, and per-tick fan-outs collapse to one query per endpoint — but
  the WRITE plane stays scope-aware: every auto-mint Create carries the
  ORIGINATING scope's prefix explicitly (the prerequisite slice's
  `--prefix`/`PrefixOverride`), never the representative dir's
  config.yaml prefix, or post-unify SDK creates for rig B (e.g. sling's
  auto-convoy) would mint rig A's prefix. Lifecycle: registry-owned
  handles are closed ONLY by the registry — a caller-visible CloseStore
  on a registry facade is a refcount release (the existing per-tick
  close discipline would otherwise LATCH the shared native handle:
  CloseStore is a one-way latch, gascity#3157). The registry closes an
  underlying handle only at process shutdown and when a config reload
  evicts its key — after draining in-flight users, evicting the cache
  entry in the same step so a later lookup reopens fresh.
- **Endpoint-identity dedup where handles can't be shared.** Candidate
  builders that enumerate scope DIRS must collapse scopes resolving to
  the same target before probing: `coordClassStoreCandidates`,
  `workAssignmentStores` (whose unclaim/reassign seen-maps are keyed by
  storeIndex and would otherwise release the same bead once per aliased
  leg — they become ID-keyed across endpoint-identical stores),
  `statusWorkCounts`, the convoy list/by-id paths
  (`resolveOwningStoreDir`/`convoyStoreCandidates` — post-unify a
  multi-store hit across endpoint-identical scopes is ALIASING, not
  ambiguity, and must not hard-fail), the hook federation legs, and
  `resolveBdScopeTarget` probes. The pool-demand pass
  (`build_desired_state.go`) already does ID-level dedup — that is the
  pattern.
- **Remote read-plane prefix scoping.** On a remote-target city, every
  aggregate/list/count surface MUST constrain results to the city's own
  prefix set (`EffectiveHQPrefix` + all rig `EffectivePrefix`). The
  mechanism is concrete, because no prefix filter exists today and
  Counter-based counts cannot be post-filtered: `ListQuery` gains an
  `IDPrefixes` filter; `NativeDoltStore` implements it as a SQL
  `id LIKE '<p>-%'` predicate plus a prefix-aware Count; the bd-CLI leg
  either gains an upstream `bd list --prefix` or remote-target cities
  REQUIRE the native provider; and on a remote-target city every
  `beads.Counter` fast path (e.g. `statusWorkCounts`) routes through the
  prefix-aware count. List surfaces may fold-time post-filter (pagination
  cuts pages in memory after the fetch, so keyset cursors survive). On a
  shared org DB, cross-city read isolation IS prefix filtering; any
  query scoped only by labels/type is a bug post-remote.
- **Routing is metadata, not residence.** Post-unify, work routing is
  carried exclusively by bead metadata/labels (`gc.routed_to`,
  `gc.run_target`, rig-qualified hook labels) — the store boundary no
  longer scopes anything. Audit item for implementation: every SDK-issued
  rig-scoped query verified metadata-scoped; pack-author docs state that
  `work_query` commands on a unified city MUST carry a rig-scoping
  filter.
- **Class residue sweeps stop chasing re-pointed scopes.** Once
  `work.unified` exists, the five class residue sweeps must not source
  rig scopes via scope resolution (they'd re-scan the shared DB — over
  WAN once remote, with deletion authority in a multi-tenant DB). They
  source ONLY the endpoint identities recorded in the class/work marker
  payloads, and retire once each recorded source is verified drained.
  Doctor reports drain state.

## Doctor / observability

`infra-class-migration` doctor check gains a work-topology line:
`work: scope=<scoped|unified> target=<managed|remote> markers=<...>
residue=<n sources undrained>`, same advisory/error semantics as the class
lines (unstatable marker = blocking). On a remote-target city the line
also surfaces remote-auth reachability (the credential preflight's Ping).

## Window-3 / deploy-lineage integration

The b36/integ topology (single infra Dolt scope + graph_store=sqlite) maps
onto this branch as: infra scope beads → the per-class sqlite migrations
(their store-open seams accept any scope root; integration adds the infra
scope root to the migration source list when `.gc/infra/.beads` exists),
work beads stay where they are (scoped), then the city walks the ladder
like any other. No b36 mechanism survives as a parallel implementation.
The b36 config key `[beads] graph_store` is registered in `retiredKeys`
(`internal/config/undecoded.go`) pointing at the replacement
(`[beads.classes.graph] backend="sqlite"` or `[beads] infra="local"`), so
a b36 city.toml loads with a warning instead of a fatal unknown-field
error — and the value is HONORED, not merely ignored: `graph_store =
"sqlite"` folds into the effective graph class backend at load (an
explicit `[beads.classes.graph]` entry wins), because warn-and-ignore
would boot a b36 city graph-blind on bd, a recorded incident class. The
deploy-lineage upgrade runbook states the new-knob setting to add before
removing the retired key.

## Red-team pins (tests that gate shipping)

- Two prefixes mint side-by-side in one DB without collision (via the
  prefix-override slice), on both the bd-CLI and native providers, AND on
  at least one non-create mint surface (`bd quick`/`batch`).
- After importing high-numbered foreign-prefix ids, the next mint under
  that prefix lands strictly above the imported maximum.
- `ImportBeads` export→import round-trip preserves status, all clocks,
  close reason, and comments; a closed bead never appears open
  mid-import; re-import after a newer source close converges the
  destination to closed; a same-second tie converges via the bounded
  `--allow-stale` re-import.
- Import fires no bd hooks / emits no `bead.created` events for migrated
  history (guards upstream drift of the batch write path).
- One-way enforcement on all three surfaces (boot, reload, one-shot
  routing), including the marker-lost/observed-state case — and boot does
  NOT refuse in the marker-present/not-yet-canonicalized self-heal
  window.
- A failed unify blocks boot, and quarantined (`gc.topology_migrating`)
  rows are invisible to hook/claim/ready surfaces until the marker.
- Endpoint-identity collapse: post-unify `gc status` counts, `/v0/convoys`
  listings, and a release/reassign sweep each act exactly once per bead;
  a per-tick CloseStore on a registry facade does not latch the shared
  handle.
- Remote collision pre-probe: a foreign same-prefix bead on the remote —
  older OR newer than ours — aborts before any write.
- shadow=true + scope=unified is a load error; the implied-backend
  reserved-prefix collision is a load error.

## Out of scope (recorded)

- dolt-native push/clone fast path for remote migration.
- Dropping old rig databases after unify (cold backup retention policy).
- Cross-org prefix governance and whole-prefix foreign-bead discovery on
  shared remote DBs.
- Tombstone propagation from legacy databases after re-point (residual
  documented under the copy primitive).
- `gc init` flags that pre-write the topology config (the config file
  itself is the interface; init sugar is a follow-up).
