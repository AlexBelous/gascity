# WD.15 campaign evidence

The durable copy of the WD.15 parity-campaign artifact chain cited by
[WE-SIGNOFF.md](../WE-SIGNOFF.md). Every JSON here is a byte-identical copy of
`/data/cities/reconciler-campaign/campaign/reports/`, taken 2026-08-19 while
the campaign city was still live — that directory is destroyed by the teardown
in WE-SIGNOFF §5.3, so until this landed the §3b chain existed only inside a
directory scheduled for deletion.

Nothing here is regenerated. Treat these files as immutable: they are the
evidence a signature is taken against, and the day-0/1 span can no longer be
re-derived from raw traces at all (WE-SIGNOFF §4.2).

## The final readout

| File | What it is |
|---|---|
| `we-final-yield-join.json` | The §3b bar evaluation the signature is taken against: 79,382 joined rows (77,648 yield + 1,734 act) over the closed 168h window, `bar_met: true`, `we_blocker: false`, `shadow_effect_violations: 0`, 0 unclassified mismatches. Corpus: `campaign/trace-archive/`, not the live WAL. |
| `arming-report.json` | The 168h arming harness run — 1,993 sample boundaries, 2,376 re-arms, `gaps: null`, `armed: true`. Produced by `campaign/run-arming.sh`, not by the parity-join tool. |

## The day chain

The per-day readouts, and the **only** surviving evidence for the pre-archiver
day-0/1 span (WE-SIGNOFF §4.2 — the live WAL prunes at a 1 GiB cap, ~24h, and
the rsync archiver only exists from day 3).

`day0-parity-join.json`, `day1`–`day3`, `day4` (plus `day4-post-restart`,
`day4-archive`, `day4-archive-excluded`, `day4b`, `day4c`), `day5`, `day6`,
`day6b`, `day7`.

`day0` carries the older `parity-join` name because it predates the commit that
gave the tool a legacy side; everything after is a yield-join.

## Tool provenance

The parity-join JSON schema has no provenance block — the artifacts do not
record the sha that produced them (a known gap; `ga-f7v2ft.160` covers the
stale `schemas/perf/parity-join/result.schema.json`). The mapping below is
derived by matching each file's mtime against the commit history of
`cmd/gc/cmd_perf_parity_join*.go`. It is an inference, not a stamp. It is
corroborated at the one point where an independent record exists:
`we-final-yield-join.json` derives to `b19d84be97`, which is the sha
WE-SIGNOFF §1 states from the ledger.

| Artifact(s) | Tool sha in effect |
|---|---|
| `day0-parity-join.json` | `66b9da9baf` (pre-legacy-side tool) |
| `day1-yield-join.json` | `9aab63574b` — join the keyed act against legacy's yield |
| `day2`, `day3` | `5c7d022e99` — deadline-crossing race triage |
| `day4-yield-join.json` | `45b742e295` — day-2 shape triage |
| `day4-post-restart`, `day4-archive` | `e70bc64ede` — pre-wake supersede + one-tick orphan lead |
| `day4-archive-yield-join-excluded` | `6b0c1073d6` — restart-gap exclusion, run from the working tree; the commit landed 4 min after the artifact |
| `day4b`, `day4c` | `6b0c1073d6` (`a789dbaab3` landed 3 min after `day4c`) |
| `day5`, `day6`, `day6b` | `a789dbaab3` — `no-data` vs false-0% JSON disclosure (`e7e9e7a3dd` landed 3 min after `day6b`) |
| `day7`, `we-final-yield-join.json` | `b19d84be97` — D-DRAIN adjacent-cycle keyed-twin decision |

Three artifacts (`day4-archive-excluded`, `day4c`, `day6b`) were produced
minutes *before* the fix they motivated was committed. That ordering is the
normal shape of same-day triage — observe, fix, re-run — and is recorded here
rather than smoothed over.

## Exclusion windows

Self-described in `we-final-yield-join.json` `excluded_windows[]`; boundaries
were supplied at event time per the runbook, never inferred after the fact.

| Window (UTC) | Records | Why |
|---|---|---|
| `2026-08-15T05:59:50Z` → `06:02:40Z` | 980 | Planned controller restart (the §3b restart requirement); binary rotated `2b7f92a9a2` → `37e34b5788`, zero engine files in the diff; arms re-verified 06:02:35Z |
| `2026-08-15T08:44:40Z` → `08:49:00Z` | 1,821 | Unplanned restart — **operator error**: SIGQUIT of a healthy controller after a misdiagnosed outage that did not exist. Disclosed in full in WE-SIGNOFF §2 |
| `2026-08-19T03:39:17Z` → `2026-08-20T00:00:00Z` | 54,806 | Not an event: truncates post-close records so the readout covers exactly the 168h window. Large because the final day ran ~10x pace as host load dropped |

The 2026-08-16 config reload and the 2026-08-16 campaign tmux-server death are
**not** excluded — the reload was non-disruptive, and the tmux outage was
survived live and produced two adjudicated day-6 shapes (WE-SIGNOFF §2, §3).

## The perf A/B

| File | What it is |
|---|---|
| `we-perf-reconciler-compare.json` | The DESIGN.md §4 / WD.15 `gc perf reconciler-compare` A/B precondition. 300 pairs (`--iter 100`), 0 mismatches, 0 errors on either arm, 3/3 actions covered, `ok: true`. Tool `4c75ef9dc2`, `dirty: false`. |
| `we-perf-reconciler-compare-iter20.json` | The same A/B at the tool's default `--iter 20` (60 pairs), run immediately before. Same sign and magnitude on all three cohorts — the larger run is not a cherry-pick. |

**Read this artifact for what it is.** `reconciler-compare` is a hermetic
synthetic micro-benchmark, not a replay of this campaign's corpus: it builds a
throwaway workspace under `TMPDIR`, uses `beads.MemStore` /
`beads.NewAtomicCloseMemStore` and `runtime.Fake`, and its own provenance
string declares `excludes=tmux,Dolt,wake-socket/IPC,contention`. It measures
action-needed→provider-entry latency on fresh single-session alternating pairs.
It touches no city, no Dolt, and no trace corpus. It is the A/B DESIGN.md §4
asks for, and it is *not* additional evidence about the campaign window.

Result (p50, `--iter 100`): keyed **start** 1.302 ms vs legacy 0.516 ms
(2.52x slower); keyed **stop** 0.390 ms vs 0.510 ms (24% faster); keyed
**nudge** 1.204 ms vs 1.147 ms (5% slower, with a better tail — p99 2.641 ms
vs 3.024 ms).

The start regression is real and reproduced across both runs. Its consequence
is bounded by scale: the worst keyed sample anywhere in the run is 4.890 ms
against the 30s debounce that DESIGN.md §4 converts into an absolute latency
budget at WE — about 0.016% of it. The keyed arm pays a fixed controller
admission cost that the legacy in-line path does not; at these magnitudes that
buys back tail stability on stop and nudge.

## Raw corpus — NOT copied here, and a teardown precondition

`/data/cities/reconciler-campaign/campaign/trace-archive` (**4.5 GiB**,
measured 2026-08-19) is deliberately not in this directory. It is the raw
cycle corpus `we-final-yield-join.json` was computed from, and the only thing
that could support **re-triage under a rule adopted later** — the day-3
incident was resolvable only because raw cycles still existed.

Before the campaign city is torn down (WE-SIGNOFF §5.3):

1. Stop the detached archiver loop (`pgrep -af wd15-trace-archiver`) before
   removing its target.
2. Move the archive out of the city directory, or consciously discard it —
   **recorded, not defaulted**. This decision is still open; landing this
   directory does not close it.

Teardown itself is in
[wd15-campaign-runbook.md](../../../contributors/wd15-campaign-runbook.md)
§ "Teardown". Never `tmux kill-server`, and never on the default socket.
