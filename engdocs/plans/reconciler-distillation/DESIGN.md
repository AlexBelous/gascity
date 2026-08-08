# Reconciler Distillation — Design

Owner directive (2026-08-07): *"take this mess of files and create the simplest,
correct version of the reconciler. We don't want to reduce quality, but
guaranteed there is a lot of fluff in those commits."*

Evidence base: 7-lens read-only analysis over the live lane
(`rec/ga88-continue`, 199 commits, +67,702/−1,714 vs origin/main), the cold lane
(`feature/reconciler-integrated-20260715`, 255 commits, abandoned), origin/main
legacy, and the ga-f7v2ft bead subtree. Full lens reports:
`/var/tmp/distill-analysis/*.md` (regenerate: workflow run wf_bac03d4b-182).

## 1. Verdict: what the lane actually is

1. **The strangler never strangled.** Nothing was deleted. The legacy god
   function `reconcileSessionBeadsTracedWithNamedDemand` GREW under the lane:
   2,544 → 2,611 lines (file 6,009 → 6,198). The lane's 21k production lines
   are net-new machinery beside an untouched legacy.
2. **The lane is a session-START controller, not a reconciler replacement.**
   Of the legacy's 28 decision sites (enumerable from its TraceSite constants),
   the keyed path owns ~4. Idle-timeout, idle-drain, reset-stall, orphan-close,
   max-session-age, config-drift, circuit-breaker/provider-health, and pool
   desired-state/min-fill are 100% legacy-owned with zero keyed tests.
3. **The keyed path has never run in production.** `daemon.session_reconciler`
   defaults Off; no city.toml in the repo sets it. All confidence is the test
   corpus. Corollary: the shadow/parity tier built to collect production parity
   evidence never collected any — it bought nothing.
4. **The test corpus is the real asset.** 46,595 test LOC ≈ 31% user-visible
   guarantees (17 named behaviors, A1–A17), 46% mechanism invariants,
   23% migration scaffolding.
5. **The cold lane is 99% superseded.** Salvage two documents
   (ACCEPTANCE_MATRIX.md 1,295 ln; EXTERNAL-REVIEW-fable-10axis.md 381 ln);
   ignore the rest (41.7k-LOC nudge "local authority", 28.3k-LOC AST
   effect-inventory analyzer, 9.3k configtxn, shadow-parity universe).
6. **Only 7 of 25 open beads are reconciler behavior.** The rest serve
   apparatus being deleted or are mis-parented config/API/packs work.

## 2. End-state architecture

One keyed reconciler. No modes, no fallback, no shadow, no parity, no
comparison tooling, no contract anchors.

- **Keep the four keyed controllers** (session-start, nudge-key,
  wait-dependency, pool-allocation) and their workqueue shape. Extract their
  four copies of stop/drain/overflow/panic plumbing into one shared controller
  skeleton (each currently re-implements ~700 LOC of it).
- **Replace the legacy god function with a detector sweep.** The patrol tick
  becomes a thin periodic scan that only *detects* conditions (idle deadline
  passed, orphan, stall, drift, age, pool under min) and enqueues exact keys
  into the controllers. All decisions and effects live in per-key reconcile
  functions. Observation fans out; action stays keyed and serial. This ports
  the missing 24 decision sites without recreating a 2,600-line function.
- **Fresh-liveness and unattended-stop become hard provider requirements** on
  the keyed management path (today: runtime type-asserts with legacy yield).
- **Keep**: exact-key/no-fleet-enumeration invariant (A1), exactly-once start
  (A2), canonical identity (A3), suspend/reset/kill parity (A4, .103, .105),
  proven-absence drain-ack (A5), attached-user safety (A6), fenced nudge
  delivery (A7/A8), pool selection policy (A9), wait-dependency wake (A10/11),
  refuse-on-ambiguity zero-effect style (A12), `gc trace status` (A13),
  clean shutdown (A14), config-publication safety (A15), provider swap +
  keyed-only latency harness (A16/17). Substrate hardening (internal/runtime
  observation, internal/beads conditional writes) survives unconditionally.
- **Out of scope, flagged separately**: trace WAL internals (3,785 LOC,
  main-owned, operator-documented — a future simplification bead, not this
  program); build_desired_state.go (5,260 LOC, shared) gets its own pass later.

## 3. The ledger (production LOC)

| Action | What | ~LOC |
| --- | --- | --- |
| DELETE NOW | contract anchor (process ceremony, guarded a plan directory in no tree, never enforced) | −585 |
| KEEP UNTIL EVIDENCE, THEN DELETE AT CUTOVER | **shadow/side-by-side parity machinery + perf-compare CLI + nudge_shadow gate** — owner directive D4: these are the semantic-parity verification instruments; they go only after the WE evidence campaign (below) | −7,900 (deferred) |
| DELETE AT CUTOVER | rollout tri-state, legacy-fallback lattice, Entered flags, drain-ack rollback, ownership latch, nudge coexistence | −1,500 prod, −2,900 test |
| DELETE AT CUTOVER | legacy reconciler cluster + wrapper chain + legacy-only trace sites | −13,700 prod, −22,000 test (triaged, not blind) |
| FOLD | six lease families → one, 11-param admit → struct, repeated preambles, socket-admission triplication | −1,300 prod |
| ADD (the only new code) | detector sweep + gap handlers (idle/orphan/stall/drift/age/circuit/pool-fill) + ported behavior tests | +1,500–2,500 prod |
| RENAME | live code misnamed "shadow": `poolMembershipShadow`, `poolAllocationShadowPolicy`, `sessionWaitShadowAdmission`, status planners | 0 |

End state: the reconciler subsystem is **smaller than main's legacy is today**,
with more proven behavior. Test corpus lands at ~24–30k (from 46.6k + 22k
legacy) with zero named guarantees lost.

## 4. Waves

- **W0 (done):** `.102` landed (`6296db9b68`).
- **WA — extract separable value.** Rebase-extract and PR to main,
  independent of any reconciler decision: internal/runtime observation
  (~1,400), internal/beads conditional-writes (~700), their 6,199 test LOC.
  Check the minimal-20260726 worktree delta (3 commits, 16 files) for the
  retained store-reuse perf commits before discarding that lane.
- **WB — delete dead + apparatus + bead triage.** The two DELETE-NOW rows;
  close/re-parent the 18 non-behavior beads; rewrite epic AC to this design.
  No behavior change; gates = full suites green.
- **WC — rebase the lane onto current main** (65 behind, 53 overlapping
  files). Do it before new behavior, not after.
- **WD — close the gap + finish parity.** Detector sweep + the 24 site
  handlers, porting behavior tests from the legacy corpus (triage: contract
  tests re-pointed, wiring tests die). Plus `.103`, `.105`, `.78.6`,
  `ga-adnxji` (proven impl at 3a96ef4e8, archaeology not replay).
- **WE — evidence campaign, then cutover commit.** Precondition (owner
  directive D4): run a real side-by-side campaign — a city on
  `daemon.session_reconciler = auto` with the shadow observers and detail
  traces armed, over the WD-completed behavior set — and archive the parity
  results; run a final `gc perf reconciler-compare` A/B and archive the
  report in engdocs. Only with that evidence recorded: keyed becomes the
  only owner; delete the rollout lattice, the legacy cluster, and the
  shadow/perf machinery (whose second arm no longer exists); rewrite the
  four "beats the 30s debounce" assertions as absolute latency budgets;
  deprecation warning (not silent removal) for `daemon.session_reconciler`,
  per the repo's own precedent.
- **WF — fold + rename + split.** Lease unification, shared controller
  skeleton, split the 964-line keyed reconcile function, shadow-name renames.

Every wave gates on: focused RED/GREEN for changed behavior, one real
schema-v59 managed-Dolt isolated-tmux journey where lifecycle is touched,
`make test-fast-parallel`, `go vet`, pre-commit; wave-end
`make test-local-full-parallel`. Fable council reviews WD and WE before merge.

## 5. Bead triage (execute in WB)

- **Keep (behavior):** .103, .105, .78.6 (re-parent to epic root), ga-adnxji.
- **Close as done:** .102 (done), .79 (Gas City side complete per its own
  note; move .79.1.x subtree to a beads-repo epic).
- **Close as moot (apparatus):** .33, .43, .44, .46, .28 + .28.4.x + .28.5,
  .29, .41 — the nudge-*authorization* kernel and shadow/canary/attestation
  tier. (Keyed nudge *delivery* already exists and stays — A7/A8.)
- **Rescope to a recorded number:** .42, .78 (drop the SLO/report programs;
  keep the keyed-only latency harness as the acceptance instrument).
- **Re-parent off-mission:** .32 subtree + ga-yb96ch → config-transaction
  epic; .34 → API epic; .80 → beads epic; (.35 already standalone).

## 6. Traps (verified, do not step on)

1. **"Shadow" names live code.** `planSessionLifecycleStatus`,
   `planSessionLifecycleStartSelection`, `poolMembershipShadow`,
   `poolAllocationShadowPolicy`, `sessionWaitShadowAdmission` are the real
   keyed path. `test/integration/session_wait_dependency_shadow_journey_test.go`
   holds real keyed journeys. Never delete by filename; delete by caller graph.
2. **Strip scaffolding by compilation, not by grep.** Delete `rollout.Mode`
   from production signatures first; the tests that fail to compile are the
   scaffolding. ~20 real pool tests contain "FallsBack" and must survive.
3. **Predicate divergence:** session_start_reconcile.go:299 uses
   `reason != Eligible` where every sibling uses `supported()`. Resolve
   (deliberate vs drift) before folding lease families.
4. **Provider capabilities are real branches.** subprocess/acp/k8s/exec/ssh
   implement neither FreshLivenessObserver nor UnattendedSessionStopper —
   keyed-only turns their suspend/stop into hard errors (see decision D2).
5. **Durable fencing already exists**: `instance_token` lives in bead
   metadata and stops are token-bound — the cold lane's `intent_generation`
   gap is closed; do not port its 471-LOC status writer. Verify the bare-string
   workqueue key against the split-store layout during WC (single session
   store per city expected).
6. **safeTick panic-swallowing is load-bearing** (incident #663). Preserve.

## 7. Owner decisions (resolved 2026-08-07)

- **D1: DROP the nudge-authorization kernel.** All 9 beads close. Nudge
  delivery keeps the local-authority trust model every other gc verb uses.
  A future multi-tenant threat model is a security epic, not reconciler work.
- **D2: HARD provider-capability requirement.** Keyed lifecycle management
  requires FreshLivenessObserver + UnattendedSessionStopper; incapable
  providers get a typed refusal. No degraded mode, no matrix.
- **D3: YES — land WA substrate PRs to origin/main now.**
- **D4 (2026-08-08, supersedes the WB apparatus deletions): KEEP the
  shadow/side-by-side parity machinery and the perf-compare harness until
  cutover.** Owner verbatim: *"we need the shadow and side-by-side to verify
  semantic parity we can't delete it"*; the perf harness measured the ~50%
  latency reduction. WB's shadow/perf deletion commits were reverted in
  `9044a47a3f`; only the contract-anchor deletion stands. WE is now gated on
  an actual side-by-side evidence campaign (§4 WE).
