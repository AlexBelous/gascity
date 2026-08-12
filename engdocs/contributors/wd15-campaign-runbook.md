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
2. `no_evidence` — must be `false`. If it is `true` and
   `cycles.without_detail_arms` is nonzero, the window ran unarmed: that is
   positive proof of a mis-armed city, not a parity result.
3. `cycles.excluded_record_budget_exceeded` and `cycles.excluded_no_cycle_rollup`
   — dropped by design (§3 operational rule / truncated evidence).
4. `cycles.unowned_records` — records with no `effect_owner`. Nonzero on a
   post-WD.2 corpus wants an explanation.
5. Per family: `match_rate` against the 0.995 bar, then `triage` for the
   classified divergences and `unclassified` for the samples that need work.

**One unclassified mismatch is a WE blocker.** Triage it, extend the §3b table
with evidence, or fix the detector — then re-run the window for that family. Do
not bucket it.

### Two readout counters that lie today

Both were found by the day-0 readout on 2026-08-12 and are filed on
`ga-f7v2ft.122`. Until they are fixed, read around them:

- **`cycles.without_detail_arms` is stuck at the considered-cycle count.** The
  collector writes the cycle rollup counters into `rec.Fields`
  (`session_reconciler_trace_collector.go:970-983`, serialized as the nested
  `fields` object) and never sets the typed `rec.DetailedTemplateCount`;
  `parity-join` reads the typed field
  (`cmd_perf_parity_join.go:269`). So the campaign's own "did this window run
  armed?" alarm reads 299-of-299 unarmed on a city whose same rollups carry
  `fields.detailed_template_count: 4`. Verify arming from `gc trace status`
  and from `fields.detailed_template_count` in the segments, not from this
  counter. (`drop_reason_counts` is unaffected — the collector does set that one
  typed field, so the `record_budget_exceeded` exclusion works.)
- **`no_evidence` is owner-presence-based, not join-based**
  (`cmd_perf_parity_join.go:219`: `owned == 0`). A corpus containing
  detector-shadow and keyed records but no legacy ones reports
  `no_evidence: false` beside `joined: 0` in every family. Treat all-zero
  `joined` as no evidence regardless of the flag.

### The join has no legacy side yet

At window open the readout joins **nothing**, in every family, and it always
will until one gap closes: **no production code stamps
`fields.effect_owner = "legacy"`.** `parity-join` distinguishes rows by that
field (`cmd_perf_parity_join.go:36,298`); the keyed handlers stamp `"keyed"` and
the sweep stamps `"detector-shadow"`, but the legacy reconciler stamps nothing,
so every legacy row lands in `cycles.unowned_records` and is never guessed at.
`parityJoinOwnerLegacy` has exactly one producer in the tree — the tool's own
tests.

Consequence for the schedule: **records written before that stamp lands can
never join**, so the §3b seven-day clock does not start until it does. The city
below is correct, armed, and accumulating a real keyed + detector-shadow
corpus; it is not yet accumulating joinable evidence. Filed on `ga-f7v2ft.122`.

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
