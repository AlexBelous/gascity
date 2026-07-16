# P0.1 Effect Inventory — Bound-Head Reconciliation Finding & Decomposition

Bead: **ga-f7v2ft.9** "Generate the execution-head reconciler effect inventory"
Branch: `ga-f7v2ft-9-effect-inventory` (worktree `/data/projects/gascity-ga-f7v2ft9-effectinv`)
Bound head: `7378aa936` (frozen pre-G0 reconciler source; candidate manifest
`engdocs/plans/reconciler-redesign/PRE_G0_CANDIDATE_MANIFEST.json`, digest `351f8a2f…`).

## What was done (committed, durable)
- `7e4e63e86` — ported the **reviewed** fail-closed SSA/VTA effect-inventory engine from the
  hardened lineage (`feature/reconciler-g0-hardened-d36a8cca`, tip `f96d81a5a8`) onto the
  bound-head branch, replacing the rejected bootstrap `discover.go` scanner. Added the missing
  `CanonicalRegistry()` combiner (assembles the four catalog partitions + boundary vocabulary —
  neither existed at the hardened tip). Package compiles clean.
- `af4a0b7ed` — **SCRATCH/diagnostic**: fixed the four boundary seeds that don't exist at bound
  head (`pidutil.Signal`/`SignalProcess`, `processgroup.SignalGroup`/`SignalCommand` → keep
  `processgroup.Terminate`/`TerminateCommand`), and temporarily bypassed the raw-process /
  route-hop / target-gate guards to run discovery and surface the work-list.

## The pivotal finding
Running the **first-ever** full-registry (`CanonicalRegistry()`, 75 boundaries) discovery over the
real bound-head `./cmd/gc` closure hard-fails **fail-closed on 94 sites on `darwin/default` alone**
(stops at profile 1 of 5):
- 48 × `unresolved channel operation has open-world provenance compatible with an inventoried boundary`
- 26 × `unresolved effect-compatible dynamic call`
- 20 × `unsupported close operation has unresolved or unsafe channel provenance`

spread across `cmd/gc`(32), `internal/api`(15), `dashboardbff`(10), `runtime/acp`(9), `events`(8),
`beads`(7), `runtime`(6), `session`(5), and ~10 more packages.

Why this is not a one-session port+reconcile:
1. **No discovery-level exception mechanism.** `analysisConfig` is only `{RepoRoot, ModulePath,
   Patterns}`; `discoverProfile` returns a hard error if *any* site is unresolved. The 94 cannot be
   "excepted" — they must be made **resolvable**.
2. **Never run to green anywhere.** The hardened production-scope tests call `discoverProfile` with a
   **single** boundary at a time (`[]BoundaryDefinition{boundary}`), never the full set. Catalog
   partitions were validated only with **synthetic** discovery (`discoveryForRegistry`). Full-boundary
   fail-closed discovery over the real closure has no passing precedent.
3. **Source is frozen.** Bound head can't be refactored to become analyzable; resolution requires
   analyzer/boundary re-tuning (risking the review + correctness) — genuine, uncertain engineering.
4. **Raw-process containment is a redesign goal, not a bound-head fact.** Bound head calls raw
   `syscall.Kill`/`os.Process.Signal`/`os.Process.Kill` **directly and uncontained** in ~15 sites
   (dolt_*, cmd_start_drift, provider_op_process, acp). The hardened `raw_process_guard.go` proves
   containment through typed vehicles that don't exist pre-refactor.

Reproduce:
```
cd /data/projects/gascity-ga-f7v2ft9-effectinv
GOCACHE=$(mktemp -d) go test -vet=off -run '^TestScratchCompileBoundHead$' -v \
  ./internal/reconciletest/effectinventory/
```

## Decomposition (child beads)
- **P0.1a — Boundary/analyzer tuning:** resolve/collapse the 94+ fail-closed discovery sites per
  profile so full-boundary discovery runs clean on the bound head (narrow over-broad wake/dynamic
  boundaries that match non-reconciler channels; prove closed provenance where possible). Uncertain;
  the hardest item. Verify on all 5 profiles.
- **P0.1b — Raw-process guard reframe + fixtures:** reframe `raw_process_guard.go` for the pre-refactor
  bound head (enumerate the ~15 uncontained raw kill/signal sites; no typed-vehicle containment), and
  fix the testdata fixtures (`canonicalroute`, `rawprocess`, `routehops`, `targetgate`) that reference
  nonexistent post-refactor symbols and break lint.
- **P0.1c — Catalog re-authoring + evidence:** re-author `catalog_{store,provider,process,event}_data.go`
  (837 rows) and reconcile `route_hop_evidence.go` + `target_gate_evidence.go` against bound-head
  discovery. Store/event are largely stable; process/provider/wake/cmd-gc need re-derivation.
- **P0.1d — Glue + verify (blocked by a/b/c):** author `cmd/gc/TestReconcilerEffectInventoryOnBoundHead`
  (drives `CompileCanonicalRegistry` pinned to the bound head) and `internal/session/REQUIREMENTS.md`
  SESSION-EFFECT-001/002/003; get all 5 profiles + lint + the bead's verification suite green; remove
  the SCRATCH guard bypasses.

Full working notes: `CONTEXT-LOG-ga-f7v2ft9.md` (git-excluded scratch) in the worktree root.
