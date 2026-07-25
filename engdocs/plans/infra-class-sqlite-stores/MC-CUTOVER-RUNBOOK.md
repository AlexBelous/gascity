# maintainer-city cutover runbook — infra → five-class sqlite (Phase 1), task DB → gasworks (Phase 2)

Status: Accepted. Supervised procedure. Companion to WINDOW3-REPLACEMENT-PLAN.md + engdocs/design/beads-work-topology.md.
Governs the SUPERVISED cutover of maintainer-city (mc, the fleet brain) onto
`feat/infra-class-sqlite-stores`. Phase 1 (local infra, reversible) is ready to
execute in a maintenance window. Phase 2 (remote task DB on gasworks) is gated on
external ops prerequisites (§Phase 2).

**Do NOT run this unattended.** mc restart goes through a consolidated 6-city
supervisor; an unattended revert-failure is a fleet outage. Execute with a human
watching, ready to abort.

## Why this is safe (evidence)
- Correctness under mixed prefixes: every automatic path (reconciler/dispatch List
  over the routed store, finalize closed-step votes, resolveSessionStore.Get, mail
  provider, graphStoreForID residence-probe) reads the OWNING class store directly,
  prefix-agnostic — a reclassified mc-/ga-/gcg- bead is found. (analysis
  a3d58981f21be87d8)
- Reversibility: S7 opens `.gc/infra` TRUE read-only (file:?mode=ro — proven
  byte-identical incl -wal); it is never mutated. Rollback = redeploy the window-3
  binary + revert config; `.gc/infra` + Dolt work store are intact.
- Self-guarding: the migration aborts BEFORE any marker on failure (boot-blocking),
  and the G1 census guard (below) aborts before any marker if `.gc/infra` holds a
  ClassWork bead — so the worst case is a safe boot-block, never a silent orphan.
- Phase 1 needs no bd rebuild: our gc reads `.gc/infra` via its own embedded sqlite
  engine; work stays on Dolt via mc's existing events-journal bd (schema_version 1,
  ≥ our bdMinVersion 1.0.4). No workspace-prefix-mint / credential-command bd needed
  until Phase 2.

## Pre-window preparation (no mc contact)
1. Deploy artifacts staged: our gc binary built from HEAD of
   feat/infra-class-sqlite-stores; keep mc's current bd.
2. Config change prepared (not applied): add `[beads] infra = "local"` to
   mc/city.toml; canonicalize the vestigial `graph_store = "sqlite"` to
   `[beads.classes.graph] backend = "sqlite"` (the retired-key fold warns to do
   this).
3. Rollback bundle ready: the exact prior gc release path + the prior city.toml,
   scripted as a one-command revert.

## Phase 1 — the maintenance window (mc STOPPED)
Order matters; each step has an abort.

1. **Quiesce mc only.** Stop mc's controller via the supervisor's per-city stop
   (NOT the unit; the other 5 cities stay up). Confirm `.gc/infra/.beads/beads.sqlite`
   has no active writer (WAL settled).
2. **Snapshot the stopped store.** `sqlite3 -readonly <.gc/infra/.beads/beads.sqlite>
   "VACUUM INTO '<staging>/infra-snap.sqlite'"` (safe now that mc is stopped). Record
   the source sha256.
3. **DRY-RUN GATE (decisive).** In an isolated staging city (unique socket/port/dir)
   whose `.gc/infra` is the snapshot, run the migration and assert:
   - **G1 zero-ClassWork census GREEN** — coordclass.Classify over all beads yields
     0 ClassWork (any ClassWork hit: confirm the same id is in the Dolt work store,
     else it is an orphan → **ABORT the cutover**, restart mc on the old binary).
   - Coverage: graph+sessions+messaging+orders+nudges imported + intended retention
     drops == total scope beads.
   - Mixed-prefix round-trip on real mc-/ga-/gcg- ids (session, message, convoy/gate)
     resolve via their routed stores + gc bd show federation.
   - Finalize: every in-flight molecule's closed steps/gates crossed to graph.db.
   - gcg id-floor lifted above the global max; a fresh graph mint doesn't collide.
   - Snapshot `.gc/infra` sha256 unchanged after the dry-run (read-only holds).
   If any assertion fails → ABORT, restart mc on the old binary, investigate. No
   live change has been made.
4. **Apply (only if the gate is green).** Deploy our gc binary; apply the config
   change. Start mc's controller. Boot runs the five class migrations sourcing
   `.gc/infra` (S7), writing `.gc/store/*.migrated`. `.gc/infra` untouched.
5. **Verify live.** `gc status` (counts sane, graph gcg beads counted), `gc doctor`
   (infra-class-migration line clean, all five markers present), a live pipeline
   check (dispatch a bead + confirm a merge), the observer/usage tick emits (the
   usage_compute session-read fix). Watch for ~10 min.
6. **Abort/rollback path (any step 4-5 failure):** stop mc, redeploy the window-3
   binary, revert city.toml, restart. `.gc/infra` + Dolt intact → mc resumes on the
   old model. File the failure for follow-up.

Outcome: mc running on our five per-class local sqlite infra stores; work beads
still on local Dolt (unified is a no-op — mc has no rigs). This is the "local infra
dbs + shared-local task DB, fully functional" end state minus the remote hop.

## Phase 2 — task DB → gasworks (gated; do NOT start until all prereqs met)
Blocked on external prerequisites (research a7f82370d6116fa31):
1. bd binary = main + workspace-prefix-mint (S0) + gateway credential-command
   (feat/dolt-credential-command) + events-journal (mc's observer). A merged build.
2. Hosted provision (ops, out-of-tree): beads-web/beads-provisioner creates
   `bd_<mc>` with issue_prefix=mc AND allowed_prefixes={mc, <every rig prefix>};
   confirm the provisioner accepts multi-allowed_prefixes (UNVERIFIED).
3. Controller EIA/STS env (BEADS_DOLT_CREDENTIAL_COMMAND + EIA_*/STS_*/
   ORCHESTRATOR_KEY_FILE) + tailnet reachability to the gateway :3306.
When met: set `[beads.work] scope="unified" target="dolt://<gateway>:3306/bd_<mc>"`,
bounce. Boot runs ensureWorkUnified (trivial) then ensureWorkRemote — which registers
the prefix set (HQ + every rig) into the org DB's `allowed_prefixes` via
`bd config add-to-set` (this works against a hosted gateway: config writes are
gated only by MySQL grants and the controller holds a whole-schema rw credential
from its `beads:write` EIA), then re-reads `allowed_prefixes` as the authoritative
check and boot-blocks BEFORE the marker if a required prefix is still absent (the
controller credential lacks org-DB config write — a read-only/`hard_blocked` EIA —
or the operator must provision the prefixes server-side). Managed-local Dolt is
kept alive until residue drains; remote is one-way. Verify remote reachability
(doctor), pipeline, and new-work mints under mc into the org DB.

## Long-tail fixups (queued, non-blocking — the user asked for these as commits)
- G2/G3/G4: give findBeadAcrossStores + /v0/bead/{id} candidate builder + the gc bd
  write-guard the same st.Get(id) residence-probe fallback the graph-by-id routers
  already use, so by-id INSPECTION of mc-/ga- reclassified beads resolves (currently
  cosmetic 404 on those paths; automatic loop unaffected). (Being folded in with the
  G1 guard.)
- gc bd mol current/progress cross-store federation (window-3 5a09b4cdf) — deferred
  operator-CLI read.
- BFF fetchRunGraph belt-and-braces (only if the cockpit run views prove
  event-log-fragile on mc).
- Non-storage window-3 PRs to keep (see WINDOW3-REPLACEMENT-PLAN §4), filtered by
  git cherry.
