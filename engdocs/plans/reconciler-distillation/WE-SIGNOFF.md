# WE Sign-off — WD.15 Parity Campaign

Window: **2026-08-12T03:39:17Z → 2026-08-19T03:39:17Z** (168h, closed complete).
City: `/data/cities/reconciler-campaign` (`daemon.session_reconciler=auto`,
effective owner keyed, schema-v59 managed Dolt, isolated sockets
`wd15-campaign`/`wd15-ops`). Below, `campaign/` means
`/data/cities/reconciler-campaign/campaign/`.

This is the one document the owner signs to authorize the WE cutover: the
deletion of the legacy god function and the D4-retained shadow/parity
machinery. It asserts nothing not already recorded in the artifact chain:

- **Ledger:** bead `ga-f7v2ft.122` (every readout, incident, triage verdict,
  and signature, day 0 through window close).
- **Final readout:** `campaign/reports/we-final-yield-join.json`
  (tool `b19d84be97`, archive corpus, produced 2026-08-19T04:59Z).
- **Arming:** `campaign/reports/arming-report.json` (168h harness run).
- **Report chain:** `campaign/reports/day0-parity-join.json` …
  `day7-yield-join.json` (plus `day4b/c`, `day6b`) — the durable per-day
  evidence, and the *only* evidence for the pre-archiver day-0/1 span (§4.2).
- **Bar definition:** `engdocs/plans/reconciler-distillation/DETECTOR.md` §3b.
- **Deletion scope:** `engdocs/plans/reconciler-distillation/DESIGN.md` §3–§4
  and DETECTOR.md § "Coexistence-code census".
- **Teardown:** `engdocs/contributors/wd15-campaign-runbook.md` § "Teardown".

Uncertainty is stated where it exists; §4 is written against interest.

## 1. The §3b bar, criterion by criterion

Bar text: DETECTOR.md §3b ("Window"/"Bar", lines ~2106–2115; re-expressed over
yield-joins per the owner ruling of 2026-08-12, § "The bar, re-expressed").
All numbers: `campaign/reports/we-final-yield-join.json` unless noted.

| # | Criterion (§3b) | Required | Measured | Source |
|---|---|---|---|---|
| 1 | Consecutive days on a live auto-mode city | ≥7 | 7.0 (168h, 2026-08-12T03:39:17Z → 2026-08-19T03:39:17Z) | ledger `ga-f7v2ft.122` window-open comment (2026-08-12 04:01) + window-close note; report chain dates |
| 2 | Joined trace rows (yield-pairs + act-pairs) | ≥10,000 | **79,382** (77,648 yield + 1,734 act); `count_bar_met: true` | final JSON `joined_total`, `joined_yields`, `joined_acts` |
| 3 | Controller restarts spanned, arms re-verified after | ≥1 | **2** (day-4 planned + day-4 unplanned; §2) | ledger day-4 notes; exclusion windows in final JSON |
| 4 | Config reloads spanned, arms re-verified after | ≥1 | **1** (2026-08-16T08:45:54Z, rev `fbe3ee649754`, non-disruptive; arms re-verified 08:46:26Z) | ledger day-5 readout |
| 5 | Match bar per must-match cell | ≥99.5% | D-DEADLINE **99.844%** (67,269 m / 105 mm); D-ORPHAN **99.896%** (10,517 / 11); D-DRAIN **100.00%** (357 / 0); D-WAKE **100.00%** (1,214 / 0); `bar_met: true` | final JSON `families[]` |
| 6 | Every mismatch triaged into a §3b class; one unclassified = WE blocker | 0 unclassified | **0** unclassified, all families; `we_blocker: false`. 116 counted mismatches, all classified (§3) | final JSON `families[]`, `triage[]` |
| 7 | Exclude cycles with `record_budget_exceeded` drops | excluded | `excluded_record_budget_exceeded: 0` (none occurred) | final JSON `cycles` |
| 8 | Arms verified at every sample boundary, re-armed before expiry (§3) | zero gaps | 6 templates, 1,993 boundaries, 2,376 re-arms, `gaps: null`, `armed: true`, full 168h | `campaign/reports/arming-report.json` |
| 9 | Zero-write shadow control (engine-divergence guard) | 0 | `shadow_effect_violations: 0`, entire window | final JSON |

Corpus scale: 66,797 cycles scanned / 66,794 considered
(`excluded_no_cycle_rollup: 3` — restart-truncated last cycles, dropped whole
rather than leaked as half-pairs). Corpus source: `campaign/trace-archive/`
(the 4h rsync archiver, live from day 3), not the live WAL — the live store
retains only ~24h at the 1 GiB cap (§4.2).

Criterion 5's rates are computed over yield-joins and act-joins together, with
the 99.5% floor applied per must-match cell exactly as §3b's table defines the
cells. The four families above are the campaign's *acting* families; the other
seven rows read `no-data` — see §4.1, which is load-bearing for what this
signature does and does not claim.

## 2. Window-events ledger

Four events occurred inside the window. Two produced exclusion windows; two
deliberately did not. All exclusion windows are self-described in the final
artifact (`excluded_windows[]`), boundaries supplied at event time per the
runbook rule ("Record the restart's exclusion window"), never inferred.

| Event | When (UTC) | What | Exclusion window | Records excluded |
|---|---|---|---|---|
| Restart 1 (planned, §3b requirement) | 2026-08-15 ~06:00:0x | `gcc stop --force` clean; binary rotated `2b7f92a9a2` → `37e34b5788` (diff = 7 files, **zero engine files** — verified in the day-4 D-SLEEP triage, ledger); controller instance 1073853 (up 3d02h) → 3470343; arms re-verified 06:02:35Z | `2026-08-15T05:59:50Z/06:02:40Z` | 980 |
| Restart 2 (unplanned, **operator error**) | 2026-08-15 08:44:50–08:48:10 | SIGQUIT of the *healthy* controller 3470343 after a misdiagnosed "26h outage" that did not exist (stale session-date header + `ps\|head` truncation). Same binary relaunched ~08:46; instance 3414031 ran 3d19h through the close; arms re-verified. Recorded honestly in the ledger as operator error; remediation and the resulting directive (`date -u` first; never `ps\|head`) are memorialized there | `2026-08-15T08:44:40Z/08:49:00Z` | 1,821 |
| Config reload (§3b requirement) | 2026-08-16 08:45:54 | `city.toml` marker edit + `gcc reload` → "Config reloaded: 6 agents, 0 rigs (rev fbe3ee649754)"; non-disruptive, no controller restart; arms re-verified 08:46:26Z | none needed | 0 |
| Infra event: campaign tmux server death | 2026-08-16 ~17:16:14 | The `wd15-campaign` tmux server dropped mid-tick (real provider outage; cause not pinned, no OOM found — ledger day-6 Q1). Keyed rebuilt the fleet in ~2min. **Not excluded** — survived live; produced the two day-6 degraded-visibility shapes, both adjudicated with in-band proof (§3 rows for `orphan_running_set_unavailable_fail_closed` and the day-6 `pre_wake_supersede_convergence` spelling) | none (deliberate) | 0 |

A third exclusion window, `2026-08-19T03:39:17Z/2026-08-20T00:00:00Z`
(54,806 records), is not an event: it truncates post-close records from the
archive corpus so the readout covers exactly the 168h window. The final day ran
~10x pace (host load dropped; ledger window-close note), which is why the
overhang is large.

## 3. Classification table summary

Every class present in the final readout (`we-final-yield-join.json`
`triage[]`), with what a record must prove to enter the class and where the
class was adjudicated. Class definitions live in DETECTOR.md §3b's table
(rows, lines ~2073–2084) and `cmd/gc/cmd_perf_parity_join_table.go`.

**Classes that keep records IN the mismatch denominator** (they count against
the 99.5% rate — nothing below excuses them):

| Family / class | Count | Verification requirement | Adjudication |
|---|---|---|---|
| D-DEADLINE `deadline_crossed_after_sweep_sample` (mismatched) | 105 | Deadline crossed inside the tick, after the sweep's shared `clk.Now()` and before legacy's per-row read; proven not-a-candidacy-gap at source | `ga-f7v2ft.158` (day 1, closed 2026-08-12) — deliberately kept MISMATCHED |
| D-ORPHAN `orphan_live_detector_lead_one_tick` (mismatched) | 11 | One-tick detector lead (~2.2s jitter); legacy twin next tick, same row+effect | Day-3 triage (2026-08-14, tool fix `e70bc64ede`) — deliberately kept MISMATCHED: "explains the singleton without proving the twin landed" |

**Classes that move records OUT of the denominator** (incomparable — each
carries its proof obligation; the dangerous variant of each stays loud):

| Family / class | Count | Verification requirement | Adjudication |
|---|---|---|---|
| D-DRAIN `drain_ack_adjacent_cycle_convergence` | 3 | **Per-record** proof of the keyed twin: same session, same `reconciler.session.drain_ack` site, keyed *ownership stamp* (never `detector_family`), within one tick (11.5s, measured cadence). A twinless skew stays an UNCLASSIFIED MISMATCH and blocks WE | **Owner-signed 2026-08-17/18** (DETECTOR.md §3b D-DRAIN row). Signed specimens: `dependent-rc-7mzpx` cycle-0860a236ff1b82bd (08-15T04:49:21.053Z), `s-rc-wisp-y73064d` cycle-41b467cd627d719e (08-17T00:21:48.259Z), `s-rc-wisp-d30uo8f` cycle-03d51f88678dbb50 (08-17T02:05:29.315Z). Implemented `b19d84be97`; option (i) of the day-6 owner-facing recommendation |
| D-DRAIN `advance_arms_journey_proven` | 8,828 | Structural: legacy's advance pass is the `session_reconcile.drain_advance` PHASE site (§1 row 28) — no per-session twin can exist; arms covered by journey parity (WD.6 deltas 6/7) | DETECTOR.md §3b D-DRAIN row (WD.6/WD.15); fixture-guarded in `cmd/gc/cmd_perf_parity_join_corpus_test.go` |
| D-ORPHAN `orphan_running_set_unavailable_fail_closed` | 1 | Scoped to the exact tuple: detector `running_set_unavailable`/skipped BESIDE legacy orphaned/drain. Any other legacy conclusion beside the refusal stays unclassified and blocks WE. Zero-write independently guarded (`shadow_effect_violations`) | Day-6 triage Q1 (2026-08-17, fix `e7e9e7a3dd`); root cause the real tmux-server outage (§2); RED from the byte-copied production cycle + scope control |
| D-SLEEP `fleet_only_no_wake_left_to_legacy` | 136 | The WD.5 delta-1 shape: legacy drains where the detector predicts nothing (fleet verdict has no keyed home until D-WAKE's fleet rungs) — incomparable **by design** | Class predates the window (DETECTOR.md §3b D-SLEEP row); day-4 triage verified it covers 100% of the family's records; JSON disclosure fix (`bar_status: no-data` vs a false 0%) `a789dbaab3` |
| D-STALE-CREATE + D-WAKE `pending_create_in_flight_family_split` | 1,250 + 1,240 | One row lands in two families at two sites; entry requires the twin's presence in the same cycle (two spellings: legacy `wake` or `keyed_start_owner` stand-down) | WD.15 day-2 triage, recorded in DETECTOR.md §3b D-STALE-CREATE row |
| D-STALE-CREATE `live_runtime_recovery_excluded_from_sweep` | 9 | Structural: legacy defers pending-create recovery only for ALIVE-runtime rows (:3117–3125); `detectStaleCreate` excludes exactly those (:1272–1274) | DETECTOR.md §3b row (WD.15) |
| D-STALL `reset_stall_alarm_no_detector_arm` | 8 | Populations disjoint by construction (alarm fires only NOT-alive; `detectStall` returns unless alive); the site is an alarm, zero mutation — no effect to compare | WD.15 day-2 triage; WE consequence recorded in `ga-f7v2ft.159`. Family off-by-default ⇒ the **Q3 test-only case, owner-signed (Julian, 2026-08-12: "signed off")** |
| D-WAKE `pre_wake_supersede_convergence` | 5 | **Requires** the same-cycle `detector_wake_target` co-twin (admission `refused_uncertifiable`, `effect_owner=keyed`, `effect_applied=false`). A twinless supersede stays unclassified and blocks WE (control-tested) | Day-3 incident triage (2026-08-14, `e70bc64ede`, 3 RED→GREEN from production cycles); day-6 Q2 fixed co-twin index threading for the `keyed_start_owner` spelling (`e7e9e7a3dd`) — same evidence shape, one record spelling over, not a widening |
| D-WAKE `wake_admission_refused_row_stays_legacy` | 135 | The sweep's own admission refused to route the target (`detectorAdmissionRefusedUncertifiable`, `session_detector_sweep.go:392-396` — "the row stays legacy's"); act-vs-non-act on either side | DETECTOR.md §3b D-WAKE row (WD sweeps) |

Sum check: 105 + 11 counted mismatches = the 116 mismatches in `families[]`;
every incomparable count above appears verbatim in the final `triage[]`.
Unclassified: 0.

Residency provenance (the Q3 column): off-by-default families take **test-only
parity in lieu of campaign residency**, per the signed Q3 decision (Julian,
2026-08-12, ledger). Campaign residency covers the acting families.

## 4. Residual caveats — stated against interest

These are the reasons NOT to sign, stated as strongly as the evidence
supports. The signature in §6 accepts them by name.

1. **Seven of eleven detector families contributed zero comparable production
   evidence.** The 99.5% bar was cleared on four acting families
   (D-DEADLINE, D-ORPHAN, D-DRAIN, D-WAKE). `start` rests on the pre-existing
   shadow-worker/comparator evidence (§3b row 1). D-DRIFT, D-ZOMBIE, D-DUP,
   D-STRANDED, D-STALL are `no-data` — off-by-config under the signed Q3
   test-only substitution, or traffic-free in this city. D-SLEEP has **no
   comparable evidence at all**: 136/136 records incomparable-by-design
   (`fleet_only_no_wake_left_to_legacy`), and its entire n is a
   controller-restart startup transient (ledger, day-4 triage). A real
   divergence in any of these families was structurally invisible to this
   campaign; their parity claim is the WD-slice test corpus, not this window.
2. **Days 0–1 cannot be re-derived from raw traces.** The live WAL prunes at
   a 1 GiB cap (~24h at campaign write rates, `cmd/gc/
   session_reconciler_trace_store.go` pruneOldSegments; ledger day-3
   RETENTION note); the archiver only exists from day 3. The day-0/1 span's
   evidence is the immutable report chain (`day0`–`day3` JSONs) plus gapless
   tick-counter and controller-instance continuity — which establishes
   residency but **cannot support re-triage of that span under any rule
   adopted later**. The day-3 incident was resolvable only because raw cycles
   survived; for days 0–1 that option is permanently gone.
3. **The one undecidable record.** The day-0 legacy drain ack
   (`worker-rc-6nq`, cycle-c10ea5757924016e, 2026-08-12T03:39:39.085Z) can
   never prove or refute its keyed twin — its adjacent cycles no longer exist
   anywhere on disk. The fixture pins it as a permanent unclassified
   (`parityJoinDrainAckUnprovenSingleton`,
   `cmd/gc/cmd_perf_parity_join_corpus_test.go:300-318`) so the class cannot
   be relaxed to launder it. It is outside the final readout's corpus (the
   archive) but inside the window's history: an unprovable ack, tightened
   rather than excused.
4. **D-DRAIN's 100.00% is a product of the signed class, not raw counting.**
   Its 3 skews all proved twins, but had they stayed counted the family reads
   357/360 = 99.17% — below bar. The family passes *because* the owner signed
   `drain_ack_adjacent_cycle_convergence` (structural skew, per-record proof).
   The accrual rate (~2/day against ~90 comparable joins/day, ledger day-6 Q3)
   means it would never have cleared on volume.
5. **~84% of the coexistence-era code is PERMANENT; only ~16% dies at WE.**
   DETECTOR.md § "Coexistence-code census": of ~31,100 production insertions,
   ~16% dies (honest band 15–20%, ~4.8–5.4k LOC); ~84% is live keyed logic.
   Signing WE deletes the god function and the instruments — it does not make
   the lane small, and the census's trap (live code with "shadow" in its
   name) is restated in §5.4.
6. **One §4-WE precondition is not satisfied: the `gc perf
   reconciler-compare` A/B was never run and archived.** DESIGN.md §4 (WE)
   and the WD.15 acceptance criteria require it "recorded in engdocs" before
   cutover. No such artifact exists (checked: `campaign/reports/`, engdocs,
   git history). Relatedly, `engdocs/plans/reconciler-distillation/evidence/`
   — the AC's named destination for campaign artifacts — was never created;
   the durable chain lives in `campaign/reports/` (a directory the teardown
   destroys) and the bead ledger. §5.3 makes evidence preservation a
   teardown *precondition*; §6 makes the perf A/B an explicit checkbox the
   owner either directs or waives.
7. **The two counted-mismatch classes remain unexplained-by-proof.**
   `deadline_crossed_after_sweep_sample` (105) and
   `orphan_live_detector_lead_one_tick` (11) are *characterized* timing races,
   deliberately kept mismatched because their benignity is argued, not
   individually proven. They fit inside the bar (99.844% / 99.896%); they are
   named here so the bar is not mistaken for 100% agreement.

## 5. The WE deletion ledger

### 5.1 What gets deleted (DESIGN.md §3 rows; census in DETECTOR.md)

| Deletion | ~LOC |
|---|---|
| Legacy reconciler cluster: the god function `reconcileSessionBeadsTracedWithNamedDemand` (2,611 lines; `cmd/gc/session_reconciler.go` 6,198) + wrapper chain + legacy-only trace sites (triaged, not blind) | −13,700 prod, −22,000 test |
| Rollout tri-state, legacy-fallback lattice, `Entered` flags, drain-ack rollback, ownership latch, nudge coexistence | −1,500 prod, −2,900 test |
| D4-retained shadow/parity machinery + perf CLI: shadow comparator pipeline, `gc perf parity-join`, `gc perf reconciler-compare`, arming harness, `nudge_shadow` gate — the retention condition (D4: "until the WE campaign") is discharged by this document | −7,900 |
| Census residue: legacy-file yield/stand-down edits (819), 14 `withLegacy*` bridges (~400), sweep lattice (~200–350) | (inside the 16%) |
| The campaign city `/data/cities/reconciler-campaign` (after §5.3 preservation) | — |
| `daemon.session_reconciler` becomes a **deprecation warning, not silent removal** (DESIGN.md §4 WE, repo precedent) | — |

### 5.2 What each deletion orphans (decide, don't default)

- **`ga-f7v2ft.159` (P2):** `events.SessionResetStalled` has exactly one
  emitter, `recordResetStallIfDue`, called from the god function's row scan.
  The keyed side cannot grow one incidentally (`detectStall` returns unless
  alive — disjoint populations). At WE: port the alarm onto the keyed reset
  machinery (.103) or retire the event from `events.KnownEventTypes` + its
  payload registration. Recorded decision required.
- **`ga-f7v2ft.137` (P1):** D-STRANDED keys on `stranded_event_emitted_at`,
  stamped only by `emitSessionStrandedDiagnostic` inside the god function
  (emit-once stamp, `ApplyOpenInfoPatch` carrier, `clearStrandedEventMarker`
  alive-tick clear). Also legacy-owned and unported: the sibling clean-close
  arm (WD.14 delta 4) and the WD.10b-deferred wake-failure accounting
  (`checkStability`/`checkChurn`) — per the architect ruling in .137, that
  accounting moves wholesale with its own REDs or retires with the breaker
  redesign, decided at WE.
- **`ga-f7v2ft.160` (P2):** `schemas/perf/parity-join/result.schema.json` is
  stale (v1 const vs v2 emission, missing keys, nothing validates). Either it
  dies with the perf CLI on this ledger, or it is regenerated with a sync
  test. Deciding neither leaves a schema that falsely rejects the WE
  artifacts this document cites.

### 5.3 Teardown steps (stated per the runbook — NOT executed by this document)

Precondition — preserve the evidence the teardown would otherwise destroy:

1. Copy `campaign/reports/*.json` (final + arming + full day chain) into
   `engdocs/plans/reconciler-distillation/evidence/` (the AC's destination)
   and commit. Until this lands, the canonical §3b artifact chain lives in a
   directory scheduled for deletion.
2. Decide raw-corpus retention: `campaign/trace-archive/` (~3–5 GiB) is the
   only thing that supports re-triage under a future rule (the day-3 lesson,
   §4.2). Archive or consciously discard — recorded, not defaulted.
3. Stop the detached archiver loop (`pgrep -af wd15-trace-archiver`) before
   removing its target.

Then, verbatim from `engdocs/contributors/wd15-campaign-runbook.md`
§ "Teardown (after WE sign-off)":

```bash
campaign/gcc stop --force
tmux -L wd15-ops kill-session -t wd15ops
tmux -L wd15-campaign kill-session -t <each remaining agent session>
```

Never `tmux kill-server`, and never on the default socket.

### 5.4 Traps carried into WE (verified; do not step on)

- **Never delete by filename.** ~894 LOC of shadow-NAMED files are live keyed
  code (`session_lifecycle_shadow_plan`, `session_lifecycle_shadow_start_plan`,
  `api_state_session_wait_shadow`, `pool_allocation_shadow`); rename at WF.
  Strip scaffolding by compilation (delete `rollout.Mode` from production
  signatures first); ~20 real pool tests contain "FallsBack" and must survive
  (DESIGN.md §6 traps 1–2; DETECTOR.md census trap).
- **`build_desired_state.go` survives WE** (council R2): the re-point
  supersede and `bindPoolSessionTriggerBead` execute post-WE; the ga-vumr7
  duplicate-claim race is not debt the deletion collects for free.
- The four "beats the 30s debounce" assertions become absolute latency
  budgets at WE (DESIGN.md §4).

## 6. Signature block

**What Julian is signing.** That the §3b campaign bar is met as tabulated in
§1 from `campaign/reports/we-final-yield-join.json`; that the window events in
§2 (including the operator-error restart) are completely disclosed and
correctly excluded; that the §3 classification taxonomy — including the
already-signed `drain_ack_adjacent_cycle_convergence` (2026-08-17/18) and the
Q3 test-only residency decision (2026-08-12) — is ratified as the complete
mismatch account for this window; that the §4 caveats, including the
seven-family no-data reality and the unrecoverable day-0/1 raw corpus, are
accepted **by name**; and — supplying the recorded sign-off §3b requires for
detection-level families (D-DRIFT, D-DRAIN, D-ZOMBIE, D-STRANDED) — that
require-mode journey A/B plus the yield-join satisfies D4 for their effect
arms, per the owner ruling of 2026-08-12.

**What this signature authorizes.**

- Opening the WE wave: deletion of the legacy god function, the coexistence
  lattice, and the D4-retained shadow/parity + perf instruments, per §5.1,
  with the §5.2 orphan decisions (.159 / .137 / .160) resolved on the WE
  ledger — recorded, not defaulted.
- Campaign-city teardown per §5.3, **only after** its evidence-preservation
  preconditions land.

**What this signature does NOT authorize.**

- Merging the WE implementation. The cutover change gets its own review
  (Fable council reviews WE before merge — DESIGN.md §4), its own RED/GREEN
  and journey gates, and the full-suite wave gate. This document is evidence
  the *precondition* is met, not review of code that does not yet exist.
- Skipping the `gc perf reconciler-compare` A/B (§4.6). Owner selects one:
  - [ ] Run and archive the A/B in engdocs before the cutover commit
        (the DESIGN.md §4 default), or
  - [ ] Waive it here, recorded: ______
- Any WF work (renames, lease folds, controller-skeleton extraction), any
  deletion by filename, or silent removal of `daemon.session_reconciler`.

Signature: ______________________  Date: ____________

(Julian Knutsen, owner — signing here is the recorded owner sign-off
referenced by DETECTOR.md §3b and closes WD.15 / `ga-f7v2ft.122`.)
