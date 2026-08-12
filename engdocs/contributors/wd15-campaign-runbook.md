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

Or the human table without `--json`. `--since 168h` scopes to the window;
`--template` narrows to one selector. The command exits nonzero on
`no_evidence` or `we_blocker` and the schema is `x-gc-jsonl`, so **capture
stdout regardless of exit code.**

Read in this order:

1. `shadow_effect_violations` — must be `0`. Any value breaks the read-only
   invariant the campaign rests on and is an unconditional WE blocker.
2. `no_evidence` — must be `false`. It is join-based: true whenever every family
   reports `joined: 0`, however many owned records the corpus holds.
3. `cycles.excluded_record_budget_exceeded` and `cycles.excluded_no_cycle_rollup`
   — dropped by design (§3 operational rule / truncated evidence).
4. `cycles.legacy_by_elimination` and `cycles.unowned_records` — see the owner
   model below.
5. Per family: `match_rate` against the 0.995 bar, then `triage` for the
   classified divergences and `unclassified` for the samples that need work.

**One unclassified mismatch is a WE blocker.** Triage it, extend the §3b table
with evidence, or fix the detector — then re-run the window for that family. Do
not bucket it.

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
is counted and surfaced under `owner_absence`, never binned:

| Disposition | Meaning |
|---|---|
| `legacy` | §1 decision site, legacy reason, has a session identity — attributed, counted in `cycles.legacy_by_elimination` |
| `phase_marker` | §1 phase site. Legacy writes one cycle-level marker per tick with no session identity; binning it would have manufactured a phantom legacy row per cycle in D-DUP and D-STRANDED, whose only sites are phase sites |
| `keyed_seam_yield` | legacy stepped aside for the keyed owner (`keyed_start_owner` and siblings) — the effect is keyed's, not legacy's |
| `unattributable` | keyed-owned or shared-writer site (§1 #27) — absence says nothing about legacy |
| `no_session_key` | nothing to join on |

`cycles.unowned_records` is the sum of the refusals. Nonzero is normal: the phase
markers alone contribute three per cycle.

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

All three shipped green because the tests built records by setting struct fields
production never sets. **Any new assertion about the corpus must be written
against a production-shaped record** — one the collector wrote and the store read
back, or bytes copied from the live corpus. `cmd/gc/testdata/wd15_campaign_corpus.jsonl`
is a byte-copy of the campaign store kept for exactly that purpose; the tests
that use it are in `cmd/gc/cmd_perf_parity_join_corpus_test.go`.

### Why `joined` stays small on an auto-mode city

Fixing B1 populated the legacy side but did not make the join large, and the
reason is structural rather than a tool defect. With
`daemon.session_reconciler = "auto"` and `effective_owner = keyed`:

- the coexistence seams make legacy **yield** on every acting family rather than
  decide (2,233 `keyed_start_owner` yields in the first 45 minutes), and
- the sweep stamps a routed condition `"keyed"`, not `"detector-shadow"`.

A legacy↔detector-shadow pair therefore only forms where legacy did *not* yield
**and** the sweep did *not* route. Over the first 45 minutes that intersection
occurred **once** in 729 cycles. Whether §3b's ≥10,000-joined-cycle bar is
reachable in auto mode — or whether the acting families' parity is instead
carried by the yield plus the keyed handler's own journey evidence — is a
campaign-design question recorded on `ga-f7v2ft.122`, not something the tool
should paper over by widening the join.

## Restart policy

§3b requires the window to span **at least one controller restart** and **at
least one config reload**, with arms re-verified after each. Both are manual
steps; schedule the restart mid-window (day 3-4).

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
  Cycles between the exit and the next good arming boundary are excluded.
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
