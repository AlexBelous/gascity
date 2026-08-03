---
title: Agent Start Latency Handoff
description: Fixed-SHA measurement contract, accepted results, rejected experiments, and remaining work for agent startup latency.
---

## Purpose

This is the restart point for the agent-session startup performance work on the
keyed reconciler branch. It records the exact measurement contract and the
negative results so a fresh session can continue without reconstructing the
last 25 hours of experiments.

Do not treat a correctness-green change as a performance win. Keep an
optimization only after an exact-binary, sequential 30-sample cohort is
`ok=true`, `baseline_eligible=true`, 30/30 terminal-correct, and improves both
p50 and p95 of `start_to_prompt_delivered`. A partial cohort is not evidence.

## Repository checkpoint

- Worktree: `/data/projects/gascity-reconciler-minimal-20260726`
- Branch: `feature/reconciler-minimal-local-20260726`
- Last code checkpoint, and parent of this documentation-only commit:
  `4c473224f538bdae37b74357849b9ad73dd2bb68`
- Code checkpoint subject: `perf(start): reuse verified store for SessionStart mail`
- At that checkpoint the branch tracked `origin/main`, was ahead 128 and behind
  27, and `origin/main` resolved to
  `c4880aef5f2c6be534358f09354c1d249e32161c`.
- The tree was clean before this handoff was added.
- Nothing on this feature branch has been pushed and no PR exists. Keep it that
  way until the owner explicitly changes that instruction.

The current retained optimization stack is:

1. `6aa8322281` removes the redundant pre-intent controller wake.
2. `9eb8bd1161` reuses the already-open SessionStart mail provider.
3. `4c473224f5` reuses the already-verified base store for auto-handoff and
   ordinary SessionStart mail.

If the branch is rebased or any dependency changes, the existing cohorts no
longer certify the new tree. Rebuild the exact binary and collect a new
baseline before comparing another candidate.

## Measurement contract

### Harness

The live harness and report contract are test-owned so they cannot change
production behavior:

- `test/acceptance/worker_inference/startup_latency_perf_test.go` owns the live
  exact-binary run, observations, cleanup proof, and atomic report publication.
- `test/acceptance/worker_inference/startup_latency_report_impl_test.go` owns
  the versioned report schema, validation, outcome accounting, and percentile
  calculations.
- `test/acceptance/worker_inference/startup_latency_report_test.go` pins the
  report contract and its failure behavior.
- `TESTING.md`, under "Live worker inference tests", documents provider setup.

The harness runs one excluded warmup followed by 30 sequential measured
starts. Every sample is correlated by an opaque run identity and durable
session identity. It measures start initiation through runtime creation, CLI
process execution, CLI readiness, prompt delivery, model output, first-turn
completion, durable retirement, and tmux absence. Model inference spans are
reported but excluded from the optimization KPI.

### Exact run recipe

Use a new absolute output directory for each candidate. Build once, then point
both the acceptance driver and the managed SessionStart hook at those exact
bytes:

```bash
cd /data/projects/gascity-reconciler-minimal-20260726

CANDIDATE=next-candidate # Replace with a unique SHA or experiment name.
OUT="/data/tmp/agent-start-$CANDIDATE"
GC_BIN="$OUT/gc"
BD_BIN=/data/tmp/agent-start-frozen-bd-758beca4f62c/bd

test ! -e "$OUT"
mkdir -p "$OUT"
GOWORK=off CGO_ENABLED=0 go build -o "$GC_BIN" ./cmd/gc
sha256sum "$GC_BIN" "$BD_BIN"

PATH="$(dirname "$BD_BIN"):$PATH" \
PROFILE=claude/tmux-cli \
GC_ACCEPTANCE_KEEP=1 \
GC_ACCEPTANCE_GC_BIN="$GC_BIN" \
GC_ACCEPTANCE_BD_BIN="$BD_BIN" \
GC_RUN_AGENT_START_PERF=1 \
GC_AGENT_START_PERF_SAMPLES=30 \
GC_AGENT_START_PERF_REPORT="$OUT/report-30.json" \
GOWORK=off \
go test -tags=acceptance_c ./test/acceptance/worker_inference \
  -run '^TestAgentStartLatencyPerf$' -count=1 -timeout=3h -v
```

`GC_ACCEPTANCE_GC_BIN` is load-bearing. The harness resolves the managed hook's
`gc`, compares its bytes to the measured binary, and fails setup if they
differ. A development build reports `gc_commit=unknown`, so use the binary hash
rather than the version's commit field as the identity.

`GC_ACCEPTANCE_BD_BIN` is also load-bearing, and the same directory must prefix
`PATH`. The first setting pins acceptance helpers; the second pins subprocess
calls made by `gc`, hooks, and the test city. Setting only one can recreate the
v59-decoder/v53-CLI mismatch this work already encountered.

The frozen bd binary is:

- Path: `/data/tmp/agent-start-frozen-bd-758beca4f62c/bd`
- Module: `github.com/steveyegge/beads`
  `v1.1.1-0.20260729081659-3789a6658060`
- Source ref: `3789a6658060690cd00b88336b279da869ae0d07`
- Raw `sha256sum`:
  `758beca4f62c8c63f7c0e12d429cc4a06afc8c9da9187bf9226cf3bb41163781`
- Report-framed hash:
  `e110937cb4155de62c89f6632a8ed4eb5df332e3e506bde7784e559e6201e792`

The report-framed hash intentionally differs from raw `sha256sum`: the harness
hashes the file basename, NUL separators, and contents. Compare raw hashes to
raw hashes and report hashes to report hashes.

`GC_ACCEPTANCE_KEEP=1` is recommended. On a failure, preserve the report and
the city named by `provenance.city_path` before cleanup. Never discard an
invalid cohort until its correctness failure is classified.

### Cohort eligibility and retention

A cohort counts only when all of these are true:

- `ok=true` and `baseline_eligible=true`;
- `expected_samples=30` and `outcome_counts.completed=30`;
- incomplete, error, canceled, and not-attempted counts are all zero;
- all 30 samples prove expected output, assistant output after the prompt,
  transcript idle, no open tool use, no pending interaction, durable session
  retirement, and tmux session absence;
- profile, provider, runtime provider, reconciler mode, readiness strategy,
  agent config, and non-candidate binary provenance match the comparison
  cohort;
- both p50 and p95 of `start_to_prompt_delivered` improve. Also inspect
  `cli_ready_to_prompt_delivered`, p99, and max; do not hide a tail regression.

The total `start_to_first_turn_complete` metric is retained as user-experience
context, but it includes model inference and is explicitly excluded from the
optimization gate. Do not reject or retain infrastructure changes based on
model-latency variance.

## Accepted baseline and current result

Both reports below are `ok=true`, `baseline_eligible=true`, and 30/30
terminal-correct with zero errors.

| Metric | Cohort | p50 | p95 | p99 / max |
| --- | --- | ---: | ---: | ---: |
| Start to prompt delivered (optimization KPI) | Accepted baseline | 8.980s | 9.474s | 9.924s |
| Start to prompt delivered (optimization KPI) | Current `4c473224f5` | 8.203s | 8.522s | 8.539s |
| CLI ready to prompt delivered | Accepted baseline | 4.495s | 4.688s | 4.975s |
| CLI ready to prompt delivered | Current `4c473224f5` | 3.810s | 4.043s | 4.122s |
| Start to first turn complete (inference included; context only) | Accepted baseline | 20.142s | 23.483s | 24.903s |
| Start to first turn complete (inference included; context only) | Current `4c473224f5` | 20.056s | 25.742s | 26.660s |

The retained store-reuse change improves start-to-prompt p50 by 0.778s (8.7%)
and p95 by 0.953s (10.1%). It improves the targeted CLI-ready-to-prompt span
by 0.685s (15.2%) at p50 and 0.645s (13.8%) at p95.

Accepted baseline, after `9eb8bd1161`:

- Report: `/data/tmp/agent-start-mailreuse-f86ef30ea521/report-30.json`
- Report SHA-256:
  `8959d26cbd270a783d08eab640731222739f835417b50eba5f9019fc7be1a56c`
- Frozen gc raw SHA-256:
  `699ed9a4653b6ffb36be08797997bb16d07022276a4a742cfde2d5fbe56f643e`
- Report-framed gc hash:
  `615fdf845a06bdfee8afd0cca8a825f01798986dd7a460c7a94f6d16e119f038`

Current result, after `4c473224f5`:

- Report:
  `/data/tmp/agent-start-storereuse-c85f9d3ee6-rerun/report-30.json`
- Report SHA-256:
  `ebddacfe3646fb17a25d7b5c925b7f71652b0a36ba1e6ea42d5cae62495c0325`
- Frozen gc raw SHA-256:
  `fb49928eda983d3c16e98068a74d8ef3c6cc9e8931d0a23423670565bbb1c7fc`
- Report-framed gc hash:
  `6da8cd1a1181039d881c01f0c1e0d8a73a0d49ccf602e34b3166916f1774ae81`

The current p50 phase budget is approximately 3.606s from start to runtime,
0.814s from runtime to provider CLI exec, 0.006s from CLI exec to ready, and
3.810s from CLI ready to prompt delivery. Percentiles are computed
independently and therefore do not add exactly. `controller_start_total` is
1.656s p50 and is nested inside start-to-runtime.

## Rejected or invalid experiments: do not repeat

### Parallel BdStore TierBoth reads (`42f286ead5`)

This overlapped the two `bd list`/query legs. Focused, race, package,
pre-commit, vet, and a three-Sol review all passed, but the exact v59
bd/Dolt/tmux adoption smoke failed 2/2 with durable session identity churn and
non-convergence. Parallel read legs are not behaviorally safe with the current
store/session startup composition. Do not retry this shape unless one coherent
read snapshot or a proven backend primitive replaces the independent legs.

Evidence:
`/data/tmp/reconciler-adoption-benchmark-20260801-after-42f286/`

### Elide the post-init scope Ping (`ga-f7v2ft.42.3`)

This was 30/30 correctness-clean but failed the performance gate.
`starting_bead_store` p50/p95 regressed from 6.470s/6.940s to
7.330s/7.490s; total p50/p95 regressed from 21.011s/22.494s to
21.693s/23.176s. It was fully reverted. Do not retry it based only on the Ping
looking redundant.

Evidence:

- Candidate: `/data/tmp/reconciler-adoption-proof-after/report-30sample-fixed.json`
- Baseline: `/data/tmp/reconciler-adoption-benchmark-20260801/report-30sample.json`

### Batch SessionStart unread-mail lookup (`ga-f7v2ft.78.4`)

The hypothesis was that three recipient lookups dominated ordinary mail.
Tracing disproved it: the batched multi-recipient fetch cost only 44.8ms and
54.5ms, while `openCityMailProvider` cost 1.885s and 1.766s, about 97% of the
ordinary-mail phase. The one-sample start result was within or worse than
baseline variance. The change was fully reverted; batching a roughly 45ms
operation is not the next optimization target.

Evidence:

- `/data/tmp/agent-start-mailbatch-5cef47edbc39/report-1.json`
- `/data/tmp/agent-start-mailbatch-trace-20260803/report-1.json`

### First store-reuse cohort

The first full store-reuse run was invalid, not a negative performance result.
Sample 18 failed cleanup after 17 completed samples with
`closed=true`, `metadata_state=awake`, `tmux_absent=true`, and
`durable_session_retired=false`. The remaining 12 samples were not attempted.
Its partial percentile improvements must never be cited.

- Invalid report:
  `/data/tmp/agent-start-storereuse-c85f9d3ee6-final/report-30.json`
- Invalid report SHA-256:
  `f38024f0a1aacfc5b9ceb68df8e4ccddbcd46c87d0217926656235d61ea22b60`
- Candidate incident bundle:
  `/data/tmp/agent-start-retire-incident-c85f9d3ee6/`

The same terminal-state failure then reproduced without store reuse on clean
reverted code checkpoint `c85f9d3ee6`, at sample 12. That proves the failure is
independent of the optimization:

- Reverted-tree report:
  `/data/tmp/agent-start-retire-incident-reverted-c85f9d3ee6/report-30.json`
- Reverted-tree report SHA-256:
  `0afbde919eb8ecc88b031aaf64fd4f6c21a75c77b8d46b6a2e44d41d8a5f51ad`
- Preserved reverted city and logs:
  `/data/tmp/agent-start-retire-incident-reverted-c85f9d3ee6/`

The product defect is `ga-f7v2ft.78.6`. The store-reuse idea was subsequently
rerun as a fresh 30/30 cohort and accepted at `4c473224f5`. Do not rerun the
invalid cohort to re-decide causality; fix the independently reproduced defect.

## Open work, in priority order

### 1. Fix `ga-f7v2ft.78.6`: closed rows retaining awake state

This is a P1 correctness defect, not benchmark noise. A session can become
durably `closed=true` and lose its tmux runtime while retaining
`metadata_state=awake`. Candidate and reverted cohorts both reproduced it.
Fix terminal close atomicity/monotonicity and stale-writer fencing in a separate
commit from any performance change. The bead contains deterministic-test and
three-cohort real-v59 acceptance criteria.

### 2. Profile the remaining approximately 3.8s SessionStart prime span

The current `cli_ready_to_prompt_delivered` p50 is 3.810s and p95 is 4.043s.
This is essentially the synchronous `gc prime` SessionStart path and is now the
largest directly actionable Gas City-controlled pre-prompt span. Profile its
subphases first, choose one dominant operation, make one narrow change, and
repeat the exact 30-sample A/B contract. Do not return to the rejected parallel
read, Ping-elision, or 45ms mail-batching shapes.

### 3. Measure and reduce the approximately 2.5s UserPromptSubmit hook

The managed UserPromptSubmit `gascity-nudge-drain` hook adds roughly 2.5s after
startup prompt delivery and is outside the current
`start_to_prompt_delivered` KPI, but it delays inference in the experience the
user actually feels. It runs the bounded `gc hook run ... nudge drain --inject`
path. Extend the trace so this time is separately attributable before changing
it, then apply the same 30/30 correctness and percentile gate. Do not hide it by
redefining prompt delivery or first-turn timestamps.

### 4. Resolve the five council-open Majors from `49e348a8d0`

The Sol council found six Majors and no Blockers on `49e348a8d0`.
`62b2548570` fixed the production reachability of native conditional writes,
including a real `OpenBestAvailable` require-mode proof. The following five
remain council-open:

1. **Provider reload can interrupt confirmed-STOP durable finalization.** The
   original code released `drainAckStopKeys` before completion admission.
   `c85f9d3ee6` now retains ownership through the callback, but the exact
   council acceptance proof was not added: barrier after confirmed death and
   before completion admission, concurrent provider reload, reload deferral,
   then durable closed/drained completion. Treat this Major as open until that
   reload-specific test passes and the council discharges it.
2. **A controller crash after STOP can permanently strand a pool slot.** If the
   runtime is killed before durable finalization, restart attempts to recover
   acknowledgement provenance from runtime metadata that no longer exists.
   The row can remain uncertain and stop-pending forever while counting against
   pool capacity. Persist enough provenance with the stop-pending CAS, or allow
   a canonical revision-fenced row plus complete dead-runtime evidence to
   finalize. Add a restart-with-runtime-absent proof that issues no second STOP.
3. **Dead-runtime finalization drops the captured row-revision fence.** After a
   complete dead observation, `session_start_reconcile.go` calls
   `finalizeDrainAckStoppedSession` without threading the
   `drainAckStopPendingFence` into its close/update writes. A concurrent wake
   can advance the row and be overwritten by stale finalization. Use
   conditional close/update operations and add a forced interleaving test in
   which the stale finalizer loses without mutating the newer row.
4. **The canonical integration shard silently skips the real-bd v59 leg.**
   `scripts/test-integration-shard` runs tests under `env -i` without forwarding
   `GC_TEST_BD_BIN`; `native_v59_start_stop` therefore reports SKIP while its
   parent test passes. Build the `deps.env` `BD_CURRENT_REF`, forward the exact
   path, make the dedicated shard fail rather than skip when unavailable, and
   add a runner contract that requires PASS and rejects SKIP.
5. **The linked v59 library can migrate a database beyond the default bd
   binary.** `go.mod` and `BD_CURRENT_REF` are on the v59-capable
   `3789a6658060` tree, but `deps.env` still declares `BD_VERSION=v1.1.0`, whose
   published/default binary knows only through v53. A native writable open can
   migrate a shared database and make the default CLI/fallback unable to open
   it. Align every packaged/default binary pin with the library or prevent the
   incompatible migration, and add a cross-version open/list plus rollback
   boundary test using the exact distributed binary.

Do not mix these STOP/schema corrections into the next prime performance
commit. Each needs its own deterministic failure, minimal fix, focused proof,
and checkpoint commit.

## Failure classification rule

Every red observed on this branch is branch-owned unless the same command,
environment, toolchain, and fixture reproduce it on a clean `origin/main`
worktree. "The current two-file slice did not touch that package" does not make
a failure pre-existing; earlier commits on this branch still own it.

Before classifying a red as pre-existing:

1. Search branch history for the failing symbol or contract:
   `git log origin/main..HEAD -- <path>` and, when useful,
   `git log -S '<symbol>' origin/main..HEAD -- <path>`.
2. Reproduce the exact failure on clean `origin/main` under the same
   environment and pinned binaries.
3. Record both SHAs, the exact commands, and outputs. Only then may the failure
   be excluded as genuinely pre-existing.

If it does not reproduce on `origin/main`, fix it or file it as branch-owned
work before retaining another optimization. Do not update a golden or weaken a
terminal assertion merely to make the branch green.

## Resume checklist

1. Start a fresh session in the worktree above and verify this docs commit has
   `4c473224f5` as its parent.
2. Verify the tree is clean and do not push or open a PR.
3. Read `ga-f7v2ft.78.6` and the two preserved incident bundles before editing
   terminal-state code.
4. Keep correctness fixes, instrumentation, and each performance candidate in
   separate commits.
5. For performance work, freeze binaries first and accept only a comparable
   30/30 `baseline_eligible` cohort.
