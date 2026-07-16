# P0.2 — Semantic Ratification and Historical Dispositions

| Field | Value |
|---|---|
| Status | Ratified — pending two-maintainer architecture approval (P0.2 acceptance) |
| Bead | `ga-f7v2ft.11` (P0.2); depends on `ga-f7v2ft.9` (P0.1 effect inventory) |
| Scope | Resolve the proposal's open forks and record dispositions before code encodes them. **No runtime behavior change.** |
| Bound head | `7378aa936` (frozen pre-G0 reconciler source; candidate digest `351f8a2f…`) |
| Controlling sources | [IMPLEMENTATION_PLAN.md](IMPLEMENTATION_PLAN.md) §P0.2 (recommended decisions); [ACCEPTANCE_MATRIX.md](ACCEPTANCE_MATRIX.md) (`RC-*` contracts); [PLAN.md](../../../internal/session/PLAN.md) (extraction order); [REQUIREMENTS.md](../../../internal/session/REQUIREMENTS.md) (`SESSION-*` ledger); [session-store-fences.md](../../design/session-store-fences.md) (store fences) |
| Rollback | Revert this document and the ratification blocks in `PLAN.md`, `REQUIREMENTS.md`, and `session-store-fences.md` before implementation begins. |

## 0. Purpose and method

This is the P0.2 deliverable: the accepted successor ratification/ownership
record that IMPLEMENTATION_PLAN.md §P0.2 names. It resolves the older
[PROPOSAL.md](PROPOSAL.md) open forks so later phases encode a settled contract,
and it gives every active session-plan step, accepted store-fence claim, and
historical proposal element one unambiguous disposition. It changes no
reconcile ownership; its rollback is documentation-only.

The inventory input is P0.1 ([P0_1_EFFECT_INVENTORY_BOUND_HEAD_FINDING.md](P0_1_EFFECT_INVENTORY_BOUND_HEAD_FINDING.md)),
which bound the execution head at `7378aa936` and found that full-boundary
effect discovery does not yet run green there — so P0.1 is decomposed (P0.1a–d)
and no cutover is authorized by this ratification.

**Disposition vocabulary** (applied uniformly below):

- **preserved** — remains controlling for current code, unchanged.
- **completed** — already landed; the work is done and its evidence exists.
- **superseded** — replaced by a named successor; do not implement the original.
- **renamed** — same intent, new identifier/home; lineage only.
- **blocked → replacement** — cannot proceed as written; the exact replacement
  task is named.

**Evidence convention.** Decisions cite `SESSION-*` rows in `REQUIREMENTS.md` and
`RC-*` rows in `ACCEPTANCE_MATRIX.md` by identifier. Review verdicts cite the two
independent 10-axis reviews ([REVIEW-fable-10axis-2026-07-12.md](REVIEW-fable-10axis-2026-07-12.md),
[EXTERNAL-REVIEW-fable-10axis.md](EXTERNAL-REVIEW-fable-10axis.md)). Where a
review did **not** adjudicate a decision, that is stated plainly rather than
implied — the decision then rests on the plan and proposal record, not on a
review it never received.

## 1. Ratified semantic decisions (D1–D11)

The recommended decisions in IMPLEMENTATION_PLAN.md §P0.2 are ratified as
written. Each is bound to its cited evidence and to an honest review verdict.

| # | Ratified decision | Cited evidence (`REQUIREMENTS.md` / `ACCEPTANCE_MATRIX.md`) | Independent-review verdict |
|---|---|---|---|
| **D1** | `internal/session` lifecycle vocabulary is canonical; the proposal's parallel 13-state enum is **not** added. | `SESSION-LIFE-008`; `RC-OBS-001` ("no parallel boolean/tri-state taxonomy"); `RC-STATE-002`; `REQUIREMENTS.md` "Canonical Vocabulary". | Consistent with the reviews' typed-Unknown treatment; not itself contested. |
| **D2** | API/CLI create synchronously commits durable intent before provider execution. `--no-attach` may return accepted after that commit; ordinary create preserves the ready-then-attach milestone. | `RC-CLI-006` (milestone table); `SESSION-START-001`; `RC-STATE-001`; `RC-CLI-007` (`--json` requires `--no-attach`). | Resolves PROPOSAL §10 Fork 2; consistent with the "durable idempotent CLI op" hardening. |
| **D3** | `Unknown` never quarantines and never authorizes destruction. | `RC-OBS-003`; `RC-OBS-005` (live-process veto); `RC-OBS-002`; `RC-PROC-002`; `SESSION-RECON-006` (fails open); `SESSION-RUNTIME-001`. | North-star "doubt is `Unknown`, never `Dead`"; reviews treat omission as a fact to converge, never a command — agreed. |
| **D4** | Corroborated death uses an immediate targeted confirmation after the first authoritative-list absence; it does **not** wait for a second 30-second patrol. | Corroboration model: `RC-OBS-002`, `RC-OBS-005`, `RC-OBS-003`. The immediate-confirm **timing** has no prior row → ratified here as `SESSION-RATIFY-004`. | **Not adjudicated by the 10-axis reviews.** Rests on PROPOSAL §10 Fork 5 and the §8 "corroborated-Dead witness" hardening. |
| **D5** | Quarantine remains durable, config-driven, generation-scoped, TTL-bound, and operator-resettable; transient store/provider errors never accrue it. | `SESSION-RECON-011`, `SESSION-RECON-010`, `SESSION-LIFE-003` (`quarantined_until` TTL); `RC-AUTH-002`, `RC-CLI-005`, `RC-START-004`, `SESSION-RECON-006` (transient errors spend no quarantine budget). | Reviews **prescribed** exactly this (durable holds clear only via TTL or operator reset); reverses the in-memory OTP stance deliberately (Fork 3). |
| **D6** | Single-controller-per-city is mandatory until the HA phase's leases and provider fencing are enabled. | `RC-MIG-001` (leases/epochs are Phase 11 only; live kernel lock, no automatic takeover); `RC-QUEUE-002`; `RC-CLI-002`; `RC-CERT-001`. | Cleanest "agreed": EXTERNAL confirms pre-HA dual-controller refusal (P0.11A) as already addressed; open concern is enforcement completeness (store-anchored claim vs host flock). |
| **D7** | The current trace taxonomy remains supported through migration and for at least one release after final cutover. | Additive/N-1 stability: `RC-EVENT-004`, `RC-EVENT-001`, `RC-MIG-002`. The post-cutover retention **window** has no prior row → ratified here as `SESSION-RATIFY-007`. | Not separately adjudicated; the additive-taxonomy discipline it depends on is review-endorsed. |
| **D8** | "Exactly once" is unavailable for provider effects without provider-side command deduplication. | `RC-NUDGE-002` (production tmux has no command-ID dedup → `delivery_unknown`); `RC-EVENT-002`; `RC-CLOSE-003` (needs a separate durable outbox); `RC-TMUX-003`; `RC-CLI-005`; `RC-CERT-001`. | **Not adjudicated by the 10-axis reviews.** Rests on PROPOSAL §4 (IntentMarker: bounded at-least-once, never exactly-once) and the §12 outbox hardening. |
| **D9** | Local single-tenant operation treats raw command-store write access as full session-control authority. Hosted/multi-tenant operation requires trusted requester provenance, claim-time authorization, and credential/namespace separation. | `RC-AUTH-001` (`store_writer_is_controller`; hosted must refuse); `RC-AUTH-002` (claim-time re-validation); `RC-AUTH-003` (namespace separation). | Coverage-gap critic found the plan lacked any authz model (HIGH/security) and **prescribed this exact trust statement**; reviewer-prescribed, not pre-existing. |
| **D10** | Every tunable has a conservative small-city default; a zero-config install must pass the certified entropy and latency/resource profile. | `RC-PERF-002` (zero-config profile fixes defaults, passes entropy suite, meets envelope); `RC-CERT-003`; `RC-ENTROPY-001/002`. | Reviews raised knob-sprawl concern and **prescribed** the small-city-default + zero-config-entropy + SLO gate now ratified. |
| **D11** | The active `PLAN.md` extraction order and accepted `session-store-fences.md` remain controlling for current code until this plan is approved and the conditional-write capability actually lands. Ratification maps every active step/bead into this plan, preserves characterization-first discipline, and **amends** — never silently contradicts — the "no reconciler rewrite / no CAS" non-goals. | `PLAN.md` header ("`PLAN.md` owns the extraction sequence"); conditional-write target `RC-STATE-003`; `RC-STATE-002`; approval gate `RC-GATE-001`. Document-control statement has no prior row → ratified here as `SESSION-RATIFY-011`. | See §4 (non-goal amendments) and §6 (store fences). |

## 2. Open-fork resolutions (PROPOSAL §10)

PROPOSAL §10 is the proposal's own list of open decision forks; it already
states "this list no longer authorizes an implementation choice." Each fork is
resolved here by the decision named.

| Fork (PROPOSAL §10) | Resolution | Decision |
|---|---|---|
| 1 — Rev spike: fund a per-bead-revision column → true CAS? | **No.** No general CAS as a refactor precondition. A future narrow conditional primitive (the `ReleaseIfCurrent` precedent; `RC-STATE-003`) may land under its own bead; multi-writer clobber stays loud-but-possible until then. | D11, §4, §6 |
| 2 — Spec ownership: synchronous create vs async reconciler-mediated? | Keep API/CLI synchronous create as the one shared `gc.spec.*` writer (create-once), committing durable intent before provider execution. | D2 |
| 3 — Durable quarantine reverses the in-memory OTP-restart stance? | **Explicit reversal.** Quarantine is durable (`quarantined_until` TTL + reset generation), not in-memory. | D5 |
| 4 — Multi-replica: ledger leader lease, or single-controller invariant? | **Single-controller-per-city is the operational invariant** until Phase 11 introduces leases/epochs and provider fencing. | D6 |
| 5 — Corroboration latency: accept ~60 s teardown, or add an immediate confirm probe? | **Add the immediate per-key confirm probe** on first absence; do not wait a second patrol cycle. | D4 |
| 6 — S-series governance: confirm supersede of S19 stages 2–7 while keeping numbering + pin? | **Confirmed.** The redesign supersedes the *content* of S19 stages 2–7 while preserving S19's numbering and parity pin. | D11, §3 |

## 3. Historical proposal-element dispositions

Covers the proposal elements that a later phase might otherwise re-open. The
one-line north star and safety kernel are **preserved**; the fleet-global
scheduler and its first-draft mechanisms are **superseded** by the plan and
matrix (see [README.md](README.md)).

| Historical element (PROPOSAL) | Disposition | Successor / note |
|---|---|---|
| Fleet-global serial scheduler | superseded | Revisioned projections + stable keyed queues (`RC-QUEUE-001..005`). |
| Single opaque incarnation | superseded | Provider-owned box + launch identities (`RC-ID-001..004`). |
| Provider-timeout assumptions | superseded | Provider-native mutation entry; caller timeout never releases ownership (`RC-TMUX-003`, `RC-CLI-010`). |
| Check-then-name / PID safety claims | superseded | Witness types requiring a corroborated observation (`RC-OBS-005`, `RC-PROC-001..003`). |
| Rev-CAS ("clobber structurally impossible") | superseded | Deleted by red-team: beads exposes no per-bead revision; serial single-writer + read-back drift (§4, §6). |
| Whole-fleet `Decide(Snapshot)` | superseded | Kept per-session; fleet arms are a later, separately-parity'd rewrite. |
| Sealed 6-method handler | superseded | Typed enum over the canonical state space + exhaustive lint (D1). |
| simworld DST as a merge gate | superseded | Demoted to nightly stretch; real gates are table tests + tmux conformance. |
| §11 "immediate recommendation" (W0 three fixes) | superseded | Explicitly "do not implement from this historical list." |
| W0–W8 wave labels | renamed | Historical lineage only; delivery runs through canonical Phase 0–12 `P*` slices. |
| Corroborated-Dead witness; intensity-only guard; `OnUnrecognized` never quarantines | preserved | Baked into D3/D4/D5 and the matrix. |
| IntentMarker: bounded at-least-once, never exactly-once | preserved | D8. |
| Witness types (`DeathCertificate`, `LiveTargetWitness`) | preserved | Unanimous red-team survivor; `RC-OBS-005`. |
| "Claim exactly the guarantees the store/runtime provide" | preserved | Governing principle; the no-CAS reality (§6) is its direct consequence. |
| Negative-space fences: trusted authorization; store-restore lineage; protected/tamper-evident gate artifacts; external `bd`/schema compatibility | preserved (added as gates) | `RC-AUTH-001..003`; `RC-STORE-001..003`; `RC-GATE-001..002`; `RC-MIG-002` / `RC-CERT-001`. |

## 4. Non-goal amendments

D11 requires amending, not silently contradicting, two non-goals carried from
`PLAN.md` and the design core.

- **"No reconciler rewrite."** Preserved as a *strangler* discipline: delivery is
  incremental behind a parity pin, arm by arm, through the Phase 0–12 gates. The
  later fleet-arm work that the proposal honestly labeled a rewrite is sequenced
  late and carries its own parity strategy; it does not authorize a big-bang
  cutover, and no phase label is ever a cutover gate.
- **"No CAS."** Preserved as "no *general* CAS, and no CAS as a refactor
  precondition." Amended only to the extent that a single **narrow** conditional
  primitive — following the existing `ConditionalAssignmentReleaser.ReleaseIfCurrent`
  precedent and modeled by `RC-STATE-003` — may be proposed later under its own
  bead and justification. Until such a primitive actually lands,
  `session-store-fences.md` remains controlling and multi-writer clobber is
  handled by convergence, not prevented by the store.

## 5. Session-plan step dispositions (`PLAN.md`)

Every active step in the session-refactor sequence is mapped. Step tracking is
canonical in beads; statuses below reflect those beads.

| Step (`PLAN.md`) | Bead(s) | Disposition | Note |
|---|---|---|---|
| Step 0 — land the behavior ledger | (this-PR in `PLAN.md`) | completed | `REQUIREMENTS.md` + `AGENTS.md` are committed sources. |
| Step 1 — lifecycle-timer decider | `ga-ltlwc1` | completed | Idle-timeout / max-age ladders extracted; `SESSION-RECON-008/009`. |
| Step 2 — stability / churn / rate-limit predicates | `ga-i9r8fi` | completed | `SESSION-RECON-010/011`. |
| Step 3 — fix the real bugs | `ga-4of1nc`, `ga-7f6ocx`, `ga-frfj2d`, `ga-kmoj9c` | completed | Configured-name hijack; `RepairEmptyType` read-path write; Huma-close worker-boundary; `session.woke`/`session.stranded`. |
| Step 4 — store-fence decision | `ga-q65c22` | completed | `session-store-fences.md` is Accepted; see §6 (its claims are preserved). |
| Step 5 — read-only target classification | `ga-mxchkb` | completed | `DecideSessionTarget`; `SESSION-ID-011`. |
| Step 6 — first mutating extraction (wake eligibility, then close) | (future; gated "only after Steps 1–5 hold") | preserved → superseded on approval | Remains the controlling next extraction for current code. On plan approval it is **superseded** by the redesign's phased mutation slices (`RC-STATE-002/003`, migration gate P0.5+), preserving characterization-first discipline. |

Design-review bead `ga-unpr2y` (owner of the long-form `DESIGN.md`, not yet
landed) remains the architecture-direction owner; this ratification does not
close it.

## 6. Store-fence claim dispositions (`session-store-fences.md`)

`session-store-fences.md` is **Accepted** and **preserved as controlling** for
every current mutating extraction. Per D11 it is superseded only through a
successor record, and only when the new conditional-write capability actually
lands.

| Store-fence claim | Disposition |
|---|---|
| Store facts: no conditional writes; external batches non-atomic; transactions cannot read | preserved (code is the source of truth in `internal/beads`) |
| Fence 1 — city identifier flock (re-read inside the lock) | preserved |
| Fence 2 — token precondition with reread (`instance_token`) | preserved |
| Accepted residual: reread-then-write is not atomic; safety from idempotent re-application, edge-triggered consumption, partial-batch tolerance | preserved |
| "Not adding CAS/conditional writes as a refactor precondition" | preserved, **amended** per §4: a single narrow conditional primitive may later land under its own bead |

## 7. Requirements crosswalk

`REQUIREMENTS.md` gains a "Semantic Ratification (P0.2)" scenario group. Decisions
already carried by an existing row cite it in §1; the three that had no prior row
are recorded as new ratification scenarios:

- `SESSION-RATIFY-004` — immediate targeted death confirmation after the first
  authoritative-list absence (D4 timing).
- `SESSION-RATIFY-007` — trace-taxonomy retention window: through migration and
  ≥1 release after final cutover (D7 window).
- `SESSION-RATIFY-011` — `PLAN.md` extraction order and `session-store-fences.md`
  remain controlling until plan approval and conditional-write landing (D11).

Each cites this record and the controlling plan/proposal evidence. `PLAN.md`
carries the step-disposition ledger of §5.

## 8. Acceptance and approval

- **Every decision here is settled** — none can change the action schema, key
  identity, safety direction, storage contract, or requester authorization in
  Phases 1–7: each such axis is pinned by a `RC-*`/`SESSION-*` row cited above.
- **Protected rows.** Per INV-32 and `RC-GATE-001`, the ratified contract and its
  protected acceptance rows are change-controlled; edits flow through the G0
  base-manifest + signed-delta lineage, not ad-hoc prose.
- **Remaining gate.** P0.2 acceptance requires **two independent maintainers** to
  approve this ratification and its requirements crosswalk. This document is the
  artifact under that review; it does not self-certify that approval.
- **No runtime behavior changed** by this ratification; its rollback is to revert
  the documentation before implementation begins.
