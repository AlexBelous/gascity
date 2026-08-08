# WD parity campaign instruments

Two instruments serve the WD parity campaign (`ga-f7v2ft.122`, spec:
`engdocs/plans/reconciler-distillation/DETECTOR.md` §3 and §3b). Both exist only
for the campaign and are deleted at WE with the rest of the D4-retained perf CLI
(DETECTOR.md §5).

- **`gc perf parity-join`** — joins legacy reconciler trace records against
  detector-shadow records and reports per-family parity.
- **`test/campaign/arming`** — keeps every configured template detail-armed for
  the whole campaign window and proves it stayed armed.

Run the harness first. An unarmed window records nothing durable, so joining an
unarmed corpus produces an empty table, not a readout.

## The arming harness

Unarmed detail records are stashed per template (cap 400, FIFO-evicting) and the
record path returns before `addRecord`; `End()` copies only promoted records. So
an unarmed campaign produces **zero evidence**, silently. Auto-arms do not save
you: they cover only anomalous reasons and outcomes, across four slots, with a
ten-minute expiry — and the detector-shadow reason vocabulary is deliberately
outside `shouldAutoArmForTrace`, so shadow records never auto-arm anything.

The harness therefore arms every configured template manually, verifies the arms
at every sample boundary, and re-arms ahead of expiry.

```bash
make test-campaign-arming GC_CAMPAIGN_CITY=/path/to/campaign/city
```

| Env var | Default | Meaning |
|---|---|---|
| `GC_CAMPAIGN_CITY` | *(required)* | city to arm; the test skips without it |
| `GC_CAMPAIGN_BIN` | `gc` | gc binary to drive |
| `GC_CAMPAIGN_WINDOW` | `168h` | how long to hold the fleet armed (§3b wants ≥7 days) |
| `GC_CAMPAIGN_INTERVAL` | `5m` | spacing between sample boundaries |
| `GC_CAMPAIGN_ARM_FOR` | `30m` | `gc trace start --for` value; must exceed interval + lead |
| `GC_CAMPAIGN_LEAD` | `2m` | re-arm when an arm cannot cover the coming interval plus this |
| `GC_CAMPAIGN_REPORT_DIR` | *(unset)* | write `arming-report.json` here |

Templates come from `gc agent list --json` (`.agents[].name`), which is the value
a trace record carries in `template` (`TemplateParams.TemplateName`).

At each boundary the harness reads `gc trace status --json`, arms anything whose
detail arm is missing, expired, or too short-lived, re-reads to prove the write
took, and then closes the previous interval. Continuity needs **both** ends:

- the arm observed when the interval opened had to outlast the interval, and
- the arm observed when it closed has to be the *same* arm — a fresh `armed_at`
  means the store lost the arm in between and re-created it.

Any failure is reported as a `Gap` with a reason (`unarmed_at_open`,
`unarmed_at_close`, `expired_inside_interval`, `rearmed_inside_interval`), and
`report.armed` goes false. **Cycles inside a gap recorded nothing durable and
must be excluded from the parity readout.** The harness also fails immediately
if an arm write does not show up in the arm store — a silently deaf arming path
would otherwise produce a whole window of false confidence.

Re-run the harness after every controller restart and config reload (§3b
requires the window to span at least one of each, with arms re-verified after
each). The harness re-arms on its own at the next boundary, but a fresh
`armed_at` is a gap for that interval by design.

## The parity-join tool

```bash
gc perf parity-join --trace-dir "$CITY/.gc/runtime/session-reconciler-trace"
gc perf parity-join --trace-dir "$TRACE_DIR" --since 168h --json
```

| Flag | Default | Meaning |
|---|---|---|
| `--trace-dir` | *(required)* | trace store root (the directory holding `segments/`) |
| `--since` | *(all)* | only join records newer than this duration ago |
| `--template` | *(all)* | narrow to one normalized template selector |
| `--bar` | `0.995` | §3b must-match bar per family |
| `--samples` | `10` | unclassified mismatch samples to print |
| `--json` | off | emit the versioned JSON readout instead of the table |

The result schema declares `x-gc-jsonl`, so the readout stays on stdout even
when the verdict exits nonzero. The command exits nonzero on `no_evidence` or
`we_blocker`; capture stdout regardless of exit code.

### Join contract

The join key is the **shared trace-cycle handle** `(trace_id, tick_id)` plus the
normalized session name, cross-checked on `session_bead_id`. Records are
distinguished by `fields.effect_owner`: `legacy`, `detector-shadow`, or `keyed`.

This is an equality join on the cycle handle, not a windowed join, and that is a
deliberate consequence of §2: the sweep runs *inside* `beadReconcileTick` beside
the legacy call — no new loop, timer, or goroutine — so there is no cadence to
reconcile. Both records for a session land in the same cycle. A window would be
strictly weaker: it would admit cross-tick false pairs.

The one genuinely time-skewed family is **D-DRAIN**, where the handler reads the
ack while legacy polls in-tick. That surfaces as a legacy-only record in cycle N
and a detector-only record in cycle N+1. §3b already names that class
(`ack_timing_skew`), so the tool triages it as a singleton divergence rather than
widening the join to hide it.

Records that carry no `effect_owner` are counted in `cycles.unowned_records` and
never guessed at, so running the tool against a pre-WD.2 corpus reads as
no-evidence with a visible cause rather than as a silent zero.

### Classification

`cmd/gc/cmd_perf_parity_join_table.go` is the machine-readable transcription of
the §3b table: per family, a parity level (`detection`, `decision`, `act`), its
trace sites, and its expected-divergence rules.

- **detection**-level pairs are matched on presence alone; the decision arms are
  handler-side and deliberately not compared.
- **decision** and **act** pairs must agree on `reason_code` and `outcome_code`.
- A failed bead-ID cross-check is `incomparable` (`bead_id_cross_check_failed`),
  never matched — two rows sharing a name but not an identity are a D-DUP shape.
- A `detector-shadow` record claiming `effect_applied: true` breaks the read-only
  invariant the whole campaign rests on. It is counted in
  `shadow_effect_violations` and is an unconditional WE blocker.

Divergence rules are deliberately conservative. A rule exists only where §3b
names the divergence **and** the trace codes that express it exist today. Where a
named divergence lands with a later WD slice (D-STALL's claim-check-error arm,
for one), no rule is written and the mismatch surfaces as `UNCLASSIFIED` with its
full `(site, reason, outcome)` tuple. That is the §3b workflow — *triage it,
extend the table with evidence, or fix the detector* — and it is why a
speculative predicate would be worse than none: it would silently bucket a real
mismatch.

### Exclusions

- Any cycle whose rollup reports `record_budget_exceeded` drops is dropped from
  the readout entirely (§3, operational rule).
- Any cycle with no `cycle_result` rollup is dropped as truncated evidence, and
  counted in `cycles.excluded_no_cycle_rollup`.
- `cycles.without_detail_arms` counts considered cycles whose rollup shows zero
  detailed templates. A nonzero value there with a `no_evidence` verdict is the
  positive proof that the window ran unarmed.

### Verdict

`RESULT` is one of `NO EVIDENCE`, `WE BLOCKER`, `BELOW BAR`, or `PASS`. The
match rate is `matched / (matched + mismatched)` per family; incomparables are
excluded from the rate but still appear in the triage log. Per §3b, **one
unclassified mismatch is a WE blocker.**
