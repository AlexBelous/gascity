# WD.15 parity campaign runbook

The operating manual for the `ga-f7v2ft.122` campaign window: the dedicated city
it runs on, how to prove it is armed, how to read it out, and what to do when
something dies. Spec: `engdocs/plans/reconciler-distillation/DETECTOR.md` §3
(arming and durability) and §3b (the sign-off bar). Instruments:
`engdocs/contributors/wd-parity-campaign-instruments.md`.

The campaign is deleted with the rest of the D4-retained perf CLI at WE.

## The city

| | |
|---|---|
| Path | `/data/cities/reconciler-campaign` |
| City tmux socket | `wd15-campaign` (gc-owned agent sessions only) |
| Ops tmux socket | `wd15-ops` (controller, arming harness, workload driver) |
| Lane | `/data/projects/gascity/.claude/worktrees/rec` |
| gc | `/data/cities/reconciler-campaign/bin/gc` |
| bd | pinned `/opt/beads/releases/gc-bf97b73749ac-20260805/bd`, symlinked at `bin/bd` |
| Beads | schema v59, managed Dolt (`BEADS_DOLT_AUTO_START=1`) |
| Mode | `daemon.session_reconciler = "auto"`, patrol 30s, `beads.conditional_writes = "require"` |
| Trace store | `.gc/runtime/session-reconciler-trace` |

Everything runs through `campaign/env.sh`. Source it, or use the two wrappers:

```bash
campaign/gcc trace status --json     # campaign gc against the campaign city
campaign/bdc list --limit 5          # PINNED bd against the campaign store
```

**Never run the ambient `bd` against this city.** It carries the beads #4907
loaded gun. `campaign/bdc` and `env.sh` both pin the release binary.

### Two sockets, and why

`gc` adopts every tmux session it finds on the city socket at startup — the
first attempt at this city logged `Adopted 1 running session(s) into bead
store` and turned the ops window into a template-less session bead that no
template arm covers and that parked the keyed path on every tick. Campaign ops
therefore live on `wd15-ops`; only gc's own agent sessions live on
`wd15-campaign`. Do not put a window on the city socket.

### Isolation from the production fleet

- `GC_HOME=<city>/gchome`, so this city is absent from `~/.gc/cities.toml` and
  the machine-wide supervisor never sees it.
- `<city>/gchome/supervisor.toml` pins port **8461**. The default is 8372,
  which on this host is the *production* supervisor — `gc status` was resolving
  it before the pin. Without this, a campaign command can reach production.
- `BEADS_DIR` is pinned to the city's own `.beads` (the ga-f7v2ft.146 stub-
  `.beads` workaround, permanent here rather than a fallback).
- The city's own tmux socket. Never the default server.

## Windows on `wd15-ops`

```bash
tmux -L wd15-ops list-windows -t wd15ops
```

| Window | Script | Role |
|---|---|---|
| `controller` | `campaign/run-controller.sh` | `gc start --foreground --no-strict`; the window *is* the controller |
| `arming` | `campaign/run-arming.sh` | `make test-campaign-arming`, the window driver |
| `workload` | `campaign/workload.sh` | traffic so every acting family sees rows |

## Building gc

```bash
bash /data/cities/reconciler-campaign/campaign/build-gc.sh
```

**A bare `go build ./cmd/gc/` is wrong here.** The lane lives under
`/data/projects/gascity/.claude/worktrees/`, and the Go toolchain's buildvcs
resolves the *enclosing* checkout's HEAD: the first campaign binary stamped
`gc_commit: 4ef1f7b001…-dirty` into every trace record while actually running
`2b7f92a9a2`. The script does what the Makefile does — `-buildvcs=false` plus
explicit `-X main.commit=$(git rev-parse --short HEAD)`. Check any binary before
trusting its records:

```bash
campaign/gcc version --json      # commit must equal the lane HEAD
```

## Checking the arms

Arms are the whole campaign. An unarmed window records **nothing**, silently,
and reads out as an empty all-matched table.

```bash
campaign/gcc trace status --json | python3 campaign/show_arms.py
```

Expect one `detail` arm per configured template (six: `worker`, `database`,
`dependent`, `reviewer`, `control-dispatcher`, `dog`), all `source=manual`, all
expiring ~30m out, plus `session_reconciler: mode=auto owner=keyed
available=True`. Anything less means the cycles since the last good boundary
recorded nothing durable and must be excluded from the readout.

The harness holds the arms; it re-arms at each 5m boundary whenever an arm
cannot cover the coming interval plus a 2m lead. It only writes
`campaign/reports/arming-report.json` when the run *ends* (including on
failure), so during the window read the live status above, and read the
harness's own narration in the `arming` window.

## Re-arming

```bash
bash /data/cities/reconciler-campaign/campaign/arm-all.sh 30m
```

Run it after every controller restart and every config reload. The harness
re-arms on its own at the next boundary, but a fresh `armed_at` inside an
interval is a `rearmed_inside_interval` gap by design — the manual pass closes
the hole faster and makes the re-arm deliberate rather than inferred.

Arms live in `arms.json` **inside the trace store directory**. Moving or
clearing the trace store clears the arms with it.

## Reading it out

```bash
campaign/gcc perf parity-join \
  --trace-dir /data/cities/reconciler-campaign/.gc/runtime/session-reconciler-trace \
  --json
```

Or the human table without `--json`. **Use `--window-start` for a campaign
readout**, not `--since`: the window's clock is backdated to a fixed instant
(`2026-08-12T03:39:17Z`), and an RFC3339 start reproduces the same readout
tomorrow, where a duration silently slides. `--template` narrows to one selector;
`--count-bar` overrides §3b's 10,000-joined floor. `--exclude-window` (repeatable)
drops a restart gap — see [Restart policy](#restart-policy); pass every recorded
window that the readout spans. The command exits nonzero on `no_evidence` or
`we_blocker` and the schema is `x-gc-jsonl`, so **capture stdout regardless of
exit code.**

```bash
campaign/gcc perf parity-join \
  --trace-dir /data/cities/reconciler-campaign/.gc/runtime/session-reconciler-trace \
  --window-start 2026-08-12T03:39:17Z --json
```

Read in this order:

1. `shadow_effect_violations` — must be `0`. Any value breaks the read-only
   invariant the campaign rests on and is an unconditional WE blocker. The check
   covers every sweep record, routed or shadow: the sweep is zero-write by design.
2. `no_evidence` — must be `false`. It is join-based: true whenever `joined_total`
   is zero, however many owned records the corpus holds.
3. `joined_yields`, `joined_acts`, `joined_total` against `count_bar`. The
   yield-pairs carry the window (see below); the act-pairs are rarer and stronger.
4. `cycles.excluded_record_budget_exceeded` and `cycles.excluded_no_cycle_rollup`
   — dropped by design (§3 operational rule / truncated evidence).
5. `cycles.legacy_by_elimination`, `cycles.yield_records`,
   `cycles.unpaired_ownership_yields` and `cycles.unowned_records` — see the
   attribution model below.
6. Per family: `match_rate` against the 0.995 bar, then `triage` for the
   classified divergences and `unclassified` for the samples that need work.

**One unclassified mismatch is a WE blocker.** Triage it, extend the §3b table
with evidence, or fix the detector — then re-run the window for that family. Do
not bucket it.

**One class is verified per row, not per rule.** D-DRAIN's
`drain_ack_adjacent_cycle_convergence` (owner-signed 2026-08-17/18) is
incomparable only for a skew that proves its own keyed twin: a record for the
same session at `reconciler.session.drain_ack`, stamped `effect_owner=keyed`, in
an adjacent cycle within one tick (11.5s). Read its `triage` count as a count of
*verified* skews. If a `reconciler.session.drain_ack` legacy record ever turns up
in `unclassified` instead, the twin was not in the corpus — that is the keyed
engine failing to acknowledge a drain legacy acknowledged, which is a detector
finding to escalate, **not** a window to widen or a bound to tune.

### How the readout attributes a record

Nothing in the tree stamps `effect_owner = "legacy"`. Every keyed handler stamps
`"keyed"`, the sweep stamps `"detector-shadow"` — or `"keyed"` for a condition it
ROUTED (`session_detector_sweep.go:2103-2130`) — and the god function stamps
nothing at all. Legacy is therefore identified by **elimination**: at a site
where legacy is the remaining writer, absence of the stamp *is* the legacy
signature. Teaching the god function to stamp would thread scaffolding through
19 decision sites of a function that dies at WE; the classification rule dies
with the tool.

Elimination only applies inside the §1 legacy vocabulary
(`parityJoinSiteDispositions`, `cmd_perf_parity_join_table.go`). Everything else
is counted and surfaced under `dispositions`, never binned:

| Disposition | Meaning |
|---|---|
| `legacy` | §1 decision site, legacy reason, has a session identity — attributed, counted in `cycles.legacy_by_elimination` |
| `phase_marker` | §1 phase site, no session identity. Legacy writes one cycle-level marker per tick there; binning it would have manufactured a phantom legacy row per cycle in D-DUP and D-STRANDED, whose only sites are phase sites |
| `unattributable` | keyed-owned or shared-writer site (§1 #27) — absence says nothing about legacy |
| `no_session_key` | not a row in a per-session join, whoever wrote it. The live case is the sweep's pool-under-min FILL condition (`queued_pool_allocation`): a wake for a session that does not exist yet |

`cycles.unowned_records` is the sum of the refusals. Nonzero is normal: the phase
markers alone contribute three per cycle.

### The yield-side vocabulary, and why most stand-downs are not evidence

Legacy's coexistence-seam **yields** are the join's main left-hand side (§3b).
The `yields` block lists them per (site, reason) with the arm classification:

- a **candidacy** arm sits inside the family's arm — legacy had already decided
  the row is actionable — so an unpaired candidacy yield is a real divergence and
  surfaces as an unclassified mismatch;
- an **ownership** arm sits at the top of the row scan before any condition is
  evaluated, so it asserts "the keyed controller holds this key" and nothing about
  the row. It joins when an actor is present and is **refused when unpaired**,
  counted in `cycles.unpaired_ownership_yields`.

`keyed_start_owner` is the ownership arm, and it dominates the corpus: on the live
window ~11,900 of ~16,700 stand-downs are unpaired blanket wake skips. Scoring
them would have fabricated the D-WAKE readout entire. They are indistinguishable
from the *conditional* wake-target stand-down at `session_reconciler.go:4093`,
which writes the same reason and the same payload — **if a later slice adds an arm
discriminator to those two sites, D-WAKE's yield evidence becomes joinable and the
arm classification should be revisited.** That is a writer change and must not be
deployed mid-window.

### What `without_detail_arms` does and does not say

It counts considered cycles whose rollup reports `detailed_template_count == 0`
— cycles in which **no detail-mode template record landed**. That is not the same
as "the window ran unarmed": a quiet cycle that touched no armed template reads
identically to an unarmed one. On the live window, 88 of 729 rollups read zero —
a contiguous run of 21 before the window-open arming pass completed, then 67
isolated single cycles interleaved with armed neighbours seconds apart, which
arms cannot flap fast enough to explain.

So: a **contiguous run** of zeros is an arming gap worth excluding; **scattered
singletons** are quiet ticks. Confirm either way against `gc trace status` and
the harness narration, never from this counter alone.

### Reading history: three counters that lied, and what they proved

The day-0 readout (2026-08-12, `ga-f7v2ft.122`) was produced by a tool with three
reader-side defects, all now fixed. They are recorded here because the shape of
the mistake recurs:

- **B1 — the join had no legacy side.** The tool split rows on the
  `effect_owner` stamp alone, so every legacy record landed in
  `unowned_records` (19,673 of them in the first 17 minutes) and every family
  reported `joined: 0`.
- **B2 — the mis-arming alarm was stuck on.** The collector writes rollup
  counters into `rec.Fields`; the tool read the typed
  `rec.DetailedTemplateCount`, which **nothing in the tree ever assigns**. The
  alarm read 299-of-299 unarmed on a city whose same rollups carried
  `fields.detailed_template_count: 4`.
- **B3 — `no_evidence` was owner-presence-based.** It reported `false` beside
  `joined: 0` in every family — backwards on the one case it exists to catch.
- **B4 — decision-level parity compared the REASON.** Found while wiring the
  yield-join's fixtures. The two writers' reason vocabularies are disjoint by
  construction — every sweep condition stamps a `detector_`-prefixed reason while
  legacy stamps its own strings — so `legacyRec.ReasonCode == shadowRec.ReasonCode`
  could only ever hold on a seeded pair. On the live corpus it turned every real
  decision-level act-pair into an **unclassified WE blocker**. Decision level now
  compares the OUTCOME, which is what the §3b must-match cells actually name.

All four shipped green because the tests built records by setting struct fields
production never sets. **Any new assertion about the corpus must be written
against a production-shaped record** — one the collector wrote and the store read
back, or bytes copied from the live corpus. `cmd/gc/testdata/wd15_campaign_corpus.jsonl`
is a byte-copy of the campaign store kept for exactly that purpose; the tests
that use it are in `cmd/gc/cmd_perf_parity_join_corpus_test.go`.

### Why the act-vs-act join stays small, and what carries the window instead

With `daemon.session_reconciler = "auto"` and `effective_owner = keyed`, the
coexistence seams make legacy **yield** on every acting family rather than decide.
A legacy-act ↔ sweep pair therefore only forms where legacy did *not* yield, and
over the first three hours that happened 46 times in ~3,400 cycles. Those pairs
are kept and reported separately (`joined_acts`) because two writers deciding one
row is the strongest single piece of evidence in the corpus — but they are not the
window.

The window is the **yield-join** (owner ruling, §3b): each traced stand-down is
legacy's recorded judgment beside keyed's action, per row per tick. The live city
produces ~1,600 yield-pairs/hour, so §3b's ≥10,000-joined floor is reached in
hours. The ≥7-day residency requirement is unchanged and still governs.

Two counters make the difference visible, and both belong in any readout you
quote: `joined_yields` is the evidence; `cycles.unpaired_ownership_yields` is the
part of the stand-down traffic that proves nothing and is deliberately not scored.

## Trace archival (added day 3)

The live trace store prunes oldest-first at a **1 GiB byte cap** — ~24h of
retention at the campaign's write rate, so the store alone can never hold the
7-day corpus (it ate window days 0-1 before anyone noticed; the day-N report
JSONs plus tick-counter continuity are the surviving evidence for that span).
A detached archiver loop now sweeps rotated segments into
`campaign/trace-archive/segments/` every 4h, read-only on the live store:

```bash
pgrep -af wd15-trace-archiver          # discover (no status files)
tail campaign/trace-archive/archive.log
# relaunch if dead:
setsid nohup campaign/trace-archive/wd15-trace-archiver.sh >/dev/null 2>&1 &
```

Every daily readout must confirm the archiver is alive and the last sweep is
recent. End-of-window readouts run the join over the **archive**, not the live
store, to cover the full retained span.

## Restart policy

§3b requires the window to span **at least one controller restart** and **at
least one config reload**, with arms re-verified after each. Both are manual
steps; schedule the restart mid-window (day 3-4).

`lastPrune` is in-memory, so the first append after a restart runs a prune
sweep immediately. **Run an archiver sweep (or verify one is <4h old) before
restarting**, or the restart eats un-archived segments.

Controller restart:

```bash
campaign/gcc stop --force
tmux -L wd15-ops kill-window -t wd15ops:controller
tmux -L wd15-ops new-window -t wd15ops -n controller \
  "bash /data/cities/reconciler-campaign/campaign/run-controller.sh; echo CONTROLLER-EXITED; sleep 86400"
bash /data/cities/reconciler-campaign/campaign/arm-all.sh 30m   # re-verify
```

`gc stop --force` can report `controller stop ownership is unproven:
acknowledged controller socket still exists after lock acquisition`. Confirm
with `pgrep -af "reconciler-campaign/bin/gc start"` before concluding anything;
in practice the controller had already exited.

### Record the restart's exclusion window

Cycles between the exit and the next good arming boundary are excluded from
every §3b readout. **Write the window down when you do the restart** — stop
instant, arming-re-verified instant — and pass it to every readout that spans
it, with the start padded back **one reconcile tick (~10s)**:

```
--exclude-window <stop-minus-one-tick>/<arming-re-verified>
```

The pad is what catches a pair split across the stop: legacy writes its
decision, the process dies, and the sweep's twin is never written. Without the
pad the surviving half joins as a singleton the §3b table has nothing true to
say about. `gc perf parity-join` never infers a boundary — a window it guessed
from the corpus would grow to fit whatever is red.

**Day 4 restart (2026-08-15).** Stop ≈`06:00:0xZ`, arming re-verified complete
`06:02:35Z`, so every readout spanning it takes:

```bash
campaign/gcc perf parity-join \
  --trace-dir /data/cities/reconciler-campaign/campaign/trace-archive \
  --exclude-window 2026-08-15T05:59:50Z/2026-08-15T06:02:40Z --json
```

That window drops 980 records: 20 cycles whole, plus the rollup of
`cycle-9fac275181d8111e` (the outgoing instance's last cycle, records from
05:59:32, rollup at 05:59:55). The rollup-only case is safe by construction —
a cycle without a rollup is already excluded as truncated evidence, and it
shows up in `cycles.excluded_no_cycle_rollup` rather than leaking half-pairs.
Compare `campaign/reports/day4-archive-yield-join.json` (unfiltered: one
unclassified D-ORPHAN `orphaned`/`closed` on `dependent-rc-5kfx6` in
`cycle-6e82321f9d754602`, the new instance's *first* tick — `we_blocker=true`)
with `day4-archive-yield-join-excluded.json` (`unclassified=0`,
`we_blocker=false`).

The readout self-describes: `excluded_windows` carries each window and its
`records_excluded`, and the key is absent entirely when no window was passed,
so an unfiltered artifact stays byte-comparable with one filed before the flag
existed.

**What the window does not fix.** D-DRAIN stays at 99.219% (1 mismatch in 128
comparables) after the exclusion, because its mismatch is not a restart artifact:
it is a legacy-only `orphaned`/`stop_pending` at
`reconciler.session.drain_ack` on `dependent-rc-7mzpx`, cycle
`cycle-0860a236ff1b82bd`, tick `…-1073853-…-049152`, at
**2026-08-15T04:49:21.053Z** — 70 minutes before the stop, mid-run of the
*outgoing* instance (`cherry:1073853`). It is an ack-timing skew: the keyed twin
reads the ack handler-side while legacy polls in-tick, so the pair lands in
adjacent cycles and a same-cycle-handle join can only report it as two
singletons. Confirmed by probe: adding a throwaway
`--exclude-window 2026-08-15T04:49:21Z/2026-08-15T04:49:22Z` takes D-DRAIN to
127/0 and 100.000%. **Do not widen an exclusion window to cover it** — that
probe is a diagnostic, never an artifact; the window records a restart, not
whatever is red. D-DRAIN clears on volume or on a §3b ruling about the class,
not on filtering.

**Resolved on day 7 by the §3b ruling, not by filtering.** The owner signed
`drain_ack_adjacent_cycle_convergence` on 2026-08-17/18: this cycle, and the two
that joined it (`s-rc-wisp-y73064d` cycle-41b467cd627d719e,
`s-rc-wisp-d30uo8f` cycle-03d51f88678dbb50), each proved their keyed twin against
the archive and are now incomparable, taking D-DRAIN to 351/0 and 100.000% with
`bar_status: ok`. The exclusion-window probe above stays a diagnostic and was
never filed.

Config reload: edit `city.toml` (or `pack.toml`), then

```bash
campaign/gcc reload
bash /data/cities/reconciler-campaign/campaign/arm-all.sh 30m
```

`No config changes detected` means the controller had already picked the change
up on its own tick — that still counts as the reload, and the arms still get
re-verified.

The trace store survives both. Do **not** clear it mid-window: that discards the
corpus the readout is built from.

## When something dies

- **Controller window shows `CONTROLLER-EXITED`.** Restart per above and re-arm.
  Cycles between the exit and the next good arming boundary are excluded — record
  the window and pass it to every readout that spans it (see
  [Record the restart's exclusion window](#record-the-restarts-exclusion-window)).
- **Arming window shows `ARMING-EXITED`.** The report was written on the way out
  (`campaign/reports/arming-report.json`); read its `gaps` before relaunching.
  Relaunch with `campaign/run-arming.sh`. The remaining window shortens, so drop
  `GC_CAMPAIGN_WINDOW` to what is left.
- **Workload window shows `WORKLOAD-EXITED`.** The driver traps its own action
  failures and never exits on them, so an exit means the shell itself died.
  Relaunch it; the corpus is unaffected, only the traffic rate.
- **The city refuses to start.** Report it. A campaign on a mis-armed or
  half-running city is void by construction — do not improvise around a broken
  window.

## Teardown (after WE sign-off)

```bash
campaign/gcc stop --force
tmux -L wd15-ops kill-session -t wd15ops
tmux -L wd15-campaign kill-session -t <each remaining agent session>
```

Never `tmux kill-server`, and never on the default socket.
