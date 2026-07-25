# Replacing window-3's bespoke storage split with `feat/infra-class-sqlite-stores`

Status: Accepted. Companion to `engdocs/design/beads-work-topology.md`.
Synthesizes the two research passes (tasks a1d52cf339f22f549 window-3 mapping,
a7f82370d6116fa31 gasworks hosting) + the shipped slice work. End state the owner
asked for: **our branch + config changes + the non-storage window-3 PRs, with mc
running local per-class infra sqlite DBs and (eventually) a shared task DB on
gasworks.**

## 1. What window-3 actually deployed (not what the archives said)

mc runs the **infra-store-split** lineage (`integ/sqlite-graph-store-onto-fork` →
`graphv2-session-key-capture 0a22b0dd4` → likely `rolling-24h-deploy 3e35d3d65`,
base main `6b0eb0d6b`) — NOT b36-proper (`graph_store=sqlite`). Model:

- ONE combined `.gc/infra` embedded-sqlite scope, `issue_prefix gcg`, holding ALL
  FIVE infra classes (graph+sessions+messaging+orders+nudges). Live file
  `.gc/infra/.beads/beads.sqlite` (~508 MB). Legacy `.gc/beads.sqlite` (~65 MB) is
  stale.
- Work beads stay on **Dolt** (city `mc` DB, `127.0.0.1:3307`, prefix `mc`).
- Activation = scope presence (`cityHasInfraStore`/`infraScopeRoot`), not a config
  key — though mc's `city.toml` still carries a vestigial `graph_store="sqlite"`.
- **No rigs registered.** mc is a single-scope (HQ-only) city.

Our branch = FIVE separate per-class stores under `.gc/store/` with FIVE prefixes
(graph=gcg, sessions=gcs, orders=gco, messaging=gcm, nudges=gcn); work stays on
Dolt until the topology axes are engaged.

## 2. Supersession — window-3 storage code our branch replaces (drop it)

Our branch was largely PORTED from window-3's own store engine and then hardened,
so the replacement is mostly deletion of superseded commits, not a rewrite:

- Core `internal/beads/sqlite_store*` engine — ported from
  `integ/cli-class-store-event-emission@2c74f8747`, plus two fixes window-3 lacks:
  dep-edge PK `(issue,depends_on)` with type-update, and the modernc `_pragma=`
  DSN busy-timeout (window-3's `?_busy_timeout=` was silently ignored). Also adds
  `ConditionalWriter` (closes the N06 silent-degradation window-3 never had),
  `CreateWithForeignID`, `DeleteBatch`.
- The whole GRAPH-READ-GAP-ANALYSIS (39 gaps vs both integ and b36) is closed on
  our branch — reconciler assigned-work/liveness, crash-recovery in_progress union,
  gc-bd read federation, order wisp-root evidence, sling GraphStore threading, API
  list/ready/status graph legs, CAS fencing, convoy lanes. Supersedes window-3's
  split-city reconciler + in-process bd mutation/read commits.
- b36 `graph_store` key: our `retiredKeys` fold HONORS it (→
  `[beads.classes.graph] backend="sqlite"`), not warn-and-ignore.

## 3. What our branch is MISSING that window-3 has (port or accept)

- **S7 (`.gc/infra` migration adapter) — BLOCKER, being built.** Our five boot
  migrations read only the Dolt work stores; they have ZERO `.gc/infra` awareness.
  Deploying our binary to mc as-is orphans mc's gcg infra beads. S7 teaches the
  migrations to read the combined `.gc/infra` scope as a source and reclassify
  gcg-prefixed beads by `coordclass.Classify` into the five class stores.
  Read-only on `.gc/infra` (reversibility).
- **Usage-tick session reads (D#3) — audited in S7.** Window-3's usage-tick fixes
  (`94297c31f`+`3c7debd26`+`746328a11`) route usage session reads through their
  session seam. Our branch never touches `usage_compute.go`; the sessions flip can
  re-break the 07-24 "session bead not found" incident unless usage reads route
  through `resolveSessionStore`. S7 audits/fixes this.
- **`gc bd mol current`/`mol progress` cross-store federation** (`5a09b4cdf`) — a
  low-severity operator-CLI read gap our branch defers (GRAPH-GAP-FIX-PLAN G05/24/27).
  Long-tail bead, not a flip blocker.
- **BFF `fetchRunGraph` loopback (b36 `db9d6302c`)** — our branch relies on
  `bead.*` emission instead (closes the reported symptom) but is not resilient to a
  rotated events.jsonl / reaped wisp the way the read is. Belt-and-braces; port only
  if mc's cockpit run views prove load-bearing. Long-tail.

## 4. Non-storage window-3 PRs to keep (end state = branch + config + THESE)

Filter each with `git cherry origin/main <sha>` before opening — several have main
PR numbers and may already be upstreamed by patch-id:

- Usage/cockpit: `ec7f63028`/`9a06367e6`/`3e35d3d65` (session resume-key, rolling
  24h aggregate, cockpit dials). The 3 usage-tick commits are store-coupled → port
  with the S7 session audit.
- Gate env: `db2016aa1` (GC_HOME), `0d9ac345d` (EX_TEMPFAIL→GateError), `2024d5d96`
  (GH_TOKEN into order-exec env).
- Reliability: `2fd05c48e` (hook EPIPE), `ba508b7f5` (don't burn a ralph attempt on
  gate exec failure), `1ff77e704` (configurable cond-check timeout), `e14fb8d1a`
  (bound store-health scan so `gc status` can't stall), `66128d8f6` (clear Codex
  hook-review dialog).
- Front-door/auth/json: `2d4bc407c`+`603305cba` (empty init template),
  `0de8cc719`+`73883b009`+`91da1f380` (citywriteauth crucible-v2),
  `63400ca77`+`2ce36f564` (json passthrough / `gc ready --json`).

## 5. Config activation on mc

```toml
[beads]
infra = "local"     # aggregate → all 5 classes to sqlite (Slice 1)
# graph_store="sqlite" vestige auto-folds via retiredKeys; replace it with the
# canonical [beads.classes.graph] backend="sqlite" or drop it (the fold warns).
```

Work axis, later, only for the shared task DB:
```toml
[beads.work]
scope  = "unified"                       # no-op for mc (no rigs) — single work DB
target = "dolt://<gasworks-gateway>:3306/bd_<mc-project>"   # the remote hop
```
Config alone is NOT sufficient — the data needs S7.

## 6. The gasworks remote task DB — feasibility and blockers

Addressing is COMPATIBLE: our `dolt://host:port/db` + credential-command already
projects the hosted-gateway shape (MySQL-wire `:3306` over TLS, EIA token-as-user,
`bd_<project>` routing schema; `targetCarriesHostedGatewayTLS` emits the TLS +
credential-command env). No wire adapter needed. But the LIVE remote flip is blocked
on four prerequisites, two of them out of our hands:

1. **bd binary** = main + `workspace-prefix-mint` (our S0 ✓) + gateway
   credential-command (`feat/dolt-credential-command` `ApplyGatewayCredential`) +
   events-journal (mc's observer depends on it). A merged bd build. mc's current bd
   has none of the first three.
2. **S8 (gascity gateway-aware `allowed_prefixes`) — buildable, being built.** On a
   hosted gateway, config is server-authoritative/read-only, so our
   `bd config add-to-set allowed_prefixes` no-ops. Fix: on a `dolt://` gateway
   target, skip add-to-set, verify prefixes via `bd config get`, fail loudly if the
   provisioner didn't set them.
3. **Hosted provision (ops, out-of-tree):** beads-web/beads-provisioner must create
   `bd_<mc>` with `issue_prefix=mc` and `allowed_prefixes={mc, <every rig prefix>}`.
   Multi-`allowed_prefixes` support is UNVERIFIED (corp-public apps/beads +
   beads-team-server). Owner: those maintainers.
4. **Controller EIA/STS env + tailnet reachability** to the gateway `:3306`.

mc holds NO hosted identity today (plaintext local Dolt). ⇒ the remote hop CANNOT
complete autonomously tonight; delivered staged + S8 + this runbook.

## 7. Cutover sequence (mc)

Preconditions: capable+credential+events-journal bd built; S7 rehearsed on a
VACUUM INTO snapshot of mc's real `.gc/infra`; rollback verified.

Phase 1 — infra to our five-class model (local, reversible):
1. Snapshot mc `.gc/infra` via `VACUUM INTO` (sanctioned; never hot-cp) → rehearse
   S7 against it in an isolated staging city; confirm every gcg bead reclassifies,
   counts match, reads route.
2. Stage the merged bd; deploy alongside (do not replace mc's bd until flip).
3. Add `[beads] infra="local"`; canonicalize the `graph_store` vestige.
4. Bounce mc via the consolidated supervisor. Boot runs the five class migrations
   sourcing `.gc/infra` (S7), writing `.gc/store/*.migrated`. `.gc/infra` untouched
   (cold backup).
5. Verify: `gc status`, `gc doctor` (infra-class-migration line), a live pipeline
   check (dispatch + a merge). Rollback = revert binary+config; `.gc/infra` intact.

Phase 2 — work task DB → gasworks (gated on §6 ops prereqs):
6. Provision `bd_<mc>` server-side with the full prefix set; wire controller EIA env.
7. Set `[beads.work] scope="unified" target="dolt://…"`. Bounce. Boot runs
   ensureWorkUnified (trivial — no rigs) then ensureWorkRemote (copy mc work Dolt →
   gasworks, one-way). Managed-local Dolt kept alive until residue drains.
8. Verify remote reachability (doctor), pipeline, and that new work mints under `mc`
   into the org DB.

## 8. Risk posture

mc is the fleet brain; its restart runs through a consolidated 6-city supervisor on
a host also running the live fleet + many `gc-int-env-*` harnesses. Therefore:
- All staging isolated (unique socket/port/dir); never `tmux kill-server`.
- Phase 1 executed live only if the real-`.gc/infra`-snapshot rehearsal is flawless
  AND reversible (binary+config revert, `.gc/infra` read-only cold backup).
- Phase 2 (one-way remote) NOT attempted until the §6 ops prerequisites are met and
  confirmed; delivered staged with the prereqs named.
- Long-tail findings → fix-up commits, not blockers.
